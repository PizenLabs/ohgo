package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A paste or a fast keystroke burst can arrive as one KeyRunes message whose
// text spells a key name. The composer must insert that text; consuming it as
// End/Home/Tab/Enter silently corrupts the user's prompt.
func TestKeyNamedTextIsInsertedVerbatim(t *testing.T) {
	for _, word := range []string{
		"end", "home", "tab", "enter", "up", "down", "left", "right", "esc",
	} {
		t.Run(word, func(t *testing.T) {
			p := NewProgram(nil, "test", 0)
			const prefix = "the "
			p.state.Input = prefix
			p.state.Cursor = len([]rune(prefix))

			p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(word)})

			if want := prefix + word; p.state.Input != want {
				t.Fatalf("input = %q, want %q", p.state.Input, want)
			}
			if want := len([]rune(prefix + word)); p.state.Cursor != want {
				t.Fatalf("cursor = %d, want %d", p.state.Cursor, want)
			}
		})
	}
}
