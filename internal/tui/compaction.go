package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// CompactionRequest is one manual /compact request.
//
// The Program supplies the live history and, when the durable task layer is
// installed, the authoritative projection. The harness owns the policy,
// model-call budget, and CompactWith call. The command never chooses a
// different compaction implementation.
type CompactionRequest struct {
	// History is the active conversation to reduce.
	History []agent.Message
	// Projection is the authoritative durable state when HasProjection is set.
	Projection agent.ContextProjection
	// HasProjection reports that Projection is valid and must be preferred
	// over semantic summarization.
	HasProjection bool
}

// Compactor runs one manual compaction through the production pipeline.
type Compactor func(ctx context.Context, req CompactionRequest) (agent.CompactResult, error)

// CompactionResultText renders one successful /compact outcome.
//
// It is intentionally short: the user asked for the result, not the pipeline.
// Token figures are estimates because the runtime has no provider-billed
// measurement for a request that was never sent.
func CompactionResultText(result agent.CompactResult) string {
	if result.Mode == agent.ModeUnchanged {
		return fmt.Sprintf("Context already compact · %s est. tokens", exactCount(result.BeforeTokens))
	}
	if result.BeforeTokens <= 0 {
		return "Compacted · context reduced"
	}
	reduction := float64(result.BeforeTokens-result.AfterTokens) / float64(result.BeforeTokens) * 100
	text := fmt.Sprintf("Compacted · %s → %s est. tokens · %.2f%% reduced",
		exactCount(result.BeforeTokens), exactCount(result.AfterTokens), reduction)
	if result.Metrics.Trigger != "" {
		text += " · " + string(result.Metrics.Trigger)
	}
	return text
}

// exactCount renders a token estimate with thousands separators. Compaction is
// a result, not a status-line approximation, so it does not round to "27k".
func exactCount(n int) string {
	if n < 0 {
		return "-" + exactCount(-n)
	}
	digits := strconv.Itoa(n)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

// CompactionErrorText maps a structured compaction failure to a concise,
// user-facing message. The typed error remains available to logs and tests.
func CompactionErrorText(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, agent.ErrEmptySummary):
		return "Compact failed: no valid compaction summary was produced."
	case errors.Is(err, agent.ErrContextOverflow):
		return "Compact failed: context remains too large after bounded compaction."
	case errors.Is(err, agent.ErrCompactionIneffective):
		return "Compact failed: context could not be reduced further."
	case errors.Is(err, budget.ErrExhausted):
		return "Compact failed: model-call budget exhausted."
	case errors.Is(err, agent.ErrNothingToCompact):
		return "Nothing to compact: the transcript is already minimal."
	case errors.Is(err, agent.ErrNoContextWindow):
		return "Compact failed: the model has no usable context window."
	case errors.Is(err, agent.ErrNoSummarizer):
		return "Compact failed: no durable projection or summarizer is available."
	case errors.Is(err, agent.ErrInvalidProjection):
		return "Compact failed: the saved task state could not be reconstructed safely."
	default:
		return "Compact failed: " + strings.TrimSpace(strings.TrimPrefix(err.Error(), "agent: "))
	}
}

// compactionProjection builds the authoritative projection for /compact from
// the same durable task state the automatic trigger uses.
//
// It returns false when no durable task exists or the projection cannot be
// validated; the command then falls back to the existing semantic pipeline.
func (p *Program) compactionProjection() (agent.ContextProjection, bool) {
	if p.task.Current == nil || p.task.Context == nil {
		return agent.ContextProjection{}, false
	}
	current, ok := p.task.Current()
	if !ok {
		return agent.ContextProjection{}, false
	}
	blocks := p.task.Context()
	if len(blocks) == 0 {
		return agent.ContextProjection{}, false
	}

	var evidence *cognition.EvidenceState
	if p.task.Evidence != nil {
		evidence = p.task.Evidence().Clone()
	}

	intent := current.Contract
	if !current.HasContract || strings.TrimSpace(intent.Objective) == "" {
		intent = contract.IntentContract{Objective: current.Objective, Kind: contract.ObjectiveInformational}
	}
	authority := strings.ToLower(strings.TrimSpace(p.state.Status.Workspace))
	if authority == "" {
		authority = strings.ToLower(strings.TrimSpace(current.Role))
	}
	if authority == "" {
		authority = "ask"
	}
	shape := cognition.BuildShape(intent, cognition.ShapeParams{
		Authority: authority,
		Intent:    "compact",
	})
	projection := cognition.CompactionProjection(shape, evidence, current.ObjectiveState, "", blocks)
	if projection.Objective == "" {
		projection.Objective = current.Objective
	}
	if err := projection.Validate(); err != nil {
		return agent.ContextProjection{}, false
	}
	// The control plane validates the candidate against a baseline built from
	// the same authoritative fields. A projection that changed authority,
	// objective, or completion is refused and the command falls back.
	baseline := cognition.CompactionProjection(shape, nil, current.ObjectiveState, "", blocks)
	if err := cognition.ValidateCompactionProjection(projection, baseline); err != nil {
		return agent.ContextProjection{}, false
	}
	return projection, true
}
