package agent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/contract"
)

// CompletionState is the runtime's verdict on whether a run reached its
// objective.
//
// It is deliberately not the model's stop reason: a model can stop cleanly
// after a failed call or without changing anything. Only evidence the runtime
// observed can move a task to COMPLETED.
type CompletionState string

// Completion states.
const (
	// CompletionComplete means the runtime observed the requested result:
	// no unrecovered failure, no work left mid-call, and any change was
	// followed by a successful verification.
	CompletionComplete CompletionState = "COMPLETED"
	// CompletionNeedsReview means the run ended without enough evidence to
	// call the objective complete. It is not a failure and not a completion:
	// a later turn may resolve it.
	CompletionNeedsReview CompletionState = "NEEDS_REVIEW"
)

// Completion is a completion verdict with its evidence reason.
type Completion struct {
	// State is the verdict.
	State CompletionState
	// Reason explains a NEEDS_REVIEW verdict, in terms of observed evidence.
	Reason string
}

// Complete reports whether the verdict is COMPLETED.
func (c Completion) Complete() bool { return c.State == CompletionComplete }

// EvaluateCompletion derives a completion verdict from the turns a run
// produced.
//
// mutationIntent says the user's request clearly asked for a repository change.
// It is false for read-only and execution-only requests. Even when it is false,
// an observed mutation still requires verification: the runtime must not let a
// model mutate during a read request and then call the turn done.
//
// The rules are deliberately small and deterministic:
//
//   - an empty run, an execution error, or a final tool-calling turn is not
//     complete;
//   - a failed call must be followed by a successful call of the same tool or
//     by a successful verification, otherwise it remains a failure;
//   - a mutation request with no definite observed change is not complete;
//   - a change or an arbitrary process execution with no successful
//     verification after it is not complete.
func EvaluateCompletion(turns []Turn, mutationIntent bool) Completion {
	if len(turns) == 0 {
		return needsReview("the run produced no completed turn")
	}
	for _, turn := range turns {
		if turn.Err != nil {
			return needsReview("the run ended with an execution error: %v", turn.Err)
		}
	}
	last := turns[len(turns)-1]
	if len(last.Results) > 0 {
		return needsReview("the run stopped with tool calls still outstanding")
	}

	evidence := scanCompletionEvidence(turns, mutationIntent)
	if len(evidence.pendingFailures) > 0 {
		return needsReview("a tool call failed and no later success or verification recovered it")
	}
	if mutationIntent && !evidence.mutated {
		return needsReview("the request asked for a repository change but no change was observed")
	}
	if evidence.lastVerification < evidence.lastPotentialMutation {
		return needsReview("the repository change or execution was not followed by a successful verification")
	}
	return Completion{State: CompletionComplete}
}

// CompletionEvidence reports the definite mutation and verification evidence a
// run produced. It is used when a delegated child's result becomes the parent's
// tool outcome: the parent must see the child's change and whether the child
// verified it, rather than inheriting a clean-looking answer.
//
// mutationIntent controls whether an arbitrary process execution counts as a
// potential change that needs verification. It does not turn an execution into
// a definite mutation: a child that only ran tests did not change the
// repository, and the parent's mutation objective still needs a real change.
func CompletionEvidence(turns []Turn, mutationIntent bool) (mutated, verifiedAfterMutation bool) {
	evidence := scanCompletionEvidence(turns, mutationIntent)
	if !evidence.mutated {
		return false, false
	}
	return true, evidence.lastVerification >= evidence.lastPotentialMutation
}

type completionEvidence struct {
	// mutated is a definite change: a write/edit/delete, or a shell command
	// classified as mutating.
	mutated bool
	// lastPotentialMutation is the last definite change, or (for a mutation
	// objective) the last arbitrary execution, because an arbitrary process
	// may have changed something the runtime could not classify.
	lastPotentialMutation int
	lastVerification      int
	pendingFailures       map[string]int
	// verifications records every successful check and the scope it covered.
	// The position is the outcome's index in the run, so a check that happened
	// before the last change can be excluded.
	verifications []verificationRecord
}

// verificationRecord is one successful check at a run position.
type verificationRecord struct {
	position int
	evidence *VerificationEvidence
}

