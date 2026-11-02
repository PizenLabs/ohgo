package task

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// HandoffStatus is where one role transition is in its lifecycle.
//
// A handoff is deliberately a record, not a state machine that owns execution.
// It exists so a transition that crosses an approval boundary can be
// diagnosed, retried, or safely abandoned after a restart. The task's own
// lifecycle and the live workspace authority remain authoritative.
type HandoffStatus string

// Handoff lifecycle states.
const (
	// HandoffProposed is awaiting the human's decision. Authority has not moved.
	HandoffProposed HandoffStatus = "PROPOSED"
	// HandoffApproved has the human's approval but authority has not moved yet.
	HandoffApproved HandoffStatus = "APPROVED"
	// HandoffApplied means the authority moved and the new role was recorded.
	HandoffApplied HandoffStatus = "APPLIED"
	// HandoffRejected means the human declined. Authority did not move.
	HandoffRejected HandoffStatus = "REJECTED"
	// HandoffFailed means the host could not complete the transition. The last
	// valid role remains the recorded active role.
	HandoffFailed HandoffStatus = "FAILED"
	// HandoffInterrupted means the process stopped between proposal and
	// application. Authority did not move.
	HandoffInterrupted HandoffStatus = "INTERRUPTED"
)

// Valid reports whether the status is one this package defines.
func (s HandoffStatus) Valid() bool {
	switch s {
	case HandoffProposed, HandoffApproved, HandoffApplied,
		HandoffRejected, HandoffFailed, HandoffInterrupted:
		return true
	default:
		return false
	}
}

// Pending reports whether the transition has not been decided or applied.
func (s HandoffStatus) Pending() bool {
	return s == HandoffProposed || s == HandoffApproved
}

// Terminal reports whether no further step is expected for this record.
func (s HandoffStatus) Terminal() bool {
	switch s {
	case HandoffApplied, HandoffRejected, HandoffFailed, HandoffInterrupted:
		return true
	default:
		return false
	}
}

// Bounds for the handoff record. The record carries references, never copied
// payloads, and one transition is never allowed to become a second task brief.
const (
	// maxHandoffRefs bounds each reference list.
	maxHandoffRefs = MaxRoleNotes
	// maxHandoffText bounds a free-text field.
	maxHandoffText = 1024
)

// Handoff is one durable role transition.
//
// It carries the identity and references a target role needs, and nothing it
// should not have: shared records by ID, source progress by reference, and a
// measured workspace summary. It never carries a capability, and its existence
// cannot permit an operation. Workspace policy and the security gate remain the
// only authority paths.
type Handoff struct {
	// ID identifies this transition, not the task.
	ID string `json:"id"`
	// TaskID is the task this transition belongs to.
	TaskID string `json:"taskId"`
	// From is the role the task was running under.
	From workspace.Workspace `json:"from"`
	// To is the requested role.
	To workspace.Workspace `json:"to"`
	// Objective is the task objective the transition preserves.
	Objective string `json:"objective"`
	// Status is the transition lifecycle position.
	Status HandoffStatus `json:"status"`
	// Reason explains why the handoff is needed.
	Reason string `json:"reason,omitempty"`
	// Summary is the concise transition description shown to the target role.
	Summary string `json:"summary,omitempty"`
	// Constraints are the task limits the target must continue to respect.
	Constraints []string `json:"constraints,omitempty"`
	// FactIDs, HypothesisIDs, QuestionIDs, and EvidenceIDs reference shared
	// records by identity. They are never copies.
	FactIDs       []string `json:"factIds,omitempty"`
	HypothesisIDs []string `json:"hypothesisIds,omitempty"`
	QuestionIDs   []string `json:"questionIds,omitempty"`
	EvidenceIDs   []string `json:"evidenceIds,omitempty"`
	// Artifacts are the source role's artifact references, e.g. a patch or a
	// reproduction. They are references, not payloads.
	Artifacts []string `json:"artifacts,omitempty"`
	// Workspace is the measured workspace state at proposal time, e.g. "main;
	// 3 modified, 1 untracked". Empty means the host could not measure it; the
	// runtime must not invent a clean tree.
	Workspace string `json:"workspace,omitempty"`
	// ApprovedBy records who authorized the transition, when it was approved.
	ApprovedBy string `json:"approvedBy,omitempty"`
	// Failure explains a failed or interrupted transition.
	Failure string `json:"failure,omitempty"`
	// CreatedAt is when the handoff was proposed.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the record last changed.
	UpdatedAt time.Time `json:"updatedAt"`
	// AppliedAt is when the target role actually became active.
	AppliedAt time.Time `json:"appliedAt,omitempty"`
}

