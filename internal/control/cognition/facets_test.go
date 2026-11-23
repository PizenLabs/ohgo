package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func relationshipFacetShape(t *testing.T, objective string) TaskShape {
	t.Helper()
	_, shape := regressionShape(objective, RepositoryFacts{Go: true})
	if !shape.NeedsRelationshipNavigation {
		t.Fatalf("shape did not classify %q as a relationship task", objective)
	}
	return shape
}

func facetStatusFor(suff Sufficiency, facet EvidenceFacet) (FacetStatus, bool) {
	for _, status := range suff.Facets {
		if status.Facet == facet {
			return status, true
		}
	}
	return FacetStatus{}, false
}

func structuralRelationsOutcome(id string, edges ...map[string]any) agent.ToolOutcome {
	return structureTestOutcome(id, "relations", map[string]any{
		"view":        "relations",
		"graph_state": "CURRENT",
		"found":       true,
		"complete":    true,
		"snapshot_id": "snap-facet",
		"targets": []map[string]any{
			{"qualified_name": "checkout.Service", "path": "backend/internal/checkout/service.go", "line": 12},
		},
		"edges": edges,
		"coordinates": []map[string]any{
			{"path": "backend/internal/checkout/service.go", "line": 12, "symbol": "checkout.Service", "kind": "target"},
		},
	})
}

// An architecture dependency question must acquire the relationship facets
// before the control plane can call the task answerable. A lexical/source
// observation that merely mentions both endpoints is not a structural edge.
func TestArchitectureDependencyFacetsGateCompletion(t *testing.T) {
	shape := relationshipFacetShape(t,
		"Trace the architecture dependencies and interface implementations from checkout to cart and productcatalog.")
	for _, facet := range []EvidenceFacet{
		FacetTarget, FacetRelationship, FacetInterfaceBoundary, FacetImplementation,
	} {
		requireNeed(t, shape, FacetKey(facet), true)
	}
	if shape.StructuralDepth != StructuralImpact {
		t.Fatalf("structural depth = %s, want IMPACT for a relationship facet", shape.StructuralDepth)
	}

	empty := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	if empty.State != Insufficient || empty.RequiredFacets != 4 || empty.SatisfiedFacets != 0 {
		t.Fatalf("empty verdict = %+v, want INSUFFICIENT with 0/4 facets", empty)
	}
	for _, key := range []string{
		FacetKey(FacetTarget), FacetKey(FacetRelationship),
		FacetKey(FacetInterfaceBoundary), FacetKey(FacetImplementation),
	} {
		if !containsString(empty.Missing, key) {
			t.Fatalf("missing = %v, want %s", empty.Missing, key)
		}
	}
	if !strings.Contains(strings.Join(empty.Next, " "), "view=relations") {
		t.Fatalf("next = %v, want the structural relations acquisition", empty.Next)
	}

	// A source read proves implementation content, but it does not prove a
	// dependency edge, an interface boundary, or a target coordinate.
	lexical := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/internal/checkout/service.go",
			"package checkout\n// checkout calls cart.CartService and productcatalog.ProductService\n"),
	)
	suff := EvaluateSufficiency(shape, lexical, ExecutionState{}, false)
	if suff.State == Sufficient || suff.Stage == StageAnswer {
		t.Fatalf("lexical source evidence closed the relationship task: %+v", suff)
	}
	if status, ok := facetStatusFor(suff, FacetRelationship); !ok || status.State != FacetMissing {
		t.Fatalf("relationship facet = %+v, want MISSING", status)
	}

	relations := claimState(t, shape, structuralRelationsOutcome("structure-1",
		map[string]any{"from": "checkout.Service", "to": "cart.CartService", "kind": "CALLS"},
		map[string]any{"from": "checkout.Service", "to": "cart.CartReader", "kind": "IMPLEMENTS"},
	))
	suff = EvaluateSufficiency(shape, relations, ExecutionState{}, false)
	if suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("structural relations verdict = %+v, want SUFFICIENT/answer", suff)
	}
	if suff.RequiredFacets != 4 || suff.SatisfiedFacets != 4 {
		t.Fatalf("facet coverage = %d/%d, want 4/4", suff.SatisfiedFacets, suff.RequiredFacets)
	}
	status, ok := facetStatusFor(suff, FacetRelationship)
	if !ok || status.State != FacetSatisfied || len(status.Evidence) == 0 {
		t.Fatalf("relationship facet = %+v, want satisfied with provenance", status)
	}
	if !strings.Contains(strings.Join(status.Evidence, "\n"), "snap-facet") {
		t.Fatalf("relationship provenance = %v, want the structural snapshot id", status.Evidence)
	}
}

