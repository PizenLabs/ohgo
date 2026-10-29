// Package search implements bounded, provenance-preserving external search.
//
// A provider is a transport seam: it performs exactly one bounded request and
// returns normalized hits or a classified failure. Budget accounting, retries,
// cancellation, and evidence publication live in the capability tool, so every
// attempt is charged to the same root grant and a retry cannot bypass it.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/external/knowledge"
)

// Hard limits. They are runtime constants, never model-controlled.
const (
	// MaxResultsLimit is the largest page a provider may be asked for.
	MaxResultsLimit = 20
	// DefaultMaxResults is the page size when the model does not choose one.
	DefaultMaxResults = 8
	// MaxQueryChars bounds the query sent to a provider.
	MaxQueryChars = 512
	// DefaultMaxBytes bounds one provider response body.
	DefaultMaxBytes = 256 * 1024
	// MaxResponseBytes is an absolute ceiling on one provider response.
	MaxResponseBytes = 1024 * 1024
	// DefaultTimeout bounds one provider request.
	DefaultTimeout = 15 * time.Second
	// MaxTimeout is an absolute ceiling on one provider request.
	MaxTimeout = 60 * time.Second
	// MaxRetries is the largest number of extra attempts a configuration may
	// request. Every attempt is separately charged.
	MaxRetries = 2
	// DefaultSnippetChars bounds one hit's published text.
	DefaultSnippetChars = 1200
)

// Request is one bounded search request.
type Request struct {
	// Query is the untrusted search text. It is bounded before it is sent.
	Query string
	// Limit is the maximum number of hits requested.
	Limit int
	// MaxBytes bounds the provider response body.
	MaxBytes int
	// MaxSnippetChars bounds one hit's published text.
	MaxSnippetChars int
}

// Hit is one normalized search result with provenance.
type Hit struct {
	// Title is the provider's title, bounded.
	Title string
	// Source is the display source (the URL host when the provider gives no
	// name).
	Source string
	// URL is the locator the provider returned. It is data and is never
	// dereferenced by the search capability.
	URL string
	// Snippet is the bounded provider description.
	Snippet string
	// RetrievedAt is when the provider response was observed.
	RetrievedAt time.Time
}

// Response is one normalized provider answer.
type Response struct {
	// Hits are the normalized results, at most Request.Limit.
	Hits []Hit
	// Provider is the configured provider name.
	Provider string
	// Bytes is the response body size observed.
	Bytes int
	// Truncated reports that results were cut at a bound.
	Truncated bool
	// Reason names the bound that cut them.
	Reason string
	// Partial reports that some provider results were unusable while others
	// were returned.
	Partial bool
	// RetrievedAt is the observation time.
	RetrievedAt time.Time
}

// Provider performs exactly one bounded search request.
type Provider interface {
	// Name is the configured provider name.
	Name() string
	// Search performs one request. It must honour ctx and every bound in req.
	Search(ctx context.Context, req Request) (Response, error)
}

// Doer is the HTTP seam. Tests substitute a scripted transport; production
// uses an http.Client with a bounded timeout.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// ProviderConfig configures one HTTP search provider.
type ProviderConfig struct {
	// Name is the model-facing provider name.
	Name string
	// Kind is the wire shape: "brave" or "tavily". It is explicit so a custom
	// endpoint is never guessed at from its hostname.
	Kind string
	// Endpoint is the provider URL. Production requires https.
	Endpoint string
	// APIKeyEnv names the environment variable holding the credential. The
	// value is never stored, logged, or placed in a result.
	APIKeyEnv string
	// Timeout bounds one request.
	Timeout time.Duration
	// MaxBytes bounds one response body.
	MaxBytes int
	// MaxResults bounds the page size a caller may request.
	MaxResults int
	// Retries is the number of extra attempts the capability may make. It is
	// configuration, never model input.
	Retries int
	// Client is the HTTP seam. Nil builds a bounded client.
	Client Doer
	// Getenv resolves a credential. Nil uses os.Getenv.
	Getenv func(string) string
	// Now supplies retrieval timestamps. Nil uses time.Now.
	Now func() time.Time
	// AllowHTTP permits an http endpoint. It exists for httptest and local
	// gateways; production configuration leaves it false.
	AllowHTTP bool
}

