package main

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

func endpointSettings() config.Settings {
	return config.Settings{
		Endpoints: map[string]config.Endpoint{
			"lmstudio": {
				Name:          "LM Studio",
				BaseURL:       "http://127.0.0.1:1234/v1",
				Models:        []string{"qwen3-coder"},
				ContextWindow: 262144,
				MaxTokens:     32768,
			},
		},
	}
}

// TestEndpointSettingsDriveTheRuntime is the integration contract: a settings
// entry resolves and builds a provider without a catalog rebuild.
func TestEndpointSettingsDriveTheRuntime(t *testing.T) {
	settings := endpointSettings()
	registry := ai.NewRegistry()
	registry.SetCredentialsFile(t.TempDir() + "/credentials.json")
	if warnings := registerEndpoints(registry, settings); len(warnings) > 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	model, err := dynamicResolver(context.Background(), registry)("lmstudio/qwen3-coder")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if model.ContextWindow != 262144 || model.MaxOutputTokens != 32768 {
		t.Errorf("model facts = %+v", model)
	}
	provider, err := registry.ProviderFor(model)
	if err != nil {
		t.Fatalf("ProviderFor: %v", err)
	}
	if _, ok := provider.(*ai.OpenAIProvider); !ok {
		t.Errorf("provider = %T, want the shared OpenAI transport", provider)
	}
	refs := modelRefs(registry)
	found := false
	for _, ref := range refs {
		if ref == "lmstudio/qwen3-coder" {
			found = true
		}
	}
	if !found {
		t.Errorf("endpoint model missing from %d selectable refs", len(refs))
	}
}

// TestEndpointNameCannotShadowABuiltin keeps endpoint resolution and the
// name-based transport mapping from disagreeing about one provider.
func TestEndpointNameCannotShadowABuiltin(t *testing.T) {
	settings := endpointSettings()
	settings.Endpoints["anthropic"] = settings.Endpoints["lmstudio"]
	registry := ai.NewRegistry()
	registry.SetCredentialsFile(t.TempDir() + "/credentials.json")
	warnings := registerEndpoints(registry, settings)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "conflicts") {
		t.Fatalf("warnings = %v, want one conflict", warnings)
	}
	if _, handled, _ := registry.CustomModel("anthropic/anything"); handled {
		t.Error("a conflicting endpoint was registered")
	}
}

// TestMalformedEndpointIsReportedAndSkipped keeps one typo from disabling an
// unrelated endpoint.
func TestMalformedEndpointIsReportedAndSkipped(t *testing.T) {
	settings := endpointSettings()
	bad := settings.Endpoints["lmstudio"]
	bad.MaxTokens = bad.ContextWindow + 1
	settings.Endpoints["broken"] = bad
	registry := ai.NewRegistry()
	registry.SetCredentialsFile(t.TempDir() + "/credentials.json")
	warnings := registerEndpoints(registry, settings)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "exceeds") {
		t.Fatalf("warnings = %v", warnings)
	}
	if _, err := dynamicResolver(context.Background(), registry)("lmstudio/qwen3-coder"); err != nil {
		t.Errorf("the valid endpoint became unreachable: %v", err)
	}
	if _, handled, err := registry.CustomModel("broken/qwen3-coder"); handled || err != nil {
		t.Errorf("broken endpoint handled=%v err=%v, want it skipped", handled, err)
	}
}
