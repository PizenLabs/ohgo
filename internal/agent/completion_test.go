package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
)

func toolOutcome(name string, result Result) ToolOutcome {
	if result.Details == nil {
		switch name {
		case "write", "edit", "delete":
			// Production file tools carry the authorized path. A fixture
			// without one is not a pathless shell mutation, so keep the
			// attribution evidence realistic.
			result.Details = map[string]any{"path": "note.txt"}
		}
	}
	return ToolOutcome{ToolName: name, Result: result}
}

func cleanStop() Turn { return Turn{Assistant: ai.AssistantMessage{}} }

// answerTurn is a clean stop that carries visible assistant text. Informational
// completion requires an answer, not just a stopped model.
func answerTurn(text string) Turn {
	return Turn{Assistant: ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}}
}

func TestEvaluateCompletionRejectsMutationWithoutVerification(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("edit", Result{Text: "edited", Mutates: true})}},
		cleanStop(),
	}
	verdict := EvaluateCompletion(turns, true)
	if verdict.Complete() {
		t.Fatal("a mutation with no verification was reported complete")
	}
	if verdict.Reason == "" {
		t.Fatal("a NEEDS_REVIEW verdict has no evidence reason")
	}
}

func TestEvaluateCompletionRejectsMissingMutation(t *testing.T) {
	// The model says it is done and the todo list is empty, but the request
	// asked for a change and the runtime observed none.
	verdict := EvaluateCompletion([]Turn{cleanStop()}, true)
	if verdict.Complete() {
		t.Fatal("a mutation objective with no observed change was reported complete")
	}
}

func TestEvaluateCompletionRejectsUnexpectedMutation(t *testing.T) {
	// A read-only or execution-only run that changed the repository is not
	// complete even if the model produced a clean final answer. The workspace
	// may have authorized the call; the objective did not.
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		answerTurn("done"),
	}
	if verdict := EvaluateCompletion(turns, false); verdict.Complete() {
		t.Fatal("a read-only run that mutated was reported complete")
	}
}

