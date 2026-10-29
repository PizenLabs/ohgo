package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/tools"
)

// TestBuildToolsExposesOneStructuralSource pins the Phase 6 extension of the
// Phase 1 boundary: symbol coordinates and relationship queries are two
// surfaces, but they must read one shared structural source. A second index
// would parse the repository twice and allow the two answers to disagree.
func TestBuildToolsExposesOneStructuralSource(t *testing.T) {
	toolSet := buildTools(testOptions(t, t.TempDir(), 1), ai.NewRegistry())

	var symbols *tools.SymbolsTool
	var graph *tools.GraphTool
	var refs *tools.RefsTool
	for _, tool := range toolSet {
		switch typed := tool.(type) {
		case *tools.SymbolsTool:
			symbols = typed
		case *tools.GraphTool:
			graph = typed
		case *tools.RefsTool:
			refs = typed
		}
	}
	if symbols == nil {
		t.Fatal("symbols is not in the default tool set")
	}
	if graph == nil {
		t.Fatal("graph is not in the default tool set")
	}
	if refs == nil {
		t.Fatal("refs is not in the default tool set")
	}
	if symbols.Source() == nil || graph.Source() == nil || refs.Source() == nil {
		t.Fatal("a structural tool has no source")
	}
	if symbols.Source() != graph.Source() || graph.Source() != refs.Source() {
		t.Fatal("symbols, graph, and refs do not share one structural source")
	}
	if hasTool(toolSet, "repo") {
		t.Fatal("repo is still registered: the duplicate structural surface must go")
	}
	names := map[string]int{}
	for _, tool := range toolSet {
		names[tool.Name()]++
	}
	for name, count := range names {
		if count > 1 {
			t.Errorf("tool %q is registered %d times", name, count)
		}
	}
}
