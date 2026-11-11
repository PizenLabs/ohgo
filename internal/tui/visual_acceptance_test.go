package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// forceTrueColor pins the renderer so a token assertion inspects the emitted
// SGR roles rather than the test host's ambient colour profile.
func forceTrueColor(t *testing.T) {
	t.Helper()
	prev := termenv.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// representativeState builds the frame the visual acceptance pass inspects:
// identity chrome, a thought, a successful command, a failed command, a live
// command, and a markdown answer. Every value is runtime-backed; nothing is
// fabricated for presentation.
func representativeState(theme string) State {
	start := time.Now().Add(-10 * time.Second)
	s := New()
	s.Theme = theme
	s.ThemePreference = theme
	s.ThemeSource = ThemeSourceExplicit
	s.Status.Model = "anthropic/claude-sonnet-4-5"
	s.Status.Workspace = "BUILD"
	s.Status.WorkspaceAccent = ModeAccentBuild
	s.Status.Session = "session 42"
	s.ConnDetail = "api ready"
	s.ConnOK = true
	s.Status.Version = "v0.1.0"
	s.Status.UsageText = "↑2.4k · ↓310"
	s.Status.Busy = true
	s.TurnStartedAt = start
	s.TurnStartedAt = start
	s.Entries = []Entry{
		{Kind: EntryUser, UserText: "Audit the retry path and run the focused tests."},
		{Kind: EntryReasoning, Reasoning: &ReasoningEntry{
			Text:      "Inspect the retry helper and the tests that exercise cancellation.",
			StartedAt: start, EndedAt: start.Add(1200 * time.Millisecond), Closed: true,
		}},
		{Kind: EntryTool, Tool: &ToolEntry{
			ID: "t1", Name: "read", Invocation: "read internal/execution/shell/retry.go",
			Summary: "84 lines", Status: ToolDone,
			StartedAt: start.Add(2 * time.Second), EndedAt: start.Add(2*time.Second + 3*time.Millisecond),
		}},
		{Kind: EntryTool, Tool: &ToolEntry{
			ID: "t2", Name: "read", Invocation: "read internal/execution/shell/missing_test.go",
			Summary: "file not found", Status: ToolError,
			Text:      "open internal/execution/shell/missing_test.go: no such file or directory",
			StartedAt: start.Add(3 * time.Second), EndedAt: start.Add(3*time.Second + time.Millisecond),
		}},
		{Kind: EntryTool, Tool: &ToolEntry{
			ID: "t3", Name: "bash", Invocation: "bash go test ./internal/execution/shell/...",
			Summary: "running", Status: ToolRunning,
			StartedAt: start.Add(6800 * time.Millisecond),
		}},
		{Kind: EntryAssistant, AssistantText: "The focused suite is running. The retry helper keeps the original error via `%w`, so cancellation still matches with `errors.Is`.\n\n```go\nif err := run(ctx); err != nil {\n    return fmt.Errorf(\"retry: %w\", err)\n}\n```"},
	}
	// Apply after installing the fixture entries: the runtime-tracking cache is
	// seeded from the transcript on the first event and must see the live tool.
	s.Apply(TerminalResized{Width: 80, Height: 24})
	return s
}

// TestRepresentativeFramesFitBothAppearances is the terminal geometry gate:
// every rendered row fits the declared width in dark and light at the
// acceptance sizes, and the semantic facts survive stripping.
func TestRepresentativeFramesFitBothAppearances(t *testing.T) {
	forceTrueColor(t)
	type size struct{ w, h int }
	for _, theme := range []string{ThemeDark, ThemeLight} {
		for _, dim := range []size{{80, 24}, {120, 40}, {140, 45}, {60, 20}} {
			t.Run(fmt.Sprintf("%s-%dx%d", theme, dim.w, dim.h), func(t *testing.T) {
				s := representativeState(theme)
				s.Apply(TerminalResized{Width: dim.w, Height: dim.h})
				frame := Render(s)
				for i, line := range strings.Split(frame, "\n") {
					if got := CellWidth(line); got > dim.w {
						t.Errorf("line %d is %d cells, want <= %d: %q", i+1, got, dim.w, ansi.Strip(line))
					}
				}
				plain := ansi.Strip(frame)
				for _, want := range []string{"ohgo", "read internal/execution/shell/retry.go", "file not found", "go test", "build ›"} {
					if !strings.Contains(plain, want) {
						t.Errorf("frame lost %q:\n%s", want, plain)
					}
				}
				if dir := os.Getenv("OHGO_VISUAL_OUT"); dir != "" {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatalf("mkdir visual output: %v", err)
					}
					base := filepath.Join(dir, fmt.Sprintf("%s-%dx%d", theme, dim.w, dim.h))
					if err := os.WriteFile(base+".ansi", []byte(frame), 0o600); err != nil {
						t.Fatalf("write frame: %v", err)
					}
					if err := os.WriteFile(base+".txt", []byte(plain), 0o600); err != nil {
						t.Fatalf("write plain frame: %v", err)
					}
				}
			})
		}
	}
}

