package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
)

// builtInExcludes are the directories a scan always prunes, whether or not a
// .gitignore mentions them.
//
// The original exposes this as the "ignore" list its glob operations always
// apply. Walking into node_modules or .git is the single largest cost in a
// file search, and their contents are never what the model is looking for.
var builtInExcludes = []string{
	".git",
	"node_modules",
	".venv",
	"venv",
	"__pycache__",
	".mypy_cache",
	".pytest_cache",
	".ruff_cache",
	".tox",
	"target",
	"dist",
	".next",
	".nuxt",
	".svelte-kit",
	".turbo",
	".cache",
	".parcel-cache",
	"coverage",
	".nyc_output",
	".terraform",
}

// isBuiltInExclude reports whether a directory name is always pruned.
func isBuiltInExclude(name string) bool {
	for _, candidate := range builtInExcludes {
		if name == candidate {
			return true
		}
	}
	return false
}

// GlobTool limits, matching the original tool's published contract.
const (
	// GlobDefaultLimit is the result cap when the model does not pick one.
	GlobDefaultLimit = 200
	// GlobMaxLimit is the hard ceiling: both the default and the maximum are
	// 200 in the original, so a caller cannot widen the window.
	GlobMaxLimit = 200
	// GlobTimeout bounds a scan. The original reports partial results at the
	// deadline rather than failing, so this must stay generous enough to
	// complete normal trees and short enough to keep a turn responsive.
	GlobTimeout = 5 * time.Second
)

// GlobTool finds files by glob pattern.
type GlobTool struct {
	Base
	// Root bounds every scan.
	Root string
	// Timeout overrides GlobTimeout when non-zero.
	Timeout time.Duration
}

// NewGlobTool returns a glob tool rooted at root.
func NewGlobTool(root string) *GlobTool {
	tool := &GlobTool{Root: root}
	tool.Base = NewBase("glob",
		"Find files by glob pattern and return their paths sorted by "+
			"modification time, newest first. Searches the working directory "+
			"by default; pass a subdirectory to narrow the scan.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern, e.g. \"**/*.go\" or \"src/*.ts\".",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to search, absolute or relative to the working directory.",
				},
				"hidden": map[string]any{
					"type":        "boolean",
					"description": "Include dotfiles. Defaults to true.",
				},
				"gitignore": map[string]any{
					"type":        "boolean",
					"description": "Honour .gitignore files. Defaults to true.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of files to return.",
					"minimum":     1.0,
					"maximum":     float64(GlobMaxLimit),
				},
			},
			"required": []string{"pattern"},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
}

// globResult is one match plus the data needed to rank it.
type globResult struct {
	// rel is the path relative to Root, which is what the model sees.
	rel string
	// modTime ranks results, newest first.
	modTime time.Time
}

// Execute implements agent.Tool.
func (t *GlobTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	pattern, err := strArg(args, "pattern", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if strings.TrimSpace(pattern) == "" {
		return agent.ErrorResult("pattern must not be empty")
	}
	// A pattern that is not a valid glob is a model error worth reporting
	// precisely, before spending any time scanning.
	if _, err := filepath.Match(pattern, ""); err != nil {
		return agent.ErrorResult("invalid pattern %q: %v", pattern, err)
	}

	hidden, err := boolArg(args, "hidden", true)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	useGitignore, err := boolArg(args, "gitignore", true)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	limit, err := intArg(args, "limit", GlobDefaultLimit)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if limit < 1 {
		limit = GlobDefaultLimit
	}
	if limit > GlobMaxLimit {
		limit = GlobMaxLimit
	}

	// The scan root: an explicit path narrows the walk, the pattern still
	// matches relative to Root so results stay consistent.
	scanRoot := t.Root
	if raw, ok := args["path"]; ok && raw != nil {
		path, err := strArg(args, "path", "")
		if err != nil {
			return agent.ErrorResult("%v", err)
		}
		if strings.TrimSpace(path) != "" {
			resolved, err := resolvePath(t.Root, path)
			if err != nil {
				return agent.ErrorResult("resolve path: %v", err)
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return agent.ErrorResult("stat %q: %v", path, err)
			}
			if !info.IsDir() {
				return agent.ErrorResult("%q is not a directory", path)
			}
			scanRoot = resolved
		}
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = GlobTimeout
	}
	scanCtx, cancel := context.WithTimeout(ctx.Ctx, timeout)
	defer cancel()

	ignorer := newIgnorer(t.Root, useGitignore)
	matches, timedOut := t.scan(scanCtx, scanRoot, pattern, hidden, ignorer)

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].modTime.Equal(matches[j].modTime) {
			// A stable tiebreak keeps output deterministic across runs.
			return matches[i].rel < matches[j].rel
		}
		return matches[i].modTime.After(matches[j].modTime)
	})

	// Capture the total before slicing so the truncation notice can report
	// what the scan actually found.
	totalMatches := len(matches)
	truncated := totalMatches > limit
	if truncated {
		matches = matches[:limit]
	}

	var sb strings.Builder
	for _, match := range matches {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(match.rel)
	}

	// The original's timeout notice exists because a partial result is
	// indistinguishable from a complete one. Say so instead of implying the
	// scan finished.
	if timedOut {
		if len(matches) == 0 {
			fmt.Fprintf(&sb, "\n\nglob timed out after %s before finding any matches — results are incomplete, "+
				"NOT proof of absence. Narrow the scan with a deeper \"path\".", timeout)
		} else {
			fmt.Fprintf(&sb, "\n\n[glob timed out after %s; showing %d of %d partial matches — results are incomplete]",
				timeout, len(matches), totalMatches)
		}
	} else if len(matches) == 0 {
		return agent.Result{
			Text:    "No files matched.",
			Details: map[string]any{"matches": 0, "pattern": pattern},
			Useless: true,
		}
	}

	text := sb.String()
	if truncated {
		text += fmt.Sprintf("\n\n[showing %d of %d matches — raise \"limit\" or narrow the pattern]", limit, totalMatches)
	}
	return agent.Result{
		Text: text,
		Details: map[string]any{
			"matches":  len(matches),
			"pattern":  pattern,
			"path":     relativeToRoot(t.Root, scanRoot),
			"timedOut": timedOut,
		},
	}
}