// ErrNoActiveTask means an operation needs a task and none is active.
var ErrNoActiveTask = errors.New("task: no active task")

// ErrHandoffPending means another transition is already awaiting a decision.
var ErrHandoffPending = errors.New("task: a handoff is already pending")

// ErrInvalidHandoff means a handoff record is incomplete or inconsistent.
var ErrInvalidHandoff = errors.New("task: invalid handoff")

// ErrUnknownHandoff means no handoff with that id exists.
var ErrUnknownHandoff = errors.New("task: unknown handoff")

// Clone returns a deep copy.
func (h Handoff) Clone() Handoff {
	out := h
	out.Constraints = append([]string(nil), h.Constraints...)
	out.FactIDs = append([]string(nil), h.FactIDs...)
	out.HypothesisIDs = append([]string(nil), h.HypothesisIDs...)
	out.QuestionIDs = append([]string(nil), h.QuestionIDs...)
	out.EvidenceIDs = append([]string(nil), h.EvidenceIDs...)
	out.Artifacts = append([]string(nil), h.Artifacts...)
	return out
}

// Validate reports whether the record is complete enough to persist.
//
// Reference lists are deliberately not checked against the context here: a
// repaired context may prune a reference, and that must not make the task
// unwritable. Sanitization prunes them instead.
func (h Handoff) Validate(t Task) error {
	if strings.TrimSpace(h.ID) == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidHandoff)
	}
	if h.TaskID != t.ID {
		return fmt.Errorf("%w: task %q, want %q", ErrInvalidHandoff, h.TaskID, t.ID)
	}
	if !h.From.Valid() || !h.To.Valid() {
		return fmt.Errorf("%w: unknown roles %q -> %q", ErrInvalidHandoff, h.From, h.To)
	}
	if h.From == h.To {
		return fmt.Errorf("%w: roles are equal (%q)", ErrInvalidHandoff, h.From)
	}
	if !h.Status.Valid() {
		return fmt.Errorf("%w: unknown status %q", ErrInvalidHandoff, h.Status)
	}
	if strings.TrimSpace(h.Objective) == "" {
		return fmt.Errorf("%w: empty objective", ErrInvalidHandoff)
	}
	if h.Objective != t.Objective {
		return fmt.Errorf("%w: objective %q does not match the task", ErrInvalidHandoff, h.Objective)
	}
	return nil
}

// Carried is the bounded reference count the UI can show without copying any
// record text. It is descriptive, never a permission.
func (h Handoff) Carried() HandoffCarried {
	return HandoffCarried{
		Facts:        len(h.FactIDs),
		Hypotheses:   len(h.HypothesisIDs),
		Questions:    len(h.QuestionIDs),
		Evidence:     len(h.EvidenceIDs),
		Artifacts:    len(h.Artifacts),
		Constraints:  len(h.Constraints),
	}
}

// HandoffCarried is a count-only view of what a handoff references.
type HandoffCarried struct {
	Facts       int `json:"facts"`
	Hypotheses  int `json:"hypotheses"`
	Questions   int `json:"questions"`
	Evidence    int `json:"evidence"`
	Artifacts   int `json:"artifacts"`
	Constraints int `json:"constraints"`
}

// HandoffInput is the host's request to begin a transition.
type HandoffInput struct {
	// To is the requested role.
	To workspace.Workspace
	// Reason explains why authority must move.
	Reason string
	// Summary is the concise transition description for the target role.
	Summary string
	// Workspace is the measured workspace state, when the host has one.
	Workspace string
}

