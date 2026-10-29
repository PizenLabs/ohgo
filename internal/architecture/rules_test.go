package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/PizenLabs/ohgo"

// domainOf is OHGO's architectural manifest: which domain owns which package.
//
// Every package under internal/ must appear here exactly once. A new package
// that is not declared fails TestManifestDeclaresEveryPackage, which is the
// enforcement point for "decide ownership before you add the package".
var domainOf = map[string]string{
	// Foundation: vocabulary, state ownership, and repo intelligence. These
	// have no module dependencies at all.
	"internal/control/contract":    "control",
	"internal/control/budget":      "control",
	"internal/security":            "control",
	"internal/jsonschema":          "vocabulary",
	"internal/tokens":              "vocabulary",
	"internal/diff":                "vocabulary",
	"internal/edit":                "vocabulary",
	"internal/vcs":                 "vcs",
	"internal/state":               "configuration",
	"internal/config":              "configuration",
	"internal/project":             "configuration",
	"internal/intelligence/ast":    "intelligence",
	"internal/intelligence/ignore": "intelligence",
	"internal/external/knowledge":  "external",
	"internal/external/search":     "external",
	"internal/execution/sandbox":   "execution",
	"internal/execution/shell":     "execution",

	// Provider plane: wire protocols, model facts, credentials.
	"internal/provider/ai":      "provider",
	"internal/provider/catalog": "provider",
	"internal/provider/creds":   "provider",

	// Runtime kernel.
	"internal/agent": "runtime",

	// Control plane: intent, authority, shape, grounding.
	"internal/control/workspace": "control",
	"internal/control/cognition": "control",

	// Execution plane: capabilities.
	"internal/execution/tools": "execution",
	"internal/execution/mcp":   "execution",
	"internal/execution/exec":  "execution",

	// Continuity: durable state and its trust boundaries.
	"internal/continuity/session":  "continuity",
	"internal/continuity/task":     "continuity",
	"internal/continuity/recovery": "continuity",
	"internal/memory":              "continuity",

	// Application services and presentation.
	"internal/prompt":       "prompt",
	"internal/commit":       "application",
	"internal/startup":      "application",
	"internal/update":       "application",
	"internal/history":      "application",
	"internal/gryhook":      "application",
	"internal/tui":          "presentation",
	"internal/tui/diff":     "presentation",
	"internal/architecture": "governance",
}

// agentAllowedImports is the complete module-level import allow-list for the
// runtime kernel. The engine may see vocabulary, the authorization kernel, and
// the provider seam — nothing else. Widening this list is an architecture
// decision, not a refactor.
var agentAllowedImports = []string{
	"internal/control/budget",
	"internal/control/contract",
	"internal/jsonschema",
	"internal/provider/ai",
	"internal/security",
	"internal/tokens",
}

// dependencyFree lists packages that must import no other module package. Each
// is a leaf whose value depends on knowing nothing about its consumers.
var dependencyFree = []string{
	"internal/control/budget",
	"internal/control/contract",
	"internal/diff",
	"internal/edit",
	"internal/execution/sandbox",
	"internal/execution/shell",
	"internal/external/knowledge",
	"internal/intelligence/ast",
	"internal/intelligence/ignore",
	"internal/jsonschema",
	"internal/memory",
	"internal/security",
	"internal/state",
	"internal/tokens",
	"internal/vcs",
}

// shellSink is the application/presentation edge. It may import the rest of the
// tree; nothing under internal/ except the TUI and the command may import it
// back, so a shell can never become a dependency of a lower layer.
var shellSink = []string{
	"internal/commit",
	"internal/gryhook",
	"internal/history",
	"internal/startup",
	"internal/tui",
	"internal/update",
}

func TestEveryImplementationPackageIsInternal(t *testing.T) {
	for dir := range loadPackages(t) {
		if strings.HasPrefix(dir, "internal/") || dir == "internal" {
			continue
		}
		switch {
		case strings.HasPrefix(dir, "cmd/"), strings.HasPrefix(dir, "bench/"), strings.HasPrefix(dir, "scripts/"):
			continue
		}
		t.Errorf("%s is a production package outside internal/: OHGO's Go API is not public, so an implementation package at the module root is an accidental compatibility promise (see OHGO_ARCHITECTURE_RULES.md §2)", dir)
	}
}

func TestManifestDeclaresEveryPackage(t *testing.T) {
	pkgs := loadPackages(t)
	for dir := range pkgs {
		if !strings.HasPrefix(dir, "internal/") {
			continue
		}
		if _, ok := domainOf[dir]; !ok {
			t.Errorf("%s has no declared domain: add it to domainOf in internal/architecture/rules_test.go after deciding who owns it (OHGO_ARCHITECTURE_RULES.md §1)", dir)
		}
	}
	for dir := range domainOf {
		if _, ok := pkgs[dir]; !ok {
			t.Errorf("manifest declares %s, which is not a package: remove the stale entry", dir)
		}
	}
}

func TestRuntimeKernelImportsOnlyItsAllowList(t *testing.T) {
	allowed := make(map[string]bool, len(agentAllowedImports))
	for _, p := range agentAllowedImports {
		allowed[p] = true
	}
	for _, imp := range loadPackages(t)["internal/agent"] {
		if !allowed[imp] {
			t.Errorf("internal/agent imports %s, which is outside the kernel allow-list: the engine must not know about capabilities, presentation, persistence, or workspace policy (OHGO_ARCHITECTURE_RULES.md §3)", imp)
		}
	}
}

