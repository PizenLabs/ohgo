package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// structureTestOutcome builds one structural observation with the exact
// completeness axes the claim projection must respect.
func structureTestOutcome(id, view string, details map[string]any) agent.ToolOutcome {
	args := map[string]any{"view": view}
	if path, ok := details["path"].(string); ok {
		args["path"] = path
	}
	return agent.ToolOutcome{
		CallID: id, ToolName: "structure", Args: args,
		Result: agent.Result{Text: view + " observation", Details: details},
	}
}

func claimState(t *testing.T, shape TaskShape, outcomes ...agent.ToolOutcome) *EvidenceState {
	t.Helper()
	state := NewEvidenceState()
	state.ObserveTurns(shape, []agent.Turn{{Results: outcomes}})
	return state
}

func frontierText(summary ClaimSummary) string {
	return strings.Join(summary.EvidenceFrontier(), "\n")
}

// A documentation-only observation establishes documentation and nothing
// stronger. The frontier names what would change that.
func TestClaimFrontierDocumentationOnly(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape, readEvidenceForTest("read-1", "README.md", "# Project"))
	summary := state.ClaimSummary()
	if !summary.HasDocumentation || summary.HasImplementation || summary.HasConfiguration {
		t.Fatalf("documentation-only summary = %+v", summary)
	}
	rendered := RenderState(shape, state, SummarizeExecution(nil),
		Sufficiency{State: Insufficient, Stage: StageExplore}, CandidateSet{}, 0, 0)
	if !strings.Contains(rendered, "documented=OBSERVED") ||
		!strings.Contains(rendered, "implementation=NOT_OBSERVED") {
		t.Fatalf("state block did not keep documentation separate:\n%s", rendered)
	}
}

// Configuration establishes what is declared. It does not establish an
// implementation observation, and its dependency signal is DECLARED only.
func TestClaimFrontierConfigurationOnly(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/go.mod", "module ecommerce\nrequire github.com/redis/go-redis/v9 v9.5.0"),
		readEvidenceForTest("read-2", "backend/docker-compose.yml", "services:\n  redis:\n    image: redis:7"),
	)
	summary := state.ClaimSummary()
	if !summary.HasConfiguration || summary.HasImplementation {
		t.Fatalf("configuration summary = %+v", summary)
	}
	if summary.DependencyLevel != "DECLARED" {
		t.Fatalf("dependency level = %q, want DECLARED", summary.DependencyLevel)
	}
	frontier := frontierText(summary)
	if !strings.Contains(frontier, "SOURCE_CONTENT missing") {
		t.Fatalf("frontier = %q, want implementation source missing", frontier)
	}
}

// A directory listing is inventory. It proves names exist; it is not source
// content and not a source-path-only observation.
func TestClaimFrontierInventoryOnly(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape, agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "backend/internal"},
		Result: agent.Result{Text: "backend/internal\norder/\nproduct/\n",
			Details: map[string]any{"path": "backend/internal", "entries": 2}},
	})
	summary := state.ClaimSummary()
	if !summary.HasInventory || summary.HasImplementation || summary.HasSourcePath {
		t.Fatalf("inventory summary = %+v", summary)
	}
	frontier := frontierText(summary)
	if !strings.Contains(frontier, "component order") ||
		!strings.Contains(frontier, "SOURCE_CONTENT missing") {
		t.Fatalf("frontier = %q, want the component source gap", frontier)
	}
	if !strings.Contains(frontier, "read backend/internal/order") {
		t.Fatalf("frontier = %q, want a concrete acquisition coordinate", frontier)
	}
}

// A glob returns source coordinates, not source content. The frontier names
// the component whose content is still missing.
func TestClaimFrontierSourcePathWithoutContent(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape, agent.ToolOutcome{
		CallID: "glob-1", ToolName: "glob", Args: map[string]any{"pattern": "**/*.go"},
		Result: agent.Result{
			Text: "backend/internal/product/service.go\n",
			Details: map[string]any{
				"pattern": "**/*.go", "matches": 1, "ignored": 0, "excluded": 0,
				"gitignore": true,
			},
		},
	})
	summary := state.ClaimSummary()
	if summary.HasImplementation || !summary.HasSourcePath {
		t.Fatalf("glob summary = %+v, want source path without implementation", summary)
	}
	if !strings.Contains(frontierText(summary), "component product") {
		t.Fatalf("frontier = %q, want product source missing", frontierText(summary))
	}
}

