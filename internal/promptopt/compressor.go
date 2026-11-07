package promptopt

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Conversational openings are only filler when they frame a request. Each
// pattern consumes its trailing separator so the next iteration sees the first
// meaningful word.
var (
	greetingPrefix = regexp.MustCompile(`(?i)^(?:hello|hi|hey|greetings)(?:\s+(?:there|everyone|all|team))?(?:\s*[,!.:;]\s*|\s+|$)|^(?:good\s+(?:morning|afternoon|evening))(?:\s*[,!.:;]\s*|\s+|$)`)
	requestPrefix  = regexp.MustCompile(`(?i)^(?:i\s+(?:would|'d)\s+like\s+(?:you\s+)?to|i\s+(?:want|need)\s+(?:you\s+)?to|we\s+need\s+to|can\s+you|could\s+you|would\s+you\s+mind|would\s+you|will\s+you)\b[\s,]*`)
	leadPoliteness = regexp.MustCompile(`(?i)^(?:please|kindly)\b[\s,]*`)

	// Politeness and thanks can appear anywhere; they carry no instruction.
	politenessAnywhere = regexp.MustCompile(`(?i)\b(?:please|kindly)\b[\s,]*`)
	thanksAnywhere     = regexp.MustCompile(`(?i)\b(?:thank\s+you\s+(?:in\s+advance|so\s+much)|thanks\s+(?:in\s+advance|a\s+lot)|thank\s+you|thanks)\b[.,!]?[ \t]*`)
	directiveFluff     = regexp.MustCompile(`(?i)\b(?:make\s+sure|be\s+sure)\s+(?:to|that)\b[\s,]*`)
)

// CompressPrompt removes conversational filler and markdown decoration before a
// prompt is written to scratch or sent to a model.
//
// The transform is deterministic and conservative: fenced code blocks, indented
// code, and inline code spans are copied byte-for-byte, including their internal
// whitespace and politeness words. Outside code it drops greetings, request
// framing ("can you", "I would like you to"), politeness, thanks, and "make sure
// to" filler; removes bold asterisks and horizontal rules; collapses runs of
// spaces; and reduces consecutive blank lines to one. Identifiers, paths, glob
// patterns, and list structure are never rewritten.
//
// An input made entirely of filler returns ""; callers that require a non-empty
// payload must decide their own fallback.
func CompressPrompt(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	lines := splitPromptLines(raw)
	segments := splitCodeSegments(lines)
	out := make([]outputLine, 0, len(lines))
	for _, segment := range segments {
		if segment.code {
			for _, line := range segment.lines {
				out = append(out, outputLine{text: line, code: true})
			}
			continue
		}
		out = append(out, compressPlainLines(segment.lines)...)
	}
	out = collapseBlankLines(out)
	if len(out) == 0 {
		return ""
	}
	result := make([]string, len(out))
	for i, line := range out {
		result[i] = line.text
	}
	return strings.Join(result, "\n")
}

// outputLine carries whether a line came from protected code. Blank-line
// compaction must never remove a blank line inside a fenced block.
type outputLine struct {
	text string
	code bool
}

// promptSegment is a run of original lines inside or outside a fenced block.
type promptSegment struct {
	lines []string
	code  bool
}

// splitPromptLines keeps CR bytes on code lines. Plain lines strip them during
// compression, so a CRLF input still yields consistent prose.
func splitPromptLines(raw string) []string {
	return strings.Split(raw, "\n")
}

// splitCodeSegments separates fenced blocks from prose. An unclosed fence stays
// protected to the end: guessing where code stops is worse than preserving too
// much.
func splitCodeSegments(lines []string) []promptSegment {
	segments := make([]promptSegment, 0, 4)
	current := promptSegment{}
	flush := func() {
		if len(current.lines) > 0 {
			segments = append(segments, current)
			current = promptSegment{}
		}
	}

	inCode := false
	var fenceChar byte
	fenceLen := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inCode {
			if ch, n, ok := openingFence(trimmed); ok {
				flush()
				inCode = true
				fenceChar, fenceLen = ch, n
				current = promptSegment{code: true, lines: []string{line}}
				continue
			}
			current.lines = append(current.lines, line)
			continue
		}
		current.lines = append(current.lines, line)
		if isClosingFence(trimmed, fenceChar, fenceLen) {
			flush()
			inCode = false
		}
	}
	flush()
	return segments
}

