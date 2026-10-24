package prompt

// Commit is the system prompt for the dedicated commit-authoring role.
//
// The model proposes text only. It receives bounded repository evidence and no
// tools, so it has no path to stage, execute, or rewrite anything. Every rule
// stated here is also enforced deterministically in the commit package; the
// prose exists to make the model succeed on the first attempt, not to be the
// boundary.
const Commit = `You write one Git commit message for the change described by the evidence.

Reply with the commit message and nothing else: no preamble, no explanation,
no code fences, and no trailing text.

Structure, exactly:

type(scope): message

- change summary
- change summary

Rules:
- The first line is the subject: lowercase conventional-commit type, a scope in
  parentheses, then a colon, one space, and a concise message.
- Use one of these types: feat, fix, refactor, test, docs, chore, perf, build,
  ci, style, revert.
- The complete subject line is at most 36 characters.
- If there is a body, put exactly one blank line between the subject and it.
- Each body line starts with "- " and is a single concrete change.
- Each complete bullet line is at most 50 characters, including the "- ".
- Keep at most five bullets. An empty body is valid when the subject already
  says everything.
- Describe only changes present in the evidence. Do not invent files, features,
  issues, or pull-request numbers.
- A reference such as #123 may appear only when it appears in the evidence, and
  it counts toward the line's length limit.
- Do not end a line with a space or a tab.`

// CommitEvidence introduces the bounded repository evidence sent as the user
// message. Keeping the label here means the exact request shape is auditable in
// one place.
func CommitEvidence(evidence string) string {
	return "Repository evidence for this one commit:\n\n" + evidence
}

// CommitRetry asks for a corrected message after deterministic validation
// rejected the previous one. The validation error is trusted control-plane
// text, never model output.
func CommitRetry(reason, previous string) string {
	return "The previous message was rejected by validation: " + reason +
		"\n\nPrevious message:\n" + previous +
		"\n\nReturn a corrected commit message only, following the required structure."
}
