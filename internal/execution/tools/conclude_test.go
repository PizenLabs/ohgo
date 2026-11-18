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
		"status":      "FINAL",
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
		{"verdict": "PASS", "status": "FINAL", "evidence": "checked"},
		{"verdict": "SAFE", "status": "FINAL", "evidence": "   "},
		{"verdict": "SAFE", "evidence": "checked"},
		{"verdict": "SAFE", "status": "MAYBE", "evidence": "checked"},
	} {
		result := NewConcludeTool().Execute(agent.Context{Ctx: context.Background()}, args)
		if !result.IsError {
			t.Fatalf("conclude accepted %+v", args)
		}
	}
}

func TestConcludeProgressDoesNotBecomeATerminalConclusion(t *testing.T) {
	result := NewConcludeTool().Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"verdict":     "UNRESOLVED",
		"status":      "IN_PROGRESS",
		"evidence":    "entrypoint not yet read",
		"uncertainty": "still investigating",
	})
	if result.IsError {
		t.Fatalf("conclude progress failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "progress: investigation remains open") {
		t.Fatalf("text = %q, want a progress observation", result.Text)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["status"] != "IN_PROGRESS" {
		t.Fatalf("details = %+v", result.Details)
	}
}

func TestConcludePreliminaryMarkerOverridesFinalStatus(t *testing.T) {
	result := NewConcludeTool().Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"verdict":     "UNRESOLVED",
		"status":      "FINAL",
		"evidence":    "partial checkout read",
		"uncertainty": "Preliminary only; investigation still in progress.",
	})
	if result.IsError {
		t.Fatalf("conclude failed: %s", result.Text)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["status"] != "IN_PROGRESS" {
		t.Fatalf("details = %+v, want the preliminary marker to override FINAL", result.Details)
	}
}

func TestConcludePlaceholderEvidenceRemainsProgress(t *testing.T) {
	result := NewConcludeTool().Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"verdict":     "UNRESOLVED",
		"status":      "FINAL",
		"evidence":    "placeholder, not yet sufficient",
		"uncertainty": "",
	})
	if result.IsError {
		t.Fatalf("conclude failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "progress: investigation remains open") {
		t.Fatalf("text = %q, a placeholder conclusion was admitted", result.Text)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["status"] != "IN_PROGRESS" {
		t.Fatalf("details = %+v, want a progress observation", result.Details)
	}
}
