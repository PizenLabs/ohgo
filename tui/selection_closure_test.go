package tui

// Selection/clipboard closure matrix.
//
// Every test defends one externally observable contract from the selection
// geometry and mouse-release fixes: highlight intervals, clipboard payloads,
// and the MouseUp event path. The canonical range (normalizeSelection +
// rowSelectionInterval) is the single source of truth; rendering and copy
// consume it, so each test asserts both sides agree.

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// closureSpan extracts the reverse-video span from a highlighted row as plain
// text: the cells between the first enable and the last disable marker, with
// styling stripped. Rows without a marker fail the test.
func closureSpan(t *testing.T, highlighted string) string {
	t.Helper()
	start := strings.Index(highlighted, "\x1b[7m")
	end := strings.LastIndex(highlighted, "\x1b[27m")
	if start < 0 || end < 0 || end < start {
		t.Fatalf("no highlight markers in %q", highlighted)
		return ""
	}
	seg := highlighted[start+len("\x1b[7m") : end]
	seg = strings.ReplaceAll(seg, "\x1b[0m\x1b[7m", "")
	seg = strings.ReplaceAll(seg, "\x1b[m\x1b[7m", "")
	return ansi.Strip(seg)
}

func closureHasHighlight(highlighted string) bool {
	return strings.Contains(highlighted, "\x1b[7m")
}

func closureDecode(t *testing.T, seq string) string {
	t.Helper()
	payload := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("clipboard payload is not base64: %v", err)
	}
	return string(decoded)
}

// A. Same-row partial selections highlight and copy exactly the range.
func TestSelectionClosureSameRowPartial(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	row := blockRowFor(t, l, 0, 0)
	for _, tc := range []struct {
		name       string
		start, end int
		want       string
	}{
		{"world", 6, 11, "world"},
		{"ell", 1, 4, "ell"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel := &Selection{
				Start: Position{BlockID: id, Row: 0, Column: tc.start},
				End:   Position{BlockID: id, Row: 0, Column: tc.end},
			}
			if got := closureSpan(t, ApplySelectionHighlight(l, sel, row)); got != tc.want {
				t.Fatalf("highlight span = %q, want %q", got, tc.want)
			}
			text, ok := CopySelection(l, sel)
			if !ok || text != tc.want {
				t.Fatalf("copy = %q (%v), want %q", text, ok, tc.want)
			}
		})
	}
}

// B. A right-to-left drag paints and copies the same range as left-to-right.
func TestSelectionClosureReversedDrag(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	first := blockIDAt(s.Entries, 0)
	second := blockIDAt(s.Entries, 1)
	forward := &Selection{
		Start: Position{BlockID: first, Row: 0, Column: 6},
		End:   Position{BlockID: second, Row: 0, Column: 6},
	}
	reversed := &Selection{Start: forward.End, End: forward.Start}
	want, ok := CopySelection(l, forward)
	if !ok {
		t.Fatal("forward selection copied nothing")
	}
	got, ok := CopySelection(l, reversed)
	if !ok || got != want {
		t.Fatalf("reversed copy = %q (%v), want %q", got, ok, want)
	}
	for block := 0; block <= 1; block++ {
		for r := 0; r < l.BlockHeight(block); r++ {
			row, ok := l.BlockRow(block, r)
			if !ok {
				continue
			}
			fhl := ApplySelectionHighlight(l, forward, row)
			rhl := ApplySelectionHighlight(l, reversed, row)
			if closureHasHighlight(fhl) != closureHasHighlight(rhl) {
				t.Fatalf("block %d row %d highlight differs by drag direction", block, r)
			}
			if closureHasHighlight(fhl) && closureSpan(t, fhl) != closureSpan(t, rhl) {
				t.Fatalf("block %d row %d span differs by drag direction", block, r)
			}
		}
	}
}

