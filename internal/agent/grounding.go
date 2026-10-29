package agent

import (
	"regexp"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Intent grounding is the runtime's semantic admission and review layer.
//
// The model still interprets the request; this file checks the interpretation
// against the deterministic contract built from the user's own words. It never
// invents an objective, never authorizes execution, and never replaces the
// security boundary. A REJECT is a semantic refusal; a REVIEW is a completion
// boundary, not a failure.

var (
	// A global negative conclusion about the repository. The subject is the
	// thing the answer claims is absent.
	negativeRepositoryPattern = regexp.MustCompile(
		`(?i)\b(?:the\s+)?(?:repository|repo|project|codebase|workspace|directory|folder)\b[^.\n]{0,80}\b(?:contains?|has|have|includes?)\s+no\s+([a-z0-9][a-z0-9 _./-]{0,40})`)
	negativeThereIsPattern = regexp.MustCompile(
		`(?i)\bthere\s+(?:is|are)\s+no\s+([a-z0-9][a-z0-9 _./-]{0,40})`)
	negativeNoPattern = regexp.MustCompile(
		`(?i)\bno\s+(README(?:\.md)?|documentation|docs|tests?|config(?:uration)?|source\s+files?|files?)\b`)
	negativeExistPattern = regexp.MustCompile(
		`(?i)\b([a-z0-9][a-z0-9_./-]{1,60})\s+(?:does\s+not|doesn't|do\s+not|don't)\s+(?:exist|contain|include)\b`)
	negativeAbsentPattern = regexp.MustCompile(
		`(?i)\b([a-z0-9][a-z0-9_./-]{1,60})\s+(?:is|are)\s+(?:not\s+present|absent)\b`)
	negativeMissingPattern = regexp.MustCompile(
		`(?i)\b([a-z0-9][a-z0-9_./-]{1,60})\s+(?:is|was|are|were)\s+(?:missing|not\s+found)\b`)
	globalScopePattern = regexp.MustCompile(
		`(?i)\b(?:repository|repo|project|codebase|workspace)\b`)
	insufficientSearchPattern = regexp.MustCompile(
		`(?i)\b(?:i\s+)?(?:could\s+not|couldn't|cannot|can't|was\s+unable\s+to|unable\s+to)\s+(?:find|locate|read|access|see)\b`)
	performedActionPattern = regexp.MustCompile(
		`(?i)\b(?:i|we)\s+(?:have\s+)?(?:already\s+)?(?:inspected|checked|reviewed|read|examined|scanned|analy[sz]ed|searched|looked\s+at|opened)\b`)
	restatedObjectivePattern = regexp.MustCompile(
		`(?i)\b(?:the\s+task\s+is|the\s+objective\s+is|you\s+asked\s+me\s+to|you\s+want\s+me\s+to|i\s+was\s+asked\s+to)\b[^.\n]{0,100}`)
	scopeNarrowPattern = regexp.MustCompile(
		`(?i)\b(?:only|just)\b[^.\n]{0,40}\b(?:this|the|current)\s+(?:repository|repo|project|workspace)\b`)
)

// ProposalForCall derives the model's concrete interpretation from trusted
// tool metadata and the call arguments. It is a proposal, never authority.
func ProposalForCall(tool Tool, args map[string]any) contract.ActionProposal {
	proposal := contract.ActionProposal{
		Action:   contract.GroundingRead,
		Targets:  proposalTargets(args),
		Mutates:  invocationMutates(tool, args),
		Executes: invocationExecutes(tool, args),
		External: HasActionClass(tool, ActionExternal),
		Delegate: HasActionClass(tool, ActionDelegate),
	}
	switch {
	case proposal.Mutates:
		proposal.Action = contract.GroundingMutate
	case proposal.Executes:
		proposal.Action = contract.GroundingExecute
	}
	return proposal
}

// ValidateCall validates one tool call against the request's grounding.
func ValidateCall(grounding *contract.IntentGrounding, call ai.ToolCall, tool Tool) contract.GroundingAssessment {
	return ValidateProposal(grounding, ProposalForCall(tool, call.Arguments))
}

// ValidateProposal decides whether a model proposal may become execution.
//
// The rule is intentionally narrow. A mutation that the user did not request
// is rejected outright, because it changes the repository under an objective
// the user never stated. A read or execution under an ambiguous request is
// allowed as bounded observation: it may gather evidence, but completion still
// cannot claim the request asked for it.
func ValidateProposal(grounding *contract.IntentGrounding, proposal contract.ActionProposal) contract.GroundingAssessment {
	assessment := contract.GroundingAssessment{Verdict: contract.GroundingPass}
	if grounding == nil {
		return assessment
	}
	if proposal.Mutates {
		if !grounding.MutationIntent || !grounding.HasAction(contract.GroundingMutate) {
			return rejectGrounding("action_invented",
				"the request does not ask for a repository change; a mutating action is not grounded")
		}
	}
	if grounding.Action == contract.GroundingUnknown && (proposal.Executes || proposal.External) {
		assessment.Verdict = contract.GroundingReview
		assessment.Issues = append(assessment.Issues, contract.GroundingIssue{
			Code:   "action_ungrounded",
			Detail: "the request names no actionable verb; the action is bounded observation only",
		})
		assessment.Reasons = append(assessment.Reasons,
			"the request names no actionable verb; the action is bounded observation only")
	}
	return assessment
}

// AssessGrounding validates the final answer and the run's observed evidence
// against the request's grounding.
//
// It is deterministic and conservative. It can only downgrade a completion:
// entity loss, an invented action narrative, and an unsupported negative
// conclusion are review boundaries. It never authorizes an action and never
// replaces target or verification evidence.
func AssessGrounding(intent contract.IntentContract, turns []Turn, answer string) contract.GroundingAssessment {
	assessment := contract.GroundingAssessment{Verdict: contract.GroundingPass}
	grounding := intent.Grounding
	if grounding == nil {
		return assessment
	}
	add := func(code, detail string) {
		for _, issue := range assessment.Issues {
			if issue.Code == code && issue.Detail == detail {
				return
			}
		}
		assessment.Issues = append(assessment.Issues, contract.GroundingIssue{Code: code, Detail: detail})
		assessment.Reasons = append(assessment.Reasons, detail)
		assessment.Verdict = contract.GroundingReview
	}

	evidence := scanCompletionEvidence(turns, intent.Kind == contract.ObjectiveMutation)
	if evidence.mutated && !grounding.MutationIntent {
		return rejectGrounding("action_invented",
			"a repository change was observed but the request did not ask for one")
	}

	answer = strings.TrimSpace(answer)
	answerLower := strings.ToLower(answer)
	if answer == "" {
		return assessment
	}

	// Ambiguity preservation: a reference the request did not resolve cannot
	// be completed by choosing entities or approaches on the user's behalf.
	for _, ambiguity := range grounding.Ambiguities {
		switch ambiguity.Kind {
		case "unresolved_reference":
			add("scope_unresolved", "the request refers to entities it does not name; the runtime cannot prove which ones")
		case "unresolved_alternative":
			add("alternative_unresolved", "the request names competing approaches without choosing; the answer cannot choose for the user")
		}
	}

	// Action fidelity: an actionless request cannot be narrated as a task the
	// model was asked to perform. Conditional relevance ("use them if
	// relevant") is an explicit permission to observe, so it is exempt.
	if grounding.Action == contract.GroundingUnknown && !grounding.Scope.Conditional {
		if performedActionPattern.MatchString(answer) {
			add("action_narrative_ungrounded",
				"the answer describes performing an action the request did not ask for")
		}
	}
	if match := restatedObjectivePattern.FindString(answer); match != "" {
		for _, action := range actionsInText(match) {
			if !grounding.HasAction(action) {
				add("objective_restated",
					"the answer restates the objective as an action the request does not ground")
				break
			}
		}
	}

	// Entity preservation: a named project is the semantic subject of an
	// informational or investigative request, not decoration. Dropping it is a
	// review boundary. A mutation objective is already bounded by target
	// evidence, so its prose is not forced to repeat every named entity.
	if intent.Kind != contract.ObjectiveMutation {
		for _, entity := range grounding.RequiredEntities() {
			if !answerMentions(answer, entity.Value) {
				add("entity_missing", "the answer does not preserve the named entity "+entity.Value)
			}
		}
	}
	if intent.Kind != contract.ObjectiveMutation &&
		grounding.Scope.Kind == contract.ScopeNamed && !grounding.Scope.Conditional &&
		scopeNarrowPattern.MatchString(answer) {
		add("scope_narrowed", "the answer narrows the request's named scope to the current repository")
	}

	// Evidence integrity: a negative conclusion must be earned by coverage
	// that can actually establish absence.
	if claims := negativeClaims(answer); len(claims) > 0 {
		coverage := collectGroundingCoverage(turns)
		for _, claim := range claims {
			if !coverage.supports(claim) {
				add("unsupported_negative_conclusion",
					"the answer concludes "+claim.Subject+" is absent without evidence covering the claim")
				break
			}
		}
	}
	if insufficientSearchPattern.MatchString(answerLower) &&
		(intent.Kind != contract.ObjectiveMutation || grounding.HasAction(contract.GroundingInvestigate)) {
		add("unverified_negative_result",
			"the answer reports an inability to find evidence as the result")
	}
	return assessment
}

// rejectGrounding builds a REJECT assessment.
func rejectGrounding(code, detail string) contract.GroundingAssessment {
	return contract.GroundingAssessment{
		Verdict: contract.GroundingReject,
		Reasons: []string{detail},
		Issues:  []contract.GroundingIssue{{Code: code, Detail: detail}},
	}
}

// invocationMutates reports a per-call repository change. A tool that reports
// its per-call capability is trusted over its declared classes, because a
// shell can run a check or rewrite a file.
func invocationMutates(tool Tool, args map[string]any) bool {
	if reporter, ok := tool.(CapabilityReporter); ok {
		return reporter.Capability(args) == TierWrite
	}
	return HasActionClass(tool, ActionMutate)
}

// invocationExecutes reports a per-call process start.
func invocationExecutes(tool Tool, args map[string]any) bool {
	if reporter, ok := tool.(CapabilityReporter); ok {
		return reporter.Capability(args) == TierExec
	}
	return HasActionClass(tool, ActionExecute)
}

// proposalTargets returns the repository coordinates a call names.
func proposalTargets(args map[string]any) []string {
	var out []string
	for _, key := range []string{"path", "file", "file_path", "target"} {
		value, _ := args[key].(string)
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

// negativeClaim is one absence conclusion an answer asserts.
type negativeClaim struct {
	// Subject is the thing the answer says is absent.
	Subject string
	// Global reports a repository-wide claim. A local listing cannot establish
	// it; a complete recursive file scan can, for a file subject.
	Global bool
}

// negativeClaims returns the absence conclusions an answer asserts. An honest
// "I could not find X" is handled separately: it is an epistemic report, not a
// claim that X does not exist.
func negativeClaims(answer string) []negativeClaim {
	var out []negativeClaim
	add := func(value string, global bool) {
		value = cleanClaimSubject(value)
		if value == "" {
			return
		}
		for _, existing := range out {
			if existing.Subject == value && existing.Global == global {
				return
			}
		}
		out = append(out, negativeClaim{Subject: value, Global: global})
	}
	addMatches := func(pattern *regexp.Regexp, repositoryWide bool) {
		for _, match := range pattern.FindAllStringSubmatchIndex(answer, -1) {
			if len(match) < 4 || match[2] < 0 || match[3] < 0 {
				continue
			}
			value := answer[match[2]:match[3]]
			global := repositoryWide || claimContextIsGlobal(answer, match[0], match[1])
			add(value, global)
		}
	}
	addMatches(negativeRepositoryPattern, true)
	addMatches(negativeThereIsPattern, false)
	addMatches(negativeNoPattern, false)
	addMatches(negativeExistPattern, false)
	addMatches(negativeAbsentPattern, false)
	addMatches(negativeMissingPattern, false)
	return out
}

// claimContextIsGlobal reports whether the sentence around a claim names the
// repository as a whole. "README.md does not exist in this repository" is not
// the same claim as "README.md is absent from docs/".
func claimContextIsGlobal(answer string, start, end int) bool {
	left := start - 80
	if left < 0 {
		left = 0
	}
	right := end + 80
	if right > len(answer) {
		right = len(answer)
	}
	return globalScopePattern.MatchString(answer[left:right])
}

// cleanClaimSubject trims a captured negative subject to the thing named,
// without swallowing a qualifier into the claim.
func cleanClaimSubject(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.Trim(value, " .,;:!?")
	for _, suffix := range []string{
		" yet", " currently", " at all", " here",
		" in this repository", " in this project", " at the moment", " right now",
	} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSpace(value[:len(value)-len(suffix)])
		}
	}
	return strings.Trim(value, " .,;:!?")
}

// groundingCoverage records the observations that can prove absence.
type groundingCoverage struct {
	// globPatterns are successful, complete recursive glob patterns.
	globPatterns []string
	// rootListing is a complete listing of the repository root.
	rootListing bool
}

// supports reports whether the coverage can establish absence of claim.
//
// File absence can be proved by a complete recursive glob that covers every
// file or the claimed name. A concept ("documentation", "tests") cannot be
// proved absent by a file search: documentation may live in comments, and a
// test may have another name. Those remain review.
func (c groundingCoverage) supports(claim negativeClaim) bool {
	claim.Subject = strings.ToLower(strings.TrimSpace(claim.Subject))
	if claim.Subject == "" {
		return false
	}
	if !claim.Global && c.rootListing && isRootFileName(claim.Subject) {
		return true
	}
	for _, pattern := range c.globPatterns {
		pattern = strings.ToLower(pattern)
		if pattern == "**" || pattern == "**/*" || pattern == "*" {
			return isFileClaim(claim.Subject)
		}
		if strings.Contains(pattern, "**") && strings.Contains(pattern, claim.Subject) {
			return isFileClaim(claim.Subject)
		}
	}
	return false
}

// isFileClaim reports whether claim names a concrete file rather than a
// semantic category. A complete file scan proves a file is absent; it does not
// prove that documentation, tests, or configuration do not exist in another
// form.
func isFileClaim(claim string) bool {
	claim = strings.TrimSpace(strings.ToLower(claim))
	if claim == "" {
		return false
	}
	if isRootFileName(claim) {
		return true
	}
	// A dotted name (README.md, go.mod, config.yaml) is a file coordinate.
	dot := strings.LastIndex(claim, ".")
	return dot > 0 && dot < len(claim)-1
}

// isRootFileName reports whether claim names one file at the root rather than
// a concept.
func isRootFileName(claim string) bool {
	switch claim {
	case "readme", "readme.md", "license", "changelog", "dockerfile",
		"makefile", "go.mod", "package.json", ".gitignore":
		return true
	}
	return false
}

// collectGroundingCoverage folds successful observations into coverage.
func collectGroundingCoverage(turns []Turn) groundingCoverage {
	var coverage groundingCoverage
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			text := strings.ToLower(outcome.Result.Text)
			complete := !strings.Contains(text, "truncated") &&
				!strings.Contains(text, "showing ") &&
				!strings.Contains(text, "limit reached") &&
				!strings.Contains(text, "more results")
			switch outcome.ToolName {
			case "ls":
				path := proposalArgString(outcome.Args, "path")
				if (path == "" || path == "." || path == "./") && complete {
					coverage.rootListing = true
				}
			case "glob":
				pattern := proposalArgString(outcome.Args, "pattern")
				path := proposalArgString(outcome.Args, "path")
				if complete && (path == "" || path == "." || path == "./") &&
					strings.Contains(pattern, "**") {
					coverage.globPatterns = append(coverage.globPatterns, pattern)
				}
			}
		}
	}
	return coverage
}

