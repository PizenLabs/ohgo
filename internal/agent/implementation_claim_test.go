package agent

import (
	"strconv"
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
	productScope := claimStructureFiles("backend/internal/product", []string{
		"backend/internal/product/service.go",
	})
	orderScope := claimStructureFiles("backend/internal/order", []string{
		"backend/internal/order/service.go",
	})
	frontendScope := claimStructureFiles("src", []string{"src/App.tsx"})

	full := []Turn{{Results: []ToolOutcome{
		productScope, product, orderScope, order, frontendScope, frontend,
	}}}
	answer := "Fully implemented: product-service, order-service, and the frontend UI."
	if verdict := claimVerdict(full, answer); !verdict.Complete() {
		t.Fatalf("an implementation claim with source and scope for every component was reviewed: %s", verdict.Reason)
	}

	missingOrder := []Turn{{Results: []ToolOutcome{
		productScope, product, orderScope, frontendScope, frontend,
	}}}
	verdict := claimVerdict(missingOrder, answer)
	if verdict.Complete() {
		t.Fatal("a product and frontend read supported a claim about order-service")
	}
	if !strings.Contains(strings.ToLower(verdict.Reason), "order-service") {
		t.Fatalf("reason = %q, want the unobserved component", verdict.Reason)
	}

	missingFrontend := []Turn{{Results: []ToolOutcome{
		productScope, product, orderScope, order,
	}}}
	verdict = claimVerdict(missingFrontend, answer)
	if verdict.Complete() {
		t.Fatal("backend reads supported a claim about the frontend UI")
	}
	if !strings.Contains(strings.ToLower(verdict.Reason), "frontend") {
		t.Fatalf("reason = %q, want the unobserved frontend", verdict.Reason)
	}
}

// claimStructureFiles builds a scoped, complete source inventory. Its files
// are the component's source scope: a strong completeness claim may use it
// only after every listed file is read.
func claimStructureFiles(path string, files []string) ToolOutcome {
	rows := make([]map[string]any, 0, len(files))
	for _, file := range files {
		rows = append(rows, map[string]any{
			"path": file, "test": false, "unsupported": false, "kind": "source",
		})
	}
	return ToolOutcome{
		ToolName: "structure",
		Args:     map[string]any{"view": "files", "path": path},
		Result: Result{
			Text: "view: files\ntotal: " + strconv.Itoa(len(files)) + "\n",
			Details: map[string]any{
				"view":                "files",
				"path":                path,
				"graph_state":         "CURRENT",
				"complete":            true,
				"partial":             false,
				"truncated":           false,
				"scope_complete":      true,
				"inventory_complete":  true,
				"structural_complete": true,
				"total":               len(files),
				"returned":            len(files),
				"files":               rows,
			},
		},
	}
}

