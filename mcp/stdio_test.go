package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestStdioMCPHelper is a real child process used by TestStdioClientRoundTrip.
// It is inert unless the parent sets the helper environment variable.
func TestStdioMCPHelper(t *testing.T) {
	if os.Getenv("OHGO_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024), 256*1024)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     *int           `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		if request.ID == nil {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05"}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "echo", "description": "echo", "inputSchema": map[string]any{
					"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}},
				}},
			}}
		case "resources/list":
			result = map[string]any{"resources": []any{}}
		case "tools/call":
			arguments, _ := request.Params["arguments"].(map[string]any)
			text, _ := arguments["text"].(string)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + text}}}
		default:
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": *request.ID,
				"error": map[string]any{"code": -32601, "message": "method not found"},
			})
			continue
		}
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": *request.ID, "result": result})
	}
	os.Exit(0)
}

// The production stdio client must frame JSON-RPC lines, filter the
// environment, and complete a real discover/call/close cycle.
func TestStdioClientRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}
	t.Setenv("OHGO_MCP_HELPER", "1")
	cfg := ServerConfig{
		Name:     "helper",
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestStdioMCPHelper"},
		Env:      []string{"OHGO_MCP_HELPER"},
		Tools:    map[string]string{"echo": "read"},
		MaxBytes: 64 * 1024,
	}
	manager, err := NewManager([]ServerConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	discovery, err := manager.Discover(context.Background(), "helper", Meter{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	capabilities := discovery.Capabilities
	if len(capabilities) != 1 || capabilities[0].Name != "echo" || capabilities[0].Classification != ClassRead {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	result, err := manager.CallTool(context.Background(), "helper", "echo", map[string]any{"text": "hello"}, ModeRead, Meter{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(result.Text, "echo: hello") {
		t.Fatalf("result = %+v", result)
	}
}
