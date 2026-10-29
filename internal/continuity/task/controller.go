package task

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// Source supplies the current todo snapshot from the runtime's single todo
// owner. The task package does not own todo mutation: it records the durable
// snapshot so an interrupted task can be oriented on resume.
type Source func() []Todo

// Controller is the task continuity layer around the existing execution engine.
//
// It is the single owner of Task state. It observes lifecycle boundaries the
// turn orchestrator reports (a turn beginning, a tool boundary, an
// interruption, completion, failure) and records them durably. It never
// executes anything and never grants authority: it is bookkeeping, and the loop
// remains the execution owner.
type Controller struct {
	store  *Store
	source Source
	now    func() time.Time

	task       Task
	lastAction string
	restored   bool

	// budgetLimits is the grant a new task starts under. The controller owns
	// the durable ledger, never enforcement: the root loop and its
	// descendants share one budget instance per run.
	budgetLimits budget.Limits
	budgetState  *budget.State

	// evidence is the durable knowledge acquired during the task. The
	// projector updates it in place; persist checkpoints a bounded copy.
	evidence *cognition.EvidenceState
	// audit is the last rendered projection, for the debug surface. It is
	// diagnostic text, never authority. auditMu guards it because the loop
	// records it on the turn goroutine while /audit reads it on the update
	// goroutine.
	auditMu    sync.Mutex
	audit      string
	lastReport agent.ProjectionReport
	hasReport  bool
}

// NewController returns a controller backed by a store.
//
// source may be nil, in which case the controller records no todo snapshot.
func NewController(store *Store, source Source) *Controller {
	return &Controller{
		store:    store,
		source:   source,
		now:      time.Now,
		evidence: cognition.NewEvidenceState(),
	}
}

// Store returns the store the controller writes through.
func (c *Controller) Store() *Store { return c.store }

// SetBudgetLimits records the grant new tasks start under. It does not touch
// the current task's ledger: a resume continues the consumption already
// recorded rather than resetting it.
func (c *Controller) SetBudgetLimits(limits budget.Limits) {
	c.budgetLimits = limits
	if c.budgetState == nil {
		c.budgetState = &budget.State{Limits: limits}
		return
	}
	// The user may deliberately resume with a larger grant. Keep the
	// cumulative usage; replace only the limits.
	c.budgetState.Limits = limits
}

// RunBudget returns the fresh root grant for one execution tree.
//
// A new run is an explicit user invocation, not a continuation inside a tree:
// descendants all share this instance, and none of them can replace it. The
// task ledger still accumulates every run's consumption, so resuming never
// erases what the previous run spent.
func (c *Controller) RunBudget() *budget.Budget {
	return budget.New(c.budgetLimits)
}

// RecordBudget folds one completed run's consumption into the task ledger.
func (c *Controller) RecordBudget(snapshot budget.Snapshot) {
	if c.budgetState == nil {
		c.budgetState = &budget.State{Limits: snapshot.Limits}
	}
	c.budgetState.Limits = snapshot.Limits
	c.budgetState.Used = c.budgetState.Used.Add(snapshot.Used)
	c.budgetState.UpdatedAt = c.now().UTC()
}

// BudgetState returns the durable ledger, if one exists.
func (c *Controller) BudgetState() (budget.State, bool) {
	if c.budgetState == nil {
		return budget.State{}, false
	}
	return *c.budgetState, true
}

// Evidence returns the durable evidence state for this task.
//
// The projector updates the returned state in place; the controller
// checkpoints a bounded copy at the next lifecycle boundary. The pointer is
// stable for the life of the task, so a projector created at a turn boundary
// keeps writing to the same knowledge.
func (c *Controller) Evidence() *cognition.EvidenceState {
	if c.evidence == nil {
		c.evidence = cognition.NewEvidenceState()
	}
	return c.evidence
}

// RecordProjection stores the last projection audit.
//
// It is diagnostic state. A malformed or missing audit never changes evidence,
// authority, or completion; it only makes the runtime harder to inspect.
func (c *Controller) RecordProjection(report agent.ProjectionReport) {
	text := cognition.RenderAudit(report)
	c.auditMu.Lock()
	c.audit = text
	c.lastReport = report
	c.hasReport = true
	c.auditMu.Unlock()
}

