package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// planeFixture is a roster with one laddered reasoning model, one curated
// non-reasoning model, and one discovered row whose reasoning is unknown.
func planeFixture() ModelPlaneOpened {
	roles := []RoleAssignment{
		{Role: "ask", Model: "anthropic/claude-sonnet-4-5", Provider: "anthropic", Name: "Claude Sonnet 4.5", Source: "default"},
		{Role: "fuzz", Model: "anthropic/claude-sonnet-4-5", Provider: "anthropic", Name: "Claude Sonnet 4.5", Source: "default"},
		{Role: "build", Model: "anthropic/claude-sonnet-4-5", Provider: "anthropic", Name: "Claude Sonnet 4.5", Source: "default"},
		{Role: "sup", Model: "anthropic/claude-sonnet-4-5", Provider: "anthropic", Name: "Claude Sonnet 4.5", Source: "default"},
		{Role: "commit", Model: "anthropic/claude-sonnet-4-5", Provider: "anthropic", Name: "Claude Sonnet 4.5", Source: "default"},
	}
	return ModelPlaneOpened{
		Providers: []ProviderRow{
			{ID: AllProviders, Name: "All providers", Count: 3},
			{ID: "anthropic", Name: "Anthropic", Count: 1, Configured: true, KeyDetail: "environment (ANTHROPIC_API_KEY)"},
			{ID: "openrouter", Name: "OpenRouter", Count: 2, Configured: false, KeyDetail: "missing: set OPENROUTER_API_KEY"},
		},
		Models: []ModelOption{
			{
				ID: "anthropic/claude-sonnet-4-5", Provider: "anthropic", Name: "Claude Sonnet 4.5",
				Context: 1000000, MaxOutput: 64000, PriceKnown: true, CostInput: 3, CostOutput: 15,
				InputModalities: []string{"text", "image"},
				ThinkingEfforts: []string{"low", "medium", "high"}, Reasoning: true, ReasoningKnown: true,
			},
			{
				ID: "openrouter/vendor/plain", Provider: "openrouter", Name: "Plain Model",
				Context: 128000, MaxOutput: 8192, PriceKnown: true, Free: true,
				InputModalities: []string{"text"}, Reasoning: false, ReasoningKnown: true,
			},
			{
				ID: "openrouter/vendor/silent", Provider: "openrouter", Name: "Silent Model",
				Context: 32000, ReasoningKnown: false,
			},
		},
		Roles:      roles,
		ActiveRole: "build",
	}
}

// TestPlaneRendersProviderAndModelPanesTogether is the topology contract: the
// provider context and the model context are visible at the same time, split
// by a vertical rule, never a sequence of full-screen pages.
func TestPlaneRendersProviderAndModelPanesTogether(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 120, Height: 36})
	s.Apply(ModelPlaneOpened(planeFixture()))
	out := Render(s)

	for _, want := range []string{"PROVIDERS", "MODEL", "Anthropic", "OpenRouter", "Claude Sonnet 4.5"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "│") {
		t.Errorf("render has no vertical split:\n%s", out)
	}
}

// TestPlaneKeepsTheSplitAtNarrowWidths proves the layout adapts widths rather
// than collapsing into a wizard: both headings survive down to the smallest
// workbench width.
func TestPlaneKeepsTheSplitAtNarrowWidths(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {80, 24}, {60, 18}, {40, 10}} {
		s := New()
		s.Apply(TerminalResized{Width: size[0], Height: size[1]})
		s.Apply(ModelPlaneOpened(planeFixture()))
		out := Render(s)
		if !strings.Contains(out, "PROVIDERS") || !strings.Contains(out, "MODEL") {
			t.Errorf("%dx%d: split collapsed:\n%s", size[0], size[1], out)
		}
		if !strings.Contains(out, "│") {
			t.Errorf("%dx%d: no split rule rendered", size[0], size[1])
		}
		for _, line := range strings.Split(out, "\n") {
			if CellWidth(line) > size[0] {
				t.Errorf("%dx%d: line overflows (%d cells): %q", size[0], size[1], CellWidth(line), line)
			}
		}
	}
}

