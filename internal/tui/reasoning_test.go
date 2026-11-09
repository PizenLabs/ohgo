package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Reasoning is a first-class block, collapsed by default, and its duration is
// measured from runtime timestamps rather than invented at paint time.
func TestReasoningBlockIsCollapsedAndMeasured(t *testing.T) {
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(UserSubmitted{Text: "go", At: start, Seq: 1})
	s.Apply(ReasoningDelta{Text: "inspect the cache", At: start, Seq: 1})

	if len(s.Entries) != 2 || s.Entries[1].Kind != EntryReasoning {
		t.Fatalf("entries = %+v, want a reasoning block", s.Entries)
	}
	if s.Entries[1].Reasoning.Expanded {
		t.Fatal("reasoning expanded by default; it must not displace the answer")
	}
	if strings.Contains(s.AssistantText(), "inspect the cache") {
		t.Fatal("reasoning leaked into assistant text")
	}

	s.Apply(AssistantDelta{Text: "the answer", At: start.Add(4800 * time.Millisecond), Seq: 1})
	if ended := s.Entries[1].Reasoning.EndedAt; !ended.Equal(start.Add(4800 * time.Millisecond)) {
		t.Fatalf("reasoning ended at %v, want the runtime timestamp", ended)
	}
	rendered := Render(s)
	if !strings.Contains(rendered, "+ Thought · 4.8s") {
		t.Fatalf("collapsed render = %q, want a measured collapsed header", rendered)
	}
	if strings.Contains(rendered, "inspect the cache") {
		t.Fatalf("collapsed render leaked the trace:\n%s", rendered)
	}

	id := s.Entries[1].ID
	if !s.ToggleBlock(id) {
		t.Fatal("reasoning block was not expandable")
	}
	expanded := Render(s)
	if !strings.Contains(expanded, "- Thought") || !strings.Contains(expanded, "inspect the cache") {
		t.Fatalf("expanded render = %q, want the trace body", expanded)
	}
	if !strings.Contains(expanded, "│ inspect the cache") {
		t.Fatalf("expanded body is not indented under its badge:\n%s", expanded)
	}
}

// Expanded thinking is desaturated, not assistant prose: the body uses the
// palette's muted role so internal reasoning cannot read as a second answer.
func TestExpandedReasoningBodyUsesMutedRole(t *testing.T) {
	pal := forcedPalette()
	reasoning := &ReasoningEntry{Text: "inspect the cache", Closed: true, Expanded: true}
	var b strings.Builder
	renderReasoning(&b, reasoning, pal, Layout{ContentWidth: 80, Mode: LayoutWide}, 0)

	out := b.String()
	if !strings.Contains(out, pal.Separator.Render("│ ")+pal.Muted.Render("inspect the cache")) {
		t.Fatalf("expanded body is not the indented muted role behind the guide: %q", out)
	}
	if !strings.Contains(out, pal.Thought.Render("- Thought")) {
		t.Fatalf("expanded header lost its open marker or amber role: %q", out)
	}
}

// A completed thought collapses to one amber telemetry row with a precise
// duration: sub-second traces read as milliseconds rather than a rounded zero.
func TestCollapsedThoughtHeaderUsesAmberAndPreciseDuration(t *testing.T) {
	pal := forcedPalette()
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	var b strings.Builder
	renderReasoning(&b, &ReasoningEntry{
		Text:      "prepare a concise reply",
		Closed:    true,
		StartedAt: start,
		EndedAt:   start.Add(563 * time.Millisecond),
	}, pal, Layout{ContentWidth: 80, Mode: LayoutWide}, 0)

	out := b.String()
	if !strings.Contains(out, pal.Thought.Render("+ Thought · 563ms")) {
		t.Fatalf("collapsed header = %q, want the amber measured badge", out)
	}
	if strings.Contains(out, "prepare a concise reply") {
		t.Fatalf("collapsed thought leaked its body: %q", out)
	}
}

// Ctrl+O targets the latest expandable block: tools and reasoning share the
// one presentation-only chord instead of inventing a second interaction.
func TestCtrlOTogglesLatestReasoningBlock(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state.Apply(UserSubmitted{Text: "go"})
	p.state.Apply(ReasoningDelta{Text: "think", At: time.Now()})

	if _, cmd := p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO}); cmd != nil {
		t.Fatalf("reasoning expansion scheduled a command %v, want presentation-only", cmd)
	}
	if len(p.state.Entries) != 2 || !p.state.Entries[1].Reasoning.Expanded {
		t.Fatalf("entries = %+v, want the reasoning block expanded", p.state.Entries)
	}
}

