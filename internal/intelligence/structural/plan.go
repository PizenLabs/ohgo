package structural

import (
	"errors"
	"fmt"
	"sort"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// Intent is the task class a structural analysis serves. It is the planner's
// primary input: the same repository produces a different analysis for "what is
// this project" than for "what breaks if I change this package".
type Intent string

// Supported intents.
const (
	// IntentProject asks for orientation: languages, major modules, shape.
	IntentProject Intent = "project"
	// IntentArchitecture asks how the system is organised and whether the
	// dependency evidence supports a named architecture.
	IntentArchitecture Intent = "architecture"
	// IntentLocate asks where a named concept or symbol lives.
	IntentLocate Intent = "locate"
	// IntentFlow asks how a capability is implemented end to end.
	IntentFlow Intent = "flow"
	// IntentImpact asks what depends on a component and what tests touch it.
	IntentImpact Intent = "impact"
	// IntentIsolation asks whether a component is safe to change alone.
	IntentIsolation Intent = "isolation"
)

// ParseIntent accepts the model-facing intent vocabulary.
func ParseIntent(value string) (Intent, bool) {
	switch Intent(value) {
	case IntentProject:
		return IntentProject, true
	case IntentArchitecture:
		return IntentArchitecture, true
	case IntentLocate:
		return IntentLocate, true
	case IntentFlow:
		return IntentFlow, true
	case IntentImpact:
		return IntentImpact, true
	case IntentIsolation:
		return IntentIsolation, true
	default:
		return "", false
	}
}

// Request is one analysis request.
type Request struct {
	Intent Intent
	// Focus is the path or symbol the intent is about. It is required for
	// locate, flow, impact, and isolation.
	Focus string
	// Level is the deepest resolution the caller authorises.
	Level Level
	// MaxFiles bounds materialised file nodes.
	MaxFiles int
	// MaxSteps bounds how many strategies may run.
	MaxSteps int
	// MinConfidence is the stopping threshold. Zero uses the intent default.
	MinConfidence float64
}

// Errors returned by Analyze for request-shape problems, never for analysis
// gaps. A gap is an Issue.
var (
	// ErrFocusRequired reports an intent that cannot be answered without a
	// selector.
	ErrFocusRequired = errors.New("structural: intent requires a focus selector")
	// ErrUnknownIntent reports an unregistered intent.
	ErrUnknownIntent = errors.New("structural: unknown intent")
)

// defaultThreshold is the intent's stopping threshold.
//
// It is a policy default, not a measurement. A caller that needs a different
// tradeoff sets MinConfidence; the planner never invents a threshold.
func defaultThreshold(intent Intent) float64 {
	switch intent {
	case IntentProject:
		return 0.55
	case IntentArchitecture:
		return 0.70
	default:
		return 0.65
	}
}

// RequiresFocus reports whether an intent cannot be answered without a path or
// symbol selector.
func RequiresFocus(intent Intent) bool {
	switch intent {
	case IntentLocate, IntentFlow, IntentImpact, IntentIsolation:
		return true
	default:
		return false
	}
}

func (r Request) normalized() Request {
	if r.Intent == "" {
		r.Intent = IntentProject
	}
	if r.Level < LevelProject || r.Level > LevelRelationship {
		r.Level = LevelPackage
	}
	if r.MaxFiles <= 0 {
		r.MaxFiles = DefaultMaxFiles
	}
	if r.MaxFiles > MaxHierarchyFiles {
		r.MaxFiles = MaxHierarchyFiles
	}
	if r.MaxSteps <= 0 {
		r.MaxSteps = 8
	}
	if r.MaxSteps > 12 {
		r.MaxSteps = 12
	}
	if r.MinConfidence <= 0 {
		r.MinConfidence = defaultThreshold(r.Intent)
	}
	if r.MinConfidence > 0.99 {
		r.MinConfidence = 0.99
	}
	return r
}

func (r Request) validate() error {
	if _, ok := ParseIntent(string(r.Intent)); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownIntent, r.Intent)
	}
	if RequiresFocus(r.Intent) && r.Focus == "" {
		return fmt.Errorf("%w: intent %s needs a path or symbol", ErrFocusRequired, r.Intent)
	}
	return nil
}

// Cost is the estimated price of one strategy.
//
// It is a relative unit, not a duration: the tables are tuned so that
// preference between strategies is stable across machines. Band is the
// human-facing summary.
type Cost struct {
	Files      int     `json:"files"`
	GraphNodes int     `json:"graph_nodes"`
	GraphEdges int     `json:"graph_edges"`
	ParseWork  int     `json:"parse_work"`
	Units      float64 `json:"units"`
	Band       string  `json:"band"`
}

