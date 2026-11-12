package ast

import (
	"path"
	"strings"
	"time"
)

// Freshness is the runtime state of one published structural snapshot.
//
// It is index state, not a task lifecycle. A query must never present a stale
// or invalid snapshot as current: the index refreshes before answering and
// returns the refresh error rather than coordinates from a tree that no longer
// matches the source.
type Freshness string

// Snapshot freshness values.
const (
	// FreshnessCurrent means the last successful snapshot matches the tree.
	FreshnessCurrent Freshness = "CURRENT"
	// FreshnessUpdating means a refresh is in progress.
	FreshnessUpdating Freshness = "UPDATING"
	// FreshnessStale means the tree changed but the snapshot has not caught up.
	FreshnessStale Freshness = "STALE"
	// FreshnessInvalid means there is no usable snapshot; queries must error.
	FreshnessInvalid Freshness = "INVALID"
)

// NodeKind is the canonical structural node vocabulary.
//
// It is a query projection vocabulary, not a second stored graph. Every value
// is derived from the same file records the coordinate queries already use.
type NodeKind string

// Canonical node kinds.
const (
	NodeProject     NodeKind = "project"
	NodeDirectory   NodeKind = "directory"
	NodeFile        NodeKind = "file"
	NodeModule      NodeKind = "module"
	NodePackage     NodeKind = "package"
	NodeNamespace   NodeKind = "namespace"
	NodeSymbol      NodeKind = "symbol"
	NodeClass       NodeKind = "class"
	NodeStruct      NodeKind = "struct"
	NodeInterface   NodeKind = "interface"
	NodeTrait       NodeKind = "trait"
	NodeEnum        NodeKind = "enum"
	NodeFunction    NodeKind = "function"
	NodeMethod      NodeKind = "method"
	NodeField       NodeKind = "field"
	NodeProperty    NodeKind = "property"
	NodeConstant    NodeKind = "constant"
	NodeVariable    NodeKind = "variable"
	NodeTypeAlias   NodeKind = "type_alias"
	NodeRecord      NodeKind = "record"
	NodeObject      NodeKind = "object"
	NodeConstructor NodeKind = "constructor"
	NodeTest        NodeKind = "test"
	NodeConfig      NodeKind = "config"
	NodeEntrypoint  NodeKind = "entrypoint"
)

// EdgeKind is the canonical structural edge vocabulary.
type EdgeKind string

// Canonical edge kinds.
const (
	EdgeContains   EdgeKind = "CONTAINS"
	EdgeDefines    EdgeKind = "DEFINES"
	EdgeImports    EdgeKind = "IMPORTS"
	EdgeExports    EdgeKind = "EXPORTS"
	EdgeReferences EdgeKind = "REFERENCES"
	EdgeCalls      EdgeKind = "CALLS"
	EdgeExtends    EdgeKind = "EXTENDS"
	EdgeImplements EdgeKind = "IMPLEMENTS"
	EdgeOverrides  EdgeKind = "OVERRIDES"
	EdgeTests      EdgeKind = "TESTS"
	EdgeEntrypoint EdgeKind = "ENTRYPOINT"
	EdgeDependsOn  EdgeKind = "DEPENDS_ON"
)

// SourceRef is the provenance of one structural node or edge.
//
// Line and column fields are 1-based. Columns are UTF-8 byte offsets, matching
// Go's token.Position; the source byte range remains the authoritative witness.
// An aggregate edge carries the deterministic witness that proves it; when no
// witness exists the edge is not emitted, so a consumer never receives an
// unsourced relationship.
type SourceRef struct {
	Path      string   `json:"path"`
	Package   string   `json:"package,omitempty"`
	Symbol    string   `json:"symbol,omitempty"`
	Language  Language `json:"language,omitempty"`
	Adapter   string   `json:"adapter,omitempty"`
	StartLine int      `json:"start_line,omitempty"`
	EndLine   int      `json:"end_line,omitempty"`
	StartCol  int      `json:"start_column,omitempty"`
	EndCol    int      `json:"end_column,omitempty"`
	Test      bool     `json:"test,omitempty"`
}

// Node is one canonical structural fact. ID is snapshot-scoped: a line change
// changes a symbol coordinate, and every edge/query carries the generation so
// a consumer can tell when it was observed.
type Node struct {
	ID         string    `json:"id"`
	Kind       NodeKind  `json:"kind"`
	Name       string    `json:"name"`
	Qualified  string    `json:"qualified_name,omitempty"`
	NativeKind string    `json:"native_kind,omitempty"`
	Language   Language  `json:"language,omitempty"`
	Exported   bool      `json:"exported,omitempty"`
	Source     SourceRef `json:"source"`
}

