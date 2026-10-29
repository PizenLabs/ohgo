package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Collapsed paste chips. A large multi-line paste becomes a single inline
// label in the composer — "[Paste #1 - 12 lines]" — while its full text is
// retained in State.Pastes for submission. The label lives literally in
// State.Input, so wrapping, the cursor, and editing all operate on exactly
// what the user sees; the payload is substituted back only at submit time.
//
// Keeping the label (rather than an invisible sentinel) as the token is what
// keeps the composer's rune math honest: there is no width mismatch between
// the stored text and the rendered rows, so vertical movement and the block
// cursor land where they appear to.

// pasteChipLineThreshold is the line count above which a bracketed paste
// collapses. Three lines is where raw expansion starts evicting the
// conversation for no benefit; shorter pastes stay literal.
const pasteChipLineThreshold = 3

// PasteChip is one collapsed paste: its payload and the line count shown in
// the inline label. ID is unique within a session and never reused, so a
// deleted chip can never resurrect an older payload through a stale label.
type PasteChip struct {
	ID      int
	Payload string
	Lines   int
}

// pasteChipLabel is the inline pill for a chip.
func pasteChipLabel(chip PasteChip) string {
	unit := "lines"
	if chip.Lines == 1 {
		unit = "line"
	}
	return fmt.Sprintf("[Paste #%d - %d %s]", chip.ID, chip.Lines, unit)
}

// pasteLineCount is the number of logical lines a paste occupies. An empty
// paste is zero, not one: nothing collapsed is nothing to label.
func pasteLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

// shouldCollapsePaste reports whether a bracketed paste is large enough to
// fold into a chip. Only a real bracketed paste collapses: a stream of
// individual keystrokes (a terminal without bracketed paste) is
// indistinguishable from typing and must stay literal.
func shouldCollapsePaste(pasted bool, text string) bool {
	return pasted && pasteLineCount(text) > pasteChipLineThreshold
}

// pasteLabelAt returns the chip whose label begins at rune index i, plus the
// label's rune length.
func pasteLabelAt(pastes []PasteChip, runes []rune, i int) (PasteChip, int, bool) {
	if i < 0 || i > len(runes) {
		return PasteChip{}, 0, false
	}
	for _, chip := range pastes {
		label := []rune(pasteChipLabel(chip))
		if i+len(label) <= len(runes) && string(runes[i:i+len(label)]) == string(label) {
			return chip, len(label), true
		}
	}
	return PasteChip{}, 0, false
}

// pasteLabelEndingAt returns the chip whose label ends at cursor, plus the
// label's start rune index.
func pasteLabelEndingAt(pastes []PasteChip, runes []rune, cursor int) (PasteChip, int, bool) {
	if cursor < 0 || cursor > len(runes) {
		return PasteChip{}, 0, false
	}
	for _, chip := range pastes {
		label := []rune(pasteChipLabel(chip))
		start := cursor - len(label)
		if start >= 0 && string(runes[start:cursor]) == string(label) {
			return chip, start, true
		}
	}
	return PasteChip{}, 0, false
}

// cursorLeftAtomic steps one rune left, or to the start of a chip label, so a
// single arrow never lands inside a pill.
func cursorLeftAtomic(pastes []PasteChip, text string, cursor int) int {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	if _, start, ok := pasteLabelEndingAt(pastes, runes, cursor); ok {
		return start
	}
	if cursor > 0 {
		return cursor - 1
	}
	return 0
}

// cursorRightAtomic steps one rune right, or to the end of a chip label.
func cursorRightAtomic(pastes []PasteChip, text string, cursor int) int {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	if _, length, ok := pasteLabelAt(pastes, runes, cursor); ok {
		return cursor + length
	}
	if cursor < len(runes) {
		return cursor + 1
	}
	return len(runes)
}

// pasteDeleteBefore removes the chip label ending at cursor, if any. It
// reports the new text and cursor plus the removed chip's id so the caller can
// drop its payload. Absent a chip it makes no change.
func pasteDeleteBefore(pastes []PasteChip, text string, cursor int) (string, int, int, bool) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	chip, start, ok := pasteLabelEndingAt(pastes, runes, cursor)
	if !ok {
		return text, cursor, 0, false
	}
	return string(append(runes[:start], runes[cursor:]...)), start, chip.ID, true
}

// pasteDeleteAt removes the chip label starting at cursor, if any.
func pasteDeleteAt(pastes []PasteChip, text string, cursor int) (string, int, int, bool) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	chip, length, ok := pasteLabelAt(pastes, runes, cursor)
	if !ok {
		return text, cursor, 0, false
	}
	return string(append(runes[:cursor], runes[cursor+length:]...)), cursor, chip.ID, true
}

// expandPastes substitutes every chip label in text with its payload. It walks
// left to right in one pass, so a payload that itself contains a
// label-looking string is emitted verbatim rather than expanded again.
func expandPastes(pastes []PasteChip, text string) string {
	if len(pastes) == 0 || text == "" {
		return text
	}
	runes := []rune(text)
	var sb strings.Builder
	sb.Grow(len(text))
	for i := 0; i < len(runes); {
		if chip, length, ok := pasteLabelAt(pastes, runes, i); ok {
			sb.WriteString(chip.Payload)
			i += length
			continue
		}
		sb.WriteRune(runes[i])
		i++
	}
	return sb.String()
}

// prunePastes drops chips whose label no longer appears in the input. It runs
// after every composer change, so deleting a label (or clearing the composer)
// releases the payload without a second event.
func (s *State) prunePastes() {
	if len(s.Pastes) == 0 {
		return
	}
	kept := make([]PasteChip, 0, len(s.Pastes))
	for _, chip := range s.Pastes {
		if strings.Contains(s.Input, pasteChipLabel(chip)) {
			kept = append(kept, chip)
		}
	}
	s.Pastes = kept
}