// "Run this project" is not proved by a command that finished. The probe is
// real execution, but it is not the requested execution; only a runtime that
// reached readiness and answered a check while alive can complete the
// objective. The failure this guards against reported PROVEN after "node -v"
// and then refused the user's "run it" as redundant.
func TestRuntimeExecutionRequiresLiveRuntimeObservation(t *testing.T) {
	intent := contract.IntentContract{
		Objective:     "run this project help me",
		Kind:          contract.ObjectiveExecution,
		ExecutionMode: contract.ExecutionRuntime,
	}
	probe := []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{
			Text:     "v24.11.0\n11.6.1",
			Executes: true,
			Finished: true,
			Details:  map[string]any{"command": "node -v && npm -v", "exitCode": 0},
		})}},
		answerTurn("Run npm run dev to start the project."),
	}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: probe, Contract: intent})
	if verdict.Complete() {
		t.Fatal("a finished environment probe completed a runtime objective")
	}
	if !strings.Contains(verdict.Reason, "live runtime") {
		t.Fatalf("reason = %q, want it to name the missing live runtime", verdict.Reason)
	}

	ran := []Turn{
		{Results: []ToolOutcome{toolOutcome("verify", Result{
			Text:         "verification: PASS\nservice: npm run dev\n- http: PASS status 200",
			Executes:     true,
			Finished:     true,
			Verifies:     true,
			Verification: &VerificationEvidence{Command: "npm run dev"},
			Runtime: &RuntimeEvidence{
				Command: "npm run dev",
				URL:     "http://127.0.0.1:3000",
				PID:     4242,
				Checks:  1,
			},
		})}},
		answerTurn("The project is serving at http://127.0.0.1:3000."),
	}
	if verdict := EvaluateTaskCompletion(CompletionInput{Turns: ran, Contract: intent}); !verdict.Complete() {
		t.Fatalf("runtime completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

// A malformed call emitted after the proof is a harness contract error, not an
// observation. It never executed, so it must not downgrade a runtime proof that
// real evidence already supports. A genuine execution failure in the same
// position still must, because that is evidence about the project.
func TestLateMalformedCallDoesNotDowngradeRuntimeProof(t *testing.T) {
	intent := contract.IntentContract{
		Objective:     "run this project help me",
		Kind:          contract.ObjectiveExecution,
		ExecutionMode: contract.ExecutionRuntime,
	}
	runtimeTurn := Turn{Results: []ToolOutcome{toolOutcome("verify", Result{
		Text:         "verification: PASS",
		Executes:     true,
		Finished:     true,
		Verifies:     true,
		Verification: &VerificationEvidence{Command: "npm run dev"},
		Runtime:      &RuntimeEvidence{Command: "npm run dev", URL: "http://127.0.0.1:3000", PID: 4242, Checks: 1},
	})}}
	lateCall := func(result Result) Turn {
		return Turn{
			Assistant: ai.AssistantMessage{
				Content: []ai.ContentBlock{
					ai.TextContent{Text: "The project is running."},
					ai.ToolCall{ID: "late-1", Name: "verify", Arguments: map[string]any{"checks": []any{}}},
				},
				StopReason: ai.StopToolUse,
			},
			Results: []ToolOutcome{toolOutcome("verify", result)},
		}
	}

	malformed := lateCall(Result{
		Text:         `Invalid arguments for "verify": /checks/0/contains: must be of type array`,
		IsError:      true,
		FailureClass: ToolFailureInvalidArgument,
	})
	if verdict := EvaluateTaskCompletion(CompletionInput{Turns: []Turn{runtimeTurn, malformed}, Contract: intent}); !verdict.Complete() {
		t.Fatalf("a malformed continuation downgraded the runtime proof: %s", verdict.Reason)
	}

	failed := lateCall(Result{
		Text:         "verification: FAIL: the service exited during the check",
		IsError:      true,
		FailureClass: ToolFailureVerification,
	})
	if verdict := EvaluateTaskCompletion(CompletionInput{Turns: []Turn{runtimeTurn, failed}, Contract: intent}); verdict.Complete() {
		t.Fatal("a later failed verification was ignored")
	}
}

func TestInformationalPathTargetDoesNotRequireVerification(t *testing.T) {
	// Naming a Go file in a question does not make the answer a checked
	// change. Before this rule, "what package is declared in tui/x.go?"
	// required a build or test that the task never asked for.
	contract := contract.IntentContract{
		Objective: "What package is declared in tui/approval.go?",
		Kind:      contract.ObjectiveInformational,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "tui/approval.go"}},
	}
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{
			Text:    "1\tpackage tui",
			Details: map[string]any{"path": "tui/approval.go", "totalLines": 1, "offset": 1, "limit": 2000},
		})}},
		answerTurn("tui"),
	}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: contract})
	if !verdict.Complete() {
		t.Fatalf("informational completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

func TestEvidenceReviewVetoesAnAnswerWithoutEvidence(t *testing.T) {
	// A model can always emit a plausible sentence. The cognitive layer's
	// sufficiency verdict is the runtime's statement that the sentence was
	// produced without the evidence the task required.
	contract := contract.IntentContract{
		Objective: "Explain this project.",
		Kind:      contract.ObjectiveInformational,
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:          []Turn{answerTurn("It is a Go project.")},
		Contract:       contract,
		EvidenceReview: "runtime evidence was not sufficient for the objective (INSUFFICIENT) at stage explore",
	})
	if verdict.Complete() {
		t.Fatal("an answer with an insufficient evidence review was reported complete")
	}
}

func TestBudgetCompletionKeepsProvenEvidenceAuthoritative(t *testing.T) {
	// A resource stop removes the closing model turn, but not the proof. The
	// cognitive review from the pre-verification request must not undo a
	// definite change followed by a successful check.
	contract := contract.IntentContract{
		Objective:           "Change note.txt and verify it.",
		Kind:                contract.ObjectiveMutation,
		BehavioralObjective: true,
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyAny,
		}},
	}
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
	}
	verdict := EvaluateBudgetCompletion(CompletionInput{
		Turns:          turns,
		Contract:       contract,
		EvidenceReview: "runtime evidence was not sufficient for the objective (INSUFFICIENT) at stage verify",
	}, true)
	if !verdict.Complete() {
		t.Fatalf("proven budget completion = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

func TestEvaluateCompletionRejectsFailedVerification(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "FAIL", IsError: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a failed verification was reported complete")
	}
}

// A failed check is an open obligation, not a permanent one. A corrective
// change followed by a successful check resolves it: the earlier failure stays
// in the timeline, but it no longer blocks completion.
func TestEvaluateCompletionResolvesAFailedVerificationAfterCorrection(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "FAIL: duplicate key", IsError: true})}},
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "corrected", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		cleanStop(),
	}
	if verdict := EvaluateCompletion(turns, true); !verdict.Complete() {
		t.Fatalf("a resolved verification failure = %s (%s), want COMPLETED", verdict.State, verdict.Reason)
	}
}

