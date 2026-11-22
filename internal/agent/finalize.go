package agent

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// AnswerAudit is the machine-readable record of the final answer gate.
//
// It is audit data, never authority: completion still evaluates the finalized
// text and the durable observations. The counters make the response
// transaction observable so a benchmark can compare models without inspecting
// their prose.
type AnswerAudit struct {
	// Finalized reports that the gate ran for an accepted answer.
	Finalized bool `json:"finalized"`
	// Qualified reports that at least one claim was removed or bounded.
	Qualified bool `json:"qualified,omitempty"`
	// Fallback reports that the draft could not be repaired sentence by
	// sentence, so only the limitation report was published.
	Fallback bool `json:"fallback,omitempty"`
	// RemovedSentences is the number of draft sentences withheld.
	RemovedSentences int `json:"removed_sentences,omitempty"`
	// DraftUnsupportedClaims is the number of audit issues in the model's
	// draft. It is the repair workload, not the published answer's defect
	// count.
	DraftUnsupportedClaims int `json:"draft_unsupported_claims,omitempty"`
	// UnsupportedClaims is the number of rejected factual claims that remain
	// in the committed text. A successful finalization leaves this at zero; a
	// non-zero value is a genuine final-answer defect.
	UnsupportedClaims int `json:"unsupported_claims"`
	// UnresolvedFacets is the number of requested deliverables the committed
	// answer explicitly leaves unverified. It is a completion gap, not a
	// false factual claim, so it is reported separately.
	UnresolvedFacets int `json:"unresolved_facets,omitempty"`
	// EvidenceGap is the control plane's frontier gap at finalization.
	EvidenceGap string `json:"evidence_gap,omitempty"`
	// Issues are the original claim-level audit findings.
	Issues []contract.GroundingIssue `json:"issues,omitempty"`
}

// FinalizedAnswer is the only answer text a host may commit for one run.
type FinalizedAnswer struct {
	// Text is the sanitized answer after unsupported claims were removed or
	// bounded. It may be a limitation report when every draft sentence failed.
	Text string
	// Audit records what the gate changed.
	Audit AnswerAudit
}

// FinalizeAnswer is the last answer-level gate before publication.
//
// It is deliberately not a second investigation and not an LLM judge. It uses
// the same deterministic grounding and provenance audits completion uses,
// removes the sentences the audit rejected, and appends a bounded limitation
// report for every unresolved claim. If the draft cannot be made auditable,
// the limitation report alone is returned: a user-visible answer never carries
// a claim the runtime's own evidence audit rejected.
//
// The function is idempotent for an already auditable answer. It cannot
// authorize an action, add a capability, or mark a task complete.
func FinalizeAnswer(intent contract.IntentContract, turns []Turn, answer string, provenance EvidenceProvenance, evidenceGap string) FinalizedAnswer {
	answer = SanitizeAnswerText(answer)
	evidenceGap = strings.TrimSpace(evidenceGap)
	if strings.TrimSpace(answer) == "" {
		return FinalizedAnswer{
			Text: answer,
			Audit: AnswerAudit{
				Finalized:   true,
				EvidenceGap: evidenceGap,
			},
		}
	}

	issues := auditAnswerClaims(intent, turns, answer, provenance)
	audit := AnswerAudit{
		Finalized:              true,
		DraftUnsupportedClaims: len(issues),
		EvidenceGap:            evidenceGap,
		Issues:                 append([]contract.GroundingIssue(nil), issues...),
	}
	if len(issues) == 0 && evidenceGap == "" {
		return FinalizedAnswer{Text: answer, Audit: audit}
	}

	// Removing one claim can reveal another sentence the same audit rejects
	// (a compound answer often has more than one unsupported assertion). Work
	// through a bounded number of passes rather than discarding the supported
	// remainder on the first stubborn sentence.
	cleaned := answer
	removed := 0
	allIssues := append([]contract.GroundingIssue(nil), issues...)
	for range 3 {
		passIssues := auditAnswerClaims(intent, turns, cleaned, provenance)
		if len(passIssues) == 0 {
			break
		}
		allIssues = appendUniqueIssues(allIssues, passIssues...)
		next, count := removeRejectedClaims(cleaned, passIssues)
		if count == 0 || next == cleaned {
			break
		}
		cleaned = next
		removed += count
	}
	cleaned = joinAnswerParts(cleaned, limitationSection(allIssues, evidenceGap))
	audit.RemovedSentences = removed
	audit.Qualified = removed > 0 || len(allIssues) > 0 || evidenceGap != ""
	if recheck := AssessGrounding(intent, turns, cleaned); !recheck.Pass() {
		// A sentence-level repair can miss a claim that spans formatting or a
		// model-authored explanation. Falling back to runtime-authored
		// limitation text is the only way to guarantee the committed answer
		// contains no claim the audit rejected.
		cleaned = limitationReport(allIssues, evidenceGap)
		audit.Fallback = true
	}
	remaining := auditAnswerClaims(intent, turns, cleaned, provenance)
	for _, issue := range remaining {
		if isFacetIssue(issue) {
			audit.UnresolvedFacets++
			continue
		}
		audit.UnsupportedClaims++
	}
	return FinalizedAnswer{Text: cleaned, Audit: audit}
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
	return issues
}

// appendUniqueIssues keeps the audit bounded while preserving the claim,
// category, and location that make a repair actionable.
func appendUniqueIssues(existing []contract.GroundingIssue, incoming ...contract.GroundingIssue) []contract.GroundingIssue {
	for _, issue := range incoming {
		duplicate := false
		for _, current := range existing {
			if current.Code == issue.Code && current.Claim == issue.Claim && current.Location == issue.Location {
				duplicate = true
				break
			}
		}
		if !duplicate {
			existing = append(existing, issue)
		}
	}
	return existing
}