// Edge is one proven structural relationship between two canonical nodes.
type Edge struct {
	From       string     `json:"from"`
	To         string     `json:"to"`
	Kind       EdgeKind   `json:"kind"`
	Certainty  string     `json:"certainty,omitempty"`
	Resolution Resolution `json:"resolution,omitempty"`
	Language   Language   `json:"language,omitempty"`
	Adapter    string     `json:"adapter,omitempty"`
	Source     SourceRef  `json:"source"`
}

// SymbolNodeID returns the canonical snapshot-scoped identity of a declaration.
func SymbolNodeID(rel string, line int, qualified string) string {
	return "sym:" + rel + ":" + itoa(line) + ":" + qualified
}

// TestNodeID returns the canonical identity of a test declaration.
func TestNodeID(rel string, line int, qualified string) string {
	return "test:" + rel + ":" + itoa(line) + ":" + qualified
}

// EntrypointNodeID returns the canonical identity of a Go entrypoint.
func EntrypointNodeID(rel string, line int, qualified string) string {
	return "entry:" + rel + ":" + itoa(line) + ":" + qualified
}

// PackageNodeID returns the canonical identity of one package in one directory.
func PackageNodeID(dir, name string) string {
	if dir == "" {
		dir = "."
	}
	return "pkg:" + dir + "#" + name
}

// FileNodeID returns the canonical identity of one indexed file.
func FileNodeID(rel string) string { return "file:" + rel }

// DirectoryNodeID returns the canonical identity of one indexed directory.
func DirectoryNodeID(rel string) string {
	if rel == "" {
		rel = "."
	}
	return "dir:" + rel
}

// ConfigNodeID returns the canonical identity of one configuration fact.
func ConfigNodeID(rel string) string { return "config:" + rel }

// ModuleNodeID returns the canonical identity of one Go module.
func ModuleNodeID(module string) string { return "module:" + module }

// ProjectNodeID returns the canonical identity used when a snapshot has no
// module path. It deliberately uses the repository-relative root spelling so a
// node identity never embeds the machine's absolute path.
func ProjectNodeID(module string) string {
	if module != "" {
		return "project:" + module
	}
	return "project:."
}

// SymbolKind is the language-neutral declaration vocabulary used by the
// canonical graph. Kind remains the Go adapter's compatibility wire value.
type SymbolKind string

// Canonical symbol kinds.
const (
	SymbolPackage     SymbolKind = "package"
	SymbolModule      SymbolKind = "module"
	SymbolNamespace   SymbolKind = "namespace"
	SymbolClass       SymbolKind = "class"
	SymbolStruct      SymbolKind = "struct"
	SymbolInterface   SymbolKind = "interface"
	SymbolTrait       SymbolKind = "trait"
	SymbolEnum        SymbolKind = "enum"
	SymbolRecord      SymbolKind = "record"
	SymbolFunction    SymbolKind = "function"
	SymbolMethod      SymbolKind = "method"
	SymbolField       SymbolKind = "field"
	SymbolProperty    SymbolKind = "property"
	SymbolConstant    SymbolKind = "constant"
	SymbolVariable    SymbolKind = "variable"
	SymbolTypeAlias   SymbolKind = "type_alias"
	SymbolType        SymbolKind = "type"
	SymbolObject      SymbolKind = "object"
	SymbolConstructor SymbolKind = "constructor"
	SymbolUnion       SymbolKind = "union"
)

// canonicalKind maps the Go adapter's Kind into the canonical vocabulary.
func canonicalKind(kind Kind) SymbolKind {
	switch kind {
	case KindMethod:
		return SymbolMethod
	case KindType:
		return SymbolType
	case KindVar:
		return SymbolVariable
	case KindConst:
		return SymbolConstant
	case KindPackage, KindImport:
		return SymbolPackage
	default:
		return SymbolFunction
	}
}

// FileKind classifies a file for structural inventory.
//
// It is not evidence provenance. Evidence provenance stays with
// agent.ClassifyPath in the control plane; this classification answers graph
// questions such as "which files are Go source?".
type FileKind string

