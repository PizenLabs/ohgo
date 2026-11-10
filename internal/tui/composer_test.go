package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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
// after "ask › ", "build › ", or "fuzz › ", never after a bare "› " once an
// authority owns the session. An unset authority keeps the generic pointer.
func TestComposerPrefixFollowsAuthority(t *testing.T) {
	cases := map[string]string{
		"ASK":   "ask › ",
		"BUILD": "build › ",
		"FUZZ":  "fuzz › ",
		"ask":   "ask › ",
		"":      "› ",
		"other": "› ",
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
	if out := Render(s); !strings.Contains(out, "build › hi") {
		t.Errorf("render lacks the live mode prefix:\n%s", out)
	}
}

// The rendered prefix keeps hue identity on the mode word alone: the pointer
// and spacing stay muted, so the prompt is not a solid colored run.
func TestComposerPrefixColorsOnlyTheModeWord(t *testing.T) {
	pal := PaletteFor(ThemeDark)
	s := New()
	s.Theme = ThemeDark
	s.Status.Workspace = "BUILD"

	styled := composerPrefixText(s, pal)
	if got := ansi.Strip(styled); got != "build › " {
		t.Fatalf("prefix = %q, want the refined pointer", got)
	}
	if !strings.Contains(styled, composerPrefixStyle(s, pal).Render("build")) {
		t.Errorf("prefix = %q, want only the mode word in its accent", styled)
	}
	if !strings.Contains(styled, pal.Muted.Render(" › ")) {
		t.Errorf("prefix = %q, want the pointer and spacing muted", styled)
	}
}

// Each authority keeps its own accent: ASK teal, BUILD blue, FUZZ violet.
// Sharing one style would make the boundary unreadable.
func TestComposerPrefixStylesDifferPerMode(t *testing.T) {
	pal := DarkPalette()
	mk := func(workspace string) string {
		s := New()
		s.Status.Workspace = workspace
		return colorName(composerPrefixStyle(s, pal).GetForeground())
	}
	ask, build, fuzz := mk("ASK"), mk("BUILD"), mk("FUZZ")
	if ask != ModeAccentAsk {
		t.Errorf("ASK prefix = %q, want the fixed teal %q", ask, ModeAccentAsk)
	}
	if build != ModeAccentBuild {
		t.Errorf("BUILD prefix = %q, want the fixed blue %q", build, ModeAccentBuild)
	}
	if fuzz != ModeAccentFuzz {
		t.Errorf("FUZZ prefix = %q, want the fixed violet %q", fuzz, ModeAccentFuzz)
	}
	if fuzz == ask || fuzz == build {
		t.Errorf("FUZZ prefix = %q, want its own violet, not ASK %q or BUILD %q", fuzz, ask, build)
	}
}

// The semantic accents are OHGO's fixed identity pairs: one signature hue per
// authority, deepened for bright terminals.
func TestModeAccentsUseSignatureThemePairs(t *testing.T) {
	cases := map[string][2]string{
		"ASK":   {"#5FB3A1", "#1F6F61"},
		"BUILD": {"#61AFEF", "#2B6CB0"},
		"FUZZ":  {"#C678DD", "#8C4398"},
	}
	for workspace, pair := range cases {
		if got := ModeAccentFor(ThemeDark, workspace); got != pair[0] {
			t.Errorf("%s dark accent = %q, want %q", workspace, got, pair[0])
		}
		if got := ModeAccentFor(ThemeLight, workspace); got != pair[1] {
			t.Errorf("%s light accent = %q, want %q", workspace, got, pair[1])
		}
	}
}

// The live mode colour keeps one hue per authority but takes its depth from
// the palette: prefix and caret must agree on both themes, and the light form
// must actually differ. The surrounding frame stays on the neutral border.
func TestComposerModeAccentsAdaptPerTheme(t *testing.T) {
	for _, workspace := range []string{"ASK", "BUILD", "FUZZ"} {
		seen := map[string]string{}
		for _, theme := range []string{ThemeDark, ThemeLight} {
			want := ModeAccentFor(theme, workspace)
			if want == "" {
				t.Fatalf("%s has no semantic accent", workspace)
			}
			seen[theme] = want
			pal := PaletteFor(theme)
			s := New()
			s.Theme = theme
			s.Status.Workspace = workspace

			if got := colorName(composerPrefixStyle(s, pal).GetForeground()); got != want {
				t.Errorf("%s %s prefix = %q, want the mode accent %q", theme, workspace, got, want)
			}
			if got := colorName(composerCursorStyle(s, pal).GetForeground()); got != want {
				t.Errorf("%s %s caret = %q, want the mode accent %q", theme, workspace, got, want)
			}
			if got, wantFrame := colorName(composerRuleStyle(s, pal).GetForeground()), colorName(pal.Separator.GetForeground()); got != wantFrame {
				t.Errorf("%s %s frame = %q, want the neutral border %q", theme, workspace, got, wantFrame)
			}
		}
		if seen[ThemeDark] == seen[ThemeLight] {
			t.Errorf("%s keeps %q in both themes, want a darkened light-screen form", workspace, seen[ThemeDark])
		}
	}
}

// Without an authority the composer keeps the theme's neutral prompt, so a
// missing mode never looks like a real one.
func TestComposerModeAccentFallsBackWithoutAuthority(t *testing.T) {
	for _, theme := range []string{ThemeDark, ThemeLight} {
		pal := PaletteFor(theme)
		s := New()
		s.Theme = theme
		if got, want := colorName(composerPrefixStyle(s, pal).GetForeground()), colorName(pal.Prompt.GetForeground()); got != want {
			t.Errorf("%s: no-authority prefix = %q, want the theme prompt %q", theme, got, want)
		}
		if got, want := colorName(composerCursorStyle(s, pal).GetForeground()), colorName(pal.Cursor.GetForeground()); got != want {
			t.Errorf("%s: no-authority caret = %q, want the theme cursor %q", theme, got, want)
		}
	}
}

// A wider mode prefix leaves fewer cells for text: the wrap budget subtracts
// the live prefix width, so "build › " folds earlier than "› " without
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
	if !strings.Contains(out, "build › ") || !strings.Contains(out, "▌") {
		t.Fatalf("BUILD composer lost prefix or cursor:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if CellWidth(line) > 40 {
			t.Errorf("line %d = %d cells, want the widened prefix to wrap, not overflow", i, CellWidth(line))
		}
	}
}

