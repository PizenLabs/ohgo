package vcs

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
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
	// Pathspecs restricts the query.
	Pathspecs []string
}

// NumstatEntry is one file's added/removed line counts.
//
// Added and Removed are nil for binary files, which git reports as "-".
type NumstatEntry struct {
	Path    string
	Added   *int
	Removed *int
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
	args := []string{"diff", "--no-ext-diff", "--no-color"}
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
	args := []string{"status", "-z", "--porcelain"}
	if opts.Untracked != "" {
		args = append(args, "--untracked-files="+opts.Untracked)
	}
	if len(opts.Pathspecs) > 0 {
		args = append(args, "--")
		args = append(args, opts.Pathspecs...)
	}
	return info.run(ctx, args...)
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
	args := []string{"diff", "--numstat"}
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
	args := []string{"diff", "--name-only"}
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
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = info.Root

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