// Step is one selected strategy and why.
type Step struct {
	Strategy string  `json:"strategy"`
	Version  string  `json:"version"`
	Purpose  string  `json:"purpose"`
	Level    Level   `json:"level"`
	Utility  float64 `json:"utility"`
	Cost     Cost    `json:"cost"`
	Reason   string  `json:"reason"`
}

// Skip names a strategy the planner did not select and the reason.
type Skip struct {
	Strategy string `json:"strategy"`
	Reason   string `json:"reason"`
}

// Plan is the deterministic record of what analysis was chosen and why.
//
// It exists so a consumer can audit cost decisions instead of trusting an
// opaque result. The planner never exposes raw internal scoring as user text;
// the tool layer decides what to show.
type Plan struct {
	Intent              Intent  `json:"intent"`
	Focus               string  `json:"focus,omitempty"`
	Level               Level   `json:"level"`
	Selected            []Step  `json:"selected"`
	Skipped             []Skip  `json:"skipped,omitempty"`
	Estimated           Cost    `json:"estimated"`
	EstimatedConfidence float64 `json:"estimated_confidence"`
	Threshold           float64 `json:"threshold"`
	StopReason          string  `json:"stop_reason"`
	Reason              string  `json:"reason"`
}

// sizeEstimate is the cheap shape the planner knows before parsing.
type sizeEstimate struct {
	Files       int
	Packages    int
	SourceFiles int
	GraphNodes  int
	GraphEdges  int
}

func estimateSize(inventory ast.Inventory) sizeEstimate {
	info := inventory.Snapshot
	nodes := len(inventory.Packages)
	if nodes == 0 {
		nodes = info.Directories
	}
	if nodes == 0 && info.Files > 0 {
		nodes = 1
	}
	// The pre-parse edge estimate is deliberately rough: a planner must not
	// claim precision it has not measured. Two imports per package directory
	// is the conservative middle for module-shaped repositories.
	return sizeEstimate{
		Files:       info.Files,
		Packages:    nodes,
		SourceFiles: info.SourceFiles,
		GraphNodes:  nodes,
		GraphEdges:  nodes * 2,
	}
}

// strategy is one entry in the portfolio.
//
// Tables are package data, not model prompts: selection is deterministic and a
// benchmark can compare the chosen strategy with the alternatives.
type strategy struct {
	name       string
	version    string
	purpose    string
	level      Level
	needsGraph bool
	needsParse bool
	cost       func(sizeEstimate) float64
	relevance  map[Intent]float64
	mandatory  map[Intent]bool
	// applicable refuses a strategy the repository shape makes useless or
	// unaffordable. The reason is recorded in the plan, so a skip is never
	// silent.
	applicable func(size sizeEstimate) (bool, string)
	run        func(*analysisState) error
}

// costBand labels a cost unit.
func costBand(units float64) string {
	switch {
	case units <= 0.5:
		return "low"
	case units <= 1.5:
		return "medium"
	default:
		return "high"
	}
}

func (s *strategy) estimate(size sizeEstimate) Cost {
	units := s.cost(size)
	return Cost{
		Files: size.Files, GraphNodes: size.GraphNodes, GraphEdges: size.GraphEdges,
		ParseWork: size.SourceFiles, Units: units, Band: costBand(units),
	}
}

// relevanceFor returns the strategy's relevance to an intent.
func (s *strategy) relevanceFor(intent Intent) float64 {
	if value, ok := s.relevance[intent]; ok {
		return value
	}
	return 0.2
}

