package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// structureFixture writes a module-shaped repository with inventory facts.
func structureFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, ok := files["go.mod"]; !ok {
		files["go.mod"] = "module example.com/app\n\ngo 1.21\n"
	}
	return symbolsFixture(t, files)
}

func structureDetails(t *testing.T, result agent.Result) map[string]any {
	t.Helper()
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %T, want map[string]any", result.Details)
	}
	return details
}

func TestStructureToolAnswersInventoryViews(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"cmd/app/main.go":          "package main\n\nfunc main() {}\n",
		"pkg/a.go":                 "package pkg\n\nfunc A() {}\n",
		"pkg/a_test.go":            "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { A() }\n",
		"Makefile":                 "build:\n\tgo build ./...\n",
		".github/workflows/ci.yml": "name: ci\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	overview := tool.Execute(ctx, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	details := structureDetails(t, overview)
	if details["view"] != "overview" || details["status"] != "ok" {
		t.Fatalf("overview details = %+v", details)
	}
	if details["snapshot_id"] == "" || details["generation"] == nil {
		t.Errorf("overview lacks snapshot provenance: %+v", details)
	}
	if !strings.Contains(overview.Text, "pending_files: 3") ||
		!strings.Contains(overview.Text, "languages: Go=UNPARSED/3") {
		t.Errorf("adaptive overview did not disclose its unparsed inventory:\n%s", overview.Text)
	}

	files := tool.Execute(ctx, map[string]any{"view": "files", "path": "pkg", "package": "pkg"})
	if files.IsError {
		t.Fatalf("files: %s", files.Text)
	}
	fileRows, _ := structureDetails(t, files)["files"].([]map[string]any)
	if len(fileRows) != 2 {
		t.Fatalf("files rows = %+v, want two pkg files", fileRows)
	}

	entrypoints := tool.Execute(ctx, map[string]any{"view": "entrypoints"})
	if entrypoints.IsError {
		t.Fatalf("entrypoints: %s", entrypoints.Text)
	}
	if !strings.Contains(entrypoints.Text, "main.main") {
		t.Errorf("entrypoint text omitted main.main:\n%s", entrypoints.Text)
	}
	if !strings.Contains(entrypoints.Text, "parsed_files: 3") {
		t.Errorf("targeted entrypoint view did not parse the source:\n%s", entrypoints.Text)
	}

	configs := tool.Execute(ctx, map[string]any{"view": "configs"})
	if configs.IsError {
		t.Fatalf("configs: %s", configs.Text)
	}
	if !strings.Contains(configs.Text, "Makefile") || !strings.Contains(configs.Text, ".github/workflows/ci.yml") {
		t.Errorf("config inventory omitted an allowlisted fact:\n%s", configs.Text)
	}

	testsView := tool.Execute(ctx, map[string]any{"view": "tests", "symbol": "A"})
	if testsView.IsError {
		t.Fatalf("tests: %s", testsView.Text)
	}
	if !strings.Contains(testsView.Text, "TestA") || !strings.Contains(testsView.Text, "not coverage") {
		t.Errorf("test association output = %s", testsView.Text)
	}

	impact := tool.Execute(ctx, map[string]any{"view": "impact", "symbol": "A"})
	if impact.IsError {
		t.Fatalf("impact: %s", impact.Text)
	}
	if !strings.Contains(impact.Text, "references:") {
		t.Errorf("impact output omitted reference composition:\n%s", impact.Text)
	}
}

func TestStructureToolSharesOneSnapshot(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() { b.B() }\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
	})
	source := NewStructuralSource(root)
	symbols := NewSymbolsToolWithSource(source)
	graph := NewGraphTool(source)
	refs := NewRefsTool(source)
	structure := NewStructureTool(source)
	ctx := testContext()

	if symbols.Source() != source || graph.Source() != source || refs.Source() != source || structure.Source() != source {
		t.Fatal("structural tools do not share one StructuralSource")
	}
	first, err := source.Index()
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Index()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("StructuralSource built more than one index")
	}

	results := []agent.Result{
		symbols.Execute(ctx, map[string]any{"symbol": "B"}),
		graph.Execute(ctx, map[string]any{"package": "a"}),
		refs.Execute(ctx, map[string]any{"symbol": "B"}),
		structure.Execute(ctx, map[string]any{}),
	}
	var snapshotID any
	var generation any
	for i, result := range results {
		if result.IsError {
			t.Fatalf("tool %d: %s", i, result.Text)
		}
		details := structureDetails(t, result)
		if snapshotID == nil {
			snapshotID, generation = details["snapshot_id"], details["generation"]
			continue
		}
		if details["snapshot_id"] != snapshotID || details["generation"] != generation {
			t.Fatalf("tool %d saw a different snapshot: %v/%v vs %v/%v",
				i, details["snapshot_id"], details["generation"], snapshotID, generation)
		}
	}
	if snapshotID == nil || snapshotID == "" {
		t.Fatal("structural tools did not report a snapshot identity")
	}
}

