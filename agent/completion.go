package agent

import "fmt"

// CompletionState is the runtime's verdict on whether a run reached its
// objective.
//
// It is deliberately not the model's stop reason: a model can stop cleanly
// after a failed call or without changing anything. Only evidence the runtime
// observed can move a task to COMPLETED.
type CompletionState string

// Completion states.
const (
	// CompletionComplete means the runtime observed the requested result:
	// no unrecovered failure, no work left mid-call, and any change was
	// followed by a successful verification.
	CompletionComplete CompletionState = "COMPLETED"
	// CompletionNeedsReview means the run ended without enough evidence to
	// call the objective complete. It is not a failure and not a completion:
	// a later turn may resolve it.
	CompletionNeedsReview CompletionState = "NEEDS_REVIEW"
)

// Completion is a completion verdict with its evidence reason.
type Completion struct {
	// State is the verdict.
	State CompletionState
	// Reason explains a NEEDS_REVIEW verdict, in terms of observed evidence.
	Reason string
}

// Complete reports whether the verdict is COMPLETED.
func (c Completion) Complete() bool { return c.State == CompletionComplete }

// EvaluateCompletion derives a completion verdict from the turns a run
// produced.
//
// mutationIntent says the user's request clearly asked for a repository change.
// It is false for read-only and execution-only requests. Even when it is false,
// an observed mutation still requires verification: the runtime must not let a
// model mutate during a read request and then call the turn done.
//
// The rules are deliberately small and deterministic:
//
//   - an empty run, an execution error, or a final tool-calling turn is not
//     complete;
//   - a failed call must be followed by a successful call of the same tool or
//     by a successful verification, otherwise it remains a failure;
//   - a mutation request with no definite observed change is not complete;
//   - a change or an arbitrary process execution with no successful
//     verification after it is not complete.
func EvaluateCompletion(turns []Turn, mutationIntent bool) Completion {
	if len(turns) == 0 {
		return needsReview("the run produced no completed turn")
	}
	for _, turn := range turns {
		if turn.Err != nil {
			return needsReview("the run ended with an execution error: %v", turn.Err)
		}
	}
	last := turns[len(turns)-1]
	if len(last.Results) > 0 {
		return needsReview("the run stopped with tool calls still outstanding")
	}

	evidence := scanCompletionEvidence(turns, mutationIntent)
	if len(evidence.pendingFailures) > 0 {
		return needsReview("a tool call failed and no later success or verification recovered it")
	}
	if mutationIntent && !evidence.mutated {
		return needsReview("the request asked for a repository change but no change was observed")
	}
	if evidence.lastVerification < evidence.lastPotentialMutation {
		return needsReview("the repository change or execution was not followed by a successful verification")
	}
	return Completion{State: CompletionComplete}
}

// CompletionEvidence reports the definite mutation and verification evidence a
// run produced. It is used when a delegated child's result becomes the parent's
// tool outcome: the parent must see the child's change and whether the child
// verified it, rather than inheriting a clean-looking answer.
//
// mutationIntent controls whether an arbitrary process execution counts as a
// potential change that needs verification. It does not turn an execution into
// a definite mutation: a child that only ran tests did not change the
// repository, and the parent's mutation objective still needs a real change.
func CompletionEvidence(turns []Turn, mutationIntent bool) (mutated, verifiedAfterMutation bool) {
	evidence := scanCompletionEvidence(turns, mutationIntent)
	if !evidence.mutated {
		return false, false
	}
	return true, evidence.lastVerification >= evidence.lastPotentialMutation
}

type completionEvidence struct {
	// mutated is a definite change: a write/edit/delete, or a shell command
	// classified as mutating.
	mutated bool
	// lastPotentialMutation is the last definite change, or (for a mutation
	// objective) the last arbitrary execution, because an arbitrary process
	// may have changed something the runtime could not classify.
	lastPotentialMutation int
	lastVerification      int
	pendingFailures       map[string]int
}

// scanCompletionEvidence walks the turns once, recording the last observed
// change and verification and which tool failures remain unrecovered.
//
// A failure is recovered by a later success of the same tool (a retry that
// worked) or by a successful verification (the final state was checked). A
// verification after a failure therefore clears every pending failure, while a
// failure with no later evidence stays pending.
func scanCompletionEvidence(turns []Turn, mutationIntent bool) completionEvidence {
	evidence := completionEvidence{
		lastPotentialMutation: -1,
		lastVerification:      -1,
		pendingFailures:       map[string]int{},
	}
	position := 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError {
				evidence.pendingFailures[outcome.ToolName]++
				position++
				continue
			}

			definite := outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates)
			if definite {
				evidence.mutated = true
				evidence.lastPotentialMutation = position
			} else if mutationIntent && outcome.Result.Executes {
				// The runtime cannot prove this process did not change the
				// repository. A mutation objective must verify after it.
				evidence.lastPotentialMutation = position
			}
			if outcome.Result.Verifies {
				evidence.lastVerification = position
				// A successful verification is evidence that the final state
				// is sound, so earlier failures no longer block completion.
				evidence.pendingFailures = map[string]int{}
			}
			if evidence.pendingFailures[outcome.ToolName] > 0 {
				evidence.pendingFailures[outcome.ToolName]--
				if evidence.pendingFailures[outcome.ToolName] == 0 {
					delete(evidence.pendingFailures, outcome.ToolName)
				}
			}
			position++
		}
	}
	return evidence
}

func needsReview(format string, args ...any) Completion {
	return Completion{State: CompletionNeedsReview, Reason: fmt.Sprintf(format, args...)}
}
