package tui

import "testing"

// One Update folds every already-published event into one state transition.
// The renderer therefore paints the burst once instead of once per fragment.
func TestUpdateCoalescesQueuedEventsIntoOneFrame(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	for _, chunk := range []string{"one ", "two ", "three"} {
		if !p.publishOK(AssistantDelta{Text: chunk}) {
			t.Fatal("event channel unexpectedly full")
		}
	}

	first := <-p.events
	p.Update(teaEvent{event: first})

	if got := p.state.AssistantText(); got != "one two three" {
		t.Fatalf("assistant text = %q, want the whole burst applied before the frame", got)
	}
	if len(p.events) != 0 {
		t.Fatalf("%d events still queued after the frame, want the burst folded", len(p.events))
	}
}

// Order is preserved across the coalesced burst; coalescing must never reorder
// a tool start before the text that preceded it.
func TestCoalescingPreservesLogicalOrder(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.publishOK(AssistantDelta{Text: "before"})
	p.publishOK(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go"})
	p.publishOK(ToolFinished{ID: "c1", Text: "body", Summary: "1 line"})

	p.Update(teaEvent{event: <-p.events})

	if len(p.state.Entries) != 2 {
		t.Fatalf("entries = %+v, want assistant then tool", p.state.Entries)
	}
	if p.state.Entries[0].Kind != EntryAssistant || p.state.Entries[1].Kind != EntryTool {
		t.Fatalf("order = %v,%v; want assistant,tool", p.state.Entries[0].Kind, p.state.Entries[1].Kind)
	}
	if p.state.Entries[1].Tool.Status != ToolDone {
		t.Fatalf("tool status = %s, want done", p.state.Entries[1].Tool.Status)
	}
}
