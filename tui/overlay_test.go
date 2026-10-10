package tui

import (
	"strings"
	"testing"
)

// State additions for the UI rebuild: history, queue, scroll, theme, picker.
// Each test drives Apply directly — the state machine owns no terminal.

// History keeps prompts recallable without resending them.

func TestSubmitRecordsRecallableHistory(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "first"})
	s.Apply(UserSubmitted{Text: "second"})
	if len(s.Submitted) != 2 || s.Submitted[0] != "first" {
		t.Errorf("Submitted = %v, want both prompts in order", s.Submitted)
	}
}

func TestSubmitCollapsesConsecutiveDuplicates(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "same"})
	s.Apply(UserSubmitted{Text: "same"})
	if len(s.Submitted) != 1 {
		t.Errorf("Submitted = %v, want one entry: holding enter must not flood history", s.Submitted)
	}
}

func TestHistoryBrowseRestoresTheDraft(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "old"})
	s.Apply(InputChanged{Text: "draft"})
	s.Apply(HistoryMoved{Delta: 1})
	if s.Input != "old" {
		t.Errorf("Input = %q, want the recalled prompt", s.Input)
	}
	s.Apply(HistoryMoved{Delta: -1})
	if s.Input != "draft" {
		t.Errorf("Input = %q, want the stashed draft back: recall must never eat typing", s.Input)
	}
}

func TestHistoryClampsAtBothEnds(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "only"})
	s.Apply(HistoryMoved{Delta: 5})
	if s.Input != "only" {
		t.Errorf("Input = %q, want it clamped to the oldest", s.Input)
	}
	s.Apply(HistoryMoved{Delta: -5})
	if s.HistCursor != -1 {
		t.Errorf("HistCursor = %d, want live input", s.HistCursor)
	}
}

func TestHistoryIsEmptyWithoutSubmissions(t *testing.T) {
	s := New()
	s.Apply(InputChanged{Text: "typing"})
	s.Apply(HistoryMoved{Delta: 1})
	if s.Input != "typing" {
		t.Errorf("Input = %q, want it untouched with no history", s.Input)
	}
}

// Queue holds prompts typed while a turn runs.

func TestQueueShiftDropsTheHead(t *testing.T) {
	s := New()
	s.Apply(MessageQueued{Text: "a"})
	s.Apply(MessageQueued{Text: "b"})
	s.Apply(QueueShifted{})
	if len(s.Queued) != 1 || s.Queued[0] != "b" {
		t.Errorf("Queued = %v, want [b]", s.Queued)
	}
}

func TestClearDropsQueuedPrompts(t *testing.T) {
	// Queued prompts were typed against the context /clear just dropped;
	// sending them into a fresh context would surprise.
	s := New()
	s.Apply(MessageQueued{Text: "stale"})
	s.Apply(Cleared{})
	if len(s.Queued) != 0 {
		t.Errorf("Queued = %v, want it dropped with the transcript", s.Queued)
	}
	if len(s.Submitted) != 0 {
		t.Errorf("Submitted = %v, want recallable history kept: it is memory, not context", s.Submitted)
	}
}

// Scroll pins a scrolled-up viewport while output lands.

func TestScrollPinsWhileUnfollowing(t *testing.T) {
	s := New()
	s.Apply(ScrollMoved{Delta: 5})
	if s.Follow {
		t.Error("scrolling up must leave follow mode")
	}
	s.Apply(Notice{Text: "a\nb"})
	if s.Scroll != 5+2 {
		t.Errorf("Scroll = %d, want 7: the offset from the bottom grows with new lines", s.Scroll)
	}
}

func TestScrollToBottomReengagesFollow(t *testing.T) {
	s := New()
	s.Apply(ScrollMoved{Delta: 5})
	s.Apply(ScrollToBottom{})
	if s.Scroll != 0 || !s.Follow {
		t.Errorf("Scroll = %d Follow = %v, want live edge", s.Scroll, s.Follow)
	}
}

func TestScrollDownToZeroReengagesFollow(t *testing.T) {
	s := New()
	s.Apply(ScrollMoved{Delta: 5})
	s.Apply(ScrollMoved{Delta: -99})
	if s.Scroll != 0 || !s.Follow {
		t.Errorf("Scroll = %d Follow = %v, want scrolling back to the bottom to follow", s.Scroll, s.Follow)
	}
}

func TestFollowModeIgnoresGrowth(t *testing.T) {
	s := New()
	s.Apply(Notice{Text: "x"})
	if s.Scroll != 0 || !s.Follow {
		t.Errorf("Scroll = %d Follow = %v, want the live edge tracked", s.Scroll, s.Follow)
	}
}

// Theme and size.

func TestThemeNormalizesUnknownNames(t *testing.T) {
	s := New()
	if s.Theme != ThemeDark {
		t.Errorf("default theme = %q, want dark", s.Theme)
	}
	s.Apply(ThemeChanged{Name: "LIGHT"})
	if s.Theme != ThemeLight {
		t.Errorf("theme = %q, want light", s.Theme)
	}
	s.Apply(ThemeChanged{Name: "solarized"})
	if s.Theme != ThemeDark {
		t.Errorf("theme = %q, want a typo to degrade to dark, not stick", s.Theme)
	}
}

func TestTerminalResizedRecordsDimensions(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	if s.Width != 100 || s.Height != 30 {
		t.Errorf("size = %dx%d, want 100x30", s.Width, s.Height)
	}
}

