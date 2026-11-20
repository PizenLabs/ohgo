package main

import (
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A headless run's measured usage is persisted once per run, not once per
// cumulative process log. Repeating a run adds its own tokens; an unmeasured
// run leaves the ledger untouched.
func TestRecordRunUsagePersistsMeasuredTurns(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create("/work", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	turns := []agent.Turn{
		{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 100, Output: 40, CacheRead: 10, Total: 140}}},
		{Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: 20, Output: 10, Total: 30}}},
		{Assistant: ai.AssistantMessage{}},
	}
	recordRunUsage(store, &sess, turns)
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Usage.Known() || got.Usage.Turns != 2 || got.Usage.Input != 120 ||
		got.Usage.Output != 50 || got.Usage.CacheRead != 10 {
		t.Fatalf("usage = %+v, want two measured turns", got.Usage)
	}
	if sess.Usage != got.Usage {
		t.Fatalf("in-memory usage = %+v, durable = %+v", sess.Usage, got.Usage)
	}

	// A second run adds its own tokens. Passing the cumulative process log
	// here would double-count the first run; the contract is run-scoped turns.
	recordRunUsage(store, &sess, turns)
	got, err = store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage.Turns != 4 || got.Usage.Input != 240 || got.Usage.Output != 100 {
		t.Fatalf("usage after second run = %+v, want additive run totals", got.Usage)
	}
}

// Missing provider usage is unknown, not a measured zero.
func TestRecordRunUsageLeavesUnmeasuredSessionUnknown(t *testing.T) {
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.Create("/work", "provider/model")
	if err != nil {
		t.Fatal(err)
	}
	recordRunUsage(store, &sess, []agent.Turn{{Assistant: ai.AssistantMessage{}}})
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage.Known() {
		t.Fatalf("unmeasured run became %+v", got.Usage)
	}
}
