package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OpenAIDefaults are the endpoint used when a model does not override it.
const (
	// OpenAIBaseURL is the official Chat Completions endpoint.
	OpenAIBaseURL = "https://api.openai.com/v1"
)

// OpenAIProvider implements the OpenAI Chat Completions API over SSE.
type OpenAIProvider struct {
	// Client is the HTTP client used for every request.
	Client *http.Client
	// APIKey is the credential sent as Authorization: Bearer.
	APIKey string
	// Organization is forwarded as OpenAI-Organization when non-empty.
	Organization string
	// BaseURL overrides OpenAIBaseURL when non-empty.
	BaseURL string
	// ExtraHeaders are sent with every request. OpenRouter uses them for
	// attribution (HTTP-Referer, X-Title): application constants, never
	// user-specific values.
	ExtraHeaders map[string]string
}

// NewOpenAIProvider returns a provider for the given credential.
func NewOpenAIProvider(apiKey string) *OpenAIProvider {
	return &OpenAIProvider{Client: &http.Client{}, APIKey: apiKey}
}

// openAIRequest is the Chat Completions request body.
type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream"`
	Tools       []openAITool    `json:"tools,omitempty"`
	ToolChoice  any             `json:"tool_choice,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	// StreamOptions asks for usage in the final chunk.
	StreamOptions   *openAIStreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role       string                 `json:"role"`
	Content    []openAIContent        `json:"content,omitempty"`
	ToolCalls  []openAIReplayToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	Name       string                 `json:"name,omitempty"`
}

// openAIContent is one content part. Type discriminates the shape.
type openAIContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// openAIReplayToolCall is an assistant tool call as sent back in history.
//
// It is a separate type from the streaming wire shape because the two carry
// different fields: replay needs a name and complete argument JSON, while a
// streamed delta needs a positional index and partial fragments.
type openAIReplayToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function openAIReplayFunction `json:"function"`
}

type openAIReplayFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// openAIWireToolCall is one tool-call fragment from a streaming delta.
//
// Index is the only stable identity mid-stream: ids arrive on the first
// fragment and names may follow later, so callers key accumulators by Index.
type openAIWireToolCall struct {
	Index    int                 `json:"index"`
	ID       string              `json:"id,omitempty"`
	Type     string              `json:"type,omitempty"`
	Function *openAIWireFunction `json:"function,omitempty"`
}

type openAIWireFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// Stream implements Provider.
func (p *OpenAIProvider) Stream(ctx context.Context, req Request) (*Stream, error) {
	if p.Client == nil {
		p.Client = &http.Client{}
	}
	base := p.BaseURL
	if base == "" {
		base = OpenAIBaseURL
	}

	body := openAIRequest{
		Model:         req.Model.ID,
		Stream:        true,
		StreamOptions: &openAIStreamOptions{IncludeUsage: true},
		MaxTokens:     req.MaxTokens,
	}
	if body.MaxTokens <= 0 {
		body.MaxTokens = defaultMaxTokens(req.Model)
	}
	if req.Temperature != nil {
		body.Temperature = req.Temperature
	}
	if req.Reasoning && req.Model.Reasoning {
		body.ReasoningEffort = req.ReasoningEffort
		if body.ReasoningEffort == "" {
			body.ReasoningEffort = "medium"
		}
	}

	messages, err := toOpenAIMessages(req.Messages, req.System)
	if err != nil {
		return nil, err
	}
	body.Messages = messages
	body.Tools = toOpenAITools(req.Tools)
	if req.ToolChoice.ToolName != "" {
		body.ToolChoice = map[string]any{
			"type":     "function",
			"function": map[string]any{"name": req.ToolChoice.ToolName},
		}
	} else {
		switch req.ToolChoice.Type {
		case "none":
			body.ToolChoice = "none"
		case "any":
			body.ToolChoice = "required"
		}
	}

	headers := map[string]string{"Authorization": "Bearer " + p.APIKey}
	if p.Organization != "" {
		headers["OpenAI-Organization"] = p.Organization
	}
	for key, value := range p.ExtraHeaders {
		if value != "" {
			headers[key] = value
		}
	}

	resp, err := doJSON(ctx, p.Client, base+"/chat/completions", body, headers)
	if err != nil {
		return nil, err
	}

	stream := newStream(64)
	go func() {
		defer resp.Body.Close()
		final := AssistantMessage{Model: req.Model}
		state := newOpenAIAccumulator(&final)

		streamErr := readSSE(ctx, resp.Body, func(event sseEvent) error {
			if err := state.apply(event); err != nil {
				return err
			}
			for _, pending := range state.pending {
				if !stream.publish(ctx, pending) {
					return ctx.Err()
				}
			}
			state.pending = nil
			return nil
		})

		if streamErr == nil {
			// The protocol has no tool_call_end, so close the calls here
			// before the done event that carries the assembled message.
			calls, err := state.finalize()
			if err != nil {
				streamErr = err
			} else {
				for _, call := range calls {
					event := EventToolCallEnd{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
					if !stream.publish(ctx, event) {
						streamErr = ctx.Err()
						break
					}
				}
			}
		}

		if streamErr != nil {
			state.msg.StopReason = StopError
			state.msg.ErrorMessage = streamErr.Error()
			stream.finish(*state.msg, streamErr)
			return
		}
		if state.msg.StopReason == "" {
			state.msg.StopReason = StopEndTurn
		}
		if state.msg.Usage.Total == 0 {
			state.msg.Usage.Total = state.msg.Usage.Input + state.msg.Usage.Output + state.msg.Usage.CacheRead
		}
		stream.publish(ctx, EventDone{Message: *state.msg})
		stream.finish(*state.msg, nil)
	}()

	return stream, nil
}

func toOpenAIMessages(messages []Message, system []SystemPromptBlock) ([]openAIMessage, error) {
	out := make([]openAIMessage, 0, len(messages)+len(system))
	if text := joinSystem(system); text != "" {
		out = append(out, openAIMessage{Role: "system", Content: []openAIContent{{Type: "text", Text: text}}})
	}

	for _, msg := range messages {
		switch m := msg.(type) {
		case UserMessage:
			content, err := toOpenAIUserContent(m.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, openAIMessage{Role: "user", Content: content})
		case AssistantMessage:
			out = append(out, openAIAssistantMessage(m))
		case ToolResultMessage:
			out = append(out, openAIMessage{
				Role:       "tool",
				ToolCallID: m.ToolCallID,
				Name:       m.ToolName,
				Content:    []openAIContent{{Type: "text", Text: m.Text()}},
			})
		default:
			return nil, fmt.Errorf("ai: unsupported message type %T", msg)
		}
	}
	return out, nil
}

func joinSystem(blocks []SystemPromptBlock) string {
	var parts []string
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func toOpenAIUserContent(content []ContentBlock) ([]openAIContent, error) {
	parts := make([]openAIContent, 0, len(content))
	for _, block := range content {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				parts = append(parts, openAIContent{Type: "text", Text: b.Text})
			}
		case ImageContent:
			parts = append(parts, openAIContent{
				Type:     "image_url",
				ImageURL: &openAIImageURL{URL: "data:" + b.MediaType + ";base64," + b.base64()},
			})
		default:
			return nil, fmt.Errorf("ai: unsupported user content block %T", block)
		}
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("ai: user message has no sendable content")
	}
	return parts, nil
}

func openAIAssistantMessage(msg AssistantMessage) openAIMessage {
	out := openAIMessage{Role: "assistant"}
	for _, block := range msg.Content {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				out.Content = append(out.Content, openAIContent{Type: "text", Text: b.Text})
			}
		case ToolCall:
			args, _ := json.Marshal(b.Arguments)
			out.ToolCalls = append(out.ToolCalls, openAIReplayToolCall{
				ID:   b.ID,
				Type: "function",
				Function: openAIReplayFunction{
					Name:      b.Name,
					Arguments: string(args),
				},
			})
		}
	}
	// OpenAI requires exactly one of content or tool_calls to be present.
	if len(out.Content) == 0 && len(out.ToolCalls) == 0 {
		out.Content = append(out.Content, openAIContent{Type: "text", Text: ""})
	}
	return out
}

func toOpenAITools(specs []ToolSpec) []openAITool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]openAITool, 0, len(specs))
	for _, spec := range specs {
		schema := spec.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  schema,
			},
		})
	}
	return out
}

// openAIAccumulator turns SSE chunks into message blocks and events.
type openAIAccumulator struct {
	msg *AssistantMessage
	// pending holds every event produced by the most recent apply, empty when
	// the chunk carried nothing publishable. It is a slice because a single
	// chunk can both open a call and carry its first argument fragment.
	pending []StreamEvent

	// toolCalls indexes tool calls by their stream-provided index, which is
	// the only stable identity mid-stream.
	toolCalls map[int]*ToolCall
	// contentIndex records the position in msg.Content of each call, since
	// text blocks may be interleaved between tool calls.
	contentIndex map[int]int
	// order preserves the first-seen index order so results are deterministic.
	order []int
}

func newOpenAIAccumulator(msg *AssistantMessage) *openAIAccumulator {
	return &openAIAccumulator{
		msg:          msg,
		toolCalls:    map[int]*ToolCall{},
		contentIndex: map[int]int{},
	}
}

func (a *openAIAccumulator) apply(event sseEvent) error {
	a.pending = nil

	if event.Data == "" {
		return nil
	}
	if strings.TrimSpace(event.Data) == "[DONE]" {
		return nil
	}

	var chunk struct {
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Role      string               `json:"role"`
				Content   string               `json:"content"`
				ToolCalls []openAIWireToolCall `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			PromptDetails    *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
		return fmt.Errorf("ai: decode openai chunk: %w", err)
	}
	if chunk.Error != nil {
		return fmt.Errorf("ai: openai %s: %s", chunk.Error.Type, chunk.Error.Message)
	}
	if chunk.Usage != nil {
		a.msg.Usage = Usage{
			Input:     chunk.Usage.PromptTokens,
			Output:    chunk.Usage.CompletionTokens,
			Total:     chunk.Usage.TotalTokens,
			CacheRead: cachedTokens(chunk.Usage.PromptDetails),
		}
	}

	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			a.appendText(choice.Delta.Content)
			a.pending = append(a.pending, EventTextDelta{Delta: choice.Delta.Content})
		}
		for _, wireCall := range choice.Delta.ToolCalls {
			if err := a.applyToolCallDelta(wireCall); err != nil {
				return err
			}
		}
		if choice.FinishReason != nil {
			a.msg.StopReason = mapOpenAIFinishReason(*choice.FinishReason)
		}
	}
	return nil
}

