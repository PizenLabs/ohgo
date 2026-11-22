package structural

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// runFilesystem records that the hierarchy exists. It is a real step, not a
// Every later strategy depends on the materialised level being present, and a
// missing snapshot is a programming error, not an empty project.
func runFilesystem(s *analysisState) error {
	if s.snapshot == nil {
		return errors.New("hierarchy was not built")
	}
	return nil
}

// runManifest projects the observed build manifests into the topology summary.
func runManifest(s *analysisState) error {
	topology := ManifestTopology{Module: s.inventory.Snapshot.Module}
	refs := make([]EvidenceRef, 0, 4)
	moduleManifests := 0
	workspaces := map[string]bool{}
	for _, config := range s.inventory.Configs {
		if len(topology.Manifests) >= 50 {
			break
		}
		switch config.Kind {
		case "module":
			moduleManifests++
		case "workspace":
			workspaces[config.Path] = true
		}
		topology.Manifests = append(topology.Manifests, ManifestFact{
			Path: config.Path, Kind: config.Kind, Framework: config.Framework,
			Language: string(config.Language),
		})
		if len(refs) < 4 {
			refs = append(refs, EvidenceRef{Kind: KindManifestFact, Path: config.Path, Rule: config.Kind})
		}
	}
	for _, boundary := range s.inventory.Snapshot.NestedProjects {
		if boundary.Included {
			workspaces[boundary.Path] = true
		}
	}
	for workspace := range workspaces {
		topology.Workspaces = append(topology.Workspaces, workspace)
	}
	sort.Strings(topology.Workspaces)
	topology.MultiModule = moduleManifests > 1 || len(topology.Workspaces) > 0
	if len(refs) == 0 {
		topology.Evidence = UnknownEvidence("no build manifest observed")
	} else {
		topology.Evidence = ObservedEvidence(KindManifestFact, refs...)
	}
	s.manifest = topology
	return nil
}

// runDependencyGraph confirms that the aggregated dependency projection is
// available. The projection itself is fetched once, before the hierarchy is
// built, because it is what triggers parsing.
func runDependencyGraph(s *analysisState) error {
	if s.deps == nil {
		return errors.New("dependency projection unavailable")
	}
	if s.graph == nil {
		return errors.New("dependency graph could not be mapped onto materialised nodes")
	}
	return nil
}

func runSCC(s *analysisState) error {
	if s.graph == nil {
		return errors.New("dependency graph unavailable")
	}
	s.components = s.graph.CyclicComponents()
	s.cycles = s.graph.RepresentativeCycles(6)
	return nil
}

