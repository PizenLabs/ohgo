package ast

import (
	"context"
	"testing"
)

func TestInventoryFilesPackagesConfigsAndEntrypoints(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"cmd/app/main.go":          "package main\n\nfunc main() {}\n",
		"cmd/app/main_test.go":     "package main\n\nimport \"testing\"\n\nfunc TestMainEntry(t *testing.T) {}\n",
		"lib/lib.go":               "package lib\n\nfunc Lib() {}\n",
		"lib/main.go":              "package helper\n\nfunc main() {}\n",
		"a/a.go":                   "package a\n\nimport \"example.com/app/b\"\n\nfunc A() { b.B() }\n",
		"b/b.go":                   "package b\n\nfunc B() {}\n",
		"Makefile":                 "build:\n\tgo build ./...\n",
		".github/workflows/ci.yml": "name: ci\n",
		"README.md":                "# app\n",
	})
	idx := mustIndex(t, root)
	ctx := context.Background()

	tests := true
	files, err := idx.Files(ctx, FileQuery{Test: &tests, Limit: 10})
	if err != nil {
		t.Fatalf("Files(tests): %v", err)
	}
	if files.Total != 1 || files.Files[0].Path != "cmd/app/main_test.go" || files.Files[0].Kind != FileGoTest {
		t.Fatalf("test file inventory = %+v", files.Files)
	}
	files, err = idx.Files(ctx, FileQuery{Path: "lib", Kind: FileGoSource})
	if err != nil {
		t.Fatalf("Files(lib): %v", err)
	}
	if files.Total != 2 {
		t.Fatalf("lib Go files = %+v, want 2", files.Files)
	}
	files, err = idx.Files(ctx, FileQuery{Limit: 1})
	if err != nil {
		t.Fatalf("Files(page): %v", err)
	}
	if !files.Truncated || files.Reason != "limit" || len(files.Files) != 1 {
		t.Fatalf("bounded file page = %+v", files)
	}

	packages, err := idx.Packages(ctx, PackageQuery{})
	if err != nil {
		t.Fatalf("Packages: %v", err)
	}
	byDir := map[string]PackageInfo{}
	for _, pkg := range packages.Packages {
		byDir[pkg.Dir] = pkg
	}
	if a := byDir["a"]; a.Imports != 1 {
		t.Errorf("a imports = %d, want 1", a.Imports)
	}
	if b := byDir["b"]; b.Importers != 1 {
		t.Errorf("b importers = %d, want 1", b.Importers)
	}
	// A package named main without a main function is not fabricated.
	entries, err := idx.Entrypoints(ctx)
	if err != nil {
		t.Fatalf("Entrypoints: %v", err)
	}
	if len(entries.Entrypoints) != 1 {
		t.Fatalf("entrypoints = %+v, want exactly cmd/app/main.go", entries.Entrypoints)
	}
	entry := entries.Entrypoints[0]
	if entry.Path != "cmd/app/main.go" || entry.QualifiedName != "main.main" {
		t.Errorf("entrypoint = %+v", entry)
	}

	configs, err := idx.Configs(ctx)
	if err != nil {
		t.Fatalf("Configs: %v", err)
	}
	seen := map[string]ConfigInfo{}
	for _, config := range configs.Configs {
		seen[config.Path] = config
	}
	if seen["go.mod"].Value != "example.com/app" {
		t.Errorf("go.mod config = %+v, want module path", seen["go.mod"])
	}
	if seen["Makefile"].Kind != "make" {
		t.Errorf("Makefile kind = %q, want make", seen["Makefile"].Kind)
	}
	if seen[".github/workflows/ci.yml"].Kind != "ci" {
		t.Errorf("workflow kind = %q, want ci", seen[".github/workflows/ci.yml"].Kind)
	}
	if _, ok := seen["README.md"]; ok {
		t.Error("documentation leaked into the configuration inventory")
	}
}

