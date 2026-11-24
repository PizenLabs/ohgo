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
			name:   "readme content read report",
			answer: "No README content was read; README.md and docs/Readme.md exist but were not opened.",
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

// Session 324: "no LICENSE file" was reviewed although the run had listed the
// root and the file really is absent. Two things had promoted it to a
// repository-wide claim and hidden the root-file vocabulary: the word "repo" in
// the next sentence fell inside the fixed byte radius, and the claim subject
// kept its trailing "file". Scope is the claim's sentence, and a complete root
// listing covers a root-file absence.
func TestEvaluateTaskCompletionAcceptsRootFileAbsence(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "check this project and analyze what this project is about?",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
	})
	rootListing := Turn{Results: []ToolOutcome{{
		ToolName: "ls",
		Args:     map[string]any{"path": "."},
		Result: Result{Text: ".\nsrc/\nREADME.md\npackage.json\n", Details: map[string]any{
			"path": ".", "entries": 4, "depth": 2, "truncated": false,
		}},
	}}}
	answer := "Notable issues: nested backend/.git (root repo ignores backend/), " +
		"empty migrations, no LICENSE file.\n\n" +
		"In short: the repo holds services and a frontend."
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{rootListing, answerTurnNoTools(answer)},
		Contract: intent,
	})
	if !verdict.Complete() {
		t.Fatalf("a root-file absence with a root listing was reviewed: %s", verdict.Reason)
	}
	// Without the listing the absence is unproven, and the review still names it.
	if verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    []Turn{answerTurnNoTools(answer)},
		Contract: intent,
	}); verdict.Complete() {
		t.Fatal("a root-file absence without a listing completed")
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

// A git inspection is bounded evidence. `git tag -l`, `git branch -a` and
// `git remote` list a whole namespace, and `git log` on an unborn branch states
// the absence outright. The bash tool publishes that observation, so an answer
// reporting the empty result is not the unproven absence the negative-claim
// gate exists to catch.
func TestEvaluateTaskCompletionAcceptsObservedVCSAbsence(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective: "Inspect the repository's git state.",
		Action:    contract.GroundingRead,
		Actions:   []contract.GroundingAction{contract.GroundingRead},
		Scope:     contract.GroundingScope{Kind: contract.ScopeRepository},
	})
	turns := func(observation map[string]any) []Turn {
		return []Turn{
			{Results: []ToolOutcome{{
				ToolName: "bash",
				Args:     map[string]any{"command": "git status && git log --oneline -5"},
				Result: Result{
					Text:    "$ git status && git log --oneline -5\n\nOn branch master\n\nNo commits yet\n\nfatal: your current branch 'master' does not have any commits yet\n\n[exit code 128, 55ms]",
					IsError: true,
					Details: observation,
				},
			}}},
			{Results: []ToolOutcome{{
				ToolName: "bash",
				Args:     map[string]any{"command": "git branch -a; git tag -l; git remote"},
				Result: Result{
					Text:    "$ git branch -a; git tag -l; git remote\n\n[exit code 0, 59ms]",
					Details: observation,
				},
			}}},
			answerTurnNoTools("The repository has no commits, no branches besides the implicit master, no tags, and no remotes."),
		}
	}
	observed := map[string]any{
		"command":     "git branch -a; git tag -l; git remote",
		"exitCode":    0,
		"git_absence": []string{"commit", "branch", "tag", "remote"},
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns(observed),
		Contract: intent,
	})
	if !verdict.Complete() {
		t.Fatalf("an absence established by a complete git enumeration was reviewed: %s", verdict.Reason)
	}

	// Control: the same answer without the tool's structured observation keeps
	// the review boundary. The test defends the evidence path, not a relaxed
	// absence rule.
	verdict = EvaluateTaskCompletion(CompletionInput{
		Turns:    turns(map[string]any{"command": "git branch -a; git tag -l; git remote", "exitCode": 0}),
		Contract: intent,
	})
	if verdict.Complete() {
		t.Fatal("an unobserved VCS absence was reported complete")
	}
	if !strings.Contains(verdict.Reason, "intent grounding") {
		t.Fatalf("control reason = %q, want the unproven-absence boundary", verdict.Reason)
	}
}

