package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// MentionPath is one workspace entry offered for "@" completion by the UI.
type MentionPath struct {
	// Path is the slash path relative to the workspace root.
	Path string
	// Dir reports whether the entry is a directory.
	Dir bool
}

// Mention bounds.
const (
	// MentionDefaultLimit caps the roster offered for "@" completion. The list
	// is a discovery aid, not a file index: a bounded, deterministic roster is
	// enough to complete a path, and an unbounded walk would stall the
	// composer on a large tree.
	MentionDefaultLimit = 2000
	// MentionTimeout bounds the walk. A partial roster is still useful — the
	// UI never claims it is complete — so the deadline truncates rather than
	// fails.
	MentionTimeout = 750 * time.Millisecond
)

// MentionFileMaxBytes caps the size of one file attached as "@" context. A
// reference is context, not a dump: a larger file is refused with an explicit
// error so the turn still proceeds rather than attaching a giant blob.
const MentionFileMaxBytes = 256 * 1024

// ReadWorkspaceFile reads one workspace-relative file for "@" context
// injection.
//
// It resolves through resolvePath, so a reference can never escape root by
// traversal or a symlink, and it refuses directories and files above
// MentionFileMaxBytes with an explicit error rather than returning partial
// content. Errors name the path as written, so the caller can cite the source
// the user typed rather than a resolved path they never saw.
func ReadWorkspaceFile(root, path string) (string, error) {
	resolved, err := resolvePath(root, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory", path)
	}
	if info.Size() > MentionFileMaxBytes {
		return "", fmt.Errorf("%q is %d bytes, over the %d-byte context limit", path, info.Size(), MentionFileMaxBytes)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WorkspaceMentions walks root and returns up to limit entries for "@"
// completion, directories and files alike.
//
// It applies the same built-in, hidden, and .gitignore pruning the search
// tools use, so a reference surface cannot suggest a path the read tool would
// refuse. Paths are slash-relative to root and sorted, so the roster is
// deterministic across calls and independent of directory-read order.
func WorkspaceMentions(ctx context.Context, root string, limit int) []MentionPath {
	if limit <= 0 {
		limit = MentionDefaultLimit
	}
	ctx, cancel := context.WithTimeout(ctx, MentionTimeout)
	defer cancel()

	ignorer := newIgnorer(root, true)
	out := make([]MentionPath, 0, 64)
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A permission error or a vanished file is skipped, not fatal:
			// the rest of the tree is still worth offering.
			if path == root {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return filepath.SkipAll
		default:
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		name := entry.Name()
		if entry.IsDir() {
			if isBuiltInExclude(name) || isHidden(name) || ignorer.ignored(rel, true) {
				return filepath.SkipDir
			}
		} else if isHidden(name) || ignorer.ignored(rel, false) {
			return nil
		}
		out = append(out, MentionPath{Path: rel, Dir: entry.IsDir()})
		if len(out) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
