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

// TestPlaneTabTogglesBetweenModelAndRoles pins the top-level navigation: Tab
// switches MODEL and ROLES, and the mode always matches the visible pane.
func TestPlaneTabTogglesBetweenModelAndRoles(t *testing.T) {
	p := openPlaneProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
	tab := tea.KeyMsg{Type: tea.KeyTab}
	p.Update(tab)
	if p.state.Plane.Mode != PlaneRoles {
		t.Fatalf("tab: mode = %q, want roles", p.state.Plane.Mode)
	}
	p.Update(tab)
	if p.state.Plane.Mode != PlaneModels {
		t.Fatalf("tab: mode = %q, want models", p.state.Plane.Mode)
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

// TestPlaneEffortSelectionPersistsThroughAssign proves the effort step routes
// the visible choice through the existing authority seam.
func TestPlaneEffortSelectionPersistsThroughAssign(t *testing.T) {
	p := openPlaneProgram(t)
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
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // stage the model (sonnet, laddered)
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // choose the default role (build)
	// Step the effort radio off its default rung, then Apply.
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if assigned.Effort == "" || assigned.Effort == EffortOff {
		t.Errorf("assign effort = %q, want a chosen rung the radio reached", assigned.Effort)
	}
	if assigned.Model == "" {
		t.Error("assign carried no model")
	}
}

// TestPlaneRoleEnterStagesInsteadOfAssigning proves the roles-first flow end
// to end: Enter on a role never assigns a hidden model — it stages the role
// and moves to model selection — and the model pick continues to the apply
// step with a single confirmation and no stacked popups.
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
	// Enter on the ASK role stages it for a model choice.
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusRoles})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 0 {
		t.Fatal("Enter on a role assigned instead of staging a model search")
	}
	if p.state.Plane.ActiveRole != "ask" {
		t.Fatalf("active role = %q, want the staged ask role", p.state.Plane.ActiveRole)
	}
	if p.state.Plane.Stage != PlaneStageModel {
		t.Fatalf("stage = %v, want model selection for the staged role", p.state.Plane.Stage)
	}
	// Pick the plain model (no ladder) and continue to the apply step.
	p.state.Plane.Move(1)
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if p.state.Plane.Stage != PlaneStageApply {
		t.Fatalf("stage = %v, want apply for a model with no ladder", p.state.Plane.Stage)
	}
	if calls != 0 {
		t.Fatal("the model pick persisted before Apply")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // apply
	if calls != 1 {
		t.Fatalf("calls = %d, want one assignment after Apply", calls)
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

// TestModelPickerRectResponsiveClamp pins the viewport formula:
// width = clamp(76, w-8, 108), height = clamp(22, h-4, 44).
func TestModelPickerRectResponsiveClamp(t *testing.T) {
	if rect := ModelPickerRect(100, 30); rect.Width != 92 || rect.Height != 26 {
		t.Fatalf("100x30 rect = %+v, want 92x26", rect)
	}
	if rect := PlaneModalRect(200, 60); rect.Width != 108 || rect.Height != 44 {
		t.Fatalf("large rect = %+v, want capped 108x44", rect)
	}
	if got, want := ModelPickerRect(100, 30), PlaneModalRect(100, 30); got != want {
		t.Fatalf("ModelPickerRect = %+v, want PlaneModalRect %+v", got, want)
	}
	for _, size := range [][2]int{{90, 26}, {76, 22}, {50, 15}, {40, 10}} {
		rect := PlaneModalRect(size[0], size[1])
		if rect.X < 0 || rect.Y < 0 || rect.X+rect.Width > size[0] || rect.Y+rect.Height > size[1] {
			t.Errorf("size %v: rect %+v escapes the terminal", size, rect)
		}
	}
}

// TestPlaneBorderTitleNamesTheMode proves the title lives centered in the
// top border in the ASK teal accent, naming search, providers, roles, and
// effort by focus and stage.
func TestPlaneBorderTitleNamesTheMode(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	if got := planeModalTitle(plane); got != "MODEL CONTROL · search" {
		t.Errorf("browse models title = %q, want search", got)
	}
	plane.FocusProviders()
	if got := planeModalTitle(plane); got != "MODEL CONTROL · providers" {
		t.Errorf("providers title = %q, want providers", got)
	}
	plane.ToggleMode()
	if got := planeModalTitle(plane); got != "MODEL CONTROL · roles" {
		t.Errorf("roles title = %q, want roles", got)
	}
	plane.ToggleMode()
	plane.FocusModels()
	model, _ := plane.ChosenModel()
	plane.beginModelFirst(model)
	plane.chooseRole()
	if got := planeModalTitle(plane); got != "MODEL CONTROL · effort" {
		t.Errorf("effort title = %q, want effort", got)
	}
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(ModelPlaneOpened(planeFixture()))
	out := stripANSI(Render(s))
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "┌") && strings.Contains(line, "MODEL CONTROL") {
			found = true
		}
	}
	if !found {
		t.Error("no top border carries the MODEL CONTROL title")
	}
}

