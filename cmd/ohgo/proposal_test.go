package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// conventionFixture roots a harness at a fixture project with visible
// entries, so the assessment sees an existing convention and stays silent.
func conventionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"README.md", "main.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture\n"), 0o644); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	return root
}

// TestProposalFactsSeesPresenceNotProjectType proves the seam's evidence is
// structural presence only: visible entries establish a convention, hidden
// runtime directories do not, and no file is read or classified.
func TestProposalFactsSeesPresenceNotProjectType(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".ohgo"), 0o755); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if facts := proposalFacts(root); facts.HasConvention || facts.VisibleEntries != 0 {
		t.Errorf("a hidden runtime directory established a convention: %+v", facts)
	}

	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if facts := proposalFacts(root); !facts.HasConvention || facts.VisibleEntries != 1 {
		t.Errorf("a visible entry did not establish a convention: %+v", facts)
	}

	if facts := proposalFacts(""); facts.HasConvention {
		t.Errorf("an empty root established a convention: %+v", facts)
	}
}

// TestTurnTransitionSurfacesRequestDerivedProposal is the core seam
// contract: the UI projection carries a proposal whose question quotes the
// actual request, not a fixed strategy template.
func TestTurnTransitionSurfacesRequestDerivedProposal(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")
	h.root = t.TempDir()

	const request = "Create a small portfolio site for John Smith."
	proposal, ok := turnTransition(h, request)
	if !ok {
		t.Fatal("a target-less greenfield creation produced no proposal")
	}
	if proposal.Kind != tui.TransitionKindProposal {
		t.Errorf("Kind = %q, want the proposal decision", proposal.Kind)
	}
	if proposal.Proposal == nil {
		t.Fatal("the projection dropped the proposal data")
	}
	if proposal.Proposal.Dimension != workspace.ProposalStructure {
		t.Errorf("dimension = %q, want structure", proposal.Proposal.Dimension)
	}
	if !strings.Contains(proposal.Question, "small portfolio site for John Smith") {
		t.Errorf("question %q does not quote the actual request", proposal.Question)
	}
	if proposal.Question == "How should this be structured?" {
		t.Error("the seam fell back to the fixed template")
	}
	if gate.Workspace() != workspace.Build {
		t.Errorf("prediction moved the gate to %s", gate.Workspace())
	}
}

// TestTurnTransitionQuestionsVaryWithTheRequest proves the seam does not
// select a canned question: two different greenfield requests produce two
// different, request-derived questions.
func TestTurnTransitionQuestionsVaryWithTheRequest(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")
	h.root = t.TempDir()

	requests := []string{
		"Create a small portfolio site for John Smith.",
		"Add support for this new configuration format.",
		"Add validation for this input format.",
	}
	seen := map[string]bool{}
	for _, request := range requests {
		proposal, ok := turnTransition(h, request)
		if !ok {
			t.Errorf("%q produced no proposal", request)
			continue
		}
		if !strings.Contains(proposal.Question, proposal.Proposal.Subject) {
			t.Errorf("%q question %q does not quote its subject %q", request, proposal.Question, proposal.Proposal.Subject)
		}
		seen[proposal.Question] = true
	}
	if len(seen) != len(requests) {
		t.Fatalf("distinct questions = %d, want %d", len(seen), len(requests))
	}
}

// TestTurnTransitionSuppressesResolvedProposal pins the generic exits at the
// seam: explicit structure, an established convention, anchored work, and
// ordinary implementation detail each keep the boundary silent.
func TestTurnTransitionSuppressesResolvedProposal(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")
	h.root = t.TempDir()

	for _, request := range []string{
		"create a single-file HTML portfolio",
		"create an HTML/CSS/JS portfolio",
		"Put this in internal/config/loader.go and add tests in internal/config/loader_test.go.",
		"Fix the off-by-one error in the paginator.",
		"Implement a helper function.",
		"Add a test for the parser.",
	} {
		if proposal, ok := turnTransition(h, request); ok {
			t.Errorf("%q proposed (kind %q) despite resolved or ordinary work", request, proposal.Kind)
		}
	}

	h.root = conventionFixture(t)
	if proposal, ok := turnTransition(h, "Create a small portfolio site for John Smith."); ok {
		t.Errorf("the request proposed despite the project convention (kind %q)", proposal.Kind)
	}
}

// TestTurnTransitionAlternativeDimensionUsesTheSameSeam is Case J at the
// production seam: a non-structural unresolved choice is projected with the
// user's own options and still uses the proposal kind.
func TestTurnTransitionAlternativeDimensionUsesTheSameSeam(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")
	h.root = t.TempDir()

	const request = "Implement config loading. Extend the existing loader vs introduce a new loader path."
	proposal, ok := turnTransition(h, request)
	if !ok {
		t.Fatal("an explicit choice produced no proposal")
	}
	if proposal.Kind != tui.TransitionKindProposal || proposal.Proposal == nil {
		t.Fatalf("the seam did not project a proposal: %+v", proposal)
	}
	if proposal.Proposal.Dimension != workspace.ProposalAlternative {
		t.Errorf("dimension = %q, want alternative", proposal.Proposal.Dimension)
	}
	if len(proposal.Proposal.Options) < 2 {
		t.Fatalf("options = %+v, want the request's alternatives", proposal.Proposal.Options)
	}
	if !strings.Contains(proposal.Proposal.Options[0].Label, "Extend the existing loader") {
		t.Errorf("first option = %q, want the request's first approach", proposal.Proposal.Options[0].Label)
	}
}

// TestTurnTransitionPrefersWorkspaceNarrowing pins precedence: a request that
// belongs to a narrower authority surfaces the workspace transition, never a
// proposal.
func TestTurnTransitionPrefersWorkspaceNarrowing(t *testing.T) {
	gate := newAuthorityGate(workspace.Build)
	h := routedHarness(gate, nil, "BUILD")
	h.root = t.TempDir()

	proposal, ok := turnTransition(h, "why does this function return nil here?")
	if !ok {
		t.Fatal("a strong informational request produced no proposal")
	}
	if proposal.Kind != tui.TransitionKindWorkspace || proposal.Proposed != "ASK" {
		t.Errorf("got kind %q proposed %q, want the workspace move to ASK", proposal.Kind, proposal.Proposed)
	}
}