// runBoundaries derives crossings from edges.
//
// It reports three kinds and nothing else: module-to-module crossings, observed
// source-class crossings (production into generated/vendor/test), and external
// dependency counts. A boundary is never inferred from a directory name.
func runBoundaries(s *analysisState) error {
	if s.deps == nil {
		return errors.New("dependency projection unavailable")
	}
	type boundaryKey struct {
		from string
		to   string
		kind ast.EdgeKind
	}
	counts := map[boundaryKey]int{}
	witness := map[boundaryKey]ast.DependencyWitness{}
	for _, dep := range s.deps.Edges {
		from := s.resolveDir(dep.From)
		to := s.resolveDir(dep.To)
		if from == "" || to == "" || from == to {
			continue
		}
		fromModule := s.moduleOf(from)
		toModule := s.moduleOf(to)
		if fromModule != "" && toModule != "" && fromModule != toModule {
			key := boundaryKey{from: fromModule, to: toModule, kind: EdgeCrosses}
			counts[key]++
			if _, ok := witness[key]; !ok {
				witness[key] = dep.Witness
			}
		}
		fromClass := s.dominantClass(from)
		toClass := s.dominantClass(to)
		if fromClass != toClass && fromClass != ClassUnknown && toClass != ClassUnknown {
			key := boundaryKey{
				from: "class:" + string(fromClass), to: "class:" + string(toClass),
				kind: EdgeCrosses,
			}
			counts[key]++
			if _, ok := witness[key]; !ok {
				witness[key] = dep.Witness
			}
		}
	}

	external := map[string]int{}
	for _, node := range s.deps.Nodes {
		if node.ExternalImports == 0 {
			continue
		}
		module := s.moduleOf(s.resolveDir(node.Path))
		if module == "" {
			module = s.resolveDir(node.Path)
		}
		if module == "" {
			continue
		}
		external[module] += node.ExternalImports
	}
	for module, count := range external {
		key := boundaryKey{from: module, to: "external", kind: ast.EdgeDependsOn}
		counts[key] += count
	}

	for key, count := range counts {
		evidence := s.edgeEvidence(witness[key])
		if key.to == "external" {
			evidence = ObservedEvidence(KindImportEdge, EvidenceRef{
				Kind: KindImportEdge, Path: key.from, Rule: "external import count",
			})
		}
		s.boundaries = append(s.boundaries, Boundary{
			From: key.from, To: key.to, Kind: key.kind,
			Crossings: count, Evidence: evidence,
		})
	}
	sort.SliceStable(s.boundaries, func(i, j int) bool {
		if s.boundaries[i].Crossings != s.boundaries[j].Crossings {
			return s.boundaries[i].Crossings > s.boundaries[j].Crossings
		}
		if s.boundaries[i].From != s.boundaries[j].From {
			return s.boundaries[i].From < s.boundaries[j].From
		}
		if s.boundaries[i].To != s.boundaries[j].To {
			return s.boundaries[i].To < s.boundaries[j].To
		}
		return s.boundaries[i].Kind < s.boundaries[j].Kind
	})
	return nil
}

// countModules reports how many Go module-level nodes the snapshot published.
// LevelModule also names top-level directories (docs, migrations); those are
// not Go modules and must not hide a single-module layout.
func countModules(snapshot *Snapshot) int {
	return len(goModuleNodes(snapshot))
}

func goModuleNodes(snapshot *Snapshot) []Node {
	if snapshot == nil {
		return nil
	}
	var out []Node
	for _, node := range snapshot.Nodes {
		if node.Level != LevelModule {
			continue
		}
		if node.Language == ast.LanguageGo {
			out = append(out, node)
			continue
		}
		for _, language := range node.Languages {
			if language == ast.LanguageGo {
				out = append(out, node)
				break
			}
		}
	}
	return out
}

// moduleChildGraph collapses a directory graph to the direct children of the
// single Go module. It is the resolution a developer uses when the repo
// contains one Go module under a top-level directory.
func moduleChildGraph(snapshot *Snapshot, directories *Graph) *Graph {
	if snapshot == nil || directories == nil {
		return nil
	}
	modules := goModuleNodes(snapshot)
	if len(modules) != 1 {
		return nil
	}
	module := modules[0]
	ids := map[string]bool{}
	edges := make([]GraphEdge, 0, directories.EdgeCount())
	for _, edge := range directories.Edges() {
		from := moduleChildID(snapshot, module, edge.From)
		to := moduleChildID(snapshot, module, edge.To)
		if from == "" || to == "" || from == to {
			continue
		}
		ids[from], ids[to] = true, true
		edges = append(edges, GraphEdge{From: from, To: to})
	}
	if len(ids) < 3 || len(edges) == 0 {
		return nil
	}
	nodeIDs := make([]string, 0, len(ids))
	for id := range ids {
		nodeIDs = append(nodeIDs, id)
	}
	return NewGraph(nodeIDs, edges)
}

// moduleChildID maps one directory node to the module's first path segment.
func moduleChildID(snapshot *Snapshot, module Node, id string) string {
	node, ok := snapshot.Node(id)
	if !ok {
		return ""
	}
	if node.Path == module.Path {
		return module.ID
	}
	prefix := module.Path + "/"
	if !strings.HasPrefix(node.Path, prefix) {
		return ""
	}
	relative := strings.TrimPrefix(node.Path, prefix)
	first := strings.SplitN(relative, "/", 2)[0]
	childID := ast.DirectoryNodeID(path.Join(module.Path, first))
	if _, ok := snapshot.Node(childID); !ok {
		return ""
	}
	return childID
}

