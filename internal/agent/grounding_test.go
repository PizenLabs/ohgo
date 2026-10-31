package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// groundingTool is a minimal tool whose declared class is the test variable.
type groundingTool struct {
	name     string
	classes  []ActionClass
	tier     Tier
	executed *bool
	seenArgs map[string]any
}

func (t *groundingTool) Name() string                   { return t.name }
func (t *groundingTool) Description() string            { return "grounding test tool" }
func (t *groundingTool) Parameters() *jsonschema.Schema { return nil }
func (t *groundingTool) Approval() Approval             { return Approval{Tier: t.tier} }
func (t *groundingTool) Exclusive(map[string]any) bool  { return false }
func (t *groundingTool) ActionClasses() []ActionClass   { return t.classes }
func (t *groundingTool) Execute(_ Context, args map[string]any) Result {
	if t.executed != nil {
		*t.executed = true
	}
	if t.seenArgs != nil {
		for key, value := range args {
			t.seenArgs[key] = value
		}
	}
	return Result{Text: "ok"}
}

// actionlessGrounding is the observed failure's request shape: entities, no
// actionable verb.
func actionlessGrounding() *contract.IntentGrounding {
	return &contract.IntentGrounding{
		Objective: "Additional projects are k8s and opencode.",
		Action:    contract.GroundingUnknown,
		Entities: []contract.Entity{
			{Value: "k8s", Kind: contract.EntityProject},
			{Value: "opencode", Kind: contract.EntityProject},
		},
		Scope: contract.GroundingScope{Kind: contract.ScopeNamed, Values: []string{"k8s", "opencode"}},
		Ambiguities: []contract.Ambiguity{{
			Kind: "unknown_action", Detail: "the request names no actionable verb", Material: true,
		}},
		Confidence: contract.GroundingConfidenceLow,
	}
}

// Delegation is an action with a potential objective. Under an actionless
// request it is allowed only as bounded observation: the loop forces the child
// read-only, so the delegated assignment cannot mutate.
func TestValidateProposalAllowsDelegationAsBoundedObservation(t *testing.T) {
	tool := &groundingTool{name: "task", tier: TierRead, classes: []ActionClass{ActionDelegate}}
	assessment := ValidateCall(actionlessGrounding(), ai.ToolCall{
		ID: "1", Name: "task", Arguments: map[string]any{"prompt": "write README.md"},
	}, tool)
	if assessment.Verdict != contract.GroundingPass {
		t.Fatalf("verdict = %s, want PASS (read-only delegation)", assessment.Verdict)
	}
}

// The loop forces a delegated child read-only when the request did not ground
// a mutation; the model's own assignment cannot widen the parent's objective.
func TestLoopForcesDelegationReadOnlyUnderActionlessRequest(t *testing.T) {
	executed := false
	seen := map[string]any{}
	delegate := &groundingTool{
		name: "task", tier: TierRead, classes: []ActionClass{ActionDelegate},
		executed: &executed, seenArgs: seen,
	}
	loop, err := New(Config{
		Stream:    scriptedMessages(callMessage("task-1", "task")),
		Tools:     []Tool{delegate},
		MaxTurns:  2,
		Grounding: actionlessGrounding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "Additional projects are k8s and opencode."}}); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("the read-only delegation did not execute")
	}
	if seen["readOnly"] != true {
		t.Fatalf("delegation args = %+v, want readOnly=true", seen)
	}
}

// A mutating proposal under an actionless request is not grounded. This is the
// semantic half of the observed failure: the model turned a context statement
// into "write/inspect README".
func TestValidateProposalRejectsInventedMutation(t *testing.T) {
	tool := &groundingTool{name: "write", tier: TierWrite}
	assessment := ValidateCall(actionlessGrounding(), ai.ToolCall{
		ID: "1", Name: "write", Arguments: map[string]any{"path": "README.md"},
	}, tool)
	if assessment.Verdict != contract.GroundingReject {
		t.Fatalf("verdict = %s (%s), want REJECT", assessment.Verdict, assessment.Reason())
	}
}

// Observation remains available under an ambiguous request: it may gather
// evidence, but completion cannot claim the request asked for it.
func TestValidateProposalAllowsObservationUnderAmbiguity(t *testing.T) {
	tool := &groundingTool{name: "read", tier: TierRead}
	assessment := ValidateCall(actionlessGrounding(), ai.ToolCall{
		ID: "1", Name: "read", Arguments: map[string]any{"path": "README.md"},
	}, tool)
	if assessment.Verdict != contract.GroundingPass {
		t.Fatalf("verdict = %s (%s), want PASS", assessment.Verdict, assessment.Reason())
	}
}

