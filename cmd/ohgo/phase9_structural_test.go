package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func phase9Write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func phase9Tool(t *testing.T, set []agent.Tool, name string) agent.Tool {
	t.Helper()
	for _, candidate := range set {
		if candidate.Name() == name {
			return candidate
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}

// astSupportsTypeScript reports whether the current build includes the
// tree-sitter TypeScript adapter. It is a build capability, not a model or
// project policy.
func astSupportsTypeScript() bool {
	for _, adapter := range ast.DefaultAdapters() {
		for _, language := range adapter.Languages() {
			if language == ast.LanguageTypeScript {
				return true
			}
		}
	}
	return false
}

// TestNextJSAnalysisUsesOverviewWithoutFullParsing is the real-project
// regression baseline for the fallback/non-Go path. It proves the runtime can
// answer a broad project question with metadata first, then escalate only when
// the task asks for declarations and relationships.
func TestNextJSAnalysisUsesOverviewWithoutFullParsing(t *testing.T) {
	if !astSupportsTypeScript() {
		// The multi-language adapters are cgo-only by design. The no-cgo path
		// is covered by internal/intelligence/ast's disclosure test; this
		// regression is about parsing TypeScript, which the portable build
		// intentionally cannot do.
		t.Skip("tree-sitter TypeScript adapter requires cgo")
	}
	root := t.TempDir()
	phase9Write(t, root, map[string]string{
		"package.json":    `{"name":"next-blog","dependencies":{"next":"15.0.0","react":"19.0.0"}}`,
		"tsconfig.json":   `{"compilerOptions":{"strict":true}}`,
		"next.config.mjs": `export default {};`,
		"app/page.tsx": `import { getAllPosts } from "../lib/content";
export default function Page() {
  return <main>{getAllPosts()}</main>;
}
`,
		"lib/content.ts": `export interface ContentSource { load(slug: string): string }
export class FileContentSource implements ContentSource {
  load(slug: string): string { return parseFrontmatter(slug); }
}
export function getAllPosts(): string { return parseFrontmatter("posts"); }
export function parseFrontmatter(value: string): string { return value; }
`,
		"__tests__/content.test.ts": `import { getAllPosts } from "../lib/content";
test("lists posts", () => { getAllPosts(); });
`,
	})

	facts := cognition.ProbeFacts(root)
	if !facts.HasLanguage("typescript") {
		t.Fatalf("probe did not detect TypeScript: %+v", facts)
	}
	shape := cognition.BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, cognition.ShapeParams{Authority: "ask", Intent: "read", Facts: facts})
	if shape.StructuralDepth != cognition.StructuralOverview {
		t.Fatalf("structural depth = %s, want OVERVIEW", shape.StructuralDepth)
	}

	toolSet := buildTools(testOptions(t, root, 1), ai.NewRegistry())
	evidence := cognition.NewEvidenceState()
	candidates := cognition.Candidates(shape, evidence, cognition.ExecutionState{}, toolSet, facts,
		cognition.EvaluateSufficiency(shape, evidence, cognition.ExecutionState{}, false))
	offered := map[string]bool{}
	for _, tool := range candidates.Tools {
		offered[tool.Name()] = true
	}
	if !offered["structure"] {
		t.Fatalf("ASK overview did not offer structure: excluded=%+v notices=%+v",
			candidates.Excluded, candidates.Notices)
	}

	structure := phase9Tool(t, toolSet, "structure")
	symbols := phase9Tool(t, toolSet, "symbols")
	refs := phase9Tool(t, toolSet, "refs")

	overview := structure.Execute(agent.Context{Ctx: context.Background()}, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	for _, want := range []string{"TypeScript", "Next.js", "parsed_files: 0"} {
		if !strings.Contains(overview.Text, want) {
			t.Fatalf("overview omitted %q:\n%s", want, overview.Text)
		}
	}
	if !strings.Contains(overview.Text, "app/page.tsx") {
		t.Fatalf("overview omitted the conventional route file:\n%s", overview.Text)
	}

	// The broad question must not have parsed every source file.
	source := structure.(*tools.StructureTool).Source()
	idx, err := source.Index()
	if err != nil {
		t.Fatal(err)
	}
	if info := idx.Info(); info.ParsedFiles != 0 || info.PendingFiles == 0 {
		t.Fatalf("overview parsed source: parsed=%d pending=%d", info.ParsedFiles, info.PendingFiles)
	}

	entrypoints := structure.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"view": "entrypoints",
	})
	if entrypoints.IsError {
		t.Fatalf("entrypoints: %s", entrypoints.Text)
	}
	if !strings.Contains(entrypoints.Text, "app/page.tsx") {
		t.Fatalf("entrypoint view omitted app/page.tsx:\n%s", entrypoints.Text)
	}
	if info := idx.Info(); info.ParsedFiles == 0 {
		t.Fatal("targeted entrypoint view did not parse source")
	}

	found := symbols.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"symbol": "getAllPosts", "language": "typescript",
	})
	if found.IsError {
		t.Fatalf("symbols: %s", found.Text)
	}
	if !strings.Contains(found.Text, "lib/content.ts") || !strings.Contains(found.Text, "typescript") {
		t.Fatalf("symbol lookup omitted the TypeScript declaration:\n%s", found.Text)
	}

	references := refs.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"symbol": "getAllPosts", "relation": "references", "limit": float64(20),
	})
	if references.IsError {
		t.Fatalf("refs: %s", references.Text)
	}
	if !strings.Contains(references.Text, "app/page.tsx") {
		t.Fatalf("references omitted the route call site:\n%s", references.Text)
	}

	tests := structure.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"view": "tests",
	})
	if tests.IsError || !strings.Contains(tests.Text, "lists posts") {
		t.Fatalf("tests view omitted the TypeScript test declaration:\n%s", tests.Text)
	}

	relations := structure.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"view": "relations", "symbol": "FileContentSource", "limit": float64(20),
	})
	if relations.IsError {
		t.Fatalf("relations: %s", relations.Text)
	}
	if !strings.Contains(relations.Text, "IMPLEMENTS") {
		t.Fatalf("relations omitted the explicit implementation edge:\n%s", relations.Text)
	}
}

