package vcs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// DiffOptions configures a diff.
type DiffOptions struct {
	// Cached diffs the index against HEAD rather than the worktree.
	Cached bool
	// Base and Head produce a two-rev diff; Head requires Base.
	Base string
	Head string
	// Files restricts the diff to these paths.
	Files []string
	// Context is the unified context line count; 0 means the git default.
	Context int
}

// StatusOptions configures a status query.
type StatusOptions struct {
	// Untracked selects the untracked reporting mode.
	Untracked string
	// Ignored includes ignored paths, reported with the "!!" code.
	Ignored bool
	// Pathspecs restricts the query.
	Pathspecs []string
}

// StatusEntry is one parsed porcelain-v1 status record.
//
// Index and Worktree are the two porcelain status bytes. For a rename or copy
// the record also carries the original path; porcelain -z emits it as the next
// NUL-separated field rather than on the same line.
type StatusEntry struct {
	// Index is the index-side status byte.
	Index byte
	// Worktree is the worktree-side status byte.
	Worktree byte
	// Path is the current path, relative to the repository root.
	Path string
	// OrigPath is the pre-rename path, or "".
	OrigPath string
}

// Staged reports whether the entry has an index change.
func (e StatusEntry) Staged() bool {
	return e.Index != ' ' && e.Index != '?' && e.Index != '!'
}

// Unstaged reports whether the entry has a worktree change.
func (e StatusEntry) Unstaged() bool {
	return e.Worktree != ' ' && e.Worktree != '?' && e.Worktree != '!'
}

// Untracked reports the "??" record.
func (e StatusEntry) Untracked() bool {
	return e.Index == '?' && e.Worktree == '?'
}

// Ignored reports the "!!" record.
func (e StatusEntry) Ignored() bool {
	return e.Index == '!' && e.Worktree == '!'
}

// NumstatEntry is one file's added/removed line counts.
//
// Added and Removed are nil for binary files, which git reports as "-".
type NumstatEntry struct {
	Path    string
	Added   *int
	Removed *int
}

// CommitRecord is one recent commit as style evidence.
//
// Subject and Body are raw commit text. A consumer that learns conventions from
// them owns the bounding: this type preserves what Git reported rather than
// silently shortening a message that a verification path may need.
type CommitRecord struct {
	SHA     string
	Subject string
	Body    string
}

// CommitDetails is one commit.
type CommitDetails struct {
	SHA     string
	Parents []string
	Author  string
	Date    string
	Message string
}

// Diff returns the unified diff for the working tree.
func (info *RepoInfo) Diff(ctx context.Context, opts DiffOptions) (string, error) {
	// Literal pathspecs keep a repository path such as ":(top)file" from
	// selecting a different file than the caller named.
	args := []string{"--literal-pathspecs", "diff", "--no-ext-diff", "--no-color"}
	if opts.Context > 0 {
		args = append(args, fmt.Sprintf("--unified=%d", opts.Context))
	}
	if opts.Cached {
		args = append(args, "--cached")
	}
	if opts.Base != "" {
		args = append(args, opts.Base)
		if opts.Head != "" {
			args = append(args, opts.Head)
		}
	} else if opts.Head != "" {
		args = append(args, opts.Head)
	}
	if len(opts.Files) > 0 {
		args = append(args, "--")
		args = append(args, opts.Files...)
	}
	return info.run(ctx, args...)
}

// StatusPorcelain returns machine-readable status.
//
// The NUL-separated form is used unconditionally: path quoting otherwise mangles
// any path with a non-ASCII character or a quote in it.
func (info *RepoInfo) StatusPorcelain(ctx context.Context, opts StatusOptions) (string, error) {
	args := []string{"--literal-pathspecs", "status", "-z", "--porcelain"}
	if opts.Untracked != "" {
		args = append(args, "--untracked-files="+opts.Untracked)
	}
	if len(opts.Pathspecs) > 0 {
		args = append(args, "--")
		args = append(args, opts.Pathspecs...)
	}
	return info.run(ctx, args...)
}

