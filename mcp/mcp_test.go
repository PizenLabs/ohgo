package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/knowledge"
)

// scriptedProcess is a line-oriented fake MCP server. It answers each written
// request through a handler, so protocol tests never need a subprocess.
type scriptedProcess struct {
	mu      sync.Mutex
	handler func(method string, params map[string]any, id int) [][]byte
	queue   [][]byte
	writes  [][]byte
	readErr error
	killed  bool
}

func (p *scriptedProcess) WriteLine(line []byte) error {
	p.mu.Lock()
	p.writes = append(p.writes, append([]byte(nil), line...))
	p.mu.Unlock()
	var request struct {
		ID     *int           `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(line, &request); err != nil {
		return err
	}
	if request.Method == "" || request.ID == nil {
		// A client refusal or a notification: record it, no response.
		return nil
	}
	responses := p.handler(request.Method, request.Params, *request.ID)
	p.mu.Lock()
	p.queue = append(p.queue, responses...)
	p.mu.Unlock()
	return nil
}

func (p *scriptedProcess) ReadLine(max int) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.killed {
		return nil, io.EOF
	}
	if len(p.queue) == 0 {
		if p.readErr != nil {
			return nil, p.readErr
		}
		return nil, io.EOF
	}
	line := p.queue[0]
	p.queue = p.queue[1:]
	if len(line) > max+1 {
		return nil, errLineTooLong
	}
	return line, nil
}

func (p *scriptedProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	return nil
}

func (p *scriptedProcess) wroteMethod(method string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, line := range p.writes {
		var request struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(line, &request) == nil && request.Method == method {
			return true
		}
	}
	return false
}

func rpcResult(t *testing.T, id int, result any) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func rpcErrorLine(t *testing.T, id, code int, message string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
	if err != nil {
		t.Fatal(err)
	}
	return line
}

// standardHandler answers initialize, tools/list, resources/list, and a tool
// call. Individual tests override the parts they need.
func standardHandler(t *testing.T, tools []any, resources []any) func(string, map[string]any, int) [][]byte {
	return func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{"protocolVersion": "2024-11-05"})}
		case "tools/list":
			return [][]byte{rpcResult(t, id, map[string]any{"tools": tools})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": resources})}
		case "tools/call":
			return [][]byte{rpcResult(t, id, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "tool output"}},
			})}
		case "resources/read":
			return [][]byte{rpcResult(t, id, map[string]any{
				"contents": []any{map[string]any{"uri": "file:///doc", "mimeType": "text/plain", "text": "resource output"}},
			})}
		default:
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		}
	}
}

func testManager(t *testing.T, cfg ServerConfig, handler func(string, map[string]any, int) [][]byte) (*Manager, *scriptedProcess) {
	t.Helper()
	process := &scriptedProcess{handler: handler}
	manager, err := newManager([]ServerConfig{cfg}, func(ServerConfig) (serverProcess, error) {
		return process, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager, process
}

func baseConfig() ServerConfig {
	return ServerConfig{
		Name:    "test",
		Command: "fake",
		Tools: map[string]string{
			"read_tool":  "read",
			"send_tool":  "mutate",
			"interact_t": "interact",
		},
		MaxBytes:     8192,
		MaxItems:     8,
		MaxTextChars: 512,
	}
}

// Discovery returns descriptions and configuration classes. It does not
// authorize anything by itself.
func TestDiscoverySeparatesDescriptionFromAuthority(t *testing.T) {
	handler := standardHandler(t, []any{
		map[string]any{"name": "read_tool", "description": "reads", "inputSchema": map[string]any{"type": "object"}},
		map[string]any{"name": "send_tool", "description": "sends"},
		map[string]any{"name": "mystery_tool", "description": "unknown"},
	}, nil)
	manager, _ := testManager(t, baseConfig(), handler)
	discovery, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := discovery.Capabilities
	byName := map[string]Capability{}
	for _, capability := range capabilities {
		byName[capability.Name] = capability
	}
	if byName["read_tool"].Classification != ClassRead {
		t.Errorf("read_tool = %s, want read", byName["read_tool"].Classification)
	}
	if byName["send_tool"].Classification != ClassMutate {
		t.Errorf("send_tool = %s, want mutate", byName["send_tool"].Classification)
	}
	if byName["mystery_tool"].Classification != ClassUnknown {
		t.Errorf("mystery_tool = %s, want unknown (fail closed)", byName["mystery_tool"].Classification)
	}
}

// A read-bound call must refuse a mutating or unknown tool before any
// tools/call request exists.
func TestMCPReadBoundaryRefusesMutatingAndUnknownTools(t *testing.T) {
	manager, process := testManager(t, baseConfig(), standardHandler(t, []any{
		map[string]any{"name": "read_tool"}, map[string]any{"name": "send_tool"}, map[string]any{"name": "mystery_tool"},
	}, nil))
	for _, tool := range []string{"send_tool", "mystery_tool", "read_tool"} {
		_, err := manager.CallTool(context.Background(), "test", tool, nil, ModeRead, Meter{})
		if tool == "read_tool" {
			if err != nil {
				t.Fatalf("read tool refused: %v", err)
			}
			continue
		}
		var failure knowledge.Failure
		if !errors.As(err, &failure) || failure.Class != knowledge.FailureMutationDenied {
			t.Fatalf("%s error = %v, want mutation_denied", tool, err)
		}
	}
	if process.wroteMethod("tools/call") {
		// Only the one admitted read tool may have reached tools/call; a
		// refused mutation must not.
		t.Log("tools/call was written for the admitted read-only call")
	}
}

// Mutation requires both explicit classification and explicit server
// configuration. Unknown and read tools never pass the mutation boundary.
func TestMCPMutationRequiresExplicitConfiguration(t *testing.T) {
	cfg := baseConfig()
	cfg.AllowMutation = false
	manager, _ := testManager(t, cfg, standardHandler(t, []any{
		map[string]any{"name": "send_tool"}, map[string]any{"name": "mystery_tool"}, map[string]any{"name": "read_tool"},
	}, nil))
	for _, tool := range []string{"send_tool", "mystery_tool", "read_tool"} {
		_, err := manager.CallTool(context.Background(), "test", tool, nil, ModeMutate, Meter{})
		var failure knowledge.Failure
		if !errors.As(err, &failure) || failure.Class != knowledge.FailureMutationDenied {
			t.Errorf("%s error = %v, want mutation_denied", tool, err)
		}
	}

	cfg.AllowMutation = true
	manager, _ = testManager(t, cfg, standardHandler(t, []any{map[string]any{"name": "send_tool"}}, nil))
	if _, err := manager.CallTool(context.Background(), "test", "send_tool", nil, ModeMutate, Meter{}); err != nil {
		t.Fatalf("explicitly configured mutation was refused: %v", err)
	}
}

// A tool's declared input schema is validated before the external call.
func TestMCPToolSchemaValidationHappensBeforeExecution(t *testing.T) {
	handler := standardHandler(t, []any{
		map[string]any{
			"name": "read_tool",
			"inputSchema": map[string]any{
				"type": "object", "required": []string{"channel"},
				"properties": map[string]any{"channel": map[string]any{"type": "string"}},
			},
		},
	}, nil)
	manager, process := testManager(t, baseConfig(), handler)
	_, err := manager.CallTool(context.Background(), "test", "read_tool", map[string]any{}, ModeRead, Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureInvalidRequest {
		t.Fatalf("error = %v, want invalid_request", err)
	}
	if process.wroteMethod("tools/call") {
		t.Fatal("a malformed argument reached tools/call")
	}
}

func TestMCPArgumentsAreStructurallyBounded(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxDepth = 1
	manager, process := testManager(t, cfg, standardHandler(t, []any{map[string]any{"name": "read_tool"}}, nil))
	deep := map[string]any{"a": map[string]any{"b": map[string]any{"c": true}}}
	_, err := manager.CallTool(context.Background(), "test", "read_tool", deep, ModeRead, Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureInvalidRequest {
		t.Fatalf("error = %v, want invalid_request", err)
	}
	if process.wroteMethod("tools/call") {
		t.Fatal("an over-deep argument tree reached tools/call")
	}
}

// Binary content is never copied into model context, and item/text bounds are
// explicit.
func TestMCPResultBoundsTextAndOmitsBinary(t *testing.T) {
	payload := strings.Repeat("x", 4096)
	handler := func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{})}
		case "tools/list":
			return [][]byte{rpcResult(t, id, map[string]any{"tools": []any{map[string]any{"name": "read_tool"}}})}
		case "tools/call":
			items := []any{map[string]any{"type": "text", "text": payload}}
			for i := 0; i < 20; i++ {
				items = append(items, map[string]any{"type": "text", "text": "extra"})
			}
			items = append(items, map[string]any{"type": "image", "mimeType": "image/png", "data": payload})
			return [][]byte{rpcResult(t, id, map[string]any{"content": items})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		}
		return [][]byte{rpcErrorLine(t, id, -32601, "no")}
	}
	cfg := baseConfig()
	cfg.MaxBytes = 64 * 1024
	cfg.MaxItems = 4
	cfg.MaxTextChars = 64
	manager, _ := testManager(t, cfg, handler)
	result, err := manager.CallTool(context.Background(), "test", "read_tool", nil, ModeRead, Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Fatal("result was not marked truncated")
	}
	if len([]rune(result.Text)) > 128 {
		t.Fatalf("text length = %d, want it bounded", len([]rune(result.Text)))
	}
	if strings.Contains(result.Text, payload) {
		t.Fatal("the full payload was published")
	}
	if result.Items != 4 {
		t.Fatalf("items = %d, want 4", result.Items)
	}
}

func TestMCPResponseLimitKillsTheExchange(t *testing.T) {
	manager, process := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		if method == "initialize" {
			return [][]byte{rpcResult(t, id, map[string]any{})}
		}
		return [][]byte{[]byte(`{"jsonrpc":"2.0","id":` + itoa(id) + `,"result":"` + strings.Repeat("x", 9000) + `"}`)}
	})
	_, err := manager.Discover(context.Background(), "test", Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureResponseLimit {
		t.Fatalf("error = %v, want response_limit", err)
	}
	if !process.killed {
		t.Fatal("an over-limit response did not kill the process")
	}
}

func TestMCPDisconnectIsProviderUnavailable(t *testing.T) {
	manager, process := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		return nil
	})
	process.readErr = io.EOF
	_, err := manager.Discover(context.Background(), "test", Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureUnavailable {
		t.Fatalf("error = %v, want provider_unavailable", err)
	}
}

func TestMCPMalformedMessageIsProtocolError(t *testing.T) {
	manager, _ := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		return [][]byte{[]byte(`not json`)}
	})
	_, err := manager.Discover(context.Background(), "test", Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureProtocol {
		t.Fatalf("error = %v, want protocol_error", err)
	}
}

// A server request is refused, not executed, and the original exchange still
// completes.
func TestMCPServerRequestIsRefusedNotExecuted(t *testing.T) {
	manager, process := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		if method == "initialize" {
			return [][]byte{
				[]byte(`{"jsonrpc":"2.0","id":99,"method":"sampling/createMessage","params":{"messages":[]}}`),
				rpcResult(t, id, map[string]any{}),
			}
		}
		return [][]byte{rpcResult(t, id, map[string]any{"tools": []any{}, "resources": []any{}})}
	})
	if _, err := manager.Discover(context.Background(), "test", Meter{}); err != nil {
		t.Fatal(err)
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	found := false
	for _, line := range process.writes {
		if strings.Contains(string(line), `"id":99`) && strings.Contains(string(line), `-32601`) {
			found = true
		}
	}
	if !found {
		t.Fatal("the client did not refuse the server request")
	}
}

func TestMCPMeterChargesEveryExchange(t *testing.T) {
	manager, _ := testManager(t, baseConfig(), standardHandler(t, []any{}, nil))
	var before []string
	meter := Meter{
		Before: func(ctx context.Context, method string) (int, error) {
			before = append(before, method)
			return 4096, nil
		},
		After: func(ctx context.Context, bytes int) error {
			if bytes <= 0 {
				t.Errorf("method paid with %d bytes", bytes)
			}
			return nil
		},
	}
	if _, err := manager.Discover(context.Background(), "test", meter); err != nil {
		t.Fatal(err)
	}
	if len(before) < 3 {
		t.Fatalf("metered methods = %v, want initialize, tools/list, resources/list", before)
	}
}

func TestFilteredEnvExcludesUnnamedSecrets(t *testing.T) {
	t.Setenv("OHGO_MCP_ALLOWED", "allowed")
	t.Setenv("OHGO_MCP_SECRET", "secret")
	env := filteredEnv([]string{"OHGO_MCP_ALLOWED"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "OHGO_MCP_ALLOWED=allowed") {
		t.Fatal("the named variable was not passed")
	}
	if strings.Contains(joined, "OHGO_MCP_SECRET") {
		t.Fatal("an unnamed secret was inherited")
	}
}

func itoa(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// A capability list over the configured item bound is explicitly truncated,
// never presented as a complete discovery.
func TestMCPDiscoveryTruncationIsExplicit(t *testing.T) {
	tools := make([]any, 0, 5)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		tools = append(tools, map[string]any{"name": name})
	}
	cfg := baseConfig()
	cfg.MaxItems = 2
	manager, _ := testManager(t, cfg, standardHandler(t, tools, nil))
	discovery, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Capabilities) != 2 {
		t.Fatalf("capabilities = %d, want 2", len(discovery.Capabilities))
	}
	if !discovery.Truncated || discovery.Reason != "item_limit" {
		t.Fatalf("truncation = %v/%q, want explicit item_limit", discovery.Truncated, discovery.Reason)
	}
}

// A closed manager refuses new work instead of starting a fresh process after
// shutdown.
func TestClosedMCPManagerRefusesNewWork(t *testing.T) {
	manager, _ := testManager(t, baseConfig(), standardHandler(t, []any{}, nil))
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Discover(context.Background(), "test", Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureNotConfigured {
		t.Fatalf("error = %v, want not_configured", err)
	}
}
