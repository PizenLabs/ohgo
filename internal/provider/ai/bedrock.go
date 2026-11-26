package ai

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"
)

// APIBedrockConverse is the Amazon Bedrock Converse streaming protocol. It is
// not SSE: ConverseStream answers in the AWS binary event-stream encoding.
const APIBedrockConverse API = "bedrock-converse"

// BedrockDefaultBaseURL is the us-east-1 runtime root. The region is part of
// the hostname, so Registry.EndpointFor rewrites it from AWS_REGION when the
// caller did not set an explicit endpoint.
const BedrockDefaultBaseURL = "https://bedrock-runtime.us-east-1.amazonaws.com"

// BedrockRegionEnv names the environment variables consulted for the default
// region. AWS_REGION wins over AWS_DEFAULT_REGION, matching the AWS CLI.
var BedrockRegionEnv = []string{"AWS_REGION", "AWS_DEFAULT_REGION"}

// BedrockBaseURL returns the runtime root for a region.
func BedrockBaseURL(region string) string {
	region = strings.TrimSpace(region)
	if region == "" {
		region = "us-east-1"
	}
	return "https://bedrock-runtime." + region + ".amazonaws.com"
}

// BedrockProvider streams completions through ConverseStream.
//
// Authentication is an Amazon Bedrock API key, sent as a bearer token. That is
// the credential this build can resolve through the one-key provider registry;
// SigV4/IAM resolution is deliberately not guessed at here.
type BedrockProvider struct {
	// Client is the HTTP client used for every request.
	Client *http.Client
	// APIKey is the Bedrock API key, sent as Authorization: Bearer.
	APIKey string
	// BaseURL overrides BedrockDefaultBaseURL when non-empty.
	BaseURL string
}

// NewBedrockProvider returns a provider for the given Bedrock API key.
func NewBedrockProvider(apiKey string) *BedrockProvider {
	return &BedrockProvider{Client: newHTTPClient(DefaultResponseHeaderTimeout), APIKey: apiKey}
}

type bedrockRequest struct {
	Messages   []bedrockMessage     `json:"messages"`
	System     []bedrockSystemBlock `json:"system,omitempty"`
	Inference  *bedrockInference    `json:"inferenceConfig,omitempty"`
	ToolConfig *bedrockToolConfig   `json:"toolConfig,omitempty"`
	// Additional carries model-family request fields Converse does not model.
	// Bedrock's Claude entries use it for the thinking budget.
	Additional map[string]any `json:"additionalModelRequestFields,omitempty"`
}

type bedrockInference struct {
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type bedrockSystemBlock struct {
	Text *string `json:"text,omitempty"`
}

type bedrockMessage struct {
	Role    string           `json:"role"`
	Content []bedrockContent `json:"content"`
}

type bedrockContent struct {
	Text             *string                  `json:"text,omitempty"`
	Image            *bedrockImage            `json:"image,omitempty"`
	ToolUse          *bedrockToolUse          `json:"toolUse,omitempty"`
	ToolResult       *bedrockToolResult       `json:"toolResult,omitempty"`
	ReasoningContent *bedrockReasoningContent `json:"reasoningContent,omitempty"`
}

type bedrockImage struct {
	Format string             `json:"format"`
	Source bedrockImageSource `json:"source"`
}

type bedrockImageSource struct {
	Bytes string `json:"bytes"`
}

type bedrockToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

type bedrockToolResult struct {
	ToolUseID string                     `json:"toolUseId"`
	Content   []bedrockToolResultContent `json:"content"`
	Status    string                     `json:"status,omitempty"`
}

type bedrockToolResultContent struct {
	Text *string `json:"text,omitempty"`
}

type bedrockReasoningContent struct {
	ReasoningText *bedrockReasoningText `json:"reasoningText,omitempty"`
}

type bedrockReasoningText struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

type bedrockToolConfig struct {
	Tools      []bedrockTool      `json:"tools"`
	ToolChoice *bedrockToolChoice `json:"toolChoice,omitempty"`
}

type bedrockTool struct {
	ToolSpec *bedrockToolSpec `json:"toolSpec,omitempty"`
}

type bedrockToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema bedrockToolInputSchema `json:"inputSchema"`
}

