package cognition

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// regressionShape builds the contract and shape the production path builds for
// a user request.
func regressionShape(prompt string, facts RepositoryFacts) (contract.IntentContract, TaskShape) {
	return regressionShapeAs(prompt, facts, "ask")
}

func regressionShapeAs(prompt string, facts RepositoryFacts, authority string) (contract.IntentContract, TaskShape) {
	intent := workspace.BuildIntentContract(prompt)
	return intent, BuildShape(intent, ShapeParams{Authority: authority, Facts: facts})
}

func requireNeed(t *testing.T, shape TaskShape, key string, want bool) {
	t.Helper()
	if got := requirementKeys(shape.RequiredKnowledge)[key]; got != want {
		t.Fatalf("%q: need %q = %v, want %v; needs=%+v", shape.Objective, key, got, want, shape.RequiredKnowledge)
	}
}

// completionAfter mirrors the production completion boundary: the projector
// records the observations, produces the evidence review, and the evaluator
// consumes both. It is used for informational objectives.
func completionAfter(shape TaskShape, facts RepositoryFacts, outcomes []agent.ToolOutcome) agent.Completion {
	evidence := NewEvidenceState()
	projector := NewProjector(shape, evidence, facts)
	turns := []agent.Turn{{Results: outcomes}}
	result := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns:    turns,
		Tools:    []agent.Tool{testTool{name: "read", tier: agent.TierRead}},
	})
	turns = append(turns, agent.Turn{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "The answer is grounded in the observed evidence."}},
	}})
	return agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:          turns,
		Contract:       contract.IntentContract{Objective: shape.Objective, Kind: shape.Kind},
		EvidenceReview: result.Report.EvidenceReview(),
	})
}

func listingEvidence(id, path, text string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: id, ToolName: "ls", Args: map[string]any{"path": path},
		Result: agent.Result{Text: text, Details: map[string]any{"path": path, "entries": 2}},
	}
}

func symbolEvidence(id, symbol, path string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: id, ToolName: "symbols", Args: map[string]any{"symbol": symbol},
		Result: agent.Result{
			Text: symbol + " at " + path,
			Details: map[string]any{
				"coordinates": []map[string]any{{
					"path": path, "symbol": symbol, "start_line": 1, "end_line": 2,
				}},
			},
		},
	}
}

func writeEvidence(id, path string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: id, ToolName: "write", Args: map[string]any{"path": path},
		Result: agent.Result{Text: "wrote", Mutates: true, Details: map[string]any{
			"path": path, "bytes": 10, "diff": "changed",
		}},
	}
}

func verifyEvidence(id string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: id, ToolName: "bash", Args: map[string]any{"command": "go test ./..."},
		Result: agent.Result{
			Text: "ok", Executes: true, Finished: true, Verifies: true,
			Verification: &agent.VerificationEvidence{
				Check: contract.CheckTest, Scopes: []agent.VerificationScope{{All: true}},
			},
		},
	}
}

func executionOf(turns ...agent.Turn) ExecutionState { return SummarizeExecution(turns) }

