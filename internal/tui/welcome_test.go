package tui

import (
	"strings"
	"testing"
	"time"
)

// The welcome state is derived chrome, never transcript content. These tests
// pin the observable contract a user sees: it appears only on an empty
// conversation, never pollutes persisted history, and never returns over a
// resumed or in-progress transcript.
//
// The surface is a runtime identity surface: product identity, greeting,
// model, workspace, git state, capability modes, and real affordances —
// never marketing, tips, or invented state.

const welcomeInvite = "agent runtime for real codebases"

func TestWelcomeShowsOnEmptyTranscriptWithoutPersisting(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})

	out := Render(s)
	if !strings.Contains(out, welcomeInvite) {
		t.Fatalf("empty render lacks the identity surface: %q", out)
	}
	for _, want := range []string{"OHGO", "Quickstart", "ASK", "BUILD", "FUZZ", "/model", "/help", "@files", "$sup", "groq/m"} {
		if !strings.Contains(out, want) {
			t.Errorf("identity surface lacks %q: %q", want, out)
		}
	}
	if strings.Contains(out, "not checked") && !strings.Contains(out, "ohgo") {
		t.Fatalf("welcome render lost the header: %q", out)
	}
	// Rendering must not have inserted anything into the conversation.
	if len(s.Entries) != 0 {
		t.Errorf("entries = %d after render, want the welcome kept out of state", len(s.Entries))
	}
	if s.AssistantText() != "" {
		t.Errorf("assistant text = %q, want the welcome never treated as a reply", s.AssistantText())
	}
}

// The welcome is a quickstart, not a second status board: the model and git
// state it used to repeat are the header's facts, and a duplicate can drift
// from the real one.
func TestWelcomeOmitsHeaderMetadata(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(IdentityUpdated{User: "Jack", Greeting: "Good evening, Jack.", GitBranch: "main", GitStatus: "clean", Ready: "ready"})
	s.Apply(TerminalResized{Width: 100, Height: 24})

	welcome := renderWelcome(s, DarkPalette(), ComputeLayout(s))
	for _, gone := range []string{"groq/m", "main · clean", "no model", "git unavailable"} {
		if strings.Contains(welcome, gone) {
			t.Errorf("welcome repeated header metadata %q:\n%s", gone, welcome)
		}
	}
}

// SessionActive is the lifecycle gate: the first submitted prompt ends the
// welcome even if the transcript is momentarily empty, so the surface cannot
// outlive the session it introduces.
func TestWelcomeRequiresAnInactiveSession(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	if !showWelcome(s) {
		t.Fatal("welcome must show before any prompt")
	}
	s.SessionActive = true
	if showWelcome(s) {
		t.Error("welcome survived SessionActive=true")
	}
}

func TestWelcomeDisappearsOnceConversationStarts(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(UserSubmitted{Text: "hello"})

	if out := Render(s); strings.Contains(out, welcomeInvite) {
		t.Errorf("welcome survived a real prompt: %q", out)
	}
}

func TestWelcomeAbsentOverResumedTranscript(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryUser, UserText: "earlier work"}}})

	out := Render(s)
	if strings.Contains(out, welcomeInvite) {
		t.Errorf("welcome rendered over a resumed transcript: %q", out)
	}
	if !strings.Contains(out, "earlier work") {
		t.Errorf("resumed entry missing: %q", out)
	}
}

func TestWelcomeReturnsAfterClear(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(UserSubmitted{Text: "hello"})
	s.Apply(Cleared{})

	if out := Render(s); !strings.Contains(out, welcomeInvite) {
		t.Errorf("/clear did not restore the empty state: %q", out)
	}
}

func TestWelcomeSuppressedBehindOverlays(t *testing.T) {
	base := New()
	base.Status.Model = "groq/m"
	base.Apply(TerminalResized{Width: 100, Height: 24})

	picker := base
	picker.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: nil})
	if out := Render(picker); strings.Contains(out, welcomeInvite) {
		t.Errorf("welcome showed behind a picker: %q", out)
	}

	key := base
	key.Apply(ConnectKeyOpened{View: ConnectKeyView{Provider: ProviderRow{ID: "openrouter"}}})
	if out := Render(key); strings.Contains(out, welcomeInvite) {
		t.Errorf("welcome showed behind the key modal: %q", out)
	}

	approval := base
	approval.Apply(ApprovalAsked{ToolName: "bash", Choices: []string{"Yes", "No"}})
	if out := Render(approval); strings.Contains(out, welcomeInvite) {
		t.Errorf("welcome showed behind an approval: %q", out)
	}
}

