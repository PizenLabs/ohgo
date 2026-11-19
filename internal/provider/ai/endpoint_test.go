package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func endpointSpec(baseURL string, keyOptional bool, env ...string) EndpointSpec {
	return EndpointSpec{
		Provider:    "local",
		Name:        "Local vLLM",
		BaseURL:     baseURL,
		EnvVars:     env,
		KeyOptional: keyOptional,
		Models: map[string]Model{
			"qwen3-coder": {
				ID:              "qwen3-coder",
				Name:            "qwen3-coder",
				Provider:        "local",
				API:             APIOpenAIChatCompletions,
				BaseURL:         baseURL,
				ContextWindow:   262144,
				MaxOutputTokens: 32768,
				InputModalities: []string{"text"},
			},
		},
	}
}

// TestRegisteredEndpointResolvesWithoutACatalogEntry is the custom-endpoint
// contract: a model the bundled catalog has never seen resolves from explicit
// facts and drives the shared OpenAI transport.
func TestRegisteredEndpointResolvesWithoutACatalogEntry(t *testing.T) {
	registry := NewRegistry()
	registry.SetCredentialsFile(t.TempDir() + "/credentials.json")
	if err := registry.RegisterEndpoint(endpointSpec("http://127.0.0.1:1234/v1", true)); err != nil {
		t.Fatal(err)
	}
	model, handled, err := registry.CustomModel("local/qwen3-coder")
	if !handled || err != nil {
		t.Fatalf("CustomModel: handled=%v err=%v", handled, err)
	}
	if model.ContextWindow != 262144 || model.MaxOutputTokens != 32768 {
		t.Errorf("facts = %+v", model)
	}
	provider, err := registry.ProviderFor(model)
	if err != nil {
		t.Fatalf("ProviderFor: %v", err)
	}
	openai, ok := provider.(*OpenAIProvider)
	if !ok {
		t.Fatalf("provider = %T, want the shared OpenAI transport", provider)
	}
	if openai.BaseURL != "http://127.0.0.1:1234/v1" || openai.APIKey != "" {
		t.Errorf("provider = %+v, want the configured keyless endpoint", openai)
	}
}

// TestRegisteredEndpointNamesItsCredential proves the environment variable the
// user declared is the one resolution names and reads.
func TestRegisteredEndpointNamesItsCredential(t *testing.T) {
	registry := NewRegistry()
	registry.SetCredentialsFile(t.TempDir() + "/credentials.json")
	if err := registry.RegisterEndpoint(endpointSpec("https://gateway.invalid/v1", false, "LOCAL_LLM_KEY")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_LLM_KEY", "")
	if _, err := registry.ResolveAPIKey("local"); err == nil || !strings.Contains(err.Error(), "LOCAL_LLM_KEY") {
		t.Fatalf("missing-key error = %v, want it to name LOCAL_LLM_KEY", err)
	}
	t.Setenv("LOCAL_LLM_KEY", "sk-local")
	key, source, err := registry.ResolveAPIKeySource("local")
	if err != nil || key != "sk-local" || !strings.Contains(source, "LOCAL_LLM_KEY") {
		t.Fatalf("resolved (%q, %q, %v)", key, source, err)
	}
}

// TestRegisteredEndpointRejectsAnUndeclaredModel keeps a typo from falling
// through to another provider's catalog row.
func TestRegisteredEndpointRejectsAnUndeclaredModel(t *testing.T) {
	registry := NewRegistry()
	registry.SetCredentialsFile(t.TempDir() + "/credentials.json")
	if err := registry.RegisterEndpoint(endpointSpec("http://127.0.0.1:1234/v1", true)); err != nil {
		t.Fatal(err)
	}
	if _, handled, err := registry.CustomModel("local/qwen3-codr"); !handled || err == nil {
		t.Fatalf("handled=%v err=%v, want a named endpoint error", handled, err)
	}
}

// TestCustomOpenAIProviderOmitsAnEmptyBearer keeps a keyless local server from
// receiving a rejected `Authorization: Bearer ` header.
func TestCustomOpenAIProviderOmitsAnEmptyBearer(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := &OpenAIProvider{BaseURL: server.URL}
	stream, err := provider.Stream(context.Background(), Request{
		Model:    testOpenAIModel(),
		Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events {
	}
	if auth != "" {
		t.Errorf("Authorization = %q, want no header for a keyless endpoint", auth)
	}
}
