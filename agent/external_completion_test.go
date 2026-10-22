package agent

import (
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/security"
)

// An external mutation decision is an approval and authority fact, not
// repository evidence. It must not make an informational run look mutated, and
// it must not satisfy a repository mutation objective.
func TestExternalMutationIsNotWorkspaceMutationEvidence(t *testing.T) {
	decision := security.Decision{
		Capability:       security.CapabilityExternalMCPMutate,
		Outcome:          security.OutcomeAuto,
		Mutates:          false,
		ExternalMutation: true,
	}
	turns := []Turn{
		{
			Assistant: ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{ID: "m1", Name: "mcp_call"}},
			},
			Results: []ToolOutcome{{
				CallID:        "m1",
				ToolName:      "mcp_call",
				Result:        Result{Text: "remote call succeeded"},
				Authorization: &decision,
			}},
		},
		{
			Assistant: ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			},
		},
	}

	informational := EvaluateTaskCompletion(CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Objective: "send a message",
			Kind:      contract.ObjectiveInformational,
		},
	})
	if !informational.Complete() {
		t.Fatalf("an external mutation failed an informational run: %s", informational.Reason)
	}

	mutation := EvaluateTaskCompletion(CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Objective: "update the README",
			Kind:      contract.ObjectiveMutation,
			Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "README.md"}},
		},
	})
	if mutation.Complete() {
		t.Fatal("an external mutation completed a repository mutation objective")
	}
}
