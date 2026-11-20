package agent

import (
	"strings"
	"testing"
)

// A copied runtime state block is orchestration, not a project finding. The
// final answer must not publish it.
func TestSanitizeAnswerRemovesCopiedRuntimeStateBlock(t *testing.T) {
	answer := "The blog loads Markdown from content/posts.\n\n" +
		"# Runtime state (authoritative, not a user request)\n\n" +
		"Sufficiency: INSUFFICIENT (explore)\n" +
		"Available actions: read, grep\n" +
		"Next action: answer from the evidence above. No tool is available.\n"
	got := SanitizeAnswerText(answer)
	if strings.Contains(got, "Runtime state") || strings.Contains(got, "Sufficiency:") ||
		strings.Contains(got, "Available actions:") {
		t.Fatalf("runtime state survived sanitization:\n%s", got)
	}
	if !strings.Contains(got, "content/posts") {
		t.Fatalf("project finding was dropped:\n%s", got)
	}
}

// A legitimate answer heading such as "Verification:" is not a copied state
// field when it stands alone. The boundary must not strip the model's own
// execution summary.
func TestSanitizeAnswerKeepsLegitimateStandaloneSection(t *testing.T) {
	answer := "Verification: `go test ./...` passed after the change."
	if got := SanitizeAnswerText(answer); got != answer {
		t.Fatalf("legitimate section = %q, want unchanged %q", got, answer)
	}
}

// Two legitimate headings are not a copied state block. The sanitizer needs an
// unequivocal runtime directive or the exact header before it strips fields.
func TestSanitizeAnswerKeepsLegitimateHeadings(t *testing.T) {
	answer := "Completion: the requested behavior is implemented.\nVerification: `go test ./...` passed."
	if got := SanitizeAnswerText(answer); got != answer {
		t.Fatalf("legitimate headings were rewritten:\nwant %q\ngot  %q", answer, got)
	}
}

// Fixed runtime directives are not useful project prose. They are removed even
// without the block header.
func TestSanitizeAnswerRemovesStandaloneRuntimeDirective(t *testing.T) {
	answer := "The project is a Next.js blog.\nNo further tools are available."
	got := SanitizeAnswerText(answer)
	if strings.Contains(got, "No further tools") {
		t.Fatalf("runtime directive survived: %q", got)
	}
	if !strings.Contains(got, "Next.js blog") {
		t.Fatalf("project finding was dropped: %q", got)
	}
}

// A fenced code block may quote runtime-looking text as data. Sanitization
// must not rewrite code the user asked to see.
func TestSanitizeAnswerLeavesFencedCodeUntouched(t *testing.T) {
	answer := "Example output:\n```\nSufficiency: INSUFFICIENT\nAvailable actions: none\n```\n"
	if got := SanitizeAnswerText(answer); got != answer {
		t.Fatalf("fenced code was rewritten:\nwant %q\ngot  %q", answer, got)
	}
}

// If the model published only a copied state block, the sanitized answer is
// empty and the ordinary empty-answer rule produces NEEDS_REVIEW. It must never
// fall back to the projection as a public answer.
func TestSanitizeAnswerDoesNotFallBackToCopiedState(t *testing.T) {
	answer := "# Runtime state (authoritative, not a user request)\n\n" +
		"Sufficiency: SUFFICIENT (complete)\n" +
		"Available actions: none\n"
	if got := SanitizeAnswerText(answer); got != "" {
		t.Fatalf("copied state became the answer: %q", got)
	}
}
