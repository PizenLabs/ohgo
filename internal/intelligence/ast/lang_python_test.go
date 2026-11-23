//go:build cgo

package ast

import (
	"context"
	"testing"
)

// TestPythonAdapterResolvesProjectFacts is the production-path Python
// regression: it goes through DefaultAdapters, the adaptive index, and the
// relationship projection rather than parsing with the adapter directly.
func TestPythonAdapterResolvesProjectFacts(t *testing.T) {
	set, err := NewAdapterSet(DefaultAdapters()...)
	if err != nil {
		t.Fatalf("NewAdapterSet: %v", err)
	}
	adapter, ok := set.AdapterFor("app.py")
	if !ok {
		t.Fatal("the production adapter set has no Python owner")
	}
	caps := adapter.Capabilities()
	if !caps.Symbols || !caps.Imports || !caps.References || !caps.Calls ||
		!caps.Inheritance || !caps.Tests || !caps.Entrypoints {
		t.Fatalf("Python capabilities = %+v, want the declared syntax facts", caps)
	}
	root := graphFixture(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/models.py": `class Post:
    def __init__(self, title):
        self.title = title
`,
		"pkg/views.py": `from .models import Post

class PostView(Post):
    def render(self):
        return helper(self.title)

def helper(value):
    return value

if __name__ == "__main__":
    helper(1)
`,
		"pkg/test_views.py": `from .views import helper

def test_render():
    assert helper(1)
`,
		"pyproject.toml": "[project]\nname = \"demo\"\n",
	})
	idx := openAdaptive(t, root)
	ctx := context.Background()
	if err := idx.EnsureParsed(ctx); err != nil {
		t.Fatalf("EnsureParsed: %v", err)
	}
	info := idx.Info()
	if info.ParsedFiles != 4 || info.FailedFiles != 0 {
		t.Fatalf("Python coverage = %+v, want four parsed files", info)
	}
	var python *LanguageCoverage
	for i := range info.Languages {
		if info.Languages[i].Language == LanguagePython {
			python = &info.Languages[i]
		}
	}
	if python == nil || !python.Supported || python.Coverage != "COMPLETE" || python.Files != 4 {
		t.Fatalf("Python coverage = %+v", python)
	}
	if len(info.Unsupported) != 0 {
		t.Fatalf("Python was reported unsupported: %+v", info.Unsupported)
	}

	found, err := idx.Lookup("PostView")
	if err != nil || len(found) != 1 {
		t.Fatalf("Lookup(PostView) = %+v, %v", found, err)
	}
	if found[0].Canonical != SymbolClass || found[0].Path != "pkg/views.py" {
		t.Fatalf("PostView = %+v", found[0])
	}

	impact, err := idx.Impact(ctx, ImpactQuery{Symbol: "Post", Depth: 1, Limit: 50})
	if err != nil {
		t.Fatalf("Impact(Post): %v", err)
	}
	extends := false
	for _, edge := range impact.Edges {
		if edge.Kind == EdgeExtends && edge.Source.Path == "pkg/views.py" {
			extends = true
		}
	}
	if !extends {
		t.Fatalf("Post impact edges = %+v, want PostView EXTENDS Post", impact.Edges)
	}

	entrypoints, err := idx.Entrypoints(ctx)
	if err != nil {
		t.Fatalf("Entrypoints: %v", err)
	}
	guard := false
	for _, entry := range entrypoints.Entrypoints {
		if entry.Path == "pkg/views.py" && entry.Symbol == "__main__" && entry.Verified {
			guard = true
		}
	}
	if !guard {
		t.Fatalf("entrypoints = %+v, want the verified __main__ guard", entrypoints.Entrypoints)
	}

	tests, err := idx.Tests(ctx, TestQuery{Symbol: "helper"})
	if err != nil {
		t.Fatalf("Tests(helper): %v", err)
	}
	if len(tests.Associations) == 0 {
		t.Fatalf("tests for helper = %+v, want the test_render association", tests)
	}
}

// TestPythonParseFailureIsPartial proves a malformed Python file is disclosed
// instead of being treated as an empty declaration set.
func TestPythonParseFailureIsPartial(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"broken.py": "def broken(:\n",
	})
	idx := openAdaptive(t, root)
	if err := idx.EnsureParsed(context.Background()); err != nil {
		t.Fatalf("EnsureParsed: %v", err)
	}
	info := idx.Info()
	if !info.Partial || info.StructuralComplete {
		t.Fatalf("broken Python snapshot = %+v, want partial", info)
	}
	if info.FailedFiles != 1 {
		t.Fatalf("failed files = %d, want 1", info.FailedFiles)
	}
}
