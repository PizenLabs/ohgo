package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func newViewportState(blocks, width, height int) State {
	s := New()
	s.Apply(TerminalResized{Width: width, Height: height})
	for i := 0; i < blocks; i++ {
		s.Apply(Notice{Text: fmt.Sprintf("row block %d", i)})
	}
	return s
}

func viewportTopRow(t *testing.T, frame *transcriptFrame) LayoutRow {
	t.Helper()
	rows := frame.index.Rows(frame.vp.Offset, 1)
	if len(rows) == 0 {
		t.Fatal("viewport has no visible content")
	}
	return rows[0]
}

// An empty document has no rows and no scroll position to invent.
func TestViewportEmptyDocument(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	frame := viewportFor(s)
	if frame.index.TotalRows() != 0 {
		t.Fatalf("rows = %d, want 0", frame.index.TotalRows())
	}
	if frame.vp.Offset != 0 || frame.vp.Marker {
		t.Fatalf("viewport = %+v, want an empty following window", frame.vp)
	}
}

// A document shorter than the viewport sits at the top and has no marker.
func TestViewportShortDocument(t *testing.T) {
	s := newViewportState(2, 80, 24)
	frame := viewportFor(s)
	if frame.vp.Offset != 0 || frame.vp.Marker || !frame.vp.Following {
		t.Fatalf("viewport = %+v, want top/ following/ no marker", frame.vp)
	}
}

// Exactly one row taller than the budget must scroll by one, and the marker
// must not hide the first row at maximum scroll.
func TestViewportExactlyOneRowTaller(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	budget := transcriptBudget(s, ComputeLayout(s))
	s.Apply(Notice{Text: strings.Repeat("x\n", budget+1)})
	l := layoutFor(s)
	if l.TotalRows() != budget+1 {
		t.Fatalf("fixture rows = %d, want %d", l.TotalRows(), budget+1)
	}
	frame := viewportFor(s)
	if !frame.vp.Following || frame.vp.Offset != 1 || frame.vp.Marker {
		t.Fatalf("following one-row-taller viewport = %+v", frame.vp)
	}
	s.Apply(ScrollMoved{Delta: 1})
	frame = viewportFor(s)
	if !frame.vp.Marker || frame.vp.Offset != 1 {
		t.Fatalf("one tick up = %+v, want marker at offset 1", frame.vp)
	}
	s.Apply(ScrollMoved{Delta: 10000})
	frame = viewportFor(s)
	if frame.vp.Offset != 0 || !frame.vp.Marker {
		t.Fatalf("max scroll = %+v, want the first row visible", frame.vp)
	}
}

// A long document clamps an overshoot to its top without losing rows.
func TestViewportLargeDocumentMaxScroll(t *testing.T) {
	s := newViewportState(40, 80, 10)
	budget := transcriptBudget(s, ComputeLayout(s))
	l := layoutFor(s)
	if l.TotalRows() <= budget {
		t.Fatalf("fixture rows = %d, budget = %d: not taller", l.TotalRows(), budget)
	}
	s.Apply(ScrollMoved{Delta: 10000})
	s.Follow = false
	frame := viewportFor(s)
	if frame.vp.Offset != 0 {
		t.Fatalf("max-scroll offset = %d, want 0", frame.vp.Offset)
	}
	if !frame.vp.Marker {
		t.Fatal("max-scroll viewport lost its marker")
	}
	rows := frame.index.Rows(frame.vp.Offset, frame.vp.Height)
	if len(rows) == 0 || !strings.Contains(rows[0].Plain(), "row block 0") {
		t.Fatalf("first row at top = %q, want block 0", rows[0].Plain())
	}
}

// Scrolling up detaches; scrolling back to the live edge reattaches.
func TestViewportDetachAndReattach(t *testing.T) {
	s := newViewportState(60, 80, 24)
	s.Apply(ScrollMoved{Delta: 5})
	if s.Follow {
		t.Fatal("scroll up did not detach")
	}
	frame := viewportFor(s)
	if frame.vp.Following || !frame.vp.Marker {
		t.Fatalf("detached viewport = %+v", frame.vp)
	}
	s.Apply(ScrollMoved{Delta: -5})
	frame = viewportFor(s)
	if !frame.vp.Following || frame.vp.Marker {
		t.Fatalf("reattached viewport = %+v", frame.vp)
	}
	if frame.vp.Offset != frame.vp.TotalRows-frame.vp.Height {
		t.Fatalf("following offset = %d, want bottom %d", frame.vp.Offset, frame.vp.TotalRows-frame.vp.Height)
	}
}

