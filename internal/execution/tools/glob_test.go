package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
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

func TestGlobExpandsBraceAlternation(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app/page.tsx":       "x",
		"app/globals.css":    "y",
		"app/util.ts":        "z",
		"components/ui/a.ts": "w",
	})

	// A model writes this because a shell would expand it. Treated as a
	// literal, the pattern matches nothing and the model retries with a second
	// scan instead of reading the tree it already asked for.
	got := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "app/**/*.{ts,tsx,mdx,css}"})
	want := []string{"app/util.ts", "app/page.tsx", "app/globals.css"}
	if len(got) != len(want) {
		t.Fatalf("matches = %v, want %v", got, want)
	}
	for _, path := range want {
		found := false
		for _, match := range got {
			if match == path {
				found = true
			}
		}
		if !found {
			t.Errorf("matches = %v, want %q", got, path)
		}
	}
}

func TestGlobBraceAlternationIsNestedAndBounded(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a/b/x.tsx": "x", "a/y.ts": "y"})

	got := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "a/**/*.{ts{,x},mdx}"})
	if len(got) != 2 {
		t.Fatalf("matches = %v, want both a/y.ts and a/b/x.tsx", got)
	}

	// An unterminated group is not alternation; it must stay a literal rather
	// than expand into an unbounded or invalid pattern.
	if got := globPaths(t, NewGlobTool(root), map[string]any{"pattern": "a/**/*.{ts"}); len(got) != 0 {
		t.Fatalf("unterminated brace matched %v, want no matches", got)
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

// A glob scoped to a path that does not exist is an absence observation, not
// an unrecovered failure: the model asked a bounded question and learned the
// path is not there, which is still usable evidence.
func TestGlobMissingScanPathIsAnAbsenceObservation(t *testing.T) {
	root := t.TempDir()
	result := NewGlobTool(root).Execute(testContext(), map[string]any{
		"pattern": "*.go",
		"path":    "missing",
	})
	if !result.IsError {
		t.Fatal("expected a missing scan path to remain an error for the model")
	}
	if !result.ObservedAbsence {
		t.Error("a missing scan path is an absence observation, not an unrecovered failure")
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

// When every candidate is pruned by .gitignore, the model must be told why and
// how to override it. "No files matched." alone is indistinguishable from an
// empty tree and invites a false conclusion that the files do not exist.
func TestGlobReportsGitignoreExclusionWhenNoMatchesRemain(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":      "backend/\n",
		"backend/main.go": "package main",
	})

	result := NewGlobTool(root).Execute(testContext(), map[string]any{"pattern": "**/*.go"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "excluded by .gitignore") {
		t.Fatalf("text = %q, want the ignore exclusion named", result.Text)
	}
	if !strings.Contains(result.Text, `"gitignore": false`) {
		t.Fatalf("text = %q, want the override argument", result.Text)
	}
	details, _ := result.Details.(map[string]any)
	if got, _ := details["ignored"].(int); got == 0 {
		t.Fatalf("details = %+v, want a non-zero ignored count", result.Details)
	}

	all := NewGlobTool(root).Execute(testContext(), map[string]any{
		"pattern": "**/*.go", "gitignore": false,
	})
	if !strings.Contains(all.Text, "backend/main.go") {
		t.Fatalf("gitignore=false did not include the ignored file: %q", all.Text)
	}
}

// A glob that returns matches can still have pruned a whole subtree through
// .gitignore. "44 matches" without a notice reads as the complete tree and lets
// a model conclude the ignored directory does not exist.
func TestGlobReportsGitignoreExclusionWhenMatchesRemain(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":      "backend/\n",
		"main.go":         "package main",
		"backend/main.go": "package main",
	})

	result := NewGlobTool(root).Execute(testContext(), map[string]any{"pattern": "**/*.go"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "main.go") {
		t.Fatalf("text = %q, want the visible match", result.Text)
	}
	if !strings.Contains(result.Text, "excluded by .gitignore") {
		t.Fatalf("text = %q, want the pruned subtree named", result.Text)
	}
	if !strings.Contains(result.Text, `"gitignore": false`) {
		t.Fatalf("text = %q, want the override argument", result.Text)
	}
	details, _ := result.Details.(map[string]any)
	if got, _ := details["ignored"].(int); got == 0 {
		t.Fatalf("details = %+v, want a non-zero ignored count", result.Details)
	}

	all := NewGlobTool(root).Execute(testContext(), map[string]any{
		"pattern": "**/*.go", "gitignore": false,
	})
	if !strings.Contains(all.Text, "backend/main.go") {
		t.Fatalf("gitignore=false did not include the ignored tree: %q", all.Text)
	}
}

// A scan's completeness facts are evidence, not presentation. The result must
// disclose gitignore pruning, built-in directory pruning, and an investigative
// gitignore override so the claim layer can keep them out of repository-wide
// absence proofs.
func TestGlobDisclosesCompletenessFacts(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main.go":               "package main",
		"node_modules/pkg/a.js": "module.exports = {}",
		"ignored/old.go":        "package ignored",
		".gitignore":            "ignored/\n",
	})
	tool := NewGlobTool(root)

	normal := tool.Execute(testContext(), map[string]any{"pattern": "**/*.go"})
	details, _ := normal.Details.(map[string]any)
	if got, _ := details["ignored"].(int); got != 1 {
		t.Fatalf("details = %#v, want ignored=1", normal.Details)
	}
	if got, _ := details["excluded"].(int); got != 1 {
		t.Fatalf("details = %#v, want excluded=1 for node_modules", normal.Details)
	}
	if got, _ := details["gitignore"].(bool); !got {
		t.Fatalf("details = %#v, want gitignore=true", normal.Details)
	}

	override := tool.Execute(testContext(), map[string]any{
		"pattern": "**/*.go", "gitignore": false,
	})
	overrideDetails, _ := override.Details.(map[string]any)
	if got, _ := overrideDetails["gitignore"].(bool); got {
		t.Fatalf("details = %#v, want gitignore=false for the override", override.Details)
	}
}

// An empty tree must not invent an ignore exclusion. The hint is evidence, not
// a generic disclaimer.
func TestGlobDoesNotInventGitignoreWhenNothingWasIgnored(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"notes.md": "text"})
	result := NewGlobTool(root).Execute(testContext(), map[string]any{"pattern": "**/*.go"})
	if strings.Contains(result.Text, "gitignore") {
		t.Fatalf("text = %q, want no ignore claim in an empty tree", result.Text)
	}
	details, _ := result.Details.(map[string]any)
	if got, _ := details["ignored"].(int); got != 0 {
		t.Fatalf("details = %#v, want ignored=0", result.Details)
	}
}
