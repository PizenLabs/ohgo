package agent

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// RetryPolicy bounds retries for a provider request that produced no model
// output because the transport was temporarily unavailable.
//
// It is deliberately narrow. A retry re-issues the identical request before any
// assistant message or tool call exists, so it cannot duplicate a tool effect
// and it does not spend an additional turn: the turn is only consumed once the
// provider actually answers. The zero value uses DefaultRetryPolicy.
type RetryPolicy struct {
	// Attempts is the total number of tries, including the first. At least one.
	Attempts int
	// Base is the first backoff. Successful later retries double it.
	Base time.Duration
	// Max caps one backoff, including a provider's Retry-After hint.
	Max time.Duration
}

// DefaultRetryPolicy tolerates a rolling rate-limit window: six tries with
// exponential backoff spend about one minute waiting, which is the window most
// hosted providers use for tokens-per-minute limits.
var DefaultRetryPolicy = RetryPolicy{
	Attempts: 6,
	Base:     2 * time.Second,
	Max:      30 * time.Second,
}

// normalized fills the zero fields with the defaults.
func (p RetryPolicy) normalized() RetryPolicy {
	if p.Attempts <= 0 {
		p.Attempts = DefaultRetryPolicy.Attempts
	}
	if p.Base <= 0 {
		p.Base = DefaultRetryPolicy.Base
	}
	if p.Max <= 0 {
		p.Max = DefaultRetryPolicy.Max
	}
	if p.Max < p.Base {
		p.Max = p.Base
	}
	return p
}

// providerStatus is the status surface ai.httpError exposes without exporting
// the transport type into the runtime.
type providerStatus interface {
	StatusCode() int
}

// providerRetryAfter is the provider's own wait hint, when it publishes one.
type providerRetryAfter interface {
	RetryAfter() int
}

// retryableProviderError reports whether the error is a transient transport
// condition rather than a rejection of the request itself. A malformed body, an
// authentication failure, or a context cancellation is never retried: repeating
// it would only multiply the failure.
func retryableProviderError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status providerStatus
	if errors.As(err, &status) {
		switch status.StatusCode() {
		case 408, 425, 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// delay is the wait before the next attempt. A published Retry-After is
// authoritative up to Max; otherwise the backoff doubles from Base.
func (p RetryPolicy) delay(attempt int, err error) time.Duration {
	if hint, ok := err.(providerRetryAfter); ok {
		if seconds := hint.RetryAfter(); seconds > 0 {
			d := time.Duration(seconds) * time.Second
			if d > p.Max {
				d = p.Max
			}
			return d
		}
	}
	d := p.Base
	for i := 0; i < attempt && d < p.Max; i++ {
		d *= 2
	}
	if d > p.Max {
		d = p.Max
	}
	return d
}

// openStream issues one provider request, retrying only a transient transport
// failure. The request is identical on every attempt, so a rate limit cannot
// duplicate model-visible work.
func (l *Loop) openStream(ctx context.Context, req ai.Request) (*ai.Stream, error) {
	policy := l.cfg.Retry.normalized()
	var lastErr error
	for attempt := 0; attempt < policy.Attempts; attempt++ {
		stream, err := l.cfg.Stream(ctx, req)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if attempt == policy.Attempts-1 || !retryableProviderError(err) {
			return nil, lastErr
		}
		delay := policy.delay(attempt, err)
		if !l.canWait(delay) {
			return nil, lastErr
		}
		if err := waitContext(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// canWait reports whether a retry delay still fits inside the run's wall-clock
// budget. Waiting past the deadline would turn a resource pause into a late
// provider failure.
func (l *Loop) canWait(delay time.Duration) bool {
	deadline := l.deadline()
	return deadline.IsZero() || time.Now().Add(delay).Before(deadline)
}

// waitContext sleeps for the backoff or returns the cancellation cause.
func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
