package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/knowledge"
)

// serverProcess is the line-oriented process seam. Tests substitute a fake;
// production uses a stdio child.
type serverProcess interface {
	WriteLine([]byte) error
	ReadLine(max int) ([]byte, error)
	Kill() error
}

// errLineTooLong reports a response line over the bound.
var errLineTooLong = errors.New("mcp: response line exceeds the byte bound")

// maxListPages bounds cursor-following for one discovery list. A server that
// advertises more pages than this is reported as truncated rather than allowed
// to drive an unbounded number of exchanges.
const maxListPages = 64

// rpcRequest is one JSON-RPC request.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcMessage is one inbound JSON-RPC message.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// client owns one MCP server connection. It serializes exchanges, so one
// server has at most one in-flight request and a bounded response reader.
type client struct {
	cfg   ServerConfig
	start func(ServerConfig) (serverProcess, error)
	// remote is the streamable-HTTP exchange seam. It is nil for stdio.
	remote *httpTransport

	gate chan struct{}

	mu           sync.Mutex
	proc         serverProcess
	initialized  bool
	closed       bool
	nextID       int
	idle         *time.Timer
	instructions string
}

// newClient returns a client for a normalized config.
func newClient(cfg ServerConfig, start func(ServerConfig) (serverProcess, error)) *client {
	client := &client{cfg: cfg, start: start, gate: make(chan struct{}, 1)}
	if cfg.Transport == TransportStreamableHTTP {
		client.remote = newHTTPTransport(cfg)
	}
	return client
}

// Close stops the process and refuses future starts.
func (c *client) Close() error {
	c.mu.Lock()
	c.closed = true
	proc := c.proc
	c.proc = nil
	c.initialized = false
	c.instructions = ""
	timer := c.idle
	c.idle = nil
	remote := c.remote
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if remote != nil {
		remote.close()
	}
	if proc != nil {
		return proc.Kill()
	}
	return nil
}

// connected reports whether the client currently holds a live connection.
func (c *client) connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remote != nil {
		return c.initialized && !c.closed
	}
	return c.proc != nil
}

// initialize handshakes once. A failed handshake kills the process so the next
// call starts from a clean state instead of writing into a broken pipe.
func (c *client) initialize(ctx context.Context, meter Meter) error {
	ctx, cancel := c.exchangeContext(ctx)
	defer cancel()
	if err := c.enter(ctx); err != nil {
		return err
	}
	defer c.leave()

	c.mu.Lock()
	initialized := c.initialized
	c.mu.Unlock()
	if initialized {
		return nil
	}
	if err := c.ensureProcess(); err != nil {
		return err
	}
	raw, _, err := c.exchangeLocked(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ohgo", "version": "0.1"},
	}, meter)
	if err != nil {
		c.kill()
		return err
	}
	if len(raw) == 0 {
		c.kill()
		return knowledge.Fail(knowledge.FailureProtocol, false, "mcp initialize returned no result")
	}
	// Server instructions are part of the initialize result, not a tool. Keep
	// them bounded and expose them through discovery so a client that does not
	// otherwise surface initialize output can still give the model the
	// server's own usage guidance.
	var handshake struct {
		Instructions string `json:"instructions"`
	}
	instructions := ""
	if err := json.Unmarshal(raw, &handshake); err == nil {
		instructions, _ = knowledge.BoundText(handshake.Instructions, c.cfg.MaxTextChars)
	}
	if err := c.notifyLocked(ctx, "notifications/initialized"); err != nil {
		c.kill()
		return err
	}
	c.mu.Lock()
	c.initialized = true
	c.instructions = instructions
	c.mu.Unlock()
	c.touch()
	return nil
}

// exchange performs one request/response exchange after initialization.
func (c *client) exchange(ctx context.Context, method string, params any, meter Meter) (json.RawMessage, int, error) {
	ctx, cancel := c.exchangeContext(ctx)
	defer cancel()
	if err := c.enter(ctx); err != nil {
		return nil, 0, err
	}
	defer c.leave()
	return c.exchangeLocked(ctx, method, params, meter)
}

