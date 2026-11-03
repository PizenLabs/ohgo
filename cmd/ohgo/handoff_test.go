package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// A request that needs stronger authority is projected as a handoff, and the
// proposal is side-effect-free: the harness keeps its workspace until the
// human answers and the TUI calls Switch.
func TestTurnTransitionProposesWideningHandoff(t *testing.T) {
	held := &harness{workspace: "FUZZ", root: t.TempDir()}
	proposal, ok := turnTransition(held, "implement the fix")
	if !ok {
		t.Fatal("no handoff proposal for a mutation request under FUZZ")
	}
	if proposal.Kind != tui.TransitionKindHandoff {
		t.Fatalf("kind = %q, want handoff", proposal.Kind)
	}
	if proposal.Current != "FUZZ" || proposal.Proposed != "BUILD" {
		t.Fatalf("proposal = %s -> %s, want FUZZ -> BUILD", proposal.Current, proposal.Proposed)
	}
	if held.activeWorkspace() != workspace.Fuzz {
		t.Fatalf("the proposal moved the workspace to %s", held.activeWorkspace())
	}
}

// The reverse direction stays a narrowing transition, not a handoff: BUILD ->
// FUZZ reduces authority and does not need a widening approval.
func TestTurnTransitionKeepsNarrowingAsTransition(t *testing.T) {
	held := &harness{workspace: "BUILD", root: t.TempDir()}
	proposal, ok := turnTransition(held, "reproduce the failure under load")
	if !ok {
		t.Fatal("no transition proposal for an investigative request under BUILD")
	}
	if proposal.Kind != tui.TransitionKindWorkspace {
		t.Fatalf("kind = %q, want a narrowing workspace transition", proposal.Kind)
	}
	if proposal.Current != "BUILD" || proposal.Proposed != "FUZZ" {
		t.Fatalf("proposal = %s -> %s, want BUILD -> FUZZ", proposal.Current, proposal.Proposed)
	}
	if held.activeWorkspace() != workspace.Build {
		t.Fatalf("the proposal moved the workspace to %s", held.activeWorkspace())
	}
}
