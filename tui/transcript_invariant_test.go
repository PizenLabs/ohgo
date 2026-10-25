package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// assertLayoutMatchesDocument pins the retained projection to a fresh render
// of the canonical transcript. A retained index is only an optimisation; it
// must never disagree with the document it indexes.
func assertLayoutMatchesDocument(t *testing.T, s State, retained *TranscriptLayout) {
	t.Helper()
	fresh := layoutFor(s)
	if got, want := retained.TotalRows(), fresh.TotalRows(); got != want {
		t.Fatalf("retained rows = %d, canonical rows = %d (entries=%d)", got, want, len(s.Entries))
	}
	if got, want := len(retained.Entries()), len(s.Entries); got != want {
		t.Fatalf("retained blocks = %d, canonical blocks = %d", got, want)
	}
	got, want := retained.AllRows(), fresh.AllRows()
	if len(got) != len(want) {
		t.Fatalf("retained row slice = %d, canonical = %d", len(got), len(want))
	}
	for i := range want {
		if got[i].BlockID != want[i].BlockID || got[i].Plain() != want[i].Plain() {
			t.Fatalf("row %d = (%s %q), canonical = (%s %q)",
				i, got[i].BlockID, got[i].Plain(), want[i].BlockID, want[i].Plain())
		}
	}
}

func rowsContainText(rows []LayoutRow, want string) bool {
	for _, row := range rows {
		if strings.Contains(row.Plain(), want) {
			return true
		}
	}
	return false
}

func countRowsContaining(rows []LayoutRow, want string) int {
	n := 0
	for _, row := range rows {
		if strings.Contains(row.Plain(), want) {
			n++
		}
	}
	return n
}

// Appending A, B, C then D, E must extend the projection, never replace it.
// The tail crosses the row index's growth boundaries so a truncated index is
// caught rather than hidden by a small fixture.
func TestAppendPreservesCommittedEvents(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	retained := &TranscriptLayout{}
	retained.Refresh(&s, ComputeLayout(s))

	appendNotice := func(text string) {
		s.Apply(Notice{Text: text})
		retained.Refresh(&s, ComputeLayout(s))
		assertLayoutMatchesDocument(t, s, retained)
	}

	for _, text := range []string{"A", "B", "C"} {
		appendNotice(text)
	}
	for _, text := range []string{"D", "E"} {
		appendNotice(text)
	}

	canonical := make([]string, len(s.Entries))
	for i, entry := range s.Entries {
		canonical[i] = entry.Notice
	}
	if want := []string{"A", "B", "C", "D", "E"}; !slices.Equal(canonical, want) {
		t.Fatalf("canonical transcript = %v, want %v", canonical, want)
	}
	rows := retained.AllRows()
	for _, want := range canonical {
		if !rowsContainText(rows, want) {
			t.Fatalf("committed event %q missing from the projection", want)
		}
	}

	// Grow past the index's internal capacity several times. Every earlier
	// event must remain reachable in the same order.
	for i := 0; i < 40; i++ {
		appendNotice(fmt.Sprintf("tail-%02d", i))
	}
	rows = retained.AllRows()
	for _, want := range []string{"A", "B", "C", "D", "E"} {
		if !rowsContainText(rows, want) {
			t.Fatalf("event %q disappeared after growth", want)
		}
	}
}

// The smallest underlying abstraction: incremental appends must keep every
// prefix sum and row lookup identical to a full rebuild.
func TestFenwickAppendPreservesPrefixSums(t *testing.T) {
	for start := 0; start <= 20; start++ {
		heights := make([]int, start)
		for i := range heights {
			heights[i] = (i * 7) % 5
		}
		f := newFenwick(start)
		f.reset(heights)

		for i := start; i < 50; i++ {
			h := (i * 7) % 5
			heights = append(heights, h)
			f.append(h)

			total := 0
			for _, x := range heights {
				total += x
			}
			if got := f.total(); got != total {
				t.Fatalf("start=%d n=%d total=%d, want %d", start, len(heights), got, total)
			}
			prefix := 0
			for j, x := range heights {
				if got := f.prefix(j); got != prefix {
					t.Fatalf("start=%d n=%d prefix(%d)=%d, want %d", start, len(heights), j, got, prefix)
				}
				for local := 0; local < x; local++ {
					block, row := f.find(prefix + local)
					if block != j || row != local {
						t.Fatalf("start=%d n=%d find(%d)=(%d,%d), want (%d,%d)",
							start, len(heights), prefix+local, block, row, j, local)
					}
				}
				prefix += x
			}
		}
	}
}

