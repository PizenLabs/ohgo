package ast

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestIndexStress measures index cost on a generated repository. It is gated
// by OHGO_INDEX_STRESS=<files> because it writes thousands of files; the
// normal suite never runs it. It logs measurements rather than asserting
// thresholds, since latency depends on the host.
func TestIndexStress(t *testing.T) {
	raw := os.Getenv("OHGO_INDEX_STRESS")
	if raw == "" {
		t.Skip("set OHGO_INDEX_STRESS=<files> to run the index stress probe")
	}
	files, err := strconv.Atoi(raw)
	if err != nil || files < 1 {
		t.Fatalf("OHGO_INDEX_STRESS must be a positive file count, got %q", raw)
	}

	root := t.TempDir()
	const funcsPerFile = 30
	const filesPerPkg = 50
	for i := 0; i < files; i++ {
		pkg := fmt.Sprintf("pkg%03d", i/filesPerPkg)
		dir := filepath.Join(root, fmt.Sprintf("internal/p%03d", i/filesPerPkg))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "package %s\n\n", pkg)
		for j := 0; j < funcsPerFile; j++ {
			fmt.Fprintf(&sb, "// F%d_%d is generated.\nfunc F%d_%d(x int) int {\n\treturn x + %d\n}\n\n", i, j, i, j, j)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d.go", i)), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	idx, err := NewIndex(root)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	cold := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)

	symbols, err := idx.Symbols()
	if err != nil {
		t.Fatalf("Symbols: %v", err)
	}
	t.Logf("files=%d symbols=%d cold_build=%s heap_retained=%.1fMiB",
		files, len(symbols), cold, float64(after.HeapAlloc-before.HeapAlloc)/(1<<20))

	const samples = 5
	var lookup, stale time.Duration
	for i := 0; i < samples; i++ {
		s := time.Now()
		if _, err := idx.Lookup(fmt.Sprintf("F%d_%d", files/2, 7)); err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		lookup += time.Since(s)
		s = time.Now()
		idx.Stale()
		stale += time.Since(s)
	}
	t.Logf("warm_lookup_avg=%s freshness_walk_avg=%s", lookup/samples, stale/samples)

	target := filepath.Join(root, fmt.Sprintf("internal/p%03d/f%05d.go", (files/2)/filesPerPkg, files/2))
	if err := os.WriteFile(target, []byte("package changed\n\nfunc Replaced() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := time.Now()
	gone, err := idx.Lookup(fmt.Sprintf("F%d_%d", files/2, 7))
	if err != nil {
		t.Fatalf("Lookup after edit: %v", err)
	}
	t.Logf("rebuild_after_one_edit=%s", time.Since(s))
	if len(gone) != 0 {
		t.Errorf("symbol from an edited-away declaration is still queryable: %v", gone)
	}

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	s = time.Now()
	after2, err := idx.Symbols()
	if err != nil {
		t.Fatalf("Symbols after delete: %v", err)
	}
	t.Logf("rebuild_after_delete=%s symbols_now=%d", time.Since(s), len(after2))
	// Each file contributes its package clause plus its functions.
	if want := len(symbols) - (funcsPerFile + 1); len(after2) != want {
		t.Errorf("deleted file's declarations still indexed: have %d, want %d", len(after2), want)
	}
}
