package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// The vocabulary checks know a finite set of predicates. This answer uses a
// verb they do not, and names a code entity no observation contains. The
// general association check must still catch it.
func TestEntityClaimRejectsInventedIdentifierWithNovelPredicate(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\nfunc NewOrder() {}\n"),
	}}}
	answer := "The `PaymentReconciler` harmonizes ledgers before settlement."
	issues := claimScopeIssues(answer, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_entity" {
		t.Fatalf("issues = %+v, want an unsupported_code_entity", issues)
	}
	if !strings.Contains(issues[0].Detail, "PaymentReconciler") {
		t.Fatalf("detail = %q, want the invented identifier", issues[0].Detail)
	}
}

// A name that the run actually observed is supported even when the sentence
// uses the same novel predicate.
func TestEntityClaimAcceptsObservedIdentifier(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go",
			"package order\n\ntype PaymentReconciler struct{}\n\nfunc (PaymentReconciler) Harmonize() {}\n"),
	}}}
	answer := "The `PaymentReconciler` harmonizes ledgers before settlement."
	if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
		t.Fatalf("an observed identifier was reviewed: %+v", issues)
	}
}

// Boundary matching prevents a short name from being supported by a longer
// observed identifier, and vice versa.
func TestEntityClaimDoesNotMatchInsideAnotherIdentifier(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\nfunc NewOrderView() {}\n"),
	}}}
	answer := "The `Order` type controls checkout."
	issues := claimScopeIssues(answer, turns)
	if len(issues) == 0 || issues[0].Code != "unsupported_code_entity" {
		t.Fatalf("issues = %+v, want a boundary miss", issues)
	}
}

// Without an observation there is no baseline, so a resumed answer is not
// refuted by this run.
func TestEntityClaimSkipsWithoutObservations(t *testing.T) {
	answer := "The `PaymentReconciler` harmonizes ledgers before settlement."
	if issues := claimScopeIssues(answer, nil); len(issues) != 0 {
		t.Fatalf("a claim was reviewed without a baseline: %+v", issues)
	}
}

// Advice about an external API is not a project fact. The general check must
// not turn a recommendation into a completion review.
func TestEntityClaimSkipsImperativeAdvice(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\nfunc NewOrder() {}\n"),
	}}}
	for _, answer := range []string{
		"Use `fmt.Errorf` with `%w` so cancellation survives wrapping.",
		"Prefer `errors.Is` over string matching.",
		"The `PaymentReconciler` was not found in the inspected source.",
	} {
		if issues := claimScopeIssues(answer, turns); len(issues) != 0 {
			t.Fatalf("non-factual answer was reviewed: %q -> %+v", answer, issues)
		}
	}
}

// The general association check reaches the completion boundary through the
// same grounding assessment as the vocabulary checks.
func TestEntityClaimReachesGroundingAssessment(t *testing.T) {
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{},
	}
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\nfunc NewOrder() {}\n"),
	}}}
	answer := "The `PaymentReconciler` harmonizes ledgers before settlement."
	assessment := AssessGrounding(intent, turns, answer)
	if assessment.Pass() || !strings.Contains(assessment.Reason(), "PaymentReconciler") {
		t.Fatalf("grounding = %+v, want an entity review", assessment)
	}
}

// A negative existential claim about a code entity is contradicted by a
// positive observation of that entity, regardless of the wording used.
func TestEntityClaimRejectsAbsenceContradictedByObservation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		readOutcome("internal/order/service.go", "package order\n\ntype PaymentReconciler struct{}\n"),
	}}}
	for _, answer := range []string{
		"The `PaymentReconciler` does not exist in this codebase.",
		"There is no `PaymentReconciler` in the project.",
	} {
		issues := claimScopeIssues(answer, turns)
		if len(issues) == 0 || issues[0].Code != "unsupported_entity_absence" {
			t.Fatalf("answer %q -> issues %+v, want an entity-absence contradiction", answer, issues)
		}
	}
}
