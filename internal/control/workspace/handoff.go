package workspace

// HandoffProposal is a pending request to move the session to a stronger
// authority.
//
// It is the counterpart of TransitionProposal. ProposeTransition only narrows:
// the classifier can safely recommend less authority without asking, so it does.
// Widening is different — it needs the human — so a HandoffProposal always
// requires explicit approval and never changes authority by itself.
//
// Like a transition proposal it is a request, not a decision. The UI presents
// it, the human accepts or rejects it, and only an accepted proposal is carried
// out through the authoritative workspace switch. The type carries no
// capability: Building a proposal here cannot let FUZZ write.
type HandoffProposal struct {
	// Current is the workspace in force when the request arrived.
	Current Workspace
	// Proposed is the workspace the request needs. It is always stronger than
	// Current: moving to an equal or weaker authority is a transition, not a
	// handoff.
	Proposed Workspace
	// Intent is the classified request.
	Intent Intent
	// Strength is the evidence behind Intent. It is a level, never a score.
	Strength Strength
	// Reason is the one-line, user-facing explanation of why authority must
	// move.
	Reason string
	// Summary is what the handoff carries: task identity, shared context, and
	// role progress. It is descriptive, never a capability.
	Summary string
}

// Question is the proposal's heading, e.g. "Hand off to BUILD?".
func (p HandoffProposal) Question() string {
	return "Hand off to " + p.Proposed.Label() + "?"
}

// ProposeHandoff decides whether a request needs more authority than the active
// workspace and, if so, describes the approval the human must give.
//
// It is deliberately the narrowest possible widening policy:
//
//   - an unresolved workspace proposes nothing;
//   - only a request whose classified handler is strictly stronger can widen;
//   - the evidence must clear the same strong threshold a narrowing proposal
//     uses, so a misclassification cannot manufacture a prompt;
//   - a narrowing request is left to ProposeTransition.
//
// Proposing is not granting. The returned value has no effect until the human
// accepts it and the host applies it through Switch, which reads the
// destination's own policy. ASK remains read-only and FUZZ remains
// non-mutating even if a handoff is proposed, declined, or accepted badly.
func ProposeHandoff(active Workspace, prediction Prediction) (HandoffProposal, bool) {
	if !active.Valid() {
		return HandoffProposal{}, false
	}
	target := prediction.Intent.Handler()
	if !target.Valid() || target == active {
		return HandoffProposal{}, false
	}
	// Only widening is a handoff. A target the active workspace encompasses is
	// a safe narrowing, which ProposeTransition owns.
	if active.Encompasses(target) {
		return HandoffProposal{}, false
	}
	if !prediction.Strength.AtLeast(proposalThreshold) {
		return HandoffProposal{}, false
	}
	return HandoffProposal{
		Current:  active,
		Proposed: target,
		Intent:   prediction.Intent,
		Strength: prediction.Strength,
		Reason:   handoffReason(target),
		Summary:  "The task, its shared context, and each role's progress are preserved; only the execution authority changes.",
	}, true
}

// handoffReason explains the authority change in the destination's terms.
func handoffReason(target Workspace) string {
	switch target {
	case Build:
		return "This request changes the project. BUILD is the only mutation authority, so the handoff needs your approval."
	case Fuzz:
		return "This request investigates or executes. FUZZ may run and reproduce without changing project source, so the handoff needs your approval."
	default:
		return "This request needs " + target.Label() + " authority, so the handoff needs your approval."
	}
}
