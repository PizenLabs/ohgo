package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// modelControlHarness builds a harness whose settings are the durable
// authority under a throwaway HOME, with the session acting as BUILD.
func modelControlHarness(t *testing.T) (*harness, *config.Settings) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearProviderKeys(t)

	settings := config.Default()
	settings.Model = catalog.DefaultRef
	if err := config.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	registry := ai.NewRegistry()
	model := catalog.MustResolve(catalog.DefaultRef)
	h := newHarness(registry, nil, nil, nil, &agent.UsageLog{}, &settings, t.TempDir(), model)
	h.workspace = workspace.Build.Label()
	return h, &settings
}

// TestModelControlPersistsRoleAndResolvesIt is the core causal chain: an
// interactive assignment writes through the configuration authority, and a
// fresh load resolves the same effective model for that role — while the other
// roles are untouched.
func TestModelControlPersistsRoleAndResolvesIt(t *testing.T) {
	h, _ := modelControlHarness(t)

	applied, roles, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: "openai/gpt-5"})
	if err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if applied.Model != "openai/gpt-5" || applied.Source != "role" {
		t.Errorf("applied = %+v, want build bound to openai/gpt-5 from its role profile", applied)
	}

	// The persisted file carries the assignment.
	reloaded, err := config.Load("")
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := reloaded.Roles["build"].Model; got != "openai/gpt-5" {
		t.Fatalf("persisted build model = %q, want openai/gpt-5", got)
	}

	// A fresh resolution agrees with the UI.
	resolved, err := resolveWorkspaceModel(reloaded, workspace.Build, "", "", catalog.Resolve)
	if err != nil {
		t.Fatalf("resolve build: %v", err)
	}
	if resolved.ID != "gpt-5" {
		t.Errorf("effective BUILD = %s, want gpt-5", resolved.ID)
	}

	// The other roles did not move.
	for _, role := range []workspace.Workspace{workspace.Ask, workspace.Fuzz} {
		got, err := resolveWorkspaceModel(reloaded, role, "", "", catalog.Resolve)
		if err != nil {
			t.Fatalf("resolve %s: %v", role, err)
		}
		if got.ID != "claude-sonnet-4-5" {
			t.Errorf("effective %s = %s, want the untouched default", role, got.ID)
		}
	}

	// The refreshed role roster reports the assignment and its source.
	seen := map[string]tui.RoleAssignment{}
	for _, role := range roles {
		seen[role.Role] = role
	}
	if seen["build"].Model != "openai/gpt-5" || seen["build"].Source != "role" {
		t.Errorf("build role row = %+v, want the assignment", seen["build"])
	}
	if seen["ask"].Source != "default" {
		t.Errorf("ask role row = %+v, want the default source", seen["ask"])
	}
}

// TestAssignRoleRefusesAnUnsupportedEffort is the capability contract at the
// persistence seam: an effort the bound model does not expose is refused, and
// a refused assignment writes nothing.
func TestAssignRoleRefusesAnUnsupportedEffort(t *testing.T) {
	h, settings := modelControlHarness(t)

	// groq/llama has no thinking ladder at all.
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: "groq/llama-3.1-8b-instant", Effort: "high"}); err == nil {
		t.Error("an effort the model does not expose was accepted")
	}
	if _, ok := settings.Roles["build"]; ok {
		t.Error("a refused assignment still wrote the role")
	}

	// haiku exposes minimal..xhigh but not "bogus".
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: "anthropic/claude-haiku-4-5", Effort: "bogus"}); err == nil {
		t.Error("an effort outside the model's ladder was accepted")
	}
	// A level inside the ladder is accepted.
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: "anthropic/claude-haiku-4-5", Effort: "high"}); err != nil {
		t.Errorf("a supported effort was refused: %v", err)
	}
}

// TestAssignRoleForANonSessionRoleDoesNotRepointTheRuntime proves roles are
// independent: assigning SUP does not change what the running BUILD session
// calls, but assigning BUILD does. Roles are not one shared slot.
func TestAssignRoleForANonSessionRoleDoesNotRepointTheRuntime(t *testing.T) {
	h, _ := modelControlHarness(t)
	original := h.model

	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "sup", Model: "openai/gpt-5"}); err != nil {
		t.Fatalf("assign sup: %v", err)
	}
	if h.model.Provider != original.Provider || h.model.ID != original.ID {
		t.Errorf("runtime model = %s/%s after a SUP assignment, want BUILD untouched", h.model.Provider, h.model.ID)
	}
	if got := h.settings.Roles["sup"].Model; got != "openai/gpt-5" {
		t.Errorf("persisted sup model = %q, want the assignment recorded", got)
	}

	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: "openai/gpt-5"}); err != nil {
		t.Fatalf("assign build: %v", err)
	}
	if h.model.ID != "gpt-5" {
		t.Errorf("runtime model = %s after a BUILD assignment, want gpt-5", h.model.ID)
	}
}

