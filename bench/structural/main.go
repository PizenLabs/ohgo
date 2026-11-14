// Command structural benchmarks the adaptive structural engine.
//
// It generates deterministic synthetic Go repositories at three scales and
// measures what the engine promises to control: cold analysis cost, warm reuse,
// incremental cost after one file changes, model-facing payload size, and the
// quality of the clustering strategies on the same graph. It makes no model
// calls and needs no network.
//
//	go run ./bench/structural
//
// The numbers are a comparison between the engine's own configurations. They are
// not a claim about wall-clock performance on any other machine or repository.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/structural"
)

func main() {
	repo := flag.String("repo", "", "analyze a real repository path instead of synthetic scales")
	flag.Parse()
	if *repo != "" {
		runRealRepository(*repo)
		return
	}
	scales := []struct {
		name    string
		modules int
		dirs    int
		files   int
	}{
		{"tiny", 2, 2, 3},
		{"small", 4, 5, 4},
		{"medium", 8, 8, 6},
	}
	for _, scale := range scales {
		runScale(scale.name, scale.modules, scale.dirs, scale.files)
	}
}

func runScale(name string, modules, dirs, files int) {
	root, err := generateRepository(modules, dirs, files)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate %s: %v\n", name, err)
		os.Exit(1)
	}
	defer func() {
		_ = os.RemoveAll(root)
	}()
	ctx := context.Background()
	index, err := ast.NewIndexWithOptions(root, ast.IndexOptions{Adaptive: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "index %s: %v\n", name, err)
		os.Exit(1)
	}
	cache := structural.NewFileCache(filepath.Join(root, ".analysis-cache"))
	analyzer := structural.NewAnalyzer(index, cache)
	fileCount := modules * dirs * files

	coldProject := measure(func() {
		mustAnalyze(analyzer, structural.Request{Intent: structural.IntentProject})
	})
	coldArchitecture := measure(func() {
		mustAnalyze(analyzer, structural.Request{Intent: structural.IntentArchitecture})
	})
	warmArchitecture := measure(func() {
		mustAnalyze(analyzer, structural.Request{Intent: structural.IntentArchitecture})
	})
	// One source change invalidates the analysis but not the process-local
	// index; this is the incremental path the engine actually takes.
	target := filepath.Join(root, "module00", "pkg00", "file000.go")
	appendImport(target)
	incrementalArchitecture := measure(func() {
		mustAnalyze(analyzer, structural.Request{Intent: structural.IntentArchitecture})
	})

	analysis := mustAnalyze(analyzer, structural.Request{Intent: structural.IntentArchitecture})
	payloadBytes := jsonBytes(analysis.Snapshot.Nodes)
	moduleSummaryBytes := jsonBytes(structural.Summaries(analysis.Snapshot, structural.LevelModule, 0))
	graph, moduleGraph := projectGraphs(ctx, index)
	comparison := comparePartitions(analysis.Snapshot, graph, moduleGraph)

	fmt.Printf("\n### %s (%d files, %d modules)\n\n", name, fileCount, modules)
	fmt.Println("| metric | value |")
	fmt.Println("|---|---|")
	fmt.Printf("| cold project analysis | %s |\n", coldProject)
	fmt.Printf("| cold architecture analysis | %s |\n", coldArchitecture)
	fmt.Printf("| warm architecture (cache hit) | %s |\n", warmArchitecture)
	fmt.Printf("| incremental architecture (1 changed file) | %s |\n", incrementalArchitecture)
	fmt.Printf("| snapshot nodes | %d |\n", len(analysis.Snapshot.Nodes))
	fmt.Printf("| node JSON | %d bytes |\n", payloadBytes)
	fmt.Printf("| module-summary JSON | %d bytes |\n", moduleSummaryBytes)
	fmt.Printf("| coverage | %s (%.2f) |\n", analysis.Coverage.State, analysis.Coverage.Confidence)
	fmt.Printf("| architecture | %s (%.2f, %s) |\n",
		analysis.Architecture.Pattern, analysis.Architecture.Confidence, analysis.Architecture.State)
	fmt.Println()
	fmt.Println("| partition | communities | modularity |")
	fmt.Println("|---|---|---|")
	for _, row := range comparison {
		fmt.Printf("| %s | %d | %+.3f |\n", row.name, row.communities, row.modularity)
	}
}

