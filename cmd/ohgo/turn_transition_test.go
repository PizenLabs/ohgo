package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// A capability-compatible request does not interrupt the user with a workspace
// picker. BUILD can inspect, so a vague or moderate read stays BUILD; the
// stricter ASK-boundary policy is tested by TestWideningFromAskIsOnlyAHandoff.
func TestTurnTransitionDoesNotInterruptForCapabilityCompatibleRequests(t *testing.T) {
	held := &harness{workspace: "BUILD", root: t.TempDir()}
	for _, request := range []string{
		"check this project",
		"take a look at this",
		"fix it and then try to break it again",
	} {
		if transition, ok := turnTransition(held, request); ok && transition.Automatic {
			t.Errorf("turnTransition(%q) applied an automatic switch: %+v", request, transition)
		}
	}
}

// A staged conditional request starts in investigation, not in the mutation
// branch its future clause describes. Under BUILD that is an automatic FUZZ
// handoff; under ASK it crosses the human boundary.
func TestTurnTransitionRoutesStagedConditionalByItsCurrentPhase(t *testing.T) {
	const request = "Investigate the project first. If you confirm a concrete issue, fix it. Otherwise explain what you found."

	ask := routedHarness(newAuthorityGate(workspace.Ask), nil, "ASK")
	proposal, ok := turnTransition(ask, request)
	if !ok || proposal.Proposed != "FUZZ" || proposal.Automatic {
		t.Fatalf("ASK staged request = %+v, %v; want a confirmed FUZZ handoff", proposal, ok)
	}

	build := routedHarness(newAuthorityGate(workspace.Build), nil, "BUILD")
	proposal, ok = turnTransition(build, request)
	if !ok || proposal.Proposed != "FUZZ" || !proposal.Automatic {
		t.Fatalf("BUILD staged request = %+v, %v; want an automatic FUZZ handoff", proposal, ok)
	}
}

// The exact product regression: an investigative opener whose later clause
// says what the answer should describe. The explanatory clause is part of the
// investigation's deliverable, not a rival READ objective, so BUILD must hand
// the turn to FUZZ automatically instead of keeping it or asking for ASK.
func TestTurnTransitionRoutesInvestigativeAnalysisToFuzz(t *testing.T) {
	held := &harness{workspace: "BUILD", root: t.TempDir()}
	const request = "investigate this project and analyze what this project is about?"

	transition, ok := turnTransition(held, request)
	if !ok {
		t.Fatal("the investigative request produced no workspace decision")
	}
	if transition.Proposed != "FUZZ" || !transition.Automatic {
		t.Fatalf("transition = %+v, want an automatic FUZZ handoff", transition)
	}
	if transition.Intent != "INVESTIGATE" {
		t.Fatalf("intent = %q, want INVESTIGATE", transition.Intent)
	}
	if held.activeWorkspace() != workspace.Build {
		t.Fatalf("prediction moved authority to %s before the decision was applied", held.activeWorkspace())
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

// A continuation is task state, not a new workspace request. Prediction must
// not turn "verify the fix" or "continue" into a role decision in BUILD/FUZZ,
// because the open task already owns the turn and its persisted contract.
func TestTurnTransitionDoesNotAskWorkspaceForTaskContinuation(t *testing.T) {
	for _, active := range []workspace.Workspace{workspace.Fuzz, workspace.Build} {
		gate := newAuthorityGate(active)
		held := routedHarness(gate, nil, active.Label())
		held.root = t.TempDir()
		for _, request := range []string{"continue", "verify the fix", "retry", "try again"} {
			if transition, ok := turnTransition(held, request); ok {
				t.Errorf("turnTransition(%s, %q) opened %q (%s -> %s)",
					active, request, transition.Kind, transition.Current, transition.Proposed)
			}
			if gate.Workspace() != active {
				t.Fatalf("prediction moved the live gate from %s to %s", active, gate.Workspace())
			}
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
