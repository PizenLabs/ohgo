package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// debtTool is a scripted tool that can fail a fixed number of attempts and
// optionally report a successful verification. It exists to prove that a
// retry is a new admission, not a free re-execution.
type debtTool struct {
	name       string
	mu         sync.Mutex
	executions int
	failFirst  int
	verifies   bool
	cancel     context.CancelFunc
}

func (t *debtTool) Name() string { return t.name }
func (t *debtTool) Description() string {
	return "test tool for budget retry accounting"
}
func (t *debtTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (t *debtTool) Approval() Approval { return Approval{Tier: TierRead} }
func (t *debtTool) Exclusive(map[string]any) bool {
	return false
}
func (t *debtTool) Execute(ctx Context, _ map[string]any) Result {
	t.mu.Lock()
	t.executions++
	attempt := t.executions
	t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
	}
	if attempt <= t.failFirst {
		return Result{Text: "attempt failed", IsError: true}
	}
	return Result{Text: "ok", Verifies: t.verifies}
}

func debtCall(id string) ai.AssistantMessage {
	return budgetToolCallMessage(id, ai.ToolCall{ID: id, Name: "debt", Arguments: map[string]any{}})
}

func debtStop() ai.AssistantMessage {
	return ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
		StopReason: ai.StopEndTurn,
	}
}

// A provider failure is charged before the request opens. Retrying on the
// same loop continues the same grant; it does not reset the turn or model
// counters.
func TestRetryAfterModelFailureStaysInsideTheGrant(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 2, ModelCalls: 2, ToolCalls: 2})
	var calls atomic.Int64
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("provider failed")
		}
		return ai.NewCompletedStream(debtStop()), nil
	}
	loop := budgetLoop(t, stream, []Tool{&debtTool{name: "debt"}}, b, 100)

	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); err == nil {
		t.Fatal("the first run did not surface the provider failure")
	}
	if used := b.Snapshot().Used; used.Turns != 1 || used.ModelCalls != 1 {
		t.Fatalf("after model failure used = %+v, want turns=1 model_calls=1", used)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "retry"}}); err != nil {
		t.Fatalf("retry after model failure: %v", err)
	}
	if used := b.Snapshot().Used; used.Turns != 2 || used.ModelCalls != 2 {
		t.Fatalf("after retry used = %+v, want turns=2 model_calls=2", used)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "again"}}); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("third attempt error = %v, want budget exhaustion", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2: the exhausted retry must not open a stream", got)
	}
}

// A failed tool execution is still an admitted execution. Retrying it needs
// another tool admission; a one-call grant cannot execute twice.
func TestRetryAfterToolFailureNeedsANewAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		toolCalls int
		wantExec  int
		wantErr   error
	}{
		{name: "one unit", toolCalls: 1, wantExec: 1, wantErr: budget.ErrExhausted},
		{name: "two units", toolCalls: 2, wantExec: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := budget.New(budget.Limits{Turns: 5, ModelCalls: 5, ToolCalls: tc.toolCalls})
			tool := &debtTool{name: "debt", failFirst: 1}
			stream, _ := scriptedStream(
				debtCall("1"),
				debtCall("2"),
				debtStop(),
			)
			loop := budgetLoop(t, stream, []Tool{tool}, b, 100)
			_, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if tool.executions != tc.wantExec {
				t.Fatalf("executions = %d, want %d", tool.executions, tc.wantExec)
			}
			if used := b.Snapshot().Used.ToolCalls; used != tc.wantExec {
				t.Fatalf("tool calls used = %d, want %d", used, tc.wantExec)
			}
		})
	}
}

// A failed verification is not a successful check, and retrying it is
// another tool admission. A one-call grant stops before the retry; a
// two-call grant can prove the later success.
func TestRetryAfterVerificationFailureNeedsANewAdmission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		toolCalls  int
		wantExec   int
		wantVerify bool
		wantErr    error
	}{
		{name: "one unit", toolCalls: 1, wantExec: 1, wantErr: budget.ErrExhausted},
		{name: "two units", toolCalls: 2, wantExec: 2, wantVerify: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := budget.New(budget.Limits{Turns: 5, ModelCalls: 5, ToolCalls: tc.toolCalls})
			tool := &debtTool{name: "debt", failFirst: 1, verifies: true}
			stream, _ := scriptedStream(
				debtCall("1"),
				debtCall("2"),
				debtStop(),
			)
			loop := budgetLoop(t, stream, []Tool{tool}, b, 100)
			turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if tool.executions != tc.wantExec {
				t.Fatalf("executions = %d, want %d", tool.executions, tc.wantExec)
			}
			verified := false
			for _, turn := range turns {
				for _, outcome := range turn.Results {
					if outcome.Result.Verifies {
						verified = true
					}
				}
			}
			if verified != tc.wantVerify {
				t.Fatalf("verified = %v, want %v", verified, tc.wantVerify)
			}
		})
	}
}

