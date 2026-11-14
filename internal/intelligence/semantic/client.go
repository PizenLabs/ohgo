package semantic

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
)

// Client errors.
var (
	// ErrUnsupportedCapability reports a request for a capability the server
	// did not advertise. It is a valid answer, not a failure to simulate.
	ErrUnsupportedCapability = errors.New("semantic: server does not support the capability")
	// ErrTimeout reports a request that exceeded its deadline.
	ErrTimeout = errors.New("semantic: request timed out")
	// ErrClosed reports a client whose process or stream is gone.
	ErrClosed = errors.New("semantic: language server is closed")
)

// Client is one JSON-RPC language server connection.
//
// It owns exactly one process and one document-synchronization stream. Requests
// are correlated, bounded by the caller's context, and cancelled with
// $/cancelRequest on deadline. It never applies workspace edits: a server
// request for workspace/applyEdit receives an error response and the edit is
// discarded.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *boundedBuffer

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[string]chan message
	caps    Capabilities
	// diagnostics is the latest push-diagnostics batch per URI.
	diagnostics map[string]PublishDiagnostics
	versions    map[string]int
	texts       map[string]string
	closing     bool
	closed      bool
	readErr     error

	done       chan struct{}
	finishOnce sync.Once
	waitErr    error
}

// boundedBuffer keeps the last N bytes of the server's stderr for failure
// explanation, without unbounded growth.
type boundedBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// WaitError blocks until the read loop is finished and reports the process
// exit error. It is intended for tests and diagnostics after the client is
// known to be stopped.
func (c *Client) WaitError() error {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitErr
}

// Stderr returns the bounded server stderr text.
func (c *Client) Stderr() string { return c.stderr.String() }

// Capabilities returns the negotiated capability set.
func (c *Client) Capabilities() Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// Supports reports whether method was advertised.
func (c *Client) Supports(method string) bool { return c.Capabilities().Supports(method) }

