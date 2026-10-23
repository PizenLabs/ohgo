package cognition

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
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

	p.Evidence.ObserveTurns(shape, req.Turns)
	execution := SummarizeExecution(req.Turns)
	suff := EvaluateSufficiency(shape, p.Evidence, execution, shape.Budget.Exhausted())
	candidates := Candidates(shape, p.Evidence, execution, req.Tools, p.Facts, suff)

	messages, report := projectMessages(req.Messages, p.Evidence, options)
	state := RenderState(shape, p.Evidence, execution, suff, candidates,
		options.StateBlockBytes, options.StateBlockItems)

	report.Turn = req.Turn
	report.Messages = len(messages)
	report.ElidedToolResults = len(report.Elisions)
	report.StateBlockBytes = len(state)
	report.ToolNames = toolNames(candidates.Tools)
	report.Excluded = candidates.Excluded
	report.Sufficiency = string(suff.State)
	report.Stage = string(suff.Stage)
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
		Report:   report,
	}
	if state != "" {
		result.System = []ai.SystemPromptBlock{{Text: state}}
	}
	return result
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
