package ast

import (
	"context"
	"sort"
	"strings"
)

// Dependency projection bounds.
const (
	// DependencyDefaultMaxEdges bounds one whole-snapshot edge projection.
	DependencyDefaultMaxEdges = 5000
	// DependencyMaxEdges is the hard ceiling for the in-process projection.
	DependencyMaxEdges = 200000
)

// DependencyQuery selects one directory-level dependency projection.
//
// The projection aggregates the same import facts the public Graph and refs
// queries already own: Go package imports from the package graph and
// language-neutral file imports from the generic projection. It is derived
// state, never a second parse.
type DependencyQuery struct {
	// MaxEdges bounds the returned edge rows. Zero uses the default.
	MaxEdges int
}

// DependencyNode is one directory that contains at least one indexed file.
//
// It is a directory, not a package: two packages can share a directory, and a
// polyglot directory carries one node with several languages. Packages lists
// the observed package/namespace names so a consumer can tell the two cases
// apart instead of receiving an invented single identity.
type DependencyNode struct {
	Path      string     `json:"path"`
	Packages  []string   `json:"packages,omitempty"`
	Languages []Language `json:"languages,omitempty"`
	Files     int        `json:"files"`
	TestFiles int        `json:"test_files"`
	Symbols   int        `json:"symbols"`
	// ExternalImports counts Go imports that resolve outside the module. It is
	// zero for a repository without a module path because no import can be
	// classified there.
	ExternalImports int `json:"external_imports,omitempty"`
}

// DependencyWitness is the deterministic site that proves one aggregated edge.
//
// A witness is a coordinate, not source text. StartLine is 1-based.
type DependencyWitness struct {
	Path     string   `json:"path"`
	Line     int      `json:"line,omitempty"`
	Language Language `json:"language,omitempty"`
	Test     bool     `json:"test,omitempty"`
	// Import is the declared import/include/require path.
	Import string `json:"import,omitempty"`
}

// DependencyEdge is one directory-to-directory dependency.
//
// It merges every observed declaration between the two directories. Source and
// Test are therefore properties of the aggregate, not of the witness: a
// non-test witness is preferred so an edge that exists in production code is
// not presented through a test file.
type DependencyEdge struct {
	From      string            `json:"from"`
	To        string            `json:"to"`
	Languages []Language        `json:"languages,omitempty"`
	Source    bool              `json:"source,omitempty"`
	Test      bool              `json:"test,omitempty"`
	Witness   DependencyWitness `json:"witness"`
}

// DependencyUnresolved is one dependency-looking declaration the snapshot could
// not resolve. It is reported, never traversed, and never converted into
// "no dependency".
type DependencyUnresolved struct {
	From     string   `json:"from"`
	Import   string   `json:"import"`
	Language Language `json:"language,omitempty"`
	Reason   string   `json:"reason"`
}

// DependencyResult is one deterministic directory dependency projection.
//
// Complete is false when the snapshot has unparsed supported source, a parse
// failure, an unsupported source language, an unavailable Go package graph, or
// a cut edge list. A consumer must not treat an incomplete projection as proof
// that an edge is absent.
type DependencyResult struct {
	Snapshot      SnapshotInfo           `json:"snapshot"`
	Nodes         []DependencyNode       `json:"nodes"`
	Edges         []DependencyEdge       `json:"edges"`
	Unresolved    []DependencyUnresolved `json:"unresolved,omitempty"`
	TotalNodes    int                    `json:"total_nodes"`
	TotalEdges    int                    `json:"total_edges"`
	Truncated     bool                   `json:"truncated"`
	Reason        string                 `json:"reason,omitempty"`
	ParseFailures int                    `json:"parse_failures"`
	Complete      bool                   `json:"complete"`
}

