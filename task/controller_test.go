package task

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/contract"
)

// testIntent is the smallest complete contract the controller needs; the
// completion rules themselves are tested in the agent package.
func testIntent(objective string) contract.IntentContract {
	return contract.IntentContract{Objective: objective, Kind: contract.ObjectiveInformational}
}

// A paused turn is resumed, not replaced: the next turn continues the same
// task identity.
func TestControllerPauseThenResumeContinuesSameTask(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)

	begun, err := controller.Begin(testIntent("ship phase 1"), nil)
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

	resumed, err := reopened.Begin(testIntent("ship phase 1"), nil)
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
	if _, err := controller.Begin(testIntent("objective"), nil); err != nil {
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

// A turn that ends without completion evidence is reviewable, not complete.
// The state is non-terminal so the next turn can resume the same task and
// supply the missing verification.
func TestControllerNeedsReviewIsResumable(t *testing.T) {
	store := NewStore(t.TempDir())
	controller := NewController(store, nil)
	begun, err := controller.Begin(testIntent("fix the parser"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	review, err := controller.NeedsReview("change was not verified")
	if err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}
	if review.Status != StatusNeedsReview {
		t.Fatalf("status = %s, want NEEDS_REVIEW", review.Status)
	}
	if review.Status.IsTerminal() {
		t.Fatal("NEEDS_REVIEW is terminal; the task could never be resolved")
	}

	// The checkpoint must survive a restart in the reviewable state, not be
	// normalized back to RUNNING or mistaken for a completion.
	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !found {
		t.Fatal("review checkpoint not found")
	}
	if restored.Status != StatusNeedsReview {
		t.Fatalf("restored status = %s, want NEEDS_REVIEW", restored.Status)
	}

	resumed, err := reopened.Begin(testIntent("fix the parser"), nil)
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
	if _, err := controller.Begin(testIntent("objective"), nil); err != nil {
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
	if _, err := controller.Begin(testIntent("objective"), nil); err != nil {
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

// The completion contract, pre-run target evidence, and review reason survive a
// restart. Without them a resumed task has no boundary to be judged against.
func TestControllerPersistsContractSnapshotAndReviewReason(t *testing.T) {
	store := NewStore(t.TempDir())
	controller := NewController(store, nil)
	intent := contract.IntentContract{
		Objective: "fix note.txt",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "note.txt"}},
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyAny,
		}},
	}
	snapshot := &agent.TargetSnapshot{
		Root: "/tmp/workspace",
		Entries: []agent.TargetSnapshotEntry{{
			Target:   contract.Target{Kind: contract.TargetPath, Value: "note.txt"},
			Resolved: true,
			Files:    []agent.FileState{{Path: "note.txt", Exists: true, Hash: "before"}},
		}},
	}
	if _, err := controller.Begin(intent, snapshot); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.NeedsReview("the change was not verified"); err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}

	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore = %v, found=%v", err, found)
	}
	if restored.Contract == nil || restored.Contract.Objective != intent.Objective {
		t.Fatalf("contract lost: %+v", restored.Contract)
	}
	if restored.Snapshot == nil || len(restored.Snapshot.Entries) != 1 ||
		restored.Snapshot.Entries[0].Files[0].Hash != "before" {
		t.Fatalf("snapshot lost: %+v", restored.Snapshot)
	}
	if restored.ReviewReason != "the change was not verified" {
		t.Fatalf("review reason = %q", restored.ReviewReason)
	}
	if _, err := reopened.Begin(intent, nil); err != nil {
		t.Fatalf("resume with the persisted contract: %v", err)
	}
}

// A continuation cannot silently acquire a different objective. The human must
// cancel or replace the task first.
func TestControllerRefusesDifferentContractOnResume(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	first := testIntent("fix the parser")
	if _, err := controller.Begin(first, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.NeedsReview("not verified"); err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}

	second := testIntent("write a different feature")
	if _, err := controller.Begin(second, nil); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("Begin different contract = %v, want ErrObjectiveConflict", err)
	}
	current, ok := controller.Current()
	if !ok || current.Status != StatusNeedsReview {
		t.Fatalf("conflict changed task state: %+v (found=%v)", current, ok)
	}
	if current.Contract == nil || current.Contract.Objective != first.Objective {
		t.Fatalf("conflict replaced the contract: %+v", current.Contract)
	}
}

// A version-1 review checkpoint has no contract. It must not be silently bound
// to the next prompt, and completion must remain impossible until replacement.
func TestControllerRefusesLegacyReviewWithoutContract(t *testing.T) {
	store := NewStore(t.TempDir())
	legacy := Task{
		ID:        "legacy",
		Objective: "fix the parser",
		Status:    StatusNeedsReview,
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.Save(legacy); err != nil {
		t.Fatalf("Save: %v", err)
	}
	controller := NewController(store, nil)
	restored, found, err := controller.Restore()
	if err != nil || !found {
		t.Fatalf("Restore = %v, found=%v", err, found)
	}
	if restored.Contract != nil {
		t.Fatal("a legacy task acquired a contract on restore")
	}
	if restored.ReviewReason == "" {
		t.Fatal("a legacy review task has no reported reason")
	}
	if _, err := controller.Begin(testIntent("continue"), nil); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("Begin legacy = %v, want ErrObjectiveConflict", err)
	}
}

// The explicit human route: cancel the review, then start a new task with a
// new identity and its own contract. The old objective is never marked done.
func TestControllerReviewCanBeCancelledAndReplaced(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	first := testIntent("fix the parser")
	begun, err := controller.Begin(first, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.NeedsReview("not verified"); err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}
	closed, err := controller.Cancel("abandoned by user")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if closed.Status != StatusCancelled {
		t.Fatalf("closed status = %s, want CANCELLED (never COMPLETED)", closed.Status)
	}

	second := testIntent("write a different feature")
	replaced, err := controller.Begin(second, nil)
	if err != nil {
		t.Fatalf("Begin replacement: %v", err)
	}
	if replaced.ID == begun.ID {
		t.Fatal("replacement reused the abandoned task identity")
	}
	if replaced.Status != StatusRunning {
		t.Fatalf("replacement status = %s, want RUNNING", replaced.Status)
	}
	if replaced.Contract == nil || replaced.Contract.Objective != second.Objective {
		t.Fatalf("replacement contract = %+v", replaced.Contract)
	}
}

// Reconstructed context must carry orientation, and must stay bounded: the
// point is semantic continuity, not a transcript dump.
func TestControllerContextCarriesOrientationAndStaysBounded(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("ship phase 1"), nil); err != nil {
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
	if _, err := failed.Begin(testIntent("a"), nil); err != nil {
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
	if _, err := cancelled.Begin(testIntent("b"), nil); err != nil {
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
	if _, err := controller.Begin(testIntent("objective"), nil); err != nil {
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
