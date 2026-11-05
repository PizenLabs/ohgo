package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/tokens"
)

// errScriptExhausted means the scripted model was asked for a turn the scenario
// did not plan. It is a benchmark bug, not a model failure, and must be loud.
var errScriptExhausted = errors.New("phase9 benchmark: scripted model exhausted")

// step is one planned model action.
//
// A step with an empty Tool is a final assistant answer. A step with a Tool is
// a tool call with fixed arguments. The script is deliberately model-free: the
// benchmark compares runtime behaviour for an identical action sequence, so a
// measured difference is attributable to context projection rather than to a
// model deciding differently.
type step struct {
	Tool string
	Args map[string]any
	Text string
}

// scriptedModel serves a fixed action script and records every provider request.
type scriptedModel struct {
	steps []step
	index int
	// emptyServed records that the script already proposed one action against
	// an answer-only surface. It lets the script model make one stale proposal
	// (so the benchmark can observe the runtime's refusal) and then answer on
	// the bounded recovery request instead of repeating forever.
	emptyServed bool

	mu       sync.Mutex
	requests []ai.Request
}

// newScriptedModel returns a model for the given steps.
func newScriptedModel(steps ...step) *scriptedModel {
	return &scriptedModel{steps: steps}
}

// stream implements ai.StreamFn.
func (m *scriptedModel) stream(ctx context.Context, req ai.Request) (*ai.Stream, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	position := m.index
	m.index++
	// A model that is offered no tools answers from the evidence it already
	// has. The script is a fixed action list, so it would otherwise keep
	// proposing steps the runtime has deliberately withdrawn; that would test
	// the script's stubbornness rather than the runtime's answer boundary.
	// Jump to the next planned final answer when the action surface closes.
	if len(req.Tools) == 0 {
		if !m.emptyServed {
			// Serve one planned action as-is so the runtime's refusal is
			// observable; this is the stale post-answer proposal the benchmark
			// measures.
			m.emptyServed = true
		} else if answer := m.nextAnswer(position); answer >= 0 {
			position = answer
			m.index = answer + 1
		}
	}
	m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if position >= len(m.steps) {
		return nil, errScriptExhausted
	}
	next := m.steps[position]

	if next.Tool == "" {
		msg := ai.AssistantMessage{
			Content:    []ai.ContentBlock{ai.TextContent{Text: next.Text}},
			Model:      req.Model,
			StopReason: ai.StopEndTurn,
			Usage: ai.Usage{
				Input:  requestTokens(req),
				Output: tokens.Count(next.Text, tokens.ModeEstimate),
			},
		}
		msg.Usage.Total = msg.Usage.Input + msg.Usage.Output
		return ai.NewCompletedStream(msg), nil
	}

	args := next.Args
	if args == nil {
		args = map[string]any{}
	}
	id := fmt.Sprintf("phase9-call-%d", position+1)
	msg := ai.AssistantMessage{
		Content: []ai.ContentBlock{
			ai.ToolCall{ID: id, Name: next.Tool, Arguments: args},
		},
		Model:      req.Model,
		StopReason: ai.StopToolUse,
		Usage: ai.Usage{
			Input:  requestTokens(req),
			Output: toolCallTokens(next.Tool, args),
		},
	}
	msg.Usage.Total = msg.Usage.Input + msg.Usage.Output
	return ai.NewCompletedStream(msg), nil
}

// nextAnswer returns the index of the next planned final answer at or after
// position, or -1 when the script has none. The caller holds m.mu.
func (m *scriptedModel) nextAnswer(position int) int {
	for i := position; i < len(m.steps); i++ {
		if m.steps[i].Tool == "" {
			return i
		}
	}
	return -1
}

// snapshotRequests returns the recorded requests.
func (m *scriptedModel) snapshotRequests() []ai.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ai.Request, len(m.requests))
	copy(out, m.requests)
	return out
}

// requestTokens estimates the on-wire input size using the runtime's own
// estimator. It is an estimate, never a provider measurement.
func requestTokens(req ai.Request) int {
	var texts []string
	for _, block := range req.System {
		texts = append(texts, block.Text)
	}
	for _, msg := range req.Messages {
		texts = append(texts, messageText(msg))
	}
	for _, spec := range req.Tools {
		encoded, _ := json.Marshal(spec.Parameters)
		texts = append(texts, spec.Name, spec.Description, string(encoded))
	}
	return tokens.CountBlocks(texts, tokens.ModeEstimate)
}

// toolCallTokens estimates the output cost of a tool call.
func toolCallTokens(name string, args map[string]any) int {
	encoded, _ := json.Marshal(args)
	return tokens.Count(name, tokens.ModeEstimate) + tokens.Count(string(encoded), tokens.ModeEstimate)
}

// messageText flattens one message for size accounting.
func messageText(msg ai.Message) string {
	switch m := msg.(type) {
	case ai.UserMessage:
		return m.Text()
	case ai.AssistantMessage:
		var text string
		for _, block := range m.Content {
			switch b := block.(type) {
			case ai.TextContent:
				text += b.Text
			case ai.ToolCall:
				encoded, _ := json.Marshal(b.Arguments)
				text += b.ID + b.Name + string(encoded)
			}
		}
		return text
	case ai.ToolResultMessage:
		return m.Text()
	default:
		return ""
	}
}
