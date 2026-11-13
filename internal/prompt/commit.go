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
- use a type from "types:" in the evidence; use a scope from "repository scopes:" when one fits
- optional body: one blank line, <= 5 bullets, each "- " and <= 50 characters
- describe the resulting repository change, not the work used to make it
- do not claim a removal, addition, file, issue, URL, or reference the evidence does not contain
- no preamble, no explanation, no code fence, no trailing spaces`

// CommitTruncatedRetry asks for the same bounded proposal after a reasoning
// model spent its whole completion budget on hidden tokens. It carries no new
// repository facts: the context did not change, only how the budget is spent.
const CommitTruncatedRetry = `The previous reply exhausted its output budget before producing a message.
Return only the commit message now, with no reasoning or explanation.`

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

// CommitRepair is the bounded validation-repair request. It carries the
// already-computed scope and change summary, not the repository again: the
// failed rule is the only new information the model needs.
func CommitRepair(reason, previous, scope string) string {
	return "The previous message was rejected: " + reason +
		"\nPrevious message:\n" + previous +
		"\n\nApproved change set:\n" + scope +
		"\n\nReturn only a corrected commit message."
}
