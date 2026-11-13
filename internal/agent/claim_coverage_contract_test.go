package agent

import (
	"strings"
	"testing"
)

// testInventoryOutcome builds one structure tests observation. The caller
// varies only the axis under test.
func testInventoryOutcome(id string, testFiles, total int, overrides map[string]any) ToolOutcome {
	details := map[string]any{
		"view":                "tests",
		"graph_state":         "CURRENT",
		"complete":            true,
		"partial":             false,
		"truncated":           false,
		"scope_complete":      true,
		"inventory_complete":  true,
		"structural_complete": true,
		"completeness":        "COMPLETE",
		"test_files":          testFiles,
		"total":               total,
	}
	for key, value := range overrides {
		details[key] = value
	}
	return ToolOutcome{
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result:   Result{Text: "view: tests\n", Details: details},
	}
}

func assertNoClaimCodes(t *testing.T, turns []Turn, answer string) {
	t.Helper()
	if codes := evidenceCodes(t, turns, answer); len(codes) > 0 {
		t.Fatalf("%q codes = %v, want no claim review", answer, codes)
	}
}

// A live runtime observation is behavior evidence even when the claim names no
// port. The runtime's own command must bind it to the claimed component.
func TestRuntimeBehaviorEvidenceWithoutPort(t *testing.T) {
	runtimeTurn := func(service, text string) []Turn {
		return []Turn{{Results: []ToolOutcome{{
			ToolName: "verify",
			Args:     map[string]any{"service": map[string]any{"command": "go run ./backend/cmd/" + service}},
			Result: Result{
				Text: text,
				Runtime: &RuntimeEvidence{
					Command: "go run ./backend/cmd/" + service,
					URL:     "http://localhost:8082",
					Checks:  1,
				},
			},
		}}}}
	}
	assertNoClaimCodes(t, runtimeTurn("cart-service", "cart connected to redis"),
		"The cart service connects to Redis.")

	if codes := evidenceCodes(t, runtimeTurn("product-service", "product connected to redis"),
		"The cart service connects to Redis."); !codes["unsupported_implementation_claim"] {
		t.Fatalf("product runtime supported cart behavior: %v", codes)
	}
	if codes := evidenceCodes(t, runtimeTurn("cart-service", "runtime ready"),
		"The cart service connects to Redis."); !codes["unsupported_implementation_claim"] {
		t.Fatalf("a live runtime without the behavior text supported it: %v", codes)
	}

	// Progressive forms are the same behavior class as "listens"/"connects".
	source := []Turn{{Results: []ToolOutcome{
		claimRead("backend/cmd/cart-service/main.go",
			"package main\nfunc main() { http.ListenAndServe(\":8082\", nil) }", 20),
	}}}
	assertNoClaimCodes(t, source, "The cart service is listening on :8082.")
	compose := []Turn{{Results: []ToolOutcome{
		claimRead("backend/docker-compose.yml", "services:\n  cart-service:\n    ports:\n      - \"8082:8080\"\n", 20),
	}}}
	if codes := evidenceCodes(t, compose, "The cart service is listening on :8082."); !codes["unsupported_implementation_claim"] {
		t.Fatalf("configuration supported a progressive behavior claim: %v", codes)
	}
}