// ProposeHandoff records a pending transition without moving authority.
//
// It refuses a terminal task, an invalid or unchanged target, and a second
// pending handoff: two live proposals would make recovery ambiguous. The record
// is durable immediately, so a crash between approval and application is
// diagnosable rather than silent.
func (c *Controller) ProposeHandoff(in HandoffInput) (Handoff, error) {
	if err := c.ensureLoaded(); err != nil {
		return Handoff{}, err
	}
	if c.task.ID == "" {
		return Handoff{}, ErrNoActiveTask
	}
	if c.task.Status.IsTerminal() {
		return Handoff{}, fmt.Errorf("%w: task %q is terminal", ErrInvalidHandoff, c.task.ID)
	}
	if !in.To.Valid() {
		return Handoff{}, fmt.Errorf("%w: unknown target %q", ErrInvalidHandoff, in.To)
	}
	from := c.task.LastRole()
	if !from.Valid() {
		if c.role.Valid() {
			from = c.role
		} else {
			from = c.currentRole()
		}
	}
	if !from.Valid() {
		return Handoff{}, fmt.Errorf("%w: the task has no recorded source role", ErrInvalidHandoff)
	}
	if from == in.To {
		return Handoff{}, fmt.Errorf("%w: %q is already the active role", ErrInvalidHandoff, in.To)
	}
	if existing, ok := c.PendingHandoff(); ok {
		return existing, fmt.Errorf("%w: %q -> %q is %s", ErrHandoffPending, existing.From, existing.To, existing.Status)
	}
	now := c.now().UTC()
	handoff, err := c.snapshotHandoff(from, in, now)
	if err != nil {
		return Handoff{}, err
	}
	c.task.Handoff = &handoff
	c.task.UpdatedAt = now
	c.lastAction = "handoff proposed: " + from.Label() + " -> " + in.To.Label()
	if err := c.persist(); err != nil {
		// A failed write must not leave a proposal in memory that the durable
		// state does not contain.
		c.task.Handoff = nil
		return Handoff{}, err
	}
	return handoff.Clone(), nil
}

// PreviewHandoff builds the same record without persisting it, so a UI can show
// exactly what will be carried before the human approves.
func (c *Controller) PreviewHandoff(in HandoffInput) (Handoff, error) {
	if c.task.ID == "" {
		return Handoff{}, ErrNoActiveTask
	}
	from := c.task.LastRole()
	if !from.Valid() {
		from = c.currentRole()
	}
	if !from.Valid() {
		return Handoff{}, fmt.Errorf("%w: the task has no recorded source role", ErrInvalidHandoff)
	}
	if !in.To.Valid() || in.To == from {
		return Handoff{}, fmt.Errorf("%w: no transition from %q to %q", ErrInvalidHandoff, from, in.To)
	}
	return c.snapshotHandoff(from, in, c.now().UTC())
}

// snapshotHandoff copies the bounded shared references and role artifacts.
func (c *Controller) snapshotHandoff(from workspace.Workspace, in HandoffInput, now time.Time) (Handoff, error) {
	id, err := newID()
	if err != nil {
		return Handoff{}, err
	}
	handoff := Handoff{
		ID:        id,
		TaskID:    c.task.ID,
		From:      from,
		To:        in.To,
		Objective: c.task.Objective,
		Status:    HandoffProposed,
		Reason:    boundText(strings.TrimSpace(in.Reason), maxHandoffText),
		Summary:   boundText(strings.TrimSpace(in.Summary), maxHandoffText),
		Workspace: boundText(strings.TrimSpace(in.Workspace), maxHandoffText),
		CreatedAt: now,
		UpdatedAt: now,
	}
	handoff.Constraints = boundRefs(c.task.Constraints)
	for _, fact := range c.task.Context.Facts {
		if fact.Status == FactActive {
			handoff.FactIDs = appendBound(handoff.FactIDs, fact.ID)
		}
	}
	for _, hypothesis := range c.task.Context.Hypotheses {
		if hypothesis.Status == HypothesisActive || hypothesis.Status == HypothesisConfirmed {
			handoff.HypothesisIDs = appendBound(handoff.HypothesisIDs, hypothesis.ID)
		}
	}
	for _, question := range c.task.Context.Questions {
		if question.Status == QuestionOpen {
			handoff.QuestionIDs = appendBound(handoff.QuestionIDs, question.ID)
		}
	}
	for _, record := range c.task.Context.Evidence {
		if record.Validity == EvidenceCurrent {
			handoff.EvidenceIDs = appendBound(handoff.EvidenceIDs, record.ID)
		}
	}
	// Artifacts come from every role's recorded references. They are already
	// references, so the target can locate a patch or reproduction without
	// inheriting the source role's private plan or strategy.
	for _, roleCtx := range c.task.Context.Roles {
		for _, artifact := range roleCtx.Artifacts {
			handoff.Artifacts = appendBound(handoff.Artifacts, artifact)
		}
	}
	if err := handoff.Validate(c.task); err != nil {
		return Handoff{}, err
	}
	return handoff, nil
}

