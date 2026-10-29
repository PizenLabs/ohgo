// Package vcs reads and drives version-control repositories.
//
// The original discovers a repository by walking the filesystem and resolving
// `.git` entries by hand, including linked worktrees and reftable repos, and
// only shells out for operations that need a real backend. The Go port keeps
// that split: discovery is pure filesystem work and fully testable, while
// status/diff/log go through the git CLI.
package vcs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind is the backend backing a working directory.
type Kind string

// Supported backends.
const (
	// KindGit is a git repository, possibly a linked worktree.
	KindGit Kind = "git"
	// KindJj is a Jujutsu workspace.
	KindJj Kind = "jj"
)

// Summary is the status counts a status line shows.
//
// Jujutsu has no index, so its Staged is always zero.
type Summary struct {
	// Staged counts files with index changes.
	Staged int
	// Unstaged counts files with worktree modifications.
	Unstaged int
	// Untracked counts gitignore-filtered untracked files.
	Untracked int
}

// Total returns the number of changed files, ignoring the category.
func (s Summary) Total() int { return s.Staged + s.Unstaged + s.Untracked }

// Clean reports whether the working tree has nothing to report.
func (s Summary) Clean() bool { return s.Total() == 0 }

// RepoInfo is a repository discovered by walking the filesystem.
//
// Every path is absolute. CommonDir distinguishes a primary checkout, where it
// equals GitDir, from a linked worktree, where it points at the shared
// repository's object store.
type RepoInfo struct {
	// Root is the checkout root: the directory containing the .git entry.
	Root string
	// GitEntry is the .git entry itself: a directory, or a pointer file for a
	// linked worktree.
	GitEntry string
	// GitDir is the resolved git directory, worktree-private for a linked
	// worktree.
	GitDir string
	// CommonDir is the shared object store.
	CommonDir string
	// HeadPath is the HEAD file inside GitDir.
	HeadPath string
	// IsReftable reports whether refs use the reftable format.
	//
	// Reftable repos route ref reads through the git CLI, so this is a signal
	// to callers that direct file reads would return stale or no data.
	IsReftable bool
}

// HeadState is what HEAD points at.
type HeadState struct {
	// RefName is the full ref name, e.g. "refs/heads/main", for a symbolic HEAD.
	RefName string
	// Branch is the short name when the ref lives under refs/heads/.
	Branch string
	// Commit is the SHA the ref resolves to, or "" for an unborn branch.
	Commit string
	// Detached reports whether HEAD points directly at a commit.
	Detached bool
}

// Discover finds the repository containing dir, or reports that none exists.
//
// The walk stops at the first directory holding a .git entry, which is the
// outermost repository: a nested repo inside a submodule wants its own
// status, not its parent's.
func Discover(dir string) (*RepoInfo, error) {
	current, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("vcs: resolve %q: %w", dir, err)
	}

	for {
		entry := filepath.Join(current, ".git")
		info, err := os.Lstat(entry)
		switch {
		case err == nil && info.IsDir():
			return resolveGitDir(current, entry, entry)
		case err == nil && info.Mode().IsRegular():
			// A .git *file* is a linked worktree pointer.
			return resolveWorktreePointer(current, entry)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return nil, nil
		}
		current = parent
	}
}

// resolveGitDir builds the info for a primary checkout.
func resolveGitDir(root, gitEntry, gitDir string) (*RepoInfo, error) {
	reftable, err := usesReftable(filepath.Join(gitDir, "config"))
	if err != nil {
		return nil, err
	}
	return &RepoInfo{
		Root:       root,
		GitEntry:   gitEntry,
		GitDir:     gitDir,
		CommonDir:  gitDir,
		HeadPath:   filepath.Join(gitDir, "HEAD"),
		IsReftable: reftable,
	}, nil
}

