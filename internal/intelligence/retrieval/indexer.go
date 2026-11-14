package retrieval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// Source is the inventory seam. *ast.Index implements it; a test can supply a
// deterministic stand-in.
type Source interface {
	Inventory(ctx context.Context, query ast.InventoryQuery) (ast.Inventory, error)
}

// CacheState reports how the persisted index was obtained.
type CacheState string

// Cache outcomes.
const (
	// CacheDisabled means no cache directory was configured.
	CacheDisabled CacheState = "disabled"
	// CacheMiss means no prior index existed.
	CacheMiss CacheState = "miss"
	// CacheHit means a prior index was loaded and validated.
	CacheHit CacheState = "hit"
	// CacheInvalid means a prior index existed but was rejected.
	CacheInvalid CacheState = "invalid"
	// CacheActive means the caller's in-memory index was reconciled without a
	// disk read.
	CacheActive CacheState = "active"
)

// Indexer owns one repository's retrieval index.
//
// The root and cache directory are supplied by the caller (composition), so
// this package never imports the control plane or state ownership.
type Indexer struct {
	// Root is the repository root.
	Root string
	// CacheDir is where the index is persisted. Empty disables persistence.
	CacheDir string
	// Options bounds the index.
	Options Options
	// ReadFile is the filesystem seam. Nil uses os.ReadFile semantics with a
	// bounded prefix.
	ReadFile func(string) ([]byte, error)
}

// NewIndexer returns an indexer over root.
func NewIndexer(root, cacheDir string, opts Options) *Indexer {
	return &Indexer{Root: root, CacheDir: cacheDir, Options: opts.normalize()}
}

// SyncStats reports one reconciliation with the repository snapshot.
type SyncStats struct {
	// Cache reports whether a persisted index was reused.
	Cache CacheState
	// CacheReason explains a rejection.
	CacheReason string
	// Documents is the resulting index size.
	Documents int
	// Added, Updated, Removed, and Reused classify the per-document work.
	Added   int
	Updated int
	Removed int
	Reused  int
	// BytesRead is the source volume tokenized this sync.
	BytesRead int64
	// Truncated reports a bound that applied (files or per-file bytes).
	Truncated bool
	// Issues are bounded, human-readable degradations.
	Issues []string
}

const maxSyncIssues = 8

// Sync loads any persisted index, reconciles it with the current snapshot, and
// persists the result.
func (ix *Indexer) Sync(ctx context.Context, source Source) (*Index, SyncStats, error) {
	return ix.sync(ctx, source, nil)
}

// Refresh reconciles a live in-memory index with the current snapshot without
// reading the cache file back from disk.
//
// The repository snapshot identity still comes from the AST inventory, so
// refreshing cannot hide a changed tree. It only avoids decoding a large index
// the caller already holds.
func (ix *Indexer) Refresh(ctx context.Context, source Source, current *Index) (*Index, SyncStats, error) {
	return ix.sync(ctx, source, current)
}

