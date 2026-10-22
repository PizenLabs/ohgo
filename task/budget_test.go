package task

import (
	"testing"

	"github.com/PizenLabs/ohgo/budget"
)

// A run's consumption is folded into the task ledger and survives a restart.
// Resuming must never present the previous run as free.
func TestBudgetLedgerSurvivesPauseAndResume(t *testing.T) {
	store := NewStore(t.TempDir())
	controller := NewController(store, nil)
	controller.SetBudgetLimits(budget.Limits{Turns: 10, ToolCalls: 20})
	if _, err := controller.Begin(testIntent("budgeted task"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	grant := controller.RunBudget()
	if err := grant.Admit(budget.DimensionTurns); err != nil {
		t.Fatal(err)
	}
	if err := grant.Admit(budget.DimensionToolCalls); err != nil {
		t.Fatal(err)
	}
	controller.RecordBudget(grant.Snapshot())
	if _, err := controller.Pause("interrupted"); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	progress, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress = %v, found=%v", err, found)
	}
	if progress.Budget == nil || progress.Budget.Used.Turns != 1 || progress.Budget.Used.ToolCalls != 1 {
		t.Fatalf("checkpoint budget = %+v, want one turn and one tool", progress.Budget)
	}

	reopened := NewController(store, nil)
	reopened.SetBudgetLimits(budget.Limits{Turns: 20, ToolCalls: 40})
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore = %v, found=%v", err, found)
	}
	if restored.Status != StatusPaused {
		t.Fatalf("restored status = %s, want PAUSED", restored.Status)
	}
	state, ok := reopened.BudgetState()
	if !ok || state.Used.Turns != 1 || state.Used.ToolCalls != 1 {
		t.Fatalf("resume reset the ledger: %+v (found=%v)", state, ok)
	}
	if state.Limits.Turns != 20 {
		t.Fatalf("resume limits = %d, want the explicit larger grant 20", state.Limits.Turns)
	}

	if _, err := reopened.Begin(testIntent("budgeted task"), nil); err != nil {
		t.Fatalf("Begin resume: %v", err)
	}
	state, _ = reopened.BudgetState()
	if state.Used.Turns != 1 {
		t.Fatalf("Begin reset consumed turns to %d", state.Used.Turns)
	}
}

// A new task has a new ledger. Carrying a completed task's consumption into an
// unrelated objective would make the next task pay for the previous one.
func TestNewTaskStartsWithAZeroLedger(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetBudgetLimits(budget.Limits{Turns: 5})
	if _, err := controller.Begin(testIntent("first"), nil); err != nil {
		t.Fatal(err)
	}
	grant := controller.RunBudget()
	if err := grant.Admit(budget.DimensionTurns); err != nil {
		t.Fatal(err)
	}
	controller.RecordBudget(grant.Snapshot())
	if _, err := controller.Complete(); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Begin(testIntent("second"), nil); err != nil {
		t.Fatal(err)
	}
	state, ok := controller.BudgetState()
	if !ok {
		t.Fatal("the new task has no budget ledger")
	}
	if state.Used.Turns != 0 {
		t.Fatalf("new task inherited %d consumed turns", state.Used.Turns)
	}
}

// RunBudget is a fresh enforcement instance, not the ledger. Admitting against
// it must not mutate the durable cumulative total until RecordBudget runs.
func TestRunBudgetIsAFreshGrant(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetBudgetLimits(budget.Limits{Turns: 2})
	if _, err := controller.Begin(testIntent("task"), nil); err != nil {
		t.Fatal(err)
	}
	first := controller.RunBudget()
	if err := first.Admit(budget.DimensionTurns); err != nil {
		t.Fatal(err)
	}
	state, _ := controller.BudgetState()
	if state.Used.Turns != 0 {
		t.Fatalf("unrecorded run mutated the ledger: %d", state.Used.Turns)
	}
	second := controller.RunBudget()
	if err := second.Admit(budget.DimensionTurns); err != nil {
		t.Fatalf("a new run must receive its own grant: %v", err)
	}
	controller.RecordBudget(first.Snapshot())
	controller.RecordBudget(second.Snapshot())
	state, _ = controller.BudgetState()
	if state.Used.Turns != 2 {
		t.Fatalf("recorded turns = %d, want 2", state.Used.Turns)
	}
}
