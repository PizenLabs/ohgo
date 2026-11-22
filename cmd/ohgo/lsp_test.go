package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeLookPath(found map[string]string) lookPathFunc {
	return func(name string) (string, error) {
		if path, ok := found[name]; ok {
			return path, nil
		}
		return "", errors.New("not installed")
	}
}

func writeProjectMarker(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLanguageServerSpecsDiscoverAndRootPerLanguage proves discovery is
// explicit and each server loads the project that owns its language, not an
// unrelated monorepo root.
func TestLanguageServerSpecsDiscoverAndRootPerLanguage(t *testing.T) {
	root := t.TempDir()
	writeProjectMarker(t, root, "backend/go.mod")
	writeProjectMarker(t, root, "frontend/package.json")
	writeProjectMarker(t, root, "python/pyproject.toml")

	specs := languageServerSpecs(root, fakeLookPath(map[string]string{
		"gopls":                      "/bin/gopls",
		"typescript-language-server": "/bin/tsls",
		"pyright-langserver":         "/bin/pyright",
	}))
	byName := map[string]struct {
		languages []string
		root      string
		command   []string
	}{}
	for _, spec := range specs {
		byName[spec.Name] = struct {
			languages []string
			root      string
			command   []string
		}{spec.Languages, spec.Root, spec.Command}
	}
	if len(byName) != 3 {
		t.Fatalf("specs = %+v, want the three installed servers", byName)
	}
	if got := byName["gopls"]; got.root != filepath.Join(root, "backend") ||
		len(got.command) != 2 || got.command[0] != "/bin/gopls" || got.command[1] != "serve" {
		t.Fatalf("gopls spec = %+v", got)
	}
	if got := byName["typescript-language-server"]; got.root != filepath.Join(root, "frontend") ||
		got.command[1] != "--stdio" {
		t.Fatalf("TypeScript spec = %+v", got)
	}
	if got := byName["pyright"]; got.root != filepath.Join(root, "python") ||
		got.languages[0] != "python" || got.command[1] != "--stdio" {
		t.Fatalf("Python spec = %+v", got)
	}
}

func TestLanguageServerSpecsSkipMissingTools(t *testing.T) {
	specs := languageServerSpecs(t.TempDir(), fakeLookPath(nil))
	if len(specs) != 0 {
		t.Fatalf("specs = %+v, want none", specs)
	}
	provider := semanticProvider(t.TempDir(), nil, fakeLookPath(nil))
	if provider.Available(context.Background()) {
		t.Fatal("a provider with no installed server reported available")
	}
}

// TestNearestProjectRootKeepsAmbiguousMonorepoRoot proves the root is not
// guessed when several projects carry the same marker.
func TestNearestProjectRootKeepsAmbiguousMonorepoRoot(t *testing.T) {
	root := t.TempDir()
	writeProjectMarker(t, root, "packages/a/package.json")
	writeProjectMarker(t, root, "packages/b/package.json")
	if got := nearestProjectRoot(root, "package.json"); got != root {
		t.Fatalf("root = %q, want %q", got, root)
	}
}

func TestSemanticProviderComposesDiscoveredServers(t *testing.T) {
	provider := semanticProvider(t.TempDir(), nil, fakeLookPath(map[string]string{
		"pyright-langserver":         "/bin/pyright",
		"typescript-language-server": "/bin/tsls",
	}))
	if !provider.Available(context.Background()) {
		t.Fatal("provider with installed servers reported unavailable")
	}
	manager, err := provider.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = manager.Close() }()
	servers := manager.Servers()
	if len(servers) != 2 || servers[0] != "typescript-language-server" || servers[1] != "pyright" {
		t.Fatalf("servers = %v, want the discovered roster", servers)
	}
}

func TestSemanticReportNamesInstalledServers(t *testing.T) {
	report := semanticReport(t.TempDir(), fakeLookPath(map[string]string{
		"gopls": "/bin/gopls", "pyright-langserver": "/bin/pyright",
	}))
	if report.Name != "semantic" || !report.OK {
		t.Fatalf("report = %+v", report)
	}
	for _, want := range []string{"gopls", "pyright", "lazily"} {
		if !strings.Contains(report.Detail, want) {
			t.Fatalf("detail = %q, want %q", report.Detail, want)
		}
	}
	missing := semanticReport(t.TempDir(), fakeLookPath(nil))
	if !strings.Contains(missing.Detail, "AST evidence remains available") {
		t.Fatalf("missing-server detail = %q, want the AST fallback", missing.Detail)
	}
	runtime := runtimeReport()
	if runtime.Name != "runtime" || !strings.Contains(runtime.Detail, "DAP") {
		t.Fatalf("runtime report = %+v", runtime)
	}
}
