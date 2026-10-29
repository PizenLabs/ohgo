package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/jsonschema"
)

// benchTree builds a repository-shaped tree for search benchmarks.
//
// The mix matters: mostly source files, a few large generated ones, and a
// directory of build artefacts that must be pruned.
func benchTree(b *testing.B, files int) string {
	b.Helper()
	root := b.TempDir()
	var body string
	for i := 0; i < 400; i++ {
		body += fmt.Sprintf("func handler%d() error {\n\treturn nil\n}\n\n", i)
	}

	for i := 0; i < files; i++ {
		dir := filepath.Join(root, "pkg", fmt.Sprintf("mod%02d", i%20))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		path := filepath.Join(dir, fmt.Sprintf("file%04d.go", i))
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}

	// A pruned directory that would dominate the walk if it were not excluded.
	artifacts := filepath.Join(root, "node_modules", "left-pad")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts, "index.js"), []byte("module.exports = 1;\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	return root
}

func BenchmarkGlob(b *testing.B) {
	root := benchTree(b, 500)
	tool := NewGlobTool(root)

	b.Run("shallow", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			result := tool.Execute(testContext(), map[string]any{"pattern": "*.go"})
			if result.IsError {
				b.Fatal(result.Text)
			}
		}
	})

	b.Run("recursive", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			result := tool.Execute(testContext(), map[string]any{"pattern": "**/*.go"})
			if result.IsError {
				b.Fatal(result.Text)
			}
		}
	})
}

func BenchmarkGrep(b *testing.B) {
	root := benchTree(b, 500)
	tool := NewGrepTool(root)

	b.Run("literal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			result := tool.Execute(testContext(), map[string]any{"pattern": "handler0123"})
			if result.IsError {
				b.Fatal(result.Text)
			}
		}
	})

	b.Run("regex", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			result := tool.Execute(testContext(), map[string]any{"pattern": `func handler\d+\(\) error`})
			if result.IsError {
				b.Fatal(result.Text)
			}
		}
	})

	b.Run("no_match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			result := tool.Execute(testContext(), map[string]any{"pattern": "zzz_no_such_symbol_zzz"})
			if result.IsError {
				b.Fatal(result.Text)
			}
		}
	})
}

func BenchmarkReadTool(b *testing.B) {
	root := b.TempDir()
	path := filepath.Join(root, "a.go")
	body := make([]byte, 0, 200_000)
	for i := 0; i < 4000; i++ {
		body = append(body, []byte("func handler() error {\n\treturn nil\n}\n")...)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		b.Fatal(err)
	}
	tool := NewReadTool(root)

	b.Run("full", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if result := tool.Execute(testContext(), map[string]any{"path": "a.go"}); result.IsError {
				b.Fatal(result.Text)
			}
		}
	})
	b.Run("paged", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if result := tool.Execute(testContext(), map[string]any{
				"path": "a.go", "offset": 2000.0, "limit": 50.0,
			}); result.IsError {
				b.Fatal(result.Text)
			}
		}
	})
}

func BenchmarkSchemaValidate(b *testing.B) {
	schema := jsonschema.MustCompile([]byte(`{
		"type":"object",
		"properties":{
			"path":{"type":"string"},
			"offset":{"type":"integer"},
			"limit":{"type":"integer"}
		},
		"required":["path"]
	}`))
	args := map[string]any{"path": "a.go", "offset": 1.0, "limit": 100.0}

	b.Run("valid", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := schema.Validate(args); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("missing_required", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if err := schema.Validate(map[string]any{"offset": 1.0}); err == nil {
				b.Fatal("expected a validation error")
			}
		}
	})
}
