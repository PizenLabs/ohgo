package tui

import (
	"strings"
	"testing"
	"time"
)

func TestApplyStreamsAssistantTextIntoOneEntry(t *testing.T) {
	// Deltas must coalesce rather than create one entry per token, or the
	// transcript becomes a stack of fragments the model never wrote.
	state := New()
	state.Apply(AssistantDelta{Text: "Hello"})
	state.Apply(AssistantDelta{Text: ", "})
	state.Apply(AssistantDelta{Text: "world"})

	if got := state.AssistantText(); got != "Hello, world" {
		t.Errorf("assistant text = %q, want %q", got, "Hello, world")
	}
	assistant := 0
	for _, entry := range state.Entries {
		if entry.Kind == EntryAssistant {
			assistant++
		}
	}
	if assistant != 1 {
		t.Errorf("assistant entries = %d, want 1", assistant)
	}
}

func TestApplySplitsAssistantTextAcrossTurns(t *testing.T) {
	// A second turn must not append to the first: the transcript would read as
	// one continuous answer.
	state := New()
	state.Apply(AssistantDelta{Text: "first"})
	state.Apply(AssistantDone{})
	state.Apply(AssistantDelta{Text: "second"})

	if got := state.AssistantText(); got != "firstsecond" {
		t.Errorf("text = %q, want the two turns concatenated for display", got)
	}
	entries := 0
	for _, entry := range state.Entries {
		if entry.Kind == EntryAssistant {
			entries++
		}
	}
	if entries != 2 {
		t.Errorf("assistant entries = %d, want 2", entries)
	}
}

func TestApplyTracksToolLifecycle(t *testing.T) {
	state := New()
	now := time.Now()

	state.Apply(ToolStarted{ID: "c1", Name: "read", At: now})
	tool := state.tool("c1")
	if tool == nil {
		t.Fatal("tool entry was not created")
	}
	if tool.Status != ToolRunning {
		t.Errorf("status = %q, want running", tool.Status)
	}

	state.Apply(ToolFinished{ID: "c1", Text: "contents", At: now.Add(time.Second)})
	if tool.Status != ToolDone {
		t.Errorf("status = %q, want done", tool.Status)
	}
	if tool.Text != "contents" {
		t.Errorf("text = %q, want the outcome", tool.Text)
	}
}

func TestApplyMarksFailedTools(t *testing.T) {
	state := New()
	state.Apply(ToolStarted{ID: "c1", Name: "bash", At: time.Now()})
	state.Apply(ToolFinished{ID: "c1", Text: "exit 1", IsError: true, At: time.Now()})

	tool := state.tool("c1")
	if tool.Status != ToolError {
		t.Errorf("status = %q, want error", tool.Status)
	}
	// The view must be able to tell success from failure without re-deriving
	// it from the text.
	if tool.Status == ToolDone {
		t.Error("an error result rendered as a success")
	}
}

func TestApplyUpdatesPreviewOfARunningTool(t *testing.T) {
	// A preview on a finished call must be ignored: the summary line would
	// otherwise resurrect stale progress after the outcome arrived.
	state := New()
	state.Apply(ToolStarted{ID: "c1", Name: "read", At: time.Now()})
	state.Apply(ToolPreview{ID: "c1", Summary: "read a.go"})
	if got := state.tool("c1").Summary; got != "read a.go" {
		t.Errorf("summary = %q, want the preview", got)
	}

	state.Apply(ToolFinished{ID: "c1", Text: "done", At: time.Now()})
	state.Apply(ToolPreview{ID: "c1", Summary: "read a.go"})
	if got := state.tool("c1").Summary; got != "" {
		t.Errorf("summary = %q, want it cleared when the call finished", got)
	}
}

func TestApplyIgnoresEventsForUnknownTools(t *testing.T) {
	// A call compacted out of the view must not create a phantom entry or panic.
	state := New()
	state.Apply(ToolFinished{ID: "ghost", Text: "x", At: time.Now()})
	state.Apply(ToolPreview{ID: "ghost", Summary: "y"})
	if len(state.Entries) != 0 {
		t.Errorf("entries = %d, want 0 for unknown call ids", len(state.Entries))
	}
}

func TestToolEntryDuration(t *testing.T) {
	start := time.Now()
	tool := &ToolEntry{StartedAt: start}
	// An unfinished call must show elapsed time, not zero, so a long command
	// looks alive.
	if tool.Duration() <= 0 {
		t.Error("an unfinished tool reports no elapsed time")
	}
	tool.EndedAt = start.Add(2 * time.Second)
	if got := tool.Duration(); got != 2*time.Second {
		t.Errorf("duration = %v, want 2s", got)
	}
}

