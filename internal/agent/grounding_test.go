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

// A semantic refusal is feedback, not completion. The model narrated an
// investigation while proposing an ungrounded write; ending the run there
// prevents it from receiving the reason and choosing a grounded observation.
// The recovery request must carry the structured refusal, and the model must be
// able to continue with an allowed action.
func TestLoopContinuesAfterGroundingRejection(t *testing.T) {
	writeExecuted, readExecuted := false, false
	write := &groundingTool{name: "write", tier: TierWrite, executed: &writeExecuted}
	read := &groundingTool{name: "read", tier: TierRead, executed: &readExecuted}

	const answer = "The additional projects are k8s and opencode."
	script := []ai.AssistantMessage{
		{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "I'll inspect the README and measure the bottleneck."},
				ai.ToolCall{ID: "write-1", Name: "write", Arguments: map[string]any{"path": "README.md"}},
			},
			StopReason: ai.StopToolUse,
		},
		{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"path": "README.md"}},
			},
			StopReason: ai.StopToolUse,
		},
		{Content: []ai.ContentBlock{ai.TextContent{Text: answer}}},
	}

	requests := 0
	var recoveryRequest []ai.Message
	loop, err := New(Config{
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			requests++
			if requests == 2 {
				recoveryRequest = req.Messages
			}
			if requests > len(script) {
				t.Fatalf("provider request %d exceeded the script", requests)
			}
			return ai.NewCompletedStream(script[requests-1]), nil
		},
		Tools:     []Tool{write, read},
		MaxTurns:  5,
		Grounding: actionlessGrounding(),
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "Additional projects are k8s and opencode."}})
	if err != nil {
		t.Fatal(err)
	}

	if requests != 3 {
		t.Fatalf("model requests = %d, want refusal, grounded retry, answer (3)", requests)
	}
	if writeExecuted {
		t.Fatal("the ungrounded write executed")
	}
	if !readExecuted {
		t.Fatal("the model was not able to continue with a grounded observation")
	}
	if len(turns) != 3 || turns[len(turns)-1].Assistant.Text() != answer {
		t.Fatalf("turns = %+v, want the recovery answer", turns)
	}
	refusal := turns[0].Results[0].Result
	if !refusal.CandidateRejected || !strings.Contains(refusal.Text, "not grounded") {
		t.Fatalf("first outcome = %+v, want a grounded refusal", refusal)
	}
	sawRefusal := false
	for _, message := range recoveryRequest {
		if result, ok := message.(ai.ToolResultMessage); ok && strings.Contains(result.Text(), "not grounded") {
			sawRefusal = true
		}
	}
	if !sawRefusal {
		t.Fatal("the recovery request did not carry the grounding refusal to the model")
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

// A report that the run left the workspace untouched is not a repository
// absence claim. The observed answer ended with "no files were modified" and
// was reviewed as if it had claimed the repository contains no files; no file
// scan can ever cover that phrasing, so the review could never be satisfied.
// A genuine existence claim keeps the boundary.
func TestAssessGroundingSeparatesChangeReportsFromAbsenceClaims(t *testing.T) {
	grounding := &contract.IntentGrounding{
		Objective: "investigate the bottleneck and evaluate",
		Action:    contract.GroundingInvestigate,
		Actions:   []contract.GroundingAction{contract.GroundingInvestigate},
		Scope:     contract.GroundingScope{Kind: contract.ScopeRepository},
	}
	cases := []struct {
		name   string
		answer string
		review bool
	}{
		{
			name:   "no-op change report",
			answer: "Workspace unchanged — git status is clean; no files were modified.",
		},
		{
			name:   "file existence claim",
			answer: "README.md does not exist in this repository.",
			review: true,
		},
		{
			name:   "test category absence",
			answer: "No test files exist in this repository.",
			review: true,
		},
		{
			// The observed session-276 answer: the model bounded the absence
			// to what it inspected and admitted the test inventory was never
			// queried. The boundary is the claim's scope, so it is a report,
			// not a repository-wide conclusion. Flagging it left the run with
			// no recap and no actionable next step.
			name:   "absence bounded to inspected scope",
			answer: "No API routes, no database, no tests detected in inspected scope (test inventory not queried), no additional pages beyond /.",
		},
		{
			name:   "absence with contracted non-observation",
			answer: "Evidence gaps: the test inventory wasn't queried.",
		},
		{
			name:   "bounded phrase beside a global conclusion",
			answer: "No tests were found in the inspected scope, and the project has no tests anywhere.",
			review: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assessment := AssessGrounding(informationalContract(grounding), nil, tc.answer)
			unsupported := false
			for _, issue := range assessment.Issues {
				if issue.Code == "unsupported_negative_conclusion" {
					unsupported = true
				}
			}
			if unsupported != tc.review {
				t.Fatalf("unsupported_negative_conclusion = %v, want %v (reasons: %v)",
					unsupported, tc.review, assessment.Reasons)
			}
		})
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

// A recursive glob that pruned .gitignore paths did not cover the repository,
// even when it returned other matches. It cannot prove a file absent: the
// missing file may live in the pruned subtree.
func TestEvaluateTaskCompletionRejectsAbsenceFromPrunedGlob(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "Check whether README.md exists.",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	})
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{{
				ToolName: "glob",
				Args:     map[string]any{"pattern": "**/*"},
				Result: Result{Text: "main.go\n\n[1 path was excluded by .gitignore; " +
					`pass "gitignore": false to include ignored paths.]`},
			}}},
			answerTurnNoTools("README.md does not exist in this repository."),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("a pruned recursive scan was accepted as proof of file absence")
	}
}