// exchangeLocked performs one exchange. The caller holds the gate.
func (c *client) exchangeLocked(ctx context.Context, method string, params any, meter Meter) (json.RawMessage, int, error) {
	ctx, cancel := c.exchangeContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, 0, contextFailure(ctx)
	}
	if err := c.ensureProcess(); err != nil {
		return nil, 0, err
	}
	limit := c.cfg.MaxBytes
	if meter.Before != nil {
		allowance, err := meter.Before(ctx, method)
		if err != nil {
			return nil, 0, err
		}
		if allowance > 0 && allowance < limit {
			limit = allowance
		}
	}
	if limit <= 0 {
		return nil, 0, knowledge.Fail(knowledge.FailureResponseLimit, false, "mcp response byte bound is exhausted")
	}

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	request, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	if err != nil {
		return nil, 0, knowledge.Fail(knowledge.FailureInvalidRequest, false, "encode mcp request: %v", err)
	}

	if c.remote != nil {
		raw, bytesRead, err := c.remote.exchange(ctx, id, request, limit, meter)
		if err != nil {
			var failure knowledge.Failure
			if errors.As(err, &failure) && failureNeedsKill(failure) {
				c.kill()
			}
			return nil, 0, c.redactError(err)
		}
		if meter.After != nil {
			if err := meter.After(ctx, bytesRead); err != nil {
				return nil, 0, err
			}
		}
		return raw, bytesRead, nil
	}

	proc := c.process()
	if proc == nil {
		return nil, 0, knowledge.Fail(knowledge.FailureUnavailable, true, "mcp server %q is not running", c.cfg.Name)
	}
	write := make(chan error, 1)
	go func() { write <- proc.WriteLine(request) }()
	select {
	case <-ctx.Done():
		c.kill()
		return nil, 0, contextFailure(ctx)
	case err := <-write:
		if err != nil {
			c.kill()
			return nil, 0, knowledge.Fail(knowledge.FailureUnavailable, true, "write to mcp server %q: %v", c.cfg.Name, err)
		}
	}

	type readResult struct {
		raw     json.RawMessage
		bytes   int
		failure *knowledge.Failure
	}
	done := make(chan readResult, 1)
	go func() {
		raw, bytesRead, failure := c.readResponse(id, limit, meter)
		done <- readResult{raw: raw, bytes: bytesRead, failure: failure}
	}()
	select {
	case <-ctx.Done():
		c.kill()
		return nil, 0, contextFailure(ctx)
	case result := <-done:
		if result.failure != nil {
			// A clean JSON-RPC error (method not supported, invalid params,
			// unknown tool) does not desynchronize the stream: the response was
			// read in full, so the process remains usable. Killing it there
			// would restart the server on every unsupported optional method —
			// for example resources/list on a tools-only server.
			if failureNeedsKill(*result.failure) {
				c.kill()
			}
			result.failure.Message = c.cfg.redact(result.failure.Message)
			result.failure.Message, _ = knowledge.BoundText(result.failure.Message, 500)
			return nil, 0, *result.failure
		}
		if meter.After != nil {
			if err := meter.After(ctx, result.bytes); err != nil {
				return nil, 0, err
			}
		}
		c.touch()
		return result.raw, result.bytes, nil
	}
}

// notifyLocked sends a notification without expecting a response. Notifications
// are part of the initialize handshake, so they are not separately metered.
func (c *client) notifyLocked(ctx context.Context, method string) error {
	ctx, cancel := c.exchangeContext(ctx)
	defer cancel()
	request, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method})
	if err != nil {
		return knowledge.Fail(knowledge.FailureInvalidRequest, false, "encode mcp notification: %v", err)
	}
	if c.remote != nil {
		if err := c.remote.notify(ctx, request); err != nil {
			return err
		}
		return nil
	}
	proc := c.process()
	if proc == nil {
		return knowledge.Fail(knowledge.FailureUnavailable, true, "mcp server %q is not running", c.cfg.Name)
	}
	done := make(chan error, 1)
	go func() { done <- proc.WriteLine(request) }()
	select {
	case <-ctx.Done():
		c.kill()
		return contextFailure(ctx)
	case err := <-done:
		if err != nil {
			return knowledge.Fail(knowledge.FailureUnavailable, true, "write mcp notification: %v", err)
		}
		return nil
	}
}

