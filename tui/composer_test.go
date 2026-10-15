package tui

import (
	"strings"
	"testing"
)

// The composer helpers are pure (text, cursor) functions: every case below
// pins a transformation the fingers rely on, not an implementation path.

func TestInsertAtLandsOnRunes(t *testing.T) {
	// Byte offsets would split a multibyte rune; the cursor is a rune index.
	if got := insertAt("héllo", 1, "X"); got != "hXéllo" {
		t.Errorf("insertAt = %q, want hXéllo", got)
	}
	if got := insertAt("ab", 99, "X"); got != "abX" {
		t.Errorf("insertAt past the end = %q, want abX", got)
	}
	if got := insertAt("ab", -3, "X"); got != "Xab" {
		t.Errorf("insertAt before the start = %q, want Xab", got)
	}
}

func TestDeleteBeforeTakesOneRune(t *testing.T) {
	text, cursor := deleteBefore("héllo", 2)
	if text != "hllo" || cursor != 1 {
		t.Errorf("deleteBefore = (%q, %d), want (hllo, 1)", text, cursor)
	}
	if text, cursor := deleteBefore("ab", 0); text != "ab" || cursor != 0 {
		t.Errorf("deleteBefore at zero = (%q, %d), want unchanged", text, cursor)
	}
}

func TestDeleteAtTakesTheRuneUnderTheCursor(t *testing.T) {
	text, cursor := deleteAt("abcd", 1)
	if text != "acd" || cursor != 1 {
		t.Errorf("deleteAt = (%q, %d), want (acd, 1)", text, cursor)
	}
	if text, cursor := deleteAt("ab", 2); text != "ab" || cursor != 2 {
		t.Errorf("deleteAt at end = (%q, %d), want unchanged", text, cursor)
	}
}

func TestLineEdgesStopAtNewlines(t *testing.T) {
	text := "first\nsecond line"
	if got := lineStart(text, 12); got != 6 {
		t.Errorf("lineStart = %d, want 6", got)
	}
	if got := lineEnd(text, 7); got != 17 {
		t.Errorf("lineEnd = %d, want 17", got)
	}
}

func TestKillToStartKeepsTheNewline(t *testing.T) {
	// ctrl+u kills to the line start, not the buffer start: the line above
	// survives. Cursor 10 sits on the 'n' of "second".
	text, cursor := killToStart("first\nsecond", 10)
	if text != "first\nnd" || cursor != 6 {
		t.Errorf("killToStart = (%q, %d), want (first\\nnd, 6)", text, cursor)
	}
}

func TestKillToEndStopsAtTheNewline(t *testing.T) {
	text, cursor := killToEnd("first\nsecond", 1)
	if text != "f\nsecond" || cursor != 1 {
		t.Errorf("killToEnd = (%q, %d), want (f\\nsecond, 1)", text, cursor)
	}
}

func TestKillWordBackTakesWordAndSpace(t *testing.T) {
	// Paths delete in one press: the unit is a whitespace run, not a word
	// character class.
	text, cursor := killWordBack("read src/a.go ", 14)
	if text != "read " || cursor != 5 {
		t.Errorf("killWordBack = (%q, %d), want (read , 5)", text, cursor)
	}
	text, cursor = killWordBack("read ", 5)
	if text != "" || cursor != 0 {
		t.Errorf("killWordBack trailing space = (%q, %d), want empty", text, cursor)
	}
}

func TestLineAboveFallsBackAtTheFirstLine(t *testing.T) {
	if _, ok := lineAbove("single", 3); ok {
		t.Error("lineAbove on one line reported a move; the caller falls back to history on false")
	}
	if pos, ok := lineAbove("ab\ncdef", 6); !ok || pos != 2 {
		t.Errorf("lineAbove = (%d, %v), want (2, true): column clamped to the short line's end", pos, ok)
	}
}

func TestLineBelowFallsBackAtTheLastLine(t *testing.T) {
	if _, ok := lineBelow("ab\ncd", 5); ok {
		t.Error("lineBelow on the last line reported a move")
	}
	if pos, ok := lineBelow("abcdef\nxy", 4); !ok || pos != 9 {
		t.Errorf("lineBelow = (%d, %v), want (9, true): column clamped to the short line", pos, ok)
	}
}

