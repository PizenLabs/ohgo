// Command intelligence benchmarks the unified intelligence runtime.
//
// It measures retrieval cold/warm/query/incremental cost at increasing
// repository sizes and compares planner-selected work with a full run over the
// same synthetic repository. Synthetic source keeps the shape exact and
// repeatable; relevance quality is measured by the labeled corpus test.
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
	keep := flag.Bool("keep", false, "keep generated repositories")
	flag.Parse()
	fmt.Println("| files | cold | warm load | refresh | query-only p50 | docs |")
	fmt.Println("|---:|---:|---:|---:|---:|---:|")
	for _, size := range parseSizes(*sizes) {
		dir, err := syntheticRepo(size)
		if err != nil {
			fail(err)
		}
		cacheDir, err := os.MkdirTemp("", "ohgo-intel-cache-*")
		if err != nil {
			fail(err)
		}
		index, err := ast.NewIndex(dir)
		if err != nil {
			fail(err)
		}
		workspace := retrieval.NewWorkspace(dir, cacheDir, retrieval.DefaultOptions())
		start := time.Now()
		_, coldStats, err := workspace.Search(context.Background(), index, retrieval.Query{Text: "handler payment", Limit: 5})
		if err != nil {
			fail(err)
		}
		cold := time.Since(start)
		start = time.Now()
		_, _, err = retrieval.NewWorkspace(dir, cacheDir, retrieval.DefaultOptions()).
			Search(context.Background(), index, retrieval.Query{Text: "handler payment", Limit: 5})
		if err != nil {
			fail(err)
		}
		warm := time.Since(start)
		start = time.Now()
		if _, err := workspace.Refresh(context.Background(), index); err != nil {
			fail(err)
		}
		refresh := time.Since(start)
		durations := make([]time.Duration, 0, 11)
		for i := 0; i < 11; i++ {
			start = time.Now()
			if _, err := workspace.Query(retrieval.Query{Text: "handler payment", Limit: 5}); err != nil {
				fail(err)
			}
			durations = append(durations, time.Since(start))
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		runtime.GC()
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		fmt.Printf("| %d | %s | %s | %s | %s | %d |\n", size, round(cold), round(warm), round(refresh),
			round(durations[len(durations)/2]), coldStats.Documents)
		fmt.Printf("\nheap after warm index (%d docs): %.1f MiB allocated, %.1f MiB system\n\n",
			coldStats.Documents, float64(memory.HeapAlloc)/(1<<20), float64(memory.Sys)/(1<<20))
		if size <= 5000 {
			adaptive(dir, index, size)
		}
		if !*keep {
			_ = os.RemoveAll(dir)
			_ = os.RemoveAll(cacheDir)
		}
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
		providers := make([]string, 0, len(result.Steps))
		for _, step := range result.Steps {
			providers = append(providers, string(step.Provider))
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
		fmt.Printf("| %d | %s | %s | %s | %s | %.2f | %s |\n", size, intent, round(adaptiveElapsed),
			round(fullElapsed), strings.Join(providers, "+"), result.Confidence, result.StopReason)
	}
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
