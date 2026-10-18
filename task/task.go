// Package task owns OHGO's durable task continuity state.
//
// A Task is what the user is trying to accomplish. It is deliberately not the
// same thing as an agent iteration, a turn, or a tool call: those are the
// execution engine's concern (the loop), while a Task is the coordination and
// continuity layer around them. The separation matters because execution may
// restart, context may be reconstructed, and an interrupted run may be resumed
// — and in every one of those cases the Task must remain continuous.
//
// The package is domain and persistence only. It never executes anything, never
// talks to a provider, and never grants authority: every method here records
// state, and state is evidence, not permission.
package task

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Status is where a task is in its lifecycle.
//
// The zero value is not a valid status; use StatusPending. The distinctions are
// load-bearing: PAUSED is not FAILED (an interruption is not a failure),
// CANCELLED is not FAILED (a deliberate stop is not a broken run), and an
// interruption is never COMPLETED.
type Status string

// Task lifecycle states.
const (
	// StatusPending means the task exists but has not started.
	StatusPending Status = "PENDING"
	// StatusRunning means the task is executing now.
	StatusRunning Status = "RUNNING"
	// StatusPaused means execution stopped at a recoverable checkpoint and the
	// task can be resumed. It is not terminal.
	StatusPaused Status = "PAUSED"
	// StatusCompleted means the task reached its objective. Terminal.
	StatusCompleted Status = "COMPLETED"
	// StatusFailed means the task hit an unrecoverable execution error.
	// Terminal.
	StatusFailed Status = "FAILED"
	// StatusCancelled means the user deliberately abandoned the task.
	// Terminal.
	StatusCancelled Status = "CANCELLED"
)

// IsTerminal reports whether no further transition is expected.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Valid reports whether the status is one this package defines.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusRunning, StatusPaused, StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// TodoStatus is where one todo item is in its (small) lifecycle.
type TodoStatus string

// Todo states. The vocabulary is intentionally three-valued: Todos are mutable
// execution guidance, not a workflow engine.
const (
	// TodoTodo is not started.
	TodoTodo TodoStatus = "TODO"
	// TodoActive is being worked on now. At most one todo is active at a time.
	TodoActive TodoStatus = "ACTIVE"
	// TodoDone is finished. Whether that is a claim or verified is carried by
	// the item's Evidence, never by the status alone.
	TodoDone TodoStatus = "DONE"
)

// Valid reports whether the todo status is one this package defines.
func (s TodoStatus) Valid() bool {
	switch s {
	case TodoTodo, TodoActive, TodoDone:
		return true
	default:
		return false
	}
}

// Todo is one item of execution guidance.
//
// A Todo is not a claim of truth: Evidence records the runtime observation
// (a file changed, a command result, a test run) that justified marking it
// DONE. An item with no Evidence is a model proposal, and the architecture must
// never present it as independently verified.
type Todo struct {
	// ID is stable for the life of the item, so a checkpoint can address it.
	ID string `json:"id"`
	// Label is the human-readable task text.
	Label string `json:"label"`
	// Status is the lifecycle position.
	Status TodoStatus `json:"status"`
	// Evidence is the runtime observation supporting a DONE status, if any.
	Evidence string `json:"evidence,omitempty"`
}

// Task is the durable representation of what the user is trying to accomplish.
//
// The fields are deliberately few. Nothing here is speculative: each one is
// needed to resume without replaying the whole transcript, which is the whole
// point of the type.
type Task struct {
	// ID identifies the task across restarts. It is minted once and never
	// changes, so a resume continues the same task rather than a new one.
	ID string `json:"id"`
	// Objective is what the user is trying to accomplish.
	Objective string `json:"objective"`
	// Constraints are the limits the work must respect.
	Constraints []string `json:"constraints,omitempty"`
	// Phase is a coarse current stage, e.g. "plan", "act", "verify".
	Phase string `json:"phase,omitempty"`
	// Todos are the execution guidance, in order.
	Todos []Todo `json:"todos,omitempty"`
	// ActiveStep is a one-line description of the current step.
	ActiveStep string `json:"activeStep,omitempty"`
	// Findings are things learned that outlive a single turn.
	Findings []string `json:"findings,omitempty"`
	// NextAction is what should happen next, for orientation after a resume.
	NextAction string `json:"nextAction,omitempty"`
	// Status is the lifecycle position.
	Status Status `json:"status"`
	// UpdatedAt is when the task last changed. It is the checkpoint ordering
	// key: a write carrying an older stamp must never overwrite a newer one.
	UpdatedAt time.Time `json:"updatedAt"`
}

