package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
			"repository snapshot: files, packages, tests, configs, "+
			"entrypoints, and bounded static impact. Select a view and "+
			"optionally scope it with path, package, or symbol. Every result "+
			"names its snapshot id, generation, and completeness; test "+
			"associations are static and are not coverage. This is advisory "+
			"evidence, not permission for mutation or execution.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"view": map[string]any{
					"type":        "string",
					"enum":        []any{"overview", "files", "packages", "entrypoints", "configs", "tests", "impact"},
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
					"description": "Caller traversal depth for impact (default 1).",
					"minimum":     1.0,
					"maximum":     float64(ast.RefMaxDepth),
				},
			},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	tool.SetRequiredLanguage("go")
	tool.SetActionClasses(agent.ActionObserve)
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
		case "overview", "files", "packages", "entrypoints", "configs", "tests", "impact":
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
	if view == "impact" && symbolArg == "" && pathArg == "" {
		return agent.ErrorResult("impact requires symbol or path")
	}
	switch view {
	case "overview", "entrypoints", "configs":
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
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	switch view {
	case "overview":
		return t.overview(ctx, idx, limit)
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
	case "impact":
		return t.impact(ctx, idx, symbolArg, pathArg, depth, limit)
	default:
		return agent.ErrorResult("unsupported view %q", view)
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
	var text strings.Builder
	writeStructureHeader(&text, status, "overview", info)
	fmt.Fprintf(&text, "packages: %d\n", info.Packages)
	fmt.Fprintf(&text, "files: %d (go: %d, configs: %d)\n", info.Files, info.GoFiles, info.Configs)
	fmt.Fprintf(&text, "symbols: %d\n", info.Symbols)
	fmt.Fprintf(&text, "references: %d\n", info.References)
	fmt.Fprintf(&text, "calls: %d\n", info.Calls)
	fmt.Fprintf(&text, "tests: %d\n", info.Tests)
	fmt.Fprintf(&text, "entrypoints: %d\n", info.Entrypoints)
	fmt.Fprintf(&text, "directories: %d\n", info.Directories)
	fmt.Fprintf(&text, "last_parsed_files: %d\n", info.LastParsedFiles)
	fmt.Fprintf(&text, "last_reused_files: %d\n", info.LastReusedFiles)
	fmt.Fprintf(&text, "last_resolution: %s\n", info.LastResolution)
	if info.Module != "" {
		fmt.Fprintf(&text, "module: %s\n", info.Module)
	}
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
		"truncated":   result.Truncated,
		"reason":      result.Reason,
	}
	addStructuralProvenance(details, idx, !result.Truncated)
	details["coordinates"] = overviewCoordinates(result.Packages, result.Entrypoints)
	return agent.Result{Text: strings.TrimRight(text.String(), "\n"), Details: details}
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
	writeStructureHeader(&text, status, "files", result.Snapshot)
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
	writeStructureHeader(&text, status, "packages", result.Snapshot)
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
	writeStructureHeader(&text, status, "entrypoints", result.Snapshot)
	fmt.Fprintf(&text, "total: %d\n", len(result.Entrypoints))
	if len(result.Entrypoints) > 0 {
		text.WriteString("\nentrypoints:\n")
		for _, entry := range result.Entrypoints {
			fmt.Fprintf(&text, "  - symbol: %s\n", entry.QualifiedName)
			fmt.Fprintf(&text, "    path: %s\n", entry.Path)
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
	writeStructureHeader(&text, status, "configs", result.Snapshot)
	fmt.Fprintf(&text, "total: %d\n", len(result.Configs))
	if len(result.Configs) > 0 {
		text.WriteString("\nconfigs:\n")
		for _, config := range result.Configs {
			fmt.Fprintf(&text, "  - path: %s\n", config.Path)
			fmt.Fprintf(&text, "    kind: %s\n", config.Kind)
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
	writeStructureHeader(&text, status, "tests", result.Snapshot)
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
	writeStructureHeader(&text, status, "impact", result.Snapshot)
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
		"limit": true, "skip": true, "depth": true,
	}
	for key := range args {
		if !allowed[key] {
			return fmt.Errorf("unknown structure argument %q", key)
		}
	}
	return nil
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
func writeStructureHeader(text *strings.Builder, status, view string, info ast.SnapshotInfo) {
	fmt.Fprintf(text, "status: %s\n", status)
	fmt.Fprintf(text, "view: %s\n", view)
	fmt.Fprintf(text, "snapshot_id: %s\n", info.SnapshotID)
	fmt.Fprintf(text, "generation: %d\n", info.Generation)
	fmt.Fprintf(text, "graph_state: %s\n", info.State)
	if info.Module != "" {
		fmt.Fprintf(text, "module: %s\n", info.Module)
	}
	if info.Partial {
		fmt.Fprintf(text, "partial: true\nparse_failures: %d\n", info.ParseFailures)
	}
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
		"root":               info.Root,
		"module":             info.Module,
		"module_root":        info.ModuleRoot,
		"generation":         info.Generation,
		"snapshot_id":        info.SnapshotID,
		"source_fingerprint": info.SourceFingerprint,
		"scope_fingerprint":  info.ScopeFingerprint,
		"state":              string(info.State),
		"built_at":           info.BuiltAt,
		"files":              info.Files,
		"go_files":           info.GoFiles,
		"directories":        info.Directories,
		"packages":           info.Packages,
		"symbols":            info.Symbols,
		"references":         info.References,
		"calls":              info.Calls,
		"tests":              info.Tests,
		"entrypoints":        info.Entrypoints,
		"configs":            info.Configs,
		"parse_failures":     info.ParseFailures,
		"partial":            info.Partial,
		"last_parsed_files":  info.LastParsedFiles,
		"last_reused_files":  info.LastReusedFiles,
		"last_resolution":    info.LastResolution,
	}
}

func fileMaps(files []ast.FileInfo) []map[string]any {
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"path": file.Path, "dir": file.Dir, "package": file.Package,
			"language": file.Language, "kind": string(file.Kind), "test": file.Test,
			"size": file.Size, "symbols": file.Symbols,
		})
	}
	return out
}

func packageMaps(packages []ast.PackageInfo) []map[string]any {
	out := make([]map[string]any, 0, len(packages))
	for _, pkg := range packages {
		out = append(out, map[string]any{
			"dir": pkg.Dir, "name": pkg.Name, "packages": append([]string(nil), pkg.Packages...),
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
			"qualified_name": entry.QualifiedName, "line": entry.Line, "end_line": entry.EndLine,
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
			"qualified_name": test.QualifiedName, "kind": string(test.Kind),
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
			"certainty": edge.Certainty, "path": edge.Source.Path,
			"line": edge.Source.StartLine, "column": edge.Source.StartCol,
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
	return out
}