// Append while following: the live edge stays visible.
func TestViewportAppendWhileFollowingTracksBottom(t *testing.T) {
	s := newViewportState(60, 80, 24)
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.syncTranscript()
	before := p.transcriptFrame.vp.TotalRows

	p.state.Apply(Notice{Text: "fresh output"})
	p.syncTranscript()
	if !p.transcriptFrame.vp.Following {
		t.Fatal("follow mode detached on append")
	}
	if got := p.transcriptFrame.vp.Offset; got != p.transcriptFrame.vp.TotalRows-p.transcriptFrame.vp.Height {
		t.Fatalf("offset = %d, want bottom %d", got, p.transcriptFrame.vp.TotalRows-p.transcriptFrame.vp.Height)
	}
	if p.transcriptFrame.vp.TotalRows <= before {
		t.Fatal("append did not grow the document")
	}
	rows := p.transcriptFrame.index.Rows(p.transcriptFrame.vp.Offset, p.transcriptFrame.vp.Height)
	found := false
	for _, row := range rows {
		if strings.Contains(row.Plain(), "fresh output") {
			found = true
		}
	}
	if !found {
		t.Fatal("new output is not visible at the live edge")
	}
	// Hit testing resolves the freshly appended row, not a stale pre-append
	// geometry.
	last := rows[len(rows)-1]
	y := ComputeLayout(p.state).HeaderLines + len(rows) - 1
	if pos, ok := p.positionAt(1, y, false); !ok || pos.BlockID != last.BlockID {
		t.Fatalf("fresh row hit = (%v,%v), want block %s", pos, ok, last.BlockID)
	}
}

// Append while detached: the content under the reading anchor must not move.
func TestViewportAppendWhileDetachedPinsAnchor(t *testing.T) {
	s := newViewportState(60, 80, 24)
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.state.Apply(ScrollMoved{Delta: 10})
	p.syncTranscript()
	before := p.transcriptFrame
	topBefore := viewportTopRow(t, before)
	beforeOffset := before.vp.Offset

	p.state.Apply(Notice{Text: "streamed while detached"})
	p.syncTranscript()
	topAfter := viewportTopRow(t, p.transcriptFrame)
	if topAfter.BlockID != topBefore.BlockID || topAfter.Row != topBefore.Row {
		t.Fatalf("top anchor moved from (%s,%d) to (%s,%d)", topBefore.BlockID, topBefore.Row, topAfter.BlockID, topAfter.Row)
	}
	if p.transcriptFrame.vp.Offset != beforeOffset {
		t.Fatalf("offset moved from %d to %d while detached", beforeOffset, p.transcriptFrame.vp.Offset)
	}
	if !p.transcriptFrame.vp.Marker {
		t.Fatal("detached viewport lost its position marker")
	}
}

func historicalToolState(t *testing.T, blocks int) (State, string) {
	t.Helper()
	s := newViewportState(blocks, 80, 24)
	entries := append([]Entry(nil), s.Entries...)
	at := 10
	if at > len(entries) {
		at = len(entries) / 2
	}
	tool := Entry{Kind: EntryTool, Tool: &ToolEntry{
		ID:         "hist",
		Name:       "read",
		Status:     ToolDone,
		Invocation: "read many lines",
		Summary:    "20 lines",
		Text:       strings.Repeat("body line\n", 20),
	}}
	entries = append(entries[:at], append([]Entry{tool}, entries[at:]...)...)
	s.Apply(HistoryReplaced{Entries: entries})
	return s, "hist"
}

// Expanding a block above the viewport must not teleport the reader: the same
// block and row stay at the top.
func TestViewportExpansionAboveKeepsAnchor(t *testing.T) {
	s, id := historicalToolState(t, 80)
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.state.Apply(ScrollMoved{Delta: 30})
	p.syncTranscript()
	topBefore := viewportTopRow(t, p.transcriptFrame)

	p.state.Apply(ToolToggled{ID: id})
	p.syncTranscript()
	topAfter := viewportTopRow(t, p.transcriptFrame)
	if topAfter.BlockID != topBefore.BlockID || topAfter.Row != topBefore.Row {
		t.Fatalf("expansion above moved anchor: (%s,%d) -> (%s,%d)",
			topBefore.BlockID, topBefore.Row, topAfter.BlockID, topAfter.Row)
	}
	if !p.state.tool(id).Expanded {
		t.Fatal("historical block did not expand")
	}
}

