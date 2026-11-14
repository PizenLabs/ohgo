package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/ignore"
)

// StructuralSource owns the one lazily built structural snapshot shared by
// every structural tool.
//
// Constructing it does not parse the repository. It owns the repository-scope
// adapter as well: the index is dependency-free, so .gitignore semantics are
// injected rather than imported into intelligence/ast.
type StructuralSource struct {
	root     string
	scope    *ignoreScope
	cacheDir string

	mu  sync.Mutex
	idx *ast.Index
}

// StructuralSourceOptions configures optional structural services.
type StructuralSourceOptions struct {
	// AnalysisCacheDir persists adaptive analyses between runs. An empty value
	// disables persistence; analysis still works, just without warm reuse.
	AnalysisCacheDir string
}

// NewStructuralSource returns a lazy index handle for root.
func NewStructuralSource(root string) *StructuralSource {
	return NewStructuralSourceWithOptions(root, StructuralSourceOptions{})
}

// NewStructuralSourceWithOptions returns a lazy index handle with services.
func NewStructuralSourceWithOptions(root string, opts StructuralSourceOptions) *StructuralSource {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	return &StructuralSource{
		root:     absRoot,
		scope:    &ignoreScope{root: absRoot},
		cacheDir: opts.AnalysisCacheDir,
	}
}

// AnalysisCacheDir reports where adaptive analyses may be cached.
func (s *StructuralSource) AnalysisCacheDir() string {
	if s == nil {
		return ""
	}
	return s.cacheDir
}

