package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a real git repository, since discovery reads .git directly
// and a hand-built fixture would drift from what git actually writes.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestDiscoverFindsTheOutermostRepository(t *testing.T) {
	root := initRepo(t)
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	info, err := Discover(nested)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if info == nil {
		t.Fatal("no repository found")
	}
	// The walk must stop at the outermost repo, not the nearest parent.
	if info.Root != root {
		t.Errorf("root = %q, want %q", info.Root, root)
	}
}

func TestDiscoverReportsNoRepository(t *testing.T) {
	// A directory outside any repo must not error: "not a repo" is a normal
	// answer, not a failure.
	info, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if info != nil {
		t.Errorf("found a repository where none exists: %+v", info)
	}
}

func TestDiscoverResolvesALinkedWorktree(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "hello\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")

	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "--quiet", linked)

	info, err := Discover(linked)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if info == nil {
		t.Fatal("no repository found in a linked worktree")
	}
	// The checkout root must be the worktree, and the object store must be
	// shared with the primary checkout.
	if info.Root != linked {
		t.Errorf("root = %q, want the worktree root", info.Root)
	}
	if info.CommonDir == info.GitDir {
		t.Error("a linked worktree shares its object store; common and git dirs must differ")
	}
	if _, err := os.Stat(info.HeadPath); err != nil {
		t.Errorf("HEAD is not readable in the worktree: %v", err)
	}
}

func TestReadHeadResolvesABranch(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "x\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")

	info, err := Discover(root)
	if err != nil || info == nil {
		t.Fatalf("Discover: %v", err)
	}
	head, err := info.ReadHead()
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if head.Detached {
		t.Error("HEAD was reported as detached on a branch")
	}
	if head.Branch == "" {
		t.Error("no branch name was resolved")
	}
	if head.Commit == "" {
		t.Error("no commit was resolved for a born branch")
	}
}

func TestReadHeadHandlesAnUnbornBranch(t *testing.T) {
	// A fresh clone has no commits; that must not surface as an error, because
	// callers distinguish it by the empty commit.
	root := initRepo(t)
	info, err := Discover(root)
	if err != nil || info == nil {
		t.Fatalf("Discover: %v", err)
	}

	head, err := info.ReadHead()
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if head.Commit != "" {
		t.Errorf("commit = %q, want empty for an unborn branch", head.Commit)
	}
	if got := info.Label(); got != "empty" {
		t.Errorf("label = %q, want \"empty\"", got)
	}
}

func TestLabelReportsADetachedHeadAsAShortSHA(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "x\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	git(t, root, "checkout", "--quiet", "--detach")

	info, _ := Discover(root)
	label := info.Label()
	if len(label) != 7 {
		t.Errorf("label = %q, want a 7-character short SHA", label)
	}
}

func TestSummaryCountsFilesByBucket(t *testing.T) {
	root := initRepo(t)
	write(t, root, "tracked.txt", "one\n")
	git(t, root, "add", "tracked.txt")
	git(t, root, "commit", "-m", "initial")

	// One staged, one unstaged, one untracked.
	write(t, root, "staged.txt", "s\n")
	write(t, root, "tracked.txt", "two\n")
	write(t, root, "untracked.txt", "u\n")
	git(t, root, "add", "staged.txt")

	info, _ := Discover(root)
	summary, err := info.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Staged != 1 {
		t.Errorf("staged = %d, want 1", summary.Staged)
	}
	if summary.Unstaged != 1 {
		t.Errorf("unstaged = %d, want 1", summary.Unstaged)
	}
	if summary.Untracked != 1 {
		t.Errorf("untracked = %d, want 1", summary.Untracked)
	}
	if summary.Clean() {
		t.Error("a dirty tree reported as clean")
	}
}

func TestSummaryReportsCleanForAnUnmodifiedTree(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "x\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")

	info, _ := Discover(root)
	summary, err := info.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if !summary.Clean() {
		t.Errorf("summary = %+v, want clean", summary)
	}
}

func TestDiffReturnsTheWorkingTreeChange(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "a.txt", "two\n")

	info, _ := Discover(root)
	diff, err := info.Diff(context.Background(), DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "-one") || !strings.Contains(diff, "+two") {
		t.Errorf("diff does not describe the change: %s", diff)
	}
}

func TestDiffCachedDiffsTheIndex(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "a.txt", "two\n")
	git(t, root, "add", "a.txt")

	info, _ := Discover(root)
	cached, err := info.Diff(context.Background(), DiffOptions{Cached: true})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(cached, "+two") {
		t.Errorf("cached diff missing the staged change: %s", cached)
	}

	// The unstaged diff must be empty now that the worktree matches the index.
	unstaged, err := info.Diff(context.Background(), DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.TrimSpace(unstaged) != "" {
		t.Errorf("unstaged diff is not empty: %s", unstaged)
	}
}

