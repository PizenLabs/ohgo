package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ProbeTimeout bounds one connection check. Discovery is one small GET, so
// anything slower is either down or unusable — not worth a longer wait.
const ProbeTimeout = 15 * time.Second

// ProbeResult classifies a provider endpoint without spending a completion.
//
// Diagnostics must distinguish detected, configured, reachable,
// authenticated, and functional: a configured provider is not necessarily
// connected, and an HTTP 200 on a health endpoint does not prove tool calls
// work. This probe reports the first three honestly and leaves functional to
// a real request, which it says rather than implies.
type ProbeResult struct {
	// Provider and BaseURL identify the probed endpoint.
	Provider string
	BaseURL  string
	// Reachable reports the endpoint answered HTTP at all.
	Reachable bool
	// Authenticated reports discovery accepted the credential. A keyless
	// local server counts as authenticated when it answers.
	Authenticated bool
	// Models counts discovered entries, when discovery succeeded.
	Models int
	// Detail is the human summary naming the outcome and the fix.
	Detail string
}

// ProbeProvider checks an endpoint by speaking its discovery protocol: the
// models list for OpenAI-family APIs, /api/tags for Ollama. It never sends a
// completion, so probing costs nothing and proves nothing beyond what it
// claims: reachability plus credential acceptance.
func ProbeProvider(ctx context.Context, client *http.Client, api API, baseURL, apiKey string) ProbeResult {
	result := ProbeResult{BaseURL: baseURL}
	if client == nil {
		client = &http.Client{}
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		result.Detail = "no endpoint configured"
		return result
	}

	var url string
	var key string
	switch api {
	case APIOllamaChat:
		url = ollamaNativeBase(base) + "/api/tags"
		key = apiKey
	case APIAnthropicMessages:
		url = base + "/v1/models"
		key = apiKey
	default:
		url = base + "/models"
		key = apiKey
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		result.Detail = fmt.Sprintf("cannot probe %s: %v", url, err)
		return result
	}
	httpReq.Header.Set("Accept", "application/json")
	if key != "" {
		if api == APIAnthropicMessages {
			httpReq.Header.Set("x-api-key", key)
			httpReq.Header.Set("anthropic-version", AnthropicVersion)
		} else {
			httpReq.Header.Set("Authorization", "Bearer "+key)
		}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		result.Detail = probeConnDetail(base, err)
		return result
	}
	defer resp.Body.Close()
	result.Reachable = true

	switch resp.StatusCode {
	case 401, 403:
		result.Detail = "endpoint reachable but the credential was rejected (HTTP " + resp.Status + ")"
		return result
	case 429:
		result.Detail = "endpoint reachable but rate-limited (HTTP 429): retry shortly"
		return result
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Detail = fmt.Sprintf("endpoint answered HTTP %s", resp.Status)
		return result
	}

	// A 200 with unparseable JSON still proves reachability and auth; the
	// model count is the only thing lost.
	var payload struct {
		Data   []any `json:"data"`
		Models []any `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		result.Authenticated = true
		result.Detail = "endpoint reachable, credential accepted, model list unreadable"
		return result
	}
	result.Authenticated = true
	result.Models = len(payload.Data) + len(payload.Models)
	result.Detail = fmt.Sprintf("endpoint reachable, credential accepted, %d models listed", result.Models)
	return result
}

// probeConnDetail classifies a dial failure with the fix, not just the error.
func probeConnDetail(base string, err error) string {
	if strings.Contains(base, "127.0.0.1") || strings.Contains(base, "localhost") {
		return fmt.Sprintf("cannot reach %s: is the local service running? (%v)", base, scrubURLUserinfo(err.Error()))
	}
	return fmt.Sprintf("cannot reach %s: check network and endpoint (%v)", base, scrubURLUserinfo(err.Error()))
}

// scrubURLUserinfo removes userinfo from an error string, so a URL that
// embedded credentials never leaks them into diagnostics.
func scrubURLUserinfo(text string) string {
	if i := strings.Index(text, "@"); i >= 0 {
		if j := strings.LastIndex(text[:i], "://"); j >= 0 {
			return text[:j+3] + text[i+1:]
		}
	}
	return text
}
