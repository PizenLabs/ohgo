package structural

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// Source is the read-only snapshot seam the analyzer consumes.
//
// It is deliberately narrower than *ast.Index: the engine needs one consistent
// inventory and one dependency projection, and nothing that could mutate the
// tree. *ast.Index satisfies it.
type Source interface {
	Inventory(ctx context.Context, query ast.InventoryQuery) (ast.Inventory, error)
	Dependencies(ctx context.Context, query ast.DependencyQuery) (ast.DependencyResult, error)
}

// NeighborhoodSource is the optional focal-symbol seam. It is separate because
// an analysis that needs no selector must not depend on the impact resolver.
type NeighborhoodSource interface {
	Impact(ctx context.Context, query ast.ImpactQuery) (ast.ImpactResult, error)
}

// CacheState reports how the result was obtained.
type CacheState string

// Cache outcomes.
const (
	CacheDisabled CacheState = "disabled"
	CacheMiss     CacheState = "miss"
	CacheHit      CacheState = "hit"
	CacheInvalid  CacheState = "invalid"
)

// ManifestFact is one build manifest observed in the inventory.
type ManifestFact struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Framework string `json:"framework,omitempty"`
	Language  string `json:"language,omitempty"`
}

// ManifestTopology is what the manifests prove about the project layout.
//
// It never claims to have parsed workspace membership: the AST inventory reads
// manifest presence, not contents. Workspaces here are confirmed by go.work or
// by an included nested repository, and MultiModule means more than one module
// manifest was observed.
type ManifestTopology struct {
	Module      string         `json:"module,omitempty"`
	Manifests   []ManifestFact `json:"manifests,omitempty"`
	Workspaces  []string       `json:"workspaces,omitempty"`
	MultiModule bool           `json:"multi_module"`
	Evidence    Evidence       `json:"evidence"`
}

// FocusReport is the structural neighbourhood of the request's selector.
type FocusReport struct {
	Selector     string             `json:"selector"`
	SelectorKind string             `json:"selector_kind"`
	Found        bool               `json:"found"`
	Ambiguous    bool               `json:"ambiguous"`
	Candidates   []string           `json:"candidates,omitempty"`
	Targets      int                `json:"targets"`
	References   int                `json:"references"`
	Callers      int                `json:"callers"`
	Importers    int                `json:"importers"`
	Tests        int                `json:"tests"`
	Nodes        int                `json:"nodes"`
	Edges        int                `json:"edges"`
	Truncated    bool               `json:"truncated"`
	Reason       string             `json:"reason,omitempty"`
	Unknown      ast.UnknownSummary `json:"unknown"`
	Evidence     Evidence           `json:"evidence"`
}

// Analysis is one complete adaptive structural analysis.
type Analysis struct {
	SchemaVersion   string  `json:"schema_version"`
	AnalyzerVersion string  `json:"analyzer_version"`
	Request         Request `json:"request"`
	// Snapshot is the multi-resolution model.
	Snapshot *Snapshot `json:"snapshot"`
	Plan     Plan      `json:"plan"`
	// Manifests are the observed build manifests.
	Manifests    ManifestTopology `json:"manifests"`
	Architecture Architecture     `json:"architecture"`
	Boundaries   []Boundary       `json:"boundaries,omitempty"`
	Communities  []Community      `json:"communities,omitempty"`
	Samples      []Sample         `json:"samples,omitempty"`
	Focus        *FocusReport     `json:"focus,omitempty"`
	Coverage     Coverage         `json:"coverage"`
	Issues       []Issue          `json:"issues,omitempty"`
	Cache        CacheState       `json:"cache"`
	CacheReason  string           `json:"cache_reason,omitempty"`
}

// Analyzer runs the adaptive engine over a Source.
type Analyzer struct {
	source Source
	cache  Cache
}

// NewAnalyzer returns an analyzer. A nil cache disables persistence.
func NewAnalyzer(source Source, cache Cache) *Analyzer {
	return &Analyzer{source: source, cache: cache}
}

// analysisState is the mutable working set of one analysis. It is never shared
// across goroutines and never escapes Analyze.
type analysisState struct {
	ctx      context.Context
	source   Source
	request  Request
	selected []*strategy

	inventory ast.Inventory
	snapshot  *Snapshot
	deps      *ast.DependencyResult

	graph       *Graph
	moduleGraph *Graph
	resolved    map[string]string

	components        [][]string
	cycles            [][]string
	centrality        []Centrality
	betweenness       []Centrality
	betweennessReason string
	communities       ModularityPartition
	communityLevel    string
	boundaries        []Boundary
	architecture      Architecture
	samples           []Sample
	manifest          ManifestTopology
	focus             *FocusReport
	issues            []Issue
	degraded          bool
}

