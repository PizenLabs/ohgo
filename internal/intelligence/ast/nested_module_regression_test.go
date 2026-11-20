package ast

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeNestedModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
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

// A Go module in a subdirectory is still the module that owns the indexed Go
// files. Reporting no module there makes every cross-package reference
// unresolved and every package graph empty.
func TestNestedGoModuleResolvesCrossPackageReferences(t *testing.T) {
	root := writeNestedModule(t, map[string]string{
		"backend/go.mod": "module example.com/app/backend\n\ngo 1.21\n",
		"backend/a/a.go": `package a

import "example.com/app/backend/b"

func A() { b.B() }
`,
		"backend/b/b.go": "package b\n\nfunc B() {}\n",
	})
	idx := mustIndex(t, root)
	if info := idx.Info(); info.Module != "example.com/app/backend" {
		t.Fatalf("module = %q, want the nested go.mod path", info.Module)
	}

	refs := mustRefs(t, idx, RefQuery{Symbol: "b.B"})
	if !refs.Found || refs.TotalReferences != 1 {
		t.Fatalf("b.B refs = found:%v total:%d refs:%+v", refs.Found, refs.TotalReferences, refs.References)
	}
	if refs.References[0].Path != "backend/a/a.go" {
		t.Fatalf("reference path = %q, want backend/a/a.go", refs.References[0].Path)
	}

	graph := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})
	if !graph.Found || len(graph.Edges) != 1 || graph.Edges[0].From != "backend/a" || graph.Edges[0].To != "backend/b" {
		t.Fatalf("package graph = %+v, want backend/a -> backend/b", graph)
	}

	deps, err := idx.Dependencies(context.Background(), DependencyQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if !deps.Complete || len(deps.Edges) != 1 || deps.Edges[0].From != "backend/a" {
		t.Fatalf("dependencies = %+v, want one complete a -> b edge", deps)
	}
}

// Impact used to expose only inbound callers. A flow question needs the
// forward edges as well.
func TestImpactReportsCallees(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() { b.B() }\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
	})
	idx := mustIndex(t, root)
	result, err := idx.Impact(context.Background(), ImpactQuery{Symbol: "a.A", Depth: 2, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || len(result.Callees) != 1 {
		t.Fatalf("A callees = %+v, want one B edge", result.Callees)
	}
	if result.Callees[0].Callee.Symbol != "B" {
		t.Fatalf("callee = %+v, want B", result.Callees[0].Callee)
	}
}
