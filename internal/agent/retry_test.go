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

// A rate limit must be visible while the loop waits. The UI previously saw
// only "preparing workspace" for the whole backoff, so a 429 looked like a
// stalled workspace for minutes.
func TestProviderActivityReportsRateLimitBeforeTheBackoff(t *testing.T) {
	stream, _ := retryStream(1, statusErr{status: 429, retry: 1})
	loop := retryLoop(t, stream, RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: 2 * time.Millisecond}, time.Time{})
	var activities []ProviderActivity
	loop.SetProviderActivityHook(func(a ProviderActivity) { activities = append(activities, a) })

	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "answer"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var retry *ProviderActivity
	for i := range activities {
		if activities[i].Phase == ProviderRetrying {
			retry = &activities[i]
			break
		}
	}
	if retry == nil {
		t.Fatalf("activities = %+v, want a retrying phase", activities)
	}
	if retry.Status != 429 {
		t.Errorf("retry status = %d, want 429", retry.Status)
	}
	if retry.Delay <= 0 || retry.Delay > time.Second {
		t.Errorf("retry delay = %s, want the measured backoff", retry.Delay)
	}
	if retry.Attempt != 1 || retry.Attempts != 3 {
		t.Errorf("retry attempt = %d/%d, want 1/3", retry.Attempt, retry.Attempts)
	}
	foundRecovered := false
	for _, a := range activities {
		if a.Phase == ProviderRecovered {
			foundRecovered = true
		}
	}
	if !foundRecovered {
		t.Errorf("activities = %+v, want a recovered phase", activities)
	}
}

// A non-retryable rejection is terminal immediately; the UI must not show a
// retry countdown for a request that will never be repeated.
func TestProviderActivityReportsTerminalFailure(t *testing.T) {
	stream, attempts := retryStream(1, statusErr{status: 401})
	loop := retryLoop(t, stream, RetryPolicy{Attempts: 4, Base: time.Millisecond, Max: 2 * time.Millisecond}, time.Time{})
	var last ProviderActivity
	loop.SetProviderActivityHook(func(a ProviderActivity) { last = a })

	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "answer"}}); err == nil {
		t.Fatal("a 401 must surface as an error")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("provider attempts = %d, want 1", got)
	}
	if last.Phase != ProviderFailed || last.Status != 401 || last.Attempt != 1 {
		t.Fatalf("last activity = %+v, want failed 401 on attempt 1", last)
	}
}
