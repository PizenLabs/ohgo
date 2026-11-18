package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// endpointHarness builds the same global-settings authority the TUI writes
// through, with an isolated HOME and a fresh registry.
func endpointHarness(t *testing.T) (*harness, *config.Settings) {
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
	h := newHarness(registry, nil, nil, nil, nil, &settings, t.TempDir(), model)
	return h, &settings
}

// TestCustomEndpointSavePersistsAndDerivesCatalog is the central causal chain:
// the TUI's Save hook writes ~/.ohgo/settings.json, the registry is rebuilt, a
// model reference resolves, and a fresh load reconstructs the same endpoint.
func TestCustomEndpointSavePersistsAndDerivesCatalog(t *testing.T) {
	h, settings := endpointHarness(t)
	endpoints, err := h.SaveEndpoint(tui.EndpointDraft{
		Name:    "vLLM Local",
		BaseURL: "http://127.0.0.1:8000/v1",
		Models:  []string{"qwen3-coder", "qwen3-32b", "qwen3-coder"},
	})
	if err != nil {
		t.Fatalf("SaveEndpoint: %v", err)
	}
	if len(endpoints) != 1 || endpoints[0].Key != "vllm-local" || len(endpoints[0].Models) != 2 {
		t.Fatalf("endpoints = %+v, want one deduplicated vllm-local", endpoints)
	}
	if settings.Endpoints["vllm-local"].ContextWindow != config.DefaultEndpointContextWindow {
		t.Errorf("context = %d, want the explicit automatic fallback", settings.Endpoints["vllm-local"].ContextWindow)
	}
	if !settings.Endpoints["vllm-local"].FactsAutomatic {
		t.Error("automatic context fallback was not labeled")
	}

	model, handled, err := h.registry.CustomModel("vllm-local/qwen3-coder")
	if err != nil || !handled || model.BaseURL != "http://127.0.0.1:8000/v1" {
		t.Fatalf("registry custom model = %+v handled=%v err=%v", model, handled, err)
	}
	snapshot := catalogSnapshot(h.registry)
	found := false
	for _, option := range snapshot.Options {
		if option.ID == "vllm-local/qwen3-coder" {
			found = true
			if !option.ContextAutomatic || !option.MaxOutputAutomatic {
				t.Errorf("snapshot did not preserve the automatic fact labels: %+v", option)
			}
			if option.ReasoningKnown {
				t.Errorf("snapshot claimed thinking support is known for an unconfigured endpoint: %+v", option)
			}
		}
	}
	if !found {
		t.Errorf("catalog snapshot does not contain the endpoint model")
	}

	raw, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".ohgo", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted config.Settings
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if endpoint, ok := persisted.Endpoints["vllm-local"]; !ok {
		t.Fatal("endpoint missing from the global settings file")
	} else if !endpoint.FactsAutomatic {
		t.Error("automatic fallback label was not persisted")
	}

	// A restart is a fresh Settings load plus a fresh registry: the endpoint
	// must come back from configuration alone.
	reloaded, err := config.Load("")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	registry := ai.NewRegistry()
	restarted := newHarness(registry, nil, nil, nil, nil, &reloaded, t.TempDir(), catalog.MustResolve(catalog.DefaultRef))
	if got := len(restarted.EndpointOptions()); got != 1 {
		t.Fatalf("restarted endpoints = %d, want 1", got)
	}
	if model, handled, err := registry.CustomModel("vllm-local/qwen3-coder"); err != nil || !handled || model.ID != "qwen3-coder" {
		t.Fatalf("restart did not re-register the model: %+v handled=%v err=%v", model, handled, err)
	}
}

