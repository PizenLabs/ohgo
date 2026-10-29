package tools

import (
	"context"
	"strings"
	"testing"
)

// TestWorkspaceMentionsPrunesAndSorts pins the roster contract: the walk
// offers directories and files, honours the built-in, hidden, and .gitignore
// pruning the search tools use, and returns a deterministic sorted list.
func TestWorkspaceMentionsPrunesAndSorts(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main.go":             "package main",
		"b.go":                "package main",
		"pkg/util.go":         "package pkg",
		"node_modules/dep.go": "ignored",
		".hidden.go":          "hidden",
		"skipped.log":         "ignored by rule",
		".gitignore":          "*.log\n",
	})

	got := WorkspaceMentions(context.Background(), root, 0)
	var paths []string
	dirs := map[string]bool{}
	for _, p := range got {
		paths = append(paths, p.Path)
		if p.Dir {
			dirs[p.Path] = true
		}
	}
	want := []string{"b.go", "main.go", "pkg", "pkg/util.go"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
	if !dirs["pkg"] {
		t.Error("the directory entry was not marked as a directory")
	}
	if dirs["main.go"] {
		t.Error("a file was marked as a directory")
	}
}

// TestWorkspaceMentionsRespectsLimit proves the roster is bounded: a large
// tree returns at most the requested number of entries.
func TestWorkspaceMentionsRespectsLimit(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go": "a",
		"b.go": "b",
		"c.go": "c",
	})
	got := WorkspaceMentions(context.Background(), root, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want the requested cap of 2", len(got))
	}
}

// TestReadWorkspaceFileReadsInsideRoot proves a referenced file is returned
// verbatim while a traversal outside the workspace is refused.
func TestReadWorkspaceFileReadsInsideRoot(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"pkg/util.go": "package pkg"})

	got, err := ReadWorkspaceFile(root, "pkg/util.go")
	if err != nil {
		t.Fatalf("ReadWorkspaceFile: %v", err)
	}
	if got != "package pkg" {
		t.Fatalf("content = %q, want the file's bytes", got)
	}
	if _, err := ReadWorkspaceFile(root, "../escape.go"); err == nil {
		t.Fatal("a traversal outside the workspace was read, want an error")
	}
}

// TestReadWorkspaceFileRefusesDirectoryAndOversize proves a reference that has
// no file content to attach fails explicitly instead of returning something the
// model would misread.
func TestReadWorkspaceFileRefusesDirectoryAndOversize(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"pkg/util.go": "package pkg",
		"big.txt":     strings.Repeat("x", MentionFileMaxBytes+1),
	})
	if _, err := ReadWorkspaceFile(root, "pkg"); err == nil {
		t.Fatal("reading a directory succeeded, want an error")
	}
	if _, err := ReadWorkspaceFile(root, "big.txt"); err == nil {
		t.Fatal("an oversized file was read, want an error")
	}
}
