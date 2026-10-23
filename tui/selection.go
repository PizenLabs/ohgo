package tui

// Application-owned logical selection.
//
// A selection names blocks and rows inside them, never terminal cursor
// coordinates. That is what lets it survive scrolling, expansion and resize:
// the layout can move or reflow, while Position still identifies the same
// logical content. Copy projects the selected rows to plain text and never
// includes ANSI styling, cursor controls, or renderer padding.

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// Position is a cell inside a block's rendered row.
//
// Row is a visual row inside the block; Column is a display-cell offset from
// the row's left edge. Neither is a screen coordinate.
type Position struct {
	BlockID BlockID
	Row     int
	Column  int
}

// Selection is a logical range with two endpoints. The endpoints may be given
// in either drag direction; consumers normalise them by document order.
type Selection struct {
	Start Position
	End   Position
}

// Empty reports whether the endpoints are indistinguishable.
func (s *Selection) Empty() bool {
	return s == nil || s.Start == s.End
}

type selectionRange struct {
	startBlock, startRow, startCol int
	endBlock, endRow, endCol       int
}

// normalizeSelection orders the endpoints by block position and clamps them to
// the current layout. It returns false when either block no longer exists or
// the range collapses.
func normalizeSelection(index *TranscriptLayout, sel *Selection) (selectionRange, bool) {
	if index == nil || sel == nil {
		return selectionRange{}, false
	}
	startBlock, ok := index.BlockIndexForID(sel.Start.BlockID)
	if !ok {
		return selectionRange{}, false
	}
	endBlock, ok := index.BlockIndexForID(sel.End.BlockID)
	if !ok {
		return selectionRange{}, false
	}
	startRow := clampInt(sel.Start.Row, 0, maxInt(index.BlockHeight(startBlock)-1, 0))
	endRow := clampInt(sel.End.Row, 0, maxInt(index.BlockHeight(endBlock)-1, 0))
	if index.BlockHeight(startBlock) == 0 || index.BlockHeight(endBlock) == 0 {
		return selectionRange{}, false
	}
	r := selectionRange{
		startBlock: startBlock,
		startRow:   startRow,
		startCol:   sel.Start.Column,
		endBlock:   endBlock,
		endRow:     endRow,
		endCol:     sel.End.Column,
	}
	if before(r.endBlock, r.endRow, r.endCol, r.startBlock, r.startRow, r.startCol) {
		r.startBlock, r.endBlock = r.endBlock, r.startBlock
		r.startRow, r.endRow = r.endRow, r.startRow
		r.startCol, r.endCol = r.endCol, r.startCol
	}
	if r.startBlock == r.endBlock && r.startRow == r.endRow && r.startCol == r.endCol {
		return selectionRange{}, false
	}
	return r, true
}

func before(aBlock, aRow, aCol, bBlock, bRow, bCol int) bool {
	if aBlock != bBlock {
		return aBlock < bBlock
	}
	if aRow != bRow {
		return aRow < bRow
	}
	return aCol < bCol
}

// rowSelectionInterval is the single definition of per-row selection geometry.
//
// Both highlight rendering (ApplySelectionHighlight) and clipboard projection
// (selectionRows/CopySelection) consume it, so a reversed drag paints exactly
// the cells it copies. It reports the half-open display-cell interval
// [startCol, endCol) for one visual row, clamped to the row's plain content
// width. Rows outside the canonical range report selected=false and must be
// left unpainted and uncopied. A blank row (width 0) never selects cells;
// callers preserve interior blanks as line breaks for the copy only.
func rowSelectionInterval(n selectionRange, block, row, width int) (int, int, bool) {
	if block < n.startBlock || block > n.endBlock {
		return 0, 0, false
	}
	// Rows in the endpoint blocks outside the endpoint rows are not selected.
	// Without these guards a drag starting mid-block would highlight every
	// row above its anchor and every row below its focus.
	if block == n.startBlock && row < n.startRow {
		return 0, 0, false
	}
	if block == n.endBlock && row > n.endRow {
		return 0, 0, false
	}
	if width <= 0 {
		return 0, 0, false
	}
	startCol, endCol := 0, width
	if block == n.startBlock && row == n.startRow {
		startCol = n.startCol
	}
	if block == n.endBlock && row == n.endRow {
		endCol = n.endCol
	}
	startCol = clampInt(startCol, 0, width)
	endCol = clampInt(endCol, 0, width)
	if endCol <= startCol {
		return 0, 0, false
	}
	return startCol, endCol, true
}

// selectedRow is one row of a normalized selection with its column budget.
type selectedRow struct {
	row      LayoutRow
	startCol int
	endCol   int
}

func selectionRows(index *TranscriptLayout, sel *Selection) []selectedRow {
	normalized, ok := normalizeSelection(index, sel)
	if !ok {
		return nil
	}
	var out []selectedRow
	for block := normalized.startBlock; block <= normalized.endBlock; block++ {
		height := index.BlockHeight(block)
		if height <= 0 {
			continue
		}
		first, last := 0, height-1
		if block == normalized.startBlock {
			first = normalized.startRow
		}
		if block == normalized.endBlock {
			last = normalized.endRow
		}
		for row := first; row <= last; row++ {
			layoutRow, ok := index.BlockRow(block, row)
			if !ok {
				continue
			}
			isStart := block == normalized.startBlock && row == normalized.startRow
			isEnd := block == normalized.endBlock && row == normalized.endRow
			fullRow := !isStart && !isEnd
			width := CellWidth(layoutRow.Plain())
			if width == 0 {
				// Blank separator/wrapped rows are still lines in the
				// document. Keep an interior one so a cross-block copy does
				// not silently concatenate two paragraphs.
				if fullRow {
					out = append(out, selectedRow{row: layoutRow})
				}
				continue
			}
			startCol, endCol, ok := rowSelectionInterval(normalized, block, row, width)
			if !ok {
				continue
			}
			out = append(out, selectedRow{row: layoutRow, startCol: startCol, endCol: endCol})
		}
	}
	return out
}

