package agent

import (
	"strings"
	"testing"
)

// evidenceCodes returns the review codes and the evidence state that caused
// them. A test that asserts only a code is insufficient: the point is that the
// wrong evidence class cannot reach the claim.
func evidenceCodes(t *testing.T, turns []Turn, answer string) map[string]bool {
	t.Helper()
	assessment := AssessGrounding(claimContract(), turns, answer)
	codes := map[string]bool{}
	for _, issue := range assessment.Issues {
		codes[issue.Code] = true
	}
	return codes
}

// TestPerComponentCoverageMatrix is the admissibility table for implementation
// and mock/stub claims. Each case names the observation and the claim it may
// support.
func TestPerComponentCoverageMatrix(t *testing.T) {
	readme := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "product-service is fully implemented.", 40),
	}}}
	manifest := []Turn{{Results: []ToolOutcome{
		claimRead("backend/go.mod", "module ecommerce", 20),
	}}}
	listing := []Turn{{Results: []ToolOutcome{
		claimListing(".", "backend/\nsrc/\n"),
		claimListing("backend/cmd", "api-gateway/\nproduct-service/\norder-service/\n"),
	}}}
	globResult := []Turn{{Results: []ToolOutcome{
		claimGlob("**/*.go", "backend/internal/product/service.go\n"),
	}}}
	productSource := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/product/service.go",
			"package product\nfunc (s *Service) Create() error { return nil }", 40),
	}}}
	orderSource := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/order/service.go",
			"package order\nfunc (s *Service) Checkout() error { return nil }", 40),
	}}}
	gatewaySource := []Turn{{Results: []ToolOutcome{
		claimRead("backend/cmd/api-gateway/main.go",
			"package main\nfunc main() { http.HandleFunc(\"/healthz\", health) }", 20),
	}}}
	frontendSource := []Turn{{Results: []ToolOutcome{
		claimRead("src/pages/shop.tsx",
			"import { mockProducts } from '../data/mockProducts'", 40),
	}}}
	frontendListing := []Turn{{Results: []ToolOutcome{
		claimListing("src", "src\npages/\ncomponents/\n"),
	}}}
	oneSource := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/product/service.go", "package product", 40),
	}}}
	scopedProduct := []Turn{{Results: []ToolOutcome{
		claimStructureFiles("backend/internal/product", []string{
			"backend/internal/product/service.go",
		}),
		claimRead("backend/internal/product/service.go", "package product", 40),
	}}}

	cases := []struct {
		name string
		turn []Turn
		text string
		want []string
		deny []string
	}{
		{
			name: "README only cannot support full implementation",
			turn: readme, text: "Product-service is fully implemented.",
			want: []string{"unsupported_implementation_claim"},
		},
		{
			name: "manifest only cannot support full implementation",
			turn: manifest, text: "Product-service is fully implemented.",
			want: []string{"unsupported_implementation_claim"},
		},
		{
			name: "directory listing cannot support full implementation",
			turn: listing, text: "Product-service is fully implemented.",
			want: []string{"unsupported_implementation_claim"},
		},
		{
			name: "glob result cannot support full implementation",
			turn: globResult, text: "Product-service is fully implemented.",
			want: []string{"unsupported_implementation_claim"},
		},
		{
			name: "product source supports the observed handler",
			turn: productSource,
			text: "The inspected product handler implements product CRUD endpoints.",
		},
		{
			name: "product source cannot support order",
			turn: productSource, text: "Order service is fully implemented.",
			want: []string{"unsupported_implementation_claim"},
		},
		{
			name: "order source cannot support product",
			turn: orderSource, text: "Product service is fully implemented.",
			want: []string{"unsupported_implementation_claim"},
		},
		{
			name: "gateway source supports its inspected handlers",
			turn: gatewaySource,
			text: "The API gateway exposes only the inspected health handlers.",
		},
		{
			name: "no gateway source cannot support a stub claim",
			turn: readme, text: "The API gateway is a health-check-only stub.",
			want: []string{"unsupported_implementation_claim"},
		},
		{
			name: "frontend source supports an inspected mock-data path",
			turn: frontendSource, text: "The inspected UI path uses mock data.",
		},
		{
			name: "frontend listing cannot support mock data",
			turn: frontendListing, text: "The frontend uses mock data.",
			want: []string{"unsupported_implementation_claim"},
		},
		{
			name: "one source file cannot make a component feature complete",
			turn: oneSource, text: "Product-service is feature-complete.",
			want: []string{"unsupported_completeness_claim"},
		},
		{
			name: "a complete scoped source inventory can",
			turn: scopedProduct, text: "Product-service is feature-complete.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codes := evidenceCodes(t, tc.turn, tc.text)
			for _, code := range tc.want {
				if !codes[code] {
					t.Fatalf("codes = %v, want %s", codes, code)
				}
			}
			for _, code := range tc.deny {
				if codes[code] {
					t.Fatalf("codes = %v, did not want %s", codes, code)
				}
			}
			verdict := claimVerdict(tc.turn, tc.text)
			if len(tc.want) > 0 && verdict.Complete() {
				t.Fatalf("claim completed despite %v: %s", tc.want, verdict.Reason)
			}
			if len(tc.want) == 0 && !verdict.Complete() {
				t.Fatalf("supported claim was reviewed: %s", verdict.Reason)
			}
		})
	}
}

