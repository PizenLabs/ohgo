package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/project"
)

// writeFile creates a file, making its parent directories first.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProjectContextReadsBothInstructionFiles pins the hierarchy: the root
// AGENTS.md is the primary source and the optional .ohgo/AGENTS.md is
// supplementary, so an OHGO-local file must never displace the root one or be
// dropped when both exist.
func TestProjectContextReadsBothInstructionFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeFile(t, filepath.Join(root, project.DirName, "AGENTS.md"), "ohgo rules")

	got := ProjectContext(root)
	rootAt := strings.Index(got, "root rules")
	localAt := strings.Index(got, "ohgo rules")
	if rootAt < 0 {
		t.Errorf("root AGENTS.md was discarded when .ohgo/AGENTS.md exists: %q", got)
	}
	if localAt < 0 {
		t.Errorf(".ohgo/AGENTS.md was not discovered: %q", got)
	}
	if rootAt >= 0 && localAt >= 0 && rootAt > localAt {
		t.Errorf("root AGENTS.md must be read before .ohgo/AGENTS.md, got:\n%s", got)
	}
}

// TestProjectContextReadsOhgoLocalAlone covers the supplementary file standing
// on its own: a project that keeps its instructions only under .ohgo/ still
// contributes them.
func TestProjectContextReadsOhgoLocalAlone(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, project.DirName, "AGENTS.md"), "ohgo only")

	if got := ProjectContext(root); !strings.Contains(got, "ohgo only") {
		t.Errorf(".ohgo/AGENTS.md alone was ignored: %q", got)
	}
}

// TestProjectContextReadsRootAlone covers the expected common case: a project
// with only the root AGENTS.md behaves as before.
func TestProjectContextReadsRootAlone(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root only")

	got := ProjectContext(root)
	if !strings.Contains(got, "root only") {
		t.Errorf("root AGENTS.md was ignored: %q", got)
	}
	if strings.Contains(got, "OHGO project instructions") {
		t.Errorf("a supplementary section appeared with no .ohgo/AGENTS.md: %q", got)
	}
}

// TestProjectContextEmptyWhenNoInstructions keeps an unconfigured project from
// emitting an empty instructions block the model would have to interpret.
func TestProjectContextEmptyWhenNoInstructions(t *testing.T) {
	if got := ProjectContext(t.TempDir()); got != "" {
		t.Errorf("no instruction files should yield no context, got %q", got)
	}
}

// TestProjectContextAnchorsOhgoLocalAtProjectRoot pins the project boundary: a
// working directory nested under a Git worktree still resolves the
// supplementary file at the worktree root, and the primary file by nearest
// match. A nested AGENTS.md stays the primary source for its directory, but
// must not hide the project's OHGO-local instructions.
func TestProjectContextAnchorsOhgoLocalAtProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules")
	writeFile(t, filepath.Join(root, project.DirName, "AGENTS.md"), "ohgo rules")
	nested := filepath.Join(root, "pkg", "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nested, "AGENTS.md"), "nested rules")

	got := ProjectContext(nested)
	if !strings.Contains(got, "nested rules") {
		t.Errorf("nearest AGENTS.md should be the primary source, got:\n%s", got)
	}
	if !strings.Contains(got, "ohgo rules") {
		t.Errorf(".ohgo/AGENTS.md at the project root was not discovered from a nested directory:\n%s", got)
	}
	if strings.Index(got, "nested rules") > strings.Index(got, "ohgo rules") {
		t.Errorf("the primary instructions must precede the supplementary ones:\n%s", got)
	}
}

// A referenced file is rendered with its path and its exact bytes, so the model
// can cite the source and reason over the real content. No files yields no
// block rather than an empty header.
func TestMentionContextRendersPathAndContent(t *testing.T) {
	if got := MentionContext(nil); got != "" {
		t.Fatalf("MentionContext(nil) = %q, want empty", got)
	}
	got := MentionContext([]ReferencedFile{{Path: "tui/app.go", Content: "package tui"}})
	if !strings.Contains(got, `path="tui/app.go"`) {
		t.Errorf("context = %q, want the path", got)
	}
	if !strings.Contains(got, "package tui") {
		t.Errorf("context = %q, want the content verbatim", got)
	}
}