// Dependencies projects the whole snapshot as a language-neutral directory
// dependency graph.
//
// The projection shares the index lock, generation, and parse state with every
// other structural query. It requires parsed source: an inventory-only snapshot
// yields nodes without edges and reports itself incomplete, which is the
// honest answer for a metadata-only activation.
func (idx *Index) Dependencies(ctx context.Context, query DependencyQuery) (DependencyResult, error) {
	if err := ctx.Err(); err != nil {
		return DependencyResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return DependencyResult{}, err
	}
	maxEdges := query.MaxEdges
	if maxEdges <= 0 {
		maxEdges = DependencyDefaultMaxEdges
	}
	if maxEdges > DependencyMaxEdges {
		maxEdges = DependencyMaxEdges
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	if err := idx.ensureParsedLockedCtx(ctx); err != nil {
		return DependencyResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return DependencyResult{}, err
	}

	info := idx.infoLocked()
	result := DependencyResult{
		Snapshot:      info,
		ParseFailures: idx.parseFailuresLocked(),
	}

	nodes := map[string]*DependencyNode{}
	node := func(dir string) *DependencyNode {
		entry := nodes[dir]
		if entry == nil {
			entry = &DependencyNode{Path: dir}
			nodes[dir] = entry
		}
		return entry
	}
	packages := map[string]map[string]bool{}
	languages := map[string]map[Language]bool{}
	for rel, rec := range idx.files {
		dir := packageDir(rel)
		entry := node(dir)
		entry.Files++
		if rec.test {
			entry.TestFiles++
		}
		entry.Symbols += len(rec.symbols)
		if rec.language != "" {
			if languages[dir] == nil {
				languages[dir] = map[Language]bool{}
			}
			languages[dir][Language(rec.language)] = true
		}
		if rec.packageName != "" {
			if packages[dir] == nil {
				packages[dir] = map[string]bool{}
			}
			packages[dir][rec.packageName] = true
		}
	}

	edges := map[string]*DependencyEdge{}
	merge := func(from, to string, language Language, test bool, witness DependencyWitness) {
		if from == "" {
			from = "."
		}
		if to == "" {
			to = "."
		}
		if from == to {
			// A dependency on the same directory carries no boundary
			// information and would dominate fan-in/fan-out counts.
			return
		}
		key := from + "\x00" + to
		edge := edges[key]
		if edge == nil {
			edge = &DependencyEdge{From: from, To: to, Witness: witness}
			edges[key] = edge
		}
		if test {
			edge.Test = true
			if edge.Witness.Path == "" || (edge.Witness.Test && !witness.Test) {
				edge.Witness = witness
			}
		} else {
			edge.Source = true
			// A non-test witness is the stronger provenance.
			if edge.Witness.Test || edge.Witness.Path == "" {
				edge.Witness = witness
			} else if dependencyWitnessLess(witness, edge.Witness) {
				edge.Witness = witness
			}
		}
		edge.Languages = appendLanguageOnce(edge.Languages, language)
	}

	if idx.graph != nil {
		for _, from := range idx.graph.order {
			source := idx.graph.nodes[from]
			if source == nil {
				continue
			}
			entry := node(from)
			entry.ExternalImports = source.externals
			for to, goEdge := range source.imports {
				if goEdge.source {
					merge(from, to, LanguageGo, false, DependencyWitness{
						Path: goEdge.sourceFile, Line: goEdge.sourceLine, Language: LanguageGo,
					})
				}
				if goEdge.test {
					merge(from, to, LanguageGo, true, DependencyWitness{
						Path: goEdge.testFile, Line: goEdge.testLine, Language: LanguageGo, Test: true,
					})
				}
			}
		}
	}

	if idx.languageGraph != nil {
		externalByDir := map[string]int{}
		for _, imported := range idx.languageGraph.imports {
			if imported.resolution == ResolutionResolved {
				continue
			}
			if locallyShapedImport(imported.language, imported.imported.Path) {
				continue
			}
			externalByDir[packageDir(imported.from)]++
		}
		for dir, count := range externalByDir {
			if entry := nodes[dir]; entry != nil {
				entry.ExternalImports += count
			}
		}
		for _, edge := range idx.languageGraph.importEdges {
			from := trimFileNodeID(edge.From)
			to := trimFileNodeID(edge.To)
			if from == "" || to == "" {
				continue
			}
			merge(packageDir(from), packageDir(to), edge.Language, edge.Source.Test,
				DependencyWitness{
					Path: from, Line: edge.Source.StartLine, Language: edge.Language, Import: "",
				})
		}
	}

	for _, entry := range nodes {
		entry.Packages = sortedKeys(packages[entry.Path])
		if set := languages[entry.Path]; set != nil {
			for language := range set {
				entry.Languages = append(entry.Languages, language)
			}
			sort.Slice(entry.Languages, func(i, j int) bool { return entry.Languages[i] < entry.Languages[j] })
		}
	}
	result.Nodes = make([]DependencyNode, 0, len(nodes))
	for _, entry := range nodes {
		result.Nodes = append(result.Nodes, *entry)
	}
	sort.SliceStable(result.Nodes, func(i, j int) bool { return result.Nodes[i].Path < result.Nodes[j].Path })
	result.TotalNodes = len(result.Nodes)

	result.Edges = make([]DependencyEdge, 0, len(edges))
	for _, edge := range edges {
		result.Edges = append(result.Edges, *edge)
	}
	sort.SliceStable(result.Edges, func(i, j int) bool {
		if result.Edges[i].From != result.Edges[j].From {
			return result.Edges[i].From < result.Edges[j].From
		}
		return result.Edges[i].To < result.Edges[j].To
	})
	result.TotalEdges = len(result.Edges)

	result.Complete = true
	if idx.graph == nil && info.GoFiles > 0 {
		result.Complete = false
		result.Reason = "go_graph_unavailable"
	}
	if len(result.Edges) > maxEdges {
		result.Edges = result.Edges[:maxEdges]
		result.Truncated = true
		result.Reason = "edge_limit"
	}

	result.Unresolved = idx.dependencyUnresolvedLocked()
	if len(result.Unresolved) > 0 {
		result.Complete = false
		if result.Reason == "" {
			result.Reason = "unresolved_imports"
		}
	}
	if !info.StructuralComplete || result.ParseFailures > 0 || result.Truncated {
		result.Complete = false
		if result.Reason == "" {
			result.Reason = "partial_snapshot"
		}
	}
	return result, nil
}

// dependencyUnresolvedLocked collects every dependency-shaped declaration the
// snapshot could not resolve. The caller holds idx.mu.
//
// A Go import that resolves outside the module is external, not unresolved: it
// is counted on the node. Only a module-local-looking path with no indexed
// target is a gap, and it is reported with the reason the resolver recorded.
func (idx *Index) dependencyUnresolvedLocked() []DependencyUnresolved {
	seen := map[string]DependencyUnresolved{}
	if idx.graph != nil {
		for _, dir := range idx.graph.order {
			node := idx.graph.nodes[dir]
			if node == nil {
				continue
			}
			for importPath, reason := range node.unresolved {
				key := dir + "\x00" + importPath + "\x00go"
				seen[key] = DependencyUnresolved{
					From: dir, Import: importPath, Language: LanguageGo, Reason: reason,
				}
			}
		}
	}
	if idx.languageGraph != nil {
		for _, imported := range idx.languageGraph.imports {
			if imported.resolution == ResolutionResolved {
				continue
			}
			dir := packageDir(imported.from)
			if !locallyShapedImport(imported.language, imported.imported.Path) {
				// A non-relative import that resolves to nothing is an external
				// dependency in every adapter this build ships. Counting it as
				// a structural gap would make every package.json project look
				// incomplete.
				continue
			}
			reason := "unresolved import target"
			switch imported.resolution {
			case ResolutionAmbiguous:
				reason = "import target is ambiguous"
			case ResolutionUnsupported:
				reason = "the adapter cannot resolve this import form"
			}
			key := dir + "\x00" + imported.imported.Path + "\x00" + string(imported.language)
			seen[key] = DependencyUnresolved{
				From: dir, Import: imported.imported.Path,
				Language: imported.language, Reason: reason,
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]DependencyUnresolved, 0, len(seen))
	for _, entry := range seen {
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].Import != out[j].Import {
			return out[i].Import < out[j].Import
		}
		return out[i].Language < out[j].Language
	})
	return out
}