// runArchitecture infers a pattern from graph shape.
//
// The inference is conservative by construction: a cycle, too few modules, or an
// incomplete projection yields "mixed" or "not-established", never a confident
// label. Directory names are collected as Hints and never used as evidence.
func runArchitecture(s *analysisState) error {
	graph := s.moduleGraph
	resolution := "module"
	moduleCount := countModules(s.snapshot)
	// A repository with one Go module nested under its project root has a
	// single module node. Every dependency then resolves to that node and the
	// module projection has no edges, even though the code inside it is
	// layered. Use the module's direct children as the architecture units in
	// that shape; a whole-project directory graph introduces deeper package
	// cycles the module root does not see.
	if moduleCount == 1 && (graph == nil || graph.EdgeCount() == 0) {
		if child := moduleChildGraph(s.snapshot, s.graph); child != nil && child.Len() >= 3 && child.EdgeCount() > 0 {
			graph = child
			resolution = "module-child"
		}
	}
	// No Go module (a non-Go project) or no module-level shape: the directory
	// projection is the only observed graph available.
	if (graph == nil || graph.Len() < 3 || graph.EdgeCount() == 0) && moduleCount <= 1 &&
		s.graph != nil && s.graph.Len() >= 3 && s.graph.EdgeCount() > 0 {
		graph = s.graph
		resolution = "directory"
	}
	architecture := Architecture{
		Pattern: "not-established",
		Basis:   "insufficient-evidence",
		State:   StateUnknown,
		Hints:   nameHints(s.snapshot),
	}
	if resolution != "module" {
		architecture.Hints = append(architecture.Hints,
			"shape inferred at "+resolution+" resolution; the module projection was too coarse")
	}
	if graph == nil || graph.Len() < 3 {
		modules := 0
		if graph != nil {
			modules = graph.Len()
		}
		architecture.Evidence = []string{
			fmt.Sprintf("module graph has %d modules; at least 3 are required for a shape claim", modules),
		}
		s.architecture = architecture
		return nil
	}

	layers, acyclic := graph.Layering()
	cycles := graph.CyclicComponents()
	var evidence []string
	confidence := 0.0
	state := StateObserved
	switch {
	case !acyclic:
		architecture.Pattern = "mixed"
		architecture.Basis = "graph"
		confidence = 0.55
		evidence = append(evidence, fmt.Sprintf(
			"module graph has %d cyclic components across %d modules", len(cycles), graph.Len()))
		for i, component := range cycles {
			if i >= 3 {
				break
			}
			evidence = append(evidence, "cycle: "+strings.Join(component, " -> "))
		}
		architecture.Alternatives = []string{"layered (contradicted by observed cycles)"}
	case len(layers) >= 3:
		architecture.Pattern = "layered"
		architecture.Basis = "graph"
		confidence = 0.75
		evidence = append(evidence, fmt.Sprintf(
			"module graph is acyclic: %d modules, %d edges, %d layers",
			graph.Len(), graph.EdgeCount(), len(layers)))
		if chain := longestChain(graph, layers); len(chain) > 1 {
			evidence = append(evidence, "deepest observed chain: "+strings.Join(chain, " -> "))
		}
		architecture.Alternatives = []string{"modular monolith"}
	case graph.EdgeCount() > 0:
		architecture.Pattern = "modular"
		architecture.Basis = "graph"
		confidence = 0.6
		evidence = append(evidence, fmt.Sprintf(
			"module graph is acyclic but shallow: %d modules, %d edges, %d layers",
			graph.Len(), graph.EdgeCount(), len(layers)))
	default:
		// A zero-edge module graph is much more often a resolution gap than a
		// set of truly independent deployables. Only claim independence when
		// the dependency projection is complete; otherwise the honest label is
		// "not-established", and the reason is attached below.
		if s.deps != nil && (!s.deps.Complete || len(s.deps.Unresolved) > 0) {
			architecture.Pattern = "not-established"
			architecture.Basis = "incomplete-graph"
			evidence = append(evidence,
				"no module-to-module dependency edge was observed, but the dependency projection is incomplete")
		} else {
			architecture.Pattern = "independent modules"
			architecture.Basis = "graph"
			confidence = 0.5
			evidence = append(evidence, "no module-to-module dependency edge was observed")
		}
	}
	if s.deps != nil && !s.deps.Complete {
		state = StatePartiallyObserved
		if confidence > 0.5 {
			confidence = 0.5
		}
		reason := s.deps.Reason
		if reason == "" {
			reason = "partial dependency evidence"
		}
		evidence = append(evidence, "dependency evidence incomplete: "+reason)
	}
	architecture.State = state
	architecture.Confidence = confidence
	architecture.Evidence = evidence
	s.architecture = architecture
	return nil
}