// The composer frame is a neutral boundary on every authority and theme: the
// mode hue belongs only to the prefix word, never to the box. Switching ASK →
// BUILD → FUZZ must leave the rule untouched while the prefix accents stay
// distinct.
func TestComposerRuleStaysNeutralAcrossModes(t *testing.T) {
	for _, theme := range []string{ThemeDark, ThemeLight} {
		pal := PaletteFor(theme)
		wantRule := colorName(pal.Separator.GetForeground())
		seenPrefix := map[string]string{}
		for _, workspace := range []string{"ASK", "BUILD", "FUZZ"} {
			s := New()
			s.Theme = theme
			s.Status.Workspace = workspace
			if got := colorName(composerRuleStyle(s, pal).GetForeground()); got != wantRule {
				t.Errorf("%s %s: rule = %q, want the neutral border %q", theme, workspace, got, wantRule)
			}
			seenPrefix[workspace] = colorName(composerPrefixStyle(s, pal).GetForeground())
		}
		if seenPrefix["ASK"] == seenPrefix["BUILD"] || seenPrefix["ASK"] == seenPrefix["FUZZ"] || seenPrefix["BUILD"] == seenPrefix["FUZZ"] {
			t.Errorf("%s: prefix colours %v share a foreground: mode identity was lost", theme, seenPrefix)
		}
	}
}

// The upper composer rule names the live session by title alone: the slot is
// already in the footer, and the protection marker stays optional. A new
// session with no title must still read cleanly rather than emitting an empty
// bracket.
func TestComposerBorderHeaderNamesLiveSession(t *testing.T) {
	fresh := New()
	fresh.Apply(TerminalResized{Width: 80, Height: 24})
	if out := ansi.Strip(Render(fresh)); !strings.Contains(out, "[New Session] ───") {
		t.Fatalf("fresh composer border lacks the placeholder:\n%s", out)
	}

	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(WorkspaceUpdated{HasSession: true, Session: "#12"})
	s.Apply(SessionTitleUpdated{ID: 12, Title: "Fix the failing parser"})
	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "[Fix failing parser] ───") {
		t.Fatalf("composer border lacks the sanitized title:\n%s", out)
	}
	if strings.Contains(out, "#12:") {
		t.Fatalf("composer border duplicated the slot id:\n%s", out)
	}

	s.Apply(SessionProtectionUpdated{ID: 12, Protected: true})
	if out := ansi.Strip(Render(s)); !strings.Contains(out, "[Fix failing parser] ★ ───") {
		t.Fatalf("composer border lacks the protected marker:\n%s", out)
	}
}

