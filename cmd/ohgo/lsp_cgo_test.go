//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/execution/tools"
)

// TestDominantLanguageUsesStructuralCoverage proves the symbol-only semantic
// fallback follows the workspace's actual source mix, not a hard-coded Go
// default.
func TestDominantLanguageUsesStructuralCoverage(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
		"a.py":    "def a():\n    pass\n",
		"b.py":    "def b():\n    pass\n",
		"app.py":  "def app():\n    pass\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := dominantLanguage(tools.NewStructuralSource(root)); got != "python" {
		t.Fatalf("dominant language = %q, want python", got)
	}
}
