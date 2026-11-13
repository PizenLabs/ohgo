package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

func requestRejection(callID, tool string, args map[string]any, class string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: callID, ToolName: tool, Args: args,
		Result: agent.Result{
			Text:         "request rejected: " + class + "; nothing executed",
			IsError:      true,
			FailureClass: class,
		},
	}
}

func broadProjectShape() TaskShape {
	return BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})
}

// A request-level rejection refuses only the identical bytes. It never becomes
// a capability failure, never blocks the task, and never refuses a corrected
// invocation.
func TestRequestRejectionDoesNotPoisonCapabilityHealth(t *testing.T) {
	state := NewEvidenceState()
	shape := broadProjectShape()
	badArgs := map[string]any{"path": "backend"}
	badSignature := agent.CallSignature("structure", badArgs)

	state.ObserveOutcome(requestRejection("bad-1", "structure", badArgs,
		agent.ToolFailureInvalidSelector))

	failures := state.FailureState()
	if len(failures.Records) != 1 {
		t.Fatalf("failure records = %+v, want one request rejection", failures.Records)
	}
	record := failures.Records[0]
	if record.Class != knowledge.FailureInvalidSelector {
		t.Fatalf("class = %q, want invalid_selector", record.Class)
	}
	if !record.RequestLevel() {
		t.Fatal("selector rejection was recorded as capability health")
	}
	if !record.Refuses() {
		t.Fatal("the identical rejected invocation was not refused")
	}
	if blockedBy(state, shape) {
		t.Fatal("one request rejection blocked the task")
	}
	if _, refused := failures.Refusals[badSignature]; !refused {
		t.Fatal("the exact rejected signature was not refused")
	}

	state.ObserveOutcome(refusedOutcome("bad-2", "structure", badArgs,
		"already rejected"))
	if blockedBy(state, shape) {
		t.Fatal("a repeated request rejection blocked the capability")
	}
	refusals := state.FailureState().Refusals
	if _, refused := refusals[agent.CallSignature("structure",
		map[string]any{"view": "files", "path": "backend"})]; refused {
		t.Fatal("a corrected invocation was refused with the rejected one")
	}
}

// The taxonomy is behavioral: request-level classes stay viable, capability
// classes keep their existing bounded blocking, and retryable classes keep
// exactly one retry.
func TestInvocationFailureTaxonomy(t *testing.T) {
	cases := []struct {
		name     string
		class    knowledge.FailureClass
		retry    bool
		failures int
		refusals int
		request  bool
		blocks   bool
	}{
		{name: "invalid argument", class: knowledge.FailureInvalidArgument, failures: 1, refusals: 1, request: true},
		{name: "invalid request", class: knowledge.FailureInvalidRequest, failures: 1, refusals: 1, request: true},
		{name: "invalid selector", class: knowledge.FailureInvalidSelector, failures: 1, refusals: 1, request: true},
		{name: "unknown tool", class: knowledge.FailureUnknownTool, failures: 1, refusals: 1, request: true},
		{name: "unsupported operation", class: knowledge.FailureUnsupported, failures: 1, refusals: 1, request: true},
		{name: "authority denied", class: knowledge.FailureAuthorityDenied, failures: 1, refusals: 1, request: true},
		{name: "budget exhausted", class: knowledge.FailureBudgetExhausted, failures: 1, refusals: 1, request: true},
		{name: "stale evidence", class: knowledge.FailureStaleEvidence, failures: 1, refusals: 1, request: true},
		{name: "remote unavailable", class: knowledge.FailureUnavailable, failures: 1, refusals: 1, blocks: true},
		{name: "transport failure", class: knowledge.FailureTransport, failures: 1, refusals: 1, blocks: true},
		{name: "execution failed", class: knowledge.FailureExecutionFailed, failures: 1, refusals: 1, blocks: true},
		{name: "remote tool error", class: knowledge.FailureToolError, failures: 1, refusals: 1, blocks: true},
		{name: "timeout retry available", class: knowledge.FailureTimeout, retry: true, failures: 1, blocks: false},
		{name: "timeout exhausted", class: knowledge.FailureTimeout, retry: true, failures: 3, blocks: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := FailureRecord{
				Tool: "structure", Class: tc.class, Retryable: tc.retry,
				Failures: tc.failures, Refusals: tc.refusals,
			}
			if record.RequestLevel() != tc.request {
				t.Fatalf("request-level = %t, want %t", record.RequestLevel(), tc.request)
			}
			if record.Blocks() != tc.blocks {
				t.Fatalf("blocks = %t, want %t", record.Blocks(), tc.blocks)
			}
			if tc.request && !record.Refuses() {
				t.Fatal("a request-level record did not refuse its identical invocation")
			}
		})
	}
}

