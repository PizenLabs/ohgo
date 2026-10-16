package diff

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	linediff "github.com/PizenLabs/ohgo/diff"
)

// DefaultContext is the unified context git includes around each change in the
// inspector. Three is git's own default and the smallest that still orients a
// reader; the inter-hunk gap markers report what lies beyond it.
const DefaultContext = 3

// DiffLineType is the role of one row inside a hunk.
type DiffLineType int

const (
	// LineContext is " ": present on both sides.
	LineContext DiffLineType = iota
	// LineAddition is "+": present only on the target side.
	LineAddition
	// LineDeletion is "-": present only on the base side.
	LineDeletion
	// LineMeta is "\ No newline at end of file": it annotates the row above
	// and carries no line on either side.
	LineMeta
)

// HighlightSpan is a byte range within a DiffLine's Content that the intra-line
// diff marks as changed. Start is inclusive, End exclusive, and both align to
// rune boundaries so the renderer can slice the string safely. The renderer
// styles spans only for the rows inside the viewport.
type HighlightSpan struct {
	Start int
	End   int
}

// DiffLine is one rendered row of a hunk.
type DiffLine struct {
	// Type is the row's role.
	Type DiffLineType
	// OldLineNum is the 1-based line in the base text, or 0 when the row does
	// not exist there (an addition).
	OldLineNum int
	// NewLineNum is the 1-based line in the target text, or 0 for a deletion.
	NewLineNum int
	// Content is the line text with its diff prefix stripped, control
	// characters removed, and tabs left in place; the renderer expands them.
	// Sanitizing here, before the intra-line diff, is what keeps the highlight
	// offsets valid against the string the renderer actually slices.
	Content string
	// Highlights are the intra-line changed spans, populated only for the
	// paired side of a modified line.
	Highlights []HighlightSpan
}

// DiffChunk is one contiguous hunk with its section header.
type DiffChunk struct {
	// Header is the full "@@ -a,b +c,d @@ <section>" line, section included so
	// the enclosing function stays visible.
	Header string
	// OldStart and NewStart are the first base/target line numbers the hunk
	// covers, parsed from the header. The viewport uses them to size the gap
	// markers between chunks.
	OldStart int
	NewStart int
	// OldLines and NewLines are the line counts the header declares for each
	// side. OldStart+OldLines is where the next hunk's base numbering resumes,
	// which is what makes the elided-line gap countable.
	OldLines int
	NewLines int
	// Lines are the hunk's rows.
	Lines []DiffLine
}

