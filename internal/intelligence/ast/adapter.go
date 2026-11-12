package ast

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Adapter is the language boundary of the structural index.
//
// There is one adapter per language family. Go stays on the standard-library
// backend; additional languages are syntax adapters over a shared tree-sitter
// substrate. Every adapter maps into the same canonical facts and the same
// snapshot; a language never gets its own graph, index, or evidence path.
type Adapter interface {
	// Name is the stable adapter identity used in diagnostics.
	Name() string
	// Language is the primary canonical language identity.
	Language() Language
	// Languages lists every language identity this adapter claims.
	Languages() []Language
	// Extensions lists the file suffixes the adapter claims.
	Extensions() []string
	// Supported reports whether a path belongs to this adapter.
	Supported(path string) bool
	// Capabilities declares which canonical facts the adapter emits.
	Capabilities() Capabilities
	// ParseFile performs one parser pass and returns the canonical file facts.
	ParseFile(path string, opts ParseOptions) (FileFacts, error)
}

// ContextAdapter is the optional cancellation-aware form of Adapter. The
// index uses it when available; the native Go backend keeps its historical
// signature and is bounded by the post-parse context check.
type ContextAdapter interface {
	ParseFileContext(ctx context.Context, path string, opts ParseOptions) (FileFacts, error)
}

// ParseOptions controls one adapter parse.
//
// It is an alias of Options so existing Search callers keep their API while the
// adapter boundary has a name that describes its role.
type ParseOptions = Options

// FileFacts is the canonical output of one parser pass.
//
// Symbols, imports, tests, entrypoints, and raw relationships come from the
// same syntax tree. The raw relationship fields are unexported because
// resolution is an index concern: a caller receives resolved coordinates,
// never syntactic fragments that could be mistaken for proven edges.
type FileFacts struct {
	// Language is the canonical language identity.
	Language Language
	// Adapter and Parser record the substrate that produced these facts.
	Adapter string
	Parser  Parser
	// Package is the language-level package, namespace, or module clause.
	Package string
	// Namespace is the fully qualified namespace/module path when the
	// language distinguishes it from the package name.
	Namespace string
	// Module is a declared module name from a manifest or language clause.
	Module string
	// Symbols are the declarations selected by ParseOptions.Kinds.
	Symbols []Match
	// Imports are every import/include/require declaration in the file.
	Imports []Import
	// Tests are test declarations discovered from syntax or conventions.
	Tests []TestDecl
	// Entrypoints are proven program entrypoints.
	Entrypoints []Match
	// Partial reports a recoverable syntax tree with at least one parse
	// error. The facts above remain usable, but absence claims must not be
	// upgraded to complete.
	Partial bool
	// Issues are bounded parse limitations.
	Issues []ParseIssue

	references []rawReference
	calls      []rawCall
	// genericCalls, genericReferences, and relations are the language-neutral
	// projection. Go keeps its precise resolver; the generic projection only
	// materialises a call/relationship when the target name resolves uniquely.
	genericCalls      []rawGenericCall
	genericReferences []rawGenericReference
	relations         []rawRelation
}

// adapterSet owns the deterministic path-to-adapter decision.
//
// It rejects two adapters claiming the same extension instead of silently
// selecting one: an arbitrary parser choice is a correctness bug, not a
// fallback policy.
type adapterSet struct {
	mu       sync.RWMutex
	adapters []Adapter
	byExt    map[string]Adapter
}

// newAdapterSet validates ownership and freezes the registry.
func newAdapterSet(adapters []Adapter) (*adapterSet, error) {
	set := &adapterSet{byExt: map[string]Adapter{}}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		for _, ext := range adapter.Extensions() {
			normalized := normalizeExtension(ext)
			if normalized == "" {
				continue
			}
			if existing, ok := set.byExt[normalized]; ok && existing.Name() != adapter.Name() {
				return nil, fmt.Errorf("ast: ambiguous adapter for %q: %s and %s", normalized, existing.Name(), adapter.Name())
			}
			set.byExt[normalized] = adapter
		}
		set.adapters = append(set.adapters, adapter)
	}
	sort.SliceStable(set.adapters, func(i, j int) bool {
		return set.adapters[i].Name() < set.adapters[j].Name()
	})
	return set, nil
}

// AdapterFor returns the adapter that owns a path.
func (s *adapterSet) AdapterFor(pathArg string) (Adapter, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	adapter, ok := s.byExt[strings.ToLower(pathExt(pathArg))]
	return adapter, ok
}

// Adapters returns the registered adapters in deterministic order.
func (s *adapterSet) Adapters() []Adapter {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Adapter(nil), s.adapters...)
}

// Extensions returns every claimed extension in stable order.
func (s *adapterSet) Extensions() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.byExt))
	for ext := range s.byExt {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

// Capabilities returns one adapter's declared capabilities.
func (s *adapterSet) Capabilities(language Language) Capabilities {
	for _, adapter := range s.Adapters() {
		for _, candidate := range adapter.Languages() {
			if candidate == language {
				return adapter.Capabilities()
			}
		}
	}
	return Capabilities{}
}

// DefaultAdapters returns the production adapter set.
//
// Go is always present. Tree-sitter adapters are appended by the cgo build;
// a no-cgo build returns Go only and reports other languages as unsupported
// rather than pretending they were parsed.
func DefaultAdapters() []Adapter {
	adapters := []Adapter{GoBackend{}}
	adapters = append(adapters, treeSitterAdapters()...)
	return adapters
}

// NewAdapterSet validates an explicit adapter list. It is exported for tests
// and embedders that need a deterministic subset.
func NewAdapterSet(adapters ...Adapter) (*adapterSet, error) {
	return newAdapterSet(adapters)
}

// normalizeExtension accepts ".ts" and "ts" spellings and returns the lower
// case dotted form.
func normalizeExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

// pathExt is a small wrapper that keeps path handling in one place.
func pathExt(pathArg string) string {
	if idx := strings.LastIndexAny(pathArg, "/\\"); idx >= 0 {
		pathArg = pathArg[idx+1:]
	}
	if idx := strings.LastIndexByte(pathArg, '.'); idx >= 0 {
		return pathArg[idx:]
	}
	return ""
}

// Language returns the adapter identity used in FileInfo.
func (GoBackend) Name() string { return "go" }

// Language implements Adapter.
func (GoBackend) Language() Language { return LanguageGo }

// Languages implements Adapter.
func (GoBackend) Languages() []Language { return []Language{LanguageGo} }

// Capabilities implements Adapter.
func (GoBackend) Capabilities() Capabilities {
	return Capabilities{
		Symbols: true, Imports: true, Exports: true, References: true,
		Calls: true, Inheritance: false, Tests: true, Entrypoints: true,
		Semantic: false,
	}
}

// ParseFile implements Adapter. It is the single Go parser pass; Parse and
// Search are compatibility projections of this method.
func (b GoBackend) ParseFile(path string, opts ParseOptions) (FileFacts, error) {
	structure, err := b.parse(path, opts)
	if err != nil {
		return FileFacts{}, err
	}
	return structure, nil
}

// var _ Adapter = GoBackend{} keeps the Go backend honest at compile time.
var _ Adapter = GoBackend{}
