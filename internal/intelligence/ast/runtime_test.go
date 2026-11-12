package ast

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// testScope is a minimal injected repository scope for membership tests. It has
// no knowledge of .gitignore; it proves the index consumes the seam rather than
// owning a second matcher.
type testScope struct {
	mu      sync.RWMutex
	ignored map[string]bool
	fp      string
}

func (s *testScope) Ignored(rel string, isDir bool) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for prefix := range s.ignored {
		if rel == prefix || len(rel) > len(prefix) && rel[:len(prefix)+1] == prefix+"/" {
			return true
		}
	}
	return false
}

func (s *testScope) Fingerprint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fp
}

func (s *testScope) set(ignored map[string]bool, fp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ignored = ignored
	s.fp = fp
}

func mustContext(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

func TestSnapshotIdentityFreshnessAndIncrementalMetadata(t *testing.T) {
	// The snapshot header must distinguish a clean tree, a Go edit, and a
	// non-Go edit. A one-file Go edit parses exactly one file; a README edit
	// parses none but still advances the structural generation.
	root := graphFixture(t, map[string]string{
		"cmd/app/main.go": "package main\n\nfunc main() {}\n",
		"pkg/a.go":        "package pkg\n\nfunc A() {}\n",
		"pkg/b.go":        "package pkg\n\nfunc B() {}\n",
		"README.md":       "# app\n",
		"Makefile":        "build:\n\tgo build ./...\n",
		"pkg/a_test.go":   "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { A() }\n",
	})
	idx := mustIndex(t, root)
	first, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if first.State != FreshnessCurrent || first.Generation == 0 || first.SnapshotID == "" {
		t.Fatalf("snapshot = %+v, want CURRENT with identity", first)
	}
	if first.GoFiles != 4 || first.Entrypoints != 1 || first.Tests != 1 {
		t.Errorf("counts = go:%d entry:%d tests:%d, want 4/1/1", first.GoFiles, first.Entrypoints, first.Tests)
	}
	if first.Configs < 2 {
		t.Errorf("Configs = %d, want go.mod and Makefile at minimum", first.Configs)
	}

	// A same-tree query does not publish a new generation.
	again, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot again: %v", err)
	}
	if again.Generation != first.Generation || again.SnapshotID != first.SnapshotID {
		t.Errorf("clean requery changed generation/id: %+v vs %+v", again, first)
	}

	if err := os.WriteFile(filepath.Join(root, "pkg", "b.go"),
		[]byte("package pkg\n\nfunc B() {}\nfunc B2() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := idx.Freshness(mustContext(t)); got != FreshnessStale {
		t.Errorf("Freshness after edit = %s, want STALE", got)
	}
	second, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot after edit: %v", err)
	}
	if second.Generation <= first.Generation {
		t.Errorf("generation did not advance: %d -> %d", first.Generation, second.Generation)
	}
	if second.LastParsedFiles != 1 {
		t.Errorf("LastParsedFiles = %d after one Go edit, want 1", second.LastParsedFiles)
	}
	if second.LastReusedFiles != 3 {
		t.Errorf("LastReusedFiles = %d, want the other three Go files", second.LastReusedFiles)
	}
	if second.LastResolution != "global_reresolve" {
		t.Errorf("LastResolution = %q after a Go edit, want global_reresolve", second.LastResolution)
	}

	// A non-Go edit updates inventory metadata without parsing any Go file.
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# app\n\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot after README edit: %v", err)
	}
	if third.Generation <= second.Generation {
		t.Errorf("non-Go edit did not advance generation: %d -> %d", second.Generation, third.Generation)
	}
	if third.LastParsedFiles != 0 {
		t.Errorf("LastParsedFiles = %d after a non-Go edit, want 0", third.LastParsedFiles)
	}
	if third.LastResolution != "none" {
		t.Errorf("LastResolution = %q after a non-Go edit, want none", third.LastResolution)
	}
}

