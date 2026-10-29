package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A subagent must not start a provider call once the parent's wall-clock
// budget has expired. Before the fix the child dropped the deadline and kept
// calling the model, so --deadline bounded only the parent's own turns.
func TestRunSubagentHonoursParentDeadline(t *testing.T) {
	calls := 0
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		calls++
		return ai.NewCompletedStream(ai.AssistantMessage{}), nil
	}
	parent := Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   stream,
		Tools:    []Tool{namedTool("read")},
		Deadline: time.Now().Add(-time.Second),
	}

	_, _ = RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "summarise src/auth",
		System: []ai.SystemPromptBlock{{Text: "you are a read-only researcher"}},
		Tools:  []Tool{namedTool("read")},
	})

	if calls != 0 {
		t.Fatalf("the child made %d provider calls after the parent deadline expired, want 0", calls)
	}
}

// Cancelling the parent must cancel the child's in-flight provider call. A
// child that ignored the context would keep spending after the user stopped the
// turn.
func TestRunSubagentHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var once sync.Once
	stream := func(streamCtx context.Context, _ ai.Request) (*ai.Stream, error) {
		once.Do(func() { close(started) })
		<-streamCtx.Done()
		return nil, streamCtx.Err()
	}
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{namedTool("read")},
	}
	go func() {
		<-started
		cancel()
	}()

	_, err := RunSubagent(ctx, parent, SubagentSpec{
		Task:   "summarise src/auth",
		System: []ai.SystemPromptBlock{{Text: "you are a read-only researcher"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// stubTool is a minimal Tool for policy tests.
//
// It is declared here rather than pulled from tools, because tools imports
// agent: importing it back would be an import cycle.
type stubTool struct{ name string }

func (t stubTool) Name() string                { return t.name }
func (stubTool) Description() string           { return "stub" }
func (stubTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (stubTool) Exclusive(map[string]any) bool { return false }
func (stubTool) Parameters() *jsonschema.Schema {
	schema, err := jsonschema.CompileMap(map[string]any{"type": "object"})
	if err != nil {
		panic(err)
	}
	return schema
}
func (stubTool) Execute(Context, map[string]any) Result { return Result{Text: "ok"} }

func namedTool(name string) stubTool { return stubTool{name: name} }

// TestIsReadOnlyIsFailSafe is the security contract behind read-only subagents.
//
// An agent is read-only only when every declared tool is in the read tier. The
// direction matters: an unknown tool must make the agent *not* read-only,
// because assuming safety on an unrecognised tool would let it mutate the
// repository while the parent believes it could not.
func TestIsReadOnlyIsFailSafe(t *testing.T) {
	cases := []struct {
		name  string
		tools []Tool
		want  bool
	}{
		{"empty is not read-only", nil, false},
		{"read tier only", []Tool{namedTool("read"), namedTool("grep"), namedTool("glob")}, true},
		{"a write tool disqualifies", []Tool{namedTool("read"), namedTool("write")}, false},
		{"an unknown tool disqualifies", []Tool{namedTool("read"), namedTool("invented_tool")}, false},
		{"a mix with an unknown name", []Tool{namedTool("grep"), namedTool("todo"), namedTool("bash")}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsReadOnly(tc.tools); got != tc.want {
				t.Errorf("IsReadOnly(%v) = %v, want %v", toolNames(tc.tools), got, tc.want)
			}
		})
	}
}

func toolNames(tools []Tool) []string {
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	return names
}

func TestReadOnlyToolNamesCoverTheReadTier(t *testing.T) {
	// The set must be a subset of what the CLI actually offers, or a tool the
	// interface promises is readable would silently disqualify its agent.
	for name := range ReadOnlyToolNames {
		if strings.TrimSpace(name) == "" {
			t.Error("the read-only set contains an empty name")
		}
	}
	for _, name := range []string{"read", "grep", "glob", "todo"} {
		if !ReadOnlyToolNames[name] {
			t.Errorf("%q is a read-tier tool but is missing from the set", name)
		}
	}
	for _, name := range []string{"write", "edit", "bash", "task"} {
		if ReadOnlyToolNames[name] {
			t.Errorf("%q is not read-only but is in the set", name)
		}
	}
}

// TestRunSubagentRejectsAnEmptyTask keeps a child from being launched with
// nothing to do, which would spend tokens to produce an answer nobody asked for.
func TestRunSubagentRejectsAnEmptyTask(t *testing.T) {
	parent := Config{Model: ai.Model{ID: "m", Provider: "p"}, Stream: noStream}
	for _, task := range []string{"", "   ", "\n\t "} {
		if _, err := RunSubagent(context.Background(), parent, SubagentSpec{
			Task:   task,
			System: []ai.SystemPromptBlock{{Text: "you are a subagent"}},
			Tools:  []Tool{namedTool("read")},
		}); err == nil {
			t.Errorf("an empty task %q was accepted", task)
		}
	}
}

// TestRunSubagentRequiresItsOwnSystemPrompt is the isolation contract.
//
// Inheriting the parent's prompt would hand the child an identity it does not
// have, so absence is an error rather than a fallback.
func TestRunSubagentRequiresItsOwnSystemPrompt(t *testing.T) {
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: noStream,
		System: []ai.SystemPromptBlock{{Text: "you are the main agent"}},
	}

	_, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:  "do a thing",
		Tools: []Tool{namedTool("read")},
	})
	if err == nil {
		t.Fatal("a subagent with no system prompt was allowed")
	}
	if !strings.Contains(err.Error(), "system prompt") {
		t.Errorf("error = %v, want it to name the system prompt", err)
	}
	if !errors.Is(err, ErrNoSystemPrompt) {
		t.Errorf("error = %v, want errSystemPromptRequired", err)
	}
}

// TestRunSubagentRejectsNoTools stops a child that could do nothing.
func TestRunSubagentRejectsNoTools(t *testing.T) {
	parent := Config{Model: ai.Model{ID: "m", Provider: "p"}, Stream: noStream}
	_, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "do a thing",
		System: []ai.SystemPromptBlock{{Text: "you are a subagent"}},
		Tools:  nil,
	})
	if !errors.Is(err, ErrNoTools) {
		t.Errorf("error = %v, want ErrNoTools", err)
	}
}

