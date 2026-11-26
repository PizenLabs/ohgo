package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// Verification claims are the answer-side counterpart to verification
// evidence classes.
//
// A check can prove that files build, that a test passed, or that a live page
// rendered. Those are not interchangeable: a static build or source assertion
// cannot establish that an animation moves, a page scrolls, or a service
// behaves as claimed. This audit rejects an observation claim that no
// behavioral check followed. It is deterministic and claim-scoped; completion
// still owns the final verdict and the evidence ledger still owns the facts.

// runtimeRenderingPattern names an observation a live HTTP or browser check
// can support: the page rendered, served, loaded, or displayed the content the
// check asserted.
var runtimeRenderingPattern = regexp.MustCompile(
	`(?i)\b(?:renders?|rendered|rendering|serves?|served|responding|responds|` +
		`loads?|loaded|shows?|shown|displayed\s+as|displays\s+as)\b`)

// unmeasuredBehaviorPattern names a motion or performance claim. No current
// verification class measures whether an animation moves, a scroll is
// seamless, or a page has no lag. A browser text check proves the rendered
// content, not the motion; a static check proves even less. These claims stay
// unverified until the runtime has an observation that actually measures them.
var unmeasuredBehaviorPattern = regexp.MustCompile(
	`(?i)\b(?:seamless(?:ly)?|without\s+(?:any\s+)?lag|no\s+lag|smooth(?:ly)?|` +
		`jank(?:y|iness)?|works?|moves?|moving|scrolls?|scrolled|animates?|animating|` +
		`loops?|looping|slides?|sliding|plays?|playing)\b`)

// runtimePagePattern catches a present-tense observation whose subject is the
// running artifact. "The page shows the cards" is runtime behavior; "the card
// shows a title" describes markup and stays an implementation claim.
var runtimePagePattern = regexp.MustCompile(
	`(?i)\b(?:the|this)\s+(?:page|site|section|app|application|server|service|project|runtime)\b` +
		`[^.\n]{0,100}\b(?:shows?|shown|displays?|displayed|loads?|loaded|running|runs?|works?|working)\b`)

// runtimeStatePattern is the narrow claim a live runtime observation supports:
// the process is up, ready, or running. It does not support a behavior verb
// such as "scrolls" or "renders".
var runtimeStatePattern = regexp.MustCompile(
	`(?i)\b(?:is|are|was|were|stays?|remains?)\s+(?:running|up|ready|live|serving)\b|` +
		`\b(?:runs|running|serving)\b`)

// AssessVerificationClaims checks whether a mutation or execution answer
// claims observed behavior that the run's verification class cannot support.
//
// Only VerificationBehavioral after the last change can support these claims.
// A test run is not a live observation, and a static build or shell assertion
// is weaker still. The audit is intentionally conservative: implementation
// statements that do not use an observation verb are left to the existing
// source and target audits.
func AssessVerificationClaims(intent contract.IntentContract, turns []Turn, answer string) contract.GroundingAssessment {
	assessment := contract.GroundingAssessment{Verdict: contract.GroundingPass}
	if intent.Kind != contract.ObjectiveMutation && intent.Kind != contract.ObjectiveExecution {
		return assessment
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return assessment
	}
	evidence := scanCompletionEvidence(turns, intent.Kind == contract.ObjectiveMutation, false)
	runtimeObserved := evidence.lastRuntime >= evidence.lastPotentialMutation

	seen := map[string]bool{}
	for _, raw := range claimSentenceBoundary.Split(answer, -1) {
		sentence := strings.TrimSpace(raw)
		if sentence == "" || claimIsNonAssertive(sentence) || claimIsBounded(sentence) ||
			claimIsDocumentationAttributed(sentence) || claimIsRetraction(sentence) {
			continue
		}
		rendering := runtimeRenderingPattern.MatchString(sentence)
		motion := unmeasuredBehaviorPattern.MatchString(sentence)
		page := runtimePagePattern.MatchString(sentence)
		if !rendering && !motion && !page {
			continue
		}
		if runtimeObserved && runtimeStatePattern.MatchString(sentence) && !rendering && !motion {
			// "The service is running" is exactly what a live runtime
			// observation proves, even without an HTTP/browser assertion.
			continue
		}
		requirement := "behavioral verification"
		action := "run a live or browser check that exercises the reported behavior, or qualify the claim as unverified"
		detail := "the answer claims observed behavior (%q), but no behavioral check followed the last change; " +
			"a static build, static analysis, or test-only check cannot establish runtime behavior"
		if motion {
			// A browser text check is behavioral evidence for what the page
			// rendered, not for how it moved. Never promote one into the other.
			requirement = "behavioral observation of the claimed motion or performance"
			action = "measure the animation, scroll, or performance behavior, or state the claim as unverified"
			detail = "the answer claims motion or performance (%q) that no current verification class measures; " +
				"a rendered-text check or a static assertion cannot establish it"
		} else if behavioralVerificationAfter(evidence, evidence.lastPotentialMutation) {
			// The rendering claim is exactly what the behavioral check
			// observed.
			continue
		}
		claim := boundedClaim(sentence)
		if seen[claim] {
			continue
		}
		seen[claim] = true
		issue := contract.GroundingIssue{
			Code:        "unsupported_behavioral_claim",
			Detail:      fmt.Sprintf(detail, claim),
			Claim:       claim,
			Category:    "verification",
			Requirement: requirement,
			Action:      action,
		}
		assessment.Issues = append(assessment.Issues, issue)
		assessment.Reasons = append(assessment.Reasons, issue.Detail)
		if len(assessment.Issues) >= MaxRepairInstructions {
			break
		}
	}
	if len(assessment.Issues) > 0 {
		assessment.Verdict = contract.GroundingReview
	}
	return assessment
}

// behavioralVerificationAfter reports whether a live/browser check followed
// the last potential change.
func behavioralVerificationAfter(evidence completionEvidence, after int) bool {
	for _, record := range evidence.verificationsAfter(after) {
		if record.evidence != nil && record.evidence.Class.Behavioral() {
			return true
		}
	}
	return false
}
