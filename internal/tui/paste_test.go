package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A large bracketed paste folds into one inline chip: the payload is kept for
// submission while the composer stays a single row.
func TestBracketedPasteCollapsesToAChip(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	payload := "one\ntwo\nthree\nfour"
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(payload), Paste: true})

	if p.state.Input != "[Paste #1 - 4 lines]" {
		t.Fatalf("input = %q, want the inline chip", p.state.Input)
	}
	if len(p.state.Pastes) != 1 || p.state.Pastes[0].Payload != payload {
		t.Fatalf("pastes = %+v, want the payload retained", p.state.Pastes)
	}
	if strings.Contains(p.state.Input, "\n") {
		t.Errorf("chip spans lines: %q", p.state.Input)
	}
}

// Only a large bracketed paste collapses. A short bracketed paste, a typed
// newline, and a keystroke stream from a terminal without bracketed paste are
// all indistinguishable from prose and must stay literal.
func TestSmallOrNonBracketedPasteStaysLiteral(t *testing.T) {
	cases := []struct {
		name   string
		pasted bool
		text   string
	}{
		{"short bracketed", true, "one\ntwo\nthree"},
		{"typed multiline", false, "one\ntwo\nthree\nfour"},
		{"keystroke stream", false, "a\nb\nc\nd\ne"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProgram(nil, "test", 0)
			keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.text), Paste: tc.pasted})
			if len(p.state.Pastes) != 0 {
				t.Errorf("collapsed %q (pasted=%v); only a large bracketed paste should", tc.text, tc.pasted)
			}
			if p.state.Input != tc.text {
				t.Errorf("input = %q, want the literal text", p.state.Input)
			}
		})
	}
}

// Manual multi-line injection uses the same collapse path as a bracketed
// paste: the event is the seam a caller (or test) drives directly.
func TestPasteCollapsedInjectsAChipDirectly(t *testing.T) {
	s := New()
	s.Apply(PasteCollapsed{Text: "1\n2\n3\n4", Lines: 4})
	if s.Input != "[Paste #1 - 4 lines]" {
		t.Fatalf("input = %q, want the chip", s.Input)
	}
	if len(s.Pastes) != 1 || s.Pastes[0].ID != 1 || s.Pastes[0].Payload != "1\n2\n3\n4" {
		t.Fatalf("pastes = %+v, want chip #1 with the payload", s.Pastes)
	}
}

// Backspace treats a chip as one token: it removes the whole pill and releases
// the payload with it, rather than editing the label character by character.
func TestBackspaceRemovesTheChipAtomically(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, runes("hi "))
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb\nc\nd"), Paste: true})
	if !strings.HasSuffix(p.state.Input, "[Paste #1 - 4 lines]") {
		t.Fatalf("input = %q, want the chip appended", p.state.Input)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyBackspace})
	if p.state.Input != "hi " {
		t.Errorf("input = %q, want the whole chip removed", p.state.Input)
	}
	if len(p.state.Pastes) != 0 {
		t.Errorf("pastes = %+v, want the payload released with the chip", p.state.Pastes)
	}
}

// Arrow keys step over a chip as one unit, and delete removes it whole, so the
// cursor can never land inside a label.
func TestChipEditingIsAtomic(t *testing.T) {
	chip := PasteChip{ID: 1, Payload: "a\nb\nc\nd", Lines: 4}
	label := pasteChipLabel(chip)
	text := "x" + label + "y"
	end := 1 + len([]rune(label))

	if got := cursorLeftAtomic([]PasteChip{chip}, text, end); got != 1 {
		t.Errorf("left = %d, want the chip start (1)", got)
	}
	if got := cursorRightAtomic([]PasteChip{chip}, text, 1); got != end {
		t.Errorf("right = %d, want the chip end (%d)", got, end)
	}
	if got, cursor, id, ok := pasteDeleteBefore([]PasteChip{chip}, text, end); !ok || got != "xy" || cursor != 1 || id != 1 {
		t.Errorf("deleteBefore = (%q, %d, %d, %v), want (xy, 1, 1, true)", got, cursor, id, ok)
	}
	if got, cursor, id, ok := pasteDeleteAt([]PasteChip{chip}, text, 1); !ok || got != "xy" || cursor != 1 || id != 1 {
		t.Errorf("deleteAt = (%q, %d, %d, %v), want (xy, 1, 1, true)", got, cursor, id, ok)
	}
}

