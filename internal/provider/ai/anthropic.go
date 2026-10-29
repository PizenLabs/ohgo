package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// AnthropicDefaults are the endpoint and version used when a model does not
// override them.
const (
	// AnthropicBaseURL is the official Messages API endpoint.
	AnthropicBaseURL = "https://api.anthropic.com"
	// AnthropicVersion is the API version pinned by this transport.
	AnthropicVersion = "2023-06-01"
)

// AnthropicProvider implements the Anthropic Messages API over SSE.
type AnthropicProvider struct {
	// Client is the HTTP client used for every request.
	Client *http.Client
	// APIKey is the credential sent as x-api-key.
	APIKey string
	// AuthToken overrides APIKey and is sent as Authorization: Bearer instead,
	// for gateway deployments that require a bearer token.
	AuthToken string
	// BaseURL overrides AnthropicBaseURL when non-empty.
	BaseURL string
	// Version overrides AnthropicVersion when non-empty.
	Version string
}

// NewAnthropicProvider returns a provider for the given credential.
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{Client: &http.Client{}, APIKey: apiKey}
}

// anthropicRequest is the Messages API request body.
type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      []anthropicSystem  `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Stream      bool               `json:"stream"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  any                `json:"tool_choice,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicSystem struct {
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// anthropicMessage carries one turn. Content is a raw JSON array so block
// shapes can be passed through without an intermediate boxing type.
type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicBlock is one content block. Fields are omitted when unset so the
// wire payload stays minimal.
type anthropicBlock struct {
	Type string `json:"type"`

	// text / thinking
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`

	// image
	Source *anthropicImageSource `json:"source,omitempty"`

	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// cache control
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// Stream implements Provider.
func (p *AnthropicProvider) Stream(ctx context.Context, req Request) (*Stream, error) {
	if p.Client == nil {
		p.Client = &http.Client{}
	}
	base := p.BaseURL
	if base == "" {
		base = AnthropicBaseURL
	}
	version := p.Version
	if version == "" {
		version = AnthropicVersion
	}

	body := anthropicRequest{
		Model:     req.Model.ID,
		System:    toAnthropicSystem(req.System),
		MaxTokens: req.MaxTokens,
		Stream:    true,
		Tools:     toAnthropicTools(req.Tools),
	}
	if body.MaxTokens <= 0 {
		body.MaxTokens = defaultMaxTokens(req.Model)
	}
	messages, err := toAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	body.Messages = messages

	if req.Temperature != nil {
		body.Temperature = req.Temperature
	}
	if req.Reasoning && req.Model.Reasoning {
		effort := req.ReasoningEffort
		if effort == "" {
			effort = "medium"
		}
		budget := thinkingBudgetForEffort(effort, req.Model.MaxOutputTokens)
		if budget > 0 {
			body.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
		}
	}
	if req.ToolChoice.ToolName != "" {
		body.ToolChoice = map[string]any{"type": "tool", "name": req.ToolChoice.ToolName}
	} else {
		switch req.ToolChoice.Type {
		case "none":
			// Anthropic has no "none"; omitting tools is the closest analogue.
			body.Tools = nil
		case "any":
			body.ToolChoice = map[string]any{"type": "any"}
		}
	}

	headers := map[string]string{
		"anthropic-version": version,
	}
	if p.AuthToken != "" {
		headers["Authorization"] = "Bearer " + p.AuthToken
	} else {
		headers["x-api-key"] = p.APIKey
	}

	resp, err := doJSON(ctx, p.Client, base+"/v1/messages", body, headers)
	if err != nil {
		return nil, err
	}

	stream := newStream(64)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		final := AssistantMessage{Model: req.Model}
		state := &anthropicAccumulator{msg: &final}

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

		if streamErr != nil {
			state.msg.StopReason = StopError
			state.msg.ErrorMessage = streamErr.Error()
			stream.finish(*state.msg, streamErr)
			return
		}
		stream.finish(*state.msg, nil)
	}()

	return stream, nil
}

func defaultMaxTokens(m Model) int {
	if m.MaxOutputTokens > 0 {
		return m.MaxOutputTokens
	}
	return 8192
}

// thinkingBudgetForEffort maps an effort level onto a token budget that always
// fits inside the model's output ceiling.
func thinkingBudgetForEffort(effort string, maxOutput int) int {
	if maxOutput <= 0 {
		maxOutput = 8192
	}
	var fraction float64
	switch effort {
	case "minimal", "low":
		fraction = 0.1
	case "medium":
		fraction = 0.25
	case "high":
		fraction = 0.5
	default:
		fraction = 0.25
	}
	budget := int(float64(maxOutput) * fraction)
	// The API rejects budgets below this floor.
	if budget < 1024 {
		budget = 1024
	}
	if budget > maxOutput {
		budget = maxOutput
	}
	return budget
}

