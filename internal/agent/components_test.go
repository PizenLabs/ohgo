package agent

import "testing"

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