// scanCompletionEvidence walks the turns once, recording the last observed
// change and verification and which tool failures remain unrecovered.
//
// A failure is recovered by a later success of the same tool (a retry that
// worked) or by a successful verification (the final state was checked). A
// verification after a failure therefore clears every pending failure, while a
// failure with no later evidence stays pending.
func scanCompletionEvidence(turns []Turn, mutationIntent bool) completionEvidence {
	evidence := completionEvidence{
		lastPotentialMutation: -1,
		lastVerification:      -1,
		pendingFailures:       map[string]int{},
	}
	position := 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError {
				evidence.pendingFailures[outcome.ToolName]++
				position++
				continue
			}

			definite := outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates)
			if definite {
				evidence.mutated = true
				evidence.lastPotentialMutation = position
			} else if mutationIntent && outcome.Result.Executes {
				// The runtime cannot prove this process did not change the
				// repository. A mutation objective must verify after it.
				evidence.lastPotentialMutation = position
			}
			if outcome.Result.Verifies {
				evidence.lastVerification = position
				evidence.verifications = append(evidence.verifications, verificationRecord{
					position: position,
					evidence: outcome.Result.Verification,
				})
				// A successful verification is evidence that the final state
				// is sound, so earlier failures no longer block completion.
				evidence.pendingFailures = map[string]int{}
			}
			if evidence.pendingFailures[outcome.ToolName] > 0 {
				evidence.pendingFailures[outcome.ToolName]--
				if evidence.pendingFailures[outcome.ToolName] == 0 {
					delete(evidence.pendingFailures, outcome.ToolName)
				}
			}
			position++
		}
	}
	return evidence
}

func needsReview(format string, args ...any) Completion {
	return Completion{State: CompletionNeedsReview, Reason: fmt.Sprintf(format, args...)}
}

// CompletionInput is the evidence a task-aware completion decision uses.
//
// Turns are observations from the loop. The contract is the bounded expectation
// extracted from the user's request. Snapshot is the pre-run state of the
// contract targets; Resolver is the existing structural index used to
// re-locate symbols after the run. None of these can authorize execution.
type CompletionInput struct {
	// Turns are the completed loop turns.
	Turns []Turn
	// Contract is the user intent contract.
	Contract contract.IntentContract
	// Snapshot is the pre-run target state, or nil when none was captured.
	Snapshot *TargetSnapshot
	// Resolver locates symbols and packages after the run.
	Resolver StructuralResolver
	// Root is the workspace root, used when no snapshot carries one.
	Root string
}

// Alignment is the bounded semantic verdict.
type Alignment string

// Alignment values.
const (
	// AlignmentNotApplicable means the request had no mutation or check that
	// needs a semantic judgement.
	AlignmentNotApplicable Alignment = "not_applicable"
	// AlignmentAligned means deterministic evidence matches the contract.
	AlignmentAligned Alignment = "aligned"
	// AlignmentUncertain means the evidence cannot establish alignment.
	AlignmentUncertain Alignment = "uncertain"
	// AlignmentMisaligned means the evidence contradicts the contract.
	AlignmentMisaligned Alignment = "misaligned"
)

// Assessment is the semantic layer's result. It is untrusted interpretation of
// deterministic evidence: it can only downgrade completion, never authorize an
// action or stand in for a missing target change.
type Assessment struct {
	// Alignment is the verdict.
	Alignment Alignment
	// Rationale explains the verdict in evidence terms.
	Rationale string
	// Evidence is the bounded evidence set the verdict used.
	Evidence []string
}

// BehavioralEvidence is what the successful checks actually covered.
type BehavioralEvidence struct {
	// RequiredScopes are the package directories the contract and targets
	// require the checks to cover.
	RequiredScopes []string
	// CoveredScopes are the required scopes a successful check covered.
	CoveredScopes []string
	// MissingScopes are the required scopes no successful check covered.
	MissingScopes []string
	// Verifications counts successful checks after the last change.
	Verifications int
	// Satisfied reports whether every behavioural requirement was met.
	Satisfied bool
	// Detail explains a failure.
	Detail string
}

