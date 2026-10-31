package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// readTurn builds one completed turn that read a file with the given contents.
func readTurn(callID, path, body string) agent.Turn {
	return agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   callID,
		ToolName: "read",
		Args:     map[string]any{"path": path},
		Result: agent.Result{Text: body, Details: map[string]any{
			"path": path, "totalLines": 3, "offset": 1, "limit": 2000,
		}},
	}}}
}

func informationalProjection(t *testing.T, shape TaskShape, turns []agent.Turn) agent.ProjectionResult {
	t.Helper()
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "ls", tier: agent.TierRead},
	}
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	return projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns:    turns,
		Tools:    tools,
		System:   []ai.SystemPromptBlock{{Text: "base"}},
	})
}

// A turn that observes the repository without adding a fact has no evidence
// gain left. The runtime must stop offering tools rather than let a model spend
// a turn on exploration that cannot change the answer.
func TestProjectorStopsWhenATurnAddsNoEvidence(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Does this workflow run automatically?",
	}, ShapeParams{Authority: "ask"})

	first := readTurn("c1", ".github/workflows/telemetry.yml", "1\ton: schedule")
	projected := informationalProjection(t, shape, []agent.Turn{first})
	if len(projected.Tools) == 0 {
		t.Fatal("the first observation was stopped before it produced evidence")
	}

	// The identical observation again, with a new call identity.
	duplicate := readTurn("c2", ".github/workflows/telemetry.yml", "1\ton: schedule")
	projected = informationalProjection(t, shape, []agent.Turn{first, duplicate})
	if projected.Tools == nil {
		t.Fatal("the stop returned a nil tool set, which the loop reads as \"unchanged\"")
	}
	if len(projected.Tools) != 0 {
		t.Fatalf("a turn with no evidence gain still offered %d tool(s)", len(projected.Tools))
	}
	if len(projected.Report.Excluded) == 0 {
		t.Fatalf("the stop was not audited: excluded=%+v", projected.Report.Excluded)
	}
	state := ""
	for _, block := range projected.System {
		state += block.Text
	}
	if !strings.Contains(state, "no new evidence") {
		t.Fatalf("the runtime state does not explain the stop:\n%s", state)
	}
}

// A different range of the same file is new information, so it must not be
// mistaken for a repeat.
func TestProjectorKeepsToolsWhenTheObservationAddsAFact(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Does this workflow run automatically?",
	}, ShapeParams{Authority: "ask"})
	first := readTurn("c1", "main.go", "1\tpackage main")
	second := agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   "c2",
		ToolName: "read",
		Args:     map[string]any{"path": "main.go", "offset": 200},
		Result: agent.Result{Text: "200\tfunc main() {}", Details: map[string]any{
			"path": "main.go", "totalLines": 400, "offset": 200, "limit": 200,
		}},
	}}}
	projected := informationalProjection(t, shape, []agent.Turn{first, second})
	if len(projected.Tools) == 0 {
		t.Fatal("a second window of the same file was treated as a repeat")
	}
}

// A resumed task may begin by re-reading a file it already knows. The run's
// first observation turn is context re-establishment, not wasted work, so it is
// not judged as stagnation.
func TestProjectorDoesNotStopOnTheFirstTurnOfARun(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Does this workflow run automatically?",
	}, ShapeParams{Authority: "ask"})
	evidence := NewEvidenceState()
	// A fact persisted from an earlier user turn.
	evidence.ObserveOutcome(agent.ToolOutcome{
		CallID:   "previous-run",
		ToolName: "read",
		Args:     map[string]any{"path": "main.go"},
		Result: agent.Result{Text: "1\tpackage main", Details: map[string]any{
			"path": "main.go", "totalLines": 1, "offset": 1, "limit": 2000,
		}},
	})
	projector := NewProjector(shape, evidence, RepositoryFacts{Go: true})
	projected := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns:    []agent.Turn{readTurn("this-run", "main.go", "1\tpackage main")},
		Tools:    []agent.Tool{testTool{name: "read", tier: agent.TierRead}},
		System:   []ai.SystemPromptBlock{{Text: "base"}},
	})
	if len(projected.Tools) == 0 {
		t.Fatal("the first observation turn of a run was judged as stagnation")
	}
}

// A turn whose calls were all refused observed nothing, so it is not evidence
// of stagnation: the model is adapting, not repeating itself.
func TestProjectorDoesNotStopOnARefusedTurn(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Does this workflow run automatically?",
	}, ShapeParams{Authority: "ask"})
	first := readTurn("c1", "main.go", "1\tpackage main")
	refused := agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   "c2",
		ToolName: "write",
		Result:   agent.Result{Text: "not offered in this task stage", IsError: true, CandidateRejected: true},
	}}}
	projected := informationalProjection(t, shape, []agent.Turn{first, refused})
	if len(projected.Tools) == 0 {
		t.Fatal("a refused turn was treated as evidence stagnation")
	}
}

// A change objective's next action is the change, not another observation. A
// repeated read must not remove the tools that can perform it.
func TestProjectorDoesNotStopAChangeObjective(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	first := readTurn("c1", "README.md", "1\t# Project")
	duplicate := readTurn("c2", "README.md", "1\t# Project")
	projected := informationalProjection(t, shape, []agent.Turn{first, duplicate})
	if len(projected.Tools) == 0 {
		t.Fatal("a change objective was stopped before it could act")
	}
}
