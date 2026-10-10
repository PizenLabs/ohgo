package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
)

// writeTree materialises a path→content map under root, creating parents.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// globPaths runs a glob and returns the matched paths.
//
// Notice and blank lines are filtered out so a caller asserts on matches
// only, regardless of what the tool appended.
func globPaths(t *testing.T, tool *GlobTool, args map[string]any) []string {
	t.Helper()
	result := tool.Execute(testContext(), args)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if strings.HasPrefix(result.Text, "No files matched") {
		return nil
	}
	var out []string
	for _, line := range strings.Split(result.Text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func TestGlobFindsFilesByPatternAtAnyDepth(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go":              "package a",
		"sub/b.go":          "package b",
		"sub/deep/c.go":     "package c",
		"notes.md":          "text",
		"node_modules/x.go": "ignored",
	})

	tool := NewGlobTool(root)
	got := globPaths(t, tool, map[string]any{"pattern": "*.go"})
	want := []string{"a.go", "sub/b.go", "sub/deep/c.go"}
	if len(got) != len(want) {
		t.Fatalf("matches = %v, want %v", got, want)
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	// node_modules is excluded by the built-in ignore, so a bare *.go pattern
	// cannot be drowned out by vendored sources.
	if contains(got, "node_modules/x.go") {
		t.Error("node_modules was not pruned")
	}
}

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func TestGlobDoubleStarMatchesAcrossDirectories(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/b/c/deep.go": "x",
		"top.go":        "y",
	})

	got := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "**/*.go"})
	if len(got) != 2 {
		t.Errorf("matches = %v, want both files", got)
	}
}

func TestGlobLimitsResults(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for i := 0; i < 10; i++ {
		files[fmt.Sprintf("f%02d.go", i)] = "package x"
	}
	writeTree(t, root, files)

	got := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "*.go", "limit": 3.0})
	if len(got) > 3 {
		t.Errorf("returned %d results, want at most 3", len(got))
	}
}

func TestGlobHonoursGitignore(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":     "build/\n*.log\n",
		"build/out.go":   "generated",
		"keep.go":        "source",
		"app.log":        "noise",
		"keep/nested.go": "source",
	})

	got := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "**/*.go"})
	if contains(got, "build/out.go") {
		t.Error("an ignored directory was not pruned")
	}
	if !contains(got, "keep.go") {
		t.Errorf("a source file was excluded: %v", got)
	}

	// Turning gitignore off must include the excluded tree.
	all := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "**/*.go", "gitignore": false})
	if !contains(all, "build/out.go") {
		t.Errorf("gitignore=false did not include ignored files: %v", all)
	}
}

func TestGlobHonoursNegatedGitignoreRule(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore": "*.go\n!keep.go\n",
		"drop.go":    "x",
		"keep.go":    "y",
	})

	got := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "*.go"})
	if contains(got, "drop.go") {
		t.Error("the excluded file was returned")
	}
	if !contains(got, "keep.go") {
		t.Errorf("a negated rule was not honoured: %v", got)
	}
}

func TestGlobExcludesHiddenByDefault(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"visible.go":   "x",
		".hidden.go":   "y",
		".config/z.go": "z",
	})

	got := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "*.go", "hidden": false})
	if contains(got, ".hidden.go") || contains(got, ".config/z.go") {
		t.Errorf("hidden files were included: %v", got)
	}
	withHidden := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "*.go", "hidden": true})
	if !contains(withHidden, ".hidden.go") {
		t.Errorf("hidden=true did not include dotfiles: %v", withHidden)
	}
}

func TestGlobRejectsInvalidInput(t *testing.T) {
	root := t.TempDir()
	tool := NewGlobTool(root)

	if result := tool.Execute(testContext(), map[string]any{}); !result.IsError {
		t.Error("expected a missing pattern to fail")
	}
	if result := tool.Execute(testContext(), map[string]any{"pattern": "["}); !result.IsError {
		t.Error("expected an invalid pattern to fail")
	}
	if result := tool.Execute(testContext(), map[string]any{"pattern": "*.go", "path": "../../etc"}); !result.IsError {
		t.Error("expected a path outside the root to fail")
	}
}

func TestGlobReportsNoMatches(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "x"})
	result := NewGlobTool(root).Execute(testContext(), map[string]any{"pattern": "*.rs"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !result.Useless {
		t.Error("an empty result should be marked useless so compaction can elide it")
	}
}

func TestGlobHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewGlobTool(root).Execute(agent.Context{Ctx: ctx}, map[string]any{"pattern": "*.go"})
	if !result.IsError {
		t.Fatal("expected a cancelled context to fail")
	}
}

// fileNameFor renders a deterministic zero-padded file name.
func fileNameFor(i int) string {
	return fmt.Sprintf("f%02d.go", i)
}
