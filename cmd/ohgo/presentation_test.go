package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The plain transcript must not show an answer-required control refusal as a
// failed tool. Only work that actually ran belongs in the output.
func TestPrintTurnOmitsAnswerRequiredControlRefusals(t *testing.T) {
	turn := agent.Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{
				ai.TextContent{Text: "The answer is established."},
				ai.ToolCall{ID: "real", Name: "read", Arguments: map[string]any{"path": "a.go"}},
				ai.ToolCall{ID: "stale", Name: "ls", Arguments: map[string]any{"path": "."}},
			},
			StopReason: ai.StopToolUse,
		},
		Results: []agent.ToolOutcome{
			{CallID: "real", ToolName: "read", Result: agent.Result{Text: "package a"}},
			{CallID: "stale", ToolName: "ls", Result: agent.Result{
				Text: "No tool is available in the current task stage.", IsError: true,
				CandidateRejected: true, AnswerRequired: true,
			}},
		},
		Answer: agent.AnswerCommitted,
	}

	var out bytes.Buffer
	printTurn(&out, turn)
	text := out.String()
	if !strings.Contains(text, "The answer is established.") {
		t.Fatalf("output = %q, want the answer", text)
	}
	if !strings.Contains(text, "ok read [real]") {
		t.Fatalf("output = %q, want the real tool outcome", text)
	}
	if strings.Contains(text, "ls") || strings.Contains(text, "No tool is available") {
		t.Fatalf("output = %q, want the control refusal omitted", text)
	}
}

// The plain transcript is a publication. A terminal refusal names the withheld
// answer and the control-plane reason in one line; it never prints the refused
// prose as if it were the answer.
func TestPrintTurnMarksTerminalRejection(t *testing.T) {
	turn := agent.Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "The first, unsupported answer."}},
		},
		Answer:       agent.AnswerRejected,
		AnswerReview: "the answer asserts a claim the run did not observe",
	}
	var out bytes.Buffer
	printTurn(&out, turn)
	text := out.String()
	if strings.Contains(text, "The first, unsupported answer.") {
		t.Fatalf("the refused prose was published: %q", text)
	}
	if !strings.Contains(text, "answer withheld") ||
		!strings.Contains(text, "the answer asserts a claim the run did not observe") {
		t.Fatalf("output = %q, want the terminal refusal artifact", text)
	}
}

// A turn no publication transaction committed is not an answer. Non-empty
// assistant prose alone must not reach stdout.
func TestPrintTurnDoesNotPublishUncommittedProse(t *testing.T) {
	turn := agent.Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "a candidate that was never committed"}},
		},
		Answer: agent.AnswerUnclassified,
	}
	var out bytes.Buffer
	printTurn(&out, turn)
	if got := out.String(); got != "" {
		t.Fatalf("uncommitted prose reached the plain transcript: %q", got)
	}
}

// A superseded candidate is internal repair context. It must not become a
// visible answer or a visible withdrawal in the plain transcript.
func TestPrintTurnHidesSupersededCandidate(t *testing.T) {
	turn := agent.Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "The first, unsupported answer."}},
		},
		Answer: agent.AnswerSuperseded,
	}
	var out bytes.Buffer
	printTurn(&out, turn)
	if got := out.String(); got != "" {
		t.Fatalf("output = %q, want no superseded candidate", got)
	}
}
