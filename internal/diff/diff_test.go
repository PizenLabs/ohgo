package diff

import (
	"strings"
	"testing"
)

func TestMyersReportsCommonRemovedAdded(t *testing.T) {
	oldTokens := []string{"a", "b", "c"}
	newTokens := []string{"a", "X", "c"}
	oldIDs, newIDs := Intern(oldTokens, newTokens)

	runs := Myers(oldIDs, newIDs)
	if len(runs) != 4 {
		t.Fatalf("runs = %d, want 4 (common, removed, added, common)", len(runs))
	}
	want := []Run{
		{Count: 1},
		{Count: 1, Removed: true},
		{Count: 1, Added: true},
		{Count: 1},
	}
	for i, w := range want {
		if runs[i] != w {
			t.Errorf("run %d = %+v, want %+v", i, runs[i], w)
		}
	}
}

func TestMyersMergesAdjacentEditsOfTheSameKind(t *testing.T) {
	// Three consecutive insertions must be one run of count 3, not three runs
	// of count 1: that is what keeps the run structure minimal.
	// Two insertions must be one run, not two.
	oldTokens := []string{"a", "d"}
	newTokens := []string{"a", "b", "c", "d"}
	oldIDs, newIDs := Intern(oldTokens, newTokens)

	runs := Myers(oldIDs, newIDs)
	var added int
	for _, run := range runs {
		if run.Added {
			added++
			if run.Count != 2 {
				t.Errorf("added run count = %d, want 2 merged", run.Count)
			}
		}
	}
	if added != 1 {
		t.Errorf("added runs = %d, want exactly 1", added)
	}
}

func TestMyersHandlesEmptyInputs(t *testing.T) {
	// An empty side is a single pure insert or delete, not an error or a
	// degenerate run list.
	if got := Myers(nil, nil); len(got) != 0 {
		t.Errorf("both empty = %+v, want no runs", got)
	}

	runs := Myers([]uint32{1, 2}, nil)
	if len(runs) != 1 || !runs[0].Removed || runs[0].Count != 2 {
		t.Errorf("removing everything = %+v, want one removed run of 2", runs)
	}

	runs = Myers(nil, []uint32{1, 2})
	if len(runs) != 1 || !runs[0].Added || runs[0].Count != 2 {
		t.Errorf("adding everything = %+v, want one added run of 2", runs)
	}
}

func TestMyersIdenticalInputsHaveNoEdits(t *testing.T) {
	oldTokens := []string{"a", "b", "c"}
	newTokens := []string{"a", "b", "c"}
	oldIDs, newIDs := Intern(oldTokens, newTokens)

	runs := Myers(oldIDs, newIDs)
	if len(runs) != 1 || runs[0].Added || runs[0].Removed {
		t.Errorf("identical inputs = %+v, want one common run", runs)
	}
	if runs[0].Count != 3 {
		t.Errorf("common run count = %d, want 3", runs[0].Count)
	}
}

func TestMyersRunsReconstructBothTexts(t *testing.T) {
	// The strongest invariant: walking the runs and concatenating from the
	// appropriate side must reproduce each input exactly. A wrong tie-break
	// still produces a valid diff, so this is the property that actually
	// catches a broken one.
	cases := []struct{ old, new string }{
		{"", "a\nb\n"},
		{"a\nb\n", ""},
		{"a\nb\nc\n", "a\nx\nc\n"},
		{"1\n2\n3\n4\n5\n", "0\n1\n2\n3\n4\n5\n6\n"},
		{"same\nsame\nsame\n", "same\nsame\nsame\n"},
		{"x\ny\n", "y\nx\n"},
		{strings.Repeat("l\n", 50), strings.Repeat("l\n", 25) + "m\n" + strings.Repeat("l\n", 25)},
	}
	for _, tc := range cases {
		t.Run(tc.old+" -> "+tc.new, func(t *testing.T) {
			oldTokens := LineTokens(tc.old)
			newTokens := LineTokens(tc.new)
			oldIDs, newIDs := Intern(oldTokens, newTokens)
			runs := Myers(oldIDs, newIDs)

			var oldOut, newOut strings.Builder
			oldPos, newPos := 0, 0
			for _, run := range runs {
				for i := 0; i < run.Count; i++ {
					switch {
					case run.Removed:
						oldOut.WriteString(oldTokens[oldPos])
						oldPos++
					case run.Added:
						newOut.WriteString(newTokens[newPos])
						newPos++
					default:
						oldOut.WriteString(oldTokens[oldPos])
						newOut.WriteString(newTokens[newPos])
						oldPos++
						newPos++
					}
				}
			}
			if oldOut.String() != tc.old {
				t.Errorf("runs reconstruct old as %q, want %q", oldOut.String(), tc.old)
			}
			if newOut.String() != tc.new {
				t.Errorf("runs reconstruct new as %q, want %q", newOut.String(), tc.new)
			}
			if oldPos != len(oldTokens) || newPos != len(newTokens) {
				t.Errorf("consumed %d/%d old and %d/%d new tokens",
					oldPos, len(oldTokens), newPos, len(newTokens))
			}
		})
	}
}

