package main

import (
	"os"
	"strings"

	"github.com/PizenLabs/ohgo/tui"
	"github.com/PizenLabs/ohgo/workspace"
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

// turnTransition projects a pending decision into the TUI's proposal shape, or
// reports that the policy wants no decision.
//
// It consults two boundaries in order. The workspace transition answers
// whether the request belongs to a narrower authority; the proposal
// assessment answers whether an authorized request leaves a decision the
// request itself opened. The two are disjoint by intent — transitions fire on
// READ and INVESTIGATE, proposals on EXECUTE — so at most one proposes, and
// the workspace narrowing keeps precedence.
//
// It is deliberately side-effect-free, unlike turnAuthority: prediction runs
// before the turn is decided, so it must not install the handler or move the
// gate. The accepted transition is applied later, through Switch; the
// resolved proposal is recorded by the program when the human answers. It is
// the single place the workspace package's policy vocabulary is translated for
// the UI, so both full-screen entry points agree.
func turnTransition(held *harness, text string) (tui.TurnTransition, bool) {
	active := held.activeWorkspace()
	prediction := workspace.ClassifyPrediction(text)
	// A moderate or contested prediction may not choose the authority. A
	// no-signal objective also needs a human choice; a plain greeting is not a
	// workspace decision and remains in the explicitly selected workspace.
	noSignalChoice := prediction.Strength == workspace.StrengthAmbiguous &&
		prediction.Competing == "" && prediction.Intent.Handler() != active &&
		!workspace.IsConversationalAcknowledgement(text)
	if prediction.Intent.Handler() != active &&
		(prediction.Strength == workspace.StrengthModerate ||
			prediction.Strength == workspace.StrengthAmbiguous && prediction.Competing != "" ||
			noSignalChoice) {
		return tui.TurnTransition{
			Kind:     tui.TransitionKindAmbiguous,
			Intent:   strings.ToUpper(string(prediction.Intent)),
			Strength: string(prediction.Strength),
			Question: "Choose a workspace for this request",
			Reason:   "The request contains competing or incomplete intent signals. The workspace is yours to choose.",
		}, true
	}
	transition, ok := workspace.ProposeTransition(active, prediction)
	if ok {
		current, proposed := transition.Current.Style(), transition.Proposed.Style()
		return tui.TurnTransition{
			Kind:           tui.TransitionKindWorkspace,
			Current:        current.Label,
			Proposed:       proposed.Label,
			CurrentAccent:  current.Accent,
			ProposedAccent: proposed.Accent,
			Intent:         strings.ToUpper(string(transition.Intent)),
			Strength:       string(transition.Strength),
			Question:       transition.Question(),
			Reason:         transition.Reason,
		}, true
	}

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
