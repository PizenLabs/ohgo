package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// sseEvent is one decoded server-sent event.
type sseEvent struct {
	// Name is the value of the event: field, empty when absent.
	Name string
	// Data is the concatenation of the event's data: lines joined by newlines.
	Data string
}

// readSSE decodes an SSE body into events and invokes fn for each in order.
//
// It implements the subset of the SSE grammar the providers use: `event:`,
// `data:`, and `id:` fields, `\n` and `\r\n` line endings, and `:` comment
// lines. Decoding stops at the first fn error or when the body ends. readSSE
// does not re-dispatch on a blank line for events that carry no data field,
// matching how every supported provider emits stream events.
func readSSE(ctx context.Context, body io.Reader, fn func(sseEvent) error) error {
	scanner := bufio.NewScanner(body)
	// Provider payloads can exceed bufio's default 64 KiB line limit when a
	// tool-call argument delta or a long text delta arrives in one chunk.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var (
		name string
		data []string
	)

	flush := func() error {
		if len(data) == 0 {
			name = ""
			return nil
		}
		event := sseEvent{Name: name, Data: strings.Join(data, "\n")}
		name = ""
		data = nil
		return fn(event)
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if isSSEIgnored(line) {
			// Neither field affects decoding for these transports.
			continue
		}
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// Comment / keep-alive.
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			data = append(data, payload)
		default:
			// Unknown field; ignore per the SSE spec.
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("ai: read SSE body: %w", err)
	}
	return flush()
}

// isSSEIgnored reports whether a line carries an SSE field this transport
// ignores. id and retry govern reconnection, not decoding, so neither
// affects the events produced here.
func isSSEIgnored(line string) bool {
	return strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:")
}

// httpError is a non-2xx response from a provider.
type httpError struct {
	// Status is the HTTP status code.
	Status int
	// Body is the truncated response body.
	Body string
	// Header carries selected response headers for retry decisions.
	Header http.Header
	// Code is the provider's own error code when the body carries one.
	Code int
	// Message is the provider's human-readable error message.
	Message string
	// FailedRoutingStep is OpenRouter's routing-stage diagnosis, empty when
	// the response is not an OpenRouter routing error.
	FailedRoutingStep string
}

func (e *httpError) Error() string {
	body := e.Body
	if len(body) > 512 {
		body = body[:512] + "…"
	}
	message := fmt.Sprintf("ai: provider returned HTTP %d: %s", e.Status, strings.TrimSpace(body))
	if e.AgenticHarnessRestriction() {
		message += "\n" + agenticHarnessDiagnostic
	}
	return message
}

// AgenticHarnessRestriction reports whether the response is OpenRouter's
// agentic-harness routing gate rather than a generic 403.
//
// The routing step is authoritative; the message check covers responses from
// before OpenRouter added the metadata field. Requiring the specific phrase
// keeps other 403s — spend limits, disabled keys, region blocks — unclassified.
func (e *httpError) AgenticHarnessRestriction() bool {
	if e.FailedRoutingStep == OpenRouterAgenticHarnessStep {
		return true
	}
	return e.Status == http.StatusForbidden &&
		strings.Contains(strings.ToLower(e.Message), "only available on agentic harnesses")
}

// Unwrap exposes the classification to errors.Is without hiding the transport
// error from errors.As.
func (e *httpError) Unwrap() error {
	if e.AgenticHarnessRestriction() {
		return ErrAgenticHarnessRestriction
	}
	return nil
}

// agenticHarnessDiagnostic explains the gate without claiming that OHGO is
// approved. Attribution and eligibility are independent states.
const agenticHarnessDiagnostic = `OpenRouter blocked this request at "` + OpenRouterAgenticHarnessStep + `": the selected free endpoint is available only to applications OpenRouter recognizes as agentic harnesses. OHGO sends app attribution (HTTP-Referer, X-OpenRouter-Title, X-OpenRouter-Categories: ` + OpenRouterCategories + `), but attribution is not eligibility. Check the app listing at https://openrouter.ai/apps?url=https%3A%2F%2Fohgo.pizenlabs.dev%2F or contact OpenRouter support. OHGO did not substitute another model.`

// StatusCode returns the HTTP status code, for errors.As consumers.
func (e *httpError) StatusCode() int { return e.Status }

// RetryAfter reports the provider's retry hint in seconds, or 0 when absent.
func (e *httpError) RetryAfter() int {
	v := e.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err != nil {
		return 0
	}
	return secs
}

// doJSON posts a JSON request and returns the response for SSE decoding.
//
// The caller must close the returned body. A non-2xx status is converted to a
// *httpError with the body already read, so callers can treat a transport
// error and an API error uniformly.
func doJSON(ctx context.Context, client *http.Client, url string, req any, headers map[string]string) (*http.Response, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: send request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		return nil, newHTTPError(resp.StatusCode, string(body), resp.Header)
	}
	return resp, nil
}

// newHTTPError decodes the provider's JSON error object without losing the raw
// body. Error metadata is diagnostic evidence: OpenRouter names the routing
// stage that rejected a request, and that name is what separates an eligibility
// restriction from a generic 403.
func newHTTPError(status int, body string, header http.Header) *httpError {
	failure := &httpError{Status: status, Body: body, Header: header}
	var payload struct {
		Error struct {
			Message  string          `json:"message"`
			Code     json.RawMessage `json:"code"`
			Metadata struct {
				FailedRoutingStep string `json:"failed_routing_step"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return failure
	}
	failure.Message = payload.Error.Message
	failure.FailedRoutingStep = payload.Error.Metadata.FailedRoutingStep
	if len(payload.Error.Code) > 0 {
		var code int
		if err := json.Unmarshal(payload.Error.Code, &code); err == nil {
			failure.Code = code
		}
	}
	return failure
}

// decodeJSONArgs lives in toolargs.go: one decoder classifies complete,
// incomplete, and malformed payloads for every provider.
