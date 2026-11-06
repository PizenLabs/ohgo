package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// TestArtifactToolFollowsTheLiveWorkspace is the regression for a TUI session
// that starts under BUILD, automatically hands an investigation to FUZZ, and
// keeps the same loop. The artifact tool must read the live gate, not the
// workspace captured when the loop was built; otherwise FUZZ's finding is
// refused as a BUILD artifact and completion is vetoed by an unrecovered tool
// failure.
func TestArtifactToolFollowsTheLiveWorkspace(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	root := t.TempDir()
	sess, err := store.Create(root, model.Ref())
	if err != nil {
		t.Fatal(err)
	}

	gate := newAuthorityGate(workspace.Build)
	opts := options{
		root:        root,
		workspace:   workspace.Build,
		gate:        gate,
		model:       model,
		settings:    config.Default(),
		autoApprove: true,
	}
	loop, err := buildLoop(opts, ai.NewRegistry(), io.Discard, store, &sess, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}

	var artifact agent.Tool
	for _, tool := range loop.Tools() {
		if tool.Name() == "artifact" {
			artifact = tool
			break
		}
	}
	if artifact == nil {
		t.Fatal("the session tool set has no artifact tool")
	}

	// This is what harness.SwitchWorkspace does before the next turn: the
	// loop and its tools are not rebuilt.
	gate.Set(workspace.Fuzz)

	result := artifact.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"kind": "finding", "target": "backend/checkout", "status": "reproduced",
		"reproduction": "submit the checkout form", "evidence": "no order request is sent",
	})
	if result.IsError {
		t.Fatalf("FUZZ's finding was refused after the switch: %s", result.Text)
	}
	artifacts, err := store.Artifacts(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("stored %d artifacts, want 1", len(artifacts))
	}
	if artifacts[0].Origin != workspace.Fuzz {
		t.Errorf("artifact origin = %s, want the live workspace FUZZ", artifacts[0].Origin)
	}
}
