package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// deferredAnswerPattern matches a sentence that only announces an action the
// assistant still intends to take. It is the shape a model falls into when the
// runtime asks it to answer while it would rather keep exploring. Once the
// action surface is closed that promised observation cannot happen, so the
// sentence cannot be the result the user asked for.
//
// The verb list is deliberately limited to observation/execution verbs. An
// explanatory lead ("let me summarise…") is an answer, and "let me know…" is a
// closing courtesy; neither is a deferred action.
var deferredAnswerPattern = regexp.MustCompile(
	`(?i)^(?:so|first|next|now)?[,:]?\s*(?:(?:let\s+me|let['’]s)|i(?:['’]ll|\s+will|\s+am\s+going\s+to|['’]m\s+going\s+to|\s+need\s+to)|we(?:['’]ll|\s+will|\s+are\s+going\s+to|['’]re\s+going\s+to))\s+(?:now\s+|first\s+|also\s+|quickly\s+|just\s+)?(?:go\s+ahead\s+and\s+)?(?:explore|check|look|inspect|read|search|find|list|examine|investigate|scan|try|run|gather|review|open|locate|analy[sz]e|probe|walk|use|start|serve|launch|spin|boot|host|test|verify)\b`)

// answerSentenceBoundary splits an answer into the clauses that must each be
// substantive. The separator requires whitespace so a period inside a path or
// a package name ("auth.go") does not turn one sentence into two. It is
// intentionally crude: this is a conservative completion gate, so
// under-splitting can only make the check less likely to reject.
var answerSentenceBoundary = regexp.MustCompile(`[.!?]+[ \t]+|\n+`)

// answerIsDeferredPlan reports whether the whole message only announces
// actions it has not performed. A clause that carries content after a colon is
// not a bare plan ("let me summarise: the project is a Go service"), and a
// message with any substantive clause is an answer.
func answerIsDeferredPlan(answer string) bool {
	clauses := answerSentenceBoundary.Split(strings.TrimSpace(answer), -1)
	seen := false
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		seen = true
		if !deferredAnswerPattern.MatchString(clause) {
			return false
		}
		if colon := strings.IndexByte(clause, ':'); colon >= 0 &&
			strings.TrimSpace(clause[colon+1:]) != "" {
			return false
		}
	}
	return seen
}

// executionIntentWithoutTool reports that a message consists only of a
// first-person promise to perform an action. The loop uses the same
// deterministic vocabulary as the completion gate: it never turns the prose
// into a call, only into a bounded request for the canonical call.
func executionIntentWithoutTool(text string) bool {
	return answerIsDeferredPlan(text)
}

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
	// CompletionCompletedLimited means the runtime observed the requested
	// change or execution and the workspace has no supported verification
	// command. The objective is satisfied; its verification is unavailable,
	// which is a limitation to report rather than a reason to keep searching.
	CompletionCompletedLimited CompletionState = "COMPLETED_LIMITED_VERIFICATION"
)

// Completion is a completion verdict with its evidence reason.
type Completion struct {
	// State is the verdict.
	State CompletionState
	// Reason explains a NEEDS_REVIEW verdict, in terms of observed evidence.
	Reason string
	// Limitation explains a COMPLETED_LIMITED_VERIFICATION verdict: what the
	// runtime could not verify and why. It is never a success claim for the
	// missing check; it states which observation is unavailable.
	Limitation string
	// Issues are the machine-readable grounding claims behind the reason.
	// They are empty unless the verdict came from the deterministic answer
	// audits, so a host can show exactly which claim to repair, what
	// evidence class it needs, and which action is permitted — data the flat
	// reason string already collapsed.
	Issues []contract.GroundingIssue
}

// Complete reports whether the verdict is a terminal completion.
//
// A limited verification is complete: the objective's observed result exists
// and the missing observation is a property of the workspace, not of the work.
// Verified distinguishes the two for a host that wants to report the
// distinction.
func (c Completion) Complete() bool {
	return c.State == CompletionComplete || c.State == CompletionCompletedLimited
}

