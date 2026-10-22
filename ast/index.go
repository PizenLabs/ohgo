package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// fileSig is the freshness fingerprint for one source file.
type fileSig struct {
	size  int64
	mtime int64
}

// moduleSig is the freshness fingerprint for go.mod. It is separate from the
// Go-file snapshot because the symbol index deliberately ignores non-Go
// changes; a module path change still invalidates every graph edge, so graph
// queries must observe it.
type moduleSig struct {
	exists bool
	size   int64
	mtime  int64
}

// fileRecord is the parsed result for one repository-relative file.
type fileRecord struct {
	// packageName is the package clause observed in this file.
	packageName string
	// symbols are the file's declarations, in coordinate order.
	symbols []Match
	// imports are the file's import declarations. They are relationship data,
	// never exposed through the symbol taxonomy.
	imports []Import
	err     error
}

// indexedKinds is the Phase 1 symbol taxonomy.
//
// Imports are deliberately excluded. They are declarations in the AST, but
// they are not coordinates a caller navigates to; including them makes file
// and package listings mostly import noise and lets a lookup resolve an
// import path as if it were a repository symbol.
var indexedKinds = []Kind{
	KindPackage,
	KindFunc,
	KindMethod,
	KindType,
	KindVar,
	KindConst,
}

// Index is an in-process, rebuildable symbol index for a repository root.
//
// It is derived state: the source tree remains authoritative. A missing or
// stale index is rebuilt on demand, and a rebuild failure is reported to the
// caller rather than serving coordinates from a snapshot that no longer
// matches the tree.
type Index struct {
	// Root is the absolute lexical root the coordinates are relative to.
	Root string

	// walkRoot is Root with a root-level symlink resolved. Nested symlinks
	// are still not followed by WalkDir, so only the working directory
	// itself may alias its canonical location.
	walkRoot string

	mu       sync.Mutex
	files    map[string]fileRecord
	snapshot map[string]fileSig
	// unreadable holds the repository-relative directories and files the last
	// build could not inspect. Their contents are unknown, so they are counted
	// as unparsed: a lookup must not present their absence as a fact.
	unreadable map[string]bool
	// modulePath is the module clause from go.mod, empty when absent or
	// unparseable. The graph cannot prove a module-local edge without it.
	modulePath string
	// moduleSig fingerprints go.mod so a graph query observes a module-path
	// change even though that file is not part of the Go-file snapshot.
	moduleSig moduleSig
	// graph is the package/import projection of the same parse. It is
	// rebuilt with the snapshot, never independently.
	graph *packageGraph
}

// NewIndex builds a fresh symbol index rooted at root.
func NewIndex(root string) (*Index, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("ast: resolve index root %q: %w", root, err)
	}
	walkRoot := absRoot
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		walkRoot = resolved
	}
	idx := &Index{
		Root:       absRoot,
		walkRoot:   walkRoot,
		files:      map[string]fileRecord{},
		snapshot:   map[string]fileSig{},
		unreadable: map[string]bool{},
	}
	if err := idx.build(); err != nil {
		return nil, err
	}
	return idx, nil
}

// build parses every Go file under Root and stores its symbols.
func (idx *Index) build() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.buildLocked()
}

// buildLocked is build with idx.mu held, so a freshness check and the
// subsequent rebuild observe one consistent snapshot.
func (idx *Index) buildLocked() error {
	return idx.buildLockedCtx(context.Background())
}

