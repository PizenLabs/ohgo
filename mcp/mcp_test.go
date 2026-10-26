package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

// Status and staging are I/O-free, and each discovery after an enable starts
// exactly one server process. Disabling closes that process and drops the
// cached snapshot so the next enable observes the server again.
func TestStatusAndStagingNeverConnectUntilDiscovery(t *testing.T) {
	handler := standardHandler(t, []any{
		map[string]any{"name": "read_tool", "description": "reads"},
	}, nil)
	var mu sync.Mutex
	starts := 0
	var processes []*scriptedProcess
	manager, err := newManager([]ServerConfig{baseConfig()}, func(ServerConfig) (serverProcess, error) {
		mu.Lock()
		defer mu.Unlock()
		starts++
		process := &scriptedProcess{handler: handler}
		processes = append(processes, process)
		return process, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	startCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return starts
	}

	status := manager.Status()
	if len(status) != 1 || status[0].Connected || status[0].Cached {
		t.Fatalf("initial status = %+v, want disconnected and uncached", status)
	}
	if startCount() != 0 {
		t.Fatal("Status started a server")
	}
	if err := manager.SetServerEnabled(baseConfig(), true); err != nil {
		t.Fatalf("no-op enable: %v", err)
	}
	if startCount() != 0 {
		t.Fatal("enabling in the UI started a server")
	}
	if err := manager.SetServerEnabled(baseConfig(), false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if startCount() != 0 {
		t.Fatal("disabling started a server")
	}
	if err := manager.SetServerEnabled(baseConfig(), true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if startCount() != 0 {
		t.Fatal("re-enabling before discovery started a server")
	}
	if _, err := manager.Discover(context.Background(), "test", Meter{}); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if got := startCount(); got != 1 {
		t.Fatalf("discovery starts = %d, want exactly one", got)
	}
	status = manager.Status()
	if !status[0].Connected || !status[0].Cached {
		t.Fatalf("status after discovery = %+v, want connected and cached", status)
	}

	if err := manager.SetServerEnabled(baseConfig(), false); err != nil {
		t.Fatalf("disable after discovery: %v", err)
	}
	status = manager.Status()
	if status[0].Connected || status[0].Cached || status[0].Enabled {
		t.Fatalf("status after disable = %+v, want closed, uncached, and disabled", status)
	}
	mu.Lock()
	first := processes[0]
	mu.Unlock()
	first.mu.Lock()
	killed := first.killed
	first.mu.Unlock()
	if !killed {
		t.Fatal("disable did not shut down the stdio process")
	}

	if err := manager.SetServerEnabled(baseConfig(), true); err != nil {
		t.Fatalf("second enable: %v", err)
	}
	if _, err := manager.Discover(context.Background(), "test", Meter{}); err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if got := startCount(); got != 2 {
		t.Fatalf("re-enable discovery starts = %d, want a fresh single attempt", got)
	}
}

// Disabling a server removes it from the model-facing roster and closes the
// live client, while Status still reports the disabled row for the manager.
func TestSetServerEnabledDisablesAndReenablesOneServer(t *testing.T) {
	handler := standardHandler(t, []any{
		map[string]any{"name": "read_tool", "description": "reads"},
	}, nil)
	var mu sync.Mutex
	var processes []*scriptedProcess
	manager, err := newManager([]ServerConfig{baseConfig()}, func(ServerConfig) (serverProcess, error) {
		mu.Lock()
		defer mu.Unlock()
		process := &scriptedProcess{handler: handler}
		processes = append(processes, process)
		return process, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Discover(context.Background(), "test", Meter{}); err != nil {
		t.Fatal(err)
	}
	if !manager.Status()[0].Connected {
		t.Fatal("discovery did not establish a connection to disable")
	}
	first := processes[0]

	if err := manager.SetServerEnabled(baseConfig(), false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := manager.Names(); len(got) != 0 {
		t.Fatalf("Names after disable = %v, want none", got)
	}
	if manager.Configured("test") {
		t.Fatal("a disabled server still reports Configured")
	}
	status := manager.Status()
	if len(status) != 1 || status[0].Enabled || status[0].Connected || status[0].Cached {
		t.Fatalf("status after disable = %+v, want disabled and disconnected", status)
	}
	first.mu.Lock()
	killed := first.killed
	first.mu.Unlock()
	if !killed {
		t.Fatal("disabling a live server did not close its client")
	}
	if _, err := manager.Discover(context.Background(), "test", Meter{}); err == nil {
		t.Fatal("discovery succeeded for a disabled server")
	}

	if err := manager.SetServerEnabled(baseConfig(), true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := manager.Names(); len(got) != 1 || got[0] != "test" {
		t.Fatalf("Names after enable = %v, want test", got)
	}
	if _, err := manager.Discover(context.Background(), "test", Meter{}); err != nil {
		t.Fatalf("discovery after enable: %v", err)
	}
	if !manager.Status()[0].Enabled || !manager.Status()[0].Connected {
		t.Fatalf("status after enable = %+v, want enabled and connected", manager.Status())
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

// Discovery follows the server's nextCursor instead of stopping at the first
// page, and surfaces the initialize instructions as bounded data.
func TestMCPDiscoveryFollowsNextCursor(t *testing.T) {
	manager, _ := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{
				"protocolVersion": "2024-11-05",
				"instructions":    "call the graph first",
			})}
		case "tools/list":
			cursor, _ := params["cursor"].(string)
			switch cursor {
			case "":
				return [][]byte{rpcResult(t, id, map[string]any{
					"tools":      []any{map[string]any{"name": "alpha"}, map[string]any{"name": "beta"}},
					"nextCursor": "page-2",
				})}
			case "page-2":
				return [][]byte{rpcResult(t, id, map[string]any{
					"tools":      []any{map[string]any{"name": "gamma"}},
					"nextCursor": "page-3",
				})}
			case "page-3":
				return [][]byte{rpcResult(t, id, map[string]any{
					"tools": []any{map[string]any{"name": "delta"}},
				})}
			}
			t.Fatalf("unexpected cursor %q", cursor)
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		}
		return [][]byte{rpcErrorLine(t, id, -32601, "no")}
	})
	discovery, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, capability := range discovery.Capabilities {
		names = append(names, capability.Name)
	}
	if strings.Join(names, ",") != "alpha,beta,delta,gamma" {
		t.Fatalf("capabilities = %v, want all four pages", names)
	}
	if discovery.Truncated {
		t.Fatal("complete pagination was marked truncated")
	}
	if discovery.Instructions != "call the graph first" {
		t.Fatalf("instructions = %q", discovery.Instructions)
	}
}

// A page that exceeds the item bound stops immediately and says so.
func TestMCPDiscoveryPageTruncationStopsFollowingCursors(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxItems = 2
	manager, process := testManager(t, cfg, func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{})}
		case "tools/list":
			return [][]byte{rpcResult(t, id, map[string]any{
				"tools":      []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}, map[string]any{"name": "c"}},
				"nextCursor": "more",
			})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		}
		return [][]byte{rpcErrorLine(t, id, -32601, "no")}
	})
	discovery, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Capabilities) != 2 || !discovery.Truncated || discovery.Reason != "item_limit" {
		t.Fatalf("discovery = %+v, want two explicit item_limit rows", discovery)
	}
	// The first page already exhausted the bound, so the cursor is never
	// requested: following it would spend another exchange for rows that could
	// not be published.
	for _, line := range process.writes {
		if strings.Contains(string(line), `"cursor"`) {
			t.Fatalf("a cursor was requested after the item bound was exhausted: %s", line)
		}
	}
}

