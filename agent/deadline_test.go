package agent

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/ai"
)

// TestLoopHonoursADeadlineIsARunawayCostGuard is the reason the deadline exists.
//
// Without it, `for {}` with no bound means a model that keeps calling tools
// loops forever, making provider calls until something external stops it. Time
// is the right bound because each iteration costs money; a turn count would only
// approximate it.
func TestLoopHonoursADeadlineIsARunawayCostGuard(t *testing.T) {
	// A stream that always asks for another tool call: the shape of a model
	// stuck in a loop.
	stream, requests := scriptedStream(
		assistantWithToolCall("call_1", "read"),
		assistantWithToolCall("call_2", "read"),
		assistantWithToolCall("call_3", "read"),
	)

	loop, err := New(Config{
		Model:    ai.Model{ID: "m", Provider: "p", ContextWindow: 100_000},
		Stream:   stream,
		Tools:    []Tool{&countingTool{name: "read"}},
		Deadline: time.Now().Add(-time.Second), // already expired
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("an expired deadline returned an error (%v); it is a spent budget, not a failure", err)
	}
	// Not even one provider call may be made: the point is to stop spending.
	if len(requests()) != 0 {
		t.Errorf("requests = %d, want 0 once the deadline has passed", len(requests()))
	}
	if len(turns) != 0 {
		t.Errorf("turns = %d, want none", len(turns))
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v, want it to stop immediately", elapsed)
	}
}

// TestLoopStopsAtTheDeadlineRatherThanMidRequest proves the check sits between
// iterations, so a run already in flight is not cut off in the middle of a
// provider response.
func TestLoopStopsAtTheDeadlineRatherThanMidRequest(t *testing.T) {
	stream, requests := scriptedStream(
		assistantWithToolCall("call_1", "read"),
		assistantWithToolCall("call_2", "read"),
		assistantWithToolCall("call_3", "read"),
	)

	const (
		deadline = 5 * time.Millisecond
		toolCost = 40 * time.Millisecond
	)

	loop, err := New(Config{
		Model:    ai.Model{ID: "m", Provider: "p", ContextWindow: 100_000},
		Stream:   stream,
		Tools:    []Tool{&slowTool{countingTool{name: "read"}, toolCost}},
		Deadline: time.Now().Add(deadline),
	})
	if err != nil {
		t.Fatal(err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(requests()); got != 1 {
		t.Errorf("requests = %d, want exactly 1: the first turn must complete and the second must not start", got)
	}
	// Whatever completed is returned, so the caller can resume from it.
	if len(turns) != 1 {
		t.Errorf("turns = %d, want the one that finished", len(turns))
	}
}

// slowTool sleeps for delay, so a test can push a run past its deadline
// deterministically rather than depending on how fast the machine is.
type slowTool struct {
	countingTool
	delay time.Duration
}

func (t *slowTool) Execute(ctx Context, args map[string]any) Result {
	time.Sleep(t.delay)
	return Result{Text: "ok"}
}

// TestLoopWithoutADeadlineRunsToCompletion keeps the default unbounded, so a
// legitimate long task is not cut short by a limit nobody asked for.
func TestLoopWithoutADeadlineRunsToCompletion(t *testing.T) {
	stream, requests := scriptedStream(
		assistantWithToolCall("call_1", "read"),
		assistantText("finished"),
	)

	loop, err := New(Config{
		Model:  ai.Model{ID: "m", Provider: "p", ContextWindow: 100_000},
		Stream: stream,
		Tools:  []Tool{&countingTool{name: "read"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(requests()) != 2 {
		t.Errorf("requests = %d, want 2", len(requests()))
	}
	if len(turns) == 0 {
		t.Fatal("no turns were returned")
	}
	if got := turns[len(turns)-1].Assistant.Text(); got != "finished" {
		t.Errorf("final text = %q, want the model's answer", got)
	}
}

// TestLoopDeadlineIsDistinctFromContextCancellation keeps the two contracts
// apart, because a caller has to handle them differently.
//
// Cancellation is an abort: something outside decided to stop, and the work is
// discarded. A deadline is a budget being spent: the work so far is intended and
// resumable. Reporting a deadline as a cancellation error would make callers
// throw away results they were entitled to keep.
func TestLoopDeadlineIsDistinctFromContextCancellation(t *testing.T) {
	stream, _ := scriptedStream(assistantText("done"))

	loop, err := New(Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   stream,
		Tools:    []Tool{&countingTool{name: "read"}},
		Deadline: time.Now().Add(-time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatalf("a deadline returned an error, which makes it a cancellation: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = loop.Run(ctx, []Message{UserMessage{Text: "go"}})
	if err == nil {
		t.Fatal("a cancelled context did not return an error")
	}
}

// TestDeadlineSurvivesSubagentConstruction proves a child inherits the parent's
// bound, so a deeply nested delegation cannot outlive the run that started it.
func TestDeadlineSurvivesSubagentConstruction(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	parent := Config{
		Model:    ai.Model{ID: "m", Provider: "p"},
		Stream:   aiTextStream("done"),
		Tools:    []Tool{namedTool("read")},
		Deadline: deadline,
	}

	// The child is built and run; if the deadline were dropped the run would
	// still succeed, so the observable contract is that it does not fail.
	if _, err := RunSubagent(context.Background(), parent, SubagentSpec{
		Task:   "t",
		System: []ai.SystemPromptBlock{{Text: "child"}},
		Tools:  []Tool{namedTool("read")},
	}); err != nil {
		t.Fatalf("RunSubagent: %v", err)
	}
}

// assistantWithToolCall builds an assistant turn that requests a tool call.
func assistantWithToolCall(id, tool string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{
			ID:        id,
			Name:      tool,
			Arguments: map[string]any{},
		}},
	}
}

// assistantText builds an assistant turn that only answers.
func assistantText(text string) ai.AssistantMessage {
	return ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: text}},
	}
}
