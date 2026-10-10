package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// CellWidth reports the terminal cells a string occupies.
//
// ANSI escape sequences contribute zero width; CJK, emoji, and other wide
// glyphs contribute two. Byte length is never consulted: a CJK character is
// three bytes but one or two cells, and measuring bytes overflows narrow
// terminals and underflows wide ones.
func CellWidth(s string) int {
	return runewidth.StringWidth(ansi.Strip(s))
}

// TruncateToWidth cuts s to at most width cells, appending ellipsis when cut.
//
// A non-positive width yields the empty string. The ellipsis counts against
// the budget so the result never overflows the column it was measured for.
func TruncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	stripped := ansi.Strip(s)
	if runewidth.StringWidth(stripped) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return runewidth.Truncate(stripped, width-1, "…")
}

// WrapToWidth folds text to the available cells.
//
// Lines break at spaces when one falls within the width; long unbroken runs
// (URLs, hashes, CJK without spaces) break hard at the boundary rather than
// overflowing. Empty input yields no lines; a trailing newline does not add
// one. ANSI sequences are stripped before measuring because styling is applied
// after wrapping, never before.
func WrapToWidth(text string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	if text == "" {
		return []string{""}
	}
	raw := strings.Split(text, "\n")
	var out []string
	for _, line := range raw {
		out = append(out, wrapLine(line, width)...)
	}
	return out
}

// wrapLine folds one logical line into display lines.
func wrapLine(line string, width int) []string {
	plain := ansi.Strip(line)
	if runewidth.StringWidth(plain) <= width {
		return []string{line}
	}
	if strings.TrimSpace(plain) == "" {
		return []string{line}
	}
	words := strings.Fields(plain)
	// No spaces to break at: hard-slice by cells.
	if len(words) <= 1 {
		return sliceCells(plain, width)
	}
	var out []string
	cur := ""
	curW := 0
	flush := func() {
		if cur != "" {
			out = append(out, cur)
			cur, curW = "", 0
		}
	}
	for _, w := range words {
		ww := runewidth.StringWidth(w)
		if ww > width {
			flush()
			out = append(out, sliceCells(w, width)...)
			continue
		}
		if cur == "" {
			cur, curW = w, ww
			continue
		}
		if curW+1+ww <= width {
			cur += " " + w
			curW += 1 + ww
			continue
		}
		flush()
		cur, curW = w, ww
	}
	flush()
	if len(out) == 0 {
		return []string{line}
	}
	return out
}

// sliceCells hard-slices s into width-cell chunks.
func sliceCells(s string, width int) []string {
	var out []string
	runes := []rune(s)
	start := 0
	for start < len(runes) {
		w := 0
		end := start
		for end < len(runes) {
			rw := runewidth.RuneWidth(runes[end])
			if rw == 0 {
				end++
				continue
			}
			if w+rw > width {
				break
			}
			w += rw
			end++
		}
		if end == start {
			end = start + 1
		}
		out = append(out, string(runes[start:end]))
		start = end
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}
