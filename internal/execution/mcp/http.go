package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

// httpTransport is the streamable-HTTP JSON-RPC seam.
//
// It deliberately implements the small interoperable core: one POST per
// message, a JSON response or a server-sent-event stream, and the session id
// header the server may issue during initialize. Resumability, long-lived
// standalone GET streams, and server-to-client requests are not implemented;
// those features would add state the capability layer does not need.
type httpTransport struct {
	cfg    ServerConfig
	client *http.Client

	mu        sync.Mutex
	sessionID string
}

// newHTTPTransport builds the bounded HTTP transport for a config.
func newHTTPTransport(cfg ServerConfig) *httpTransport {
	return &httpTransport{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			// Redirects are refused: a configured endpoint is the exact
			// egress target, and following a redirect would silently widen it.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// close releases idle connections. The transport remains usable.
func (t *httpTransport) close() {
	t.client.CloseIdleConnections()
}

// reset clears the server-issued session id. A created or cancelled
// connection must re-initialize rather than reuse a session the server may
// have dropped.
func (t *httpTransport) reset() {
	t.mu.Lock()
	t.sessionID = ""
	t.mu.Unlock()
	t.client.CloseIdleConnections()
}

// exchange performs one JSON-RPC request and returns its result.
func (t *httpTransport) exchange(ctx context.Context, id int, request []byte, limit int, meter Meter) (json.RawMessage, int, error) {
	response, err := t.post(ctx, request)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent {
		return nil, 0, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// The body is intentionally not read into the error: a proxy or
		// authentication layer may echo credentials or request headers.
		return nil, 0, httpStatusFailure(t.cfg.Name, response.StatusCode)
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if mediaType == "text/event-stream" {
		return t.readSSE(ctx, response.Body, id, limit, meter)
	}
	body, bytesRead, err := readBounded(response.Body, limit)
	if err != nil {
		return nil, bytesRead, err
	}
	if bytesRead == 0 {
		return nil, 0, knowledge.Fail(knowledge.FailureProtocol, false,
			"mcp server %q returned an empty HTTP response", t.cfg.Name)
	}
	var message rpcMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return nil, bytesRead, knowledge.Fail(knowledge.FailureMalformedResponse, false,
			"mcp server %q returned malformed JSON", t.cfg.Name)
	}
	if message.Method != "" && len(message.ID) > 0 && string(message.ID) != "null" {
		// A server request is never executed. Refuse it with a clear protocol
		// failure rather than letting it become client-side work.
		return nil, bytesRead, knowledge.Fail(knowledge.FailureUnsupported, false,
			"mcp server %q sent an unsupported server request %q", t.cfg.Name, message.Method)
	}
	if len(message.ID) == 0 {
		return nil, bytesRead, knowledge.Fail(knowledge.FailureProtocol, false,
			"mcp server %q returned a notification instead of a response", t.cfg.Name)
	}
	if !idMatches(message.ID, id) {
		return nil, bytesRead, knowledge.Fail(knowledge.FailureProtocol, false,
			"mcp server %q returned a mismatched response id", t.cfg.Name)
	}
	if message.Error != nil {
		return nil, bytesRead, classifyRPCError(*message.Error)
	}
	return message.Result, bytesRead, nil
}

// notify performs one JSON-RPC notification. A server acknowledgement is not
// part of the result and is not metered.
func (t *httpTransport) notify(ctx context.Context, request []byte) error {
	response, err := t.post(ctx, request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	return httpStatusFailure(t.cfg.Name, response.StatusCode)
}

// httpStatusFailure normalizes an HTTP status without reading a body that may
// echo credentials.
func httpStatusFailure(server string, status int) knowledge.Failure {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return knowledge.Fail(knowledge.FailureAuthentication, false,
			"mcp server %q rejected the credential (HTTP %d)", server, status)
	case http.StatusTooManyRequests:
		return knowledge.Fail(knowledge.FailureRateLimited, true,
			"mcp server %q rate limited the request (HTTP %d)", server, status)
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return knowledge.Fail(knowledge.FailureTimeout, true,
			"mcp server %q timed out (HTTP %d)", server, status)
	default:
		return knowledge.Fail(knowledge.FailureUnavailable, status >= 500,
			"mcp server %q returned HTTP %d", server, status)
	}
}

// post sends one JSON-RPC message with the configured headers.
func (t *httpTransport) post(ctx context.Context, request []byte) (*http.Response, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.URL, bytes.NewReader(request))
	if err != nil {
		return nil, knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"mcp server %q request: %v", t.cfg.Name, err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json, text/event-stream")
	httpRequest.Header.Set("Mcp-Protocol-Version", "2024-11-05")
	t.mu.Lock()
	sessionID := t.sessionID
	t.mu.Unlock()
	if sessionID != "" {
		httpRequest.Header.Set("Mcp-Session-Id", sessionID)
	}
	for header, envName := range t.cfg.HeaderEnv {
		value := os.Getenv(envName)
		if value == "" {
			continue
		}
		httpRequest.Header.Set(header, value)
	}
	response, err := t.client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return nil, contextFailure(ctx)
		}
		return nil, knowledge.Fail(knowledge.FailureUnavailable, true,
			"mcp server %q request failed", t.cfg.Name)
	}
	if issued := strings.TrimSpace(response.Header.Get("Mcp-Session-Id")); issued != "" {
		t.mu.Lock()
		t.sessionID = issued
		t.mu.Unlock()
	}
	return response, nil
}

