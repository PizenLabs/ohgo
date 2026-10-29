package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

func budgetTestModel() ai.Model { return catalog.MustResolve("anthropic/claude-sonnet-4-5") }

func budgetToolCallMessage(id string, calls ...ai.ToolCall) ai.AssistantMessage {
	content := make([]ai.ContentBlock, 0, len(calls))
	for _, call := range calls {
		content = append(content, call)
	}
	return ai.AssistantMessage{Content: content, StopReason: ai.StopToolUse}
}

func budgetLoop(t *testing.T, stream ai.StreamFn, tools []Tool, b *budget.Budget, maxTurns int) *Loop {
	t.Helper()
	loop, err := New(Config{
		Model:    budgetTestModel(),
		Stream:   stream,
		Tools:    tools,
		Budget:   b,
		MaxTurns: maxTurns,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

// One over the root turn grant stops before another provider call and leaves
// the completed turns intact. This is the tree-wide unit of admission.
func TestRootTurnBudgetStopsBeforeAnUnadmittedModelCall(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 3, ModelCalls: 10, ToolCalls: 10})
	tool := newScriptedTool("read", "ok")
	stream, requests := scriptedStream(
		budgetToolCallMessage("1", ai.ToolCall{ID: "1", Name: "read", Arguments: map[string]any{"value": "x"}}),
		budgetToolCallMessage("2", ai.ToolCall{ID: "2", Name: "read", Arguments: map[string]any{"value": "x"}}),
		budgetToolCallMessage("3", ai.ToolCall{ID: "3", Name: "read", Arguments: map[string]any{"value": "x"}}),
		budgetToolCallMessage("4", ai.ToolCall{ID: "4", Name: "read", Arguments: map[string]any{"value": "x"}}),
	)
	loop := budgetLoop(t, stream, []Tool{tool}, b, 100)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("err = %v, want budget.ErrExhausted", err)
	}
	if len(turns) != 3 {
		t.Fatalf("turns = %d, want the 3 admitted turns", len(turns))
	}
	if got := len(requests()); got != 3 {
		t.Fatalf("provider calls = %d, want 3", got)
	}
	if got := b.Snapshot().Used.Turns; got != 3 {
		t.Fatalf("root used turns = %d, want 3", got)
	}
}

// A model-call grant is independent of the turn count. The loop must stop
// before opening a provider request it cannot afford, even though turns
// remain.
func TestModelCallBudgetStopsBeforeOpeningAStream(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 10, ModelCalls: 2, ToolCalls: 10})
	tool := newScriptedTool("read", "ok")
	stream, requests := scriptedStream(
		budgetToolCallMessage("1", ai.ToolCall{ID: "1", Name: "read", Arguments: map[string]any{"value": "x"}}),
		budgetToolCallMessage("2", ai.ToolCall{ID: "2", Name: "read", Arguments: map[string]any{"value": "x"}}),
		budgetToolCallMessage("3", ai.ToolCall{ID: "3", Name: "read", Arguments: map[string]any{"value": "x"}}),
	)
	loop := budgetLoop(t, stream, []Tool{tool}, b, 100)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("err = %v, want budget.ErrExhausted", err)
	}
	var exhausted *budget.ExhaustedError
	if !errors.As(err, &exhausted) || exhausted.Dimension != budget.DimensionModelCalls {
		t.Fatalf("dimension = %v, want model_calls", err)
	}
	if len(turns) != 2 || len(requests()) != 2 {
		t.Fatalf("turns/requests = %d/%d, want 2/2", len(turns), len(requests()))
	}
}