// A runtime-state claim needs a live runtime. Configuration and source declare
// what would run; neither proves that a process is running now.
func TestRuntimeStateClaimRequiresLiveObservation(t *testing.T) {
	compose := []Turn{{Results: []ToolOutcome{
		claimRead("backend/docker-compose.yml", "services:\n  cart-service:\n    ports:\n      - \"8082:8080\"\n", 30),
	}}}
	if codes := evidenceCodes(t, compose, "The cart service is running."); !codes["unsupported_runtime_state"] {
		t.Fatalf("configuration supported a runtime-state claim: %v", codes)
	}

	source := []Turn{{Results: []ToolOutcome{
		claimRead("backend/cmd/cart-service/main.go",
			"package main\nfunc main() { http.ListenAndServe(\":8082\", nil) }", 20),
	}}}
	if codes := evidenceCodes(t, source, "The cart service is running."); !codes["unsupported_runtime_state"] {
		t.Fatalf("source content supported a runtime-state claim: %v", codes)
	}

	runtimeTurn := func(service string) []Turn {
		return []Turn{{Results: []ToolOutcome{{
			ToolName: "verify",
			Args:     map[string]any{"service": map[string]any{"command": "go run ./backend/cmd/" + service}},
			Result: Result{
				Text: "runtime ready",
				Runtime: &RuntimeEvidence{
					Command: "go run ./backend/cmd/" + service,
					URL:     "http://localhost:8082",
					Checks:  1,
				},
			},
		}}}}
	}
	assertNoClaimCodes(t, runtimeTurn("cart-service"), "The cart service is running.")
	if codes := evidenceCodes(t, runtimeTurn("product-service"), "The cart service is running."); !codes["unsupported_runtime_state"] {
		t.Fatalf("another service's runtime supported the claim: %v", codes)
	}

	// "is running the job" is transitive behavior, not the live-process
	// predicate. Source evidence owns it.
	workerSource := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/worker/worker.go",
			"package worker\nfunc (w *Worker) Run(job Job) { w.executor.Execute(job) }", 20),
	}}}
	assertNoClaimCodes(t, workerSource, "The worker is running the job every night.")
	if codes := evidenceCodes(t, workerSource, "The worker is currently running."); !codes["unsupported_runtime_state"] {
		t.Fatalf("transitive source supported the live-process claim: %v", codes)
	}
}

// A concrete test count needs a complete current unscoped test inventory. A
// README or partial inventory cannot establish it.
func TestTestInventoryCountClaimRequiresCompleteCurrentInventory(t *testing.T) {
	complete := []Turn{{Results: []ToolOutcome{
		testInventoryOutcome("tests-1", 120, 120, nil),
	}}}
	assertNoClaimCodes(t, complete, "The project has 120 tests.")

	readme := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "The project has 120 tests.", 30),
	}}}
	if codes := evidenceCodes(t, readme, "The project has 120 tests."); !codes["unsupported_test_inventory_claim"] {
		t.Fatalf("a README count was accepted: %v", codes)
	}

	for name, override := range map[string]map[string]any{
		"partial scope":     {"scope_complete": false},
		"partial inventory": {"inventory_complete": false},
		"unparsed":          {"structural_complete": false},
		"truncated":         {"truncated": true},
		"stale":             {"graph_state": "STALE"},
		"nested unresolved": {"nested_projects": []map[string]any{{
			"path": "backend", "included": false,
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			turns := []Turn{{Results: []ToolOutcome{
				testInventoryOutcome("tests-1", 120, 120, override),
			}}}
			if codes := evidenceCodes(t, turns, "The project has 120 tests."); !codes["unsupported_test_inventory_claim"] {
				t.Fatalf("%s accepted the count: %v", name, codes)
			}
		})
	}

	mismatch := []Turn{{Results: []ToolOutcome{
		testInventoryOutcome("tests-1", 3, 3, nil),
	}}}
	if codes := evidenceCodes(t, mismatch, "The project has 120 tests."); !codes["unsupported_test_inventory_claim"] {
		t.Fatalf("a contradicted count was accepted: %v", codes)
	}

	// Zero is a real observation when the inventory is complete, and it is not
	// accepted from a partial scope.
	zero := []Turn{{Results: []ToolOutcome{
		testInventoryOutcome("tests-1", 0, 0, nil),
	}}}
	assertNoClaimCodes(t, zero, "The project has 0 tests.")
	partialZero := []Turn{{Results: []ToolOutcome{
		testInventoryOutcome("tests-1", 0, 0, map[string]any{"scope_complete": false}),
	}}}
	if codes := evidenceCodes(t, partialZero, "The project has 0 tests."); !codes["unsupported_test_inventory_claim"] {
		t.Fatalf("a partial inventory established a zero count: %v", codes)
	}
}

