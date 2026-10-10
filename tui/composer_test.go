package tui

import (
	"testing"
)

// The composer helpers are pure (text, cursor) functions: every case below
// pins a transformation the fingers rely on, not an implementation path.

func TestInsertAtLandsOnRunes(t *testing.T) {
	// Byte offsets would split a multibyte rune; the cursor is a rune index.
	if got := insertAt("héllo", 1, "X"); got != "hXéllo" {
		t.Errorf("insertAt = %q, want hXéllo", got)
	}
	if got := insertAt("ab", 99, "X"); got != "abX" {
		t.Errorf("insertAt past the end = %q, want abX", got)
	}
	if got := insertAt("ab", -3, "X"); got != "Xab" {
		t.Errorf("insertAt before the start = %q, want Xab", got)
	}
}

func TestDeleteBeforeTakesOneRune(t *testing.T) {
	text, cursor := deleteBefore("héllo", 2)
	if text != "hllo" || cursor != 1 {
		t.Errorf("deleteBefore = (%q, %d), want (hllo, 1)", text, cursor)
	}
	if text, cursor := deleteBefore("ab", 0); text != "ab" || cursor != 0 {
		t.Errorf("deleteBefore at zero = (%q, %d), want unchanged", text, cursor)
	}
}

func TestDeleteAtTakesTheRuneUnderTheCursor(t *testing.T) {
	text, cursor := deleteAt("abcd", 1)
	if text != "acd" || cursor != 1 {
		t.Errorf("deleteAt = (%q, %d), want (acd, 1)", text, cursor)
	}
	if text, cursor := deleteAt("ab", 2); text != "ab" || cursor != 2 {
		t.Errorf("deleteAt at end = (%q, %d), want unchanged", text, cursor)
	}
}

func TestLineEdgesStopAtNewlines(t *testing.T) {
	text := "first\nsecond line"
	if got := lineStart(text, 12); got != 6 {
		t.Errorf("lineStart = %d, want 6", got)
	}
	if got := lineEnd(text, 7); got != 17 {
		t.Errorf("lineEnd = %d, want 17", got)
	}
}

func TestKillToStartKeepsTheNewline(t *testing.T) {
	// ctrl+u kills to the line start, not the buffer start: the line above
	// survives. Cursor 10 sits on the 'n' of "second".
	text, cursor := killToStart("first\nsecond", 10)
	if text != "first\nnd" || cursor != 6 {
		t.Errorf("killToStart = (%q, %d), want (first\\nnd, 6)", text, cursor)
	}
}

func TestKillToEndStopsAtTheNewline(t *testing.T) {
	text, cursor := killToEnd("first\nsecond", 1)
	if text != "f\nsecond" || cursor != 1 {
		t.Errorf("killToEnd = (%q, %d), want (f\\nsecond, 1)", text, cursor)
	}
}

func TestKillWordBackTakesWordAndSpace(t *testing.T) {
	// Paths delete in one press: the unit is a whitespace run, not a word
	// character class.
	text, cursor := killWordBack("read src/a.go ", 14)
	if text != "read " || cursor != 5 {
		t.Errorf("killWordBack = (%q, %d), want (read , 5)", text, cursor)
	}
	text, cursor = killWordBack("read ", 5)
	if text != "" || cursor != 0 {
		t.Errorf("killWordBack trailing space = (%q, %d), want empty", text, cursor)
	}
}

func TestLineAboveFallsBackAtTheFirstLine(t *testing.T) {
	if _, ok := lineAbove("single", 3); ok {
		t.Error("lineAbove on one line reported a move; the caller falls back to history on false")
	}
	if pos, ok := lineAbove("ab\ncdef", 6); !ok || pos != 2 {
		t.Errorf("lineAbove = (%d, %v), want (2, true): column clamped to the short line's end", pos, ok)
	}
}

func TestLineBelowFallsBackAtTheLastLine(t *testing.T) {
	if _, ok := lineBelow("ab\ncd", 5); ok {
		t.Error("lineBelow on the last line reported a move")
	}
	if pos, ok := lineBelow("abcdef\nxy", 4); !ok || pos != 9 {
		t.Errorf("lineBelow = (%d, %v), want (9, true): column clamped to the short line", pos, ok)
	}
}
