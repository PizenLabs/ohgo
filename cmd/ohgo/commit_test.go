package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/commit"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/tui"
	tea "github.com/charmbracelet/bubbletea"
)

// TestNewCommitServiceUsesTheDedicatedCommitRole proves /commit does not take
// the active workspace model. The COMMIT role is resolved through the same
// configuration authority the /model control plane writes, so an explicit
// commit assignment wins over ASK, BUILD, and the global default.
func TestNewCommitServiceUsesTheDedicatedCommitRole(t *testing.T) {
	settings := config.Settings{
		Model:  "anthropic/claude-haiku-4-5",
		Models: map[string]string{"ask": "openai/gpt-5", "build": "openai/gpt-5-mini"},
		Roles: map[string]config.Profile{
			"commit": {Model: "groq/llama-3.1-8b-instant"},
		},
	}
	service, err := newCommitService(context.Background(), t.TempDir(), settings, ai.NewRegistry())
	if err != nil {
		t.Fatalf("newCommitService: %v", err)
	}
	if service.Model.Provider != "groq" || service.Model.ID != "llama-3.1-8b-instant" {
		t.Errorf("commit model = %s/%s, want the dedicated role",
			service.Model.Provider, service.Model.ID)
	}
}

// TestResolveRoleModelKeepsCommitSeparate pins the underlying resolver: an
// explicit commit profile does not fall through to ASK, BUILD, a legacy
// workspace override, or the global model.
func TestResolveRoleModelKeepsCommitSeparate(t *testing.T) {
	settings := config.Settings{
		Model:  "anthropic/claude-haiku-4-5",
		Models: map[string]string{"ask": "openai/gpt-5", "build": "openai/gpt-5-mini"},
		Roles: map[string]config.Profile{
			"commit": {Model: "groq/llama-3.1-8b-instant"},
		},
	}
	model, _, err := resolveRoleModel(settings, config.RoleCommit, "/commit", catalog.Resolve)
	if err != nil {
		t.Fatalf("resolveRoleModel: %v", err)
	}
	ref := model.Provider + "/" + model.ID
	if ref != "groq/llama-3.1-8b-instant" {
		t.Errorf("commit role resolved to %q, want the explicit binding", ref)
	}
}

// TestCommitWithoutAnExplicitRoleUsesTheDocumentedFallback proves an
// unassigned COMMIT role keeps the existing precedence: the global model, not
// an unrelated workspace role and not the catalog default.
func TestCommitWithoutAnExplicitRoleUsesTheDocumentedFallback(t *testing.T) {
	settings := config.Settings{
		Model:  "groq/llama-3.1-8b-instant",
		Models: map[string]string{"ask": "groq/llama-3.3-70b-versatile", "build": "groq/llama-3.3-70b-versatile"},
		Roles:  map[string]config.Profile{},
	}
	service, err := newCommitService(context.Background(), t.TempDir(), settings, ai.NewRegistry())
	if err != nil {
		t.Fatalf("newCommitService: %v", err)
	}
	if service.Model.Provider != "groq" || service.Model.ID != "llama-3.1-8b-instant" {
		t.Errorf("fallback model = %s/%s, want the global model",
			service.Model.Provider, service.Model.ID)
	}
}