// C. First/middle/last rows use [start,end), [0,end), [0,end) geometry.
func TestSelectionClosureFirstMiddleLast(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 50, Height: 30})
	s.Apply(HistoryReplaced{Entries: []Entry{
		{Kind: EntryNotice, Notice: "row0-content"},
		{Kind: EntryNotice, Notice: "row1-content"},
		{Kind: EntryNotice, Notice: "row2-content"},
	}})
	l := layoutFor(s)
	ids := []BlockID{blockIDAt(s.Entries, 0), blockIDAt(s.Entries, 1), blockIDAt(s.Entries, 2)}
	sel := &Selection{
		Start: Position{BlockID: ids[0], Row: 0, Column: 4},
		End:   Position{BlockID: ids[2], Row: 0, Column: 3},
	}
	rows := selectionRows(l, sel)
	intervals := map[int][2]int{}
	for _, sr := range rows {
		if CellWidth(sr.row.Plain()) == 0 {
			continue
		}
		intervals[sr.row.BlockIndex] = [2]int{sr.startCol, sr.endCol}
	}
	want := map[int][2]int{0: {4, 12}, 1: {0, 12}, 2: {0, 3}}
	for block, w := range want {
		got, ok := intervals[block]
		if !ok || got != w {
			t.Fatalf("block %d interval = %v (%v), want %v", block, got, ok, w)
		}
	}
	// Highlight agrees per row; blank separators stay unpainted.
	for block := 0; block <= 2; block++ {
		for r := 0; r < l.BlockHeight(block); r++ {
			row, _ := l.BlockRow(block, r)
			hl := ApplySelectionHighlight(l, sel, row)
			if CellWidth(row.Plain()) == 0 {
				if closureHasHighlight(hl) {
					t.Fatalf("blank block %d row %d highlighted", block, r)
				}
				continue
			}
			if !closureHasHighlight(hl) {
				t.Fatalf("block %d row %d not highlighted", block, r)
			}
			if got := closureSpan(t, hl); got != CellSlice(row.Plain(), want[block][0], want[block][1]) {
				t.Fatalf("block %d highlight %q != interval slice", block, got)
			}
		}
	}
	text, ok := CopySelection(l, sel)
	if !ok || text != "-content\n\nrow1-content\n\nrow" {
		t.Fatalf("copy = %q (%v)", text, ok)
	}
}

// C2. Rows in the endpoint block outside [startRow,endRow] stay unpainted.
func TestSelectionClosureSameBlockRowGuards(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 40, Height: 30})
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryAssistant, AssistantText: strings.Repeat("word ", 30)}}})
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	if l.BlockHeight(0) < 4 {
		t.Fatalf("fixture did not wrap: height %d", l.BlockHeight(0))
	}
	sel := &Selection{
		Start: Position{BlockID: id, Row: 1, Column: 2},
		End:   Position{BlockID: id, Row: 2, Column: 3},
	}
	for r := 0; r < l.BlockHeight(0); r++ {
		row, _ := l.BlockRow(0, r)
		hl := ApplySelectionHighlight(l, sel, row)
		has := closureHasHighlight(hl)
		switch {
		case r < 1 || r > 2:
			if has {
				t.Fatalf("row %d outside selection highlighted", r)
			}
		default:
			if !has {
				t.Fatalf("row %d inside selection not highlighted", r)
			}
		}
	}
}

// D. Wrapped rows are independent visual rows for paint and copy.
func TestSelectionClosureWrappedRows(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 24, Height: 30})
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryUser, UserText: "wrap this line many times over"}}})
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	if l.BlockHeight(0) < 2 {
		t.Fatalf("fixture did not wrap: height %d", l.BlockHeight(0))
	}
	r0, _ := l.BlockRow(0, 0)
	r1, _ := l.BlockRow(0, 1)
	sel := &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 2},
		End:   Position{BlockID: id, Row: 1, Column: 3},
	}
	text, ok := CopySelection(l, sel)
	if !ok {
		t.Fatal("wrapped selection copied nothing")
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("copy contains ANSI: %q", text)
	}
	lines := strings.Split(text, "\n")
	if len(lines) != 2 {
		t.Fatalf("wrapped copy lost its line break: %q", text)
	}
	if lines[0] != CellSlice(r0.Plain(), 2, CellWidth(r0.Plain())) || lines[1] != CellSlice(r1.Plain(), 0, 3) {
		t.Fatalf("wrapped copy = %q", text)
	}
	if got := closureSpan(t, ApplySelectionHighlight(l, sel, r0)); got != lines[0] {
		t.Fatalf("first-row highlight %q != copy %q", got, lines[0])
	}
	if got := closureSpan(t, ApplySelectionHighlight(l, sel, r1)); got != lines[1] {
		t.Fatalf("last-row highlight %q != copy %q", got, lines[1])
	}
}

