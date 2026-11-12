package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// StructureTool answers bounded project-structure inventory questions.
//
// It is read-only. It shares StructuralSource with symbols, graph, and refs, so
// every answer names the same snapshot generation. It owns no parser and no
// second graph.
type StructureTool struct {
	Base
	// Root bounds every path selector.
	Root string

	source *StructuralSource

	mu           sync.Mutex
	lastOverview string
}

// NewStructureTool returns the inventory tool over source.
func NewStructureTool(source *StructuralSource) *StructureTool {
	root := ""
	if source != nil {
		root = source.Root()
	}
	tool := &StructureTool{Root: root, source: source}
	tool.Base = NewBase("structure",
		"Answer project-structure inventory questions over the shared "+
			"repository snapshot: languages, files, packages, tests, configs, "+
			"entrypoints, explicit relations, and bounded static impact. Select "+
			"a view and optionally scope it with path, package, or symbol. "+
			"Overview is metadata-first and does not parse source; targeted and "+
			"impact views parse the pending supported files on demand. Every "+
			"result names its snapshot id, generation, activation, and four "+
			"independent completeness axes: scope, inventory, structural, and "+
			"result. Scope reports excluded_roots and nested repository "+
			"boundaries; a nested repository is included in the same snapshot "+
			"and governed by its own ignore files. A zero tests or files count "+
			"is not proof of absence unless scope, inventory, and structural are "+
			"all COMPLETE and the result is not truncated. Test associations are "+
			"static and are not coverage. Semantic activation is unsupported "+
			"and fails explicitly.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"view": map[string]any{
					"type":        "string",
					"enum":        []any{"overview", "scope", "languages", "files", "packages", "entrypoints", "configs", "tests", "relations", "impact"},
					"description": "Inventory view. Defaults to overview.",
				},
				"path": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "File or directory inside the working directory.",
				},
				"package": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Exact Go package name for files, packages, or tests views.",
				},
				"symbol": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Exact symbol or qualified name for tests and impact views.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum rows returned.",
					"minimum":     1.0,
					"maximum":     float64(ast.StructureMaxLimit),
				},
				"skip": map[string]any{
					"type":        "integer",
					"description": "Rows to skip, for pagination.",
					"minimum":     0.0,
				},
				"depth": map[string]any{
					"type":        "integer",
					"description": "Caller traversal depth for impact/relations (default 1).",
					"minimum":     1.0,
					"maximum":     float64(ast.RefMaxDepth),
				},
				"activation": map[string]any{
					"type":        "string",
					"enum":        []any{"overview", "targeted", "impact", "semantic"},
					"description": "Requested structural depth. Defaults from the view. Semantic is explicitly unsupported in this build.",
				},
			},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	tool.SetActionClasses(agent.ActionObserve, agent.ActionStructure)
	return tool
}

func (t *StructureTool) index() (*ast.Index, error) {
	return structuralSourceIndex(t.source, "structure")
}

// Source returns the structural source the tool reads.
func (t *StructureTool) Source() *StructuralSource { return t.source }

