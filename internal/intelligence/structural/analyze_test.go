package structural

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

type countingSource struct {
	*ast.Index
	inventories  int
	dependencies int
}

func (c *countingSource) Inventory(ctx context.Context, query ast.InventoryQuery) (ast.Inventory, error) {
	c.inventories++
	return c.Index.Inventory(ctx, query)
}

func (c *countingSource) Dependencies(ctx context.Context, query ast.DependencyQuery) (ast.DependencyResult, error) {
	c.dependencies++
	return c.Index.Dependencies(ctx, query)
}

func chainFixture(t *testing.T) string {
	t.Helper()
	return fixture(t, map[string]string{
		"go.mod":          "module example.com/app\n\ngo 1.21\n",
		"api/api.go":      "package api\n\nimport \"example.com/app/service\"\n\nfunc Handle() { service.Serve() }\n",
		"api/api_test.go": "package api\n\nimport \"testing\"\n\nfunc TestHandle(t *testing.T) { Handle() }\n",
		"service/svc.go":  "package service\n\nimport \"example.com/app/store\"\n\nfunc Serve() { store.Get() }\n",
		"store/store.go":  "package store\n\nfunc Get() {}\n",
	})
}

func analyze(t *testing.T, source Source, request Request, cache Cache) Analysis {
	t.Helper()
	analysis, err := NewAnalyzer(source, cache).Analyze(context.Background(), request)
	if err != nil {
		t.Fatalf("Analyze(%+v): %v", request, err)
	}
	return analysis
}

// TestProjectAnalysisDoesNotBuildAGraph pins the adaptive cheapest-sufficient
// path end to end: inventory and hierarchy, no parse, no dependency edges.
func TestProjectAnalysisDoesNotBuildAGraph(t *testing.T) {
	source := &countingSource{Index: newIndex(t, chainFixture(t))}
	analysis := analyze(t, source, Request{Intent: IntentProject}, nil)
	if analysis.Coverage.DependenciesSeen {
		t.Error("project analysis built a dependency graph")
	}
	if source.dependencies != 0 {
		t.Errorf("dependency projection called %d times", source.dependencies)
	}
	if len(analysis.Snapshot.Nodes) == 0 {
		t.Fatal("project analysis produced no nodes")
	}
	if analysis.Architecture.Pattern != "not-analyzed" {
		t.Errorf("architecture = %+v, want an explicit not-analyzed marker", analysis.Architecture)
	}
	if analysis.Snapshot.Coverage.State != StatePartiallyObserved {
		t.Errorf("metadata coverage = %s, want PARTIALLY_OBSERVED (declarations unparsed)",
			analysis.Snapshot.Coverage.State)
	}
	if !hasIssue(analysis.Issues, "unparsed_source") {
		t.Errorf("issues = %+v", analysis.Issues)
	}
}

// TestArchitectureAnalysisUsesEdgeEvidence pins that an architecture claim is
// backed by the observed module graph, not by directory names.
func TestArchitectureAnalysisUsesEdgeEvidence(t *testing.T) {
	source := newIndex(t, chainFixture(t))
	analysis := analyze(t, source, Request{Intent: IntentArchitecture}, nil)
	if !analysis.Coverage.DependenciesSeen || analysis.Coverage.GraphEdges == 0 {
		t.Fatalf("coverage = %+v", analysis.Coverage)
	}
	if analysis.Architecture.Pattern != "layered" {
		t.Fatalf("architecture = %+v", analysis.Architecture)
	}
	if analysis.Architecture.State != StateObserved {
		t.Errorf("architecture state = %s", analysis.Architecture.State)
	}
	if len(analysis.Architecture.Evidence) == 0 {
		t.Error("architecture claim has no evidence")
	}
	foundCrossing := false
	for _, boundary := range analysis.Boundaries {
		if boundary.From == "dir:api" && boundary.To == "dir:service" && boundary.Kind == EdgeCrosses {
			foundCrossing = true
			if boundary.Evidence.State == StateUnknown {
				t.Error("boundary has unknown evidence")
			}
		}
	}
	if !foundCrossing {
		t.Fatalf("boundaries = %+v", analysis.Boundaries)
	}
}

