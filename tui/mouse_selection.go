package tui

// Mouse interaction inside the owned viewport.
//
// Only active when the program requested mouse reporting. In terminal-owned
// selection mode the terminal keeps drag-select and copy, and no mouse bytes
// reach this code by contract (alternate scroll 1007 carries the wheel as
// arrow keys). When reporting is on, the application owns the pointer:
//
//	screen cell -> visible frame row -> block ID + local row + column
//
// There is no path from a mouse Y to a scrollback line. Hit testing always
// goes through the current TranscriptLayout, so it remains correct after
// scrolling, expansion, resize, and streamed appends.

import (
	"io"

	tea "github.com/charmbracelet/bubbletea"
)

// dragState tracks one pointer drag between press and release.
type dragState struct {
	Anchor  Position
	Current Position
	Moved   bool
	// Alt marks an alt-click: release copies the whole logical block instead
	// of toggling its expansion.
	Alt bool
}

// handleTranscriptMouse handles a non-wheel mouse event. It reports whether
// the event was consumed and returns the follow-up command, if any. A
// successful clipboard copy schedules the transient confirmation's expiry;
// empty, disabled, unsupported, or failed copies return nil and never show
// a false confirmation.
func (p *Program) handleTranscriptMouse(m tea.MouseMsg) (bool, tea.Cmd) {
	if !p.mouseReporting {
		return false, nil
	}
	switch m.Action {
	case tea.MouseActionPress:
		if m.Button != tea.MouseButtonLeft {
			return false, nil
		}
		pos, ok := p.positionAt(m.X, m.Y, false)
		if !ok {
			// A click outside the transcript clears the old selection but
			// never touches history or execution state.
			p.state.Selection = nil
			p.drag = nil
			return true, nil
		}
		p.drag = &dragState{Anchor: pos, Current: pos, Alt: m.Alt}
		p.state.Selection = nil
		p.trace(traceSelection)
		return true, nil

	case tea.MouseActionMotion:
		if p.drag == nil {
			return false, nil
		}
		// During an active drag any motion updates the focus, even when the
		// terminal reports it without a button (X10 fallback and
		// Windows Terminal quirks). Hover without a press has no drag and
		// is ignored above.
		if m.Button != tea.MouseButtonLeft && m.Button != tea.MouseButtonNone {
			return false, nil
		}
		pos, ok := p.positionAt(m.X, m.Y, true)
		if !ok {
			return true, nil
		}
		p.drag.Current = pos
		p.drag.Moved = pos != p.drag.Anchor
		if p.drag.Moved {
			p.state.Selection = &Selection{Start: p.drag.Anchor, End: pos}
			p.trace(traceSelection)
		} else {
			p.state.Selection = nil
		}
		return true, nil

	case tea.MouseActionRelease:
		if p.drag == nil {
			return true, nil
		}
		// Finalize on left-button and button-less releases. X10 encodes a
		// release as ButtonNone, and SGR as ButtonLeft; accepting only one
		// of them drops the copy on terminals using the other. Any other
		// button leaves the drag in place rather than finalizing it.
		if m.Button != tea.MouseButtonLeft && m.Button != tea.MouseButtonNone {
			return true, nil
		}
		drag := p.drag
		p.drag = nil
		if pos, ok := p.positionAt(m.X, m.Y, true); ok && pos != drag.Current {
			drag.Current = pos
			drag.Moved = pos != drag.Anchor
		}
		if !drag.Moved {
			p.state.Selection = nil
			if drag.Alt {
				if ok, cmd := p.copyBlockAt(drag.Anchor); !ok {
					p.trace(traceUnsupported)
					return true, nil
				} else {
					return true, cmd
				}
			}
			if !p.toggleBlockAt(drag.Anchor) {
				p.trace(traceSelection)
			}
			return true, nil
		}
		p.state.Selection = &Selection{Start: drag.Anchor, End: drag.Current}
		return true, p.copySelection()
	}
	return false, nil
}

// visibleRows returns the transcript rows currently painted and the number of
// marker rows above them.
func (p *Program) visibleRows() ([]LayoutRow, int, int, bool) {
	p.syncTranscript()
	frame := p.transcriptFrame
	if frame == nil || frame.index == nil {
		return nil, 0, 0, false
	}
	layout := ComputeLayout(p.state)
	if layout.Mode == LayoutMini || (layout.Mode == LayoutWide && p.state.Width <= 0) || p.transcriptModalOpen() {
		return nil, 0, 0, false
	}
	budget := transcriptBudget(p.state, layout)
	if budget <= 0 {
		return nil, 0, 0, false
	}
	markerRows := 0
	if frame.vp.Marker {
		markerRows = 1
	}
	rows := frame.index.Rows(frame.vp.Offset, frame.vp.Height)
	return rows, markerRows, layout.HeaderLines, true
}

