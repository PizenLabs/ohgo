package structural

import (
	"context"
	"fmt"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// syntheticInventory builds an inventory without a filesystem so a benchmark
// measures the analysis layer, not the OS cache.
func syntheticInventory(modules, dirsPerModule, filesPerDir int) ast.Inventory {
	inventory := ast.Inventory{}
	inventory.Snapshot = ast.SnapshotInfo{
		Module:             "example.com/synthetic",
		Files:              modules * dirsPerModule * filesPerDir,
		SourceFiles:        modules * dirsPerModule * filesPerDir,
		ParsedFiles:        modules * dirsPerModule * filesPerDir,
		GoFiles:            modules * dirsPerModule * filesPerDir,
		Directories:        modules * dirsPerModule,
		Packages:           modules * dirsPerModule,
		StructuralComplete: true,
		ScopeComplete:      true,
		InventoryComplete:  true,
	}
	for module := range modules {
		for dir := range dirsPerModule {
			dirPath := fmt.Sprintf("module%02d/pkg%02d", module, dir)
			inventory.Packages = append(inventory.Packages, ast.PackageInfo{
				Dir: dirPath, Name: fmt.Sprintf("pkg%02d", dir), Files: filesPerDir,
			})
			for file := range filesPerDir {
				inventory.Files = append(inventory.Files, ast.FileInfo{
					Path:     fmt.Sprintf("%s/file%02d.go", dirPath, file),
					Dir:      dirPath,
					Language: "go",
					Kind:     ast.FileGoSource,
					Parsed:   true,
					Symbols:  3,
				})
			}
		}
	}
	inventory.Snapshot.Packages = modules * dirsPerModule
	return inventory
}

// syntheticDependencies builds a deterministic dependency projection over the
// same shape, with one import per directory plus a cross-module edge.
func syntheticDependencies(modules, dirsPerModule int) ast.DependencyResult {
	result := ast.DependencyResult{Complete: true}
	result.Snapshot = ast.SnapshotInfo{Module: "example.com/synthetic", StructuralComplete: true}
	for module := range modules {
		for dir := range dirsPerModule {
			dirPath := fmt.Sprintf("module%02d/pkg%02d", module, dir)
			result.Nodes = append(result.Nodes, ast.DependencyNode{Path: dirPath})
			if dir+1 < dirsPerModule {
				result.Edges = append(result.Edges, ast.DependencyEdge{
					From:    dirPath,
					To:      fmt.Sprintf("module%02d/pkg%02d", module, dir+1),
					Witness: ast.DependencyWitness{Path: dirPath + "/file00.go", Line: 3},
				})
			}
			if module+1 < modules {
				result.Edges = append(result.Edges, ast.DependencyEdge{
					From:    dirPath,
					To:      fmt.Sprintf("module%02d/pkg%02d", module+1, dir),
					Witness: ast.DependencyWitness{Path: dirPath + "/file00.go", Line: 4},
				})
			}
		}
	}
	result.TotalNodes = len(result.Nodes)
	result.TotalEdges = len(result.Edges)
	return result
}

// staticSource is a Source with no filesystem and no parser.
type staticSource struct {
	inventory ast.Inventory
	deps      ast.DependencyResult
}

func (s staticSource) Inventory(context.Context, ast.InventoryQuery) (ast.Inventory, error) {
	return s.inventory, nil
}

func (s staticSource) Dependencies(context.Context, ast.DependencyQuery) (ast.DependencyResult, error) {
	return s.deps, nil
}

func BenchmarkBuildHierarchy(b *testing.B) {
	for _, size := range []struct {
		name    string
		modules int
		dirs    int
		files   int
	}{
		{"small", 4, 5, 5},
		{"medium", 20, 10, 10},
		{"large", 40, 25, 10},
	} {
		inventory := syntheticInventory(size.modules, size.dirs, size.files)
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				snapshot := Build(inventory, BuildOptions{Level: LevelFile})
				if len(snapshot.Nodes) == 0 {
					b.Fatal("empty hierarchy")
				}
			}
		})
	}
}

func BenchmarkModularityClustering(b *testing.B) {
	for _, size := range []struct {
		name  string
		nodes int
	}{
		{"100", 100},
		{"1000", 1000},
	} {
		ids := make([]string, 0, size.nodes)
		var edges []GraphEdge
		for i := range size.nodes {
			id := fmt.Sprintf("n%05d", i)
			ids = append(ids, id)
			if i > 0 {
				edges = append(edges, GraphEdge{From: ids[i-1], To: id})
			}
			if i > 10 {
				edges = append(edges, GraphEdge{From: ids[i-10], To: id})
			}
		}
		graph := NewGraph(ids, edges)
		b.Run(size.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				partition := ModularityClustering(graph, ModularitySweepBound)
				if len(partition.Communities) == 0 {
					b.Fatal("empty partition")
				}
			}
		})
	}
}

func BenchmarkAnalyzeProject(b *testing.B) {
	inventory := syntheticInventory(10, 10, 10)
	source := staticSource{inventory: inventory}
	analyzer := NewAnalyzer(source, nil)
	request := Request{Intent: IntentProject}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := analyzer.Analyze(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyzeArchitecture(b *testing.B) {
	inventory := syntheticInventory(10, 10, 10)
	source := staticSource{
		inventory: inventory,
		deps:      syntheticDependencies(10, 10),
	}
	analyzer := NewAnalyzer(source, nil)
	request := Request{Intent: IntentArchitecture}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := analyzer.Analyze(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}
