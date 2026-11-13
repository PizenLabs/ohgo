package workspace

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// Workspace transition policy.
//
// The policy answers one question: may this request run under the current
// authority, and if not, is moving to another workspace a human-boundary
// decision or an internal execution-workspace handoff? It is deliberately
// separate from the capability gate: this file describes what should happen,
// while Authority.Admit enforces what may actually happen.
//
// The two boundaries are not symmetric:
//
//	ASK <-> BUILD/FUZZ   human authority boundary, always confirmed
//	BUILD <-> FUZZ       cooperative execution handoff, automatic
//
// Capability, not intent-handler equality, decides whether the current
// workspace can already serve the request. BUILD can read and investigate; it
// only needs to leave those to ASK/FUZZ when doing so is better for the user's
// intent, not because the operation is impossible.

// Request capabilities are the contract's capability vocabulary. The control
// plane reads them from one owner so the transition policy and the capability
// preflight cannot disagree about what a request needs.

// Requirement is the control plane's deterministic reading of what a request
// needs from a workspace.
type Requirement struct {
	// Capability is the operation class the turn needs.
	Capability Capability
	// Intent is the routing intent used for model-facing context and evidence.
	Intent Intent
	// Strength is the classifier's evidence level. It is a level, never a
	// probability.
	Strength Strength
	// Reason explains the requirement in user-facing terms.
	Reason string
}

// AnalyzeRequest derives the workspace requirement for one request.
//
// The staged-conditional check comes first: a request that explicitly starts
// in investigation is an investigation turn even when a later clause names a
// conditional change. Within an ordinary request, an investigative classifier
// result keeps investigation precedence over a later mutation clause, matching
// the safety rule that a mixed request begins in the lower authority.
func AnalyzeRequest(text string) Requirement {
	if IsConversationalAcknowledgement(text) {
		// A greeting carries no capability evidence, so it does not justify a
		// workspace change. An open task handles explicit continuation
		// vocabulary before this analysis runs.
		return Requirement{
			Capability: CapabilityRead,
			Intent:     IntentRead,
			Strength:   StrengthAmbiguous,
			Reason:     "the request is a conversational acknowledgement",
		}
	}
	if ConditionalWorkflow(text) {
		return Requirement{
			Capability: CapabilityInvestigate,
			Intent:     IntentInvestigate,
			Strength:   StrengthStrong,
			Reason:     "the request starts by investigating before any conditional change",
		}
	}
	prediction := ClassifyPrediction(text)
	switch prediction.Intent {
	case IntentInvestigate:
		return Requirement{
			Capability: CapabilityInvestigate,
			Intent:     IntentInvestigate,
			Strength:   prediction.Strength,
			Reason:     "the request needs autonomous investigation",
		}
	case IntentExecute:
		if RequestsMutation(text) {
			return Requirement{
				Capability: CapabilityMutate,
				Intent:     IntentExecute,
				Strength:   StrengthStrong,
				Reason:     "the request needs a repository change",
			}
		}
		return Requirement{
			Capability: CapabilityExecute,
			Intent:     IntentExecute,
			Strength:   prediction.Strength,
			Reason:     "the request needs to execute a process",
		}
	default:
		return Requirement{
			Capability: CapabilityRead,
			Intent:     IntentRead,
			Strength:   prediction.Strength,
			Reason:     "the request asks for understanding or read-only analysis",
		}
	}
}

// ToolCallTransition derives the workspace transition one concrete tool call
// needs, and reports whether the control plane may apply it automatically.
//
// It is the tool-boundary counterpart to AnalyzeRequest: the call's own
// meaning, not the model's narration, establishes that a staged task has
// entered another phase. The capability is read from the invocation through
// OperationForTool, so a tool that can mutate — a shell, for example — only
// escalates when this command actually changes the repository. Using the
// tool's declared action classes here would move every shell call to BUILD.
//
// Only a BUILD <-> FUZZ automatic decision is reported as usable here. A plan
// artifact under FUZZ would cross ASK, which remains a human decision and is
// left to the ordinary artifact refusal.
func ToolCallTransition(active Workspace, tool agent.Tool, args map[string]any) (TransitionDecision, bool) {
	requirement, ok := ToolCallRequirement(tool.Name(), args)
	if !ok {
		if !OperationForTool(tool, args).Mutates() {
			return TransitionDecision{}, false
		}
		requirement = Requirement{
			Capability: CapabilityMutate,
			Intent:     IntentExecute,
			Strength:   StrengthStrong,
			Reason:     "the call changes repository state, which belongs to BUILD",
		}
	}
	decision := DecideTransition(active, requirement)
	if !decision.Automatic() {
		return decision, false
	}
	return decision, true
}

