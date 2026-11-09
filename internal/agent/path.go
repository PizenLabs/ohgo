package agent

import (
	"path/filepath"
	"strings"
)

// PathClass is what a repository path can establish.
//
// It is the single path classifier used by answer claim scope and by the
// cognition layer's evidence provenance. Keeping it in the lower layer avoids a
// second set of suffix rules drifting from the evidence rules.
type PathClass string

// Path classes.
const (
	// PathUnknown is a path the classifier does not recognize, such as a
	// directory name with no extension. It is not documentation or a manifest.
	PathUnknown PathClass = "unknown"
	// PathSource is implementation source code.
	PathSource PathClass = "source"
	// PathDocumentation is prose about the project.
	PathDocumentation PathClass = "documentation"
	// PathManifest is dependency or package metadata.
	PathManifest PathClass = "manifest"
	// PathConfiguration is declared runtime or deployment configuration.
	PathConfiguration PathClass = "configuration"
	// PathGenerated is vendored, generated, or minified content.
	PathGenerated PathClass = "generated"
)

// manifestLanguages maps a manifest base name to the implementation language
// it identifies. An empty language marks a manifest for a language the runtime
// does not model (Java, Ruby, PHP, Elixir): it is still manifest evidence, but
// it contributes no language identity.
var manifestLanguages = map[string]string{
	"go.mod": "go", "go.sum": "go", "go.work": "go",
	"package.json": "javascript", "package-lock.json": "javascript",
	"yarn.lock": "javascript", "pnpm-lock.yaml": "javascript",
	"cargo.toml": "rust", "cargo.lock": "rust",
	"pyproject.toml": "python", "requirements.txt": "python",
	"poetry.lock": "python", "pipfile": "python", "pipfile.lock": "python",
	"pom.xml": "", "build.gradle": "", "build.gradle.kts": "",
	"settings.gradle": "", "settings.gradle.kts": "",
	"gemfile": "", "gemfile.lock": "",
	"composer.json": "", "composer.lock": "",
	"mix.exs": "", "mix.lock": "",
}

// ClassifyPath decides what a path observation can establish. The
// classification is deterministic and path-based.
func ClassifyPath(path string) PathClass {
	normalized := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if normalized == "" {
		return PathUnknown
	}
	base := filepath.Base(normalized)
	for _, prefix := range []string{
		"vendor", "node_modules", "third_party", "third-party",
		"dist", "build", "gen", "generated",
	} {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"/") ||
			strings.Contains(normalized, "/"+prefix+"/") ||
			strings.HasSuffix(normalized, "/"+prefix) {
			return PathGenerated
		}
	}
	if strings.Contains(base, ".pb.") || strings.Contains(base, "_generated.") ||
		strings.Contains(base, ".gen.") || strings.HasSuffix(base, ".min.js") ||
		strings.HasSuffix(base, ".min.css") {
		return PathGenerated
	}
	if _, ok := manifestLanguages[base]; ok {
		return PathManifest
	}
	if normalized == "docs" || normalized == "doc" || normalized == "documentation" ||
		strings.HasPrefix(normalized, "docs/") || strings.HasPrefix(normalized, "doc/") ||
		strings.HasPrefix(normalized, "documentation/") ||
		strings.HasSuffix(normalized, "/docs") || strings.HasSuffix(normalized, "/doc") ||
		strings.HasSuffix(normalized, "/documentation") ||
		strings.HasPrefix(base, "readme") || strings.HasPrefix(base, "changelog") ||
		strings.HasPrefix(base, "contributing") || strings.HasPrefix(base, "license") ||
		strings.HasPrefix(base, "licence") || strings.HasPrefix(base, "notice") ||
		strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".markdown") ||
		strings.HasSuffix(base, ".rst") || strings.HasSuffix(base, ".adoc") ||
		strings.HasSuffix(base, ".org") || strings.HasSuffix(base, ".txt") {
		return PathDocumentation
	}
	switch base {
	case "dockerfile", "docker-compose.yml", "docker-compose.yaml",
		"compose.yml", "compose.yaml", "makefile":
		return PathConfiguration
	}
	switch filepath.Ext(base) {
	case ".yml", ".yaml", ".toml", ".ini", ".conf", ".config":
		return PathConfiguration
	}
	for _, prefix := range []string{"k8s", "deploy", "deployment", "helm", "charts", ".github"} {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"/") ||
			strings.Contains(normalized, "/"+prefix+"/") ||
			strings.HasSuffix(normalized, "/"+prefix) {
			return PathConfiguration
		}
	}
	switch filepath.Ext(base) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs",
		".py", ".rs", ".java", ".kt", ".kts", ".rb", ".php", ".cs",
		".c", ".cc", ".cpp", ".h", ".hpp", ".swift", ".scala",
		".sh", ".bash", ".vue", ".svelte":
		return PathSource
	}
	return PathUnknown
}

// ManifestLanguage returns the implementation language a manifest path
// identifies, or "" when the path is not a recognized manifest or the language
// is not modeled.
func ManifestLanguage(path string) string {
	normalized := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if normalized == "" {
		return ""
	}
	return manifestLanguages[filepath.Base(normalized)]
}
