package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// statusViewFixture is a populated card snapshot for the render tests.
func statusViewFixture() StatusView {
	return StatusView{
		Path:          "/work/proj",
		GitBranch:     "main",
		GitKnown:      true,
		GitStaged:     1,
		GitModified:   2,
		GitUntracked:  5,
		Slot:          "#12",
		Prompt:        "fix the failing test",
		TokensIn:      1200,
		TokensOut:     3400,
		Turns:         7,
		ContextUsed:   500,
		ContextWindow: 2000,
		Model:         "ollama/qwen2.5-coder:7b",
		Workspace:     "ASK",
		Policy:        "Read-Only",
	}
}

// card returns the plain card for a width, with the ANSI stripped.
func card(view StatusView, width, height int) string {
	return stripANSI(RenderStatusModal(view, DarkPalette(), width, height))
}

// cardLines is the plain card split into rows.
func cardLines(view StatusView, width, height int) []string {
	return strings.Split(card(view, width, height), "\n")
}

// The card carries every required section, the border-embedded title, the
// inter-section rules, and the structured footer bar.
func TestRenderStatusModalShowsSections(t *testing.T) {
	view := statusViewFixture()
	view.Engine = &StatusEngine{Indexed: true, Symbols: 4200, ASTReady: true}
	lines := cardLines(view, 80, 0)
	out := strings.Join(lines, "\n")

	for _, want := range []string{
		"SYSTEM STATUS",
		"WORKSPACE STATUS", "  Path    · /work/proj", "  Git     · main · dirty · 1 staged · 2 modified · 5 untracked",
		"SYMBOL ENGINE", "  Engine  · ✓ indexed · 4k symbols · AST ✓ ready",
		"SESSION CONTEXT", "  Slot    · #12", "  Prompt  · fix the failing test",
		"  Tokens  · ↑1k · ↓3k (4k total) · 7 turns", "  Context · 500 / 2k (25%)",
		"ENGINE & AUTHORITY", "  Model   · ollama/qwen2.5-coder:7b", "  Mode    · ASK · Read-Only",
		"Esc / q", "close modal",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("card missing %q:\n%s", want, out)
		}
	}

	// The title rides the top border, not a body row.
	if !strings.HasPrefix(lines[0], "┌") || !strings.HasSuffix(lines[0], "┐") || !strings.Contains(lines[0], "SYSTEM STATUS") {
		t.Errorf("title is not embedded in the top border: %q", lines[0])
	}
	// Every block is separated by a full-width rule between two borders.
	rules := 0
	for _, line := range lines[1 : len(lines)-1] {
		inner := strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│")
		if inner != "" && strings.Trim(inner, "─") == "" {
			rules++
		}
	}
	if rules < 3 {
		t.Errorf("card has %d inter-section rules, want at least 3:\n%s", rules, out)
	}
}

// The frame is rectangular: every row is exactly the card width, so the
// borders land in one column and the columns align.
func TestRenderStatusModalFrameAlignment(t *testing.T) {
	lines := cardLines(statusViewFixture(), 80, 0)
	width := CellWidth(lines[0])
	for i, line := range lines {
		if got := CellWidth(line); got != width {
			t.Errorf("row %d is %d cells wide, want %d: %q", i, got, width, line)
		}
	}
	// The value column is fixed: each label occupies the same 8-cell column
	// followed by " · ", so the values share one axis.
	for _, want := range []string{"  Path    · ", "  Git     · ", "  Slot    · ", "  Model   · "} {
		if !strings.Contains(strings.Join(lines, "\n"), want) {
			t.Errorf("label column not padded to the shared axis: %q", want)
		}
	}
}

// A nil engine hides the section rather than inventing an index; a wired one
// reports the index, symbol count, and AST readiness.
func TestRenderStatusModalEngineSection(t *testing.T) {
	view := statusViewFixture()
	if out := card(view, 80, 0); strings.Contains(out, "SYMBOL ENGINE") {
		t.Errorf("an absent engine rendered a section:\n%s", out)
	}
	view.Engine = &StatusEngine{Indexed: true, Symbols: 4200, ASTReady: true}
	out := card(view, 80, 0)
	for _, want := range []string{"SYMBOL ENGINE", "✓ indexed", "4k symbols", "AST ✓ ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("engine section missing %q:\n%s", want, out)
		}
	}
}