// A read request must not acquire mutation authority because the workspace is
// BUILD; the objective is read-only.
func TestValidateProposalRejectsMutationForReadRequest(t *testing.T) {
	grounding := actionlessGrounding()
	grounding.Action = contract.GroundingRead
	grounding.Actions = []contract.GroundingAction{contract.GroundingRead}
	grounding.Ambiguities = nil
	tool := &groundingTool{name: "edit", tier: TierWrite}
	assessment := ValidateCall(grounding, ai.ToolCall{
		ID: "1", Name: "edit", Arguments: map[string]any{"path": "note.txt"},
	}, tool)
	if assessment.Verdict != contract.GroundingReject {
		t.Fatalf("verdict = %s, want REJECT", assessment.Verdict)
	}
}

// A grounded mutation remains allowed; the layer narrows, never blocks a
// request that actually asked for the change.
func TestValidateProposalAllowsGroundedMutation(t *testing.T) {
	grounding := actionlessGrounding()
	grounding.Action = contract.GroundingMutate
	grounding.Actions = []contract.GroundingAction{contract.GroundingMutate}
	grounding.MutationIntent = true
	grounding.Ambiguities = nil
	tool := &groundingTool{name: "write", tier: TierWrite}
	assessment := ValidateCall(grounding, ai.ToolCall{
		ID: "1", Name: "write", Arguments: map[string]any{"path": "README.md"},
	}, tool)
	if assessment.Verdict != contract.GroundingPass {
		t.Fatalf("verdict = %s (%s), want PASS", assessment.Verdict, assessment.Reason())
	}
}

// An ungrounded execution is review, not rejection: a process is not a
// repository change, and the observation may still gather evidence.
func TestValidateProposalReviewsUngroundedExecution(t *testing.T) {
	tool := &groundingTool{name: "bash", tier: TierExec, classes: []ActionClass{ActionExecute}}
	assessment := ValidateCall(actionlessGrounding(), ai.ToolCall{
		ID: "1", Name: "bash", Arguments: map[string]any{"command": "ls"},
	}, tool)
	if assessment.Verdict != contract.GroundingReview {
		t.Fatalf("verdict = %s, want REVIEW", assessment.Verdict)
	}
}

// The loop refuses the invented mutation before any authorizer or execution.
func TestLoopRefusesInventedMutationAtSemanticAdmission(t *testing.T) {
	executed := false
	write := &groundingTool{name: "write", tier: TierWrite, executed: &executed}
	loop, err := New(Config{
		Stream:    scriptedMessages(callMessage("write-1", "write")),
		Tools:     []Tool{write},
		MaxTurns:  2,
		Grounding: actionlessGrounding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "Additional projects are k8s and opencode."}})
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("the ungrounded write executed")
	}
	if len(turns) == 0 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	outcome := turns[0].Results[0]
	if !outcome.Result.IsError || !outcome.Result.CandidateRejected {
		t.Fatalf("outcome = %+v, want a refused (not executed) action", outcome.Result)
	}
	if !strings.Contains(outcome.Result.Text, "not grounded") {
		t.Fatalf("refusal = %q, want a grounding reason", outcome.Result.Text)
	}
}

// A grounded observation still executes through the same loop.
func TestLoopAllowsObservationUnderAmbiguity(t *testing.T) {
	executed := false
	read := &groundingTool{name: "read", tier: TierRead, executed: &executed}
	loop, err := New(Config{
		Stream:    scriptedMessages(callMessage("read-1", "read")),
		Tools:     []Tool{read},
		MaxTurns:  2,
		Grounding: actionlessGrounding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "Additional projects are k8s and opencode."}}); err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("the bounded observation did not execute")
	}
}

// informationalContract builds a manually grounded informational contract.
func informationalContract(grounding *contract.IntentGrounding) contract.IntentContract {
	return contract.IntentContract{
		Objective: grounding.Objective,
		Kind:      contract.ObjectiveInformational,
		Grounding: grounding,
	}
}

// observedRootListing is a successful complete root listing with no README.
func observedRootListing() Turn {
	return Turn{Results: []ToolOutcome{{
		ToolName: "ls",
		Args:     map[string]any{"path": "."},
		Result:   Result{Text: "cmd\ninternal\nmain.go\n"},
	}}}
}