// scan walks scanRoot and collects matches.
//
// It returns the matches found and whether the deadline was hit. Pruning a
// directory requires knowing whether anything beneath it can match, so the
// walker consults the pattern per directory rather than per file.
func (t *GlobTool) scan(ctx context.Context, scanRoot, pattern string, hidden bool, ignorer *ignorer) ([]globResult, bool) {
	var (
		matches  []globResult
		timedOut bool
	)

	walkErr := filepath.WalkDir(scanRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A permission error or a vanished file is skipped, not fatal:
			// the rest of the tree is still worth reporting.
			if path == scanRoot {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			timedOut = true
			return filepath.SkipAll
		default:
		}

		rel, relErr := filepath.Rel(t.Root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if entry.IsDir() {
			if path == scanRoot {
				return nil
			}
			// Prune built-in, hidden, and ignored directories before
			// descending: walking into node_modules is the single largest
			// glob cost.
			if isBuiltInExclude(entry.Name()) {
				return filepath.SkipDir
			}
			if !hidden && isHidden(entry.Name()) {
				return filepath.SkipDir
			}
			if ignorer.ignored(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if !hidden && isHidden(entry.Name()) {
			return nil
		}
		if ignorer.ignored(rel, false) {
			return nil
		}

		// Match against both the Root-relative path and the name alone, so a
		// pattern like "*.go" matches at any depth, as the model expects.
		if matchGlob(pattern, rel) || matchGlob(pattern, entry.Name()) {
			info, statErr := entry.Info()
			var modTime time.Time
			if statErr == nil {
				modTime = info.ModTime()
			}
			matches = append(matches, globResult{rel: rel, modTime: modTime})
		}
		return nil
	})
	if walkErr != nil {
		// The walk failed at the root of the scan; the caller converts this
		// into a tool error.
		return matches, timedOut
	}
	return matches, timedOut || ctx.Err() != nil
}

// matchGlob reports whether a pattern matches a path.
//
// filepath.Match does not cross separators, so "**" is expanded into a
// recursive form: "**/" means zero or more directories, and a bare "**"
// matches any depth.
func matchGlob(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "**") {
		return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
	}

	// Split on "**" and require the literal segments around each "**" to
	// match a contiguous prefix/suffix, with the middle free to span
	// directories.
	parts := strings.Split(pattern, "**")
	if len(parts) == 2 {
		prefix, suffix := strings.TrimSuffix(parts[0], "/"), strings.TrimPrefix(parts[1], "/")
		nameParts := strings.Split(name, "/")
		if prefix == "" && suffix == "" {
			return true
		}
		if prefix == "" {
			return matchSuffix(suffix, nameParts)
		}
		if suffix == "" {
			return matchPrefix(prefix, nameParts)
		}
		return matchPrefix(prefix, nameParts) && matchSuffix(suffix, nameParts)
	}
	// More than one "**": fall back to a per-segment match that ignores the
	// recursive semantics. Rare in practice and still deterministic.
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchPrefix reports whether nameParts begins with prefix.
func matchPrefix(prefix string, nameParts []string) bool {
	parts := strings.Split(prefix, "/")
	if len(parts) > len(nameParts) {
		return false
	}
	return matchSegments(parts, nameParts[:len(parts)])
}

// matchSuffix reports whether nameParts ends with suffix.
func matchSuffix(suffix string, nameParts []string) bool {
	parts := strings.Split(suffix, "/")
	if len(parts) > len(nameParts) {
		return false
	}
	return matchSegments(parts, nameParts[len(nameParts)-len(parts):])
}

// matchSegments matches path segments one-to-one, honouring "*" within a
// segment but never letting it cross a separator.
func matchSegments(pattern, name []string) bool {
	if len(pattern) != len(name) {
		return false
	}
	for i := range pattern {
		ok, err := filepath.Match(pattern[i], name[i])
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// isHidden reports whether a name is a dotfile.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

// relativeToRoot renders a path relative to root for display.
func relativeToRoot(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && rel != "." {
		return filepath.ToSlash(rel)
	}
	return ""
}