// Execute implements agent.Tool.
func (t *StructureTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	if err := validateStructureArgs(args); err != nil {
		return agent.ErrorResult("%v", err)
	}

	view := "overview"
	if raw, ok := args["view"]; ok {
		value, ok := raw.(string)
		if !ok {
			return agent.ErrorResult("argument %q must be a string", "view")
		}
		view = strings.TrimSpace(value)
		switch view {
		case "overview", "scope", "languages", "files", "packages", "entrypoints", "configs", "tests", "relations", "impact":
		default:
			return agent.ErrorResult("unsupported view %q", value)
		}
	}

	pathArg, err := strArg(args, "path", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	pathArg = strings.TrimSpace(pathArg)
	packageArg, err := strArg(args, "package", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	packageArg = strings.TrimSpace(packageArg)
	symbolArg, err := strArg(args, "symbol", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	symbolArg = strings.TrimSpace(symbolArg)
	limit, err := intArg(args, "limit", ast.StructureDefaultLimit)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if limit < 1 || limit > ast.StructureMaxLimit {
		return agent.ErrorResult("limit must be between 1 and %d, got %d", ast.StructureMaxLimit, limit)
	}
	skip, err := intArg(args, "skip", 0)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if skip < 0 {
		return agent.ErrorResult("skip must be >= 0, got %d", skip)
	}
	depth, err := intArg(args, "depth", 1)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if depth < 1 || depth > ast.RefMaxDepth {
		return agent.ErrorResult("depth must be between 1 and %d, got %d", ast.RefMaxDepth, depth)
	}
	if (view == "impact" || view == "relations") && symbolArg == "" && pathArg == "" {
		return agent.ErrorResult("%s requires symbol or path", view)
	}
	activation, err := structureActivation(args, view)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if activation == ast.ActivationSemantic {
		return agent.ErrorResult("semantic structural activation is unsupported: no semantic resolver is configured")
	}
	switch view {
	case "overview", "scope", "entrypoints", "configs":
		if pathArg != "" || packageArg != "" || symbolArg != "" {
			return agent.ErrorResult("%s does not take path, package, or symbol selectors", view)
		}
	}
	// Resolve every model-supplied path through the workspace containment
	// authority before the index sees it.
	if pathArg != "" {
		resolved, err := resolvePath(t.Root, pathArg)
		if err != nil {
			return agent.ErrorResult("resolve path: %v", err)
		}
		if _, err := os.Stat(resolved); err != nil {
			if os.IsNotExist(err) {
				return absenceResult("stat %q: %v", pathArg, err)
			}
			return agent.ErrorResult("stat %q: %v", pathArg, err)
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
	if activation.ParsesSource() {
		if err := idx.EnsureParsed(ctx.Ctx); err != nil {
			return agent.ErrorResult("parse repository structure: %v", err)
		}
	}
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	switch view {
	case "overview":
		return t.overview(ctx, idx, limit)
	case "scope":
		return t.scope(ctx, idx)
	case "languages":
		return t.languageView(ctx, idx, limit)
	case "files":
		return t.files(ctx, idx, pathArg, packageArg, limit, skip)
	case "packages":
		return t.packages(ctx, idx, pathArg, packageArg, limit, skip)
	case "entrypoints":
		return t.entrypoints(ctx, idx)
	case "configs":
		return t.configs(ctx, idx)
	case "tests":
		return t.tests(ctx, idx, symbolArg, pathArg, packageArg, limit, skip)
	case "relations":
		return t.relationsView(ctx, idx, symbolArg, pathArg, depth, limit)
	case "impact":
		return t.impact(ctx, idx, symbolArg, pathArg, depth, limit)
	default:
		return agent.ErrorResult("unsupported view %q", view)
	}
}

func (t *StructureTool) languageView(ctx agent.Context, idx *ast.Index, limit int) agent.Result {
	result, err := idx.Overview(ctx.Ctx, limit)
	if err != nil {
		return agent.ErrorResult("languages: %v", err)
	}
	status := "ok"
	if len(result.Languages) == 0 {
		status = "not_found"
	}
	var text strings.Builder
	writeStructureHeader(&text, status, "languages", result.Snapshot, result.Truncated, result.Reason)
	fmt.Fprintf(&text, "languages: %d\n", len(result.Languages))
	for _, language := range result.Languages {
		fmt.Fprintf(&text, "  - language: %s\n", ast.DisplayLanguage(language.Language))
		fmt.Fprintf(&text, "    supported: %v\n", language.Supported)
		fmt.Fprintf(&text, "    coverage: %s\n", language.Coverage)
		fmt.Fprintf(&text, "    files: %d\n", language.Files)
		fmt.Fprintf(&text, "    parsed: %d\n", language.Parsed)
		fmt.Fprintf(&text, "    pending: %d\n", language.Pending)
		fmt.Fprintf(&text, "    failed: %d\n", language.Failed)
		fmt.Fprintf(&text, "    symbols: %d\n", language.Symbols)
	}
	if len(result.Unsupported) > 0 {
		text.WriteString("\nunsupported_source:\n")
		for _, item := range result.Unsupported {
			fmt.Fprintf(&text, "  - extension: %s\n    files: %d\n", item.Extension, item.Files)
		}
	}
	if len(result.Frameworks) > 0 {
		fmt.Fprintf(&text, "\nframeworks: %s\n", strings.Join(result.Frameworks, ", "))
	}
	details := map[string]any{
		"status":      status,
		"view":        "languages",
		"snapshot":    snapshotMap(result.Snapshot),
		"languages":   languageMaps(result.Languages),
		"unsupported": unsupportedMaps(result.Unsupported),
		"frameworks":  append([]string(nil), result.Frameworks...),
		"truncated":   result.Truncated,
		"reason":      result.Reason,
	}
	addStructuralProvenance(details, idx, result.Snapshot.StructuralComplete)
	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: details,
		Useless: len(result.Languages) == 0,
	}
}

// relationsView exposes the explicit inheritance/call neighbourhood without
// dumping references. It is the IMPACT-level structural surface.
func (t *StructureTool) relationsView(ctx agent.Context, idx *ast.Index, symbolArg, pathArg string, depth, limit int) agent.Result {
	result, err := idx.Impact(ctx.Ctx, ast.ImpactQuery{
		Symbol: symbolArg, Path: pathArg, Depth: depth, Limit: limit,
	})
	if err != nil {
		return agent.ErrorResult("relations: %v", err)
	}
	status := structureStatus(len(result.Edges) == 0, result.Truncated)
	if result.Ambiguous {
		status = "ambiguous"
	} else if !result.Found {
		status = "not_found"
	}
	var text strings.Builder
	writeStructureHeader(&text, status, "relations", result.Snapshot, result.Truncated, result.Reason)
	fmt.Fprintf(&text, "query: %s %s\n", result.QueryType, result.Selector)
	if result.Ambiguous {
		text.WriteString("ambiguous: true\n")
		for _, candidate := range result.Candidates {
			fmt.Fprintf(&text, "  - %s %s:%d\n", candidate.QualifiedName, candidate.Path, candidate.Line)
		}
	}
	if result.Truncated {
		fmt.Fprintf(&text, "truncated: true\nreason: %s\n", result.Reason)
	}
	fmt.Fprintf(&text, "targets: %d\n", len(result.Targets))
	fmt.Fprintf(&text, "nodes: %d\n", len(result.Nodes))
	fmt.Fprintf(&text, "edges: %d\n", len(result.Edges))
	writeImpactUnknown(&text, result.Unknown)
	if len(result.Targets) > 0 {
		text.WriteString("\ntargets:\n")
		for _, target := range result.Targets {
			fmt.Fprintf(&text, "  - symbol: %s\n", target.QualifiedName)
			fmt.Fprintf(&text, "    path: %s\n", target.Path)
			fmt.Fprintf(&text, "    line: %d\n", target.Line)
			if target.Language != "" {
				fmt.Fprintf(&text, "    language: %s\n", target.Language)
			}
			if target.Canonical != "" {
				fmt.Fprintf(&text, "    canonical_kind: %s\n", target.Canonical)
			}
		}
	}
	if len(result.Edges) > 0 {
		text.WriteString("\nedges:\n")
		for _, edge := range result.Edges {
			fmt.Fprintf(&text, "  - from: %s\n", edge.From)
			fmt.Fprintf(&text, "    to: %s\n", edge.To)
			fmt.Fprintf(&text, "    kind: %s\n", edge.Kind)
			if edge.Resolution != "" {
				fmt.Fprintf(&text, "    resolution: %s\n", edge.Resolution)
			}
			if edge.Language != "" {
				fmt.Fprintf(&text, "    language: %s\n", edge.Language)
			}
			fmt.Fprintf(&text, "    path: %s\n", edge.Source.Path)
			fmt.Fprintf(&text, "    line: %d\n", edge.Source.StartLine)
		}
	}
	details := map[string]any{
		"status":     status,
		"view":       "relations",
		"snapshot":   snapshotMap(result.Snapshot),
		"query_type": result.QueryType,
		"selector":   result.Selector,
		"found":      result.Found,
		"ambiguous":  result.Ambiguous,
		"candidates": refTargetMaps(result.Candidates),
		"targets":    refTargetMaps(result.Targets),
		"nodes":      nodeMaps(result.Nodes),
		"edges":      edgeMaps(result.Edges),
		"truncated":  result.Truncated,
		"reason":     result.Reason,
		"unknown":    unknownMap(result.Unknown),
	}
	addStructuralProvenance(details, idx, result.Found && !result.Truncated && !result.Ambiguous)
	details["coordinates"] = impactCoordinates(result)
	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: details,
		Useless: !result.Found && !result.Ambiguous,
	}
}

func (t *StructureTool) overview(ctx agent.Context, idx *ast.Index, limit int) agent.Result {
	slice := limit
	if slice > 10 {
		slice = 10
	}
	result, err := idx.Overview(ctx.Ctx, slice)
	if err != nil {
		return agent.ErrorResult("overview: %v", err)
	}
	info := result.Snapshot

	status := "ok"
	if result.Truncated {
		status = "truncated"
	}
	repeated := t.markOverview(info.SnapshotID, slice)
	var text strings.Builder
	writeStructureHeader(&text, status, "overview", info, result.Truncated, result.Reason)
	if repeated {
		text.WriteString("repeated: true (the same snapshot was already reported)\n")
	}
	fmt.Fprintf(&text, "languages: %s\n", languageSummary(info.Languages))
	if len(result.Frameworks) > 0 {
		fmt.Fprintf(&text, "frameworks: %s\n", strings.Join(result.Frameworks, ", "))
	}
	fmt.Fprintf(&text, "packages: %d\n", info.Packages)
	fmt.Fprintf(&text, "files: %d (go: %d, source: %d, configs: %d)\n", info.Files, info.GoFiles, info.SourceFiles, info.Configs)
	fmt.Fprintf(&text, "symbols: %d\n", info.Symbols)
	fmt.Fprintf(&text, "references: %d\n", info.References)
	fmt.Fprintf(&text, "calls: %d\n", info.Calls)
	fmt.Fprintf(&text, "tests: %d (test_files: %d)\n", info.Tests, info.TestFiles)
	fmt.Fprintf(&text, "entrypoints: %d\n", info.Entrypoints)
	fmt.Fprintf(&text, "directories: %d\n", info.Directories)
	fmt.Fprintf(&text, "last_parsed_files: %d\n", info.LastParsedFiles)
	fmt.Fprintf(&text, "last_reused_files: %d\n", info.LastReusedFiles)
	fmt.Fprintf(&text, "last_resolution: %s\n", info.LastResolution)
	writeScopeInventory(&text, info)
	if len(result.Packages) > 0 {
		text.WriteString("\npackages:\n")
		for _, pkg := range result.Packages {
			fmt.Fprintf(&text, "  - %s (%s) files=%d tests=%d symbols=%d\n",
				pkg.Dir, displayPackage(pkg), pkg.Files, pkg.TestFiles, pkg.Symbols)
		}
	}
	if len(result.Entrypoints) > 0 {
		text.WriteString("\nentrypoints:\n")
		for _, entry := range result.Entrypoints {
			fmt.Fprintf(&text, "  - %s %s:%d\n", entry.QualifiedName, entry.Path, entry.Line)
		}
	}
	if len(result.Configs) > 0 {
		text.WriteString("\nconfigs:\n")
		for _, config := range result.Configs {
			fmt.Fprintf(&text, "  - %s (%s)\n", config.Path, config.Kind)
		}
	}

	details := map[string]any{
		"status":      status,
		"view":        "overview",
		"snapshot":    snapshotMap(info),
		"packages":    packageMaps(result.Packages),
		"files":       fileMaps(result.Files),
		"entrypoints": entrypointMaps(result.Entrypoints),
		"configs":     configMaps(result.Configs),
		"languages":   languageMaps(result.Languages),
		"unsupported": unsupportedMaps(result.Unsupported),
		"frameworks":  append([]string(nil), result.Frameworks...),
		"truncated":   result.Truncated,
		"reason":      result.Reason,
		"repeated":    repeated,
	}
	addStructuralProvenance(details, idx, !result.Truncated)
	details["coordinates"] = overviewCoordinates(result.Packages, result.Entrypoints)
	return agent.Result{Text: strings.TrimRight(text.String(), "\n"), Details: details}
}

// scope is the project-boundary view. It is metadata-only: an excluded root is
// described, never recursively walked or parsed, and an included nested
// repository is already part of the same snapshot.
func (t *StructureTool) scope(ctx agent.Context, idx *ast.Index) agent.Result {
	info, err := idx.Snapshot(ctx.Ctx)
	if err != nil {
		return agent.ErrorResult("scope: %v", err)
	}
	status := "ok"
	var text strings.Builder
	writeStructureHeader(&text, status, "scope", info, false, "")
	if len(info.ExcludedRoots) == 0 {
		text.WriteString("excluded_roots: 0\n")
	}
	writeScopeInventory(&text, info)
	details := map[string]any{
		"status":          status,
		"view":            "scope",
		"snapshot":        snapshotMap(info),
		"excluded_roots":  excludedRootMaps(info.ExcludedRoots),
		"nested_projects": projectBoundaryMaps(info.NestedProjects),
		"scope_complete":  info.ScopeComplete,
	}
	addStructuralProvenance(details, idx, info.ScopeComplete)
	return agent.Result{Text: strings.TrimRight(text.String(), "\n"), Details: details}
}

// writeScopeInventory renders the excluded roots and included nested projects
// that explain why an inventory is or is not repository-wide.
func writeScopeInventory(text *strings.Builder, info ast.SnapshotInfo) {
	if len(info.ExcludedRoots) > 0 {
		fmt.Fprintf(text, "excluded_roots: %d (contents not indexed)\n", len(info.ExcludedRoots))
	}
	const maxScopeRows = 10
	for i, root := range info.ExcludedRoots {
		if i >= maxScopeRows {
			fmt.Fprintf(text, "  - ... %d more excluded roots\n", len(info.ExcludedRoots)-maxScopeRows)
			break
		}
		fmt.Fprintf(text, "  - path: %s\n", root.Path)
		fmt.Fprintf(text, "    reason: %s\n", root.Reason)
		if root.Rule != "" {
			fmt.Fprintf(text, "    rule: %s\n", root.Rule)
		}
		if root.NestedRepository {
			text.WriteString("    nested_repository: true\n")
		}
		if len(root.Markers) > 0 {
			fmt.Fprintf(text, "    markers: %s\n", strings.Join(root.Markers, ", "))
		}
		if len(root.Languages) > 0 {
			fmt.Fprintf(text, "    languages: %s\n", strings.Join(languageStrings(root.Languages), ", "))
		}
	}
	if info.ExcludedSourceFiles > 0 {
		fmt.Fprintf(text, "excluded_source_files: %d (contents not indexed)\n", info.ExcludedSourceFiles)
		shown := 0
		for _, file := range info.ExcludedFiles {
			if shown >= maxScopeRows {
				break
			}
			fmt.Fprintf(text, "  - path: %s\n", file.Path)
			if file.Rule != "" {
				fmt.Fprintf(text, "    rule: %s\n", file.Rule)
			}
			shown++
		}
		if remaining := info.ExcludedSourceFiles - shown; remaining > 0 {
			fmt.Fprintf(text, "  - ... %d more excluded source files\n", remaining)
		}
	}
	if len(info.NestedProjects) > 0 {
		fmt.Fprintf(text, "nested_projects: %d\n", len(info.NestedProjects))
		for i, boundary := range info.NestedProjects {
			if i >= maxScopeRows {
				fmt.Fprintf(text, "  - ... %d more nested projects\n", len(info.NestedProjects)-maxScopeRows)
				break
			}
			fmt.Fprintf(text, "  - path: %s\n", boundary.Path)
			fmt.Fprintf(text, "    included: %v\n", boundary.Included)
			if boundary.ParentScopeRule != "" {
				fmt.Fprintf(text, "    parent_scope_rule: %s\n", boundary.ParentScopeRule)
			}
			if boundary.Reason != "" {
				fmt.Fprintf(text, "    reason: %s\n", boundary.Reason)
			}
			if len(boundary.Markers) > 0 {
				fmt.Fprintf(text, "    markers: %s\n", strings.Join(boundary.Markers, ", "))
			}
			if len(boundary.Languages) > 0 {
				fmt.Fprintf(text, "    languages: %s\n", strings.Join(languageStrings(boundary.Languages), ", "))
			}
		}
	}
}

// markOverview reports whether this exact overview was already returned for
// the same snapshot and page size. It is a presentation hint, never a cache:
// the full result is still rendered so a compacted context can recover it.
func (t *StructureTool) markOverview(snapshotID string, limit int) bool {
	if snapshotID == "" {
		return false
	}
	key := snapshotID + ":" + fmt.Sprintf("%d", limit)
	t.mu.Lock()
	defer t.mu.Unlock()
	repeated := t.lastOverview == key
	t.lastOverview = key
	return repeated
}

func (t *StructureTool) files(ctx agent.Context, idx *ast.Index, pathArg, packageArg string, limit, skip int) agent.Result {
	result, err := idx.Files(ctx.Ctx, ast.FileQuery{
		Path: pathArg, Package: packageArg, Limit: limit, Skip: skip,
	})
	if err != nil {
		return agent.ErrorResult("files: %v", err)
	}
	status := structureStatus(result.Total == 0, result.Truncated)
	var text strings.Builder
	writeStructureHeader(&text, status, "files", result.Snapshot, result.Truncated, result.Reason)
	fmt.Fprintf(&text, "returned: %d\n", len(result.Files))
	fmt.Fprintf(&text, "total: %d\n", result.Total)
	if result.Truncated {
		fmt.Fprintf(&text, "truncated: true\nreason: %s\n", result.Reason)
	}
	if len(result.Files) > 0 {
		text.WriteString("\nfiles:\n")
		for _, file := range result.Files {
			fmt.Fprintf(&text, "  - path: %s\n", file.Path)
			fmt.Fprintf(&text, "    kind: %s\n", file.Kind)
			if file.Package != "" {
				fmt.Fprintf(&text, "    package: %s\n", file.Package)
			}
			if file.Language != "" {
				fmt.Fprintf(&text, "    language: %s\n", file.Language)
			}
			if file.Test {
				text.WriteString("    test: true\n")
			}
			if file.Unsupported {
				text.WriteString("    unsupported: true\n")
			}
			if file.ParseError != "" {
				fmt.Fprintf(&text, "    parse_error: %s\n", file.ParseError)
			}
			fmt.Fprintf(&text, "    symbols: %d\n", file.Symbols)
			fmt.Fprintf(&text, "    size: %d\n", file.Size)
		}
	}
	details := map[string]any{
		"status":    status,
		"view":      "files",
		"snapshot":  snapshotMap(result.Snapshot),
		"files":     fileMaps(result.Files),
		"total":     result.Total,
		"returned":  len(result.Files),
		"truncated": result.Truncated,
		"reason":    result.Reason,
	}
	addStructuralProvenance(details, idx, !result.Truncated)
	details["coordinates"] = fileCoordinates(result.Files)
	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: details,
		Useless: result.Total == 0,
	}
}

func (t *StructureTool) packages(ctx agent.Context, idx *ast.Index, pathArg, packageArg string, limit, skip int) agent.Result {
	result, err := idx.Packages(ctx.Ctx, ast.PackageQuery{
		Path: pathArg, Name: packageArg, Limit: limit, Skip: skip,
	})
	if err != nil {
		return agent.ErrorResult("packages: %v", err)
	}
	status := structureStatus(result.Total == 0, result.Truncated)
	var text strings.Builder
	writeStructureHeader(&text, status, "packages", result.Snapshot, result.Truncated, result.Reason)
	fmt.Fprintf(&text, "returned: %d\n", len(result.Packages))
	fmt.Fprintf(&text, "total: %d\n", result.Total)
	if result.Truncated {
		fmt.Fprintf(&text, "truncated: true\nreason: %s\n", result.Reason)
	}
	if len(result.Packages) > 0 {
		text.WriteString("\npackages:\n")
		for _, pkg := range result.Packages {
			fmt.Fprintf(&text, "  - dir: %s\n", pkg.Dir)
			fmt.Fprintf(&text, "    package: %s\n", pkg.Name)
			if pkg.Language != "" {
				fmt.Fprintf(&text, "    language: %s\n", ast.DisplayLanguage(pkg.Language))
			}
			fmt.Fprintf(&text, "    files: %d\n", pkg.Files)
			fmt.Fprintf(&text, "    test_files: %d\n", pkg.TestFiles)
			fmt.Fprintf(&text, "    symbols: %d\n", pkg.Symbols)
			fmt.Fprintf(&text, "    imports: %d\n", pkg.Imports)
			fmt.Fprintf(&text, "    importers: %d\n", pkg.Importers)
			if pkg.Ambiguous {
				fmt.Fprintf(&text, "    ambiguous: true\n    packages: [%s]\n", strings.Join(pkg.Packages, ", "))
			}
		}
	}
	details := map[string]any{
		"status":    status,
		"view":      "packages",
		"snapshot":  snapshotMap(result.Snapshot),
		"packages":  packageMaps(result.Packages),
		"total":     result.Total,
		"returned":  len(result.Packages),
		"truncated": result.Truncated,
		"reason":    result.Reason,
	}
	addStructuralProvenance(details, idx, !result.Truncated)
	details["coordinates"] = packageCoordinates(result.Packages)
	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: details,
		Useless: result.Total == 0,
	}
}

func (t *StructureTool) entrypoints(ctx agent.Context, idx *ast.Index) agent.Result {
	result, err := idx.Entrypoints(ctx.Ctx)
	if err != nil {
		return agent.ErrorResult("entrypoints: %v", err)
	}
	status := structureStatus(len(result.Entrypoints) == 0, result.Truncated)
	var text strings.Builder
	writeStructureHeader(&text, status, "entrypoints", result.Snapshot, result.Truncated, result.Reason)
	fmt.Fprintf(&text, "total: %d\n", len(result.Entrypoints))
	if len(result.Entrypoints) > 0 {
		text.WriteString("\nentrypoints:\n")
		for _, entry := range result.Entrypoints {
			fmt.Fprintf(&text, "  - symbol: %s\n", entry.QualifiedName)
			fmt.Fprintf(&text, "    path: %s\n", entry.Path)
			if entry.Language != "" {
				fmt.Fprintf(&text, "    language: %s\n", ast.DisplayLanguage(entry.Language))
			}
			if entry.NativeKind != "" {
				fmt.Fprintf(&text, "    native_kind: %s\n", entry.NativeKind)
			}
			fmt.Fprintf(&text, "    line: %d\n", entry.Line)
			fmt.Fprintf(&text, "    column: %d\n", entry.Column)
		}
	}
	details := map[string]any{
		"status":      status,
		"view":        "entrypoints",
		"snapshot":    snapshotMap(result.Snapshot),
		"entrypoints": entrypointMaps(result.Entrypoints),
		"total":       len(result.Entrypoints),
		"truncated":   result.Truncated,
		"reason":      result.Reason,
	}
	addStructuralProvenance(details, idx, !result.Truncated)
	details["coordinates"] = entrypointCoordinates(result.Entrypoints)
	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: details,
		Useless: len(result.Entrypoints) == 0,
	}
}

