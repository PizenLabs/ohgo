package tui

import (
	"strings"
	"testing"
)

// Identity is allocated once and survives appends around it.
func TestBlockIDsAreStable(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "first"})
	firstID := s.Entries[0].ID
	if firstID == "" {
		t.Fatal("first block has no ID")
	}
	s.Apply(AssistantDelta{Text: "answer"})
	s.Apply(AssistantDone{})
	s.Apply(Notice{Text: "later"})
	if s.Entries[0].ID != firstID {
		t.Fatalf("append changed identity: %s -> %s", firstID, s.Entries[0].ID)
	}
	seen := map[BlockID]bool{}
	for i, entry := range s.Entries {
		if entry.ID == "" {
			t.Fatalf("entry %d has no ID", i)
		}
		if seen[entry.ID] {
			t.Fatalf("duplicate block ID %q", entry.ID)
		}
		seen[entry.ID] = true
	}
}

// Expansion is presentation-only: it must not alter the tool's content,
// status, timing, or any other block.
func TestToggleBlockIsPresentationOnly(t *testing.T) {
	s := New()
	s.Apply(ToolStarted{ID: "a", Name: "read", Summary: "read a"})
	s.Apply(ToolFinished{ID: "a", Text: "out a\nmore", Summary: "done"})
	s.Apply(ToolStarted{ID: "b", Name: "ls", Summary: "list"})
	s.Apply(ToolFinished{ID: "b", Text: "out b", Summary: "done"})
	first := blockIDAt(s.Entries, 0)
	before := *s.tool("a")
	if !s.ToggleBlock(first) {
		t.Fatal("historical block did not toggle")
	}
	if !s.tool("a").Expanded {
		t.Fatal("block did not expand")
	}
	if s.tool("b").Expanded {
		t.Fatal("sibling expanded")
	}
	after := *s.tool("a")
	if after.Text != before.Text || after.Summary != before.Summary || after.Status != before.Status ||
		after.ID != before.ID || !after.StartedAt.Equal(before.StartedAt) || !after.EndedAt.Equal(before.EndedAt) {
		t.Fatalf("toggle changed tool semantics: %+v -> %+v", before, after)
	}
	if !s.ToggleBlock(first) || s.tool("a").Expanded {
		t.Fatal("second toggle did not collapse")
	}
}

// A non-expandable block rejects a toggle instead of fabricating state.
func TestToggleBlockRejectsStaticBlocks(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "hello"})
	if s.ToggleBlock(blockIDAt(s.Entries, 0)) {
		t.Fatal("user block toggled")
	}
	if s.ToggleBlock("missing") {
		t.Fatal("missing block toggled")
	}
}

// Block copy is canonical logical text: styling and cursor controls do not
// survive, ordinary Unicode does.
func TestBlockCopyTextStripsTerminalFormatting(t *testing.T) {
	entry := Entry{
		Kind:          EntryAssistant,
		AssistantText: "\x1b[1;31mred\x1b[0m line\r\nsecond\x1b[2J line",
	}
	got := BlockCopyText(entry)
	if strings.Contains(got, "\x1b") || strings.Contains(got, "\r") {
		t.Fatalf("copy leaked terminal controls: %q", got)
	}
	if !strings.Contains(got, "red line") || !strings.Contains(got, "second line") {
		t.Fatalf("copy lost logical text: %q", got)
	}
}

// Whole-block copy covers the structured kinds, not only prose.
func TestBlockCopyTextStructuredKinds(t *testing.T) {
	tool := Entry{Kind: EntryTool, Tool: &ToolEntry{ID: "t", Text: "\x1b[32mresult\x1b[0m\nsecond"}}
	if got := BlockCopyText(tool); got != "result\nsecond" {
		t.Fatalf("tool copy = %q", got)
	}
	shell := Entry{Kind: EntryShell, Shell: &ShellEntry{Command: "printf hi", Output: "hi\n"}}
	if got := BlockCopyText(shell); got != "printf hi\nhi" {
		t.Fatalf("shell copy = %q", got)
	}
	task := Entry{Kind: EntryTask, Task: &TaskProjection{
		ID:        "x",
		Objective: "ship",
		Items:     []TaskItem{{State: "done", Label: "layout"}},
	}}
	if got := BlockCopyText(task); !strings.Contains(got, "ship") || !strings.Contains(got, "[done] layout") {
		t.Fatalf("task copy = %q", got)
	}
}

// Resume replaces the document and must not leave identities or a selection
// pointing at blocks that no longer exist.
func TestHistoryReplacedReassignsIdentityAndClearsSelection(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "old"})
	oldID := s.Entries[0].ID
	s.Selection = &Selection{Start: Position{BlockID: oldID}, End: Position{BlockID: oldID, Row: 0, Column: 1}}
	s.Apply(ScrollMoved{Delta: 4})
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryUser, UserText: "new"}}})
	if s.Entries[0].ID == oldID {
		t.Fatal("resume reused an old block ID")
	}
	if s.Selection != nil {
		t.Fatal("resume kept a selection over dead blocks")
	}
	if !s.Follow || s.Scroll != 0 || s.scrollIntent != 0 {
		t.Fatalf("resume kept stale viewport: follow=%v scroll=%d intent=%d", s.Follow, s.Scroll, s.scrollIntent)
	}
}
