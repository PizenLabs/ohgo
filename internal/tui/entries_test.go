package tui

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// EntriesFromMessages rebuilds the transcript view for session resume.

func assistantWithCall(id, name string, args map[string]any) agent.AssistantMessage {
	return agent.AssistantMessage{Message: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ToolCall{ID: id, Name: name, Arguments: args}},
	}}
}

func TestReplayPairsCallsWithResults(t *testing.T) {
	history := []agent.Message{
		agent.UserMessage{Text: "list files"},
		assistantWithCall("c1", "glob", map[string]any{"pattern": "*.go"}),
		agent.ToolResultMessage{ToolCallID: "c1", ToolName: "glob", Content: []ai.ContentBlock{ai.TextContent{Text: "a.go"}}},
	}
	entries := EntriesFromMessages(history)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want user + tool card", len(entries))
	}
	tool := entries[1].Tool
	if tool == nil || tool.Name != "glob" || tool.Status != ToolDone {
		t.Errorf("tool = %+v, want the done glob card", tool)
	}
	if tool.Text != "a.go" {
		t.Errorf("tool text = %q, want the paired result", tool.Text)
	}
}

func TestReplayMarksErrorResults(t *testing.T) {
	history := []agent.Message{
		assistantWithCall("c9", "bash", map[string]any{"command": "false"}),
		agent.ToolResultMessage{ToolCallID: "c9", ToolName: "bash", Content: []ai.ContentBlock{ai.TextContent{Text: "boom"}}, IsError: true},
	}
	entries := EntriesFromMessages(history)
	if len(entries) != 1 || entries[0].Tool.Status != ToolError {
		t.Errorf("entries = %+v, want one error card", entries)
	}
}

func TestReplayKeepsUnpairedCallsVisible(t *testing.T) {
	// A call whose result never persisted keeps its card with a note: a
	// transcript that drops work it did lies about what happened.
	history := []agent.Message{assistantWithCall("c2", "read", map[string]any{"path": "a.go"})}
	entries := EntriesFromMessages(history)
	if len(entries) != 1 || entries[0].Tool == nil {
		t.Fatalf("entries = %+v, want the unpaired card kept", entries)
	}
	if entries[0].Tool.Text == "" {
		t.Error("the unpaired card needs its explanatory note")
	}
}

func TestReplayKeepsOrphanResultsVisible(t *testing.T) {
	history := []agent.Message{
		agent.ToolResultMessage{ToolCallID: "ghost", ToolName: "grep", Content: []ai.ContentBlock{ai.TextContent{Text: "hit"}}},
	}
	entries := EntriesFromMessages(history)
	if len(entries) != 1 {
		t.Errorf("entries = %d, want the orphan result kept rather than silenced", len(entries))
	}
}

func TestReplaySkipsUIOnlyAndEmpty(t *testing.T) {
	history := []agent.Message{
		agent.UserMessage{Text: "  "},
		agent.Notification{Text: "ephemeral"},
		agent.UserMessage{Text: "real"},
	}
	entries := EntriesFromMessages(history)
	if len(entries) != 1 || entries[0].UserText != "real" {
		t.Errorf("entries = %+v, want only the real prompt", entries)
	}
}
