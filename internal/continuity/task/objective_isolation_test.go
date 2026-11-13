package task

import (
	"errors"
	"testing"
)

// A completed or failed objective is archived before the next request starts a
// new task. The previous objective must never become the current one, and it
// must stay recoverable.
func TestCompletedObjectiveCannotBecomeTheCurrentObjective(t *testing.T) {
	store := NewStore(t.TempDir())
	controller := NewController(store, nil)

	previous, err := controller.Begin(testIntent("run this project help me"), nil)
	if err != nil {
		t.Fatalf("Begin(previous): %v", err)
	}
	if _, err := controller.Fail("the provider rejected the request"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	// This is the host's turn-boundary sequence: a terminal task is detached
	// (archived) before the new objective starts.
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}

	current, err := controller.Begin(testIntent("How to convert this project to Next.js?"), nil)
	if err != nil {
		t.Fatalf("Begin(current): %v", err)
	}
	if current.ID == previous.ID {
		t.Fatal("the new objective reused the terminal task identity")
	}
	if current.Objective != "How to convert this project to Next.js?" {
		t.Fatalf("current objective = %q, want the new request", current.Objective)
	}
	if current.Contract == nil || current.Contract.Objective != current.Objective {
		t.Fatalf("current contract = %+v, want the new objective", current.Contract)
	}

	archived, err := controller.ArchivedTasks()
	if err != nil {
		t.Fatalf("ArchivedTasks: %v", err)
	}
	found := false
	for _, record := range archived {
		if record.Objective == previous.Objective {
			found = true
		}
	}
	if !found {
		t.Fatalf("archived objectives = %+v, want the previous objective preserved", archived)
	}

	// A fresh controller over the same store is the recovery path. The next
	// request must still own the current objective, and the archived one must
	// stay archived.
	recovered := NewController(store, nil)
	restored, ok, err := recovered.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !ok || restored.Objective != current.Objective {
		t.Fatalf("restored objective = %q (found=%v), want %q", restored.Objective, ok, current.Objective)
	}
}

// An open task cannot be silently rebound to a different objective. The human
// must cancel, detach, or continue it explicitly.
func TestRecoveredOpenTaskIsNotReboundToANewObjective(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	previous, err := controller.Begin(testIntent("How to convert this project to Next.js?"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// Recovery loads the open task as if the process had just started.
	recovered := NewController(NewStore(root), nil)
	if _, ok, err := recovered.Restore(); err != nil || !ok {
		t.Fatalf("Restore = found=%v err=%v", ok, err)
	}
	if _, err := recovered.Begin(testIntent("run this project help me"), nil); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("Begin(new objective) = %v, want ErrObjectiveConflict", err)
	}
	current, ok := recovered.Current()
	if !ok || current.Objective != previous.Objective {
		t.Fatalf("current objective = %q, want the persisted %q", current.Objective, previous.Objective)
	}

	// The explicit replacement path leaves the old objective archived and
	// starts the new one.
	if _, _, err := recovered.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	next, err := recovered.Begin(testIntent("run this project help me"), nil)
	if err != nil {
		t.Fatalf("Begin after detach: %v", err)
	}
	if next.Objective != "run this project help me" {
		t.Fatalf("objective = %q, want the replacement request", next.Objective)
	}

	// Recovery again: the replacement is current, the detached task is not.
	final := NewController(NewStore(root), nil)
	restored, ok, err := final.Restore()
	if err != nil || !ok {
		t.Fatalf("Restore = found=%v err=%v", ok, err)
	}
	if restored.Objective != next.Objective {
		t.Fatalf("recovered objective = %q, want %q", restored.Objective, next.Objective)
	}
}
