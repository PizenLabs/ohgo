package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// statusErr is a provider rejection with the HTTP status surface the retry
// predicate consumes.
type statusErr struct {
	status int
	retry  int
}

func (e statusErr) Error() string   { return "provider rejected the request" }
func (e statusErr) StatusCode() int { return e.status }
func (e statusErr) RetryAfter() int { return e.retry }

// retryStream fails with the given error on the first failures attempts and
// then answers. It counts every request so a test can prove the retry happened
// before any model output existed.
func retryStream(failures int, err error) (ai.StreamFn, *atomic.Int32) {
	var attempts atomic.Int32
	return func(context.Context, ai.Request) (*ai.Stream, error) {
		n := attempts.Add(1)
		if int(n) <= failures {
			return nil, err
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
			StopReason: ai.StopEndTurn,
		}), nil
	}, &attempts
}

func retryLoop(t *testing.T, stream ai.StreamFn, policy RetryPolicy, deadline time.Time) *Loop {
	t.Helper()
	loop, err := New(Config{
		Model:    catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:   stream,
		Tools:    []Tool{newScriptedTool("read", "ok")},
		Retry:    policy,
		Deadline: deadline,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

// A rate-limited request is retried transparently and the run completes from
// the first answer that actually arrives.
func TestLoopRetriesARateLimitedRequestWithoutRepeatingModelWork(t *testing.T) {
	stream, attempts := retryStream(2, statusErr{status: 429})
	loop := retryLoop(t, stream, RetryPolicy{Attempts: 4, Base: time.Millisecond, Max: 2 * time.Millisecond}, time.Time{})

	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "answer"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("provider attempts = %d, want 3", got)
	}
	if len(turns) != 1 || turns[0].Assistant.Text() != "done" {
		t.Fatalf("turns = %+v, want one completed answer", turns)
	}
}

// A request the provider rejects on its merits is not repeated: a 400 means the
// request itself is wrong, and retrying multiplies the failure.
func TestLoopDoesNotRetryARejectedRequest(t *testing.T) {
	stream, attempts := retryStream(3, statusErr{status: 400})
	loop := retryLoop(t, stream, RetryPolicy{Attempts: 4, Base: time.Millisecond, Max: 2 * time.Millisecond}, time.Time{})

	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "answer"}}); err == nil {
		t.Fatal("a rejected request must surface as an error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("provider attempts = %d, want 1", got)
	}
}

// A backoff that would cross the run deadline is not started: the run reports
// the provider failure instead of spending its last seconds asleep.
func TestLoopDoesNotSleepPastTheRunDeadline(t *testing.T) {
	stream, attempts := retryStream(5, statusErr{status: 503})
	loop := retryLoop(t, stream, RetryPolicy{Attempts: 4, Base: time.Hour, Max: time.Hour}, time.Now().Add(50*time.Millisecond))

	start := time.Now()
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "answer"}}); err == nil {
		t.Fatal("an exhausted deadline must not complete")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("run waited %s past a 50ms deadline", elapsed)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("provider attempts = %d, want 1", got)
	}
}

// A cancelled context ends the retry instead of waiting for the next backoff.
func TestLoopStopsRetryingOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		cancel()
		return nil, statusErr{status: 429}
	}
	loop := retryLoop(t, stream, RetryPolicy{Attempts: 3, Base: time.Hour, Max: time.Hour}, time.Time{})

	_, err := loop.Run(ctx, []Message{UserMessage{Text: "answer"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}
