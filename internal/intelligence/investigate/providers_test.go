package investigate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// A target-shaped investigation must run the symbol-neighborhood strategy.
// The provider used to leave Request.Level at the package default, which made
// the mandatory strategy ineligible and produced no focus report at all.
func TestStructuralProviderRunsFocusStrategyForImpact(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":      "module example.com/app\n\ngo 1.21\n",
		"a/a.go":      "package a\n\nimport \"example.com/app/b\"\n\nfunc A() { b.B() }\n",
		"b/b.go":      "package b\n\nfunc B() {}\n",
		"b/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) { B() }\n",
	}
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
	provider := &StructuralProvider{Source: idx, Root: root}
	obs, err := provider.Observe(context.Background(),
		Request{Intent: planner.IntentImpact, Focus: "B"},
		planner.Step{Strategy: "reverse-dependencies"}, NewMeter(Budget{}))
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Sufficient {
		t.Fatalf("native impact answer not sufficient: %+v", obs)
	}
	joined := ""
	for _, fact := range obs.Facts {
		joined += fact.Subject + " " + fact.Detail + "\n"
	}
	if !strings.Contains(joined, "references") || !strings.Contains(joined, "callers") {
		t.Fatalf("focus facts missing from observation:\n%s", joined)
	}
}