// StatusEntries returns parsed status records.
//
// The parsed form exists so callers that must decide a commit scope can use the
// same status bytes the summary counts, including the original path of a
// rename, instead of re-parsing quoted porcelain text themselves.
func (info *RepoInfo) StatusEntries(ctx context.Context, opts StatusOptions) ([]StatusEntry, error) {
	args := []string{"--literal-pathspecs", "status", "-z", "--porcelain"}
	if opts.Untracked != "" {
		args = append(args, "--untracked-files="+opts.Untracked)
	}
	if opts.Ignored {
		args = append(args, "--ignored=matching")
	}
	if len(opts.Pathspecs) > 0 {
		args = append(args, "--")
		args = append(args, opts.Pathspecs...)
	}
	out, err := info.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseStatusEntries(out), nil
}

// parseStatusEntries decodes porcelain-v1 -z output.
func parseStatusEntries(output string) []StatusEntry {
	fields := strings.Split(output, "\x00")
	entries := make([]StatusEntry, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		// Two status bytes, one separator space, then the path.
		if len(entry) < 4 || entry[2] != ' ' {
			continue
		}
		parsed := StatusEntry{
			Index:    entry[0],
			Worktree: entry[1],
			Path:     entry[3:],
		}
		if parsed.Index == 'R' || parsed.Index == 'C' ||
			parsed.Worktree == 'R' || parsed.Worktree == 'C' {
			if i+1 < len(fields) {
				parsed.OrigPath = fields[i+1]
				i++
			}
		}
		entries = append(entries, parsed)
	}
	return entries
}

// OperationInProgress names an in-progress multi-commit operation, or "".
//
// /commit creates an ordinary single-parent commit. A merge, rebase,
// cherry-pick, revert, or bisect changes that shape and is a human decision, so
// the commit workflow refuses rather than creating a commit the verifier would
// have to reject after the fact.
func (info *RepoInfo) OperationInProgress() (string, error) {
	markers := []struct {
		name  string
		label string
	}{
		{"MERGE_HEAD", "merge"},
		{"CHERRY_PICK_HEAD", "cherry-pick"},
		{"REVERT_HEAD", "revert"},
		{"BISECT_LOG", "bisect"},
		{"rebase-merge", "rebase"},
		{"rebase-apply", "rebase"},
		{"sequencer", "cherry-pick or revert sequence"},
	}
	for _, marker := range markers {
		_, err := os.Lstat(filepath.Join(info.GitDir, marker.name))
		switch {
		case err == nil:
			return marker.label, nil
		case os.IsNotExist(err):
			continue
		default:
			return "", err
		}
	}
	return "", nil
}

// HeadSHA resolves HEAD, returning "" for an unborn branch.
func (info *RepoInfo) HeadSHA(ctx context.Context) (string, error) {
	out, code, err := info.runExit(ctx, nil, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// CurrentBranch returns the checked-out branch, or "" when detached.
func (info *RepoInfo) CurrentBranch(ctx context.Context) (string, error) {
	out, code, err := info.runExit(ctx, nil, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// StagePaths stages exactly the named paths.
//
// The -A form makes a deletion stage too, while the explicit path list keeps
// the operation from touching anything the caller did not name. It is
// deliberately not `git add -A` with an empty pathspec.
func (info *RepoInfo) StagePaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("vcs: no paths to stage")
	}
	args := append([]string{"--literal-pathspecs", "add", "-A", "--"}, paths...)
	_, err := info.run(ctx, args...)
	return err
}

// UnstagePaths removes exactly the named paths from the index without touching
// the worktree.
//
// It exists for one narrow failure: StagingPaths failed after partially moving
// the index, and /commit must leave the repository at the boundary the human
// approved (a clean index in worktree mode) rather than commit a partial
// subset on the next attempt. A pre-existing staged change is never removed by
// this path because worktree-mode staging is only entered with a clean index.
func (info *RepoInfo) UnstagePaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"--literal-pathspecs", "reset", "-q", "--"}, paths...)
	if _, code, err := info.runExit(ctx, nil, args...); err != nil {
		return err
	} else if code == 0 {
		return nil
	}
	// An unborn branch has no HEAD to reset against. Removing the entries is
	// the same index state for a first commit. On a born branch, never fall
	// back to removing entries: that could turn a cleanup into a deletion.
	head, err := info.HeadSHA(ctx)
	if err != nil {
		return err
	}
	if head != "" {
		return fmt.Errorf("vcs: could not unstage the approved paths")
	}
	remove := append([]string{"--literal-pathspecs", "rm", "--cached", "-q", "--ignore-unmatch", "--"}, paths...)
	_, err = info.run(ctx, remove...)
	return err
}

