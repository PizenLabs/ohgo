package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// TestPhase4UnifiedTrustPipeline exercises the real sequence a deep
// investigation takes: structure → survey → drift → investigation → check,
// then makes a source subtree unreadable and re-runs the same path. The point
// is not any one tool's output; it is that stale state, survey coverage,
// evidence freshness, and verification agree with the same source state.
func TestPhase4UnifiedTrustPipeline(t *testing.T) {
	root := t.TempDir()
	writePipelineFile(t, root, "go.mod", "module example.com/app\n\ngo 1.21\n")
	writePipelineFile(t, root, "README.md",
		"# App\n\nUse `alpha/a.go` and `docs/missing.yaml`.\n")
	writePipelineFile(t, root, "alpha/a.go", "package alpha\n\nfunc A() int { return 1 }\n")
	writePipelineFile(t, root, "alpha/a_test.go",
		"package alpha\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { if A() != 1 { t.Fatal() } }\n")
	writePipelineFile(t, root, "beta/b.go", "package beta\n\nfunc B() int { return 2 }\n")

	source := tools.NewStructuralSource(root)
	structure := tools.NewStructureTool(source)
	ctx := agent.Context{Ctx: context.Background()}

	project := executePipelineTool(t, structure, ctx, map[string]any{"view": "analysis", "intent": "project"})
	projectDetails := project.Details.(map[string]any)
	if pending := projectDetails["pending_files"].(int); pending == 0 {
		t.Fatalf("project analysis parsed source: %+v", projectDetails)
	}
	survey := projectDetails["survey"].(map[string]any)
	if survey["explored"].(int) == 0 {
		t.Fatalf("survey represented no module: %+v", survey)
	}

	drift := executePipelineTool(t, structure, ctx, map[string]any{"view": "analysis", "intent": "drift"})
	driftDetails := drift.Details.(map[string]any)["documentation_drift"].(map[string]any)
	if !pipelineHasFinding(driftDetails, "docs/missing.yaml") {
		t.Fatalf("drift did not report the missing path: %+v", driftDetails)
	}

	check := tools.NewCheckTool(source)
	discovery := executePipelineTool(t, check, ctx, map[string]any{"mode": "discover"})
	if !strings.Contains(discovery.Text, "go test ./...") {
		t.Fatalf("check discovery omitted go test:\n%s", discovery.Text)
	}

	engine := investigate.New(&investigate.StructuralProvider{
		Source: sourceFor(source),
		Root:   root,
	})
	investigation, err := engine.Run(ctx.Ctx, investigate.Request{
		Question: "compare documentation with the source",
		Intent:   planner.IntentDrift,
		Budget:   investigate.Budget{MaxSteps: 2},
	})
	if err != nil {
		t.Fatalf("drift investigation: %v", err)
	}
	if len(investigation.Facts) == 0 {
		t.Fatal("drift investigation produced no facts")
	}
	for _, fact := range investigation.Facts {
		if fact.Freshness != evidence.FreshnessCurrent {
			t.Fatalf("fact %q freshness = %s, want CURRENT for a complete documentation comparison",
				fact.Subject, fact.Freshness)
		}
	}

	// The live check is scoped to alpha and is the only real process the test
	// starts. It proves the check path and completion evidence agree.
	run := executePipelineTool(t, check, ctx, map[string]any{
		"mode": "run", "checks": []any{"test"}, "path": "alpha",
	})
	if run.IsError || !run.Verifies {
		t.Fatalf("scoped check did not verify:\n%s\ndetails=%+v", run.Text, run.Details)
	}
	if run.Verification == nil || len(run.Verification.Scopes) != 1 ||
		run.Verification.Scopes[0].Path != "alpha" {
		t.Fatalf("verification scope = %+v, want alpha", run.Verification)
	}

	// Make beta unreadable. Every later output must describe the same stale
	// source state instead of falling back to the previous parsed snapshot.
	beta := filepath.Join(root, "beta")
	if err := os.Chmod(beta, 0o000); err != nil {
		t.Skipf("cannot make %s unreadable: %v", beta, err)
	}
	t.Cleanup(func() { _ = os.Chmod(beta, 0o755) })

	stale := executePipelineTool(t, structure, ctx, map[string]any{"view": "analysis", "intent": "project"})
	staleDetails := stale.Details.(map[string]any)
	if staleDetails["stale_files"].(int) == 0 {
		t.Fatalf("stale analysis reported no stale files: %+v", staleDetails)
	}
	staleSurvey := staleDetails["survey"].(map[string]any)
	if unexplored := pipelineStrings(staleSurvey["unexplored"]); !pipelineContains(unexplored, "beta") {
		t.Fatalf("stale survey did not name beta as unexplored: %+v", staleSurvey)
	}

	staleInvestigation, err := engine.Run(ctx.Ctx, investigate.Request{
		Question: "what is this project?",
		Intent:   planner.IntentProject,
		Budget:   investigate.Budget{MaxSteps: 2},
	})
	if err != nil {
		t.Fatalf("stale investigation: %v", err)
	}
	if len(staleInvestigation.Facts) == 0 {
		t.Fatal("stale investigation produced no facts")
	}
	for _, fact := range staleInvestigation.Facts {
		if fact.Freshness != evidence.FreshnessStale {
			t.Fatalf("stale fact %q freshness = %s, want STALE", fact.Subject, fact.Freshness)
		}
	}

	// Recovery must clear both the stale count and the survey gap.
	if err := os.Chmod(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	recovered := executePipelineTool(t, structure, ctx, map[string]any{"view": "analysis", "intent": "project"})
	recoveredDetails := recovered.Details.(map[string]any)
	if stale := recoveredDetails["stale_files"].(int); stale != 0 {
		t.Fatalf("recovered analysis still reports %d stale files", stale)
	}
	recoveredSurvey := recoveredDetails["survey"].(map[string]any)
	if unexplored := pipelineStrings(recoveredSurvey["unexplored"]); pipelineContains(unexplored, "beta") {
		t.Fatalf("recovered survey still treats beta as unexplored: %+v", recoveredSurvey)
	}
}

func writePipelineFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func executePipelineTool(t *testing.T, tool agent.Tool, ctx agent.Context, args map[string]any) agent.Result {
	t.Helper()
	result := tool.Execute(ctx, args)
	if result.IsError {
		t.Fatalf("%s failed: %s", tool.Name(), result.Text)
	}
	return result
}

func pipelineHasFinding(details map[string]any, target string) bool {
	findings, _ := details["findings"].([]map[string]any)
	for _, finding := range findings {
		if finding["target"] == target {
			return true
		}
	}
	return false
}

func pipelineStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func pipelineContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
