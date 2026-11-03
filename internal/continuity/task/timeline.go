package task

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// Timeline bounds keep task progression an index of meaningful transitions,
// never a second transcript. A tool result is referenced by id; its payload
// stays in the cognition evidence state.
const (
	// MaxTimelineEvents bounds the durable event log.
	MaxTimelineEvents = 256
	// maxTimelineSummaryBytes bounds one event's human-readable line.
	maxTimelineSummaryBytes = 320
	// maxTimelineRefs bounds the records one event points at.
	maxTimelineRefs = 16
)

// TimelineKind classifies one meaningful task event.
type TimelineKind string

// Timeline event kinds.
const (
	// TimelineCreated is the task identity being minted.
	TimelineCreated TimelineKind = "created"
	// TimelineResumed is work continuing from a checkpoint or a detach.
	TimelineResumed TimelineKind = "resumed"
	// TimelineCheckpoint is a recorded progress boundary.
	TimelineCheckpoint TimelineKind = "checkpoint"
	// TimelinePaused is an interruption at a recoverable checkpoint.
	TimelinePaused TimelineKind = "paused"
	// TimelineReview is a turn ending without enough evidence to complete.
	TimelineReview TimelineKind = "needs_review"
	// TimelineCompleted is the terminal completion transition.
	TimelineCompleted TimelineKind = "completed"
	// TimelineFailed is an unrecoverable execution failure.
	TimelineFailed TimelineKind = "failed"
	// TimelineCancelled is a deliberate abandon.
	TimelineCancelled TimelineKind = "cancelled"
	// TimelineDetached is durable work leaving the live session slot.
	TimelineDetached TimelineKind = "detached"
	// TimelineEvidence is one task-linked observation.
	TimelineEvidence TimelineKind = "evidence"
	// TimelineVerification is an observation produced by a check.
	TimelineVerification TimelineKind = "verification"
	// TimelineFinding is a legacy, unclassified note. It is not a fact.
	TimelineFinding TimelineKind = "finding"
	// TimelineFact is a verified statement.
	TimelineFact TimelineKind = "fact"
	// TimelineHypothesis is an unverified proposal.
	TimelineHypothesis TimelineKind = "hypothesis"
	// TimelineQuestion is an outstanding question.
	TimelineQuestion TimelineKind = "question"
	// TimelineHypothesisConfirmed is a hypothesis promoted by evidence.
	TimelineHypothesisConfirmed TimelineKind = "hypothesis_confirmed"
	// TimelineHypothesisRejected is a hypothesis closed without a fact.
	TimelineHypothesisRejected TimelineKind = "hypothesis_rejected"
	// TimelineHypothesisSuperseded is a hypothesis replaced by another.
	TimelineHypothesisSuperseded TimelineKind = "hypothesis_superseded"
	// TimelineFactSuperseded is a fact replaced by another.
	TimelineFactSuperseded TimelineKind = "fact_superseded"
	// TimelineEvidenceInvalidated is an observation that no longer applies.
	TimelineEvidenceInvalidated TimelineKind = "evidence_invalidated"
	// TimelineEvidenceSuperseded is an observation replaced by a later one.
	TimelineEvidenceSuperseded TimelineKind = "evidence_superseded"
	// TimelineHandoff is a cross-role transition disposition.
	TimelineHandoff TimelineKind = "handoff"
)

// Valid reports whether the kind is defined.
func (k TimelineKind) Valid() bool {
	switch k {
	case TimelineCreated, TimelineResumed, TimelineCheckpoint, TimelinePaused,
		TimelineReview, TimelineCompleted, TimelineFailed, TimelineCancelled,
		TimelineDetached, TimelineEvidence, TimelineVerification, TimelineFinding, TimelineFact,
		TimelineHypothesis, TimelineQuestion, TimelineHypothesisConfirmed,
		TimelineHypothesisRejected, TimelineHypothesisSuperseded,
		TimelineFactSuperseded, TimelineEvidenceInvalidated,
		TimelineEvidenceSuperseded, TimelineHandoff:
		return true
	default:
		return false
	}
}

