package structural

import (
	"sort"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// SchemaVersion identifies the serialized model shape. A cache entry written by
// a different schema is never trusted.
const SchemaVersion = "structural/v1"

// AnalyzerVersion identifies the analysis semantics: planner tables, strategy
// behaviour, and evidence rules. Bumping it invalidates cached analyses even
// when the repository and schema are unchanged.
const AnalyzerVersion = "2026-11-14.2"

// Level is one resolution of the structural model.
//
// Higher numbers are more detailed and more expensive. The planner stops at the
// lowest level that answers the task; a project question never materialises
// symbols.
type Level int

// Model resolutions.
//
// The numbering starts at 1 so the zero value means "unset". A zero Level must
// default to the cheapest sufficient resolution, never silently to the project
// resolution: the two mistakes produce very different analyses.
const (
	LevelProject Level = iota + 1
	LevelWorkspace
	LevelModule
	LevelPackage
	LevelFile
	LevelSymbol
	LevelRelationship
)

// String returns the stable label used in plans, cache keys, and tool output.
func (l Level) String() string {
	switch l {
	case LevelProject:
		return "project"
	case LevelWorkspace:
		return "workspace"
	case LevelModule:
		return "module"
	case LevelPackage:
		return "package"
	case LevelFile:
		return "file"
	case LevelSymbol:
		return "symbol"
	default:
		return "relationship"
	}
}

// ParseLevel accepts the stable labels a tool may receive from a model.
func ParseLevel(value string) (Level, bool) {
	switch value {
	case "project":
		return LevelProject, true
	case "workspace":
		return LevelWorkspace, true
	case "module":
		return LevelModule, true
	case "package", "directory":
		return LevelPackage, true
	case "file":
		return LevelFile, true
	case "symbol":
		return LevelSymbol, true
	case "relationship":
		return LevelRelationship, true
	default:
		return 0, false
	}
}

// SourceClass distinguishes what a path is used for. Structural evidence from a
// generated file or a vendored dependency must not be weighted like authored
// production code.
type SourceClass string

// Source classes. ClassUnknown is deliberate: an unclassifiable path is not
// silently counted as production.
const (
	ClassProduction    SourceClass = "production"
	ClassTest          SourceClass = "test"
	ClassGenerated     SourceClass = "generated"
	ClassVendor        SourceClass = "vendor"
	ClassBuildArtifact SourceClass = "build_artifact"
	ClassDocumentation SourceClass = "documentation"
	ClassFixture       SourceClass = "fixture"
	ClassExample       SourceClass = "example"
	ClassTooling       SourceClass = "tooling"
	ClassUnknown       SourceClass = "unknown"
)

// Authored reports a class whose source a developer wrote and a consumer may
// treat as the project's own code.
func (c SourceClass) Authored() bool {
	return c == ClassProduction || c == ClassTooling
}

// NodeWorkspace is the multi-repository boundary kind. The ast vocabulary has
// no workspace node because it owns one repository per index; a project with
// nested repositories needs the extra resolution.
const NodeWorkspace ast.NodeKind = "workspace"

// Additional relationship kinds the generic model needs beyond the ast
// vocabulary. They extend the single ast.EdgeKind type rather than creating a
// second edge vocabulary.
const (
	EdgeBelongsTo     ast.EdgeKind = "BELONGS_TO"
	EdgeConfiguredBy  ast.EdgeKind = "CONFIGURED_BY"
	EdgeGeneratedFrom ast.EdgeKind = "GENERATED_FROM"
	EdgeExposes       ast.EdgeKind = "EXPOSES"
	EdgeConsumes      ast.EdgeKind = "CONSUMES"
	EdgeCrosses       ast.EdgeKind = "CROSSES"
)

// Node is one structural entity at one resolution.
//
// ID is derived from the path (or project identity) and is stable for a given
// snapshot. Parent expresses containment; it is not duplicated as an edge,
// because a second containment representation is a second source of truth.
type Node struct {
	ID        string         `json:"id"`
	Kind      ast.NodeKind   `json:"kind"`
	Level     Level          `json:"level"`
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	Parent    string         `json:"parent,omitempty"`
	Class     SourceClass    `json:"class,omitempty"`
	Language  ast.Language   `json:"language,omitempty"`
	Languages []ast.Language `json:"languages,omitempty"`
	Files     int            `json:"files"`
	TestFiles int            `json:"test_files,omitempty"`
	Symbols   int            `json:"symbols,omitempty"`
	// Packages lists the observed package/namespace names when the node is a
	// directory shared by several identities.
	Packages []string `json:"packages,omitempty"`
	// Classes counts members by source class. It lets a consumer see that a
	// directory is half generated without expanding it.
	Classes map[SourceClass]int `json:"classes,omitempty"`
	// Classification is the evidence behind Class. For an inferred class it is
	// INFERRED, not OBSERVED, so a consumer cannot cite a path convention as
	// proof that code is authored.
	Classification Evidence `json:"classification"`
	Evidence       Evidence `json:"evidence"`
}

// Edge is one structurally proved relationship between two nodes.
type Edge struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Kind     ast.EdgeKind `json:"kind"`
	Language ast.Language `json:"language,omitempty"`
	Source   bool         `json:"source,omitempty"`
	Test     bool         `json:"test,omitempty"`
	Evidence Evidence     `json:"evidence"`
}

