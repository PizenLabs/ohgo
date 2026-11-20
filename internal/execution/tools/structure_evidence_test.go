package tools

import (
	"strings"
	"testing"
)

// A metadata-first overview cannot report zero packages or entrypoints as an
// observation. Before parsing, zero is an unobserved count and must be
// qualified so a model does not conclude absence.
func TestOverviewQualifiesUnparsedCounts(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"cmd/app/main.go": "package main\n\nfunc main() {}\n",
		"pkg/a.go":        "package pkg\n\nfunc A() {}\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{"view": "overview"})
	if result.IsError {
		t.Fatalf("overview: %s", result.Text)
	}
	for _, want := range []string{
		"packages: 0 (source not parsed)",
		"entrypoints: 0 (verified: 0, discovered: 0; not established: source not parsed)",
	} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("overview = %s\nmissing %q", result.Text, want)
		}
	}
}

// A symbol test query has two lists. Counts must describe the list they
// belong to; returned must never exceed the total it is compared with.
func TestTestsViewSeparatesUniqueTestsFromAssociations(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"pkg/a.go":      "package pkg\n\nfunc A() {}\n",
		"pkg/a_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { A() }\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{"view": "tests", "symbol": "A"})
	if result.IsError {
		t.Fatalf("tests: %s", result.Text)
	}
	for _, want := range []string{
		"returned: 1",
		"total: 1",
		"associations_returned: 1",
		"associations_total: 1",
	} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("tests view = %s\nmissing %q", result.Text, want)
		}
	}
}