func runDegree(s *analysisState) error {
	if s.graph == nil {
		return errors.New("dependency graph unavailable")
	}
	s.centrality = s.graph.DegreeCentrality()
	return nil
}

// runBetweenness runs exact betweenness only when the graph is small.
//
// The bound is a refusal, not a failure: it is recorded as a skipped strategy
// with the real graph size, so a consumer knows the answer is degree-based.
func runBetweenness(s *analysisState) error {
	if s.graph == nil {
		return errors.New("dependency graph unavailable")
	}
	ranking, ok := s.graph.Betweenness(BetweennessOptions{MaxNodes: 400, MaxEdges: 4000})
	if !ok {
		s.betweennessReason = fmt.Sprintf(
			"graph is %d nodes / %d edges; exact betweenness is bounded to 400 nodes / 4000 edges",
			s.graph.Len(), s.graph.EdgeCount())
		return nil
	}
	s.betweenness = ranking
	return nil
}

// runModularityLabelPropagation clusters the dependency graph by bounded
// modularity gain. The algorithm is graph clustering; it is unrelated to the
// Okapi BM25 ranking function used by intelligence/retrieval.
//
// The directory graph is used when it fits the analysis budget; a large graph is
// clustered at module resolution instead, because a 10,000-node partition is
// neither explainable nor cheap to present.
func runModularityLabelPropagation(s *analysisState) error {
	target := s.graph
	level := "directory"
	if target != nil && target.Len() > 800 && s.moduleGraph != nil && s.moduleGraph.Len() > 0 {
		target = s.moduleGraph
		level = "module"
	}
	if target == nil {
		return errors.New("dependency graph unavailable")
	}
	s.communities = ModularityClustering(target, ModularitySweepBound)
	s.communityLevel = level
	return nil
}

// runSampling selects representative directories by structural signal.
func runSampling(s *analysisState) error {
	if s.graph == nil {
		return errors.New("dependency graph unavailable")
	}
	const limit = 12
	for _, item := range s.graph.DegreeCentrality() {
		if len(s.samples) >= limit {
			break
		}
		node, ok := s.snapshot.Node(item.ID)
		if !ok || node.Files == 0 {
			continue
		}
		reason := "representative non-isolated module"
		switch {
		case item.FanIn >= 3 && item.FanOut >= 3:
			reason = fmt.Sprintf("two-way coupling (fan-in %d, fan-out %d)", item.FanIn, item.FanOut)
		case item.FanIn >= 3:
			reason = fmt.Sprintf("high fan-in (%d dependents)", item.FanIn)
		case item.FanOut >= 3:
			reason = fmt.Sprintf("high fan-out (%d dependencies)", item.FanOut)
		}
		s.samples = append(s.samples, Sample{
			Path:   s.representativeFile(node.Path),
			NodeID: node.ID,
			Reason: reason,
			Score:  item.Score,
			Evidence: ObservedEvidence(KindImportEdge, EvidenceRef{
				Kind: KindImportEdge, Path: node.Path, Rule: "degree ranking",
			}),
		})
	}
	return nil
}

