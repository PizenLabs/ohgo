package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/security"
)

func TestCandidateRejectionIsNotCompletionFailure(t *testing.T) {
	visible := newScriptedTool("read", "visible result")
	hidden := newScriptedTool("write", "should not run")
	stream, requests := scriptedStream(
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "write", Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{visible, hidden},
		Project: func(ProjectionRequest) ProjectionResult {
			return ProjectionResult{Tools: []Tool{visible}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "read only"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %#v", turns)
	}
	outcome := turns[0].Results[0]
	if !outcome.Result.IsError || !outcome.Result.CandidateRejected {
		t.Fatalf("outcome = %#v, want candidate rejection", outcome.Result)
	}
	if len(hidden.invoked()) != 0 {
		t.Fatal("a withheld tool must not execute")
	}
	if completion := EvaluateCompletion(turns, false); !completion.Complete() {
		t.Fatalf("completion = %#v, want complete", completion)
	}
	// The first request offered only the visible tool; the second request is
	// the model's answer after the runtime refused the withheld call.
	if len(requests()) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests()))
	}
	for _, req := range requests() {
		if len(req.Tools) != 1 || req.Tools[0].Name != "read" {
			t.Fatalf("offered tools = %#v", req.Tools)
		}
	}
}

func TestProjectionCannotAddTools(t *testing.T) {
	visible := newScriptedTool("read", "visible")
	fabricated := newScriptedTool("fabricated", "not built")
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}, StopReason: ai.StopEndTurn},
	)
	var report ProjectionReport
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{visible},
		Project: func(ProjectionRequest) ProjectionResult {
			return ProjectionResult{Tools: []Tool{visible, fabricated}}
		},
		OnProjection: func(r ProjectionReport) { report = r },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "hi"}}); err != nil {
		t.Fatal(err)
	}
	reqs := requests()
	if len(reqs) != 1 || len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "read" {
		t.Fatalf("request tools = %#v", reqs)
	}
	if !strings.Contains(report.Reason, "outside the loop's tool set") {
		t.Fatalf("projection report = %#v", report)
	}
}

func TestProjectionCannotSubstituteAToolWithTheSameName(t *testing.T) {
	// A projector that returns a different tool under a known name is still
	// limited to the loop's constructed instance. Name-only intersection
	// would let a fabricated tool replace the authorizer-approved capability.
	constructed := newScriptedTool("read", "real result")
	fabricated := newScriptedTool("read", "fabricated result")
	stream, _ := scriptedStream(
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn},
	)
	var report ProjectionReport
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{constructed},
		Project: func(ProjectionRequest) ProjectionResult {
			return ProjectionResult{Tools: []Tool{fabricated}}
		},
		OnProjection: func(r ProjectionReport) { report = r },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "read"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(constructed.invoked()); got != 1 {
		t.Fatalf("constructed tool ran %d times, want 1", got)
	}
	if got := len(fabricated.invoked()); got != 0 {
		t.Fatalf("fabricated tool ran %d times, want 0", got)
	}
	if len(report.ToolNames) != 1 || report.ToolNames[0] != "read" {
		t.Fatalf("audit tool names = %#v", report.ToolNames)
	}
}

func TestAuthorizationDenialIsNotACompletionFailure(t *testing.T) {
	// A refused call never executed. If the model then answers a read-only
	// request, the objective can still be complete: the refusal is a control
	// decision, not an unrecovered tool failure.
	tool := newScriptedTool("read", "contents")
	authorizer := &scriptedAuthorizer{decisions: []security.Decision{{
		Outcome: security.OutcomeDeny,
		Rule:    "test-deny",
		Reason:  "not allowed",
	}}}
	turns, scripted := runWithAuthorizer(t, tool, authorizer)
	if len(scripted.invoked()) != 0 {
		t.Fatal("a denied tool executed")
	}
	if len(turns) == 0 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %#v", turns)
	}
	if !turns[0].Results[0].Refused() {
		t.Fatalf("outcome = %#v, want a refusal", turns[0].Results[0])
	}
	if completion := EvaluateCompletion(turns, false); !completion.Complete() {
		t.Fatalf("completion = %s (%s), want complete after a denied call", completion.State, completion.Reason)
	}
}

