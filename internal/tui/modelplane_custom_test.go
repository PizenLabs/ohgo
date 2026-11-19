package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func customPlaneOpened(endpoints []EndpointOption) ModelPlaneOpened {
	opened := planeFixture()
	opened.Providers = append(opened.Providers, ProviderRow{
		ID:     CustomProvider,
		Name:   CustomProviderName,
		Custom: true,
	})
	opened.Endpoints = endpoints
	return opened
}

// TestCustomProviderEmptyStateIsNotAFailure pins the first visual contract:
// Custom is another provider row, and its empty state offers an action rather
// than a broken-provider warning.
func TestCustomProviderEmptyStateIsNotAFailure(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 120, Height: 36})
	s.Apply(ModelPlaneOpened(customPlaneOpened(nil)))
	s.Plane.ProviderCursor = len(s.Plane.Providers) - 1
	out := Render(s)
	for _, want := range []string{"+ Custom", "No endpoints configured.", "[Enter] Add endpoint", "MODEL · custom"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "! Custom") {
		t.Errorf("Custom rendered as a failed provider:\n%s", out)
	}
}

// TestCustomAddProbeSelectSave drives the complete control path: Add opens the
// compact editor, Enter probes off the update goroutine, discovery selection
// stages models, and Ctrl+S reaches the persistence seam with those models.
func TestCustomAddProbeSelectSave(t *testing.T) {
	p := NewProgram(nil, "anthropic/claude-sonnet-4-5", 1000000)
	p.SetCatalogHooks(CatalogHooks{Snapshot: func() CatalogSnapshot {
		return CatalogSnapshot{Options: planeFixture().Models, Origin: "bundled"}
	}})
	var saved EndpointDraft
	var probed EndpointDraft
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return customPlaneOpened(nil).Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			return update, planeFixture().Roles, nil
		},
		Endpoints: func() []EndpointOption { return nil },
		SaveEndpoint: func(draft EndpointDraft) ([]EndpointOption, error) {
			saved = draft
			return []EndpointOption{{
				Key:           "vllm-local",
				Name:          draft.Name,
				BaseURL:       draft.BaseURL,
				APIKeyEnv:     draft.APIKeyEnv,
				Models:        append([]string(nil), draft.Models...),
				ContextWindow: 32768,
				MaxTokens:     4096,
			}}, nil
		},
		ProbeEndpoint: func(ctx context.Context, draft EndpointDraft) EndpointProbeResult {
			probed = draft
			return EndpointProbeResult{
				Reachable: true, Authenticated: true, Models: 2,
				ModelIDs: []string{"qwen3-32b", "qwen3-coder"},
				Detail:   "endpoint reachable, credential accepted, 2 models listed",
			}
		},
	})
	p.state.Apply(TerminalResized{Width: 120, Height: 36})
	Command{Name: "model"}.runOn(p)
	if p.state.Plane == nil {
		t.Fatal("/model did not open the plane")
	}
	plane := p.state.Plane
	plane.ProviderCursor = len(plane.Providers) - 1

	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // Custom empty state -> Add
	if plane.EndpointEditor == nil {
		t.Fatal("Enter did not open the endpoint editor")
	}
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("vllm-local")})
	p.Update(tea.KeyMsg{Type: tea.KeyTab})
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("http://127.0.0.1:8000/v1")})
	p.Update(tea.KeyMsg{Type: tea.KeyTab})
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("VLLM_API_KEY")})
	if got := plane.EndpointEditor.Draft(); got.Name != "vllm-local" || got.BaseURL != "http://127.0.0.1:8000/v1" || got.APIKeyEnv != "VLLM_API_KEY" {
		t.Fatalf("draft = %+v, want the typed fields", got)
	}

	cmd := p.probeCustomEndpoint()
	if cmd == nil {
		t.Fatal("probe returned no command")
	}
	p.Update(cmd())
	if probed.BaseURL != "http://127.0.0.1:8000/v1" || probed.APIKeyEnv != "VLLM_API_KEY" {
		t.Errorf("probe draft = %+v, want the editor values", probed)
	}
	if len(plane.EndpointEditor.Discovered) != 2 {
		t.Fatalf("discovered = %v, want two runtime rows", plane.EndpointEditor.Discovered)
	}

	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")}) // discovery
	if plane.CustomView != CustomViewDiscovery || plane.Discovery == nil {
		t.Fatal("m did not open discovery")
	}
	p.Update(tea.KeyMsg{Type: tea.KeySpace}) // select first
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if len(plane.EndpointEditor.Models) != 1 {
		t.Fatalf("staged models = %v, want the one selected row", plane.EndpointEditor.Models)
	}

	p.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if len(saved.Models) != 1 || saved.Models[0] != "qwen3-32b" {
		t.Errorf("saved = %+v, want only the explicitly selected model", saved)
	}
	if plane.CustomView != CustomViewEndpoints || len(plane.Endpoints) != 1 {
		t.Errorf("after save view=%v endpoints=%v, want the refreshed list", plane.CustomView, plane.Endpoints)
	}
}

