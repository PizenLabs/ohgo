// Package edit locates and applies edits to file content.
//
// It replaces the original `pi-edit` crate's replace mode, the path the agent's
// `edit` tool takes. The value over a plain `strings.Replace` is that it
// *locates* text robustly and reports ambiguity precisely, so a model that
// mis-transcribes a line by one character gets a correction rather than a
// failure or a silently wrong application.
package edit

import (
	"strings"
)

// Thresholds governing when a near match is accepted.
const (
	// DefaultFuzzyThreshold is the confidence a candidate must reach before it
	// is considered a match at all.
	DefaultFuzzyThreshold = 0.95
	// DominantFuzzyMinConfidence is the confidence a single candidate must
	// reach before it can win against a rival.
	DominantFuzzyMinConfidence = 0.97
	// DominantFuzzyDelta is the margin by which the best candidate must beat
	// the second best to be considered unambiguous.
	DominantFuzzyDelta = 0.08
)

// Match is a located span of content.
type Match struct {
	// ActualText is the text that was actually found, which for a fuzzy match
	// differs from what the caller asked for.
	ActualText string
	// StartIndex is the byte offset of the match start.
	StartIndex int
	// StartLine is the 1-based line the match starts on.
	StartLine int
	// Confidence is 1.0 for an exact match and a similarity score otherwise.
	Confidence float64
}

// Outcome is the result of locating a target.
//
// A non-nil Matched means the target was located. A non-nil Occurrences with no
// Match means the target was found but is ambiguous, which is a *failure* the
// caller must report rather than guess at.
type Outcome struct {
	// Matched is the located span, or nil when nothing was found.
	Matched *Match
	// Closest is the nearest candidate even when it fell below the threshold,
	// so an error can offer a concrete suggestion.
	Closest *Match
	// Occurrences counts exact matches when there is more than one.
	Occurrences int
	// OccurrenceLines lists the line numbers of the ambiguous matches.
	OccurrenceLines []int
	// FuzzyMatches counts candidates that met the threshold.
	FuzzyMatches int
	// DominantFuzzy reports that one candidate clearly beat the rest.
	DominantFuzzy bool
}

// Options controls a search.
type Options struct {
	// AllowFuzzy permits accepting a near match.
	AllowFuzzy bool
	// Threshold overrides DefaultFuzzyThreshold when non-zero.
	Threshold float64
	// ExcludedRanges are byte ranges to skip, used by multi-edit flows so a
	// second edit does not re-match text the first edit already replaced.
	ExcludedRanges []Range
}

// Range is a half-open byte range.
type Range struct {
	// Start is the inclusive first byte.
	Start int
	// End is the exclusive last byte.
	End int
}

// Find locates a target in content.
//
// An exact match always wins over a fuzzy one, and an ambiguous exact match is
// reported as ambiguity rather than resolved: guessing which occurrence a model
// meant would silently edit the wrong place.
func Find(content, target string, opts Options) Outcome {
	if target == "" {
		return Outcome{}
	}
	if exact, ok := findExact(content, target, opts.ExcludedRanges); ok {
		return exact
	}

	threshold := opts.Threshold
	if threshold == 0 {
		threshold = DefaultFuzzyThreshold
	}
	if !opts.AllowFuzzy {
		return Outcome{}
	}

	best, secondBest, aboveCount, haveBest := bestFuzzy(content, target, threshold, opts.ExcludedRanges)
	if !haveBest {
		return Outcome{}
	}

	bestMatch := best.match()
	if aboveCount == 1 {
		return Outcome{Matched: bestMatch, Closest: bestMatch}
	}
	if aboveCount > 1 && best.Confidence >= DominantFuzzyMinConfidence &&
		best.Confidence-secondBest.Confidence >= DominantFuzzyDelta {
		return Outcome{
			Matched:       bestMatch,
			Closest:       bestMatch,
			FuzzyMatches:  aboveCount,
			DominantFuzzy: true,
		}
	}
	return Outcome{Closest: bestMatch, FuzzyMatches: aboveCount}
}

// findExact searches for every occurrence of target.
//
// It reports ok=false when there is no exact match at all, leaving the caller
// to fall back to fuzzy.
func findExact(content, target string, excluded []Range) (Outcome, bool) {
	type hit struct {
		index int
	}

	var (
		first    = -1
		count    int
		recorded []hit
		searchAt = 0
	)

	for searchAt <= len(content)-len(target) {
		relative := strings.Index(content[searchAt:], target)
		if relative < 0 {
			break
		}
		index := searchAt + relative
		end := index + len(target)

		if !overlapsExcluded(index, end, excluded) {
			if first < 0 {
				first = index
			}
			count++
			// Recording is capped: a pathological file with thousands of hits
			// must not build an unbounded list just to report "ambiguous".
			if len(recorded) < maxRecordedMatches {
				recorded = append(recorded, hit{index: index})
			}
		}
		searchAt = end
	}

	if first < 0 {
		return Outcome{}, false
	}

	if count > 1 {
		lines := make([]int, 0, len(recorded))
		for _, h := range recorded {
			lines = append(lines, lineAt(content, h.index))
		}
		return Outcome{Occurrences: count, OccurrenceLines: lines}, true
	}

	return Outcome{Matched: &Match{
		ActualText: target,
		StartIndex: first,
		StartLine:  lineAt(content, first),
		Confidence: 1.0,
	}}, true
}

// maxRecordedMatches caps how many ambiguous occurrences are reported.
const maxRecordedMatches = 20

// lineAt returns the 1-based line number of a byte offset.
func lineAt(content string, index int) int {
	if index > len(content) {
		index = len(content)
	}
	return strings.Count(content[:index], "\n") + 1
}

// overlapsExcluded reports whether a range intersects any excluded range.
func overlapsExcluded(start, end int, excluded []Range) bool {
	for _, r := range excluded {
		if start < r.End && r.Start < end {
			return true
		}
	}
	return false
}

// Apply replaces a located span with replacement text.
func Apply(content string, m Match, replacement string) (string, Range, error) {
	if m.StartIndex < 0 || m.StartIndex+m.len() > len(content) {
		return "", Range{}, errIndexOutOfRange
	}
	// The span's length comes from the matched text, which for a fuzzy match
	// differs from what the caller asked for. Replacing anything else would
	// corrupt the file.
	span := content[m.StartIndex : m.StartIndex+m.len()]
	out := content[:m.StartIndex] + replacement + content[m.StartIndex+m.len():]
	return out, Range{Start: m.StartIndex, End: m.StartIndex + len(span)}, nil
}

// len returns the byte length of the matched text.
func (m Match) len() int { return len(m.ActualText) }
