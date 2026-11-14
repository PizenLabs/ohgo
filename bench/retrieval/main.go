// Command retrieval measures the Okapi BM25 retrieval capability against a
// real repository.
//
// It reports cold index build, warm reconciliation, query latency, index size,
// and the top-ranked files for a few representative queries. It does not
// assert relevance: ground-truth relevance is measured by the labeled corpus
// test in internal/intelligence/retrieval, because a real repository has no
// labels here.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/retrieval"
)

func main() {
	repo := flag.String("repo", ".", "repository root to index")
	cache := flag.String("cache", "", "cache directory; empty uses a temporary directory")
	flag.Parse()

	index, err := ast.NewIndex(*repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "index %s: %v\n", *repo, err)
		os.Exit(1)
	}
	cacheDir := *cache
	if cacheDir == "" {
		dir, err := os.MkdirTemp("", "ohgo-retrieval-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
			os.Exit(1)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		cacheDir = dir
	}
	indexer := retrieval.NewIndexer(*repo, cacheDir, retrieval.DefaultOptions())

	coldStart := time.Now()
	_, coldStats, err := indexer.Sync(context.Background(), index)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cold sync: %v\n", err)
		os.Exit(1)
	}
	coldElapsed := time.Since(coldStart)
	fmt.Printf("### %s\n\n", *repo)
	fmt.Printf("cold: %s, %d documents, %d bytes read, cache %s\n",
		round(coldElapsed), coldStats.Documents, coldStats.BytesRead, coldStats.Cache)
	warmStart := time.Now()
	warm, warmStats, err := indexer.Sync(context.Background(), index)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warm sync: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("warm: %s, %d reused, %d bytes read, cache %s\n",
		round(time.Since(warmStart)), warmStats.Reused, warmStats.BytesRead, warmStats.Cache)
	refreshStart := time.Now()
	_, refreshStats, err := indexer.Refresh(context.Background(), index, warm)
	if err != nil {
		fmt.Fprintf(os.Stderr, "refresh: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("refresh: %s, %d reused, %d bytes read, cache %s\n",
		round(time.Since(refreshStart)), refreshStats.Reused, refreshStats.BytesRead, refreshStats.Cache)
	stats := warm.Stats()
	fmt.Printf("index: %d terms, %d postings, %d truncated documents\n",
		stats.Terms, stats.Postings, stats.TruncatedDocs)

	queries := []string{
		"adaptive structural analysis planner",
		"okapi bm25 ranking",
		"tool approval tier",
		"bm25 relevance retrieval",
	}
	fmt.Printf("\n| query | p50 | top files |\n|---|---:|---|\n")
	for _, query := range queries {
		durations := make([]time.Duration, 0, 15)
		var result retrieval.Result
		for i := 0; i < 15; i++ {
			start := time.Now()
			computed, err := warm.Search(retrieval.Query{Text: query, Limit: 3})
			if err != nil {
				fmt.Fprintf(os.Stderr, "search %q: %v\n", query, err)
				os.Exit(1)
			}
			durations = append(durations, time.Since(start))
			result = computed
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		var paths []string
		for _, hit := range result.Hits {
			paths = append(paths, hit.Path)
		}
		fmt.Printf("| %s | %s | %v |\n", query, round(durations[len(durations)/2]), paths)
	}
}

func round(duration time.Duration) time.Duration {
	if duration < time.Millisecond {
		return duration.Round(time.Microsecond)
	}
	return duration.Round(time.Millisecond)
}
