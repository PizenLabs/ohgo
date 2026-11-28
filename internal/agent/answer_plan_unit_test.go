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

// The accepted plan is a claim-strength contract, not a one-time gate. A
// documented claim may not become an unattributed world claim in the prose.
func TestReviewAnswerPlanConformanceCatchesStrengthenedClaim(t *testing.T) {
	plan := AnswerPlan{Claims: []string{
		"According to the README, storage is a local SQLite database with no hosted service, API key, or telemetry.",
	}}
	answer := "Everything is stored in a local SQLite database; the README states there is no hosted service, API key, or telemetry."
	issues := ReviewAnswerPlanConformance(nil, answer, plan)
	if len(issues) != 1 || issues[0].Code != "plan_claim_strengthened" {
		t.Fatalf("issues = %+v, want one strengthened documented claim", issues)
	}
	if !strings.Contains(issues[0].Claim, "Everything is stored") {
		t.Fatalf("issue named the wrong clause: %+v", issues[0])
	}
	if !strings.Contains(issues[0].Action, "According to the README") {
		t.Fatalf("repair did not carry the accepted wording: %+v", issues[0])
	}

	acceptedWording := "According to the README, storage is a local SQLite database; the README states there is no hosted service, API key, or telemetry."
	if issues := ReviewAnswerPlanConformance(nil, acceptedWording, plan); len(issues) != 0 {
		t.Fatalf("the accepted wording was refused: %+v", issues)
	}
}

// A plan with no documentation attribution cannot impose an attribution rule
// on a source-observed claim, and a plan ledger renders exactly the accepted
// claims it was given.
func TestAnswerPlanLedgerRendersAcceptedClaims(t *testing.T) {
	plan := AnswerPlan{
		Claims:      []string{"The root Cargo.toml declares seven crates.", "The CLI is defined in crates/gry-cli/src/main.rs."},
		Limitations: []string{"The internals were not read in this pass."},
	}
	got := RenderAnswerPlanLedger("Accepted:\n{{claims}}\nLimits:\n{{limitations}}", plan)
	for _, want := range []string{"The root Cargo.toml declares seven crates.", "The CLI is defined", "The internals were not read", "Limits:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ledger missing %q:\n%s", want, got)
		}
	}
	if issues := ReviewAnswerPlanConformance(nil, "The root Cargo.toml declares seven crates.", plan); len(issues) != 0 {
		t.Fatalf("a source-only plan imposed attribution: %+v", issues)
	}
}