// ApproveHandoff records the human's authorization. Authority still does not
// move: the host applies the transition next.
func (c *Controller) ApproveHandoff(id string) (Handoff, error) {
	handoff, err := c.transitionHandoff(id, HandoffApproved)
	if err != nil {
		return Handoff{}, err
	}
	handoff.ApprovedBy = "human"
	c.task.Handoff = &handoff
	if err := c.persist(); err != nil {
		return Handoff{}, err
	}
	return handoff.Clone(), nil
}

// ApplyHandoff records that authority actually moved to the target role.
//
// It is the only path that changes the task's active role through a handoff.
// The caller must have already applied the authoritative workspace switch: this
// method records the completed transition, it never performs the switch.
func (c *Controller) ApplyHandoff(id string) (Task, error) {
	if c.task.Handoff == nil || c.task.Handoff.ID != id {
		return Task{}, fmt.Errorf("%w: %q", ErrUnknownHandoff, id)
	}
	if c.task.Handoff.Status != HandoffApproved {
		return Task{}, fmt.Errorf("%w: %q is %s, want APPROVED", ErrInvalidHandoff, id, c.task.Handoff.Status)
	}
	now := c.now().UTC()
	handoff := *c.task.Handoff
	handoff.Status = HandoffApplied
	handoff.UpdatedAt = now
	handoff.AppliedAt = now
	handoff.Failure = ""
	c.task.Handoff = &handoff
	c.task.ActiveRole = handoff.To
	c.role = handoff.To
	c.roleKnown = true
	c.task.UpdatedAt = now
	note := "resumed via handoff from " + handoff.From.Label()
	if handoff.Summary != "" {
		note += ": " + handoff.Summary
	}
	if _, err := c.task.UpdateRoleContext(handoff.To, RoleContextUpdate{Progress: []string{note}}, now); err != nil {
		return Task{}, err
	}
	c.lastAction = "handoff applied: " + handoff.From.Label() + " -> " + handoff.To.Label()
	return c.task.Clone(), c.persist()
}

// RejectHandoff records a human decline. Authority did not move.
func (c *Controller) RejectHandoff(id, reason string) (Handoff, error) {
	handoff, err := c.transitionHandoff(id, HandoffRejected)
	if err != nil {
		return Handoff{}, err
	}
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		handoff.Failure = boundText(trimmed, maxHandoffText)
	}
	c.task.Handoff = &handoff
	if err := c.persist(); err != nil {
		return Handoff{}, err
	}
	return handoff.Clone(), nil
}

// FailHandoff records that the host could not complete the transition. The
// recorded active role is left untouched, so the last valid role remains
// authoritative.
func (c *Controller) FailHandoff(id, reason string) (Handoff, error) {
	handoff, err := c.transitionHandoff(id, HandoffFailed)
	if err != nil {
		return Handoff{}, err
	}
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		handoff.Failure = boundText(trimmed, maxHandoffText)
	}
	c.task.Handoff = &handoff
	if err := c.persist(); err != nil {
		return Handoff{}, err
	}
	return handoff.Clone(), nil
}

// transitionHandoff applies a terminal disposition to a pending record.
func (c *Controller) transitionHandoff(id string, to HandoffStatus) (Handoff, error) {
	if c.task.Handoff == nil || c.task.Handoff.ID != id {
		return Handoff{}, fmt.Errorf("%w: %q", ErrUnknownHandoff, id)
	}
	current := *c.task.Handoff
	if to == HandoffApproved {
		if current.Status != HandoffProposed {
			return Handoff{}, fmt.Errorf("%w: %q is %s, want PROPOSED", ErrInvalidHandoff, id, current.Status)
		}
	} else if !current.Status.Pending() {
		return Handoff{}, fmt.Errorf("%w: %q is already %s", ErrInvalidHandoff, id, current.Status)
	}
	now := c.now().UTC()
	current.Status = to
	current.UpdatedAt = now
	return current, nil
}

// Handoff returns the task's current handoff record, if any.
func (c *Controller) Handoff() (Handoff, bool) {
	if c.task.Handoff == nil {
		return Handoff{}, false
	}
	return c.task.Handoff.Clone(), true
}

