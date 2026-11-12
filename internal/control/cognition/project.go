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
	// KeepRecentToolResults is the number of trailing tool results always sent
	// verbatim, so the model keeps its immediate working set.
	KeepRecentToolResults int
	// ExactKeepBytes is the largest exact result sent without excerpting.
	ExactKeepBytes int
	// DiagnosticKeepBytes bounds an exact error or check result.
	DiagnosticKeepBytes int
	// ExternalKeepBytes bounds external content retained verbatim. Provenance
	// and the untrusted envelope are always preserved.
	ExternalKeepBytes int
	// ToolResultBudgetBytes bounds the total exact tool-result bytes in one
	// projected request. The newest results are retained first; older exact
	// observations become evidence summaries. Zero uses the default.
	ToolResultBudgetBytes int
}

// DefaultProjectorOptions returns the built-in bounds.
func DefaultProjectorOptions() ProjectorOptions {
	return ProjectorOptions{
		StateBlockBytes:       DefaultStateBlockBytes,
		StateBlockItems:       DefaultStateBlockItems,
		KeepRecentToolResults: 2,
		ExactKeepBytes:        4096,
		DiagnosticKeepBytes:   8192,
		ExternalKeepBytes:     4096,
		ToolResultBudgetBytes: 16 * 1024,
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
	if o.KeepRecentToolResults < 0 {
		o.KeepRecentToolResults = defaults.KeepRecentToolResults
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
	if o.ToolResultBudgetBytes <= 0 {
		o.ToolResultBudgetBytes = defaults.ToolResultBudgetBytes
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
// changed the task boundary.
//
// During verification, novelty is deliberately not progress: the task already
// has a change waiting for its check, and another read cannot close that
// obligation. A successful check or a corrective change is progress. Outside
// verification, new evidence and a changed stage/requirement/blocker are
// progress, so ordinary exploration is not cut short.
func (p *Projector) trackProgress(turns []agent.Turn, suff Sufficiency, novel int) {
	tracker := &p.progress
	if !tracker.seeded {
		tracker.seeded = true
		tracker.lastTurn = len(turns)
		tracker.stage = suff.Stage
		tracker.missing = strings.Join(suff.Missing, "\x00")
		tracker.kind = suff.Kind
		return
	}
	if len(turns) <= tracker.lastTurn {
		return
	}
	completed := turns[tracker.lastTurn:]
	advanced := false
	for _, turn := range completed {
		for _, outcome := range turn.Results {
			if outcome.Refused() || outcome.Result.IsError {
				continue
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
	if advanced {
		tracker.stalled = 0
	} else {
		tracker.stalled++
	}
	tracker.lastTurn = len(turns)
	tracker.stage = suff.Stage
	tracker.missing = strings.Join(suff.Missing, "\x00")
	tracker.kind = suff.Kind
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
	p.trackProgress(req.Turns, suff, novel)
	p.stall(&suff)
	failures := p.Evidence.FailureState()
	candidates := Candidates(shape, p.Evidence, execution, req.Tools, p.Facts, suff)
	advice := adviceFor(p.Advisor, shape, p.Evidence, execution, suff, candidates.Tools, req.Turns)
	if len(advice.PreferredTools) > 0 {
		// The advisor cannot add a tool: orderCandidateTools only reorders the
		// filtered surface Candidates already decided.
		candidates.Tools = orderCandidateTools(candidates.Tools, advice.PreferredTools)
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

	messages, report := projectMessages(req.Messages, p.Evidence, options)
	state := RenderState(shape, p.Evidence, execution, suff, candidates,
		options.StateBlockBytes, options.StateBlockItems)
	if len(advice.Hints) > 0 {
		state = appendExperienceHints(state, advice.Hints, options.StateBlockBytes)
	}

	report.Turn = req.Turn
	report.Messages = len(messages)
	report.ElidedToolResults = len(report.Elisions)
	report.StateBlockBytes = len(state)
	report.ToolNames = toolNames(candidates.Tools)
	report.Excluded = candidates.Excluded
	report.ExperienceApplied = append([]string(nil), advice.Matched...)
	report.ExperienceHints = append([]string(nil), advice.Hints...)
	report.Sufficiency = string(suff.State)
	report.Stage = string(suff.Stage)
	report.Progress = "advancing"
	if p.progress.stalled > 0 {
		report.Progress = "stalled"
	}
	report.NoProgressTurns = p.progress.stalled
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
	report.Terminal = suff.Kind == BlockCapabilityFailure || suff.Kind == BlockProgressStalled
	switch {
	case suff.Kind == BlockProgressStalled:
		report.StopReason = "no meaningful task progress: " + strings.Join(suff.Reasons, "; ")
	case report.Terminal:
		report.StopReason = "the task is blocked: " + strings.Join(suff.Reasons, "; ")
	}
	report.Provenance = p.Evidence.Provenance(shape)
	report.Shape = shape.Summary()
	report.Evidence = evidenceSummary(p.Evidence, suff, execution)
	report.RetentionPolicy = fmt.Sprintf(
		"newest %d tool result(s) exact; %d-byte exact working set; diagnostics <=%d bytes; external <=%d bytes; structural/derived results summarized",
		options.KeepRecentToolResults, options.ToolResultBudgetBytes,
		options.DiagnosticKeepBytes, options.ExternalKeepBytes)
	if len(report.Elisions) == 0 && state == "" && len(candidates.Tools) == len(req.Tools) {
		report.Reason = "no projection change"
	}

	result := agent.ProjectionResult{
		Messages: messages,
		Tools:    candidates.Tools,
		Refusals: failures.Refusals,
		Report:   report,
	}
	if state != "" {
		result.System = []ai.SystemPromptBlock{{Text: state}}
	}
	return result
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

// executionToolCalls counts the tool outcomes already observed in a run.
func executionToolCalls(turns []agent.Turn) int {
	total := 0
	for _, turn := range turns {
		total += len(turn.Results)
	}
	return total
}

// projectMessages replaces bounded tool results with evidence summaries while
// preserving message order and tool-call/result pairing.
//
// Only tool results are elided. User turns, assistant text, thinking blocks,
// and tool calls are never rewritten: the projection must not silently change
// the model's own words or the user's request. The last KeepRecentToolResults
// are always retained verbatim.
func projectMessages(messages []agent.Message, evidence *EvidenceState, options ProjectorOptions) ([]agent.Message, agent.ProjectionReport) {
	report := agent.ProjectionReport{Applied: true}
	out := make([]agent.Message, 0, len(messages))
	keepExact := exactWorkingSet(messages, evidence, options)
	totalToolResults := 0
	for _, message := range messages {
		if _, ok := message.(agent.ToolResultMessage); ok {
			totalToolResults++
		}
	}
	seen := 0
	for _, message := range messages {
		result, ok := message.(agent.ToolResultMessage)
		if !ok {
			out = append(out, message)
			report.BytesBefore += len(messageBytes(message))
			report.BytesAfter += len(messageBytes(message))
			continue
		}
		seen++
		index := seen - 1
		before := len(result.Text())
		report.BytesBefore += before
		if keepExact[index] {
			out = append(out, message)
			report.BytesAfter += before
			report.KeptToolResults++
			continue
		}
		item, found := evidenceForCall(evidence, result.ToolCallID)
		if !found || item.Status == StatusConflicting {
			out = append(out, message)
			report.BytesAfter += before
			report.KeptToolResults++
			continue
		}
		policy, text := projectionText(result, item, options)
		if item.Retention == RetentionExact && !keepExact[index] {
			isRecent := totalToolResults-index <= options.KeepRecentToolResults
			if isRecent {
				// A recent result too large for the exact window keeps a
				// bounded excerpt rather than disappearing entirely.
				if policy == "" {
					policy, text = "exact_excerpt", excerpt(result.Text(), options.ExactKeepBytes,
						"exact observation excerpted to the working-set window")
				}
			} else {
				// An old exact observation is either in the verbatim working
				// set or summarized as a whole. This is the budget enforcement
				// point: the exact working set is the only reason a small old
				// result stays verbatim.
				policy, text = "exact_budget_summary", exactSummary(item, result.ToolName)
			}
		}
		if policy == "" {
			out = append(out, message)
			report.BytesAfter += before
			report.KeptToolResults++
			continue
		}
		replaced := agent.ToolResultMessage{
			ToolCallID: result.ToolCallID,
			ToolName:   result.ToolName,
			Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
			IsError:    result.IsError,
		}
		out = append(out, replaced)
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

// exactWorkingSet selects which tool results stay verbatim.
//
// The newest KeepRecentToolResults are always retained as the immediate working
// set. Older exact observations are retained while the total exact budget
// allows; the rest become evidence summaries. Structural, derived, diagnostic,
// and external results follow their own retention policy.
func exactWorkingSet(messages []agent.Message, evidence *EvidenceState, options ProjectorOptions) map[int]bool {
	type result struct {
		index     int
		size      int
		retention Retention
		found     bool
	}
	var results []result
	index := 0
	for _, message := range messages {
		typed, ok := message.(agent.ToolResultMessage)
		if !ok {
			continue
		}
		entry := result{index: index, size: len(typed.Text())}
		if item, found := evidenceForCall(evidence, typed.ToolCallID); found {
			entry.retention = item.Retention
			entry.found = true
		}
		results = append(results, entry)
		index++
	}

	keep := map[int]bool{}
	recent := options.KeepRecentToolResults
	used := 0
	for i := len(results) - 1; i >= 0; i-- {
		entry := results[i]
		if recent > 0 {
			// The newest results are the working set, but one enormous result
			// must still be bounded; the excerpt policy handles that case.
			if entry.size <= options.ExactKeepBytes {
				keep[entry.index] = true
				used += entry.size
			}
			recent--
			continue
		}
		if entry.found && entry.retention == RetentionExact &&
			used+entry.size <= options.ToolResultBudgetBytes {
			keep[entry.index] = true
			used += entry.size
		}
	}
	return keep
}

// exactSummary renders an exact observation that no longer fits the working
// set. The provenance and size stay visible; the payload is recoverable by a
// later read.
func exactSummary(item EvidenceItem, tool string) string {
	summary := strings.TrimSpace(item.Summary)
	if summary == "" {
		summary = "observation"
	}
	return fmt.Sprintf("[%s result summarized from runtime evidence; %d retained bytes]\n%s",
		tool, len(item.Value), bound(summary, 360))
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
	case RetentionStructural, RetentionDerived, RetentionTransient:
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