// Root returns the absolute workspace root the source was built for.
func (s *StructuralSource) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Index returns the cached index, building it on first use. Freshness remains
// owned by ast.Index; this method must not add a second refresh policy.
func (s *StructuralSource) Index() (*ast.Index, error) {
	if s == nil {
		return nil, fmt.Errorf("tools: structural source is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx == nil {
		idx, err := ast.NewIndexWithOptions(s.root, ast.IndexOptions{
			Scope:    s.scope,
			Adaptive: true,
		})
		if err != nil {
			return nil, err
		}
		s.idx = idx
	}
	return s.idx, nil
}

// addStructuralProvenance adds the snapshot header every structural result
// must carry. Evidence records these fields directly instead of parsing prose.
func addStructuralProvenance(details map[string]any, idx *ast.Index, complete bool) {
	if idx == nil {
		details["graph_state"] = string(ast.FreshnessInvalid)
		details["complete"] = false
		return
	}
	info := idx.Info()
	details["snapshot_id"] = info.SnapshotID
	details["generation"] = info.Generation
	details["graph_state"] = string(info.State)
	details["partial"] = info.Partial
	details["parse_failures"] = info.ParseFailures
	details["activation"] = string(info.Activation)
	details["structural_complete"] = info.StructuralComplete
	details["inventory_complete"] = info.InventoryComplete
	details["parsed_files"] = info.ParsedFiles
	details["pending_files"] = info.PendingFiles
	details["failed_files"] = info.FailedFiles
	details["test_files"] = info.TestFiles
	details["scope_complete"] = info.ScopeComplete
	details["scope_partial"] = !info.ScopeComplete
	details["inventory_partial"] = !info.InventoryComplete
	details["structural_partial"] = !info.StructuralComplete
	truncated, _ := details["truncated"].(bool)
	details["result_truncated"] = truncated
	details["excluded_roots"] = excludedRootMaps(info.ExcludedRoots)
	details["excluded_source_files"] = info.ExcludedSourceFiles
	details["excluded_files"] = excludedFileMaps(info.ExcludedFiles)
	details["nested_projects"] = projectBoundaryMaps(info.NestedProjects)
	details["languages"] = languageMaps(info.Languages)
	details["unsupported_languages"] = unsupportedMaps(info.Unsupported)
	details["completeness"] = structuralCompleteness(info)
	details["complete"] = complete && info.StructuralComplete && info.InventoryComplete && info.ScopeComplete
}

// structuralCompleteness is the evidence-facing label; it never upgrades a
// parse failure or unsupported source into COMPLETE.
func structuralCompleteness(info ast.SnapshotInfo) string {
	return describeStructural(info).Label
}

// structuralState is the snapshot-level structural verdict plus the decisive
// limitation behind it.
//
// The detail exists because PARTIAL alone is ambiguous: a snapshot whose
// source languages have no adapter in this build is not waiting for more
// parsing, it can never yield a symbol, and a model that reads PARTIAL as
// "more evidence is coming" will wait for evidence that does not exist.
type structuralState struct {
	Label  string
	Detail string
}

func describeStructural(info ast.SnapshotInfo) structuralState {
	switch {
	case info.StructuralComplete:
		return structuralState{Label: "COMPLETE"}
	case info.PendingFiles > 0:
		return structuralState{
			Label:  "UNPARSED",
			Detail: fmt.Sprintf("%d supported source files not parsed yet", info.PendingFiles),
		}
	case unsupportedOnlySource(info):
		return structuralState{
			Label: "UNSUPPORTED",
			Detail: fmt.Sprintf("no adapter for %s; 0 of %d source files parsed",
				strings.Join(unsupportedExtensions(info), ", "), info.SourceFiles),
		}
	case info.FailedFiles > 0:
		return structuralState{
			Label:  "PARTIAL",
			Detail: fmt.Sprintf("%d of %d source files failed to parse", info.FailedFiles, info.SourceFiles),
		}
	default:
		return structuralState{Label: "PARTIAL", Detail: "some source files produced no structural facts"}
	}
}

// unsupportedOnlySource reports that every source-bearing language in the
// inventory lacks an adapter in this build. It is the difference between "no
// symbols found" and "symbols are unreachable in this binary".
func unsupportedOnlySource(info ast.SnapshotInfo) bool {
	source := 0
	for _, coverage := range info.Languages {
		if coverage.Files == 0 {
			continue
		}
		source += coverage.Files
		if coverage.Supported {
			return false
		}
	}
	return source > 0 && info.ParsedFiles == 0
}

// unsupportedExtensions lists inventoried extensions with no adapter in the
// stable order the snapshot recorded.
func unsupportedExtensions(info ast.SnapshotInfo) []string {
	out := make([]string, 0, len(info.Unsupported))
	for _, item := range info.Unsupported {
		out = append(out, item.Extension)
	}
	return out
}

// structuralSourceIndex builds and returns the shared snapshot, mapping the
// error to the tool result vocabulary.
func structuralSourceIndex(source *StructuralSource, name string) (*ast.Index, error) {
	if source == nil {
		return nil, fmt.Errorf("tools: %s tool has no structural source", name)
	}
	return source.Index()
}

// structuralLanguageNames is the tool-facing language vocabulary. It mirrors
// the adapters' canonical identities; an extension alias (ts/tsx) is resolved
// by languageMatches rather than creating a second registry.
func structuralLanguageNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, adapter := range ast.DefaultAdapters() {
		for _, language := range adapter.Languages() {
			name := string(language)
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func structuralLanguageEnums() []any {
	names := structuralLanguageNames()
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, name)
	}
	return out
}

// languageMatches treats TS/TSX and JS/JSX as one family for a model-facing
// filter. It is a query convenience, not adapter ownership: the per-file
// language remains the exact observed identity.
func languageMatches(actual, filter string) bool {
	actual = strings.ToLower(strings.TrimSpace(actual))
	filter = strings.ToLower(strings.TrimSpace(filter))
	switch filter {
	case "typescript", "ts":
		return actual == "typescript" || actual == "tsx"
	case "javascript", "js":
		return actual == "javascript" || actual == "jsx"
	case "csharp", "c#", "cs":
		return actual == "csharp"
	case "c++":
		return actual == "cpp"
	default:
		return actual == filter
	}
}

type ignoreScope struct {
	root string

	mu        sync.RWMutex
	matcher   *ignore.Matcher
	nested    []string
	loadedSig string
}

// Ignored reports whether rel is excluded by the current structural scope.
//
// A nested repository is a project boundary, not a directory the enclosing
// repository gets to hide. The enclosing .gitignore stops at that boundary;
// the nested repository's own ignore files govern its contents. Without this,
// a root rule such as "backend/" (the normal way to keep a nested Git
// repository out of the parent's index) makes the entire backend invisible to
// project analysis.
func (s *ignoreScope) Ignored(rel string, isDir bool) bool {
	s.ensureLoaded()
	s.mu.RLock()
	matcher := s.matcher
	nested := s.nested
	s.mu.RUnlock()
	if base := deepestNested(nested, rel); base != "" {
		return matcher.IgnoredFrom(base, rel, isDir)
	}
	return matcher.Ignored(rel, isDir)
}

// Explain names the rule that excluded rel, when the scope can identify one.
// It is optional provenance for the structural snapshot; a scope without an
// explainer is still valid.
func (s *ignoreScope) Explain(rel string, isDir bool) string {
	s.ensureLoaded()
	s.mu.RLock()
	matcher := s.matcher
	nested := s.nested
	s.mu.RUnlock()
	base := deepestNested(nested, rel)
	excluded, reason := matcher.MatchFrom(base, rel, isDir)
	if !excluded {
		return ""
	}
	return reason
}

// NestedRepositories reports root-relative nested repository roots in
// deterministic order. It is the project-boundary view of the same scope, not
// a second ignore matcher.
func (s *ignoreScope) NestedRepositories() []string {
	s.ensureLoaded()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.nested...)
}

