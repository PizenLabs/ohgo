package agent

import (
	"strings"
	"testing"
)

// Session 302: the model listed the repository root and reported "No README at
// repo root". The absence gate read the word "repo" inside "repo root" as a
// repository-wide scope, so a root listing could not satisfy it and a correct,
// observed statement was sent to review.
func TestRootScopedAbsenceIsSupportedByRootListing(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", ".gitignore\napp/\ncomponents/\ncontent/\ndocs/\nlib/\npackage.json\n"),
		claimRead("package.json", `{"name":"andev0x"}`, 92),
	}}}
	answer := "No README at repo root (docs live in docs/requirement.md)."

	if issues := AssessGrounding(claimContract(), turns, answer).Issues; len(issues) != 0 {
		t.Fatalf("a root-scoped absence covered by a root listing was reviewed: %+v", issues)
	}
	if verdict := claimVerdict(turns, answer); !verdict.Complete() {
		t.Fatalf("a root-scoped absence was reviewed: %s", verdict.Reason)
	}
}

// The same sentence without a root scope stays a repository-wide absence, and a
// root listing cannot establish it.
func TestRepositoryWideAbsenceStillRequiresCoverage(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", ".gitignore\napp/\ncomponents/\ncontent/\ndocs/\nlib/\npackage.json\n"),
		claimRead("package.json", `{"name":"andev0x"}`, 92),
	}}}
	answer := "There is no README anywhere in the repository."

	assessment := AssessGrounding(claimContract(), turns, answer)
	if assessment.Pass() {
		t.Fatal("a repository-wide absence was accepted from a root listing")
	}
	found := false
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_negative_conclusion" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want unsupported_negative_conclusion", assessment.Issues)
	}
}

// Root scope must not swallow a genuine repository-wide claim that also
// mentions a root.
func TestRootMentionDoesNotWeakenRepositoryScope(t *testing.T) {
	if !claimContextIsGlobal("No README exists anywhere in the repository, not even at the root.", 0, 35) {
		t.Fatal("a repository-wide claim was narrowed by an incidental root mention")
	}
	if claimContextIsGlobal("No README at the project root.", 0, 28) {
		t.Fatal("a root-scoped claim was treated as repository-wide")
	}
	if claimContextIsGlobal("No README at top level.", 0, 24) {
		t.Fatal("a top-level claim was treated as repository-wide")
	}
}

// The review detail names the subject so the model can see what is unproven.
func TestRootScopeReviewDetailNamesTheSubject(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("package.json", `{"name":"andev0x"}`, 92),
	}}}
	reason := strings.ToLower(AssessGrounding(claimContract(), turns, "There is no README in the repository.").Reason())
	if !strings.Contains(reason, "readme") {
		t.Fatalf("reason = %q, want the readme subject", reason)
	}
}