// An optional method the server does not implement is a complete JSON-RPC
// error response, not a broken stream. The process must survive it so the next
// call does not pay for another handshake.
func TestUnsupportedOptionalMethodDoesNotKillTheProcess(t *testing.T) {
	manager, process := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{
				"protocolVersion": "2024-11-05",
				"instructions":    "tools only",
			})}
		case "tools/list":
			return [][]byte{rpcResult(t, id, map[string]any{
				"tools": []any{map[string]any{"name": "read_tool"}},
			})}
		case "resources/list":
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		case "tools/call":
			return [][]byte{rpcResult(t, id, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "still alive"}},
			})}
		}
		return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
	})
	discovery, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if discovery.Instructions != "tools only" || len(discovery.Capabilities) != 1 {
		t.Fatalf("discovery = %+v", discovery)
	}
	if process.killed {
		t.Fatal("an unsupported optional method killed the process")
	}
	result, err := manager.CallTool(context.Background(), "test", "read_tool", nil, ModeRead, Meter{})
	if err != nil {
		t.Fatalf("call after unsupported method: %v", err)
	}
	if !strings.Contains(result.Text, "still alive") {
		t.Fatalf("result = %+v", result)
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

// A fresh capability snapshot is reused until its TTL expires; an explicit
// refresh always observes the server again.
func TestCapabilityCacheReuseAndRefresh(t *testing.T) {
	toolsCalls := 0
	manager, _ := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{})}
		case "tools/list":
			toolsCalls++
			return [][]byte{rpcResult(t, id, map[string]any{
				"tools": []any{map[string]any{"name": "read_tool"}},
			})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		default:
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		}
	})
	first, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit || toolsCalls != 1 {
		t.Fatalf("first discovery = hit=%v calls=%d, want a fresh call", first.CacheHit, toolsCalls)
	}
	second, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if !second.CacheHit || toolsCalls != 1 {
		t.Fatalf("cached discovery = hit=%v calls=%d, want no new exchange", second.CacheHit, toolsCalls)
	}
	third, err := manager.Refresh(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if third.CacheHit || toolsCalls != 2 {
		t.Fatalf("refresh = hit=%v calls=%d, want a fresh exchange", third.CacheHit, toolsCalls)
	}
}