// A flow question adds the flow facet; a call edge satisfies it, while an
// implementation edge alone does not.
func TestFlowFacetRequiresCallEvidence(t *testing.T) {
	shape := relationshipFacetShape(t, "Trace the request flow from checkout to cart.")
	requireNeed(t, shape, FacetKey(FacetFlow), true)

	implementsOnly := claimState(t, shape, structuralRelationsOutcome("structure-1",
		map[string]any{"from": "checkout.Service", "to": "cart.CartReader", "kind": "IMPLEMENTS"},
	))
	suff := EvaluateSufficiency(shape, implementsOnly, ExecutionState{}, false)
	if status, _ := facetStatusFor(suff, FacetFlow); status.State != FacetMissing {
		t.Fatalf("flow facet = %+v, want MISSING without a call/reference edge", status)
	}

	withCall := claimState(t, shape, structuralRelationsOutcome("structure-1",
		map[string]any{"from": "checkout.Service", "to": "cart.CartService", "kind": "CALLS"},
	))
	suff = EvaluateSufficiency(shape, withCall, ExecutionState{}, false)
	if status, _ := facetStatusFor(suff, FacetFlow); status.State != FacetSatisfied {
		t.Fatalf("flow facet = %+v, want SATISFIED with a call edge", status)
	}
}

// The structural provider inside an investigation contributes the same facets
// as the direct structure tool. A derived investigation status alone does not:
// only the provider's structured facts are evidence.
func TestInvestigationStructuralProviderContributesFacets(t *testing.T) {
	shape := relationshipFacetShape(t, "Trace the architecture dependencies, interface boundaries, and request flow from checkout to cart.")
	state := claimState(t, shape, agent.ToolOutcome{
		CallID: "investigate-1", ToolName: "investigate",
		Args: map[string]any{"question": shape.Objective},
		Result: agent.Result{
			Text: "investigation: sufficient",
			Details: map[string]any{
				"status":      "sufficient",
				"stop_reason": "confidence target reached",
				"providers": []map[string]any{
					{"provider": "structural", "outcome": "SUCCESS", "state": "OBSERVED"},
				},
				"facts": []map[string]any{
					{"subject": "boundary", "detail": "checkout -> cart (2 crossings)"},
					{"subject": "caller", "detail": "cart.CartService at backend/internal/checkout/service.go:42"},
				},
			},
		},
	})
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	for _, facet := range []EvidenceFacet{FacetTarget, FacetRelationship, FacetInterfaceBoundary, FacetFlow} {
		if status, _ := facetStatusFor(suff, facet); status.State != FacetSatisfied {
			t.Fatalf("%s facet = %+v, want SATISFIED from the structural provider facts", facet, status)
		}
	}
	// The structural provider did not read implementation source, so that
	// facet remains open.
	if status, _ := facetStatusFor(suff, FacetImplementation); status.State != FacetMissing {
		t.Fatalf("implementation facet = %+v, want MISSING without implementation source", status)
	}
}

// The state block exposes facet coverage so the model sees the required
// frontier before writing an answer.
func TestStateBlockShowsFacetCoverage(t *testing.T) {
	shape := relationshipFacetShape(t, "Trace the architecture dependencies from checkout to cart.")
	state := NewEvidenceState()
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	rendered := RenderState(shape, state, ExecutionState{}, suff, CandidateSet{}, 0, 0)
	if !strings.Contains(rendered, "Evidence facets: 0/") {
		t.Fatalf("state block did not disclose facet coverage:\n%s", rendered)
	}
	if !strings.Contains(rendered, "relationship") {
		t.Fatalf("state block did not name the missing relationship facet:\n%s", rendered)
	}
}

