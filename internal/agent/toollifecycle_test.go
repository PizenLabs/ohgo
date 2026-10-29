package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// fileReadTool reads a real file from disk: the lifecycle under test runs
// through the genuine execute → Execute path, not around it.
type fileReadTool struct{}

func (t *fileReadTool) Name() string        { return "read" }
func (t *fileReadTool) Description() string { return "read a file" }
func (t *fileReadTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`))
}
func (t *fileReadTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (t *fileReadTool) Exclusive(map[string]any) bool { return false }
func (t *fileReadTool) Execute(ctx Context, args map[string]any) Result {
	path, _ := args["path"].(string)
	data, err := os.ReadFile(path)
	if err != nil {
		return ErrorResult("read %s: %v", path, err)
	}
	return Result{Text: string(data)}
}

// fileWriteTool creates a real file on disk.
type fileWriteTool struct{}

func (t *fileWriteTool) Name() string        { return "write" }
func (t *fileWriteTool) Description() string { return "write a file" }
func (t *fileWriteTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`))
}
func (t *fileWriteTool) Approval() Approval            { return Approval{Tier: TierWrite} }
func (t *fileWriteTool) Exclusive(map[string]any) bool { return false }
func (t *fileWriteTool) Execute(ctx Context, args map[string]any) Result {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return ErrorResult("write %s: %v", path, err)
	}
	return Result{Text: "wrote " + path}
}