// A repository concept is a targeted repository question: it needs observed
// content, not a literal path and not a whole-repository survey.
func TestConceptRequestShapeAndCompletion(t *testing.T) {
	facts := RepositoryFacts{Go: true}
	prompts := []string{
		"Explain how the checkout flow works.",
		"Understand how the checkout flow works.",
		"Check this project and understand how the checkout flow works.",
		"Check this Go e-commerce project and understand how the main order/checkout flow works.",
	}
	for _, prompt := range prompts {
		t.Run(prompt, func(t *testing.T) {
			intent, shape := regressionShape(prompt, facts)
			if len(intent.Targets) != 0 {
				t.Fatalf("concept acquired literal targets: %+v", intent.Targets)
			}
			if !shape.RepositoryScope {
				t.Fatal("concept request did not record repository scope")
			}
			if shape.BroadRepositoryAnalysis {
				t.Fatal("a targeted concept became a whole-repository survey")
			}
			requireNeed(t, shape, "repository_overview", true)
			requireNeed(t, shape, "repository_survey", false)
			for _, need := range shape.RequiredKnowledge {
				if len(need.Key) >= len("target_state:") && need.Key[:len("target_state:")] == "target_state:" {
					t.Fatalf("concept acquired literal target need %q", need.Key)
				}
			}

			empty := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
			if empty.State != Insufficient || empty.Stage != StageExplore {
				t.Fatalf("empty evidence = %s/%s, want INSUFFICIENT/explore", empty.State, empty.Stage)
			}
			if review := completionAfter(shape, facts, nil); review.Complete() {
				t.Fatal("an unobserved concept answer completed")
			}

			one := NewEvidenceState()
			one.ObserveTurns(shape, readTurns("order/controller.go"))
			if suff := EvaluateSufficiency(shape, one, ExecutionState{}, false); suff.State != Sufficient || suff.Stage != StageAnswer {
				t.Fatalf("one content read = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
			}
			if review := completionAfter(shape, facts, []agent.ToolOutcome{
				readEvidence("read-1", "order/controller.go"),
			}); !review.Complete() {
				t.Fatalf("a grounded concept answer did not complete: %s", review.Reason)
			}
		})
	}
}

// A literal file target is strict: a sibling read does not satisfy it, and the
// exact file read does.
func TestLiteralFileTargetStaysStrict(t *testing.T) {
	intent, shape := regressionShape("Read order/checkout.go", RepositoryFacts{Go: true})
	if len(intent.Targets) != 1 || intent.Targets[0].Kind != contract.TargetPath ||
		intent.Targets[0].Value != "order/checkout.go" {
		t.Fatalf("targets = %+v, want the literal file", intent.Targets)
	}
	requireNeed(t, shape, "target_state:order/checkout.go", true)
	requireNeed(t, shape, "target_content:order/checkout.go", true)

	if suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false); suff.State != Insufficient {
		t.Fatalf("empty evidence = %s, want INSUFFICIENT", suff.State)
	}
	sibling := NewEvidenceState()
	sibling.ObserveTurns(shape, readTurns("order/controller.go"))
	if suff := EvaluateSufficiency(shape, sibling, ExecutionState{}, false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("sibling read = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}
	if review := completionAfter(shape, RepositoryFacts{Go: true}, []agent.ToolOutcome{
		readEvidence("read-1", "order/controller.go"),
	}); review.Complete() {
		t.Fatal("a sibling read satisfied a literal file target")
	}

	exact := NewEvidenceState()
	exact.ObserveTurns(shape, readTurns("order/checkout.go"))
	if suff := EvaluateSufficiency(shape, exact, ExecutionState{}, false); suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("exact read = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
	if review := completionAfter(shape, RepositoryFacts{Go: true}, []agent.ToolOutcome{
		readEvidence("read-1", "order/checkout.go"),
	}); !review.Complete() {
		t.Fatalf("the exact file read did not complete: %s", review.Reason)
	}
}

// An explicit directory inspection needs that directory's contents. A parent
// listing establishes presence but not what the directory contains.
func TestLiteralDirectoryInspectionNeedsItsOwnListing(t *testing.T) {
	intent, shape := regressionShape("Inspect the order/checkout directory", RepositoryFacts{Go: true})
	if len(intent.Targets) != 1 || intent.Targets[0].Kind != contract.TargetPath ||
		intent.Targets[0].Value != "order/checkout" {
		t.Fatalf("targets = %+v, want the literal directory", intent.Targets)
	}
	requireNeed(t, shape, "target_state:order/checkout", true)
	requireNeed(t, shape, "target_content:order/checkout", true)

	parent := NewEvidenceState()
	parent.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{
		listingEvidence("ls-parent", "order", "checkout\ncheckout.go"),
	}}})
	if suff := EvaluateSufficiency(shape, parent, ExecutionState{}, false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("parent listing = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}
	if review := completionAfter(shape, RepositoryFacts{Go: true}, []agent.ToolOutcome{
		listingEvidence("ls-parent", "order", "checkout\ncheckout.go"),
	}); review.Complete() {
		t.Fatal("a parent listing satisfied a directory inspection")
	}

	own := NewEvidenceState()
	own.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{
		listingEvidence("ls-own", "order/checkout", "controller.go\nservice.go"),
	}}})
	if suff := EvaluateSufficiency(shape, own, ExecutionState{}, false); suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("own listing = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
	if review := completionAfter(shape, RepositoryFacts{Go: true}, []agent.ToolOutcome{
		listingEvidence("ls-own", "order/checkout", "controller.go\nservice.go"),
	}); !review.Complete() {
		t.Fatalf("the directory's own listing did not complete: %s", review.Reason)
	}
}

