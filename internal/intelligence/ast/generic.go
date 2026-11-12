package ast

import (
	"context"
	"path"
	"sort"
	"strconv"
	"strings"
)

// rawGenericCall is one language-neutral call site. Go call sites stay in the
// precise Go projection; this shape exists for adapters whose dispatch is
// name-based and cannot prove receiver types.
type rawGenericCall struct {
	Name          string
	Qualifier     string
	Line          int
	Column        int
	CallerName    string
	CallerLine    int
	CallerEndLine int
	CallerKind    Kind
	InClosure     bool
}

// rawGenericReference is one non-call syntactic reference, e.g. an extension
// or implementation target.
type rawGenericReference struct {
	Name      string
	Qualifier string
	Line      int
	Column    int
	Kind      EdgeKind
}

// rawRelation is one explicit inheritance/mixin clause.
type rawRelation struct {
	Kind   EdgeKind
	Target string
	// Source is the declaration the relation belongs to. When empty, the
	// resolver uses the narrowest declaration enclosing Line.
	Source string
	Line   int
	Column int
}

// genericCall is one resolved or disclosed call site.
type genericCall struct {
	call       rawGenericCall
	caller     Match
	callee     Match
	resolution Resolution
}

// genericReference is one resolved or disclosed reference site.
type genericReference struct {
	ref        rawGenericReference
	file       string
	language   Language
	test       bool
	tests      []TestDecl
	target     Match
	resolution Resolution
}

// genericImport is one import/include/require declaration with its resolved
// repository target, when the static path is unambiguous.
type genericImport struct {
	from       string
	language   Language
	imported   Import
	resolution Resolution
	resolved   string
}

// languageGraph is the language-neutral relationship projection of the same
// file records that own the symbols. It is not a second source of truth: it is
// derived and discarded with the snapshot, and it only contains facts the Go
// resolver does not own.
//
// Resolution is name-based and deliberately conservative. A name that matches
// more than one declaration is AMBIGUOUS and produces no edge. A name with no
// declaration is UNRESOLVED and produces no edge. This is why a syntax adapter
// never has to pretend it has semantic type resolution.
type languageGraph struct {
	calls       []genericCall
	references  []genericReference
	relations   []Edge
	imports     []genericImport
	importEdges []Edge
	symbols     []Match
	byName      map[string][]Match
	byQualified map[string][]Match
	unresolved  int
	ambiguous   int
}