func TestStructureToolHonoursGitignoreScope(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"main.go":        "package main\n\nfunc main() {}\n",
		"ignored/gen.go": "package ignored\n\nfunc Generated() {}\n",
		".gitignore":     "ignored/\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	files := tool.Execute(ctx, map[string]any{"view": "files"})
	if files.IsError {
		t.Fatalf("files: %s", files.Text)
	}
	if strings.Contains(files.Text, "ignored/gen.go") {
		t.Fatalf("gitignored file appeared in inventory:\n%s", files.Text)
	}

	// A rule change must refresh repository membership without a Go edit.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("# no rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files = tool.Execute(ctx, map[string]any{"view": "files"})
	if files.IsError {
		t.Fatalf("files after rule change: %s", files.Text)
	}
	if !strings.Contains(files.Text, "ignored/gen.go") {
		t.Fatalf("newly included file missing after rule change:\n%s", files.Text)
	}
}

// A nested repository is a project boundary, not a directory the enclosing
// repository's .gitignore may silently erase. The exact failure this guards:
// a root rule "backend/" plus backend/.git made every backend Go file and test
// disappear from the inventory.
func TestStructureToolIncludesNestedRepositoryBoundary(t *testing.T) {
	root := structureFixture(t, map[string]string{
		".gitignore":                           "backend/\n",
		"backend/.git/HEAD":                    "ref: refs/heads/main\n",
		"backend/go.mod":                       "module example.com/backend\n\ngo 1.21\n",
		"backend/go.work":                      "go 1.21\n\nuse .\n",
		"backend/cmd/api/main.go":              "package main\n\nfunc main() {}\n",
		"backend/internal/order/order_test.go": "package order\n\nimport \"testing\"\n\nfunc TestOrder(t *testing.T) {}\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	overview := tool.Execute(ctx, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	details := structureDetails(t, overview)
	snapshot, _ := details["snapshot"].(map[string]any)
	if snapshot["go_files"] != 2 {
		t.Fatalf("go_files = %v, want the nested repository's two Go files:\n%s",
			snapshot["go_files"], overview.Text)
	}
	if details["test_files"] != 1 {
		t.Errorf("test_files = %v, want the nested test file", details["test_files"])
	}
	if details["scope_complete"] != true {
		t.Errorf("scope_complete = %v, want true; a nested repository is included:\n%s",
			details["scope_complete"], overview.Text)
	}
	nested, _ := details["nested_projects"].([]map[string]any)
	if len(nested) != 1 || nested[0]["path"] != "backend" || nested[0]["included"] != true {
		t.Fatalf("nested_projects = %+v, want an included backend boundary", nested)
	}
	if rule, _ := nested[0]["parent_scope_rule"].(string); !strings.Contains(rule, ".gitignore:1") {
		t.Errorf("parent_scope_rule = %q, want the overridden root rule", rule)
	}
	if !strings.Contains(overview.Text, "nested_projects: 1") ||
		!strings.Contains(overview.Text, "backend") {
		t.Errorf("overview did not disclose the nested project:\n%s", overview.Text)
	}

	files := tool.Execute(ctx, map[string]any{"view": "files", "path": "backend"})
	if files.IsError {
		t.Fatalf("files: %s", files.Text)
	}
	if !strings.Contains(files.Text, "backend/cmd/api/main.go") ||
		!strings.Contains(files.Text, "backend/internal/order/order_test.go") {
		t.Fatalf("nested repository files are missing:\n%s", files.Text)
	}

	testsView := tool.Execute(ctx, map[string]any{"view": "tests"})
	if testsView.IsError {
		t.Fatalf("tests: %s", testsView.Text)
	}
	if !strings.Contains(testsView.Text, "TestOrder") {
		t.Fatalf("nested test was not discovered:\n%s", testsView.Text)
	}

	scopeView := tool.Execute(ctx, map[string]any{"view": "scope"})
	if scopeView.IsError {
		t.Fatalf("scope: %s", scopeView.Text)
	}
	if !strings.Contains(scopeView.Text, "included: true") ||
		!strings.Contains(scopeView.Text, "backend") {
		t.Errorf("scope view omitted the included nested project:\n%s", scopeView.Text)
	}
}

// An ordinary ignored directory is still excluded, but it is named rather than
// disappearing. A zero source count is then visibly scoped, not a clean
// repository-wide absence.
func TestStructureToolDisclosesExcludedRoot(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"main.go":        "package main\n\nfunc main() {}\n",
		"ignored/gen.go": "package ignored\n\nfunc Generated() {}\n",
		".gitignore":     "ignored/\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	overview := tool.Execute(ctx, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	details := structureDetails(t, overview)
	if details["scope_complete"] != false {
		t.Errorf("scope_complete = %v, want false", details["scope_complete"])
	}
	excluded, _ := details["excluded_roots"].([]map[string]any)
	if len(excluded) != 1 || excluded[0]["path"] != "ignored" {
		t.Fatalf("excluded_roots = %+v, want ignored/", excluded)
	}
	if rule, _ := excluded[0]["rule"].(string); !strings.Contains(rule, ".gitignore:1") {
		t.Errorf("exclusion rule = %q, want the decisive .gitignore line", rule)
	}
	if !strings.Contains(overview.Text, "excluded_roots: 1 (contents not indexed)") ||
		!strings.Contains(overview.Text, "path: ignored") {
		t.Errorf("overview did not disclose the excluded root:\n%s", overview.Text)
	}
	if strings.Contains(overview.Text, "ignored/gen.go") {
		t.Errorf("excluded file leaked into the inventory:\n%s", overview.Text)
	}

	// The scope view owns freshness too: removing the rule must clear the
	// exclusion without requiring a separate inventory query first.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("# no rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scopeView := tool.Execute(ctx, map[string]any{"view": "scope"})
	if scopeView.IsError {
		t.Fatalf("scope after rule removal: %s", scopeView.Text)
	}
	if !strings.Contains(scopeView.Text, "scope: COMPLETE") ||
		!strings.Contains(scopeView.Text, "excluded_roots: 0") {
		t.Errorf("scope view did not refresh after the rule changed:\n%s", scopeView.Text)
	}
}

// A file-level rule can hide a source file without excluding its directory.
// The scope report must name that gap, or a complete-looking test inventory
// could still prove a false absence.
func TestStructureToolDisclosesExcludedSourceFile(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"main.go":      "package main\n\nfunc main() {}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) {}\n",
		".gitignore":   "*_test.go\n",
	})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	overview := tool.Execute(ctx, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	details := structureDetails(t, overview)
	if details["scope_complete"] != false {
		t.Errorf("scope_complete = %v, want false", details["scope_complete"])
	}
	if details["excluded_source_files"] != 1 {
		t.Errorf("excluded_source_files = %v, want 1", details["excluded_source_files"])
	}
	if !strings.Contains(overview.Text, "excluded_source_files: 1") ||
		!strings.Contains(overview.Text, "main_test.go") {
		t.Errorf("overview did not disclose the excluded source file:\n%s", overview.Text)
	}
}

func TestStructureToolMarksARepeatedOverview(t *testing.T) {
	root := structureFixture(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	first := tool.Execute(ctx, map[string]any{})
	second := tool.Execute(ctx, map[string]any{})
	if first.IsError || second.IsError {
		t.Fatalf("overviews failed: %s / %s", first.Text, second.Text)
	}
	if strings.Contains(first.Text, "repeated: true") {
		t.Errorf("first overview was marked repeated:\n%s", first.Text)
	}
	if !strings.Contains(second.Text, "repeated: true") {
		t.Errorf("second overview of the same snapshot was not marked repeated:\n%s", second.Text)
	}
	if repeated, _ := structureDetails(t, second)["repeated"].(bool); !repeated {
		t.Errorf("second overview details did not carry repeated: %+v", structureDetails(t, second))
	}
}

func TestStructureToolRejectsEscapeAndUnknownArguments(t *testing.T) {
	root := structureFixture(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	tool := NewStructureTool(NewStructuralSource(root))
	ctx := testContext()

	if result := tool.Execute(ctx, map[string]any{"path": "../outside"}); !result.IsError {
		t.Fatalf("path escape was accepted: %s", result.Text)
	}
	if result := tool.Execute(ctx, map[string]any{"bogus": true}); !result.IsError {
		t.Fatalf("unknown argument was accepted: %s", result.Text)
	}
	if result := tool.Execute(ctx, map[string]any{"view": "impact"}); !result.IsError {
		t.Fatalf("impact without a selector was accepted: %s", result.Text)
	}
}

func TestStructureToolIsReadOnlyAndDiscoverable(t *testing.T) {
	root := structureFixture(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	tool := NewStructureTool(NewStructuralSource(root))
	if tool.Name() != "structure" {
		t.Errorf("name = %q, want structure", tool.Name())
	}
	if tool.Approval().Tier != agent.TierRead {
		t.Errorf("tier = %q, want read", tool.Approval().Tier)
	}
	if len(tool.SupportedLanguages()) != 0 {
		t.Errorf("structure declared a language requirement: %v", tool.SupportedLanguages())
	}
	if tool.RequiredLanguage() != "" {
		t.Errorf("required language = %q, want no single-language gate", tool.RequiredLanguage())
	}
	if !agent.ReadOnlyToolNames[tool.Name()] {
		t.Error("structure is missing from ReadOnlyToolNames")
	}
	result := tool.Execute(testContext(), map[string]any{})
	if result.Mutates || result.Executes || result.Verifies {
		t.Errorf("structure reported a mutation/execution capability: %+v", result)
	}
}

// TestDescribeStructuralNeverCallsUnparseableSourcePartial pins the four-state
// verdict: a snapshot whose source languages have no adapter in this build is
// UNSUPPORTED, not PARTIAL. Reading it as PARTIAL makes a model wait for
// structural evidence that can never arrive.
func TestDescribeStructuralNeverCallsUnparseableSourcePartial(t *testing.T) {
	noAdapters := []ast.LanguageCoverage{
		{Language: ast.LanguageJavaScript, Files: 1, Coverage: "UNSUPPORTED"},
		{Language: ast.LanguageTypeScript, Files: 1, Coverage: "UNSUPPORTED"},
		{Language: ast.LanguageTSX, Files: 3, Coverage: "UNSUPPORTED"},
	}
	unsupported := ast.SnapshotInfo{
		SourceFiles: 5,
		Languages:   noAdapters,
		Unsupported: []ast.UnsupportedLanguage{
			{Extension: ".mjs", Files: 1},
			{Extension: ".ts", Files: 1},
			{Extension: ".tsx", Files: 3},
		},
	}
	goSource := []ast.LanguageCoverage{
		{Language: ast.LanguageGo, Files: 3, Supported: true, Coverage: "PARTIAL"},
	}
	cases := []struct {
		name   string
		info   ast.SnapshotInfo
		label  string
		detail string
	}{
		{
			name:  "complete",
			info:  ast.SnapshotInfo{StructuralComplete: true, SourceFiles: 3, ParsedFiles: 3, Languages: goSource},
			label: "COMPLETE",
		},
		{
			name:   "pending",
			info:   ast.SnapshotInfo{SourceFiles: 5, PendingFiles: 2, Languages: goSource},
			label:  "UNPARSED",
			detail: "2 supported source files not parsed yet",
		},
		{
			name:   "no adapter",
			info:   unsupported,
			label:  "UNSUPPORTED",
			detail: "no adapter for .mjs, .ts, .tsx; 0 of 5 source files parsed",
		},
		{
			name:   "parse failure",
			info:   ast.SnapshotInfo{SourceFiles: 3, ParsedFiles: 1, FailedFiles: 1, Languages: goSource},
			label:  "PARTIAL",
			detail: "1 of 3 source files failed to parse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := describeStructural(tc.info)
			if state.Label != tc.label {
				t.Fatalf("label = %q, want %q", state.Label, tc.label)
			}
			if state.Detail != tc.detail {
				t.Errorf("detail = %q, want %q", state.Detail, tc.detail)
			}
			if got := structuralCompleteness(tc.info); got != tc.label {
				t.Errorf("structuralCompleteness = %q, want %q", got, tc.label)
			}
		})
	}
}

// TestStructureToolNamesTheSliceBehindTruncation pins that a truncated
// overview says which list it cut and against which total. Without the count
// the model must issue a second inventory query to learn that the file total
// it already received was complete.
func TestStructureToolNamesTheSliceBehindTruncation(t *testing.T) {
	files := map[string]string{}
	for i := 1; i <= 14; i++ {
		files[fmt.Sprintf("pkg/f%02d.go", i)] = fmt.Sprintf("package pkg\n\nfunc F%02d() {}\n", i)
	}
	root := symbolsFixture(t, files)
	tool := NewStructureTool(NewStructuralSource(root))

	overview := tool.Execute(testContext(), map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	if !strings.Contains(overview.Text, "result: TRUNCATED (files 10 of 14)") {
		t.Errorf("overview did not name the truncated slice:\n%s", overview.Text)
	}
	if !strings.Contains(overview.Text, "result_reason: file_limit") {
		t.Errorf("overview did not name the decisive bound:\n%s", overview.Text)
	}
	// An overview does not parse, so its declaration totals are not zero
	// observations; they are simply not made yet.
	if !strings.Contains(overview.Text, "symbols: 0 (source not parsed)") {
		t.Errorf("overview reported unparsed declarations as an observed zero:\n%s", overview.Text)
	}
	bounds, ok := structureDetails(t, overview)["bounds"].([]map[string]any)
	if !ok {
		t.Fatalf("bounds = %T, want []map[string]any", structureDetails(t, overview)["bounds"])
	}
	if len(bounds) != 1 || bounds[0]["slice"] != "files" || bounds[0]["shown"] != 10 || bounds[0]["total"] != 14 {
		t.Errorf("bounds = %+v, want only the cut slice files 10 of 14", bounds)
	}
}
