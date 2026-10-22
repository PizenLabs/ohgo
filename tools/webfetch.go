package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/knowledge"
	"github.com/PizenLabs/ohgo/security"
)

// WebFetch limits.
const (
	// WebFetchTimeout bounds one fetch.
	WebFetchTimeout = 30 * time.Second
	// WebFetchMaxBody caps the downloaded body size, in bytes.
	WebFetchMaxBody = 5 * 1024 * 1024
	// WebFetchMaxChars caps the extracted text length.
	WebFetchMaxChars = 50_000
)

// WebFetchTool fetches a URL and extracts readable text.
type WebFetchTool struct {
	Base
	// Client is the HTTP client used for every fetch.
	Client *http.Client
}

// NewWebFetchTool returns a fetch tool with a default client.
func NewWebFetchTool() *WebFetchTool {
	tool := &WebFetchTool{Client: &http.Client{}}
	tool.Base = NewBase("web_fetch",
		"Fetch a URL and return its text content. HTML is reduced to readable "+
			"prose: scripts, styles, and markup are removed.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The http or https URL to fetch.",
				},
			},
			"required": []string{"url"},
		},
		agent.Approval{Tier: agent.TierRead, Reason: "sends a request to a remote host"},
	)
	return tool
}

// SecurityCapability implements agent.CapabilityNamer.
//
// Its declared tier is read, but a network fetch is not a filesystem read: the
// security boundary must see egress as its own capability so policy can treat
// it as such.
func (t *WebFetchTool) SecurityCapability() security.Capability {
	return security.CapabilityNetwork
}

// Execute implements agent.Tool.
func (t *WebFetchTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}

	rawURL, err := strArg(args, "url", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if strings.TrimSpace(rawURL) == "" {
		return agent.ErrorResult("url must not be empty")
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return agent.ErrorResult("invalid url %q: %v", rawURL, err)
	}
	// Only network-fetchable schemes are allowed: file:// and friends would
	// turn a read tool into a local-file disclosure vector.
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return agent.ErrorResult("only http and https URLs can be fetched, got scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return agent.ErrorResult("url %q has no host", rawURL)
	}

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: WebFetchTimeout}
		t.Client = client
	}
	// The request context is honoured separately from the client timeout so a
	// run-level abort kills an in-flight fetch.
	fetchCtx, cancel := context.WithTimeout(ctx.Ctx, WebFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return agent.ErrorResult("build request: %v", err)
	}
	req.Header.Set("User-Agent", "ohgo/0.1 (+https://pizenlabs.dev/ohgo-web/)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,*/*")

	resp, err := client.Do(req)
	if err != nil {
		return agent.ErrorResult("fetch %q: %v", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return agent.ErrorResult("fetch returned HTTP %d for %s", resp.StatusCode, rawURL)
	}

	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, WebFetchMaxBody))
	if readErr != nil {
		// A body larger than the cap still yields a usable prefix.
		if len(body) == 0 {
			return agent.ErrorResult("read response: %v", readErr)
		}
	}

	var text string
	switch {
	case strings.Contains(ctype, "html"), strings.Contains(ctype, "xhtml"):
		text = htmlToText(string(body))
	case strings.Contains(ctype, "json"):
		text = string(body)
	default:
		text = string(body)
	}
	if text == "" {
		return agent.Result{
			Text: knowledge.Envelope(
				"capability: web.fetch\nurl: "+singleLine(rawURL)+"\nstatus: empty",
				"(no readable text)"),
			Details: map[string]any{
				"url":         rawURL,
				"contentType": ctype,
				"status":      "empty",
				"trust":       string(knowledge.TrustUntrusted),
			},
			Useless: true,
		}
	}

	truncated := false
	text, truncated = knowledge.BoundText(text, WebFetchMaxChars)
	if truncated {
		text += fmt.Sprintf("\n\n[text truncated at %d characters]", WebFetchMaxChars)
	}
	retrievedAt := time.Now().UTC()
	parsedURL, _ := url.Parse(rawURL)
	source := ""
	if parsedURL != nil {
		source = parsedURL.Hostname()
	}
	evidence := knowledge.Evidence{
		Kind:  knowledge.EvidenceWebFetch,
		Trust: knowledge.TrustUntrusted,
		Provenance: knowledge.Provenance{
			Capability:  knowledge.CapabilityWebFetch,
			Source:      source,
			Locator:     rawURL,
			Provider:    "web_fetch",
			RetrievedAt: retrievedAt,
			RequestID:   knowledge.Hash(rawURL),
		},
		Bound: knowledge.Bound{
			Bytes: len(body), Chars: len([]rune(text)),
			Items: 1, Truncated: truncated, Reason: truncationReason(truncated),
		},
		ContentHash: knowledge.Hash(text),
	}.Normalize()
	header := fmt.Sprintf("capability: web.fetch\nurl: %s\ncontent_type: %s\nretrieved_at: %s\n",
		singleLine(rawURL), singleLine(ctype), retrievedAt.Format(time.RFC3339))
	if truncated {
		header += "truncated: true (char_limit)\n"
	}
	return agent.Result{
		Text: knowledge.Envelope(header, text),
		Details: map[string]any{
			"url":          rawURL,
			"contentType":  ctype,
			"bytes":        len(body),
			"truncated":    truncated,
			"trust":        string(knowledge.TrustUntrusted),
			"retrieved_at": retrievedAt,
			"evidence":     []knowledge.Evidence{evidence},
		},
	}
}

