package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// phase8Root is the repository root from the cmd/ohgo test working directory.
func phase8Root() string { return filepath.Join("..", "..") }

// BenchmarkPhase8StructuralColdIndex measures a cold snapshot of the real
// repository. It is a benchmark, not a normal test: plain `go test` does not
// pay the cost.
func BenchmarkPhase8StructuralColdIndex(b *testing.B) {
	root, err := filepath.Abs(phase8Root())
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		source := tools.NewStructuralSource(root)
		if _, err := source.Index(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPhase8WarmStructuralQueries measures the bounded query paths after
// one cold build.
func BenchmarkPhase8WarmStructuralQueries(b *testing.B) {
	root, err := filepath.Abs(phase8Root())
	if err != nil {
		b.Fatal(err)
	}
	idx, err := tools.NewStructuralSource(root).Index()
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if _, err := idx.Snapshot(ctx); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := idx.Lookup("ToolActionClasses"); err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Files(ctx, ast.FileQuery{Limit: 50}); err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Packages(ctx, ast.PackageQuery{Limit: 20}); err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Impact(ctx, ast.ImpactQuery{Symbol: "ToolActionClasses", Depth: 1, Limit: 50}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPhase8IncrementalSingleFile copies the repository to a temporary
// tree, indexes it once, then appends one declaration per iteration. Each
// measured query must parse exactly one Go file.
func BenchmarkPhase8IncrementalSingleFile(b *testing.B) {
	src, err := filepath.Abs(phase8Root())
	if err != nil {
		b.Fatal(err)
	}
	root := b.TempDir()
	if err := copyPhase8Tree(src, root); err != nil {
		b.Fatal(err)
	}
	idx, err := tools.NewStructuralSource(root).Index()
	if err != nil {
		b.Fatal(err)
	}
	target := filepath.Join(root, "internal", "intelligence", "ast", "model.go")
	data, err := os.ReadFile(target)
	if err != nil {
		b.Fatal(err)
	}
	base := string(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := fmt.Sprintf("Phase8Incremental%d", i)
		if err := os.WriteFile(target, []byte(base+"\nfunc "+name+"() {}\n"), 0o644); err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Lookup(name); err != nil {
			b.Fatal(err)
		}
	}
	info := idx.Info()
	b.ReportMetric(float64(info.LastParsedFiles), "parsed_files")
}

// copyPhase8Tree copies only the files the structural index reads: Go source,
// go.mod, and go.work. That keeps benchmark setup independent of generated
// artifacts and Git state.
func copyPhase8Tree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		switch filepath.Ext(path) {
		case ".go":
		default:
			switch entry.Name() {
			case "go.mod", "go.work":
			default:
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}

// TestPhase8StructuralSnapshot is gated because a normal unit run should not
// parse the whole repository. Set OHGO_PHASE8=1 to validate the real runtime.
func TestPhase8StructuralSnapshot(t *testing.T) {
	if os.Getenv("OHGO_PHASE8") != "1" {
		t.Skip("set OHGO_PHASE8=1 to validate the real-repository structural snapshot")
	}
	root, err := filepath.Abs(phase8Root())
	if err != nil {
		t.Fatal(err)
	}
	idx, err := tools.NewStructuralSource(root).Index()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	info, err := idx.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if info.State != ast.FreshnessCurrent || info.Generation == 0 || info.GoFiles == 0 {
		t.Fatalf("snapshot = %+v, want a current non-empty Go snapshot", info)
	}
	if info.ParseFailures != 0 {
		t.Errorf("ParseFailures = %d on the repository tree", info.ParseFailures)
	}
	impact, err := idx.Impact(context.Background(), ast.ImpactQuery{Symbol: "ToolActionClasses", Depth: 1, Limit: 50})
	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	if !impact.Found {
		t.Errorf("impact target not found in %+v", impact)
	}
}