func TestInternAssignsDenseIDs(t *testing.T) {
	// Interning is what makes the comparison O(1) per token, so equal tokens
	// must share an id and the ids must be dense.
	oldIDs, newIDs := Intern([]string{"a", "b"}, []string{"b", "a", "c"})
	if oldIDs[0] != newIDs[1] {
		t.Error("equal tokens were assigned different ids")
	}
	if oldIDs[1] != newIDs[0] {
		t.Error("equal tokens were assigned different ids")
	}

	seen := map[uint32]bool{}
	for _, id := range append(append([]uint32{}, oldIDs...), newIDs...) {
		seen[id] = true
	}
	if len(seen) > 3 {
		t.Errorf("ids are not dense: %v", seen)
	}
}

func TestDiffLinesReportsChanges(t *testing.T) {
	changes := DiffLines("a\nb\n", "a\nX\n")
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3", len(changes))
	}
	if changes[0].Added || changes[0].Removed {
		t.Error("the first change should be common")
	}
	if !changes[1].Removed || changes[1].Value != "b\n" {
		t.Errorf("change 1 = %+v, want b removed", changes[1])
	}
	if !changes[2].Added || changes[2].Value != "X\n" {
		t.Errorf("change 2 = %+v, want X added", changes[2])
	}
}

func TestLineTokensRoundTrips(t *testing.T) {
	// Joining the tokens must reproduce the input, including a final line that
	// lacks a newline.
	for _, text := range []string{"", "a\n", "a\nb", "a\nb\n", "a\n\nb\n", "\n"} {
		got := strings.Join(LineTokens(text), "")
		if got != text {
			t.Errorf("LineTokens(%q) joined = %q", text, got)
		}
	}
}

func TestLineTokensHasNoPhantomTrailingLine(t *testing.T) {
	// A text ending in a newline must not produce an empty final token, which
	// would become a phantom context line and inflate the hunk header.
	if got := len(LineTokens("a\nb\n")); got != 2 {
		t.Errorf("tokens = %d, want 2", got)
	}
	if got := len(LineTokens("a\nb")); got != 2 {
		t.Errorf("tokens = %d, want 2", got)
	}
}

func TestStructuredHunksGroupNearbyChanges(t *testing.T) {
	// Changes within the context window must merge into one hunk, or a
	// scattered edit produces an unreadable pile of tiny hunks.
	old := "1\n2\n3\n4\n5\n6\n7\n8\n9\n"
	new := "X\n2\n3\n4\n5\n6\n7\n8\nY\n"

	hunks := StructuredHunks(old, new, 3)
	if len(hunks) != 2 {
		t.Errorf("hunks = %d, want 2 (the changes are far apart)", len(hunks))
	}

	// Now bring them close enough to merge.
	closeOld := "1\n2\n3\n4\n5\n"
	closeNew := "X\n2\n3\n4\nY\n"
	if got := len(StructuredHunks(closeOld, closeNew, 3)); got != 1 {
		t.Errorf("hunks = %d, want 1 merged", got)
	}
}

func TestHunkHeaderMatchesGit(t *testing.T) {
	// The header must be byte-compatible with a real unified diff, because it is
	// shown to users who read git output all day.
	hunks := StructuredHunks("a\nb\nc\n", "a\nB\nc\n", 0)
	if len(hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(hunks))
	}
	if got := hunks[0].Header(); got != "@@ -1,3 +1,3 @@" {
		t.Errorf("header = %q, want @@ -1,3 +1,3 @@", got)
	}

	// A single-line side omits its count, as unified diff does.
	empty := StructuredHunks("", "a\n", 0)
	if got := empty[0].Header(); got != "@@ -0,0 +1 @@" {
		t.Errorf("header = %q, want @@ -0,0 +1 @@", got)
	}
}

func TestHunkLinesCarryTheRightKinds(t *testing.T) {
	// A context window of 1 around a changed middle line gives one context
	// line either side: context, removed, added, context.
	hunks := StructuredHunks("a\nb\nc\n", "a\nB\nc\n", 1)
	var kinds []HunkLineKind
	var texts []string
	for _, line := range hunks[0].Lines {
		kinds = append(kinds, line.Kind)
		texts = append(texts, line.Text)
	}
	wantKinds := []HunkLineKind{KindContext, KindRemoved, KindAdded, KindContext}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("rows = %v (%d), want %v", kinds, len(kinds), wantKinds)
	}
	for i, want := range wantKinds {
		if kinds[i] != want {
			t.Errorf("row %d kind = %q, want %q", i, kinds[i], want)
		}
	}
	if texts[1] != "b" || texts[2] != "B" {
		t.Errorf("changed rows = %q/%q, want b/B", texts[1], texts[2])
	}
}

func TestRenderProducesAUnifiedDiff(t *testing.T) {
	out := Unified("one\ntwo\n", "one\nTWO\n", 3)
	want := "@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"
	if out != want {
		t.Errorf("diff =\n%q\nwant\n%q", out, want)
	}
}

