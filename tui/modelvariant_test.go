package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// variantFixture is a roster with one bare base plus two suffixed siblings,
// one prefix-trap row that shares characters but not the base, and one
// lonely model with no family at all.
func variantFixture() ModelPlaneOpened {
	roles := []RoleAssignment{
		{Role: "ask", Model: "openrouter/vendor/base", Provider: "openrouter", Name: "Base Model", Source: "default"},
		{Role: "fuzz", Model: "openrouter/vendor/base", Provider: "openrouter", Name: "Base Model", Source: "default"},
		{Role: "build", Model: "openrouter/vendor/base", Provider: "openrouter", Name: "Base Model", Source: "default"},
		{Role: "sup", Model: "openrouter/vendor/base", Provider: "openrouter", Name: "Base Model", Source: "default"},
		{Role: "commit", Model: "openrouter/vendor/base", Provider: "openrouter", Name: "Base Model", Source: "default"},
	}
	return ModelPlaneOpened{
		Providers: []ProviderRow{
			{ID: AllProviders, Name: "All providers", Count: 5},
			{ID: "openrouter", Name: "OpenRouter", Count: 5, Configured: true, KeyDetail: "environment (OPENROUTER_API_KEY)"},
		},
		Models: []ModelOption{
			{
				ID: "openrouter/vendor/base", Provider: "openrouter", Name: "Base Model",
				Context: 200000, MaxOutput: 64000, PriceKnown: true, CostInput: 1, CostOutput: 5,
				InputModalities: []string{"text", "image"},
				ThinkingEfforts: []string{"low", "high"}, Reasoning: true, ReasoningKnown: true,
			},
			{
				ID: "openrouter/vendor/base:free", Provider: "openrouter", Name: "Base Model",
				Context: 200000, MaxOutput: 64000, PriceKnown: true, Free: true,
				InputModalities: []string{"text", "image"},
				ThinkingEfforts: []string{"low", "high"}, Reasoning: true, ReasoningKnown: true,
			},
			{
				ID: "openrouter/vendor/base:nitro", Provider: "openrouter", Name: "Base Model",
				Context: 200000, MaxOutput: 64000, PriceKnown: true, CostInput: 2, CostOutput: 8,
				InputModalities: []string{"text"},
				ThinkingEfforts: []string{"low", "high"}, Reasoning: true, ReasoningKnown: true,
			},
			{
				ID: "openrouter/vendor/barista", Provider: "openrouter", Name: "Barista Model",
				Context: 32000, PriceKnown: true, Free: true,
				InputModalities: []string{"text"}, Reasoning: false, ReasoningKnown: true,
			},
			{
				ID: "openrouter/vendor/lonely", Provider: "openrouter", Name: "Lonely Model",
				Context: 32000, ReasoningKnown: false,
			},
		},
		Roles:      roles,
		ActiveRole: "build",
	}
}

// TestSplitModelVariantParsesSuffixes pins the variant syntax: the last
// colon separates the endpoint suffix from the callable base.
func TestSplitModelVariantParsesSuffixes(t *testing.T) {
	cases := []struct{ ref, base, variant string }{
		{"openrouter/foo/bar", "openrouter/foo/bar", ""},
		{"openrouter/foo/bar:free", "openrouter/foo/bar", ":free"},
		{"openrouter/foo/bar:nitro", "openrouter/foo/bar", ":nitro"},
		{"ollama-cloud/gemma3:12b", "ollama-cloud/gemma3", ":12b"},
		{"ollama-cloud/qwen3-vl:235b-instruct", "ollama-cloud/qwen3-vl", ":235b-instruct"},
		{"openrouter/foo/bar:", "openrouter/foo/bar:", ""},
		{"bare", "bare", ""},
	}
	for _, tc := range cases {
		base, variant := SplitModelVariant(tc.ref)
		if base != tc.base || variant != tc.variant {
			t.Errorf("SplitModelVariant(%q) = (%q, %q), want (%q, %q)",
				tc.ref, base, variant, tc.base, tc.variant)
		}
	}
}

// TestVariantSiblingsMatchExactBases proves family grouping never confuses a
// shared prefix for a family: barista is not a variant of base.
func TestVariantSiblingsMatchExactBases(t *testing.T) {
	plane := NewModelPlane(variantFixture())
	plane.FocusModels()

	siblings := plane.VariantSiblings(plane.Models[0])
	if len(siblings) != 3 {
		t.Fatalf("siblings of base = %d, want bare + :free + :nitro", len(siblings))
	}
	for _, sibling := range siblings {
		if strings.Contains(sibling.ID, "barista") {
			t.Errorf("sibling %q shares a prefix, not a base", sibling.ID)
		}
	}
	if got := siblings[0].ID; got != "openrouter/vendor/base" {
		t.Errorf("first sibling = %q, want the bare base first", got)
	}
}

