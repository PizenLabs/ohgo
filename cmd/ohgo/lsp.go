package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/semantic"
)

// lookPathFunc is the tool-discovery seam. Tests substitute it so the
// capability roster is deterministic on a machine with arbitrary tools
// installed.
type lookPathFunc func(string) (string, error)

// languageServerSpecs discovers the language servers available on PATH.
//
// Discovery is explicit and local: no server is started here, and a missing
// server is simply absent from the roster. The manager starts only the server
// a request's language selects. This is the single owner of the known-server
// table; a new server is added here, not in an investigation call site.
func languageServerSpecs(root string, lookPath lookPathFunc) []semantic.Spec {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	var specs []semantic.Spec
	add := func(name string, languages []string, command ...string) {
		if len(command) == 0 {
			return
		}
		path, err := lookPath(command[0])
		if err != nil {
			return
		}
		command[0] = path
		specs = append(specs, semantic.Spec{
			Name: name, Languages: languages, Command: command, Root: root,
		})
	}

	// Go keeps the module root: gopls loads the module whose go.mod is at or
	// below the workspace, and a monorepo root with no go.mod makes it answer
	// nothing for a symbol that exists.
	add("gopls", []string{"go"}, "gopls", "serve")
	add("typescript-language-server",
		[]string{"typescript", "typescriptreact", "javascript", "javascriptreact"},
		"typescript-language-server", "--stdio")
	add("pyright", []string{"python"}, "pyright-langserver", "--stdio")
	add("pylsp", []string{"python"}, "pylsp")
	add("rust-analyzer", []string{"rust"}, "rust-analyzer")
	add("jdtls", []string{"java"}, "jdtls")
	add("clangd", []string{"c", "cpp"}, "clangd")
	add("ruby-lsp", []string{"ruby"}, "ruby-lsp")
	add("csharp-ls", []string{"csharp"}, "csharp-ls")
	add("kotlin-language-server", []string{"kotlin"}, "kotlin-language-server")

	for i := range specs {
		switch specs[i].Name {
		case "gopls":
			specs[i].Root = semanticWorkspaceRoot(root)
		case "typescript-language-server":
			specs[i].Root = nearestProjectRoot(root, "tsconfig.json", "package.json")
		case "pyright", "pylsp":
			specs[i].Root = nearestProjectRoot(root, "pyproject.toml", "setup.py", "requirements.txt")
		case "rust-analyzer":
			specs[i].Root = nearestProjectRoot(root, "Cargo.toml")
		case "jdtls":
			specs[i].Root = nearestProjectRoot(root, "pom.xml", "build.gradle", "build.gradle.kts")
		case "clangd":
			specs[i].Root = nearestProjectRoot(root, "compile_commands.json", "CMakeLists.txt")
		case "ruby-lsp":
			specs[i].Root = nearestProjectRoot(root, "Gemfile")
		case "csharp-ls":
			specs[i].Root = nearestProjectRoot(root, "*.csproj", "*.sln")
		case "kotlin-language-server":
			specs[i].Root = nearestProjectRoot(root, "build.gradle.kts", "build.gradle")
		}
	}
	return specs
}

// semanticWorkspaceRoot picks the module gopls should load. It never guesses
// among several modules: a multi-module workspace keeps the project root and
// lets the server report what it can.
func semanticWorkspaceRoot(root string) string {
	return nearestProjectRoot(root, "go.mod")
}

// nearestProjectRoot picks the single project root carrying one of markers.
//
// A monorepo with several matching projects keeps the workspace root: choosing
// one arbitrarily would make the server answer for the wrong project. The walk
// is bounded and skips the same dependency directories the structural index
// skips.
func nearestProjectRoot(root string, markers ...string) string {
	if len(markers) == 0 {
		return root
	}
	found := map[string]bool{}
	entries := 0
	truncated := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if entries > 20000 {
			truncated = true
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		for _, marker := range markers {
			if marker == entry.Name() || (strings.HasPrefix(marker, "*") && strings.HasSuffix(entry.Name(), marker[1:])) {
				found[filepath.Dir(path)] = true
				return nil
			}
		}
		return nil
	})
	if len(found) == 1 && !truncated {
		for dir := range found {
			return dir
		}
	}
	return root
}

// structuralSnapshotID is the identity a semantic observation is correlated
// with. An index error yields empty provenance rather than a fabricated id.
func structuralSnapshotID(source *tools.StructuralSource) string {
	if source == nil {
		return ""
	}
	idx, err := source.Index()
	if err != nil {
		return ""
	}
	return idx.Info().SnapshotID
}

// dominantLanguage is the workspace's most common supported language. It is a
// lazy fallback for a semantic request that names a symbol but no path; the
// structural index is already warm by the time the planner reaches semantic.
func dominantLanguage(source *tools.StructuralSource) string {
	if source == nil {
		return ""
	}
	idx, err := source.Index()
	if err != nil {
		return ""
	}
	best := ""
	bestFiles := 0
	for _, coverage := range idx.Info().Languages {
		if !coverage.Supported || coverage.Files == 0 {
			continue
		}
		name := string(coverage.Language)
		if coverage.Files > bestFiles || (coverage.Files == bestFiles && name < best) {
			best, bestFiles = name, coverage.Files
		}
	}
	return lspLanguageID(best)
}

// lspLanguageID maps a structural language identity to the language id a
// language server expects. Unknown identities stay empty so selection never
// guesses.
func lspLanguageID(language string) string {
	switch ast.Language(language) {
	case ast.LanguageGo:
		return "go"
	case ast.LanguageTypeScript, ast.LanguageTSX:
		return "typescript"
	case ast.LanguageJavaScript, ast.LanguageJSX:
		return "javascript"
	case ast.LanguagePython:
		return "python"
	case ast.LanguageRust:
		return "rust"
	case ast.LanguageJava:
		return "java"
	case ast.LanguageC:
		return "c"
	case ast.LanguageCpp:
		return "cpp"
	case ast.LanguageRuby:
		return "ruby"
	case ast.LanguageCSharp:
		return "csharp"
	case ast.LanguageKotlin:
		return "kotlin"
	default:
		return ""
	}
}
