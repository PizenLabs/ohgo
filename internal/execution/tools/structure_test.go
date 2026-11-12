package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
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
