package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// LsTool limits.
const (
	// LsDefaultLimit caps entries in one response.
	LsDefaultLimit = 200
	// LsMaxLimit is the ceiling.
	LsMaxLimit = 500
	// LsMaxDepth bounds a recursive listing in directory levels. It exists so
	// one call can answer a topology question that otherwise costs a sequence
	// of single-directory listings, without turning the tool into a tree dump.
	LsMaxDepth = 4
)

// LsTool lists a directory, optionally recursing a bounded number of levels.
//
// The recursive mode is the read-only topology primitive for a session without
// shell access: depth 0 is a flat listing, depth N is a pre-order walk bounded
// by the same entry limit. It is not a shell escape hatch and it never follows
// directory symlinks.
type LsTool struct {
	Base
	// Root bounds every listing.
	Root string
}

// NewLsTool returns a listing tool rooted at root.
func NewLsTool(root string) *LsTool {
	tool := &LsTool{Root: root}
	tool.Base = NewBase("ls",
		"List a directory, one entry per line, with a size column for files. "+
			"Directories are listed first. A listing is inventory: it proves "+
			"names exist, not what any file implements. Set depth to recurse a "+
			"bounded number of levels; a recursive walk honours .gitignore and "+
			"prunes dependency/build directories, and says when its entry limit "+
			"stopped it early.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to list, absolute or relative to the working directory. Defaults to the working directory.",
				},
				"depth": map[string]any{
					"type":        "integer",
					"description": "Directory levels to descend. 0 lists only the target directory; 1 also lists its subdirectories; and so on. Defaults to 0.",
					"minimum":     0.0,
					"maximum":     float64(LsMaxDepth),
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of entries to return across the whole walk.",
					"minimum":     1.0,
					"maximum":     float64(LsMaxLimit),
				},
				"hidden": map[string]any{
					"type":        "boolean",
					"description": "Include dotfiles. Defaults to false.",
				},
				"gitignore": map[string]any{
					"type":        "boolean",
					"description": "Honour .gitignore and prune dependency/build directories while recursing (depth > 0). Defaults to true.",
				},
			},
			"required": []string{},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
}

// lsEntry is one observed directory entry, keyed by its path relative to the
// listing target so a recursive description keeps its directory boundaries.
type lsEntry struct {
	rel     string
	isDir   bool
	size    int64
	symlink string
}

// Execute implements agent.Tool.
func (t *LsTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	target := t.Root
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
			target = resolved
		}
	}

	hidden, err := boolArg(args, "hidden", false)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	limit, err := intArg(args, "limit", LsDefaultLimit)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if limit < 1 {
		limit = LsDefaultLimit
	}
	if limit > LsMaxLimit {
		limit = LsMaxLimit
	}
	depth, err := intArg(args, "depth", 0)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if depth < 0 {
		depth = 0
	}
	if depth > LsMaxDepth {
		depth = LsMaxDepth
	}
	useGitignore, err := boolArg(args, "gitignore", true)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}

	if _, err := os.ReadDir(target); err != nil {
		// A listing of a path that does not exist is the answer to the probe.
		// Without this, an exploratory ls of a guessed directory looked like
		// an unrecovered execution failure and vetoed an otherwise complete
		// answer.
		if os.IsNotExist(err) {
			return absenceResult("read directory: %v", err)
		}
		return agent.ErrorResult("read directory: %v", err)
	}

	// A flat listing stays literal: it shows what the directory holds, even a
	// dependency directory the caller may be looking for. A recursive walk is a
	// topology observation, so it prunes repository-external and ignored
	// subtrees that would otherwise consume the entry limit.
	var ign *ignorer
	if depth > 0 && useGitignore {
		ign = newIgnorer(t.Root, true)
	}

	var entries []lsEntry
	truncated := false
	var visit func(dir string, level int)
	visit = func(dir string, level int) {
		if len(entries) >= limit {
			truncated = true
			return
		}
		items := readSortedEntries(dir, hidden)
		for _, item := range items {
			if len(entries) >= limit {
				truncated = true
				return
			}
			if depth > 0 {
				if item.isDir && (isBuiltInExclude(item.name) || item.name == ".git") {
					continue
				}
				if ign != nil && ign.ignored(rel(t.Root, item.path), item.isDir) {
					continue
				}
			}
			relPath, relErr := filepath.Rel(target, item.path)
			if relErr != nil {
				relPath = item.name
			}
			entries = append(entries, lsEntry{
				rel:     filepath.ToSlash(relPath),
				isDir:   item.isDir,
				size:    item.size,
				symlink: item.symlink,
			})
			if item.isDir && level < depth {
				visit(item.path, level+1)
			}
		}
	}
	visit(target, 0)

	var sb strings.Builder
	for _, item := range entries {
		name := item.rel
		if item.isDir {
			name += "/"
		}
		switch {
		case item.symlink != "":
			fmt.Fprintf(&sb, "%s -> %s\n", name, item.symlink)
		case item.isDir:
			fmt.Fprintf(&sb, "%s\n", name)
		default:
			fmt.Fprintf(&sb, "%8d  %s\n", item.size, name)
		}
	}

	header := relativeToRoot(t.Root, target)
	if header == "" {
		header = "."
	}
	text := header + "\n" + strings.TrimRight(sb.String(), "\n")
	if truncated {
		if depth > 0 {
			text += fmt.Sprintf("\n\n[limit reached at %d entries; the walk stopped early. Narrow \"path\", raise \"limit\", or lower \"depth\"]", len(entries))
		} else {
			text += fmt.Sprintf("\n\n[showing %d of more entries — raise \"limit\"]", len(entries))
		}
	}

	return agent.Result{
		Text: text,
		Details: map[string]any{
			"path":      header,
			"entries":   len(entries),
			"depth":     depth,
			"truncated": truncated,
		},
	}
}

// listedEntry is one directory member prepared for sorting.
type listedEntry struct {
	name    string
	path    string
	isDir   bool
	size    int64
	symlink string
}

// readSortedEntries returns the visible members of dir, directories first and
// then by name, so a caller can early-exit on the first file boundary.
func readSortedEntries(dir string, hidden bool) []listedEntry {
	raw, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]listedEntry, 0, len(raw))
	for _, e := range raw {
		if !hidden && isHidden(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		item := listedEntry{
			name:  e.Name(),
			path:  filepath.Join(dir, e.Name()),
			isDir: e.IsDir(),
			size:  info.Size(),
		}
		if e.Type()&os.ModeSymlink != 0 {
			if linkTarget, err := os.Readlink(item.path); err == nil {
				item.symlink = linkTarget
			}
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir
		}
		return out[i].name < out[j].name
	})
	return out
}