func TestIncrementalDeleteAndRenameWithoutReparsingSiblings(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"pkg/a.go": "package pkg\n\nfunc A() {}\n",
		"pkg/b.go": "package pkg\n\nfunc B() {}\n",
		"pkg/c.go": "package pkg\n\nfunc C() {}\n",
	})
	idx := mustIndex(t, root)

	if err := os.Remove(filepath.Join(root, "pkg", "b.go")); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot after delete: %v", err)
	}
	if afterDelete.LastParsedFiles != 0 {
		t.Errorf("LastParsedFiles = %d after delete, want 0", afterDelete.LastParsedFiles)
	}
	if got := lookupCount(t, idx, "B"); got != 0 {
		t.Errorf("deleted declaration B survived in %d results", got)
	}
	if lookupCount(t, idx, "A") != 1 || lookupCount(t, idx, "C") != 1 {
		t.Error("deleting a sibling changed an unaffected declaration")
	}

	if err := os.Rename(filepath.Join(root, "pkg", "c.go"), filepath.Join(root, "pkg", "renamed.go")); err != nil {
		t.Fatal(err)
	}
	afterRename, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot after rename: %v", err)
	}
	if afterRename.LastParsedFiles != 1 {
		t.Errorf("LastParsedFiles = %d after rename, want 1 for the new path", afterRename.LastParsedFiles)
	}
	got, err := idx.Lookup("C")
	if err != nil {
		t.Fatalf("Lookup(C): %v", err)
	}
	if len(got) != 1 || got[0].Path != "pkg/renamed.go" {
		t.Fatalf("renamed declaration = %+v, want pkg/renamed.go", got)
	}
}

func TestIncrementalBatchPublishesOneGeneration(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"pkg/a.go": "package pkg\n\nfunc A() {}\n",
		"pkg/b.go": "package pkg\n\nfunc B() {}\n",
		"pkg/c.go": "package pkg\n\nfunc C() {}\n",
	})
	idx := mustIndex(t, root)
	first, _ := idx.Snapshot(mustContext(t))

	for _, file := range []string{"a.go", "b.go"} {
		path := filepath.Join(root, "pkg", file)
		content := "package pkg\n\nfunc " + strings.ToUpper(strings.TrimSuffix(file, ".go")) + "2() {}\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	second, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot after batch: %v", err)
	}
	if second.Generation != first.Generation+1 {
		t.Errorf("generation = %d, want exactly one advance from %d", second.Generation, first.Generation)
	}
	if second.LastParsedFiles != 2 {
		t.Errorf("LastParsedFiles = %d for a two-file batch, want 2", second.LastParsedFiles)
	}
}

func TestForceRefreshReparsesEveryGoFile(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"pkg/a.go": "package pkg\n\nfunc A() {}\n",
		"pkg/b.go": "package pkg\n\nfunc B() {}\n",
	})
	idx := mustIndex(t, root)
	first, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := idx.ForceRefresh(mustContext(t)); err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	second, err := idx.Snapshot(mustContext(t))
	if err != nil {
		t.Fatalf("Snapshot after force refresh: %v", err)
	}
	if second.Generation <= first.Generation {
		t.Errorf("force refresh did not advance generation: %d -> %d", first.Generation, second.Generation)
	}
	if second.LastParsedFiles != 2 {
		t.Errorf("LastParsedFiles = %d after force refresh, want 2", second.LastParsedFiles)
	}
	if second.LastResolution != "full_snapshot" {
		t.Errorf("LastResolution = %q after force refresh, want full_snapshot", second.LastResolution)
	}
}

func TestModuleChangeReResolvesWithoutReparsing(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"a/a.go": "package a\n\nimport \"example.com/app/b\"\n\nfunc A() {}\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
	})
	idx := mustIndex(t, root)
	if edge := graphEdgeSet(mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1}))["a->b"]; edge.To == "" {
		t.Fatal("precondition: a->b resolves inside the module")
	}

	// A module rename invalidates every module-local edge. It must not
	// re-parse the Go files: the raw import declarations are retained.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/other\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1})
	if len(result.Edges) != 0 {
		t.Fatalf("module-local edges survived a module rename: %+v", result.Edges)
	}
	info := idx.Info()
	if info.Module != "example.com/other" {
		t.Errorf("Module = %q, want example.com/other", info.Module)
	}
	if info.LastParsedFiles != 0 {
		t.Errorf("LastParsedFiles = %d after module edit, want 0", info.LastParsedFiles)
	}
	if info.LastResolution != "global_reresolve" {
		t.Errorf("LastResolution = %q after module edit, want global_reresolve", info.LastResolution)
	}

	// Restoring the module path restores the edge from the same retained
	// syntax, again with no Go parse.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if edge := graphEdgeSet(mustGraph(t, idx, GraphQuery{Package: "a", Depth: 1}))["a->b"]; edge.To == "" {
		t.Fatal("restored module path did not re-resolve a->b")
	}
	if got := idx.Info().LastParsedFiles; got != 0 {
		t.Errorf("LastParsedFiles = %d after restoring the module path, want 0", got)
	}
}

