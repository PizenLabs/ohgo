package tui

import (
	"strings"
)

// RenderMarkdown renders assistant text for the transcript.
//
// The original renders full markdown (tables, LaTeX, Mermaid, syntax themes).
// This ports the visible subset of chat prose — fences, inline code, headers,
// emphasis, lists, quotes, rules — without a dependency: a chat transcript
// needs readable structure, not a document engine. Anything unrecognised
// passes through untouched, so exotic markup degrades to plain text rather
// than disappearing.
func RenderMarkdown(text string, pal *Palette) string {
	if pal == nil {
		pal = DarkPalette()
	}
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	inFence := false
	fenceLang := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				inFence = true
				fenceLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				sb.WriteString(pal.CodeFence.Render("```"+fenceLang) + "\n")
			} else {
				inFence = false
				fenceLang = ""
				sb.WriteString(pal.CodeFence.Render("```") + "\n")
			}
			continue
		}
		if inFence {
			sb.WriteString(pal.Code.Render(line) + "\n")
			continue
		}
		sb.WriteString(renderProseLine(line, pal) + "\n")
		_ = i
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderProseLine renders one non-fence line.
func renderProseLine(line string, pal *Palette) string {
	trimmed := strings.TrimSpace(line)
	switch {
	case trimmed == "":
		return ""
	case trimmed == "---" || trimmed == "***" || trimmed == "___":
		return pal.Muted.Render("─ ─ ─")
	case strings.HasPrefix(trimmed, "#"):
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level <= 6 && strings.HasPrefix(trimmed[level:], " ") {
			return pal.Heading.Render(strings.TrimSpace(trimmed[level:]))
		}
	case strings.HasPrefix(trimmed, ">"):
		return pal.Quote.Render("│ " + renderInline(strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), pal))
	case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
		return pal.Bullet.Render("•") + " " + renderInline(strings.TrimSpace(trimmed[2:]), pal)
	case isOrderedItem(trimmed):
		dot := strings.Index(trimmed, ".")
		return pal.Bullet.Render(trimmed[:dot+1]) + " " + renderInline(strings.TrimSpace(trimmed[dot+1:]), pal)
	}
	// Preserve leading indentation for nested content, styling the rest.
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	return indent + renderInline(strings.TrimSpace(line), pal)
}

// isOrderedItem reports "1. " style list markers.
func isOrderedItem(trimmed string) bool {
	dot := strings.Index(trimmed, ".")
	if dot <= 0 || !strings.HasPrefix(trimmed[dot:], ". ") {
		return false
	}
	for _, r := range trimmed[:dot] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// renderInline styles code spans, bold, and italics.
//
// Scanning is single-pass and best-effort: an unclosed marker renders
// literally, so model output with stray asterisks survives intact.
func renderInline(text string, pal *Palette) string {
	var sb strings.Builder
	i := 0
	for i < len(text) {
		switch {
		case text[i] == '`':
			end := strings.Index(text[i+1:], "`")
			if end < 0 {
				sb.WriteByte(text[i])
				i++
				continue
			}
			sb.WriteString(pal.Code.Render(text[i+1 : i+1+end]))
			i += end + 2
		case strings.HasPrefix(text[i:], "**"):
			end := strings.Index(text[i+2:], "**")
			if end < 0 {
				sb.WriteString(text[i:])
				i = len(text)
				continue
			}
			sb.WriteString(pal.Bold.Render(text[i+2 : i+2+end]))
			i += end + 4
		case text[i] == '*' || text[i] == '_':
			marker := text[i : i+1]
			end := strings.Index(text[i+1:], marker)
			if end < 0 {
				sb.WriteByte(text[i])
				i++
				continue
			}
			sb.WriteString(pal.Italic.Render(text[i+1 : i+1+end]))
			i += end + 2
		default:
			sb.WriteByte(text[i])
			i++
		}
	}
	return sb.String()
}