func TestEvaluateCompletionAcceptsMutationThenVerification(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		cleanStop(),
	}
	if !EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a verified mutation was not reported complete")
	}
}

func TestEvaluateCompletionRejectsVerificationBeforeMutation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a verification that preceded the change was accepted")
	}
}

func TestEvaluateCompletionRejectsUnrecoveredToolFailure(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("edit", Result{Text: "no match", IsError: true})}},
		cleanStop(),
	}
	completion := EvaluateCompletion(turns, false)
	if completion.Complete() {
		t.Fatal("a failed tool call followed by a clean stop was reported complete")
	}
	if !strings.Contains(completion.Reason, "edit") {
		t.Fatalf("review reason does not name the failing tool: %q", completion.Reason)
	}
}

func TestEvaluateCompletionAcceptsRetriedTool(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "missing", IsError: true})}},
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "contents"})}},
		cleanStop(),
	}
	if !EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a failure recovered by a successful retry was not reported complete")
	}
}

func TestEvaluateCompletionRejectsCancellation(t *testing.T) {
	turns := []Turn{{Err: context.Canceled}}
	if EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a cancelled run was reported complete")
	}

	// A tool-level cancellation is an error outcome, not a clean stop.
	turns = []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "command cancelled", IsError: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a cancelled tool followed by a clean stop was reported complete")
	}
}

func TestEvaluateCompletionRejectsOutstandingToolCalls(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a run stopped mid-tool-batch was reported complete")
	}
}

func TestEvaluateCompletionAcceptsReadOnlyCleanStop(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "contents"})}},
		cleanStop(),
	}
	if !EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a clean read-only run was not reported complete")
	}
}

func TestEvaluateCompletionRequiresVerificationAfterTheLastMutation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		{Results: []ToolOutcome{toolOutcome("edit", Result{Text: "edited", Mutates: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a change after the last verification was reported complete")
	}
}

func TestEvaluateCompletionRequiresVerificationAfterPotentialMutation(t *testing.T) {
	// An arbitrary shell command in a mutation objective may have changed the
	// repository even when the classifier did not prove it. A later read-only
	// exec is not verification, so the task is reviewable.
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ran", Executes: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a potential mutation after the last verification was reported complete")
	}
}

func TestEvaluateCompletionRejectsMutationTaskThatOnlyRanTests(t *testing.T) {
	// Running tests is verification, not a change. A mutation objective with
	// no definite change is still unmet, however clean the test run was.
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Executes: true, Verifies: true})}},
		cleanStop(),
	}
	if EvaluateCompletion(turns, true).Complete() {
		t.Fatal("a mutation objective with no observed change was reported complete")
	}
}

func TestEvaluateCompletionAcceptsReadOnlyExecution(t *testing.T) {
	// A read-only task that runs a process is not forced to verify a change it
	// did not request.
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "output", Executes: true})}},
		cleanStop(),
	}
	if !EvaluateCompletion(turns, false).Complete() {
		t.Fatal("a clean read-only execution was not reported complete")
	}
}

func TestCompletionEvidenceCarriesDelegatedMutation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("write", Result{Text: "wrote", Mutates: true})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{Text: "ok", Verifies: true})}},
		cleanStop(),
	}
	mutated, verified := CompletionEvidence(turns, true)
	if !mutated || !verified {
		t.Fatalf("evidence = (%v, %v), want (true, true)", mutated, verified)
	}

	turns[1].Results = nil
	mutated, verified = CompletionEvidence(turns, true)
	if !mutated || verified {
		t.Fatalf("unverified evidence = (%v, %v), want (true, false)", mutated, verified)
	}
}