// runSymbolNeighborhood asks the existing impact resolver about the focus. It
// consumes that projection; it does not re-resolve relationships itself.
func runSymbolNeighborhood(s *analysisState) error {
	selector := strings.TrimSpace(s.request.Focus)
	kind := "symbol"
	if looksLikePath(selector) {
		kind = "path"
	}
	report := &FocusReport{
		Selector: selector, SelectorKind: kind,
		Evidence: UnknownEvidence("no impact resolver is configured"),
	}
	neighbor, ok := s.source.(NeighborhoodSource)
	if !ok {
		s.focus = report
		return errors.New("source does not implement impact resolution")
	}
	query := ast.ImpactQuery{Depth: 2, Limit: 50}
	if kind == "path" {
		query.Path = selector
	} else {
		query.Symbol = selector
	}
	result, err := neighbor.Impact(s.ctx, query)
	if err != nil {
		s.focus = report
		return err
	}
	report.Found = result.Found
	report.Ambiguous = result.Ambiguous
	report.Targets = len(result.Targets)
	// References is the reference list only. Impact.Total also contains
	// callers, callees, importers, and tests; reporting that total here made a
	// query with one reference and one caller claim two references.
	report.References = len(result.References)
	report.Callers = len(result.Callers)
	report.Callees = len(result.Callees)
	report.Importers = len(result.Importers)
	report.Tests = len(result.Tests)
	report.Nodes = len(result.Nodes)
	report.Edges = len(result.Edges)
	report.Truncated = result.Truncated
	report.Reason = result.Reason
	report.Unknown = result.Unknown
	const maxFocusCalls = 12
	for _, edge := range result.Callees {
		if len(report.Calls) >= maxFocusCalls {
			report.Truncated = true
			if report.Reason == "" {
				report.Reason = "focus_call_limit"
			}
			break
		}
		report.Calls = append(report.Calls, FocusCall{
			Direction: "callee", Symbol: focusCallSymbol(edge.Callee),
			Path: edge.CallPath, Line: edge.CallLine, Test: edge.Test,
		})
	}
	for _, edge := range result.Callers {
		if len(report.Calls) >= maxFocusCalls {
			report.Truncated = true
			if report.Reason == "" {
				report.Reason = "focus_call_limit"
			}
			break
		}
		report.Calls = append(report.Calls, FocusCall{
			Direction: "caller", Symbol: focusCallSymbol(edge.Caller),
			Path: edge.CallPath, Line: edge.CallLine, Test: edge.Test,
		})
	}
	for _, dir := range result.Importers {
		if len(report.ImporterPaths) >= 8 {
			break
		}
		report.ImporterPaths = append(report.ImporterPaths, dir)
	}
	for _, candidate := range result.Candidates {
		label := candidate.QualifiedName
		if label == "" {
			label = candidate.Symbol
		}
		if candidate.Path != "" {
			// An ambiguous selector is only actionable when the candidates
			// name distinct coordinates. Two identical qualified names gave a
			// consumer no way to choose.
			label = fmt.Sprintf("%s (%s:%d)", label, candidate.Path, candidate.Line)
		}
		report.Candidates = append(report.Candidates, label)
	}
	switch {
	case result.Ambiguous:
		report.Evidence = UnknownEvidence(fmt.Sprintf(
			"selector %q matched %d declarations; no single neighbourhood is a fact",
			selector, len(result.Candidates)))
	case !result.Found:
		report.Evidence = UnknownEvidence(
			fmt.Sprintf("selector %q matched no indexed declaration", selector))
	default:
		refs := make([]EvidenceRef, 0, 4)
		for _, target := range result.Targets {
			if len(refs) >= 4 {
				break
			}
			refs = append(refs, EvidenceRef{
				Kind: KindAdapterFact, Path: target.Path, Line: target.Line,
			})
		}
		evidence := ObservedEvidence(KindAdapterFact, refs...)
		if result.Truncated || unknownTotal(result.Unknown) > 0 {
			evidence.State = StatePartiallyObserved
			evidence.Confidence = 0.6
			evidence.Reason = fmt.Sprintf(
				"%d static resolution gaps (dynamic, ambiguous, unresolved, or unsupported)",
				unknownTotal(result.Unknown))
		}
		report.Evidence = evidence
	}
	s.focus = report
	return nil
}

