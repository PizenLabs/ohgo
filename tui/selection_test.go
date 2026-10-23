package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// selectionFixture has one user block, one styled notice and one assistant
// answer, so selection can cross block boundaries.
func selectionFixture() State {
	s := New()
	s.Apply(TerminalResized{Width: 50, Height: 30})
	s.Apply(HistoryReplaced{Entries: []Entry{
		{Kind: EntryUser, UserText: "hello world"},
		{Kind: EntryNotice, Notice: "\x1b[31msecond line\x1b[0m"},
		{Kind: EntryAssistant, AssistantText: "alpha beta gamma delta epsilon zeta eta theta iota kappa"},
	}})
	return s
}

func blockRowFor(t *testing.T, l *TranscriptLayout, block int, row int) LayoutRow {
	t.Helper()
	got, ok := l.BlockRow(block, row)
	if !ok {
		t.Fatalf("block %d row %d does not exist", block, row)
	}
	return got
}

func TestSelectionCopySingleLine(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	text, ok := CopySelection(l, &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: 5},
	})
	if !ok || text != "hello" {
		t.Fatalf("copy = %q, %v; want hello", text, ok)
	}
}

func TestSelectionCopyTrailingPaddingIsNotContent(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	width := l.BlockHeight(0)
	if width == 0 {
		t.Fatal("user block has no rows")
	}
	row := blockRowFor(t, l, 0, 0)
	text, ok := CopySelection(l, &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: row.Width + 40},
	})
	if !ok {
		t.Fatal("selection over padding copied nothing")
	}
	if text != "hello world" {
		t.Fatalf("copy = %q, want the logical text without paint padding", text)
	}
}

func TestSelectionCopyCrossBlockAndReversedDrag(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	first := blockIDAt(s.Entries, 0)
	second := blockIDAt(s.Entries, 1)

	forward := &Selection{
		Start: Position{BlockID: first, Row: 0, Column: 6},
		End:   Position{BlockID: second, Row: 0, Column: 6},
	}
	want, ok := CopySelection(l, forward)
	if !ok {
		t.Fatal("forward selection copied nothing")
	}
	if want != "world\n\nsecond" {
		t.Fatalf("cross-block copy = %q, want the interior blank row preserved", want)
	}
	reversed := &Selection{Start: forward.End, End: forward.Start}
	got, ok := CopySelection(l, reversed)
	if !ok || got != want {
		t.Fatalf("reversed copy = %q (%v), want %q", got, ok, want)
	}
}

func TestSelectionCopyMultilineAndWrapped(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 40, Height: 30})
	long := strings.Repeat("wrap this line ", 12)
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryAssistant, AssistantText: long}}})
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	if l.BlockHeight(0) < 3 {
		t.Fatalf("fixture did not wrap: height %d", l.BlockHeight(0))
	}
	text, ok := CopySelection(l, &Selection{
		Start: Position{BlockID: id, Row: 1, Column: 0},
		End:   Position{BlockID: id, Row: 2, Column: 4},
	})
	if !ok {
		t.Fatal("wrapped selection copied nothing")
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("copy contains ANSI: %q", text)
	}
	if !strings.Contains(text, "\n") {
		t.Fatalf("multi-row selection lost its line break: %q", text)
	}
}

func TestSelectionCopyEmpty(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	for _, sel := range []*Selection{
		nil,
		{Start: Position{BlockID: id, Row: 0, Column: 3}, End: Position{BlockID: id, Row: 0, Column: 3}},
		{Start: Position{BlockID: "missing", Row: 0, Column: 0}, End: Position{BlockID: id, Row: 0, Column: 3}},
	} {
		if text, ok := CopySelection(l, sel); ok || text != "" {
			t.Fatalf("empty selection copied %q (%v)", text, ok)
		}
	}
}

func TestSelectionCopyStyledContentHasNoANSI(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 1)
	row := blockRowFor(t, l, 1, 0)
	text, ok := CopySelection(l, &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: row.Width},
	})
	if !ok {
		t.Fatal("styled selection copied nothing")
	}
	if strings.Contains(text, "\x1b") || strings.Contains(text, "[31m") {
		t.Fatalf("styled copy leaked ANSI: %q", text)
	}
	if text != "second line" {
		t.Fatalf("styled copy = %q", text)
	}
}

func TestSelectionCopyUnicodeCells(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 30, Height: 20})
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryNotice, Notice: "A你B好C"}}})
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	// Cells: A=0, 你=1..2, B=3, 好=4..5, C=6.
	for _, tc := range []struct {
		start, end int
		want       string
	}{
		{0, 1, "A"},
		{1, 3, "你"},
		{3, 4, "B"},
		{4, 6, "好"},
		{0, 7, "A你B好C"},
	} {
		got, ok := CopySelection(l, &Selection{
			Start: Position{BlockID: id, Row: 0, Column: tc.start},
			End:   Position{BlockID: id, Row: 0, Column: tc.end},
		})
		if !ok || got != tc.want {
			t.Fatalf("cells [%d,%d) = %q (%v), want %q", tc.start, tc.end, got, ok, tc.want)
		}
	}
}

func TestSelectionHighlightKeepsWidthAndPaintsRange(t *testing.T) {
	styled := "\x1b[31mabcdef\x1b[0m"
	got := HighlightSelection(styled, 2, 4)
	if CellWidth(got) != CellWidth(styled) {
		t.Fatalf("highlight changed width: %d -> %d", CellWidth(styled), CellWidth(got))
	}
	if !strings.Contains(got, "\x1b[7m") {
		t.Fatalf("highlight did not enable reverse video: %q", got)
	}
	plain := ansi.Strip(got)
	if plain != "abcdef" {
		t.Fatalf("highlight changed text: %q", plain)
	}
}

