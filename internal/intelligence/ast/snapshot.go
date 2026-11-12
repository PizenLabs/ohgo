package ast

import (
	"os"
	"path"
	"sort"
	"strings"
)

// fileKindForLanguage classifies one path using the extension registry. It is
// the single owner of source/test/config/manifest classification.
func fileKindForLanguage(rel string, language Language, isSource bool) FileKind {
	if isSource {
		test := IsTestPath(rel)
		if language == LanguageGo {
			if test {
				return FileGoTest
			}
			return FileGoSource
		}
		if test {
			return FileTest
		}
		return FileSource
	}
	base := path.Base(rel)
	switch base {
	case "go.mod", "go.work", "go.sum", "package.json", "tsconfig.json",
		"jsconfig.json", "pom.xml", "build.gradle", "build.gradle.kts",
		"settings.gradle", "settings.gradle.kts", "Cargo.toml", "Cargo.lock",
		"Gemfile", "Gemfile.lock", "Rakefile", "CMakeLists.txt",
		"Makefile", "makefile", "GNUmakefile", "Dockerfile", "Containerfile":
		return FileManifest
	case ".golangci.yml", ".golangci.yaml", ".goreleaser.yml", ".goreleaser.yaml",
		"Taskfile.yml", "Taskfile.yaml", "next.config.js", "next.config.mjs",
		"next.config.ts", "vite.config.js", "vite.config.ts",
		"webpack.config.js", "tsconfig.base.json", "Directory.Build.props",
		"Directory.Build.targets":
		return FileConfig
	}
	if strings.HasPrefix(base, "README") || strings.HasPrefix(base, "CONTRIBUTING") {
		return FileDocumentation
	}
	if strings.HasSuffix(base, ".csproj") || strings.HasSuffix(base, ".fsproj") ||
		strings.HasSuffix(base, ".sln") || strings.HasSuffix(base, ".props") ||
		strings.HasSuffix(base, ".targets") {
		return FileManifest
	}
	switch strings.ToLower(path.Ext(rel)) {
	case ".md", ".markdown", ".rst":
		return FileDocumentation
	case ".yml", ".yaml":
		if strings.HasPrefix(rel, ".github/workflows/") ||
			strings.Contains(rel, "/.github/workflows/") {
			return FileConfig
		}
	}
	if strings.HasPrefix(rel, ".github/workflows/") {
		switch strings.ToLower(path.Ext(rel)) {
		case ".yml", ".yaml":
			return FileConfig
		}
	}
	return FileOther
}

