package agent

import (
	"context"
	"fmt"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingTool records how many calls it has seen, thread-safely.
type countingTool struct {
	name string
	// exclusive forces sole use of the batch.
	exclusive bool
	// ms is an artificial per-call delay, to widen the concurrency window.
	delay func()
	calls atomic.Int64
	mu    sync.Mutex
	seen  []string
}

func (t *countingTool) Name() string        { return t.name }
func (t *countingTool) Description() string { return "counting tool" }
func (t *countingTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"value":{"type":"string"}}}`))
}
func (t *countingTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (t *countingTool) Exclusive(map[string]any) bool { return t.exclusive }
func (t *countingTool) Execute(ctx Context, args map[string]any) Result {
	t.calls.Add(1)
	if t.delay != nil {
		t.delay()
	}
	t.mu.Lock()
	t.seen = append(t.seen, fmt.Sprint(args["value"]))
	t.mu.Unlock()
	return Result{Text: "ok:" + fmt.Sprint(args["value"])}
}

// TestLoopRunsSharedToolsConcurrently proves the batch actually overlaps.
//
// If the loop serialised shared tools, four calls with a 20 ms delay would
// take at least 80 ms. Overlap is an observable contract: it is what keeps a
// multi-tool turn responsive.
func TestLoopRunsSharedToolsConcurrently(t *testing.T) {
	tool := &countingTool{name: "read", delay: waitFor(20 * time.Millisecond)}

	var content []ai.ContentBlock
	for i := 0; i < 4; i++ {
		content = append(content, ai.ToolCall{
			ID:        fmt.Sprintf("call_%d", i),
			Name:      "read",
			Arguments: map[string]any{"value": fmt.Sprintf("v%d", i)},
		})
	}
	first := ai.AssistantMessage{Content: content, StopReason: ai.StopToolUse}
	final := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn}

	streamFn, _ := scriptedStream(first, final)
	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	if got := tool.calls.Load(); got != 4 {
		t.Errorf("calls = %d, want 4", got)
	}
	// 80 ms serialised vs ~20 ms overlapped: allow a generous margin.
	if elapsed >= 80*time.Millisecond {
		t.Errorf("4 calls with 20 ms each took %s; the batch did not overlap", elapsed)
	}
}

func waitFor(d time.Duration) func() { return func() { time.Sleep(d) } }

// TestLoopKeepsExclusiveCallsIsolatedFromLaterSharedTools covers the ordering
// the two-homogeneous tests miss.
//
// A shared tool launched *after* an exclusive one must still wait for it: an
// exclusive call is only exclusive if nothing at all overlaps it.
func TestLoopKeepsExclusiveCallsIsolatedFromLaterSharedTools(t *testing.T) {
	shared := &countingTool{name: "read", delay: waitFor(15 * time.Millisecond)}
	exclusive := &countingTool{name: "bash", delay: waitFor(15 * time.Millisecond)}
	exclusive.exclusive = true

	first := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: "c_shared", Name: "read", Arguments: map[string]any{}},
			ai.ToolCall{ID: "c_excl", Name: "bash", Arguments: map[string]any{}},
			ai.ToolCall{ID: "c_shared2", Name: "read", Arguments: map[string]any{}},
		},
		StopReason: ai.StopToolUse,
	}
	final := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn}

	streamFn, _ := scriptedStream(first, final)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{shared, exclusive},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	// 15 + 15 + 15 ms serialised minimum: the exclusive call must be isolated
	// from both neighbours.
	if elapsed < 45*time.Millisecond {
		t.Errorf("took %s; the exclusive call overlapped a shared one", elapsed)
	}
}

// TestLoopSerialisesExclusiveTools proves an exclusive call drains the batch.
//
// An exclusive tool must not overlap anything. Two exclusive calls with a
// delay must therefore serialise.
func TestLoopSerialisesExclusiveTools(t *testing.T) {
	tool := &countingTool{name: "bash", delay: waitFor(20 * time.Millisecond)}
	tool.exclusive = true

	var content []ai.ContentBlock
	for i := 0; i < 3; i++ {
		content = append(content, ai.ToolCall{
			ID:        fmt.Sprintf("call_%d", i),
			Name:      "bash",
			Arguments: map[string]any{"value": "v"},
		})
	}
	first := ai.AssistantMessage{Content: content, StopReason: ai.StopToolUse}
	final := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn}

	streamFn, _ := scriptedStream(first, final)
	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	// Three 20 ms calls must serialise, so the total must exceed 60 ms rather
	// than collapsing to ~20 ms.
	if elapsed < 60*time.Millisecond {
		t.Errorf("3 exclusive calls took %s; they appear to have overlapped", elapsed)
	}
	if got := tool.calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

// TestLoopSurvivesConcurrentRuns exercises the loop under real parallelism.
//
// A single Loop is not documented as safe for concurrent Run calls, so this
// drives several independent loops built from one shared Config. It is
// primarily a -race guard for the slices a loop mutates.
func TestLoopSurvivesConcurrentRuns(t *testing.T) {
	tools := []Tool{&countingTool{name: "read"}}
	const runs = 8

	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			first := ai.AssistantMessage{
				Content: []ai.ContentBlock{
					ai.ToolCall{ID: fmt.Sprintf("c%d", id), Name: "read", Arguments: map[string]any{"value": "x"}},
				},
				StopReason: ai.StopToolUse,
			}
			final := ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}
			streamFn, _ := scriptedStream(first, final)
			loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: tools})
			if err != nil {
				t.Errorf("New: %v", err)
				return
			}
			if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err != nil {
				t.Errorf("Run: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// Every run must have dispatched exactly once.
	if got := tools[0].(*countingTool).calls.Load(); got != runs {
		t.Errorf("total calls = %d, want %d", got, runs)
	}
}

// TestLoopHandlesManyToolCallsInOneTurn keeps the batch bounded.
//
// A model that emits a very large batch must not blow up memory or lose
// results; the count and the ordering are both asserted.
func TestLoopHandlesManyToolCallsInOneTurn(t *testing.T) {
	const total = 200
	tool := &countingTool{name: "read"}

	var content []ai.ContentBlock
	for i := 0; i < total; i++ {
		content = append(content, ai.ToolCall{
			ID:        fmt.Sprintf("call_%03d", i),
			Name:      "read",
			Arguments: map[string]any{"value": fmt.Sprintf("v%03d", i)},
		})
	}
	first := ai.AssistantMessage{Content: content, StopReason: ai.StopToolUse}
	final := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn}

	streamFn, _ := scriptedStream(first, final)
	loop, err := New(Config{Model: catalog.MustResolve("anthropic/claude-sonnet-4-5"), Stream: streamFn, Tools: []Tool{tool}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := int(tool.calls.Load()); got != total {
		t.Fatalf("calls = %d, want %d", got, total)
	}
	results := turns[0].Results
	if len(results) != total {
		t.Fatalf("results = %d, want %d", len(results), total)
	}
	for i, outcome := range results {
		want := fmt.Sprintf("call_%03d", i)
		if outcome.CallID != want {
			t.Fatalf("result %d = %q, want %q", i, outcome.CallID, want)
		}
	}
}

// TestLoopOnTurnSeesEveryTurn asserts the progress callback fires once per
// turn, which is what a UI relies on for streaming updates.
func TestLoopOnTurnSeesEveryTurn(t *testing.T) {
	tool := &countingTool{name: "read"}
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c0", Name: "read", Arguments: map[string]any{}}},
		StopReason: ai.StopToolUse,
	}
	final := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn}

	streamFn, _ := scriptedStream(first, final)
	var mu sync.Mutex
	var seen int
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		OnTurn: func(turn Turn) {
			mu.Lock()
			seen++
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
	if seen != 2 {
		t.Errorf("OnTurn fired %d times, want 2", seen)
	}
}

// TestLoopAfterToolCallOverridesResult checks the field-by-field override.
func TestLoopAfterToolCallOverridesResult(t *testing.T) {
	tool := &countingTool{name: "read"}
	first := ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.ToolCall{ID: "c0", Name: "read", Arguments: map[string]any{}}},
		StopReason: ai.StopToolUse,
	}
	final := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn}

	streamFn, _ := scriptedStream(first, final)
	replacement := "rewritten by the host"
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		AfterToolCall: func(ctx AfterToolCallContext) AfterToolCallResult {
			return AfterToolCallResult{Text: &replacement}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := turns[0].Results[0].Result.Text; got != replacement {
		t.Errorf("result text = %q, want %q", got, replacement)
	}
}

// TestLoopTransformContextFiltersMessages asserts the hook can drop messages
// before conversion, which is how a host prunes history.
func TestLoopTransformContextFiltersMessages(t *testing.T) {
	tool := &countingTool{name: "read"}
	final := ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn}
	streamFn, requests := scriptedStream(final)

	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: streamFn,
		Tools:  []Tool{tool},
		TransformContext: func(messages []Message) []Message {
			var out []Message
			for _, msg := range messages {
				if _, ok := msg.(Notification); ok {
					continue
				}
				out = append(out, msg)
			}
			return out
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = loop.Run(context.Background(), []Message{
		Notification{Text: "internal noise"},
		UserMessage{Text: "real question"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var foundNoise bool
	for _, msg := range requests()[0].Messages {
		if user, ok := msg.(ai.UserMessage); ok && strings.Contains(user.Text(), "internal noise") {
			foundNoise = true
		}
	}
	if foundNoise {
		t.Error("a UI-only message reached the provider")
	}
}
