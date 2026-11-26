package task

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// A run that cannot establish verification records both the lifecycle
// boundary and the semantic outcome. The task stays resumable, the conclusion
// survives a restart, and a later resume clears it because it is no longer the
// current state.
func TestReviewUnresolvedPersistsAndClearsOnResume(t *testing.T) {
	store := NewStore(t.TempDir())
	controller := NewController(store, nil)
	begun, err := controller.Begin(testIntent("fix the parser"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	conclusion := agent.RunConclusion{
		Status:  agent.ConclusionUnresolved,
		Reason:  agent.ConclusionVerificationNotEstablished,
		Summary: "the requested verification was not established in 8 verification turns",
	}
	review, err := controller.ReviewUnresolved("verification window closed", conclusion)
	if err != nil {
		t.Fatalf("ReviewUnresolved: %v", err)
	}
	if review.Status != StatusNeedsReview {
		t.Fatalf("status = %s, want NEEDS_REVIEW", review.Status)
	}
	if review.Status.IsTerminal() {
		t.Fatal("an unresolved review became terminal")
	}
	if review.Conclusion == nil {
		t.Fatal("the unresolved review recorded no conclusion")
	}
	if review.Conclusion.Status != agent.ConclusionUnresolved ||
		review.Conclusion.Reason != agent.ConclusionVerificationNotEstablished {
		t.Fatalf("conclusion = %+v, want UNRESOLVED / verification_not_established", review.Conclusion)
	}
	if review.Conclusion.At.IsZero() {
		t.Fatal("the durable conclusion has no timestamp")
	}
	if review.ReviewReason != "verification window closed" {
		t.Fatalf("review reason = %q", review.ReviewReason)
	}

	restored, found, err := NewController(store, nil).Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.Conclusion == nil ||
		restored.Conclusion.Reason != agent.ConclusionVerificationNotEstablished {
		t.Fatalf("conclusion did not survive the checkpoint: %+v", restored.Conclusion)
	}
	if summary := strings.TrimSpace(restored.Conclusion.Summary); summary == "" {
		t.Fatal("the restored conclusion lost its explanation")
	}

	// Resuming resolves the review boundary: the terminal outcome describes a
	// run that is over, not the task's current state.
	resumed, err := NewController(store, nil).Begin(testIntent("fix the parser"), nil)
	if err != nil {
		t.Fatalf("Begin resume: %v", err)
	}
	if resumed.ID != begun.ID {
		t.Fatalf("resume created a new task: %q -> %q", begun.ID, resumed.ID)
	}
	if resumed.Status != StatusRunning {
		t.Fatalf("resumed status = %s, want RUNNING", resumed.Status)
	}
	if resumed.Conclusion != nil {
		t.Fatalf("resume kept a stale conclusion: %+v", resumed.Conclusion)
	}
}

// A plain review must not inherit a previous terminal conclusion: the two
// outcomes are different, and a resume that still needs evidence is not an
// UNRESOLVED verdict.
func TestNeedsReviewClearsAStaleConclusion(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.ReviewUnresolved("verification window closed", agent.RunConclusion{
		Status: agent.ConclusionUnresolved,
		Reason: agent.ConclusionVerificationNotEstablished,
	}); err != nil {
		t.Fatalf("ReviewUnresolved: %v", err)
	}
	review, err := controller.NeedsReview("answer needs a narrower claim")
	if err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}
	if review.Conclusion != nil {
		t.Fatalf("plain review inherited a conclusion: %+v", review.Conclusion)
	}
}

// A conclusion without the durable vocabulary is refused before the lifecycle
// moves, so a failed write cannot leave an unvalidatable checkpoint behind.
func TestReviewUnresolvedRejectsAnInvalidConclusion(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("fix the parser"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.ReviewUnresolved("verification window closed", agent.RunConclusion{
		Status: agent.ConclusionUnresolved,
	}); err == nil {
		t.Fatal("a conclusion without a reason was accepted")
	}
	current, ok := controller.Current()
	if !ok || current.Status != StatusRunning || current.Conclusion != nil {
		t.Fatalf("a refused conclusion changed the task: %+v", current)
	}
}
