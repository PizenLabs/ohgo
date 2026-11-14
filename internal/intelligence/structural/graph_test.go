package structural

import (
	"reflect"
	"testing"
)

func testGraph() *Graph {
	return NewGraph(
		[]string{"a", "b", "c", "d"},
		[]GraphEdge{
			{From: "a", To: "b"},
			{From: "b", To: "c"},
			{From: "c", To: "a"},
			{From: "c", To: "d"},
		},
	)
}

// TestStronglyConnectedComponentsAreExact pins the cycle boundary: a, b, c form
// one component and d is outside it. Reporting d as cyclic would make every
// impact claim wrong.
func TestStronglyConnectedComponentsAreExact(t *testing.T) {
	graph := testGraph()
	components := graph.SCCs()
	want := [][]string{{"a", "b", "c"}, {"d"}}
	if !reflect.DeepEqual(components, want) {
		t.Fatalf("SCCs = %v, want %v", components, want)
	}
	cyclic := graph.CyclicComponents()
	if len(cyclic) != 1 || !reflect.DeepEqual(cyclic[0], []string{"a", "b", "c"}) {
		t.Fatalf("cyclic components = %v", cyclic)
	}
	cycles := graph.RepresentativeCycles(3)
	if len(cycles) != 1 || !reflect.DeepEqual(cycles[0], []string{"a", "b", "c"}) {
		t.Fatalf("representative cycles = %v", cycles)
	}
}

// TestCondensationIsAcyclic pins that the reduced graph can carry a layering
// claim even when the raw graph cannot.
func TestCondensationIsAcyclic(t *testing.T) {
	condensed := testGraph().Condensation()
	if got := condensed.Nodes(); !reflect.DeepEqual(got, []string{"a", "d"}) {
		t.Fatalf("condensed nodes = %v", got)
	}
	if _, acyclic := condensed.Layering(); !acyclic {
		t.Fatal("condensation is cyclic")
	}
	if _, acyclic := testGraph().Layering(); acyclic {
		t.Fatal("cyclic graph reported an acyclic layering")
	}
}

// TestLayeringUsesLongestDepth pins the layering metric: depth is the longest
// path from a source, not the shortest, so a diamond is not flattened.
func TestLayeringUsesLongestDepth(t *testing.T) {
	graph := NewGraph(
		[]string{"a", "b", "c", "d"},
		[]GraphEdge{
			{From: "a", To: "b"}, {From: "a", To: "c"},
			{From: "b", To: "d"}, {From: "c", To: "d"},
		},
	)
	layers, ok := graph.Layering()
	if !ok {
		t.Fatal("acyclic graph reported a cycle")
	}
	if len(layers) != 3 {
		t.Fatalf("layers = %v, want 3", layers)
	}
	if !reflect.DeepEqual(layers[2].Nodes, []string{"d"}) {
		t.Fatalf("deepest layer = %v", layers[2].Nodes)
	}
}

// TestBetweennessRefusesOversizedGraphs pins bounded analysis: the strategy is
// skipped with a report, never run unbounded.
func TestBetweennessRefusesOversizedGraphs(t *testing.T) {
	graph := NewGraph([]string{"a", "b", "c"}, []GraphEdge{{From: "a", To: "b"}})
	if _, ok := graph.Betweenness(BetweennessOptions{MaxNodes: 2}); ok {
		t.Fatal("betweenness ran above its node bound")
	}
	ranking, ok := graph.Betweenness(BetweennessOptions{MaxNodes: 10, MaxEdges: 10})
	if !ok {
		t.Fatal("betweenness refused a graph inside its bounds")
	}
	if len(ranking) != 3 {
		t.Fatalf("ranking = %+v", ranking)
	}
}

// TestDependencyDirectionIsPreserved pins that fan-in and fan-out are not
// interchangeable: b depends on c, so c has a dependent and b has a dependency.
func TestDependencyDirectionIsPreserved(t *testing.T) {
	graph := NewGraph(
		[]string{"a", "b", "c"},
		[]GraphEdge{{From: "a", To: "b"}, {From: "b", To: "c"}},
	)
	if got := graph.FanOut("a"); got != 1 {
		t.Errorf("fan-out(a) = %d, want 1", got)
	}
	if got := graph.FanIn("c"); got != 1 {
		t.Errorf("fan-in(c) = %d, want 1", got)
	}
	if got := graph.Dependents("c"); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("dependents(c) = %v", got)
	}
	if got := graph.Dependencies("a"); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("dependencies(a) = %v", got)
	}
}
