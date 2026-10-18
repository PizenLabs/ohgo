package workspace

// TransitionProposal is a pending recommendation to move the session to a
// lower-authority workspace.
//
// It is a request, not a decision: nothing here changes authority. The UI
// presents it, the human accepts or rejects it, and only an accepted proposal
// is carried out through the authoritative workspace switch (§4, §5). The
// classifier produces the Intent and Strength; the policy decides whether
// asking is worthwhile; the human decides.
type TransitionProposal struct {
	// Current is the workspace in force when the request arrived.
	Current Workspace
	// Proposed is the workspace the request appears to belong to. It always
	// narrows or equals Current's authority, never widens it.
	Proposed Workspace
	// Intent is the classified request.
	Intent Intent
	// Strength is the evidence behind Intent: a level, never a probability
	// (§4, I12).
	Strength Strength
	// Reason is the one-line, user-facing explanation of the proposal.
	Reason string
}

// Question is the proposal's heading, e.g. "Switch to ASK?".
func (p TransitionProposal) Question() string {
	return "Switch to " + p.Proposed.Label() + "?"
}

// explanationFor is the user-facing copy for a proposal's intent.
//
// The copy lives here, beside the policy that produces the proposal, so every
// surface says the same thing about the same intent.
func explanationFor(intent Intent) string {
	if intent == IntentInvestigate {
		return "This request appears investigative."
	}
	return "This request appears informational."
}

// proposalThreshold is the least evidence that justifies asking the user.
//
// It is explicit and pinned by tests. Strong — a leading-verb match that no
// rival signal contests — is the "strong separation" the policy requires (§7).
// Moderate is a single corroborating phrase, and ambiguous is either no signal
// or a contested one; neither is worth interrupting a turn for, and the
// existing safe narrowing (delegation and refusal) still handles those
// requests without changing authority.
const proposalThreshold = StrengthStrong

// transitionTargets is the only set of transitions the policy may propose.
//
// Every entry is a safe narrowing within the authority chain: BUILD may offer
// ASK or FUZZ, FUZZ may offer ASK. ASK has no entry, so no classifier output
// can propose leaving the read-only workspace for a stronger one (I8, I9).
var transitionTargets = map[Workspace]map[Intent]Workspace{
	Build: {IntentRead: Ask, IntentInvestigate: Fuzz},
	Fuzz:  {IntentRead: Ask},
}

// ProposeTransition decides whether a prediction justifies asking the user to
// move the session to another workspace.
//
// It never changes authority; it only answers "is this worth proposing?".
// Four refusals define the contract (§6, §7):
//
//   - an unresolved workspace proposes nothing;
//   - the request's own workspace (ASK+READ, BUILD+EXECUTE, FUZZ+INVESTIGATE)
//     is no transition;
//   - anything the active workspace does not encompass is refused before the
//     table is believed, so a proposal can only narrow (I8, I9);
//   - evidence below proposalThreshold proposes nothing (I12).
//
// The second guard is deliberately redundant with the table: the table is
// data, the guard is the invariant.
func ProposeTransition(active Workspace, prediction Prediction) (TransitionProposal, bool) {
	if !active.Valid() {
		return TransitionProposal{}, false
	}
	target, ok := transitionTargets[active][prediction.Intent]
	if !ok || target == active {
		return TransitionProposal{}, false
	}
	if !active.Encompasses(target) {
		return TransitionProposal{}, false
	}
	if !prediction.Strength.AtLeast(proposalThreshold) {
		return TransitionProposal{}, false
	}
	return TransitionProposal{
		Current:  active,
		Proposed: target,
		Intent:   prediction.Intent,
		Strength: prediction.Strength,
		Reason:   explanationFor(prediction.Intent),
	}, true
}
