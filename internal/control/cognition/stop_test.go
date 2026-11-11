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

// A repeated observation with required evidence still missing is a stalled
// path, not a finished investigation. The runtime must keep the surface open
// and escalate with the exact gap; closing it would force an answer that the
// completion boundary has to reject.
func TestProjectorEscalatesWhenATurnAddsNoEvidenceButRequirementsRemain(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}, ShapeParams{Authority: "ask"})

	first := readTurn("c1", ".github/workflows/telemetry.yml", "1\ton: schedule")
	projected := informationalProjection(t, shape, []agent.Turn{first})
	if len(projected.Tools) == 0 {
		t.Fatal("the first observation was stopped before it produced evidence")
	}

	// The identical observation again, with a new call identity.
	duplicate := readTurn("c2", ".github/workflows/telemetry.yml", "1\ton: schedule")
	projected = informationalProjection(t, shape, []agent.Turn{first, duplicate})
	if len(projected.Tools) == 0 {
		t.Fatalf("a stalled turn closed the surface while evidence was missing: %+v", projected.Report.Excluded)
	}
	if projected.Report.AnswerEligible {
		t.Fatal("a stalled, insufficient turn was declared answer-eligible")
	}
	if projected.Report.Sufficiency != string(Insufficient) {
		t.Fatalf("sufficiency = %s, want INSUFFICIENT", projected.Report.Sufficiency)
	}
	state := ""
	for _, block := range projected.System {
		state += block.Text
	}
	for _, want := range []string{"no new evidence", "repository_survey", "Next evidence"} {
		if !strings.Contains(state, want) {
			t.Fatalf("the runtime state does not explain the escalation with %q:\n%s", want, state)
		}
	}
}

// Once the required evidence exists, the same informational objective closes:
// the next request is the answer, not another observation. This is the stop
// that must stay cheap for an ordinary repository question.
func TestProjectorStopsOnceRequiredEvidenceIsKnown(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}, ShapeParams{Authority: "ask"})

	turns := []agent.Turn{
		{Results: []agent.ToolOutcome{readTurn("c1", "README.md", "1\t# Project").Results[0]}},
		{Results: []agent.ToolOutcome{readTurn("c2", "main.go", "1\tpackage main").Results[0]}},
	}
	projected := informationalProjection(t, shape, turns)
	if projected.Report.Sufficiency != string(Sufficient) {
		t.Fatalf("sufficiency = %s, want SUFFICIENT", projected.Report.Sufficiency)
	}
	if len(projected.Tools) != 0 {
		t.Fatalf("a sufficient answer still offered %d tool(s)", len(projected.Tools))
	}
	if !projected.Report.AnswerEligible {
		t.Fatal("a sufficient repository answer was not answer-eligible")
	}
	if len(projected.Report.Excluded) == 0 {
		t.Fatal("the evidence-complete stop was not audited")
	}
}

// A read the runtime cannot attribute to a repository path recorded no usable
// evidence. It must not be counted as an observation, because "observed but not
// novel" is exactly the signal that withholds every tool and asks for an answer.
func TestUnattributableReadIsNotEvidenceGain(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}, ShapeParams{Authority: "ask"})

	// The read succeeded, but the metadata names an absolute path outside the
	// repository-relative vocabulary. Nothing can be attributed.
	first := readTurn("c1", "/tmp/workspace/README.md", "# project")
	state := NewEvidenceState()
	observed, novel := state.ObserveTurns(shape, []agent.Turn{first})
	if observed != 0 || novel != 0 {
		t.Fatalf("unattributable read counted as evidence: observed=%d novel=%d", observed, novel)
	}

	duplicate := readTurn("c2", "/tmp/workspace/README.md", "# project")
	projected := informationalProjection(t, shape, []agent.Turn{first, duplicate})
	if len(projected.Tools) == 0 {
		t.Fatalf("an unattributable read triggered the answer-only stop: %+v", projected.Report.Excluded)
	}
}

// A different range of the same file is new information, so it must not be
// mistaken for a repeat.
func TestProjectorKeepsToolsWhenTheObservationAddsAFact(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
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

// A declared literal target has required evidence even when the request is not
// a repository-wide question. Once that evidence is known, the target answer is
// as complete as a repository overview: keeping tools open would be waste.
func TestProjectorClosesADeclaredTargetAnswer(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Read note.txt",
		Targets: []contract.Target{{
			Kind: contract.TargetPath, Value: "note.txt",
		}},
	}, ShapeParams{Authority: "ask"})

	first := readTurn("c1", "note.txt", "1\tcontents")
	projected := informationalProjection(t, shape, []agent.Turn{first})
	if projected.Report.Sufficiency != string(Sufficient) {
		t.Fatalf("sufficiency = %s, want SUFFICIENT", projected.Report.Sufficiency)
	}
	if len(projected.Tools) != 0 {
		t.Fatalf("a known literal target still offered %d tool(s)", len(projected.Tools))
	}
}

