package structural

import (
	"path"
	"strings"
)

// classifyPath assigns a source class to a repository-relative path.
//
// The classification is path-based except where the inventory already proved a
// fact (a test file). It therefore reports INFERRED for everything a convention
// decided. That is deliberate: "this file is production code" is not something
// a filename can prove, and a consumer that needs proof reads the file.
func classifyPath(rel string, test bool) (SourceClass, Evidence) {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "./")
	lower := strings.ToLower(clean)
	base := path.Base(lower)
	segments := strings.Split(lower, "/")

	if test {
		return ClassTest, ObservedEvidence(KindInventory, EvidenceRef{
			Kind: KindInventory, Path: clean, Rule: "test_file",
		})
	}
	if rule, ok := vendorRule(segments); ok {
		return ClassVendor, InferredEvidence(rule, clean)
	}
	if rule, ok := buildRule(segments); ok {
		return ClassBuildArtifact, InferredEvidence(rule, clean)
	}
	if rule, ok := generatedRule(base, segments); ok {
		return ClassGenerated, InferredEvidence(rule, clean)
	}
	if rule, ok := documentationRule(segments, base); ok {
		return ClassDocumentation, InferredEvidence(rule, clean)
	}
	if rule, ok := fixtureRule(segments, base); ok {
		return ClassFixture, InferredEvidence(rule, clean)
	}
	if rule, ok := exampleRule(segments); ok {
		return ClassExample, InferredEvidence(rule, clean)
	}
	if rule, ok := testNameRule(base); ok {
		return ClassTest, InferredEvidence(rule, clean)
	}
	if rule, ok := toolingRule(segments, base); ok {
		return ClassTooling, InferredEvidence(rule, clean)
	}
	return ClassProduction, InferredEvidence("no vendor, generated, test, documentation, or artifact rule matched", clean)
}

func vendorRule(segments []string) (string, bool) {
	for _, segment := range segments {
		switch segment {
		case "vendor", "node_modules", "bower_components", "third_party", "third-party",
			".venv", "site-packages", "godeps", "pods":
			return "vendor directory: " + segment, true
		}
	}
	return "", false
}

func buildRule(segments []string) (string, bool) {
	// Only a root-level or nested build-output directory is classified. A
	// segment such as "build" below a source root is ambiguous, and an
	// INFERRED class is not a license to drop authored code.
	for i, segment := range segments {
		switch segment {
		case "dist", "build", "out", "target", ".next", ".nuxt", ".output", "coverage", "obj":
			if i+1 < len(segments) {
				return "build-output directory: " + segment, true
			}
		}
	}
	return "", false
}

func generatedRule(base string, segments []string) (string, bool) {
	generatedSuffixes := []string{
		".pb.go", ".pb.cc", ".pb.h", "_pb2.py", ".g.dart", ".freezed.dart",
		".designer.cs", ".generated.cs", ".generated.ts", ".generated.js",
		".gen.go", ".min.js", ".min.css", ".js.map", ".css.map",
	}
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(base, suffix) {
			return "generated filename suffix: " + suffix, true
		}
	}
	if strings.HasPrefix(base, "zz_generated") || strings.HasPrefix(base, "generated_") ||
		strings.Contains(base, ".generated.") {
		return "generated filename prefix", true
	}
	for _, segment := range segments {
		if segment == "generated" || segment == "gen" {
			return "generated directory: " + segment, true
		}
	}
	return "", false
}

func documentationRule(segments []string, base string) (string, bool) {
	for _, segment := range segments {
		switch segment {
		case "docs", "doc", "documentation":
			return "documentation directory: " + segment, true
		}
	}
	switch path.Ext(base) {
	case ".md", ".rst", ".adoc", ".txt":
		return "documentation file extension", true
	}
	return "", false
}

func fixtureRule(segments []string, base string) (string, bool) {
	for _, segment := range segments {
		switch segment {
		case "testdata", "fixtures", "__fixtures__", "__snapshots__", "snapshots":
			return "fixture directory: " + segment, true
		}
	}
	if strings.HasSuffix(base, "_fixture.go") || strings.HasSuffix(base, ".fixture.ts") {
		return "fixture filename suffix", true
	}
	return "", false
}

func exampleRule(segments []string) (string, bool) {
	for _, segment := range segments {
		switch segment {
		case "examples", "example", "samples", "sample", "demo", "demos":
			return "example directory: " + segment, true
		}
	}
	return "", false
}

func testNameRule(base string) (string, bool) {
	suffixes := []string{
		"_test.go", "_test.py", ".test.ts", ".test.tsx", ".test.js", ".test.jsx",
		".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx", "_test.rs", "_spec.rb",
		"test.cs", "tests.cs",
	}
	for _, suffix := range suffixes {
		if strings.HasSuffix(base, suffix) {
			return "test filename suffix: " + suffix, true
		}
	}
	if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
		return "test filename prefix", true
	}
	return "", false
}

func toolingRule(segments []string, base string) (string, bool) {
	for _, segment := range segments {
		switch segment {
		case "scripts", "script", "tools", "tooling", "hack", ".github", ".circleci":
			return "tooling directory: " + segment, true
		}
	}
	manifestNames := map[string]bool{
		"go.mod": true, "go.sum": true, "go.work": true, "package.json": true,
		"package-lock.json": true, "pnpm-lock.yaml": true, "yarn.lock": true,
		"tsconfig.json": true, "cargo.toml": true, "cargo.lock": true,
		"pyproject.toml": true, "requirements.txt": true, "pom.xml": true,
		"build.gradle": true, "build.gradle.kts": true, "makefile": true,
		".golangci.yml": true, ".golangci.yaml": true, "dockerfile": true,
		"docker-compose.yml": true, "docker-compose.yaml": true,
		"lerna.json": true, "turbo.json": true, "nx.json": true,
		"pnpm-workspace.yaml": true, ".editorconfig": true,
	}
	if manifestNames[base] {
		return "build manifest or tool configuration", true
	}
	if strings.HasPrefix(base, "dockerfile.") || strings.HasPrefix(base, ".env") {
		return "build manifest or tool configuration", true
	}
	return "", false
}