func (t *StructureTool) configs(ctx agent.Context, idx *ast.Index) agent.Result {
	result, err := idx.Configs(ctx.Ctx)
	if err != nil {
		return agent.ErrorResult("configs: %v", err)
	}
	status := structureStatus(len(result.Configs) == 0, result.Truncated)
	var text strings.Builder
	writeStructureHeader(&text, status, "configs", result.Snapshot, result.Truncated, result.Reason)
	fmt.Fprintf(&text, "total: %d\n", len(result.Configs))
	if len(result.Configs) > 0 {
		text.WriteString("\nconfigs:\n")
		for _, config := range result.Configs {
			fmt.Fprintf(&text, "  - path: %s\n", config.Path)
			fmt.Fprintf(&text, "    kind: %s\n", config.Kind)
			if config.Language != "" {
				fmt.Fprintf(&text, "    language: %s\n", ast.DisplayLanguage(config.Language))
			}
			if config.Framework != "" {
				fmt.Fprintf(&text, "    framework: %s\n", config.Framework)
			}
			if config.Value != "" {
				fmt.Fprintf(&text, "    value: %s\n", config.Value)
			}
		}
	}
	details := map[string]any{
		"status":    status,
		"view":      "configs",
		"snapshot":  snapshotMap(result.Snapshot),
		"configs":   configMaps(result.Configs),
		"total":     len(result.Configs),
		"truncated": result.Truncated,
		"reason":    result.Reason,
	}
	addStructuralProvenance(details, idx, !result.Truncated)
	details["coordinates"] = configCoordinates(result.Configs)
	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: details,
		Useless: len(result.Configs) == 0,
	}
}

