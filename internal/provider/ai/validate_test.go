package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func textMessage(text string) UserMessage {
	return UserMessage{Content: []ContentBlock{TextContent{Text: text}}}
}

func assistantText(text string) AssistantMessage {
	return AssistantMessage{Content: []ContentBlock{TextContent{Text: text}}}
}

// The runtime's own message model cannot express "type=text, text=undefined":
// a text part either carries text or is a construction bug. Validation must
// refuse the empty part before any transport can omit the field on the wire.
func TestValidateMessagesRejectsEmptyTextPart(t *testing.T) {
	messages := []Message{
		textMessage("read the project"),
		AssistantMessage{Content: []ContentBlock{
			TextContent{Text: "before"},
			TextContent{Text: ""},
		}},
	}

	err := ValidateMessages(messages, []Provenance{ProvenanceHistory, ProvenanceAssistant})
	if err == nil {
		t.Fatal("an empty text part passed validation")
	}
	var validation *MessageValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T, want *MessageValidationError", err)
	}
	if len(validation.Issues) != 1 {
		t.Fatalf("issues = %+v, want one violation", validation.Issues)
	}
	issue := validation.Issues[0]
	if issue.Index != 1 || issue.Role != RoleAssistant || issue.Provenance != ProvenanceAssistant {
		t.Fatalf("issue = %+v, want message 1 attributed to the assistant stage", issue)
	}
	if issue.Kind != IssueEmptyTextPart {
		t.Fatalf("kind = %q, want %q", issue.Kind, IssueEmptyTextPart)
	}
	// The diagnostic is what a user sees instead of a provider error: it names
	// the position and the stage, and it never dumps message content.
	if !strings.Contains(err.Error(), "message 1") || !strings.Contains(err.Error(), "provenance assistant") {
		t.Fatalf("error = %q, want the index and provenance", err)
	}
}

// A turn the model closed with reasoning only has nothing a provider can send
// back. Replaying it produced the observed HTTP 400, so it is rejected at the
// canonical boundary instead of being translated into an empty text part.
func TestValidateMessagesRejectsReasoningOnlyAssistant(t *testing.T) {
	messages := []Message{
		textMessage("run this project"),
		AssistantMessage{Content: []ContentBlock{ThinkingContent{Thinking: "the server answered 200"}}},
	}
	err := ValidateMessages(messages, []Provenance{ProvenanceUser, ProvenanceHistory})
	if err == nil {
		t.Fatal("a reasoning-only assistant message passed validation")
	}
	var validation *MessageValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T, want *MessageValidationError", err)
	}
	if len(validation.Issues) != 1 || validation.Issues[0].Kind != IssueUnsendableAssistant {
		t.Fatalf("issues = %+v, want one unsendable-assistant violation", validation.Issues)
	}
	if issue := validation.Issues[0]; issue.Index != 1 || issue.Provenance != ProvenanceHistory {
		t.Fatalf("issue = %+v, want message 1 from history", issue)
	}
}

// Every form the runtime actually emits must pass unchanged: validation is a
// contract check, not a policy that rewrites the conversation.
func TestValidateMessagesAcceptsCanonicalForms(t *testing.T) {
	messages := []Message{
		UserMessage{Content: []ContentBlock{
			TextContent{Text: "inspect this"},
			ImageContent{MediaType: "image/png", Data: []byte{0x89, 0x50}},
		}},
		AssistantMessage{Content: []ContentBlock{
			ThinkingContent{Thinking: "plan", Signature: "sig"},
			ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a"}},
		}},
		ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: []ContentBlock{TextContent{Text: "package a"}}},
		// A tool with no output is empty content, not a text part without text.
		ToolResultMessage{ToolCallID: "c2", ToolName: "bash"},
		assistantText("done"),
	}
	if err := ValidateMessages(messages, nil); err != nil {
		t.Fatalf("valid canonical messages rejected: %v", err)
	}
}

// A tool result that names no call cannot be matched to its request; the
// provider rejects it long after the runtime could have named the defect.
func TestValidateMessagesRejectsToolResultWithoutCallID(t *testing.T) {
	err := ValidateMessages([]Message{
		ToolResultMessage{ToolName: "read", Content: []ContentBlock{TextContent{Text: "x"}}},
	}, nil)
	if err == nil {
		t.Fatal("a tool result without a call id passed validation")
	}
	var validation *MessageValidationError
	if !errors.As(err, &validation) || validation.Issues[0].Kind != IssueMissingToolCallID {
		t.Fatalf("error = %v, want the missing tool call id violation", err)
	}
}