// Verified reports whether the completion is backed by a successful check.
func (c Completion) Verified() bool { return c.State == CompletionComplete }

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
	if outstandingCalls(last) > 0 {
		return needsReview("the run stopped with tool calls still outstanding")
	}
	if last.Assistant.ProtocolResidue {
		return needsReview("the model emitted serialized tool syntax that could not be normalized")
	}

	evidence := scanCompletionEvidence(turns, mutationIntent, false)
	if len(evidence.pendingFailures) > 0 {
		return needsReview("a tool call failed and no later success or verification recovered it: %s",
			pendingFailureNames(evidence.pendingFailures, evidence.pendingFailureClasses))
	}
	if mutationIntent && !evidence.mutated {
		return needsReview("the request asked for a repository change but no change was observed")
	}
	if !mutationIntent && evidence.mutated {
		// A read-only or execution-only request must not be called complete
		// after the run changed the repository. The workspace may have
		// authorized the call, but the objective did not.
		return needsReview("the request did not ask for a repository change but a change was observed")
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
	evidence := scanCompletionEvidence(turns, mutationIntent, false)
	if !evidence.mutated {
		return false, false
	}
	return true, evidence.lastVerification >= evidence.lastPotentialMutation
}

type completionEvidence struct {
	// executed is a process that reached a successful terminal state, or a
	// successful verification (which is itself execution evidence). A started
	// but unfinished background job is not evidence.
	executed bool
	// runtimeObserved is a live project runtime that reached readiness, ran at
	// least one passing check, and stayed alive. It is the only evidence that
	// proves a runtime objective; an unrelated finished command is not.
	runtimeObserved bool
	// lastRuntime is the position of the last live-runtime observation. It is
	// behavioral evidence even when the tool did not carry a check record.
	lastRuntime int
	// mutated is a definite change: a write/edit/delete, or a shell command
	// classified as mutating.
	mutated bool
	// lastPotentialMutation is the last definite change, or (for a mutation
	// objective without a captured journal) the last arbitrary execution,
	// because an arbitrary process may have changed something the runtime
	// could not classify.
	lastPotentialMutation int
	// lastAmbiguousExecution is the last write-capable execution the runtime
	// could not classify. With a captured journal it does not reopen the
	// obligation by itself: the journal's tree identity must prove the run
	// ended in the checked state.
	lastAmbiguousExecution int
	lastVerification       int
	pendingFailures        map[string]int
	pendingFailureClasses  map[string]string
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
// journalCaptured reports that the host's edit journal observed the run. A
// captured journal can bound a write-capable execution after a check: it pins
// the workspace identity at the check and at run end, and the completion
// evaluator compares them. Without it, the command's shape is the only
// evidence available.
//
// A failure is recovered by a later success of the same tool (a retry that
// worked) or by a successful verification (the final state was checked). A
// verification after a failure therefore clears every pending failure, while a
// failure with no later evidence stays pending.
func scanCompletionEvidence(turns []Turn, mutationIntent bool, journalCaptured bool) completionEvidence {
	evidence := completionEvidence{
		lastPotentialMutation:  -1,
		lastAmbiguousExecution: -1,
		lastVerification:       -1,
		lastRuntime:            -1,
		pendingFailures:        map[string]int{},
		pendingFailureClasses:  map[string]string{},
	}
	position := 0
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError {
				// A malformed call never executed, so it is not an observation
				// that can contradict the objective. Counting it would let a
				// late continuation downgrade a proof the runtime already
				// holds; the missing-evidence rules still decide the objective
				// on its own terms.
				if !outcome.Refused() && !outcome.Result.NegativeObservation() && !outcome.Result.Rejected() {
					evidence.pendingFailures[outcome.ToolName]++
					if class := strings.TrimSpace(outcome.Result.FailureClass); class != "" {
						evidence.pendingFailureClasses[outcome.ToolName] = class
					}
				}
				position++
				continue
			}

			definite := outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates)
			if definite {
				evidence.mutated = true
				evidence.lastPotentialMutation = position
			} else if mutationIntent && outcome.Result.Executes && !outcome.ReadOnly() {
				// Without a captured journal the runtime cannot prove this
				// process did not change the repository, so a mutation
				// objective must verify after it. A read-only authorization is
				// the exception: the sandbox prevented a write, so an
				// investigation execution (FUZZ probing the service) does not
				// create a verification debt.
				evidence.lastAmbiguousExecution = position
				if !journalCaptured {
					// A captured journal is the authority on what changed, but
					// it only records the net diff: it cannot order a change
					// against the check. The completion evaluator compares the
					// journal's verification and run-end identities, which is
					// the proof the conservative command-shape rule stands in
					// for here.
					evidence.lastPotentialMutation = position
				}
			}
			if outcome.Result.Executes && outcome.Result.Finished {
				evidence.executed = true
			}
			if outcome.Result.Runtime != nil {
				// The live runtime observation is stronger than a finished
				// command and is the only evidence a runtime objective accepts.
				evidence.runtimeObserved = true
				evidence.lastRuntime = position
				evidence.executed = true
			}
			if outcome.Result.Verifies {
				// A successful check proves a process ran and completed even
				// when the tool cannot report an Executes/Finished pair (a
				// delegated child, for example).
				evidence.executed = true
				evidence.lastVerification = position
				evidence.verifications = append(evidence.verifications, verificationRecord{
					position: position,
					evidence: outcome.Result.Verification,
				})
				// A successful verification is evidence that the final state
				// is sound, so earlier failures no longer block completion.
				evidence.pendingFailures = map[string]int{}
				evidence.pendingFailureClasses = map[string]string{}
			}
			if evidence.pendingFailures[outcome.ToolName] > 0 {
				// Any later success of the same capability supersedes its
				// earlier failures. Keeping a count would make two probes of
				// different coordinates require two successes, so a model that
				// recovered once would still be reported as unrecovered.
				delete(evidence.pendingFailures, outcome.ToolName)
				delete(evidence.pendingFailureClasses, outcome.ToolName)
			}
			position++
		}
	}
	return evidence
}

