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
	// Consumed records that a turn has already attached to this task through
	// the handoff. An applied-but-unconsumed handoff is the next turn's
	// continuation signal; after that turn it stops being one, so the approval
	// does not make every later prompt attach to the task.
	Consumed bool `json:"consumed,omitempty"`
	// CreatedAt is when the handoff was proposed.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the record last changed.
	UpdatedAt time.Time `json:"updatedAt"`
	// AppliedAt is when the target role actually became active.
	AppliedAt time.Time `json:"appliedAt,omitempty"`
}

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
		Facts:       len(h.FactIDs),
		Hypotheses:  len(h.HypothesisIDs),
		Questions:   len(h.QuestionIDs),
		Evidence:    len(h.EvidenceIDs),
		Artifacts:   len(h.Artifacts),
		Constraints: len(h.Constraints),
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

// RecordWorkspaceSwitch records an explicit human workspace command as an
// already-applied transition.
//
// A slash command is itself the human's decision, so it needs no proposal and
// no second approval. Recording it keeps the next turn attached to the same
// task (the applied-but-unconsumed signal) and leaves a truthful recovery
// record. It never performs the switch: the caller must already have moved the
// live authority through the workspace owner.
func (c *Controller) RecordWorkspaceSwitch(from, to workspace.Workspace, workspaceState string) error {
	return c.recordWorkspaceSwitch(from, to,
		"explicit workspace command",
		"human",
		"workspace switched by explicit human command",
		workspaceState)
}

// RecordAutomaticSwitch records a control-plane BUILD <-> FUZZ handoff as an
// already-applied transition.
//
// The control plane, not the human, selected this execution-workspace move, so
// the record names that provenance. It is still a transition of the same task:
// the record preserves identity and evidence, and it grants no capability. The
// caller must already have moved the live authority through the workspace
// owner, exactly like RecordWorkspaceSwitch.
func (c *Controller) RecordAutomaticSwitch(from, to workspace.Workspace, reason, workspaceState string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "control-plane workspace transition"
	}
	return c.recordWorkspaceSwitch(from, to, reason, "control-plane",
		"workspace switched by the control plane: "+reason, workspaceState)
}