// answerTurnNoTools is a final answer with no tool calls.
func answerTurnNoTools(text string) Turn {
	return Turn{Assistant: ai.AssistantMessage{
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		StopReason: ai.StopEndTurn,
	}}
}

// The observed failure must not complete: a root listing does not prove that
// the repository has no documentation, and the named projects disappeared.
func TestEvaluateTaskCompletionRejectsUnsupportedNegativeConclusion(t *testing.T) {
	intent := informationalContract(actionlessGrounding())
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			observedRootListing(),
			answerTurnNoTools("The repository contains no README documentation, so I cannot describe the test-web2 project or its contents."),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("an unsupported global negative conclusion was reported complete")
	}
	if !strings.Contains(verdict.Reason, "intent grounding") {
		t.Fatalf("reason = %q, want a grounding boundary", verdict.Reason)
	}
}

// Entity loss is reviewed even when the answer is otherwise non-empty.
func TestEvaluateTaskCompletionRejectsMissingNamedEntity(t *testing.T) {
	intent := informationalContract(actionlessGrounding())
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{answerTurnNoTools("opencode looks fine.")},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("an answer that dropped k8s was reported complete")
	}
	if !strings.Contains(verdict.Reason, "k8s") {
		t.Fatalf("reason = %q, want the missing entity", verdict.Reason)
	}
}

// An acknowledgement preserves both entities, so an actionless request can
// complete without inventing an action.
func TestEvaluateTaskCompletionAcceptsGroundedAcknowledgement(t *testing.T) {
	intent := informationalContract(actionlessGrounding())
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{answerTurnNoTools("Noted: k8s and opencode are additional projects.")},
		Contract: intent,
	})
	if !verdict.Complete() {
		t.Fatalf("grounded acknowledgement was not complete: %s", verdict.Reason)
	}
}

// Conditional entities are preserved in state but are not forced into an
// answer that may not need them.
func TestEvaluateTaskCompletionAcceptsConditionalEntities(t *testing.T) {
	grounding := actionlessGrounding()
	for i := range grounding.Entities {
		grounding.Entities[i].Optional = true
	}
	grounding.Scope.Conditional = true
	intent := informationalContract(grounding)
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{answerTurnNoTools("Understood; I will use them only if relevant.")},
		Contract: intent,
	})
	if !verdict.Complete() {
		t.Fatalf("conditional entities forced a review: %s", verdict.Reason)
	}
}

// A conditional request permits bounded observation of the named entities;
// the narrative itself is not an invented objective.
func TestEvaluateTaskCompletionAcceptsConditionalObservation(t *testing.T) {
	grounding := actionlessGrounding()
	for i := range grounding.Entities {
		grounding.Entities[i].Optional = true
	}
	grounding.Scope.Conditional = true
	intent := informationalContract(grounding)
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{answerTurnNoTools("I inspected k8s and opencode; both are relevant to the request.")},
		Contract: intent,
	})
	if !verdict.Complete() {
		t.Fatalf("conditional observation was reviewed: %s", verdict.Reason)
	}
}

// A tool failure is not a negative conclusion, and an answer that only reports
// the failure is not a completed objective.
func TestEvaluateTaskCompletionRejectsUnverifiedSearchResult(t *testing.T) {
	intent := informationalContract(actionlessGrounding())
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{answerTurnNoTools("I could not find any README or documentation for k8s or opencode.")},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("an unverified search result was reported complete")
	}
}

// A local listing that did not contain a file is not repository-wide proof
// that the file is missing. This covers the "is missing" phrasing separately
// from "does not exist".
func TestEvaluateTaskCompletionRejectsLocalAbsenceAsGlobal(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "Check whether README.md exists.",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	})
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{{
				ToolName: "ls",
				Args:     map[string]any{"path": "docs"},
				Result:   Result{Text: "design.md\n"},
			}}},
			answerTurnNoTools("README.md is missing from this repository."),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("a local listing was accepted as repository-wide proof of absence")
	}
}

// A root listing proves absence at the root, not repository-wide absence. The
// sentence's own scope decides which evidence is required.
func TestEvaluateTaskCompletionRejectsRootListingAsGlobalProof(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "Check whether README.md exists.",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	})
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{{
				ToolName: "ls",
				Args:     map[string]any{"path": "."},
				Result:   Result{Text: "cmd\ninternal\nmain.go\n"},
			}}},
			answerTurnNoTools("README.md does not exist in this repository."),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("a root listing was accepted as repository-wide proof of absence")
	}
}