// TestFramesDegradeAcrossColorProfiles checks the same representative frame
// under truecolor, 256-colour, and no-colour terminals. The semantic text and
// geometry must survive; only escape density changes.
func TestFramesDegradeAcrossColorProfiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile termenv.Profile
	}{
		{"truecolor", termenv.TrueColor},
		{"256", termenv.ANSI256},
		{"none", termenv.Ascii},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := termenv.ColorProfile()
			lipgloss.SetColorProfile(tc.profile)
			t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
			for _, theme := range []string{ThemeDark, ThemeLight} {
				s := representativeState(theme)
				s.Apply(TerminalResized{Width: 80, Height: 24})
				frame := Render(s)
				plain := ansi.Strip(frame)
				for _, want := range []string{"read internal/execution/shell/retry.go", "file not found", "go test", "executing tools…"} {
					if !strings.Contains(plain, want) {
						t.Errorf("%s/%s lost %q", tc.name, theme, want)
					}
				}
				for i, line := range strings.Split(frame, "\n") {
					if got := CellWidth(line); got > 80 {
						t.Errorf("%s/%s line %d is %d cells", tc.name, theme, i+1, got)
					}
				}
			}
		})
	}
}

// TestThemePickerFrameFitsBothAppearances verifies the chooser itself in both
// appearances and at the narrow acceptance width. The card names the stored
// preference, the effective appearance, and its source.
func TestThemePickerFrameFitsBothAppearances(t *testing.T) {
	for _, theme := range []string{ThemeDark, ThemeLight} {
		for _, width := range []int{80, 60} {
			p := NewProgram(nil, "test", 0)
			p.SetAppearanceDetector(func() (string, bool) { return ThemeLight, true })
			p.SetTheme(theme)
			p.state.Apply(TerminalResized{Width: width, Height: 26})
			Command{Name: "theme"}.runOn(p)
			frame := Render(p.state)
			for i, line := range strings.Split(frame, "\n") {
				if got := CellWidth(line); got > width {
					t.Errorf("%s/%d line %d is %d cells: %q", theme, width, i+1, got, ansi.Strip(line))
				}
			}
			plain := ansi.Strip(frame)
			for _, want := range []string{"THEME", "Auto", "Dark Theme", "Light Theme", "Theme: " + titleTheme(theme) + " (explicit)"} {
				if !strings.Contains(plain, want) {
					t.Errorf("%s/%d card lost %q:\n%s", theme, width, want, plain)
				}
			}
			if dir := os.Getenv("OHGO_VISUAL_OUT"); dir != "" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir visual output: %v", err)
				}
				base := filepath.Join(dir, fmt.Sprintf("theme-picker-%s-%d", theme, width))
				if err := os.WriteFile(base+".txt", []byte(plain), 0o600); err != nil {
					t.Fatalf("write picker frame: %v", err)
				}
			}
		}
	}
}

// TestCompletedToolKeepsCommandTextNeutral: the status glyph carries the
// success/error role; the invocation itself stays body text, so a column of
// successful commands does not read as a wall of green.
func TestCompletedToolKeepsCommandTextNeutral(t *testing.T) {
	forceTrueColor(t)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	start := time.Now().Add(-time.Second)
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: start})
	s.Apply(ToolFinished{ID: "c1", Text: "package a\n", Summary: "1 line", At: start.Add(time.Millisecond)})
	s.Entries[0].Tool.DwellUntil = time.Time{}
	s.Entries[0].AppearedAt = time.Time{}
	out := Render(s)

	tok := DarkTokens()
	if !strings.Contains(out, rgbSeq(tok.Success)+"m✔") {
		t.Fatalf("success glyph did not use the success role:\n%q", out)
	}
	if !strings.Contains(out, rgbSeq(tok.TextPrimary)+"mread a.go") {
		t.Fatalf("command text did not use the primary text role:\n%q", out)
	}
	if strings.Contains(out, rgbSeq(tok.Success)+"mread a.go") {
		t.Fatalf("command text borrowed the success role:\n%q", out)
	}
}