func TestCompletionEvidenceCountsAuthorizedMutation(t *testing.T) {
	// A shell command classified as mutating carries the authorization flag,
	// even when the tool result itself does not set Mutates.
	turns := []Turn{
		{Results: []ToolOutcome{{
			ToolName:      "bash",
			Result:        Result{Text: "ok"},
			Authorization: &security.Decision{Mutates: true},
		}}},
		cleanStop(),
	}
	mutated, _ := CompletionEvidence(turns, false)
	if !mutated {
		t.Fatal("an authorized mutation was not counted as evidence")
	}
}

func TestLaterToolSuccessSupersedesAllEarlierFailures(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{
			toolOutcome("read", Result{Text: "read a: no such file", IsError: true}),
			toolOutcome("read", Result{Text: "read b: no such file", IsError: true}),
		}},
		{Results: []ToolOutcome{
			toolOutcome("read", Result{Text: "README.md", Details: map[string]any{
				"path": "README.md", "totalLines": 1, "offset": 1, "limit": 2000,
			}}),
		}},
		answerTurn("README.md describes the project."),
	}
	got := EvaluateTaskCompletion(CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "Explain this project.",
		},
	})
	if !got.Complete() {
		t.Fatalf("a later success did not recover the earlier failures: %s", got.Reason)
	}
}

// A change artifact is a model-authored reference, not an observed mutation.
// It must never satisfy a mutation objective: the artifact tool records
// metadata, so a run that only files one has not changed the repository.
func TestChangeArtifactIsNotAnObservedMutation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{
			toolOutcome("artifact", Result{Text: "recorded change change-1234"}),
		}},
		answerTurn("All four design steps are complete."),
	}
	got := EvaluateTaskCompletion(CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Objective: "redesign the portfolio site",
			Kind:      contract.ObjectiveMutation,
			Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "index.html"}},
			Verification: []contract.VerificationRequirement{{
				Kind: contract.VerifyAny,
			}},
		},
		Root: t.TempDir(),
	})
	if got.Complete() {
		t.Fatal("a recorded change artifact completed a mutation objective")
	}
	if !strings.Contains(got.Reason, "no change was observed") {
		t.Fatalf("reason = %q, want the missing observed change", got.Reason)
	}
}

func TestRefusedCallsAreNotOutstandingWork(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{
			toolOutcome("read", Result{Text: "Tool \"read\" is not offered", IsError: true, CandidateRejected: true}),
		}, Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "The project is a small HTTP server."},
		}}},
	}
	got := EvaluateTaskCompletion(CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "Explain this project.",
		},
	})
	if strings.Contains(got.Reason, "outstanding") {
		t.Fatalf("a refused call was counted as outstanding work: %s", got.Reason)
	}
}

// A tool-level authority refusal is a control decision, not an execution
// failure. A model that proposes the wrong artifact kind under FUZZ and then
// answers from successful investigation must not be vetoed by that refusal.
func TestToolLevelRefusalDoesNotBlockCompletion(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{
			toolOutcome("artifact", Result{
				Text:    "workspace fuzz cannot produce a plan artifact; allowed kind(s): finding",
				IsError: true, Refused: true,
			}),
			{ToolName: "bash", Result: Result{Text: "read-only inspection", Executes: true, Finished: true},
				Authorization: &security.Decision{Outcome: security.OutcomeAuto, ReadOnly: true}},
		}},
		answerTurn("The investigation is complete; no source was modified."),
	}
	root := t.TempDir()
	got := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: conditionalMutationContract(),
		Root:     root,
		Snapshot: absentCheckoutSnapshot(root),
	})
	if !got.Complete() {
		t.Fatalf("a tool-level refusal vetoed completion: %s", got.Reason)
	}
}

