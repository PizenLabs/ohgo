package agent

import (
	"strings"
	"testing"
)

// Component identity is derived from the project structure, not from an
// arbitrary path substring. A file called api.go inside cart/ is cart, and the
// enclosing user directory is never a component.
func TestComponentFromPathIsStructural(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"backend/internal/product/handler/http.go", "product"},
		{"backend/internal/product/service/service.go", "product"},
		{"backend/internal/product/order.go", "product"},
		{"backend/internal/order/domain/order.go", "order"},
		{"backend/cmd/api-gateway/main.go", "api-gateway"},
		{"backend/internal/cart/api.go", "cart"},
		{"src/pages/shop.tsx", "frontend"},
		{"src/client/components/Checkout.tsx", "frontend"},
		{"/Users/anvndev/projects/shop/backend/internal/order/service/service.go", "order"},
		{"/Users/anvndev/projects/shop/src/App.tsx", "frontend"},
		{"go.mod", ""},
		{"README.md", ""},
		{"backend/docker-compose.yml", "backend"},
	}
	for _, tc := range cases {
		if got := ComponentFromPath(tc.path); got != tc.want {
			t.Errorf("ComponentFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// A component claim matches only its own component. Comparable-looking names
// in a path are not enough: the path's derived component owns the evidence.
func TestComponentMatchesPathDoesNotLeak(t *testing.T) {
	cases := []struct {
		component string
		path      string
		want      bool
	}{
		{"product", "backend/internal/product/handler/http.go", true},
		{"product-service", "backend/cmd/product-service/main.go", true},
		{"gateway", "backend/cmd/api-gateway/main.go", true},
		{"api-gateway", "backend/cmd/api-gateway/main.go", true},
		{"frontend", "src/App.tsx", true},
		{"client", "src/client/ClientApp.tsx", true},
		{"order", "backend/internal/product/order.go", false},
		{"order", "backend/internal/product/handler/http.go", false},
		{"product", "backend/internal/order/service/service.go", false},
		{"api", "backend/internal/cart/api.go", false},
		{"cart", "backend/cmd/product-service/main.go", false},
	}
	for _, tc := range cases {
		if got := ComponentMatchesPath(tc.component, tc.path); got != tc.want {
			t.Errorf("ComponentMatchesPath(%q, %q) = %v, want %v",
				tc.component, tc.path, got, tc.want)
		}
	}
}

// A runtime command is a component coordinate when it names one. An address
// that names another service cannot be reused for this component's claim.
func TestComponentMatchesTextRequiresAnExplicitHint(t *testing.T) {
	if !ComponentMatchesText("cart", "go run ./backend/cmd/cart-service") {
		t.Fatal("cart command did not match cart")
	}
	if ComponentMatchesText("cart", "go run ./backend/cmd/product-service") {
		t.Fatal("product command matched cart")
	}
	if ComponentMatchesText("cart", "") {
		t.Fatal("an empty hint matched a component")
	}
}

// Session 310: "It is an in-progress scaffold: the product-service is fully
// implemented, the order-service now implements checkout, ..." produced the
// review subjects "checkout" and "in-progress". A status word followed by an
// explanation introduces prose, not a component list, and only the text before
// the status word names its subject.
func TestRepositoryScaffoldClaimDoesNotFabricateComponents(t *testing.T) {
	sentence := "It is an in-progress scaffold: the product-service is fully " +
		"implemented, the order-service now implements checkout, and the other " +
		"four backend services are health-check stubs."
	match := mockStatusPattern.FindStringIndex(sentence)
	if match == nil {
		t.Fatal("fixture no longer matches the mock/scaffold audit")
	}
	subjects := implementationClaimSubjects(sentence, match)
	if len(subjects) != 0 {
		var names []string
		for _, subject := range subjects {
			names = append(names, implementationSubjectName(subject))
		}
		t.Fatalf("subjects = %v, want none: the claim is about the repository, not a component", names)
	}
}

// The explicit list form still names components; the guard is about prose, not
// about colons.
func TestStatusListStillNamesComponents(t *testing.T) {
	cases := []struct {
		sentence string
		want     []string
	}{
		{
			sentence: "Fully implemented: product-service (REST CRUD), order-service (checkout).",
			want:     []string{"product-service", "order-service"},
		},
		{
			sentence: "Stubs (health checks only): API-gateway, cart, delivery, notification services.",
			want:     []string{"API-gateway", "cart", "delivery", "notification"},
		},
		{
			sentence: "product-service is fully implemented.",
			want:     []string{"product-service"},
		},
	}
	for _, tc := range cases {
		match := implementationStatusPattern.FindStringIndex(tc.sentence)
		if match == nil {
			match = mockStatusPattern.FindStringIndex(tc.sentence)
		}
		if match == nil {
			t.Fatalf("%q no longer matches a status audit", tc.sentence)
		}
		var got []string
		for _, subject := range implementationClaimSubjects(tc.sentence, match) {
			got = append(got, implementationSubjectName(subject))
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%q subjects = %v, want %v", tc.sentence, got, tc.want)
		}
	}
}

// The fabricated-subject review is what the user saw; it must be gone.
func TestScaffoldReviewNoLongerNamesProseWords(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("backend/cmd/product-service/main.go", "package main\nfunc main() {}\n", 98),
		claimRead("backend/cmd/order-service/main.go", "package main\nfunc main() {}\n", 71),
	}}}
	answer := "It is an in-progress scaffold: the product-service is fully implemented, the order-service now implements checkout."
	reason := strings.ToLower(AssessGrounding(claimContract(), turns, answer).Reason())
	for _, junk := range []string{"checkout", "in-progress"} {
		if strings.Contains(reason, junk) {
			t.Fatalf("reason = %q, want no fabricated subject %q", reason, junk)
		}
	}
}
func TestComponentSubjectSkipsAdjectiveQualifiers(t *testing.T) {
	sentence := "Frontend: storefront at /shop and role-protected admin console at /admin, with mock auth (localStorage)."
	start := strings.Index(sentence, "mock auth")
	if start < 0 {
		t.Fatal("fixture changed")
	}
	subject := ComponentSubject(sentence, start)
	if subject == "role-protected" {
		t.Fatalf("subject = %q, want the component the adjective qualifies", subject)
	}
	if subject == "" {
		t.Fatal("no component subject was extracted at all")
	}
	for _, qualifier := range []string{"role-protected", "server-side", "read-only"} {
		if !componentTokenIsQualifier(qualifier) {
			t.Errorf("%q was not recognized as a qualifier", qualifier)
		}
	}
	for _, component := range []string{"product-service", "api-gateway", "cart-service"} {
		if componentTokenIsQualifier(component) {
			t.Errorf("%q was misread as a qualifier", component)
		}
	}
}

