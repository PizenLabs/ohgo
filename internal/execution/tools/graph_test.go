package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

func graphFixtureRepo(t *testing.T) string {
	t.Helper()
	return symbolsFixture(t, map[string]string{
		"go.mod":   "module example.com/app\n\ngo 1.21\n",
		"a/a.go":   "package a\n\nimport \"example.com/app/b\"\n\nfunc A() {}\n",
		"b/b.go":   "package b\n\nimport \"example.com/app/c\"\n",
		"c/c.go":   "package c\n",
		"iso/i.go": "package iso\n",
	})
}

func graphStatus(t *testing.T, result agent.Result) string {
	t.Helper()
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %T, want map[string]any", result.Details)
	}
	status, _ := details["status"].(string)
	return status
}

func TestGraphToolFindsDependencyAndDependents(t *testing.T) {
	root := graphFixtureRepo(t)
	tool := NewGraphTool(NewStructuralSource(root))

	result := tool.Execute(testContext(), map[string]any{"package": "a", "relation": "imports", "depth": 2.0})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if status := graphStatus(t, result); status != "ok" {
		t.Errorf("status = %q, want ok: %s", status, result.Text)
	}
	for _, want := range []string{
		"path: a", "path: b", "path: c",
		"from: a", "to: b", "source_evidence: a/a.go:3",
		"from: b", "to: c",
	} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("text omits %q:\n%s", want, result.Text)
		}
	}
	details := result.Details.(map[string]any)
	if details["relation"] != "imports" {
		t.Errorf("relation = %v", details["relation"])
	}
	if details["found"] != true {
		t.Errorf("found = %v", details["found"])
	}

	reverse := tool.Execute(testContext(), map[string]any{"package": "c", "relation": "imported_by", "depth": 2.0})
	if reverse.IsError {
		t.Fatalf("reverse error: %s", reverse.Text)
	}
	if !strings.Contains(reverse.Text, "from: b") || !strings.Contains(reverse.Text, "from: a") {
		t.Errorf("reverse text omits the importer chain:\n%s", reverse.Text)
	}
}

func TestGraphToolReportsTruncationExplicitly(t *testing.T) {
	root := graphFixtureRepo(t)
	tool := NewGraphTool(NewStructuralSource(root))

	result := tool.Execute(testContext(), map[string]any{"package": "a", "depth": 1.0})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if !strings.Contains(result.Text, "status: truncated") ||
		!strings.Contains(result.Text, "truncated: true") ||
		!strings.Contains(result.Text, "truncation_reason: depth") {
		t.Errorf("depth-bound result did not disclose truncation:\n%s", result.Text)
	}
}

func TestGraphToolRejectsMalformedArguments(t *testing.T) {
	root := graphFixtureRepo(t)
	tool := NewGraphTool(NewStructuralSource(root))

	cases := []map[string]any{
		{},
		{"package": "a", "path": "a"},
		{"package": ""},
		{"package": "a", "relation": "calls"},
		{"package": "a", "relation": ""},
		{"package": "a", "depth": 0.0},
		{"package": "a", "depth": 5.0},
		{"package": "a", "limit": 0.0},
		{"package": "a", "limit": 201.0},
		{"package": "a", "edges": 0.0},
		{"package": "a", "edges": 401.0},
		{"package": 1.0},
		{"package": "a", "surprise": true},
	}
	for _, args := range cases {
		result := tool.Execute(testContext(), args)
		if !result.IsError {
			t.Errorf("args %v were accepted: %s", args, result.Text)
		}
	}
}

func TestGraphToolSchemaIsClosedAndBounded(t *testing.T) {
	tool := NewGraphTool(NewStructuralSource(t.TempDir()))
	if err := agent.ValidateArgs(tool, map[string]any{"package": "a", "depth": 2.0, "limit": 10.0, "edges": 20.0}); err != nil {
		t.Errorf("valid arguments rejected: %v", err)
	}
	for _, args := range []map[string]any{
		{"package": "a", "depth": 0.0},
		{"package": "a", "depth": 5.0},
		{"package": "a", "limit": 0.0},
		{"package": "a", "limit": 201.0},
		{"package": "a", "edges": 0.0},
		{"package": "a", "edges": 401.0},
		{"package": "a", "relation": "calls"},
		{"package": "a", "surprise": true},
	} {
		if err := agent.ValidateArgs(tool, args); err == nil {
			t.Errorf("invalid arguments %v were accepted", args)
		}
	}
}

