package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/contract"
)

func exhaustedToolResult() Result {
	return Result{
		Text: "budget exhausted", IsError: true,
		Exhausted: &budget.ExhaustedError{Dimension: budget.DimensionBackgroundJobs, Limit: 1, Used: 1},
	}
}

type exhaustedTool struct{ *scriptedTool }

func (t *exhaustedTool) Execute(Context, map[string]any) Result { return exhaustedToolResult() }

// A tool that refuses work for budget reasons must surface a resource stop to
// the loop, not only an ordinary failed result the model could ignore. The
// completed turn is kept so its evidence is not lost.
func TestToolBudgetRefusalStopsTheRunAfterTheTurn(t *testing.T) {
	tool := &exhaustedTool{scriptedTool: newScriptedTool("bash", "ok")}
	stream, _ := scriptedStream(budgetToolCallMessage("1",
		ai.ToolCall{ID: "1", Name: "bash", Arguments: map[string]any{"value": "x"}}))
	loop, err := New(Config{
		Model: budgetTestModel(), Stream: stream, Tools: []Tool{tool},
		Budget: budget.New(budget.Limits{Turns: 5, ModelCalls: 5, ToolCalls: 5}),
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("err = %v, want budget.ErrExhausted", err)
	}
	if len(turns) != 1 || len(turns[0].Results) != 1 || !turns[0].Results[0].Result.IsError {
		t.Fatalf("turn evidence lost: %+v", turns)
	}
}

// Budget-completion is Phase 4 completion with one narrow Phase 5 allowance:
// a verified mutation whose closing turn was removed by exhaustion. An
// informational read or an unverified change gains nothing.
func TestEvaluateBudgetCompletionOnlyClosesVerifiedMutations(t *testing.T) {
	write := Turn{Results: []ToolOutcome{{
		ToolName: "write", Result: Result{Text: "wrote", Mutates: true},
	}}}
	verify := Turn{Results: []ToolOutcome{{
		ToolName: "bash", Result: Result{
			Text: "ok", Verifies: true,
			Verification: &VerificationEvidence{Check: contract.CheckTest, Scopes: []VerificationScope{{All: true}}},
		},
	}}}
	read := Turn{Results: []ToolOutcome{{
		ToolName: "read", Result: Result{Text: "contents"},
	}}}
	mutation := contract.IntentContract{
		Objective:           "Fix the failing tests.",
		Kind:                contract.ObjectiveMutation,
		BehavioralObjective: true,
		Verification: []contract.VerificationRequirement{{
			Kind:  contract.VerifyScope,
			Scope: ".",
			Check: contract.CheckTest,
		}},
	}
	informational := contract.IntentContract{Objective: "Explain this.", Kind: contract.ObjectiveInformational}

	if got := EvaluateBudgetCompletion(CompletionInput{Turns: []Turn{write, verify}, Contract: mutation}, true); !got.Complete() {
		t.Fatalf("verified mutation after exhaustion = %s (%s), want COMPLETED", got.State, got.Reason)
	}
	if got := EvaluateBudgetCompletion(CompletionInput{Turns: []Turn{write, verify}, Contract: mutation}, false); got.Complete() {
		t.Fatal("a missing closing turn completed without exhaustion")
	}
	if got := EvaluateBudgetCompletion(CompletionInput{Turns: []Turn{write}, Contract: mutation}, true); got.Complete() {
		t.Fatal("exhaustion completed an unverified mutation")
	}
	if got := EvaluateBudgetCompletion(CompletionInput{Turns: []Turn{read}, Contract: informational}, true); got.Complete() {
		t.Fatal("exhaustion completed a read-only run that never answered")
	}
}
