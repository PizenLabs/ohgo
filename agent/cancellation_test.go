package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/jsonschema"
)

// A cancellation that lands between the model's proposal and the tool call must
// prevent the call. Without the check, a tool could run after the user already
// stopped the turn.
func TestCancellationBeforeToolExecutionStopsTheCall(t *testing.T) {
	tool := newScriptedTool("read", "contents")
	ctx, cancel := context.WithCancel(context.Background())
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: oneCallStream("read", map[string]any{"value": "x"}),
		Tools:  []Tool{tool},
		BeforeToolCall: func(BeforeToolCallContext) BeforeToolCallResult {
			cancel()
			return BeforeToolCallResult{}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	turns, err := loop.Run(ctx, []Message{UserMessage{Text: "go"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := len(tool.invoked()); got != 0 {
		t.Fatalf("tool invocations = %d, want 0 after cancellation", got)
	}
	if len(turns) != 1 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %+v, want one turn with one refusal", turns)
	}
	if !turns[0].Results[0].Result.IsError {
		t.Fatal("a cancelled call was recorded as a successful execution")
	}
}

// blockingTool waits for cancellation, so the test exercises a tool that is
// already running when the turn is stopped.
type blockingTool struct {
	started chan struct{}
}

func (t *blockingTool) Name() string        { return "block" }
func (t *blockingTool) Description() string { return "blocks until cancelled" }
func (t *blockingTool) Approval() Approval  { return Approval{Tier: TierRead} }
func (t *blockingTool) Exclusive(map[string]any) bool {
	return false
}
func (t *blockingTool) Parameters() *jsonschema.Schema {
	schema, err := jsonschema.CompileMap(map[string]any{"type": "object"})
	if err != nil {
		panic(err)
	}
	return schema
}
func (t *blockingTool) Execute(ctx Context, _ map[string]any) Result {
	close(t.started)
	<-ctx.Ctx.Done()
	return ErrorResult("cancelled")
}

// Cancellation during execution is observable as an error outcome, and the run
// returns the cancellation rather than pretending the turn finished.
func TestCancellationDuringToolExecutionIsObservable(t *testing.T) {
	tool := &blockingTool{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: oneCallStream("block", map[string]any{"value": "x"}),
		Tools:  []Tool{tool},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan error, 1)
	var turns []Turn
	go func() {
		var runErr error
		turns, runErr = loop.Run(ctx, []Message{UserMessage{Text: "go"}})
		done <- runErr
	}()

	<-tool.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(turns) != 1 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %+v, want the cancelled tool outcome", turns)
	}
	if !turns[0].Results[0].Result.IsError {
		t.Fatal("a cancelled execution was recorded as success")
	}
}