// TestCommitEndToEndUsesTheConfiguredModelAndCommitsOnlyTheApprovedScope is the
// integration path: type /commit in the real TUI state machine, hit a real
// OpenAI-compatible provider, render the proposal, approve it through the
// existing permission dialog, and verify exactly one scoped Git commit.
func TestCommitEndToEndUsesTheConfiguredModelAndCommitsOnlyTheApprovedScope(t *testing.T) {
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
		Model:  "groq/llama-3.3-70b-versatile",
		Models: map[string]string{"ask": "groq/llama-3.3-70b-versatile", "build": "groq/llama-3.3-70b-versatile"},
		Roles: map[string]config.Profile{
			"commit": {Model: "groq/llama-3.1-8b-instant"},
		},
	}
	hooks := commitHooks(root, &settings, registry)
	program := tui.NewProgram(nil, "test", 0)
	program.SetCommitHooks(hooks)

	// Type the command and dispatch it exactly as the composer does.
	program.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/commit")})
	_, proposeCmd := program.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if proposeCmd == nil {
		t.Fatal("/commit produced no proposal command")
	}
	if view := plainView(program); !strings.Contains(view, "Preparing commit") {
		t.Fatalf("view before the provider answered = %q, want immediate commit progress", view)
	}

	program.Update(proposeCmd())

	view := plainView(program)
	for _, want := range []string{
		"Commit Proposal",
		"feat(files): add hana.txt",
		"- create hana.txt",
		"hana.txt",
		"Create this commit?",
		"Cancel (default)",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("proposal view = %q, want %q", view, want)
		}
	}

	captured := capture.snapshot()
	if captured.Model != "llama-3.1-8b-instant" {
		t.Errorf("provider model = %q, want the configured COMMIT model", captured.Model)
	}
	if captured.Model == "llama-3.3-70b-versatile" {
		t.Error("/commit invoked the ASK/BUILD model instead of the COMMIT binding")
	}
	for _, want := range []string{"Repository evidence for this one commit", "hana.txt", "commit scope"} {
		if !strings.Contains(captured.Body, want) {
			t.Errorf("provider request = %q, want bounded evidence %q", captured.Body, want)
		}
	}

	// The resting cursor is Cancel; one move selects the affirmative action.
	program.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, executeCmd := program.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if executeCmd == nil {
		t.Fatal("approval produced no execution command")
	}
	if view := plainView(program); !strings.Contains(view, "Creating commit") {
		t.Errorf("view after approval = %q, want execution progress", view)
	}
	program.Update(executeCmd())

	view = plainView(program)
	if !strings.Contains(view, "Committed") || !strings.Contains(view, "feat(files): add hana.txt") {
		t.Errorf("final view = %q, want the verified result", view)
	}

	// Exactly one commit, with exactly the approved file and message.
	if got := commitCount(t, root); got != 2 {
		t.Fatalf("commits = %d, want the initial commit plus exactly one /commit", got)
	}
	files := gitOutput(t, root, "show", "--pretty=format:", "--name-only", "HEAD")
	if strings.TrimSpace(files) != "hana.txt" {
		t.Errorf("commit files = %q, want only hana.txt", files)
	}
	message := strings.TrimSpace(gitOutput(t, root, "log", "-1", "--pretty=%B"))
	if message != "feat(files): add hana.txt\n\n- create hana.txt" {
		t.Errorf("commit message = %q", message)
	}
}