// The broad survey policy is unchanged: one observation is not an overview.
func TestBroadSurveyStillRequiresASurvey(t *testing.T) {
	_, shape := regressionShape("Explain this project.", RepositoryFacts{Go: true})
	requireNeed(t, shape, "repository_overview", true)
	requireNeed(t, shape, "repository_survey", true)

	empty := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	if empty.State != Insufficient || empty.Stage != StageExplore {
		t.Fatalf("empty evidence = %s/%s, want INSUFFICIENT/explore", empty.State, empty.Stage)
	}
	one := NewEvidenceState()
	one.ObserveTurns(shape, readTurns("README.md"))
	if suff := EvaluateSufficiency(shape, one, ExecutionState{}, false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("one read = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}
	if review := completionAfter(shape, RepositoryFacts{Go: true}, []agent.ToolOutcome{
		readEvidence("read-1", "README.md"),
	}); review.Complete() {
		t.Fatal("one read completed a broad survey")
	}

	two := []agent.ToolOutcome{
		readEvidence("read-1", "README.md"),
		readEvidence("read-2", "main.go"),
	}
	state := NewEvidenceState()
	state.ObserveTurns(shape, []agent.Turn{{Results: two}})
	if suff := EvaluateSufficiency(shape, state, ExecutionState{}, false); suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("two reads = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
	if review := completionAfter(shape, RepositoryFacts{Go: true}, two); !review.Complete() {
		t.Fatalf("a surveyed answer did not complete: %s", review.Reason)
	}
}

// A named symbol remains a coordinate requirement.
func TestNamedSymbolTargetStillRequiresCoordinates(t *testing.T) {
	_, shape := regressionShape("Who calls `parseConfig`?", RepositoryFacts{Go: true})
	requireNeed(t, shape, "target_coordinates:parseConfig", true)

	if suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("empty evidence = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}
	state := NewEvidenceState()
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{
		symbolEvidence("symbols-1", "parseConfig", "internal/config/config.go"),
	}}})
	if suff := EvaluateSufficiency(shape, state, ExecutionState{}, false); suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("symbol observation = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
	if review := completionAfter(shape, RepositoryFacts{Go: true}, []agent.ToolOutcome{
		symbolEvidence("symbols-1", "parseConfig", "internal/config/config.go"),
	}); !review.Complete() {
		t.Fatalf("the symbol answer did not complete: %s", review.Reason)
	}
}

// An execution objective still needs execution evidence, not a static read.
func TestExecutionObjectiveStillRequiresExecutionEvidence(t *testing.T) {
	intent, shape := regressionShapeAs("Run the tests.", RepositoryFacts{Go: true}, "build")
	requireNeed(t, shape, "execution_result", true)
	requireNeed(t, shape, "verification", true)

	readTurn := agent.Turn{Results: readTurns("README.md")[0].Results}
	if suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("empty evidence = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}
	readState := NewEvidenceState()
	readState.ObserveTurns(shape, []agent.Turn{readTurn})
	if suff := EvaluateSufficiency(shape, readState, executionOf(readTurn), false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("static read = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}

	verifyTurn := agent.Turn{Results: []agent.ToolOutcome{verifyEvidence("verify-1")}}
	verified := NewEvidenceState()
	verified.ObserveTurns(shape, []agent.Turn{verifyTurn})
	if suff := EvaluateSufficiency(shape, verified, executionOf(verifyTurn), false); suff.State != Sufficient || suff.Stage != StageComplete {
		t.Fatalf("verified execution = %s/%s, want SUFFICIENT/complete", suff.State, suff.Stage)
	}

	answer := agent.Turn{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "The tests passed."}},
	}}
	if review := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    []agent.Turn{verifyTurn, answer},
		Contract: intent,
	}); !review.Complete() {
		t.Fatalf("verified execution did not complete: %s", review.Reason)
	}
	if review := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    []agent.Turn{readTurn, answer},
		Contract: intent,
	}); review.Complete() {
		t.Fatal("a static read completed an execution objective")
	}
}