func TestApprovalDefaultsToTheSafestChoice(t *testing.T) {
	// An operator who does not read the prompt must land on the conservative
	// answer, so the default is the last choice.
	state := New()
	state.Apply(ApprovalAsked{
		ToolName: "bash",
		Reason:   "runs a shell command",
		Choices:  []string{"Yes", "No"},
	})
	if got := state.Pending.Selected(); got != "No" {
		t.Errorf("default = %q, want the last choice", got)
	}
	if state.Pending.Reason != "runs a shell command" {
		t.Errorf("reason = %q, want the declared reason", state.Pending.Reason)
	}
}

func TestApprovalCursorWraps(t *testing.T) {
	p := &PendingApproval{Choices: []string{"a", "b", "c"}, Cursor: 2}
	p.Move(1)
	if p.Cursor != 0 {
		t.Errorf("cursor = %d, want it to wrap to 0", p.Cursor)
	}
	p.Move(-1)
	if p.Cursor != 2 {
		t.Errorf("cursor = %d, want it to wrap to the last", p.Cursor)
	}
	p.Move(5)
	if p.Cursor != 1 {
		t.Errorf("cursor = %d, want 1 (5 mod 3 from 2)", p.Cursor)
	}
}

func TestApprovalWithNoChoicesDoesNotPanic(t *testing.T) {
	p := &PendingApproval{}
	p.Move(1)
	if p.Selected() != "" {
		t.Errorf("selected = %q, want empty", p.Selected())
	}
}

func TestApplyClosesApproval(t *testing.T) {
	state := New()
	state.Apply(ApprovalAsked{ToolName: "bash", Choices: []string{"Yes", "No"}})
	state.Apply(ApprovalAnswered{})
	if state.Pending != nil {
		t.Error("the approval prompt is still open")
	}
}

func TestApplyAnnouncesCompaction(t *testing.T) {
	// A silently summarised history is indistinguishable from a lost one, so
	// compaction must leave a visible trace.
	state := New()
	state.Apply(Compacted{Messages: 12, Retained: 8, AfterTokens: 500, Window: 1000})

	found := false
	for _, entry := range state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "compacted") {
			found = true
		}
	}
	if !found {
		t.Errorf("no compaction notice in %+v", state.Entries)
	}
	if state.Status.ContextUsed != 500 {
		t.Errorf("context used = %d, want the post-compaction size", state.Status.ContextUsed)
	}
}

func TestStatusPercentHandlesUnknownWindow(t *testing.T) {
	// A model with no published window must not render "0%", which would read
	// as an empty context rather than an unknown one.
	if got := (Status{ContextUsed: 500}).Percent(); got != -1 {
		t.Errorf("percent = %d, want -1 for an unknown window", got)
	}
	if got := (Status{ContextUsed: 500, ContextWindow: 1000}).Percent(); got != 50 {
		t.Errorf("percent = %d, want 50", got)
	}
}

func TestUserSubmittedClearsInputAndMarksBusy(t *testing.T) {
	state := New()
	state.Apply(InputChanged{Text: "hello"})
	state.Apply(UserSubmitted{Text: "hello"})

	if state.Input != "" {
		t.Errorf("input = %q, want it cleared once sent", state.Input)
	}
	if !state.Status.Busy {
		t.Error("the session is not marked busy")
	}
	state.Apply(AssistantDone{})
	if state.Status.Busy {
		t.Error("the session stayed busy after the turn ended")
	}
}

