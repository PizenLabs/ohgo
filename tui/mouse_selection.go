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

import tea "github.com/charmbracelet/bubbletea"

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
// the event was consumed. Callers route wheel ticks to the scroll logic first.
func (p *Program) handleTranscriptMouse(m tea.MouseMsg) bool {
	if !p.mouseReporting {
		return false
	}
	switch m.Action {
	case tea.MouseActionPress:
		if m.Button != tea.MouseButtonLeft {
			return false
		}
		pos, ok := p.positionAt(m.X, m.Y, false)
		if !ok {
			// A click outside the transcript clears the old selection but
			// never touches history or execution state.
			p.state.Selection = nil
			p.drag = nil
			return true
		}
		p.drag = &dragState{Anchor: pos, Current: pos, Alt: m.Alt}
		p.state.Selection = nil
		p.trace(traceSelection)
		return true

	case tea.MouseActionMotion:
		if p.drag == nil {
			return false
		}
		// During an active drag any motion updates the focus, even when the
		// terminal reports it without a button (X10 fallback and
		// Windows Terminal quirks). Hover without a press has no drag and
		// is ignored above.
		if m.Button != tea.MouseButtonLeft && m.Button != tea.MouseButtonNone {
			return false
		}
		pos, ok := p.positionAt(m.X, m.Y, true)
		if !ok {
			return true
		}
		p.drag.Current = pos
		p.drag.Moved = pos != p.drag.Anchor
		if p.drag.Moved {
			p.state.Selection = &Selection{Start: p.drag.Anchor, End: pos}
			p.trace(traceSelection)
		} else {
			p.state.Selection = nil
		}
		return true

	case tea.MouseActionRelease:
		if p.drag == nil {
			return true
		}
		// Finalize on left-button and button-less releases. X10 encodes a
		// release as ButtonNone, and SGR as ButtonLeft; accepting only one
		// of them drops the copy on terminals using the other. Any other
		// button leaves the drag in place rather than finalizing it.
		if m.Button != tea.MouseButtonLeft && m.Button != tea.MouseButtonNone {
			return true
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
				if !p.copyBlockAt(drag.Anchor) {
					p.trace(traceUnsupported)
				}
				return true
			}
			if !p.toggleBlockAt(drag.Anchor) {
				p.trace(traceSelection)
			}
			return true
		}
		p.state.Selection = &Selection{Start: drag.Anchor, End: drag.Current}
		p.copySelection()
		return true
	}
	return false
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

// copySelection projects the current logical selection and queues its OSC 52
// write for the next frame.
func (p *Program) copySelection() {
	if p.state.Selection == nil || p.transcriptFrame == nil {
		return
	}
	text, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
	if !ok || text == "" {
		return
	}
	p.queueClipboard(text)
}

// copyBlockAt copies one block's canonical logical content, independent of its
// current projection. An alt-click uses it; it never needs a selection range.
func (p *Program) copyBlockAt(pos Position) bool {
	block, ok := p.state.Document().ByID(pos.BlockID)
	if !ok {
		return false
	}
	text := BlockCopyText(block.Entry)
	if text == "" {
		return false
	}
	p.queueClipboard(text)
	return true
}

func (p *Program) queueClipboard(text string) {
	if p.clipboard == nil {
		p.trace(traceUnsupported)
		return
	}
	p.pendingClipboard = p.clipboard(text)
	p.trace(traceCopy)
}
