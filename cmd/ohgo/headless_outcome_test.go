package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/workspace"
)

// headlessLoop builds a one-shot loop with a scripted provider and the
// production authority gate. It is the same construction path `ohgo run` uses.
func headlessLoop(t *testing.T, root string, stream ai.StreamFn) *agent.Loop {
	t.Helper()
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Build)
	loop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    stream,
		Tools:     []agent.Tool{tools.NewWriteTool(root), tools.NewBashTool(root)},
		Authorize: opts.newSecurityGate(nil),
		MaxTurns:  10,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return loop
}

func runOnceHeadless(t *testing.T, root string, stream ai.StreamFn, request string) (runOutcome, string) {
	t.Helper()
	var out bytes.Buffer
	outcome, _ := runHeadlessTurn(context.Background(), headlessLoop(t, root, stream), root, &out, request, nil)
	return outcome, out.String()
}

func TestHeadlessExitCodes(t *testing.T) {
	for _, tc := range []struct {
		state runState
		code  int
	}{
		{runCompleted, 0},
		{runNeedsReview, 2},
		{runPaused, 3},
		{runFailed, 1},
		{runCancelled, 130},
	} {
		if got := newOutcome(tc.state, "reason").exitCode(); got != tc.code {
			t.Errorf("%s exit code = %d, want %d", tc.state, got, tc.code)
		}
	}
}

func TestHeadlessShapeUsesRoutedAuthority(t *testing.T) {
	// The router may delegate an informational or investigative turn to a
	// narrower handler while the active workspace stays BUILD. The shape the
	// stateless path sends must name the handler it is actually running under,
	// not the completion kind's default.
	root := t.TempDir()
	var offered [][]string
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		names := make([]string, 0, len(req.Tools))
		for _, spec := range req.Tools {
			names = append(names, spec.Name)
		}
		offered = append(offered, names)
		return ai.NewCompletedStream(stopMessage()), nil
	}
	var out bytes.Buffer
	_, _ = runHeadlessTurnAs(context.Background(), headlessLoop(t, root, stream), root, &out, "Write README.md.", nil, "ask")
	if len(offered) == 0 {
		t.Fatal("the provider was not called")
	}
	for _, name := range offered[0] {
		if name == "write" || name == "bash" {
			t.Fatalf("ASK-shaped turn offered %q: %#v", name, offered[0])
		}
	}
}

func TestHeadlessEvidenceReviewPreventsGuessedAnswer(t *testing.T) {
	// A repository-scoped question answered without a single read is a guess.
	// The projection says INSUFFICIENT at the request that produced the
	// answer; completion must report review, not COMPLETED.
	outcome, text := runOnceHeadless(t, t.TempDir(), scriptedCompletionStream(stopMessage()), "Explain this project.")
	if outcome.State == runCompleted {
		t.Fatalf("an evidence-free answer was reported complete: %s", text)
	}
	if !strings.Contains(text, "NEEDS_REVIEW") {
		t.Fatalf("output = %q, want NEEDS_REVIEW", text)
	}
}

func TestOneShotReturnsZeroOnCompleted(t *testing.T) {
	outcome, text := runOnceHeadless(t, t.TempDir(), scriptedCompletionStream(stopMessage()), "explain this")
	if outcome.State != runCompleted || outcome.exitCode() != 0 {
		t.Fatalf("outcome = %s/%d, want COMPLETED/0: %s", outcome.State, outcome.exitCode(), text)
	}
	if !strings.Contains(text, "COMPLETED") {
		t.Fatalf("output = %q, want the verdict line", text)
	}
}

func TestOneShotReturnsNonZeroOnNeedsReview(t *testing.T) {
	outcome, text := runOnceHeadless(t, t.TempDir(), scriptedCompletionStream(stopMessage()), "change note.txt")
	if outcome.State != runNeedsReview || outcome.exitCode() == 0 {
		t.Fatalf("outcome = %s/%d, want NEEDS_REVIEW/non-zero: %s", outcome.State, outcome.exitCode(), text)
	}
	if !strings.Contains(text, "NEEDS_REVIEW") {
		t.Fatalf("output = %q, want the review verdict", text)
	}
}

