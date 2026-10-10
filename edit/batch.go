package edit

import (
	"fmt"
	"strings"
)

// Entry is one edit in a batch.
type Entry struct {
	// OldString is the text to replace. A batch with any empty entry is
	// rejected outright, because an empty match is not a location.
	OldString string
	// NewString is the replacement.
	NewString string
	// ReplaceAll replaces every occurrence rather than refusing an ambiguous
	// match. It is opt-in per entry.
	ReplaceAll bool
}

// AppliedEntry is the outcome of one entry in a committed batch.
type AppliedEntry struct {
	// Index is the entry's position in the batch.
	Index int
	// Replaced counts how many occurrences were replaced.
	Replaced int
	// Line is the 1-based line of the first replacement.
	Line int
	// Confidence is 1.0 for an exact replacement and the similarity otherwise.
	Confidence float64
}

// BatchResult is the outcome of applying a batch.
type BatchResult struct {
	// Content is the new content.
	Content string
	// Applied describes each entry that ran, in batch order.
	Applied []AppliedEntry
}

// Batch applies several edits to one content.
//
// Entries apply **sequentially against the evolving content**: the second entry
// sees what the first produced. That is what lets a batch rename a symbol and
// then update its callers, which two independent edits against the original
// text could not express.
//
// A batch is all-or-nothing. Any failure leaves the content untouched, because
// a half-applied batch is not a state the user asked for and cannot easily be
// reasoned about.
type Batch struct {
	// AllowFuzzy permits near matches, as in Find.
	AllowFuzzy bool
	// Threshold overrides DefaultFuzzyThreshold when non-zero.
	Threshold float64
}

// Apply runs the batch.
func (b Batch) Apply(content string, entries []Entry) (BatchResult, error) {
	if len(entries) == 0 {
		return BatchResult{}, fmt.Errorf("edit: no entries to apply")
	}
	// Validating every entry before touching anything means a malformed batch
	// fails without a partial write.
	for i, entry := range entries {
		if entry.OldString == "" {
			return BatchResult{}, fmt.Errorf("edit: entry %d has an empty old_string", i)
		}
	}

	current := content
	// excluded accumulates spans an earlier entry already replaced, so a later
	// entry cannot match text that is no longer there.
	var excluded []Range
	applied := make([]AppliedEntry, 0, len(entries))

	for i, entry := range entries {
		opts := Options{
			AllowFuzzy:     b.AllowFuzzy,
			Threshold:      b.Threshold,
			ExcludedRanges: excluded,
		}

		var matches []Match
		if entry.ReplaceAll {
			matches = ReplaceEvery(current, entry.OldString, opts)
			if len(matches) == 0 {
				return BatchResult{}, fmt.Errorf("edit: entry %d not found", i)
			}
		} else {
			out := Find(current, entry.OldString, opts)
			switch {
			case out.Matched != nil:
				matches = []Match{*out.Matched}
			case out.Occurrences > 1:
				// Ambiguity is refused rather than resolved: guessing which
				// occurrence was meant would silently edit the wrong line.
				return BatchResult{}, fmt.Errorf(
					"edit: entry %d matches %d times; include more context to make it unique",
					i, out.Occurrences)
			case out.Closest != nil:
				return BatchResult{}, fmt.Errorf(
					"edit: entry %d not found; closest match line %d (confidence %.2f)",
					i, out.Closest.StartLine, out.Closest.Confidence)
			default:
				return BatchResult{}, fmt.Errorf("edit: entry %d not found", i)
			}
		}

		// Replace last-to-first so earlier offsets stay valid: each replacement
		// shifts every byte after it.
		confidence := 1.0
		firstLine := 0
		replaced := 0
		for j := len(matches) - 1; j >= 0; j-- {
			match := matches[j]
			if match.StartIndex < 0 || match.StartIndex+match.len() > len(current) {
				return BatchResult{}, fmt.Errorf("edit: entry %d: %w", i, errIndexOutOfRange)
			}
			current = current[:match.StartIndex] + entry.NewString + current[match.StartIndex+match.len():]
			excluded = append(excluded, Range{Start: match.StartIndex, End: match.StartIndex + match.len()})
			replaced++
			if match.Confidence < confidence {
				confidence = match.Confidence
			}
			if firstLine == 0 || match.StartLine < firstLine {
				firstLine = match.StartLine
			}
		}

		applied = append(applied, AppliedEntry{
			Index:      i,
			Replaced:   replaced,
			Line:       firstLine,
			Confidence: confidence,
		})
	}

	if current == content {
		return BatchResult{}, fmt.Errorf("edit: the batch resulted in no changes")
	}
	return BatchResult{Content: current, Applied: applied}, nil
}

// ReplaceEvery returns the match for every occurrence of a target.
//
// It is the seam `replace_all` needs: Find reports a single match or ambiguity,
// and this enumerates them so a caller can replace each one.
func ReplaceEvery(content, target string, opts Options) []Match {
	var out []Match
	searchAt := 0
	for searchAt <= len(content)-len(target) {
		relative := strings.Index(content[searchAt:], target)
		if relative < 0 {
			break
		}
		index := searchAt + relative
		end := index + len(target)
		if !overlapsExcluded(index, end, opts.ExcludedRanges) {
			out = append(out, Match{
				ActualText: target,
				StartIndex: index,
				StartLine:  lineAt(content, index),
				Confidence: 1.0,
			})
		}
		searchAt = end
	}
	return out
}