// Source content for one component is implementation evidence for that
// component only. A second component remains a gap.
func TestClaimFrontierSourceContentIsComponentScoped(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape,
		agent.ToolOutcome{
			CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "backend/internal"},
			Result: agent.Result{Text: "backend/internal\norder/\nproduct/\n",
				Details: map[string]any{"path": "backend/internal", "entries": 2}},
		},
		readEvidenceForTest("read-1", "backend/internal/product/service.go", "package product"),
	)
	summary := state.ClaimSummary()
	if !summary.HasImplementation {
		t.Fatalf("source read was not implementation: %+v", summary)
	}
	components := map[string]ComponentEvidence{}
	for _, component := range summary.Components {
		components[component.Name] = component
	}
	if !components["product"].SourceContent {
		t.Fatalf("components = %+v, want product source content", summary.Components)
	}
	if components["order"].SourceContent {
		t.Fatalf("components = %+v, product source leaked to order", summary.Components)
	}
	frontier := frontierText(summary)
	if !strings.Contains(frontier, "component order") {
		t.Fatalf("frontier = %q, want order source missing", frontier)
	}
	if strings.Contains(frontier, "component product") {
		t.Fatalf("frontier = %q, product should not be a gap", frontier)
	}
}

// An imported dependency is weaker than a source operation. The state block
// must say so before the model writes "uses".
func TestClaimFrontierDependencyStrength(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	importOnly := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/internal/product/cache.go",
			"package product\nimport \"github.com/redis/go-redis/v9\""),
	)
	summary := importOnly.ClaimSummary()
	if summary.DependencyLevel != "IMPORTED" {
		t.Fatalf("dependency level = %q, want IMPORTED", summary.DependencyLevel)
	}
	if !strings.Contains(frontierText(summary), "IMPORTED only") {
		t.Fatalf("frontier = %q, want the import/use distinction", frontierText(summary))
	}

	used := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/internal/product/cache.go",
			"package product\nimport \"github.com/redis/go-redis/v9\"\nfunc New(){ r := redis.NewClient(nil) }\nfunc Get(){ r.Get(ctx, k) }"),
	)
	if got := used.ClaimSummary().DependencyLevel; got != "USED_IN_SOURCE" {
		t.Fatalf("dependency level = %q, want USED_IN_SOURCE", got)
	}
}

// A live runtime is its own evidence class. It does not become a dependency
// signal unless the observation names the dependency and a readiness/operation.
func TestClaimFrontierRuntimeEvidence(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	plain := claimState(t, shape, agent.ToolOutcome{
		CallID: "verify-1", ToolName: "verify",
		Result: agent.Result{
			Text: "runtime ready",
			Runtime: &agent.RuntimeEvidence{
				Command: "go run ./backend/cmd/product-service",
				URL:     "http://localhost:8081",
				Checks:  1,
			},
		},
	})
	summary := plain.ClaimSummary()
	if !summary.HasRuntime {
		t.Fatalf("runtime observation was not recorded: %+v", summary)
	}
	if summary.DependencyLevel == "RUNTIME" {
		t.Fatalf("a runtime without a dependency name became a dependency signal: %+v", summary)
	}

	withDependency := claimState(t, shape, agent.ToolOutcome{
		CallID: "verify-2", ToolName: "verify",
		Result: agent.Result{
			Text: "redis ping ok",
			Runtime: &agent.RuntimeEvidence{
				Command: "go run ./backend/cmd/product-service",
				URL:     "http://localhost:8081",
				Checks:  1,
			},
		},
	})
	if got := withDependency.ClaimSummary().DependencyLevel; got != "RUNTIME" {
		t.Fatalf("dependency level = %q, want RUNTIME", got)
	}
}

