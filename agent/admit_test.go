package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
)

func TestLoopAdmitBlocksTheCallBeforeApproval(t *testing.T) {
	// The authority gate runs before approval: a call the workspace forbids is
	// refused outright and never offered to the user as something they could
	// approve (I5, §27).
	tool := newScriptedTool("write", "wrote")
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "write", Arguments: map[string]any{"value": "x"}}},
		StopReason: ai.StopToolUse,
	}
	second := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}, StopReason: ai.StopEndTurn}
	streamFn, _ := scriptedStream(first, second)

	approveCalled := false
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		Admit: func(call ai.ToolCall, tool Tool) (bool, string) {
			return false, "capability denied workspace=fuzz operation=write reason=mutation_not_permitted"
		},
		Approve: func(ai.ToolCall, Approval) (bool, string) {
			approveCalled = true
			return true, ""
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if approveCalled {
		t.Error("approval ran for a call the authority refused")
	}
	if got := len(tool.invoked()); got != 0 {
		t.Errorf("the refused tool ran %d times", got)
	}
	if len(turns) == 0 || len(turns[0].Results) == 0 {
		t.Fatal("no tool outcome was recorded")
	}
	result := turns[0].Results[0].Result
	if !result.IsError {
		t.Error("a refused call must be an error result")
	}
	if !strings.Contains(result.Text, "mutation_not_permitted") {
		t.Errorf("refusal text = %q, want the authority reason", result.Text)
	}
}

func TestLoopWithoutAdmitAdmitsEveryCall(t *testing.T) {
	// A nil Admit preserves the pre-authority behavior: every call proceeds.
	tool := newScriptedTool("read", "contents")
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": "x"}}},
		StopReason: ai.StopToolUse,
	}
	second := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}, StopReason: ai.StopEndTurn}
	streamFn, _ := scriptedStream(first, second)

	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(tool.invoked()); got != 1 {
		t.Errorf("tool invocations = %d, want 1", got)
	}
}
