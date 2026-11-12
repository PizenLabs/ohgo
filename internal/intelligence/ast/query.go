package ast

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// ErrImpactSelector reports an impact query with neither a symbol nor a path.
var ErrImpactSelector = errors.New("ast: impact requires a symbol or path")

// Files answers a bounded structural file inventory query.
func (idx *Index) Files(ctx context.Context, query FileQuery) (FileResult, error) {
	if err := ctx.Err(); err != nil {
		return FileResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return FileResult{}, err
	}
	limit, skip := normalizeInventoryBounds(query.Limit, query.Skip)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	result := FileResult{Snapshot: idx.infoLocked(), Reason: ""}
	scope, scopeFile, err := idx.fileScopeLocked(query.Path)
	if err != nil {
		return FileResult{}, err
	}
	for rel, rec := range idx.files {
		if scope != "" {
			if scopeFile {
				if rel != scope {
					continue
				}
			} else if !pathInside(scope, rel) {
				continue
			}
		}
		if query.Package != "" && rec.packageName != query.Package {
			continue
		}
		if query.Kind != "" && rec.kind != query.Kind {
			continue
		}
		if query.Language != "" && rec.language != query.Language {
			continue
		}
		if query.Test != nil && rec.test != *query.Test {
			continue
		}
		result.Files = append(result.Files, idx.fileInfoLocked(rel, rec))
	}
	sort.SliceStable(result.Files, func(i, j int) bool {
		if result.Files[i].Path != result.Files[j].Path {
			return result.Files[i].Path < result.Files[j].Path
		}
		return result.Files[i].Kind < result.Files[j].Kind
	})
	result.Total = len(result.Files)
	result.Files, result.Truncated, result.Reason = pageFiles(result.Files, limit, skip)
	return result, nil
}

// Packages answers a bounded package inventory query.
func (idx *Index) Packages(ctx context.Context, query PackageQuery) (PackageResult, error) {
	if err := ctx.Err(); err != nil {
		return PackageResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return PackageResult{}, err
	}
	limit, skip := normalizeInventoryBounds(query.Limit, query.Skip)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	result := PackageResult{Snapshot: idx.infoLocked()}
	if idx.graph == nil {
		return result, nil
	}
	scope, scopeFile, err := idx.fileScopeLocked(query.Path)
	if err != nil {
		return PackageResult{}, err
	}
	if scopeFile {
		scope = packageDir(scope)
	}
	for _, dir := range idx.graph.order {
		node := idx.graph.nodes[dir]
		if scope != "" && !pathInside(scope, dir) {
			continue
		}
		if query.Name != "" && node.name != query.Name {
			continue
		}
		result.Packages = append(result.Packages, idx.packageInfoLocked(node))
	}
	sort.SliceStable(result.Packages, func(i, j int) bool {
		return result.Packages[i].Dir < result.Packages[j].Dir
	})
	result.Total = len(result.Packages)
	result.Packages, result.Truncated, result.Reason = pagePackages(result.Packages, limit, skip)
	return result, nil
}

