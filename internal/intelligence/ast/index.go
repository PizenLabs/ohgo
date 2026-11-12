package ast

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrSourceChanged reports a file that was rewritten while it was being
// indexed. The refresh publishes nothing and the caller retries; serving a
// mixture of two source states would be worse than an explicit retryable error.
var ErrSourceChanged = errors.New("source changed while indexing")

// Scope is the repository-membership seam owned by the caller.
//
// The AST package is a dependency-free intelligence leaf: it must not import
// intelligence/ignore or implement a second .gitignore matcher. Production
// injects the shared matcher through StructuralSource; tests may inject a
// simple function-backed scope.
//
// Ignored is called for every directory and regular file below the index root.
// Fingerprint is a deterministic identity of the loaded rule text. A changed
// fingerprint invalidates the snapshot even when no file signature changed,
// because a rule edit can add or remove files without touching a source file.
type Scope interface {
	Ignored(rel string, isDir bool) bool
	Fingerprint() string
}

// IndexOptions configures one index without changing the default constructor.
type IndexOptions struct {
	// Scope excludes repository paths. A nil scope includes every path the
	// index's built-in non-source exclusions do not skip.
	Scope Scope
}

// fileSig is the freshness fingerprint for one source file.
type fileSig struct {
	size  int64
	mtime int64
}

// moduleSig is the freshness fingerprint for go.mod. It is separate from the
// file snapshot because a module path change invalidates every module-local
// edge even when no Go file changed.
type moduleSig struct {
	exists bool
	size   int64
	mtime  int64
}

// fileRecord is the parsed result for one repository-relative file. Non-Go
// files carry metadata only; they are inventory facts, never symbols.
type fileRecord struct {
	language    string
	kind        FileKind
	test        bool
	size        int64
	mtime       int64
	packageName string
	symbols     []Match
	// imports, references, and calls are the retained syntactic projection.
	// They are re-resolved after a change without re-parsing unchanged files:
	// that is what makes incremental parsing possible. They never leave the
	// package as raw data.
	imports     []Import
	references  []rawReference
	calls       []rawCall
	tests       []TestDecl
	entrypoints []Match
	err         error
}

// indexedKinds is the symbol taxonomy.
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

// Index is an in-process, incremental structural snapshot for a repository
// root.
//
// It is derived state: the source tree remains authoritative. A missing or
// stale snapshot is refreshed on demand, and a refresh failure is reported to
// the caller rather than serving coordinates from a snapshot that no longer
// matches the tree.
type Index struct {
	// Root is the absolute lexical root the coordinates are relative to.
	Root string

	// walkRoot is Root with a root-level symlink resolved. Nested symlinks
	// are still not followed by WalkDir, so only the working directory
	// itself may alias its canonical location.
	walkRoot string

	scope Scope

	mu    sync.Mutex
	built bool
	files map[string]fileRecord
	// snapshot holds the signature of every indexed file, not only Go files.
	// A non-Go change is a structural change even though it costs no parse.
	snapshot map[string]fileSig
	// unreadable holds the repository-relative directories and files the last
	// build could not inspect. Their contents are unknown, so they are counted
	// as unparsed: a lookup must not present their absence as a fact.
	unreadable map[string]bool
	// modulePath is the module clause from go.mod, empty when absent or
	// unparseable. The graph cannot prove a module-local edge without it.
	modulePath string
	// moduleSig fingerprints go.mod so a graph query observes a module-path
	// change even though that file is not part of the Go parse.
	moduleSig moduleSig
	// moduleRoot is the nearest directory at or above Root carrying go.mod.
	// It is metadata only; the index never widens its walk to include it.
	moduleRoot string
	// graph is the package/import projection of the same records. It is
	// rebuilt with the snapshot, never independently.
	graph *packageGraph
	// symbolGraph is the resolved reference and direct-call projection of
	// the same records. It shares the records and package graph above; it is
	// not a second parser or index.
	symbolGraph *symbolGraph

	generation        uint64
	snapshotID        string
	sourceFingerprint string
	scopeFingerprint  string
	state             Freshness
	builtAt           time.Time
	lastParsed        int
	lastReused        int
	lastResolution    string
}

// NewIndex builds a fresh structural snapshot rooted at root.
//
// It keeps the historical self-contained constructor: no repository scope is
// injected, so tests and fixtures observe exactly the files they wrote.
// Production uses NewIndexWithOptions through tools.StructuralSource so the
// shared .gitignore matcher applies.
func NewIndex(root string) (*Index, error) {
	return NewIndexWithOptions(root, IndexOptions{})
}

