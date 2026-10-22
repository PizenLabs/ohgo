package ast

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// graphFixture writes a module-shaped repository for graph tests.
func graphFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/app\n\ngo 1.21\n"
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustGraph(t *testing.T, idx *Index, query GraphQuery) GraphResult {
	t.Helper()
	result, err := idx.Graph(context.Background(), query)
	if err != nil {
		t.Fatalf("Graph(%+v): %v", query, err)
	}
	return result
}

func graphEdgeSet(result GraphResult) map[string]GraphEdge {
	out := map[string]GraphEdge{}
	for _, edge := range result.Edges {
		out[edge.From+"->"+edge.To] = edge
	}
	return out
}

func graphNodeSet(result GraphResult) map[string]GraphNode {
	out := map[string]GraphNode{}
	for _, node := range result.Nodes {
		out[node.Path] = node
	}
	return out
}

func TestGraphFollowsImportsAndReverseDependents(t *testing.T) {
	// The smallest useful relationship: A imports B, B's test imports C.
	// Outgoing and incoming traversal must agree on the same stored edges,
	// preserve the source/test distinction, and not invent an edge to an
	// external package.
	root := graphFixture(t, map[string]string{
		"a/a.go":      "package a\n\nimport \"example.com/app/b\"\n\nfunc A() {}\n",
		"b/b.go":      "package b\n\nimport \"fmt\"\n\nfunc B() {}\n",
		"b/b_test.go": "package b\n\nimport \"example.com/app/c\"\n",
		"c/c.go":      "package c\n\nfunc C() {}\n",
	})
	idx := mustIndex(t, root)

	forward := mustGraph(t, idx, GraphQuery{Package: "a", Relation: RelationImports, Depth: 2})
	if !forward.Found {
		t.Fatalf("package a not found: %+v", forward)
	}
	nodes := graphNodeSet(forward)
	for _, want := range []string{"a", "b", "c"} {
		if _, ok := nodes[want]; !ok {
			t.Errorf("forward traversal omitted %q: %+v", want, forward.Nodes)
		}
	}
	edges := graphEdgeSet(forward)
	if edge, ok := edges["a->b"]; !ok || !edge.Source || edge.Test {
		t.Errorf("a->b = %+v, want source-only", edge)
	} else if edge.SourceFile != "a/a.go" || edge.SourceLine != 3 {
		t.Errorf("a->b evidence = %s:%d, want a/a.go:3", edge.SourceFile, edge.SourceLine)
	}
	if edge, ok := edges["b->c"]; !ok || edge.Source || !edge.Test {
		t.Errorf("b->c = %+v, want test-only", edge)
	} else if edge.TestFile != "b/b_test.go" || edge.TestLine != 3 {
		t.Errorf("b->c evidence = %s:%d, want b/b_test.go:3", edge.TestFile, edge.TestLine)
	}
	if nodes["b"].ExternalImports != 1 {
		t.Errorf("b external imports = %d, want 1", nodes["b"].ExternalImports)
	}

	reverse := mustGraph(t, idx, GraphQuery{Package: "c", Relation: RelationImportedBy, Depth: 2})
	if !reverse.Found {
		t.Fatalf("package c not found: %+v", reverse)
	}
	reverseEdges := graphEdgeSet(reverse)
	if _, ok := reverseEdges["b->c"]; !ok {
		t.Errorf("reverse traversal omitted b->c: %+v", reverse.Edges)
	}
	if _, ok := reverseEdges["a->b"]; !ok {
		t.Errorf("reverse traversal omitted a->b: %+v", reverse.Edges)
	}
}

func TestGraphReportsExternalAndUnresolvedImports(t *testing.T) {
	// External imports are counted, not materialised. A module-local import
	// with no indexed target is explicit rather than silently dropped.
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport (\n\t\"fmt\"\n\t\"example.com/app/missing\"\n)\n\nfunc A() {}\n",
	})
	idx := mustIndex(t, root)

	result := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})
	nodes := graphNodeSet(result)
	if nodes["a"].ExternalImports != 1 {
		t.Errorf("external imports = %d, want 1", nodes["a"].ExternalImports)
	}
	if len(result.Edges) != 0 {
		t.Errorf("external or unresolved imports produced edges: %+v", result.Edges)
	}
	if len(result.Unresolved) != 1 {
		t.Fatalf("unresolved = %+v, want one", result.Unresolved)
	}
	if result.Unresolved[0].Import != "example.com/app/missing" {
		t.Errorf("unresolved import = %q", result.Unresolved[0].Import)
	}
	if result.Unresolved[0].Reason == "" {
		t.Error("unresolved relationship has no reason")
	}
}