func (t *StructureTool) tests(ctx agent.Context, idx *ast.Index, symbolArg, pathArg, packageArg string, limit, skip int) agent.Result {
	result, err := idx.Tests(ctx.Ctx, ast.TestQuery{
		Symbol: symbolArg, Package: packageArg, Path: pathArg, Limit: limit, Skip: skip,
	})
	if err != nil {
		return agent.ErrorResult("tests: %v", err)
	}
	status := structureStatus(result.Total == 0, result.Truncated)
	if result.Ambiguous {
		status = "ambiguous"
	} else if result.Symbol != "" {
		if !result.Found {
			status = "not_found"
		} else if result.Total == 0 && !result.Truncated {
			status = "ok"
		}
	}
	var text strings.Builder
	writeStructureHeader(&text, status, "tests", result.Snapshot, result.Truncated, result.Reason)
	if result.Symbol != "" {
		fmt.Fprintf(&text, "symbol: %s\n", result.Symbol)
	}
	fmt.Fprintf(&text, "returned: %d\n", len(result.Tests)+len(result.Associations))
	fmt.Fprintf(&text, "total: %d\n", result.Total)
	text.WriteString("coverage: static_association (not coverage)\n")
	if result.Ambiguous {
		text.WriteString("ambiguous: true\n")
		for _, candidate := range result.Candidates {
			fmt.Fprintf(&text, "  - %s %s:%d\n", candidate.QualifiedName, candidate.Path, candidate.Line)
		}
	}
	if result.Truncated {
		fmt.Fprintf(&text, "truncated: true\nreason: %s\n", result.Reason)
	}
	if len(result.Tests) > 0 {
		text.WriteString("\ntests:\n")
		for _, test := range result.Tests {
			fmt.Fprintf(&text, "  - symbol: %s\n", test.QualifiedName)
			fmt.Fprintf(&text, "    path: %s\n", test.Path)
			if test.Language != "" {
				fmt.Fprintf(&text, "    language: %s\n", ast.DisplayLanguage(test.Language))
			}
			if test.Framework != "" {
				fmt.Fprintf(&text, "    framework: %s\n", test.Framework)
			}
			fmt.Fprintf(&text, "    line: %d\n", test.Line)
			fmt.Fprintf(&text, "    kind: %s\n", test.Kind)
		}
	}
	if len(result.Associations) > 0 {
		text.WriteString("\nassociations:\n")
		for _, association := range result.Associations {
			fmt.Fprintf(&text, "  - test: %s\n", association.Test.QualifiedName)
			fmt.Fprintf(&text, "    path: %s\n", association.Path)
			fmt.Fprintf(&text, "    line: %d\n", association.Line)
			fmt.Fprintf(&text, "    relation: %s\n", association.Relation)
		}
	}
	details := map[string]any{
		"status":       status,
		"view":         "tests",
		"snapshot":     snapshotMap(result.Snapshot),
		"symbol":       result.Symbol,
		"found":        result.Found,
		"ambiguous":    result.Ambiguous,
		"candidates":   refTargetMaps(result.Candidates),
		"tests":        testMaps(result.Tests),
		"associations": associationMaps(result.Associations),
		"total":        result.Total,
		"returned":     len(result.Tests) + len(result.Associations),
		"truncated":    result.Truncated,
		"reason":       result.Reason,
	}
	complete := !result.Truncated
	if result.Symbol != "" {
		complete = result.Found && !result.Ambiguous && !result.Truncated
	}
	addStructuralProvenance(details, idx, complete)
	details["coordinates"] = testCoordinates(result.Tests, result.Associations)
	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: details,
		Useless: result.Total == 0 && !result.Ambiguous,
	}
}