// TestNeedsVariantChoiceGatesThePrompt proves when the prompt triggers: a
// bare base with siblings asks, everything else assigns directly.
func TestNeedsVariantChoiceGatesThePrompt(t *testing.T) {
	plane := NewModelPlane(variantFixture())
	plane.FocusModels()

	if !plane.NeedsVariantChoice(plane.Models[0]) {
		t.Error("bare base with siblings needs no choice: the prompt would never open")
	}
	for i, want := range map[int]bool{1: false, 2: false, 3: false, 4: false} {
		if plane.NeedsVariantChoice(plane.Models[i]) != want {
			t.Errorf("model %d (%s) choice = %v, want %v", i, plane.Models[i].ID, !want, want)
		}
	}
}

// openVariantProgram opens the plane on the variant fixture, focused on the
// models stop with the bare base highlighted.
func openVariantProgram(t *testing.T, assign func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error)) *Program {
	t.Helper()
	p := NewProgram(nil, "openrouter/vendor/base", 1000000)
	p.SetCatalogHooks(CatalogHooks{
		Snapshot: func() CatalogSnapshot {
			return CatalogSnapshot{Options: variantFixture().Models, Origin: "live"}
		},
	})
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return variantFixture().Providers },
		Roles:      func() []RoleAssignment { return variantFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign:     assign,
	})
	p.state.Apply(TerminalResized{Width: 120, Height: 36})
	Command{Name: "model"}.runOn(p)
	if p.state.Plane == nil {
		t.Fatal("/model did not open the control plane")
	}
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
	return p
}

// TestVariantPromptOpensBeforeAssign proves Enter on a variant-capable model
// prompts instead of assigning: nothing reaches the authority seam yet.
func TestVariantPromptOpensBeforeAssign(t *testing.T) {
	called := false
	p := openVariantProgram(t, func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
		called = true
		return update, variantFixture().Roles, nil
	})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if called {
		t.Fatal("Enter assigned before a variant was chosen")
	}
	if p.state.Plane.Variant == nil {
		t.Fatal("Enter did not open the variant prompt")
	}
	out := Render(p.state)
	for _, want := range []string{"VARIANT", "(default)", ":free", ":nitro"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q:\n%s", want, out)
		}
	}
}

// TestVariantConfirmAssignsTheChosenSuffix proves the confirmed sibling's
// full id — suffix included — flows through the linear transaction to the
// existing seam: variant, then role, then effort, then Apply.
func TestVariantConfirmAssignsTheChosenSuffix(t *testing.T) {
	var assigned RoleAssignment
	calls := 0
	p := openVariantProgram(t, func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
		calls++
		assigned = update
		update.Name = "Base Model"
		return update, variantFixture().Roles, nil
	})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // opens the prompt
	p.Update(tea.KeyMsg{Type: tea.KeyDown})  // :free
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirms the variant
	if p.state.Plane.Variant != nil {
		t.Fatal("the prompt stayed open after confirming")
	}
	if p.state.Plane.Stage != PlaneStageRole {
		t.Fatalf("stage = %v, want role selection after the variant", p.state.Plane.Stage)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // choose BUILD (the default role)
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // apply the effort step
	if calls != 1 {
		t.Fatalf("calls = %d, want one assignment after Apply", calls)
	}
	if assigned.Model != "openrouter/vendor/base:free" {
		t.Errorf("assigned model = %q, want the chosen suffixed id", assigned.Model)
	}
	if assigned.Role != "build" {
		t.Errorf("assigned role = %q, want build", assigned.Role)
	}
	if notice := p.state.Plane.Notice; !strings.Contains(notice, "assigned") {
		t.Errorf("notice = %q, want a confirmation", notice)
	}
}

// TestVariantDefaultConfirmsTheBareBase proves Enter on an open prompt keeps
// yesterday's behaviour: the preselected default is the bare base itself.
func TestVariantDefaultConfirmsTheBareBase(t *testing.T) {
	var assigned RoleAssignment
	p := openVariantProgram(t, func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
		assigned = update
		return update, variantFixture().Roles, nil
	})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // opens the prompt
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirms the bare base
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // choose the role
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // apply
	if assigned.Model != "openrouter/vendor/base" {
		t.Errorf("assigned model = %q, want the bare base default", assigned.Model)
	}
}

// TestVariantCancelAssignsNothing proves Esc abandons the choice without
// touching configuration or the highlight.
func TestVariantCancelAssignsNothing(t *testing.T) {
	called := false
	p := openVariantProgram(t, func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
		called = true
		return update, variantFixture().Roles, nil
	})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if called {
		t.Error("Esc assigned a variant it must abandon")
	}
	if p.state.Plane.Variant != nil {
		t.Error("Esc did not close the variant prompt")
	}
	if model, _ := p.state.Plane.ChosenModel(); model.ID != "openrouter/vendor/base" {
		t.Errorf("highlight moved to %q, want it kept on the base", model.ID)
	}
}

