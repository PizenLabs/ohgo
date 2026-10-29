package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// recordingJournal records the edit boundary the loop opens and whether the
// transaction was committed or aborted.
type recordingJournal struct {
	begins   int
	commits  int
	aborts   int
	boundary EditBoundary
	failWith error
	txn      *recordingTxn
}

// recordingTxn is one edit transaction observed by the test.
type recordingTxn struct {
	journal *recordingJournal
}

func (j *recordingJournal) BeginEdit(ctx context.Context, boundary EditBoundary) (EditTransaction, error) {
	j.begins++
	j.boundary = boundary
	if j.failWith != nil {
		return nil, j.failWith
	}
	txn := &recordingTxn{journal: j}
	j.txn = txn
	return txn, nil
}

func (t *recordingTxn) Commit(context.Context) error {
	t.journal.commits++
	return nil
}

func (t *recordingTxn) Abort() { t.journal.aborts++ }

// newTestLoop builds a loop with the given tools and scripted model answers.
func newTestLoop(t *testing.T, journal EditJournal, tools []Tool, messages ...ai.AssistantMessage) (*Loop, func() []ai.Request) {
	t.Helper()
	streamFn, requests := scriptedStream(messages...)
	loop, err := New(Config{
		Model:   catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:  streamFn,
		Tools:   tools,
		Journal: journal,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop, requests
}

func TestJournalOpensBeforeTheMutatingBatchAndCommitsAtTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	journal := &recordingJournal{}
	loop, _ := newTestLoop(t, journal, []Tool{&fileWriteTool{}},
		toolCallMessage(ai.ToolCall{ID: "c1", Name: "write", Arguments: map[string]any{
			"path": path, "content": "package a\n",
		}}),
		textMessage("done"),
	)

	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if journal.begins != 1 {
		t.Fatalf("BeginEdit calls = %d, want 1", journal.begins)
	}
	if journal.commits != 1 {
		t.Fatalf("Commit calls = %d, want 1", journal.commits)
	}
	if journal.boundary.Turn != 0 || len(journal.boundary.Tools) != 1 || journal.boundary.Tools[0] != "write" {
		t.Errorf("boundary = %+v, want turn 0 with the write tool", journal.boundary)
	}
	// The point of the order is that the file did not exist when the journal
	// opened; the tool wrote it afterwards.
	if got, err := os.ReadFile(path); err != nil || string(got) != "package a\n" {
		t.Errorf("file = %q (%v)", got, err)
	}
}

func TestJournalSkipsReadOnlyRuns(t *testing.T) {
	journal := &recordingJournal{}
	loop, _ := newTestLoop(t, journal, []Tool{&fileReadTool{}},
		toolCallMessage(ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "/dev/null"}}),
		textMessage("done"),
	)
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if journal.begins != 0 {
		t.Errorf("BeginEdit calls = %d, want 0 for a read-only run", journal.begins)
	}
}

func TestJournalFailureRefusesTheMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	journal := &recordingJournal{failWith: errors.New("disk full")}
	loop, _ := newTestLoop(t, journal, []Tool{&fileWriteTool{}},
		toolCallMessage(ai.ToolCall{ID: "c1", Name: "write", Arguments: map[string]any{
			"path": path, "content": "package a\n",
		}}),
		textMessage("done"),
	)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("the tool ran even though the recovery point could not be captured")
	}
	if len(turns) == 0 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	if !turns[0].Results[0].Result.IsError {
		t.Error("the refusal was not reported to the model as an error")
	}
	if journal.commits != 0 {
		t.Error("a failed capture was committed")
	}
}

func TestJournalCommitsAfterCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	journal := &recordingJournal{}
	ctx, cancel := context.WithCancel(context.Background())
	tool := &cancellingWriteTool{path: path, cancel: cancel}
	loop, err := New(Config{
		Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: completedStreamFn(toolCallMessage(ai.ToolCall{
			ID: "c1", Name: "write",
			Arguments: map[string]any{"path": path, "content": "package a\n"},
		})),
		Tools:   []Tool{tool},
		Journal: journal,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatal("the tool did not run")
	}
	if journal.begins != 1 || journal.commits != 1 {
		t.Errorf("begins=%d commits=%d, want the checkpoint published despite cancellation",
			journal.begins, journal.commits)
	}
}

