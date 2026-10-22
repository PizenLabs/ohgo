package ast

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Graph traversal defaults and hard bounds.
//
// The bounds are deliberately small: a model asking "what depends on this?"
// needs a neighbourhood, not the whole repository serialised into context.
const (
	// GraphDefaultDepth is the default traversal depth.
	GraphDefaultDepth = 1
	// GraphMaxDepth is the hard depth ceiling.
	GraphMaxDepth = 4
	// GraphDefaultLimit is the default number of package nodes returned.
	GraphDefaultLimit = 50
	// GraphMaxLimit is the hard node ceiling.
	GraphMaxLimit = 200
	// GraphMaxEdges is the hard edge ceiling.
	GraphMaxEdges = 400
	// GraphWorkLimit bounds edge inspections for one query.
	GraphWorkLimit = 100_000
)

// GraphRelation selects the direction of a package traversal.
type GraphRelation string

// Supported graph relations.
const (
	// RelationImports follows outgoing import edges.
	RelationImports GraphRelation = "imports"
	// RelationImportedBy follows incoming import edges.
	RelationImportedBy GraphRelation = "imported_by"
)

// GraphQuery is one bounded package-graph request.
//
// Exactly one of Package, Path, or Symbol selects the focal node. The other
// fields carry explicit bounds; zero values are replaced by the documented
// defaults by Index.Graph, while out-of-range non-zero values are errors.
type GraphQuery struct {
	// Package selects a package by exact Go package name.
	Package string
	// Path selects a file or directory inside the index root.
	Path string
	// Symbol selects a package by an exact symbol or qualified name.
	Symbol string
	// Relation is imports (default) or imported_by.
	Relation GraphRelation
	// Depth is how many edges to traverse from the focal node.
	Depth int
	// Limit is the maximum number of package nodes returned.
	Limit int
	// EdgeLimit is the maximum number of edges returned.
	EdgeLimit int
}

// GraphNode is one package in a graph result.
type GraphNode struct {
	// Path is the repository-relative package directory.
	Path string
	// Package is the canonical package name, empty when ambiguous.
	Package string
	// Packages lists conflicting package names when the directory is
	// ambiguous.
	Packages []string
	// Depth is the traversal distance from the focal node.
	Depth int
	// Files and TestFiles count the parsed files in the directory.
	Files     int
	TestFiles int
	// Ambiguous reports conflicting package identities in the directory.
	Ambiguous bool
	// ExternalImports counts imports that resolve outside the module.
	ExternalImports int
	// Imports is the number of module-local outgoing edges.
	Imports int
}

// GraphEdge is one proven package import edge.
type GraphEdge struct {
	// From imports To.
	From string
	// To is the imported package directory.
	To string
	// Source reports a non-test file declares the import.
	Source bool
	// Test reports a _test.go file declares the import.
	Test bool
	// SourceFile and SourceLine identify a deterministic non-test witness.
	SourceFile string
	SourceLine int
	// TestFile and TestLine identify a deterministic test witness.
	TestFile string
	TestLine int
	// TargetAmbiguous reports that To's package identity is ambiguous.
	TargetAmbiguous bool
}

// GraphUnresolved is a module-local-looking import with no indexed target.
//
// It is reported instead of being dropped or guessed. It is never traversed.
type GraphUnresolved struct {
	// From is the importing package directory.
	From string
	// Import is the declared import path.
	Import string
	// Reason explains why the path could not be resolved.
	Reason string
}