// A session-scoped event must only claim the border for the live slot: a
// backfilled title for another roster row must not rename the conversation
// the user is looking at.
func TestComposerBorderIgnoresOtherSessionEvents(t *testing.T) {
	s := New()
	s.Apply(WorkspaceUpdated{HasSession: true, Session: "#7"})
	s.Apply(SessionTitleUpdated{ID: 8, Title: "wrong session"})
	s.Apply(SessionProtectionUpdated{ID: 8, Protected: true})
	if s.Status.SessionTitle != "" || s.Status.SessionProtected {
		t.Fatalf("foreign event mutated live meta: title=%q protected=%v", s.Status.SessionTitle, s.Status.SessionProtected)
	}
	s.Apply(SessionTitleUpdated{ID: 7, Title: "right session"})
	s.Apply(SessionProtectionUpdated{ID: 7, Protected: true})
	if s.Status.SessionTitle != "right session" || !s.Status.SessionProtected {
		t.Fatalf("live event missed the border: title=%q protected=%v", s.Status.SessionTitle, s.Status.SessionProtected)
	}
}

// The border title is bounded to the available cells on every width: it may
// shrink and ellipsize, but it must never overflow or push the closing rule
// off-screen.
func TestComposerBorderHeaderWidthSafety(t *testing.T) {
	state := State{Status: Status{
		Session:      "#12",
		SessionTitle: "alpha beta gamma delta epsilon zeta",
	}}
	for _, width := range []int{20, 30, 40, 60, 79, 120} {
		got := composerBorderTitle(state, width)
		if got == "" || CellWidth(got) != width {
			t.Errorf("width %d: %q is %d cells, want exactly %d", width, got, CellWidth(got), width)
		}
		if !strings.HasPrefix(got, "─") || !strings.HasSuffix(got, "───") {
			t.Errorf("width %d: %q lost the framing boundary", width, got)
		}
		// Right alignment: the label block sits against the trailing cap,
		// never at the left edge.
		if open := strings.Index(got, " ["); open < 1 || strings.Trim(got[:open], "─") != "" {
			t.Errorf("width %d: %q is not right-aligned", width, got)
		}
	}
}

// Long titles are cut between words with one ellipsis, never mid-word and
// never with a dangling space before the bracket.
func TestComposerBorderHeaderTruncatesAtWordBoundary(t *testing.T) {
	state := State{Status: Status{
		Session:      "#12",
		SessionTitle: "alpha beta gamma delta epsilon zeta",
	}}
	got := composerBorderTitle(state, 30)
	if !strings.Contains(got, "alpha beta gamma…") {
		t.Errorf("border = %q, want a word-bounded ellipsis", got)
	}
	if strings.Contains(got, "delta…") || strings.Contains(got, "… ]") {
		t.Errorf("border = %q, want no partial word or dangling space", got)
	}
	// A title that already fits keeps every word and gains no ellipsis.
	short := State{Status: Status{Session: "#1", SessionTitle: "short title"}}
	if got := composerBorderTitle(short, 80); strings.Contains(got, "…") {
		t.Errorf("fitting title = %q, want no ellipsis", got)
	}
}

// composerScrollOffset is the cursor-following window: the caret row is always
// inside [offset, offset+maxRows), and the window does not jitter while the
// caret moves within it. Each case also asserts the contract, so a bad table
// entry cannot pass by matching a stale want.
func TestComposerScrollOffsetKeepsCursorInWindow(t *testing.T) {
	cases := []struct {
		name              string
		prev, cursor, all int
		max               int
		want              int
	}{
		{"content fits", 0, 2, 3, 6, 0},
		{"at the bottom edge", 0, 5, 6, 6, 0},
		{"one past the bottom", 0, 6, 7, 6, 1},
		{"deep overflow", 0, 10, 12, 6, 5},
		{"inside the window stays", 5, 10, 12, 6, 5},
		{"within the window does not jitter", 5, 7, 12, 6, 5},
		{"above the top scrolls down", 5, 3, 12, 6, 3},
		{"previous offset past the end clamps", 99, 10, 12, 6, 6},
		{"two-row window", 0, 2, 3, 2, 1},
		{"three-row window", 0, 3, 4, 3, 1},
		{"zero max rows still shows one", 0, 4, 9, 0, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := composerScrollOffset(tc.prev, tc.cursor, tc.all, tc.max)
			if got != tc.want {
				t.Fatalf("offset = %d, want %d", got, tc.want)
			}
			window := tc.max
			if window < 1 {
				window = 1
			}
			if got > tc.cursor || tc.cursor >= got+window {
				t.Fatalf("cursor row %d is outside the window [%d,%d)", tc.cursor, got, got+window)
			}
		})
	}
}

