package agent

import (
	"fmt"
	"regexp"
	"sort"
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
	// conditionalAbsencePattern marks the condition of a behavior rule. "It
	// redirects to /cart when the cart is missing" describes a branch, not a
	// repository absence.
	conditionalAbsencePattern = regexp.MustCompile(
		`(?i)\b(?:when|if|unless|whenever|until)\b[^.;\n]{0,50}$`)
	// negativeAbsenceOfPattern catches the noun form of a repository absence:
	// "the absence of tests". It is the same conclusion as "there are no tests"
	// and must earn the same coverage.
	negativeAbsenceOfPattern = regexp.MustCompile(
		`(?i)\babsence\s+of\s+([a-z0-9][a-z0-9 _./-]{0,40}?)(?:\s+(?:is|are|was|were|in|within|at|on|for|to)\b|[.,;:]|$)`)
	// negativeUncertainPredicatePattern marks an absence the answer itself
	// reports as unproven. "The absence of tests elsewhere is unproven" is an
	// honest scope limitation, not the assertion that tests are absent; the
	// negative-claim audit must not demand an inventory for it.
	negativeUncertainPredicatePattern = regexp.MustCompile(
		`(?i)^\s*(?:(?:is|are|was|were|remains?|has\s+not\s+been|have\s+not\s+been)\s+)?` +
			`(?:unproven|not\s+proven|not\s+established|not\s+verified|unverified|unknown|undetermined|indeterminate|unclear|not\s+determined)\b`)
	// changeReportPattern follows a negated quantity with a run predicate.
	// "No files were modified", "no source files were read", and "no
	// application or test was executed" report what the run did, not what the
	// repository contains. No file scan can establish them, and they are not
	// the existence claim the negative-conclusion check exists to police.
	changeReportPattern = regexp.MustCompile(
		`(?i)^\s*(?:content\s+)?(?:(?:was|were|is|are|has\s+been|have\s+been|had\s+been|got)\s+)?` +
			`(?:modified|changed|created|deleted|removed|added|written|rewritten|touched|altered|renamed|moved|edited` +
			`|read|inspected|examined|reviewed|opened|loaded|queried|searched|sampled|observed` +
			`|executed|run|ran|performed|started|launched|invoked|triggered)\b`)
	// runReportPattern names a coordinated report about what this run did:
	// "no build, test, or runtime session was executed". The subject list ends
	// at the passive run verb, so the ordinary changeReportPattern (anchored at
	// the end of the negative match) cannot see it. It is a limitation of the
	// run, not a claim that the repository contains no tests or builds. "run",
	// "suite", and "application" are heads of the same noun phrases ("test
	// run", "test suite"); omitting them made "no application or test run was
	// performed" read as a repository absence.
	runReportPattern = regexp.MustCompile(
		`(?i)\b(?:build|builds|test|tests|runtime|command|commands|check|checks|execution|session|probe|probes|run|runs|suite|suites|application|applications|app|apps)\b` +
			`[^.;\n]{0,60}\b(?:was|were|is|are|has\s+been|have\s+been|had\s+been)\s+` +
			`(?:executed|run|performed|started|launched|invoked|observed|queried)\b`)
	globalScopePattern = regexp.MustCompile(
		`(?i)\b(?:repository|repo|project|codebase|workspace)\b`)
	// rootScopePattern names a location bounded to the top level of the tree.
	// "No README at repo root" is the narrowest possible absence claim and a
	// root listing covers it; the word "repo" inside it must not promote it to
	// a repository-wide claim.
	rootScopePattern = regexp.MustCompile(
		`(?i)\b(?:(?:repo(?:sitory)?|project|workspace|directory|folder)?[\s-]*root|top[- ]level)\b`)
	insufficientSearchPattern = regexp.MustCompile(
		`(?i)\b(?:i\s+)?(?:could\s+not|couldn't|cannot|can't|was\s+unable\s+to|unable\s+to)\s+(?:be\s+)?(?:find|found|locate|located|read|access|accessed|see|seen)\b`)
	// boundedRunLimitPattern names the run's own resource bound. "The Readme
	// could not be read before the evidence window closed" is a truthful
	// limitation of this run, not a claim that the file was inaccessible; the
	// completion audit must not demand a call the budget never allowed.
	boundedRunLimitPattern = regexp.MustCompile(
		`(?i)\b(?:before\s+(?:the\s+)?(?:evidence\s+|turn\s+|time\s+)?(?:window|run|budget|deadline|turn|turns)|` +
			`within\s+(?:the\s+)?(?:evidence|turn|time)\s+(?:window|budget)|` +
			`(?:repair|correction|evidence|tool)\s+(?:window|budget|allowance)|` +
			`not\s+successfully\s+read|were\s+not\s+read|was\s+not\s+read|not\s+parsed|unparsed|` +
			`ran\s+out\s+of\s+(?:turns|budget|time|evidence))\b`)
	// An epistemic limit is not a repository absence. "No contradiction to
	// arbitrate" says the comparison cannot be made; it does not assert what
	// the repository contains. The distinction matters when no second evidence
	// provider (runtime or Gry) was used: the answer must say the comparison was
	// not assessable, and completion must not demand a file scan for it.
	negativeEpistemicPattern = regexp.MustCompile(
		`(?i)\bno\s+[^.\n]{0,80}?\bto\s+(?:arbitrate|assess|establish|verify|compare|evaluate|resolve|determine|inspect|test|run|read|measure|observe|report|describe|present|provide|offer|demonstrate|quantify)\b`)
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
	// negatedEventPattern marks a negator inside the event's subject phrase:
	// "the server was not executed", "the workflow hasn't run". The sentence
	// then reports that the event did not happen.
	negatedEventPattern = regexp.MustCompile(
		`(?i)\b(?:not|never|no|none|without|nor|neither)\b|\b\w+n['’]t\b`)
	// negatedLeadPattern marks a negator immediately before the event's
	// subject: "no server was executed", "no deployment was published".
	negatedLeadPattern = regexp.MustCompile(
		`(?i)\b(?:no|not|never|without|nor|neither)\s+$`)
	// negatedListLeadPattern marks a negator that introduces a coordinated
	// subject list ending at the claim's subject: "No tests, server, or
	// binary were executed". One negator distributes over every item, so no
	// listed event happened.
	negatedListLeadPattern = regexp.MustCompile(
		`(?i)\b(?:no|not|never|without|nor|neither)\b`)
	// finiteVerbPattern reports a clause predicate. A negator before a list
	// that already contains one governs its own clause only: "No tests were
	// run, but the deployment was executed" keeps the deployment claim.
	finiteVerbPattern = regexp.MustCompile(
		`(?i)\b(?:was|were|is|are|has|have|had|been|did|does|do|ran|run|executed|deployed|published|pushed|triggered|completed|succeeded|passed|started|launched)\b`)
	// subjectListArticle is a determiner, not a list item. "No, the server
	// was executed" is a discourse marker followed by a claim, not a
	// distributed negation over "the".
	subjectListArticle = map[string]bool{
		"the": true, "a": true, "an": true, "this": true, "that": true,
		"these": true, "those": true, "its": true, "their": true,
		"or": true, "and": true, "nor": true, "but": true,
	}
	// uncertaintyPattern marks an honest report about external state: the
	// answer names the claim in order to say it cannot be established.
	uncertaintyPattern = regexp.MustCompile(
		`(?i)\b(?:cannot|can't|could not|couldn't|unable|not able|no evidence|not verified|unverified|unknown|not established|unproven|not proven|does not establish|whether|may have|might have|possibly|would need)\b`)
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
		addIssue(contract.GroundingIssue{Code: code, Detail: detail})
	}

	// Grounding only reads the definite-mutation flag; ordering is decided by
	// the completion evaluator, which has the journal evidence.
	evidence := scanCompletionEvidence(turns, intent.Kind == contract.ObjectiveMutation, false)
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
			addIssue(contract.GroundingIssue{
				Code:        "unsupported_external_state",
				Detail:      "the answer states external state the run never observed: " + claims[0],
				Claim:       claims[0],
				Category:    "runtime",
				Requirement: "runtime observation",
				Action:      "execute the operation or qualify the claim as unverified",
			})
		}
	}

	// Evidence integrity: a negative conclusion must be earned by coverage
	// that can actually establish absence.
	if claims := negativeClaims(answer); len(claims) > 0 {
		coverage := collectGroundingCoverage(turns)
		// The most specific subject leads. One sentence can produce both a
		// precise claim ("portfolio file") and the bare noun a pattern matched
		// ("file"), and reporting the bare noun tells the reader nothing about
		// what the answer got wrong.
		sort.SliceStable(claims, func(i, j int) bool {
			return len(claims[i].Subject) > len(claims[j].Subject)
		})
		for _, claim := range claims {
			if coverage.supports(claim) {
				continue
			}
			detail := fmt.Sprintf("the answer treats %q as absent without evidence covering the claim", claim.Subject)
			if observed := coverage.contradicts(claim.Subject); observed != "" {
				// The run can do better than "unproven": it listed the very
				// entry the answer calls missing. Naming it turns a review
				// boundary into a correction the reader can act on.
				detail = fmt.Sprintf(
					"the answer says %q is absent, but this run observed %s; an observation refutes the absence",
					claim.Subject, observed)
			} else if coverage.testFiles > 0 && strings.Contains(strings.ToLower(claim.Subject), "test") {
				detail = fmt.Sprintf(
					"the answer treats %q as absent, but the observed inventory already reports %d test file(s); inspect them or bound the claim to what was inspected",
					claim.Subject, coverage.testFiles)
			} else if coverage.testInventoryObserved && strings.Contains(strings.ToLower(claim.Subject), "test") {
				// A static inventory counts paths it can associate with tests.
				// It cannot prove that a JS/TS project has no tests under
				// another naming convention, so the claim must be bounded to
				// what the inventory actually reported.
				detail = fmt.Sprintf(
					"the answer treats %q as absent, but the run's static test inventory reports %d test file(s); a static inventory is not proof that tests do not exist. Say \"0 test files were reported\" or remove the claim",
					claim.Subject, coverage.testFiles)
			}
			addIssue(contract.GroundingIssue{
				Code:        "unsupported_negative_conclusion",
				Detail:      detail,
				Claim:       claim.Subject,
				Location:    claimLocationAt(answer, claim.Start, claim.End),
				Category:    "absence",
				Requirement: "complete current inventory",
				Reference:   claim.Subject,
				Action:      "inspect the observed subject or bound the claim to the inspected scope",
			})
			if len(assessment.Issues) >= MaxRepairInstructions {
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
			addIssue(issue.groundingIssue())
		}
	}
	// Coverage is not itself a claim. A project-wide answer is judged by the
	// claims it makes and the evidence those claims require, not by whether it
	// repeats a disclaimer about the repository. Requiring a disclosure here
	// made "this project is X" fail unless the answer said "this is partial",
	// which taught the model to add a generic limitation instead of obtaining
	// the missing observation. The TUI footer still reports measured structural
	// coverage, where it is presentation, not a completion veto.
	// The explicit deliverable contract is separate from claim scope: an answer
	// can avoid every unsupported claim and still omit the field the user asked
	// for. The facet check owns that obligation.
	if facets := AssessRequiredFacets(intent, turns, answer); !facets.Pass() {
		for _, issue := range facets.Issues {
			addIssue(issue)
		}
	}
	if intent.Kind != contract.ObjectiveMutation || grounding.HasAction(contract.GroundingInvestigate) {
		for _, match := range insufficientSearchPattern.FindAllStringIndex(answerLower, -1) {
			// A limitation the sentence itself attributes to the run's
			// evidence window is not an unavailable capability. The model
			// reported why it stopped, which is the honest answer the
			// completion boundary asks for.
			window := uncertaintyWindow(answerLower, match[0], match[1])
			// "I could not read it" is not the same as "the request could not
			// be answered": when the run listed the path, the statement is
			// about a call that was never made, and the reader needs the path,
			// not the admission. An observed-but-unread path outranks a
			// bounded-window phrase in the same sentence.
			declined := collectGroundingCoverage(turns).declinesObservedPath(answerLower)
			if declined == "" && (boundedRunLimitPattern.MatchString(window) ||
				claimIsBounded(window) || boundedNegativeReport(window)) {
				// The sentence itself bounds the limitation to what this run
				// observed or to the window it was given. That is an honest
				// scope statement, not a claim that the evidence is absent or
				// inaccessible.
				continue
			}
			detail := "the answer reports an inability to find evidence as the result"
			if declined != "" {
				detail = fmt.Sprintf(
					"the answer reports it could not read the evidence, but this run observed %s; that gap is a call the run did not make, not a file it could not access",
					declined)
			}
			add("unverified_negative_result", detail)
			break
		}
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
	// Start and End delimit this specific occurrence. Re-finding the subject
	// later would point the repair at the first "test" token in the answer,
	// which may be a supported manifest statement rather than the unsupported
	// sentence the audit rejected.
	Start int
	End   int
}

// negativeClaims returns the absence conclusions an answer asserts. An honest
// "I could not find X" is handled separately: it is an epistemic report, not a
// claim that X does not exist.
func negativeClaims(answer string) []negativeClaim {
	var out []negativeClaim
	add := func(value string, global bool, start, end int) {
		value = cleanClaimSubject(value)
		if value == "" || claimSubjectIsEvidenceGap(value) {
			return
		}
		for _, existing := range out {
			if claimSubjectKey(existing.Subject) == claimSubjectKey(value) && existing.Global == global {
				return
			}
		}
		out = append(out, negativeClaim{Subject: value, Global: global, Start: start, End: end})
	}
	addMatches := func(pattern *regexp.Regexp, repositoryWide bool) {
		for _, match := range pattern.FindAllStringSubmatchIndex(answer, -1) {
			if len(match) < 4 || match[2] < 0 || match[3] < 0 {
				continue
			}
			if pattern == negativeMissingPattern && conditionalAbsenceForm(answer, match[0]) {
				continue
			}
			window := uncertaintyWindow(answer, match[0], match[1])
			// "No test files were surfaced in the probes" is a report about
			// this run's observations, not a repository-wide absence. The
			// same sentence with a global scope remains an absence claim.
			if boundedNegativeReport(window) || negativeEpistemicPattern.MatchString(window) {
				continue
			}
			// An absence the answer itself calls unproven is a limitation, not
			// the assertion that the subject is missing. It needs no inventory;
			// the final facet/completion boundary owns the unresolved gap.
			if negativePredicateUncertain(answer, match[1]) {
				continue
			}
			// A statement about what this run changed is not an existence
			// claim. "No files were modified" is a no-op report: a file scan
			// can never cover it, and review here would punish an honest
			// account of an untouched workspace.
			if changeReportPattern.MatchString(answer[match[1]:]) ||
				runReportFollowsNegative(answer, match[1]) {
				continue
			}
			// "The README states there is no hosted service" reports the
			// documentation, not a world absence. The audit owns what the
			// repository contains; a documentation-attributed absence is a
			// provenance claim and is checked by the attribution audit.
			if attributedNegativeClaim(answer, match[0], match[1]) {
				continue
			}
			if claimIsEvidenceGap(answer, match[3]) {
				// "No runtime or configuration evidence" is a statement about
				// this run's observations, not a repository absence. Treating
				// an epistemic limitation as a negative conclusion punishes the
				// answer for being explicit about what it did not observe.
				continue
			}
			// "No test scripts declared" is a statement about a manifest's
			// declaration list, not a repository-wide absence. The observed
			// package.json is its evidence; asking for source coverage here
			// points the model at a sentence it already supported.
			if manifestDeclarationAbsence(answer, match[0], match[1]) {
				continue
			}
			value := answer[match[2]:match[3]]
			global := repositoryWide || claimContextIsGlobal(answer, match[0], match[1])
			add(value, global, match[0], match[1])
		}
	}
	addMatches(negativeRepositoryPattern, true)
	addMatches(negativeThereIsPattern, false)
	addMatches(negativeNoPattern, false)
	addMatches(negativeExistPattern, false)
	addMatches(negativeAbsentPattern, false)
	addMatches(negativeMissingPattern, false)
	addMatches(negativeAbsenceOfPattern, false)
	return out
}

// negativePredicateUncertain reports whether the predicate immediately after
// a negative match qualifies it as unproven. The match may consume the copula
// ("absence of tests is" + "unproven") or stop before it; both forms are
// reports of uncertainty, not absence claims. The scan is bounded to the
// predicate's own clause so a later "cannot" cannot excuse a real absence.
func negativePredicateUncertain(answer string, end int) bool {
	if end < 0 || end >= len(answer) {
		return false
	}
	limit := end + 80
	if limit > len(answer) {
		limit = len(answer)
	}
	tail := answer[end:limit]
	if cut := strings.IndexAny(tail, ".;\n"); cut >= 0 {
		tail = tail[:cut]
	}
	return negativeUncertainPredicatePattern.MatchString(tail)
}

// attributedNegativeClaim reports whether the negative occurrence is a
// statement about documentation rather than a claim that the subject is absent
// from the repository. The attribution must share the clause with the
// negative; a later contrast ("README says no X, so the project has no X") is
// a world claim and must still be audited.
func attributedNegativeClaim(answer string, start, end int) bool {
	if start < 0 || end < start || end > len(answer) {
		return false
	}
	lineStart := strings.LastIndex(answer[:start], "\n") + 1
	linePrefix := answer[lineStart:start]
	clauseStart := strings.LastIndexAny(linePrefix, ".;!?")
	prefix := linePrefix[clauseStart+1:]
	if hasAttribution(prefix) && !negativeAttributionContrast(prefix) {
		return true
	}
	// A bullet under an attributed section ("**Per README**" followed by a
	// list) inherits the section's provenance. Only the immediately preceding
	// non-empty line is considered; an unrelated earlier attribution does not
	// govern a later section.
	if lineStart > 1 {
		previousEnd := lineStart - 1
		previousStart := strings.LastIndex(answer[:previousEnd], "\n") + 1
		previous := strings.TrimSpace(answer[previousStart:previousEnd])
		if previous != "" && hasAttribution(previous) && !negativeAttributionContrast(previous) {
			return true
		}
	}
	// "There is no hosted service, according to the README" attributes the
	// same clause after the negative. Stop at a clause boundary so a later
	// sentence's attribution cannot excuse this claim.
	suffix := answer[end:]
	if cut := strings.IndexAny(suffix, ".;!?\n"); cut >= 0 {
		suffix = suffix[:cut]
	}
	return hasAttribution(suffix) && !negativeAttributionContrast(suffix)
}

// negativeAttributionContrast reports a conclusion joined to the attribution.
// "The README says X and the project has no Y" cannot borrow README as the
// evidence for Y.
func negativeAttributionContrast(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{" but ", " however ", " yet ", " whereas ", " so ", " therefore ", " thus ", " hence ", " and the project ", " and the repository "} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// manifestDeclarationAbsence reports a negative about what a manifest
// declares ("no test scripts declared", "package.json declares no test
// script"), not a claim that the repository contains no such thing. A
// declaration list is an inventory fact about an observed file; this audit
// owns repository absence, so treating the two as one claim makes a supported
// sentence block publication and hides the unsupported sentence.
func manifestDeclarationAbsence(answer string, start, end int) bool {
	if start < 0 || end <= start || end > len(answer) {
		return false
	}
	tail := answer[end:]
	if sentences := claimSentenceBoundary.Split(tail, -1); len(sentences) > 0 {
		tail = sentences[0]
	}
	if manifestDeclarationTailPattern.MatchString(strings.ToLower(strings.ReplaceAll(tail, "`", ""))) {
		return true
	}
	// The declaration verb can precede the negation: "package.json declares
	// no test script". Only the clause before the match matters.
	lead := answer[max(0, start-80):start]
	if sentences := claimSentenceBoundary.Split(lead, -1); len(sentences) > 0 {
		lead = sentences[len(sentences)-1]
	}
	// A manifest mention earlier in the same sentence does not govern a later
	// clause: "`package.json` has no test script, so the project has no tests"
	// is a manifest fact followed by a repository conclusion.
	if cut := strings.LastIndexAny(lead, ",;"); cut >= 0 {
		lead = lead[cut+1:]
	}
	return manifestDeclarationLeadPattern.MatchString(strings.ToLower(strings.ReplaceAll(lead, "`", "")))
}

var (
	manifestDeclarationTailPattern = regexp.MustCompile(
		`^\s+(?:(?:scripts?|commands?|entries|fields|keys|targets?)\s*(?:are|is|was|were)?\s*(?:declared|defined|listed|configured)\b|` +
			`(?:scripts?|commands?|entries|fields|keys|targets?)\s*(?:are|is|was|were)?\s*(?:in|on|from)\s+(?:the\s+)?(?:package\.json|manifest)\b)`)
	manifestDeclarationLeadPattern = regexp.MustCompile(
		`(?:\b(?:package\.json|manifest|scripts?\s+(?:field|section|entry))\b|scripts?\s*:)[^.;\n]{0,60}$`)
)

// runReportFollowsNegative reports a coordinated run report whose passive verb
// follows the negative noun span: "no build, test, or runtime session was
// executed". The span is bounded to the sentence and to the text after the
// negative match, so an unrelated run report in another clause is not used to
// excuse a repository absence.
func runReportFollowsNegative(answer string, end int) bool {
	if end < 0 || end >= len(answer) {
		return false
	}
	limit := end + 140
	if limit > len(answer) {
		limit = len(answer)
	}
	tail := answer[end:limit]
	if cut := strings.IndexAny(tail, ".;\n"); cut >= 0 {
		tail = tail[:cut]
	}
	return runReportPattern.MatchString(tail)
}

// conditionalAbsenceForm reports an absence phrase inside a behavior
// condition ("when the cart is missing").
func conditionalAbsenceForm(answer string, start int) bool {
	left := start - 64
	if left < 0 {
		left = 0
	}
	return conditionalAbsencePattern.MatchString(strings.ToLower(answer[left:start]))
}

// claimSubjectIsEvidenceGap reports a subject that names the run's evidence
// rather than a repository artifact.
func claimSubjectIsEvidenceGap(subject string) bool {
	subject = strings.ToLower(strings.TrimSpace(subject))
	for _, suffix := range []string{"evidence", "observation", "observations", "information", "data"} {
		if subject == suffix || strings.HasSuffix(subject, " "+suffix) {
			return true
		}
	}
	// "I make no claim about test coverage" is an epistemic limit, not a
	// statement that the repository has no tests. The noun after "no" is the
	// claim the answer declines to make, not an artifact it asserts absent.
	for _, prefix := range []string{"claim", "claims", "statement", "statements", "assertion", "assertions", "conclusion", "conclusions"} {
		if subject == prefix || strings.HasPrefix(subject, prefix+" ") {
			return true
		}
	}
	return false
}

// claimIsEvidenceGap reports whether the words after an absence span make it a
// statement about the run's evidence ("no runtime evidence", "no configuration
// evidence available") rather than a claim that the repository lacks the thing.
func claimIsEvidenceGap(answer string, end int) bool {
	if end < 0 || end >= len(answer) {
		return false
	}
	limit := end + 48
	if limit > len(answer) {
		limit = len(answer)
	}
	rest := strings.ToLower(strings.TrimSpace(answer[end:limit]))
	for _, suffix := range []string{
		"evidence", "evidence available", "evidence was available",
		"evidence observed", "observation", "observations",
		"data", "data available", "information", "information available",
		"is not evidence", "isn't evidence", "is not proof", "is not a claim",
		"does not establish", "doesn't establish",
	} {
		if strings.HasPrefix(rest, suffix) {
			return true
		}
	}
	return false
}

// claimContextIsGlobal reports whether the sentence containing a claim names
// the repository as a whole. "README.md does not exist in this repository" is
// not the same claim as "README.md is absent from docs/".
//
// The window is the claim's own sentence, not a fixed byte radius: session 324
// reviewed "no LICENSE file" as a repository-wide absence because the word
// "repo" in the *next* sentence ("a scaffolded e-commerce microservices repo
// …") fell inside the radius. A parenthetical aside is excluded too — "nested
// backend/.git (root repo ignores backend/)" describes the git layout, it does
// not scope the other items in the list.
func claimContextIsGlobal(answer string, start, end int) bool {
	begin, finish := 0, len(answer)
	for _, match := range claimSentenceBoundary.FindAllStringIndex(answer, -1) {
		if match[1] <= start {
			begin = match[1]
			continue
		}
		if match[0] >= end {
			finish = match[0]
			break
		}
	}
	if begin > start {
		begin = 0
	}
	if finish < end {
		finish = len(answer)
	}
	window := parenthesesPattern.ReplaceAllString(answer[begin:finish], " ")
	window = rootScopePattern.ReplaceAllString(window, " ")
	return globalScopePattern.MatchString(window)
}

// claimLocationAt returns the sentence containing one specific claim
// occurrence. The match offsets are authoritative; searching for the subject
// again would locate the first occurrence, which may be a different claim.
func claimLocationAt(answer string, start, end int) string {
	if start < 0 || end <= start || end > len(answer) {
		return ""
	}
	left := start
	for left > 0 && !strings.ContainsRune(".!?\n", rune(answer[left-1])) {
		left--
	}
	right := end
	for right < len(answer) && !strings.ContainsRune(".!?\n", rune(answer[right])) {
		right++
	}
	return strings.TrimSpace(answer[left:right])
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
			// A negated event reports that the external state did not occur.
			// "No tests were run and no server/database was executed" is an
			// evidence-gap report, not the claim that the server was used.
			if len(match) >= 4 && negatedEventForm(answer, match[0], match[2]) {
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

// negatedEventForm reports whether the event verb is negated by its own
// subject phrase, so the sentence states that the event did not happen.
//
// The negator must attach to the subject or sit inside the subject-verb
// phrase. Scanning the whole sentence would excuse a real claim next to an
// unrelated negation ("no tests were run, but the deployment was executed"),
// which is exactly the assertion this check exists to review.
func negatedEventForm(answer string, subjectStart, verbStart int) bool {
	if subjectStart < 0 || verbStart <= subjectStart || verbStart > len(answer) {
		return false
	}
	if negatedEventPattern.MatchString(strings.ToLower(answer[subjectStart:verbStart])) {
		return true
	}
	lead := subjectStart - 16
	if lead < 0 {
		lead = 0
	}
	return negatedLeadPattern.MatchString(strings.ToLower(answer[lead:subjectStart])) ||
		negatedSubjectList(answer, subjectStart)
}

// negatedSubjectList reports a negator that introduces a coordinated subject
// list ending at the claim's subject: "No tests, server, or binary were
// executed". The negator distributes over every item, so the event did not
// happen for any of them.
//
// The span before the subject must contain no predicate of its own. That is
// what keeps "No tests were run, but the deployment was executed" a claim
// about the deployment: the earlier clause already has its verb. A discourse
// marker ("No, the server was executed") is not a list either, so the span
// must name at least one content subject, not just a determiner.
func negatedSubjectList(answer string, subjectStart int) bool {
	if subjectStart <= 0 || subjectStart > len(answer) {
		return false
	}
	start := subjectStart - 96
	if start < 0 {
		start = 0
	}
	if cut := strings.LastIndexAny(answer[start:subjectStart], ".;:\n"); cut >= 0 {
		start += cut + 1
	}
	before := answer[start:subjectStart]
	loc := negatedListLeadPattern.FindStringIndex(before)
	if loc == nil {
		return false
	}
	segment := before[loc[1]:]
	if finiteVerbPattern.MatchString(segment) {
		return false
	}
	return subjectListHasContent(segment)
}

// subjectListHasContent reports a noun token in the list span. Articles and
// conjunctions alone are not a coordinated subject.
func subjectListHasContent(segment string) bool {
	for _, item := range strings.FieldsFunc(segment, func(r rune) bool {
		return r == ',' || r == ';'
	}) {
		for _, raw := range strings.Fields(strings.ToLower(item)) {
			token := strings.Trim(raw, " \t\r\n\"'`*_")
			if token == "" || subjectListArticle[token] {
				continue
			}
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

// claimLocation returns the sentence around a claim, bounded for a repair
// instruction. The offset is only a locator for the model; it is never an
// evidence coordinate.
func claimLocation(answer, claim string) string {
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return ""
	}
	lower := strings.ToLower(answer)
	needle := strings.ToLower(claim)
	index := -1
	for offset := 0; offset < len(lower); {
		found := strings.Index(lower[offset:], needle)
		if found < 0 {
			break
		}
		start := offset + found
		end := start + len(needle)
		// "ddd" inside "middle" is not the claim. Locate the token, not a
		// substring, so the repair points at the sentence the audit read.
		if (start == 0 || !codeIdentByte(lower[start-1])) &&
			(end == len(lower) || !codeIdentByte(lower[end])) {
			index = start
			break
		}
		offset = start + 1
	}
	if index < 0 {
		return ""
	}
	left := index
	for left > 0 && !strings.ContainsRune(".!?\n", rune(answer[left-1])) {
		left--
	}
	right := index + len(claim)
	for right < len(answer) && !strings.ContainsRune(".!?\n", rune(answer[right])) {
		right++
	}
	return boundRepair(strings.TrimSpace(answer[left:right]), 180)
}

// claimSubjectKey collapses spelling variants of one absence claim. A single
// sentence can produce "known test", "tests", and "test files" from
// overlapping patterns; they are one gap, not three repair instructions.
func claimSubjectKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "known ")
	switch {
	case strings.Contains(value, "test"):
		return "test"
	case value == "file" || value == "files":
		return "file"
	}
	return value
}

// cleanClaimSubject trims a captured negative subject to the thing named,
// without swallowing a qualifier into the claim.
func cleanClaimSubject(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "known ")
	value = strings.Trim(value, " .,;:!?")
	for _, suffix := range []string{
		" yet", " currently", " at all", " here", " known",
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
	// testInventoryObserved reports that the run queried the structural test
	// inventory. Zero test files is a measurement, not proof of concept
	// absence, so the repair detail must name the inventory rather than asking
	// for a completeness proof the audit cannot accept.
	testInventoryObserved bool
	// testFiles is the largest test-file count any observed structure snapshot
	// reported. A positive count contradicts an absence claim even when the
	// snapshot is not complete enough to prove zero, and the review should say
	// so instead of asking for evidence the run already has.
	testFiles int
	// rootListing is a complete listing of the repository root.
	rootListing bool
	// external reports a successful observation of a remote surface: a
	// knowledge call, or a command that talked to a remote (a remote check, a
	// push, an API call).
	external bool
	// observedPaths are the names the run actually listed, in observation
	// order. They exist to contradict an absence claim the run can refute,
	// which is more useful than reporting that the absence was unproven.
	observedPaths []string
	// vcsAbsence holds the VCS namespaces an observation established empty: a
	// complete enumeration (`git tag -l`) or an output that stated the absence
	// outright ("No commits yet"). A git inspection is bounded evidence, so an
	// answer reporting its empty result is not an unproven absence.
	vcsAbsence map[string]bool
}

// globScan is one complete recursive scan.
type globScan struct {
	pattern string
	text    string
}

// contradicts returns the observed path that refutes an absence claim, or "".
//
// The subject is a noun phrase ("portfolio file", "frontend module"), so the
// comparison is word by word against the stems the run actually listed, never
// against the phrase itself. Generic nouns are ignored: "no file matched" is a
// report about a search, not a claim that a name does not exist.
func (c groundingCoverage) contradicts(subject string) string {
	tokens := claimNameTokens(subject)
	if len(tokens) == 0 {
		return ""
	}
	// A package or directory name refutes absence only when the claim is
	// about a file-like artifact. "No portfolio file" is refuted by
	// portfolio.tsx; "no productcatalog existence check" is not refuted by the
	// productcatalog directory, because the claim is about a check, not the
	// package's existence.
	head := tokens[len(tokens)-1]
	allowModifiers := fileArtifactHead(head)
	for _, path := range c.observedPaths {
		stem := strings.ToLower(pathStem(path))
		if stem == "" {
			continue
		}
		for _, token := range tokens {
			if !allowModifiers && token != head {
				continue
			}
			if stem == token {
				return path
			}
		}
	}
	return ""
}

// fileArtifactHead reports a noun whose modifiers may name the artifact: a
// "portfolio file" is refuted by a portfolio path, while a "cart check" is not
// refuted by the cart directory.
func fileArtifactHead(token string) bool {
	switch token {
	case "file", "files", "module", "modules", "component", "components",
		"directory", "directories", "folder", "folders", "artifact", "artifacts",
		"path", "paths", "script", "scripts", "config", "configuration",
		"manifest", "package", "packages", "template", "templates":
		return true
	}
	return false
}

// declinesObservedPath returns the observed path an answer says it could not
// read, or "" when the answer names nothing the run actually listed.
func (c groundingCoverage) declinesObservedPath(answer string) string {
	best := ""
	for _, path := range c.observedPaths {
		stem := strings.ToLower(pathStem(path))
		if len(stem) < 3 || genericClaimNoun(stem) {
			continue
		}
		// A stem must be a whole token: "auth" inside "authentication" is not
		// the observed backend/auth path, and matching it would attribute an
		// unrelated limitation to a file the run never named.
		if tokenObservedBoundary(answer, stem) && len(stem) > len(strings.ToLower(pathStem(best))) {
			best = path
		}
	}
	return best
}

// claimNameTokens extracts the words of a claim subject that can name a path.
func claimNameTokens(subject string) []string {
	var out []string
	for _, field := range strings.FieldsFunc(strings.ToLower(subject), func(r rune) bool {
		return r != '-' && r != '_' && r != '.' && r != '/' &&
			(r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		token := pathStem(field)
		if len(token) < 3 || genericClaimNoun(token) {
			continue
		}
		out = append(out, token)
	}
	return out
}

// genericClaimNoun reports a noun that names a kind, never a coordinate.
func genericClaimNoun(token string) bool {
	switch token {
	case "file", "files", "dir", "dirs", "directory", "directories", "folder",
		"folders", "module", "modules", "component", "components", "route",
		"routes", "page", "pages", "test", "tests", "package", "packages",
		"service", "services", "the", "and", "any", "additional", "other":
		return true
	default:
		return false
	}
}

// pathStem returns a path's last element without its extension, so
// "app/portfolio.tsx" and "portfolio" name the same thing.
func pathStem(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, "/"))
	if value == "" {
		return ""
	}
	if index := strings.LastIndexAny(value, "/\\"); index >= 0 {
		value = value[index+1:]
	}
	if index := strings.LastIndex(value, "."); index > 0 {
		value = value[:index]
	}
	return value
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
	if c.supportsVCS(claim.Subject) {
		return true
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

// supportsVCS reports an absence the run's git inspection established. The
// mapping is finite on purpose: only VCS namespaces a complete enumeration can
// list are covered, so a sentence about a concept ("no authentication") stays
// a semantic claim the inspection cannot prove.
func (c groundingCoverage) supportsVCS(subject string) bool {
	if len(c.vcsAbsence) == 0 {
		return false
	}
	for _, raw := range strings.FieldsFunc(subject, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		namespace, ok := vcsNamespaceSingular[raw]
		if ok && c.vcsAbsence[namespace] {
			return true
		}
	}
	return false
}

// vcsNamespaceSingular maps the nouns an answer uses to the namespace a git
// enumeration reports.
var vcsNamespaceSingular = map[string]string{
	"commit": "commit", "commits": "commit",
	"tag": "tag", "tags": "tag",
	"branch": "branch", "branches": "branch",
	"remote": "remote", "remotes": "remote",
	"stash": "stash", "stashes": "stash",
	"worktree": "worktree", "worktrees": "worktree",
	"note": "note", "notes": "note",
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
	claim = strings.TrimSpace(strings.ToLower(claim))
	// The negative-claim reader captures "no LICENSE file" as the subject
	// "license file". The trailing noun names the artifact, not a different
	// claim, so it must not defeat the root-file vocabulary (session 324
	// reviewed a true root absence because of it).
	for _, suffix := range []string{" file", " files", " document"} {
		if strings.HasSuffix(claim, suffix) {
			claim = strings.TrimSpace(strings.TrimSuffix(claim, suffix))
			break
		}
	}
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
			// A structured absence observation is evidence even when the command
			// exited non-zero: `git log` on an unborn branch reports "does not
			// have any commits yet" on stderr, and the tool publishes that fact
			// rather than the model's reading of it. The generic error filter
			// below stays in force for everything else.
			if outcome.ToolName == "bash" {
				if details, ok := outcome.Result.Details.(map[string]any); ok {
					for _, namespace := range detailStringList(details, "git_absence") {
						if coverage.vcsAbsence == nil {
							coverage.vcsAbsence = map[string]bool{}
						}
						coverage.vcsAbsence[namespace] = true
					}
				}
			}
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
			if details, ok := outcome.Result.Details.(map[string]any); ok {
				coverage.observedPaths = append(coverage.observedPaths, detailStringList(details, "paths")...)
			}
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
				structureDetails, _ := outcome.Result.Details.(map[string]any)
				if detailValueString(structureDetails, "view") == "tests" {
					coverage.testInventoryObserved = true
				}
				if count := detailValueInt(structureDetails, "test_files"); count > coverage.testFiles {
					coverage.testFiles = count
				}
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

// detailStringList returns a structured string list from tool details. A tool
// that publishes no list contributes nothing, never an empty observation.
func detailStringList(details map[string]any, key string) []string {
	switch value := details[key].(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
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