// StartClient launches spec and performs the initialize handshake.
//
// The process is not bound to ctx: the manager owns its lifetime. ctx bounds
// only the handshake, so a request-level deadline cannot kill a server that
// other requests are using.
func StartClient(ctx context.Context, root string, spec Spec) (*Client, error) {
	if len(spec.Command) == 0 {
		return nil, errors.New("semantic: language server has no command")
	}
	command := exec.Command(spec.Command[0], spec.Command[1:]...)
	command.Dir = root
	if len(spec.Env) > 0 {
		command.Env = append(os.Environ(), spec.Env...)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("semantic: stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("semantic: stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("semantic: stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("semantic: start %q: %w", spec.Name, err)
	}
	client := &Client{
		cmd:         command,
		stdin:       stdin,
		stdout:      stdout,
		stderr:      newBoundedBuffer(4096),
		pending:     make(map[string]chan message),
		diagnostics: make(map[string]PublishDiagnostics),
		versions:    make(map[string]int),
		texts:       make(map[string]string),
		done:        make(chan struct{}),
	}
	go client.drainStderr(stderr)
	go client.readLoop()
	go client.waitLoop()

	startTimeout := spec.StartTimeout
	if startTimeout <= 0 {
		startTimeout = 20 * time.Second
	}
	startCtx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if err := client.initialize(startCtx, root, spec); err != nil {
		client.kill()
		return nil, err
	}
	return client, nil
}

// initialize performs the LSP initialize/initialized handshake.
func (c *Client) initialize(ctx context.Context, root string, spec Spec) error {
	type folder struct {
		URI  string `json:"uri"`
		Name string `json:"name"`
	}
	params := map[string]any{
		"processId":        os.Getpid(),
		"clientInfo":       map[string]string{"name": "ohgo", "version": "1"},
		"rootUri":          FileURI(root),
		"workspaceFolders": []folder{{URI: FileURI(root), Name: "workspace"}},
		"capabilities": map[string]any{
			"workspace": map[string]any{
				"workspaceFolders": true,
				"configuration":    true,
			},
			"textDocument": map[string]any{
				"synchronization": map[string]any{"dynamicRegistration": false},
				"definition":      map[string]any{"linkSupport": true},
				"references":      map[string]any{},
				"documentSymbol":  map[string]any{"hierarchicalDocumentSymbolSupport": true},
				"hover":           map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"callHierarchy":   map[string]any{},
				"diagnostic":      map[string]any{},
			},
			"window": map[string]any{"workDoneProgress": false},
		},
	}
	if len(spec.InitializationOptions) > 0 {
		params["initializationOptions"] = spec.InitializationOptions
	}
	var result struct {
		Capabilities serverCapabilities `json:"capabilities"`
		ServerInfo   struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := c.call(ctx, MethodInitialize, params, &result); err != nil {
		return fmt.Errorf("semantic: initialize %q: %w", spec.Name, err)
	}
	c.mu.Lock()
	c.caps = Capabilities{
		Definition:       enabled(result.Capabilities.DefinitionProvider),
		References:       enabled(result.Capabilities.ReferencesProvider),
		DocumentSymbols:  enabled(result.Capabilities.DocumentSymbol),
		WorkspaceSymbols: enabled(result.Capabilities.WorkspaceSymbol),
		Hover:            enabled(result.Capabilities.HoverProvider),
		CallHierarchy:    enabled(result.Capabilities.CallHierarchy),
		TypeHierarchy:    enabled(result.Capabilities.TypeHierarchy),
		PullDiagnostics:  enabled(result.Capabilities.DiagnosticProvider),
		SyncKind:         syncKind(result.Capabilities.TextDocumentSync),
	}
	c.mu.Unlock()
	if err := c.notify(MethodInitialized, map[string]any{}); err != nil {
		return fmt.Errorf("semantic: initialized notification: %w", err)
	}
	return nil
}

// readLoop dispatches every inbound message until the stream ends.
func (c *Client) readLoop() {
	reader := bufio.NewReaderSize(c.stdout, 64<<10)
	for {
		payload, err := readMessage(reader)
		if err != nil {
			c.finish(err)
			return
		}
		var msg message
		if err := json.Unmarshal(payload, &msg); err != nil {
			c.finish(fmt.Errorf("%w: decode payload: %v", ErrTransport, err))
			return
		}
		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			c.handleServerRequest(msg)
		case msg.Method != "":
			c.handleNotification(msg)
		default:
			c.handleResponse(msg)
		}
	}
}

// waitLoop observes process exit so a crash is never mistaken for an idle
// server.
func (c *Client) waitLoop() {
	err := c.cmd.Wait()
	c.mu.Lock()
	c.waitErr = err
	c.mu.Unlock()
	if err != nil {
		c.finish(fmt.Errorf("semantic: language server exited: %w", err))
		return
	}
	c.finish(ErrClosed)
}

// drainStderr keeps the tail of server diagnostics.
func (c *Client) drainStderr(stderr io.ReadCloser) {
	_, _ = io.Copy(c.stderr, io.LimitReader(stderr, 64<<10))
}

// finish publishes one terminal state and fails every pending request.
func (c *Client) finish(err error) {
	c.finishOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.readErr = err
		pending := c.pending
		c.pending = make(map[string]chan message)
		c.mu.Unlock()
		for _, channel := range pending {
			channel <- message{Error: &rpcError{Code: -32000, Message: err.Error()}}
		}
		close(c.done)
	})
}

// handleResponse completes one pending request.
func (c *Client) handleResponse(msg message) {
	key := idKey(msg.ID)
	c.mu.Lock()
	channel, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	if ok {
		channel <- msg
	}
}

// handleNotification stores diagnostics and ignores progress noise.
func (c *Client) handleNotification(msg message) {
	if msg.Method != MethodPublishDiagnostic {
		return
	}
	var published PublishDiagnostics
	if err := json.Unmarshal(msg.Params, &published); err != nil {
		return
	}
	if len(published.Diagnostics) > 500 {
		published.Diagnostics = published.Diagnostics[:500]
	}
	c.mu.Lock()
	c.diagnostics[published.URI] = published
	c.mu.Unlock()
}

