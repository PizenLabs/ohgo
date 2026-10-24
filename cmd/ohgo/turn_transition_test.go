package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/tui"
	"github.com/PizenLabs/ohgo/workspace"
)

func TestTurnTransitionSendsAmbiguousIntentToAuthorityPicker(t *testing.T) {
	held := &harness{workspace: "BUILD", root: t.TempDir()}
	for _, request := range []string{
		"check this project",
		"take a look at this",
		"fix it and then try to break it again",
	} {
		transition, ok := turnTransition(held, request)
		if !ok || transition.Kind != tui.TransitionKindAmbiguous {
			t.Errorf("turnTransition(%q) = %+v, %v; want an explicit workspace choice", request, transition, ok)
		}
	}
}

func TestTurnTransitionDoesNotAskWorkspaceForNoSignalGreeting(t *testing.T) {
	held := &harness{workspace: "BUILD", root: t.TempDir()}
	for _, request := range []string{"hello", "thanks"} {
		if transition, ok := turnTransition(held, request); ok {
			t.Fatalf("acknowledgement %q opened a workspace transition: %+v", request, transition)
		}
	}
}

func TestPortfolioWebsiteCreationUsesOnlyItsBuildProposal(t *testing.T) {
	held := &harness{workspace: "BUILD", root: t.TempDir()}
	transition, ok := turnTransition(held, "create a portfolio website")
	if !ok || transition.Kind != tui.TransitionKindProposal || transition.Proposal == nil {
		t.Fatalf("clear BUILD objective did not produce its implementation proposal: %+v, %v", transition, ok)
	}
	if route := workspace.RouteTurn(workspace.Build, "create a portfolio website"); !route.Allowed() {
		t.Fatalf("BUILD refused the explicit creation objective: %+v", route)
	}
}