// focusCallSymbol renders a call node as package-qualified when the adapter
// did not already include the package.
func focusCallSymbol(node ast.CallNode) string {
	name := node.Symbol
	if name == "" {
		return node.Package
	}
	if node.Package == "" || strings.HasPrefix(name, node.Package+".") {
		return name
	}
	return node.Package + "." + name
}

// runModuleSurvey checks whether every module is represented by a sample and
// selects a coordinate for modules the degree-ranked sampler did not cover.
// It never invents a module: the hierarchy is the only source of containers.
func runModuleSurvey(s *analysisState) error {
	if s.snapshot == nil {
		return errors.New("hierarchy was not built")
	}
	level := LevelModule
	nodes := sourceSurveyNodes(s.snapshot.AtLevel(level))
	if len(nodes) <= 1 {
		// A repository with one module would report a single coverage unit.
		// Expand to the module's direct package children so bounded contexts
		// inside that module are visible without listing every nested package.
		base := "."
		if len(nodes) == 1 {
			base = nodes[0].Path
		}
		var direct []Node
		for _, node := range sourceSurveyNodes(s.snapshot.AtLevel(LevelPackage)) {
			if directSurveyChild(base, node.Path) {
				direct = append(direct, node)
			}
		}
		if len(direct) > len(nodes) {
			level = LevelPackage
			nodes = direct
		}
	}
	if len(nodes) == 0 {
		s.survey = &SurveyReport{
			Level: level.String(), Evidence: UnknownEvidence("no module or workspace container was materialised"),
		}
		return nil
	}
	const maxModules = 200
	report := &SurveyReport{Level: level.String()}
	inventoryBounded := s.inventory.FilesTruncated
	if inventoryBounded {
		report.Truncated = true
		report.Reason = "file inventory was truncated; module coverage may be incomplete"
	}
	refs := make([]EvidenceRef, 0, 4)
	explored := 0
	for index, node := range nodes {
		if index >= maxModules {
			report.Truncated = true
			report.Reason = fmt.Sprintf("module list bounded to %d containers", maxModules)
			break
		}
		if node.Files == 0 || (node.Classes[ClassProduction] == 0 && node.Classes[ClassTest] == 0) {
			// Documentation, configuration, and tooling containers are not
			// source modules; counting them as unexplored would overstate the
			// survey gap.
			continue
		}
		entry := SurveyModule{Path: node.Path, Files: node.Files}
		for _, sample := range s.samples {
			if node.Path == "." || sample.Path == node.Path ||
				strings.HasPrefix(sample.Path, strings.TrimSuffix(node.Path, "/")+"/") {
				entry.Sample = sample.Path
				entry.Explored = true
				break
			}
		}
		if !entry.Explored {
			if candidate := s.representativeFile(node.Path); candidate != "" {
				entry.Sample = candidate
				entry.Explored = true
				if len(s.samples) < 24 {
					s.samples = append(s.samples, Sample{
						Path: candidate, NodeID: node.ID,
						Reason: "module not covered by degree-ranked sampling",
						Evidence: ObservedEvidence(KindInventory, EvidenceRef{
							Kind: KindInventory, Path: candidate, Rule: "module survey",
						}),
					})
				} else {
					report.Truncated = true
					report.Reason = "sample budget reached before every module had a coordinate"
				}
			}
		}
		if entry.Explored {
			explored++
		} else {
			report.Unexplored = append(report.Unexplored, node.Path)
		}
		report.Modules = append(report.Modules, entry)
		if len(refs) < 4 {
			refs = append(refs, EvidenceRef{Kind: KindInventory, Path: node.Path, Rule: "module hierarchy"})
		}
	}
	report.Explored = explored
	if len(refs) == 0 {
		report.Evidence = UnknownEvidence("no module coordinate was observed")
	} else {
		report.Evidence = ObservedEvidence(KindInventory, refs...)
	}
	if len(report.Unexplored) > 0 {
		report.Evidence.State = StatePartiallyObserved
		report.Evidence.Reason = fmt.Sprintf("%d module(s) have no sampled source coordinate", len(report.Unexplored))
	}
	if stale := s.inventory.Snapshot.StaleFiles; stale > 0 {
		report.Evidence.State = StatePartiallyObserved
		if report.Evidence.Reason != "" {
			report.Evidence.Reason += "; "
		}
		report.Evidence.Reason += fmt.Sprintf("%d source files are stale and were not sampled", stale)
	}
	if report.Truncated && report.Evidence.State == StateObserved {
		report.Evidence.State = StatePartiallyObserved
		if report.Evidence.Reason == "" {
			report.Evidence.Reason = report.Reason
		}
	}
	s.survey = report
	return nil
}

