package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// RunVerdict is the terminal classification of a finished run, independent of
// the vocabulary a particular shell reports it in.
//
// The stateless runner and the durable TUI observe the same run and must not
// disagree about *why* it stopped: a budget stop that is PAUSED in one place
// and COMPLETED in the other would let the same evidence produce two
// contradictory transcripts. The classification therefore lives here, next to
// the evaluators it delegates to, and each shell only projects a verdict into
// its own types. Vocabularies stay separate; the decision does not.
type RunVerdict int

// RunVerdicts and their classes.
const (
	// RunCompleted means the contract was satisfied by observed evidence.
	RunCompleted RunVerdict = iota
	// RunNeedsReview means the run stopped without enough evidence to call the
	// objective complete. It is not a failure and not a completion.
	RunNeedsReview
	// RunPaused means a resource stop or a turn limit ended the run before a
	// verdict could be reached.
	RunPaused
	// RunFailed means an execution or provider failure ended the run.
	RunFailed
	// RunCancelled means the user interrupted the run. It is distinct from
	// RunPaused because a shell may report it differently (a process exit
	// status, or a transcript marked interrupted), but neither may treat it as
	// success.
	RunCancelled
	// RunBlocked means the objective cannot be executed by this runtime: a
	// required capability is unavailable or the live authority forbids it. It
	// is not a model failure and not a review: nothing ran, and repeating the
	// turn cannot change the outcome until the human changes the runtime or
	// the objective.
	RunBlocked
)

// String returns the machine-readable class of the verdict.
//
// It is the vocabulary durable records and diagnostics carry, so a shell never
// has to re-invent the class name or parse a reason string. A blocked capability
// is `CAPABILITY_UNAVAILABLE`, which is deliberately not the same word as a
// failed run or a review.
func (v RunVerdict) String() string {
	switch v {
	case RunCompleted:
		return "COMPLETED"
	case RunNeedsReview:
		return "NEEDS_REVIEW"
	case RunPaused:
		return "PAUSED"
	case RunFailed:
		return "FAILED"
	case RunCancelled:
		return "CANCELLED"
	case RunBlocked:
		return "CAPABILITY_UNAVAILABLE"
	}
	return "UNKNOWN"
}

// ClassifyRun maps a finished run's error and evidence to one verdict, its
// evidence reason, and the machine-readable claims behind a review.
//
// The order is load-bearing: cancellation and resource stops are decided first
// because evaluating evidence after a stop would perform a filesystem walk the
// user already cancelled. A resource stop is not success by itself — every
// completion rule still runs, via EvaluateBudgetCompletion, and only observed
// evidence can upgrade it to RunCompleted.
//
// deadlineExpired covers the deadline stop the loop reports as a nil error,
// because a wall-clock bound is still authoritative. A caller that has already
// normalised the deadline into context.DeadlineExceeded passes false and gets
// identical behaviour through the error branch.
//
// The issues are non-empty only when the verdict is a review produced by the
// deterministic answer audits; every other review (lifecycle and resource
// conditions) has no claim list to carry.
func ClassifyRun(err error, input CompletionInput, deadlineExpired bool) (RunVerdict, string, []contract.GroundingIssue) {
	switch {
	case errors.Is(err, context.Canceled):
		return RunCancelled, "interrupted", nil
	case errors.Is(err, context.DeadlineExceeded):
		return budgetVerdict(input, "deadline reached")
	case errors.Is(err, budget.ErrExhausted):
		reason := "budget exhausted"
		var exhausted *budget.ExhaustedError
		if errors.As(err, &exhausted) {
			reason = exhausted.Error()
		}
		return budgetVerdict(input, reason)
	case errors.Is(err, ErrTurnLimit):
		return RunPaused, "turn limit reached", nil
	case errors.Is(err, ErrTaskBlocked):
		reason := strings.TrimSpace(strings.TrimPrefix(err.Error(), ErrTaskBlocked.Error()+": "))
		if reason == "" {
			reason = "the runtime closed the task because no further action can make progress"
		}
		return RunNeedsReview, reason, nil
	case errors.Is(err, ErrCapabilityUnavailable):
		// The control plane refused the turn before the provider was called.
		// Nothing executed and no model output exists, so this is neither a
		// model failure nor a review of work: the objective is unreachable in
		// this runtime until a capability or the objective changes.
		reason := strings.TrimSpace(strings.TrimPrefix(err.Error(), ErrCapabilityUnavailable.Error()+": "))
		if reason == "" {
			reason = "a required capability is unavailable in this runtime"
		}
		return RunBlocked, reason, nil
	case errors.Is(err, ErrContextOverflow):
		// The task state is intact; the runtime could not construct a request
		// that fits this model window. Report review rather than completion or
		// a terminal failure, so a larger model or a narrower objective can
		// continue the same task.
		reason := strings.TrimSpace(strings.TrimPrefix(err.Error(), ErrContextOverflow.Error()+": "))
		if reason == "" {
			reason = "the context could not be reduced to fit the model window"
		}
		return RunNeedsReview, reason, nil
	case errors.Is(err, ErrNoModelOutput), errors.Is(err, ErrExecutionIntentWithoutTool),
		errors.Is(err, ErrToolNormalizationFailed), errors.Is(err, ErrToolCallIncomplete),
		errors.Is(err, ErrProviderStalled), errors.Is(err, ai.ErrStreamStalled):
		// These are model/protocol/transport lifecycle failures before any
		// requested action executed. They are non-terminal: the task identity,
		// evidence, and authority remain valid, so the human can retry the
		// turn and earlier mutations stay authoritative. A stalled compaction
		// summary reaches here wrapped by the compaction layer, which is why
		// the provider-level sentinel is checked too.
		return RunNeedsReview, strings.TrimPrefix(err.Error(), "agent: "), nil
	case errors.Is(err, ErrProviderRequest), errors.Is(err, ErrProviderStream):
		// A provider failure before any turn completed is the same terminal
		// run failure the headless runner has always reported. After at least
		// one completed turn, the task is not failed: real work and possibly
		// a mutation exist, and marking them FAILED would make a resumable
		// interruption look unrecoverable. Review preserves the checkpoint
		// and the truthful reason.
		if len(input.Turns) > 0 || input.Mutations.Captured {
			return RunNeedsReview, strings.TrimPrefix(err.Error(), "agent: "), nil
		}
		return RunFailed, err.Error(), nil
	case err != nil:
		return RunFailed, err.Error(), nil
	}
	if deadlineExpired {
		return budgetVerdict(input, "deadline reached")
	}
	completion := EvaluateTaskCompletion(input)
	if completion.Complete() {
		return RunCompleted, "", nil
	}
	return RunNeedsReview, completion.Reason, completion.Issues
}

// budgetVerdict applies the budget-completion rule to a resource stop: already
// complete evidence remains authoritative, and everything else stays paused
// under the caller's reason.
func budgetVerdict(input CompletionInput, reason string) (RunVerdict, string, []contract.GroundingIssue) {
	if EvaluateBudgetCompletion(input, true).Complete() {
		return RunCompleted, "", nil
	}
	return RunPaused, reason, nil
}