// readResponse reads messages until the matching response arrives.
func (c *client) readResponse(id int, limit int, meter Meter) (json.RawMessage, int, *knowledge.Failure) {
	total := 0
	for unsolicited := 0; unsolicited < 64; unsolicited++ {
		remaining := limit - total
		if remaining <= 0 {
			failure := knowledge.Fail(knowledge.FailureResponseLimit, false,
				"mcp exchange exceeded %d bytes", limit)
			return nil, total, &failure
		}
		proc := c.process()
		if proc == nil {
			failure := knowledge.Fail(knowledge.FailureUnavailable, true, "mcp server %q disconnected", c.cfg.Name)
			return nil, total, &failure
		}
		line, err := proc.ReadLine(remaining)
		if err != nil {
			failure := classifyReadError(err, c.cfg.Name)
			return nil, total, &failure
		}
		total += len(line)
		var message rpcMessage
		if err := json.Unmarshal(line, &message); err != nil {
			failure := knowledge.Fail(knowledge.FailureProtocol, false, "mcp server sent malformed JSON")
			return nil, total, &failure
		}
		// A server request is never executed. Refuse it explicitly so a
		// server cannot turn the client into its tool runtime.
		if message.Method != "" && len(message.ID) > 0 && string(message.ID) != "null" {
			c.refuseServerRequest(message)
			continue
		}
		if len(message.ID) == 0 || string(message.ID) == "null" {
			// A notification is bounded. Progress is the only one that maps to
			// runtime state; everything else stays inert.
			if message.Method == "notifications/progress" {
				emitProgress(meter, message.Params, c.cfg)
			}
			continue
		}
		if !idMatches(message.ID, id) {
			continue
		}
		if message.Error != nil {
			failure := classifyRPCError(*message.Error)
			return nil, total, &failure
		}
		return message.Result, total, nil
	}
	failure := knowledge.Fail(knowledge.FailureProtocol, false, "mcp server sent too many unsolicited messages")
	return nil, total, &failure
}

// emitProgress maps a bounded server progress notification to the Meter.
//
// The notification is untrusted data. It is bounded before it reaches a UI and
// is never treated as completion.
func emitProgress(meter Meter, params json.RawMessage, cfg ServerConfig) {
	if meter.Progress == nil || len(params) == 0 {
		return
	}
	var payload struct {
		Progress float64 `json:"progress"`
		Total    float64 `json:"total"`
		Message  string  `json:"message"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return
	}
	message, _ := knowledge.BoundText(strings.TrimSpace(cfg.redact(payload.Message)), 200)
	meter.Progress(Progress{
		Message:  message,
		Progress: payload.Progress,
		Total:    payload.Total,
	})
}

// refuseServerRequest answers a server-initiated request with "not supported".
func (c *client) refuseServerRequest(message rpcMessage) {
	proc := c.process()
	if proc == nil {
		return
	}
	response, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      message.ID,
		"error": map[string]any{
			"code":    -32601,
			"message": "client does not implement server requests",
		},
	})
	if err != nil {
		return
	}
	_ = proc.WriteLine(response)
}

// ensureProcess starts the configured server if needed.
func (c *client) ensureProcess() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return knowledge.Fail(knowledge.FailureUnavailable, false, "mcp client is closed")
	}
	if c.remote != nil {
		c.mu.Unlock()
		return nil
	}
	if c.proc != nil {
		c.mu.Unlock()
		return nil
	}
	start := c.start
	c.mu.Unlock()
	proc, err := start(c.cfg)
	if err != nil {
		return knowledge.Fail(knowledge.FailureUnavailable, true, "start mcp server %q: %v", c.cfg.Name, err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = proc.Kill()
		return knowledge.Fail(knowledge.FailureUnavailable, false, "mcp client is closed")
	}
	c.proc = proc
	c.initialized = false
	c.mu.Unlock()
	return nil
}

// process returns the current process, if any.
func (c *client) process() serverProcess {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proc
}

// serverInstructions returns the bounded instructions from the last
// handshake. It is empty when the server supplied none or the process has been
// restarted since.
func (c *client) serverInstructions() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instructions
}

// touch resets the idle process timer.
func (c *client) touch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idle != nil {
		c.idle.Stop()
	}
	if c.closed || c.proc == nil || c.remote != nil {
		return
	}
	c.idle = time.AfterFunc(c.cfg.IdleTimeout, func() { c.kill() })
}

// kill stops the process and clears initialization state.
func (c *client) kill() {
	c.mu.Lock()
	proc := c.proc
	c.proc = nil
	c.initialized = false
	c.instructions = ""
	timer := c.idle
	c.idle = nil
	remote := c.remote
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if remote != nil {
		remote.reset()
	}
	if proc != nil {
		_ = proc.Kill()
	}
}

// enter acquires the single-exchange gate, honouring cancellation.
func (c *client) enter(ctx context.Context) error {
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return contextFailure(ctx)
	}
}

// leave releases the gate.
func (c *client) leave() { <-c.gate }

// listTools calls tools/list, follows nextCursor, and bounds the result.
//
// Pagination is mandatory for compatibility: a server may return a bounded
// page with an opaque nextCursor, and a client that ignores it silently sees
// only a fraction of the advertised tools. Every page is a separately metered
// exchange, and a repeated cursor or the page ceiling is reported as
// truncation rather than followed forever.
func (c *client) listTools(ctx context.Context, meter Meter) ([]wireTool, bool, error) {
	var out []wireTool
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, _, err := c.exchange(ctx, "tools/list", params, meter)
		if err != nil {
			return nil, false, err
		}
		if len(raw) == 0 {
			return out, false, nil
		}
		var envelope struct {
			Tools      []wireTool `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, false, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp tools/list result is malformed")
		}
		out = append(out, envelope.Tools...)
		if len(out) >= c.cfg.MaxItems {
			return out[:c.cfg.MaxItems], true, nil
		}
		next := strings.TrimSpace(envelope.NextCursor)
		if next == "" || next == cursor {
			return out, next != "" && next == cursor, nil
		}
		cursor = next
	}
	return out, true, nil
}

