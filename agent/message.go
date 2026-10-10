package agent

import (
	"strings"

	"github.com/PizenLabs/ohgo/ai"
)

// Message is one agent-level turn.
//
// It is an interface so hosts can add their own message kinds (UI-only
// notifications, status banners) without the loop needing to know about them.
// Every message either converts to a provider message or is dropped.
type Message interface {
	// toLLM converts the message, reporting false when it is UI-only and
	// must not reach the model.
	toLLM() (ai.Message, bool)
}

// UserMessage is a user-authored turn.
type UserMessage struct {
	// Text is the user's input.
	Text string
	// Images are attachments, in order.
	Images []ai.ImageContent
}

// toLLM implements Message.
func (m UserMessage) toLLM() (ai.Message, bool) {
	content := make([]ai.ContentBlock, 0, 1+len(m.Images))
	if m.Text != "" {
		content = append(content, ai.TextContent{Text: m.Text})
	}
	for _, image := range m.Images {
		content = append(content, image)
	}
	if len(content) == 0 {
		// An empty user turn is dropped: providers reject it.
		return nil, false
	}
	return ai.UserMessage{Content: content}, true
}

// AssistantMessage is a completed model turn.
type AssistantMessage struct {
	// Message is the provider message.
	Message ai.AssistantMessage
}

// toLLM implements Message.
func (m AssistantMessage) toLLM() (ai.Message, bool) {
	return m.Message, true
}

// ToolResultMessage answers one tool call.
type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []ai.ContentBlock
	// IsError marks a non-throwing failure the model should see as a failure.
	IsError bool
}

// Text joins every text block in the result with newlines.
func (m ToolResultMessage) Text() string {
	var out []string
	for _, block := range m.Content {
		switch b := block.(type) {
		case ai.TextContent:
			if b.Text != "" {
				out = append(out, b.Text)
			}
		case ai.ToolResultContent:
			if b.Text != "" {
				out = append(out, b.Text)
			}
		}
	}
	return strings.Join(out, "\n")
}

// toLLM implements Message.
func (m ToolResultMessage) toLLM() (ai.Message, bool) {
	return ai.ToolResultMessage{
		ToolCallID: m.ToolCallID,
		ToolName:   m.ToolName,
		Content:    m.Content,
		IsError:    m.IsError,
	}, true
}

// Notification is a host-only message that never reaches the model.
type Notification struct {
	// Text is the banner content.
	Text string
}

// toLLM implements Message by reporting that this message is UI-only.
func (Notification) toLLM() (ai.Message, bool) { return nil, false }