// buildLanguageGraph resolves the retained generic syntax against every
// declaration in the snapshot. It is bounded by the same cancellation contract
// as the Go projection.
func buildLanguageGraph(ctx context.Context, records map[string]fileRecord) (*languageGraph, error) {
	graph := &languageGraph{
		byName:      map[string][]Match{},
		byQualified: map[string][]Match{},
	}
	paths := make([]string, 0, len(records))
	for rel := range records {
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	// First pass: the declaration index. Go declarations participate so a
	// TypeScript caller can target a Go symbol; only non-Go call sites are
	// stored in this projection.
	for _, rel := range paths {
		rec := records[rel]
		if !rec.parsed || rec.err != nil {
			continue
		}
		for _, symbol := range rec.symbols {
			if symbol.Kind == KindPackage || symbol.Kind == KindImport {
				continue
			}
			graph.symbols = append(graph.symbols, symbol)
			graph.byName[symbol.Name] = append(graph.byName[symbol.Name], symbol)
			if symbol.QualifiedName != "" {
				graph.byQualified[symbol.QualifiedName] = append(graph.byQualified[symbol.QualifiedName], symbol)
			}
			if symbol.Receiver != "" && symbol.Receiver+"."+symbol.Name != symbol.Name {
				qualified := symbol.Receiver + "." + symbol.Name
				graph.byName[qualified] = append(graph.byName[qualified], symbol)
			}
		}
	}
	for name, matches := range graph.byName {
		sortSymbols(matches)
		graph.byName[name] = dedupeMatches(matches)
	}
	for name, matches := range graph.byQualified {
		sortSymbols(matches)
		graph.byQualified[name] = dedupeMatches(matches)
	}

	// Second pass: resolve call sites and explicit relationships. Only non-Go
	// files contribute generic calls because Go has a stronger resolver.
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rec := records[rel]
		if !rec.parsed || rec.err != nil {
			continue
		}
		if rec.language != string(LanguageGo) {
			for _, imported := range rec.imports {
				graph.imports = append(graph.imports, genericImport{
					from: rel, language: rec.languageValue(), imported: imported,
					resolution: imported.Resolution, resolved: imported.Resolved,
				})
				if imported.Resolution == ResolutionResolved && imported.Resolved != "" {
					graph.importEdges = append(graph.importEdges, Edge{
						From: FileNodeID(rel), To: FileNodeID(imported.Resolved),
						Kind: EdgeImports, Resolution: ResolutionResolved,
						Language: rec.languageValue(), Adapter: rec.adapter,
						Source: SourceRef{Path: rel, Language: rec.languageValue(),
							Adapter: rec.adapter, StartLine: imported.Line},
					})
				}
			}
		}
		if rec.language == string(LanguageGo) {
			continue
		}
		for _, call := range rec.genericCalls {
			caller, ok := callerAt(rec.symbols, call.CallerLine)
			if !ok {
				caller, ok = callerByName(rec.symbols, call.CallerName)
			}
			if !ok {
				continue
			}
			target, resolution := graph.resolveCall(call)
			graph.calls = append(graph.calls, genericCall{
				call: call, caller: caller, callee: target, resolution: resolution,
			})
			if resolution == ResolutionUnresolved {
				graph.unresolved++
			}
			if resolution == ResolutionAmbiguous {
				graph.ambiguous++
			}
		}
		for _, ref := range rec.genericReferences {
			target, resolution := graph.resolveName(ref.Name, ref.Qualifier)
			graph.references = append(graph.references, genericReference{
				ref: ref, file: rel, language: rec.languageValue(), test: rec.test,
				tests: rec.tests, target: target, resolution: resolution,
			})
			if resolution == ResolutionUnresolved {
				graph.unresolved++
			}
			if resolution == ResolutionAmbiguous {
				graph.ambiguous++
			}
		}
		for _, relation := range rec.relations {
			target, resolution := graph.resolveName(relation.Target, "")
			if resolution != ResolutionResolved {
				graph.ambiguous++
				continue
			}
			source, ok := declarationAt(rec.symbols, relation.Line)
			if relation.Source != "" {
				source, ok = declarationByName(rec.symbols, relation.Source)
			}
			if !ok {
				continue
			}
			kind := relation.Kind
			if kind == EdgeExtends && target.Canonical == SymbolInterface {
				kind = EdgeImplements
			}
			graph.relations = append(graph.relations, Edge{
				From:       SymbolNodeID(source.Path, source.Line, source.QualifiedName),
				To:         SymbolNodeID(target.Path, target.Line, target.QualifiedName),
				Kind:       kind,
				Resolution: ResolutionResolved,
				Language:   rec.languageValue(),
				Adapter:    rec.adapter,
				Source: SourceRef{
					Path: source.Path, Package: source.Package, Symbol: source.Name,
					Language: rec.languageValue(), Adapter: rec.adapter,
					StartLine: relation.Line, StartCol: relation.Column,
				},
			})
		}
	}
	sortGenericGraph(graph)
	return graph, nil
}

// resolveCall applies the call-specific target filter and then the shared
// exact-name rule.
func (g *languageGraph) resolveCall(call rawGenericCall) (Match, Resolution) {
	matches := g.matchName(call.Name, call.Qualifier)
	if len(matches) == 0 {
		return Match{}, ResolutionUnresolved
	}
	filtered := matches[:0]
	for _, match := range matches {
		switch match.Canonical {
		case SymbolFunction, SymbolMethod, SymbolConstructor, SymbolClass,
			SymbolStruct, SymbolInterface, SymbolTrait, SymbolObject:
			filtered = append(filtered, match)
		}
	}
	if len(filtered) == 0 {
		return Match{}, ResolutionUnresolved
	}
	if len(filtered) > 1 {
		return Match{}, ResolutionAmbiguous
	}
	return filtered[0], ResolutionResolved
}

