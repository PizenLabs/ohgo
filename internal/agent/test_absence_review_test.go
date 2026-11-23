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

// Session 970: the answer said "no test scripts declared" (supported by the
// observed package.json) and later "No tests exist" (not supported by the
// static inventory). The review must name the unsupported sentence, not the
// first occurrence of the word "test".
func TestTestAbsenceReviewPointsAtTheUnsupportedSentence(t *testing.T) {
	answer := "**Scripts**: `dev`, `build`, `start`, `lint` — no test scripts declared.\n\n" +
		"No tests exist, and runtime behavior was not executed."
	turns := []Turn{{Results: []ToolOutcome{
		claimStructureTests(0, true),
	}}}
	assessment := AssessGrounding(claimContract(), turns, answer)
	if assessment.Pass() {
		t.Fatal("an unsupported test absence was accepted")
	}
	if len(assessment.Issues) == 0 {
		t.Fatal("no issue was reported")
	}
	location := assessment.Issues[0].Location
	if !strings.Contains(location, "No tests exist") || strings.Contains(location, "Scripts") {
		t.Fatalf("location = %q, want the unsupported sentence", location)
	}
	if reason := strings.ToLower(assessment.Reason()); !strings.Contains(reason, "static") {
		t.Fatalf("reason = %q, want the static-inventory boundary", assessment.Reason())
	}
	directive := completionRepairDirective("{{repairs}}", "", assessment.Reason(), assessment.Repairs(), 0)
	if !strings.Contains(directive, "No tests exist") || strings.Contains(directive, "Scripts") {
		t.Fatalf("directive = %q, want the unsupported sentence", directive)
	}
}

// A statement about a manifest's declaration list is not a repository-wide
// existence claim. The observed package.json is the evidence for "no test
// scripts declared"; reviewing it for source coverage points the model at a
// sentence it already supported.
func TestManifestScriptAbsenceIsNotARepositoryAbsence(t *testing.T) {
	for _, answer := range []string{
		"**Scripts**: `dev`, `build`, `start`, `lint` — no test scripts declared.",
		"package.json declares no test script.",
		"No test scripts are in `package.json`.",
	} {
		if claims := negativeClaims(answer); len(claims) != 0 {
			t.Fatalf("a manifest declaration was read as a repository absence: %q -> %+v", answer, claims)
		}
	}
}

// A declaration verb alone does not make a repository absence a manifest
// declaration. Without a manifest noun or a scripts field, the claim still
// needs coverage.
func TestRepositoryDeclarationAbsenceStillNeedsCoverage(t *testing.T) {
	answer := "The repository declares no tests."
	if claims := negativeClaims(answer); len(claims) == 0 {
		t.Fatal("a repository-wide declaration was treated as a manifest inventory")
	}
}
