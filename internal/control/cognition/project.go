package cognition

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// ProjectorOptions bounds what a projection may retain.
type ProjectorOptions struct {
	// StateBlockBytes bounds the runtime state block. Zero uses the default.
	StateBlockBytes int
	// StateBlockItems bounds evidence lines in the state block.
	StateBlockItems int
	// ExactKeepBytes is the largest exact result sent without excerpting.
	//
	// The bound is per observation, deliberately: a result's wire form must be
	// a function of the result alone. Deciding it from the conversation's
	// current length (a "newest N" window, a total-size budget that a growing
	// history reallocates) means re-rendering an already-sent message on a
	// later turn, which invalidates every cached token after it. Aggregate
	// growth is compaction's job, where reduction is an explicit transition.
	ExactKeepBytes int
	// DiagnosticKeepBytes bounds an exact error or check result.
	DiagnosticKeepBytes int
	// ExternalKeepBytes bounds external content retained verbatim. Provenance
	// and the untrusted envelope are always preserved.
	ExternalKeepBytes int
	// StructuralKeepBytes bounds exact structural observations (grep, symbols,
	// graph, refs, structure). Coordinates are the payload the model needs to
	// avoid repeating the query; a 360-byte summary discards them and causes
	// the redundant exploration this cap exists to prevent.
	StructuralKeepBytes int
}

// DefaultProjectorOptions returns the built-in bounds.
func DefaultProjectorOptions() ProjectorOptions {
	return ProjectorOptions{
		StateBlockBytes: DefaultStateBlockBytes,
		StateBlockItems: DefaultStateBlockItems,
		// A source read is the evidence synthesis explains architecture from.
		// The evidence ledger already retains 16 KiB per exact observation, so
		// excerpting the same result to 4 KiB in the request was discarding the
		// implementation the answer had just read. The bound is still per
		// result, so the cache-stable wire form does not depend on history
		// length; compaction remains the aggregate bound.
		ExactKeepBytes:      16 * 1024,
		DiagnosticKeepBytes: 8192,
		ExternalKeepBytes:   4096,
		StructuralKeepBytes: 8192,
	}
}

// normalized fills non-positive options with their defaults.
func (o ProjectorOptions) normalized() ProjectorOptions {
	defaults := DefaultProjectorOptions()
	if o.StateBlockBytes <= 0 {
		o.StateBlockBytes = defaults.StateBlockBytes
	}
	if o.StateBlockItems <= 0 {
		o.StateBlockItems = defaults.StateBlockItems
	}
	if o.ExactKeepBytes <= 0 {
		o.ExactKeepBytes = defaults.ExactKeepBytes
	}
	if o.DiagnosticKeepBytes <= 0 {
		o.DiagnosticKeepBytes = defaults.DiagnosticKeepBytes
	}
	if o.ExternalKeepBytes <= 0 {
		o.ExternalKeepBytes = defaults.ExternalKeepBytes
	}
	if o.StructuralKeepBytes <= 0 {
		o.StructuralKeepBytes = defaults.StructuralKeepBytes
	}
	return o
}

// Projector builds a task-shaped view of the next model request.
//
// It owns the evidence state pointer it is given: the runtime observes every
// completed tool outcome into that state, and the task layer persists it at
// checkpoint boundaries. A Projector is used by one run goroutine and is not
// safe for concurrent use.
type Projector struct {
	// Shape is the task shape for this turn.
	Shape TaskShape
	// Evidence is the durable evidence state. It is updated in place.
	Evidence *EvidenceState
	// Facts is the cheap workspace probe.
	Facts RepositoryFacts
	// Options bounds the projection.
	Options ProjectorOptions
	// Advisor is optional promoted execution experience. It may reorder the
	// already-constructed tool surface and append bounded advisory hints. It
	// never widens the surface, grants authority, or changes evidence.
	Advisor ExperienceAdvisor

	// progress tracks consecutive completed turns that did not advance the
	// task. It is runtime-only state for one run; a new run starts fresh.
	progress progressTracker
	// preActionTurns counts projections spent observing before the requested
	// change or execution is applied. It is a semantic bound, separate from the
	// generic no-progress counter: novelty does not satisfy an action, so a
	// survey that keeps finding new files is still bounded. It covers the
	// explore and act stages together, because those stages name different
	// evidence states, not different budgets, and a per-stage window let one
	// phase quietly double the other.
	preActionTurns int
	// verifyTurns counts projections spent at the verification boundary
	// without a successful check.
	verifyTurns int
	// conclusionTurns counts projections whose only missing requirement is the
	// terminal investigation verdict.
	conclusionTurns int
}

