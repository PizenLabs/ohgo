package agent

import (
	"strings"
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

// A rejected candidate is model context, not a user-visible answer. It must
// not reach the transcript, plain output, persistence, or the next request.
func TestTranscriptTurnHidesSuppressedCandidate(t *testing.T) {
	turn := Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "The first, unsupported answer."}},
		},
		Suppressed: true,
	}
	if got := TranscriptTurn(turn); got.Assistant.Text() != "" || len(got.Results) != 0 {
		t.Fatalf("suppressed turn was published: %+v", got)
	}
	if got := ConversationMessages([]Turn{turn}); len(got) != 0 {
		t.Fatalf("suppressed turn entered the next request: %+v", got)
	}
}

// A candidate superseded by a bounded correction is internal repair context,
// not an answer a host may display or persist. Its tool outcomes are still
// real work and stay in the transcript.
func TestTranscriptTurnHidesSupersededCandidateButKeepsItsWork(t *testing.T) {
	turn := Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "The first, unsupported answer."},
				ai.ToolCall{ID: "read", Name: "read", Arguments: map[string]any{"path": "a.go"}},
			},
			StopReason: ai.StopToolUse,
		},
		Superseded: true,
		Results: []ToolOutcome{
			{CallID: "read", ToolName: "read", Result: Result{Text: "package a"}},
		},
	}
	got := TranscriptTurn(turn)
	if text := got.Assistant.Text(); text != "" {
		t.Fatalf("superseded answer reached the transcript: %q", text)
	}
	if len(got.Results) != 1 || got.Results[0].CallID != "read" {
		t.Fatalf("superseded turn lost its tool outcome: %+v", got.Results)
	}
	if messages := ConversationMessages([]Turn{turn}); len(messages) != 2 {
		t.Fatalf("superseded turn projection = %+v, want the call and its result", messages)
	}
}

// Prose followed by a tool call is investigation, not a conclusion. The call
// and its evidence stay; the abandoned answer text does not.
func TestTranscriptTurnDropsProseBeforeToolCall(t *testing.T) {
	turn := Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "Here is the full answer I will replace after one more read."},
				ai.ToolCall{ID: "read", Name: "read", Arguments: map[string]any{"path": "a.go"}},
			},
			StopReason: ai.StopToolUse,
		},
		SuppressText: true,
		Results: []ToolOutcome{
			{CallID: "read", ToolName: "read", Result: Result{Text: "package a"}},
		},
	}
	got := TranscriptTurn(turn)
	if text := got.Assistant.Text(); text != "" {
		t.Fatalf("prose published: %q", text)
	}
	if calls := got.Assistant.ToolCalls(); len(calls) != 1 || calls[0].ID != "read" {
		t.Fatalf("calls = %+v, want the read call", calls)
	}
	if len(got.Results) != 1 || got.Results[0].CallID != "read" {
		t.Fatalf("results = %+v, want the read evidence", got.Results)
	}
}

// The projection is a value, but its Content slice shares the loop's live
// conversation. Stripping display text must not overwrite the turn the
// completion authority still reads.
func TestTranscriptTurnDoesNotMutateTheLoopTurn(t *testing.T) {
	turn := Turn{
		SuppressText: true,
		Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{
			ai.TextContent{Text: "I will inspect the parser."},
			ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "x.go"}},
		}},
	}
	projected := TranscriptTurn(turn)
	if text := projected.Assistant.Text(); strings.TrimSpace(text) != "" {
		t.Fatalf("projected text = %q, want the narration removed", text)
	}
	if text := turn.Assistant.Text(); text != "I will inspect the parser." {
		t.Fatalf("the loop turn was mutated: %q", text)
	}
}