// "No X to arbitrate" is an epistemic limit, not a repository absence. A run
// without Gry/runtime evidence must be allowed to say it could not compare,
// rather than fabricating a contradiction or demanding a file scan.
func TestEpistemicContradictionLimitIsNotAbsence(t *testing.T) {
	answer := "No Gry/runtime evidence was obtained, so there is no static-vs-runtime contradiction to arbitrate."
	if claims := negativeClaims(answer); len(claims) != 0 {
		t.Fatalf("an epistemic limit was treated as an absence claim: %+v", claims)
	}
	// The same sentence without the epistemic boundary remains an assertion
	// that the contradiction does not exist and still needs evidence.
	claims := negativeClaims("There is no static-vs-runtime contradiction in this repository.")
	if len(claims) == 0 {
		t.Fatal("an actual absence assertion was ignored")
	}
}

// A negated event reports that the external state did not happen. Session 733
// answered "No tests were run and no server/database was executed" and the
// runtime reviewed it as an assertion that the server was used, because the
// event pattern saw "server ... executed" and ignored the negation.
func TestAssessGroundingAcceptsANegatedExternalEvent(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective:  "Investigate this project and analyze what this project is about.",
		Action:     contract.GroundingRead,
		Confidence: contract.GroundingConfidenceHigh,
	})
	for _, answer := range []string{
		"No tests were run and no server/database was executed.",
		"The workflow was not executed.",
		"The deployment was never published.",
		"The server hasn't been executed.",
	} {
		if assessment := AssessGrounding(intent, nil, answer); !assessment.Pass() {
			t.Fatalf("%q was reviewed as an external claim: %s", answer, assessment.Reason())
		}
	}

	// A positive event in the same shape is still a claim that needs an
	// observation: the negation must attach to that event, not to the answer.
	for _, answer := range []string{
		"The server was executed.",
		"No tests were run, but the deployment was executed.",
		"The workflow ran and the server was executed.",
	} {
		if assessment := AssessGrounding(intent, nil, answer); assessment.Pass() {
			t.Fatalf("%q passed without an external observation", answer)
		}
	}
}

// "No source files were read" reports what this run observed, not what the
// repository contains. Session 731 was reviewed for exactly that sentence
// while being honest about its evidence gap; a repository-wide absence in the
// same grammatical shape must still be reviewed.
func TestNegatedRunObservationIsNotARepositoryAbsence(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective:  "Investigate this project and analyze what this project is about.",
		Action:     contract.GroundingRead,
		Confidence: contract.GroundingConfidenceHigh,
	})
	for _, answer := range []string{
		"No source files were read beyond the README, so no implementation behavior was verified.",
		"No files were inspected in the docs directory.",
	} {
		if assessment := AssessGrounding(intent, nil, answer); !assessment.Pass() {
			t.Fatalf("%q was reviewed as a repository absence: %s", answer, assessment.Reason())
		}
	}
	if assessment := AssessGrounding(intent, nil, "No tests exist in this repository."); assessment.Pass() {
		t.Fatal("a repository-wide absence claim passed without a scan")
	}
}

// Session 812: "No tests, server, or binary were executed" was reviewed on
// both audits. The external-state check read "server ... executed" as a claim
// that the server ran, and the absence check read "no tests" as a repository
// conclusion. The negator introduces one coordinated subject list and
// distributes over every item, so the sentence reports what the run did not
// do — exactly the honest limitation the answer needs.
func TestNegationDistributesOverACoordinatedSubjectList(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective:  "Investigate this project and analyze what this project is about.",
		Action:     contract.GroundingRead,
		Confidence: contract.GroundingConfidenceHigh,
	})
	for _, answer := range []string{
		"No tests, server, or binary were executed: runtime behavior is entirely unobserved.",
		"No test files, server, or binary were executed.",
	} {
		if assessment := AssessGrounding(intent, nil, answer); !assessment.Pass() {
			t.Fatalf("%q was reviewed: %s", answer, assessment.Reason())
		}
		if claims := negativeClaims(answer); len(claims) != 0 {
			t.Fatalf("%q became an absence claim: %+v", answer, claims)
		}
	}

	// The negator governs its own clause only. A predicate before the list
	// closes the negation, a separate existence predicate keeps the
	// repository conclusion, and a discourse marker is not a list.
	for _, control := range []string{
		"No tests were run, but the deployment was executed.",
		"The server and binary were executed.",
		"No tests exist in this repository.",
		"No, the server was executed.",
	} {
		if verdict := AssessGrounding(intent, nil, control); verdict.Pass() {
			t.Fatalf("%q passed without an external observation or absence proof", control)
		}
	}
}

