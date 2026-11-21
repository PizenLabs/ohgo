package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// Required-facet evaluation is the answer-side completion contract.
//
// A fluent answer can satisfy every run-shape rule and still omit a field the
// user explicitly asked for, or admit that the field is unverified. The
// previous completion policy accepted that as COMPLETED because it judged the
// answer's form, not its deliverables. This file checks the deterministic
// facets the user named ("report the callers, the tests, and affected
// packages") against what the answer actually established.
//
// The checks are narrow on purpose. A passing mention of a topic is not a
// request for its inventory; workspace.BuildIntentContract only records a
// facet when the user's own clause asks for it. An uncertainty is material
// only when it qualifies the requested facet. Nothing here authorizes an
// action or invents evidence.

var facetTermPatterns = map[contract.Facet]*regexp.Regexp{
	contract.FacetCallers:    regexp.MustCompile(`(?i)\b(?:callers?|called\s+by|inbound\s+call\s+graph)\b`),
	contract.FacetReferences: regexp.MustCompile(`(?i)\b(?:references?|usages?|reference\s+sites?)\b`),
	contract.FacetCallSites:  regexp.MustCompile(`(?i)\b(?:call|invocation)\s+sites?\b`),
	contract.FacetImporters:  regexp.MustCompile(`(?i)\b(?:importers?|imported\s+by)\b`),
	contract.FacetDependencies: regexp.MustCompile(
		`(?i)\b(?:dependencies|depends?\s+on|dependents?)\b`),
	contract.FacetTests: regexp.MustCompile(
		`(?i)\b(?:tests|test\s+files?|test\s+suite|test\s+cases?|test\s+functions?)\b`),
	contract.FacetTestTooling: regexp.MustCompile(
		`(?i)\b(?:test\s+(?:script|command|tooling|runner|framework)|npm\s+test|go\s+test|make\s+test|pnpm\s+test|yarn\s+test)\b`),
	contract.FacetPackages: regexp.MustCompile(`(?i)\b(?:packages?|modules?)\b`),
	contract.FacetEntrypoints: regexp.MustCompile(
		`(?i)\b(?:entry[ -]?points?|main\s+(?:function|package)|bootstrap)\b`),
	contract.FacetArchitecture: regexp.MustCompile(
		`(?i)\b(?:architecture|major\s+components?|components?|structure)\b`),
	contract.FacetImpact: regexp.MustCompile(
		`(?i)\b(?:impact|affected|downstream|ripple|what\s+else\s+(?:breaks|changes))\b`),
}

// facetUncertaintyPattern marks a sentence that explicitly leaves its subject
// unresolved. "No callers were found" is a result; "callers remain unverified"
// is a gap. Only the second is a completion veto.
var facetUncertaintyPattern = regexp.MustCompile(
	`(?i)\b(?:unverified|not\s+verified|cannot\s+verify|can't\s+verify|could\s+not\s+verify|couldn't\s+verify|` +
		`not\s+established|not\s+confirmed|not\s+determined|undetermined|unknown|unclear|no\s+data|` +
		`not\s+inspected|not\s+checked|not\s+probed|not\s+queried|not\s+evaluated|` +
		`insufficient\s+evidence|unable\s+to\s+(?:verify|determine|establish|inspect)|` +
		`remain(?:s|ed)?\s+(?:unverified|unknown|unclear|open)|` +
		`only\s+\d+\s+of\s+\d+|not\s+individually|not\s+fully\s+(?:expanded|listed|reported|identified|verified)|` +
		`not\s+exhaustive|incomplete|not\s+all|` +
		`rest\s+(?:were|was)\s+not|(?:were|was)\s+not\s+retrieved)\b`)

// facetAbsencePattern marks a sentence that asserts the requested relationship
// does not exist. It is only used to detect a contradiction with a positive
// observation; a supported absence remains a valid answer.
var facetAbsencePattern = regexp.MustCompile(
	`(?i)\b(?:no|zero|0)\s+(?:direct\s+|indirect\s+|static\s+)?(?:callers?|references?|call\s+sites?|importers?|tests?|dependencies)\b|` +
		`\b(?:callers?|references?|call\s+sites?|importers?|tests?|dependencies)\s*[:=]\s*0\b`)

// facetCountPattern extracts a positive relationship count from tool output.
var facetCountPattern = regexp.MustCompile(
	`(?i)\b(?:callers?|references?|call\s+sites?|importers?|tests?|dependencies)\s*[:=]\s*(\d[\d,]*)|` +
		`\b(\d[\d,]*)\s+(?:direct\s+|indirect\s+|static\s+)?(?:callers?|references?|call\s+sites?|importers?|tests?|dependencies)\b`)

