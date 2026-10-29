package diff

import (
	"fmt"
	"strings"
)

// HunkLineKind is the role of one row in a unified hunk.
type HunkLineKind string

// Hunk row kinds.
const (
	// KindContext is " ": the line is in both texts.
	KindContext HunkLineKind = "context"
	// KindRemoved is "-": the line is only in the old text.
	KindRemoved HunkLineKind = "removed"
	// KindAdded is "+": the line is only in the new text.
	KindAdded HunkLineKind = "added"
	// KindNoNewlineAtEof marks a preceding row that ended without a newline.
	KindNoNewlineAtEof HunkLineKind = "no-newline"
)

// HunkLine is one row of a unified hunk.
type HunkLine struct {
	// Kind is the row's role.
	Kind HunkLineKind
	// Text is the line content, without its trailing newline.
	Text string
}

// Hunk is one contiguous region of a unified diff.
//
// Starts are 1-based and lengths count the lines of each text the hunk covers,
// matching the `@@ -old_start,old_lines +new_start,new_lines @@` header.
type Hunk struct {
	// OldStart is the 1-based first line in the old text.
	OldStart int
	// OldLines counts old-text lines covered.
	OldLines int
	// NewStart is the 1-based first line in the new text.
	NewStart int
	// NewLines counts new-text lines covered.
	NewLines int
	// Lines are the body rows.
	Lines []HunkLine
}

// Header renders the "@@ -a,b +c,d @@" line for this hunk.
//
// A single-line side omits its count, as unified diff does: `-1` rather than
// `-1,1`.
func (h Hunk) Header() string {
	return fmt.Sprintf("@@ -%s +%s @@", rangeSpec(h.OldStart, h.OldLines), rangeSpec(h.NewStart, h.NewLines))
}