func TestChangedFilesAndNumstat(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "a.txt", "one\ntwo\nthree\n")

	info, _ := Discover(root)
	files, err := info.ChangedFiles(context.Background(), DiffOptions{})
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "a.txt" {
		t.Errorf("changed files = %v, want [a.txt]", files)
	}

	stats, err := info.Numstat(context.Background(), DiffOptions{})
	if err != nil {
		t.Fatalf("Numstat: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("numstat = %d entries, want 1", len(stats))
	}
	if stats[0].Added == nil || *stats[0].Added != 2 {
		t.Errorf("added = %v, want 2", stats[0].Added)
	}
}

func TestNumstatReportsBinaryAsNil(t *testing.T) {
	// Binary files have no line counts, and a nil is what distinguishes them
	// from a genuine zero.
	root := initRepo(t)
	if err := os.WriteFile(filepath.Join(root, "bin"), []byte{0, 1, 2, 3, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "bin")
	git(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(root, "bin"), []byte{0, 1, 2, 3, 4, 0}, 0o644); err != nil {
		t.Fatal(err)
	}

	info, _ := Discover(root)
	stats, err := info.Numstat(context.Background(), DiffOptions{})
	if err != nil {
		t.Fatalf("Numstat: %v", err)
	}
	if len(stats) == 0 {
		t.Fatal("no numstat entries")
	}
	if stats[0].Added != nil || stats[0].Removed != nil {
		t.Errorf("binary counts = %v/%v, want nil/nil", stats[0].Added, stats[0].Removed)
	}
}

func TestCommitDetailsReadsMessageAndParent(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "x\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "a.txt", "y\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "second\n\nwith a body")

	info, _ := Discover(root)
	head, _ := info.ReadHead()
	details, err := info.CommitDetails(context.Background(), head.Commit)
	if err != nil {
		t.Fatalf("CommitDetails: %v", err)
	}
	if details.SHA != head.Commit {
		t.Errorf("sha = %q, want %q", details.SHA, head.Commit)
	}
	if len(details.Parents) != 1 {
		t.Errorf("parents = %v, want exactly one", details.Parents)
	}
	if !strings.Contains(details.Message, "second") || !strings.Contains(details.Message, "with a body") {
		t.Errorf("message = %q, want subject and body", details.Message)
	}
}

func TestLogOnelinesReturnsCommits(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "x\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "a.txt", "y\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "second")

	info, _ := Discover(root)
	log, err := info.LogOnelines(context.Background(), 2)
	if err != nil {
		t.Fatalf("LogOnelines: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("log = %d entries, want 2", len(log))
	}
	if !strings.Contains(log[0], "second") {
		t.Errorf("newest commit = %q, want \"second\"", log[0])
	}
}

func TestLsFilesListsTrackedFilesOnly(t *testing.T) {
	root := initRepo(t)
	write(t, root, "tracked.txt", "x\n")
	write(t, root, "untracked.txt", "y\n")
	git(t, root, "add", "tracked.txt")

	info, _ := Discover(root)
	files, err := info.LsFiles(context.Background(), false)
	if err != nil {
		t.Fatalf("LsFiles: %v", err)
	}
	if contains2(files, "tracked.txt") != true {
		t.Errorf("a tracked file was not listed: %v", files)
	}
	if contains2(files, "untracked.txt") {
		t.Errorf("an untracked file was listed: %v", files)
	}
}

func TestRunSurfacesGitFailure(t *testing.T) {
	// A command failure must carry git's own message, or a caller debugging a
	// rejected option sees only "exit status 1".
	root := initRepo(t)
	info, _ := Discover(root)
	if _, err := info.run(context.Background(), "definitely-not-a-command"); err == nil {
		t.Fatal("expected a git failure")
	}
}

func TestDiscoveredTreeMatchesGit(t *testing.T) {
	// The status line and the tree walk must agree on what is ignored, or a
	// file shows up in one and not the other.
	root := initRepo(t)
	write(t, root, ".gitignore", "ignored.txt\n")
	write(t, root, "a.go", "package a\n")
	// ls-files reports the index, so the file must be tracked before it can be
	// listed at all.
	git(t, root, "add", "a.go")

	info, _ := Discover(root)
	files, err := info.LsFiles(context.Background(), true)
	if err != nil {
		t.Fatalf("LsFiles: %v", err)
	}
	if contains2(files, "ignored.txt") {
		t.Errorf("an ignored file was listed: %v", files)
	}
	if !contains2(files, "a.go") {
		t.Errorf("a real file was not listed: %v", files)
	}
}

func contains2(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
