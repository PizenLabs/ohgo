//go:build !cgo

package ast

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestNoCgoBuildDisclosesUnsupportedLanguages proves the portable build does
// not silently claim the tree-sitter adapters. It keeps Go and reports every
// other source extension as unsupported.
func TestNoCgoBuildDisclosesUnsupportedLanguages(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"main.go":    "package main\n\nfunc main() {}\n",
		"web/app.ts": `export function page() { return 1; }`,
	})
	idx, err := NewIndexWithOptions(root, IndexOptions{Adaptive: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.EnsureParsed(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := idx.Info()
	if info.GoFiles != 1 || info.ParsedFiles != 1 {
		t.Fatalf("Go coverage = %+v", info)
	}
	if info.StructuralComplete || !info.Partial {
		t.Fatalf("no-cgo snapshot claimed completeness: %+v", info)
	}
	foundUnsupported := false
	for _, item := range info.Unsupported {
		if item.Extension == ".ts" {
			foundUnsupported = true
		}
	}
	if !foundUnsupported {
		t.Fatalf("unsupported TS was not disclosed: %+v", info.Unsupported)
	}
	for _, language := range info.Languages {
		if language.Language == LanguageTypeScript {
			if language.Supported || language.Coverage != "UNSUPPORTED" {
				t.Fatalf("TypeScript coverage = %+v", language)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "web", "app.ts")); err != nil {
		t.Fatal(err)
	}
}