func TestScopeFingerprintChangesMembershipWithoutFullReparse(t *testing.T) {
	root := graphFixture(t, map[string]string{
		"main.go":        "package main\n\nfunc main() {}\n",
		"ignored/gen.go": "package ignored\n\nfunc Generated() {}\n",
	})
	scope := &testScope{}
	scope.set(map[string]bool{"ignored": true}, "rules-v1")
	idx, err := NewIndexWithOptions(root, IndexOptions{Scope: scope})
	if err != nil {
		t.Fatalf("NewIndexWithOptions: %v", err)
	}
	if got := lookupCount(t, idx, "Generated"); got != 0 {
		t.Fatalf("Lookup(Generated) = %d with ignored scope, want 0", got)
	}
	first, _ := idx.Snapshot(mustContext(t))
	if first.GoFiles != 1 {
		t.Errorf("GoFiles = %d with ignored scope, want 1", first.GoFiles)
	}

	// Same files, different rule identity: the newly included Go file is
	// parsed, the already indexed file is reused.
	scope.set(nil, "rules-v2")
	got, err := idx.Lookup("Generated")
	if err != nil {
		t.Fatalf("Lookup after scope change: %v", err)
	}
	if len(got) != 1 || got[0].Path != "ignored/gen.go" {
		t.Fatalf("newly included declaration = %+v", got)
	}
	second, _ := idx.Snapshot(mustContext(t))
	if second.LastParsedFiles != 1 || second.LastReusedFiles != 1 {
		t.Errorf("scope update work = parsed:%d reused:%d, want 1/1",
			second.LastParsedFiles, second.LastReusedFiles)
	}
	if second.ScopeFingerprint != "rules-v2" {
		t.Errorf("ScopeFingerprint = %q, want rules-v2", second.ScopeFingerprint)
	}

	// Excluding the subtree removes its contribution rather than leaving a
	// stale coordinate behind.
	scope.set(map[string]bool{"ignored": true}, "rules-v3")
	if got := lookupCount(t, idx, "Generated"); got != 0 {
		t.Errorf("excluded declaration survived with %d results", got)
	}
}

func TestSnapshotInvalidAfterRootDisappears(t *testing.T) {
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	idx := mustIndex(t, root)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Snapshot(mustContext(t)); err == nil {
		t.Fatal("Snapshot succeeded after the root disappeared")
	}
	if got := idx.Freshness(mustContext(t)); got != FreshnessInvalid {
		t.Errorf("Freshness = %s after root removal, want INVALID", got)
	}
}

func TestConcurrentSnapshotQueriesSeeWholeFiles(t *testing.T) {
	// Writers rename a complete file into place, so every scan observes one
	// whole generation. Readers must never receive an error or a mixed parse.
	root := indexFixture(t, map[string]string{
		"a.go": "package main\n\nfunc alpha() {}\n",
	})
	idx := mustIndex(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			names := []string{"alpha", "beta"}
			body := "package main\n\nfunc " + names[i%2] + "() {}\n"
			tmp := filepath.Join(root, "a.go.tmp")
			if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
				errCh <- err
				return
			}
			if err := os.Rename(tmp, filepath.Join(root, "a.go")); err != nil {
				errCh <- err
				return
			}
		}
	}()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := idx.Snapshot(ctx); err != nil && !errors.Is(err, ErrSourceChanged) {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if _, err := idx.Snapshot(context.Background()); err != nil {
		t.Fatalf("snapshot after the writer stopped: %v", err)
	}
}
