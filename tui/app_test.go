package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/jsonschema"
	"github.com/PizenLabs/ohgo/session"
)

// blockingTool blocks until it is approved or denied.
type blockingTool struct {
	name     string
	entered  chan struct{}
	finished chan agent.Result
}

func newBlockingTool(name string) *blockingTool {
	return &blockingTool{
		name:     name,
		entered:  make(chan struct{}, 1),
		finished: make(chan agent.Result, 1),
	}
}

func (t *blockingTool) Name() string        { return t.name }
func (t *blockingTool) Description() string { return "blocking test tool" }
func (t *blockingTool) Parameters() *jsonschema.Schema {
	return nil
}
func (t *blockingTool) Approval() agent.Approval {
	return agent.Approval{Tier: agent.TierExec, Reason: "runs a shell command"}
}
func (t *blockingTool) Exclusive(map[string]any) bool { return true }
func (t *blockingTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	close(t.entered)
	// Block until the context is cancelled, modelling a long-running tool that
	// the approval must have already cleared.
	<-ctx.Ctx.Done()
	result := agent.Result{Text: "ran"}
	t.finished <- result
	return result
}

func TestApprovalFuncBlocksTheToolAndReturnsTheAnswer(t *testing.T) {
	// The hook must actually block the tool: an approval that returned
	// immediately would let a shell command run before the user consented.
	p := NewProgram(nil, "test", 0)
	approve := p.ApprovalFunc()

	done := make(chan struct{})
	var gotOK bool
	go func() {
		defer close(done)
		gotOK, _ = approve(ai.ToolCall{ID: "c1", Name: "bash"}, agent.Approval{
			Tier: agent.TierExec, Reason: "runs a shell command",
		})
	}()

	// Take the question, then leave it unanswered for a while. The tool must
	// still be blocked.
	request := <-p.approvals
	select {
	case <-done:
		t.Fatal("the approval returned before the user answered")
	case <-time.After(80 * time.Millisecond):
	}

	// Answer it the way Update routes an answer.
	p.pending = &request
	request.answer <- approvalAnswer{approved: true}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the tool never unblocked after the answer")
	}
	if !gotOK {
		t.Error("the tool was denied when the user approved")
	}
}

func TestApprovalFuncCarriesTheDeclaredReason(t *testing.T) {
	// The prompt must show why it appeared; a bare tool name forces the user
	// to guess what they are consenting to.
	p := NewProgram(nil, "test", 0)
	approve := p.ApprovalFunc()

	go approve(ai.ToolCall{ID: "c1", Name: "bash"}, agent.Approval{
		Tier: agent.TierExec, Reason: "runs a shell command",
	})

	request := <-p.approvals
	if request.reason != "runs a shell command" {
		t.Errorf("reason = %q, want the declared reason", request.reason)
	}
	if request.toolName != "bash" {
		t.Errorf("tool name = %q, want bash", request.toolName)
	}
	request.answer <- approvalAnswer{approved: true}
}

func TestApprovalSummaryDescribesTheCall(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	approve := p.ApprovalFunc()

	go approve(ai.ToolCall{
		ID: "c1", Name: "bash", Arguments: map[string]any{"command": "rm -rf build"},
	}, agent.Approval{Tier: agent.TierExec})

	request := <-p.approvals
	if !strings.Contains(request.summary, "rm -rf build") {
		t.Errorf("summary = %q, want the command being run", request.summary)
	}
	request.answer <- approvalAnswer{approved: false, reason: "denied"}
}

// TestRebaseDropsCompactedHistory proves the rebase rule.
//
// Appending new turns to the pre-compaction history would silently undo the
// summarisation, so the history must be rebuilt from what was actually sent.
func TestRebaseDropsCompactedHistory(t *testing.T) {
	request := []agent.Message{
		agent.UserMessage{Text: "SUMMARY"},
		agent.UserMessage{Text: "recent"},
	}
	turns := []agent.Turn{{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "answer"}},
		},
		Results: []agent.ToolOutcome{{
			CallID: "c1", ToolName: "read", Result: agent.Result{Text: "contents"},
		}},
	}}

	history := rebase(request, turns)

	// The request must be preserved verbatim — it is what the provider saw.
	if len(history) != len(request)+2 {
		t.Fatalf("history = %d messages, want %d", len(history), len(request)+2)
	}
	if got := history[0].(agent.UserMessage).Text; got != "SUMMARY" {
		t.Errorf("first message = %q, want the compacted summary preserved", got)
	}
	// A tool result must follow its call, or the provider rejects the replay.
	for i, msg := range history {
		if _, ok := msg.(agent.ToolResultMessage); ok {
			if i == 0 {
				t.Fatal("a tool result appears before any tool call")
			}
		}
	}
}

func TestRebaseHandlesNoTurns(t *testing.T) {
	history := rebase([]agent.Message{agent.UserMessage{Text: "q"}}, nil)
	if len(history) != 1 {
		t.Errorf("history = %d messages, want the request unchanged", len(history))
	}
}