// The deferred-plan rule must not turn an honest answer into a review. Only a
// message made entirely of unperformed observation verbs is a plan; an
// explanatory lead, a closing courtesy, or a substantive clause is an answer.
func TestDeferredPlanDetectionIsNarrow(t *testing.T) {
	cases := map[string]bool{
		"Let me explore the backend directory structure.":                   true,
		"I'll inspect the auth.go file.":                                    true,
		"Let me explore the backend.\nI'll list the Go files.":              true,
		"Let me check the README: it declares module example.com/ohgo.":     false,
		"Let me summarize: the project is a Go service.":                    false,
		"Let me know if you need more detail.":                              false,
		"The project is a Go service. Let me check one more file.":          false,
		"Let me check the README.\nIt declares module example.com/ohgo.":    false,
		"Let me walk you through the layout: cmd contains the entry point.": false,
	}
	for answer, want := range cases {
		if got := answerIsDeferredPlan(answer); got != want {
			t.Errorf("answerIsDeferredPlan(%q) = %v, want %v", answer, got, want)
		}
	}
}

// A release objective names a carrier file the user knows by basename, while
// the canonical coordinate lives deeper in the repository. The run's own
// observed change must resolve that target and the workspace scope must admit
// the other version files. A trailing search that found nothing is an absence
// observation, not an unrecovered failure.
func TestReleaseCompletionResolvesNamedFileAndAcceptsSearchAbsence(t *testing.T) {
	root := t.TempDir()
	intent := contract.IntentContract{
		Objective: "update new version to v0.1.2 and update changelog.json",
		Kind:      contract.ObjectiveMutation,
		Targets: []contract.Target{{
			Kind: contract.TargetPath, Value: "changelog.json",
		}},
		ExpectedChanges: []contract.ExpectedChange{{
			Target:    contract.Target{Kind: contract.TargetPath, Value: "changelog.json"},
			Operation: contract.ChangeModify,
		}},
		MutationScope: contract.MutationScope{Kind: contract.MutationScopeWorkspace},
		Verification:  []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	snapshot := CaptureTargets(root, intent, nil)
	if snapshot == nil || len(snapshot.Entries) == 0 {
		t.Fatal("the named target was not captured")
	}

	turns := []Turn{
		{Results: []ToolOutcome{
			toolOutcome("write", Result{Text: "wrote changelog", Mutates: true,
				Details: map[string]any{"path": "internal/update/changelog.json"}}),
			toolOutcome("write", Result{Text: "wrote version", Mutates: true,
				Details: map[string]any{"path": "VERSION"}}),
		}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{
			Text: "ok", Executes: true, Finished: true, Verifies: true,
		})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{
			Text: "no matches", IsError: true, ObservedAbsence: true,
		})}},
		cleanStop(),
	}
	got := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: intent,
		Snapshot: snapshot,
		Root:     root,
	})
	if !got.Complete() {
		t.Fatalf("completion = %s (%s), want COMPLETED", got.State, got.Reason)
	}
}

// A bare-name resolution is only made when exactly one observed change carries
// that basename. Two candidates leave the target unresolved rather than
// inventing a coordinate.
func TestAbsentNamedTargetResolutionRequiresOneCandidate(t *testing.T) {
	root := t.TempDir()
	intent := contract.IntentContract{
		Objective: "update changelog.json",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "changelog.json"}},
		ExpectedChanges: []contract.ExpectedChange{{
			Target:    contract.Target{Kind: contract.TargetPath, Value: "changelog.json"},
			Operation: contract.ChangeModify,
		}},
		MutationScope: contract.MutationScope{Kind: contract.MutationScopePaths, Files: []string{"changelog.json"}},
	}
	snapshot := CaptureTargets(root, intent, nil)
	resolved := ResolveAbsentNamedTargets(snapshot, []string{
		"internal/update/changelog.json",
		"docs/changelog.json",
	})
	if len(resolved) != 0 {
		t.Fatalf("ambiguous observed paths were resolved: %v", resolved)
	}
}