func toAnthropicSystem(blocks []SystemPromptBlock) []anthropicSystem {
	out := make([]anthropicSystem, 0, len(blocks))
	for i, block := range blocks {
		entry := anthropicSystem{Text: block.Text}
		// Only the final cacheable block carries cache_control: Anthropic
		// caches the longest matching prefix, so marking one block is enough.
		if block.Cache && i == len(blocks)-1 {
			entry.CacheControl = &cacheControl{Type: "ephemeral"}
		}
		out = append(out, entry)
	}
	return out
}

func toAnthropicTools(specs []ToolSpec) []anthropicTool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]anthropicTool, 0, len(specs))
	for _, spec := range specs {
		schema := spec.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out = append(out, anthropicTool{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: schema,
		})
	}
	return out
}

func toAnthropicMessages(messages []Message) ([]anthropicMessage, error) {
	out := make([]anthropicMessage, 0, len(messages))
	for _, msg := range messages {
		switch m := msg.(type) {
		case UserMessage:
			blocks, err := toAnthropicUserBlocks(m.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, anthropicMessage{Role: "user", Content: blocks})
		case AssistantMessage:
			out = append(out, anthropicMessage{Role: "assistant", Content: toAnthropicAssistantBlocks(m)})
		case ToolResultMessage:
			block := anthropicBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Text(),
				IsError:   m.IsError,
			}
			// Anthropic requires tool results to arrive in a user turn.
			out = append(out, anthropicMessage{Role: "user", Content: []anthropicBlock{block}})
		default:
			return nil, fmt.Errorf("ai: unsupported message type %T", msg)
		}
	}
	return out, nil
}

func toAnthropicUserBlocks(content []ContentBlock) ([]anthropicBlock, error) {
	blocks := make([]anthropicBlock, 0, len(content))
	for _, block := range content {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: b.Text})
			}
		case ImageContent:
			blocks = append(blocks, anthropicBlock{
				Type: "image",
				Source: &anthropicImageSource{
					Type:      "base64",
					MediaType: b.MediaType,
					Data:      b.base64(),
				},
			})
		default:
			return nil, fmt.Errorf("ai: unsupported user content block %T", block)
		}
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("ai: user message has no sendable content")
	}
	return blocks, nil
}

func toAnthropicAssistantBlocks(msg AssistantMessage) []anthropicBlock {
	blocks := make([]anthropicBlock, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: b.Text})
			}
		case ThinkingContent:
			block := anthropicBlock{Type: "thinking", Text: b.Thinking, Signature: b.Signature}
			blocks = append(blocks, block)
		case ToolCall:
			blocks = append(blocks, anthropicBlock{
				Type:  "tool_use",
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Arguments,
			})
		}
	}
	if len(blocks) == 0 {
		// The API rejects an empty assistant turn.
		blocks = append(blocks, anthropicBlock{Type: "text", Text: ""})
	}
	return blocks
}

// anthropicAccumulator turns SSE events into message blocks and events.
type anthropicAccumulator struct {
	msg *AssistantMessage
	// pending holds every event produced by the most recent apply, empty when
	// the event carried nothing publishable. It is a slice because one SSE
	// event can both open a block and carry its first delta.
	pending []StreamEvent

	// blockIndex maps Anthropic's content-block index to the slice position.
	blockIndex int
	// index records the content-block index each tool call arrived under.
	toolIndexes map[string]int
}

