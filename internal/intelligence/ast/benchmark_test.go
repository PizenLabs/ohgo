package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeBenchRepo writes n package-shaped Go files plus go.mod.
func writeBenchRepo(b *testing.B, n int) string {
	b.Helper()
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/bench\n\ngo 1.21\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%03d", i%20))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		body := fmt.Sprintf("package pkg%03d\n\nfunc F%d() {}\n", i%20, i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.go", i)), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return root
}

func BenchmarkColdIndex(b *testing.B) {
	root := writeBenchRepo(b, 200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := NewIndex(root); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkIncrementalSingleFileUpdate(b *testing.B) {
	root := writeBenchRepo(b, 200)
	idx, err := NewIndex(root)
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(root, "pkg000", "f0000.go")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := fmt.Sprintf("package pkg000\n\nfunc F0() {}\nfunc Changed%d() {}\n", i)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Lookup("Changed" + fmt.Sprint(i)); err != nil {
			b.Fatal(err)
		}
	}
	info := idx.Info()
	b.ReportMetric(float64(info.LastParsedFiles), "parsed_files")
}

func BenchmarkWarmStructuralQueries(b *testing.B) {
	root := writeBenchRepo(b, 200)
	idx, err := NewIndex(root)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := idx.Lookup("F0"); err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Files(ctx, FileQuery{Limit: 50}); err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Packages(ctx, PackageQuery{Limit: 20}); err != nil {
			b.Fatal(err)
		}
	}
}
