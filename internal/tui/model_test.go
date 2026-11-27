package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
)

func TestApplyStreamsAssistantTextIntoOneEntry(t *testing.T) {
	// Deltas must coalesce rather than create one entry per token, or the
	// transcript becomes a stack of fragments the model never wrote. The
	// coalesced text stays buffered until the publication decision.
	state := New()
	state.Apply(AssistantDelta{Text: "Hello"})
	state.Apply(AssistantDelta{Text: ", "})
	state.Apply(AssistantDelta{Text: "world"})

	if got := state.AssistantText(); got != "" {
		t.Errorf("uncommitted assistant text = %q, want empty", got)
	}
	assistant := 0
	for _, entry := range state.Entries {
		if entry.Kind == EntryAssistant {
			assistant++
			if got := entry.AssistantText; got != "Hello, world" {
				t.Errorf("buffered text = %q, want %q", got, "Hello, world")
			}
		}
	}
	if assistant != 1 {
		t.Errorf("assistant entries = %d, want 1", assistant)
	}
}

func TestApplySplitsAssistantTextAcrossTurns(t *testing.T) {
	// A second turn must not append to the first: the transcript would read as
	// one continuous answer. A second candidate is buffered until it commits.
	state := New()
	state.Apply(AssistantDelta{Text: "first"})
	state.Apply(AssistantReconcile{Text: "first", Answer: agent.AnswerCommitted})
	state.Apply(AssistantDone{})
	state.Apply(AssistantDelta{Text: "second"})

	if got := state.AssistantText(); got != "first" {
		t.Errorf("text = %q, want only the committed first turn", got)
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
	state.Apply(AssistantReconcile{Text: "second", Answer: agent.AnswerCommitted})
	if got := state.AssistantText(); got != "firstsecond" {
		t.Errorf("text = %q, want the two committed turns concatenated", got)
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
	// Backdate the start by a millisecond: the contract is "a live call shows
	// elapsed time", and the same-instant clock reading is not the failure
	// mode under test.
	start := time.Now().Add(-time.Millisecond)
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

// The approval cursor stops at the ends rather than wrapping.
//
// Wrapping turned every navigation key into an escape from the conservative
// default, which sits on the last choice: a forward move resolved to index 0,
// so Tab landed on "Yes" and Enter then ran the tool. See
// TestApprovalNavigationStopsAtTheEndInsteadOfWrapping for the user-visible
// contract; this pins the arithmetic.
func TestApprovalCursorStopsAtTheEnds(t *testing.T) {
	p := &PendingApproval{Choices: []string{"a", "b", "c"}, Cursor: 2}
	p.Move(1)
	if p.Cursor != 2 {
		t.Errorf("cursor = %d, want it to stay at the last choice", p.Cursor)
	}
	p.Move(-1)
	if p.Cursor != 1 {
		t.Errorf("cursor = %d, want one step back", p.Cursor)
	}
	p.Move(5)
	if p.Cursor != 2 {
		t.Errorf("cursor = %d, want it clamped to the last choice", p.Cursor)
	}
	p.Move(-5)
	if p.Cursor != 0 {
		t.Errorf("cursor = %d, want it clamped to the first choice", p.Cursor)
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

// VersionLabel spells the release one way on every surface: trimmed,
// v-prefixed, and empty for unknown so no chrome invents a release.
func TestVersionLabelNormalizes(t *testing.T) {
	cases := map[string]string{
		"1.0.0":     "v1.0.0",
		"v1.0.0":    "v1.0.0",
		"0.1.0-dev": "v0.1.0-dev",
		"  2.3.4  ": "v2.3.4",
		"":          "",
		"   ":       "",
	}
	for in, want := range cases {
		if got := VersionLabel(in); got != want {
			t.Errorf("VersionLabel(%q) = %q, want %q", in, got, want)
		}
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
		{"url", "web_fetch", map[string]any{"url": "https://x.dev"}, "external web_fetch https://x.dev"},
		{"search", "search", map[string]any{"query": "go generics"}, "external search go generics"},
		{"mcp", "mcp_read", map[string]any{"server": "s", "tool": "read"}, "external mcp_read s read"},
		{"browser", "browser", map[string]any{"operation": "open", "url": "https://x.dev"}, "external browser open https://x.dev"},
		{"browser follow", "browser", map[string]any{"operation": "follow", "index": float64(2)}, "external browser follow #2"},
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
	// appending to the one that was dropped. The new candidate is buffered
	// until it is committed.
	state.Apply(AssistantDelta{Text: "fresh"})
	if got := state.AssistantText(); got != "" {
		t.Errorf("text = %q, want no committed output before reconcile", got)
	}
	if len(state.Entries) != 1 || state.Entries[0].AssistantText != "fresh" {
		t.Errorf("entries = %+v, want one buffered post-clear candidate", state.Entries)
	}
	state.Apply(AssistantReconcile{Text: "fresh", Answer: agent.AnswerCommitted})
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

func TestAssistantReconcileReplacesTheProvisionalStream(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "partial draft"})
	state.Apply(AssistantReconcile{Text: "the exact committed answer", Answer: agent.AnswerCommitted})
	if got := state.AssistantText(); got != "the exact committed answer" {
		t.Fatalf("AssistantText = %q, want the exact committed value", got)
	}
	if open := state.openAssistant(); open < 0 || state.Entries[open].Provisional {
		t.Fatal("the reconciled answer is still provisional")
	}
}

func TestAssistantReconcileDiscardsToolNarration(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "Let me inspect the backend."})
	state.Apply(AssistantReconcile{Answer: agent.AnswerNarration})
	if got := state.AssistantText(); got != "" {
		t.Fatalf("AssistantText = %q, want the withdrawn narration gone", got)
	}
	state.Apply(ToolStarted{ID: "c1", Name: "read", At: time.Now()})
	state.Apply(AssistantDelta{Text: "final answer"})
	state.Apply(AssistantReconcile{Text: "final answer", Answer: agent.AnswerCommitted})
	state.Apply(AssistantDone{})
	if got := state.AssistantText(); got != "final answer" {
		t.Fatalf("AssistantText = %q, want only the accepted answer", got)
	}
}

// A dropped delta leaves a hole in the live preview. The reconcile value is
// the whole committed answer, so it must replace the preview, never append to
// it; appending would duplicate the visible prefix and reorder the text.
func TestAssistantReconcileRepairsADroppedDeltaWithoutDuplication(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "the first half"})
	state.Apply(AssistantReconcile{Text: "the first half and the repaired second half", Answer: agent.AnswerCommitted})
	if got := state.AssistantText(); got != "the first half and the repaired second half" {
		t.Fatalf("AssistantText = %q", got)
	}
	if strings.Count(state.AssistantText(), "the first half") != 1 {
		t.Fatalf("the preview prefix was duplicated: %q", state.AssistantText())
	}
}

// A terminal refusal already streamed as a normal block. It must not vanish
// without a trace when the review refuses it: the text moves to a labelled
// display-only draft, while AssistantText stays empty so copy and persistence
// never treat it as the answer. The label carries the control-plane reason
// verbatim; the renderer invents none.
func TestAssistantReconcileKeepsRejectedAnswerAsWithdrawnDraft(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "There is no database."})
	state.Apply(AssistantReconcile{Answer: agent.AnswerRejected, Reason: "answer review requested changes"})
	if got := state.AssistantText(); got != "" {
		t.Fatalf("AssistantText = %q, want the withdrawn draft excluded", got)
	}
	entry := state.Entries[len(state.Entries)-1]
	if !entry.Discarded || entry.Provisional {
		t.Fatalf("withdrawn entry state = discarded=%v provisional=%v", entry.Discarded, entry.Provisional)
	}
	if entry.WithdrawnText != "There is no database." {
		t.Fatalf("WithdrawnText = %q", entry.WithdrawnText)
	}
	if entry.WithdrawnReason != "answer review requested changes" {
		t.Fatalf("WithdrawnReason = %q", entry.WithdrawnReason)
	}
	var rendered strings.Builder
	renderEntryContent(&rendered, entry, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}, entryRenderContext{})
	if !strings.Contains(rendered.String(), "draft withdrawn") ||
		!strings.Contains(rendered.String(), "There is no database.") {
		t.Fatalf("withdrawn draft is not visible:\n%s", rendered.String())
	}

	// The corrected answer is a new block; the withdrawn text never leaks into
	// the answer projection. The new candidate stays buffered until commit.
	state.Apply(AssistantDelta{Text: "This project is a Go service."})
	if got := state.AssistantText(); got != "" {
		t.Fatalf("uncommitted candidate AssistantText = %q, want empty", got)
	}
	state.Apply(AssistantReconcile{Text: "This project is a Go service.", Answer: agent.AnswerCommitted})
	if got := state.AssistantText(); got != "This project is a Go service." {
		t.Fatalf("committed AssistantText = %q", got)
	}
}