// LastProjection returns the last projection report and whether one exists.
//
// It is evidence diagnostics, not authority: a caller may use the report's
// sufficiency verdict to veto a false completion claim, but cannot use it to
// authorize an action.
func (c *Controller) LastProjection() (agent.ProjectionReport, bool) {
	c.auditMu.Lock()
	defer c.auditMu.Unlock()
	return c.lastReport, c.hasReport
}

// Audit returns the last projection audit for the debug surface.
func (c *Controller) Audit() string {
	c.auditMu.Lock()
	text := c.audit
	c.auditMu.Unlock()
	if text == "" {
		return "no context projection has run for this task"
	}
	return text
}

// Current returns the task in memory, if any.
func (c *Controller) Current() (Task, bool) {
	if c.task.ID == "" {
		return Task{}, false
	}
	return c.task.Clone(), true
}

// Restore loads any checkpoint and normalizes it for resume.
//
// A task left RUNNING on disk is restored as PAUSED: the process stopped
// without a terminal transition, so the runtime has no evidence the objective
// was reached. Restore never rewrites the files itself; the next recorded
// transition persists the normalized status.
func (c *Controller) Restore() (Task, bool, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, false, err
	}
	if c.task.ID == "" {
		return Task{}, false, nil
	}
	return c.task.Clone(), true, nil
}

// Begin records that a turn is starting with this contract and pre-run target
// evidence.
//
// It continues the current task when one is resumable (a paused task resumes
// with its identity intact) and creates a new one when there is none or when
// the previous task is terminal. It never turns an interruption into a fresh,
// unrelated task.
//
// A non-terminal task's contract is immutable. The incoming contract must be
// the one already persisted; a different objective is refused with
// ErrObjectiveConflict so the caller must ask the human to continue or abandon
// the task explicitly. The incoming snapshot is used only when the task is new:
// a resume keeps the original pre-run baseline, because recapturing an already
// changed file would erase the evidence the contract was built on.
func (c *Controller) Begin(intent contract.IntentContract, snapshot *agent.TargetSnapshot) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	objective := strings.TrimSpace(intent.Objective)

	now := c.now().UTC()
	switch {
	case c.task.ID != "" && !c.task.Status.IsTerminal():
		if c.task.Contract == nil {
			return Task{}, fmt.Errorf("%w: %q has no persisted completion contract; cancel or replace it explicitly",
				ErrObjectiveConflict, c.task.Objective)
		}
		if !reflect.DeepEqual(*c.task.Contract, intent) {
			return Task{}, fmt.Errorf("%w: persisted %q, requested %q",
				ErrObjectiveConflict, c.task.Contract.Objective, objective)
		}
		// A paused, reviewable, or pending task resumes with its identity
		// intact; a running one continues (a turn boundary, not a new task).
		if c.task.Status == StatusPaused || c.task.Status == StatusPending || c.task.Status == StatusNeedsReview {
			if err := c.task.Start(now); err != nil {
				return Task{}, err
			}
		}
		if c.budgetState == nil {
			c.budgetState = &budget.State{Limits: c.budgetLimits}
		}
		c.task.UpdatedAt = now
	default:
		created, err := New(objective)
		if err != nil {
			return Task{}, err
		}
		created.UpdatedAt = now
		cloned := cloneContract(intent)
		created.Contract = &cloned
		if snapshot != nil {
			clonedSnapshot := cloneSnapshot(*snapshot)
			created.Snapshot = &clonedSnapshot
		}
		c.task = created
		// A new task owns a new ledger and new evidence. Its identity is new,
		// so neither prior consumption nor prior observations apply.
		c.budgetState = &budget.State{Limits: c.budgetLimits, UpdatedAt: now}
		c.evidence = cognition.NewEvidenceState()
		c.auditMu.Lock()
		c.audit = ""
		c.lastReport = agent.ProjectionReport{}
		c.hasReport = false
		c.auditMu.Unlock()
	}

	c.syncTodos()
	c.lastAction = "started: " + describeObjective(c.task.Objective)
	return c.task.Clone(), c.persist()
}