// TestDependencyUseMatrix keeps DECLARED, IMPORTED, INITIALIZED,
// USED_IN_SOURCE, and RUNTIME distinct. Wording strength and component binding
// are both part of the contract.
func TestDependencyUseMatrix(t *testing.T) {
	manifest := []Turn{{Results: []ToolOutcome{
		claimRead("backend/go.mod", "module ecommerce\nrequire github.com/redis/go-redis/v9 v9.5.0", 30),
	}}}
	readme := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "The project uses Redis for caching.", 40),
	}}}
	importOnly := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/product/repository/cache.go",
			"package repository\nimport \"github.com/redis/go-redis/v9\"", 30),
	}}}
	initialized := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/product/repository/cache.go",
			"package repository\nimport \"github.com/redis/go-redis/v9\"\nfunc New() { redis.NewClient(&redis.Options{}) }", 30),
	}}}
	used := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/product/repository/cache.go",
			"package repository\nimport \"github.com/redis/go-redis/v9\"\nfunc New() { rdb := redis.NewClient(&redis.Options{}) }\nfunc Get(k string) { rdb.Get(ctx, k) }", 30),
	}}}
	orderUsed := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/order/repository/cache.go",
			"package repository\nimport \"github.com/redis/go-redis/v9\"\nfunc New() { rdb := redis.NewClient(&redis.Options{}) }\nfunc Get(k string) { rdb.Get(ctx, k) }", 30),
	}}}
	runtime := []Turn{{Results: []ToolOutcome{{
		ToolName: "verify",
		Args:     map[string]any{"service": map[string]any{"command": "go run ./backend/cmd/product-service"}},
		Result: Result{
			Text: "redis ping ok",
			Runtime: &RuntimeEvidence{
				Command: "go run ./backend/cmd/product-service",
				URL:     "http://localhost:8081",
				Checks:  1,
			},
		},
	}}}}

	cases := []struct {
		name string
		turn []Turn
		text string
		want string
	}{
		{
			name: "declared claim with manifest",
			turn: manifest, text: "The project declares Redis as a dependency.",
		},
		{
			name: "use claim with only a manifest",
			turn: manifest, text: "The project uses Redis.",
			want: "unsupported_dependency_claim",
		},
		{
			name: "README usage is not dependency evidence",
			turn: readme, text: "The project uses Redis.",
			want: "unsupported_dependency_claim",
		},
		{
			name: "import claim with import evidence",
			turn: importOnly, text: "The product repository imports Redis.",
		},
		{
			name: "use claim with only an import",
			turn: importOnly, text: "The product service uses Redis.",
			want: "unsupported_dependency_claim",
		},
		{
			name: "use claim with only an initialized client",
			turn: initialized, text: "Product-service uses Redis for caching.",
			want: "unsupported_dependency_claim",
		},
		{
			name: "use claim with a concrete operation",
			turn: used, text: "Product-service uses Redis for caching.",
		},
		{
			name: "dependency evidence does not leak to another component",
			turn: orderUsed, text: "Product-service uses Redis for caching.",
			want: "unsupported_dependency_claim",
		},
		{
			name: "runtime observation supports use",
			turn: runtime, text: "Product-service uses Redis.",
		},
		{
			name: "one missing match is not concept absence",
			turn: manifest, text: "RabbitMQ is not implemented.",
			want: "unsupported_concept_absence",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codes := evidenceCodes(t, tc.turn, tc.text)
			if tc.want == "" {
				for code := range codes {
					if strings.HasPrefix(code, "unsupported_") {
						t.Fatalf("codes = %v, want no unsupported claim", codes)
					}
				}
				return
			}
			if !codes[tc.want] {
				t.Fatalf("codes = %v, want %s", codes, tc.want)
			}
		})
	}
}

// claimGlobWithDetails builds a glob observation with the scanner's
// completeness facts.
func claimGlobWithDetails(pattern, text string, details map[string]any) ToolOutcome {
	return ToolOutcome{
		ToolName: "glob",
		Args:     map[string]any{"pattern": pattern},
		Result:   Result{Text: text, Details: details},
	}
}