// handleServerRequest answers the few client-bound requests this client
// supports. Edit-producing requests are refused by contract.
func (c *Client) handleServerRequest(msg message) {
	switch msg.Method {
	case MethodApplyEdit:
		c.respond(msg.ID, nil, &rpcError{
			Code:    -32601,
			Message: "ohgo semantic intelligence does not apply workspace edits; edits require the authorized mutation path",
		})
	case MethodConfiguration:
		var params struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		result := make([]any, len(params.Items))
		c.respond(msg.ID, result, nil)
	case MethodRegisterCapabilty:
		c.respond(msg.ID, nil, nil)
	default:
		c.respond(msg.ID, nil, &rpcError{Code: -32601, Message: "unsupported client request " + msg.Method})
	}
}

// respond writes one response to a server request.
func (c *Client) respond(id json.RawMessage, result any, rpcErr *rpcError) {
	response := message{JSONRPC: "2.0", ID: id}
	if rpcErr != nil {
		response.Error = rpcErr
	} else {
		encoded, err := json.Marshal(result)
		if err != nil {
			response.Error = &rpcError{Code: -32603, Message: err.Error()}
		} else {
			response.Result = encoded
		}
	}
	_ = c.send(response)
}

// send marshals and writes one message.
func (c *Client) send(msg message) error {
	payload, err := encodeMessage(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeMessage(c.stdin, payload)
}

// Alive reports whether the process and read loop are still usable.
func (c *Client) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}

// call performs one request and waits for its response.
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	return c.callWith(ctx, method, params, result, false)
}

// callWith optionally permits a request while closing, which shutdown needs.
func (c *Client) callWith(ctx context.Context, method string, params any, result any, allowClosing bool) error {
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("%w: encode %s params: %v", ErrTransport, method, err)
	}
	c.mu.Lock()
	if c.closed || (c.closing && !allowClosing) {
		readErr := c.readErr
		c.mu.Unlock()
		if readErr != nil {
			return readErr
		}
		return ErrClosed
	}
	c.nextID++
	id := c.nextID
	key := strconv.FormatInt(id, 10)
	channel := make(chan message, 1)
	c.pending[key] = channel
	c.mu.Unlock()

	request := message{JSONRPC: "2.0", ID: json.RawMessage(key), Method: method, Params: encodedParams}
	if err := c.send(request); err != nil {
		c.removePending(key)
		return fmt.Errorf("semantic: send %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		_ = c.notify(MethodCancelRequest, map[string]any{"id": id})
		c.removePending(key)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: %s", ErrTimeout, method)
		}
		return ctx.Err()
	case <-c.done:
		c.removePending(key)
		c.mu.Lock()
		readErr := c.readErr
		c.mu.Unlock()
		if readErr != nil {
			return readErr
		}
		return ErrClosed
	case response := <-channel:
		if response.Error != nil {
			return fmt.Errorf("semantic: %s: %w", method, response.Error)
		}
		if result != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, result); err != nil {
				return fmt.Errorf("%w: decode %s result: %v", ErrTransport, method, err)
			}
		}
		return nil
	}
}

// removePending drops one request channel.
func (c *Client) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

// notify sends a notification with no response.
func (c *Client) notify(method string, params any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(message{JSONRPC: "2.0", Method: method, Params: encoded})
}

// OpenDocument sends didOpen and records the version.
func (c *Client) OpenDocument(uri, languageID, text string) error {
	caps := c.Capabilities()
	if caps.SyncKind == 0 {
		return nil
	}
	c.mu.Lock()
	version := c.versions[uri] + 1
	c.versions[uri] = version
	c.texts[uri] = text
	c.mu.Unlock()
	return c.notify(MethodDidOpen, map[string]any{
		"textDocument": DocumentItem{URI: uri, LanguageID: languageID, Version: version, Text: text},
	})
}

// ChangeDocument sends a bounded full or whole-range replacement.
//
// A server that advertises incremental synchronization receives one change
// covering the previous document, which is the correct incremental encoding of
// "this content replaced that content". The client never maintains a diff
// because a wrong range would corrupt the server's view.
func (c *Client) ChangeDocument(uri, text string) error {
	caps := c.Capabilities()
	if caps.SyncKind == 0 {
		return nil
	}
	c.mu.Lock()
	version := c.versions[uri] + 1
	c.versions[uri] = version
	previous := c.texts[uri]
	c.texts[uri] = text
	c.mu.Unlock()
	change := map[string]any{"text": text}
	if caps.SyncKind == 2 && previous != "" {
		end := endPosition(previous)
		change["range"] = Range{Start: Position{}, End: end}
	}
	return c.notify(MethodDidChange, map[string]any{
		"textDocument":   VersionedTextDocumentIdentifier{URI: uri, Version: version},
		"contentChanges": []any{change},
	})
}

