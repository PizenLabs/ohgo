package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// CellWidth reports the terminal cells a string occupies.
//
// ANSI escape sequences contribute zero width; CJK, emoji, and other wide
// glyphs contribute two. Tabs are expanded at the renderer's fixed tab stops
// before measurement: a terminal paints them, so counting them as zero would
// understate the row and cause a soft wrap the layout never budgeted.
func CellWidth(s string) int {
	return runewidth.StringWidth(expandTabsStyled(ansi.Strip(s)))
}

// expandTabsStyled expands tabs without disturbing ANSI escape sequences.
//
// A tab stop is a screen column, and an escape sequence occupies none. The
// scanner walks the original string, copies escape sequences whole, and only
// advances the column for printable runes.
func expandTabsStyled(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	runes := []rune(s)
	var sb strings.Builder
	sb.Grow(len(s))
	col := 0
	for i := 0; i < len(runes); {
		if advance, keep := scanEscape(runes, i); advance > 0 {
			if keep {
				for _, r := range runes[i : i+advance] {
					sb.WriteRune(r)
				}
			}
			i += advance
			continue
		}
		r := runes[i]
		if r == '\t' {
			n := CodeTabWidth - col%CodeTabWidth
			sb.WriteString(strings.Repeat(" ", n))
			col += n
			i++
			continue
		}
		sb.WriteRune(r)
		if w := runewidth.RuneWidth(r); w > 0 {
			col += w
		}
		i++
	}
	return sb.String()
}

// CodeTabWidth is the tab stop the renderer commits to when it lays out a code
// block.
//
// It is fixed rather than inherited because measurement must agree with the
// terminal: the styling pass expands tabs to spaces on its way out, so a tab
// left in a line is counted as a zero-width cell by CellWidth and then painted
// four cells wide. Committing to one width here is what lets a padded row
// still fit its budget.
const CodeTabWidth = 4

// expandTabs replaces each tab with spaces up to the next CodeTabWidth stop.
//
// Callers must run this before measuring anything they will later style or
// pad. A tab is not zero-width on screen, and leaving one in place makes the
// measured width and the rendered width disagree by up to CodeTabWidth cells
// per tab — enough for a padded code row to overflow its budget, which the
// terminal then soft-wraps, turning one logical row into two.
func expandTabs(s string) string {
	return expandTabsStyled(s)
}

// TruncateToWidth cuts s to at most width cells, appending ellipsis when cut.
//
// A non-positive width yields the empty string. The ellipsis counts against
// the budget so the result never overflows the column it was measured for.
func TruncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = expandTabsStyled(s)
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
		out = append(out, wrapLine(expandTabsStyled(line), width)...)
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
		if runewidth.StringWidth(plain) <= width {
			return []string{line}
		}
		// A long run of spaces or expanded tabs still occupies cells the
		// terminal will lay out; hiding it behind a one-row shortcut would
		// desynchronise the row count from the screen.
		return sliceCells(plain, width)
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
