package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/provider/creds"
)

// DefaultHTTPTimeout bounds one provider request, including the body read.
//
// A completion can legitimately stream for minutes, so this covers connection
// establishment and response headers rather than the full stream duration.
const DefaultHTTPTimeout = 10 * time.Minute

// ErrMissingAPIKey means no credential was found for the requested provider.
var ErrMissingAPIKey = errors.New("ai: missing API key for provider")

// errUnknownProvider reports a provider with no registered credentials.
var errUnknownProvider = errors.New("ai: unknown provider")

// Registry resolves API keys and providers by provider key.
//
// A Registry is safe for concurrent use.
type Registry struct {
	mu sync.RWMutex
	// apiKeys maps provider key to credential.
	apiKeys map[string]string
	// baseURLs maps provider key to an endpoint override.
	baseURLs map[string]string
	// providers caches constructed providers keyed by "provider|baseURL".
	providers map[string]Provider
	// httpClient is shared so connections are pooled across providers.
	httpClient *http.Client
	// credFile overrides the credential-file path. Empty means the default
	// (~/.ohgo/credentials.json). Tests point it at a temp file so the suite
	// never touches the real home directory.
	credFile string
	// shellOnce caches parsed startup-file assignments: the files are read at
	// most once per registry, because re-reading on every tool call would
	// turn each turn into a filesystem scan.
	shellOnce   sync.Once
	shellValues map[string]string
	shellSource string
}

// NewRegistry returns an empty registry with pooled HTTP defaults.
func NewRegistry() *Registry {
	return &Registry{
		apiKeys:    map[string]string{},
		baseURLs:   map[string]string{},
		providers:  map[string]Provider{},
		httpClient: &http.Client{Timeout: DefaultHTTPTimeout},
	}
}

// SetAPIKey records the credential used for a provider.
func (r *Registry) SetAPIKey(provider, apiKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apiKeys[provider] = apiKey
}

// SetBaseURL records an endpoint override for a provider.
//
// The override replaces the provider's base URL entirely, version prefix
// included. A base rooted at "/v1" therefore yields "/v1/chat/completions",
// while one without it yields "/chat/completions". That is deliberate — the
// alternative, appending a prefix to whatever was given, would make an endpoint
// impossible to express — but it means anyone adding a user-facing flag for this
// must say so in its help text, or a gateway rooted at /v1 will return a 404
// with nothing on screen to explain it.
func (r *Registry) SetBaseURL(provider, baseURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.baseURLs[provider] = baseURL
}

// BaseURLOverride reports the endpoint override for a provider, if any.
// Discovery uses it so a test server or gateway override applies to the
// roster fetch exactly as it does to completions.
func (r *Registry) BaseURLOverride(provider string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	override, ok := r.baseURLs[provider]
	return override, ok && override != ""
}

// APIKey returns the credential for a provider, or an empty string.
func (r *Registry) APIKey(provider string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.apiKeys[provider]
}

// SetCredentialsFile overrides where the file store is read from. Tests use
// it to isolate from the real home directory; production leaves it empty for
// the default.
func (r *Registry) SetCredentialsFile(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.credFile = path
}

// envAPIKeyVars is kept as documentation of the concept; the authoritative
// table lives in creds.ProviderEnvVars so environment and shell discovery
// share it. Nothing here may reintroduce a second copy.

// ResolveAPIKey finds the credential for a provider through the precedence
// chain: explicit registry keys, the credential file, the environment, then
// parsed shell startup files.
//
// The environment lookup is case-insensitive on the variable name only; the
// provider key itself is matched exactly.
func (r *Registry) ResolveAPIKey(provider string) (string, error) {
	key, _, err := r.ResolveAPIKeySource(provider)
	return key, err
}

// ResolveAPIKeySource resolves like ResolveAPIKey and names the winning
// source, e.g. "environment (OPENROUTER_API_KEY)". Diagnostics and the
// connect flow need the source; the transport only needs the key.
func (r *Registry) ResolveAPIKeySource(provider string) (key, source string, err error) {
	if key := r.APIKey(provider); key != "" {
		return key, "explicit", nil
	}
	vars, known := creds.ProviderEnvVars(provider)
	if !known {
		return "", "", fmt.Errorf("%w: %q", errUnknownProvider, provider)
	}
	if store := r.credentialStore(); store != nil {
		if value, ok := store.Get(provider); ok {
			return value, "credential file", nil
		}
	}
	for _, name := range vars {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, "environment (" + name + ")", nil
		}
	}
	r.shellOnce.Do(func() {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil || home == "" {
			return
		}
		values, sources := creds.DiscoverShellAssignments(home)
		r.shellValues = values
		if len(sources) > 0 {
			r.shellSource = sources[0]
		}
	})
	for _, name := range vars {
		if value := strings.TrimSpace(r.shellValues[name]); value != "" {
			source := "shell file"
			if r.shellSource != "" {
				source += " (" + r.shellSource + ")"
			}
			return value, source, nil
		}
	}
	if len(vars) == 0 {
		return "", "none required", nil
	}
	return "", "", fmt.Errorf("%w: %s (set %s)", ErrMissingAPIKey, provider, strings.Join(vars, " or "))
}