// openingFence reports a fenced block opener and its marker length.
func openingFence(trimmed string) (byte, int, bool) {
	if len(trimmed) < 3 {
		return 0, 0, false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	return ch, n, true
}

// isClosingFence reports a closer made only of the opening marker. A line such
// as ```go inside a block is code, not a close.
func isClosingFence(trimmed string, ch byte, min int) bool {
	if len(trimmed) < min {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != ch {
			return false
		}
	}
	return true
}

// compressPlainLines processes one prose segment. Indented code is preserved:
// Markdown's four-space rule is the only code block that has no explicit fence.
func compressPlainLines(lines []string) []outputLine {
	out := make([]outputLine, 0, len(lines))
	inIndentedCode := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if inIndentedCode {
				// A blank inside an indented block is code; preserve its
				// whitespace and its position.
				out = append(out, outputLine{text: line, code: true})
			} else {
				out = append(out, outputLine{})
			}
			continue
		}
		if isIndentedCode(line) {
			inIndentedCode = true
			out = append(out, outputLine{text: line, code: true})
			continue
		}
		inIndentedCode = false
		text := compressPlainLine(line)
		if strings.TrimSpace(text) == "" {
			// A line that was only filler disappears rather than leaving a
			// paragraph gap behind.
			continue
		}
		out = append(out, outputLine{text: text})
	}
	return out
}

// isIndentedCode reports a Markdown indented code line.
func isIndentedCode(line string) bool {
	return strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")
}

// compressPlainLine compresses one prose line while protecting inline code.
func compressPlainLine(line string) string {
	trimmed := strings.TrimRight(line, " \t\r")
	if trimmed == "" || isHorizontalRule(trimmed) {
		return ""
	}
	lead := leadingIndent(trimmed)
	body := trimmed[len(lead):]

	atLineStart := !strings.HasPrefix(body, "`")
	first := true
	body = transformOutsideInlineCode(body, func(chunk string) string {
		start := atLineStart && first
		first = false
		return compressPlainChunk(chunk, start)
	})
	return strings.TrimRight(lead+body, " \t\r")
}

// leadingIndent returns the indentation that carries list structure. Four-space
// or tab-indented lines never reach this function; they are code.
func leadingIndent(line string) string {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return line[:i]
}

// compressPlainChunk transforms text that is outside every code span.
func compressPlainChunk(chunk string, atLineStart bool) string {
	chunk = strings.ReplaceAll(chunk, "\r", "")
	chunk = stripBoldAsterisks(chunk)
	if atLineStart {
		chunk = trimLeadFluff(chunk)
	}
	chunk = politenessAnywhere.ReplaceAllString(chunk, "")
	chunk = thanksAnywhere.ReplaceAllString(chunk, "")
	chunk = directiveFluff.ReplaceAllString(chunk, "")
	chunk = tidyPunctuationSpacing(chunk)
	return collapseHorizontal(chunk)
}

// stripBoldAsterisks removes paired ** or *** markers around prose. Markdown
// emphasis and glob syntax are lexically identical, so the function is
// deliberately conservative: the opening marker must sit at a prose boundary,
// the content must contain no path separator or nested asterisk, and the closing
// marker must be followed by a boundary. A glob such as **/foo/** is therefore
// preserved, while **bold** becomes bold.
func stripBoldAsterisks(s string) string {
	if !strings.Contains(s, "**") {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '*' {
			out.WriteByte(s[i])
			i++
			continue
		}
		run := i
		for run < len(s) && s[run] == '*' {
			run++
		}
		width := run - i
		if width < 2 || width > 3 || !atProseBoundaryBefore(s, i) {
			out.WriteString(s[i:run])
			i = run
			continue
		}
		closeStart, closeEnd := findBoldClose(s, run, width)
		if closeStart < 0 {
			out.WriteString(s[i:run])
			i = run
			continue
		}
		out.WriteString(s[run:closeStart])
		i = closeEnd
	}
	return out.String()
}

// findBoldClose returns the marker run that may close a bold span, or -1 when
// none qualifies.
func findBoldClose(s string, start, width int) (int, int) {
	for i := start; i < len(s); {
		if s[i] != '*' {
			i++
			continue
		}
		run := i
		for run < len(s) && s[run] == '*' {
			run++
		}
		if run-i == width && atProseBoundaryAfter(s, run) && boldContent(s[start:i]) {
			return i, run
		}
		i = run
	}
	return -1, -1
}

// boldContent reports prose rather than a path or glob.
func boldContent(s string) bool {
	if s == "" || strings.ContainsAny(s, "*\n/\\") {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// atProseBoundaryBefore reports that an opening marker starts a word rather
// than continuing an identifier or path.
func atProseBoundaryBefore(s string, at int) bool {
	if at == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:at])
	return unicode.IsSpace(r) || strings.ContainsRune("([{>\"'`“”‘’", r)
}