// BoundaryRule reports the enclosing rule a nested project boundary overrides.
func (s *ignoreScope) BoundaryRule(rel string) string {
	s.ensureLoaded()
	s.mu.RLock()
	matcher := s.matcher
	s.mu.RUnlock()
	excluded, reason := matcher.MatchOutside(rel, rel, true)
	if !excluded {
		return ""
	}
	return reason
}

// Fingerprint reloads the matcher when an ignore file or a nested repository
// boundary changed and returns the identity the index records with the
// snapshot.
func (s *ignoreScope) Fingerprint() string {
	s.reload()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.matcher == nil {
		return s.loadedSig
	}
	return s.matcher.Fingerprint() + ":" + s.loadedSig
}

// ensureLoaded is the cheap path for membership checks. The expensive scope
// walk belongs to Fingerprint, which the index calls once per freshness scan;
// doing it here would re-walk the tree for every file.
func (s *ignoreScope) ensureLoaded() {
	s.mu.RLock()
	loaded := s.matcher != nil
	s.mu.RUnlock()
	if loaded {
		return
	}
	s.reload()
}

// reload reads the current ignore files and nested boundaries. It is the
// freshness point owned by Fingerprint.
func (s *ignoreScope) reload() {
	sig, nested := scanStructuralScope(s.root)
	s.mu.Lock()
	defer s.mu.Unlock()
	if sig == s.loadedSig && s.matcher != nil {
		return
	}
	s.matcher = ignore.New(s.root, true)
	s.nested = nested
	s.loadedSig = sig
}

// scanStructuralScope hashes the .gitignore metadata and records every nested
// repository root in one directory walk. It does not read rule text:
// ignore.New owns parsing, and the matcher fingerprint owns the semantic
// identity.
func scanStructuralScope(root string) (string, []string) {
	hasher := sha256.New()
	var nested []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			name := entry.Name()
			switch name {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			if strings.HasPrefix(name, ".") && name != ".github" {
				return filepath.SkipDir
			}
			if isNestedRepository(path) {
				rel, err := filepath.Rel(root, path)
				if err == nil {
					nested = append(nested, filepath.ToSlash(rel))
				}
			}
			return nil
		}
		if entry.Name() != ".gitignore" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		_, _ = fmt.Fprintf(hasher, "%s\x00%d\x00%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", nil
	}
	sort.Strings(nested)
	for _, rel := range nested {
		_, _ = fmt.Fprintf(hasher, "nested\x00%s\n", rel)
	}
	return hex.EncodeToString(hasher.Sum(nil))[:16], nested
}

// isNestedRepository reports a directory carrying its own Git metadata. A
// worktree uses a .git file, so existence rather than directory shape is the
// test.
func isNestedRepository(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}

// deepestNested returns the nearest nested repository root containing rel.
func deepestNested(nested []string, rel string) string {
	best := ""
	for _, base := range nested {
		if rel == base || strings.HasPrefix(rel, base+"/") {
			if len(base) > len(best) {
				best = base
			}
		}
	}
	return best
}