// removeRejectedClaims withholds the sentence carrying each rejected claim.
//
// The audit's Location is the exact sentence it read. When it is empty, the
// claim token is located and its sentence bounds are used. Code fences are
// left untouched: a rejected prose claim must not delete an example the user
// needs to reproduce the finding.
func removeRejectedClaims(answer string, issues []contract.GroundingIssue) (string, int) {
	text := answer
	removed := 0
	for _, issue := range issues {
		location := strings.TrimSpace(issue.Location)
		if location == "" {
			location = claimLocation(text, issue.Claim)
		}
		if location == "" {
			continue
		}
		index := strings.Index(text, location)
		if index < 0 || insideFence(text, index) {
			continue
		}
		text = text[:index] + text[index+len(location):]
		removed++
	}
	text = strings.TrimSpace(collapseBlankLines(text))
	if strings.Trim(text, " \t\r\n.!?;:,-") == "" {
		// Removing the only substantive sentence can leave punctuation. It is
		// not an answer, and the limitation report will carry the result.
		text = ""
	}
	return text, removed
}

// limitationSection renders one bounded line per unresolved claim. It never
// repeats the rejected assertion as a fact: the subject is named, and the
// sentence states only that the run did not establish it.
func limitationSection(issues []contract.GroundingIssue, evidenceGap string) string {
	if len(issues) == 0 && evidenceGap == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Evidence limitations:\n")
	seen := map[string]bool{}
	for _, issue := range issues {
		subject := limitationSubject(issue)
		if subject == "" || seen[strings.ToLower(subject)] {
			continue
		}
		seen[strings.ToLower(subject)] = true
		required := strings.TrimSpace(firstNonEmpty(issue.Requirement, issue.Action))
		fmt.Fprintf(&b, "- %s: the run did not establish this claim", boundRepair(subject, 120))
		if required != "" {
			fmt.Fprintf(&b, " (required: %s)", boundRepair(required, 120))
		}
		b.WriteString(".\n")
	}
	if evidenceGap != "" {
		fmt.Fprintf(&b, "- Required evidence frontier: %s.\n", boundRepair(evidenceGap, 240))
	}
	return strings.TrimSpace(b.String())
}

// limitationSubject names the rejected claim without repeating the global
// scope that made it unsupported. "database anywhere in the repository" is the
// same subject as "database"; listing the scope in every bullet is noise.
func limitationSubject(issue contract.GroundingIssue) string {
	subject := strings.TrimSpace(firstNonEmpty(issue.Claim, issue.Reference, issue.Code, "unresolved claim"))
	lower := strings.ToLower(subject)
	for _, suffix := range []string{
		" anywhere in the repository", " anywhere in the repo",
		" in the repository", " in the repo", " in this repository",
		" in the project", " in the codebase", " in the workspace",
	} {
		if strings.HasSuffix(lower, suffix) {
			subject = strings.TrimSpace(subject[:len(subject)-len(suffix)])
			break
		}
	}
	switch strings.ToLower(subject) {
	case "test":
		// The requested-facet audit looks for the deliverable's ordinary noun.
		// "test: not established" is the same gap as "test files: not
		// established", but only the latter answers the user's facet.
		subject = "test files"
	case "file":
		subject = "files"
	}
	return neutralizeAbsencePhrase(subject)
}

// neutralizeAbsencePhrase removes the assertion grammar from a claim subject.
// A limitation says the subject was not established; it must not repeat "no
// eslint config" and thereby publish the very absence claim it is qualifying.
func neutralizeAbsencePhrase(subject string) string {
	subject = strings.TrimSpace(subject)
	lower := strings.ToLower(subject)
	for _, prefix := range []string{"there is no ", "there are no ", "no "} {
		if strings.HasPrefix(lower, prefix) {
			subject = strings.TrimSpace(subject[len(prefix):])
			break
		}
	}
	replacer := strings.NewReplacer(
		" and no ", " and ",
		" or no ", " or ",
		" with no ", " with ",
		" without ", " with ",
	)
	return strings.TrimSpace(replacer.Replace(subject))
}

// limitationReport is the final fallback when the draft cannot be repaired
// claim by claim. It contains no sentence from the model draft.
func limitationReport(issues []contract.GroundingIssue, evidenceGap string) string {
	report := "The available evidence did not support every claim in the draft answer.\n\n" +
		limitationSection(issues, evidenceGap)
	return strings.TrimSpace(report)
}

// joinAnswerParts joins the retained answer and the limitation report without
// creating a leading or trailing blank block.
func joinAnswerParts(answer, limitations string) string {
	answer = strings.TrimSpace(answer)
	limitations = strings.TrimSpace(limitations)
	switch {
	case answer == "":
		return limitations
	case limitations == "":
		return answer
	default:
		return answer + "\n\n" + limitations
	}
}

// collapseBlankLines bounds runs of empty lines left by removed sentences.
func collapseBlankLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// insideFence reports whether an offset lies inside a fenced code block.
func insideFence(text string, offset int) bool {
	if offset < 0 || offset > len(text) {
		return false
	}
	fences := 0
	for index := 0; index < offset; index++ {
		if text[index] != '`' {
			continue
		}
		if index+2 < offset && text[index] == '`' && text[index+1] == '`' && text[index+2] == '`' {
			fences++
			index += 2
		}
	}
	return fences%2 == 1
}
