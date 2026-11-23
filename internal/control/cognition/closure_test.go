package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

func externalReadEvidenceForTest(id string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: id, ToolName: "mcp_read",
		Result: agent.Result{
			Text: "MCP tool result: offline note capture",
			Details: map[string]any{
				"status": "ok",
				"evidence": []knowledge.Evidence{{
					Kind: knowledge.EvidenceMCPToolResult,
					Provenance: knowledge.Provenance{
						Capability: knowledge.CapabilityMCPReadTool,
						Source:     "mcp:gry",
						Provider:   "gry",
					},
				}},
			},
		},
	}
}

func missingKeys(suff Sufficiency) map[string]bool {
	keys := map[string]bool{}
	for _, key := range suff.Missing {
		keys[key] = true
	}
	return keys
}

// A request that explicitly requires external knowledge is not answer-eligible
// while every observed fact is local. Closing here demanded an answer that the
// completion evaluator would reject for lacking the evidence the user named.
func TestRequiredExternalObservationPreventsAnswerClosure(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about? use gry mcp",
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "# Project\n\nA coding agent."),
		readEvidenceForTest("read-2", "main.go", "package main"),
	)
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State == Sufficient || suff.Stage == StageAnswer {
		t.Fatalf("local evidence closed an external objective: %+v", suff)
	}
	if !missingKeys(suff)["external_observation"] {
		t.Fatalf("sufficiency missing = %v, want external_observation", suff.Missing)
	}

	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "mcp_read", tier: agent.TierRead, classes: []agent.ActionClass{
			agent.ActionExternal, agent.ActionConfigured,
		}},
	}
	set := Candidates(shape, state, ExecutionState{}, tools, RepositoryFacts{Go: true}, suff)
	if len(set.Tools) != 1 || set.Tools[0].Name() != "mcp_read" {
		names := make([]string, 0, len(set.Tools))
		for _, tool := range set.Tools {
			names = append(names, tool.Name())
		}
		t.Fatalf("candidates = %v, want only the external capability", names)
	}
}

// The external observation is the missing evidence. Once it succeeds, the
// local evidence is sufficient and the answer boundary is valid.
func TestExternalObservationReachesTheAnswerBoundary(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about? use gry mcp",
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "# Project\n\nA coding agent."),
		readEvidenceForTest("read-2", "main.go", "package main"),
		externalReadEvidenceForTest("mcp-1"),
	)
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("external observation left the task at %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
	if !state.HasExternalObservation() {
		t.Fatal("the successful external read was not recorded as external evidence")
	}
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "mcp_read", tier: agent.TierRead, classes: []agent.ActionClass{
			agent.ActionExternal, agent.ActionConfigured,
		}},
	}
	set := Candidates(shape, state, ExecutionState{}, tools, RepositoryFacts{Go: true}, suff)
	if len(set.Tools) != 0 {
		t.Fatalf("offered %d tool(s) after the external answer boundary", len(set.Tools))
	}
}

// When external knowledge is the only missing requirement and the constructed
// action space has no external capability, no model turn can make progress.
// The projection must say so terminally instead of offering an empty surface
// with no explanation.
func TestRequiredExternalCapabilityWithNoProviderIsTerminal(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about? use gry mcp",
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	projected := projector.Project(agent.ProjectionRequest{
		Tools: []agent.Tool{testTool{name: "read", tier: agent.TierRead}},
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{
			readEvidenceForTest("read-1", "README.md", "# Project\n\nA coding agent."),
			readEvidenceForTest("read-2", "main.go", "package main"),
		}}},
	})
	if !projected.Report.Terminal {
		t.Fatalf("a required external capability with no provider was not terminal: %+v", projected.Report)
	}
	if !strings.Contains(projected.Report.StopReason, "no external capability") {
		t.Fatalf("stop reason = %q, want the missing-capability reason", projected.Report.StopReason)
	}
	if len(projected.Tools) != 0 {
		t.Fatalf("offered %d tool(s) at a terminal boundary", len(projected.Tools))
	}
}

// Discovery names capabilities; it does not observe the external knowledge the
// request requires. It must not satisfy the requirement or close the surface.
func TestMCPDiscoveryDoesNotSatisfyExternalObservation(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and use gry mcp",
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "list-1", ToolName: "mcp_list",
		Result: agent.Result{
			Text: "server: gry\ndisclosed: 20",
			Details: map[string]any{
				"capability":     string(knowledge.CapabilityMCPDiscovery),
				"discovery_only": true,
			},
		},
	})
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State == Sufficient || suff.Stage == StageAnswer {
		t.Fatalf("discovery closed the external requirement: %+v", suff)
	}
	if !missingKeys(suff)["external_observation"] {
		t.Fatalf("sufficiency missing = %v, want external_observation", suff.Missing)
	}
}

