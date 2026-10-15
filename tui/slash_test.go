package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// TestPaletteTokenDetectsTriggers pins the multi-trigger rule: "/", "@", and
// "$" open the palette when they begin the word under the cursor. "/" and "$"
// are line-level directives, so a mid-sentence token stays prose; "@" may
// appear anywhere in a prompt.
func TestPaletteTokenDetectsTriggers(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		cursor  int
		trigger paletteTrigger
		query   string
		start   int
		end     int
		ok      bool
	}{
		{name: "command", input: "/mod", cursor: 4, trigger: paletteCommand, query: "mod", start: 0, end: 4, ok: true},
		{name: "file", input: "@pkg/x", cursor: 6, trigger: paletteFile, query: "pkg/x", start: 0, end: 6, ok: true},
		{name: "support", input: "$s", cursor: 2, trigger: paletteSupport, query: "s", start: 0, end: 2, ok: true},
		{name: "file mid prompt", input: "see @src/", cursor: 9, trigger: paletteFile, query: "src/", start: 4, end: 9, ok: true},
		{name: "at inside word", input: "foo@bar", cursor: 7, ok: false},
		{name: "command mid prompt", input: "hello /mod", cursor: 10, ok: false},
		{name: "support mid prompt", input: "hello $s", cursor: 8, ok: false},
		{name: "cursor before trigger", input: "@x", cursor: 0, ok: false},
		{name: "no trigger", input: "plain words", cursor: 5, ok: false},
	}
	for _, tt := range tests {
		trigger, query, start, end, ok := paletteToken(tt.input, tt.cursor)
		if ok != tt.ok {
			t.Errorf("%s: ok = %v, want %v", tt.name, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if trigger != tt.trigger || query != tt.query || start != tt.start || end != tt.end {
			t.Errorf("%s: got (%q,%q,%d,%d), want (%q,%q,%d,%d)",
				tt.name, trigger, query, start, end, tt.trigger, tt.query, tt.start, tt.end)
		}
	}
}

// TestSupportTriggerRendersInlinePalette proves "$" opens the same inline
// palette as "/" and "@", with the $sup shortcut and a SUPPORT header, and
// never a modal picker.
func TestSupportTriggerRendersInlinePalette(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 100, Height: 24})
	p.state.Apply(ComposerChanged{Text: "$", Cursor: 1})
	if p.state.Picker != nil {
		t.Fatalf("picker = %+v, want no modal overlay for $", p.state.Picker)
	}
	out := Render(p.state)
	if !strings.Contains(out, "SUPPORT") || !strings.Contains(out, "$sup") {
		t.Errorf("inline palette missing the $sup shortcut:\n%s", out)
	}
}

// TestSupportTriggerFilters proves a non-matching query closes the palette.
func TestSupportTriggerFilters(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "$zzz", Cursor: 4})
	if len(paletteEntriesFor(p.state)) != 0 {
		t.Fatalf("entries = %+v, want none for a non-matching query", paletteEntriesFor(p.state))
	}
}

// TestSupportSplicesOnAccept proves Enter/Tab splices "$sup" into the composer.
func TestSupportSplicesOnAccept(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(ComposerChanged{Text: "$s", Cursor: 2})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Input != "$sup " {
		t.Fatalf("input = %q, want the $sup shortcut spliced", p.state.Input)
	}
}

// The palette filters on a case-insensitive prefix of the command name, and
// hidden aliases never appear: what the palette shows is what dispatch accepts.
func TestFilterCommandsIsCaseInsensitivePrefixAndHidesAliases(t *testing.T) {
	got := map[string]bool{}
	for _, spec := range filterCommands("MOD") {
		got[spec.Name] = true
	}
	if !got["model"] {
		t.Errorf(`filterCommands("MOD") = %+v, want /model`, got)
	}
	if got["models"] {
		t.Error("hidden alias /models leaked into the palette")
	}
	if n := len(filterCommands("zzz")); n != 0 {
		t.Errorf("filterCommands(zzz) returned %d matches, want none", n)
	}
	if names := len(filterCommands("")); names == 0 {
		t.Error("bare palette must list every non-hidden command")
	}
}