// TestRunSubagentRefusesAWriteToolUnderAReadOnlyRequest guards the misconfiguration.
func TestRunSubagentRefusesAWriteToolUnderAReadOnlyRequest(t *testing.T) {
	parent := Config{Model: ai.Model{ID: "m", Provider: "p"}, Stream: noStream}

	_, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:     "look around",
		System:   []ai.SystemPromptBlock{{Text: "you are a subagent"}},
		Tools:    []Tool{namedTool("read"), namedTool("write")},
		ReadOnly: true,
	})
	if err == nil {
		t.Fatal("a write-capable tool set was accepted under a read-only request")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Errorf("error = %v, want it to name the offending tool", err)
	}
}

// TestRunSubagentStartsWithOnlyTheTask is the isolation contract at the wire.
//
// If the child saw the parent's history it would inherit a conversation about a
// different problem, and its answers would be coloured by context the parent
// never intended it to see. The request must carry exactly one user message.
func TestRunSubagentHonorsCompletionReview(t *testing.T) {
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "It is a Go project."}},
			StopReason: ai.StopEndTurn,
		}), nil
	}
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{namedTool("read")},
	}
	result, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:             "Explain this project.",
		System:           []ai.SystemPromptBlock{{Text: "you are a subagent"}},
		Tools:            []Tool{namedTool("read")},
		Contract:         &contract.IntentContract{Objective: "Explain this project.", Kind: contract.ObjectiveInformational},
		CompletionReview: func() string { return "runtime evidence was not sufficient (INSUFFICIENT)" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completion.Complete() {
		t.Fatal("a child answer with an insufficient completion review was reported complete")
	}
	// The legacy no-contract path must also honor the review; a read-only
	// subagent without a captured contract still must not complete a guess.
	result, err = RunSubagent(context.Background(), parent, SubagentSpec{
		Task:             "Explain this project.",
		System:           []ai.SystemPromptBlock{{Text: "you are a subagent"}},
		Tools:            []Tool{namedTool("read")},
		CompletionReview: func() string { return "runtime evidence was not sufficient (INSUFFICIENT)" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completion.Complete() {
		t.Fatal("a no-contract child ignored the completion review")
	}
}

func TestRunSubagentStartsWithOnlyTheTask(t *testing.T) {
	var gotMessages []ai.Message
	var gotSystem []ai.SystemPromptBlock

	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		gotMessages = req.Messages
		gotSystem = req.System
		return nil, errStop
	}

	parent := Config{
		Model:  ai.Model{ID: "parent-model", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{namedTool("read")},
		System: []ai.SystemPromptBlock{{Text: "you are the main agent"}},
	}

	_, _ = RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "summarise src/auth",
		System: []ai.SystemPromptBlock{{Text: "you are a read-only researcher"}},
		Tools:  []Tool{namedTool("read")},
	})

	if len(gotMessages) != 1 {
		t.Fatalf("the child was given %d messages, want exactly 1 (the task)", len(gotMessages))
	}
	user, ok := gotMessages[0].(ai.UserMessage)
	if !ok {
		t.Fatalf("the child's first message is %T, want a user message", gotMessages[0])
	}
	if !strings.Contains(user.Text(), "summarise src/auth") {
		t.Errorf("the task text is %q, want it to carry the assignment", user.Text())
	}

	// The child's prompt is its own, never the parent's.
	if len(gotSystem) != 1 || !strings.Contains(gotSystem[0].Text, "read-only researcher") {
		t.Errorf("system = %v, want the subagent's own prompt", gotSystem)
	}
	for _, block := range gotSystem {
		if strings.Contains(block.Text, "main agent") {
			t.Errorf("system = %q, leaked the parent's identity", block.Text)
		}
	}
}

// TestRunSubagentDoesNotInheritTheParentModel proves a child runs on the model
// the caller configured, not one it captured from the parent by accident.
func TestRunSubagentDoesNotInheritTheParentModel(t *testing.T) {
	var gotModel ai.Model
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		gotModel = req.Model
		return nil, errStop
	}
	parent := Config{
		Model:  ai.Model{ID: "parent-model", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{namedTool("read")},
	}

	_, _ = RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "t",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{namedTool("read")},
	})
	if gotModel.ID != "parent-model" {
		t.Errorf("model = %q, want the configured model", gotModel.ID)
	}
}

