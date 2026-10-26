package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/knowledge"
)

// contentItem is one MCP content entry. Binary payloads are represented only by
// a bounded marker; the base64 data never enters model context.
type contentItem struct {
	Type     string        `json:"type"`
	Text     string        `json:"text"`
	Data     string        `json:"data"`
	MIMEType string        `json:"mimeType"`
	Resource *wireResource `json:"resource"`
}

// parseToolResult bounds one tools/call result.
func parseToolResult(raw json.RawMessage, bytesRead int, cfg ServerConfig) (ToolResult, error) {
	if len(raw) == 0 {
		return ToolResult{}, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp tools/call returned no result")
	}
	var envelope struct {
		Content           []contentItem   `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ToolResult{}, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp tools/call result is malformed")
	}

	result := ToolResult{Bytes: bytesRead, IsError: envelope.IsError}
	items := envelope.Content
	if len(items) > cfg.MaxItems {
		items = items[:cfg.MaxItems]
		result.Truncated = true
		result.Reason = "item_limit"
	}
	result.Items = len(items)

	var body strings.Builder
	remaining := cfg.MaxTextChars
	appendText := func(text string) {
		if remaining <= 0 {
			result.Truncated = true
			if result.Reason == "" {
				result.Reason = "text_limit"
			}
			return
		}
		bounded, cut := knowledge.BoundText(text, remaining)
		if cut {
			result.Truncated = true
			if result.Reason == "" {
				result.Reason = "text_limit"
			}
		}
		body.WriteString(bounded)
		remaining -= len([]rune(bounded))
	}

	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "text":
			appendText(item.Text)
			body.WriteString("\n")
		case "resource":
			if item.Resource == nil {
				body.WriteString("[resource content omitted: missing resource object]\n")
				continue
			}
			if strings.TrimSpace(item.Resource.URI) != "" {
				uri, _ := knowledge.BoundText(cfg.redact(item.Resource.URI), 300)
				mime, _ := knowledge.BoundText(cfg.redact(item.Resource.MIMEType), 100)
				fmt.Fprintf(&body, "[resource %s", uri)
				if mime != "" {
					fmt.Fprintf(&body, " (%s)", mime)
				}
				body.WriteString("]\n")
			}
			if item.Resource.Text != "" {
				appendText(item.Resource.Text)
				body.WriteString("\n")
			} else {
				body.WriteString("[binary resource content omitted]\n")
			}
		case "image", "audio", "blob":
			MIMEType := item.MIMEType
			if MIMEType == "" {
				MIMEType = "unknown"
			}
			MIMEType, _ = knowledge.BoundText(MIMEType, 100)
			typeName, _ := knowledge.BoundText(item.Type, 40)
			// Estimate decoded size without publishing the base64 payload.
			fmt.Fprintf(&body, "[binary %s omitted: mime=%s, base64_bytes=%d]\n",
				typeName, MIMEType, len(item.Data))
		case "":
			body.WriteString("[content item without a type]\n")
		default:
			name, _ := knowledge.BoundText(item.Type, 80)
			fmt.Fprintf(&body, "[unsupported content type %q]\n", name)
		}
	}
	if strings.TrimSpace(body.String()) == "" && len(envelope.StructuredContent) > 0 {
		appendText(string(envelope.StructuredContent))
	}
	if len(envelope.StructuredContent) > 0 && strings.TrimSpace(body.String()) != "" && remaining > 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, envelope.StructuredContent); err == nil {
			body.WriteString("\nstructured_content: ")
			appendText(compact.String())
			body.WriteString("\n")
		}
	}
	if strings.TrimSpace(body.String()) == "" {
		body.WriteString("(empty result)\n")
	}
	result.Text = strings.TrimRight(body.String(), "\n")
	result.Text = cfg.redact(result.Text)
	result.Text, _ = knowledge.BoundText(result.Text, cfg.MaxTextChars)
	return result, nil
}

// parseResource bounds one resources/read result.
func parseResource(raw json.RawMessage, bytesRead int, cfg ServerConfig) (ResourceResult, error) {
	if len(raw) == 0 {
		return ResourceResult{}, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp resources/read returned no result")
	}
	var envelope struct {
		Contents []struct {
			URI      string `json:"uri"`
			MIMEType string `json:"mimeType"`
			Text     string `json:"text"`
			Blob     string `json:"blob"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ResourceResult{}, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp resources/read result is malformed")
	}
	result := ResourceResult{Bytes: bytesRead}
	items := envelope.Contents
	if len(items) > cfg.MaxItems {
		items = items[:cfg.MaxItems]
		result.Truncated = true
		result.Reason = "item_limit"
	}
	result.Items = len(items)

	var body strings.Builder
	remaining := cfg.MaxTextChars
	for _, item := range items {
		if result.URI == "" {
			result.URI = item.URI
		}
		if result.MIMEType == "" {
			result.MIMEType = item.MIMEType
		}
		if item.Text != "" {
			bounded, cut := knowledge.BoundText(item.Text, remaining)
			if cut {
				result.Truncated = true
				if result.Reason == "" {
					result.Reason = "text_limit"
				}
			}
			body.WriteString(bounded)
			remaining -= len([]rune(bounded))
		} else if item.Blob != "" {
			uri, _ := knowledge.BoundText(cfg.redact(item.URI), 300)
			mime, _ := knowledge.BoundText(cfg.redact(item.MIMEType), 100)
			fmt.Fprintf(&body, "[binary resource content omitted: uri=%s, mime=%s, base64_bytes=%d]",
				uri, mime, len(item.Blob))
			result.Truncated = true
			if result.Reason == "" {
				result.Reason = "binary_content"
			}
		}
		if remaining <= 0 {
			result.Truncated = true
			if result.Reason == "" {
				result.Reason = "text_limit"
			}
			break
		}
	}
	result.Text = strings.TrimRight(body.String(), "\n")
	result.Text = cfg.redact(result.Text)
	result.Text, _ = knowledge.BoundText(result.Text, cfg.MaxTextChars)
	result.URI = cfg.redact(result.URI)
	result.URI, _ = knowledge.BoundText(result.URI, 500)
	result.MIMEType = cfg.redact(result.MIMEType)
	result.MIMEType, _ = knowledge.BoundText(result.MIMEType, 100)
	return result, nil
}

