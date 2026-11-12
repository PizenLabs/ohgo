package cognition

import (
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// Stage is the projected position of a task in its evidence lifecycle.
//
// It is not a workflow instruction. It describes what the current evidence can
// support: more exploration, an action, a verification, an answer, or nothing
// further. Candidate selection reads it; the model still chooses the specific
// action.
type Stage string

// Task stages.
const (
	// StageExplore means required evidence is missing.
	StageExplore Stage = "explore"
	// StageAct means the evidence supports the requested action.
	StageAct Stage = "act"
	// StageVerify means a change or execution needs a successful check.
	StageVerify Stage = "verify"
	// StageAnswer means an informational task has enough evidence to answer.
	StageAnswer Stage = "answer"
	// StageComplete means the objective's evidence conditions are satisfied.
	StageComplete Stage = "complete"
	// StageBlocked means no further progress is possible in this run.
	StageBlocked Stage = "blocked"
)

// SufficiencyState is whether the current evidence can support progress.
type SufficiencyState string

// Sufficiency verdicts.
const (
	// Sufficient means the current objective can proceed.
	Sufficient SufficiencyState = "SUFFICIENT"
	// Insufficient means required evidence is missing.
	Insufficient SufficiencyState = "INSUFFICIENT"
	// Conflicting means observations disagree and must be resolved.
	Conflicting SufficiencyState = "CONFLICTING"
	// Blocked means the run cannot make progress.
	Blocked SufficiencyState = "BLOCKED"
)

// BlockKind explains why sufficiency is blocked, so the projection can choose
// between keeping a truthful observation surface and closing every action.
type BlockKind string

// Block kinds.
const (
	// BlockNone means the task is not blocked.
	BlockNone BlockKind = ""
	// BlockBudget means the root grant is exhausted.
	BlockBudget BlockKind = "budget"
	// BlockUnauthorizedMutation means a change occurred under an objective that
	// did not permit one.
	BlockUnauthorizedMutation BlockKind = "unauthorized_mutation"
	// BlockScopeViolation means a change fell outside the requested scope.
	BlockScopeViolation BlockKind = "scope_violation"
	// BlockCapabilityFailure means a required capability failed and the
	// remaining evidence cannot satisfy the objective. No further tool can
	// make progress, so the next request is for an answer or a review report.
	BlockCapabilityFailure BlockKind = "capability_failure"
	// BlockProgressStalled means consecutive completed turns did not advance
	// the task: no change, verification, requirement, or explicit blocker
	// moved the evidence boundary. The next request is a report or review,
	// not another observation. It is a semantic bound, not a resource bound.
	BlockProgressStalled BlockKind = "progress_stalled"
)

// MaxConsecutiveNoProgress is the semantic circuit breaker for a run that keeps
// observing without changing the task.
//
// A turn is progress when it applies a change, observes a successful check,
// changes the sufficiency stage or the unmet requirements, or establishes an
// explicit blocker. During the verification stage, new evidence alone is not
// progress: the task already has a change waiting for its check, and another
// read cannot close that obligation. The bound is deliberately independent of
// the model-turn budget; it stops the repeated-observation shape long before a
// resource cap would, while a task that keeps changing state is never stopped
// by this counter.
const MaxConsecutiveNoProgress = 4

// VerificationRecord is one successful check at a run position.
type VerificationRecord struct {
	Position int
	Check    contract.VerificationCheck
	Scopes   []string
}

// ExecutionState is the runtime's summary of what the run has done.
//
// It is derived from tool outcomes, never from model narration. It is used for
// projection and sufficiency; the completion evaluator remains the authority
// on whether a task is PROVEN.
type ExecutionState struct {
	// ToolCalls counts the outcomes observed.
	ToolCalls int
	// FailedActions counts failed outcomes.
	FailedActions int
	// Mutated reports a definite repository change.
	Mutated bool
	// MutatedPaths are the attributable changed paths.
	MutatedPaths []string
	// MutatedUnattributed reports a mutation whose path could not be named.
	MutatedUnattributed bool
	// LastMutation is the outcome position of the last change.
	LastMutation int
	// LastVerification is the outcome position of the last successful check.
	LastVerification int
	// LastExecution is the outcome position of the last successful terminal
	// process.
	LastExecution int
	// VerifiedAfterMutation reports a successful check after the last change.
	VerifiedAfterMutation bool
	// VerifiedAfterExecution reports a successful check after the last
	// successful execution.
	VerifiedAfterExecution bool
	// Executed reports a successful terminal process.
	Executed bool
	// RuntimeObserved reports a live project runtime: a service reached
	// readiness, at least one check passed against it while it was alive, and
	// it did not exit. It is the observation a runtime execution objective
	// requires; Executed alone is not enough for it.
	RuntimeObserved bool
	// BackgroundStarted reports a detached job that has not been collected.
	BackgroundStarted bool
	// PendingFailures counts unrecovered failures per tool.
	PendingFailures map[string]int
	// Verifications records the successful checks in run order.
	Verifications []VerificationRecord
}

// SummarizeExecution folds completed turns into an ExecutionState.
func SummarizeExecution(turns []agent.Turn) ExecutionState {
	state := ExecutionState{
		LastMutation:     -1,
		LastVerification: -1,
		LastExecution:    -1,
		PendingFailures:  map[string]int{},
	}
	position := 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			state.ToolCalls++
			if outcome.Result.IsError {
				if !outcome.Refused() && !outcome.Result.ObservedAbsence {
					state.FailedActions++
					state.PendingFailures[outcome.ToolName]++
				}
				position++
				continue
			}
			definite := outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates)
			if definite {
				state.Mutated = true
				state.LastMutation = position
				if path := outcomePath(outcome); path != "" {
					state.MutatedPaths = appendUnique(state.MutatedPaths, path)
				} else {
					state.MutatedUnattributed = true
				}
			}
			if outcome.Result.Executes && outcome.Result.Finished {
				state.Executed = true
				state.LastExecution = position
			}
			if outcome.Result.Runtime != nil {
				state.RuntimeObserved = true
				state.Executed = true
				state.LastExecution = position
			}
			if outcome.Result.Executes && !outcome.Result.Finished {
				state.BackgroundStarted = true
			}
			if outcome.Result.Verifies {
				state.Executed = true
				state.LastVerification = position
				record := VerificationRecord{Position: position, Check: outcome.Result.Verification.EffectiveCheck()}
				if outcome.Result.Verification != nil {
					for _, scope := range outcome.Result.Verification.Scopes {
						if scope.All {
							record.Scopes = append(record.Scopes, "*")
							continue
						}
						record.Scopes = append(record.Scopes, scope.Path)
					}
				}
				state.Verifications = append(state.Verifications, record)
				// A successful check proves the current final state is sound,
				// so earlier failures no longer block the next action.
				state.PendingFailures = map[string]int{}
			}
			if state.PendingFailures[outcome.ToolName] > 0 {
				// A later success of the same capability supersedes its earlier
				// failures, regardless of how many failed probes preceded it.
				delete(state.PendingFailures, outcome.ToolName)
			}
			position++
		}
	}
	sort.Strings(state.MutatedPaths)
	state.VerifiedAfterMutation = state.Mutated && state.LastVerification >= state.LastMutation
	state.VerifiedAfterExecution = state.LastExecution >= 0 && state.LastVerification >= state.LastExecution
	return state
}

