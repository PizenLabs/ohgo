package tui

import (
	"strings"
	"testing"
)

// Cell widths drive every layout decision; byte length must never stand in.

func TestCellWidthCountsWideGlyphs(t *testing.T) {
	if got := CellWidth("hello"); got != 5 {
		t.Errorf("ascii = %d, want 5", got)
	}
	// Two CJK characters occupy four cells, not two runes or six bytes.
	if got := CellWidth("日本"); got != 4 {
		t.Errorf("cjk = %d, want 4", got)
	}
	if got := CellWidth("\x1b[31mhi\x1b[0m"); got != 2 {
		t.Errorf("ansi = %d, want 2: escapes must not count", got)
	}
}

func TestTruncateToWidthRespectsCells(t *testing.T) {
	got := TruncateToWidth("日本語abc", 5)
	if CellWidth(got) > 5 {
		t.Errorf("truncated = %q (%d cells), want <= 5", got, CellWidth(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated = %q, want the ellipsis marker", got)
	}
	if got := TruncateToWidth("hi", 10); got != "hi" {
		t.Errorf("short = %q, want it untouched", got)
	}
}

func TestWrapBreaksLongUnbrokenStrings(t *testing.T) {
	url := "https://example.com/" + strings.Repeat("a", 200)
	lines := WrapToWidth(url, 20)
	for _, line := range lines {
		if CellWidth(line) > 20 {
			t.Errorf("line = %q (%d cells), want <= 20", line, CellWidth(line))
		}
	}
	if len(lines) < 5 {
		t.Errorf("wrapped %d lines, want the URL folded hard", len(lines))
	}
}

func TestWrapKeepsCJKWithinWidth(t *testing.T) {
	text := "日本語テストの折り返し確認です"
	for _, line := range WrapToWidth(text, 10) {
		if CellWidth(line) > 10 {
			t.Errorf("line = %q (%d cells), want <= 10", line, CellWidth(line))
		}
	}
}

func TestWrapEmptyAndTrailingNewline(t *testing.T) {
	if got := WrapToWidth("", 20); len(got) != 1 {
		t.Errorf("empty = %v, want one empty line", got)
	}
	lines := WrapToWidth("a\n", 20)
	if len(lines) != 2 || lines[1] != "" {
		t.Errorf("trailing = %q, want the final empty preserved, not dropped", lines)
	}
}
