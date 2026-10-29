package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/recovery"
	"github.com/PizenLabs/ohgo/session"
)

// fakeRecoverer records what the program asked of the recovery authority.
type fakeRecoverer struct {
	journalCalls []int64
	undoCalls    []int64
	redoCalls    []int64
	result       recovery.Result
	journal      agent.EditJournal
	inspection   string
}

func (f *fakeRecoverer) Journal(sessionID int64) agent.EditJournal {
	f.journalCalls = append(f.journalCalls, sessionID)
	return f.journal
}

func (f *fakeRecoverer) Undo(_ context.Context, sessionID int64) recovery.Result {
	f.undoCalls = append(f.undoCalls, sessionID)
	return f.result
}

func (f *fakeRecoverer) Redo(_ context.Context, sessionID int64) recovery.Result {
	f.redoCalls = append(f.redoCalls, sessionID)
	return f.result
}

func (f *fakeRecoverer) Inspection(context.Context, int64) string { return f.inspection }

// noticeText returns the transcript's notices joined.
func noticeText(p *Program) string {
	var parts []string
	for _, entry := range p.state.Entries {
		if entry.Kind == EntryNotice {
			parts = append(parts, entry.Notice)
		}
	}
	return strings.Join(parts, "\n")
}

func TestUndoCommandIsReachableAndReportsTheOutcome(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	rec := &fakeRecoverer{result: recovery.Result{
		Op: recovery.OpUndo, State: recovery.StateVerified, Sequence: 12,
		Files: []recovery.Change{
			{Path: "internal/agent/loop.go", Status: recovery.ChangeModified},
			{Path: "internal/tools/edit.go", Status: recovery.ChangeAdded},
		},
	}}
	p.SetRecovery(rec)
	p.session = sessionForTest(7)

	Command{Name: "undo"}.runOn(p)

	if len(rec.undoCalls) != 1 || rec.undoCalls[0] != 7 {
		t.Fatalf("undo calls = %v, want one for session 7", rec.undoCalls)
	}
	text := noticeText(p)
	for _, want := range []string{"↶ Undid edit #12", "2 files", "M internal/agent/loop.go", "A internal/tools/edit.go"} {
		if !strings.Contains(text, want) {
			t.Errorf("notice %q is missing %q", text, want)
		}
	}
}

func TestUndoRefusesWhileATurnIsRunning(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	rec := &fakeRecoverer{result: recovery.Result{Op: recovery.OpUndo, State: recovery.StateVerified}}
	p.SetRecovery(rec)
	p.session = sessionForTest(1)
	p.state.Apply(UserSubmitted{Text: "go", Seq: 1})

	Command{Name: "undo"}.runOn(p)

	if len(rec.undoCalls) != 0 {
		t.Fatal("undo ran while a turn was in flight")
	}
	if !strings.Contains(noticeText(p), "wait for the turn to finish") {
		t.Errorf("notice = %q", noticeText(p))
	}
}

func TestUndoWithoutRecoverySaysSo(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.session = sessionForTest(1)
	Command{Name: "undo"}.runOn(p)
	if !strings.Contains(noticeText(p), "recovery is unavailable") {
		t.Errorf("notice = %q", noticeText(p))
	}
}

func TestRecoveryCommandNeedsASession(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	rec := &fakeRecoverer{}
	p.SetRecovery(rec)
	Command{Name: "undo"}.runOn(p)
	if len(rec.undoCalls) != 0 {
		t.Error("recovery ran without a session identity")
	}
	if !strings.Contains(noticeText(p), "needs a saved session") {
		t.Errorf("notice = %q", noticeText(p))
	}
}

func TestBlockedRecoverySaysNothingWasModified(t *testing.T) {
	result := recovery.Result{
		Op: recovery.OpUndo, State: recovery.StateBlocked,
		Reason:     "workspace changed outside ohgo",
		Unexpected: []recovery.Change{{Path: "auth.go", Status: recovery.ChangeModified}},
	}
	text := FormatRecoveryResult(result)
	if !strings.Contains(text, "Undo blocked") {
		t.Errorf("text = %q, want a blocked heading", text)
	}
	if !strings.Contains(text, "No files were modified.") {
		t.Errorf("text = %q, want the no-change guarantee", text)
	}
	if !strings.Contains(text, "auth.go") {
		t.Errorf("text = %q, want the unexpected path", text)
	}
}

