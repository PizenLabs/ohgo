package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// The gate must not silently delete a detailed answer. An unsupported global
// absence claim is reported as an uncertified claim and completion refuses to
// mark the task PROVEN; the user still receives the model's one answer, not a
// runtime-authored replacement.
func TestFinalizeAnswerPreservesDraftAndReportsUnsupportedClaim(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "README.md\npackage.json\nlib\ncomponents\n"),
		claimRead("README.md", "# andev0x\nA terminal-style personal blog.\n", 20),
		claimRead("package.json", `{"dependencies":{"next":"16.2.0","react":"19.2.4"}}`, 72),
		claimRead("lib/post-loader.ts", "export async function loadPostsFromReadmes() {}\n", 201),
	}}}
	answer := "This is a Next.js terminal-style blog. There is no database and no headless CMS."

	finalized := FinalizeAnswer(claimContract(), turns, answer, EvidenceProvenance{}, "")
	if finalized.Audit.UnsupportedClaims == 0 {
		t.Fatal("the unsupported absence claims were not audited")
	}
	if finalized.Text != answer {
		t.Fatalf("finalization rewrote the answer instead of auditing it:\n%s", finalized.Text)
	}
	if assessment := AssessGrounding(claimContract(), turns, finalized.Text); assessment.Pass() {
		t.Fatal("the unsupported absence claim passed grounding")
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    append(turns, answerTurnNoTools(finalized.Text)),
		Contract: claimContract(),
	})
	if verdict.Complete() {
		t.Fatal("an answer with an unsupported absence claim was certified complete")
	}
	if len(verdict.Issues) == 0 {
		t.Fatal("the review carried no repairable claim")
	}
}

// The evidence-frontier gap is recorded on the audit and carried by the review
// verdict. It must not be appended to the model's prose as a competing
// conclusion.
func TestFinalizeAnswerRecordsAnEvidenceGap(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA coding agent.\n", 10),
	}}}
	answer := "It is a Go coding agent."
	gap := "component runtime -> SOURCE_CONTENT missing; next: read internal/agent/loop.go"
	finalized := FinalizeAnswer(claimContract(), turns, answer, EvidenceProvenance{}, gap)
	if finalized.Audit.EvidenceGap != gap {
		t.Fatalf("evidence gap = %q, want %q", finalized.Audit.EvidenceGap, gap)
	}
	if finalized.Text != answer {
		t.Fatalf("the frontier gap was appended to the answer:\n%s", finalized.Text)
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:          append(turns, answerTurnNoTools(finalized.Text)),
		Contract:       claimContract(),
		EvidenceReview: gap,
	})
	if verdict.Complete() || !strings.Contains(verdict.Reason, "SOURCE_CONTENT missing") {
		t.Fatalf("review = %q, want the frontier gap", verdict.Reason)
	}
}

// Finalization is idempotent: the host may audit the same answer at more than
// one boundary.
func TestFinalizeAnswerIsIdempotent(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA coding agent.\n", 10),
	}}}
	first := FinalizeAnswer(claimContract(), turns, "There is no database.", EvidenceProvenance{}, "")
	second := FinalizeAnswer(claimContract(), turns, first.Text, EvidenceProvenance{}, "")
	if second.Text != first.Text {
		t.Fatalf("second finalization changed the answer:\nfirst:\n%s\nsecond:\n%s", first.Text, second.Text)
	}
	if first.Audit.UnsupportedClaims != second.Audit.UnsupportedClaims {
		t.Fatalf("audit changed between passes: %d then %d",
			first.Audit.UnsupportedClaims, second.Audit.UnsupportedClaims)
	}
}

// A rejected claim inside a code fence is never edited away: the fence is the
// user's reproduction material and the audit reports the prose claim instead.
func TestFinalizeAnswerDoesNotEditCodeFenceContent(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA coding agent.\n", 10),
	}}}
	answer := "The project is a Go agent.\n\n```\n// no database here\n```"
	finalized := FinalizeAnswer(claimContract(), turns, answer, EvidenceProvenance{}, "")
	if finalized.Text != answer {
		t.Fatalf("the code fence block was edited:\n%s", finalized.Text)
	}
}

