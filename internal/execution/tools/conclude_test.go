package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
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

func TestConcludeRejectsADefectVerdictForAGeneralAnswer(t *testing.T) {
	result := NewConcludeTool().Execute(agent.Context{
		Ctx:            context.Background(),
		ConclusionMode: contract.ConclusionAnswer,
	}, map[string]any{
		"verdict":  "SAFE",
		"status":   "FINAL",
		"evidence": "the project is a Next.js blog",
	})
	if !result.IsError {
		t.Fatal("a general answer was recorded as a safety verdict")
	}
	if !strings.Contains(result.Text, "ANSWERED") || !strings.Contains(result.Text, "defect investigation") {
		t.Fatalf("error = %q, want the required vocabulary and the defect boundary", result.Text)
	}
}

func TestConcludeRecordsAnAnswerOutcome(t *testing.T) {
	result := NewConcludeTool().Execute(agent.Context{
		Ctx:            context.Background(),
		ConclusionMode: contract.ConclusionAnswer,
	}, map[string]any{
		"verdict":     "ANSWERED",
		"status":      "FINAL",
		"evidence":    "app/page.tsx loads Markdown posts through lib/post-loader.ts",
		"uncertainty": "runtime behavior was not observed",
	})
	if result.IsError {
		t.Fatalf("answer conclusion failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "conclusion: ANSWERED") ||
		!strings.Contains(result.Text, "status: FINAL") {
		t.Fatalf("text = %q, want the terminal conclusion label and status", result.Text)
	}
	if strings.Contains(result.Text, "answer: ANSWERED") {
		t.Fatalf("text = %q, a conclusion verdict was mislabeled as the user's answer", result.Text)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["mode"] != string(contract.ConclusionAnswer) {
		t.Fatalf("details = %+v, want the answer mode", result.Details)
	}
}

func TestConcludeRejectsAnAnswerVerdictForADefectInvestigation(t *testing.T) {
	result := NewConcludeTool().Execute(agent.Context{
		Ctx:            context.Background(),
		ConclusionMode: contract.ConclusionDefect,
	}, map[string]any{
		"verdict":  "ANSWERED",
		"status":   "FINAL",
		"evidence": "the checkout path calls Reserve before Save",
	})
	if !result.IsError {
		t.Fatal("a defect investigation accepted an answer verdict")
	}
	if !strings.Contains(result.Text, "SAFE") || !strings.Contains(result.Text, "BUG") {
		t.Fatalf("error = %q, want the defect vocabulary", result.Text)
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
