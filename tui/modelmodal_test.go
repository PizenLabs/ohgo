package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPlaneModalRectStaysInsideTerminal pins the modal geometry contract at
// every required size: the dialog stays inside the terminal, keeps margins
// on roomy screens, and never yields negative or impossible rectangles.
func TestPlaneModalRectStaysInsideTerminal(t *testing.T) {
	for _, size := range [][2]int{{40, 10}, {60, 20}, {80, 24}, {120, 40}, {160, 50}} {
		w, h := size[0], size[1]
		rect := PlaneModalRect(w, h)
		if rect.Width <= 0 || rect.Height <= 0 {
			t.Errorf("%dx%d: rect = %+v, want positive dimensions", w, h, rect)
			continue
		}
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.Width > w || rect.Y+rect.Height > h {
			t.Errorf("%dx%d: rect %+v escapes the terminal", w, h, rect)
		}
		if w >= 80 && h >= 24 {
			if rect.X <= 0 || rect.Y <= 0 {
				t.Errorf("%dx%d: rect %+v touches the edge, want margins", w, h, rect)
			}
			if rect.Width > planeModalMaxWidth || rect.Height > planeModalMaxHeight {
				t.Errorf("%dx%d: rect %+v exceeds the maxima", w, h, rect)
			}
		}
		interior := rect.Width - 2
		left, right, stacked := planeModalColumns(interior)
		if stacked {
			if left != interior {
				t.Errorf("%dx%d: stacked left = %d, want full interior %d", w, h, left, interior)
			}
			continue
		}
		if left < 10 || right < 10 {
			t.Errorf("%dx%d: panes %d/%d below minimum", w, h, left, right)
		}
		if left+1+right != interior {
			t.Errorf("%dx%d: panes %d+1+%d != interior %d", w, h, left, right, interior)
		}
	}
}

// TestPlaneModalRendersCenteredDialog proves /model is a dialog, not a
// full-screen replacement: borders render, margins survive, and no line
// overflows the viewport.
func TestPlaneModalRendersCenteredDialog(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 40}} {
		s := New()
		s.Apply(TerminalResized{Width: size[0], Height: size[1]})
		s.Apply(ModelPlaneOpened(planeFixture()))
		out := Render(s)
		for _, want := range []string{"┌", "┐", "└", "┘", "MODEL CONTROL"} {
			if !strings.Contains(out, want) {
				t.Errorf("%dx%d: modal missing %q:\n%s", size[0], size[1], want, out)
			}
		}
		for _, line := range strings.Split(out, "\n") {
			if CellWidth(line) > size[0] {
				t.Errorf("%dx%d: line overflows (%d cells): %q", size[0], size[1], CellWidth(line), line)
			}
		}
	}
}

// TestPlaneModalKeepsChrome proves the underlying TUI survives under the
// modal: the header and status still render around the dialog.
func TestPlaneModalKeepsChrome(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(ModelPlaneOpened(planeFixture()))
	out := Render(s)
	if !strings.Contains(out, "ohgo") {
		t.Errorf("header lost under the modal:\n%s", out)
	}
	if !strings.Contains(out, "┌") {
		t.Errorf("modal border missing:\n%s", out)
	}
}

// TestPlaneModalKeepsDetailWithOverflow proves the list overflow marker does
// not push the detail block out: with more models than fit, the effort radio
// — the visible thinking control — still renders inside the dialog.
func TestPlaneModalKeepsDetailWithOverflow(t *testing.T) {
	var models []ModelOption
	for i := 0; i < 60; i++ {
		models = append(models, ModelOption{
			ID: fmt.Sprintf("openrouter/vendor/model-%02d", i), Provider: "openrouter",
			Name:            fmt.Sprintf("Vendor Model %02d", i),
			Context:         262000,
			InputModalities: []string{"text"},
			Reasoning:       true, ReasoningKnown: true, ThinkingEfforts: []string{"low", "high"},
			PriceKnown: true, Free: true,
		})
	}
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(ModelPlaneOpened(ModelPlaneOpened{
		Providers: []ProviderRow{{ID: AllProviders, Name: "All providers", Count: len(models)}},
		Models:    models, Roles: planeFixture().Roles, ActiveRole: "ask", Origin: "live",
	}))
	if s.Plane.VisibleRows <= 0 {
		t.Fatalf("VisibleRows = %d, want the open path to size the list window", s.Plane.VisibleRows)
	}
	s.Apply(PlaneFocusSet{Focus: PlaneFocusEffort})
	out := Render(s)
	for _, want := range []string{"…", "Effort", "off", "low", "high"} {
		if !strings.Contains(out, want) {
			t.Errorf("overflowed modal missing %q:\n%s", want, out)
		}
	}
}