// The observed inventory already reported 110 test files, so a run report that
// no test was executed must not be promoted into "the project has no tests".
// The two statements are compatible: inventory establishes existence, only
// execution establishes pass/fail.
func TestObservedTestInventoryDoesNotContradictANegatedRunReport(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimStructureTests(110, true),
		claimRead("backend/Readme.md", "# Backend\n\n`go test ./...` runs the in-memory suite.\n", 45),
		claimRead("backend/cmd/web/main.go", "package main\n\nfunc main() {}\n", 202),
	}}}
	answer := "The project has 110 test files.\n\n" +
		"Testing (documented + inventory): the README describes `go test ./...` as in-memory mode and `-tags=integration` as Postgres-backed. I did not run either, so pass/fail status is unknown.\n\n" +
		"No tests, server, or binary were executed: runtime behavior is entirely unobserved."
	verdict := claimVerdict(turns, answer)
	if !verdict.Complete() {
		t.Fatalf("an honest unexecuted-test report was reviewed: %s", verdict.Reason)
	}
}

// "I make no claim about test coverage" is an epistemic limit. The noun after
// "no" is the claim the answer declines to make, not a repository artifact it
// asserts absent; the 110 test files the inventory reported are compatible with
// never having executed them.
func TestDecliningToClaimIsNotAnAbsenceConclusion(t *testing.T) {
	inventory := []Turn{{Results: []ToolOutcome{claimStructureTests(110, true)}}}
	for _, answer := range []string{
		"I make no claim about test coverage or pass/fail.",
		"The inventory reports 110 test files; I make no claim about test coverage because none were executed.",
	} {
		if assessment := AssessGrounding(claimContract(), inventory, answer); !assessment.Pass() {
			t.Fatalf("%q was reviewed as an absence claim: %s", answer, assessment.Reason())
		}
		if claims := negativeClaims(answer); len(claims) != 0 {
			t.Fatalf("%q produced absence claims: %+v", answer, claims)
		}
	}
	if assessment := AssessGrounding(claimContract(), nil, "No tests exist in this repository."); assessment.Pass() {
		t.Fatal("a repository-wide test absence still needs coverage")
	}
}

// "No static-vs-runtime comparison to report" is the same epistemic limit as
// "no contradiction to arbitrate": the run did not execute the runtime, so it
// cannot offer the comparison. It is not a claim that the repository lacks the
// pieces being compared.
func TestEpistemicReportLimitIsNotAbsence(t *testing.T) {
	for _, answer := range []string{
		"There is no static-vs-runtime comparison to report.",
		"There is no runtime behavior to describe from this inspection.",
	} {
		if claims := negativeClaims(answer); len(claims) != 0 {
			t.Fatalf("%q was treated as an absence claim: %+v", answer, claims)
		}
		if assessment := AssessGrounding(claimContract(), nil, answer); !assessment.Pass() {
			t.Fatalf("%q was reviewed: %s", answer, assessment.Reason())
		}
	}
}