// PendingHandoff returns the undecided or approved handoff, if any.
func (c *Controller) PendingHandoff() (Handoff, bool) {
	if c.task.Handoff == nil || !c.task.Handoff.Status.Pending() {
		return Handoff{}, false
	}
	return c.task.Handoff.Clone(), true
}

// sanitizeHandoff repairs a loaded record.
//
// A pending transition that survived a restart is interrupted, not applied:
// the runtime cannot prove the authority switch happened. The task's recorded
// role stays the source role, so the last valid state remains recoverable. When
// the record is unrecoverable the caller drops it and reports the loss.
func sanitizeHandoff(h *Handoff, t Task) (*Handoff, int, string) {
	if h == nil {
		return nil, 0, ""
	}
	changes := 0
	out := h.Clone()
	out.ID = strings.TrimSpace(out.ID)
	out.TaskID = strings.TrimSpace(out.TaskID)
	out.Objective = strings.TrimSpace(out.Objective)
	out.Reason = boundText(strings.TrimSpace(out.Reason), maxHandoffText)
	out.Summary = boundText(strings.TrimSpace(out.Summary), maxHandoffText)
	out.Workspace = boundText(strings.TrimSpace(out.Workspace), maxHandoffText)
	out.ApprovedBy = boundText(strings.TrimSpace(out.ApprovedBy), maxHandoffText)
	out.Failure = boundText(strings.TrimSpace(out.Failure), maxHandoffText)
	if out.TaskID != t.ID {
		return nil, changes + 1, "the handoff record belongs to a different task"
	}
	if out.Objective == "" {
		out.Objective = t.Objective
		changes++
	}
	if out.Objective != t.Objective {
		return nil, changes + 1, "the handoff objective does not match the task"
	}
	if !out.From.Valid() || !out.To.Valid() || out.From == out.To || !out.Status.Valid() || out.ID == "" {
		return nil, changes + 1, "the handoff record is incomplete"
	}
	if out.Status.Pending() {
		out.Status = HandoffInterrupted
		out.Failure = "the application stopped before the handoff completed; authority did not move"
		out.UpdatedAt = time.Now().UTC()
		changes++
	}
	pruned := pruneHandoffRefs(out, t)
	if pruned > 0 {
		changes += pruned
	}
	return &out, changes, ""
}

// pruneHandoffRefs removes references whose record no longer exists. The
// transition stays valid; only its carried set is smaller and honestly so.
func pruneHandoffRefs(h Handoff, t Task) int {
	removed := 0
	facts := h.FactIDs[:0]
	for _, id := range h.FactIDs {
		if _, ok := t.Context.factByID(id); ok {
			facts = append(facts, id)
		} else {
			removed++
		}
	}
	h.FactIDs = facts
	hypotheses := h.HypothesisIDs[:0]
	for _, id := range h.HypothesisIDs {
		if _, ok := t.Context.hypothesisByID(id); ok {
			hypotheses = append(hypotheses, id)
		} else {
			removed++
		}
	}
	h.HypothesisIDs = hypotheses
	questions := h.QuestionIDs[:0]
	for _, id := range h.QuestionIDs {
		if _, ok := t.Context.questionByID(id); ok {
			questions = append(questions, id)
		} else {
			removed++
		}
	}
	h.QuestionIDs = questions
	evidence := h.EvidenceIDs[:0]
	for _, id := range h.EvidenceIDs {
		if _, ok := t.Context.evidenceByID(id); ok {
			evidence = append(evidence, id)
		} else {
			removed++
		}
	}
	h.EvidenceIDs = evidence
	if len(h.Constraints) > maxHandoffRefs {
		removed += len(h.Constraints) - maxHandoffRefs
		h.Constraints = h.Constraints[:maxHandoffRefs]
	}
	if len(h.Artifacts) > maxHandoffRefs {
		removed += len(h.Artifacts) - maxHandoffRefs
		h.Artifacts = h.Artifacts[:maxHandoffRefs]
	}
	return removed
}

// appendBound appends a non-empty reference while the list is below its cap.
func appendBound(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || len(values) >= maxHandoffRefs {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// boundRefs deduplicates and bounds a reference list.
func boundRefs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = appendBound(out, value)
	}
	return out
}