// Sync refreshes the todo snapshot and checkpoints.
//
// It is called at meaningful boundaries — a todo tool call completing, a
// context compaction — rather than on every streaming event.
func (c *Controller) Sync() (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	c.syncTodos()
	c.task.UpdatedAt = c.now().UTC()
	if active, ok := c.task.ActiveTodo(); ok {
		c.lastAction = "working on: " + active.Label
	}
	return c.task.Clone(), c.persist()
}

// Pause records an interruption at a recoverable checkpoint.
//
// Pausing is idempotent and never converts a terminal task: an interruption of
// an already-completed task is not a pause.
func (c *Controller) Pause(reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	c.syncTodos()
	if c.task.Status != StatusPaused {
		if err := c.task.Pause(c.now().UTC()); err != nil {
			return c.task.Clone(), err
		}
	}
	c.lastAction = firstNonEmpty(reason, "interrupted")
	return c.task.Clone(), c.persist()
}

// Complete records a successful end when the work is actually finished.
//
// A turn can end cleanly while outstanding todos remain. That is progress, not
// completion: the task stays RUNNING and a checkpoint records the progress,
// because marking it COMPLETED on a model's clean stop alone would assert more
// than the runtime observed.
func (c *Controller) Complete() (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	c.syncTodos()
	now := c.now().UTC()
	if c.task.HasOpenTodos() {
		c.task.UpdatedAt = now
		c.lastAction = "turn finished with work remaining"
		return c.task.Clone(), c.persist()
	}
	if err := c.task.Complete(now); err != nil {
		return c.task.Clone(), err
	}
	c.lastAction = "completed"
	return c.task.Clone(), c.persist()
}

// NeedsReview records that a turn ended without enough runtime evidence to
// claim the objective was reached.
//
// It is deliberately distinct from PAUSED (an interruption) and FAILED (an
// unrecoverable error): the work may be fine, but the runtime did not observe
// the change or its verification. A later turn resumes the same task and can
// supply the missing evidence.
func (c *Controller) NeedsReview(reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	c.syncTodos()
	if reviewed := strings.TrimSpace(reason); reviewed != "" {
		c.task.ReviewReason = reviewed
	}
	if c.task.Status != StatusNeedsReview {
		if err := c.task.NeedsReview(c.now().UTC()); err != nil {
			return c.task.Clone(), err
		}
	}
	c.lastAction = firstNonEmpty(reason, "needs review")
	return c.task.Clone(), c.persist()
}

// Fail records an unrecoverable execution error.
func (c *Controller) Fail(reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	c.syncTodos()
	if err := c.task.Fail(c.now().UTC()); err != nil {
		return c.task.Clone(), err
	}
	c.lastAction = firstNonEmpty(reason, "failed")
	return c.task.Clone(), c.persist()
}

// Cancel records a deliberate abandon by the user.
func (c *Controller) Cancel(reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if c.task.ID == "" || c.task.Status.IsTerminal() {
		return c.task.Clone(), nil
	}
	c.syncTodos()
	if err := c.task.Cancel(c.now().UTC()); err != nil {
		return c.task.Clone(), err
	}
	c.lastAction = firstNonEmpty(reason, "cancelled")
	return c.task.Clone(), c.persist()
}

// Context returns deterministic orientation blocks for the current task.
//
// It is the basis for continuing after an interruption or a compaction without
// replaying the transcript: the model is told what we were doing, what has
// happened, what is known, what remains, and what should happen next.
func (c *Controller) Context() []string {
	if c.task.ID == "" {
		return nil
	}
	return Context(c.task, c.lastAction)
}

