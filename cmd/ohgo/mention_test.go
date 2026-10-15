package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMentionListerMarksFolders pins the roster shape the inline palette
// consumes: folders carry a trailing slash, files do not.
func TestMentionListerMarksFolders(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, path := range mentionLister(root)() {
		got[path] = true
	}
	if !got["sub/"] {
		t.Errorf("roster = %v, want the folder with a trailing slash", got)
	}
	if !got["a.go"] {
		t.Errorf("roster = %v, want the file path", got)
	}
}