func (t *StructureTool) impact(ctx agent.Context, idx *ast.Index, symbolArg, pathArg string, depth, limit int) agent.Result {
	result, err := idx.Impact(ctx.Ctx, ast.ImpactQuery{
		Symbol: symbolArg, Path: pathArg, Depth: depth, Limit: limit,
	})
	if err != nil {
		return agent.ErrorResult("impact: %v", err)
	}
	status := structureStatus(result.Total == 0, result.Truncated)
	if result.Ambiguous {
		status = "ambiguous"
	} else if !result.Found {
		status = "not_found"
	} else if result.Total == 0 && !result.Truncated {
		status = "ok"
	}
	var text strings.Builder
	writeStructureHeader(&text, status, "impact", result.Snapshot, result.Truncated, result.Reason)
	fmt.Fprintf(&text, "query: %s %s\n", result.QueryType, result.Selector)
	if result.Ambiguous {
		text.WriteString("ambiguous: true\n")
		for _, candidate := range result.Candidates {
			fmt.Fprintf(&text, "  - %s %s:%d\n", candidate.QualifiedName, candidate.Path, candidate.Line)
		}
	}
	if result.Truncated {
		fmt.Fprintf(&text, "truncated: true\nreason: %s\n", result.Reason)
	}
	fmt.Fprintf(&text, "references: %d\n", len(result.References))
	fmt.Fprintf(&text, "callers: %d\n", len(result.Callers))
	fmt.Fprintf(&text, "importers: %d\n", len(result.Importers))
	fmt.Fprintf(&text, "tests: %d\n", len(result.Tests))
	writeImpactUnknown(&text, result.Unknown)
	if len(result.Targets) > 0 {
		text.WriteString("\ntargets:\n")
		for _, target := range result.Targets {
			fmt.Fprintf(&text, "  - symbol: %s\n", target.QualifiedName)
			fmt.Fprintf(&text, "    path: %s\n", target.Path)
			fmt.Fprintf(&text, "    line: %d\n", target.Line)
		}
	}
	if len(result.References) > 0 {
		text.WriteString("\nreferences:\n")
		for _, ref := range result.References {
			fmt.Fprintf(&text, "  - path: %s\n", ref.Path)
			fmt.Fprintf(&text, "    line: %d\n", ref.Line)
			fmt.Fprintf(&text, "    column: %d\n", ref.Column)
			if ref.Test {
				text.WriteString("    test: true\n")
			}
		}
	}
	if len(result.Callers) > 0 {
		text.WriteString("\ncallers:\n")
		for _, edge := range result.Callers {
			fmt.Fprintf(&text, "  - caller: %s\n", edge.Caller.QualifiedName())
			fmt.Fprintf(&text, "    caller_path: %s\n", edge.Caller.Path)
			fmt.Fprintf(&text, "    caller_line: %d\n", edge.Caller.Line)
			fmt.Fprintf(&text, "    callee: %s\n", edge.Callee.QualifiedName())
			fmt.Fprintf(&text, "    call: %s:%d\n", edge.CallPath, edge.CallLine)
			fmt.Fprintf(&text, "    certainty: %s\n", edge.Certainty)
		}
	}
	if len(result.Importers) > 0 {
		text.WriteString("\nimporters:\n")
		for _, importer := range result.Importers {
			fmt.Fprintf(&text, "  - %s\n", importer)
		}
	}
	if len(result.Tests) > 0 {
		text.WriteString("\ntest_associations:\n")
		for _, association := range result.Tests {
			fmt.Fprintf(&text, "  - test: %s\n", association.Test.QualifiedName)
			fmt.Fprintf(&text, "    path: %s\n", association.Path)
			fmt.Fprintf(&text, "    line: %d\n", association.Line)
			fmt.Fprintf(&text, "    relation: %s\n", association.Relation)
		}
	}

	details := map[string]any{
		"status":     status,
		"view":       "impact",
		"snapshot":   snapshotMap(result.Snapshot),
		"query_type": result.QueryType,
		"selector":   result.Selector,
		"found":      result.Found,
		"ambiguous":  result.Ambiguous,
		"candidates": refTargetMaps(result.Candidates),
		"targets":    refTargetMaps(result.Targets),
		"references": referenceMaps(result.References),
		"callers":    callEdgeMaps(result.Callers),
		"importers":  append([]string(nil), result.Importers...),
		"tests":      associationMaps(result.Tests),
		"truncated":  result.Truncated,
		"reason":     result.Reason,
		"unknown":    unknownMap(result.Unknown),
		"nodes":      nodeMaps(result.Nodes),
		"edges":      edgeMaps(result.Edges),
	}
	addStructuralProvenance(details, idx, result.Found && !result.Truncated && !result.Ambiguous)
	details["coordinates"] = impactCoordinates(result)
	return agent.Result{
		Text:    strings.TrimRight(text.String(), "\n"),
		Details: details,
		Useless: !result.Found && !result.Ambiguous,
	}
}

