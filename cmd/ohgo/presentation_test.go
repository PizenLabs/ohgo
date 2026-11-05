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