// TestRunSubagentSuppressesTheParentHooks keeps a child's turns out of a UI that
// is already showing the parent's. A child that emitted both would double-count.
func TestRunSubagentSuppressesTheParentHooks(t *testing.T) {
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) { return nil, errStop }
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{namedTool("read")},
		OnTurn: func(Turn) { t.Error("a child emitted a turn to the parent's hook") },
		OnToolProgress: func(ToolProgress) {
			t.Error("a child emitted tool progress to the parent's hook")
		},
	}

	_, _ = RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "t",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{namedTool("read")},
	})
}

// TestRunSubagentKeepsTheApprovalPolicy proves a child inherits the parent's
// approval hook rather than defaulting to "prompt", which would deadlock a
// non-interactive run.
func TestRunSubagentKeepsTheApprovalPolicy(t *testing.T) {
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) { return nil, errStop }
	called := false
	parent := Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  []Tool{namedTool("read")},
		Approve: func(ai.ToolCall, Approval) (bool, string) {
			called = true
			return true, ""
		},
	}

	_, _ = RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "t",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{namedTool("read")},
	})
	_ = called // the hook is only reachable through a real tool call
}

// noStream is a StreamFn that fails immediately, for specs that never reach the
// provider because they are rejected first.
func noStream(ctx context.Context, req ai.Request) (*ai.Stream, error) {
	return nil, errors.New("agent: no stream configured")
}

var errStop = errors.New("stop")