// ToolCallRequirement derives the workspace capability a concrete tool call
// needs, or reports that the call does not establish a phase.
//
// AnalyzeRequest reads the user's words. A long staged task also moves between
// phases while it runs, and the model's own actions are the only signal that
// the phase changed: filing a finding is investigation, and changing source is
// mutation. This function is the capability side of that signal, so the
// control plane can apply DecideTransition before the call executes instead of
// letting a workspace-incompatible call fail and calling it an error.
//
// It is deliberately narrow. The artifact tool is the one call whose meaning is
// fixed by its own arguments: the kind names the phase. A repository mutation
// is supplied by the caller from the tool's declared action class, so a tool
// named "write" that is not actually mutating cannot trigger a handoff, and a
// shell command that may or may not mutate is left to the security gate, which
// already classifies it.
func ToolCallRequirement(name string, args map[string]any) (Requirement, bool) {
	switch name {
	case "artifact":
		kind, _ := args["kind"].(string)
		switch ArtifactKind(strings.TrimSpace(kind)) {
		case KindPlan:
			return Requirement{
				Capability: CapabilityRead,
				Intent:     IntentRead,
				Strength:   StrengthStrong,
				Reason:     "the call records a plan, which belongs to ASK",
			}, true
		case KindFinding:
			return Requirement{
				Capability: CapabilityInvestigate,
				Intent:     IntentInvestigate,
				Strength:   StrengthStrong,
				Reason:     "the call records a reproduction finding, which belongs to FUZZ",
			}, true
		case KindChange, KindVerification:
			return Requirement{
				Capability: CapabilityMutate,
				Intent:     IntentExecute,
				Strength:   StrengthStrong,
				Reason:     "the call records a repository change, which belongs to BUILD",
			}, true
		}
	}
	return Requirement{}, false
}

// DecisionKind is the control plane's transition disposition.
type DecisionKind string

// Transition dispositions.
const (
	// DecisionStay means the current workspace can serve the request as-is.
	DecisionStay DecisionKind = "stay"
	// DecisionAutomatic means the control plane moves between execution
	// workspaces without asking the human. It is limited to BUILD <-> FUZZ.
	DecisionAutomatic DecisionKind = "automatic"
	// DecisionConfirm means the request crosses the ASK authority boundary and
	// needs the human's answer before authority moves.
	DecisionConfirm DecisionKind = "confirm"
)

// TransitionDecision is the control plane's answer for one request.
//
// It is data, never authority. The caller applies an automatic or confirmed
// target through the single workspace switch owner; a decision that is only
// inspected changes nothing.
type TransitionDecision struct {
	// Kind is the disposition.
	Kind DecisionKind
	// Current is the workspace in force when the request arrived.
	Current Workspace
	// Target is the workspace the decision selects. It equals Current when the
	// decision is Stay.
	Target Workspace
	// Requirement is the analysis that produced the decision.
	Requirement Requirement
	// Reason is the user-facing explanation.
	Reason string
	// Question is the confirmation heading for a Confirm decision, e.g.
	// "Switch to BUILD?".
	Question string
}

// NeedsConfirmation reports whether the human must decide the transition.
func (d TransitionDecision) NeedsConfirmation() bool { return d.Kind == DecisionConfirm }

// Automatic reports whether the control plane may move without asking.
func (d TransitionDecision) Automatic() bool { return d.Kind == DecisionAutomatic }

// Proposed reports whether the decision would move authority.
func (d TransitionDecision) Proposed() bool {
	return d.Target.Valid() && d.Target != d.Current && (d.Automatic() || d.NeedsConfirmation())
}

