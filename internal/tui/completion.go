package tui

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// taskAction is what a finished turn does to the durable task.
type taskAction int

const (
	taskActionComplete taskAction = iota
	taskActionReview
	taskActionPause
	taskActionFail
	// taskActionPhaseComplete means the turn satisfied the part of the task
	// the active authority can perform. The persisted objective still needs a
	// higher authority, so the task stays active and no proof is recorded.
	taskActionPhaseComplete
)

// turnTaskOutcome maps a run's result and evidence to a task transition.
//
// It is the single place the UI decides whether a turn completed, needs
// review, was interrupted, or failed. The model's stop reason is never enough:
// completion comes from agent.EvaluateTaskCompletion, which requires the
// contract's target evidence and a verification that covers it.
func turnTaskOutcome(err error, turns []agent.Turn, intent contract.IntentContract, snapshot *agent.TargetSnapshot, resolver agent.StructuralResolver) (taskAction, string) {
	return turnTaskOutcomeWithReview(err, turns, intent, snapshot, resolver, "", false, agent.EvidenceProvenance{})
}

// turnTaskOutcomeWithReview is turnTaskOutcome with the cognitive layer's
// evidence verdict. A non-empty review can only downgrade a normal completion;
// a proven mutation on a resource stop remains governed by
// EvaluateBudgetCompletion, which deliberately ignores it.
//
// priorChange says the durable task already observed a current change before
// this run. It lets a resumed verification-only turn reach the durable
// completion boundary; it never proves the objective by itself.
//
// The stop classification and the completion rules belong to agent.ClassifyRun;
// this function only projects its verdict into the durable task lifecycle.
func turnTaskOutcomeWithReview(err error, turns []agent.Turn, intent contract.IntentContract, snapshot *agent.TargetSnapshot, resolver agent.StructuralResolver, evidenceReview string, priorChange bool, provenance agent.EvidenceProvenance) (taskAction, string) {
	return turnTaskOutcomeForAuthority(err, turns, intent, snapshot, resolver, evidenceReview, priorChange, provenance, "")
}

// turnTaskOutcomeForAuthority is the completion boundary when the live
// authority is known. A task can carry a mutation objective while the current
// turn runs under ASK or FUZZ, where a repository change is not permitted. The
// run is then judged as the informational phase the authority can perform: a
// substantive answer keeps the task active instead of reporting a change the
// authority never allowed or claiming an objective that is still outstanding.
//
// An empty or invalid authority disables the phase projection. The caller has
// no trustworthy capability context, so the persisted objective is evaluated
// unchanged.
func turnTaskOutcomeForAuthority(err error, turns []agent.Turn, intent contract.IntentContract, snapshot *agent.TargetSnapshot, resolver agent.StructuralResolver, evidenceReview string, priorChange bool, provenance agent.EvidenceProvenance, authority string) (taskAction, string) {
	if err == nil {
		if active, parseErr := workspace.Parse(authority); parseErr == nil {
			if phase, deferred := workspace.PhaseContract(intent, active); deferred {
				verdict, reason := agent.ClassifyRun(nil, agent.CompletionInput{
					Turns:          turns,
					Contract:       phase,
					Snapshot:       snapshot,
					Resolver:       resolver,
					EvidenceReview: evidenceReview,
					PriorChange:    priorChange,
					Provenance:     provenance,
				}, false)
				return projectTaskVerdict(verdict, reason, true)
			}
		}
	}
	verdict, reason := agent.ClassifyRun(err, agent.CompletionInput{
		Turns:          turns,
		Contract:       intent,
		Snapshot:       snapshot,
		Resolver:       resolver,
		EvidenceReview: evidenceReview,
		PriorChange:    priorChange,
		Provenance:     provenance,
	}, false)
	// A conditional mutation that has not entered its change branch completed
	// the investigation phase, not the persisted objective. The mutation is
	// still deferred, so the task keeps its identity and stays ACTIVE rather
	// than recording a proof the change branch never earned.
	if verdict == agent.RunCompleted && err == nil && intent.Kind == workspace.ObjectiveMutation &&
		intent.ConditionalMutation && !priorChange {
		if mutated, _ := agent.CompletionEvidence(turns, true); !mutated {
			return taskActionPhaseComplete, ""
		}
	}
	return projectTaskVerdict(verdict, reason, false)
}

// projectTaskVerdict maps the shared run verdict into the durable task
// lifecycle. phase distinguishes a completed authority phase from completion
// of the persisted objective; only the latter may record a proof.
func projectTaskVerdict(verdict agent.RunVerdict, reason string, phase bool) (taskAction, string) {
	switch verdict {
	case agent.RunCompleted:
		if phase {
			return taskActionPhaseComplete, ""
		}
		return taskActionComplete, ""
	case agent.RunNeedsReview:
		return taskActionReview, reason
	case agent.RunCancelled:
		// Cancellation is a pause, not a failure verdict: the human's stop is
		// the more truthful transition for a non-terminal run.
		return taskActionPause, reason
	case agent.RunBlocked:
		// The objective needs a capability this runtime cannot provide. The
		// task becomes BLOCKED (a failed lifecycle, never a review of work
		// that did not happen), and the reason carries the machine-readable
		// class so a diagnostic path can distinguish it from a model failure.
		return taskActionFail, agent.RunBlocked.String() + ": " + reason
	case agent.RunFailed:
		return taskActionFail, reason
	default: // agent.RunPaused
		return taskActionPause, reason
	}
}

// hasOpenTaskItems reports whether a projection still shows work that is not
// done. A completion refusal caused by outstanding work keeps the task active;
// any other refusal is reviewable.
func hasOpenTaskItems(projection TaskProjection) bool {
	for _, item := range projection.Items {
		if !strings.EqualFold(strings.TrimSpace(item.State), taskStateDone) {
			return true
		}
	}
	return false
}
