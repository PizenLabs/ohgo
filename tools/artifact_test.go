package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/workspace"
)

func runArtifact(t *testing.T, tool *ArtifactTool, args map[string]any) agent.Result {
	t.Helper()
	return tool.Execute(agent.Context{Ctx: context.Background()}, args)
}

func TestArtifactToolRecordsAWorkspaceArtifact(t *testing.T) {
	var recorded []workspace.Artifact
	tool := NewArtifactTool(func(a workspace.Artifact) error {
		recorded = append(recorded, a)
		return nil
	}, workspace.Fuzz)

	result := runArtifact(t, tool, map[string]any{
		"kind": "finding", "target": "internal/runtime.go", "status": "reproduced",
		"reproduction": "go test -race ./...", "evidence": "DATA RACE at runtime.go:42",
	})
	if result.IsError {
		t.Fatalf("recording a finding failed: %s", result.Text)
	}
	if len(recorded) != 1 {
		t.Fatalf("recorded %d artifacts, want 1", len(recorded))
	}
	got := recorded[0]
	if got.Kind != workspace.KindFinding || got.Origin != workspace.Fuzz {
		t.Errorf("artifact = %+v, want a fuzz finding", got)
	}
	if !strings.HasPrefix(got.ID, "finding-") {
		t.Errorf("id = %q, want a finding- prefix", got.ID)
	}
}

func TestArtifactToolRefusesTheWrongKindForTheWorkspace(t *testing.T) {
	// FUZZ may discover but not change: it cannot author a Change artifact,
	// which is the same boundary as refusing to write a file (§27).
	tool := NewArtifactTool(func(workspace.Artifact) error { return nil }, workspace.Fuzz)
	result := runArtifact(t, tool, map[string]any{"kind": "change", "target": "a.go"})
	if !result.IsError {
		t.Fatal("FUZZ authored a change artifact")
	}
	if !strings.Contains(result.Text, "cannot produce") {
		t.Errorf("refusal = %q, want an authority message", result.Text)
	}

	// ASK may plan but not claim a verification it never ran.
	ask := NewArtifactTool(func(workspace.Artifact) error { return nil }, workspace.Ask)
	if result := runArtifact(t, ask, map[string]any{"kind": "verification", "target": "x"}); !result.IsError {
		t.Error("ASK authored a verification artifact")
	}
	if result := runArtifact(t, ask, map[string]any{"kind": "plan", "target": "the migration"}); result.IsError {
		t.Errorf("ASK could not record a plan: %s", result.Text)
	}
}

func TestArtifactToolRequiresEvidenceForAFinding(t *testing.T) {
	tool := NewArtifactTool(func(workspace.Artifact) error { return nil }, workspace.Fuzz)
	result := runArtifact(t, tool, map[string]any{"kind": "finding", "target": "x.go"})
	if !result.IsError {
		t.Fatal("a finding without reproduction or evidence was recorded")
	}
}

func TestArtifactToolRefusesUnknownKindAndMissingSink(t *testing.T) {
	tool := NewArtifactTool(func(workspace.Artifact) error { return nil }, workspace.Build)
	if result := runArtifact(t, tool, map[string]any{"kind": "rumour", "target": "x"}); !result.IsError {
		t.Error("an unknown artifact kind was accepted")
	}
	noSink := NewArtifactTool(nil, workspace.Build)
	if result := runArtifact(t, noSink, map[string]any{"kind": "change", "target": "x"}); !result.IsError {
		t.Error("a nil sink silently dropped the artifact")
	}
}