// Vertical movement follows the wrapped display rows, not just logical lines:
// a single long line that folds across three rows is three up/down hops, and
// only the top and bottom rows fall back to history.
func TestVerticalMovementFollowsWrappedRows(t *testing.T) {
	text := "abcdefghij" // folded to 4 cells: abcd / efgh / ij
	if pos, ok := lineAboveWrapped(text, 9, 4); !ok || pos != 5 {
		t.Errorf("up from the wrapped tail = (%d, %v), want (5, true)", pos, ok)
	}
	if pos, ok := lineAboveWrapped(text, 5, 4); !ok || pos != 1 {
		t.Errorf("up across a wrapped row = (%d, %v), want (1, true)", pos, ok)
	}
	if _, ok := lineAboveWrapped(text, 1, 4); ok {
		t.Error("up on the top display row reported a move; the caller falls back to history")
	}
	if pos, ok := lineBelowWrapped(text, 1, 4); !ok || pos != 5 {
		t.Errorf("down across a wrapped row = (%d, %v), want (5, true)", pos, ok)
	}
	if _, ok := lineBelowWrapped(text, 9, 4); ok {
		t.Error("down on the bottom display row reported a move")
	}
}

// A wrapped row shorter than the one above clamps the column instead of
// pulling the cursor past its end, matching the logical-line behaviour.
func TestVerticalMovementClampsToShortWrappedRows(t *testing.T) {
	text := "abcdefghij" // abcd / efgh / ij
	if pos, ok := lineBelowWrapped(text, 3, 4); !ok || pos != 7 {
		t.Errorf("down from column 3 = (%d, %v), want (7, true)", pos, ok)
	}
	// The last row holds two runes: column 3 clamps to its end (offset 10).
	if pos, ok := lineBelowWrapped(text, 7, 4); !ok || pos != 10 {
		t.Errorf("down into the short row = (%d, %v), want (10, true): column clamped", pos, ok)
	}
}

// An explicit newline is a row boundary too: movement crosses from the tail of
// one wrapped block into the next without treating the whole buffer as one
// line.
func TestVerticalMovementCrossesWrappedAndNewlineRows(t *testing.T) {
	text := "hello world\nxy" // width 6 -> "hello " / "world" / "xy"
	if pos, ok := lineAboveWrapped(text, 8, 6); !ok || pos != 2 {
		t.Errorf("up from inside the wrapped row = (%d, %v), want (2, true)", pos, ok)
	}
	if pos, ok := lineAboveWrapped(text, 12, 6); !ok || pos != 6 {
		t.Errorf("up out of the new line = (%d, %v), want (6, true)", pos, ok)
	}
	if _, ok := lineAboveWrapped(text, 0, 6); ok {
		t.Error("up on the first row reported a move")
	}
	if pos, ok := lineBelowWrapped(text, 2, 6); !ok || pos != 8 {
		t.Errorf("down across the wrapped row = (%d, %v), want (8, true)", pos, ok)
	}
}

// The composer affordance names the live authority in lowercase: typing lands
// after "ask > ", "build > ", or "fuzz > ", never after a bare "> " once an
// authority owns the session. An unset authority keeps the generic prompt.
func TestComposerPrefixFollowsAuthority(t *testing.T) {
	cases := map[string]string{
		"ASK":   "ask > ",
		"BUILD": "build > ",
		"FUZZ":  "fuzz > ",
		"ask":   "ask > ",
		"":      "> ",
		"other": "> ",
	}
	for workspace, want := range cases {
		s := New()
		s.Status.Workspace = workspace
		if got := composerPrefix(s); got != want {
			t.Errorf("workspace %q prefix = %q, want %q", workspace, got, want)
		}
	}
	// The frame shows the same affordance the helper reports: stripped render
	// output contains the mode prefix, so the cursor visibly sits after it.
	s := New()
	s.Status.Model = "m"
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ComposerChanged{Text: "hi", Cursor: 2})
	if out := Render(s); !strings.Contains(out, "build > hi") {
		t.Errorf("render lacks the live mode prefix:\n%s", out)
	}
}