// ensureLoaded loads the checkpoint once.
//
// A corrupt checkpoint that still leaves a readable projection is not fatal:
// the projection carries enough to continue. Only a store with nothing
// recoverable leaves the controller with no task, and then Begin starts fresh.
func (c *Controller) ensureLoaded() error {
	if c.restored {
		return nil
	}
	c.restored = true

	snap, err := c.store.Load()
	if !snap.Found {
		return nil
	}
	c.task = NormalizeRecovered(snap.Task)
	// A non-terminal task from a version-1 checkpoint has no contract. It
	// cannot be completed, and it must not be silently rebound to whatever the
	// next prompt says; say why so the human can cancel or replace it.
	if !c.task.Status.IsTerminal() && c.task.Contract == nil && c.task.ReviewReason == "" {
		c.task.ReviewReason = "the persisted task has no completion contract; it can be cancelled or replaced, not completed"
	}
	if stored := snap.Progress.Budget; stored != nil {
		state := *stored
		if c.budgetState == nil {
			c.budgetState = &state
		} else {
			// An explicit grant supplied before Restore wins over the stored
			// limits; the consumed total always comes from disk, so a resume
			// can raise the ceiling but never erase what was spent.
			c.budgetState.Used = state.Used
			c.budgetState.UpdatedAt = state.UpdatedAt
		}
	}
	if snap.Progress.EvidenceState != nil {
		c.evidence = snap.Progress.EvidenceState.Clone()
	}
	if snap.Progress.Audit != "" {
		c.auditMu.Lock()
		c.audit = snap.Progress.Audit
		c.auditMu.Unlock()
	}
	if err != nil && c.task.ID == "" {
		// Nothing recoverable: report rather than silently starting over a
		// state we could not read.
		return fmt.Errorf("%w: %v", ErrCorruptTask, err)
	}
	if err == nil && snap.Progress.LastAction != "" {
		c.lastAction = snap.Progress.LastAction
	}
	return nil
}

// syncTodos refreshes the task's todo snapshot from the runtime's owner.
func (c *Controller) syncTodos() {
	if c.source == nil {
		return
	}
	// A sync failure must not abort a lifecycle transition: the labels are
	// still recorded, only their ids would be regenerated.
	_ = c.task.SetTodos(c.source())
}

// persist writes the checkpoint and projection, stamping the last action.
func (c *Controller) persist() error {
	if c.task.ID == "" {
		return nil
	}
	progress := ProgressFrom(c.task)
	if progress.CheckpointAt.IsZero() {
		progress.CheckpointAt = c.task.UpdatedAt
	}
	progress.LastAction = c.lastAction
	if c.budgetState != nil {
		state := *c.budgetState
		progress.Budget = &state
	}
	if c.evidence != nil && !c.evidence.Empty() {
		progress.EvidenceState = c.evidence.Clone()
	}
	c.auditMu.Lock()
	audit := c.audit
	c.auditMu.Unlock()
	if audit != "" {
		progress.Audit = audit
	}
	return c.store.SaveCheckpoint(c.task, progress)
}

// SetTodo marks one todo, for the controller's own tests and for a host that
// records an observed todo outcome. It is not exposed to the model.
func (c *Controller) SetTodo(id string, status TodoStatus, evidence string) (Task, error) {
	if c.task.ID == "" {
		return Task{}, errors.New("task: no active task")
	}
	if err := c.task.SetTodoStatus(id, status, evidence); err != nil {
		return Task{}, err
	}
	c.task.UpdatedAt = c.now().UTC()
	return c.task.Clone(), c.persist()
}