// hunkHeader matches "@@ -old,count +new,count @@ trailing section".
//
// The counts are optional because git elides a single-line side ("@@ -1 +1 @@"),
// and a bare "0" is a real start: a diff against the empty tree, as the
// inspector produces for a repository with no commits or for a new file, opens
// with "@@ -0,0 +1,N @@".
var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// ParseUnified reads a single-file unified diff and returns its hunks.
//
// It streams from r, so the caller can hand it a subprocess pipe and never hold
// the whole diff in memory. Everything before the first hunk header — the
// "diff --git", "index", "new file mode", "---", and "+++" lines, including a
// "--- /dev/null" base — is ignored: the tree already carries the path and
// status, and the hunks are all the viewer renders.
//
// The empty-tree shape an unborn repository produces ("@@ -0,0 +1,N @@") needs
// no special case: a zero old start and count are ordinary values here, and a
// hunk with no lines on either side is dropped rather than rendered as a bare
// header row.
func ParseUnified(r io.Reader) ([]DiffChunk, error) {
	scanner := newScanner(r)
	var chunks []DiffChunk
	// current is the index of the hunk being filled, or -1 before the first.
	current := -1
	oldNum, newNum := 0, 0

	for scanner.Scan() {
		line := scanner.Text()

		if match := hunkHeader.FindStringSubmatch(line); match != nil {
			oldStart, oldCount := parseRange(match[1], match[2])
			newStart, newCount := parseRange(match[3], match[4])
			if oldCount == 0 && newCount == 0 {
				// Nothing on either side: skip the degenerate header so it
				// cannot become an empty hunk row in the viewport.
				current = -1
				continue
			}
			chunks = append(chunks, DiffChunk{
				Header: line, OldStart: oldStart, NewStart: newStart,
				OldLines: oldCount, NewLines: newCount,
			})
			current = len(chunks) - 1
			oldNum, newNum = oldStart, newStart
			continue
		}
		if current < 0 || line == "" {
			continue
		}

		switch line[0] {
		case ' ':
			chunks[current].Lines = append(chunks[current].Lines, DiffLine{
				Type: LineContext, OldLineNum: oldNum, NewLineNum: newNum, Content: sanitize(line[1:]),
			})
			oldNum++
			newNum++
		case '-':
			chunks[current].Lines = append(chunks[current].Lines, DiffLine{
				Type: LineDeletion, OldLineNum: oldNum, Content: sanitize(line[1:]),
			})
			oldNum++
		case '+':
			chunks[current].Lines = append(chunks[current].Lines, DiffLine{
				Type: LineAddition, NewLineNum: newNum, Content: sanitize(line[1:]),
			})
			newNum++
		case '\\':
			// The no-newline marker annotates the row above; it never advances
			// either side's counter.
			chunks[current].Lines = append(chunks[current].Lines, DiffLine{
				Type: LineMeta, Content: strings.TrimSpace(sanitize(line[1:])),
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("diff: scan output: %w", err)
	}

	annotateIntraLine(chunks)
	return chunks, nil
}

// ParseUnifiedString parses a unified diff held in memory. It is the string
// convenience over ParseUnified and exists for tests and callers that already
// have the bytes.
func ParseUnifiedString(text string) ([]DiffChunk, error) {
	return ParseUnified(strings.NewReader(text))
}

// parseRange decodes one side of a hunk header. A missing count is one line,
// which is how git elides ",1".
func parseRange(start, count string) (int, int) {
	s, _ := strconv.Atoi(start)
	if count == "" {
		return s, 1
	}
	c, _ := strconv.Atoi(count)
	return s, c
}

// maxIntraLineTokens bounds the word diff. Beyond it the intra-line highlight
// is skipped and the whole line reads as changed, which is both correct and
// cheap: pairing two 5,000-token minified lines would cost more than the
// highlight is worth.
const maxIntraLineTokens = 400

// annotateIntraLine computes word-level highlights for adjacent changed line
// pairs inside every chunk. It mutates chunks in place.
//
// git emits a run of deletions immediately followed by a run of additions. The
// i-th deletion pairs with the i-th addition, which is the same alignment
// GitHub's split view uses and is what makes "one word changed" read as one
// word rather than a whole line.
func annotateIntraLine(chunks []DiffChunk) {
	for c := range chunks {
		lines := chunks[c].Lines
		for i := 0; i < len(lines); {
			if lines[i].Type != LineDeletion {
				i++
				continue
			}
			delEnd := i
			for delEnd < len(lines) && lines[delEnd].Type == LineDeletion {
				delEnd++
			}
			addEnd := delEnd
			for addEnd < len(lines) && lines[addEnd].Type == LineAddition {
				addEnd++
			}

			if pairs := minInt(delEnd-i, addEnd-delEnd); pairs > 0 {
				for p := 0; p < pairs; p++ {
					oldSpans, newSpans := wordDiff(lines[i+p].Content, lines[delEnd+p].Content)
					lines[i+p].Highlights = oldSpans
					lines[delEnd+p].Highlights = newSpans
				}
			}
			i = addEnd
		}
	}
}

// wordDiff returns the changed spans of each side of a modified line pair.
//
// The tokens are interned and diffed with the shared Myers engine, so word
// highlighting and file-level highlighting agree on tie-breaking; there is one
// diff implementation in the tree, not two.
func wordDiff(oldLine, newLine string) (oldSpans, newSpans []HighlightSpan) {
	if oldLine == "" || newLine == "" || oldLine == newLine {
		return nil, nil
	}
	oldTokens := tokenizeLine(oldLine)
	newTokens := tokenizeLine(newLine)
	if len(oldTokens) > maxIntraLineTokens || len(newTokens) > maxIntraLineTokens {
		return fullSpan(oldLine), fullSpan(newLine)
	}

	oldText := make([]string, len(oldTokens))
	for i, t := range oldTokens {
		oldText[i] = t.text
	}
	newText := make([]string, len(newTokens))
	for i, t := range newTokens {
		newText[i] = t.text
	}

	oldIDs, newIDs := linediff.Intern(oldText, newText)
	runs := linediff.Myers(oldIDs, newIDs)

	oldPos, newPos := 0, 0
	for _, run := range runs {
		switch {
		case run.Removed:
			oldSpans = append(oldSpans, spanOf(oldTokens, oldPos, run.Count))
			oldPos += run.Count
		case run.Added:
			newSpans = append(newSpans, spanOf(newTokens, newPos, run.Count))
			newPos += run.Count
		default:
			oldPos += run.Count
			newPos += run.Count
		}
	}
	return mergeSpans(oldSpans), mergeSpans(newSpans)
}

// wordToken is one token with its byte range in the source line.
type wordToken struct {
	text       string
	start, end int
}

// tokenizeLine splits a line into word, whitespace, and punctuation tokens,
// each carrying its byte offsets. Offsets, not copies, are what let the diff
// run over token text while the result maps back to exact character ranges.
func tokenizeLine(s string) []wordToken {
	var tokens []wordToken
	runes := []rune(s)
	byteAt := make([]int, len(runes)+1)
	pos := 0
	for i, r := range runes {
		byteAt[i] = pos
		pos += len(string(r))
	}
	byteAt[len(runes)] = pos

	i := 0
	for i < len(runes) {
		start := i
		switch {
		case unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_':
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_') {
				i++
			}
		case unicode.IsSpace(runes[i]):
			for i < len(runes) && unicode.IsSpace(runes[i]) {
				i++
			}
		default:
			i++
		}
		tokens = append(tokens, wordToken{
			text:  string(runes[start:i]),
			start: byteAt[start],
			end:   byteAt[i],
		})
	}
	return tokens
}

// spanOf returns the byte span covering count tokens from pos.
func spanOf(tokens []wordToken, pos, count int) HighlightSpan {
	return HighlightSpan{Start: tokens[pos].start, End: tokens[pos+count-1].end}
}

// mergeSpans coalesces overlapping or touching spans, so adjacent changed
// tokens style as one run instead of a run per token.
func mergeSpans(spans []HighlightSpan) []HighlightSpan {
	if len(spans) < 2 {
		return spans
	}
	out := make([]HighlightSpan, 0, len(spans))
	out = append(out, spans[0])
	for _, span := range spans[1:] {
		last := &out[len(out)-1]
		if span.Start <= last.End {
			if span.End > last.End {
				last.End = span.End
			}
			continue
		}
		out = append(out, span)
	}
	return out
}

// fullSpan marks an entire line changed, the fallback when the pair is too
// large to word-diff.
func fullSpan(line string) []HighlightSpan {
	return []HighlightSpan{{Start: 0, End: len(line)}}
}

// minInt is a local minimum; the package deliberately avoids pulling in the
// generic helper for two call sites.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