func TestGraphPreservesSelectorAmbiguity(t *testing.T) {
	// A package name and a symbol name can each resolve to more than one
	// directory. The graph returns every candidate and chooses none.
	root := graphFixture(t, map[string]string{
		"x1/x.go": "package same\n\nfunc X() {}\n",
		"x2/x.go": "package same\n\nfunc X() {}\n",
		"y1/y.go": "package y\n\nfunc Dup() {}\n",
		"y2/y.go": "package y\n\nfunc Dup() {}\n",
	})
	idx := mustIndex(t, root)

	byPackage := mustGraph(t, idx, GraphQuery{Package: "same"})
	if !byPackage.Ambiguous {
		t.Fatalf("package selector did not report ambiguity: %+v", byPackage)
	}
	if !reflect.DeepEqual(byPackage.Candidates, []string{"x1", "x2"}) {
		t.Errorf("candidates = %v, want [x1 x2]", byPackage.Candidates)
	}

	bySymbol := mustGraph(t, idx, GraphQuery{Symbol: "Dup"})
	if !bySymbol.Ambiguous {
		t.Fatalf("symbol selector did not report ambiguity: %+v", bySymbol)
	}
	if !reflect.DeepEqual(bySymbol.Candidates, []string{"y1", "y2"}) {
		t.Errorf("candidates = %v, want [y1 y2]", bySymbol.Candidates)
	}

	// A path is a coordinate, not a name: it disambiguates explicitly.
	byPath := mustGraph(t, idx, GraphQuery{Path: "x1"})
	if !byPath.Found || byPath.Ambiguous {
		t.Fatalf("path selector did not resolve x1: %+v", byPath)
	}
	if len(byPath.Nodes) != 1 || byPath.Nodes[0].Path != "x1" {
		t.Errorf("path result = %+v", byPath.Nodes)
	}
}

func TestGraphPreservesPackageIdentityAmbiguity(t *testing.T) {
	// A directory whose files disagree on package name is not a uniquely
	// named package. Its edges are still declarations, but the identity is
	// marked ambiguous on both the node and the edge.
	root := graphFixture(t, map[string]string{
		"m/one.go":      "package one\n\nfunc One() {}\n",
		"m/two.go":      "package two\n\nfunc Two() {}\n",
		"consumer/c.go": "package consumer\n\nimport \"example.com/app/m\"\n",
	})
	idx := mustIndex(t, root)

	result := mustGraph(t, idx, GraphQuery{Package: "consumer", Depth: 1})
	nodes := graphNodeSet(result)
	if nodes["m"].Ambiguous != true {
		t.Errorf("target package identity ambiguity was lost: %+v", nodes["m"])
	}
	if len(nodes["m"].Packages) != 2 {
		t.Errorf("packages = %v, want both names", nodes["m"].Packages)
	}
	edge := graphEdgeSet(result)["consumer->m"]
	if !edge.TargetAmbiguous {
		t.Errorf("edge target ambiguity was lost: %+v", edge)
	}
}