// A complete current test inventory is the only state that permits the
// absence claim; a partial or stale one keeps the frontier open.
func TestClaimFrontierTestInventoryStates(t *testing.T) {
	completeDetails := map[string]any{
		"view": "tests", "graph_state": "CURRENT", "complete": true,
		"partial": false, "truncated": false, "scope_complete": true,
		"inventory_complete": true, "structural_complete": true,
		"completeness": "COMPLETE", "test_files": 0, "total": 0,
	}
	shape := broadShape(RepositoryFacts{Go: true})
	complete := claimState(t, shape, structureTestOutcome("tests-1", "tests", completeDetails))
	summary := complete.ClaimSummary()
	if summary.TestInventory != "COMPLETE" {
		t.Fatalf("complete inventory = %q", summary.TestInventory)
	}
	if strings.Contains(frontierText(summary), "test absence") {
		t.Fatalf("complete inventory frontier = %q, want no test gap", frontierText(summary))
	}

	partialDetails := map[string]any{
		"view": "tests", "graph_state": "CURRENT", "complete": false,
		"partial": true, "truncated": false, "scope_complete": false,
		"inventory_complete": true, "structural_complete": true,
		"completeness": "COMPLETE", "test_files": 0, "total": 0,
	}
	partial := claimState(t, shape, structureTestOutcome("tests-1", "tests", partialDetails))
	if got := partial.ClaimSummary().TestInventory; got != "PARTIAL" {
		t.Fatalf("partial inventory = %q, want PARTIAL", got)
	}
	if !strings.Contains(frontierText(partial.ClaimSummary()), "test absence") {
		t.Fatalf("partial frontier = %q, want the test gap", frontierText(partial.ClaimSummary()))
	}

	staleDetails := cloneMap(completeDetails)
	staleDetails["graph_state"] = "STALE"
	stale := claimState(t, shape, structureTestOutcome("tests-1", "tests", staleDetails))
	if got := stale.ClaimSummary().TestInventory; got != "PARTIAL" {
		t.Fatalf("stale inventory = %q, want PARTIAL", got)
	}
}

// A truncated or partially scoped inventory is a disclosed gap, not a complete
// repository account.
func TestClaimFrontierScopeAndTruncation(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	details := map[string]any{
		"view": "overview", "graph_state": "CURRENT", "complete": false,
		"partial": true, "truncated": true, "scope_complete": false,
		"inventory_complete": true, "structural_complete": false,
		"completeness": "UNPARSED", "test_files": 1, "total": 0,
		"excluded_source_files": 2,
		"excluded_roots": []map[string]any{
			{"path": "vendor", "reason": "repository scope"},
		},
		"unsupported_languages": []map[string]any{
			{"extension": ".tsx", "files": 3},
		},
	}
	state := claimState(t, shape, structureTestOutcome("overview-1", "overview", details))
	summary := state.ClaimSummary()
	if summary.StructuralScope != "PARTIAL" || summary.ExcludedRoots != 1 ||
		summary.ExcludedSourceFiles != 2 || summary.UnsupportedSources != 3 || !summary.Truncated {
		t.Fatalf("scope summary = %+v", summary)
	}
	frontier := frontierText(summary)
	for _, want := range []string{"scope -> PARTIAL", "TRUNCATED", "unsupported source"} {
		if !strings.Contains(frontier, want) {
			t.Fatalf("frontier = %q, want %q", frontier, want)
		}
	}
}

// An unsupported source language is inventoried but not structurally
// observed. The state block must not present it as covered.
func TestClaimFrontierUnsupportedSource(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true, TypeScript: true})
	state := claimState(t, shape, structureTestOutcome("overview-1", "overview", map[string]any{
		"view": "overview", "graph_state": "CURRENT", "complete": true,
		"scope_complete": true, "inventory_complete": true,
		"structural_complete": true, "completeness": "COMPLETE",
		"test_files": 0, "total": 0,
		"unsupported_languages": []map[string]any{{"extension": ".tsx", "files": 4}},
	}))
	summary := state.ClaimSummary()
	if summary.UnsupportedSources != 4 {
		t.Fatalf("unsupported sources = %d, want 4", summary.UnsupportedSources)
	}
	if !strings.Contains(frontierText(summary), "unsupported source") {
		t.Fatalf("frontier = %q, want the unsupported source gap", frontierText(summary))
	}
}

// A broad overview question that names tests acquires the complete inventory
// requirement; an ordinary targeted question does not.
func TestTestInventoryRequirementIsClaimDriven(t *testing.T) {
	_, broad := regressionShape("Analyze this project's test coverage.", RepositoryFacts{Go: true})
	requireNeed(t, broad, "test_inventory", true)

	_, targeted := regressionShape("Read order/checkout.go", RepositoryFacts{Go: true})
	requireNeed(t, targeted, "test_inventory", false)
}

