package agent

import (
	"strings"
	"testing"
)

// Session 307: the answer said "No Go tests exist" while the structure
// overview it had already received reported test_files: 5. The review asked for
// evidence the run had instead of naming the contradiction it was holding.
func TestTestAbsenceReviewNamesObservedTestFiles(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimStructureOverview(5, false, true),
	}}}
	assessment := AssessGrounding(claimContract(), turns, "No Go tests exist.")
	if assessment.Pass() {
		t.Fatal("a contradicted test absence was accepted")
	}
	reason := strings.ToLower(assessment.Reason())
	if !strings.Contains(reason, "5 test file") {
		t.Fatalf("reason = %q, want the observed test-file count", assessment.Reason())
	}
	if strings.Contains(reason, "without evidence covering the claim") {
		t.Fatalf("reason = %q, want the contradiction, not a request for evidence", assessment.Reason())
	}
}

// With no observed test files the review keeps the general form: absence is
// still unproven rather than contradicted.
func TestTestAbsenceReviewWithoutAnObservedCountStaysGeneral(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimStructureOverview(0, false, true),
	}}}
	reason := strings.ToLower(AssessGrounding(claimContract(), turns, "No Go tests exist.").Reason())
	if !strings.Contains(reason, "without evidence covering the claim") {
		t.Fatalf("reason = %q, want the general absence reason", reason)
	}
}

// The contradiction detail is reserved for a test claim; an unrelated absence
// keeps the general form even when test files were counted.
func TestTestFileCountDoesNotRewriteUnrelatedAbsenceReasons(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimStructureOverview(5, false, true),
	}}}
	reason := strings.ToLower(AssessGrounding(claimContract(), turns, "No README exists anywhere in the repository.").Reason())
	if strings.Contains(reason, "test file") {
		t.Fatalf("reason = %q, want the readme claim, not the test count", reason)
	}
}
