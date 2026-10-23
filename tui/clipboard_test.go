package tui

import (
	"encoding/base64"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestOSC52SequenceFraming(t *testing.T) {
	text := "hello\nworld"
	got := OSC52Sequence(text)
	if !strings.HasPrefix(got, "\x1b]52;c;") || !strings.HasSuffix(got, "\x07") {
		t.Fatalf("bad framing: %q", got)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	if string(decoded) != text {
		t.Fatalf("decoded = %q, want %q", decoded, text)
	}
	if OSC52Sequence("") != "" {
		t.Fatal("empty text produced a clipboard clear")
	}
}

func TestOSC52TmuxAndScreenPackaging(t *testing.T) {
	plain := OSC52Sequence("hi")
	tmux := OSC52SequenceFor("hi", true, "xterm-256color")
	if tmux != "\x1bPtmux;\x1b"+plain+"\x1b\\" {
		t.Fatalf("tmux wrapper = %q", tmux)
	}
	screen := OSC52SequenceFor("hi", false, "screen-256color")
	if !strings.HasPrefix(screen, "\x1bP\x1b]52;c;") || !strings.HasSuffix(screen, "\x07\x1b\\") {
		t.Fatalf("screen wrapper = %q", screen)
	}
	if got := OSC52SequenceFor("hi", false, "xterm-256color"); got != plain {
		t.Fatalf("plain terminal wrapper = %q", got)
	}
}

func TestClipboardAvailability(t *testing.T) {
	for _, term := range []string{"", "dumb", " DUMB "} {
		if ClipboardAvailable(term) {
			t.Fatalf("ClipboardAvailable(%q) = true", term)
		}
	}
	for _, term := range []string{"xterm-256color", "screen-256color", "tmux-256color"} {
		if !ClipboardAvailable(term) {
			t.Fatalf("ClipboardAvailable(%q) = false", term)
		}
	}
}

// In the default selection mode the terminal owns drag-copy. If a mouse event
// still arrives, the application must not build a competing selection.
func TestSelectionModeIgnoresPointerDrags(t *testing.T) {
	p := NewProgram(nil, "test", 0) // no WithMouseReporting
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	top := ComputeLayout(p.state).HeaderLines
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: top})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 5, Y: top})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 5, Y: top})
	if p.state.Selection != nil {
		t.Fatalf("terminal-owned mode created %+v", p.state.Selection)
	}
	if p.pendingClipboard != "" {
		t.Fatal("terminal-owned mode queued an OSC 52 copy")
	}
}

// A program-owned copy emits exactly one OSC payload on the next frame and
// then stops repeating it.
func TestProgramCopyEmitsOSC52Once(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	p := NewProgram(nil, "test", 0)
	p.clipboard = OSC52Sequence
	p.transcriptFrame = &transcriptFrame{index: l}
	p.state.Entries = s.Entries
	id := blockIDAt(p.state.Entries, 0)
	p.state.Selection = &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: 5},
	}
	p.copySelection()
	if p.pendingClipboard == "" {
		t.Fatal("copy produced no clipboard sequence")
	}
	view := p.View()
	if !strings.HasPrefix(view, "\x1b]52;c;") {
		t.Fatalf("frame did not carry the clipboard sequence: %q", view[:min(len(view), 20)])
	}
	// Bubble Tea truncates each rendered line to the terminal width. The
	// zero-width OSC payload must survive that pass intact.
	first := strings.SplitN(view, "\n", 2)[0]
	if truncated := ansi.Truncate(first, 80, ""); !strings.HasPrefix(truncated, "\x1b]52;c;") {
		t.Fatalf("renderer-width truncation dropped the clipboard sequence: %q", truncated)
	}
	if again := p.View(); strings.HasPrefix(again, "\x1b]52;c;") {
		t.Fatal("clipboard sequence repeated on a later frame")
	}
}

// Disabled clipboard means no sequence, not a process fallback and not a
// false success indicator.
func TestProgramCopyDisabledIsSilent(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.clipboard = nil
	p.state = selectionFixture()
	p.transcriptFrame = viewportFor(p.state)
	id := blockIDAt(p.state.Entries, 0)
	p.state.Selection = &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: 3},
	}
	p.copySelection()
	if p.pendingClipboard != "" {
		t.Fatalf("disabled clipboard queued %q", p.pendingClipboard)
	}
}

// The clipboard payload is the logical selection, never the styled projection.
func TestClipboardPayloadHasNoANSIRendererArtifacts(t *testing.T) {
	s := selectionFixture()
	l := layoutFor(s)
	id := blockIDAt(s.Entries, 1)
	text, ok := CopySelection(l, &Selection{
		Start: Position{BlockID: id, Row: 0, Column: 0},
		End:   Position{BlockID: id, Row: 0, Column: 11},
	})
	if !ok {
		t.Fatal("selection copied nothing")
	}
	seq := OSC52Sequence(text)
	payload := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decoded), "\x1b") {
		t.Fatalf("payload contains ANSI: %q", decoded)
	}
	if string(decoded) != "second line" {
		t.Fatalf("payload = %q, want logical text", decoded)
	}
}