// GraphResult is the deterministic answer to one GraphQuery.
type GraphResult struct {
	// Module is the go.mod module path the edges were resolved against.
	Module string
	// QueryType is package, path, or symbol.
	QueryType string
	// Selector is the value the caller supplied.
	Selector string
	// Relation is the traversal direction.
	Relation GraphRelation
	// Depth is the effective depth.
	Depth int
	// Found reports whether the focal node resolved.
	Found bool
	// Ambiguous reports that the selector matched more than one package.
	Ambiguous bool
	// Candidates lists the package directories when Ambiguous is true.
	Candidates []string
	// Nodes are the visited packages, ordered by depth then path.
	Nodes []GraphNode
	// Edges are the traversed import edges, ordered by from then to.
	Edges []GraphEdge
	// Unresolved are unresolved imports for the included nodes.
	Unresolved []GraphUnresolved
	// TotalPackages and TotalEdges describe the whole indexed graph.
	TotalPackages int
	TotalEdges    int
	// Truncated reports that a bound stopped the traversal.
	Truncated bool
	// Reason names the bound: depth, node_limit, edge_limit, or work_limit.
	Reason string
	// ParseFailures is the number of entries the index could not parse or
	// read. A positive count makes the result partial, not a proven negative.
	ParseFailures int
	// Work is the number of edge inspections performed.
	Work int
}

// packageGraph is the bounded relationship projection of one index build.
//
// It contains only aggregated package nodes and module-local import edges. It
// is not a second repository index: symbols, paths, and imports come from the
// same file records that the coordinate queries use.
type packageGraph struct {
	module  string
	nodes   map[string]*graphNode
	order   []string
	edgeNum int
}

type graphNode struct {
	path        string
	name        string
	packages    []string
	files       int
	testFiles   int
	ambiguous   bool
	externals   int
	externalSet map[string]bool
	// imports is keyed by target package directory. One entry aggregates all
	// files that declare the import.
	imports map[string]*graphEdge
	// unresolved is keyed by import path and keeps the first deterministic
	// reason. Multiple files importing the same missing path merge.
	unresolved map[string]string
}

// buildPackageGraph derives the graph from parsed file records.
func buildPackageGraph(records map[string]fileRecord, module string) *packageGraph {
	g := &packageGraph{module: module, nodes: map[string]*graphNode{}}

	// First pass: create a node for every directory with at least one parsed
	// file. The package clause is the identity input; a parse failure removes
	// the file from this projection and is reported as unparsed.
	type dirNames struct {
		nonTest map[string]bool
		test    map[string]bool
	}
	names := map[string]*dirNames{}
	for rel, rec := range records {
		if rec.err != nil {
			continue
		}
		dir := packageDir(rel)
		node := g.nodes[dir]
		if node == nil {
			node = &graphNode{
				path:        dir,
				imports:     map[string]*graphEdge{},
				unresolved:  map[string]string{},
				externalSet: map[string]bool{},
			}
			g.nodes[dir] = node
		}
		node.files++
		if isTestFile(rel) {
			node.testFiles++
		}
		set := names[dir]
		if set == nil {
			set = &dirNames{nonTest: map[string]bool{}, test: map[string]bool{}}
			names[dir] = set
		}
		name := rec.packageName
		if name == "" {
			continue
		}
		if isTestFile(rel) {
			set.test[name] = true
		} else {
			set.nonTest[name] = true
		}
	}

	// Resolve each directory's package identity. A directory is ambiguous
	// when its non-test files disagree, or a test package is neither the
	// base name nor base+"_test". We never pick one of several names.
	for dir, node := range g.nodes {
		set := names[dir]
		all := map[string]bool{}
		if set != nil {
			for name := range set.nonTest {
				all[name] = true
			}
			for name := range set.test {
				all[name] = true
			}
		}
		node.packages = sortedKeys(all)
		switch len(set.nonTest) {
		case 1:
			base := onlyKey(set.nonTest)
			node.name = base
			for name := range set.test {
				if name != base && name != base+"_test" {
					node.ambiguous = true
				}
			}
		case 0:
			// A directory with only _test.go files has no base package
			// identity. It is discoverable by path, but not claimed as a
			// uniquely named package.
			node.ambiguous = true
		default:
			node.ambiguous = true
		}
		if node.ambiguous {
			node.name = ""
		}
	}

	// Second pass: aggregate imports into edges. External imports are a
	// count; module-local imports that do not resolve are explicit.
	for rel, rec := range records {
		if rec.err != nil {
			continue
		}
		dir := packageDir(rel)
		node := g.nodes[dir]
		if node == nil {
			continue
		}
		for _, imp := range rec.imports {
			if module == "" {
				// Without a module path no import can be proven local. The
				// graph query reports the missing module rather than serving
				// an empty edge set as authoritative.
				node.externalSet[imp.Path] = true
				continue
			}
			target, local := moduleLocalTarget(module, imp.Path)
			if !local {
				node.externalSet[imp.Path] = true
				continue
			}
			if unsafeModuleTarget(target) {
				if _, ok := node.unresolved[imp.Path]; !ok {
					node.unresolved[imp.Path] = "import path escapes the module root"
				}
				continue
			}
			targetNode := g.nodes[target]
			if targetNode == nil {
				if _, ok := node.unresolved[imp.Path]; !ok {
					node.unresolved[imp.Path] = "no indexed package at that path"
				}
				continue
			}
			edge := node.imports[target]
			if edge == nil {
				edge = &graphEdge{from: dir, to: target}
				node.imports[target] = edge
			}
			if imp.Test {
				edge.test = true
				if edge.testFile == "" || rel < edge.testFile || (rel == edge.testFile && imp.Line < edge.testLine) {
					edge.testFile = rel
					edge.testLine = imp.Line
				}
			} else {
				edge.source = true
				if edge.sourceFile == "" || rel < edge.sourceFile || (rel == edge.sourceFile && imp.Line < edge.sourceLine) {
					edge.sourceFile = rel
					edge.sourceLine = imp.Line
				}
			}
			edge.targetAmbiguous = targetNode.ambiguous
		}
	}

	g.order = make([]string, 0, len(g.nodes))
	for key := range g.nodes {
		g.order = append(g.order, key)
	}
	sort.Strings(g.order)
	for _, key := range g.order {
		node := g.nodes[key]
		node.externals = len(node.externalSet)
		node.externalSet = nil
		g.edgeNum += len(node.imports)
	}
	return g
}