func TestWelcomeIsCompactOnNarrowTerminals(t *testing.T) {
	wide := New()
	wide.Status.Model = "groq/m"
	wide.Apply(TerminalResized{Width: 120, Height: 30})

	narrow := New()
	narrow.Status.Model = "groq/m"
	narrow.Apply(TerminalResized{Width: 50, Height: 30})

	wideOut := Render(wide)
	narrowOut := Render(narrow)
	// Both tiers keep the semantic content: identity, modes, affordances.
	for _, out := range []string{wideOut, narrowOut} {
		for _, want := range []string{"OHGO", "ASK", "BUILD", "FUZZ", "/help"} {
			if !strings.Contains(out, want) {
				t.Errorf("tier lost essential content %q: %q", want, out)
			}
		}
	}
	// The wide tier boxes the composition; the narrow tier drops decoration
	// rather than clipping content.
	if !strings.Contains(wideOut, "┌") {
		t.Errorf("wide identity surface lost its frame: %q", wideOut)
	}
	if strings.Contains(narrowOut, "┌") {
		t.Errorf("narrow identity surface kept a frame that risks clipping: %q", narrowOut)
	}
}

// Header owns identity, model, and phase; the bottom hint bar owns session
// metrics and path. Neither may repeat the other.

func TestHeaderAndFooterDoNotDuplicateIdentity(t *testing.T) {
	s := New()
	s.Status.Model = "groq/some-model"
	s.Status.ContextUsed = 50
	s.Status.ContextWindow = 100
	s.Apply(WorkspaceUpdated{Cwd: "/work/project", HasCwd: true, Session: "#3", HasSession: true})
	s.Apply(TerminalResized{Width: 90, Height: 24})
	s.Apply(UserSubmitted{Text: "go"}) // busy -> phase renders

	out := Render(s)
	lines := strings.Split(out, "\n")
	header := lines[0]
	if !strings.Contains(header, "groq/some-model") {
		t.Errorf("header = %q, want the model there", header)
	}
	if !strings.Contains(header, "working") {
		t.Errorf("header = %q, want the runtime phase there", header)
	}
	// The footer only claims its row once the turn ends: while busy the
	// hint row belongs to cancellation keys, never to telemetry.
	s.Apply(AssistantDone{})
	footer := findStatusLine(t, Render(s), "#3")
	if strings.Contains(footer, "groq/some-model") {
		t.Errorf("footer = %q, want no duplicate model", footer)
	}
	if strings.Contains(strings.ToLower(footer), "ohgo") {
		t.Errorf("footer = %q, want no duplicate identity", footer)
	}
	if !strings.Contains(footer, "(50%)") {
		t.Errorf("footer = %q, want the context budget", footer)
	}
}

// Every known-size render must fill exactly the viewport: short content pads
// below the transcript (keeping the composer at the bottom) and overflow is
// trimmed, so the composer never drifts and regions never overlap.

func TestRenderFillsExactlyTheViewport(t *testing.T) {
	sizes := [][2]int{{120, 24}, {90, 20}, {80, 24}, {60, 24}, {50, 30}}
	for _, size := range sizes {
		empty := New()
		empty.Status.Model = "groq/m"
		empty.Apply(TerminalResized{Width: size[0], Height: size[1]})
		assertViewportFilled(t, size, Render(empty), "empty")

		full := New()
		full.Status.Model = "groq/m"
		full.Apply(TerminalResized{Width: size[0], Height: size[1]})
		for i := 0; i < 40; i++ {
			full.Apply(UserSubmitted{Text: "prompt"})
			full.Apply(AssistantDone{})
		}
		assertViewportFilled(t, size, Render(full), "full")
	}
}