// A vertical hop that would land inside a pill snaps to its edge instead, so
// the cursor can never be stranded mid-label.
func TestVerticalMovementSnapsOutsideAChip(t *testing.T) {
	chip := PasteChip{ID: 1, Payload: "p", Lines: 1}
	label := pasteChipLabel(chip)
	text := "xx" + label + "yy"
	if got := snapPasteBoundary([]PasteChip{chip}, text, 5); got != 2 {
		t.Errorf("snap inside = %d, want the label start (2)", got)
	}
	if got := snapPasteBoundary([]PasteChip{chip}, text, 1); got != 1 {
		t.Errorf("snap outside = %d, want it unchanged", got)
	}
}

// Submission substitutes the payload back in a single left-to-right pass: a
// payload that itself contains a label-looking string is emitted verbatim, not
// expanded again.
func TestExpandPastesSubstitutesOnce(t *testing.T) {
	inner := PasteChip{ID: 2, Payload: "inner", Lines: 1}
	outer := PasteChip{ID: 1, Payload: pasteChipLabel(inner), Lines: 1}
	got := expandPastes([]PasteChip{outer, inner}, pasteChipLabel(outer))
	if want := pasteChipLabel(inner); got != want {
		t.Errorf("expandPastes = %q, want %q: a single pass must not re-expand a payload", got, want)
	}
}

// Enter on a chip-only composer sends the payload, not the label. The busy
// path queues synchronously, which is the observable seam without a turn.
func TestChipExpandsToItsPayloadOnSubmit(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "busy"}) // a turn is running
	payload := "one\ntwo\nthree\nfour"
	p.state.Apply(PasteCollapsed{Text: payload, Lines: 4})
	keyPress(p, tea.KeyMsg{Type: tea.KeyEnter})
	if len(p.state.Queued) != 1 || p.state.Queued[0] != payload {
		t.Fatalf("queued = %v, want the expanded payload", p.state.Queued)
	}
}

// The renderer paints the chip and never the raw payload, so a large paste
// cannot evict the conversation.
func TestCollapsedPasteRendersAsAChip(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	payload := "alpha\nbravo\ncharlie\ndelta"
	s.Apply(PasteCollapsed{Text: payload, Lines: 4})
	out := Render(s)
	if !strings.Contains(out, "[Paste #1 - 4 lines]") {
		t.Errorf("render lacks the chip:\n%s", out)
	}
	if strings.Contains(out, payload) {
		t.Errorf("render expanded the payload:\n%s", out)
	}
}

// A chip left in the composer keeps its payload across unrelated edits, and a
// cleared composer releases it: payloads never outlive their pills.
func TestPayloadLifecycleFollowsItsLabel(t *testing.T) {
	s := New()
	s.Apply(PasteCollapsed{Text: "a\nb\nc\nd", Lines: 4})
	s.Apply(ComposerChanged{Text: s.Input + " tail", Cursor: len([]rune(s.Input + " tail"))})
	if len(s.Pastes) != 1 {
		t.Fatalf("pastes = %+v, want the chip kept while its label remains", s.Pastes)
	}
	s.Apply(InputChanged{Text: ""})
	if len(s.Pastes) != 0 {
		t.Errorf("pastes = %+v, want the payload dropped with the cleared composer", s.Pastes)
	}
}

// Browsing history from a chip draft and returning must not lose the payload:
// the stashed draft still references the pill, so recall cannot silently turn
// the chip back into literal text.
func TestDraftChipSurvivesHistoryBrowsing(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "old"})
	s.Apply(PasteCollapsed{Text: "a\nb\nc\nd", Lines: 4})

	s.Apply(HistoryMoved{Delta: 1})
	if s.Input != "old" {
		t.Fatalf("history recall = %q, want the older prompt", s.Input)
	}
	s.Apply(HistoryMoved{Delta: -1})
	if s.Input != "[Paste #1 - 4 lines]" || len(s.Pastes) != 1 {
		t.Fatalf("draft = %q pastes = %+v, want the chip restored", s.Input, s.Pastes)
	}
}

// The output-first composer renders the same chip, so a large paste stays
// compact there too.
func TestLiveComposerRendersChips(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(PasteCollapsed{Text: "a\nb\nc\nd", Lines: 4})
	lines := liveComposerLines(s, PaletteFor(s.Theme), ComputeLayout(s))
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "[Paste #1 - 4 lines]") {
		t.Errorf("live composer lacks the chip:\n%s", joined)
	}
}

