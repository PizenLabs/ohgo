package workspace

import (
	"regexp"
	"strings"
)

// Conditional workflow analysis.
//
// Ohgo's existing classifier is clause-aware but treats every mutation phrase
// as part of the current objective. A staged request is different: it names an
// investigation or understanding phase first, then makes the change conditional
// on what that phase finds. The distinction changes routing (start in
// investigation) and completion (do not demand a change the branch has not
// reached) without changing what the change itself requires when it happens.
//
// The analysis here is finite and deterministic, like the rest of the
// classifier. It does not understand the condition; it only preserves the
// request's own structure.

// requestSentenceBoundary splits a request into sentences without relying on
// normalizeRequest, which intentionally collapses punctuation used for phrase
// matching. A newline, period, question mark, exclamation mark, or semicolon
// ends a sentence.
var requestSentenceBoundary = regexp.MustCompile(`[.!?;\n]+`)

// investigationFirstPattern matches an explicit instruction to investigate
// before doing anything else. "Investigate the flow" alone is a normal
// investigation request; only the staging words make it a first phase.
var investigationFirstPattern = regexp.MustCompile(
	`(?is)\b(?:start|begin|open)\s+(?:by|with)\s+(?:investigat\w*|reproduc\w*|diagnos\w*|inspect\w*|check\w*|explor\w*|understand\w*)` +
		`|\b(?:investigat\w*|reproduc\w*|diagnos\w*|inspect\w*|explor\w*)\b[^.;\n]{0,48}\bfirst\b` +
		`|\bfirst\b[^.;\n]{0,48}\b(?:investigat\w*|reproduc\w*|diagnos\w*|inspect\w*|explor\w*)`)

// conditionalOpeners start a condition clause. "Whether" is deliberately absent:
// it introduces a question, not a condition on an imperative.
var conditionalOpeners = []string{
	"if", "when", "once", "after", "unless", "provided", "assuming",
}

// ConditionalWorkflow reports whether a request stages an investigation or
// understanding phase before a conditional future change.
//
// It is true only when all three hold:
//
//   - the request explicitly starts in investigation ("start by investigating",
//     "investigate first");
//   - at least one mutation clause is guarded by a condition;
//   - no mutation clause is unconditional, because an unconditional change is
//     part of the current objective regardless of what the investigation finds.
//
// This is structure, not sentiment: "If the tests fail, fix them" is a
// conditional mutation but not a staged workflow, because it does not start with
// an investigation phase. "Investigate first; if the issue is confirmed, fix
// it" is both.
func ConditionalWorkflow(text string) bool {
	if !startsInInvestigation(text) {
		return false
	}
	conditional, unconditional := mutationConditions(text)
	return conditional && !unconditional
}

// startsInInvestigation reports whether the request opens with an explicit
// investigation phase.
func startsInInvestigation(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if investigationFirstPattern.MatchString(text) {
		return true
	}
	// A first sentence headed by an investigation verb is also an opening
	// investigation phase when the request later makes its change conditional.
	// The conditional half is checked by ConditionalWorkflow.
	first := firstRequestSentence(text)
	if first == "" {
		return false
	}
	for _, entry := range requestClauses(normalizeRequest(first)) {
		if entry.asked {
			continue
		}
		head := clauseHead(entry.text)
		if headMatches(investigateHeads, head) || investigationPhaseHeads[head] {
			return true
		}
	}
	return false
}

// investigationPhaseHeads open a request by inspecting the project. On their
// own they are READ; together with a later conditional change they describe the
// investigation phase that must happen before the branch is decided.
var investigationPhaseHeads = map[string]bool{
	"check": true, "inspect": true, "review": true, "analyze": true,
	"analyse": true, "explore": true, "understand": true, "read": true,
	"examine": true,
}

// mutationConditions reports whether the request contains a conditional and an
// unconditional mutation clause.
func mutationConditions(text string) (conditional, unconditional bool) {
	for _, sentence := range requestSentenceBoundary.Split(text, -1) {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		guarded := false
		for _, entry := range requestClauses(normalizeRequest(sentence)) {
			if opensConditionalClause(entry.text) {
				guarded = true
				continue
			}
			if !clauseRequestsMutation(entry) {
				continue
			}
			if guarded {
				conditional = true
			} else {
				unconditional = true
			}
		}
	}
	return conditional, unconditional
}

// opensConditionalClause reports whether a clause starts a condition.
func opensConditionalClause(text string) bool {
	head := clauseHead(text)
	for _, opener := range conditionalOpeners {
		if head == opener {
			return true
		}
	}
	return false
}

// firstRequestSentence returns the first non-empty sentence.
func firstRequestSentence(text string) string {
	for _, sentence := range requestSentenceBoundary.Split(text, -1) {
		if trimmed := strings.TrimSpace(sentence); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
