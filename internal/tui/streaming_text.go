package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Streaming text correction.
//
// Model deltas routinely contain three kinds of noise that survive markdown
// parsing and make the transcript look broken while it is still arriving:
// common lexical typos, mojibake/zero-width artifacts, and space-before-
// punctuation produced by token boundaries. This pass repairs only that
// mechanical layer. It is deliberately lexical: it never rewrites meaning,
// never touches fenced or inline code, and never changes paths, URLs, or
// identifiers. Rendering remains a pure function of the accumulated text, so
// the corrected projection converges once the full word has arrived.
//
// The pass is applied at the markdown boundary, before any token is styled or
// measured. The stored conversation is untouched.

// streamingCanonicalTerms fixes the casing of product and language names that
// are routinely typed in lowercase. These are canonical spellings, not
// synonyms, so they do not change what the sentence claims.
var streamingCanonicalTerms = map[string]string{
	"github":     "GitHub",
	"gitlab":     "GitLab",
	"javascript": "JavaScript",
	"typescript": "TypeScript",
	"nodejs":     "Node.js",
	"postgres":   "Postgres",
	"postgresql": "PostgreSQL",
	"golang":     "Golang",
	"sqlite":     "SQLite",
	"graphql":    "GraphQL",
	"openai":     "OpenAI",
	"oauth":      "OAuth",
	"json":       "JSON",
	"yaml":       "YAML",
	"toml":       "TOML",
	"http":       "HTTP",
	"https":      "HTTPS",
	"url":        "URL",
	"urls":       "URLs",
	"api":        "API",
	"apis":       "APIs",
	"cli":        "CLI",
	"tui":        "TUI",
	"llm":        "LLM",
	"llms":       "LLMs",
}

// streamingTypoFixes is intentionally small and conservative: inflections are
// listed explicitly so a substitution never produces a half-corrected word.
var streamingTypoFixes = map[string]string{
	"teh":           "the",
	"adn":           "and",
	"taht":          "that",
	"thsi":          "this",
	"waht":          "what",
	"wich":          "which",
	"becuase":       "because",
	"beacuse":       "because",
	"recieve":       "receive",
	"recieved":      "received",
	"recieves":      "receives",
	"retreive":      "retrieve",
	"retreived":     "retrieved",
	"seperate":      "separate",
	"seperated":     "separated",
	"seperator":     "separator",
	"occured":       "occurred",
	"occurence":     "occurrence",
	"definately":    "definitely",
	"neccessary":    "necessary",
	"necesary":      "necessary",
	"acheive":       "achieve",
	"acheived":      "achieved",
	"beleive":       "believe",
	"existance":     "existence",
	"enviroment":    "environment",
	"enviroments":   "environments",
	"compatability": "compatibility",
	"dependancy":    "dependency",
	"dependancies":  "dependencies",
	"sucess":        "success",
	"sucessful":     "successful",
	"succesful":     "successful",
	"paramter":      "parameter",
	"paramters":     "parameters",
	"arguement":     "argument",
	"arguements":    "arguments",
	"refered":       "referred",
	"lenght":        "length",
	"widht":         "width",
	"heigth":        "height",
	"funtion":       "function",
	"fucntion":      "function",
	"retun":         "return",
	"retruns":       "returns",
	"implemenation": "implementation",
	"implemetation": "implementation",
	"implmentation": "implementation",
	"asyncronous":   "asynchronous",
	"synchronus":    "synchronous",
	"respository":   "repository",
	"repositry":     "repository",
	"directoy":      "directory",
	"direcotry":     "directory",
	"commiting":     "committing",
	"commited":      "committed",
	"intialize":     "initialize",
	"initialze":     "initialize",
	"intialized":    "initialized",
	"availabe":      "available",
	"availble":      "available",
	"configuartion": "configuration",
	"vulnerabilty":  "vulnerability",
	"auhtor":        "author",
	"writting":      "writing",
	"runing":        "running",
}

// streamingArtifactReplacer repairs bytes that arrived as the wrong character
// (usually a UTF-8 sequence decoded twice) and removes invisible characters
// that would otherwise desynchronise cell widths from glyphs.
var streamingArtifactReplacer = strings.NewReplacer(
	"\u00a0", " ", // no-break space
	"\u200b", "", // zero-width space
	"\u200c", "", // zero-width non-joiner
	"\ufeff", "", // byte-order mark
	// The zero-width joiner is deliberately preserved: it is required by
	// emoji sequences such as 👨‍👩‍👧 and removing it splits one glyph into three.
	"\u00e2\u20ac\u2122", "'", // â€™
	"\u00e2\u20ac\u02dc", "'", // â€˜
	"\u00e2\u20ac\u0153", "\"", // â€œ
	"\u00e2\u20ac\u009d", "\"", // â€<U+009D>
	"\u00e2\u20ac\u201c", "–", // â€“
	"\u00e2\u20ac\u201d", "—", // â€”
	"\u00e2\u20ac\u00a6", "…", // â€¦
	"\u00c2 ", " ", // Â followed by space
)

