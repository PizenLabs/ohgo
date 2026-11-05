package tui

import (
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// EntriesFromMessages rebuilds a transcript view from agent messages.
//
// It exists for session resume: history continues the conversation for the
// model, and these entries continue it for the eyes. Both paths consume the
// same canonical content blocks — text, reasoning, and tool calls in the order
// the provider produced them — so scrolling cannot reveal a representation the
// live transcript never had.
//
// Stored assistant content is re-canonicalized here because sessions written
// before stream normalization persisted provider protocol inside ordinary
// text. That repair happens at the history boundary, never in the renderer: a
// viewport must not know a provider's wire syntax exists.
func EntriesFromMessages(messages []agent.Message) []Entry {
	results := map[string]agent.ToolResultMessage{}
	paired := map[string]bool{}
	for _, msg := range messages {
		switch m := msg.(type) {
		case agent.ToolResultMessage:
			results[m.ToolCallID] = m
		case agent.AssistantMessage:
			for _, call := range toolCallsOf(m.Message) {
				paired[call.ID] = true
			}
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
			for _, block := range canonicalAssistantBlocks(m.Message) {
				switch b := block.(type) {
				case ai.TextContent:
					entries = appendAssistantEntry(entries, b.Text)
				case ai.ThinkingContent:
					entries = appendReasoningEntry(entries, b.Thinking)
				case ai.ToolCall:
					entries = append(entries, toolEntryFor(b, results[b.ID]))
				}
			}
		case agent.ToolResultMessage:
			// Rendered with its call above; a result without a recorded call
			// still deserves a line rather than silence.
			if !paired[m.ToolCallID] {
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

// canonicalAssistantBlocks returns the stored message's content blocks in
// canonical provider-neutral form.
//
// NormalizeMessage is idempotent for content produced after stream
// normalization, so it repairs legacy rows without changing new ones.
func canonicalAssistantBlocks(msg ai.AssistantMessage) []ai.ContentBlock {
	return ai.NormalizeMessage(msg, ai.NormalizeOptions{}).Content
}

// appendAssistantEntry merges adjacent text blocks, matching the live path's
// one-assistant-block-per-text-run rule.
func appendAssistantEntry(entries []Entry, text string) []Entry {
	if strings.TrimSpace(text) == "" {
		return entries
	}
	if len(entries) > 0 && entries[len(entries)-1].Kind == EntryAssistant {
		entries[len(entries)-1].AssistantText += text
		return entries
	}
	return append(entries, Entry{Kind: EntryAssistant, AssistantText: text})
}

// appendReasoningEntry merges adjacent reasoning blocks. It stays collapsed:
// the default review surface is the answer, with the trace one chord away.
func appendReasoningEntry(entries []Entry, text string) []Entry {
	if strings.TrimSpace(text) == "" {
		return entries
	}
	if len(entries) > 0 && entries[len(entries)-1].Kind == EntryReasoning && entries[len(entries)-1].Reasoning != nil {
		entries[len(entries)-1].Reasoning.Text += text
		return entries
	}
	return append(entries, Entry{Kind: EntryReasoning, Reasoning: &ReasoningEntry{Text: text, Closed: true}})
}

// toolCallsOf extracts the tool calls a stored assistant message requested.
func toolCallsOf(msg ai.AssistantMessage) []ai.ToolCall {
	var out []ai.ToolCall
	for _, block := range canonicalAssistantBlocks(msg) {
		if call, ok := block.(ai.ToolCall); ok {
			out = append(out, call)
		}
	}
	return out
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
