package tui

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

func conflictTaskResume() TaskResume {
	return TaskResume{
		ID:          "task-1",
		Status:      "NEEDS_REVIEW",
		Objective:   "Make the project section scroll in one row without lag",
		HasContract: true,
		Contract: contract.IntentContract{
			Kind:                contract.ObjectiveMutation,
			Objective:           "Make the project section scroll in one row without lag",
			BehavioralObjective: true,
			Verification:        []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
		},
	}
}

// A user correction after a recorded verification is a conflict, not a new
// objective. It must keep the task and route to behavioral investigation.
func TestVerificationConflictOwnsTheCorrectionTurn(t *testing.T) {
	text := "fix it help me and test it"
	current := conflictTaskResume()
	current.VerificationConflict = "the section does not move"
	decision, owned := taskTurnDecision(current, text, "build", workspace.BuildIntentContract(text))
	if !owned || decision != "VERIFICATION_CONFLICT" {
		t.Fatalf("decision = %q owned=%t, want VERIFICATION_CONFLICT", decision, owned)
	}
}

// The first correction itself is also owned by the task: its wording reports
// behavior, but the runtime must record the conflict before deciding the next
// action. A new named objective remains a new task.
func TestBehavioralCorrectionAttachesToOpenVerification(t *testing.T) {
	current := conflictTaskResume()
	correction := "but right now it doesn't move in a circle from left to right with"
	decision, owned := taskTurnDecision(current, correction, "build", workspace.BuildIntentContract(correction))
	if !owned || decision != "VERIFICATION_CONFLICT" {
		t.Fatalf("decision = %q owned=%t, want VERIFICATION_CONFLICT", decision, owned)
	}

	newObjective := "build a login page with email and password"
	decision, owned = taskTurnDecision(current, newObjective, "build", workspace.BuildIntentContract(newObjective))
	if owned {
		t.Fatalf("a new objective was attached as a verification conflict: %q", decision)
	}
}
