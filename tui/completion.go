package tui

import (
	"context"
	"errors"

	"github.com/PizenLabs/ohgo/agent"
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
// completion comes from agent.EvaluateCompletion, which requires observed
// mutation and verification evidence for a change request.
func turnTaskOutcome(err error, turns []agent.Turn, mutationIntent bool) (taskAction, string) {
	completion := agent.EvaluateCompletion(turns, mutationIntent)
	switch {
	case err == nil && completion.Complete():
		return taskActionComplete, ""
	case err == nil:
		return taskActionReview, completion.Reason
	case errors.Is(err, context.Canceled):
		return taskActionPause, "interrupted"
	case errors.Is(err, agent.ErrTurnLimit):
		return taskActionPause, "turn limit reached"
	default:
		return taskActionFail, err.Error()
	}
}