// TestUnsupportedLanguageIsAnExplicitIssue pins failure semantics: the analysis
// succeeds, but the unreadable language is named and coverage is qualified.
func TestUnsupportedLanguageIsAnExplicitIssue(t *testing.T) {
	root := fixture(t, map[string]string{
		"go.mod":    "module example.com/app\n\ngo 1.21\n",
		"main.go":   "package main\n\nfunc main() {}\n",
		"script.py": "def run():\n    pass\n",
	})
	analysis := analyze(t, newIndex(t, root), Request{Intent: IntentArchitecture}, nil)
	if !hasIssue(analysis.Issues, "unsupported_language") {
		t.Fatalf("issues = %+v", analysis.Issues)
	}
	if analysis.Snapshot.Coverage.Complete() {
		t.Error("analysis claimed completeness with an unsupported source language")
	}
	if analysis.Architecture.State == StateObserved {
		t.Error("architecture inferred from a partial snapshot was reported OBSERVED")
	}
}

// TestUnknownFocusStaysUnknown pins that a selector with no declaration is an
// explicit unknown, not an empty result and not an error.
func TestUnknownFocusStaysUnknown(t *testing.T) {
	analysis := analyze(t, newIndex(t, chainFixture(t)),
		Request{Intent: IntentImpact, Focus: "NoSuchSymbol", Level: LevelFile}, nil)
	if analysis.Focus == nil {
		t.Fatal("impact analysis produced no focus report")
	}
	if analysis.Focus.Found {
		t.Error("focus claimed a match")
	}
	if analysis.Focus.Evidence.State != StateUnknown {
		t.Errorf("focus evidence = %+v, want UNKNOWN", analysis.Focus.Evidence)
	}
}

// TestKnownFocusReportsStaticNeighbourhood pins that the engine consumes the
// existing impact resolver rather than reimplementing it.
func TestKnownFocusReportsStaticNeighbourhood(t *testing.T) {
	analysis := analyze(t, newIndex(t, chainFixture(t)),
		Request{Intent: IntentImpact, Focus: "Handle", Level: LevelFile}, nil)
	if analysis.Focus == nil || !analysis.Focus.Found {
		t.Fatalf("focus = %+v", analysis.Focus)
	}
	if analysis.Focus.Targets == 0 {
		t.Errorf("focus targets = %+v", analysis.Focus)
	}
	if analysis.Focus.Evidence.State == StateUnknown {
		t.Errorf("focus evidence = %+v", analysis.Focus.Evidence)
	}
}

