package tools

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
)

func refsFixtureRepo(t *testing.T) string {
	t.Helper()
	return symbolsFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"a/a.go": `package a

import "example.com/app/b"

func A() {
	b.B()
}
`,
		"b/b.go": `package b

func B() {}

func C() { B() }
`,
	})
}

func refsStatus(t *testing.T, result agent.Result) string {
	t.Helper()
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %T, want map[string]any", result.Details)
	}
	status, _ := details["status"].(string)
	return status
}

func TestRefsToolFindsReferencesAndCallEdges(t *testing.T) {
	root := refsFixtureRepo(t)
	tool := NewRefsTool(NewStructuralSource(root))

	references := tool.Execute(testContext(), map[string]any{
		"symbol":   "b.B",
		"relation": "references",
	})
	if references.IsError {
		t.Fatalf("references error: %s", references.Text)
	}
	if status := refsStatus(t, references); status != "ok" {
		t.Fatalf("references status = %q, want ok: %s", status, references.Text)
	}
	for _, want := range []string{
		"target: b/b.go:3",
		"kind: qualified",
		"call: true",
		"path: a/a.go",
	} {
		if !strings.Contains(references.Text, want) {
			t.Errorf("references text omits %q:\n%s", want, references.Text)
		}
	}

	callers := tool.Execute(testContext(), map[string]any{
		"symbol":   "b.B",
		"relation": "callers",
	})
	if callers.IsError {
		t.Fatalf("callers error: %s", callers.Text)
	}
	for _, want := range []string{
		"relation: callers",
		"caller: A",
		"callee: B",
		"call: a/a.go:6:2",
		"certainty: static",
	} {
		if !strings.Contains(callers.Text, want) {
			t.Errorf("callers text omits %q:\n%s", want, callers.Text)
		}
	}

	callees := tool.Execute(testContext(), map[string]any{
		"symbol":   "b.C",
		"relation": "callees",
		"depth":    2.0,
	})
	if callees.IsError {
		t.Fatalf("callees error: %s", callees.Text)
	}
	if !strings.Contains(callees.Text, "caller: C") || !strings.Contains(callees.Text, "callee: B") {
		t.Errorf("callees text omits the edge:\n%s", callees.Text)
	}
}

func TestRefsToolReportsUnsupportedAndAmbiguousStates(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"p/p.go": `package p

type T struct{}

func (T) M() {}

func X() {}
`,
		"q/q.go": "package q\n\nfunc X() {}\n",
	})
	tool := NewRefsTool(NewStructuralSource(root))

	method := tool.Execute(testContext(), map[string]any{"symbol": "p.T.M"})
	if method.IsError {
		t.Fatalf("method query error: %s", method.Text)
	}
	if status := refsStatus(t, method); status != "unsupported" {
		t.Fatalf("method status = %q, want unsupported:\n%s", status, method.Text)
	}
	if !strings.Contains(method.Text, "unsupported:") {
		t.Errorf("method text does not explain the limitation:\n%s", method.Text)
	}

	ambiguous := tool.Execute(testContext(), map[string]any{"symbol": "X"})
	if ambiguous.IsError {
		t.Fatalf("ambiguous query error: %s", ambiguous.Text)
	}
	if status := refsStatus(t, ambiguous); status != "ambiguous" {
		t.Fatalf("ambiguous status = %q, want ambiguous:\n%s", status, ambiguous.Text)
	}
	if !strings.Contains(ambiguous.Text, "candidates:") {
		t.Errorf("ambiguous text omits candidates:\n%s", ambiguous.Text)
	}
}

