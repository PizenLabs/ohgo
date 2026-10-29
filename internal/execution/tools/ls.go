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
)

// LsTool lists a directory.
type LsTool struct {
	Base
	// Root bounds every listing.
	Root string
}

// NewLsTool returns a listing tool rooted at root.
func NewLsTool(root string) *LsTool {
	tool := &LsTool{Root: root}
	tool.Base = NewBase("ls",
		"List the contents of a directory, one entry per line, with a size "+
			"column for files. Directories are listed first.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to list, absolute or relative to the working directory. Defaults to the working directory.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of entries to return.",
					"minimum":     1.0,
					"maximum":     float64(LsMaxLimit),
				},
				"hidden": map[string]any{
					"type":        "boolean",
					"description": "Include dotfiles. Defaults to false.",
				},
			},
			"required": []string{},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
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

	entries, err := os.ReadDir(target)
	if err != nil {
		return agent.ErrorResult("read directory: %v", err)
	}

	// Only entries whose parent is inside Root are shown, and hidden entries
	// are filtered before sorting so the limit counts what the user sees.
	type entry struct {
		name    string
		isDir   bool
		size    int64
		isLink  bool
		symlink string
	}
	filtered := make([]entry, 0, len(entries))
	for _, e := range entries {
		if !hidden && isHidden(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		item := entry{name: e.Name(), isDir: e.IsDir(), size: info.Size()}
		if e.Type()&os.ModeSymlink != 0 {
			item.isLink = true
			if linkTarget, err := os.Readlink(filepath.Join(target, e.Name())); err == nil {
				item.symlink = linkTarget
			}
		}
		filtered = append(filtered, item)
	}

	// Directories first, then names, so the ordering is predictable and a
	// caller can early-exit on the first directory boundary.
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].isDir != filtered[j].isDir {
			return filtered[i].isDir
		}
		return filtered[i].name < filtered[j].name
	})

	truncated := len(filtered) > limit
	if truncated {
		filtered = filtered[:limit]
	}

	var sb strings.Builder
	for _, item := range filtered {
		if item.isLink {
			fmt.Fprintf(&sb, "%s -> %s\n", item.name, item.symlink)
			continue
		}
		if item.isDir {
			fmt.Fprintf(&sb, "%s/\n", item.name)
			continue
		}
		fmt.Fprintf(&sb, "%8d  %s\n", item.size, item.name)
	}

	header := relativeToRoot(t.Root, target)
	if header == "" {
		header = "."
	}
	text := header + "\n" + strings.TrimRight(sb.String(), "\n")
	if truncated {
		text += fmt.Sprintf("\n\n[showing %d of more entries — raise \"limit\"]", limit)
	}

	return agent.Result{
		Text: text,
		Details: map[string]any{
			"path":    header,
			"entries": len(filtered),
		},
	}
}