// File kinds.
const (
	FileGoSource      FileKind = "go_source"
	FileGoTest        FileKind = "go_test"
	FileSource        FileKind = "source"
	FileTest          FileKind = "test"
	FileConfig        FileKind = "config"
	FileManifest      FileKind = "manifest"
	FileDocumentation FileKind = "documentation"
	FileOther         FileKind = "other"
)

// TestKind classifies a Go test declaration.
type TestKind string

// Test declaration kinds.
const (
	TestUnit      TestKind = "test"
	TestBenchmark TestKind = "benchmark"
	TestFuzz      TestKind = "fuzz"
	TestExample   TestKind = "example"
	TestMain      TestKind = "test_main"
)

// TestDecl is one test declaration extracted in the same parse pass as the
// file's symbols.
type TestDecl struct {
	// Symbol is the declaration coordinate. Kind is KindFunc.
	Symbol Match
	// Kind is the static test shape.
	Kind TestKind
	// Framework is the test convention that proved the declaration, e.g.
	// "go-test", "jest", "junit", "xunit", "rust-test", "rspec".
	Framework string
}

// SnapshotInfo is the provenance and completeness header of one published
// structural snapshot.
type SnapshotInfo struct {
	// Root is the absolute index root.
	Root string `json:"root"`
	// Module is the Go module path, empty when there is no usable go.mod.
	Module string `json:"module,omitempty"`
	// ModuleRoot is the nearest directory above Root that carries go.mod.
	// It is metadata only; the index never widens its walk to include it.
	ModuleRoot string `json:"module_root,omitempty"`
	// Generation is process-local and monotonic.
	Generation uint64 `json:"generation"`
	// SnapshotID is deterministic identity for this published snapshot.
	SnapshotID string `json:"snapshot_id"`
	// SourceFingerprint is a cheap hash of path/size/mtime signatures.
	SourceFingerprint string `json:"source_fingerprint"`
	// ScopeFingerprint is the repository-scope rule fingerprint supplied by
	// the injected scope. It changes when a .gitignore rule changes.
	ScopeFingerprint string `json:"scope_fingerprint,omitempty"`
	// State is the current freshness.
	State Freshness `json:"state"`
	// BuiltAt is when this generation was published.
	BuiltAt time.Time `json:"built_at"`
	// Files counts every indexed file, not only Go files.
	Files int `json:"files"`
	// GoFiles counts indexed Go source and test files, regardless of parse state.
	GoFiles int `json:"go_files"`
	// SourceFiles counts every supported source and test file.
	SourceFiles int `json:"source_files"`
	// ParsedFiles counts source files whose canonical facts are published.
	ParsedFiles int `json:"parsed_files"`
	// PendingFiles counts supported source files not parsed yet. A non-zero
	// count is normal after an overview and makes structural absence claims
	// incomplete.
	PendingFiles int `json:"pending_files"`
	// FailedFiles counts source files whose adapter reported a parse failure.
	FailedFiles int `json:"failed_files"`
	// Languages reports per-language inventory and parse coverage.
	Languages []LanguageCoverage `json:"languages,omitempty"`
	// Unsupported counts observed extensions with no registered adapter.
	Unsupported []UnsupportedLanguage `json:"unsupported_languages,omitempty"`
	// InventoryComplete reports that every observed source path was mapped to
	// a language and included in the inventory.
	InventoryComplete bool `json:"inventory_complete"`
	// StructuralComplete reports that every supported source file parsed and
	// no unsupported source-bearing extension remains. CURRENT does not imply
	// this.
	StructuralComplete bool `json:"structural_complete"`
	// Activation is the depth used for the last refresh.
	Activation Activation `json:"activation,omitempty"`
	// Directories counts directories that contain indexed files.
	Directories int `json:"directories"`
	Packages    int `json:"packages"`
	Symbols     int `json:"symbols"`
	References  int `json:"references"`
	Calls       int `json:"calls"`
	Tests       int `json:"tests"`
	Entrypoints int `json:"entrypoints"`
	Configs     int `json:"configs"`
	// ParseFailures counts entries the snapshot could not parse or read.
	ParseFailures int `json:"parse_failures"`
	// Partial reports that at least one observed entry is missing from the
	// snapshot. A consumer must qualify any negative claim when it is true.
	Partial bool `json:"partial"`
	// LastParsedFiles and LastReusedFiles describe the work of the last
	// update. A normal one-file change parses exactly one Go file.
	LastParsedFiles int `json:"last_parsed_files"`
	LastReusedFiles int `json:"last_reused_files"`
	// LastResolution reports whether the last update re-resolved
	// relationships. "none" means only non-Go inventory metadata changed;
	// "global_reresolve" means retained syntax was re-resolved without a
	// re-parse; "full_snapshot" means every Go file was parsed.
	LastResolution string `json:"last_resolution"`
}

