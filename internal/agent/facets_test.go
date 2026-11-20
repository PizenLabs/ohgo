package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func facetIntent(facets ...contract.Facet) contract.IntentContract {
	intent := contract.IntentContract{Kind: contract.ObjectiveInformational}
	for _, facet := range facets {
		intent.RequiredFacets = append(intent.RequiredFacets, contract.RequiredFacet{Facet: facet})
	}
	return intent
}

func facetTurns(text string, results ...ToolOutcome) []Turn {
	return []Turn{{Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: text}}}, Results: results}}
}

// The real failure this contract exists for: the answer lists some findings
// and then says the caller count, test count, and call sites remain
// unverified. That is not a completed impact analysis.
func TestRequiredFacetRejectsExplicitlyUnverifiedDeliverable(t *testing.T) {
	intent := facetIntent(contract.FacetCallers, contract.FacetTests, contract.FacetCallSites)
	answer := "The order flow starts at NewOrder. The caller count, test count, and two call sites remain unverified."
	assessment := AssessRequiredFacets(intent, facetTurns(answer), answer)
	if assessment.Pass() {
		t.Fatal("an explicitly unverified deliverable passed completion")
	}
	if !strings.Contains(assessment.Reason(), "unverified") {
		t.Fatalf("reason = %q, want it to name the unverified facet", assessment.Reason())
	}
}

// A verified mention in the same answer satisfies the facet even when another
// sentence uses a qualified form.
func TestRequiredFacetAcceptsObservedDeliverable(t *testing.T) {
	intent := facetIntent(contract.FacetCallers, contract.FacetTests)
	answer := "Callers: 13 direct callers. Tests: 4 associated test functions."
	if assessment := AssessRequiredFacets(intent, facetTurns(answer), answer); !assessment.Pass() {
		t.Fatalf("observed deliverables were reviewed: %s", assessment.Reason())
	}
}

// An omitted deliverable is a different failure from an explicitly unverified
// one, and the reason must name it.
func TestRequiredFacetRejectsOmittedDeliverable(t *testing.T) {
	intent := facetIntent(contract.FacetTests)
	answer := "This is a Next.js blog with a Markdown content pipeline."
	assessment := AssessRequiredFacets(intent, facetTurns(answer), answer)
	if assessment.Pass() || !strings.Contains(assessment.Reason(), "does not report") {
		t.Fatalf("omitted facet = %+v, want a missing-deliverable review", assessment)
	}
}

// A complete absence result is not uncertainty. The prior policy already
// accepts a bounded observation; the facet contract must not turn it into a
// review merely because the expected count is zero.
func TestRequiredFacetAcceptsBoundedZeroObservation(t *testing.T) {
	intent := facetIntent(contract.FacetTests)
	answer := "No test files were observed in the available inventory; test tooling was not inspected."
	if assessment := AssessRequiredFacets(intent, facetTurns(answer), answer); !assessment.Pass() {
		t.Fatalf("a bounded zero observation was reviewed: %s", assessment.Reason())
	}
}

// A flat absence claim contradicted by the run's own structural count is a
// completion veto, not a stylistic preference.
func TestRequiredFacetRejectsContradictedAbsence(t *testing.T) {
	intent := facetIntent(contract.FacetCallers)
	outcome := ToolOutcome{
		ToolName: "refs",
		Result:   Result{Text: "callers: 13 (direct)"},
	}
	answer := "NewOrder has no callers."
	assessment := AssessRequiredFacets(intent, facetTurns(answer, outcome), answer)
	if assessment.Pass() || !strings.Contains(assessment.Reason(), "reports 13") {
		t.Fatalf("contradicted absence = %+v, want a count contradiction", assessment)
	}
}

// A partial caller list is not the requested caller analysis. The answer may
// be honest about the gap, but honest incompleteness is still NEEDS_REVIEW.
func TestRequiredFacetRejectsPartialCallerList(t *testing.T) {
	intent := facetIntent(contract.FacetCallers)
	answer := "The search surfaced 13 references; only 3 call sites were named. " +
		"Production callers may exist but were not individually identified."
	assessment := AssessRequiredFacets(intent, facetTurns(answer), answer)
	if assessment.Pass() || !strings.Contains(assessment.Reason(), "incomplete") {
		t.Fatalf("partial caller list = %+v, want an incomplete-deliverable review", assessment)
	}
}

// Naming three of thirteen observed call sites without stating the total is
// the ERNIE failure: the answer reads as the caller analysis while omitting ten
// sites. A summary that states the total is accepted.
func TestRequiredFacetRejectsPartialObservedList(t *testing.T) {
	intent := contract.IntentContract{Kind: contract.ObjectiveInformational}
	intent.RequiredFacets = []contract.RequiredFacet{{Facet: contract.FacetCallers, Subject: "NewOrder"}}
	outcome := ToolOutcome{
		ToolName: "refs",
		Args:     map[string]any{"symbol": "NewOrder"},
		Result: Result{
			Text: "13 references for NewOrder",
			Details: map[string]any{
				"relation":         "references",
				"symbol":           "NewOrder",
				"total_references": 13,
			},
		},
	}
	partial := "Callers of NewOrder: 3 reference sites: backend/a_test.go:1, backend/b_test.go:2, backend/c_test.go:3."
	assessment := AssessRequiredFacets(intent, facetTurns(partial, outcome), partial)
	if assessment.Pass() || !strings.Contains(assessment.Reason(), "observed 13") {
		t.Fatalf("partial list = %+v, want an incomplete-list review", assessment)
	}
	summary := "13 callers, including backend/a_test.go:1 and backend/b_test.go:2."
	if assessment := AssessRequiredFacets(intent, facetTurns(summary, outcome), summary); !assessment.Pass() {
		t.Fatalf("a reported total was reviewed: %s", assessment.Reason())
	}
}

// A static-analysis caveat about dynamic dispatch is a limitation, not a
// partial list. The answer lists every observed static caller and states the
// total; it must not be forced into review for naming what static analysis
// cannot see.
func TestRequiredFacetAcceptsStaticCaveat(t *testing.T) {
	intent := contract.IntentContract{Kind: contract.ObjectiveInformational}
	intent.RequiredFacets = []contract.RequiredFacet{{Facet: contract.FacetCallers, Subject: "NewOrder"}}
	outcome := ToolOutcome{
		ToolName: "refs",
		Args:     map[string]any{"symbol": "NewOrder"},
		Result: Result{
			Text: "13 references for NewOrder",
			Details: map[string]any{
				"relation":         "references",
				"symbol":           "NewOrder",
				"total_references": 13,
			},
		},
	}
	answer := "Callers of NewOrder (13 static direct call sites):\n" +
		"- backend/a_test.go:1\n- backend/b_test.go:2\n- backend/c_test.go:3\n" +
		"Caveat: dynamic and indirect calls may exist but are not captured by static analysis."
	assessment := AssessRequiredFacets(intent, facetTurns(answer, outcome), answer)
	if !assessment.Pass() {
		t.Fatalf("a complete static list with a dispatch caveat was reviewed: %s", assessment.Reason())
	}
}

// A request that names no facet must not acquire obligations. This is the
// guard against turning an ordinary project overview into a checklist.
func TestRequiredFacetIgnoresRequestsWithNoFacet(t *testing.T) {
	intent := contract.IntentContract{Kind: contract.ObjectiveInformational}
	answer := "This project is a blog."
	if assessment := AssessRequiredFacets(intent, facetTurns(answer), answer); !assessment.Pass() {
		t.Fatalf("facet-free answer was reviewed: %s", assessment.Reason())
	}
}