// Overview returns one bounded project summary from one snapshot.
//
// It exists so the inventory tool does not compose five independent queries
// that could each observe a different generation. The package and file slices
// are bounded; entrypoints and configs share the same snapshot and their own
// hard ceiling.
func (idx *Index) Overview(ctx context.Context, limit int) (OverviewResult, error) {
	if err := ctx.Err(); err != nil {
		return OverviewResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return OverviewResult{}, err
	}
	limit, _ = normalizeInventoryBounds(limit, 0)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	result := OverviewResult{Snapshot: idx.infoLocked()}
	if idx.graph != nil {
		for _, dir := range idx.graph.order {
			if len(result.Packages) >= limit {
				result.Truncated = true
				result.Reason = "package_limit"
				break
			}
			result.Packages = append(result.Packages, idx.packageInfoLocked(idx.graph.nodes[dir]))
		}
	}
	files := make([]FileInfo, 0, len(idx.files))
	for rel, rec := range idx.files {
		files = append(files, idx.fileInfoLocked(rel, rec))
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files) > limit {
		files = files[:limit]
		result.Truncated = true
		if result.Reason == "" {
			result.Reason = "file_limit"
		}
	}
	result.Files = files
	for rel, rec := range idx.files {
		for _, entry := range rec.entrypoints {
			result.Entrypoints = append(result.Entrypoints, Entrypoint{
				Path: rel, Package: entry.Package, Symbol: entry.Name,
				QualifiedName: entry.QualifiedName, Line: entry.Line,
				EndLine: entry.EndLine, Column: entry.Column,
			})
		}
	}
	sort.SliceStable(result.Entrypoints, func(i, j int) bool {
		if result.Entrypoints[i].Path != result.Entrypoints[j].Path {
			return result.Entrypoints[i].Path < result.Entrypoints[j].Path
		}
		return result.Entrypoints[i].Line < result.Entrypoints[j].Line
	})
	if len(result.Entrypoints) > StructureMaxLimit {
		result.Entrypoints = result.Entrypoints[:StructureMaxLimit]
		result.Truncated = true
		if result.Reason == "" {
			result.Reason = "entrypoint_limit"
		}
	}
	result.Configs = configFacts(idx.files, idx.modulePath)
	if len(result.Configs) > StructureMaxLimit {
		result.Configs = result.Configs[:StructureMaxLimit]
		result.Truncated = true
		if result.Reason == "" {
			result.Reason = "config_limit"
		}
	}
	return result, nil
}

// fileInfoLocked projects one record into the inventory shape.
func (idx *Index) fileInfoLocked(rel string, rec fileRecord) FileInfo {
	return FileInfo{
		Path: rel, Dir: dirOf(rel), Package: rec.packageName,
		Language: rec.language, Kind: rec.kind, Test: rec.test,
		Size: rec.size, Symbols: len(rec.symbols),
	}
}

// packageInfoLocked projects one graph node into the inventory shape.
func (idx *Index) packageInfoLocked(node *graphNode) PackageInfo {
	return PackageInfo{
		Dir: node.path, Name: node.name,
		Packages: append([]string(nil), node.packages...),
		Files:    node.files, TestFiles: node.testFiles, Symbols: node.symbols,
		Imports: len(node.imports), Importers: node.importers, Ambiguous: node.ambiguous,
	}
}