// truncationReason names the content bound for the legacy fetch.
func truncationReason(truncated bool) string {
	if truncated {
		return "char_limit"
	}
	return ""
}

// htmlToText strips markup and reduces an HTML document to readable prose.
//
// It is a token-level scanner rather than a DOM parser: the goal is to remove
// the noise that crowds out content, not to build a tree. Script, style, and
// head content is dropped entirely, and block elements become line breaks.
func htmlToText(html string) string {
	var (
		out  strings.Builder
		drop bool // true while inside script/style/head
	)

	runes := []byte(html)
	for i := 0; i < len(runes); i++ {
		c := runes[i]

		if c == '<' {
			// A tag ends at the next '>' outside a quoted attribute value.
			tagStart := i
			j := i + 1
			for j < len(runes) && runes[j] != '>' {
				if runes[j] == '"' || runes[j] == '\'' {
					quote := runes[j]
					j++
					for j < len(runes) && runes[j] != quote {
						j++
					}
				}
				j++
			}
			if j >= len(runes) {
				break
			}

			raw := string(runes[tagStart+1 : j])
			closing := strings.HasPrefix(raw, "/")
			name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(raw, "/"), "!")))
			if idx := strings.IndexAny(name, " \t\r\n/"); idx >= 0 {
				name = name[:idx]
			}

			switch name {
			case "script", "style", "head", "template", "noscript", "svg":
				// Toggle the dropped region on the matching close tag.
				drop = !closing
			case "br", "p", "div", "li", "tr", "section", "article", "header",
				"footer", "ul", "ol", "table", "h1", "h2", "h3", "h4", "h5", "h6",
				"blockquote", "pre":
				out.WriteByte('\n')
			}
			i = j
			continue
		}

		if drop {
			continue
		}

		// Decode the handful of entities that appear in prose; anything else is
		// left verbatim rather than guessed at.
		if c == '&' {
			if decoded, width, ok := decodeEntity(runes[i:]); ok {
				out.WriteString(decoded)
				i += width - 1
				continue
			}
		}
		out.WriteByte(c)
	}

	return collapseBlankLines(out.String())
}

// decodeEntity decodes one HTML entity starting at the given position.
func decodeEntity(r []byte) (string, int, bool) {
	end := -1
	for i := 1; i < len(r) && i < 12; i++ {
		if r[i] == ';' {
			end = i
			break
		}
	}
	if end < 0 {
		return "", 0, false
	}
	body := string(r[1:end])
	switch body {
	case "amp":
		return "&", end + 1, true
	case "lt":
		return "<", end + 1, true
	case "gt":
		return ">", end + 1, true
	case "quot":
		return "\"", end + 1, true
	case "apos":
		return "'", end + 1, true
	case "nbsp":
		return " ", end + 1, true
	case "#39", "#x27":
		return "'", end + 1, true
	case "mdash":
		return "—", end + 1, true
	case "ndash":
		return "–", end + 1, true
	case "hellip":
		return "…", end + 1, true
	case "copy":
		return "©", end + 1, true
	case "reg":
		return "®", end + 1, true
	}
	// A decimal newline reference is the only numeric form worth decoding;
	// anything else is left verbatim rather than guessed at.
	if body == "#10" {
		return "\n", end + 1, true
	}
	return "", 0, false
}

// collapseBlankLines squeezes runs of blank lines and trims each line.
func collapseBlankLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		// Collapse runs of whitespace inside a line: indentation artefacts
		// carry no meaning in extracted prose.
		out = append(out, strings.Join(strings.Fields(trimmed), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	return strings.Join(out, "\n")
}
