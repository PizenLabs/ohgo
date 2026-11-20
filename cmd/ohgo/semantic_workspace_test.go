package main

import (
	"os"
	"path/filepath"
	"testing"
)

// gopls needs the module root, not the project root. A single module stored in
// a subdirectory is the common layout that made workspace/symbol empty.
func TestSemanticWorkspaceRootFindsSingleNestedModule(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backend", "go.mod"), []byte("module example.com/backend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := semanticWorkspaceRoot(root); got != filepath.Join(root, "backend") {
		t.Fatalf("workspace root = %q, want the nested module", got)
	}
}

func TestSemanticWorkspaceRootKeepsAmbiguousRoot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/"+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := semanticWorkspaceRoot(root); got != root {
		t.Fatalf("workspace root = %q, want the project root for an ambiguous multi-module tree", got)
	}
}