func TestGraphBoundsTraversal(t *testing.T) {
	// Every bound is explicit: depth is a requested scope, node and edge
	// limits are resource cuts. None may be reported as an unbounded complete
	// answer.
	root := graphFixture(t, map[string]string{
		"hub/h.go": "package hub\n\nimport (\n\t\"example.com/app/l1\"\n\t\"example.com/app/l2\"\n)\n",
		"l1/l.go":  "package l1\n\nimport \"example.com/app/l3\"\n",
		"l2/l.go":  "package l2\n\nimport \"example.com/app/l4\"\n",
		"l3/l.go":  "package l3\n",
		"l4/l.go":  "package l4\n",
	})
	idx := mustIndex(t, root)

	depthLimited := mustGraph(t, idx, GraphQuery{Package: "hub", Depth: 1})
	if !depthLimited.Truncated || depthLimited.Reason != "depth" {
		t.Errorf("depth-bound result = truncated %v reason %q, want true/depth", depthLimited.Truncated, depthLimited.Reason)
	}

	nodeLimited := mustGraph(t, idx, GraphQuery{Package: "hub", Depth: 4, Limit: 2, EdgeLimit: 10})
	if !nodeLimited.Truncated || nodeLimited.Reason != "node_limit" {
		t.Errorf("node-bound result = truncated %v reason %q, want true/node_limit", nodeLimited.Truncated, nodeLimited.Reason)
	}
	if len(nodeLimited.Nodes) != 2 {
		t.Errorf("nodes = %d, want the 2-node limit", len(nodeLimited.Nodes))
	}

	edgeLimited := mustGraph(t, idx, GraphQuery{Package: "hub", Depth: 2, Limit: 10, EdgeLimit: 1})
	if !edgeLimited.Truncated || edgeLimited.Reason != "edge_limit" {
		t.Errorf("edge-bound result = truncated %v reason %q, want true/edge_limit", edgeLimited.Truncated, edgeLimited.Reason)
	}
	if len(edgeLimited.Edges) != 1 {
		t.Errorf("edges = %d, want the 1-edge limit", len(edgeLimited.Edges))
	}

	for _, query := range []GraphQuery{
		{Package: "hub", Depth: GraphMaxDepth + 1},
		{Package: "hub", Limit: GraphMaxLimit + 1},
		{Package: "hub", EdgeLimit: GraphMaxEdges + 1},
	} {
		if _, err := idx.Graph(context.Background(), query); err == nil {
			t.Errorf("invalid bound %+v was accepted", query)
		}
	}
}

func TestGraphFreshness(t *testing.T) {
	// Every change that affects an edge must invalidate it: a same-size
	// import rewrite, a created file, a deleted file, a module-path change,
	// and a parse failure. Stale edges must never be served as current.
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n",
		"b/b.go": "package b\n",
		"c/c.go": "package c\n",
	})
	aPath := filepath.Join(root, "a", "a.go")
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(aPath, old, old); err != nil {
		t.Fatal(err)
	}
	idx := mustIndex(t, root)

	if edges := graphEdgeSet(mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})); edges["a->b"].To == "" {
		t.Fatal("precondition: a->b is not indexed")
	}

	// Same size, different target, different mtime.
	if err := os.WriteFile(aPath, []byte("package a\n\nimport \"example.com/app/c\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterRewrite := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})
	edges := graphEdgeSet(afterRewrite)
	if _, ok := edges["a->c"]; !ok {
		t.Errorf("same-size import rewrite was not discovered: %+v", afterRewrite.Edges)
	}
	if _, ok := edges["a->b"]; ok {
		t.Errorf("stale a->b edge survived the rewrite: %+v", afterRewrite.Edges)
	}

	// A created package is discoverable.
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "d.go"), []byte("package d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result := mustGraph(t, idx, GraphQuery{Package: "d"}); !result.Found {
		t.Errorf("created package is not discoverable: %+v", result)
	}

	// A deleted package is gone.
	if err := os.RemoveAll(filepath.Join(root, "d")); err != nil {
		t.Fatal(err)
	}
	if result := mustGraph(t, idx, GraphQuery{Package: "d"}); result.Found {
		t.Errorf("deleted package is still discoverable: %+v", result)
	}

	// A module-path change invalidates every module-local edge. The same
	// import path is now external to the new module.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/other\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterModule := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})
	if len(afterModule.Edges) != 0 {
		t.Errorf("edges survived a module-path change: %+v", afterModule.Edges)
	}
	if nodes := graphNodeSet(afterModule); nodes["a"].ExternalImports != 1 {
		t.Errorf("external imports after module change = %d, want 1", nodes["a"].ExternalImports)
	}

	// A parse failure is isolated and counted, not silently presented as a
	// complete graph.
	if err := os.WriteFile(filepath.Join(root, "bad", "bad.go"), []byte("not go"), 0o644); err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "bad"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "bad", "bad.go"), []byte("not go"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if result := mustGraph(t, idx, GraphQuery{Package: "a"}); result.ParseFailures != 1 {
		t.Errorf("parse failures = %d, want 1", result.ParseFailures)
	}
}

func TestGraphRequiresModulePath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := mustIndex(t, root)
	if _, err := idx.Graph(context.Background(), GraphQuery{Package: "a"}); err == nil {
		t.Fatal("graph without a go.mod module path was accepted")
	}
}