// TestCommitRetryAndEditThroughTheTUI exercises both secondary actions against
// the real provider seam: "r" regenerates the proposal, "e" edits the message
// locally, and the approval still creates exactly one commit.
func TestCommitRetryAndEditThroughTheTUI(t *testing.T) {
	root := commitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	firstProposal := "feat(files): first proposal"
	secondProposal := "feat(files): second proposal"
	server, capture := newCommitProviderSequence(t, firstProposal, secondProposal)
	defer server.Close()

	registry := ai.NewRegistry()
	registry.SetAPIKey("groq", "test-key")
	registry.SetBaseURL("groq", server.URL)
	settings := config.Settings{
		Model: "groq/llama-3.1-8b-instant",
		Roles: map[string]config.Profile{
			"commit": {Model: "groq/llama-3.1-8b-instant"},
		},
	}
	program := tui.NewProgram(nil, "test", 0)
	program.SetCommitHooks(commitHooks(root, &settings, registry))

	// Initial proposal.
	program.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/commit")})
	_, proposeCmd := program.Update(tea.KeyMsg{Type: tea.KeyEnter})
	program.Update(proposeCmd())
	if view := plainView(program); !strings.Contains(view, "first proposal") {
		t.Fatalf("initial proposal = %q", view)
	}

	// Retry from the dialog: a second provider request, no Git mutation.
	_, retryCmd := program.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if retryCmd == nil {
		t.Fatal("retry key produced no proposal command")
	}
	if view := plainView(program); !strings.Contains(view, "Preparing commit") {
		t.Errorf("retry view = %q, want immediate progress", view)
	}
	program.Update(retryCmd())
	if view := plainView(program); !strings.Contains(view, "second proposal") {
		t.Fatalf("retried proposal = %q", view)
	}
	if got := capture.snapshot().Calls; got != 2 {
		t.Fatalf("provider calls = %d, want 2 after retry", got)
	}

	// Edit from the dialog: replace the message in the composer, validate, and
	// return to review without another provider call.
	program.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	for range []rune(secondProposal) {
		program.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	edited := "fix(files): edited hana.txt\n\n- keep the scope exact"
	program.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(edited)})
	program.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if view := plainView(program); !strings.Contains(view, "edited hana.txt") {
		t.Fatalf("edited proposal = %q", view)
	}
	if got := capture.snapshot().Calls; got != 2 {
		t.Errorf("provider calls = %d after edit, want no extra call", got)
	}

	// Approve and execute once.
	program.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, executeCmd := program.Update(tea.KeyMsg{Type: tea.KeyEnter})
	program.Update(executeCmd())
	if got := commitCount(t, root); got != 2 {
		t.Fatalf("commits = %d, want exactly one /commit commit", got)
	}
	message := strings.TrimSpace(gitOutput(t, root, "log", "-1", "--pretty=%B"))
	if message != edited {
		t.Errorf("commit message = %q, want the edited message", message)
	}
	if files := strings.TrimSpace(gitOutput(t, root, "show", "--pretty=format:", "--name-only", "HEAD")); files != "hana.txt" {
		t.Errorf("commit files = %q, want only hana.txt", files)
	}
}

// TestCommitPrepareSnapshotsTheRoleBinding proves the configuration is copied
// before the async request: a /model reassignment after /commit starts applies
// to the next invocation, not the one already in flight.
func TestCommitPrepareSnapshotsTheRoleBinding(t *testing.T) {
	root := commitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, capture := newCommitProvider(t, "feat(files): add hana.txt")
	defer server.Close()

	registry := ai.NewRegistry()
	registry.SetAPIKey("groq", "test-key")
	registry.SetBaseURL("groq", server.URL)
	settings := config.Settings{
		Model: "groq/llama-3.3-70b-versatile",
		Roles: map[string]config.Profile{
			"commit": {Model: "groq/llama-3.1-8b-instant"},
		},
	}
	hooks := commitHooks(root, &settings, registry)

	first, err := hooks.Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// The picker changes the binding after the proposal was prepared.
	settings.AssignRole("commit", "groq/llama-3.3-70b-versatile", "")
	second, err := hooks.Prepare()
	if err != nil {
		t.Fatalf("Prepare after reassignment: %v", err)
	}

	if _, err := first(context.Background()); err != nil {
		t.Fatalf("first proposal: %v", err)
	}
	if got := capture.snapshot().Model; got != "llama-3.1-8b-instant" {
		t.Errorf("in-flight request used %q, want the snapshotted COMMIT model", got)
	}
	if _, err := second(context.Background()); err != nil {
		t.Fatalf("second proposal: %v", err)
	}
	if got := capture.snapshot().Model; got != "llama-3.3-70b-versatile" {
		t.Errorf("next request used %q, want the reassigned COMMIT model", got)
	}
}