// collectProgress runs one scripted turn and records every progress report.
func collectProgress(t *testing.T, tools []Tool, messages ...ai.AssistantMessage) []ToolProgress {
	t.Helper()
	streamFn, _ := scriptedStream(messages...)
	var (
		mu       sync.Mutex
		progress []ToolProgress
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  tools,
		OnToolProgress: func(p ToolProgress) {
			mu.Lock()
			progress = append(progress, p)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return progress
}

func toolCallMessage(calls ...ai.ToolCall) ai.AssistantMessage {
	blocks := make([]ai.ContentBlock, 0, len(calls))
	for _, call := range calls {
		blocks = append(blocks, call)
	}
	return ai.AssistantMessage{Content: blocks, StopReason: ai.StopToolUse}
}

func textMessage(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}
}

// A real read of a real file must announce itself before it runs: without
// the start report the UI shows silence while the disk is being read.
func TestLifecyclePublishesStartForRealRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.go")
	if err := os.WriteFile(path, []byte("package auth\n"), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	progress := collectProgress(t, []Tool{&fileReadTool{}},
		toolCallMessage(ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": path}}),
		textMessage("done"))

	if len(progress) == 0 {
		t.Fatal("no progress reports were emitted for a real tool execution")
	}
	first := progress[0]
	if first.Stage() != ProgressStarted {
		t.Errorf("first report staged as %q, want started", first.Stage())
	}
	if first.CallID != "c1" || first.ToolName != "read" {
		t.Errorf("first report = %+v, want CallID c1 tool read", first)
	}
	if first.Args["path"] != path {
		t.Errorf("start args = %v, want the call's path", first.Args)
	}
}

// Start and finish of one invocation share its identity, and the final
// report carries the real outcome the model receives.
func TestLifecycleCorrelatesStartAndFinish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.go")
	if err := os.WriteFile(path, []byte("package auth\n"), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	progress := collectProgress(t, []Tool{&fileReadTool{}},
		toolCallMessage(ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": path}}),
		textMessage("done"))

	for _, p := range progress {
		if p.CallID != "c1" {
			t.Errorf("report CallID = %q, want c1", p.CallID)
		}
	}
	if got := progress[0].Stage(); got != ProgressStarted {
		t.Errorf("first stage = %q, want started", got)
	}
	last := progress[len(progress)-1]
	if got := last.Stage(); got != ProgressFinished {
		t.Errorf("last stage = %q, want finished", got)
	}
	if last.Result.Text != "package auth\n" {
		t.Errorf("final text = %q, want the file contents", last.Result.Text)
	}
	if last.Result.IsError {
		t.Error("a successful read reported as an error")
	}
}

// A deterministic failure — reading a file that does not exist — must
// surface as a failed finish, never as a success.
func TestLifecycleFailureStaysFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.go")
	progress := collectProgress(t, []Tool{&fileReadTool{}},
		toolCallMessage(ai.ToolCall{ID: "c9", Name: "read", Arguments: map[string]any{"path": missing}}),
		textMessage("recovered"))

	last := progress[len(progress)-1]
	if last.Stage() != ProgressFinished {
		t.Fatalf("last stage = %q, want finished", last.Stage())
	}
	if !last.Result.IsError {
		t.Error("a failed read reported success: the UI would render a checkmark over an error")
	}
	if last.Result.Text == "" {
		t.Error("a failure with no text leaves the UI with an error badge and nothing to explain it")
	}
}

// A mutation tool must run the same lifecycle and leave its effect on disk.
func TestLifecycleMutationToolWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	progress := collectProgress(t, []Tool{&fileWriteTool{}},
		toolCallMessage(ai.ToolCall{ID: "c2", Name: "write", Arguments: map[string]any{"path": path, "content": "hello"}}),
		textMessage("done"))

	if progress[0].Stage() != ProgressStarted || progress[len(progress)-1].Stage() != ProgressFinished {
		t.Errorf("stages = %q..%q, want started..finished", progress[0].Stage(), progress[len(progress)-1].Stage())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the tool reported success but the file is missing: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("file = %q, want hello", data)
	}
}

// Two calls in one batch each get their own identity and lifecycle: neither
// result may overwrite the other and turn order must match request order.
func TestLifecycleSequentialToolsStayCorrelated(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("aaa"), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	streamFn, _ := scriptedStream(
		toolCallMessage(
			ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": a}},
			ai.ToolCall{ID: "c2", Name: "write", Arguments: map[string]any{"path": b, "content": "bbb"}},
		),
		textMessage("done"),
	)
	var (
		mu       sync.Mutex
		progress []ToolProgress
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{&fileReadTool{}, &fileWriteTool{}},
		OnToolProgress: func(p ToolProgress) {
			mu.Lock()
			progress = append(progress, p)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	seen := map[string][]ToolProgressStage{}
	for _, p := range progress {
		seen[p.CallID] = append(seen[p.CallID], p.Stage())
	}
	for _, id := range []string{"c1", "c2"} {
		stages := seen[id]
		if len(stages) < 2 || stages[0] != ProgressStarted || stages[len(stages)-1] != ProgressFinished {
			t.Errorf("call %s stages = %v, want started..finished", id, stages)
		}
	}
	if len(turns) == 0 || len(turns[0].Results) != 2 {
		t.Fatalf("results = %v, want one turn with two outcomes", turns)
	}
	if turns[0].Results[0].CallID != "c1" || turns[0].Results[1].CallID != "c2" {
		t.Errorf("result order = %q,%q, want c1,c2",
			turns[0].Results[0].CallID, turns[0].Results[1].CallID)
	}
	if turns[0].Results[0].Result.Text != "aaa" {
		t.Errorf("c1 text = %q, want the first file, not the second call's result", turns[0].Results[0].Result.Text)
	}
}

// A tool turn followed by a text turn must deliver both: the outcome for
// the model and the answer for the user.
func TestLifecycleToolFollowedByAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.go")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	streamFn, _ := scriptedStream(
		toolCallMessage(ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": path}}),
		textMessage("the file holds x"),
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{&fileReadTool{}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2 (tool turn, answer turn)", len(turns))
	}
	if len(turns[0].Results) != 1 || turns[0].Results[0].Result.Text != "x" {
		t.Errorf("tool turn results = %+v, want the file content", turns[0].Results)
	}
	if turns[1].Assistant.Text() != "the file holds x" {
		t.Errorf("answer = %q, want the streamed text", turns[1].Assistant.Text())
	}
}
