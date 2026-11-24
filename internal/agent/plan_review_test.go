package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// scriptedPlanTool is a plan owner with no dependency on the execution tools
// package, which would import this package back.
type scriptedPlanTool struct {
	mu    sync.Mutex
	items []string
	calls int
}

func (t *scriptedPlanTool) Name() string        { return "todo" }
func (t *scriptedPlanTool) Description() string { return "reconcile the plan" }
func (t *scriptedPlanTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (t *scriptedPlanTool) Approval() Approval            { return Approval{Tier: TierRead} }
func (t *scriptedPlanTool) Exclusive(map[string]any) bool { return true }
func (t *scriptedPlanTool) ActionClasses() []ActionClass {
	return []ActionClass{ActionPlan}
}

func (t *scriptedPlanTool) OpenPlanItems() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.items...)
}

func (t *scriptedPlanTool) Execute(Context, map[string]any) Result {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	t.items = nil
	return Result{Text: "Todo: 1/1 done"}
}

// An open plan is a publication precondition, not evidence. The first answer is
// superseded into a correction turn that keeps the plan tool reachable; the
// model reconciles the plan and the corrected answer commits. Without the
// correction the durable task would refuse the completion after the answer.
func TestOpenPlanIsReconciledBeforeTheAnswerCommits(t *testing.T) {
	plan := &scriptedPlanTool{items: []string{"trace the pipeline"}}
	stream := &reviewStream{responses: []ai.AssistantMessage{
		reviewAnswer("The project indexes a repository into a graph."),
		{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "call_todo", Name: "todo", Arguments: map[string]any{
					"op": "done", "task": "trace the pipeline",
				}},
			},
			StopReason: ai.StopToolUse,
		},
		reviewAnswer("The project indexes a repository into a graph."),
	}}
	loop := newReviewLoop(t, stream.fn(), plan, reviewIntent(), testReviewDirective)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "what is this project?"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if plan.calls != 1 {
		t.Fatalf("plan executions = %d, want the reconciliation call", plan.calls)
	}
	if len(turns) < 3 || turns[0].Answer != AnswerSuperseded {
		t.Fatalf("turns = %+v, want the unanswered candidate superseded", turns)
	}
	if turns[len(turns)-1].Answer != AnswerCommitted {
		t.Fatal("the reconciled answer was not committed")
	}
	if len(stream.requests) < 2 {
		t.Fatalf("requests = %d, want the correction request", len(stream.requests))
	}
	offered := false
	for _, spec := range stream.requests[1].Tools {
		if spec.Name == "todo" {
			offered = true
		}
	}
	if !offered {
		t.Fatal("the correction request did not offer the plan tool")
	}
	if !strings.Contains(lastRuntimeState(t, stream.requests[1]), "task plan still has 1 open item") {
		t.Fatalf("correction request did not name the open plan: %+v", stream.requests[1].Messages)
	}
}

// The correction allowance buys evidence, not bookkeeping. A correction that
// spends its last call on a read must still be allowed to reconcile the plan;
// otherwise the runtime closes the only tool that can make the task completable.
func TestPlanToolSurvivesSpentCorrectionBudget(t *testing.T) {
	plan := &scriptedPlanTool{items: []string{"trace the pipeline"}}
	read := &reviewReadTool{text: terminalBlogSource}
	intent := reviewIntent(contract.FacetTests)
	stream := &reviewStream{responses: []ai.AssistantMessage{
		reviewAnswer("This is a Next.js blog."),
		{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "call_read", Name: "read", Arguments: map[string]any{"path": "components/TerminalBlog.tsx"}},
			},
			StopReason: ai.StopToolUse,
		},
		{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "call_todo", Name: "todo", Arguments: map[string]any{
					"op": "done", "task": "trace the pipeline",
				}},
			},
			StopReason: ai.StopToolUse,
		},
		reviewAnswer("Tests: 4 associated test files."),
	}}
	loop, err := New(Config{
		Model:                   catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:                  stream.fn(),
		Tools:                   []Tool{read, plan},
		Grounding:               intent.Grounding,
		CorrectionToolCalls:     1,
		CompletionReviewRequest: testReviewDirective,
		AnswerReview: func(turns []Turn, answer string) string {
			return ReviewInformationalAnswer(intent, turns, answer)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "report the tests"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if plan.calls != 1 {
		t.Fatalf("plan executions = %d, want the plan call after the allowance was spent", plan.calls)
	}
	if turns[len(turns)-1].Answer != AnswerCommitted {
		t.Fatal("the reconciled answer was not committed")
	}
	var refused bool
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.ToolName == "todo" && outcome.Result.IsError {
				refused = true
			}
		}
	}
	if refused {
		t.Fatal("the correction budget refused the plan reconciliation")
	}
}

// lastRuntimeState returns the final runtime-state message of a request.
func lastRuntimeState(t *testing.T, req ai.Request) string {
	t.Helper()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if state, ok := req.Messages[i].(ai.RuntimeStateMessage); ok {
			return state.Text
		}
	}
	return ""
}