// NewIndexWithOptions builds a snapshot with the supplied scope seam.
func NewIndexWithOptions(root string, opts IndexOptions) (*Index, error) {
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
		scope:      opts.Scope,
		files:      map[string]fileRecord{},
		snapshot:   map[string]fileSig{},
		unreadable: map[string]bool{},
		state:      FreshnessInvalid,
	}
	if err := idx.build(); err != nil {
		return nil, err
	}
	return idx, nil
}

// build performs a full rebuild. It is the initial-load path and the explicit
// recovery path; normal freshness uses an incremental update.
func (idx *Index) build() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.buildLockedCtx(context.Background())
}

// buildLockedCtx performs a full rebuild with cancellation checks.
//
// A cancelled or failed rebuild leaves the previous snapshot untouched and
// returns the error; the caller cannot answer from stale coordinates. It is
// also the explicit recovery path: every Go file is parsed again.
func (idx *Index) buildLockedCtx(ctx context.Context) error {
	return idx.refreshLockedCtx(ctx, true, true)
}

// treeScan is one stat-only observation of the indexed tree.
type treeScan struct {
	files            map[string]fileSig
	unreadable       map[string]bool
	scopeFingerprint string
	module           string
	moduleSig        moduleSig
	moduleRoot       string
}

// scanLockedCtx walks the tree and records signatures. It does not parse:
// freshness costs one stat walk, not one parse per query.
func (idx *Index) scanLockedCtx(ctx context.Context) (treeScan, error) {
	scan := treeScan{
		files:      map[string]fileSig{},
		unreadable: map[string]bool{},
	}
	if err := ctx.Err(); err != nil {
		return treeScan{}, err
	}
	if idx.scope != nil {
		scan.scopeFingerprint = idx.scope.Fingerprint()
	}

	err := filepath.WalkDir(idx.walkRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == idx.walkRoot {
				return walkErr
			}
			scan.unreadable[relKey(idx.walkRoot, path)] = true
			return nil
		}
		rel := relKey(idx.walkRoot, path)
		if entry.IsDir() {
			if path != idx.walkRoot && shouldSkipIndexDir(entry.Name()) {
				return filepath.SkipDir
			}
			if path != idx.walkRoot && idx.scope != nil && idx.scope.Ignored(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if idx.scope != nil && idx.scope.Ignored(rel, false) {
			return nil
		}
		if !entry.Type().IsRegular() {
			// Symlinks and devices are not repository source. WalkDir does
			// not follow them, and indexing a link would smuggle an outside
			// coordinate into the root.
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			scan.unreadable[rel] = true
			return nil
		}
		scan.files[rel] = fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return treeScan{}, ctxErr
		}
		return treeScan{}, fmt.Errorf("ast: index %q: %w", idx.Root, err)
	}
	if err := ctx.Err(); err != nil {
		return treeScan{}, err
	}
	scan.module, scan.moduleSig = readModule(idx.walkRoot)
	scan.moduleRoot = nearestModuleRoot(idx.walkRoot)
	return scan, nil
}

// treeChangedLocked compares a scan to the published snapshot.
func (idx *Index) treeChangedLocked(scan treeScan, watchModule bool) bool {
	if scan.scopeFingerprint != idx.scopeFingerprint {
		return true
	}
	if len(scan.files) != len(idx.snapshot) {
		return true
	}
	for rel, sig := range scan.files {
		old, ok := idx.snapshot[rel]
		if !ok || old != sig {
			return true
		}
	}
	if watchModule && scan.moduleSig != idx.moduleSig {
		return true
	}
	if len(scan.unreadable) != len(idx.unreadable) {
		return true
	}
	for rel := range scan.unreadable {
		if !idx.unreadable[rel] {
			return true
		}
	}
	return false
}

// Stale reports whether the on-disk tree differs from the indexed snapshot.
// An inspection error is reported as stale so a caller refreshes and receives
// the error rather than trusting the old snapshot.
func (idx *Index) Stale() bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	stale, err := idx.staleLockedCtx(context.Background())
	return err != nil || stale
}

// Freshness reports the explicit state without publishing a refresh.
//
// It scans the tree, so a caller that needs a current answer should run a query
// instead. It exists so diagnostics and tests can distinguish CURRENT, STALE,
// and INVALID without mutating the snapshot.
func (idx *Index) Freshness(ctx context.Context) Freshness {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if !idx.built {
		return FreshnessInvalid
	}
	stale, err := idx.staleLockedCtx(ctx)
	if err != nil {
		return FreshnessInvalid
	}
	if stale {
		return FreshnessStale
	}
	return idx.state
}