type bedrockToolInputSchema struct {
	JSON map[string]any `json:"json"`
}

type bedrockToolChoice struct {
	Auto *struct{}            `json:"auto,omitempty"`
	Any  *struct{}            `json:"any,omitempty"`
	Tool *bedrockSpecificTool `json:"tool,omitempty"`
}

type bedrockSpecificTool struct {
	Name string `json:"name"`
}

// Stream implements Provider.
func (p *BedrockProvider) Stream(ctx context.Context, req Request) (*Stream, error) {
	if p.Client == nil {
		p.Client = newHTTPClient(DefaultResponseHeaderTimeout)
	}
	base := p.BaseURL
	if base == "" {
		base = BedrockDefaultBaseURL
	}
	body, err := toBedrockRequest(req)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Accept": "application/vnd.amazon.eventstream",
	}
	if p.APIKey != "" {
		headers["Authorization"] = "Bearer " + p.APIKey
	}
	endpoint := strings.TrimRight(base, "/") + "/model/" + url.PathEscape(req.Model.ID) + "/converse-stream"
	resp, err := doJSON(ctx, p.Client, endpoint, body, headers)
	if err != nil {
		return nil, err
	}

	stream := newStream(64)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		final := AssistantMessage{Model: req.Model}
		state := newBedrockAccumulator(&final)
		if !stream.publish(ctx, EventStart{Message: final}) {
			stream.finish(final, ctx.Err())
			return
		}

		streamErr := readBedrockEventStream(ctx, resp.Body, func(event bedrockEvent) error {
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
			streamErr = state.finalize()
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

func toBedrockRequest(req Request) (bedrockRequest, error) {
	out := bedrockRequest{}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens(req.Model)
	}
	out.Inference = &bedrockInference{MaxTokens: maxTokens, Temperature: req.Temperature}
	if req.Reasoning && req.Model.Reasoning && req.ReasoningEffort != "" {
		// Bedrock's Claude models take the thinking budget through
		// additionalModelRequestFields; the Converse inferenceConfig has no
		// effort vocabulary. The budget is drawn from the completion budget
		// actually sent, matching the Anthropic transport's constraint.
		if budget := thinkingBudgetForEffort(req.ReasoningEffort, maxTokens); budget > 0 {
			out.Additional = map[string]any{
				"thinking": map[string]any{"type": "enabled", "budget_tokens": budget},
			}
		}
	}
	for _, block := range req.System {
		if block.Text == "" {
			continue
		}
		text := block.Text
		out.System = append(out.System, bedrockSystemBlock{Text: &text})
	}
	messages, err := toBedrockMessages(req.Messages)
	if err != nil {
		return bedrockRequest{}, err
	}
	out.Messages = messages
	if len(req.Tools) > 0 {
		tools, err := toBedrockTools(req.Tools)
		if err != nil {
			return bedrockRequest{}, err
		}
		out.ToolConfig = &bedrockToolConfig{Tools: tools}
		switch {
		case req.ToolChoice.ToolName != "":
			out.ToolConfig.ToolChoice = &bedrockToolChoice{Tool: &bedrockSpecificTool{Name: req.ToolChoice.ToolName}}
		case req.ToolChoice.Type == "any":
			out.ToolConfig.ToolChoice = &bedrockToolChoice{Any: &struct{}{}}
		case req.ToolChoice.Type == "none":
			// Converse has no disable value; omitting the tools is the
			// closest protocol-level equivalent.
			out.ToolConfig = nil
		}
	}
	return out, nil
}

func toBedrockMessages(messages []Message) ([]bedrockMessage, error) {
	out := make([]bedrockMessage, 0, len(messages))
	appendBlocks := func(role string, blocks []bedrockContent) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, blocks...)
			return
		}
		out = append(out, bedrockMessage{Role: role, Content: blocks})
	}
	for _, msg := range messages {
		switch m := msg.(type) {
		case UserMessage:
			blocks, err := bedrockUserBlocks(m.Content)
			if err != nil {
				return nil, err
			}
			appendBlocks("user", blocks)
		case AssistantMessage:
			appendBlocks("assistant", bedrockAssistantBlocks(m))
		case ToolResultMessage:
			text := m.Text()
			block := bedrockContent{ToolResult: &bedrockToolResult{
				ToolUseID: m.ToolCallID,
				Content:   []bedrockToolResultContent{{Text: &text}},
			}}
			if m.IsError {
				block.ToolResult.Status = "error"
			}
			appendBlocks("user", []bedrockContent{block})
		case RuntimeStateMessage:
			if m.Text != "" {
				text := m.Text
				appendBlocks("user", []bedrockContent{{Text: &text}})
			}
		default:
			return nil, fmt.Errorf("ai: unsupported message type %T", msg)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ai: conversation has no sendable content")
	}
	return out, nil
}

