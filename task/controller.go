package task

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/budget"
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
}

// NewController returns a controller backed by a store.
//
// source may be nil, in which case the controller records no todo snapshot.
func NewController(store *Store, source Source) *Controller {
	return &Controller{store: store, source: source, now: time.Now}
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

// Begin records that a turn is starting with this objective.
//
// It continues the current task when one is resumable (a paused task resumes
// with its identity intact) and creates a new one when there is none or when
// the previous task is terminal. It never turns an interruption into a fresh,
// unrelated task.
func (c *Controller) Begin(objective string) (Task, error) {
	if err := c.ensureLoaded(); err != nil {
		return Task{}, err
	}
	objective = strings.TrimSpace(objective)

	now := c.now().UTC()
	switch {
	case c.task.ID != "" && !c.task.Status.IsTerminal():
		// A paused, reviewable, or pending task resumes with its identity
		// intact; a running one continues (a turn boundary, not a new task).
		if c.task.Status == StatusPaused || c.task.Status == StatusPending || c.task.Status == StatusNeedsReview {
			if err := c.task.Start(now); err != nil {
				return Task{}, err
			}
		}
		if objective != "" && objective != c.task.Objective {
			c.task.NextAction = objective
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
		c.task = created
		// A new task owns a new ledger. Its identity is new, so there is no
		// prior consumption to carry forward.
		c.budgetState = &budget.State{Limits: c.budgetLimits, UpdatedAt: now}
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
