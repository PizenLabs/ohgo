// Package retrieval implements Okapi BM25 source retrieval.
//
// It answers "which source is most relevant to this question" before the
// planner spends work on parsing, graph analysis, or semantic resolution. It
// is a lexical ranking layer, not a structural one: it does not discover
// declarations, resolve references, or claim containment. Those facts come
// from intelligence/ast and intelligence/semantic, and this package never
// substitutes a ranking score for their evidence.
//
// The index stores term frequencies only. Source text is read to build or
// refresh one document and then dropped, so retrieval cannot become a second
// copy of the repository.
package retrieval

import (
	"path"
	"strings"
	"unicode"
)

// Token bounds. They are runtime constants, never model-controlled.
const (
	// MaxTokenLength bounds one emitted token. Longer identifiers are not
	// useful retrieval terms and would blow up the vocabulary.
	MaxTokenLength = 64
	// maxChainSegments bounds a qualified-name chain such as a.b.c.d.
	maxChainSegments = 4
	// maxScannedTokens bounds work per document. A pathological file cannot
	// make tokenization unbounded; the cut is reported by the caller.
	maxScannedTokens = 20000
)

// normalize lowercases a token for case-insensitive matching.
func normalize(token string) string { return strings.ToLower(token) }

// PathTokens returns normalized tokens for a repository-relative path: every
// directory segment, the file stem, and the split identifier parts.
//
// The path field exists because paths are strong, cheap relevance evidence: a
// query for "checkout" should find checkout/service.go without the file
// containing the word in a comment.
func PathTokens(rel string) []string {
	rel = path.Clean(strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "./"))
	if rel == "." || rel == "" {
		return nil
	}
	seen := make(map[string]struct{}, 8)
	out := make([]string, 0, 8)
	emit := func(token string) {
		token = normalize(token)
		if token == "" || len(token) > MaxTokenLength || !hasLetter(token) {
			return
		}
		if _, ok := seen[token]; ok {
			return
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	for _, segment := range strings.Split(rel, "/") {
		stem := strings.TrimSuffix(segment, path.Ext(segment))
		emit(stem)
		for _, part := range SplitIdentifier(stem) {
			emit(part)
		}
	}
	return out
}

// SourceTokens returns the identifier and comment tokens of source text.
//
// Identifiers are scanned with naive comment and string awareness: content
// inside string and character literals is skipped, so a log message does not
// outrank the code that emits it, while line and block comments contribute
// English words. The scanner is lexical on purpose; structural parsing belongs
// to the AST adapters and is not duplicated here.
func SourceTokens(content []byte) (identifiers, comments []string) {
	text := string(content)
	identifiers = make([]string, 0, 256)
	comments = make([]string, 0, 64)
	tokens := 0
	bounded := func() bool { return tokens < maxScannedTokens }

	addIdentifier := func(token string) {
		if !bounded() {
			return
		}
		token = normalize(token)
		if token == "" || len(token) > MaxTokenLength || !hasLetter(token) {
			return
		}
		identifiers = append(identifiers, token)
		tokens++
	}
	addComment := func(word string) {
		if !bounded() {
			return
		}
		word = normalize(word)
		if len(word) < 2 || len(word) > 40 || !hasLetter(word) {
			return
		}
		comments = append(comments, word)
		tokens++
	}

	lineStart := true
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				end = len(text) - i
			}
			collectCommentWords(text[i+2:i+end], addComment)
			i += end
			lineStart = false
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				collectCommentWords(text[i+2:], addComment)
				return identifiers, comments
			}
			collectCommentWords(text[i+2:i+2+end], addComment)
			i += 2 + end + 2
			lineStart = false
		case c == '#' && lineStart:
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				end = len(text) - i
			}
			// Directives such as #include are code, not prose; the word itself
			// is still a legitimate low-weight term.
			collectCommentWords(text[i+1:i+end], addComment)
			i += end
			lineStart = false
		case c == '"' || c == '\'' || c == '`':
			i = skipLiteral(text, i, c)
			lineStart = false
		case c == '\n':
			lineStart = true
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		default:
			lineStart = false
			if isIdentifierStart(text[i]) {
				i = scanQualified(text, i, addIdentifier)
				continue
			}
			i++
		}
	}
	return identifiers, comments
}

// SplitIdentifier splits one identifier into its lowercase parts.
//
// The full normalized identifier is not included: callers emit that
// separately so an exact compound term and its parts can carry different
// weights. Splits cover snake_case, kebab-case, camelCase, PascalCase,
// acronym-to-word boundaries (HTTPServer -> http, server), and dotted or
// qualified chains (payment.Process -> payment, process).
func SplitIdentifier(identifier string) []string {
	var parts []string
	var current []rune
	flush := func() {
		if len(current) == 0 {
			return
		}
		value := normalize(string(current))
		if len(value) <= MaxTokenLength && hasLetter(value) {
			parts = append(parts, value)
		}
		current = current[:0]
	}
	runes := []rune(identifier)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '$' || r == ':' || r == '.':
			flush()
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if len(current) > 0 && unicode.IsUpper(r) {
				prev := current[len(current)-1]
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					flush()
				} else if unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					flush()
				}
			}
			current = append(current, r)
		default:
			flush()
		}
	}
	flush()
	return parts
}

// scanQualified consumes one identifier, emits its full form and parts, and
// continues across dotted segments so a.b.c is also a term.
func scanQualified(text string, start int, emit func(string)) int {
	segments := make([]string, 0, 2)
	i := start
	for len(segments) < maxChainSegments {
		begin := i
		for i < len(text) && isIdentifierPart(text[i]) {
			i++
		}
		segments = append(segments, text[begin:i])
		// Continue only for a dotted identifier chain; "a.b" in prose is a
		// sentence, not a qualified name, but comments never reach here.
		if i+1 >= len(text) || text[i] != '.' || !isIdentifierStart(text[i+1]) {
			break
		}
		i++
	}
	for _, segment := range segments {
		whole := normalize(segment)
		emit(whole)
		for _, part := range SplitIdentifier(segment) {
			if part != whole {
				emit(part)
			}
		}
	}
	if len(segments) > 1 {
		emit(strings.Join(segments, "."))
	}
	return i
}

// skipLiteral advances past a quoted literal starting at start.
func skipLiteral(text string, start int, quote byte) int {
	i := start + 1
	for i < len(text) {
		switch text[i] {
		case '\\':
			i += 2
			continue
		case quote:
			return i + 1
		case '\n':
			// An unterminated literal must not swallow the rest of the file:
			// treat the newline as its end and resume normal scanning.
			return i + 1
		}
		i++
	}
	return i
}

// collectCommentWords extracts prose words from one comment span.
func collectCommentWords(span string, add func(string)) {
	start := -1
	for i, r := range span {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			add(span[start:i])
			start = -1
		}
	}
	if start >= 0 {
		add(span[start:])
	}
}

func isIdentifierStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentifierPart(c byte) bool {
	return isIdentifierStart(c) || (c >= '0' && c <= '9')
}

func hasLetter(token string) bool {
	for _, r := range token {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
