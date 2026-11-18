package ai

import (
	"fmt"
	"sort"
	"strings"
)

// EndpointSpec declares a user-owned OpenAI-compatible provider in the
// transport plane.
//
// It is the boundary between configuration and credentials: config owns the
// JSON, this package owns the provider instance. Models are prebuilt so the
// resolver never derives a context window or price from a model id; those
// facts came from the user or the catalog, not from a guess.
type EndpointSpec struct {
	// Provider is the provider key, i.e. the first half of "<provider>/<id>".
	Provider string
	// Name is the display name.
	Name string
	// BaseURL is the complete OpenAI-compatible root (including any /v1).
	BaseURL string
	// Models maps model id to its transport-ready facts.
	Models map[string]Model
	// EnvVars names credentials this provider accepts, in preference order.
	EnvVars []string
	// KeyOptional reports that the endpoint works without a credential, as a
	// local LM Studio or vLLM server usually does.
	KeyOptional bool
}

// RegisterEndpoint installs a user-owned provider and its model facts.
//
// Registration is explicit rather than lazy so a malformed endpoint fails
// while the caller still has the configuration in hand. The provider name may
// shadow a bundled one: that is how a gateway in front of a known provider is
// expressed, and the facts in the spec then replace the catalog's for that
// key.
func (r *Registry) RegisterEndpoint(spec EndpointSpec) error {
	provider := strings.TrimSpace(spec.Provider)
	if provider == "" {
		return fmt.Errorf("ai: endpoint has no provider key")
	}
	if strings.ContainsAny(provider, "/ \t") {
		return fmt.Errorf("ai: endpoint provider %q must not contain a slash or whitespace", provider)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(spec.BaseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("ai: endpoint %s has no base URL", provider)
	}
	if len(spec.Models) == 0 {
		return fmt.Errorf("ai: endpoint %s has no models", provider)
	}
	models := make(map[string]Model, len(spec.Models))
	for id, model := range spec.Models {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("ai: endpoint %s has an empty model id", provider)
		}
		if model.ID == "" {
			model.ID = id
		}
		if model.Provider == "" {
			model.Provider = provider
		}
		if model.API == "" {
			model.API = APIOpenAIChatCompletions
		}
		if model.Provider != provider {
			return fmt.Errorf("ai: endpoint %s model %s names provider %s", provider, id, model.Provider)
		}
		if model.BaseURL == "" {
			model.BaseURL = baseURL
		}
		if model.ContextWindow <= 0 || model.MaxOutputTokens <= 0 {
			return fmt.Errorf("ai: endpoint %s model %s has no context/output ceiling", provider, id)
		}
		if model.MaxOutputTokens > model.ContextWindow {
			return fmt.Errorf("ai: endpoint %s model %s max output exceeds its context", provider, id)
		}
		hasText := false
		for _, modality := range model.InputModalities {
			if modality == "text" {
				hasText = true
				break
			}
		}
		if !hasText {
			return fmt.Errorf("ai: endpoint %s model %s does not accept text", provider, id)
		}
		models[id] = model
	}

	r.mu.Lock()
	r.endpoints[provider] = EndpointSpec{
		Provider:    provider,
		Name:        spec.Name,
		BaseURL:     baseURL,
		Models:      models,
		EnvVars:     append([]string(nil), spec.EnvVars...),
		KeyOptional: spec.KeyOptional,
	}
	r.baseURLs[provider] = baseURL
	r.providerEnv[provider] = append([]string(nil), spec.EnvVars...)
	r.keyOptional[provider] = spec.KeyOptional
	// A provider instance cached against an earlier base URL or credential
	// must not survive re-registration.
	for key := range r.providers {
		if strings.HasPrefix(key, provider+"|") {
			delete(r.providers, key)
		}
	}
	r.mu.Unlock()
	return nil
}

// Endpoint returns one registered endpoint.
func (r *Registry) Endpoint(provider string) (EndpointSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.endpoints[provider]
	return spec, ok
}

// RemoveEndpoint removes a user-owned provider and every fact derived from it.
//
// The config authority is the caller's; this only clears the registry so a
// deleted or renamed endpoint cannot keep resolving from a stale provider
// instance. Removing a provider that is not registered is a no-op, which keeps
// delete idempotent after a failed earlier cleanup.
func (r *Registry) RemoveEndpoint(provider string) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return
	}
	r.mu.Lock()
	if _, registered := r.endpoints[provider]; !registered {
		r.mu.Unlock()
		return
	}
	delete(r.endpoints, provider)
	delete(r.baseURLs, provider)
	delete(r.providerEnv, provider)
	delete(r.keyOptional, provider)
	for key := range r.providers {
		if strings.HasPrefix(key, provider+"|") {
			delete(r.providers, key)
		}
	}
	r.mu.Unlock()
}

// EndpointModels returns every registered endpoint model, sorted by provider
// then id. It is the picker's source for user-owned providers.
func (r *Registry) EndpointModels() []Model {
	r.mu.RLock()
	out := make([]Model, 0, len(r.endpoints))
	for _, spec := range r.endpoints {
		for _, id := range sortedIDs(spec.Models) {
			out = append(out, spec.Models[id])
		}
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// CustomModel resolves a "provider/model-id" reference against registered
// endpoints.
//
// handled is false when the reference names no endpoint, so the caller can
// fall through to the catalog. A handled reference with an unknown model is an
// error: silently falling through would let a typo resolve to a different
// provider's model.
func (r *Registry) CustomModel(ref string) (model Model, handled bool, err error) {
	provider, id, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || provider == "" || id == "" {
		return Model{}, false, nil
	}
	r.mu.RLock()
	spec, found := r.endpoints[provider]
	r.mu.RUnlock()
	if !found {
		return Model{}, false, nil
	}
	model, ok = spec.Models[id]
	if !ok {
		ids := sortedIDs(spec.Models)
		return Model{}, true, fmt.Errorf(
			"ai: endpoint %s does not declare model %q (have %s)",
			provider, id, strings.Join(ids, ", "))
	}
	return model, true, nil
}

func sortedIDs(models map[string]Model) []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