// portfolio is the ordered strategy registry. Order is the mandatory execution
// order; optional strategies are ordered by utility/cost.
func portfolio() []*strategy {
	return []*strategy{
		{
			name: "filesystem-hierarchy", version: "1",
			purpose: "name the project, workspaces, modules, directories, and files with source classes",
			level:   LevelWorkspace,
			cost: func(size sizeEstimate) float64 {
				return 0.2 + float64(size.Files)/10000
			},
			relevance: map[Intent]float64{
				IntentProject: 1, IntentArchitecture: 0.9, IntentLocate: 0.8,
				IntentFlow: 0.7, IntentImpact: 0.7, IntentIsolation: 0.8,
			},
			mandatory: map[Intent]bool{
				IntentProject: true, IntentArchitecture: true, IntentLocate: true,
				IntentFlow: true, IntentImpact: true, IntentIsolation: true,
			},
			run: runFilesystem,
		},
		{
			name: "manifest-topology", version: "1",
			purpose: "read build manifests to confirm the module path and workspace layout",
			level:   LevelModule,
			cost: func(size sizeEstimate) float64 {
				return 0.1
			},
			relevance: map[Intent]float64{
				IntentProject: 1, IntentArchitecture: 0.8, IntentIsolation: 0.5,
			},
			mandatory: map[Intent]bool{IntentProject: true, IntentArchitecture: true},
			run:       runManifest,
		},
		{
			name: "dependency-graph", version: "1",
			purpose: "aggregate import evidence into a language-neutral directory graph",
			level:   LevelPackage, needsGraph: true, needsParse: true,
			cost: func(size sizeEstimate) float64 {
				return 0.3 + float64(size.SourceFiles)/1000 + float64(size.GraphNodes)/2000
			},
			relevance: map[Intent]float64{
				IntentProject: 0.6, IntentArchitecture: 1, IntentLocate: 0.8,
				IntentFlow: 1, IntentImpact: 1, IntentIsolation: 1,
			},
			mandatory: map[Intent]bool{
				IntentArchitecture: true, IntentLocate: true, IntentFlow: true,
				IntentImpact: true, IntentIsolation: true,
			},
			run: runDependencyGraph,
		},
		{
			name: "scc-cycles", version: "1",
			purpose: "compute strongly connected components and representative cycles",
			level:   LevelModule, needsGraph: true,
			cost: func(size sizeEstimate) float64 {
				return 0.2 + float64(size.GraphNodes)/1000
			},
			relevance: map[Intent]float64{
				IntentArchitecture: 0.9, IntentImpact: 0.9, IntentIsolation: 1,
			},
			mandatory: map[Intent]bool{IntentArchitecture: true, IntentIsolation: true},
			run:       runSCC,
		},
		{
			name: "boundary-analysis", version: "1",
			purpose: "derive region and source-class crossings from dependency edges",
			level:   LevelModule, needsGraph: true,
			cost: func(size sizeEstimate) float64 {
				return 0.2 + float64(size.GraphNodes)/2000
			},
			relevance: map[Intent]float64{
				IntentArchitecture: 1, IntentFlow: 0.8, IntentIsolation: 0.9,
			},
			mandatory: map[Intent]bool{IntentArchitecture: true},
			run:       runBoundaries,
		},
		{
			name: "architecture-inference", version: "1",
			purpose: "infer an architecture pattern only from graph shape and cycles",
			level:   LevelModule, needsGraph: true,
			cost: func(size sizeEstimate) float64 {
				return 0.05
			},
			relevance: map[Intent]float64{IntentArchitecture: 1},
			mandatory: map[Intent]bool{IntentArchitecture: true},
			run:       runArchitecture,
		},
		{
			name: "degree-centrality", version: "1",
			purpose: "rank nodes by normalised dependency degree",
			level:   LevelPackage, needsGraph: true,
			cost: func(size sizeEstimate) float64 {
				return 0.1 + float64(size.GraphNodes)/5000
			},
			relevance: map[Intent]float64{
				IntentArchitecture: 0.8, IntentProject: 0.6, IntentImpact: 0.7,
			},
			run: runDegree,
		},
		{
			name: "betweenness-centrality", version: "1",
			purpose: "find bridge nodes on short dependency paths",
			level:   LevelPackage, needsGraph: true,
			cost: func(size sizeEstimate) float64 {
				return 0.1 + float64(size.GraphNodes*size.GraphNodes)/20000
			},
			relevance: map[Intent]float64{IntentArchitecture: 0.7, IntentImpact: 0.6},
			run:       runBetweenness,
		},
		{
			name: ModularityStrategyName, version: ModularityStrategyVersion,
			purpose: "cluster the dependency graph by bounded modularity gain",
			level:   LevelPackage, needsGraph: true,
			cost: func(size sizeEstimate) float64 {
				return 0.4 + float64(size.GraphEdges)/500 + float64(size.GraphNodes)/1000
			},
			relevance: map[Intent]float64{IntentArchitecture: 0.85, IntentProject: 0.5},
			mandatory: map[Intent]bool{IntentArchitecture: true},
			applicable: func(size sizeEstimate) (bool, string) {
				if size.GraphNodes > 800 {
					return false, fmt.Sprintf(
						"estimated graph of %d nodes exceeds the 800-node clustering bound; use module resolution for a smaller graph",
						size.GraphNodes)
				}
				return true, ""
			},
			run: runModularityLabelPropagation,
		},
		{
			name: "representative-sampling", version: "1",
			purpose: "choose explainable evidence coordinates by structural signal",
			level:   LevelPackage, needsGraph: true,
			cost: func(size sizeEstimate) float64 {
				return 0.1 + float64(size.GraphNodes)/3000
			},
			relevance: map[Intent]float64{
				IntentProject: 0.9, IntentArchitecture: 0.7, IntentLocate: 0.6,
			},
			run: runSampling,
		},
		{
			name: "symbol-neighborhood", version: "1",
			purpose: "project references, callers, callers-of-callers, importers, and tests for the focus",
			level:   LevelFile, needsParse: true,
			cost: func(size sizeEstimate) float64 {
				return 0.3 + float64(size.Files)/5000
			},
			relevance: map[Intent]float64{
				IntentLocate: 1, IntentFlow: 1, IntentImpact: 1, IntentIsolation: 1,
			},
			mandatory: map[Intent]bool{
				IntentLocate: true, IntentFlow: true, IntentImpact: true, IntentIsolation: true,
			},
			run: runSymbolNeighborhood,
		},
	}
}