// parsePrompt bounds one prompts/get result.
//
// Prompt messages are context, not instructions. Each message is rendered with
// its role as a label so the model and the UI can see that the text came from
// an external template; the runtime never executes it.
func parsePrompt(raw json.RawMessage, bytesRead int, cfg ServerConfig) (PromptResult, error) {
	if len(raw) == 0 {
		return PromptResult{}, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp prompts/get returned no result")
	}
	var envelope struct {
		Description string `json:"description"`
		Messages    []struct {
			Role    string      `json:"role"`
			Content contentItem `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return PromptResult{}, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp prompts/get result is malformed")
	}
	result := PromptResult{Bytes: bytesRead}
	items := envelope.Messages
	if len(items) > cfg.MaxItems {
		items = items[:cfg.MaxItems]
		result.Truncated = true
		result.Reason = "item_limit"
	}
	result.Items = len(items)
	result.Description, _ = knowledge.BoundText(strings.TrimSpace(envelope.Description), 500)
	result.Description = cfg.redact(result.Description)

	var body strings.Builder
	remaining := cfg.MaxTextChars
	appendText := func(text string) {
		if remaining <= 0 {
			result.Truncated = true
			if result.Reason == "" {
				result.Reason = "text_limit"
			}
			return
		}
		bounded, cut := knowledge.BoundText(text, remaining)
		if cut {
			result.Truncated = true
			if result.Reason == "" {
				result.Reason = "text_limit"
			}
		}
		body.WriteString(bounded)
		remaining -= len([]rune(bounded))
	}
	for _, message := range items {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "" {
			role = "unknown"
		}
		role, _ = knowledge.BoundText(role, 40)
		fmt.Fprintf(&body, "[%s]\n", role)
		switch strings.ToLower(strings.TrimSpace(message.Content.Type)) {
		case "text":
			appendText(message.Content.Text)
			body.WriteString("\n")
		case "resource":
			if message.Content.Resource != nil && message.Content.Resource.Text != "" {
				appendText(message.Content.Resource.Text)
				body.WriteString("\n")
			} else {
				body.WriteString("[binary or missing resource content omitted]\n")
			}
		case "image", "audio", "blob":
			typeName, _ := knowledge.BoundText(message.Content.Type, 40)
			fmt.Fprintf(&body, "[binary %s omitted]\n", typeName)
		case "":
			body.WriteString("[message without a content type]\n")
		default:
			name, _ := knowledge.BoundText(message.Content.Type, 80)
			fmt.Fprintf(&body, "[unsupported content type %q]\n", name)
		}
	}
	if strings.TrimSpace(body.String()) == "" {
		body.WriteString("(empty prompt)\n")
	}
	result.Text = strings.TrimRight(body.String(), "\n")
	result.Text = cfg.redact(result.Text)
	result.Text, _ = knowledge.BoundText(result.Text, cfg.MaxTextChars)
	return result, nil
}