// Scrolling is a viewport operation; it cannot change the logical selection or
// the text it projects.
func TestSelectionSurvivesScrolling(t *testing.T) {
	s := selectionFixture()
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 12})
	p.syncTranscript()
	id := blockIDAt(p.state.Entries, 2)
	sel := &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: 5},
	}
	p.state.Selection = sel
	before, ok := CopySelection(p.transcriptFrame.index, sel)
	if !ok {
		t.Fatal("selection did not copy before scrolling")
	}
	for _, delta := range []int{20, -10, 5, -100} {
		p.state.Apply(ScrollMoved{Delta: delta})
		p.syncTranscript()
		after, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
		if !ok || after != before {
			t.Fatalf("scroll %d changed selection copy %q -> %q (%v)", delta, before, after, ok)
		}
	}
}

// A logical selection survives reflow: endpoints are block IDs and local
// rows, so resizing cannot turn them into coordinates on dead content.
func TestSelectionSurvivesResize(t *testing.T) {
	s := selectionFixture()
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	id := blockIDAt(p.state.Entries, 2)
	p.state.Selection = &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: 3},
	}
	for _, width := range []int{30, 80, 45} {
		p.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		if p.state.Selection == nil {
			t.Fatalf("width %d lost the selection", width)
		}
		p.syncTranscript()
		if _, ok := p.transcriptFrame.index.BlockIndexForID(p.state.Selection.Start.BlockID); !ok {
			t.Fatalf("width %d selection names a missing block", width)
		}
		if _, ok := CopySelection(p.transcriptFrame.index, p.state.Selection); !ok {
			t.Fatalf("width %d selection no longer copies", width)
		}
	}
}

// Hit testing must resolve a screen row through the viewport, not through an
// absolute terminal line. Scrolling and expansion change the answer; both are
// exercised through the same API the mouse handler uses.
func TestHitTestResolvesVisibleBlockAfterScrollAndExpansion(t *testing.T) {
	s := newViewportState(80, 80, 24)
	s.Apply(ToolStarted{ID: "mid", Name: "read", Summary: "read many"})
	s.Apply(ToolFinished{ID: "mid", Text: strings.Repeat("detail\n", 12), Summary: "12 lines"})
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	p.state.Apply(ScrollMoved{Delta: 20})
	p.syncTranscript()
	rows, marker, top, ok := p.visibleRows()
	if !ok || len(rows) == 0 {
		t.Fatal("no visible rows")
	}
	for i, row := range rows {
		pos, ok := p.positionAt(2, top+marker+i, false)
		if !ok {
			t.Fatalf("row %d missed", i)
		}
		if pos.BlockID != row.BlockID || pos.Row != row.Row {
			t.Fatalf("row %d hit (%s,%d), want (%s,%d)", i, pos.BlockID, pos.Row, row.BlockID, row.Row)
		}
	}

	// Expand the historical tool block and hit-test it at its new height.
	if _, ok := p.positionAt(2, top+marker, false); !ok {
		t.Fatal("top row missed")
	}
	p.state.Apply(ToolToggled{ID: "mid"})
	p.syncTranscript()
	if !p.state.tool("mid").Expanded {
		t.Fatal("tool did not expand")
	}
	// The viewport keeps its anchor, so the same screen cell still resolves to
	// a block from the current layout, never a stale one.
	if got, ok := p.positionAt(2, top+marker, false); !ok || got.BlockID == "" {
		t.Fatal("hit test failed after expansion")
	}
	p.state.Apply(ToolToggled{ID: "mid"}) // collapse again
	p.syncTranscript()
	if got, ok := p.positionAt(2, top+marker, false); !ok || got.BlockID == "" {
		t.Fatal("hit test failed after collapse")
	}
}

// A hit outside the painted transcript misses instead of clamping; a drag
// clamps to the nearest row.
func TestHitTestClampsOnlyForDrags(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(10, 80, 24)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	layout := ComputeLayout(p.state)
	if _, ok := p.positionAt(0, layout.HeaderLines-1, false); ok {
		t.Fatal("header cell resolved to a transcript row")
	}
	if pos, ok := p.positionAt(0, 1000, true); !ok || pos.BlockID == "" {
		t.Fatal("drag clamp below the region failed")
	}
	if pos, ok := p.positionAt(0, -1000, true); !ok || pos.BlockID == "" {
		t.Fatal("drag clamp above the region failed")
	}
}

// Resize changes wrapping but not identity: hit testing still resolves the
// block under the cell.
func TestHitTestSurvivesResize(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = syntheticDocument(50, 120)
	p.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	p.syncTranscript()
	for _, width := range []int{80, 40, 160} {
		p.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		rows, marker, y, ok := p.visibleRows()
		if !ok || len(rows) == 0 {
			t.Fatalf("width %d: no rows", width)
		}
		// The following viewport is at the bottom; the last row must hit the
		// last visible block rather than a stale pre-resize row.
		pos, ok := p.positionAt(1, y+marker+len(rows)-1, false)
		if !ok || pos.BlockID != rows[len(rows)-1].BlockID {
			t.Fatalf("width %d hit (%v,%v), want block %s", width, pos, ok, rows[len(rows)-1].BlockID)
		}
	}
}