// Alt+Enter grows the input past the visible cap. The window must follow the
// caret; a fixed tail slice would hide it as soon as it moved above the last
// maxComposerRows rows.
func TestAltEnterOverflowKeepsCursorVisible(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	lines := maxComposerRows + 1
	var want strings.Builder
	for i := 0; i < lines; i++ {
		if i > 0 {
			keyPress(p, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
			want.WriteByte('\n')
		}
		keyPress(p, runes(fmt.Sprintf("r%d", i)))
		fmt.Fprintf(&want, "r%d", i)
	}
	if p.state.Input != want.String() {
		t.Fatalf("input = %q, want %q", p.state.Input, want.String())
	}
	if p.state.ComposerScroll != 1 {
		t.Errorf("ComposerScroll = %d, want 1: the top row scrolled off to keep r%d visible", p.state.ComposerScroll, lines-1)
	}
	out := Render(p.state)
	if got := strings.Count(out, "▌"); got != 1 {
		t.Fatalf("rendered %d cursor markers, want exactly one:\n%s", got, out)
	}
	if strings.Contains(out, "r0") {
		t.Errorf("render still shows the scrolled-off top row:\n%s", out)
	}
	assertCursorOnRow(t, p.state, fmt.Sprintf("r%d", lines-1))

	// Walk the caret back to the first row: it now sits above the tail window,
	// the exact place a fixed tail slice would stop painting it.
	for i := 0; i < lines-1; i++ {
		keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	}
	if p.state.ComposerScroll != 0 {
		t.Errorf("ComposerScroll = %d, want 0 with the caret on the first row", p.state.ComposerScroll)
	}
	assertCursorOnRow(t, p.state, "r0")
	if tail := fmt.Sprintf("r%d", lines-1); strings.Contains(Render(p.state), tail) {
		t.Errorf("render shows %s, want the window scrolled down with the caret", tail)
	}
}

// The arrows move the caret through the wrapped rows; the window only scrolls
// when the caret would leave it, so navigating inside the window is stable.
func TestComposerScrollTracksArrowNavigation(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	text := "r0\nr1\nr2\nr3\nr4\nr5\nr6\nr7"
	p.state.Apply(ComposerChanged{Text: text, Cursor: len([]rune(text))})
	if p.state.ComposerScroll != 2 {
		t.Fatalf("ComposerScroll = %d, want 2 at the tail of eight rows", p.state.ComposerScroll)
	}
	// Six rows up lands on row 1, one above the old window top (2).
	for i := 0; i < 6; i++ {
		keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	}
	if p.state.ComposerScroll != 1 {
		t.Fatalf("ComposerScroll = %d, want 1 after rising to row 1", p.state.ComposerScroll)
	}
	assertCursorOnRow(t, p.state, "r1")
	// Six rows down returns to the tail and the window follows back to 2.
	for i := 0; i < 6; i++ {
		keyPress(p, tea.KeyMsg{Type: tea.KeyDown})
	}
	if p.state.ComposerScroll != 2 {
		t.Fatalf("ComposerScroll = %d, want 2 back at the tail", p.state.ComposerScroll)
	}
	assertCursorOnRow(t, p.state, "r7")
}

// Recalling a long multiline prompt must not turn the composer into a
// full-screen editor: the same row cap that bounds typed input bounds history,
// and the visible window follows the caret to the end of the recalled text.
func TestRecalledMultilinePromptStaysWithinComposerCap(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(TerminalResized{Width: 80, Height: 24})
	const lines = maxComposerRows + 6
	parts := make([]string, lines)
	for i := range parts {
		parts[i] = fmt.Sprintf("row-%02d", i)
	}
	prompt := strings.Join(parts, "\n")
	p.state.Submitted = []string{prompt}

	keyPress(p, tea.KeyMsg{Type: tea.KeyUp})
	if p.state.Input != prompt {
		t.Fatalf("recall = %q, want the multiline prompt", p.state.Input)
	}
	if p.state.ComposerScroll != lines-maxComposerRows {
		t.Errorf("ComposerScroll = %d, want %d at the recalled prompt's tail",
			p.state.ComposerScroll, lines-maxComposerRows)
	}

	out := Render(p.state)
	for i := 0; i < lines-maxComposerRows; i++ {
		if strings.Contains(out, parts[i]) {
			t.Errorf("render still shows scrolled-off %q; history recall expanded the composer", parts[i])
		}
	}
	if !strings.Contains(out, parts[lines-1]) {
		t.Errorf("render hides the recalled prompt's last row %q", parts[lines-1])
	}
	if got := strings.Count(out, "row-"); got > maxComposerRows {
		t.Errorf("render shows %d prompt rows, want at most %d", got, maxComposerRows)
	}
}

// assertCursorOnRow pins the cursor's visible row: exactly one block cursor is
// painted, and it rides the expected input row.
func assertCursorOnRow(t *testing.T, s State, row string) {
	t.Helper()
	out := Render(s)
	if got := strings.Count(out, "▌"); got != 1 {
		t.Fatalf("rendered %d cursor markers, want exactly one:\n%s", got, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "▌") {
			if !strings.Contains(line, row) {
				t.Fatalf("cursor line = %q, want it on the %q row", line, row)
			}
			return
		}
	}
	t.Fatalf("no cursor line found:\n%s", out)
}