// A test-coverage request acquires the complete inventory requirement and the
// sufficiency layer names the exact acquisition when it is missing.
func TestTestInventoryRequirementDrivesSufficiency(t *testing.T) {
	_, shape := regressionShape("Analyze this project's test coverage.", RepositoryFacts{Go: true})
	empty := NewEvidenceState()
	suff := EvaluateSufficiency(shape, empty, ExecutionState{}, false)
	if suff.State != Insufficient {
		t.Fatalf("empty evidence = %s, want INSUFFICIENT", suff.State)
	}
	if !containsString(suff.Missing, "test_inventory") {
		t.Fatalf("missing = %v, want test_inventory", suff.Missing)
	}
	if !strings.Contains(strings.Join(suff.Next, " "), "structure") {
		t.Fatalf("next = %v, want the structure tests acquisition", suff.Next)
	}

	state := claimState(t, shape,
		readEvidenceForTest("read-1", "README.md", "# Demo"),
		structureTestOutcome("tests-1", "tests", map[string]any{
			"view": "tests", "graph_state": "CURRENT", "complete": true,
			"partial": false, "truncated": false, "scope_complete": true,
			"inventory_complete": true, "structural_complete": true,
			"completeness": "COMPLETE", "test_files": 0, "total": 0,
		}),
	)
	suff = EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State != Sufficient {
		t.Fatalf("complete inventory + overview = %s/%s, want SUFFICIENT: %v",
			suff.State, suff.Stage, suff.Missing)
	}
}

// cloneMap copies a small details map so a test can vary one axis.
func cloneMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

// Once the inventory is known and the missing requirement is implementation
// content, glob/ls cannot make progress. The runtime withholds them so the
// next action is a target read, not another tree walk.
func TestSourceAcquisitionWithholdsInventoryTools(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	projector := NewProjector(shape, NewEvidenceState(), shape.RepositoryFacts)
	result := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{
			readEvidenceForTest("read-1", "README.md", "# Project"),
			agent.ToolOutcome{
				CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "backend/cmd"},
				Result: agent.Result{Text: "backend/cmd\napi-gateway/\norder-service/\n",
					Details: map[string]any{"path": "backend/cmd", "entries": 2}},
			},
		}}},
		Tools: []agent.Tool{
			testTool{name: "read", tier: agent.TierRead},
			testTool{name: "glob", tier: agent.TierRead},
			testTool{name: "ls", tier: agent.TierRead},
		},
	})
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name()] = true
	}
	if !names["read"] {
		t.Fatalf("offered tools = %+v, want read", names)
	}
	if names["glob"] || names["ls"] {
		t.Fatalf("offered tools = %+v, inventory tools must be withheld for source acquisition", names)
	}
	var reason string
	for _, exclusion := range result.Report.Excluded {
		if exclusion.Tool == "glob" {
			reason = exclusion.Reason
		}
	}
	if !strings.Contains(reason, "source content") {
		t.Fatalf("glob exclusion reason = %q, want the source-content requirement", reason)
	}
}

// A failed or empty search is not a source-path observation. It may be
// inventory context, but the state block must not present it as a coordinate
// the run observed.
func TestEmptyGlobIsNotSourcePathEvidence(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape, agent.ToolOutcome{
		CallID: "glob-empty", ToolName: "glob", Args: map[string]any{"pattern": "src/**/*.tsx"},
		Result: agent.Result{
			Text: "No files matched. 4 paths were excluded by .gitignore; pass \"gitignore\": false to include ignored paths.",
			Details: map[string]any{
				"pattern": "src/**/*.tsx", "matches": 0, "ignored": 4,
				"excluded": 0, "gitignore": true,
			},
		},
	})
	summary := state.ClaimSummary()
	if summary.HasSourcePath || summary.HasImplementation {
		t.Fatalf("empty glob summary = %+v, want neither source path nor implementation", summary)
	}
	if summary.HasInventory {
		t.Fatalf("empty glob summary = %+v, want no inventory claim from a failed search", summary)
	}
}

// A source-scoped grep that matched nothing is not source content. Its notice
// text cannot satisfy an implementation observation.
func TestEmptySourceGrepIsNotImplementation(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape, agent.ToolOutcome{
		CallID: "grep-empty", ToolName: "grep",
		Args: map[string]any{"pattern": "checkout", "path": "backend/internal/product"},
		Result: agent.Result{
			Text: "No matches found.",
			Details: map[string]any{
				"pattern": "checkout", "path": "backend/internal/product",
				"matches": 0, "files": 0,
			},
		},
	})
	summary := state.ClaimSummary()
	if summary.HasImplementation || summary.HasSourcePath {
		t.Fatalf("empty grep summary = %+v, want neither implementation nor source path", summary)
	}
	if state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("an empty source grep satisfied the repository survey")
	}
}