// CopySelection projects a selection to clipboard text.
//
// Every row is stripped of ANSI and right padding before the cell range is
// cut. Wrapped rows stay separate lines, matching what the user saw and
// dragged across; no renderer artifact can enter the payload.
func CopySelection(index *TranscriptLayout, sel *Selection) (string, bool) {
	rows := selectionRows(index, sel)
	if len(rows) == 0 {
		return "", false
	}
	lines := make([]string, 0, len(rows))
	for _, selected := range rows {
		plain := selected.row.Plain()
		line := CellSlice(plain, selected.startCol, selected.endCol)
		lines = append(lines, strings.TrimRight(line, " "))
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		return "", false
	}
	return logicalText(text), true
}

// HighlightSelection renders one styled row with the selected cell range in
// reverse video. Width is unchanged; the highlight is a terminal attribute,
// not inserted content.
//
// The interval uses the same cell semantics as CellSlice: a wide glyph is
// included when any of its cells intersect [startCol, endCol), and a tab
// advances to the renderer's next tab stop, so the highlight can never cover
// half a glyph while the copy takes the whole one (or vice versa). ANSI
// styling passes through untouched; only zero-width reverse-video markers are
// added.
func HighlightSelection(styled string, startCol, endCol int) string {
	if startCol < 0 {
		startCol = 0
	}
	if endCol <= startCol || styled == "" {
		return styled
	}
	runes := []rune(styled)
	var sb strings.Builder
	sb.Grow(len(styled) + len("\x1b[7m\x1b[27m"))
	col := 0
	// baseCol is the display column where the current grapheme started. A
	// combining mark occupies no cell of its own; it belongs to its base
	// character's cell, so ending a range right after the base keeps the
	// mark instead of splitting the grapheme.
	baseCol := 0
	inside := false
	selectedAny := false
	for i := 0; i < len(runes); {
		if advance, keep := scanEscape(runes, i); advance > 0 {
			if keep {
				seq := string(runes[i : i+advance])
				sb.WriteString(seq)
				// A reset inside the selected span would switch reverse
				// video off for the rest of the row. Re-assert it.
				if inside && (seq == "\x1b[0m" || seq == "\x1b[m") {
					sb.WriteString("\x1b[7m")
				}
			}
			i += advance
			continue
		}
		r := runes[i]
		if r == '\n' {
			// Rows never contain newlines; a stray one must not leak an
			// unclosed attribute onto the next row.
			if inside {
				sb.WriteString("\x1b[27m")
				inside = false
			}
			sb.WriteRune(r)
			i++
			continue
		}
		var w int
		zeroWidth := false
		if r == '\t' {
			w = CodeTabWidth - col%CodeTabWidth
		} else {
			rw := runewidth.RuneWidth(r)
			if rw < 0 {
				rw = 0
			}
			if rw == 0 {
				zeroWidth = true
			} else {
				w = rw
			}
		}
		var selected bool
		if zeroWidth {
			selected = baseCol >= startCol && baseCol < endCol
		} else {
			selected = col+w > startCol && col < endCol
		}
		if selected {
			if !inside {
				sb.WriteString("\x1b[7m")
				inside = true
				selectedAny = true
			}
		} else if inside {
			sb.WriteString("\x1b[27m")
			inside = false
		}
		sb.WriteRune(r)
		if !zeroWidth {
			baseCol = col
			col += w
		}
		i++
	}
	if inside {
		sb.WriteString("\x1b[27m")
	}
	if !selectedAny {
		return styled
	}
	return sb.String()
}

// CellSlice cuts a plain string by display cells. A wide glyph is included
// when any of its cells intersect the range, so a cut can never split a rune.
// A tab advances to the renderer's next tab stop, matching HighlightSelection
// and CellWidth, so copy and paint agree on tabbed rows.
func CellSlice(s string, start, end int) string {
	if end <= start || s == "" {
		return ""
	}
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	col := 0
	// baseCol tracks the grapheme start for combining marks; see
	// HighlightSelection for why they attach to the base cell.
	baseCol := 0
	for _, r := range s {
		if r == '\t' {
			w := CodeTabWidth - col%CodeTabWidth
			if col+w > start && col < end {
				b.WriteRune(r)
			}
			baseCol = col
			col += w
			continue
		}
		w := runewidth.RuneWidth(r)
		if w < 0 {
			w = 0
		}
		next := col + w
		if w == 0 {
			if baseCol >= start && baseCol < end {
				b.WriteRune(r)
			}
			continue
		}
		if next > start && col < end {
			b.WriteRune(r)
		}
		baseCol = col
		col = next
	}
	return b.String()
}

// ApplySelectionHighlight returns a frame row with the selection painted.
//
// It resolves the row's interval through rowSelectionInterval, the same
// definition the clipboard projection uses, so a reversed drag highlights
// exactly the cells it copies. Columns clamp to the row's plain content
// width: renderer padding past the content is never painted.
func ApplySelectionHighlight(index *TranscriptLayout, sel *Selection, row LayoutRow) string {
	if index == nil || sel == nil {
		return row.Styled
	}
	normalized, ok := normalizeSelection(index, sel)
	if !ok {
		return row.Styled
	}
	width := CellWidth(row.Plain())
	startCol, endCol, selected := rowSelectionInterval(normalized, row.BlockIndex, row.Row, width)
	if !selected {
		return row.Styled
	}
	return HighlightSelection(row.Styled, startCol, endCol)
}
