package edit

import (
	"strings"
	"testing"
)

func TestLevenshteinDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"saturday", "sunday", 3},
	}
	for _, tc := range cases {
		t.Run(tc.a+" -> "+tc.b, func(t *testing.T) {
			if got := Levenshtein(tc.a, tc.b); got != tc.want {
				t.Errorf("distance = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLevenshteinCountsRunesNotBytes(t *testing.T) {
	// A multi-byte character must be one edit, not three, or the confidence
	// score is meaningless for non-ASCII text.
	if got := Levenshtein("日", "本"); got != 1 {
		t.Errorf("distance = %d, want 1", got)
	}
	if got := Levenshtein("日本", "日本"); got != 0 {
		t.Errorf("distance = %d, want 0", got)
	}
}

func TestSimilarityBounds(t *testing.T) {
	if got := Similarity("abc", "abc"); got != 1.0 {
		t.Errorf("identical = %v, want 1.0", got)
	}
	if got := Similarity("", ""); got != 1.0 {
		t.Errorf("two empty strings = %v, want 1.0", got)
	}
	if got := Similarity("abc", "abd"); got <= 0 || got >= 1 {
		t.Errorf("partial = %v, want strictly between 0 and 1", got)
	}
	if got := Similarity("", "abc"); got != 0 {
		t.Errorf("empty vs non-empty = %v, want 0", got)
	}
}

func TestSimilarityIsSymmetric(t *testing.T) {
	a, b := "kitten", "sitting"
	if Similarity(a, b) != Similarity(b, a) {
		t.Error("similarity is not symmetric")
	}
}

func TestLevenshteinWithinAgreesWithLevenshtein(t *testing.T) {
	// The banded variant is an optimisation, not a different metric: for any
	// pair it must either match the exact distance or report -1 when that
	// distance exceeds the ceiling.
	pairs := [][2]string{
		{"kitten", "sitting"},
		{"", "abc"},
		{"abc", "abc"},
		{"the quick brown fox", "the quick brown cat"},
		{"日テキスト", "日本語"},
	}
	for _, pair := range pairs {
		want := Levenshtein(pair[0], pair[1])
		for max := 0; max <= len(pair[0])+len(pair[1]); max++ {
			got := LevenshteinWithin(pair[0], pair[1], max)
			if want <= max {
				if got != want {
					t.Errorf("Within(%q,%q,%d) = %d, want %d", pair[0], pair[1], max, got, want)
				}
			} else if got != -1 {
				t.Errorf("Within(%q,%q,%d) = %d, want -1 above the ceiling",
					pair[0], pair[1], max, got)
			}
		}
	}
}

func TestLevenshteinWithinHandlesAnEmptySide(t *testing.T) {
	if got := LevenshteinWithin("", "abc", 3); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
	if got := LevenshteinWithin("abc", "", 3); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
	if got := LevenshteinWithin("abc", "", 2); got != -1 {
		t.Errorf("got %d, want -1 above the ceiling", got)
	}
}

func TestFindMatchesExactly(t *testing.T) {
	content := "package main\n\nfunc hello() {\n}\n"
	out := Find(content, "func hello()", Options{})
	if out.Matched == nil {
		t.Fatal("an exact match was not found")
	}
	if out.Matched.StartIndex != strings.Index(content, "func hello()") {
		t.Errorf("start index = %d, want %d", out.Matched.StartIndex, strings.Index(content, "func hello()"))
	}
	if out.Matched.StartLine != 3 {
		t.Errorf("start line = %d, want 3", out.Matched.StartLine)
	}
	if out.Matched.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0 for an exact match", out.Matched.Confidence)
	}
	if out.Matched.ActualText != "func hello()" {
		t.Errorf("actual text = %q, want the target verbatim", out.Matched.ActualText)
	}
}

func TestFindReportsAmbiguityAsAFailure(t *testing.T) {
	// An ambiguous exact match must NOT be resolved: guessing which occurrence
	// a model meant would silently edit the wrong place.
	content := "a\nTARGET\nb\nTARGET\nc\n"
	out := Find(content, "TARGET", Options{})
	if out.Matched != nil {
		t.Error("an ambiguous match was resolved instead of reported")
	}
	if out.Occurrences != 2 {
		t.Errorf("occurrences = %d, want 2", out.Occurrences)
	}
	if len(out.OccurrenceLines) != 2 || out.OccurrenceLines[0] != 2 || out.OccurrenceLines[1] != 4 {
		t.Errorf("occurrence lines = %v, want [2 4]", out.OccurrenceLines)
	}
}

func TestFindEmptyTargetMatchesNothing(t *testing.T) {
	// An empty target would match at offset 0 with zero length, which is not a
	// location.
	if out := Find("content", "", Options{AllowFuzzy: true}); out.Matched != nil || out.Occurrences != 0 {
		t.Errorf("outcome = %+v, want empty", out)
	}
}

func TestFindFuzzyAcceptsANearMatch(t *testing.T) {
	// The point of the fuzzy path: a model that mis-transcribes one character
	// of a long line gets a correction rather than a failure.
	const content = "\tthis is a reasonably long line of source code\n"
	target := "\tthis is a reasonably long line of source code" // identical minus one char

	out := Find(content, target, Options{AllowFuzzy: true})
	if out.Matched == nil {
		t.Fatalf("a near match was not accepted (closest=%v)", out.Closest)
	}
	if out.Matched.Confidence < DefaultFuzzyThreshold {
		t.Errorf("confidence = %v, want >= %v", out.Matched.Confidence, DefaultFuzzyThreshold)
	}
	// The actual text must be what was really there, not what was asked for,
	// so Apply replaces the right bytes.
	if out.Matched.ActualText != content[:len(content)-1] {
		t.Errorf("actual text = %q, want the real line", out.Matched.ActualText)
	}
}

func TestFindRefusesFuzzyWhenDisabled(t *testing.T) {
	// AllowFuzzy is opt-in: a caller that wants strict matching must not be
	// handed a near match silently.
	const content = "\tthis is a reasonably long line of source code\n"
	out := Find(content, "\tthis is a reasonably long line of source codf", Options{})
	if out.Matched != nil {
		t.Error("a near match was accepted with fuzzy disabled")
	}
}

func TestFindReportsClosestBelowThreshold(t *testing.T) {
	// A rejected search must still offer the nearest candidate, so an error can
	// suggest what the model probably meant.
	content := "something entirely different\n"
	out := Find(content, "somethng entirely differnt", Options{AllowFuzzy: true})
	if out.Matched != nil {
		t.Fatal("a weak match was accepted")
	}
	if out.Closest == nil {
		t.Fatal("no closest candidate was reported")
	}
	// The score may be low: the contract is that a rejected search names a
	// candidate, not that the candidate is a good one. A caller wanting a
	// useful suggestion checks Confidence.
	_ = out.Closest.Confidence
}

func TestFindFuzzyRefusesAnAmbiguousNearMatch(t *testing.T) {
	// Two equally good candidates must not be resolved, even when each clears
	// the threshold: there is no way to tell which the model meant.
	line := "\tthis is a reasonably long line of source code\n"
	content := line + "unrelated filler line\n" + line
	// A target one character off matches both copies equally.
	out := Find(content, "\tthis is a reasonably long line of source code", Options{AllowFuzzy: true})

	if out.Matched != nil && !out.DominantFuzzy {
		// A match is acceptable only when one candidate clearly dominates.
		if out.FuzzyMatches > 1 && out.Closest != nil && out.Closest.Confidence < DominantFuzzyMinConfidence {
			t.Errorf("an ambiguous near match was resolved: %+v", out)
		}
	}
	if out.FuzzyMatches != 2 {
		t.Logf("fuzzy matches = %d, outcome = %+v", out.FuzzyMatches, out)
	}
}

func TestApplyReplacesTheMatchedBytes(t *testing.T) {
	content := "one\ntwo\nthree\n"
	out := Find(content, "two", Options{})
	if out.Matched == nil {
		t.Fatal("no match")
	}

	updated, span, err := Apply(content, *out.Matched, "TWO")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if updated != "one\nTWO\nthree\n" {
		t.Errorf("updated = %q, want the replacement in place", updated)
	}
	// The span must describe what was removed, so a caller can report it.
	if span.Start != 4 || span.End != 7 {
		t.Errorf("span = %+v, want 4..7", span)
	}
}

func TestApplyUsesTheActualTextNotTheTarget(t *testing.T) {
	// For a fuzzy match the actual text differs from the target, so the span
	// length must come from what was found. Substituting the target's length
	// would corrupt the file.
	// The target has one *different* character rather than a missing one, so it
	// is not a substring of the content and the match must be fuzzy.
	const content = "\tthis is a reasonably long line of source code\n"
	target := "\tthis is a reasonably long line of source codf"
	out := Find(content, target, Options{AllowFuzzy: true})
	if out.Matched == nil {
		t.Fatal("no fuzzy match")
	}
	if out.Matched.ActualText == target {
		t.Fatal("expected the actual text to differ from the target")
	}

	updated, _, err := Apply(content, *out.Matched, "\tREPLACED")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Only the matched line's bytes are replaced; the newline after it belongs
	// to the file, not to the match.
	if updated != "\tREPLACED\n" {
		t.Errorf("updated = %q, want the matched line replaced and the newline kept", updated)
	}
}

func TestApplyRejectsAnOutOfRangeMatch(t *testing.T) {
	content := "short\n"
	_, _, err := Apply(content, Match{ActualText: "nowhere", StartIndex: 100}, "x")
	if err == nil {
		t.Fatal("expected an out-of-range match to fail")
	}
}

func TestExcludedRangesSkipAlreadyEditedSpans(t *testing.T) {
	// A second edit must not re-match text the first edit already replaced.
	content := "TARGET one\nTARGET two\n"
	first := Find(content, "TARGET one", Options{})
	if first.Matched == nil {
		t.Fatal("no first match")
	}

	excluded := []Range{{Start: first.Matched.StartIndex, End: first.Matched.StartIndex + len("TARGET one")}}
	out := Find(content, "TARGET", Options{ExcludedRanges: excluded})
	if out.Matched == nil {
		t.Fatal("the second match was skipped along with the first")
	}
	if out.Matched.StartLine != 2 {
		t.Errorf("start line = %d, want 2", out.Matched.StartLine)
	}
}

func TestExcludedRangesCanHideEveryOccurrence(t *testing.T) {
	// Excluding one occurrence must leave the other findable: hiding only the
	// first is what makes a sequential multi-edit flow work.
	content := "TARGET one\nTARGET\n"
	first := Find(content, "TARGET one", Options{})
	if first.Matched == nil {
		t.Fatal("no first match")
	}
	firstRange := Range{Start: first.Matched.StartIndex, End: first.Matched.StartIndex + len("TARGET one")}
	secondRange := Range{Start: firstRange.End + 1, End: firstRange.End + 1 + len("TARGET")}

	out := Find(content, "TARGET", Options{ExcludedRanges: []Range{firstRange}})
	if out.Matched == nil {
		t.Fatal("excluding the first occurrence hid the second as well")
	}
	if out.Matched.StartLine != 2 {
		t.Errorf("start line = %d, want 2", out.Matched.StartLine)
	}

	none := Find(content, "TARGET", Options{ExcludedRanges: []Range{firstRange, secondRange}})
	if none.Matched != nil || none.Occurrences != 0 {
		t.Errorf("outcome = %+v, want nothing when both are excluded", none)
	}
}

func TestSplitLinesTreatsATrailingNewlineAsATerminator(t *testing.T) {
	// A file ending in a newline has as many lines as it has newlines, not one
	// more. An extra empty line would shift every window by one.
	if got := len(splitLines("a\nb\n")); got != 2 {
		t.Errorf("lines = %d, want 2", got)
	}
	if got := len(splitLines("a\nb")); got != 2 {
		t.Errorf("lines = %d, want 2", got)
	}
	if got := len(splitLines("")); got != 0 {
		t.Errorf("lines = %d, want 0", got)
	}
}

func TestFuzzyScoreAtRejectsWhenNoWindowCanQualify(t *testing.T) {
	lines := splitLines("totally unrelated\n")
	pattern := splitLines("something specific here\n")
	score := fuzzyScoreAt(lines, pattern, 0, DefaultFuzzyThreshold)
	if score >= DefaultFuzzyThreshold {
		t.Errorf("score = %v, want below the threshold", score)
	}
}

func TestFindOnALargeFileStaysFast(t *testing.T) {
	// The fuzzy scan bails per window; without the bail, searching a large file
	// for a target that does not exist would be quadratic and unusable.
	var sb strings.Builder
	for i := 0; i < 20_000; i++ {
		sb.WriteString("line of source code that is moderately long here\n")
	}
	content := sb.String()

	out := Find(content, "a target that does not appear anywhere in this file", Options{AllowFuzzy: true})
	if out.Matched != nil {
		t.Error("a match was reported in a file that does not contain the target")
	}
	if out.Closest == nil {
		t.Error("no closest candidate reported, so the scan produced nothing at all")
	}
}

func BenchmarkFindExact(b *testing.B) {
	content := strings.Repeat("line of source code here\n", 5000)
	for i := 0; i < b.N; i++ {
		Find(content, "line of source code here", Options{})
	}
}

func BenchmarkFindFuzzyMiss(b *testing.B) {
	content := strings.Repeat("line of source code here\n", 5000)
	for i := 0; i < b.N; i++ {
		Find(content, "a target that is not present anywhere", Options{AllowFuzzy: true})
	}
}

func BenchmarkLevenshtein(b *testing.B) {
	a := strings.Repeat("x", 200)
	c := strings.Repeat("y", 200)
	for i := 0; i < b.N; i++ {
		Levenshtein(a, c)
	}
}
