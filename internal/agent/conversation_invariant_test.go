package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// conversationModel is the minimal model a loop needs for a scripted stream.
func conversationModel() ai.Model {
	return ai.Model{ID: "test-model", Provider: "test", ContextWindow: 128000}
}

func conversationLoop(t *testing.T, stream ai.StreamFn, tools ...Tool) *Loop {
	t.Helper()
	loop, err := New(Config{
		Model:    conversationModel(),
		Stream:   stream,
		Tools:    tools,
		MaxTurns: 8,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

// A turn that produced only reasoning is a run observation, never conversation
// content. The observed failure was exactly this turn being replayed on the
// next user request, where a provider rejected its empty content.
func TestReasoningOnlyTurnIsNotReplayed(t *testing.T) {
	tool := newScriptedTool("read", "package main")
	first, firstRequests := scriptedStream(
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ThinkingContent{Thinking: "the project is a static site and it answered 200"}},
			StopReason: ai.StopEndTurn,
		},
	)
	loop := conversationLoop(t, first, tool)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "run this project help me"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want the action turn and the reasoning-only observation", len(turns))
	}
	if turns[1].Conversational() {
		t.Fatalf("reasoning-only turn reported as conversation content: %#v", turns[1].Assistant.Content)
	}
	if got := len(firstRequests()); got != 2 {
		t.Fatalf("model requests = %d, want 2", got)
	}

	// Rebuild the next request the way every host does: the previous request
	// plus the completed turns, then the new user message.
	history := []Message{UserMessage{Text: "run this project help me"}}
	history = append(history, ConversationMessages(turns)...)
	history = append(history, UserMessage{Text: "How to convert this project to Next.js?"})

	second, secondRequests := scriptedStream(ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "Convert the static pages into the Next.js app router."}},
	})
	next := conversationLoop(t, second, tool)
	if _, err := next.Run(context.Background(), history); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	requests := secondRequests()
	if len(requests) != 1 {
		t.Fatalf("second run requests = %d, want 1", len(requests))
	}
	messages := requests[0].Messages
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want user, action, tool result, user: %#v", len(messages), messages)
	}
	if err := ai.ValidateMessages(messages, nil); err != nil {
		t.Fatalf("second request is not canonical: %v", err)
	}
	for index, message := range messages {
		if assistant, ok := message.(ai.AssistantMessage); ok && !ai.HasSendableContent(assistant) {
			t.Fatalf("message %d = %#v, want only sendable assistant turns", index, assistant)
		}
	}
}

// Malformed history is an internal construction failure. It must be refused
// before the stream is opened, and the diagnostic must name the message.
func TestMalformedHistoryIsRejectedBeforeProvider(t *testing.T) {
	stream, requests := scriptedStream(ai.AssistantMessage{
		Content: []ai.ContentBlock{ai.TextContent{Text: "this response must never be requested"}},
	})
	loop := conversationLoop(t, stream, newScriptedTool("read", "package main"))
	_, err := loop.Run(context.Background(), []Message{
		UserMessage{Text: "run this project help me"},
		AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ThinkingContent{Thinking: "the server answered 200"}},
		}},
		UserMessage{Text: "How to convert this project to Next.js?"},
	})
	if err == nil {
		t.Fatal("malformed history produced a provider request")
	}
	if !errors.Is(err, ErrInvalidProviderContext) {
		t.Fatalf("error = %v, want ErrInvalidProviderContext", err)
	}
	if got := len(requests()); got != 0 {
		t.Fatalf("provider requests = %d, want 0", got)
	}
	for _, want := range []string{"message 1", "assistant", "provenance history", "unsendable_assistant"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %q", err, want)
		}
	}
}

