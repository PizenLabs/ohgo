package session

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Ordered blocks must survive storage: a resumed transcript that loses
// reasoning or reorders text after a tool call shows a conversation the model
// never had.
func TestReplayPreservesOrderedTextThinkingAndCalls(t *testing.T) {
	original := agent.AssistantMessage{Message: ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ThinkingContent{Thinking: "inspect first", Signature: "sig-1"},
			ai.TextContent{Text: "before"},
			ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a.go"}},
			ai.TextContent{Text: "after"},
		},
		StopReason: ai.StopToolUse,
	}}
	stored, err := FromAgentMessage(original)
	if err != nil {
		t.Fatalf("FromAgentMessage: %v", err)
	}
	replayed, err := ToAgentMessage(stored)
	if err != nil {
		t.Fatalf("ToAgentMessage: %v", err)
	}
	assistant, ok := replayed.(agent.AssistantMessage)
	if !ok {
		t.Fatalf("replayed %T, want AssistantMessage", replayed)
	}
	content := assistant.Message.Content
	if len(content) != 4 {
		t.Fatalf("blocks = %d, want 4 in original order: %#v", len(content), content)
	}
	if thinking, ok := content[0].(ai.ThinkingContent); !ok || thinking.Thinking != "inspect first" || thinking.Signature != "sig-1" {
		t.Fatalf("block 0 = %#v, want the signed reasoning block", content[0])
	}
	if text, ok := content[1].(ai.TextContent); !ok || text.Text != "before" {
		t.Fatalf("block 1 = %#v, want text before the call", content[1])
	}
	if call, ok := content[2].(ai.ToolCall); !ok || call.ID != "c1" {
		t.Fatalf("block 2 = %#v, want the tool call", content[2])
	}
	if text, ok := content[3].(ai.TextContent); !ok || text.Text != "after" {
		t.Fatalf("block 3 = %#v, want text after the call", content[3])
	}
}

// Legacy rows have no Blocks and must keep replaying through Text plus Calls.
func TestReplayFallsBackToLegacyTextAndCalls(t *testing.T) {
	replayed, err := ToAgentMessage(StoredMessage{
		Role:  "assistant",
		Text:  "legacy text",
		Calls: []StoredToolCall{{ID: "c9", Name: "ls", Arguments: map[string]any{"path": "x"}}},
	})
	if err != nil {
		t.Fatalf("ToAgentMessage: %v", err)
	}
	assistant := replayed.(agent.AssistantMessage)
	if assistant.Message.Text() != "legacy text" {
		t.Errorf("text = %q", assistant.Message.Text())
	}
	calls := assistant.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "c9" {
		t.Errorf("calls = %+v, want the legacy call", calls)
	}
}

// The observed failure left a reasoning-only assistant row in the transcript:
// a retried empty turn persisted beside the real answer. Replay must treat it
// as display history, not conversation content, or the next request carries an
// assistant message with nothing a provider can send.
func TestReplaySkipsReasoningOnlyAssistant(t *testing.T) {
	stored := []StoredMessage{
		{Role: "user", Text: "run this project help me"},
		{
			Role: "assistant", Model: "beam-free", StopReason: "end_turn",
			Blocks: []StoredBlock{{Kind: StoredBlockThinking, Text: "the server answered 200"}},
		},
		{
			Role: "assistant", Text: "Done.", Model: "beam-free", StopReason: "end_turn",
			Blocks: []StoredBlock{
				{Kind: StoredBlockThinking, Text: "report the result"},
				{Kind: StoredBlockText, Text: "Done."},
			},
		},
		{Role: "user", Text: "How to convert this project to Next.js?"},
	}
	replayed, err := Replay(stored)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != 3 {
		t.Fatalf("replayed = %d messages, want the user, the answer, and the next request: %#v", len(replayed), replayed)
	}
	for index, message := range replayed {
		if assistant, ok := message.(agent.AssistantMessage); ok {
			if !ai.HasSendableContent(assistant.Message) {
				t.Fatalf("message %d = %#v, want only sendable assistant turns", index, assistant)
			}
			if assistant.Message.Text() != "Done." {
				t.Fatalf("message %d text = %q, want the real answer", index, assistant.Message.Text())
			}
		}
	}
}

// A legacy row with no text, no calls, and no blocks has no replayable form
// either. It is skipped rather than failing the whole transcript, because a
// session must remain resumable.
func TestReplaySkipsEmptyAssistantRow(t *testing.T) {
	replayed, err := Replay([]StoredMessage{
		{Role: "user", Text: "run this project"},
		{Role: "assistant", Model: "m", StopReason: "end_turn"},
		{Role: "user", Text: "and then?"},
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != 2 {
		t.Fatalf("replayed = %d messages, want both user turns and no empty assistant", len(replayed))
	}
}