func TestHeadlessRejectsEmptyInformationalAnswer(t *testing.T) {
	for name, message := range map[string]ai.AssistantMessage{
		"empty":      {},
		"whitespace": {Content: []ai.ContentBlock{ai.TextContent{Text: " \n\t "}}},
	} {
		outcome, text := runOnceHeadless(t, t.TempDir(), scriptedCompletionStream(message), "explain this")
		if outcome.State != runNeedsReview {
			t.Fatalf("%s outcome = %s, want NEEDS_REVIEW: %s", name, outcome.State, text)
		}
	}
}

func TestHeadlessExecutionNeedsExecutionEvidence(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		outcome, _ := runOnceHeadless(t, t.TempDir(), scriptedCompletionStream(stopMessage()), "run true")
		if outcome.State != runNeedsReview {
			t.Fatalf("outcome = %s, want NEEDS_REVIEW", outcome.State)
		}
	})
	t.Run("successful", func(t *testing.T) {
		outcome, text := runOnceHeadless(t, t.TempDir(), scriptedCompletionStream(
			completionBashCall("true"), stopMessage(),
		), "run true")
		if outcome.State != runCompleted {
			t.Fatalf("outcome = %s, want COMPLETED: %s", outcome.State, text)
		}
	})
}

func mutationFixture(t *testing.T, failing bool) string {
	t.Helper()
	root := goModuleFixture(t, failing)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestHeadlessMutationRequiresVerification(t *testing.T) {
	t.Run("verified", func(t *testing.T) {
		root := mutationFixture(t, false)
		outcome, text := runOnceHeadless(t, root, scriptedCompletionStream(
			completionWriteCall("note.txt", "changed\n"),
			completionBashCall("go test ./..."),
			stopMessage(),
		), "change note.txt and verify")
		if outcome.State != runCompleted {
			t.Fatalf("outcome = %s, want COMPLETED: %s", outcome.State, text)
		}
	})
	t.Run("missing", func(t *testing.T) {
		root := mutationFixture(t, false)
		outcome, _ := runOnceHeadless(t, root, scriptedCompletionStream(
			completionWriteCall("note.txt", "changed\n"),
			stopMessage(),
		), "change note.txt and verify")
		if outcome.State != runNeedsReview {
			t.Fatalf("outcome = %s, want NEEDS_REVIEW", outcome.State)
		}
	})
	t.Run("failed", func(t *testing.T) {
		root := mutationFixture(t, true)
		outcome, _ := runOnceHeadless(t, root, scriptedCompletionStream(
			completionWriteCall("note.txt", "changed\n"),
			completionBashCall("go test ./..."),
			stopMessage(),
		), "change note.txt and verify")
		if outcome.State != runNeedsReview {
			t.Fatalf("outcome = %s, want NEEDS_REVIEW", outcome.State)
		}
	})
}

func TestHeadlessBudgetExhaustionIsPaused(t *testing.T) {
	root := t.TempDir()
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Build)
	stream := scriptedCompletionStream(
		completionBashCall("true"),
		completionBashCall("true"),
		stopMessage(),
	)
	loop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    stream,
		Tools:     []agent.Tool{tools.NewBashTool(root)},
		Authorize: opts.newSecurityGate(nil),
		MaxTurns:  10,
		Budget:    budget.New(budget.Limits{Turns: 1, ModelCalls: 1, ToolCalls: 10}),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	var out bytes.Buffer
	outcome, _ := runHeadlessTurn(context.Background(), loop, root, &out, "run true", nil)
	if outcome.State != runPaused || outcome.exitCode() == 0 {
		t.Fatalf("outcome = %s/%d, want PAUSED/non-zero: %s", outcome.State, outcome.exitCode(), out.String())
	}
}

func TestHeadlessCancellationIsNotSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	outcome, _ := runHeadlessTurn(ctx, headlessLoop(t, t.TempDir(), scriptedCompletionStream(stopMessage())), t.TempDir(), &out, "explain this", nil)
	if outcome.State != runCancelled || outcome.exitCode() != exitCancelled {
		t.Fatalf("outcome = %s/%d, want CANCELLED/130", outcome.State, outcome.exitCode())
	}
}

func TestOneShotReturnsNonZeroOnRefusedRoute(t *testing.T) {
	route := workspace.RouteTurn(workspace.Ask, "run ls")
	if route.Allowed() {
		t.Fatal("ASK unexpectedly allowed execution")
	}
	var out bytes.Buffer
	outcome := refuseHeadless(&out, route)
	if outcome.State != runFailed || outcome.exitCode() == 0 {
		t.Fatalf("outcome = %s/%d, want FAILED/non-zero", outcome.State, outcome.exitCode())
	}
	if !strings.Contains(out.String(), "FAILED") {
		t.Fatalf("output = %q, want the failure verdict", out.String())
	}
}

func TestHeadlessModelFailureIsFailed(t *testing.T) {
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		return nil, errors.New("provider unavailable")
	}
	outcome, text := runOnceHeadless(t, t.TempDir(), stream, "explain this")
	if outcome.State != runFailed || outcome.exitCode() == 0 {
		t.Fatalf("outcome = %s/%d, want FAILED/non-zero: %s", outcome.State, outcome.exitCode(), text)
	}
}

func TestHeadlessDeadlineIsNotSuccess(t *testing.T) {
	root := t.TempDir()
	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Build)
	loop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    scriptedCompletionStream(stopMessage()),
		Tools:     []agent.Tool{tools.NewBashTool(root)},
		Authorize: opts.newSecurityGate(nil),
		MaxTurns:  10,
		Deadline:  time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	var out bytes.Buffer
	outcome, _ := runHeadlessTurn(context.Background(), loop, root, &out, "explain this", nil)
	if outcome.State != runPaused || outcome.exitCode() == 0 {
		t.Fatalf("outcome = %s/%d, want PAUSED/non-zero: %s", outcome.State, outcome.exitCode(), out.String())
	}
}

// The plain REPL reports the same verdict as `ohgo run` and keeps reading after
// a reviewable turn. It must not present an evidence gap as success.
func TestReplReportsNeedsReviewAndContinues(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	root := t.TempDir()
	loop := headlessLoop(t, root, scriptedCompletionStream(
		ai.AssistantMessage{},
		stopMessage(),
	))
	settings := config.Default()
	h := newHarness(nil, loop, nil, nil, nil, &settings, root, catalog.MustResolve("anthropic/claude-sonnet-4-5"))
	h.workspace = workspace.Build.Label()

	var out bytes.Buffer
	err := repl(h, &out, strings.NewReader("change note.txt\n/ask\nexplain this\n/quit\n"), nil)
	if err != nil {
		t.Fatalf("repl: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "NEEDS_REVIEW") {
		t.Fatalf("REPL output lacks a review verdict:\n%s", text)
	}
	if !strings.Contains(text, "COMPLETED") {
		t.Fatalf("REPL did not continue to a completed turn:\n%s", text)
	}
}

// A headless resumed session still evaluates only the current turn's evidence.
// History is context, not completion evidence.
func TestHeadlessHistoryIsNotCompletionEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	history := []agent.Message{agent.UserMessage{Text: "change note.txt and verify"}}
	var out bytes.Buffer
	outcome, _ := runHeadlessTurn(context.Background(),
		headlessLoop(t, root, scriptedCompletionStream(stopMessage())),
		root, &out, "change note.txt and verify", history)
	if outcome.State != runNeedsReview {
		t.Fatalf("outcome = %s, want NEEDS_REVIEW: %s", outcome.State, out.String())
	}
}
