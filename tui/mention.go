package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/prompt"
	"github.com/charmbracelet/lipgloss"
)

// "@" references serve two consumers: the composer highlights them so a file
// the prompt will attach reads as distinct from prose, and the turn pipeline
// parses the same tokens to attach the file's real contents. Both share the
// token rule here so what is highlighted is exactly what gets attached.

// mentionTokenAt reports whether an "@path" reference begins at rune i, and
// how many runes it spans.
//
// A reference begins at the start of the row or after whitespace, so an "@"
// inside a word (an email address) stays prose. The path must hold at least one
// path rune, which leaves a bare "@" — the trigger still open with nothing
// typed — unstyled and unparsed.
func mentionTokenAt(runes []rune, i int) (length int, ok bool) {
	if i < 0 || i >= len(runes) || runes[i] != '@' {
		return 0, false
	}
	if i > 0 && !unicode.IsSpace(runes[i-1]) {
		return 0, false
	}
	j := i + 1
	for j < len(runes) && isMentionPathRune(runes[j]) {
		j++
	}
	if j == i+1 {
		return 0, false
	}
	return j - i, true
}

// isMentionPathRune reports whether r can appear in a workspace path token.
// The set is deliberately narrow so sentence punctuation that follows a
// reference ("," ")" ".") ends the token instead of being swallowed by it.
func isMentionPathRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	switch r {
	case '/', '.', '_', '-', '~':
		return true
	}
	return false
}

// highlightMentions styles every "@path" reference in one rendered composer
// row and returns the row plus the cursor adjusted for the escape runes the
// styling added. It mirrors renderPasteChips: the cursor is a rune index, and a
// position inside a reference parks just before the styled span rather than
// splitting it. A zero style leaves the row unchanged.
func highlightMentions(style lipgloss.Style, row string, cursor int) (string, int) {
	if row == "" || !strings.ContainsRune(row, '@') {
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
		if length, ok := mentionTokenAt(runes, i); ok {
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

// parseMentions returns the workspace paths referenced with "@" in text, in
// first-seen order and without duplicates. A folder reference (a trailing
// slash) is skipped: a directory has no content to attach, so it belongs to
// completion alone.
func parseMentions(text string) []string {
	runes := []rune(text)
	seen := make(map[string]bool)
	var out []string
	for i := 0; i < len(runes); {
		length, ok := mentionTokenAt(runes, i)
		if !ok {
			i++
			continue
		}
		token := string(runes[i : i+length])
		i += length
		if strings.HasSuffix(token, "/") {
			continue
		}
		path := strings.TrimPrefix(token, "@")
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

// mentionContext resolves the "@" references in text into one system-context
// block plus a warning per reference that could not be read.
//
// read is the workspace seam: production reads through the read tool's root
// containment, and a test substitutes a map. A reference that fails to read
// produces an explicit warning instead of silently contributing nothing, so a
// typo never reaches the model as an empty attachment. A nil read leaves "@" a
// pure completion affordance and resolves nothing.
func mentionContext(text string, read func(path string) (string, error)) (string, []string) {
	if read == nil {
		return "", nil
	}
	var files []prompt.ReferencedFile
	var warnings []string
	for _, path := range parseMentions(text) {
		content, err := read(path)
		if err != nil {
			// Name the reference the way the user wrote it, so the warning
			// points at the token in their prompt rather than a bare path.
			warnings = append(warnings, fmt.Sprintf("could not read @%s: %v", path, err))
			continue
		}
		files = append(files, prompt.ReferencedFile{Path: path, Content: content})
	}
	return prompt.MentionContext(files), warnings
}

// systemContextBlocks wraps rendered file context as one system block.
//
// Empty context yields nil, which clears any context from a previous turn: a
// prompt with no "@" reference must never inherit the last turn's files.
func systemContextBlocks(text string) []ai.SystemPromptBlock {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []ai.SystemPromptBlock{{Text: text}}
}