// resolveWorktreePointer reads a "gitdir: <path>" pointer file.
func resolveWorktreePointer(root, gitEntry string) (*RepoInfo, error) {
	data, err := os.ReadFile(gitEntry)
	if err != nil {
		return nil, fmt.Errorf("vcs: read %s: %w", gitEntry, err)
	}

	var target string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "gitdir:"); ok {
			target = strings.TrimSpace(after)
			break
		}
	}
	if target == "" {
		return nil, fmt.Errorf("vcs: %s has no gitdir pointer", gitEntry)
	}

	// The pointer may be relative to the checkout root.
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	gitDir, err := filepath.Abs(target)
	if err != nil {
		return nil, fmt.Errorf("vcs: resolve gitdir %q: %w", target, err)
	}

	// A linked worktree records its shared repository in commondir.
	common := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		candidate := strings.TrimSpace(string(data))
		if candidate != "" {
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(gitDir, candidate)
			}
			common = filepath.Clean(candidate)
		}
	}

	reftable, err := usesReftable(filepath.Join(common, "config"))
	if err != nil {
		return nil, err
	}
	return &RepoInfo{
		Root:       root,
		GitEntry:   gitEntry,
		GitDir:     gitDir,
		CommonDir:  common,
		HeadPath:   filepath.Join(gitDir, "HEAD"),
		IsReftable: reftable,
	}, nil
}

// usesReftable reports whether a git config enables the reftable ref backend.
//
// It is a line scan rather than an ini parse: the setting is an extension
// stanza, and a partial read is preferable to pulling in an ini dependency for
// one key.
func usesReftable(configPath string) (bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// A repository without a config still has refs; they are simply
			// not reftable.
			return false, nil
		}
		return false, fmt.Errorf("vcs: read %s: %w", configPath, err)
	}
	inExtensions := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inExtensions = strings.EqualFold(strings.Trim(trimmed, "[]"), "extensions")
			continue
		}
		if !inExtensions {
			continue
		}
		if key, value, ok := strings.Cut(trimmed, "="); ok {
			if strings.EqualFold(strings.TrimSpace(key), "refStorage") &&
				strings.EqualFold(strings.TrimSpace(value), "reftable") {
				return true, nil
			}
		}
	}
	return false, nil
}

// ReadHead resolves what HEAD points at.
//
// An unborn branch reports a Ref with an empty Commit rather than an error:
// a fresh clone is a legitimate state, and callers distinguish it by the empty
// commit.
func (info *RepoInfo) ReadHead() (HeadState, error) {
	data, err := os.ReadFile(info.HeadPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Some worktree layouts have no HEAD yet.
			return HeadState{}, nil
		}
		return HeadState{}, fmt.Errorf("vcs: read %s: %w", info.HeadPath, err)
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return HeadState{}, nil
	}
	if ref, ok := strings.CutPrefix(content, "ref: "); ok {
		refName := strings.TrimSpace(ref)
		commit := info.resolveRef(refName)
		return HeadState{
			RefName: refName,
			Branch:  shortBranch(refName),
			Commit:  commit,
		}, nil
	}
	return HeadState{Detached: true, Commit: content}, nil
}

// shortBranch strips refs/heads/ from a ref name.
func shortBranch(refName string) string {
	if after, ok := strings.CutPrefix(refName, "refs/heads/"); ok {
		return after
	}
	return ""
}

// resolveRef reads a loose or packed ref, returning "" when it does not exist.
func (info *RepoInfo) resolveRef(refName string) string {
	// Reftable repos cannot be read as files: the refs are in a table, not a
	// path. Callers detect this and fall back to the CLI.
	if info.IsReftable {
		return ""
	}

	loose := filepath.Join(info.GitDir, filepath.FromSlash(refName))
	if data, err := os.ReadFile(loose); err == nil {
		return strings.TrimSpace(string(data))
	}

	// Packed refs hold "refs/heads/main <sha>" lines.
	packed := filepath.Join(info.CommonDir, "packed-refs")
	data, err := os.ReadFile(packed)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		if sha, name, ok := strings.Cut(line, " "); ok && name == refName {
			return sha
		}
	}
	return ""
}

// Label returns a short description of HEAD for a status line.
//
// A branch name is preferred; a detached head renders as its short SHA, and an
// unborn branch as "empty" so a fresh clone shows something rather than a
// blank.
func (info *RepoInfo) Label() string {
	head, err := info.ReadHead()
	if err != nil {
		return ""
	}
	if head.Detached {
		if head.Commit == "" {
			return "detached"
		}
		return shortSHA(head.Commit)
	}
	// An unborn branch has a name but no commit, and showing the name would
	// claim a branch exists with history. `git init` produces exactly this.
	if head.Commit == "" {
		return "empty"
	}
	if head.Branch != "" {
		return head.Branch
	}
	return "empty"
}

// shortSHA truncates a SHA to the conventional 7 characters.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