// A failed structural provider must not close the task while README, manifests,
// root listings, or source reads can satisfy the same requirement.
func TestReconcileAcquisitionUsesIndependentProvider(t *testing.T) {
	shape := broadProjectShape()
	state := NewEvidenceState()
	args := map[string]any{"view": "overview"}
	state.ObserveOutcome(failureOutcome("cap-1", "structure", args,
		knowledge.FailureUnavailable, false))
	state.ObserveOutcome(refusedOutcome("cap-2", "structure", args, "not executed"))
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.Kind != BlockCapabilityFailure {
		t.Fatalf("sufficiency = %+v, want the unreconciled capability block", suff)
	}

	tools := []agent.Tool{
		testTool{name: "structure", tier: agent.TierRead, classes: []agent.ActionClass{
			agent.ActionObserve, agent.ActionStructure,
		}},
		testTool{name: "read", tier: agent.TierRead},
	}
	reconciled := ReconcileAcquisition(state, tools, suff)
	if reconciled.Kind == BlockCapabilityFailure || reconciled.Stage == StageBlocked {
		t.Fatalf("an available read provider did not keep the task viable: %+v", reconciled)
	}
	joined := strings.Join(reconciled.Next, "; ")
	if !strings.Contains(joined, "read") {
		t.Fatalf("fallback guidance did not name the independent provider: %v", reconciled.Next)
	}
}

// A genuine outage still blocks when every provider for a required
// observation is unavailable. Reconciliation is not a blanket unblock.
func TestReconcileAcquisitionKeepsGenuineOutageBlocked(t *testing.T) {
	shape := githubShape()
	state := NewEvidenceState()
	args := map[string]any{"capability": "gry:search"}
	state.ObserveOutcome(failureOutcome("cap-1", "mcp_read", args,
		knowledge.FailureUnavailable, false))
	state.ObserveOutcome(refusedOutcome("cap-2", "mcp_read", args, "not executed"))
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.Kind != BlockCapabilityFailure {
		t.Fatalf("sufficiency = %+v, want a capability block", suff)
	}
	tools := []agent.Tool{
		testTool{name: "mcp_read", tier: agent.TierRead, classes: []agent.ActionClass{
			agent.ActionExternal, agent.ActionConfigured,
		}},
		testTool{name: "read", tier: agent.TierRead},
	}
	reconciled := ReconcileAcquisition(state, tools, suff)
	if reconciled.Kind != BlockCapabilityFailure {
		t.Fatalf("a required external observation was unblocked without a provider: %+v", reconciled)
	}
}

// A successful observation is not invalidated by a later unrelated rejection.
// The rejected invocation contributes a diagnostic, never success evidence.
func TestSuccessfulObservationSurvivesUnrelatedRejection(t *testing.T) {
	state := NewEvidenceState()
	shape := broadProjectShape()
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{
		readEvidenceForTest("read-1", "README.md", "# Project\n\nA coding agent."),
	}}})
	before, ok := state.Find("file:README.md")
	if !ok {
		t.Fatal("baseline read was not recorded")
	}

	badArgs := map[string]any{"path": "backend"}
	state.ObserveOutcome(requestRejection("bad-1", "structure", badArgs,
		agent.ToolFailureInvalidSelector))

	after, ok := state.Find("file:README.md")
	if !ok {
		t.Fatal("an unrelated rejection removed the successful observation")
	}
	if after.Status != before.Status || after.Digest != before.Digest {
		t.Fatalf("rejection changed the successful observation: before=%+v after=%+v", before, after)
	}
	if _, ok := state.Find("structure:backend"); ok {
		t.Fatal("a failed structural call was recorded as a successful observation")
	}
}