// AssessRequiredFacets checks the answer against the facets the request named.
//
// It returns a REVIEW when a requested facet is absent from the answer, left
// explicitly unverified, or flatly contradicted by a positive observation. It
// returns PASS for an empty facet set, so an ordinary overview is unaffected.
func AssessRequiredFacets(intent contract.IntentContract, turns []Turn, answer string) contract.GroundingAssessment {
	assessment := contract.GroundingAssessment{Verdict: contract.GroundingPass}
	facets := intent.RequiredFacets
	if intent.Kind != contract.ObjectiveInformational || len(facets) == 0 {
		return assessment
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		// Empty answers are already a completion veto; naming each missing
		// facet would crowd the review reason with duplicates.
		return assessment
	}
	addIssue := func(issue contract.GroundingIssue) {
		for _, existing := range assessment.Issues {
			if existing.Code == issue.Code && existing.Detail == issue.Detail {
				return
			}
		}
		assessment.Issues = append(assessment.Issues, issue)
		assessment.Reasons = append(assessment.Reasons, issue.Detail)
		assessment.Verdict = contract.GroundingReview
	}
	add := func(code, detail string) {
		issue := contract.GroundingIssue{Code: code, Detail: detail}
		switch code {
		case "required_facet_missing", "required_facet_unverified",
			"required_facet_incomplete_list", "required_facet_contradicted":
			issue.Category = "facet"
			issue.Requirement = "the requested facet's evidence"
			issue.Action = "report the facet from observed evidence or state it as unverified"
		}
		addIssue(issue)
	}
	for _, facet := range facets {
		pattern := facetTermPatterns[facet.Facet]
		if pattern == nil {
			continue
		}
		mentions := facetSentences(pattern, answer)
		label := facet.Facet.Label()
		if facet.Subject != "" {
			label += " of " + facet.Subject
		}
		if len(mentions) == 0 {
			add("required_facet_missing",
				fmt.Sprintf("the request asked for %s, but the answer does not report it", label))
			continue
		}
		if facetMentionsIncomplete(mentions) {
			add("required_facet_unverified",
				fmt.Sprintf("the request asked for %s, but the answer explicitly leaves it unverified or incomplete; establish it or the task is not complete", label))
			continue
		}
		if issue := facetListOmission(facet, answer, turns); issue != "" {
			add("required_facet_incomplete_list", issue)
			continue
		}
		if contradiction := facetContradiction(facet, answer, turns); contradiction != "" {
			add("required_facet_contradicted", contradiction)
		}
	}
	return assessment
}

// facetSentences returns the answer sentences that mention a facet.
func facetSentences(pattern *regexp.Regexp, answer string) []string {
	var out []string
	for _, raw := range claimSentenceBoundary.Split(strings.TrimSpace(answer), -1) {
		sentence := strings.TrimSpace(raw)
		if sentence == "" || !pattern.MatchString(sentence) {
			continue
		}
		out = append(out, sentence)
	}
	return out
}

// facetMentionsIncomplete reports whether any sentence that mentions the facet
// also marks it as unverified or incomplete. A heading with a complete list and
// a separate caveat about another facet is not rejected: the caveat sentence
// does not mention this facet. A partial list that admits it is partial is a
// material gap for a deliverable the user asked for.
func facetMentionsIncomplete(sentences []string) bool {
	for _, sentence := range sentences {
		if facetUncertaintyPattern.MatchString(sentence) {
			return true
		}
	}
	return false
}

// facetListOmission reports an answer that names fewer observed items than the
// run actually found without reporting the total. This is the difference
// between a bounded summary ("13 callers, including …") and a partial list that
// reads as the complete answer.
func facetListOmission(facet contract.RequiredFacet, answer string, turns []Turn) string {
	switch facet.Facet {
	case contract.FacetCallers, contract.FacetReferences, contract.FacetCallSites, contract.FacetTests:
	default:
		return ""
	}
	observed := observedFacetItems(facet, turns)
	if observed <= 1 {
		return ""
	}
	named := answerFacetItems(facet, answer)
	if named >= observed {
		return ""
	}
	if answerReportsCount(answer, observed) {
		return ""
	}
	label := facet.Facet.Label()
	if facet.Subject != "" {
		label += " of " + facet.Subject
	}
	return fmt.Sprintf(
		"the answer lists %d %s but the run observed %d; report the complete list or state the observed total",
		named, label, observed)
}

