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
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// client owns one MCP server process. It serializes exchanges, so one server
// has at most one in-flight request and a bounded response reader.
type client struct {
	cfg   ServerConfig
	start func(ServerConfig) (serverProcess, error)

	gate chan struct{}

	mu          sync.Mutex
	proc        serverProcess
	initialized bool
	closed      bool
	nextID      int
	idle        *time.Timer
}

// newClient returns a client for a normalized config.
func newClient(cfg ServerConfig, start func(ServerConfig) (serverProcess, error)) *client {
	return &client{cfg: cfg, start: start, gate: make(chan struct{}, 1)}
}

// Close stops the process and refuses future starts.
func (c *client) Close() error {
	c.mu.Lock()
	c.closed = true
	proc := c.proc
	c.proc = nil
	c.initialized = false
	timer := c.idle
	c.idle = nil
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if proc != nil {
		return proc.Kill()
	}
	return nil
}

// initialize handshakes once. A failed handshake kills the process so the next
// call starts from a clean state instead of writing into a broken pipe.
func (c *client) initialize(ctx context.Context, meter Meter) error {
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
	if err := c.notifyLocked(ctx, "notifications/initialized"); err != nil {
		c.kill()
		return err
	}
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
	c.touch()
	return nil
}

// exchange performs one request/response exchange after initialization.
func (c *client) exchange(ctx context.Context, method string, params any, meter Meter) (json.RawMessage, int, error) {
	if err := c.enter(ctx); err != nil {
		return nil, 0, err
	}
	defer c.leave()
	return c.exchangeLocked(ctx, method, params, meter)
}

// exchangeLocked performs one exchange. The caller holds the gate.
func (c *client) exchangeLocked(ctx context.Context, method string, params any, meter Meter) (json.RawMessage, int, error) {
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
		raw, bytesRead, failure := c.readResponse(id, limit)
		done <- readResult{raw: raw, bytes: bytesRead, failure: failure}
	}()
	select {
	case <-ctx.Done():
		c.kill()
		return nil, 0, contextFailure(ctx)
	case result := <-done:
		if result.failure != nil {
			c.kill()
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
	request, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method})
	if err != nil {
		return knowledge.Fail(knowledge.FailureInvalidRequest, false, "encode mcp notification: %v", err)
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
func (c *client) readResponse(id int, limit int) (json.RawMessage, int, *knowledge.Failure) {
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
		if len(message.ID) == 0 {
			// A notification is bounded but otherwise ignored.
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

// touch resets the idle process timer.
func (c *client) touch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idle != nil {
		c.idle.Stop()
	}
	if c.closed || c.proc == nil {
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
	timer := c.idle
	c.idle = nil
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
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

// listTools calls tools/list and bounds the result.
func (c *client) listTools(ctx context.Context, meter Meter) ([]wireTool, bool, error) {
	raw, _, err := c.exchange(ctx, "tools/list", map[string]any{}, meter)
	if err != nil {
		return nil, false, err
	}
	if len(raw) == 0 {
		return nil, false, nil
	}
	var envelope struct {
		Tools []wireTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, false, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp tools/list result is malformed")
	}
	truncated := false
	if len(envelope.Tools) > c.cfg.MaxItems {
		envelope.Tools = envelope.Tools[:c.cfg.MaxItems]
		truncated = true
	}
	return envelope.Tools, truncated, nil
}

// listResources calls resources/list and bounds the result.
func (c *client) listResources(ctx context.Context, meter Meter) ([]wireResource, bool, error) {
	raw, _, err := c.exchange(ctx, "resources/list", map[string]any{}, meter)
	if err != nil {
		return nil, false, err
	}
	if len(raw) == 0 {
		return nil, false, nil
	}
	var envelope struct {
		Resources []wireResource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, false, knowledge.Fail(knowledge.FailureMalformedResponse, false, "mcp resources/list result is malformed")
	}
	truncated := false
	if len(envelope.Resources) > c.cfg.MaxItems {
		envelope.Resources = envelope.Resources[:c.cfg.MaxItems]
		truncated = true
	}
	return envelope.Resources, truncated, nil
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

// contextFailure classifies a finished context.
func contextFailure(ctx context.Context) knowledge.Failure {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return knowledge.Fail(knowledge.FailureTimeout, false, "mcp exchange timed out")
	}
	return knowledge.Fail(knowledge.FailureCancelled, false, "mcp exchange cancelled")
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