// Structural intelligence can establish what is connected; it cannot establish
// that the user's requested change happened. A successful investigation turn
// with gry-shaped evidence must not substitute for an observed mutation.
func TestStructuralEvidenceCannotCompleteAMutation(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("investigate", Result{
			Text: "investigation: sufficient · structural/gry(SUCCESS, OBSERVED)",
			Details: map[string]any{
				"status":            "sufficient",
				"outcome":           "SUCCESS",
				"source":            "gry",
				"records":           12,
				"confidence":        0.82,
				"external_requests": 1,
			},
		})}},
		answerTurn("the change is structurally safe"),
	}
	verdict := EvaluateCompletion(turns, true)
	if verdict.Complete() {
		t.Fatalf("structural evidence completed a mutation objective: %+v", verdict)
	}
}

// A failed recall of a compacted observation is a navigation miss, not a
// poisoned capability. Reading the source directly after it is the recovery.
func TestFailedRecallDoesNotBlockAfterDirectSourceRead(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{
			toolOutcome("recall", Result{
				Text: "recall: no archived observation", IsError: true, ObservedAbsence: true,
			}),
			toolOutcome("recall", Result{
				Text: "recall: no archived observation", IsError: true, ObservedAbsence: true,
			}),
		}},
		{Results: []ToolOutcome{
			toolOutcome("read", Result{Text: "package checkout\n\nfunc Place() {}\n", Details: map[string]any{
				"path": "checkout/app/checkout.go", "totalLines": 3, "offset": 1, "limit": 2000,
			}}),
		}},
		answerTurn("checkout/app/checkout.go defines Place."),
	}
	got := EvaluateTaskCompletion(CompletionInput{
		Turns: turns,
		Contract: contract.IntentContract{
			Kind:      contract.ObjectiveInformational,
			Objective: "Inspect checkout/app/checkout.go.",
		},
	})
	if !got.Complete() {
		t.Fatalf("a direct source read did not recover the recall miss: %s", got.Reason)
	}
}

// A tool failure that reached the capability remains a hard veto until a later
// success or verification recovers it. The failure is part of the run's
// evidence, and completion cannot certify an answer that leaves it unresolved.
func TestInformationalCompletionStillVetoesAFailedProbe(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "package main"})}},
		{Results: []ToolOutcome{toolOutcome("bash", Result{
			Text: "find: permission denied", IsError: true, FailureClass: ToolFailureRuntime,
		})}},
		answerTurn("The inspected entry point is main.go."),
	}
	intent := contract.IntentContract{Objective: "analyze this project", Kind: contract.ObjectiveInformational}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: intent})
	if verdict.Complete() {
		t.Fatal("an unrecovered tool failure was certified complete")
	}
	if !strings.Contains(verdict.Reason, "bash") {
		t.Fatalf("reason = %q, want the failing capability named", verdict.Reason)
	}
}

// Execution and mutation objectives keep the same hard failure rule: a failed
// command is part of the proof state, not an unrelated observation.
func TestExecutionCompletionStillVetoesAFailedCommand(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("bash", Result{
			Text: "command failed", IsError: true, FailureClass: ToolFailureRuntime,
		})}},
		answerTurn("The command completed."),
	}
	intent := contract.IntentContract{Objective: "run the command", Kind: contract.ObjectiveExecution}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: intent})
	if verdict.Complete() {
		t.Fatal("a failed execution was reported complete")
	}
}

// A correction-budget refusal never ran. It is control feedback, not a failed
// observation, and must not veto an informational answer that already
// qualified the claim the repair could not complete.
func TestCorrectionBudgetRefusalIsNotAnUnrecoveredFailure(t *testing.T) {
	turns := []Turn{
		{Results: []ToolOutcome{toolOutcome("read", Result{Text: "package main"})}},
		{Results: []ToolOutcome{toolOutcome("read", Result{
			Text: "correction budget exhausted", IsError: true,
			FailureClass: ToolFailureCorrectionBudget,
		})}},
		answerTurn("The inspected entry point is main.go; the architecture rules were not verified."),
	}
	intent := contract.IntentContract{Objective: "analyze this project", Kind: contract.ObjectiveInformational}
	verdict := EvaluateTaskCompletion(CompletionInput{Turns: turns, Contract: intent})
	if !verdict.Complete() {
		t.Fatalf("a correction-budget refusal vetoed the answer: %s", verdict.Reason)
	}
}
