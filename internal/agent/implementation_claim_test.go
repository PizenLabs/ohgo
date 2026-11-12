package agent

import (
	"strings"
	"testing"
)

// Session 206: a broad project analysis read documentation, a manifest, a
// Compose file, directory listings, and a metadata overview, then declared
// components "fully implemented" and others "stubs". A declaration or an
// inventory does not establish implementation state.
func TestImplementationStatusClaimRequiresSourceObservation(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "README.md\npackage.json\nbackend\nsrc\n"),
		claimRead("README.md", "# E-commerce Microservices Platform", 333),
		claimRead("package.json", `{"dependencies":{"react":"18"}}`, 35),
		claimRead("backend/docker-compose.yml",
			"services:\n  product-service:\n    ports:\n      - \"8081:8080\"\n  order-service:\n    depends_on:\n      - product-service\n", 266),
		claimStructureOverview(0, false, true),
	}}}
	answer := strings.Join([]string{
		"This repository is an e-commerce microservices platform with a Go backend and a React/TypeScript frontend.",
		"Fully implemented: product-service (REST CRUD for products/categories, Redis caching, GORM/Postgres), order-service (checkout with server-computed totals, idempotency, status updates), and the frontend UI (storefront /shop, admin /admin with mock auth).",
		"Stubs (health checks only): API-gateway, cart, delivery, notification services.",
	}, "\n\n")

	assessment := AssessGrounding(claimContract(), turns, answer)
	if assessment.Pass() {
		t.Fatal("implementation status claims without source observations were accepted")
	}
	found := false
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_implementation_claim" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want unsupported_implementation_claim", assessment.Issues)
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"fully implemented", "product-service", "order-service"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}

	if verdict := claimVerdict(turns, answer); verdict.Complete() {
		t.Fatal("the source-free implementation analysis completed")
	}
}

// A source read proves implementation content for the component it touched,
// not for a different component in the same sentence.
func TestImplementationStatusClaimRequiresSourceForEachComponent(t *testing.T) {
	product := claimRead("backend/internal/product/service.go",
		"package product\nfunc (s *Service) Create() error { return nil }", 40)
	order := claimRead("backend/internal/order/service.go",
		"package order\nfunc (s *Service) Checkout() error { return nil }", 40)
	frontend := claimRead("src/App.tsx", "export default function App() { return null }", 40)

	full := []Turn{{Results: []ToolOutcome{product, order, frontend}}}
	answer := "Fully implemented: product-service, order-service, and the frontend UI."
	if verdict := claimVerdict(full, answer); !verdict.Complete() {
		t.Fatalf("an implementation claim with source for every component was reviewed: %s", verdict.Reason)
	}

	missingOrder := []Turn{{Results: []ToolOutcome{product, frontend}}}
	verdict := claimVerdict(missingOrder, answer)
	if verdict.Complete() {
		t.Fatal("a product and frontend read supported a claim about order-service")
	}
	if !strings.Contains(strings.ToLower(verdict.Reason), "order-service") {
		t.Fatalf("reason = %q, want the unobserved component", verdict.Reason)
	}

	missingFrontend := []Turn{{Results: []ToolOutcome{product, order}}}
	verdict = claimVerdict(missingFrontend, answer)
	if verdict.Complete() {
		t.Fatal("backend reads supported a claim about the frontend UI")
	}
	if !strings.Contains(strings.ToLower(verdict.Reason), "frontend") {
		t.Fatalf("reason = %q, want the unobserved frontend", verdict.Reason)
	}
}

// A claim explicitly attributed to the README is a documentation claim, not an
// implementation observation.
func TestAttributedImplementationStatusClaimCompletes(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "product-service is fully implemented; the rest are stubs", 333),
	}}}
	answer := "According to the README, product-service is fully implemented and the remaining services are stubs."
	if verdict := claimVerdict(turns, answer); !verdict.Complete() {
		t.Fatalf("an attributed documentation claim was reviewed: %s", verdict.Reason)
	}
}

// A source observation for one stub component does not prove the whole stub
// list. Each named component must be covered by source evidence.
func TestStubClaimRequiresSourceForEachComponent(t *testing.T) {
	gateway := claimRead("backend/cmd/api-gateway/main.go", "package main\nfunc main() {}", 20)
	cart := claimRead("backend/internal/cart/service.go", "package cart\nfunc (s *Service) Health() {}", 20)
	delivery := claimRead("backend/internal/delivery/service.go", "package delivery\nfunc (s *Service) Health() {}", 20)
	notification := claimRead("backend/internal/notification/service.go", "package notification\nfunc (s *Service) Health() {}", 20)
	answer := "Stubs (health checks only): API-gateway, cart, delivery, notification services."

	all := []Turn{{Results: []ToolOutcome{gateway, cart, delivery, notification}}}
	if verdict := claimVerdict(all, answer); !verdict.Complete() {
		t.Fatalf("a stub claim with source for every component was reviewed: %s", verdict.Reason)
	}

	partial := []Turn{{Results: []ToolOutcome{gateway, cart}}}
	verdict := claimVerdict(partial, answer)
	if verdict.Complete() {
		t.Fatal("two source reads supported a stub claim about four components")
	}
	for _, want := range []string{"delivery", "notification"} {
		if !strings.Contains(strings.ToLower(verdict.Reason), want) {
			t.Fatalf("reason = %q, want %q", verdict.Reason, want)
		}
	}
}

// A status claim with no extractable component is still an implementation
// claim: documentation, manifests, and inventory cannot support it.
func TestImplementationStatusClaimWithoutComponentNeedsSomeSource(t *testing.T) {
	docs := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "The project is production-ready and fully implemented.", 100),
		claimRead("backend/go.mod", "module ecommerce", 20),
	}}}
	answer := "The implementation is fully implemented and production-ready."
	if verdict := claimVerdict(docs, answer); verdict.Complete() {
		t.Fatal("a source-free implementation status claim completed")
	}

	withSource := []Turn{{Results: []ToolOutcome{
		claimRead("backend/main.go", "package main\nfunc main() {}", 20),
	}}}
	if verdict := claimVerdict(withSource, answer); !verdict.Complete() {
		t.Fatalf("a source-backed implementation status claim was reviewed: %s", verdict.Reason)
	}
}

// The practical session mixed an unsupported implementation claim with a
// global test absence. Both must survive the bounded review reason; the
// implementation claim must not be hidden by the absence issue.
func TestImplementationAndAbsenceClaimsAreBothReviewed(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# E-commerce Microservices Platform", 333),
		claimStructureOverview(5, false, true),
	}}}
	answer := "Fully implemented: product-service, order-service, and the frontend UI.\n\nNo tests exist."
	assessment := AssessGrounding(claimContract(), turns, answer)
	if assessment.Pass() {
		t.Fatal("the implementation and absence claims were accepted")
	}
	codes := map[string]bool{}
	for _, issue := range assessment.Issues {
		codes[issue.Code] = true
	}
	for _, want := range []string{"unsupported_implementation_claim", "unsupported_negative_conclusion"} {
		if !codes[want] {
			t.Fatalf("issues = %+v, want %s", assessment.Issues, want)
		}
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"fully implemented", "tests"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}
}
