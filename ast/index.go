package ast

import (
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

// fileRecord is the parsed result for one repository-relative file.
type fileRecord struct {
	symbols []Match
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
		Root:     absRoot,
		walkRoot: walkRoot,
		files:    map[string]fileRecord{},
		snapshot: map[string]fileSig{},
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
	sigs := map[string]fileSig{}
	records := map[string]fileRecord{}
	backend := GoBackend{}

	err := filepath.WalkDir(idx.walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == idx.walkRoot {
				return walkErr
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
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel := filepath.ToSlash(mustRelPath(idx.walkRoot, path))
		sig := fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
		sigs[rel] = sig

		matches, perr := backend.Search(path, Options{Kinds: indexedKinds})
		if perr != nil {
			records[rel] = fileRecord{err: perr}
			return nil
		}
		syms := make([]Match, 0, len(matches))
		for _, m := range matches {
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
		records[rel] = fileRecord{symbols: syms}
		return nil
	})
	if err != nil {
		return fmt.Errorf("ast: index %q: %w", idx.Root, err)
	}
	idx.files = records
	idx.snapshot = sigs
	return nil
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
	// Only a count is needed for deletion detection. The per-file
	// comparison below finds changes and additions; allocating a second
	// path map on every query would churn memory for no extra signal.
	current := 0
	changed := false
	err := filepath.WalkDir(idx.walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == idx.walkRoot {
				return walkErr
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
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		current++
		rel := filepath.ToSlash(mustRelPath(idx.walkRoot, path))
		sig := fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
		old, ok := idx.snapshot[rel]
		if !ok || old != sig {
			changed = true
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("ast: inspect %q: %w", idx.Root, err)
	}
	if current != len(idx.snapshot) {
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

// refreshLocked rebuilds only when the snapshot differs from the tree. The
// check and the rebuild share one lock so concurrent queries cannot each
// decide the tree changed and re-parse the repository in turn.
func (idx *Index) refreshLocked() error {
	stale, err := idx.staleLocked()
	if err != nil {
		return err
	}
	if !stale {
		return nil
	}
	return idx.buildLocked()
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

// ParseFailures reports how many files in the current index snapshot failed
// to parse. It does not refresh the index; callers that need freshness run a
// query first.
//
// A parse failure is isolated to its file: its neighbours stay searchable.
// Callers surface this count so a partial result is never mistaken for a
// complete one.
func (idx *Index) ParseFailures() int {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	count := 0
	for _, rec := range idx.files {
		if rec.err != nil {
			count++
		}
	}
	return count
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