// TestCustomEndpointDetailUsesTheSharedModelRenderer proves Custom endpoint
// models appear with the same row/detail vocabulary as every other model.
func TestCustomEndpointDetailUsesTheSharedModelRenderer(t *testing.T) {
	endpoint := EndpointOption{
		Key: "vllm-local", Name: "vLLM Local", BaseURL: "http://localhost:8000/v1",
		Models: []string{"qwen3-coder"}, ContextWindow: 32768, MaxTokens: 4096, FactsAutomatic: true,
	}
	opened := customPlaneOpened([]EndpointOption{endpoint})
	opened.Models = append(opened.Models, ModelOption{
		ID: "vllm-local/qwen3-coder", Provider: "vllm-local", Name: "qwen3-coder",
		Context: 32768, ContextAutomatic: true, MaxOutput: 4096, MaxOutputAutomatic: true,
		InputModalities: []string{"text"}, ReasoningKnown: false,
	})
	plane := NewModelPlane(opened)
	plane.ProviderCursor = len(plane.Providers) - 1
	if !plane.OpenEndpoint("vllm-local") {
		t.Fatal("endpoint did not open")
	}
	lines := renderCustomEndpointPane(plane, PaletteFor(ThemeDark), 80, 30)
	out := strings.Join(lines, "\n")
	for _, want := range []string{"MODEL · vLLM Local", "qwen3-coder", "URL", "http://localhost:8000/v1", "OpenAI-compatible", "not probed", "Endpoint", "(automatic)", "Thinking · unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestCustomEditorRejectsAnEmptyNameBeforeCallingSave(t *testing.T) {
	p := NewProgram(nil, "anthropic/claude-sonnet-4-5", 1000000)
	p.SetCatalogHooks(CatalogHooks{Snapshot: func() CatalogSnapshot {
		return CatalogSnapshot{Options: planeFixture().Models, Origin: "bundled"}
	}})
	called := false
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return customPlaneOpened(nil).Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			return update, planeFixture().Roles, nil
		},
		SaveEndpoint: func(draft EndpointDraft) ([]EndpointOption, error) {
			called = true
			return nil, nil
		},
	})
	p.state.Apply(TerminalResized{Width: 120, Height: 36})
	Command{Name: "model"}.runOn(p)
	if p.state.Plane == nil {
		t.Fatal("/model did not open the plane")
	}
	p.state.Plane.ProviderCursor = len(p.state.Plane.Providers) - 1
	p.state.Apply(PlaneEndpointAdded{})
	p.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if called {
		t.Error("an empty endpoint reached the persistence seam")
	}
	if got := p.state.Plane.Notice; !strings.Contains(got, "name is required") {
		t.Errorf("notice = %q, want the validation refusal", got)
	}
}