// Boundary is an observed crossing between two structural regions.
//
// It is derived from edges, never from directory names. A boundary with a zero
// crossing count is not emitted: an empty boundary would be an inference.
type Boundary struct {
	From      string       `json:"from"`
	To        string       `json:"to"`
	Kind      ast.EdgeKind `json:"kind"`
	Crossings int          `json:"crossings"`
	Evidence  Evidence     `json:"evidence"`
}

// Community is one grouping proposed by a clustering strategy.
type Community struct {
	ID       string   `json:"id"`
	Members  []string `json:"members"`
	Internal int      `json:"internal"`
	External int      `json:"external"`
	Evidence Evidence `json:"evidence"`
}

// Sample is one representative evidence coordinate chosen by a sampling
// strategy. Reason names the signal, so a sample is explainable rather than
// "the first N".
type Sample struct {
	Path     string   `json:"path"`
	NodeID   string   `json:"node_id"`
	Reason   string   `json:"reason"`
	Score    float64  `json:"score"`
	Evidence Evidence `json:"evidence"`
}

// Architecture is one evidence-backed architecture inference.
//
// Pattern is allowed to be "not-established". A repository that does not match
// a pattern must not be forced into one, and name hints are recorded separately
// so a consumer can see they were not treated as proof.
type Architecture struct {
	Pattern      string        `json:"pattern"`
	Confidence   float64       `json:"confidence"`
	State        EvidenceState `json:"state"`
	Basis        string        `json:"basis"`
	Evidence     []string      `json:"evidence,omitempty"`
	Hints        []string      `json:"name_hints,omitempty"`
	Alternatives []string      `json:"alternatives,omitempty"`
}

// Coverage states what the analysis actually observed.
//
// Confidence is derived from these fields; it is not an independent opinion.
// Complete requires every axis: inventory, parse, dependency projection, and no
// truncation.
type Coverage struct {
	Level            Level         `json:"level"`
	Files            int           `json:"files"`
	Materialized     int           `json:"materialized_files"`
	Parsed           int           `json:"parsed_files"`
	Pending          int           `json:"pending_files"`
	Failed           int           `json:"failed_files"`
	GraphNodes       int           `json:"graph_nodes"`
	GraphEdges       int           `json:"graph_edges"`
	Unresolved       int           `json:"unresolved_dependencies"`
	DependenciesSeen bool          `json:"dependencies_seen"`
	Truncated        bool          `json:"truncated"`
	Reason           string        `json:"reason,omitempty"`
	State            EvidenceState `json:"state"`
	Confidence       float64       `json:"confidence"`
}

// Complete reports that every claim the analysis could make is supported.
func (c Coverage) Complete() bool {
	return c.State == StateObserved || c.State == StateVerified
}

// Issue is one explicit analysis failure or limitation.
//
// Unknown must stay unknown: every condition that prevents a claim is recorded
// here instead of being folded into an empty list or a false negative.
type Issue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Snapshot is the multi-resolution structural model of one repository state.
//
// It is derived, read-only, and deterministic for identical input. Nodes are
// ordered by level then ID; a consumer must not depend on map iteration.
type Snapshot struct {
	SchemaVersion string           `json:"schema_version"`
	Version       string           `json:"analyzer_version"`
	ProjectID     string           `json:"project_id"`
	Source        ast.SnapshotInfo `json:"source"`
	Level         Level            `json:"level"`
	Nodes         []Node           `json:"nodes"`
	Coverage      Coverage         `json:"coverage"`
	Issues        []Issue          `json:"issues,omitempty"`

	byID map[string]int
}

// Node returns the node with the given ID.
func (s *Snapshot) Node(id string) (Node, bool) {
	if s == nil {
		return Node{}, false
	}
	index, ok := s.byID[id]
	if !ok || index < 0 || index >= len(s.Nodes) {
		return Node{}, false
	}
	return s.Nodes[index], true
}

// Children returns the direct children of a node, ordered by level then ID.
func (s *Snapshot) Children(id string) []Node {
	if s == nil {
		return nil
	}
	var out []Node
	for _, node := range s.Nodes {
		if node.Parent == id {
			out = append(out, node)
		}
	}
	return out
}

// AtLevel returns every node at one resolution, ordered by ID.
func (s *Snapshot) AtLevel(level Level) []Node {
	if s == nil {
		return nil
	}
	var out []Node
	for _, node := range s.Nodes {
		if node.Level == level {
			out = append(out, node)
		}
	}
	return out
}

// index rebuilds the ID lookup. It is called once after the node list is final.
func (s *Snapshot) index() {
	s.byID = make(map[string]int, len(s.Nodes))
	for i, node := range s.Nodes {
		s.byID[node.ID] = i
	}
}

// sortNodes applies the one deterministic node order.
func sortNodes(nodes []Node) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Level != nodes[j].Level {
			return nodes[i].Level < nodes[j].Level
		}
		return nodes[i].ID < nodes[j].ID
	})
}
