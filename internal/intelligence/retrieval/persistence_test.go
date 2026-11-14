package retrieval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func testSource(t *testing.T, root string) Source {
	t.Helper()
	index, err := ast.NewIndex(root)
	if err != nil {
		t.Fatalf("ast.NewIndex: %v", err)
	}
	return index
}

// TestIndexPersistenceRoundTrip pins that a saved index restores the same
// corpus and ranking, not merely the same document count.
func TestIndexPersistenceRoundTrip(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"auth/service.go": "package auth\n\nfunc Authenticate() {}\n",
		"billing/pay.go":  "package billing\n\nfunc Payment() {}\n",
	})
	indexer := NewIndexer(root, t.TempDir(), DefaultOptions())
	index, stats, err := indexer.Sync(context.Background(), testSource(t, root))
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stats.Cache != CacheMiss || stats.Added != 2 {
		t.Fatalf("first sync = %+v", stats)
	}
	before := search(t, index, Query{Text: "authentication payment"})

	loaded, err := Load(indexer.CacheDir, mustAbs(t, root), DefaultOptions())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	after := search(t, loaded, Query{Text: "authentication payment"})
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restored ranking differs:\n%+v\n%+v", before, after)
	}
	if loaded.Stats() != index.Stats() {
		t.Fatalf("restored stats = %+v, want %+v", loaded.Stats(), index.Stats())
	}
}

func TestLoadRejectsForeignIdentityAndCorruption(t *testing.T) {
	root := writeFixture(t, map[string]string{"a/x.go": "package a\nfunc X() {}\n"})
	cacheDir := t.TempDir()
	indexer := NewIndexer(root, cacheDir, DefaultOptions())
	if _, _, err := indexer.Sync(context.Background(), testSource(t, root)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cacheDir, "/another/root", DefaultOptions()); !errors.Is(err, ErrCacheIdentity) {
		t.Fatalf("foreign root load error = %v, want ErrCacheIdentity", err)
	}
	options := DefaultOptions()
	options.K1 = 0.9
	if _, err := Load(cacheDir, mustAbs(t, root), options); !errors.Is(err, ErrCacheIdentity) {
		t.Fatalf("changed configuration load error = %v, want ErrCacheIdentity", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, cacheFileName), []byte("not a gob"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cacheDir, mustAbs(t, root), DefaultOptions()); err == nil || errors.Is(err, ErrCacheIdentity) {
		t.Fatalf("corrupt cache error = %v, want a decode error", err)
	}
}

// TestIndexerSyncIsIncremental pins the reconciliation contract: unchanged
// files are never re-read, one changed file is the only read, deletion removes
// the document, and a restart reuses the persisted index.
func TestIndexerSyncIsIncremental(t *testing.T) {
	root := writeFixture(t, map[string]string{
		"auth/service.go":      "package auth\n\nfunc Authenticate() {}\n",
		"auth/service_test.go": "package auth\n\nimport \"testing\"\n\nfunc TestAuthenticate(t *testing.T) { Authenticate() }\n",
		"README.md":            "# Authentication design\n",
		"tools/helper.py":      "def authenticate_helper():\n    pass\n",
		"assets/logo.bin":      "\x00\x01\x02binary",
	})
	reads := 0
	indexer := NewIndexer(root, t.TempDir(), DefaultOptions())
	indexer.ReadFile = func(path string) ([]byte, error) {
		reads++
		return os.ReadFile(path)
	}
	first, stats, err := indexer.Sync(context.Background(), testSource(t, root))
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if stats.Added != 4 || stats.BytesRead == 0 {
		t.Fatalf("first sync = %+v, want four text documents (Go, test, doc, Python)", stats)
	}
	if reads != 4 {
		t.Fatalf("first sync read %d files, want 4", reads)
	}
	if hasPath(search(t, first, Query{Text: "authenticate"}).Hits, "auth/service_test.go") {
		t.Fatal("test file was returned without IncludeTests")
	}
	if !hasPath(search(t, first, Query{Text: "authenticate helper"}).Hits, "tools/helper.py") {
		t.Fatal("unsupported-language Python file was not retrievable")
	}

	reads = 0
	_, second, err := indexer.Sync(context.Background(), testSource(t, root))
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.Added != 0 || second.Updated != 0 || second.Removed != 0 || second.BytesRead != 0 || reads != 0 {
		t.Fatalf("unchanged sync did work: %+v reads=%d", second, reads)
	}
	if second.Reused != second.Documents {
		t.Fatalf("unchanged sync reused %d of %d documents", second.Reused, second.Documents)
	}
	if second.Cache != CacheHit {
		t.Fatalf("second sync cache state = %s, want hit", second.Cache)
	}

	if err := os.WriteFile(filepath.Join(root, "auth/service.go"),
		[]byte("package auth\n\nfunc Authenticate() {}\n\nfunc RotateSessionSecret() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, stats, err := indexer.Sync(context.Background(), testSource(t, root))
	if err != nil {
		t.Fatalf("changed sync: %v", err)
	}
	if stats.Updated != 1 || stats.BytesRead == 0 || reads != 1 {
		t.Fatalf("changed sync = %+v reads=%d, want exactly one re-read", stats, reads)
	}
	if !hasPath(search(t, changed, Query{Text: "rotatesessionsecret"}).Hits, "auth/service.go") {
		t.Fatal("changed file's new term is not retrievable")
	}

	if err := os.Remove(filepath.Join(root, "tools/helper.py")); err != nil {
		t.Fatal(err)
	}
	removed, stats, err := indexer.Sync(context.Background(), testSource(t, root))
	if err != nil {
		t.Fatalf("removed sync: %v", err)
	}
	if stats.Removed != 1 || hasPath(search(t, removed, Query{Text: "helper"}).Hits, "tools/helper.py") {
		t.Fatalf("deletion was not applied: %+v", stats)
	}

	// A new indexer over the same cache directory is the restart path.
	restarted := NewIndexer(root, indexer.CacheDir, DefaultOptions())
	_, restartStats, err := restarted.Sync(context.Background(), testSource(t, root))
	if err != nil {
		t.Fatalf("restart sync: %v", err)
	}
	if restartStats.Cache != CacheHit || restartStats.BytesRead != 0 {
		t.Fatalf("restart sync = %+v, want a cache hit with no reads", restartStats)
	}
}

func TestIndexerRecoversFromCorruptCache(t *testing.T) {
	root := writeFixture(t, map[string]string{"a/x.go": "package a\nfunc X() {}\n"})
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, cacheFileName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(root, cacheDir, DefaultOptions())
	_, stats, err := indexer.Sync(context.Background(), testSource(t, root))
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stats.Cache != CacheInvalid || stats.CacheReason == "" || stats.Documents != 1 {
		t.Fatalf("corrupt cache recovery = %+v", stats)
	}
}

func mustAbs(t *testing.T, root string) string {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}
