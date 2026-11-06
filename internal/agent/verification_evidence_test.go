package agent

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// sequencedTool returns its configured results in call order. The test drives
// one call per turn so the evidence order is deterministic even though a real
// batch dispatches concurrently.
type sequencedTool struct {
	*scriptedTool
	results []Result
	next    int
}

func newSequencedTool(name string, results ...Result) *sequencedTool {
	return &sequencedTool{scriptedTool: newScriptedTool(name, ""), results: results}
}

func (t *sequencedTool) Execute(ctx Context, args map[string]any) Result {
	result := Result{Text: "ran"}
	if t.next < len(t.results) {
		result = t.results[t.next]
	}
	t.next++
	t.scriptedTool.Execute(ctx, args)
	return result
}

func oneCallTurn(id, name string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: map[string]any{"value": id}}},
		StopReason: ai.StopToolUse,
	}
}

func stoppedTurn(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}
}

// A verification artifact is a reference to a check the runtime observed, so
// the loop must expose that evidence after the check and withdraw it when a
// later change reopens the obligation.
func TestLoopTracksVerificationAfterTheLastDefiniteChange(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")

	t.Run("change then check", func(t *testing.T) {
		tool := newSequencedTool("work",
			Result{Text: "wrote", Mutates: true},
			Result{Text: "ok", Verifies: true},
		)
		stream, _ := scriptedStream(
			oneCallTurn("c1", "work"),
			oneCallTurn("c2", "work"),
			stoppedTurn("done"),
		)
		loop, err := New(Config{Model: model, Stream: stream, Tools: []Tool{tool}})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "fix it"}}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !loop.VerificationObserved() {
			t.Fatal("a change followed by a successful check did not establish verification evidence")
		}
	})

	t.Run("check then later change", func(t *testing.T) {
		tool := newSequencedTool("work",
			Result{Text: "wrote", Mutates: true},
			Result{Text: "ok", Verifies: true},
			Result{Text: "wrote", Mutates: true},
		)
		stream, _ := scriptedStream(
			oneCallTurn("c1", "work"),
			oneCallTurn("c2", "work"),
			oneCallTurn("c3", "work"),
			stoppedTurn("done"),
		)
		loop, err := New(Config{Model: model, Stream: stream, Tools: []Tool{tool}})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "fix it again"}}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if loop.VerificationObserved() {
			t.Fatal("a change after the last check still reported verification evidence")
		}
	})
}