// resolveName resolves an exact name or qualified name.
func (g *languageGraph) resolveName(name, qualifier string) (Match, Resolution) {
	matches := g.matchName(name, qualifier)
	switch len(matches) {
	case 0:
		return Match{}, ResolutionUnresolved
	case 1:
		return matches[0], ResolutionResolved
	default:
		return Match{}, ResolutionAmbiguous
	}
}

func (g *languageGraph) matchName(name, qualifier string) []Match {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if qualifier != "" {
		if matches := g.byQualified[qualifier+"."+name]; len(matches) > 0 {
			return matches
		}
	}
	return g.byName[name]
}

// mergeGenericImpactLocked merges the language-neutral projection into an
// impact result. It is additive: Go reference/call edges already in the result
// are not duplicated.
func (idx *Index) mergeGenericImpactLocked(ctx context.Context, result *ImpactResult, targets []RefTarget, limit int, nodes map[string]Node) error {
	if idx.languageGraph == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	knownRef := map[string]bool{}
	for _, ref := range result.References {
		knownRef[ref.Path+":"+strconv.Itoa(ref.Line)+":"+strconv.Itoa(ref.Column)] = true
	}
	for _, target := range targets {
		for _, ref := range idx.languageGraph.referencesTo(target) {
			key := ref.Path + ":" + strconv.Itoa(ref.Line) + ":" + strconv.Itoa(ref.Column)
			if knownRef[key] {
				continue
			}
			if len(result.References) >= limit {
				result.Truncated = true
				if result.Reason == "" {
					result.Reason = "limit"
				}
				break
			}
			knownRef[key] = true
			result.References = append(result.References, ref)
			from := FileNodeID(ref.Path)
			nodes[from] = Node{ID: from, Kind: NodeFile, Name: ref.Path,
				Language: ref.Language,
				Source:   SourceRef{Path: ref.Path, StartLine: ref.Line, StartCol: ref.Column, Test: ref.Test, Language: ref.Language}}
			result.Edges = append(result.Edges, Edge{
				From: from, To: SymbolNodeID(target.Path, target.Line, target.QualifiedName),
				Kind: EdgeReferences, Resolution: ResolutionResolved,
				Language: target.Language,
				Source: SourceRef{Path: ref.Path, StartLine: ref.Line, StartCol: ref.Column,
					Test: ref.Test, Language: ref.Language},
			})
		}
		for _, edge := range idx.languageGraph.importsFor(target.Path) {
			result.Edges = append(result.Edges, edge)
			for _, id := range []string{edge.From, edge.To} {
				if node := nodeForEdgeID(id); node.ID != "" {
					nodes[node.ID] = node
				}
			}
		}
		for _, edge := range idx.languageGraph.relationsFor(target) {
			result.Edges = append(result.Edges, edge)
			if source := nodeForEdgeID(edge.From); source.ID != "" {
				nodes[source.ID] = source
			}
			if targetNode := nodeForEdgeID(edge.To); targetNode.ID != "" {
				nodes[targetNode.ID] = targetNode
			}
		}
		for _, edge := range idx.languageGraph.callsFor(target, RelationCallers) {
			if len(result.Callers) >= limit {
				result.Truncated = true
				if result.Reason == "" {
					result.Reason = "limit"
				}
				break
			}
			result.Callers = append(result.Callers, edge)
			caller := callNodeID(edge.Caller)
			callee := callNodeID(edge.Callee)
			nodes[caller] = callNodeToNode(edge.Caller)
			nodes[callee] = callNodeToNode(edge.Callee)
			result.Edges = append(result.Edges, Edge{
				From: caller, To: callee, Kind: EdgeCalls, Certainty: string(edge.Certainty),
				Resolution: ResolutionResolved, Language: edge.Caller.Language,
				Source: SourceRef{Path: edge.CallPath, StartLine: edge.CallLine, StartCol: edge.Column,
					Test: edge.Test, Language: edge.Caller.Language},
			})
		}
		for _, importer := range idx.languageGraph.importersOf(target.Path) {
			found := false
			for _, existing := range result.Importers {
				if existing == importer {
					found = true
					break
				}
			}
			if !found {
				result.Importers = append(result.Importers, importer)
			}
		}
	}
	result.Tests = append(result.Tests, idx.languageGraph.testAssociations(targets)...)
	result.Unknown = mergeUnknown(result.Unknown, idx.languageGraph.stats())
	return nil
}

