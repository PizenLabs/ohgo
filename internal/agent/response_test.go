package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// A response transaction commits once. A second commit attempt is a no-op,
// not a second answer, so a host retry cannot duplicate the assistant message.
func TestResponseCommitIsIdempotent(t *testing.T) {
	loop := &Loop{}
	loop.beginResponse()
	turns := []Turn{{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "the answer"}},
	}}}
	live := []Message{AssistantMessage{Message: turns[0].Assistant}}
	if !loop.finalizeResponse(turns, &live, "") {
		t.Fatal("the first commit was refused")
	}
	if loop.finalizeResponse(turns, &live, "") {
		t.Fatal("a second commit was accepted")
	}
	audit := loop.LastResponseAudit()
	if audit.State != ResponseCommitted {
		t.Fatalf("state = %s, want COMMITTED", audit.State)
	}
	if len(live) != 1 || live[0].(AssistantMessage).Message.Text() != "the answer" {
		t.Fatalf("live conversation = %+v, want one answer", live)
	}
}

// A cancellation after a rejected draft must not publish the draft or commit
// a partial answer. The task is interrupted, not complete.
func TestCancelledRunDoesNotCommitARejectedDraft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	loop, err := New(Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			calls++
			if calls == 1 {
				return ai.NewCompletedStream(ai.AssistantMessage{
					Content: []ai.ContentBlock{ai.TextContent{Text: "a premature draft"}},
				}), nil
			}
			cancel()
			return nil, context.Canceled
		},
		Tools:                   []Tool{&reviewReadTool{}},
		AnswerReview:            func([]Turn, string) string { return "the draft is not supported" },
		CompletionReviewRequest: "Repair the answer: {{reason}}",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(ctx, []Message{UserMessage{Text: "analyze this"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if len(turns) != 1 || turns[0].Answer != AnswerSuperseded {
		t.Fatalf("turns = %+v, want one internal draft superseded by the correction", turns)
	}
	audit := loop.LastResponseAudit()
	if audit.State != ResponseCancelled {
		t.Fatalf("state = %s, want CANCELLED", audit.State)
	}
	if audit.Rejected != 1 {
		t.Fatalf("rejected drafts = %d, want 1", audit.Rejected)
	}
}

// A retry is part of one investigation, not a second user-visible response.
// A failed call followed by a successful retry and one answer commits exactly
// once, and the pending failure is recovered.
func TestRetryRecoveryCommitsOneAnswer(t *testing.T) {
	read := &retryReadTool{}
	index := 0
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"path": "a.go"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "read-2", Name: "read", Arguments: map[string]any{"path": "a.go"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "The project is a Go agent."}}},
	}
	loop, err := New(Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			message := script[index]
			index++
			return ai.NewCompletedStream(message), nil
		},
		Tools:    []Tool{read},
		MaxTurns: 6,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "analyze this project"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if read.calls != 2 {
		t.Fatalf("read calls = %d, want 2 (failure and retry)", read.calls)
	}
	if got := turns[len(turns)-1].Assistant.Text(); got != "The project is a Go agent." {
		t.Fatalf("final answer = %q", got)
	}
	audit := loop.LastResponseAudit()
	if audit.State != ResponseCommitted {
		t.Fatalf("state = %s, want COMMITTED", audit.State)
	}
	if audit.ToolCalls != 2 {
		t.Fatalf("audited tool calls = %d, want 2", audit.ToolCalls)
	}
	if audit.RetriedToolCalls != 1 || audit.RedundantToolCalls != 0 {
		t.Fatalf("retry metrics = retried:%d redundant:%d, want 1/0",
			audit.RetriedToolCalls, audit.RedundantToolCalls)
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: claimContract(),
	})
	if !verdict.Complete() {
		t.Fatalf("recovered retry was not completable: %s", verdict.Reason)
	}
}

type retryReadTool struct{ calls int }

