// Package runtime implements Debug Adapter Protocol observations.
//
// It is the runtime intelligence layer: it starts an authorized debug adapter,
// negotiates capabilities, and observes real execution — breakpoints, threads,
// stack frames, scopes, variables, and exceptions. It does not own process
// authorization (that is the manager's Authorizer seam, wired by the control
// plane) and it does not replace the Execution Plane: the adapter command comes
// from configuration, never from model output, and every session is bounded and
// reaped.
package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/wire"
)

// Errors.
var (
	// ErrTimeout reports a request or handshake that exceeded its deadline.
	ErrTimeout = errors.New("runtime: debug adapter timed out")
	// ErrClosed reports a session whose adapter process is gone.
	ErrClosed = errors.New("runtime: debug adapter is closed")
	// ErrUnsupported reports a capability the adapter did not advertise.
	ErrUnsupported = errors.New("runtime: adapter does not support the capability")
)

// Protocol message envelope types.
const (
	typeRequest  = "request"
	typeResponse = "response"
	typeEvent    = "event"
)

// Request is one DAP request.
type Request struct {
	Seq       int             `json:"seq"`
	Type      string          `json:"type"`
	Command   string          `json:"command"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Response is one DAP response.
type Response struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	RequestSeq int             `json:"request_seq"`
	Success    bool            `json:"success"`
	Command    string          `json:"command"`
	Message    string          `json:"message,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

// Event is one DAP event.
type Event struct {
	Seq   int             `json:"seq"`
	Type  string          `json:"type"`
	Event string          `json:"event"`
	Body  json.RawMessage `json:"body,omitempty"`
}

// Capabilities is the subset of initialize.response.body this client
// negotiates. Absent booleans are false: absence means unsupported.
type Capabilities struct {
	SupportsConditionalBreakpoints bool `json:"supportsConditionalBreakpoints"`
	SupportsFunctionBreakpoints    bool `json:"supportsFunctionBreakpoints"`
	SupportsTerminateRequest       bool `json:"supportsTerminateRequest"`
	SupportsRestartRequest         bool `json:"supportsRestartRequest"`
	SupportsSetVariable            bool `json:"supportsSetVariable"`
	SupportsExceptionInfoRequest   bool `json:"supportsExceptionInfoRequest"`
	SupportsEvaluateForHovers      bool `json:"supportsEvaluateForHovers"`
}

// Thread is one debuggee thread.
type Thread struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Frame is one stack frame.
type Frame struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Source *Source `json:"source,omitempty"`
	Line   int     `json:"line"`
	Column int     `json:"column"`
}

// Source is a source location.
type Source struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// Scope is one variable scope.
type Scope struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
	Expensive          bool   `json:"expensive"`
}

// Variable is one debuggee value.
type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference,omitempty"`
	EvaluateName       string `json:"evaluateName,omitempty"`
	IndexedVariables   int    `json:"indexedVariables,omitempty"`
	NamedVariables     int    `json:"namedVariables,omitempty"`
}

// EvaluateResult is one evaluated expression.
type EvaluateResult struct {
	Result             string `json:"result"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference,omitempty"`
}

// Breakpoint is one resolved breakpoint.
type Breakpoint struct {
	ID       int     `json:"id,omitempty"`
	Verified bool    `json:"verified"`
	Line     int     `json:"line,omitempty"`
	Message  string  `json:"message,omitempty"`
	Source   *Source `json:"source,omitempty"`
}

// SourceBreakpoint is one requested breakpoint.
type SourceBreakpoint struct {
	Line      int    `json:"line"`
	Column    int    `json:"column,omitempty"`
	Condition string `json:"condition,omitempty"`
}

// SourceBreakpoints groups breakpoints by source.
type SourceBreakpoints struct {
	Name        string
	Path        string
	Breakpoints []SourceBreakpoint
}

// client is one adapter connection. It is unexported so consumers use Session,
// which enforces authorization and state transitions.
type client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *stderrBuffer

	writeMu sync.Mutex
	mu      sync.Mutex
	nextSeq int
	pending map[int]chan Response
	caps    Capabilities
	closed  bool
	readErr error

	done       chan struct{}
	waitDone   chan struct{}
	finishOnce sync.Once
	waitErr    error

	onEvent func(Event)
}

type stderrBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *stderrBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// startClient launches one adapter process and starts its read loop.
func startClient(spec Spec, onEvent func(Event)) (*client, error) {
	if len(spec.Command) == 0 {
		return nil, errors.New("runtime: debug adapter has no command")
	}
	if spec.DialClient {
		return startDialClient(spec, onEvent)
	}
	command := exec.Command(spec.Command[0], spec.Command[1:]...)
	if spec.Dir != "" {
		command.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		command.Env = append(os.Environ(), spec.Env...)
	}
	configureProcess(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("runtime: stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("runtime: stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("runtime: stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("runtime: start %q: %w", spec.Name, err)
	}
	c := &client{
		cmd:      command,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   &stderrBuffer{limit: 4096},
		pending:  make(map[int]chan Response),
		done:     make(chan struct{}),
		waitDone: make(chan struct{}),
		onEvent:  onEvent,
	}
	go c.drainStderr(stderr)
	go c.readLoop()
	go c.waitLoop()
	return c, nil
}

// startDialClient starts an adapter that connects back to a loopback listener.
//
// This is Delve's `dlv dap --client-addr=host:port` shape: the adapter owns the
// debuggee, while Ohgo owns the socket. The listener is closed immediately
// after the single accepted connection, so nothing else can join the session.
func startDialClient(spec Spec, onEvent func(Event)) (*client, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("runtime: listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	command := exec.Command(spec.Command[0], substituteAddr(spec.Command[1:], listener.Addr().String())...)
	if spec.Dir != "" {
		command.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		command.Env = append(os.Environ(), substituteAddr(spec.Env, listener.Addr().String())...)
	}
	configureProcess(command)
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("runtime: stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("runtime: start %q: %w", spec.Name, err)
	}
	acceptDeadline := spec.ConnectTimeout
	if acceptDeadline <= 0 {
		acceptDeadline = 10 * time.Second
	}
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(acceptDeadline))
	}
	conn, err := listener.Accept()
	if err != nil {
		killProcess(command)
		_ = command.Wait()
		return nil, fmt.Errorf("runtime: adapter %q did not connect: %w", spec.Name, err)
	}
	c := &client{
		cmd:      command,
		stdin:    conn,
		stdout:   conn,
		stderr:   &stderrBuffer{limit: 4096},
		pending:  make(map[int]chan Response),
		done:     make(chan struct{}),
		waitDone: make(chan struct{}),
		onEvent:  onEvent,
	}
	go c.drainStderr(stderr)
	go c.readLoop()
	go c.waitLoop()
	return c, nil
}

// substituteAddr replaces the {addr} placeholder in adapter arguments.
func substituteAddr(args []string, addr string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, "{addr}", addr)
	}
	return out
}

// requestFuture sends one request without waiting for the response, so the
// caller can continue the DAP handshake. Some adapters (Delve) answer launch
// only after configurationDone; waiting for the reply first deadlocks.
func (c *client) requestFuture(ctx context.Context, command string, arguments any) <-chan error {
	done := make(chan error, 1)
	go func() { done <- c.request(ctx, command, arguments, nil) }()
	return done
}

// initialize performs the initialize handshake.
func (c *client) initialize(ctx context.Context, adapterID string) (Capabilities, error) {
	var result struct {
		Capabilities Capabilities `json:"capabilities"`
	}
	if err := c.request(ctx, "initialize", map[string]any{
		"clientID":                     "ohgo",
		"clientName":                   "ohgo",
		"adapterID":                    adapterID,
		"pathFormat":                   "path",
		"linesStartAt1":                true,
		"columnsStartAt1":              true,
		"supportsVariableType":         true,
		"supportsVariablePaging":       false,
		"supportsRunInTerminalRequest": false,
	}, &result); err != nil {
		return Capabilities{}, err
	}
	c.mu.Lock()
	c.caps = result.Capabilities
	c.mu.Unlock()
	return result.Capabilities, nil
}