// applyMutationProvenance merges the host's journal evidence into the turn
// scan.
//
// A shell command such as "npx create-next-app ." can change the repository
// without the tool being able to report the resulting paths, so the turn scan
// cannot see a definite mutation. The journal captured the actual diff; that is
// runtime evidence of a change, not a model claim. The mutation position is the
// start of the turn whose batch first could mutate, so a check that ran before
// that turn cannot satisfy the verification obligation.
func applyMutationProvenance(evidence *completionEvidence, turns []Turn, provenance MutationProvenance) {
	if evidence == nil || !provenance.Captured || len(provenance.Changes) == 0 {
		return
	}
	evidence.mutated = true
	position := MutationTurnPosition(turns, provenance.Turn)
	if position > evidence.lastPotentialMutation {
		evidence.lastPotentialMutation = position
	}
}

// MutationTurnPosition returns the run position of the first outcome in a
// turn. It lets the host record journal evidence at the same observation order
// the completion evaluator uses.
func MutationTurnPosition(turns []Turn, turn int) int {
	if turn <= 0 {
		return 0
	}
	position := 0
	for index, item := range turns {
		if index >= turn {
			break
		}
		position += len(item.Results)
	}
	return position
}

// outstandingCalls counts the last turn's results that represent work the run
// left undone. A refusal is a control decision: the call did not run and the
// runtime did not leave it pending, so it is never outstanding. Counting it
// would make an answered turn undecidable forever merely because the model
// proposed one action the runtime had already closed.
func outstandingCalls(turn Turn) int {
	count := 0
	for _, outcome := range turn.Results {
		if outcome.Refused() || outcome.Result.Rejected() {
			// Neither ran. A refusal is a control decision and a malformed
			// call never reached the capability, so neither leaves work
			// outstanding.
			continue
		}
		count++
	}
	return count
}

func needsReview(format string, args ...any) Completion {
	return Completion{State: CompletionNeedsReview, Reason: fmt.Sprintf(format, args...)}
}

// needsReviewIssues is needsReview with the machine-readable claims behind
// the verdict. The issues come from the assessment that produced the reason,
// never a second audit.
func needsReviewIssues(issues []contract.GroundingIssue, format string, args ...any) Completion {
	return Completion{State: CompletionNeedsReview, Reason: fmt.Sprintf(format, args...), Issues: issues}
}

