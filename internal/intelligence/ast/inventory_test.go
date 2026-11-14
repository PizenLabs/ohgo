package ast

import (
	"context"
	"reflect"
	"testing"
)

// newAdaptiveIndex builds the production shape: inventory first, declarations
// only when a query asks for them.
func newAdaptiveIndex(t *testing.T, root string) *Index {
	t.Helper()
	idx, err := NewIndexWithOptions(root, IndexOptions{Adaptive: true})
	if err != nil {
		t.Fatalf("NewIndexWithOptions: %v", err)
	}
	return idx
}

func dependencyEdgeSet(result DependencyResult) map[string]DependencyEdge {
	out := map[string]DependencyEdge{}
	for _, edge := range result.Edges {
		out[edge.From+"->"+edge.To] = edge
	}
	return out
}

// TestInventoryMetadataDoesNotParseAndDependenciesDo pins the activation
// boundary that makes an architecture overview cheap: the inventory names
// files without running an adapter, and the first dependency query is what
// parses them. A caller that reads a metadata inventory as a parsed snapshot
// would report an empty dependency graph as fact.
func TestInventoryMetadataDoesNotParseAndDependenciesDo(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() {}\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
	})
	ctx := context.Background()
	idx := newAdaptiveIndex(t, root)

	metadata, err := idx.Inventory(ctx, InventoryQuery{})
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if metadata.TotalFiles != 3 {
		t.Fatalf("inventory files = %d, want 3 (go.mod, a/a.go, b/b.go)", metadata.TotalFiles)
	}
	if metadata.Snapshot.StructuralComplete {
		t.Error("metadata inventory claimed structural completeness without parsing")
	}
	if metadata.Snapshot.PendingFiles == 0 {
		t.Error("metadata inventory reported no pending source files")
	}

	graph, err := idx.Dependencies(ctx, DependencyQuery{})
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	if !graph.Complete {
		t.Fatalf("dependency projection incomplete after a parsing query: %+v", graph)
	}
	edges := dependencyEdgeSet(graph)
	edge, ok := edges["a->b"]
	if !ok {
		t.Fatalf("missing a->b dependency: %+v", graph.Edges)
	}
	if !edge.Source || edge.Test {
		t.Errorf("a->b provenance = source:%v test:%v, want production only", edge.Source, edge.Test)
	}
	if edge.Witness.Path != "a/a.go" || edge.Witness.Line != 3 {
		t.Errorf("witness = %+v, want a/a.go:3", edge.Witness)
	}
}

// TestDependencyProjectionSeparatesTestImportsAndUnknowns pins the two claims a
// consumer makes from this graph: a test-only import is not a production
// dependency, and an unresolved module-local import is disclosed rather than
// dropped.
func TestDependencyProjectionSeparatesTestImportsAndUnknowns(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go":      "package a\n\nfunc A() {}\n",
		"a/a_test.go": "package a\n\nimport \"example.com/app/c\"\n",
		"b/b.go":      "package b\n\nimport \"example.com/app/missing\"\n",
		"c/c.go":      "package c\n\nfunc C() {}\n",
	})
	result, err := mustIndex(t, root).Dependencies(context.Background(), DependencyQuery{})
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	edges := dependencyEdgeSet(result)
	testEdge, ok := edges["a->c"]
	if !ok {
		t.Fatalf("missing test-only a->c dependency: %+v", result.Edges)
	}
	if !testEdge.Test || testEdge.Source {
		t.Errorf("a->c provenance = source:%v test:%v, want test only", testEdge.Source, testEdge.Test)
	}
	if testEdge.Witness.Test != true || testEdge.Witness.Path != "a/a_test.go" {
		t.Errorf("test witness = %+v", testEdge.Witness)
	}
	if _, ok := edges["a->a"]; ok {
		t.Error("self dependency was projected")
	}
	found := false
	for _, unresolved := range result.Unresolved {
		if unresolved.From == "b" && unresolved.Import == "example.com/app/missing" {
			found = true
			if unresolved.Reason == "" {
				t.Error("unresolved import has no reason")
			}
		}
	}
	if !found {
		t.Fatalf("unresolved module-local import was dropped: %+v", result.Unresolved)
	}
	if result.Complete {
		t.Error("projection claimed completeness with an unresolved module-local import")
	}
}

