package prompt

import (
	"fmt"
	"unicode/utf8"
)

// Commit is the system prompt for the dedicated commit-authoring role.
//
// It carries only the message contract and the bounded evidence. The model has
// no tools, no repository access, and no session context: staging, scope,
// authorization, execution, and verification are deterministic and stay
// outside this prompt. Every rule here is also enforced by commit.Parse; the
// prose exists to make the first attempt succeed, not to be the boundary.
const Commit = `Write one Git commit message for the evidence below.

Output only the message, in exactly this shape:

type(scope): summary

- change

Rules:
- the complete subject must be at most 36 characters
- subject form: lowercase type, a required scope in parentheses, colon, summary
- keep the summary near 15 characters so the full subject fits
- types: feat fix refactor test docs chore perf build ci style revert
- optional body: one blank line, <= 5 bullets, each "- " and <= 50 characters
- describe only changes in the evidence; never invent files, issues, URLs, or references
- no preamble, no explanation, no code fence, no trailing spaces`

// CommitShorten rewrites only an overlong subject. It is a focused correction:
// the body was already valid and the model does not need the repository again.
const CommitShorten = `Rewrite the Git commit subject to at most 36 characters.
Keep the type(scope) prefix. Replace the summary with a shorter phrase of at most 3 words.
Do not add new claims. Output only the subject line, nothing else.`

// CommitEvidence introduces the bounded repository evidence sent as the user
// message. Keeping the label here means the exact request shape is auditable in
// one place.
func CommitEvidence(evidence string) string {
	return "Repository evidence for this one commit:\n\n" + evidence
}

// CommitShortenSubject carries the one line the focused correction rewrites.
// The subject is model output, so it is untrusted data, never instructions. The
// measured length is stated explicitly because a small model does not reliably
// count the prefix it is being asked to preserve.
func CommitShortenSubject(subject string) string {
	return fmt.Sprintf("Current subject (%d characters):\n%s\n\nThe complete subject must be at most 36 characters.",
		utf8.RuneCountInString(subject), subject)
}

// CommitRetry asks for a corrected message after deterministic validation
// rejected the previous one. The validation error is trusted control-plane
// text, never model output.
func CommitRetry(reason, previous string) string {
	return "The previous message was rejected: " + reason +
		"\nPrevious message:\n" + previous +
		"\n\nReturn only a corrected commit message."
}
