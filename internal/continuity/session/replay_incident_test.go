package session

import (
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// incidentStored reproduces the transcript of the session that reported the
// provider rejection: a completed BUILD turn, then a retried empty answer turn
// (reasoning only) persisted immediately before the real answer, then the next
// user request. The provider counted its system message as index 0, so this
// reasoning-only row was messages[13] on the wire.
func incidentStored() []StoredMessage {
	calls := func(id, tool string) []StoredBlock {
		return []StoredBlock{{Kind: StoredBlockToolCall, ID: id, Name: tool}}
	}
	result := func(id, tool, text string) StoredMessage {
		return StoredMessage{Role: "toolResult", ToolCallID: id, ToolName: tool, Text: text}
	}
	return []StoredMessage{
		{Role: "user", Text: "run this project help me"},
		{Role: "assistant", Model: "m", StopReason: "tool_use", Blocks: calls("c1", "structure")},
		result("c1", "structure", "status: ok\nview: overview\nfiles: 5"),
		{Role: "assistant", Model: "m", StopReason: "tool_use", Blocks: calls("c2", "ls")},
		result("c2", "ls", "README.md\nindex.html\nscript.js\nstyles.css"),
		{Role: "assistant", Model: "m", StopReason: "tool_use", Blocks: calls("c3", "verify")},
		result("c3", "verify", "verification: PASS — http://127.0.0.1:8000/ status 200"),
		// The retried empty turn: reasoning arrived, nothing was answered.
		{
			Role: "assistant", Model: "m", StopReason: "end_turn",
			Blocks: []StoredBlock{{Kind: StoredBlockThinking, Text: "the server answered 200"}},
		},
		{
			Role: "assistant", Model: "m", StopReason: "end_turn", Text: "Done — the site serves at :8000.",
			Blocks: []StoredBlock{
				{Kind: StoredBlockThinking, Text: "report the result concisely"},
				{Kind: StoredBlockText, Text: "Done — the site serves at :8000."},
			},
		},
		{Role: "user", Text: "How to convert this project to Next.js?"},
	}
}

// The row that produced the rejection is a display record, not conversation.
// It is skipped, its answer survives, and the replayed conversation is valid.
func TestIncidentReplayProducesACanonicalConversation(t *testing.T) {
	stored := incidentStored()
	displayOnly := stored[7]
	if _, err := ToAgentMessage(displayOnly); !errors.Is(err, ErrDisplayOnlyMessage) {
		t.Fatalf("row %d = %v, want ErrDisplayOnlyMessage", 7, err)
	}

	replayed, err := Replay(stored)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) != len(stored)-1 {
		t.Fatalf("replayed = %d messages, want one display-only row skipped", len(replayed))
	}
	// Every replayed turn must be representable on the wire: an assistant
	// turn with sendable content, and text parts that carry text.
	for index, message := range replayed {
		switch m := message.(type) {
		case agent.AssistantMessage:
			if !ai.HasSendableContent(m.Message) {
				t.Fatalf("message %d = %#v, want a sendable assistant turn", index, m.Message)
			}
		case agent.ToolResultMessage:
			for _, block := range m.Content {
				if text, ok := block.(ai.TextContent); ok && text.Text == "" {
					t.Fatalf("message %d has an empty text part", index)
				}
			}
		}
	}

	// The assistant turn the user actually saw is still last before the new
	// request, and it carries the answer.
	last := replayed[len(replayed)-2]
	assistant, ok := last.(agent.AssistantMessage)
	if !ok {
		t.Fatalf("message before the new request = %T, want the assistant answer", last)
	}
	if assistant.Message.Text() != "Done — the site serves at :8000." {
		t.Fatalf("answer = %q", assistant.Message.Text())
	}
}

// The pre-fix conversation is exactly what the validator now refuses. This
// pins the classification without depending on a provider: a reasoning-only
// assistant message can never be a valid outbound turn.
func TestIncidentRowWouldViolateTheCanonicalContract(t *testing.T) {
	reasoningOnly := agent.AssistantMessage{Message: ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.ThinkingContent{Thinking: "the server answered 200"}},
	}}
	if ai.HasSendableContent(reasoningOnly.Message) {
		t.Fatal("a reasoning-only assistant message was treated as sendable")
	}
	err := ai.ValidateMessages([]ai.Message{
		ai.UserMessage{Content: ai.TextBlocks("run this project help me")},
		reasoningOnly.Message,
	}, nil)
	if err == nil {
		t.Fatal("the replayed reasoning-only turn passed the canonical contract")
	}
	var validation *ai.MessageValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T, want *ai.MessageValidationError", err)
	}
	if issue := validation.Issues[0]; issue.Index != 1 || issue.Kind != ai.IssueUnsendableAssistant {
		t.Fatalf("issue = %+v, want message 1 flagged as unsendable", issue)
	}
}
