package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// Symbol query limits.
const (
	// SymbolDefaultLimit is the page size when the caller does not choose one.
	SymbolDefaultLimit = 50
	// SymbolMaxLimit is the hard ceiling on one response page.
	SymbolMaxLimit = 200
)

// SymbolsTool answers structural symbol queries against the repository.
type SymbolsTool struct {
	Base
	// Root bounds every query.
	Root string

	// source is the shared, lazily built structural index. It is the only
	// repository coordinate store; the graph tool reads the same instance.
	source *StructuralSource
}

// NewSymbolsTool returns the symbol-coordinate tool rooted at root.
//
// It owns a private StructuralSource. Production buildTools uses
// NewSymbolsToolWithSource so the graph tool shares that source.
func NewSymbolsTool(root string) *SymbolsTool {
	return NewSymbolsToolWithSource(NewStructuralSource(root))
}

// NewSymbolsToolWithSource returns the symbol-coordinate tool over source.
func NewSymbolsToolWithSource(source *StructuralSource) *SymbolsTool {
	root := ""
	if source != nil {
		root = source.Root()
	}
	tool := &SymbolsTool{Root: root, source: source}
	tool.Base = NewBase("symbols",
		"Locate Go declarations by exact symbol name, or list the declarations "+
			"in a file, directory, or package. Returns repository-relative "+
			"coordinates (path, package, symbol, kind, start_line, end_line) "+
			"without source. Use read on a returned coordinate for the exact "+
			"declaration. Ambiguous, truncated, and partially parsed results "+
			"are reported explicitly.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"symbol": map[string]any{
					"type":      "string",
					"minLength": 1,
					"description": "Exact symbol name or qualified name, case-sensitive. " +
						"Examples: GrepTool.Execute, tools.GrepTool.Execute, agent.Tool.",
				},
				"path": map[string]any{
					"type":      "string",
					"minLength": 1,
					"description": "File or directory, relative to the working directory. " +
						"Lists its declarations, or scopes a symbol or package query.",
				},
				"package": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Go package name to list or to scope a symbol query.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of symbols to return.",
					"minimum":     1.0,
					"maximum":     float64(SymbolMaxLimit),
				},
				"skip": map[string]any{
					"type":        "integer",
					"description": "Number of symbols to skip, for pagination.",
					"minimum":     0.0,
				},
			},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	tool.SetRequiredLanguage("go")
	return tool
}

// index returns the shared repository index, building it on first use.
// Freshness is owned by the index itself: every query entry point
// revalidates against the tree (see Index.ensureFresh), so the tool must
// not add a second refresh policy here.
func (t *SymbolsTool) index() (*ast.Index, error) {
	if t.source == nil {
		return nil, fmt.Errorf("tools: symbols tool has no structural source")
	}
	return t.source.Index()
}

// Source returns the structural source the tool reads. It exists so wiring
// tests can prove the coordinate and relationship surfaces share one snapshot.
func (t *SymbolsTool) Source() *StructuralSource { return t.source }

// LocateSymbol implements agent.StructuralResolver over the cached index.
//
// The completion verifier shares this tool's index rather than building a
// second coordinate store. The lookup is exact, matching the symbols tool's
// own contract, so a completion check cannot be more permissive than the
// coordinate the user named.
func (t *SymbolsTool) LocateSymbol(name string) ([]agent.SymbolLocation, error) {
	idx, err := t.index()
	if err != nil {
		return nil, err
	}
	matches, err := idx.Lookup(name)
	if err != nil {
		return nil, err
	}
	return symbolLocations(matches), nil
}

// LocatePackage implements agent.StructuralResolver over the cached index.
func (t *SymbolsTool) LocatePackage(name string) ([]agent.SymbolLocation, error) {
	idx, err := t.index()
	if err != nil {
		return nil, err
	}
	matches, err := idx.Package(name)
	if err != nil {
		return nil, err
	}
	return symbolLocations(matches), nil
}

// symbolLocations projects index matches to the verifier's small coordinate
// type. The index remains the owner of the declaration data.
func symbolLocations(matches []ast.Match) []agent.SymbolLocation {
	out := make([]agent.SymbolLocation, 0, len(matches))
	for _, match := range matches {
		out = append(out, agent.SymbolLocation{
			Path:          match.Path,
			Symbol:        match.Name,
			QualifiedName: match.QualifiedName,
			Line:          match.Line,
			EndLine:       match.EndLine,
		})
	}
	return out
}

