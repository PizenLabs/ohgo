package task

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A paused turn is resumed, not replaced: the next turn continues the same
// task identity.
func TestControllerPauseThenResumeContinuesSameTask(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)

	begun, err := controller.Begin("ship phase 1")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if begun.Status != StatusRunning {
		t.Fatalf("status = %s, want RUNNING", begun.Status)
	}
	paused, err := controller.Pause("interrupted by user")
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused.Status != StatusPaused {
		t.Fatalf("status = %s, want PAUSED", paused.Status)
	}

	// The artifacts must exist: recovery cannot depend on process memory.
	if _, err := os.Stat(store.TaskPath()); err != nil {
		t.Fatalf("task.md was not written: %v", err)
	}
	if _, err := os.Stat(store.ProgressPath()); err != nil {
		t.Fatalf("progress.json was not written: %v", err)
	}

	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !found {
		t.Fatal("Restore did not find the checkpoint")
	}
	if restored.ID != begun.ID {
		t.Fatalf("restored id = %q, want %q", restored.ID, begun.ID)
	}
	if restored.Status != StatusPaused {
		t.Fatalf("restored status = %s, want PAUSED", restored.Status)
	}

	resumed, err := reopened.Begin("continue")
	if err != nil {
		t.Fatalf("Begin resume: %v", err)
	}
	if resumed.ID != begun.ID {
		t.Fatalf("resume created a new task: %q -> %q", begun.ID, resumed.ID)
	}
	if resumed.Status != StatusRunning {
		t.Fatalf("resumed status = %s, want RUNNING", resumed.Status)
	}
}

// A clean turn end while todos remain is progress, not completion. Marking it
// COMPLETED would assert more than the runtime observed.
func TestControllerCompletionRequiresAllWorkDone(t *testing.T) {
	todos := []Todo{
		{Label: "write code", Status: TodoTodo},
		{Label: "validate", Status: TodoTodo},
	}
	controller := NewController(NewStore(t.TempDir()), func() []Todo { return todos })
	if _, err := controller.Begin("objective"); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	partial, err := controller.Complete()
	if err != nil {
		t.Fatalf("Complete with open todos: %v", err)
	}
	if partial.Status != StatusRunning {
		t.Fatalf("status = %s, want RUNNING while work remains", partial.Status)
	}

	for i := range todos {
		todos[i].Status = TodoDone
		todos[i].Evidence = "observed"
	}
	done, err := controller.Complete()
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if done.Status != StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", done.Status)
	}
}

// A checkpoint left RUNNING is evidence the process stopped, not that the task
// finished. Recovery must pause it and never claim success.
func TestControllerRecoversCrashedTaskAsPaused(t *testing.T) {
	store := NewStore(t.TempDir())
	crashed := Task{
		ID:        "crashed-task",
		Objective: "finish the work",
		Status:    StatusRunning,
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.Save(crashed); err != nil {
		t.Fatalf("Save: %v", err)
	}

	controller := NewController(store, nil)
	restored, found, err := controller.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !found {
		t.Fatal("crash checkpoint not found")
	}
	if restored.Status != StatusPaused {
		t.Fatalf("status = %s, want PAUSED after a crash", restored.Status)
	}
	if restored.Status == StatusCompleted {
		t.Fatal("a crashed task was reported as completed")
	}
}

// The checkpoint records the last meaningful action, which is what orients the
// model after a resume.
func TestControllerCheckpointRecordsLastAction(t *testing.T) {
	store := NewStore(t.TempDir())
	controller := NewController(store, nil)
	if _, err := controller.Begin("objective"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted by user"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	progress, found, err := store.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress: %v", err)
	}
	if !found {
		t.Fatal("no checkpoint was written")
	}
	if progress.LastAction != "interrupted by user" {
		t.Fatalf("last action = %q, want the interruption reason", progress.LastAction)
	}
	if progress.Status != StatusPaused {
		t.Fatalf("checkpoint status = %s, want PAUSED", progress.Status)
	}
}

// Re-syncing the todo owner must keep ids stable by label, or the checkpoint's
// todo references churn on every boundary.
func TestControllerSyncKeepsTodoIDsByLabel(t *testing.T) {
	todos := []Todo{{Label: "a", Status: TodoTodo}, {Label: "b", Status: TodoTodo}}
	controller := NewController(NewStore(t.TempDir()), func() []Todo { return todos })
	if _, err := controller.Begin("objective"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	first := controller.task.Todos[0].ID
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := controller.task.Todos[0].ID; got != first {
		t.Fatalf("todo id churned across sync: %q -> %q", first, got)
	}
}

// Reconstructed context must carry orientation, and must stay bounded: the
// point is semantic continuity, not a transcript dump.
func TestControllerContextCarriesOrientationAndStaysBounded(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin("ship phase 1"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.task.Constraints = []string{"one loop"}
	controller.task.NextAction = "run tests"
	id, err := controller.task.AddTodo("write tests")
	if err != nil {
		t.Fatalf("AddTodo: %v", err)
	}
	if err := controller.task.SetTodoStatus(id, TodoActive, ""); err != nil {
		t.Fatalf("activate: %v", err)
	}
	for i := 0; i < 500; i++ {
		controller.task.AddFinding(strings.Repeat("finding ", 40))
	}

	blocks := controller.Context()
	joined := strings.Join(blocks, "\n")
	for _, want := range []string{"Objective: ship phase 1", "Todo:", "Next action: run tests", "Constraints: one loop"} {
		if !strings.Contains(joined, want) {
			t.Errorf("context is missing %q:\n%s", want, joined)
		}
	}
	if len(joined) > contextBudget {
		t.Fatalf("context = %d chars, want <= %d", len(joined), contextBudget)
	}
}

// Failure and cancellation are distinct outcomes and must not collapse.
func TestControllerFailAndCancelAreDistinct(t *testing.T) {
	failed := NewController(NewStore(t.TempDir()), nil)
	if _, err := failed.Begin("a"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	failedTask, err := failed.Fail("provider exploded")
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if failedTask.Status != StatusFailed {
		t.Fatalf("status = %s, want FAILED", failedTask.Status)
	}

	cancelled := NewController(NewStore(t.TempDir()), nil)
	if _, err := cancelled.Begin("b"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	cancelledTask, err := cancelled.Cancel("user asked")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelledTask.Status != StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED", cancelledTask.Status)
	}
	if cancelledTask.Status == StatusFailed {
		t.Fatal("cancellation was reported as failure")
	}
}

// A terminal task is never resurrected by a routine sync: continuing after
// completion means a new task, not reopening the finished one.
func TestControllerSyncLeavesTerminalTaskAlone(t *testing.T) {
	todos := []Todo{{Label: "done item", Status: TodoDone}}
	controller := NewController(NewStore(t.TempDir()), func() []Todo { return todos })
	if _, err := controller.Begin("objective"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// A late todo arriving after completion must not reopen the task.
	todos = append(todos, Todo{Label: "late todo", Status: TodoTodo})
	synced, err := controller.Sync()
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if synced.Status != StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", synced.Status)
	}
	if len(synced.Todos) != 1 {
		t.Fatalf("terminal task picked up a new todo: %+v", synced.Todos)
	}
}
