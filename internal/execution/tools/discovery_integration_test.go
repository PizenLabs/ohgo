package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// Structural and lexical discovery are different tools for different questions,
// not competing indexes. A Go declaration must be found by the symbol index; a
// text occurrence, including one in a non-Go file, must be found by grep. This
// is the integration row the Phase 3 matrix asks for, not a Phase 2 benchmark.
func TestStructuralAndLexicalDiscoveryCoverDifferentEvidence(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"pkg/needle.go": "package pkg\n\nfunc Needle() {}\n",
		"notes.txt":     "Needle is described here in prose.\n",
	})

	symbols := NewSymbolsTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"symbol": "Needle",
	})
	if symbols.IsError {
		t.Fatalf("symbols: %s", symbols.Text)
	}
	if !strings.Contains(symbols.Text, "pkg/needle.go") {
		t.Errorf("the symbol index did not find the declaration: %s", symbols.Text)
	}
	if strings.Contains(symbols.Text, "notes.txt") {
		t.Errorf("the symbol index claimed a non-Go file as structural evidence: %s", symbols.Text)
	}

	grep := NewGrepTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"pattern": "Needle",
	})
	if grep.IsError {
		t.Fatalf("grep: %s", grep.Text)
	}
	for _, want := range []string{"pkg/needle.go", "notes.txt"} {
		if !strings.Contains(grep.Text, want) {
			t.Errorf("lexical search missed %s: %s", want, grep.Text)
		}
	}
}

// TestDiscoveryAndStructuralScopeShareDependencyExclusions proves a vendored
// Python tree pruned by glob is not silently parsed into the structural graph.
// Two different answers to "is this file part of the repository?" are a
// correctness bug, not a performance detail.
func TestDiscoveryAndStructuralScopeShareDependencyExclusions(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app/main.go":           "package main\n\nfunc main() {}\n",
		".venv/lib/site.go":     "package site\n",
		"node_modules/pkg/x.go": "package pkg\n",
		"target/debug/tool.go":  "package main\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	packages := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"view": "packages"})
	if packages.IsError {
		t.Fatalf("packages: %s", packages.Text)
	}
	for _, excluded := range []string{".venv", "node_modules", "target"} {
		if strings.Contains(packages.Text, excluded) {
			t.Errorf("structural index parsed %s:\n%s", excluded, packages.Text)
		}
	}
	if !strings.Contains(packages.Text, "dir: app") || !strings.Contains(packages.Text, "language: Go") {
		t.Fatalf("structural index omitted the repository source:\n%s", packages.Text)
	}

	glob := NewGlobTool(root)
	paths := globPaths(t, glob, map[string]any{"pattern": "**/*.go"})
	if len(paths) != 1 || paths[0] != "app/main.go" {
		t.Fatalf("glob paths = %v, want only app/main.go", paths)
	}
}