// streamingSpaceArtifacts removes the space tokenizers often leave before
// punctuation. Code spans are handled before this pass, so operators such as
// "a . b" inside a fence are never touched.
var streamingSpaceArtifacts = strings.NewReplacer(
	" ,", ",",
	" .", ".",
	" ;", ";",
	" :", ":",
	" !", "!",
	" ?", "?",
)

// correctStreamingText applies the lexical repair pass to markdown-ready text.
func correctStreamingText(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	inFence := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			// Fence markers and everything between them are code: spelling
			// in a snippet is the author's, not a typo to repair.
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lines[i] = correctProseLine(line)
	}
	return strings.Join(lines, "\n")
}

// correctProseLine repairs one non-fence line, leaving inline code spans
// byte-for-byte intact. Odd backtick-separated segments are code; an
// unterminated span protects the rest of the line, which is the correct
// provisional behaviour while a delta is still arriving.
func correctProseLine(line string) string {
	parts := strings.Split(line, "`")
	for i := range parts {
		if i%2 == 1 {
			continue
		}
		parts[i] = correctProseSegment(parts[i])
	}
	return strings.Join(parts, "`")
}

// correctProseSegment repairs one run of prose outside inline code.
func correctProseSegment(seg string) string {
	if seg == "" {
		return seg
	}
	seg = streamingArtifactReplacer.Replace(seg)
	seg = streamingSpaceArtifacts.Replace(seg)
	runes := []rune(seg)
	var b strings.Builder
	b.Grow(len(seg))
	for i := 0; i < len(runes); {
		if !unicode.IsLetter(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		start := i
		for i < len(runes) && unicode.IsLetter(runes[i]) {
			i++
		}
		word := string(runes[start:i])
		if streamingCorrectableWord(runes, start, i) {
			word = correctStreamingWord(word)
		}
		b.WriteString(word)
	}
	return b.String()
}

// streamingCorrectableWord rejects words that are part of a path, URL,
// identifier, or dotted name. Those are exact strings; a spelling pass must
// never rewrite them.
func streamingCorrectableWord(runes []rune, start, end int) bool {
	prev := rune(0)
	next := rune(0)
	if start > 0 {
		prev = runes[start-1]
	}
	if end < len(runes) {
		next = runes[end]
	}
	if strings.ContainsRune(`/\@_`, prev) || strings.ContainsRune(`/\@_`, next) {
		return false
	}
	// A whitespace-delimited token that carries URL syntax is one exact
	// address, including query values and fragments. Its words are not prose.
	tokenStart := start
	for tokenStart > 0 && !unicode.IsSpace(runes[tokenStart-1]) {
		tokenStart--
	}
	tokenEnd := end
	for tokenEnd < len(runes) && !unicode.IsSpace(runes[tokenEnd]) {
		tokenEnd++
	}
	token := string(runes[tokenStart:tokenEnd])
	if strings.Contains(token, "://") || strings.HasPrefix(token, "www.") || strings.Contains(token, "@") {
		return false
	}
	if isDottedName(runes, start, end) {
		return false
	}
	// "://" is a URL scheme, wherever it appears (including inside a markdown
	// link target). Schemes are exact protocol names, not prose to repair.
	if next == ':' && end+1 < len(runes) && runes[end+1] == '/' {
		return false
	}
	if next == ':' && (prev == 0 || unicode.IsSpace(prev)) {
		return false
	}
	return true
}

// isDottedName reports that the word is joined to a dot on either side and the
// dot continues into another identifier character ("README.md", "node.api").
func isDottedName(runes []rune, start, end int) bool {
	if end+1 < len(runes) && runes[end] == '.' &&
		(unicode.IsLetter(runes[end+1]) || unicode.IsDigit(runes[end+1])) {
		return true
	}
	if start >= 2 && runes[start-1] == '.' {
		before := runes[start-2]
		if unicode.IsLetter(before) || unicode.IsDigit(before) || before == '_' {
			return true
		}
	}
	return false
}

// correctStreamingWord maps one complete word through the canonical and typo
// tables while preserving the source's casing.
func correctStreamingWord(word string) string {
	lower := strings.ToLower(word)
	if canonical, ok := streamingCanonicalTerms[lower]; ok {
		if word == strings.ToUpper(word) && word != strings.ToLower(word) {
			return strings.ToUpper(canonical)
		}
		return canonical
	}
	fix, ok := streamingTypoFixes[lower]
	if !ok {
		return word
	}
	return preserveCase(word, fix)
}

// preserveCase applies fix with the leading case of src. All-uppercase input
// stays uppercase; everything else keeps sentence case.
func preserveCase(src, fix string) string {
	if src == "" || fix == "" {
		return fix
	}
	if src == strings.ToUpper(src) && src != strings.ToLower(src) {
		return strings.ToUpper(fix)
	}
	r, _ := utf8.DecodeRuneInString(src)
	if unicode.IsUpper(r) {
		first, size := utf8.DecodeRuneInString(fix)
		return string(unicode.ToUpper(first)) + fix[size:]
	}
	return fix
}
