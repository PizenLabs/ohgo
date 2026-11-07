package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/continuity/session"
)

// localSessionTitle is the deterministic, synchronous source of every session
// label. It strips request framing, preserves technical tokens, and bounds the
// result without a model call.
func TestLocalSessionTitleIsDeterministicAndBounded(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "request framing", prompt: "Use ls to inspect the project root and backend/", want: "ls project root backend"},
		{name: "politeness", prompt: "Please explain the auth flow to me", want: "auth flow"},
		{name: "paths and identifiers", prompt: "Use internal/promptopt/titler.go in the patch", want: "internal/promptopt/titler.go patch"},
		{name: "trailing article", prompt: "Use ls to inspect the", want: "ls"},
		{name: "noise only", prompt: "please explain the", want: ""},
		{name: "empty", prompt: "   ", want: ""},
		{name: "long single token", prompt: strings.Repeat("a", 60), want: strings.Repeat("a", 40)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := localSessionTitle(tc.prompt); got != tc.want {
				t.Fatalf("localSessionTitle(%q) = %q, want %q", tc.prompt, got, tc.want)
			}
		})
	}
}

// Applying a local title is synchronous and persists the exact deterministic
// label. It never touches a provider, and a human title is never overwritten.
func TestApplyLocalSessionTitlePersistsWithoutOverwritingHumanChoice(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	defer func() { _ = store.Close() }()

	sess, err := store.CreateWith(session.CreateOptions{Cwd: t.TempDir(), Model: "p/m"})
	if err != nil {
		t.Fatalf("CreateWith: %v", err)
	}
	const prompt = "Use ls to inspect the project root and backend/"
	if got := applyLocalSessionTitle(store, &sess, prompt); got != "ls project root backend" {
		t.Fatalf("applied title = %q, want the local sanitization", got)
	}
	if sess.Title != "ls project root backend" {
		t.Fatalf("in-memory title = %q, want the local sanitization", sess.Title)
	}
	stored, err := store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Title != "ls project root backend" {
		t.Fatalf("stored title = %q, want the local sanitization", stored.Title)
	}

	// An explicit title is a human decision, not a placeholder to replace.
	if got := applyLocalSessionTitle(store, &sess, "rename this session"); got != "" {
		t.Fatalf("second application returned %q, want no overwrite", got)
	}
	stored, err = store.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get after second application: %v", err)
	}
	if stored.Title != "ls project root backend" {
		t.Fatalf("stored title = %q, want the original local label", stored.Title)
	}
}