// The invariant is enforced before provider-specific serialization: the same
// malformed sequence is rejected once, by the canonical check, and no adapter
// is asked to encode it.
func TestMalformedMessagesNeverReachProvider(t *testing.T) {
	r := NewRegistry()
	fake := &countingProvider{}
	r.providers["fake|"] = fake
	model := Model{ID: "m", Provider: "fake", API: APIAnthropicMessages}

	_, err := r.StreamFn()(context.Background(), Request{
		Model: model,
		Messages: []Message{
			textMessage("run this project"),
			AssistantMessage{Content: []ContentBlock{ThinkingContent{Thinking: "reasoning only"}}},
		},
	})
	if err == nil {
		t.Fatal("the malformed request was opened")
	}
	var validation *MessageValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want the canonical validation error", err)
	}
	if fake.calls != 0 {
		t.Fatalf("provider calls = %d, want 0: the transport must never see invalid state", fake.calls)
	}
}

// countingProvider is a transport that records whether it was reached.
type countingProvider struct {
	calls int
}

func (p *countingProvider) Stream(context.Context, Request) (*Stream, error) {
	p.calls++
	return NewTextStream("unexpected"), nil
}

// Every content part an adapter emits carries text. The failure this guards is
// a part with `type: "text"` and no `text` key, which two providers report with
// different wording but reject identically.
func TestAdaptersNeverEmitTextPartWithoutText(t *testing.T) {
	messages := []Message{
		textMessage("inspect"),
		AssistantMessage{Content: []ContentBlock{
			ThinkingContent{Thinking: "only reasoning survives here"},
			TextContent{Text: "answer"},
			ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "a"}},
		}},
		ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: []ContentBlock{TextContent{Text: "package a"}}},
		ToolResultMessage{ToolCallID: "c2", ToolName: "bash"},
	}
	if err := ValidateMessages(messages, nil); err != nil {
		t.Fatalf("valid messages rejected: %v", err)
	}

	openaiMessages, err := toOpenAIMessages(messages, []SystemPromptBlock{{Text: "system"}})
	if err != nil {
		t.Fatalf("openai: %v", err)
	}
	assertNoEmptyTextPart(t, "openai", openaiMessages)

	anthropicMessages, err := toAnthropicMessages(messages)
	if err != nil {
		t.Fatalf("anthropic: %v", err)
	}
	assertNoEmptyTextPart(t, "anthropic", anthropicMessages)

	if _, err := toOllamaMessages(messages, nil); err != nil {
		t.Fatalf("ollama: %v", err)
	}
}

// assertNoEmptyTextPart marshals a provider body and fails on any text part
// that would arrive without a usable string.
func assertNoEmptyTextPart(t *testing.T, provider string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("%s: marshal: %v", provider, err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s: decode: %v", provider, err)
	}
	var walk func(value any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if kind, _ := v["type"].(string); kind == "text" {
				text, ok := v["text"].(string)
				if !ok || text == "" {
					t.Fatalf("%s: text part without text: %s", provider, raw)
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(decoded)
}

// A tool with no output is truthful, not malformed: it is an empty string
// content for OpenAI and an omitted content for Anthropic, never an empty text
// part.
func TestEmptyToolResultSerializesAsEmptyString(t *testing.T) {
	messages := []Message{
		AssistantMessage{Content: []ContentBlock{ToolCall{ID: "c1", Name: "bash"}}},
		ToolResultMessage{ToolCallID: "c1", ToolName: "bash"},
	}
	converted, err := toOpenAIMessages(messages, nil)
	if err != nil {
		t.Fatalf("toOpenAIMessages: %v", err)
	}
	if content, ok := converted[1].Content.(string); !ok || content != "" {
		t.Fatalf("tool content = %#v, want the empty string", converted[1].Content)
	}
	raw, err := json.Marshal(converted[1])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"content":""`) {
		t.Fatalf("tool message = %s, want an explicit empty string content", raw)
	}
	if strings.Contains(string(raw), `"type":"text"`) {
		t.Fatalf("tool message = %s, must not carry a text part", raw)
	}
}

// An assistant turn that only carried reasoning must not gain a fabricated
// empty text part on the way to the wire.
func TestAssistantWithToolCallsOmitsEmptyContent(t *testing.T) {
	converted, err := toOpenAIMessages([]Message{
		AssistantMessage{Content: []ContentBlock{ToolCall{ID: "c1", Name: "read"}}},
	}, nil)
	if err != nil {
		t.Fatalf("toOpenAIMessages: %v", err)
	}
	if converted[0].Content != nil {
		t.Fatalf("assistant content = %#v, want no content for a call-only turn", converted[0].Content)
	}
	if len(converted[0].ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want one", converted[0].ToolCalls)
	}
}

// An assembled stream never exposes a zero-length part: an empty text block is
// not content, and replaying it is the provider failure this boundary prevents.
func TestCompletedStreamDropsEmptyParts(t *testing.T) {
	stream := NewCompletedStream(AssistantMessage{Content: []ContentBlock{
		TextContent{Text: ""},
		ThinkingContent{Thinking: "kept", Signature: "sig"},
		TextContent{Text: "answer"},
	}})
	message, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(message.Content) != 2 {
		t.Fatalf("content = %#v, want the reasoning and the answer", message.Content)
	}
	if text, ok := message.Content[1].(TextContent); !ok || text.Text != "answer" {
		t.Fatalf("last block = %#v, want the answer text", message.Content[1])
	}
}