// nodeForEdgeID projects an edge endpoint into a minimal canonical node when
// the query has no richer declaration for it. It never invents a coordinate:
// the endpoint string itself is the only observed identity.
func nodeForEdgeID(id string) Node {
	if id == "" {
		return Node{}
	}
	return Node{ID: id, Kind: NodeSymbol, Name: id}
}
func (g *languageGraph) referencesFor(target RefTarget) []ReferenceSite {
	if g == nil {
		return nil
	}
	out := make([]ReferenceSite, 0)
	for _, ref := range g.references {
		if ref.resolution != ResolutionResolved {
			continue
		}
		if !sameDeclarationTarget(refTarget(ref.target), target) {
			continue
		}
		out = append(out, ReferenceSite{
			Path: ref.file, Line: ref.ref.Line, Column: ref.ref.Column,
			Qualified: ref.ref.Qualifier != "", Call: false, Test: ref.test,
			Language: ref.language, targetKey: refTargetKey(target),
		})
	}
	return out
}

// referencesTo includes resolved call sites. A call is a static reference to
// its callee; the separate call edge records the caller attribution.
func (g *languageGraph) referencesTo(target RefTarget) []ReferenceSite {
	out := g.referencesFor(target)
	if g == nil {
		return out
	}
	for _, call := range g.calls {
		if call.resolution != ResolutionResolved {
			continue
		}
		if !sameDeclarationTarget(refTarget(call.callee), target) {
			continue
		}
		out = append(out, ReferenceSite{
			Path: call.caller.Path, Line: call.call.Line, Column: call.call.Column,
			Qualified: call.call.Qualifier != "", Call: true,
			Test: call.caller.Test, Language: call.caller.Language,
			targetKey: refTargetKey(target),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Column < out[j].Column
	})
	return out
}

// importsFor returns resolved import edges touching one repository file.
func (g *languageGraph) importsFor(path string) []Edge {
	if g == nil || path == "" {
		return nil
	}
	from, to := FileNodeID(path), FileNodeID(path)
	out := make([]Edge, 0)
	for _, edge := range g.importEdges {
		if edge.From == from || edge.To == to {
			out = append(out, edge)
		}
	}
	return out
}

// importersOf returns repository files whose import resolves to targetPath.
func (g *languageGraph) importersOf(targetPath string) []string {
	if g == nil || targetPath == "" {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, imported := range g.imports {
		if imported.resolution != ResolutionResolved || imported.resolved != targetPath {
			continue
		}
		if seen[imported.from] {
			continue
		}
		seen[imported.from] = true
		out = append(out, imported.from)
	}
	sort.Strings(out)
	return out
}

// callsFor returns resolved generic call edges touching target.
func (g *languageGraph) callsFor(target RefTarget, relation RefRelation) []CallEdge {
	if g == nil {
		return nil
	}
	out := make([]CallEdge, 0)
	for _, call := range g.calls {
		if call.resolution != ResolutionResolved {
			continue
		}
		matches := sameDeclarationTarget(refTarget(call.callee), target)
		if relation == RelationCallees {
			matches = sameDeclarationTarget(refTarget(call.caller), target)
		}
		if !matches {
			continue
		}
		caller := callNodeForMatch(call.caller)
		callee := callNodeForMatch(call.callee)
		out = append(out, CallEdge{
			Caller: caller, Callee: callee,
			CallPath: call.caller.Path, CallLine: call.call.Line,
			Column: call.call.Column, InClosure: call.call.InClosure,
			Test: call.caller.Test, Certainty: CertaintyLexical,
			callerKey: caller.key, calleeKey: callee.key,
		})
	}
	return out
}

// relationsFor returns explicit extends/implements edges touching target.
func (g *languageGraph) relationsFor(target RefTarget) []Edge {
	if g == nil {
		return nil
	}
	targetID := SymbolNodeID(target.Path, target.Line, target.QualifiedName)
	out := make([]Edge, 0)
	for _, edge := range g.relations {
		if edge.From == targetID || edge.To == targetID {
			out = append(out, edge)
		}
	}
	return out
}

// testAssociations returns static generic call/reference associations from
// test files to the supplied targets. It is not coverage.
func (g *languageGraph) testAssociations(targets []RefTarget) []TestAssociation {
	if g == nil {
		return nil
	}
	byTarget := map[string]RefTarget{}
	for _, target := range targets {
		byTarget[declarationKey(target)] = target
	}
	out := make([]TestAssociation, 0)
	for _, call := range g.calls {
		if call.resolution != ResolutionResolved || !call.caller.Test {
			continue
		}
		if _, ok := byTarget[declarationKey(matchTarget(call.callee))]; !ok {
			continue
		}
		test := TestDecl{Symbol: call.caller, Kind: TestUnit}
		out = append(out, TestAssociation{
			Test: testInfo(test), Relation: "calls", Path: call.caller.Path,
			Line: call.call.Line, Column: call.call.Column, Call: true,
		})
	}
	for _, ref := range g.references {
		if ref.resolution != ResolutionResolved || !ref.test {
			continue
		}
		if _, ok := byTarget[declarationKey(matchTarget(ref.target))]; !ok {
			continue
		}
		test, ok := testAtLine(ref.tests, ref.ref.Line)
		if !ok {
			continue
		}
		out = append(out, TestAssociation{
			Test: testInfo(test), Relation: "references", Path: ref.file,
			Line: ref.ref.Line, Column: ref.ref.Column,
		})
	}
	return out
}

// stats reports generic blind spots for a query disclosure.
func (g *languageGraph) stats() UnknownSummary {
	if g == nil {
		return UnknownSummary{}
	}
	return UnknownSummary{
		GenericUnresolved: g.unresolved,
		GenericAmbiguous:  g.ambiguous,
	}
}

func callerAt(symbols []Match, line int) (Match, bool) {
	for _, symbol := range symbols {
		if line < symbol.Line || line > symbol.EndLine {
			continue
		}
		if symbol.Canonical == SymbolFunction || symbol.Canonical == SymbolMethod ||
			symbol.Canonical == SymbolConstructor {
			return symbol, true
		}
	}
	return Match{}, false
}

// declarationAt selects the most specific declaration that starts on or
// contains line. A relation clause is inside the class header, so both the
// class and a same-line member can be candidates; the narrowest wins.
func declarationAt(symbols []Match, line int) (Match, bool) {
	best := Match{}
	found := false
	for _, symbol := range symbols {
		if symbol.Kind == KindPackage || symbol.Kind == KindImport {
			continue
		}
		if line < symbol.Line || line > symbol.EndLine {
			continue
		}
		if !found || spanOf(symbol) < spanOf(best) {
			best, found = symbol, true
		}
	}
	return best, found
}

func spanOf(symbol Match) int {
	return symbol.EndLine - symbol.Line
}

func callerByName(symbols []Match, name string) (Match, bool) {
	for _, symbol := range symbols {
		if symbol.Name == name || symbol.QualifiedName == name {
			return symbol, true
		}
	}
	return Match{}, false
}

func declarationByName(symbols []Match, name string) (Match, bool) {
	for _, symbol := range symbols {
		if symbol.Name == name || symbol.QualifiedName == name ||
			(symbol.Container != "" && symbol.Container+"."+symbol.Name == name) {
			return symbol, true
		}
	}
	return Match{}, false
}

func matchTarget(match Match) RefTarget {
	return refTarget(match)
}

func sameDeclarationMatch(a, b Match) bool {
	return a.Path == b.Path && a.Line == b.Line && a.QualifiedName == b.QualifiedName
}

func sameDeclarationTarget(a, b RefTarget) bool {
	return a.Path == b.Path && a.Line == b.Line && a.QualifiedName == b.QualifiedName
}

func declarationKey(target RefTarget) string {
	return target.Path + "\x00" + strconv.Itoa(target.Line) + "\x00" + target.QualifiedName
}

func dedupeMatches(matches []Match) []Match {
	if len(matches) < 2 {
		return matches
	}
	out := matches[:1]
	for _, match := range matches[1:] {
		if sameDeclarationMatch(out[len(out)-1], match) {
			continue
		}
		out = append(out, match)
	}
	return out
}

func sortGenericGraph(graph *languageGraph) {
	sort.SliceStable(graph.calls, func(i, j int) bool {
		if graph.calls[i].caller.Path != graph.calls[j].caller.Path {
			return graph.calls[i].caller.Path < graph.calls[j].caller.Path
		}
		if graph.calls[i].call.Line != graph.calls[j].call.Line {
			return graph.calls[i].call.Line < graph.calls[j].call.Line
		}
		return graph.calls[i].call.Name < graph.calls[j].call.Name
	})
	sort.SliceStable(graph.references, func(i, j int) bool {
		if graph.references[i].file != graph.references[j].file {
			return graph.references[i].file < graph.references[j].file
		}
		return graph.references[i].ref.Line < graph.references[j].ref.Line
	})
	sort.SliceStable(graph.relations, func(i, j int) bool {
		if graph.relations[i].From != graph.relations[j].From {
			return graph.relations[i].From < graph.relations[j].From
		}
		return graph.relations[i].To < graph.relations[j].To
	})
	sort.SliceStable(graph.imports, func(i, j int) bool {
		if graph.imports[i].from != graph.imports[j].from {
			return graph.imports[i].from < graph.imports[j].from
		}
		return graph.imports[i].imported.Line < graph.imports[j].imported.Line
	})
	sort.SliceStable(graph.importEdges, func(i, j int) bool {
		if graph.importEdges[i].From != graph.importEdges[j].From {
			return graph.importEdges[i].From < graph.importEdges[j].From
		}
		return graph.importEdges[i].To < graph.importEdges[j].To
	})
}

// resolveRecordImports maps every non-Go import to a repository file when the
// convention is unambiguous. Go imports stay with the module-aware package
// graph. Unresolved external modules remain import facts but produce no edge.
func resolveRecordImports(records map[string]fileRecord) {
	for rel, rec := range records {
		if !rec.parsed || rec.err != nil || rec.language == string(LanguageGo) {
			continue
		}
		language := rec.languageValue()
		changed := false
		for i := range rec.imports {
			target, resolution := resolveImportTarget(rel, language, rec.imports[i], records)
			rec.imports[i].Resolution = resolution
			rec.imports[i].Resolved = target
			changed = true
		}
		if changed {
			records[rel] = rec
		}
	}
}
func resolveImportTarget(from string, language Language, imported Import, files map[string]fileRecord) (string, Resolution) {
	spec := strings.TrimSpace(imported.Path)
	if spec == "" {
		return "", ResolutionUnresolved
	}
	switch language {
	case LanguageTypeScript, LanguageTSX, LanguageJavaScript, LanguageJSX:
		if !strings.HasPrefix(spec, ".") {
			return "", ResolutionUnresolved
		}
		return resolveRelativeSource(from, spec, []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}, files)
	case LanguageJava:
		return resolvePackageFile(from, spec, ".java", files)
	case LanguageKotlin:
		return resolvePackageFile(from, spec, ".kt", files)
	case LanguageCSharp:
		return resolveNamespaceFile(spec, ".cs", files)
	case LanguageRust:
		return resolveRustImport(from, spec, files)
	case LanguageRuby:
		if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
			return resolveRelativeSource(from, spec, []string{".rb"}, files)
		}
		return resolveRubyRequire(spec, files)
	case LanguageC, LanguageCpp:
		if imported.Kind == "include" && strings.HasPrefix(imported.Path, "\"") {
			spec = strings.Trim(imported.Path, "\"")
			return resolveRelativeSource(from, spec, []string{"", ".h", ".hpp"}, files)
		}
		return resolveRelativeSource(from, spec, []string{"", ".h", ".hpp"}, files)
	default:
		return "", ResolutionUnsupported
	}
}

