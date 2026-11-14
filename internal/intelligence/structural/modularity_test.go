package structural

import (
	"reflect"
	"testing"
)

// plantedClusters is two triangles joined by one bridge: the modularity answer
// is unambiguous, so the test measures the algorithm rather than the fixture.
func plantedClusters() *Graph {
	return NewGraph(
		[]string{"a1", "a2", "a3", "b1", "b2", "b3"},
		[]GraphEdge{
			{From: "a1", To: "a2"}, {From: "a2", To: "a3"}, {From: "a3", To: "a1"},
			{From: "b1", To: "b2"}, {From: "b2", To: "b3"}, {From: "b3", To: "b1"},
			{From: "a1", To: "b1"},
		},
	)
}

// TestModularityLabelPropagationSeparatesPlantedClusters pins cluster quality on a graph with a known
// answer.
func TestModularityLabelPropagationSeparatesPlantedClusters(t *testing.T) {
	partition := ModularityClustering(plantedClusters(), ModularitySweepBound)
	if len(partition.Communities) != 2 {
		t.Fatalf("communities = %+v, want 2", partition.Communities)
	}
	got := [][]string{
		partition.Communities[0].Members,
		partition.Communities[1].Members,
	}
	want := [][]string{{"a1", "a2", "a3"}, {"b1", "b2", "b3"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("communities = %v, want %v", got, want)
	}
	if partition.Modularity <= 0 {
		t.Errorf("modularity = %.3f, want positive", partition.Modularity)
	}
}

// TestModularityLabelPropagationIsDeterministic pins stable output, which is what makes the result
// cacheable and diffable.
func TestModularityLabelPropagationIsDeterministic(t *testing.T) {
	first := ModularityClustering(plantedClusters(), ModularitySweepBound)
	second := ModularityClustering(plantedClusters(), ModularitySweepBound)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("modularity clustering is not deterministic")
	}
}

// TestModularityLabelPropagationDoesNotInventCohesion pins the empty-evidence answer: with no edges
// there is no basis to merge nodes, so each stays its own community.
func TestModularityLabelPropagationDoesNotInventCohesion(t *testing.T) {
	partition := ModularityClustering(NewGraph([]string{"x", "y"}, nil), ModularitySweepBound)
	if len(partition.Communities) != 2 {
		t.Fatalf("communities = %d, want 2", len(partition.Communities))
	}
	if partition.Modularity != 0 {
		t.Errorf("modularity = %.3f, want 0", partition.Modularity)
	}
	empty := ModularityClustering(NewGraph(nil, nil), ModularitySweepBound)
	if len(empty.Communities) != 0 {
		t.Fatalf("empty graph produced %d communities", len(empty.Communities))
	}
}

// TestModularityLabelPropagationSweepBoundIsEnforced pins the cost bound the strategy name promises.
func TestModularityLabelPropagationSweepBoundIsEnforced(t *testing.T) {
	partition := ModularityClustering(plantedClusters(), 1000)
	if partition.Sweeps > ModularitySweepBound {
		t.Fatalf("sweeps = %d, want at most %d", partition.Sweeps, ModularitySweepBound)
	}
}

// TestPartitionModularityRanksPartitions pins that the benchmark metric actually
// distinguishes a cluster-aligned partition from an arbitrary one.
func TestPartitionModularityRanksPartitions(t *testing.T) {
	graph := plantedClusters()
	good := ModularityClustering(graph, ModularitySweepBound)
	bad := []Community{
		{ID: "x", Members: []string{"a1", "a2", "a3", "b1", "b2", "b3"}},
	}
	if PartitionModularity(graph, good.Communities) <= PartitionModularity(graph, bad) {
		t.Error("modularity did not prefer the cluster-aligned partition")
	}
}