// atProseBoundaryAfter reports that a closing marker ends a word rather than
// continuing an identifier or path.
func atProseBoundaryAfter(s string, at int) bool {
	if at >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[at:])
	return unicode.IsSpace(r) || strings.ContainsRune(")]}>\"'`“”‘’.,;:!?—–…", r)
}

// trimLeadFluff removes request framing from the start of a prose line.
func trimLeadFluff(chunk string) string {
	for i := 0; i < 8; i++ {
		switch {
		case greetingPrefix.MatchString(chunk):
			chunk = greetingPrefix.ReplaceAllString(chunk, "")
		case requestPrefix.MatchString(chunk):
			chunk = requestPrefix.ReplaceAllString(chunk, "")
		case leadPoliteness.MatchString(chunk):
			chunk = leadPoliteness.ReplaceAllString(chunk, "")
		default:
			return chunk
		}
	}
	return chunk
}

// transformOutsideInlineCode applies fn only to text between backtick spans.
// The spans themselves, including runs longer than one backtick, are copied
// verbatim.
func transformOutsideInlineCode(line string, fn func(string) string) string {
	var out strings.Builder
	out.Grow(len(line))
	for i := 0; i < len(line); {
		open := strings.IndexByte(line[i:], '`')
		if open < 0 {
			out.WriteString(fn(line[i:]))
			break
		}
		open += i
		out.WriteString(fn(line[i:open]))

		run := open
		for run < len(line) && line[run] == '`' {
			run++
		}
		close := closingBackticks(line, run, run-open)
		if close < 0 {
			// An unmatched backtick is literal text, but treating the rest as
			// code keeps accidental truncation from rewriting it.
			out.WriteString(line[open:])
			break
		}
		out.WriteString(line[open:close])
		i = close
	}
	return out.String()
}

// closingBackticks returns the index just past a backtick run of exactly width,
// or -1 when the span never closes.
func closingBackticks(line string, start, width int) int {
	for i := start; i < len(line); {
		next := strings.IndexByte(line[i:], '`')
		if next < 0 {
			return -1
		}
		next += i
		end := next
		for end < len(line) && line[end] == '`' {
			end++
		}
		if end-next == width {
			return end
		}
		i = end
	}
	return -1
}

// tidyPunctuationSpacing removes horizontal whitespace before sentence
// punctuation. A dot that begins a relative path (./ or ../) keeps its leading
// space: "go test ./..." is technical syntax, not a sentence ending.
func tidyPunctuationSpacing(s string) string {
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != ' ' && s[i] != '\t' {
			out.WriteByte(s[i])
			i++
			continue
		}
		end := i
		for end < len(s) && (s[end] == ' ' || s[end] == '\t') {
			end++
		}
		if end < len(s) && isTightPunctuation(s, end) {
			i = end
			continue
		}
		out.WriteByte(' ')
		i = end
	}
	return out.String()
}

// isTightPunctuation reports punctuation that attaches to the preceding word.
func isTightPunctuation(s string, at int) bool {
	switch s[at] {
	case ',', ';', ':', '!', '?':
		return true
	case '.':
		return at+1 >= len(s) || (s[at+1] != '/' && s[at+1] != '\\')
	}
	return false
}

// collapseHorizontal folds runs of spaces and tabs to one, preserving a single
// boundary space so text on either side of an inline code span stays separated.
func collapseHorizontal(s string) string {
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	pending := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			pending = true
			continue
		}
		if pending {
			out.WriteByte(' ')
			pending = false
		}
		out.WriteRune(r)
	}
	if pending {
		out.WriteByte(' ')
	}
	return out.String()
}

// collapseBlankLines keeps at most one blank prose line in a row and trims
// leading/trailing prose blanks. Blank lines inside protected code are kept.
func collapseBlankLines(lines []outputLine) []outputLine {
	out := make([]outputLine, 0, len(lines))
	for _, line := range lines {
		blank := !line.code && strings.TrimSpace(line.text) == ""
		if blank {
			if len(out) == 0 {
				continue
			}
			previous := out[len(out)-1]
			if !previous.code && strings.TrimSpace(previous.text) == "" {
				continue
			}
		}
		out = append(out, line)
	}
	for len(out) > 0 && !out[len(out)-1].code && strings.TrimSpace(out[len(out)-1].text) == "" {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && !out[0].code && strings.TrimSpace(out[0].text) == "" {
		out = out[1:]
	}
	return out
}

// isHorizontalRule reports a Markdown rule, which is decoration with no text.
func isHorizontalRule(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 {
		return false
	}
	ch := trimmed[0]
	if ch != '-' && ch != '*' && ch != '_' {
		return false
	}
	count := 0
	for _, r := range trimmed {
		switch r {
		case rune(ch):
			count++
		case ' ', '\t':
		default:
			return false
		}
	}
	return count >= 3
}
