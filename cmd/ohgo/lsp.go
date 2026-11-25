package main

import (
	"io/fs"
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

// projectWalkFunc is the workspace-walk seam for project-root discovery.
// Tests substitute it so they can observe whether a startup path touched the
// tree without depending on timing or filesystem size.
type projectWalkFunc func(root string, fn fs.WalkDirFunc) error

// maxProjectRootEntries bounds the one project-root walk. It is a safety cap,
// not a target: a repository larger than this is treated as ambiguous and the
// server loads the workspace root, which is always safe.
const maxProjectRootEntries = 20000

// languageServerSpecs discovers the language servers available on PATH and
// resolves the project root each one should load.
//
// This is the explicit diagnostic path (doctor, tests). Startup composition
// must use languageServerCommands plus resolveProjectRoots on demand: resolving
// every root walks the workspace once per server, which is measurable on a
// large checkout (a Rust tree with a built target/ directory is the canonical
// case).
func languageServerSpecs(root string, lookPath lookPathFunc) []semantic.Spec {
	return languageServerSpecsWithWalk(root, lookPath, filepath.WalkDir)
}

// languageServerSpecsWithWalk is languageServerSpecs with the walk injected.
func languageServerSpecsWithWalk(root string, lookPath lookPathFunc, walk projectWalkFunc) []semantic.Spec {
	return resolveProjectRoots(root, languageServerCommands(lookPath), walk)
}

// languageServerCommands discovers the language servers available on PATH.
//
// Discovery is explicit and local: no server is started here, and a missing
// server is simply absent from the roster. It performs no filesystem walk, so
// it is safe on a startup path. The manager starts only the server a request's
// language selects. This is the single owner of the known-server table; a new
// server is added here, not in an investigation call site.
func languageServerCommands(lookPath lookPathFunc) []semantic.Spec {
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
			Name: name, Languages: languages, Command: command,
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
	return specs
}

// projectRootMarkers names the files that identify the project a server owns.
// Empty means the server loads the workspace root.
func projectRootMarkers(server string) []string {
	switch server {
	case "gopls":
		return []string{"go.mod"}
	case "typescript-language-server":
		return []string{"tsconfig.json", "package.json"}
	case "pyright", "pylsp":
		return []string{"pyproject.toml", "setup.py", "requirements.txt"}
	case "rust-analyzer":
		return []string{"Cargo.toml"}
	case "jdtls":
		return []string{"pom.xml", "build.gradle", "build.gradle.kts"}
	case "clangd":
		return []string{"compile_commands.json", "CMakeLists.txt"}
	case "ruby-lsp":
		return []string{"Gemfile"}
	case "csharp-ls":
		return []string{"*.csproj", "*.sln"}
	case "kotlin-language-server":
		return []string{"build.gradle.kts", "build.gradle"}
	default:
		return nil
	}
}

// resolveProjectRoots assigns each server the single project directory that
// owns its language.
//
// One bounded walk collects every marker any server needs, so a machine with
// several language servers still scans the workspace once rather than once per
// server. Skip rules match the structural index, so build output such as
// Rust's target/ is never mistaken for a project.
func resolveProjectRoots(root string, specs []semantic.Spec, walk projectWalkFunc) []semantic.Spec {
	if len(specs) == 0 {
		return specs
	}
	if walk == nil {
		walk = filepath.WalkDir
	}
	needed := make(map[string]struct{})
	for _, spec := range specs {
		for _, marker := range projectRootMarkers(spec.Name) {
			needed[marker] = struct{}{}
		}
	}
	scan := scanProjectMarkers(root, needed, walk)
	resolved := make([]semantic.Spec, len(specs))
	copy(resolved, specs)
	for i := range resolved {
		markers := projectRootMarkers(resolved[i].Name)
		resolved[i].Root = scan.rootFor(root, markers)
	}
	return resolved
}

// projectMarkerScan is the result of one bounded workspace walk.
type projectMarkerScan struct {
	// dirs maps a marker to the directories that contain it.
	dirs map[string]map[string]struct{}
	// truncated reports that the entry cap was reached. A truncated scan is
	// ambiguous by construction, so callers keep the workspace root.
	truncated bool
}

// scanProjectMarkers walks root once and indexes every requested marker.
func scanProjectMarkers(root string, markers map[string]struct{}, walk projectWalkFunc) projectMarkerScan {
	scan := projectMarkerScan{dirs: make(map[string]map[string]struct{}, len(markers))}
	if len(markers) == 0 || walk == nil {
		return scan
	}
	entries := 0
	_ = walk(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entries++
		if entries > maxProjectRootEntries {
			scan.truncated = true
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			if skipProjectRootDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		for marker := range markers {
			if !markerMatches(marker, name) {
				continue
			}
			dirs := scan.dirs[marker]
			if dirs == nil {
				dirs = make(map[string]struct{})
				scan.dirs[marker] = dirs
			}
			dirs[filepath.Dir(path)] = struct{}{}
		}
		return nil
	})
	return scan
}

// rootFor returns the single directory carrying any of markers, or root when
// none or several do. Ambiguity is never guessed.
func (s projectMarkerScan) rootFor(root string, markers []string) string {
	if s.truncated {
		return root
	}
	found := make(map[string]struct{})
	for _, marker := range markers {
		for dir := range s.dirs[marker] {
			found[dir] = struct{}{}
		}
	}
	if len(found) != 1 {
		return root
	}
	for dir := range found {
		return dir
	}
	return root
}

// markerMatches reports whether a file name satisfies a marker. A marker of
// the form "*.ext" matches any name with that suffix; every other marker is an
// exact file name.
func markerMatches(marker, name string) bool {
	if strings.HasPrefix(marker, "*.") {
		return strings.HasSuffix(name, marker[1:])
	}
	return marker == name
}

// skipProjectRootDir mirrors the structural index's exclusions. A project
// marker found under target/, node_modules/, or another build/cache directory
// describes generated or vendored output, not the repository's project.
func skipProjectRootDir(name string) bool {
	if name == "testdata" {
		return true
	}
	if ast.BuiltinExcludedDir(name) {
		return true
	}
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
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
	wanted := make(map[string]struct{}, len(markers))
	for _, marker := range markers {
		wanted[marker] = struct{}{}
	}
	return scanProjectMarkers(root, wanted, filepath.WalkDir).rootFor(root, markers)
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