// pendingFailureVetoes reports whether an unrecovered tool failure still
// invalidates the answer.
//
// The evidence review is the runtime's statement that the objective's required
// observations are missing. When it is non-empty, every pending failure is part
// of that gap and must veto. When it is empty, the evidence contract is
// satisfied: an unavailable alternative provider is a real failure worth
// reporting, but it did not prevent the proof, so it cannot veto the answer.
// Verification and runtime failures stay hard vetoes because they contradict
// the final state the proof depends on, not merely one acquisition path. A
// control refusal (authorization, invalid arguments, correction budget) never
// ran and is excluded earlier as a rejection, not a pending failure.
func pendingFailureVetoes(evidence completionEvidence, evidenceReview string) bool {
	if strings.TrimSpace(evidenceReview) != "" {
		return true
	}
	for _, class := range evidence.pendingFailureClasses {
		switch class {
		case ToolFailureVerification, ToolFailureInsufficient, ToolFailureRuntime:
			return true
		}
	}
	return false
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
	// EvidenceReview is an optional runtime evidence verdict. A non-empty
	// value means the cognitive layer judged the evidence insufficient,
	// conflicting, or blocked at the last request. It can only downgrade a
	// completion; the completion evaluator still owns the final decision.
	EvidenceReview string
	// PriorChange reports that the durable task already recorded a current
	// observed change before this run. A resumed turn that only verifies that
	// change must not be refused as "no change was observed"; the durable
	// completion boundary still requires the verification to follow the change
	// and cover the contract. It can only enable that boundary, never complete
	// the objective by itself.
	PriorChange bool
	// Mutations is the run-level workspace mutation evidence captured by the
	// host's edit journal. It attributes a shell mutation whose paths the
	// command text cannot reveal. It is runtime evidence, never model output,
	// and an empty Captured=false value must stay unattributed.
	//
	// When Captured is true the journal also bounds a write-capable execution
	// after the last check. It pins VerifiedTree at the check and FinalTree at
	// run end; equal identities prove nothing changed after the check, and an
	// unclassified execution that changed an already-expected path reopens the
	// obligation when they differ. Without a captured journal the command's
	// write capability is the only evidence available, so any write-capable
	// execution still owes a later check.
	Mutations MutationProvenance
	// Provenance summarizes what kind of evidence the run can truthfully rely
	// on. It can only downgrade completion: an explicit external requirement
	// that was never observed, or a documentation-only claim presented as an
	// implementation fact, is a review boundary.
	Provenance EvidenceProvenance
	// UnresolvedTargets names symbol or package targets the runtime looked for
	// and established this repository does not contain. The projection
	// downgraded those requirements after its bounded acquisition budget; the
	// evaluator must agree, or the same heuristic target becomes a review
	// boundary after it stopped being an obligation.
	UnresolvedTargets []string
	// VerificationLimitation is non-empty when the run established that the
	// workspace has no supported verification command. It is the control
	// plane's observation, never model text: completion reports the change
	// with a limited verification instead of demanding a check that cannot
	// run.
	VerificationLimitation string
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
	// StaticVerifications, TestVerifications, and BehavioralVerifications
	// partition those checks by what they actually observed. A static check
	// can close a build/scope requirement; only a behavioral check can support
	// a claim about runtime behavior.
	StaticVerifications     int
	TestVerifications       int
	BehavioralVerifications int
	// Satisfied reports whether every behavioural requirement was met.
	Satisfied bool
	// Detail explains a failure.
	Detail string
}