// Entrypoints returns every proven non-test Go entrypoint. It is bounded by
// the inventory ceiling; a repository with more entrypoints than that is
// reported as truncated rather than silently cut.
func (idx *Index) Entrypoints(ctx context.Context) (EntrypointResult, error) {
	if err := ctx.Err(); err != nil {
		return EntrypointResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return EntrypointResult{}, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	result := EntrypointResult{Snapshot: idx.infoLocked()}
	for rel, rec := range idx.files {
		for _, entry := range rec.entrypoints {
			result.Entrypoints = append(result.Entrypoints, Entrypoint{
				Path:          rel,
				Package:       entry.Package,
				Symbol:        entry.Name,
				QualifiedName: entry.QualifiedName,
				Line:          entry.Line,
				EndLine:       entry.EndLine,
				Column:        entry.Column,
			})
		}
	}
	sort.SliceStable(result.Entrypoints, func(i, j int) bool {
		if result.Entrypoints[i].Path != result.Entrypoints[j].Path {
			return result.Entrypoints[i].Path < result.Entrypoints[j].Path
		}
		return result.Entrypoints[i].Line < result.Entrypoints[j].Line
	})
	if len(result.Entrypoints) > StructureMaxLimit {
		result.Entrypoints = result.Entrypoints[:StructureMaxLimit]
		result.Truncated = true
		result.Reason = "limit"
	}
	return result, nil
}

// Configs returns the allowlisted configuration inventory.
func (idx *Index) Configs(ctx context.Context) (ConfigResult, error) {
	if err := ctx.Err(); err != nil {
		return ConfigResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return ConfigResult{}, err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	result := ConfigResult{
		Snapshot: idx.infoLocked(),
		Configs:  configFacts(idx.files, idx.modulePath),
	}
	if len(result.Configs) > StructureMaxLimit {
		result.Configs = result.Configs[:StructureMaxLimit]
		result.Truncated = true
		result.Reason = "limit"
	}
	return result, nil
}

// Tests answers either the test declaration inventory or the static test
// associations for one symbol.
//
// An association is a reference or direct call from a test function. It is not
// coverage and must never be presented as proof that the symbol's behaviour is
// exercised at runtime.
func (idx *Index) Tests(ctx context.Context, query TestQuery) (TestResult, error) {
	if err := ctx.Err(); err != nil {
		return TestResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return TestResult{}, err
	}
	limit, skip := normalizeInventoryBounds(query.Limit, query.Skip)

	idx.mu.Lock()
	defer idx.mu.Unlock()

	result := TestResult{Snapshot: idx.infoLocked(), Symbol: query.Symbol}
	scope, scopeFile, err := idx.fileScopeLocked(query.Path)
	if err != nil {
		return TestResult{}, err
	}

	if query.Symbol == "" {
		for rel, rec := range idx.files {
			if scope != "" && !fileScopeContains(scope, scopeFile, rel) {
				continue
			}
			for _, test := range rec.tests {
				if query.Package != "" && test.Symbol.Package != query.Package {
					continue
				}
				result.Tests = append(result.Tests, testInfo(test))
			}
		}
		sortTests(result.Tests)
		result.Total = len(result.Tests)
		result.Tests, result.Truncated, result.Reason = pageTests(result.Tests, limit, skip)
		return result, nil
	}

	target, ambiguous, candidates, found, err := idx.resolveImpactTargetLocked(ctx, query.Symbol, scope, scopeFile)
	if err != nil {
		return TestResult{}, err
	}
	if found && query.Package != "" && target.Package != query.Package {
		found = false
	}
	result.Found = found
	result.Ambiguous = ambiguous
	result.Candidates = candidates
	if !found {
		return result, nil
	}

	associations, limited, err := idx.testAssociationsLocked(ctx, []RefTarget{target})
	if err != nil {
		return TestResult{}, err
	}
	if limited {
		result.Truncated, result.Reason = true, "work_limit"
	}
	sort.SliceStable(associations, func(i, j int) bool {
		if associations[i].Test.Path != associations[j].Test.Path {
			return associations[i].Test.Path < associations[j].Test.Path
		}
		if associations[i].Test.Line != associations[j].Test.Line {
			return associations[i].Test.Line < associations[j].Test.Line
		}
		if associations[i].Line != associations[j].Line {
			return associations[i].Line < associations[j].Line
		}
		return associations[i].Relation < associations[j].Relation
	})
	result.Total = len(associations)
	paged, pageTruncated, pageReason := pageAssociations(associations, limit, skip)
	result.Associations = paged
	if pageTruncated {
		result.Truncated, result.Reason = true, pageReason
	}
	result.Tests = uniqueTests(result.Associations)
	return result, nil
}

// Impact composes static references, callers, package importers, and test
// associations for one symbol or path.
//
// It reports direct static reachability only. Dynamic dispatch, reflection,
// and code outside the indexed root stay in Unknown; the result is advisory
// evidence, never a behavioural claim.
func (idx *Index) Impact(ctx context.Context, query ImpactQuery) (ImpactResult, error) {
	if err := ctx.Err(); err != nil {
		return ImpactResult{}, err
	}
	if err := idx.ensureFreshGraph(ctx); err != nil {
		return ImpactResult{}, err
	}
	limit, _ := normalizeInventoryBounds(query.Limit, 0)
	depth := query.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > RefMaxDepth {
		depth = RefMaxDepth
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	result := ImpactResult{
		Snapshot:  idx.infoLocked(),
		QueryType: "symbol",
		Selector:  query.Symbol,
	}
	scope, scopeFile, err := idx.fileScopeLocked(query.Path)
	if err != nil {
		return ImpactResult{}, err
	}

	var targets []RefTarget
	if query.Symbol != "" {
		target, ambiguous, candidates, found, err := idx.resolveImpactTargetLocked(ctx, query.Symbol, scope, scopeFile)
		if err != nil {
			return ImpactResult{}, err
		}
		result.Found = found
		result.Ambiguous = ambiguous
		result.Candidates = candidates
		if !found {
			return result, nil
		}
		targets = []RefTarget{target}
	} else if query.Path != "" {
		result.QueryType = "path"
		result.Selector = scope
		if scope == "" {
			scope = "."
		}
		targets, result.Truncated, result.Reason = idx.pathTargetsLocked(scope, scopeFile, limit)
		result.Found = len(targets) > 0
		if !result.Found {
			return result, nil
		}
	} else {
		return ImpactResult{}, ErrImpactSelector
	}
	result.Targets = targets

	refKeys := map[string]bool{}
	callKeys := map[string]bool{}
	nodes := map[string]Node{}
	for _, target := range targets {
		refKeys[refTargetKey(target)] = true
		callKeys[callKey(target.Path, target.Line, target.Symbol)] = true
		node := targetNode(target)
		nodes[node.ID] = node
	}
	result.Nodes = append(result.Nodes, sortedNodes(nodes)...)

	references, truncated, reason, err := idx.impactReferencesLocked(ctx, refKeys, limit)
	if err != nil {
		return ImpactResult{}, err
	}
	if truncated {
		result.Truncated, result.Reason = true, reason
	}
	result.References = references
	for _, ref := range references {
		from := FileNodeID(ref.Path)
		nodes[from] = Node{
			ID:     from,
			Kind:   NodeFile,
			Name:   ref.Path,
			Source: SourceRef{Path: ref.Path, StartLine: ref.Line, StartCol: ref.Column, Test: ref.Test},
		}
		to := targetIDForRefLocked(ref, targets)
		if to == "" {
			continue
		}
		result.Edges = append(result.Edges, Edge{
			From: from, To: to, Kind: EdgeReferences,
			Source: SourceRef{Path: ref.Path, StartLine: ref.Line, StartCol: ref.Column, Test: ref.Test},
		})
	}

	callers, truncated, reason, err := idx.impactCallersLocked(ctx, callKeys, depth, limit)
	if err != nil {
		return ImpactResult{}, err
	}
	if truncated {
		result.Truncated, result.Reason = true, reason
	}
	result.Callers = callers
	for _, edge := range callers {
		caller := callNodeID(edge.Caller)
		callee := callNodeID(edge.Callee)
		nodes[caller] = callNodeToNode(edge.Caller)
		nodes[callee] = callNodeToNode(edge.Callee)
		result.Edges = append(result.Edges, Edge{
			From: caller, To: callee, Kind: EdgeCalls, Certainty: string(edge.Certainty),
			Source: SourceRef{Path: edge.CallPath, StartLine: edge.CallLine, StartCol: edge.Column, Test: edge.Test},
		})
	}

	result.Importers = idx.packageImportersLocked(targets)
	for _, dir := range result.Importers {
		from := DirectoryNodeID(dir)
		nodes[from] = Node{ID: from, Kind: NodeDirectory, Name: dir, Source: SourceRef{Path: dir}}
	}
	tests, testsLimited, err := idx.testAssociationsLocked(ctx, targets)
	if err != nil {
		return ImpactResult{}, err
	}
	if testsLimited {
		result.Truncated, result.Reason = true, "work_limit"
	}
	result.Tests = tests
	sort.SliceStable(result.Tests, func(i, j int) bool {
		if result.Tests[i].Test.Path != result.Tests[j].Test.Path {
			return result.Tests[i].Test.Path < result.Tests[j].Test.Path
		}
		if result.Tests[i].Test.Line != result.Tests[j].Test.Line {
			return result.Tests[i].Test.Line < result.Tests[j].Test.Line
		}
		return result.Tests[i].Line < result.Tests[j].Line
	})
	if len(result.Tests) > limit {
		result.Tests = result.Tests[:limit]
		result.Truncated, result.Reason = true, "limit"
	}

	result.Total = len(result.References) + len(result.Callers) + len(result.Importers) + len(result.Tests)
	result.Nodes = sortedNodes(nodes)
	result.Unknown = idx.impactUnknownLocked(targets)
	if result.Truncated && result.Reason == "" {
		result.Reason = "limit"
	}
	return result, nil
}

// fileScopeLocked normalizes an optional inventory path and reports whether it
// names one indexed file.
func (idx *Index) fileScopeLocked(pathArg string) (string, bool, error) {
	if strings.TrimSpace(pathArg) == "" {
		return "", false, nil
	}
	rel, err := normalizeIndexRel(idx.Root, pathArg)
	if err != nil {
		return "", false, err
	}
	if _, ok := idx.files[rel]; ok {
		return rel, true, nil
	}
	return rel, false, nil
}

// resolveImpactTargetLocked resolves one exact symbol, preserving ambiguity.
func (idx *Index) resolveImpactTargetLocked(ctx context.Context, symbol, scope string, scopeFile bool) (RefTarget, bool, []RefTarget, bool, error) {
	var matches []Match
	work := 0
	for rel, rec := range idx.files {
		work++
		if work%refCancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return RefTarget{}, false, nil, false, err
			}
		}
		if scope != "" && !fileScopeContains(scope, scopeFile, rel) {
			continue
		}
		for _, sym := range rec.symbols {
			if sym.Kind == KindPackage || sym.Kind == KindImport {
				continue
			}
			if sym.Name != symbol && sym.QualifiedName != symbol {
				continue
			}
			matches = append(matches, sym)
		}
	}
	sortSymbols(matches)
	if len(matches) == 0 {
		return RefTarget{}, false, nil, false, nil
	}
	if len(matches) > 1 {
		candidates := make([]RefTarget, 0, len(matches))
		for _, match := range matches {
			candidates = append(candidates, refTarget(match))
		}
		return RefTarget{}, true, candidates, false, nil
	}
	return refTarget(matches[0]), false, nil, true, nil
}

// pathTargetsLocked projects a file or directory scope into declaration
// targets. A path impact over a very large scope is truncated explicitly.
func (idx *Index) pathTargetsLocked(scope string, scopeFile bool, limit int) ([]RefTarget, bool, string) {
	var targets []RefTarget
	for rel, rec := range idx.files {
		if !fileScopeContains(scope, scopeFile, rel) {
			continue
		}
		for _, sym := range rec.symbols {
			if sym.Kind == KindPackage || sym.Kind == KindImport {
				continue
			}
			targets = append(targets, refTarget(sym))
			if len(targets) >= limit {
				return targets, true, "target_limit"
			}
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Path != targets[j].Path {
			return targets[i].Path < targets[j].Path
		}
		return targets[i].Line < targets[j].Line
	})
	return targets, false, ""
}

// impactReferencesLocked scans the resolved reference projection once.
func (idx *Index) impactReferencesLocked(ctx context.Context, keys map[string]bool, limit int) ([]ReferenceSite, bool, string, error) {
	var out []ReferenceSite
	if idx.symbolGraph == nil {
		return out, false, "", nil
	}
	work := 0
	for _, ref := range idx.symbolGraph.refs {
		work++
		if work%refCancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, false, "", err
			}
		}
		if work > RefWorkLimit {
			return out, true, "work_limit", nil
		}
		if !keys[ref.targetKey] {
			continue
		}
		if len(out) >= limit {
			return out, true, "limit", nil
		}
		out = append(out, ReferenceSite{
			Path: ref.Path, Line: ref.Line, Column: ref.Column,
			Qualified: ref.Qualified, Call: ref.Call, Test: ref.Test,
			targetKey: ref.targetKey,
		})
	}
	return out, false, "", nil
}

