package investigate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/semantic"
)

// TestSemanticProviderResolvesWithGopls is the real-server check for the live
// provider adapter, not just the client. It skips when gopls is absent.
func TestSemanticProviderResolvesWithGopls(t *testing.T) {
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
	provider := &SemanticProvider{Root: root, Start: func(ctx context.Context) (*semantic.Manager, error) {
		return semantic.NewManager(root, []semantic.Spec{{
			Name: "gopls", Languages: []string{"go"}, Command: []string{gopls, "serve"},
			StartTimeout: 30 * time.Second,
		}}, 1), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	observation, err := provider.Observe(ctx, Request{Question: "Serve"}, plannerStep(), newMeter(Budget{}.normalized()))
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(observation.Records) == 0 {
		t.Fatalf("no semantic records: issues=%v", observation.Issues)
	}
	for _, record := range observation.Records {
		if record.Provider != evidence.ProviderSemantic || record.Source != evidence.SourceLSP {
			t.Fatalf("record provenance = %s/%s", record.Provider, record.Source)
		}
	}
	if len(observation.Facts) == 0 {
		t.Fatal("no model-facing facts")
	}
}
