package agent

import "testing"

// An honest limitation scoped to this run's inspection window is not a claim
// that the evidence is absent or inaccessible. The same inability without the
// scope still reaches review.
func TestBoundedScopeLimitationIsNotAnUnverifiedNegativeResult(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{session276Overview()}}}
	bounded := "The internal product source could not be read in this pass; its implementation remains unverified."
	if codes := evidenceCodes(t, turns, bounded); codes["unverified_negative_result"] {
		t.Fatalf("a bounded run limitation was reviewed as a negative result: %v", codes)
	}

	unbounded := "The internal product source could not be read."
	if codes := evidenceCodes(t, turns, unbounded); !codes["unverified_negative_result"] {
		t.Fatalf("an unbounded inability was accepted: %v", codes)
	}
}