// impactCallersLocked walks incoming call edges from the target declarations
// up to depth. It is cycle-safe through a seen set and bounded by limit/work.
func (idx *Index) impactCallersLocked(ctx context.Context, keys map[string]bool, depth, limit int) ([]CallEdge, bool, string, error) {
	var out []CallEdge
	if idx.symbolGraph == nil || depth <= 0 {
		return out, false, "", nil
	}
	incoming := map[string][]CallEdge{}
	for _, edge := range idx.symbolGraph.calls {
		incoming[edge.calleeKey] = append(incoming[edge.calleeKey], edge)
	}
	frontier := make([]string, 0, len(keys))
	for key := range keys {
		frontier = append(frontier, key)
	}
	sort.Strings(frontier)
	seenEdge := map[string]bool{}
	work := 0
	for level := 0; level < depth; level++ {
		next := map[string]bool{}
		for _, key := range frontier {
			for _, edge := range incoming[key] {
				work++
				if work%refCancelCheckInterval == 0 {
					if err := ctx.Err(); err != nil {
						return nil, false, "", err
					}
				}
				if work > RefWorkLimit {
					return out, true, "work_limit", nil
				}
				edgeID := edge.Caller.Path + "\x00" + itoa(edge.Caller.Line) + "\x00" +
					edge.Callee.Path + "\x00" + itoa(edge.Callee.Line)
				if seenEdge[edgeID] {
					continue
				}
				seenEdge[edgeID] = true
				if len(out) >= limit {
					return out, true, "limit", nil
				}
				out = append(out, publicCallEdge(edge))
				next[edge.callerKey] = true
			}
		}
		if len(next) == 0 {
			break
		}
		frontier = frontier[:0]
		for key := range next {
			frontier = append(frontier, key)
		}
		sort.Strings(frontier)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Caller.Path != out[j].Caller.Path {
			return out[i].Caller.Path < out[j].Caller.Path
		}
		if out[i].Caller.Line != out[j].Caller.Line {
			return out[i].Caller.Line < out[j].Caller.Line
		}
		return out[i].CallLine < out[j].CallLine
	})
	return out, false, "", nil
}

