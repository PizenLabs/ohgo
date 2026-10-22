package mcp

import (
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
				fmt.Fprintf(&body, "[resource %s", item.Resource.URI)
				if item.Resource.MIMEType != "" {
					fmt.Fprintf(&body, " (%s)", item.Resource.MIMEType)
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
			// Estimate decoded size without publishing the base64 payload.
			fmt.Fprintf(&body, "[binary %s omitted: mime=%s, base64_bytes=%d]\n",
				item.Type, MIMEType, len(item.Data))
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
	if strings.TrimSpace(body.String()) == "" {
		body.WriteString("(empty result)\n")
	}
	result.Text = strings.TrimRight(body.String(), "\n")
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
			fmt.Fprintf(&body, "[binary resource content omitted: uri=%s, mime=%s, base64_bytes=%d]",
				item.URI, item.MIMEType, len(item.Blob))
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
	return result, nil
}