func TestDependencyFreePackagesImportNothingFromTheModule(t *testing.T) {
	pkgs := loadPackages(t)
	for _, dir := range dependencyFree {
		for _, imp := range pkgs[dir] {
			t.Errorf("%s imports %s, but it is declared dependency-free: a leaf that depends on another module package is not a leaf, so the declaration is now wrong (OHGO_ARCHITECTURE_RULES.md §4)", dir, imp)
		}
	}
}

func TestControlPlaneDoesNotReachIntoLowerPlanes(t *testing.T) {
	forbidInPlane(t, "internal/control/", "control",
		"internal/execution/", "internal/continuity/", "internal/commit",
		"internal/startup", "internal/update", "internal/history",
		"internal/gryhook", "internal/tui",
	)
}

func TestExecutionPlaneDoesNotDependOnContinuityOrPresentation(t *testing.T) {
	forbidInPlane(t, "internal/execution/", "execution",
		"internal/continuity/", "internal/tui", "internal/commit",
		"internal/startup", "internal/update", "internal/history",
	)
}

func TestContinuityDoesNotDependOnPresentation(t *testing.T) {
	forbidInPlane(t, "internal/continuity/", "continuity",
		"internal/tui", "internal/commit",
		"internal/startup", "internal/update", "internal/history",
	)
}

func TestRepositoryIntelligenceDoesNotDependOnTheRuntime(t *testing.T) {
	forbidInPlane(t, "internal/intelligence/", "repository intelligence",
		"internal/agent", "internal/control/", "internal/execution/",
		"internal/continuity/", "internal/provider/", "internal/tui",
	)
}

func TestProviderPlaneStaysBelowTheRuntime(t *testing.T) {
	for dir, imports := range loadPackages(t) {
		if !strings.HasPrefix(dir, "internal/provider/") {
			continue
		}
		for _, imp := range imports {
			if strings.HasPrefix(imp, "internal/provider/") || imp == "internal/state" {
				continue
			}
			t.Errorf("%s imports %s: the provider plane may depend only on itself and internal/state, so model facts and transports never learn about the agent (OHGO_ARCHITECTURE_RULES.md §6)", dir, imp)
		}
	}
}

func TestShellLayerIsASink(t *testing.T) {
	for dir, imports := range loadPackages(t) {
		if !strings.HasPrefix(dir, "internal/") || dir == "internal/tui" {
			continue
		}
		for _, imp := range imports {
			for _, sink := range shellSink {
				if imp == sink {
					t.Errorf("%s imports %s, but the application/presentation layer is a sink: nothing below the shell may depend on it (OHGO_ARCHITECTURE_RULES.md §7)", dir, imp)
				}
			}
		}
	}
}

func TestPresentationDoesNotConstructCapabilitiesOrTaskState(t *testing.T) {
	forbidden := []string{
		"internal/execution/tools",
		"internal/execution/mcp",
		"internal/continuity/task",
	}
	for _, imp := range loadPackages(t)["internal/tui"] {
		for _, bad := range forbidden {
			if imp == bad {
				t.Errorf("internal/tui imports %s: the TUI drives the loop through hooks and projects task state, it must not construct capabilities or reach the task controller (OHGO_ARCHITECTURE_RULES.md §8)", imp)
			}
		}
	}
}

func TestNothingImportsTheCommand(t *testing.T) {
	for dir, imports := range loadPackages(t) {
		for _, imp := range imports {
			if imp == "cmd/ohgo" {
				t.Errorf("%s imports the command package: cmd/ohgo is package main and must stay a leaf (OHGO_ARCHITECTURE_RULES.md §9)", dir)
			}
		}
	}
}

// forbidInPlane asserts that no package whose directory is under prefix imports
// a package whose module-relative path has any of the forbidden prefixes.
func forbidInPlane(t *testing.T, prefix, plane string, forbidden ...string) {
	t.Helper()
	for dir, imports := range loadPackages(t) {
		if !strings.HasPrefix(dir, prefix) {
			continue
		}
		for _, imp := range imports {
			for _, bad := range forbidden {
				if hasPrefix(imp, bad) {
					t.Errorf("%s imports %s: the %s plane must not depend on %s (OHGO_ARCHITECTURE_RULES.md §5)", dir, imp, plane, bad)
				}
			}
		}
	}
}

// hasPrefix reports whether value is at or below prefix in the package tree.
// A trailing slash on prefix is ignored so callers can write either
// "internal/execution" or "internal/execution/".
func hasPrefix(value string, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}

// loadPackages parses every production (non-test) Go file in the module and
// returns each package directory mapped to the module-relative imports it
// declares. Test files are excluded: these rules constrain shipped code, and a
// test may legitimately use a package its subject must not.
func loadPackages(t *testing.T) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	pkgs := map[string][]string{}
	seen := map[string]map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "dist" || name == "img-bugs") {
			return fs.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(rel)
		if dir == "." {
			dir = ""
		}
		for _, entry := range entries {
			file := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(path, file), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", filepath.Join(dir, file), err)
			}
			if seen[dir] == nil {
				seen[dir] = map[string]bool{}
				pkgs[dir] = nil
			}
			for _, spec := range parsed.Imports {
				raw, err := strconv.Unquote(spec.Path.Value)
				if err != nil || !strings.HasPrefix(raw, modulePath+"/") {
					continue
				}
				imp := strings.TrimPrefix(raw, modulePath+"/")
				if !seen[dir][imp] {
					seen[dir][imp] = true
					pkgs[dir] = append(pkgs[dir], imp)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	for dir := range pkgs {
		sort.Strings(pkgs[dir])
	}
	return pkgs
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the module root: %v", root, err)
	}
	return root
}