func resolveRelativeSource(from, spec string, extensions []string, files map[string]fileRecord) (string, Resolution) {
	base := path.Clean(path.Join(path.Dir(from), spec))
	candidates := []string{base}
	for _, ext := range extensions {
		if ext != "" {
			candidates = append(candidates, base+ext)
		}
	}
	for _, ext := range extensions {
		if ext != "" {
			candidates = append(candidates, path.Join(base, "index"+ext))
		}
	}
	for _, candidate := range candidates {
		candidate = strings.TrimPrefix(candidate, "./")
		if _, ok := files[candidate]; ok {
			return candidate, ResolutionResolved
		}
	}
	return "", ResolutionUnresolved
}

func resolvePackageFile(_ string, spec, ext string, files map[string]fileRecord) (string, Resolution) {
	candidate := strings.ReplaceAll(spec, ".", "/") + ext
	if _, ok := files[candidate]; ok {
		return candidate, ResolutionResolved
	}
	// Imports may name a nested type (com.example.Outer.Inner); try prefixes
	// from longest to shortest without inventing a target.
	parts := strings.Split(spec, ".")
	for i := len(parts); i > 0; i-- {
		candidate := strings.ReplaceAll(strings.Join(parts[:i], "."), ".", "/") + ext
		if _, ok := files[candidate]; ok {
			return candidate, ResolutionResolved
		}
	}
	return "", ResolutionUnresolved
}