// ErrInvalidTransition means a lifecycle transition is not permitted from the
// task's current status.
var ErrInvalidTransition = errors.New("task: invalid status transition")

// ErrInvalidTask means a task is missing a field the contract requires.
var ErrInvalidTask = errors.New("task: invalid task")

// Validate reports whether the task satisfies the contract a checkpoint needs.
func (t Task) Validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("%w: empty id", ErrInvalidTask)
	}
	if !t.Status.Valid() {
		return fmt.Errorf("%w: unknown status %q", ErrInvalidTask, t.Status)
	}
	for i, todo := range t.Todos {
		if strings.TrimSpace(todo.ID) == "" {
			return fmt.Errorf("%w: todo %d has an empty id", ErrInvalidTask, i)
		}
		if !todo.Status.Valid() {
			return fmt.Errorf("%w: todo %q has unknown status %q", ErrInvalidTask, todo.ID, todo.Status)
		}
	}
	return nil
}

// New returns a task in RUNNING with a fresh identity.
//
// It starts RUNNING rather than PENDING because a task is only ever created at
// the moment a turn begins: there is no interval in which the task exists and
// execution has not started.
func New(objective string) (Task, error) {
	id, err := newID()
	if err != nil {
		return Task{}, err
	}
	return Task{
		ID:        id,
		Objective: strings.TrimSpace(objective),
		Status:    StatusRunning,
		UpdatedAt: time.Now().UTC(),
	}, nil
}

// Clone returns a deep copy, so a projection or checkpoint cannot alias the
// live task's slices.
func (t Task) Clone() Task {
	out := t
	out.Constraints = append([]string(nil), t.Constraints...)
	out.Findings = append([]string(nil), t.Findings...)
	out.Todos = append([]Todo(nil), t.Todos...)
	return out
}

// Todo returns the item with the given id.
func (t Task) FindTodo(id string) (Todo, bool) {
	for _, todo := range t.Todos {
		if todo.ID == id {
			return todo, true
		}
	}
	return Todo{}, false
}

// ActiveTodo returns the single active item, if any.
func (t Task) ActiveTodo() (Todo, bool) {
	for _, todo := range t.Todos {
		if todo.Status == TodoActive {
			return todo, true
		}
	}
	return Todo{}, false
}

// HasOpenTodos reports whether any todo is not DONE. It is the precondition for
// treating a successfully finished turn as a completed task: an objective with
// outstanding work is not done merely because the model stopped calling tools.
func (t Task) HasOpenTodos() bool {
	for _, todo := range t.Todos {
		if todo.Status != TodoDone {
			return true
		}
	}
	return false
}

// transition moves the task to a new status, stamping UpdatedAt. It refuses any
// move the lifecycle does not permit, so an interruption can never silently
// become COMPLETED and a terminal task can never be re-opened in place.
func (t *Task) transition(to Status, at time.Time) error {
	if !canTransition(t.Status, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.Status, to)
	}
	t.Status = to
	t.UpdatedAt = at
	return nil
}

// canTransition reports whether the lifecycle permits a move.
//
// Terminal states are absorbing: a completed or failed task is never re-opened.
// Continuing after completion means a *new* task, which is what keeps task
// identity honest across a resume.
func canTransition(from, to Status) bool {
	if from == to {
		return true
	}
	switch from {
	case StatusPending:
		return to == StatusRunning || to == StatusCancelled
	case StatusRunning:
		return to == StatusPaused || to == StatusCompleted || to == StatusFailed || to == StatusCancelled
	case StatusPaused:
		return to == StatusRunning || to == StatusCancelled || to == StatusFailed
	default:
		// Terminal states absorb every transition.
		return false
	}
}

// Start moves a pending or paused task to RUNNING.
func (t *Task) Start(at time.Time) error { return t.transition(StatusRunning, at) }

