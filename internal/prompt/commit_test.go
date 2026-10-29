package prompt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCommitEvidenceKeepsTheEvidenceVerbatim(t *testing.T) {
	evidence := "approved scope:\nM a.go"
	got := CommitEvidence(evidence)
	const prefix = "Repository evidence for this one commit:\n\n"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("CommitEvidence = %q, want the fixed evidence label", got)
	}
	if got != prefix+evidence {
		t.Errorf("CommitEvidence = %q, want the evidence byte for byte", got)
	}
}

func TestCommitShortenSubjectCountsRunesNotBytes(t *testing.T) {
	// A model that counts bytes would believe this subject already exceeds 36
	// characters, so the measured count must be the human-visible one.
	subject := "docs(runtime): 界界界"
	got := CommitShortenSubject(subject)
	if !strings.Contains(got, "Current subject (18 characters):") {
		t.Errorf("CommitShortenSubject = %q, want the rune count", got)
	}
	if !strings.Contains(got, subject) {
		t.Errorf("CommitShortenSubject = %q, want the subject verbatim", got)
	}
	if utf8.RuneCountInString(subject) != 18 {
		t.Fatalf("fixture has %d runes, want 18", utf8.RuneCountInString(subject))
	}
}

func TestCommitRetryCarriesTheRuleAndPreviousMessage(t *testing.T) {
	got := CommitRetry("subject is too long", "feat(scope): x")
	for _, want := range []string{
		"The previous message was rejected: subject is too long",
		"Previous message:\nfeat(scope): x",
		"Return only a corrected commit message.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CommitRetry = %q, want %q", got, want)
		}
	}
}
