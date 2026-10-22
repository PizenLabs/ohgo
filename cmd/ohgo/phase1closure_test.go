package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/ai"
)

// TestBuildToolsExposesSingleStructuralTool pins the Phase 1 closure
// decision: exactly one structural-navigation surface ("symbols"). A second
// wire name for the same capability would double the model's choice without
// adding any coordinate the first one cannot return.
func TestBuildToolsExposesSingleStructuralTool(t *testing.T) {
	toolSet := buildTools(testOptions(t, t.TempDir(), 1), ai.NewRegistry())

	if !hasTool(toolSet, "symbols") {
		t.Fatal("symbols is not in the default tool set")
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