// runRealRepository validates the engine against a repository the author did not
// design the fixtures for. It reports what the engine observed, including the
// parts it could not.
func runRealRepository(path string) {
	index, err := ast.NewIndexWithOptions(path, ast.IndexOptions{Adaptive: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "index %s: %v\n", path, err)
		os.Exit(1)
	}
	analyzer := structural.NewAnalyzer(index, nil)

	projectTime := measure(func() {
		analysis := mustAnalyze(analyzer, structural.Request{Intent: structural.IntentProject})
		fmt.Printf("### %s — project analysis\n\n", path)
		printAnalysis(analysis)
	})
	architectureTime := measure(func() {
		analysis := mustAnalyze(analyzer, structural.Request{Intent: structural.IntentArchitecture})
		fmt.Printf("\n### %s — architecture analysis\n\n", path)
		printAnalysis(analysis)
	})
	fmt.Printf("\n| run | duration |\n|---|---|\n")
	fmt.Printf("| project | %s |\n", projectTime)
	fmt.Printf("| architecture | %s |\n", architectureTime)
}

func printAnalysis(analysis structural.Analysis) {
	fmt.Println("| metric | value |")
	fmt.Println("|---|---|")
	fmt.Printf("| intent | %s |\n", analysis.Request.Intent)
	fmt.Printf("| plan stop | %s |\n", analysis.Plan.StopReason)
	fmt.Printf("| selected strategies | %d |\n", len(analysis.Plan.Selected))
	fmt.Printf("| level | %s |\n", analysis.Snapshot.Level)
	fmt.Printf("| nodes | %d |\n", len(analysis.Snapshot.Nodes))
	fmt.Printf("| coverage | %s (%.2f) |\n", analysis.Coverage.State, analysis.Coverage.Confidence)
	fmt.Printf("| files / parsed / pending / failed | %d / %d / %d / %d |\n",
		analysis.Coverage.Files, analysis.Coverage.Parsed,
		analysis.Coverage.Pending, analysis.Coverage.Failed)
	fmt.Printf("| graph nodes / edges | %d / %d |\n",
		analysis.Coverage.GraphNodes, analysis.Coverage.GraphEdges)
	fmt.Printf("| unresolved dependencies | %d |\n", analysis.Coverage.Unresolved)
	fmt.Printf("| architecture | %s (%.2f, %s) |\n",
		analysis.Architecture.Pattern, analysis.Architecture.Confidence, analysis.Architecture.State)
	fmt.Printf("| node JSON | %d bytes |\n", jsonBytes(analysis.Snapshot.Nodes))
	summary := structural.Summaries(analysis.Snapshot, structural.LevelModule, 0)
	fmt.Printf("| module-summary JSON | %d bytes |\n", jsonBytes(summary))
	if len(analysis.Issues) > 0 {
		fmt.Println()
		fmt.Println("| issue | detail |")
		fmt.Println("|---|---|")
		for _, issue := range analysis.Issues {
			fmt.Printf("| %s | %s |\n", issue.Code, issue.Detail)
		}
	}
	if len(summary) > 0 {
		fmt.Println()
		fmt.Println("| module | files | tests | symbols | evidence |")
		fmt.Println("|---|---:|---:|---:|---|")
		for i, node := range summary {
			if i >= 12 {
				fmt.Printf("| … %d more | | | | |\n", len(summary)-12)
				break
			}
			fmt.Printf("| %s | %d | %d | %d | %s |\n",
				node.Path, node.Files, node.TestFiles, node.Symbols, node.Evidence.State)
		}
	}
	if len(analysis.Communities) > 0 {
		largest := 0
		for _, community := range analysis.Communities {
			if len(community.Members) > largest {
				largest = len(community.Members)
			}
		}
		fmt.Printf("\ncommunities: %d (largest %d members)\n", len(analysis.Communities), largest)
	}
}

func mustAnalyze(analyzer *structural.Analyzer, request structural.Request) structural.Analysis {
	analysis, err := analyzer.Analyze(context.Background(), request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "analyze: %v\n", err)
		os.Exit(1)
	}
	return analysis
}

func measure(run func()) time.Duration {
	start := time.Now()
	run()
	return time.Since(start)
}

func projectGraphs(ctx context.Context, index *ast.Index) (*structural.Graph, *structural.Graph) {
	inventory, err := index.Inventory(ctx, ast.InventoryQuery{Parse: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "inventory: %v\n", err)
		os.Exit(1)
	}
	deps, err := index.Dependencies(ctx, ast.DependencyQuery{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "dependencies: %v\n", err)
		os.Exit(1)
	}
	snapshot := structural.Build(inventory, structural.BuildOptions{Level: structural.LevelPackage})
	return structural.ProjectGraphs(snapshot, deps)
}

type partitionScore struct {
	name        string
	communities int
	modularity  float64
}