// TestFailedToolKeepsDiagnosticsReadable: the branch marker alone carries the
// error role; the reason stays neutral body text.
func TestFailedToolKeepsDiagnosticsReadable(t *testing.T) {
	forceTrueColor(t)
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	at := time.Now().Add(-time.Second)
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read missing.go", At: at})
	s.Apply(ToolFinished{ID: "c1", IsError: true, Text: "open missing.go: no such file or directory\n", Summary: "no such file", At: at.Add(time.Millisecond)})
	s.Entries[0].Tool.DwellUntil = time.Time{}
	s.Entries[0].AppearedAt = time.Time{}
	out := Render(s)

	tok := DarkTokens()
	if !strings.Contains(ansi.Strip(out), "✗ read missing.go") {
		t.Fatalf("error row lost its glyph:\n%s", ansi.Strip(out))
	}
	if got := colorName(DarkPalette().ToolError.GetForeground()); got != tok.Error {
		t.Fatalf("error role = %q, want %q", got, tok.Error)
	}
	if !strings.Contains(out, rgbSeq(tok.TextPrimary)+"mread missing.go") {
		t.Fatalf("failed command text did not use the primary text role:\n%q", out)
	}
	if !strings.Contains(out, rgbSeq(tok.TextPrimary)+"mno such file") {
		t.Fatalf("error reason did not stay readable body text:\n%q", out)
	}
	if strings.Contains(out, rgbSeq(tok.Error)+"mread missing.go") {
		t.Fatalf("command text borrowed the error role:\n%q", out)
	}
}

// TestThoughtUsesCognitiveAmberNotOperationalWarning: reasoning is a
// cognitive stop, marked by its own amber pair. The header is bold amber and
// the body is the muted amber italic; neither borrows the operational warning
// role, so a thought never reads as degradation. The rendered frame must use
// both roles.
func TestThoughtUsesCognitiveAmberNotOperationalWarning(t *testing.T) {
	forceTrueColor(t)
	for _, theme := range []string{ThemeDark, ThemeLight} {
		s := New()
		s.Apply(TerminalResized{Width: 80, Height: 24})
		s.Theme = theme
		s.Entries = []Entry{{Kind: EntryReasoning, Reasoning: &ReasoningEntry{
			Text: "inspect the retry path", Expanded: true, Closed: true, EndedAt: time.Now(),
		}}}
		out := Render(s)
		tok := DarkTokens()
		pal := DarkPalette()
		if theme == ThemeLight {
			tok = LightTokens()
			pal = LightPalette()
		}
		if !strings.Contains(ansi.Strip(out), "- Thought") {
			t.Errorf("%s thought header disappeared:\n%s", theme, ansi.Strip(out))
		}
		if !strings.Contains(out, pal.Thought.Render("- Thought")) {
			t.Errorf("%s thought header did not use the amber header role", theme)
		}
		if !strings.Contains(out, pal.ThoughtBody.Render("inspect the retry path")) {
			t.Errorf("%s thought body did not use the amber body role", theme)
		}
		if got := colorName(pal.Thought.GetForeground()); got != tok.ThoughtHeader {
			t.Errorf("%s thought header role = %q, want %q", theme, got, tok.ThoughtHeader)
		}
		if !pal.Thought.GetBold() {
			t.Errorf("%s thought header is not bold", theme)
		}
		if !pal.ThoughtBody.GetItalic() {
			t.Errorf("%s thought body is not italic", theme)
		}
		if colorName(pal.Thought.GetForeground()) == colorName(pal.ToolRun.GetForeground()) {
			t.Errorf("%s thought shares the operational running role", theme)
		}
		// Thought amber and operational warning stay separate values in both
		// appearances: one is a cognitive stop, the other a degradation. The
		// design token test pins the distinction.
	}
}