func resolveNamespaceFile(spec, ext string, files map[string]fileRecord) (string, Resolution) {
	candidate := strings.ReplaceAll(spec, ".", "/") + ext
	if _, ok := files[candidate]; ok {
		return candidate, ResolutionResolved
	}
	suffix := strings.ReplaceAll(spec, ".", "/") + ext
	for rel := range files {
		if strings.HasSuffix(rel, suffix) {
			return rel, ResolutionResolved
		}
	}
	return "", ResolutionUnresolved
}

func resolveRustImport(from, spec string, files map[string]fileRecord) (string, Resolution) {
	spec = strings.TrimSpace(spec)
	spec = strings.TrimPrefix(spec, "pub ")
	spec = strings.TrimPrefix(spec, "use ")
	if idx := strings.Index(spec, " as "); idx >= 0 {
		spec = spec[:idx]
	}
	spec = strings.TrimSuffix(spec, ";")
	parts := strings.Split(spec, "::")
	if len(parts) == 0 {
		return "", ResolutionUnresolved
	}
	var base string
	switch parts[0] {
	case "crate":
		base = "src"
		parts = parts[1:]
	case "self", "super":
		base = path.Dir(from)
		parts = parts[1:]
	default:
		base = "src"
	}
	for _, part := range parts {
		if part == "" || part == "*" || part == "{" || part == "}" {
			break
		}
		candidate := path.Join(base, part+".rs")
		if _, ok := files[candidate]; ok {
			return candidate, ResolutionResolved
		}
		candidate = path.Join(base, part, "mod.rs")
		if _, ok := files[candidate]; ok {
			return candidate, ResolutionResolved
		}
		base = path.Join(base, part)
	}
	return "", ResolutionUnresolved
}

func resolveRubyRequire(spec string, files map[string]fileRecord) (string, Resolution) {
	candidate := spec + ".rb"
	if _, ok := files[candidate]; ok {
		return candidate, ResolutionResolved
	}
	for rel := range files {
		if strings.HasSuffix(rel, "/"+candidate) {
			return rel, ResolutionResolved
		}
	}
	return "", ResolutionUnresolved
}

// languageValue projects a fileRecord language into the canonical identity.
func (r fileRecord) languageValue() Language {
	if r.language == "" {
		return LanguageGo
	}
	return Language(r.language)
}