// One source file is an observation of one file, not a complete component.
// Only an observed component source scope can support whole-component
// completeness wording.
func TestFullImplementationNeedsComponentSourceScope(t *testing.T) {
	answer := "Fully implemented: product-service, order-service, and the frontend UI."
	isolated := []Turn{{Results: []ToolOutcome{
		claimRead("backend/internal/product/service.go", "package product", 40),
		claimRead("backend/internal/order/service.go", "package order", 40),
		claimRead("src/App.tsx", "export default function App() { return null }", 40),
	}}}
	verdict := claimVerdict(isolated, answer)
	if verdict.Complete() {
		t.Fatal("three isolated source reads were accepted as whole-component completeness")
	}
	assessment := AssessGrounding(claimContract(), isolated, answer)
	found := false
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_completeness_claim" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want unsupported_completeness_claim", assessment.Issues)
	}

	// The same answer with a scoped inventory and every file read is the
	// evidence class that can support it.
	scoped := []Turn{{Results: []ToolOutcome{
		claimStructureFiles("backend/internal/product", []string{"backend/internal/product/service.go"}),
		claimRead("backend/internal/product/service.go", "package product", 40),
		claimStructureFiles("backend/internal/order", []string{"backend/internal/order/service.go"}),
		claimRead("backend/internal/order/service.go", "package order", 40),
		claimStructureFiles("src", []string{"src/App.tsx"}),
		claimRead("src/App.tsx", "export default function App() { return null }", 40),
	}}}
	if verdict := claimVerdict(scoped, answer); !verdict.Complete() {
		t.Fatalf("a fully scoped component claim was reviewed: %s", verdict.Reason)
	}

	// A partial read of the scoped inventory is not full coverage.
	partial := []Turn{{Results: []ToolOutcome{
		claimStructureFiles("backend/internal/product", []string{
			"backend/internal/product/service.go",
			"backend/internal/product/handler.go",
		}),
		claimRead("backend/internal/product/service.go", "package product", 40),
		claimStructureFiles("backend/internal/order", []string{"backend/internal/order/service.go"}),
		claimRead("backend/internal/order/service.go", "package order", 40),
		claimStructureFiles("src", []string{"src/App.tsx"}),
		claimRead("src/App.tsx", "export default function App() { return null }", 40),
	}}}
	if verdict := claimVerdict(partial, "Fully implemented: product-service."); verdict.Complete() {
		t.Fatal("a partially read component inventory supported full implementation")
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
// claim: documentation, manifests, and inventory cannot support it, and one
// source file cannot establish whole-project completeness. A narrower claim
// about the inspected file is the admissible form.
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
	if verdict := claimVerdict(withSource, answer); verdict.Complete() {
		t.Fatal("one source file supported a whole-project completeness claim")
	}

	narrowed := "The inspected backend/main.go implements an HTTP entrypoint."
	if verdict := claimVerdict(withSource, narrowed); !verdict.Complete() {
		t.Fatalf("a claim narrowed to the inspected file was reviewed: %s", verdict.Reason)
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

// The plain verb form is a status claim too. "order-service also implements a
// checkout API (… idempotent order creation …)" attaches a capability to a
// component, so the capability itself needs source evidence: reading only the
// entrypoint proves the component exists, not that checkout is idempotent.
func TestImplementVerbClaimRequiresEachCapability(t *testing.T) {
	entrypoint := claimRead("backend/cmd/order-service/main.go",
		"package main\n\nfunc main() {\n\tdb.AutoMigrate(&domain.Order{})\n}", 71)
	answer := "order-service also implements a checkout API " +
		"(server-computed totals, idempotent order creation, status updates, AutoMigrate)."

	assessment := AssessGrounding(claimContract(), []Turn{{Results: []ToolOutcome{entrypoint}}}, answer)
	if assessment.Pass() {
		t.Fatal("an implementation claim with an unobserved capability was accepted")
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"implements a checkout api", "order-service", "idempotency"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}

	// The same claim is admissible once the observed source for that component
	// shows the capability. Review is bounded by evidence, not by vocabulary.
	service := claimRead("backend/internal/order/service/service.go",
		"package service\n\n// Create is idempotent on the caller's key.\nfunc (s *Service) Create() error { return nil }", 120)
	supported := []Turn{{Results: []ToolOutcome{entrypoint, service}}}
	if verdict := claimVerdict(supported, answer); !verdict.Complete() {
		t.Fatalf("a capability observed in the component source was reviewed: %s", verdict.Reason)
	}
}

// "Auth is mock" is a status claim even though it carries no completion
// adjective and no "mock data" noun. The component path is not the evidence:
// the observed source has to show a mock.
func TestCopulaMockClaimRequiresAMockMarker(t *testing.T) {
	answer := "Auth is mock (localStorage, two hardcoded demo accounts)."
	real := claimRead("backend/internal/auth/service.go",
		"package auth\n\nfunc Verify(token string) error { return nil }", 60)
	assessment := AssessGrounding(claimContract(), []Turn{{Results: []ToolOutcome{real}}}, answer)
	if assessment.Pass() {
		t.Fatal("a mock claim whose source shows no mock was accepted")
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"mock", "auth"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}

	mock := claimRead("backend/internal/auth/mock.go",
		"package auth\n\n// mock verifier used by the demo build.\nfunc Verify(token string) error { return nil }", 30)
	supported := []Turn{{Results: []ToolOutcome{mock}}}
	if verdict := claimVerdict(supported, answer); !verdict.Complete() {
		t.Fatalf("a mock marker in the component source was reviewed: %s", verdict.Reason)
	}
}

// A feature phrase belongs to the surface that owns it. "Frontend auth is mock
// only" is a claim about the frontend; naming "auth" would send the model to a
// component no path can match, because a file under src/ derives to frontend.
func TestFrontendSurfaceOwnsAnAuthClaim(t *testing.T) {
	answer := "Frontend auth is mock only (demo accounts: admin@ecommerce.com)."
	assessment := AssessGrounding(claimContract(), nil, answer)
	if assessment.Pass() {
		t.Fatal("a mock claim with no observed frontend source was accepted")
	}
	reason := strings.ToLower(assessment.Reason())
	if !strings.Contains(reason, "frontend") {
		t.Fatalf("reason = %q, want the owning surface", assessment.Reason())
	}

	auth := claimRead("src/contexts/AuthContext.tsx",
		"const user = localStorage.getItem('user'); // mock auth for the demo", 80)
	supported := []Turn{{Results: []ToolOutcome{auth}}}
	if verdict := claimVerdict(supported, answer); !verdict.Complete() {
		t.Fatalf("frontend source with a mock marker was reviewed: %s", verdict.Reason)
	}
}

// A colon list followed by an em-dash explanation is still a list of
// components. "Stubs (health-check only): api-gateway, cart-service, … — plain
// net/http" asserts a status for every name, so every service the run did not
// read is a review.
func TestStubListNamesEveryUnreadService(t *testing.T) {
	answer := "Stubs (health-check only): api-gateway, cart-service, delivery-service, " +
		"notification-service — plain net/http /health + /ready, no business logic."
	gateway := claimRead("backend/cmd/api-gateway/main.go", "package main\n\nfunc main() {}\n", 16)
	assessment := AssessGrounding(claimContract(), []Turn{{Results: []ToolOutcome{gateway}}}, answer)
	if assessment.Pass() {
		t.Fatal("a stub claim for unread services was accepted")
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"cart-service", "delivery-service", "notification-service"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}
	if strings.Contains(reason, "api-gateway") {
		t.Fatalf("reason = %q, want only the unread services", assessment.Reason())
	}

	all := []Turn{{Results: []ToolOutcome{
		gateway,
		claimRead("backend/cmd/cart-service/main.go", "package main\n\nfunc main() {}\n", 12),
		claimRead("backend/cmd/delivery-service/main.go", "package main\n\nfunc main() {}\n", 12),
		claimRead("backend/cmd/notification-service/main.go", "package main\n\nfunc main() {}\n", 12),
	}}}
	if verdict := claimVerdict(all, answer); !verdict.Complete() {
		t.Fatalf("a stub claim with source for every service was reviewed: %s", verdict.Reason)
	}
}

// "Not production-ready" asserts the opposite of a completion claim. The
// status vocabulary must not read the negated adjective as a claim.
func TestNegatedStatusIsNotACompletionClaim(t *testing.T) {
	for _, answer := range []string{
		"Not production-ready; messaging, auth, and API integration remain roadmap items.",
		"The frontend is not feature-complete.",
		"It is not fully implemented.",
	} {
		if assessment := AssessGrounding(claimContract(), nil, answer); !assessment.Pass() {
			t.Fatalf("%q was reviewed as a completion claim: %s", answer, assessment.Reason())
		}
	}
	// Without the negation the same adjective is still a claim.
	if assessment := AssessGrounding(claimContract(), nil, "Production-ready service."); assessment.Pass() {
		t.Fatal("an unnegated completeness claim was accepted")
	}
}

// An adjective-first status names its subject after the marker. Reviewing
// "Feature-complete UI" as an unnamed component asked for information the
// sentence already provided.
func TestAdjectiveFirstStatusNamesItsComponent(t *testing.T) {
	answer := "Feature-complete UI with mock data; not yet wired to backend APIs."
	assessment := AssessGrounding(claimContract(), nil, answer)
	if assessment.Pass() {
		t.Fatal("a completeness claim with no frontend source was accepted")
	}
	reason := strings.ToLower(assessment.Reason())
	if !strings.Contains(reason, "frontend") {
		t.Fatalf("reason = %q, want the component the sentence names", assessment.Reason())
	}
	if strings.Contains(reason, "without naming the component") {
		t.Fatalf("reason = %q, want the named component", assessment.Reason())
	}
}

// "Stubs: api-gateway, cart-service, …" is a status list: every service the run
// did not read is a review, and reading them all clears it.
func TestStubsColonListNamesEveryUnreadService(t *testing.T) {
	answer := "Stubs: api-gateway, cart-service, delivery-service, notification-service " +
		"(only /health and /ready endpoints)."
	gateway := claimRead("backend/cmd/api-gateway/main.go", stubServiceSource, 16)
	assessment := AssessGrounding(claimContract(), []Turn{{Results: []ToolOutcome{gateway}}}, answer)
	if assessment.Pass() {
		t.Fatal("a stub claim for unread services was accepted")
	}
	reason := strings.ToLower(assessment.Reason())
	for _, want := range []string{"cart-service", "delivery-service", "notification-service"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", assessment.Reason(), want)
		}
	}

	all := []Turn{{Results: []ToolOutcome{
		gateway,
		claimRead("backend/cmd/cart-service/main.go", stubServiceSource, 12),
		claimRead("backend/cmd/delivery-service/main.go", stubServiceSource, 12),
		claimRead("backend/cmd/notification-service/main.go", stubServiceSource, 12),
	}}}
	if verdict := claimVerdict(all, answer); !verdict.Complete() {
		t.Fatalf("a stub claim with source for every service was reviewed: %s", verdict.Reason)
	}
}

// stubServiceSource is a health-check-only service: the stub claim's capability
// list ("only /health and /ready endpoints") is what the source shows.
const stubServiceSource = "package main\n\nfunc main() {\n\thttp.HandleFunc(\"/health\", ok)\n\thttp.HandleFunc(\"/ready\", ok)\n}\n"
