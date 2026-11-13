package ai

import (
	"fmt"
	"strings"
)

// Provenance names the subsystem that constructed one outbound message.
//
// It is diagnostic metadata, never authority and never content: a validation
// failure names the stage that produced the offending message so the defect is
// located without guessing at provider behaviour. Every production request
// reaches the provider through the same validator, so the same malformed state
// is rejected once, whatever transport would have serialized it.
type Provenance string

// Provenance categories for messages the runtime can construct.
const (
	// ProvenanceUnknown means the assembler could not attribute the message.
	ProvenanceUnknown Provenance = "unknown"
	// ProvenanceUser is the user's current request.
	ProvenanceUser Provenance = "user"
	// ProvenanceAssistant is a model turn this run produced.
	ProvenanceAssistant Provenance = "assistant"
	// ProvenanceToolResult is a tool outcome this run produced.
	ProvenanceToolResult Provenance = "tool_result"
	// ProvenanceHistory is a turn restored from the session transcript.
	ProvenanceHistory Provenance = "history"
	// ProvenanceSynthetic is a message the runtime injected (steering).
	ProvenanceSynthetic Provenance = "synthetic"
	// ProvenanceRecovery is a bounded protocol/answer recovery request.
	ProvenanceRecovery Provenance = "recovery"
)

// Issue kinds reported by the canonical message validator.
const (
	IssueNilMessage          = "nil_message"
	IssueUnsupportedMessage  = "unsupported_message"
	IssueEmptyTextPart       = "empty_text_part"
	IssueEmptyImagePart      = "empty_image_part"
	IssueUnsupportedBlock    = "unsupported_block"
	IssueMissingToolCall     = "missing_tool_call"
	IssueMissingToolCallID   = "missing_tool_call_id"
	IssueUnsendableUser      = "unsendable_user"
	IssueUnsendableAssistant = "unsendable_assistant"
)

// MessageIssue is one canonical-contract violation for one outbound message.
//
// Detail is a bounded description of the shape, never message content: the
// diagnostic is safe to log and to show as an internal error.
type MessageIssue struct {
	// Index is the zero-based position in the outbound message list.
	Index int
	// Role is the offending message's canonical role, empty when the value is
	// not a known message type.
	Role Role
	// Provenance is the constructing subsystem reported by the assembler.
	Provenance Provenance
	// Kind is one of the Issue* constants.
	Kind string
	// Detail explains the specific violation.
	Detail string
}

// MessageValidationError reports every canonical-contract violation in one
// outbound request.
//
// It is an internal construction failure, not a provider failure: by the time
// a transport could reject it, the runtime has already lost the ability to say
// which subsystem produced the invalid state.
type MessageValidationError struct {
	Issues []MessageIssue
}

// Error implements error.
func (e *MessageValidationError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "ai: invalid outbound messages"
	}
	const maxReported = 4
	parts := make([]string, 0, maxReported+1)
	for i, issue := range e.Issues {
		if i == maxReported {
			parts = append(parts, fmt.Sprintf("and %d more violation(s)", len(e.Issues)-maxReported))
			break
		}
		role := string(issue.Role)
		if role == "" {
			role = "unknown"
		}
		provenance := issue.Provenance
		if provenance == "" {
			provenance = ProvenanceUnknown
		}
		parts = append(parts, fmt.Sprintf("message %d (role %s, provenance %s): %s: %s",
			issue.Index, role, provenance, issue.Kind, issue.Detail))
	}
	return "ai: invalid outbound messages: " + strings.Join(parts, "; ")
}

// HasSendableContent reports whether an assistant message carries content a
// provider can transmit: non-empty text or at least one complete tool call.
//
// Reasoning is not conversation content. A turn that produced only a thinking
// block has nothing to send back, and replaying it is what put a text part
// without text on the wire.
func HasSendableContent(msg AssistantMessage) bool {
	for _, block := range msg.Content {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				return true
			}
		case ToolCall:
			if b.Name != "" {
				return true
			}
		}
	}
	return false
}

// TextBlocks returns content blocks carrying text, or nil when text is empty.
//
// A text part must carry a non-empty string: every supported transport omits
// an empty text field, so an empty part becomes a part with no text at all.
// An empty tool output is therefore represented by no blocks and encoded as an
// empty string content, never as a text part without text.
func TextBlocks(text string) []ContentBlock {
	if text == "" {
		return nil
	}
	return []ContentBlock{TextContent{Text: text}}
}

