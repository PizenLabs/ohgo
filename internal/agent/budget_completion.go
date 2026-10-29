package agent

import (
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// EvaluateBudgetCompletion is the Phase 5 completion interaction.
//
// It delegates every semantic rule to EvaluateTaskCompletion; the only thing
// it changes is the run shape when a resource stop removed the closing turn.
// A verification turn is normally followed by one more model turn so the model
// can answer. Budget exhaustion can make that turn impossible. When the run
// already contains a definite mutation followed by a successful verification,
// the runtime has the same evidence a closing stop would have carried, so the
// missing stop is supplied as an empty final turn. Without both bits the turns
// are evaluated unchanged: a read-only answer that never happened, or an
// unverified change, is not completion.
//
// exhausted must come from budget.ErrExhausted (or a legacy turn limit the
// caller classifies as a resource stop). The function never turns exhaustion
// itself into success; EvaluateTaskCompletion still decides.
func EvaluateBudgetCompletion(input CompletionInput, exhausted bool) Completion {
	if !exhausted {
		return EvaluateTaskCompletion(input)
	}
	input.Turns = budgetCompletionTurns(input.Turns, input.Contract)
	// A resource stop is not an evidence gap by itself. EvaluateTaskCompletion
	// still applies every evidence rule; the cognitive review must not veto a
	// change that was already proven before the resource ran out.
	input.EvidenceReview = ""
	return EvaluateTaskCompletion(input)
}

// budgetCompletionTurns appends a synthetic clean stop only when the bounded
// completion evidence is already conclusive.
func budgetCompletionTurns(turns []Turn, intent contract.IntentContract) []Turn {
	if intent.Kind != contract.ObjectiveMutation || len(turns) == 0 {
		return turns
	}
	last := turns[len(turns)-1]
	if len(last.Results) == 0 {
		return turns
	}
	mutated, verified := CompletionEvidence(turns, true)
	if !mutated || !verified {
		return turns
	}
	out := make([]Turn, len(turns), len(turns)+1)
	copy(out, turns)
	return append(out, Turn{Assistant: ai.AssistantMessage{StopReason: ai.StopEndTurn}})
}