// transcriptModalOpen reports whether an overlay owns the canvas in place of
// the transcript. A pending approval does not: it shares the conversation
// budget and leaves history selectable above it.
func (p *Program) transcriptModalOpen() bool {
	s := p.state
	return s.Picker != nil || s.Plane != nil || s.SupportPanel != nil ||
		s.StatusModal != nil || s.UsageModal != nil || s.InfoModal != nil ||
		s.InformationModal != nil || s.SessionModal != nil || s.HelpModal != nil ||
		s.ConnectModal != nil || s.ConnectKey != nil || s.Diff != nil ||
		s.QuitConfirm != nil || s.Transition != nil || s.Permissions != nil
}

// positionAt maps a terminal cell to a logical position.
//
// clamp controls drag behaviour at the edges: a click outside the painted
// rows misses, while a drag clamps to the first/last visible row so releasing
// slightly past the region still selects the content the user was over.
func (p *Program) positionAt(x, y int, clamp bool) (Position, bool) {
	rows, markerRows, top, ok := p.visibleRows()
	if !ok {
		return Position{}, false
	}
	if len(rows) == 0 {
		return Position{}, false
	}
	local := y - top - markerRows
	if local < 0 || local >= len(rows) {
		if !clamp {
			return Position{}, false
		}
		if local < 0 {
			local = 0
		} else {
			local = len(rows) - 1
		}
	}
	row := rows[local]
	col := x
	if col < 0 {
		col = 0
	}
	if width := CellWidth(row.Plain()); col > width {
		col = width
	}
	return Position{BlockID: row.BlockID, Row: row.Row, Column: col}, true
}

// toggleBlockAt flips a presentation block. It reports whether the click
// landed on something expandable.
func (p *Program) toggleBlockAt(pos Position) bool {
	index := p.state.BlockIndex(pos.BlockID)
	if index < 0 || p.state.Entries[index].Kind != EntryTool || p.state.Entries[index].Tool == nil {
		return false
	}
	expanded := p.state.Entries[index].Tool.Expanded
	if !p.state.ToggleBlock(pos.BlockID) {
		return false
	}
	if expanded {
		p.trace(traceCollapse)
	} else {
		p.trace(traceExpand)
	}
	p.syncTranscript()
	return true
}

// copySelection projects the current logical selection and emits its OSC 52
// payload to the terminal output transport. It returns the transient
// confirmation's expiry command on emission success and nil otherwise. Empty
// selections, a disabled or unsupported clipboard, a missing writer, and
// emission failures never show a confirmation, and the selection itself is
// always preserved.
//
// Success means the OSC 52 bytes were handed to the application's terminal
// output writer (termOut, os.Stdout in production). OSC 52 has no portable
// synchronous acknowledgement from the terminal, so this is emission success,
// not an OS-level clipboard guarantee; the end-to-end Ghostty/tmux proof
// lives in the transport closure report.
func (p *Program) copySelection() tea.Cmd {
	if p.state.Selection == nil || p.transcriptFrame == nil {
		return nil
	}
	text, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
	if !ok || text == "" {
		return nil
	}
	return p.queueClipboard(text)
}

// copyBlockAt copies one block's canonical logical content, independent of its
// current projection. An alt-click uses it; it never needs a selection range.
// It reports whether the copy reached the clipboard path and, on success,
// returns the confirmation's expiry command.
func (p *Program) copyBlockAt(pos Position) (bool, tea.Cmd) {
	block, ok := p.state.Document().ByID(pos.BlockID)
	if !ok {
		return false, nil
	}
	text := BlockCopyText(block.Entry)
	if text == "" {
		return false, nil
	}
	return true, p.queueClipboard(text)
}

func (p *Program) queueClipboard(text string) tea.Cmd {
	if p.clipboard == nil {
		p.trace(traceUnsupported)
		return nil
	}
	seq := p.clipboard(text)
	if seq == "" {
		// The clipboard path refused the payload: report nothing false
		// about the clipboard while keeping the selection intact.
		p.trace(traceUnsupported)
		return nil
	}
	out := p.termOut
	if out == nil {
		p.trace(traceUnsupported)
		return nil
	}
	// Synchronous emission to the terminal transport: one unbuffered write,
	// no queue, no next-frame delay. Only a successful write earns Copied.
	// The payload is never logged; only lengths and byte counts are traced.
	n, err := io.WriteString(out, seq)
	if err != nil || n != len(seq) {
		p.trace(traceUnsupported)
		return nil
	}
	p.trace(traceCopy)
	return p.markCopySuccess()
}