// TestDependencyProjectionKeepsBothProvenances pins that an edge proved in both
// production and test source reports both. Collapsing it to one flag would hide
// a production dependency or overstate test-only coupling.
func TestDependencyProjectionKeepsBothProvenances(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go":      "package a\n\nimport \"example.com/app/c\"\n\nfunc A() {}\n",
		"a/a_test.go": "package a\n\nimport (\n\t\"example.com/app/c\"\n\t\"testing\"\n)\n",
		"c/c.go":      "package c\n\nfunc C() {}\n",
	})
	result, err := mustIndex(t, root).Dependencies(context.Background(), DependencyQuery{})
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	edge, ok := dependencyEdgeSet(result)["a->c"]
	if !ok {
		t.Fatalf("missing a->c dependency: %+v", result.Edges)
	}
	if !edge.Source || !edge.Test {
		t.Errorf("provenance = source:%v test:%v, want both", edge.Source, edge.Test)
	}
	if edge.Witness.Test {
		t.Errorf("aggregate witness = %+v, want the non-test witness", edge.Witness)
	}
	// "testing" is an external standard-library import, not a structural gap.
	if len(result.Unresolved) != 0 {
		t.Fatalf("external import reported as unresolved: %+v", result.Unresolved)
	}
	if !result.Complete {
		t.Fatalf("projection claimed a gap: reason=%q", result.Reason)
	}
}

// TestDependencyProjectionTruncatesExplicitly pins that a bounded projection
// keeps whole-snapshot totals and names the bound instead of silently returning
// a smaller graph as if it were the whole one.
func TestDependencyProjectionTruncatesExplicitly(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport (\n\t\"example.com/app/b\"\n\t\"example.com/app/c\"\n)\n\nfunc A() {}\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
		"c/c.go": "package c\n\nfunc C() {}\n",
	})
	result, err := mustIndex(t, root).Dependencies(context.Background(), DependencyQuery{MaxEdges: 1})
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	if !result.Truncated || result.Reason != "edge_limit" {
		t.Fatalf("truncated=%v reason=%q, want edge_limit", result.Truncated, result.Reason)
	}
	if result.TotalEdges != 2 || len(result.Edges) != 1 {
		t.Errorf("edges = %d of %d, want 1 of 2", len(result.Edges), result.TotalEdges)
	}
	if result.Complete {
		t.Error("truncated projection claimed completeness")
	}
}

// TestDependencyProjectionIsDeterministic pins stable ordering across repeated
// queries. A structural summary that reorders between calls cannot be cached,
// diffed, or compared.
func TestDependencyProjectionIsDeterministic(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport (\n\t\"example.com/app/b\"\n\t\"example.com/app/c\"\n)\n\nfunc A() {}\n",
		"b/b.go": "package b\n\nimport \"example.com/app/c\"\n\nfunc B() {}\n",
		"c/c.go": "package c\n\nfunc C() {}\n",
	})
	idx := mustIndex(t, root)
	first, err := idx.Dependencies(context.Background(), DependencyQuery{})
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	second, err := idx.Dependencies(context.Background(), DependencyQuery{})
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	if !reflect.DeepEqual(first.Edges, second.Edges) || !reflect.DeepEqual(first.Nodes, second.Nodes) {
		t.Error("dependency projection is not deterministic")
	}
}

// TestInventoryTruncationKeepsWholeTreeCounts pins that a row bound never
// rewrites the snapshot totals a consumer uses to qualify a claim.
func TestInventoryTruncationKeepsWholeTreeCounts(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nfunc A() {}\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
		"c/c.go": "package c\n\nfunc C() {}\n",
	})
	inventory, err := mustIndex(t, root).Inventory(context.Background(), InventoryQuery{MaxFiles: 2})
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if !inventory.FilesTruncated || inventory.Reason != "file_limit" {
		t.Fatalf("truncated=%v reason=%q", inventory.FilesTruncated, inventory.Reason)
	}
	if len(inventory.Files) != 2 || inventory.TotalFiles != 4 {
		t.Errorf("files = %d of %d, want 2 of 4", len(inventory.Files), inventory.TotalFiles)
	}
	if inventory.Snapshot.Files != 4 {
		t.Errorf("snapshot files = %d, want the whole-tree total 4", inventory.Snapshot.Files)
	}
}
