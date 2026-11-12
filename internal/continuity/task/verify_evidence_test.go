package task

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
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
		Results: []agent.ToolOutcome{runtimeVerifyOutcome()},
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

// A runtime objective is not proven by a command that merely finished. The
// environment probe ("node -v && npm -v") that runs while the model inspects a
// project is real execution evidence, but it is not the requested execution;
// accepting it reports "run this project" as proven when nothing ever ran.
func TestRuntimeObjectiveRejectsCommandOnlyEvidence(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	intent := workspace.BuildIntentContract("run this project")
	if intent.Kind != contract.ObjectiveExecution || intent.ExecutionMode != contract.ExecutionRuntime {
		t.Fatalf("contract = %+v, want a runtime execution objective", intent)
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{
		Results: []agent.ToolOutcome{{
			CallID:   "probe-1",
			ToolName: "bash",
			Result: agent.Result{
				Text:     "v24.11.0\n11.6.1\nnode_modules",
				Executes: true,
				Finished: true,
				Details:  map[string]any{"command": "node -v && npm -v", "exitCode": 0},
			},
		}},
	}})
	refused, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err == nil {
		t.Fatalf("command-only evidence completed a runtime objective: %s", refused.ObjectiveState())
	}
	if refused.Status == StatusCompleted {
		t.Fatalf("status = %s, want the task to stay open", refused.Status)
	}
	if refused.ObjectiveState() != ObjectiveActive {
		t.Fatalf("objective state = %s, want ACTIVE", refused.ObjectiveState())
	}
}

// runtimeVerifyOutcome is the result the verify capability returns for a plan
// that started a service, observed readiness, and passed an HTTP check.
func runtimeVerifyOutcome() agent.ToolOutcome {
	return agent.ToolOutcome{
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
			Runtime: &agent.RuntimeEvidence{
				Command: "npx live-server",
				URL:     "http://127.0.0.1:8080",
				PID:     4242,
				Checks:  1,
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
	}
}
