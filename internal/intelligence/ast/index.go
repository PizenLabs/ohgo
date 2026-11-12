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

// maxStructuralFileBytes bounds one parser pass. A larger file is reported as
// an unparsed failure rather than risking unbounded parser memory; it remains
// visible in inventory and blocks a clean structural-completeness claim.
const maxStructuralFileBytes = 16 << 20

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
	// Adapters overrides the default adapter set. Nil uses DefaultAdapters.
	Adapters []Adapter
	// Adaptive defers source parsing until a query needs it. The inventory,
	// language mix, manifests, and unsupported-extension facts are available
	// immediately; symbols/relationships are parsed on demand. It is the
	// production mode. The zero value is eager, preserving the historical
	// NewIndex contract for callers and tests that expect a full snapshot.
	Adaptive bool
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
	adapter     string
	parser      Parser
	kind        FileKind
	test        bool
	size        int64
	mtime       int64
	packageName string
	framework   string
	// parsed distinguishes an inventory observation from an empty/failed
	// file. Adaptive snapshots carry pending records; a query that needs
	// declarations parses them before answering.
	parsed  bool
	partial bool
	issues  []ParseIssue
	symbols []Match
	// imports, references, and calls are the retained syntactic projection.
	// They are re-resolved after a change without re-parsing unchanged files:
	// that is what makes incremental parsing possible. They never leave the
	// package as raw data.
	imports           []Import
	references        []rawReference
	calls             []rawCall
	genericCalls      []rawGenericCall
	genericReferences []rawGenericReference
	relations         []rawRelation
	tests             []TestDecl
	entrypoints       []Match
	err               error
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

	adapters *adapterSet
	adaptive bool

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
	// languageGraph is the language-neutral projection over the same records.
	// It is derived, never stored, and contains only the facts the precise Go
	// resolver does not own.
	languageGraph *languageGraph

	generation        uint64
	snapshotID        string
	sourceFingerprint string
	scopeFingerprint  string
	state             Freshness
	builtAt           time.Time
	lastParsed        int
	lastReused        int
	lastResolution    string
	activation        Activation
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
	adapters := opts.Adapters
	if adapters == nil {
		adapters = DefaultAdapters()
	}
	set, err := newAdapterSet(adapters)
	if err != nil {
		return nil, err
	}
	idx := &Index{
		Root:       absRoot,
		walkRoot:   walkRoot,
		scope:      opts.Scope,
		adapters:   set,
		adaptive:   opts.Adaptive,
		files:      map[string]fileRecord{},
		snapshot:   map[string]fileSig{},
		unreadable: map[string]bool{},
		state:      FreshnessInvalid,
		activation: ActivationOverview,
	}
	if idx.adaptive {
		idx.activation = ActivationOverview
	} else {
		idx.activation = ActivationTargeted
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
	return idx.refreshLockedCtx(ctx, true, true, !idx.adaptive)
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
func (idx *Index) refreshLockedCtx(ctx context.Context, watchModule, force, parseAll bool) error {
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
	if err := idx.updateLockedCtx(ctx, scan, force || !idx.built, parseAll); err != nil {
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
	return idx.refreshLockedCtx(ctx, watchModule, false, false)
}

// updateLockedCtx applies one scan and publishes the resulting snapshot.
//
// In eager mode it parses added/changed source files. In adaptive mode an
// inventory refresh parses nothing; a later query that needs declarations
// parses the pending files through ensureParsedLockedCtx. Deleted files are
// removed, unreadable subtrees retain their last known records, and graph
// projections are re-derived from retained raw syntax without re-parsing
// unchanged files.
func (idx *Index) updateLockedCtx(ctx context.Context, scan treeScan, forceFull, parseAll bool) error {
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
	shouldParse := parseAll || !idx.adaptive
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		sig := scan.files[rel]
		old, hadOld := idx.files[rel]
		language, isSource := LanguageForPath(rel)
		adapter, hasAdapter := idx.adapters.AdapterFor(rel)
		kind := fileKindForLanguage(rel, language, isSource)
		if !isSource {
			rec := fileRecord{
				kind:     kind,
				language: string(language),
				test:     IsTestPath(rel),
				size:     sig.size,
				mtime:    sig.mtime,
				parsed:   true,
			}
			if kind == FileConfig || kind == FileManifest {
				rec.framework = detectFramework(filepath.Join(idx.walkRoot, filepath.FromSlash(rel)))
			}
			records[rel] = rec
			continue
		}
		if !hasAdapter {
			records[rel] = fileRecord{
				kind:     kind,
				language: string(language),
				test:     IsTestPath(rel),
				size:     sig.size,
				mtime:    sig.mtime,
			}
			continue
		}

		if hadOld && old.parsed && old.adapter == adapter.Name() &&
			idx.snapshot[rel] == sig && !forceFull {
			records[rel] = old
			reused++
			continue
		}
		if hadOld && !old.parsed && idx.snapshot[rel] == sig && !forceFull && !shouldParse {
			records[rel] = old
			continue
		}
		pending := fileRecord{
			language: string(language),
			adapter:  adapter.Name(),
			kind:     kind,
			test:     IsTestPath(rel),
			size:     sig.size,
			mtime:    sig.mtime,
		}
		if !shouldParse {
			records[rel] = pending
			continue
		}
		rec, err := idx.parseRecord(ctx, rel, sig, adapter)
		if err != nil {
			return err
		}
		records[rel] = rec
		if rec.parsed {
			parsed++
		}
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

	deletedParsed := false
	for rel, rec := range idx.files {
		if !rec.parsed {
			continue
		}
		if _, ok := records[rel]; !ok {
			deletedParsed = true
			break
		}
	}
	graph := idx.graph
	symbolGraph := idx.symbolGraph
	languageGraph := idx.languageGraph
	resolution := "none"
	if idx.adaptive && parsed == 0 && !parseAll {
		resolution = "inventory_only"
	}
	needsProjection := forceFull || parsed > 0 || deletedParsed ||
		scan.moduleSig != idx.moduleSig || scan.scopeFingerprint != idx.scopeFingerprint ||
		(!idx.adaptive && (graph == nil || symbolGraph == nil || languageGraph == nil))
	if needsProjection {
		resolution = "global_reresolve"
		if parseAll {
			resolution = "full_snapshot"
		}
		graph = buildPackageGraph(records, scan.module)
		var err error
		symbolGraph, err = buildSymbolGraph(ctx, records, scan.module, graph)
		if err != nil {
			return err
		}
		resolveRecordImports(records)
		languageGraph, err = buildLanguageGraph(ctx, records)
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
	idx.languageGraph = languageGraph
	idx.scopeFingerprint = scan.scopeFingerprint
	idx.generation++
	idx.builtAt = time.Now()
	idx.lastParsed = parsed
	idx.lastReused = reused
	idx.lastResolution = resolution
	idx.sourceFingerprint = sourceFingerprint(scan)
	idx.snapshotID = snapshotIdentity(idx.sourceFingerprint, scan.module, scan.scopeFingerprint)
	if parsed > 0 {
		if needsProjection && languageGraph != nil {
			idx.activation = ActivationImpact
		} else {
			idx.activation = ActivationTargeted
		}
	} else {
		idx.activation = ActivationOverview
	}
	idx.built = true
	return nil
}

// ensureParsedLockedCtx parses every pending source record. It is the
// TARGETED/IMPACT activation step: inventory-only overviews never call it, so
// a project question does not build a full cross-reference graph.
func (idx *Index) ensureParsedLockedCtx(ctx context.Context) error {
	if idx == nil || !idx.adaptive {
		return nil
	}
	paths := make([]string, 0, len(idx.files))
	for rel, rec := range idx.files {
		if rec.parsed || rec.err != nil || !IsSourcePath(rel) {
			continue
		}
		if _, ok := idx.adapters.AdapterFor(rel); !ok {
			continue
		}
		paths = append(paths, rel)
	}
	if len(paths) == 0 {
		if idx.languageGraph == nil && idx.graph != nil {
			resolveRecordImports(idx.files)
			graph, err := buildLanguageGraph(ctx, idx.files)
			if err != nil {
				return err
			}
			idx.languageGraph = graph
		}
		return nil
	}
	sort.Strings(paths)
	records := make(map[string]fileRecord, len(idx.files))
	for rel, rec := range idx.files {
		records[rel] = rec
	}
	parsed := 0
	newlyFailed := 0
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		adapter, ok := idx.adapters.AdapterFor(rel)
		if !ok {
			continue
		}
		sig := idx.snapshot[rel]
		rec, err := idx.parseRecord(ctx, rel, sig, adapter)
		if err != nil {
			return err
		}
		records[rel] = rec
		if rec.parsed {
			parsed++
		} else if rec.err != nil {
			newlyFailed++
		}
	}
	// A parse error is a published fact about the snapshot, not a reason to
	// crash the query; failure counts are reported and negative claims stay
	// incomplete.
	graph := buildPackageGraph(records, idx.modulePath)
	symbolGraph, err := buildSymbolGraph(ctx, records, idx.modulePath, graph)
	if err != nil {
		return err
	}
	resolveRecordImports(records)
	languageGraph, err := buildLanguageGraph(ctx, records)
	if err != nil {
		return err
	}
	idx.files = records
	idx.graph = graph
	idx.symbolGraph = symbolGraph
	idx.languageGraph = languageGraph
	idx.generation++
	idx.builtAt = time.Now()
	idx.lastParsed = parsed
	idx.lastReused = len(records) - parsed
	idx.lastResolution = "targeted_parse"
	idx.activation = ActivationImpact
	_ = newlyFailed
	return nil
}

// parseRecord parses one source file through its adapter and guards against a
// source change during the parse.
func (idx *Index) parseRecord(ctx context.Context, rel string, sig fileSig, adapter Adapter) (fileRecord, error) {
	rec := idx.parseFileRecord(ctx, rel, sig, adapter)
	if err := ctx.Err(); err != nil {
		return fileRecord{}, err
	}
	if rec.err != nil {
		return rec, nil
	}
	current, err := statSignature(filepath.Join(idx.walkRoot, filepath.FromSlash(rel)))
	if err != nil {
		return fileRecord{}, fmt.Errorf("ast: inspect %q after parsing: %w", rel, err)
	}
	if current == sig {
		return rec, nil
	}
	rec = idx.parseFileRecord(ctx, rel, current, adapter)
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

// parseFileRecord performs exactly one adapter pass for one file.
func (idx *Index) parseFileRecord(ctx context.Context, rel string, sig fileSig, adapter Adapter) fileRecord {
	abs := filepath.Join(idx.walkRoot, filepath.FromSlash(rel))
	language, _ := LanguageForPath(rel)
	kind := fileKindForLanguage(rel, language, true)
	rec := fileRecord{
		language: string(language),
		adapter:  adapter.Name(),
		parser:   Parser{Name: adapter.Name()},
		kind:     kind,
		test:     IsTestPath(rel),
		size:     sig.size,
		mtime:    sig.mtime,
	}
	if sig.size > maxStructuralFileBytes {
		rec.err = fmt.Errorf("ast: %q exceeds the %d-byte structural parse bound", rel, maxStructuralFileBytes)
		return rec
	}
	var facts FileFacts
	var err error
	if contextual, ok := adapter.(ContextAdapter); ok {
		facts, err = contextual.ParseFileContext(ctx, abs, Options{Kinds: indexedKinds, Relationships: true})
	} else {
		facts, err = adapter.ParseFile(abs, Options{Kinds: indexedKinds, Relationships: true})
	}
	if err != nil {
		rec.err = err
		return rec
	}
	if facts.Language != "" {
		rec.language = string(facts.Language)
	}
	if facts.Adapter != "" {
		rec.adapter = facts.Adapter
	}
	rec.parser = facts.Parser
	rec.partial = facts.Partial
	rec.issues = append([]ParseIssue(nil), facts.Issues...)
	rec.packageName = facts.Package
	if rec.packageName == "" {
		rec.packageName = facts.Namespace
	}

	syms := make([]Match, 0, len(facts.Symbols))
	for _, m := range facts.Symbols {
		m.Path = rel
		if m.Language == "" {
			m.Language = language
		}
		if m.Adapter == "" {
			m.Adapter = rec.adapter
		}
		if !m.Test {
			m.Test = rec.test
		}
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
		if test.Symbol.Language == "" {
			test.Symbol.Language = language
		}
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
		if entry.Language == "" {
			entry.Language = language
		}
		entry.Doc = ""
		entry.Signature = ""
		entries = append(entries, entry)
	}
	sortSymbols(entries)

	rec.parsed = true
	rec.symbols = syms
	rec.imports = facts.Imports
	rec.references = facts.references
	rec.calls = facts.calls
	rec.genericCalls = facts.genericCalls
	rec.genericReferences = facts.genericReferences
	rec.relations = facts.relations
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
	if err := idx.ensureParsedLockedCtx(context.Background()); err != nil {
		return nil, err
	}

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
	if err := idx.ensureParsedLockedCtx(context.Background()); err != nil {
		return nil, err
	}

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
	if err := idx.ensureParsedLockedCtx(context.Background()); err != nil {
		return nil, err
	}

	rec, ok := idx.files[rel]
	if !ok {
		return nil, fmt.Errorf("ast: no indexed file %q", rel)
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
	if err := idx.ensureParsedLockedCtx(context.Background()); err != nil {
		return nil, err
	}

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
		Activation:        idx.activation,
	}
	dirs := map[string]bool{}
	packages := 0
	if idx.graph != nil {
		packages = len(idx.graph.order)
	}
	coverage := map[Language]*LanguageCoverage{}
	unsupported := map[string]int{}
	unsupportedSource := false
	for rel, rec := range idx.files {
		dirs[dirOf(rel)] = true
		language, source := LanguageForPath(rel)
		if !source {
			if ext := unsupportedSourceExtension(rel); ext != "" {
				unsupported[ext]++
			}
			switch rec.kind {
			case FileConfig, FileManifest:
				info.Configs++
			}
			continue
		}
		info.SourceFiles++
		item := coverage[language]
		if item == nil {
			item = &LanguageCoverage{Language: language}
			coverage[language] = item
		}
		item.Files++
		if language == LanguageGo {
			info.GoFiles++
		}
		if _, supported := idx.adapterLanguageSupported(language); !supported {
			item.Supported = false
			item.Coverage = "UNSUPPORTED"
			unsupportedSource = true
			unsupported[pathExt(rel)]++
			continue
		}
		if rec.parsed {
			info.ParsedFiles++
			item.Parsed++
			info.Symbols += len(rec.symbols)
			info.Tests += len(rec.tests)
			info.Entrypoints += len(rec.entrypoints)
			item.Symbols += len(rec.symbols)
			if rec.partial {
				info.FailedFiles++
				item.Failed++
			}
		} else if rec.err != nil {
			info.FailedFiles++
			item.Failed++
		} else {
			info.PendingFiles++
			item.Pending++
		}
	}
	for language, item := range coverage {
		_, supported := idx.adapterLanguageSupported(language)
		item.Supported = supported
		switch {
		case !supported:
			item.Coverage = "UNSUPPORTED"
		case item.Failed > 0:
			item.Coverage = "PARTIAL"
		case item.Pending > 0:
			item.Coverage = "UNPARSED"
		case item.Parsed == item.Files:
			item.Coverage = "COMPLETE"
		default:
			item.Coverage = "PARTIAL"
		}
		info.Languages = append(info.Languages, *item)
	}
	sort.SliceStable(info.Languages, func(i, j int) bool {
		return info.Languages[i].Language < info.Languages[j].Language
	})
	for ext, count := range unsupported {
		info.Unsupported = append(info.Unsupported, UnsupportedLanguage{Extension: ext, Files: count})
	}
	sort.SliceStable(info.Unsupported, func(i, j int) bool {
		return info.Unsupported[i].Extension < info.Unsupported[j].Extension
	})
	info.Directories = len(dirs)
	info.Packages = packages
	if idx.symbolGraph != nil {
		info.References = len(idx.symbolGraph.refs)
		info.Calls = len(idx.symbolGraph.calls)
	}
	if idx.languageGraph != nil {
		info.References += len(idx.languageGraph.references)
		info.Calls += len(idx.languageGraph.calls)
	}
	info.InventoryComplete = len(idx.unreadable) == 0
	info.StructuralComplete = info.FailedFiles == 0 && info.PendingFiles == 0 &&
		len(info.Unsupported) == 0 && !unsupportedSource && info.InventoryComplete
	info.Partial = info.ParseFailures > 0 || info.FailedFiles > 0 || info.PendingFiles > 0 ||
		len(info.Unsupported) > 0 || unsupportedSource
	return info
}

// adapterLanguageSupported reports whether this build registered an adapter
// for the language.
func (idx *Index) adapterLanguageSupported(language Language) (Adapter, bool) {
	for _, adapter := range idx.adapters.Adapters() {
		for _, candidate := range adapter.Languages() {
			if candidate == language {
				return adapter, true
			}
		}
	}
	return nil, false
}

// unsupportedSourceExtension reports a source-bearing extension this build
// cannot parse. Unknown non-source suffixes (images, archives) are inventory,
// not structural coverage gaps.
func unsupportedSourceExtension(rel string) string {
	ext := strings.ToLower(pathExt(rel))
	switch ext {
	case ".py", ".pyi", ".swift", ".php", ".scala", ".sc", ".sh", ".bash", ".zsh",
		".lua", ".ex", ".exs", ".dart", ".vue", ".svelte", ".m", ".mm", ".fs",
		".fsx", ".pl", ".pm", ".r", ".jl", ".zig", ".sol", ".groovy", ".clj",
		".cljs", ".hs", ".ml", ".mli", ".elm", ".cl", ".adb", ".ads", ".asm",
		".s", ".ps1", ".bat", ".cmd", ".sql":
		return ext
	default:
		return ""
	}
}

// EnsureParsed refreshes inventory and parses every pending supported source
// file. It is the explicit TARGETED/IMPACT boundary: callers that need
// declarations or relationships ask for it, while an overview can stay
// metadata-only.
func (idx *Index) EnsureParsed(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.ensureParsedLockedCtx(ctx)
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
	if err := idx.refreshLockedCtx(ctx, true, true, true); err != nil {
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