// pendingFailureNames renders the tools with unrecovered failures so the
// review reason names the capability instead of saying only "a tool call".
func pendingFailureNames(failures map[string]int, classes map[string]string) string {
	names := make([]string, 0, len(failures))
	for name, count := range failures {
		label := name
		if class := strings.TrimSpace(classes[name]); class != "" {
			label += " [" + class + "]"
		}
		if count > 1 {
			label += fmt.Sprintf(" x%d", count)
		}
		names = append(names, label)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
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
	if outstandingCalls(last) > 0 {
		return needsReview("the run stopped with tool calls still outstanding")
	}
	if last.Assistant.ProtocolResidue {
		return needsReview("the model emitted serialized tool syntax that could not be normalized")
	}

	intent := input.Contract
	if intent.Kind == "" {
		return needsReview("no intent contract was supplied")
	}
	evidence := scanCompletionEvidence(input.Turns, intent.Kind == contract.ObjectiveMutation, input.Mutations.Captured)
	applyMutationProvenance(&evidence, input.Turns, input.Mutations)
	if len(evidence.pendingFailures) > 0 && pendingFailureVetoes(evidence, input.EvidenceReview) {
		return needsReview("a tool call failed and no later success or verification recovered it: %s",
			pendingFailureNames(evidence.pendingFailures, evidence.pendingFailureClasses))
	}
	// A conditional mutation starts in the investigation phase. Until a change
	// is actually observed (or a durable prior change exists), the turn is
	// judged as the answer the request asked for first, not as a failed change.
	// The original contract is restored for the mutation branch and still owns
	// target, verification, and grounding rules once a change exists.
	deferredPhase := intent.Kind == contract.ObjectiveMutation &&
		intent.ConditionalMutation && !evidence.mutated && !input.PriorChange
	effective := intent
	if deferredPhase {
		effective = intent.InformationalPhase()
	}
	if effective.Kind == contract.ObjectiveMutation && !evidence.mutated && !input.PriorChange {
		return needsReview("the request asked for a repository change but no change was observed")
	}
	if effective.Kind == contract.ObjectiveExecution &&
		effective.ExecutionMode == contract.ExecutionRuntime && !evidence.runtimeObserved {
		// "Run this project" is not proven by any process that finished. The
		// environment probe that satisfied an earlier classifier ("node -v &&
		// npm -v") is real execution, but it is not the requested execution.
		// A clean stop, a model claim, and a successful exit code are all
		// insufficient without an observed live runtime.
		return needsReview("the request asked to run the project's runtime but no live runtime observation was made; " +
			"launch the project's entry point and observe it reach readiness and answer a check")
	}
	if effective.Kind == contract.ObjectiveExecution && len(effective.Verification) == 0 && !evidence.executed {
		// A clean model stop is not execution evidence. A started background
		// job is not finished execution evidence either: its outcome is only
		// known when wait observes it.
		return needsReview("the request asked for execution but no successful execution was observed")
	}
	if effective.Kind == contract.ObjectiveInformational && strings.TrimSpace(last.Assistant.Text()) == "" {
		// An empty answer is not an answer. The rule is deliberately only
		// "visible text exists": judging relevance would require an LLM judge.
		return needsReview("the request asked for an answer but the final assistant message was empty")
	}
	if effective.Kind == contract.ObjectiveInformational && answerIsDeferredPlan(last.Assistant.Text()) {
		// A plan is not a result. The model says "let me check…" because it
		// intends to keep acting; on a closed surface that action is refused,
		// so accepting the sentence would report a completion the user never
		// saw. This is narrower than relevance judging: it only rejects a
		// message whose every clause is an unperformed observation.
		return needsReview("the request asked for an answer but the final assistant message only announces a further action")
	}
	limited := strings.TrimSpace(input.VerificationLimitation)
	// Verification is outstanding when the last successful check does not
	// follow the last change, or when an execution objective requires a check
	// and none ran. The position rule matters: a workspace can gain a check
	// command as part of the change itself, so a NOT_RUN observed before the
	// change is not proof that the final state cannot be checked.
	verificationOutstanding := evidence.lastVerification < evidence.lastPotentialMutation ||
		(effective.Kind == contract.ObjectiveExecution && len(effective.Verification) > 0 &&
			evidence.lastVerification < 0)
	if verificationOutstanding {
		if limited == "" {
			after := evidence.lastPotentialMutation
			if after < 0 {
				after = 0
			}
			limited = verificationLimitation(input.Turns, after)
		}
		if limited == "" {
			return needsReview("the repository change or execution was not followed by a successful verification")
		}
	} else {
		limited = ""
	}
	if input.Mutations.Captured && evidence.lastVerification >= 0 &&
		evidence.lastAmbiguousExecution > evidence.lastVerification {
		// A write-capable execution ran after the check. The journal's net diff
		// cannot order a change against the check, so the only proof is the
		// workspace identity: the run must have ended in exactly the state the
		// check observed. An unpinned identity is not that proof.
		if input.Mutations.VerifiedTree == "" ||
			input.Mutations.VerifiedTree != input.Mutations.FinalTree {
			return needsReview("a write-capable execution ran after the last successful check and the workspace is not the checked state; run the check again")
		}
	}
	if effective.Kind != contract.ObjectiveMutation && evidence.mutated {
		// The phase contract must not hide a change the request did not ask
		// for. A conditional contract still names the mutation, so this only
		// fires for a genuinely informational/execution objective.
		return needsReview("the request did not ask for a repository change but a change was observed")
	}

	root := input.Root
	if input.Snapshot != nil && input.Snapshot.Root != "" {
		root = input.Snapshot.Root
	}
	observedPaths := ObservedMutationPathsWithProvenance(root, input.Turns, input.Snapshot, input.Mutations)
	resolvedTargets := ResolveAbsentNamedTargets(input.Snapshot, observedPaths)
	scoped := WithResolvedTargetScope(effective, resolvedTargets)

	var structural StructuralEvidence
	if effective.Kind == contract.ObjectiveMutation && len(effective.Targets) > 0 {
		if input.Snapshot != nil {
			structural = CheckTargets(input.Snapshot, input.Resolver)
			structural.Checks = ApplyResolvedTargets(structural.Checks, resolvedTargets)
		} else {
			structural = TargetEvidenceWithoutSnapshot(effective)
		}
		structural = dropUnresolvedSemanticTargets(structural, unresolvedTargets(input, effective))
	}
	classified := ClassifyMutations(root, input.Turns, scoped, input.Snapshot, input.Mutations)
	for _, mutation := range classified {
		switch mutation.Class {
		case MutationUnexpected, MutationOutsideWorkspace:
			structural.Unexpected = append(structural.Unexpected, mutation.Path)
		case MutationUnattributed:
			structural.Unattributed = append(structural.Unattributed, mutation.Path)
		}
	}
	structural.Unexpected = uniqueSorted(structural.Unexpected)
	structural.Unattributed = uniqueSorted(structural.Unattributed)
	behavioral := AssessBehavior(scoped, input.Snapshot, evidence,
		goSourceDirectories(observedPaths))
	assessment := AssessAlignment(scoped, structural, behavioral)
	if limited != "" {
		// Verification is unavailable, so the behavioural oracle cannot be
		// evaluated. The observed change is the alignment evidence that
		// remains; a genuine contradiction (an unexpected or outside-scope
		// change, or an unsatisfied named path target) is still reported
		// through structural and the reason list below.
		limitedIntent := scoped
		limitedIntent.Ambiguous = false
		limitedBehavioral := behavioral
		limitedBehavioral.Satisfied = true
		assessment = AssessAlignment(limitedIntent, structural, limitedBehavioral)
		if assessment.Alignment == AlignmentUncertain {
			assessment.Alignment = AlignmentAligned
			assessment.Rationale = "an observed change is the objective's evidence; verification is limited"
		}
	}

	var reasons []string
	if effective.Kind == contract.ObjectiveMutation && len(effective.Targets) > 0 {
		for _, issue := range structural.Issues {
			reasons = append(reasons, "target evidence is incomplete: "+issue)
		}
		for _, check := range structural.Checks {
			if check.Status == TargetSemantic {
				// Semantic targets are judged by mutation and verification
				// evidence, not by a matching changed path.
				continue
			}
			if check.Status != TargetSatisfied {
				reasons = append(reasons, check.Detail)
			}
		}
	}
	if len(structural.Unexpected) > 0 {
		reasons = append(reasons, "observed change outside the requested scope: "+
			strings.Join(structural.Unexpected, ", "))
	}
	if len(structural.Unattributed) > 0 {
		reasons = append(reasons, "observed change could not be attributed to a path: "+
			strings.Join(structural.Unattributed, ", "))
	}
	if !behavioral.Satisfied && limited == "" {
		reasons = append(reasons, behavioral.Detail)
	}
	if effective.Ambiguous && limited == "" {
		detail := "the request does not name a verifiable target or behavioural check"
		if len(effective.Issues) > 0 {
			detail = effective.Issues[0]
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
		return needsReview("%s", strings.Join(orderedUnique(reasons), "; "))
	}
	// Semantic integrity comes before the model's claim of success. A
	// deterministic answer check can veto completion even when every
	// structural target and verification passed: an unsupported global
	// negative conclusion is not a completed objective.
	grounding := AssessGrounding(effective, input.Turns, last.Assistant.Text())
	if !grounding.Pass() {
		return needsReviewIssues(grounding.Issues, "intent grounding: %s", grounding.Reason())
	}
	if claims := AssessVerificationClaims(effective, input.Turns, last.Assistant.Text()); !claims.Pass() {
		return needsReviewIssues(claims.Issues, "verification claim: %s", claims.Reason())
	}
	provenance := AssessAnswerProvenance(effective, last.Assistant.Text(), input.Provenance)
	if !provenance.Pass() {
		return needsReviewIssues(provenance.Issues, "answer provenance: %s", provenance.Reason())
	}
	if input.Provenance.ExternalRequired && !input.Provenance.ExternalObserved {
		// A request that explicitly needs external knowledge is not complete
		// because a local answer exists. The required capability either
		// produced an observation or the task is a review.
		return needsReview("the request required external knowledge but no external observation succeeded")
	}
	if input.Provenance.RequireAttribution {
		// A broad repository account can make an exclusive file-count claim.
		// When the run's own glob or listing observed more files than the
		// answer claims, the answer contradicts its evidence; completion must
		// not claim a result the user can see is false.
		if issue := inventoryClaimIssue(root, last.Assistant.Text(), input.Turns); issue != "" {
			return needsReview("%s", issue)
		}
	}
	if review := strings.TrimSpace(input.EvidenceReview); review != "" {
		// The cognitive layer is authoritative about what evidence it has.
		// A model answering while the runtime still reports missing evidence
		// is a guess, not a proven completion.
		return needsReview("%s", review)
	}
	if limited != "" {
		return Completion{State: CompletionCompletedLimited, Limitation: limited}
	}
	return Completion{State: CompletionComplete}
}

// verificationLimitation reports the control plane's "no supported check"
// observation when it happened after the last potential mutation.
//
// It reads the check capability's own result payload, never model prose. The
// position rule matters: a workspace can gain a check command as part of the
// change itself, so a NOT_RUN observed before the change is not proof that the
// final state cannot be checked.
func verificationLimitation(turns []Turn, mutatedAt int) string {
	if mutatedAt < 0 {
		return ""
	}
	position := 0
	lastGap := -1
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if !outcome.Result.IsError && !outcome.Result.Verifies &&
				(outcome.ToolName == "check" || outcome.ToolName == "verify") {
				details, _ := outcome.Result.Details.(map[string]any)
				status, _ := details["status"].(string)
				if strings.EqualFold(strings.TrimSpace(status), "NOT_RUN") {
					lastGap = position
				}
			}
			position++
		}
	}
	if lastGap >= mutatedAt {
		return "no supported verification command was discovered for this workspace"
	}
	return ""
}