// listResources calls resources/list, follows nextCursor, and bounds the
// result.
func (c *client) listResources(ctx context.Context, meter Meter) ([]wireResource, bool, error) {
	var out []wireResource
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, _, err := c.exchange(ctx, "resources/list", params, meter)
		if err != nil {
			return nil, false, err
		}
		if len(raw) == 0 {
			return out, false, nil
		}
		var envelope struct {
			Resources  []wireResource `json:"resources"`
			NextCursor string         `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, false, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp resources/list result is malformed")
		}
		out = append(out, envelope.Resources...)
		if len(out) >= c.cfg.MaxItems {
			return out[:c.cfg.MaxItems], true, nil
		}
		next := strings.TrimSpace(envelope.NextCursor)
		if next == "" || next == cursor {
			return out, next != "" && next == cursor, nil
		}
		cursor = next
	}
	return out, true, nil
}

// wireTool is one discovered tool description.
type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Schema      json.RawMessage `json:"schema"`
}

// Schema returns the input schema, tolerating either field spelling.
func (t wireTool) SchemaRaw() json.RawMessage {
	if len(t.InputSchema) > 0 {
		return t.InputSchema
	}
	return t.Schema
}

// wireResource is one discovered resource description.
type wireResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType"`
	Text        string `json:"text,omitempty"`
	Blob        string `json:"blob,omitempty"`
}

// wirePrompt is one discovered prompt template.
type wirePrompt struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Arguments   []wireArgument `json:"arguments"`
}

// wireArgument is one prompt argument declaration.
type wireArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// schemaRaw synthesizes a JSON Schema for a prompt's arguments, so discovery
// publishes the same callable contract for a prompt as for a tool.
func (p wirePrompt) schemaRaw() json.RawMessage {
	if len(p.Arguments) == 0 {
		return nil
	}
	properties := make(map[string]any, len(p.Arguments))
	var required []string
	for _, argument := range p.Arguments {
		name := strings.TrimSpace(argument.Name)
		if name == "" {
			continue
		}
		property := map[string]any{"type": "string"}
		if argument.Description != "" {
			property["description"] = argument.Description
		}
		properties[name] = property
		if argument.Required {
			required = append(required, name)
		}
	}
	if len(properties) == 0 {
		return nil
	}
	object := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		object["required"] = required
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil
	}
	return encoded
}