// moduleLocalTarget maps an import path to a repository-relative directory.
// It reports false when the import is outside the module.
func moduleLocalTarget(module, importPath string) (string, bool) {
	if importPath == "" || strings.HasPrefix(importPath, ".") {
		return "", false
	}
	if importPath == module {
		return ".", true
	}
	prefix := module + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	target := strings.TrimPrefix(importPath, prefix)
	if target == "" {
		return ".", true
	}
	return path.Clean(target), true
}

// unsafeModuleTarget rejects a cleaned target that leaves the module root.
func unsafeModuleTarget(target string) bool {
	return target == ".." || strings.HasPrefix(target, "../")
}

// packageDir returns the repository-relative directory for a file key.
func packageDir(rel string) string {
	dir := path.Dir(rel)
	if dir == "" {
		return "."
	}
	return dir
}

// isTestFile reports the Go test-file suffix.
func isTestFile(rel string) bool {
	return strings.HasSuffix(rel, "_test.go")
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func onlyKey(set map[string]bool) string {
	for key := range set {
		return key
	}
	return ""
}

// Graph answers one bounded package-relationship query.
//
// The query shares the index's freshness check and snapshot. A parse failure
// is reported as ParseFailures; a cancelled context returns the context error
// rather than a partial answer. The caller owns the error taxonomy: a missing
// module, malformed selector, or invalid bound is a State/Discovery error.
func (idx *Index) Graph(ctx context.Context, query GraphQuery) (GraphResult, error) {
	if err := ctx.Err(); err != nil {
		return GraphResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return GraphResult{}, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.graph == nil {
		return GraphResult{}, fmt.Errorf("ast: graph snapshot is unavailable")
	}
	if idx.modulePath == "" {
		return GraphResult{}, fmt.Errorf("ast: graph requires a module path in go.mod")
	}

	relation := query.Relation
	if relation == "" {
		relation = RelationImports
	}
	if relation != RelationImports && relation != RelationImportedBy {
		return GraphResult{}, fmt.Errorf("ast: unsupported graph relation %q", relation)
	}
	depth, limit, edgeLimit, err := normalizeGraphBounds(query)
	if err != nil {
		return GraphResult{}, err
	}

	result := GraphResult{
		Module:        idx.modulePath,
		Relation:      relation,
		Depth:         depth,
		TotalPackages: len(idx.graph.order),
		TotalEdges:    idx.graph.edgeNum,
		ParseFailures: idx.parseFailuresLocked(),
	}

	focal, ambiguous, candidates, queryType, selector, err := idx.resolveGraphFocalLocked(query)
	if err != nil {
		return GraphResult{}, err
	}
	result.QueryType = queryType
	result.Selector = selector
	if ambiguous {
		result.Ambiguous = true
		result.Candidates = candidates
		return result, nil
	}
	if focal == "" {
		return result, nil
	}
	result.Found = true

	g := idx.graph
	visited := map[string]bool{focal: true}
	nodes := []*graphNode{g.nodes[focal]}
	depths := map[string]int{focal: 0}
	edges := make([]GraphEdge, 0, edgeLimit)
	queue := []string{focal}
	queueDepth := []int{0}
	seenEdge := map[string]bool{}
	truncated := false
	reason := ""
	work := 0

	// Reverse traversal needs an importer adjacency. It is built on demand
	// from the same forward edges; it is not a second stored graph.
	var reverse map[string][]graphEdge
	if relation == RelationImportedBy {
		reverse = make(map[string][]graphEdge, len(g.order))
		for _, from := range g.order {
			node := g.nodes[from]
			for _, target := range sortedEdgeKeys(node.imports) {
				work++
				if work > GraphWorkLimit {
					truncated, reason = true, "work_limit"
					break
				}
				edge := node.imports[target]
				reverse[target] = append(reverse[target], graphEdge{
					from:            from,
					to:              target,
					source:          edge.source,
					test:            edge.test,
					sourceFile:      edge.sourceFile,
					sourceLine:      edge.sourceLine,
					testFile:        edge.testFile,
					testLine:        edge.testLine,
					targetAmbiguous: edge.targetAmbiguous,
				})
			}
			if truncated {
				break
			}
		}
		for key := range reverse {
			sort.Slice(reverse[key], func(i, j int) bool {
				return reverse[key][i].from < reverse[key][j].from
			})
		}
	}

	for pos := 0; pos < len(queue) && !truncated; pos++ {
		if err := ctx.Err(); err != nil {
			return GraphResult{}, err
		}
		from := queue[pos]
		fromDepth := queueDepth[pos]
		if fromDepth >= depth {
			continue
		}
		var outgoing []graphEdge
		if relation == RelationImportedBy {
			outgoing = reverse[from]
		} else {
			node := g.nodes[from]
			for _, target := range sortedEdgeKeys(node.imports) {
				edge := node.imports[target]
				outgoing = append(outgoing, graphEdge{
					from:            from,
					to:              target,
					source:          edge.source,
					test:            edge.test,
					sourceFile:      edge.sourceFile,
					sourceLine:      edge.sourceLine,
					testFile:        edge.testFile,
					testLine:        edge.testLine,
					targetAmbiguous: edge.targetAmbiguous,
				})
			}
		}
		for _, edge := range outgoing {
			work++
			if work > GraphWorkLimit {
				truncated, reason = true, "work_limit"
				break
			}
			next := edge.to
			if relation == RelationImportedBy {
				next = edge.from
			}
			if next == "" || g.nodes[next] == nil {
				continue
			}
			key := edge.from + "\x00" + edge.to
			if seenEdge[key] {
				continue
			}
			if !visited[next] && len(nodes) >= limit {
				truncated, reason = true, "node_limit"
				break
			}
			if len(edges) >= edgeLimit {
				truncated, reason = true, "edge_limit"
				break
			}
			if !visited[next] {
				visited[next] = true
				depths[next] = fromDepth + 1
				nodes = append(nodes, g.nodes[next])
				queue = append(queue, next)
				queueDepth = append(queueDepth, fromDepth+1)
			}
			seenEdge[key] = true
			edges = append(edges, GraphEdge{
				From:            edge.from,
				To:              edge.to,
				Source:          edge.source,
				Test:            edge.test,
				SourceFile:      edge.sourceFile,
				SourceLine:      edge.sourceLine,
				TestFile:        edge.testFile,
				TestLine:        edge.testLine,
				TargetAmbiguous: edge.targetAmbiguous,
			})
		}
	}

	// A node at the requested depth may still have relationships beyond the
	// scope. Mark the result depth-limited rather than presenting it as a
	// complete traversal; the caller can raise depth explicitly.
	if !truncated {
		for _, node := range nodes {
			if depths[node.path] != depth {
				continue
			}
			if relation == RelationImportedBy {
				if len(reverse[node.path]) > 0 {
					truncated, reason = true, "depth"
					break
				}
			} else if len(node.imports) > 0 {
				truncated, reason = true, "depth"
				break
			}
		}
	}
	if truncated && reason == "" {
		reason = "work_limit"
	}

	result.Nodes = make([]GraphNode, 0, len(nodes))
	for _, node := range nodes {
		view := graphNodeView(node, depths[node.path])
		if node.unresolved != nil {
			for _, importPath := range sortedStringMapKeys(node.unresolved) {
				result.Unresolved = append(result.Unresolved, GraphUnresolved{
					From:   node.path,
					Import: importPath,
					Reason: node.unresolved[importPath],
				})
			}
		}
		result.Nodes = append(result.Nodes, view)
	}
	sort.SliceStable(result.Nodes, func(i, j int) bool {
		if result.Nodes[i].Depth != result.Nodes[j].Depth {
			return result.Nodes[i].Depth < result.Nodes[j].Depth
		}
		return result.Nodes[i].Path < result.Nodes[j].Path
	})
	result.Edges = edges
	sort.Slice(result.Edges, func(i, j int) bool {
		if result.Edges[i].From != result.Edges[j].From {
			return result.Edges[i].From < result.Edges[j].From
		}
		return result.Edges[i].To < result.Edges[j].To
	})
	sort.Slice(result.Unresolved, func(i, j int) bool {
		if result.Unresolved[i].From != result.Unresolved[j].From {
			return result.Unresolved[i].From < result.Unresolved[j].From
		}
		return result.Unresolved[i].Import < result.Unresolved[j].Import
	})
	result.Truncated = truncated
	result.Reason = reason
	result.Work = work
	return result, nil
}

// parseFailuresLocked is ParseFailures with idx.mu held.
func (idx *Index) parseFailuresLocked() int {
	count := len(idx.unreadable)
	for _, rec := range idx.files {
		if rec.err != nil {
			count++
		}
	}
	return count
}

// normalizeGraphBounds applies defaults and rejects out-of-range bounds.
func normalizeGraphBounds(query GraphQuery) (int, int, int, error) {
	depth := query.Depth
	if depth == 0 {
		depth = GraphDefaultDepth
	}
	if depth < 1 || depth > GraphMaxDepth {
		return 0, 0, 0, fmt.Errorf("ast: graph depth must be between 1 and %d, got %d", GraphMaxDepth, depth)
	}
	limit := query.Limit
	if limit == 0 {
		limit = GraphDefaultLimit
	}
	if limit < 1 || limit > GraphMaxLimit {
		return 0, 0, 0, fmt.Errorf("ast: graph limit must be between 1 and %d, got %d", GraphMaxLimit, limit)
	}
	edgeLimit := query.EdgeLimit
	if edgeLimit == 0 {
		edgeLimit = limit * 2
		if edgeLimit > GraphMaxEdges {
			edgeLimit = GraphMaxEdges
		}
	}
	if edgeLimit < 1 || edgeLimit > GraphMaxEdges {
		return 0, 0, 0, fmt.Errorf("ast: graph edge limit must be between 1 and %d, got %d", GraphMaxEdges, edgeLimit)
	}
	return depth, limit, edgeLimit, nil
}

// resolveGraphFocalLocked maps the single selector to a package directory.
// It returns an empty focal when the selector has no match. The caller holds
// idx.mu.
func (idx *Index) resolveGraphFocalLocked(query GraphQuery) (focal string, ambiguous bool, candidates []string, queryType, selector string, err error) {
	selectors := 0
	if query.Package != "" {
		selectors++
	}
	if query.Path != "" {
		selectors++
	}
	if query.Symbol != "" {
		selectors++
	}
	if selectors != 1 {
		return "", false, nil, "", "", fmt.Errorf("ast: graph requires exactly one of package, path, or symbol")
	}

	if query.Package != "" {
		var matches []string
		for _, key := range idx.graph.order {
			node := idx.graph.nodes[key]
			if node.name == query.Package || containsString(node.packages, query.Package) {
				matches = append(matches, key)
			}
		}
		sort.Strings(matches)
		if len(matches) > 1 {
			return "", true, matches, "package", query.Package, nil
		}
		if len(matches) == 1 {
			return matches[0], false, nil, "package", query.Package, nil
		}
		return "", false, nil, "package", query.Package, nil
	}

	if query.Path != "" {
		rel, err := normalizeIndexRel(idx.Root, query.Path)
		if err != nil {
			return "", false, nil, "path", query.Path, err
		}
		rel = strings.TrimPrefix(path.Clean(filepath.ToSlash(rel)), "./")
		if rel == "" {
			rel = "."
		}
		if idx.graph.nodes[rel] != nil {
			return rel, false, nil, "path", query.Path, nil
		}
		// A file selector maps to its directory. A file that failed to
		// parse is still known to the index, but its directory may not have
		// a package node; reporting not_found plus ParseFailures is honest.
		if _, ok := idx.files[rel]; ok {
			dir := packageDir(rel)
			if idx.graph.nodes[dir] != nil {
				return dir, false, nil, "path", query.Path, nil
			}
		}
		return "", false, nil, "path", query.Path, nil
	}

	// Symbol selector: exact name or qualified name, matching the symbols
	// contract. Multiple declaration directories are ambiguous; multiple
	// declarations in one directory resolve to one package node.
	dirs := map[string]bool{}
	for rel, rec := range idx.files {
		for _, sym := range rec.symbols {
			if sym.Name == query.Symbol || sym.QualifiedName == query.Symbol {
				dirs[packageDir(rel)] = true
				break
			}
		}
	}
	if len(dirs) > 1 {
		return "", true, sortedKeys(dirs), "symbol", query.Symbol, nil
	}
	if len(dirs) == 1 {
		return onlyKey(dirs), false, nil, "symbol", query.Symbol, nil
	}
	return "", false, nil, "symbol", query.Symbol, nil
}

func graphNodeView(node *graphNode, depth int) GraphNode {
	return GraphNode{
		Path:            node.path,
		Package:         node.name,
		Packages:        append([]string(nil), node.packages...),
		Depth:           depth,
		Files:           node.files,
		TestFiles:       node.testFiles,
		Ambiguous:       node.ambiguous,
		ExternalImports: node.externals,
		Imports:         len(node.imports),
	}
}

func sortedEdgeKeys(edges map[string]*graphEdge) []string {
	out := make([]string, 0, len(edges))
	for key := range edges {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedStringMapKeys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// graphEdge is the internal mutable form of a GraphEdge.
type graphEdge struct {
	from            string
	to              string
	source          bool
	test            bool
	sourceFile      string
	sourceLine      int
	testFile        string
	testLine        int
	targetAmbiguous bool
}