func TestApprovalRejectionIsNotACompletionFailure(t *testing.T) {
	// A human refusal is also a control decision. It must not leave a
	// pending-failure shadow over an otherwise answered read-only objective.
	tool := newScriptedTool("read", "contents")
	authorizer := &scriptedAuthorizer{
		decisions:     []security.Decision{{Outcome: security.OutcomeApprove}},
		approveResult: false,
	}
	turns, scripted := runWithAuthorizer(t, tool, authorizer)
	if len(scripted.invoked()) != 0 {
		t.Fatal("a rejected tool executed")
	}
	if !turns[0].Results[0].Refused() {
		t.Fatalf("outcome = %#v, want a refusal", turns[0].Results[0])
	}
	if completion := EvaluateCompletion(turns, false); !completion.Complete() {
		t.Fatalf("completion = %s (%s), want complete after a rejected call", completion.State, completion.Reason)
	}
}

func TestProjectionDoesNotBypassAuthorizer(t *testing.T) {
	tool := newScriptedTool("read", "ok")
	authorizer := &scriptedAuthorizer{decisions: []security.Decision{{
		Outcome: security.OutcomeDeny,
		Rule:    "test-deny",
		Reason:  "projection must not widen authority",
	}}}
	loop, err := New(Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    oneCallStream("read", map[string]any{"value": "x"}),
		Tools:     []Tool{tool},
		Authorize: authorizer,
		Project: func(ProjectionRequest) ProjectionResult {
			return ProjectionResult{Tools: []Tool{tool}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tool.invoked()) != 0 {
		t.Fatal("a projected tool executed despite a DENY decision")
	}
	if len(turns) == 0 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %#v", turns)
	}
	result := turns[0].Results[0]
	if result.Authorization == nil || result.Authorization.Outcome != security.OutcomeDeny {
		t.Fatalf("authorization = %#v", result.Authorization)
	}
	if !result.Result.IsError {
		t.Fatal("a denied call must report an error result")
	}
}

func TestEmptyProjectionFallsBackToLiveContext(t *testing.T) {
	tool := newScriptedTool("read", "ok")
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{tool},
		Project: func(ProjectionRequest) ProjectionResult {
			return ProjectionResult{Messages: []Message{}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	reqs := requests()
	if len(reqs) != 1 || len(reqs[0].Messages) != 1 {
		t.Fatalf("request messages = %#v, want the live user turn", reqs)
	}
}

func TestProviderFailureWithProjectionDoesNotClaimCompletion(t *testing.T) {
	tool := newScriptedTool("read", "ok")
	calls := 0
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("provider unavailable")
		}
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		}), nil
	}
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{tool},
		Project: func(ProjectionRequest) ProjectionResult {
			return ProjectionResult{Tools: []Tool{tool}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "go"}})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("err = %v, want provider failure", err)
	}
	if completion := EvaluateCompletion(turns, false); completion.Complete() {
		t.Fatal("a provider failure must not be reported as completion")
	}
}

func TestProjectionReportIsEmitted(t *testing.T) {
	tool := newScriptedTool("read", "visible")
	stream, _ := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}, StopReason: ai.StopEndTurn},
	)
	var reports []ProjectionReport
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{tool},
		Project: func(ProjectionRequest) ProjectionResult {
			return ProjectionResult{Tools: []Tool{tool}}
		},
		OnProjection: func(r ProjectionReport) { reports = append(reports, r) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || !reports[0].Applied {
		t.Fatalf("reports = %#v", reports)
	}
	if len(reports[0].ToolNames) != 1 || reports[0].ToolNames[0] != "read" {
		t.Fatalf("report tools = %#v", reports[0].ToolNames)
	}
}
