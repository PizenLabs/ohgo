// Package exec runs the user's "!<command>" shell escape.
//
// It is deliberately small and terminal-free: parsing, authority gating, and
// process execution are pure enough to unit-test without a TUI. The interactive
// layer owns presentation; this package owns the contract of what an escape is
// and how it runs. It is the process-execution seam the TUI is wired to, so a
// test can substitute a stub for the shell.
package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// EscapePrefix is the character that opens a shell escape.
const EscapePrefix = '!'

// MaxOutput is the default byte cap on captured output. A command that emits
// more is truncated at a rune boundary and flagged, so a runaway stream can
// never grow the transcript without bound.
const MaxOutput = 256 * 1024

// ParseEscape reports whether line is a shell escape and returns the command
// that follows the leading "!".
//
// Leading spaces and tabs before the "!" are tolerated so a stray indent does
// not silently turn an escape back into a model prompt; a line whose first
// non-space rune is anything else is not an escape. A line that is exactly "!"
// parses as an escape with an empty command, which the caller reports as a
// syntax hint rather than feeding an empty string to the shell.
func ParseEscape(line string) (command string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" || trimmed[0] != EscapePrefix {
		return "", false
	}
	return strings.TrimSpace(trimmed[1:]), true
}

// Action is how an escape command may proceed under the active authority.
type Action int

const (
	// Run executes the command immediately.
	Run Action = iota
	// Confirm asks the user before executing.
	Confirm
)

// Decide classifies an escape command under an authority mode.
//
// BUILD and FUZZ are execution authorities, so the command runs directly. ASK
// is read-only, so a command that provably mutates the workspace must be
// confirmed by the human who typed it, while a read-only command runs
// directly. An unset or unknown authority follows ASK: mutation needs a human.
func Decide(mode string, mutating bool) Action {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "BUILD", "FUZZ":
		return Run
	}
	if mutating {
		return Confirm
	}
	return Run
}

// EscapeResult is the outcome of one executed escape command.
type EscapeResult struct {
	// Command is the raw command as typed, without the leading "!".
	Command string
	// Output is the combined stdout and stderr, in arrival order.
	Output string
	// ExitCode is the process exit status, or -1 when it is unknown.
	ExitCode int
	// Duration is the measured wall-clock execution time.
	Duration time.Duration
	// Truncated reports that Output was capped at the runner's limit.
	Truncated bool
}

// ShellRunner executes escape commands in a working directory.
//
// The zero value is usable: it runs in the process's own directory through
// the platform's default shell with the default output cap.
type ShellRunner struct {
	// Dir is the working directory for every command. Empty uses the
	// process's own working directory.
	Dir string
	// Shell overrides the shell program. Empty auto-detects: "/bin/sh" when
	// present, else "sh" (cmd on Windows).
	Shell string
	// MaxOutput caps captured bytes. Zero means MaxOutput.
	MaxOutput int
}

// Run executes command, invoking onChunk for each chunk of output as it
// arrives; onChunk may be nil.
//
// A non-zero exit is reported in the result, not as an error: a command that
// fails is a result to render, not a failure to report. An error is returned
// only when the process could not start or was killed by cancellation or
// timeout, in which case the caller should treat the attempt as aborted.
//
// onChunk is called synchronously from the writer goroutines, so it must be
// quick and must not call back into the runner. Chunks arrive in the same
// order they are appended to Output.
func (r ShellRunner) Run(ctx context.Context, command string, onChunk func(string)) (EscapeResult, error) {
	result := EscapeResult{Command: command, ExitCode: -1}
	if strings.TrimSpace(command) == "" {
		return result, errors.New("empty command")
	}

	shell, flag := r.shell()
	proc := exec.CommandContext(ctx, shell, flag, command)
	if r.Dir != "" {
		proc.Dir = r.Dir
	}

	max := r.MaxOutput
	if max <= 0 {
		max = MaxOutput
	}
	sink := &sink{onChunk: onChunk, max: max}
	// One sink backs both streams: stdout and stderr are interleaved in
	// arrival order, which is how a terminal would have shown them.
	proc.Stdout = sink
	proc.Stderr = sink

	start := time.Now()
	runErr := proc.Run()
	result.Duration = time.Since(start)

	sink.mu.Lock()
	result.Output = sink.buf.String()
	result.Truncated = sink.truncated
	sink.mu.Unlock()

	// Cancellation wins over the exit status: a killed process reports a
	// signal, and the caller needs to know the run was abandoned rather
	// than that the command genuinely failed.
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("run %q: %w", command, runErr)
	}
	result.ExitCode = 0
	return result, nil
}

// shell resolves the shell program and its command flag.
func (r ShellRunner) shell() (string, string) {
	if r.Shell != "" {
		return r.Shell, "-c"
	}
	if runtime.GOOS == "windows" {
		return "cmd", "/C"
	}
	if _, err := os.Stat("/bin/sh"); err == nil {
		return "/bin/sh", "-c"
	}
	return "sh", "-c"
}

// sink is the combined stdout/stderr writer.
//
// exec copies the two streams on separate goroutines, so the mutex is what
// keeps the accumulated Output and the onChunk order consistent. onChunk runs
// under the lock on purpose: publishing an out-of-order chunk would interleave
// the transcript differently from the result it eventually reports.
type sink struct {
	mu        sync.Mutex
	buf       strings.Builder
	onChunk   func(string)
	max       int
	truncated bool
}

// Write implements io.Writer.
func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.max > 0 && s.buf.Len() >= s.max {
		s.truncated = true
		return len(p), nil
	}
	chunk := string(p)
	if s.max > 0 && s.buf.Len()+len(chunk) > s.max {
		chunk = truncateRune(chunk, s.max-s.buf.Len())
		s.truncated = true
	}
	s.buf.WriteString(chunk)
	if s.onChunk != nil && chunk != "" {
		s.onChunk(chunk)
	}
	return len(p), nil
}

// truncateRune cuts text to the largest prefix of at most n bytes that does
// not split a rune, so capped output is always valid UTF-8.
func truncateRune(text string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(text) <= n {
		return text
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