// TestPlaneProviderScopesTheModelList proves the provider pane filters the
// model pane, and that moving back to All restores the full roster.
func TestPlaneProviderScopesTheModelList(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	if got := len(plane.VisibleModels()); got != 3 {
		t.Fatalf("All scope = %d models, want 3", got)
	}
	// Move to OpenRouter (index 2).
	plane.Move(2)
	if scope := plane.ProviderScope(); scope != "openrouter" {
		t.Fatalf("scope = %q, want openrouter", scope)
	}
	if got := len(plane.VisibleModels()); got != 2 {
		t.Errorf("openrouter scope = %d models, want 2", got)
	}
}

// TestPlaneSelectionIsDeterministicAcrossMoves pins the cursor behaviour: it
// wraps within the focused pane and a provider change re-parks the model
// highlight at the top of the new scope rather than leaving it out of range.
func TestPlaneSelectionIsDeterministicAcrossMoves(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	// Focus the model pane and move down: the highlight advances one row.
	plane.FocusModels()
	plane.Move(1)
	chosen, ok := plane.ChosenModel()
	if !ok || chosen.ID != "openrouter/vendor/plain" {
		t.Fatalf("chosen = %+v, want the second model", chosen)
	}
	// Selecting a provider re-parks the model highlight at index 0.
	plane.FocusProviders()
	plane.Move(2) // openrouter
	if plane.ModelCursor != 0 {
		t.Errorf("model cursor = %d, want it parked at the top after a scope change", plane.ModelCursor)
	}
	plane.FocusModels()
	chosen, ok = plane.ChosenModel()
	if !ok || chosen.Provider != "openrouter" {
		t.Errorf("chosen = %+v, want an openrouter row", chosen)
	}
}

// TestPlaneDetailReflectsTheSelectedModel proves the detail block is derived
// from the highlighted model, not a stale or fixed row.
func TestPlaneDetailReflectsTheSelectedModel(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	detail := planeModelDetail(plane, PaletteFor(ThemeDark), 60, 12)
	joined := strings.Join(detail, "\n")
	if !strings.Contains(joined, "Claude Sonnet 4.5") {
		t.Fatalf("detail does not name the selected model:\n%s", joined)
	}
	if !strings.Contains(joined, "anthropic/claude-sonnet-4-5") {
		t.Errorf("detail does not show the id:\n%s", joined)
	}
	// Move to the curated non-reasoning model and re-render the detail.
	plane.Move(1)
	detail = planeModelDetail(plane, PaletteFor(ThemeDark), 60, 12)
	joined = strings.Join(detail, "\n")
	if !strings.Contains(joined, "Plain Model") {
		t.Errorf("detail did not follow the highlight:\n%s", joined)
	}
}

// TestPlaneThinkingStatesAreDistinct is the unknown-vs-unsupported contract at
// the UI seam: three models render three different thinking states, and no
// effort control appears unless the model exposes a ladder.
func TestPlaneThinkingStatesAreDistinct(t *testing.T) {
	pal := PaletteFor(ThemeDark)
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()

	// Laddered model: supported with a radio.
	if got := planeThinking(plane.Models[0]); !strings.Contains(got, "supported") {
		t.Errorf("laddered thinking = %q, want supported", got)
	}
	radio := planeEffortRadio(plane, pal, 60)
	if len(radio) == 0 {
		t.Fatal("a laddered model rendered no effort radio")
	}
	joined := strings.Join(radio, " ")
	for _, want := range []string{"off", "low", "medium", "high"} {
		if !strings.Contains(joined, want) {
			t.Errorf("radio = %q, want option %q", joined, want)
		}
	}

	// Curated non-reasoning model: unsupported, no radio.
	plain := plane.Models[1]
	if got := planeThinking(plain); got != "unsupported" {
		t.Errorf("plain thinking = %q, want unsupported", got)
	}
	plane.FocusModels()
	plane.SetFilter("Plain")
	if opts := plane.EffortOptions(); len(opts) != 0 {
		t.Errorf("unsupported model exposed efforts %v", opts)
	}
	if radio := planeEffortRadio(plane, pal, 60); len(radio) != 0 {
		t.Errorf("unsupported model rendered an effort control: %v", radio)
	}

	// Discovered row with no reasoning metadata: unknown, not unsupported.
	if got := planeThinking(plane.Models[2]); got != "unknown" {
		t.Errorf("silent thinking = %q, want unknown", got)
	}
}

