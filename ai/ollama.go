package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIOllamaChat is the native Ollama chat protocol: POST {base}/api/chat with
// newline-delimited JSON responses. Both local Ollama and Ollama Cloud speak
// it; they differ in base URL and authentication, not in wire shape.
const APIOllamaChat API = "ollama-chat"

// OllamaBaseURL is the default local server. Cloud uses https://ollama.com.
const OllamaBaseURL = "http://127.0.0.1:11434"

// OllamaProvider streams completions through the native chat protocol.
type OllamaProvider struct {
	// Client is the HTTP client used for every request.
	Client *http.Client
	// APIKey is sent as Authorization: Bearer when non-empty. Local servers
	// need none, so an empty key sends no auth header at all rather than an
	// empty bearer that a strict proxy could reject.
	APIKey string
	// BaseURL overrides OllamaBaseURL when non-empty. It is the native root
	// (no /v1 suffix): chat lives at {base}/api/chat.
	BaseURL string
}

// NewOllamaProvider returns a provider for the given credential. Pass an
// empty key for a local server.
func NewOllamaProvider(apiKey string) *OllamaProvider {
	return &OllamaProvider{Client: &http.Client{}, APIKey: apiKey}
}

// ollamaMessage is one chat turn.
type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaFunctionTool is one offered tool.
type ollamaFunctionTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters,omitempty"`
	} `json:"function"`
}

// ollamaRequest is the /api/chat body.
type ollamaRequest struct {
	Model    string               `json:"model"`
	Messages []ollamaMessage      `json:"messages"`
	Tools    []ollamaFunctionTool `json:"tools,omitempty"`
	Options  map[string]any       `json:"options,omitempty"`
	Stream   bool                 `json:"stream"`
}

// ollamaWireToolCall is one streamed tool call.
type ollamaWireToolCall struct {
	Function *struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function"`
}

// ollamaChunk is one NDJSON line.
type ollamaChunk struct {
	Message *struct {
		Role      string               `json:"role"`
		Content   string               `json:"content"`
		Thinking  string               `json:"thinking,omitempty"`
		ToolCalls []ollamaWireToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason,omitempty"`
	PromptEvalCount int    `json:"prompt_eval_count,omitempty"`
	EvalCount       int    `json:"eval_count,omitempty"`
	Error           string `json:"error,omitempty"`
}

// toOllamaMessages converts the conversation. Assistant tool calls replay as
// plain text placeholders: the native protocol has no assistant-tool message
// shape, and dropping the calls would orphan their results.
func toOllamaMessages(messages []Message, system []SystemPromptBlock) ([]ollamaMessage, error) {
	out := make([]ollamaMessage, 0, len(messages)+len(system))
	for _, block := range system {
		if block.Text != "" {
			out = append(out, ollamaMessage{Role: "system", Content: block.Text})
		}
	}
	for _, msg := range messages {
		switch m := msg.(type) {
		case UserMessage:
			text, err := joinTextBlocks(m.Content)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("ai: user message has no sendable content")
			}
			out = append(out, ollamaMessage{Role: "user", Content: text})
		case AssistantMessage:
			var parts []string
			for _, block := range m.Content {
				switch b := block.(type) {
				case TextContent:
					if b.Text != "" {
						parts = append(parts, b.Text)
					}
				case ToolCall:
					parts = append(parts, fmt.Sprintf("[called %s]", b.Name))
				}
			}
			out = append(out, ollamaMessage{Role: "assistant", Content: strings.Join(parts, "\n")})
		case ToolResultMessage:
			out = append(out, ollamaMessage{Role: "tool", Content: m.Text()})
		default:
			return nil, fmt.Errorf("ai: unsupported message type %T", msg)
		}
	}
	// A request with no user turn makes some local models load and emit
	// nothing, which the server reports as done_reason "load" and this
	// transport surfaces as an error. The static system prefix stays on
	// system: demoting it would break prefix caching for every request just
	// to rescue a malformed one.
	return out, nil
}

