package agent

import (
	"context"
	"errors"

	"github.com/PizenLabs/ohgo/internal/control/budget"
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

// Run verdicts.
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
)

// ClassifyRun maps a finished run's error and evidence to one verdict and its
// evidence reason.
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
func ClassifyRun(err error, input CompletionInput, deadlineExpired bool) (RunVerdict, string) {
	switch {
	case errors.Is(err, context.Canceled):
		return RunCancelled, "interrupted"
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
		return RunPaused, "turn limit reached"
	case err != nil:
		return RunFailed, err.Error()
	}
	if deadlineExpired {
		return budgetVerdict(input, "deadline reached")
	}
	completion := EvaluateTaskCompletion(input)
	if completion.Complete() {
		return RunCompleted, ""
	}
	return RunNeedsReview, completion.Reason
}

// budgetVerdict applies the budget-completion rule to a resource stop: already
// complete evidence remains authoritative, and everything else stays paused
// under the caller's reason.
func budgetVerdict(input CompletionInput, reason string) (RunVerdict, string) {
	if EvaluateBudgetCompletion(input, true).Complete() {
		return RunCompleted, ""
	}
	return RunPaused, reason
}