// CloseDocument sends didClose and forgets the document state.
func (c *Client) CloseDocument(uri string) error {
	c.mu.Lock()
	delete(c.versions, uri)
	delete(c.texts, uri)
	c.mu.Unlock()
	if c.Capabilities().SyncKind == 0 {
		return nil
	}
	return c.notify(MethodDidClose, map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
	})
}

// endPosition converts a text's end to an LSP position in UTF-16 units.
func endPosition(text string) Position {
	var position Position
	for _, r := range text {
		if r == '\n' {
			position.Line++
			position.Character = 0
			continue
		}
		if r == '\r' {
			continue
		}
		if r > 0xFFFF {
			position.Character += 2
			continue
		}
		position.Character++
	}
	return position
}

// Definition resolves the definition at pos.
func (c *Client) Definition(ctx context.Context, uri string, pos Position) ([]Location, error) {
	if !c.caps.Supports(MethodDefinition) {
		return nil, ErrUnsupportedCapability
	}
	var raw json.RawMessage
	if err := c.call(ctx, MethodDefinition, PositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     pos,
	}, &raw); err != nil {
		return nil, err
	}
	return decodeLocations(raw), nil
}

// References resolves references at pos.
func (c *Client) References(ctx context.Context, uri string, pos Position, includeDeclaration bool) ([]Location, error) {
	if !c.caps.Supports(MethodReferences) {
		return nil, ErrUnsupportedCapability
	}
	params := ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     pos,
	}
	params.Context.IncludeDeclaration = includeDeclaration
	var raw json.RawMessage
	if err := c.call(ctx, MethodReferences, params, &raw); err != nil {
		return nil, err
	}
	return decodeLocations(raw), nil
}

// DocumentSymbols returns normalized symbols for one document.
func (c *Client) DocumentSymbols(ctx context.Context, uri string) ([]Symbol, error) {
	if !c.caps.Supports(MethodDocumentSymbol) {
		return nil, ErrUnsupportedCapability
	}
	var raw json.RawMessage
	if err := c.call(ctx, MethodDocumentSymbol, map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
	}, &raw); err != nil {
		return nil, err
	}
	var hierarchical []DocumentSymbol
	if err := json.Unmarshal(raw, &hierarchical); err == nil && len(hierarchical) > 0 {
		out := make([]Symbol, 0, len(hierarchical))
		var walk func(items []DocumentSymbol, container string)
		walk = func(items []DocumentSymbol, container string) {
			for _, item := range items {
				out = append(out, Symbol{
					Name: item.Name, Detail: item.Detail, Kind: item.Kind,
					Container: container, URI: uri, Range: item.SelectionRange,
				})
				walk(item.Children, item.Name)
			}
		}
		walk(hierarchical, "")
		return out, nil
	}
	var flat []SymbolInformation
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("%w: decode document symbols: %v", ErrTransport, err)
	}
	out := make([]Symbol, 0, len(flat))
	for _, item := range flat {
		out = append(out, Symbol{
			Name: item.Name, Kind: item.Kind, Container: item.ContainerName,
			URI: item.Location.URI, Range: item.Location.Range,
		})
	}
	return out, nil
}

// WorkspaceSymbols queries the workspace symbol index.
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]Symbol, error) {
	if !c.caps.Supports(MethodWorkspaceSymbol) {
		return nil, ErrUnsupportedCapability
	}
	var flat []SymbolInformation
	if err := c.call(ctx, MethodWorkspaceSymbol, map[string]any{"query": query}, &flat); err != nil {
		return nil, err
	}
	if len(flat) > 200 {
		flat = flat[:200]
	}
	out := make([]Symbol, 0, len(flat))
	for _, item := range flat {
		out = append(out, Symbol{
			Name: item.Name, Kind: item.Kind, Container: item.ContainerName,
			URI: item.Location.URI, Range: item.Location.Range,
		})
	}
	return out, nil
}