// Pause records an interruption to a recoverable checkpoint.
//
// It is valid from RUNNING only: a task that already finished or failed is not
// paused, and a pending task has nothing to interrupt.
func (t *Task) Pause(at time.Time) error { return t.transition(StatusPaused, at) }

// Resume moves a paused task back to RUNNING, preserving its identity.
func (t *Task) Resume(at time.Time) error { return t.transition(StatusRunning, at) }

// Complete marks the task finished. It is the only path to COMPLETED, and the
// caller is responsible for having actual completion evidence: an interruption
// must never reach this method.
func (t *Task) Complete(at time.Time) error { return t.transition(StatusCompleted, at) }

// Fail marks the task failed with an unrecoverable execution error.
func (t *Task) Fail(at time.Time) error { return t.transition(StatusFailed, at) }

// Cancel marks the task deliberately abandoned by the user.
func (t *Task) Cancel(at time.Time) error { return t.transition(StatusCancelled, at) }

// AddTodo appends an item with a fresh id and returns the id.
func (t *Task) AddTodo(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", fmt.Errorf("%w: empty todo label", ErrInvalidTask)
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	t.Todos = append(t.Todos, Todo{ID: id, Label: label, Status: TodoTodo})
	return id, nil
}

// SetTodos replaces the guidance wholesale, assigning ids to any item that
// arrives without one. It is how a projection from the runtime's todo owner
// refreshes the task's snapshot without inventing a second mutation path.
//
// An item whose label already exists keeps its id, so a label-addressed
// checkpoint stays valid across repeated syncs.
func (t *Task) SetTodos(todos []Todo) error {
	byLabel := make(map[string]string, len(t.Todos))
	for _, existing := range t.Todos {
		byLabel[existing.Label] = existing.ID
	}
	out := make([]Todo, 0, len(todos))
	for _, todo := range todos {
		if strings.TrimSpace(todo.Label) == "" {
			continue
		}
		if strings.TrimSpace(todo.ID) == "" {
			if id, ok := byLabel[todo.Label]; ok {
				todo.ID = id
			} else {
				id, err := newID()
				if err != nil {
					return err
				}
				todo.ID = id
			}
		}
		if !todo.Status.Valid() {
			todo.Status = TodoTodo
		}
		out = append(out, todo)
	}
	t.Todos = out
	return nil
}

// SetTodoStatus updates one item, enforcing the single-active invariant.
//
// Marking an item ACTIVE demotes any other active item to TODO: two active
// items would read as "both are being done now", which is never true.
func (t *Task) SetTodoStatus(id string, status TodoStatus, evidence string) error {
	if !status.Valid() {
		return fmt.Errorf("%w: unknown todo status %q", ErrInvalidTask, status)
	}
	found := false
	for i := range t.Todos {
		if t.Todos[i].ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: unknown todo %q", ErrInvalidTask, id)
	}
	for i := range t.Todos {
		if t.Todos[i].ID == id {
			continue
		}
		if status == TodoActive && t.Todos[i].Status == TodoActive {
			t.Todos[i].Status = TodoTodo
		}
	}
	for i := range t.Todos {
		if t.Todos[i].ID != id {
			continue
		}
		t.Todos[i].Status = status
		if evidence != "" {
			t.Todos[i].Evidence = evidence
		}
		if status != TodoDone {
			t.Todos[i].Evidence = ""
		}
	}
	return nil
}

// RemoveTodo drops an item. The runtime must not assume the original plan is
// permanently correct, so removing guidance that new evidence invalidated is a
// first-class operation.
func (t *Task) RemoveTodo(id string) bool {
	out := t.Todos[:0]
	removed := false
	for _, todo := range t.Todos {
		if todo.ID == id {
			removed = true
			continue
		}
		out = append(out, todo)
	}
	t.Todos = out
	return removed
}

// AddFinding records something learned that outlives the turn.
func (t *Task) AddFinding(text string) {
	if text = strings.TrimSpace(text); text != "" {
		t.Findings = append(t.Findings, text)
	}
}

// newID mints a random identifier.
func newID() (string, error) {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("task: generate id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