// "Could not be read before the evidence window closed" reports this run's
// budget, not an inaccessible file. The model named the frontier it could not
// finish; completion must accept that limitation instead of demanding a call
// the run had no turn left to make.
func TestEvidenceWindowLimitIsNotAnUnverifiedSearchResult(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{
		claimListing("backend/internal/dependency", "Readme.md\n"),
		claimRead("backend/auth/bounded_context.go", "package auth\n", 40),
	}}}
	answer := "backend/internal/dependency was named as a frontier but its Readme could not be read before the evidence window closed."
	if assessment := AssessGrounding(claimContract(), turns, answer); !assessment.Pass() {
		t.Fatalf("a budget-bounded limitation was reviewed: %s", assessment.Reason())
	}

	// The same admission without the bound still reports an unusable result,
	// and a stem must not be attributed to an unrelated observed path.
	unbounded := "The authentication implementation could not be read, so behavior is unknown."
	assessment := AssessGrounding(claimContract(), turns, unbounded)
	if assessment.Pass() {
		t.Fatal("an unbounded inability report completed")
	}
	if strings.Contains(assessment.Reason(), "backend/auth") {
		t.Fatalf("an unrelated path was attributed to the admission: %s", assessment.Reason())
	}
}

// An unsupported absence conclusion is a claim-level repair: it names the
// subject and the inventory evidence it needs, not just a prose reason.
func TestNegativeConclusionCarriesAClaimRepair(t *testing.T) {
	assessment := AssessGrounding(claimContract(), nil, "No tests exist in this repository.")
	repairs := assessment.Repairs()
	if len(repairs) != 1 {
		t.Fatalf("repairs = %+v, want one absence claim", repairs)
	}
	repair := repairs[0]
	if repair.Category != "absence" || repair.Reference != "tests" || repair.Requirement == "" || repair.Action == "" {
		t.Fatalf("repair = %+v, want absence/tests/requirement/action", repair)
	}
}

// Multiple independent unsupported conclusions are repaired in one pass. The
// correction budget must be proportional to the claims, not to the answer's
// overall length.
func TestMultipleIndependentAbsenceClaimsProduceMultipleRepairs(t *testing.T) {
	answer := "No tests exist in this repository. No documentation exists in this repository."
	repairs := AssessGrounding(claimContract(), nil, answer).Repairs()
	if len(repairs) < 2 {
		t.Fatalf("repairs = %+v, want both absence claims", repairs)
	}
}

// Runtime-state and citation failures are claim-level repairs too: the model is
// told which observation is missing, not asked to rewrite the whole answer.
func TestRuntimeAndCitationClaimsCarryRepairs(t *testing.T) {
	runtime := AssessGrounding(claimContract(), nil, "The workflow ran successfully today.").Repairs()
	if len(runtime) != 1 || runtime[0].Category != "runtime" || runtime[0].Requirement == "" {
		t.Fatalf("runtime repairs = %+v", runtime)
	}
	turns := []Turn{{Results: []ToolOutcome{claimListing("lib", "search.ts\n")}}}
	citation := AssessGrounding(claimContract(), turns,
		"Search uses the index built in lib/search.ts (lib/search.ts).").Repairs()
	if len(citation) != 1 || citation[0].Category != "citation" || citation[0].Reference != "lib/search.ts" {
		t.Fatalf("citation repairs = %+v", citation)
	}
}

// "When the cart is missing" is a behavior condition, not a claim that the
// repository has no cart. The corrected deep answer described a redirect branch
// and was reviewed as an absence conclusion.
func TestConditionalAbsenceIsNotARepositoryClaim(t *testing.T) {
	answer := "The handler redirects to `/cart` when the cart is missing."
	if claims := negativeClaims(answer); len(claims) != 0 {
		t.Fatalf("a behavior condition became an absence claim: %+v", claims)
	}
	if assessment := AssessGrounding(claimContract(), nil, answer); !assessment.Pass() {
		t.Fatalf("a behavior condition was reviewed: %s", assessment.Reason())
	}
	if claims := negativeClaims("The cart is missing from the repository."); len(claims) == 0 {
		t.Fatal("an unconditional absence claim was ignored")
	}
}

