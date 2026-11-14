package tools

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

func analysisFixture(t *testing.T) *StructureTool {
	t.Helper()
	root := structureFixture(t, map[string]string{
		"api/api.go":      "package api\n\nimport \"example.com/app/service\"\n\nfunc Handle() { service.Serve() }\n",
		"api/api_test.go": "package api\n\nimport \"testing\"\n\nfunc TestHandle(t *testing.T) { Handle() }\n",
		"service/svc.go":  "package service\n\nimport \"example.com/app/store\"\n\nfunc Serve() { store.Get() }\n",
		"store/store.go":  "package store\n\nfunc Get() {}\n",
	})
	return NewStructureTool(NewStructuralSource(root))
}

// TestStructureAnalysisIsIntentAware pins that the capability routes an
// architecture question to the graph-backed plan and reports its evidence.
func TestStructureAnalysisIsIntentAware(t *testing.T) {
	tool := analysisFixture(t)
	result := tool.Execute(testContext(), map[string]any{
		"view": "analysis", "intent": "architecture",
	})
	if result.IsError {
		t.Fatalf("analysis failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "architecture: layered") {
		t.Fatalf("architecture inference missing:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "dependency-graph") {
		t.Fatalf("plan did not select the dependency graph:\n%s", result.Text)
	}
	details := structureDetails(t, result)
	if details["view"] != "analysis" {
		t.Fatalf("view = %v", details["view"])
	}
	coverage, ok := details["coverage"].(map[string]any)
	if !ok || coverage["dependencies_seen"] != true {
		t.Fatalf("coverage = %v", details["coverage"])
	}
	plan, ok := details["plan"].(map[string]any)
	if !ok {
		t.Fatalf("plan = %T", details["plan"])
	}
	steps, ok := plan["selected"].([]map[string]any)
	if !ok || len(steps) == 0 {
		t.Fatalf("plan steps = %v", plan["selected"])
	}
	// Provenance must remain the shared vocabulary so existing evidence
	// grounding still reads these results.
	if _, ok := details["structural_complete"]; !ok {
		t.Error("analysis result is missing structural provenance")
	}
	if _, ok := details["test_files"]; !ok {
		t.Error("analysis result is missing the inventory test count")
	}
}

// TestStructureAnalysisProjectStaysMetadataFirst pins the cheap default: an
// orientation request does not parse the repository.
func TestStructureAnalysisProjectStaysMetadataFirst(t *testing.T) {
	tool := analysisFixture(t)
	result := tool.Execute(testContext(), map[string]any{"view": "analysis"})
	if result.IsError {
		t.Fatalf("analysis failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "dependencies: not analyzed") {
		t.Fatalf("project analysis built dependencies:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "architecture: not analyzed") {
		t.Fatalf("project analysis claimed an architecture:\n%s", result.Text)
	}
	details := structureDetails(t, result)
	coverage, ok := details["coverage"].(map[string]any)
	if !ok || coverage["dependencies_seen"] != false {
		t.Fatalf("coverage = %v", details["coverage"])
	}
	if details["complete"] != false {
		t.Errorf("metadata analysis claimed completeness: %v", details["complete"])
	}
}

// TestStructureExpandListsDirectChildren pins progressive disclosure: the
// analysis returns node ids, and expansion returns only that node's children.
func TestStructureExpandListsDirectChildren(t *testing.T) {
	tool := analysisFixture(t)
	analysis := tool.Execute(testContext(), map[string]any{
		"view": "analysis", "intent": "architecture",
	})
	if analysis.IsError {
		t.Fatalf("analysis failed: %s", analysis.Text)
	}
	details := structureDetails(t, analysis)
	nodes, ok := details["nodes"].([]map[string]any)
	if !ok || len(nodes) == 0 {
		t.Fatalf("nodes = %v", details["nodes"])
	}
	nodeID, _ := nodes[0]["id"].(string)
	if nodeID == "" {
		t.Fatalf("node has no id: %v", nodes[0])
	}

	expanded := tool.Execute(testContext(), map[string]any{"view": "expand", "node": nodeID})
	if expanded.IsError {
		t.Fatalf("expand failed: %s", expanded.Text)
	}
	if !strings.Contains(expanded.Text, "node: "+nodeID) {
		t.Fatalf("expand did not name the node:\n%s", expanded.Text)
	}
	childDetails := structureDetails(t, expanded)
	children, ok := childDetails["children"].([]map[string]any)
	if !ok || len(children) == 0 {
		t.Fatalf("children = %v", childDetails["children"])
	}
	for _, child := range children {
		if child["parent"] != nodeID {
			t.Fatalf("expand returned a non-child %v for parent %s", child["id"], nodeID)
		}
	}
}

// TestStructureExpandByPath pins the path selector, which is the form a model
// uses when it has not called analysis first.
func TestStructureExpandByPath(t *testing.T) {
	tool := analysisFixture(t)
	result := tool.Execute(testContext(), map[string]any{"view": "expand", "path": "api"})
	if result.IsError {
		t.Fatalf("expand failed: %s", result.Text)
	}
	details := structureDetails(t, result)
	node, ok := details["node"].(map[string]any)
	if !ok || node["path"] != "api" {
		t.Fatalf("node = %v", details["node"])
	}
}

// TestStructureAnalysisRejectsMismatchedSelectors pins request-shape errors
// instead of silently ignoring an argument the caller believed was applied.
func TestStructureAnalysisRejectsMismatchedSelectors(t *testing.T) {
	tool := analysisFixture(t)
	cases := []struct {
		args  map[string]any
		class string
	}{
		{map[string]any{"view": "analysis", "intent": "impact"}, agent.ToolFailureInvalidArgument},
		{map[string]any{"view": "analysis", "path": "api"}, agent.ToolFailureInvalidSelector},
		{map[string]any{"view": "analysis", "package": "api"}, agent.ToolFailureInvalidSelector},
		{map[string]any{"view": "overview", "intent": "architecture"}, agent.ToolFailureInvalidArgument},
		{map[string]any{"view": "expand"}, agent.ToolFailureInvalidArgument},
		{map[string]any{"view": "analysis", "intent": "nonsense"}, agent.ToolFailureInvalidSelector},
		{map[string]any{"view": "analysis", "node": "dir:api"}, agent.ToolFailureInvalidArgument},
		{map[string]any{"view": "analysis", "skip": 5}, agent.ToolFailureInvalidArgument},
		{map[string]any{"view": "expand", "node": "dir:api", "intent": "project"}, agent.ToolFailureInvalidArgument},
		{map[string]any{"view": "analysis", "level": "planet"}, agent.ToolFailureInvalidSelector},
	}
	for _, testCase := range cases {
		result := tool.Execute(testContext(), testCase.args)
		if !result.IsError {
			t.Errorf("%v was accepted: %s", testCase.args, result.Text)
			continue
		}
		if string(result.FailureClass) != testCase.class {
			t.Errorf("%v class = %q, want %q", testCase.args, result.FailureClass, testCase.class)
		}
		if !result.Rejected() {
			t.Errorf("%v was not marked request-level", testCase.args)
		}
	}
}

// TestStructureAnalysisRejectsOverviewActivationForGraphIntent pins the
// activation floor: an architecture question cannot be answered from metadata,
// so the request is refused instead of being silently upgraded.
func TestStructureAnalysisRejectsOverviewActivationForGraphIntent(t *testing.T) {
	tool := analysisFixture(t)
	result := tool.Execute(testContext(), map[string]any{
		"view": "analysis", "intent": "architecture", "activation": "overview",
	})
	if !result.IsError || result.FailureClass != agent.ToolFailureInvalidArgument {
		t.Fatalf("result = %+v", result)
	}
}