// Execute implements agent.Tool.
func (t *SymbolsTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	symbolQuery, err := strArg(args, "symbol", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	symbolQuery = strings.TrimSpace(symbolQuery)
	packageQuery, err := strArg(args, "package", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	packageQuery = strings.TrimSpace(packageQuery)
	pathArg, err := strArg(args, "path", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	pathArg = strings.TrimSpace(pathArg)

	limit, err := intArg(args, "limit", SymbolDefaultLimit)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if limit < 1 {
		limit = SymbolDefaultLimit
	}
	if limit > SymbolMaxLimit {
		limit = SymbolMaxLimit
	}
	skip, err := intArg(args, "skip", 0)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if skip < 0 {
		return agent.ErrorResult("skip must be >= 0, got %d", skip)
	}

	// Resolve the path through the workspace containment authority once.
	// Every later filter uses the repository-relative spelling, so a
	// symbol can never be selected from outside the requested scope.
	var scope *symbolScope
	if pathArg != "" {
		resolved, err := resolvePath(t.Root, pathArg)
		if err != nil {
			return agent.ErrorResult("resolve path: %v", err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return agent.ErrorResult("stat %q: %v", pathArg, err)
		}
		rel, err := filepath.Rel(t.Root, resolved)
		if err != nil {
			return agent.ErrorResult("relativize %q: %v", pathArg, err)
		}
		scope = &symbolScope{
			rel: filepath.ToSlash(rel),
			dir: info.IsDir(),
			abs: resolved,
		}
	}

	idx, err := t.index()
	if err != nil {
		return agent.ErrorResult("build symbol index: %v", err)
	}
	// The first query may spend time parsing the repository. If the turn
	// was cancelled while that happened, do not go on to answer from it.
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	var symbols []ast.Match
	var queryType string
	switch {
	case symbolQuery != "":
		queryType = "lookup"
		symbols, err = idx.Lookup(symbolQuery)
	case packageQuery != "":
		queryType = "package"
		symbols, err = idx.Package(packageQuery)
	case scope != nil && scope.dir:
		queryType = "directory"
		symbols, err = idx.Symbols()
	case scope != nil:
		queryType = "file"
		symbols, err = idx.File(scope.abs)
	default:
		queryType = "list"
		symbols, err = idx.Symbols()
	}
	if err != nil {
		return agent.ErrorResult("%s symbols: %v", queryType, err)
	}

	// A path scopes lookups, package listings, and directory listings. A
	// file listing is already exact, so it needs no second filter.
	if scope != nil && queryType != "file" {
		filtered := symbols[:0]
		for _, s := range symbols {
			if scope.contains(s.Path) {
				filtered = append(filtered, s)
			}
		}
		symbols = filtered
	}
	if queryType == "lookup" || queryType == "package" {
		if packageQuery != "" {
			filtered := symbols[:0]
			for _, s := range symbols {
				if s.Package == packageQuery {
					filtered = append(filtered, s)
				}
			}
			symbols = filtered
		}
	}

	// Every Index query already returns path/line/qualified-name order, and
	// filtering preserves it, so there is no second ordering rule here.

	total := len(symbols)
	if skip > total {
		skip = total
	}
	symbols = symbols[skip:]
	truncated := len(symbols) > limit
	if truncated {
		symbols = symbols[:limit]
	}
	returned := len(symbols)
	ambiguous := queryType == "lookup" && total > 1
	parseFailures := idx.ParseFailures()

	status := "ok"
	switch {
	case total == 0:
		status = "not_found"
	case ambiguous:
		status = "ambiguous"
	case truncated:
		status = "truncated"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "status: %s\n", status)
	if total == 0 {
		fmt.Fprintf(&sb, "query_type: %s\n", queryType)
		if parseFailures > 0 {
			fmt.Fprintf(&sb, "unparsed: %d\n", parseFailures)
		}
		return agent.Result{
			Text: strings.TrimRight(sb.String(), "\n"),
			Details: map[string]any{
				"status":         "not_found",
				"query_type":     queryType,
				"results":        0,
				"returned":       0,
				"truncated":      false,
				"ambiguous":      false,
				"parse_failures": parseFailures,
			},
			Useless: parseFailures == 0,
		}
	}
	if truncated || skip > 0 {
		fmt.Fprintf(&sb, "returned: %d\n", returned)
		fmt.Fprintf(&sb, "total: %d\n", total)
		if skip > 0 {
			fmt.Fprintf(&sb, "skip: %d\n", skip)
		}
		if truncated && status != "truncated" {
			// The status line already says truncated; an ambiguous lookup
			// needs the extra line so both facts reach the model.
			sb.WriteString("truncated: true\n")
		}
	}
	if parseFailures > 0 {
		fmt.Fprintf(&sb, "unparsed: %d\n", parseFailures)
	}

	sb.WriteString("\nmatches:\n")
	for _, s := range symbols {
		fmt.Fprintf(&sb, "  - path: %s\n", s.Path)
		fmt.Fprintf(&sb, "    package: %s\n", s.Package)
		fmt.Fprintf(&sb, "    symbol: %s\n", s.Name)
		fmt.Fprintf(&sb, "    kind: %s\n", s.Kind)
		fmt.Fprintf(&sb, "    start_line: %d\n", s.Line)
		fmt.Fprintf(&sb, "    end_line: %d\n", s.EndLine)
	}

	coords := make([]map[string]any, 0, len(symbols))
	for _, s := range symbols {
		coords = append(coords, map[string]any{
			"path":       s.Path,
			"package":    s.Package,
			"symbol":     s.Name,
			"kind":       string(s.Kind),
			"start_line": s.Line,
			"end_line":   s.EndLine,
		})
	}

	return agent.Result{
		Text: strings.TrimRight(sb.String(), "\n"),
		Details: map[string]any{
			"status":         status,
			"query_type":     queryType,
			"results":        total,
			"returned":       returned,
			"truncated":      truncated,
			"ambiguous":      ambiguous,
			"parse_failures": parseFailures,
			"coordinates":    coords,
		},
	}
}

// symbolScope is one resolved file or directory filter.
type symbolScope struct {
	// rel is the repository-relative path, slash-separated.
	rel string
	// dir reports whether rel names a directory.
	dir bool
	// abs is the resolved absolute path used for direct file lookups.
	abs string
}

// contains reports whether a repository-relative file path is inside the
// scope. The root itself is spelled "." by filepath.Rel.
func (s *symbolScope) contains(path string) bool {
	if s.rel == "." || s.rel == "" {
		return true
	}
	if !s.dir {
		return path == s.rel
	}
	return strings.HasPrefix(path, s.rel+"/")
}
