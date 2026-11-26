package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// ProviderActivityPhase is the transport-visible provider state.
//
// It is a projection of the retry loop, not a second retry policy: the loop
// owns the attempt count, delay, and classification, and a UI only renders
// what it reports.
type ProviderActivityPhase string

// Provider activity phases.
const (
	// ProviderRequested means one attempt is currently in flight.
	ProviderRequested ProviderActivityPhase = "requested"
	// ProviderRetrying means an attempt failed and the loop is waiting before
	// the next identical attempt.
	ProviderRetrying ProviderActivityPhase = "retrying"
	// ProviderFailed means no further attempt will be made in this run.
	ProviderFailed ProviderActivityPhase = "failed"
	// ProviderStreaming means the request was accepted and its stream is open,
	// but no content has arrived yet.
	//
	// It is the state a slow or silent model produces after the transport
	// succeeds: the runtime is still waiting on the provider, not preparing the
	// workspace, and the UI can only say so if it is told.
	ProviderStreaming ProviderActivityPhase = "streaming"
)

// ProviderActivity reports one provider transport transition.
//
// Err is the transport failure, Status is the HTTP status when the provider
// supplied one, and Delay is the wait before the next attempt. The UI must not
// have to parse Err to know whether the request is merely rate-limited.
type ProviderActivity struct {
	Phase    ProviderActivityPhase
	Attempt  int
	Attempts int
	Delay    time.Duration
	Status   int
	Err      error
}

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
	// Budget bounds the whole open phase: every attempt and every backoff.
	//
	// Attempts alone does not bound time. A provider that accepts a connection
	// and never answers spends its response-header timeout on the first try, so
	// six attempts can mean tens of minutes of a busy turn with nothing to
	// show. The budget states how long the runtime is willing to keep a turn
	// waiting on a transport at all; a backoff that would cross it is not
	// started, and the failure is reported instead.
	Budget time.Duration
}

// DefaultRetryPolicy tolerates a rolling rate-limit window: six tries with
// exponential backoff spend about one minute waiting, which is the window most
// hosted providers use for tokens-per-minute limits. The budget bounds the
// pathological shape — a provider that accepts and then never answers — so a
// hung connection fails the turn in minutes rather than consuming the run.
var DefaultRetryPolicy = RetryPolicy{
	Attempts: 6,
	Base:     2 * time.Second,
	Max:      30 * time.Second,
	Budget:   5 * time.Minute,
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
	if p.Budget <= 0 {
		p.Budget = DefaultRetryPolicy.Budget
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
//
// Every transition is reported before the wait starts. A UI must not keep
// saying "preparing workspace" while the loop sleeps through a 429 backoff,
// and the budget stops a provider that accepts a request and never answers from
// holding the turn: the attempt count alone does not bound that shape, because
// one silent attempt can last as long as the transport's header timeout.
func (l *Loop) openStream(ctx context.Context, req ai.Request) (*ai.Stream, error) {
	policy := l.cfg.Retry.normalized()
	started := time.Now()
	var lastErr error
	for attempt := 0; attempt < policy.Attempts; attempt++ {
		if attempt > 0 && time.Since(started) >= policy.Budget {
			l.reportProvider(ProviderActivity{
				Phase: ProviderFailed, Attempt: attempt, Attempts: policy.Attempts,
				Status: providerHTTPStatus(lastErr), Err: lastErr,
			})
			return nil, providerBudgetError(policy.Budget, lastErr)
		}
		l.reportProvider(ProviderActivity{
			Phase: ProviderRequested, Attempt: attempt + 1, Attempts: policy.Attempts,
		})
		stream, err := l.cfg.Stream(ctx, req)
		if err == nil {
			l.reportProvider(ProviderActivity{
				Phase: ProviderStreaming, Attempt: attempt + 1, Attempts: policy.Attempts,
			})
			return stream, nil
		}
		lastErr = err
		// A cancelled turn is not a provider policy decision: the context is
		// the caller's stop and it outranks the retry budget or the attempt
		// count, or an interrupt would be reported as a transport failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if attempt == policy.Attempts-1 || !retryableProviderError(err) {
			l.reportProvider(ProviderActivity{
				Phase: ProviderFailed, Attempt: attempt + 1, Attempts: policy.Attempts,
				Status: providerHTTPStatus(err), Err: err,
			})
			return nil, lastErr
		}
		delay := policy.delay(attempt, err)
		if time.Since(started)+delay >= policy.Budget {
			l.reportProvider(ProviderActivity{
				Phase: ProviderFailed, Attempt: attempt + 1, Attempts: policy.Attempts,
				Status: providerHTTPStatus(err), Err: err,
			})
			return nil, providerBudgetError(policy.Budget, lastErr)
		}
		if !l.canWait(delay) {
			l.reportProvider(ProviderActivity{
				Phase: ProviderFailed, Attempt: attempt + 1, Attempts: policy.Attempts,
				Status: providerHTTPStatus(err), Err: lastErr,
			})
			return nil, lastErr
		}
		l.reportProvider(ProviderActivity{
			Phase: ProviderRetrying, Attempt: attempt + 1, Attempts: policy.Attempts,
			Delay: delay, Status: providerHTTPStatus(err), Err: err,
		})
		if err := waitContext(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// providerBudgetError names the bound that ended the open phase, so the reason
// the turn stopped is the runtime's own decision rather than a provider message
// about one attempt.
func providerBudgetError(budget time.Duration, cause error) error {
	return fmt.Errorf("the provider did not answer within %s; retrying was stopped: %w", budget, cause)
}

// reportProvider publishes one transport transition when a host is listening.
func (l *Loop) reportProvider(activity ProviderActivity) {
	if l.cfg.OnProviderActivity != nil {
		l.cfg.OnProviderActivity(activity)
	}
}

// providerHTTPStatus extracts the HTTP status from a provider rejection, or 0
// when the failure was a transport timeout without a response.
func providerHTTPStatus(err error) int {
	var status providerStatus
	if errors.As(err, &status) {
		return status.StatusCode()
	}
	return 0
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
