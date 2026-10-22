package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/ast"
)

type phase6PerfRow struct {
	Files          int
	Packages       int
	ColdIndex      time.Duration
	ParseOnly      time.Duration
	RelParseOnly   time.Duration
	QueryAvg       time.Duration
	RefQueryAvg    time.Duration
	FreshnessAvg   time.Duration
	SingleChange   time.Duration
	AddPackage     time.Duration
	ConcurrentAvg  time.Duration
	HeapRetainedMB float64
	References     int
	Calls          int
	UnknownCalls   int
}

func TestPhase6Performance(t *testing.T) {
	if os.Getenv("OHGO_PHASE6_PERF") != "1" {
		t.Skip("set OHGO_PHASE6_PERF=1 to run the Phase 6 performance probe")
	}

	rows := []phase6PerfRow{
		phase6MeasureRepo(t, 100),
		phase6MeasureRepo(t, 1000),
		phase6MeasureRepo(t, 3000),
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Phase 6 graph performance probe\n")
	fmt.Fprintf(&sb, "generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&sb, "host: %s/%s, %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(&sb, "method: synthetic single-module repositories with a chain of package imports;\n")
	fmt.Fprintf(&sb, "        symbols, package graph, references, and direct calls are built from one parse pass.\n")
	fmt.Fprintf(&sb, "note: synthetic measurements, not a production-scale claim.\n\n")
	fmt.Fprintf(&sb, "%-6s %-9s %-8s %-11s %-13s %-16s %-10s %-12s %-12s %-11s %-10s %-10s %-10s %-13s %-10s %-10s\n",
		"files", "packages", "cold_ms", "parse_ms", "rel_parse_ms", "index/rel_parse", "query_us", "ref_q_us", "stale_us", "change_ms", "addpkg_ms", "heap_MiB", "conc_us", "references", "calls", "unknown")
	for _, row := range rows {
		ratio := 0.0
		if row.RelParseOnly > 0 {
			ratio = float64(row.ColdIndex) / float64(row.RelParseOnly)
		}
		fmt.Fprintf(&sb, "%-6d %-9d %-8.2f %-11.2f %-13.2f %-16.2f %-10.1f %-12.1f %-12.1f %-11.2f %-10.2f %-10.1f %-10.1f %-13d %-10d %-10d\n",
			row.Files, row.Packages,
			ms(row.ColdIndex), ms(row.ParseOnly), ms(row.RelParseOnly), ratio,
			us(row.QueryAvg), us(row.RefQueryAvg), us(row.FreshnessAvg),
			ms(row.SingleChange), ms(row.AddPackage),
			row.HeapRetainedMB, us(row.ConcurrentAvg),
			row.References, row.Calls, row.UnknownCalls)
	}
	sb.WriteString("\nInterpretation rules:\n")
	sb.WriteString("- parse_ms is a symbols-only parse walk; rel_parse_ms adds reference and call extraction.\n")
	sb.WriteString("- index/rel_parse is the cold index divided by the relationship parse walk; a value\n")
	sb.WriteString("  near 1 means the graph and relationships did not add a second parse.\n")
	sb.WriteString("- query_us is a warm depth-1 graph query after the index is built.\n")
	sb.WriteString("- ref_q_us is a warm symbol-reference query after the index is built.\n")
	sb.WriteString("- stale_us is the freshness stat walk alone (Index.Stale); a graph or refs query\n")
	sb.WriteString("  adds the go.mod fingerprint and traversal on top of it.\n")
	sb.WriteString("- change_ms is a graph query after one import rewrite (full snapshot rebuild).\n")
	sb.WriteString("- addpkg_ms is a graph query after creating one package (full snapshot rebuild).\n")
	sb.WriteString("- conc_us is the average of 8 goroutines issuing bounded graph queries.\n")

	path := filepath.Join("..", "..", "docs", "benchmarks", "results", "phase6-performance.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write performance artifact: %v", err)
	}
	t.Logf("wrote %s\n%s", path, sb.String())
}

func phase6MeasureRepo(t *testing.T, files int) phase6PerfRow {
	t.Helper()
	root, packages := phase6PerfFixture(t, files)
	row := phase6PerfRow{Files: files, Packages: packages}

	parseStart := time.Now()
	if err := phase6ParseOnly(root, false); err != nil {
		t.Fatalf("symbols-only parse walk: %v", err)
	}
	row.ParseOnly = time.Since(parseStart)

	relParseStart := time.Now()
	if err := phase6ParseOnly(root, true); err != nil {
		t.Fatalf("relationship parse walk: %v", err)
	}
	row.RelParseOnly = time.Since(relParseStart)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	idx, err := ast.NewIndex(root)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	row.ColdIndex = time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	row.HeapRetainedMB = float64(after.HeapAlloc-before.HeapAlloc) / (1 << 20)

	if row.RelParseOnly > 0 && float64(row.ColdIndex)/float64(row.RelParseOnly) > 2.5 {
		t.Errorf("cold index is %.2fx the relationship parse walk; the index doubled the parse",
			float64(row.ColdIndex)/float64(row.RelParseOnly))
	}

	row.QueryAvg = phase6MedianQuery(t, idx, "internal/p000", 20)
	row.RefQueryAvg = phase6MedianRefs(t, idx, "p000.F0_0", 20)
	row.FreshnessAvg = phase6MedianStale(t, idx, 20)
	stats := idx.RelationshipStats()
	row.References = stats.References
	row.Calls = stats.Calls
	row.UnknownCalls = stats.UnknownCalls

	// One import rewrite: same package, different target. The query must see
	// the new edge and must pay a full snapshot rebuild.
	changePath := filepath.Join(root, "internal", "p000", "f0.go")
	if err := os.WriteFile(changePath, []byte(phase6PerfFile(0, 10, 0)), 0o644); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	result, err := idx.Graph(context.Background(), ast.GraphQuery{Path: "internal/p000", Depth: 1, Limit: 10})
	row.SingleChange = time.Since(start)
	if err != nil {
		t.Fatalf("graph after import change: %v", err)
	}
	if !phase6HasEdge(result, "internal/p000", "internal/p010") {
		t.Fatalf("import change was not visible: %+v", result.Edges)
	}

	// One package creation: the graph must discover the new node and edge.
	if err := os.MkdirAll(filepath.Join(root, "internal", "newpkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "newpkg", "n.go"),
		[]byte("package newpkg\n\nfunc N() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changePath, []byte("package p000\n\nimport \"example.com/perf/internal/newpkg\"\n\nfunc F0_0(x int) int { return x }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	result, err = idx.Graph(context.Background(), ast.GraphQuery{Path: "internal/p000", Depth: 1, Limit: 10})
	row.AddPackage = time.Since(start)
	if err != nil {
		t.Fatalf("graph after package creation: %v", err)
	}
	if !phase6HasEdge(result, "internal/p000", "internal/newpkg") {
		t.Fatalf("new package was not visible: %+v", result.Edges)
	}

	row.ConcurrentAvg = phase6ConcurrentQuery(t, idx, "internal/p000")
	return row
}

func phase6PerfFixture(t *testing.T, files int) (string, int) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/perf\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const filesPerPackage = 4
	packages := files / filesPerPackage
	if packages < 1 {
		packages = 1
	}
	for p := 0; p < packages; p++ {
		dir := filepath.Join(root, "internal", fmt.Sprintf("p%03d", p))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		next := p + 1
		if next >= packages {
			next = -1
		}
		for f := 0; f < filesPerPackage; f++ {
			content := phase6PerfFile(p, next, f)
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.go", f)), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root, packages
}

// phase6PerfFile builds one synthetic source file. next=-1 means no import.
func phase6PerfFile(pkg, next, file int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "package p%03d\n\n", pkg)
	if next >= 0 {
		fmt.Fprintf(&sb, "import \"example.com/perf/internal/p%03d\"\n\n", next)
	}
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "func F%d_%d(x int) int { return x + %d }\n", file, i, i)
	}
	// A small amount of real reference/call work so the relationship
	// projection is measured rather than inferred from empty function bodies.
	fmt.Fprintf(&sb, "func Caller%d(x int) int { return F%d_0(x) + F%d_1(x) }\n", file, file, file)
	return sb.String()
}

func phase6ParseOnly(root string, relationships bool) error {
	backend := ast.GoBackend{}
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		_, err = backend.Parse(path, ast.Options{
			Kinds: []ast.Kind{
				ast.KindPackage, ast.KindFunc, ast.KindMethod, ast.KindType, ast.KindVar, ast.KindConst,
			},
			Relationships: relationships,
		})
		return err
	})
}