// buildPlan selects strategies for one request.
//
// The algorithm is intentionally small and auditable:
//  1. mandatory strategies for the intent, in registry order;
//  2. optional strategies while the estimated confidence is below the
//     threshold, the step budget remains, and the strategy fits the level
//     budget — ordered by utility/cost;
//  3. everything else is recorded with why it was skipped.
func buildPlan(request Request, inventory ast.Inventory) (Plan, []*strategy) {
	size := estimateSize(inventory)
	plan := Plan{
		Intent:    request.Intent,
		Focus:     request.Focus,
		Level:     request.Level,
		Threshold: request.MinConfidence,
	}
	entries := portfolio()
	selectedNames := map[string]bool{}
	var selected []*strategy

	estimatedConfidence := 0.2
	estimatedUnits := 0.0
	var steps []Step
	for _, entry := range entries {
		if !entry.mandatory[request.Intent] {
			continue
		}
		if ok, why := strategyApplicable(entry, size); !ok {
			plan.Skipped = append(plan.Skipped, Skip{Strategy: entry.name, Reason: why})
			continue
		}
		if entry.level > request.Level {
			plan.Skipped = append(plan.Skipped, Skip{
				Strategy: entry.name,
				Reason: fmt.Sprintf("requires %s resolution but the request caps at %s",
					entry.level, request.Level),
			})
			continue
		}
		selected = append(selected, entry)
		selectedNames[entry.name] = true
		cost := entry.estimate(size)
		estimatedUnits += cost.Units
		estimatedConfidence += 0.18
		steps = append(steps, Step{
			Strategy: entry.name, Version: entry.version, Purpose: entry.purpose,
			Level: entry.level, Utility: entry.relevanceFor(request.Intent), Cost: cost,
			Reason: planStepReason(entry, request, size, cost),
		})
	}

	type candidate struct {
		entry *strategy
		score float64
		cost  Cost
	}
	var candidates []candidate
	for _, entry := range entries {
		if selectedNames[entry.name] {
			continue
		}
		if planHasSkip(plan, entry.name) {
			continue
		}
		relevance := entry.relevanceFor(request.Intent)
		if ok, why := strategyApplicable(entry, size); !ok {
			plan.Skipped = append(plan.Skipped, Skip{Strategy: entry.name, Reason: why})
			continue
		}
		if relevance < 0.45 {
			plan.Skipped = append(plan.Skipped, Skip{
				Strategy: entry.name,
				Reason:   fmt.Sprintf("low relevance to intent %s (%.2f)", request.Intent, relevance),
			})
			continue
		}
		if entry.level > request.Level {
			plan.Skipped = append(plan.Skipped, Skip{
				Strategy: entry.name,
				Reason: fmt.Sprintf("requires %s resolution but the request caps at %s",
					entry.level, request.Level),
			})
			continue
		}
		cost := entry.estimate(size)
		candidates = append(candidates, candidate{
			entry: entry, cost: cost, score: relevance / (0.2 + cost.Units),
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].entry.name < candidates[j].entry.name
	})

	budget := request.MaxSteps - len(selected)
	for _, candidate := range candidates {
		if budget <= 0 {
			plan.Skipped = append(plan.Skipped, Skip{
				Strategy: candidate.entry.name,
				Reason:   fmt.Sprintf("step budget of %d exhausted", request.MaxSteps),
			})
			continue
		}
		if estimatedConfidence >= request.MinConfidence && len(selected) > 0 {
			plan.Skipped = append(plan.Skipped, Skip{
				Strategy: candidate.entry.name,
				Reason: fmt.Sprintf("estimated confidence %.2f already meets threshold %.2f",
					estimatedConfidence, request.MinConfidence),
			})
			continue
		}
		selected = append(selected, candidate.entry)
		selectedNames[candidate.entry.name] = true
		estimatedUnits += candidate.cost.Units
		estimatedConfidence += 0.12
		budget--
		steps = append(steps, Step{
			Strategy: candidate.entry.name, Version: candidate.entry.version,
			Purpose: candidate.entry.purpose, Level: candidate.entry.level,
			Utility: candidate.score, Cost: candidate.cost,
			Reason: planStepReason(candidate.entry, request, size, candidate.cost),
		})
	}
	// A graph-dependent strategy cannot run without the graph provider. If the
	// provider itself was skipped (level budget), drop its dependants too
	// instead of letting them fail at execution time.
	graphAvailable := false
	for _, entry := range selected {
		if entry.name == "dependency-graph" {
			graphAvailable = true
			break
		}
	}
	if !graphAvailable {
		kept := selected[:0]
		allowed := map[string]bool{}
		for _, entry := range selected {
			if entry.needsGraph && entry.name != "dependency-graph" {
				plan.Skipped = append(plan.Skipped, Skip{
					Strategy: entry.name,
					Reason:   "requires the dependency graph, which this request's level budget excludes",
				})
				continue
			}
			kept = append(kept, entry)
			allowed[entry.name] = true
		}
		selected = kept
		keptSteps := steps[:0]
		for _, step := range steps {
			if allowed[step.Strategy] {
				keptSteps = append(keptSteps, step)
			}
		}
		steps = keptSteps
	}
	plan.Selected = steps
	// Recompute from the final selection: a dropped strategy must not keep
	// contributing estimated confidence it will never produce.
	estimatedConfidence = 0.2
	estimatedUnits = 0.0
	for _, entry := range selected {
		if entry.mandatory[request.Intent] {
			estimatedConfidence += 0.18
		} else {
			estimatedConfidence += 0.12
		}
		estimatedUnits += entry.estimate(size).Units
	}
	if estimatedConfidence > 0.95 {
		estimatedConfidence = 0.95
	}
	plan.EstimatedConfidence = estimatedConfidence
	plan.Estimated = Cost{
		Files: size.Files, GraphNodes: size.GraphNodes, GraphEdges: size.GraphEdges,
		ParseWork: size.SourceFiles, Units: estimatedUnits, Band: costBand(estimatedUnits),
	}
	if len(plan.Selected) == 0 {
		plan.StopReason = "no_applicable_strategy"
	} else if estimatedConfidence < request.MinConfidence {
		plan.StopReason = "portfolio_exhausted"
	} else {
		plan.StopReason = "estimated_confidence_reached"
	}
	plan.Reason = planReason(request, inventory, size)
	return plan, selected
}