// A timed-out recursive scan is incomplete even when it returned no matches.
// The tool reports the deadline; completion must not read that absence as proof.
func TestEvaluateTaskCompletionRejectsAbsenceFromTimedOutGlob(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "Check whether README.md exists.",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	})
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{{
				ToolName: "glob",
				Args:     map[string]any{"pattern": "**/*"},
				Result: Result{Text: "glob timed out after 5s before finding any matches — " +
					"results are incomplete, NOT proof of absence."},
			}}},
			answerTurnNoTools("README.md does not exist in this repository."),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("a timed-out recursive scan was accepted as proof of file absence")
	}
}

// A failed lookup at a guessed path is not evidence that a documented subsystem
// is absent. "Could not be read" and "no files matched" report an unverified
// search result; they cannot support a conclusion that the backend is not
// implemented.
func TestEvaluateTaskCompletionRejectsBackendAbsenceFromGuessedPath(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "check this project and analyze what this project is about?",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	})
	answer := "The backend/ folder exists, but backend/go-ecommerce could not be read and no Go source files " +
		"matched backend/go-ecommerce/**/*.go (glob returned nothing, with 4 paths excluded by .gitignore). " +
		"So the Go microservices backend described in the README appears to be planned/stubbed rather than " +
		"implemented and checked in."
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{
				{ToolName: "ls", Args: map[string]any{"path": "."},
					Result: Result{Text: "README.md\nbackend\npackage.json\nsrc\nsupabase\n"}},
				{ToolName: "ls", Args: map[string]any{"path": "backend"},
					Result: Result{Text: "cmd\ninternal\npkg\ngo.mod\ngo.sum\ngo.work\n"}},
				{ToolName: "glob", Args: map[string]any{"pattern": "backend/go-ecommerce/**/*.go"},
					Result: Result{Text: "No files matched. 4 paths were excluded by .gitignore; " +
						`pass "gitignore": false to include ignored paths.`}},
				{ToolName: "ls", Args: map[string]any{"path": "backend/go-ecommerce"},
					Result: Result{Text: "read directory: no such file or directory", IsError: true}},
				{ToolName: "ls", Args: map[string]any{"path": "src/client"},
					Result: Result{Text: "ClientApp.tsx\ncomponents\n"}},
				{ToolName: "read", Args: map[string]any{"path": "src/client/ClientApp.tsx"},
					Result: Result{Text: "import React from 'react';"}},
			}},
			answerTurnNoTools(answer),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("an absence conclusion from a failed guessed path was reported complete")
	}
	if !strings.Contains(verdict.Reason, "intent grounding") {
		t.Fatalf("reason = %q, want a grounding boundary", verdict.Reason)
	}
}

// An adjective between "no" and a file category ("no Go source files") does not
// turn the claim into a concrete file coordinate. It is still a concept absence
// and needs evidence the run does not have.
func TestEvaluateTaskCompletionRejectsQualifiedFileAbsence(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "check this project and analyze what this project is about?",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	})
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{
				{ToolName: "ls", Args: map[string]any{"path": "backend"},
					Result: Result{Text: "cmd\ninternal\npkg\ngo.mod\n"}},
			}},
			answerTurnNoTools("No Go source files are present in the backend."),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("a qualified file-category absence was reported complete")
	}
	if !strings.Contains(verdict.Reason, "intent grounding") {
		t.Fatalf("reason = %q, want a grounding boundary", verdict.Reason)
	}
}

// Reporting a failed read as the basis for the answer is an unverified negative
// result even when the sentence has no "no X" claim. The passive form
// ("could not be read") must count the same as "could not read".
func TestEvaluateTaskCompletionRejectsPassiveSearchFailure(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "check this project and analyze what this project is about?",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	})
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns: []Turn{
			{Results: []ToolOutcome{
				{ToolName: "ls", Args: map[string]any{"path": "backend"},
					Result: Result{Text: "cmd\ninternal\npkg\ngo.mod\n"}},
			}},
			answerTurnNoTools("The backend/go-ecommerce directory could not be read, so the backend is likely a stub."),
		},
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("a passive search failure was accepted as a completed answer")
	}
	if !strings.Contains(verdict.Reason, "intent grounding") {
		t.Fatalf("reason = %q, want a grounding boundary", verdict.Reason)
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

// "Tests are not run" describes the project, not this run. Only the run-fact
// form ("no tests were run") is a bounded report.
func TestUnrunTestsAreNotExcusedAsABoundedReport(t *testing.T) {
	if boundedNegativeReport("Tests are not run for this module.") {
		t.Error("a project property was excused as a run fact")
	}
	if !boundedNegativeReport("No tests were run in this pass.") {
		t.Error("the run-fact form was treated as a repository absence")
	}
}
