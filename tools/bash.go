package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/sandbox"
)

// Bash limits, shared with tests.
const (
	// BashDefaultTimeout is the timeout when the model does not pick one.
	BashDefaultTimeout = 2 * time.Minute
	// BashMaxTimeout is the largest timeout a call may request.
	BashMaxTimeout = 10 * time.Minute
	// BashMaxOutput caps the captured output, in bytes.
	BashMaxOutput = 30_000
)

// BashTool runs a shell command.
type BashTool struct {
	Base
	// Root is the working directory for every command.
	Root string
	// Timeout overrides BashDefaultTimeout when non-zero.
	Timeout time.Duration
	// Env carries extra environment variables applied on top of the process
	// environment.
	Env []string
	// jobs is the background job table. It is created with the tool and
	// shared with the wait tool, so both observe the same jobs.
	jobs *BackgroundJobs
}

// NewBashTool returns a bash tool rooted at root.
func NewBashTool(root string) *BashTool {
	tool := &BashTool{Root: root, jobs: NewBackgroundJobs()}
	tool.Base = NewBase("bash",
		"Run a shell command and return its combined output. Prefer dedicated "+
			"tools over shell commands when one exists.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The command to run.",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Timeout in milliseconds.",
					"minimum":     1.0,
					"maximum":     float64(BashMaxTimeout.Milliseconds()),
				},
				"description": map[string]any{
					"type":        "string",
					"description": "A short description of what the command does.",
				},
				"background": map[string]any{
					"type":        "boolean",
					"description": "Start the command as a background job and return its id immediately; collect output with wait.",
				},
			},
			"required": []string{"command"},
		},
		agent.Approval{Tier: agent.TierExec, Reason: "runs a shell command"},
	)
	return tool
}

// Jobs returns the background job table, so the wait tool can observe the
// same jobs this tool starts.
func (t *BashTool) Jobs() *BackgroundJobs {
	if t.jobs == nil {
		t.jobs = NewBackgroundJobs()
	}
	return t.jobs
}

// Exclusive implements agent.Tool: concurrent foreground shell commands
// would interleave their output and compete for the working tree.
//
// A background spawn is the exception: it starts a process and returns
// immediately without producing output, so there is nothing to interleave.
// Resolving this per call is what lets a model launch several jobs at once
// while foreground runs stay serialised.
func (t *BashTool) Exclusive(args map[string]any) bool {
	background, err := boolArg(args, "background", false)
	if err != nil {
		// Malformed input is refused by Execute; until then the safe answer
		// is exclusive, because a call that might run in the foreground must
		// not overlap one that does.
		return true
	}
	return !background
}

// Capability implements agent.CapabilityReporter for the admission layer.
//
// The tool's declared tier is exec, but a shell command can also mutate the
// repository. Classifying the specific command lets the runtime refuse a
// mutation attempted from a workspace that may execute but not change
// (I3, §5.3). Malformed arguments classify as write, so a call that might
// mutate is denied outside BUILD rather than admitted on a parsing failure.
//
// A command classified exec is still not proof of read-only behaviour. The
// authorization decision's ReadOnly bit is what the tool enforces with an OS
// sandbox; this method only improves the risk and mutation evidence.
func (t *BashTool) Capability(args map[string]any) agent.Tier {
	command, err := strArg(args, "command", "")
	if err != nil || strings.TrimSpace(command) == "" {
		return agent.TierWrite
	}
	return commandCapability(command)
}

