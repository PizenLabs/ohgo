package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
)

// newTestIgnorer builds an ignorer from literal .gitignore contents, without
// touching the filesystem, so the rule semantics can be tested directly.
func newTestIgnorer(contents string) *ignorer {
	ig := &ignorer{root: "/"}
	ig.appendFile("", contents)
	return ig
}

func TestIgnorerExcludesPlainNameAtAnyDepth(t *testing.T) {
	ig := newTestIgnorer("*.log\n")
	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"a.log", false, true},
		{"logs/a.log", false, true},
		{"logs/deep/a.log", false, true},
		{"a.txt", false, false},
		{"log", false, false},
	}
	for _, tc := range cases {
		if got := ig.ignored(tc.path, tc.isDir); got != tc.want {
			t.Errorf("ignored(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

func TestIgnorerExcludesNamedDirectory(t *testing.T) {
	ig := newTestIgnorer("node_modules\n")
	if !ig.ignored("node_modules", true) {
		t.Error("the directory itself was not excluded")
	}
	if !ig.ignored("node_modules/index.js", false) {
		t.Error("a file inside an excluded directory was not excluded")
	}
	if !ig.ignored("packages/node_modules/x.js", false) {
		t.Error("a nested occurrence was not excluded")
	}
}

func TestIgnorerHonoursTrailingSlashAsDirOnly(t *testing.T) {
	ig := newTestIgnorer("build/\n")
	if !ig.ignored("build", true) {
		t.Error("the directory was not excluded")
	}
	// A dir-only rule must not exclude a file of the same name.
	if ig.ignored("build", false) {
		t.Error("a dir-only rule excluded a plain file")
	}
}

func TestIgnorerAnchorsPatternsWithSlash(t *testing.T) {
	ig := newTestIgnorer("/top/only.txt\n")
	if !ig.ignored("top/only.txt", false) {
		t.Error("an anchored pattern did not match its exact path")
	}
	if ig.ignored("nested/top/only.txt", false) {
		t.Error("an anchored pattern matched below its root")
	}
}

func TestIgnorerAnchorsBarePatternToBase(t *testing.T) {
	// "/ohgo" is the repository's root binary. It must not match the
	// "cmd/ohgo" directory: a bare anchored pattern matches from the base,
	// not at any depth.
	ig := newTestIgnorer("/ohgo\n")
	if !ig.ignored("ohgo", false) {
		t.Error("the anchored root file was not excluded")
	}
	if ig.ignored("cmd/ohgo", true) {
		t.Error("an anchored root file name matched a nested directory")
	}
	if ig.ignored("cmd/ohgo/run.go", false) {
		t.Error("an anchored root file name matched a nested source file")
	}
	if !ig.ignored("ohgo/cache", true) {
		t.Error("a directory matched by the anchored name did not exclude its contents")
	}
}

func TestIgnorerAnchorsBarePatternInsideNestedBase(t *testing.T) {
	ig := &ignorer{root: "/"}
	ig.appendFile("cmd", "/ohgo\n")
	if !ig.ignored("cmd/ohgo", true) {
		t.Error("the nested anchored file was not excluded")
	}
	if ig.ignored("other/ohgo", true) {
		t.Error("the nested anchored rule leaked outside its base")
	}
}

func TestGrepDoesNotLetAnchoredRootRuleHideNestedSource(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":      "/ohgo\n",
		"ohgo":            "needle in the ignored root binary\n",
		"cmd/ohgo/run.go": "needle in a nested source file\n",
	})
	text, result := grepText(t, NewGrepTool(root), map[string]any{"pattern": "needle"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", text)
	}
	if !strings.Contains(text, "cmd/ohgo/run.go") {
		t.Errorf("the nested source file was hidden by the anchored root rule: %s", text)
	}
	if strings.Contains(text, "ohgo:\n") {
		t.Errorf("the anchored root file was not ignored: %s", text)
	}
}

func TestIgnorerAppliesNegationLast(t *testing.T) {
	ig := newTestIgnorer("*.go\n!keep.go\n")
	if !ig.ignored("drop.go", false) {
		t.Error("the blanket exclusion did not apply")
	}
	if ig.ignored("keep.go", false) {
		t.Error("a negated rule did not rescue the file")
	}
}

func TestIgnorerHonoursCommentsAndBlanks(t *testing.T) {
	ig := newTestIgnorer("# a comment\n\n   \n*.tmp\n")
	if ig.ignored("a.tmp", false) != true {
		t.Error("a rule after comments was not loaded")
	}
	if ig.ignored("a.go", false) {
		t.Error("a comment line was parsed as a rule")
	}
}

func TestIgnorerIsInertWhenDisabled(t *testing.T) {
	ig := &ignorer{root: "/", disabled: true}
	ig.appendFile("", "*.go\n")
	if ig.ignored("a.go", false) {
		t.Error("a disabled ignorer still excluded files")
	}
}

func TestIgnorerScopesNestedFilesToTheirSubtree(t *testing.T) {
	// A rule in sub/.gitignore governs only sub/, which is how git scopes
	// nested ignore files.
	ig := &ignorer{root: "/"}
	ig.appendFile("sub", "*.tmp\n")

	if !ig.ignored("sub/a.tmp", false) {
		t.Error("a nested rule did not apply inside its own directory")
	}
	if ig.ignored("other/a.tmp", false) {
		t.Error("a nested rule leaked outside its directory")
	}
}

func TestIgnorerLetsNestedNegationWin(t *testing.T) {
	ig := &ignorer{root: "/"}
	ig.appendFile("", "*.log\n")
	ig.appendFile("sub", "!keep.log\n")

	if !ig.ignored("a.log", false) {
		t.Error("the outer exclusion stopped applying")
	}
	if ig.ignored("sub/keep.log", false) {
		t.Error("a nested negation did not override the outer exclusion")
	}
}

func TestIgnorerLoadsNestedGitIgnoreFilesFromDisk(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":     "*.log\n",
		"sub/.gitignore": "!keep.log\n",
		"a.log":          "x",
		"keep.log":       "y",
		"sub/keep.log":   "z",
		"other/keep.log": "w",
	})

	ig := newIgnorer(root, true)
	if !ig.ignored("a.log", false) {
		t.Error("the root rule did not apply")
	}
	if ig.ignored("sub/keep.log", false) {
		t.Error("a nested negation was not picked up from disk")
	}
	// The nested negation must be scoped: outside sub/ the outer rule stands.
	if !ig.ignored("other/keep.log", false) {
		t.Error("a scoped negation leaked to a sibling directory")
	}
}

