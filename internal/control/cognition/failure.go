package cognition

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

// Failure-memory bounds.
const (
	// MaxFailureRecords bounds the durable failure ledger. Records are
	// aggregated by call signature, so a runaway repeat cannot grow the state
	// by one entry per turn.
	MaxFailureRecords = 64
	// RetryableFailureAttempts is how many failed executions a retryable class
	// permits before an identical proposal is refused. One retry is the
	// recovery budget: more than that is the pathological loop this ledger
	// exists to stop.
	MaxRetryableFailureAttempts = 2
	// MaxRequiredExternalFailures bounds distinct failed attempts against a
	// required external capability. Different capability names are legitimate
	// recovery attempts up to this bound; after it, the required capability is
	// treated as unavailable and the task closes instead of spending the turn
	// grant on guesses.
	MaxRequiredExternalFailures = 3
)

// FailureRecord is one durable tool failure.
//
// It is keyed by the exact invocation signature so a model can recover by
// changing the capability or the arguments, while an identical repeat is a
// deterministic runtime decision rather than another execution. A record may
// describe a failed capability or a rejected invocation; RequestLevel decides
// which, and only a capability failure can influence provider health or block
// the task. The record is observation state, never authority.
type FailureRecord struct {
	// Signature is agent.CallSignature(tool, args).
	Signature string `json:"signature"`
	// Target is agent.CallTarget(tool, args): the coordinate a later success
	// can resolve, even when the retry uses different secondary arguments.
	Target string `json:"target,omitempty"`
	// Tool is the capability that failed.
	Tool string `json:"tool,omitempty"`
	// Class is the provider-agnostic failure category.
	Class knowledge.FailureClass `json:"class,omitempty"`
	// Retryable reports that the class permits another attempt.
	Retryable bool `json:"retryable,omitempty"`
	// External reports that the failed capability is an external-knowledge
	// provider. It lets a required external objective distinguish repeated
	// guesses at different capabilities from unrelated local failures.
	External bool `json:"external,omitempty"`
	// Failures counts executions that failed.
	Failures int `json:"failures,omitempty"`
	// Refusals counts identical proposals the runtime refused after the retry
	// budget was spent. A refusal is not an execution.
	Refusals int `json:"refusals,omitempty"`
	// Resolved marks a failure a later success covered.
	Resolved bool `json:"resolved,omitempty"`
	// Summary is a bounded model-facing description.
	Summary string `json:"summary,omitempty"`
	// Seq is the last observation order.
	Seq int `json:"seq,omitempty"`
}

// Attempts is the total number of proposals the ledger observed.
func (r FailureRecord) Attempts() int { return r.Failures + r.Refusals }

// Fatal reports a class no retry can recover without an external change.
func (r FailureRecord) Fatal() bool {
	if r.Resolved {
		return false
	}
	if r.Class.RequestLevel() {
		return false
	}
	switch r.Class {
	case knowledge.FailureAuthentication, knowledge.FailureNotConfigured,
		knowledge.FailureProtocol:
		return true
	}
	return false
}

// RequestLevel reports a rejected invocation rather than a failed capability.
func (r FailureRecord) RequestLevel() bool {
	return r.Class.RequestLevel()
}

// Refuses reports that the exact invocation must not execute again.
func (r FailureRecord) Refuses() bool {
	if r.Resolved || r.Failures == 0 {
		return false
	}
	if r.RequestLevel() {
		// One rejected request is enough correction information; repeating the
		// identical bytes cannot produce a different result. A corrected
		// invocation has a different signature and remains eligible.
		return true
	}
	if r.Fatal() {
		return true
	}
	if r.Retryable {
		return r.Failures >= MaxRetryableFailureAttempts
	}
	return true
}

// Blocks reports that the task cannot make progress on this capability.
// A non-retryable failure blocks only after the model repeats the identical
// proposal and the runtime refuses it; that gives the model one chance to
// correct the capability or its arguments while still closing the loop.
//
// A request-level failure never blocks here. It says one invocation was
// malformed, not that the capability, provider, or task is unavailable. Task
// viability is decided by the acquisition reconciliation, which can still see
// every independent provider.
func (r FailureRecord) Blocks() bool {
	if r.Resolved || r.RequestLevel() {
		return false
	}
	if r.Fatal() {
		return true
	}
	if r.Retryable {
		return r.Refusals > 0 || r.Failures > MaxRetryableFailureAttempts
	}
	return r.Refusals > 0
}

