package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTransportsForwardTheCatalogSessionHeader is the contract that makes
// OpenCode Go usable: the gateway rejects a routed request without
// x-opencode-session, and the header name is catalog data rather than a
// provider-name branch in the transport.
func TestTransportsForwardTheCatalogSessionHeader(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		got := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got <- r.Header.Get("x-opencode-session")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer server.Close()

		model := testOpenAIModel()
		model.BaseURL = server.URL
		model.SessionHeader = "x-opencode-session"
		provider := &OpenAIProvider{APIKey: "k", BaseURL: server.URL}

		stream, err := provider.Stream(context.Background(), Request{
			Model:     model,
			Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
			MaxTokens: 16,
			SessionID: "ses_from_host",
		})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		for range stream.Events {
		}
		if header := <-got; header != "ses_from_host" {
			t.Errorf("session header = %q, want the request's identity", header)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		got := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got <- r.Header.Get("x-opencode-session")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
		}))
		defer server.Close()

		model := testModel()
		model.BaseURL = server.URL
		model.SessionHeader = "x-opencode-session"
		provider := &AnthropicProvider{APIKey: "k", BaseURL: server.URL, Version: AnthropicVersion}

		stream, err := provider.Stream(context.Background(), Request{
			Model:     model,
			Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
			MaxTokens: 16,
			SessionID: "ses_from_host",
		})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		for range stream.Events {
		}
		if header := <-got; header != "ses_from_host" {
			t.Errorf("session header = %q, want the request's identity", header)
		}
	})
}

// TestProviderSessionFallbackStillSatisfiesTheGateway covers a run without a
// session store: the header must still be present and stable, or every turn
// fails before the model is reached.
func TestProviderSessionFallbackStillSatisfiesTheGateway(t *testing.T) {
	got := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("x-opencode-session")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := testOpenAIModel()
	model.BaseURL = server.URL
	model.SessionHeader = "x-opencode-session"
	provider := &OpenAIProvider{APIKey: "k", BaseURL: server.URL, SessionID: "ses_fallback"}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     model,
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events {
	}
	if header := <-got; header != "ses_fallback" {
		t.Errorf("session header = %q, want the provider's stable fallback", header)
	}
}

// TestTransportsOmitAnUnnamedSessionHeader proves the header is opt-in
// catalog data, not a value sprayed at every provider.
func TestTransportsOmitAnUnnamedSessionHeader(t *testing.T) {
	got := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("x-opencode-session")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := testOpenAIModel()
	model.BaseURL = server.URL
	provider := &OpenAIProvider{APIKey: "k", BaseURL: server.URL, SessionID: "ses_fallback"}

	stream, err := provider.Stream(context.Background(), Request{
		Model:     model,
		Messages:  []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "hi"}}}},
		MaxTokens: 16,
		SessionID: "ses_from_host",
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events {
	}
	if header := <-got; header != "" {
		t.Errorf("session header = %q, want no header without a catalog name", header)
	}
}
