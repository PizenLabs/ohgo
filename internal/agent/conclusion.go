package agent

import (
	"errors"
	"strings"
	"time"
)

// ErrInvalidConclusion means a conclusion is missing the status or reason a
// durable record requires.
var ErrInvalidConclusion = errors.New("agent: invalid run conclusion")

// ConclusionStatus is the semantic outcome of a task that is not a proven
// completion.
//
// The lifecycle status answers whether the runtime can continue; the
// conclusion answers what the task established. They are deliberately
// separate: NEEDS_REVIEW is non-terminal and resumable, while an unresolved
// conclusion records that this run could not establish the objective's
// missing evidence. Completion and failure keep owning PASS and FAIL through
// the task lifecycle and its proof, so only the unresolved outcome needs its
// own record here.
type ConclusionStatus string

// ConclusionUnresolved means the objective was not established and no further
// action in this run can establish it. It is not a failure verdict: the task
// and its observed evidence remain resumable.
const ConclusionUnresolved ConclusionStatus = "UNRESOLVED"

// Conclusion reason codes. They are machine-readable vocabulary, not prose: a
// host must not have to parse the human explanation to learn why the runtime
// stopped.
const (
	// ConclusionVerificationNotEstablished means a change waited for its check
	// past the runtime's bounded observation window. Another read or repeat of
	// the same check cannot close the gap.
	ConclusionVerificationNotEstablished = "verification_not_established"
)

// RunConclusion is the terminal semantic outcome of a run that did not prove
// its objective.
//
// It is evidence, never authority, and it never completes or fails a task by
// itself. The Summary is the authoritative explanation a host may publish when
// the model produced no final answer; it states what the runtime observed and
// what remains unresolved without claiming an execution it did not see.
type RunConclusion struct {
	// Status is the semantic outcome.
	Status ConclusionStatus `json:"status"`
	// Reason is the machine-readable cause. It is a stable code, not prose.
	Reason string `json:"reason,omitempty"`
	// Summary explains the outcome in bounded, observed terms.
	Summary string `json:"summary,omitempty"`
	// At is when the durable task recorded the conclusion. A live projection
	// leaves it zero; the task boundary stamps it.
	At time.Time `json:"at,omitempty"`
}

// Validate reports whether the conclusion carries the vocabulary a persisted
// record requires.
func (c RunConclusion) Validate() error {
	if c.Status != ConclusionUnresolved {
		return ErrInvalidConclusion
	}
	if strings.TrimSpace(c.Reason) == "" {
		return ErrInvalidConclusion
	}
	return nil
}

// Label renders the status and reason as one stable line. The summary remains
// separate so a caller can wrap it.
func (c RunConclusion) Label() string {
	label := string(c.Status)
	if reason := strings.TrimSpace(c.Reason); reason != "" {
		label += " · " + reason
	}
	return label
}