// A resumed task may begin by re-reading a file it already knows. The run's
// first observation turn is context re-establishment, not wasted work, so it is
// not judged as stagnation.
func TestProjectorDoesNotStopOnTheFirstTurnOfARun(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
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
		Objective: "check this project and analyze what this project is about?",
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

// A resumed run has no live turns, but its evidence still carries the change
// the previous run applied. Reconstructing the execution state from evidence
// is what keeps the task in verification instead of treating it as unstarted.
func TestExecutionFromEvidenceKeepsPriorChangePendingVerification(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	evidence := NewEvidenceState()
	evidence.ObserveOutcome(writeEvidence("write-1", "README.md"))

	execution := ExecutionFromEvidence(evidence)
	if !execution.Mutated || execution.VerifiedAfterMutation {
		t.Fatalf("durable execution = %+v, want a change without verification", execution)
	}
	if suff := EvaluateSufficiency(shape, evidence, execution, false); suff.Stage != StageVerify {
		t.Fatalf("stage = %s, want verify", suff.Stage)
	}

	evidence.ObserveOutcome(verifyEvidence("check-1"))
	execution = ExecutionFromEvidence(evidence)
	if !execution.VerifiedAfterMutation {
		t.Fatalf("durable execution = %+v, want the check after the change", execution)
	}
	if suff := EvaluateSufficiency(shape, evidence, execution, false); suff.Stage != StageComplete {
		t.Fatalf("stage = %s, want complete", suff.Stage)
	}
}

// Four consecutive observations that do not advance a change waiting for its
// check are a semantic stop, not a reason to spend the rest of the turn grant.
// The run stops before the resource cap and reports the missing verification.
func TestProjectorStopsAfterConsecutiveNoProgressInVerification(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	evidence := NewEvidenceState()
	evidence.ObserveOutcome(writeEvidence("write-1", "README.md"))
	projector := NewProjector(shape, evidence, RepositoryFacts{Go: true})
	tools := []agent.Tool{testTool{name: "read", tier: agent.TierRead}}

	request := func(turns []agent.Turn) agent.ProjectionResult {
		return projector.Project(agent.ProjectionRequest{
			Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
			Turns:    turns,
			Tools:    tools,
			System:   []ai.SystemPromptBlock{{Text: "base"}},
		})
	}

	var turns []agent.Turn
	first := request(turns)
	if first.Report.Stage != string(StageVerify) {
		t.Fatalf("first projection stage = %s, want verify", first.Report.Stage)
	}
	if first.Report.Terminal || len(first.Tools) == 0 {
		t.Fatalf("the first projection stopped before it could verify: terminal=%t tools=%d",
			first.Report.Terminal, len(first.Tools))
	}

	var projected agent.ProjectionResult
	for turn := 0; turn < MaxConsecutiveNoProgress; turn++ {
		turns = append(turns, readTurn("read-"+itoa(turn), "README.md", "1\t# Project"))
		projected = request(turns)
	}
	if !projected.Report.Terminal {
		t.Fatalf("the run did not stop after %d no-progress turns: %+v",
			MaxConsecutiveNoProgress, projected.Report)
	}
	if len(projected.Tools) != 0 {
		t.Fatalf("the stalled projection still offered %d tool(s)", len(projected.Tools))
	}
	if !strings.Contains(projected.Report.StopReason, "no meaningful task progress") {
		t.Fatalf("stop reason = %q, want the semantic no-progress reason", projected.Report.StopReason)
	}
	if projected.Report.Progress != "stalled" || projected.Report.NoProgressTurns < MaxConsecutiveNoProgress {
		t.Fatalf("progress audit = %s/%d, want stalled/%d",
			projected.Report.Progress, projected.Report.NoProgressTurns, MaxConsecutiveNoProgress)
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

// An explicitly directory-scoped prompt reaches the answer stage once the
// directory has been listed and the named file has been read. The directory
// target must not demand file bytes it can never have.
func TestDirectoryScopedPromptReachesAnswerAfterListingAndFileRead(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Use ls to inspect the project root and backend/, read backend/go.mod, then tell me the module name.",
		Targets: []contract.Target{
			{Kind: contract.TargetPath, Value: "backend/"},
			{Kind: contract.TargetPath, Value: "backend/go.mod"},
		},
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true}})

	turns := []agent.Turn{{Results: []agent.ToolOutcome{
		{
			CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
			Result: agent.Result{Text: "backend\nREADME.md", Details: map[string]any{"path": ".", "entries": 2}},
		},
		{
			CallID: "ls-2", ToolName: "ls", Args: map[string]any{"path": "backend"},
			Result: agent.Result{Text: "cmd\ninternal\ngo.mod", Details: map[string]any{"path": "backend", "entries": 3}},
		},
		readTurn("read-1", "backend/go.mod", "1\tmodule ecommerce").Results[0],
	}}}

	projected := informationalProjection(t, shape, turns)
	if projected.Report.Sufficiency != string(Sufficient) {
		t.Fatalf("sufficiency = %s (%s), want SUFFICIENT; excluded=%+v",
			projected.Report.Sufficiency, projected.Report.Evidence, projected.Report.Excluded)
	}
	if !projected.Report.AnswerEligible {
		t.Fatal("the answer stage was not reachable after the directory listing and file read")
	}
}