// staleLockedCtx is staleLocked with cancellation checks during the stat walk.
func (idx *Index) staleLockedCtx(ctx context.Context) (bool, error) {
	scan, err := idx.scanLockedCtx(ctx)
	if err != nil {
		return false, err
	}
	return idx.treeChangedLocked(scan, true), nil
}

// ensureFresh refreshes the snapshot when the tree has changed.
func (idx *Index) ensureFresh() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.refreshGraphLocked(context.Background(), false)
}

// ensureFreshGraph refreshes the snapshot for a graph query, including go.mod.
func (idx *Index) ensureFreshGraph(ctx context.Context) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.refreshGraphLocked(ctx, true)
}

// refreshLockedCtx applies one scan, rebuilding only when needed or forced.
func (idx *Index) refreshLockedCtx(ctx context.Context, watchModule, force bool) error {
	scan, err := idx.scanLockedCtx(ctx)
	if err != nil {
		if idx.built {
			if ctx.Err() != nil {
				idx.state = FreshnessStale
			} else {
				idx.state = FreshnessInvalid
			}
		}
		return err
	}
	if idx.built && !force && !idx.treeChangedLocked(scan, watchModule) {
		idx.state = FreshnessCurrent
		return nil
	}
	idx.state = FreshnessUpdating
	if err := idx.updateLockedCtx(ctx, scan, force || !idx.built); err != nil {
		if idx.built {
			idx.state = FreshnessStale
		} else {
			idx.state = FreshnessInvalid
		}
		return err
	}
	idx.state = FreshnessCurrent
	return nil
}

// refreshGraphLocked is refreshLocked with optional module freshness and
// cancellation. The check and the update share one lock so concurrent queries
// cannot each decide the tree changed and refresh in turn.
func (idx *Index) refreshGraphLocked(ctx context.Context, watchModule bool) error {
	return idx.refreshLockedCtx(ctx, watchModule, false)
}

