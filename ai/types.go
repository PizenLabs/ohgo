// Package ai defines the provider-agnostic message model and the streaming
// contract every LLM transport implements.
//
// The types here deliberately mirror the wire-neutral parts of the original
// TypeScript surface: a message is one of user / assistant / toolResult, and
// content is a discriminated union of text, image, thinking, and tool-call
// blocks. Providers translate between this model and their native API shape.
package ai

import (
	"encoding/base64"
)

// Role identifies the author of a message.
type Role string

// The three roles that appear on the wire.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "toolResult"
)

// StopReason records why an assistant turn ended.
type StopReason string

// Stop reasons shared across providers.
const (
	// StopEndTurn: the model finished its turn naturally.
	StopEndTurn StopReason = "end_turn"
	// StopToolUse: the model emitted at least one tool call.
	StopToolUse StopReason = "tool_use"
	// StopMaxTokens: the output budget was exhausted.
	StopMaxTokens StopReason = "max_tokens"
	// StopAborted: the caller cancelled the stream.
	StopAborted StopReason = "aborted"
	// StopError: the stream failed.
	StopError StopReason = "error"
	// StopRefusal: the provider refused the request on policy grounds.
	StopRefusal StopReason = "refusal"
)

// ContentBlock is one block inside a message's content array.
//
// Implementations are immutable value types; a provider builds them while
// decoding a stream and the agent loop reads them without mutation.
type ContentBlock interface {
	// BlockKind returns the discriminant used to recover the concrete type.
	BlockKind() string
}

// TextContent is plain model- or user-authored text.
type TextContent struct {
	Text string
}

// BlockKind implements ContentBlock.
func (TextContent) BlockKind() string { return "text" }

// ImageContent carries raw image bytes plus the media type needed to send them.
type ImageContent struct {
	MediaType string
	Data      []byte
}

// BlockKind implements ContentBlock.
func (ImageContent) BlockKind() string { return "image" }

// base64 encodes the payload the way every supported provider expects.
func (c ImageContent) base64() string { return base64.StdEncoding.EncodeToString(c.Data) }

// ThinkingContent holds a reasoning trace plus the provider signature that
// proves it was produced server-side.
type ThinkingContent struct {
	Thinking  string
	Signature string
}

// BlockKind implements ContentBlock.
func (ThinkingContent) BlockKind() string { return "thinking" }

// ToolCall is a model-requested invocation.
//
// Arguments holds the decoded parameters once the call is complete. While a
// call is still streaming, providers emit the raw JSON through
// EventToolCallDelta and Arguments stays nil.
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any

	// argumentsJSON accumulates the streamed argument fragments. It is cleared
	// when the call closes and Arguments is populated.
	argumentsJSON string
	// pendingInput marks a call whose input block arrived empty and is
	// therefore expected to be filled in by input_json_delta events.
	pendingInput bool
}

// RawArguments returns the accumulated, not-yet-decoded argument fragments.
//
// It is only meaningful while a call is streaming, and exists so a provider
// accumulator can detect a call that never closed.
func (c ToolCall) RawArguments() string { return c.argumentsJSON }

// appendArgs accumulates one streamed argument fragment.
func (c *ToolCall) appendArgs(fragment string) { c.argumentsJSON += fragment }

// BlockKind implements ContentBlock.
func (ToolCall) BlockKind() string { return "toolCall" }

// ToolResultContent is the payload returned to the model for one tool call.
type ToolResultContent struct {
	ToolCallID string
	ToolName   string
	Text       string
	IsError    bool
}

// BlockKind implements ContentBlock.
func (ToolResultContent) BlockKind() string { return "toolResult" }

// Message is one turn in the conversation.
type Message interface {
	// Role returns the author role.
	Role() Role
}

// UserMessage is a turn authored by the user or by a tool on the user's behalf.
type UserMessage struct {
	// Content holds text and image blocks in stream order.
	Content []ContentBlock
}

// Role implements Message.
func (UserMessage) Role() Role { return RoleUser }

// Text joins every text block in the message with newlines.
func (m UserMessage) Text() string { return joinText(m.Content) }

// AssistantMessage is one completed model turn.
type AssistantMessage struct {
	Content    []ContentBlock
	Model      Model
	Usage      Usage
	StopReason StopReason
	// ErrorMessage is set when StopReason is StopError.
	ErrorMessage string
}

// Role implements Message.
func (AssistantMessage) Role() Role { return RoleAssistant }

// Text joins every text block in the message with newlines.
func (m AssistantMessage) Text() string { return joinText(m.Content) }

// ToolCalls returns the tool-call blocks in stream order.
func (m AssistantMessage) ToolCalls() []ToolCall {
	out := make([]ToolCall, 0, len(m.Content))
	for _, block := range m.Content {
		if call, ok := block.(ToolCall); ok {
			out = append(out, call)
		}
	}
	return out
}

// ToolResultMessage is the answer to one tool call.
type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []ContentBlock
	// IsError marks a non-throwing failure the model should see as a failure.
	IsError bool
}

// Role implements Message.
func (ToolResultMessage) Role() Role { return RoleTool }

// Text joins every text block in the message with newlines.
func (m ToolResultMessage) Text() string { return joinText(m.Content) }

func joinText(blocks []ContentBlock) string {
	var sb []byte
	for _, block := range blocks {
		switch b := block.(type) {
		case TextContent:
			if b.Text == "" {
				continue
			}
			if len(sb) > 0 {
				sb = append(sb, '\n')
			}
			sb = append(sb, b.Text...)
		case ToolResultContent:
			if b.Text == "" {
				continue
			}
			if len(sb) > 0 {
				sb = append(sb, '\n')
			}
			sb = append(sb, b.Text...)
		}
	}
	return string(sb)
}
