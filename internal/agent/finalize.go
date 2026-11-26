package agent

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// AnswerAudit is the machine-readable record of the answer gate.
//
// It is audit data, never authority: completion still evaluates the same text
// and the durable observations. A non-zero UnsupportedClaims count says the
// runtime refused to certify the text, so a host must not publish it as the
// final answer; the raw turn remains in the completion input so the review can
// name the exact claims.
type AnswerAudit struct {
	// Finalized reports that the gate ran for an accepted answer.
	Finalized bool `json:"finalized"`
	// UnsupportedClaims is the number of claims the deterministic audit did
	// not certify. The answer is still published, because deleting sentences
	// silently destroys a detailed explanation; completion converts this
	// count into NEEDS_REVIEW with the exact claims and requirements.
	UnsupportedClaims int `json:"unsupported_claims"`
	// UnresolvedFacets is the number of requested deliverables the answer
	// explicitly leaves unverified. It is a completion gap, not a false
	// factual claim, so it is reported separately.
	UnresolvedFacets int `json:"unresolved_facets,omitempty"`
	// EvidenceGap is the control plane's frontier gap at finalization.
	EvidenceGap string `json:"evidence_gap,omitempty"`
	// Issues are the claim-level audit findings.
	Issues []contract.GroundingIssue `json:"issues,omitempty"`
}

// Approved reports whether the gate accepted the answer for publication.
//
// An unresolved facet is a completion gap the answer may honestly state; it is
// not a false factual claim, so it does not block publication. An uncertified
// claim does: publishing it as the final answer is exactly the contradiction
// between the response transaction and the completion verdict this gate
// exists to prevent.
func (a AnswerAudit) Approved() bool {
	return a.UnsupportedClaims == 0
}

// FinalizedAnswer is the answer text a host may commit for one run, with the
// audit that judged it.
type FinalizedAnswer struct {
	// Text is the sanitized model answer. The gate never removes a substantive
	// sentence: an unsupported claim is reported to the user and to the
	// completion authority instead of being silently replaced with runtime
	// prose.
	Text string
	// Audit records what the gate observed.
	Audit AnswerAudit
}

// FinalizeAnswer is the last answer-level gate before publication.
//
// It is deliberately not an editor and not an LLM judge. It removes copied
// runtime state from the text, then runs the same deterministic grounding and
// provenance audits completion uses. Unsupported claims are reported, not
// deleted: a sentence-level deletion mangles tables, lists, and fenced
// examples, and appending a runtime-authored "Evidence limitations" section
// produces a competing conclusion the user never asked for.
//
// The completion authority still refuses to certify an answer with uncertified
// claims, so the audit remains a real boundary. Approved reports whether the
// text may be published; a refused answer stays in the turn for completion to
// name what was not established.
//
// The function is idempotent. It cannot authorize an action, add a capability,
// or mark a task complete.
func FinalizeAnswer(intent contract.IntentContract, turns []Turn, answer string, provenance EvidenceProvenance, evidenceGap string) FinalizedAnswer {
	answer = SanitizeAnswerText(answer)
	evidenceGap = strings.TrimSpace(evidenceGap)
	issues := auditAnswerClaims(intent, turns, answer, provenance)
	audit := AnswerAudit{
		Finalized:   true,
		EvidenceGap: evidenceGap,
		Issues:      append([]contract.GroundingIssue(nil), issues...),
	}
	for _, issue := range issues {
		if isFacetIssue(issue) {
			audit.UnresolvedFacets++
			continue
		}
		audit.UnsupportedClaims++
	}
	return FinalizedAnswer{Text: answer, Audit: audit}
}

// isFacetIssue reports a requested-deliverable gap. The answer may honestly
// say it could not establish the facet; that is a completion gap, not a false
// factual claim.
func isFacetIssue(issue contract.GroundingIssue) bool {
	return issue.Category == "facet" || strings.HasPrefix(issue.Code, "required_facet_")
}

// auditAnswerClaims is the one deterministic claim audit used by the final
// gate and by the correction review.
func auditAnswerClaims(intent contract.IntentContract, turns []Turn, answer string, provenance EvidenceProvenance) []contract.GroundingIssue {
	issues := append([]contract.GroundingIssue(nil), AssessGrounding(intent, turns, answer).Issues...)
	if intent.Kind == contract.ObjectiveInformational {
		issues = append(issues, AssessAnswerProvenance(intent, answer, provenance).Issues...)
	}
	issues = append(issues, AssessVerificationClaims(intent, turns, answer).Issues...)
	return issues
}