// TestPlaneEffortOptionsAreModelSpecific proves the radio is derived from the
// model's own ladder, never a hard-coded universal list.
func TestPlaneEffortOptionsAreModelSpecific(t *testing.T) {
	plane := NewModelPlane(ModelPlaneOpened{
		Models: []ModelOption{
			{ID: "anthropic/a", Provider: "anthropic", ThinkingEfforts: []string{"minimal", "low", "medium", "high", "xhigh"}, Reasoning: true, ReasoningKnown: true},
			{ID: "google/b", Provider: "google", ThinkingEfforts: []string{"standard", "extended"}, Reasoning: true, ReasoningKnown: true},
		},
	})
	plane.FocusModels()
	first := plane.EffortOptions()
	if len(first) != 6 || first[0] != EffortOff || first[1] != "minimal" {
		t.Fatalf("first options = %v, want off + the first model's ladder", first)
	}
	plane.Move(1)
	second := plane.EffortOptions()
	if len(second) != 3 || second[1] != "standard" || second[2] != "extended" {
		t.Fatalf("second options = %v, want off + the second model's ladder", second)
	}
	// The pending effort is re-validated against the new model rather than
	// carrying a level it does not accept.
	if plane.Effort != EffortOff && !contains(second, plane.Effort) {
		t.Errorf("effort = %q survives a model change, want a valid option", plane.Effort)
	}
}

// TestPlaneAssignTargetsTheActiveRoleInModelMode proves Enter's target is
// explicit and the highlighted model is what gets applied.
func TestPlaneAssignTargetsTheActiveRoleInModelMode(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	plane.Move(1) // the openrouter plain model
	assignment, ok := plane.Assignment()
	if !ok {
		t.Fatal("no assignment produced")
	}
	if assignment.Role != "build" {
		t.Errorf("role = %q, want the active role", assignment.Role)
	}
	if assignment.Model != "openrouter/vendor/plain" {
		t.Errorf("model = %q, want the highlighted model", assignment.Model)
	}
	// Cycle the active role and confirm the target moves with it.
	plane.NextRole(1)
	if role := plane.ActiveRole; role == "build" {
		t.Error("role cycle did not move the active role")
	}
}

// TestPlaneRolesModeExposesEveryRole proves roles are first-class and the
// assignment pane names the source, and that toggling back keeps the selected
// model.
func TestPlaneRolesModeExposesEveryRole(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	plane.Move(1)
	selected := plane.ModelCursor

	plane.ToggleMode()
	if plane.Mode != PlaneRoles {
		t.Fatal("Tab did not enter roles mode")
	}
	if len(plane.Roles) != 5 {
		t.Fatalf("roles = %d, want ask/fuzz/build/sup/commit", len(plane.Roles))
	}
	pal := PaletteFor(ThemeDark)
	pane := strings.Join(renderPlaneAssignmentPane(plane, pal, 60), "\n")
	if !strings.Contains(pane, "Source") {
		t.Errorf("assignment pane hides the source:\n%s", pane)
	}
	// Select the COMMIT role and assign to it.
	for plane.RoleCursor < 4 {
		plane.Move(1)
	}
	assignment, ok := plane.Assignment()
	if !ok || assignment.Role != "commit" {
		t.Fatalf("assignment = %+v, want the highlighted role", assignment)
	}
	plane.ToggleMode()
	if plane.Mode != PlaneModels {
		t.Error("Tab did not return to model mode")
	}
	if plane.ModelCursor != selected {
		t.Error("toggling modes lost the selected model")
	}
}

