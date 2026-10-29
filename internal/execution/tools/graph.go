package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// GraphTool answers bounded package-import relationship queries.
//
// It is read-only repository intelligence. It shares the symbols tool's
// ast.Index, so a graph edge and a symbol coordinate always come from the same
// repository snapshot.
type GraphTool struct {
	Base
	// Root bounds every path query.
	Root string

	source *StructuralSource
}

// NewGraphTool returns the relationship tool over source.
//
// source must be the same handle the symbols tool uses; a separate handle
// would parse the repository twice and create two competing snapshots.
func NewGraphTool(source *StructuralSource) *GraphTool {
	root := ""
	if source != nil {
		root = source.Root()
	}
	tool := &GraphTool{Root: root, source: source}
	tool.Base = NewBase("graph",
		"Answer package relationship questions derived from Go import "+
			"declarations. Select exactly one package, path, or symbol, then "+
			"follow imports or imported_by for a bounded depth. Returns "+
			"repository-relative package directories and proven import edges "+
			"with deterministic file:line evidence. External imports are "+
			"counted, unresolved module-local imports are reported, and "+
			"ambiguous selectors are never resolved arbitrarily. This is "+
			"advisory evidence, not permission for any mutation or execution, "+
			"and it does not prove behavioral impact.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"package": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Exact Go package name. Multiple directories with that name are ambiguous.",
				},
				"path": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "File or directory inside the working directory. A file maps to its package directory.",
				},
				"symbol": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Exact symbol or qualified name; resolves to its package directory.",
				},
				"relation": map[string]any{
					"type":        "string",
					"enum":        []any{string(ast.RelationImports), string(ast.RelationImportedBy)},
					"description": "imports (outgoing, default) or imported_by (incoming).",
				},
				"depth": map[string]any{
					"type":        "integer",
					"description": "Traversal depth from the focal package.",
					"minimum":     1.0,
					"maximum":     float64(ast.GraphMaxDepth),
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum package nodes returned.",
					"minimum":     1.0,
					"maximum":     float64(ast.GraphMaxLimit),
				},
				"edges": map[string]any{
					"type":        "integer",
					"description": "Maximum edges returned. Defaults to 2 * limit, capped at 400.",
					"minimum":     1.0,
					"maximum":     float64(ast.GraphMaxEdges),
				},
			},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	tool.SetRequiredLanguage("go")
	tool.SetActionClasses(agent.ActionObserve, agent.ActionRelationship)
	return tool
}

func (t *GraphTool) index() (*ast.Index, error) {
	if t.source == nil {
		return nil, fmt.Errorf("tools: graph tool has no structural source")
	}
	return t.source.Index()
}

// Source returns the structural source the tool reads.
func (t *GraphTool) Source() *StructuralSource { return t.source }

