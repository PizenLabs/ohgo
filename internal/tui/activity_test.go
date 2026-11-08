package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// TestActivityOwnershipHasExactlyOneSurface is the single-meaning invariant:
// every active runtime state resolves to one surface, never two. The table is
// semantic, not string-based: it asks the projection which element owns the
// activity and checks that no other element claims it.
func TestActivityOwnershipHasExactlyOneSurface(t *testing.T) {
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	busy := func() State {
		s := New()
		s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
		return s
	}
	runningTool := func() State {
		s := busy()
		s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: at, Seq: 1})
		return s
	}
	openReasoning := func() State {
		s := busy()
		s.Apply(ReasoningDelta{Text: "plan", At: at, Seq: 1})
		return s
	}

	tests := []struct {
		name    string
		state   State
		surface ActivitySurface
		entry   int
	}{
		{name: "idle", state: New(), surface: ActivityNone, entry: -1},
		{name: "model thinking", state: busy(), surface: ActivityLine, entry: -1},
		{name: "open reasoning", state: openReasoning(), surface: ActivityEntry, entry: 1},
		{name: "running tool", state: runningTool(), surface: ActivityEntry, entry: 1},
		{
			name: "finished tool returns to thinking",
			state: func() State {
				s := runningTool()
				s.Apply(ToolFinished{ID: "c1", Text: "ok", Summary: "1 line", At: at, Seq: 1})
				return s
			}(),
			surface: ActivityLine,
			entry:   -1,
		},
		{
			name:    "streaming answer",
			state:   func() State { s := busy(); s.Apply(AssistantDelta{Text: "answer", At: at, Seq: 1}); return s }(),
			surface: ActivityLine,
			entry:   -1,
		},
		{
			name: "approval dialog owns the screen",
			state: func() State {
				s := runningTool()
				s.Apply(ApprovalAsked{ToolName: "read", Summary: "read a.go", Choices: []string{"Yes", "No"}})
				return s
			}(),
			surface: ActivityNone,
			entry:   -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProjectActivity(tt.state)
			if got.Surface != tt.surface {
				t.Fatalf("surface = %s, want %s (%+v)", got.Surface, tt.surface, got)
			}
			if got.EntryIndex != tt.entry {
				t.Fatalf("entry = %d, want %d", got.EntryIndex, tt.entry)
			}
			line := ActivityLineText(tt.state, at.Add(2*time.Second), 0, 80)
			if tt.surface == ActivityLine && line == "" {
				t.Fatal("the line owns the activity but rendered nothing")
			}
			if tt.surface != ActivityLine && line != "" {
				t.Fatalf("line = %q, but it does not own the activity", line)
			}
		})
	}
}

// TestRunningToolUsesItsEntryNotTheActivityLine covers the observed BUILD
// duplicate: the running tool row and the "✳ Running ..." line must never
// coexist, and the header must not repeat the active phase either.
func TestRunningToolUsesItsEntryNotTheActivityLine(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 30})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "check this project", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "ls", Summary: "inspecting backend/internal", At: time.Now(), Seq: 1})

	out := ansi.Strip(Render(s))
	header := strings.Split(out, "\n")[0]
	if strings.Contains(header, "running tool") {
		t.Fatalf("header repeated the active phase: %q", header)
	}
	if strings.Contains(out, "✳ Running") {
		t.Fatalf("activity line duplicated the running call:\n%s", out)
	}
	if got := strings.Count(out, "inspecting backend/internal"); got != 1 {
		t.Fatalf("running action appeared %d times, want exactly one:\n%s", got, out)
	}
	if got := strings.Count(out, "build › "); got != 1 {
		t.Fatalf("prompt role appeared %d times, want exactly one:\n%s", got, out)
	}
}

// TestOpenReasoningOwnsThinking: while a reasoning trace is growing, its own
// head is the one activity representation; the activity line stays absent.
func TestOpenReasoningOwnsThinking(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ReasoningDelta{Text: "inspect the cache", At: time.Now(), Seq: 1})

	out := ansi.Strip(Render(s))
	if got := strings.Count(out, spinnerFrames[0]+" Thought"); got != 1 {
		t.Fatalf("reasoning head appeared %d times, want one live spinner:\n%s", got, out)
	}
	if strings.Contains(out, "◌ reason ·") {
		t.Fatalf("activity line duplicated the open reasoning trace:\n%s", out)
	}
}

