package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// lsFixture writes a small repository-shaped tree with the boundaries a
// recursive listing must preserve: a nested directory, an ignored dependency
// tree, and a source file at each level.
func lsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		".gitignore":                "node_modules/\n",
		"components/ui/button.tsx":  "export const Button = 1\n",
		"components/card.tsx":       "export const Card = 1\n",
		"content/post.mdx":          "# post\n",
		"node_modules/dep/index.js": "module.exports = 1\n",
		"app/page.tsx":              "export default 1\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func lsResult(t *testing.T, result agent.Result) (string, map[string]any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("ls failed: %s", result.Text)
	}
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %T, want map[string]any", result.Details)
	}
	return result.Text, details
}

// A recursive listing is the topology primitive ASK relies on when shell
// access is unavailable: one call must describe nested boundaries, and it must
// not spend its entry budget on a pruned dependency tree.
func TestLsRecursiveListingPreservesNestedBoundaries(t *testing.T) {
	tool := NewLsTool(lsFixture(t))
	text, details := lsResult(t, tool.Execute(testContext(), map[string]any{"depth": float64(2)}))

	for _, want := range []string{
		"components/", "components/ui/", "components/ui/button.tsx",
		"components/card.tsx", "content/", "content/post.mdx",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("recursive listing omitted %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "node_modules") {
		t.Errorf("recursive listing did not honour .gitignore:\n%s", text)
	}
	if got := details["depth"]; got != 2 {
		t.Errorf("details depth = %v, want 2", got)
	}
	if got, _ := details["truncated"].(bool); got {
		t.Errorf("a small tree was reported as truncated")
	}
}

// Depth 0 keeps the flat contract: a dependency directory is visible when the
// caller asks for the directory's contents rather than for repository topology.
func TestLsDepthZeroStaysFlat(t *testing.T) {
	tool := NewLsTool(lsFixture(t))
	text, details := lsResult(t, tool.Execute(testContext(), map[string]any{}))
	if !strings.Contains(text, "node_modules/") {
		t.Errorf("flat listing hid a real directory entry:\n%s", text)
	}
	if strings.Contains(text, "button.tsx") || strings.Contains(text, "post.mdx") {
		t.Errorf("depth 0 descended into subdirectories:\n%s", text)
	}
	if got := details["depth"]; got != 0 {
		t.Errorf("details depth = %v, want 0", got)
	}
}

// The entry limit bounds the walk and the result says it stopped early. A
// silent cut presented the tree as complete, which is the failure a topology
// observation must not have.
func TestLsRecursiveLimitIsExplicit(t *testing.T) {
	tool := NewLsTool(lsFixture(t))
	text, details := lsResult(t, tool.Execute(testContext(), map[string]any{"depth": float64(3), "limit": float64(2)}))
	if got, _ := details["truncated"].(bool); !got {
		t.Fatalf("details truncated = false, want true:\n%s", text)
	}
	if got := details["entries"]; got != 2 {
		t.Fatalf("details entries = %v, want the limit of 2", got)
	}
	if !strings.Contains(text, "limit reached") || !strings.Contains(text, "stopped early") {
		t.Fatalf("truncated listing did not explain the cut:\n%s", text)
	}
}

// The walk is deterministic: the same tree must render the same bytes so a
// cached observation cannot be invalidated by map or readdir order.
func TestLsRecursiveListingIsDeterministic(t *testing.T) {
	tool := NewLsTool(lsFixture(t))
	first, _ := lsResult(t, tool.Execute(testContext(), map[string]any{"depth": float64(2)}))
	for range 3 {
		next, _ := lsResult(t, tool.Execute(testContext(), map[string]any{"depth": float64(2)}))
		if next != first {
			t.Fatalf("listing changed between identical calls:\n--- first\n%s\n--- next\n%s", first, next)
		}
	}
}

// A directory symlink is reported, never followed: a cycle must not turn a
// read-only topology observation into an unbounded walk.
func TestLsDoesNotFollowDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.ts"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "src", "loop")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	tool := NewLsTool(root)
	text, details := lsResult(t, tool.Execute(testContext(), map[string]any{"depth": float64(3)}))
	if got := details["entries"]; got != 3 {
		t.Fatalf("entries = %v, want src/, src/a.ts, and the symlink, not its contents:\n%s", got, text)
	}
	if strings.Count(text, "src/loop") > 1 {
		t.Fatalf("directory symlink was followed:\n%s", text)
	}
}
