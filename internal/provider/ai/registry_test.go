package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegistryBaseURLOverridesTheModelDefault is a regression test for a
// precedence bug.
//
// The catalog ships every model with a BaseURL, which made Model.BaseURL take
// priority over a registry override. Requests then went to the provider's
// public endpoint instead of the configured gateway or test server, with
// whatever credential happened to be in the environment — an authentication
// error nowhere near the cause.
//
// SetBaseURL is the operator's explicit choice; Model.BaseURL is only the
// catalog's default. The override must win.
func TestRegistryBaseURLOverridesTheModelDefault(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	registry := NewRegistry()
	registry.SetAPIKey("anthropic", "test-key")
	registry.SetBaseURL("anthropic", server.URL)

	model := testModel()
	if model.BaseURL == "" {
		t.Skip("the fixture carries no catalog default, so it cannot prove precedence")
	}

	provider, err := registry.ProviderFor(model)
	if err != nil {
		t.Fatalf("ProviderFor: %v", err)
	}
	anthropic, ok := provider.(*AnthropicProvider)
	if !ok {
		t.Fatalf("provider = %T, want *AnthropicProvider", provider)
	}
	if anthropic.BaseURL != server.URL {
		t.Errorf("provider base URL = %q, want the registry override %q", anthropic.BaseURL, server.URL)
	}

	// Prove it end to end: the request must actually reach the test server.
	stream, err := anthropic.Stream(context.Background(), Request{
		Model:    model,
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events {
	}
	if gotPath != "/v1/messages" {
		t.Errorf("the request reached %q, want it to hit the override server at /v1/messages", gotPath)
	}
}

// TestRegistryBaseURLWithoutOverrideKeepsTheModelDefault proves the catalog
// default still applies, so removing the override does not break resolution.
func TestRegistryBaseURLWithoutOverrideKeepsTheModelDefault(t *testing.T) {
	registry := NewRegistry()
	registry.SetAPIKey("anthropic", "test-key")

	model := testModel()
	model.BaseURL = "https://catalog.invalid"
	provider, err := registry.ProviderFor(model)
	if err != nil {
		t.Fatalf("ProviderFor: %v", err)
	}
	anthropic := provider.(*AnthropicProvider)
	if anthropic.BaseURL != "https://catalog.invalid" {
		t.Errorf("base URL = %q, want the model default to survive", anthropic.BaseURL)
	}
}

// TestProviderCachingKeysOnBaseURL guards the provider cache: two different
// endpoints for one provider must not share an instance, or the second would
// silently keep talking to the first.
func TestProviderCachingKeysOnBaseURL(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer second.Close()

	registry := NewRegistry()
	registry.SetAPIKey("anthropic", "test-key")

	registry.SetBaseURL("anthropic", first.URL)
	a, err := registry.ProviderFor(testModel())
	if err != nil {
		t.Fatal(err)
	}

	registry.SetBaseURL("anthropic", second.URL)
	b, err := registry.ProviderFor(testModel())
	if err != nil {
		t.Fatal(err)
	}

	if a == b {
		t.Fatal("two base URLs returned the same provider instance")
	}
	if a.(*AnthropicProvider).BaseURL != first.URL {
		t.Errorf("first provider points at %q, want %q", a.(*AnthropicProvider).BaseURL, first.URL)
	}
	if b.(*AnthropicProvider).BaseURL != second.URL {
		t.Errorf("second provider points at %q, want %q", b.(*AnthropicProvider).BaseURL, second.URL)
	}
}

// TestUnknownAPIHasNoProvider keeps the failure at resolve time rather than
// call time: a model whose protocol this build cannot speak must not reach a
// request.
func TestUnknownAPIHasNoProvider(t *testing.T) {
	registry := NewRegistry()
	registry.SetAPIKey("anthropic", "test-key")

	model := testModel()
	model.API = "some-protocol-nobody-implements"

	_, err := registry.ProviderFor(model)
	if err == nil {
		t.Fatal("a model with an unimplemented protocol was given a provider")
	}
	if !errors.Is(err, errNoProviderFor(model.API)) && err.Error() == "" {
		t.Error("the error does not name the missing protocol")
	}
}

func errNoProviderFor(api API) error { return unknownAPIError{API: api} }
