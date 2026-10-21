package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
)

// File tools report whether they changed the workspace so completion can
// require verification. The flag is set from the tool's own success, not from
// model text.
func TestFileToolsReportMutationEvidence(t *testing.T) {
	root := t.TempDir()
	write := NewWriteTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path":    "a.txt",
		"content": "one",
	})
	if write.IsError || !write.Mutates {
		t.Fatalf("write evidence = (error %v, mutates %v), want a mutation", write.IsError, write.Mutates)
	}

	edit := NewEditTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path":       "a.txt",
		"old_string": "one",
		"new_string": "two",
	})
	if edit.IsError || !edit.Mutates {
		t.Fatalf("edit evidence = (error %v, mutates %v), want a mutation", edit.IsError, edit.Mutates)
	}

	read := NewReadTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"path": "a.txt",
	})
	if read.IsError || read.Mutates {
		t.Fatalf("read evidence = (error %v, mutates %v), want no mutation", read.IsError, read.Mutates)
	}
	if data, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(data) != "two" {
		t.Fatalf("the edit did not land: %q, %v", data, err)
	}
}