// EvaluateTaskCompletion derives a completion verdict from a task contract and
// runtime evidence.
//
// It extends EvaluateCompletion: the run-shape and failure rules are identical,
// and the evidence scan is shared. The additions are deterministic target
// checks, verification-scope matching, and a bounded semantic assessment that
// can only downgrade the result. Semantic uncertainty is always NEEDS_REVIEW.
func EvaluateTaskCompletion(input CompletionInput) Completion {
	if len(input.Turns) == 0 {
		return needsReview("the run produced no completed turn")
	}
	for _, turn := range input.Turns {
		if turn.Err != nil {
			return needsReview("the run ended with an execution error: %v", turn.Err)
		}
	}
	last := input.Turns[len(input.Turns)-1]
	if len(last.Results) > 0 {
		return needsReview("the run stopped with tool calls still outstanding")
	}

	intent := input.Contract
	if intent.Kind == "" {
		return needsReview("no intent contract was supplied")
	}
	evidence := scanCompletionEvidence(input.Turns, intent.Kind == contract.ObjectiveMutation)
	if len(evidence.pendingFailures) > 0 {
		return needsReview("a tool call failed and no later success or verification recovered it")
	}
	if intent.Kind == contract.ObjectiveMutation && !evidence.mutated {
		return needsReview("the request asked for a repository change but no change was observed")
	}
	if evidence.lastVerification < evidence.lastPotentialMutation {
		return needsReview("the repository change or execution was not followed by a successful verification")
	}
	if intent.Kind != contract.ObjectiveMutation && evidence.mutated {
		return needsReview("the request did not ask for a repository change but a change was observed")
	}

	root := input.Root
	if input.Snapshot != nil && input.Snapshot.Root != "" {
		root = input.Snapshot.Root
	}

	var structural StructuralEvidence
	if intent.Kind == contract.ObjectiveMutation && len(intent.Targets) > 0 {
		structural = CheckTargets(input.Snapshot, input.Resolver)
	}
	unexpected := UnexpectedMutations(root, input.Turns, intent, input.Snapshot)
	if len(unexpected) > 0 {
		structural.Unexpected = uniqueSorted(append(structural.Unexpected, unexpected...))
	}
	behavioral := AssessBehavior(intent, input.Snapshot, evidence,
		goSourceDirectories(ObservedMutationPaths(root, input.Turns, input.Snapshot)))
	assessment := AssessAlignment(intent, structural, behavioral)

	var reasons []string
	if intent.Kind == contract.ObjectiveMutation && len(intent.Targets) > 0 {
		for _, issue := range structural.Issues {
			reasons = append(reasons, "target evidence is incomplete: "+issue)
		}
		for _, check := range structural.Checks {
			if check.Status != TargetSatisfied {
				reasons = append(reasons, check.Detail)
			}
		}
	}
	if len(structural.Unexpected) > 0 {
		reasons = append(reasons, "observed change outside the requested scope: "+
			strings.Join(structural.Unexpected, ", "))
	}
	if !behavioral.Satisfied {
		reasons = append(reasons, behavioral.Detail)
	}
	if intent.Ambiguous {
		detail := "the request does not name a verifiable target or behavioural check"
		if len(intent.Issues) > 0 {
			detail = intent.Issues[0]
		}
		reasons = append(reasons, detail)
	}
	if assessment.Alignment == AlignmentUncertain || assessment.Alignment == AlignmentMisaligned {
		reasons = append(reasons, assessment.Rationale)
		for _, item := range assessment.Evidence {
			already := false
			for _, reason := range reasons {
				if strings.Contains(reason, item) {
					already = true
					break
				}
			}
			if !already {
				reasons = append(reasons, "evidence: "+item)
			}
		}
	}
	if len(reasons) > 0 {
		return needsReview("%s", boundedReason(strings.Join(orderedUnique(reasons), "; ")))
	}
	return Completion{State: CompletionComplete}
}

// verificationNeed is one check the contract requires: a scope (empty for a
// generic check) and the kind of check that can satisfy it.
type verificationNeed struct {
	scope string
	check contract.VerificationCheck
}

// AssessBehavior matches successful checks to the contract's scopes and check
// kinds.
//
// changedDirs are the packages the run actually changed. They let a targetless
// behavioural objective ("fix the failing tests") be bounded by the code that
// changed instead of by any check anywhere in the repository.
func AssessBehavior(intent contract.IntentContract, snapshot *TargetSnapshot, evidence completionEvidence, changedDirs []string) BehavioralEvidence {
	result := BehavioralEvidence{Satisfied: true}
	after := evidence.verificationsAfter(evidence.lastPotentialMutation)
	result.Verifications = len(after)

	var needs []verificationNeed
	for _, requirement := range intent.Verification {
		need := verificationNeed{check: requirement.EffectiveCheck()}
		if requirement.Kind == contract.VerifyScope {
			need.scope = requirement.Scope
		}
		needs = append(needs, need)
	}
	primary := primaryVerificationCheck(intent.Verification)
	for _, scope := range targetVerificationScopes(intent, snapshot) {
		needs = append(needs, verificationNeed{scope: scope, check: primary})
	}
	needs = uniqueNeeds(needs)

	if len(after) == 0 {
		switch {
		case intent.Kind == contract.ObjectiveMutation:
			result.Satisfied = false
			result.Detail = "no successful verification followed the last change"
			return result
		case len(needs) > 0:
			result.Satisfied = false
			result.Detail = "the requested check did not run successfully"
			return result
		}
	}

	for _, need := range needs {
		scopes := []string{need.scope}
		if need.scope == "*" && len(changedDirs) > 0 {
			// A wildcard from a targetless behavioural objective is bounded by
			// the packages the run changed. With no attributable change, it
			// still means the whole repository.
			scopes = changedDirs
		}
		for _, scope := range scopes {
			covered := false
			for _, record := range after {
				if !checkCovers(record.evidence.EffectiveCheck(), need.check) {
					continue
				}
				if scope == "" || record.evidence.Covers(scope) {
					covered = true
					break
				}
			}
			label := verificationLabel(scope, need.check)
			if scope != "" {
				result.RequiredScopes = append(result.RequiredScopes, label)
			}
			if covered {
				if scope != "" {
					result.CoveredScopes = append(result.CoveredScopes, label)
				}
			} else {
				result.MissingScopes = append(result.MissingScopes, label)
			}
		}
	}
	result.RequiredScopes = uniqueSorted(result.RequiredScopes)
	result.CoveredScopes = uniqueSorted(result.CoveredScopes)
	result.MissingScopes = uniqueSorted(result.MissingScopes)
	if len(result.MissingScopes) > 0 {
		result.Satisfied = false
		result.Detail = "the successful verification did not cover " +
			strings.Join(result.MissingScopes, ", ")
	}
	return result
}

