package edit

import (
	"strings"
	"testing"
)

const batchSource = `package main

func oldName() {}

func caller() {
	oldName()
	oldName()
}
`

func TestBatchAppliesEntriesSequentially(t *testing.T) {
	// The second entry must see what the first produced: that is what lets a
	// batch rename a symbol and then update its callers.
	content := batchSource

	result, err := (Batch{}).Apply(content, []Entry{
		{OldString: "func oldName() {}", NewString: "func newName() {}"},
		// The second entry sees the first entry's output, and replaces every
		// call site because a rename legitimately touches several lines.
		{OldString: "\toldName()", NewString: "\tnewName()", ReplaceAll: true},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(result.Content, "func newName() {}") {
		t.Errorf("content = %q, want the definition renamed", result.Content)
	}
	// Both call sites should now use the new name.
	if got := strings.Count(result.Content, "oldName()"); got != 0 {
		t.Errorf("content still has %d oldName occurrences", got)
	}
	if len(result.Applied) != 2 {
		t.Errorf("applied = %d entries, want 2", len(result.Applied))
	}
	if got := result.Applied[1].Replaced; got != 2 {
		t.Errorf("second entry replaced %d occurrences, want 2", got)
	}
}

func TestBatchIsAllOrNothing(t *testing.T) {
	// A failure part-way must leave the content untouched: a half-applied batch
	// is not a state the user asked for.
	content := batchSource
	batch := Batch{}
	entries := []Entry{
		{OldString: "func oldName() {}", NewString: "func newName() {}"},
		{OldString: "this text does not exist at all", NewString: "x"},
	}

	if _, err := batch.Apply(content, entries); err == nil {
		t.Fatal("a batch with a failing entry succeeded")
	}
	// The content argument is a value, so the caller's copy cannot have moved;
	// the point is that no result carrying a partial edit is ever returned.
	if result, err := batch.Apply(content, entries); err == nil {
		_ = result
		t.Fatal("expected the same failure on a retry")
	}
}

func TestBatchRejectsAnEmptyOldString(t *testing.T) {
	// An empty match is not a location, so it is refused up front rather than
	// matching at offset 0 with zero length.
	for _, entry := range []Entry{
		{OldString: "", NewString: "x"},
		{OldString: "content", NewString: ""},
	} {
		if _, err := (Batch{}).Apply("a content", []Entry{entry}); err == nil && entry.OldString == "" {
			t.Error("an empty old_string was accepted")
		}
	}
}

func TestBatchRejectsAnEmptyEntryList(t *testing.T) {
	if _, err := (Batch{}).Apply("content", nil); err == nil {
		t.Error("an empty batch was accepted")
	}
}

func TestBatchRejectsANoop(t *testing.T) {
	// A batch whose net effect is nothing is a mistake worth reporting, rather
	// than a silent success that writes the same bytes back.
	if _, err := (Batch{}).Apply("content here", []Entry{
		{OldString: "content", NewString: "content"},
	}); err == nil {
		t.Error("a batch that changed nothing was accepted")
	}
}

func TestBatchRefusesAnAmbiguousEntry(t *testing.T) {
	// Ambiguity must stay refused inside a batch too, or a batch would be a
	// loophole around the single-edit rule.
	if _, err := (Batch{}).Apply(batchSource, []Entry{
		{OldString: "\toldName()", NewString: "\tnewName()"},
	}); err == nil {
		t.Fatal("an ambiguous entry inside a batch was accepted")
	}
}

func TestBatchReplaceAllReplacesEveryOccurrence(t *testing.T) {
	// replace_all is the escape hatch for a deliberate rename that legitimately
	// touches several lines.
	content := batchSource

	result, err := (Batch{}).Apply(content, []Entry{
		{OldString: "\toldName()", NewString: "\tnewName()", ReplaceAll: true},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := strings.Count(result.Content, "\tnewName()"); got != 2 {
		t.Errorf("replaced %d occurrences, want 2", got)
	}
	if got := result.Applied[0].Replaced; got != 2 {
		t.Errorf("reported %d replaced, want 2", got)
	}
}

func TestBatchReplaceAllWithNoMatchFails(t *testing.T) {
	if _, err := (Batch{}).Apply("content", []Entry{
		{OldString: "absent", NewString: "x", ReplaceAll: true},
	}); err == nil {
		t.Error("replace_all with no match succeeded")
	}
}

func TestBatchExcludedRangesPreventRematching(t *testing.T) {
	// A later entry must not re-match inside text an earlier entry just
	// inserted. Entry 1 writes "MARKER" where "beta" was; entry 2 then asks for
	// "MARKER" and must not find it, because it is inside a span the batch
	// already produced.
	content := "alpha beta alpha\n"

	if _, err := (Batch{}).Apply(content, []Entry{
		{OldString: "beta", NewString: "MARKER"},
		{OldString: "MARKER", NewString: "clobbered"},
	}); err == nil {
		t.Error("a later entry matched text an earlier entry had just inserted")
	}
}

func TestBatchAppliesReplacementsLastToFirst(t *testing.T) {
	// Applying in document order would shift the offsets of every match after
	// the first, corrupting later ones.
	content := "a a a a a\n"

	result, err := (Batch{}).Apply(content, []Entry{
		{OldString: "a", NewString: "b", ReplaceAll: true},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Content != "b b b b b\n" {
		t.Errorf("content = %q, want every occurrence replaced", result.Content)
	}
}

func TestBatchReportsTheFirstChangedLine(t *testing.T) {
	// The reported line is what a caller shows the user, so it must be the
	// earliest line the entry touched.
	content := "one\ntwo\nthree\nfour\n"
	result, err := (Batch{}).Apply(content, []Entry{
		{OldString: "three", NewString: "THREE"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Applied[0].Line != 3 {
		t.Errorf("line = %d, want 3", result.Applied[0].Line)
	}
	if result.Applied[0].Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0 for an exact match", result.Applied[0].Confidence)
	}
}

func TestReplaceEveryFindsAllOccurrences(t *testing.T) {
	content := "x\nTARGET\ny\nTARGET\n"
	matches := ReplaceEvery(content, "TARGET", Options{})
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	if matches[0].StartLine != 2 || matches[1].StartLine != 4 {
		t.Errorf("lines = %d,%d, want 2,4", matches[0].StartLine, matches[1].StartLine)
	}
	// The matches must be in ascending offset order, because Apply walks them
	// backwards.
	if matches[0].StartIndex >= matches[1].StartIndex {
		t.Error("matches are not in ascending order")
	}
}

func TestReplaceEveryHonoursExclusions(t *testing.T) {
	content := "TARGET one\nTARGET\n"
	all := ReplaceEvery(content, "TARGET", Options{})
	if len(all) != 2 {
		t.Fatalf("matches = %d, want 2", len(all))
	}
	excluded := []Range{{Start: all[0].StartIndex, End: all[0].StartIndex + len("TARGET")}}
	if got := ReplaceEvery(content, "TARGET", Options{ExcludedRanges: excluded}); len(got) != 1 {
		t.Errorf("matches = %d, want 1 after excluding the first", len(got))
	}
}

func TestReplaceEveryOnAnAbsentTarget(t *testing.T) {
	if got := ReplaceEvery("content", "absent", Options{}); len(got) != 0 {
		t.Errorf("matches = %+v, want none", got)
	}
}

func TestBatchAcceptsAFuzzyMatch(t *testing.T) {
	// Inside a batch, a near match is still a match when fuzzy is enabled.
	const content = "\tthis is a reasonably long line of source code here\n"
	target := "\tthis is a reasonably long line of source code herf"

	result, err := Batch{AllowFuzzy: true}.Apply(content, []Entry{
		{OldString: target, NewString: "\treplaced"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(result.Content, "\treplaced") {
		t.Errorf("content = %q, want the replacement", result.Content)
	}
	if result.Applied[0].Confidence >= 1.0 {
		t.Errorf("confidence = %v, want below 1.0 for a fuzzy match", result.Applied[0].Confidence)
	}
}

func TestBatchRefusesFuzzyWhenDisabled(t *testing.T) {
	const content = "\tthis is a reasonably long line of source code here\n"
	target := "\tthis is a reasonably long line of source code herf"

	if _, err := (Batch{}).Apply(content, []Entry{
		{OldString: target, NewString: "\treplaced"},
	}); err == nil {
		t.Error("a near match was accepted with fuzzy disabled")
	}
}
