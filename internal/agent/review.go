package agent

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// ReviewInformationalAnswer is the answer-level gate a host installs on the
// loop for an informational or investigative turn.
//
// It is deliberately narrower than completion: it can ask for one corrected
// answer, but it cannot prove a mutation, grant authority, or publish a result.
// The final verdict still comes from EvaluateTaskCompletion after the loop
// stops. A mutation objective is not reviewed here because its correction is a
// real change plus a check, not a rewritten sentence.
func ReviewInformationalAnswer(intent contract.IntentContract, turns []Turn, answer string) string {
	if intent.Kind != contract.ObjectiveInformational {
		return ""
	}
	if strings.TrimSpace(answer) == "" {
		return "the answer contains no project finding"
	}
	assessment := AssessGrounding(intent, turns, answer)
	if assessment.Pass() {
		return ""
	}
	if reason := strings.TrimSpace(assessment.Reason()); reason != "" {
		return reason
	}
	return "the answer does not satisfy the request's evidence contract"
}

// answerReviewReason runs the installed answer review, if any.
func (l *Loop) answerReviewReason(turns []Turn) string {
	if l == nil || l.cfg.AnswerReview == nil || len(turns) == 0 {
		return ""
	}
	answer := turns[len(turns)-1].Assistant.Text()
	return strings.TrimSpace(l.cfg.AnswerReview(turns, answer))
}

// completionReviewDirective renders the one correction instruction. An empty
// template means the host did not authorize a correction turn, so the rejected
// answer is returned to the completion evaluator instead.
func completionReviewDirective(template, reason string) string {
	template = strings.TrimSpace(template)
	reason = strings.TrimSpace(reason)
	if template == "" || reason == "" {
		return ""
	}
	return strings.ReplaceAll(template, "{{reason}}", reason)
}
