package ai

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// Live tests are opt-in and environment-gated: they prove the execution path
// against real services, but a missing service or credential skips with a
// reason rather than failing. A skip is reported, never silent.

// TestLiveOllamaLocalTags runs against a real local server when one answers.
// It validates discovery shape and the no-key contract, not model quality.
func TestLiveOllamaLocalTags(t *testing.T) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:11434", 2*time.Second)
	if err != nil {
		t.Skipf("no local Ollama server: %v", err)
	}
	_ = conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := ProbeProvider(ctx, nil, APIOllamaChat, OllamaBaseURL, "")
	if !result.Reachable || !result.Authenticated {
		t.Fatalf("probe = %+v, want a keyless local server to answer", result)
	}
	t.Logf("local Ollama lists %d models", result.Models)
}

// TestLiveOpenRouterCatalog runs when a real credential is configured. It
// validates discovery and auth against the production endpoint without
// spending a completion.
func TestLiveOpenRouterCatalog(t *testing.T) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skipf("no OPENROUTER_API_KEY configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := ProbeProvider(ctx, nil, APIOpenAIChatCompletions, "https://openrouter.ai/api/v1", key)
	if !result.Reachable {
		t.Fatalf("probe = %+v, want the endpoint reachable", result)
	}
	if !result.Authenticated {
		t.Fatalf("probe = %+v, want the configured key accepted", result)
	}
	if result.Models == 0 {
		t.Errorf("probe lists no models: the catalog shape may have changed")
	}
	t.Logf("OpenRouter lists %d models", result.Models)
}