// progressTracker is the semantic no-progress circuit breaker.
//
// The first projection seeds the comparison without counting a stall, because
// a resumed run is expected to re-establish its immediate context. A later
// turn is progress only when the runtime can name what changed; repeated
// observation of the same boundary is not.
type progressTracker struct {
	seeded   bool
	lastTurn int
	stage    Stage
	missing  string
	kind     BlockKind
	stalled  int
}

// trackProgress records whether the completed turns since the last projection
// changed the task boundary, and reports what those turns did.
//
// During verification, novelty is deliberately not progress: the task already
// has a change waiting for its check, and another read cannot close that
// obligation. A successful check or a corrective change is progress. Outside
// verification, new evidence and a changed stage/requirement/blocker are
// progress, so ordinary exploration is not cut short.
//
// Waiting at the verification boundary is bounded by verifyTurns, a dedicated
// window that restarts on a new change. The generic counter must not also count
// those turns: with a smaller bound it closed the surface after four reads
// while the model was still implementing the change, before the window that
// exists to bound exactly that wait could end.
func (p *Projector) trackProgress(turns []agent.Turn, suff Sufficiency, novel int) turnActivity {
	tracker := &p.progress
	var activity turnActivity
	if !tracker.seeded {
		tracker.seeded = true
		tracker.lastTurn = len(turns)
		tracker.stage = suff.Stage
		tracker.missing = strings.Join(suff.Missing, "\x00")
		tracker.kind = suff.Kind
		return activity
	}
	if len(turns) <= tracker.lastTurn {
		return activity
	}
	completed := turns[tracker.lastTurn:]
	advanced := false
	for _, turn := range completed {
		for _, outcome := range turn.Results {
			if outcome.Refused() || outcome.Result.IsError {
				continue
			}
			if outcome.Result.Mutates || (outcome.Authorization != nil && outcome.Authorization.Mutates) {
				activity.mutated = true
			}
			if outcome.Result.Mutates || outcome.Result.Verifies {
				advanced = true
			}
		}
	}
	if suff.Stage != tracker.stage || strings.Join(suff.Missing, "\x00") != tracker.missing ||
		suff.Kind != tracker.kind ||
		(suff.Stage != StageVerify && novel > 0) {
		advanced = true
	}
	if advanced || suff.Stage == StageVerify {
		tracker.stalled = 0
	} else {
		tracker.stalled++
	}
	tracker.lastTurn = len(turns)
	tracker.stage = suff.Stage
	tracker.missing = strings.Join(suff.Missing, "\x00")
	tracker.kind = suff.Kind
	return activity
}

// turnActivity is what the completed turns definitely did, as opposed to what
// the model said about them. A stage-scoped bound restarts on it: a change is
// new work, so the wait for its check starts over.
type turnActivity struct {
	// mutated reports a change the runtime admitted and observed.
	mutated bool
}

// stall applies the semantic stop to a sufficiency verdict that has not
// advanced for the configured number of turns.
func (p *Projector) stall(suff *Sufficiency) bool {
	if suff == nil || p.progress.stalled < MaxConsecutiveNoProgress {
		return false
	}
	suff.State = Blocked
	suff.Stage = StageBlocked
	suff.Kind = BlockProgressStalled
	suff.Reasons = append(suff.Reasons,
		fmt.Sprintf("no meaningful task progress for %d consecutive turns: no change, verification, required evidence, or blocker advanced the task", p.progress.stalled))
	suff.Next = []string{"report the remaining evidence gap and require review; do not repeat the same observation"}
	return true
}

// preActionObservation reports whether this projection is still observing
// before the change or execution the request asked for.
//
// The explore and act stages name different evidence states, not different
// budgets: in both the deliverable is an action, and another observation cannot
// satisfy it. Counting them separately would let a stage transition reset the
// window, so one phase could spend the budget twice.
func preActionObservation(shape TaskShape, suff Sufficiency) bool {
	if suff.Kind != BlockNone {
		return false
	}
	if shape.Kind != contract.ObjectiveMutation && shape.Kind != contract.ObjectiveExecution {
		return false
	}
	return suff.Stage == StageExplore || suff.Stage == StageAct
}

// preActionDeliverable names the action the observation window is waiting for.
func preActionDeliverable(shape TaskShape) string {
	if shape.Kind == contract.ObjectiveExecution {
		return "execution"
	}
	return "change"
}

// NewProjector returns a projector for one turn.
func NewProjector(shape TaskShape, evidence *EvidenceState, facts RepositoryFacts) *Projector {
	if evidence == nil {
		evidence = NewEvidenceState()
	}
	return &Projector{Shape: shape, Evidence: evidence, Facts: facts, Options: DefaultProjectorOptions()}
}

