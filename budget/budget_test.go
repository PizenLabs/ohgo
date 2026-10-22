package budget

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// An admission at the exact boundary succeeds once; the next one fails and
// consumes nothing. A consumer must never see a limit crossed.
func TestAdmissionIsExactAndMonotonic(t *testing.T) {
	b := New(Limits{ToolCalls: 2})
	if err := b.Admit(DimensionToolCalls); err != nil {
		t.Fatalf("first admission: %v", err)
	}
	if err := b.Admit(DimensionToolCalls); err != nil {
		t.Fatalf("second admission: %v", err)
	}
	err := b.Admit(DimensionToolCalls)
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("third admission = %v, want ErrExhausted", err)
	}
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("error %T does not expose the dimension", err)
	}
	if exhausted.Dimension != DimensionToolCalls || exhausted.Limit != 2 || exhausted.Used != 2 {
		t.Fatalf("exhaustion = %+v, want tool_calls 2/2", exhausted)
	}
	if got := b.Snapshot().Used.ToolCalls; got != 2 {
		t.Fatalf("failed admission changed usage to %d", got)
	}
}

// A multi-dimension admission is all-or-nothing. If the second dimension is
// exhausted, the first must not be charged.
func TestAdmitAllChargesNothingOnFailure(t *testing.T) {
	b := New(Limits{Turns: 2, ModelCalls: 1})
	if err := b.AdmitAll(DimensionTurns, DimensionModelCalls); err != nil {
		t.Fatalf("first admission: %v", err)
	}
	err := b.AdmitAll(DimensionTurns, DimensionModelCalls)
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("second admission = %v, want ErrExhausted", err)
	}
	snap := b.Snapshot()
	if snap.Used.Turns != 1 || snap.Used.ModelCalls != 1 {
		t.Fatalf("failed admission charged %+v, want turns=1 model_calls=1", snap.Used)
	}
}

// Concurrent children racing for the last unit must produce exactly the
// granted number of admissions. A check-then-charge race would exceed it.
func TestConcurrentAdmissionNeverExceedsTheGrant(t *testing.T) {
	const limit = 25
	b := New(Limits{ToolCalls: limit})
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.Admit(DimensionToolCalls); err == nil {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != limit {
		t.Fatalf("admitted = %d, want exactly %d", got, limit)
	}
	if got := b.Snapshot().Used.ToolCalls; got != limit {
		t.Fatalf("used = %d, want %d", got, limit)
	}
}

// A non-positive limit means unmetered, not "zero allowed": the zero value is
// the only safe way for a caller to leave a dimension outside this grant.
func TestNonPositiveLimitIsUnmetered(t *testing.T) {
	b := New(Limits{})
	for i := 0; i < 10; i++ {
		if err := b.Admit(DimensionTurns); err != nil {
			t.Fatalf("admission %d: %v", i, err)
		}
	}
	if got := b.Snapshot().Remaining.Turns; got != -1 {
		t.Fatalf("remaining turns = %d, want -1 for unmetered", got)
	}
}

// A snapshot is a copy: a later admission must not rewrite a value a caller is
// about to persist or report.
func TestSnapshotIsIndependent(t *testing.T) {
	b := New(Limits{Turns: 2})
	if err := b.Admit(DimensionTurns); err != nil {
		t.Fatal(err)
	}
	snap := b.Snapshot()
	if err := b.Admit(DimensionTurns); err != nil {
		t.Fatal(err)
	}
	if snap.Used.Turns != 1 || snap.Remaining.Turns != 1 {
		t.Fatalf("snapshot changed after later admission: %+v", snap)
	}
}

// The holder is the run-boundary seam: replacing it must be visible to every
// reader without changing the previous tree's instance.
func TestHolderReplacesAtRunBoundary(t *testing.T) {
	first := New(Limits{Turns: 1})
	holder := NewHolder(first)
	if holder.Get() != first {
		t.Fatal("holder did not return the installed budget")
	}
	second := New(Limits{Turns: 2})
	holder.Set(second)
	if holder.Get() != second {
		t.Fatal("holder did not replace the budget")
	}
	if first.Snapshot().Limits.Turns != 1 {
		t.Fatal("replacing the holder mutated the previous budget")
	}
}

// State is the durable projection; it must round-trip both limits and usage so
// a resume cannot silently start from zero.
func TestStateCarriesLimitsAndUsage(t *testing.T) {
	b := New(Limits{Turns: 3, ToolCalls: 7, Deadline: time.Unix(100, 0)})
	if err := b.Admit(DimensionTurns); err != nil {
		t.Fatal(err)
	}
	state := b.State()
	if state.Limits.Turns != 3 || state.Limits.ToolCalls != 7 || !state.Limits.Deadline.Equal(time.Unix(100, 0)) {
		t.Fatalf("state lost limits: %+v", state.Limits)
	}
	if state.Used.Turns != 1 {
		t.Fatalf("state lost usage: %+v", state.Used)
	}
}