// The answer review asks the model to correct an unsupported claim. The loop
// reopens the constructed tool set for that one turn, so the runtime block must
// describe the same surface: the previous answer-only verdict told the model
// that no tool existed while 23 tools were still in the request.
func TestCorrectionRequestReopensOnlyThePermittedSurface(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "# Project\n\nA coding agent."),
		readEvidenceForTest("read-2", "docs/architecture.md", "# Architecture\n"),
		readEvidenceForTest("read-3", "main.go", "package main\n"),
		readEvidenceForTest("read-4", "go.mod", "module example.com/project\n"),
	)
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "ls", tier: agent.TierRead},
		testTool{name: "write", tier: agent.TierWrite, ops: []contract.ChangeOp{contract.ChangeCreate}},
	}

	closed := NewProjector(shape, state.Clone(), RepositoryFacts{Go: true}).Project(agent.ProjectionRequest{
		Tools: tools, ForceAnswer: true,
	})
	if len(closed.Tools) != 0 || !closed.Report.AnswerEligible {
		t.Fatalf("answer-only request offered %d tool(s), eligible=%v", len(closed.Tools), closed.Report.AnswerEligible)
	}

	corrected := NewProjector(shape, state.Clone(), RepositoryFacts{Go: true}).Project(agent.ProjectionRequest{
		Tools: tools, ForceAnswer: true, Correcting: true, CorrectionBudget: 4,
	})
	var names []string
	for _, tool := range corrected.Tools {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "read,ls" {
		t.Fatalf("correction surface = %v, want the read-only tools the ASK shape permits", names)
	}
	if corrected.Report.AnswerEligible {
		t.Fatal("a correction request with tools was reported answer-eligible")
	}
	runtime := strings.Join(corrected.Runtime, "\n")
	if strings.Contains(runtime, "No tool is available") {
		t.Fatalf("correction runtime still claims no tool is available:\n%s", runtime)
	}
	if !strings.Contains(runtime, "Available actions: read") {
		t.Fatalf("correction runtime does not name the reopened surface:\n%s", runtime)
	}
}

// A spent correction window must close the action surface. The loop refuses
// every non-terminal call after the allowance is spent, so offering the same
// tools advertises a capability the next call cannot use and costs a turn on a
// refusal instead of the correction.
func TestSpentCorrectionClosesTheSurface(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "# Project\n\nA coding agent."),
	)
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "grep", tier: agent.TierRead},
	}
	projected := NewProjector(shape, state, RepositoryFacts{Go: true}).Project(agent.ProjectionRequest{
		Tools: tools, Correcting: true, CorrectionBudget: 0,
	})
	if len(projected.Tools) != 0 {
		t.Fatalf("spent correction offered %d tool(s): %v", len(projected.Tools), projected.Report.ToolNames)
	}
	if !projected.Report.AnswerEligible {
		t.Fatal("spent correction left the answer ineligible despite closing every tool")
	}
	runtime := strings.Join(projected.Runtime, "\n")
	if !strings.Contains(runtime, "Correction budget exhausted") {
		t.Fatalf("spent correction did not state the exhausted allowance:\n%s", runtime)
	}
	if strings.Contains(runtime, "tool call(s) remaining") {
		t.Fatalf("spent correction still advertised a remaining allowance:\n%s", runtime)
	}
	if len(projected.Report.Excluded) == 0 {
		t.Fatal("spent correction withheld the tools without a reason")
	}
}

// During a bounded repair the review's claims are the actionable list. The
// generic component frontier names unrelated work, and following it spends the
// correction allowance on a component the answer was not asked about.
func TestCorrectionSuppressesTheGenericEvidenceFrontier(t *testing.T) {
	shape := broadShape(RepositoryFacts{Go: true})
	state := NewEvidenceState()
	observeReads(state, shape,
		readEvidenceForTest("read-1", "README.md", "# Project\n\nA coding agent."),
		readEvidenceForTest("read-2", "backend/main.go", "package main\n"),
	)
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "backend"},
		Result: agent.Result{
			Text:    "backend\nauth/\ncart/\n",
			Details: map[string]any{"path": "backend", "entries": 2},
		},
	}}}})
	tools := []agent.Tool{testTool{name: "read", tier: agent.TierRead}}

	ordinary := NewProjector(shape, state.Clone(), RepositoryFacts{Go: true}).Project(agent.ProjectionRequest{
		Tools: tools,
	})
	if !strings.Contains(strings.Join(ordinary.Runtime, "\n"), "Evidence frontier:") {
		t.Fatalf("ordinary projection lost the evidence frontier:\n%s", strings.Join(ordinary.Runtime, "\n"))
	}

	repair := NewProjector(shape, state.Clone(), RepositoryFacts{Go: true}).Project(agent.ProjectionRequest{
		Tools: tools, Correcting: true, CorrectionBudget: 4,
	})
	runtime := strings.Join(repair.Runtime, "\n")
	if strings.Contains(runtime, "Evidence frontier:") {
		t.Fatalf("correction kept the unrelated component frontier:\n%s", runtime)
	}
	if !strings.Contains(runtime, "Correction budget: 4") {
		t.Fatalf("correction lost its bounded allowance:\n%s", runtime)
	}
}
