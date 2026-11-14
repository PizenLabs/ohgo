package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// TestRecallToolReturnsArchivedText proves the addressable-history contract:
// a compaction id resolves to the exact observation it replaced, and an
// unknown id is a truthful error rather than an empty success.
func TestRecallToolReturnsArchivedText(t *testing.T) {
	store := agent.NewRecallStore()
	id := store.Archive("read", "call-1", "original observation")
	if id == "" {
		t.Fatal("archive refused a valid observation")
	}
	tool := NewRecallTool(store)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"id": id})
	if result.IsError || !strings.Contains(result.Text, "original observation") {
		t.Fatalf("result = %+v, want the archived text", result)
	}
	missing := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"id": "E999"})
	if !missing.IsError {
		t.Fatal("an unknown id was reported as success")
	}
}
