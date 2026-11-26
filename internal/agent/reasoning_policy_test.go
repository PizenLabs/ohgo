package agent

import (
	"context"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The policy is consulted before every provider request, not once per run.
// The second request sees the budget after the first was charged and can lower
// effort for the last allowed call.
func TestReasoningPolicyReevaluatesEachRequest(t *testing.T) {
	model := ai.Model{
		Provider: "test", ID: "policy", Reasoning: true,
		ReasoningEfforts: []string{"low", "high"}, ContextWindow: 10000,
	}
	stream, requests := scriptedStream(
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model: model, Stream: stream,
		Tools:  []Tool{newScriptedTool("read", "ok")},
		Budget: budget.New(budget.Limits{Turns: 2, ModelCalls: 2}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var seen []ReasoningFacts
	loop.SetReasoningPolicy(func(facts ReasoningFacts) (bool, string) {
		seen = append(seen, facts)
		if facts.RemainingModelCalls == 1 {
			return true, "low"
		}
		return true, "high"
	})

	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("policy calls = %d, want one per provider request", len(seen))
	}
	if seen[0].RemainingModelCalls != 2 || seen[1].RemainingModelCalls != 1 {
		t.Fatalf("remaining calls = %d then %d, want 2 then 1", seen[0].RemainingModelCalls, seen[1].RemainingModelCalls)
	}
	if seen[0].ContextWindow != model.ContextWindow || seen[0].ContextUsed <= 0 {
		t.Errorf("context facts = %d/%d, want the measured request context", seen[0].ContextUsed, seen[0].ContextWindow)
	}
	sent := requests()
	if len(sent) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(sent))
	}
	if sent[0].ReasoningEffort != "high" || sent[1].ReasoningEffort != "low" {
		t.Errorf("request efforts = %q then %q, want high then low", sent[0].ReasoningEffort, sent[1].ReasoningEffort)
	}
}

// Clearing the policy restores the loop's configured setting. The policy is a
// per-run seam, not a permanent rewrite of configuration.
func TestReasoningPolicyClearRestoresConfiguredSetting(t *testing.T) {
	model := ai.Model{Provider: "test", ID: "configured", Reasoning: true, ReasoningEfforts: []string{"medium"}}
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "one"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{Model: model, Stream: stream, Reasoning: true, ReasoningEffort: "medium",
		Tools: []Tool{newScriptedTool("read", "ok")}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loop.SetReasoningPolicy(func(ReasoningFacts) (bool, string) { return false, "" })
	loop.SetReasoningPolicy(nil)
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	sent := requests()
	if len(sent) != 1 || !sent[0].Reasoning || sent[0].ReasoningEffort != "medium" {
		t.Fatalf("request reasoning = %+v, want the configured medium setting", sent)
	}
}
