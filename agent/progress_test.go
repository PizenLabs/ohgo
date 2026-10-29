package agent

import (
	"context"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"sync"
	"testing"
	"time"
)

// progressTool emits partial results while it runs.
type progressTool struct {
	name    string
	updates []string
	// gate holds the tool open until closed, so a test can watch updates land
	// before the call finishes.
	gate chan struct{}
}

func (t *progressTool) Name() string        { return t.name }
func (t *progressTool) Description() string { return "progress test tool" }
func (t *progressTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (t *progressTool) Approval() Approval            { return Approval{Tier: TierExec} }
func (t *progressTool) Exclusive(map[string]any) bool { return false }

func (t *progressTool) Execute(ctx Context, args map[string]any) Result {
	for _, update := range t.updates {
		if ctx.OnProgress != nil {
			ctx.OnProgress(Result{Text: update})
		}
	}
	if t.gate != nil {
		<-t.gate
	}
	return Result{Text: "done"}
}

func TestLoopReportsProgressInOrder(t *testing.T) {
	// A UI that renders previews needs started, updates, then finished. A
	// missing or out-of-order report shows a blank command or stale output.
	tool := &progressTool{name: "bash", updates: []string{"line1", "line2"}}
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{}}},
		StopReason: ai.StopToolUse,
	}
	final := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "ok"}},
		StopReason: ai.StopEndTurn,
	}
	streamFn, _ := scriptedStream(first, final)

	var (
		mu       sync.Mutex
		progress []ToolProgress
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
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

	mu.Lock()
	defer mu.Unlock()
	if len(progress) == 0 {
		t.Fatal("no progress reports were emitted")
	}
	if progress[0].Final {
		t.Error("the first report was final; the call should be announced before it runs")
	}
	if !progress[len(progress)-1].Final {
		t.Error("the last report was not final")
	}
	// The final report must carry the outcome, or a UI would show an empty
	// result for a tool that succeeded.
	last := progress[len(progress)-1]
	if last.Result.Text != "done" {
		t.Errorf("final report text = %q, want the tool's result", last.Result.Text)
	}
}

func TestLoopProgressDeliveredWhileToolRuns(t *testing.T) {
	// This is the whole point of the feature: a preview must arrive *before*
	// the command exits, or a slow tool still looks frozen.
	tool := &progressTool{name: "bash", updates: []string{"partial output"}, gate: make(chan struct{})}
	streamFn, _ := scriptedStream(
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{}}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "ok"}},
			StopReason: ai.StopEndTurn,
		},
	)

	// Only reports carrying output are previews: the announcement that the
	// call started is intentionally empty.
	previews := make(chan ToolProgress, 8)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		OnToolProgress: func(p ToolProgress) {
			if !p.Final && p.Result.Text != "" {
				previews <- p
			}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
		done <- err
	}()

	select {
	case p := <-previews:
		if p.Result.Text != "partial output" {
			t.Errorf("preview text = %q, want the tool's partial output", p.Result.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no preview arrived while the tool was still running")
	}

	// Let the tool finish and confirm the run completes.
	close(tool.gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the run did not finish after the tool returned")
	}
}

func TestLoopProgressSeesAfterToolCallOverride(t *testing.T) {
	// The final report must reflect what the model will actually receive, not
	// the raw tool output, or a UI shows a result that never reached the model.
	tool := &progressTool{name: "bash"}
	replacement := "rewritten by the host"
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{}}},
		StopReason: ai.StopToolUse,
	}
	final := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "ok"}},
		StopReason: ai.StopEndTurn,
	}
	streamFn, _ := scriptedStream(first, final)

	var (
		mu   sync.Mutex
		last ToolProgress
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		AfterToolCall: func(ctx AfterToolCallContext) AfterToolCallResult {
			return AfterToolCallResult{Text: &replacement}
		},
		OnToolProgress: func(p ToolProgress) {
			mu.Lock()
			last = p
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !last.Final {
		t.Fatal("the last report was not final")
	}
	if last.Result.Text != replacement {
		t.Errorf("final report text = %q, want the overridden %q", last.Result.Text, replacement)
	}
}

func TestToolProgressStage(t *testing.T) {
	// A consumer switching on stage must not have to infer it from emptiness.
	// The pre-Execute announcement is an empty, non-final report, so it
	// stages as started; a partial outcome stages as an update.
	if got := (ToolProgress{}).Stage(); got != ProgressStarted {
		t.Errorf("an empty report staged as %q, want started", got)
	}
	if got := (ToolProgress{Result: Result{Text: "partial"}}).Stage(); got != ProgressUpdate {
		t.Errorf("a partial report staged as %q, want update", got)
	}
	if got := (ToolProgress{Result: Result{Details: struct{}{}}}).Stage(); got != ProgressUpdate {
		t.Errorf("a structured partial staged as %q, want update", got)
	}
	if got := (ToolProgress{Final: true}).Stage(); got != ProgressFinished {
		t.Errorf("a final report staged as %q, want finished", got)
	}
}

func TestLoopWithoutProgressHookStillWorks(t *testing.T) {
	// The hook is optional: a caller with no UI must not break, and a tool that
	// reports progress must not panic on a nil callback.
	tool := &progressTool{name: "bash", updates: []string{"x"}}
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{}}},
		StopReason: ai.StopToolUse,
	}
	final := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "ok"}},
		StopReason: ai.StopEndTurn,
	}
	streamFn, _ := scriptedStream(first, final)

	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