func (*retryReadTool) Name() string                   { return "read" }
func (*retryReadTool) Description() string            { return "read a file" }
func (*retryReadTool) Parameters() *jsonschema.Schema { return nil }
func (*retryReadTool) Approval() Approval             { return Approval{Tier: TierRead} }
func (*retryReadTool) Exclusive(map[string]any) bool  { return false }
func (t *retryReadTool) Execute(_ Context, _ map[string]any) Result {
	t.calls++
	if t.calls == 1 {
		return Result{Text: "not found", IsError: true, FailureClass: ToolFailureInsufficient}
	}
	return Result{Text: "package a"}
}

// A repeated identical successful observation is redundant evidence. The loop
// answers it from the observation already in hand; the audit reports the reuse
// and the duplicate without executing the tool again.
func TestRepeatedSuccessfulObservationIsAuditedAsRedundant(t *testing.T) {
	read := &reviewReadTool{text: "source body"}
	index := 0
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "read-1", Name: "read", Arguments: map[string]any{"path": "components/TerminalBlog.tsx"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "read-2", Name: "read", Arguments: map[string]any{"path": "components/TerminalBlog.tsx"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "The component renders the blog."}}},
	}
	loop, err := New(Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(context.Context, ai.Request) (*ai.Stream, error) {
			message := script[index]
			index++
			return ai.NewCompletedStream(message), nil
		},
		Tools:    []Tool{read},
		MaxTurns: 6,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "describe the component"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if read.n.Load() != 1 {
		t.Fatalf("read executions = %d, want the second call reused", read.n.Load())
	}
	audit := loop.LastResponseAudit()
	if audit.ReusedObservations != 1 {
		t.Fatalf("reused observations = %d, want 1", audit.ReusedObservations)
	}
	if audit.RedundantToolCalls != 1 {
		t.Fatalf("redundant calls = %d, want 1", audit.RedundantToolCalls)
	}
}

// A revised answer turn may have tool results after its assistant message. The
// live conversation rewrite must update the assistant block, never overwrite
// the evidence a later request still needs.
func TestAdoptAnswerTextRewritesTheAssistantNotTheLastToolResult(t *testing.T) {
	turns := []Turn{{
		Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "candidate"}}},
		Results: []ToolOutcome{{
			CallID: "c1", ToolName: "read", Result: Result{Text: "evidence"},
		}},
	}}
	live := []Message{
		AssistantMessage{Message: turns[0].Assistant},
		ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "evidence"}}},
	}
	adoptAnswerText(turns, &live, "repaired answer")
	if got := turns[0].Assistant.Text(); got != "repaired answer" {
		t.Fatalf("turn text = %q, want the repaired answer", got)
	}
	if got := live[0].(AssistantMessage).Message.Text(); got != "repaired answer" {
		t.Fatalf("live assistant = %q, want the repaired answer", got)
	}
	if got := live[1].(ToolResultMessage).Text(); got != "evidence" {
		t.Fatalf("tool result = %q, want the evidence preserved", got)
	}
}

// A finalizer that returns no text has withheld the draft on purpose. The
// response transaction must commit the empty result, not resurrect the raw
// draft the gate removed (for example, a copied runtime-state block).
func TestFinalizerEmptyTextDoesNotRestoreTheRawDraft(t *testing.T) {
	loop := &Loop{cfg: Config{FinalizeAnswer: func([]Turn, string, string) FinalizedAnswer {
		return FinalizedAnswer{Text: ""}
	}}}
	loop.beginResponse()
	turns := []Turn{{Assistant: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "# Runtime state\nsecret directive"}},
	}}}
	live := []Message{AssistantMessage{Message: turns[0].Assistant}}
	if !loop.finalizeResponse(turns, &live, "") {
		t.Fatal("the commit was refused")
	}
	if got := turns[0].Assistant.Text(); got != "" {
		t.Fatalf("committed text = %q, want the gate's empty result", got)
	}
	if got := live[0].(AssistantMessage).Message.Text(); got != "" {
		t.Fatalf("live text = %q, want the gate's empty result", got)
	}
}
