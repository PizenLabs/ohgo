package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validEndpoint() Endpoint {
	return Endpoint{
		BaseURL:       "http://127.0.0.1:1234/v1",
		Models:        []string{"qwen3-coder"},
		ContextWindow: 262144,
		MaxTokens:     32768,
	}
}

func TestEndpointValidateAcceptsALocalServer(t *testing.T) {
	if err := validEndpoint().Validate("local"); err != nil {
		t.Fatalf("valid endpoint refused: %v", err)
	}
}

// TestEndpointValidateRejectsUnusableFacts is the guard that keeps a typo from
// becoming a wrong context window or a provider-side 400 at request time.
func TestEndpointValidateRejectsUnusableFacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Endpoint)
		want   string
	}{
		{"missing base", func(e *Endpoint) { e.BaseURL = "" }, "baseUrl"},
		{"non-http base", func(e *Endpoint) { e.BaseURL = "ftp://host/v1" }, "http or https"},
		{"no models", func(e *Endpoint) { e.Models = nil }, "no models"},
		{"duplicate model", func(e *Endpoint) { e.Models = []string{"a", "a"} }, "twice"},
		{"no context", func(e *Endpoint) { e.ContextWindow = 0 }, "contextWindow"},
		{"no output", func(e *Endpoint) { e.MaxTokens = 0 }, "maxTokens"},
		{"output above context", func(e *Endpoint) { e.MaxTokens = e.ContextWindow + 1 }, "exceeds"},
		{"bad env", func(e *Endpoint) { e.APIKeyEnv = "not a name" }, "apiKeyEnv"},
		{"no text", func(e *Endpoint) { e.Input = []string{"image"} }, "include text"},
		{"efforts without reasoning", func(e *Endpoint) {
			e.ReasoningEfforts = []string{"high"}
		}, "reasoning is false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := validEndpoint()
			tc.mutate(&endpoint)
			err := endpoint.Validate("local")
			if err == nil {
				t.Fatal("invalid endpoint accepted")
			}
			if !errors.Is(err, ErrInvalidEndpoint) {
				t.Errorf("error = %v, want ErrInvalidEndpoint", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestSettingsDecodeEndpoints proves the persisted shape a user hand-edits is
// the shape the loader reads, with no second spelling.
func TestSettingsDecodeEndpoints(t *testing.T) {
	const raw = `{
		"model": "local/qwen3-coder",
		"endpoints": {
			"local": {
				"baseUrl": "http://127.0.0.1:1234/v1",
				"models": ["qwen3-coder"],
				"contextWindow": 262144,
				"maxTokens": 32768
			}
		}
	}`
	var settings Settings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	endpoint, ok := settings.Endpoints["local"]
	if !ok {
		t.Fatal("endpoint missing after decode")
	}
	if err := endpoint.Validate("local"); err != nil {
		t.Fatalf("decoded endpoint is invalid: %v", err)
	}
	if got := endpoint.InputModalities(); len(got) != 1 || got[0] != "text" {
		t.Errorf("default input = %v, want text", got)
	}
}
