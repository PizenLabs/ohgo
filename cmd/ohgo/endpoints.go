package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// registerEndpoints installs every user-declared OpenAI-compatible provider on
// the registry and returns human-readable warnings for entries it refused.
//
// A malformed endpoint is reported and skipped rather than aborting the whole
// session: the user may have several endpoints and one typo must not make an
// unrelated model unreachable. The warning names the fix.
func registerEndpoints(registry *ai.Registry, settings config.Settings) []string {
	if registry == nil || len(settings.Endpoints) == 0 {
		return nil
	}
	names := make([]string, 0, len(settings.Endpoints))
	builtin := map[string]bool{}
	for _, name := range catalog.ProviderNames() {
		builtin[name] = true
	}
	for name := range settings.Endpoints {
		names = append(names, name)
	}
	sort.Strings(names)

	var warnings []string
	for _, name := range names {
		if name == "custom" {
			warnings = append(warnings, "endpoint custom conflicts with the model control plane's Custom row; rename it")
			continue
		}
		if builtin[name] {
			warnings = append(warnings, fmt.Sprintf(
				"endpoint %s conflicts with the built-in provider of the same name; rename it", name))
			continue
		}
		endpoint := settings.Endpoints[name]
		endpoint.EnsureModelFacts()
		if err := endpoint.Validate(name); err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		baseURL := strings.TrimRight(strings.TrimSpace(endpoint.BaseURL), "/")
		if len(endpoint.Models) == 0 {
			// A configured address with no models is a valid state, not a
			// provider yet. The pane reads it from settings; registration
			// waits until the user explicitly adds a model.
			continue
		}
		models := make(map[string]ai.Model, len(endpoint.Models))
		for _, id := range endpoint.Models {
			id = strings.TrimSpace(id)
			models[id] = ai.Model{
				ID:                 id,
				Name:               id,
				Provider:           name,
				API:                ai.APIOpenAIChatCompletions,
				BaseURL:            baseURL,
				ContextWindow:      endpoint.ContextWindow,
				ContextAutomatic:   endpoint.FactsAutomatic,
				MaxOutputTokens:    endpoint.MaxTokens,
				MaxOutputAutomatic: endpoint.FactsAutomatic,
				Reasoning:          endpoint.Reasoning,
				ReasoningEfforts:   append([]string(nil), endpoint.ReasoningEfforts...),
				ReasoningDisable:   endpoint.ReasoningDisable,
				ReasoningMandatory: endpoint.ReasoningMandatory,
				InputModalities:    endpoint.InputModalities(),
			}
		}
		spec := ai.EndpointSpec{
			Provider:    name,
			Name:        endpoint.DisplayName(name),
			BaseURL:     baseURL,
			Models:      models,
			KeyOptional: endpoint.APIKeyEnv == "",
		}
		if endpoint.APIKeyEnv != "" {
			spec.EnvVars = []string{endpoint.APIKeyEnv}
		}
		if err := registry.RegisterEndpoint(spec); err != nil {
			warnings = append(warnings, fmt.Sprintf("endpoint %s: %v", name, err))
		}
	}
	return warnings
}

// endpointModels returns the registered endpoint models, or nil when the
// registry is unavailable. It is a nil-safe read for presentation code.
func endpointModels(registry *ai.Registry) []ai.Model {
	if registry == nil {
		return nil
	}
	return registry.EndpointModels()
}

// modelRefs lists every selectable reference: bundled catalog rows first, then
// configured endpoint models.
func modelRefs(registry *ai.Registry) []string {
	catalogModels := catalog.List()
	ids := make([]string, 0, len(catalogModels))
	for _, model := range catalogModels {
		ids = append(ids, model.Ref())
	}
	for _, model := range endpointModels(registry) {
		ids = append(ids, model.Ref())
	}
	return ids
}