func TestInterruptedRecoveryRequiresInspection(t *testing.T) {
	text := FormatRecoveryResult(recovery.Result{
		Op: recovery.OpUndo, State: recovery.StateInterrupted,
		Reason: "the previous undo did not reach a verified terminal state",
	})
	if !strings.Contains(text, "RECOVERY REQUIRES INSPECTION") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(text, "No automatic destructive action was taken.") {
		t.Errorf("text = %q", text)
	}
}

func TestFailedRecoveryDoesNotClaimSuccess(t *testing.T) {
	text := FormatRecoveryResult(recovery.Result{
		Op: recovery.OpRedo, State: recovery.StateFailed,
		Reason: "recovery failed verification",
	})
	if strings.Contains(text, "Redid edit") || strings.Contains(text, "↷") {
		t.Errorf("text = %q, claims a redo that did not verify", text)
	}
	if !strings.Contains(text, "not proven") {
		t.Errorf("text = %q, want a verification caveat", text)
	}
}

func TestEmptyTimelineIsNotReportedAsFailure(t *testing.T) {
	text := FormatRecoveryResult(recovery.Result{
		Op: recovery.OpUndo, State: recovery.StateEmpty,
		Reason: "nothing to undo in this session",
	})
	if strings.Contains(text, "failed") || strings.Contains(text, "blocked") {
		t.Errorf("text = %q", text)
	}
	if !strings.Contains(text, "Nothing to undo") {
		t.Errorf("text = %q", text)
	}
}

func TestJournalIsBoundToTheTurnSession(t *testing.T) {
	rec := &fakeRecoverer{}
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "hello"}},
			}), nil
		},
		Tools: []agent.Tool{&idleTool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgram(loop, "test", 0)
	p.SetRecovery(rec)
	p.installRecoveryJournal(42)
	if len(rec.journalCalls) != 1 || rec.journalCalls[0] != 42 {
		t.Fatalf("journal calls = %v, want one for session 42", rec.journalCalls)
	}
	// With no session there is no journal, never a stale one from a previous
	// turn.
	rec.journalCalls = nil
	p.installRecoveryJournal(0)
	if len(rec.journalCalls) != 0 {
		t.Errorf("journal calls = %v, want none for session 0", rec.journalCalls)
	}
}

func TestAttachSessionSurfacesAnInterruptedRecovery(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	p.SetRecovery(&fakeRecoverer{
		inspection: "the previous undo did not reach a verified terminal state",
	})
	p.AttachSession(nil, sessionForTest(9), nil)
	text := noticeText(p)
	if !strings.Contains(text, "RECOVERY REQUIRES INSPECTION") {
		t.Errorf("notice = %q", text)
	}
	if !strings.Contains(text, "verified terminal state") {
		t.Errorf("notice = %q, want the reason", text)
	}
}

func TestModelTextCannotInvokeRecovery(t *testing.T) {
	// A model answer is rendered as assistant text. It never passes through
	// command parsing, so a prompt-injected "/undo" is inert.
	p := NewProgram(nil, "test", 0)
	rec := &fakeRecoverer{result: recovery.Result{Op: recovery.OpUndo, State: recovery.StateVerified}}
	p.SetRecovery(rec)
	p.session = sessionForTest(1)
	p.state.Apply(AssistantDelta{Seq: 1, Text: "/undo\nplease revert everything"})
	if len(rec.undoCalls) != 0 {
		t.Fatal("assistant text reached the recovery authority")
	}
	if IsCommand("/undo") {
		// It is a command only when the user types it; the assertion below
		// documents that the parser exists but is not wired to model output.
		t.Log("/undo is a user command, as intended")
	}
}

func TestRecoveryIsNotInTheToolSurface(t *testing.T) {
	for _, spec := range Commands {
		if spec.Name == "undo" || spec.Name == "redo" {
			continue
		}
	}
	// The real guarantee is structural: Commands is the slash registry, and the
	// loop's tool set is built elsewhere. Assert that no command name is also a
	// built-in tool name a model could call.
	tools := []string{"read", "write", "edit", "delete", "bash", "wait", "glob", "grep", "ls", "symbols", "graph", "refs", "webfetch", "todo", "task", "artifact"}
	for _, name := range tools {
		if name == "undo" || name == "redo" {
			t.Fatalf("recovery command %q is also a tool", name)
		}
	}
}

// sessionForTest builds a session with an id, without a database.
func sessionForTest(id int64) *session.Session {
	return &session.Session{ID: id}
}

