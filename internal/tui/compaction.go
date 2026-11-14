package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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

// CompactionPhase is the explicit /compact lifecycle. It is stored in State
// so the one activity line can represent the work while it is in flight,
// without a second progress subsystem.
type CompactionPhase string

const (
	// CompactionIdle means no manual compaction is running.
	CompactionIdle CompactionPhase = ""
	// CompactionRunning means the production compaction pipeline is active.
	CompactionRunning CompactionPhase = "running"
	// CompactionDone clears the activity once the result or failure is
	// published. The result itself is a deterministic transcript notice.
	CompactionDone CompactionPhase = "done"
)

// CompactionActivity is the live /compact activity state plus its truthful
// start time. It never carries result text: the structured outcome owns that.
type CompactionActivity struct {
	Phase     CompactionPhase
	StartedAt time.Time
}

// Active reports whether the compaction lifecycle still owns the activity
// line. A terminal result clears it.
func (c *CompactionActivity) Active() bool {
	return c != nil && c.Phase == CompactionRunning
}

// CompactionActive reports an in-flight explicit /compact operation.
func (s State) CompactionActive() bool { return s.Compaction.Active() }

// CompactionChanged moves the explicit /compact lifecycle. It is the only
// write path, so the activity line cannot claim a phase the command did not
// publish.
type CompactionChanged struct {
	Phase CompactionPhase
	// At is when the running phase began. Zero means unknown and suppresses
	// the elapsed segment rather than fabricating one.
	At time.Time
}

func (CompactionChanged) eventKind() string { return "compaction_changed" }

// CompactionOutcomeKind distinguishes a state-first rebuild from the semantic
// fallback and from a safe no-op. It is derived from the structured
// CompactResult, never from rendered text.
type CompactionOutcomeKind string

const (
	// CompactionComplete means authoritative durable state rebuilt the
	// context.
	CompactionComplete CompactionOutcomeKind = "complete"
	// CompactionDegraded means the semantic fallback ran because no durable
	// projection was available. The result is successful but continuity is
	// reduced.
	CompactionDegraded CompactionOutcomeKind = "degraded"
	// CompactionUnchanged means the context was already as compact as the
	// authoritative projection.
	CompactionUnchanged CompactionOutcomeKind = "unchanged"
)

// CompactionOutcome is the deterministic, renderer-facing view of a successful
// compaction. Every field comes from CompactResult; the renderer does not
// re-measure context or infer preservation from prose.
type CompactionOutcome struct {
	Kind              CompactionOutcomeKind
	StatePreserved    bool
	EvidencePreserved int
	ContextRebuilt    bool
	RedundancyRemoved int
	BeforeTokens      int
	AfterTokens       int
	Reduction         float64
}

// CompactionOutcomeFrom maps the authoritative result to the presentation
// contract.
func CompactionOutcomeFrom(result agent.CompactResult) CompactionOutcome {
	out := CompactionOutcome{
		Kind:              CompactionComplete,
		EvidencePreserved: result.Metrics.EvidencePreserved,
		RedundancyRemoved: result.Compacted + result.Trimmed,
		BeforeTokens:      result.BeforeTokens,
		AfterTokens:       result.AfterTokens,
	}
	if result.BeforeTokens > 0 {
		out.Reduction = float64(result.BeforeTokens-result.AfterTokens) / float64(result.BeforeTokens) * 100
	}
	switch result.Mode {
	case agent.ModeReconstructed:
		out.StatePreserved = true
		out.ContextRebuilt = true
	case agent.ModeSummarized:
		out.Kind = CompactionDegraded
		out.ContextRebuilt = true
	case agent.ModeUnchanged:
		out.Kind = CompactionUnchanged
		out.StatePreserved = true
	default:
		// A successful result with no recognized mode still came through the
		// production pipeline; do not invent preservation claims for it.
	}
	return out
}

// CompactionResultText renders the deterministic final /compact block.
//
// The lifecycle is represented by the activity line while the operation runs;
// this block is the terminal result. It is intentionally short, uses only
// facts the structured outcome carries, and never includes the internal
// pressure band.
func CompactionResultText(result agent.CompactResult) string {
	return CompactionOutcomeFrom(result).Text()
}

// Text renders one CompactionOutcome as a concise terminal block.
func (o CompactionOutcome) Text() string {
	var b strings.Builder
	switch o.Kind {
	case CompactionDegraded:
		b.WriteString("Context compacted with reduced continuity\n")
		b.WriteString("  ! Durable task state was unavailable; a semantic summary was used\n")
	case CompactionUnchanged:
		b.WriteString("Context already compact\n")
	default:
		b.WriteString("Context compacted\n")
	}
	if o.StatePreserved {
		b.WriteString("  ✓ Task state preserved\n")
	}
	if o.EvidencePreserved > 0 {
		fmt.Fprintf(&b, "  ✓ Evidence preserved (%d items)\n", o.EvidencePreserved)
	}
	if o.ContextRebuilt {
		if o.Kind == CompactionDegraded {
			b.WriteString("  ✓ Conversation context rebuilt from the summary\n")
		} else {
			b.WriteString("  ✓ Conversation context rebuilt from task state\n")
		}
	}
	if o.RedundancyRemoved > 0 {
		b.WriteString("  ✓ Redundant history removed\n")
	}
	if o.Kind == CompactionUnchanged {
		b.WriteString("  ✓ No further reduction was needed\n")
	}
	b.WriteByte('\n')
	switch {
	case o.BeforeTokens > 0 && o.AfterTokens > 0:
		fmt.Fprintf(&b, "%s → %s est. tokens · %.2f%% reduced\n",
			exactCount(o.BeforeTokens), exactCount(o.AfterTokens), o.Reduction)
	case o.BeforeTokens > 0:
		fmt.Fprintf(&b, "%s est. tokens\n", exactCount(o.BeforeTokens))
	default:
		b.WriteString("context reduced\n")
	}
	b.WriteString("\nReady to continue.")
	return b.String()
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
//
// Every failure leaves the durable task state and the working conversation
// untouched: compaction produces a candidate, and only a validated result is
// installed. The message states that boundary instead of implying the task
// itself failed.
func CompactionErrorText(err error) string {
	if err == nil {
		return ""
	}
	reason := ""
	switch {
	case errors.Is(err, agent.ErrEmptySummary):
		reason = "no valid compaction summary was produced"
	case errors.Is(err, agent.ErrContextOverflow):
		reason = "context remains too large after bounded compaction"
	case errors.Is(err, agent.ErrCompactionIneffective):
		reason = "context could not be reduced further"
	case errors.Is(err, budget.ErrExhausted):
		reason = "model-call budget exhausted"
	case errors.Is(err, agent.ErrNothingToCompact):
		return "Nothing to compact: the transcript is already minimal."
	case errors.Is(err, agent.ErrNoContextWindow):
		reason = "the model has no usable context window"
	case errors.Is(err, agent.ErrNoSummarizer):
		reason = "no durable task state or summarizer is available"
	case errors.Is(err, agent.ErrInvalidProjection):
		reason = "the saved task state could not be reconstructed safely"
	default:
		reason = strings.TrimSpace(strings.TrimPrefix(err.Error(), "agent: "))
		return "Context compaction incomplete: " + reason + "\nTask state was not modified; conversation left unchanged."
	}
	return "Context compaction incomplete: " + reason + ".\nTask state was not modified; conversation left unchanged."
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
