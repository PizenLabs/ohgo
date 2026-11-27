package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func planIssueCodes(issues []contract.GroundingIssue) map[string]bool {
	codes := map[string]bool{}
	for _, issue := range issues {
		codes[issue.Code] = true
	}
	return codes
}

// A pre-prose plan is audited by the same claim rules as the final answer. A
// whole-service implementation claim backed only by the entry point is refused
// before any answer prose exists.
func TestReviewAnswerPlanRefusesUnsupportedImplementationClaim(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("backend/cmd/product-service/main.go", "package main", 20),
	}}}
	plan := AnswerPlan{Claims: []string{"Product-service is fully implemented."}}
	issues := ReviewAnswerPlan(claimContract(), turns, plan, EvidenceProvenance{})
	codes := planIssueCodes(issues)
	if !codes["unsupported_implementation_claim"] && !codes["unsupported_completeness_claim"] {
		t.Fatalf("plan issues = %+v, want an implementation refusal", issues)
	}

	supportedTurns := []Turn{{Results: []ToolOutcome{
		claimStructureFiles("backend/internal/product", []string{
			"backend/internal/product/service.go",
		}),
		claimRead("backend/internal/product/service.go", "package product", 40),
	}}}
	issues = ReviewAnswerPlan(claimContract(), supportedTurns, plan, EvidenceProvenance{})
	codes = planIssueCodes(issues)
	if codes["unsupported_implementation_claim"] || codes["unsupported_completeness_claim"] {
		t.Fatalf("scoped source still refused the plan: %+v", issues)
	}
}

// A manifest declaration is enough to name the stack. A behavioral claim about
// the same dependency still requires a source operation.
func TestReviewAnswerPlanDistinguishesStackFromBehavior(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("backend/go.mod", "module ecommerce\nrequire (\n"+
			"  gorm.io/gorm v1.25.5\n"+
			"  gorm.io/driver/postgres v1.5.4\n"+
			"  github.com/redis/go-redis/v9 v9.3.1\n"+
			")", 52),
	}}}
	stack := AnswerPlan{Claims: []string{"The backend uses GORM, PostgreSQL, and Redis."}}
	issues := ReviewAnswerPlan(claimContract(), turns, stack, EvidenceProvenance{})
	if planIssueCodes(issues)["unsupported_dependency_claim"] {
		t.Fatalf("a manifest-backed stack was refused: %+v", issues)
	}

	behavior := AnswerPlan{Claims: []string{"The backend uses Redis for caching."}}
	issues = ReviewAnswerPlan(claimContract(), turns, behavior, EvidenceProvenance{})
	if !planIssueCodes(issues)["unsupported_dependency_claim"] {
		t.Fatalf("a behavioral dependency claim was accepted: %+v", issues)
	}
}

// Limitations are honest scope statements, not assertions. A plan that leaves
// an inspected-window detail open is not refused for it.
func TestReviewAnswerPlanDoesNotAuditLimitationsAsClaims(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# Project\nA Go service.", 20),
	}}}
	plan := AnswerPlan{
		Claims:      []string{"The repository contains a Go service."},
		Limitations: []string{"The internal package source was not read in this pass."},
	}
	issues := ReviewAnswerPlan(claimContract(), turns, plan, EvidenceProvenance{})
	for _, issue := range issues {
		if strings.Contains(strings.ToLower(issue.Detail), "internal package") {
			t.Fatalf("a limitation was audited as a claim: %+v", issues)
		}
	}
}

// A requested deliverable the plan honestly leaves unverified is a completion
// gap, not a claim the preflight may refuse. The final answer still has to
// state it.
func TestReviewAnswerPlanDoesNotRefuseAnHonestFacetLimitation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# Project\nA Go service.", 20),
	}}}
	intent := claimContract()
	intent.RequiredFacets = []contract.RequiredFacet{{Facet: contract.FacetTests}}
	plan := AnswerPlan{
		Claims:      []string{"The repository contains a Go service."},
		Limitations: []string{"The test inventory was not inspected in this pass."},
	}
	if issues := ReviewAnswerPlan(intent, turns, plan, EvidenceProvenance{}); len(issues) != 0 {
		t.Fatalf("honest facet limitation refused the plan: %+v", issues)
	}
}

func TestParseAnswerPlanValidatesShape(t *testing.T) {
	plan, err := ParseAnswerPlan(map[string]any{
		"claims":      []any{"The project uses Go.", "The project uses Go."},
		"limitations": []any{"The UI was not inspected in this pass."},
	})
	if err != nil {
		t.Fatalf("ParseAnswerPlan: %v", err)
	}
	if len(plan.Claims) != 1 || len(plan.Limitations) != 1 {
		t.Fatalf("plan = %+v, want one deduplicated claim and one limitation", plan)
	}
	if _, err := ParseAnswerPlan(map[string]any{"claims": []any{}}); err == nil {
		t.Fatal("an empty plan was accepted")
	}
	if _, err := ParseAnswerPlan(map[string]any{"claims": []any{42}}); err == nil {
		t.Fatal("a non-string claim was accepted")
	}
}

// A refused plan is a control decision, not an execution failure. Completion
// must not turn it into a pending failure after the answer itself succeeds.
func TestRefusedAnswerPlanIsControlFeedbackNotAFailure(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{{
			ToolName: AnswerPlanToolName,
			Args:     map[string]any{"claims": []any{"An unsupported claim."}},
			Result: Result{
				Text:         "answer plan refused",
				IsError:      true,
				FailureClass: ToolFailureAnswerPlan,
			},
		}}},
		{Results: []ToolOutcome{claimRead("README.md", "# Project\nA Go service.", 20)}},
	}
	verdict := claimVerdict(turns, "The project is a Go service.")
	if !verdict.Complete() {
		t.Fatalf("a refused plan vetoed completion: %s", verdict.Reason)
	}
}