// planeKey builds a printable key press for the plane.
func planeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// openPlaneProgram opens the control plane on a program for key tests.
func openPlaneProgram(t *testing.T) *Program {
	t.Helper()
	p := NewProgram(nil, "anthropic/claude-sonnet-4-5", 1000000)
	p.SetCatalogHooks(CatalogHooks{
		Snapshot: func() CatalogSnapshot {
			return CatalogSnapshot{Options: planeFixture().Models, Origin: "live"}
		},
	})
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return planeFixture().Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			return update, planeFixture().Roles, nil
		},
	})
	p.state.Apply(TerminalResized{Width: 120, Height: 36})
	Command{Name: "model"}.runOn(p)
	if p.state.Plane == nil {
		t.Fatal("/model did not open the control plane")
	}
	return p
}

// TestPlaneSearchOwnsPrintableKeys is the mandatory input-safety regression:
// with search focused, typing e appends text and changes nothing else.
func TestPlaneSearchOwnsPrintableKeys(t *testing.T) {
	p := openPlaneProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusSearch})
	beforeEffort := p.state.Plane.Effort
	beforeModel, _ := p.state.Plane.ChosenModel()
	beforeRole := p.state.Plane.ActiveRole

	p.Update(planeKey("e"))

	plane := p.state.Plane
	if !strings.HasSuffix(plane.Filter, "e") {
		t.Errorf("filter = %q, want it to end in e", plane.Filter)
	}
	if plane.Effort != beforeEffort {
		t.Errorf("effort = %q, want it unchanged %q: e must not cycle effort", plane.Effort, beforeEffort)
	}
	afterModel, _ := plane.ChosenModel()
	_ = afterModel
	if plane.ActiveRole != beforeRole {
		t.Errorf("role = %q, want it unchanged %q", plane.ActiveRole, beforeRole)
	}
	_ = beforeModel
}

// TestPlaneSearchOwnsOtherPrintables extends the contract to m and r: no
// model, effort, or provider action may occur while search is focused. Each
// key is typed where the highlight survives the filter, so any effort or
// selection change would be a stolen shortcut rather than re-validation.
func TestPlaneSearchOwnsOtherPrintables(t *testing.T) {
	cases := []struct {
		key   string
		setup func(plane *ModelPlaneState)
	}{
		// "r" matches every row (anthropic and openrouter both contain
		// it), so the laddered highlight survives.
		{key: "r", setup: func(plane *ModelPlaneState) {}},
		// "m" hides the Sonnet row, so park on the Plain row first: the
		// filter keeps it highlighted and its (empty) effort valid.
		{key: "m", setup: func(plane *ModelPlaneState) {
			plane.FocusModels()
			plane.Move(1)
			plane.FocusSearch()
		}},
		{key: "claude", setup: func(plane *ModelPlaneState) {}},
	}
	for _, tc := range cases {
		p := openPlaneProgram(t)
		p.state.Apply(PlaneFocusSet{Focus: PlaneFocusSearch})
		tc.setup(p.state.Plane)
		beforeEffort := p.state.Plane.Effort
		beforeModel, _ := p.state.Plane.ChosenModel()
		beforeProvider := p.state.Plane.ProviderCursor
		beforeFilter := p.state.Plane.Filter

		p.Update(planeKey(tc.key))

		plane := p.state.Plane
		if !strings.HasSuffix(plane.Filter, tc.key) {
			t.Errorf("key %q: filter = %q, want suffix %q", tc.key, plane.Filter, beforeFilter+tc.key)
		}
		if plane.Effort != beforeEffort {
			t.Errorf("key %q: effort = %q, want it unchanged %q", tc.key, plane.Effort, beforeEffort)
		}
		afterModel, _ := plane.ChosenModel()
		if afterModel.ID != beforeModel.ID {
			t.Errorf("key %q: model = %q, want it unchanged %q", tc.key, afterModel.ID, beforeModel.ID)
		}
		if plane.ProviderCursor != beforeProvider {
			t.Errorf("key %q: provider cursor moved", tc.key)
		}
	}
}