// TestToolFinalizationReplacesLiveRepresentationExactlyOnce: one entry changes
// state in place, never appending a second finalized copy next to a live one.
func TestToolFinalizationReplacesLiveRepresentationExactlyOnce(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: time.Now(), Seq: 1})
	id := s.Entries[1].ID

	running := ansi.Strip(Render(s))
	if !strings.Contains(running, "◌ read a.go") || strings.Contains(running, "⠋") {
		t.Fatalf("running render lost its live representation:\n%s", running)
	}

	finishedAt := time.Now()
	s.Apply(ToolFinished{ID: "c1", Text: "package a\n", Summary: "1 line", At: finishedAt, Seq: 1})
	if len(s.Entries) != 2 {
		t.Fatalf("entries = %d after completion, want the same two blocks", len(s.Entries))
	}
	if s.Entries[1].ID != id {
		t.Fatalf("completion reassigned the block id %q -> %q", id, s.Entries[1].ID)
	}
	if s.Entries[1].Tool.Status != ToolDone {
		t.Fatalf("status = %q, want done", s.Entries[1].Tool.Status)
	}

	done := ansi.Strip(Render(s))
	if strings.Contains(done, "◌ read a.go") || strings.Contains(done, "⠋ read a.go") || strings.Contains(done, "running") {
		t.Fatalf("completed tool kept the live representation:\n%s", done)
	}
	if !strings.Contains(done, "⠋ reason") {
		t.Fatalf("busy turn lost the reasoning spinner:\n%s", done)
	}
	if got := strings.Count(done, "read a.go"); got != 1 {
		t.Fatalf("completed action appeared %d times, want one:\n%s", got, done)
	}
	if got := strings.Count(done, "✓ read a.go"); got != 1 {
		t.Fatalf("stable completion appeared %d times, want one:\n%s", got, done)
	}
}

// TestToolPreviewMutatesTheSameSurface: progress updates change the one entry's
// projection; they never append a block or allocate a new identity.
func TestToolPreviewMutatesTheSameSurface(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "go test ./...", At: time.Now(), Seq: 1})
	id := s.Entries[1].ID
	layout := &TranscriptLayout{}
	layout.Refresh(&s, ComputeLayout(s))

	before := len(layout.blocks)
	for i := 0; i < 5; i++ {
		s.Apply(ToolPreview{ID: "c1", Summary: "progress " + string(rune('a'+i)), Seq: 1})
		layout.Refresh(&s, ComputeLayout(s))
	}
	if got := len(s.Entries); got != 2 {
		t.Fatalf("entries = %d after previews, want 2", got)
	}
	if got := len(layout.blocks); got != before {
		t.Fatalf("layout blocks = %d after previews, want %d", got, before)
	}
	if s.Entries[1].ID != id {
		t.Fatalf("preview reassigned the block id %q -> %q", id, s.Entries[1].ID)
	}
	if got := ProjectActivity(s).Surface; got != ActivityEntry {
		t.Fatalf("surface = %s after previews, want the same entry", got)
	}
}

// TestCancelledToolFinalizesExactlyOnce: cancellation is its own terminal
// state, not a running tool and not a provider failure. Repeated cancels are
// idempotent.
func TestCancelledToolFinalizesExactlyOnce(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Summary: "go test ./...", At: time.Now(), Seq: 1})
	id := s.Entries[1].ID

	s.Apply(TurnCancelled{Seq: 1, Text: "cancelled", At: time.Now()})
	s.Apply(TurnCancelled{Seq: 1, Text: "cancelled", At: time.Now()})

	if len(s.Entries) != 3 { // user, tool, cancellation notice
		t.Fatalf("entries = %d, want user, tool, notice", len(s.Entries))
	}
	tool := s.Entries[1]
	if tool.ID != id || tool.Tool.Status != ToolCancelled {
		t.Fatalf("tool = %+v, want the same block cancelled", tool)
	}
	if s.runningTools != 0 {
		t.Fatalf("runningTools = %d after cancellation", s.runningTools)
	}
	if ProjectThinking(s).Active || ProjectActivity(s).Surface != ActivityNone {
		t.Fatal("cancelled turn still claims an activity")
	}
	out := ansi.Strip(Render(s))
	if strings.Contains(out, "⠋") || strings.Contains(out, "running") {
		t.Fatalf("cancelled view kept a live representation:\n%s", out)
	}
	if got := strings.Count(out, "⊘ go test ./..."); got != 1 {
		t.Fatalf("cancellation marker appeared %d times, want one:\n%s", got, out)
	}
}

