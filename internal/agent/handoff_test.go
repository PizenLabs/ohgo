package agent

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// authorityTool is a write tool: it declares the mutation action class so the
// grounding boundary treats it as a repository change.
type authorityTool struct {
	*scriptedTool
}

func newAuthorityTool() *authorityTool {
	return &authorityTool{scriptedTool: newScriptedTool("write", "wrote")}
}

func (t *authorityTool) Approval() Approval { return Approval{Tier: TierWrite} }
func (t *authorityTool) ActionClasses() []ActionClass {
	return []ActionClass{ActionMutate}
}

// The tool-boundary handoff runs before admission and the loop re-evaluates the
// same call under the authority the host moved it to. Without it, a staged task
// that reaches a mutation while FUZZ is still active is refused even though the
// control plane can move to BUILD automatically.
func TestWorkspaceHandoffRunsBeforeAdmission(t *testing.T) {
	tool := newAuthorityTool()
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "write", Arguments: map[string]any{"path": "a.go"}}},
		StopReason: ai.StopToolUse,
	}
	second := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
		StopReason: ai.StopEndTurn,
	}
	streamFn, _ := scriptedStream(first, second)

	allowed := false
	handoffCalls := 0
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		Admit: func(ai.ToolCall, Tool) (bool, string) {
			if !allowed {
				return false, "mutation_not_permitted"
			}
			return true, ""
		},
		WorkspaceHandoff: func(call ai.ToolCall, got Tool, args map[string]any) bool {
			handoffCalls++
			if call.Name != "write" || got.Name() != "write" || args["path"] != "a.go" {
				t.Fatalf("handoff saw call=%s tool=%s args=%v", call.Name, got.Name(), args)
			}
			allowed = true
			return true
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "fix it"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if handoffCalls != 1 {
		t.Fatalf("handoff calls = %d, want 1", handoffCalls)
	}
	if got := len(tool.invoked()); got != 1 {
		t.Fatalf("tool invocations = %d, want the call admitted after the handoff", got)
	}
	if len(turns) == 0 || len(turns[0].Results) == 0 || turns[0].Results[0].Result.IsError {
		t.Fatalf("the admitted call was recorded as a failure: %+v", turns)
	}
}

// A handoff must not rescue a call the request never grounded. Grounding runs
// first, so a write under an informational objective is refused even when a
// host is willing to move authority.
func TestWorkspaceHandoffCannotGroundAnUngroundedMutation(t *testing.T) {
	tool := newAuthorityTool()
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "write", Arguments: map[string]any{"path": "a.go"}}},
		StopReason: ai.StopToolUse,
	}
	second := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
		StopReason: ai.StopEndTurn,
	}
	streamFn, _ := scriptedStream(first, second)

	handoffCalls := 0
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		Grounding: &contract.IntentGrounding{
			Action: contract.GroundingRead,
			// MutationIntent is false: the request never asked for a change.
		},
		Admit: func(ai.ToolCall, Tool) (bool, string) { return true, "" },
		WorkspaceHandoff: func(ai.ToolCall, Tool, map[string]any) bool {
			handoffCalls++
			return true
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "explain the code"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if handoffCalls != 0 {
		t.Fatalf("handoff ran for an ungrounded mutation: %d calls", handoffCalls)
	}
	if got := len(tool.invoked()); got != 0 {
		t.Fatalf("an ungrounded mutation executed %d times", got)
	}
	if len(turns) == 0 || len(turns[0].Results) == 0 || !turns[0].Results[0].Result.CandidateRejected {
		t.Fatalf("the ungrounded call was not refused: %+v", turns)
	}
}