// TestGoProjectAnalysisPreservesNativeCoverage is the Go regression baseline:
// the native parser, tests, entrypoints, and evidence path still work while
// the same snapshot also serves non-Go adapters.
func TestGoProjectAnalysisPreservesNativeCoverage(t *testing.T) {
	root := t.TempDir()
	phase9Write(t, root, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.21\n",
		"cmd/app/main.go": `package main

import "example.com/app/service"

func main() { service.Run() }
`,
		"service/service.go": `package service

func Run() { helper() }
func helper() {}
`,
		"service/service_test.go": `package service

import "testing"

func TestRun(t *testing.T) { Run() }
`,
	})
	toolSet := buildTools(testOptions(t, root, 1), ai.NewRegistry())
	structure := phase9Tool(t, toolSet, "structure")
	symbols := phase9Tool(t, toolSet, "symbols")
	refs := phase9Tool(t, toolSet, "refs")
	ctx := agent.Context{Ctx: context.Background()}

	overview := structure.Execute(ctx, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	if !strings.Contains(overview.Text, "Go=UNPARSED") || !strings.Contains(overview.Text, "parsed_files: 0") {
		t.Fatalf("Go overview was not metadata-first:\n%s", overview.Text)
	}

	entrypoints := structure.Execute(ctx, map[string]any{"view": "entrypoints"})
	if entrypoints.IsError || !strings.Contains(entrypoints.Text, "main.main") {
		t.Fatalf("Go entrypoint = %s", entrypoints.Text)
	}
	tests := structure.Execute(ctx, map[string]any{"view": "tests"})
	if tests.IsError || !strings.Contains(tests.Text, "TestRun") {
		t.Fatalf("Go tests = %s", tests.Text)
	}
	if !strings.Contains(tests.Text, "not coverage") {
		t.Fatalf("Go test result upgraded association to coverage:\n%s", tests.Text)
	}

	found := symbols.Execute(ctx, map[string]any{
		"symbol": "Run", "path": "service/service.go",
	})
	if found.IsError || !strings.Contains(found.Text, "service/service.go") {
		t.Fatalf("Go symbols = %s", found.Text)
	}
	references := refs.Execute(ctx, map[string]any{
		"symbol": "Run", "relation": "references", "limit": float64(20),
	})
	if references.IsError || !strings.Contains(references.Text, "main.go") {
		t.Fatalf("Go refs = %s", references.Text)
	}
}

