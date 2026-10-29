package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// alwaysToolStream answers every request with another tool call. It is the
// shape a runaway model takes: no turn ever stops the loop on its own.
func alwaysToolStream(name string) ai.StreamFn {
	var mu sync.Mutex
	n := 0
	return func(context.Context, ai.Request) (*ai.Stream, error) {
		mu.Lock()
		n++
		id := fmt.Sprintf("call-%d", n)
		mu.Unlock()
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		}), nil
	}
}

// finiteToolStream makes n tool calls and then answers with text.
func finiteToolStream(name string, calls int) ai.StreamFn {
	var mu sync.Mutex
	n := 0
	return func(context.Context, ai.Request) (*ai.Stream, error) {
		mu.Lock()
		n++
		position := n
		mu.Unlock()
		if position > calls {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: fmt.Sprintf("call-%d", position), Name: name, Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		}), nil
	}
}

func turnLimitLoop(t *testing.T, stream ai.StreamFn, maxTurns int) *Loop {
	t.Helper()
	loop, err := New(Config{
		Model:    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:   stream,
		Tools:    []Tool{newScriptedTool("read", "contents")},
		MaxTurns: maxTurns,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

// A model that never stops proposing tools must be bounded even without a
// deadline. The cap returns the completed turns intact so the task can resume.
func TestTurnLimitBoundsARunawayModel(t *testing.T) {
	loop := turnLimitLoop(t, alwaysToolStream("read"), 5)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if !errors.Is(err, ErrTurnLimit) {
		t.Fatalf("err = %v, want ErrTurnLimit", err)
	}
	if len(turns) != 5 {
		t.Fatalf("turns = %d, want 5", len(turns))
	}
}

// The default is a real bound, not zero-means-unlimited.
func TestDefaultTurnLimitIsBounded(t *testing.T) {
	loop := turnLimitLoop(t, alwaysToolStream("read"), 0)
	if loop.cfg.MaxTurns != DefaultMaxTurns {
		t.Fatalf("default MaxTurns = %d, want %d", loop.cfg.MaxTurns, DefaultMaxTurns)
	}
}

// Five, ten, twenty, and fifty turn workloads are well within the default
// budget and complete normally.
func TestTurnBudgetsCompleteWithinTheDefaultLimit(t *testing.T) {
	for _, calls := range []int{5, 10, 20, 50} {
		t.Run(fmt.Sprintf("%d_turns", calls), func(t *testing.T) {
			loop := turnLimitLoop(t, finiteToolStream("read", calls), 0)
			turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(turns) != calls+1 {
				t.Fatalf("turns = %d, want %d (tool turns plus the final answer)", len(turns), calls+1)
			}
			if !EvaluateCompletion(turns, false).Complete() {
				t.Fatal("a completed read-only run was not complete")
			}
		})
	}
}

// Repeated identical actions are bounded by the same turn budget; there is no
// second no-progress rule to disagree with it.
func TestRepeatedIdenticalCallsAreBounded(t *testing.T) {
	loop := turnLimitLoop(t, alwaysToolStream("read"), 20)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if !errors.Is(err, ErrTurnLimit) {
		t.Fatalf("err = %v, want ErrTurnLimit", err)
	}
	if len(turns) != 20 {
		t.Fatalf("turns = %d, want 20", len(turns))
	}
}

// Repeated tool failures are bounded by the same turn budget. The failures are
// not retried forever by the runtime; the model decides whether to retry, and
// the cap bounds that decision.
func TestRepeatedToolFailureIsBounded(t *testing.T) {
	tool := newScriptedTool("read", "failed")
	tool.result.IsError = true
	loop, err := New(Config{
		Model:    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:   alwaysToolStream("read"),
		Tools:    []Tool{tool},
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if !errors.Is(err, ErrTurnLimit) {
		t.Fatalf("err = %v, want ErrTurnLimit", err)
	}
	if len(turns) != 5 {
		t.Fatalf("turns = %d, want 5", len(turns))
	}
	if EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a run of failed tool calls was reported complete")
	}
}

// Delegation inherits the turn budget: a child that always calls tools cannot
// outlive the parent's bound.
func TestSubagentInheritsTheTurnLimit(t *testing.T) {
	parent := Config{
		Model:    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:   alwaysToolStream("read"),
		Tools:    []Tool{newScriptedTool("read", "contents")},
		MaxTurns: 3,
	}
	_, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "inspect forever",
		System: []ai.SystemPromptBlock{{Text: "you are a researcher"}},
	})
	if !errors.Is(err, ErrTurnLimit) {
		t.Fatalf("subagent err = %v, want ErrTurnLimit", err)
	}
}
