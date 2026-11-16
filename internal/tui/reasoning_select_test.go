package tui

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// The host selects reasoning for the turn; the program applies it to the live
// loop and projects the level. It never decides the level itself.
func TestReasoningSelectorIsAppliedBeforeTheTurn(t *testing.T) {
	p := NewProgram(scriptedLoop(t, "done"), "test", 4096)
	var gotIntent workspace.IntentContract
	var gotText string
	p.SetReasoningSelector(func(intent workspace.IntentContract, text string, request []agent.Message) (bool, string) {
		gotIntent, gotText = intent, text
		return true, "high"
	})

	intent := workspace.BuildIntentContract("Refactor the task lifecycle and run go test ./...")
	p.applyReasoning(intent, "Refactor the task lifecycle and run go test ./...", nil)
	if gotIntent.Objective != intent.Objective || gotText != intent.Objective {
		t.Fatalf("selector saw %q/%q, want the turn's contract and text", gotIntent.Objective, gotText)
	}
	if p.state.Status.Effort != "high" {
		t.Fatalf("header effort = %q, want the applied level", p.state.Status.Effort)
	}

	// An omitted setting must clear the badge instead of leaving a stale level
	// that the loop is no longer using.
	p.SetReasoningSelector(func(workspace.IntentContract, string, []agent.Message) (bool, string) {
		return false, ""
	})
	p.applyReasoning(intent, "simple", nil)
	if p.state.Status.Effort != "" {
		t.Fatalf("header effort = %q, want an omitted reasoning setting", p.state.Status.Effort)
	}
}

// A program without a selector keeps the role's configured effort: adding the
// seam must not silently replace manual configuration in every build.
func TestReasoningSelectorNilLeavesConfiguredEffort(t *testing.T) {
	p := NewProgram(scriptedLoop(t, "done"), "test", 4096, WithEffort("medium"))
	p.applyReasoning(workspace.BuildIntentContract("rename it"), "rename it", nil)
	if p.state.Status.Effort != "medium" {
		t.Fatalf("header effort = %q, want the configured level", p.state.Status.Effort)
	}
}