func TestPalettesDiffer(t *testing.T) {
	dark, light := DarkPalette(), LightPalette()
	// Styles hold funcs and do not compare; their foregrounds do.
	if dark.User.GetForeground() == light.User.GetForeground() {
		t.Error("the palettes are identical: a theme switch would change nothing")
	}
	if got := PaletteFor("nope"); got.User.GetForeground() != dark.User.GetForeground() {
		t.Error("an unknown theme must degrade to dark")
	}
	if got := PaletteFor("light"); got.User.GetForeground() != light.User.GetForeground() {
		t.Error("the light theme must resolve to the light palette")
	}
}

// Picker overlay.

func TestPickerFiltersPredictably(t *testing.T) {
	s := New()
	s.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: []PickerItem{
		{Title: "anthropic/claude-sonnet-4-5"},
		{Title: "groq/llama-3.1-8b-instant"},
	}})
	s.Apply(PickerFilterChanged{Text: "GROQ"})
	visible := s.Picker.Visible()
	if len(visible) != 1 || visible[0].Title != "groq/llama-3.1-8b-instant" {
		t.Errorf("visible = %v, want the case-insensitive substring match", visible)
	}
}

func TestPickerClampsCursorOnNarrowing(t *testing.T) {
	p := NewPicker(PickerModels, "models", []PickerItem{{Title: "a"}, {Title: "b"}})
	p.Cursor = 1
	p.Filter = "a"
	p.clamp()
	if p.Cursor != 0 {
		t.Errorf("Cursor = %d, want it re-clamped into the narrowed list", p.Cursor)
	}
}

func TestPickerMoveWraps(t *testing.T) {
	p := NewPicker(PickerModels, "models", []PickerItem{{Title: "a"}, {Title: "b"}})
	p.Move(-1)
	if p.Cursor != 1 {
		t.Errorf("Cursor = %d, want wrap to the end", p.Cursor)
	}
	p.Move(1)
	if p.Cursor != 0 {
		t.Errorf("Cursor = %d, want wrap to the start", p.Cursor)
	}
}

func TestPickerChosenFailsEmpty(t *testing.T) {
	p := NewPicker(PickerModels, "models", nil)
	p.Filter = "zzz"
	if _, ok := p.Chosen(); ok {
		t.Error("an empty visible list must not yield a choice")
	}
}

func TestPickerClosedClears(t *testing.T) {
	s := New()
	s.Apply(PickerOpened{Kind: PickerJobs, Title: "jobs", Items: nil})
	s.Apply(PickerClosed{})
	if s.Picker != nil {
		t.Error("the overlay must dismiss without choosing")
	}
}

// Composer cursor.

func TestComposerChangedClampsTheCursor(t *testing.T) {
	s := New()
	s.Apply(ComposerChanged{Text: "ab", Cursor: 99})
	if s.Cursor != 2 {
		t.Errorf("Cursor = %d, want it clamped to the text", s.Cursor)
	}
}

func TestTypingLeavesHistory(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "old"})
	s.Apply(HistoryMoved{Delta: 1})
	s.Apply(ComposerChanged{Text: "old!", Cursor: 4})
	if s.HistCursor != -1 {
		t.Error("editing a recalled entry returns to live input")
	}
}

// HistoryReplaced swaps the transcript on resume.

func TestHistoryReplacedSwapsEntries(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "current"})
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryUser, UserText: "earlier"}}})
	if len(s.Entries) != 1 || s.Entries[0].UserText != "earlier" {
		t.Errorf("Entries = %v, want the replayed transcript, not a merge", s.Entries)
	}
}

// Render slices to the viewport.

func TestRenderSlicesToViewport(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 8})
	for i := 0; i < 20; i++ {
		s.Apply(Notice{Text: "line"})
	}
	out := Render(s)
	// 20 notices plus chrome cannot fit 8 rows: the oldest must be cut.
	if strings.Count(out, "line") >= 20 {
		t.Errorf("rendered %d lines in an 8-row viewport: nothing was sliced", strings.Count(out, "line"))
	}
	if !strings.Contains(out, "ohgo") {
		t.Error("the status line must survive slicing")
	}
}

func TestRenderWithoutSizeShowsEverything(t *testing.T) {
	s := New()
	for i := 0; i < 5; i++ {
		s.Apply(Notice{Text: "line"})
	}
	if got := strings.Count(Render(s), "line"); got != 5 {
		t.Errorf("rendered %d of 5 lines with unknown size: only a known viewport may slice", got)
	}
}

func TestRenderShowsScrollPosition(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 8})
	for i := 0; i < 20; i++ {
		s.Apply(Notice{Text: "line"})
	}
	s.Apply(ScrollMoved{Delta: 3})
	if out := Render(s); !strings.Contains(out, "lines above") {
		t.Errorf("no scroll indicator rendered: %q", out)
	}
}

func TestRenderMarksTheCursor(t *testing.T) {
	s := New()
	s.Apply(ComposerChanged{Text: "ab", Cursor: 1})
	if out := Render(s); !strings.Contains(out, "▌") {
		t.Errorf("no cursor marker rendered: %q", out)
	}
}

func TestRenderHidesComposerBehindPicker(t *testing.T) {
	s := New()
	s.Apply(InputChanged{Text: "typing"})
	s.Apply(PickerOpened{Kind: PickerModels, Title: "models", Items: []PickerItem{{Title: "m"}}})
	out := Render(s)
	if strings.Contains(out, "typing") {
		t.Errorf("composer visible behind the picker: %q", out)
	}
	if !strings.Contains(out, "models") {
		t.Errorf("picker title missing: %q", out)
	}
}