// buildLockedCtx is buildLocked with cancellation checks between files.
//
// A cancelled build returns the context error without publishing the partial
// maps. The previous snapshot remains in place but the caller that requested
// the refresh receives an error, so it cannot answer from stale coordinates.
func (idx *Index) buildLockedCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sigs := map[string]fileSig{}
	records := map[string]fileRecord{}
	unreadable := map[string]bool{}
	backend := GoBackend{}

	err := filepath.WalkDir(idx.walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == idx.walkRoot {
				return walkErr
			}
			unreadable[relKey(idx.walkRoot, path)] = true
			return nil
		}
		if entry.IsDir() {
			if path != idx.walkRoot && shouldSkipIndexDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".go") {
			return nil
		}
		rel := relKey(idx.walkRoot, path)
		info, err := entry.Info()
		if err != nil {
			unreadable[rel] = true
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		sig := fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
		sigs[rel] = sig

		structure, perr := backend.Parse(path, Options{Kinds: indexedKinds})
		if perr != nil {
			records[rel] = fileRecord{err: perr}
			return nil
		}
		syms := make([]Match, 0, len(structure.Symbols))
		for _, m := range structure.Symbols {
			m.Path = rel
			// The index is a coordinate store, not a source cache. Doc and
			// Signature duplicate source text; a lookup that needs them can
			// read the file at the returned line range. Dropping them keeps
			// resident memory proportional to the coordinate set.
			m.Doc = ""
			m.Signature = ""
			syms = append(syms, m)
		}
		sortSymbols(syms)
		records[rel] = fileRecord{
			packageName: structure.Package,
			symbols:     syms,
			imports:     structure.Imports,
		}
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("ast: index %q: %w", idx.Root, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// go.mod is not part of the Go-file snapshot. Read it with the same build
	// so the graph's edges and the module path it resolved against agree.
	modulePath, moduleSig := readModule(idx.walkRoot)
	graph := buildPackageGraph(records, modulePath)

	if err := ctx.Err(); err != nil {
		return err
	}
	idx.files = records
	idx.snapshot = sigs
	idx.unreadable = unreadable
	idx.modulePath = modulePath
	idx.moduleSig = moduleSig
	idx.graph = graph
	return nil
}

// readModule returns the module path from go.mod and a fingerprint of the
// file. An absent or malformed go.mod yields an empty path, which the graph
// query reports as an error rather than inventing local edges.
func readModule(root string) (string, moduleSig) {
	path := filepath.Join(root, "go.mod")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", moduleSig{}
	}
	sig := moduleSig{exists: true, size: info.Size(), mtime: info.ModTime().UnixNano()}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", sig
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "module" {
			continue
		}
		path := strings.Trim(fields[1], `"`)
		if path != "" {
			return path, sig
		}
	}
	return "", sig
}

// moduleFingerprint returns the current go.mod fingerprint.
func moduleFingerprint(root string) moduleSig {
	info, err := os.Stat(filepath.Join(root, "go.mod"))
	if err != nil || !info.Mode().IsRegular() {
		return moduleSig{}
	}
	return moduleSig{exists: true, size: info.Size(), mtime: info.ModTime().UnixNano()}
}

// relKey is the repository-relative, slash-separated key for a path under root.
func relKey(root, path string) string {
	return filepath.ToSlash(mustRelPath(root, path))
}

// shouldSkipIndexDir reports directories that are never useful to index.
//
// testdata is skipped because the Go toolchain does not treat it as package
// source; indexing it would report declarations the compiler never sees.
func shouldSkipIndexDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "testdata":
		return true
	}
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

// Stale reports whether the on-disk tree differs from the indexed snapshot.
// An inspection error is reported as stale so a caller refreshes and receives
// the error rather than trusting the old snapshot.
func (idx *Index) Stale() bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	stale, err := idx.staleLocked()
	return err != nil || stale
}

// staleLocked compares the tree to the snapshot with idx.mu held.
func (idx *Index) staleLocked() (bool, error) {
	return idx.staleLockedCtx(context.Background())
}

// staleLockedCtx is staleLocked with cancellation checks during the stat walk.
func (idx *Index) staleLockedCtx(ctx context.Context) (bool, error) {
	// Only a count is needed for deletion detection. The per-file
	// comparison below finds changes and additions; allocating a second
	// path map on every query would churn memory for no extra signal.
	current := 0
	seenUnreadable := 0
	changed := false
	err := filepath.WalkDir(idx.walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == idx.walkRoot {
				return walkErr
			}
			// A newly unreadable entry is a change; one already recorded is
			// not, so a persistently unreadable directory does not force a
			// full re-parse on every query.
			seenUnreadable++
			if !idx.unreadable[relKey(idx.walkRoot, path)] {
				changed = true
			}
			return nil
		}
		if entry.IsDir() {
			if path != idx.walkRoot && shouldSkipIndexDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".go") {
			return nil
		}
		rel := relKey(idx.walkRoot, path)
		info, err := entry.Info()
		if err != nil {
			seenUnreadable++
			if !idx.unreadable[rel] {
				changed = true
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		current++
		sig := fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
		old, ok := idx.snapshot[rel]
		if !ok || old != sig {
			changed = true
		}
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, fmt.Errorf("ast: inspect %q: %w", idx.Root, err)
	}
	if current != len(idx.snapshot) {
		changed = true
	}
	// An entry recorded as unreadable that is no longer seen (deleted or
	// renamed) must also trigger a rebuild, or its failure would linger.
	if seenUnreadable != len(idx.unreadable) {
		changed = true
	}
	return changed, nil
}

// ensureFresh rebuilds the index when the tree has changed.
//
// A rebuild error is returned, not swallowed: serving the previous snapshot
// after the source tree changed would present stale coordinates as current.
func (idx *Index) ensureFresh() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.refreshLocked()
}