// A transcript taller than the window must keep its oldest rows reachable.
// Following shows the live edge, but scrolling to the top must still project
// the first event rather than a clamped or truncated window.
func TestViewportOverflowKeepsOldestEventsReachable(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 90, Height: 24})
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("grep-%d", i)
		s.Apply(ToolStarted{ID: id, Name: "grep", Summary: fmt.Sprintf("grep query-%02d", i)})
		s.Apply(ToolFinished{ID: id, Summary: "8 lines", Text: "match"})
	}
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	p.syncTranscript()
	assertLayoutMatchesDocument(t, p.state, p.transcriptIndex)

	p.state.Apply(ScrollMoved{Delta: 100000})
	p.syncTranscript()
	vp := p.transcriptFrame.vp
	if vp.Offset != 0 {
		t.Fatalf("max scroll offset = %d, want 0", vp.Offset)
	}
	rows := p.transcriptFrame.index.Rows(vp.Offset, vp.Height)
	if !rowsContainText(rows, "grep query-00") {
		t.Fatalf("oldest event is not visible at the top: %v", rows)
	}
}

// Streaming and tool lifecycle events append or update in place. Neither may
// drop a block that was already committed, even when the index grows.
func TestStreamingToolEventsNeverRemoveCommittedRows(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	retained := &TranscriptLayout{}
	retained.Refresh(&s, ComputeLayout(s))

	commit := func(event Event) {
		s.Apply(event)
		retained.Refresh(&s, ComputeLayout(s))
		assertLayoutMatchesDocument(t, s, retained)
	}

	commit(UserSubmitted{Text: "PROMPT-MARKER change the author"})
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("stream-%02d", i)
		commit(AssistantDelta{Text: fmt.Sprintf("chunk-%02d ", i)})
		commit(ToolStarted{ID: id, Name: "grep", Summary: fmt.Sprintf("grep marker-%02d", i)})
		commit(ToolPreview{ID: id, Summary: fmt.Sprintf("grep marker-%02d (%d files)", i, i)})
		commit(ToolFinished{ID: id, Summary: "3 lines", Text: "one\ntwo\nthree"})
	}
	commit(AssistantDone{})
	commit(AssistantDelta{Text: "FINAL-MARKER done"})
	commit(AssistantDone{})

	rows := retained.AllRows()
	for _, want := range []string{"PROMPT-MARKER", "chunk-00", "grep marker-00", "grep marker-19", "FINAL-MARKER"} {
		if !rowsContainText(rows, want) {
			t.Fatalf("committed text %q is missing after streaming", want)
		}
	}
}

// A resize changes geometry only. The canonical blocks, their identities, and
// their order must survive any number of reflows and later appends.
func TestResizePreservesCanonicalTranscript(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	retained := &TranscriptLayout{}
	retained.Refresh(&s, ComputeLayout(s))

	appendTool := func(i int) {
		id := fmt.Sprintf("resize-%02d", i)
		s.Apply(ToolStarted{ID: id, Name: "read", Summary: fmt.Sprintf("read file-%02d.go", i)})
		s.Apply(ToolFinished{ID: id, Summary: "12 lines", Text: "body"})
		retained.Refresh(&s, ComputeLayout(s))
	}
	for i := 0; i < 12; i++ {
		appendTool(i)
	}
	assertLayoutMatchesDocument(t, s, retained)
	before := entryFingerprint(s.Entries)

	s.Apply(TerminalResized{Width: 60, Height: 18})
	retained.Refresh(&s, ComputeLayout(s))
	assertLayoutMatchesDocument(t, s, retained)
	if !slices.Equal(before, entryFingerprint(s.Entries)) {
		t.Fatal("resize changed the canonical transcript")
	}

	for i := 12; i < 24; i++ {
		appendTool(i)
	}
	assertLayoutMatchesDocument(t, s, retained)

	s.Apply(TerminalResized{Width: 120, Height: 40})
	retained.Refresh(&s, ComputeLayout(s))
	assertLayoutMatchesDocument(t, s, retained)
	if !slices.Equal(before[:], entryFingerprint(s.Entries)[:len(before)]) {
		t.Fatal("resize after appends changed earlier canonical blocks")
	}
	rows := retained.AllRows()
	for i := 0; i < 24; i++ {
		if !rowsContainText(rows, fmt.Sprintf("read file-%02d.go", i)) {
			t.Fatalf("tool %d lost after resize", i)
		}
	}
}