// E. Display columns agree across ASCII, accented, CJK, emoji, combining marks.
func TestSelectionClosureUnicode(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 40, Height: 20})
	const text = "Aé中日本語✅éB"
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryNotice, Notice: text}}})
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	row, _ := l.BlockRow(0, 0)
	plain := row.Plain()
	if plain != text {
		t.Fatalf("plain = %q, want %q", plain, text)
	}
	// Cells: A=0, é=1, 中=2..3, 日=4..5, 本=6..7, 語=8..9, ✅=10..11, e=12, ́(combining)=12, B=13.
	for _, tc := range []struct {
		start, end int
		want       string
	}{
		{0, 1, "A"},
		{0, 2, "Aé"},
		{2, 4, "中"},
		{4, 10, "日本語"},
		{10, 12, "✅"},
		{12, 13, "é"},
		{13, 14, "B"},
		{0, 14, text},
		// Half-cell cuts never split a wide glyph: paint and copy agree.
		{0, 3, "Aé中"},
		{3, 4, "中"},
		{5, 6, "日"},
	} {
		sel := &Selection{
			Start: Position{BlockID: id, Row: 0, Column: tc.start},
			End:   Position{BlockID: id, Row: 0, Column: tc.end},
		}
		got, ok := CopySelection(l, sel)
		if !ok || got != tc.want {
			t.Fatalf("cells [%d,%d) copy = %q (%v), want %q", tc.start, tc.end, got, ok, tc.want)
		}
		if span := closureSpan(t, ApplySelectionHighlight(l, sel, row)); span != got {
			t.Fatalf("cells [%d,%d) highlight %q != copy %q", tc.start, tc.end, span, got)
		}
	}
}

// F. Styled content highlights visibly but copies plain logical text.
func TestSelectionClosureStyledContent(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 1)
	row := blockRowFor(t, l, 1, 0)
	sel := &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: row.Width},
	}
	text, ok := CopySelection(l, sel)
	if !ok {
		t.Fatal("styled selection copied nothing")
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("copy leaked ANSI: %q", text)
	}
	if text != "second line" {
		t.Fatalf("copy = %q, want logical text", text)
	}
	if span := closureSpan(t, ApplySelectionHighlight(l, sel, row)); span != "second line" {
		t.Fatalf("highlight span = %q, want logical text", span)
	}
}

// G. Renderer padding past the content is never painted or copied.
func TestSelectionClosurePadding(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 0)
	row := blockRowFor(t, l, 0, 0)
	sel := &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: row.Width + 40},
	}
	text, ok := CopySelection(l, sel)
	if !ok || text != "hello world" {
		t.Fatalf("copy = %q (%v), want logical text without padding", text, ok)
	}
	if span := closureSpan(t, ApplySelectionHighlight(l, sel, row)); span != "hello world" {
		t.Fatalf("highlight span = %q, want content without padding", span)
	}
}

// H. The MouseUp event path completes exactly one copy and keeps the selection.
func TestSelectionClosureMouseUpCopiesOnce(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var out bytes.Buffer
	p.termOut = &out
	calls := 0
	var copied string
	p.clipboard = func(s string) string {
		calls++
		copied = s
		return OSC52Sequence(s)
	}
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	_, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if calls != 1 {
		t.Fatalf("clipboard calls = %d, want exactly 1", calls)
	}
	if copied != "world" {
		t.Fatalf("copied = %q, want logical selection", copied)
	}
	if p.state.Selection == nil {
		t.Fatal("selection cleared by MouseUp")
	}
	if out.Len() == 0 {
		t.Fatal("no OSC 52 bytes emitted")
	}
	if got := closureDecode(t, out.String()); got != "world" {
		t.Fatalf("OSC 52 payload = %q, want logical text", got)
	}
}

// H2. An X10-style release (ButtonNone) finalizes the same way as SGR.
func TestSelectionClosureMouseUpButtonNone(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var out bytes.Buffer
	p.termOut = &out
	calls := 0
	p.clipboard = func(s string) string {
		calls++
		return OSC52Sequence(s)
	}
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	_, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 5, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonNone, Action: tea.MouseActionRelease, X: 5, Y: y})
	if calls != 1 {
		t.Fatalf("ButtonNone release clipboard calls = %d, want 1", calls)
	}
	if p.drag != nil {
		t.Fatal("drag leaked past MouseUp")
	}
	if p.state.Selection == nil {
		t.Fatal("selection lost on ButtonNone release")
	}
}