// TestFailedToolFinalizesExactlyOnce keeps failure distinct from cancellation
// and proves the running surface is removed on the terminal transition.
func TestFailedToolFinalizesExactlyOnce(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read missing.go", At: time.Now(), Seq: 1})
	id := s.Entries[1].ID

	s.Apply(ToolFinished{ID: "c1", Text: "no such file", Summary: "no such file", IsError: true, At: time.Now(), Seq: 1})
	if s.Entries[1].ID != id || s.Entries[1].Tool.Status != ToolError {
		t.Fatalf("tool = %+v, want the same block failed", s.Entries[1])
	}
	out := ansi.Strip(Render(s))
	if strings.Contains(out, "◌ read missing.go") || strings.Contains(out, "⠋ read missing.go") || strings.Contains(out, "running") {
		t.Fatalf("failed tool kept a live representation:\n%s", out)
	}
	if !strings.Contains(out, "⠋ reason") {
		t.Fatalf("busy turn lost the reasoning spinner:\n%s", out)
	}
	if got := strings.Count(out, "✗ read missing.go"); got != 1 {
		t.Fatalf("failure badge appeared %d times, want one:\n%s", got, out)
	}
}

// TestLivePainterKeepsOneLiveRowAndSealsItOnce: output-first mode repaints the
// running call in the live region, never in history, and seals the finished
// call exactly once when it completes.
func TestLivePainterKeepsOneLiveRowAndSealsItOnce(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 24})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "ls", Summary: "inspecting backend/internal", At: time.Now(), Seq: 1})

	painter := newLivePainter()
	var history []string
	history = append(history, painter.render(s)...)
	for i := 0; i < 4; i++ {
		more := painter.render(s)
		if len(more) != 0 {
			t.Fatalf("live update %d sealed history %q", i, more)
		}
	}
	if got := countStripped(history, "inspecting backend/internal") + countStripped(painter.live, "inspecting backend/internal"); got != 1 {
		t.Fatalf("running action appeared %d times across history+live, want one", got)
	}
	if got := countStripped(painter.live, "◌"); got != 1 {
		t.Fatalf("live region has %d running markers, want one: %q", got, painter.live)
	}

	s.Apply(ToolFinished{ID: "c1", Text: "19 lines\n", Summary: "19 lines", At: time.Now(), Seq: 1})
	history = append(history, painter.render(s)...)
	if got := countStripped(history, "inspecting backend/internal"); got != 1 {
		t.Fatalf("sealed history contains the action %d times, want one: %q", got, history)
	}
	if countStripped(painter.live, "inspecting backend/internal") != 0 {
		t.Fatalf("finished action stayed in the live region: %q", painter.live)
	}
	if countStripped(painter.live, "◌ inspecting") != 0 {
		t.Fatalf("live region still claims the finished call is running: %q", painter.live)
	}
	if again := painter.render(s); len(again) != 0 {
		t.Fatalf("completion was sealed twice: %q", again)
	}
}

// TestResizeDoesNotDuplicateActivity: a width change reflows the one live
// entry; it never promotes it to a second block.
func TestResizeDoesNotDuplicateActivity(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 24})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "ls", Summary: "inspecting backend/internal", At: time.Now(), Seq: 1})

	layout := &TranscriptLayout{}
	for _, width := range []int{96, 60, 120, 96} {
		s.Apply(TerminalResized{Width: width, Height: 24})
		layout.Refresh(&s, ComputeLayout(s))
		if len(layout.blocks) != 2 {
			t.Fatalf("width %d: blocks = %d, want 2", width, len(layout.blocks))
		}
		out := ansi.Strip(Render(s))
		if got := strings.Count(out, "inspecting backend/internal"); got != 1 {
			t.Fatalf("width %d: action appeared %d times, want one:\n%s", width, got, out)
		}
	}
}