func TestRefsToolRejectsMalformedArguments(t *testing.T) {
	root := refsFixtureRepo(t)
	tool := NewRefsTool(NewStructuralSource(root))

	cases := []map[string]any{
		{},
		{"symbol": ""},
		{"symbol": 1.0},
		{"symbol": "b.B", "relation": "invented"},
		{"symbol": "b.B", "relation": ""},
		{"symbol": "b.B", "relation": "references", "depth": 2.0},
		{"symbol": "b.B", "relation": "callers", "edges": 5.0},
		{"symbol": "b.B", "edges": 5.0},
		{"symbol": "b.B", "depth": 0.0},
		{"symbol": "b.B", "relation": "callers", "depth": 5.0},
		{"symbol": "b.B", "limit": 0.0},
		{"symbol": "b.B", "limit": 201.0},
		{"symbol": "b.B", "edges": 0.0},
		{"symbol": "b.B", "edges": 401.0},
		{"symbol": "b.B", "skip": -1.0},
		{"symbol": "b.B", "surprise": true},
		{"symbol": "b.B", "path": "../outside"},
		{"symbol": "b.B", "path": "   "},
		{"symbol": "b.B", "path": "does-not-exist"},
	}
	for _, args := range cases {
		result := tool.Execute(testContext(), args)
		if !result.IsError {
			t.Errorf("args %v were accepted: %s", args, result.Text)
		}
	}
}

func TestRefsToolSchemaIsClosedAndBounded(t *testing.T) {
	tool := NewRefsTool(NewStructuralSource(t.TempDir()))
	if err := agent.ValidateArgs(tool, map[string]any{
		"symbol": "x", "relation": "callers", "depth": 2.0,
		"limit": 10.0, "edges": 20.0, "skip": 0.0,
	}); err != nil {
		t.Errorf("valid arguments rejected: %v", err)
	}
	for _, args := range []map[string]any{
		{},
		{"symbol": "x", "relation": "invented"},
		{"symbol": "x", "depth": 0.0},
		{"symbol": "x", "depth": 5.0},
		{"symbol": "x", "limit": 0.0},
		{"symbol": "x", "limit": 201.0},
		{"symbol": "x", "edges": 401.0},
		{"symbol": "x", "skip": -1.0},
		{"symbol": "x", "surprise": true},
	} {
		if err := agent.ValidateArgs(tool, args); err == nil {
			t.Errorf("schema accepted %v", args)
		}
	}
}

func TestRefsToolIsReadOnlyAndSharesTheStructuralSource(t *testing.T) {
	root := refsFixtureRepo(t)
	source := NewStructuralSource(root)
	symbols := NewSymbolsToolWithSource(source)
	graph := NewGraphTool(source)
	refs := NewRefsTool(source)

	if symbols.Source() != refs.Source() || graph.Source() != refs.Source() {
		t.Fatal("refs does not share the symbols/graph structural source")
	}
	if refs.Approval().Tier != agent.TierRead {
		t.Fatalf("refs tier = %q, want read", refs.Approval().Tier)
	}
	if !agent.IsReadOnly([]agent.Tool{refs}) {
		t.Fatal("refs is not recognised as a read-only tool")
	}

	result := refs.Execute(testContext(), map[string]any{"symbol": "b.B"})
	if result.IsError {
		t.Fatalf("refs error: %s", result.Text)
	}
	if result.Mutates || result.Executes || result.Verifies || result.Finished {
		t.Fatalf("refs claimed execution effects: %+v", result)
	}
}

func TestRefsToolNotFoundIsExplicit(t *testing.T) {
	tool := NewRefsTool(NewStructuralSource(refsFixtureRepo(t)))
	result := tool.Execute(testContext(), map[string]any{"symbol": "missing.Symbol"})
	if result.IsError {
		t.Fatalf("not-found query error: %s", result.Text)
	}
	if status := refsStatus(t, result); status != "not_found" {
		t.Fatalf("status = %q, want not_found:\n%s", status, result.Text)
	}
	if !result.Useless {
		t.Error("a not-found refs answer without parse failures is not marked useless")
	}
}

func TestRefsToolBoundsAreExplicit(t *testing.T) {
	var calls strings.Builder
	for i := 0; i < 5; i++ {
		calls.WriteString("\tB()\n")
	}
	root := symbolsFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"b/b.go": "package b\n\nfunc B() {}\n\nfunc C() {\n" + calls.String() + "}\n",
	})
	tool := NewRefsTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{"symbol": "b.B", "limit": 2.0})
	if result.IsError {
		t.Fatalf("bounded query error: %s", result.Text)
	}
	if status := refsStatus(t, result); status != "truncated" {
		t.Fatalf("status = %q, want truncated:\n%s", status, result.Text)
	}
	for _, want := range []string{"truncated: true", "truncation_reason: limit", "returned: 2", "total: 5"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("bounded text omits %q:\n%s", want, result.Text)
		}
	}
}