// Each authority keeps its own accent: ASK teal/primary, BUILD blue/accent,
// FUZZ magenta. Sharing one style would make the boundary unreadable.
func TestComposerPrefixStylesDifferPerMode(t *testing.T) {
	pal := DarkPalette()
	mk := func(workspace string) string {
		s := New()
		s.Status.Workspace = workspace
		return colorName(composerPrefixStyle(s, pal).GetForeground())
	}
	ask, build, fuzz := mk("ASK"), mk("BUILD"), mk("FUZZ")
	if ask != "#5FB3A1" {
		t.Errorf("ASK prefix = %q, want the teal primary", ask)
	}
	if build != "#7AA8C9" {
		t.Errorf("BUILD prefix = %q, want the blue accent", build)
	}
	if fuzz == ask || fuzz == build {
		t.Errorf("FUZZ prefix = %q, want its own magenta, not ASK %q or BUILD %q", fuzz, ask, build)
	}
}

// A wider mode prefix leaves fewer cells for text: the wrap budget subtracts
// the live prefix width, so "build > " folds earlier than "> " without
// overflowing, and the cursor marker survives the fold.
func TestComposerWrapAccountsForPrefixWidth(t *testing.T) {
	layout := Layout{ContentWidth: 20}
	if got := composerTextWidthFor(State{Status: Status{Workspace: "BUILD"}}, layout); got != 12 {
		t.Errorf("BUILD text width = %d, want 20-8=12", got)
	}
	if got := composerTextWidthFor(New(), layout); got != 18 {
		t.Errorf("generic text width = %d, want 20-2=18", got)
	}
	s := New()
	s.Status.Model = "m"
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	s.Apply(TerminalResized{Width: 40, Height: 24})
	long := strings.Repeat("a", 60)
	s.Apply(ComposerChanged{Text: long, Cursor: len([]rune(long))})
	out := Render(s)
	if !strings.Contains(out, "build > ") || !strings.Contains(out, "▌") {
		t.Fatalf("BUILD composer lost prefix or cursor:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 40 {
			t.Errorf("line %d = %d cells, want the widened prefix to wrap, not overflow", i, CellWidth(line))
		}
	}
}

// The two lines framing the prompt share the workspace prefix colour so the
// boundary reads as one: switching from ASK to BUILD to FUZZ must recolour
// the frame, and a modal approval must quiet it back to the dim separator so
// attention goes to the dialog.
func TestComposerRuleFollowsWorkspacePrefix(t *testing.T) {
	for _, theme := range []string{ThemeDark, ThemeLight} {
		pal := PaletteFor(theme)
		seen := map[string]string{}
		for _, workspace := range []string{"ASK", "BUILD", "FUZZ"} {
			s := New()
			s.Theme = theme
			s.Status.Workspace = workspace
			ruleFg := colorName(composerRuleStyle(s, pal).GetForeground())
			prefixFg := colorName(composerPrefixStyle(s, pal).GetForeground())
			if ruleFg != prefixFg {
				t.Errorf("%s %s: rule foreground = %q, want the prefix %q: the frame split from its prompt text", theme, workspace, ruleFg, prefixFg)
			}
			seen[workspace] = ruleFg
		}
		if seen["ASK"] == seen["BUILD"] || seen["ASK"] == seen["FUZZ"] || seen["BUILD"] == seen["FUZZ"] {
			t.Errorf("%s: rule colours %v share a foreground: switching workspaces left the frame unchanged", theme, seen)
		}
		modal := New()
		modal.Theme = theme
		modal.Status.Workspace = "BUILD"
		modal.Pending = &PendingApproval{ToolName: "bash", Summary: "run it", Choices: []string{"Yes", "No"}}
		if got, want := colorName(composerRuleStyle(modal, pal).GetForeground()), colorName(pal.Separator.GetForeground()); got != want {
			t.Errorf("%s: modal rule = %q, want the dim separator %q: approval lost focus to the composer frame", theme, got, want)
		}
	}
}