// StagedPaths lists index paths relative to HEAD.
//
// Rename detection is disabled so a rename appears as its deletion plus its
// addition, which is the same path set the caller approved.
func (info *RepoInfo) StagedPaths(ctx context.Context) ([]string, error) {
	out, err := info.run(ctx, "diff", "--cached", "--name-only", "--no-renames", "-z")
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// IndexHashes returns the blob hash recorded in the index for each tracked
// path. A path that is not tracked (an untracked addition, or a deletion that
// has reached the index) is absent from the result.
func (info *RepoInfo) IndexHashes(ctx context.Context, paths []string) (map[string]string, error) {
	hashes := make(map[string]string, len(paths))
	if len(paths) == 0 {
		return hashes, nil
	}
	args := append([]string{"--literal-pathspecs", "ls-files", "--stage", "-z", "--"}, paths...)
	out, err := info.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	for _, entry := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 2 {
			continue
		}
		hashes[path] = fields[1]
	}
	return hashes, nil
}

// HashPath returns the git blob hash of a worktree path, or found=false when
// the path does not exist (a deletion).
//
// Regular files are hashed with their path so clean/smudge filters apply
// exactly as they would during `git add`. A symlink is hashed from its link
// target bytes, matching how git stores a symlink blob.
func (info *RepoInfo) HashPath(ctx context.Context, path string) (hash string, found bool, err error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("vcs: path %q escapes the repository", path)
	}
	abs := filepath.Join(info.Root, clean)
	fi, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("vcs: stat %q: %w", path, err)
	}
	if fi.IsDir() {
		return "", false, fmt.Errorf("vcs: %q is a directory; submodule and nested-repository changes are not supported", path)
	}
	if !fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0 {
		return "", false, fmt.Errorf("vcs: %q is not a regular file", path)
	}

	var (
		data []byte
		args []string
	)
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return "", false, fmt.Errorf("vcs: read symlink %q: %w", path, err)
		}
		data = []byte(target)
		// A symlink's target is stored verbatim; no content filter applies.
		args = []string{"hash-object", "--stdin"}
	} else {
		// Let git read the file itself: a large worktree file must not be
		// duplicated in memory merely to hash it.
		args = []string{"hash-object", "--path=" + path, "--", path}
	}
	var out string
	if data != nil {
		out, err = info.runInput(ctx, data, args...)
	} else {
		out, err = info.run(ctx, args...)
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// IndexTree writes the current index as a tree object and returns its SHA.
//
// It is plumbing used after staging to pin exactly what a commit must contain:
// comparing this tree with the new commit's tree closes the window between
// verification and the commit itself.
func (info *RepoInfo) IndexTree(ctx context.Context) (string, error) {
	out, err := info.run(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitTree returns the tree SHA of one commit.
func (info *RepoInfo) CommitTree(ctx context.Context, rev string) (string, error) {
	if strings.TrimSpace(rev) == "" {
		return "", fmt.Errorf("vcs: commit revision is required")
	}
	out, err := info.run(ctx, "rev-parse", rev+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitMessage creates one commit with exactly the supplied message and
// returns its SHA.
func (info *RepoInfo) CommitMessage(ctx context.Context, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("vcs: commit message is required")
	}
	if _, err := info.run(ctx, "commit", "-m", message); err != nil {
		return "", err
	}
	sha, err := info.HeadSHA(ctx)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "", fmt.Errorf("vcs: commit succeeded but HEAD does not resolve")
	}
	return sha, nil
}

// Summary returns the staged/unstaged/untracked counts.
//
// Counts are **files**, not hunks: a status line shows how many files changed,
// and a file that is both staged and modified is counted once in each bucket.
func (info *RepoInfo) Summary(ctx context.Context) (Summary, error) {
	out, err := info.StatusPorcelain(ctx, StatusOptions{Untracked: "normal"})
	if err != nil {
		return Summary{}, err
	}
	return parsePorcelainSummary(out), nil
}

// parsePorcelainSummary counts files by porcelain status code.
//
// The code is two columns: index status and worktree status. "??" is untracked,
// anything in the first column is staged, anything in the second is unstaged.
func parsePorcelainSummary(output string) Summary {
	var summary Summary
	for _, entry := range strings.Split(output, "\x00") {
		entry = strings.TrimRight(entry, "\n")
		if len(entry) < 2 {
			continue
		}
		index, worktree := entry[0], entry[1]
		switch {
		case index == '?' && worktree == '?':
			summary.Untracked++
		case index != ' ' && index != '?':
			summary.Staged++
		}
		if worktree != ' ' && worktree != '?' {
			summary.Unstaged++
		}
	}
	return summary
}

// Numstat returns per-file line counts.
func (info *RepoInfo) Numstat(ctx context.Context, opts DiffOptions) ([]NumstatEntry, error) {
	args := []string{"--literal-pathspecs", "diff", "--numstat"}
	if opts.Cached {
		args = append(args, "--cached")
	}
	if opts.Base != "" {
		args = append(args, opts.Base)
		if opts.Head != "" {
			args = append(args, opts.Head)
		}
	}
	if len(opts.Files) > 0 {
		args = append(args, "--")
		args = append(args, opts.Files...)
	}

	out, err := info.run(ctx, args...)
	if err != nil {
		return nil, err
	}

	var entries []NumstatEntry
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		entry := NumstatEntry{Path: fields[2]}
		entry.Added = parseCount(fields[0])
		entry.Removed = parseCount(fields[1])
		entries = append(entries, entry)
	}
	return entries, nil
}

// parseCount converts a numstat field, returning nil for a binary file.
func parseCount(field string) *int {
	field = strings.TrimSpace(field)
	if field == "-" {
		return nil
	}
	value, err := strconv.Atoi(field)
	if err != nil {
		return nil
	}
	return &value
}

// ChangedFiles lists paths that differ, relative to the repository root.
func (info *RepoInfo) ChangedFiles(ctx context.Context, opts DiffOptions) ([]string, error) {
	args := []string{"--literal-pathspecs", "diff", "--name-only"}
	if opts.Cached {
		args = append(args, "--cached")
	}
	if opts.Base != "" {
		args = append(args, opts.Base)
		if opts.Head != "" {
			args = append(args, opts.Head)
		}
	}
	out, err := info.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// LogOnelines returns the most recent n commits as one-liners.
func (info *RepoInfo) LogOnelines(ctx context.Context, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	out, err := info.run(ctx, "log", "--oneline", fmt.Sprintf("-%d", n))
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// RecentCommits returns up to n commits, newest first, as style evidence.
//
// It is one process invocation rather than n CommitDetails calls: /commit reads
// history to learn the repository's message conventions, not to reconstruct any
// single commit. An unborn branch has no history and is not an error, so a fresh
// repository reports an empty slice instead of a failed inspection.
func (info *RepoInfo) RecentCommits(ctx context.Context, n int) ([]CommitRecord, error) {
	if n <= 0 {
		return nil, nil
	}
	if n > 64 {
		n = 64
	}
	const (
		field  = "\x1f"
		record = "\x1e"
	)
	out, code, err := info.runExit(ctx, nil, "log", fmt.Sprintf("-%d", n),
		"--format=%H"+field+"%s"+field+"%b"+record)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		// An empty repository has no HEAD to log. That is a repository fact,
		// not an inspection failure.
		return nil, nil
	}
	var commits []CommitRecord
	for _, chunk := range strings.Split(out, record) {
		chunk = strings.TrimLeft(chunk, "\n")
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		parts := strings.SplitN(chunk, field, 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, CommitRecord{
			SHA:     strings.TrimSpace(parts[0]),
			Subject: strings.TrimSpace(parts[1]),
			Body:    strings.TrimSpace(parts[2]),
		})
	}
	return commits, nil
}

// Upstream returns the configured upstream ref, or "" when the branch has none.
//
// Branch/base context is evidence for /commit's classification, not a condition
// for proposing: a local-only branch is a normal state, so a missing upstream is
// reported as empty rather than as an error.
func (info *RepoInfo) Upstream(ctx context.Context) (string, error) {
	out, code, err := info.runExit(ctx, nil, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// CommitDetails returns one commit's metadata and message.
func (info *RepoInfo) CommitDetails(ctx context.Context, rev string) (CommitDetails, error) {
	if strings.TrimSpace(rev) == "" {
		return CommitDetails{}, fmt.Errorf("vcs: commit revision is required")
	}
	// A field separator that cannot appear in a message or an identity.
	const sep = "\x1f"
	out, err := info.run(ctx, "show", "-s",
		"--format=%H"+sep+"%P"+sep+"%an <%ae>"+sep+"%ad"+sep+"%B", rev)
	if err != nil {
		return CommitDetails{}, err
	}
	parts := strings.SplitN(out, sep, 5)
	if len(parts) != 5 {
		return CommitDetails{}, fmt.Errorf("vcs: unexpected commit format for %q", rev)
	}

	return CommitDetails{
		SHA:     strings.TrimSpace(parts[0]),
		Parents: strings.Fields(parts[1]),
		Author:  strings.TrimSpace(parts[2]),
		Date:    strings.TrimSpace(parts[3]),
		Message: strings.TrimRight(parts[4], "\n"),
	}, nil
}

// LsFiles lists tracked files, relative to the repository root.
func (info *RepoInfo) LsFiles(ctx context.Context, excludeStandard bool) ([]string, error) {
	args := []string{"ls-files", "-z"}
	if excludeStandard {
		args = append(args, "--exclude-standard")
	}
	out, err := info.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// run executes a git command inside the repository.
func (info *RepoInfo) run(ctx context.Context, args ...string) (string, error) {
	return info.runInput(ctx, nil, args...)
}

// runInput executes a git command with optional stdin bytes.
func (info *RepoInfo) runInput(ctx context.Context, input []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = info.Root
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("vcs: git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.String(), nil
}

// runExit executes a git command and preserves a non-zero exit status as data.
//
// Callers that ask a yes/no question (does HEAD resolve?) must distinguish "the
// answer is no" from "git could not run at all"; only the latter is an error.
func (info *RepoInfo) runExit(ctx context.Context, input []byte, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = info.Root
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), exitErr.ExitCode(), nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		message = err.Error()
	}
	return "", -1, fmt.Errorf("vcs: git %s: %s", strings.Join(args, " "), message)
}

// splitLines splits on newlines and drops empty entries.
func splitLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimRight(line, "\r"); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// splitNUL splits on NUL, which is how git separates paths safely.
func splitNUL(out string) []string {
	var out2 []string
	for _, entry := range strings.Split(out, "\x00") {
		if entry != "" {
			out2 = append(out2, entry)
		}
	}
	return out2
}