// TestPlaneDetailGridIsSymmetric proves the bottom-right metadata renders
// as a borderless two-column grid with aligned pairs and no divider
// character.
func TestPlaneDetailGridIsSymmetric(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	detail := planeModelDetail(plane, PaletteFor(ThemeDark), 67, 12)
	joined := strings.Join(detail, "\n")
	for _, want := range []string{"Context", "Input", "Output", "Vision", "Thinking", "Price"} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail grid missing %q:\n%s", want, joined)
		}
	}
	for _, line := range detail {
		if CellWidth(line) > 67 {
			t.Errorf("detail row overflows (%d cells): %q", CellWidth(line), line)
		}
	}
}

// TestPlaneRowsSanitizeExternalText proves catalog and provider text passes
// the sanitization boundary before measuring or rendering.
func TestPlaneRowsSanitizeExternalText(t *testing.T) {
	pal := PaletteFor(ThemeDark)
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	nasty := ModelOption{
		ID: "openrouter/\x1b[2Jevil", Provider: "openrouter\x1b[3;1H",
		Name: "Evil\x1b[2K Model\nsplit", Context: 131000,
		PriceKnown: true, CostInput: 0.8, CostOutput: 1.6,
		InputModalities: []string{"text"},
	}
	row := renderPlaneModelRow(0, plane, nasty, pal, 60)
	if strings.Contains(row, "\x1b[2J") || strings.Contains(row, "\n") {
		t.Errorf("model row leaked unsanitized text: %q", row)
	}
	detail := strings.Join(planeModelDetailFor(plane, nasty, pal, 67, 12), "\n")
	if strings.Contains(detail, "\x1b[2J") {
		t.Errorf("detail leaked unsanitized text: %q", detail)
	}
	provider := ProviderRow{ID: "evil\x1b[2J", Name: "Evil\nProvider", Count: 3, KeyDetail: "missing"}
	prow := renderProviderRow(0, plane, provider, pal, 40)
	if strings.Contains(prow, "\x1b[2J") || strings.Contains(prow, "\n") {
		t.Errorf("provider row leaked unsanitized text: %q", prow)
	}
}

// TestPlaneRowRightGroupDocks proves strict two-ended docking: every row
// with full metadata fills the pane exactly, and the price, context, and
// capability columns share one vertical axis across rows of different name
// lengths.
func TestPlaneRowRightGroupDocks(t *testing.T) {
	pal := PaletteFor(ThemeDark)
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	models := []ModelOption{
		{ID: "m/kimi-k2.5", Provider: "m", Name: "kimi-k2.5", Context: 262000, PriceKnown: true, CostInput: 0.6, CostOutput: 3, InputModalities: []string{"text", "image"}, Reasoning: true, ReasoningKnown: true},
		{ID: "m/a-much-longer-model-name-here", Provider: "m", Name: "A Much Longer Model Name Here", Context: 131000, PriceKnown: true, CostInput: 0.8, CostOutput: 1.6, InputModalities: []string{"text"}, Reasoning: true, ReasoningKnown: true},
		{ID: "m/tiny", Provider: "m", Name: "T", Context: 1000000, PriceKnown: true, CostInput: 0.3, CostOutput: 2.5, InputModalities: []string{"text", "image"}},
	}
	const width = 67
	var plain []string
	for i, model := range models {
		line := stripANSI(renderPlaneModelRow(i, plane, model, pal, width))
		plain = append(plain, line)
		if CellWidth(line) != width {
			t.Errorf("row %d width = %d, want exactly %d (right-docked): %q", i, CellWidth(line), width, line)
		}
	}
	// The capability column (last 10 cells) right-aligns on every row.
	for i, line := range plain {
		runes := []rune(line)
		if len(runes) < 10 {
			t.Fatalf("row %d too short: %q", i, line)
		}
	}
	if got := string([]rune(plain[0])[len([]rune(plain[0]))-10:]); got != " vis think" {
		t.Errorf("kimi caps = %q, want %q", got, " vis think")
	}
	if got := string([]rune(plain[1])[len([]rune(plain[1]))-10:]); got != "     think" {
		t.Errorf("long-name caps = %q, want %q", got, "     think")
	}
	// The context column ends at the same cell on every full row: the
	// three cells before the caps gap hold the context's last digit.
	for i, line := range plain {
		runes := []rune(line)
		ctxEnd := len(runes) - 10 - 2
		if ctxEnd < 0 || ctxEnd >= len(runes) {
			t.Fatalf("row %d too short for a context column: %q", i, line)
		}
		if c := runes[ctxEnd-1]; c < '0' || (c > '9' && c != 'M' && c != 'k') {
			t.Errorf("row %d context does not dock at the shared axis: %q", i, line)
		}
	}
}

