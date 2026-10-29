package cognition

import (
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
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
				if !outcome.Refused() {
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
				state.PendingFailures[outcome.ToolName]--
				if state.PendingFailures[outcome.ToolName] == 0 {
					delete(state.PendingFailures, outcome.ToolName)
				}
			}
			position++
		}
	}
	sort.Strings(state.MutatedPaths)
	state.VerifiedAfterMutation = state.Mutated && state.LastVerification >= state.LastMutation
	state.VerifiedAfterExecution = state.LastExecution >= 0 && state.LastVerification >= state.LastExecution
	return state
}

// Sufficiency is the runtime's judgement about the current evidence.
type Sufficiency struct {
	// State is the verdict.
	State SufficiencyState
	// Stage is what the evidence supports.
	Stage Stage
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
		result.Reasons = append(result.Reasons, "the root budget is exhausted")
		return result
	}
	if evidence == nil {
		evidence = NewEvidenceState()
	}
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
			result.Reasons = append(result.Reasons, "a mutation could not be attributed to a requested path")
			result.Next = append(result.Next, "report the unattributed change and require review")
			return result
		}
		for _, path := range execution.MutatedPaths {
			if !containsString(shape.Mutation.Paths, path) {
				result.State = Blocked
				result.Stage = StageBlocked
				result.Reasons = append(result.Reasons, "a change outside the requested scope was observed: "+path)
				result.Next = append(result.Next, "report the scope violation; do not continue mutating")
				return result
			}
		}
	}

	missing := missingForAction(shape, evidence, execution)
	if len(missing) > 0 {
		result.State = Insufficient
		result.Stage = StageExplore
		for _, requirement := range missing {
			result.Missing = append(result.Missing, requirement.Key)
			result.Next = append(result.Next, nextEvidence(requirement.Key))
			if requirement.Reason != "" {
				result.Reasons = append(result.Reasons, requirement.Reason)
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
	case key == "verification":
		return "run the requested check and require a successful result"
	case key == "execution_result":
		return "run the requested command and observe its terminal state"
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