// Execute implements agent.Tool.
func (t *BashTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	command, err := strArg(args, "command", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if strings.TrimSpace(command) == "" {
		return agent.ErrorResult("command must not be empty")
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = BashDefaultTimeout
	}
	if rawMs, err := intArg(args, "timeout", 0); err != nil {
		return agent.ErrorResult("%v", err)
	} else if rawMs > 0 {
		requested := time.Duration(rawMs) * time.Millisecond
		if requested > BashMaxTimeout {
			requested = BashMaxTimeout
		}
		timeout = requested
	}

	mutates := t.Capability(args) == agent.TierWrite && !ctx.ReadOnly

	if background, err := boolArg(args, "background", false); err != nil {
		return agent.ErrorResult("%v", err)
	} else if background {
		return t.spawnBackground(command, timeout, ctx.ReadOnly, mutates)
	}

	// The request context is the parent so a run-level abort still kills the
	// process, while the per-command timeout stays independent of it.
	runCtx, cancel := context.WithTimeout(ctx.Ctx, timeout)
	defer cancel()

	cmd, err := sandbox.Command(runCtx, sandbox.Spec{
		Argv:     []string{"sh", "-c", command},
		Dir:      t.Root,
		Root:     t.Root,
		ReadOnly: ctx.ReadOnly,
	})
	if err != nil {
		// A read-only authority that cannot be enforced is refused, never
		// downgraded to an unguarded run.
		return agent.ErrorResult("command refused: %v", err)
	}
	sandbox.Configure(cmd)
	if len(t.Env) > 0 {
		cmd.Env = append(os.Environ(), t.Env...)
	}

	// Output flows through a sink that reports progress, so a long command
	// streams into a UI instead of showing nothing until it exits. A nil
	// reporter still collects, it just never forwards — which keeps the tool
	// usable by callers with no progress channel.
	sink := &outputSink{onUpdate: ctx.OnProgress, command: command}
	cmd.Stdout = &streamWriter{sink: sink}
	cmd.Stderr = &streamWriter{sink: sink, stderr: true}

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)
	// Read the buffers only after Run returns: Wait joins the copy
	// goroutines, so no further writes can land once we are here.
	sink.mu.Lock()
	stdout, stderr := sink.stdout.String(), sink.stderr.String()
	sink.mu.Unlock()

	combined := stdout
	if stderr != "" {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += stderr
	}

	truncated := false
	if len(combined) > BashMaxOutput {
		combined = combined[:BashMaxOutput]
		truncated = true
	}

	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		// Success.
	case runCtx.Err() != nil:
		// A killed process reports exit -1, so the context state is the only
		// way to tell a timeout from a genuine non-zero exit. A command that
		// was classified as mutating may have changed the workspace before it
		// was killed, so the evidence still requires verification.
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return agent.Result{Text: fmt.Sprintf("command timed out after %s", timeout), IsError: true, Mutates: mutates, Executes: true}
		}
		return agent.Result{Text: "command cancelled", IsError: true, Mutates: mutates, Executes: true}
	case errors.As(runErr, &exitErr):
		exitCode = exitErr.ExitCode()
	default:
		return agent.Result{Text: fmt.Sprintf("run command: %v", runErr), IsError: true, Mutates: mutates, Executes: true}
	}

	text := fmt.Sprintf("$ %s\n\n%s", command, strings.TrimRight(combined, "\n"))
	if truncated {
		text += fmt.Sprintf("\n\n[output truncated at %d bytes]", BashMaxOutput)
	}
	text += fmt.Sprintf("\n\n[exit code %d, %s]", exitCode, elapsed.Round(time.Millisecond))

	if exitCode != 0 {
		return agent.Result{
			Text:     text,
			IsError:  true,
			Mutates:  mutates,
			Executes: true,
			Details: map[string]any{
				"command":  command,
				"exitCode": exitCode,
				"duration": elapsed.String(),
			},
		}
	}
	verification := VerificationFor(command)
	return agent.Result{
		Text:         text,
		Mutates:      mutates,
		Executes:     true,
		Verifies:     verification != nil,
		Verification: verification,
		Details: map[string]any{
			"command":  command,
			"exitCode": 0,
			"duration": elapsed.String(),
		},
	}
}