// Analyze runs the planner and the selected strategies.
//
// The only errors are request-shape and source-access failures. A missing
// parser, an incomplete graph, or a truncated inventory is an Issue on an
// otherwise successful analysis: the caller must be able to distinguish "the
// answer is partial" from "the question could not be asked".
func (a *Analyzer) Analyze(ctx context.Context, request Request) (Analysis, error) {
	if a == nil || a.source == nil {
		return Analysis{}, errors.New("structural: analyzer has no source")
	}
	if err := ctx.Err(); err != nil {
		return Analysis{}, err
	}
	req := request.normalized()
	if err := req.validate(); err != nil {
		return Analysis{}, err
	}

	metadata, err := a.source.Inventory(ctx, ast.InventoryQuery{
		MaxFiles: req.MaxFiles,
	})
	if err != nil {
		return Analysis{}, fmt.Errorf("structural: inventory: %w", err)
	}
	analysis := Analysis{
		SchemaVersion: SchemaVersion, AnalyzerVersion: AnalyzerVersion,
		Request: req, Cache: CacheDisabled,
	}
	key := cacheKey(req, metadata.Snapshot)
	if a.cache != nil {
		data, err := a.cache.Load(key)
		switch {
		case err == nil:
			if cached, ok := decodeEnvelope(data, req); ok {
				cached.Cache = CacheHit
				return cached, nil
			}
			analysis.Cache = CacheInvalid
			analysis.CacheReason = "cached entry does not match this schema, analyzer, snapshot, or request"
			analysis.Issues = append(analysis.Issues, Issue{
				Code:   "cache_invalid",
				Detail: analysis.CacheReason,
			})
		case errors.Is(err, fs.ErrNotExist):
			analysis.Cache = CacheMiss
		default:
			analysis.Cache = CacheInvalid
			analysis.CacheReason = "cache read failed: " + err.Error()
			analysis.Issues = append(analysis.Issues, Issue{
				Code:   "cache_read_failed",
				Detail: analysis.CacheReason,
			})
		}
	}

	plan, selected := buildPlan(req, metadata)
	state := &analysisState{
		ctx: ctx, source: a.source, request: req,
		inventory: metadata, selected: selected,
		resolved: map[string]string{},
	}

	needsGraph := false
	needsParse := false
	for _, entry := range selected {
		if entry.needsGraph {
			needsGraph = true
		}
		if entry.needsParse {
			needsParse = true
		}
	}

	if needsGraph {
		deps, err := a.source.Dependencies(ctx, ast.DependencyQuery{
			MaxEdges: dependencyBudget(metadata),
		})
		if err != nil {
			state.addIssue("dependency_graph_failed", err.Error())
			state.degraded = true
		} else {
			state.deps = &deps
		}
	}
	if needsGraph || needsParse {
		// The dependency query parses pending source. Re-project the inventory
		// so the hierarchy's parsed flags and symbol counts describe the state
		// the relationships were observed in, not the pre-parse one.
		if refreshed, err := a.source.Inventory(ctx, ast.InventoryQuery{
			Parse: true, MaxFiles: req.MaxFiles,
		}); err == nil {
			state.inventory = refreshed
		} else {
			state.addIssue("inventory_refresh_failed", err.Error())
			state.degraded = true
		}
	}

	buildLevel := req.Level
	for _, entry := range selected {
		if entry.level > buildLevel {
			buildLevel = entry.level
		}
	}
	snapshot := Build(state.inventory, BuildOptions{Level: buildLevel, MaxFiles: req.MaxFiles})
	state.snapshot = snapshot
	if state.deps != nil {
		state.connect()
	}

	for _, entry := range selected {
		if err := entry.run(state); err != nil {
			state.addIssue("strategy_failed_"+entry.name, err.Error())
			state.degraded = true
		}
		if err := ctx.Err(); err != nil {
			return Analysis{}, err
		}
	}

	plan.Level = snapshot.Level
	coverage := state.finalizeCoverage()
	plan.StopReason = state.stopReason(coverage)

	issues := mergeIssues(analysis.Issues, snapshot.Issues, state.issues)
	plan.Skipped = append(plan.Skipped, state.runtimeSkips()...)
	analysis.Plan = plan
	analysis.Snapshot = snapshot
	analysis.Manifests = state.manifest
	analysis.Architecture = state.architecture
	if analysis.Architecture.Pattern == "" {
		analysis.Architecture = Architecture{
			Pattern: "not-analyzed",
			Basis:   "strategy-not-selected",
			State:   StateUnknown,
		}
	}
	analysis.Boundaries = state.boundaries
	analysis.Communities = state.communities.Communities
	analysis.Samples = state.samples
	analysis.Focus = state.focus
	analysis.Coverage = coverage
	analysis.Issues = issues

	if a.cache != nil {
		if encoded, err := encodeEnvelope(req, analysis); err == nil {
			if err := a.cache.Store(key, encoded); err != nil {
				analysis.Issues = append(analysis.Issues, Issue{
					Code: "cache_write_failed", Detail: err.Error(),
				})
			}
		}
	}
	return analysis, nil
}