func TestIgnorerNeverDescendsIntoGitOrBuiltInExcludes(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".git/config":             "[core]",
		"node_modules/.gitignore": "*.js\n",
		".gitignore":              "*.log\n",
	})

	// A .gitignore inside node_modules must never be loaded: the built-in
	// prune means the walker never reaches it, so its rules cannot affect
	// the rest of the tree.
	ig := newIgnorer(root, true)
	if !ig.ignored("a.log", false) {
		t.Error("the root .gitignore was not loaded")
	}
	if ig.ignored("src/a.js", false) {
		t.Error("a rule from a pruned directory leaked out")
	}
}

func TestNewIgnorerHandlesMissingAndUnreadableFiles(t *testing.T) {
	root := t.TempDir()
	// A directory with no .gitignore at all is the common case and must not
	// error or exclude anything.
	ig := newIgnorer(root, true)
	if ig == nil {
		t.Fatal("newIgnorer returned nil for a directory without .gitignore")
	}
	if ig.ignored("a.go", false) {
		t.Error("files were excluded with no rules loaded")
	}
}

func TestMatchSegmentsNeverCrossesSeparators(t *testing.T) {
	// "*" must not span a "/": this is the difference between
	// "src/*.ts" matching one level and matching everything.
	if matchSegments([]string{"src", "*.ts"}, []string{"src", "a", "b.ts"}) {
		t.Error("a single-star pattern crossed a directory boundary")
	}
	if !matchSegments([]string{"src", "*.ts"}, []string{"src", "a.ts"}) {
		t.Error("a valid single-level pattern did not match")
	}
}

func TestResolvePathRejectsEscapingRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := resolvePath(root, "../../etc/passwd"); err == nil {
		t.Fatal("expected an escaping path to fail")
	}
	if _, err := resolvePath(root, "sub/../../outside"); err == nil {
		t.Fatal("expected a traversal path to fail")
	}
	// A safe relative path must still resolve.
	got, err := resolvePath(root, "sub/file.go")
	if err != nil {
		t.Fatalf("a safe path failed: %v", err)
	}
	if !withinRoot(root, got) {
		t.Errorf("resolved path %q escaped the root", got)
	}
}

func TestWithinRootRejectsSiblings(t *testing.T) {
	root := filepath.Join(string(os.PathSeparator)+"tmp", "project")
	if withinRoot(root, filepath.Join(string(os.PathSeparator)+"tmp", "project-other")) {
		t.Error("a sibling with a shared prefix was accepted as inside the root")
	}
	if !withinRoot(root, filepath.Join(root, "a", "b")) {
		t.Error("a legitimate child was rejected")
	}
}

func TestToolsRejectSymlinkEscapes(t *testing.T) {
	// Containment must hold after symlink resolution: a link planted inside
	// the workspace is otherwise a direct route to a file the model never
	// named.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Every read-path tool must refuse the traversal.
	cases := []struct {
		name string
		tool agent.Tool
		args map[string]any
	}{
		{"read", NewReadTool(root), map[string]any{"path": "escape/secret.txt"}},
		{"write", NewWriteTool(root), map[string]any{"path": "escape/new.txt", "content": "x"}},
		{"edit", NewEditTool(root), map[string]any{"path": "escape/secret.txt", "old_string": "a", "new_string": "b"}},
		{"glob", NewGlobTool(root), map[string]any{"pattern": "*.txt", "path": "escape"}},
		{"grep", NewGrepTool(root), map[string]any{"pattern": "top secret", "path": "escape"}},
		{"ls", NewLsTool(root), map[string]any{"path": "escape"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if result := tc.tool.Execute(testContext(), tc.args); !result.IsError {
				t.Errorf("tool allowed a symlink escape: %s", result.Text)
			}
		})
	}
}

func TestToolsFollowSymlinksWithinRoot(t *testing.T) {
	// A link that stays inside the workspace must keep working: containment
	// is about the destination, not about forbidding links.
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result := NewReadTool(root).Execute(testContext(), map[string]any{"path": "alias/a.go"})
	if result.IsError {
		t.Fatalf("a link inside the root was rejected: %s", result.Text)
	}
	if !strings.Contains(result.Text, "package a") {
		t.Errorf("contents not read through the link: %s", result.Text)
	}
}