// A resumable run keeps the rejection class, refusals, and successful evidence
// across a checkpoint. A corrected invocation after resume is not refused.
func TestRequestRejectionSurvivesCheckpoint(t *testing.T) {
	shape := broadProjectShape()
	state := NewEvidenceState()
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{
		readEvidenceForTest("read-1", "README.md", "# Project\n\nA coding agent."),
	}}})
	badArgs := map[string]any{"path": "backend"}
	state.ObserveOutcome(requestRejection("bad-1", "structure", badArgs,
		agent.ToolFailureInvalidSelector))
	state.ObserveOutcome(refusedOutcome("bad-2", "structure", badArgs, "not executed"))

	resumed := state.Clone()
	failures := resumed.FailureState()
	if len(failures.Records) != 1 || !failures.Records[0].RequestLevel() {
		t.Fatalf("resumed failure state = %+v, want the request rejection", failures.Records)
	}
	if failures.Records[0].Refusals == 0 {
		t.Fatal("the refusal count was lost across the checkpoint")
	}
	if _, ok := resumed.Find("file:README.md"); !ok {
		t.Fatal("successful evidence was lost across the checkpoint")
	}
	if _, refused := failures.Refusals[agent.CallSignature("structure",
		map[string]any{"view": "files", "path": "backend"})]; refused {
		t.Fatal("a corrected invocation was refused after resume")
	}
}

// A corrected structure request adds valid evidence while the earlier
// rejection remains diagnostic history. Recovery never fabricates the evidence
// the failed call would have produced, and it does not close the remaining
// evidence gap by itself.
func TestCorrectedStructureObservationAddsEvidenceAndKeepsHistory(t *testing.T) {
	state := NewEvidenceState()
	shape := broadProjectShape()
	badArgs := map[string]any{"path": "backend"}
	state.ObserveOutcome(requestRejection("bad-1", "structure", badArgs,
		agent.ToolFailureInvalidSelector))

	if _, ok := state.Find("structure:backend"); ok {
		t.Fatal("a rejected structure request was recorded as evidence")
	}
	if len(state.Failures) != 1 || state.Failures[0].Class != knowledge.FailureInvalidSelector {
		t.Fatalf("failure history = %+v, want the invalid selector", state.Failures)
	}

	corrected := agent.ToolOutcome{
		CallID: "fixed-1", ToolName: "structure",
		Args: map[string]any{"view": "files", "path": "backend"},
		Result: agent.Result{
			Text: "view: files\nreturned: 1\ntotal: 1\n",
			Details: map[string]any{
				"view": "files", "path": "backend", "graph_state": "CURRENT",
				"complete": true, "scope_complete": true,
				"inventory_complete": true, "structural_complete": true,
				"completeness": "COMPLETE", "total": 1, "returned": 1,
				"files": []map[string]any{{
					"path": "backend/main.go", "test": false, "unsupported": false,
				}},
			},
		},
	}
	state.ObserveOutcome(corrected)
	if _, ok := state.Find("structure:backend"); !ok {
		t.Fatal("the corrected structure observation was not recorded")
	}
	if len(state.Failures) == 0 ||
		state.Failures[0].Class != knowledge.FailureInvalidSelector {
		t.Fatalf("the earlier rejection was erased: %+v", state.Failures)
	}

	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	if suff.State == Sufficient {
		t.Fatalf("recovery alone made the broad task sufficient: %+v", suff)
	}
	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{requestRejection("bad-1", "structure", badArgs,
			agent.ToolFailureInvalidSelector)}},
		{Results: []agent.ToolOutcome{corrected}},
	}
	if verdict := agent.EvaluateCompletion(turns, false); verdict.Complete() {
		t.Fatal("a corrected tool call auto-promoted PROVEN without an answer")
	}
}

// The state block tells the model that an invocation was corrected, not that
// the capability failed, so it does not close the acquisition surface.
func TestRequestRejectionRendersAsCorrection(t *testing.T) {
	shape := broadProjectShape()
	state := NewEvidenceState()
	badArgs := map[string]any{"path": "backend"}
	state.ObserveOutcome(requestRejection("bad-1", "structure", badArgs,
		agent.ToolFailureInvalidSelector))
	suff := EvaluateSufficiency(shape, state, ExecutionState{}, false)
	text := RenderState(shape, state, ExecutionState{}, suff, CandidateSet{}, 2000, 20)
	if !strings.Contains(text, "Invocation corrections") ||
		!strings.Contains(text, "invalid_selector") ||
		!strings.Contains(text, "remains available") {
		t.Fatalf("state block did not describe a request correction:\n%s", text)
	}
	if strings.Contains(text, "Failed capabilities") {
		t.Fatalf("request rejection was rendered as a failed capability:\n%s", text)
	}
}