// A tool with no output is truthful. It must travel as an empty tool result,
// never as a text part whose text does not exist.
func TestEmptyToolResultDoesNotBecomeEmptyTextPart(t *testing.T) {
	empty := newScriptedTool("bash", "")
	stream, requests := scriptedStream(
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "bash", Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "The command printed nothing."}}},
	)
	loop := conversationLoop(t, stream, empty)
	if _, err := loop.Run(context.Background(), []Message{UserMessage{Text: "run the command"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requestsRecorded := requests()
	if len(requestsRecorded) != 2 {
		t.Fatalf("requests = %d, want 2", len(requestsRecorded))
	}
	for index, request := range requestsRecorded {
		if err := ai.ValidateMessages(request.Messages, nil); err != nil {
			t.Fatalf("request %d is not canonical: %v", index, err)
		}
	}
	var result ai.ToolResultMessage
	for _, message := range requestsRecorded[1].Messages {
		if candidate, ok := message.(ai.ToolResultMessage); ok && candidate.ToolCallID == "c1" {
			result = candidate
		}
	}
	if result.ToolCallID == "" {
		t.Fatal("the empty tool result is missing from the second request")
	}
	if len(result.Content) != 0 {
		t.Fatalf("empty tool result content = %#v, want no parts", result.Content)
	}
}

// State accumulates across a session. Every request after a reasoning-only turn
// must still be canonical, and the conversation must keep working when the same
// shape repeats.
func TestConversationStaysValidAcrossRepeatedTurns(t *testing.T) {
	tool := newScriptedTool("read", "package main")
	history := []Message{UserMessage{Text: "run this project help me"}}
	requestsSeen := 0
	for cycle := range 4 {
		stream, requests := scriptedStream(
			ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.ToolCall{ID: "c" + string(rune('a'+cycle)), Name: "read", Arguments: map[string]any{"value": "x"}}},
				StopReason: ai.StopToolUse,
			},
			// The answer phase closes with reasoning only, the shape that
			// originally poisoned the context.
			ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.ThinkingContent{Thinking: "everything needed is known"}},
				StopReason: ai.StopEndTurn,
			},
		)
		loop := conversationLoop(t, stream, tool)
		turns, err := loop.Run(context.Background(), history)
		if err != nil {
			t.Fatalf("cycle %d: Run: %v", cycle, err)
		}
		for index, request := range requests() {
			requestsSeen++
			if err := ai.ValidateMessages(request.Messages, nil); err != nil {
				t.Fatalf("cycle %d request %d is not canonical: %v", cycle, index, err)
			}
			for messageIndex, message := range request.Messages {
				if assistant, ok := message.(ai.AssistantMessage); ok && !ai.HasSendableContent(assistant) {
					t.Fatalf("cycle %d request %d message %d = %#v, want sendable content",
						cycle, index, messageIndex, assistant)
				}
			}
		}
		history = append(history, ConversationMessages(turns)...)
		history = append(history, UserMessage{Text: "next request " + string(rune('a'+cycle))})
	}
	if requestsSeen != 8 {
		t.Fatalf("provider requests = %d, want two per cycle", requestsSeen)
	}
}

// The transcript still carries the reasoning-only turn for display; only the
// provider conversation excludes it. Dropping it from turns would hide the
// empty answer from completion evaluation.
func TestConversationProjectionKeepsRunObservationsOutOfContextOnly(t *testing.T) {
	tool := newScriptedTool("read", "package main")
	stream, _ := scriptedStream(
		ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"value": "x"}}},
			StopReason: ai.StopToolUse,
		},
		ai.AssistantMessage{Content: []ai.ContentBlock{ai.ThinkingContent{Thinking: "no visible answer"}}},
	)
	loop := conversationLoop(t, stream, tool)
	turns, err := loop.Run(context.Background(), []Message{UserMessage{Text: "inspect"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(turns) != 2 || turns[1].Assistant.ThinkingText() == "" {
		t.Fatalf("turns = %#v, want the reasoning observation preserved", turns)
	}
	projected := ConversationMessages(turns)
	if len(projected) != 2 {
		t.Fatalf("projected = %d messages, want the action turn only: %#v", len(projected), projected)
	}
}