// proposalArgString reads a string argument without panicking on another type.
func proposalArgString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

// actionsInText maps the action words in a sentence back to grounding actions.
//
// The vocabulary is deliberately smaller than the router's: it only classifies
// an answer's restatement of the objective, never authority. Keeping it local
// avoids importing the workspace layer (which imports agent).
func actionsInText(text string) []contract.GroundingAction {
	var out []contract.GroundingAction
	add := func(action contract.GroundingAction) {
		for _, existing := range out {
			if existing == action {
				return
			}
		}
		out = append(out, action)
	}
	for _, token := range strings.Fields(normalizeGroundingText(text)) {
		if action, ok := groundingActionWords[token]; ok {
			add(action)
		}
	}
	return out
}

// groundingActionWords is the narrow restatement vocabulary.
var groundingActionWords = map[string]contract.GroundingAction{
	"fix": contract.GroundingMutate, "change": contract.GroundingMutate,
	"modify": contract.GroundingMutate, "update": contract.GroundingMutate,
	"create": contract.GroundingMutate, "write": contract.GroundingMutate,
	"add": contract.GroundingMutate, "remove": contract.GroundingMutate,
	"delete": contract.GroundingMutate, "rename": contract.GroundingMutate,
	"refactor": contract.GroundingMutate, "implement": contract.GroundingMutate,
	"clean": contract.GroundingMutate, "format": contract.GroundingMutate,

	"read": contract.GroundingRead, "inspect": contract.GroundingRead,
	"check": contract.GroundingRead, "review": contract.GroundingRead,
	"explain": contract.GroundingRead, "describe": contract.GroundingRead,
	"show": contract.GroundingRead, "list": contract.GroundingRead,

	"investigate": contract.GroundingInvestigate, "find": contract.GroundingInvestigate,
	"diagnose": contract.GroundingInvestigate, "debug": contract.GroundingInvestigate,
	"reproduce": contract.GroundingInvestigate, "probe": contract.GroundingInvestigate,

	"run": contract.GroundingExecute, "execute": contract.GroundingExecute,
	"build": contract.GroundingExecute, "test": contract.GroundingExecute,
}