// TestResumeContainsFinalizedHistoryOnly: intermediate live states never reach
// the persisted history, so replay cannot resurrect a running surface.
func TestResumeContainsFinalizedHistoryOnly(t *testing.T) {
	call := ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.go"}}
	message := agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{call}}}
	entries := EntriesFromMessages([]agent.Message{
		agent.UserMessage{Text: "go"},
		message,
		agent.ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "package a"}}},
	})
	for _, entry := range entries {
		if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.Status == ToolRunning {
			t.Fatalf("resumed history contains a running tool: %+v", entry.Tool)
		}
	}
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 24})
	s.Apply(HistoryReplaced{Entries: entries})
	if ProjectThinking(s).Active || ProjectActivity(s).Surface != ActivityNone {
		t.Fatal("resumed state claims a live activity")
	}
	out := ansi.Strip(Render(s))
	if strings.Contains(out, "⠋") || strings.Contains(out, "running") {
		t.Fatalf("resumed view contains a live representation:\n%s", out)
	}
}

// TestIntermediateActivityIsNotPersisted proves the store only receives the
// turn's finalized messages: reasoning deltas, tool starts, previews, and
// answer deltas are presentation events with no persistence path.
func TestIntermediateActivityIsNotPersisted(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create(t.TempDir(), "m")
	if err != nil {
		t.Fatal(err)
	}

	p := NewProgram(nil, "test", 0)
	p.AttachSession(store, &sess, nil)
	p.persist(agent.UserMessage{Text: "go"})

	at := time.Now()
	p.state.Apply(ReasoningDelta{Text: "thinking", At: at, Seq: 1})
	p.state.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: at, Seq: 1})
	p.state.Apply(ToolPreview{ID: "c1", Summary: "half", Seq: 1})
	p.state.Apply(AssistantDelta{Text: "answer", At: at, Seq: 1})

	messages, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("persisted %d messages during live activity, want only the user turn", len(messages))
	}

	p.persist(agent.AssistantMessage{Message: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "answer"}}}})
	p.persist(agent.ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "package a"}}})
	messages, err = store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("persisted %d messages after finalization, want 3", len(messages))
	}
}

// TestClearIsPresentationOnly: wiping the transcript must not change the
// authority, the busy turn, or the task identity that own execution.
func TestClearIsPresentationOnly(t *testing.T) {
	s := New()
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true, Session: "#7", HasSession: true})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: time.Now(), Seq: 1})

	workspace := s.Status.Workspace
	sessionID := s.Status.Session
	turnStart := s.TurnStartedAt
	busy := s.Status.Busy

	s.Apply(Cleared{})

	if s.Status.Workspace != workspace || s.Status.Session != sessionID {
		t.Fatalf("clear mutated authority: workspace %q -> %q, session %q -> %q",
			workspace, s.Status.Workspace, sessionID, s.Status.Session)
	}
	if !s.TurnStartedAt.Equal(turnStart) || s.Status.Busy != busy {
		t.Fatalf("clear mutated the turn: start %v -> %v, busy %v -> %v",
			turnStart, s.TurnStartedAt, busy, s.Status.Busy)
	}
	if len(s.Entries) != 0 {
		t.Fatalf("clear kept %d transcript entries", len(s.Entries))
	}
}

// TestBuildInvestigationRegression reproduces the reported BUILD flow. The
// final frame must contain one representation per semantic fact: role, thinking,
// each completed call, and the one live call.
func TestBuildInvestigationRegression(t *testing.T) {
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 32})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "check this project and analyze what this project is about?", At: at, Seq: 1})
	s.Apply(ReasoningDelta{Text: "I'll inspect the project.", At: at, Seq: 1})
	s.Apply(AssistantDelta{Text: "I'll inspect the project.", At: at.Add(time.Second), Seq: 1})

	completed := []struct{ id, name, summary string }{
		{"c1", "ls", "ls backend"},
		{"c2", "ls", "ls ."},
		{"c3", "read", "read backend/go.mod"},
		{"c4", "read", "read backend/Makefile"},
		{"c5", "read", "read package.json"},
	}
	for i, call := range completed {
		start := at.Add(time.Duration(i+2) * time.Second)
		s.Apply(ToolStarted{ID: call.id, Name: call.name, Summary: call.summary, At: start, Seq: 1})
		s.Apply(ToolFinished{ID: call.id, Text: "19 lines\n", Summary: "19 lines", At: start.Add(time.Second), Seq: 1})
	}
	s.Apply(ToolStarted{ID: "c6", Name: "ls", Summary: "inspecting backend/internal", At: at.Add(10 * time.Second), Seq: 1})

	out := ansi.Strip(Render(s))
	if got := strings.Count(out, "build › "); got != 1 {
		t.Fatalf("BUILD role appeared %d times, want one:\n%s", got, out)
	}
	if got := strings.Count(out, "+ Thought"); got != 1 {
		t.Fatalf("reasoning appeared %d times, want one:\n%s", got, out)
	}
	for _, call := range completed {
		if got := strings.Count(out, call.summary); got != 1 {
			t.Fatalf("completed %q appeared %d times, want one:\n%s", call.summary, got, out)
		}
	}
	if got := strings.Count(out, "◌ inspecting backend/internal"); got != 1 {
		t.Fatalf("live action appeared %d times, want one:\n%s", got, out)
	}
	if got := strings.Count(out, "⠋"); got != 0 {
		t.Fatalf("live view kept a spinner frame %d times, want none:\n%s", got, out)
	}
	for _, forbidden := range []string{"✳ BUILD", "✳ Running", "✳ · Running", "⠋"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("duplicate activity representation %q:\n%s", forbidden, out)
		}
	}
}