// Cursor clamping: an empty list parks at zero, an overshoot lands on the last
// match, and an in-range index is untouched.
func TestClampPaletteCursor(t *testing.T) {
	cases := []struct{ cursor, matches, want int }{
		{-1, 3, 0},
		{0, 0, 0},
		{5, 3, 2},
		{2, 3, 2},
		{0, 3, 0},
	}
	for _, c := range cases {
		if got := ClampPaletteCursor(c.cursor, c.matches); got != c.want {
			t.Errorf("ClampPaletteCursor(%d,%d) = %d, want %d", c.cursor, c.matches, got, c.want)
		}
	}
}

// Cycling the cursor must not change the overlay's shape: every row is padded
// to one content width and the total line count is identical, so the palette
// never jitters as the highlight moves between an active and an inactive row.
func TestRenderSlashPaletteFixedWidthAcrossCursor(t *testing.T) {
	const width = 80
	var wantLines int
	for _, cursor := range []int{0, 1, 2, 3} {
		out := RenderSlashPalette("/", cursor, width)
		lines := strings.Split(out, "\n")
		if wantLines == 0 {
			wantLines = len(lines)
		} else if len(lines) != wantLines {
			t.Fatalf("cursor %d rendered %d lines, want the stable %d", cursor, len(lines), wantLines)
		}
		for i, line := range lines {
			if got := CellWidth(line); got != width {
				t.Fatalf("cursor %d line %d width = %d, want %d: %q", cursor, i, got, width, ansi.Strip(line))
			}
		}
	}
}

// Typing filter letters must not shift columns either: a filtered palette has
// the same per-row width as the unfiltered one at a given terminal width.
func TestRenderSlashPaletteFixedWidthAcrossFilter(t *testing.T) {
	for _, query := range []string{"/", "/m", "/mode"} {
		out := RenderSlashPalette(query, 0, 72)
		if out == "" {
			t.Fatalf("query %q rendered nothing", query)
		}
		for i, line := range strings.Split(out, "\n") {
			if got := CellWidth(line); got != 72 {
				t.Errorf("query %q line %d width = %d, want 72: %q", query, i, got, ansi.Strip(line))
			}
		}
	}
}

// The selection is visible without colour: the active row carries the marker,
// and no inactive row does.
func TestRenderSlashPaletteMarksTheActiveRow(t *testing.T) {
	out := ansi.Strip(RenderSlashPalette("/", 2, 80))
	if !strings.Contains(out, "▸ /build") {
		t.Fatalf("active row missing its marker:\n%s", out)
	}
	if strings.Contains(out, "▸ /ask") {
		t.Errorf("an inactive row carried the marker:\n%s", out)
	}
}

// The palette shows a bounded window that follows the cursor: the seventh match
// is off-screen at the top but scrolls into view with the highlight.
func TestRenderSlashPaletteWindowsToTheCursor(t *testing.T) {
	top := ansi.Strip(RenderSlashPalette("/", 0, 80))
	if strings.Contains(top, "/model") {
		t.Errorf("the window showed a match past the top PaletteMaxRows:\n%s", top)
	}
	scrolled := ansi.Strip(RenderSlashPalette("/", 6, 80))
	if !strings.Contains(scrolled, "▸ /model") {
		t.Errorf("scrolling to the seventh match did not reveal it:\n%s", scrolled)
	}
}

// No matches means no overlay: the caller clears the space rather than showing
// an empty frame.
func TestRenderSlashPaletteEmptyWhenNothingMatches(t *testing.T) {
	if out := RenderSlashPalette("/zzz-nope", 0, 80); out != "" {
		t.Errorf("no-match palette = %q, want empty", out)
	}
}

// The inline palette must emit exactly the rows the layout reserved, across
// every non-mini viewport. Trimming the match list can drop a whole group
// (header and rows together), which used to leave the box one row short and
// float the composer up into the gap; padding the interior to the reservation
// is what keeps the frame pinned to its height.
func TestSlashPaletteFillsItsReservedRows(t *testing.T) {
	for w := 40; w <= 200; w += 8 {
		for h := 8; h <= 60; h += 3 {
			s := New()
			s.Status.Model = "p/m"
			s.Apply(TerminalResized{Width: w, Height: h})
			s.Apply(UserSubmitted{Text: "go"})
			s.Apply(AssistantDone{})
			s.Apply(ComposerChanged{Text: "/", Cursor: 1})
			if ComputeLayout(s).Mode == LayoutMini {
				continue
			}
			if got := len(strings.Split(Render(s), "\n")); got != h {
				t.Fatalf("size %dx%d rendered %d lines, want the full %d-row viewport with the palette open", w, h, got, h)
			}
		}
	}
}