// The claim block must remain within the state-block budget while carrying
// the actionable frontier.
func TestClaimBlockStaysWithinStateBudget(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape,
		agent.ToolOutcome{
			CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "backend/cmd"},
			Result: agent.Result{Text: "backend/cmd\napi-gateway/\ncart-service/\norder-service/\n",
				Details: map[string]any{"path": "backend/cmd", "entries": 3}},
		},
		readEvidenceForTest("read-1", "README.md", "# Project"),
		readEvidenceForTest("read-2", "package.json", `{"dependencies":{"react":"18"}}`),
	)
	rendered := RenderState(shape, state, SummarizeExecution(nil),
		Sufficiency{State: Insufficient, Stage: StageExplore}, CandidateSet{}, 0, 0)
	if len(rendered) > DefaultStateBlockBytes {
		t.Fatalf("state block = %d bytes, budget %d", len(rendered), DefaultStateBlockBytes)
	}
	if !strings.Contains(rendered, "Evidence frontier:") {
		t.Fatalf("state block lost the actionable frontier:\n%s", rendered)
	}
}

// The state block must not describe a source path or inventory as observed
// implementation content.
func TestClaimEvidenceBlockNeverUpgradesEvidenceClass(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := claimState(t, shape,
		agent.ToolOutcome{
			CallID: "glob-1", ToolName: "glob", Args: map[string]any{"pattern": "**/*.go"},
			Result: agent.Result{Text: "backend/internal/product/service.go\n",
				Details: map[string]any{"pattern": "**/*.go", "ignored": 0, "excluded": 0}},
		},
		agent.ToolOutcome{
			CallID: "tests-1", ToolName: "structure", Args: map[string]any{"view": "tests"},
			Result: agent.Result{Text: "tests: 0 (test_files: 0)",
				Details: map[string]any{
					"view": "tests", "graph_state": "CURRENT", "complete": true,
					"scope_complete": true, "inventory_complete": true,
					"structural_complete": true, "completeness": "COMPLETE",
					"test_files": 0, "total": 0,
				}},
		},
	)
	rendered := RenderState(shape, state, SummarizeExecution(nil),
		Sufficiency{State: Insufficient, Stage: StageExplore}, CandidateSet{}, 0, 0)
	if !strings.Contains(rendered, "implementation=NOT_OBSERVED") {
		t.Fatalf("glob path was promoted to implementation:\n%s", rendered)
	}
	if !strings.Contains(rendered, "source_paths=OBSERVED") {
		t.Fatalf("glob path was not classified as a source path:\n%s", rendered)
	}
	if !strings.Contains(rendered, "inventory=OBSERVED") {
		t.Fatalf("structural inventory was not recorded:\n%s", rendered)
	}
	if strings.Contains(rendered, "implementation=OBSERVED") {
		t.Fatalf("path/inventory evidence was promoted to implementation:\n%s", rendered)
	}
}

// The state block is deterministic: the same observations produce the same
// bytes, independent of map iteration order.
func TestClaimStateIsDeterministic(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	outcomes := []agent.ToolOutcome{
		agent.ToolOutcome{
			CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "backend/cmd"},
			Result: agent.Result{Text: "backend/cmd\norder-service/\napi-gateway/\n",
				Details: map[string]any{"path": "backend/cmd", "entries": 2}},
		},
		readEvidenceForTest("read-1", "backend/internal/product/service.go", "package product"),
	}
	first := claimState(t, shape, outcomes...)
	second := claimState(t, shape, outcomes...)
	one := RenderState(shape, first, SummarizeExecution(nil),
		Sufficiency{State: Insufficient, Stage: StageExplore}, CandidateSet{}, 0, 0)
	two := RenderState(shape, second, SummarizeExecution(nil),
		Sufficiency{State: Insufficient, Stage: StageExplore}, CandidateSet{}, 0, 0)
	if one != two {
		t.Fatalf("state block is not deterministic:\n%s\n---\n%s", one, two)
	}
}

// The task shape must stay read-only for an informational repository request.
func TestInformationalShapeStaysReadOnly(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read"})
	if shape.Mutation.Allowed || !shape.Authority.ReadOnly {
		t.Fatalf("informational shape allowed mutation: %+v", shape.Authority)
	}
	if shape.Authority.ExecutionAllowed {
		t.Fatal("ASK shape allowed execution")
	}
}