// Vertical movement folds at the width the active renderer used. Live mode
// draws a fixed two-cell prompt, so a BUILD prefix must not shrink its wrap
// budget to the full-screen composer's.
func TestLiveMovementUsesTheLiveComposerWidth(t *testing.T) {
	p, _ := newLiveProgram()
	p.state.Apply(TerminalResized{Width: 12, Height: 24})
	p.state.Status.Workspace = "BUILD"
	p.state.Apply(ComposerChanged{Text: strings.Repeat("a", 15), Cursor: 14})

	pos, ok := p.composerLineAbove()
	if !ok || pos != 6 {
		t.Errorf("live up = (%d, %v), want (6, true): live folds at 8 cells, not the BUILD prefix width", pos, ok)
	}
}

// A word kill scans by whitespace, and a chip label contains spaces, so without
// chip awareness it would stop mid-pill and orphan half a label. The whole pill
// must go as one token.
func TestKillCommandsConsumeAWholeChip(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	keyPress(p, runes("read "))
	keyPress(p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb\nc\nd"), Paste: true})
	keyPress(p, runes(" tail"))

	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlW})
	if !strings.Contains(p.state.Input, "[Paste #1 - 4 lines]") || len(p.state.Pastes) != 1 {
		t.Fatalf("ctrl+w over the tail split the chip: input = %q pastes = %+v", p.state.Input, p.state.Pastes)
	}
	keyPress(p, tea.KeyMsg{Type: tea.KeyCtrlW})
	if p.state.Input != "read " {
		t.Errorf("input = %q, want the whole chip removed as one word", p.state.Input)
	}
	if len(p.state.Pastes) != 0 {
		t.Errorf("pastes = %+v, want the payload released with the label", p.state.Pastes)
	}
	if strings.Contains(p.state.Input, "Paste") {
		t.Errorf("input = %q, want no orphaned half-label left", p.state.Input)
	}
}

// Every kill must remove a whole label or none of it, even from a cursor a
// malformed state could strand inside a pill.
func TestChipAwareEditsNeverLeaveAHalfLabel(t *testing.T) {
	chip := PasteChip{ID: 1, Payload: "a\nb\nc\nd", Lines: 4}
	label := pasteChipLabel(chip)
	text := "xy " + label + " z"
	start := len([]rune("xy "))
	end := start + len([]rune(label))
	inside := start + 2

	killers := []struct {
		name string
		fn   func([]PasteChip, string, int) (string, int)
	}{
		{"ctrl+u", killToStartAtomic},
		{"ctrl+k", killToEndAtomic},
		{"ctrl+w", killWordBackAtomic},
	}
	for _, kill := range killers {
		for _, cur := range []int{start, inside, end} {
			out, got := kill.fn([]PasteChip{chip}, text, cur)
			if got < 0 || got > len([]rune(out)) {
				t.Errorf("%s at %d: cursor %d is outside %q", kill.name, cur, got, out)
			}
			if partialLabel(out, label) {
				t.Errorf("%s at %d left a half-label: %q", kill.name, cur, out)
			}
		}
	}
	// Home and End never park the cursor inside a label, so a pill stays whole.
	if got := lineStartAtomic([]PasteChip{chip}, text, inside); got != 0 {
		t.Errorf("home from inside a pill = %d, want the line start", got)
	}
	if got := lineEndAtomic([]PasteChip{chip}, text, inside); got != len([]rune(text)) {
		t.Errorf("end from inside a pill = %d, want the line end", got)
	}
}

// A forward snap moves a stranded cursor to a label's tail; Home uses the
// backward snap and End the forward one, so neither splits a pill.
func TestSnapPasteBoundaryForwardReachesThePillTail(t *testing.T) {
	chip := PasteChip{ID: 1, Payload: "p", Lines: 1}
	label := pasteChipLabel(chip)
	text := "x" + label + "y"
	start := 1
	end := 1 + len([]rune(label))
	if got := snapPasteBoundaryForward([]PasteChip{chip}, text, start+2); got != end {
		t.Errorf("forward snap inside = %d, want the label tail %d", got, end)
	}
	if got := snapPasteBoundaryForward([]PasteChip{chip}, text, end); got != end {
		t.Errorf("forward snap on the edge = %d, want it unchanged", got)
	}
}

// partialLabel reports a label's run split across out: the whole label is
// absent, yet a run of it survives as an orphaned half-pill.
func partialLabel(out, label string) bool {
	if strings.Contains(out, label) {
		return false
	}
	runes := []rune(label)
	const minRun = 5
	for n := len(runes) - 1; n >= minRun; n-- {
		for i := 0; i+n <= len(runes); i++ {
			if strings.Contains(out, string(runes[i:i+n])) {
				return true
			}
		}
	}
	return false
}