func phase6MedianQuery(t *testing.T, idx *ast.Index, path string, samples int) time.Duration {
	t.Helper()
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		if _, err := idx.Graph(context.Background(), ast.GraphQuery{Path: path, Depth: 1, Limit: 10}); err != nil {
			t.Fatalf("graph query: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

// phase6MedianStale measures the freshness stat walk alone. Graph queries add
// the go.mod fingerprint check and traversal on top of this.
func phase6MedianRefs(t *testing.T, idx *ast.Index, symbol string, samples int) time.Duration {
	t.Helper()
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		if _, err := idx.References(context.Background(), ast.RefQuery{Symbol: symbol}); err != nil {
			t.Fatalf("symbol-reference query: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

func phase6MedianStale(t *testing.T, idx *ast.Index, samples int) time.Duration {
	t.Helper()
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		idx.Stale()
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

func phase6ConcurrentQuery(t *testing.T, idx *ast.Index, path string) time.Duration {
	t.Helper()
	const workers = 8
	const perWorker = 5
	var wg sync.WaitGroup
	durations := make([]time.Duration, workers*perWorker)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				start := time.Now()
				if _, err := idx.Graph(context.Background(), ast.GraphQuery{Path: path, Depth: 1, Limit: 10}); err != nil {
					t.Errorf("concurrent graph query: %v", err)
					return
				}
				durations[worker*perWorker+i] = time.Since(start)
			}
		}(worker)
	}
	wg.Wait()
	var total time.Duration
	for _, duration := range durations {
		total += duration
	}
	return total / time.Duration(len(durations))
}

func phase6HasEdge(result ast.GraphResult, from, to string) bool {
	for _, edge := range result.Edges {
		if edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func us(d time.Duration) float64 { return float64(d) / float64(time.Microsecond) }