func TestIdenticalTextsProduceNoHunks(t *testing.T) {
	// An empty diff must be genuinely empty, not a header with no rows.
	if got := StructuredHunks("a\nb\n", "a\nb\n", 3); len(got) != 0 {
		t.Errorf("hunks = %+v, want none", got)
	}
	if got := Unified("a\nb\n", "a\nb\n", 3); got != "" {
		t.Errorf("rendered = %q, want empty", got)
	}
}

func TestNoNewlineMarkerOnlyWhenMissing(t *testing.T) {
	// The marker is git's way of saying the file lacks a final newline. Emitting
	// it for a file that has one makes every diff lie about the content.
	with := Render(StructuredHunks("a\nb\n", "a\nB\n", 3))
	if strings.Contains(with, NoNewlineMarker) {
		t.Errorf("marker emitted for a newline-terminated file:\n%s", with)
	}

	without := Render(StructuredHunks("a\nb", "a\nB", 3))
	if !strings.Contains(without, NoNewlineMarker) {
		t.Errorf("marker missing for a file without a trailing newline:\n%s", without)
	}
}

func TestZeroContextUsesTheDefault(t *testing.T) {
	// A caller passing 0 must not get a hunk with no context; git's default of
	// three is what makes a diff readable.
	hunks := StructuredHunks(strings.Repeat("x\n", 10), "X\n"+strings.Repeat("x\n", 9), 0)
	contexts := 0
	for _, line := range hunks[0].Lines {
		if line.Kind == KindContext {
			contexts++
		}
	}
	if contexts != DefaultContextLines {
		t.Errorf("context rows = %d, want %d", contexts, DefaultContextLines)
	}
}

func TestHunkLineNumbersPointAtTheRightLines(t *testing.T) {
	// A hunk header that points at the wrong line sends a reader to the wrong
	// place, which is worse than no line number at all.
	lines := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	old := strings.Join(lines, "\n") + "\n"
	new := strings.Join([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "TEN"}, "\n") + "\n"

	hunks := StructuredHunks(old, new, 3)
	if len(hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(hunks))
	}
	// The change is on the last line, so three lines of context either side
	// covers lines 7 through 10.
	if hunks[0].OldStart != 7 || hunks[0].NewStart != 7 {
		t.Errorf("starts = %d/%d, want 7/7", hunks[0].OldStart, hunks[0].NewStart)
	}
	if hunks[0].OldLines != 4 || hunks[0].NewLines != 4 {
		t.Errorf("lines = %d/%d, want 4/4", hunks[0].OldLines, hunks[0].NewLines)
	}
}

func TestBuildChangesTakesCommonTextFromTheNewSide(t *testing.T) {
	// Common runs read from the new tokens: the two texts agree there, and only
	// the new one carries the version that was kept.
	runs := []Run{{Count: 1}}
	changes := BuildChanges(runs, []string{"old\n"}, []string{"new\n"})
	if changes[0].Value != "new\n" {
		t.Errorf("value = %q, want the new-side text", changes[0].Value)
	}
}

func TestLargeDiffDoesNotBlowUp(t *testing.T) {
	// Myers is O(ND); a pathological pair must still finish rather than consume
	// unbounded memory.
	old := strings.Repeat("a\n", 2000)
	new := strings.Repeat("b\n", 2000)
	changes := DiffLines(old, new)
	if len(changes) == 0 {
		t.Fatal("no changes reported for completely different inputs")
	}
}

func BenchmarkDiffLines(b *testing.B) {
	old := strings.Repeat("line\n", 500)
	new := strings.Repeat("line\n", 250) + "inserted\n" + strings.Repeat("line\n", 250)
	for i := 0; i < b.N; i++ {
		DiffLines(old, new)
	}
}

func BenchmarkUnified(b *testing.B) {
	old := strings.Repeat("line\n", 500)
	new := strings.Repeat("line\n", 250) + "inserted\n" + strings.Repeat("line\n", 250)
	for i := 0; i < b.N; i++ {
		Unified(old, new, 3)
	}
}

func TestHeaderOmitsGitFunctionContext(t *testing.T) {
	// git appends the nearest preceding function-like line after the "@@" when
	// it can infer one. The original's jsdiff-compatible hunks do not, and
	// neither does this port: a caller that wants git's exact header can add it,
	// but a surprising suffix would break any consumer that parses the header.
	old := strings.Repeat("x\n", 20)
	new := strings.Repeat("x\n", 10) + "MOD\n" + strings.Repeat("x\n", 10)
	hunks := StructuredHunks(old, new, 3)
	for _, h := range hunks {
		// The header is exactly "@@ -a,b +c,d @@" with nothing after the
		// closing marker.
		if !strings.HasSuffix(h.Header(), "@@") {
			t.Errorf("header %q carries trailing content beyond the ranges", h.Header())
		}
		if got := strings.Count(h.Header(), "@@"); got != 2 {
			t.Errorf("header %q has %d @@ markers, want 2", h.Header(), got)
		}
	}
}