func TestIsAffirmative(t *testing.T) {
	for _, choice := range []string{"yes", "Yes", "y", "allow", "always"} {
		if !isAffirmative(choice) {
			t.Errorf("%q was treated as a denial", choice)
		}
	}
	for _, choice := range []string{"no", "No", "n", "", "cancel"} {
		if isAffirmative(choice) {
			t.Errorf("%q was treated as an approval", choice)
		}
	}
}

func TestAdvanceSpinnerWraps(t *testing.T) {
	if got := AdvanceSpinner(2, 3); got != 0 {
		t.Errorf("advance = %d, want 0", got)
	}
	if got := AdvanceSpinner(0, 0); got != 0 {
		t.Errorf("advance with no frames = %d, want 0", got)
	}
}

// WithVersion records the binary release on the chrome state: the header and
// the welcome frame read it from there, so the option is the only wiring the
// command layer needs.
func TestWithVersionRecordsTheRelease(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithVersion("1.0.0"))
	if got := p.state.Status.Version; got != "1.0.0" {
		t.Errorf("state version = %q, want the raw release %q", got, "1.0.0")
	}
	plain := NewProgram(nil, "test", 0)
	if got := plain.state.Status.Version; got != "" {
		t.Errorf("state version = %q, want unknown without the option", got)
	}
}

// TestRenderNeverPanicsOnAnEmptyState guards the alt-screen path: a nil tool or
// a missing window would crash the whole session, not just redraw.
func TestRenderNeverPanicsOnAnEmptyState(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Render panicked on an empty state: %v", r)
		}
	}()
	out := Render(New())
	if out == "" {
		t.Error("an empty state rendered nothing")
	}
}

func TestRenderShowsAToolOutcome(t *testing.T) {
	// The outcome must appear, indented, so the user can see what the tool
	// actually did rather than just that it ran.
	state := New()
	now := time.Now()
	state.Apply(ToolStarted{ID: "c1", Name: "read", At: now})
	state.Apply(ToolFinished{ID: "c1", Text: "file contents", At: now.Add(time.Second)})

	out := Render(state)
	if !strings.Contains(out, "read") {
		t.Errorf("output does not name the tool: %q", out)
	}
	if !strings.Contains(out, "file contents") {
		t.Errorf("output does not show the outcome: %q", out)
	}
}

// TestAttachSessionCopiesHistory guards the resume path.
//
// A program that aliased the caller's slice would see its history change if
// the caller mutated it afterwards, which would make a resumed session send
// different messages than the ones that were replayed.
func TestAttachSessionCopiesHistory(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	history := []agent.Message{agent.UserMessage{Text: "earlier"}}
	p.AttachSession(nil, nil, history)

	history[0] = agent.UserMessage{Text: "mutated"}
	if p.history[0].(agent.UserMessage).Text != "earlier" {
		t.Errorf("history = %+v, want the original preserved", p.history[0])
	}
}

// TestProgramPersistsWithoutASession proves the no-session mode.
//
// A throwaway or embedded session must not panic on a nil store, and must not
// lose the turn either.
func TestProgramPersistsWithoutASession(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.persist(agent.UserMessage{Text: "no store attached"})
	if len(p.history) != 0 {
		t.Errorf("history = %d, want 0 for an unattached session", len(p.history))
	}
}

// TestProgramPersistsTurns is the contract that makes the interactive UI safe
// to use: a session opened in the UI must survive the process.
func TestProgramPersistsTurns(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create(t.TempDir(), "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}

	p := NewProgram(nil, "test", 0)
	p.AttachSession(store, &sess, nil)

	p.persist(agent.UserMessage{Text: "do a thing"})
	p.persist(agent.AssistantMessage{Message: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "done"}},
	}})

	messages, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("persisted %d messages, want 2", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Text != "do a thing" {
		t.Errorf("first message = %+v, want the user turn", messages[0])
	}
	if messages[1].Role != "assistant" {
		t.Errorf("second message role = %q, want assistant", messages[1].Role)
	}
}

// TestProgramPersistsUIOnlyMessagesAsNoOp covers the drop rule.
func TestProgramPersistsUIOnlyMessagesAsNoOp(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, _ := store.Create(t.TempDir(), "m")

	p := NewProgram(nil, "test", 0)
	p.AttachSession(store, &sess, nil)
	p.persist(agent.Notification{Text: "internal noise"})

	messages, err := store.Messages(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Errorf("persisted %d messages, want 0 for a UI-only message", len(messages))
	}
}

