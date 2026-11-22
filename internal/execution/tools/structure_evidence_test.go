package tools

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
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

// A project analysis reports the survey, including a module that has no
// sampled source coordinate. The view remains metadata-first.
func TestStructureProjectAnalysisReportsSurveyCoverage(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"alpha/a.go":     "package alpha\n\nfunc A() {}\n",
		"beta/b_test.go": "package beta\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {}\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{
		"view": "analysis", "intent": "project",
	})
	if result.IsError {
		t.Fatalf("analysis: %s", result.Text)
	}
	if !strings.Contains(result.Text, "survey: level=") {
		t.Fatalf("analysis output omits the survey:\n%s", result.Text)
	}
	details := structureDetails(t, result)
	survey, ok := details["survey"].(map[string]any)
	if !ok {
		t.Fatalf("survey = %T", details["survey"])
	}
	unexplored, _ := survey["unexplored"].([]string)
	if len(unexplored) == 0 {
		t.Fatalf("survey = %+v, want the test-only module named", survey)
	}
	if pending, _ := details["pending_files"].(int); pending == 0 {
		t.Fatalf("details = %+v, want pending source", details)
	}
}

// A drift analysis is metadata-first: comparing documented paths with the
// inventory does not require parsing source, and the snapshot reports the
// pending source rather than implying the whole repository was analysed.
func TestStructureDriftComparesDocumentationWithoutParsingSource(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"README.md": "# App\n\nUse `pkg/a.go`; `docs/missing.yaml` does not exist.\n",
		"pkg/a.go":  "package pkg\n\nfunc A() {}\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{
		"view": "analysis", "intent": "drift",
	})
	if result.IsError {
		t.Fatalf("drift: %s", result.Text)
	}
	for _, want := range []string{"documentation_drift: claims=", "docs/missing.yaml"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("drift output = %s\nmissing %q", result.Text, want)
		}
	}
	details := structureDetails(t, result)
	drift, ok := details["documentation_drift"].(map[string]any)
	if !ok {
		t.Fatalf("documentation_drift = %T", details["documentation_drift"])
	}
	findings, ok := drift["findings"].([]map[string]any)
	if !ok || len(findings) != 1 || findings[0]["target"] != "docs/missing.yaml" {
		t.Fatalf("findings = %+v, want the missing documented path", drift["findings"])
	}
	pending, ok := details["pending_files"].(int)
	if !ok || pending == 0 {
		t.Fatalf("details = %+v, want pending source: drift parsed source", details)
	}
}

// A targeted files view is path-scoped: it parses only the selected directory
// and reports the rest as pending instead of paying for a whole repository
// parse or silently implying the whole snapshot was analysed.
func TestTargetedFilesViewParsesOnlySelectedPath(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"alpha/a.go": "package alpha\n\nfunc Alpha() {}\n",
		"beta/b.go":  "package beta\n\nfunc Beta() {}\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{
		"view": "files", "path": "alpha", "activation": "targeted",
	})
	if result.IsError {
		t.Fatalf("files: %s", result.Text)
	}
	details := structureDetails(t, result)
	snapshot, ok := details["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot = %T", details["snapshot"])
	}
	if snapshot["parsed_files"] != 1 || snapshot["pending_files"] != 1 {
		t.Fatalf("snapshot = %+v, want parsed=1 pending=1", snapshot)
	}
	files, ok := details["files"].([]map[string]any)
	if !ok || len(files) != 1 {
		t.Fatalf("files = %+v, want the alpha row", details["files"])
	}
	if files[0]["state"] != string(ast.ParseStateParsed) {
		t.Fatalf("file state = %v, want PARSED", files[0]["state"])
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