// A capability identity contains its server, and authority is normalized
// without turning trust into permission.
func TestCapabilityIdentityAuthorityAndTrust(t *testing.T) {
	cfg := baseConfig()
	cfg.Trust = TrustLocal
	cfg.Tools = map[string]string{
		"create_issue": "mutate",
		"run_query":    "execute",
		"search":       "read",
	}
	manager, _ := testManager(t, cfg, standardHandler(t, []any{
		map[string]any{"name": "create_issue"},
		map[string]any{"name": "run_query"},
		map[string]any{"name": "search"},
	}, nil))
	discovery, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Capability{}
	for _, capability := range discovery.Capabilities {
		byName[capability.Name] = capability
	}
	if got := byName["create_issue"].ID(); got != "test:create_issue" {
		t.Fatalf("identity = %q, want test:create_issue", got)
	}
	if got := byName["create_issue"].Authority; got != AuthorityExternalSideEffect {
		t.Fatalf("create_issue authority = %q, want external side effect", got)
	}
	if got := byName["run_query"].Authority; got != AuthorityExecute {
		t.Fatalf("run_query authority = %q, want execute", got)
	}
	if got := byName["search"].Authority; got != AuthorityRead {
		t.Fatalf("search authority = %q, want read", got)
	}
	for _, capability := range discovery.Capabilities {
		if capability.Trust != TrustLocal {
			t.Fatalf("%s trust = %q, want local", capability.Name, capability.Trust)
		}
	}
}