// TimelineEvent is one durable, bounded task event.
//
// An event names what happened and the records that establish it. It never
// carries a tool payload, a conversation message, or a second copy of a fact:
// the referenced owner stays authoritative. The event log is an index for
// explaining progression, not a completion or authority signal.
type TimelineEvent struct {
	// Seq orders events within one task. It is monotonic across restarts and
	// survives a bounded log because gaps stay visible.
	Seq int `json:"seq"`
	// At is when the event was recorded.
	At time.Time `json:"at"`
	// Kind classifies the event.
	Kind TimelineKind `json:"kind"`
	// Role is the execution role that produced it, when one applies.
	Role workspace.Workspace `json:"role,omitempty"`
	// Status is the resulting disposition for lifecycle and handoff events.
	Status string `json:"status,omitempty"`
	// Summary is a bounded human-readable line.
	Summary string `json:"summary"`
	// Refs name the facts, hypotheses, evidence, questions, or handoffs the
	// event points at. They are references, never copied records.
	Refs []string `json:"refs,omitempty"`
}

// Clone returns a deep copy.
func (e TimelineEvent) Clone() TimelineEvent {
	out := e
	out.Refs = append([]string(nil), e.Refs...)
	return out
}

// AppendEvent records one meaningful transition.
//
// It bounds the summary and references, assigns the next sequence number, and
// keeps the creation event plus the newest events when the log is full. Dropped
// events leave a sequence gap that Timeline() reports, so truncation is visible
// rather than silent. An unknown kind is refused rather than persisted.
func (t *Task) AppendEvent(e TimelineEvent) bool {
	if !e.Kind.Valid() {
		return false
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	e.Summary = boundText(strings.TrimSpace(e.Summary), maxTimelineSummaryBytes)
	if e.Summary == "" {
		e.Summary = string(e.Kind)
	}
	e.Refs = boundEventRefs(e.Refs)
	e.Seq = 1
	if len(t.Timeline) > 0 {
		e.Seq = t.Timeline[len(t.Timeline)-1].Seq + 1
	}
	t.Timeline = append(t.Timeline, e)
	if len(t.Timeline) > MaxTimelineEvents {
		t.Timeline, _ = trimTimeline(t.Timeline)
	}
	return true
}

// isObservationEvent reports whether an event indexes one record rather than a
// lifecycle boundary. These are the highest-volume events, so they are the
// first to be elided when the bounded log is full.
func isObservationEvent(kind TimelineKind) bool {
	switch kind {
	case TimelineEvidence, TimelineVerification, TimelineFinding,
		TimelineFact, TimelineHypothesis, TimelineQuestion:
		return true
	default:
		return false
	}
}

// trimTimeline bounds a log while protecting lifecycle history. The first
// retained event is the anchor and is never dropped. Within the rest, the
// oldest observation event is dropped first; lifecycle boundaries are only
// dropped when no observation event remains. This keeps a long investigation
// from pushing pauses, reviews, completions, and handoffs out of the record.
func trimTimeline(events []TimelineEvent) ([]TimelineEvent, int) {
	removed := 0
	for len(events) > MaxTimelineEvents {
		drop := 1
		for i := 1; i < len(events); i++ {
			if isObservationEvent(events[i].Kind) {
				drop = i
				break
			}
		}
		events = append(events[:drop], events[drop+1:]...)
		removed++
	}
	return events, removed
}

// TimelineDropped returns how many events the bounded log no longer carries,
// derived from the sequence gaps in the retained log. It does not assume the
// first retained event is the creation event: a repaired or already-dropped
// prefix is counted from its own sequence number, and every internal gap is
// counted too. The count is visible so a reader never mistakes a bounded
// history for a complete one.
func (t Task) TimelineDropped() int {
	if len(t.Timeline) == 0 {
		return 0
	}
	dropped := 0
	previous := 0
	for _, event := range t.Timeline {
		if event.Seq <= previous {
			continue
		}
		if event.Seq > previous+1 {
			dropped += event.Seq - previous - 1
		}
		previous = event.Seq
	}
	return dropped
}

// LastEvent returns the newest event, if any.
func (t Task) LastEvent() (TimelineEvent, bool) {
	if len(t.Timeline) == 0 {
		return TimelineEvent{}, false
	}
	return t.Timeline[len(t.Timeline)-1].Clone(), true
}

// HasEvent reports whether an event with the kind, first reference, and status
// was already recorded. It keeps a repeated load from duplicating a recovery
// fact that has not been re-persisted yet.
func (t Task) HasEvent(kind TimelineKind, ref, status string) bool {
	for _, event := range t.Timeline {
		if event.Kind != kind || event.Status != status {
			continue
		}
		if ref == "" {
			return true
		}
		for _, candidate := range event.Refs {
			if candidate == ref {
				return true
			}
		}
	}
	return false
}

// AppendEventIfChanged records an event unless the newest event has the same
// kind and summary. It keeps repeated turn boundaries from flooding the log
// while still recording a genuinely different action.
func (t *Task) AppendEventIfChanged(e TimelineEvent) bool {
	if last, ok := t.LastEvent(); ok && last.Kind == e.Kind && last.Summary == e.Summary {
		return false
	}
	return t.AppendEvent(e)
}

// sanitizeTimeline returns the largest valid bounded log and the number of
// repairs. A damaged event is dropped rather than allowed to make the whole
// checkpoint unreadable; valid history is never rewritten.
func sanitizeTimeline(events []TimelineEvent) ([]TimelineEvent, int) {
	if len(events) == 0 {
		return nil, 0
	}
	changes := 0
	if len(events) > MaxTimelineEvents {
		copied := append([]TimelineEvent(nil), events...)
		trimmed, removed := trimTimeline(copied)
		events = trimmed
		changes += removed
	}
	out := make([]TimelineEvent, 0, len(events))
	lastSeq := 0
	for _, event := range events {
		if !event.Kind.Valid() {
			changes++
			continue
		}
		summary := boundText(strings.TrimSpace(event.Summary), maxTimelineSummaryBytes)
		if summary == "" {
			summary = string(event.Kind)
			changes++
		}
		if event.At.IsZero() {
			event.At = time.Now().UTC()
			changes++
		}
		if event.Role != "" && !event.Role.Valid() {
			event.Role = ""
			changes++
		}
		refs := boundEventRefs(event.Refs)
		if len(refs) != len(event.Refs) {
			changes++
		}
		seq := event.Seq
		if seq <= lastSeq {
			seq = lastSeq + 1
			changes++
		}
		out = append(out, TimelineEvent{
			Seq:     seq,
			At:      event.At,
			Kind:    event.Kind,
			Role:    event.Role,
			Status:  boundText(strings.TrimSpace(event.Status), maxTimelineSummaryBytes),
			Summary: summary,
			Refs:    refs,
		})
		lastSeq = seq
	}
	if len(out) == 0 {
		return nil, changes
	}
	return out, changes
}

func boundEventRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = boundText(strings.TrimSpace(ref), maxEvidenceRefBytes)
		if ref == "" {
			continue
		}
		if len(out) >= maxTimelineRefs {
			break
		}
		out = append(out, ref)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ValidateTimeline reports whether the log satisfies its bounds and shape.
func (t Task) ValidateTimeline() error {
	if len(t.Timeline) > MaxTimelineEvents {
		return fmt.Errorf("%w: %d timeline events > %d", ErrInvalidTask, len(t.Timeline), MaxTimelineEvents)
	}
	lastSeq := 0
	for i, event := range t.Timeline {
		if !event.Kind.Valid() {
			return fmt.Errorf("%w: timeline event %d has unknown kind %q", ErrInvalidTask, i, event.Kind)
		}
		if strings.TrimSpace(event.Summary) == "" {
			return fmt.Errorf("%w: timeline event %d has an empty summary", ErrInvalidTask, i)
		}
		if event.Seq <= lastSeq {
			return fmt.Errorf("%w: timeline event %d is not ordered", ErrInvalidTask, i)
		}
		if event.Role != "" && !event.Role.Valid() {
			return fmt.Errorf("%w: timeline event %d has unknown role %q", ErrInvalidTask, i, event.Role)
		}
		if len(event.Refs) > maxTimelineRefs {
			return fmt.Errorf("%w: timeline event %d has too many refs", ErrInvalidTask, i)
		}
		lastSeq = event.Seq
	}
	return nil
}