// validateStructureArgs rejects arguments outside the closed schema.
func validateStructureArgs(args map[string]any) error {
	allowed := map[string]bool{
		"view": true, "path": true, "package": true, "symbol": true,
		"limit": true, "skip": true, "depth": true, "activation": true,
	}
	for key := range args {
		if !allowed[key] {
			return fmt.Errorf("unknown structure argument %q", key)
		}
	}
	return nil
}

// structureActivation resolves the requested depth and rejects a view that
// would need more than the caller allowed. The default is the smallest depth
// that can answer the view: overview views stay metadata-only.
func structureActivation(args map[string]any, view string) (ast.Activation, error) {
	required := ast.ActivationOverview
	switch view {
	case "overview", "scope", "languages", "files", "configs":
		required = ast.ActivationOverview
	case "packages", "entrypoints", "tests":
		required = ast.ActivationTargeted
	case "relations", "impact":
		required = ast.ActivationImpact
	}
	requested := required
	if raw, ok := args["activation"]; ok {
		value, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("argument %q must be a string", "activation")
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "overview":
			requested = ast.ActivationOverview
		case "targeted":
			requested = ast.ActivationTargeted
		case "impact":
			requested = ast.ActivationImpact
		case "semantic":
			requested = ast.ActivationSemantic
		default:
			return "", fmt.Errorf("unsupported activation %q", value)
		}
	}
	if requested == ast.ActivationSemantic {
		return requested, nil
	}
	rank := func(level ast.Activation) int {
		switch level {
		case ast.ActivationOverview:
			return 1
		case ast.ActivationTargeted:
			return 2
		case ast.ActivationImpact:
			return 3
		default:
			return 0
		}
	}
	if rank(requested) < rank(required) {
		return "", fmt.Errorf("view %q requires %s activation, got %s", view, required, requested)
	}
	return requested, nil
}

// structureStatus is the shared inventory status vocabulary.
func structureStatus(empty, truncated bool) string {
	switch {
	case empty:
		return "not_found"
	case truncated:
		return "truncated"
	default:
		return "ok"
	}
}

// writeStructureHeader renders the provenance every inventory view shares.
//
// Completeness is four independent axes. A CURRENT snapshot can be
// structurally partial, a structurally complete snapshot can have excluded
// roots, and a complete query can still be truncated by its own page bound.
// Collapsing them into one "partial" flag is what lets an absence claim escape
// a scope it never covered.
func writeStructureHeader(text *strings.Builder, status, view string, info ast.SnapshotInfo, resultTruncated bool, reason string) {
	fmt.Fprintf(text, "status: %s\n", status)
	fmt.Fprintf(text, "view: %s\n", view)
	fmt.Fprintf(text, "snapshot_id: %s\n", info.SnapshotID)
	fmt.Fprintf(text, "generation: %d\n", info.Generation)
	fmt.Fprintf(text, "graph_state: %s\n", info.State)
	fmt.Fprintf(text, "activation: %s\n", info.Activation)
	if info.Module != "" {
		fmt.Fprintf(text, "module: %s\n", info.Module)
	}
	fmt.Fprintf(text, "scope: %s\n", completeLabel(info.ScopeComplete))
	if !info.ScopeComplete {
		fmt.Fprintf(text, "scope_gaps: %d excluded_roots, %d excluded_source_files\n",
			len(info.ExcludedRoots), info.ExcludedSourceFiles)
	}
	fmt.Fprintf(text, "inventory: %s\n", completeLabel(info.InventoryComplete))
	fmt.Fprintf(text, "structural: %s\n", structuralCompleteness(info))
	fmt.Fprintf(text, "result: %s\n", resultLabel(resultTruncated))
	if resultTruncated && reason != "" {
		fmt.Fprintf(text, "result_reason: %s\n", reason)
	}
	fmt.Fprintf(text, "parsed_files: %d pending_files: %d failed_files: %d\n",
		info.ParsedFiles, info.PendingFiles, info.FailedFiles)
	if info.ParseFailures > 0 {
		fmt.Fprintf(text, "parse_failures: %d\n", info.ParseFailures)
	}
	if len(info.Unsupported) > 0 {
		parts := make([]string, 0, len(info.Unsupported))
		for _, item := range info.Unsupported {
			parts = append(parts, fmt.Sprintf("%s(%d)", item.Extension, item.Files))
		}
		fmt.Fprintf(text, "unsupported_source: %s\n", strings.Join(parts, ", "))
	}
}

func completeLabel(complete bool) string {
	if complete {
		return "COMPLETE"
	}
	return "PARTIAL"
}

func resultLabel(truncated bool) string {
	if truncated {
		return "TRUNCATED"
	}
	return "COMPLETE"
}

// displayPackage renders a package name, or the ambiguity list.
func displayPackage(pkg ast.PackageInfo) string {
	if pkg.Name != "" {
		return pkg.Name
	}
	if len(pkg.Packages) > 0 {
		return strings.Join(pkg.Packages, "|")
	}
	return "?"
}