// dependencyBudget scales the edge ceiling with repository size while keeping it
// bounded. A 200,000-edge graph is summarised hierarchically, not materialised
// into a model-facing result.
func dependencyBudget(inventory ast.Inventory) int {
	budget := inventory.Snapshot.Packages * 8
	if budget < 1000 {
		budget = 1000
	}
	if budget > 20000 {
		budget = 20000
	}
	return budget
}

func (s *analysisState) addIssue(code, detail string) {
	if code == "" {
		return
	}
	for _, existing := range s.issues {
		if existing.Code == code && existing.Detail == detail {
			return
		}
	}
	s.issues = append(s.issues, Issue{Code: code, Detail: detail})
}

// mergeIssues de-duplicates two issue lists deterministically.
func mergeIssues(groups ...[]Issue) []Issue {
	var out []Issue
	seen := map[string]bool{}
	for _, group := range groups {
		for _, issue := range group {
			key := issue.Code + "\x00" + issue.Detail
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, issue)
		}
	}
	return out
}

// connect builds the directory and module graphs from the dependency
// projection.
func (s *analysisState) connect() {
	if s.deps == nil || s.snapshot == nil {
		return
	}
	s.graph, s.moduleGraph = ProjectGraphs(s.snapshot, *s.deps)
}

// ProjectGraphs maps a dependency projection onto the materialised model.
//
// Endpoints resolve to the deepest published node that contains them: a model
// built at module resolution aggregates edges there instead of inventing
// directory nodes it never published. Both graphs are returned so a consumer can
// analyse coupling at directory resolution and architecture at module
// resolution without a second projection.
func ProjectGraphs(snapshot *Snapshot, deps ast.DependencyResult) (directories, modules *Graph) {
	if snapshot == nil {
		return NewGraph(nil, nil), NewGraph(nil, nil)
	}
	resolved := map[string]string{}
	ids := make([]string, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		switch node.Kind {
		case ast.NodeProject, NodeWorkspace, ast.NodeFile:
			continue
		}
		if node.Level > LevelPackage {
			continue
		}
		ids = append(ids, node.ID)
	}
	edges := make([]GraphEdge, 0, len(deps.Edges))
	moduleEdges := make([]GraphEdge, 0, len(deps.Edges))
	for _, dep := range deps.Edges {
		from := resolveNodePath(snapshot, resolved, dep.From)
		to := resolveNodePath(snapshot, resolved, dep.To)
		if from == "" || to == "" || from == to {
			continue
		}
		edges = append(edges, GraphEdge{From: from, To: to})
		fromModule := moduleNodeOf(snapshot, from)
		toModule := moduleNodeOf(snapshot, to)
		if fromModule != "" && toModule != "" && fromModule != toModule {
			moduleEdges = append(moduleEdges, GraphEdge{From: fromModule, To: toModule})
		}
	}
	moduleIDs := make([]string, 0)
	for _, node := range snapshot.Nodes {
		if node.Level == LevelModule {
			moduleIDs = append(moduleIDs, node.ID)
		}
	}
	return NewGraph(ids, edges), NewGraph(moduleIDs, moduleEdges)
}

// resolveNodePath maps a dependency directory to the deepest materialised node
// containing it, caching the answer for one projection pass.
func resolveNodePath(snapshot *Snapshot, cache map[string]string, dir string) string {
	if cached, ok := cache[dir]; ok {
		return cached
	}
	candidate := dir
	for {
		id := ast.DirectoryNodeID(candidate)
		if _, ok := snapshot.Node(id); ok {
			cache[dir] = id
			return id
		}
		parent := path.Dir(candidate)
		if parent == candidate || candidate == "." || candidate == "" {
			break
		}
		candidate = parent
	}
	best := ""
	bestID := ""
	for _, node := range snapshot.Nodes {
		if node.Kind != NodeWorkspace {
			continue
		}
		if pathInside(node.Path, dir) && len(node.Path) > len(best) {
			best = node.Path
			bestID = node.ID
		}
	}
	cache[dir] = bestID
	return bestID
}