// Facet requirements are placed before supporting repository requirements so
// a repair turn spends its next observation on the missing relationship, not
// on a secondary overview or implementation remark.
func TestFacetRequirementsPrecedeSupportingClaims(t *testing.T) {
	_, shape := regressionShape(
		"Investigate this project's architecture dependencies and interface implementations.",
		RepositoryFacts{Go: true})
	if !shape.BroadRepositoryAnalysis && !shape.Investigation {
		t.Fatalf("test shape is not a repository investigation: %+v", shape)
	}
	requireNeed(t, shape, FacetKey(FacetRelationship), true)
	requireNeed(t, shape, "repository_overview", true)

	var keys []string
	for _, need := range shape.RequiredKnowledge {
		keys = append(keys, need.Key)
	}
	relationship := -1
	overview := -1
	for i, key := range keys {
		if key == FacetKey(FacetRelationship) {
			relationship = i
		}
		if key == "repository_overview" {
			overview = i
		}
	}
	if relationship < 0 || overview < 0 || relationship > overview {
		t.Fatalf("requirement order = %v, want the relationship facet before repository_overview", keys)
	}

	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	missing := strings.Join(suff.Missing, "\x00")
	if !strings.Contains(missing, FacetKey(FacetRelationship)) ||
		strings.Index(missing, FacetKey(FacetRelationship)) > strings.Index(missing, "repository_overview") {
		t.Fatalf("missing order = %v, want the relationship facet before repository_overview", suff.Missing)
	}
}

// The projection audit carries facet coverage, so a resumed run or an external
// audit can see which structural frontier was still open.
func TestProjectionReportsFacetCoverage(t *testing.T) {
	shape := relationshipFacetShape(t,
		"Trace the architecture dependencies and interface implementations from checkout to cart.")
	projector := NewProjector(shape, NewEvidenceState(), shape.RepositoryFacts)
	projected := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
	})
	if projected.Report.FacetCoverage != "0/4" {
		t.Fatalf("facet coverage = %q, want 0/4", projected.Report.FacetCoverage)
	}
	if len(projected.Report.Facets) != 4 {
		t.Fatalf("facet rows = %v, want four", projected.Report.Facets)
	}
	for _, row := range projected.Report.Facets {
		if !strings.HasSuffix(row, "=MISSING") {
			t.Fatalf("facet row = %q, want MISSING", row)
		}
	}
}

// A source read can carry implementation content, but it cannot substitute for
// a structural target or edge. Confidence is not part of this rule.
func TestFacetsDoNotUseConfidence(t *testing.T) {
	shape := relationshipFacetShape(t, "Who calls parseConfig?")
	state := claimState(t, shape, readEvidenceForTest("read-1", "config.go",
		"package config\n// parseConfig is called by loadConfig"))
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State == Sufficient || suff.Stage == StageAnswer {
		t.Fatalf("source-only relationship evidence closed the task: %+v", suff)
	}
	if status, _ := facetStatusFor(suff, FacetRelationship); status.State != FacetMissing {
		t.Fatalf("relationship facet = %+v, want MISSING", status)
	}
}

// An interface word in source prose is not an observed interface boundary.
func TestSourceProseDoesNotSatisfyBoundaryFacet(t *testing.T) {
	shape := relationshipFacetShape(t, "Trace the interface implementations from checkout to cart.")
	state := claimState(t, shape, readEvidenceForTest("read-1", "checkout.go",
		"package checkout\n// CartReader is an interface implemented by cart.CartService\n"))
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if status, _ := facetStatusFor(suff, FacetInterfaceBoundary); status.State != FacetMissing {
		t.Fatalf("boundary facet = %+v, want MISSING from source prose", status)
	}
}

