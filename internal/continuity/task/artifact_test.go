package task

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// A FUZZ finding is a proposal. Projecting it into the task must make it
// visible to BUILD as an active hypothesis, while keeping facts empty until a
// runtime observation supports the claim.
func TestFindingArtifactBecomesBuildVisibleHypothesis(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(contract.IntentContract{
		Objective: "investigate the checkout flow",
		Kind:      contract.ObjectiveInformational,
	}, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	finding := workspace.Artifact{
		ID:           "finding-1",
		Kind:         workspace.KindFinding,
		Origin:       workspace.Fuzz,
		Target:       "order/checkout",
		Status:       "reproduced",
		Expected:     "an order request is sent",
		Observed:     "no order request is sent",
		Reproduction: "go test ./order/...",
		Evidence:     "FAIL TestCheckout",
	}
	if err := controller.RecordArtifact(finding); err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	current, ok := controller.Current()
	if !ok {
		t.Fatal("the task disappeared after the artifact projection")
	}
	if len(current.Context.Facts) != 0 {
		t.Fatalf("a finding became a fact: %+v", current.Context.Facts)
	}
	if len(current.Context.Hypotheses) != 1 {
		t.Fatalf("hypotheses = %d, want 1", len(current.Context.Hypotheses))
	}
	hypothesis := current.Context.Hypotheses[0]
	if hypothesis.Status != HypothesisActive {
		t.Fatalf("hypothesis status = %s, want ACTIVE", hypothesis.Status)
	}
	for _, want := range []string{"order/checkout", "reproduced", "go test ./order/...", "FAIL TestCheckout"} {
		if !strings.Contains(hypothesis.Text, want) {
			t.Fatalf("hypothesis lost %q: %s", want, hypothesis.Text)
		}
	}

	// BUILD is a different role; the shared hypothesis must survive the role
	// change and appear in its briefing.
	role = workspace.Build
	briefing := strings.Join(controller.Context(), "\n")
	for _, want := range []string{"Hypotheses", "order/checkout", "go test ./order/..."} {
		if !strings.Contains(briefing, want) {
			t.Fatalf("BUILD briefing missing %q:\n%s", want, briefing)
		}
	}

	// A replay of the same finding must not grow the shared context.
	replay := finding
	replay.ID = "finding-2"
	if err := controller.RecordArtifact(replay); err != nil {
		t.Fatalf("RecordArtifact replay: %v", err)
	}
	current, _ = controller.Current()
	if len(current.Context.Hypotheses) != 1 {
		t.Fatalf("a replayed finding created %d hypotheses", len(current.Context.Hypotheses))
	}
}

// Completion resolves the review boundary. The durable task must not keep
// presenting a reason for a review that no longer describes its state.
func TestCompletionClearsResolvedReviewReason(t *testing.T) {
	store := NewStore(t.TempDir())
	controller := NewController(store, nil)
	intent := contract.IntentContract{
		Objective: "summarize the checkout flow",
		Kind:      contract.ObjectiveInformational,
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.NeedsReview("the repository was not observed"); err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if strings.Contains(strings.Join(controller.Context(), "\n"), "Review reason") {
		t.Fatal("a resumed task still presented its previous review as current")
	}
	completed, err := controller.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if completed.ReviewReason != "" {
		t.Fatalf("completed task retained review reason %q", completed.ReviewReason)
	}
	if briefing := strings.Join(controller.Context(), "\n"); strings.Contains(briefing, "Review reason") {
		t.Fatalf("completed task briefing retained a review warning:\n%s", briefing)
	}

	// The durable checkpoint must not restore the resolved warning either.
	restored, found, err := NewController(store, nil).Restore()
	if err != nil || !found {
		t.Fatalf("Restore = %v, found=%v", err, found)
	}
	if restored.ReviewReason != "" {
		t.Fatalf("restored completed task review reason = %q", restored.ReviewReason)
	}
}

// Older checkpoints may carry a review reason after completion. Load must
// normalize it rather than resurrect resolved uncertainty.
func TestCompletedCheckpointDropsLegacyReviewReason(t *testing.T) {
	store := NewStore(t.TempDir())
	stale := Task{
		ID:           "legacy-completed",
		Status:       StatusCompleted,
		Objective:    "summarize the checkout flow",
		Contract:     &contract.IntentContract{Objective: "summarize the checkout flow", Kind: contract.ObjectiveInformational},
		ReviewReason: "the repository was not observed",
		UpdatedAt:    time.Now().UTC(),
	}
	if err := store.Save(stale); err != nil {
		t.Fatalf("Save: %v", err)
	}
	restored, found, err := NewController(store, nil).Restore()
	if err != nil || !found {
		t.Fatalf("Restore = %v, found=%v", err, found)
	}
	if restored.ReviewReason != "" {
		t.Fatalf("legacy completed task review reason = %q, want empty", restored.ReviewReason)
	}
}