// updateLockedCtx applies one scan and publishes the resulting snapshot.
//
// It parses only added/changed Go files for a normal refresh. Deleted files are
// removed, unreadable subtrees retain their last known records, and package and
// relationship graphs are re-derived from retained raw syntax without a
// re-parse of unchanged files.
func (idx *Index) updateLockedCtx(ctx context.Context, scan treeScan, forceFull bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	records := make(map[string]fileRecord, len(scan.files))
	paths := make([]string, 0, len(scan.files))
	for rel := range scan.files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	parsed, reused := 0, 0
	backend := GoBackend{}
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		sig := scan.files[rel]
		old, hadOld := idx.files[rel]
		kind := fileKindFor(rel)
		if !isGoFile(rel) {
			records[rel] = fileRecord{
				kind:  kind,
				test:  kind == FileGoTest,
				size:  sig.size,
				mtime: sig.mtime,
			}
			continue
		}

		if hadOld && old.language == "go" && idx.snapshot[rel] == sig && !forceFull {
			records[rel] = old
			reused++
			continue
		}
		rec, err := idx.parseGoRecord(ctx, rel, sig, backend)
		if err != nil {
			return err
		}
		records[rel] = rec
		parsed++
	}

	// Preserve records from an unreadable subtree. Their absence from the
	// scan is "cannot see", not "deleted", and dropping them would turn a
	// permission error into a false clean negative.
	for rel, rec := range idx.files {
		if _, seen := scan.files[rel]; seen {
			continue
		}
		if underUnreadable(rel, scan.unreadable) {
			records[rel] = rec
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Graph projections are derived from Go records. A non-Go-only change
	// updates inventory metadata and reuses the existing projections; a Go,
	// module, or scope change re-resolves them from retained syntax without
	// reparsing unchanged files.
	deletedGo := false
	for rel, rec := range idx.files {
		if rec.language != "go" {
			continue
		}
		if _, ok := records[rel]; !ok {
			deletedGo = true
			break
		}
	}
	graph := idx.graph
	symbolGraph := idx.symbolGraph
	resolution := "none"
	if forceFull || graph == nil || symbolGraph == nil || parsed > 0 || deletedGo ||
		scan.moduleSig != idx.moduleSig || scan.scopeFingerprint != idx.scopeFingerprint {
		resolution = "global_reresolve"
		if forceFull {
			resolution = "full_snapshot"
		}
		graph = buildPackageGraph(records, scan.module)
		var err error
		symbolGraph, err = buildSymbolGraph(ctx, records, scan.module, graph)
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Publish atomically: nothing above this point has touched the live
	// snapshot, so a cancellation or parse failure cannot expose a mixture.
	idx.files = records
	idx.snapshot = scan.files
	idx.unreadable = scan.unreadable
	idx.modulePath = scan.module
	idx.moduleSig = scan.moduleSig
	idx.moduleRoot = scan.moduleRoot
	idx.graph = graph
	idx.symbolGraph = symbolGraph
	idx.scopeFingerprint = scan.scopeFingerprint
	idx.generation++
	idx.builtAt = time.Now()
	idx.lastParsed = parsed
	idx.lastReused = reused
	idx.lastResolution = resolution
	idx.sourceFingerprint = sourceFingerprint(scan)
	idx.snapshotID = snapshotIdentity(idx.sourceFingerprint, scan.module, scan.scopeFingerprint)
	idx.built = true
	return nil
}

// parseGoRecord parses one Go file and guards against a source change during
// the parse. A second change fails the refresh rather than publishing mixed
// facts.
func (idx *Index) parseGoRecord(ctx context.Context, rel string, sig fileSig, backend GoBackend) (fileRecord, error) {
	rec := idx.parseGoFile(rel, sig, backend)
	if err := ctx.Err(); err != nil {
		return fileRecord{}, err
	}
	current, err := statSignature(filepath.Join(idx.walkRoot, filepath.FromSlash(rel)))
	if err != nil {
		return fileRecord{}, fmt.Errorf("ast: inspect %q after parsing: %w", rel, err)
	}
	if current == sig {
		return rec, nil
	}
	// The file changed while it was parsed. Retry once against the new
	// signature; if it changes again, fail the refresh instead of publishing
	// a mixed snapshot.
	rec = idx.parseGoFile(rel, current, backend)
	if err := ctx.Err(); err != nil {
		return fileRecord{}, err
	}
	after, err := statSignature(filepath.Join(idx.walkRoot, filepath.FromSlash(rel)))
	if err != nil {
		return fileRecord{}, fmt.Errorf("ast: inspect %q after retry: %w", rel, err)
	}
	if after != current {
		return fileRecord{}, fmt.Errorf("ast: %q changed while it was being indexed: %w", rel, ErrSourceChanged)
	}
	return rec, nil
}

// parseGoFile performs exactly one parser pass for one file.
func (idx *Index) parseGoFile(rel string, sig fileSig, backend GoBackend) fileRecord {
	abs := filepath.Join(idx.walkRoot, filepath.FromSlash(rel))
	kind := fileKindFor(rel)
	rec := fileRecord{
		language: "go",
		kind:     kind,
		test:     kind == FileGoTest,
		size:     sig.size,
		mtime:    sig.mtime,
	}
	facts, err := backend.ParseFile(abs, Options{Kinds: indexedKinds, Relationships: true})
	if err != nil {
		rec.err = err
		return rec
	}
	syms := make([]Match, 0, len(facts.Symbols))
	for _, m := range facts.Symbols {
		m.Path = rel
		// The index is a coordinate store, not a source cache. Doc and
		// Signature duplicate source text; a lookup that needs them can read
		// the file at the returned line range.
		m.Doc = ""
		m.Signature = ""
		syms = append(syms, m)
	}
	sortSymbols(syms)

	tests := make([]TestDecl, 0, len(facts.Tests))
	for _, test := range facts.Tests {
		test.Symbol.Path = rel
		test.Symbol.Doc = ""
		test.Symbol.Signature = ""
		tests = append(tests, test)
	}
	sort.SliceStable(tests, func(i, j int) bool {
		if tests[i].Symbol.Line != tests[j].Symbol.Line {
			return tests[i].Symbol.Line < tests[j].Symbol.Line
		}
		return tests[i].Symbol.QualifiedName < tests[j].Symbol.QualifiedName
	})

	entries := make([]Match, 0, len(facts.Entrypoints))
	for _, entry := range facts.Entrypoints {
		entry.Path = rel
		entry.Doc = ""
		entry.Signature = ""
		entries = append(entries, entry)
	}
	sortSymbols(entries)

	rec.packageName = facts.Package
	rec.symbols = syms
	rec.imports = facts.Imports
	rec.references = facts.references
	rec.calls = facts.calls
	rec.tests = tests
	rec.entrypoints = entries
	return rec
}

// statSignature returns the current signature of one path.
func statSignature(path string) (fileSig, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fileSig{}, err
	}
	return fileSig{size: info.Size(), mtime: info.ModTime().UnixNano()}, nil
}

// underUnreadable reports whether rel lies below an unreadable entry.
func underUnreadable(rel string, unreadable map[string]bool) bool {
	for prefix := range unreadable {
		if prefix == rel || strings.HasPrefix(rel, prefix+"/") {
			return true
		}
	}
	return false
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

// nearestModuleRoot records the nearest go.mod above (or at) root. It is
// metadata only: the index never widens its walk beyond Root.
func nearestModuleRoot(root string) string {
	current := root
	for {
		if info, err := os.Stat(filepath.Join(current, "go.mod")); err == nil && info.Mode().IsRegular() {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// sourceFingerprint hashes the path/size/mtime signature set and the scope.
func sourceFingerprint(scan treeScan) string {
	hasher := sha256.New()
	paths := make([]string, 0, len(scan.files))
	for rel := range scan.files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		sig := scan.files[rel]
		_, _ = fmt.Fprintf(hasher, "%s\x00%d\x00%d\n", rel, sig.size, sig.mtime)
	}
	_, _ = fmt.Fprintf(hasher, "scope\x00%s\nmodule\x00%s\n", scan.scopeFingerprint, scan.module)
	return hex.EncodeToString(hasher.Sum(nil))[:16]
}

// snapshotIdentity is deterministic for identical source and scope. It is not
// the generation: generation is process-local, identity survives a restart.
func snapshotIdentity(fingerprint, module, scope string) string {
	sum := sha256.Sum256([]byte(fingerprint + "\x00" + module + "\x00" + scope))
	return hex.EncodeToString(sum[:])[:16]
}

// relKey is the repository-relative, slash-separated key for a path under root.
func relKey(root, path string) string {
	return filepath.ToSlash(mustRelPath(root, path))
}

// shouldSkipIndexDir reports directories that are never useful to index.
//
// testdata is skipped because the Go toolchain does not treat it as package
// source; indexing it would report declarations the compiler never sees.
// .github is the one hidden directory with structural configuration facts.
func shouldSkipIndexDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "testdata":
		return true
	case ".github":
		return false
	}
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
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

// ParseFailures reports how many entries in the current snapshot could not be
// indexed: files that failed to parse or read, and directories that could not
// be listed. It does not refresh the index; callers that need freshness run a
// query first.
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
	if rec.language != "go" {
		return nil, nil
	}
	if rec.err != nil {
		return nil, rec.err
	}
	// update stores each file's symbols in coordinate order.
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

// Info returns the published snapshot header without refreshing. A caller that
// needs a current answer runs a query first; structural tools call this after
// their query so the details describe the generation they actually observed.
func (idx *Index) Info() SnapshotInfo {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.infoLocked()
}

// infoLocked projects the current snapshot state.
func (idx *Index) infoLocked() SnapshotInfo {
	info := SnapshotInfo{
		Root:              idx.Root,
		Module:            idx.modulePath,
		ModuleRoot:        idx.moduleRoot,
		Generation:        idx.generation,
		SnapshotID:        idx.snapshotID,
		SourceFingerprint: idx.sourceFingerprint,
		ScopeFingerprint:  idx.scopeFingerprint,
		State:             idx.state,
		BuiltAt:           idx.builtAt,
		Files:             len(idx.files),
		ParseFailures:     idx.parseFailuresLocked(),
		LastParsedFiles:   idx.lastParsed,
		LastReusedFiles:   idx.lastReused,
		LastResolution:    idx.lastResolution,
	}
	dirs := map[string]bool{}
	packages := 0
	if idx.graph != nil {
		packages = len(idx.graph.order)
	}
	for rel, rec := range idx.files {
		dirs[dirOf(rel)] = true
		if rec.language == "go" {
			info.GoFiles++
			info.Symbols += len(rec.symbols)
			info.Tests += len(rec.tests)
			info.Entrypoints += len(rec.entrypoints)
			continue
		}
		switch rec.kind {
		case FileConfig, FileManifest:
			info.Configs++
		}
	}
	info.Directories = len(dirs)
	info.Packages = packages
	if idx.symbolGraph != nil {
		info.References = len(idx.symbolGraph.refs)
		info.Calls = len(idx.symbolGraph.calls)
	}
	info.Partial = info.ParseFailures > 0
	return info
}

// Snapshot refreshes the snapshot and returns its provenance header.
func (idx *Index) Snapshot(ctx context.Context) (SnapshotInfo, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotInfo{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return SnapshotInfo{}, err
	}
	return idx.Info(), nil
}

// ForceRefresh rebuilds every Go file. It exists for callers that know stat
// metadata is unreliable (for example a restored timestamp).
func (idx *Index) ForceRefresh(ctx context.Context) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.state = FreshnessUpdating
	if err := idx.refreshLockedCtx(ctx, true, true); err != nil {
		if idx.built {
			idx.state = FreshnessStale
		} else {
			idx.state = FreshnessInvalid
		}
		return err
	}
	idx.state = FreshnessCurrent
	return nil
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