// stripEmptyBlocks removes content parts that carry nothing.
//
// Some transports open a block before any delta arrives, and an opaque block
// (Anthropic's redacted_thinking) is decoded as an empty part so later block
// indices keep their position. Such a part is not conversation content: left
// in an assembled message it would be replayed as a text part without text.
// Only empty parts are removed; order and every real block stay.
func stripEmptyBlocks(content []ContentBlock) []ContentBlock {
	out := make([]ContentBlock, 0, len(content))
	changed := false
	for _, block := range content {
		switch b := block.(type) {
		case TextContent:
			if b.Text == "" {
				changed = true
				continue
			}
		case ThinkingContent:
			if b.Thinking == "" && b.Signature == "" {
				changed = true
				continue
			}
		}
		out = append(out, block)
	}
	if !changed {
		return content
	}
	return out
}

// ValidateMessages checks the provider-independent canonical contract.
//
// origins, when non-nil, is aligned with messages and labels the subsystem
// that constructed each entry; a short or empty origins slice reports
// ProvenanceUnknown rather than failing.
//
// The contract is the same for every transport:
//
//   - a message is a known user, assistant, or tool-result turn;
//   - a text part always carries a non-empty string;
//   - a user turn has at least one sendable block;
//   - an assistant turn has non-empty text or a complete tool call, because
//     reasoning alone cannot be replayed;
//   - a tool result names the call it answers; its content may be empty,
//     which encodes as an empty string, never as an empty text part.
func ValidateMessages(messages []Message, origins []Provenance) error {
	var issues []MessageIssue
	for index, msg := range messages {
		provenance := ProvenanceUnknown
		if index < len(origins) && origins[index] != "" {
			provenance = origins[index]
		}
		issues = append(issues, validateMessage(index, msg, provenance)...)
	}
	if len(issues) == 0 {
		return nil
	}
	return &MessageValidationError{Issues: issues}
}

func validateMessage(index int, msg Message, provenance Provenance) []MessageIssue {
	issue := func(role Role, kind, detail string) MessageIssue {
		return MessageIssue{Index: index, Role: role, Provenance: provenance, Kind: kind, Detail: detail}
	}
	if msg == nil {
		return []MessageIssue{issue("", IssueNilMessage, "the message is nil")}
	}
	switch m := msg.(type) {
	case UserMessage:
		var issues []MessageIssue
		sendable := false
		for _, block := range m.Content {
			switch b := block.(type) {
			case TextContent:
				if b.Text == "" {
					issues = append(issues, issue(RoleUser, IssueEmptyTextPart, "text content part has no text"))
					continue
				}
				sendable = true
			case ImageContent:
				if b.MediaType == "" || len(b.Data) == 0 {
					issues = append(issues, issue(RoleUser, IssueEmptyImagePart, "image content part has no media type or data"))
					continue
				}
				sendable = true
			default:
				issues = append(issues, issue(RoleUser, IssueUnsupportedBlock,
					fmt.Sprintf("unsupported user content block %T", block)))
			}
		}
		if !sendable {
			issues = append(issues, issue(RoleUser, IssueUnsendableUser, "user message has no sendable content"))
		}
		return issues
	case AssistantMessage:
		var issues []MessageIssue
		for _, block := range m.Content {
			switch b := block.(type) {
			case TextContent:
				if b.Text == "" {
					issues = append(issues, issue(RoleAssistant, IssueEmptyTextPart, "text content part has no text"))
				}
			case ThinkingContent:
				// Reasoning is display-only: it is valid in an assistant
				// message only alongside sendable content.
			case ToolCall:
				if b.Name == "" {
					issues = append(issues, issue(RoleAssistant, IssueMissingToolCall, "tool call has no name"))
				}
				if b.ID == "" {
					issues = append(issues, issue(RoleAssistant, IssueMissingToolCallID, "tool call has no id"))
				}
			default:
				issues = append(issues, issue(RoleAssistant, IssueUnsupportedBlock,
					fmt.Sprintf("unsupported assistant content block %T", block)))
			}
		}
		if !HasSendableContent(m) {
			issues = append(issues, issue(RoleAssistant, IssueUnsendableAssistant,
				"assistant message has no text or tool call to send"))
		}
		return issues
	case ToolResultMessage:
		var issues []MessageIssue
		if m.ToolCallID == "" {
			issues = append(issues, issue(RoleTool, IssueMissingToolCallID, "tool result has no tool call id"))
		}
		for _, block := range m.Content {
			switch b := block.(type) {
			case TextContent:
				if b.Text == "" {
					issues = append(issues, issue(RoleTool, IssueEmptyTextPart, "text content part has no text"))
				}
			case ToolResultContent:
				if b.Text == "" {
					issues = append(issues, issue(RoleTool, IssueEmptyTextPart, "tool result content part has no text"))
				}
			default:
				issues = append(issues, issue(RoleTool, IssueUnsupportedBlock,
					fmt.Sprintf("unsupported tool result content block %T", block)))
			}
		}
		return issues
	default:
		return []MessageIssue{issue("", IssueUnsupportedMessage,
			fmt.Sprintf("unsupported message type %T", msg))}
	}
}
