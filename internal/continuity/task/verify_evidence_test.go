package task

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// A structured verification result is execution and check evidence. The durable
// task boundary must accept it without inventing a second observation or
// requiring the model to narrate what it saw.
func TestVerificationEvidenceSupportsExecutionCompletion(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	intent := workspace.BuildIntentContract("use npx live-server test this project")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{
		Results: []agent.ToolOutcome{{
			CallID:   "verify-1",
			ToolName: "verify",
			Result: agent.Result{
				Text:     "verification: PASS\nservice: npx live-server\n- http: PASS status 200",
				Executes: true,
				Finished: true,
				Verifies: true,
				Verification: &agent.VerificationEvidence{
					Command: "npx live-server",
				},
				Details: map[string]any{
					"verification": map[string]any{
						"status": "PASS",
						"service": map[string]any{
							"command": "npx live-server",
						},
					},
				},
			},
		}},
	}})
	completed, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if completed.Status != StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", completed.Status)
	}
	if completed.ObjectiveState() != ObjectiveProven {
		t.Fatalf("objective state = %s, want PROVEN", completed.ObjectiveState())
	}
}