// A tool batch larger than the remaining grant executes exactly the admitted
// calls. The refused calls must produce no execution, and the completed turn
// still records their error outcomes.
func TestToolBudgetAdmitsExactlyTheRemainingCalls(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 10, ModelCalls: 10, ToolCalls: 2})
	tool := newScriptedTool("read", "ok")
	stream, _ := scriptedStream(budgetToolCallMessage("1",
		ai.ToolCall{ID: "1", Name: "read", Arguments: map[string]any{"value": "a"}},
		ai.ToolCall{ID: "2", Name: "read", Arguments: map[string]any{"value": "b"}},
		ai.ToolCall{ID: "3", Name: "read", Arguments: map[string]any{"value": "c"}},
	))
	loop := budgetLoop(t, stream, []Tool{tool}, b, 100)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("err = %v, want budget.ErrExhausted", err)
	}
	if got := len(tool.invoked()); got != 2 {
		t.Fatalf("tool executions = %d, want exactly 2", got)
	}
	if len(turns) != 1 || len(turns[0].Results) != 3 {
		t.Fatalf("turn shape = %d/%d, want 1 turn with 3 outcomes", len(turns), len(turns[0].Results))
	}
	failed := 0
	for _, outcome := range turns[0].Results {
		if outcome.Result.IsError {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed outcomes = %d, want the one refused admission", failed)
	}
}

// The legacy MaxTurns cap keeps its frozen sentinel, while an explicit
// tree-wide grant reports the resource dimension.
func TestLegacyTurnCapAndTreeBudgetAreDistinguishable(t *testing.T) {
	tool := newScriptedTool("read", "ok")
	legacy, err := New(Config{
		Model:    budgetTestModel(),
		Stream:   alwaysToolStream("read"),
		Tools:    []Tool{tool},
		MaxTurns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Run(context.Background(), []Message{UserMessage{Text: "go"}}); !errors.Is(err, ErrTurnLimit) {
		t.Fatalf("legacy err = %v, want ErrTurnLimit", err)
	}

	b := budget.New(budget.Limits{Turns: 2, ModelCalls: 10, ToolCalls: 10})
	tree, err := New(Config{
		Model:  budgetTestModel(),
		Stream: alwaysToolStream("read"),
		Tools:  []Tool{tool},
		Budget: b,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.Run(context.Background(), []Message{UserMessage{Text: "go"}}); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("tree err = %v, want budget.ErrExhausted", err)
	}
}

// A child consumes the same counters as its parent. Once the shared grant is
// spent, a second child is refused before its provider is called: it cannot
// reset the tree by being a new child.
func TestChildCannotResetTheRootGrant(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 2, ModelCalls: 10, ToolCalls: 10, Children: 4})
	providerCalls := 0
	var mu sync.Mutex
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		mu.Lock()
		providerCalls++
		call := providerCalls
		mu.Unlock()
		if call == 1 {
			return ai.NewCompletedStream(budgetToolCallMessage("child-1",
				ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": "x"}})), nil
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "child done"}},
			StopReason: ai.StopEndTurn,
		}), nil
	}
	parent := Config{
		Model:    budgetTestModel(),
		Stream:   stream,
		Tools:    []Tool{newScriptedTool("read", "ok")},
		MaxTurns: 100,
		Budget:   b,
	}
	spec := SubagentSpec{Task: "do it", System: []ai.SystemPromptBlock{{Text: "child"}}}
	if _, err := RunSubagent(context.Background(), parent, spec); err != nil {
		t.Fatalf("first child: %v", err)
	}
	if got := b.Snapshot().Used.Turns; got != 2 {
		t.Fatalf("root used turns = %d, want the child's 2", got)
	}
	before := providerCalls
	_, err := RunSubagent(context.Background(), parent, spec)
	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("second child err = %v, want budget.ErrExhausted", err)
	}
	if providerCalls != before {
		t.Fatalf("refused child made %d extra provider calls", providerCalls-before)
	}
}

// A root budget deadline is one absolute instant. A loop started after it
// makes no provider call even when its legacy Deadline is unset.
func TestRootBudgetDeadlineIsEnforcedByTheLoop(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 5, ModelCalls: 5, Deadline: time.Now().Add(-time.Second)})
	stream, requests := scriptedStream(ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
		StopReason: ai.StopEndTurn,
	})
	loop := budgetLoop(t, stream, []Tool{newScriptedTool("read", "ok")}, b, 100)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 0 || len(requests()) != 0 {
		t.Fatalf("turns/requests = %d/%d after the root deadline", len(turns), len(requests()))
	}
}
