package cognition

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Reasoning demand is the control plane's abstract estimate of how much
// thinking the current work needs. It is deliberately not a provider value:
// every provider publishes its own vocabulary, and a runtime that names one
// universal ladder either invents a level or sends one the model rejects.
//
// The demand is independent of the role. An ASK analysis of a race condition
// and a BUILD refactor of the task lifecycle may both need high reasoning; a
// one-line BUILD rename needs none. Role is authority, not difficulty.
type ReasoningDemand int

// Reasoning demands, from least to most.
const (
	// ReasoningMinimal is enough for a direct answer or a single mechanical
	// change.
	ReasoningMinimal ReasoningDemand = iota
	// ReasoningModerate is enough for multi-step work with a verification or a
	// staged objective.
	ReasoningModerate
	// ReasoningHigh is warranted when several independent components interact,
	// the objective is ambiguous, or the task carries unresolved evidence.
	ReasoningHigh
)

// Valid reports whether the demand is one this vocabulary defines.
func (d ReasoningDemand) Valid() bool {
	switch d {
	case ReasoningMinimal, ReasoningModerate, ReasoningHigh:
		return true
	}
	return false
}

// Label is the diagnostic name of the demand.
func (d ReasoningDemand) Label() string {
	switch d {
	case ReasoningMinimal:
		return "minimal"
	case ReasoningModerate:
		return "moderate"
	case ReasoningHigh:
		return "high"
	}
	return "unknown"
}

// ReasoningInput is the deterministic evidence the demand assessment reads.
//
// It contains no role and no model: the same work has the same demand whatever
// authority performs it. The investigation flag describes the required
// capability; it does not grant it.
type ReasoningInput struct {
	// Intent is the user-derived completion contract for the work.
	Intent *contract.IntentContract
	// Investigate reports that the work requires autonomous investigation.
	// Running a single mechanical command is not a complexity signal: the
	// verification requirement and the blast radius are.
	Investigate bool
	// OpenTodos is the number of unfinished task items.
	OpenTodos int
	// OpenHypotheses is the number of hypotheses still awaiting evidence.
	OpenHypotheses int
	// UnresolvedQuestions is the number of open task questions.
	UnresolvedQuestions int
	// RemainingTurns is the run's remaining model exchanges, or -1 when the
	// dimension is unmetered.
	RemainingTurns int
	// RemainingModelCalls is the run's remaining provider requests, or -1.
	RemainingModelCalls int
	// ContextWindow is the model's total context, or zero when unknown.
	ContextWindow int
	// ContextUsed is the estimated current request size, or zero when unknown.
	ContextUsed int
}

// AssessReasoningDemand derives the demand from the work's structure.
//
// The signals are structural on purpose: a keyword table would turn vocabulary
// into difficulty, and role-based defaults would make an ASK architecture
// analysis permanently shallow. Every signal is evidence the runtime already
// holds; nothing here calls a model or reads a repository.
func AssessReasoningDemand(in ReasoningInput) ReasoningDemand {
	signals := 0
	if intent := in.Intent; intent != nil {
		if len(intent.Targets) >= 2 || len(intent.ExpectedChanges) >= 2 {
			signals++
		}
		// Every mutation contract carries at least a generic "observed change"
		// requirement. Only a check the request itself named adds reasoning:
		// the implicit requirement is what makes a mechanical rename verifiable,
		// not what makes it hard.
		if intent.BehavioralObjective || namedVerification(intent.Verification) {
			signals++
		}
		if intent.ConditionalMutation {
			signals++
		}
		if grounding := intent.Grounding; grounding != nil {
			if len(grounding.Actions) >= 3 || len(grounding.Entities) >= 4 {
				signals++
			}
			// An unresolved dimension only adds reasoning when it changes what
			// may be executed. A merely unverifiable target ("this variable")
			// is handled by the completion contract, not by a deeper model
			// deliberation.
			if grounding.HasMaterialAmbiguity() {
				signals++
			}
			if len(grounding.Assumptions) >= 2 {
				signals++
			}
		}
		if len(strings.Fields(intent.Objective)) >= 40 {
			signals++
		}
	}
	if in.Investigate {
		signals++
	}
	if in.OpenTodos >= 3 || in.OpenHypotheses >= 2 || in.UnresolvedQuestions >= 2 {
		signals++
	}
	switch {
	case signals >= 3:
		return ReasoningHigh
	case signals >= 1:
		return ReasoningModerate
	default:
		return ReasoningMinimal
	}
}

// namedVerification reports whether any requirement names a concrete check or
// scope, rather than the generic "something changed" requirement every mutation
// contract carries.
func namedVerification(requirements []contract.VerificationRequirement) bool {
	for _, requirement := range requirements {
		if requirement.Kind == contract.VerifyScope || requirement.EffectiveCheck() != contract.CheckAny {
			return true
		}
	}
	return false
}

// ClampReasoningDemand lowers a demand to what the run can afford.
//
// It only reduces. A high demand does not imply unlimited reasoning: a run with
// one remaining request or a nearly full context must not spend its last
// capacity thinking instead of answering. Unmetered dimensions are reported as
// -1 and impose no cap.
func ClampReasoningDemand(demand ReasoningDemand, in ReasoningInput) ReasoningDemand {
	demand = clampDemand(demand, in.RemainingTurns, in.RemainingModelCalls)
	if in.ContextWindow <= 0 || in.ContextUsed <= 0 {
		return demand
	}
	usedPercent := in.ContextUsed * 100 / in.ContextWindow
	switch {
	case usedPercent >= 90:
		return ReasoningMinimal
	case usedPercent >= 75 && demand == ReasoningHigh:
		return ReasoningModerate
	}
	return demand
}

// clampDemand applies the remaining-turn and remaining-model-call caps.
func clampDemand(demand ReasoningDemand, turns, calls int) ReasoningDemand {
	for _, remaining := range []int{turns, calls} {
		switch {
		case remaining == 1:
			return ReasoningMinimal
		case remaining > 1 && remaining <= 3 && demand == ReasoningHigh:
			demand = ReasoningModerate
		}
	}
	return demand
}

// ReasoningSetting maps a demand onto a model's native reasoning controls.
//
// It returns (enabled, effort). Both are omitted when the model publishes no
// safe value: a missing ladder is unknown capability, not an invitation to
// invent "medium". The ladder is the catalog's own ordered list, lowest first,
// so the selected value is always one the model advertises.
//
// A model whose reasoning is mandatory is never reported as disabled. Omitting
// the configuration is the truthful representation: the caller does not claim
// to have turned it off.
func ReasoningSetting(model ai.Model, demand ReasoningDemand) (bool, string) {
	if !model.Reasoning || !demand.Valid() {
		return false, ""
	}
	ladder := model.ReasoningEfforts
	if demand == ReasoningMinimal {
		if model.ReasoningMandatory {
			return false, ""
		}
		if model.ReasoningDisable != "" {
			return true, model.ReasoningDisable
		}
		if len(ladder) == 0 {
			return false, ""
		}
		return true, ladder[0]
	}
	if len(ladder) == 0 {
		return false, ""
	}
	switch demand {
	case ReasoningModerate:
		return true, ladder[len(ladder)/2]
	case ReasoningHigh:
		return true, ladder[len(ladder)-1]
	}
	return false, ""
}
