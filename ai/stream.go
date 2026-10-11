package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Sentinel errors returned by the streaming layer. Callers should match them
// with errors.Is rather than comparing error strings.
var (
	// ErrStreamClosed means events were requested after the stream finished.
	ErrStreamClosed = errors.New("ai: stream already closed")
	// ErrStreamAborted means the caller's context was cancelled mid-stream.
	ErrStreamAborted = errors.New("ai: stream aborted")
)

// StreamEvent is one item published while a completion is generated.
type StreamEvent interface {
	eventKind() string
}

// EventStart is published once, before any content delta, carrying a partial
// message with the model and an empty content slice.
type EventStart struct {
	Message AssistantMessage
}

func (EventStart) eventKind() string { return "start" }

// EventTextDelta carries a chunk of visible assistant text.
type EventTextDelta struct {
	Delta string
}

func (EventTextDelta) eventKind() string { return "text_delta" }

// EventThinkingDelta carries a chunk of a reasoning trace.
type EventThinkingDelta struct {
	Delta string
}

func (EventThinkingDelta) eventKind() string { return "thinking_delta" }

// EventToolCallStart announces a new tool call and its name.
type EventToolCallStart struct {
	ID   string
	Name string
}

func (EventToolCallStart) eventKind() string { return "tool_call_start" }

// EventToolCallDelta carries a raw JSON fragment of a tool call's arguments.
//
// Fragments are not independently parseable; consumers accumulate them and
// decode once, at EventToolCallEnd.
type EventToolCallDelta struct {
	ID    string
	Delta string
}

func (EventToolCallDelta) eventKind() string { return "tool_call_delta" }

// EventToolCallEnd closes a tool call and carries its decoded arguments.
type EventToolCallEnd struct {
	ID        string
	Name      string
	Arguments map[string]any
}

func (EventToolCallEnd) eventKind() string { return "tool_call_end" }

// EventDone is published once, after the final content event, with the fully
// assembled message.
type EventDone struct {
	Message AssistantMessage
}

func (EventDone) eventKind() string { return "done" }

// EventError is published instead of EventDone when the stream failed.
type EventError struct {
	Err error
}

func (EventError) eventKind() string { return "error" }

// Stream is a live completion.
//
// Events yields every event in order and is closed when the stream finishes.
// Result blocks until the stream finishes and returns the final assistant
// message, or the terminating error. Result must not be called concurrently
// with itself, but may be called after Events has been drained or abandoned.
type Stream struct {
	// Events carries the event sequence. It is closed exactly once.
	Events chan StreamEvent

	done   chan struct{}
	msg    AssistantMessage
	err    error
	once   sync.Once
	closed bool
}

// newStream returns a stream whose event channel has the given buffer.
func newStream(buffer int) *Stream {
	return &Stream{
		Events: make(chan StreamEvent, buffer),
		done:   make(chan struct{}),
	}
}