// Project implements the agent projection seam.
func (p *Projector) Project(req agent.ProjectionRequest) agent.ProjectionResult {
	if p == nil {
		return agent.ProjectionResult{}
	}
	if p.Evidence == nil {
		p.Evidence = NewEvidenceState()
	}
	options := p.Options.normalized()
	shape := p.Shape
	// The grant snapshot was taken at the turn boundary. Fold the run's own
	// consumption into it so a near-exhausted grant narrows the surface before
	// the loop has to refuse a call.
	shape.Budget.TurnsUsed += len(req.Turns)
	shape.Budget.ToolCallsUsed += executionToolCalls(req.Turns)

	observed, novel := p.Evidence.ObserveTurns(shape, req.Turns)
	// The durable evidence, not the current run alone, is the execution state.
	// A resumed task that already applied a change must stay in verification
	// even though this run starts with no mutation of its own.
	execution := ExecutionFromEvidence(p.Evidence)
	// Capability availability is a fact about the runtime, not about this
	// turn's evidence, so it is resolved before any stage or budget narrowing.
	// A gap here means no later turn can succeed either.
	available := agent.AvailableOperations(req.Tools)
	gaps := CapabilityGap(shape, available)
	// Sufficiency is an evidence verdict, not a resource verdict. Budget
	// exhaustion withholds tools (Candidates checks it directly), but a
	// proven change followed by a successful check must not be downgraded just
	// because the grant ran out at the closing answer.
	suff := EvaluateSufficiency(shape, p.Evidence, execution, false)
	// An external requirement with no external capability in the constructed
	// action space cannot be satisfied by another model turn. Treat it as the
	// capability failure it is, so the model gets one report opportunity and
	// the terminal reason names the real gap instead of a generic empty surface.
	if onlyExternalRemains(shape, suff) && !hasExternalCapability(req.Tools) {
		suff.State = Blocked
		suff.Stage = StageBlocked
		suff.Kind = BlockCapabilityFailure
		suff.Reasons = append([]string{
			"the request requires external knowledge but no external capability is available",
		}, suff.Reasons...)
		suff.Missing = []string{"external_observation"}
	}
	// A run that keeps observing without changing the task is stopped on its
	// own semantic bound, not left to the model-turn budget. The check is
	// independent of the capability-failure boundary above: that one says a
	// required capability cannot run, this one says the last several actions
	// did not advance the task.
	//
	// Before that verdict closes the surface, ask whether the failed provider
	// was the only authorized acquisition path. A structural query that fails
	// does not make README, manifests, root listings, or source reads
	// unavailable; only a genuine absence of every provider is a task block.
	suff = ReconcileAcquisition(p.Evidence, req.Tools, suff)
	// The workspace probe measured whether this process can list the root at
	// all. That is an environment fact, not a provider failure, so it is
	// applied after the acquisition reconciliation: no remaining provider can
	// produce a listing, and the model must be told which restriction blocks it
	// rather than being left to discover it one failing call at a time.
	suff = workspaceUnreadable(p.Facts, suff)
	activity := p.trackProgress(req.Turns, suff, novel)
	p.stall(&suff)
	if len(suff.Missing) == 1 && suff.Missing[0] == "investigation_conclusion" &&
		suff.State == Insufficient && suff.Stage != StageAnswer && suff.Stage != StageComplete {
		// An explicit progress observation is not a terminal attempt and must
		// not consume the conclusion window. The model is allowed to say "still
		// investigating" without being pushed one turn closer to a forced
		// verdict by the act of saying it.
		// Progress is free while the window is open, but it cannot extend an
		// already-exhausted window indefinitely: at that point it is a report
		// boundary, not another investigation attempt.
		if !lastTurnWasConclusionProgress(req.Turns) ||
			p.conclusionTurns >= MaxConclusionObservationTurns {
			p.conclusionTurns++
		}
		if p.conclusionTurns > MaxConclusionObservationTurns {
			suff.State = Blocked
			suff.Stage = StageBlocked
			suff.Kind = BlockProgressStalled
			suff.Reasons = append(suff.Reasons,
				fmt.Sprintf("the investigation conclusion was requested for %d turns; another observation cannot satisfy it", p.conclusionTurns))
			suff.Next = []string{"call conclude with status FINAL, " +
				contract.ConclusionDefect.VerdictList() +
				" and the evidence already established; no further survey is required"}
		}
	} else if suff.Missing == nil || len(suff.Missing) != 1 || suff.Missing[0] != "investigation_conclusion" {
		p.conclusionTurns = 0
	}
	// Observation before an action is bounded even when every turn finds a new
	// path: novelty proves the search is working, not that the objective needs
	// it, and another observation cannot apply the requested change. The
	// window is a share of the remaining model-turn grant, so it ends the shape
	// that walks a filesystem one directory at a time without ending a
	// legitimate first pass over a multi-file project. It is deliberately not a
	// wall-clock limit.
	if preActionObservation(shape, suff) {
		p.preActionTurns++
		if p.preActionTurns > PreActionObservationAllowance(shape) {
			suff.State = Blocked
			suff.Stage = StageBlocked
			suff.Kind = BlockProgressStalled
			suff.Reasons = append(suff.Reasons,
				fmt.Sprintf("the requested %s was not performed in %d observation turns; further evidence cannot be acquired by another survey",
					preActionDeliverable(shape), p.preActionTurns))
			suff.Next = []string{"perform the requested action with the tools already offered, or report the remaining gap and the work performed"}
		}
	} else if suff.Stage != StageExplore && suff.Stage != StageAct {
		p.preActionTurns = 0
	}
	// Verification has a finite budget too. If the check has not succeeded
	// after the allowed attempts, another turn cannot establish it; the
	// truthful outcome is a report of the change and the missing verification,
	// not more exploration. A new change is new work, so it restarts the wait:
	// the window bounds waiting, not iteration. This is the only bound at the
	// verification boundary; the generic no-progress counter is reset there
	// rather than closing the surface first.
	if activity.mutated {
		p.verifyTurns = 0
	}
	if suff.Stage == StageVerify && suff.Kind == BlockNone {
		p.verifyTurns++
		if p.verifyTurns > MaxVerifyObservationTurns {
			suff.State = Blocked
			suff.Stage = StageBlocked
			suff.Kind = BlockProgressStalled
			suff.Reasons = append(suff.Reasons,
				fmt.Sprintf("the requested verification was not established in %d verification turns; another turn cannot establish it", p.verifyTurns))
			suff.Next = []string{"report the change and the missing verification; do not repeat the same observation or check"}
			// The lifecycle needs a semantic outcome, not only a stop reason:
			// a run that ends here must report UNRESOLVED even when the model
			// never produces a closing answer. The conclusion is evidence
			// that the check is still missing, never a failure of the change.
			suff.Conclusion = &agent.RunConclusion{
				Status: agent.ConclusionUnresolved,
				Reason: agent.ConclusionVerificationNotEstablished,
				Summary: fmt.Sprintf(
					"the requested verification was not established in %d verification turns. No further turn in this run can establish it; the task remains resumable.",
					p.verifyTurns),
			}
		}
	} else if suff.Stage != StageVerify {
		p.verifyTurns = 0
	}
	failures := p.Evidence.FailureState()
	candidates := Candidates(shape, p.Evidence, execution, req.Tools, p.Facts, suff)
	// At the end of the conclusion window, make the transition enforceable
	// rather than advisory. A provider may ignore tool_choice; narrowing the
	// surface means a continued read is refused by the loop instead of
	// consuming the last turn on another survey.
	if p.conclusionTurns >= MaxConclusionObservationTurns &&
		suff.State == Insufficient && len(suff.Missing) == 1 &&
		suff.Missing[0] == "investigation_conclusion" {
		if forced := toolNamed(req.Tools, "conclude"); forced != nil {
			for _, tool := range candidates.Tools {
				if tool.Name() != forced.Name() {
					candidates.Excluded = append(candidates.Excluded, agent.CandidateExclusion{
						Tool: tool.Name(), Reason: "the investigation conclusion window is closed; only the terminal conclusion is accepted",
					})
				}
			}
			candidates.Tools = []agent.Tool{forced}
		}
	}
	advice := adviceFor(p.Advisor, shape, p.Evidence, execution, suff, candidates.Tools, req.Turns)
	if line := preferredToolHint(advice.PreferredTools, candidates.Tools); line != "" {
		// The advisor cannot add a tool, and it does not reorder one either:
		// the preference travels in the runtime block so the stable tool
		// definitions stay byte-identical across turns.
		advice.Hints = append([]string{line}, advice.Hints...)
	}
	// A repeated observation with required evidence still missing is not a
	// reason to close the action surface: the contract is not satisfied, and
	// forcing an answer would only produce a completion veto. The first turn of
	// a run is not judged because a model may be re-establishing context from
	// persisted evidence. After that, a no-gain turn escalates with the exact
	// missing requirement instead of blindly re-offering the same coordinates.
	// Closure remains stage-driven (answer/complete/blocked), never novelty.
	if shape.Kind == contract.ObjectiveInformational && len(req.Turns) > 1 &&
		observed > 0 && novel == 0 && len(suff.Missing) > 0 &&
		(suff.State == Insufficient || suff.State == Conflicting) {
		suff.Reasons = append(suff.Reasons,
			"the last observation added no new evidence; repeating the same coordinate cannot close the remaining gap")
		if len(suff.Next) == 0 {
			for _, missing := range suff.Missing {
				suff.Next = append(suff.Next, nextEvidence(missing))
			}
		}
	}

	// The loop refused the last proposals and the model still did not answer.
	// No action can be accepted, so keeping the probe surface open only invites
	// another refusal. Close it and make the answer the only remaining output;
	// the answer is still a completion candidate, not an automatic success.
	if req.ForceAnswer {
		// A nil slice would ask the loop to keep its full tool set. Use an
		// empty non-nil slice so the projection actually closes the surface.
		candidates.Tools = []agent.Tool{}
		candidates.AnswerOnly = true
		suff.Next = nil
	}
	// The previous answer failed review: the loop reopens the constructed
	// surface for this one request, so the runtime block must describe the
	// same tools. A terminal or missing-capability verdict is not overridden;
	// no call could close it, and the loop keeps the surface closed.
	correcting := false
	correctionSpent := false
	if req.Correcting && len(gaps) == 0 &&
		suff.Kind != BlockCapabilityFailure && suff.Kind != BlockProgressStalled {
		if req.CorrectionBudget <= 0 {
			// The allowance belongs to the answer lifecycle. Once it is spent,
			// the loop refuses every non-terminal call, so offering the
			// surface would advertise a capability the next call cannot use.
			// Close it and make the answer the only remaining output.
			correctionSpent = true
		} else {
			// Remove only the answer-boundary closure: authority, scope, language,
			// and budget exclusions still apply, so the reopened surface is the
			// same one the task shape permits, not the full constructed set.
			offered, excluded := correctionCandidates(shape, execution, req.Tools, p.Facts, suff)
			if len(offered) > 0 {
				candidates.Tools = offered
				candidates.Excluded = excluded
				candidates.Notices = nil
				candidates.AnswerOnly = false
				suff.Next = nil
				correcting = true
			}
		}
	}
	if correctionSpent && len(candidates.Tools) > 0 {
		// The allowance buys evidence, not bookkeeping. Keeping the plan tool
		// lets the model reconcile an open item before it answers; withdrawing
		// it here left the durable task refusing a completion the model could
		// no longer reach.
		kept := agent.OpenPlanTools(candidates.Tools)
		keptNames := make(map[string]bool, len(kept))
		for _, tool := range kept {
			keptNames[tool.Name()] = true
		}
		for _, tool := range candidates.Tools {
			if keptNames[tool.Name()] {
				continue
			}
			candidates.Excluded = append(candidates.Excluded, agent.CandidateExclusion{
				Tool:   tool.Name(),
				Reason: "the correction tool allowance is spent; answer from the evidence already observed",
			})
		}
		candidates.Tools = kept
		candidates.AnswerOnly = len(kept) == 0
		suff.Next = nil
	}

	messages, report := projectMessages(req.Messages, p.Evidence, options)
	// A correction suppresses the generic evidence frontier for the same
	// reason the candidate surface is bounded: the review already named the
	// claims to repair, and an unrelated component list competes with that
	// instruction instead of narrowing it.
	state := renderState(shape, p.Evidence, execution, suff, candidates,
		options.StateBlockBytes, options.StateBlockItems, !req.Correcting)
	if correctionSpent {
		// The allowance is enforced before execution. State the consequence,
		// not the control-plane counter: a model told how many calls remain
		// spends reasoning on the budget instead of the evidence.
		if len(agent.OpenPlanItems(candidates.Tools)) > 0 {
			state += "The repair window is closed. Reconcile the plan, then answer from the evidence already observed; qualify or remove any claim that remains unsupported.\n"
		} else {
			state += "The repair window is closed. Answer from the evidence already observed; qualify or remove any claim that remains unsupported.\n"
		}
	}
	if len(advice.Hints) > 0 {
		state = appendExperienceHints(state, advice.Hints, options.StateBlockBytes)
	}

	report.Turn = req.Turn
	report.Messages = len(messages)
	report.ElidedToolResults = len(report.Elisions)
	report.StateBlockBytes = len(state)
	report.ToolNames = toolNames(candidates.Tools)
	report.Excluded = candidates.Excluded
	report.Redundant = candidates.Redundant
	report.ExperienceApplied = append([]string(nil), advice.Matched...)
	report.ExperienceHints = append([]string(nil), advice.Hints...)
	report.RequiredCapabilities = capabilityLabels(shape.RequiredCapabilities)
	report.AvailableOperations = operationLabels(available)
	report.MissingCapabilities = capabilityLabels(gaps)
	// A required capability the runtime cannot perform closes the action
	// surface for the same reason a completed objective does: no proposal can
	// make progress. The loop refuses to send the request at all, so the model
	// is never asked to explain a limit it cannot act on.
	if len(gaps) > 0 {
		candidates.Tools = []agent.Tool{}
		candidates.AnswerOnly = true
	}
	report.Sufficiency = string(suff.State)
	report.Stage = string(suff.Stage)
	report.Progress = "advancing"
	if p.progress.stalled > 0 {
		report.Progress = "stalled"
	}
	report.NoProgressTurns = p.progress.stalled
	report.ExplorationTurns = p.preActionTurns
	report.VerificationTurns = p.verifyTurns
	// An informational task whose required evidence is known is done
	// observing. If the model answers in the same turn as a tool call, the
	// answer is final and the call is continuation, not evidence gathering.
	// A blocked capability has the same answer boundary: the runtime cannot
	// acquire more evidence, so the next visible text is the candidate report.
	//
	// A complete lifecycle is the same boundary for every objective kind. The
	// candidates normally close the surface at StageComplete, but this makes
	// the closure explicit: a completed objective is answer-only by definition,
	// so a continuation call can never be dispatched even if some future
	// narrowing leaves a tool behind.
	report.AnswerEligible = req.ForceAnswer || informationalEvidenceBoundary(shape, suff) ||
		suff.Stage == StageComplete ||
		(shape.Kind == contract.ObjectiveInformational && suff.Kind == BlockCapabilityFailure)
	if correcting {
		// A tool call during the correction request is evidence gathering,
		// not continuation after a finished answer.
		report.AnswerEligible = false
	}
	if correctionSpent {
		// No action remains, so the next visible text is the candidate answer,
		// not narration preceding a call the loop would refuse.
		report.AnswerEligible = true
	}
	report.Terminal = suff.Kind == BlockCapabilityFailure || suff.Kind == BlockProgressStalled
	report.ObjectiveComplete = suff.Stage == StageComplete
	report.Missing = append([]string(nil), suff.Missing...)
	report.Unresolved = append([]string(nil), suff.Unresolved...)
	report.VerificationLimited = suff.VerificationLimited
	if suff.Conclusion != nil {
		conclusion := *suff.Conclusion
		report.Conclusion = &conclusion
	}
	report.Facets = facetReportRows(suff.Facets)
	if suff.RequiredFacets > 0 {
		report.FacetCoverage = fmt.Sprintf("%d/%d", suff.SatisfiedFacets, suff.RequiredFacets)
	}
	switch {
	case len(gaps) > 0:
		// The capability verdict is the actual reason this request cannot
		// run, so it takes precedence over a secondary evidence verdict.
		report.StopReason = capabilityGapReason(gaps)
	case suff.Kind == BlockProgressStalled:
		report.StopReason = "no meaningful task progress: " + strings.Join(suff.Reasons, "; ")
	case report.Terminal:
		report.StopReason = "the task is blocked: " + strings.Join(suff.Reasons, "; ")
	}
	report.Provenance = p.Evidence.Provenance(shape)
	report.Shape = shape.Summary()
	report.Evidence = evidenceSummary(p.Evidence, suff, execution)
	report.RetentionPolicy = fmt.Sprintf(
		"each result bounded once by its own size: exact <=%d bytes, diagnostics <=%d bytes, external <=%d bytes, structural <=%d bytes then excerpted; derived/transient results summarized; aggregate growth is compaction's job",
		options.ExactKeepBytes, options.DiagnosticKeepBytes, options.ExternalKeepBytes, options.StructuralKeepBytes)
	if len(report.Elisions) == 0 && state == "" && len(candidates.Tools) == len(req.Tools) {
		report.Reason = "no projection change"
	}
	if correcting {
		report.Reason = "answer correction: the permitted action surface is reopened for the evidence the review named"
	}
	if correctionSpent {
		report.Reason = "answer correction: the tool allowance is spent; answer from the evidence already observed"
	}

	result := agent.ProjectionResult{
		Messages: messages,
		Tools:    candidates.Tools,
		Refusals: failures.Refusals,
		Report:   report,
	}
	if p.conclusionTurns >= MaxConclusionObservationTurns &&
		len(suff.Missing) == 1 && suff.Missing[0] == "investigation_conclusion" {
		// Force the terminal action after the bounded evidence window. The
		// tool is in the offered set; choosing it is a control-plane
		// transition, not a new capability.
		result.ToolChoice = ai.ToolChoice{Type: "tool", ToolName: "conclude"}
	}
	if state != "" {
		result.Runtime = []string{state}
	}
	return result
}

