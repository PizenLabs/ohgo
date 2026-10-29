package tui

import (
	"strings"
	"unicode"

	"github.com/PizenLabs/ohgo/internal/history"
	"github.com/mattn/go-runewidth"
)

// Composer editing helpers. All operate on (text, rune cursor) and return the
// result, so they are pure and testable without a state machine. The caller
// folds the outcome back through ComposerChanged, which clamps.

// maxComposerRows bounds how many display rows the composer shows before it
// scrolls to the tail. It matches the layout's own cap so the region reserved
// and the region rendered agree.
const maxComposerRows = 6

// inlineSuggestion returns the history prompt that completes the current
// input, or "" when there is nothing to complete.
//
// A suggestion is offered only while the cursor sits at the end of the live
// input. Once the user moves into the middle of a line, inserting a tail would
// complete text ahead of the cursor rather than from it. Recalled entries and
// an open palette are excluded for the same reason: the user is already
// choosing, and a second completion would compete with that choice.
func (s State) inlineSuggestion() string {
	if s.HistCursor >= 0 || s.Cursor != len([]rune(s.Input)) {
		return ""
	}
	if len(paletteEntriesFor(s)) > 0 {
		return ""
	}
	return history.Suggestion(s.Submitted, s.Input)
}

// suggestionTail is the not-yet-typed remainder of the inline suggestion,
// sanitized as single-line chrome so it cannot inject a row break.
func (s State) suggestionTail() string {
	full := s.inlineSuggestion()
	if full == "" {
		return ""
	}
	return sanitizeInline(strings.TrimPrefix(full, s.Input))
}

// composerRows hard-wraps one logical line to width cells, preserving every
// rune (multiple spaces are not folded: what was typed is what is shown). It
// reports where the cursor lands as a row and rune index within the returned
// rows, or haveCursor=false when cursor is negative.
func composerRows(line string, cursor, width int) (rows []string, cursorRow, cursorRune int, haveCursor bool) {
	if width < 1 {
		width = 1
	}
	runes := []rune(line)
	if cursor > len(runes) {
		cursor = len(runes)
	}
	row := make([]rune, 0, width)
	rowW := 0
	flush := func() {
		rows = append(rows, string(row))
		row = row[:0]
		rowW = 0
	}
	for i := 0; i <= len(runes); i++ {
		if i == cursor {
			cursorRow, cursorRune, haveCursor = len(rows), len(row), true
		}
		if i == len(runes) {
			break
		}
		rw := runewidth.RuneWidth(runes[i])
		if rowW+rw > width && len(row) > 0 {
			flush()
		}
		row = append(row, runes[i])
		rowW += rw
	}
	flush()
	return rows, cursorRow, cursorRune, haveCursor
}

// layoutComposer folds every logical line of composed text to width cells and
// returns the display rows plus the cursor's absolute row and rune index.
//
// The cursor is a rune offset into text; it is mapped to a row here so the
// view can place the block cursor after reflow without splitting a rune or
// losing it when a line wraps.
func layoutComposer(text string, cursor, width int) (rows []string, cursorRow, cursorRune int, haveCursor bool) {
	if width < 1 {
		width = 1
	}
	offset := 0
	for _, line := range strings.Split(text, "\n") {
		lineRunes := []rune(line)
		lineCursor := -1
		if cursor >= offset && cursor <= offset+len(lineRunes) {
			lineCursor = cursor - offset
		}
		local, localRow, localRune, localHave := composerRows(line, lineCursor, width)
		base := len(rows)
		rows = append(rows, local...)
		if localHave {
			cursorRow, cursorRune, haveCursor = base+localRow, localRune, true
		}
		offset += len(lineRunes) + 1 // +1 for the newline
	}
	return rows, cursorRow, cursorRune, haveCursor
}

// composerScrollOffset advances a visible-row window so the cursor's display
// row stays painted. prev is the window shown on the previous frame, so the
// window follows the cursor with hysteresis: moving inside the window does not
// jitter, and crossing an edge scrolls exactly far enough to re-enter.
//
// The contract is offset <= cursorRow < offset+maxRows. Crossing the bottom
// scrolls the top text up (the caret line stays fully visible); rising above
// the top scrolls it back down. The window is clamped to the content, so it can
// never be positioned past the last row and emit blanks where text belongs.
func composerScrollOffset(prev, cursorRow, total, maxRows int) int {
	if maxRows < 1 {
		maxRows = 1
	}
	maxOffset := total - maxRows
	if maxOffset < 0 {
		maxOffset = 0
	}
	offset := clampRune(prev, maxOffset)
	if cursorRow < offset {
		offset = cursorRow
	}
	if cursorRow >= offset+maxRows {
		offset = cursorRow - maxRows + 1
	}
	return clampRune(offset, maxOffset)
}

// composerWindow slices formatted display rows to the visible window so the
// cursor row is always painted. It returns the rows to render and the window's
// first-row offset, the value the next frame folds back in as prev. An empty
// slice has no cursor row, so the window is the empty slice.
func composerWindow(rows []string, cursorRow, prev, maxRows int) ([]string, int) {
	if len(rows) == 0 {
		return rows, 0
	}
	offset := composerScrollOffset(prev, cursorRow, len(rows), maxRows)
	end := offset + maxRows
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], offset
}

// insertAt inserts s at the rune offset.
func insertAt(text string, cursor int, s string) string {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	out := make([]rune, 0, len(runes)+len([]rune(s)))
	out = append(out, runes[:cursor]...)
	out = append(out, []rune(s)...)
	out = append(out, runes[cursor:]...)
	return string(out)
}

