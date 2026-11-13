package cognition

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

func coverageState(t *testing.T, summary ClaimSummary, class ClaimClass) ClaimCoverageState {
	t.Helper()
	coverage := summary.CoverageFor(class)
	if coverage.Class != class {
		t.Fatalf("coverage class = %q, want %q", coverage.Class, class)
	}
	return coverage.State
}

// Every claim class has an explicit requirement and a stable identity.
func TestClaimRequirementsCoverEveryClass(t *testing.T) {
	requirements := ClaimRequirements()
	if len(requirements) != 10 {
		t.Fatalf("requirements = %d, want every claim class", len(requirements))
	}
	seen := map[ClaimClass]bool{}
	for _, requirement := range requirements {
		if requirement.Class == "" || requirement.Evidence == "" || requirement.Scope == "" {
			t.Fatalf("incomplete requirement: %+v", requirement)
		}
		if seen[requirement.Class] {
			t.Fatalf("duplicate requirement for %q", requirement.Class)
		}
		seen[requirement.Class] = true
		if _, ok := ClaimRequirementFor(requirement.Class); !ok {
			t.Fatalf("no lookup for %q", requirement.Class)
		}
	}
	for _, class := range []ClaimClass{
		ClaimProjectIdentity, ClaimTopology, ClaimConfiguration,
		ClaimImplementationStatus, ClaimDependencyUse, ClaimBehavior,
		ClaimRuntimeState, ClaimStructuralRelationship, ClaimAbsence,
		ClaimTestInventory,
	} {
		if !seen[class] {
			t.Fatalf("class %q has no requirement", class)
		}
	}
}

// The coverage view keeps the evidence classes separate. None of these
// observations may silently satisfy a stronger class.
func TestClaimCoverageSeparatesEvidenceClasses(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})

	documentation := claimState(t, shape,
		readEvidenceForTest("read-1", "README.md", "# Project\n\nA Go service."),
	).ClaimSummary()
	if got := coverageState(t, documentation, ClaimProjectIdentity); got != ClaimSupported {
		t.Fatalf("project identity from documentation = %s", got)
	}
	for _, class := range []ClaimClass{
		ClaimImplementationStatus, ClaimBehavior, ClaimRuntimeState,
		ClaimStructuralRelationship,
	} {
		if got := coverageState(t, documentation, class); got != ClaimUnobserved {
			t.Fatalf("documentation supported %s: %s", class, got)
		}
	}

	configuration := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/go.mod", "module ecommerce\nrequire github.com/redis/go-redis/v9 v9.5.0"),
		readEvidenceForTest("read-2", "backend/docker-compose.yml", "services:\n  product-service:\n    ports:\n      - \"8081:8080\"\n"),
	).ClaimSummary()
	if got := coverageState(t, configuration, ClaimConfiguration); got != ClaimSupported {
		t.Fatalf("configuration = %s", got)
	}
	if got := coverageState(t, configuration, ClaimTopology); got != ClaimSupported {
		t.Fatalf("topology = %s", got)
	}
	if got := coverageState(t, configuration, ClaimImplementationStatus); got != ClaimUnobserved {
		t.Fatalf("configuration supported implementation: %s", got)
	}
	if got := coverageState(t, configuration, ClaimBehavior); got != ClaimUnobserved {
		t.Fatalf("configuration supported behavior: %s", got)
	}
	if got := coverageState(t, configuration, ClaimDependencyUse); got != ClaimPartial {
		t.Fatalf("declared dependency = %s, want PARTIAL", got)
	}

	inventory := claimState(t, shape, agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "backend/internal"},
		Result: agent.Result{Text: "backend/internal\norder/\nproduct/\n",
			Details: map[string]any{"path": "backend/internal", "entries": 2}},
	}).ClaimSummary()
	if got := coverageState(t, inventory, ClaimTopology); got != ClaimSupported {
		t.Fatalf("inventory topology = %s", got)
	}
	if got := coverageState(t, inventory, ClaimBehavior); got != ClaimUnobserved {
		t.Fatalf("inventory supported behavior: %s", got)
	}
	if got := coverageState(t, inventory, ClaimStructuralRelationship); got != ClaimUnobserved {
		t.Fatalf("inventory supported a structural relationship: %s", got)
	}

	source := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/internal/product/service.go",
			"package product\nfunc Create() {}\nfunc Update() {}"),
	).ClaimSummary()
	if got := coverageState(t, source, ClaimImplementationStatus); got != ClaimSupported {
		t.Fatalf("source implementation = %s", got)
	}
	if got := coverageState(t, source, ClaimBehavior); got != ClaimSupported {
		t.Fatalf("source behavior = %s", got)
	}
	if got := coverageState(t, source, ClaimStructuralRelationship); got != ClaimPartial {
		t.Fatalf("source relationship = %s, want PARTIAL", got)
	}
	if got := coverageState(t, source, ClaimRuntimeState); got != ClaimUnobserved {
		t.Fatalf("source supported runtime state: %s", got)
	}

	structural := claimState(t, shape, agent.ToolOutcome{
		CallID: "graph-1", ToolName: "graph",
		Args: map[string]any{"symbol": "Checkout"},
		Result: agent.Result{
			Text: "Order.Checkout calls payment.Authorize",
			Details: map[string]any{
				"graph_state": "CURRENT", "complete": true,
			},
		},
	}).ClaimSummary()
	if got := coverageState(t, structural, ClaimStructuralRelationship); got != ClaimSupported {
		t.Fatalf("structural relationship = %s", got)
	}

	runtimeState := claimState(t, shape, agent.ToolOutcome{
		CallID: "verify-1", ToolName: "verify",
		Result: agent.Result{
			Text: "runtime ready",
			Runtime: &agent.RuntimeEvidence{
				Command: "go run ./backend/cmd/product-service",
				URL:     "http://localhost:8081",
				Checks:  1,
			},
		},
	}).ClaimSummary()
	if got := coverageState(t, runtimeState, ClaimRuntimeState); got != ClaimSupported {
		t.Fatalf("runtime state = %s", got)
	}
	if got := coverageState(t, runtimeState, ClaimBehavior); got != ClaimSupported {
		t.Fatalf("runtime behavior = %s", got)
	}
	if got := coverageState(t, runtimeState, ClaimImplementationStatus); got != ClaimUnobserved {
		t.Fatalf("runtime supported implementation: %s", got)
	}
}