// sync reconciles the index with the current snapshot and persists it.
//
// It reads only new or changed files: an unchanged file is recognized by its
// size and modification time, and an unchanged path is never re-tokenized. A
// stale persisted document is removed; a persisted document that still exists
// but was not selected this pass is retained rather than silently dropped.
func (ix *Indexer) sync(ctx context.Context, source Source, current *Index) (*Index, SyncStats, error) {
	if source == nil {
		return nil, SyncStats{}, errors.New("retrieval: nil source")
	}
	root, err := filepath.Abs(ix.Root)
	if err != nil {
		return nil, SyncStats{}, fmt.Errorf("retrieval: resolve root: %w", err)
	}
	opts := ix.Options.normalize()
	stats := SyncStats{Cache: CacheDisabled}
	index := NewIndex(opts)
	switch {
	case current != nil && current.Options() == opts:
		index = current
		stats.Cache = CacheActive
	case ix.CacheDir != "":
		loaded, loadErr := Load(ix.CacheDir, root, opts)
		switch {
		case loadErr == nil:
			index = loaded
			stats.Cache = CacheHit
		case errors.Is(loadErr, os.ErrNotExist):
			stats.Cache = CacheMiss
		default:
			stats.Cache = CacheInvalid
			stats.CacheReason = loadErr.Error()
		}
	}

	inventory, err := source.Inventory(ctx, ast.InventoryQuery{MaxFiles: opts.MaxDocs})
	if err != nil {
		return nil, stats, fmt.Errorf("retrieval: inventory: %w", err)
	}
	if inventory.FilesTruncated {
		stats.Truncated = true
	}

	selected := make(map[string]ast.FileInfo, len(inventory.Files))
	updates := make([]Document, 0, 16)
	for _, row := range inventory.Files {
		if !retrievable(row.Path, row.Kind) {
			continue
		}
		rel := path.Clean(strings.TrimPrefix(filepath.ToSlash(row.Path), "./"))
		if rel == "." || rel == "" {
			continue
		}
		selected[rel] = row
	}
	paths := make([]string, 0, len(selected))
	for rel := range selected {
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	for position, rel := range paths {
		if position%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, stats, err
			}
		}
		row := selected[rel]
		full := filepath.Join(root, filepath.FromSlash(rel))
		// The AST inventory already observed size and mtime. Statting again
		// doubles the per-call filesystem work; only a source that does not
		// carry the signature (a test stand-in or an older adapter) falls back.
		size, modTime := row.Size, row.ModUnixNano
		if modTime == 0 {
			info, statErr := os.Stat(full)
			if statErr != nil {
				stats.Issues = appendIssue(stats.Issues, fmt.Sprintf("%s: %v", rel, statErr))
				continue
			}
			size, modTime = info.Size(), info.ModTime().UnixNano()
		}
		if cachedSize, cachedMod, ok := index.Signature(rel); ok &&
			cachedSize == size && cachedMod == modTime {
			stats.Reused++
			continue
		}
		content, truncated, readErr := ix.read(full, opts.MaxDocBytes)
		if readErr != nil {
			stats.Issues = appendIssue(stats.Issues, fmt.Sprintf("%s: %v", rel, readErr))
			continue
		}
		if truncated {
			stats.Truncated = true
		}
		stats.BytesRead += int64(len(content))
		doc := DocumentFromSource(rel, row.Language, string(row.Kind), row.Test, size, modTime, content, opts)
		doc.Generated = generatedPath(rel)
		if _, _, ok := index.Signature(rel); ok {
			stats.Updated++
		} else {
			stats.Added++
		}
		updates = append(updates, doc)
	}

	removals := make([]string, 0, 4)
	inventoryPaths := make(map[string]struct{}, len(paths))
	for _, rel := range paths {
		inventoryPaths[rel] = struct{}{}
	}
	for _, rel := range index.Paths() {
		if _, ok := inventoryPaths[rel]; ok {
			continue
		}
		// A truncated inventory must not delete documents that still exist on
		// disk; only a genuinely missing file is removed. A complete inventory
		// already proved absence, so it needs no extra stats.
		if inventory.FilesTruncated {
			if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); statErr == nil {
				continue
			}
		}
		removals = append(removals, rel)
		stats.Removed++
	}

	if err := index.Apply(updates, removals); err != nil {
		return nil, stats, err
	}
	stats.Documents = index.Stats().Documents
	if ix.CacheDir != "" {
		if err := index.Save(ix.CacheDir, root); err != nil {
			stats.Issues = appendIssue(stats.Issues, "cache: "+err.Error())
		}
	}
	return index, stats, nil
}

// read returns at most max bytes of one file.
func (ix *Indexer) read(full string, max int64) ([]byte, bool, error) {
	if ix.ReadFile != nil {
		content, err := ix.ReadFile(full)
		if err != nil {
			return nil, false, err
		}
		if int64(len(content)) > max {
			return content[:max], true, nil
		}
		return content, false, nil
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(content)) > max {
		return content[:max], true, nil
	}
	return content, false, nil
}

// retrievable reports whether a file carries text worth indexing. The AST
// inventory classifies an unsupported language as FileOther; retrieval is
// lexical, so it can still index a Python or Ruby file this build cannot
// parse, as long as the extension is a known text source.
func retrievable(rel string, kind ast.FileKind) bool {
	if kind != ast.FileOther {
		return true
	}
	switch strings.ToLower(path.Ext(rel)) {
	case ".py", ".pyi", ".rb", ".php", ".swift", ".scala", ".sc",
		".sh", ".bash", ".zsh", ".fish", ".sql", ".lua", ".pl", ".r",
		".ex", ".exs", ".erl", ".hs", ".ml", ".mli", ".clj", ".cljs",
		".vue", ".svelte", ".astro", ".dart", ".zig", ".nim", ".jl",
		".tpl", ".tmpl", ".html", ".css", ".scss", ".less", ".xml",
		".toml", ".ini", ".cfg", ".conf":
		return true
	}
	return false
}

// appendIssue keeps the issue list bounded and ordered by first occurrence.
func appendIssue(issues []string, issue string) []string {
	if len(issues) >= maxSyncIssues {
		return issues
	}
	return append(issues, issue)
}

// generatedPath identifies output that should not dominate retrieval. It is a
// path-convention heuristic, disclosed as such: no adapter reports a
// generation relationship in the current snapshot.
func generatedPath(rel string) bool {
	base := path.Base(rel)
	switch {
	case strings.HasSuffix(base, ".pb.go"),
		strings.HasSuffix(base, "_generated.go"),
		strings.HasPrefix(base, "zz_generated"),
		strings.HasSuffix(base, ".min.js"),
		strings.HasSuffix(base, ".min.css"),
		strings.HasSuffix(base, ".g.dart"):
		return true
	}
	return strings.Contains(rel, "/vendor/") || strings.HasPrefix(rel, "vendor/")
}