// A subdirectory observation cannot support a repository-wide absence. The
// audit reports the claim; the answer is preserved for the review.
func TestFinalizeAnswerReportsGlobalScopeBeyondInspectedSubdirectory(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("content/posts/README.md", "# posts\nFive posts.\n", 5),
	}}}
	answer := "There is no database anywhere in the repository."
	finalized := FinalizeAnswer(claimContract(), turns, answer, EvidenceProvenance{}, "")
	if finalized.Audit.UnsupportedClaims == 0 {
		t.Fatal("a repository-wide absence from a subdirectory observation was not audited")
	}
	if finalized.Text != answer {
		t.Fatalf("the answer was rewritten:\n%s", finalized.Text)
	}
}

// Conflicting evidence is a completion veto. The model's answer is published
// as the one answer; the task cannot become PROVEN until the conflict is
// resolved or the answer is corrected.
func TestConflictingEvidenceVetoesCompletionAndIsPreserved(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# posts\nPosts are loaded from README files.\n", 10),
		claimRead("lib/post-loader.ts", "export const POSTS = [{slug: \"static\"}];\n", 20),
	}}}
	gap := "runtime evidence was not sufficient for the objective (CONFLICTING) at stage answer; missing: resolve the post source contradiction"
	answer := "Posts are loaded from README files."
	finalized := FinalizeAnswer(claimContract(), turns, answer, EvidenceProvenance{}, gap)
	if finalized.Text != answer {
		t.Fatalf("the conflicting-evidence answer was rewritten:\n%s", finalized.Text)
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:          append(turns, answerTurnNoTools(finalized.Text)),
		Contract:       claimContract(),
		EvidenceReview: gap,
	})
	if verdict.Complete() {
		t.Fatal("conflicting evidence was reported complete")
	}
}

// A missing requested facet is a completion gap, not a false factual claim.
// The gate counts it separately and leaves the model's explanation intact.
func TestFinalizeAnswerCountsTheRequestedFacetWithoutRewriting(t *testing.T) {
	intent := claimContract()
	intent.RequiredFacets = []contract.RequiredFacet{{Facet: contract.FacetTests}}
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA Go coding agent.\n", 10),
	}}}
	answer := "This project is a Go coding agent."
	finalized := FinalizeAnswer(intent, turns, answer, EvidenceProvenance{}, "")
	if finalized.Text != answer {
		t.Fatalf("the facet gap rewrote the answer:\n%s", finalized.Text)
	}
	if finalized.Audit.UnresolvedFacets == 0 {
		t.Fatal("the unresolved requested facet was not counted")
	}
	if finalized.Audit.UnsupportedClaims != 0 {
		t.Fatalf("an unresolved facet was counted as a false factual claim: %+v", finalized.Audit)
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    append(turns, answerTurnNoTools(finalized.Text)),
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatalf("an unresolved required facet was completed:\n%s", finalized.Text)
	}
}

// An unsupported absence claim remains the model's sentence. The audit names
// it; the gate does not invent a second "Evidence limitations" conclusion.
func TestFinalizeAnswerDoesNotAppendALimitationSection(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("package.json", `{"scripts":{"lint":"next lint"}}`, 20),
	}}}
	answer := `The lint script exists, but there is no ESLint devDependency and no ESLint config in the inspected tree.`
	finalized := FinalizeAnswer(claimContract(), turns, answer, EvidenceProvenance{}, "")
	if finalized.Text != answer {
		t.Fatalf("the gate appended runtime prose:\n%s", finalized.Text)
	}
	if strings.Contains(finalized.Text, "Evidence limitations") {
		t.Fatalf("the gate appended a competing conclusion:\n%s", finalized.Text)
	}
	if finalized.Audit.UnsupportedClaims == 0 {
		t.Fatal("the unsupported absence claim was not audited")
	}
}