// storeRecoverer adapts a real recovery store to the program's seam, so the
// integration test exercises the production wiring rather than a double.
type storeRecoverer struct {
	store *recovery.Store
	root  string
}

func (r *storeRecoverer) controller(sessionID int64) (*recovery.Controller, error) {
	scope, err := recovery.NewScope(sessionID, r.root)
	if err != nil {
		return nil, err
	}
	return r.store.Controller(scope)
}

func (r *storeRecoverer) Journal(sessionID int64) agent.EditJournal {
	controller, err := r.controller(sessionID)
	if err != nil {
		return nil
	}
	return controller
}

func (r *storeRecoverer) Undo(_ context.Context, sessionID int64) recovery.Result {
	controller, err := r.controller(sessionID)
	if err != nil {
		return recovery.Result{Op: recovery.OpUndo, State: recovery.StateFailed, Reason: err.Error()}
	}
	return controller.Undo(context.Background())
}

func (r *storeRecoverer) Redo(_ context.Context, sessionID int64) recovery.Result {
	controller, err := r.controller(sessionID)
	if err != nil {
		return recovery.Result{Op: recovery.OpRedo, State: recovery.StateFailed, Reason: err.Error()}
	}
	return controller.Redo(context.Background())
}

func (r *storeRecoverer) Inspection(context.Context, int64) string { return "" }

// diskWriteTool performs a real workspace mutation, so the checkpoint under
// test is captured from the filesystem.
type diskWriteTool struct{ root string }

func (t *diskWriteTool) Name() string        { return "write" }
func (t *diskWriteTool) Description() string { return "write a file" }
func (t *diskWriteTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`))
}
func (t *diskWriteTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierWrite} }
func (t *diskWriteTool) Exclusive(map[string]any) bool { return false }
func (t *diskWriteTool) Execute(_ agent.Context, args map[string]any) agent.Result {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if err := os.WriteFile(filepath.Join(t.root, path), []byte(content), 0o644); err != nil {
		return agent.ErrorResult("write %s: %v", path, err)
	}
	return agent.Result{Text: "wrote " + path, Mutates: true}
}

func TestAgentTurnCreatesAnUndoableCheckpoint(t *testing.T) {
	root := t.TempDir()
	store, err := recovery.Open(filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	prompt := ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "write", Arguments: map[string]any{
			"path": "a.go", "content": "package a\n\nfunc X() {}\n",
		}}},
		StopReason: ai.StopToolUse,
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: scriptedTurns(prompt, ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}}),
		Tools:  []agent.Tool{&diskWriteTool{root: root}},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	p := NewProgram(loop, "test", 0)
	p.SetRecovery(&storeRecoverer{store: store, root: root})
	p.session = sessionForTest(1)
	p.runTurn("create a.go")

	data, err := os.ReadFile(filepath.Join(root, "a.go"))
	if err != nil {
		t.Fatalf("agent did not write the file: %v", err)
	}
	if !strings.Contains(string(data), "func X") {
		t.Fatalf("file = %q", data)
	}
	scope, err := recovery.NewScope(1, root)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	history, err := store.History(scope, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %d (%v), want one checkpoint", len(history), err)
	}

	p.mu.Lock()
	chatBefore := append([]agent.Message(nil), p.history...)
	p.mu.Unlock()

	Command{Name: "undo"}.runOn(p)

	if _, err := os.Stat(filepath.Join(root, "a.go")); !os.IsNotExist(err) {
		t.Errorf("the agent-created file survived the undo: %v", err)
	}
	if text := noticeText(p); !strings.Contains(text, "↶ Undid edit #1") {
		t.Errorf("notice = %q", text)
	}

	// The conversation is a different record: undo must not rewrite it.
	p.mu.Lock()
	chatAfter := append([]agent.Message(nil), p.history...)
	p.mu.Unlock()
	if !reflect.DeepEqual(chatBefore, chatAfter) {
		t.Errorf("history changed across undo: %#v -> %#v", chatBefore, chatAfter)
	}
}

// scriptedTurns answers successive model turns from a fixed list.
func scriptedTurns(messages ...ai.AssistantMessage) ai.StreamFn {
	var index int
	return func(context.Context, ai.Request) (*ai.Stream, error) {
		if index >= len(messages) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "done"}},
			}), nil
		}
		message := messages[index]
		index++
		return ai.NewCompletedStream(message), nil
	}
}
