package mcp

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestGryLive is an opt-in compatibility test against a real gry MCP server.
//
// Set GRY_BIN to the gry executable and GRY_CACHE to a cache directory that
// already contains an indexed project. It is deliberately skipped by default
// so the normal test suite never depends on another repository's build.
func TestGryLive(t *testing.T) {
	bin := os.Getenv("GRY_BIN")
	cache := os.Getenv("GRY_CACHE")
	project := os.Getenv("GRY_PROJECT")
	if bin == "" || cache == "" || project == "" {
		t.Skip("set GRY_BIN, GRY_CACHE and GRY_PROJECT to run the gry compatibility test")
	}
	cfg := ServerConfig{
		Name:    "gry",
		Command: bin,
		Args:    []string{"--cache-dir", cache, "serve"},
		Dir:     os.Getenv("GRY_DIR"),
		Env:     []string{"GRY_CACHE_DIR", "GRY_DB"},
		Tools: map[string]string{
			"index_repository":     "mutate",
			"search_graph":         "read",
			"query_graph":          "read",
			"trace_path":           "read",
			"get_code_snippet":     "read",
			"get_file_outline":     "read",
			"get_graph_schema":     "read",
			"compare_graphs":       "read",
			"get_architecture":     "read",
			"search_code":          "read",
			"list_projects":        "read",
			"delete_project":       "mutate",
			"index_status":         "read",
			"check_index_coverage": "read",
			"detect_changes":       "read",
			"manage_adr":           "mutate",
			"ingest_traces":        "mutate",
		},
		MaxBytes: 4 * 1024 * 1024,
	}
	manager, err := NewManager([]ServerConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	discovery, err := manager.Discover(context.Background(), "gry", Meter{})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if discovery.Truncated {
		t.Fatalf("discovery truncated: %s", discovery.Reason)
	}
	tools, prompts := 0, 0
	for _, capability := range discovery.Capabilities {
		switch capability.Kind {
		case KindTool:
			tools++
		case KindPrompt:
			prompts++
		}
	}
	t.Logf("discovered %d capabilities (%d tools, %d prompts)", len(discovery.Capabilities), tools, prompts)
	if tools != 17 {
		t.Fatalf("discovered %d tools, want 17", tools)
	}
	if !strings.Contains(discovery.Instructions, "search_graph") {
		t.Fatalf("server instructions were not surfaced: %q", discovery.Instructions)
	}
	for _, capability := range discovery.Capabilities {
		if capability.Schema == nil {
			t.Errorf("tool %q has no compiled schema: %s", capability.Name, capability.Description)
		}
		if len(capability.SchemaRaw) == 0 {
			t.Errorf("tool %q has no raw schema for discovery output", capability.Name)
		}
	}

	calls := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"list_projects", map[string]any{}, project},
		{"search_graph", map[string]any{"project": project, "query": "parse"}, "parse"},
		{"get_architecture", map[string]any{"project": project, "aspects": []any{"overview"}}, "languages"},
		{"index_status", map[string]any{"project": project}, "indexed"},
	}
	for _, tc := range calls {
		result, err := manager.CallTool(context.Background(), "gry", tc.tool, tc.args, ModeRead, Meter{})
		if err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		if result.IsError {
			t.Fatalf("%s returned isError: %s", tc.tool, result.Text)
		}
		if !strings.Contains(result.Text, tc.want) {
			t.Fatalf("%s: result does not contain %q: %s", tc.tool, tc.want, result.Text)
		}
	}
}