// A package directory does not refute a claim about a check inside it.
func TestPackageDirectoryDoesNotRefuteConceptAbsence(t *testing.T) {
	turns := []Turn{{Results: []ToolOutcome{claimListing("backend", "productcatalog\ncart\ncheckout\n")}}}
	coverage := collectGroundingCoverage(turns)
	if got := coverage.contradicts("productcatalog existence check"); got != "" {
		t.Fatalf("a package listing refuted a concept absence: %s", got)
	}
	if got := coverage.contradicts("portfolio file"); got != "" {
		t.Fatalf("unexpected contradiction in fixture: %s", got)
	}
	coverage.observedPaths = append(coverage.observedPaths, "app/portfolio.tsx")
	if got := coverage.contradicts("portfolio file"); got != "app/portfolio.tsx" {
		t.Fatalf("a file claim was not refuted by its path: %s", got)
	}
}

// An explicit open question and next read is a limitation, not a claim about
// the inspected evidence.
func TestOpenQuestionAndPendingReadAreBounded(t *testing.T) {
	sentence := "The reach-persistence question stays open pending a read of the stock reserver and the Postgres projection."
	if !claimIsBounded(sentence) {
		t.Fatal("an explicit pending read was not treated as a limitation")
	}
}

// "The absence of a particular test is not evidence" is an epistemic
// limitation, not a repository test-absence conclusion.
func TestAbsenceOfEvidenceIsNotAnAbsenceClaim(t *testing.T) {
	answer := "The test inventory was truncated, so the absence of a particular test is not evidence."
	if claims := negativeClaims(answer); len(claims) != 0 {
		t.Fatalf("an epistemic limit became an absence claim: %+v", claims)
	}
	if assessment := AssessGrounding(claimContract(), nil, answer); !assessment.Pass() {
		t.Fatalf("an epistemic limit was reviewed: %s", assessment.Reason())
	}
}

// "At all" is an intensifier, not a repository-wide scope marker. It must not
// veto the same sentence's "in the inspected scope" qualification.
func TestIntensifierDoesNotWidenABoundedAbsence(t *testing.T) {
	answer := "There is no automated correctness gate at all beyond lint; no test files, CI workflow, or test runner were found in the inspected scope."
	if claims := negativeClaims(answer); len(claims) != 0 {
		t.Fatalf("bounded report became absence claims: %+v", claims)
	}
}

// "For the inspected scope" is the same bounded report as "in the inspected
// scope". A complete test inventory that reports zero tests supports it.
func TestAbsenceSupportedForInspectedScope(t *testing.T) {
	answer := "The absence of tests is supported for the inspected scope."
	if claims := negativeClaims(answer); len(claims) != 0 {
		t.Fatalf("bounded absence became a claim: %+v", claims)
	}
}

// Overlapping patterns can name one test gap as "known test", "tests", and
// "test files". The repair must name it once.
func TestOneTestGapProducesOneRepair(t *testing.T) {
	answer := "No known test runner dependency, no tests, and no test files were found."
	claims := negativeClaims(answer)
	if len(claims) != 1 || claims[0].Subject != "test" {
		t.Fatalf("claims = %+v, want one test claim", claims)
	}
}

// Coverage disclosure is presentation, not a claim. A project-wide answer is
// judged by the claims it makes and the evidence they require. Requiring a
// generic disclaimer made the runtime reward prose qualification instead of
// the missing observation, so the audit no longer has a coverage veto.
func TestAssessGroundingDoesNotRequireACoverageDisclaimer(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective:  "investigate this project and analyze what this project is about",
		Action:     contract.GroundingRead,
		Scope:      contract.GroundingScope{Kind: contract.ScopeRepository},
		Confidence: contract.GroundingConfidenceHigh,
	})
	partial := []Turn{{Results: []ToolOutcome{
		{
			ToolName: "read",
			Args:     map[string]any{"path": "app/page.tsx"},
			Result:   Result{Text: "export default function Page() { return null }"},
		},
		{
			ToolName: "structure",
			Args:     map[string]any{"view": "overview"},
			Result: Result{
				Text: "structural: UNPARSED\nparsed_files: 0 pending_files: 77",
				Details: map[string]any{
					"view": "overview", "parsed_files": 0, "pending_files": 77,
					"structural_complete": false, "inventory_complete": true, "truncated": true,
				},
			},
		},
	}}}
	for _, answer := range []string{
		"This project is a Next.js blog with a terminal UI.",
		"This is andev0x - Terminal Blog, a Next.js 16 personal developer blog with a terminal-style UI.",
		"According to package.json and app/layout.tsx, this project is a Next.js blog.",
		"This project is a Next.js blog. No tests exist in the inspected scope.",
	} {
		if assessment := AssessGrounding(intent, partial, answer); !assessment.Pass() {
			t.Fatalf("a supported project description required a disclaimer: %s\nissues: %+v", assessment.Reason(), assessment.Issues)
		}
	}

	// The removed disclosure check must not weaken a specific claim. A
	// repository-wide absence is still a review boundary without coverage.
	assessment := AssessGrounding(intent, partial, "This project contains no database.")
	if assessment.Pass() {
		t.Fatal("an unsupported repository-wide absence passed grounding")
	}
	found := false
	for _, issue := range assessment.Issues {
		if issue.Code == "unsupported_negative_conclusion" {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %+v, want unsupported_negative_conclusion", assessment.Issues)
	}
}

