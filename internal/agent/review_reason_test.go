package agent

import (
	"strings"
	"testing"
)

// A long single finding is never clipped: the footer can wrap it, but a
// partial sentence would read as a different finding.
func TestCompactReviewReasonKeepsWholeLongClause(t *testing.T) {
	reason := strings.Repeat("provenance detail ", 80)
	got := CompactReviewReason(reason)
	want := strings.TrimSpace(reason)
	if got != want {
		t.Fatalf("reason changed length %d -> %d", len(want), len(got))
	}
	for _, marker := range []string{"...", "…"} {
		if strings.Contains(got, marker) {
			t.Fatalf("reason was clipped with %q", marker)
		}
	}
}

// Duplicate clauses are dropped case-insensitively and the cap lands on a
// semicolon boundary, so only whole actionable findings survive.
func TestCompactReviewReasonCapsDistinctClauses(t *testing.T) {
	got := CompactReviewReason("first; SECOND; second; third; fourth")
	want := "first; SECOND; third"
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}