// Detach preserves the active task durably and clears the session binding.
//
// A non-terminal task is archived as resumable (RUNNING is first paused, so an
// interruption is never recorded as success) and the live slot is cleared, so
// the next session or the next new intent starts with no active task. A
// terminal task is archived as history and then cleared: completed, failed,
// and cancelled work is never resumable, but its objective, evidence, and
// outcome are never deleted. The archived copy keeps the full task, progress,
// budget, evidence, and audit, so /task resume can restore resumable work
// without replaying the transcript.
//
// It never deletes durable work: detaching moves the live task to the archive,
// it never drops it.
func (c *Controller) Detach() (Task, bool, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, false, err
	}
	if c.task.ID == "" {
		return Task{}, false, nil
	}
	detached := c.task.Clone()
	if detached.Status.IsTerminal() {
		progress := ProgressFrom(c.task)
		if progress.CheckpointAt.IsZero() {
			progress.CheckpointAt = c.task.UpdatedAt
		}
		progress.LastAction = firstNonEmpty(c.lastAction, "detached terminal task")
		if c.budgetState != nil {
			state := *c.budgetState
			progress.Budget = &state
		}
		_ = c.store.ArchiveTask(c.task, progress)
		c.task = Task{}
		c.evidence = cognition.NewEvidenceState()
		c.auditMu.Lock()
		c.audit = ""
		c.lastReport = agent.ProjectionReport{}
		c.hasReport = false
		c.auditMu.Unlock()
		c.lastAction = "detached terminal task"
		_ = c.store.ClearActive()
		return detached, false, nil
	}
	c.syncTodos()
	if detached.Status == StatusRunning {
		if err := c.task.Pause(c.now().UTC()); err != nil {
			return Task{}, false, err
		}
		detached = c.task.Clone()
		c.lastAction = firstNonEmpty(c.lastAction, "interrupted")
	}
	progress := ProgressFrom(c.task)
	if progress.CheckpointAt.IsZero() {
		progress.CheckpointAt = c.task.UpdatedAt
	}
	progress.LastAction = firstNonEmpty(c.lastAction, "detached for a new session")
	if c.budgetState != nil {
		state := *c.budgetState
		progress.Budget = &state
	}
	if c.evidence != nil && !c.evidence.Empty() {
		progress.EvidenceState = c.evidence.Clone()
	}
	c.auditMu.Lock()
	audit := c.audit
	c.auditMu.Unlock()
	if audit != "" {
		progress.Audit = audit
	}
	if err := c.store.ArchiveTask(c.task, progress); err != nil {
		return Task{}, false, err
	}
	archived := c.task.Clone()
	c.task = Task{}
	c.evidence = cognition.NewEvidenceState()
	c.auditMu.Lock()
	c.audit = ""
	c.lastReport = agent.ProjectionReport{}
	c.hasReport = false
	c.auditMu.Unlock()
	c.lastAction = "detached: " + describeObjective(archived.Objective)
	if err := c.store.ClearActive(); err != nil {
		return Task{}, false, err
	}
	return archived, true, nil
}

// ListResumable returns the active non-terminal task plus every archived
// non-terminal task, oldest first.
//
// The order is deterministic (checkpoint stamp, then ID), so /task list and
// /task resume with no ID resolve the same way every time. Terminal tasks are
// never resumable and are excluded.
func (c *Controller) ListResumable() ([]Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return nil, err
	}
	var out []Task
	seen := map[string]bool{}
	if c.task.ID != "" && IsResumable(c.task.Status) {
		out = append(out, c.task.Clone())
		seen[c.task.ID] = true
	}
	archived, err := c.store.ListArchived()
	if err != nil {
		return nil, err
	}
	for _, snap := range archived {
		t := NormalizeRecovered(snap.Task)
		if !IsResumable(t.Status) || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		out = append(out, t.Clone())
	}
	return out, nil
}

// ResumeTask attaches a resumable task to the current session.
//
// An empty id resumes the latest resumable task (newest stamp, ID tie-break);
// an unknown id is an error, never an arbitrary pick. Resuming archives the
// current active task first when it is a different non-terminal task, so no
// durable work is lost by switching. The resumed task moves to RUNNING with
// its persisted contract, snapshot, review reason, budget, evidence, and audit
// intact: durable state remains authoritative and is never reconstructed from
// the transcript.
func (c *Controller) ResumeTask(id string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	target, progress, found, err := c.findResumable(id)
	if err != nil {
		return Task{}, err
	}
	if !found {
		if strings.TrimSpace(id) == "" {
			return Task{}, errors.New("task: no resumable task")
		}
		return Task{}, fmt.Errorf("task: no resumable task %q", id)
	}
	if c.task.ID != "" && c.task.ID != target.ID && IsResumable(c.task.Status) {
		if _, _, err := c.Detach(); err != nil {
			return Task{}, err
		}
	}
	c.task = target
	if stored := progress.Budget; stored != nil {
		state := *stored
		if c.budgetState == nil {
			c.budgetState = &state
		} else {
			c.budgetState.Used = state.Used
			c.budgetState.UpdatedAt = state.UpdatedAt
			if c.budgetState.Limits == (budget.Limits{}) {
				c.budgetState.Limits = state.Limits
			}
		}
	}
	if progress.EvidenceState != nil {
		c.evidence = progress.EvidenceState.Clone()
	} else if c.evidence == nil {
		c.evidence = cognition.NewEvidenceState()
	}
	if progress.Audit != "" {
		c.auditMu.Lock()
		c.audit = progress.Audit
		c.auditMu.Unlock()
	}
	if progress.LastAction != "" {
		c.lastAction = progress.LastAction
	}
	now := c.now().UTC()
	switch c.task.Status {
	case StatusPaused, StatusPending, StatusNeedsReview:
		if err := c.task.Start(now); err != nil {
			return Task{}, err
		}
	case StatusRunning:
		c.task.UpdatedAt = now
	}
	c.lastAction = "resumed: " + describeObjective(c.task.Objective)
	if err := c.persist(); err != nil {
		return Task{}, err
	}
	_ = c.store.RemoveArchived(c.task.ID)
	return c.task.Clone(), nil
}