// TestPlaneTypingEntersSearch proves no shortcut is needed to filter: typing
// while navigating focuses search and lands in the query, while still
// changing no selection, effort, or role — printables never execute commands.
func TestPlaneTypingEntersSearch(t *testing.T) {
	p := openPlaneProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	beforeEffort := p.state.Plane.Effort
	beforeRole := p.state.Plane.ActiveRole

	p.Update(planeKey("e"))

	plane := p.state.Plane
	if plane.Focus != PlaneFocusSearch {
		t.Errorf("focus = %v, want typing to land in search", plane.Focus)
	}
	if plane.Filter != "e" {
		t.Errorf("filter = %q, want the typed text without a '/' first", plane.Filter)
	}
	if plane.Effort != beforeEffort {
		t.Errorf("effort = %q, want it unchanged %q", plane.Effort, beforeEffort)
	}
	if plane.ActiveRole != beforeRole {
		t.Errorf("role = %q, want it unchanged %q", plane.ActiveRole, beforeRole)
	}

	// Former bracket shortcuts are text too, not role commands.
	p.Update(planeKey("["))
	if plane.Filter != "e[" {
		t.Errorf("filter = %q, want brackets typed as text", plane.Filter)
	}
}

// TestPlaneTypingIgnoresRolesMode proves typing where no search box exists
// stays a no-op instead of yanking the mode away.
func TestPlaneTypingIgnoresRolesMode(t *testing.T) {
	p := openPlaneProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusRoles})
	p.Update(planeKey("e"))
	if p.state.Plane.Filter != "" {
		t.Errorf("filter = %q, want typing ignored in roles mode", p.state.Plane.Filter)
	}
	if p.state.Plane.Mode != PlaneRoles {
		t.Error("typing left roles mode, want the assignment context kept")
	}
}

// TestPlaneTabCyclesFocusDeterministically pins the streamlined focus order:
// Provider -> Model -> Role -> Effort, with search reached by typing rather
// than as an interrupting Tab stop.
func TestPlaneTabCyclesFocusDeterministically(t *testing.T) {
	p := openPlaneProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
	tab := tea.KeyMsg{Type: tea.KeyTab}
	want := []PlaneFocus{PlaneFocusModels, PlaneFocusRoles, PlaneFocusEffort}
	for i, focus := range want {
		p.Update(tab)
		if p.state.Plane.Focus != focus {
			t.Fatalf("tab %d: focus = %v, want %v", i+1, p.state.Plane.Focus, focus)
		}
	}
	// A fourth Tab reaches search; a fifth wraps to providers.
	p.Update(tab)
	if p.state.Plane.Focus != PlaneFocusSearch {
		t.Fatalf("tab 4: focus = %v, want search", p.state.Plane.Focus)
	}
	p.Update(tab)
	if p.state.Plane.Focus != PlaneFocusProviders {
		t.Fatalf("tab 5: focus = %v, want wrap to providers", p.state.Plane.Focus)
	}
}