// Session 318: "go-ecommerce is a full-stack e-commerce platform, currently a
// work-in-progress scaffold: …" produced the review subjects go-ecommerce,
// full-stack, and e-commerce. The project name and the predicate's adjectives
// name no component the runtime can read, so the review sent the model to three
// paths that do not exist.
func TestProjectDescriptionIsNotAComponentList(t *testing.T) {
	sentence := "go-ecommerce is a full-stack e-commerce platform, currently a " +
		"work-in-progress scaffold: a React + TypeScript storefront/admin UI on " +
		"the frontend and a Go (Golang) microservices backend."
	match := mockStatusPattern.FindStringIndex(sentence)
	if match == nil {
		t.Fatal("fixture no longer matches the scaffold audit")
	}
	if subjects := implementationClaimSubjects(sentence, match); len(subjects) != 0 {
		var names []string
		for _, subject := range subjects {
			names = append(names, implementationSubjectName(subject))
		}
		t.Fatalf("subjects = %v, want none: the sentence describes the project, not a component", names)
	}
}

// A route names a screen, not a component coordinate. "/admin" cannot be
// satisfied by reading the admin page, because a path under src/ derives to the
// frontend.
func TestRouteNamesAreNotComponentSubjects(t *testing.T) {
	sentence := "Splits into /shop (customer storefront) and /admin " +
		"(role-protected console), plus /login with mock auth stored in localStorage."
	match := mockStatusPattern.FindStringIndex(sentence)
	if match == nil {
		t.Fatal("fixture no longer matches the mock audit")
	}
	if subjects := implementationClaimSubjects(sentence, match); len(subjects) != 0 {
		t.Fatalf("subjects = %+v, want none: a route is not a component", subjects)
	}
}

// The coordinate filter must not reject real component names: the audit still
// names every service a stub claim covers.
func TestKebabCoordinatesStillNameComponents(t *testing.T) {
	sentence := "The API-gateway and cart-service are health-check stubs."
	match := implementationStatusPattern.FindStringIndex(sentence)
	if match == nil {
		t.Fatal("fixture no longer matches the stub audit")
	}
	var got []string
	for _, subject := range implementationClaimSubjects(sentence, match) {
		got = append(got, implementationSubjectName(subject))
	}
	if strings.Join(got, ",") != "API-gateway,cart-service" {
		t.Fatalf("subjects = %v, want [API-gateway cart-service]", got)
	}
}