// An honest limitation about what the run executed is not a repository
// absence. The real project-analysis answer ended with "no build, test, or
// runtime session was executed" and was reviewed as if it claimed the
// repository had no tests.
func TestRunActivityListIsNotAnAbsenceClaim(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective:  "analyze what this project is about",
		Action:     contract.GroundingRead,
		Scope:      contract.GroundingScope{Kind: contract.ScopeRepository},
		Confidence: contract.GroundingConfidenceHigh,
	})
	answer := "The inspected implementation is the CLI entry point. No build, test, or runtime session was executed."
	if claims := negativeClaims(answer); len(claims) != 0 {
		t.Fatalf("run activity was parsed as absence claims: %+v", claims)
	}
	turns := []Turn{{Results: []ToolOutcome{{
		ToolName: "structure",
		Args:     map[string]any{"view": "tests"},
		Result: Result{
			Text:    "test_files: 592",
			Details: map[string]any{"view": "tests", "test_files": 592},
		},
	}}}}
	if assessment := AssessGrounding(intent, turns, answer); !assessment.Pass() {
		t.Fatalf("an honest run limitation was reviewed: %s", assessment.Reason())
	}

	// The control: a genuine repository absence is still reviewed.
	falseClaim := "No tests exist in this repository."
	if assessment := AssessGrounding(intent, turns, falseClaim); assessment.Pass() {
		t.Fatal("a repository-wide absence was accepted")
	}
}

// "No application or test run was performed" reports what this run did, not
// what the repository contains. The observed project-analysis answer used this
// wording while the inventory reported test files, and the audit treated the
// leading list item as the absent thing. Execution negation and entity absence
// are different claims: only the latter needs inventory coverage.
func TestNoApplicationOrTestRunIsNotAnAbsenceClaim(t *testing.T) {
	intent := informationalContract(&contract.IntentGrounding{
		Objective:  "investigate this project and analyze what this project is about",
		Action:     contract.GroundingInvestigate,
		Scope:      contract.GroundingScope{Kind: contract.ScopeRepository},
		Confidence: contract.GroundingConfidenceHigh,
	})
	turns := []Turn{{Results: []ToolOutcome{claimStructureTests(5, true)}}}

	for _, answer := range []string{
		"No application or test run was performed.",
		"No application or test suite was performed.",
		"No application or test was executed.",
	} {
		if claims := negativeClaims(answer); len(claims) != 0 {
			t.Fatalf("%q produced absence claims: %+v", answer, claims)
		}
		if assessment := AssessGrounding(intent, turns, answer); !assessment.Pass() {
			t.Fatalf("%q was reviewed: %s", answer, assessment.Reason())
		}
	}

	// The control: a genuine repository absence still needs coverage. The
	// observed test files contradict it, so accepting it would hide the bug.
	if assessment := AssessGrounding(intent, turns,
		"No application or test files exist in this repository."); assessment.Pass() {
		t.Fatal("a genuine repository absence was accepted")
	}
}
