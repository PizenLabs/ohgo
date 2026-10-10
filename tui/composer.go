package tui

import (
	"unicode"
)

// Composer editing helpers. All operate on (text, rune cursor) and return the
// result, so they are pure and testable without a state machine. The caller
// folds the outcome back through ComposerChanged, which clamps.

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

// lineAbove moves the cursor to the same column of the previous logical line,
// reporting false at the first line so the caller can fall back to history.
func lineAbove(text string, cursor int) (int, bool) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	start := lineStart(text, cursor)
	if start == 0 {
		return cursor, false
	}
	col := cursor - start
	prevEnd := start - 1
	prevStart := 0
	for i := prevEnd - 1; i >= 0; i-- {
		if runes[i] == '\n' {
			prevStart = i + 1
			break
		}
	}
	width := prevEnd - prevStart
	if col > width {
		col = width
	}
	return prevStart + col, true
}

// lineBelow moves the cursor to the same column of the next logical line,
// reporting false at the last line.
func lineBelow(text string, cursor int) (int, bool) {
	runes := []rune(text)
	cursor = clampRune(cursor, len(runes))
	end := lineEnd(text, cursor)
	if end >= len(runes) {
		return cursor, false
	}
	col := cursor - lineStart(text, cursor)
	nextStart := end + 1
	nextEnd := len(runes)
	for i := nextStart; i < len(runes); i++ {
		if runes[i] == '\n' {
			nextEnd = i
			break
		}
	}
	width := nextEnd - nextStart
	if col > width {
		col = width
	}
	return nextStart + col, true
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
