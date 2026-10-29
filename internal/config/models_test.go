package config

import "testing"

func TestModelForPrefersWorkspaceOverride(t *testing.T) {
	settings := Settings{
		Model:  "anthropic/global",
		Models: map[string]string{"build": "openrouter/strong-code"},
	}
	provider, id, ok := settings.ModelFor("build")
	if !ok || provider != "openrouter" || id != "strong-code" {
		t.Fatalf("build model = %q/%q (%v), want openrouter/strong-code", provider, id, ok)
	}
	// A workspace with no override falls back to the global default, so
	// configuring one workspace does not require configuring all three (§12).
	provider, id, ok = settings.ModelFor("ask")
	if !ok || provider != "anthropic" || id != "global" {
		t.Fatalf("ask model = %q/%q (%v), want the global fallback", provider, id, ok)
	}
}

func TestModelForIsCaseInsensitive(t *testing.T) {
	settings := Settings{Models: map[string]string{"fuzz": "ollama/llama3"}}
	provider, id, ok := settings.ModelFor("FUZZ")
	if !ok || provider != "ollama" || id != "llama3" {
		t.Fatalf("got %q/%q (%v), want ollama/llama3", provider, id, ok)
	}
}

func TestModelForFallsBackWhenWorkspaceValueIsMalformed(t *testing.T) {
	// A malformed workspace override must not shadow a valid global default:
	// falling through is better than refusing to resolve anything.
	settings := Settings{
		Model:  "anthropic/global",
		Models: map[string]string{"build": "not-a-model-ref"},
	}
	provider, id, ok := settings.ModelFor("build")
	if !ok || provider != "anthropic" || id != "global" {
		t.Fatalf("got %q/%q (%v), want the global fallback", provider, id, ok)
	}
}

func TestModelForWithNoConfigurationReportsNothing(t *testing.T) {
	if _, _, ok := (Settings{}).ModelFor("build"); ok {
		t.Error("an empty configuration resolved a model")
	}
}