func TestToolSummaryForCallPrefersKnownKeys(t *testing.T) {
	// A running tool must show what it is acting on, not just its name, and the
	// key it looks for is the tool's own primary argument.
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"path", "read", map[string]any{"path": "a.go"}, "read a.go"},
		{"command", "bash", map[string]any{"command": "ls -la"}, "bash ls -la"},
		{"pattern", "grep", map[string]any{"pattern": "func Test"}, "grep func Test"},
		{"url", "web_fetch", map[string]any{"url": "https://x.dev"}, "web_fetch https://x.dev"},
		{"unknown key", "read", map[string]any{"other": "v"}, "read"},
		{"empty args", "read", map[string]any{}, "read"},
		{"non string value", "read", map[string]any{"path": 12.0}, "read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolSummaryForCall(tc.tool, tc.args); got != tc.want {
				t.Errorf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToolSummaryCollapsesLongValues(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := ToolSummaryForCall("bash", map[string]any{"command": long})
	if len(got) > 80 {
		t.Errorf("summary length = %d, want it bounded", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("summary = %q, want a truncation marker", got)
	}
}

func TestTruncateForDisplay(t *testing.T) {
	if got, truncated := TruncateForDisplay("short", 100); truncated || got != "short" {
		t.Errorf("got (%q, %v), want it unchanged", got, truncated)
	}
	got, truncated := TruncateForDisplay("long text", 4)
	if !truncated {
		t.Error("a long value was not marked truncated")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("result = %q, want a truncation notice", got)
	}
}

func TestQuitRequestedSetsTheFlag(t *testing.T) {
	state := New()
	state.Apply(QuitRequested{})
	if !state.Quit {
		t.Error("the quit flag was not set")
	}
}

func TestParseCommandRecognisesSlashLines(t *testing.T) {
	cmd, ok := ParseCommand("/model anthropic/claude-opus-4-1")
	if !ok {
		t.Fatal("a slash line was not recognised as a command")
	}
	if cmd.Name != "model" {
		t.Errorf("name = %q, want model", cmd.Name)
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "anthropic/claude-opus-4-1" {
		t.Errorf("args = %v, want the single argument", cmd.Args)
	}
}

func TestParseCommandRejectsNonCommands(t *testing.T) {
	// A prompt that merely mentions a slash must reach the model, not be
	// swallowed as a command.
	for _, line := range []string{
		"explain /usr/bin",
		"",
		"   ",
		"just a prompt",
		"/",          // a bare slash is a path, not a command
		"  /model x", // leading whitespace is trimmed, so this IS a command
	} {
		got, ok := ParseCommand(line)
		if line == "  /model x" {
			if !ok {
				t.Error("a trimmed slash line was not recognised")
			}
			continue
		}
		if ok {
			t.Errorf("%q parsed as command %+v, want it rejected", line, got)
		}
	}
}

func TestParseCommandIsCaseInsensitive(t *testing.T) {
	cmd, ok := ParseCommand("/HELP")
	if !ok || cmd.Name != "help" {
		t.Errorf("got (%+v, %v), want the name lowercased", cmd, ok)
	}
}

func TestParseCommandSplitsArguments(t *testing.T) {
	cmd, _ := ParseCommand("/resume 12 extra")
	want := []string{"12", "extra"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, cmd.Args[i], want[i])
		}
	}
}

func TestLookupCommand(t *testing.T) {
	if _, ok := LookupCommand("model"); !ok {
		t.Error("a registered command was not found")
	}
	if _, ok := LookupCommand("MODEL"); !ok {
		t.Error("lookup is not case-insensitive")
	}
	if _, ok := LookupCommand("nonexistent"); ok {
		t.Error("an unregistered command was found")
	}
}

func TestHelpTextHidesAliases(t *testing.T) {
	// /quit and /exit are the same command; listing both doubles the help.
	text := HelpText()
	if !strings.Contains(text, "/model") {
		t.Errorf("help does not mention /model: %s", text)
	}
	if strings.Contains(text, "/quit") {
		t.Errorf("a hidden alias was listed: %s", text)
	}
}

func TestClearedEmptiesTheTranscript(t *testing.T) {
	// /clear must drop everything, or the "fresh context" is not fresh.
	state := New()
	state.Apply(UserSubmitted{Text: "question"})
	state.Apply(AssistantDelta{Text: "answer"})
	state.Apply(ToolStarted{ID: "c1", Name: "read", At: time.Now()})

	state.Apply(Cleared{})
	if len(state.Entries) != 0 {
		t.Errorf("entries = %d, want 0 after /clear", len(state.Entries))
	}

	// A turn after the clear must start a new assistant entry rather than
	// appending to the one that was dropped.
	state.Apply(AssistantDelta{Text: "fresh"})
	if got := state.AssistantText(); got != "fresh" {
		t.Errorf("text = %q, want only the post-clear output", got)
	}
}

func TestClearedKeepsTheStatusLine(t *testing.T) {
	// Clearing the context must not forget which model is in use.
	state := New()
	state.Status.Model = "anthropic/claude-sonnet-4-5"
	state.Status.ContextUsed = 500
	state.Apply(Cleared{})

	if state.Status.Model == "" {
		t.Error("the model was dropped by /clear")
	}
}