// rangeSpec renders one side of a hunk header.
func rangeSpec(start, count int) string {
	if count == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

// NoNewlineMarker is the line unified diff emits after a row that ended
// without a trailing newline.
const NoNewlineMarker = `\ No newline at end of file`

// DefaultContextLines is the context either side of a change when the caller
// does not choose. It matches git's default.
const DefaultContextLines = 3

// StructuredHunks builds unified hunks from two texts.
//
// contextLines is the number of unchanged lines either side of a change; zero
// uses DefaultContextLines. Two changes within contextLines of each other are
// merged into one hunk, which is what keeps a scattered edit readable.
func StructuredHunks(oldText, newText string, contextLines int) []Hunk {
	if contextLines <= 0 {
		contextLines = DefaultContextLines
	}

	oldTokens := LineTokens(oldText)
	newTokens := LineTokens(newText)
	oldIDs, newIDs := Intern(oldTokens, newTokens)
	runs := Myers(oldIDs, newIDs)
	return HunksFromTokens(oldTokens, newTokens, runs, contextLines)
}

// diffRow is one flattened diff row with its line numbers in both texts.
//
// Line numbers are flattened once so hunk grouping is a single pass rather than
// a re-walk of the run list.
type diffRow struct {
	kind    HunkLineKind
	text    string
	oldLine int
	newLine int
}

// HunksFromTokens builds hunks from precomputed line tokens and runs.
//
// Splitting once and reusing the tokens is what lets a caller render the same
// diff twice without re-tokenising.
func HunksFromTokens(oldTokens, newTokens []string, runs []Run, contextLines int) []Hunk {
	if contextLines <= 0 {
		contextLines = DefaultContextLines
	}

	// Flatten the runs into rows carrying their line numbers, so hunk grouping
	// is a single pass instead of a re-walk of the run list.
	var rows []diffRow
	oldIndex, newIndex := 0, 0

	// emit appends a row, plus the no-newline marker that must follow it when
	// the token it came from lacks a trailing newline.
	//
	// The marker belongs *in place*, immediately after the line it annotates,
	// which is what git does. Appending one at the end of the hunk instead
	// loses the old-side marker whenever the last row is an addition.
	emit := func(kind HunkLineKind, text string, token string, oldLine, newLine int, hasNewline bool) {
		rows = append(rows, diffRow{kind: kind, text: text, oldLine: oldLine, newLine: newLine})
		if !hasNewline {
			rows = append(rows, diffRow{kind: KindNoNewlineAtEof})
		}
	}

	for _, run := range runs {
		for i := 0; i < run.Count; i++ {
			switch {
			case run.Removed:
				token := oldTokens[oldIndex]
				emit(KindRemoved, stripNewline(token), token, oldIndex+1, 0, strings.HasSuffix(token, "\n"))
				oldIndex++
			case run.Added:
				token := newTokens[newIndex]
				emit(KindAdded, stripNewline(token), token, 0, newIndex+1, strings.HasSuffix(token, "\n"))
				newIndex++
			default:
				token := newTokens[newIndex]
				emit(KindContext, stripNewline(token), token, oldIndex+1, newIndex+1, strings.HasSuffix(token, "\n"))
				oldIndex++
				newIndex++
			}
		}
	}

	return groupHunks(rows, contextLines)
}

// groupHunks clusters rows into hunks, merging changes that fall within the
// context window of each other.
func groupHunks(rows []diffRow, contextLines int) []Hunk {
	// Index the changed rows so the context around them can be sliced.
	var changes []int
	for i, r := range rows {
		// The marker annotates the row above it and is not itself a change, so
		// it must not anchor a hunk or inflate the line counts.
		if r.kind != KindContext && r.kind != KindNoNewlineAtEof {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return nil
	}

	// Group changes whose context windows overlap. Comparing the windows rather
	// than the raw indices is what decides whether two changes belong to one
	// readable hunk: each change owns contextLines of its own on either side, so
	// the next change must land within that combined span.
	type group struct{ start, end int }
	var groups []group
	start, end := changes[0], changes[0]
	for _, index := range changes[1:] {
		if index-contextLines <= end+contextLines {
			end = index
			continue
		}
		groups = append(groups, group{start: start, end: end})
		start, end = index, index
	}
	groups = append(groups, group{start: start, end: end})

	var hunks []Hunk
	for _, g := range groups {
		from := maxInt(0, g.start-contextLines)
		to := minInt(len(rows)-1, g.end+contextLines)

		hunk := Hunk{}
		for i := from; i <= to; i++ {
			r := rows[i]
			hunk.Lines = append(hunk.Lines, HunkLine{Kind: r.kind, Text: r.text})

			switch r.kind {
			case KindRemoved:
				if hunk.OldStart == 0 {
					hunk.OldStart = r.oldLine
				}
				hunk.OldLines++
			case KindAdded:
				if hunk.NewStart == 0 {
					hunk.NewStart = r.newLine
				}
				hunk.NewLines++
			case KindContext:
				if hunk.OldStart == 0 {
					hunk.OldStart = r.oldLine
				}
				if hunk.NewStart == 0 {
					hunk.NewStart = r.newLine
				}
				hunk.OldLines++
				hunk.NewLines++
			}
		}

		hunks = append(hunks, hunk)
	}
	return hunks
}

// stripNewline removes a single trailing newline from a line token.
func stripNewline(token string) string {
	return strings.TrimSuffix(token, "\n")
}

// Render renders hunks as a unified diff body.
func Render(hunks []Hunk) string {
	var sb strings.Builder
	for _, hunk := range hunks {
		sb.WriteString(hunk.Header())
		sb.WriteString("\n")
		for _, line := range hunk.Lines {
			switch line.Kind {
			case KindContext:
				sb.WriteString(" " + line.Text)
			case KindRemoved:
				sb.WriteString("-" + line.Text)
			case KindAdded:
				sb.WriteString("+" + line.Text)
			case KindNoNewlineAtEof:
				sb.WriteString(NoNewlineMarker)
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// Unified renders the complete unified diff between two texts.
func Unified(oldText, newText string, contextLines int) string {
	return Render(StructuredHunks(oldText, newText, contextLines))
}
