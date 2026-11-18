package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

func TestConcludeRecordsAVerdictAndEvidence(t *testing.T) {
	tool := NewConcludeTool()
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"verdict":     "safe",
		"evidence":    "POST /checkout -> checkout.Place -> stock.Reserve before storage.Save; reserve returns ErrInsufficientStock for a missing variant",
		"uncertainty": "no integration test covers a deleted variant",
	})
	if result.IsError {
		t.Fatalf("conclude failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "conclusion: SAFE") {
		t.Fatalf("text = %q, want the normalized verdict", result.Text)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["verdict"] != "SAFE" || details["evidence"] == "" || details["uncertainty"] == "" {
		t.Fatalf("details = %+v", result.Details)
	}
}

func TestConcludeRejectsAnUnsupportedVerdict(t *testing.T) {
	for _, args := range []map[string]any{
		{"verdict": "PASS", "evidence": "checked"},
		{"verdict": "SAFE", "evidence": "   "},
	} {
		result := NewConcludeTool().Execute(agent.Context{Ctx: context.Background()}, args)
		if !result.IsError {
			t.Fatalf("conclude accepted %+v", args)
		}
	}
}