// ExecutionFromEvidence reconstructs the durable execution state from the
// evidence a task has observed.
//
// A resumed run starts with no live turns, but its evidence still records the
// change a prior run applied and any check that followed it. Deriving the
// execution state from that evidence is what keeps a continuation in the
// verification stage instead of treating the task as if nothing had happened.
// The reconstruction is deliberately conservative: an item is a change only
// when it was recorded as a mutation, and a check only when it carries the
// verification marker the producing command reported.
func ExecutionFromEvidence(state *EvidenceState) ExecutionState {
	out := ExecutionState{
		LastMutation:     -1,
		LastVerification: -1,
		LastExecution:    -1,
		PendingFailures:  map[string]int{},
	}
	if state == nil {
		return out
	}
	for _, item := range state.Items {
		if item.Status == StatusRequired || item.Status == StatusUnknown {
			continue
		}
		out.ToolCalls++
		switch {
		case isMutationEvidence(item):
			out.Mutated = true
			out.LastMutation = maxInt(out.LastMutation, item.Seq)
			if path := cleanPath(item.Source.Path); path != "" {
				out.MutatedPaths = appendUnique(out.MutatedPaths, path)
			} else {
				out.MutatedUnattributed = true
			}
		case isVerificationEvidenceItem(item):
			check := contract.VerificationCheck(item.Source.Check)
			if check == "" {
				check = contract.CheckAny
			}
			scope := item.Source.Scope
			record := VerificationRecord{Position: item.Seq, Check: check}
			if scope != "" {
				record.Scopes = append(record.Scopes, scope)
			}
			out.Verifications = append(out.Verifications, record)
			out.LastVerification = maxInt(out.LastVerification, item.Seq)
		}
		if item.Executed {
			out.Executed = true
			out.LastExecution = maxInt(out.LastExecution, item.Seq)
		}
		if item.Runtime {
			out.RuntimeObserved = true
			out.Executed = true
			out.LastExecution = maxInt(out.LastExecution, item.Seq)
		}
		if strings.HasPrefix(item.ID, "background:") {
			out.BackgroundStarted = true
		}
	}
	for _, failure := range state.FailureState().Records {
		if failure.Failures > 0 {
			out.PendingFailures[failure.Tool] = failure.Failures
		}
	}
	sort.Strings(out.MutatedPaths)
	out.VerifiedAfterMutation = out.Mutated && out.LastVerification >= out.LastMutation
	out.VerifiedAfterExecution = out.LastExecution >= 0 && out.LastVerification >= out.LastExecution
	return out
}