// TestPlaneRowsClampAfterFiltering proves a narrowing filter never leaves the
// highlight pointing past the end of the visible set.
func TestPlaneRowsClampAfterFiltering(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusModels()
	plane.SetVisibleRows(2)
	plane.Move(2)
	plane.SetFilter("silent")
	if plane.ModelCursor >= len(plane.VisibleModels()) {
		t.Errorf("cursor = %d over %d rows, want it clamped", plane.ModelCursor, len(plane.VisibleModels()))
	}
	if _, ok := plane.ChosenModel(); !ok {
		t.Error("no choice after filtering to a non-empty set")
	}
}

// TestModelCommandOpensControlPlaneWhenHooksAreInstalled proves /model routes
// to the split pane when the control hooks are present, and that Enter applies
// through the Assign seam and records the outcome.
func TestModelCommandOpensControlPlaneWhenHooksAreInstalled(t *testing.T) {
	p := NewProgram(nil, "anthropic/claude-sonnet-4-5", 1000000)
	p.SetCatalogHooks(CatalogHooks{
		Snapshot: func() CatalogSnapshot {
			return CatalogSnapshot{Options: planeFixture().Models, Origin: "bundled"}
		},
	})
	var assigned RoleAssignment
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return planeFixture().Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			assigned = update
			update.Name = "Applied"
			return update, planeFixture().Roles, nil
		},
	})
	p.state.Apply(TerminalResized{Width: 120, Height: 36})

	Command{Name: "model"}.runOn(p)
	if p.state.Plane == nil {
		t.Fatal("/model did not open the control plane")
	}
	if out := Render(p.state); !strings.Contains(out, "PROVIDERS") || !strings.Contains(out, "MODEL") {
		t.Fatalf("control plane did not render as a split:\n%s", out)
	}

	// Enter on the model stop applies the highlighted model to the active role.
	// The plane opens on providers, so step focus there first.
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if assigned.Role != "build" {
		t.Errorf("assigned role = %q, want build", assigned.Role)
	}
	if assigned.Model != "anthropic/claude-sonnet-4-5" {
		t.Errorf("assigned model = %q, want the highlighted model", assigned.Model)
	}
	if p.state.Plane == nil || !strings.Contains(p.state.Plane.Notice, "assigned") {
		t.Errorf("notice = %q, want a confirmation", p.state.Plane.Notice)
	}
}

// TestModelCommandFallsBackToPickerWithoutControlHooks keeps the legacy path
// working for a caller that has not wired the plane.
func TestModelCommandFallsBackToPickerWithoutControlHooks(t *testing.T) {
	p := NewProgram(nil, "test-model", 0)
	p.SetModelOptionLister(func() []ModelOption {
		return []ModelOption{{ID: "a/1", Title: "One", Provider: "a"}}
	})
	Command{Name: "model"}.runOn(p)
	if p.state.Picker == nil {
		t.Fatal("/model did not open the legacy picker when no control hooks are installed")
	}
	if p.state.Plane != nil {
		t.Error("the control plane opened without hooks")
	}
}