// DecideTransition derives the workspace disposition for one request.
//
// It is pure and deterministic. It never moves authority and never reads or
// writes runtime state. The accepted/automatic target is applied by the host
// through the existing switch owner.
func DecideTransition(active Workspace, requirement Requirement) TransitionDecision {
	decision := TransitionDecision{
		Kind:        DecisionStay,
		Current:     active,
		Target:      active,
		Requirement: requirement,
		Reason:      requirement.Reason,
	}
	if !active.Valid() || !validCapability(requirement.Capability) {
		return decision
	}
	target := preferredWorkspace(requirement.Capability)
	switch {
	case requirement.Capability == CapabilityMutate && active == Fuzz:
		return automaticDecision(active, Build, requirement,
			"The request needs a repository change; BUILD is the only mutation authority.")
	case requirement.Capability == CapabilityInvestigate && active == Build &&
		requirement.Strength == StrengthStrong:
		return automaticDecision(active, Fuzz, requirement,
			"The request needs autonomous investigation; FUZZ can reproduce and execute without changing source.")
	case requirement.Capability == CapabilityRead && (active == Build || active == Fuzz) &&
		requirement.Strength == StrengthStrong:
		return confirmDecision(active, Ask, requirement,
			"This request asks for a human-facing explanation; ASK keeps the session read-only.")
	}
	if canPerform(active, requirement.Capability) {
		return decision
	}
	if !target.Valid() || target == active {
		return decision
	}
	// Crossing ASK in either direction is a human authority boundary. A move
	// between BUILD and FUZZ is safe cooperation within the execution side.
	if active == Ask || target == Ask {
		return confirmDecision(active, target, requirement, confirmationReason(target))
	}
	return automaticDecision(active, target, requirement, automaticReason(active, target))
}

// automaticDecision builds an execution-workspace handoff.
func automaticDecision(active, target Workspace, requirement Requirement, reason string) TransitionDecision {
	return TransitionDecision{
		Kind:        DecisionAutomatic,
		Current:     active,
		Target:      target,
		Requirement: requirement,
		Reason:      reason,
	}
}

// confirmDecision builds a human-boundary decision.
func confirmDecision(active, target Workspace, requirement Requirement, reason string) TransitionDecision {
	return TransitionDecision{
		Kind:        DecisionConfirm,
		Current:     active,
		Target:      target,
		Requirement: requirement,
		Reason:      reason,
		Question:    "Switch to " + target.Label() + "?",
	}
}

// preferredWorkspace is the workspace specialized for a capability.
func preferredWorkspace(capability Capability) Workspace {
	switch capability {
	case CapabilityRead:
		return Ask
	case CapabilityInvestigate:
		return Fuzz
	case CapabilityExecute, CapabilityMutate:
		return Build
	}
	return ""
}

// canPerform reports whether the workspace can actually carry out the
// capability. It mirrors Workspace.Permits without taking a tool call.
func canPerform(active Workspace, capability Capability) bool {
	switch capability {
	case CapabilityRead:
		return active.Valid()
	case CapabilityExecute, CapabilityInvestigate:
		return active == Fuzz || active == Build
	case CapabilityMutate:
		return active == Build
	}
	return false
}

// valid reports whether the capability is one the contract vocabulary defines.
func validCapability(c Capability) bool { return c.Valid() }

// confirmationReason explains a human-boundary decision in the target's terms.
func confirmationReason(target Workspace) string {
	switch target {
	case Ask:
		return "This request asks for a human-facing explanation; ASK keeps the session read-only."
	case Build:
		return "This request needs a repository change; BUILD is the only mutation authority."
	case Fuzz:
		return "This request needs autonomous investigation; FUZZ can reproduce and execute without changing source."
	}
	return "This request needs a different workspace authority."
}

// automaticReason explains a cooperative execution-workspace handoff.
func automaticReason(active, target Workspace) string {
	switch {
	case active == Build && target == Fuzz:
		return "Moving to FUZZ for autonomous investigation without mutation authority."
	case active == Fuzz && target == Build:
		return "Moving to BUILD to perform and verify the repository change."
	}
	return "Moving to " + target.Label() + " for the capability this request needs."
}