// Cancellation ends the run but does not refund its consumption. A later run
// on the same grant continues from the consumed counters.
func TestRetryAfterCancellationDoesNotResetTheGrant(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 3, ModelCalls: 3, ToolCalls: 3})
	ctx, cancel := context.WithCancel(context.Background())
	tool := &debtTool{name: "debt", cancel: cancel}
	var calls atomic.Int64
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		if calls.Add(1) == 1 {
			return ai.NewCompletedStream(debtCall("1")), nil
		}
		return ai.NewCompletedStream(debtStop()), nil
	}
	loop := budgetLoop(t, stream, []Tool{tool}, b, 100)

	if _, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run error = %v, want context.Canceled", err)
	}
	if used := b.Snapshot().Used; used.Turns != 1 || used.ModelCalls != 1 || used.ToolCalls != 1 {
		t.Fatalf("after cancellation used = %+v, want one of each", used)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "retry"}}); err != nil {
		t.Fatalf("retry after cancellation: %v", err)
	}
	if used := b.Snapshot().Used; used.Turns != 2 || used.ModelCalls != 2 || used.ToolCalls != 1 {
		t.Fatalf("after retry used = %+v, want turns=2 model_calls=2 tool_calls=1", used)
	}
}

// An expired absolute deadline is not resurrected by retrying. Every attempt
// observes the same instant and makes no provider call.
func TestRetryAfterDeadlineDoesNotResurrectBudget(t *testing.T) {
	b := budget.New(budget.Limits{
		Turns: 2, ModelCalls: 2, ToolCalls: 2,
		Deadline: time.Now().Add(-time.Second),
	})
	stream, requests := scriptedStream(debtStop())
	loop := budgetLoop(t, stream, []Tool{&debtTool{name: "debt"}}, b, 100)
	for attempt := 0; attempt < 2; attempt++ {
		turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if len(turns) != 0 {
			t.Fatalf("attempt %d returned %d turns after the deadline", attempt, len(turns))
		}
	}
	if got := len(requests()); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	if used := b.Snapshot().Used; used.Turns != 0 || used.ModelCalls != 0 {
		t.Fatalf("deadline retry consumed budget: %+v", used)
	}
}

// Exhaustion is terminal for the current grant. Retrying cannot mint a turn
// or open a provider stream.
func TestRetryAfterBudgetExhaustionMakesNoProviderCall(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 1, ModelCalls: 1, ToolCalls: 2})
	stream, requests := scriptedStream(debtCall("1"), debtStop())
	loop := budgetLoop(t, stream, []Tool{&debtTool{name: "debt"}}, b, 100)
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}}); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("first run error = %v, want budget exhaustion", err)
	}
	before := len(requests())
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "retry"}}); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("retry error = %v, want budget exhaustion", err)
	}
	if got := len(requests()) - before; got != 0 {
		t.Fatalf("exhausted retry opened %d provider calls", got)
	}
	if used := b.Snapshot().Used; used.Turns != 1 || used.ModelCalls != 1 {
		t.Fatalf("used after retry = %+v, want the original single turn", used)
	}
}

// Sibling children contend for one shared child grant. Admission is atomic:
// exactly one child runs, and the root sees only that child's consumption.
func TestConcurrentChildrenCannotBothAdmitTheLastUnit(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 10, ModelCalls: 10, ToolCalls: 10, Children: 1})
	var providerCalls atomic.Int64
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		providerCalls.Add(1)
		return ai.NewCompletedStream(debtStop()), nil
	}
	parent := Config{
		Model:    budgetTestModel(),
		Stream:   stream,
		Tools:    []Tool{newScriptedTool("read", "ok")},
		MaxTurns: 10,
		Budget:   b,
	}
	spec := SubagentSpec{Task: "do it", System: []ai.SystemPromptBlock{{Text: "child"}}}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = RunSubagent(context.Background(), parent, spec)
		}(i)
	}
	wg.Wait()

	success, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, budget.ErrExhausted):
			refused++
		default:
			t.Fatalf("unexpected child error: %v", err)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatalf("children = %d success / %d refused, want 1/1", success, refused)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want only the admitted child", got)
	}
	used := b.Snapshot().Used
	if used.Children != 1 || used.Turns != 1 || used.ModelCalls != 1 {
		t.Fatalf("root used = %+v, want children=1 turns=1 model_calls=1", used)
	}
}

// A failed child does not refund its delegated unit. A retry is another
// child admission and another provider request.
func TestFailedChildRetryConsumesAnotherChildUnit(t *testing.T) {
	b := budget.New(budget.Limits{Turns: 10, ModelCalls: 10, Children: 2})
	var providerCalls atomic.Int64
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		if providerCalls.Add(1) == 1 {
			return nil, errors.New("child provider failed")
		}
		return ai.NewCompletedStream(debtStop()), nil
	}
	parent := Config{
		Model:    budgetTestModel(),
		Stream:   stream,
		Tools:    []Tool{newScriptedTool("read", "ok")},
		MaxTurns: 10,
		Budget:   b,
	}
	spec := SubagentSpec{Task: "do it", System: []ai.SystemPromptBlock{{Text: "child"}}}

	if _, err := RunSubagent(context.Background(), parent, spec); err == nil {
		t.Fatal("the failing child returned no error")
	}
	used := b.Snapshot().Used
	if used.Children != 1 || used.Turns != 1 || used.ModelCalls != 1 {
		t.Fatalf("after failed child used = %+v, want one of each", used)
	}
	if _, err := RunSubagent(context.Background(), parent, spec); err != nil {
		t.Fatalf("child retry: %v", err)
	}
	used = b.Snapshot().Used
	if used.Children != 2 || used.Turns != 2 || used.ModelCalls != 2 {
		t.Fatalf("after child retry used = %+v, want two of each", used)
	}
	if got := providerCalls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
}