// readSSE consumes a server-sent-event response until the matching JSON-RPC
// response arrives. Progress notifications are forwarded; other notifications
// are ignored but still count toward the byte bound.
func (t *httpTransport) readSSE(ctx context.Context, body io.Reader, id int, limit int, meter Meter) (json.RawMessage, int, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, int64(limit)+1))
	scanner.Buffer(make([]byte, 4096), limit+1)
	var data bytes.Buffer
	total := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, total, contextFailure(ctx)
		}
		line := scanner.Bytes()
		total += len(line) + 1
		if total > limit {
			return nil, total, knowledge.Fail(knowledge.FailureResponseLimit, false,
				"mcp exchange exceeded %d bytes", limit)
		}
		if len(line) == 0 {
			if data.Len() == 0 {
				continue
			}
			payload := append([]byte(nil), data.Bytes()...)
			data.Reset()
			var message rpcMessage
			if err := json.Unmarshal(payload, &message); err != nil {
				return nil, total, knowledge.Fail(knowledge.FailureMalformedResponse, false,
					"mcp server %q sent a malformed event", t.cfg.Name)
			}
			if message.Method == "notifications/progress" {
				emitProgress(meter, message.Params, t.cfg)
				continue
			}
			if message.Method != "" && len(message.ID) > 0 && string(message.ID) != "null" {
				return nil, total, knowledge.Fail(knowledge.FailureUnsupported, false,
					"mcp server %q sent an unsupported server request %q", t.cfg.Name, message.Method)
			}
			if len(message.ID) == 0 || !idMatches(message.ID, id) {
				continue
			}
			if message.Error != nil {
				return nil, total, classifyRPCError(*message.Error)
			}
			return message.Result, total, nil
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			value := bytes.TrimPrefix(line, []byte("data:"))
			value = bytes.TrimPrefix(value, []byte(" "))
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(value)
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, total, knowledge.Fail(knowledge.FailureResponseLimit, false,
				"mcp exchange exceeded %d bytes", limit)
		}
		if ctx.Err() != nil {
			return nil, total, contextFailure(ctx)
		}
		return nil, total, knowledge.Fail(knowledge.FailureUnavailable, true,
			"read mcp server %q event stream: %v", t.cfg.Name, err)
	}
	return nil, total, knowledge.Fail(knowledge.FailureProtocol, false,
		"mcp server %q closed the event stream without a response", t.cfg.Name)
}

// readBounded reads at most limit bytes and reports a response-limit failure
// when the body is larger.
func readBounded(body io.Reader, limit int) ([]byte, int, error) {
	if limit <= 0 {
		return nil, 0, knowledge.Fail(knowledge.FailureResponseLimit, false, "mcp response byte bound is exhausted")
	}
	limited := io.LimitReader(body, int64(limit)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, len(data), knowledge.Fail(knowledge.FailureUnavailable, true, "read mcp response: %v", err)
	}
	if len(data) > limit {
		return nil, len(data), knowledge.Fail(knowledge.FailureResponseLimit, false,
			"mcp exchange exceeded %d bytes", limit)
	}
	return data, len(data), nil
}

// validHeaderName reports whether a configured HTTP header name is a token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r <= 32 || r >= 127 {
			return false
		}
		switch r {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
			return false
		}
	}
	return true
}
