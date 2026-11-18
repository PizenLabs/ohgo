package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// A request that needs stronger authority is projected as a handoff, and the
// proposal is side-effect-free: the harness keeps its workspace until the
// human answers and the TUI calls Switch.
func TestTurnTransitionProposesWideningHandoff(t *testing.T) {
	held := &harness{workspace: "ASK", root: t.TempDir()}
	proposal, ok := turnTransition(held, workspace.ContractFor(workspace.Build), "implement the fix")
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
	proposal, ok := turnTransition(held, workspace.ContractFor(workspace.Build), "reproduce the failure under load")
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

// The turn boundary consumes the task's persistent contract. A standalone FUZZ
// task has no mutation authority and must ask; the same phase under a
// BUILD-authorized master task stays an automatic internal handoff.
func TestTurnTransitionUsesTaskMutationAuthority(t *testing.T) {
	held := &harness{
		workspace: workspace.Fuzz.Label(),
		model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
	}

	unauthorized, ok := turnTransition(held, workspace.ContractFor(workspace.Fuzz), "fix the checkout bug")
	if !ok || unauthorized.Automatic || unauthorized.Proposed != "BUILD" {
		t.Fatalf("standalone FUZZ mutation = %+v ok=%v, want a BUILD confirmation", unauthorized, ok)
	}
	if unauthorized.Kind != tui.TransitionKindHandoff {
		t.Fatalf("transition kind = %q, want the widening handoff surface", unauthorized.Kind)
	}

	authorized, ok := turnTransition(held, workspace.ContractFor(workspace.Build), "fix the checkout bug")
	if !ok || !authorized.Automatic || authorized.Proposed != "BUILD" {
		t.Fatalf("authorized FUZZ mutation = %+v ok=%v, want an automatic BUILD handoff", authorized, ok)
	}
}

// A new objective must not inherit the open task's grant. The previous task's
// BUILD authority explains how that task was authorized; it says nothing about
// a new task started while the session is in FUZZ.
func TestNewObjectiveDoesNotInheritTaskAuthority(t *testing.T) {
	previous := workspace.ContractFor(workspace.Build)
	strict := func() workspace.ExecutionContract { return previous }

	inherited := executionContractForTurn(workspace.Fuzz, true, strict)
	if inherited.Origin != workspace.Build || !inherited.Mutate {
		t.Fatalf("owned turn contract = %s, want the open task's BUILD grant", inherited)
	}

	fresh := executionContractForTurn(workspace.Fuzz, false, strict)
	if fresh.Origin != workspace.Fuzz || fresh.Mutate {
		t.Fatalf("new objective contract = %s, want the live FUZZ authority", fresh)
	}
	if decision := workspace.DecideTransition(workspace.Fuzz, fresh, workspace.AnalyzeRequest("fix the checkout bug")); !decision.NeedsConfirmation() {
		t.Fatalf("new objective under FUZZ = %+v, want a BUILD confirmation", decision)
	}
}