// packageImportersLocked lists module-local packages importing a target's
// package.
func (idx *Index) packageImportersLocked(targets []RefTarget) []string {
	if idx.graph == nil {
		return nil
	}
	wanted := map[string]bool{}
	for _, target := range targets {
		wanted[packageDir(target.Path)] = true
	}
	set := map[string]bool{}
	for from, node := range idx.graph.nodes {
		for to := range node.imports {
			if wanted[to] && from != to {
				set[from] = true
			}
		}
	}
	return sortedKeys(set)
}

// testAssociationsLocked maps references and calls in test files to the
// enclosing test declaration. A reference from a test helper is not attributed
// to a test: the static association would be false.
func (idx *Index) testAssociationsLocked(ctx context.Context, targets []RefTarget) ([]TestAssociation, bool, error) {
	if idx.symbolGraph == nil {
		return nil, false, nil
	}
	refKeys := map[string]bool{}
	callKeys := map[string]bool{}
	for _, target := range targets {
		refKeys[refTargetKey(target)] = true
		callKeys[callKey(target.Path, target.Line, target.Symbol)] = true
	}

	var out []TestAssociation
	seen := map[string]bool{}
	appendSite := func(rel string, line, column int, relation string, call bool) {
		rec, ok := idx.files[rel]
		if !ok || !rec.test {
			return
		}
		test, ok := testAtLine(rec.tests, line)
		if !ok {
			return
		}
		key := rel + "\x00" + itoa(line) + "\x00" + relation + "\x00" + test.Symbol.QualifiedName
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, TestAssociation{
			Test: testInfo(test), Relation: relation,
			Path: rel, Line: line, Column: column, Call: call,
		})
	}
	work := 0
	for _, ref := range idx.symbolGraph.refs {
		work++
		if work%refCancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
		}
		if work > RefWorkLimit {
			return out, true, nil
		}
		if !refKeys[ref.targetKey] || !ref.Test {
			continue
		}
		relation := "references"
		if ref.Call {
			relation = "calls"
		}
		appendSite(ref.Path, ref.Line, ref.Column, relation, ref.Call)
	}
	for _, edge := range idx.symbolGraph.calls {
		work++
		if work%refCancelCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
		}
		if work > RefWorkLimit {
			return out, true, nil
		}
		if !callKeys[edge.calleeKey] || !edge.Test {
			continue
		}
		appendSite(edge.CallPath, edge.CallLine, edge.Column, "calls", true)
	}
	return out, false, nil
}

