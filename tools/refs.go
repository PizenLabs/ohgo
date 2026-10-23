package tools

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ast"
)

// RefsTool answers bounded symbol-reference and direct-call questions.
//
// It is read-only repository intelligence over the same ast.Index the
// symbols and graph tools use. It never executes a command, writes a file,
// changes authority, or contributes completion evidence.
type RefsTool struct {
	Base
	// Root bounds every path selector.
	Root string

	source *StructuralSource
}

// NewRefsTool returns the relationship tool over source.
//
// source must be the same handle the symbols and graph tools use; a separate
// handle would build a second repository snapshot with independent freshness.
func NewRefsTool(source *StructuralSource) *RefsTool {
	root := ""
	if source != nil {
		root = source.Root()
	}
	tool := &RefsTool{Root: root, source: source}
	tool.Base = NewBase("refs",
		"Answer symbol reference and direct-call questions over the shared "+
			"repository index. Select exactly one exact symbol (or qualified "+
			"name), then ask where it is referenced, who calls it, or what it "+
			"calls. Every result carries repository-relative file:line "+
			"coordinates. Package-qualified and unqualified references to "+
			"package-scope declarations are static; method dispatch, function "+
			"values, and dot imports are reported as explicit unknowns rather "+
			"than guessed. This is advisory evidence, not permission and not "+
			"completion.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"symbol"},
			"properties": map[string]any{
				"symbol": map[string]any{
					"type":      "string",
					"minLength": 1,
					"description": "Exact symbol or qualified name, case-sensitive. " +
						"Examples: resolvePath, tools.resolvePath, GrepTool.Execute.",
				},
				"relation": map[string]any{
					"type":        "string",
					"enum":        []any{string(ast.RelationReferences), string(ast.RelationCallers), string(ast.RelationCallees)},
					"description": "references (default), callers, or callees.",
				},
				"path": map[string]any{
					"type":      "string",
					"minLength": 1,
					"description": "File or directory inside the working directory; scopes " +
						"the symbol declaration and disambiguates a repeated name.",
				},
				"depth": map[string]any{
					"type":        "integer",
					"description": "Call-graph traversal depth for callers/callees.",
					"minimum":     1.0,
					"maximum":     float64(ast.RefMaxDepth),
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum references returned, or call-graph nodes for a transitive callers/callees traversal. Direct callers/callees use it as the edge page size.",
					"minimum":     1.0,
					"maximum":     float64(ast.RefMaxLimit),
				},
				"edges": map[string]any{
					"type":        "integer",
					"description": "Maximum call edges returned by a transitive callers/callees traversal. Defaults to 2 * limit, capped at 400. Not valid for a direct call query.",
					"minimum":     1.0,
					"maximum":     float64(ast.RefMaxEdges),
				},
				"skip": map[string]any{
					"type":        "integer",
					"description": "Rows to skip: references, direct call edges, or traversal call edges.",
					"minimum":     0.0,
				},
			},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	tool.SetRequiredLanguage("go")
	tool.SetActionClasses(agent.ActionObserve, agent.ActionRelationship)
	return tool
}

func (t *RefsTool) index() (*ast.Index, error) {
	if t.source == nil {
		return nil, fmt.Errorf("tools: refs tool has no structural source")
	}
	return t.source.Index()
}

// Source returns the structural source the tool reads.
func (t *RefsTool) Source() *StructuralSource { return t.source }

// Execute implements agent.Tool.
func (t *RefsTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	if err := validateRefsArgs(args); err != nil {
		return agent.ErrorResult("%v", err)
	}

	symbol, err := strArg(args, "symbol", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return agent.ErrorResult("argument %q must not be empty", "symbol")
	}

	relation := ast.RelationReferences
	if raw, ok := args["relation"]; ok {
		value, ok := raw.(string)
		if !ok {
			return agent.ErrorResult("argument %q must be a string", "relation")
		}
		relation = ast.RefRelation(strings.TrimSpace(value))
		switch relation {
		case ast.RelationReferences, ast.RelationCallers, ast.RelationCallees:
		default:
			return agent.ErrorResult("unsupported relation %q (supported: references, callers, callees)", value)
		}
	}

	pathArg, err := strArg(args, "path", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	pathArg = strings.TrimSpace(pathArg)
	if _, provided := args["path"]; provided && pathArg == "" {
		return agent.ErrorResult("argument %q must not be empty", "path")
	}
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

	depth, err := graphIntArg(args, "depth", 0)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if _, provided := args["depth"]; provided && (depth < 1 || depth > ast.RefMaxDepth) {
		return agent.ErrorResult("depth must be between 1 and %d, got %d", ast.RefMaxDepth, depth)
	}
	if relation == ast.RelationReferences && depth > 1 {
		return agent.ErrorResult("references do not take a traversal depth")
	}

	limit, err := graphIntArg(args, "limit", ast.RefDefaultLimit)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if limit < 1 || limit > ast.RefMaxLimit {
		return agent.ErrorResult("limit must be between 1 and %d, got %d", ast.RefMaxLimit, limit)
	}
	edges, err := graphIntArg(args, "edges", 0)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if _, provided := args["edges"]; provided {
		if edges < 1 || edges > ast.RefMaxEdges {
			return agent.ErrorResult("edges must be between 1 and %d, got %d", ast.RefMaxEdges, edges)
		}
		if relation == ast.RelationReferences || depth <= 1 {
			return agent.ErrorResult("edges applies only to a transitive callers/callees traversal; use limit for a direct call page")
		}
	}
	skip, err := graphIntArg(args, "skip", 0)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if skip < 0 {
		return agent.ErrorResult("skip must be >= 0, got %d", skip)
	}

	idx, err := t.index()
	if err != nil {
		return agent.ErrorResult("build repository index: %v", err)
	}
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	result, err := idx.References(ctx.Ctx, ast.RefQuery{
		Symbol:    symbol,
		Relation:  relation,
		Path:      pathArg,
		Depth:     depth,
		Limit:     limit,
		EdgeLimit: edges,
		Skip:      skip,
	})
	if err != nil {
		if ctxErr := ctx.Ctx.Err(); ctxErr != nil {
			return agent.ErrorResult("cancelled: %v", ctxErr)
		}
		return agent.ErrorResult("refs: %v", err)
	}

	status := "ok"
	switch {
	case result.Unsupported:
		status = "unsupported"
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
	fmt.Fprintf(&text, "query: %s %s\n", result.Relation, result.Symbol)
	fmt.Fprintf(&text, "relation: %s\n", result.Relation)
	if result.Relation == ast.RelationReferences {
		fmt.Fprintf(&text, "depth: 0\n")
	} else {
		fmt.Fprintf(&text, "depth: %d\n", result.Depth)
	}
	if result.Found {
		fmt.Fprintf(&text, "target: %s:%d\n", result.Target.Path, result.Target.Line)
		fmt.Fprintf(&text, "target_symbol: %s\n", result.Target.QualifiedName)
		fmt.Fprintf(&text, "target_kind: %s\n", result.Target.Kind)
	}
	if result.Ambiguous {
		text.WriteString("ambiguous: true\n")
	}
	if result.Unsupported {
		fmt.Fprintf(&text, "unsupported: %s\n", result.UnsupportedReason)
	}
	if result.Truncated {
		text.WriteString("truncated: true\n")
		fmt.Fprintf(&text, "truncation_reason: %s\n", result.Reason)
	}
	if result.ParseFailures > 0 {
		fmt.Fprintf(&text, "unparsed: %d\n", result.ParseFailures)
	}
	if result.Relation == ast.RelationReferences {
		text.WriteString("coverage: static_syntax\n")
	} else {
		text.WriteString("coverage: static_direct_calls\n")
	}
	writeRefUnknown(&text, result.Unknown)
	if result.Relation == ast.RelationReferences {
		fmt.Fprintf(&text, "returned: %d\n", len(result.References))
		fmt.Fprintf(&text, "total: %d\n", result.TotalReferences)
	} else {
		fmt.Fprintf(&text, "returned_nodes: %d\n", len(result.Nodes))
		fmt.Fprintf(&text, "returned_edges: %d\n", len(result.Edges))
		if result.Depth <= 1 {
			fmt.Fprintf(&text, "matched_edges: %d\n", result.TotalEdges)
		} else {
			fmt.Fprintf(&text, "traversed_edges: %d\n", result.TotalEdges)
		}
	}

	if result.Ambiguous {
		text.WriteString("\ncandidates:\n")
		for _, candidate := range result.Candidates {
			fmt.Fprintf(&text, "  - path: %s\n", candidate.Path)
			fmt.Fprintf(&text, "    symbol: %s\n", candidate.QualifiedName)
			fmt.Fprintf(&text, "    kind: %s\n", candidate.Kind)
			fmt.Fprintf(&text, "    line: %d\n", candidate.Line)
		}
	}
	if len(result.References) > 0 {
		text.WriteString("\nreferences:\n")
		for _, ref := range result.References {
			fmt.Fprintf(&text, "  - path: %s\n", ref.Path)
			fmt.Fprintf(&text, "    line: %d\n", ref.Line)
			fmt.Fprintf(&text, "    column: %d\n", ref.Column)
			fmt.Fprintf(&text, "    kind: %s\n", referenceKind(ref))
			if ref.Call {
				text.WriteString("    call: true\n")
			}
			if ref.Test {
				text.WriteString("    test: true\n")
			}
		}
	}
	if len(result.Nodes) > 0 {
		text.WriteString("\nnodes:\n")
		for _, node := range result.Nodes {
			fmt.Fprintf(&text, "  - path: %s\n", node.Path)
			fmt.Fprintf(&text, "    symbol: %s\n", node.Symbol)
			fmt.Fprintf(&text, "    package: %s\n", node.Package)
			if node.Kind != "" {
				fmt.Fprintf(&text, "    kind: %s\n", node.Kind)
			}
			fmt.Fprintf(&text, "    line: %d\n", node.Line)
			fmt.Fprintf(&text, "    end_line: %d\n", node.EndLine)
			fmt.Fprintf(&text, "    depth: %d\n", node.Depth)
			fmt.Fprintf(&text, "    caller_kind: %s\n", node.CallerKind)
		}
	}
	if len(result.Edges) > 0 {
		text.WriteString("\nedges:\n")
		for _, edge := range result.Edges {
			fmt.Fprintf(&text, "  - caller: %s\n", edge.Caller.Symbol)
			fmt.Fprintf(&text, "    caller_path: %s\n", edge.Caller.Path)
			fmt.Fprintf(&text, "    caller_line: %d\n", edge.Caller.Line)
			fmt.Fprintf(&text, "    caller_kind: %s\n", edge.Caller.CallerKind)
			fmt.Fprintf(&text, "    callee: %s\n", edge.Callee.Symbol)
			fmt.Fprintf(&text, "    callee_path: %s\n", edge.Callee.Path)
			fmt.Fprintf(&text, "    callee_line: %d\n", edge.Callee.Line)
			fmt.Fprintf(&text, "    call: %s:%d:%d\n", edge.CallPath, edge.CallLine, edge.Column)
			fmt.Fprintf(&text, "    certainty: %s\n", edge.Certainty)
			if edge.InClosure {
				text.WriteString("    closure: true\n")
			}
			if edge.Test {
				text.WriteString("    test: true\n")
			}
		}
	}

	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: refsDetails(status, result),
		Useless: status == "not_found" && result.ParseFailures == 0,
	}
}

func referenceKind(ref ast.ReferenceSite) string {
	if ref.Qualified {
		return "qualified"
	}
	return "unqualified"
}

func writeRefUnknown(sb *strings.Builder, unknown ast.UnknownSummary) {
	if unknown.DotImportFiles > 0 {
		fmt.Fprintf(sb, "dot_import_files: %d\n", unknown.DotImportFiles)
	}
	if unknown.AmbiguousUses > 0 {
		fmt.Fprintf(sb, "repository_ambiguous_uses: %d\n", unknown.AmbiguousUses)
	}
	if unknown.UnresolvedUses > 0 {
		fmt.Fprintf(sb, "repository_unresolved_uses: %d\n", unknown.UnresolvedUses)
	}
	if unknown.DynamicCalls > 0 {
		fmt.Fprintf(sb, "dynamic_calls: %d\n", unknown.DynamicCalls)
	}
	if unknown.PackageInitializerCalls > 0 {
		fmt.Fprintf(sb, "package_initializer_calls: %d\n", unknown.PackageInitializerCalls)
	}
	if len(unknown.Reasons) > 0 {
		reasons := make([]string, 0, len(unknown.Reasons))
		for reason := range unknown.Reasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		for _, reason := range reasons {
			fmt.Fprintf(sb, "unknown_%s: %d\n", reason, unknown.Reasons[reason])
		}
	}
}

// validateRefsArgs rejects arguments outside the closed schema.
func validateRefsArgs(args map[string]any) error {
	allowed := map[string]bool{
		"symbol": true, "relation": true, "path": true,
		"depth": true, "limit": true, "edges": true, "skip": true,
	}
	for key := range args {
		if !allowed[key] {
			return fmt.Errorf("unknown refs argument %q", key)
		}
	}
	return nil
}

// refsDetails is the structured payload for the UI and benchmarks.
func refsDetails(status string, result ast.RefResult) map[string]any {
	references := make([]map[string]any, 0, len(result.References))
	for _, ref := range result.References {
		references = append(references, map[string]any{
			"path":      ref.Path,
			"line":      ref.Line,
			"column":    ref.Column,
			"qualified": ref.Qualified,
			"call":      ref.Call,
			"test":      ref.Test,
		})
	}
	nodes := make([]map[string]any, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		nodes = append(nodes, map[string]any{
			"path":        node.Path,
			"package":     node.Package,
			"symbol":      node.Symbol,
			"kind":        string(node.Kind),
			"line":        node.Line,
			"end_line":    node.EndLine,
			"depth":       node.Depth,
			"caller_kind": node.CallerKind,
		})
	}
	edges := make([]map[string]any, 0, len(result.Edges))
	for _, edge := range result.Edges {
		edges = append(edges, map[string]any{
			"caller": map[string]any{
				"path":        edge.Caller.Path,
				"package":     edge.Caller.Package,
				"symbol":      edge.Caller.Symbol,
				"kind":        string(edge.Caller.Kind),
				"line":        edge.Caller.Line,
				"end_line":    edge.Caller.EndLine,
				"caller_kind": edge.Caller.CallerKind,
			},
			"callee": map[string]any{
				"path":        edge.Callee.Path,
				"package":     edge.Callee.Package,
				"symbol":      edge.Callee.Symbol,
				"kind":        string(edge.Callee.Kind),
				"line":        edge.Callee.Line,
				"end_line":    edge.Callee.EndLine,
				"caller_kind": edge.Callee.CallerKind,
			},
			"call_path":  edge.CallPath,
			"call_line":  edge.CallLine,
			"column":     edge.Column,
			"in_closure": edge.InClosure,
			"test":       edge.Test,
			"certainty":  string(edge.Certainty),
		})
	}
	candidates := make([]map[string]any, 0, len(result.Candidates))
	for _, candidate := range result.Candidates {
		candidates = append(candidates, map[string]any{
			"path":           candidate.Path,
			"package":        candidate.Package,
			"symbol":         candidate.Symbol,
			"qualified_name": candidate.QualifiedName,
			"kind":           string(candidate.Kind),
			"line":           candidate.Line,
			"end_line":       candidate.EndLine,
		})
	}
	unknown := map[string]any{
		"dot_import_files":          result.Unknown.DotImportFiles,
		"ambiguous_uses":            result.Unknown.AmbiguousUses,
		"unresolved_uses":           result.Unknown.UnresolvedUses,
		"dynamic_calls":             result.Unknown.DynamicCalls,
		"package_initializer_calls": result.Unknown.PackageInitializerCalls,
		"reasons":                   result.Unknown.Reasons,
	}
	return map[string]any{
		"status":             status,
		"module":             result.Module,
		"relation":           string(result.Relation),
		"symbol":             result.Symbol,
		"target":             refTargetDetails(result.Target),
		"found":              result.Found,
		"ambiguous":          result.Ambiguous,
		"candidates":         candidates,
		"unsupported":        result.Unsupported,
		"unsupported_reason": result.UnsupportedReason,
		"depth":              result.Depth,
		"references":         references,
		"nodes":              nodes,
		"edges":              edges,
		"total_references":   result.TotalReferences,
		"total_edges":        result.TotalEdges,
		"truncated":          result.Truncated,
		"truncation_reason":  result.Reason,
		"work":               result.Work,
		"parse_failures":     result.ParseFailures,
		"unknown":            unknown,
	}
}

func refTargetDetails(target ast.RefTarget) map[string]any {
	return map[string]any{
		"path":           target.Path,
		"package":        target.Package,
		"symbol":         target.Symbol,
		"qualified_name": target.QualifiedName,
		"kind":           string(target.Kind),
		"line":           target.Line,
		"end_line":       target.EndLine,
	}
}
