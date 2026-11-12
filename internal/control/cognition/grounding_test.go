package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// The grounding contract is projected into the runtime state block, so the
// model sees the entities and ambiguity it must not silently replace. This is
// the context half of entity preservation.
func TestRenderStateCarriesGrounding(t *testing.T) {
	intent := contract.IntentContract{
		Objective: "Additional projects are k8s and opencode.",
		Kind:      contract.ObjectiveInformational,
		Grounding: &contract.IntentGrounding{
			Objective: "Additional projects are k8s and opencode.",
			Action:    contract.GroundingUnknown,
			Entities: []contract.Entity{
				{Value: "k8s", Kind: contract.EntityProject},
				{Value: "opencode", Kind: contract.EntityProject},
			},
			Scope: contract.GroundingScope{
				Kind:   contract.ScopeNamed,
				Values: []string{"k8s", "opencode"},
			},
			Ambiguities: []contract.Ambiguity{{Kind: "unknown_action", Material: true}},
			Confidence:  contract.GroundingConfidenceLow,
		},
	}
	shape := BuildShape(intent, ShapeParams{Authority: "ask", Intent: "read"})
	if len(shape.NamedEntities) != 2 {
		t.Fatalf("shape entities = %v", shape.NamedEntities)
	}
	if shape.RequestedAction != string(contract.GroundingUnknown) {
		t.Fatalf("shape action = %q", shape.RequestedAction)
	}
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		ToolName: "read",
		Args:     map[string]any{"path": "README.md"},
		Result:   agent.Result{Text: "package main"},
	})
	rendered := RenderState(shape, state, SummarizeExecution(nil), Sufficiency{State: Sufficient, Stage: StageAnswer},
		CandidateSet{}, 0, 0)
	for _, want := range []string{"Named entities", "k8s", "opencode", "Requested action: unknown", "unknown_action"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("state block missing %q:\n%s", want, rendered)
		}
	}
}

// The model sees the claim boundary before it writes the answer, so it does
// not learn the difference between configuration and observed behavior only
// from a completion review. The statuses are evidence-derived, not a generic
// warning: a README establishes documentation and nothing else.
func TestRenderStateCarriesClaimBoundary(t *testing.T) {
	intent := contract.IntentContract{
		Objective: "check this project",
		Kind:      contract.ObjectiveInformational,
	}
	shape := BuildShape(intent, ShapeParams{Authority: "ask", Intent: "read"})
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		ToolName: "read",
		Args:     map[string]any{"path": "README.md"},
		Result:   agent.Result{Text: "project"},
	})
	rendered := RenderState(shape, state, SummarizeExecution(nil), Sufficiency{State: Sufficient, Stage: StageAnswer},
		CandidateSet{}, 0, 0)
	for _, want := range []string{
		"Claim evidence:",
		"implementation=NOT_OBSERVED",
		"runtime=NOT_OBSERVED",
		"configured=NOT_OBSERVED",
		"documented=OBSERVED",
		"test_inventory=NOT_QUERIED",
		"configuration declares topology, not execution",
		"not found in inspected scope",
		"Implementation status and behavior need source/runtime evidence",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("state block missing %q:\n%s", want, rendered)
		}
	}
}

// The generation boundary is not only prose: a broad project analysis with
// documentation, manifests, configuration, and inventory but no source content
// stays in exploration, and the projected action surface is not answer-only.
func TestProjectionKeepsSourceMissingClaimOpen(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read", Facts: RepositoryFacts{Go: true}})
	state := NewEvidenceState()
	turns := []agent.Turn{{Results: []agent.ToolOutcome{
		readEvidenceForTest("read-readme", "README.md", "# E-commerce Microservices Platform"),
		readEvidenceForTest("read-package", "package.json", `{"dependencies":{"react":"18"}}`),
		readEvidenceForTest("read-module", "backend/go.mod", "module ecommerce"),
		readEvidenceForTest("read-make", "backend/Makefile", "build:\n\tgo build ./...\n"),
		readEvidenceForTest("read-compose", "backend/docker-compose.yml", "services:\n  cart-service:\n    ports:\n      - \"8082:8080\"\n"),
		{ToolName: "structure", CallID: "structure-overview", Args: map[string]any{"view": "overview"},
			Result: agent.Result{
				Text: "view: overview\ntests: 0 (test_files: 0)\n",
				Details: map[string]any{
					"view": "overview", "graph_state": "CURRENT", "complete": false,
					"partial": true, "truncated": true, "scope_complete": false,
					"inventory_complete": true, "structural_complete": true,
					"test_files": 0, "total": 0,
				},
			}},
	}}}
	projector := NewProjector(shape, state, shape.RepositoryFacts)
	result := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns:    turns,
		Tools:    []agent.Tool{testTool{name: "read", tier: agent.TierRead}},
	})
	if result.Report.AnswerEligible {
		t.Fatal("a source-free broad analysis was made answer-eligible")
	}
	if review := result.Report.EvidenceReview(); review == "" {
		t.Fatal("a source-free broad analysis had no evidence review")
	}
	var system strings.Builder
	for _, block := range result.System {
		system.WriteString(block.Text)
	}
	if !strings.Contains(system.String(), "implementation=NOT_OBSERVED") {
		t.Fatalf("state block did not name the missing implementation class:\n%s", system.String())
	}
}

