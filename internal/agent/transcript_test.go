package agent

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// An answer-required refusal is a control decision, not work. It must not
// survive into the displayed or persisted transcript, and its call block must
// be removed with it so a resumed conversation never has a tool call without a
// result. A real outcome in the same turn is kept.
func TestTranscriptTurnRemovesAnswerRequiredControlRefusals(t *testing.T) {
	turn := Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "The answer is already established."},
				ai.ToolCall{ID: "real", Name: "read", Arguments: map[string]any{"path": "a.go"}},
				ai.ToolCall{ID: "stale", Name: "ls", Arguments: map[string]any{"path": "."}},
			},
			StopReason: ai.StopToolUse,
		},
		Results: []ToolOutcome{
			{CallID: "real", ToolName: "read", Result: Result{Text: "package a"}},
			{CallID: "stale", ToolName: "ls", Result: Result{
				Text: "No tool is available in the current task stage.", IsError: true,
				CandidateRejected: true, AnswerRequired: true,
			}},
		},
	}

	got := TranscriptTurn(turn)
	if len(got.Results) != 1 || got.Results[0].CallID != "real" {
		t.Fatalf("results = %+v, want only the real outcome", got.Results)
	}
	if calls := got.Assistant.ToolCalls(); len(calls) != 1 || calls[0].ID != "real" {
		t.Fatalf("calls = %+v, want only the real call", calls)
	}
	if got.Assistant.Text() != "The answer is already established." {
		t.Fatalf("text = %q, want the answer preserved", got.Assistant.Text())
	}
	if got.Assistant.StopReason != ai.StopToolUse {
		t.Fatalf("stop reason = %q, want tool_use while a real call remains", got.Assistant.StopReason)
	}
}

// A call-only turn whose calls were all answer-required refusals has no
// transcript record at all. Keeping it would put red cards after the answer.
func TestTranscriptTurnDropsACallOnlyControlRefusalTurn(t *testing.T) {
	turn := Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "stale", Name: "ls", Arguments: map[string]any{"path": "."}},
			},
			StopReason: ai.StopToolUse,
		},
		Results: []ToolOutcome{{
			CallID: "stale", ToolName: "ls", Result: Result{
				Text: "No tool is available in the current task stage.", IsError: true,
				CandidateRejected: true, AnswerRequired: true,
			},
		}},
	}

	got := TranscriptTurn(turn)
	if len(got.Results) != 0 || len(got.Assistant.Content) != 0 {
		t.Fatalf("projection = %+v, want an empty transcript turn", got)
	}
}

// A grounding refusal is not an answer-required refusal: it is a meaningful
// attempted action and remains visible.
func TestTranscriptTurnKeepsNonAnswerRefusals(t *testing.T) {
	turn := Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.ToolCall{ID: "ungrounded", Name: "edit", Arguments: map[string]any{"path": "a.go"}},
			},
			StopReason: ai.StopToolUse,
		},
		Results: []ToolOutcome{{
			CallID: "ungrounded", ToolName: "edit", Result: Result{
				Text: "Action not grounded in the request.", IsError: true,
				CandidateRejected: true,
			},
		}},
	}

	got := TranscriptTurn(turn)
	if len(got.Results) != 1 || len(got.Assistant.ToolCalls()) != 1 {
		t.Fatalf("projection = %+v, want the grounding refusal preserved", got)
	}
}