// trimFileNodeID recovers the repository-relative path from a FileNodeID.
func trimFileNodeID(id string) string {
	const prefix = "file:"
	if len(id) <= len(prefix) || id[:len(prefix)] != prefix {
		return ""
	}
	return id[len(prefix):]
}

// locallyShapedImport reports whether an unresolved import names a repository
// target rather than an external dependency.
//
// The distinction is language-aware on purpose: "react" and "com.google.guava"
// are libraries, while "./missing" is a broken local reference. Only the second
// makes the dependency projection incomplete.
func locallyShapedImport(language Language, spec string) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return false
	}
	switch language {
	case LanguageTypeScript, LanguageTSX, LanguageJavaScript, LanguageJSX, LanguageRuby:
		return strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/")
	case LanguageRust:
		return strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") ||
			strings.HasPrefix(spec, "crate::") || strings.HasPrefix(spec, "super::") ||
			strings.HasPrefix(spec, "self::") || strings.HasPrefix(spec, "crate/")
	case LanguageC, LanguageCpp:
		return strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") ||
			strings.HasPrefix(spec, `"`) || strings.Contains(spec, "/") ||
			strings.HasSuffix(spec, ".h") || strings.HasSuffix(spec, ".hpp")
	default:
		return false
	}
}

func appendLanguageOnce(values []Language, language Language) []Language {
	if language == "" {
		return values
	}
	for _, existing := range values {
		if existing == language {
			return values
		}
	}
	values = append(values, language)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func dependencyWitnessLess(left, right DependencyWitness) bool {
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	return left.Line < right.Line
}
