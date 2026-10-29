package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/provider/creds"
)

// testHarness builds a backend with an isolated HOME and no credentials.
func testHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearProviderKeys(t)
	registry := ai.NewRegistry()
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	return newHarness(registry, nil, nil, nil, &agent.UsageLog{}, nil, t.TempDir(), model)
}

func TestProviderRowsNameSourcesWithoutSecrets(t *testing.T) {
	h := testHarness(t)
	t.Setenv("GROQ_API_KEY", "secret-value")

	rows := h.ProviderRows()
	if len(rows) == 0 {
		t.Fatal("no provider rows")
	}
	for _, row := range rows {
		if row.ID == "" || row.Name == "" || row.KeyDetail == "" {
			t.Errorf("row = %+v, want id, name, and key status", row)
		}
		if strings.Contains(row.KeyDetail, "secret-value") {
			t.Errorf("row = %+v, want sources named, never values", row)
		}
	}
	found := false
	for _, row := range rows {
		if row.ID == "groq" && !strings.Contains(row.KeyDetail, "GROQ_API_KEY") {
			t.Errorf("groq row = %+v, want the variable named", row)
		}
		if row.ID == "ollama" && row.KeyDetail != "none required" {
			t.Errorf("ollama row = %+v, want keyless stated", row)
		}
		if row.ID == "groq" {
			found = true
		}
	}
	if !found {
		t.Error("no groq row")
	}
}

func TestDisconnectRemovesOnlyFileCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	registry := ai.NewRegistry()
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	h := newHarness(registry, nil, nil, nil, &agent.UsageLog{}, nil, t.TempDir(), model)

	// Environment credentials are read-only: the answer names the source.
	t.Setenv("GROQ_API_KEY", "env-key")
	if _, err := h.Disconnect("groq"); err == nil || !strings.Contains(err.Error(), "environment") {
		t.Errorf("err = %v, want it to name the read-only source", err)
	}

	// File credentials are removed.
	t.Setenv("GROQ_API_KEY", "")
	store, err := creds.Open(filepath.Join(home, ".ohgo", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("groq", "file-key"); err != nil {
		t.Fatal(err)
	}
	outcome, err := h.Disconnect("groq")
	if err != nil || !strings.Contains(outcome, "removed") {
		t.Errorf("outcome = %q, err = %v, want the removal confirmed", outcome, err)
	}
	if _, _, err := registry.ResolveAPIKeySource("groq"); err == nil {
		t.Error("the deleted credential still resolves")
	}
}

func TestCompactHistoryNeedsHistory(t *testing.T) {
	h := testHarness(t)
	if _, _, err := h.CompactHistory(context.Background(), nil); err == nil {
		t.Error("an empty transcript compacted")
	}
}

func TestStatusReportIsLocal(t *testing.T) {
	h := testHarness(t)
	text := h.StatusReport(nil, 0, "dark", false)
	for _, want := range []string{"anthropic/claude-sonnet-4-5", "turns: 0", "theme: dark"} {
		if !strings.Contains(text, want) {
			t.Errorf("status = %q, want %q", text, want)
		}
	}
}

func TestConnectREPLEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearProviderKeys(t)

	// A scripted OpenRouter: discovery lists two models, chat is never hit.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[
			{"id":"test/alpha","context_length":10000,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]},
			{"id":"test/beta","context_length":20000,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}]}`)
	}))
	defer server.Close()

	registry := ai.NewRegistry()
	registry.SetBaseURL("openrouter", server.URL)
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	settings := config.Default()
	h := newHarness(registry, nil, nil, nil, &agent.UsageLog{}, &settings, t.TempDir(), model)

	// Provider 9th of 13 alphabetically is openrouter; choose by number is
	// brittle, so find it instead.
	var provIdx int
	for i, row := range h.ProviderRows() {
		if row.ID == "openrouter" {
			provIdx = i + 1
		}
	}
	if provIdx == 0 {
		t.Fatal("no openrouter row")
	}
	in := strings.NewReader(fmt.Sprintf("%d\npasted-key-123\n1\n", provIdx))
	var out strings.Builder
	if err := h.ConnectREPL(&out, in); err != nil {
		t.Fatalf("ConnectREPL: %v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{"connected to openrouter as test/alpha", "validating"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "pasted-key-123") {
		t.Errorf("the pasted key echoes in output:\n%s", text)
	}
	// The key persisted to the file store, the model to settings and the loop
	// is untouched here (nil loop in this harness) — assert the durable half.
	data, err := os.ReadFile(filepath.Join(home, ".ohgo", "credentials.json"))
	if err != nil || !strings.Contains(string(data), "pasted-key-123") {
		t.Errorf("credentials file = %s, %v: want the saved key", data, err)
	}
	if settings.Model != "openrouter/test/alpha" {
		t.Errorf("settings model = %q, want the chosen model saved", settings.Model)
	}
}

func TestResumeSessionMovesPointer(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create(dir, "m")
	if err != nil {
		t.Fatal(err)
	}
	registry := ai.NewRegistry()
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	h := newHarness(registry, nil, store, nil, &agent.UsageLog{}, nil, dir, model)

	history, notice, err := h.ResumeSession(sess.ID)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	_ = history
	if !strings.Contains(notice, "resumed session") {
		t.Errorf("notice = %q", notice)
	}
	if h.sess == nil || h.sess.ID != sess.ID {
		t.Error("the session pointer did not move")
	}
	if _, _, err := h.ResumeSession(99999); err == nil {
		t.Error("an unknown session resumed")
	}
}
