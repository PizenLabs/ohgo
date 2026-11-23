package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// Removal must be exact: splitting and rejoining without a repair reproduces
// the answer byte for byte, including fenced code and list spacing.
func TestSplitAnswerUnitsReconstructsExactly(t *testing.T) {
	const answer = "# Project\n\nFirst fact. Second fact!\n\n```go\nx := 1\n```\n\n- one\n- two\n"
	units := splitAnswerUnits(answer)
	var rebuilt strings.Builder
	for _, unit := range units {
		rebuilt.WriteString(unit.Text)
		rebuilt.WriteString(unit.Sep)
	}
	if rebuilt.String() != answer {
		t.Fatalf("reconstruction differs:\nwant %q\ngot  %q", answer, rebuilt.String())
	}
}

// A review failure marks only the claim it names. Every other claim keeps its
// proven state and text, which is the invariant the whole repair lifecycle
// rests on.
func TestAnswerDraftScopesReviewFailureToTheNamedClaim(t *testing.T) {
	const answer = "The module is ohgo. The entry point is main.go. The project uses HTMX."
	issue := contract.GroundingIssue{
		Code:     "unsupported_documentation_claim",
		Detail:   "the answer presents documentation-only term htmx as an observed repository fact",
		Claim:    "HTMX",
		Location: "The project uses HTMX.",
		Category: "attribution",
	}
	draft := newAnswerDraft(1, 0, answer, []contract.GroundingIssue{issue})
	if len(draft.Claims) != 3 {
		t.Fatalf("claims = %d, want 3", len(draft.Claims))
	}
	if draft.Claims[0].State != ClaimProven || draft.Claims[1].State != ClaimProven {
		t.Fatalf("unrelated claims were invalidated: %+v", draft.Claims)
	}
	if draft.Claims[2].State != ClaimRepairRequired {
		t.Fatalf("target claim state = %s, want REPAIR_REQUIRED", draft.Claims[2].State)
	}
	if got := len(draft.ValidClaims()); got != 2 {
		t.Fatalf("valid claims = %d, want 2", got)
	}
	if got := len(draft.RepairClaims()); got != 1 {
		t.Fatalf("repair claims = %d, want 1", got)
	}
}

// The deterministic fallback removes only the rejected sentence. Headings,
// surviving claims, and their spacing remain intact.
func TestSalvageDraftRemovesOnlyTheRejectedClaim(t *testing.T) {
	const answer = "# Project\n\nThe module is ohgo. The project uses HTMX. The entry point is main.go."
	issue := contract.GroundingIssue{
		Code:     "unsupported_documentation_claim",
		Claim:    "HTMX",
		Location: "The project uses HTMX.",
		Category: "attribution",
	}
	draft := newAnswerDraft(1, 0, answer, []contract.GroundingIssue{issue})
	text, salvaged, ok := salvageDraft(draft)
	if !ok {
		t.Fatal("salvage refused a claim it could locate")
	}
	if strings.Contains(text, "HTMX") {
		t.Fatalf("salvage kept the rejected claim:\n%s", text)
	}
	for _, keep := range []string{"# Project", "The module is ohgo.", "The entry point is main.go."} {
		if !strings.Contains(text, keep) {
			t.Fatalf("salvage lost %q:\n%s", keep, text)
		}
	}
	if len(salvaged.ValidClaims()) != 2 {
		t.Fatalf("salvaged valid claims = %d, want the two surviving facts", len(salvaged.ValidClaims()))
	}
}

// An unlocated finding cannot be repaired by deleting a guessed sentence.
func TestSalvageDraftRefusesAnUnlocatedFinding(t *testing.T) {
	draft := newAnswerDraft(1, 0, "The module is ohgo. It is small.", []contract.GroundingIssue{{
		Code:   "answer_review",
		Detail: "the answer does not satisfy the request's evidence contract",
	}})
	if text, _, ok := salvageDraft(draft); ok {
		t.Fatalf("salvage guessed a sentence for an unlocated finding: %q", text)
	}
}

// A facet gap is a completion gap, not a false claim. It must not invalidate a
// sentence or make deterministic repair unsafe.
func TestAnswerDraftDoesNotInvalidateClaimsForAFacetGap(t *testing.T) {
	draft := newAnswerDraft(1, 0, "The module is ohgo. Tests: not inspected.", []contract.GroundingIssue{{
		Code:     "required_facet_tests",
		Category: "facet",
		Claim:    "tests",
		Detail:   "the requested test inventory is not established",
	}})
	if got := len(draft.RepairClaims()); got != 0 {
		t.Fatalf("facet gap invalidated %d claims", got)
	}
	if got := len(draft.ValidClaims()); got != 2 {
		t.Fatalf("valid claims = %d, want both sentences", got)
	}
	if len(draft.FacetIssues) != 1 {
		t.Fatalf("facet issues = %d, want 1", len(draft.FacetIssues))
	}
}

// A preservation check recognises a reworded but still-present claim, and
// reports a claim the revision dropped.
func TestMissingClaimsRecognizesRewordedAndDroppedClaims(t *testing.T) {
	best := newAnswerDraft(1, 0,
		"The project stores its sessions in SQLite. The CLI is built with cobra. The watcher tracks changes.", nil)
	missing := missingClaims(best, "The project stores sessions in SQLite. The CLI is built with cobra.")
	if len(missing) != 1 || !strings.Contains(missing[0].Text, "watcher") {
		t.Fatalf("missing = %+v, want only the watcher claim", missing)
	}
}