// TestProgramResumeRoundTrip is the contract that makes the interactive UI
// safe to use.
//
// It persists a conversation through the program, drops the program, then
// reattaches a fresh one to the same session and replays. Anything that only
// worked in memory would fail here.
func TestProgramResumeRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	home := t.TempDir()

	// First session: persist a turn through the program.
	store, err := session.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(home, "anthropic/claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID

	first := NewProgram(nil, "test", 0)
	first.AttachSession(store, &sess, nil)
	first.persist(agent.UserMessage{Text: "remember this"})
	first.persist(agent.AssistantMessage{Message: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.go"}}},
	}})
	first.persist(agent.ToolResultMessage{
		ToolCallID: "c1", ToolName: "read",
		Content: []ai.ContentBlock{ai.TextContent{Text: "package a"}},
	})
	_ = store.Close()

	// Second session: reopen and replay, exactly as a resume does.
	reopened, err := session.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()

	resumedSess, err := reopened.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stored, err := reopened.Messages(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	history, err := session.Replay(stored)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("replayed %d messages, want 3", len(history))
	}

	second := NewProgram(nil, "test", 0)
	second.AttachSession(reopened, &resumedSess, history)
	if len(second.history) != 3 {
		t.Errorf("the resumed program holds %d messages, want 3", len(second.history))
	}

	// A tool result must still follow its call after the round trip, or the
	// provider rejects the replayed request.
	for i, msg := range second.history {
		if _, ok := msg.(agent.ToolResultMessage); ok {
			if i == 0 {
				t.Fatal("a tool result appears before any tool call")
			}
			if _, ok := second.history[i-1].(agent.AssistantMessage); !ok {
				t.Errorf("message %d before the tool result is %T, want AssistantMessage", i-1, second.history[i-1])
			}
		}
	}
}

// TestRunCommandDispatchesKnownCommands exercises the dispatch table.
//
// An unknown command must be reported, not forwarded to the model: a typo like
// "/modle" would otherwise cost a turn and produce a confused answer.
func TestRunCommandDispatchesKnownCommands(t *testing.T) {
	cases := []struct {
		name    string
		command Command
		want    string
	}{
		{"help opens the help center", Command{Name: "help"}, ""},
		{"clear", Command{Name: "clear"}, ""},
		{"model without args reports the current one", Command{Name: "model"}, "test-model"},
		{"unknown", Command{Name: "modle"}, "unknown command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := State{Status: Status{Model: "test-model"}}
			p := NewProgram(nil, "test-model", 0)
			p.state = state

			cmd := tc.command
			cmd.runOn(p)

			if tc.command.Name == "help" {
				if p.state.HelpModal == nil {
					t.Fatalf("/help did not open the command & help center")
				}
				// The old plain-text listing must never reach the transcript
				// again; the modal is the only presentation now.
				for _, entry := range p.state.Entries {
					if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "Commands:") {
						t.Errorf("/help polluted the transcript: %q", entry.Notice)
					}
				}
				return
			}
			if tc.want == "" {
				return
			}
			found := false
			for _, entry := range p.state.Entries {
				if entry.Kind == EntryNotice && strings.Contains(entry.Notice, tc.want) {
					found = true
				}
			}
			if !found {
				t.Errorf("no notice containing %q in %+v", tc.want, p.state.Entries)
			}
		})
	}
}

// TestRunCommandClearsHistory proves /clear resets the conversation too.
//
// Clearing only the visible transcript while keeping history would make the
// next request carry the very context the user asked to drop.
func TestRunCommandClearsHistory(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.history = []agent.Message{agent.UserMessage{Text: "old"}}

	Command{Name: "clear"}.runOn(p)

	if len(p.history) != 0 {
		t.Errorf("history = %d messages, want 0 after /clear", len(p.history))
	}
	if len(p.state.Entries) != 0 {
		t.Errorf("entries = %d, want 0 after /clear", len(p.state.Entries))
	}
}

// TestRunCommandSwitchesTheModel covers /model with an argument.
func TestRunCommandSwitchesTheModel(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var got string
	p.SetModelSwitcher(
		func(id string) error {
			got = id
			return nil
		},
		func() string { return "anthropic/claude-sonnet-4-5" },
	)

	Command{Name: "model", Args: []string{"openai/gpt-5"}}.runOn(p)

	if got != "openai/gpt-5" {
		t.Errorf("the switcher was called with %q, want the requested id", got)
	}
	// The status line must follow, or the footer lies about what will run.
	if p.state.Status.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("status model = %q", p.state.Status.Model)
	}
}

// TestRunCommandReportsASwitchFailure keeps a bad /model from failing silently.
func TestRunCommandReportsASwitchFailure(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetModelSwitcher(
		func(id string) error { return errors.New("unknown model") },
		func() string { return "test" },
	)

	Command{Name: "model", Args: []string{"bogus/nope"}}.runOn(p)

	found := false
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice && strings.Contains(entry.Notice, "unknown model") {
			found = true
		}
	}
	if !found {
		t.Errorf("the failure was not reported: %+v", p.state.Entries)
	}
}

// TestRunCommandWithoutSwitcherDoesNotPanic covers the headless program.
func TestRunCommandWithoutSwitcherDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("/model panicked with no switcher installed: %v", r)
		}
	}()
	p := NewProgram(nil, "test", 0)
	Command{Name: "model", Args: []string{"openai/gpt-5"}}.runOn(p)
}