// FailureState is the ledger's current verdict for one projection.
type FailureState struct {
	// Records are the unresolved failures in observation order.
	Records []FailureRecord
	// Blocking is the first failure that closed the task, when one exists.
	Blocking *FailureRecord
	// Refusals maps an exact call signature to the runtime reason it must not
	// execute. It is applied by the agent loop before the capability boundary.
	Refusals map[string]string
}

// FailureState derives the current verdict from the durable ledger.
func (s *EvidenceState) FailureState() FailureState {
	state := FailureState{Refusals: map[string]string{}}
	if s == nil {
		return state
	}
	for i := range s.Failures {
		record := s.Failures[i]
		if record.Resolved {
			continue
		}
		state.Records = append(state.Records, record)
		if record.Refuses() {
			state.Refusals[record.Signature] = failureRefusalText(record)
		}
		if record.Blocks() && state.Blocking == nil {
			blocked := record
			state.Blocking = &blocked
		}
	}
	return state
}

// ExternalFailures counts unresolved external-capability failures.
func (s FailureState) ExternalFailures() int {
	count := 0
	for _, record := range s.Records {
		if record.External {
			count++
		}
	}
	return count
}

// failureRefusalText explains a refusal in class terms, never in vendor terms.
func failureRefusalText(record FailureRecord) string {
	class := string(record.Class)
	if class == "" {
		class = "unclassified"
	}
	if record.RequestLevel() {
		detail := strings.TrimSpace(record.Summary)
		if detail == "" {
			detail = "the invocation did not satisfy the tool contract"
		}
		return fmt.Sprintf(
			"The identical %s invocation was already rejected as %s: %s Nothing executed. The capability remains available; submit corrected arguments or use another authorized observation.",
			record.Tool, class, ensureSentence(detail))
	}
	if record.Fatal() {
		return fmt.Sprintf(
			"Capability %q already failed with a non-recoverable %s failure. No retry of this call is permitted; report the failure and answer from the evidence already established.",
			record.Tool, class)
	}
	return fmt.Sprintf(
		"Capability %q already failed with a %s failure. The identical call is not executed again; use a different capability or corrected arguments, or report the failure.",
		record.Tool, class)
}

// ensureSentence keeps a bounded correction readable inside a refusal without
// letting an unbounded model-facing error grow the state block.
func ensureSentence(text string) string {
	text = firstLine(text)
	if text == "" {
		return ""
	}
	if strings.ContainsAny(text[len(text)-1:], ".!?") {
		return bound(text, 480)
	}
	return bound(text, 479) + "."
}

// recordFailure folds one failed outcome into the ledger.
//
// A control decision never calls this: a refusal, a denial, an observed
// absence, and a resource stop are not failed requests or capabilities. A
// schema or selector rejection does call it, because the ledger must refuse an
// identical repeat; its request-level class keeps that record out of capability
// health and task blocking.
func (s *EvidenceState) recordFailure(outcome agent.ToolOutcome, seq int) {
	if s == nil {
		return
	}
	signature := agent.CallSignature(outcome.ToolName, outcome.Args)
	target := agent.CallTarget(outcome.ToolName, outcome.Args)
	class, retryable, external, summary := failureDetails(outcome)
	for i := range s.Failures {
		record := &s.Failures[i]
		if record.Signature != signature {
			continue
		}
		record.Target = target
		record.Tool = outcome.ToolName
		record.Class = class
		record.Retryable = retryable
		record.External = external
		record.Failures++
		record.Resolved = false
		record.Summary = summary
		record.Seq = seq
		return
	}
	s.Failures = append(s.Failures, FailureRecord{
		Signature: signature,
		Target:    target,
		Tool:      outcome.ToolName,
		Class:     class,
		Retryable: retryable,
		External:  external,
		Failures:  1,
		Summary:   summary,
		Seq:       seq,
	})
	s.trimFailures()
}

// recordFailureRefusal counts an identical proposal the runtime refused after
// the retry budget was spent. It makes a non-retryable repeat blocking without
// executing it again.
func (s *EvidenceState) recordFailureRefusal(outcome agent.ToolOutcome, seq int) {
	if s == nil {
		return
	}
	signature := agent.CallSignature(outcome.ToolName, outcome.Args)
	for i := range s.Failures {
		record := &s.Failures[i]
		if record.Signature != signature {
			continue
		}
		record.Refusals++
		record.Resolved = false
		record.Seq = seq
		return
	}
	// The durable record may have been evicted while the loop still remembers
	// the refusal. Do not invent a capability failure from a control decision:
	// the class that justified the refusal is gone with the record, and a
	// synthetic blocking record would close a task whose provider may still be
	// healthy. The loop's refusal memory still prevents the identical call from
	// executing.
}