// unresolvedTargets returns the named symbol and package targets the runtime
// established are absent.
//
// The projection's own downgrade is authoritative when the host supplies it;
// otherwise the same bounded rule is derived from the run's structural misses
// so a host that does not project (a subagent) still reaches the same verdict.
func unresolvedTargets(input CompletionInput, intent contract.IntentContract) map[string]bool {
	out := map[string]bool{}
	for _, value := range input.UnresolvedTargets {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out[value] = true
		// The projection reports a limitation as "requirement: reason", and a
		// coordinate requirement carries the inferred target in its key. A
		// run whose structural capability cannot look the target up has no
		// miss turn from which to derive the bare name, so the evaluator must
		// read the requirement shape it is handed.
		if rest, ok := strings.CutPrefix(value, "target_coordinates:"); ok {
			name, _, _ := strings.Cut(rest, ":")
			if name = strings.TrimSpace(name); name != "" {
				out[name] = true
			}
		}
	}
	misses := map[string]int{}
	for _, turn := range input.Turns {
		for _, outcome := range turn.Results {
			if !outcome.Refused() && !outcome.Result.IsError && structuralLookupMiss(outcome) {
				for _, target := range intent.Targets {
					if target.Kind != contract.TargetSymbol && target.Kind != contract.TargetPackage {
						continue
					}
					if outcomeNamesArgument(outcome, target.Value) {
						misses[target.Value]++
					}
				}
			}
		}
	}
	for value, count := range misses {
		if count >= contract.MaxTargetLookupMisses {
			out[value] = true
		}
	}
	return out
}