// snapPasteBoundary moves a cursor that landed strictly inside a chip label to
// the label's start, so vertical movement can never split a pill. A cursor
// outside every label is returned unchanged.
func snapPasteBoundary(pastes []PasteChip, text string, cursor int) int {
	if len(pastes) == 0 {
		return cursor
	}
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	for i := 0; i < len(runes); {
		if _, length, ok := pasteLabelAt(pastes, runes, i); ok {
			if cursor > i && cursor < i+length {
				return i
			}
			i += length
			continue
		}
		i++
	}
	return cursor
}

// pasteSpanIn returns the [start, end) rune span of the chip label strictly
// containing cursor, if the cursor was stranded inside one. A cursor on a
// label's edge is outside every span.
func pasteSpanIn(pastes []PasteChip, runes []rune, cursor int) (start, end int, ok bool) {
	for i := 0; i < len(runes); {
		if _, length, found := pasteLabelAt(pastes, runes, i); found {
			if cursor > i && cursor < i+length {
				return i, i + length, true
			}
			i += length
			continue
		}
		i++
	}
	return 0, 0, false
}

// snapPasteBoundaryForward moves a cursor stranded strictly inside a chip label
// to the label's end, so a line-end move never splits a pill. A cursor outside
// every label is returned unchanged.
func snapPasteBoundaryForward(pastes []PasteChip, text string, cursor int) int {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	if _, end, ok := pasteSpanIn(pastes, runes, cursor); ok {
		return end
	}
	return cursor
}

// expandPasteSpanStart moves start left to the head of any chip label it cuts
// through. A word kill scans by whitespace, and a label contains spaces, so
// without this it would stop mid-pill and leave a half-label behind; pulling
// start back consumes the whole label instead.
func expandPasteSpanStart(pastes []PasteChip, runes []rune, start int) int {
	for i := 0; i < len(runes); {
		if _, length, ok := pasteLabelAt(pastes, runes, i); ok {
			if i < start && start < i+length {
				start = i
			}
			i += length
			continue
		}
		i++
	}
	return start
}

// killToStartAtomic is killToStart with chip labels treated as atomic: a cursor
// stranded inside a pill snaps out, and a boundary inside one is pulled to the
// pill's head, so a kill removes a whole label or none of it.
func killToStartAtomic(pastes []PasteChip, text string, cursor int) (string, int) {
	runes := []rune(text)
	cursor = snapPasteBoundary(pastes, text, clampRune(cursor, len(runes)))
	start := expandPasteSpanStart(pastes, runes, lineStart(text, cursor))
	return string(append(runes[:start], runes[cursor:]...)), start
}

// killToEndAtomic is killToEnd with chip labels treated as atomic: the kill
// stops at a label's edge rather than slicing it, and a cursor inside a pill
// snaps out first.
func killToEndAtomic(pastes []PasteChip, text string, cursor int) (string, int) {
	runes := []rune(text)
	cursor = snapPasteBoundary(pastes, text, clampRune(cursor, len(runes)))
	end := lineEnd(text, cursor)
	if _, spanEnd, ok := pasteSpanIn(pastes, runes, end); ok {
		end = spanEnd
	}
	return string(append(runes[:cursor], runes[end:]...)), cursor
}

// killWordBackAtomic is killWordBack that treats a chip label as one word: a
// label's spaces no longer fool the scan into deleting only its tail.
func killWordBackAtomic(pastes []PasteChip, text string, cursor int) (string, int) {
	runes := []rune(text)
	cursor = snapPasteBoundary(pastes, text, clampRune(cursor, len(runes)))
	_, newCursor := killWordBack(text, cursor)
	newCursor = expandPasteSpanStart(pastes, runes, newCursor)
	return string(append(runes[:newCursor], runes[cursor:]...)), newCursor
}

// lineStartAtomic is lineStart that never parks the cursor inside a pill: a
// line start landing inside a label snaps to the label's head.
func lineStartAtomic(pastes []PasteChip, text string, cursor int) int {
	return snapPasteBoundary(pastes, text, lineStart(text, cursor))
}

// lineEndAtomic is lineEnd that never parks the cursor inside a pill: a line
// end landing inside a label snaps past it to the label's tail.
func lineEndAtomic(pastes []PasteChip, text string, cursor int) int {
	return snapPasteBoundaryForward(pastes, text, lineEnd(text, cursor))
}

// renderPasteChips replaces each chip label in one rendered composer row with
// a styled pill, returning the row and cursor adjusted for the escape runes
// the styling added. The cursor is a rune index; editing snaps it to a label's
// edges, so a position strictly inside a pill parks just before it. A label
// split by a display wrap matches neither half and stays plain — the payload,
// editing, and submission are unaffected because they all read State.Input.
func renderPasteChips(pastes []PasteChip, style lipgloss.Style, row string, cursor int) (string, int) {
	if len(pastes) == 0 || row == "" {
		return row, cursor
	}
	runes := []rune(row)
	var sb strings.Builder
	sb.Grow(len(row))
	outCursor := -1
	outPos := 0
	for i := 0; i < len(runes); {
		if i == cursor {
			outCursor = outPos
		}
		if _, length, ok := pasteLabelAt(pastes, runes, i); ok {
			if cursor > i && cursor < i+length {
				outCursor = outPos
			}
			styled := style.Render(string(runes[i : i+length]))
			sb.WriteString(styled)
			i += length
			outPos += len([]rune(styled))
			continue
		}
		sb.WriteRune(runes[i])
		i++
		outPos++
	}
	if outCursor < 0 {
		outCursor = outPos
	}
	return sb.String(), outCursor
}