// observedFacetItems returns the largest item count a trusted structural
// observation reported for the facet. A subject named by the request must be
// present in the observation, so another symbol's count is never reused.
func observedFacetItems(facet contract.RequiredFacet, turns []Turn) int {
	largest := 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			details, ok := outcome.Result.Details.(map[string]any)
			if !ok {
				continue
			}
			if facet.Subject != "" &&
				!strings.Contains(strings.ToLower(outcome.Result.Text), strings.ToLower(facet.Subject)) &&
				!strings.Contains(strings.ToLower(detailValueString(details, "symbol")), strings.ToLower(facet.Subject)) {
				continue
			}
			count := 0
			switch outcome.ToolName {
			case "refs":
				relation := detailValueString(details, "relation")
				switch facet.Facet {
				case contract.FacetCallers:
					if relation == "callers" {
						count = detailValueInt(details, "total_edges")
					} else {
						count = detailValueInt(details, "total_references")
					}
				case contract.FacetReferences, contract.FacetCallSites:
					count = detailValueInt(details, "total_references")
				}
			case "structure":
				if detailValueString(details, "view") != "impact" {
					continue
				}
				switch facet.Facet {
				case contract.FacetCallers:
					count = detailValueListLen(details, "callers")
				case contract.FacetReferences, contract.FacetCallSites:
					count = detailValueListLen(details, "references")
				case contract.FacetTests:
					count = detailValueListLen(details, "tests")
				}
			default:
				continue
			}
			if count > largest {
				largest = count
			}
		}
	}
	return largest
}

// answerFacetItems counts the distinct coordinates or test names an answer
// presents for a facet.
func answerFacetItems(facet contract.RequiredFacet, answer string) int {
	seen := map[string]bool{}
	for _, match := range answerSitePattern.FindAllStringSubmatch(answer, -1) {
		if len(match) > 2 {
			seen[strings.ToLower(match[1])+":"+match[2]] = true
		}
	}
	if facet.Facet == contract.FacetTests {
		for _, match := range answerTestNamePattern.FindAllString(answer, -1) {
			seen[strings.ToLower(match)] = true
		}
	}
	return len(seen)
}

// answerReportsCount reports whether the answer names the observed total. A
// count plus a bounded example list is a summary, not a silent omission.
func answerReportsCount(answer string, count int) bool {
	pattern := regexp.MustCompile(`\b` + strconv.Itoa(count) + `\b`)
	return pattern.MatchString(answer)
}

var answerSitePattern = regexp.MustCompile(
	`([A-Za-z0-9_./-]+\.(?:go|ts|tsx|js|jsx|mjs|cjs|java|cs|rs|kt|kts|rb|c|cc|cpp|h|hpp)):(\d+)`)

var answerTestNamePattern = regexp.MustCompile(`\bTest[A-Za-z0-9_]+\b`)

// detailValueListLen reads the length of a structured list from tool details.
func detailValueListLen(details map[string]any, key string) int {
	switch value := details[key].(type) {
	case []any:
		return len(value)
	case []map[string]any:
		return len(value)
	case []string:
		return len(value)
	default:
		return 0
	}
}

// facetContradiction reports an absence claim the run's own tool output
// contradicts. It is limited to facets with a countable observation and to
// sentences that assert zero. A subject named by the request must appear in
// the observation, so a count for another symbol is not reused.
func facetContradiction(facet contract.RequiredFacet, answer string, turns []Turn) string {
	pattern := facetTermPatterns[facet.Facet]
	if pattern == nil {
		return ""
	}
	claimedAbsent := false
	for _, sentence := range facetSentences(pattern, answer) {
		if facetAbsencePattern.MatchString(sentence) && !facetUncertaintyPattern.MatchString(sentence) {
			claimedAbsent = true
			break
		}
	}
	if !claimedAbsent {
		return ""
	}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			switch outcome.ToolName {
			case "refs", "graph", "structure", "symbols", "grep":
			default:
				continue
			}
			text := outcome.Result.Text
			if facet.Subject != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(facet.Subject)) {
				continue
			}
			if count := positiveFacetCount(text); count > 0 {
				label := facet.Facet.Label()
				if facet.Subject != "" {
					label += " of " + facet.Subject
				}
				return fmt.Sprintf(
					"the answer says there are no %s, but an observation in this run reports %d; correct the absence claim or cite the observation",
					label, count)
			}
		}
	}
	return ""
}

// positiveFacetCount returns the largest positive count a tool observation
// reports. A zero count is not a positive observation and does not contradict
// an absence claim.
func positiveFacetCount(text string) int {
	largest := 0
	for _, match := range facetCountPattern.FindAllStringSubmatch(text, -1) {
		for _, raw := range match[1:] {
			if raw == "" {
				continue
			}
			value, err := strconv.Atoi(strings.ReplaceAll(raw, ",", ""))
			if err == nil && value > largest {
				largest = value
			}
		}
	}
	return largest
}