// Collapsing a block above the viewport keeps the anchor too.
func TestViewportCollapseAboveKeepsAnchor(t *testing.T) {
	s, id := historicalToolState(t, 80)
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.state.Apply(ToolToggled{ID: id}) // expanded
	p.state.Apply(ScrollMoved{Delta: 30})
	p.syncTranscript()
	topBefore := viewportTopRow(t, p.transcriptFrame)

	p.state.Apply(ToolToggled{ID: id}) // collapse
	p.syncTranscript()
	topAfter := viewportTopRow(t, p.transcriptFrame)
	if topAfter.BlockID != topBefore.BlockID || topAfter.Row != topBefore.Row {
		t.Fatalf("collapse above moved anchor: (%s,%d) -> (%s,%d)",
			topBefore.BlockID, topBefore.Row, topAfter.BlockID, topAfter.Row)
	}
}

// Resize reflows rows but preserves the logical reading position and every
// block's expanded state.
func TestViewportResizePreservesAnchorAndExpansion(t *testing.T) {
	s := syntheticDocument(120, 120)
	s.Apply(ScrollMoved{Delta: 15})
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	p.syncTranscript()
	topBefore := viewportTopRow(t, p.transcriptFrame)

	expandedBefore := 0
	for _, entry := range p.state.Entries {
		if entryExpanded(entry) {
			expandedBefore++
		}
	}
	idsBefore := make([]BlockID, len(p.state.Entries))
	for i, entry := range p.state.Entries {
		idsBefore[i] = entry.ID
	}

	for _, width := range []int{80, 120, 40, 160, 80, 120} {
		p.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		topAfter := viewportTopRow(t, p.transcriptFrame)
		if topAfter.BlockID != topBefore.BlockID {
			t.Fatalf("width %d moved anchor block %s -> %s", width, topBefore.BlockID, topAfter.BlockID)
		}
		// A reflow can split a block differently; compare the block, and the
		// local row only when the block's height is unchanged.
		if p.transcriptFrame.index.BlockHeight(topAfter.BlockIndex) == 1 && topAfter.Row != 0 {
			t.Fatalf("width %d produced impossible row %d", width, topAfter.Row)
		}
		expandedAfter := 0
		for i, entry := range p.state.Entries {
			if entryExpanded(entry) {
				expandedAfter++
			}
			if entry.ID != idsBefore[i] {
				t.Fatalf("width %d changed block ID at %d: %s -> %s", width, i, idsBefore[i], entry.ID)
			}
		}
		if expandedAfter != expandedBefore {
			t.Fatalf("width %d changed expanded blocks %d -> %d", width, expandedBefore, expandedAfter)
		}
	}
}

// Exactly viewport height: the document fits, so no marker and no offset.
func TestViewportExactHeightDocument(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	budget := transcriptBudget(s, ComputeLayout(s))
	s.Apply(Notice{Text: strings.Repeat("x\n", budget)})
	l := layoutFor(s)
	if l.TotalRows() != budget {
		t.Fatalf("document rows = %d, want exact budget %d", l.TotalRows(), budget)
	}
	frame := viewportFor(s)
	if frame.vp.Offset != 0 || frame.vp.Marker || !frame.vp.Following {
		t.Fatalf("viewport = %+v, want a fitting document", frame.vp)
	}
}

// A height-only resize changes the window, not the rows: it must not reflow
// the document.
func TestHeightOnlyResizeDoesNotReflow(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state = syntheticDocument(300, 100)
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	p.syncTranscript()
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	p.syncTranscript()
	if p.transcriptIndex.LastRendered != 0 {
		t.Fatalf("height-only resize rendered %d blocks, want 0", p.transcriptIndex.LastRendered)
	}
	if p.transcriptIndex.Reflowed {
		t.Fatal("height-only resize marked a full reflow")
	}
	if p.transcriptFrame.vp.Height == 0 {
		t.Fatal("viewport height did not update")
	}
}

// Offsets are clamped for every intent, including overshoot and stale state.
func TestViewportOffsetAlwaysInRange(t *testing.T) {
	for _, blocks := range []int{0, 1, 2, 10, 200} {
		for _, delta := range []int{-10000, -5, -1, 0, 1, 5, 10000} {
			s := newViewportState(blocks, 60, 20)
			s.Apply(ScrollMoved{Delta: delta})
			frame := viewportFor(s)
			if frame.vp.Offset < 0 || frame.vp.Offset > frame.vp.MaxOffset {
				t.Fatalf("blocks=%d delta=%d offset=%d max=%d", blocks, delta, frame.vp.Offset, frame.vp.MaxOffset)
			}
			if frame.vp.Marker && frame.vp.Height >= transcriptBudget(s, ComputeLayout(s)) {
				t.Fatalf("blocks=%d delta=%d marker did not reserve a row", blocks, delta)
			}
		}
	}
}

