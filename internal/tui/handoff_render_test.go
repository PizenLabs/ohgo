package tui

import (
	"strings"
	"testing"
)

// The handoff decision reuses the existing transition card and states what the
// target will receive, the measured workspace, and that approval is required.
func TestHandoffCardDescribesWhatWillBeCarried(t *testing.T) {
	prompt := &TransitionPrompt{
		Current:        "FUZZ",
		Proposed:       "BUILD",
		CurrentAccent:  DarkTokens().Error,
		ProposedAccent: DarkTokens().Error,
		Question:       "Hand off to BUILD?",
		Reason:         "This request changes the project.",
		Kind:           TransitionKindHandoff,
		Handoff: &HandoffView{
			Objective: "fix the flaky test",
			Summary:   "shared context is preserved",
			Workspace: "main; 3 modified",
			Facts:     2,
			Evidence:  1,
			Artifacts: 2,
		},
	}
	card := transitionCard(prompt, 56)
	plain := stripANSI(card)
	for _, want := range []string{
		"Hand off to BUILD?",
		"Task: fix the flaky test",
		"2 facts",
		"1 evidence",
		"2 artifacts",
		"main; 3 modified",
		"requires your approval",
		"Keep FUZZ",
		"BUILD",
		"Esc keeps FUZZ",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("handoff card is missing %q:\n%s", want, plain)
		}
	}
	if prompt.Selected() {
		t.Fatal("the conservative choice is not the default")
	}
}

// A long objective is bounded so the decision never becomes an unbounded block.
func TestHandoffCardBoundsALongObjective(t *testing.T) {
	prompt := &TransitionPrompt{
		Current:  "FUZZ",
		Proposed: "BUILD",
		Question: "Hand off to BUILD?",
		Handoff:  &HandoffView{Objective: strings.Repeat("objective ", 200)},
	}
	card := stripANSI(transitionCard(prompt, 40))
	if len(card) > 2000 {
		t.Fatalf("handoff card = %d bytes, want a bounded decision", len(card))
	}
}