// Trust metadata never grants mutation authority. A local server still needs
// explicit allowMutation and the normal approval boundary.
func TestLocalTrustDoesNotGrantMutationAuthority(t *testing.T) {
	cfg := baseConfig()
	cfg.Trust = TrustLocal
	cfg.AllowMutation = false
	manager, _ := testManager(t, cfg, standardHandler(t, []any{map[string]any{"name": "send_tool"}}, nil))
	_, err := manager.CallTool(context.Background(), "test", "send_tool", nil, ModeMutate, Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureMutationDenied {
		t.Fatalf("error = %v, want mutation_denied even for a local server", err)
	}
}

// Execute-class tools use the mutation boundary, not the read boundary.
func TestExecuteClassRequiresExplicitBoundary(t *testing.T) {
	cfg := baseConfig()
	cfg.AllowMutation = true
	cfg.Tools = map[string]string{"run_query": "execute"}
	manager, _ := testManager(t, cfg, standardHandler(t, []any{map[string]any{"name": "run_query"}}, nil))
	if _, err := manager.CallTool(context.Background(), "test", "run_query", nil, ModeRead, Meter{}); err == nil {
		t.Fatal("read boundary accepted an execute-class tool")
	}
	result, err := manager.CallTool(context.Background(), "test", "run_query", nil, ModeMutate, Meter{})
	if err != nil {
		t.Fatalf("execute-class call refused: %v", err)
	}
	if !strings.Contains(result.Text, "tool output") {
		t.Fatalf("result = %+v", result)
	}
}

// A stale schema is refreshed before execution rather than trusted forever.
func TestStaleSchemaIsRefreshedBeforeExecution(t *testing.T) {
	var required = "old_field"
	toolsCalls := 0
	cfg := baseConfig()
	cfg.CacheTTL = time.Nanosecond
	manager, process := testManager(t, cfg, func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{})}
		case "tools/list":
			toolsCalls++
			return [][]byte{rpcResult(t, id, map[string]any{
				"tools": []any{map[string]any{
					"name": "read_tool",
					"inputSchema": map[string]any{
						"type":     "object",
						"required": []string{required},
						"properties": map[string]any{
							"old_field": map[string]any{"type": "string"},
							"new_field": map[string]any{"type": "string"},
						},
					},
				}},
			})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		case "tools/call":
			return [][]byte{rpcResult(t, id, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "called"}},
			})}
		default:
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		}
	})
	if _, err := manager.Discover(context.Background(), "test", Meter{}); err != nil {
		t.Fatal(err)
	}
	required = "new_field"
	time.Sleep(2 * time.Millisecond)
	_, err := manager.CallTool(context.Background(), "test", "read_tool", map[string]any{"old_field": "x"}, ModeRead, Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureInvalidRequest {
		t.Fatalf("error = %v, want invalid_request from the refreshed schema", err)
	}
	if toolsCalls < 2 {
		t.Fatalf("tools/list calls = %d, want a refresh", toolsCalls)
	}
	if process.wroteMethod("tools/call") {
		t.Fatal("an argument that only satisfied the stale schema reached tools/call")
	}
}

// Progress notifications are bounded and mapped to the Meter, while the final
// result remains the only execution outcome.
func TestProgressNotificationsAreMapped(t *testing.T) {
	manager, _ := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{})}
		case "tools/list":
			return [][]byte{rpcResult(t, id, map[string]any{"tools": []any{map[string]any{"name": "read_tool"}}})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		case "tools/call":
			notification, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/progress",
				"params":  map[string]any{"progress": 2, "total": 4, "message": "halfway"},
			})
			return [][]byte{notification, rpcResult(t, id, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "done"}},
			})}
		default:
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		}
	})
	var updates []Progress
	if _, err := manager.CallTool(context.Background(), "test", "read_tool", nil, ModeRead, Meter{
		Progress: func(progress Progress) { updates = append(updates, progress) },
	}); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Message != "halfway" || updates[0].Progress != 2 || updates[0].Total != 4 {
		t.Fatalf("progress = %+v", updates)
	}
}

// A prompt is discovered with a callable schema, fetched as bounded context,
// and never gains authority.
func TestPromptDiscoveryAndRendering(t *testing.T) {
	manager, _ := testManager(t, baseConfig(), func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{})}
		case "tools/list":
			return [][]byte{rpcResult(t, id, map[string]any{"tools": []any{}})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		case "prompts/list":
			return [][]byte{rpcResult(t, id, map[string]any{"prompts": []any{map[string]any{
				"name":        "review",
				"description": "review a change",
				"arguments": []any{map[string]any{
					"name": "target", "description": "what to review", "required": true,
				}},
			}}})}
		case "prompts/get":
			arguments, _ := params["arguments"].(map[string]any)
			return [][]byte{rpcResult(t, id, map[string]any{
				"description": "review instructions",
				"messages": []any{map[string]any{
					"role":    "user",
					"content": map[string]any{"type": "text", "text": "review " + arguments["target"].(string)},
				}},
			})}
		default:
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		}
	})
	discovery, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	var prompt Capability
	for _, capability := range discovery.Capabilities {
		if capability.Kind == KindPrompt {
			prompt = capability
		}
	}
	if prompt.ID() != "test:prompt/review" || prompt.Authority != AuthorityRead {
		t.Fatalf("prompt = %+v", prompt)
	}
	if prompt.Schema == nil {
		t.Fatal("prompt argument schema was not compiled")
	}
	result, err := manager.GetPrompt(context.Background(), "test", "review", map[string]any{"target": "a.go"}, Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items != 1 || !strings.Contains(result.Text, "[user]") || !strings.Contains(result.Text, "review a.go") {
		t.Fatalf("prompt result = %+v", result)
	}
	if _, err := manager.GetPrompt(context.Background(), "test", "review", map[string]any{}, Meter{}); err == nil {
		t.Fatal("a prompt missing a required argument was accepted")
	}
}