// deleteBefore removes the rune ahead of the cursor's left, returning the new
// text and cursor.
func deleteBefore(text string, cursor int) (string, int) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	if cursor == 0 {
		return text, 0
	}
	return string(append(runes[:cursor-1], runes[cursor:]...)), cursor - 1
}

// deleteAt removes the rune under the cursor.
func deleteAt(text string, cursor int) (string, int) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	if cursor >= len(runes) {
		return text, cursor
	}
	return string(append(runes[:cursor], runes[cursor+1:]...)), cursor
}

// lineStart returns the offset of the start of the cursor's logical line.
func lineStart(text string, cursor int) int {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	for cursor > 0 && runes[cursor-1] != '\n' {
		cursor--
	}
	return cursor
}

// lineEnd returns the offset of the end of the cursor's logical line.
func lineEnd(text string, cursor int) int {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	for cursor < len(runes) && runes[cursor] != '\n' {
		cursor++
	}
	return cursor
}

// killToStart removes everything from the line start to the cursor, the
// ctrl+u half of line editing.
func killToStart(text string, cursor int) (string, int) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	start := lineStart(text, cursor)
	return string(append(runes[:start], runes[cursor:]...)), start
}

// killToEnd removes everything from the cursor to the line end.
func killToEnd(text string, cursor int) (string, int) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	end := lineEnd(text, cursor)
	return string(append(runes[:cursor], runes[end:]...)), cursor
}

// killWordBack removes the word behind the cursor plus its preceding
// whitespace, the ctrl+w unit. A "word" is a run of non-space runes, so paths
// and flags delete in one press rather than a character at a time.
func killWordBack(text string, cursor int) (string, int) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	rest := cursor
	for cursor > 0 && unicode.IsSpace(runes[cursor-1]) {
		cursor--
	}
	for cursor > 0 && !unicode.IsSpace(runes[cursor-1]) && runes[cursor-1] != '\n' {
		cursor--
	}
	return string(append(runes[:cursor], runes[rest:]...)), cursor
}

// composerRow is one display row of the composer: the rune offset in the
// source text where it begins and how many source runes it holds. Newlines
// belong to no row, so a cursor sitting on a newline maps to the row it ends.
type composerRow struct {
	start  int
	length int
}

// composerRowSpans partitions text into display rows exactly as composerRows
// wraps them, so a cursor mapped to a row here agrees with what the renderer
// painted. The wrapping rule is identical: a rune that would overflow the
// width starts the next row, and an empty logical line still occupies a row.
func composerRowSpans(text string, width int) []composerRow {
	if width < 1 {
		width = 1
	}
	var spans []composerRow
	offset := 0
	for _, line := range strings.Split(text, "\n") {
		runes := []rune(line)
		rowStart, rowLen, rowW := offset, 0, 0
		for i := 0; i < len(runes); i++ {
			rw := runewidth.RuneWidth(runes[i])
			if rowW+rw > width && rowLen > 0 {
				spans = append(spans, composerRow{start: rowStart, length: rowLen})
				rowStart, rowLen, rowW = offset+i, 0, 0
			}
			rowLen++
			rowW += rw
		}
		spans = append(spans, composerRow{start: rowStart, length: rowLen})
		offset += len(runes) + 1
	}
	return spans
}

// lineVertical moves the cursor to the same column of the display row above
// (dir -1) or below (dir +1). It reports false at the top or bottom row so
// the caller can fall back to history recall. The column is clamped to the
// target row, so a short wrapped row does not pull the cursor past its end.
func lineVertical(text string, cursor, width, dir int) (int, bool) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	spans := composerRowSpans(text, width)
	row, col := 0, 0
	placed := false
	for i, span := range spans {
		if cursor >= span.start && cursor <= span.start+span.length {
			row, col = i, cursor-span.start
			placed = true
			break
		}
	}
	if !placed {
		last := spans[len(spans)-1]
		row, col = len(spans)-1, last.length
	}
	target := row + dir
	if target < 0 || target >= len(spans) {
		return cursor, false
	}
	span := spans[target]
	if col > span.length {
		col = span.length
	}
	return span.start + col, true
}

// lineAboveWrapped moves the cursor one display row up, so vertical movement
// follows the wrapped text the user sees rather than only its logical lines.
func lineAboveWrapped(text string, cursor, width int) (int, bool) {
	return lineVertical(text, cursor, width, -1)
}

// lineBelowWrapped moves the cursor one display row down, wrapping-aware.
func lineBelowWrapped(text string, cursor, width int) (int, bool) {
	return lineVertical(text, cursor, width, +1)
}

// composerNoWrapWidth is wider than any logical line can be, so the
// logical-line helpers below reduce to the wrapped ones with one row per line.
const composerNoWrapWidth = 1 << 30

// lineAbove moves the cursor to the same column of the previous logical line,
// reporting false at the first line so the caller can fall back to history.
func lineAbove(text string, cursor int) (int, bool) {
	return lineVertical(text, cursor, composerNoWrapWidth, -1)
}

// lineBelow moves the cursor to the same column of the next logical line,
// reporting false at the last line.
func lineBelow(text string, cursor int) (int, bool) {
	return lineVertical(text, cursor, composerNoWrapWidth, +1)
}

func clampRune(pos, length int) int {
	if pos < 0 {
		return 0
	}
	if pos > length {
		return length
	}
	return pos
}
