package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/continuity/session"
)

// TestCleanCommandPrunesConfiguredRetention exercises the explicit CLI path:
// the protected session survives even though it is the oldest, and --keep
// retains only the newest unprotected conversation.
func TestCleanCommandPrunesConfiguredRetention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := session.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < 4; i++ {
		sess, err := store.Create("/tmp", "m")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
		// Distinct activity timestamps make retention deterministic.
		time.Sleep(2 * time.Millisecond)
	}
	if err := store.SetProtected(ids[0], true); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := cleanCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--keep", "1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if !strings.Contains(out.String(), "pruned 2 session(s)") {
		t.Fatalf("output = %q, want the pruned count", out.String())
	}

	reopened, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	list, err := reopened.List(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("remaining sessions = %d, want the protected one plus the newest", len(list))
	}
	kept := map[int64]bool{}
	for _, sess := range list {
		kept[sess.ID] = true
	}
	if !kept[ids[0]] || !kept[ids[3]] {
		t.Fatalf("kept = %v, want protected %d and newest %d", kept, ids[0], ids[3])
	}
}
