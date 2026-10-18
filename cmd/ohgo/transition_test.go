package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/workspace"
)

// TestTurnTransitionPredictsWithoutMovingAuthority is invariant 1 and 2: the
// classifier may propose, but computing a proposal must not touch the live
// gate or the persistent workspace. turnTransition is deliberately not
// turnAuthority, which does install the handler.
func TestTurnTransitionPredictsWithoutMovingAuthority(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")

	proposal, ok := turnTransition(h, "why does this function return nil here?")
	if !ok {
		t.Fatal("a strong informational request in BUILD produced no proposal")
	}
	if proposal.Proposed != "ASK" {
		t.Errorf("Proposed = %q, want ASK", proposal.Proposed)
	}
	if proposal.Strength != "strong" {
		t.Errorf("Strength = %q, want the evidence level strong", proposal.Strength)
	}
	if gate.Workspace() != workspace.Build {
		t.Errorf("prediction moved the live gate to %s", gate.Workspace())
	}
	if h.activeWorkspace() != workspace.Build {
		t.Errorf("prediction moved the persistent workspace to %s", h.activeWorkspace())
	}
}

// TestTurnTransitionMatrixMatchesThePolicy pins the seam's projection against
// the §6 table: the safe narrowings propose, everything else stays silent.
func TestTurnTransitionMatrixMatchesThePolicy(t *testing.T) {
	cases := []struct {
		active   string
		text     string
		proposed string
		want     bool
	}{
		{"ASK", "explain this", "", false},
		{"ASK", "try to break this", "", false},
		{"ASK", "fix the bug", "", false},
		{"BUILD", "why does this work?", "ASK", true},
		{"BUILD", "reproduce the failure", "FUZZ", true},
		{"BUILD", "implement the fix", "", false},
		{"FUZZ", "explain this", "ASK", true},
		{"FUZZ", "try to break this", "", false},
		{"FUZZ", "fix the bug", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.active+"_"+tc.text, func(t *testing.T) {
			gate := newAuthorityGate(workspace.Workspace(tc.active))
			h := routedHarness(gate, nil, tc.active)
			proposal, ok := turnTransition(h, tc.text)
			if ok != tc.want {
				t.Fatalf("turnTransition(%s, %q) = %v, want %v", tc.active, tc.text, ok, tc.want)
			}
			if ok && proposal.Proposed != tc.proposed {
				t.Errorf("Proposed = %q, want %q", proposal.Proposed, tc.proposed)
			}
			// Whatever the outcome, prediction leaves authority alone.
			if gate.Workspace() != workspace.Workspace(tc.active) {
				t.Errorf("prediction moved the gate to %s", gate.Workspace())
			}
		})
	}
}

// TestTurnTransitionNeverProposesEscalation is the negative authority contract
// at the seam (I8, I9): no classifier output may propose BUILD (or FUZZ, from
// ASK) to the UI.
func TestTurnTransitionNeverProposesEscalation(t *testing.T) {
	for _, active := range []string{"ASK", "FUZZ"} {
		gate := newAuthorityGate(workspace.Workspace(active))
		h := routedHarness(gate, nil, active)
		for _, text := range []string{"implement the fix", "fix the bug", "run the tests", "reproduce the failure"} {
			if proposal, ok := turnTransition(h, text); ok {
				t.Errorf("%s proposed %s for %q", active, proposal.Proposed, text)
			}
		}
	}
}

// TestAcceptedTransitionUsesTheAuthoritativeSwitch proves acceptance routes
// through the existing ownership seam and moves the live gate (I7).
func TestAcceptedTransitionUsesTheAuthoritativeSwitch(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")

	proposal, ok := turnTransition(h, "why does this work?")
	if !ok {
		t.Fatal("no proposal")
	}
	ws, err := h.SwitchWorkspace(proposal.Proposed)
	if err != nil {
		t.Fatalf("SwitchWorkspace: %v", err)
	}
	if ws != workspace.Ask {
		t.Errorf("switched to %s, want ASK", ws)
	}
	if got := gate.Workspace(); got != workspace.Ask {
		t.Errorf("live gate = %s, want ASK", got)
	}
	if h.activeWorkspace() != workspace.Ask {
		t.Errorf("persistent workspace = %s, want ASK", h.activeWorkspace())
	}
}

// TestTransitionKeepsTheSameSession is invariant 11: a workspace transition is
// a state change within one session, never a new session.
func TestTransitionKeepsTheSameSession(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")
	h.sess = &session.Session{ID: 42, Workspace: "build"}
	h.model = ai.Model{Provider: "p", ID: "m"}

	if _, err := h.SwitchWorkspace("ask"); err != nil {
		t.Fatalf("SwitchWorkspace: %v", err)
	}
	if h.sess.ID != 42 {
		t.Errorf("session id = %d, want the original 42", h.sess.ID)
	}
	if h.sess.Workspace != "ask" {
		t.Errorf("session workspace = %q, want ask", h.sess.Workspace)
	}
	if h.sess.Model != "p/m" {
		t.Errorf("session model = %q, want p/m", h.sess.Model)
	}
}

// TestRejectedTransitionLeavesAuthorityUntouched is the conservative half of
// I3: declining a proposal never escalates and never switches.
func TestRejectedTransitionLeavesAuthorityUntouched(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")

	if _, ok := turnTransition(h, "why does this work?"); !ok {
		t.Fatal("expected a proposal")
	}
	// The user rejects: nothing calls SwitchWorkspace.
	if h.activeWorkspace() != workspace.Build {
		t.Errorf("workspace = %s, want BUILD", h.activeWorkspace())
	}
	if got := gate.Workspace(); got != workspace.Build {
		t.Errorf("gate = %s, want BUILD", got)
	}
}

// TestTurnTransitionCarriesNoProbability guards the truthfulness contract
// (I12): the projection exposes an evidence level, never a score.
func TestTurnTransitionCarriesNoProbability(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")
	proposal, ok := turnTransition(h, "why does this work?")
	if !ok {
		t.Fatal("expected a proposal")
	}
	if proposal.Strength != "strong" {
		t.Errorf("Strength = %q, want the word strong", proposal.Strength)
	}
	if proposal.Reason == "" || proposal.Question == "" {
		t.Error("the projection dropped the user-facing copy")
	}
}
