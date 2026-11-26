package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// regression1180Request is the recorded task that ran for ~44 minutes after the
// requested change had already been made.
const regression1180Request = "Add a few projects in a separate stack with the projects: OpenCode, Claude Code, Duckduck Go"

// countingStream serves the scripted turns and counts requests. Once the script
// is exhausted it keeps proposing the same outside-workspace search, so a run
// that has not closed its action surface fails by exceeding the turn bound
// rather than by quietly stopping.
func countingStream(messages []ai.AssistantMessage, runaway ai.AssistantMessage) (ai.StreamFn, func() int) {
	var (
		mu    sync.Mutex
		index int
	)
	return func(context.Context, ai.Request) (*ai.Stream, error) {
			mu.Lock()
			defer mu.Unlock()
			position := index
			index++
			if position < len(messages) {
				return ai.NewCompletedStream(messages[position]), nil
			}
			return ai.NewCompletedStream(runaway), nil
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return index
		}
}

func callTurn(id, name string, args map[string]any) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: args}},
		StopReason: ai.StopToolUse,
	}
}

// countingTool records every invocation of an inner capability so the test can
// prove a post-completion call was never executed.
type countingTool struct {
	inner agent.Tool
	mu    sync.Mutex
	calls []map[string]any
}

func (t *countingTool) Name() string                   { return t.inner.Name() }
func (t *countingTool) Description() string            { return t.inner.Description() }
func (t *countingTool) Parameters() *jsonschema.Schema { return t.inner.Parameters() }
func (t *countingTool) Approval() agent.Approval       { return t.inner.Approval() }
func (t *countingTool) Exclusive(args map[string]any) bool {
	return t.inner.Exclusive(args)
}
func (t *countingTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	t.mu.Lock()
	t.calls = append(t.calls, args)
	t.mu.Unlock()
	return t.inner.Execute(ctx, args)
}
func (t *countingTool) invoked() []map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]map[string]any(nil), t.calls...)
}

func siteFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"index.html": "<!doctype html><html><body><nav></nav></body></html>",
		"styles.css": "body{margin:0}",
		"script.js":  "document.body.dataset.ready='1'",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestRegression1180TerminatesAtTheCompletionBarrier is the recorded failure:
// the change is made, the symbol the extractor inferred does not exist, and the
// workspace has no check command. The run must reach a truthful terminal state
// in a bounded number of turns, and it must not search a sibling or parent path.
func TestRegression1180TerminatesAtTheCompletionBarrier(t *testing.T) {
	root := siteFixture(t)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("sibling project"), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := workspace.BuildIntentContract(regression1180Request)
	if intent.Kind != workspace.ObjectiveMutation {
		t.Fatalf("fixture intent = %s, want mutation", intent.Kind)
	}
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(intent, cognition.ShapeParams{Authority: "build", Facts: facts})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)

	grep := &countingTool{inner: tools.NewGrepTool(root)}
	bash := &countingTool{inner: tools.NewBashTool(root)}
	source := tools.NewStructuralSource(root)
	script := []ai.AssistantMessage{
		callTurn("write-1", "write", map[string]any{
			"path": "index.html",
			"content": "<!doctype html><html><body><nav></nav>" +
				"<section id=\"projects\"><h3>OpenCode</h3><h3>Claude Code</h3><h3>Duckduck Go</h3></section>" +
				"</body></html>",
		}),
		callTurn("symbols-1", "symbols", map[string]any{"symbol": "OpenCode"}),
		callTurn("symbols-2", "symbols", map[string]any{"symbol": "OpenCode"}),
		callTurn("check-1", "check", map[string]any{"mode": "run"}),
	}
	// Everything after the completion barrier is the observed escape shape: a
	// read of a sibling project through the shell. It must never execute.
	runaway := callTurn("bash-runaway", "bash", map[string]any{"command": "cat " + secret})
	stream, requests := countingStream(script, runaway)

	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Build)
	loop, err := agent.New(agent.Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools: []agent.Tool{
			tools.NewReadTool(root), tools.NewWriteTool(root),
			tools.NewSymbolsToolWithSource(source), tools.NewCheckTool(source), grep, bash,
		},
		Authorize: opts.newSecurityGate(nil),
		MaxTurns:  60,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var (
		lastReport agent.ProjectionReport
		haveReport bool
	)
	loop.SetGrounding(intent.Grounding)
	loop.SetProjectionHook(projector.Project)
	loop.SetProjectionReportHook(func(report agent.ProjectionReport) {
		lastReport = report
		haveReport = true
	})

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: regression1180Request}})
	if err != nil && !errors.Is(err, agent.ErrTaskBlocked) {
		t.Fatalf("Run: %v", err)
	}
	// The barrier is semantic, but the run must also be cheap: a handful of
	// setup turns plus at most one bounded answer retry.
	if got := requests(); got > 8 {
		t.Fatalf("requests = %d, want <= 8; the completion barrier did not stop exploration", got)
	}
	if got := len(grep.invoked()); got != 0 {
		t.Fatalf("grep ran %d time(s) after the objective was complete: %v", got, grep.invoked())
	}
	if got := len(bash.invoked()); got != 0 {
		t.Fatalf("a shell command ran after the objective was complete: %v", bash.invoked())
	}

	// Implementation: the requested cards are in the file.
	body, readErr := os.ReadFile(filepath.Join(root, "index.html"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{"OpenCode", "Claude Code", "Duckduck Go", `id="projects"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("implemented page is missing %q:\n%s", want, body)
		}
	}

	if !haveReport {
		t.Fatal("the projector never ran")
	}
	if lastReport.VerificationLimited == "" {
		t.Fatalf("no verification limitation was recorded: %+v", lastReport)
	}
	if lastReport.Stage != string(cognition.StageComplete) {
		t.Fatalf("final stage = %q, want complete (missing=%v unresolved=%v)", lastReport.Stage, lastReport.Missing, lastReport.Unresolved)
	}

	outcome := projectRun(nil, turns, intent, nil, nil, false, lastReport.EvidenceReview(),
		lastReport.Provenance, loop.LastMutation(), lastReport.Unresolved, lastReport.VerificationLimited)
	if outcome.State != runCompleted {
		t.Fatalf("outcome = %s (%s), want completed", outcome.State, outcome.Reason)
	}
	if !strings.Contains(strings.ToLower(outcome.Reason), "verification") {
		t.Fatalf("completed outcome did not state the limitation: %q", outcome.Reason)
	}
	if !strings.Contains(outcome.line(), "COMPLETED") {
		t.Fatalf("terminal line = %q", outcome.line())
	}
	// The sibling read was never dispatched, so the file was never observed.
	if strings.Contains(string(body), "sibling project") {
		t.Fatal("the workspace file contains sibling content")
	}
}