// Mouse press/motion/release drives the logical selection and the clipboard.
func TestMouseDragSelectsAndCopies(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.clipboard = OSC52Sequence
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	rows, marker, top, ok := p.visibleRows()
	if !ok || len(rows) < 2 {
		t.Fatal("no rows to select")
	}
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 5, Y: y})
	if p.state.Selection == nil {
		t.Fatal("drag produced no selection")
	}
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 5, Y: y})
	if p.pendingClipboard == "" {
		t.Fatal("release did not copy the selection")
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(p.pendingClipboard, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "hello") {
		t.Fatalf("copied %q, want the selected prompt text", decoded)
	}
}

// A historical block can be expanded long after newer output exists. The
// click resolves through the current layout to a stable ID; no scrollback is
// rewritten and the viewport is not stolen.
func TestMouseExpandsHistoricalBlockWithoutScrollbackMutation(t *testing.T) {
	s := newViewportState(800, 80, 24)
	entries := append([]Entry(nil), s.Entries...)
	tool := Entry{Kind: EntryTool, Tool: &ToolEntry{
		ID: "old", Name: "read", Status: ToolDone,
		Invocation: "read old.go", Summary: "two lines",
		Text: "first\nsecond",
	}}
	entries = append(entries[:3], append([]Entry{tool}, entries[3:]...)...)
	s.Apply(HistoryReplaced{Entries: entries})

	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.handleKey(navKey("home"))
	p.syncTranscript()
	rows, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	clicked := false
	anchorBefore := p.transcriptFrame.vp.Anchor
	for i, row := range rows {
		entry, ok := p.state.Document().ByID(row.BlockID)
		if !ok || entry.Kind != EntryTool || !entry.Expandable {
			continue
		}
		y := top + marker + i
		p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 1, Y: y})
		p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 1, Y: y})
		clicked = true
		break
	}
	if !clicked {
		t.Fatal("historical tool block was not visible to click")
	}
	if !p.state.tool("old").Expanded {
		t.Fatal("historical tool did not expand")
	}
	if p.transcriptFrame.vp.Anchor != anchorBefore {
		t.Fatalf("click moved the viewport anchor: %+v -> %+v", anchorBefore, p.transcriptFrame.vp.Anchor)
	}

	p.state.Apply(ScrollToBottom{})
	p.syncTranscript()
	if !p.transcriptFrame.vp.Following {
		t.Fatal("returning to the bottom did not reattach")
	}
	if !p.state.tool("old").Expanded {
		t.Fatal("scrollback interaction lost the expansion")
	}
}

// Alt-click copies the whole canonical block without changing presentation
// or execution state.
func TestMouseAltClickCopiesWholeBlock(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.clipboard = OSC52Sequence
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	rows, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	userID := blockIDAt(p.state.Entries, 0)
	for i, row := range rows {
		if row.BlockID != userID {
			continue
		}
		y := top + marker + i
		p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 1, Y: y, Alt: true})
		p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 1, Y: y, Alt: true})
		break
	}
	if p.pendingClipboard == "" {
		t.Fatal("alt-click copied nothing")
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(p.pendingClipboard, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "hello world" {
		t.Fatalf("alt-click copied %q, want the whole block", decoded)
	}
	if p.state.Selection != nil {
		t.Fatal("alt-click created a selection")
	}
}

// Clicks cannot become execution or authority: a click on a tool block only
// flips expansion, and a click on ordinary history changes no runtime state.
func TestMouseClickNeverSubmitsOrAuthorizes(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(30, 80, 24)
	p.state.Apply(ToolStarted{ID: "a", Name: "read", Summary: "read a"})
	p.state.Apply(ToolFinished{ID: "a", Text: "a\nb", Summary: "two lines"})
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.syncTranscript()
	rows, marker, top, ok := p.visibleRows()
	if !ok || len(rows) == 0 {
		t.Fatal("no visible rows")
	}
	inputBefore := p.state.Input
	busyBefore := p.state.Status.Busy
	entriesBefore := len(p.state.Entries)
	for i := range rows {
		y := top + marker + i
		pos, _ := p.positionAt(1, y, false)
		entry, ok := p.state.Document().ByID(pos.BlockID)
		if !ok {
			continue
		}
		if entry.Kind == EntryTool && entry.Expandable {
			p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 1, Y: y})
			p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 1, Y: y})
			if !p.state.tool("a").Expanded {
				t.Fatal("click did not expand the historical tool")
			}
			break
		}
	}
	if p.state.Input != inputBefore || p.state.Status.Busy != busyBefore || len(p.state.Entries) != entriesBefore {
		t.Fatal("a mouse click mutated runtime or prompt state")
	}
	if p.pending != nil {
		t.Fatal("a mouse click opened an approval")
	}
}
