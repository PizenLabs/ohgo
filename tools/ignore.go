package tools

import "github.com/PizenLabs/ohgo/internal/ignore"

// ignorer is the tools package's view of the shared ignore matcher.
//
// The matcher itself lives in internal/ignore because workspace recovery must
// apply exactly the same rules when it decides which paths belong to the
// workspace. A second implementation would be a second answer to "is this file
// part of the repository?", and the two surfaces disagreeing is a correctness
// bug, not a cosmetic one.
type ignorer struct {
	matcher *ignore.Matcher
}

// newIgnorer loads every .gitignore under root.
func newIgnorer(root string, enabled bool) *ignorer {
	return &ignorer{matcher: ignore.New(root, enabled)}
}

// ignored reports whether a root-relative path is excluded.
func (ig *ignorer) ignored(rel string, isDir bool) bool {
	if ig == nil {
		return false
	}
	return ig.matcher.Ignored(rel, isDir)
}

// isHidden reports whether a name is a dotfile or dot-directory.
func isHidden(name string) bool { return ignore.IsHidden(name) }
