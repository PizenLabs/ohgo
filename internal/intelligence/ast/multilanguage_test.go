//go:build cgo

package ast

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// multiLanguageFixture contains one concrete syntax shape per supported
// adapter. It is intentionally small: the test asserts canonical facts, not
// grammar conformance.
func multiLanguageFixture(t *testing.T) string {
	t.Helper()
	return graphFixture(t, map[string]string{
		"web/util.ts": `export function uniqueHelper(x: number): number { return x * 2; }
export interface Shape { area(): number }
export class Circle implements Shape {
  radius: number;
  constructor(r: number) { this.radius = r; }
  area(): number { return uniqueHelper(this.radius); }
}
`,
		"web/app.ts": `import { uniqueHelper } from "./util";
export function caller(): number { return uniqueHelper(1); }
test("caller works", () => { caller(); });
`,
		"java/com/example/Service.java": `package com.example;
public class Service {
  int run() { return uniqueHelper(1); }
}
`,
		"rust/lib.rs": `pub trait Area { fn area(&self) -> f64; }
pub struct Square { side: f64 }
impl Area for Square { fn area(&self) -> f64 { self.side } }
pub fn entry_helper() -> f64 { 1.0 }
#[test]
fn it_works() { entry_helper(); }
fn main() { entry_helper(); }
`,
		"csharp/Service.cs": `namespace Example;
public interface IArea { double Area(); }
public class Square : IArea {
  public double Area() { return Helper(); }
  static double Helper() { return 1.0; }
}
`,
		"kotlin/Service.kt": `package example
interface Area { fun area(): Double }
data class Point(val x: Int, val y: Int)
class Square : Area { override fun area(): Double = 1.0 }
fun main() { Square().area() }
`,
		"ruby/service.rb": `module Example
  class Base
    def run
      helper(1)
    end
  end
  def helper(x)
    x
  end
end
`,
		"cpp/service.cpp": `#include "service.hpp"
namespace app {
class Base {};
class Service : public Base {
public:
  int run() { return helper(1); }
  int helper(int x) { return x; }
};
}
`,
	})
}

func openAdaptive(t *testing.T, root string) *Index {
	t.Helper()
	idx, err := NewIndexWithOptions(root, IndexOptions{Adaptive: true})
	if err != nil {
		t.Fatalf("NewIndexWithOptions: %v", err)
	}
	return idx
}

func TestAdaptiveOverviewDoesNotParseSource(t *testing.T) {
	root := multiLanguageFixture(t)
	idx := openAdaptive(t, root)

	info := idx.Info()
	if info.ParsedFiles != 0 || info.PendingFiles == 0 {
		t.Fatalf("inventory build parsed source: parsed=%d pending=%d", info.ParsedFiles, info.PendingFiles)
	}
	result, err := idx.Overview(context.Background(), 20)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if result.Snapshot.ParsedFiles != 0 {
		t.Fatalf("overview parsed source files: %+v", result.Snapshot)
	}
	if len(result.Languages) < 7 {
		t.Fatalf("overview languages = %+v, want the observed language mix", result.Languages)
	}
	for _, coverage := range result.Languages {
		if !coverage.Supported {
			t.Fatalf("%s is unsupported in the cgo adapter build: %+v", coverage.Language, coverage)
		}
		if coverage.Coverage != "UNPARSED" {
			t.Errorf("%s coverage = %s, want UNPARSED before a structural query", coverage.Language, coverage.Coverage)
		}
	}
	if result.Snapshot.StructuralComplete {
		t.Fatal("an unparsed overview claimed structural completeness")
	}
}

func TestTargetedLookupParsesTheCanonicalMixAndResolvesCrossLanguageCalls(t *testing.T) {
	root := multiLanguageFixture(t)
	idx := openAdaptive(t, root)

	matches, err := idx.Lookup("uniqueHelper")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("uniqueHelper matches = %+v, want one", matches)
	}
	if matches[0].Language != LanguageTypeScript || matches[0].Canonical != SymbolFunction {
		t.Fatalf("uniqueHelper = %+v, want a TypeScript function", matches[0])
	}

	refs, err := idx.References(context.Background(), RefQuery{Symbol: "uniqueHelper"})
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if !refs.Found || refs.Ambiguous {
		t.Fatalf("uniqueHelper refs = found:%v ambiguous:%v", refs.Found, refs.Ambiguous)
	}
	paths := map[string]bool{}
	for _, ref := range refs.References {
		paths[ref.Path] = true
	}
	if !paths["web/app.ts"] || !paths["java/com/example/Service.java"] {
		t.Fatalf("cross-language call sites = %+v, want web/app.ts and the Java caller", refs.References)
	}
	callers, err := idx.References(context.Background(), RefQuery{
		Symbol: "uniqueHelper", Relation: RelationCallers, Limit: 20,
	})
	if err != nil {
		t.Fatalf("callers: %v", err)
	}
	if len(callers.Edges) == 0 {
		t.Logf("callers=%+v target=%+v unknown=%+v", callers, callers.Target, callers.Unknown)
		t.Fatal("callers returned no static call edge")
	}

	info := idx.Info()
	if info.ParsedFiles != info.SourceFiles || info.PendingFiles != 0 {
		t.Fatalf("targeted query did not publish a fully parsed snapshot: %+v", info)
	}
	if !info.StructuralComplete {
		t.Fatalf("supported fixture is not structurally complete: %+v", info)
	}
}

