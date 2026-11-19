package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOpenRouterAttributionSurvivesProviderCache proves the headers are part
// of the cached provider instance and not a one-shot decoration of the first
// caller: the second call must reuse the same provider and still attribute.
func TestOpenRouterAttributionSurvivesProviderCache(t *testing.T) {
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	registry := NewRegistry()
	registry.SetBaseURL("openrouter", server.URL)
	registry.SetAPIKey("openrouter", "secret-key")
	model := Model{
		ID: "x", Provider: "openrouter", API: APIOpenAIChatCompletions,
		BaseURL: server.URL, ContextWindow: 1000, MaxOutputTokens: 100,
	}
	first, err := registry.ProviderFor(model)
	if err != nil {
		t.Fatalf("first ProviderFor: %v", err)
	}
	second, err := registry.ProviderFor(model)
	if err != nil {
		t.Fatalf("second ProviderFor: %v", err)
	}
	if first != second {
		t.Fatal("identical provider requests built two instances instead of using the cache")
	}

	stream, err := second.Stream(context.Background(), Request{
		Model:    Model{ID: "x", Provider: "openrouter"},
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_, _ = stream.Result()

	got := <-headers
	for name, want := range OpenRouterAttribution() {
		if got.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, got.Get(name), want)
		}
		if strings.Contains(got.Get(name), "secret-key") {
			t.Errorf("%s leaked the API key", name)
		}
	}
}

// TestAttributionDoesNotReachOtherProviders guards the boundary: attribution
// is keyed on OpenRouter, not on the shared Chat Completions transport.
func TestAttributionDoesNotReachOtherProviders(t *testing.T) {
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	registry := NewRegistry()
	registry.SetBaseURL("openai", server.URL)
	registry.SetAPIKey("openai", "k")
	provider, err := registry.ProviderFor(Model{
		ID: "gpt-5", Provider: "openai", API: APIOpenAIChatCompletions,
		BaseURL: server.URL, ContextWindow: 1000, MaxOutputTokens: 100,
	})
	if err != nil {
		t.Fatalf("ProviderFor: %v", err)
	}
	stream, err := provider.Stream(context.Background(), Request{
		Model:    Model{ID: "gpt-5", Provider: "openai"},
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_, _ = stream.Result()

	got := <-headers
	for name := range OpenRouterAttribution() {
		if value := got.Get(name); value != "" {
			t.Errorf("%s = %q on an unrelated provider, want absent", name, value)
		}
	}
}

const agenticHarnessResponse = `{
  "error": {
    "message": "thinkingmachines/inkling-2-small:free is only available on agentic harnesses. Try plugging it into a coding agent or productivity app listed on https://openrouter.ai/apps",
    "code": 403,
    "metadata": {
      "routing_funnel": [{"step": "Initial Endpoints", "endpoint_count": 1}],
      "failed_routing_step": "Gate Free Endpoints by Agentic Harness"
    }
  }
}`

// errorServer answers a stream request with one status and body.
func errorServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func streamError(t *testing.T, provider *OpenAIProvider) error {
	t.Helper()
	_, err := provider.Stream(context.Background(), Request{
		Model:    Model{ID: "x", Provider: "openrouter"},
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("Stream succeeded, want the provider rejection")
	}
	return err
}

// TestAgenticHarnessRestrictionIsClassified pins the supplied production
// failure: status and provider code survive, the routing step is recognized,
// and the diagnostic never claims eligibility.
func TestAgenticHarnessRestrictionIsClassified(t *testing.T) {
	server := errorServer(t, http.StatusForbidden, agenticHarnessResponse)
	err := streamError(t, &OpenAIProvider{APIKey: "k", BaseURL: server.URL, ExtraHeaders: OpenRouterAttribution()})

	var httpErr *httpError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T, want *httpError", err)
	}
	if httpErr.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", httpErr.Status)
	}
	if httpErr.Code != 403 {
		t.Errorf("provider code = %d, want 403 preserved", httpErr.Code)
	}
	if httpErr.FailedRoutingStep != OpenRouterAgenticHarnessStep {
		t.Errorf("routing step = %q, want %q", httpErr.FailedRoutingStep, OpenRouterAgenticHarnessStep)
	}
	if !httpErr.AgenticHarnessRestriction() {
		t.Error("the agentic-harness gate was not recognized")
	}
	if !errors.Is(err, ErrAgenticHarnessRestriction) {
		t.Error("errors.Is did not classify the restriction")
	}
	message := err.Error()
	if !strings.Contains(message, "attribution is not eligibility") {
		t.Errorf("message = %q, want the attribution/eligibility distinction", message)
	}
	if strings.Contains(message, "OHGO is approved") || strings.Contains(message, "OHGO is recognized") {
		t.Errorf("message = %q, must not claim OHGO eligibility", message)
	}
}

// TestUnrelated403IsNotTheHarnessGate keeps spend limits, disabled keys, and
// other access denials from being mislabelled as an eligibility decision.
func TestUnrelated403IsNotTheHarnessGate(t *testing.T) {
	body := `{"error":{"message":"Key disabled by the account owner","code":403}}`
	server := errorServer(t, http.StatusForbidden, body)
	err := streamError(t, &OpenAIProvider{APIKey: "k", BaseURL: server.URL})

	if errors.Is(err, ErrAgenticHarnessRestriction) {
		t.Fatal("a generic 403 was classified as the agentic-harness gate")
	}
	if strings.Contains(err.Error(), "agentic harness") {
		t.Errorf("message = %q, want no harness diagnostic", err.Error())
	}
}

// TestProviderErrorsPreserveStatusAndCode covers the other rejection classes
// a user can hit, so classification never overwrites the provider's evidence.
func TestProviderErrorsPreserveStatusAndCode(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"error":{"message":"No auth credentials found","code":401}}`},
		{http.StatusPaymentRequired, `{"error":{"message":"Insufficient credits","code":402}}`},
		{http.StatusNotFound, `{"error":{"message":"No endpoints found for model","code":404}}`},
		{http.StatusTooManyRequests, `{"error":{"message":"Rate limit exceeded","code":429}}`},
		{http.StatusBadGateway, `{"error":{"message":"Provider is down","code":502}}`},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("HTTP%d", tc.status), func(t *testing.T) {
			server := errorServer(t, tc.status, tc.body)
			err := streamError(t, &OpenAIProvider{APIKey: "k", BaseURL: server.URL})

			var httpErr *httpError
			if !errors.As(err, &httpErr) {
				t.Fatalf("error = %T, want *httpError", err)
			}
			if httpErr.Status != tc.status || httpErr.Code != tc.status {
				t.Errorf("status/code = %d/%d, want %d/%d", httpErr.Status, httpErr.Code, tc.status, tc.status)
			}
			if errors.Is(err, ErrAgenticHarnessRestriction) {
				t.Error("unrelated provider rejection was misclassified")
			}
		})
	}
}
