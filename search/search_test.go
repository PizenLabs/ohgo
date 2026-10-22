package search

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/knowledge"
)

func testProvider(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New([]ProviderConfig{{
		Name:       "brave",
		Kind:       "brave",
		Endpoint:   server.URL,
		APIKeyEnv:  "TEST_SEARCH_KEY",
		MaxBytes:   4096,
		MaxResults: 4,
		Client:     server.Client(),
		Getenv:     func(string) string { return "secret-key" },
		Now:        func() time.Time { return time.Unix(100, 0).UTC() },
		AllowHTTP:  true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

// The provider is a transport only: it returns normalized hits with source,
// URL, timestamp, and provider, and never flattens them into anonymous text.
func TestBraveSearchNormalizesHitsWithProvenance(t *testing.T) {
	client, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "go generics" {
			t.Errorf("query = %q", r.URL.Query().Get("q"))
		}
		if r.Header.Get("X-Subscription-Token") != "secret-key" {
			t.Errorf("credential header missing")
		}
		if r.URL.Query().Get("count") != "2" {
			t.Errorf("count = %q", r.URL.Query().Get("count"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{"results": []any{
				map[string]any{"title": "Generics", "url": "https://go.dev/doc/generics", "description": "A guide"},
				map[string]any{"title": "Spec", "url": "https://go.dev/ref/spec", "description": "The spec"},
			}},
		})
	})
	provider, err := client.Get("")
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.Search(context.Background(), Request{Query: "go generics", Limit: 2, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(got.Hits))
	}
	hit := got.Hits[0]
	if hit.Title != "Generics" || hit.URL != "https://go.dev/doc/generics" ||
		hit.Source != "go.dev" || hit.Snippet != "A guide" {
		t.Fatalf("hit = %+v", hit)
	}
	if got.Provider != "brave" || got.RetrievedAt.Unix() != 100 {
		t.Fatalf("response provenance = %+v", got)
	}
}

// One malformed provider item must not discard the usable items around it.
func TestSearchReportsPartialWhenAnItemIsUnusable(t *testing.T) {
	client, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"web":{"results":[{"title":"ok","url":"https://a.example/x","description":"d"},{"title":"bad","url":"file:///etc/passwd","description":"d"}]}}`)
	})
	provider, _ := client.Get("brave")
	got, err := provider.Search(context.Background(), Request{Query: "q", Limit: 4, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %d, want the one usable hit", len(got.Hits))
	}
	if !got.Partial {
		t.Fatal("a skipped bad item was not reported as partial")
	}
}

// A response over the configured body cap fails truthfully; it is not parsed
// as if the prefix were the whole document.
func TestSearchRefusesOversizedResponse(t *testing.T) {
	client, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
	})
	provider, _ := client.Get("brave")
	_, err := provider.Search(context.Background(), Request{Query: "q", Limit: 2, MaxBytes: 4096})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureResponseLimit {
		t.Fatalf("error = %v, want response_limit", err)
	}
}

func TestSearchClassifiesProviderFailures(t *testing.T) {
	cases := []struct {
		status int
		want   knowledge.FailureClass
	}{
		{401, knowledge.FailureAuthentication},
		{429, knowledge.FailureRateLimited},
		{500, knowledge.FailureUnavailable},
		{400, knowledge.FailureInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			client, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			})
			provider, _ := client.Get("brave")
			_, err := provider.Search(context.Background(), Request{Query: "q", Limit: 2, MaxBytes: 4096})
			var failure knowledge.Failure
			if !errors.As(err, &failure) || failure.Class != tc.want {
				t.Fatalf("error = %v, want class %s", err, tc.want)
			}
		})
	}
}

func TestSearchClassifiesMalformedJSON(t *testing.T) {
	client, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"web":`)
	})
	provider, _ := client.Get("brave")
	_, err := provider.Search(context.Background(), Request{Query: "q", Limit: 2, MaxBytes: 4096})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureMalformedResponse {
		t.Fatalf("error = %v, want malformed_response", err)
	}
}

func TestSearchRefusesMissingCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request with no credential reached the provider")
	}))
	defer server.Close()
	client, err := New([]ProviderConfig{{
		Name: "brave", Kind: "brave", Endpoint: server.URL, APIKeyEnv: "MISSING",
		Getenv: func(string) string { return "" }, Client: server.Client(), AllowHTTP: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := client.Get("")
	_, err = provider.Search(context.Background(), Request{Query: "q", Limit: 1, MaxBytes: 1024})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureAuthentication {
		t.Fatalf("error = %v, want authentication_failure", err)
	}
}

func TestTavilyUsesPOSTAndBearerCredential(t *testing.T) {
	var gotMethod, gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{map[string]any{"title": "T", "url": "https://example.com", "content": "C"}},
		})
	}))
	defer server.Close()
	client, err := New([]ProviderConfig{{
		Name: "tavily", Kind: "tavily", Endpoint: server.URL, APIKeyEnv: "TAVILY_KEY",
		Client: server.Client(), Getenv: func(string) string { return "k" }, AllowHTTP: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := client.Get("tavily")
	got, err := provider.Search(context.Background(), Request{Query: "query", Limit: 3, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotAuth != "Bearer k" {
		t.Fatalf("method/auth = %s/%q", gotMethod, gotAuth)
	}
	if gotBody["query"] != "query" || gotBody["max_results"] != float64(3) {
		t.Fatalf("body = %+v", gotBody)
	}
	if got.Hits[0].Snippet != "C" {
		t.Fatalf("hit = %+v", got.Hits[0])
	}
}

func TestProviderConfigurationRequiresExplicitSafety(t *testing.T) {
	if _, err := New([]ProviderConfig{{Name: "x", Kind: "brave", Endpoint: "http://example.com"}}); err == nil {
		t.Fatal("plain http endpoint was accepted without AllowHTTP")
	}
	if _, err := New([]ProviderConfig{{Name: "x", Kind: "unknown", Endpoint: "https://example.com"}}); err == nil {
		t.Fatal("unknown provider kind was accepted")
	}
	if _, err := New([]ProviderConfig{{Name: "x", Endpoint: "https://example.com"}}); err == nil {
		t.Fatal("missing provider kind was accepted")
	}
	if _, err := New([]ProviderConfig{{Name: "x", Kind: "brave", Endpoint: "https://user:pass@example.com"}}); err == nil {
		t.Fatal("endpoint with embedded credentials was accepted")
	}
}

func TestValidateRequestBoundsQueryAndLimit(t *testing.T) {
	if err := ValidateRequest(Request{Query: "", Limit: 1, MaxBytes: 1}); err == nil {
		t.Fatal("empty query was accepted")
	}
	if err := ValidateRequest(Request{Query: strings.Repeat("q", MaxQueryChars+1), Limit: 1, MaxBytes: 1}); err == nil {
		t.Fatal("oversized query was accepted")
	}
	if err := ValidateRequest(Request{Query: "q", Limit: MaxResultsLimit + 1, MaxBytes: 1}); err == nil {
		t.Fatal("oversized result limit was accepted")
	}
	if err := ValidateRequest(Request{Query: "q", Limit: 1, MaxBytes: MaxResponseBytes + 1}); err == nil {
		t.Fatal("oversized byte bound was accepted")
	}
}