// answerMentions reports whether an answer preserves a named entity. Exact
// wording is preferred; a multi-word entity may be preserved by its
// distinctive words so "API issue" is not lost to grammar.
func answerMentions(answer, entity string) bool {
	normalizedAnswer := normalizeGroundingText(answer)
	normalizedEntity := normalizeGroundingText(entity)
	if normalizedAnswer == "" || normalizedEntity == "" {
		return false
	}
	if strings.Contains(normalizedAnswer, normalizedEntity) {
		return true
	}
	words := strings.Fields(normalizedEntity)
	if len(words) < 2 {
		return false
	}
	meaningful := 0
	matched := 0
	for _, word := range words {
		if len(word) < 3 || groundingStopWords[word] {
			continue
		}
		meaningful++
		if strings.Contains(normalizedAnswer, word) {
			matched++
		}
	}
	return meaningful > 0 && matched == meaningful
}

// groundingStopWords are grammar words that do not carry entity identity.
var groundingStopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"of": true, "for": true, "to": true, "in": true, "on": true,
	"with": true, "this": true, "that": true, "these": true, "those": true,
	"is": true, "are": true, "be": true, "it": true, "its": true,
}

// normalizeGroundingText lowercases and collapses non-alphanumeric runs so
// punctuation cannot defeat an entity match.
func normalizeGroundingText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := false
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