// impactUnknownLocked discloses the blind spots that apply to the projection.
func (idx *Index) impactUnknownLocked(targets []RefTarget) UnknownSummary {
	if idx.symbolGraph == nil {
		return UnknownSummary{}
	}
	if len(targets) == 1 {
		unknown := idx.referenceUnknownLocked(targets[0])
		unknown.DynamicCalls = idx.symbolGraph.unknownTotal
		unknown.Reasons = copyReasons(idx.symbolGraph.unknownReasons)
		return unknown
	}
	dotFiles := map[string]bool{}
	for _, files := range idx.symbolGraph.dotAffected {
		for rel := range files {
			dotFiles[rel] = true
		}
	}
	return UnknownSummary{
		DotImportFiles: len(dotFiles),
		AmbiguousUses:  idx.symbolGraph.ambiguousUses,
		UnresolvedUses: idx.symbolGraph.unresolvedUses,
		DynamicCalls:   idx.symbolGraph.unknownTotal,
		Reasons:        copyReasons(idx.symbolGraph.unknownReasons),
	}
}

// targetNode projects one declaration into the canonical node vocabulary.
func targetNode(target RefTarget) Node {
	return Node{
		ID:   SymbolNodeID(target.Path, target.Line, target.QualifiedName),
		Kind: NodeSymbol,
		Name: target.Symbol,
		Source: SourceRef{
			Path: target.Path, Package: target.Package, Symbol: target.Symbol,
			StartLine: target.Line, EndLine: target.EndLine,
		},
	}
}