// comparePartitions runs every grouping strategy on the same dependency graph.
//
// Modularity label propagation is one strategy, not the default answer: a flat partition, the path
// hierarchy, and SCC condensation are measured against it on the same evidence.
func comparePartitions(snapshot *structural.Snapshot, graph, moduleGraph *structural.Graph) []partitionScore {
	target := graph
	if target == nil || target.Len() == 0 {
		target = moduleGraph
	}
	if target == nil || target.Len() == 0 {
		return nil
	}
	flat := make([]structural.Community, 0, target.Len())
	for _, id := range target.Nodes() {
		flat = append(flat, structural.Community{ID: "flat-" + id, Members: []string{id}})
	}
	prefixGroups := map[string][]string{}
	for _, id := range target.Nodes() {
		prefix := "."
		if node, ok := snapshot.Node(id); ok {
			prefix = firstSegment(node.Path)
		}
		prefixGroups[prefix] = append(prefixGroups[prefix], id)
	}
	var prefix []structural.Community
	for _, name := range sortedKeys(prefixGroups) {
		prefix = append(prefix, structural.Community{ID: "prefix-" + name, Members: prefixGroups[name]})
	}
	componentGroups := map[string][]string{}
	for _, component := range target.SCCs() {
		key := component[0]
		componentGroups[key] = append(componentGroups[key], component...)
	}
	var components []structural.Community
	for _, name := range sortedKeys(componentGroups) {
		components = append(components, structural.Community{ID: "scc-" + name, Members: componentGroups[name]})
	}
	modularity := structural.ModularityClustering(target, structural.ModularitySweepBound)

	candidates := []struct {
		name        string
		communities []structural.Community
	}{
		{"flat (no grouping)", flat},
		{"path prefix", prefix},
		{"SCC condensation", components},
		{"modularity label propagation", modularity.Communities},
	}
	out := make([]partitionScore, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, partitionScore{
			name:        candidate.name,
			communities: len(candidate.communities),
			modularity:  structural.PartitionModularity(target, candidate.communities),
		})
	}
	return out
}

func firstSegment(value string) string {
	value = strings.TrimPrefix(value, "./")
	if index := strings.Index(value, "/"); index >= 0 {
		return value[:index]
	}
	if value == "" {
		return "."
	}
	return value
}

func sortedKeys(set map[string][]string) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func jsonBytes(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(data)
}

// generateRepository writes a deterministic module-shaped Go repository. Each
// directory imports the next directory in its module and the first directory of
// the next module, so the dependency graph has both layering and cross-module
// edges without being a ring.
func generateRepository(modules, dirs, files int) (string, error) {
	root, err := os.MkdirTemp("", "ohgo-structural-bench-*")
	if err != nil {
		return "", err
	}
	write := func(rel, content string) error {
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(content), 0o644)
	}
	if err := write("go.mod", "module example.com/bench\n\ngo 1.21\n"); err != nil {
		return root, err
	}
	for module := range modules {
		for dir := range dirs {
			pkg := fmt.Sprintf("pkg%02d%02d", module, dir)
			for file := range files {
				var imports strings.Builder
				imports.WriteString("package " + pkg + "\n\n")
				if dir+1 < dirs {
					fmt.Fprintf(&imports, "import next%d \"example.com/bench/module%02d/pkg%02d%02d\"\n\n",
						module, module, module, dir+1)
				} else if module+1 < modules {
					fmt.Fprintf(&imports, "import nextm%d \"example.com/bench/module%02d/pkg%02d00\"\n\n",
						module, module+1, module+1)
				}
				fmt.Fprintf(&imports, "func Fn%02d() int {\n\treturn %d\n}\n", file, file)
				if strings.Contains(imports.String(), "import next") {
					imports.WriteString("\nfunc Use() {\n")
					if dir+1 < dirs {
						fmt.Fprintf(&imports, "\t_ = next%d.Fn00()\n", module)
					} else {
						fmt.Fprintf(&imports, "\t_ = nextm%d.Fn00()\n", module)
					}
					imports.WriteString("}\n")
				}
				rel := fmt.Sprintf("module%02d/pkg%02d%02d/file%03d.go", module, module, dir, file)
				if err := write(rel, imports.String()); err != nil {
					return root, err
				}
			}
		}
	}
	return root, nil
}

// appendImport adds one dependency to an existing file, simulating a developer
// editing exactly one file between analyses.
func appendImport(path string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer func() {
		_ = file.Close()
	}()
	_, _ = file.WriteString("\nfunc Extra() {}\n")
}