// readLoop dispatches responses and events.
func (c *client) readLoop() {
	reader := bufio.NewReaderSize(c.stdout, 64<<10)
	for {
		payload, err := wire.Read(reader)
		if err != nil {
			c.finish(err)
			return
		}
		var envelope struct {
			Seq    int    `json:"seq"`
			Type   string `json:"type"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			c.finish(fmt.Errorf("%w: decode message: %v", wire.ErrProtocol, err))
			return
		}
		switch envelope.Type {
		case typeResponse:
			var response Response
			if err := json.Unmarshal(payload, &response); err != nil {
				c.finish(fmt.Errorf("%w: decode response: %v", wire.ErrProtocol, err))
				return
			}
			c.handleResponse(response)
		case typeEvent:
			var event Event
			if err := json.Unmarshal(payload, &event); err != nil {
				c.finish(fmt.Errorf("%w: decode event: %v", wire.ErrProtocol, err))
				return
			}
			if c.onEvent != nil {
				c.onEvent(event)
			}
		case typeRequest:
			// A reverse request (runInTerminal, startDebugging) would create
			// an execution path outside the Execution Plane. Refuse it.
			var request Request
			if err := json.Unmarshal(payload, &request); err != nil {
				continue
			}
			c.respondError(request, "reverse requests are not supported; execution stays in the Execution Plane")
		default:
			// Other message types are protocol noise.
		}
	}
}

// waitLoop publishes process exit.
func (c *client) waitLoop() {
	err := c.cmd.Wait()
	c.mu.Lock()
	c.waitErr = err
	c.mu.Unlock()
	close(c.waitDone)
	if err != nil {
		c.finish(fmt.Errorf("runtime: debug adapter exited: %w", err))
		return
	}
	c.finish(ErrClosed)
}

func (c *client) drainStderr(stderr io.ReadCloser) {
	_, _ = io.Copy(c.stderr, io.LimitReader(stderr, 64<<10))
}

// finish publishes one terminal state and fails pending requests.
func (c *client) finish(err error) {
	c.finishOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.readErr = err
		pending := c.pending
		c.pending = make(map[int]chan Response)
		c.mu.Unlock()
		for _, channel := range pending {
			channel <- Response{Success: false, Message: err.Error()}
		}
		close(c.done)
	})
}

// handleResponse completes one pending request.
func (c *client) handleResponse(response Response) {
	c.mu.Lock()
	channel, ok := c.pending[response.RequestSeq]
	if ok {
		delete(c.pending, response.RequestSeq)
	}
	c.mu.Unlock()
	if ok {
		channel <- response
	}
}

// request sends one command and decodes its body.
func (c *client) request(ctx context.Context, command string, arguments any, result any) error {
	var encoded json.RawMessage
	if arguments != nil {
		payload, err := json.Marshal(arguments)
		if err != nil {
			return fmt.Errorf("runtime: encode %s arguments: %w", command, err)
		}
		encoded = payload
	}
	c.mu.Lock()
	if c.closed {
		readErr := c.readErr
		c.mu.Unlock()
		if readErr != nil {
			return readErr
		}
		return ErrClosed
	}
	c.nextSeq++
	seq := c.nextSeq
	channel := make(chan Response, 1)
	c.pending[seq] = channel
	c.mu.Unlock()

	if err := c.send(message{Type: typeRequest, Seq: seq, Command: command, Arguments: encoded}); err != nil {
		c.removePending(seq)
		return fmt.Errorf("runtime: send %s: %w", command, err)
	}
	select {
	case <-ctx.Done():
		c.removePending(seq)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: %s", ErrTimeout, command)
		}
		return ctx.Err()
	case <-c.done:
		c.removePending(seq)
		c.mu.Lock()
		readErr := c.readErr
		c.mu.Unlock()
		if readErr != nil {
			return readErr
		}
		return ErrClosed
	case response := <-channel:
		if !response.Success {
			message := response.Message
			if message == "" {
				message = "adapter refused " + command
			}
			return fmt.Errorf("runtime: %s: %s", command, message)
		}
		if result != nil && len(response.Body) > 0 {
			if err := json.Unmarshal(response.Body, result); err != nil {
				return fmt.Errorf("%w: decode %s body: %v", wire.ErrProtocol, command, err)
			}
		}
		return nil
	}
}

// message is one outbound envelope.
type message struct {
	Type       string          `json:"type"`
	Seq        int             `json:"seq"`
	Command    string          `json:"command,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	RequestSeq int             `json:"request_seq,omitempty"`
	Success    *bool           `json:"success,omitempty"`
	Message    string          `json:"message,omitempty"`
}

// send writes one message.
func (c *client) send(msg message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wire.Write(c.stdin, payload)
}

// respondError refuses one reverse request.
func (c *client) respondError(request Request, text string) {
	c.mu.Lock()
	c.nextSeq++
	seq := c.nextSeq
	c.mu.Unlock()
	success := false
	_ = c.send(message{
		Type: typeResponse, Seq: seq, RequestSeq: request.Seq,
		Success: &success, Message: text,
	})
}

func (c *client) removePending(seq int) {
	c.mu.Lock()
	delete(c.pending, seq)
	c.mu.Unlock()
}

// closeAdapter shuts the adapter down and reaps it.
func (c *client) closeAdapter(grace time.Duration) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.done
		return nil
	}
	c.mu.Unlock()
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(grace):
		killProcess(c.cmd)
		<-c.done
	}
	// The read loop may close done before the process reaper records the exit
	// status; cleanup must observe the reaped process, not an EOF race.
	select {
	case <-c.waitDone:
	case <-time.After(2 * time.Second):
	}
	return nil
}