// TestPlaneRefusesToChangeModelsMidTurn keeps a running turn consistent: the
// loop reads its model and effort when it builds each request, so an
// assignment during a turn is refused rather than landed halfway through.
func TestPlaneRefusesToChangeModelsMidTurn(t *testing.T) {
	p := NewProgram(nil, "anthropic/claude-sonnet-4-5", 1000000)
	called := false
	p.SetCatalogHooks(CatalogHooks{
		Snapshot: func() CatalogSnapshot {
			return CatalogSnapshot{Options: planeFixture().Models, Origin: "bundled"}
		},
	})
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return planeFixture().Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			called = true
			return update, planeFixture().Roles, nil
		},
	})
	p.state.Status.Busy = true
	Command{Name: "model"}.runOn(p)
	if p.state.Plane == nil {
		t.Fatal("the plane did not open")
	}
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if called {
		t.Error("an assignment landed while a turn was running")
	}
	if p.state.Plane == nil || !strings.Contains(p.state.Plane.Notice, "turn to finish") {
		t.Errorf("notice = %q, want the busy refusal", p.state.Plane.Notice)
	}
}

// TestPlaneProviderWindowFollowsTheCursor proves the provider list scrolls
// with its highlight: moving past the visible window keeps the marker on the
// cursor's row instead of losing it off-screen.
func TestPlaneProviderWindowFollowsTheCursor(t *testing.T) {
	providers := []ProviderRow{{ID: AllProviders, Name: "All providers"}}
	for i := 0; i < 10; i++ {
		providers = append(providers, ProviderRow{
			ID:   "provider-" + string(rune('a'+i)),
			Name: "Provider " + string(rune('A'+i)),
		})
	}
	opened := planeFixture()
	opened.Providers = providers
	plane := NewModelPlane(opened)
	plane.SetVisibleRows(3)

	for i := 0; i < 6; i++ {
		plane.Move(1)
	}
	if plane.ProviderCursor != 6 {
		t.Fatalf("provider cursor = %d, want 6", plane.ProviderCursor)
	}
	lines := renderPlaneProvidersPane(plane, PaletteFor(ThemeDark), New(), 40)
	var marked []string
	for _, line := range lines {
		if strings.Contains(line, "> ") {
			marked = append(marked, line)
		}
	}
	if len(marked) != 1 {
		t.Fatalf("marked rows = %d, want the cursor kept in view:\n%s", len(marked), strings.Join(lines, "\n"))
	}
	if !strings.Contains(marked[0], "Provider F") {
		t.Errorf("marked row = %q, want the cursor's provider kept with it", marked[0])
	}
}

// TestPlaneRolesPaneDisplaysAssignedModels proves the Roles pane is a
// display of effective assignments: every role row names its current model,
// so a glance shows what each role runs without opening the detail.
func TestPlaneRolesPaneDisplaysAssignedModels(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusRoles()
	lines := strings.Join(renderPlaneRolesPane(plane, PaletteFor(ThemeDark), 60), "\n")
	for _, want := range []string{"ASK", "BUILD", "Claude Sonnet 4.5"} {
		if !strings.Contains(lines, want) {
			t.Errorf("roles pane missing %q:\n%s", want, lines)
		}
	}
}

// TestPlaneStageRoleReturnsToModelSearch proves the Roles-mode Enter
// contract: choosing a role stages it as the assignment target and returns
// to model browsing for its replacement, instead of assigning a hidden
// model highlight. The model pane's search then acts as the picker.
func TestPlaneStageRoleReturnsToModelSearch(t *testing.T) {
	plane := NewModelPlane(planeFixture())
	plane.FocusRoles()
	// Highlight ASK (index 0 is ask in the fixture).
	plane.RoleCursor = 0
	if !plane.StageRole() {
		t.Fatal("StageRole reported no role staged")
	}
	if plane.ActiveRole != "ask" {
		t.Errorf("active role = %q, want ask", plane.ActiveRole)
	}
	if plane.Mode != PlaneModels {
		t.Errorf("mode = %q, want model browsing for the replacement", plane.Mode)
	}
	if plane.Focus != PlaneFocusModels {
		t.Errorf("focus = %v, want models so typing searches", plane.Focus)
	}
	if !strings.Contains(plane.Notice, "ASK") {
		t.Errorf("notice = %q, want it to name the staged role", plane.Notice)
	}
}

// contains reports membership for small option slices.
func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