// credentialStore opens the file store, or returns nil when it cannot be
// located or parsed. A broken store must not break resolution for everyone
// else: doctor reports it separately, where the fix belongs.
func (r *Registry) credentialStore() *creds.Store {
	r.mu.RLock()
	path := r.credFile
	r.mu.RUnlock()
	if path == "" {
		var err error
		path, err = creds.DefaultPath()
		if err != nil {
			return nil
		}
	}
	store, err := creds.Open(path)
	if err != nil {
		return nil
	}
	return store
}

// ProviderFor returns the provider that speaks a model's wire protocol.
//
// The same provider instance is returned for repeated calls with the same
// provider key and base URL.
func (r *Registry) ProviderFor(model Model) (Provider, error) {
	// An explicit registry override wins over the model's own BaseURL.
	//
	// The catalog ships a BaseURL as a *default*; SetBaseURL is the operator's
	// choice, whether for a gateway or a test server. When Model.BaseURL won
	// instead, any registry override was silently ignored and requests went to
	// the provider's public endpoint with whatever credential happened to be
	// present.
	baseURL := model.BaseURL
	if override := r.baseURLs[model.Provider]; override != "" {
		baseURL = override
	}

	// The cache key is the endpoint actually used, not the model's own field.
	//
	// Keying on model.BaseURL while resolving the endpoint from an override
	// meant two different overrides for one provider collided: the second
	// caller got the provider built for the first's endpoint, and every request
	// silently went to the wrong host.
	cacheKey := model.Provider + "|" + baseURL

	r.mu.RLock()
	if provider, ok := r.providers[cacheKey]; ok {
		r.mu.RUnlock()
		return provider, nil
	}
	r.mu.RUnlock()

	var provider Provider
	switch model.API {
	case APIAnthropicMessages:
		key, keyErr := r.ResolveAPIKey(model.Provider)
		if keyErr != nil {
			return nil, keyErr
		}
		anthropic := NewAnthropicProvider(key)
		anthropic.Client = r.httpClient
		anthropic.BaseURL = baseURL
		// A bearer-token environment variable means the deployment is a gateway
		// that rejects x-api-key.
		if model.Provider == "anthropic" && os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
			anthropic.AuthToken = key
			anthropic.APIKey = ""
		}
		provider = anthropic
	case APIOpenAIChatCompletions:
		key, keyErr := r.ResolveAPIKey(model.Provider)
		if keyErr != nil {
			return nil, keyErr
		}
		openai := NewOpenAIProvider(key)
		openai.Client = r.httpClient
		openai.BaseURL = baseURL
		// OpenRouter asks senders to identify for analytics and routing
		// priority. These are application constants, never user data.
		if model.Provider == "openrouter" {
			openai.ExtraHeaders = map[string]string{
				"HTTP-Referer": "https://pizenlabs.dev/ohgo-agent/",
				"X-Title":      "ohgo",
			}
		}
		provider = openai
	case APIOllamaChat:
		// Local Ollama needs no key: ResolveAPIKey reports "none required"
		// rather than an error, and the transport omits the auth header.
		key, keyErr := r.ResolveAPIKey(model.Provider)
		if keyErr != nil {
			return nil, keyErr
		}
		ollama := NewOllamaProvider(key)
		ollama.Client = r.httpClient
		ollama.BaseURL = baseURL
		provider = ollama
	default:
		return nil, unknownAPIError{API: model.API}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.providers[cacheKey]; ok {
		return existing, nil
	}
	r.providers[cacheKey] = provider
	return provider, nil
}

// StreamFn opens a completion. Implementations must return a non-nil stream
// whenever err is nil.
//
// The agent loop depends on this seam so tests can substitute a scripted
// provider without an HTTP server.
type StreamFn func(ctx context.Context, req Request) (*Stream, error)

// RegistryStream adapts a Registry to StreamFn.
func (r *Registry) StreamFn() StreamFn {
	return func(ctx context.Context, req Request) (*Stream, error) {
		provider, err := r.ProviderFor(req.Model)
		if err != nil {
			return nil, err
		}
		return provider.Stream(ctx, req)
	}
}

// Well-known models live in the catalog package.
//
// They used to be hand-written structs here, and they had drifted: the context
// windows and prices no longer matched the providers. A wrong context window
// makes compaction fire at the wrong time and a wrong price makes the cost
// display a lie, and neither shows a symptom. Resolve models through
// catalog.Resolve instead of adding them back here.

// StreamFn opens a completion. Implementations must return a non-nil stream
// whenever err is nil.