func isMutationEvidence(item EvidenceItem) bool {
	return strings.HasPrefix(item.ID, "mutation:")
}

func isVerificationEvidenceItem(item EvidenceItem) bool {
	return item.Source.Check != "" || strings.HasPrefix(item.ID, "verification:")
}

func maxInt(a, b int) int {
	if b > a {
		return b
	}
	return a
}

// Sufficiency is the runtime's judgement about the current evidence.
type Sufficiency struct {
	// State is the verdict.
	State SufficiencyState
	// Stage is what the evidence supports.
	Stage Stage
	// Kind explains a Blocked verdict.
	Kind BlockKind
	// Reasons explains the verdict in evidence terms.
	Reasons []string
	// Missing names the requirements that are not satisfied.
	Missing []string
	// Next names the highest-value evidence to acquire next, when insufficient.
	Next []string
}

// EvaluateSufficiency decides whether the current evidence supports progress.
//
// The rule order is the invariant: a conflict is resolved before new work, a
// required fact is acquired before acting, a change is verified before it is
// called complete, and a resource stop is reported rather than hidden.
func EvaluateSufficiency(shape TaskShape, evidence *EvidenceState, execution ExecutionState, budgetExhausted bool) Sufficiency {
	result := Sufficiency{State: Sufficient}
	if budgetExhausted {
		result.State = Blocked
		result.Stage = StageBlocked
		result.Kind = BlockBudget
		result.Reasons = append(result.Reasons, "the root budget is exhausted")
		return result
	}
	if evidence == nil {
		evidence = NewEvidenceState()
	}
	failures := evidence.FailureState()
	// The shape declares the needs; sufficiency must consider them even when a
	// caller evaluated the verdict before any turn was observed. Without this,
	// an empty state looked "sufficient" and the candidate layer withheld every
	// tool from a repository question that had read nothing.
	evidence.ensureRequirements(shape)
	if conflicts := evidence.Conflicts(); len(conflicts) > 0 {
		result.State = Conflicting
		result.Stage = StageExplore
		for _, conflict := range conflicts {
			result.Reasons = append(result.Reasons, "conflicting evidence: "+conflict.Summary)
			if conflict.Conflict != "" {
				result.Reasons = append(result.Reasons, conflict.Conflict)
			}
		}
		result.Missing = append(result.Missing, "resolve the conflicting observation")
		result.Next = append(result.Next, "re-read the changed path or run the authoritative check")
		return result
	}

	// A mutation the task shape did not permit is a review boundary even when
	// the workspace authority allowed the call. The task objective, not just
	// the workspace, defines what may change; the projection must say so
	// rather than continuing as if an unauthorized change were ordinary
	// progress.
	if !shape.Mutation.Allowed && execution.Mutated {
		result.State = Blocked
		result.Stage = StageBlocked
		result.Kind = BlockUnauthorizedMutation
		result.Reasons = append(result.Reasons, "a repository mutation was observed but the task shape does not permit one")
		result.Next = append(result.Next, "report the unexpected change and require review")
		return result
	}

	// A change outside an explicit scope is a review boundary, not a reason to
	// keep exploring. The model may have done real work, but the runtime cannot
	// claim the request's bounded objective was met.
	if shape.Mutation.Allowed && shape.Mutation.UnrelatedForbidden {
		if execution.MutatedUnattributed {
			result.State = Blocked
			result.Stage = StageBlocked
			result.Kind = BlockScopeViolation
			result.Reasons = append(result.Reasons, "a mutation could not be attributed to a requested path")
			result.Next = append(result.Next, "report the unattributed change and require review")
			return result
		}
		for _, path := range execution.MutatedPaths {
			if !containsString(shape.Mutation.Paths, path) {
				result.State = Blocked
				result.Stage = StageBlocked
				result.Kind = BlockScopeViolation
				result.Reasons = append(result.Reasons, "a change outside the requested scope was observed: "+path)
				result.Next = append(result.Next, "report the scope violation; do not continue mutating")
				return result
			}
		}
	}

	missing := missingForAction(shape, evidence, execution)
	if len(missing) > 0 {
		// A request that explicitly needs external knowledge cannot recover by
		// guessing capability names forever. Different failed capabilities are
		// legitimate recovery attempts up to a bounded number; after that the
		// required capability is treated as unavailable and the task closes.
		if shape.External.Required && failures.ExternalFailures() >= MaxRequiredExternalFailures {
			result.State = Blocked
			result.Stage = StageBlocked
			result.Kind = BlockCapabilityFailure
			result.Reasons = append(result.Reasons,
				"the required external capability failed repeatedly without an observation")
			for _, requirement := range missing {
				result.Missing = append(result.Missing, requirement.Key)
			}
			result.Next = append(result.Next,
				"report the unavailable external capability and the evidence gap; do not guess another capability")
			return result
		}
		// A required capability that failed non-retryably and left the
		// objective's evidence missing is a terminal boundary: another model
		// turn cannot make progress by repeating it. The runtime closes the
		// action surface and asks for an answer or a review report instead.
		if failures.Blocking != nil {
			result.State = Blocked
			result.Stage = StageBlocked
			result.Kind = BlockCapabilityFailure
			result.Reasons = append(result.Reasons, failureRecordReason(*failures.Blocking))
			for _, requirement := range missing {
				result.Missing = append(result.Missing, requirement.Key)
				if requirement.Reason != "" {
					result.Reasons = append(result.Reasons, requirement.Reason)
				}
			}
			result.Next = append(result.Next,
				"report the failed capability and the evidence gap; do not repeat the failed call")
			return result
		}
		result.State = Insufficient
		result.Stage = StageExplore
		for _, requirement := range missing {
			result.Missing = append(result.Missing, requirement.Key)
			result.Next = append(result.Next, nextEvidence(requirement.Key))
			if requirement.Reason != "" {
				result.Reasons = append(result.Reasons, requirement.Reason)
			}
		}
		for _, record := range failures.Records {
			result.Reasons = append(result.Reasons, failureRecordReason(record))
			if record.Refuses() {
				result.Next = append(result.Next,
					"do not repeat "+record.Tool+" with the same arguments; change the capability or the arguments")
			}
		}
		return result
	}

	switch shape.Kind {
	case contract.ObjectiveMutation:
		targetsDone := mutationTargetsDone(shape, execution)
		if execution.Mutated {
			if len(shape.Verification) > 0 && !execution.VerifiedAfterMutation {
				result.State = Insufficient
				result.Stage = StageVerify
				result.Missing = append(result.Missing, "verification")
				result.Next = append(result.Next, nextEvidence("verification"))
				result.Reasons = append(result.Reasons, "a change was observed but no successful check followed it")
				return result
			}
			if !targetsDone {
				result.State = Insufficient
				result.Stage = StageAct
				for _, path := range shape.Mutation.Paths {
					if !containsString(execution.MutatedPaths, path) {
						result.Missing = append(result.Missing, "target_state:"+path)
						result.Next = append(result.Next, "complete the requested change to "+path)
						result.Reasons = append(result.Reasons, "the requested target "+path+" has not changed")
					}
				}
				return result
			}
			result.Stage = StageComplete
			result.Reasons = append(result.Reasons, "the requested change and its verification are observed")
			return result
		}
		result.State = Sufficient
		result.Stage = StageAct
		result.Reasons = append(result.Reasons, "the required pre-change evidence is known")
		return result
	case contract.ObjectiveExecution:
		if len(shape.Verification) > 0 && !execution.VerifiedAfterExecution {
			result.State = Insufficient
			result.Stage = StageVerify
			result.Missing = append(result.Missing, "verification")
			result.Next = append(result.Next, nextEvidence("verification"))
			result.Reasons = append(result.Reasons, "the requested check has not run successfully")
			return result
		}
		if shape.ExecutionMode == contract.ExecutionRuntime && !execution.RuntimeObserved {
			// A finished command is execution, not a running application. The
			// runtime objective stays in the act stage until a service is
			// observed reaching readiness and answering a check.
			result.State = Insufficient
			result.Stage = StageAct
			result.Missing = append(result.Missing, "runtime_observation")
			result.Next = append(result.Next, nextEvidence("runtime_observation"))
			result.Reasons = append(result.Reasons,
				"the request asked to run the project's runtime but no live runtime was observed")
			if execution.Executed {
				result.Reasons = append(result.Reasons,
					"a completed command is not the requested runtime; a command that exited proves only that it ran")
			}
			return result
		}
		if !execution.Executed {
			result.State = Insufficient
			result.Stage = StageAct
			result.Missing = append(result.Missing, "execution_result")
			result.Next = append(result.Next, nextEvidence("execution_result"))
			result.Reasons = append(result.Reasons, "the requested process has not reached a successful terminal state")
			return result
		}
		result.Stage = StageComplete
		result.Reasons = append(result.Reasons, "the requested execution was observed")
		return result
	default:
		result.Stage = StageAnswer
		result.Reasons = append(result.Reasons, "the required repository evidence is known")
		return result
	}
}

