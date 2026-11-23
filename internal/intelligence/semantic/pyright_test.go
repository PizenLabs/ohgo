package semantic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealPyrightResolvesDocumentSymbols is the optional Python-server
// integration check. It is skipped when pyright-langserver is not installed and
// in -short mode; it proves the generic client and the Python command in the
// discovery table agree with a real server.
func TestRealPyrightResolvesDocumentSymbols(t *testing.T) {
	if testing.Short() {
		t.Skip("real language server test skipped in -short")
	}
	pyright, err := exec.LookPath("pyright-langserver")
	if err != nil {
		t.Skip("pyright-langserver is not installed")
	}
	root := t.TempDir()
	files := map[string]string{
		"pyproject.toml":  "[project]\nname = \"demo\"\n",
		"app/__init__.py": "",
		"app/models.py":   "class Post:\n    def __init__(self, title):\n        self.title = title\n",
		"app/views.py":    "from .models import Post\n\nclass PostView(Post):\n    def render(self):\n        return self.title\n",
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

	manager := NewManager(root, []Spec{{
		Name: "pyright", Languages: []string{"python"},
		Command: []string{pyright, "--stdio"}, StartTimeout: 30 * time.Second,
	}}, 1)
	t.Cleanup(func() { _ = manager.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := manager.ForLanguage(ctx, "python")
	if err != nil {
		t.Fatalf("start pyright: %v", err)
	}
	uri := FileURI(filepath.Join(root, "app", "views.py"))
	if err := client.OpenDocument(uri, "python", files["app/views.py"]); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		symbols, err := client.DocumentSymbols(ctx, uri)
		if err == nil {
			for _, symbol := range symbols {
				if strings.Contains(symbol.Name, "PostView") {
					return
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("pyright did not resolve PostView; stderr: %s", client.Stderr())
}