// A FUZZ investigation may execute to acquire evidence, but a successful
// command is observation, not the deliverable. Treating the objective as an
// execution contract made the command move the shape to complete and withdrew
// every tool while the model was still investigating.
func TestInvestigationExecutionDoesNotCloseTheSurface(t *testing.T) {
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "bash", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}},
	}
	cases := []struct {
		name   string
		prompt string
		facts  RepositoryFacts
		turns  []agent.Turn
	}{
		{
			name:   "unrelated successful command",
			prompt: "investigate the bottleneck and evaluate",
			turns: []agent.Turn{{Results: []agent.ToolOutcome{{
				CallID: "log-1", ToolName: "bash", Args: map[string]any{"command": "git log --oneline -10"},
				Result: agent.Result{Text: "abc123 commit", Executes: true, Finished: true},
			}}}},
		},
		{
			name:   "repository overview is not the probe",
			prompt: "Investigate why this test is failing.",
			facts:  RepositoryFacts{Go: true},
			turns:  []agent.Turn{{Results: []agent.ToolOutcome{readEvidence("read-calc", "calc.go")}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent, shape := regressionShapeAs(tc.prompt, tc.facts, "fuzz")
			if intent.Kind != contract.ObjectiveInformational {
				t.Fatalf("contract kind = %s, want informational", intent.Kind)
			}
			if !shape.Investigation {
				t.Fatalf("shape did not record the investigation: %+v", shape.Intent)
			}
			if !shape.NeedsExecution || !shape.Authority.ExecutionAllowed {
				t.Fatalf("the investigation lost its executable surface: needsExecution=%v authority=%s",
					shape.NeedsExecution, shape.Authority.Workspace)
			}
			projector := NewProjector(shape, NewEvidenceState(), tc.facts)
			projected := projector.Project(agent.ProjectionRequest{
				Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
				Turns:    tc.turns,
				Tools:    tools,
			})
			if projected.Report.AnswerEligible || projected.Report.Terminal {
				t.Fatalf("the investigation was closed before the probe: answerEligible=%v terminal=%v",
					projected.Report.AnswerEligible, projected.Report.Terminal)
			}
			if names := toolNames(projected.Tools); strings.Join(names, ",") != "read,bash" {
				t.Fatalf("offered tools = %v, want the investigation surface to remain open", names)
			}
		})
	}
}

// The loop sets ForceAnswer after repeated refusal-only turns. It must close a
// surface the investigation shape would otherwise keep open, and the state
// block must say that only an answer can make progress.
func TestForceAnswerClosesAnOpenInvestigationSurface(t *testing.T) {
	intent, shape := regressionShapeAs("investigate the bottleneck and evaluate", RepositoryFacts{}, "fuzz")
	if intent.Kind != contract.ObjectiveInformational || !shape.Investigation {
		t.Fatalf("shape = %s investigation=%v, want an investigation", intent.Kind, shape.Investigation)
	}
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{})
	projected := projector.Project(agent.ProjectionRequest{
		Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{{
			CallID: "log-1", ToolName: "bash", Args: map[string]any{"command": "git log --oneline -10"},
			Result: agent.Result{Text: "abc123 commit", Executes: true, Finished: true},
		}}}},
		Tools: []agent.Tool{
			testTool{name: "read", tier: agent.TierRead},
			testTool{name: "bash", tier: agent.TierExec},
		},
		ForceAnswer: true,
	})
	if len(projected.Tools) != 0 {
		t.Fatalf("offered tools = %v, want the forced-answer surface closed", toolNames(projected.Tools))
	}
	if !projected.Report.AnswerEligible {
		t.Fatal("the forced-answer projection did not mark the answer boundary")
	}
	var state strings.Builder
	for _, block := range projected.System {
		state.WriteString(block.Text)
	}
	if !strings.Contains(state.String(), "no tool is available") {
		t.Fatalf("state block does not tell the model to answer:\n%s", state.String())
	}
}