// Hover returns the server's hover answer.
func (c *Client) Hover(ctx context.Context, uri string, pos Position) (*Hover, error) {
	if !c.caps.Supports(MethodHover) {
		return nil, ErrUnsupportedCapability
	}
	var hover Hover
	if err := c.call(ctx, MethodHover, PositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     pos,
	}, &hover); err != nil {
		return nil, err
	}
	return &hover, nil
}

// PrepareCallHierarchy returns the callable item at pos.
func (c *Client) PrepareCallHierarchy(ctx context.Context, uri string, pos Position) ([]CallHierarchyItem, error) {
	if !c.caps.Supports(MethodPrepareCallHier) {
		return nil, ErrUnsupportedCapability
	}
	var items []CallHierarchyItem
	if err := c.call(ctx, MethodPrepareCallHier, PositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     pos,
	}, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// IncomingCalls returns callers of item.
func (c *Client) IncomingCalls(ctx context.Context, item CallHierarchyItem) ([]IncomingCall, error) {
	if !c.caps.Supports(MethodIncomingCalls) {
		return nil, ErrUnsupportedCapability
	}
	var calls []IncomingCall
	if err := c.call(ctx, MethodIncomingCalls, map[string]any{"item": item}, &calls); err != nil {
		return nil, err
	}
	return calls, nil
}

// OutgoingCalls returns callees of item.
func (c *Client) OutgoingCalls(ctx context.Context, item CallHierarchyItem) ([]OutgoingCall, error) {
	if !c.caps.Supports(MethodOutgoingCalls) {
		return nil, ErrUnsupportedCapability
	}
	var calls []OutgoingCall
	if err := c.call(ctx, MethodOutgoingCalls, map[string]any{"item": item}, &calls); err != nil {
		return nil, err
	}
	return calls, nil
}

// Diagnostics returns the latest pushed diagnostics for one URI.
func (c *Client) Diagnostics(uri string) []Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	published, ok := c.diagnostics[uri]
	if !ok {
		return nil
	}
	return append([]Diagnostic(nil), published.Diagnostics...)
}

// PullDiagnostics requests diagnostics when the server supports pull
// diagnostics.
func (c *Client) PullDiagnostics(ctx context.Context, uri string) ([]Diagnostic, error) {
	if !c.caps.Supports(MethodDiagnostic) {
		return nil, ErrUnsupportedCapability
	}
	var report struct {
		Kind  string       `json:"kind"`
		Items []Diagnostic `json:"items"`
	}
	if err := c.call(ctx, MethodDiagnostic, map[string]any{
		"textDocument": TextDocumentIdentifier{URI: uri},
	}, &report); err != nil {
		return nil, err
	}
	return report.Items, nil
}

// Close shuts the server down and reaps the process.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closing || c.closed {
		c.mu.Unlock()
		<-c.done
		return nil
	}
	c.closing = true
	c.mu.Unlock()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = c.callWith(shutdownCtx, MethodShutdown, nil, nil, true)
	cancel()
	_ = c.notify(MethodExit, nil)
	_ = c.stdin.Close()

	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		c.kill()
		<-c.done
	}
	return nil
}

// kill terminates the process group and waits for the read loop.
func (c *Client) kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	<-c.done
}

// decodeLocations normalizes Location, []Location, and LocationLink results.
func decodeLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []Location
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var single Location
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return []Location{single}
	}
	var links []struct {
		TargetURI   string `json:"targetUri"`
		TargetRange Range  `json:"targetSelectionRange"`
	}
	if err := json.Unmarshal(raw, &links); err == nil {
		out := make([]Location, 0, len(links))
		for _, link := range links {
			out = append(out, Location{URI: link.TargetURI, Range: link.TargetRange})
		}
		return out
	}
	return nil
}

// idKey normalizes a raw JSON-RPC id for correlation.
func idKey(raw json.RawMessage) string {
	return strings.TrimSpace(string(bytes.TrimSpace(raw)))
}
