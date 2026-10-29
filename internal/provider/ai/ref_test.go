package ai

import (
	"strings"
	"testing"
)

// A model reference is persisted (session rows, settings roles) and parsed by
// catalog.Resolve with a first-slash split. The formatter must stay the exact
// inverse, or a stored reference stops resolving after a restart.
func TestModelRefRoundTripsThroughTheFirstSlashSplit(t *testing.T) {
	model := Model{Provider: "anthropic", ID: "claude-sonnet-4-5"}
	if got, want := model.Ref(), "anthropic/claude-sonnet-4-5"; got != want {
		t.Fatalf("Ref() = %q, want %q", got, want)
	}
	provider, id, ok := strings.Cut(model.Ref(), "/")
	if !ok || provider != model.Provider || id != model.ID {
		t.Fatalf("split of %q = (%q, %q, %v), want (%q, %q, true)",
			model.Ref(), provider, id, ok, model.Provider, model.ID)
	}
}

// An OpenRouter-style id contains a slash. Splitting on the first slash keeps
// the provider and the whole id distinct; a formatter that escaped or dropped
// the slash would silently retarget the model on resume.
func TestModelRefPreservesAnIDThatContainsASlash(t *testing.T) {
	model := Model{Provider: "openrouter", ID: "anthropic/claude-3.5-sonnet"}
	ref := model.Ref()
	if want := "openrouter/anthropic/claude-3.5-sonnet"; ref != want {
		t.Fatalf("Ref() = %q, want %q", ref, want)
	}
	provider, id, ok := strings.Cut(ref, "/")
	if !ok || provider != "openrouter" || id != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("split of %q = (%q, %q, %v), want the provider and the full id",
			ref, provider, id, ok)
	}
}

// The pair formatter and the method must agree: a settings entry is stored as a
// (provider, id) pair and compared against a resolved model's reference.
func TestRefPairAndMethodAgree(t *testing.T) {
	model := Model{Provider: "local", ID: "llama3"}
	if got, want := Ref(model.Provider, model.ID), model.Ref(); got != want {
		t.Fatalf("Ref(parts) = %q, Model.Ref() = %q; want one definition", got, want)
	}
}
