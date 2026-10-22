package task

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A checkpoint must round-trip through disk without losing the task it
// describes, or a resume would continue something else.
func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	now := time.Now().UTC().Truncate(time.Second)
	task := Task{
		ID:          "task-1",
		Objective:   "ship phase 1",
		Constraints: []string{"no new deps"},
		Phase:       "act",
		Todos: []Todo{
			{ID: "t1", Label: "write code", Status: TodoDone, Evidence: "tests passed"},
			{ID: "t2", Label: "validate", Status: TodoActive},
		},
		ActiveStep: "validate",
		Findings:   []string{"checkpoint owner is the store"},
		NextAction: "run go test",
		Status:     StatusPaused,
		UpdatedAt:  now,
	}
	if err := store.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !snap.Found {
		t.Fatal("Load reported no artifacts after Save")
	}
	if snap.Task.ID != task.ID || snap.Task.Status != task.Status {
		t.Fatalf("loaded %s/%s, want %s/%s", snap.Task.ID, snap.Task.Status, task.ID, task.Status)
	}
	if snap.Task.ActiveStep != "validate" || snap.Task.NextAction != "run go test" {
		t.Fatalf("orientation fields lost: %+v", snap.Task)
	}
	if len(snap.Task.Todos) != 2 || snap.Task.Todos[0].Evidence != "tests passed" {
		t.Fatalf("todos lost through the projection: %+v", snap.Task.Todos)
	}
	if snap.Progress.LastAction != "" {
		t.Fatalf("plain Save recorded a last action %q", snap.Progress.LastAction)
	}
}

// Writing the same state twice must be safe: a checkpoint can be taken at any
// boundary, including one that produced no change.
func TestRepeatedCheckpointIsIdempotent(t *testing.T) {
	store := NewStore(t.TempDir())
	task, err := New("objective")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.Save(task); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := store.Save(task); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Task.Status != task.Status || snap.Task.ID != task.ID {
		t.Fatalf("repeated checkpoint changed state: %+v", snap.Task)
	}
}

// A stale writer must never roll recovery backwards: a resume racing a newer
// checkpoint would silently lose progress.
func TestStaleCheckpointIsRefused(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	current := Task{ID: "task-1", Objective: "current", Status: StatusRunning, UpdatedAt: now}
	if err := store.Save(current); err != nil {
		t.Fatalf("Save current: %v", err)
	}

	stale := Task{ID: "task-1", Objective: "stale", Status: StatusRunning, UpdatedAt: now.Add(-time.Hour)}
	if err := store.Save(stale); !errors.Is(err, ErrStaleCheckpoint) {
		t.Fatalf("Save stale = %v, want ErrStaleCheckpoint", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Task.Objective != "current" {
		t.Fatalf("stale write won: objective = %q", snap.Task.Objective)
	}
}

// A corrupt checkpoint is reported, but a readable projection still recovers
// the task: refusing to continue over a damaged JSON file would throw away
// work the human artifact still describes.
func TestCorruptCheckpointStillRecoversProjection(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	task, err := New("recover me")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(store.ProgressPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("corrupt progress: %v", err)
	}

	snap, err := store.Load()
	if !errors.Is(err, ErrCorruptCheckpoint) {
		t.Fatalf("Load = %v, want ErrCorruptCheckpoint", err)
	}
	if snap.Task.Objective != "recover me" {
		t.Fatalf("projection not recovered: %+v", snap.Task)
	}
}

// A checkpoint missing its identity cannot be trusted: silently treating it as
// a new task would fork the task's identity.
func TestIncompleteCheckpointIsCorrupt(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if err := os.MkdirAll(filepath.Dir(store.ProgressPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `{"version":1,"status":"PAUSED","checkpointAt":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(store.ProgressPath(), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadProgress(); !errors.Is(err, ErrCorruptCheckpoint) {
		t.Fatalf("LoadProgress = %v, want ErrCorruptCheckpoint", err)
	}
}

// An unknown future schema is refused rather than guessed at.
func TestFutureCheckpointVersionIsRefused(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if err := os.MkdirAll(filepath.Dir(store.ProgressPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `{"version":99,"taskId":"t","status":"PAUSED"}`
	if err := os.WriteFile(store.ProgressPath(), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadProgress(); !errors.Is(err, ErrCheckpointVersion) {
		t.Fatalf("LoadProgress = %v, want ErrCheckpointVersion", err)
	}
}

// A version-1 checkpoint is still readable, but it carries no completion
// contract. Loading must not invent one or reject the recoverable task.
func TestVersion1CheckpointLoadsWithoutContract(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(store.ProgressPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `{
  "version": 1,
  "taskId": "legacy",
  "status": "NEEDS_REVIEW",
  "checkpointAt": "2026-01-01T00:00:00Z"
}`
	if err := os.WriteFile(store.ProgressPath(), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	progress, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress = %v, found=%v", err, found)
	}
	if progress.Contract != nil {
		t.Fatal("a version-1 checkpoint acquired a contract")
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Task.ID != "legacy" || snap.Task.Status != StatusNeedsReview {
		t.Fatalf("recovered task = %+v", snap.Task)
	}
	if snap.Task.Contract != nil {
		t.Fatal("Load invented a completion contract")
	}
}

// A process that died mid-run leaves RUNNING on disk. Recovery must report it
// as recoverable (PAUSED), never as completed.
func TestNormalizeRecovered(t *testing.T) {
	for _, tc := range []struct {
		in   Status
		want Status
	}{
		{StatusRunning, StatusPaused},
		{StatusPending, StatusPaused},
		{StatusPaused, StatusPaused},
		{StatusCompleted, StatusCompleted},
		{StatusFailed, StatusFailed},
		{StatusCancelled, StatusCancelled},
	} {
		got := NormalizeRecovered(Task{Status: tc.in}).Status
		if got != tc.want {
			t.Errorf("NormalizeRecovered(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