func TestGraphToolReportsNotFoundWithoutInventingEdges(t *testing.T) {
	root := graphFixtureRepo(t)
	tool := NewGraphTool(NewStructuralSource(root))

	result := tool.Execute(testContext(), map[string]any{"package": "does-not-exist"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if status := graphStatus(t, result); status != "not_found" {
		t.Errorf("status = %q, want not_found", status)
	}
	if !result.Useless {
		t.Error("a complete negative relationship should be marked useless so compaction can elide it")
	}
	if strings.Contains(result.Text, "from:") {
		t.Errorf("a missing package produced edges:\n%s", result.Text)
	}
}

func TestGraphToolReportsAmbiguity(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"go.mod":  "module example.com/app\n\ngo 1.21\n",
		"x1/x.go": "package same\n",
		"x2/x.go": "package same\n",
	})
	tool := NewGraphTool(NewStructuralSource(root))

	result := tool.Execute(testContext(), map[string]any{"package": "same"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Text)
	}
	if status := graphStatus(t, result); status != "ambiguous" {
		t.Errorf("status = %q, want ambiguous:\n%s", status, result.Text)
	}
	if !strings.Contains(result.Text, "x1") || !strings.Contains(result.Text, "x2") {
		t.Errorf("ambiguous result did not list both candidates:\n%s", result.Text)
	}
}

func TestGraphToolIsReadOnly(t *testing.T) {
	root := graphFixtureRepo(t)
	tool := NewGraphTool(NewStructuralSource(root))
	if tool.Approval().Tier != agent.TierRead {
		t.Errorf("tier = %q, want read", tool.Approval().Tier)
	}

	snapshot := func() map[string]string {
		t.Helper()
		got := map[string]string{}
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			got[path] = string(data)
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return got
	}

	before := snapshot()
	for _, args := range []map[string]any{
		{"package": "a"},
		{"path": "a/a.go"},
		{"symbol": "A"},
	} {
		result := tool.Execute(testContext(), args)
		if result.IsError {
			t.Fatalf("args %v: %s", args, result.Text)
		}
		if result.Mutates || result.Executes || result.Verifies {
			t.Errorf("graph result claimed execution evidence: %+v", result)
		}
	}
	after := snapshot()
	if len(before) != len(after) {
		t.Fatal("executing the graph tool changed the repository tree")
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("executing the graph tool modified %s", path)
		}
	}
}

func TestGraphToolRejectsPathsOutsideRoot(t *testing.T) {
	root := graphFixtureRepo(t)
	tool := NewGraphTool(NewStructuralSource(root))

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, path := range []string{"../outside", link} {
		result := tool.Execute(testContext(), map[string]any{"path": path})
		if !result.IsError {
			t.Errorf("path %q was accepted: %s", path, result.Text)
		}
	}
}

func TestGraphToolHonoursCancellation(t *testing.T) {
	root := graphFixtureRepo(t)
	tool := NewGraphTool(NewStructuralSource(root))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := tool.Execute(agent.Context{Ctx: ctx}, map[string]any{"package": "a"})
	if !result.IsError {
		t.Fatal("expected a cancelled context to fail")
	}
	if !strings.Contains(strings.ToLower(result.Text), "cancel") {
		t.Errorf("cancellation was not reported as cancellation: %s", result.Text)
	}
}

func TestStructuralToolsShareOneFreshSnapshot(t *testing.T) {
	// The coordinate and relationship surfaces must read the same index. A
	// change seen by one must be seen by the other without a second parse or
	// a second refresh policy.
	root := symbolsFixture(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() {}\n",
		"b/b.go": "package b\n",
		"c/c.go": "package c\n",
	})
	source := NewStructuralSource(root)
	symbols := NewSymbolsToolWithSource(source)
	graph := NewGraphTool(source)
	if symbols.Source() != graph.Source() {
		t.Fatal("the tools do not share one source")
	}

	if result := graph.Execute(testContext(), map[string]any{"package": "a"}); !strings.Contains(result.Text, "to: b") {
		t.Fatalf("precondition: a->b missing:\n%s", result.Text)
	}
	if result := symbols.Execute(testContext(), map[string]any{"symbol": "A"}); symbolStatus(t, result) != "ok" {
		t.Fatalf("precondition: A missing:\n%s", result.Text)
	}

	if err := os.WriteFile(filepath.Join(root, "a", "a.go"),
		[]byte("package a\n\nimport \"example.com/app/c\"\n\nfunc Renamed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result := graph.Execute(testContext(), map[string]any{"package": "a"}); !strings.Contains(result.Text, "to: c") || strings.Contains(result.Text, "to: b") {
		t.Fatalf("graph served a stale edge:\n%s", result.Text)
	}
	if result := symbols.Execute(testContext(), map[string]any{"symbol": "Renamed"}); symbolStatus(t, result) != "ok" {
		t.Fatalf("symbols did not see the shared rebuild:\n%s", result.Text)
	}
	if result := symbols.Execute(testContext(), map[string]any{"symbol": "A"}); symbolStatus(t, result) != "not_found" {
		t.Fatalf("symbols served a stale coordinate:\n%s", result.Text)
	}
}