func cachedTokens(details *struct {
	CachedTokens int `json:"cached_tokens"`
}) int {
	if details == nil {
		return 0
	}
	return details.CachedTokens
}

func (a *openAIAccumulator) appendText(text string) {
	for i := len(a.msg.Content) - 1; i >= 0; i-- {
		if t, ok := a.msg.Content[i].(TextContent); ok {
			t.Text += text
			a.msg.Content[i] = t
			return
		}
	}
	// OpenAI sends role/content as separate chunks, so the first content chunk
	// must create the block.
	a.msg.Content = append(a.msg.Content, TextContent{Text: text})
}

func (a *openAIAccumulator) applyToolCallDelta(wire openAIWireToolCall) error {
	call, known := a.toolCalls[wire.Index]
	if !known {
		call = &ToolCall{}
		a.toolCalls[wire.Index] = call
		a.order = append(a.order, wire.Index)
		a.contentIndex[wire.Index] = len(a.msg.Content)
		a.msg.Content = append(a.msg.Content, *call)
	}
	if wire.ID != "" {
		call.ID = wire.ID
	}
	if wire.Function == nil {
		if !known {
			a.pending = append(a.pending, EventToolCallStart{ID: call.ID})
		}
		return nil
	}
	if wire.Function.Name != "" {
		call.Name = wire.Function.Name
	}
	// Announce the call only once its name is known, so a consumer building a
	// preview from the start event is never shown an empty tool name.
	if !known {
		a.pending = append(a.pending, EventToolCallStart{ID: call.ID, Name: call.Name})
	}
	if wire.Function.Arguments != "" {
		call.appendArgs(wire.Function.Arguments)
		a.pending = append(a.pending, EventToolCallDelta{ID: call.ID, Delta: wire.Function.Arguments})
	}
	// Mirror the accumulator into the content slice at its recorded position.
	a.msg.Content[a.contentIndex[wire.Index]] = *call
	return nil
}

// finalize decodes every accumulated tool call and returns them in order.
//
// The OpenAI protocol has no explicit tool-call terminator, so decryption of
// the accumulated argument JSON happens once the stream drains.
func (a *openAIAccumulator) finalize() ([]ToolCall, error) {
	out := make([]ToolCall, 0, len(a.order))
	for _, index := range a.order {
		call := *a.toolCalls[index]
		if call.Name == "" {
			// The API always sends a name before arguments.
			continue
		}
		args, err := decodeJSONArgs(call.argumentsJSON)
		if err != nil {
			return nil, err
		}
		call.Arguments = args
		call.argumentsJSON = ""
		position := a.contentIndex[index]
		a.msg.Content[position] = call
		out = append(out, call)
	}
	return out, nil
}

func mapOpenAIFinishReason(reason string) StopReason {
	switch reason {
	case "stop":
		return StopEndTurn
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	case "content_filter":
		return StopRefusal
	default:
		return StopEndTurn
	}
}
