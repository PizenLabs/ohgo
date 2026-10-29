package tools

import (
	"errors"
	"fmt"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

// admitExternal charges one external capability call to the root grant.
//
// The loop has already charged the tool call. This charge is separate because
// one tool call can make several bounded external attempts (a retry or a
// discovery exchange), and a retry must not be free.
func admitExternal(ctx agent.Context) error {
	return ctx.Budget.Admit(budget.DimensionExternalCalls)
}

// externalByteAllowance returns the per-response byte allowance bounded by the
// remaining aggregate grant.
//
// It is a read, not a reservation: admission after the response is still the
// enforcement point. Clamping the reader is what keeps a single response from
// observing bytes the tree can no longer afford.
func externalByteAllowance(ctx agent.Context, requested int) (int, error) {
	if requested <= 0 {
		return 0, knowledge.Fail(knowledge.FailureInvalidRequest, false, "external byte bound must be positive")
	}
	remaining := ctx.Budget.Remaining(budget.DimensionExternalBytes)
	if remaining == 0 {
		return 0, &budget.ExhaustedError{
			Dimension: budget.DimensionExternalBytes,
			Limit:     ctx.Budget.Limits().ExternalBytes,
			Used:      ctx.Budget.Snapshot().Used.ExternalBytes,
		}
	}
	if remaining > 0 && remaining < requested {
		return remaining, nil
	}
	return requested, nil
}

// chargeExternalBytes consumes the bytes actually observed.
func chargeExternalBytes(ctx agent.Context, observed int) error {
	return ctx.Budget.AdmitN(budget.DimensionExternalBytes, observed)
}

// exhaustedResult turns a budget refusal into the loop-visible resource stop.
//
// It is an error result because refusing work is not success; the Exhausted
// pointer is what lets the loop record the stop dimension truthfully.
func exhaustedResult(err error) agent.Result {
	var exhausted *budget.ExhaustedError
	if errors.As(err, &exhausted) {
		return agent.Result{
			Text:    fmt.Sprintf("external capability refused: %v", err),
			IsError: true,
			Details: map[string]any{
				"status":         "budget_exhausted",
				"classification": "budget_exhausted",
				"dimension":      exhausted.Dimension,
				"limit":          exhausted.Limit,
				"used":           exhausted.Used,
			},
			Exhausted: exhausted,
		}
	}
	return agent.ErrorResult("external capability refused: %v", err)
}

// boundInt returns fallback when value is non-positive, and clamps upward.
func boundInt(value, fallback, max int) int {
	if value <= 0 {
		value = fallback
	}
	if value > max {
		value = max
	}
	return value
}

// boundDuration returns fallback when value is non-positive, and clamps upward.
func boundDuration(value, fallback, max time.Duration) time.Duration {
	if value <= 0 {
		value = fallback
	}
	if value > max {
		value = max
	}
	return value
}

// withAudit attaches a bounded duration to an external result.
//
// It runs after the capability returns, so it covers every path (success,
// classified failure, cancellation, and exhaustion) without repeating timing
// code. The details map is runtime-owned; no payload is copied into it.
func withAudit(result agent.Result, started time.Time, ctx agent.Context) agent.Result {
	details, ok := result.Details.(map[string]any)
	if !ok || details == nil {
		details = map[string]any{}
	}
	ended := time.Now()
	details["started_at"] = started.UTC()
	details["ended_at"] = ended.UTC()
	details["duration_ms"] = ended.Sub(started).Milliseconds()
	if ctx.Budget != nil {
		snapshot := ctx.Budget.Snapshot()
		details["budget"] = map[string]any{
			"external_calls_used": snapshot.Used.ExternalCalls,
			"external_bytes_used": snapshot.Used.ExternalBytes,
			"external_calls_left": snapshot.Remaining.ExternalCalls,
			"external_bytes_left": snapshot.Remaining.ExternalBytes,
		}
	}
	result.Details = details
	return result
}

// externalFailureResult renders a classified failure.
//
// The class is part of the model-visible text, not a hidden detail: a timeout,
// a rate limit, and an empty result must not look the same.
func externalFailureResult(failure knowledge.Failure, details map[string]any) agent.Result {
	if details == nil {
		details = map[string]any{}
	}
	details["status"] = "failed"
	details["failure"] = failure
	// Keep a capability classification when the tool already published one
	// (MCP read/execute/mutate); the failure class is always available under
	// its own key.
	if _, ok := details["classification"]; !ok {
		details["classification"] = string(failure.Class)
	}
	details["failure_classification"] = string(failure.Class)
	text := fmt.Sprintf("external capability failed: %s", failure.Class)
	if failure.Message != "" {
		text += ": " + failure.Message
	}
	if failure.Retryable {
		text += " (retryable)"
	}
	return agent.Result{Text: text, IsError: true, Details: details}
}

// externalErrorResult classifies any external error for the model.
//
// Budget exhaustion and cancellation are kept distinct from provider failures:
// a refused resource stop is not a rate limit, and a cancelled call is not a
// failed one.
func externalErrorResult(ctx agent.Context, err error, details map[string]any) agent.Result {
	var exhausted *budget.ExhaustedError
	if errors.As(err, &exhausted) {
		return exhaustedResult(err)
	}
	if ctx.Ctx.Err() != nil {
		return externalCancelledResult(ctx, details)
	}
	var failure knowledge.Failure
	if errors.As(err, &failure) {
		if failure.Class == knowledge.FailureCancelled {
			return externalCancelledResult(ctx, details)
		}
		return externalFailureResult(failure, details)
	}
	return externalFailureResult(knowledge.Fail(knowledge.FailureUnavailable, true, "%v", err), details)
}

// externalCancelledResult is a cancellation, never a successful result.
func externalCancelledResult(ctx agent.Context, details map[string]any) agent.Result {
	if details == nil {
		details = map[string]any{}
	}
	details["status"] = "cancelled"
	details["cancelled"] = true
	return agent.Result{
		Text:    fmt.Sprintf("cancelled: %v", ctx.Ctx.Err()),
		IsError: true,
		Details: details,
	}
}

// externalEvidenceDetails is the bounded audit projection attached to a result.
//
// It carries identities and counts, not payloads or credentials. RequestID is a
// hash of the non-secret request identity; it is not a bearer token.
func externalEvidenceDetails(capability knowledge.Capability, operation knowledge.Operation, requestID string, evidence []knowledge.Evidence) map[string]any {
	return map[string]any{
		"status":     "ok",
		"capability": string(capability),
		"operation":  string(operation),
		"request_id": requestID,
		"trust":      string(knowledge.TrustUntrusted),
		"evidence":   evidence,
	}
}