func TestExplicitInheritanceEdgesAreSourceDerived(t *testing.T) {
	root := multiLanguageFixture(t)
	idx := openAdaptive(t, root)

	impact, err := idx.Impact(context.Background(), ImpactQuery{
		Symbol: "Circle", Depth: 1, Limit: 50,
	})
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if !impact.Found || impact.Ambiguous {
		t.Fatalf("Circle impact = found:%v ambiguous:%v candidates:%+v",
			impact.Found, impact.Ambiguous, impact.Candidates)
	}
	kinds := map[EdgeKind]bool{}
	for _, edge := range impact.Edges {
		kinds[edge.Kind] = true
		if edge.Source.Path == "" || edge.Source.StartLine == 0 {
			t.Errorf("edge lacks a source witness: %+v", edge)
		}
	}
	if !kinds[EdgeImplements] {
		t.Fatalf("Circle edges = %+v, want IMPLEMENTS Shape", impact.Edges)
	}
}

func TestResolvedImportsAppearAsCanonicalEdges(t *testing.T) {
	root := multiLanguageFixture(t)
	idx := openAdaptive(t, root)
	impact, err := idx.Impact(context.Background(), ImpactQuery{
		Symbol: "caller", Depth: 1, Limit: 50,
	})
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	found := false
	for _, edge := range impact.Edges {
		if edge.Kind == EdgeImports && edge.Source.Path == "web/app.ts" {
			found = true
			if edge.Source.StartLine == 0 || edge.Language != LanguageTypeScript {
				t.Errorf("import edge provenance = %+v", edge.Source)
			}
		}
	}
	if !found {
		t.Fatalf("caller impact edges = %+v, want a resolved web/app.ts IMPORTS edge", impact.Edges)
	}
}

func TestUnsupportedSourceIsDisclosedAndBlocksCompleteness(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
		"tool.py": "def main():\n    pass\n",
	})
	idx := openAdaptive(t, root)
	info := idx.Info()
	if len(info.Unsupported) != 1 || info.Unsupported[0].Extension != ".py" {
		t.Fatalf("unsupported = %+v, want .py", info.Unsupported)
	}
	if info.StructuralComplete || !info.Partial {
		t.Fatalf("unsupported source did not make the snapshot partial: %+v", info)
	}
	if _, err := idx.Lookup("main"); err != nil {
		t.Fatalf("Lookup with unsupported sibling: %v", err)
	}
	if idx.Info().StructuralComplete {
		t.Fatal("a supported lookup upgraded unsupported source to complete")
	}
}

func TestIncrementalMultiLanguageReparsesOnlyTheChangedFile(t *testing.T) {
	root := multiLanguageFixture(t)
	idx := openAdaptive(t, root)
	if _, err := idx.Lookup("uniqueHelper"); err != nil {
		t.Fatal(err)
	}
	info := idx.Info()
	if info.ParsedFiles == 0 {
		t.Fatal("targeted lookup did not parse")
	}
	path := filepath.Join(root, "web", "util.ts")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("\nexport function addedLater(): number { return uniqueHelper(2); }\n")...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	matches, err := idx.Lookup("addedLater")
	if err != nil {
		t.Fatalf("Lookup after edit: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("addedLater = %+v, want one match", matches)
	}
	info = idx.Info()
	if info.LastParsedFiles != 1 {
		t.Fatalf("LastParsedFiles = %d after one TypeScript edit, want 1", info.LastParsedFiles)
	}
	if info.LastReusedFiles == 0 {
		t.Fatal("incremental update reused no unchanged files")
	}
}

func TestSemanticActivationIsExplicitlyUnsupported(t *testing.T) {
	if ActivationSemantic.Supported() {
		t.Fatal("semantic activation is not implemented but reported supported")
	}
	if ActivationOverview.ParsesSource() {
		t.Fatal("overview activation claimed source parsing")
	}
	if !ActivationImpact.ParsesSource() {
		t.Fatal("impact activation must parse source")
	}
}