// A structural relationship needs an observation that binds both endpoints.
// Reading one endpoint is not proof of the relation.
func TestStructuralRelationshipClaimNeedsBothEndpoints(t *testing.T) {
	callSite := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/order/service.go",
			"package order\nfunc Checkout() { payment.Authorize(ctx, total) }", 20),
	}}}
	assertNoClaimCodes(t, callSite, "Order Checkout calls payment.Authorize.")

	oneEndpoint := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/order/service.go", "package order\nfunc Checkout() {}", 20),
	}}}
	if codes := evidenceCodes(t, oneEndpoint, "Order Checkout calls payment.Authorize."); !codes["unsupported_structural_claim"] {
		t.Fatalf("one endpoint supported a relationship: %v", codes)
	}

	structural := []Turn{{Results: []ToolOutcome{{
		ToolName: "graph",
		Args:     map[string]any{"symbol": "Checkout"},
		Result:   Result{Text: "Order.Checkout calls payment.Authorize"},
	}}}}
	assertNoClaimCodes(t, structural, "Order Checkout calls payment.Authorize.")

	documentation := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "Order Checkout calls payment.Authorize.", 20),
	}}}
	if codes := evidenceCodes(t, documentation, "Order Checkout calls payment.Authorize."); !codes["unsupported_structural_claim"] {
		t.Fatalf("documentation supported a structural relationship: %v", codes)
	}
}

// A whole-component implementation claim does not establish every capability
// in its "with ..." list. Each asserted aspect needs source evidence.
func TestImplementationAspectsRequireObservedSource(t *testing.T) {
	answer := "Product-service is fully implemented with CRUD, Redis caching, graceful shutdown, and structured logging."
	scope := claimStructureFiles("backend/internal/product", []string{
		"backend/internal/product/main.go",
	})
	bare := []Turn{{Results: []ToolOutcome{
		scope,
		claimRead("backend/internal/product/main.go", "package product\nfunc main() {}", 20),
	}}}
	assessment := AssessGrounding(claimContract(), bare, answer)
	found := false
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_implementation_claim" &&
			strings.Contains(strings.ToLower(issue.Detail), "capabilities") {
			found = true
		}
	}
	if !found {
		t.Fatalf("aspect claim without aspect source was accepted: %+v", assessment.Issues)
	}

	implemented := strings.Join([]string{
		"package product",
		"func Create() {}",
		"func Update() {}",
		"func Delete() {}",
		"func main() {",
		"  rdb := redis.NewClient(nil)",
		"  rdb.Get(ctx, key)",
		"  server.Shutdown(ctx)",
		"  slog.Info(\"started\")",
		"}",
	}, "\n")
	withSource := []Turn{{Results: []ToolOutcome{
		scope,
		claimRead("backend/internal/product/main.go", implemented, 20),
	}}}
	assertNoClaimCodes(t, withSource, answer)
}

// A mock or scaffold claim must be visible in the component's source, not
// inferred from the component path.
func TestMockClaimRequiresMockMarkerInSource(t *testing.T) {
	answer := "The frontend uses mock authentication."
	plain := []Turn{{Results: []ToolOutcome{
		claimRead("src/App.tsx", "export default function App() { return null }", 20),
	}}}
	if codes := evidenceCodes(t, plain, answer); !codes["unsupported_implementation_claim"] {
		t.Fatalf("a plain source read supported a mock claim: %v", codes)
	}

	mocked := []Turn{{Results: []ToolOutcome{
		claimRead("src/pages/shop.tsx", "import { mockAuth } from '../data/mockAuth'", 20),
	}}}
	assertNoClaimCodes(t, mocked, answer)
}
