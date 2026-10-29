package vcs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func discoverTestRepo(t *testing.T, root string) *RepoInfo {
	t.Helper()
	info, err := Discover(root)
	if err != nil || info == nil {
		t.Fatalf("Discover(%s): %v (%v)", root, err, info)
	}
	return info
}

func TestStatusEntryPredicatesClassifyIndexAndWorktree(t *testing.T) {
	for _, tc := range []struct {
		name      string
		entry     StatusEntry
		staged    bool
		unstaged  bool
		untracked bool
		ignored   bool
	}{
		{"worktree edit", StatusEntry{Index: ' ', Worktree: 'M'}, false, true, false, false},
		{"index edit", StatusEntry{Index: 'M', Worktree: ' '}, true, false, false, false},
		{"both", StatusEntry{Index: 'M', Worktree: 'M'}, true, true, false, false},
		{"untracked", StatusEntry{Index: '?', Worktree: '?'}, false, false, true, false},
		{"ignored", StatusEntry{Index: '!', Worktree: '!'}, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.Staged(); got != tc.staged {
				t.Errorf("Staged = %v, want %v", got, tc.staged)
			}
			if got := tc.entry.Unstaged(); got != tc.unstaged {
				t.Errorf("Unstaged = %v, want %v", got, tc.unstaged)
			}
			if got := tc.entry.Untracked(); got != tc.untracked {
				t.Errorf("Untracked = %v, want %v", got, tc.untracked)
			}
			if got := tc.entry.Ignored(); got != tc.ignored {
				t.Errorf("Ignored = %v, want %v", got, tc.ignored)
			}
		})
	}
}

func TestStatusQueriesHonorPathspecsAndFailure(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "a.txt", "two\n")
	write(t, root, "b.txt", "new\n")

	info := discoverTestRepo(t, root)
	ctx := context.Background()
	entries, err := info.StatusEntries(ctx, StatusOptions{Untracked: "all", Pathspecs: []string{"a.txt"}})
	if err != nil {
		t.Fatalf("StatusEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "a.txt" {
		t.Errorf("entries = %+v, want only the named pathspec", entries)
	}

	text, err := info.StatusPorcelain(ctx, StatusOptions{Untracked: "normal", Pathspecs: []string{"b.txt"}})
	if err != nil {
		t.Fatalf("StatusPorcelain: %v", err)
	}
	if !strings.Contains(text, "b.txt") || strings.Contains(text, "a.txt") {
		t.Errorf("porcelain = %q, want only b.txt", text)
	}

	broken := &RepoInfo{Root: t.TempDir()}
	if _, err := broken.StatusEntries(ctx, StatusOptions{}); err == nil {
		t.Error("StatusEntries succeeded outside a repository")
	}
	if _, err := broken.StatusPorcelain(ctx, StatusOptions{}); err == nil {
		t.Error("StatusPorcelain succeeded outside a repository")
	}
}

func TestHeadAndBranchDistinguishUnbornDetachedAndMissing(t *testing.T) {
	root := initRepo(t)
	info := discoverTestRepo(t, root)
	ctx := context.Background()

	if sha, err := info.HeadSHA(ctx); err != nil || sha != "" {
		t.Errorf("HeadSHA on an unborn branch = %q, %v; want empty and no error", sha, err)
	}
	if branch, err := info.CurrentBranch(ctx); err != nil || branch == "" {
		t.Errorf("CurrentBranch on an unborn branch = %q, %v; want the branch name", branch, err)
	}

	write(t, root, "a.txt", "one\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	if sha, err := info.HeadSHA(ctx); err != nil || sha == "" {
		t.Errorf("HeadSHA after a commit = %q, %v", sha, err)
	}
	git(t, root, "checkout", "--quiet", "--detach")
	if branch, err := info.CurrentBranch(ctx); err != nil || branch != "" {
		t.Errorf("CurrentBranch detached = %q, %v; want empty and no error", branch, err)
	}

	broken := &RepoInfo{Root: t.TempDir()}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := broken.HeadSHA(canceled); err == nil {
		t.Error("HeadSHA did not surface a transport failure")
	}
	if _, err := broken.CurrentBranch(canceled); err == nil {
		t.Error("CurrentBranch did not surface a transport failure")
	}
}

