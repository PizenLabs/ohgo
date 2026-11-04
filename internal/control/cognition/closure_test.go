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
