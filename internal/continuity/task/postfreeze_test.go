package task

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func postfreezeIntent(objective string) contract.IntentContract {
	return contract.IntentContract{Objective: objective, Kind: contract.ObjectiveInformational}
}

// Detaching a resumable task preserves it and clears the live slot: the next
// Begin starts cleanly instead of being blocked.
func TestDetachPreservesResumableAndClearsLive(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	begun, err := controller.Begin(postfreezeIntent("check this project and write README.md"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	detached, ok, err := controller.Detach()
	if err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if !ok {
		t.Fatal("Detach did not report a preserved task")
	}
	if detached.ID != begun.ID || detached.Objective != begun.Objective {
		t.Fatalf("detached = %+v, want %+v", detached, begun)
	}
	if _, found := controller.Current(); found {
		t.Fatal("Detach left an active task bound to the session")
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 1 || resumable[0].ID != begun.ID {
		t.Fatalf("resumable = %+v, want the detached task", resumable)
	}
	replaced, err := controller.Begin(postfreezeIntent("migrating a project to Next.js"), nil)
	if err != nil {
		t.Fatalf("Begin new intent: %v", err)
	}
	if replaced.ID == begun.ID {
		t.Fatal("a new intent reused the detached task identity")
	}
}

// Detach never deletes: the archived copy survives a restart.
func TestDetachedTaskSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	controller := NewController(NewStore(root), nil)
	begun, err := controller.Begin(postfreezeIntent("write README.md"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	reopened := NewController(NewStore(root), nil)
	resumable, err := reopened.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 1 || resumable[0].Objective != begun.Objective {
		t.Fatalf("resumable after restart = %+v, want the detached task", resumable)
	}
}

// Resuming restores durable state and attaches it to the session.
func TestResumeRestoresDurableState(t *testing.T) {
	root := t.TempDir()
	controller := NewController(NewStore(root), nil)
	begun, err := controller.Begin(postfreezeIntent("write README.md"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.NeedsReview("change was not verified"); err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	resumed, err := controller.ResumeTask("")
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if resumed.ID != begun.ID {
		t.Fatalf("resumed id = %q, want %q", resumed.ID, begun.ID)
	}
	if resumed.Status != StatusRunning {
		t.Fatalf("resumed status = %s, want RUNNING", resumed.Status)
	}
	current, ok := controller.Current()
	if !ok || current.ID != begun.ID {
		t.Fatalf("current = %+v found=%v, want the resumed task", current, ok)
	}
}

// Cancelling is terminal and stops being resumable.
func TestCancelRemovesResumability(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	begun, err := controller.Begin(postfreezeIntent("write README.md"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	closed, err := controller.CancelResumable("", "abandoned by user")
	if err != nil {
		t.Fatalf("CancelResumable: %v", err)
	}
	if closed.ID != begun.ID {
		t.Fatalf("cancelled id = %q, want %q", closed.ID, begun.ID)
	}
	if closed.Status != StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED", closed.Status)
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	for _, r := range resumable {
		if r.ID == begun.ID {
			t.Fatal("a cancelled task is still listed as resumable")
		}
	}
}

// Multiple detached tasks list deterministically and resume picks the latest.
func TestMultipleResumableListDeterministically(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	for _, objective := range []string{"first task", "second task"} {
		if _, err := controller.Begin(postfreezeIntent(objective), nil); err != nil {
			t.Fatalf("Begin %q: %v", objective, err)
		}
		if _, err := controller.Pause("interrupted"); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		if _, _, err := controller.Detach(); err != nil {
			t.Fatalf("Detach: %v", err)
		}
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 2 {
		t.Fatalf("resumable = %d, want 2", len(resumable))
	}
	// Order is deterministic by stamp then ID; the second task was detached
	// later and must resume by default.
	latest, err := controller.ResumeTask("")
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if latest.Objective != "second task" {
		t.Fatalf("latest = %q, want the second task", latest.Objective)
	}
}

// A terminal task is never resumable and detach does not archive it.
func TestTerminalTaskIsNeverResumable(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	begun, err := controller.Begin(postfreezeIntent("done work"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.Complete(); err != nil {
		// No todos, so Complete should succeed.
		t.Fatalf("Complete: %v", err)
	}
	_ = begun
	if _, ok, err := controller.Detach(); err != nil || ok {
		t.Fatalf("Detach terminal = (%v, %v), want (false, nil)", ok, err)
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 0 {
		t.Fatalf("resumable = %+v, want none for a completed task", resumable)
	}
}

// Detach preserves authority-relevant state: objective, contract, review
// reason, and evidence survive the session boundary.
func TestDetachPreservesTaskState(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	intent := contract.IntentContract{
		Objective: "fix note.txt",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "note.txt"}},
	}
	begun, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.NeedsReview("change was not verified"); err != nil {
		t.Fatalf("NeedsReview: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	resumed, err := controller.ResumeTask(begun.ID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if resumed.Contract == nil || resumed.Contract.Objective != intent.Objective {
		t.Fatalf("contract lost: %+v", resumed.Contract)
	}
	if resumed.ReviewReason != "change was not verified" {
		t.Fatalf("review reason = %q, want it preserved", resumed.ReviewReason)
	}
	if !strings.Contains(resumed.Objective, "fix note.txt") {
		t.Fatalf("objective = %q, want it preserved", resumed.Objective)
	}
}