func bedrockUserBlocks(content []ContentBlock) ([]bedrockContent, error) {
	blocks := make([]bedrockContent, 0, len(content))
	for _, block := range content {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				text := b.Text
				blocks = append(blocks, bedrockContent{Text: &text})
			}
		case ImageContent:
			format, ok := bedrockImageFormat(b.MediaType)
			if !ok {
				return nil, fmt.Errorf("ai: bedrock does not accept image media type %q", b.MediaType)
			}
			blocks = append(blocks, bedrockContent{Image: &bedrockImage{
				Format: format,
				Source: bedrockImageSource{Bytes: b.base64()},
			}})
		default:
			return nil, fmt.Errorf("ai: unsupported user content block %T", block)
		}
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("ai: user message has no sendable content")
	}
	return blocks, nil
}

func bedrockImageFormat(mediaType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return "png", true
	case "image/jpeg", "image/jpg":
		return "jpeg", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	}
	return "", false
}

func bedrockAssistantBlocks(msg AssistantMessage) []bedrockContent {
	blocks := make([]bedrockContent, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				text := b.Text
				blocks = append(blocks, bedrockContent{Text: &text})
			}
		case ThinkingContent:
			// Converse requires the provider signature to replay reasoning.
			// Without one the block is not sendable; dropping it preserves
			// the text and tool calls instead of failing the next turn.
			if b.Thinking != "" && b.Signature != "" {
				blocks = append(blocks, bedrockContent{ReasoningContent: &bedrockReasoningContent{
					ReasoningText: &bedrockReasoningText{Text: b.Thinking, Signature: b.Signature},
				}})
			}
		case ToolCall:
			input := b.Arguments
			if input == nil {
				input = map[string]any{}
			}
			blocks = append(blocks, bedrockContent{ToolUse: &bedrockToolUse{
				ToolUseID: b.ID,
				Name:      b.Name,
				Input:     input,
			}})
		}
	}
	return blocks
}

func toBedrockTools(specs []ToolSpec) ([]bedrockTool, error) {
	out := make([]bedrockTool, 0, len(specs))
	for _, spec := range specs {
		schema := map[string]any{"type": "object"}
		if len(spec.Parameters) > 0 {
			if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("ai: bedrock tool %q has an invalid schema: %w", spec.Name, err)
			}
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
		}
		out = append(out, bedrockTool{ToolSpec: &bedrockToolSpec{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: bedrockToolInputSchema{JSON: schema},
		}})
	}
	return out, nil
}

// bedrockEvent is one decoded event-stream message.
type bedrockEvent struct {
	Type    string
	Payload []byte
}

