package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// The exact historical failure: a plausible survey ends with a repository-wide
// absence claim that a source grep cannot prove. Finalization must keep the
// supported project description, withhold the absolute claim, and leave an
// auditable limitation. The committed text must pass the same audit that
// completion runs.
func TestFinalizeAnswerQualifiesUnsupportedGlobalAbsence(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing(".", "README.md\npackage.json\nlib\ncomponents\n"),
		claimRead("README.md", "# andev0x\nA terminal-style personal blog.\n", 20),
		claimRead("package.json", `{"dependencies":{"next":"16.2.0","react":"19.2.4"}}`, 72),
		claimRead("lib/post-loader.ts", "export async function loadPostsFromReadmes() {}\n", 201),
	}}}
	answer := "This is a Next.js terminal-style blog. There is no database and no headless CMS."

	finalized := FinalizeAnswer(claimContract(), turns, answer, EvidenceProvenance{}, "")
	if finalized.Audit.DraftUnsupportedClaims == 0 {
		t.Fatal("the unsupported absence claims were not audited")
	}
	if finalized.Audit.UnsupportedClaims != 0 {
		t.Fatalf("the committed answer still has %d unsupported claims: %s",
			finalized.Audit.UnsupportedClaims, finalized.Text)
	}
	if !strings.Contains(finalized.Text, "Next.js terminal-style blog") {
		t.Fatalf("finalized answer dropped the supported description:\n%s", finalized.Text)
	}
	if strings.Contains(finalized.Text, "There is no database") {
		t.Fatalf("the absolute absence claim survived finalization:\n%s", finalized.Text)
	}
	if !strings.Contains(finalized.Text, "Evidence limitations") || !strings.Contains(finalized.Text, "database") {
		t.Fatalf("the limitation report is missing the rejected subject:\n%s", finalized.Text)
	}
	if assessment := AssessGrounding(claimContract(), turns, finalized.Text); !assessment.Pass() {
		t.Fatalf("the committed answer still fails the grounding audit: %s", assessment.Reason())
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    append(turns, answerTurnNoTools(finalized.Text)),
		Contract: claimContract(),
	})
	if !verdict.Complete() {
		t.Fatalf("the qualified answer was not completable: %s", verdict.Reason)
	}
}

// An evidence-frontier gap must appear in the committed text even when the
// model's sentence passes the prose audit. This is the answer-then-realize
// shape: the claim is plausible, but the runtime knows the required evidence
// was never acquired.
func TestFinalizeAnswerRecordsAnEvidenceGap(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA coding agent.\n", 10),
	}}}
	finalized := FinalizeAnswer(claimContract(), turns,
		"It is a Go coding agent.", EvidenceProvenance{},
		"component runtime -> SOURCE_CONTENT missing; next: read internal/agent/loop.go")
	if !finalized.Audit.Qualified {
		t.Fatal("an answer with an open frontier was not qualified")
	}
	if !strings.Contains(finalized.Text, "SOURCE_CONTENT missing") {
		t.Fatalf("the frontier gap was not published:\n%s", finalized.Text)
	}
	if strings.Contains(finalized.Text, "Evidence limitations") == false {
		t.Fatalf("the limitation heading is missing:\n%s", finalized.Text)
	}
}

// Finalization is idempotent: the host may audit the same answer at more than
// one boundary, and a second pass must not append another limitation block.
func TestFinalizeAnswerIsIdempotent(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA coding agent.\n", 10),
	}}}
	first := FinalizeAnswer(claimContract(), turns, "There is no database.", EvidenceProvenance{}, "")
	second := FinalizeAnswer(claimContract(), turns, first.Text, EvidenceProvenance{}, "")
	if second.Text != first.Text {
		t.Fatalf("second finalization changed the committed answer:\nfirst:\n%s\nsecond:\n%s", first.Text, second.Text)
	}
	if strings.Count(second.Text, "Evidence limitations") != 1 {
		t.Fatalf("limitation block duplicated:\n%s", second.Text)
	}
}

// A rejected claim inside a code fence is not deleted: the limitation is
// published, but the user's reproduction material remains intact.
func TestFinalizeAnswerDoesNotDeleteCodeFenceContent(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA coding agent.\n", 10),
	}}}
	answer := "The project is a Go agent.\n\n```\n// no database here\n```"
	finalized := FinalizeAnswer(claimContract(), turns, answer, EvidenceProvenance{}, "")
	if !strings.Contains(finalized.Text, "// no database here") {
		t.Fatalf("the code fence was deleted:\n%s", finalized.Text)
	}
}