// I. Press without movement copies nothing.
func TestSelectionClosureEmptyDragNoCopy(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var out bytes.Buffer
	p.termOut = &out
	calls := 0
	p.clipboard = func(s string) string {
		calls++
		return OSC52Sequence(s)
	}
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	_, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 2, Y: y})
	if calls != 0 {
		t.Fatalf("empty drag copied %d times", calls)
	}
	if out.Len() != 0 {
		t.Fatalf("empty drag wrote %q", out.String())
	}
}

// J. Dragging across a scroll keeps one logical selection and one copy.
func TestSelectionClosureScrollDuringSelection(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var out bytes.Buffer
	p.termOut = &out
	calls := 0
	var copied string
	p.clipboard = func(s string) string {
		calls++
		copied = s
		return OSC52Sequence(s)
	}
	s := selectionFixture()
	for i := 0; i < 30; i++ {
		s.Apply(HistoryReplaced{Entries: append(s.Entries, Entry{Kind: EntryAssistant, AssistantText: "filler line"})})
	}
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 20})
	p.syncTranscript()
	_, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 5, Y: y + 1})
	// Wheel scroll while the button is held.
	p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress, X: 0, Y: 0})
	p.syncTranscript()
	_, marker2, top2, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows after scroll")
	}
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 5, Y: top2 + marker2 + 1})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 5, Y: top2 + marker2 + 1})
	if calls != 1 {
		t.Fatalf("clipboard calls = %d, want 1", calls)
	}
	if p.state.Selection == nil {
		t.Fatal("selection lost across scroll")
	}
	text, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
	if !ok || text != copied {
		t.Fatalf("retained copy %q != released payload %q", text, copied)
	}
	if strings.Contains(copied, "\x1b") {
		t.Fatalf("scroll-drag payload leaked ANSI: %q", copied)
	}
}

// K. A detached selection survives streaming appends with a stable anchor.
func TestSelectionClosureDetachedStreaming(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 12})
	p.syncTranscript()
	p.state.Apply(ScrollMoved{Delta: 20})
	p.syncTranscript()
	if p.transcriptFrame.vp.Following {
		t.Fatal("scroll did not detach the viewport")
	}
	anchor := p.transcriptFrame.vp.Anchor
	id := blockIDAt(p.state.Entries, 0)
	p.state.Selection = &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: 5},
	}
	before, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
	if !ok || before != "hello" {
		t.Fatalf("detached copy = %q (%v)", before, ok)
	}
	p.state.Apply(AssistantDelta{Text: "streamed line one\nstreamed line two\n"})
	p.syncTranscript()
	if p.transcriptFrame.vp.Following {
		t.Fatal("append reattached a detached viewport")
	}
	if p.transcriptFrame.vp.Anchor != anchor {
		t.Fatalf("append moved the reading anchor %+v -> %+v", anchor, p.transcriptFrame.vp.Anchor)
	}
	after, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
	if !ok || after != before {
		t.Fatalf("append changed the detached copy %q -> %q", before, after)
	}
}

// L. Resize keeps the block identity and the logical text; paint reflows.
func TestSelectionClosureResize(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	id := blockIDAt(p.state.Entries, 0)
	p.state.Selection = &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: 5},
	}
	for _, width := range []int{30, 80, 45} {
		p.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		p.syncTranscript()
		if p.state.Selection == nil {
			t.Fatalf("width %d lost the selection", width)
		}
		if p.state.Selection.Start.BlockID != id {
			t.Fatalf("width %d moved the selection to %s", width, p.state.Selection.Start.BlockID)
		}
		text, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
		if !ok || text != "hello" {
			t.Fatalf("width %d copy = %q (%v)", width, text, ok)
		}
		row, ok := p.transcriptFrame.index.BlockRow(0, 0)
		if !ok {
			t.Fatalf("width %d lost block 0", width)
		}
		if span := closureSpan(t, ApplySelectionHighlight(p.transcriptFrame.index, p.state.Selection, row)); span != "hello" {
			t.Fatalf("width %d highlight = %q", width, span)
		}
	}
}