func TestTestsAreBoundedStaticAssociations(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"pkg/foo.go": "package pkg\n\nfunc Foo() {}\n",
		"pkg/foo_test.go": "package pkg\n\nimport \"testing\"\n\n" +
			"func TestFoo(t *testing.T) { Foo() }\n\n" +
			"func helper() { Foo() }\n\n" +
			"func TestOther(t *testing.T) {}\n",
	})
	idx := mustIndex(t, root)
	ctx := context.Background()

	all, err := idx.Tests(ctx, TestQuery{})
	if err != nil {
		t.Fatalf("Tests listing: %v", err)
	}
	if len(all.Tests) != 2 {
		t.Fatalf("test declarations = %+v, want TestFoo and TestOther", all.Tests)
	}
	for _, test := range all.Tests {
		if test.Symbol == "helper" {
			t.Fatal("a lowercase helper was fabricated into a test")
		}
	}

	associations, err := idx.Tests(ctx, TestQuery{Symbol: "Foo"})
	if err != nil {
		t.Fatalf("Tests(Foo): %v", err)
	}
	if !associations.Found {
		t.Fatalf("Foo not found: %+v", associations)
	}
	if len(associations.Associations) != 1 {
		t.Fatalf("associations = %+v, want exactly TestFoo", associations.Associations)
	}
	association := associations.Associations[0]
	if association.Test.Symbol != "TestFoo" || association.Relation != "calls" {
		t.Errorf("association = %+v, want TestFoo calls", association)
	}
	if association.Path != "pkg/foo_test.go" || association.Line == 0 {
		t.Errorf("association witness = %s:%d", association.Path, association.Line)
	}
	// The helper is a test-file reference but not a test declaration; it must
	// not be reported as a test association.
	for _, association := range associations.Associations {
		if association.Test.Symbol == "helper" {
			t.Fatal("a helper reference was attributed to a test")
		}
	}
}

func TestImpactComposesStaticEvidenceAndUnknowns(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() { b.Foo() }\n",
		"a/a_test.go": "package a\n\nimport (\n\t\"testing\"\n\t\"example.com/app/b\"\n)\n\n" +
			"func TestA(t *testing.T) { b.Foo() }\n",
		"b/b.go": "package b\n\nfunc Foo() {}\n",
		"c/c.go": "package c\n\nimport \"example.com/app/b\"\n\nvar _ = b.Foo\n",
	})
	idx := mustIndex(t, root)
	result, err := idx.Impact(context.Background(), ImpactQuery{Symbol: "Foo", Depth: 2, Limit: 50})
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if !result.Found || result.Ambiguous {
		t.Fatalf("Impact target = %+v, want unique Foo", result)
	}
	if len(result.Targets) != 1 || result.Targets[0].Path != "b/b.go" {
		t.Errorf("targets = %+v, want b/b.go", result.Targets)
	}
	if len(result.References) == 0 {
		t.Error("impact omitted static references")
	}
	if len(result.Callers) == 0 {
		t.Error("impact omitted direct callers")
	}
	if len(result.Importers) != 2 {
		t.Errorf("importers = %v, want a and c", result.Importers)
	}
	if len(result.Tests) == 0 || result.Tests[0].Test.Symbol != "TestA" {
		t.Errorf("test associations = %+v, want TestA", result.Tests)
	}
	if len(result.Nodes) == 0 || len(result.Edges) == 0 {
		t.Error("impact omitted its canonical node/edge projection")
	}
	if result.Snapshot.SnapshotID == "" || result.Snapshot.Generation == 0 {
		t.Error("impact result is detached from its snapshot")
	}
}

func TestImpactPathModeIsBoundedAndExplicit(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"pkg/a.go":   "package pkg\n\nfunc A() {}\nfunc B() {}\n",
		"use/use.go": "package use\n\nimport \"example.com/app/pkg\"\n\nfunc Use() { pkg.A() }\n",
	})
	idx := mustIndex(t, root)
	result, err := idx.Impact(context.Background(), ImpactQuery{Path: "pkg", Limit: 1})
	if err != nil {
		t.Fatalf("Impact(path): %v", err)
	}
	if result.QueryType != "path" || !result.Found {
		t.Fatalf("path impact = %+v", result)
	}
	if !result.Truncated || result.Reason != "target_limit" {
		t.Errorf("path target bound = truncated:%v reason:%q", result.Truncated, result.Reason)
	}
	if len(result.Importers) != 1 || result.Importers[0] != "use" {
		t.Errorf("importers = %v, want use", result.Importers)
	}
}