// moduleNodeOf returns the module-level ancestor of a node, empty when the model
// is too coarse or the node is a workspace.
func moduleNodeOf(snapshot *Snapshot, id string) string {
	for id != "" {
		node, ok := snapshot.Node(id)
		if !ok {
			return ""
		}
		if node.Level == LevelModule {
			return id
		}
		if node.Kind == NodeWorkspace || node.Level == LevelProject {
			return ""
		}
		id = node.Parent
	}
	return ""
}

// pathInside reports whether rel is dir or below it.
func pathInside(dir, rel string) bool {
	if dir == "" || dir == "." {
		return true
	}
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}

// resolveDir maps a dependency directory to a materialised node for this analysis.
func (s *analysisState) resolveDir(dir string) string {
	if s.snapshot == nil {
		return ""
	}
	return resolveNodePath(s.snapshot, s.resolved, dir)
}

// moduleOf is moduleNodeOf bound to this analysis.
func (s *analysisState) moduleOf(id string) string {
	if s.snapshot == nil {
		return ""
	}
	return moduleNodeOf(s.snapshot, id)
}

// finalizeCoverage folds graph facts into the hierarchy coverage.
func (s *analysisState) finalizeCoverage() Coverage {
	coverage := s.snapshot.Coverage
	if s.graph != nil {
		coverage.GraphNodes = s.graph.Len()
		coverage.GraphEdges = s.graph.EdgeCount()
	}
	if s.deps != nil {
		coverage.DependenciesSeen = true
		coverage.Unresolved = len(s.deps.Unresolved)
	}
	if s.deps != nil && !s.deps.Complete {
		coverage.State = StatePartiallyObserved
		if coverage.Confidence > 0.7 {
			coverage.Confidence = 0.7
		}
		if coverage.Reason == "" {
			coverage.Reason = s.deps.Reason
		}
	}
	if s.deps == nil && s.needsDependencies() {
		coverage.State = StatePartiallyObserved
		coverage.Confidence = 0.45
		coverage.Reason = "dependency_graph_unavailable"
	}
	if s.degraded && coverage.Confidence > 0.6 {
		coverage.Confidence = 0.6
	}
	if s.snapshot != nil {
		s.snapshot.Coverage = coverage
	}
	return coverage
}

func (s *analysisState) needsDependencies() bool {
	for _, entry := range s.selected {
		if entry.needsGraph {
			return true
		}
	}
	return false
}

// stopReason names why the analysis ended.
func (s *analysisState) stopReason(coverage Coverage) string {
	switch {
	case s.degraded:
		return "partial_analysis"
	case coverage.Confidence >= s.request.MinConfidence:
		return "confidence_reached"
	case len(s.selected) >= s.request.MaxSteps:
		return "step_budget_exhausted"
	default:
		return "portfolio_exhausted"
	}
}

// runtimeSkips reports strategy decisions that could only be made once the real
// graph size was known.
func (s *analysisState) runtimeSkips() []Skip {
	if s.betweennessReason == "" {
		return nil
	}
	return []Skip{{Strategy: "betweenness-centrality", Reason: s.betweennessReason}}
}

// Summaries returns the top nodes at one resolution, ordered by file count then
// ID. It is the model-facing projection helper: a summary never serialises the
// whole graph.
func Summaries(snapshot *Snapshot, level Level, limit int) []Node {
	if snapshot == nil {
		return nil
	}
	nodes := snapshot.AtLevel(level)
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Files != nodes[j].Files {
			return nodes[i].Files > nodes[j].Files
		}
		return nodes[i].ID < nodes[j].ID
	})
	if limit > 0 && len(nodes) > limit {
		nodes = nodes[:limit]
	}
	return nodes
}

// Ancestors returns the node and its containment chain, root last.
func Ancestors(snapshot *Snapshot, id string) []Node {
	if snapshot == nil {
		return nil
	}
	var out []Node
	for id != "" {
		node, ok := snapshot.Node(id)
		if !ok {
			break
		}
		out = append(out, node)
		id = node.Parent
	}
	return out
}