// A test-absence claim needs a complete current inventory. An investigative
// gitignore override and a scan that pruned built-in directories are not
// complete project inventories, even when they found no test file.
func TestTestAbsenceRejectsInvestigativeScans(t *testing.T) {
	complete := []Turn{{Results: []ToolOutcome{
		claimGlobWithDetails("**/*.go", "backend/cmd/main.go\n", map[string]any{
			"pattern": "**/*.go", "matches": 1, "ignored": 0, "excluded": 0,
			"gitignore": true,
		}),
	}}}
	if verdict := claimVerdict(complete, "No Go test files are present."); !verdict.Complete() {
		t.Fatalf("a complete unpruned scan did not prove the narrowed absence: %s", verdict.Reason)
	}

	override := []Turn{{Results: []ToolOutcome{
		claimGlobWithDetails("**/*.go", "backend/cmd/main.go\n", map[string]any{
			"pattern": "**/*.go", "matches": 1, "ignored": 0, "excluded": 0,
			"gitignore": false,
		}),
	}}}
	if verdict := claimVerdict(override, "No Go test files are present."); verdict.Complete() {
		t.Fatal("a gitignore=false investigative scan proved repository-wide absence")
	}

	pruned := []Turn{{Results: []ToolOutcome{
		claimGlobWithDetails("**/*.go", "backend/cmd/main.go\n", map[string]any{
			"pattern": "**/*.go", "matches": 1, "ignored": 0, "excluded": 2,
			"gitignore": true,
		}),
	}}}
	if verdict := claimVerdict(pruned, "No Go test files are present."); verdict.Complete() {
		t.Fatal("a scan that pruned built-in directories proved repository-wide absence")
	}
}

// A structural test inventory needs inventory completeness in addition to
// scope and structural completeness. A complete-looking test count from a
// partial file inventory is not an absence proof.
func TestStructuralTestAbsenceNeedsEveryAxis(t *testing.T) {
	incomplete := ToolOutcome{
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: Result{
			Text: "view: tests\nreturned: 0\ntotal: 0\n",
			Details: map[string]any{
				"view": "tests", "graph_state": "CURRENT", "complete": true,
				"partial": false, "truncated": false, "scope_complete": true,
				"inventory_complete": false, "structural_complete": true,
				"completeness": "COMPLETE", "test_files": 0, "total": 0,
			},
		},
	}
	if verdict := claimVerdict([]Turn{{Results: []ToolOutcome{incomplete}}},
		"No Go test files are present."); verdict.Complete() {
		t.Fatal("an incomplete file inventory proved test absence")
	}
}

// A partial scope cannot prove repository-wide absence even when the filtered
// inventory reports zero tests. A complete current inventory can support the
// exact bounded claim it observed.
func TestPartialScopeCannotProveRepositoryAbsence(t *testing.T) {
	partial := ToolOutcome{
		ToolName: "structure", Args: map[string]any{"view": "tests"},
		Result: Result{
			Text: "view: tests\nscope: PARTIAL\nreturned: 0\ntotal: 0\n",
			Details: map[string]any{
				"view": "tests", "graph_state": "CURRENT", "complete": false,
				"partial": true, "truncated": false, "scope_complete": false,
				"inventory_complete": true, "structural_complete": true,
				"completeness": "COMPLETE", "test_files": 0, "total": 0,
				"excluded_roots": []map[string]any{{"path": "vendor", "reason": "repository scope"}},
			},
		},
	}
	if verdict := claimVerdict([]Turn{{Results: []ToolOutcome{partial}}},
		"No Go test files are present."); verdict.Complete() {
		t.Fatal("a partial scope proved repository-wide test absence")
	}

	complete := ToolOutcome{
		ToolName: "structure", Args: map[string]any{"view": "tests"},
		Result: Result{
			Text: "view: tests\nscope: COMPLETE\nreturned: 0\ntotal: 0\n",
			Details: map[string]any{
				"view": "tests", "graph_state": "CURRENT", "complete": true,
				"partial": false, "truncated": false, "scope_complete": true,
				"inventory_complete": true, "structural_complete": true,
				"completeness": "COMPLETE", "test_files": 0, "total": 0,
				"nested_projects": []map[string]any{{
					"path": "backend", "included": true, "markers": []string{"go.mod"},
				}},
			},
		},
	}
	if verdict := claimVerdict([]Turn{{Results: []ToolOutcome{complete}}},
		"No Go test files are present."); !verdict.Complete() {
		t.Fatalf("a complete current test inventory did not support its exact absence: %s", verdict.Reason)
	}
}

