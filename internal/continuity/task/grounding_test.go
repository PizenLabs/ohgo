package task

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// The grounding contract is durable task state: a resumed task must remember
// the entities and ambiguities the user stated, or a later turn can silently
// replace them.
func TestControllerPersistsIntentGrounding(t *testing.T) {
	root := t.TempDir()
	controller := NewController(NewStore(root), nil)
	intent := contract.IntentContract{
		Objective: "Additional projects are k8s and opencode.",
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{
			Objective: "Additional projects are k8s and opencode.",
			Action:    contract.GroundingUnknown,
			Entities: []contract.Entity{
				{Value: "k8s", Kind: contract.EntityProject},
				{Value: "opencode", Kind: contract.EntityProject},
			},
			Scope: contract.GroundingScope{
				Kind:   contract.ScopeNamed,
				Values: []string{"k8s", "opencode"},
			},
			Ambiguities: []contract.Ambiguity{{
				Kind: "unknown_action", Material: true,
			}},
			Confidence: contract.GroundingConfidenceLow,
		},
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatal(err)
	}

	// A write to the caller's slice must not reach the persisted contract.
	intent.Grounding.Entities[0].Value = "mutated"
	current, ok := controller.Current()
	if !ok || current.Contract == nil || current.Contract.Grounding == nil {
		t.Fatal("grounding was not persisted")
	}
	if got := current.Contract.Grounding.EntityValues(); len(got) != 2 || got[0] != "k8s" {
		t.Fatalf("entities = %v, want the caller's later mutation isolated", got)
	}

	reopened := NewController(NewStore(root), nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("restore: found=%v err=%v", found, err)
	}
	if restored.Contract == nil || restored.Contract.Grounding == nil {
		t.Fatal("restored task lost its grounding")
	}
	if got := restored.Contract.Grounding.EntityValues(); len(got) != 2 ||
		got[0] != "k8s" || got[1] != "opencode" {
		t.Fatalf("restored entities = %v", got)
	}
	if !restored.Contract.Grounding.HasMaterialAmbiguity() {
		t.Fatal("restored task lost the material ambiguity")
	}
}

// The review's machine-readable claims are durable state: a resumed task
// repairs the named claims instead of re-auditing the answer from scratch,
// and a completion clears the resolved list. General answer-level issues have
// no category and stay in the flat reason.
func TestControllerPersistsReviewRepairList(t *testing.T) {
	root := t.TempDir()
	controller := NewController(NewStore(root), nil)
	intent := contract.IntentContract{
		Objective: "explain the parser",
		Kind:      contract.ObjectiveInformational,
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatal(err)
	}
	issues := []contract.GroundingIssue{
		{
			Code:        "unsupported_code_entity",
			Category:    "entity",
			Claim:       "`PaymentReconciler` harmonizes ledgers",
			Requirement: "an observed code entity",
			Action:      "inspect the claim's coordinate or remove the identifier",
		},
		{
			Code:   "answer_level",
			Detail: "a general reason with no claim class",
		},
	}
	if _, err := controller.NeedsReview("intent grounding: unsupported claim", issues...); err != nil {
		t.Fatal(err)
	}

	reopened := NewController(NewStore(root), nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("restore: found=%v err=%v", found, err)
	}
	if len(restored.ReviewIssues) != 1 || restored.ReviewIssues[0].Code != "unsupported_code_entity" {
		t.Fatalf("restored review issues = %+v, want the claim-level entry only", restored.ReviewIssues)
	}
	orientation := strings.Join(reopened.Context(), "\n")
	if !strings.Contains(orientation, "PaymentReconciler") {
		t.Fatalf("orientation = %q, want the named claim", orientation)
	}
	if !strings.Contains(orientation, "required: an observed code entity") {
		t.Fatalf("orientation = %q, want the shared repair line", orientation)
	}
	if !strings.Contains(orientation, agent.RepairInstructions(restored.ReviewIssues)) {
		t.Fatalf("orientation = %q, want the one repair formatter's output", orientation)
	}

	// Completion resolves the review boundary, so the repair list must not
	// survive it as current state. A review is completed through the running
	// lifecycle, never by a direct transition.
	running := restored.Clone()
	running.Status = StatusRunning
	if err := running.Complete(time.Now()); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(running.ReviewIssues) != 0 || running.ReviewReason != "" {
		t.Fatalf("completed task kept review state: reason=%q issues=%+v",
			running.ReviewReason, running.ReviewIssues)
	}
}