// TestCustomEndpointEditKeepsIdentityAndRefusesDuplicates covers the edit
// transaction: the key is stable, the changed URL reaches the registry, and a
// second endpoint with the same display name is refused without writing.
func TestCustomEndpointEditKeepsIdentityAndRefusesDuplicates(t *testing.T) {
	h, _ := endpointHarness(t)
	if _, err := h.SaveEndpoint(tui.EndpointDraft{Name: "Local", BaseURL: "http://127.0.0.1:8000/v1"}); err != nil {
		t.Fatal(err)
	}
	endpoints, err := h.SaveEndpoint(tui.EndpointDraft{
		Key: "local", Name: "Local Renamed", BaseURL: "http://127.0.0.1:9000/v1", Models: []string{"m1"},
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if len(endpoints) != 1 || endpoints[0].Key != "local" || endpoints[0].Name != "Local Renamed" {
		t.Fatalf("edit = %+v, want the same key with a new display name", endpoints)
	}
	spec, ok := h.registry.Endpoint("local")
	if !ok || spec.BaseURL != "http://127.0.0.1:9000/v1" {
		t.Fatalf("registry endpoint = %+v, want the edited base URL", spec)
	}

	if _, err := h.SaveEndpoint(tui.EndpointDraft{Name: "Local", BaseURL: "http://127.0.0.1:8000/v1"}); err == nil {
		t.Fatal("a duplicate display name overwrote or created a second endpoint")
	}
	if got := len(h.EndpointOptions()); got != 1 {
		t.Errorf("endpoints after duplicate = %d, want 1", got)
	}
}

// TestCustomEndpointDeleteClearsReferences proves deleting removes the
// endpoint from configuration, the registry, and every role that pointed at
// it, so nothing dangles into a different provider.
func TestCustomEndpointDeleteClearsReferences(t *testing.T) {
	h, settings := endpointHarness(t)
	if _, err := h.SaveEndpoint(tui.EndpointDraft{
		Name: "vllm", BaseURL: "http://127.0.0.1:8000/v1", Models: []string{"qwen3"},
	}); err != nil {
		t.Fatal(err)
	}
	settings.Model = "vllm/qwen3"
	settings.Roles["build"] = config.Profile{Model: "vllm/qwen3", Effort: "high"}
	if err := config.Save(*settings); err != nil {
		t.Fatal(err)
	}

	endpoints, err := h.DeleteEndpoint("vllm")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("remaining endpoints = %+v, want none", endpoints)
	}
	if _, ok := h.registry.Endpoint("vllm"); ok {
		t.Error("registry still has the deleted endpoint")
	}
	reloaded, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Endpoints["vllm"]; ok {
		t.Error("settings still has the deleted endpoint")
	}
	if reloaded.Roles["build"].Model != "" || reloaded.Model != "" {
		t.Errorf("dangling references survived: role=%q model=%q", reloaded.Roles["build"].Model, reloaded.Model)
	}
	if _, handled, _ := h.registry.CustomModel("vllm/qwen3"); handled {
		t.Error("deleted endpoint still resolves")
	}
}

// TestCustomEndpointProbeIsEvidenceOnly proves discovery returns model ids and
// does not mutate the endpoint roster until Save is called.
func TestCustomEndpointProbeIsEvidenceOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearProviderKeys(t)
	t.Setenv("VLLM_API_KEY", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization = %q, want the environment key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-32b"},{"id":"qwen3-coder"}]}`))
	}))
	defer server.Close()

	settings := config.Default()
	h := newHarness(ai.NewRegistry(), nil, nil, nil, nil, &settings, t.TempDir(), catalog.MustResolve(catalog.DefaultRef))
	result := h.ProbeEndpoint(context.Background(), tui.EndpointDraft{
		Name: "vllm", BaseURL: server.URL + "/v1", APIKeyEnv: "VLLM_API_KEY",
	})
	if !result.Reachable || !result.Authenticated || result.Models != 2 {
		t.Fatalf("probe = %+v, want reachable/authenticated/2", result)
	}
	if len(result.ModelIDs) != 2 || result.ModelIDs[0] != "qwen3-32b" {
		t.Errorf("discovered ids = %v", result.ModelIDs)
	}
	if len(h.EndpointOptions()) != 0 || len(settings.Endpoints) != 0 {
		t.Error("probe mutated endpoint configuration")
	}
}

// TestCustomEndpointRejectsBuiltinAndReservedNames protects the model-reference
// namespace from a user endpoint shadowing a provider or the Custom row.
func TestCustomEndpointRejectsBuiltinAndReservedNames(t *testing.T) {
	h, _ := endpointHarness(t)
	for _, name := range []string{"anthropic", "custom"} {
		if _, err := h.SaveEndpoint(tui.EndpointDraft{Name: name, BaseURL: "http://127.0.0.1:8000/v1"}); err == nil {
			t.Errorf("endpoint name %q was accepted", name)
		}
	}
	if again, err := h.SaveEndpoint(tui.EndpointDraft{Name: "anonymous", BaseURL: "not a url"}); err == nil {
		t.Errorf("invalid URL accepted: %+v", again)
	}
	if _, ok := h.settingsOrDefault().Endpoints["anonymous"]; ok {
		t.Error("a rejected endpoint was written")
	}
}

// TestProviderPaneCustomRowIsNotAProvider keeps the row out of the flat
// provider roster (/usage, /connect) while model control gets the control row.
func TestProviderPaneCustomRowIsNotAProvider(t *testing.T) {
	h, _ := endpointHarness(t)
	for _, row := range h.ProviderRows() {
		if row.Custom || row.ID == tui.CustomProvider {
			t.Fatalf("flat provider roster contains the Custom control row: %+v", row)
		}
	}
	pane := h.ProviderPaneRows()
	if len(pane) == 0 || !pane[len(pane)-1].Custom || pane[len(pane)-1].Name != tui.CustomProviderName {
		t.Fatalf("provider pane = %+v, want Custom last", pane)
	}
}