// targetVerificationScopes derives the directories a successful check must
// cover from the Go targets. Explicit requirements are handled separately.
func targetVerificationScopes(intent contract.IntentContract, snapshot *TargetSnapshot) []string {
	var scopes []string
	if snapshot == nil {
		for _, target := range intent.Targets {
			if target.Kind == contract.TargetPath {
				if dir := goDirectory(target.Value); dir != "" {
					scopes = append(scopes, dir)
				}
			}
		}
		return uniqueSorted(scopes)
	}
	for _, entry := range snapshot.Entries {
		switch entry.Target.Kind {
		case contract.TargetPath:
			if dir := goDirectory(entry.Target.Value); dir != "" {
				scopes = append(scopes, dir)
			}
		case contract.TargetSymbol:
			for _, state := range entry.Symbols {
				if dir := goDirectory(state.Location.Path); dir != "" {
					scopes = append(scopes, dir)
				}
			}
		case contract.TargetPackage:
			for _, state := range entry.Files {
				if dir := goDirectory(state.Path); dir != "" {
					scopes = append(scopes, dir)
				}
			}
		}
	}
	return uniqueSorted(scopes)
}

// primaryVerificationCheck picks the most specific check kind the request
// named, for the target-derived scopes. A request that only says "verify" uses
// CheckAny.
func primaryVerificationCheck(requirements []contract.VerificationRequirement) contract.VerificationCheck {
	for _, requirement := range requirements {
		if check := requirement.EffectiveCheck(); check != contract.CheckAny {
			return check
		}
	}
	return contract.CheckAny
}

// uniqueNeeds removes duplicate scope/check pairs while preserving order.
func uniqueNeeds(needs []verificationNeed) []verificationNeed {
	seen := map[verificationNeed]bool{}
	var out []verificationNeed
	for _, need := range needs {
		if !seen[need] {
			seen[need] = true
			out = append(out, need)
		}
	}
	return out
}

// verificationLabel names one requirement in a review reason.
func verificationLabel(scope string, check contract.VerificationCheck) string {
	if scope == "" {
		return "a " + string(check) + " check"
	}
	if check == contract.CheckAny {
		return scope
	}
	return scope + " (" + string(check) + ")"
}

