package tui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/internal/tui/design"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

// Modal cards are painted surfaces. A card that leaves cells with the
// terminal's default background is see-through on a translucent terminal and
// exposes whatever the window behind it happened to show. These tests walk the
// emitted cells and require every one of them to carry a background.

// cellBackgrounds reports the active SGR background at each painted cell of a
// rendered row. The empty string means the terminal default.
func cellBackgrounds(line string) []string {
	var out []string
	bg := ""
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			if i+1 < len(line) && line[i+1] == '[' {
				j := i + 2
				for j < len(line) && (line[j] >= '0' && line[j] <= '9' || line[j] == ';') {
					j++
				}
				if j < len(line) && line[j] == 'm' {
					bg = applySGR(bg, line[i+2:j])
				}
				if j < len(line) {
					i = j + 1
					continue
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		i += size
		for n := runewidth.RuneWidth(r); n > 0; n-- {
			out = append(out, bg)
		}
	}
	return out
}

// applySGR returns the background state after one SGR parameter list.
func applySGR(current, params string) string {
	if params == "" {
		return ""
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0 || n == 49:
			current = ""
		case n == 38:
			// Skip the foreground's own parameters so an RGB component is
			// never mistaken for a background index.
			i = skipExtendedColor(fields, i)
		case n == 48:
			kind := ""
			if i+1 < len(fields) {
				kind = fields[i+1]
			}
			switch kind {
			case "2":
				if i+4 < len(fields) {
					r, _ := strconv.Atoi(fields[i+2])
					g, _ := strconv.Atoi(fields[i+3])
					b, _ := strconv.Atoi(fields[i+4])
					current = fmt.Sprintf("#%02x%02x%02x", r, g, b)
					i += 4
				}
			case "5":
				if i+2 < len(fields) {
					current = "256:" + fields[i+2]
					i += 2
				}
			}
		case n >= 40 && n <= 47:
			current = fmt.Sprintf("ansi:%d", n-40)
		case n >= 100 && n <= 107:
			current = fmt.Sprintf("ansi:%d", n-100+8)
		}
	}
	return current
}

// skipExtendedColor advances past the parameters of an extended colour.
func skipExtendedColor(fields []string, at int) int {
	if at+1 >= len(fields) {
		return at
	}
	switch fields[at+1] {
	case "2":
		return at + 4
	case "5":
		return at + 2
	}
	return at
}

// assertOpaque fails when any painted cell of the block falls back to the
// terminal's background.
func assertOpaque(t *testing.T, name, rendered string) {
	t.Helper()
	for row, line := range strings.Split(rendered, "\n") {
		for cell, bg := range cellBackgrounds(line) {
			if bg == "" {
				t.Fatalf("%s: row %d cell %d has no background; the card is transparent there: %q",
					name, row, cell, ansi.Strip(line))
			}
		}
	}
}

func useTrueColor(t *testing.T) {
	t.Helper()
	prev := termenv.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// A nested style ends with an SGR reset, which also clears the outer surface.
// The helper must re-assert the surface after that reset, not only at the start
// of the row.
func TestPaintSurfaceRefillsAfterNestedResets(t *testing.T) {
	useTrueColor(t)
	block := "\x1b[38;2;1;2;3mnested\x1b[0m   \nshort"
	out := paintSurface(block, design.Dark().SurfaceElevated)
	assertOpaque(t, "painted block", out)

	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("painted block kept %d rows, want 2", len(lines))
	}
	if CellWidth(lines[0]) != CellWidth(lines[1]) {
		t.Fatalf("painted rows are ragged: %d and %d cells", CellWidth(lines[0]), CellWidth(lines[1]))
	}
	if plain := ansi.Strip(lines[1]); plain != "short"+"    " {
		t.Fatalf("painted block changed the text: %q", plain)
	}
}

// Every raised card must cover its whole rectangle, including the padding
// after a nested reset and the border rows.
func TestRaisedCardsAreOpaque(t *testing.T) {
	useTrueColor(t)
	for _, theme := range []string{ThemeDark, ThemeLight} {
		t.Run(theme, func(t *testing.T) {
			quit := quitCardForTheme(&PendingApproval{Cursor: 0}, 52, theme)
			assertOpaque(t, "quit", quit)

			withHandoff := &TransitionPrompt{
				Question:       "Switch to ASK?",
				Reason:         "The request is read-only.",
				Current:        "BUILD",
				Proposed:       "ASK",
				CurrentAccent:  ModeAccentFor(theme, "BUILD"),
				ProposedAccent: ModeAccentFor(theme, "ASK"),
				Handoff:        &HandoffView{Facts: 2, Evidence: 1},
			}
			assertOpaque(t, "transition", transitionCardForTheme(withHandoff, 52, theme))

			assertOpaque(t, "restart", updateRestartCard(&UpdateRestartView{Version: "v1.2.3"}, PaletteFor(theme), 52))
			assertOpaque(t, "release", postUpdateCard(&PostUpdateView{
				Version: "v1.2.3",
				Title:   "What's new",
				Changes: []PostUpdateChange{{Type: "fixed", Title: "a fix"}},
				URL:     "https://example.com/release",
			}, PaletteFor(theme), 52))
		})
	}
}

// The shared card frame is the surface every picker and inspector reuses.
func TestSharedModalFrameIsOpaque(t *testing.T) {
	useTrueColor(t)
	for _, theme := range []string{ThemeDark, ThemeLight} {
		frame := renderModalFrameWithFooter("TITLE", []string{"row"}, "enter confirm", PaletteFor(theme), 40)
		assertOpaque(t, "shared frame "+theme, frame)
	}
}

// The model picker is a card too; its horizontal margins are the transparent
// canvas, but every cell inside the border must carry the raised surface.
func TestModelPickerIsOpaque(t *testing.T) {
	useTrueColor(t)
	for _, theme := range []string{ThemeDark, ThemeLight} {
		state := State{Theme: theme, Width: 100, Height: 40}
		plane := NewModelPlane(planeFixture())
		rect := PlaneModalRect(state.Width, state.Height)
		rect.X = 0
		state.Width = rect.Width
		rows := renderPlaneModal(plane, PaletteFor(theme), state, ComputeLayout(state), rect)
		assertOpaque(t, "model picker "+theme, strings.Join(rows, "\n"))
	}
}

// The authority selector is a raised dialog, not a wireframe over the
// terminal's background.
func TestPermissionsModalIsOpaque(t *testing.T) {
	useTrueColor(t)
	for _, theme := range []string{ThemeDark, ThemeLight} {
		card := renderPermissionsModalForTheme(0, ModeBuild, permissionsCardWidth(100), "", "", "", theme)
		assertOpaque(t, "permissions "+theme, card)
	}
}