// A nested project with a test file remains visible. The inventory cannot
// report zero tests when the nested repository contributed one.
func TestNestedProjectTestsRemainVisible(t *testing.T) {
	outcome := ToolOutcome{
		ToolName: "structure", Args: map[string]any{"view": "tests"},
		Result: Result{
			Text: "view: tests\nreturned: 1\ntotal: 1\n",
			Details: map[string]any{
				"view": "tests", "graph_state": "CURRENT", "complete": true,
				"partial": false, "truncated": false, "scope_complete": true,
				"inventory_complete": true, "structural_complete": true,
				"completeness": "COMPLETE", "test_files": 1, "total": 1,
				"nested_projects": []map[string]any{{
					"path": "backend", "included": true, "markers": []string{"go.mod"},
				}},
			},
		},
	}
	if verdict := claimVerdict([]Turn{{Results: []ToolOutcome{outcome}}},
		"No Go test files are present."); verdict.Complete() {
		t.Fatal("nested repository test files were hidden from the absence gate")
	}
}

// Configuration declares topology. It does not prove that a process listens.
func TestConfigurationTopologyAndRuntimeBehaviorStaySeparate(t *testing.T) {
	compose := []Turn{{Results: []ToolOutcome{
		claimRead("backend/docker-compose.yml",
			"services:\n  cart-service:\n    ports:\n      - \"8082:8080\"\n", 30),
	}}}
	if codes := evidenceCodes(t, compose,
		"Compose maps cart-service to host port 8082."); len(codes) > 0 {
		t.Fatalf("a configuration mapping claim was reviewed: %v", codes)
	}
	if codes := evidenceCodes(t, compose,
		"The cart service listens on :8082."); !codes["unsupported_implementation_claim"] {
		t.Fatalf("configuration was accepted as runtime behavior: %v", codes)
	}
}

// An explicitly attributed documentation statement is a documentation claim,
// not an observed dependency or implementation fact.
func TestAttributedDocumentationClaimRemainsDocumentation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "The project uses Redis; product-service is fully implemented.", 40),
	}}}
	for _, answer := range []string{
		"According to the README, the project uses Redis.",
		"The project documentation states that product-service is fully implemented.",
	} {
		if codes := evidenceCodes(t, turns, answer); len(codes) > 0 {
			t.Fatalf("%q produced %v, want a documentation claim", answer, codes)
		}
	}
}

// Bounded concept negatives and recommendations are admissible. An unbounded
// one is not.
func TestConceptAbsenceStaysBoundedOrReviewed(t *testing.T) {
	manifest := []Turn{{Results: []ToolOutcome{
		claimRead("backend/go.mod", "module ecommerce", 20),
	}}}
	bounded := []string{
		"No JWT implementation was found in the inspected authentication source.",
		"The inspected source did not show RabbitMQ operations.",
		"I could not verify database integration from the inspected files.",
	}
	for _, answer := range bounded {
		if codes := evidenceCodes(t, manifest, answer); len(codes) > 0 {
			t.Fatalf("%q produced %v, want a bounded statement", answer, codes)
		}
	}
	unbounded := []string{
		"JWT is not implemented.",
		"RabbitMQ is not implemented.",
		"The application has no database integration.",
		"The project does not use Redis.",
		"Authentication is unused.",
		"No authentication.",
	}
	for _, answer := range unbounded {
		if codes := evidenceCodes(t, manifest, answer); !codes["unsupported_concept_absence"] {
			t.Fatalf("%q codes = %v, want unsupported_concept_absence", answer, codes)
		}
	}
}

// Questions, recommendations, and hypotheticals are not factual claims. They
// must not become a review boundary merely because they mention a status word.
func TestNonAssertiveLanguageIsNotAClaim(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "project", 10),
	}}}
	for _, answer := range []string{
		"Is product-service fully implemented?",
		"You could add JWT authentication; it is not implemented.",
		"Consider adding a database integration.",
		"If the service is fully implemented, it still needs production hardening.",
	} {
		if codes := evidenceCodes(t, turns, answer); len(codes) > 0 {
			t.Fatalf("%q produced %v, want non-assertive language ignored", answer, codes)
		}
	}
}

// A semicolon or contrast can join a supported claim and an unsupported one.
// Both halves must be audited, and the unsupported one must survive the
// bounded review reason.
func TestMixedClaimSentenceRetainsEveryCode(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "product-service is fully implemented", 40),
		claimRead("backend/internal/product/service.go", "package product", 40),
	}}}
	answer := "The inspected product handler implements CRUD; JWT is not implemented."
	assessment := AssessGrounding(claimContract(), turns, answer)
	codes := map[string]bool{}
	for _, issue := range assessment.Issues {
		codes[issue.Code] = true
	}
	if !codes["unsupported_concept_absence"] {
		t.Fatalf("issues = %+v, want the unsupported half retained", assessment.Issues)
	}
	if verdict := claimVerdict(turns, answer); verdict.Complete() {
		t.Fatal("a mixed answer with an unsupported half completed")
	}
}
