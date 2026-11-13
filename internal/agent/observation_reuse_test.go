package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// callMessage builds one tool-calling assistant turn.
func reuseCallMessage(calls ...ai.ToolCall) ai.AssistantMessage {
	content := make([]ai.ContentBlock, 0, len(calls))
	for _, call := range calls {
		content = append(content, call)
	}
	return ai.AssistantMessage{Content: content, StopReason: ai.StopToolUse}
}

// answerMessage builds a final answer.
func reuseAnswerMessage(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}
}

// An identical read in a later turn is answered from the current observation
// instead of spending a second call and context window.
func TestRepeatedObservationIsReusedAcrossTurns(t *testing.T) {
	tool := newScriptedTool("read", "file contents")
	streamFn, _ := scriptedStream(
		reuseCallMessage(ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"value": "a.go"}}),
		reuseCallMessage(ai.ToolCall{ID: "read-2", Name: "read", Arguments: map[string]any{"value": "a.go"}}),
		reuseAnswerMessage("done"),
	)
	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "read"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(tool.invoked()); got != 1 {
		t.Fatalf("tool invocations = %d, want the duplicate observation reused", got)
	}
	if len(turns) < 3 || len(turns[1].Results) != 1 {
		t.Fatalf("turns = %+v, want a second-turn reuse result", turns)
	}
	reused := turns[1].Results[0]
	if reused.Result.IsError {
		t.Fatalf("reuse was reported as an error: %s", reused.Result.Text)
	}
	// The reuse must replay the payload the model needs. A notice without the
	// observation starves the model of the evidence it already paid for, and
	// the next turn repeats the call to recover it.
	if !strings.Contains(reused.Result.Text, "file contents") {
		t.Fatalf("reuse text = %q, want the recorded payload replayed", reused.Result.Text)
	}
	if !strings.Contains(reused.Result.Text, "unchanged since your earlier call") {
		t.Fatalf("reuse text = %q, want an explicit reuse notice", reused.Result.Text)
	}
	if strings.Contains(reused.Result.Text, "replayed below; do not call the tool again for it.\n\nThis observation") {
		t.Fatalf("reuse notice recorded itself recursively: %q", reused.Result.Text)
	}
}

// A reuse replays the whole recorded observation, not its first line: an
// observation is the evidence, and a truncated replay forces the model to
// re-run the same call to see the part it was already given.
func TestReuseReplaysTheWholePayload(t *testing.T) {
	payload := "path: components\n" +
		"button.tsx\ncard.tsx\ndialog.tsx\ninput.tsx\nlabel.tsx"
	tool := newScriptedTool("ls", payload)
	streamFn, _ := scriptedStream(
		reuseCallMessage(ai.ToolCall{ID: "ls-1", Name: "ls", Arguments: map[string]any{"value": "components"}}),
		reuseCallMessage(ai.ToolCall{ID: "ls-2", Name: "ls", Arguments: map[string]any{"value": "components"}}),
		reuseAnswerMessage("done"),
	)
	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "list"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(tool.invoked()); got != 1 {
		t.Fatalf("tool invocations = %d, want the duplicate observation reused", got)
	}
	if len(turns) < 3 || len(turns[1].Results) != 1 {
		t.Fatalf("turns = %+v, want a second-turn reuse result", turns)
	}
	if got := turns[1].Results[0].Result.Text; !strings.Contains(got, payload) {
		t.Fatalf("replay = %q, want the full payload", got)
	}
}

// A change invalidates the observation index: after a write, re-reading the
// file is a new observation and must execute.
func TestObservationReuseIsInvalidatedByAChange(t *testing.T) {
	read := newScriptedTool("read", "v1")
	write := newScriptedTool("write", "changed")
	write.result = Result{Text: "changed", Mutates: true}
	streamFn, _ := scriptedStream(
		reuseCallMessage(ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"value": "a.go"}}),
		reuseCallMessage(ai.ToolCall{ID: "write-1", Name: "write", Arguments: map[string]any{"value": "a.go"}}),
		reuseCallMessage(ai.ToolCall{ID: "read-2", Name: "read", Arguments: map[string]any{"value": "a.go"}}),
		reuseAnswerMessage("done"),
	)
	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{read, write}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "read and change"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(read.invoked()); got != 2 {
		t.Fatalf("read invocations = %d, want the post-change read to execute", got)
	}
}

// A duplicate inside one batch is not answered from a sibling's result: each
// call keeps its own outcome and the concurrency contract is unchanged.
func TestSameBatchObservationIsNotReused(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	streamFn, _ := scriptedStream(
		reuseCallMessage(
			ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"value": "a.go"}},
			ai.ToolCall{ID: "read-2", Name: "read", Arguments: map[string]any{"value": "a.go"}},
		),
		reuseAnswerMessage("done"),
	)
	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "read"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(tool.invoked()); got != 2 {
		t.Fatalf("tool invocations = %d, want both same-batch calls to run", got)
	}
}