// Client is the set of configured search providers.
type Client struct {
	providers map[string]*httpProvider
	order     []string
	defaultID string
}

// New builds a client from provider configurations.
//
// A malformed provider is a configuration error: the client is refused rather
// than silently dropping the provider the user configured.
func New(configs []ProviderConfig) (*Client, error) {
	client := &Client{providers: map[string]*httpProvider{}}
	for _, cfg := range configs {
		provider, err := newHTTPProvider(cfg)
		if err != nil {
			return nil, err
		}
		if _, exists := client.providers[provider.Name()]; exists {
			return nil, fmt.Errorf("search: duplicate provider %q", provider.Name())
		}
		client.providers[provider.Name()] = provider
		client.order = append(client.order, provider.Name())
		if client.defaultID == "" {
			client.defaultID = provider.Name()
		}
	}
	if len(client.providers) == 0 {
		return nil, errors.New("search: no providers configured")
	}
	return client, nil
}

// SetDefault chooses the provider used when a request names none.
func (c *Client) SetDefault(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if _, ok := c.providers[name]; !ok {
		return fmt.Errorf("search: unknown default provider %q", name)
	}
	c.defaultID = name
	return nil
}

// Default returns the provider used when a request names none.
func (c *Client) Default() string { return c.defaultID }

// Names returns the configured provider names in configuration order.
func (c *Client) Names() []string { return append([]string(nil), c.order...) }

// Get returns a configured provider.
func (c *Client) Get(name string) (Provider, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = c.defaultID
	}
	provider, ok := c.providers[name]
	if !ok {
		return nil, knowledge.Fail(knowledge.FailureNotConfigured, false,
			"search provider %q is not configured (configured: %s)", name, strings.Join(c.order, ", "))
	}
	return provider, nil
}

// Retries returns the configured retry allowance for a provider.
func (c *Client) Retries(name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		name = c.defaultID
	}
	if provider, ok := c.providers[name]; ok {
		return provider.retries
	}
	return 0
}

// ValidateRequest bounds a request before any network or budget activity.
func ValidateRequest(req Request) error {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return knowledge.Fail(knowledge.FailureInvalidRequest, false, "search query must not be empty")
	}
	if len([]rune(query)) > MaxQueryChars {
		return knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"search query exceeds %d characters", MaxQueryChars)
	}
	if req.Limit <= 0 || req.Limit > MaxResultsLimit {
		return knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"search result limit must be between 1 and %d", MaxResultsLimit)
	}
	if req.MaxBytes <= 0 || req.MaxBytes > MaxResponseBytes {
		return knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"search response bound must be between 1 and %d bytes", MaxResponseBytes)
	}
	return nil
}

// httpProvider owns one provider's endpoint, credential, and bounds.
type httpProvider struct {
	name       string
	kind       string
	endpoint   *url.URL
	apiKeyEnv  string
	timeout    time.Duration
	maxBytes   int
	maxResults int
	retries    int
	client     Doer
	getenv     func(string) string
	now        func() time.Time
}