// targetIDForRefLocked finds the target identity for a returned reference.
// The reference projection already resolved the target, so this is a lookup
// over the small target slice, never a second resolution pass.
func targetIDForRefLocked(ref ReferenceSite, targets []RefTarget) string {
	for _, target := range targets {
		if ref.targetKey == refTargetKey(target) {
			return SymbolNodeID(target.Path, target.Line, target.QualifiedName)
		}
	}
	return ""
}

// callNodeID returns the canonical identity of a call node.
func callNodeID(node CallNode) string {
	if node.Kind == "" && node.Symbol == "<package init>" {
		return "sym:" + node.Path + ":" + itoa(node.Line) + ":" + node.Symbol
	}
	return SymbolNodeID(node.Path, node.Line, node.QualifiedName())
}

// QualifiedName renders the declaration identity of a call node.
func (node CallNode) QualifiedName() string {
	if node.Package != "" && node.Symbol != "" {
		return node.Package + "." + node.Symbol
	}
	return node.Symbol
}

// callNodeToNode projects a call-graph node into the canonical vocabulary.
func callNodeToNode(node CallNode) Node {
	kind := NodeSymbol
	if node.Kind == "" && node.Symbol == "<package init>" {
		kind = NodeFile
	}
	return Node{
		ID:   callNodeID(node),
		Kind: kind,
		Name: node.Symbol,
		Source: SourceRef{
			Path: node.Path, Package: node.Package, Symbol: node.Symbol,
			StartLine: node.Line, EndLine: node.EndLine,
		},
	}
}

