package structural

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

const (
	cacheChildRootEnv  = "OHGO_STRUCTURAL_CACHE_CHILD_ROOT"
	cacheChildCacheEnv = "OHGO_STRUCTURAL_CACHE_CHILD_CACHE"
	cacheChildMarker   = "ohgo-structural-cache-child "
)

type cacheChildResult struct {
	cache    CacheState
	snapshot string
}

// TestStructuralCacheSurvivesProcessRestart proves persistence across real
// process boundaries, not merely across two cache objects in one process: the
// first child writes the entry, the second child reads it, and a changed tree
// in a third child misses. A cache that only appeared to persist in-process
// would make the "warm restart" claim false.
func TestStructuralCacheSurvivesProcessRestart(t *testing.T) {
	if root, cacheDir := os.Getenv(cacheChildRootEnv), os.Getenv(cacheChildCacheEnv); root != "" && cacheDir != "" {
		runCacheChild(t, root, cacheDir)
		return
	}

	root := chainFixture(t)
	cacheDir := t.TempDir()

	cold := runCacheChildProcess(t, root, cacheDir)
	if cold.cache != CacheMiss {
		t.Fatalf("cold child cache state = %s, want MISS", cold.cache)
	}
	warm := runCacheChildProcess(t, root, cacheDir)
	if warm.cache != CacheHit {
		t.Fatalf("warm child cache state = %s, want HIT", warm.cache)
	}
	if warm.snapshot != cold.snapshot {
		t.Fatalf("snapshot changed between children: %s then %s", cold.snapshot, warm.snapshot)
	}

	if err := os.WriteFile(filepath.Join(root, "api", "extra.go"),
		[]byte("package api\n\nimport \"example.com/app/store\"\n\nfunc Extra() { store.Get() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := runCacheChildProcess(t, root, cacheDir)
	if changed.cache != CacheMiss {
		t.Fatalf("changed tree cache state = %s, want MISS", changed.cache)
	}
	if changed.snapshot == cold.snapshot {
		t.Fatal("changed tree reused the old snapshot identity")
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("cache directory is empty after three analyses")
	}
}

// runCacheChild is the child-process body: it analyses architecture over the
// shared cache and prints one machine-readable result line.
func runCacheChild(t *testing.T, root, cacheDir string) {
	t.Helper()
	index, err := ast.NewIndex(root)
	if err != nil {
		fmt.Printf("%sERROR %v\n", cacheChildMarker, err)
		os.Exit(1)
	}
	analysis, err := NewAnalyzer(index, NewFileCache(cacheDir)).Analyze(context.Background(), Request{Intent: IntentArchitecture})
	if err != nil {
		fmt.Printf("%sERROR %v\n", cacheChildMarker, err)
		os.Exit(1)
	}
	fmt.Printf("%s%s %s\n", cacheChildMarker, analysis.Cache, analysis.Snapshot.Source.SnapshotID)
}

func runCacheChildProcess(t *testing.T, root, cacheDir string) cacheChildResult {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	command := exec.Command(executable, "-test.run=^TestStructuralCacheSurvivesProcessRestart$", "-test.timeout=120s")
	command.Env = append(os.Environ(),
		cacheChildRootEnv+"="+root,
		cacheChildCacheEnv+"="+cacheDir,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("cache child failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, cacheChildMarker) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, cacheChildMarker))
		if len(fields) != 2 {
			t.Fatalf("malformed child result %q", line)
		}
		if fields[0] == "ERROR" {
			t.Fatalf("cache child reported %q", line)
		}
		return cacheChildResult{cache: CacheState(fields[0]), snapshot: fields[1]}
	}
	t.Fatalf("cache child produced no result line:\n%s", output)
	return cacheChildResult{}
}