// TestVariantPromptOwnsItsKeys proves the prompt is strictly modal: typing,
// Tab, and refresh-adjacent editing never reach the dialog behind it.
func TestVariantPromptOwnsItsKeys(t *testing.T) {
	called := false
	p := openVariantProgram(t, func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
		called = true
		return update, variantFixture().Roles, nil
	})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	p.Update(tea.KeyMsg{Type: tea.KeyTab})
	if p.state.Plane.Filter != "" {
		t.Errorf("filter = %q, want typing ignored while choosing", p.state.Plane.Filter)
	}
	if p.state.Plane.Variant == nil {
		t.Error("Tab dismissed the prompt it must not own")
	}
	if called {
		t.Error("a stray key assigned during the prompt")
	}
	if cursor := p.state.Plane.Variant.Cursor; cursor != 0 {
		t.Errorf("cursor = %d, want ignored keys to leave it parked", cursor)
	}
}

// TestVariantConfirmRefusesMidTurn keeps the busy boundary on the confirm
// path, not just the direct one.
func TestVariantConfirmRefusesMidTurn(t *testing.T) {
	called := false
	p := openVariantProgram(t, func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
		called = true
		return update, variantFixture().Roles, nil
	})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p.state.Status.Busy = true
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if called {
		t.Error("a variant assigned while a turn was running")
	}
	if notice := p.state.Plane.Notice; !strings.Contains(notice, "turn to finish") {
		t.Errorf("notice = %q, want the busy refusal", notice)
	}
}

// TestVariantEffortResolvesAgainstTheVariantLadder proves the confirmed
// variant's own ladder decides the effort step: a level offered by the base is
// not carried into a sibling that does not expose it, and there is no silent
// fallback to a level the sibling would reject.
func TestVariantEffortResolvesAgainstTheVariantLadder(t *testing.T) {
	plane := NewModelPlane(variantFixture())
	plane.Mode = PlaneRoles
	plane.ActiveRole = "sup"
	plane.PendingModel = ModelOption{
		ID: "openrouter/vendor/base", Provider: "openrouter",
		ThinkingEfforts: []string{"low", "high"}, Reasoning: true, ReasoningKnown: true,
	}
	plane.Effort = "high"
	plane.EffortCursor = 2
	variant := ModelOption{
		ID: "openrouter/vendor/base:odd", Provider: "openrouter",
		ThinkingEfforts: []string{"ludicrous"}, Reasoning: true, ReasoningKnown: true,
	}
	plane.ConfirmVariant(variant)
	if plane.PendingModel.ID != "openrouter/vendor/base:odd" {
		t.Fatalf("pending model = %q, want the sibling id", plane.PendingModel.ID)
	}
	if plane.Stage != PlaneStageEffort {
		t.Fatalf("stage = %v, want the effort step for the sibling's ladder", plane.Stage)
	}
	if plane.Effort != EffortOff || plane.EffortCursor != 0 {
		t.Errorf("effort = %q cursor = %d, want the sibling ladder's first option, not the base's level",
			plane.Effort, plane.EffortCursor)
	}
	if got := plane.EffortOptions(); len(got) != 2 || got[1] != "ludicrous" {
		t.Errorf("options = %v, want off + ludicrous", got)
	}
}

// TestProviderCountKeepsAirBeforeTheEdge proves the count never touches the
// pane separator: a tight pane drops the count rather than jamming it.
func TestProviderCountKeepsAirBeforeTheEdge(t *testing.T) {
	pal := PaletteFor(ThemeDark)
	plane := NewModelPlane(variantFixture())
	row := ProviderRow{ID: "openrouter", Name: "OpenRouter", Count: 390, Configured: true, KeyDetail: "environment (X)"}
	for _, width := range []int{14, 18, 24, 40} {
		line := renderProviderRow(1, plane, row, pal, width)
		if CellWidth(line) > width {
			t.Errorf("width %d: row overflows (%d cells): %q", width, CellWidth(line), line)
		}
	}
	line := renderProviderRow(1, plane, row, pal, 24)
	if !strings.Contains(line, "390") {
		t.Errorf("roomy row dropped the count: %q", line)
	}
	// The row reserves the air cell the layout fills before the separator.
	if air := 24 - CellWidth(line); air < 1 {
		t.Errorf("count leaves %d cells of air, want at least 1: %q", air, line)
	}
}

// TestModelRowKeepsNameAndPrice proves the rebalance: at a constrained width
// the name and price survive while the capability signals yield first.
func TestModelRowKeepsNameAndPrice(t *testing.T) {
	pal := PaletteFor(ThemeDark)
	plane := NewModelPlane(variantFixture())
	plane.FocusModels()
	model := plane.Models[0] // Base Model with price + vision + think
	line := renderPlaneModelRow(0, plane, model, pal, 60)
	for _, want := range []string{"Base Model", "$1/$5"} {
		if !strings.Contains(line, want) {
			t.Errorf("width 60: row lost %q: %q", want, line)
		}
	}
	if CellWidth(line) > 60 {
		t.Errorf("width 60: row overflows: %q", line)
	}
}