// recordWorkspaceSwitch is the shared recording path for an already-applied
// authority move.
func (c *Controller) recordWorkspaceSwitch(from, to workspace.Workspace, reason, approvedBy, summary, workspaceState string) error {
	if err := c.ensureLoaded(); err != nil {
		return err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return nil
	}
	if !from.Valid() || !to.Valid() || from == to {
		return nil
	}
	// The caller contract is "the authority already moved". Verify it against
	// the live gate when one is installed: recording a switch the host did not
	// perform would make the task metadata disagree with the only real
	// authority, and recovery would then describe a role that never existed.
	// Without a role source the controller cannot observe the gate, so it keeps
	// the documented host-only contract.
	if c.roleSource != nil {
		live := c.currentRole()
		if !live.Valid() || live != to {
			return fmt.Errorf("%w: the live workspace is %q, not the claimed target %q",
				ErrInvalidHandoff, live, to)
		}
	}
	// The source is the role the task actually ran under. Rejecting a claimed
	// human source keeps the recovery narrative from naming a role this task
	// never held; the live gate above is still the only authority. An automatic
	// control-plane move may repair stale metadata after a restart: the live
	// workspace is the source, and the transition records that fact.
	if approvedBy == "human" {
		if recorded := c.task.LastRole(); recorded.Valid() && recorded != from {
			return fmt.Errorf("%w: the task last ran under %q, not the claimed source %q",
				ErrInvalidHandoff, recorded, from)
		}
	}
	// A transition proposal already recorded this exact move. Leave it in its
	// approved state so the turn boundary applies it with the human's reason
	// and summary instead of replacing it with the generic command record.
	if existing := c.task.Handoff; existing != nil && existing.From == from && existing.To == to &&
		(existing.Status == HandoffApproved || existing.Status == HandoffApplied) {
		return nil
	}
	before := c.snapshotState()
	now := c.now().UTC()
	handoff, err := c.snapshotHandoff(from, HandoffInput{
		To:        to,
		Reason:    reason,
		Summary:   summary,
		Workspace: workspaceState,
	}, now)
	if err != nil {
		return err
	}
	handoff.Status = HandoffApplied
	handoff.ApprovedBy = approvedBy
	handoff.AppliedAt = now
	handoff.UpdatedAt = now
	handoff.Consumed = false
	c.task.Handoff = &handoff
	c.task.ActiveRole = to
	c.role = to
	c.roleKnown = true
	c.task.UpdatedAt = now
	if _, err := c.task.UpdateRoleContext(to, RoleContextUpdate{
		Progress: []string{"resumed via " + approvedBy + " workspace switch from " + from.Label()},
	}, now); err != nil {
		return err
	}
	c.lastAction = "workspace switched: " + from.Label() + " -> " + to.Label()
	c.task.AppendEvent(TimelineEvent{
		At:      now,
		Kind:    TimelineHandoff,
		Role:    to,
		Status:  string(HandoffApplied),
		Summary: summary + ": " + from.Label() + " -> " + to.Label(),
		Refs:    []string{handoff.ID},
	})
	if _, err := c.commitState(before); err != nil {
		return err
	}
	return nil
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
	// The source is the live authority, never the task's recorded role: after a
	// recovered transition the recorded role can still name the previous
	// workspace while the live gate already moved. Deriving the source from
	// continuity metadata would then refuse a valid narrowing or mislabel it.
	from := c.currentRole()
	if !from.Valid() {
		return Handoff{}, fmt.Errorf("%w: the live workspace is unresolved", ErrInvalidHandoff)
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
	beforeTimeline := cloneTimeline(c.task.Timeline)
	c.task.AppendEvent(TimelineEvent{
		At:      now,
		Kind:    TimelineHandoff,
		Role:    from,
		Status:  string(HandoffProposed),
		Summary: "handoff proposed: " + from.Label() + " -> " + in.To.Label(),
		Refs:    []string{handoff.ID},
	})
	if err := c.persist(); err != nil {
		// A failed write must not leave a proposal in memory that the durable
		// state does not contain.
		c.task.Handoff = nil
		c.task.Timeline = beforeTimeline
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
	from := c.currentRole()
	if !from.Valid() {
		return Handoff{}, fmt.Errorf("%w: the live workspace is unresolved", ErrInvalidHandoff)
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
	// Evidence is relevance-ordered, not recency-only: the records an active
	// fact or hypothesis cites come first, then the newest current records.
	// The target still gets the full briefing through ContextFor; this is the
	// bounded transition snapshot.
	referenced := make(map[string]bool)
	for _, fact := range c.task.Context.Facts {
		for _, id := range fact.EvidenceIDs {
			referenced[id] = true
		}
	}
	for _, hypothesis := range c.task.Context.Hypotheses {
		for _, id := range hypothesis.EvidenceIDs {
			referenced[id] = true
		}
	}
	for _, record := range c.task.Context.Evidence {
		if record.Validity == EvidenceCurrent && referenced[record.ID] {
			handoff.EvidenceIDs = appendBound(handoff.EvidenceIDs, record.ID)
		}
	}
	for i := len(c.task.Context.Evidence) - 1; i >= 0 && len(handoff.EvidenceIDs) < maxHandoffRefs; i-- {
		record := c.task.Context.Evidence[i]
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
	if err := c.ensureLoaded(); err != nil {
		return Handoff{}, err
	}
	before := c.snapshotState()
	handoff, err := c.transitionHandoff(id, HandoffApproved)
	if err != nil {
		return Handoff{}, err
	}
	handoff.ApprovedBy = "human"
	c.task.Handoff = &handoff
	c.task.AppendEvent(TimelineEvent{
		At:      handoff.UpdatedAt,
		Kind:    TimelineHandoff,
		Role:    handoff.From,
		Status:  string(handoff.Status),
		Summary: "handoff approved: " + handoff.From.Label() + " -> " + handoff.To.Label(),
		Refs:    []string{handoff.ID},
	})
	if _, err := c.commitState(before); err != nil {
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
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.Handoff == nil || c.task.Handoff.ID != id {
		return Task{}, fmt.Errorf("%w: %q", ErrUnknownHandoff, id)
	}
	before := c.snapshotState()
	if err := c.applyApprovedHandoff(c.now().UTC()); err != nil {
		return Task{}, err
	}
	return c.commitState(before)
}

// applyApprovedHandoff records the completed transition in memory. It is the
// single place that changes the task's active role through a handoff, and it
// never performs the authority switch itself: the caller must already have
// applied that through the workspace owner.
func (c *Controller) applyApprovedHandoff(now time.Time) error {
	handoff := c.task.Handoff
	if handoff == nil || handoff.Status != HandoffApproved {
		return fmt.Errorf("%w: no approved handoff to apply", ErrInvalidHandoff)
	}
	handoff.Status = HandoffApplied
	handoff.UpdatedAt = now
	handoff.AppliedAt = now
	handoff.Failure = ""
	handoff.Consumed = false
	c.task.ActiveRole = handoff.To
	c.role = handoff.To
	c.roleKnown = true
	c.task.UpdatedAt = now
	note := "resumed via handoff from " + handoff.From.Label()
	if handoff.Summary != "" {
		note += ": " + handoff.Summary
	}
	if _, err := c.task.UpdateRoleContext(handoff.To, RoleContextUpdate{Progress: []string{note}}, now); err != nil {
		return err
	}
	c.lastAction = "handoff applied: " + handoff.From.Label() + " -> " + handoff.To.Label()
	c.task.AppendEvent(TimelineEvent{
		At:      now,
		Kind:    TimelineHandoff,
		Role:    handoff.To,
		Status:  string(handoff.Status),
		Summary: "handoff applied: " + handoff.From.Label() + " -> " + handoff.To.Label(),
		Refs:    []string{handoff.ID},
	})
	return nil
}

// RejectHandoff records a human decline. Authority did not move.
func (c *Controller) RejectHandoff(id, reason string) (Handoff, error) {
	if err := c.ensureLoaded(); err != nil {
		return Handoff{}, err
	}
	before := c.snapshotState()
	handoff, err := c.transitionHandoff(id, HandoffRejected)
	if err != nil {
		return Handoff{}, err
	}
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		handoff.Failure = boundText(trimmed, maxHandoffText)
	}
	c.task.Handoff = &handoff
	c.task.AppendEvent(TimelineEvent{
		At:      handoff.UpdatedAt,
		Kind:    TimelineHandoff,
		Role:    handoff.From,
		Status:  string(handoff.Status),
		Summary: "handoff rejected: " + handoff.From.Label() + " -> " + handoff.To.Label(),
		Refs:    []string{handoff.ID},
	})
	if _, err := c.commitState(before); err != nil {
		return Handoff{}, err
	}
	return handoff.Clone(), nil
}

// FailHandoff records that the host could not complete the transition. The
// recorded active role is left untouched, so the last valid role remains
// authoritative.
func (c *Controller) FailHandoff(id, reason string) (Handoff, error) {
	if err := c.ensureLoaded(); err != nil {
		return Handoff{}, err
	}
	before := c.snapshotState()
	handoff, err := c.transitionHandoff(id, HandoffFailed)
	if err != nil {
		return Handoff{}, err
	}
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		handoff.Failure = boundText(trimmed, maxHandoffText)
	}
	c.task.Handoff = &handoff
	c.task.AppendEvent(TimelineEvent{
		At:      handoff.UpdatedAt,
		Kind:    TimelineHandoff,
		Role:    handoff.From,
		Status:  string(handoff.Status),
		Summary: "handoff failed: " + handoff.From.Label() + " -> " + handoff.To.Label(),
		Refs:    []string{handoff.ID},
	})
	if _, err := c.commitState(before); err != nil {
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

// HandoffRecovery is the outcome of reconciling a recovered transition with the
// authority that was actually restored.
type HandoffRecovery struct {
	// Handoff is the record the caller may trust. On success it is the record
	// after the decision; when persisting failed it is the prior durable record
	// because the decision was rolled back.
	Handoff Handoff
	// Changed reports whether the record or the task role changed.
	Changed bool
	// Notice is a truthful, user-facing sentence for the recovery boundary. It
	// names only facts the restored state establishes. Empty when there is
	// nothing to report.
	Notice string
}

// ReconcileRecoveredHandoff resolves an approved transition after a restart.
//
// Approval is not evidence that the authority switch happened, and it is not
// evidence that it did not. The live workspace is the only authority that can
// settle the question, so this method takes it explicitly and never reads the
// session store. It is the application boundary's job to supply the restored
// authority.
//
// The rules are deliberately narrow:
//
//   - an unapproved proposal is closed as interrupted; it never moves authority;
//   - an approved record whose target is the live role is recorded as applied
//     without switching anything, and is marked consumed so recovery cannot
//     resume a model turn by itself;
//   - an approved record whose source is the live role stays unapplied and is
//     reported as interrupted;
//   - any other restored role is a discrepancy: the record is closed as
//     interrupted, the task's recorded role is left untouched, and the caller
//     reports what it actually observed.
//
// Reconciliation never grants authority, never switches authority, and never
// starts a turn. It only makes the durable narrative match the restored state.
func (c *Controller) ReconcileRecoveredHandoff(live workspace.Workspace) (HandoffRecovery, error) {
	if err := c.ensureLoaded(); err != nil {
		return HandoffRecovery{}, err
	}
	if c.task.Handoff == nil || c.task.Status.IsTerminal() {
		return HandoffRecovery{}, nil
	}
	before := c.snapshotState()
	current := c.task.Handoff.Clone()
	now := c.now().UTC()
	notice := ""
	switch current.Status {
	case HandoffProposed:
		// Sanitization normally closes this before the boundary sees it. Keep
		// the rule here too, so a host that builds a task in memory cannot
		// treat an unapproved proposal as anything else.
		current.Status = HandoffInterrupted
		current.Failure = "the handoff was never approved; the transition was not applied"
		notice = "handoff " + current.From.Label() + " -> " + current.To.Label() + ": " + current.Failure
	case HandoffApproved:
		switch {
		case !live.Valid():
			current.Status = HandoffInterrupted
			current.Failure = "the restored workspace is unresolved; the handoff was not applied"
			notice = "handoff " + current.From.Label() + " -> " + current.To.Label() + ": " + current.Failure
		case live == current.To:
			current.Status = HandoffApplied
			current.AppliedAt = now
			current.Failure = ""
			// Recovery reconciles the record; it does not continue work. Marking
			// it consumed keeps the approval from attaching an unrelated next
			// prompt to this task. The human can still continue explicitly.
			current.Consumed = true
			if _, err := c.task.UpdateRoleContext(current.To, RoleContextUpdate{
				Progress: []string{"recovered applied handoff from " + current.From.Label()},
			}, now); err != nil {
				// The record is still the loaded APPROVED one; do not report a
				// disposition the task does not carry.
				return HandoffRecovery{Handoff: c.task.Handoff.Clone()}, err
			}
			c.task.ActiveRole = current.To
			c.role = current.To
			c.roleKnown = true
			notice = "approved handoff " + current.From.Label() + " -> " + current.To.Label() +
				" matches the restored " + current.To.Label() + " workspace; recorded as applied, no turn was executed"
		case live == current.From:
			current.Status = HandoffInterrupted
			current.Failure = "the application stopped after approval and before the authority switch; the workspace is still " + current.From.Label()
			notice = "handoff " + current.From.Label() + " -> " + current.To.Label() + " was not applied; the workspace is still " + current.From.Label()
		default:
			current.Status = HandoffInterrupted
			current.Failure = "the restored workspace " + live.Label() + " matches neither the handoff source " +
				current.From.Label() + " nor target " + current.To.Label() + "; the handoff was not applied"
			notice = "recovery discrepancy: " + current.Failure
		}
	default:
		return HandoffRecovery{Handoff: current}, nil
	}
	current.UpdatedAt = now
	c.task.Handoff = &current
	c.task.UpdatedAt = now
	c.lastAction = "handoff recovery: " + handoffStatusLabel(current)
	c.task.AppendEvent(TimelineEvent{
		At:      now,
		Kind:    TimelineHandoff,
		Role:    live,
		Status:  string(current.Status),
		Summary: "handoff recovery: " + current.From.Label() + " -> " + current.To.Label() + " " + string(current.Status),
		Refs:    []string{current.ID},
	})
	if err := c.persist(); err != nil {
		// The reconciliation is a decision, not a fact until the checkpoint
		// commit lands. Restore the loaded record so this boundary cannot
		// expose an APPLIED/INTERRUPTED disposition the durable state never
		// recorded; the next boundary reconciles again.
		c.restoreState(before)
		return HandoffRecovery{
			Handoff: before.task.Handoff.Clone(),
			Notice:  notice + " (not recorded: " + err.Error() + ")",
		}, err
	}
	return HandoffRecovery{Handoff: current.Clone(), Changed: true, Notice: notice}, nil
}

// handoffStatusLabel names a disposition for the last-action diagnostic.
func handoffStatusLabel(h Handoff) string {
	return h.From.Label() + " -> " + h.To.Label() + " " + string(h.Status)
}

// sanitizeHandoff repairs a loaded record.
//
// A proposal that was never approved cannot have moved authority, so it is
// closed as interrupted. An approved record is deliberately left untouched:
// approval proves the human consented, not that the switch happened, and not
// that it did not. Only the application boundary, which can compare the record
// with the restored workspace, may decide an approved transition.
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
	// A projection-less recovery has no task objective to compare against; the
	// handoff's own objective is the only readable copy and is accepted.
	if t.Objective != "" && out.Objective != t.Objective {
		return nil, changes + 1, "the handoff objective does not match the task"
	}
	if !out.From.Valid() || !out.To.Valid() || out.From == out.To || !out.Status.Valid() || out.ID == "" {
		return nil, changes + 1, "the handoff record is incomplete"
	}
	if out.Status == HandoffProposed {
		out.Status = HandoffInterrupted
		out.Failure = "the handoff was never approved; the transition was not applied"
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
		if t.Context.hasQuestion(id) {
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