// representativeFile returns a production source coordinate inside dir. It
// prefers a direct child and falls back to the deepest subtree so a module
// whose source lives in nested packages is still representable.
func (s *analysisState) representativeFile(dir string) string {
	for _, file := range s.inventory.Files {
		if path.Dir(file.Path) != dir {
			continue
		}
		if file.Test || !sampleableState(file.State) || file.Language == "" {
			continue
		}
		class, _ := classifyPath(file.Path, false)
		if class != ClassProduction {
			continue
		}
		return file.Path
	}
	prefix := strings.TrimSuffix(dir, "/")
	if prefix == "." {
		prefix = ""
	} else {
		prefix += "/"
	}
	var best string
	for _, file := range s.inventory.Files {
		if prefix != "" && !strings.HasPrefix(file.Path, prefix) {
			continue
		}
		if file.Test || !sampleableState(file.State) {
			continue
		}
		class, _ := classifyPath(file.Path, false)
		if class != ClassProduction {
			continue
		}
		if file.Language == "" {
			continue
		}
		if best == "" || file.Path < best {
			best = file.Path
		}
	}
	return best
}

// sourceSurveyNodes keeps containers that hold at least one recognised source
// language. Documentation, deployment YAML, and SQL-only directories are not
// source modules; counting them as unexplored would overstate the survey gap.
func sourceSurveyNodes(nodes []Node) []Node {
	out := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Files == 0 || len(node.Languages) == 0 {
			continue
		}
		out = append(out, node)
	}
	return out
}

// directSurveyChild reports whether path is base itself or one directory below
// it. It is the bounded-context resolution for a single-module repository.
func directSurveyChild(base, path string) bool {
	if base == "" || base == "." {
		return path == "." || !strings.Contains(path, "/")
	}
	if path == base {
		return true
	}
	rest := strings.TrimPrefix(path, base+"/")
	return rest != path && rest != "" && !strings.Contains(rest, "/")
}

// sampleableState rejects a coordinate whose content is unknown or historical.
// Pending source is sampleable because a project orientation deliberately does
// not parse it; the sample is still a file the model can read.
func sampleableState(state ast.ParseState) bool {
	switch state {
	case ast.ParseStateStale, ast.ParseStateFailed, ast.ParseStateUnsupported:
		return false
	default:
		return true
	}
}

// dominantClass names the class a node's membership implies.
func (s *analysisState) dominantClass(id string) SourceClass {
	node, ok := s.snapshot.Node(id)
	if !ok {
		return ClassUnknown
	}
	if node.Class != "" {
		return node.Class
	}
	for _, class := range []SourceClass{ClassVendor, ClassGenerated, ClassBuildArtifact, ClassFixture} {
		if node.Classes[class] > 0 {
			return class
		}
	}
	if node.Classes[ClassTest] > 0 && node.Classes[ClassProduction] == 0 {
		return ClassTest
	}
	if node.Classes[ClassProduction] > 0 {
		return ClassProduction
	}
	return ClassUnknown
}