// A subdirectory observation cannot support a repository-wide absence. The
// absolute claim is withheld and the committed answer keeps the inspected
// scope.
func TestFinalizeAnswerRejectsGlobalScopeBeyondInspectedSubdirectory(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("content/posts/README.md", "# posts\nFive posts.\n", 5),
	}}}
	finalized := FinalizeAnswer(claimContract(), turns,
		"There is no database anywhere in the repository.", EvidenceProvenance{}, "")
	if strings.Contains(finalized.Text, "There is no database anywhere in the repository") {
		t.Fatalf("a global absence claim survived a subdirectory observation:\n%s", finalized.Text)
	}
	if !strings.Contains(finalized.Text, "Evidence limitations") {
		t.Fatalf("the scope limitation is missing:\n%s", finalized.Text)
	}
}

// Conflicting evidence is a completion veto. Finalization may publish the
// model's answer with the conflict named, but the task cannot become PROVEN
// until the conflict is resolved.
func TestConflictingEvidenceVetoesCompletionAndIsPublished(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# posts\nPosts are loaded from README files.\n", 10),
		claimRead("lib/post-loader.ts", "export const POSTS = [{slug: \"static\"}];\n", 20),
	}}}
	gap := "runtime evidence was not sufficient for the objective (CONFLICTING) at stage answer; missing: resolve the post source contradiction"
	finalized := FinalizeAnswer(claimContract(), turns,
		"Posts are loaded from README files.", EvidenceProvenance{}, gap)
	if !strings.Contains(finalized.Text, "CONFLICTING") {
		t.Fatalf("the conflict was not surfaced in the committed answer:\n%s", finalized.Text)
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

// When a requested facet is missing, the limitation must name the deliverable
// the user asked for. "required_facet_missing" is audit vocabulary, not an
// answer.
func TestFinalizeAnswerNamesTheRequestedFacet(t *testing.T) {
	intent := claimContract()
	intent.RequiredFacets = []contract.RequiredFacet{{Facet: contract.FacetTests}}
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA Go coding agent.\n", 10),
	}}}
	finalized := FinalizeAnswer(intent, turns, "This project is a Go coding agent.", EvidenceProvenance{}, "")
	if !strings.Contains(finalized.Text, "tests:") {
		t.Fatalf("the limitation did not name the requested facet:\n%s", finalized.Text)
	}
	if strings.Contains(finalized.Text, "required_facet_missing") {
		t.Fatalf("internal audit vocabulary leaked into the answer:\n%s", finalized.Text)
	}
	if finalized.Audit.UnresolvedFacets == 0 {
		t.Fatal("the unresolved requested facet was not counted")
	}
	if finalized.Audit.UnsupportedClaims != 0 {
		t.Fatalf("an acknowledged facet gap was counted as a false factual claim: %+v", finalized.Audit)
	}
}

// A limitation that names a requested facet is still an unresolved required
// claim. It may be published, but it cannot make the task PROVEN.
func TestFinalizationLimitationForRequiredFacetRemainsReview(t *testing.T) {
	intent := claimContract()
	intent.RequiredFacets = []contract.RequiredFacet{{Facet: contract.FacetTests}}
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("README.md", "# ohgo\nA Go coding agent.\n", 10),
	}}}
	finalized := FinalizeAnswer(intent, turns, "This project is a Go coding agent.", EvidenceProvenance{}, "")
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    append(turns, answerTurnNoTools(finalized.Text)),
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatalf("an unresolved required facet was completed:\n%s", finalized.Text)
	}
}

// A limitation must not repeat the absence grammar of the claim it is
// qualifying. "and no eslint config" in the limitation would itself be a new
// unsupported absence claim.
func TestFinalizeAnswerNeutralizesAbsenceGrammarInLimitations(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimRead("package.json", `{"scripts":{"lint":"next lint"}}`, 20),
	}}}
	finalized := FinalizeAnswer(claimContract(), turns,
		`The lint script exists, but there is no ESLint devDependency and no ESLint config in the inspected tree.`,
		EvidenceProvenance{}, "")
	if strings.Contains(strings.ToLower(finalized.Text), "no eslint") {
		t.Fatalf("the limitation repeated the absence claim:\n%s", finalized.Text)
	}
	if finalized.Audit.UnsupportedClaims != 0 {
		t.Fatalf("the committed limitation still has %d unsupported claims:\n%s",
			finalized.Audit.UnsupportedClaims, finalized.Text)
	}
}