// readBedrockEventStream decodes the AWS application/vnd.amazon.eventstream
// framing. Each message is length-prefixed, CRC-checked at the prelude and the
// message tail, and annotated by string headers.
func readBedrockEventStream(ctx context.Context, body io.Reader, fn func(bedrockEvent) error) error {
	const maxPayload = 24 * 1024 * 1024
	prelude := make([]byte, 12)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := io.ReadFull(body, prelude)
		if err == io.EOF || (err == io.ErrUnexpectedEOF && n == 0) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("ai: read bedrock event prelude: %w", err)
		}
		total := binary.BigEndian.Uint32(prelude[0:4])
		headersLen := binary.BigEndian.Uint32(prelude[4:8])
		if total < 16 || total > maxPayload {
			return fmt.Errorf("ai: bedrock event length %d is outside the framing bounds", total)
		}
		if headersLen > total-16 {
			return fmt.Errorf("ai: bedrock event headers length %d exceeds message length %d", headersLen, total)
		}
		if got, want := crc32.ChecksumIEEE(prelude[:8]), binary.BigEndian.Uint32(prelude[8:12]); got != want {
			return fmt.Errorf("ai: bedrock event prelude checksum mismatch")
		}
		rest := make([]byte, total-12)
		if _, err := io.ReadFull(body, rest); err != nil {
			return fmt.Errorf("ai: read bedrock event body: %w", err)
		}
		hasher := crc32.NewIEEE()
		_, _ = hasher.Write(prelude)
		_, _ = hasher.Write(rest[:len(rest)-4])
		if got, want := hasher.Sum32(), binary.BigEndian.Uint32(rest[len(rest)-4:]); got != want {
			return fmt.Errorf("ai: bedrock event message checksum mismatch")
		}
		headers, err := parseBedrockHeaders(rest[:headersLen])
		if err != nil {
			return err
		}
		payload := rest[headersLen : len(rest)-4]
		eventType := headers[":event-type"]
		if eventType == "" {
			// Modeled errors carry :exception-type instead of :event-type.
			eventType = headers[":exception-type"]
		}
		if err := fn(bedrockEvent{Type: eventType, Payload: payload}); err != nil {
			return err
		}
	}
}

func parseBedrockHeaders(raw []byte) (map[string]string, error) {
	headers := map[string]string{}
	for len(raw) > 0 {
		nameLen := int(raw[0])
		raw = raw[1:]
		if nameLen == 0 || nameLen > len(raw) {
			return nil, fmt.Errorf("ai: bedrock event header name is truncated")
		}
		name := string(raw[:nameLen])
		raw = raw[nameLen:]
		if len(raw) < 1 {
			return nil, fmt.Errorf("ai: bedrock event header %q has no type", name)
		}
		kind := raw[0]
		raw = raw[1:]
		switch kind {
		case 0, 1: // true / false
			headers[name] = fmt.Sprint(kind == 0)
		case 2: // byte
			if len(raw) < 1 {
				return nil, fmt.Errorf("ai: bedrock event header %q is truncated", name)
			}
			raw = raw[1:]
		case 3: // short
			if len(raw) < 2 {
				return nil, fmt.Errorf("ai: bedrock event header %q is truncated", name)
			}
			raw = raw[2:]
		case 4: // integer
			if len(raw) < 4 {
				return nil, fmt.Errorf("ai: bedrock event header %q is truncated", name)
			}
			raw = raw[4:]
		case 5, 8: // long / timestamp
			if len(raw) < 8 {
				return nil, fmt.Errorf("ai: bedrock event header %q is truncated", name)
			}
			raw = raw[8:]
		case 6, 7: // byte array / string
			if len(raw) < 2 {
				return nil, fmt.Errorf("ai: bedrock event header %q is truncated", name)
			}
			n := int(binary.BigEndian.Uint16(raw[:2]))
			raw = raw[2:]
			if n > len(raw) {
				return nil, fmt.Errorf("ai: bedrock event header %q is truncated", name)
			}
			if kind == 7 {
				if !utf8.Valid(raw[:n]) {
					return nil, fmt.Errorf("ai: bedrock event header %q is not UTF-8", name)
				}
				headers[name] = string(raw[:n])
			}
			raw = raw[n:]
		case 9: // uuid
			if len(raw) < 16 {
				return nil, fmt.Errorf("ai: bedrock event header %q is truncated", name)
			}
			raw = raw[16:]
		default:
			return nil, fmt.Errorf("ai: bedrock event header %q has unknown type %d", name, kind)
		}
	}
	return headers, nil
}

type bedrockBlockState struct {
	index int
	kind  string
}

type bedrockAccumulator struct {
	msg     *AssistantMessage
	blocks  map[int]*bedrockBlockState
	pending []StreamEvent
}

func newBedrockAccumulator(msg *AssistantMessage) *bedrockAccumulator {
	return &bedrockAccumulator{msg: msg, blocks: map[int]*bedrockBlockState{}}
}

