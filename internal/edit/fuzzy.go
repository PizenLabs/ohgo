package edit

import (
	"errors"
	"strings"
)

// Fuzzy-search errors.
var (
	// errIndexOutOfRange means a match no longer describes a valid span of the
	// content, which happens when the content changed underneath it.
	errIndexOutOfRange = errors.New("edit: match is out of range for the content")
)

// Levenshtein returns the edit distance between two strings, in characters.
//
// It is rune-based rather than byte-based, so a multi-byte character counts as
// one edit rather than three. That is what makes the confidence score
// meaningful for non-ASCII text.
func Levenshtein(a, b string) int {
	return levenshteinChars([]rune(a), []rune(b))
}

// levenshteinChars is the unbounded dynamic-programming distance.
func levenshteinChars(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}

	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = minIntLevenshtein(
				previous[j]+1,      // deletion
				current[j-1]+1,     // insertion
				previous[j-1]+cost, // substitution
			)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

func minIntLevenshtein(values ...int) int {
	best := values[0]
	for _, v := range values[1:] {
		if v < best {
			best = v
		}
	}
	return best
}

// LevenshteinWithin returns the distance, or -1 when it exceeds max.
//
// A banded variant exists because a caller scanning a large file asks about
// thousands of windows and almost all are far apart; bounding the work keeps
// the scan linear in practice rather than quadratic per candidate.
func LevenshteinWithin(a, b string, max int) int {
	if max < 0 {
		return -1
	}
	return levenshteinCharsWithin([]rune(a), []rune(b), max)
}

// levenshteinCharsWithin computes the distance against a ceiling.
//
// Shared prefixes and suffixes are stripped first, which is the common case for
// a near-identical line and makes the useful comparison short.
//
// The band only fills cells within `max` of the diagonal; everything outside is
// `over`, a sentinel above the ceiling, so a path that leaves the band can
// never win and the caller still learns "further than max".
func levenshteinCharsWithin(a, b []rune, max int) int {
	if equalRunes(a, b) {
		return 0
	}

	shared := 0
	for shared < len(a) && shared < len(b) && a[shared] == b[shared] {
		shared++
	}
	a, b = a[shared:], b[shared:]

	shared = 0
	for shared < len(a) && shared < len(b) && a[len(a)-1-shared] == b[len(b)-1-shared] {
		shared++
	}
	a, b = a[:len(a)-shared], b[:len(b)-shared]

	longer, shorter := a, b
	if len(b) > len(a) {
		longer, shorter = b, a
	}
	rows, columns := len(longer), len(shorter)

	// A length gap alone exceeds the ceiling.
	if rows-columns > max {
		return -1
	}
	if columns == 0 {
		return rows
	}
	if max > rows {
		max = rows
	}

	over := max + 1
	previous := make([]int, columns+1)
	current := make([]int, columns+1)
	for j := range previous {
		previous[j] = j
	}

	for i := 1; i <= rows; i++ {
		lo, hi := rowBounds(i, columns, max)

		// Cells outside the band are unreachable within the ceiling.
		for j := 0; j <= columns; j++ {
			if j < lo || j > hi {
				current[j] = over
			}
		}
		// dp[i][0] is the distance of a prefix against the empty string.
		if i <= max {
			current[0] = i
		}

		for j := lo; j <= hi; j++ {
			cost := 1
			if longer[i-1] == shorter[j-1] {
				cost = 0
			}
			best := previous[j-1] + cost
			if v := previous[j] + 1; v < best {
				best = v
			}
			if v := current[j-1] + 1; v < best {
				best = v
			}
			current[j] = best
		}

		previous, current = current, previous
	}

	if previous[columns] > max {
		return -1
	}
	return previous[columns]
}