// publish sends one event, honouring context cancellation. It reports whether
// the event was delivered.
func (s *Stream) publish(ctx context.Context, event StreamEvent) bool {
	select {
	case s.Events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// NewCompletedStream returns a stream that immediately yields the given
// message and finishes.
//
// It exists for transports that do not token-by-token stream — deterministic
// or replayed responses, and tests — without forcing them to fabricate SSE.
// The returned stream's Events channel is already drained and closed.
func NewCompletedStream(msg AssistantMessage) *Stream {
	// The buffer must hold both events: publish blocks until a reader drains
	// the channel, and there is no reader in this path.
	stream := newStream(2)
	stream.publish(context.Background(), EventStart{Message: msg})
	stream.publish(context.Background(), EventDone{Message: msg})
	stream.finish(msg, nil)
	return stream
}

// NewTextStream returns a stream that emits each delta as an EventTextDelta
// before completing with their concatenation.
//
// It exists for scripting token-by-token streams in tests without an HTTP
// server: a consumer draining Events sees the same sequence a live provider
// produces, and Result reports the assembled message.
func NewTextStream(deltas ...string) *Stream {
	var sb strings.Builder
	for _, d := range deltas {
		sb.WriteString(d)
	}
	msg := AssistantMessage{
		Content: []ContentBlock{TextContent{Text: sb.String()}},
	}
	// The buffer holds the whole sequence: publishing blocks until a reader
	// drains, and there is no reader in this path.
	stream := newStream(len(deltas) + 2)
	ctx := context.Background()
	stream.publish(ctx, EventStart{Message: AssistantMessage{}})
	for _, d := range deltas {
		if d != "" {
			stream.publish(ctx, EventTextDelta{Delta: d})
		}
	}
	stream.publish(ctx, EventDone{Message: msg})
	stream.finish(msg, nil)
	return stream
}

// finish records the terminal state and closes both channels. It is safe to
// call more than once; only the first call has an effect.
func (s *Stream) finish(msg AssistantMessage, err error) {
	s.once.Do(func() {
		s.msg = msg
		s.err = err
		s.closed = true
		close(s.Events)
		close(s.done)
	})
}

// Result waits for the stream to finish and returns the final message.
//
// When the stream terminated with an error, Result returns that error and a
// partially populated message.
func (s *Stream) Result() (AssistantMessage, error) {
	<-s.done
	return s.msg, s.err
}

// ToolSpec describes one tool as sent to the model.
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Parameters is a JSON Schema object describing the argument shape.
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

// ToolChoice forces or suppresses tool use for one request.
//
// Exactly one of Type or ToolName is meaningful: Type "auto" or "any" selects
// a provider-native mode, while ToolName pins a single tool.
type ToolChoice struct {
	// Type is "auto", "none", "any", or "tool".
	Type string `json:"type"`
	// ToolName is set when Type is "tool".
	ToolName string `json:"name,omitempty"`
}

// Predefined tool choices.
var (
	// ToolChoiceAuto lets the model decide.
	ToolChoiceAuto = ToolChoice{Type: "auto"}
	// ToolChoiceNone forbids tool calls.
	ToolChoiceNone = ToolChoice{Type: "none"}
	// ToolChoiceAny requires at least one tool call.
	ToolChoiceAny = ToolChoice{Type: "any"}
)

// SystemPromptBlock is one system-prompt entry.
//
// Providers that support prompt caching mark blocks cacheable; the Anthropic
// transport uses this to attach cache_control to the final block.
type SystemPromptBlock struct {
	Text string
	// Cache marks the block as a long-lived prefix eligible for caching.
	Cache bool
}

// Request is one completion request, protocol-neutral.
type Request struct {
	// Model is the model to call.
	Model Model
	// Messages is the conversation in order.
	Messages []Message
	// System is the system prompt, in order.
	System []SystemPromptBlock
	// Tools are the tools offered to the model.
	Tools []ToolSpec
	// ToolChoice constrains tool use. The zero value lets the model decide.
	ToolChoice ToolChoice
	// MaxTokens caps the completion length.
	MaxTokens int
	// Temperature is the sampling temperature; nil leaves the provider default.
	Temperature *float64
	// Reasoning enables extended thinking when the model supports it.
	Reasoning bool
	// ReasoningEffort is the provider-agnostic effort level, e.g. "low", "medium", "high".
	ReasoningEffort string
	// Stream requests a streaming response. It is always true for these transports.
	Stream bool
	// APIKey overrides the environment-resolved credential when non-empty.
	APIKey string
	// SessionID is forwarded to providers that support session caching.
	SessionID string
	// Metadata carries provider-specific request extensions.
	Metadata map[string]string
}

// Provider streams completions for one wire protocol.
//
// Implementations must be safe for concurrent use across distinct requests.
type Provider interface {
	// Stream opens a completion. The returned stream is always non-nil when
	// err is nil, and the caller owns draining or abandoning it.
	Stream(ctx context.Context, req Request) (*Stream, error)
}

// unknownAPIError reports a model whose API has no registered provider.
type unknownAPIError struct {
	API API
}

func (e unknownAPIError) Error() string {
	return fmt.Sprintf("ai: no provider registered for API %q", string(e.API))
}

// Is allows errors.Is(err, ErrNoProvider).
func (e unknownAPIError) Is(target error) bool {
	return target == ErrNoProvider
}

// ErrNoProvider means the requested API has no registered provider.
var ErrNoProvider = errors.New("ai: no provider registered for this API")
