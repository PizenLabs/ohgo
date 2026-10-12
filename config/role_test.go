package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRoleModelPrecedence pins the four-layer resolution the control plane and
// the runtime both depend on: a role profile wins over a legacy workspace
// override, which wins over the global default, which falls through to the
// built-in. Getting this order wrong sends a role's turns to the wrong model
// with nothing on screen to say so.
func TestRoleModelPrecedence(t *testing.T) {
	settings := Settings{
		Model:  "anthropic/global",
		Models: map[string]string{"build": "openrouter/legacy"},
		Roles: map[string]Profile{
			"build": {Model: "openai/role", Effort: "high"},
			"sup":   {Model: "groq/support"},
		},
	}

	model, effort, source := settings.RoleModel("build")
	if model != "openai/role" || effort != "high" || source != "role" {
		t.Errorf("build = %q effort=%q source=%q, want the role profile", model, effort, source)
	}

	// FUZZ has no role and no legacy entry: the global default applies.
	model, _, source = settings.RoleModel("fuzz")
	if model != "anthropic/global" || source != "default" {
		t.Errorf("fuzz = %q source=%q, want the global default", model, source)
	}

	// A role with no entry anywhere reports the built-in fallback.
	if model, _, source = settings.RoleModel("commit"); model != "anthropic/global" || source != "default" {
		t.Errorf("commit = %q source=%q, want the global default", model, source)
	}
}

// TestRoleModelLegacyWorkspaceOverride proves a file that predates roles keeps
// resolving exactly as before. It is the backward-compatibility contract.
func TestRoleModelLegacyWorkspaceOverride(t *testing.T) {
	settings := Settings{
		Model:  "anthropic/global",
		Models: map[string]string{"build": "openrouter/legacy"},
	}
	model, _, source := settings.RoleModel("build")
	if model != "openrouter/legacy" || source != "workspace" {
		t.Errorf("build = %q source=%q, want the legacy override", model, source)
	}
}

// TestRoleModelEffortOnlyKeepsModel proves setting effort alone never unbinds a
// role's model: the effort rides along while the model resolves below it.
func TestRoleModelEffortOnlyKeepsModel(t *testing.T) {
	settings := Settings{
		Model: "anthropic/global",
		Roles: map[string]Profile{"build": {Effort: "low"}},
	}
	model, effort, _ := settings.RoleModel("build")
	if model != "anthropic/global" || effort != "low" {
		t.Errorf("build = %q effort=%q, want the global model with the role effort", model, effort)
	}
}

// TestRoleModelCaseInsensitive matches the rest of the config surface: "BUILD"
// and "build" address one profile.
func TestRoleModelCaseInsensitive(t *testing.T) {
	settings := Settings{Roles: map[string]Profile{"build": {Model: "openai/role"}}}
	if model, _, _ := settings.RoleModel("BUILD"); model != "openai/role" {
		t.Errorf("BUILD = %q, want the build profile", model)
	}
}

// TestAssignRolePersistsModelAndEffort is the durability contract: an
// interactive assignment round-trips through the file and survives a reload.
func TestAssignRolePersistsModelAndEffort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	settings := Default()
	settings.AssignRole("BUILD", "openrouter/strong-code", "high")
	if err := Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	model, effort, source := reloaded.RoleModel("build")
	if model != "openrouter/strong-code" || effort != "high" || source != "role" {
		t.Fatalf("build = %q effort=%q source=%q, want the assigned profile", model, effort, source)
	}
}

// TestAssignRoleEffortOnlyKeepsModelBinding proves an effort-only update does
// not clear a previously assigned model.
func TestAssignRoleEffortOnlyKeepsModelBinding(t *testing.T) {
	settings := Default()
	settings.AssignRole("build", "openrouter/strong-code", "high")
	settings.AssignRole("build", "", "low")
	profile := settings.Roles["build"]
	if profile.Model != "openrouter/strong-code" {
		t.Errorf("model = %q, want the earlier binding kept", profile.Model)
	}
	if profile.Effort != "low" {
		t.Errorf("effort = %q, want the new effort", profile.Effort)
	}
}

// TestAssignRoleRejectsEmptyRole keeps a blank name from silently creating a
// junk profile that every later lookup would then have to ignore.
func TestAssignRoleRejectsEmptyRole(t *testing.T) {
	settings := Default()
	settings.AssignRole("  ", "openai/x", "high")
	if len(settings.Roles) != 0 {
		t.Errorf("roles = %v, want an empty role to be ignored", settings.Roles)
	}
}

// TestSaveIsAtomicAndLeavesNoTempFiles is the persistence safety contract:
// settings are replaced in place and a failed or successful save never leaves
// a partial file or a stray temp file behind.
func TestSaveIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	settings := Default()
	settings.Model = "openai/gpt-5"
	if err := Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dir := filepath.Join(home, ".ohgo")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "settings.json" {
			t.Errorf("unexpected file %q left in %s", entry.Name(), dir)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("settings mode = %v, want 0644", perm)
	}

	// A second save replaces the first; the file stays valid JSON with the
	// new value, never a blend.
	settings.Model = "openai/gpt-6"
	if err := Save(settings); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	reloaded, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Model != "openai/gpt-6" {
		t.Errorf("model = %q, want the replacement", reloaded.Model)
	}
}

// TestRoleNamesAreCanonicalAndValid keeps the vocabulary closed: the five
// roles are recognised and nothing else is.
func TestRoleNamesAreCanonicalAndValid(t *testing.T) {
	want := []Role{RoleAsk, RoleFuzz, RoleBuild, RoleSup, RoleCommit}
	if got := RoleNames(); len(got) != len(want) {
		t.Fatalf("RoleNames = %v, want %v", got, want)
	}
	for _, role := range want {
		if !ValidRole(role) {
			t.Errorf("%q is not valid", role)
		}
	}
	if ValidRole("plan") {
		t.Error("a non-role was accepted")
	}
}
