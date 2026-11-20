package structural

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

func auditIndex(t *testing.T, files map[string]string) *ast.Index {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := ast.NewIndexWithOptions(root, ast.IndexOptions{Adaptive: true})
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

// FocusReport.References must count references, not the whole neighbourhood.
// A query with two references, one caller, and one importer used to report
// four references.
func TestFocusReferencesDoesNotIncludeCallersOrImporters(t *testing.T) {
	idx := auditIndex(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() { b.B() }\n\nvar X = b.B\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
	})
	analysis, err := NewAnalyzer(idx, nil).Analyze(context.Background(), Request{
		Intent: IntentFlow, Focus: "B", Level: LevelSymbol,
	})
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Focus == nil || !analysis.Focus.Found {
		t.Fatalf("focus = %+v", analysis.Focus)
	}
	if analysis.Focus.References != 2 {
		t.Fatalf("references = %d, want the two reference sites only: %+v",
			analysis.Focus.References, analysis.Focus)
	}
	if analysis.Focus.Callers != 1 {
		t.Fatalf("callers = %d, want 1", analysis.Focus.Callers)
	}
	if analysis.Focus.Importers != 1 {
		t.Fatalf("importers = %d, want 1", analysis.Focus.Importers)
	}
}

// A single Go module under a project directory must still produce an
// architecture shape. Collapsing every package to the one module node erased
// all edges and reported the repository as independent/not-established.
func TestSingleNestedGoModuleProducesArchitectureShape(t *testing.T) {
	idx := auditIndex(t, map[string]string{
		"svc/go.mod":           "module example.com/svc\n\ngo 1.21\n",
		"svc/domain/domain.go": "package domain\n\nfunc New() {}\n",
		"svc/app/app.go": `package app

import "example.com/svc/domain"

func Run() { domain.New() }
`,
		"svc/http/http.go": `package http

import "example.com/svc/app"

func Serve() { app.Run() }
`,
		"docs/README.md": "# docs\n",
	})
	analysis, err := NewAnalyzer(idx, nil).Analyze(context.Background(), Request{
		Intent: IntentArchitecture, Level: LevelPackage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := countModules(analysis.Snapshot); got != 1 {
		t.Fatalf("Go module count = %d, want 1", got)
	}
	if analysis.Architecture.Pattern != "layered" {
		t.Fatalf("architecture = %+v, want layered from module children", analysis.Architecture)
	}
	if analysis.Coverage.GraphEdges == 0 {
		t.Fatalf("dependency graph has no edges: %+v", analysis.Coverage)
	}
}

// A flow question asks what the focus reaches. Reporting only inbound callers
// left the forward direction empty.
func TestFocusReportsForwardCallees(t *testing.T) {
	idx := auditIndex(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() { b.B() }\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
	})
	analysis, err := NewAnalyzer(idx, nil).Analyze(context.Background(), Request{
		Intent: IntentFlow, Focus: "A", Level: LevelSymbol,
	})
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Focus == nil || !analysis.Focus.Found {
		t.Fatalf("focus = %+v", analysis.Focus)
	}
	if analysis.Focus.Callees != 1 {
		t.Fatalf("callees = %d, want the forward A -> B edge: %+v", analysis.Focus.Callees, analysis.Focus)
	}
}
