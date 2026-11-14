// Command intelligence benchmarks the unified intelligence runtime.
//
// Latency is reported as median and p95 over repeated rounds so a single noisy
// run cannot be mistaken for a regression. Memory can be measured per phase
// (ast, retrieval, investigation) in a fresh process, because a single process
// heap conflates the repository snapshot with the retrieval index.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
	"github.com/PizenLabs/ohgo/internal/intelligence/retrieval"
	"github.com/PizenLabs/ohgo/internal/intelligence/structural"
)

func main() {
	sizes := flag.String("sizes", "100,1000,5000", "comma-separated repository sizes")
	rounds := flag.Int("rounds", 5, "measurement rounds per size")
	keep := flag.Bool("keep", false, "keep generated repositories")
	phase := flag.String("phase", "", "memory phase to measure: ast, retrieval, investigation")
	flag.Parse()
	if *phase != "" {
		runPhase(*sizes, *phase)
		return
	}
	fmt.Println("| files | cold med/p95 | warm med/p95 | refresh med/p95 | query med/p95 | docs |")
	fmt.Println("|---:|---:|---:|---:|---:|---:|")
	for _, size := range parseSizes(*sizes) {
		dir, err := syntheticRepo(size)
		if err != nil {
			fail(err)
		}
		index, err := ast.NewIndex(dir)
		if err != nil {
			fail(err)
		}
		measureSize(dir, index, size, *rounds)
		if !*keep {
			_ = os.RemoveAll(dir)
		}
	}
}

func measureSize(dir string, index *ast.Index, size, rounds int) {
	if rounds < 1 {
		rounds = 1
	}
	coldDurations := make([]time.Duration, 0, rounds)
	warmDurations := make([]time.Duration, 0, rounds)
	refreshDurations := make([]time.Duration, 0, rounds)
	queryDurations := make([]time.Duration, 0, rounds*15)
	var documents int
	for round := 0; round < rounds; round++ {
		cacheDir, err := os.MkdirTemp("", "ohgo-intel-cache-*")
		if err != nil {
			fail(err)
		}
		start := time.Now()
		workspace := retrieval.NewWorkspace(dir, cacheDir, retrieval.DefaultOptions())
		_, stats, err := workspace.Search(context.Background(), index, retrieval.Query{Text: "handler payment", Limit: 5})
		if err != nil {
			fail(err)
		}
		coldDurations = append(coldDurations, time.Since(start))
		documents = stats.Documents

		start = time.Now()
		warm := retrieval.NewWorkspace(dir, cacheDir, retrieval.DefaultOptions())
		if _, _, err := warm.Search(context.Background(), index, retrieval.Query{Text: "handler payment", Limit: 5}); err != nil {
			fail(err)
		}
		warmDurations = append(warmDurations, time.Since(start))

		start = time.Now()
		if _, err := workspace.Refresh(context.Background(), index); err != nil {
			fail(err)
		}
		refreshDurations = append(refreshDurations, time.Since(start))

		for i := 0; i < 15; i++ {
			start = time.Now()
			if _, err := workspace.Query(retrieval.Query{Text: "handler payment", Limit: 5}); err != nil {
				fail(err)
			}
			queryDurations = append(queryDurations, time.Since(start))
		}
		_ = os.RemoveAll(cacheDir)
	}
	fmt.Printf("| %d | %s | %s | %s | %s | %d |\n", size,
		medianP95(coldDurations), medianP95(warmDurations), medianP95(refreshDurations),
		medianP95(queryDurations), documents)
	if size <= 5000 {
		adaptive(dir, index, size)
	}
}

func adaptive(dir string, index *ast.Index, size int) {
	source := &inventorySource{index: index}
	cache, err := os.MkdirTemp("", "ohgo-intel-struct-*")
	if err != nil {
		fail(err)
	}
	defer func() { _ = os.RemoveAll(cache) }()
	workspace := retrieval.NewWorkspace(dir, "", retrieval.DefaultOptions())
	engine := investigate.New(
		&investigate.StructuralProvider{Source: source, Cache: structural.NewFileCache(cache), Root: dir},
		&investigate.RetrievalProvider{Workspace: workspace, Source: index, Root: dir},
	)
	fmt.Println("\n| files | intent | adaptive | full (structural+retrieval) | providers | confidence | stop |")
	fmt.Println("|---:|---|---:|---:|---|---:|---|")
	for _, intent := range []planner.Intent{planner.IntentProject, planner.IntentLocate} {
		question := "what is this project?"
		focus := ""
		if intent == planner.IntentLocate {
			question = "where is handler 42 implemented?"
			focus = "pkg42"
		}
		start := time.Now()
		result, err := engine.Run(context.Background(), investigate.Request{
			Question: question, Intent: intent, Focus: focus,
			Budget: investigate.Budget{MaxSteps: 3},
		})
		if err != nil {
			fail(err)
		}
		adaptiveElapsed := time.Since(start)
		fullStart := time.Now()
		structuralIntent, ok := structural.ParseIntent(string(intent))
		if !ok {
			structuralIntent = structural.IntentProject
		}
		fullCache, cacheErr := os.MkdirTemp("", "ohgo-intel-full-*")
		if cacheErr != nil {
			fail(cacheErr)
		}
		if _, analyzeErr := structural.NewAnalyzer(source, structural.NewFileCache(fullCache)).
			Analyze(context.Background(), structural.Request{Intent: structuralIntent, Focus: focus, MaxFiles: 4000}); analyzeErr != nil {
			fail(analyzeErr)
		}
		if _, refreshErr := workspace.Refresh(context.Background(), index); refreshErr != nil {
			fail(refreshErr)
		}
		if _, queryErr := workspace.Query(retrieval.Query{Text: question, Limit: 8}); queryErr != nil {
			fail(queryErr)
		}
		fullElapsed := time.Since(fullStart)
		_ = os.RemoveAll(fullCache)
		providers := make([]string, 0, len(result.Steps))
		for _, step := range result.Steps {
			providers = append(providers, string(step.Provider))
		}
		fmt.Printf("| %d | %s | %s | %s | %s | %.2f | %s |\n", size, intent, round(adaptiveElapsed),
			round(fullElapsed), strings.Join(providers, "+"), result.Confidence, result.StopReason)
	}
}

