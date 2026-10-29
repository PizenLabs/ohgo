package tui

import (
	"strings"
)

// Terminal-output sanitization. Model output, tool results, and pasted input
// are untrusted bytes: progress bars, colored diffs, and test runners embed
// carriage returns and cursor-movement sequences that, printed raw, rewrite
// earlier rows — the classic "upside-down and duplicated" TTY corruption
// that no string line-count test can see. Every render path for untrusted
// text passes through one of the two boundaries below, so measurement and
// wrapping always operate on what the terminal will actually lay out.
//
// The policy is deliberately narrow: SGR styling (colors, bold) survives,
// everything that can move the cursor or reinterpret layout does not.
//
// sanitizeBlock prepares multi-line content (user prompts, notices, tool
// output, composer input): carriage returns become newlines so progress
// overwrites read as history, dangerous sequences are stripped, and stray
// control bytes become visible placeholders. Invalid UTF-8 (e.g. from a
// byte-cut truncation upstream) is made valid so no half-rune escapes.
func sanitizeBlock(s string) string {
	s = strings.ToValidUTF8(s, "�")
	runes := []rune(s)
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(runes); {
		// One pass: surviving SGR styling is emitted whole so the control
		// mapping below never sees its introducer as a stray byte.
		if advance, keep := scanEscape(runes, i); advance > 0 {
			if keep {
				for _, r := range runes[i : i+advance] {
					sb.WriteRune(r)
				}
			}
			i += advance
			continue
		}
		switch r := runes[i]; {
		case r == '\r':
			sb.WriteByte('\n')
		case r == '\n' || r == '\t':
			sb.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			sb.WriteRune('�')
		default:
			sb.WriteRune(r)
		}
		i++
	}
	return sb.String()
}

// sanitizeInline prepares single-line chrome (status fields, header fields,
// picker queries, suggestion state): block rules first, then newlines fold
// to spaces so the line can never split the row budget it was counted for.
func sanitizeInline(s string) string {
	return strings.ReplaceAll(sanitizeBlock(s), "\n", " ")
}

// sanitizeWithCursor sanitizes composer input for display while keeping the
// rune cursor meaningful: single-rune mappings (\r→\n, controls→�) preserve
// indices exactly, and each stripped multi-rune sequence before the cursor
// shifts it back by its length. The stored input is untouched — editing
// still operates on raw runes.
func sanitizeWithCursor(s string, cursor int) (string, int) {
	s = strings.ToValidUTF8(s, "�")
	runes := []rune(s)
	cursor = clampRune(cursor, len(runes))
	var sb strings.Builder
	sb.Grow(len(s))
	// The cursor counts surviving output runes strictly before it, so a
	// cursor inside stripped text parks at the removal point and every
	// other position maps exactly.
	outCursor := 0
	for i := 0; i < len(runes); {
		if advance, keep := scanEscape(runes, i); advance > 0 {
			if keep {
				for k := i; k < i+advance; k++ {
					sb.WriteRune(runes[k])
					if k < cursor {
						outCursor++
					}
				}
			}
			i += advance
			if i > len(runes) {
				i = len(runes)
			}
			continue
		}
		if i < cursor {
			outCursor++
		}
		r := runes[i]
		switch {
		case r == '\r':
			sb.WriteByte('\n')
		case r == '\n' || r == '\t':
			sb.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			sb.WriteRune('�')
		default:
			sb.WriteRune(r)
		}
		i++
	}
	return sb.String(), outCursor
}

// scanEscape inspects a sequence starting at runes[i]. It returns the rune
// length of the sequence and whether to keep it: SGR styling (final byte
// 'm') is styling, not movement, and passes through whole; cursor movement,
// OSC hyperlinks, charset shifts, and lone or unterminated introducers are
// stripped. Advance 0 with keep=false means an ordinary rune.
func scanEscape(runes []rune, i int) (advance int, keep bool) {
	if runes[i] != 0x1b {
		return 0, false
	}
	if i+1 >= len(runes) {
		return 1, false // lone ESC at the end
	}
	switch runes[i+1] {
	case '[':
		j := i + 2
		// Parameter and intermediate bytes, then the final byte.
		for j < len(runes) && runes[j] >= 0x20 && runes[j] <= 0x3f {
			j++
		}
		if j >= len(runes) {
			return len(runes) - i, false // unterminated: strip to end
		}
		if runes[j] == 'm' {
			return j - i + 1, true
		}
		return j - i + 1, false
	case ']':
		// OSC: terminated by BEL or ESC \. Unterminated strips to end.
		j := i + 2
		for j < len(runes) {
			if runes[j] == 0x07 {
				return j - i + 1, false
			}
			if runes[j] == 0x1b && j+1 < len(runes) && runes[j+1] == '\\' {
				return j - i + 2, false
			}
			j++
		}
		return len(runes) - i, false
	default:
		// ESC followed by anything else (charset shifts, single-byte
		// controls): drop the pair when present, the lone ESC otherwise.
		if i+1 < len(runes) {
			return 2, false
		}
		return 1, false
	}
}