func TestGraphHandlesCyclesAndIsolatedPackages(t *testing.T) {
	// Go rejects import cycles, but a parser-level graph can still observe
	// them. Traversal must terminate, and an isolated package is an ok node
	// with zero edges, not a not_found.
	root := graphFixture(t, map[string]string{
		"a/a.go":   "package a\n\nimport \"example.com/app/b\"\n",
		"b/b.go":   "package b\n\nimport \"example.com/app/a\"\n",
		"iso/i.go": "package iso\n",
	})
	idx := mustIndex(t, root)

	cycle := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 4})
	if len(cycle.Nodes) != 2 {
		t.Errorf("cycle nodes = %d, want 2", len(cycle.Nodes))
	}
	if len(cycle.Edges) != 2 {
		t.Errorf("cycle edges = %d, want 2", len(cycle.Edges))
	}

	isolated := mustGraph(t, idx, GraphQuery{Package: "iso"})
	if !isolated.Found || isolated.Truncated || len(isolated.Edges) != 0 {
		t.Errorf("isolated package = %+v", isolated)
	}
}

func TestGraphCancellation(t *testing.T) {
	// Cancellation before the query is an error, not an empty success.
	root := graphFixture(t, map[string]string{"a/a.go": "package a\n"})
	idx := mustIndex(t, root)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := idx.Graph(cancelled, GraphQuery{Package: "a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled query = %v, want context.Canceled", err)
	}

	// Cancel after the freshness walk, while the traversal is visiting
	// queue nodes. The first query counts the context checks; the second
	// cancels a few checks before that count. This is deterministic for a
	// fixed fixture and a fresh index.
	chain := map[string]string{"go.mod": "module example.com/app\n\ngo 1.21\n"}
	for i := 0; i < 5; i++ {
		next := ""
		if i+1 < 5 {
			next = fmt.Sprintf("import \"example.com/app/p%d\"\n", i+1)
		}
		chain[fmt.Sprintf("p%d/p.go", i)] = fmt.Sprintf("package p%d\n\n%s", i, next)
	}
	chainRoot := graphFixture(t, chain)
	chainIdx := mustIndex(t, chainRoot)
	baseline := &countingContext{Context: context.Background()}
	if _, err := chainIdx.Graph(baseline, GraphQuery{Package: "p0", Depth: 4}); err != nil {
		t.Fatalf("baseline query: %v", err)
	}
	if baseline.calls < 5 {
		t.Fatalf("baseline context checks = %d, too few to cancel during traversal", baseline.calls)
	}
	mid := &countingContext{Context: context.Background(), cancelAt: baseline.calls - 3}
	if _, err := chainIdx.Graph(mid, GraphQuery{Package: "p0", Depth: 4}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-traversal query = %v, want context.Canceled", err)
	}
	// The cancelled query must not have published a partial snapshot: a
	// fresh query still sees the complete chain.
	after := mustGraph(t, chainIdx, GraphQuery{Package: "p0", Depth: 4})
	if len(after.Nodes) != 5 {
		t.Errorf("nodes after cancellation = %d, want 5", len(after.Nodes))
	}
}

// countingContext counts Err calls and optionally cancels after cancelAt.
type countingContext struct {
	context.Context
	mu       sync.Mutex
	calls    int
	cancelAt int
}

func (c *countingContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.cancelAt > 0 && c.calls >= c.cancelAt {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestGraphConcurrentQueriesStayConsistent(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n",
		"b/b.go": "package b\n\nimport \"example.com/app/c\"\n",
		"c/c.go": "package c\n",
	})
	idx := mustIndex(t, root)
	want := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 2})

	var wg sync.WaitGroup
	results := make([]GraphResult, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := idx.Graph(context.Background(), GraphQuery{Package: "a", Depth: 2})
			if err != nil {
				t.Errorf("concurrent graph query: %v", err)
				return
			}
			results[i] = result
		}(i)
	}
	wg.Wait()
	for i, result := range results {
		if !reflect.DeepEqual(result, want) {
			t.Errorf("concurrent result %d differs from the single-threaded result", i)
		}
	}
}

func TestGraphOutputIsDeterministic(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport (\n\t\"example.com/app/c\"\n\t\"example.com/app/b\"\n)\n",
		"b/b.go": "package b\n",
		"c/c.go": "package c\n",
	})
	idx := mustIndex(t, root)
	first := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})
	second := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated graph queries differ:\n%+v\n%+v", first, second)
	}
	if len(first.Edges) == 2 && first.Edges[0].To > first.Edges[1].To {
		t.Errorf("edges are not sorted: %+v", first.Edges)
	}
}

