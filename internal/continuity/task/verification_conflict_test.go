package task

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

func conflictController(t *testing.T) (*Controller, string) {
	t.Helper()
	root := t.TempDir()
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	controller.now = func() time.Time { return time.Unix(17200, 0).UTC() }
	intent := contract.IntentContract{
		Kind:                contract.ObjectiveMutation,
		Objective:           "Make the project section scroll in one row without lag",
		BehavioralObjective: true,
		Verification:        []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceArtifact, Role: workspace.Build, Source: "write:index.html",
		Summary: "changed index.html", Outcome: EvidenceObserved, At: controller.now(),
	}); err != nil {
		t.Fatalf("RecordEvidence(change): %v", err)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Role: workspace.Build, Source: "verify:check:any",
		Scope: "*", Summary: "static marquee assertions passed",
		Outcome: EvidenceObserved, At: controller.now().Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordEvidence(verification): %v", err)
	}
	return controller, root
}

// A user report against a recorded check becomes an explicit, durable
// conflict. The original verification is preserved, and the task no longer
// claims the report is ordinary new work.
func TestUserContradictionCreatesDurableVerificationConflict(t *testing.T) {
	controller, root := conflictController(t)
	recorded, err := controller.RecordVerificationConflict("the section does not move")
	if err != nil {
		t.Fatalf("RecordVerificationConflict: %v", err)
	}
	if recorded.Status != StatusNeedsReview {
		t.Fatalf("status = %s, want NEEDS_REVIEW", recorded.Status)
	}
	conflict, ok := recorded.VerificationConflict()
	if !ok {
		t.Fatal("no durable verification conflict was recorded")
	}
	if conflict.Claim != "the section does not move" {
		t.Fatalf("claim = %q", conflict.Claim)
	}
	if !strings.Contains(conflict.Evidence, "static marquee") {
		t.Fatalf("disputed evidence = %q, want the recorded check", conflict.Evidence)
	}
	if reason := strings.ToLower(recorded.ReviewReason); !strings.Contains(reason, "verification conflict") {
		t.Fatalf("review reason = %q, want the explicit conflict", recorded.ReviewReason)
	}
	if open, ok := controller.VerificationConflict(); !ok || open.Claim != conflict.Claim {
		t.Fatalf("runtime conflict = %+v/%t, want the open conflict", open, ok)
	}

	// The contradiction must survive a checkpoint; a restart that forgets it
	// would silently resume a mutation against a disputed verification.
	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, _, err := reopened.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	current, ok := reopened.Current()
	if !ok {
		t.Fatal("the task did not restore")
	}
	restored, ok := current.VerificationConflict()
	if !ok || restored.Claim != conflict.Claim {
		t.Fatalf("restored conflict = %+v/%t, want %q", restored, ok, conflict.Claim)
	}
	if open, ok := reopened.VerificationConflict(); !ok || open.Claim != conflict.Claim {
		t.Fatalf("restored runtime conflict = %+v/%t, want %q", open, ok, conflict.Claim)
	}
}

// A second report while the conflict is open is the same conflict, not a new
// contradictory record. The retry must not accumulate evidence or reset the
// investigation window.
func TestVerificationConflictIsIdempotentWhileOpen(t *testing.T) {
	controller, _ := conflictController(t)
	first, err := controller.RecordVerificationConflict("the section does not move")
	if err != nil {
		t.Fatalf("RecordVerificationConflict: %v", err)
	}
	before := len(first.Context.Evidence)
	second, err := controller.RecordVerificationConflict("still does not move")
	if err != nil {
		t.Fatalf("second RecordVerificationConflict: %v", err)
	}
	if len(second.Context.Evidence) != before {
		t.Fatalf("evidence grew from %d to %d records for one open conflict", before, len(second.Context.Evidence))
	}
}

// A behavioral check answers an open conflict, including when it fails. The
// contradiction becomes history, the original verification remains recorded,
// and the ordinary repair/verification lifecycle can continue.
func TestBehavioralObservationResolvesVerificationConflict(t *testing.T) {
	controller, _ := conflictController(t)
	if _, err := controller.RecordVerificationConflict("the section does not move"); err != nil {
		t.Fatalf("RecordVerificationConflict: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{{
		ToolName: "verify",
		Result: agent.Result{
			Text:         "verification: FAIL",
			IsError:      true,
			FailureClass: agent.ToolFailureVerification,
			Verification: &agent.VerificationEvidence{
				Command: "verify", Check: contract.CheckAny, Class: agent.VerificationBehavioral,
			},
		},
	}}}})
	current, ok := controller.Current()
	if !ok {
		t.Fatal("no task after observing the behavioral check")
	}
	if conflict, ok := current.VerificationConflict(); ok {
		t.Fatalf("the resolved conflict is still current: %+v", conflict)
	}
	if conflict, ok := controller.VerificationConflict(); ok {
		t.Fatalf("the runtime still reports an open conflict: %+v", conflict)
	}
	// The history is retained, not deleted: one invalidated user report and
	// one still-current verification.
	var reports, verifications int
	for _, record := range current.Context.Evidence {
		if record.Outcome == EvidenceContradicting && record.Validity == EvidenceInvalid {
			reports++
		}
		if isVerificationEvidence(record) && record.Validity == EvidenceCurrent {
			verifications++
		}
	}
	if reports != 1 || verifications != 1 {
		t.Fatalf("history = %d invalidated reports, %d current verifications; want 1 and 1", reports, verifications)
	}
}
