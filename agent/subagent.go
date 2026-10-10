package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/ai"
)

// ReadOnlyToolNames is the set of built-in tools whose approval tier is "read".
//
// It mirrors the original's READ_ONLY_TOOL_NAMES. An agent is read-only iff its
// declared tools are a non-empty subset of this set. The fail-safe direction
// matters: any unknown tool makes the agent *not* read-only, because assuming
// write access is safe would let an unrecognised tool mutate the repository.
var ReadOnlyToolNames = map[string]bool{
	"read":    true,
	"wait":    true,
	"grep":    true,
	"glob":    true,
	"find":    true,
	"todo":    true,
	"yield":   true,
	"ask":     true,
	"recall":  true,
	"reflect": true,
	"retain":  true,
}

// IsReadOnly reports whether a tool set can only observe.
//
// An empty set is not read-only: it declares nothing, so nothing is known about
// what the agent may do, and the safe answer to "unknown" is "not read-only".
func IsReadOnly(tools []Tool) bool {
	if len(tools) == 0 {
		return false
	}
	for _, tool := range tools {
		if !ReadOnlyToolNames[tool.Name()] {
			return false
		}
	}
	return true
}

// ErrNoSubagentResult is returned when a subagent finished without producing
// any assistant text.
//
// An empty result would read as "the subagent answered nothing", which is
// indistinguishable from a silent failure. Returning the parent an error
// instead keeps the two apart.
var ErrNoSubagentResult = errors.New("agent: subagent produced no output")

// ErrNoSystemPrompt is returned when a subagent was given no system prompt.
//
// The absence is an error rather than a fallback to the parent's prompt, because
// handing a child an identity it does not have is exactly what isolation exists
// to prevent.
var ErrNoSystemPrompt = errors.New("agent: subagent requires its own system prompt")

// SubagentSpec describes one child agent.
type SubagentSpec struct {
	// Task is the assignment, sent as the child's only user message.
	Task string
	// System is the child's system prompt, in order. It is not the parent's:
	// a subagent that inherited the parent's identity would behave as though it
	// were the main agent, which is the failure mode this field exists to
	// prevent.
	System []ai.SystemPromptBlock
	// Tools are offered to the child. When empty the parent's set is used.
	Tools []Tool
	// MaxTokens caps each completion. Zero inherits the parent's budget.
	MaxTokens int
	// Reasoning and ReasoningEffort are inherited when zero.
	Reasoning       bool
	ReasoningEffort string
	// Depth is this subagent's nesting level, used with MaxDepth to bound
	// recursion.
	Depth int
	// ReadOnly requests that the child be restricted to observing. When set the
	// tool set must be read-only; a set containing a write-tier tool is refused
	// rather than silently filtered, because silently dropping a tool the task
	// needs would make the child fail for a reason it cannot see.
	ReadOnly bool
}

// SubagentResult is what a child agent produced.
type SubagentResult struct {
	// Text is the child's final assistant text, which is what the parent's tool
	// result carries.
	Text string
	// Turns is the child's turn list, for callers that want more than the final
	// answer.
	Turns []Turn
	// ToolCalls counts the tool calls the child made.
	ToolCalls int
	// ReadOnly reports whether the child could only observe.
	ReadOnly bool
}

// RunSubagent runs a child loop derived from a parent configuration.
//
// The isolation is the point. A subagent that shared the parent's message
// history would inherit a conversation about a different problem, and its
// answers would be coloured by context the parent never intended it to see.
// It therefore starts from exactly one user message — the task — and its own
// system prompt.
//
// Everything that is not context is inherited: the stream, the model, the
// approval policy, and compaction. A child that could not write should be
// stopped by its tool set, not by a flag the caller might forget to set.
func RunSubagent(ctx context.Context, parent Config, spec SubagentSpec) (SubagentResult, error) {
	if strings.TrimSpace(spec.Task) == "" {
		return SubagentResult{}, errors.New("agent: subagent task is empty")
	}
	if len(spec.System) == 0 {
		// Deliberately not inherited: falling back to the parent's prompt would
		// hand the child an identity it does not have.
		return SubagentResult{}, ErrNoSystemPrompt
	}

	tools := spec.Tools
	if len(tools) == 0 {
		tools = parent.Tools
	}
	if len(tools) == 0 {
		return SubagentResult{}, ErrNoTools
	}

	// A read-only request against a tool set that can write is refused, not
	// filtered. Dropping the offending tool would leave the child unable to do
	// part of its task with no way to report why.
	if spec.ReadOnly && !IsReadOnly(tools) {
		return SubagentResult{}, fmt.Errorf(
			"agent: subagent requested read-only, but its tool set can write (%s)",
			firstWriteTool(tools))
	}

	maxTokens := spec.MaxTokens
	if maxTokens <= 0 {
		maxTokens = parent.MaxTokens
	}

	child := Config{
		Model:           parent.Model,
		ModelFn:         parent.ModelFn,
		Stream:          parent.Stream,
		System:          spec.System,
		Tools:           tools,
		MaxTokens:       maxTokens,
		Reasoning:       spec.Reasoning || parent.Reasoning,
		ReasoningEffort: firstNonEmpty(spec.ReasoningEffort, parent.ReasoningEffort),
		ToolChoice:      parent.ToolChoice,
		SessionID:       parent.SessionID,
		// A child may not rewrite the parent's context, and its own compaction
		// would need its own summarizer to be correct.
		TransformContext:    nil,
		GetToolChoice:       nil,
		GetSteeringMessages: nil,
		BeforeToolCall:      parent.BeforeToolCall,
		AfterToolCall:       parent.AfterToolCall,
		Approve:             parent.Approve,
		Compaction:          parent.Compaction,
		Summarize:           parent.Summarize,
		OnCompact:           parent.OnCompact,
		// Progress and turns are the parent's concern; a child that emitted
		// them would double-count into a UI that is already showing the
		// parent's turn.
		OnTurn:         nil,
		OnToolProgress: nil,
	}

	loop, err := New(child)
	if err != nil {
		return SubagentResult{}, fmt.Errorf("agent: build subagent loop: %w", err)
	}

	turns, err := loop.Run(ctx, []Message{UserMessage{Text: spec.Task}})
	if err != nil {
		return SubagentResult{}, err
	}

	text := ""
	toolCalls := 0
	for _, turn := range turns {
		toolCalls += len(turn.Results)
		if t := strings.TrimSpace(turn.Assistant.Text()); t != "" {
			// A later turn is more considered than an earlier one: between them
			// the child read tool output it did not have before.
			text = t
		}
	}
	if text == "" {
		return SubagentResult{Turns: turns, ToolCalls: toolCalls}, ErrNoSubagentResult
	}

	return SubagentResult{
		Text:      text,
		Turns:     turns,
		ToolCalls: toolCalls,
		ReadOnly:  IsReadOnly(tools),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// firstWriteTool names a tool that can mutate state, for an error message.
func firstWriteTool(tools []Tool) string {
	for _, tool := range tools {
		if !ReadOnlyToolNames[tool.Name()] {
			return tool.Name()
		}
	}
	return "(none)"
}