// FileInfo is one file fact from the structural inventory.
type FileInfo struct {
	Path     string   `json:"path"`
	Dir      string   `json:"dir"`
	Package  string   `json:"package,omitempty"`
	Language string   `json:"language,omitempty"`
	Adapter  string   `json:"adapter,omitempty"`
	Kind     FileKind `json:"kind"`
	Test     bool     `json:"test"`
	// Parsed reports that this file's declarations and relationships are in
	// the published snapshot. A false value is a pending inventory fact, not
	// an empty file.
	Parsed bool `json:"parsed"`
	// ParseError is the bounded reason a parsed file failed.
	ParseError string `json:"parse_error,omitempty"`
	// Unsupported reports a source extension this build has no adapter for.
	Unsupported bool  `json:"unsupported,omitempty"`
	Size        int64 `json:"size"`
	Symbols     int   `json:"symbols"`
	Exports     int   `json:"exports,omitempty"`
}

// PackageInfo is one Go package aggregate.
type PackageInfo struct {
	Dir       string   `json:"dir"`
	Name      string   `json:"name"`
	Packages  []string `json:"packages,omitempty"`
	Language  Language `json:"language,omitempty"`
	Languages []string `json:"languages,omitempty"`
	Files     int      `json:"files"`
	TestFiles int      `json:"test_files"`
	Symbols   int      `json:"symbols"`
	Imports   int      `json:"imports"`
	Importers int      `json:"importers"`
	Ambiguous bool     `json:"ambiguous"`
}

// Entrypoint is one proven Go entrypoint: a non-test main function in package
// main. A package named main without a main function is not fabricated into an
// entrypoint.
type Entrypoint struct {
	Path          string   `json:"path"`
	Package       string   `json:"package"`
	Symbol        string   `json:"symbol"`
	QualifiedName string   `json:"qualified_name"`
	Language      Language `json:"language,omitempty"`
	NativeKind    string   `json:"native_kind,omitempty"`
	Line          int      `json:"line"`
	EndLine       int      `json:"end_line"`
	Column        int      `json:"column"`
}

// TestInfo is one test declaration coordinate.
type TestInfo struct {
	Path          string   `json:"path"`
	Package       string   `json:"package"`
	Symbol        string   `json:"symbol"`
	QualifiedName string   `json:"qualified_name"`
	Language      Language `json:"language,omitempty"`
	Framework     string   `json:"framework,omitempty"`
	Kind          TestKind `json:"kind"`
	Line          int      `json:"line"`
	EndLine       int      `json:"end_line"`
}

// TestAssociation is one static reference or call from a test function to a
// symbol. It is not coverage: reflection, external processes, and runtime-only
// paths are outside this model.
type TestAssociation struct {
	Test     TestInfo `json:"test"`
	Relation string   `json:"relation"` // references or calls
	Path     string   `json:"path"`
	Line     int      `json:"line"`
	Column   int      `json:"column"`
	Call     bool     `json:"call"`
}

// ConfigInfo is one configuration fact from the allowlist.
type ConfigInfo struct {
	Path      string   `json:"path"`
	Kind      string   `json:"kind"`
	Language  Language `json:"language,omitempty"`
	Framework string   `json:"framework,omitempty"`
	Value     string   `json:"value,omitempty"`
}

// OverviewResult is one bounded project summary from a single snapshot.
type OverviewResult struct {
	Snapshot    SnapshotInfo          `json:"snapshot"`
	Packages    []PackageInfo         `json:"packages,omitempty"`
	Files       []FileInfo            `json:"files,omitempty"`
	Entrypoints []Entrypoint          `json:"entrypoints,omitempty"`
	Configs     []ConfigInfo          `json:"configs,omitempty"`
	Languages   []LanguageCoverage    `json:"languages,omitempty"`
	Unsupported []UnsupportedLanguage `json:"unsupported_languages,omitempty"`
	Frameworks  []string              `json:"frameworks,omitempty"`
	Truncated   bool                  `json:"truncated"`
	Reason      string                `json:"reason,omitempty"`
}

