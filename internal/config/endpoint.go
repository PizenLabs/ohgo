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
// hardcoded provider. A local server's /models route usually carries no context
// window or output ceiling, so explicit facts win when present; otherwise the
// endpoint gets conservative defaults marked FactsAutomatic, because an
// unlabeled guess would make compaction fire at the wrong time or a request
// fail with a provider-side 400. Models may be empty between saving an address
// and selecting discovered models. There is deliberately no credential field:
// secrets live in credentials.json or an environment variable named by
// APIKeyEnv.
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
	// FactsAutomatic reports ContextWindow and MaxTokens are conservative
	// fallbacks because discovery exposed no capability metadata. The pane
	// labels them as automatic; they are still real limits the runtime uses.
	FactsAutomatic bool `json:"factsAutomatic,omitempty"`
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

// Conservative defaults for an endpoint whose discovery route exposes no
// context window. They are deliberately small: undershooting the server's
// window wastes some room, while overshooting produces a provider-side 400
// that looks like an ohgo bug. The endpoint pane labels these values as
// automatic instead of pretending they were discovered.
const (
	// DefaultEndpointContextWindow is the fallback total token window.
	DefaultEndpointContextWindow = 32768
	// DefaultEndpointMaxTokens is the fallback completion ceiling.
	DefaultEndpointMaxTokens = 4096
)

// EndpointKey derives the stable provider key for a display name.
//
// The key is the model reference prefix, so it may not contain "/" or
// whitespace. A name with no usable ASCII character gets an empty key; the
// caller reports that rather than inventing an opaque identity.
func EndpointKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '.' || r == '_' || r == '-':
			if !lastDash {
				b.WriteRune(r)
				lastDash = true
			}
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-._")
}

// EnsureModelFacts fills the automatic context and output ceilings when an
// endpoint has none. It never overwrites a positive value, so a user edit of a
// known fact survives; callers that persist the result also persist
// FactsAutomatic.
func (e *Endpoint) EnsureModelFacts() {
	automatic := false
	if e.ContextWindow <= 0 {
		e.ContextWindow = DefaultEndpointContextWindow
		automatic = true
	}
	if e.MaxTokens <= 0 {
		e.MaxTokens = DefaultEndpointMaxTokens
		automatic = true
		if e.MaxTokens > e.ContextWindow {
			e.MaxTokens = e.ContextWindow
		}
	}
	if automatic {
		e.FactsAutomatic = true
	}
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
	// A configured endpoint with no model yet is a valid state: the user may
	// save the address first and add discovered models after probing. The
	// roster says zero rather than inventing a model id.
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
	if e.ContextWindow < 0 || e.MaxTokens < 0 {
		return fmt.Errorf("%w: %s has a negative context/output ceiling", ErrInvalidEndpoint, provider)
	}
	if e.ContextWindow > 0 && e.MaxTokens > e.ContextWindow {
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
