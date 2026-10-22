package tui

import (
	"context"
	"errors"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/contract"
)

// taskAction is what a finished turn does to the durable task.
type taskAction int

const (
	taskActionComplete taskAction = iota
	taskActionReview
	taskActionPause
	taskActionFail
)

// turnTaskOutcome maps a run's result and evidence to a task transition.
//
// It is the single place the UI decides whether a turn completed, needs
// review, was interrupted, or failed. The model's stop reason is never enough:
// completion comes from agent.EvaluateTaskCompletion, which requires the
// contract's target evidence and a verification that covers it.
func turnTaskOutcome(err error, turns []agent.Turn, intent contract.IntentContract, snapshot *agent.TargetSnapshot, resolver agent.StructuralResolver) (taskAction, string) {
	// A cancelled or turn-limited run is a recoverable pause. It must not read
	// verification evidence after the run has been stopped: cancellation would
	// otherwise be observable only after a post-cancel filesystem walk.
	switch {
	case errors.Is(err, context.Canceled):
		return taskActionPause, "interrupted"
	case errors.Is(err, budget.ErrExhausted):
		// Budget exhaustion is a resource stop, not a verdict. Phase 4
		// completion evidence remains authoritative: a tree that stopped
		// after an independently verified objective may still complete, while
		// an unverified stop is resumable and never becomes success merely
		// because the grant ran out.
		//
		completion := agent.EvaluateBudgetCompletion(agent.CompletionInput{
			Turns:    turns,
			Contract: intent,
			Snapshot: snapshot,
			Resolver: resolver,
		}, true)
		if completion.Complete() {
			return taskActionComplete, ""
		}
		var exhausted *budget.ExhaustedError
		if errors.As(err, &exhausted) {
			return taskActionPause, exhausted.Error()
		}
		return taskActionPause, "budget exhausted"
	case errors.Is(err, agent.ErrTurnLimit):
		return taskActionPause, "turn limit reached"
	case err != nil:
		return taskActionFail, err.Error()
	}
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    turns,
		Contract: intent,
		Snapshot: snapshot,
		Resolver: resolver,
	})
	switch {
	case completion.Complete():
		return taskActionComplete, ""
	default:
		return taskActionReview, completion.Reason
	}
}