func assertViewportFilled(t *testing.T, size [2]int, out, label string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != size[1] {
		t.Errorf("%s %dx%d rendered %d lines, want %d", label, size[0], size[1], len(lines), size[1])
	}
	for i, line := range lines {
		if CellWidth(line) > size[0] {
			t.Errorf("%s %dx%d line %d = %d cells, want <= %d", label, size[0], size[1], i, CellWidth(line), size[0])
		}
	}
}

// The composer must wrap long input within the viewport and keep the cursor
// visible, rather than overflowing or silently dropping the marker.

func TestComposerWrapsLongInput(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Apply(TerminalResized{Width: 40, Height: 24})
	long := "https://example.com/" + strings.Repeat("a", 90)
	s.Apply(ComposerChanged{Text: long, Cursor: len([]rune(long))})

	out := Render(s)
	if !strings.Contains(out, "▌") {
		t.Fatalf("cursor marker missing from a wrapped composer: %q", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 40 {
			t.Errorf("composer line %d = %d cells, want <= 40", i, CellWidth(line))
		}
	}
	// The typed text must survive intact across the fold.
	if got := strings.Count(out, "a"); got < 90 {
		t.Errorf("rendered %d of 90 'a' runes: input was lost in wrapping", got)
	}
}

// layoutComposer is the pure seam behind the composer view.

func TestLayoutComposerPreservesSpaces(t *testing.T) {
	rows, _, _, _ := layoutComposer("a    b", -1, 80)
	if len(rows) != 1 || rows[0] != "a    b" {
		t.Errorf("rows = %q, want internal runs of spaces preserved", rows)
	}
}

func TestLayoutComposerMapsCursorAcrossLines(t *testing.T) {
	rows, row, col, ok := layoutComposer("ab\ncd", 4, 80)
	if !ok || row != 1 || col != 1 {
		t.Errorf("cursor = (row %d, col %d, %v), want (1, 1, true) for offset 4", row, col, ok)
	}
	if len(rows) != 2 {
		t.Errorf("rows = %q, want two logical rows", rows)
	}
}

func TestLayoutComposerMapsCursorOnWrappedRow(t *testing.T) {
	// Width 4: "abcdefgh" folds to "abcd"/"efgh"; a cursor at offset 6 lands
	// on the second row.
	_, row, col, ok := layoutComposer("abcdefgh", 6, 4)
	if !ok || row != 1 || col != 2 {
		t.Errorf("cursor = (row %d, col %d, %v), want (1, 2, true)", row, col, ok)
	}
}

// A running tool and the footer animate on the same spinner frame.

func TestRunningToolUsesTheLiveSpinnerFrame(t *testing.T) {
	s := New()
	s.Status.Model = "groq/m"
	s.Status.Spinner = 3
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(ToolStarted{ID: "c1", Name: "read", At: time.Now()})
	s.Apply(ToolPreview{ID: "c1", Summary: "read a.go"})

	out := Render(s)
	if !strings.Contains(out, spinnerFrames[3]) {
		t.Errorf("render = %q, want the live spinner frame %q on the running tool", out, spinnerFrames[3])
	}
}

// Both themes render a coherent, identically shaped workbench.

func TestThemesRenderCoherently(t *testing.T) {
	build := func(theme string) State {
		s := New()
		s.Theme = theme
		s.Status.Model = "groq/m"
		s.Apply(TerminalResized{Width: 100, Height: 24})
		s.Apply(UserSubmitted{Text: "explain this"})
		return s
	}
	dark := Render(build(ThemeDark))
	light := Render(build(ThemeLight))
	if !strings.Contains(dark, "> ") || !strings.Contains(light, "> ") {
		t.Error("a theme lost the composer affordance")
	}
	if strings.Count(dark, "\n") != strings.Count(light, "\n") {
		t.Errorf("themes differ in layout: dark %d newlines, light %d",
			strings.Count(dark, "\n"), strings.Count(light, "\n"))
	}
	// The rendered text is plain under a non-TTY (lipgloss drops ANSI), so
	// theme application is asserted on the styles themselves.
	if PaletteFor(ThemeDark).User.GetForeground() == PaletteFor(ThemeLight).User.GetForeground() {
		t.Error("theme switch does not change the accent")
	}
	if ExtrasFor(ThemeDark).Header.GetForeground() == ExtrasFor(ThemeLight).Header.GetForeground() {
		t.Error("theme switch does not change the chrome styles")
	}
}