// A complete recursive file scan can prove a file is absent; the conclusion is
// then allowed to complete.
func TestEvaluateTaskCompletionAcceptsProvenFileAbsence(t *testing.T) {
	grounding := &contract.IntentGrounding{
		Objective: "Check whether README.md exists.",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	}
	intent := informationalContract(grounding)
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{{
				ToolName: "glob",
				Args:     map[string]any{"pattern": "**/*"},
				Result:   Result{Text: "main.go\ndocs.md\n"},
			}}},
			answerTurnNoTools("README.md does not exist in this repository."),
		},
		Contract: intent,
	})
	if !verdict.Complete() {
		t.Fatalf("proven file absence was not complete: %s", verdict.Reason)
	}
}

// A category ("documentation") is not a file, so even a complete glob cannot
// prove its absence.
func TestEvaluateTaskCompletionRejectsProvenConceptAbsence(t *testing.T) {
	intent := informationalContract(actionlessGrounding())
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{{
				ToolName: "glob",
				Args:     map[string]any{"pattern": "**/*"},
				Result:   Result{Text: "main.go\nnotes.txt\n"},
			}}},
			answerTurnNoTools("The repository contains no documentation for k8s or opencode."),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("a concept absence was reported complete from a file scan")
	}
}

// An answer may not narrate an action the request did not ask for.
func TestEvaluateTaskCompletionRejectsUngroundedActionNarrative(t *testing.T) {
	intent := informationalContract(actionlessGrounding())
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{answerTurnNoTools("I inspected k8s and opencode and found them healthy.")},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("an ungrounded action narrative was reported complete")
	}
}

// A scope narrowed to the current repository is a semantic drift when the
// request named other projects.
func TestEvaluateTaskCompletionRejectsScopeNarrowing(t *testing.T) {
	intent := informationalContract(actionlessGrounding())
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{answerTurnNoTools("Only the current repository is relevant. k8s and opencode are out of scope.")},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("scope narrowing was reported complete")
	}
}

// A local workflow file proves what is configured, never that a remote run
// happened. An answer that converts local configuration into external fact is
// the failure class this check exists for.
func TestAssessGroundingRequiresAnObservationForExternalState(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective:  "Does this workflow run automatically?",
		Action:     contract.GroundingRead,
		Confidence: contract.GroundingConfidenceHigh,
	})
	localOnly := []Turn{{
		Results: []ToolOutcome{{
			ToolName: "read",
			Args:     map[string]any{"path": ".github/workflows/telemetry.yml"},
			Result:   Result{Text: "on:\n  schedule:\n    - cron: \"17 3 * * *\""},
		}},
		Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "reading"}}},
	}}

	claim := AssessGrounding(intent, localOnly, "The workflow ran successfully at 03:17 today and pushed the chart.")
	if claim.Pass() {
		t.Fatal("local configuration was accepted as proof that GitHub ran the workflow")
	}

	// A hedge in the same sentence is an epistemic report, not a claim, so the
	// honest answer completes.
	honest := AssessGrounding(intent, localOnly,
		"The workflow is scheduled for 03:17 daily, but I cannot verify from here whether it ran today.")
	if !honest.Pass() {
		t.Fatalf("an honest uncertainty report was reviewed: %s", honest.Reason())
	}

	// A configuration sentence describes what can happen, not what did. The
	// modal form is not a claim that the remote executed it.
	configured := AssessGrounding(intent, localOnly,
		"The workflow is set up to run on a schedule, and it can also be triggered by hand.")
	if !configured.Pass() {
		t.Fatalf("a configuration description was reviewed as an external claim: %s", configured.Reason())
	}

	// An observation of the remote surface is the evidence the claim needs.
	observed := append(append([]Turn(nil), localOnly...), Turn{Results: []ToolOutcome{{
		ToolName: "bash",
		Args:     map[string]any{"command": "gh run list --workflow telemetry.yml --limit 1"},
		Result:   Result{Text: "completed  success  telemetry  2026-10-31T03:17:00Z", Executes: true, Finished: true},
	}}})
	if verdict := AssessGrounding(intent, observed, "The workflow ran successfully at 03:17 today."); !verdict.Pass() {
		t.Fatalf("an observed remote run was not accepted: %s", verdict.Reason())
	}
}