// A provisional block holds its text in state but paints nothing. The
// telemetry line carries the review status, so unapproved prose never appears
// and vanishes in the transcript.
func TestProvisionalAnswerIsBufferedWithoutTranscriptLabel(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "candidate answer"})
	entry := state.Entries[len(state.Entries)-1]
	if !entry.Provisional {
		t.Fatal("streamed answer is not provisional")
	}
	if got := state.AssistantText(); got != "" {
		t.Fatalf("buffered candidate entered the answer projection: %q", got)
	}
	var rendered strings.Builder
	renderEntryContent(&rendered, entry, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}, entryRenderContext{})
	if strings.TrimSpace(rendered.String()) != "" {
		t.Fatalf("buffered candidate painted transcript rows:\n%s", rendered.String())
	}
	if status := liveStatusLine(state, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}); !strings.Contains(status, "verifying evidence…") {
		t.Fatalf("candidate review status missing from the telemetry line: %q", status)
	}
	state.Apply(AssistantReconcile{Text: "candidate answer", Answer: agent.AnswerCommitted})
	entry = state.Entries[len(state.Entries)-1]
	if entry.Provisional {
		t.Fatal("committed answer is still provisional")
	}
	rendered.Reset()
	renderEntryContent(&rendered, entry, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}, entryRenderContext{})
	if !strings.Contains(rendered.String(), "candidate answer") {
		t.Fatalf("committed answer is not visible:\n%s", rendered.String())
	}
}