// spawnBackground starts a command as a detached job and returns its id.
//
// The timeout bounds the job, not the call: a job still running when it
// elapses is killed, which is what keeps a forgotten background build from
// running until the session ends. The process is detached from the request
// context on purpose — binding it would kill the job when the turn ends,
// which is exactly when background work must survive — so jobs live until
// they exit, are killed by their timeout, or the ohgo process ends.
func (t *BashTool) spawnBackground(command string, timeout time.Duration, readOnly, mutates bool) agent.Result {
	jobs := t.Jobs()
	id, err := jobs.reserve(command)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}

	// Background work is detached from the request context on purpose, but it
	// must not escape the read-only boundary the turn was authorized under.
	cmd, err := sandbox.Command(context.Background(), sandbox.Spec{
		Argv:     []string{"sh", "-c", command},
		Dir:      t.Root,
		Root:     t.Root,
		ReadOnly: readOnly,
	})
	if err != nil {
		jobs.discard(id)
		return agent.ErrorResult("command refused: %v", err)
	}
	sandbox.Configure(cmd)
	if len(t.Env) > 0 {
		cmd.Env = append(os.Environ(), t.Env...)
	}
	cmd.Stdout = &jobWriter{jobs: jobs, id: id}
	cmd.Stderr = &jobWriter{jobs: jobs, id: id}
	jobs.attach(id, cmd)

	if err := cmd.Start(); err != nil {
		jobs.discard(id)
		return agent.ErrorResult("start background command: %v", err)
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	// The reaper records the exit; first finish wins, so a timeout kill and
	// a natural exit racing each other cannot record twice.
	go func() {
		runErr := cmd.Wait()
		exitCode := 0
		var exitErr *exec.ExitError
		if runErr != nil && errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		jobs.finish(id, exitCode, false)
	}()

	// The timeout is a kill-after, not a wait bound: the call already
	// returned, so there is nothing to stop waiting on.
	time.AfterFunc(timeout, func() {
		if jobs.kill(id) {
			jobs.finish(id, -1, true)
		}
	})

	return agent.Result{
		Text: fmt.Sprintf("$ %s\n\n[background job %s started, pid %d — killed after %s if still running; use wait to collect output]",
			command, id, pid, timeout),
		// A background command has not been observed to finish, so it cannot
		// verify anything. If it was classified as mutating, completion must
		// still require a later verification.
		Mutates:  mutates,
		Executes: true,
		Details: map[string]any{
			"job":     id,
			"command": command,
			"pid":     pid,
		},
	}
}

// jobWriter appends one stream of a background job to the shared table.
//
// The two streams share one writer type because background output is kept
// interleaved in arrival order: unlike the foreground path there is no final
// render to reconcile separate buffers against, so keeping one chronological
// buffer is both simpler and what incremental reads want.
type jobWriter struct {
	jobs *BackgroundJobs
	id   string
}

// Write implements io.Writer.
func (w *jobWriter) Write(p []byte) (int, error) {
	w.jobs.append(w.id, p)
	return len(p), nil
}

// It exists so a slow command is visible while it runs. Reports are throttled
// because a build can emit thousands of lines a second, and forwarding each
// one would spend more time in the UI than in the build.
type outputSink struct {
	// mu guards every field. The exec package copies each stream on its
	// own goroutine, so the two writers run concurrently with each other.
	mu     sync.Mutex
	stdout bytes.Buffer
	stderr bytes.Buffer
	// onUpdate receives each throttled report. It may be nil.
	onUpdate func(agent.Result)
	// command is the command being run, for the report prefix.
	command string
	// lastReport bounds how often a report is emitted.
	lastReport time.Time
	// total counts bytes written, for the report summary.
	total int
}

// progressInterval bounds how often output is reported.
const progressInterval = 150 * time.Millisecond

// progressPreviewLines bounds how many trailing lines a report carries.
const progressPreviewLines = 5

// report forwards a throttled preview of the output so far.
//
// Both streams are previewed together because that is how the final output is
// rendered, so a preview that showed only stdout would not match what arrives
// at the end.
func (s *outputSink) report() {
	s.mu.Lock()
	if s.onUpdate == nil {
		s.mu.Unlock()
		return
	}
	if !s.lastReport.IsZero() && time.Since(s.lastReport) < progressInterval {
		s.mu.Unlock()
		return
	}
	s.lastReport = time.Now()
	report := agent.Result{
		Text: tailLines(s.stdout.String()+s.stderr.String(), progressPreviewLines),
		Details: map[string]any{
			"command": s.command,
			"bytes":   s.total,
			"running": true,
		},
	}
	s.mu.Unlock()

	// The callback runs outside the lock: it may be slow, and holding the
	// mutex across it would serialise the two stream copiers for its duration.
	s.onUpdate(report)
}

// streamWriter adapts one of the command's streams to the shared sink.
//
// exec.Cmd assigns a writer per stream and cannot otherwise say which is which,
// so the direction is carried on the writer.
type streamWriter struct {
	sink   *outputSink
	stderr bool
}

// Write implements io.Writer.
func (w *streamWriter) Write(p []byte) (int, error) {
	w.sink.mu.Lock()
	if w.stderr {
		w.sink.stderr.Write(p)
	} else {
		w.sink.stdout.Write(p)
	}
	w.sink.total += len(p)
	w.sink.mu.Unlock()

	w.sink.report()
	return len(p), nil
}

// tailLines returns the last n lines of text.
func tailLines(text string, n int) string {
	if n <= 0 || text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