// strategyApplicable applies the strategy's repository-shape guard.
func strategyApplicable(entry *strategy, size sizeEstimate) (bool, string) {
	if entry.applicable == nil {
		return true, ""
	}
	return entry.applicable(size)
}

// planHasSkip reports whether a strategy already has a recorded skip reason.
func planHasSkip(plan Plan, name string) bool {
	for _, skip := range plan.Skipped {
		if skip.Strategy == name {
			return true
		}
	}
	return false
}

func planStepReason(entry *strategy, request Request, size sizeEstimate, cost Cost) string {
	scale := "small"
	switch {
	case size.Files >= 50000:
		scale = "very large"
	case size.Files >= 5000:
		scale = "large"
	case size.Files >= 100:
		scale = "medium"
	}
	return fmt.Sprintf("%s repository (%d files, %d package directories); %s; estimated cost %s (%.2f units)",
		scale, size.Files, size.Packages, entry.purpose, cost.Band, cost.Units)
}

func planReason(request Request, inventory ast.Inventory, size sizeEstimate) string {
	scope := "complete inventory"
	if !inventory.Snapshot.StructuralComplete {
		scope = "partial inventory (source not fully parsed at planning time)"
	}
	return fmt.Sprintf("intent %s over %d files and %d package directories, %s; level budget %s",
		request.Intent, size.Files, size.Packages, scope, request.Level)
}
