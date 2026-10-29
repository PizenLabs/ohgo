package budget

import (
	"errors"
	"testing"
)

// External calls are a root-owned dimension: a tool cannot start more external
// work than the tree grant admits, and a refused admission consumes nothing.
func TestExternalCallsAreAdmittedAtomically(t *testing.T) {
	b := New(Limits{ExternalCalls: 1})
	if err := b.Admit(DimensionExternalCalls); err != nil {
		t.Fatalf("first call: %v", err)
	}
	err := b.Admit(DimensionExternalCalls)
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("second call error = %v, want exhaustion", err)
	}
	if got := b.Snapshot().Used.ExternalCalls; got != 1 {
		t.Fatalf("used = %d, want exactly one admitted call", got)
	}
	if got := b.Remaining(DimensionExternalCalls); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
}

// Byte admission is all-or-nothing: a response that would cross the aggregate
// cap is refused without charging the prefix, so concurrent children cannot
// both spend the last bytes.
func TestExternalBytesAdmitNIsAllOrNothing(t *testing.T) {
	b := New(Limits{ExternalBytes: 10})
	if err := b.AdmitN(DimensionExternalBytes, 10); err != nil {
		t.Fatalf("admit 10: %v", err)
	}
	if err := b.AdmitN(DimensionExternalBytes, 1); !errors.Is(err, ErrExhausted) {
		t.Fatalf("admit past cap error = %v, want exhaustion", err)
	}
	if got := b.Snapshot().Used.ExternalBytes; got != 10 {
		t.Fatalf("used = %d, want the refused amount not charged", got)
	}
	if err := b.AdmitN(DimensionExternalBytes, 0); err != nil {
		t.Fatalf("zero admission failed: %v", err)
	}
	if got := b.Remaining(DimensionExternalBytes); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
}

func TestRemainingIsUnmeteredSentinelWhenNoLimit(t *testing.T) {
	b := New(Limits{})
	if got := b.Remaining(DimensionExternalBytes); got != -1 {
		t.Fatalf("remaining = %d, want -1 for an unmetered dimension", got)
	}
}

// A new dimension must participate in the durable state and snapshot, or a
// resumed task would silently regain external capacity.
func TestExternalDimensionsSurviveSnapshotAndState(t *testing.T) {
	b := New(Limits{ExternalCalls: 3, ExternalBytes: 20})
	if err := b.Admit(DimensionExternalCalls); err != nil {
		t.Fatal(err)
	}
	if err := b.AdmitN(DimensionExternalBytes, 7); err != nil {
		t.Fatal(err)
	}
	snap := b.Snapshot()
	if snap.Used.ExternalCalls != 1 || snap.Used.ExternalBytes != 7 {
		t.Fatalf("snapshot used = %+v", snap.Used)
	}
	if snap.Remaining.ExternalCalls != 2 || snap.Remaining.ExternalBytes != 13 {
		t.Fatalf("snapshot remaining = %+v", snap.Remaining)
	}
	state := b.State()
	if state.Limits.ExternalCalls != 3 || state.Used.ExternalBytes != 7 {
		t.Fatalf("state = %+v", state)
	}
}
