package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// A projection that reports a required capability the runtime cannot perform
// must stop the run before the provider is called.
//
// The failure this defends against is specific and expensive: the model is
// asked to act, proposes an action the task surface withholds, then writes prose
// explaining the limit. The turn is spent, and nothing was executed. The loop
// owning the request shape means the request is never sent at all.
func TestMissingCapabilityStopsBeforeTheModelTurn(t *testing.T) {
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "should never be asked"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{newScriptedTool("read", "result")},
		Project: func(ProjectionRequest) ProjectionResult {
			report := ProjectionReport{
				MissingCapabilities: []string{"mutate"},
				StopReason:          "the runtime cannot perform a capability this objective requires: repository mutation (write or edit)",
			}
			return ProjectionResult{Tools: []Tool{}, Report: report}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "convert this project to Next.js"}})
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("err = %v, want %v", err, ErrCapabilityUnavailable)
	}
	if len(turns) != 0 {
		t.Fatalf("turns = %d, want 0: no model turn may run without the capability", len(turns))
	}
	if got := len(requests()); got != 0 {
		t.Fatalf("provider requests = %d, want 0", got)
	}

	verdict, reason := ClassifyRun(err, CompletionInput{}, false)
	if verdict != RunBlocked {
		t.Fatalf("verdict = %v, want RunBlocked", verdict)
	}
	if reason == "" {
		t.Fatal("blocked run carried no reason")
	}
}

// The same projection must not block a run when nothing is missing: an empty
// MissingCapabilities list is the ready verdict, not an implicit failure.
func TestReadyProjectionStillReachesTheModel(t *testing.T) {
	stream, requests := scriptedStream(
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "answer"}}, StopReason: ai.StopEndTurn},
	)
	loop, err := New(Config{
		Model:  catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream: stream,
		Tools:  []Tool{newScriptedTool("read", "result")},
		Project: func(ProjectionRequest) ProjectionResult {
			return ProjectionResult{Report: ProjectionReport{
				RequiredCapabilities: []string{"read"},
				AvailableOperations:  []string{"read"},
			}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "what is this?"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(requests()); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
}

// A proposal for a capability the task surface withholds is classified as a
// capability rejection, not an execution failure, so completion does not count
// it as a failed call.
func TestWithheldToolProposalIsACapabilityRejection(t *testing.T) {
	visible := newScriptedTool("read", "visible result")
	hidden := newScriptedTool("write", "should not run")
	stream, _ := scriptedStream(
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
	if len(turns) == 0 || len(turns[0].Results) != 1 {
		t.Fatalf("turns = %#v", turns)
	}
	result := turns[0].Results[0].Result
	if result.FailureClass != ToolFailureCapabilityUnavailable {
		t.Fatalf("failure class = %q, want %q", result.FailureClass, ToolFailureCapabilityUnavailable)
	}
	if !result.CandidateRejected {
		t.Fatal("a withheld proposal must be a candidate rejection")
	}
}
