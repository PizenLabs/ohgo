package tui

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// EntriesFromMessages rebuilds a transcript view from agent messages.
//
// It exists for session resume: history continues the conversation for the
// model, and these entries continue it for the eyes. Tool calls pair with
// their results by call id; an unpaired call keeps its card with a note
// rather than vanishing, because a transcript that drops work it did lies
// about what happened.
func EntriesFromMessages(messages []agent.Message) []Entry {
	results := map[string]agent.ToolResultMessage{}
	for _, msg := range messages {
		if result, ok := msg.(agent.ToolResultMessage); ok {
			results[result.ToolCallID] = result
		}
	}

	var entries []Entry
	for _, msg := range messages {
		switch m := msg.(type) {
		case agent.UserMessage:
			if strings.TrimSpace(m.Text) == "" {
				continue
			}
			entries = append(entries, Entry{Kind: EntryUser, UserText: m.Text})
		case agent.AssistantMessage:
			if text := strings.TrimSpace(m.Message.Text()); text != "" {
				entries = append(entries, Entry{Kind: EntryAssistant, AssistantText: text})
			}
			for _, call := range toolCallsOf(m.Message) {
				entries = append(entries, toolEntryFor(call, results[call.ID]))
			}
		case agent.ToolResultMessage:
			// Rendered with its call above; a result without a recorded call
			// still deserves a line rather than silence.
			if !pairedCall(messages, m.ToolCallID) {
				entries = append(entries, Entry{
					Kind: EntryTool,
					Tool: &ToolEntry{
						ID:         m.ToolCallID,
						Name:       m.ToolName,
						Invocation: m.ToolName,
						Status:     errorOrDone(m.IsError),
						Text:       m.Text(),
						Summary:    CompactResultSummary(m.Text(), m.IsError),
					},
				})
			}
		case agent.Notification:
			// UI-only: never persisted, never replayed.
		}
	}
	return entries
}

// toolCallsOf extracts the tool calls a stored assistant message requested.
func toolCallsOf(msg ai.AssistantMessage) []ai.ToolCall {
	var out []ai.ToolCall
	for _, block := range msg.Content {
		if call, ok := block.(ai.ToolCall); ok {
			out = append(out, call)
		}
	}
	return out
}

// pairedCall reports whether a call id was requested by any assistant turn,
// so orphan results can be detected without a second index.
func pairedCall(messages []agent.Message, id string) bool {
	if id == "" {
		return false
	}
	for _, msg := range messages {
		assistant, ok := msg.(agent.AssistantMessage)
		if !ok {
			continue
		}
		for _, call := range toolCallsOf(assistant.Message) {
			if call.ID == id {
				return true
			}
		}
	}
	return false
}

// toolEntryFor pairs one call with its result, if any arrived.
func toolEntryFor(call ai.ToolCall, result agent.ToolResultMessage) Entry {
	invocation := ToolSummaryForCall(call.Name, call.Arguments)
	entry := Entry{
		Kind: EntryTool,
		Tool: &ToolEntry{
			ID:         call.ID,
			Name:       call.Name,
			Invocation: invocation,
			Summary:    invocation,
			Status:     ToolDone,
		},
	}
	if result.ToolCallID == "" {
		entry.Tool.Summary = ""
		entry.Tool.Text = "(earlier result not shown)"
		return entry
	}
	entry.Tool.Status = errorOrDone(result.IsError)
	entry.Tool.Text = result.Text()
	entry.Tool.Summary = CompactResultSummary(result.Text(), result.IsError)
	return entry
}

func errorOrDone(isError bool) ToolStatus {
	if isError {
		return ToolError
	}
	return ToolDone
}