// Execute implements agent.Tool.
func (t *GraphTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	if err := validateGraphArgs(args); err != nil {
		return agent.ErrorResult("%v", err)
	}

	packageArg, err := graphSelectorArg(args, "package")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	pathArg, err := graphSelectorArg(args, "path")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	symbolArg, err := graphSelectorArg(args, "symbol")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	selectors := 0
	for _, value := range []string{packageArg, pathArg, symbolArg} {
		if value != "" {
			selectors++
		}
	}
	if selectors != 1 {
		return agent.ErrorResult("graph requires exactly one of package, path, or symbol")
	}

	relation := ast.RelationImports
	if raw, ok := args["relation"]; ok {
		value, ok := raw.(string)
		if !ok {
			return agent.ErrorResult("argument %q must be a string", "relation")
		}
		relation = ast.GraphRelation(strings.TrimSpace(value))
		switch relation {
		case ast.RelationImports, ast.RelationImportedBy:
		default:
			return agent.ErrorResult("unsupported relation %q (supported: imports, imported_by)", value)
		}
	}

	depth, err := graphIntArg(args, "depth", ast.GraphDefaultDepth)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if depth < 1 || depth > ast.GraphMaxDepth {
		return agent.ErrorResult("depth must be between 1 and %d, got %d", ast.GraphMaxDepth, depth)
	}
	limit, err := graphIntArg(args, "limit", ast.GraphDefaultLimit)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if limit < 1 || limit > ast.GraphMaxLimit {
		return agent.ErrorResult("limit must be between 1 and %d, got %d", ast.GraphMaxLimit, limit)
	}
	edges, err := graphIntArg(args, "edges", 0)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if _, provided := args["edges"]; provided && (edges < 1 || edges > ast.GraphMaxEdges) {
		return agent.ErrorResult("edges must be between 1 and %d, got %d", ast.GraphMaxEdges, edges)
	}

	// The path selector crosses the same containment authority as every other
	// model-supplied path argument. The index then sees a repository-relative
	// spelling.
	if pathArg != "" {
		resolved, err := resolvePath(t.Root, pathArg)
		if err != nil {
			return agent.ErrorResult("resolve path: %v", err)
		}
		rel, err := filepath.Rel(t.Root, resolved)
		if err != nil {
			return agent.ErrorResult("relativize %q: %v", pathArg, err)
		}
		pathArg = filepath.ToSlash(rel)
	}

	idx, err := t.index()
	if err != nil {
		return agent.ErrorResult("build repository index: %v", err)
	}
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	result, err := idx.Graph(ctx.Ctx, ast.GraphQuery{
		Package:   packageArg,
		Path:      pathArg,
		Symbol:    symbolArg,
		Relation:  relation,
		Depth:     depth,
		Limit:     limit,
		EdgeLimit: edges,
	})
	if err != nil {
		if ctxErr := ctx.Ctx.Err(); ctxErr != nil {
			return agent.ErrorResult("cancelled: %v", ctxErr)
		}
		return agent.ErrorResult("graph: %v", err)
	}

	status := "ok"
	switch {
	case result.Ambiguous:
		status = "ambiguous"
	case !result.Found:
		status = "not_found"
	case result.Truncated:
		status = "truncated"
	}

	var text strings.Builder
	fmt.Fprintf(&text, "status: %s\n", status)
	fmt.Fprintf(&text, "module: %s\n", result.Module)
	fmt.Fprintf(&text, "query: %s %s\n", result.QueryType, result.Selector)
	fmt.Fprintf(&text, "relation: %s\n", result.Relation)
	fmt.Fprintf(&text, "depth: %d\n", result.Depth)
	fmt.Fprintf(&text, "total_packages: %d\n", result.TotalPackages)
	fmt.Fprintf(&text, "total_edges: %d\n", result.TotalEdges)
	fmt.Fprintf(&text, "returned_nodes: %d\n", len(result.Nodes))
	fmt.Fprintf(&text, "returned_edges: %d\n", len(result.Edges))
	if result.Ambiguous {
		text.WriteString("ambiguous: true\n")
	}
	if result.Truncated {
		fmt.Fprintf(&text, "truncated: true\n")
		fmt.Fprintf(&text, "truncation_reason: %s\n", result.Reason)
	}
	if result.ParseFailures > 0 {
		fmt.Fprintf(&text, "unparsed: %d\n", result.ParseFailures)
	}
	if result.Reason == "work_limit" {
		fmt.Fprintf(&text, "work: %d\n", result.Work)
	}

	if result.Ambiguous {
		text.WriteString("\ncandidates:\n")
		for _, candidate := range result.Candidates {
			fmt.Fprintf(&text, "  - %s\n", candidate)
		}
	}
	if len(result.Nodes) > 0 {
		text.WriteString("\nnodes:\n")
		for _, node := range result.Nodes {
			fmt.Fprintf(&text, "  - path: %s\n", node.Path)
			fmt.Fprintf(&text, "    package: %s\n", node.Package)
			fmt.Fprintf(&text, "    depth: %d\n", node.Depth)
			fmt.Fprintf(&text, "    files: %d\n", node.Files)
			fmt.Fprintf(&text, "    test_files: %d\n", node.TestFiles)
			fmt.Fprintf(&text, "    external_imports: %d\n", node.ExternalImports)
			if node.Ambiguous {
				text.WriteString("    ambiguous: true\n")
			}
			if node.Ambiguous && len(node.Packages) > 1 {
				fmt.Fprintf(&text, "    packages: [%s]\n", strings.Join(node.Packages, ", "))
			}
		}
	}
	if len(result.Edges) > 0 {
		text.WriteString("\nedges:\n")
		for _, edge := range result.Edges {
			fmt.Fprintf(&text, "  - from: %s\n", edge.From)
			fmt.Fprintf(&text, "    to: %s\n", edge.To)
			fmt.Fprintf(&text, "    kinds: %s\n", edgeKinds(edge))
			if edge.Source && edge.SourceFile != "" {
				fmt.Fprintf(&text, "    source_evidence: %s:%d\n", edge.SourceFile, edge.SourceLine)
			}
			if edge.Test && edge.TestFile != "" {
				fmt.Fprintf(&text, "    test_evidence: %s:%d\n", edge.TestFile, edge.TestLine)
			}
			if edge.TargetAmbiguous {
				text.WriteString("    target_ambiguous: true\n")
			}
		}
	}
	if len(result.Unresolved) > 0 {
		text.WriteString("\nunresolved:\n")
		for _, unresolved := range result.Unresolved {
			fmt.Fprintf(&text, "  - from: %s\n", unresolved.From)
			fmt.Fprintf(&text, "    import: %s\n", unresolved.Import)
			fmt.Fprintf(&text, "    reason: %s\n", unresolved.Reason)
		}
	}

	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: graphDetails(status, result),
		Useless: status == "not_found" && result.ParseFailures == 0,
	}
}

