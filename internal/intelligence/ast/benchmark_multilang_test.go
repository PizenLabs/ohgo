//go:build cgo

package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func benchmarkMultiLanguageRoot(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	files := map[string]string{
		"web/app.ts": `import { uniqueHelper } from "./util";
export function caller(): number { return uniqueHelper(1); }
`,
		"web/util.ts": `export function uniqueHelper(x: number): number { return x * 2; }
export interface Shape { area(): number }
export class Circle implements Shape { area(): number { return uniqueHelper(1); } }
`,
		"java/com/example/Service.java": `package com.example;
public class Service { int run() { return uniqueHelper(1); } }
`,
		"rust/lib.rs": `pub fn helper() -> f64 { 1.0 }
fn main() { helper(); }
`,
		"csharp/Service.cs": `namespace Example;
public class Service { public double Area() { return 1.0; } }
`,
		"kotlin/Service.kt": `package example
fun main() { helper() }
fun helper(): Int = 1
`,
		"ruby/service.rb": `module Example
  def self.helper(x)
    x
  end
end
`,
		"cpp/service.cpp": `namespace app {
int helper(int x) { return x; }
int main() { return helper(1); }
}
`,
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return root
}

// BenchmarkPhase9AdaptiveOverview measures the metadata-first path. It must
// not parse source files even when eight adapters are registered.
func BenchmarkPhase9AdaptiveOverview(b *testing.B) {
	root := benchmarkMultiLanguageRoot(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx, err := NewIndexWithOptions(root, IndexOptions{Adaptive: true})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Overview(context.Background(), 20); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPhase9TargetedLookup measures the first structural activation over
// the mixed fixture. The index is cold on each iteration so the parse cost is
// visible.
func BenchmarkPhase9TargetedLookup(b *testing.B) {
	root := benchmarkMultiLanguageRoot(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx, err := NewIndexWithOptions(root, IndexOptions{Adaptive: true})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Lookup("uniqueHelper"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPhase9IncrementalOneFile measures a warm update after one
// TypeScript file changes.
func BenchmarkPhase9IncrementalOneFile(b *testing.B) {
	root := benchmarkMultiLanguageRoot(b)
	idx, err := NewIndexWithOptions(root, IndexOptions{Adaptive: true})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := idx.Lookup("uniqueHelper"); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(root, "web", "util.ts")
	base, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := append(append([]byte(nil), base...), []byte(fmt.Sprintf("\nexport function added%d(): number { return uniqueHelper(1); }\n", i))...)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			b.Fatal(err)
		}
		if _, err := idx.Lookup(fmt.Sprintf("added%d", i)); err != nil {
			b.Fatal(err)
		}
	}
}
