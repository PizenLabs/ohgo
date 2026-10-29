package tui

import (
	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
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
	return turnTaskOutcomeWithReview(err, turns, intent, snapshot, resolver, "")
}

// turnTaskOutcomeWithReview is turnTaskOutcome with the cognitive layer's
// evidence verdict. A non-empty review can only downgrade a normal completion;
// a proven mutation on a resource stop remains governed by
// EvaluateBudgetCompletion, which deliberately ignores it.
//
// The stop classification and the completion rules belong to agent.ClassifyRun;
// this function only projects its verdict into the durable task lifecycle.
func turnTaskOutcomeWithReview(err error, turns []agent.Turn, intent contract.IntentContract, snapshot *agent.TargetSnapshot, resolver agent.StructuralResolver, evidenceReview string) (taskAction, string) {
	verdict, reason := agent.ClassifyRun(err, agent.CompletionInput{
		Turns:          turns,
		Contract:       intent,
		Snapshot:       snapshot,
		Resolver:       resolver,
		EvidenceReview: evidenceReview,
	}, false)

	switch verdict {
	case agent.RunCompleted:
		return taskActionComplete, ""
	case agent.RunNeedsReview:
		return taskActionReview, reason
	case agent.RunCancelled:
		// Cancellation is a pause, not a failure verdict: the human's stop is
		// the more truthful transition for a non-terminal run.
		return taskActionPause, reason
	case agent.RunFailed:
		return taskActionFail, reason
	default: // agent.RunPaused
		return taskActionPause, reason
	}
}