// Growth while detached inflates the compatibility Scroll mirror; the same
// number of wheel ticks the user applied must still return to the live edge.
func TestScrollDownReattachesAfterContentGrowth(t *testing.T) {
	s := newViewportState(30, 80, 20)
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	p.state.Apply(ScrollMoved{Delta: 5})
	for i := 0; i < 20; i++ {
		p.state.Apply(AssistantDelta{Text: fmt.Sprintf("growth %d with some wrapping words\nsecond line\n", i)})
	}
	p.syncTranscript()
	if p.state.Follow {
		t.Fatal("fixture did not stay detached")
	}
	p.state.Apply(ScrollMoved{Delta: -5})
	if !p.state.Follow || p.state.Scroll != 0 || p.state.scrollIntent != 0 {
		t.Fatalf("five ticks down did not reattach: follow=%v scroll=%d intent=%d",
			p.state.Follow, p.state.Scroll, p.state.scrollIntent)
	}
}

// Repeated wheel ticks clamp at both ends and never leave a negative or
// oversized offset.
func TestRepeatedWheelEventsClamp(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state = newViewportState(80, 80, 20)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	for i := 0; i < 100; i++ {
		p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress, X: 1, Y: 3})
	}
	p.syncTranscript()
	if vp := p.transcriptFrame.vp; vp.Offset < 0 || vp.Offset > vp.MaxOffset {
		t.Fatalf("wheel-up offset = %d, max = %d", vp.Offset, vp.MaxOffset)
	}
	for i := 0; i < 100; i++ {
		p.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress, X: 1, Y: 3})
	}
	p.syncTranscript()
	if vp := p.transcriptFrame.vp; !vp.Following || vp.Offset != vp.MaxOffset {
		t.Fatalf("wheel-down did not reattach: %+v", vp)
	}
}

// Home reaches the top of the transcript while the composer is idle; End
// returns to the live edge.
func TestHomeAndEndReachTranscriptEdges(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state = newViewportState(60, 80, 20)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	p.state.Apply(ScrollMoved{Delta: 5})
	p.syncTranscript()
	if p.transcriptFrame.vp.Offset == 0 {
		t.Fatal("fixture did not scroll away from the top")
	}
	p.handleKey(navKey("home"))
	p.syncTranscript()
	if p.transcriptFrame.vp.Offset != 0 || p.transcriptFrame.vp.Following {
		t.Fatalf("home: offset=%d following=%v, want top and detached",
			p.transcriptFrame.vp.Offset, p.transcriptFrame.vp.Following)
	}
	p.handleKey(navKey("end"))
	p.syncTranscript()
	if !p.transcriptFrame.vp.Following {
		t.Fatal("end did not return to the live edge")
	}
}

// Expanding at the live edge keeps following the bottom; the new rows are
// visible rather than leaving the viewport pinned to the old height.
func TestViewportExpansionWhileFollowingTracksBottom(t *testing.T) {
	s, id := historicalToolState(t, 60)
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.state.Apply(ScrollToBottom{})
	p.syncTranscript()
	if !p.transcriptFrame.vp.Following {
		t.Fatal("fixture did not follow")
	}
	p.state.Apply(ToolToggled{ID: id})
	p.syncTranscript()
	vp := p.transcriptFrame.vp
	if !vp.Following || vp.Offset != vp.MaxOffset {
		t.Fatalf("expansion detached the live edge: %+v", vp)
	}
	if p.transcriptFrame.index.BlockHeight(0) == 0 {
		t.Fatal("layout lost its first block")
	}
}

// Scrolling while streaming must leave the viewport where the user put it.
func TestViewportScrollDuringStreamingDoesNotJump(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state = newViewportState(20, 80, 20)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	p.state.Apply(ScrollMoved{Delta: 6})
	p.syncTranscript()
	top := viewportTopRow(t, p.transcriptFrame)

	for i := 0; i < 10; i++ {
		p.state.Apply(AssistantDelta{Text: fmt.Sprintf("chunk %d with enough words to wrap at the width\n", i)})
		p.syncTranscript()
		now := viewportTopRow(t, p.transcriptFrame)
		if now.BlockID != top.BlockID || now.Row != top.Row {
			t.Fatalf("chunk %d moved the anchor: (%s,%d) -> (%s,%d)",
				i, top.BlockID, top.Row, now.BlockID, now.Row)
		}
	}
	if strings.Contains(ansi.Strip(Render(p.state)), "chunk 9") {
		// The newest chunk must be below the detached window, not forced
		// into view.
		t.Fatal("detached viewport was forced to the newest chunk")
	}
}
