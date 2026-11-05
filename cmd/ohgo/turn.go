package main

import (
	"os"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// turnAuthority projects a harness route into the TUI's turn decision.
//
// It is the one place the router's vocabulary is translated for the UI, so the
// full-screen path and the plain REPL cannot disagree about what a route
// means.
func turnAuthority(held *harness, text string) tui.TurnAuthority {
	route := held.RouteTurn(text)
	return tui.TurnAuthority{
		Active:  route.Active.Label(),
		Intent:  strings.ToUpper(string(route.Intent)),
		Blocked: !route.Allowed(),
		Reason:  route.Message(),
	}
}

// turnTransition projects the control plane's transition decision into the
// TUI's proposal shape, or reports that no decision is pending.
//
// The decision itself lives in workspace.DecideTransition: it is capability
// based, automatic between BUILD and FUZZ, and confirmed whenever ASK is
// crossed. This function only translates that decision for the UI. It remains
// side-effect-free: prediction runs before the turn, so it must not install a
// handler or move the gate. The accepted or automatic move is applied later
// through the workspace owner.
func turnTransition(held *harness, text string) (tui.TurnTransition, bool) {
	active := held.activeWorkspace()
	// A continuation of an open task is resolved by the task layer before
	// prediction. A standalone "run the tests" is not a continuation, though:
	// without task ownership it is an execution request and must be routed.
	requirement := workspace.AnalyzeRequest(text)
	decision := workspace.DecideTransition(active, requirement)
	if decision.Proposed() {
		current, proposed := decision.Current.Style(), decision.Target.Style()
		kind := tui.TransitionKindWorkspace
		if decision.NeedsConfirmation() && !active.Encompasses(decision.Target) {
			// A widening decision is a handoff: classification may ask for it,
			// but only the human can approve stronger authority.
			kind = tui.TransitionKindHandoff
		}
		return tui.TurnTransition{
			Kind:           kind,
			Current:        current.Label,
			Proposed:       proposed.Label,
			CurrentAccent:  current.Accent,
			ProposedAccent: proposed.Accent,
			Intent:         strings.ToUpper(string(requirement.Intent)),
			Strength:       string(requirement.Strength),
			Question:       decision.Question,
			Reason:         decision.Reason,
			Automatic:      decision.Automatic(),
		}, true
	}

	// The workspace decision stayed. A request can still carry a decision about
	// the execution path (for example, which implementation approach to take).
	// That proposal is independent of authority and uses the existing approval
	// surface.
	prediction := workspace.ClassifyPrediction(text)
	proposal, ok := workspace.AssessProposal(active, text, proposalFacts(held.root))
	if !ok {
		return tui.TurnTransition{}, false
	}
	return tui.TurnTransition{
		Kind:     tui.TransitionKindProposal,
		Proposal: &proposal,
		Intent:   strings.ToUpper(string(prediction.Intent)),
		Strength: string(prediction.Strength),
		Question: proposal.Question,
		Reason:   proposal.Reason,
	}, true
}

// proposalFacts builds the generic workspace evidence a proposal assessment
// may consult: whether any visible entry exists. Hidden runtime directories
// (.ohgo, .git) do not count, so a freshly initialized empty workspace
// establishes no convention while a real project does.
//
// The probe never reads a file, classifies a language, or names a project
// kind. Presence is the whole evidence.
func proposalFacts(root string) workspace.ProposalFacts {
	var facts workspace.ProposalFacts
	if strings.TrimSpace(root) == "" {
		return facts
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return facts
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		facts.VisibleEntries++
	}
	facts.HasConvention = facts.VisibleEntries > 0
	return facts
}
