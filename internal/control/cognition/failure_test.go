package cognition

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

func failureOutcome(callID, tool string, args map[string]any, class knowledge.FailureClass, retryable bool) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID:   callID,
		ToolName: tool,
		Args:     args,
		Result: agent.Result{
			Text:             "capability failed: " + string(class),
			IsError:          true,
			FailureClass:     string(class),
			FailureRetryable: retryable,
			Details: map[string]any{
				"capability":             "mcp.read_tool",
				"failure_classification": string(class),
				"retryable":              retryable,
			},
		},
	}
}

func refusedOutcome(callID, tool string, args map[string]any, text string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID:   callID,
		ToolName: tool,
		Args:     args,
		Result: agent.Result{
			Text:              text,
			IsError:           true,
			CandidateRejected: true,
			FailureRejected:   true,
		},
	}
}

func githubShape() TaskShape {
	return BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about? use gry mcp",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
}

func blockedBy(state *EvidenceState, shape TaskShape) bool {
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	return suff.State == Blocked && suff.Kind == BlockCapabilityFailure
}

// A non-retryable failure refuses the identical call after one execution, then
// closes the task when the model repeats it. The second proposal never runs.
func TestNonRetryableFailureRefusesRepeatThenBlocks(t *testing.T) {
	state := NewEvidenceState()
	shape := githubShape()
	args := map[string]any{"capability": "gry:nope"}
	first := failureOutcome("c1", "mcp_read", args, knowledge.FailureUnknownCapability, false)
	state.ObserveOutcome(first)

	if blockedBy(state, shape) {
		t.Fatal("a single non-retryable failure blocked before the model could correct it")
	}
	failures := state.FailureState()
	if _, refused := failures.Refusals[agent.CallSignature("mcp_read", args)]; !refused {
		t.Fatal("the identical non-retryable call was not refused")
	}

	state.ObserveOutcome(refusedOutcome("c2", "mcp_read", args, "not executed again"))
	if !blockedBy(state, shape) {
		t.Fatal("a repeated non-retryable failure did not close the task")
	}
}

// A fatal class cannot recover without an external change, so it blocks at the
// first failure rather than waiting for a repeat.
func TestFatalFailureBlocksAtTheFirstFailure(t *testing.T) {
	state := NewEvidenceState()
	shape := githubShape()
	state.ObserveOutcome(failureOutcome("c1", "mcp_read",
		map[string]any{"capability": "gry:search"}, knowledge.FailureAuthentication, false))
	if !blockedBy(state, shape) {
		t.Fatal("an authentication failure did not block the required capability")
	}
}

// A retryable class gets one bounded retry, then the identical call is refused
// and the task closes. The budget is class-based, not provider-based.
func TestRetryableFailureGetsOneRetryThenBlocks(t *testing.T) {
	state := NewEvidenceState()
	shape := githubShape()
	args := map[string]any{"capability": "gry:search"}
	state.ObserveOutcome(failureOutcome("c1", "mcp_read", args, knowledge.FailureTimeout, true))
	if blockedBy(state, shape) {
		t.Fatal("the first retryable failure closed the task")
	}
	if _, refused := state.FailureState().Refusals[agent.CallSignature("mcp_read", args)]; refused {
		t.Fatal("the first retryable failure refused the one allowed retry")
	}
	state.ObserveOutcome(failureOutcome("c2", "mcp_read", args, knowledge.FailureTimeout, true))
	if _, refused := state.FailureState().Refusals[agent.CallSignature("mcp_read", args)]; !refused {
		t.Fatal("the retry budget was not enforced after the second failure")
	}
	state.ObserveOutcome(refusedOutcome("c3", "mcp_read", args, "retry budget spent"))
	if !blockedBy(state, shape) {
		t.Fatal("a repeated retryable failure did not close the task")
	}
}

// A successful observation of the same coordinate recovers the failure, so a
// corrected retry is not punished.
func TestSuccessfulRetryResolvesFailure(t *testing.T) {
	state := NewEvidenceState()
	shape := githubShape()
	args := map[string]any{"capability": "gry:search"}
	state.ObserveOutcome(failureOutcome("c1", "mcp_read", args, knowledge.FailureTimeout, true))
	state.ObserveOutcome(agent.ToolOutcome{
		CallID:   "c2",
		ToolName: "mcp_read",
		Args:     args,
		Result: agent.Result{
			Text:    "MCP tool result",
			Details: map[string]any{"status": "ok"},
		},
	})
	if blockedBy(state, shape) {
		t.Fatal("a recovered failure still blocked the task")
	}
	if records := state.FailureState().Records; len(records) != 0 {
		t.Fatalf("failure records = %+v, want the recovered record excluded", records)
	}
}