// TestNestedRepositoryProjectAnalysis is the gated real-project regression for
// the failure where a root ".gitignore" rule "backend/" hid a nested Go
// repository from structural inventory. It skips unless the caller supplies the
// project root, so the suite never depends on a machine-specific path.
func TestNestedRepositoryProjectAnalysis(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("OHGO_ECOMMERCE_ROOT"))
	if root == "" {
		t.Skip("set OHGO_ECOMMERCE_ROOT to a mixed frontend/nested-Go project")
	}
	source := tools.NewStructuralSource(root)
	idx, err := source.Index()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	info := idx.Info()
	if info.GoFiles < 10 || info.TestFiles < 1 {
		t.Fatalf("nested Go repository disappeared: go=%d tests=%d excluded=%+v",
			info.GoFiles, info.TestFiles, info.ExcludedRoots)
	}
	included := false
	for _, boundary := range info.NestedProjects {
		if boundary.Path == "backend" && boundary.Included {
			included = true
			if boundary.ParentScopeRule == "" {
				t.Errorf("nested backend does not name the parent rule it overrides: %+v", boundary)
			}
		}
	}
	if !included {
		t.Fatalf("nested backend is not an included project boundary: %+v", info.NestedProjects)
	}
	structure := tools.NewStructureTool(source)
	ctx := agent.Context{Ctx: context.Background()}
	overview := structure.Execute(ctx, map[string]any{})
	if overview.IsError {
		t.Fatalf("overview: %s", overview.Text)
	}
	if !strings.Contains(overview.Text, "nested_projects: 1") ||
		!strings.Contains(overview.Text, "- path: backend") {
		t.Fatalf("overview omitted the nested backend:\n%s", overview.Text)
	}
	testsView := structure.Execute(ctx, map[string]any{"view": "tests"})
	if testsView.IsError || !strings.Contains(testsView.Text, "Test") {
		t.Fatalf("tests view did not see the nested Go tests: %s", testsView.Text)
	}

	// The claim layer must agree with the structural result: a metadata
	// overview is not implementation evidence, and a test inventory with
	// excluded roots cannot support a repository-wide absence claim.
	facts := cognition.ProbeFacts(root)
	shape := cognition.BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, cognition.ShapeParams{Authority: "ask", Intent: "read", Facts: facts})
	evidence := cognition.NewEvidenceState()
	evidence.ObserveOutcome(agent.ToolOutcome{
		ToolName: "structure", Args: map[string]any{}, Result: overview,
	})
	summary := evidence.ClaimSummary()
	if summary.HasImplementation {
		t.Fatalf("a metadata overview was counted as implementation: %+v", summary)
	}
	if summary.TestInventory != "NOT_QUERIED" {
		t.Fatalf("an overview set the filtered test inventory to %q", summary.TestInventory)
	}
	if summary.StructuralScope != "PARTIAL" {
		t.Fatalf("structural scope = %q, want PARTIAL with excluded roots", summary.StructuralScope)
	}
	evidence.ObserveOutcome(agent.ToolOutcome{
		ToolName: "structure", Args: map[string]any{"view": "tests"}, Result: testsView,
	})
	summary = evidence.ClaimSummary()
	if summary.TestInventory != "PARTIAL" {
		t.Fatalf("a scope-partial test inventory = %q, want PARTIAL", summary.TestInventory)
	}
	if !summary.TestFilesKnown || summary.TestFiles == 0 {
		t.Fatalf("the nested test files disappeared from the claim inventory: %+v", summary)
	}
	if suff := cognition.EvaluateSufficiency(shape, evidence, cognition.ExecutionState{}, false); suff.State != cognition.Insufficient {
		t.Fatalf("a metadata-only broad analysis = %s/%s, want INSUFFICIENT",
			suff.State, suff.Stage)
	}

	packagesView := structure.Execute(ctx, map[string]any{"view": "packages", "path": "backend"})
	if packagesView.IsError || !strings.Contains(packagesView.Text, "backend/internal/order") {
		t.Fatalf("packages view lost the nested Go packages: %s", packagesView.Text)
	}
	if parsed := idx.Info(); parsed.Entrypoints == 0 {
		t.Fatalf("nested service entrypoints were not discovered: %+v", parsed)
	}
}

// TestSelfAnalysisStructuralRuntime is the gated self-analysis baseline. It
// verifies that the same runtime comprehends OHGO's own architecture without
// changing the native Go path.
func TestSelfAnalysisStructuralRuntime(t *testing.T) {
	if os.Getenv("OHGO_PHASE9") != "1" {
		t.Skip("set OHGO_PHASE9=1 to validate self-analysis over the real repository")
	}
	root, err := filepath.Abs(phase8Root())
	if err != nil {
		t.Fatal(err)
	}
	source := tools.NewStructuralSource(root)
	idx, err := source.Index()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if err := idx.EnsureParsed(context.Background()); err != nil {
		t.Fatalf("EnsureParsed: %v", err)
	}
	info := idx.Info()
	if info.GoFiles == 0 || info.Symbols == 0 || info.Tests == 0 || info.Entrypoints == 0 {
		t.Fatalf("self-analysis snapshot = %+v", info)
	}
	// Unsupported source (this repository contains Python and shell
	// utilities) must remain disclosed rather than silently folded into the Go
	// snapshot.
	for _, language := range info.Languages {
		if language.Language == ast.LanguageGo && language.Coverage != "COMPLETE" {
			t.Fatalf("Go self-analysis coverage = %+v", language)
		}
	}
	impact, err := idx.Impact(context.Background(), ast.ImpactQuery{
		Symbol: "ToolActionClasses", Depth: 1, Limit: 50,
	})
	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	if !impact.Found {
		t.Fatalf("self-analysis did not locate ToolActionClasses: %+v", impact)
	}
}

// view that would exceed the requested depth, and rejects semantic escalation
// explicitly instead of silently downgrading it.
func TestStructureViewHonoursRequestedActivation(t *testing.T) {
	root := t.TempDir()
	phase9Write(t, root, map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	})
	toolSet := buildTools(testOptions(t, root, 1), ai.NewRegistry())
	structure := phase9Tool(t, toolSet, "structure").(*tools.StructureTool)
	ctx := agent.Context{Ctx: context.Background()}

	if result := structure.Execute(ctx, map[string]any{
		"view": "entrypoints", "activation": "overview",
	}); !result.IsError {
		t.Fatalf("entrypoint view accepted overview activation: %s", result.Text)
	}
	if result := structure.Execute(ctx, map[string]any{
		"view": "overview", "activation": "semantic",
	}); !result.IsError || !strings.Contains(result.Text, "semantic") {
		t.Fatalf("semantic activation = %+v", result)
	}
}