// Without measured counts the card reports the summary string alone: a failed
// status read must never be presented as a clean tree.
func TestRenderStatusModalGitFallback(t *testing.T) {
	view := statusViewFixture()
	view.GitKnown = false
	view.GitStatus = "3 changed"
	out := card(view, 80, 0)
	if !strings.Contains(out, "main · 3 changed") {
		t.Errorf("card = %q, want the summary fallback", out)
	}
	if strings.Contains(out, "clean") {
		t.Errorf("card invented a clean tree from an unknown status:\n%s", out)
	}
}

// A short viewport drops content, never the structured footer bar.
func TestRenderStatusModalKeepsFooterWhenShort(t *testing.T) {
	out := card(statusViewFixture(), 80, 8)
	if !strings.Contains(out, "Esc / q") || !strings.Contains(out, "close modal") {
		t.Errorf("short card lost the footer bar:\n%s", out)
	}
	if got := strings.Count(out, "\n") + 1; got > 8 {
		t.Errorf("card emitted %d rows, want at most 8", got)
	}
}

// /status opens the card as an overlay, hides the composer, and centers the
// bordered box.
func TestStatusModalOwnsTheScreen(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 100, Height: 30})
	p.state.Status.Model = "groq/m"
	p.state.Apply(InputChanged{Text: "typing"})

	Command{Name: "status"}.runOn(p)

	if p.state.StatusModal == nil {
		t.Fatal("/status did not open the card")
	}
	out := stripANSI(Render(p.state))
	if strings.Contains(out, "enter send") {
		t.Errorf("composer hints rendered under the card:\n%s", out)
	}
	if strings.Contains(out, "typing") {
		t.Errorf("composer text rendered under the card:\n%s", out)
	}
	centered := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "┌") && strings.Contains(line, "SYSTEM STATUS") && strings.HasPrefix(line, " ") {
			centered = true
		}
	}
	if !centered {
		t.Errorf("card is not centered:\n%s", out)
	}
}

// Every dismissal key closes the card without leaking into the composer or
// opening the quit confirmation.
func TestStatusModalClosesOnAnyKey(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEsc},
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyRunes, Runes: []rune("x")},
	} {
		p := NewProgram(nil, "test", 0)
		p.state.Apply(StatusModalOpened{View: statusViewFixture()})
		_, cmd := p.handleKey(key)
		if p.state.StatusModal != nil {
			t.Errorf("key %q did not close the card", key.String())
		}
		if cmd != nil {
			t.Errorf("key %q returned a command, want nil", key.String())
		}
		if p.state.QuitConfirm != nil {
			t.Errorf("key %q opened the quit confirmation", key.String())
		}
	}
}

// Apply is the only transition path: opening installs the snapshot and closing
// clears it, and /clear drops a card caught mid-session.
func TestStatusModalStateTransitions(t *testing.T) {
	s := New()
	s.Apply(StatusModalOpened{View: statusViewFixture()})
	if s.StatusModal == nil || s.StatusModal.Model != "ollama/qwen2.5-coder:7b" {
		t.Fatalf("StatusModal = %+v, want the installed snapshot", s.StatusModal)
	}
	s.Apply(Cleared{})
	if s.StatusModal != nil {
		t.Error("/clear left a status card open")
	}
	s.Apply(StatusModalOpened{View: statusViewFixture()})
	s.Apply(StatusModalClosed{})
	if s.StatusModal != nil {
		t.Error("close did not clear the card")
	}
}

// A wired symbol engine reaches the card; an unwired one leaves the section
// out entirely.
func TestStatusModalSymbolHook(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	Command{Name: "status"}.runOn(p)
	if out := stripANSI(Render(p.state)); strings.Contains(out, "SYMBOL ENGINE") {
		t.Errorf("an unwired engine rendered a section:\n%s", out)
	}

	q := NewProgram(nil, "test", 0)
	q.SetSymbolStatusFunc(func() StatusEngine {
		return StatusEngine{Indexed: true, Symbols: 120, ASTReady: true}
	})
	q.state.Apply(StatusModalOpened{View: q.statusView()})
	out := stripANSI(Render(q.state))
	if !strings.Contains(out, "SYMBOL ENGINE") || !strings.Contains(out, "✓ indexed") {
		t.Errorf("wired engine missing from the card:\n%s", out)
	}
}

// The authority label maps to a plain-language policy, and an unknown label
// reports nothing rather than guessing.
func TestStatusPolicy(t *testing.T) {
	cases := map[string]string{
		"ASK":   "Read-Only",
		"BUILD": "Interactive",
		"FUZZ":  "Auto",
		"":      "",
		"other": "",
	}
	for in, want := range cases {
		if got := statusPolicy(in); got != want {
			t.Errorf("statusPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}