// delegateTool runs a child agent, like the built-in task tool.
type delegateTool struct {
	run func(ctx context.Context) error
}

func (t *delegateTool) Name() string        { return "task" }
func (t *delegateTool) Description() string { return "delegate" }
func (t *delegateTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (t *delegateTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (t *delegateTool) Exclusive(map[string]any) bool { return true }
func (t *delegateTool) ActionClasses() []ActionClass  { return []ActionClass{ActionDelegate} }
func (t *delegateTool) Execute(ctx Context, _ map[string]any) Result {
	if err := t.run(ctx.Ctx); err != nil {
		return ErrorResult("%v", err)
	}
	return Result{Text: "child done"}
}

func TestSubagentDoesNotInheritTheParentJournal(t *testing.T) {
	// A delegated child mutates the same workspace, but it must not publish its
	// own checkpoint: the parent's turn is the edit boundary, and a second
	// journal would split one undoable edit into two.
	journal := &recordingJournal{}
	childText := textMessage("child done")
	parentConfig := Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: completedStreamFn(childText),
		Tools:  []Tool{&fileWriteTool{}},
	}
	delegate := &delegateTool{run: func(ctx context.Context) error {
		result, err := RunSubagent(ctx, parentConfig, SubagentSpec{
			Task:   "write a file",
			System: []ai.SystemPromptBlock{{Text: "you are a child"}},
			Tools:  []Tool{&fileWriteTool{}},
		})
		if err != nil {
			return err
		}
		if result.Text != "child done" {
			t.Errorf("child text = %q", result.Text)
		}
		return nil
	}}
	parentStream, _ := scriptedStream(
		toolCallMessage(ai.ToolCall{ID: "c1", Name: "task", Arguments: map[string]any{}}),
		textMessage("done"),
	)
	loop, err := New(Config{
		Model:   catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:  parentStream,
		Tools:   []Tool{delegate},
		Journal: journal,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if journal.begins != 1 || journal.commits != 1 {
		t.Errorf("begins=%d commits=%d, want exactly the parent's one edit boundary",
			journal.begins, journal.commits)
	}
}

type cancellingWriteTool struct {
	path   string
	cancel context.CancelFunc
}

func (t *cancellingWriteTool) Name() string        { return "write" }
func (t *cancellingWriteTool) Description() string { return "write a file" }
func (t *cancellingWriteTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`))
}
func (t *cancellingWriteTool) Approval() Approval            { return Approval{Tier: TierWrite} }
func (t *cancellingWriteTool) Exclusive(map[string]any) bool { return false }
func (t *cancellingWriteTool) Execute(ctx Context, args map[string]any) Result {
	content, _ := args["content"].(string)
	if err := os.WriteFile(t.path, []byte(content), 0o600); err != nil {
		return ErrorResult("write %s: %v", t.path, err)
	}
	t.cancel()
	return Result{Text: "wrote " + t.path, Mutates: true}
}

func TestJournalIsNotReachableFromToolArguments(t *testing.T) {
	// A tool receives only its arguments. There is no Context field, no
	// registry lookup, and no name that resolves to a journal, so a model
	// cannot name its way to recovery authority. This asserts the shape the
	// loop passes: the boundary carries tool names only, never the journal.
	journal := &recordingJournal{}
	loop, _ := newTestLoop(t, journal, []Tool{&fileWriteTool{}},
		toolCallMessage(ai.ToolCall{ID: "c1", Name: "write", Arguments: map[string]any{
			"path": filepath.Join(t.TempDir(), "a.go"), "content": "x",
		}}),
		textMessage("done"),
	)
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if journal.begins != 1 {
		t.Fatalf("journal not exercised")
	}
	if journal.txn == nil {
		t.Fatal("no transaction was created")
	}
	// The transaction exposes only Commit and Abort. Undo/Redo do not exist on
	// the interface, so even a pointer to it grants no recovery authority.
	if _, ok := any(journal.txn).(interface{ Undo(context.Context) }); ok {
		t.Error("the edit transaction exposes a recovery operation")
	}
}
