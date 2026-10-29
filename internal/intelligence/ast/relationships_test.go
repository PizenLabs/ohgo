package ast

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// refCoords renders a reference set as stable path:line:column strings.
func refCoords(refs []ReferenceSite) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		out = append(out, fmt.Sprintf("%s:%d:%d", ref.Path, ref.Line, ref.Column))
	}
	sort.Strings(out)
	return out
}

// edgeCoords renders call edges as caller@call-site -> callee strings.
func edgeCoords(edges []CallEdge) []string {
	out := make([]string, 0, len(edges))
	for _, edge := range edges {
		out = append(out, fmt.Sprintf("%s@%s:%d:%d->%s",
			edge.Caller.Symbol, edge.CallPath, edge.CallLine, edge.Column, edge.Callee.Symbol))
	}
	sort.Strings(out)
	return out
}

func mustRefs(t *testing.T, idx *Index, query RefQuery) RefResult {
	t.Helper()
	result, err := idx.References(context.Background(), query)
	if err != nil {
		t.Fatalf("References(%+v): %v", query, err)
	}
	return result
}

// A reference answer must have stable coordinates, distinguish a call from a
// value use, and never turn a composite-literal field key into a symbol
// reference.
func TestReferencesResolveStaticSymbolUses(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"a/a.go": `package a

import "example.com/app/b"

func A() {
	B()
	b.B()
	_ = b.T{}
}

func B() {}
`,
		"b/b.go": `package b

var Field = 1

type T struct{ Field int }

func B() {}

func C() {
	B()
	var v T
	_ = v.Field
	_ = T{Field: 1}
}
`,
	})
	idx := mustIndex(t, root)

	bRefs := mustRefs(t, idx, RefQuery{Symbol: "b.B"})
	if !bRefs.Found || bRefs.Ambiguous || bRefs.Unsupported {
		t.Fatalf("b.B target = found:%v ambiguous:%v unsupported:%v", bRefs.Found, bRefs.Ambiguous, bRefs.Unsupported)
	}
	if got, want := refCoords(bRefs.References), []string{"a/a.go:7:2", "b/b.go:10:2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b.B references = %v, want %v", got, want)
	}
	for _, ref := range bRefs.References {
		if !ref.Qualified && ref.Path == "a/a.go" {
			t.Errorf("a/a.go reference was not marked qualified: %+v", ref)
		}
		if !ref.Call {
			t.Errorf("call reference was not marked as a call: %+v", ref)
		}
	}

	tRefs := mustRefs(t, idx, RefQuery{Symbol: "b.T"})
	if got, want := refCoords(tRefs.References), []string{"a/a.go:8:6", "b/b.go:11:8", "b/b.go:13:6"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b.T references = %v, want %v", got, want)
	}
	if tRefs.TotalReferences != 3 {
		t.Errorf("b.T total = %d, want 3", tRefs.TotalReferences)
	}

	// A field key in T{Field: 1} and a selector v.Field are not references to
	// the package-level var b.Field.
	fieldRefs := mustRefs(t, idx, RefQuery{Symbol: "b.Field"})
	if fieldRefs.TotalReferences != 0 || len(fieldRefs.References) != 0 {
		t.Fatalf("b.Field picked up a field name: %+v", fieldRefs.References)
	}

	callers := mustRefs(t, idx, RefQuery{Symbol: "b.B", Relation: RelationCallers})
	if got, want := edgeCoords(callers.Edges), []string{"A@a/a.go:7:2->B", "C@b/b.go:10:2->B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b.B callers = %v, want %v", got, want)
	}
	if callers.Unknown.DynamicCalls != 0 || !callers.Complete {
		t.Fatalf("b.B callers completeness = complete:%v dynamic:%d", callers.Complete, callers.Unknown.DynamicCalls)
	}

	callees := mustRefs(t, idx, RefQuery{Symbol: "b.C", Relation: RelationCallees})
	if got, want := edgeCoords(callees.Edges), []string{"C@b/b.go:10:2->B"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b.C callees = %v, want %v", got, want)
	}
}

