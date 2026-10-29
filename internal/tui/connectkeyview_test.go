package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// connectKeyFixture is an unconfigured provider so the modal must collect a
// key rather than lean on a detected credential.
func connectKeyFixture() ConnectKeyView {
	return ConnectKeyView{
		Provider: ProviderRow{
			ID:        "openrouter",
			Name:      "OpenRouter",
			Driver:    "OpenAI Chat",
			KeyDetail: "missing: set OPENROUTER_API_KEY",
			Count:     390,
		},
	}
}

// connectKeyCard returns the plain modal for a size, with ANSI stripped.
func connectKeyCard(view ConnectKeyView, width, height int) string {
	return stripANSI(RenderConnectKeyModal(view, DarkPalette(), width, height))
}

// connectKeyLines is the plain modal split into rows.
func connectKeyLines(view ConnectKeyView, width, height int) []string {
	return strings.Split(connectKeyCard(view, width, height), "\n")
}

// The modal carries the border-embedded title, the provider facts, the masked
// field, the hint, and the action bar.
func TestRenderConnectKeyModalShowsProviderAndField(t *testing.T) {
	view := connectKeyFixture()
	view.Key = "sk-or-secret"
	lines := connectKeyLines(view, 92, 0)
	out := strings.Join(lines, "\n")

	for _, want := range []string{
		"ADD API KEY · OPENROUTER",
		"CONNECT PROVIDER", "API KEY",
		"Provider", "OpenRouter (openrouter)",
		"Driver", "OpenAI Chat",
		"Status", "[×] missing credential",
		"Key", "••••••••••••",
		"paste the key for openrouter",
		"↵", "validate", "Esc", "cancel",
		"─",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("modal missing %q:\n%s", want, out)
		}
	}

	// The secret itself must never render.
	if strings.Contains(out, "sk-or-secret") {
		t.Errorf("the raw key leaked into the modal:\n%s", out)
	}

	// The title rides the top border, not a body row.
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") || !strings.Contains(lines[0], "ADD API KEY") {
		t.Errorf("title is not embedded in the top border: %q", lines[0])
	}
}

// A configured provider advertises the detected credential so the user can
// connect without pasting anything.
func TestRenderConnectKeyDetectedHint(t *testing.T) {
	view := connectKeyFixture()
	view.Provider.Configured = true
	view.Provider.KeyDetail = "environment (OPENROUTER_API_KEY)"
	out := connectKeyCard(view, 92, 0)
	if !strings.Contains(out, "leave blank to use environment (OPENROUTER_API_KEY)") {
		t.Errorf("detected credential not advertised:\n%s", out)
	}
	if strings.Contains(out, "paste the key for") {
		t.Errorf("a configured provider must not demand a paste:\n%s", out)
	}
}

// The frame is rectangular at every width.
func TestRenderConnectKeyModalFrameAlignment(t *testing.T) {
	view := connectKeyFixture()
	view.Key = strings.Repeat("k", 80)
	for _, width := range []int{34, 50, 92} {
		lines := connectKeyLines(view, width, 0)
		want := CellWidth(lines[0])
		for i, line := range lines {
			if got := CellWidth(line); got != want {
				t.Errorf("width %d row %d is %d cells, want %d: %q", width, i, got, want, line)
			}
		}
	}
}

// While a check is in flight the field locks and the frame says so.
func TestRenderConnectKeyValidating(t *testing.T) {
	view := connectKeyFixture()
	view.Key = "sk-1"
	view.Validating = true
	out := connectKeyCard(view, 92, 0)
	if !strings.Contains(out, "validating OpenRouter") {
		t.Errorf("in-flight check not named:\n%s", out)
	}
	if strings.Contains(out, "▌") {
		t.Errorf("the field cursor must hide while the check runs:\n%s", out)
	}
	if strings.Contains(out, "validate") {
		t.Errorf("the footer must not offer validate while locked:\n%s", out)
	}
}

// A failure renders in place, so the key can be fixed and retried.
func TestRenderConnectKeyError(t *testing.T) {
	view := connectKeyFixture()
	view.Error = "credential rejected"
	out := connectKeyCard(view, 92, 0)
	if !strings.Contains(out, "credential rejected") {
		t.Errorf("failure not shown in place:\n%s", out)
	}
	if !strings.Contains(out, "validate") {
		t.Errorf("a failed check must offer validate again:\n%s", out)
	}
}

// Enter submits the buffered key, and Esc cancels without ever creating a
// prompt.
func TestConnectKeyTypingAndCancel(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.flow = &connectFlow{provider: "openrouter"}
	p.state.Apply(ConnectKeyOpened{View: connectKeyFixture()})
	keyPress(p, runes("sk-"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyBackspace})
	if got := p.state.ConnectKey.Key; got != "sk" {
		t.Fatalf("buffer after backspace = %q, want sk", got)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.ConnectKey != nil || p.flow != nil {
		t.Error("escape must close the modal and abandon the flow")
	}
	if len(p.state.Entries) != 0 || len(p.history) != 0 {
		t.Errorf("a key leaked into the transcript or history: entries=%v history=%v", p.state.Entries, p.history)
	}
}

// ctrl+u clears the whole buffer.
func TestConnectKeyCtrlUClears(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ConnectKeyOpened{View: connectKeyFixture()})
	keyPress(p, runes("sk-1"))
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlU})
	if p.state.ConnectKey.Key != "" {
		t.Errorf("buffer = %q, want empty", p.state.ConnectKey.Key)
	}
}

// An empty buffer for an unconfigured provider reports in place instead of
// spawning a check that is guaranteed to fail.
func TestConnectKeyEmptyRequiresAPastedKey(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	called := false
	p.SetConnectHooks(ConnectHooks{
		Validate: func(_ context.Context, _, _ string) ([]string, string, error) {
			called = true
			return nil, "", nil
		},
	})
	p.flow = &connectFlow{provider: "openrouter"}
	p.state.Apply(ConnectKeyOpened{View: connectKeyFixture()})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if called {
		t.Error("an empty key with nothing detected still ran a check")
	}
	if p.state.ConnectKey == nil || p.state.ConnectKey.Error == "" {
		t.Error("an empty key must be reported in place")
	}
}
