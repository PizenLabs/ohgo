package ast

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectFrameworkFromManifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "package.json")
	if err := os.WriteFile(path, []byte(`{"dependencies":{"next":"15.0.0","react":"19.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectFramework(path); got != "Next.js" {
		t.Fatalf("detectFramework = %q, want Next.js", got)
	}
}