// Test inventory and absence coverage require every completeness axis. A
// complete current inventory supports both; a partial, stale, or truncated one
// supports neither at full strength.
func TestClaimCoverageTestInventoryAndAbsence(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	complete := claimState(t, shape, structureTestOutcome("tests-1", "tests", map[string]any{
		"view": "tests", "graph_state": "CURRENT", "complete": true,
		"partial": false, "truncated": false, "scope_complete": true,
		"inventory_complete": true, "structural_complete": true,
		"completeness": "COMPLETE", "test_files": 0, "total": 0,
	})).ClaimSummary()
	if got := coverageState(t, complete, ClaimTestInventory); got != ClaimSupported {
		t.Fatalf("complete test inventory = %s", got)
	}
	if got := coverageState(t, complete, ClaimAbsence); got != ClaimSupported {
		t.Fatalf("complete absence scope = %s", got)
	}

	for name, override := range map[string]map[string]any{
		"partial scope":     {"scope_complete": false},
		"partial inventory": {"inventory_complete": false},
		"unparsed":          {"structural_complete": false, "completeness": "UNPARSED"},
		"truncated":         {"truncated": true},
		"stale":             {"graph_state": "STALE"},
		"excluded root":     {"excluded_roots": []map[string]any{{"path": "vendor"}}},
		"unsupported":       {"unsupported_languages": []map[string]any{{"extension": ".tsx", "files": 2}}},
		"nested unresolved": {"nested_projects": []map[string]any{{"path": "backend", "included": false}}},
	} {
		t.Run(name, func(t *testing.T) {
			details := map[string]any{
				"view": "tests", "graph_state": "CURRENT", "complete": true,
				"partial": false, "truncated": false, "scope_complete": true,
				"inventory_complete": true, "structural_complete": true,
				"completeness": "COMPLETE", "test_files": 0, "total": 0,
			}
			for key, value := range override {
				details[key] = value
			}
			summary := claimState(t, shape, structureTestOutcome("tests-1", "tests", details)).ClaimSummary()
			if got := coverageState(t, summary, ClaimTestInventory); got != ClaimPartial {
				t.Fatalf("%s test inventory = %s, want PARTIAL", name, got)
			}
			absence := coverageState(t, summary, ClaimAbsence)
			if name == "stale" {
				// A stale observation is not a current scope at all; it is
				// not merely a partial current one.
				if absence != ClaimUnobserved {
					t.Fatalf("stale absence = %s, want NOT_OBSERVED", absence)
				}
				return
			}
			if absence != ClaimPartial {
				t.Fatalf("%s absence = %s, want PARTIAL", name, absence)
			}
		})
	}
}

// Dependency coverage follows the same ladder as the answer audit.
func TestClaimCoverageDependencyLadder(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	manifest := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/go.mod", "module ecommerce\nrequire github.com/redis/go-redis/v9 v9.5.0"),
	).ClaimSummary()
	if got := coverageState(t, manifest, ClaimDependencyUse); got != ClaimPartial {
		t.Fatalf("DECLARED = %s", got)
	}
	imported := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/internal/product/cache.go",
			"package product\nimport \"github.com/redis/go-redis/v9\""),
	).ClaimSummary()
	if got := coverageState(t, imported, ClaimDependencyUse); got != ClaimPartial {
		t.Fatalf("IMPORTED = %s", got)
	}
	used := claimState(t, shape,
		readEvidenceForTest("read-1", "backend/internal/product/cache.go",
			"package product\nimport \"github.com/redis/go-redis/v9\"\nfunc Get(){ r.Get(ctx, k) }"),
	).ClaimSummary()
	if got := coverageState(t, used, ClaimDependencyUse); got != ClaimSupported {
		t.Fatalf("USED_IN_SOURCE = %s", got)
	}
}

// Coverage is a deterministic projection of the same summary.
func TestClaimCoverageIsDeterministic(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	outcomes := []agent.ToolOutcome{
		readEvidenceForTest("read-1", "README.md", "# Project"),
		readEvidenceForTest("read-2", "backend/internal/product/service.go", "package product\nfunc Create() {}"),
		structureTestOutcome("tests-1", "tests", map[string]any{
			"view": "tests", "graph_state": "CURRENT", "complete": true,
			"scope_complete": true, "inventory_complete": true,
			"structural_complete": true, "completeness": "COMPLETE",
			"test_files": 0, "total": 0,
		}),
	}
	first := claimState(t, shape, outcomes...).ClaimSummary().Coverage()
	second := claimState(t, shape, outcomes...).ClaimSummary().Coverage()
	if len(first) != len(second) {
		t.Fatalf("coverage lengths differ: %d != %d", len(first), len(second))
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("coverage[%d] = %+v, want %+v", index, first[index], second[index])
		}
	}
}
