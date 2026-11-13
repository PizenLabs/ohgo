package agent

import (
	"fmt"
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
		`(?i)\bno\s+((?:[a-z0-9_.+#-]+\s+){0,3}?(?:README(?:\.md)?|documentation|docs|tests?|config(?:uration)?|source\s+files?|files?))\b`)
	negativeExistPattern = regexp.MustCompile(
		`(?i)\b([a-z0-9][a-z0-9_./-]{1,60})\s+(?:does\s+not|doesn't|do\s+not|don't)\s+(?:exist|contain|include)\b`)
	negativeAbsentPattern = regexp.MustCompile(
		`(?i)\b([a-z0-9][a-z0-9_./-]{1,60})\s+(?:is|are)\s+(?:not\s+present|absent)\b`)
	negativeMissingPattern = regexp.MustCompile(
		`(?i)\b([a-z0-9][a-z0-9_./-]{1,60})\s+(?:is|was|are|were)\s+(?:missing|not\s+found)\b`)
	// changeReportPattern follows a negated quantity with a change predicate.
	// "No files were modified" reports what the run did to the workspace, not
	// what the repository contains. No file scan can establish it, and it is
	// not the existence claim the negative-conclusion check exists to police.
	changeReportPattern = regexp.MustCompile(
		`(?i)^\s*(?:(?:was|were|is|are|has\s+been|have\s+been|had\s+been|got)\s+)?` +
			`(?:modified|changed|created|deleted|removed|added|written|rewritten|touched|altered|renamed|moved|edited)\b`)
	globalScopePattern = regexp.MustCompile(
		`(?i)\b(?:repository|repo|project|codebase|workspace)\b`)
	insufficientSearchPattern = regexp.MustCompile(
		`(?i)\b(?:i\s+)?(?:could\s+not|couldn't|cannot|can't|was\s+unable\s+to|unable\s+to)\s+(?:be\s+)?(?:find|found|locate|located|read|access|accessed|see|seen)\b`)
	performedActionPattern = regexp.MustCompile(
		`(?i)\b(?:i|we)\s+(?:have\s+)?(?:already\s+)?(?:inspected|checked|reviewed|read|examined|scanned|analy[sz]ed|searched|looked\s+at|opened)\b`)
	restatedObjectivePattern = regexp.MustCompile(
		`(?i)\b(?:the\s+task\s+is|the\s+objective\s+is|you\s+asked\s+me\s+to|you\s+want\s+me\s+to|i\s+was\s+asked\s+to)\b[^.\n]{0,100}`)
	scopeNarrowPattern = regexp.MustCompile(
		`(?i)\b(?:only|just)\b[^.\n]{0,40}\b(?:this|the|current)\s+(?:repository|repo|project|workspace)\b`)

	// externalStatePattern matches an answer asserting that something happened
	// outside the workspace: a CI run, a push, a deployment, or a publish.
	// Present-tense configuration ("it runs on a schedule", "the job pushes
	// telemetry.svg") describes what is configured, not what happened, so only
	// completed-event words count as an assertion about external state. The verb
	// is captured so a modal or configuration form ("can also be triggered")
	// can be rejected before the claim is accepted.
	externalStatePattern = regexp.MustCompile(
		`(?i)\b(?:github|gitlab|bitbucket|ci|actions?|pipeline|workflow|deployment|release|origin|upstream|remote|server|production)\b[^.\n]{0,60}\b(ran|has run|already ran|executed|completed|succeeded|passed|triggered|pushed|deployed|published|went live)\b`)
	externalPushPattern = regexp.MustCompile(
		`(?i)\b(pushed|deployed|published)\b[^.\n]{0,40}\b(?:to|on|into)\s+(?:github|gitlab|bitbucket|origin|upstream|the remote|production|the server)\b`)
	// modalEventPattern matches a modal or copular introduction to the captured
	// verb. "can also be triggered" and "is triggered on a schedule" describe
	// what is possible or configured; "was triggered at 03:17" is an event.
	modalEventPattern = regexp.MustCompile(
		`(?i)\b(?:can|could|may|might|will|would|should|must|is|are)\s+(?:also\s+|automatically\s+)?(?:be\s+)?$`)
	// uncertaintyPattern marks an honest report about external state: the
	// answer names the claim in order to say it cannot be established.
	uncertaintyPattern = regexp.MustCompile(
		`(?i)\b(?:cannot|can't|could not|couldn't|unable|not able|no evidence|not verified|unverified|unknown|not established|does not establish|whether|may have|might have|possibly|would need)\b`)
	// remoteObservationPattern matches a command that actually talked to a
	// remote surface. A successful result is the only local evidence that
	// external state was observed rather than inferred.
	remoteObservationPattern = regexp.MustCompile(
		`(?i)\b(?:gh|git\s+push|git\s+ls-remote|git\s+fetch|git\s+remote|curl|wget|ssh|kubectl|terraform|aws|gcloud|az|docker\s+push|npm\s+publish|cargo\s+publish)\b`)
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

	// Evidence integrity: a conclusion about state outside the workspace needs
	// an observation from outside the workspace. Local configuration proves
	// what is configured, never that a remote run, push, or deployment
	// happened.
	if claims := externalClaims(answer); len(claims) > 0 {
		if !collectGroundingCoverage(turns).external {
			add("unsupported_external_state",
				"the answer states external state the run never observed: "+claims[0])
		}
	}

	// Evidence integrity: a negative conclusion must be earned by coverage
	// that can actually establish absence.
	if claims := negativeClaims(answer); len(claims) > 0 {
		coverage := collectGroundingCoverage(turns)
		for _, claim := range claims {
			if !coverage.supports(claim) {
				add("unsupported_negative_conclusion",
					fmt.Sprintf("the answer treats %q as absent without evidence covering the claim", claim.Subject))
				break
			}
		}
	}
	// Claim scope: declared configuration and dependency manifests establish
	// intent and topology, not runtime behavior. A concrete line count,
	// deployment conclusion, or path-scoped inference needs the observation
	// that supports that stronger claim. A mutation answer is judged by target
	// and verification evidence instead of this answer-level audit.
	if intent.Kind == contract.ObjectiveInformational {
		for _, issue := range claimScopeIssues(answer, turns) {
			add(issue.Code, issue.Detail)
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
			// "No test files were surfaced in the probes" is a report about
			// this run's observations, not a repository-wide absence. The
			// same sentence with a global scope remains an absence claim.
			if boundedNegativeReport(uncertaintyWindow(answer, match[0], match[1])) {
				continue
			}
			// A statement about what this run changed is not an existence
			// claim. "No files were modified" is a no-op report: a file scan
			// can never cover it, and review here would punish an honest
			// account of an untouched workspace.
			if changeReportPattern.MatchString(answer[match[1]:]) {
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

// externalClaims returns the assertions an answer makes about state outside the
// workspace. A sentence that names the claim only to report it as unverified
// ("I cannot establish whether GitHub ran it") is an epistemic report, not a
// claim, so it is not returned.
func externalClaims(answer string) []string {
	var out []string
	consider := func(start, end int) {
		if start < 0 || end <= start || end > len(answer) {
			return
		}
		claim := strings.TrimSpace(answer[start:end])
		if claim == "" {
			return
		}
		if uncertaintyPattern.MatchString(claim) {
			return
		}
		window := uncertaintyWindow(answer, start, end)
		if uncertaintyPattern.MatchString(window) {
			return
		}
		out = append(out, claim)
	}
	for _, pattern := range []*regexp.Regexp{externalStatePattern, externalPushPattern} {
		for _, match := range pattern.FindAllStringSubmatchIndex(answer, -1) {
			// Group 1 is the captured event verb. A modal or configuration form
			// before it describes possibility, not a completed event.
			if len(match) >= 4 && descriptiveEventForm(answer, match[2]) {
				continue
			}
			consider(match[0], match[1])
		}
	}
	return out
}

// descriptiveEventForm reports whether the event verb at position is introduced
// by a modal or a configuration phrase, so the sentence describes what can or
// should happen rather than what did happen.
func descriptiveEventForm(answer string, verbStart int) bool {
	const lookback = 64
	start := verbStart - lookback
	if start < 0 {
		start = 0
	}
	before := strings.ToLower(answer[start:verbStart])
	// Only the immediate clause matters: an earlier sentence's "can" must not
	// excuse a later event claim.
	if cut := strings.LastIndexAny(before, ".;,\n"); cut >= 0 {
		before = before[cut+1:]
	}
	if modalEventPattern.MatchString(before) {
		return true
	}
	for _, marker := range []string{
		"set up to", "configured to", "scheduled to", "allows", "able to",
		"capable of", "designed to",
	} {
		if strings.Contains(before, marker) {
			return true
		}
	}
	return false
}

// uncertaintyWindow returns the sentence around a claim, so a hedge in the same
// sentence ("the workflow may have run, but not verified") is seen even when
// the matched span is short.
func uncertaintyWindow(answer string, start, end int) string {
	left := start
	for left > 0 && !strings.ContainsRune(".!?\n", rune(answer[left-1])) {
		left--
	}
	right := end
	for right < len(answer) && !strings.ContainsRune(".!?\n", rune(answer[right])) {
		right++
	}
	return answer[left:right]
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
	// globScans are successful, complete recursive globs. The result text is
	// retained so a language-qualified test-file claim can be checked against
	// the names the scan actually returned.
	globScans []globScan
	// structuralTestAbsence is a complete, unscoped structure tests query that
	// saw no test files. It is equivalent to a complete recursive scan for the
	// narrow Go test-file claim, but only when scope, inventory, and structural
	// completeness all hold.
	structuralTestAbsence bool
	// rootListing is a complete listing of the repository root.
	rootListing bool
	// external reports a successful observation of a remote surface: a
	// knowledge call, or a command that talked to a remote (a remote check, a
	// push, an API call).
	external bool
}

// globScan is one complete recursive scan.
type globScan struct {
	pattern string
	text    string
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
	if c.supportsGoTestFiles(claim.Subject) {
		return true
	}
	for _, scan := range c.globScans {
		pattern := strings.ToLower(scan.pattern)
		if pattern == "**" || pattern == "**/*" || pattern == "*" {
			return isFileClaim(claim.Subject)
		}
		if strings.Contains(pattern, "**") && strings.Contains(pattern, claim.Subject) {
			return isFileClaim(claim.Subject)
		}
	}
	return false
}

// supportsGoTestFiles reports a Go test-file absence proven by a complete
// recursive scan of every Go file. Go mandates the `_test.go` suffix, so a
// complete scan without one is proof for that narrowed claim. A bare "no
// tests" claim remains a concept absence, and frontend test naming is
// convention-based rather than guaranteed.
func (c groundingCoverage) supportsGoTestFiles(subject string) bool {
	if !strings.Contains(subject, "test") || !mentionsGoLanguage(subject) {
		return false
	}
	if c.structuralTestAbsence {
		return true
	}
	for _, scan := range c.globScans {
		if !strings.Contains(scan.pattern, "**") ||
			!patternCoversExtension(scan.pattern, "go") {
			continue
		}
		if !strings.Contains(strings.ToLower(scan.text), "_test.go") {
			return true
		}
	}
	return false
}

// mentionsGoLanguage reports a standalone Go language token. It avoids reading
// "go" out of an unrelated name such as "django" or "google".
func mentionsGoLanguage(subject string) bool {
	for _, token := range strings.FieldsFunc(subject, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if token == "go" || token == "golang" {
			return true
		}
	}
	return false
}

// patternCoversExtension reports whether the glob names the extension. The
// suffix check keeps *.go from being read from *.golden; a brace group such as
// **/*.{go,tmpl} covers go, and a generic **/* covers everything.
func patternCoversExtension(pattern, extension string) bool {
	pattern = strings.ToLower(pattern)
	if pattern == "**" || pattern == "**/*" {
		return true
	}
	if strings.HasSuffix(pattern, "*."+extension) ||
		strings.HasSuffix(pattern, "."+extension) {
		return true
	}
	for _, group := range []string{"{" + extension + ",", "," + extension + "}", "{" + extension + "}"} {
		if strings.Contains(pattern, group) {
			return true
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
			if observedExternal(outcome) {
				coverage.external = true
			}
			text := strings.ToLower(outcome.Result.Text)
			// A scan is complete only when it covered the whole requested
			// tree. Truncation, a deadline, or a pruned .gitignore subtree all
			// leave the result unable to prove that something is absent.
			complete := !strings.Contains(text, "truncated") &&
				!strings.Contains(text, "showing ") &&
				!strings.Contains(text, "limit reached") &&
				!strings.Contains(text, "more results") &&
				!strings.Contains(text, "timed out") &&
				!strings.Contains(text, "excluded by .gitignore")
			switch outcome.ToolName {
			case "ls":
				path := proposalArgString(outcome.Args, "path")
				if (path == "" || path == "." || path == "./") && complete {
					coverage.rootListing = true
				}
			case "glob":
				pattern := proposalArgString(outcome.Args, "pattern")
				path := proposalArgString(outcome.Args, "path")
				details, _ := outcome.Result.Details.(map[string]any)
				// A gitignore=false scan is an investigative override, not a
				// complete project inventory. A built-in exclusion (node_modules,
				// dist, target) is a disclosed pruning gap too.
				gitignoreComplete := true
				if value, ok := details["gitignore"].(bool); ok {
					gitignoreComplete = value
				}
				unpruned := detailValueInt(details, "ignored") == 0 &&
					detailValueInt(details, "excluded") == 0
				if complete && gitignoreComplete && unpruned &&
					(path == "" || path == "." || path == "./") &&
					strings.Contains(pattern, "**") {
					coverage.globScans = append(coverage.globScans, globScan{
						pattern: pattern,
						text:    outcome.Result.Text,
					})
				}
			case "structure":
				if complete && structureProvesNoTestFiles(outcome) {
					coverage.structuralTestAbsence = true
				}
			}
		}
	}
	return coverage
}

// structureProvesNoTestFiles reports a complete, unscoped structure tests
// query with no test files and no excluded or unparsed scope.
//
// It is deliberately narrower than "the snapshot has zero tests": a pending
// parse, an unsupported language, an unreadable directory, or an excluded root
// each leaves the absence unproven. The same boundary that makes an overview
// honest makes a negative conclusion safe.
func structureProvesNoTestFiles(outcome ToolOutcome) bool {
	details, ok := outcome.Result.Details.(map[string]any)
	if !ok {
		return false
	}
	if detailValueString(details, "view") != "tests" || detailValueBool(details, "truncated") {
		return false
	}
	// Freshness is independent of completeness: a complete inventory of a
	// snapshot that no longer matches the tree does not establish current
	// absence.
	if detailValueString(details, "graph_state") != "CURRENT" {
		return false
	}
	if !detailValueBool(details, "scope_complete") ||
		!detailValueBool(details, "inventory_complete") ||
		!detailValueBool(details, "structural_complete") {
		return false
	}
	// A disclosed scope gap is a gap even if a producer reports a stale
	// completeness boolean. Excluded roots, excluded source files, and
	// unsupported languages each leave the inventory unable to prove absence.
	if len(claimFileRows(details["excluded_roots"])) > 0 ||
		detailValueInt(details, "excluded_source_files") > 0 {
		return false
	}
	for _, project := range claimFileRows(details["nested_projects"]) {
		if included, ok := project["included"].(bool); !ok || !included {
			return false
		}
	}
	for _, language := range claimFileRows(details["unsupported_languages"]) {
		if detailValueInt(language, "files") > 0 {
			return false
		}
	}
	if detailValueInt(details, "test_files") != 0 || detailValueInt(details, "total") != 0 {
		return false
	}
	if proposalArgString(outcome.Args, "path") != "" ||
		proposalArgString(outcome.Args, "package") != "" ||
		proposalArgString(outcome.Args, "symbol") != "" {
		return false
	}
	return true
}

func detailValueString(details map[string]any, key string) string {
	value, _ := details[key].(string)
	return value
}

func detailValueBool(details map[string]any, key string) bool {
	value, _ := details[key].(bool)
	return value
}

func detailValueInt(details map[string]any, key string) int {
	switch value := details[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

// observedExternal reports whether one successful outcome saw a remote surface.
//
// The decision is made from trusted metadata and arguments, never from the
// model's narration: a knowledge tool is external by capability, and a shell
// command is external only when the command it ran names a remote operation.
func observedExternal(outcome ToolOutcome) bool {
	switch outcome.ToolName {
	case "web_fetch", "search", "browser":
		return true
	}
	if strings.HasPrefix(outcome.ToolName, "mcp_") {
		return true
	}
	command := proposalArgString(outcome.Args, "command")
	return remoteObservationPattern.MatchString(command)
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