// edgeEvidence is the provenance of an aggregated dependency edge.
func (s *analysisState) edgeEvidence(witness ast.DependencyWitness) Evidence {
	ref := EvidenceRef{Kind: KindImportEdge, Path: witness.Path, Line: witness.Line}
	if witness.Path == "" {
		ref.Path = "."
	}
	evidence := ObservedEvidence(KindImportEdge, ref)
	if s.deps != nil && !s.deps.Complete {
		evidence.State = StatePartiallyObserved
		evidence.Confidence = 0.6
		evidence.Reason = s.deps.Reason
	}
	return evidence
}

// longestChain samples one longest dependency chain from the layering.
func longestChain(graph *Graph, layers []Layer) []string {
	if len(layers) == 0 || len(layers[0].Nodes) == 0 {
		return nil
	}
	depthOf := map[string]int{}
	for _, layer := range layers {
		for _, id := range layer.Nodes {
			depthOf[id] = layer.Depth
		}
	}
	chain := []string{layers[0].Nodes[0]}
	current := chain[0]
	for {
		want := depthOf[current] + 1
		if want >= len(layers) {
			break
		}
		next := ""
		for _, candidate := range graph.Dependencies(current) {
			if depthOf[candidate] == want {
				next = candidate
				break
			}
		}
		if next == "" {
			break
		}
		chain = append(chain, next)
		current = next
	}
	return chain
}

// nameHints records directory-name vocabulary that may suggest an architecture.
//
// They are explicitly hints: the architecture inference never uses them, and a
// consumer must not either. They exist so a reader can see what a name-only
// analysis would have guessed and compare it with the graph evidence.
func nameHints(snapshot *Snapshot) []string {
	vocabulary := map[string]bool{
		"api": true, "adapter": true, "adapters": true, "app": true,
		"application": true, "backend": true, "cmd": true, "config": true,
		"controller": true, "controllers": true, "core": true, "domain": true,
		"frontend": true, "handler": true, "handlers": true, "infra": true,
		"infrastructure": true, "internal": true, "lib": true, "model": true,
		"models": true, "pkg": true, "ports": true, "repository": true,
		"repo": true, "service": true, "services": true, "store": true,
		"ui": true, "usecase": true, "usecases": true, "view": true,
		"views": true, "web": true, "worker": true, "workers": true,
	}
	seen := map[string]bool{}
	var hints []string
	for _, node := range snapshot.Nodes {
		base := strings.ToLower(path.Base(node.Path))
		if vocabulary[base] && !seen[base] {
			seen[base] = true
			hints = append(hints, base)
		}
	}
	sort.Strings(hints)
	return hints
}

// looksLikePath distinguishes a repository path from a symbol selector. A
// qualified symbol contains dots but no slash; a file path has a separator or a
// known source extension.
func looksLikePath(selector string) bool {
	if selector == "." || selector == ".." || strings.HasPrefix(selector, "./") ||
		strings.HasPrefix(selector, "../") || strings.HasPrefix(selector, "/") {
		return true
	}
	if strings.Contains(selector, "/") || strings.Contains(selector, "\\") {
		return true
	}
	switch strings.ToLower(path.Ext(selector)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts",
		".py", ".rs", ".java", ".kt", ".kts", ".cs", ".c", ".h", ".cc",
		".cpp", ".cxx", ".hpp", ".hh", ".hxx", ".rb", ".md", ".json", ".yaml", ".yml":
		return true
	}
	return false
}

// unknownTotal counts every static resolution gap the impact resolver reported.
func unknownTotal(unknown ast.UnknownSummary) int {
	return unknown.DotImportFiles + unknown.AmbiguousUses + unknown.UnresolvedUses +
		unknown.DynamicCalls + unknown.PackageInitializerCalls +
		unknown.GenericUnresolved + unknown.GenericAmbiguous + unknown.UnsupportedFiles
}