// A failure does not block when the evidence already satisfies the objective.
// The ledger narrows retries; it never overrides sufficient evidence.
func TestFailureDoesNotBlockSufficientEvidence(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "Does this workflow run automatically?",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
	state := NewEvidenceState()
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{
		readEvidenceForTest("read-1", ".github/workflows/ci.yml", "1\ton: schedule"),
	}}})
	state.ObserveOutcome(failureOutcome("c1", "mcp_read",
		map[string]any{"capability": "gry:nope"}, knowledge.FailureUnknownCapability, false))
	state.ObserveOutcome(refusedOutcome("c2", "mcp_read",
		map[string]any{"capability": "gry:nope"}, "not executed"))
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State == Blocked {
		t.Fatalf("sufficient evidence was blocked by an optional failure: %+v", suff)
	}
}

// A refusal, an absence, and a resource stop are control observations, not
// failed capabilities.
func TestNonFailuresDoNotEnterTheLedger(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "denied", ToolName: "write",
		Result: agent.Result{Text: "authorization denied", IsError: true, CandidateRejected: true},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "absent", ToolName: "read",
		Result: agent.Result{Text: "no such file", IsError: true, ObservedAbsence: true},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "budget", ToolName: "bash",
		Result: agent.Result{
			Text:      "budget exhausted",
			IsError:   true,
			Exhausted: &budget.ExhaustedError{Dimension: budget.DimensionToolCalls},
		},
	})
	if records := state.FailureState().Records; len(records) != 0 {
		t.Fatalf("control decisions entered the failure ledger: %+v", records)
	}
}

// The state block names the failed capability and its class, so the model sees
// why the action surface closed.
func TestRenderStateShowsFailedCapability(t *testing.T) {
	shape := githubShape()
	state := NewEvidenceState()
	args := map[string]any{"capability": "gry:nope"}
	state.ObserveOutcome(failureOutcome("c1", "mcp_read", args, knowledge.FailureUnknownCapability, false))
	state.ObserveOutcome(refusedOutcome("c2", "mcp_read", args, "not executed"))
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	text := RenderState(shape, state, ExecutionState{}, suff, CandidateSet{}, 2000, 20)
	for _, want := range []string{"Failed capabilities", "mcp_read", "unknown_capability"} {
		if !strings.Contains(text, want) {
			t.Fatalf("state block missing %q:\n%s", want, text)
		}
	}
}

// A required external objective cannot guess capability names forever. Each
// distinct failed capability is a legitimate recovery attempt up to the bound;
// after it, the task closes even though no single signature repeated.
func TestRequiredExternalFailureBudgetClosesDistinctGuesses(t *testing.T) {
	state := NewEvidenceState()
	shape := githubShape()
	for i, capability := range []string{"gry:one", "gry:two", "gry:three"} {
		state.ObserveOutcome(failureOutcome(
			fmt.Sprintf("c%d", i), "mcp_read",
			map[string]any{"capability": capability},
			knowledge.FailureUnknownCapability, false,
		))
	}
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State != Blocked || suff.Kind != BlockCapabilityFailure {
		t.Fatalf("sufficiency = %+v, want a capability block after three distinct guesses", suff)
	}
	if !strings.Contains(strings.Join(suff.Reasons, ";"), "required external capability") {
		t.Fatalf("block reason = %v, want the required-external-capability reason", suff.Reasons)
	}
}

// A workspace change invalidates a non-fatal failure: the same command is a
// different proposal once the code it checks has changed.
func TestMutationInvalidatesNonFatalFailure(t *testing.T) {
	state := NewEvidenceState()
	shape := githubShape()
	args := map[string]any{"command": "go test ./..."}
	state.ObserveOutcome(failureOutcome("c1", "bash", args, knowledge.FailureToolError, false))
	if _, refused := state.FailureState().Refusals[agent.CallSignature("bash", args)]; !refused {
		t.Fatal("the failed command was not refused before a change")
	}
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "c2", ToolName: "edit", Args: map[string]any{"path": "calc.go"},
		Result: agent.Result{Text: "fixed", Mutates: true, Details: map[string]any{"path": "calc.go"}},
	})
	if _, refused := state.FailureState().Refusals[agent.CallSignature("bash", args)]; refused {
		t.Fatal("a workspace change did not invalidate the pre-change failure")
	}
	if blockedBy(state, shape) {
		t.Fatal("a resolved pre-change failure still blocked the task")
	}
}

// readEvidenceForTest builds a real successful read outcome with the metadata
// the evidence model needs to attribute it.
func readEvidenceForTest(id, path, body string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: id, ToolName: "read", Args: map[string]any{"path": path},
		Result: agent.Result{Text: body, Details: map[string]any{
			"path": path, "totalLines": 1, "offset": 1, "limit": 2000,
		}},
	}
}