// A mutation objective still needs an observed change and a successful check.
func TestMutationObjectiveStillRequiresChangeAndVerification(t *testing.T) {
	intent, shape := regressionShapeAs("Fix the failing tests.", RepositoryFacts{Go: true}, "build")
	requireNeed(t, shape, "repository_overview", true)
	requireNeed(t, shape, "verification", true)

	if suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("empty evidence = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}

	readTurn := agent.Turn{Results: readTurns("tests/cart_test.go")[0].Results}
	readState := NewEvidenceState()
	readState.ObserveTurns(shape, []agent.Turn{readTurn})
	if suff := EvaluateSufficiency(shape, readState, executionOf(readTurn), false); suff.State != Sufficient || suff.Stage != StageAct {
		t.Fatalf("pre-change evidence = %s/%s, want SUFFICIENT/act", suff.State, suff.Stage)
	}

	changeTurn := agent.Turn{Results: []agent.ToolOutcome{writeEvidence("write-1", "tests/cart_test.go")}}
	changed := NewEvidenceState()
	changed.ObserveTurns(shape, []agent.Turn{readTurn, changeTurn})
	if suff := EvaluateSufficiency(shape, changed, executionOf(readTurn, changeTurn), false); suff.State != Insufficient || suff.Stage != StageVerify {
		t.Fatalf("change without check = %s/%s, want INSUFFICIENT/verify", suff.State, suff.Stage)
	}

	verifyTurn := agent.Turn{Results: []agent.ToolOutcome{verifyEvidence("verify-1")}}
	proven := NewEvidenceState()
	proven.ObserveTurns(shape, []agent.Turn{readTurn, changeTurn, verifyTurn})
	if suff := EvaluateSufficiency(shape, proven, executionOf(readTurn, changeTurn, verifyTurn), false); suff.State != Sufficient || suff.Stage != StageComplete {
		t.Fatalf("change and check = %s/%s, want SUFFICIENT/complete", suff.State, suff.Stage)
	}

	answer := agent.Turn{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "The failing test is fixed and the suite passes."}},
	}}
	if review := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    []agent.Turn{readTurn, changeTurn, verifyTurn, answer},
		Contract: intent,
	}); !review.Complete() {
		t.Fatalf("a changed and verified mutation did not complete: %s", review.Reason)
	}
	if review := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns:    []agent.Turn{readTurn, changeTurn, answer},
		Contract: intent,
	}); review.Complete() {
		t.Fatal("a change without verification completed")
	}

	// A literal mutation target keeps the same change + verification rule.
	_, noteShape := regressionShapeAs("Update note.txt.", RepositoryFacts{Go: true}, "build")
	requireNeed(t, noteShape, "target_state:note.txt", true)
	requireNeed(t, noteShape, "verification", true)
	if suff := EvaluateSufficiency(noteShape, NewEvidenceState(), ExecutionState{}, false); suff.State != Insufficient || suff.Stage != StageExplore {
		t.Fatalf("literal mutation with no evidence = %s/%s, want INSUFFICIENT/explore", suff.State, suff.Stage)
	}
	noteRead := agent.Turn{Results: []agent.ToolOutcome{readEvidence("read-note", "note.txt")}}
	noteState := NewEvidenceState()
	noteState.ObserveTurns(noteShape, []agent.Turn{noteRead})
	if suff := EvaluateSufficiency(noteShape, noteState, executionOf(noteRead), false); suff.State != Sufficient || suff.Stage != StageAct {
		t.Fatalf("literal mutation with pre-change evidence = %s/%s, want SUFFICIENT/act", suff.State, suff.Stage)
	}
	noteWrite := agent.Turn{Results: []agent.ToolOutcome{writeEvidence("write-note", "note.txt")}}
	noteChanged := NewEvidenceState()
	noteChanged.ObserveTurns(noteShape, []agent.Turn{noteRead, noteWrite})
	if suff := EvaluateSufficiency(noteShape, noteChanged, executionOf(noteRead, noteWrite), false); suff.State != Insufficient || suff.Stage != StageVerify {
		t.Fatalf("literal mutation without check = %s/%s, want INSUFFICIENT/verify", suff.State, suff.Stage)
	}
}