// TestLiveModeRoleAndActivitySurfaces: the output-first composer renders the
// authority prefix, and the footer never repeats the role or the running call.
func TestLiveModeRoleAndActivitySurfaces(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 24})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "ls", Summary: "inspecting backend/internal", At: time.Now(), Seq: 1})

	pal := PaletteFor(s.Theme)
	layout := ComputeLayout(s)
	composer := ansi.Strip(strings.Join(liveComposerLines(s, pal, layout), "\n"))
	if got := strings.Count(composer, "build › "); got != 1 {
		t.Fatalf("live prompt role appeared %d times, want one:\n%s", got, composer)
	}
	status := ansi.Strip(liveStatusLine(s, pal, layout))
	if strings.Contains(strings.ToUpper(status), "BUILD") {
		t.Fatalf("live footer repeated the role: %q", status)
	}
	if strings.Contains(status, "Running") || strings.Contains(status, "inspecting backend/internal") {
		t.Fatalf("live footer repeated the running entry: %q", status)
	}
}

// TestRedrawAndScrollNeverDuplicateActivity: repainting the same state, or
// detaching the viewport, must not clone the live representation.
func TestRedrawAndScrollNeverDuplicateActivity(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 24})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "ls", Summary: "inspecting backend/internal", At: time.Now(), Seq: 1})

	for i := 0; i < 3; i++ {
		out := ansi.Strip(Render(s))
		if got := strings.Count(out, "inspecting backend/internal"); got != 1 {
			t.Fatalf("redraw %d: action appeared %d times, want one:\n%s", i, got, out)
		}
	}
	s.Apply(ScrollMoved{Delta: 5})
	out := ansi.Strip(Render(s))
	if got := strings.Count(out, "inspecting backend/internal"); got > 1 {
		t.Fatalf("scrolled view duplicated the action %d times:\n%s", got, out)
	}
}

// TestRoleSwitchLeavesOneActivitySurface: changing authority moves the prompt
// boundary only. It cannot strand a second activity surface or a stale role.
func TestRoleSwitchLeavesOneActivitySurface(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 96, Height: 24})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "ls", Summary: "inspecting backend/internal", At: time.Now(), Seq: 1})

	s.Apply(WorkspaceUpdated{Workspace: "ASK", HasWorkspace: true})
	out := ansi.Strip(Render(s))
	if got := strings.Count(out, "ask › "); got != 1 {
		t.Fatalf("new role prompt appeared %d times, want one:\n%s", got, out)
	}
	if strings.Contains(out, "build › ") {
		t.Fatalf("stale role prompt survived the switch:\n%s", out)
	}
	if got := strings.Count(out, "inspecting backend/internal"); got != 1 {
		t.Fatalf("activity appeared %d times after the role switch:\n%s", got, out)
	}
	if got := ProjectActivity(s).Surface; got != ActivityEntry {
		t.Fatalf("surface = %s after the switch, want the running entry", got)
	}
}

func countStripped(lines []string, needle string) int {
	n := 0
	for _, line := range lines {
		n += strings.Count(ansi.Strip(line), needle)
	}
	return n
}