// A configuration read and a structural overview are not source behavior. The
// state block must say so before the model can promote Compose topology into a
// runtime claim.
func TestRenderStateClaimBoundarySeparatesConfigurationFromImplementation(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read"})
	state := NewEvidenceState()
	for _, path := range []string{"README.md", "backend/go.mod", "backend/Makefile", "backend/docker-compose.yml"} {
		state.ObserveOutcome(agent.ToolOutcome{
			CallID:   "read-" + path,
			ToolName: "read",
			Args:     map[string]any{"path": path},
			Result:   agent.Result{Text: "declared content"},
		})
	}
	state.ObserveOutcome(agent.ToolOutcome{
		CallID:   "structure-overview",
		ToolName: "structure",
		Args:     map[string]any{"view": "overview"},
		Result: agent.Result{
			Text: "view: overview\ntests: 0 (test_files: 0)\n",
			Details: map[string]any{
				"view":                "overview",
				"graph_state":         "CURRENT",
				"complete":            false,
				"partial":             true,
				"truncated":           true,
				"scope_complete":      false,
				"structural_complete": true,
				"test_files":          0,
				"total":               0,
			},
		},
	})
	rendered := RenderState(shape, state, SummarizeExecution(nil), Sufficiency{State: Insufficient, Stage: StageExplore},
		CandidateSet{}, 0, 0)
	for _, want := range []string{
		"implementation=NOT_OBSERVED",
		"runtime=NOT_OBSERVED",
		"configured=OBSERVED",
		"documented=OBSERVED",
		"scope=PARTIAL",
		"test_inventory=NOT_QUERIED",
		"[KNOWN/exact/configuration]",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("state block missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "implementation=OBSERVED") {
		t.Fatalf("configuration or inventory was promoted to implementation:\n%s", rendered)
	}
}

// A complete inventory that observed test files supports the opposite claim:
// the state block must not let the model call tests absent.
func TestRenderStateClaimBoundaryRejectsAbsenceWhenTestsExist(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "check this project",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read"})
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID:   "structure-tests",
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: agent.Result{
			Text: "view: tests\nreturned: 2\ntotal: 5\n",
			Details: map[string]any{
				"view":                "tests",
				"graph_state":         "CURRENT",
				"complete":            true,
				"partial":             false,
				"truncated":           false,
				"scope_complete":      true,
				"inventory_complete":  true,
				"structural_complete": true,
				"completeness":        "COMPLETE",
				"test_files":          2,
				"total":               5,
			},
		},
	})
	rendered := RenderState(shape, state, SummarizeExecution(nil), Sufficiency{State: Sufficient, Stage: StageAnswer},
		CandidateSet{}, 0, 0)
	if !strings.Contains(rendered, "test-file absence not supported") {
		t.Fatalf("state block did not reject a false absence claim:\n%s", rendered)
	}
	if strings.Contains(rendered, "test-file absence supported") {
		t.Fatalf("state block supported absence despite observed test files:\n%s", rendered)
	}
}

// A complete, unscoped test inventory is the one observation that can support
// a test-file absence claim. The state block must expose that support, including
// the observed count.
func TestRenderStateClaimBoundaryShowsCompleteTestInventory(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "check this project",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Intent: "read"})
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID:   "read-main",
		ToolName: "read",
		Args:     map[string]any{"path": "backend/cmd/product-service/main.go"},
		Result:   agent.Result{Text: "func main() { http.ListenAndServe(\":8081\", nil) }"},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID:   "structure-tests",
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: agent.Result{
			Text: "view: tests\nreturned: 0\ntotal: 0\n",
			Details: map[string]any{
				"view":                "tests",
				"graph_state":         "CURRENT",
				"complete":            true,
				"partial":             false,
				"truncated":           false,
				"scope_complete":      true,
				"inventory_complete":  true,
				"structural_complete": true,
				"completeness":        "COMPLETE",
				"test_files":          0,
				"total":               0,
			},
		},
	})
	rendered := RenderState(shape, state, SummarizeExecution(nil), Sufficiency{State: Sufficient, Stage: StageAnswer},
		CandidateSet{}, 0, 0)
	for _, want := range []string{
		"implementation=OBSERVED",
		"backend/cmd/product-service/main.go",
		"test_inventory=COMPLETE (0 test files, 0 tests; test-file absence supported)",
		"scope=COMPLETE",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("state block missing %q:\n%s", want, rendered)
		}
	}
}