// CancelResumable cancels one resumable task by ID, or the latest when empty.
//
// Cancelling is terminal and never claims completion. A cancelled archived
// task stays on disk as CANCELLED so it is never listed as resumable again;
// the live task uses the same transition and remains CANCELLED in place.
func (c *Controller) CancelResumable(id, reason string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	if strings.TrimSpace(id) == "" {
		resumable, err := c.ListResumable()
		if err != nil {
			return Task{}, err
		}
		if len(resumable) == 0 {
			return Task{}, errors.New("task: no resumable task")
		}
		id = resumable[len(resumable)-1].ID
	}
	if c.task.ID == id {
		closed, err := c.Cancel(reason)
		if err != nil {
			return Task{}, err
		}
		return closed, nil
	}
	snap, err := c.store.LoadArchived(id)
	if err != nil {
		return Task{}, err
	}
	if !snap.Found {
		return Task{}, fmt.Errorf("task: no resumable task %q", id)
	}
	t := NormalizeRecovered(snap.Task)
	if t.Status.IsTerminal() {
		return t.Clone(), nil
	}
	now := c.now().UTC()
	switch t.Status {
	case StatusRunning:
		if err := t.Pause(now); err != nil {
			return Task{}, err
		}
		fallthrough
	default:
		if err := t.Cancel(now); err != nil {
			// NEEDS_REVIEW -> CANCELLED is permitted; RUNNING was paused above.
			// Any other refusal is a lifecycle error worth surfacing.
			return Task{}, err
		}
	}
	progress := snap.Progress
	progress.Status = t.Status
	progress.TaskID = t.ID
	progress.CheckpointAt = t.UpdatedAt
	if err := c.store.ArchiveTask(t, progress); err != nil {
		return Task{}, err
	}
	return t.Clone(), nil
}

// findResumable locates one resumable task and its progress, latest when empty.
func (c *Controller) findResumable(id string) (Task, Progress, bool, error) {
	id = strings.TrimSpace(id)
	if id != "" && c.task.ID == id && IsResumable(c.task.Status) {
		progress := ProgressFrom(c.task)
		progress.LastAction = c.lastAction
		return c.task.Clone(), progress, true, nil
	}
	archived, err := c.store.ListArchived()
	if err != nil {
		return Task{}, Progress{}, false, err
	}
	if id != "" {
		for _, snap := range archived {
			if snap.Task.ID == id && IsResumable(NormalizeRecovered(snap.Task).Status) {
				t := NormalizeRecovered(snap.Task)
				return t.Clone(), snap.Progress, true, nil
			}
		}
		if c.task.ID == id {
			progress := ProgressFrom(c.task)
			return c.task.Clone(), progress, IsResumable(c.task.Status), nil
		}
		return Task{}, Progress{}, false, nil
	}
	var (
		best      Task
		bestProg  Progress
		bestFound bool
	)
	consider := func(t Task, p Progress) {
		t = NormalizeRecovered(t)
		if !IsResumable(t.Status) {
			return
		}
		if !bestFound || p.CheckpointAt.After(bestProg.CheckpointAt) ||
			(p.CheckpointAt.Equal(bestProg.CheckpointAt) && t.ID > best.ID) {
			best, bestProg, bestFound = t.Clone(), p, true
		}
	}
	if c.task.ID != "" {
		p := ProgressFrom(c.task)
		p.CheckpointAt = c.task.UpdatedAt
		p.LastAction = c.lastAction
		consider(c.task, p)
	}
	for _, snap := range archived {
		consider(snap.Task, snap.Progress)
	}
	return best, bestProg, bestFound, nil
}

func describeObjective(objective string) string {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return "task"
	}
	return objective
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