// TestRoleAssignmentDrivesTheNextRequest proves the runtime receives the
// change through the loop seam: the next inference request carries the newly
// assigned model, so an assignment is configuration, not a label.
func TestRoleAssignmentDrivesTheNextRequest(t *testing.T) {
	h, _ := modelControlHarness(t)
	t.Setenv("HOME", t.TempDir())

	var requested ai.Model
	stream := func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
		requested = req.Model
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "done"}},
		}), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  h.model,
		Stream: stream,
		Tools:  []agent.Tool{stubPickerTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	h.loop = loop

	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: "openai/gpt-5"}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if _, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "hi"}}); err != nil {
		t.Fatalf("loop.Run: %v", err)
	}
	if requested.ID != "gpt-5" {
		t.Errorf("requested model = %s, want the assigned gpt-5 on the wire", requested.ID)
	}
}

// TestAssigningTheSessionRoleUpdatesTheSnapshot proves an explicit
// reassignment of the session's own role does not leave a stale session
// snapshot behind: the snapshot describes what executes, so the role pane does
// not falsely report a session override.
func TestAssigningTheSessionRoleUpdatesTheSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearProviderKeys(t)

	settings := config.Default()
	settings.Model = catalog.DefaultRef
	if err := config.Save(settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	sess, err := store.CreateWith(session.CreateOptions{
		Cwd: t.TempDir(), Model: catalog.DefaultRef, Workspace: "build",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	registry := ai.NewRegistry()
	h := newHarness(registry, nil, store, &sess, &agent.UsageLog{}, &settings, t.TempDir(),
		catalog.MustResolve(catalog.DefaultRef))
	h.workspace = "BUILD"

	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: "openai/gpt-5"}); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if h.sess.Model != "openai/gpt-5" {
		t.Errorf("session snapshot = %q, want it to follow the reassignment", h.sess.Model)
	}
	for _, row := range h.RoleAssignments() {
		if row.Role == "build" && row.SessionModel != "" && row.SessionModel != row.Model {
			t.Errorf("build row reports a stale override: session=%q model=%q", row.SessionModel, row.Model)
		}
	}
}

// TestDirectConfigEditReachesRoleAssignments is the reverse direction of the
// convergence contract: editing the file by hand shows up in the plane's role
// roster without any interactive involvement.
func TestDirectConfigEditReachesRoleAssignments(t *testing.T) {
	h, settings := modelControlHarness(t)
	settings.Roles = map[string]config.Profile{
		"build": {Model: "openai/gpt-5", Effort: "low"},
	}
	settings.Models = map[string]string{"ask": "groq/llama-3.1-8b-instant"}
	if err := config.Save(*settings); err != nil {
		t.Fatalf("save: %v", err)
	}

	rows := map[string]tui.RoleAssignment{}
	for _, row := range h.RoleAssignments() {
		rows[row.Role] = row
	}
	if got := rows["build"]; got.Model != "openai/gpt-5" || got.Effort != "low" || got.Source != "role" {
		t.Errorf("build row = %+v, want the hand-edited profile", got)
	}
	if got := rows["ask"]; got.Model != "groq/llama-3.1-8b-instant" || got.Source != "workspace" {
		t.Errorf("ask row = %+v, want the legacy workspace override", got)
	}
}

// TestReplModelSwitchPersistsAndKeepsEffort proves the plain REPL converges on
// the same authority as the control plane: /model records the choice in the
// session's role and does not clobber a configured thinking effort.
func TestReplModelSwitchPersistsAndKeepsEffort(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	h, settings := modelControlHarness(t)
	settings.Roles = map[string]config.Profile{"build": {Model: catalog.DefaultRef, Effort: "high"}}
	if err := config.Save(*settings); err != nil {
		t.Fatalf("save: %v", err)
	}

	in := strings.NewReader("/model openai/gpt-5\n/quit\n")
	var out strings.Builder
	if err := repl(h, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	if !strings.Contains(out.String(), "Switched to openai/gpt-5") {
		t.Fatalf("output = %q, want the switch confirmation", out.String())
	}

	reloaded, err := config.Load("")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	profile := reloaded.Roles["build"]
	if profile.Model != "openai/gpt-5" {
		t.Errorf("persisted build model = %q, want the switch recorded", profile.Model)
	}
	if profile.Effort != "high" {
		t.Errorf("persisted build effort = %q, want it preserved across a model switch", profile.Effort)
	}
}