func (a *bedrockAccumulator) apply(event bedrockEvent) error {
	switch event.Type {
	case "messageStart":
		return nil
	case "contentBlockStart":
		return a.applyBlockStart(event.Payload)
	case "contentBlockDelta":
		return a.applyBlockDelta(event.Payload)
	case "contentBlockStop":
		return a.applyBlockStop(event.Payload)
	case "messageStop":
		var body struct {
			StopReason string `json:"stopReason"`
		}
		if err := json.Unmarshal(event.Payload, &body); err != nil {
			return fmt.Errorf("ai: decode bedrock messageStop: %w", err)
		}
		a.msg.StopReason = mapBedrockStopReason(body.StopReason)
		return nil
	case "metadata":
		var body struct {
			Usage struct {
				InputTokens           int `json:"inputTokens"`
				OutputTokens          int `json:"outputTokens"`
				TotalTokens           int `json:"totalTokens"`
				CacheReadInputTokens  int `json:"cacheReadInputTokens"`
				CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(event.Payload, &body); err != nil {
			return fmt.Errorf("ai: decode bedrock metadata: %w", err)
		}
		a.msg.Usage = Usage{
			Input:      body.Usage.InputTokens,
			Output:     body.Usage.OutputTokens,
			Total:      body.Usage.TotalTokens,
			CacheRead:  body.Usage.CacheReadInputTokens,
			CacheWrite: body.Usage.CacheWriteInputTokens,
		}
		return nil
	case "validationException", "throttlingException", "serviceUnavailableException",
		"internalServerException", "modelStreamErrorException":
		return fmt.Errorf("ai: bedrock %s: %s", event.Type, bedrockErrorMessage(event.Payload))
	default:
		// Unknown event types are ignored for forward compatibility: the
		// protocol adds content kinds without changing the framing.
		return nil
	}
}

func (a *bedrockAccumulator) applyBlockStart(raw []byte) error {
	var body struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
		Start             struct {
			ToolUse *struct {
				ToolUseID string `json:"toolUseId"`
				Name      string `json:"name"`
			} `json:"toolUse"`
		} `json:"start"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("ai: decode bedrock contentBlockStart: %w", err)
	}
	if body.Start.ToolUse == nil {
		a.blocks[body.ContentBlockIndex] = &bedrockBlockState{index: body.ContentBlockIndex, kind: "opaque"}
		return nil
	}
	call := ToolCall{
		ID:     body.Start.ToolUse.ToolUseID,
		Name:   body.Start.ToolUse.Name,
		Origin: ToolCallNative,
	}
	state := &bedrockBlockState{index: len(a.msg.Content), kind: "tool"}
	a.blocks[body.ContentBlockIndex] = state
	a.msg.Content = append(a.msg.Content, call)
	a.pending = append(a.pending, EventToolCallStart{ID: call.ID, Name: call.Name})
	return nil
}

func (a *bedrockAccumulator) applyBlockDelta(raw []byte) error {
	var body struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
		Delta             struct {
			Text    string `json:"text"`
			ToolUse *struct {
				Input string `json:"input"`
			} `json:"toolUse"`
			ReasoningContent *struct {
				Text      string `json:"text"`
				Signature string `json:"signature"`
			} `json:"reasoningContent"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("ai: decode bedrock contentBlockDelta: %w", err)
	}
	state, ok := a.blocks[body.ContentBlockIndex]
	if !ok {
		// A text block has no start event, so the first delta defines it.
		state = &bedrockBlockState{index: len(a.msg.Content), kind: "text"}
		a.blocks[body.ContentBlockIndex] = state
	}
	switch {
	case body.Delta.Text != "":
		if state.kind != "text" {
			a.msg.Content = append(a.msg.Content, TextContent{})
			state.index = len(a.msg.Content) - 1
			state.kind = "text"
		}
		a.appendText(state, body.Delta.Text)
		a.pending = append(a.pending, EventTextDelta{Delta: body.Delta.Text})
	case body.Delta.ToolUse != nil && body.Delta.ToolUse.Input != "":
		current, ok := a.msg.Content[state.index].(ToolCall)
		if !ok {
			return fmt.Errorf("ai: bedrock tool delta for a non-tool block %d", body.ContentBlockIndex)
		}
		current.appendArgs(body.Delta.ToolUse.Input)
		a.msg.Content[state.index] = current
		a.pending = append(a.pending, EventToolCallDelta{ID: current.ID, Delta: body.Delta.ToolUse.Input})
	case body.Delta.ReasoningContent != nil:
		if state.kind != "thinking" {
			a.msg.Content = append(a.msg.Content, ThinkingContent{})
			state.index = len(a.msg.Content) - 1
			state.kind = "thinking"
		}
		current, _ := a.msg.Content[state.index].(ThinkingContent)
		current.Thinking += body.Delta.ReasoningContent.Text
		current.Signature += body.Delta.ReasoningContent.Signature
		a.msg.Content[state.index] = current
		if body.Delta.ReasoningContent.Text != "" {
			a.pending = append(a.pending, EventThinkingDelta{Delta: body.Delta.ReasoningContent.Text})
		}
	}
	return nil
}

func (a *bedrockAccumulator) appendText(state *bedrockBlockState, delta string) {
	if state.index >= len(a.msg.Content) {
		a.msg.Content = append(a.msg.Content, TextContent{})
		state.index = len(a.msg.Content) - 1
		state.kind = "text"
	}
	current, _ := a.msg.Content[state.index].(TextContent)
	current.Text += delta
	a.msg.Content[state.index] = current
}

func (a *bedrockAccumulator) applyBlockStop(raw []byte) error {
	var body struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("ai: decode bedrock contentBlockStop: %w", err)
	}
	state, ok := a.blocks[body.ContentBlockIndex]
	if !ok || state.kind != "tool" {
		return nil
	}
	current, ok := a.msg.Content[state.index].(ToolCall)
	if !ok {
		return fmt.Errorf("ai: bedrock tool stop for a non-tool block %d", body.ContentBlockIndex)
	}
	args, err := decodeJSONArgs(current.RawArguments())
	if err != nil {
		return classifyToolArgumentError(current.Name, a.msg.StopReason, err)
	}
	current.Arguments = args
	a.msg.Content[state.index] = current
	a.pending = append(a.pending, EventToolCallEnd{ID: current.ID, Name: current.Name, Arguments: args})
	delete(a.blocks, body.ContentBlockIndex)
	return nil
}