func TestGraphRootSymlink(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n",
		"b/b.go": "package b\n",
	})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	idx := mustIndex(t, link)
	result := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})
	if !result.Found || len(result.Edges) != 1 {
		t.Fatalf("symlinked root graph = %+v", result)
	}
	if !strings.HasPrefix(result.Edges[0].From, "a") {
		t.Errorf("edge from = %q, want repository-relative", result.Edges[0].From)
	}
}

func TestGraphCancellationDuringBuildDoesNotPublishPartialState(t *testing.T) {
	// A cancelled rebuild must leave the previous snapshot intact and return
	// the cancellation error. The next uncancelled query rebuilds from the
	// tree, so a partial parse can never become the served graph.
	root := graphFixture(t, map[string]string{
		"p0/p.go": "package p0\n\nimport \"example.com/app/p1\"\n",
		"p1/p.go": "package p1\n",
	})
	idx := mustIndex(t, root)
	if edge := graphEdgeSet(mustGraph(t, idx, GraphQuery{Path: "p0", Depth: 1}))["p0->p1"]; edge.To == "" {
		t.Fatal("precondition: p0->p1 is indexed")
	}

	if err := os.WriteFile(filepath.Join(root, "p0", "p.go"),
		[]byte("package p0\n\nimport \"example.com/app/p1\"\n\nfunc Changed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := idx.staleLockedCtx(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled freshness scan = %v, want context.Canceled", err)
	}

	// Cancel a rebuild after it has begun parsing, then inspect the published
	// snapshot directly: the partial local maps must not have replaced it.
	mid := &countingContext{Context: context.Background(), cancelAt: 6}
	idx.mu.Lock()
	err := idx.buildLockedCtx(mid)
	if err == nil {
		idx.mu.Unlock()
		t.Fatal("mid-build cancellation did not abort the build")
	}
	published := false
	if rec, ok := idx.files["p0/p.go"]; ok {
		for _, sym := range rec.symbols {
			if sym.Name == "Changed" {
				published = true
			}
		}
	}
	idx.mu.Unlock()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-build cancellation = %v, want context.Canceled", err)
	}
	if published {
		t.Fatal("a cancelled build published partial symbols")
	}

	// The next query rebuilds from the authoritative tree.
	got, err := idx.Lookup("Changed")
	if err != nil || len(got) != 1 {
		t.Fatalf("rebuild after cancellation did not see the change: %v err=%v", got, err)
	}
}

func TestGraphMissingSymbolIsNotFound(t *testing.T) {
	root := graphFixture(t, map[string]string{"a/a.go": "package a\n\nfunc A() {}\n"})
	idx := mustIndex(t, root)
	result := mustGraph(t, idx, GraphQuery{Symbol: "Missing"})
	if result.Found || result.Ambiguous {
		t.Fatalf("missing symbol = %+v, want not found", result)
	}
}

func TestGraphCrossPackageSymbolResolvesOnePackage(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() {}\n",
		"b/b.go": "package b\n\nfunc Unique() {}\n",
	})
	idx := mustIndex(t, root)
	result := mustGraph(t, idx, GraphQuery{Symbol: "Unique"})
	if !result.Found || result.Ambiguous {
		t.Fatalf("cross-package symbol = %+v", result)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Path != "b" {
		t.Errorf("nodes = %+v, want b", result.Nodes)
	}
}

func TestGraphTreatsGeneratedAndBuildTaggedFilesAsSource(t *testing.T) {
	// The index is source-level, not build-context-aware. Generated files
	// and files excluded by a build tag are still declarations in the tree,
	// so their imports are reported. This is an explicit contract limit, not
	// a silent semantic claim.
	root := graphFixture(t, map[string]string{
		"p0/gen.go": "// Code generated by a tool. DO NOT EDIT.\n\npackage p0\n\nimport \"example.com/app/p1\"\n",
		"p0/tag.go": "//go:build ignore\n\npackage p0\n\nimport \"example.com/app/p2\"\n",
		"p1/p.go":   "package p1\n",
		"p2/p.go":   "package p2\n",
	})
	idx := mustIndex(t, root)
	result := mustGraph(t, idx, GraphQuery{Path: "p0", Depth: 1})
	edges := graphEdgeSet(result)
	for _, target := range []string{"p0->p1", "p0->p2"} {
		if _, ok := edges[target]; !ok {
			t.Errorf("source-level edge %s missing: %+v", target, result.Edges)
		}
	}
}
