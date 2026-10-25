package workspace_test

import (
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/workspace"
)

// The observed failure, end to end: a context statement becomes an invented
// repository conclusion. The contract built from the user's text must reach
// the completion evaluator, which refuses the negative conclusion.
func TestActionlessRequestCannotCompleteAnInventedConclusion(t *testing.T) {
	intent := workspace.BuildIntentContract("Additional projects are k8s and opencode.")
	if intent.Grounding == nil {
		t.Fatal("BuildIntentContract did not attach grounding")
	}
	if intent.Grounding.Action != contract.GroundingUnknown {
		t.Fatalf("action = %q, want unknown", intent.Grounding.Action)
	}
	verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Contract: intent,
		Turns: []agent.Turn{
			{Results: []agent.ToolOutcome{{
				ToolName: "ls",
				Args:     map[string]any{"path": "."},
				Result:   agent.Result{Text: "cmd\ninternal\nmain.go\n"},
			}}},
			{Assistant: ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "The repository contains no README documentation, so I cannot describe the test-web2 project."}},
			}},
		},
	})
	if verdict.Complete() {
		t.Fatal("the invented negative conclusion completed")
	}
}

// The same request, answered faithfully, completes. Grounding must not turn
// every ambiguous statement into a permanent review.
func TestActionlessRequestCompletesAGroundedAcknowledgement(t *testing.T) {
	intent := workspace.BuildIntentContract("Additional projects are k8s and opencode.")
	verdict := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Contract: intent,
		Turns: []agent.Turn{{Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "Noted: k8s and opencode are additional projects."}},
		}}},
	})
	if !verdict.Complete() {
		t.Fatalf("grounded acknowledgement did not complete: %s", verdict.Reason)
	}
}