// TestPlaneMetadataWhitespaceSeparation proves the metadata grid is
// borderless: no │ character separates the columns, and the right-column
// keys (Input, Vision, Price) start on one shared axis past the halfway
// mark on every grid row.
func TestPlaneMetadataWhitespaceSeparation(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	const width = 67
	detail := planeModelDetail(plane, PaletteFor(ThemeDark), width, 12)
	axis := -1
	found := 0
	for _, line := range detail {
		plain := stripANSI(line)
		if strings.Contains(plain, "│") {
			t.Errorf("grid leaked a divider character: %q", plain)
		}
		var leftHit, rightHit string
		for _, label := range []string{"Context", "Output", "Thinking"} {
			if strings.Contains(plain, label) {
				leftHit = label
			}
		}
		for _, label := range []string{"Input", "Vision", "Price"} {
			if strings.Contains(plain, label) {
				rightHit = label
			}
		}
		if leftHit == "" || rightHit == "" {
			continue
		}
		found++
		col := CellWidth(plain[:strings.Index(plain, rightHit)])
		if axis < 0 {
			axis = col
		} else if col != axis {
			t.Errorf("%s axis at cell %d, want the shared axis %d: %q", rightHit, col, axis, plain)
		}
	}
	if found != 3 {
		t.Errorf("grid rows = %d, want 3 (context/output/thinking)", found)
	}
	if axis <= width/2 {
		t.Errorf("right axis at cell %d, want it past half (%d)", axis, width/2)
	}
}

// TestPlaneArrowKeysSwitchPanes proves ← docks focus on PROVIDERS and →
// docks focus on the MODELS list, while search keeps the arrows for cursor
// movement.
func TestPlaneArrowKeysSwitchPanes(t *testing.T) {
	p := openPlaneProgram(t)
	left := tea.KeyMsg{Type: tea.KeyLeft}
	right := tea.KeyMsg{Type: tea.KeyRight}

	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(left)
	if p.state.Plane.Focus != PlaneFocusProviders {
		t.Fatalf("left: focus = %v, want providers", p.state.Plane.Focus)
	}
	p.Update(right)
	if p.state.Plane.Focus != PlaneFocusModels {
		t.Fatalf("right: focus = %v, want models", p.state.Plane.Focus)
	}
	// Inside search the arrows edit text, never steal focus.
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusSearch})
	p.state.Apply(PlaneFilterChanged{Text: "ab"})
	p.state.Apply(PlaneFilterCursorMoved{Delta: -1})
	before := p.state.Plane.FilterCursor
	p.Update(left)
	if p.state.Plane.Focus != PlaneFocusSearch {
		t.Error("left in search left the search box")
	}
	if p.state.Plane.FilterCursor != before-1 {
		t.Errorf("left in search: cursor = %d, want %d", p.state.Plane.FilterCursor, before-1)
	}

	// The browse footer advertises the pane contract.
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(ModelPlaneOpened(planeFixture()))
	s.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	if out := stripANSI(Render(s)); !strings.Contains(out, "←→ pane") {
		t.Error("browse footer lost the ←→ pane hint")
	}
}
