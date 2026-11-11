package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Bare /theme opens the interactive chooser instead of printing the current
// palette; the card names both options and the keys that work.
func TestThemeCommandOpensPickerCard(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})

	Command{Name: "theme"}.runOn(p)

	if p.state.Picker == nil || p.state.Picker.Kind != PickerThemes {
		t.Fatalf("picker = %+v, want the theme chooser", p.state.Picker)
	}
	out := ansi.Strip(Render(p.state))
	for _, want := range []string{"THEME", "Auto", "Dark Theme", "Light Theme", "Enter", "Esc"} {
		if !strings.Contains(out, want) {
			t.Errorf("theme card = %q, want %q", out, want)
		}
	}
}

// The picker starts on the stored preference, not on the palette Auto happens
// to resolve to, and it names the effective appearance and its source.
func TestThemePickerShowsAutoPreference(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetAppearanceDetector(func() (string, bool) { return ThemeLight, true })
	p.SetTheme(ThemeAuto)
	p.state.Apply(TerminalResized{Width: 80, Height: 26})

	Command{Name: "theme"}.runOn(p)

	if p.state.Picker == nil || p.state.Picker.Cursor != 0 {
		t.Fatalf("picker = %+v, want Auto at the cursor", p.state.Picker)
	}
	if !p.state.Picker.Items[0].Active {
		t.Fatal("the Auto row is not marked current")
	}
	out := ansi.Strip(Render(p.state))
	for _, want := range []string{"Auto", "Theme: Auto → Light (system)"} {
		if !strings.Contains(out, want) {
			t.Errorf("theme card = %q, want %q", out, want)
		}
	}
}

// The chooser starts on the active palette, so Enter reaffirms the current
// look instead of flipping the screen without a deliberate move.
func TestThemePickerStartsOnCurrentTheme(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetTheme(ThemeLight)

	Command{Name: "theme"}.runOn(p)

	if p.state.Picker == nil {
		t.Fatal("theme picker did not open")
	}
	if p.state.Picker.Cursor != 2 {
		t.Errorf("cursor = %d, want the active light row", p.state.Picker.Cursor)
	}
	if !p.state.Picker.Items[2].Active {
		t.Error("the light row is not marked current")
	}
}

// Down + Enter applies and persists the highlighted palette, then returns to
// the composer.
func TestThemePickerSelectionAppliesAndPersists(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	saved := ""
	p.SetThemeChangedHandler(func(theme string) { saved = theme })

	Command{Name: "theme"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})

	if p.state.Theme != ThemeLight || saved != ThemeLight {
		t.Errorf("theme = %q saved = %q, want light applied and persisted", p.state.Theme, saved)
	}
	if p.state.Picker != nil {
		t.Error("theme picker stayed open after selection")
	}
}

// Switching the palette is a presentation event only: a live draft and caret
// position survive it byte for byte.
func TestThemeChangePreservesComposerDraft(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "keep me", Cursor: 4})

	p.applyTheme(ThemeLight)

	if p.state.Input != "keep me" || p.state.Cursor != 4 {
		t.Errorf("composer = (%q, %d), want the draft untouched", p.state.Input, p.state.Cursor)
	}
}

// The transcript projection is theme-dependent, so a switch must reflow the
// cached Markdown/code/border rows instead of leaving dark-theme escapes on
// screen under a light palette.
func TestThemeChangeReflowsTranscript(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state = syntheticDocument(20, 100)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	p.syncTranscript()

	p.applyTheme(ThemeLight)
	p.syncTranscript()

	if !p.transcriptIndex.Reflowed {
		t.Fatal("theme change did not reflow the transcript cache")
	}
	if p.transcriptIndex.LastRendered == 0 {
		t.Fatal("theme change rendered no blocks")
	}
}

// Esc dismisses the chooser without applying or persisting anything.
func TestThemePickerEscapeLeavesThemeUnchanged(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	saved := ""
	p.SetThemeChangedHandler(func(theme string) { saved = theme })

	Command{Name: "theme"}.runOn(p)
	keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEscape})

	if p.state.Theme != ThemeDark || saved != "" {
		t.Errorf("theme = %q saved = %q, want dark untouched and no save", p.state.Theme, saved)
	}
	if p.state.Picker != nil {
		t.Error("Esc left the theme picker open")
	}
}
