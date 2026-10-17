package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/prompt"
	"github.com/PizenLabs/ohgo/tokens"
	"github.com/PizenLabs/ohgo/workspace"
)

// supTestRoot builds a working directory that looks like a real project: an
// AGENTS.md project instruction file and a source file, each carrying a unique
// sentinel. A $sup request that leaks repository context will contain one of
// those sentinels, so the assertions can name the failure instead of trusting a
// byte count.
func supTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("# Project instructions\n\nSUP_REPO_SENTINEL_9F3A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\n// SUP_FILE_SENTINEL_7B2C\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// supTestRegistry points an Anthropic registry at a recording server.
func supTestRegistry(t *testing.T, server *recordingServer) *ai.Registry {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)
	return registry
}

// supTestOpts builds the production option shape for a $sup run.
func supTestOpts(root string, settings config.Settings) options {
	return options{root: root, model: catalog.MustResolve(supSonnet), settings: settings, autoApprove: true}
}

// TestSupRequestCarriesNoRepositoryOrToolContext is the architectural contract
// for the optimized $sup path: the request the provider receives is the minimal
// system instruction plus the query, and nothing else.
//
// It defends §2 (no repository context), §4 (no tools), §5 (the prompt is the
// dedicated one), and §14 (the request stays small) at once, by inspecting the
// bytes actually put on the wire rather than the code that built them.
func TestSupRequestCarriesNoRepositoryOrToolContext(t *testing.T) {
	server := newRecordingServer(t, ackStreamWire)
	registry := supTestRegistry(t, server)

	surface := newSupportSurface(supportBuilder(supTestOpts(supTestRoot(t), config.Default()), registry))
	if _, err := surface.ask(context.Background(), "what is golang", nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	body := server.last()
	if body == "" {
		t.Fatal("$sup made no provider request")
	}

	// The repository, its files, and its instructions are absent.
	for _, sentinel := range []string{"SUP_REPO_SENTINEL_9F3A", "SUP_FILE_SENTINEL_7B2C", "Project instructions"} {
		if strings.Contains(body, sentinel) {
			t.Errorf("$sup request leaked repository context %q:\n%s", sentinel, body)
		}
	}
	// The workspace agent's identity and its tool roster are absent.
	if strings.Contains(body, prompt.Identity) {
		t.Errorf("$sup request carried the workspace agent identity:\n%s", body)
	}
	if strings.Contains(body, `"tools"`) || strings.Contains(body, "input_schema") {
		t.Errorf("$sup request offered tools:\n%s", body)
	}
	for _, tool := range []string{"\"bash\"", "\"glob\"", "\"grep\"", "\"webfetch\""} {
		if strings.Contains(body, tool) {
			t.Errorf("$sup request mentions tool %s:\n%s", tool, body)
		}
	}
	// The dedicated minimal prompt and the query are present.
	if !strings.Contains(body, "quick-answer assistant") {
		t.Errorf("$sup request did not carry the minimal system prompt:\n%s", body)
	}
	if !strings.Contains(body, "what is golang") {
		t.Errorf("$sup request did not carry the query:\n%s", body)
	}

	var parsed struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(parsed.System) != 1 || parsed.System[0].Text != prompt.Sup {
		t.Errorf("$sup system prompt = %q, want exactly prompt.Sup", parsed.System)
	}
	if len(parsed.Messages) != 1 {
		t.Fatalf("first $sup request carried %d messages, want only the query", len(parsed.Messages))
	}
	if text := parsed.Messages[0].Content[0].Text; text != "what is golang" {
		t.Errorf("first message text = %q, want the query alone", text)
	}

	// A regression bound, not a tokenizer target: it is the project's own
	// payload estimate and only has to separate "minimal" from the ~1.9k the
	// agent path produced. The structural assertions above are the real
	// contract.
	if got := tokens.Count(body, tokens.ModeEstimate); got > 400 {
		t.Errorf("$sup request estimated at %d tokens, want a minimal payload (<=400)", got)
	}
}

// TestSupIsolatesFromWorkspaceConfiguration proves $sup does not inherit the
// workspace's context even when one is configured.
//
// The user's global system prompt and the workspace option are exactly what
// BUILD/ASK/FUZZ would carry; neither may reach a $sup request (§11, §13D).
func TestSupIsolatesFromWorkspaceConfiguration(t *testing.T) {
	server := newRecordingServer(t, ackStreamWire)
	registry := supTestRegistry(t, server)

	settings := config.Default()
	settings.SystemPrompt = "GLOBAL_WORKSPACE_SENTINEL_5D1E"
	opts := supTestOpts(supTestRoot(t), settings)
	ws, err := workspace.Parse("build")
	if err != nil {
		t.Fatal(err)
	}
	opts.workspace = ws

	surface := newSupportSurface(supportBuilder(opts, registry))
	if _, err := surface.ask(context.Background(), "what is golang", nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	body := server.last()
	if strings.Contains(body, "GLOBAL_WORKSPACE_SENTINEL_5D1E") {
		t.Errorf("$sup inherited the workspace system prompt:\n%s", body)
	}
	if strings.Contains(body, prompt.Identity) || strings.Contains(body, `"tools"`) {
		t.Errorf("$sup inherited workspace agent context:\n%s", body)
	}
}

// TestSupInsideARealRepositoryAddsNoContext runs $sup with its working
// directory set to this repository, where an AGENTS.md and source tree would
// have been injected by the old agent path.
//
// Being inside a repository must not pull repository context into a $sup
// request (§2, §13C). The markers are the stable headers the workspace prompt
// builder adds, not the repository's own text, so the contract is asserted
// without coupling the test to AGENTS.md's contents.
func TestSupInsideARealRepositoryAddsNoContext(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "AGENTS.md")); err != nil {
		t.Fatalf("expected a real repository root at %s: %v", repoRoot, err)
	}

	server := newRecordingServer(t, ackStreamWire)
	registry := supTestRegistry(t, server)
	surface := newSupportSurface(supportBuilder(supTestOpts(repoRoot, config.Default()), registry))
	if _, err := surface.ask(context.Background(), "what is golang", nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	body := server.last()
	for _, marker := range []string{"# Environment", "# Project instructions", "# OHGO project instructions", "Working directory:"} {
		if strings.Contains(body, marker) {
			t.Errorf("$sup inside a repository carried context marker %q:\n%s", marker, body)
		}
	}
	if strings.Contains(body, `"tools"`) {
		t.Errorf("$sup inside a repository offered tools:\n%s", body)
	}
}

// TestSupGreetingAnswersLocally proves the deterministic fast path: a greeting
// or acknowledgement is answered without spending a provider request (§7).
func TestSupGreetingAnswersLocally(t *testing.T) {
	server := newRecordingServer(t, ackStreamWire)
	registry := supTestRegistry(t, server)
	surface := newSupportSurface(supportBuilder(supTestOpts(t.TempDir(), config.Default()), registry))

	for query, want := range map[string]string{
		"hi":        "Hi.",
		"  Hello  ": "Hi.",
		"THANKS":    "You're welcome.",
		"ok":        "Got it.",
	} {
		answer, err := surface.ask(context.Background(), query, nil)
		if err != nil {
			t.Fatalf("ask(%q): %v", query, err)
		}
		if answer != want {
			t.Errorf("ask(%q) = %q, want %q", query, answer, want)
		}
	}
	if server.count() != 0 {
		t.Errorf("a greeting reached the provider (%d requests), want a local answer", server.count())
	}

	// A real question must still reach the model: the fast path matches the
	// whole normalized query, not a prefix of it.
	if _, err := surface.ask(context.Background(), "history of Go", nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if server.count() != 1 {
		t.Errorf("a non-greeting query made %d requests, want it sent to the model", server.count())
	}
}

// TestSupRequestUsesTheSupRoleModel proves the optimized path keeps model
// selection: the request is addressed to the independently configured SUP
// role, not the workspace model (§9, §13E).
func TestSupRequestUsesTheSupRoleModel(t *testing.T) {
	server := newRecordingServer(t, ackStreamWire)
	registry := supTestRegistry(t, server)

	settings := config.Default()
	settings.Model = supSonnet
	settings.AssignRole("sup", supHaiku, "")

	surface := newSupportSurface(supportBuilder(supTestOpts(t.TempDir(), settings), registry))
	if _, err := surface.ask(context.Background(), "what is golang", nil); err != nil {
		t.Fatalf("ask: %v", err)
	}
	body := server.last()
	if !strings.Contains(body, "claude-haiku-4-5") {
		t.Errorf("$sup request did not use the SUP role model:\n%s", body)
	}
	if strings.Contains(body, "claude-sonnet-4-5") {
		t.Errorf("$sup request used the workspace model:\n%s", body)
	}
}
