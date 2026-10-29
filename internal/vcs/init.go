package vcs

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultBranch is the initial branch of a repository OHGO creates. An
// existing repository keeps whatever branch it already has: only an explicit
// initialization of a new repository binds this name, and no code path may
// rename a branch that predates it.
const DefaultBranch = "main"

// Init creates a Git repository in dir with HEAD on branch.
//
// The branch is bound atomically by `git init -b`, so there is no window in
// which the repository exists on Git's compiled-in default name. An empty
// branch selects DefaultBranch.
//
// A directory that already holds its own `.git` entry is left completely
// alone: re-initialization must never change the checked-out branch of an
// existing repository. A directory merely *inside* a repository is not the
// same case; callers that walk up already know it is a repository and do not
// ask for initialization.
func Init(dir, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = DefaultBranch
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("vcs: resolve %q: %w", dir, err)
	}
	if repo, derr := Discover(abs); derr == nil && repo != nil && repo.Root == abs {
		return nil
	}

	cmd := exec.Command("git", "init", "-b", branch)
	cmd.Dir = abs
	if out, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("vcs: git init -b %s: %s", branch, message)
	}
	return nil
}
