package agent

import (
	"context"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"sync"
	"testing"
)

// scriptedTool is a test tool that records its invocations.
type scriptedTool struct {
	name      string
	result    Result
	calls     []map[string]any
	readOnly  []bool
	mu        sync.Mutex
	exclusive bool
}

func newScriptedTool(name, text string) *scriptedTool {
	return &scriptedTool{name: name, result: Result{Text: text}}
}

func (t *scriptedTool) Name() string { return t.name }
func (t *scriptedTool) Description() string {
	return "test tool " + t.name
}
func (t *scriptedTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"value":{"type":"string"}}}`))
}
func (t *scriptedTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (t *scriptedTool) Exclusive(map[string]any) bool { return t.exclusive }
func (t *scriptedTool) Execute(ctx Context, args map[string]any) Result {
	t.mu.Lock()
	t.calls = append(t.calls, args)
	t.readOnly = append(t.readOnly, ctx.ReadOnly)
	t.mu.Unlock()
	return t.result
}

// readOnlyModes returns the authorization mode of every call, in call order.
func (t *scriptedTool) readOnlyModes() []bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]bool(nil), t.readOnly...)
}

// invoked returns the arguments of every call, in call order.
func (t *scriptedTool) invoked() []map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]map[string]any, len(t.calls))
	copy(out, t.calls)
	return out
}

// scriptedStream serves the given assistant messages in order, one per
// request, and records every request it was handed so tests can assert that
// the loop fed tool results back on the following request.
func scriptedStream(messages ...ai.AssistantMessage) (ai.StreamFn, func() []ai.Request) {
	var (
		mu       sync.Mutex
		index    int
		requests []ai.Request
	)
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			mu.Lock()
			requests = append(requests, req)
			position := index
			index++
			mu.Unlock()

			if position >= len(messages) {
				return nil, nil
			}
			return ai.NewCompletedStream(messages[position]), nil
		}, func() []ai.Request {
			mu.Lock()
			defer mu.Unlock()
			out := make([]ai.Request, len(requests))
			copy(out, requests)
			return out
		}
}

// completedStreamFn is a StreamFn that always answers with one fixed message.
func completedStreamFn(msg ai.AssistantMessage) ai.StreamFn {
	return func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		return ai.NewCompletedStream(msg), nil
	}
}

func TestLoopStopsWhenModelMakesNoToolCalls(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	streamFn, requests := scriptedStream(ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
		StopReason: ai.StopEndTurn,
	})

	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "hello"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	if turns[0].Assistant.Text() != "done" {
		t.Errorf("assistant text = %q, want %q", turns[0].Assistant.Text(), "done")
	}
	if got := len(requests()); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
	if got := len(tool.invoked()); got != 0 {
		t.Errorf("tool invocations = %d, want 0", got)
	}
}

func TestLoopFeedsToolResultsBackOnTheNextRequest(t *testing.T) {
	tool := newScriptedTool("read", "file contents")

	first := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call_1", Name: "read", Arguments: map[string]any{"value": "a.go"}},
		},
		StopReason: ai.StopToolUse,
	}
	second := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "read it"}},
		StopReason: ai.StopEndTurn,
	}

	streamFn, requests := scriptedStream(first, second)
	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "read a.go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}

	invoked := tool.invoked()
	if len(invoked) != 1 {
		t.Fatalf("tool invocations = %d, want 1", len(invoked))
	}
	if invoked[0]["value"] != "a.go" {
		t.Errorf("tool argument = %v, want a.go", invoked[0]["value"])
	}

	// The second request must carry the tool result, or the model would have
	// no idea what the tool returned.
	all := requests()
	if len(all) != 2 {
		t.Fatalf("requests = %d, want 2", len(all))
	}
	var found bool
	for _, msg := range all[1].Messages {
		if result, ok := msg.(ai.ToolResultMessage); ok {
			found = true
			if result.ToolCallID != "call_1" {
				t.Errorf("result tool call id = %q, want call_1", result.ToolCallID)
			}
			if result.Text() != "file contents" {
				t.Errorf("result text = %q, want %q", result.Text(), "file contents")
			}
			if result.IsError {
				t.Error("successful tool result was marked as an error")
			}
		}
	}
	if !found {
		t.Error("second request carried no tool result message")
	}

	// The first request must not contain a tool result.
	for _, msg := range all[0].Messages {
		if _, ok := msg.(ai.ToolResultMessage); ok {
			t.Error("first request unexpectedly carried a tool result")
		}
	}
}

func TestLoopReportsSchemaViolationWithoutRunningTheTool(t *testing.T) {
	tool := newScriptedTool("read", "contents")

	// An integer where the schema requires a string.
	first := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call_1", Name: "read", Arguments: map[string]any{"value": 42}},
		},
		StopReason: ai.StopToolUse,
	}
	streamFn, requests := scriptedStream(first, ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "ok"}},
		StopReason: ai.StopEndTurn,
	})

	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(tool.invoked()); got != 0 {
		t.Fatalf("tool invocations = %d, want 0", got)
	}
	// The failure must reach the model as an error result so it can retry.
	var found bool
	for _, msg := range requests()[1].Messages {
		if result, ok := msg.(ai.ToolResultMessage); ok {
			found = true
			if !result.IsError {
				t.Error("schema violation was not reported as an error")
			}
		}
	}
	if !found {
		t.Error("second request carried no tool result for the invalid call")
	}
}

func TestLoopReportsUnknownToolToTheModel(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	first := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call_1", Name: "nonexistent", Arguments: map[string]any{}},
		},
		StopReason: ai.StopToolUse,
	}
	streamFn, requests := scriptedStream(first, ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "ok"}},
		StopReason: ai.StopEndTurn,
	})

	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(tool.invoked()); got != 0 {
		t.Errorf("tool invocations = %d, want 0", got)
	}
	var found bool
	for _, msg := range requests()[1].Messages {
		if result, ok := msg.(ai.ToolResultMessage); ok {
			found = true
			if !result.IsError {
				t.Error("unknown tool result was not marked as an error")
			}
		}
	}
	if !found {
		t.Error("second request carried no tool result for the unknown tool")
	}
}

func TestLoopStopsOnContextCancellation(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	// A transport that fails as soon as its context is cancelled models a
	// provider whose request was aborted mid-flight.
	hang := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: hang, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}}); err == nil {
		t.Fatal("expected a cancellation error")
	}
}

func TestLoopReturnsResultsInModelEmissionOrder(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	first := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call_a", Name: "read", Arguments: map[string]any{"value": "a"}},
			ai.ToolCall{ID: "call_b", Name: "read", Arguments: map[string]any{"value": "b"}},
		},
		StopReason: ai.StopToolUse,
	}
	streamFn, _ := scriptedStream(first, ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
		StopReason: ai.StopEndTurn,
	})

	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) == 0 || len(turns[0].Results) != 2 {
		t.Fatalf("first turn results = %d, want 2", len(turns[0].Results))
	}
	// Concurrent execution must not reorder the transcript.
	if turns[0].Results[0].CallID != "call_a" || turns[0].Results[1].CallID != "call_b" {
		t.Errorf("results out of order: %q then %q, want call_a then call_b",
			turns[0].Results[0].CallID, turns[0].Results[1].CallID)
	}
}

func TestLoopBlocksCallWhenBeforeToolCallBlocks(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	first := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "call_1", Name: "read", Arguments: map[string]any{"value": "a"}},
		},
		StopReason: ai.StopToolUse,
	}
	streamFn, _ := scriptedStream(first, ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "ok"}},
		StopReason: ai.StopEndTurn,
	})

	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		BeforeToolCall: func(ctx BeforeToolCallContext) BeforeToolCallResult {
			return BeforeToolCallResult{Block: true, Reason: "not allowed here"}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(tool.invoked()); got != 0 {
		t.Errorf("tool invocations = %d, want 0", got)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	results := turns[0].Results
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].Result.Text != "not allowed here" || !results[0].Result.IsError {
		t.Errorf("blocked result = %+v, want the block reason as an error", results[0].Result)
	}
}

func TestLoopRejectsAnEmptyToolSet(t *testing.T) {
	_, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: completedStreamFn(ai.AssistantMessage{}), Tools: nil})
	if err == nil {
		t.Fatal("expected an error for a loop with no tools")
	}
}

func TestLoopSetModelClearsAResolver(t *testing.T) {
	// An explicit /model switch must beat a ModelFn that would otherwise keep
	// returning the old model, or the switch silently does nothing.
	calls := 0
	tool := &scriptedTool{name: "read", result: Result{Text: "ok"}}
	streamFn, requests := scriptedStream(ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn,
	})

	loop, err := New(Config{
		Model:   catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		ModelFn: func() ai.Model { calls++; return catalog.MustResolve("anthropic/claude-sonnet-4-5") },
		Stream:  streamFn,
		Tools:   []Tool{tool},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	loop.SetModel(catalog.MustResolve("openai/gpt-5"))
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if calls != 0 {
		t.Errorf("the resolver was called %d times after an explicit switch", calls)
	}
	all := requests()
	if len(all) == 0 {
		t.Fatal("no requests were recorded")
	}
	if all[0].Model.ID != catalog.MustResolve("openai/gpt-5").ID {
		t.Errorf("request model = %q, want %q", all[0].Model.ID, catalog.MustResolve("openai/gpt-5").ID)
	}
}

// TestSetTurnHookReplacesAndSilencesReports pins the hook the full-screen UI
// depends on: installing nil must stop turn reports (which would otherwise
// print raw turns over the alt-screen), and installing a hook must replace
// the construction-time one rather than adding a second reporter.
func TestSetTurnHookReplacesAndSilencesReports(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	done := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
		StopReason: ai.StopEndTurn,
	}
	streamFn, _ := scriptedStream(done, done, done)
	var first, second int
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		OnTurn: func(Turn) { first++ },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if first != 1 {
		t.Fatalf("construction hook fired %d times, want 1", first)
	}
	loop.SetTurnHook(nil)
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if first != 1 {
		t.Fatalf("silenced hook fired %d times, want still 1", first)
	}
	loop.SetTurnHook(func(Turn) { second++ })
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if second != 1 || first != 1 {
		t.Errorf("hooks = (%d, %d), want the replacement alone at (1, 1)", first, second)
	}
}

// Per-turn system context is appended after the loop's base system prompt, and
// clearing it removes it on the next request. This is how referenced-file
// context reaches the model without rebuilding the loop.
func TestSystemContextAppendsAfterBaseSystem(t *testing.T) {
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}},
	)
	loop, err := New(Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		System: []ai.SystemPromptBlock{{Text: "base"}},
		Tools:  []Tool{newScriptedTool("t", "x")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	loop.SetSystemContext([]ai.SystemPromptBlock{{Text: "attached file"}})
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	loop.SetSystemContext(nil)
	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := requests()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}
	if len(got[0].System) != 2 || got[0].System[0].Text != "base" || got[0].System[1].Text != "attached file" {
		t.Fatalf("first system = %+v, want base then context", got[0].System)
	}
	if len(got[1].System) != 1 || got[1].System[0].Text != "base" {
		t.Fatalf("second system = %+v, want the base prompt alone after clearing", got[1].System)
	}
}