// ReconcileAcquisition keeps a capability failure from closing the task while
// every missing requirement still has an independent authorized provider.
//
// Sufficiency sees evidence, not the constructed action surface, so it cannot
// decide whether a failed tool was the only acquisition path. This reconciliation
// runs after candidate construction is still possible and before the surface is
// closed. It only relaxes a BlockCapabilityFailure: budget, mutation, scope, and
// external-only boundaries keep their own verdict.
func ReconcileAcquisition(evidence *EvidenceState, tools []agent.Tool, suff Sufficiency) Sufficiency {
	if evidence == nil || suff.Kind != BlockCapabilityFailure || len(suff.Missing) == 0 {
		return suff
	}
	failures := evidence.FailureState()
	blocked := make(map[string]bool, len(failures.Records))
	for _, record := range failures.Records {
		if record.Blocks() {
			blocked[record.Tool] = true
		}
	}
	alternatives := make([]string, 0, len(suff.Missing))
	for _, key := range suff.Missing {
		providers := acquisitionProviders(key, tools, blocked)
		if len(providers) == 0 {
			// One required observation has no remaining provider, so the task
			// is still genuinely blocked. A partial fallback cannot prove the
			// objective.
			return suff
		}
		alternatives = append(alternatives, key+" via "+strings.Join(providers, ", "))
	}
	suff.State = Insufficient
	suff.Stage = StageExplore
	suff.Kind = BlockNone
	suff.Reasons = append(suff.Reasons,
		"the failed capability is not the only acquisition path; independent authorized providers remain available")
	suff.Next = nil
	for _, key := range suff.Missing {
		suff.Next = append(suff.Next, nextEvidence(key))
	}
	suff.Next = append(suff.Next, alternatives...)
	return suff
}