// New output while the user is detached must not reset their scroll position
// or silently replace the rows under their reading anchor. Returning to the
// bottom restores ordinary follow behaviour.
func TestDetachedScrollSurvivesAppendsAndReattaches(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 90, Height: 24})
	for i := 0; i < 20; i++ {
		s.Apply(Notice{Text: fmt.Sprintf("history-%02d", i)})
	}
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	p.syncTranscript()

	p.state.Apply(ScrollMoved{Delta: 5})
	p.syncTranscript()
	if p.state.Follow {
		t.Fatal("fixture did not detach")
	}
	topBefore := viewportTopRow(t, p.transcriptFrame)
	offsetBefore := p.transcriptFrame.vp.Offset

	for i := 0; i < 20; i++ {
		p.state.Apply(Notice{Text: fmt.Sprintf("late-%02d", i)})
		p.syncTranscript()
		if p.state.Follow {
			t.Fatalf("append %d forced the viewport back to the bottom", i)
		}
		topAfter := viewportTopRow(t, p.transcriptFrame)
		if topAfter.BlockID != topBefore.BlockID || topAfter.Row != topBefore.Row {
			t.Fatalf("append %d moved the reading anchor: (%s,%d) -> (%s,%d)",
				i, topBefore.BlockID, topBefore.Row, topAfter.BlockID, topAfter.Row)
		}
		if got := p.transcriptFrame.vp.Offset; got != offsetBefore {
			t.Fatalf("append %d moved the offset: %d -> %d", i, offsetBefore, got)
		}
	}

	p.state.Apply(ScrollToBottom{})
	p.syncTranscript()
	if !p.transcriptFrame.vp.Following {
		t.Fatal("returning to the bottom did not resume follow")
	}
	if got, want := p.transcriptFrame.vp.Offset, p.transcriptFrame.vp.MaxOffset; got != want {
		t.Fatalf("reattached offset = %d, want bottom %d", got, want)
	}
	rows := p.transcriptFrame.index.Rows(p.transcriptFrame.vp.Offset, p.transcriptFrame.vp.Height)
	if !rowsContainText(rows, "late-19") {
		t.Fatal("newest event is not visible after returning to the bottom")
	}
}

// The reported workload: a prompt, a web-serving shell, then repeated
// read/edit/grep tool calls and a final answer. Every completed block must
// remain present in the logical transcript and in the projection.
func TestToolHeavyWorkloadKeepsEveryCompletedEvent(t *testing.T) {
	s := New()
	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 28})

	p.state.Apply(UserSubmitted{Text: "change author from johny smith to 'Jong Adam'"})
	p.syncTranscript()

	// The reproduction's shell escapes, modelled as completed shell blocks.
	for i, command := range []string{"ls", "npx live-server"} {
		p.state.Apply(ShellStarted{Command: command, Seq: i + 1})
		p.state.Apply(ShellDelta{Seq: i + 1, Text: "output for " + command})
		p.state.Apply(ShellFinished{Seq: i + 1, ExitCode: 0})
	}

	type call struct{ name, invocation, result string }
	calls := []call{
		{"read", "read author.go", "42 lines"},
		{"read", "read README.md", "18 lines"},
		{"glob", "glob **/*.go", "12 files"},
		{"ls", "ls src", "7 entries"},
		{"grep", "grep jong adam", "8 lines"},
		{"grep", "grep johny smith", "No matches found."},
		{"edit", "edit author.go", "1 file changed"},
		{"read", "read author.go", "42 lines"},
		{"grep", "grep Jong Adam", "2 lines"},
		{"grep", "grep johny", "No matches found."},
		// A verification pass: the agent re-reads and re-greps.
		{"read", "read author_test.go", "30 lines"},
		{"grep", "grep Jong Adam author_test.go", "3 lines"},
		{"edit", "edit author_test.go", "1 file changed"},
		{"read", "read author.go", "42 lines"},
		{"grep", "grep johny smith", "No matches found."},
		{"ls", "ls", "9 entries"},
	}
	for i, c := range calls {
		id := fmt.Sprintf("tool-%02d", i)
		p.state.Apply(ToolStarted{ID: id, Name: c.name, Summary: c.invocation})
		p.syncTranscript()
		p.state.Apply(ToolFinished{ID: id, Summary: c.result, Text: c.result})
		p.syncTranscript()
		assertLayoutMatchesDocument(t, p.state, p.transcriptIndex)
	}
	p.state.Apply(AssistantDelta{Text: "Updated the author to 'Jong Adam' in author.go and author_test.go."})
	p.state.Apply(AssistantDone{})
	p.syncTranscript()
	assertLayoutMatchesDocument(t, p.state, p.transcriptIndex)

	rows := p.transcriptIndex.AllRows()
	for _, want := range []string{"change author from johny smith", "ls", "read author.go", "grep jong adam", "8 lines", "grep johny smith", "No matches found.", "edit author_test.go", "Updated the author"} {
		if !rowsContainText(rows, want) {
			t.Fatalf("completed event %q is missing from the transcript projection", want)
		}
	}
	// No path may duplicate a completed tool call into the projection.
	if got := countRowsContaining(rows, "grep jong adam"); got != 1 {
		t.Fatalf("grep invocation appears %d times, want exactly 1", got)
	}
}

func entryFingerprint(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		part := fmt.Sprintf("%s|%s|%s|%s|%s", entry.ID, entry.Kind, entry.UserText, entry.AssistantText, entry.Notice)
		if entry.Tool != nil {
			part += "|" + entry.Tool.ID + "|" + entry.Tool.Name + "|" + string(entry.Tool.Status) + "|" + entry.Tool.Summary
		}
		if entry.Shell != nil {
			part += "|" + entry.Shell.Command + "|" + string(entry.Shell.Status)
		}
		out[i] = part
	}
	return out
}
