package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidEndpoint reports a user-declared OpenAI-compatible provider whose
// facts are unusable. It is a distinct error so a typo in one endpoint can be
// reported without disabling unrelated configuration.
var ErrInvalidEndpoint = errors.New("config: invalid endpoint")

// Endpoint describes one user-owned OpenAI-compatible provider.
//
// It exists so local servers and internal gateways do not each need a
// hardcoded provider. The model facts are explicit because a local server's
// /models route usually carries no context window or output ceiling; guessing
// either would make compaction fire at the wrong time or a request fail with a
// provider-side 400. There is deliberately no credential field: secrets live
// in credentials.json or an environment variable named by APIKeyEnv.
type Endpoint struct {
	// Name is the display name. Empty falls back to the provider key.
	Name string `json:"name,omitempty"`
	// BaseURL is the complete OpenAI-compatible root, e.g.
	// http://127.0.0.1:1234/v1. The transport appends /chat/completions.
	BaseURL string `json:"baseUrl"`
	// Models lists the model ids this endpoint serves. A reference is
	// "<endpoint-key>/<id>", so the key is the map key in Settings.Endpoints.
	Models []string `json:"models"`
	// APIKeyEnv names the environment variable carrying the credential.
	// Empty means the endpoint is keyless, as most local servers are.
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
	// ContextWindow is the maximum total tokens the model accepts.
	ContextWindow int `json:"contextWindow"`
	// MaxTokens is the largest completion the model emits.
	MaxTokens int `json:"maxTokens"`
	// Input lists accepted modalities. Empty defaults to text.
	Input []string `json:"input,omitempty"`
	// Reasoning reports whether the model accepts a reasoning effort.
	Reasoning bool `json:"reasoning,omitempty"`
	// ReasoningEfforts is the effort ladder, lowest first.
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
	// ReasoningDisable is the provider-native value that turns reasoning off.
	ReasoningDisable string `json:"reasoningDisable,omitempty"`
	// ReasoningMandatory reports a model that reasons whether asked or not.
	ReasoningMandatory bool `json:"reasoningMandatory,omitempty"`
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks one endpoint's facts. The caller supplies the map key so the
// error can name the provider a user typed.
func (e Endpoint) Validate(provider string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("%w: endpoint has no provider key", ErrInvalidEndpoint)
	}
	if strings.ContainsAny(provider, "/ \t") {
		return fmt.Errorf("%w: %q is not a provider key (no slash or whitespace)", ErrInvalidEndpoint, provider)
	}
	parsed, err := url.Parse(strings.TrimSpace(e.BaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%w: %s has no valid baseUrl", ErrInvalidEndpoint, provider)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: %s baseUrl must be http or https", ErrInvalidEndpoint, provider)
	}
	if len(e.Models) == 0 {
		return fmt.Errorf("%w: %s lists no models", ErrInvalidEndpoint, provider)
	}
	seen := map[string]bool{}
	for _, id := range e.Models {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("%w: %s lists an empty model id", ErrInvalidEndpoint, provider)
		}
		if seen[id] {
			return fmt.Errorf("%w: %s lists %q twice", ErrInvalidEndpoint, provider, id)
		}
		seen[id] = true
	}
	if e.ContextWindow <= 0 {
		return fmt.Errorf("%w: %s has no contextWindow", ErrInvalidEndpoint, provider)
	}
	if e.MaxTokens <= 0 {
		return fmt.Errorf("%w: %s has no maxTokens", ErrInvalidEndpoint, provider)
	}
	if e.MaxTokens > e.ContextWindow {
		return fmt.Errorf("%w: %s maxTokens %d exceeds contextWindow %d",
			ErrInvalidEndpoint, provider, e.MaxTokens, e.ContextWindow)
	}
	if e.APIKeyEnv != "" && !envNamePattern.MatchString(e.APIKeyEnv) {
		return fmt.Errorf("%w: %s apiKeyEnv %q is not an environment variable name",
			ErrInvalidEndpoint, provider, e.APIKeyEnv)
	}
	modalities := e.InputModalities()
	if len(modalities) == 0 || modalities[0] != "text" {
		return fmt.Errorf("%w: %s input must include text", ErrInvalidEndpoint, provider)
	}
	if len(e.ReasoningEfforts) > 0 && !e.Reasoning {
		return fmt.Errorf("%w: %s lists reasoning efforts but reasoning is false",
			ErrInvalidEndpoint, provider)
	}
	for _, effort := range e.ReasoningEfforts {
		if strings.TrimSpace(effort) == "" {
			return fmt.Errorf("%w: %s lists an empty reasoning effort", ErrInvalidEndpoint, provider)
		}
	}
	return nil
}

// InputModalities returns the configured modalities, defaulting to text. Text
// is guaranteed to be present and first: every chat transport here requires
// it, and a configuration that omits it is rejected rather than silently
// repaired into a different capability claim.
func (e Endpoint) InputModalities() []string {
	if len(e.Input) == 0 {
		return []string{"text"}
	}
	out := make([]string, 0, len(e.Input))
	for _, modality := range e.Input {
		modality = strings.TrimSpace(modality)
		if modality != "" {
			out = append(out, modality)
		}
	}
	return out
}

// DisplayName returns the human name for the endpoint.
func (e Endpoint) DisplayName(provider string) string {
	if name := strings.TrimSpace(e.Name); name != "" {
		return name
	}
	return provider
}