// acquisitionProviders returns the offered tools that can satisfy one
// requirement. It maps a semantic requirement to capability classes, never to
// a vendor or a fixed tool name: read, ls, glob, and structure all provide
// inventory or content observation, while external and execution requirements
// keep their own class.
func acquisitionProviders(key string, tools []agent.Tool, blocked map[string]bool) []string {
	var needed []agent.ActionClass
	switch key {
	case "external_observation":
		needed = []agent.ActionClass{agent.ActionExternal}
	case "verification", "execution_result", "runtime_observation":
		needed = []agent.ActionClass{agent.ActionExecute}
	case "test_inventory":
		needed = []agent.ActionClass{agent.ActionStructure, agent.ActionObserve}
	default:
		needed = []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}
	}
	var providers []string
	for _, tool := range tools {
		if blocked[tool.Name()] {
			continue
		}
		for _, class := range needed {
			if agent.HasActionClass(tool, class) {
				providers = append(providers, tool.Name())
				break
			}
		}
	}
	return providers
}

// missingForAction returns the requirements that must be known before the
// current stage can proceed. Verification is excluded here because it is a
// post-change need, not a pre-action one.
func missingForAction(shape TaskShape, evidence *EvidenceState, execution ExecutionState) []Requirement {
	var missing []Requirement
	for _, requirement := range evidence.Requirements {
		if requirement.Key == "verification" {
			continue
		}
		if strings.HasPrefix(requirement.Key, "target_state:") && execution.Mutated {
			continue
		}
		if evidence.Satisfied(requirement.Key, execution) {
			continue
		}
		missing = append(missing, requirement)
	}
	return missing
}