// TestCommitAbortsWhenTheRepositoryChangesAfterProposal drives the real TUI
// approval path and proves a post-proposal edit is reported, never absorbed.
func TestCommitAbortsWhenTheRepositoryChangesAfterProposal(t *testing.T) {
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
		Roles: map[string]config.Profile{
			"commit": {Model: "groq/llama-3.1-8b-instant"},
		},
	}
	program := tui.NewProgram(nil, "test", 0)
	program.SetCommitHooks(commitHooks(root, &settings, registry))

	program.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/commit")})
	_, proposeCmd := program.Update(tea.KeyMsg{Type: tea.KeyEnter})
	program.Update(proposeCmd())
	if view := plainView(program); !strings.Contains(view, "Commit Proposal") {
		t.Fatalf("proposal did not open: %q", view)
	}

	// The human edits the file after seeing the proposal.
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("changed after review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	program.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, executeCmd := program.Update(tea.KeyMsg{Type: tea.KeyEnter})
	program.Update(executeCmd())

	if view := plainView(program); !strings.Contains(view, "repository state changed after proposal") {
		t.Errorf("final view = %q, want the state-change abort", view)
	}
	if got := commitCount(t, root); got != 1 {
		t.Fatalf("commits = %d, want only the initial commit", got)
	}
}

// --- helpers ----------------------------------------------------------------

// capturedRequest is the provider body the test server observed.
type capturedRequest struct {
	Model string
	Body  string
	Calls int
}

// commitProvider serves one OpenAI-compatible SSE completion and records the
// request, so the test can prove which model was called and what evidence it
// received.
type commitProvider struct {
	mu      sync.Mutex
	last    capturedRequest
	calls   int
	replies []string
}

func (c *commitProvider) snapshot() capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	captured := c.last
	captured.Calls = c.calls
	return captured
}

func newCommitProvider(t *testing.T, message string) (*httptest.Server, *commitProvider) {
	t.Helper()
	return newCommitProviderSequence(t, message)
}

