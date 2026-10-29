package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// An explicit deletion is an authorized mutation: it removes exactly the
// requested file and reports the change as evidence.
func TestDeleteToolRemovesTheFileAndReportsMutation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "obsolete.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewDeleteTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path": "obsolete.txt",
	})
	if result.IsError {
		t.Fatalf("delete failed: %s", result.Text)
	}
	if !result.Mutates {
		t.Fatal("a successful deletion was not reported as mutation evidence")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the file still exists: %v", err)
	}
}

// A missing target is an observable failure, not a silent success.
func TestDeleteToolReportsAMissingFile(t *testing.T) {
	root := t.TempDir()
	result := NewDeleteTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path": "missing.txt",
	})
	if !result.IsError {
		t.Fatal("deleting a missing file was reported as success")
	}
	if result.Mutates {
		t.Fatal("a failed deletion claimed to have mutated the workspace")
	}
}

// Containment is enforced by the same resolver as the other file tools.
func TestDeleteToolRefusesPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := NewDeleteTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path": outside,
	})
	if !result.IsError {
		t.Fatal("a delete outside the workspace was accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the outside file was removed: %v", err)
	}
}