// resolveNonFatalFailures marks every non-fatal failure recovered after a
// definite workspace change. The failure was an observation of the pre-change
// state; retrying the same command after the change is a new proposal, not a
// repeat of an identical observation.
func (s *EvidenceState) resolveNonFatalFailures() {
	if s == nil {
		return
	}
	for i := range s.Failures {
		if !s.Failures[i].Fatal() {
			s.Failures[i].Resolved = true
		}
	}
}

// resolveFailure marks a failure recovered by a later successful observation of
// the same coordinate. An exact success resolves its exact record; a success
// against the same target resolves any failure for that target, so a corrected
// retry is not punished.
func (s *EvidenceState) resolveFailure(outcome agent.ToolOutcome) {
	if s == nil || len(s.Failures) == 0 {
		return
	}
	signature := agent.CallSignature(outcome.ToolName, outcome.Args)
	target := agent.CallTarget(outcome.ToolName, outcome.Args)
	for i := range s.Failures {
		record := &s.Failures[i]
		if record.Resolved {
			continue
		}
		if record.Signature == signature || (target != "" && record.Target == target) {
			record.Resolved = true
		}
	}
}

// failureDetails reads the structured classification a capability attached to
// its failure. It never classifies from model prose: an unclassified failure is
// recorded conservatively as non-retryable tool_error.
func failureDetails(outcome agent.ToolOutcome) (knowledge.FailureClass, bool, bool, string) {
	if outcome.Result.Exhausted != nil {
		return knowledge.FailureBudgetExhausted, false, false, firstLine(outcome.Result.Text)
	}
	details := detailsMap(outcome.Result.Details)
	class := knowledge.FailureClass(strings.TrimSpace(outcome.Result.FailureClass))
	retryable := outcome.Result.FailureRetryable
	if class == "" {
		switch value := details["failure"].(type) {
		case knowledge.Failure:
			class, retryable = value.Class, value.Retryable
		case *knowledge.Failure:
			if value != nil {
				class, retryable = value.Class, value.Retryable
			}
		}
	}
	if class == "" {
		if raw, ok := details["failure_classification"].(string); ok && strings.TrimSpace(raw) != "" {
			class = knowledge.FailureClass(strings.TrimSpace(raw))
			retryable, _ = details["retryable"].(bool)
		}
	}
	class = normalizeFailureClass(class)
	if class == "" {
		class = knowledge.FailureToolError
	}
	// A rejected invocation is not an external-provider outage. Keeping the
	// external bit clear is what stops request-level failures from consuming
	// the required external capability's bounded failure budget.
	external := !class.RequestLevel() && (detailString(details, "capability") != "" ||
		detailString(details, "trust") != "" ||
		detailString(details, "provider") != "")
	return class, retryable, external, firstLine(outcome.Result.Text)
}

// normalizeFailureClass maps the loop's request-stage class vocabulary onto the
// shared failure taxonomy. They are separate constants because the agent kernel
// cannot import the external knowledge package, but the control plane must see
// one vocabulary.
func normalizeFailureClass(class knowledge.FailureClass) knowledge.FailureClass {
	switch string(class) {
	case agent.ToolFailureInvalidArgument:
		return knowledge.FailureInvalidArgument
	case agent.ToolFailureInvalidSelector:
		return knowledge.FailureInvalidSelector
	case agent.ToolFailureUnknownTool:
		return knowledge.FailureUnknownTool
	case agent.ToolFailureUnsupported:
		return knowledge.FailureUnsupported
	case agent.ToolFailureAuthorization:
		return knowledge.FailureAuthorityDenied
	case agent.ToolFailureExecutionFailed, agent.ToolFailureDispatch,
		agent.ToolFailureStart, agent.ToolFailureRuntime:
		return knowledge.FailureExecutionFailed
	case agent.ToolFailureTimeout:
		return knowledge.FailureTimeout
	case agent.ToolFailureCancelled:
		return knowledge.FailureCancelled
	default:
		return class
	}
}

// trimFailures bounds the ledger. Resolved and oldest records go first so the
// current blocking failure survives.
func (s *EvidenceState) trimFailures() {
	if len(s.Failures) <= MaxFailureRecords {
		return
	}
	sort.SliceStable(s.Failures, func(i, j int) bool {
		if s.Failures[i].Resolved != s.Failures[j].Resolved {
			return s.Failures[i].Resolved
		}
		if s.Failures[i].Blocks() != s.Failures[j].Blocks() {
			return !s.Failures[i].Blocks()
		}
		return s.Failures[i].Seq < s.Failures[j].Seq
	})
	s.Failures = s.Failures[len(s.Failures)-MaxFailureRecords:]
}