// sortedNodes returns deterministic node order.
func sortedNodes(nodes map[string]Node) []Node {
	out := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// testInfo projects one retained test declaration.
func testInfo(test TestDecl) TestInfo {
	return TestInfo{
		Path: test.Symbol.Path, Package: test.Symbol.Package,
		Symbol: test.Symbol.Name, QualifiedName: test.Symbol.QualifiedName,
		Kind: test.Kind, Line: test.Symbol.Line, EndLine: test.Symbol.EndLine,
	}
}

// testAtLine finds the test declaration enclosing a line.
func testAtLine(tests []TestDecl, line int) (TestDecl, bool) {
	for _, test := range tests {
		if line >= test.Symbol.Line && line <= test.Symbol.EndLine {
			return test, true
		}
	}
	return TestDecl{}, false
}

// uniqueTests returns the distinct test declarations in an association set.
func uniqueTests(associations []TestAssociation) []TestInfo {
	seen := map[string]bool{}
	out := make([]TestInfo, 0)
	for _, association := range associations {
		key := association.Test.Path + "\x00" + itoa(association.Test.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, association.Test)
	}
	sortTests(out)
	return out
}

// fileScopeContains applies one file or directory scope.
func fileScopeContains(scope string, scopeFile bool, rel string) bool {
	if scope == "" {
		return true
	}
	if scopeFile {
		return rel == scope
	}
	return pathInside(scope, rel)
}

// normalizeInventoryBounds applies the shared inventory defaults and ceilings.
func normalizeInventoryBounds(limit, skip int) (int, int) {
	if limit <= 0 {
		limit = StructureDefaultLimit
	}
	if limit > StructureMaxLimit {
		limit = StructureMaxLimit
	}
	if skip < 0 {
		skip = 0
	}
	return limit, skip
}

// pageFiles paginates and reports the bound that stopped the page.
func pageFiles(files []FileInfo, limit, skip int) ([]FileInfo, bool, string) {
	if skip > len(files) {
		skip = len(files)
	}
	files = files[skip:]
	if len(files) > limit {
		return files[:limit], true, "limit"
	}
	return files, false, ""
}

// pagePackages paginates and reports the bound that stopped the page.
func pagePackages(packages []PackageInfo, limit, skip int) ([]PackageInfo, bool, string) {
	if skip > len(packages) {
		skip = len(packages)
	}
	packages = packages[skip:]
	if len(packages) > limit {
		return packages[:limit], true, "limit"
	}
	return packages, false, ""
}

// pageTests paginates and reports the bound that stopped the page.
func pageTests(tests []TestInfo, limit, skip int) ([]TestInfo, bool, string) {
	if skip > len(tests) {
		skip = len(tests)
	}
	tests = tests[skip:]
	if len(tests) > limit {
		return tests[:limit], true, "limit"
	}
	return tests, false, ""
}

// pageAssociations paginates and reports the bound that stopped the page.
func pageAssociations(items []TestAssociation, limit, skip int) ([]TestAssociation, bool, string) {
	if skip > len(items) {
		skip = len(items)
	}
	items = items[skip:]
	if len(items) > limit {
		return items[:limit], true, "limit"
	}
	return items, false, ""
}

// sortTests orders test declarations by path then line.
func sortTests(tests []TestInfo) {
	sort.SliceStable(tests, func(i, j int) bool {
		if tests[i].Path != tests[j].Path {
			return tests[i].Path < tests[j].Path
		}
		if tests[i].Line != tests[j].Line {
			return tests[i].Line < tests[j].Line
		}
		return tests[i].QualifiedName < tests[j].QualifiedName
	})
}