// TestCustomProbeResultForAnEditedTargetIsRejected keeps runtime evidence from
// describing a URL the user has already replaced.
func TestCustomProbeResultForAnEditedTargetIsRejected(t *testing.T) {
	plane := NewModelPlane(customPlaneOpened(nil))
	plane.OpenNewEndpoint()
	plane.EndpointEditor.BaseURL = "http://old/v1"
	plane.EndpointEditor.ProbeSeq = 1
	plane.EndpointEditor.Probing = true
	plane.ApplyEndpointProbe(1, "http://old/v1", "", EndpointProbeResult{
		Reachable: true, Authenticated: true, Protocol: true, Models: 1, ModelIDs: []string{"m"},
	})
	if len(plane.EndpointEditor.Discovered) != 1 {
		t.Fatalf("matching probe did not apply: %+v", plane.EndpointEditor)
	}
	plane.EndpointEditor.BaseURL = "http://new/v1"
	plane.EndpointEditor.ProbeSeq = 2
	plane.EndpointEditor.Probing = true
	plane.ApplyEndpointProbe(2, "http://old/v1", "", EndpointProbeResult{
		Reachable: true, Authenticated: true, Protocol: true, Models: 1, ModelIDs: []string{"stale"},
	})
	if len(plane.EndpointEditor.Discovered) != 1 || plane.EndpointEditor.Discovered[0] != "m" {
		t.Errorf("stale probe replaced evidence: %+v", plane.EndpointEditor.Discovered)
	}
	if !strings.Contains(plane.EndpointEditor.ProbeStatus, "probe again") {
		t.Errorf("status = %q, want the stale-target refusal", plane.EndpointEditor.ProbeStatus)
	}
}

// TestCustomModelAssignsThroughTheNormalFlow proves a custom model is not a
// second runtime path: Enter on the endpoint detail stages it and the ordinary
// role/effort/apply transaction reaches the Assign seam.
func TestCustomModelAssignsThroughTheNormalFlow(t *testing.T) {
	endpoint := EndpointOption{
		Key: "vllm", Name: "vLLM", BaseURL: "http://127.0.0.1:8000/v1",
		Models: []string{"qwen3"}, ContextWindow: 32768, MaxTokens: 4096,
	}
	opened := customPlaneOpened([]EndpointOption{endpoint})
	opened.Models = append(opened.Models, ModelOption{
		ID: "vllm/qwen3", Provider: "vllm", Name: "qwen3", Context: 32768,
		InputModalities: []string{"text"},
	})
	p := NewProgram(nil, "anthropic/claude-sonnet-4-5", 1000000)
	p.SetCatalogHooks(CatalogHooks{Snapshot: func() CatalogSnapshot {
		return CatalogSnapshot{Options: opened.Models, Origin: "bundled"}
	}})
	var assigned RoleAssignment
	p.SetModelControl(ModelControlHooks{
		Providers:  func() []ProviderRow { return opened.Providers },
		Roles:      func() []RoleAssignment { return planeFixture().Roles },
		ActiveRole: func() string { return "build" },
		Assign: func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error) {
			assigned = update
			update.Name = "Applied"
			return update, planeFixture().Roles, nil
		},
		Endpoints: func() []EndpointOption { return []EndpointOption{endpoint} },
	})
	p.state.Apply(TerminalResized{Width: 120, Height: 36})
	Command{Name: "model"}.runOn(p)
	if p.state.Plane == nil {
		t.Fatal("/model did not open")
	}
	p.state.Plane.ProviderCursor = len(p.state.Plane.Providers) - 1
	p.state.Apply(PlaneEndpointOpened{Key: "vllm"})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // stage custom model
	if p.state.Plane.Stage != PlaneStageRole {
		t.Fatalf("stage = %v, want the normal role step", p.state.Plane.Stage)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // role
	p.Update(tea.KeyMsg{Type: tea.KeyEnter}) // apply
	if assigned.Model != "vllm/qwen3" || assigned.Role != "build" {
		t.Fatalf("assigned = %+v, want the custom model through BUILD", assigned)
	}
}