// TestAnalysisCacheBalancesFreshnessAndReuse pins the cache contract: a hit
// still performs the freshness scan (one inventory), reuses the dependency
// work, and a changed tree misses.
func TestAnalysisCacheBalancesFreshnessAndReuse(t *testing.T) {
	root := chainFixture(t)
	index := newIndex(t, root)
	cache := NewFileCache(t.TempDir())
	request := Request{Intent: IntentArchitecture}

	firstSource := &countingSource{Index: index}
	first := analyze(t, firstSource, request, cache)
	if first.Cache != CacheMiss {
		t.Fatalf("first cache state = %s", first.Cache)
	}
	if firstSource.dependencies == 0 {
		t.Fatal("first analysis did not build the dependency projection")
	}

	secondSource := &countingSource{Index: index}
	second := analyze(t, secondSource, request, cache)
	if second.Cache != CacheHit {
		t.Fatalf("second cache state = %s (%s)", second.Cache, second.CacheReason)
	}
	if secondSource.inventories != 1 {
		t.Errorf("cache hit performed %d inventory scans, want 1 freshness scan", secondSource.inventories)
	}
	if secondSource.dependencies != 0 {
		t.Errorf("cache hit rebuilt the dependency projection %d times", secondSource.dependencies)
	}
	if !reflect.DeepEqual(first.Snapshot.Nodes, second.Snapshot.Nodes) ||
		!reflect.DeepEqual(first.Architecture, second.Architecture) {
		t.Fatal("cached analysis differs from the computed one")
	}

	// A source change must invalidate: a hit here would serve stale evidence.
	if err := os.WriteFile(filepath.Join(root, "api", "extra.go"),
		[]byte("package api\n\nimport \"example.com/app/store\"\n\nfunc Extra() { store.Get() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third := analyze(t, &countingSource{Index: newIndex(t, root)}, request, cache)
	if third.Cache != CacheMiss {
		t.Fatalf("changed tree cache state = %s, want MISS", third.Cache)
	}
	if third.Snapshot.Source.Files <= first.Snapshot.Source.Files {
		t.Errorf("changed tree analysis saw %d files, first saw %d",
			third.Snapshot.Source.Files, first.Snapshot.Source.Files)
	}
}

// TestCorruptCacheIsRecomputedNotTrusted pins corruption handling: a bad entry
// is reported, discarded, and the analysis is recomputed.
func TestCorruptCacheIsRecomputedNotTrusted(t *testing.T) {
	root := chainFixture(t)
	index := newIndex(t, root)
	cache := NewFileCache(t.TempDir())
	request := (Request{Intent: IntentArchitecture}).normalized()
	inventory, err := index.Inventory(context.Background(), ast.InventoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	key := cacheKey(request, inventory.Snapshot)
	if err := os.MkdirAll(cache.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache.Dir(), "analysis-"+key+".json"),
		[]byte("{ this is not an analysis"), 0o600); err != nil {
		t.Fatal(err)
	}

	analysis := analyze(t, index, request, cache)
	if analysis.Cache != CacheInvalid {
		t.Fatalf("cache state = %s, want INVALID", analysis.Cache)
	}
	if !hasIssue(analysis.Issues, "cache_invalid") {
		t.Errorf("issues = %+v", analysis.Issues)
	}
	if analysis.Architecture.Pattern == "" {
		t.Error("corrupt cache entry suppressed the recomputed analysis")
	}
}

// TestConcurrentAnalysisIsSafeAndConsistent pins that the analyzer holds no
// package-level mutable state and that concurrent cached runs agree. The race
// detector turns a shared map or cache race into a failure here.
func TestConcurrentAnalysisIsSafeAndConsistent(t *testing.T) {
	index := newIndex(t, chainFixture(t))
	analyzer := NewAnalyzer(index, NewFileCache(t.TempDir()))
	request := (Request{Intent: IntentArchitecture}).normalized()

	const workers = 8
	results := make([]Analysis, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			results[worker], errs[worker] = analyzer.Analyze(context.Background(), request)
		}(worker)
	}
	wg.Wait()
	for worker, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", worker, err)
		}
	}
	for worker := 1; worker < workers; worker++ {
		if !reflect.DeepEqual(results[0].Snapshot.Nodes, results[worker].Snapshot.Nodes) {
			t.Fatalf("worker %d observed a different structure", worker)
		}
	}
}

// TestAnalysisNeverMutatesTheRepository pins the read-only contract at the
// filesystem boundary, not just in prose.
func TestAnalysisNeverMutatesTheRepository(t *testing.T) {
	root := chainFixture(t)
	before := treeDigest(t, root)
	analyze(t, newIndex(t, root),
		Request{Intent: IntentArchitecture, Focus: "", Level: LevelFile}, NewFileCache(t.TempDir()))
	after := treeDigest(t, root)
	if before != after {
		t.Fatal("structural analysis mutated the repository")
	}
}

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var builder strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		builder.WriteString(path)
		builder.WriteString("|")
		if !info.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			builder.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return builder.String()
}