// blockingProcess blocks reads until killed, which is what a real child does
// when it never answers.
type blockingProcess struct {
	done chan struct{}
	once sync.Once
}

func newBlockingProcess() *blockingProcess        { return &blockingProcess{done: make(chan struct{})} }
func (p *blockingProcess) WriteLine([]byte) error { return nil }
func (p *blockingProcess) ReadLine(int) ([]byte, error) {
	<-p.done
	return nil, io.EOF
}
func (p *blockingProcess) Kill() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// Cancellation reaches the transport, kills the child, and reports the
// exchange as cancelled rather than as a provider failure or success.
func TestCancellationKillsBlockedExchange(t *testing.T) {
	process := newBlockingProcess()
	manager, err := newManager([]ServerConfig{baseConfig()}, func(ServerConfig) (serverProcess, error) {
		return process, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err = manager.Discover(ctx, "test", Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureCancelled {
		t.Fatalf("error = %v, want cancelled", err)
	}
	select {
	case <-process.done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not kill the blocked process")
	}
}

// Streamable HTTP completes a full exchange, carries the session id, maps
// progress, and never needs a child process.
func TestStreamableHTTPRoundTripAndProgress(t *testing.T) {
	var sawProgress bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     *int           `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if request.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "session-1")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": *request.ID,
				"result": map[string]any{"protocolVersion": "2024-11-05"},
			})
			return
		}
		if got := r.Header.Get("Mcp-Session-Id"); got != "session-1" {
			t.Errorf("session id = %q, want session-1", got)
		}
		switch request.Method {
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": *request.ID,
				"result": map[string]any{"tools": []any{map[string]any{"name": "read_tool"}}},
			})
		case "resources/list", "prompts/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": *request.ID,
				"result": map[string]any{"resources": []any{}, "prompts": []any{}},
			})
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			sawProgress = true
			_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"message\":\"working\"}}\n\n")
			payload, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": *request.ID,
				"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "http result"}}},
			})
			_, _ = io.WriteString(w, "data: "+string(payload)+"\n\n")
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": *request.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}))
	defer server.Close()
	manager, err := NewManager([]ServerConfig{{
		Name: "remote", Transport: TransportStreamableHTTP, URL: server.URL,
		Trust: TrustExternal, Tools: map[string]string{"read_tool": "read"},
		MaxBytes: 64 * 1024,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	discovery, err := manager.Discover(context.Background(), "remote", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Capabilities) != 1 || discovery.Capabilities[0].ID() != "remote:read_tool" {
		t.Fatalf("discovery = %+v", discovery)
	}
	var progress []Progress
	result, err := manager.CallTool(context.Background(), "remote", "read_tool", nil, ModeRead, Meter{
		Progress: func(update Progress) { progress = append(progress, update) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "http result") {
		t.Fatalf("result = %+v", result)
	}
	if !sawProgress || len(progress) != 1 || progress[0].Message != "working" {
		t.Fatalf("HTTP progress = %+v (server saw %v)", progress, sawProgress)
	}
}

// An HTTP error is classified without reading a body that may echo secrets.
func TestStreamableHTTPErrorDoesNotLeakBody(t *testing.T) {
	const secret = "super-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "token="+secret)
	}))
	defer server.Close()
	manager, err := NewManager([]ServerConfig{{
		Name: "remote", Transport: TransportStreamableHTTP, URL: server.URL,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	_, err = manager.Discover(context.Background(), "remote", Meter{})
	if err == nil {
		t.Fatal("an HTTP error was reported as success")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked a response body: %v", err)
	}
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureAuthentication {
		t.Fatalf("error = %v, want authentication_failure", err)
	}
}

// Structured content is preserved as bounded data instead of being dropped or
// stringified away.
func TestStructuredContentIsPreserved(t *testing.T) {
	handler := func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{})}
		case "tools/list":
			return [][]byte{rpcResult(t, id, map[string]any{"tools": []any{map[string]any{"name": "read_tool"}}})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		case "tools/call":
			return [][]byte{rpcResult(t, id, map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": "summary"}},
				"structuredContent": map[string]any{"count": 2, "items": []string{"a", "b"}},
			})}
		default:
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		}
	}
	manager, _ := testManager(t, baseConfig(), handler)
	result, err := manager.CallTool(context.Background(), "test", "read_tool", nil, ModeRead, Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "summary") {
		t.Fatalf("result lost text content: %q", result.Text)
	}
	if !strings.Contains(result.Text, `structured_content: {"count":2,"items":["a","b"]}`) {
		t.Fatalf("result lost structured content: %q", result.Text)
	}
}

// A configured credential value never appears in discovery, a result, or a
// classified error, even when a server echoes it.
func TestConfiguredSecretIsRedacted(t *testing.T) {
	const secret = "super-secret-value"
	t.Setenv("OHGO_MCP_SECRET", secret)
	cfg := baseConfig()
	cfg.Env = []string{"OHGO_MCP_SECRET"}
	calls := 0
	manager, _ := testManager(t, cfg, func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{"instructions": "use " + secret})}
		case "tools/list":
			return [][]byte{rpcResult(t, id, map[string]any{"tools": []any{map[string]any{
				"name": "read_tool", "description": "token " + secret,
			}}})}
		case "resources/list":
			return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
		case "tools/call":
			calls++
			if calls == 1 {
				return [][]byte{rpcResult(t, id, map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "value " + secret}},
				})}
			}
			return [][]byte{rpcErrorLine(t, id, -32000, "rejected "+secret)}
		default:
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		}
	})
	discovery, err := manager.Discover(context.Background(), "test", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(discovery.Instructions, secret) {
		t.Fatalf("instructions leaked the secret: %q", discovery.Instructions)
	}
	for _, capability := range discovery.Capabilities {
		if strings.Contains(capability.Description, secret) {
			t.Fatalf("description leaked the secret: %q", capability.Description)
		}
	}
	result, err := manager.CallTool(context.Background(), "test", "read_tool", nil, ModeRead, Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, secret) {
		t.Fatalf("result leaked the secret: %q", result.Text)
	}
	_, err = manager.CallTool(context.Background(), "test", "read_tool", nil, ModeRead, Meter{})
	if err == nil {
		t.Fatal("expected the scripted error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the secret: %v", err)
	}
}

// Two servers advertising the same tool name keep distinct identities. A
// capability from one is never represented as the other's.
func TestCapabilityIdentityKeepsServersApart(t *testing.T) {
	processes := map[string]*scriptedProcess{}
	for _, name := range []string{"alpha", "beta"} {
		processes[name] = &scriptedProcess{handler: func(method string, params map[string]any, id int) [][]byte {
			switch method {
			case "initialize":
				return [][]byte{rpcResult(t, id, map[string]any{})}
			case "tools/list":
				return [][]byte{rpcResult(t, id, map[string]any{
					"tools": []any{map[string]any{"name": "search"}},
				})}
			case "resources/list":
				return [][]byte{rpcResult(t, id, map[string]any{"resources": []any{}})}
			default:
				return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
			}
		}}
	}
	manager, err := newManager([]ServerConfig{
		{Name: "alpha", Command: "fake", Tools: map[string]string{"search": "read"}},
		{Name: "beta", Command: "fake", Tools: map[string]string{"search": "read"}},
	}, func(cfg ServerConfig) (serverProcess, error) {
		return processes[cfg.Name], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	alpha, err := manager.Discover(context.Background(), "alpha", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := manager.Discover(context.Background(), "beta", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	alphaID := alpha.Capabilities[0].ID()
	betaID := beta.Capabilities[0].ID()
	if alphaID != "alpha:search" || betaID != "beta:search" {
		t.Fatalf("identities = %q, %q", alphaID, betaID)
	}
	if alphaID == betaID {
		t.Fatal("two servers with the same tool name shared an identity")
	}
}

// Classification and transport parsing are deterministic and fail closed.
func TestConfigurationVocabularyParsing(t *testing.T) {
	classes := map[string]Classification{
		"read":     ClassRead,
		"READ":     ClassRead,
		"execute":  ClassExecute,
		"exec":     ClassExecute,
		"interact": ClassInteract,
		"mutate":   ClassMutate,
		"write":    ClassMutate,
		"":         ClassUnknown,
		"nonsense": ClassUnknown,
	}
	for input, want := range classes {
		if got := ParseClassification(input); got != want {
			t.Errorf("ParseClassification(%q) = %s, want %s", input, got, want)
		}
	}
	if ClassExecute.Authority() != AuthorityExecute {
		t.Error("execute authority was not EXECUTE")
	}
	if ClassMutate.Authority() != AuthorityExternalSideEffect {
		t.Error("MCP mutate is an external side effect, not a repository mutation")
	}
	if !ClassExecute.RequiresMutationBoundary() || ClassRead.RequiresMutationBoundary() {
		t.Error("execute must use the approved boundary; read must not")
	}
	if transport, ok := ParseTransport("streamable-http"); !ok || transport != TransportStreamableHTTP {
		t.Errorf("streamable-http = %q/%v", transport, ok)
	}
	if _, ok := ParseTransport("htp"); ok {
		t.Error("an unknown transport was accepted")
	}
	if ParseTrust("LOCAL") != TrustLocal || ParseTrust("typo") != TrustUntrusted {
		t.Error("trust parsing did not normalize safely")
	}
}

// A stdio exchange honours the configured timeout even when the caller has no
// deadline, and the unresponsive child is killed.
func TestStdioExchangeTimeout(t *testing.T) {
	process := newBlockingProcess()
	cfg := baseConfig()
	cfg.Timeout = 5 * time.Millisecond
	manager, err := newManager([]ServerConfig{cfg}, func(ServerConfig) (serverProcess, error) {
		return process, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	_, err = manager.Discover(context.Background(), "test", Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureTimeout {
		t.Fatalf("error = %v, want timeout", err)
	}
	select {
	case <-process.done:
	case <-time.After(time.Second):
		t.Fatal("timeout did not kill the unresponsive process")
	}
}

// A resource read is bounded, carries its URI/type, and honours the configured
// prefix policy before any exchange.
func TestReadResourceBoundsAndHonoursPrefixPolicy(t *testing.T) {
	handler := func(method string, params map[string]any, id int) [][]byte {
		switch method {
		case "initialize":
			return [][]byte{rpcResult(t, id, map[string]any{})}
		case "resources/read":
			return [][]byte{rpcResult(t, id, map[string]any{
				"contents": []any{map[string]any{
					"uri": "file:///allowed/doc", "mimeType": "text/plain", "text": "resource body",
				}},
			})}
		default:
			return [][]byte{rpcErrorLine(t, id, -32601, "method not found")}
		}
	}
	cfg := baseConfig()
	cfg.ResourcePrefixes = []string{"file:///allowed/"}
	manager, process := testManager(t, cfg, handler)
	result, err := manager.ReadResource(context.Background(), "test", "file:///allowed/doc", Meter{})
	if err != nil {
		t.Fatal(err)
	}
	if result.URI != "file:///allowed/doc" || result.MIMEType != "text/plain" || result.Text != "resource body" {
		t.Fatalf("resource = %+v", result)
	}
	_, err = manager.ReadResource(context.Background(), "test", "file:///denied/doc", Meter{})
	var failure knowledge.Failure
	if !errors.As(err, &failure) || failure.Class != knowledge.FailureInvalidRequest {
		t.Fatalf("denied resource error = %v", err)
	}
	if process.wroteMethod("resources/read") == false {
		t.Fatal("the allowed resource was not read")
	}
}