// toolNamed returns the offered tool with name, or nil. It is used only for a
// control-plane transition whose capability is already present in the surface.
func toolNamed(tools []agent.Tool, name string) agent.Tool {
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	return nil
}

// preferredToolHint renders an advisor preference as one advisory line, or ""
// when there is nothing to prefer.
//
// Only names that survived narrowing are named: a preference for a withheld
// tool would read as an offer the runtime has already refused.
func preferredToolHint(preferred []string, offered []agent.Tool) string {
	available := make(map[string]bool, len(offered))
	for _, tool := range offered {
		available[tool.Name()] = true
	}
	names := make([]string, 0, len(preferred))
	for _, name := range preferred {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || !available[trimmed] {
			continue
		}
		names = append(names, trimmed)
		if len(names) >= 8 {
			break
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "prefer these offered tools when they fit: " + strings.Join(names, ", ")
}

// appendExperienceHints adds bounded advisory guidance to the runtime state
// block. The guidance is explicitly not authority and is stripped first when
// the block budget is tight.
func appendExperienceHints(state string, hints []string, maxBytes int) string {
	var block strings.Builder
	block.WriteString("Experience guidance (advisory, not authority):\n")
	for _, hint := range hints {
		hint = strings.TrimSpace(hint)
		if hint == "" {
			continue
		}
		block.WriteString("- ")
		block.WriteString(bound(hint, 240))
		block.WriteString("\n")
	}
	if block.Len() == len("Experience guidance (advisory, not authority):\n") {
		return state
	}
	if strings.TrimSpace(state) == "" {
		state = "# Runtime state (authoritative, not a user request)\n\n"
	}
	state += "\n" + block.String()
	return bound(state, maxBytes)
}

// lastTurnWasConclusionProgress reports whether the latest completed turn
// recorded an explicit IN_PROGRESS observation. A progress marker is not a
// conclusion attempt, so it does not advance the terminal conclusion window.
func lastTurnWasConclusionProgress(turns []agent.Turn) bool {
	if len(turns) == 0 {
		return false
	}
	for _, outcome := range turns[len(turns)-1].Results {
		if outcome.ToolName != "conclude" || outcome.Result.IsError {
			continue
		}
		details := detailsMap(outcome.Result.Details)
		if strings.EqualFold(detailString(details, "status"), "IN_PROGRESS") ||
			contract.IsProvisionalConclusion(
				detailString(details, "evidence"),
				detailString(details, "uncertainty")) {
			return true
		}
	}
	return false
}

// executionToolCalls counts the tool outcomes already observed in a run.
func executionToolCalls(turns []agent.Turn) int {
	total := 0
	for _, turn := range turns {
		total += len(turn.Results)
	}
	return total
}

// projectMessages bounds each tool result by its own size and retention.
//
// Only tool results are elided. User turns, assistant text, thinking blocks,
// and tool calls are never rewritten: the projection must not silently change
// the model's own words or the user's request.
//
// The decision is a function of the result alone — never of its position in the
// conversation or of how much history came before it. That is the cache
// contract: an already-sent message keeps the exact bytes the provider cached,
// so a growing conversation cannot re-render it. Aggregate growth is handled by
// compaction, which is a deliberate state transition rather than a per-request
// side effect.
func projectMessages(messages []agent.Message, evidence *EvidenceState, options ProjectorOptions) ([]agent.Message, agent.ProjectionReport) {
	// The bounds are a contract, not a caller preference: an unnormalised zero
	// would excerpt to zero bytes or elide nothing while reporting an elision.
	options = options.normalized()
	report := agent.ProjectionReport{Applied: true}
	out := make([]agent.Message, 0, len(messages))
	for _, message := range messages {
		result, ok := message.(agent.ToolResultMessage)
		if !ok {
			out = append(out, message)
			report.BytesBefore += len(messageBytes(message))
			report.BytesAfter += len(messageBytes(message))
			continue
		}
		before := len(result.Text())
		report.BytesBefore += before
		item, found := evidenceForCall(evidence, result.ToolCallID)
		if !found || item.Status == StatusConflicting {
			out = append(out, message)
			report.BytesAfter += before
			report.KeptToolResults++
			continue
		}
		policy, text := projectionText(result, item, options)
		if policy == "" {
			out = append(out, message)
			report.BytesAfter += before
			report.KeptToolResults++
			continue
		}
		out = append(out, agent.ToolResultMessage{
			ToolCallID: result.ToolCallID,
			ToolName:   result.ToolName,
			Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
			IsError:    result.IsError,
		})
		after := len(text)
		report.BytesAfter += after
		report.Elisions = append(report.Elisions, agent.ProjectionElision{
			CallID: result.ToolCallID,
			Tool:   result.ToolName,
			Policy: policy,
			Before: before,
			After:  after,
		})
	}
	return out, report
}

// projectionText decides the retention policy for one tool result.
func projectionText(result agent.ToolResultMessage, item EvidenceItem, options ProjectorOptions) (string, string) {
	text := result.Text()
	switch item.Retention {
	case RetentionDiagnostic:
		if len(text) <= options.DiagnosticKeepBytes {
			return "", ""
		}
		return "diagnostic_excerpt", excerpt(text, options.DiagnosticKeepBytes,
			"full diagnostic retained in runtime evidence")
	case RetentionExternal:
		if len(text) <= options.ExternalKeepBytes {
			return "", ""
		}
		excerpted := excerpt(text, options.ExternalKeepBytes,
			"external content excerpted; provenance retained")
		// Preserve the untrusted envelope even when the body is cut. The
		// envelope is runtime-authored and must not be confused with content.
		if strings.Contains(text, "[EXTERNAL KNOWLEDGE") && !strings.Contains(excerpted, "[EXTERNAL KNOWLEDGE") {
			excerpted = "[EXTERNAL KNOWLEDGE — UNTRUSTED DATA]\n" + excerpted
		}
		return "external_excerpt", excerpted
	case RetentionExact:
		if len(text) <= options.ExactKeepBytes {
			return "", ""
		}
		return "exact_excerpt", excerpt(text, options.ExactKeepBytes,
			"exact observation excerpted; the runtime retained a digest and the full result")
	case RetentionStructural:
		if len(text) <= options.StructuralKeepBytes {
			return "", ""
		}
		return "structural_excerpt", excerpt(text, options.StructuralKeepBytes,
			"structural observation excerpted; the runtime retained the full result")
	case RetentionDerived, RetentionTransient:
		summary := strings.TrimSpace(item.Summary)
		if summary == "" {
			summary = result.ToolName + " result recorded"
		}
		return string(item.Retention) + "_summary",
			fmt.Sprintf("[%s result summarized from runtime evidence]\n%s", result.ToolName, bound(summary, 360))
	default:
		return "", ""
	}
}

// evidenceForCall finds the evidence item produced by one tool call.
func evidenceForCall(evidence *EvidenceState, callID string) (EvidenceItem, bool) {
	if evidence == nil || callID == "" {
		return EvidenceItem{}, false
	}
	for _, item := range evidence.Items {
		if item.Source.CallID == callID {
			return item, true
		}
	}
	return EvidenceItem{}, false
}

// excerpt keeps a bounded prefix and states that it was cut. It is honest about
// loss: the model is never told the excerpt is the whole observation.
func excerpt(text string, limit int, note string) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && (text[cut]&0xc0) == 0x80 {
		cut--
	}
	return strings.TrimSpace(text[:cut]) +
		fmt.Sprintf("\n\n[%s; %d of %d bytes shown]", note, cut, len(text))
}

// messageBytes returns the text bytes of one message.
func messageBytes(message agent.Message) string {
	switch typed := message.(type) {
	case agent.UserMessage:
		return typed.Text
	case agent.AssistantMessage:
		return typed.Message.Text()
	case agent.ToolResultMessage:
		return typed.Text()
	default:
		return ""
	}
}

// toolNames returns the names of a tool set in order.
func toolNames(tools []agent.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}

// evidenceSummary renders a compact audit line.
func evidenceSummary(evidence *EvidenceState, suff Sufficiency, execution ExecutionState) string {
	if evidence == nil {
		return ""
	}
	known := 0
	for _, item := range evidence.Items {
		if item.Status == StatusKnown || item.Status == StatusDerived {
			known++
		}
	}
	return fmt.Sprintf("%d known, %d requirements, %d missing, %d conflicting, %d tool calls, mutated=%t verified=%t",
		known, len(evidence.Requirements), len(suff.Missing), len(evidence.Conflicts()),
		execution.ToolCalls, execution.Mutated, execution.VerifiedAfterMutation)
}