func newCommitProviderSequence(t *testing.T, messages ...string) (*httptest.Server, *commitProvider) {
	t.Helper()
	capture := &commitProvider{replies: append([]string(nil), messages...)}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		raw, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		capture.mu.Lock()
		capture.calls++
		index := capture.calls - 1
		if index >= len(capture.replies) {
			index = len(capture.replies) - 1
		}
		message := ""
		if len(capture.replies) > 0 {
			message = capture.replies[index]
		}
		capture.last = capturedRequest{Model: request.Model, Body: string(raw)}
		capture.mu.Unlock()

		content, _ := json.Marshal(message)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, chunk := range []string{
			fmt.Sprintf(`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":%s}}]}`, content),
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`,
			`data: [DONE]`,
		} {
			_, _ = fmt.Fprintln(w, chunk)
			_, _ = fmt.Fprintln(w)
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	return httptest.NewServer(mux), capture
}

// commitTestRepo creates a disposable repository with one initial commit.
func commitTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "base.txt")
	runGit(t, root, "commit", "-qm", "initial")
	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func commitCount(t *testing.T, root string) int {
	t.Helper()
	out := strings.TrimSpace(gitOutput(t, root, "rev-list", "--count", "HEAD"))
	var count int
	if _, err := fmt.Sscanf(out, "%d", &count); err != nil {
		t.Fatalf("parse commit count %q: %v", out, err)
	}
	return count
}

// ansiPattern strips SGR/CSI sequences so assertions see the visible text.
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

func plainView(program *tui.Program) string {
	return ansiPattern.ReplaceAllString(program.View(), "")
}

// commitRequestFacts is the provider-side view of one /commit request.
type commitRequestFacts struct {
	Model     string
	MaxTokens int
}

// TestCommitProposalTimeoutUsesTheResolvedCommitModel drives the real role
// resolution and provider transport, then proves the request carries the
// configured COMMIT model and the reasoning-safe budget when its own deadline
// expires.
func TestCommitProposalTimeoutUsesTheResolvedCommitModel(t *testing.T) {
	root := commitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	facts := make(chan commitRequestFacts, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &request)
		facts <- commitRequestFacts{Model: request.Model, MaxTokens: request.MaxTokens}
		<-r.Context().Done()
	}))
	defer server.Close()

	registry := ai.NewRegistry()
	registry.SetAPIKey("groq", "test-key")
	registry.SetBaseURL("groq", server.URL)
	settings := config.Settings{
		Model: "groq/llama-3.3-70b-versatile",
		Roles: map[string]config.Profile{
			"commit": {Model: "groq/llama-3.1-8b-instant"},
		},
	}
	service, err := newCommitService(context.Background(), root, settings, registry)
	if err != nil {
		t.Fatalf("newCommitService: %v", err)
	}
	// The deadline is the only test-specific knob; resolution still used the
	// dedicated COMMIT role above.
	service.Timeout = 200 * time.Millisecond
	var diagnostics commit.ProposalDiagnostics
	service.OnDiagnostics = func(d commit.ProposalDiagnostics) { diagnostics = d }

	started := time.Now()
	_, err = service.Propose(context.Background())
	if !errors.Is(err, commit.ErrModelTimeout) {
		t.Fatalf("Propose = %v, want a classified timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("elapsed = %s, want the configured 200ms deadline", elapsed)
	}
	if text := commit.AbortText(err); !strings.Contains(text, "timed out") {
		t.Errorf("AbortText = %q, want a timeout line", text)
	}

	select {
	case got := <-facts:
		if got.Model != "llama-3.1-8b-instant" {
			t.Errorf("provider model = %q, want the dedicated COMMIT model", got.Model)
		}
		if got.MaxTokens != commitMaxTokens {
			t.Errorf("max_tokens = %d, want %d", got.MaxTokens, commitMaxTokens)
		}
	case <-time.After(time.Second):
		t.Fatal("the provider never observed the request")
	}
	if diagnostics.Timeout != 200*time.Millisecond {
		t.Errorf("diagnostic timeout = %s, want the configured budget", diagnostics.Timeout)
	}
	if diagnostics.CancellationReason != "timeout" {
		t.Errorf("diagnostic reason = %q, want timeout", diagnostics.CancellationReason)
	}
	if diagnostics.Provider != "groq" || diagnostics.Model != "llama-3.1-8b-instant" {
		t.Errorf("diagnostic model = %s/%s, want the resolved COMMIT model", diagnostics.Provider, diagnostics.Model)
	}
	if diagnostics.Elapsed < diagnostics.Timeout {
		t.Errorf("diagnostic elapsed = %s, want at least the deadline", diagnostics.Elapsed)
	}
}

// TestCommitProposalDelayedResponseRendersInsideTheDeadline proves a slow but
// valid provider answer is not mistaken for a timeout.
func TestCommitProposalDelayedResponseRendersInsideTheDeadline(t *testing.T) {
	root := commitTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "hana.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		content, _ := json.Marshal("feat(files): add hana.txt\n\n- create hana.txt")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s}}]}\n\n", content)
		_, _ = fmt.Fprintln(w, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, `data: [DONE]`)
		_, _ = fmt.Fprintln(w)
	}))
	defer server.Close()

	registry := ai.NewRegistry()
	registry.SetAPIKey("groq", "test-key")
	registry.SetBaseURL("groq", server.URL)
	settings := config.Settings{
		Model: "groq/llama-3.3-70b-versatile",
		Roles: map[string]config.Profile{
			"commit": {Model: "groq/llama-3.1-8b-instant"},
		},
	}
	service, err := newCommitService(context.Background(), root, settings, registry)
	if err != nil {
		t.Fatalf("newCommitService: %v", err)
	}
	service.Timeout = 2 * time.Second
	request, err := service.Propose(context.Background())
	if err != nil {
		t.Fatalf("a delayed response inside the deadline failed: %v", err)
	}
	if request.Proposal.Subject() != "feat(files): add hana.txt" {
		t.Errorf("subject = %q", request.Proposal.Subject())
	}
}
