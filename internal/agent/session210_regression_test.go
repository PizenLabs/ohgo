package agent

import (
	"strings"
	"testing"
)

// session210Evidence reproduces the practical session-210 observation shape:
// a truncated structural overview, README/package metadata, repeated reads,
// pruned and investigative globs, service-directory listings, and source
// content for only some components.
func session210Evidence() []Turn {
	return []Turn{{Results: []ToolOutcome{
		{
			ToolName: "structure", CallID: "structure-overview",
			Args: map[string]any{"view": "overview"},
			Result: Result{
				Text: "status: truncated\nview: overview\ngraph_state: CURRENT\n" +
					"scope: PARTIAL\ninventory: COMPLETE\nstructural: UNPARSED\n" +
					"result: TRUNCATED\ntests: 5 (test_files: 5)\n",
				Details: map[string]any{
					"view": "overview", "graph_state": "CURRENT", "complete": false,
					"partial": true, "truncated": true, "scope_complete": false,
					"inventory_complete": true, "structural_complete": false,
					"completeness": "UNPARSED", "test_files": 5, "total": 5,
					"excluded_roots": []map[string]any{
						{"path": "backend/bin", "reason": "repository scope"},
						{"path": "dist", "reason": "repository scope"},
					},
					"nested_projects": []map[string]any{{
						"path": "backend", "included": true,
						"markers": []string{"go.mod", "go.work"},
					}},
					"unsupported_languages": []map[string]any{
						{"extension": ".tsx", "files": 29},
						{"extension": ".js", "files": 4},
					},
				},
			},
		},
		claimRead("README.md", "# E-commerce Microservices Platform", 333),
		claimRead("package.json", `{"dependencies":{"react":"18"}}`, 35),
		// The same documentation observed again is still documentation; the
		// observation count does not upgrade the evidence class.
		claimRead("README.md", "# E-commerce Microservices Platform", 333),
		claimListing(".", "README.md\npackage.json\nbackend/\nsrc/\n"),
		claimListing("backend/cmd",
			"api-gateway/\ncart-service/\ndelivery-service/\nnotification-service/\norder-service/\nproduct-service/\n"),
		claimListing("backend/internal", "cart/\ndelivery/\nnotification/\norder/\nproduct/\n"),
		claimListing("src", "admin/\nclient/\ncomponents/\npages/\n"),
		claimGlobWithDetails("src/**/*.{ts,tsx}",
			"No files matched. 4 paths were excluded by .gitignore.\n",
			map[string]any{"pattern": "src/**/*.{ts,tsx}", "ignored": 4, "excluded": 0, "gitignore": true}),
		claimGlobWithDetails("backend/**/*.go",
			"backend/cmd/product-service/main.go\nbackend/internal/product/service.go\n",
			map[string]any{"pattern": "backend/**/*.go", "ignored": 0, "excluded": 2, "gitignore": false}),
		claimRead("backend/internal/order/service/service.go",
			"package service\nfunc (s *Service) Checkout() error { return nil }", 134),
		claimRead("backend/cmd/product-service/main.go",
			"package main\nfunc main() { http.ListenAndServe(\":8081\", nil) }", 98),
		claimRead("backend/cmd/api-gateway/main.go",
			"package main\nfunc main() { http.HandleFunc(\"/healthz\", health) }", 18),
		claimRead("backend/docker-compose.yml",
			"services:\n  cart-service:\n    ports:\n      - \"8082:8080\"\n  rabbitmq:\n    image: rabbitmq:3\n", 266),
		claimRead("backend/go.mod", "module ecommerce\nrequire github.com/redis/go-redis/v9 v9.5.0", 52),
	}}}
}

// session210Answer is the practical answer shape: implementation status,
// component stubs, mock data, concept absences, test absence, dependency use,
// and a configuration-derived listener claim.
const session210Answer = `Product service is fully implemented.
The inspected order handler implements checkout.
The inspected API gateway entrypoint exposes only its health handlers.
Cart, delivery, and notification are stubs.
The frontend uses mock data and mock authentication.
JWT is not implemented.
RabbitMQ is not implemented.
No Go tests exist.
Product-service uses Redis for caching.
The cart service listens on :8082.
A useful next step would be to add tests and CI.`

// Session-210 regression: directory and glob observations are inventory, not
// source content; component coverage does not leak; test absence is not
// established by pruned or investigative scans; concept negatives stay
// bounded; and every applicable review code survives the bounded reason.
func TestSession210EvidenceShapeAndClaims(t *testing.T) {
	turns := session210Evidence()

	assessment := AssessGrounding(claimContract(), turns, session210Answer)
	if assessment.Pass() {
		t.Fatal("the session-210 answer was accepted")
	}
	codes := map[string]bool{}
	for _, issue := range assessment.Issues {
		codes[issue.Code] = true
	}
	for _, want := range []string{
		"unsupported_completeness_claim",   // product fully implemented without scope
		"unsupported_implementation_claim", // cart/delivery/notification/frontend source missing
		"unsupported_dependency_claim",     // Redis declared, caching not observed
		"unsupported_concept_absence",      // JWT/RabbitMQ
		"unsupported_negative_conclusion",  // no Go tests
	} {
		if !codes[want] {
			t.Fatalf("issues = %+v, want %s", assessment.Issues, want)
		}
	}
	// The bounded human-readable reason must not lose every other finding:
	// the assessment retains every applicable code even when the completion
	// reason is truncated.
	if len(assessment.Issues) < 8 {
		t.Fatalf("issues = %d (%+v), want every applicable finding retained",
			len(assessment.Issues), assessment.Issues)
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"complete implementation", "source observation", "redis"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}

	// Completion cannot call this PROVEN.
	if verdict := claimVerdict(turns, session210Answer); verdict.Complete() {
		t.Fatal("the session-210 answer completed")
	}
}

// A component-specific stub claim needs source from that component. The API
// gateway read must not support cart, delivery, or notification.
func TestSession210StubClaimDoesNotLeakAcrossComponents(t *testing.T) {
	turns := session210Evidence()
	answer := "The inspected API gateway entrypoint exposes only its health handlers; cart is a stub."
	assessment := AssessGrounding(claimContract(), turns, answer)
	found := false
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_implementation_claim" &&
			strings.Contains(strings.ToLower(issue.Detail), "cart") {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want the unobserved cart component", assessment.Issues)
	}
}

// The session-210 evidence shape must not be read as a complete inventory.
// The overview was truncated, scope partial, structural unparsed, and a glob
// overrode .gitignore; none of those can prove repository-wide test absence.
func TestSession210TestAbsenceStaysReview(t *testing.T) {
	turns := session210Evidence()
	for _, answer := range []string{
		"No Go tests exist.",
		"The repository contains no Go test files.",
		"No tests exist in the repository.",
	} {
		assessment := AssessGrounding(claimContract(), turns, answer)
		if assessment.Pass() {
			t.Fatalf("%q was accepted from the session-210 evidence", answer)
		}
	}
}

// The optional recommendation in the practical answer is not an action the
// user requested and must not alter objective grounding.
func TestSession210RecommendationDoesNotBecomeAnAction(t *testing.T) {
	turns := session210Evidence()
	assessment := AssessGrounding(claimContract(), turns,
		"A useful next step would be to add tests and CI.")
	if !assessment.Pass() {
		t.Fatalf("a recommendation was treated as an ungrounded action: %+v", assessment.Issues)
	}
}