// configFacts projects the allowlisted configuration records into inventory
// facts. The module path comes from the same manifest read the graph resolved
// against; a second parser would be a second answer.
func configFacts(files map[string]fileRecord, module string) []ConfigInfo {
	out := make([]ConfigInfo, 0)
	for rel, rec := range files {
		if rec.kind != FileConfig && rec.kind != FileManifest {
			continue
		}
		fact := ConfigInfo{
			Path: rel, Kind: configKind(rel), Language: configLanguage(rel),
			Framework: rec.framework,
		}
		if rel == "go.mod" {
			fact.Value = module
		}
		out = append(out, fact)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// configKind names the configuration role without parsing its contents.
func configKind(rel string) string {
	base := path.Base(rel)
	switch {
	case base == "go.mod":
		return "module"
	case base == "go.work":
		return "workspace"
	case base == "go.sum":
		return "module_checksums"
	case base == "package.json":
		return "node_manifest"
	case base == "tsconfig.json" || base == "jsconfig.json" || base == "tsconfig.base.json":
		return "typescript_config"
	case base == "pom.xml":
		return "maven_manifest"
	case strings.Contains(base, "gradle"):
		return "gradle_manifest"
	case base == "Cargo.toml":
		return "cargo_manifest"
	case base == "Cargo.lock":
		return "cargo_lock"
	case base == "Gemfile" || base == "Gemfile.lock":
		return "bundler_manifest"
	case strings.HasSuffix(base, ".csproj") || strings.HasSuffix(base, ".sln"):
		return "dotnet_manifest"
	case base == "CMakeLists.txt":
		return "cmake"
	case base == "Makefile" || base == "makefile" || base == "GNUmakefile":
		return "make"
	case base == "Dockerfile" || base == "Containerfile":
		return "container"
	case strings.Contains(rel, ".github/workflows/"):
		return "ci"
	default:
		return "config"
	}
}

// configLanguage maps a manifest to the implementation language it identifies.
func configLanguage(rel string) Language {
	switch path.Base(rel) {
	case "go.mod", "go.work", "go.sum":
		return LanguageGo
	case "package.json", "tsconfig.json", "jsconfig.json", "next.config.js",
		"next.config.mjs", "next.config.ts", "vite.config.js", "vite.config.ts":
		return LanguageTypeScript
	case "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts":
		return LanguageJava
	case "Cargo.toml", "Cargo.lock":
		return LanguageRust
	case "Gemfile", "Gemfile.lock", "Rakefile":
		return LanguageRuby
	case "CMakeLists.txt":
		return LanguageCpp
	default:
		if strings.HasSuffix(rel, ".csproj") || strings.HasSuffix(rel, ".sln") {
			return LanguageCSharp
		}
		return ""
	}
}

// conventionalEntrypoints names route files whose location is the entrypoint
// witness. It is metadata-only: no symbol is invented, and the entrypoint says
// it comes from a convention rather than a parsed declaration.
func conventionalEntrypoints(files map[string]fileRecord) []Entrypoint {
	out := make([]Entrypoint, 0)
	for rel := range files {
		language, ok := LanguageForPath(rel)
		if !ok || (language != LanguageTSX && language != LanguageTypeScript &&
			language != LanguageJavaScript && language != LanguageJSX) {
			continue
		}
		normalized := strings.ReplaceAll(rel, "\\", "/")
		base := path.Base(normalized)
		conventional := false
		for _, prefix := range []string{"app/", "src/app/", "pages/", "src/pages/"} {
			if strings.Contains("/"+normalized, "/"+prefix) {
				conventional = true
				break
			}
		}
		if !conventional {
			continue
		}
		switch base {
		case "page.tsx", "page.ts", "page.jsx", "page.js", "layout.tsx", "layout.ts",
			"layout.jsx", "layout.js", "index.tsx", "index.ts", "index.jsx", "index.js":
		default:
			continue
		}
		out = append(out, Entrypoint{
			Path: rel, Symbol: base, QualifiedName: rel, Language: language,
			NativeKind: "route_convention", Line: 1, EndLine: 1, Column: 1,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// detectFramework is a bounded manifest sniff. It reads at most 64 KiB and
// only names frameworks whose dependency or build marker is present; absence
// stays absence rather than a guessed stack.
func detectFramework(pathArg string) string {
	data, err := os.ReadFile(pathArg)
	if err != nil || len(data) > 64*1024 {
		return ""
	}
	text := strings.ToLower(string(data))
	base := strings.ToLower(path.Base(pathArg))
	switch base {
	case "package.json":
		for _, marker := range []struct{ token, name string }{
			{"\"next\"", "Next.js"}, {"\"react\"", "React"},
			{"\"vue\"", "Vue"}, {"\"svelte\"", "Svelte"},
			{"\"@nestjs/core\"", "NestJS"}, {"\"express\"", "Express"},
			{"\"vite\"", "Vite"},
		} {
			if strings.Contains(text, marker.token) {
				return marker.name
			}
		}
	case "pom.xml", "build.gradle", "build.gradle.kts":
		switch {
		case strings.Contains(text, "spring-boot"):
			return "Spring Boot"
		case strings.Contains(text, "quarkus"):
			return "Quarkus"
		case strings.Contains(text, "micronaut"):
			return "Micronaut"
		}
	case "cargo.toml":
		switch {
		case strings.Contains(text, "axum"):
			return "Axum"
		case strings.Contains(text, "actix-web"):
			return "Actix Web"
		}
	case "gemfile":
		if strings.Contains(text, "rails") {
			return "Rails"
		}
	case "cmakelists.txt":
		if strings.Contains(text, "find_package(qt") {
			return "Qt"
		}
	}
	if strings.HasSuffix(base, ".csproj") && strings.Contains(text, "microsoft.aspnetcore") {
		return "ASP.NET Core"
	}
	return ""
}

// frameworkFacts derives the observed framework names from manifest/config
// inventory. It never claims a framework from a package name alone.
func frameworkFacts(configs []ConfigInfo) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, config := range configs {
		if config.Framework == "" || seen[config.Framework] {
			continue
		}
		seen[config.Framework] = true
		out = append(out, config.Framework)
	}
	sort.Strings(out)
	return out
}
