package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func conflictShape() TaskShape {
	return BuildShape(contract.IntentContract{
		Kind:                contract.ObjectiveMutation,
		Objective:           "Make the project section scroll in one row without lag",
		BehavioralObjective: true,
		Verification:        []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}, ShapeParams{Authority: "build"})
}

func conflictEvidence() *EvidenceState {
	evidence := NewEvidenceState()
	evidence.Seq = 3
	evidence.Items = []EvidenceItem{
		{
			ID: "mutation:index", Status: StatusKnown, Retention: RetentionExact,
			Provenance: ProvenanceObservation, Summary: "changed index.html",
			Source: Source{Path: "index.html"}, Seq: 1,
		},
		{
			ID: "verification:marquee", Status: StatusKnown, Retention: RetentionExact,
			Provenance: ProvenanceObservation, Summary: "grep marquee assertions passed",
			Source: Source{Tool: "verify", Check: string(contract.CheckAny), Class: string(agent.VerificationStatic), Scope: "*"},
			Seq:    2, Executed: true,
		},
	}
	evidence.RecordVerificationConflict("the section does not move", "verification:marquee",
		"grep marquee assertions passed", 2, "contradiction:1")
	return evidence
}

func conflictTools() []agent.Tool {
	return []agent.Tool{
		testTool{name: "read", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionObserve}},
		testTool{name: "write", tier: agent.TierWrite, classes: []agent.ActionClass{agent.ActionMutate}},
		testTool{name: "edit", tier: agent.TierWrite, classes: []agent.ActionClass{agent.ActionMutate}},
		testTool{name: "bash", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}},
		testTool{name: "verify", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute}},
	}
}

// A user report that contradicts a recorded check must not silently restart
// discovery. The next projection asks for behavioral verification and keeps
// the mutation surface closed.
func TestVerificationConflictRoutesToBehavioralInvestigation(t *testing.T) {
	shape := conflictShape()
	evidence := conflictEvidence()
	projector := NewProjector(shape, evidence, RepositoryFacts{})
	result := projector.Project(agent.ProjectionRequest{Tools: conflictTools(), Turn: 1})

	if result.Report.Sufficiency != string(Insufficient) || result.Report.Stage != string(StageVerify) {
		t.Fatalf("verdict = %s/%s, want INSUFFICIENT/verify", result.Report.Sufficiency, result.Report.Stage)
	}
	if !containsString(result.Report.Missing, "behavioral_verification") {
		t.Fatalf("missing = %v, want behavioral_verification", result.Report.Missing)
	}
	names := toolNames(result.Tools)
	if !containsString(names, "read") || !containsString(names, "verify") {
		t.Fatalf("tools = %v, want read and verify", names)
	}
	if !strings.Contains(strings.Join(result.Runtime, "\n"), "behavioral") {
		t.Fatalf("runtime state = %v, want the behavioral investigation next action", result.Runtime)
	}
}

// While the conflict is open, a direct or shell mutation would repeat the work
// whose result is disputed. The projection must withhold it.
func TestVerificationConflictWithholdsDuplicateBuildMutation(t *testing.T) {
	shape := conflictShape()
	evidence := conflictEvidence()
	execution := ExecutionFromEvidence(evidence)
	suff := EvaluateSufficiency(shape, evidence, execution, false)
	candidates := Candidates(shape, evidence, execution, conflictTools(), RepositoryFacts{}, suff)

	names := make(map[string]bool)
	for _, tool := range candidates.Tools {
		names[tool.Name()] = true
	}
	for _, withheld := range []string{"write", "edit", "bash"} {
		if names[withheld] {
			t.Fatalf("conflict still offered %s: %+v", withheld, candidates.Tools)
		}
	}
	if !names["read"] || !names["verify"] {
		t.Fatalf("tools = %+v, want read and verify", candidates.Tools)
	}
}

// A failed behavioral check confirms the user's report. It resolves the
// conflict truthfully, records the failure, and lets the ordinary BUILD
// surface repair the change instead of trapping the task forever.
func TestFailedBehavioralVerificationResolvesConflict(t *testing.T) {
	shape := conflictShape()
	evidence := conflictEvidence()
	failed := agent.Turn{Results: []agent.ToolOutcome{{
		ToolName: "verify",
		Result: agent.Result{
			Text:         "verification: FAIL",
			IsError:      true,
			FailureClass: agent.ToolFailureVerification,
			Verification: &agent.VerificationEvidence{
				Command: "verify", Check: contract.CheckAny, Class: agent.VerificationBehavioral,
			},
		},
	}}}
	evidence.ObserveTurns(shape, []agent.Turn{mutationTurn("edit-1", "index.html"), failed})
	if open := evidence.OpenVerificationConflict(); open != nil {
		t.Fatalf("conflict stayed open after a behavioral failure: %+v", open)
	}
	if evidence.VerificationConflict == nil || evidence.VerificationConflict.Resolution != "FAIL" {
		t.Fatalf("conflict = %+v, want a recorded FAIL resolution", evidence.VerificationConflict)
	}
	suff := EvaluateSufficiency(shape, evidence, ExecutionFromEvidence(evidence), false)
	candidates := Candidates(shape, evidence, ExecutionFromEvidence(evidence), conflictTools(), RepositoryFacts{}, suff)
	if !containsString(toolNames(candidates.Tools), "edit") {
		t.Fatalf("the failed check did not reopen the repair surface: %+v", candidates.Tools)
	}
}

// A static check is real evidence, but it cannot answer a behavioral conflict.
// The conflict stays open until a behavioral observation exists.
func TestStaticVerificationDoesNotResolveBehavioralConflict(t *testing.T) {
	shape := conflictShape()
	evidence := conflictEvidence()
	static := agent.Turn{Results: []agent.ToolOutcome{{
		ToolName: "verify",
		Result: agent.Result{
			Text:     "verification: PASS",
			Executes: true,
			Finished: true,
			Verifies: true,
			Verification: &agent.VerificationEvidence{
				Command: "grep", Check: contract.CheckAny, Class: agent.VerificationStatic,
			},
		},
	}}}
	evidence.ObserveTurns(shape, []agent.Turn{static})
	open := evidence.OpenVerificationConflict()
	if open == nil {
		t.Fatal("a static check resolved a behavioral conflict")
	}
	if open.Status != VerificationConflictOpen {
		t.Fatalf("status = %s, want OPEN", open.Status)
	}
}

// The model-facing state names the observation class, so a static PASS is not
// presented as proof of runtime behavior.
func TestRenderNamesTheVerificationObservationClass(t *testing.T) {
	shape := conflictShape()
	evidence := NewEvidenceState()
	execution := ExecutionState{
		ToolCalls:    1,
		LastMutation: 0,
		Verifications: []VerificationRecord{{
			Position: 1,
			Check:    contract.CheckAny,
			Class:    agent.VerificationStatic,
		}},
	}
	suff := EvaluateSufficiency(shape, evidence, execution, false)
	state := RenderState(shape, evidence, execution, suff, CandidateSet{}, 4096, 12)
	if !strings.Contains(state, "STATIC (does not prove runtime behavior)") {
		t.Fatalf("state block does not name the static class:\n%s", state)
	}
}
