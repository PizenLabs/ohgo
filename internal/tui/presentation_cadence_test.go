package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// completedToolStream builds a transcript of finished calls.
func completedToolStream(summaries ...string) State {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	at := time.Now()
	for i, summary := range summaries {
		id := "c" + string(rune('a'+i))
		s.Apply(ToolStarted{ID: id, Name: "read", Summary: summary, At: at})
		s.Apply(ToolFinished{ID: id, Text: "one line\n", Summary: "1 line", At: at})
	}
	return s
}

// Consecutive completed calls are one compact stream: no blank row between
// them, but the run keeps its separator before whatever follows.
func TestConsecutiveToolsRenderAsOneCompactStream(t *testing.T) {
	s := completedToolStream("ls backend", "ls .", "read backend/go.mod")
	s.Apply(AssistantDelta{Text: "The project is a Go service."})

	out := ansi.Strip(Render(s))
	if strings.Contains(out, "⎿ 1 line\n\n✓") {
		t.Fatalf("consecutive tools kept a blank card boundary:\n%s", out)
	}
	if !strings.Contains(out, "⎿ 1 line\n✓ ls .") {
		t.Fatalf("tool stream is not compact:\n%s", out)
	}
	if !strings.Contains(out, "⎿ 1 line\n\nThe project is a Go service.") {
		t.Fatalf("the end of the action run lost its separator:\n%s", out)
	}
}

// The layout cache must notice that appending another tool removed the
// previous tool's separator, even though Apply never named it dirty.
func TestAppendingToolInvalidatesPreviousGroupBoundary(t *testing.T) {
	s := completedToolStream("ls backend")
	layout := &TranscriptLayout{}
	layout.Refresh(&s, ComputeLayout(s))
	before := len(layout.blocks[0].lines)

	s.Apply(ToolStarted{ID: "c2", Name: "read", Summary: "read go.mod", At: time.Now()})
	s.Apply(ToolFinished{ID: "c2", Text: "module x\n", Summary: "1 line", At: time.Now()})
	layout.Refresh(&s, ComputeLayout(s))
	after := len(layout.blocks[0].lines)
	if after != before-1 {
		t.Fatalf("previous tool rows = %d after append, want %d (separator removed)", after, before-1)
	}
}

// The prompt boundary owns the workspace role. A running tool owns the current
// activity through its own entry, so the activity line must not render a second
// "ASK · Running ..." copy for the same call.
func TestRunningToolOwnsActivityAndPromptOwnsRole(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(WorkspaceUpdated{Workspace: "ASK", WorkspaceAccent: "39", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "analyze this project", At: time.Now()})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "inspecting backend/internal", At: time.Now()})

	activity := ProjectActivity(s)
	if activity.Surface != ActivityEntry || activity.EntryIndex != 1 {
		t.Fatalf("activity = %+v, want the running tool entry to own it", activity)
	}
	if line := ActivityLineText(s, time.Now(), 0, 100); line != "" {
		t.Fatalf("activity line = %q, want no second representation of the running call", line)
	}
	out := ansi.Strip(Render(s))
	if strings.Count(out, "ask > ") != 1 {
		t.Fatalf("prompt role appeared %d times, want exactly one:\n%s", strings.Count(out, "ask > "), out)
	}
	if strings.Contains(out, "✳ Running") {
		t.Fatalf("running call also rendered an activity line:\n%s", out)
	}
	if !strings.Contains(out, "inspecting backend/internal") {
		t.Fatalf("running call lost its action detail:\n%s", out)
	}
}