// A local variable that shadows a package function must not create a call
// edge, and a local variable that shadows a package var must not create a
// reference.
func TestReferencesRespectLocalShadowing(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": `package p

var Value = 1

type T struct{ Field int }

func F() {}

func H() {
	F := func() {}
	F()
	Value := 2
	_ = Value
	_ = T{Field: 1}
}
`,
	})
	idx := mustIndex(t, root)

	fRefs := mustRefs(t, idx, RefQuery{Symbol: "p.F"})
	if fRefs.TotalReferences != 0 {
		t.Fatalf("local F was reported as a reference to p.F: %+v", fRefs.References)
	}
	fCallers := mustRefs(t, idx, RefQuery{Symbol: "p.F", Relation: RelationCallers})
	if len(fCallers.Edges) != 0 {
		t.Fatalf("local F produced a package call edge: %+v", fCallers.Edges)
	}
	if fCallers.Unknown.DynamicCalls != 1 || fCallers.Unknown.Reasons["function_value"] != 1 {
		t.Fatalf("local F call was not disclosed as a function value: %+v", fCallers.Unknown)
	}

	valueRefs := mustRefs(t, idx, RefQuery{Symbol: "p.Value"})
	if valueRefs.TotalReferences != 0 {
		t.Fatalf("local Value was reported as a reference to p.Value: %+v", valueRefs.References)
	}
	fieldRefs := mustRefs(t, idx, RefQuery{Symbol: "p.Field"})
	if fieldRefs.TotalReferences != 0 {
		t.Fatalf("composite field key was reported as p.Field: %+v", fieldRefs.References)
	}
	typeRefs := mustRefs(t, idx, RefQuery{Symbol: "p.T"})
	if got, want := refCoords(typeRefs.References), []string{"p/p.go:14:6"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("p.T references = %v, want the composite literal type only (%v)", got, want)
	}
}

// An import name is file-scoped and shadows a package-level declaration. The
// selector base is the package, not a reference to the shadowed symbol.
func TestReferencesDoNotTreatAnImportQualifierAsASymbol(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"q/q.go": "package q\n\nfunc F() {}\n",
		"p/p.go": `package p

import pkg "example.com/app/q"

var pkg = 1

func Use() {
	pkg.F()
}
`,
	})
	idx := mustIndex(t, root)

	shadowed := mustRefs(t, idx, RefQuery{Symbol: "p.pkg"})
	if shadowed.TotalReferences != 0 {
		t.Fatalf("import qualifier was reported as a reference to the shadowed var: %+v", shadowed.References)
	}
	target := mustRefs(t, idx, RefQuery{Symbol: "q.F"})
	if got, want := refCoords(target.References), []string{"p/p.go:8:2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("q.F references = %v, want %v", got, want)
	}
	callees := mustRefs(t, idx, RefQuery{Symbol: "p.Use", Relation: RelationCallees})
	if got, want := edgeCoords(callees.Edges), []string{"Use@p/p.go:8:2->F"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Use callees = %v, want %v", got, want)
	}
}

// An exact symbol name must never choose a winner between declarations.
func TestReferencesPreserveAmbiguity(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.21\n",
		"x1/x.go": "package same\n\nfunc X() {}\n",
		"x2/x.go": "package same\n\nfunc X() {}\n",
		"p/a.go":  "package p\n\nfunc Y() {}\n",
		"p/b.go":  "package p\n\nfunc Y() {}\n",
	})
	idx := mustIndex(t, root)

	for _, symbol := range []string{"X", "same.X", "p.Y"} {
		result := mustRefs(t, idx, RefQuery{Symbol: symbol})
		if !result.Ambiguous || result.Found || len(result.Candidates) != 2 {
			t.Errorf("%s = found:%v ambiguous:%v candidates:%d, want ambiguous with 2 candidates",
				symbol, result.Found, result.Ambiguous, len(result.Candidates))
		}
		if len(result.References) != 0 {
			t.Errorf("%s returned references for an ambiguous target", symbol)
		}
	}

	// A path selector is an explicit disambiguator, not a guess.
	scoped := mustRefs(t, idx, RefQuery{Symbol: "X", Path: "x1"})
	if !scoped.Found || scoped.Ambiguous {
		t.Fatalf("scoped X = found:%v ambiguous:%v", scoped.Found, scoped.Ambiguous)
	}
	if scoped.Target.Path != "x1/x.go" {
		t.Fatalf("scoped X target = %q, want x1/x.go", scoped.Target.Path)
	}
	if _, err := idx.References(context.Background(), RefQuery{Symbol: "X", Path: "../outside"}); err == nil {
		t.Fatal("a scope outside the index root was accepted")
	}
}

// A dot import can capture any unqualified name, so an answer that could see
// one must be marked incomplete rather than silently omitting it.
func TestReferencesDiscloseDotImportBlindSpots(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
		"dot/dot.go": `package dot

import . "example.com/app/b"

func helper() {}

func E() {
	B()
	helper()
}
`,
		"q/q.go":     "package q\n\nimport \"example.com/app/b\"\n\nfunc Q() { b.B() }\n",
		"clean/c.go": "package clean\n\nfunc C() {}\n",
	})
	idx := mustIndex(t, root)

	bRefs := mustRefs(t, idx, RefQuery{Symbol: "b.B"})
	if got, want := refCoords(bRefs.References), []string{"q/q.go:5:12"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("b.B references = %v, want only the qualified site %v", got, want)
	}
	if bRefs.Complete {
		t.Fatal("b.B was reported complete despite a dot import of package b")
	}
	if bRefs.Unknown.DotImportFiles != 1 {
		t.Fatalf("dot import files = %d, want 1", bRefs.Unknown.DotImportFiles)
	}

	callers := mustRefs(t, idx, RefQuery{Symbol: "b.B", Relation: RelationCallers})
	if len(callers.Edges) != 1 || callers.Edges[0].Caller.Symbol != "Q" {
		t.Fatalf("b.B callers = %+v, want only Q", callers.Edges)
	}
	if callers.Complete || callers.Unknown.Reasons["dot_import"] != 1 {
		t.Fatalf("dot-import call blind spot was not disclosed: %+v", callers.Unknown)
	}

	// A lowercase local name cannot be captured by a dot import, so it still
	// resolves. The target package remains incomplete because exported names
	// in the same file can be captured.
	helper := mustRefs(t, idx, RefQuery{Symbol: "dot.helper"})
	if got, want := refCoords(helper.References), []string{"dot/dot.go:9:2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dot.helper references = %v, want %v", got, want)
	}
	if helper.Complete {
		t.Fatal("a package containing a dot-importing file was reported complete")
	}
	dotCallees := mustRefs(t, idx, RefQuery{Symbol: "dot.E", Relation: RelationCallees})
	if got, want := edgeCoords(dotCallees.Edges), []string{"E@dot/dot.go:9:2->helper"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dot.E callees = %v, want the lowercase local call only (%v)", got, want)
	}
	if dotCallees.Unknown.Reasons["dot_import"] != 1 {
		t.Fatalf("exported dot-import call was not disclosed: %+v", dotCallees.Unknown)
	}

	clean := mustRefs(t, idx, RefQuery{Symbol: "clean.C"})
	if !clean.Complete {
		t.Fatalf("clean.C completeness = false without a dot import in scope: %+v", clean.Unknown)
	}
}

// A reference page must report truncation and support skip without silently
// clamping the target or the bounds.
func TestReferencesBoundsAndPagination(t *testing.T) {
	var calls strings.Builder
	for i := 0; i < 5; i++ {
		calls.WriteString("\tF()\n")
	}
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": "package p\n\nfunc F() {}\n\nfunc C() {\n" + calls.String() + "}\n",
	})
	idx := mustIndex(t, root)

	result := mustRefs(t, idx, RefQuery{Symbol: "p.F", Limit: 2})
	if len(result.References) != 2 || result.TotalReferences != 5 {
		t.Fatalf("limited refs = %d total %d, want 2/5", len(result.References), result.TotalReferences)
	}
	if !result.Truncated || result.Reason != "limit" {
		t.Fatalf("limit = truncated:%v reason:%q, want true/limit", result.Truncated, result.Reason)
	}

	page := mustRefs(t, idx, RefQuery{Symbol: "p.F", Skip: 4, Limit: 2})
	if len(page.References) != 1 || page.TotalReferences != 5 {
		t.Fatalf("skipped page = %d total %d, want 1/5", len(page.References), page.TotalReferences)
	}
	if page.Truncated {
		t.Fatal("the final page was reported truncated")
	}

	for _, query := range []RefQuery{
		{Symbol: "p.F", Limit: RefMaxLimit + 1},
		{Symbol: "p.F", EdgeLimit: RefMaxEdges + 1},
		{Symbol: "p.F", Skip: -1},
		{Symbol: ""},
		{Symbol: "p.F", Relation: "invented"},
		{Symbol: "p.F", Depth: 2},
	} {
		if _, err := idx.References(context.Background(), query); err == nil {
			t.Errorf("invalid query %+v was accepted", query)
		}
	}
}

// A cancelled context must never produce a partial reference or call answer.
func TestReferencesHonourCancellation(t *testing.T) {
	var calls strings.Builder
	for i := 0; i < 20000; i++ {
		calls.WriteString("\tF()\n")
	}
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": "package p\n\nfunc F() {}\n\nfunc C() {\n" + calls.String() + "}\n",
	})
	idx := mustIndex(t, root)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := idx.References(cancelled, RefQuery{Symbol: "p.F"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query error = %v, want context.Canceled", err)
	}

	// The scan checks cancellation every 256 work items. Cancel at the 20th
	// check: a scan that ignored the check would run to the final check and
	// record far more calls, so the call count is the mutation-sensitive
	// assertion.
	mid := &countingContext{Context: context.Background(), cancelAt: 20}
	if _, err := idx.References(mid, RefQuery{Symbol: "p.F"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-scan cancellation error = %v, want context.Canceled", err)
	}
	if mid.calls > 20 {
		t.Fatalf("cancellation was observed after %d checks, want at most 20", mid.calls)
	}

	// The call projection uses the same discipline.
	mid = &countingContext{Context: context.Background(), cancelAt: 20}
	if _, err := idx.References(mid, RefQuery{Symbol: "p.F", Relation: RelationCallers}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-scan caller cancellation error = %v, want context.Canceled", err)
	}
	if mid.calls > 20 {
		t.Fatalf("caller cancellation was observed after %d checks, want at most 20", mid.calls)
	}
}

// A changed reference or module path must invalidate the answer; a parse
// failure must make the answer visibly partial.
func TestReferencesFreshness(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod":   "module example.com/app\n\ngo 1.21\n",
		"m/m.go":   "package m\n\nfunc M() {}\n",
		"use/u.go": "package use\n\nimport \"example.com/app/m\"\n\nfunc U() { m.M() }\n",
	})
	idx := mustIndex(t, root)

	initial := mustRefs(t, idx, RefQuery{Symbol: "m.M"})
	if initial.TotalReferences != 1 || !initial.Complete {
		t.Fatalf("initial m.M = total:%d complete:%v", initial.TotalReferences, initial.Complete)
	}

	// A same-size rewrite with a changed mtime must rebuild and replace the
	// old reference.
	usePath := filepath.Join(root, "use", "u.go")
	if err := os.WriteFile(usePath, []byte("package use\n\nimport \"example.com/app/m\"\n\nfunc U() { m.M(); m.M() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refreshed := mustRefs(t, idx, RefQuery{Symbol: "m.M"})
	if refreshed.TotalReferences != 2 {
		t.Fatalf("rewrite m.M total = %d, want 2", refreshed.TotalReferences)
	}

	// A module-path change makes the old import external. The qualified
	// reference must disappear rather than survive as a stale edge.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/other\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterModule := mustRefs(t, idx, RefQuery{Symbol: "m.M"})
	if afterModule.TotalReferences != 0 {
		t.Fatalf("module change left %d stale references: %+v", afterModule.TotalReferences, afterModule.References)
	}

	// A file that cannot be parsed is counted, and the answer is marked
	// partial. The clean reference is still reported.
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
	partial := mustRefs(t, idx, RefQuery{Symbol: "m.M"})
	if partial.ParseFailures != 1 {
		t.Fatalf("parse failures = %d, want 1", partial.ParseFailures)
	}
	if partial.Complete {
		t.Fatal("an answer with a parse failure was reported complete")
	}
}

// Only statically provable direct calls become edges. Method dispatch,
// function values, conversions, and builtins are classified, never guessed.
func TestCallGraphKeepsDynamicDispatchUnknown(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": `package p

type T struct{}

func F() {}

func G() { F() }

func (T) M(int) {}

func H() {
	F()
	G()
	T.M(0)
	var v T
	v.M(1)
	f := F
	f()
	_ = T(0)
	_ = (*T)(nil)
	_ = len("x")
	_ = int(0)
	_ = string("x")
	_ = bool(true)
	_ = (*int)(nil)
}
`,
	})
	idx := mustIndex(t, root)

	h := mustRefs(t, idx, RefQuery{Symbol: "p.H", Relation: RelationCallees})
	if got, want := edgeCoords(h.Edges), []string{"H@p/p.go:12:2->F", "H@p/p.go:13:2->G", "H@p/p.go:14:2->T.M"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("H callees = %v, want %v", got, want)
	}
	if h.Unknown.DynamicCalls != 2 || h.Unknown.Reasons["method_dispatch"] != 1 || h.Unknown.Reasons["function_value"] != 1 {
		t.Fatalf("H dynamic classification = %+v, want one method and one function value", h.Unknown)
	}
	if h.Unknown.Reasons["undefined"] != 0 {
		t.Fatalf("predeclared conversions/builtins were classified as undefined calls: %+v", h.Unknown.Reasons)
	}
	if h.Complete {
		t.Fatal("H was reported complete despite dynamic calls")
	}

	fRefs := mustRefs(t, idx, RefQuery{Symbol: "p.F"})
	if fRefs.TotalReferences != 3 {
		t.Fatalf("F references = %d, want the two call sites plus G's call plus f's value use", fRefs.TotalReferences)
	}
	fCallers := mustRefs(t, idx, RefQuery{Symbol: "p.F", Relation: RelationCallers})
	if got, want := edgeCoords(fCallers.Edges), []string{"G@p/p.go:7:12->F", "H@p/p.go:12:2->F"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("F callers = %v, want %v", got, want)
	}
	if fCallers.Complete {
		t.Fatal("F callers was reported complete despite a function-value call")
	}

	methodRefs := mustRefs(t, idx, RefQuery{Symbol: "p.T.M"})
	if !methodRefs.Unsupported || !methodRefs.Found {
		t.Fatalf("method references = unsupported:%v found:%v, want the method resolved but the relation unsupported",
			methodRefs.Unsupported, methodRefs.Found)
	}
	if len(methodRefs.References) != 0 {
		t.Fatalf("method references returned body references: %+v", methodRefs.References)
	}
	methodCallers := mustRefs(t, idx, RefQuery{Symbol: "p.T.M", Relation: RelationCallers})
	if got, want := edgeCoords(methodCallers.Edges), []string{"H@p/p.go:14:2->T.M"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("T.M callers = %v, want the method expression only (%v)", got, want)
	}
	if methodCallers.Unknown.Reasons["method_dispatch"] != 1 {
		t.Fatalf("ordinary method dispatch was not disclosed: %+v", methodCallers.Unknown)
	}

	typeRefs := mustRefs(t, idx, RefQuery{Symbol: "p.T"})
	// The method receiver, the method expression base, the local variable
	// type, and both conversions are references to the type. Field selectors
	// and struct field declarations are not.
	if typeRefs.TotalReferences < 4 {
		t.Fatalf("T references = %d, want receiver/method expression/var/conversions", typeRefs.TotalReferences)
	}
}

// A direct call graph traversal must terminate on cycles and report every
// bound it reaches instead of presenting a partial walk as complete.
func TestCallGraphTraversalIsBoundedAndCycleSafe(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": `package p

func A() { B() }
func B() { C() }
func C() { A() }
func D() { B(); B() }
`,
	})
	idx := mustIndex(t, root)

	cycle := mustRefs(t, idx, RefQuery{Symbol: "p.A", Relation: RelationCallees, Depth: 4})
	if len(cycle.Nodes) != 3 || len(cycle.Edges) != 3 {
		t.Fatalf("cycle = %d nodes %d edges, want 3/3", len(cycle.Nodes), len(cycle.Edges))
	}
	if cycle.Truncated {
		t.Fatalf("cycle traversal was truncated without a bound: %+v", cycle)
	}
	if cycle.TotalEdges != 3 {
		t.Fatalf("cycle total edges = %d, want 3", cycle.TotalEdges)
	}

	page := mustRefs(t, idx, RefQuery{Symbol: "p.A", Relation: RelationCallees, Depth: 4, Skip: 1})
	if len(page.Edges) != 2 || page.TotalEdges != 3 {
		t.Fatalf("traversal page = %d edges total %d, want 2/3", len(page.Edges), page.TotalEdges)
	}

	depth := mustRefs(t, idx, RefQuery{Symbol: "p.A", Relation: RelationCallees, Depth: 1})
	if !depth.Truncated || depth.Reason != "depth" {
		t.Fatalf("depth bound = truncated:%v reason:%q, want true/depth", depth.Truncated, depth.Reason)
	}

	nodes := mustRefs(t, idx, RefQuery{Symbol: "p.A", Relation: RelationCallees, Depth: 4, Limit: 2})
	if !nodes.Truncated || nodes.Reason != "node_limit" || len(nodes.Nodes) != 2 {
		t.Fatalf("node bound = truncated:%v reason:%q nodes:%d, want true/node_limit/2",
			nodes.Truncated, nodes.Reason, len(nodes.Nodes))
	}

	edges := mustRefs(t, idx, RefQuery{Symbol: "p.A", Relation: RelationCallees, Depth: 4, Limit: 10, EdgeLimit: 1})
	if !edges.Truncated || edges.Reason != "edge_limit" || len(edges.Edges) != 1 {
		t.Fatalf("edge bound = truncated:%v reason:%q edges:%d, want true/edge_limit/1",
			edges.Truncated, edges.Reason, len(edges.Edges))
	}

	// Two calls on one line are two call-site witnesses, not one deduped
	// edge.
	callers := mustRefs(t, idx, RefQuery{Symbol: "p.B", Relation: RelationCallers})
	if callers.TotalEdges != 3 || len(callers.Edges) != 3 {
		t.Fatalf("B callers = %d total %d, want both D call sites plus A", callers.TotalEdges, len(callers.Edges))
	}
}

// A bounded call traversal must still cancel. Cancelling at the last check
// proves the final result cannot be published after the caller aborted.
func TestCallGraphTraversalHonoursCancellation(t *testing.T) {
	var body strings.Builder
	body.WriteString("func A() {\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&body, "\tB%d()\n", i)
	}
	body.WriteString("}\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&body, "func B%d() {}\n", i)
	}
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": "package p\n\n" + body.String(),
	})
	idx := mustIndex(t, root)

	// The BFS checks cancellation at every queue node. Cancelling at the 20th
	// check must stop there; a traversal that only checked at the end would
	// record far fewer calls and still return the same error.
	mid := &countingContext{Context: context.Background(), cancelAt: 20}
	if _, err := idx.References(mid, RefQuery{Symbol: "p.A", Relation: RelationCallees, Depth: 4, Limit: 200}); !errors.Is(err, context.Canceled) {
		t.Fatalf("traversal cancellation error = %v, want context.Canceled", err)
	}
	if mid.calls < 20 {
		t.Fatalf("traversal observed cancellation after only %d checks, want at least 20", mid.calls)
	}
}

// Static calls are not enough to claim a complete caller set when the code
// calls a function through a value or an interface method.
func TestCallGraphCallerCompletenessIsConservative(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": `package p

type Runner interface{ Run() }

type T struct{}

func (T) Run() {}

func Target() {}

func Uses(r Runner) {
	r.Run()
	f := Target
	f()
	Target()
}
`,
	})
	idx := mustIndex(t, root)

	result := mustRefs(t, idx, RefQuery{Symbol: "p.Target", Relation: RelationCallers})
	if len(result.Edges) != 1 || result.Edges[0].CallPath != "p/p.go" {
		t.Fatalf("Target callers = %+v, want the direct call only", result.Edges)
	}
	if result.Complete {
		t.Fatal("caller set was reported complete despite interface and function-value calls")
	}
	if result.Unknown.Reasons["method_dispatch"] != 1 || result.Unknown.Reasons["function_value"] != 1 {
		t.Fatalf("dynamic reasons = %+v, want interface dispatch and function value", result.Unknown.Reasons)
	}

	// A method call is not resolved to every method with that name.
	run := mustRefs(t, idx, RefQuery{Symbol: "p.T.Run", Relation: RelationCallers})
	if len(run.Edges) != 0 {
		t.Fatalf("interface dispatch produced method edges: %+v", run.Edges)
	}
	if run.Unknown.Reasons["method_dispatch"] != 1 {
		t.Fatalf("interface dispatch was not disclosed: %+v", run.Unknown)
	}
}

// Package initializers and anonymous closures are static callers without a
// named enclosing function. They appear with an explicit caller kind rather
// than being attributed to an arbitrary function.
func TestCallGraphLabelsPackageInitAndClosureCallers(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": `package p

var V = initFn()

var F = func() { closureFn() }

func initFn() int { return 1 }
func closureFn() int { return 2 }
`,
	})
	idx := mustIndex(t, root)

	initCall := mustRefs(t, idx, RefQuery{Symbol: "p.initFn", Relation: RelationCallers})
	if len(initCall.Edges) != 1 || initCall.Edges[0].Caller.CallerKind != "package_initializer" {
		t.Fatalf("initFn callers = %+v, want one package-initializer edge", initCall.Edges)
	}
	if initCall.Edges[0].Certainty != CertaintyStatic {
		t.Fatalf("package initializer certainty = %q, want static", initCall.Edges[0].Certainty)
	}
	if initCall.Unknown.PackageInitializerCalls != 1 {
		t.Fatalf("package initializer calls = %d, want 1", initCall.Unknown.PackageInitializerCalls)
	}

	closureCall := mustRefs(t, idx, RefQuery{Symbol: "p.closureFn", Relation: RelationCallers})
	if len(closureCall.Edges) != 1 || closureCall.Edges[0].Caller.CallerKind != "anonymous" {
		t.Fatalf("closureFn callers = %+v, want one anonymous-caller edge", closureCall.Edges)
	}
	if closureCall.Edges[0].Certainty != CertaintyLexical {
		t.Fatalf("closure certainty = %q, want lexical", closureCall.Edges[0].Certainty)
	}
}
