//go:build cgo

package tools

import (
	"strings"
	"testing"
)

// mixedLanguageProject is the production-path fixture for the capability the
// portable build cannot provide: Go and TypeScript/TSX/JavaScript in one
// snapshot, sharing one adapter set and one graph.
func mixedLanguageProject(t *testing.T) string {
	t.Helper()
	return symbolsFixture(t, map[string]string{
		"tsconfig.json": `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./*"]}}}`,
		"go.mod":        "module example.com/mixed\n\ngo 1.21\n",
		"service/main.go": `package main

import "example.com/mixed/service"

func main() { service.Run() }
`,
		"service/run.go": `package service

func Run() { helper() }
func helper() {}
`,
		"app/page.tsx": `import { Widget } from "@/components/Widget";
export default function Page() { return <Widget />; }
`,
		"components/Widget.tsx": `export function Widget() { return null; }
`,
		"lib/format.ts": `export function format(value: string): string { return value; }
`,
		"scripts/build.mjs": `export async function build() { return 1; }
`,
		"tools/report.py": `def report(value):
    return value
`,
		// Not a supported source extension: inventory metadata only. It must
		// not turn a parsed project into an unsupported one.
		"components/legacy.vue": `<template><div /></template>
`,
	})
}

// The structure tool is the runtime surface an agent actually calls. A
// TypeScript project must reach the same adapters, snapshot, and graph the
// index tests exercise; proving the adapter in isolation does not prove the
// production path selects it.
func TestStructureToolParsesMixedLanguageProject(t *testing.T) {
	root := mixedLanguageProject(t)
	source := NewStructuralSource(root)
	tool := NewStructureTool(source)
	ctx := testContext()

	overview := tool.Execute(ctx, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	for _, want := range []string{
		"tree-sitter-typescript", "tree-sitter-tsx", "tree-sitter-javascript",
		"tree-sitter-python",
		"TypeScript=UNPARSED/", "TypeScript JSX=UNPARSED/", "JavaScript=UNPARSED/",
		"Python=UNPARSED/",
	} {
		if !strings.Contains(overview.Text, want) {
			t.Fatalf("overview omitted %q:\n%s", want, overview.Text)
		}
	}
	// The overview is metadata-first: it must not pay to parse source.
	if !strings.Contains(overview.Text, "parsed_files: 0") {
		t.Fatalf("overview parsed source before a targeted view:\n%s", overview.Text)
	}

	packages := tool.Execute(ctx, map[string]any{"view": "packages"})
	if packages.IsError {
		t.Fatalf("packages: %s", packages.Text)
	}
	// Every supported source file parsed. The unknown .vue extension is a
	// disclosed coverage gap, so the verdict is PARTIAL rather than
	// COMPLETE — but it is not UNSUPPORTED, and it does not suppress the
	// parsed facts.
	for _, want := range []string{
		"structural: PARTIAL",
		"parsed_files: 7 pending_files: 0 failed_files: 0",
		"unsupported_source: .vue(1)",
		"language: TypeScript JSX", "language: TypeScript", "language: JavaScript",
		"language: Python", "language: Go",
	} {
		if !strings.Contains(packages.Text, want) {
			t.Fatalf("packages view omitted %q:\n%s", want, packages.Text)
		}
	}

	// One snapshot serves both languages: the Go package and the TSX package
	// are rows in the same projection.
	if !strings.Contains(packages.Text, "dir: service") || !strings.Contains(packages.Text, "dir: components") {
		t.Fatalf("packages view did not mix Go and TypeScript:\n%s", packages.Text)
	}

	// Cross-file TypeScript resolution through the production tools, not the
	// index API: the page renders the component, and the alias import proves
	// the dependency edge.
	symbols := NewSymbolsToolWithSource(source).Execute(ctx, map[string]any{
		"symbol": "Widget", "language": "tsx",
	})
	if symbols.IsError || !strings.Contains(symbols.Text, "components/Widget.tsx") {
		t.Fatalf("symbols did not resolve the TSX declaration:\n%s", symbols.Text)
	}
	refs := NewRefsTool(source).Execute(ctx, map[string]any{
		"symbol": "Widget", "relation": "references",
	})
	if refs.IsError || !strings.Contains(refs.Text, "app/page.tsx") {
		t.Fatalf("refs did not resolve the JSX reference:\n%s", refs.Text)
	}
	relations := tool.Execute(ctx, map[string]any{
		"view": "relations", "symbol": "Widget",
	})
	if relations.IsError {
		t.Fatalf("relations: %s", relations.Text)
	}
	for _, want := range []string{"kind: REFERENCES", "kind: IMPORTS", "resolution: RESOLVED"} {
		if !strings.Contains(relations.Text, want) {
			t.Fatalf("relations omitted %q:\n%s", want, relations.Text)
		}
	}

	// Provenance: every claim above came from one identified snapshot.
	idx, err := source.Index()
	if err != nil {
		t.Fatal(err)
	}
	info := idx.Info()
	if info.SnapshotID == "" || info.Generation == 0 {
		t.Fatalf("snapshot identity missing: %+v", info)
	}
	if info.ParsedFiles != 7 || info.PendingFiles != 0 || info.StructuralComplete {
		t.Fatalf("snapshot coverage = parsed:%d pending:%d complete:%v",
			info.ParsedFiles, info.PendingFiles, info.StructuralComplete)
	}
	if len(info.Unsupported) != 1 || info.Unsupported[0].Extension != ".vue" {
		t.Fatalf("unknown extension was not disclosed as unsupported source: %+v", info.Unsupported)
	}
}

func TestStructureToolScopesPythonFileAndDirectory(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"backend/app.py": "def run():\n    return helper()\n\ndef helper():\n    return 1\n",
		"backend/lib.py": "def lib():\n    return 2\n",
		"web/app.ts":     "export function web() { return 1; }\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	file := tool.Execute(ctx, map[string]any{
		"view": "files", "path": "backend/app.py", "activation": "targeted",
	})
	if file.IsError {
		t.Fatalf("file target: %s", file.Text)
	}
	for _, want := range []string{"path: backend/app.py", "language: python"} {
		if !strings.Contains(file.Text, want) {
			t.Fatalf("file target omitted %q:\n%s", want, file.Text)
		}
	}
	if strings.Contains(file.Text, "web/app.ts") {
		t.Fatalf("file target leaked an out-of-scope path:\n%s", file.Text)
	}

	dir := tool.Execute(ctx, map[string]any{
		"view": "files", "path": "backend", "activation": "targeted",
	})
	if dir.IsError {
		t.Fatalf("directory target: %s", dir.Text)
	}
	for _, want := range []string{"path: backend/app.py", "path: backend/lib.py", "language: python"} {
		if !strings.Contains(dir.Text, want) {
			t.Fatalf("directory target omitted %q:\n%s", want, dir.Text)
		}
	}
	if strings.Contains(dir.Text, "web/app.ts") {
		t.Fatalf("directory target leaked an out-of-scope path:\n%s", dir.Text)
	}
}

// A recoverable syntax error is partial coverage, not a global failure. The
// declarations recovered from the rest of the project must remain usable and
// the limitation must be named.
func TestStructureToolKeepsParsedFactsWhenOneFileIsPartial(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"lib/good.ts": `export function good(): string { return "ok"; }
`,
		"lib/broken.ts": `export function broken( { 
`,
		"app/page.tsx": `import { good } from "../lib/good";
export default function Page() { return good(); }
`,
	})
	tool := NewStructureTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{"view": "packages"})
	if result.IsError {
		t.Fatalf("packages: %s", result.Text)
	}
	if !strings.Contains(result.Text, "structural: PARTIAL") {
		t.Fatalf("a recoverable syntax error was not disclosed as PARTIAL:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "parsed_files: 3") || !strings.Contains(result.Text, "failed_files: 1") {
		t.Fatalf("partial parse counts were misreported:\n%s", result.Text)
	}
	symbols := NewSymbolsToolWithSource(tool.Source()).Execute(testContext(), map[string]any{
		"symbol": "good",
	})
	if symbols.IsError || !strings.Contains(symbols.Text, "lib/good.ts") {
		t.Fatalf("a partial sibling suppressed the recovered declaration:\n%s", symbols.Text)
	}
}