// newHTTPProvider validates and bounds one configuration.
func newHTTPProvider(cfg ProviderConfig) (*httpProvider, error) {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		return nil, errors.New("search: provider name is required")
	}
	endpoint, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("search: provider %q endpoint: %w", name, err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("search: provider %q endpoint must be http or https", name)
	}
	if endpoint.Scheme != "https" && !cfg.AllowHTTP {
		return nil, fmt.Errorf("search: provider %q endpoint must use https", name)
	}
	if endpoint.Host == "" {
		return nil, fmt.Errorf("search: provider %q endpoint has no host", name)
	}
	if endpoint.User != nil {
		return nil, fmt.Errorf("search: provider %q endpoint must not embed credentials", name)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		timeout = MaxTimeout
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxBytes > MaxResponseBytes {
		maxBytes = MaxResponseBytes
	}
	maxResults := cfg.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	if maxResults > MaxResultsLimit {
		maxResults = MaxResultsLimit
	}
	retries := cfg.Retries
	if retries < 0 {
		retries = 0
	}
	if retries > MaxRetries {
		retries = MaxRetries
	}
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	kind := strings.ToLower(strings.TrimSpace(cfg.Kind))
	if kind == "" {
		return nil, fmt.Errorf("search: provider %q kind is required (brave or tavily)", name)
	}
	if kind != "brave" && kind != "tavily" {
		return nil, fmt.Errorf("search: provider %q kind must be brave or tavily, got %q", name, cfg.Kind)
	}
	return &httpProvider{
		name:       name,
		kind:       kind,
		endpoint:   endpoint,
		apiKeyEnv:  strings.TrimSpace(cfg.APIKeyEnv),
		timeout:    timeout,
		maxBytes:   maxBytes,
		maxResults: maxResults,
		retries:    retries,
		client:     client,
		getenv:     getenv,
		now:        now,
	}, nil
}

// Name implements Provider.
func (p *httpProvider) Name() string { return p.name }

