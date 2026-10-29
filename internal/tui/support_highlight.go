package tui

import (
	"regexp"
	"strings"
)

// Support overlay typography hierarchy.
//
// The $sup answer viewport renders through RenderSupportMarkdownWrapped,
// which is the shared markdown engine with support=true: plain runs gain
// entity highlights, trailing-colon lines gain section-header emphasis,
// and bare commands gain code pills. Main-chat rendering uses
// RenderMarkdownWrapped unchanged, so the transcript never changes.

var (
	// Dates, years, and dotted versions read as specs: amber.
	supportAltRe = regexp.MustCompile(`\b(?:19\d{2}|20\d{2})\b|\bv?\d+\.\d+(?:\.\d+)?\b`)
	// Strict proper nouns: case-sensitive so a "go" verb never highlights.
	supportStrictRe = regexp.MustCompile(`\b(?:Golang|Go|Google|GitHub|Linux|Docker|Kubernetes)\b`)
	// Generic technologies and concepts: case-insensitive.
	supportGenericRe = regexp.MustCompile(`(?i)\b(?:goroutines?|channels?|concurrency|compilation|open-source|programming language|static binary|object-oriented|compiler|concurrency|runtime|garbage collection|type safety|standard library)\b`)
	// Bare commands and flags read as pills even without backticks.
	supportPillRe = regexp.MustCompile(`--[A-Za-z][A-Za-z0-9-]*|\bgo (?:run|build|test|mod|get|install|fmt|vet)\b|\bgofmt\b|\$\s*[A-Za-z0-9_./-]+`)
)

// highlightSupportChunks splits plain chunks by entity patterns. Styled
// chunks (code, bold, links) pass through untouched so markdown meaning
// wins over heuristics.
func highlightSupportChunks(chunks []chunk, pal *Palette) []chunk {
	var out []chunk
	for _, c := range chunks {
		if c.style != nil {
			out = append(out, c)
			continue
		}
		out = append(out, splitSupportText(c.text, pal)...)
	}
	if len(out) == 0 {
		return chunks
	}
	return out
}

// splitSupportText scans one plain run left to right, emitting the
// earliest match at each position. Pill, amber spec, and cyan entity take
// precedence in that order; anything unmatched stays plain.
func splitSupportText(text string, pal *Palette) []chunk {
	var out []chunk
	rest := text
	for len(rest) > 0 {
		pillLoc := supportPillRe.FindStringIndex(rest)
		altLoc := supportAltRe.FindStringIndex(rest)
		strictLoc := supportStrictRe.FindStringIndex(rest)
		genLoc := supportGenericRe.FindStringIndex(rest)

		bestKind := ""
		bestLoc := []int(nil)
		consider := func(kind string, loc []int) {
			if loc == nil {
				return
			}
			if bestLoc == nil || loc[0] < bestLoc[0] ||
				(loc[0] == bestLoc[0] && loc[1]-loc[0] > bestLoc[1]-bestLoc[0]) {
				bestKind, bestLoc = kind, loc
			}
		}
		// Pills win ties so a flag inside a longer entity keeps its surface.
		consider("pill", pillLoc)
		consider("alt", altLoc)
		consider("strict", strictLoc)
		consider("generic", genLoc)

		if bestLoc == nil {
			out = append(out, chunk{text: rest})
			break
		}
		if bestLoc[0] > 0 {
			out = append(out, chunk{text: rest[:bestLoc[0]]})
		}
		match := rest[bestLoc[0]:bestLoc[1]]
		switch bestKind {
		case "pill":
			out = append(out, styledChunk(match, pal.Code))
		case "alt":
			out = append(out, styledChunk(match, pal.SupportHiAlt))
		default:
			out = append(out, styledChunk(match, pal.SupportHi))
		}
		rest = rest[bestLoc[1]:]
	}
	return out
}

// isSupportSectionHeader reports a bare trailing-colon label such as
// "Origins and Design:". Markdown headings and bold labels already carry
// emphasis; this covers the plain "Label:" shape models emit in prose.
func isSupportSectionHeader(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasSuffix(trimmed, ":") {
		return false
	}
	body := strings.TrimSpace(strings.TrimSuffix(trimmed, ":"))
	if len([]rune(body)) < 3 || len([]rune(body)) > 48 {
		return false
	}
	if strings.Contains(body, "\n") {
		return false
	}
	first := []rune(body)[0]
	if first < 'A' || first > 'Z' {
		return false
	}
	// A full sentence is not a header, even with a trailing colon.
	if strings.Count(body, " ") > 6 {
		return false
	}
	return true
}

// boldPlainChunks gives a section header its high-contrast weight while
// keeping entity colors: plain runs take Bold, highlighted runs keep
// their own style (already bold cyan/amber).
func boldPlainChunks(chunks []chunk, pal *Palette) []chunk {
	out := make([]chunk, 0, len(chunks))
	for _, c := range chunks {
		if c.style == nil {
			out = append(out, styledChunk(c.text, pal.Bold))
			continue
		}
		out = append(out, c)
	}
	return out
}

// RenderSupportMarkdownWrapped renders $sup answer text with the overlay
// hierarchy. Sanitization matches RenderMarkdownWrapped exactly: ANSI is
// stripped at the boundary, CRLF folds to LF, and rendering stays pure.
func RenderSupportMarkdownWrapped(text string, pal *Palette, width int) string {
	if pal == nil {
		pal = DarkPalette()
	}
	text = stripANSI(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	r := &blockRenderer{pal: pal, width: width, support: true}
	return r.render(text)
}