// mutationTargetsDone reports whether every explicitly named path changed.
func mutationTargetsDone(shape TaskShape, execution ExecutionState) bool {
	if len(shape.Mutation.Paths) == 0 {
		return execution.Mutated
	}
	for _, path := range shape.Mutation.Paths {
		if !containsString(execution.MutatedPaths, path) {
			return false
		}
	}
	return true
}

// failureRecordReason renders one failure in evidence terms for the model and
// audit. It names the class, not vendor prose.
func failureRecordReason(record FailureRecord) string {
	class := string(record.Class)
	if class == "" {
		class = "unclassified"
	}
	if record.RequestLevel() {
		return record.Tool + " rejected an invocation with " + class + " (" +
			itoa(record.Failures) + " rejection(s)" +
			refusalSuffix(record) + "); the capability remains available"
	}
	reason := record.Tool + " failed with " + class + " (" +
		itoa(record.Failures) + " attempt(s)"
	if record.Refusals > 0 {
		reason += ", " + itoa(record.Refusals) + " identical refusal(s)"
	}
	return reason + ")"
}

// refusalSuffix renders the repeated-refusal count once, without duplicating
// the closing parenthesis between the request-level and capability branches.
func refusalSuffix(record FailureRecord) string {
	if record.Refusals <= 0 {
		return ""
	}
	return ", " + itoa(record.Refusals) + " identical refusal(s)"
}

// nextEvidence renders the highest-value next observation for a requirement.
func nextEvidence(key string) string {
	switch {
	case strings.HasPrefix(key, "target_state:"):
		path := strings.TrimPrefix(key, "target_state:")
		if path == "answer" {
			return "read the repository coordinate the answer names"
		}
		return "read " + path + " or list its directory to establish whether it exists"
	case strings.HasPrefix(key, "target_content:"):
		return "read " + strings.TrimPrefix(key, "target_content:") + " or query its declarations"
	case strings.HasPrefix(key, "target_coordinates:"):
		return "locate the requested declaration with the symbols tool"
	case key == "repository_overview":
		return "list the repository root and read the entry point or module file"
	case key == "repository_survey":
		return "read a source file (entry point or module) for the components the answer will describe; documentation, manifests, configuration, and inventory are not implementation evidence"
	case key == "test_inventory":
		return "query the structure tool with view=tests, unscoped, and inspect scope, inventory, and structural completeness; an overview test_files count is not a filtered inventory"
	case key == "external_observation":
		return "use the external capability the request named and observe its result"
	case key == "verification":
		return "run the requested check and require a successful result"
	case key == "execution_result":
		return "run the requested command and observe its terminal state"
	case key == "runtime_observation":
		return "launch the project's entry point with the verify capability, declare readiness, and observe a passing HTTP/browser/command check against the live process; a command that merely exited is not the runtime"
	default:
		return "acquire evidence for " + key
	}
}

// outcomePath extracts a repository-relative path from a tool outcome.
func outcomePath(outcome agent.ToolOutcome) string {
	details := detailsMap(outcome.Result.Details)
	if path := detailString(details, "path"); path != "" {
		return cleanPath(path)
	}
	if path := argString(outcome.Args, "path"); path != "" {
		return cleanPath(path)
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