func (a *anthropicAccumulator) apply(event sseEvent) error {
	a.pending = nil

	if event.Name == "error" {
		return fmt.Errorf("ai: anthropic stream error: %s", strings.TrimSpace(event.Data))
	}
	if event.Data == "" {
		return nil
	}

	var envelope struct {
		Type  string          `json:"type"`
		Index int             `json:"index"`
		Delta json.RawMessage `json:"delta"`
		// Usage on message_delta sits at the top level, not inside delta.
		Usage *struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		// content_block
		ContentBlock json.RawMessage `json:"content_block"`
		// message
		Message *struct {
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				CacheRead    int `json:"cache_read_input_tokens"`
				CacheWrite   int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		// error
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(event.Data), &envelope); err != nil {
		return fmt.Errorf("ai: decode anthropic event %q: %w", event.Name, err)
	}

	switch envelope.Type {
	case "message_start":
		a.msg.StopReason = ""
		if envelope.Message != nil && envelope.Message.Usage != nil {
			a.msg.Usage = Usage{
				Input:      envelope.Message.Usage.InputTokens,
				CacheRead:  envelope.Message.Usage.CacheRead,
				CacheWrite: envelope.Message.Usage.CacheWrite,
			}
		}
		a.pending = append(a.pending, EventStart{Message: *a.msg})

	case "content_block_start":
		block, err := decodeAnthropicBlock(envelope.ContentBlock)
		if err != nil {
			return err
		}
		a.blockIndex = envelope.Index
		a.msg.Content = append(a.msg.Content, block)
		if call, ok := block.(ToolCall); ok {
			if a.toolIndexes == nil {
				a.toolIndexes = map[string]int{}
			}
			a.toolIndexes[call.ID] = envelope.Index
			a.pending = append(a.pending, EventToolCallStart{ID: call.ID, Name: call.Name})
		}

	case "content_block_delta":
		var delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
		}
		if err := json.Unmarshal(envelope.Delta, &delta); err != nil {
			return fmt.Errorf("ai: decode anthropic delta: %w", err)
		}
		target := a.blockAt(envelope.Index)
		if target == nil {
			return fmt.Errorf("ai: anthropic delta for unknown block %d", envelope.Index)
		}
		switch delta.Type {
		case "text_delta":
			a.appendText(target, delta.Text)
			a.pending = append(a.pending, EventTextDelta{Delta: delta.Text})
		case "thinking_delta":
			a.appendThinking(target, delta.Thinking)
			a.pending = append(a.pending, EventThinkingDelta{Delta: delta.Thinking})
		case "signature_delta":
			if thinking, ok := (*target).(ThinkingContent); ok {
				thinking.Signature += delta.Signature
				*target = thinking
			}
		case "input_json_delta":
			if call, ok := (*target).(ToolCall); ok {
				call.appendArgs(delta.PartialJSON)
				*target = call
			}
			a.pending = append(a.pending, EventToolCallDelta{ID: blockToolCallID(*target), Delta: delta.PartialJSON})
		}

	case "content_block_stop":
		if target := a.blockAt(envelope.Index); target != nil {
			if tc, ok := (*target).(ToolCall); ok {
				args, err := decodeJSONArgs(tc.argumentsJSON)
				if err != nil {
					return err
				}
				tc.Arguments = args
				tc.argumentsJSON = ""
				*target = tc
				a.pending = append(a.pending, EventToolCallEnd{ID: tc.ID, Name: tc.Name, Arguments: args})
			}
		}

	case "message_delta":
		var delta struct {
			StopReason string `json:"stop_reason"`
		}
		if err := json.Unmarshal(envelope.Delta, &delta); err != nil {
			return fmt.Errorf("ai: decode anthropic message_delta: %w", err)
		}
		if delta.StopReason != "" {
			a.msg.StopReason = mapAnthropicStopReason(delta.StopReason)
		}
		if envelope.Usage != nil {
			a.msg.Usage.Output = envelope.Usage.OutputTokens
		}

	case "message_stop":
		if a.msg.StopReason == "" {
			a.msg.StopReason = StopEndTurn
		}
		a.msg.Usage.Total = a.msg.Usage.Input + a.msg.Usage.Output + a.msg.Usage.CacheRead
		a.pending = append(a.pending, EventDone{Message: *a.msg})

	case "error":
		if envelope.Error != nil {
			return fmt.Errorf("ai: anthropic %s: %s", envelope.Error.Type, envelope.Error.Message)
		}
		return fmt.Errorf("ai: anthropic error: %s", strings.TrimSpace(event.Data))
	}
	return nil
}

// blockAt returns a pointer to the content block at the given slice position.
func (a *anthropicAccumulator) blockAt(index int) *ContentBlock {
	if index < 0 || index >= len(a.msg.Content) {
		return nil
	}
	return &a.msg.Content[index]
}

func (a *anthropicAccumulator) appendText(target *ContentBlock, text string) {
	switch b := (*target).(type) {
	case TextContent:
		b.Text += text
		*target = b
	case ToolCall:
		// A stray text delta on a tool block is ignored.
	default:
		*target = TextContent{Text: text}
	}
}

func (a *anthropicAccumulator) appendThinking(target *ContentBlock, text string) {
	switch b := (*target).(type) {
	case ThinkingContent:
		b.Thinking += text
		*target = b
	default:
		*target = ThinkingContent{Thinking: text}
	}
}

func blockToolCallID(block ContentBlock) string {
	if call, ok := block.(ToolCall); ok {
		return call.ID
	}
	return ""
}

func decodeAnthropicBlock(raw json.RawMessage) (ContentBlock, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("ai: empty content_block payload")
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("ai: decode content_block type: %w", err)
	}
	switch head.Type {
	case "text":
		var block struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		return TextContent{Text: block.Text}, nil
	case "thinking":
		var block struct {
			Thinking string `json:"thinking"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		return ThinkingContent{Thinking: block.Thinking}, nil
	case "tool_use":
		var block struct {
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		// Anthropic always sends an empty input object here and streams the
		// real arguments through input_json_delta. Keeping a non-nil empty map
		// would make content_block_stop believe the call already had input.
		return ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input}, nil
	case "redacted_thinking":
		// The opaque payload is not replayable; drop it.
		return TextContent{Text: ""}, nil
	default:
		return nil, fmt.Errorf("ai: unsupported anthropic content block %q", head.Type)
	}
}

func mapAnthropicStopReason(reason string) StopReason {
	switch reason {
	case "end_turn", "stop_sequence":
		return StopEndTurn
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	case "refusal":
		return StopRefusal
	default:
		return StopEndTurn
	}
}