// ensureFreshGraph refreshes the snapshot for a graph query.
//
// It adds the go.mod fingerprint to the Go-file check: the symbol index
// deliberately ignores non-Go changes, but a module-path change invalidates
// every graph edge. A cancellation during the stat walk or rebuild is returned
// as the context error, never as a partial success.
func (idx *Index) ensureFreshGraph(ctx context.Context) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.refreshGraphLocked(ctx, true)
}

// refreshLocked rebuilds only when the snapshot differs from the tree. The
// check and the rebuild share one lock so concurrent queries cannot each
// decide the tree changed and re-parse the repository in turn.
func (idx *Index) refreshLocked() error {
	return idx.refreshGraphLocked(context.Background(), false)
}

// refreshGraphLocked is refreshLocked with optional go.mod freshness and
// cancellation. watchModule=false preserves the frozen symbol freshness
// contract.
func (idx *Index) refreshGraphLocked(ctx context.Context, watchModule bool) error {
	stale, err := idx.staleLockedCtx(ctx)
	if err != nil {
		return err
	}
	if !stale && watchModule && moduleFingerprint(idx.walkRoot) != idx.moduleSig {
		stale = true
	}
	if !stale {
		return nil
	}
	return idx.buildLockedCtx(ctx)
}

// Symbols returns every indexed symbol in deterministic order.
func (idx *Index) Symbols() ([]Match, error) {
	if err := idx.ensureFresh(); err != nil {
		return nil, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	var out []Match
	for _, rec := range idx.files {
		out = append(out, rec.symbols...)
	}
	sortSymbols(out)
	return out, nil
}

// ParseFailures reports how many entries in the current index snapshot could
// not be indexed: files that failed to parse or read, and directories that
// could not be listed. It does not refresh the index; callers that need
// freshness run a query first.
//
// A failure is isolated to its entry: its neighbours stay searchable.
// Callers surface this count so a partial result is never mistaken for a
// complete one.
func (idx *Index) ParseFailures() int {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.parseFailuresLocked()
}

// Lookup returns symbols whose name or qualified name exactly matches name.
func (idx *Index) Lookup(name string) ([]Match, error) {
	if err := idx.ensureFresh(); err != nil {
		return nil, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	var out []Match
	for _, rec := range idx.files {
		for _, s := range rec.symbols {
			if s.Name == name || s.QualifiedName == name {
				out = append(out, s)
			}
		}
	}
	sortSymbols(out)
	return out, nil
}

// File returns the symbols declared in the given file.
func (idx *Index) File(path string) ([]Match, error) {
	rel, err := normalizeIndexRel(idx.Root, path)
	if err != nil {
		return nil, err
	}
	if err := idx.ensureFresh(); err != nil {
		return nil, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	rec, ok := idx.files[rel]
	if !ok {
		return nil, fmt.Errorf("ast: no indexed file %q", rel)
	}
	if rec.err != nil {
		return nil, rec.err
	}
	// build already stores each file's symbols in coordinate order.
	return append([]Match(nil), rec.symbols...), nil
}

// Package returns the symbols belonging to the named Go package.
func (idx *Index) Package(name string) ([]Match, error) {
	if err := idx.ensureFresh(); err != nil {
		return nil, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	var out []Match
	for _, rec := range idx.files {
		for _, s := range rec.symbols {
			if s.Package == name {
				out = append(out, s)
			}
		}
	}
	sortSymbols(out)
	return out, nil
}

// normalizeIndexRel converts a path to a repository-relative key and rejects
// paths outside the index root. The index only contains keys under Root, but
// the explicit containment check keeps a caller error from looking like a
// missing symbol.
//
// The check is lexical. Callers that accept model-supplied paths must resolve
// them through the workspace's containment authority first; this is a second
// guard at the index boundary, not a replacement for it.
func normalizeIndexRel(root, path string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("ast: resolve index root %q: %w", root, err)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(absRoot, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("ast: resolve path %q: %w", path, err)
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("ast: relativize %q: %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("ast: path %q escapes index root %q", path, root)
	}
	return filepath.ToSlash(rel), nil
}

func mustRelPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func sortSymbols(syms []Match) {
	sort.SliceStable(syms, func(i, j int) bool {
		if syms[i].Path != syms[j].Path {
			return syms[i].Path < syms[j].Path
		}
		if syms[i].Line != syms[j].Line {
			return syms[i].Line < syms[j].Line
		}
		if syms[i].QualifiedName != syms[j].QualifiedName {
			return syms[i].QualifiedName < syms[j].QualifiedName
		}
		if syms[i].Kind != syms[j].Kind {
			return syms[i].Kind < syms[j].Kind
		}
		return syms[i].EndLine < syms[j].EndLine
	})
}