func (a *bedrockAccumulator) finalize() error {
	for index, state := range a.blocks {
		if state.kind == "tool" {
			name := ""
			if current, ok := a.msg.Content[state.index].(ToolCall); ok {
				name = current.Name
			}
			return &ToolCallArgumentError{
				Tool:       name,
				Incomplete: true,
				Err:        fmt.Errorf("bedrock stream ended before the tool call at block %d closed", index),
			}
		}
	}
	return nil
}

func bedrockErrorMessage(payload []byte) string {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || strings.TrimSpace(body.Message) == "" {
		return strings.TrimSpace(string(payload))
	}
	return body.Message
}

func mapBedrockStopReason(reason string) StopReason {
	switch reason {
	case "end_turn", "stop_sequence":
		return StopEndTurn
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	case "guardrail_intervened", "content_filtered":
		return StopRefusal
	}
	return StopEndTurn
}

// BedrockRegion reports the region the endpoint should be built for.
//
// The explicit endpoint wins; otherwise AWS_REGION and AWS_DEFAULT_REGION are
// read in that order. An empty result means the default us-east-1 root.
func BedrockRegion(baseURL string) string {
	if region := bedrockRegionFromURL(baseURL); region != "" {
		return region
	}
	for _, name := range BedrockRegionEnv {
		if region := strings.TrimSpace(os.Getenv(name)); region != "" {
			return region
		}
	}
	return ""
}

func bedrockRegionFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := parsed.Hostname()
	const prefix = "bedrock-runtime."
	const suffix = ".amazonaws.com"
	if strings.HasPrefix(host, prefix) && strings.HasSuffix(host, suffix) {
		region := strings.TrimSuffix(strings.TrimPrefix(host, prefix), suffix)
		if !strings.Contains(region, ".") {
			return region
		}
	}
	return ""
}
