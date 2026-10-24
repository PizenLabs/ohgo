package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/recovery"
)

// isolateRecoveryState points the machine-level state directory at a temp home.
func isolateRecoveryState(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestRecoveryServiceRoundTrip(t *testing.T) {
	isolateRecoveryState(t)
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	service := openRecoveryService(root)
	defer func() { _ = service.Close() }()
	if err := service.Err(); err != nil {
		t.Fatalf("open: %v", err)
	}

	journal := service.Journal(1)
	txn, err := journal.BeginEdit(context.Background(), agent.EditBoundary{Tools: []string{"write"}})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := os.WriteFile(path, []byte("package a\n\nfunc X() {}\n"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if err := txn.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	undo := service.Undo(context.Background(), 1)
	if !undo.Verified() {
		t.Fatalf("undo = %s (%s)", undo.State, undo.Reason)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "package a\n" {
		t.Errorf("content after undo = %q", data)
	}

	redo := service.Redo(context.Background(), 1)
	if !redo.Verified() {
		t.Fatalf("redo = %s (%s)", redo.State, redo.Reason)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "package a\n\nfunc X() {}\n" {
		t.Errorf("content after redo = %q", data)
	}
}

func TestRecoveryServiceRefusesMutationWhenStoreIsUnavailable(t *testing.T) {
	// A regular file where the state directory should be makes ~/.ohgo
	// impossible to create, so the store cannot open.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".ohgo"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	t.Setenv("HOME", home)

	service := openRecoveryService(t.TempDir())
	defer func() { _ = service.Close() }()
	if service.Err() == nil {
		t.Fatal("the service reported a working store it could not open")
	}

	journal := service.Journal(1)
	if _, err := journal.BeginEdit(context.Background(), agent.EditBoundary{}); err == nil {
		t.Fatal("a mutating batch was admitted without a recovery point")
	}
	result := service.Undo(context.Background(), 1)
	if result.State != recovery.StateFailed {
		t.Errorf("undo state = %s, want FAILED", result.State)
	}
}

// TestNoRecoveryToolIsRegistered proves the model has no path to recovery
// authority: the built-in tool set contains no undo, redo, or checkpoint tool.
func TestNoRecoveryToolIsRegistered(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	toolSet := buildTools(testOptions(t, t.TempDir(), 1), ai.NewRegistry())
	for _, tool := range toolSet {
		switch tool.Name() {
		case "undo", "redo", "recovery", "checkpoint", "restore":
			t.Errorf("recovery authority is exposed as tool %q", tool.Name())
		}
	}
}
