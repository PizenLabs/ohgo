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

// TestRealGoplsResolvesDefinition is the real-server integration check. It is
// skipped when gopls is not installed, and in -short mode. It proves the client
// against the server Ohgo actually uses for Go, not only against the fake.
func TestRealGoplsResolvesDefinition(t *testing.T) {
	if testing.Short() {
		t.Skip("real language server test skipped in -short")
	}
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls is not installed")
	}
	root := t.TempDir()
	files := map[string]string{
		"go.mod":             "module example.com/app\n\ngo 1.21\n",
		"service/service.go": "package service\n\nfunc Serve() {}\n",
		"main.go":            "package main\n\nimport \"example.com/app/service\"\n\nfunc main() {\n\tservice.Serve()\n}\n",
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	manager := NewManager(root, []Spec{{
		Name:         "gopls",
		Languages:    []string{"go"},
		Command:      []string{gopls},
		StartTimeout: 30 * time.Second,
	}}, 1)
	t.Cleanup(func() { _ = manager.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := manager.Client(ctx, "gopls")
	if err != nil {
		t.Fatalf("start gopls: %v", err)
	}
	mainURI := FileURI(filepath.Join(root, "main.go"))
	serviceURI := FileURI(filepath.Join(root, "service", "service.go"))
	if err := client.OpenDocument(mainURI, "go", files["main.go"]); err != nil {
		t.Fatal(err)
	}
	if err := client.OpenDocument(serviceURI, "go", files["service/service.go"]); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		locations, err := client.Definition(ctx, mainURI, Position{Line: 5, Character: 9})
		if err == nil && len(locations) > 0 {
			if path, ok := PathFromURI(locations[0].URI); ok && strings.HasSuffix(path, filepath.FromSlash("service/service.go")) {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("gopls did not resolve service.Serve; stderr: %s", client.Stderr())
}
