package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestProbeClassifiesReachableAndAuthenticated drives one openai-family
// discovery exchange and pins the full-success report.
func TestProbeClassifiesReachableAndAuthenticated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want the discovery endpoint", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth = %q, want the bearer key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":"b"}]}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := ProbeProvider(ctx, server.Client(), APIOpenAIChatCompletions, server.URL, "k")
	if !result.Reachable || !result.Authenticated || !result.Protocol || result.Models != 2 {
		t.Errorf("result = %+v, want reachable/authenticated/2", result)
	}
	if len(result.ModelIDs) != 2 || result.ModelIDs[0] != "a" || result.ModelIDs[1] != "b" {
		t.Errorf("model ids = %v, want sorted [a b]", result.ModelIDs)
	}
	if !strings.Contains(result.Detail, "2 models") {
		t.Errorf("detail = %q, want the count", result.Detail)
	}
}

// TestProbeRejectsBadCredentials distinguishes auth failure from outage: a
// 401 means the key is wrong, and retrying the network fixes nothing.
func TestProbeRejectsBadCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := ProbeProvider(ctx, server.Client(), APIOpenAIChatCompletions, server.URL, "bad")
	if result.Reachable != true || result.Authenticated != false {
		t.Errorf("result = %+v, want reachable but not authenticated", result)
	}
	if !strings.Contains(result.Detail, "rejected") {
		t.Errorf("detail = %q, want the rejection named", result.Detail)
	}
}

// TestProbeUnreachableLocalNamesTheFix is the Ollama-down failure: dial
// errors must say to start the service.
func TestProbeUnreachableLocalNamesTheFix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := ProbeProvider(ctx, nil, APIOllamaChat, "http://127.0.0.1:9", "")
	if result.Reachable || result.Authenticated {
		t.Errorf("result = %+v, want unreachable", result)
	}
	if !strings.Contains(result.Detail, "running") {
		t.Errorf("detail = %q, want the fix named", result.Detail)
	}
}

// TestProbeOllamaTagsShape counts the native tags list, which nests under
// "models" rather than OpenAI's "data".
func TestProbeOllamaTagsShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want the native tags endpoint", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("auth = %q, want no header without a key", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3.1"}]}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := ProbeProvider(ctx, server.Client(), APIOllamaChat, server.URL, "")
	if !result.Reachable || !result.Authenticated || result.Models != 1 {
		t.Errorf("result = %+v, want reachable/authenticated/1 with no key", result)
	}
}

// TestProbeAnthropicUsesKeyHeader pins the family auth: x-api-key plus the
// version header the Messages API requires.
func TestProbeAnthropicUsesKeyHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want the versioned discovery path", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("headers missing the key or version")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := ProbeProvider(ctx, server.Client(), APIAnthropicMessages, server.URL, "k")
	if !result.Reachable || !result.Authenticated {
		t.Errorf("result = %+v, want success", result)
	}
}

// TestProbeNeverLeaksCredentials scrubs userinfo URLs from diagnostics: a
// base URL embedding a key must not surface it in an error.
func TestProbeNeverLeaksCredentials(t *testing.T) {
	got := scrubURLUserinfo(`dial https://secret-key@proxy.local:8080/x: refused`)
	if strings.Contains(got, "secret-key") {
		t.Errorf("scrubbed = %q, want the userinfo gone", got)
	}
}

// TestOpenRouterSendsAttributionHeaders proves the gateway attribution the
// provider asks for: canonical OpenRouter headers, application constants, and
// no legacy title header that could disagree with the canonical one.
func TestOpenRouterSendsAttributionHeaders(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	registry := NewRegistry()
	registry.SetBaseURL("openrouter", server.URL)
	registry.SetAPIKey("openrouter", "k")
	provider, err := registry.ProviderFor(Model{
		ID: "anthropic/claude-sonnet-4-5", Provider: "openrouter",
		API: APIOpenAIChatCompletions, BaseURL: server.URL,
		ContextWindow: 100000, MaxOutputTokens: 1000,
	})
	if err != nil {
		t.Fatalf("ProviderFor: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := provider.Stream(ctx, Request{
		Model:    Model{ID: "x", Provider: "openrouter"},
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_, _ = stream.Result()

	if got.Get("HTTP-Referer") != OpenRouterReferer {
		t.Errorf("HTTP-Referer = %q, want %q", got.Get("HTTP-Referer"), OpenRouterReferer)
	}
	if got.Get("X-OpenRouter-Title") != OpenRouterTitle {
		t.Errorf("X-OpenRouter-Title = %q, want %q", got.Get("X-OpenRouter-Title"), OpenRouterTitle)
	}
	if got.Get("X-OpenRouter-Categories") != OpenRouterCategories {
		t.Errorf("X-OpenRouter-Categories = %q, want %q", got.Get("X-OpenRouter-Categories"), OpenRouterCategories)
	}
	if got.Get("X-Title") != "" {
		t.Errorf("X-Title = %q, want the legacy header omitted", got.Get("X-Title"))
	}
}

// TestRegistryBuildsOllamaWithoutAKey proves local Ollama needs no
// credential anywhere in the chain: resolution reports none required and
// the transport builds.
func TestRegistryBuildsOllamaWithoutAKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	registry := NewRegistry()
	registry.SetCredentialsFile(home + "/no-creds.json")
	provider, err := registry.ProviderFor(Model{
		ID: "llama3.1", Provider: "ollama", API: APIOllamaChat,
		BaseURL:       "http://127.0.0.1:11434/v1",
		ContextWindow: 128000, MaxOutputTokens: 8192,
	})
	if err != nil {
		t.Fatalf("ProviderFor: %v", err)
	}
	if _, ok := provider.(*OllamaProvider); !ok {
		t.Errorf("provider = %T, want the native ollama transport", provider)
	}
}
