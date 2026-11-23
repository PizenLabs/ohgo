package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// ResponseState is the publication state of one user task's answer.
//
// Many model turns and tool calls happen inside one task, but only one answer
// may be committed. The state is audit data: it does not widen authority or
// replace the host's transcript.
type ResponseState string

// Response transaction states.
const (
	// ResponseInvestigating means the run is gathering evidence and has no
	// committed answer.
	ResponseInvestigating ResponseState = "INVESTIGATING"
	// ResponseFinalizing means the accepted draft is passing the claim audit.
	ResponseFinalizing ResponseState = "FINALIZING"
	// ResponseReview means a candidate was produced but the publication audit
	// refused it. The raw turn remains available to the completion evaluator,
	// but no host may publish it as the final answer.
	ResponseReview ResponseState = "REVIEW"
	// ResponseCommitted means exactly one answer was accepted for publication.
	ResponseCommitted ResponseState = "COMMITTED"
	// ResponseAborted means the run ended without an accepted answer.
	ResponseAborted ResponseState = "ABORTED"
	// ResponseCancelled means the human interrupted the run before an answer
	// was committed.
	ResponseCancelled ResponseState = "CANCELLED"
	// ResponseFailed means an execution or provider failure ended the run
	// before an answer was committed.
	ResponseFailed ResponseState = "FAILED"
)

// ResponseAudit records the answer transaction for one run.
//
// Drafts counts answer candidates the model produced, including candidates
// suppressed for a correction. Rejected counts candidates the answer review
// refused. Answer is the final audit applied to the committed text.
type ResponseAudit struct {
	State    ResponseState `json:"state"`
	Drafts   int           `json:"drafts,omitempty"`
	Rejected int           `json:"rejected,omitempty"`
	// ToolCalls counts the outcomes the run observed.
	ToolCalls int `json:"tool_calls,omitempty"`
	// ReusedObservations counts calls answered from an observation already in
	// hand. They are not new evidence and do not execute the tool.
	ReusedObservations int `json:"reused_observations,omitempty"`
	// RetriedToolCalls counts an identical call repeated after a failure.
	// Retries may be legitimate recovery, so they are not redundant.
	RetriedToolCalls int `json:"retried_tool_calls,omitempty"`
	// RedundantToolCalls counts an identical call repeated after a successful
	// observation. It could not add evidence.
	RedundantToolCalls int `json:"redundant_tool_calls,omitempty"`
	// Lineage is the internal draft history for this run. It is audit data:
	// a superseded draft is never replayed as a user-visible answer.
	Lineage []DraftAudit `json:"lineage,omitempty"`
	// PreservedClaims counts the valid claims the committed answer retained
	// from the strongest draft.
	PreservedClaims int `json:"preserved_claims,omitempty"`
	// RemovedClaims counts unsupported claims the fallback repair removed.
	RemovedClaims int         `json:"removed_claims,omitempty"`
	Answer        AnswerAudit `json:"answer,omitempty"`
}

// LastResponseAudit returns the publication audit for the most recent run.
func (l *Loop) LastResponseAudit() ResponseAudit {
	if l == nil {
		return ResponseAudit{}
	}
	l.responseMu.Lock()
	defer l.responseMu.Unlock()
	return l.response
}

// beginResponse starts a fresh transaction at Run. A loop is reused across
// user turns, so a committed answer from the previous turn must never make the
// next turn look already published.
func (l *Loop) beginResponse() {
	l.responseMu.Lock()
	l.response = ResponseAudit{State: ResponseInvestigating}
	l.responseMu.Unlock()
}

// noteDraft records one answer candidate without committing it.
func (l *Loop) noteDraft() {
	l.responseMu.Lock()
	l.response.Drafts++
	l.responseMu.Unlock()
}

// noteRejected records a candidate the answer review suppressed. It does not
// change the transaction state: a candidate rejected during correction leaves
// the run investigating, while one rejected by the publication gate keeps the
// REVIEW state that gate recorded.
func (l *Loop) noteRejected() {
	l.responseMu.Lock()
	l.response.Rejected++
	l.responseMu.Unlock()
}

