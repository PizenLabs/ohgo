package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/commit"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func TestNewCommitServiceRejectsMissingRegistryAndRole(t *testing.T) {
	if _, err := newCommitService(context.Background(), t.TempDir(), config.Settings{}, nil); err == nil {
		t.Error("newCommitService accepted a nil registry")
	}
	settings := config.Settings{
		Model: "groq/llama-3.1-8b-instant",
		Roles: map[string]config.Profile{"commit": {Model: "unknown/definitely-not-a-model"}},
	}
	if _, err := newCommitService(context.Background(), t.TempDir(), settings, ai.NewRegistry()); err == nil {
		t.Error("newCommitService accepted an unresolvable COMMIT role")
	}
}

func TestCommitEffortPrefersTheCheapestTruthfulDisable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  ai.Model
		effort string
		want   string
	}{
		{"non-reasoning", ai.Model{}, "high", "off"},
		{"role disabled", ai.Model{Reasoning: true}, "off", "off"},
		{"anthropic budget", ai.Model{Reasoning: true, API: ai.APIAnthropicMessages}, "high", "off"},
		{"native disable", ai.Model{Reasoning: true, ReasoningDisable: "none"}, "high", "none"},
		{"lowest ladder", ai.Model{Reasoning: true, ReasoningEfforts: []string{"low", "high"}}, "high", "low"},
		{"mandatory keeps role", ai.Model{Reasoning: true, ReasoningMandatory: true}, "high", "high"},
		{"no known control", ai.Model{Reasoning: true}, "high", "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commitEffort(tc.model, tc.effort); got != tc.want {
				t.Errorf("commitEffort = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommitHooksRefuseMissingConfiguration(t *testing.T) {
	hooks := commitHooks(t.TempDir(), nil, ai.NewRegistry())
	prepared, err := hooks.Prepare()
	if err == nil || prepared != nil {
		t.Fatalf("Prepare with nil settings = %v, want an error", err)
	}

	settings := config.Settings{Model: "groq/llama-3.1-8b-instant"}
	hooks = commitHooks(t.TempDir(), &settings, nil)
	prepared, err = hooks.Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, err := prepared(context.Background()); err == nil {
		t.Error("the prepared proposal ran without a provider registry")
	}
}

func TestCommitREPLCreatesOneCommitAfterExplicitApproval(t *testing.T) {
	root := commitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, capture := newCommitProvider(t, "feat(files): add hana.txt\n\n- create hana.txt")
	defer server.Close()
	registry := ai.NewRegistry()
	registry.SetAPIKey("groq", "test-key")
	registry.SetBaseURL("groq", server.URL)
	settings := config.Settings{
		Model: "groq/llama-3.1-8b-instant",
		Roles: map[string]config.Profile{"commit": {Model: "groq/llama-3.1-8b-instant"}},
	}
	h := &harness{registry: registry, settings: &settings, root: root}
	var out strings.Builder
	if err := h.commitREPL(&out, bufio.NewReader(strings.NewReader("y\n"))); err != nil {
		t.Fatalf("commitREPL: %v", err)
	}
	if !strings.Contains(out.String(), "Commit proposal") || !strings.Contains(out.String(), "Committed ") {
		t.Errorf("output = %q, want the proposal and the verified result", out.String())
	}
	if got := commitCount(t, root); got != 2 {
		t.Errorf("commits = %d, want the initial commit plus exactly one", got)
	}
	if captured := capture.snapshot(); captured.Model != "llama-3.1-8b-instant" {
		t.Errorf("provider model = %q, want the COMMIT role", captured.Model)
	}
}

func TestCommitREPLCancellationMutatesNothing(t *testing.T) {
	root := commitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, _ := newCommitProvider(t, "feat(files): add hana.txt")
	defer server.Close()
	registry := ai.NewRegistry()
	registry.SetAPIKey("groq", "test-key")
	registry.SetBaseURL("groq", server.URL)
	settings := config.Settings{
		Model: "groq/llama-3.1-8b-instant",
		Roles: map[string]config.Profile{"commit": {Model: "groq/llama-3.1-8b-instant"}},
	}
	h := &harness{registry: registry, settings: &settings, root: root}

	// EOF is the resting answer: no newline and no approval must still cancel.
	var out strings.Builder
	if err := h.commitREPL(&out, bufio.NewReader(strings.NewReader("no"))); err != nil {
		t.Fatalf("commitREPL: %v", err)
	}
	if !strings.Contains(out.String(), "commit cancelled.") {
		t.Errorf("output = %q, want the cancellation line", out.String())
	}
	if got := commitCount(t, root); got != 1 {
		t.Errorf("commits = %d, want the initial commit only", got)
	}
}

func TestCommitREPLSurfacesProposalFailure(t *testing.T) {
	root := commitTestRepo(t)
	h := &harness{registry: ai.NewRegistry(), settings: &config.Settings{}, root: root}
	var out strings.Builder
	if err := h.commitREPL(&out, bufio.NewReader(strings.NewReader("y\n"))); !errors.Is(err, commit.ErrNoChanges) {
		t.Fatalf("clean repository = %v, want ErrNoChanges", err)
	}
}

func TestCommitREPLReportsAReadFailure(t *testing.T) {
	root := commitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, _ := newCommitProvider(t, "feat(files): add hana.txt")
	defer server.Close()
	registry := ai.NewRegistry()
	registry.SetAPIKey("groq", "test-key")
	registry.SetBaseURL("groq", server.URL)
	settings := config.Settings{
		Model: "groq/llama-3.1-8b-instant",
		Roles: map[string]config.Profile{"commit": {Model: "groq/llama-3.1-8b-instant"}},
	}
	h := &harness{registry: registry, settings: &settings, root: root}
	var out strings.Builder
	if err := h.commitREPL(&out, bufio.NewReader(failingReader{})); err == nil ||
		!strings.Contains(err.Error(), "read approval") {
		t.Fatalf("read failure = %v, want the read error", err)
	}
	if got := commitCount(t, root); got != 1 {
		t.Errorf("commits = %d, want the initial commit only", got)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("stdin closed") }

func TestCommitREPLReturnsAnExecutionFailure(t *testing.T) {
	root := commitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server, _ := newCommitProvider(t, "feat(files): add hana.txt")
	defer server.Close()
	registry := ai.NewRegistry()
	registry.SetAPIKey("groq", "test-key")
	registry.SetBaseURL("groq", server.URL)
	settings := config.Settings{
		Model: "groq/llama-3.1-8b-instant",
		Roles: map[string]config.Profile{"commit": {Model: "groq/llama-3.1-8b-instant"}},
	}
	h := &harness{registry: registry, settings: &settings, root: root}
	var out strings.Builder
	if err := h.commitREPL(&out, bufio.NewReader(strings.NewReader("y\n"))); err == nil {
		t.Fatal("commitREPL reported success despite a rejecting hook")
	}
	if got := commitCount(t, root); got != 1 {
		t.Errorf("commits = %d, want no corrective commit", got)
	}
}
