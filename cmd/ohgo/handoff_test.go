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
	held := &harness{workspace: "ASK", root: t.TempDir()}
	proposal, ok := turnTransition(held, "implement the fix")
	if !ok {
		t.Fatal("no handoff proposal for a mutation request under ASK")
	}
	if proposal.Kind != tui.TransitionKindHandoff {
		t.Fatalf("kind = %q, want handoff", proposal.Kind)
	}
	if proposal.Current != "ASK" || proposal.Proposed != "BUILD" {
		t.Fatalf("proposal = %s -> %s, want ASK -> BUILD", proposal.Current, proposal.Proposed)
	}
	if proposal.Automatic {
		t.Fatal("crossing ASK was marked automatic")
	}
	if held.activeWorkspace() != workspace.Ask {
		t.Fatalf("the proposal moved the workspace to %s", held.activeWorkspace())
	}
}

// BUILD -> FUZZ is cooperative execution-workspace movement, not a narrowing
// approval. The control plane selects it and the owner applies it.
func TestTurnTransitionMarksBuildToFuzzAsAutomatic(t *testing.T) {
	held := &harness{workspace: "BUILD", root: t.TempDir()}
	proposal, ok := turnTransition(held, "reproduce the failure under load")
	if !ok {
		t.Fatal("no decision for an investigative request under BUILD")
	}
	if proposal.Kind != tui.TransitionKindWorkspace || !proposal.Automatic {
		t.Fatalf("proposal = %+v, want an automatic workspace handoff", proposal)
	}
	if proposal.Current != "BUILD" || proposal.Proposed != "FUZZ" {
		t.Fatalf("proposal = %s -> %s, want BUILD -> FUZZ", proposal.Current, proposal.Proposed)
	}
	if held.activeWorkspace() != workspace.Build {
		t.Fatalf("the proposal moved the workspace to %s", held.activeWorkspace())
	}
}