// structuralLookupMiss reports a completed lookup that found nothing. The
// structural capabilities render their status in the result text and, for the
// native tools, in the details payload; accepting both keeps an MCP-provided
// symbols capability on the same rule without keying on a vendor tool name.
func structuralLookupMiss(outcome ToolOutcome) bool {
	if outcome.Result.IsError || outcome.Refused() {
		return false
	}
	details, _ := outcome.Result.Details.(map[string]any)
	status, _ := details["status"].(string)
	if strings.EqualFold(strings.TrimSpace(status), "not_found") {
		return true
	}
	return strings.Contains(strings.ToLower(outcome.Result.Text), "status: not_found")
}

// outcomeNamesArgument reports whether the call named one target value.
func outcomeNamesArgument(outcome ToolOutcome, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, raw := range outcome.Args {
		if text, ok := raw.(string); ok && strings.EqualFold(strings.TrimSpace(text), value) {
			return true
		}
	}
	details, _ := outcome.Result.Details.(map[string]any)
	for _, key := range []string{"name", "query", "selector", "value", "symbol"} {
		if text, ok := details[key].(string); ok && strings.EqualFold(strings.TrimSpace(text), value) {
			return true
		}
	}
	return strings.Contains(outcome.Result.Text, value)
}

// dropUnresolvedSemanticTargets rewrites the checks and issues for a named
// coordinate the runtime established is not in this repository. The target
// described the objective, not a coordinate the change had to touch, so it
// stops being a review boundary.
func dropUnresolvedSemanticTargets(evidence StructuralEvidence, unresolved map[string]bool) StructuralEvidence {
	if len(unresolved) == 0 {
		return evidence
	}
	for i := range evidence.Checks {
		check := &evidence.Checks[i]
		if !unresolved[check.Target.Value] {
			continue
		}
		if check.Target.Kind != contract.TargetSymbol && check.Target.Kind != contract.TargetPackage {
			continue
		}
		check.Status = TargetSemantic
		check.Detail = fmt.Sprintf(
			"%q is not a coordinate this repository contains; treated as not applicable", check.Target.Value)
	}
	if len(evidence.Issues) > 0 {
		kept := evidence.Issues[:0]
		for _, issue := range evidence.Issues {
			drop := false
			for value := range unresolved {
				if strings.Contains(issue, value) {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, issue)
			}
		}
		evidence.Issues = kept
	}
	return evidence
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
	// An informational objective produces an answer, not a checked change.
	// Its contract may name a Go path because the question is about that file,
	// but that does not turn the path into a verification scope. Requiring a
	// build/test after "what package is declared here?" was a false review.
	if intent.Kind == contract.ObjectiveInformational {
		return result
	}
	after := evidence.verificationsAfter(evidence.lastPotentialMutation)
	result.Verifications = len(after)
	for _, record := range after {
		class := VerificationStatic
		if record.evidence != nil {
			class = record.evidence.EffectiveClass()
		}
		switch class {
		case VerificationBehavioral:
			result.BehavioralVerifications++
		case VerificationTest:
			result.TestVerifications++
		default:
			result.StaticVerifications++
		}
	}

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
		case TargetSemantic:
			// A technology or repository target describes the result and the
			// scope. It is not a changed path, and requiring its name in a
			// filename would be a false target.
			add("semantic objective: " + string(check.Target.Kind) + " " + check.Target.Value)
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
	if len(structural.Unattributed) > 0 {
		assessment.Alignment = AlignmentUncertain
		assessment.Rationale = "a mutation could not be attributed to a path"
		add("unattributed: " + strings.Join(structural.Unattributed, ", "))
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

// maxReviewClauses bounds a review reason to the distinct findings a person
// can act on in one footer.
const maxReviewClauses = 3

// CompactReviewReason keeps the first distinct clauses of a verifier reason.
//
// It never clips a clause mid-sentence: a partial finding reads as a different
// claim from the one the runtime made. The cap is therefore applied only at a
// semicolon boundary, and duplicate clauses are dropped case-insensitively so
// repeated evidence does not crowd out a new violation.
func CompactReviewReason(reason string) string {
	clauses := make([]string, 0, maxReviewClauses)
	seen := make(map[string]bool, maxReviewClauses)
	for _, raw := range strings.Split(reason, ";") {
		clause := strings.TrimSpace(raw)
		if clause == "" {
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(clause), " "))
		if seen[key] {
			continue
		}
		seen[key] = true
		clauses = append(clauses, clause)
		if len(clauses) == maxReviewClauses {
			break
		}
	}
	return strings.Join(clauses, "; ")
}