// AssessAlignment is the bounded semantic layer. It runs after deterministic
// evidence has been collected and can only downgrade a completion verdict.
func AssessAlignment(intent contract.IntentContract, structural StructuralEvidence, behavioral BehavioralEvidence) Assessment {
	assessment := Assessment{Alignment: AlignmentNotApplicable}
	add := func(text string) {
		if len(assessment.Evidence) < 8 {
			assessment.Evidence = append(assessment.Evidence, text)
		}
	}

	switch intent.Kind {
	case contract.ObjectiveInformational:
		if len(structural.Unexpected) > 0 {
			assessment.Alignment = AlignmentMisaligned
			assessment.Rationale = "the request was informational but the run changed the repository"
			add("unexpected change: " + strings.Join(structural.Unexpected, ", "))
			return assessment
		}
		assessment.Rationale = "the request asked for information; no mutation assessment is required"
		return assessment

	case contract.ObjectiveExecution:
		if len(structural.Unexpected) > 0 {
			assessment.Alignment = AlignmentMisaligned
			assessment.Rationale = "the request asked for execution only but the run changed the repository"
			add("unexpected change: " + strings.Join(structural.Unexpected, ", "))
			return assessment
		}
		if len(intent.Verification) > 0 {
			if !behavioral.Satisfied {
				assessment.Alignment = AlignmentUncertain
				assessment.Rationale = firstNonEmptyString(behavioral.Detail,
					"the requested check did not provide the evidence the request named")
				add(behavioral.Detail)
				if len(behavioral.MissingScopes) > 0 {
					add("missing verification scope: " + strings.Join(behavioral.MissingScopes, ", "))
				}
				return assessment
			}
			assessment.Alignment = AlignmentAligned
			assessment.Rationale = "the requested check ran successfully"
			add(fmt.Sprintf("verification count: %d", behavioral.Verifications))
			if len(behavioral.CoveredScopes) > 0 {
				add("verification covered: " + strings.Join(behavioral.CoveredScopes, ", "))
			}
			return assessment
		}
		assessment.Rationale = "the request asked for execution without a completion check"
		return assessment
	}

	// Mutation objective.
	for _, issue := range structural.Issues {
		assessment.Alignment = AlignmentUncertain
		assessment.Rationale = "target evidence is incomplete: " + issue
		add(issue)
	}
	for _, check := range structural.Checks {
		switch check.Status {
		case TargetSatisfied:
			add("satisfied: " + string(check.Target.Kind) + " " + check.Target.Value)
			continue
		case TargetUnresolved:
			assessment.Alignment = AlignmentUncertain
			assessment.Rationale = firstNonEmptyString(check.Detail, "a target could not be verified")
			add(check.Detail)
			continue
		default:
			assessment.Alignment = AlignmentMisaligned
			assessment.Rationale = firstNonEmptyString(check.Detail, "the observed change did not match the request")
			add(check.Detail)
		}
	}
	if len(structural.Unexpected) > 0 {
		assessment.Alignment = AlignmentMisaligned
		assessment.Rationale = "the run changed a path outside the requested target scope"
		add("unexpected: " + strings.Join(structural.Unexpected, ", "))
	}
	if assessment.Alignment != AlignmentNotApplicable {
		return assessment
	}
	if intent.Ambiguous {
		assessment.Alignment = AlignmentUncertain
		assessment.Rationale = "the request does not name a verifiable target or behavioural check"
		return assessment
	}
	if len(intent.Targets) == 0 && !intent.BehavioralObjective {
		assessment.Alignment = AlignmentUncertain
		assessment.Rationale = "the request does not name a target or a behavioural oracle"
		return assessment
	}
	if !behavioral.Satisfied {
		assessment.Alignment = AlignmentUncertain
		assessment.Rationale = firstNonEmptyString(behavioral.Detail, "the verification evidence is incomplete")
		if len(behavioral.MissingScopes) > 0 {
			add("missing verification scope: " + strings.Join(behavioral.MissingScopes, ", "))
		}
		return assessment
	}
	if len(structural.Checks) == 0 && !intent.BehavioralObjective {
		assessment.Alignment = AlignmentUncertain
		assessment.Rationale = "no target evidence was captured for the requested change"
		return assessment
	}
	assessment.Alignment = AlignmentAligned
	assessment.Rationale = "the observed change matched the requested target and the verification covered it"
	if len(behavioral.RequiredScopes) > 0 {
		add("required verification scopes: " + strings.Join(behavioral.RequiredScopes, ", "))
	}
	if len(behavioral.CoveredScopes) > 0 {
		add("verification covered: " + strings.Join(behavioral.CoveredScopes, ", "))
	}
	return assessment
}

// verificationsAfter returns successful checks at or after a run position.
func (e completionEvidence) verificationsAfter(position int) []verificationRecord {
	var out []verificationRecord
	for _, record := range e.verifications {
		if record.position >= position {
			out = append(out, record)
		}
	}
	return out
}

// goDirectory maps a Go source path to the package directory a check must
// cover. It returns "" for a non-Go target.
func goDirectory(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if !strings.HasSuffix(value, ".go") {
		return ""
	}
	dir := filepath.ToSlash(filepath.Dir(value))
	if dir == "" || dir == "." {
		return "."
	}
	return dir
}

// goSourceDirectories returns the unique package directories of changed Go
// files. A non-Go change contributes no package scope.
func goSourceDirectories(paths []string) []string {
	var dirs []string
	for _, path := range paths {
		if dir := goDirectory(path); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return uniqueSorted(dirs)
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func orderedUnique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func boundedReason(reason string) string {
	const limit = 512
	runes := []rune(strings.TrimSpace(reason))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-3]) + "..."
}