func TestStageAndIndexOperationsStayExact(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	write(t, root, "b.txt", "two\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "a.txt", "changed\n")
	git(t, root, "add", "--", "a.txt")
	write(t, root, "b.txt", "changed too\n")

	info := discoverTestRepo(t, root)
	ctx := context.Background()
	if err := info.StagePaths(ctx, nil); err == nil {
		t.Error("StagePaths accepted an empty list")
	}
	if _, err := info.StagedPaths(ctx); err != nil {
		t.Fatalf("StagedPaths: %v", err)
	}
	hashes, err := info.IndexHashes(ctx, nil)
	if err != nil || len(hashes) != 0 {
		t.Errorf("IndexHashes(nil) = %v, %v; want an empty map", hashes, err)
	}

	broken := &RepoInfo{Root: t.TempDir()}
	if _, err := broken.StagedPaths(ctx); err == nil {
		t.Error("StagedPaths succeeded outside a repository")
	}
	if _, err := broken.IndexHashes(ctx, []string{"a.txt"}); err == nil {
		t.Error("IndexHashes succeeded outside a repository")
	}
}

func TestHashPathHashesRegularFilesSymlinksAndMissingPaths(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	info := discoverTestRepo(t, root)
	ctx := context.Background()

	regular, found, err := info.HashPath(ctx, "a.txt")
	if err != nil || !found || regular == "" {
		t.Fatalf("HashPath regular = %q found=%v err=%v", regular, found, err)
	}
	link, found, err := info.HashPath(ctx, "link")
	if err != nil || !found || link == "" || link == regular {
		t.Errorf("HashPath symlink = %q found=%v err=%v, want the link target blob", link, found, err)
	}
	if _, found, err := info.HashPath(ctx, "missing.txt"); err != nil || found {
		t.Errorf("HashPath missing found=%v err=%v, want not found and no error", found, err)
	}
	if _, _, err := info.HashPath(ctx, "dir"); err == nil {
		t.Error("HashPath accepted a directory")
	}
	for _, escape := range []string{"../outside", "/absolute"} {
		if _, _, err := info.HashPath(ctx, escape); err == nil {
			t.Errorf("HashPath(%q) accepted a path outside the repository", escape)
		}
	}
	if _, _, err := info.HashPath(ctx, "a.txt/child"); err == nil {
		t.Error("HashPath ignored a non-directory stat failure")
	}
}

func TestTreeAndCommitPlumbingAgree(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	info := discoverTestRepo(t, root)
	ctx := context.Background()
	if err := info.StagePaths(ctx, []string{"a.txt"}); err != nil {
		t.Fatalf("StagePaths: %v", err)
	}
	tree, err := info.IndexTree(ctx)
	if err != nil || tree == "" {
		t.Fatalf("IndexTree = %q, %v", tree, err)
	}
	if _, err := info.CommitTree(ctx, ""); err == nil {
		t.Error("CommitTree accepted an empty revision")
	}
	if _, err := info.CommitTree(ctx, "definitely-not-a-rev"); err == nil {
		t.Error("CommitTree accepted an unknown revision")
	}
	if _, err := info.CommitMessage(ctx, "  "); err == nil {
		t.Error("CommitMessage accepted a blank message")
	}
	sha, err := info.CommitMessage(ctx, "initial")
	if err != nil || sha == "" {
		t.Fatalf("CommitMessage = %q, %v", sha, err)
	}
	committedTree, err := info.CommitTree(ctx, sha)
	if err != nil || committedTree != tree {
		t.Errorf("CommitTree = %q, %v; want the staged index tree %q", committedTree, err, tree)
	}
}

func TestDiffAndNumstatHonorOptions(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	write(t, root, "b.txt", "one\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initial")
	write(t, root, "a.txt", "two\n")
	write(t, root, "b.txt", "two\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "second")
	write(t, root, "a.txt", "three\n")
	write(t, root, "b.txt", "three\n")
	git(t, root, "add", "--", "b.txt")

	info := discoverTestRepo(t, root)
	ctx := context.Background()
	diff, err := info.Diff(ctx, DiffOptions{Context: 1, Files: []string{"a.txt"}})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if strings.Contains(diff, "b.txt") || !strings.Contains(diff, "+three") {
		t.Errorf("Diff = %q, want only the named file with the requested context", diff)
	}

	cached, err := info.Diff(ctx, DiffOptions{Cached: true, Files: []string{"b.txt"}})
	if err != nil {
		t.Fatalf("Diff cached: %v", err)
	}
	if !strings.Contains(cached, "+three") || strings.Contains(cached, "a.txt") {
		t.Errorf("cached diff = %q, want the indexed b.txt change", cached)
	}

	between, err := info.Diff(ctx, DiffOptions{Base: "HEAD~1", Head: "HEAD"})
	if err != nil {
		t.Fatalf("Diff base/head: %v", err)
	}
	if !strings.Contains(between, "+two") {
		t.Errorf("base/head diff = %q, want the committed change", between)
	}

	stats, err := info.Numstat(ctx, DiffOptions{Cached: true, Files: []string{"b.txt"}})
	if err != nil || len(stats) != 1 || stats[0].Path != "b.txt" {
		t.Errorf("Numstat = %+v, %v; want the named indexed file", stats, err)
	}
	betweenStats, err := info.Numstat(ctx, DiffOptions{Base: "HEAD~1", Head: "HEAD"})
	if err != nil || len(betweenStats) != 2 {
		t.Errorf("Numstat base/head = %+v, %v; want both committed files", betweenStats, err)
	}
	files, err := info.ChangedFiles(ctx, DiffOptions{Base: "HEAD~1", Head: "HEAD"})
	if err != nil || !contains2(files, "a.txt") || !contains2(files, "b.txt") {
		t.Errorf("ChangedFiles = %v, %v; want both committed files", files, err)
	}
	cachedFiles, err := info.ChangedFiles(ctx, DiffOptions{Cached: true})
	if err != nil || len(cachedFiles) != 1 || cachedFiles[0] != "b.txt" {
		t.Errorf("ChangedFiles cached = %v, %v", cachedFiles, err)
	}

	broken := &RepoInfo{Root: t.TempDir()}
	if _, err := broken.Diff(ctx, DiffOptions{}); err == nil {
		t.Error("Diff succeeded outside a repository")
	}
	if _, err := broken.Numstat(ctx, DiffOptions{}); err == nil {
		t.Error("Numstat succeeded outside a repository")
	}
	if _, err := broken.ChangedFiles(ctx, DiffOptions{}); err == nil {
		t.Error("ChangedFiles succeeded outside a repository")
	}
}

func TestLogAndCommitDetailsRejectInvalidQueries(t *testing.T) {
	root := initRepo(t)
	write(t, root, "a.txt", "one\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-m", "initial")
	info := discoverTestRepo(t, root)
	ctx := context.Background()

	if lines, err := info.LogOnelines(ctx, 0); err != nil || lines != nil {
		t.Errorf("LogOnelines(0) = %v, %v; want nil and no error", lines, err)
	}
	if _, err := info.CommitDetails(ctx, " "); err == nil {
		t.Error("CommitDetails accepted an empty revision")
	}
	if _, err := info.CommitDetails(ctx, "definitely-not-a-rev"); err == nil {
		t.Error("CommitDetails accepted an unknown revision")
	}

	broken := &RepoInfo{Root: t.TempDir()}
	if _, err := broken.LogOnelines(ctx, 5); err == nil {
		t.Error("LogOnelines succeeded outside a repository")
	}
	if _, err := broken.CommitDetails(ctx, "HEAD"); err == nil {
		t.Error("CommitDetails succeeded outside a repository")
	}
}

func TestRunInputFeedsStdinToGit(t *testing.T) {
	root := initRepo(t)
	info := discoverTestRepo(t, root)
	hash, err := info.runInput(context.Background(), []byte("hello\n"), "hash-object", "--stdin")
	if err != nil {
		t.Fatalf("runInput: %v", err)
	}
	if strings.TrimSpace(hash) == "" {
		t.Error("runInput returned no hash for stdin content")
	}
}

func TestRunExitReportsACancelledCommand(t *testing.T) {
	root := initRepo(t)
	info := discoverTestRepo(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, code, err := info.runExit(ctx, nil, "status"); err == nil {
		t.Errorf("runExit on a cancelled context = code %d, want an error", code)
	}
	if _, err := info.run(ctx, "definitely-not-a-command"); err == nil {
		t.Error("run did not surface the git failure")
	}
}