// Search implements Provider with exactly one request. Retries are owned by
// the capability tool so each attempt consumes the root budget.
func (p *httpProvider) Search(ctx context.Context, req Request) (Response, error) {
	if err := ValidateRequest(req); err != nil {
		return Response{}, err
	}
	limit := req.Limit
	if limit > p.maxResults {
		limit = p.maxResults
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 || maxBytes > p.maxBytes {
		maxBytes = p.maxBytes
	}
	apiKey := strings.TrimSpace(p.getenv(p.apiKeyEnv))
	if p.apiKeyEnv != "" && apiKey == "" {
		return Response{}, knowledge.Fail(knowledge.FailureAuthentication, false,
			"credential for search provider %q is not configured", p.name)
	}

	requestCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	var (
		httpReq *http.Request
		err     error
	)
	switch p.kind {
	case "tavily":
		httpReq, err = p.tavilyRequest(requestCtx, req.Query, limit, apiKey)
	default:
		httpReq, err = p.braveRequest(requestCtx, req.Query, limit, apiKey)
	}
	if err != nil {
		return Response{}, knowledge.Fail(knowledge.FailureInvalidRequest, false, "build search request: %v", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Response{}, knowledge.Fail(knowledge.FailureTimeout, false, "search request timed out")
			}
			return Response{}, knowledge.Fail(knowledge.FailureCancelled, false, "search request cancelled")
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return Response{}, knowledge.Fail(knowledge.FailureTimeout, false, "search request timed out after %s", p.timeout)
		}
		return Response{}, knowledge.Fail(knowledge.FailureUnavailable, true, "search provider request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if class, failed := knowledge.ClassifyHTTP(resp.StatusCode); failed {
		retryable := class == knowledge.FailureRateLimited || class == knowledge.FailureUnavailable || class == knowledge.FailureTimeout
		failure := knowledge.Fail(class, retryable, "search provider returned HTTP %d", resp.StatusCode)
		failure.StatusCode = resp.StatusCode
		return Response{}, failure
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if readErr != nil {
		if ctx.Err() != nil {
			return Response{}, knowledge.Fail(knowledge.FailureCancelled, false, "search request cancelled")
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return Response{}, knowledge.Fail(knowledge.FailureTimeout, false, "search response timed out")
		}
		return Response{}, knowledge.Fail(knowledge.FailureUnavailable, true, "read search response: %v", readErr)
	}
	if len(body) > maxBytes {
		return Response{}, knowledge.Fail(knowledge.FailureResponseLimit, false,
			"search response exceeded %d bytes", maxBytes)
	}
	if len(body) == 0 {
		return Response{}, knowledge.Fail(knowledge.FailureMalformedResponse, false, "search provider returned an empty body")
	}

	var wire []wireHit
	switch p.kind {
	case "tavily":
		wire, err = parseTavily(body)
	default:
		wire, err = parseBrave(body)
	}
	if err != nil {
		return Response{}, knowledge.Fail(knowledge.FailureMalformedResponse, false,
			"search provider response is malformed: %v", err)
	}

	retrievedAt := p.now().UTC()
	hits := make([]Hit, 0, limit)
	partial := false
	for _, item := range wire {
		if len(hits) >= limit {
			break
		}
		hit, err := p.normalize(item, req.MaxSnippetChars, retrievedAt)
		if err != nil {
			partial = true
			continue
		}
		hits = append(hits, hit)
	}
	response := Response{
		Hits:        hits,
		Provider:    p.name,
		Bytes:       len(body),
		RetrievedAt: retrievedAt,
		Partial:     partial,
	}
	if len(wire) > limit {
		response.Truncated = true
		response.Reason = "result_limit"
	}
	return response, nil
}

// wireHit is the provider-neutral result shape.
type wireHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"description"`
	Content string `json:"content"`
}

// parseBrave decodes a Brave web search response.
func parseBrave(body []byte) ([]wireHit, error) {
	var envelope struct {
		Web struct {
			Results []json.RawMessage `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	return decodeItems(envelope.Web.Results)
}

// parseTavily decodes a Tavily search response.
func parseTavily(body []byte) ([]wireHit, error) {
	var envelope struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	return decodeItems(envelope.Results)
}

// decodeItems decodes each provider item independently, so one malformed item
// becomes a partial result rather than discarding the whole answer.
func decodeItems(raw []json.RawMessage) ([]wireHit, error) {
	hits := make([]wireHit, 0, len(raw))
	for _, item := range raw {
		var hit wireHit
		if err := json.Unmarshal(item, &hit); err != nil {
			// Keep an empty placeholder so the caller can mark the response
			// partial without losing the usable items around it.
			hits = append(hits, wireHit{})
			continue
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// normalize converts one wire item into a bounded, sourced hit.
func (p *httpProvider) normalize(item wireHit, maxSnippet int, retrievedAt time.Time) (Hit, error) {
	rawURL := strings.TrimSpace(item.URL)
	if rawURL == "" {
		return Hit{}, errors.New("missing URL")
	}
	if len(rawURL) > 2048 {
		return Hit{}, errors.New("URL exceeds 2048 characters")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return Hit{}, errors.New("invalid URL")
	}
	title, _ := knowledge.BoundText(strings.TrimSpace(item.Title), 300)
	snippet := item.Snippet
	if strings.TrimSpace(snippet) == "" {
		snippet = item.Content
	}
	if maxSnippet <= 0 || maxSnippet > DefaultSnippetChars {
		maxSnippet = DefaultSnippetChars
	}
	snippet, _ = knowledge.BoundText(strings.TrimSpace(snippet), maxSnippet)
	if title == "" {
		title = parsed.Host
	}
	return Hit{
		Title:       title,
		Source:      parsed.Host,
		URL:         rawURL,
		Snippet:     snippet,
		RetrievedAt: retrievedAt,
	}, nil
}

// braveRequest builds one Brave request. The credential travels only in the
// header; it is never part of the URL or a result.
func (p *httpProvider) braveRequest(ctx context.Context, query string, limit int, apiKey string) (*http.Request, error) {
	target := *p.endpoint
	values := target.Query()
	values.Set("q", query)
	values.Set("count", fmt.Sprintf("%d", limit))
	target.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ohgo/0.1")
	if apiKey != "" {
		req.Header.Set("X-Subscription-Token", apiKey)
	}
	return req, nil
}

// tavilyRequest builds one Tavily request.
func (p *httpProvider) tavilyRequest(ctx context.Context, query string, limit int, apiKey string) (*http.Request, error) {
	payload := map[string]any{
		"query":        query,
		"max_results":  limit,
		"search_depth": "basic",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint.String(), strings.NewReader(string(encoded)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ohgo/0.1")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return req, nil
}