// listPrompts calls prompts/list, follows nextCursor, and bounds the result.
func (c *client) listPrompts(ctx context.Context, meter Meter) ([]wirePrompt, bool, error) {
	var out []wirePrompt
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, _, err := c.exchange(ctx, "prompts/list", params, meter)
		if err != nil {
			return nil, false, err
		}
		if len(raw) == 0 {
			return out, false, nil
		}
		var envelope struct {
			Prompts    []wirePrompt `json:"prompts"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, false, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp prompts/list result is malformed")
		}
		out = append(out, envelope.Prompts...)
		if len(out) >= c.cfg.MaxItems {
			return out[:c.cfg.MaxItems], true, nil
		}
		next := strings.TrimSpace(envelope.NextCursor)
		if next == "" || next == cursor {
			return out, next != "" && next == cursor, nil
		}
		cursor = next
	}
	return out, true, nil
}

// classifyReadError turns a process read failure into a truthful class.
func classifyReadError(err error, server string) knowledge.Failure {
	if errors.Is(err, errLineTooLong) {
		return knowledge.Fail(knowledge.FailureResponseLimit, false, "mcp server %q response line exceeded the byte bound", server)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return knowledge.Fail(knowledge.FailureUnavailable, true, "mcp server %q disconnected", server)
	}
	return knowledge.Fail(knowledge.FailureUnavailable, true, "read mcp server %q: %v", server, err)
}

// failureNeedsKill reports whether a failure leaves the stream unusable.
//
// A JSON-RPC error response is a complete frame: the request is finished even
// though it failed. Transport, framing, and bound failures are different — the
// next read cannot be trusted to line up with the next request.
func failureNeedsKill(failure knowledge.Failure) bool {
	switch failure.Class {
	case knowledge.FailureUnsupported, knowledge.FailureInvalidRequest,
		knowledge.FailureUnknownCapability, knowledge.FailureToolError:
		return false
	default:
		return true
	}
}

// classifyRPCError maps a JSON-RPC error to a failure class.
func classifyRPCError(rpc rpcError) knowledge.Failure {
	message, _ := knowledge.BoundText(rpc.Message, 300)
	switch rpc.Code {
	case -32601:
		return knowledge.Fail(knowledge.FailureUnsupported, false, "mcp method not supported: %s", message)
	case -32602:
		return knowledge.Fail(knowledge.FailureInvalidRequest, false, "mcp invalid params: %s", message)
	case -32603:
		return knowledge.Fail(knowledge.FailureUnavailable, true, "mcp internal error: %s", message)
	default:
		return knowledge.Fail(knowledge.FailureProtocol, false, "mcp error %d: %s", rpc.Code, message)
	}
}

// exchangeContext applies the configured per-exchange timeout without
// extending a stricter caller deadline.
func (c *client) exchangeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.cfg.Timeout
	if timeout <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// contextFailure classifies a finished context.
func contextFailure(ctx context.Context) knowledge.Failure {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return knowledge.Fail(knowledge.FailureTimeout, false, "mcp exchange timed out")
	}
	return knowledge.Fail(knowledge.FailureCancelled, false, "mcp exchange cancelled")
}

// redactError removes configured credential values from a classified failure.
func (c *client) redactError(err error) error {
	var failure knowledge.Failure
	if errors.As(err, &failure) {
		failure.Message = c.cfg.redact(failure.Message)
		failure.Message, _ = knowledge.BoundText(failure.Message, 500)
		return failure
	}
	return err
}

// idMatches reports whether a response id matches the numeric request id.
func idMatches(raw json.RawMessage, id int) bool {
	text := strings.TrimSpace(string(raw))
	if text == strconv.Itoa(id) {
		return true
	}
	// Some servers echo a JSON number with whitespace or as a string.
	var value int
	if err := json.Unmarshal(raw, &value); err == nil && value == id {
		return true
	}
	var textValue string
	if err := json.Unmarshal(raw, &textValue); err == nil && textValue == strconv.Itoa(id) {
		return true
	}
	return false
}

// stdioProcess is a line-oriented JSON-RPC child process.
type stdioProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	done   chan struct{}

	waitMu  sync.Mutex
	waitErr error
}

// startStdio starts a configured MCP server.
func startStdio(cfg ServerConfig) (serverProcess, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.Dir
	cmd.Env = filteredEnv(cfg.Env)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	process := &stdioProcess{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 64*1024),
		done:   make(chan struct{}),
	}
	go func() {
		err := cmd.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.done)
	}()
	return process, nil
}

// WriteLine writes one newline-delimited message.
func (p *stdioProcess) WriteLine(line []byte) error {
	if len(line) == 0 {
		return errors.New("mcp: empty write")
	}
	payload := make([]byte, 0, len(line)+1)
	payload = append(payload, line...)
	payload = append(payload, '\n')
	_, err := p.stdin.Write(payload)
	return err
}

// ReadLine reads one bounded line.
func (p *stdioProcess) ReadLine(max int) ([]byte, error) {
	if max <= 0 {
		return nil, errLineTooLong
	}
	var out []byte
	for {
		chunk, err := p.stdout.ReadSlice('\n')
		if len(out)+len(chunk) > max+1 {
			return nil, errLineTooLong
		}
		out = append(out, chunk...)
		switch {
		case err == nil:
			out = bytes.TrimSuffix(out, []byte("\n"))
			out = bytes.TrimSuffix(out, []byte("\r"))
			return out, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

// Kill terminates the process and waits briefly for it to be reaped.
func (p *stdioProcess) Kill() error {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("mcp: timed out waiting for %q to exit", p.cmd.Path)
	}
}

// filteredEnv passes only the base environment and explicitly named variables.
//
// It deliberately does not inherit the whole environment: an external server
// must not receive unrelated provider credentials by default.
func filteredEnv(names []string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true,
		"LANG": true, "LC_ALL": true, "TZ": true,
	}
	for _, name := range names {
		allowed[name] = true
	}
	var out []string
	for _, assignment := range os.Environ() {
		name, _, ok := strings.Cut(assignment, "=")
		if ok && allowed[name] {
			out = append(out, assignment)
		}
	}
	return out
}