// validateGraphArgs rejects arguments outside the closed schema. The loop
// validates model-supplied arguments before Execute; this check also protects
// direct callers and documents the closed set at the tool boundary.
func validateGraphArgs(args map[string]any) error {
	allowed := map[string]bool{
		"package": true, "path": true, "symbol": true,
		"relation": true, "depth": true, "limit": true, "edges": true,
	}
	for key := range args {
		if !allowed[key] {
			return fmt.Errorf("unknown graph argument %q", key)
		}
	}
	return nil
}

// graphSelectorArg reads one optional selector and rejects an empty value.
func graphSelectorArg(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("argument %q must not be empty", key)
	}
	return value, nil
}

// graphIntArg reads an optional integer argument.
func graphIntArg(args map[string]any, key string, fallback int) (int, error) {
	if _, ok := args[key]; !ok {
		return fallback, nil
	}
	return intArg(args, key, fallback)
}

func edgeKinds(edge ast.GraphEdge) string {
	switch {
	case edge.Source && edge.Test:
		return "source,test"
	case edge.Test:
		return "test"
	default:
		return "source"
	}
}

// graphDetails is the structured payload for the UI and benchmarks.
func graphDetails(status string, result ast.GraphResult) map[string]any {
	nodes := make([]map[string]any, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		nodes = append(nodes, map[string]any{
			"path":             node.Path,
			"package":          node.Package,
			"packages":         append([]string(nil), node.Packages...),
			"depth":            node.Depth,
			"files":            node.Files,
			"test_files":       node.TestFiles,
			"ambiguous":        node.Ambiguous,
			"external_imports": node.ExternalImports,
			"imports":          node.Imports,
		})
	}
	edges := make([]map[string]any, 0, len(result.Edges))
	for _, edge := range result.Edges {
		edges = append(edges, map[string]any{
			"from":             edge.From,
			"to":               edge.To,
			"source":           edge.Source,
			"test":             edge.Test,
			"source_file":      edge.SourceFile,
			"source_line":      edge.SourceLine,
			"test_file":        edge.TestFile,
			"test_line":        edge.TestLine,
			"target_ambiguous": edge.TargetAmbiguous,
		})
	}
	unresolved := make([]map[string]any, 0, len(result.Unresolved))
	for _, item := range result.Unresolved {
		unresolved = append(unresolved, map[string]any{
			"from":   item.From,
			"import": item.Import,
			"reason": item.Reason,
		})
	}
	return map[string]any{
		"status":            status,
		"module":            result.Module,
		"query_type":        result.QueryType,
		"selector":          result.Selector,
		"relation":          string(result.Relation),
		"depth":             result.Depth,
		"found":             result.Found,
		"ambiguous":         result.Ambiguous,
		"candidates":        append([]string(nil), result.Candidates...),
		"nodes":             nodes,
		"edges":             edges,
		"unresolved":        unresolved,
		"total_packages":    result.TotalPackages,
		"total_edges":       result.TotalEdges,
		"truncated":         result.Truncated,
		"truncation_reason": result.Reason,
		"parse_failures":    result.ParseFailures,
		"work":              result.Work,
	}
}