// rowBounds returns the inclusive column range reachable within the band.
func rowBounds(row, columns, max int) (int, int) {
	low := row - max
	if low < 1 {
		low = 1
	}
	high := row + max
	if high > columns {
		high = columns
	}
	return low, high
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Similarity scores two strings from 0 to 1, where 1 means identical.
//
// Two empty strings are identical, which is why the empty case returns 1 rather
// than dividing by zero.
func Similarity(a, b string) float64 {
	aRunes := []rune(a)
	bRunes := []rune(b)
	maxLen := maxIntLevenshtein(len(aRunes), len(bRunes))
	if maxLen == 0 {
		return 1.0
	}
	return 1.0 - float64(levenshteinChars(aRunes, bRunes))/float64(maxLen)
}

func maxIntLevenshtein(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// candidate is one fuzzy match found in the content.
type candidate struct {
	// LineIndex is the 0-based line the pattern starts on.
	LineIndex int
	// StartIndex is the byte offset of the match start.
	StartIndex int
	// Confidence is the mean similarity across the pattern's lines.
	Confidence float64
	// ActualText is the text actually found.
	ActualText string
}

// bestFuzzy scans for the window that best matches a multi-line target.
//
// It returns the best and second-best candidates plus how many met the
// threshold, because the caller needs all three: the best alone cannot say
// whether the match is unambiguous.
func bestFuzzy(content, target string, threshold float64, excluded []Range) (best, secondBest candidate, aboveCount int, haveBest bool) {
	lines := splitLines(content)
	pattern := splitLines(target)
	if len(pattern) == 0 || len(pattern) > len(lines) {
		return candidate{}, candidate{}, 0, false
	}

	// Byte offsets of each line start, so a line index maps to an offset.
	offsets := lineOffsets(content, len(lines))

	for i := 0; i+len(pattern) <= len(lines); i++ {
		start := offsets[i]
		end := offsets[i+len(pattern)-1] + len(lines[i+len(pattern)-1])
		if overlapsExcluded(start, end, excluded) {
			continue
		}

		score := fuzzyScoreAt(lines, pattern, i, threshold)

		found := candidate{
			LineIndex:  i,
			StartIndex: start,
			Confidence: score,
			ActualText: content[start:end],
		}

		// Track the best candidate overall, even below the threshold: the
		// original reports it as `closest` so an error can offer a concrete
		// suggestion instead of "not found". A candidate whose score bailed to
		// zero is still recorded — that matches the original, and a caller
		// wanting a *useful* suggestion checks Confidence rather than nil.
		switch {
		case !haveBest || score > best.Confidence:
			if haveBest {
				secondBest = best
			}
			best = found
			haveBest = true
		case score > secondBest.Confidence:
			secondBest = found
		}

		if score < threshold {
			continue
		}
		aboveCount++
	}
	return best, secondBest, aboveCount, haveBest
}

// splitLines splits text into lines without their terminators.
//
// A trailing newline does not produce a final empty line: a file ending in a
// newline has as many lines as it has newlines, not one more.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if last := len(lines) - 1; lines[last] == "" {
		lines = lines[:last]
	}
	return lines
}

// lineOffsets returns the byte offset of each line start.
func lineOffsets(content string, lineCount int) []int {
	offsets := make([]int, 0, lineCount+1)
	offset := 0
	for i := 0; i < lineCount; i++ {
		offsets = append(offsets, offset)
		next := strings.IndexByte(content[offset:], '\n')
		if next < 0 {
			break
		}
		offset += next + 1
	}
	return offsets
}

// fuzzyScoreAt scores the pattern against the window starting at line i.
//
// It bails out early once the remaining lines cannot lift the score above
// minScore: scanning every line of a large file for a pattern that already
// failed is the difference between fast and unusable.
func fuzzyScoreAt(lines []string, pattern []string, i int, minScore float64) float64 {
	count := len(pattern)
	total := 0.0

	for offset, want := range pattern {
		line := lines[i+offset]
		if line == want {
			total += 1.0
			continue
		}

		remaining := count - offset - 1
		maxLen := maxIntLevenshtein(len([]rune(line)), len([]rune(want)))
		upperBound := 1.0
		if maxLen > 0 {
			upperBound = 1.0 - float64(absIntLevenshtein(len([]rune(line))-len([]rune(want))))/float64(maxLen)
		}
		// No configuration of the remaining lines can still reach minScore.
		if (total+upperBound+float64(remaining))/float64(count) < minScore {
			return total / float64(count)
		}
		if upperBound > 0 {
			total += Similarity(line, want)
		}
		if (total+float64(remaining))/float64(count) < minScore {
			return total / float64(count)
		}
	}
	return total / float64(count)
}

func absIntLevenshtein(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// match converts a candidate into a Match.
func (c candidate) match() *Match {
	return &Match{
		ActualText: c.ActualText,
		StartIndex: c.StartIndex,
		StartLine:  0,
		Confidence: c.Confidence,
	}
}
