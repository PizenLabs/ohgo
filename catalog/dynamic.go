package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/ai"
)

// dynamicCacheTTL bounds how long a fetched roster is trusted. A day matches
// gateway catalog churn without re-fetching on every launch; a failed refresh
// falls back to the stale file rather than failing, because yesterday's
// roster answers "what can I call" far better than an error does.
const dynamicCacheTTL = 24 * time.Hour

// openRouterDiscoveryNote: OpenRouter's /models answers the OpenAI-compatible
// list shape with rich extensions (context_length, pricing per token,
// architecture modalities, supported_parameters, reasoning ladders). Entries
// are kept only when they can actually serve a chat turn here.

// discoverOpenRouter fetches the live roster. Models without tool support
// are skipped: the harness always offers tools, and a model that cannot take
// them fails at request time with a confusing provider error instead.
func discoverOpenRouter(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]record, error) {
	entries, err := getModelEntries(ctx, client, baseURL+"/models", apiKey)
	if err != nil {
		return nil, err
	}
	var out []record
	for _, entry := range entries {
		id, _ := entry["id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		if params, ok := entry["supported_parameters"].([]any); ok && !hasString(params, "tools") {
			continue
		}
		rec := recordFromOpenRouterEntry(id, entry)
		if rec.Context > 0 && rec.MaxTokens > 0 {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// recordFromOpenRouterEntry maps one rich entry. Pricing is per token on the
// wire and per million here; a missing price is zero, not an error, because
// free models price at zero legitimately.
func recordFromOpenRouterEntry(id string, entry map[string]any) record {
	rec := record{ID: id, Provider: "openrouter", API: "openrouter", Route: "openai-chat-completions"}
	if name, _ := entry["name"].(string); name != "" {
		rec.Name = name
	} else {
		rec.Name = id
	}
	rec.Context = intPos(num(entry["context_length"]))
	top := mapOf(entry["top_provider"])
	rec.MaxTokens = intPos(num(top["max_completion_tokens"]))
	if rec.MaxTokens == 0 {
		rec.MaxTokens = rec.Context
	}
	pricing := mapOf(entry["pricing"])
	rec.Cost = costJSON{
		Input:      num(pricing["prompt"]) * 1_000_000,
		Output:     num(pricing["completion"]) * 1_000_000,
		CacheRead:  num(pricing["input_cache_read"]) * 1_000_000,
		CacheWrite: num(pricing["input_cache_write"]) * 1_000_000,
	}
	// Priced is presence, not value: free models price at zero legitimately,
	// while a missing pricing object is "unknown", never "free".
	rec.Priced = len(pricing) > 0
	arch := mapOf(entry["architecture"])
	modalities := stringsOf(arch["input_modalities"])
	if len(modalities) == 0 {
		if strings.Contains(strings.ToLower(strOf(arch["modality"])), "image") {
			modalities = []string{"text", "image"}
		} else {
			modalities = []string{"text"}
		}
	}
	rec.Input = modalities
	if reasoning, ok := entry["reasoning"].(map[string]any); ok {
		efforts := stringsOf(reasoning["supported_efforts"])
		if len(efforts) > 0 {
			rec.Reasoning = true
			rec.Thinking = &Thinking{Mode: "effort", Efforts: efforts}
		} else if mandatory(reasoning) {
			rec.Reasoning = true
		}
	}
	return rec
}

// discoverGateway fetches an OpenCode gateway roster (zen or go). Entries may
// be rich (like OpenRouter's) or minimal OpenAI ids; minimal ids merge with
// bundled facts by id, because discovery confirms existence while the bundle
// carries the numbers. A minimal id with no bundled row is skipped: without
// a context window it cannot feed compaction, and fabricating one would be
// the silent-wrong-answer class.
func discoverGateway(ctx context.Context, client *http.Client, provider, baseURL, apiKey string, bundled map[string]record) ([]record, error) {
	entries, err := getModelEntries(ctx, client, baseURL+"/models", apiKey)
	if err != nil {
		return nil, err
	}
	var out []record
	for _, entry := range entries {
		id, _ := entry["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		rich := recordFromOpenRouterEntry(id, entry)
		if rich.Context > 0 && rich.MaxTokens > 0 {
			rich.Provider = provider
			out = append(out, rich)
			continue
		}
		if ref, ok := bundled[id]; ok {
			out = append(out, ref)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// discoverOllama lists local or cloud models via /api/tags, enriching each
// with /api/show caps. Show failures fall back per id rather than failing
// the roster: one undocumented model must not hide every installed one.
func discoverOllama(ctx context.Context, client *http.Client, provider, nativeBase, apiKey string) ([]record, error) {
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := getJSON(ctx, client, nativeBase+"/api/tags", apiKey, &tags); err != nil {
		return nil, err
	}
	var out []record
	for _, entry := range tags.Models {
		id := strings.TrimSpace(entry.Model)
		if id == "" {
			id = strings.TrimSpace(entry.Name)
		}
		if id == "" {
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = id
		}
		meta := showOllamaModel(ctx, client, nativeBase, apiKey, id)
		rec := record{
			ID: id, Name: name, Provider: provider,
			API: "ollama-chat", Route: "ollama-chat",
			Context: meta.context, MaxTokens: meta.maxTokens,
			Reasoning: meta.reasoning, Input: meta.input,
		}
		if provider == "ollama-cloud" && ollamaOutputCapped(id) {
			// Pinned upstream (ollama/ollama#16890): these deployments reject
			// num_predict above 65536 with HTTP 400 despite advertising
			// wider windows. The ceiling replaces the default output cap,
			// never the discovered window. The cap travels with the record,
			// not the code.
			rec.MaxTokens = min(rec.Context, 65_536)
		}
		if rec.MaxTokens > rec.Context {
			rec.MaxTokens = rec.Context
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ollamaOutputCapped names the deployments enforcing a 65536 output ceiling
// below their advertised window, by untagged base id.
func ollamaOutputCapped(id string) bool {
	base := id
	if i := strings.Index(id, ":"); i > 0 {
		base = id[:i]
	}
	return base == "deepseek-v4-flash" || base == "deepseek-v4-pro"
}

// ollamaMeta is one model's resolved capabilities.
type ollamaMeta struct {
	context   int
	maxTokens int
	reasoning bool
	input     []string
}

// showOllamaModel resolves one model's caps via /api/show, falling back to
// the documented defaults (128k window, 8192 output) when the endpoint is
// unavailable or omits them. A nil client means the default one, matching
// getJSON: every production caller passes nil, and a reachable /api/tags
// followed by a nil dereference here is a startup panic, not a fallback.
func showOllamaModel(ctx context.Context, client *http.Client, nativeBase, apiKey, id string) ollamaMeta {
	if client == nil {
		client = &http.Client{}
	}
	fallback := ollamaMeta{context: 128_000, maxTokens: 8192, input: []string{"text"}}
	body, _ := json.Marshal(map[string]string{"model": id})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, nativeBase+"/api/show", bytes.NewReader(body))
	if err != nil {
		return fallback
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(httpReq)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return fallback
	}
	defer func() { _ = resp.Body.Close() }()
	var payload struct {
		Capabilities []string       `json:"capabilities"`
		ModelInfo    map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fallback
	}
	meta := fallback
	if window := ollamaContextWindow(payload.ModelInfo); window > 0 {
		meta.context = window
		meta.maxTokens = min(window, 8192)
	}
	for _, cap := range payload.Capabilities {
		switch cap {
		case "thinking":
			meta.reasoning = true
		case "vision":
			meta.input = []string{"text", "image"}
		}
	}
	return meta
}

// ollamaContextWindow scans model_info for a positive context-length field,
// matching upstream's key set.
func ollamaContextWindow(info map[string]any) int {
	for key, value := range info {
		if !isFloat(value) || num(value) <= 0 {
			continue
		}
		if strings.HasSuffix(key, ".context_length") || strings.HasSuffix(key, ".num_ctx") || strings.HasSuffix(key, ".context_window") {
			return int(num(value))
		}
	}
	return 0
}

// getModelEntries fetches an OpenAI-compatible models list, accepting both
// the rich extension shape and the minimal {id} shape.
func getModelEntries(ctx context.Context, client *http.Client, url, apiKey string) ([]map[string]any, error) {
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := getJSON(ctx, client, url, apiKey, &payload); err != nil {
		return nil, err
	}
	return payload.Data, nil
}

// getJSON performs an authenticated GET with a bearer key when non-empty.
func getJSON(ctx context.Context, client *http.Client, url, apiKey string, target any) error {
	if client == nil {
		client = &http.Client{}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("catalog: request %s: %w", url, err)
	}
	httpReq.Header.Set("Accept", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("catalog: get %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case 401, 403:
		return fmt.Errorf("catalog: %s rejected the credential (HTTP %d)", url, resp.StatusCode)
	case 429:
		return fmt.Errorf("catalog: %s rate-limited discovery (HTTP 429)", url)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("catalog: %s answered HTTP %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("catalog: decode %s: %w", url, err)
	}
	return nil
}

// discoverCache is the on-disk roster.
type discoverCache struct {
	FetchedAt time.Time `json:"fetched_at"`
	Models    []record  `json:"models"`
}

// cachePath locates the roster file. Tests redirect HOME, which UserHomeDir
// follows, so no extra seam is needed.
func cachePath(provider string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("catalog: locate home directory")
	}
	return filepath.Join(home, ".ohgo", "cache", provider+"-models.json"), nil
}

// readCache returns a fresh roster, or nil when missing, stale, or broken.
// A broken cache reads as absent: yesterday's bytes must never outrank
// today's discovery, and a corrupt file must not fail resolution.
func readCache(provider string) []record {
	path, err := cachePath(provider)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cached discoverCache
	if err := json.Unmarshal(raw, &cached); err != nil {
		return nil
	}
	if time.Since(cached.FetchedAt) > dynamicCacheTTL {
		return nil
	}
	return cached.Models
}

// readStaleCache returns any parseable roster regardless of age: the offline
// fallback of last resort.
func readStaleCache(provider string) []record {
	path, err := cachePath(provider)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cached discoverCache
	if err := json.Unmarshal(raw, &cached); err != nil {
		return nil
	}
	return cached.Models
}

// writeCache persists a roster best-effort: a cache failure must never fail
// the resolution it was meant to speed up.
func writeCache(provider string, models []record) {
	path, err := cachePath(provider)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	raw, err := json.Marshal(discoverCache{FetchedAt: time.Now(), Models: models})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o644)
}

// CacheStatus reports the on-disk roster for a provider: how many rows, how
// old, and whether it is within the trust window. Zero rows reads as absent.
func CacheStatus(provider string) (count int, age time.Duration, fresh bool) {
	path, err := cachePath(provider)
	if err != nil {
		return 0, 0, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	var cached discoverCache
	if err := json.Unmarshal(raw, &cached); err != nil {
		return 0, 0, false
	}
	return len(cached.Models), time.Since(cached.FetchedAt), time.Since(cached.FetchedAt) <= dynamicCacheTTL
}

// DiscoverModelsFresh bypasses a fresh cache and fetches the live roster,
// persisting it on success. On failure it returns the stale cache when one
// exists (with the fetch error), so browsing degrades to cached rows rather
// than to nothing. Callers that need to distinguish must check the error.
func DiscoverModelsFresh(ctx context.Context, client *http.Client, provider, baseURL, apiKey string) ([]ai.Model, error) {
	records, err := fetchRoster(ctx, client, provider, baseURL, apiKey)
	if err != nil {
		if stale := readStaleCache(provider); stale != nil {
			if models, convErr := toModels(provider, stale); convErr == nil {
				return models, err
			}
		}
		return nil, err
	}
	writeCache(provider, records)
	return toModels(provider, records)
}

// SnapshotEntry is one merged roster row with its provenance.
type SnapshotEntry struct {
	// Model is the validated, transport-ready model.
	Model ai.Model
	// PriceKnown reports explicit pricing metadata backs the row.
	PriceKnown bool
	// Free reports verified zero pricing. Meaningless without PriceKnown.
	Free bool
	// Origin names the freshest source behind the row: "live" (recently
	// fetched), "cached" (stale disk roster), or "bundled" (shipped data).
	Origin string
}

// freeRecord reports verified zero pricing: every known price leg is zero.
func freeRecord(rec record) bool {
	return rec.Cost.Input == 0 && rec.Cost.Output == 0 &&
		rec.Cost.CacheRead == 0 && rec.Cost.CacheWrite == 0
}

// SnapshotAll merges bundled rows with cached live rosters across every
// provider, without touching the network.
//
// Live-cached rows win over bundled ones when valid (fresher values never
// lose to shipped ones); bundled rows fill ids discovery never listed; live
// ids the bundle never saw appear as their own rows. Providers stay
// distinct: merging keys on (provider, id), never on display name, so the
// same underlying model under two providers remains two rows. The returned
// stale flag reports any dynamic provider is serving non-live data.
func SnapshotAll() (entries []SnapshotEntry, stale bool) {
	doc, err := load()
	if err != nil {
		return nil, true
	}
	type key struct {
		provider string
		id       string
	}
	merged := map[key]SnapshotEntry{}
	// Bundled first: the explicit fallback everything else overrides.
	for provider, models := range doc.Models {
		for id, rec := range models {
			k := key{provider, id}
			merged[k] = SnapshotEntry{Origin: "bundled", PriceKnown: rec.Priced, Free: rec.Priced && freeRecord(rec)}
			_ = id
		}
	}
	// Cached live rosters override bundled rows id by id.
	for _, provider := range DynamicProviders() {
		cached := readCache(provider)
		origin := "live"
		if cached == nil {
			origin = "cached"
			cached = readStaleCache(provider)
			stale = true
		}
		if cached == nil {
			// No roster at all: bundled rows (if any) stand, flagged stale
			// for dynamic providers that should discover.
			stale = true
			continue
		}
		for _, rec := range cached {
			rec.Provider = provider
			merged[key{provider, rec.ID}] = SnapshotEntry{
				Origin:     origin,
				PriceKnown: rec.Priced,
				Free:       rec.Priced && freeRecord(rec),
			}
		}
	}
	meta := doc.Providers
	for k, entry := range merged {
		var rec record
		found := false
		// Prefer the cached live record; fall back to bundled.
		rec, found = findCachedRecord(k.provider, k.id)
		if !found {
			rec, found = doc.Models[k.provider][k.id]
		}
		if !found {
			continue
		}
		if rec.BaseURL == "" {
			rec.BaseURL = meta[k.provider].BaseURL
		}
		rec.Provider = k.provider
		model, err := rec.toModel(meta[k.provider])
		if err != nil {
			continue
		}
		entry.Model = model
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Model.Provider != entries[j].Model.Provider {
			return entries[i].Model.Provider < entries[j].Model.Provider
		}
		return entries[i].Model.ID < entries[j].Model.ID
	})
	return entries, stale
}

// findCachedRecord returns the cached live record for one model, fresh or
// stale: the merge prefers any discovered values over shipped ones.
func findCachedRecord(provider, id string) (record, bool) {
	if cached := readCache(provider); cached != nil {
		for _, rec := range cached {
			if rec.ID == id {
				return rec, true
			}
		}
	}
	if cached := readStaleCache(provider); cached != nil {
		for _, rec := range cached {
			if rec.ID == id {
				return rec, true
			}
		}
	}
	return record{}, false
}

// DiscoverModels returns a provider's live roster, consulting the disk cache
// first and refreshing from the network. doc is the loaded static catalog,
// used for gateway reference merges and provider metadata.
func DiscoverModels(ctx context.Context, client *http.Client, provider, baseURL, apiKey string) ([]ai.Model, error) {
	if cached := readCache(provider); cached != nil {
		return toModels(provider, cached)
	}
	records, err := fetchRoster(ctx, client, provider, baseURL, apiKey)
	if err != nil {
		if stale := readStaleCache(provider); stale != nil {
			return toModels(provider, stale)
		}
		return nil, err
	}
	writeCache(provider, records)
	return toModels(provider, records)
}

// fetchRoster speaks the provider's discovery protocol.
func fetchRoster(ctx context.Context, client *http.Client, provider, baseURL, apiKey string) ([]record, error) {
	doc, err := load()
	if err != nil {
		return nil, err
	}
	switch provider {
	case "openrouter":
		return discoverOpenRouter(ctx, client, baseURL, apiKey)
	case "opencode-zen", "opencode-go":
		return discoverGateway(ctx, client, provider, baseURL, apiKey, doc.Models[provider])
	case "ollama":
		return discoverOllama(ctx, client, provider, ollamaNativeBase(baseURL), "")
	case "ollama-cloud":
		return discoverOllama(ctx, client, provider, ollamaCloudBase(baseURL), apiKey)
	default:
		return nil, fmt.Errorf("catalog: %s has no live discovery", provider)
	}
}

// ollamaNativeBase strips a /v1 suffix: chat lives under it, native APIs at
// the root.
func ollamaNativeBase(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1")
}

// ollamaCloudBase normalizes the cloud root the same way upstream does: an
// /api suffix is stripped, bare hosts kept.
func ollamaCloudBase(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return strings.TrimSuffix(trimmed, "/api")
}

// toModels converts records through the same validation bundled rows face:
// a discovered row that fails it is skipped, never served broken.
func toModels(provider string, records []record) ([]ai.Model, error) {
	doc, err := load()
	if err != nil {
		return nil, err
	}
	meta, ok := doc.Providers[provider]
	if !ok {
		return nil, fmt.Errorf("catalog: unknown model: no provider %q", provider)
	}
	var out []ai.Model
	for _, rec := range records {
		rec.Provider = provider
		if rec.BaseURL == "" {
			rec.BaseURL = meta.BaseURL
		}
		model, err := rec.toModel(meta)
		if err != nil {
			continue
		}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ResolveDynamic resolves a reference through live discovery. Static hits
// are the caller's job to check first: bundled rows answer offline and fast,
// while discovery answers for ids the bundle never saw.
func ResolveDynamic(ctx context.Context, client *http.Client, ref, baseURL, apiKey string) (ai.Model, error) {
	provider, id, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || provider == "" || id == "" {
		return ai.Model{}, fmt.Errorf(
			"catalog: %q is not a provider/model-id reference (e.g. openrouter/anthropic/claude-sonnet-4-5)", ref)
	}
	if !DynamicProvider(provider) {
		return ai.Model{}, fmt.Errorf("catalog: unknown model: %s (no live discovery for %q)", ref, provider)
	}
	models, err := DiscoverModels(ctx, client, provider, baseURL, apiKey)
	if err != nil {
		return ai.Model{}, err
	}
	for _, model := range models {
		if model.ID == id {
			return model, nil
		}
	}
	return ai.Model{}, fmt.Errorf("catalog: unknown model: %s (not in the live %s roster of %d models)",
		ref, provider, len(models))
}

// Small JSON helpers for discovery payloads, which mix strings and numbers.

func mapOf(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func strOf(value any) string {
	s, _ := value.(string)
	return s
}

func num(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		var f float64
		_, _ = fmt.Sscanf(strings.TrimSpace(n), "%f", &f)
		return f
	default:
		return 0
	}
}

func isFloat(value any) bool {
	switch value.(type) {
	case float64, float32, int, int64, json.Number:
		return true
	default:
		return false
	}
}

func intPos(value float64) int {
	if value <= 0 {
		return 0
	}
	return int(value)
}

func stringsOf(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func hasString(list []any, want string) bool {
	for _, item := range list {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}

func mandatory(reasoning map[string]any) bool {
	flag, _ := reasoning["mandatory"].(bool)
	return flag
}