// snapshotMap projects the snapshot header into structured details.
func snapshotMap(info ast.SnapshotInfo) map[string]any {
	return map[string]any{
		"root":                  info.Root,
		"module":                info.Module,
		"module_root":           info.ModuleRoot,
		"generation":            info.Generation,
		"snapshot_id":           info.SnapshotID,
		"source_fingerprint":    info.SourceFingerprint,
		"scope_fingerprint":     info.ScopeFingerprint,
		"state":                 string(info.State),
		"activation":            string(info.Activation),
		"built_at":              info.BuiltAt,
		"files":                 info.Files,
		"go_files":              info.GoFiles,
		"source_files":          info.SourceFiles,
		"parsed_files":          info.ParsedFiles,
		"pending_files":         info.PendingFiles,
		"failed_files":          info.FailedFiles,
		"directories":           info.Directories,
		"packages":              info.Packages,
		"symbols":               info.Symbols,
		"references":            info.References,
		"calls":                 info.Calls,
		"tests":                 info.Tests,
		"test_files":            info.TestFiles,
		"entrypoints":           info.Entrypoints,
		"configs":               info.Configs,
		"parse_failures":        info.ParseFailures,
		"partial":               info.Partial,
		"scope_complete":        info.ScopeComplete,
		"excluded_roots":        excludedRootMaps(info.ExcludedRoots),
		"excluded_source_files": info.ExcludedSourceFiles,
		"excluded_files":        excludedFileMaps(info.ExcludedFiles),
		"nested_projects":       projectBoundaryMaps(info.NestedProjects),
		"inventory_complete":    info.InventoryComplete,
		"structural_complete":   info.StructuralComplete,
		"languages":             languageMaps(info.Languages),
		"unsupported":           unsupportedMaps(info.Unsupported),
		"last_parsed_files":     info.LastParsedFiles,
		"last_reused_files":     info.LastReusedFiles,
		"last_resolution":       info.LastResolution,
	}
}

// languageSummary renders the compact language mix for a text result.
func languageSummary(languages []ast.LanguageCoverage) string {
	parts := make([]string, 0, len(languages))
	for _, language := range languages {
		parts = append(parts, fmt.Sprintf("%s=%s/%d", ast.DisplayLanguage(language.Language), language.Coverage, language.Files))
	}
	return strings.Join(parts, ", ")
}

func languageMaps(languages []ast.LanguageCoverage) []map[string]any {
	out := make([]map[string]any, 0, len(languages))
	for _, language := range languages {
		out = append(out, map[string]any{
			"language":  string(language.Language),
			"supported": language.Supported,
			"files":     language.Files,
			"parsed":    language.Parsed,
			"failed":    language.Failed,
			"pending":   language.Pending,
			"symbols":   language.Symbols,
			"coverage":  language.Coverage,
		})
	}
	return out
}

func unsupportedMaps(items []ast.UnsupportedLanguage) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"extension": item.Extension,
			"files":     item.Files,
		})
	}
	return out
}

func excludedRootMaps(items []ast.ExcludedRoot) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"path":              item.Path,
			"reason":            item.Reason,
			"rule":              item.Rule,
			"nested_repository": item.NestedRepository,
			"markers":           append([]string(nil), item.Markers...),
			"languages":         languageStrings(item.Languages),
		})
	}
	return out
}

func excludedFileMaps(items []ast.ExcludedFile) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{"path": item.Path, "rule": item.Rule})
	}
	return out
}

func projectBoundaryMaps(items []ast.ProjectBoundary) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"path":              item.Path,
			"included":          item.Included,
			"parent_scope_rule": item.ParentScopeRule,
			"reason":            item.Reason,
			"markers":           append([]string(nil), item.Markers...),
			"languages":         languageStrings(item.Languages),
		})
	}
	return out
}

func languageStrings(items []ast.Language) []string {
	out := make([]string, 0, len(items))
	for _, language := range items {
		out = append(out, string(language))
	}
	return out
}

func fileMaps(files []ast.FileInfo) []map[string]any {
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"path": file.Path, "dir": file.Dir, "package": file.Package,
			"language": file.Language, "adapter": file.Adapter, "kind": string(file.Kind),
			"test": file.Test, "parsed": file.Parsed, "parse_error": file.ParseError,
			"unsupported": file.Unsupported, "size": file.Size, "symbols": file.Symbols,
			"exports": file.Exports,
		})
	}
	return out
}

func packageMaps(packages []ast.PackageInfo) []map[string]any {
	out := make([]map[string]any, 0, len(packages))
	for _, pkg := range packages {
		out = append(out, map[string]any{
			"dir": pkg.Dir, "name": pkg.Name, "packages": append([]string(nil), pkg.Packages...),
			"language": string(pkg.Language), "languages": append([]string(nil), pkg.Languages...),
			"files": pkg.Files, "test_files": pkg.TestFiles, "symbols": pkg.Symbols,
			"imports": pkg.Imports, "importers": pkg.Importers, "ambiguous": pkg.Ambiguous,
		})
	}
	return out
}

func entrypointMaps(entries []ast.Entrypoint) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, map[string]any{
			"path": entry.Path, "package": entry.Package, "symbol": entry.Symbol,
			"qualified_name": entry.QualifiedName, "language": string(entry.Language),
			"native_kind": entry.NativeKind, "line": entry.Line, "end_line": entry.EndLine,
			"column": entry.Column,
		})
	}
	return out
}

func configMaps(configs []ast.ConfigInfo) []map[string]any {
	out := make([]map[string]any, 0, len(configs))
	for _, config := range configs {
		out = append(out, map[string]any{"path": config.Path, "kind": config.Kind, "value": config.Value})
	}
	return out
}

func testMaps(tests []ast.TestInfo) []map[string]any {
	out := make([]map[string]any, 0, len(tests))
	for _, test := range tests {
		out = append(out, map[string]any{
			"path": test.Path, "package": test.Package, "symbol": test.Symbol,
			"qualified_name": test.QualifiedName, "language": string(test.Language),
			"framework": test.Framework, "kind": string(test.Kind),
			"line": test.Line, "end_line": test.EndLine,
		})
	}
	return out
}

func associationMaps(items []ast.TestAssociation) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"test": map[string]any{
				"path": item.Test.Path, "package": item.Test.Package, "symbol": item.Test.Symbol,
				"qualified_name": item.Test.QualifiedName, "line": item.Test.Line, "end_line": item.Test.EndLine,
			},
			"relation": item.Relation, "path": item.Path, "line": item.Line,
			"column": item.Column, "call": item.Call,
		})
	}
	return out
}

func refTargetMaps(targets []ast.RefTarget) []map[string]any {
	out := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		out = append(out, map[string]any{
			"path": target.Path, "package": target.Package, "symbol": target.Symbol,
			"qualified_name": target.QualifiedName, "kind": string(target.Kind),
			"line": target.Line, "end_line": target.EndLine,
		})
	}
	return out
}

