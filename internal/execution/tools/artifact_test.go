package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

func runArtifact(t *testing.T, tool *ArtifactTool, args map[string]any) agent.Result {
	t.Helper()
	return tool.Execute(agent.Context{Ctx: context.Background()}, args)
}

// fixedArtifactWorkspace adapts a constant workspace to the live source the
// tool reads on every call.
func fixedArtifactWorkspace(ws workspace.Workspace) func() workspace.Workspace {
	return func() workspace.Workspace { return ws }
}

func TestArtifactToolRecordsAWorkspaceArtifact(t *testing.T) {
	var recorded []workspace.Artifact
	tool := NewArtifactTool(func(a workspace.Artifact) error {
		recorded = append(recorded, a)
		return nil
	}, fixedArtifactWorkspace(workspace.Fuzz))

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

// The session sink and the task-continuity observer are additive: a host must
// not have to replace durable artifact storage to project a finding.
func TestArtifactToolNotifiesAdditiveSinks(t *testing.T) {
	var primary, observed []workspace.Artifact
	tool := NewArtifactTool(func(a workspace.Artifact) error {
		primary = append(primary, a)
		return nil
	}, fixedArtifactWorkspace(workspace.Fuzz))
	tool.AddSink(func(a workspace.Artifact) error {
		observed = append(observed, a)
		return nil
	})
	tool.AddSink(nil)

	result := runArtifact(t, tool, map[string]any{
		"kind": "finding", "target": "order/checkout", "status": "reproduced",
		"reproduction": "go test ./order/...", "evidence": "FAIL",
	})
	if result.IsError {
		t.Fatalf("recording a finding failed: %s", result.Text)
	}
	if len(primary) != 1 || len(observed) != 1 {
		t.Fatalf("primary=%d observed=%d, want one record in each sink", len(primary), len(observed))
	}
	if observed[0].ID != primary[0].ID {
		t.Fatalf("observer saw %q, primary saw %q", observed[0].ID, primary[0].ID)
	}
}

func TestArtifactToolRefusesTheWrongKindForTheWorkspace(t *testing.T) {
	// FUZZ may discover but not change: it cannot author a Change artifact,
	// which is the same boundary as refusing to write a file (§27).
	tool := NewArtifactTool(func(workspace.Artifact) error { return nil }, fixedArtifactWorkspace(workspace.Fuzz))
	result := runArtifact(t, tool, map[string]any{"kind": "change", "target": "a.go"})
	if !result.IsError {
		t.Fatal("FUZZ authored a change artifact")
	}
	if !result.Refused {
		t.Error("a workspace kind refusal was recorded as an execution failure")
	}
	if !strings.Contains(result.Text, "cannot produce") || !strings.Contains(result.Text, "finding") {
		t.Errorf("refusal = %q, want an authority message naming the allowed kind", result.Text)
	}

	// The live session chose "plan" for an investigation. It is still the
	// wrong producer for FUZZ, but the refusal must name the usable kind.
	plan := runArtifact(t, tool, map[string]any{
		"kind": "plan", "target": "order/checkout flow", "status": "investigated",
		"expected": "understand the flow", "observed": "no backend implementation",
	})
	if !plan.IsError || !plan.Refused {
		t.Fatal("FUZZ authored a plan artifact")
	}
	if !strings.Contains(plan.Text, "finding") {
		t.Errorf("plan refusal = %q, want the allowed FUZZ kind", plan.Text)
	}

	// ASK may plan but not claim a verification it never ran.
	ask := NewArtifactTool(func(workspace.Artifact) error { return nil }, fixedArtifactWorkspace(workspace.Ask))
	if result := runArtifact(t, ask, map[string]any{"kind": "verification", "target": "x"}); !result.IsError {
		t.Error("ASK authored a verification artifact")
	}
	if result := runArtifact(t, ask, map[string]any{"kind": "plan", "target": "the migration"}); result.IsError {
		t.Errorf("ASK could not record a plan: %s", result.Text)
	}
}

// TestArtifactToolFollowsTheLiveWorkspace is the regression for a session that
// moves BUILD -> FUZZ without rebuilding its tool set. The tool must attribute
// the finding to FUZZ and accept it; a captured origin would refuse the call
// and leave an unrecoverable failure in the run.
func TestArtifactToolFollowsTheLiveWorkspace(t *testing.T) {
	role := workspace.Build
	var recorded []workspace.Artifact
	tool := NewArtifactTool(func(a workspace.Artifact) error {
		recorded = append(recorded, a)
		return nil
	}, func() workspace.Workspace { return role })

	role = workspace.Fuzz
	result := runArtifact(t, tool, map[string]any{
		"kind": "finding", "target": "backend/checkout", "status": "reproduced",
		"reproduction": "submit the checkout form", "evidence": "no order request is sent",
	})
	if result.IsError {
		t.Fatalf("a FUZZ finding was refused after the switch: %s", result.Text)
	}
	if len(recorded) != 1 || recorded[0].Origin != workspace.Fuzz {
		t.Fatalf("recorded = %+v, want one finding attributed to FUZZ", recorded)
	}

	// The same tool must refuse a kind the newly active workspace cannot
	// produce. A stale BUILD origin would have accepted this change.
	role = workspace.Ask
	result = runArtifact(t, tool, map[string]any{"kind": "change", "target": "a.go"})
	if !result.IsError || !result.Refused {
		t.Fatal("ASK authored a change artifact through the live source")
	}
}

// A verification artifact must reference a check the runtime observed. The
// model can record a change before verifying it, but it cannot author a
// "verified" record the completion evidence contradicts.
func TestArtifactVerificationRequiresAnObservedCheck(t *testing.T) {
	verified := false
	var recorded []workspace.Artifact
	tool := NewArtifactTool(func(a workspace.Artifact) error {
		recorded = append(recorded, a)
		return nil
	}, fixedArtifactWorkspace(workspace.Build))
	tool.SetVerificationSource(func() bool { return verified })

	// Recording the change before the check is what lets the model describe
	// the work first; it is not a verification claim.
	if result := runArtifact(t, tool, map[string]any{"kind": "change", "target": "backend/checkout"}); result.IsError {
		t.Fatalf("the change artifact was refused before verification: %s", result.Text)
	}

	result := runArtifact(t, tool, map[string]any{"kind": "verification", "target": "backend/checkout"})
	if !result.IsError || !result.Refused {
		t.Fatalf("an unbacked verification was recorded: %+v", result)
	}
	if !strings.Contains(result.Text, "go test") {
		t.Errorf("refusal = %q, want an actionable check to run", result.Text)
	}
	if len(recorded) != 1 || recorded[0].Kind != workspace.KindChange {
		t.Fatalf("recorded = %+v, want only the change artifact", recorded)
	}

	verified = true
	result = runArtifact(t, tool, map[string]any{"kind": "verification", "target": "backend/checkout"})
	if result.IsError {
		t.Fatalf("an observed check was not accepted: %s", result.Text)
	}
	if len(recorded) != 2 || recorded[1].Kind != workspace.KindVerification {
		t.Fatalf("recorded = %+v, want the verification after the observed check", recorded)
	}
}

func TestArtifactToolRequiresEvidenceForAFinding(t *testing.T) {
	tool := NewArtifactTool(func(workspace.Artifact) error { return nil }, fixedArtifactWorkspace(workspace.Fuzz))
	result := runArtifact(t, tool, map[string]any{"kind": "finding", "target": "x.go"})
	if !result.IsError {
		t.Fatal("a finding without reproduction or evidence was recorded")
	}
}

func TestArtifactToolRefusesUnknownKindAndMissingSink(t *testing.T) {
	tool := NewArtifactTool(func(workspace.Artifact) error { return nil }, fixedArtifactWorkspace(workspace.Build))
	if result := runArtifact(t, tool, map[string]any{"kind": "rumour", "target": "x"}); !result.IsError {
		t.Error("an unknown artifact kind was accepted")
	}
	noSink := NewArtifactTool(nil, fixedArtifactWorkspace(workspace.Build))
	if result := runArtifact(t, noSink, map[string]any{"kind": "change", "target": "x"}); !result.IsError {
		t.Error("a nil sink silently dropped the artifact")
	}
	noOrigin := NewArtifactTool(func(workspace.Artifact) error { return nil }, nil)
	if result := runArtifact(t, noOrigin, map[string]any{"kind": "change", "target": "x"}); !result.IsError {
		t.Error("a nil workspace source silently produced an artifact")
	}
}