// Expanding a thought must reflow the retained viewport in the same key frame.
// Otherwise the next paint would use stale row geometry and the live edge would
// visibly jump.
func TestReasoningExpansionReflowsViewportInSameFrame(t *testing.T) {
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 90, Height: 20})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(ReasoningDelta{Text: strings.Repeat("inspect the cache carefully ", 8), At: at, Seq: 1})
	s.Apply(AssistantDelta{Text: "done", At: at.Add(1200 * time.Millisecond), Seq: 1})
	for i := 0; i < 20; i++ {
		s.Apply(Notice{Text: fmt.Sprintf("notice-%02d", i)})
	}

	p := NewProgram(nil, "test", 0)
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 90, Height: 20})
	p.syncTranscript()
	before := p.transcriptFrame.vp
	if !before.Following {
		t.Fatalf("fixture did not start at the live edge: %+v", before)
	}

	p.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	after := p.transcriptFrame.vp
	if after.TotalRows <= before.TotalRows {
		t.Fatalf("thought expansion did not grow the document: %d -> %d", before.TotalRows, after.TotalRows)
	}
	if after.Offset != after.TotalRows-after.Height {
		t.Fatalf("viewport offset = %d, want the same-frame live edge %d", after.Offset, after.TotalRows-after.Height)
	}
	if after.Offset <= before.Offset {
		t.Fatalf("viewport did not follow the reflowed content: offset %d -> %d", before.Offset, after.Offset)
	}
}

// A live transcript and a resumed one must converge on the same block sequence
// for the same canonical turn: text, reasoning, tool, and the text after it.
func TestLiveAndHistoricalTranscriptsUseTheSameBlocks(t *testing.T) {
	call := ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.go"}}
	at := time.Now()

	var live State
	live.Apply(UserSubmitted{Text: "inspect", At: at})
	live.Apply(ReasoningDelta{Text: "plan", At: at})
	live.Apply(AssistantDelta{Text: "before", At: at})
	live.Apply(ToolStarted{ID: call.ID, Name: call.Name, Summary: "read a.go", At: at})
	live.Apply(ToolFinished{ID: call.ID, Text: "package a", Summary: "1 line", At: at})
	live.Apply(AssistantDelta{Text: "after", At: at})
	live.Apply(AssistantDone{At: at})

	history := []agent.Message{
		agent.UserMessage{Text: "inspect"},
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.ThinkingContent{Thinking: "plan"},
			ai.TextContent{Text: "before"},
			call,
			ai.TextContent{Text: "after"},
		}}},
		agent.ToolResultMessage{
			ToolCallID: call.ID, ToolName: call.Name,
			Content: []ai.ContentBlock{ai.TextContent{Text: "package a"}},
		},
	}
	historical := EntriesFromMessages(history)
	replay := New()
	replay.Apply(HistoryReplaced{Entries: historical})

	if got, want := kindSequence(live.Entries), kindSequence(replay.Entries); !strings.EqualFold(got, want) {
		t.Fatalf("live blocks %q, historical %q: the two views diverged", got, want)
	}
	if !strings.Contains(Render(replay), "before") || !strings.Contains(Render(replay), "after") {
		t.Fatalf("historical render lost text around the call:\n%s", Render(replay))
	}
	if !strings.Contains(Render(replay), "+ Thought") {
		t.Fatalf("historical render lost the reasoning block:\n%s", Render(replay))
	}
}

// Legacy persisted assistant text that still contains provider protocol is
// repaired at the history boundary. The viewport itself never learns the
// syntax, and the raw envelope never reaches the transcript.
func TestHistoricalProviderProtocolIsCanonicalized(t *testing.T) {
	history := []agent.Message{
		agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "<dotsfunctioncall><invoke name=\"ls\"><parameter name=\"path\">cmd</parameter></invoke></dotsfunctioncall>"},
		}}},
	}
	entries := EntriesFromMessages(history)
	if len(entries) != 1 || entries[0].Kind != EntryTool || entries[0].Tool == nil {
		t.Fatalf("entries = %+v, want one canonical tool block", entries)
	}
	if entries[0].Tool.Name != "ls" {
		t.Fatalf("tool = %+v, want ls", entries[0].Tool)
	}
	rendered := Render(State{Entries: entries, Theme: ThemeDark})
	for _, marker := range []string{"<dotsfunctioncall>", "</dotsfunctioncall>", "<invoke", "<parameter"} {
		if strings.Contains(rendered, marker) {
			t.Fatalf("historical render leaked %q:\n%s", marker, rendered)
		}
	}
	if strings.Contains(rendered, "<dotsfunctioncall>") {
		t.Fatalf("protocol reached the historical transcript:\n%s", rendered)
	}
}

func kindSequence(entries []Entry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, string(entry.Kind))
	}
	return strings.Join(parts, ",")
}