func referenceMaps(refs []ast.ReferenceSite) []map[string]any {
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		out = append(out, map[string]any{
			"path": ref.Path, "line": ref.Line, "column": ref.Column,
			"qualified": ref.Qualified, "call": ref.Call, "test": ref.Test,
		})
	}
	return out
}

func callEdgeMaps(edges []ast.CallEdge) []map[string]any {
	out := make([]map[string]any, 0, len(edges))
	for _, edge := range edges {
		out = append(out, map[string]any{
			"caller": map[string]any{
				"path": edge.Caller.Path, "package": edge.Caller.Package, "symbol": edge.Caller.Symbol,
				"qualified_name": edge.Caller.QualifiedName(), "line": edge.Caller.Line,
			},
			"callee": map[string]any{
				"path": edge.Callee.Path, "package": edge.Callee.Package, "symbol": edge.Callee.Symbol,
				"qualified_name": edge.Callee.QualifiedName(), "line": edge.Callee.Line,
			},
			"call_path": edge.CallPath, "call_line": edge.CallLine, "column": edge.Column,
			"certainty": string(edge.Certainty), "test": edge.Test, "in_closure": edge.InClosure,
		})
	}
	return out
}

func nodeMaps(nodes []ast.Node) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, map[string]any{
			"id": node.ID, "kind": string(node.Kind), "name": node.Name,
			"qualified_name": node.Qualified, "native_kind": node.NativeKind,
			"language": string(node.Language), "exported": node.Exported,
			"path": node.Source.Path, "line": node.Source.StartLine,
		})
	}
	return out
}

func edgeMaps(edges []ast.Edge) []map[string]any {
	out := make([]map[string]any, 0, len(edges))
	for _, edge := range edges {
		out = append(out, map[string]any{
			"from": edge.From, "to": edge.To, "kind": string(edge.Kind),
			"certainty": edge.Certainty, "resolution": string(edge.Resolution),
			"language": string(edge.Language), "adapter": edge.Adapter,
			"path": edge.Source.Path, "line": edge.Source.StartLine, "column": edge.Source.StartCol,
		})
	}
	return out
}

func unknownMap(unknown ast.UnknownSummary) map[string]any {
	return map[string]any{
		"dot_import_files":          unknown.DotImportFiles,
		"ambiguous_uses":            unknown.AmbiguousUses,
		"unresolved_uses":           unknown.UnresolvedUses,
		"dynamic_calls":             unknown.DynamicCalls,
		"package_initializer_calls": unknown.PackageInitializerCalls,
		"generic_unresolved":        unknown.GenericUnresolved,
		"generic_ambiguous":         unknown.GenericAmbiguous,
		"unsupported_files":         unknown.UnsupportedFiles,
		"reasons":                   unknown.Reasons,
	}
}

// writeImpactUnknown renders the static blind spots beside the result.
func writeImpactUnknown(text *strings.Builder, unknown ast.UnknownSummary) {
	if unknown.DotImportFiles > 0 {
		fmt.Fprintf(text, "dot_import_files: %d\n", unknown.DotImportFiles)
	}
	if unknown.AmbiguousUses > 0 {
		fmt.Fprintf(text, "repository_ambiguous_uses: %d\n", unknown.AmbiguousUses)
	}
	if unknown.UnresolvedUses > 0 {
		fmt.Fprintf(text, "repository_unresolved_uses: %d\n", unknown.UnresolvedUses)
	}
	if unknown.DynamicCalls > 0 {
		fmt.Fprintf(text, "dynamic_calls: %d\n", unknown.DynamicCalls)
	}
}

// Normalized evidence coordinates for the shared evidence layer. The evidence
// model stays one model: each structural tool projects its rows into the same
// small coordinate shape instead of teaching cognition every result schema.
func overviewCoordinates(packages []ast.PackageInfo, entries []ast.Entrypoint) []map[string]any {
	out := packageCoordinates(packages)
	out = append(out, entrypointCoordinates(entries)...)
	return out
}

func fileCoordinates(files []ast.FileInfo) []map[string]any {
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"path": file.Path, "line": 1, "symbol": file.Path,
			"kind": string(file.Kind), "query": "file",
		})
	}
	return out
}

func packageCoordinates(packages []ast.PackageInfo) []map[string]any {
	out := make([]map[string]any, 0, len(packages))
	for _, pkg := range packages {
		out = append(out, map[string]any{
			"path": pkg.Dir, "line": 1, "symbol": displayPackage(pkg), "kind": "package",
		})
	}
	return out
}

func entrypointCoordinates(entries []ast.Entrypoint) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, map[string]any{
			"path": entry.Path, "line": entry.Line, "column": entry.Column,
			"end_line": entry.EndLine, "symbol": entry.QualifiedName, "kind": "entrypoint",
		})
	}
	return out
}

func configCoordinates(configs []ast.ConfigInfo) []map[string]any {
	out := make([]map[string]any, 0, len(configs))
	for _, config := range configs {
		out = append(out, map[string]any{
			"path": config.Path, "line": 1, "symbol": config.Path, "kind": "config",
		})
	}
	return out
}

func testCoordinates(tests []ast.TestInfo, associations []ast.TestAssociation) []map[string]any {
	out := make([]map[string]any, 0, len(tests)+len(associations))
	for _, test := range tests {
		out = append(out, map[string]any{
			"path": test.Path, "line": test.Line, "end_line": test.EndLine,
			"symbol": test.QualifiedName, "kind": "test",
		})
	}
	for _, association := range associations {
		out = append(out, map[string]any{
			"path": association.Path, "line": association.Line, "column": association.Column,
			"symbol": association.Test.QualifiedName, "kind": "test_association",
		})
	}
	return out
}

func impactCoordinates(result ast.ImpactResult) []map[string]any {
	out := make([]map[string]any, 0)
	for _, target := range result.Targets {
		out = append(out, map[string]any{
			"path": target.Path, "line": target.Line, "end_line": target.EndLine,
			"symbol": target.QualifiedName, "kind": "impact_target",
		})
	}
	for _, ref := range result.References {
		out = append(out, map[string]any{
			"path": ref.Path, "line": ref.Line, "column": ref.Column,
			"symbol": "reference", "kind": "reference",
		})
	}
	for _, edge := range result.Callers {
		out = append(out, map[string]any{
			"path": edge.CallPath, "line": edge.CallLine, "column": edge.Column,
			"symbol": edge.Caller.QualifiedName(), "kind": "caller",
		})
	}
	for _, dir := range result.Importers {
		out = append(out, map[string]any{"path": dir, "line": 1, "symbol": dir, "kind": "importer"})
	}
	for _, association := range result.Tests {
		out = append(out, map[string]any{
			"path": association.Path, "line": association.Line,
			"symbol": association.Test.QualifiedName, "kind": "test_association",
		})
	}
	for _, edge := range result.Edges {
		if edge.Source.Path == "" {
			continue
		}
		out = append(out, map[string]any{
			"path": edge.Source.Path, "line": edge.Source.StartLine,
			"column": edge.Source.StartCol, "symbol": string(edge.Kind) + " " + edge.To,
			"kind": "structural_edge", "language": string(edge.Language),
			"resolution": string(edge.Resolution),
		})
	}
	return out
}