// joinTextBlocks joins text blocks, refusing images: the native protocol
// takes image bytes in a separate field this transport does not implement,
// and silently dropping a screenshot the model was asked about would be a
// silent wrong answer.
func joinTextBlocks(content []ContentBlock) (string, error) {
	var parts []string
	for _, block := range content {
		switch b := block.(type) {
		case TextContent:
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		default:
			return "", fmt.Errorf("ai: unsupported user content block %T for ollama-chat", block)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// toOllamaTools converts tool specs. Schemas travel as decoded maps because
// the wire shape nests parameters as an object, not a string.
func toOllamaTools(specs []ToolSpec) ([]ollamaFunctionTool, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]ollamaFunctionTool, 0, len(specs))
	for _, spec := range specs {
		tool := ollamaFunctionTool{Type: "function"}
		tool.Function.Name = spec.Name
		tool.Function.Description = spec.Description
		if len(spec.Parameters) > 0 {
			var params map[string]any
			if err := json.Unmarshal(spec.Parameters, &params); err != nil {
				return nil, fmt.Errorf("ai: tool %q has invalid parameters: %w", spec.Name, err)
			}
			tool.Function.Parameters = params
		}
		out = append(out, tool)
	}
	return out, nil
}

// ollamaNativeBase strips a trailing /v1: chat lives under the native root,
// while catalog records carry the OpenAI-compatible base for discovery.
func ollamaNativeBase(base string) string {
	return strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
}

// Stream implements Provider.
func (p *OllamaProvider) Stream(ctx context.Context, req Request) (*Stream, error) {
	if p.Client == nil {
		p.Client = &http.Client{}
	}
	base := ollamaNativeBase(p.BaseURL)
	if base == "" {
		base = OllamaBaseURL
	}

	messages, err := toOllamaMessages(req.Messages, req.System)
	if err != nil {
		return nil, err
	}
	tools, err := toOllamaTools(req.Tools)
	if err != nil {
		return nil, err
	}
	body := ollamaRequest{Model: req.Model.ID, Messages: messages, Tools: tools, Stream: true}
	if req.MaxTokens > 0 {
		body.Options = map[string]any{"num_predict": req.MaxTokens}
	}
	if req.Temperature != nil {
		if body.Options == nil {
			body.Options = map[string]any{}
		}
		body.Options["temperature"] = *req.Temperature
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ai: encode ollama request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/chat", strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("ai: build ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := p.Client.Do(httpReq)
	if err != nil {
		return nil, ollamaConnError(base, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, readOllamaError(resp, base)
	}

	stream := newStream(64)
	go func() {
		defer resp.Body.Close()
		final := AssistantMessage{Model: req.Model}
		acc := newOllamaAccumulator(&final)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		streamErr := error(nil)
		stream.publish(ctx, EventStart{Message: final})
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var chunk ollamaChunk
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				streamErr = fmt.Errorf("ai: decode ollama chunk: %w", err)
				break
			}
			if chunk.Error != "" {
				streamErr = fmt.Errorf("ai: ollama error: %s", chunk.Error)
				break
			}
			for _, pending := range acc.apply(ctx, chunk) {
				if !stream.publish(ctx, pending) {
					streamErr = ctx.Err()
					break
				}
			}
			if streamErr != nil {
				break
			}
			if chunk.Done {
				break
			}
		}
		if streamErr == nil {
			streamErr = scanner.Err()
		}
		if streamErr == nil {
			for _, pending := range acc.finish() {
				if !stream.publish(ctx, pending) {
					streamErr = ctx.Err()
					break
				}
			}
		}
		if streamErr == nil && final.StopReason == StopError {
			// done_reason "load": the model loaded but generated nothing.
			// A clean empty stop would be laundered and retried behind a
			// misleading hint; an error names the actual condition.
			streamErr = errors.New("ai: Ollama loaded the model but generated nothing (done_reason: load): the request contained no user-role message")
		}
		if streamErr != nil {
			final.StopReason = StopError
			final.ErrorMessage = streamErr.Error()
			stream.finish(final, streamErr)
			return
		}
		stream.finish(final, nil)
	}()
	return stream, nil
}

// ollamaConnError classifies a dial failure. A refused localhost connection
// is the common case and must say what to do, not just what failed.
func ollamaConnError(base string, err error) error {
	if strings.Contains(base, "127.0.0.1") || strings.Contains(base, "localhost") {
		return fmt.Errorf("ai: cannot reach Ollama at %s: is `ollama serve` running? (%v)", base, err)
	}
	return fmt.Errorf("ai: cannot reach %s: %w", base, err)
}

// readOllamaError decodes a non-2xx response, classifying auth failures so a
// rejected key reads as rejected rather than generic.
func readOllamaError(resp *http.Response, base string) error {
	defer resp.Body.Close()
	var payload struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	detail := strings.TrimSpace(payload.Error)
	if detail == "" {
		detail = resp.Status
	}
	switch resp.StatusCode {
	case 401, 403:
		return fmt.Errorf("ai: %s rejected the credential (HTTP %d): %s", base, resp.StatusCode, detail)
	case 404:
		return fmt.Errorf("ai: %s has no /api/chat endpoint (HTTP 404): %s", base, detail)
	case 429:
		return fmt.Errorf("ai: %s rate-limited the request (HTTP 429): %s", base, detail)
	default:
		return fmt.Errorf("ai: %s returned HTTP %d: %s", base, resp.StatusCode, detail)
	}
}

// ollamaAccumulator reassembles one response.
type ollamaAccumulator struct {
	msg       *AssistantMessage
	textOpen  bool
	calls     []ollamaCall
	usageIn   int
	usageOut  int
	idCounter int
}

// ollamaCall is one reassembled tool call.
type ollamaCall struct {
	id        string
	name      string
	arguments map[string]any
}

func newOllamaAccumulator(msg *AssistantMessage) *ollamaAccumulator {
	return &ollamaAccumulator{msg: msg}
}

// apply folds one chunk into events.
func (a *ollamaAccumulator) apply(ctx context.Context, chunk ollamaChunk) []StreamEvent {
	_ = ctx
	var out []StreamEvent
	if chunk.Message != nil {
		if chunk.Message.Thinking != "" {
			a.closeText()
			a.appendThinking(chunk.Message.Thinking)
			out = append(out, EventThinkingDelta{Delta: chunk.Message.Thinking})
		}
		if chunk.Message.Content != "" {
			if !a.textOpen {
				a.textOpen = true
			}
			a.appendText(chunk.Message.Content)
			out = append(out, EventTextDelta{Delta: chunk.Message.Content})
		}
		for _, wire := range chunk.Message.ToolCalls {
			a.closeText()
			name := "unknown_tool"
			rawArgs := "{}"
			if wire.Function != nil {
				if wire.Function.Name != "" {
					name = wire.Function.Name
				}
				switch args := wire.Function.Arguments.(type) {
				case string:
					if args != "" {
						rawArgs = args
					}
				default:
					if encoded, err := json.Marshal(wire.Function.Arguments); err == nil {
						rawArgs = string(encoded)
					}
				}
			}
			a.idCounter++
			id := fmt.Sprintf("ollama:%d:%s", a.idCounter, name)
			decoded := map[string]any{}
			_ = json.Unmarshal([]byte(rawArgs), &decoded)
			out = append(out, EventToolCallStart{ID: id, Name: name})
			out = append(out, EventToolCallDelta{ID: id, Delta: rawArgs})
			a.calls = append(a.calls, ollamaCall{id: id, name: name, arguments: decoded})
		}
	}
	if chunk.PromptEvalCount > 0 {
		a.usageIn = chunk.PromptEvalCount
	}
	if chunk.EvalCount > 0 {
		a.usageOut = chunk.EvalCount
	}
	if chunk.Done {
		a.closeText()
		a.msg.StopReason = mapOllamaStopReason(chunk.DoneReason, len(a.calls) > 0)
		a.msg.Usage = Usage{Input: a.usageIn, Output: a.usageOut, Total: a.usageIn + a.usageOut}
	}
	return out
}

// finish closes reassembled calls after the final chunk.
func (a *ollamaAccumulator) finish() []StreamEvent {
	var out []StreamEvent
	for _, call := range a.calls {
		a.msg.Content = append(a.msg.Content, ToolCall{ID: call.id, Name: call.name, Arguments: call.arguments})
		out = append(out, EventToolCallEnd{ID: call.id, Name: call.name, Arguments: call.arguments})
	}
	out = append(out, EventDone{Message: *a.msg})
	return out
}

func (a *ollamaAccumulator) closeText() {
	a.textOpen = false
}

// appendText extends the trailing text block. Chunks fragment mid-word, and
// the joiner puts newlines between blocks, so each fragment must extend
// rather than append.
func (a *ollamaAccumulator) appendText(text string) {
	for i := len(a.msg.Content) - 1; i >= 0; i-- {
		if t, ok := a.msg.Content[i].(TextContent); ok {
			t.Text += text
			a.msg.Content[i] = t
			return
		}
	}
	a.msg.Content = append(a.msg.Content, TextContent{Text: text})
}

// appendThinking extends the trailing thinking block for the same reason.
func (a *ollamaAccumulator) appendThinking(text string) {
	for i := len(a.msg.Content) - 1; i >= 0; i-- {
		if t, ok := a.msg.Content[i].(ThinkingContent); ok {
			t.Thinking += text
			a.msg.Content[i] = t
			return
		}
	}
	a.msg.Content = append(a.msg.Content, ThinkingContent{Thinking: text})
}

// mapOllamaStopReason translates done_reason. "load" means the model loaded
// but generated nothing (no user turn): surfacing it as an error keeps it
// from being laundered and retried behind a misleading hint.
func mapOllamaStopReason(reason string, hasCalls bool) StopReason {
	switch reason {
	case "length":
		return StopMaxTokens
	case "tool_calls":
		return StopToolUse
	case "load":
		return StopError
	case "":
		if hasCalls {
			return StopToolUse
		}
		return StopEndTurn
	default:
		return StopEndTurn
	}
}