// finalizeResponse applies the host's finalizer and commits the returned text
// once. A second call is a no-op: the first committed answer is the only one
// the run may publish.
func (l *Loop) finalizeResponse(turns []Turn, live *[]Message, evidenceGap string) bool {
	state := l.responseState()
	if len(turns) == 0 || state == ResponseCommitted || state == ResponseReview {
		return false
	}
	l.responseMu.Lock()
	l.response.State = ResponseFinalizing
	l.responseMu.Unlock()

	answer := turns[len(turns)-1].Assistant.Text()
	finalized := FinalizedAnswer{Text: answer, Audit: AnswerAudit{Finalized: true}}
	if l.cfg.FinalizeAnswer != nil {
		finalized = l.cfg.FinalizeAnswer(turns, answer, evidenceGap)
		// An empty result is deliberate: the gate removed text that must not
		// be published (for example a copied runtime-state block). Restoring
		// the raw draft here would leak exactly what the gate withheld.
	}
	if !finalized.Audit.Approved() {
		// The gate's own verdict is authoritative. A host must never publish a
		// candidate the same audit refuses to certify: the completion boundary
		// would immediately contradict the answer the user just saw.
		l.responseMu.Lock()
		l.response.State = ResponseReview
		l.response.Answer = finalized.Audit
		l.responseMu.Unlock()
		return false
	}
	if finalized.Text != answer {
		turns[len(turns)-1].Assistant = replaceAssistantText(turns[len(turns)-1].Assistant, finalized.Text)
		replaceLiveAssistant(live, turns[len(turns)-1].Assistant)
	}

	metrics := responseToolMetrics(turns)
	l.responseMu.Lock()
	l.response.State = ResponseCommitted
	l.response.ToolCalls = metrics.calls
	l.response.ReusedObservations = metrics.reused
	l.response.RetriedToolCalls = metrics.retried
	l.response.RedundantToolCalls = metrics.redundant
	l.response.Answer = finalized.Audit
	l.responseMu.Unlock()
	return true
}

// responseToolMetrics counts executed outcomes and the observable redundancy
// classes. It is audit data for the response report; it never changes
// completion.
func responseToolMetrics(turns []Turn) (metrics responseMetrics) {
	seen := map[string]bool{}
	failed := map[string]bool{}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			metrics.calls++
			if outcome.Result.Reused {
				metrics.reused++
			}
			key := toolOutcomeSignature(outcome)
			switch {
			case seen[key]:
				metrics.redundant++
			case failed[key]:
				metrics.retried++
			}
			if outcome.Result.IsError {
				failed[key] = true
				continue
			}
			seen[key] = true
		}
	}
	return metrics
}

type responseMetrics struct {
	calls, reused, retried, redundant int
}

// toolOutcomeSignature identifies an identical invocation. Argument order is
// normalized by JSON encoding, so a map iteration cannot make the same call
// look new.
func toolOutcomeSignature(outcome ToolOutcome) string {
	raw, err := json.Marshal(outcome.Args)
	if err != nil {
		return outcome.ToolName
	}
	return outcome.ToolName + "\x00" + string(raw)
}

// markResponseReview records a candidate the pre-publication review rejected.
// It is not a failure of the run: the completion evaluator still reads the
// candidate and produces the review verdict and its claim list.
func (l *Loop) markResponseReview(issues []contract.GroundingIssue) {
	l.responseMu.Lock()
	defer l.responseMu.Unlock()
	audit := AnswerAudit{Finalized: true, Issues: append([]contract.GroundingIssue(nil), issues...)}
	for _, issue := range issues {
		if isFacetIssue(issue) {
			audit.UnresolvedFacets++
			continue
		}
		audit.UnsupportedClaims++
	}
	l.response.State = ResponseReview
	l.response.Answer = audit
}

// recordDraftLineage copies the per-run draft history into the response audit.
// It is audit data: a host never uses it to decide what to display.
func (l *Loop) recordDraftLineage(summaries []DraftAudit, preserved, removed int) {
	l.responseMu.Lock()
	l.response.Lineage = append([]DraftAudit(nil), summaries...)
	l.response.PreservedClaims = preserved
	l.response.RemovedClaims = removed
	l.responseMu.Unlock()
}

// abortResponse records a run that ended without a committed answer.
func (l *Loop) abortResponse(runErr error) {
	l.responseMu.Lock()
	defer l.responseMu.Unlock()
	if l.response.State == ResponseCommitted || l.response.State == ResponseReview {
		return
	}
	switch {
	case errors.Is(runErr, context.Canceled):
		l.response.State = ResponseCancelled
	case runErr != nil:
		l.response.State = ResponseFailed
	default:
		l.response.State = ResponseAborted
	}
}

// responseState reads the transaction state under the mutex.
func (l *Loop) responseState() ResponseState {
	l.responseMu.Lock()
	defer l.responseMu.Unlock()
	return l.response.State
}

// replaceAssistantText swaps the visible text of an assistant message while
// preserving its reasoning and tool-call blocks in their original order.
func replaceAssistantText(message ai.AssistantMessage, text string) ai.AssistantMessage {
	content := make([]ai.ContentBlock, 0, len(message.Content)+1)
	placed := false
	for _, block := range message.Content {
		if _, ok := block.(ai.TextContent); ok {
			if !placed {
				content = append(content, ai.TextContent{Text: text})
				placed = true
			}
			continue
		}
		content = append(content, block)
	}
	if !placed {
		content = append(content, ai.TextContent{Text: text})
	}
	message.Content = content
	return message
}