// Query bounds for inventory queries. They deliberately match the existing
// structural vocabulary: a project summary must not become a whole-repository
// dump in context.
const (
	// StructureDefaultLimit is the default inventory page size.
	StructureDefaultLimit = 50
	// StructureMaxLimit is the hard inventory page ceiling.
	StructureMaxLimit = 200
)

// FileQuery selects a bounded file inventory slice.
type FileQuery struct {
	Path     string
	Package  string
	Kind     FileKind
	Language string
	Test     *bool
	Limit    int
	Skip     int
}

// FileResult is one bounded file inventory page.
type FileResult struct {
	Snapshot  SnapshotInfo `json:"snapshot"`
	Files     []FileInfo   `json:"files"`
	Total     int          `json:"total"`
	Truncated bool         `json:"truncated"`
	Reason    string       `json:"reason,omitempty"`
}

// PackageQuery selects a bounded package inventory slice.
type PackageQuery struct {
	Path  string
	Name  string
	Limit int
	Skip  int
}

// PackageResult is one bounded package inventory page.
type PackageResult struct {
	Snapshot  SnapshotInfo  `json:"snapshot"`
	Packages  []PackageInfo `json:"packages"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
	Reason    string        `json:"reason,omitempty"`
}

// EntrypointResult is the complete bounded entrypoint inventory.
type EntrypointResult struct {
	Snapshot    SnapshotInfo `json:"snapshot"`
	Entrypoints []Entrypoint `json:"entrypoints"`
	Truncated   bool         `json:"truncated"`
	Reason      string       `json:"reason,omitempty"`
}

// ConfigResult is the complete bounded configuration inventory.
type ConfigResult struct {
	Snapshot  SnapshotInfo `json:"snapshot"`
	Configs   []ConfigInfo `json:"configs"`
	Truncated bool         `json:"truncated"`
	Reason    string       `json:"reason,omitempty"`
}

// TestQuery asks for test declarations or static associations for one symbol.
type TestQuery struct {
	Symbol  string
	Package string
	Path    string
	Limit   int
	Skip    int
}

// TestResult is a bounded declaration or association page.
type TestResult struct {
	Snapshot     SnapshotInfo      `json:"snapshot"`
	Symbol       string            `json:"symbol,omitempty"`
	Found        bool              `json:"found"`
	Ambiguous    bool              `json:"ambiguous"`
	Candidates   []RefTarget       `json:"candidates,omitempty"`
	Tests        []TestInfo        `json:"tests,omitempty"`
	Associations []TestAssociation `json:"associations,omitempty"`
	Total        int               `json:"total"`
	Truncated    bool              `json:"truncated"`
	Reason       string            `json:"reason,omitempty"`
}

// ImpactQuery asks for the static neighbourhood of a symbol or path.
type ImpactQuery struct {
	Symbol string
	Path   string
	Depth  int
	Limit  int
}

// ImpactResult is one bounded impact projection. It is static reachability,
// not a behavioral claim.
type ImpactResult struct {
	Snapshot   SnapshotInfo      `json:"snapshot"`
	QueryType  string            `json:"query_type"`
	Selector   string            `json:"selector"`
	Found      bool              `json:"found"`
	Ambiguous  bool              `json:"ambiguous"`
	Candidates []RefTarget       `json:"candidates,omitempty"`
	Targets    []RefTarget       `json:"targets,omitempty"`
	References []ReferenceSite   `json:"references,omitempty"`
	Callers    []CallEdge        `json:"callers,omitempty"`
	Importers  []string          `json:"importers,omitempty"`
	Tests      []TestAssociation `json:"tests,omitempty"`
	Nodes      []Node            `json:"nodes,omitempty"`
	Edges      []Edge            `json:"edges,omitempty"`
	Total      int               `json:"total"`
	Truncated  bool              `json:"truncated"`
	Reason     string            `json:"reason,omitempty"`
	Unknown    UnknownSummary    `json:"unknown"`
}

// itoa avoids a strconv import at every identity helper. The values are small
// line numbers and the conversion is not hot enough to justify a shared pool.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// dirOf returns the repository-relative directory of a file key.
func dirOf(rel string) string {
	dir := path.Dir(rel)
	if dir == "" {
		return "."
	}
	return dir
}

// pathInside reports whether rel is the directory dir or below it.
func pathInside(dir, rel string) bool {
	if dir == "" || dir == "." {
		return true
	}
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}
