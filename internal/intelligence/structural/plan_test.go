package structural

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

func planFixture(t *testing.T) ast.Inventory {
	t.Helper()
	root := fixture(t, map[string]string{
		"go.mod":         "module example.com/app\n\ngo 1.21\n",
		"api/api.go":     "package api\n\nimport \"example.com/app/service\"\n\nfunc Handle() { service.Serve() }\n",
		"service/svc.go": "package service\n\nfunc Serve() {}\n",
		"store/store.go": "package store\n\nfunc Get() {}\n",
		"web/app.ts":     "export function app() {}\n",
		"README.md":      "# app\n",
	})
	return loadInventory(t, newIndex(t, root), false)
}

func selectedNames(plan Plan) map[string]bool {
	names := map[string]bool{}
	for _, step := range plan.Selected {
		names[step.Strategy] = true
	}
	return names
}

func skippedReason(plan Plan, strategy string) string {
	for _, skip := range plan.Skipped {
		if skip.Strategy == strategy {
			return skip.Reason
		}
	}
	return ""
}

// TestProjectPlanStaysMetadataFirst pins the cost contract for orientation: a
// project overview must not parse the repository just to describe it.
func TestProjectPlanStaysMetadataFirst(t *testing.T) {
	inventory := planFixture(t)
	plan, selected := buildPlan((Request{Intent: IntentProject}).normalized(), inventory)
	names := selectedNames(plan)
	if !names["filesystem-hierarchy"] || !names["manifest-topology"] {
		t.Fatalf("project plan omitted mandatory strategies: %+v", plan.Selected)
	}
	if names["dependency-graph"] {
		t.Fatal("project plan selected the dependency graph for a metadata question")
	}
	for _, entry := range selected {
		if entry.needsGraph {
			t.Fatalf("project plan selected graph strategy %s", entry.name)
		}
	}
	if plan.StopReason != "estimated_confidence_reached" {
		t.Errorf("stop reason = %q", plan.StopReason)
	}
}

// TestArchitecturePlanSelectsGraphEvidence pins intent awareness: the same
// repository produces a graph plan only when the question is about shape.
func TestArchitecturePlanSelectsGraphEvidence(t *testing.T) {
	plan, _ := buildPlan((Request{Intent: IntentArchitecture}).normalized(), planFixture(t))
	names := selectedNames(plan)
	for _, want := range []string{
		"dependency-graph", "scc-cycles", "boundary-analysis",
		"architecture-inference", ModularityStrategyName,
	} {
		if !names[want] {
			t.Errorf("architecture plan omitted %s: %+v", want, plan.Selected)
		}
	}
	if plan.EstimatedConfidence <= (Request{Intent: IntentArchitecture}).normalized().MinConfidence {
		t.Errorf("estimated confidence %.2f did not meet the threshold", plan.EstimatedConfidence)
	}
	for _, step := range plan.Selected {
		if step.Reason == "" {
			t.Errorf("step %s has no reason", step.Strategy)
		}
	}
	if plan.Reason == "" {
		t.Error("plan has no reason")
	}
}

// TestPlanRespectsLevelBudget pins that a caller's resolution cap removes
// strategies it cannot afford, including dependants of the skipped provider.
func TestPlanRespectsLevelBudget(t *testing.T) {
	plan, selected := buildPlan((Request{Intent: IntentArchitecture, Level: LevelModule}).normalized(), planFixture(t))
	if reason := skippedReason(plan, "dependency-graph"); reason == "" {
		t.Fatalf("dependency-graph was not skipped: %+v", plan.Selected)
	}
	for _, entry := range selected {
		if entry.needsGraph {
			t.Errorf("selected graph strategy %s under a module level budget", entry.name)
		}
	}
}

// TestPlanRespectsStepBudget pins that optional analysis stops at the step cap
// and says why, instead of silently dropping candidates.
func TestPlanRespectsStepBudget(t *testing.T) {
	request := Request{Intent: IntentProject, MinConfidence: 0.99, MaxSteps: 3}.normalized()
	plan, selected := buildPlan(request, planFixture(t))
	if len(selected) > request.MaxSteps {
		t.Fatalf("selected %d strategies, cap %d", len(selected), request.MaxSteps)
	}
	budgetSkip := false
	for _, skip := range plan.Skipped {
		if skip.Reason == "step budget of 3 exhausted" {
			budgetSkip = true
		}
	}
	if !budgetSkip {
		t.Fatalf("no step-budget skip recorded: %+v", plan.Skipped)
	}
}

// TestModularityStrategyIsBoundedByGraphSize pins that clustering is a bounded strategy, not
// an unconditional stage: a large graph is refused with the bound it exceeded.
func TestModularityStrategyIsBoundedByGraphSize(t *testing.T) {
	root := fixture(t, map[string]string{"go.mod": "module example.com/app\n\ngo 1.21\n"})
	base := loadInventory(t, newIndex(t, root), false)
	// A synthetic package list larger than the bound, without touching disk.
	base.Packages = make([]ast.PackageInfo, 0, 1000)
	for i := range 1000 {
		base.Packages = append(base.Packages, ast.PackageInfo{
			Dir: fmt.Sprintf("module%02d/pkg%02d", i/10, i%10), Name: "pkg",
		})
	}
	plan, selected := buildPlan((Request{Intent: IntentArchitecture}).normalized(), base)
	for _, entry := range selected {
		if entry.name == ModularityStrategyName {
			t.Fatal("modularity clustering was selected above its graph bound")
		}
	}
	reason := skippedReason(plan, ModularityStrategyName)
	if !strings.Contains(reason, "800-node") {
		t.Fatalf("skip reason = %q", reason)
	}
}

// TestPlanIsDeterministic pins stable selection and reasons, which is what makes
// the plan auditable across runs.
func TestPlanIsDeterministic(t *testing.T) {
	inventory := planFixture(t)
	request := (Request{Intent: IntentArchitecture}).normalized()
	first, _ := buildPlan(request, inventory)
	second, _ := buildPlan(request, inventory)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("plan is not deterministic")
	}
}
