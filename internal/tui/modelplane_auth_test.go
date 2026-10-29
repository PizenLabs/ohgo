package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ctrlE builds the Ctrl+E chord the spec traps in browse mode.
func ctrlE() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyCtrlE, Runes: []rune{'e'}}
}

// openAuthProgram opens the plane with a save seam that records the call
// and flips the saved provider to configured, like the credential store.
func openAuthProgram(t *testing.T, saved *AuthCall) *Program {
	t.Helper()
	p := openPlaneProgram(t)
	rows := planeFixture().Providers
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return rows },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			return update, planeFixture().Roles, nil
		},
		SaveKey: func(provider, key string) error {
			*saved = AuthCall{Provider: provider, Key: key}
			for i, row := range rows {
				if row.ID == provider {
					rows[i].Configured = true
					rows[i].KeyDetail = "credential file"
				}
			}
			return nil
		},
	})
	return p
}

// AuthCall records one SaveKey invocation.
type AuthCall struct {
	Provider string
	Key      string
}

// TestPlaneCtrlEOpensAuthOnProvidersPane is the trigger contract: Ctrl+E
// with the providers pane focused opens masked entry for that provider.
func TestPlaneCtrlEOpensAuthOnProvidersPane(t *testing.T) {
	p := openPlaneProgram(t)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
	// Cursor opens on the All scope row; move to a real provider first.
	p.state.Apply(PlaneMoved{Delta: 2})

	p.Update(ctrlE())

	auth := p.state.Plane.Auth
	if auth == nil {
		t.Fatal("Ctrl+E on providers pane did not open the key prompt")
	}
	if auth.Provider != "openrouter" {
		t.Errorf("auth provider = %q, want the highlighted openrouter", auth.Provider)
	}
	if p.state.Plane.FocusName() != "auth" {
		t.Errorf("focus = %q, want auth to own the keyboard", p.state.Plane.FocusName())
	}
}

// TestPlaneCtrlEIgnoresModelsAndSearchPane is the focus-condition
// contract: the chord is a no-op anywhere but the providers pane.
func TestPlaneCtrlEIgnoresModelsAndSearchPane(t *testing.T) {
	for _, focus := range []PlaneFocus{PlaneFocusModels, PlaneFocusSearch} {
		p := openPlaneProgram(t)
		p.state.Apply(PlaneFocusSet{Focus: focus})
		p.Update(ctrlE())
		if p.state.Plane.Auth != nil {
			t.Errorf("focus %v: Ctrl+E opened auth, want it ignored", focus)
		}
	}
}

// TestPlaneAuthRendersMaskedInput proves the secret never reaches the
// screen: the prompt names the provider and shows bullets, not the key.
func TestPlaneAuthRendersMaskedInput(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(ModelPlaneOpened(planeFixture()))
	s.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
	s.Apply(PlaneMoved{Delta: 2})
	s.Apply(PlaneAuthOpened{})
	s.Apply(PlaneAuthTyped{Text: "sk-secret-123"})
	out := Render(s)

	if !strings.Contains(out, "Configure API Key for OpenRouter") {
		t.Errorf("prompt missing provider title:\n%s", out)
	}
	if strings.Contains(out, "sk-secret-123") {
		t.Errorf("render leaked the raw key:\n%s", out)
	}
	if !strings.Contains(out, "•") {
		t.Errorf("render missing masked bullets:\n%s", out)
	}
	if !strings.Contains(out, "Enter save") || !strings.Contains(out, "Esc cancel") {
		t.Errorf("render missing save/cancel contract:\n%s", out)
	}
}

// TestPlaneAuthFooterAdvertisesCtrlE proves the shortcut is discoverable:
// the providers-pane footer names it.
func TestPlaneAuthFooterAdvertisesCtrlE(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 120, Height: 36})
	s.Apply(ModelPlaneOpened(planeFixture()))
	s.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
	out := Render(s)
	if !strings.Contains(out, "Ctrl+E configure key") {
		t.Errorf("footer missing Ctrl+E hint:\n%s", out)
	}
}

// TestPlaneAuthEnterSavesAndRefreshesGlyph is the persistence contract:
// Enter writes through the credential seam, closes the prompt, confirms in
// the footer, and refreshes the provider row to configured.
func TestPlaneAuthEnterSavesAndRefreshesGlyph(t *testing.T) {
	var saved AuthCall
	p := openAuthProgram(t, &saved)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
	p.state.Apply(PlaneMoved{Delta: 2})
	p.Update(ctrlE())
	for _, r := range "sk-live-key" {
		p.Update(planeKey(string(r)))
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if saved.Provider != "openrouter" || saved.Key != "sk-live-key" {
		t.Errorf("saved = %+v, want openrouter/sk-live-key", saved)
	}
	if p.state.Plane.Auth != nil {
		t.Error("auth prompt stayed open after save, want it closed")
	}
	if !strings.Contains(p.state.Plane.Notice, "API key saved for openrouter") {
		t.Errorf("notice = %q, want the save confirmation", p.state.Plane.Notice)
	}
	found := false
	for _, row := range p.state.Plane.Providers {
		if row.ID == "openrouter" && row.Configured {
			found = true
		}
	}
	if !found {
		t.Error("openrouter row not refreshed to configured after save")
	}
}

// TestPlaneAuthEscAbandonsWithoutSaving proves cancellation is clean: Esc
// closes the prompt and the seam sees nothing.
func TestPlaneAuthEscAbandonsWithoutSaving(t *testing.T) {
	var saved AuthCall
	p := openAuthProgram(t, &saved)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
	p.state.Apply(PlaneMoved{Delta: 2})
	p.Update(ctrlE())
	p.Update(planeKey("x"))
	p.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if p.state.Plane.Auth != nil {
		t.Error("Esc did not close the key prompt")
	}
	if saved.Key != "" || saved.Provider != "" {
		t.Errorf("Esc saved %+v, want no credential write", saved)
	}
}

// TestPlaneAuthEmptyKeyStaysOpen proves an empty submit is refused without
// losing the prompt: the footer explains and entry continues.
func TestPlaneAuthEmptyKeyStaysOpen(t *testing.T) {
	var saved AuthCall
	p := openAuthProgram(t, &saved)
	p.state.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
	p.state.Apply(PlaneMoved{Delta: 2})
	p.Update(ctrlE())
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if p.state.Plane.Auth == nil {
		t.Error("empty Enter closed the prompt, want it kept for correction")
	}
	if saved.Key != "" {
		t.Errorf("empty key reached the seam: %+v", saved)
	}
}