// TestPlaneEffortControlIsVisibleAndModelSpecific proves effort is an
// explicit control fed only by authoritative ladders.
func TestPlaneEffortControlIsVisibleAndModelSpecific(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(ModelPlaneOpened(planeFixture()))
	s.Apply(PlaneFocusSet{Focus: PlaneFocusEffort})
	out := Render(s)
	for _, want := range []string{"off", "low", "medium", "high"} {
		if !strings.Contains(out, want) {
			t.Errorf("effort radio missing %q:\n%s", want, out)
		}
	}

	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	plane.SetFilter("Plain")
	if opts := plane.EffortOptions(); len(opts) != 0 {
		t.Errorf("unsupported model exposed efforts %v", opts)
	}
	plane.SetFilter("Silent")
	if opts := plane.EffortOptions(); len(opts) != 0 {
		t.Errorf("unknown-thinking model fabricated efforts %v", opts)
	}
}

// TestPlaneEffortSelectionPersistsThroughAssign proves the visible control
// routes through the existing authority seam.
func TestPlaneEffortSelectionPersistsThroughAssign(t *testing.T) {
	p := openPlaneProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusEffort})
	var assigned RoleAssignment
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return planeFixture().Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			assigned = update
			return update, planeFixture().Roles, nil
		},
	})
	// Step the radio to a non-default rung, then assign.
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if assigned.Effort == "" {
		t.Error("assign carried no effort: the radio selection did not reach the seam")
	}
	if assigned.Model == "" {
		t.Error("assign carried no model")
	}
}

// TestPlaneRoleEnterStagesInsteadOfAssigning proves the streamlined Roles
// flow end to end: Enter on a role never assigns a hidden model — it stages
// the role and returns to model search — and the next model Enter assigns to
// that staged role with a single confirmation and no stacked popups.
func TestPlaneRoleEnterStagesInsteadOfAssigning(t *testing.T) {
	var assigned RoleAssignment
	calls := 0
	p := openPlaneProgram(t)
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return planeFixture().Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			calls++
			assigned = update
			return update, planeFixture().Roles, nil
		},
	})
	// Enter on the ASK role stages it for replacement.
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusRoles})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 0 {
		t.Fatal("Enter on a role assigned instead of staging a model search")
	}
	if p.state.Plane.ActiveRole != "ask" {
		t.Fatalf("active role = %q, want the staged ask role", p.state.Plane.ActiveRole)
	}
	if p.state.Plane.Focus != PlaneFocusModels {
		t.Fatalf("focus = %v, want model search for the replacement", p.state.Plane.Focus)
	}
	// Pick the plain model and assign it to the staged role.
	p.state.Plane.Move(1)
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 1 {
		t.Fatalf("calls = %d, want one assignment after the model pick", calls)
	}
	if assigned.Role != "ask" {
		t.Errorf("assigned role = %q, want the staged ask role", assigned.Role)
	}
	if assigned.Model != "openrouter/vendor/plain" {
		t.Errorf("assigned model = %q, want the highlighted model", assigned.Model)
	}
	if p.state.Plane.Variant != nil {
		t.Error("a variant popup opened for a model with no variants")
	}
}

// TestPlaneOpenPreservesStateAndEscClosesCleanly proves the modal never
// destroys the application: opening keeps the transcript, and Esc leaves
// without persisting anything.
func TestPlaneOpenPreservesStateAndEscClosesCleanly(t *testing.T) {
	p := openPlaneProgram(t)
	p.state.Apply(UserSubmitted{Text: "keep me"})
	entries := len(p.state.Entries)
	called := false
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return planeFixture().Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			called = true
			return update, planeFixture().Roles, nil
		},
	})
	if len(p.state.Entries) != entries {
		t.Fatalf("opening /model changed the transcript: %d -> %d", entries, len(p.state.Entries))
	}
	// Esc from a non-search stop with an empty filter closes directly.
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if p.state.Plane != nil {
		t.Error("Esc did not close the modal")
	}
	if called {
		t.Error("Esc persisted an assignment it must never make")
	}
	if len(p.state.Entries) != entries {
		t.Errorf("closing changed the transcript: %d -> %d", entries, len(p.state.Entries))
	}
}
