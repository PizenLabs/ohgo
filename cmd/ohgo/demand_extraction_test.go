package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// The demand estimate must be evaluated against what the request extractor
// actually produces, not only against hand-built contracts. Broad feature and
// analysis requests are the cases that previously stayed at the lowest rung.
func TestReasoningDemandRisesForExtractedBroadRequests(t *testing.T) {
	demand := func(prompt string) cognition.ReasoningDemand {
		intent := workspace.BuildIntentContract(prompt)
		requirement := workspace.AnalyzeRequest(prompt)
		return cognition.AssessReasoningDemand(cognition.ReasoningInput{
			Intent:         &intent,
			Investigate:    requirement.Capability == workspace.CapabilityInvestigate,
			RemainingTurns: -1, RemainingModelCalls: -1,
		})
	}

	if got := demand("Rename this variable."); got != cognition.ReasoningMinimal {
		t.Errorf("mechanical rename = %s, want minimal", got.Label())
	}
	if got := demand("Fix the bug."); got != cognition.ReasoningMinimal {
		t.Errorf("terse bug report = %s, want minimal", got.Label())
	}
	if got := demand("Add a /effort command with a picker."); got != cognition.ReasoningModerate {
		t.Errorf("multi-component feature = %s, want moderate", got.Label())
	}
	if got := demand("Review this architecture and identify correctness and optimization risks."); got != cognition.ReasoningModerate {
		t.Errorf("broad architecture review = %s, want moderate", got.Label())
	}
	refactor := demand("Refactor the task lifecycle across the continuity and control owners without breaking resume, evidence, or role handoffs.")
	if refactor < cognition.ReasoningModerate {
		t.Errorf("cross-owner refactor = %s, want at least moderate", refactor.Label())
	}
}