// A general definition that happens to contain a relationship word is not a
// repository traversal. "What is an interface?" must not require a structural
// index merely because the word appears.
func TestDefinitionQuestionDoesNotAcquireFacets(t *testing.T) {
	_, shape := regressionShape("What is an interface?", RepositoryFacts{Go: true})
	if len(shape.RequiredFacets) != 0 {
		t.Fatalf("definition question acquired facets: %+v", shape.RequiredFacets)
	}
	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	if suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("definition question = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
}

func TestFacetsDoNotWidenSimpleReadTasks(t *testing.T) {
	_, shape := regressionShape("Read the checkout service file.", RepositoryFacts{Go: true})
	if len(shape.RequiredFacets) != 0 {
		t.Fatalf("simple read task acquired facets: %+v", shape.RequiredFacets)
	}
	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	if suff.RequiredFacets != 0 || len(suff.Facets) != 0 {
		t.Fatalf("simple read task rendered facets: %+v", suff.Facets)
	}
}

// The facet requirement is derived from the contract, not from a model turn.
// Two shapes for the same request are identical.
func TestFacetPolicyIsDeterministic(t *testing.T) {
	first := relationshipFacetShape(t, "Trace the architecture dependencies from checkout to cart.")
	second := relationshipFacetShape(t, "Trace the architecture dependencies from checkout to cart.")
	if len(first.RequiredFacets) != len(second.RequiredFacets) {
		t.Fatalf("facet count differs: %d != %d", len(first.RequiredFacets), len(second.RequiredFacets))
	}
	for i := range first.RequiredFacets {
		if first.RequiredFacets[i] != second.RequiredFacets[i] {
			t.Fatalf("facet[%d] = %+v, want %+v", i, first.RequiredFacets[i], second.RequiredFacets[i])
		}
	}
	if first.StructuralDepth != second.StructuralDepth || first.StructuralDepth != StructuralImpact {
		t.Fatalf("structural depth = %s/%s, want IMPACT", first.StructuralDepth, second.StructuralDepth)
	}
}

// The control plane owns facet activation: the structure tool is offered for a
// relationship intent even before any source has been read, and the candidate
// set does not depend on a model's prior structural call.
func TestRelationshipFacetsActivateStructureDeterministically(t *testing.T) {
	shape := relationshipFacetShape(t, "Trace the architecture dependencies from checkout to cart.")
	tools := []agent.Tool{
		testTool{name: "structure", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}},
		testTool{name: "read", tier: agent.TierRead},
	}
	set := Candidates(shape, NewEvidenceState(), ExecutionState{}, tools, shape.RepositoryFacts,
		EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false))
	var names []string
	for _, tool := range set.Tools {
		names = append(names, tool.Name())
	}
	if !containsString(names, "structure") {
		t.Fatalf("candidates = %v, want structure for a required relationship facet", names)
	}
}

// A non-relationship broad account remains an overview task. The facet policy
// must not turn "what is this project" into an impact analysis.
func TestBroadOverviewDoesNotAcquireRelationshipFacets(t *testing.T) {
	_, shape := regressionShape("Analyze what this project is about.", RepositoryFacts{Go: true})
	if len(shape.RequiredFacets) != 0 {
		t.Fatalf("broad overview acquired relationship facets: %+v", shape.RequiredFacets)
	}
	if shape.StructuralDepth != StructuralOverview {
		t.Fatalf("structural depth = %s, want OVERVIEW", shape.StructuralDepth)
	}
}

// A changed contract can be re-scoped while observations remain. Facet
// requirements are recomputed from the new shape, not retained from the old
// task.
func TestFacetRequirementsFollowShapeRescoping(t *testing.T) {
	oldShape := relationshipFacetShape(t, "Trace the architecture dependencies from checkout to cart.")
	state := claimState(t, oldShape, structuralRelationsOutcome("structure-1",
		map[string]any{"from": "checkout.Service", "to": "cart.CartService", "kind": "CALLS"}))
	state.ResetRequirements()
	newShape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Read the checkout service file.",
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
	suff := EvaluateSufficiency(newShape, state, ExecutionState{}, false)
	if suff.RequiredFacets != 0 {
		t.Fatalf("rescoped task retained facet requirements: %+v", suff.Facets)
	}
	if len(state.Requirements) != 0 {
		t.Fatalf("reset state retained requirements: %+v", state.Requirements)
	}
}
