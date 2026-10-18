package task

import (
	"errors"
	"testing"
	"time"
)

// A new task is RUNNING: it is only created as a turn begins, so a PENDING
// interval would be a state the runtime never observes.
func TestNewTaskIsRunning(t *testing.T) {
	task, err := New("ship phase 1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if task.Status != StatusRunning {
		t.Fatalf("status = %s, want RUNNING", task.Status)
	}
	if task.ID == "" {
		t.Fatal("a task without an id cannot be resumed")
	}
}

// An interruption is recoverable, not terminal: PAUSED must resume the same
// task rather than start an unrelated one.
func TestPauseResumeKeepsTaskIdentity(t *testing.T) {
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := task.ID
	if err := task.Pause(time.Now()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if task.Status != StatusPaused {
		t.Fatalf("status = %s, want PAUSED", task.Status)
	}
	if err := task.Resume(time.Now()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if task.Status != StatusRunning {
		t.Fatalf("status = %s, want RUNNING", task.Status)
	}
	if task.ID != id {
		t.Fatalf("id changed across pause/resume: %q -> %q", id, task.ID)
	}
}

// An interruption must never reach COMPLETED without an explicit resume: the
// runtime has no evidence the objective was reached.
func TestInterruptedTaskCannotCompleteDirectly(t *testing.T) {
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := task.Pause(time.Now()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := task.Complete(time.Now()); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Complete after interrupt = %v, want ErrInvalidTransition", err)
	}
	if task.Status != StatusPaused {
		t.Fatalf("status = %s, want PAUSED", task.Status)
	}
}

// Terminal states are absorbing: resuming a completed task in place would
// silently reopen finished work under the same identity.
func TestTerminalStatesAreAbsorbing(t *testing.T) {
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := task.Complete(time.Now()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	for _, transition := range []struct {
		name string
		fn   func(time.Time) error
	}{
		{"pause", task.Pause},
		{"resume", task.Resume},
		{"fail", task.Fail},
		{"cancel", task.Cancel},
	} {
		if err := transition.fn(time.Now()); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("%s after COMPLETED = %v, want ErrInvalidTransition", transition.name, err)
		}
	}
	if task.Status != StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", task.Status)
	}
}

// Cancelled and failed are distinct outcomes: a deliberate stop is not a
// broken run.
func TestCancelIsNotFailure(t *testing.T) {
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := task.Cancel(time.Now()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if task.Status != StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED", task.Status)
	}
	if task.Status == StatusFailed {
		t.Fatal("CANCELLED must not be reported as FAILED")
	}
}

// Exactly one todo may be ACTIVE; starting another demotes the previous one so
// the UI never claims two things are being done at once.
func TestOnlyOneTodoIsActive(t *testing.T) {
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first, _ := task.AddTodo("first")
	second, _ := task.AddTodo("second")
	if err := task.SetTodoStatus(first, TodoActive, ""); err != nil {
		t.Fatalf("activate first: %v", err)
	}
	if err := task.SetTodoStatus(second, TodoActive, ""); err != nil {
		t.Fatalf("activate second: %v", err)
	}
	if task.Todos[0].Status != TodoTodo {
		t.Fatalf("first = %s, want TODO after second became active", task.Todos[0].Status)
	}
	if task.Todos[1].Status != TodoActive {
		t.Fatalf("second = %s, want ACTIVE", task.Todos[1].Status)
	}
}

// Evidence belongs to a DONE item only. Leaving it on an item that moved back
// to TODO would make a stale observation look like current proof.
func TestEvidenceOnlySurvivesDone(t *testing.T) {
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, _ := task.AddTodo("run tests")
	if err := task.SetTodoStatus(id, TodoDone, "go test ./... passed"); err != nil {
		t.Fatalf("done: %v", err)
	}
	if task.Todos[0].Evidence == "" {
		t.Fatal("evidence was dropped from a DONE item")
	}
	if err := task.SetTodoStatus(id, TodoTodo, ""); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if task.Todos[0].Evidence != "" {
		t.Fatal("evidence survived reopening the item")
	}
}

// Outstanding work means the objective is not done, even if the model stopped
// calling tools.
func TestHasOpenTodos(t *testing.T) {
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if task.HasOpenTodos() {
		t.Fatal("an empty plan reported open work")
	}
	id, _ := task.AddTodo("work")
	if !task.HasOpenTodos() {
		t.Fatal("a TODO item was not reported as open work")
	}
	_ = task.SetTodoStatus(id, TodoDone, "observed")
	if task.HasOpenTodos() {
		t.Fatal("a fully DONE plan still reported open work")
	}
}

// RemoveTodo is how new evidence invalidates the original plan without
// inventing a workflow status.
func TestRemoveTodo(t *testing.T) {
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, _ := task.AddTodo("stale step")
	if !task.RemoveTodo(id) {
		t.Fatal("RemoveTodo reported false for an existing item")
	}
	if len(task.Todos) != 0 {
		t.Fatalf("todos = %d, want 0", len(task.Todos))
	}
	if task.RemoveTodo(id) {
		t.Fatal("RemoveTodo reported true for a missing item")
	}
}