// runPhase measures one subsystem in a fresh process so heap numbers are not
// conflated. It is invoked with -phase and a single size.
func runPhase(sizes, phase string) {
	parsed := parseSizes(sizes)
	if len(parsed) == 0 {
		fail(fmt.Errorf("no size"))
	}
	size := parsed[0]
	dir, err := syntheticRepo(size)
	if err != nil {
		fail(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	index, err := ast.NewIndex(dir)
	if err != nil {
		fail(err)
	}
	inventory, err := index.Inventory(context.Background(), ast.InventoryQuery{MaxFiles: 200000})
	if err != nil {
		fail(err)
	}
	report := func(label string) {
		runtime.GC()
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		fmt.Printf("| %s | %.1f MiB | %.1f MiB |\n", label,
			float64(memory.HeapAlloc)/(1<<20), float64(memory.HeapInuse)/(1<<20))
	}
	fmt.Println("| phase | heap alloc | heap inuse |")
	fmt.Println("|---|---:|---:|")
	report(fmt.Sprintf("ast inventory (%d files)", len(inventory.Files)))
	if phase == "ast" {
		return
	}
	workspace := retrieval.NewWorkspace(dir, "", retrieval.DefaultOptions())
	start := time.Now()
	if _, _, err := workspace.Search(context.Background(), index, retrieval.Query{Text: "handler payment", Limit: 5}); err != nil {
		fail(err)
	}
	report(fmt.Sprintf("ast + retrieval cold (%.0f ms)", float64(time.Since(start).Milliseconds())))
	if phase == "retrieval" {
		return
	}
	source := &inventorySource{index: index}
	cache, err := os.MkdirTemp("", "ohgo-intel-mem-*")
	if err != nil {
		fail(err)
	}
	defer func() { _ = os.RemoveAll(cache) }()
	engine := investigate.New(
		&investigate.StructuralProvider{Source: source, Cache: structural.NewFileCache(cache), Root: dir},
		&investigate.RetrievalProvider{Workspace: workspace, Source: index, Root: dir},
	)
	if _, err := engine.Run(context.Background(), investigate.Request{
		Question: "where is handler 42 implemented?", Intent: planner.IntentLocate, Focus: "pkg42",
		Budget: investigate.Budget{MaxSteps: 3},
	}); err != nil {
		fail(err)
	}
	report("ast + retrieval + investigation")
	runtime.KeepAlive(engine)
	runtime.KeepAlive(workspace)
	runtime.KeepAlive(index)
	runtime.KeepAlive(inventory)
}

func syntheticRepo(files int) (string, error) {
	dir, err := os.MkdirTemp("", "ohgo-intel-repo-*")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/synthetic\n\ngo 1.21\n"), 0o644); err != nil {
		return "", err
	}
	for i := 0; i < files; i++ {
		pkg := fmt.Sprintf("pkg%d", i%100)
		sub := filepath.Join(dir, pkg)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return "", err
		}
		body := fmt.Sprintf(`package %s

// Handler%d processes a payment authorization and settles the order.
func Handler%d(input string) string {
	result := transform(input)
	return result
}

func transform(value string) string { return value + "!" }
`, pkg, i, i)
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("handler_%d.go", i)), []byte(body), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func parseSizes(value string) []int {
	var out []int
	for _, part := range strings.Split(value, ",") {
		var size int
		if _, err := fmt.Sscanf(strings.TrimSpace(part), "%d", &size); err == nil && size > 0 {
			out = append(out, size)
		}
	}
	return out
}

type inventorySource struct{ index *ast.Index }

func (s *inventorySource) Inventory(ctx context.Context, query ast.InventoryQuery) (ast.Inventory, error) {
	return s.index.Inventory(ctx, query)
}

func (s *inventorySource) Dependencies(ctx context.Context, query ast.DependencyQuery) (ast.DependencyResult, error) {
	return s.index.Dependencies(ctx, query)
}

func medianP95(values []time.Duration) string {
	if len(values) == 0 {
		return "-"
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	median := sorted[len(sorted)/2]
	p95 := sorted[(len(sorted)*95)/100]
	if p95 < median {
		p95 = median
	}
	return round(median).String() + " / " + round(p95).String()
}

func round(duration time.Duration) time.Duration {
	if duration < time.Millisecond {
		return duration.Round(time.Microsecond)
	}
	return duration.Round(time.Millisecond)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "intelligence bench: %v\n", err)
	os.Exit(1)
}