// A committed answer is never withdrawn by a later reconcile. Only provisional
// text participates in the accept/discard decision, so a late event from a
// correction or a stream close cannot erase what the runtime published.
func TestCommittedAnswerIsNeverRetracted(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "the committed answer"})
	state.Apply(AssistantReconcile{Text: "the committed answer", Answer: agent.AnswerCommitted})
	state.Apply(AssistantReconcile{Answer: agent.AnswerSuperseded})
	if got := state.AssistantText(); got != "the committed answer" {
		t.Fatalf("committed answer was retracted: %q", got)
	}
	entry := state.Entries[len(state.Entries)-1]
	if entry.Discarded || entry.WithdrawnText != "" {
		t.Fatalf("committed entry was marked withdrawn: %+v", entry)
	}
}

// A withdrawn draft is display-only: copy sees an empty answer, not the text a
// review rejected.
func TestWithdrawnDraftIsNotCopyableAsAnswer(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "There is no database."})
	state.Apply(AssistantReconcile{Answer: agent.AnswerRejected, Reason: "answer review requested changes"})
	entry := state.Entries[len(state.Entries)-1]
	if got := BlockCopyText(entry); strings.TrimSpace(got) != "" {
		t.Fatalf("withdrawn draft copied as an answer: %q", got)
	}
	if got := state.AssistantText(); strings.TrimSpace(got) != "" {
		t.Fatalf("withdrawn draft projected as an answer: %q", got)
	}
}

// A completed transport has no streamed preview. A terminal refusal still
// leaves exactly one accountable artifact, and a repeated settlement does not
// duplicate it.
func TestRejectedAnswerOpensOneArtifactWithoutAPreview(t *testing.T) {
	state := New()
	state.Apply(AssistantReconcile{
		Text:   "There is no database.",
		Answer: agent.AnswerRejected,
		Reason: "the answer asserts a claim the run did not observe",
	})
	state.Apply(AssistantReconcile{
		Text:   "There is no database.",
		Answer: agent.AnswerRejected,
		Reason: "the answer asserts a claim the run did not observe",
	})
	if got := state.AssistantText(); got != "" {
		t.Fatalf("the refused answer entered the answer projection: %q", got)
	}
	var withheld []Entry
	for _, entry := range state.Entries {
		if entry.Kind == EntryAssistant && entry.Discarded {
			withheld = append(withheld, entry)
		}
	}
	if len(withheld) != 1 {
		t.Fatalf("withheld artifacts = %d, want exactly one", len(withheld))
	}
	if withheld[0].WithdrawnText != "There is no database." ||
		withheld[0].WithdrawnReason != "the answer asserts a claim the run did not observe" {
		t.Fatalf("artifact = %+v", withheld[0])
	}
}

// A turn boundary that never received a publication decision must not promote
// the preview to an answer. Only AnswerCommitted settles text as an answer;
// an unclassified preview is withdrawn, and the terminal footer carries the
// real outcome.
func TestAssistantDoneDoesNotPromoteAnUnclassifiedPreview(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "partial candidate"})
	state.Apply(AssistantDone{})
	if got := state.AssistantText(); got != "" {
		t.Fatalf("an unclassified preview became the answer: %q", got)
	}
	entry := state.Entries[len(state.Entries)-1]
	if !entry.Discarded {
		t.Fatalf("unclassified preview was not withdrawn: %+v", entry)
	}
	if entry.WithdrawnText != "" || entry.WithdrawnReason != "" {
		t.Fatalf("a turn with no review verdict was labelled as a refusal: %+v", entry)
	}
}

// Cancellation is not an answer-review refusal. The partial text stays visible
// as one display-only draft that names the interruption, and it never enters
// the answer projection.
func TestTurnCancelledLabelsTheDraftAsInterrupted(t *testing.T) {
	state := New()
	state.Apply(AssistantDelta{Text: "half an answer"})
	state.Apply(TurnCancelled{At: time.Now()})
	if got := state.AssistantText(); got != "" {
		t.Fatalf("a cancelled partial answer was treated as a result: %q", got)
	}
	var entry *Entry
	for i := range state.Entries {
		if state.Entries[i].Kind == EntryAssistant {
			entry = &state.Entries[i]
		}
	}
	if entry == nil || !entry.Discarded || entry.WithdrawnText != "half an answer" {
		t.Fatalf("cancelled draft state = %+v", entry)
	}
	if entry.WithdrawnReason != "the turn was interrupted" {
		t.Fatalf("cancelled draft reason = %q", entry.WithdrawnReason)
	}
	var rendered strings.Builder
	renderEntryContent(&rendered, *entry, PaletteFor(""), Layout{Width: 80, ContentWidth: 80}, entryRenderContext{})
	if !strings.Contains(rendered.String(), "draft withdrawn") ||
		!strings.Contains(rendered.String(), "interrupted") {
		t.Fatalf("cancelled draft has no truthful label:\n%s", rendered.String())
	}
}
