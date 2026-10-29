package main

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// Model references shared by the assignment tests: one with an authoritative
// thinking ladder and one with none.
const (
	assignLaddered = "anthropic/claude-sonnet-4-5"
	assignNoLadder = "groq/llama-3.1-8b-instant"
)

// TestAssignRoleCoversEveryRole proves the single assignment seam is
// role-agnostic: all five roles persist a valid assignment through the same
// path, and no role owns a private mutation path.
func TestAssignRoleCoversEveryRole(t *testing.T) {
	h, _ := modelControlHarness(t)
	for _, role := range config.RoleNames() {
		if _, _, err := h.AssignRole(tui.RoleAssignment{Role: string(role), Model: assignLaddered, Effort: "low"}); err != nil {
			t.Fatalf("assign %s: %v", role, err)
		}
	}
	reloaded, err := config.Load("")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, role := range config.RoleNames() {
		profile := reloaded.Roles[string(role)]
		if profile.Model != assignLaddered || profile.Effort != "low" {
			t.Errorf("%s = %+v, want %s/low", role, profile, assignLaddered)
		}
	}
}

// TestAssignRoleRejectsAnEffortTheModelLacks is the persisted-assignment
// invariant at the authority seam: a model + effort combination the catalog
// says is impossible is refused, and nothing is written.
func TestAssignRoleRejectsAnEffortTheModelLacks(t *testing.T) {
	h, settings := modelControlHarness(t)
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "sup", Model: assignNoLadder, Effort: "medium"}); err == nil {
		t.Fatal("an impossible model+effort was persisted")
	}
	if _, ok := settings.Roles["sup"]; ok {
		t.Error("a refused assignment wrote the profile")
	}
}

// TestModelReplacementDropsAnIncompatibleEffort is the Model Replacement Rule:
// an old model's effort must never survive onto a new model that does not
// expose it. The new model resolves to no effort, so the plane stages "" rather
// than carrying the stale level.
func TestModelReplacementDropsAnIncompatibleEffort(t *testing.T) {
	h, _ := modelControlHarness(t)
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: assignLaddered, Effort: "medium"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: assignNoLadder}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	reloaded, err := config.Load("")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	profile := reloaded.Roles["build"]
	if profile.Model != assignNoLadder {
		t.Fatalf("model = %q, want %q", profile.Model, assignNoLadder)
	}
	if profile.Effort != "" {
		t.Errorf("effort = %q, want the incompatible claim dropped with the old model", profile.Effort)
	}
}

// TestReplModelSwitchDoesNotKeepAnIncompatibleEffort proves the plain REPL's
// /model converges on the same invariant: switching a role to a model that does
// not expose the role's effort clears the claim instead of persisting it.
func TestReplModelSwitchDoesNotKeepAnIncompatibleEffort(t *testing.T) {
	h, settings := modelControlHarness(t)
	settings.Roles = map[string]config.Profile{"build": {Model: assignLaddered, Effort: "medium"}}
	if err := config.Save(*settings); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := h.SetModel(assignNoLadder); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	reloaded, err := config.Load("")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	profile := reloaded.Roles["build"]
	if profile.Model != assignNoLadder {
		t.Fatalf("model = %q, want the switch recorded", profile.Model)
	}
	if profile.Effort != "" {
		t.Errorf("effort = %q, want the incompatible claim dropped", profile.Effort)
	}
}

// TestRoleAssignmentDoesNotChangeTheWorkspace proves role assignment and
// workspace authority stay separate: assigning SUP never moves the running
// workspace.
func TestRoleAssignmentDoesNotChangeTheWorkspace(t *testing.T) {
	h, _ := modelControlHarness(t)
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "sup", Model: assignLaddered, Effort: "low"}); err != nil {
		t.Fatalf("assign sup: %v", err)
	}
	if h.workspace != workspace.Build.Label() {
		t.Errorf("workspace = %q, want %q unchanged", h.workspace, workspace.Build.Label())
	}
}

// TestSupCannotPersistAnUnsupportedEffort is the SUP regression at the picker
// seam: the failure shape from the field — a SUP model bound to an effort it
// does not expose — can no longer be persisted, while a valid one can.
func TestSupCannotPersistAnUnsupportedEffort(t *testing.T) {
	h, settings := modelControlHarness(t)
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "sup", Model: assignNoLadder, Effort: "medium"}); err == nil {
		t.Fatal("SUP accepted an effort its model does not expose")
	}
	if _, ok := settings.Roles["sup"]; ok {
		t.Error("a refused SUP assignment still wrote the profile")
	}

	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "sup", Model: assignLaddered, Effort: "minimal"}); err != nil {
		t.Fatalf("a valid SUP assignment was refused: %v", err)
	}
	reloaded, err := config.Load("")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Roles["sup"]; got.Model != assignLaddered || got.Effort != "minimal" {
		t.Errorf("persisted sup = %+v, want %s/minimal", got, assignLaddered)
	}
}

// TestSupInheritedDefaultEffortIsNotValidatedWhenThinkingDisabled is the
// runtime half of the regression: with thinking globally disabled, the global
// effort never reaches the provider, so an unsupported default must not be
// validated and must not fail $sup. This reproduces the field shape — SUP
// bound to a model with no "medium", reasoningEffort "medium" — and proves it
// runs.
func TestSupInheritedDefaultEffortIsNotValidatedWhenThinkingDisabled(t *testing.T) {
	settings := config.Default() // Reasoning false, ReasoningEffort "medium"
	settings.Model = supSonnet
	settings.AssignRole("sup", assignNoLadder, "")
	_, effort, err := resolveSupModel(settings, catalog.Resolve)
	if err != nil {
		t.Fatalf("$sup refused an inherited default that is never sent: %v", err)
	}
	if effort != "" {
		t.Errorf("effort = %q, want none while thinking is disabled", effort)
	}
}

// TestSupInheritedDefaultEffortIsValidatedWhenThinkingEnabled keeps the strict
// boundary: an enabled global default the SUP model does not expose is still
// refused truthfully.
func TestSupInheritedDefaultEffortIsValidatedWhenThinkingEnabled(t *testing.T) {
	settings := config.Default()
	settings.Model = supSonnet
	settings.Reasoning = true
	settings.ReasoningEffort = "high"
	settings.AssignRole("sup", assignNoLadder, "")
	if _, _, err := resolveSupModel(settings, catalog.Resolve); err == nil {
		t.Fatal("an enabled, unsupported inherited effort was accepted")
	}
}
