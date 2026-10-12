package main

import (
	"io"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/tui"
)

const (
	supSonnet = "anthropic/claude-sonnet-4-5"
	supHaiku  = "anthropic/claude-haiku-4-5"
)

// TestSupResolvesSupRoleNotWorkspace proves $sup model resolution is
// explicit: the SUP role decides, never whatever model the main loop
// happens to hold.
func TestSupResolvesSupRoleNotWorkspace(t *testing.T) {
	registry := ai.NewRegistry()
	mkOpts := func(settings config.Settings) options {
		return options{root: t.TempDir(), model: catalog.MustResolve(supSonnet), settings: settings, autoApprove: true}
	}

	// SUP explicitly assigned: it wins over the workspace model.
	settings := config.Default()
	settings.Model = supSonnet
	settings.AssignRole("sup", supHaiku, "")
	bundle, err := supportLoopBuilder(mkOpts(settings), registry, io.Discard)()
	if err != nil {
		t.Fatalf("supportLoopBuilder: %v", err)
	}
	if bundle.ref != supHaiku {
		t.Errorf("support model = %q, want the SUP role %q", bundle.ref, supHaiku)
	}

	// SUP unassigned but ASK assigned differently: SUP falls back through
	// the canonical chain (global default), not to the ASK model.
	settings = config.Default()
	settings.Model = supSonnet
	settings.AssignRole("ask", supHaiku, "")
	bundle, err = supportLoopBuilder(mkOpts(settings), registry, io.Discard)()
	if err != nil {
		t.Fatalf("supportLoopBuilder: %v", err)
	}
	if bundle.ref != supSonnet {
		t.Errorf("support model = %q, want the SUP fallback %q, not the ASK model", bundle.ref, supSonnet)
	}
}

// TestSupEffortFollowsSupRole proves the SUP effort travels with the SUP
// role rather than the workspace's flags.
func TestSupEffortFollowsSupRole(t *testing.T) {
	registry := ai.NewRegistry()
	settings := config.Default()
	settings.Model = supSonnet
	settings.AssignRole("sup", supHaiku, "minimal")
	opts := options{root: t.TempDir(), model: catalog.MustResolve(supSonnet), settings: settings, autoApprove: true, effort: "high", reasoning: true}
	bundle, err := supportLoopBuilder(opts, registry, io.Discard)()
	if err != nil {
		t.Fatalf("supportLoopBuilder: %v", err)
	}
	if bundle.effort != "minimal" {
		t.Errorf("support effort = %q, want the SUP role effort %q", bundle.effort, "minimal")
	}
}

// TestSupRejectsUnknownEffort proves the runtime refuses an effort the
// bound SUP model does not expose, through the same Supports check the
// /model assignment seam uses — never a silent downgrade.
func TestSupRejectsUnknownEffort(t *testing.T) {
	registry := ai.NewRegistry()
	settings := config.Default()
	settings.Model = supSonnet
	settings.AssignRole("sup", supSonnet, "bogus-effort")
	opts := options{root: t.TempDir(), model: catalog.MustResolve(supSonnet), settings: settings, autoApprove: true}
	_, err := supportLoopBuilder(opts, registry, io.Discard)()
	if err == nil || !strings.Contains(err.Error(), "bogus-effort") {
		t.Fatalf("supportLoopBuilder err = %v, want a refusal naming the effort", err)
	}
}

// TestSupRoleAssignmentIsIndependent proves SUP is an independently
// configurable role: changing SUP never mutates ASK/FUZZ/BUILD, and
// changing BUILD never mutates SUP.
func TestSupRoleAssignmentIsIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registry := ai.NewRegistry()
	settings := config.Default()
	settings.Model = supSonnet
	h := newHarness(registry, nil, nil, nil, nil, &settings, t.TempDir(), catalog.MustResolve(supSonnet))

	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "sup", Model: supHaiku}); err != nil {
		t.Fatalf("assign sup: %v", err)
	}
	if model, _, _ := settings.RoleModel("sup"); model != supHaiku {
		t.Errorf("sup = %q, want %q", model, supHaiku)
	}
	for _, role := range []string{"ask", "fuzz", "build"} {
		if model, _, _ := settings.RoleModel(role); model == supHaiku {
			t.Errorf("%s = %q, want it untouched by the SUP assignment", role, model)
		}
	}

	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "build", Model: supHaiku}); err != nil {
		t.Fatalf("assign build: %v", err)
	}
	if model, _, _ := settings.RoleModel("sup"); model != supHaiku {
		t.Errorf("sup = %q, want it untouched by the BUILD assignment", model)
	}
}

// TestSupInfoMatchesRuntime proves the overlay identity line reports the
// resolved SUP configuration, never a stale copy.
func TestSupInfoMatchesRuntime(t *testing.T) {
	registry := ai.NewRegistry()
	settings := config.Default()
	settings.Model = supSonnet
	settings.AssignRole("sup", supHaiku, "minimal")
	h := newHarness(registry, nil, nil, nil, nil, &settings, t.TempDir(), catalog.MustResolve(supSonnet))
	model, effort := h.SupportInfo()
	if model != supHaiku || effort != "minimal" {
		t.Errorf("SupportInfo = %q/%q, want %q/minimal", model, effort, supHaiku)
	}
}

// TestSupRebuildsAfterRoleChange proves no restart is required: the next
// $sup invocation resolves the reassigned SUP model.
func TestSupRebuildsAfterRoleChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registry := ai.NewRegistry()
	settings := config.Default()
	settings.Model = supSonnet
	h := newHarness(registry, nil, nil, nil, nil, &settings, t.TempDir(), catalog.MustResolve(supSonnet))
	opts := options{root: t.TempDir(), model: catalog.MustResolve(supSonnet), settings: settings, autoApprove: true}
	build := supportLoopBuilder(opts, registry, io.Discard)

	first, err := build()
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if first.ref != supSonnet {
		t.Fatalf("first support model = %q, want %q", first.ref, supSonnet)
	}
	if _, _, err := h.AssignRole(tui.RoleAssignment{Role: "sup", Model: supHaiku}); err != nil {
		t.Fatalf("assign sup: %v", err)
	}
	// The harness owns a copy of the settings struct, so hand the builder
	// the updated authority the same way runSession does.
	opts.settings = settings
	build = supportLoopBuilder(opts, registry, io.Discard)
	second, err := build()
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if second.ref != supHaiku {
		t.Errorf("second support model = %q, want the reassigned %q", second.ref, supHaiku)
	}
}