// A long action still shows both the action's measured duration and the whole
// turn's elapsed time. The formatter contract is tested directly because the
// tool entry, not the line, now presents the call.
func TestActivityLineShowsActionAndTurnElapsed(t *testing.T) {
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	state := ThinkingState{
		Active: true, Phase: ThinkingTool, Label: "Running", Detail: "go test ./...",
		StartedAt: start.Add(60 * time.Second), TurnStartedAt: start, Interruptible: true,
	}

	line := ansi.Strip(RenderThinking(state, start.Add(70*time.Second), 100)[0])
	if !strings.Contains(line, "10s") {
		t.Fatalf("activity line lost the action duration: %q", line)
	}
	if !strings.Contains(line, "turn 1m 10s") {
		t.Fatalf("activity line lost the whole-turn duration: %q", line)
	}
}

// Runtime event rate and render rate are distinct: a burst folds into one
// state transition, and only an actual View produces a frame.
func TestRuntimeBurstFoldsIntoOneStateTransitionAndOneView(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	for i := 0; i < 20; i++ {
		if !p.publishOK(AssistantDelta{Text: "x"}) {
			t.Fatal("event channel unexpectedly full")
		}
	}
	p.Update(teaEvent{event: <-p.events})
	if p.eventsApplied != 20 {
		t.Fatalf("events applied = %d, want the whole 20-event burst", p.eventsApplied)
	}
	if p.viewsRendered != 0 {
		t.Fatalf("views rendered = %d before View, want 0", p.viewsRendered)
	}
	if got := len(p.state.Entries); got != 1 {
		t.Fatalf("entries = %d, want one coalesced assistant block", got)
	}
	p.View()
	if p.viewsRendered != 1 {
		t.Fatalf("views rendered = %d after one View, want 1", p.viewsRendered)
	}
}

// A completed action stream does not move a detached reader's viewport.
func TestDetachedViewportHoldsAnchorWhenToolCompletes(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.state = newViewportState(40, 80, 24)
	p.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	p.syncTranscript()
	// Scroll away from the live edge.
	p.state.Apply(ScrollMoved{Delta: 5})
	p.Update(teaEvent{event: ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: time.Now()}})
	p.syncTranscript()
	if p.transcriptFrame.vp.Following {
		t.Fatal("tool start reattached a detached viewport")
	}
	anchor := p.transcriptFrame.vp.Anchor
	p.Update(teaEvent{event: ToolFinished{ID: "c1", Text: "package x\n", Summary: "1 line", At: time.Now()}})
	p.syncTranscript()
	if p.transcriptFrame.vp.Following {
		t.Fatal("tool completion reattached a detached viewport")
	}
	if p.transcriptFrame.vp.Anchor != anchor {
		t.Fatalf("tool completion moved the reading anchor: %+v -> %+v", anchor, p.transcriptFrame.vp.Anchor)
	}
}

// A running call is still visually distinct from the completed stream.
func TestRunningActionStaysDistinct(t *testing.T) {
	s := completedToolStream("ls backend")
	s.Apply(ToolStarted{ID: "c2", Name: "bash", Summary: "go test ./...", At: time.Now()})
	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "go test ./...") {
		t.Fatalf("running action disappeared from the stream:\n%s", out)
	}
	if !strings.Contains(out, "running") {
		t.Fatalf("running action lost its lifecycle marker:\n%s", out)
	}
}

// Live mode seals blocks one frame at a time, so it expresses the group gap on
// the block after the run rather than rewriting the last tool.
func TestLiveToolStreamUsesOneLeadingGap(t *testing.T) {
	s := completedToolStream("ls backend", "ls .")
	s.Apply(AssistantDelta{Text: "The project is a Go service."})
	pal := PaletteFor(s.Theme)
	layout := ComputeLayout(s)

	tool := liveEntryLines(s, 0, s.Entries[0], pal, layout)
	if len(tool) > 0 && tool[len(tool)-1] == "" {
		t.Fatalf("live tool block wrote its own separator: %#v", tool)
	}
	answer := liveEntryLines(s, len(s.Entries)-1, s.Entries[len(s.Entries)-1], pal, layout)
	if len(answer) == 0 || answer[0] != "" {
		t.Fatalf("live answer did not carry the action-run gap: %#v", answer)
	}
}
