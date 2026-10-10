package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Model-picker catalog sync: OpenRouter decoding, provider-qualified ids,
// merging, freshness, and failure fallback.

// openRouterPayload serves a realistic /models document: string pricing (as
// the real API returns), provider-qualified ids, reasoning ladders,
// modality metadata, and rows that must be skipped.
const openRouterPayload = `{"data":[
	{"id":"openai/gpt-4o","name":"GPT-4o","context_length":128000,
	 "top_provider":{"max_completion_tokens":16384},
	 "pricing":{"prompt":"0.0000025","completion":"0.00001"},
	 "architecture":{"input_modalities":["text","image"]},
	 "supported_parameters":["tools","temperature"]},
	{"id":"deepseek/deepseek-v4-free","name":"DeepSeek V4","context_length":128000,
	 "pricing":{"prompt":"0","completion":"0"},
	 "architecture":{"modality":"text->text"},
	 "supported_parameters":["tools"]},
	{"id":"unpriced/model","context_length":64000,
	 "architecture":{"modality":"text->text"},
	 "supported_parameters":["tools"]},
	{"id":"no-tools/model","name":"No Tools","context_length":64000,
	 "pricing":{"prompt":"0.000001","completion":"0.000002"},
	 "supported_parameters":["temperature"]},
	{"id":"","name":"Empty","context_length":100},
	{"id":"no-context/model","name":"No Context",
	 "pricing":{"prompt":"0","completion":"0"},
	 "supported_parameters":["tools"]}
]}`

// servePayload starts a server answering every /models GET with payload,
// recording whether an Authorization header arrived.
func servePayload(t *testing.T, payload string, status int, sawAuth *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sawAuth != nil && r.Header.Get("Authorization") != "" {
			*sawAuth = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, payload)
	}))
}

func TestOpenRouterDecodesRichEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := servePayload(t, openRouterPayload, http.StatusOK, nil)
	defer server.Close()

	models, err := DiscoverModelsFresh(context.Background(), server.Client(), "openrouter", server.URL, "k")
	if err != nil {
		t.Fatalf("DiscoverModelsFresh: %v", err)
	}
	byID := map[string]bool{}
	for _, m := range models {
		byID[m.ID] = true
	}
	// Provider-qualified ids survive exactly, slashes included.
	if !byID["openai/gpt-4o"] || !byID["deepseek/deepseek-v4-free"] || !byID["unpriced/model"] {
		t.Errorf("ids = %v, want the qualified ids preserved exactly", byID)
	}
	// Tool-less, id-less, and context-less rows are skipped, not served broken.
	if byID["no-tools/model"] || byID[""] || byID["no-context/model"] {
		t.Errorf("ids = %v, want unusable rows dropped", byID)
	}
	for _, m := range models {
		if m.ID == "openai/gpt-4o" {
			if m.Name != "GPT-4o" {
				t.Errorf("name = %q, want the display name kept apart from the id", m.Name)
			}
			if m.ContextWindow != 128000 || m.MaxOutputTokens != 16384 {
				t.Errorf("window = %d/%d, want 128000/16384", m.ContextWindow, m.MaxOutputTokens)
			}
			if m.Cost.Input != 2.5 || m.Cost.Output != 10 {
				t.Errorf("cost = %+v, want string pricing scaled to millions", m.Cost)
			}
			if !m.SupportsImages() {
				t.Error("gpt-4o must report image input from architecture modalities")
			}
		}
	}
}

func TestOpenRouterMissingPricingIsUnknownNotFree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := servePayload(t, openRouterPayload, http.StatusOK, nil)
	defer server.Close()

	if _, err := DiscoverModelsFresh(context.Background(), server.Client(), "openrouter", server.URL, "k"); err != nil {
		t.Fatalf("DiscoverModelsFresh: %v", err)
	}
	entries, _ := SnapshotAll()
	for _, entry := range entries {
		if entry.Model.Provider != "openrouter" || entry.Model.ID != "unpriced/model" {
			continue
		}
		if entry.PriceKnown {
			t.Error("a row without a pricing object claims known prices")
		}
		if entry.Free {
			t.Error("a row without pricing claims verified free")
		}
		return
	}
	t.Error("unpriced/model missing from the snapshot")
}

func TestOpenRouterFreeModelsFlagged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := servePayload(t, openRouterPayload, http.StatusOK, nil)
	defer server.Close()

	if _, err := DiscoverModelsFresh(context.Background(), server.Client(), "openrouter", server.URL, "k"); err != nil {
		t.Fatalf("DiscoverModelsFresh: %v", err)
	}
	entries, _ := SnapshotAll()
	for _, entry := range entries {
		if entry.Model.Provider == "openrouter" && entry.Model.ID == "deepseek/deepseek-v4-free" {
			if !entry.PriceKnown || !entry.Free {
				t.Errorf("verified zero-price row = known:%v free:%v, want both true", entry.PriceKnown, entry.Free)
			}
			return
		}
	}
	t.Error("the free row is missing from the snapshot")
}

func TestOpenRouterRejectsMalformedAndHTTPFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		payload string
		status  int
		want    string
	}{
		{"malformed", `{"data":[`, http.StatusOK, "decode"},
		{"rate limited", `{}`, 429, "rate-limited"},
		{"rejected credential", `{}`, 401, "rejected the credential"},
		{"server error", `{}`, 500, "HTTP 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := servePayload(t, tc.payload, tc.status, nil)
			defer server.Close()
			_, err := DiscoverModelsFresh(ctx, server.Client(), "openrouter", server.URL, "k")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOpenRouterRefreshRespectsTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := DiscoverModelsFresh(ctx, server.Client(), "openrouter", server.URL, "k"); err == nil {
		t.Error("a hung endpoint succeeded: the timeout did not fire")
	}
}

func TestOpenRouterPublicDiscoveryNeedsNoKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var sawAuth bool
	server := servePayload(t, openRouterPayload, http.StatusOK, &sawAuth)
	defer server.Close()

	// Browsing passes no credential; inference still authenticates.
	models, err := DiscoverModelsFresh(context.Background(), server.Client(), "openrouter", server.URL, "")
	if err != nil || len(models) == 0 {
		t.Fatalf("unauthenticated discovery = %v, %v: public metadata must not require a key", len(models), err)
	}
	if sawAuth {
		t.Error("an Authorization header was sent with an empty key")
	}
}

func TestSnapshotMergesLiveOverBundled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Seed the live cache with a context that differs from the bundle.
	server := servePayload(t, `{"data":[{"id":"longcat-2.5-preview-free","name":"Longcat Live",
		"context_length":999000,"pricing":{"prompt":"0","completion":"0"},
		"supported_parameters":["tools"]}]}`, http.StatusOK, nil)
	defer server.Close()
	if _, err := DiscoverModelsFresh(context.Background(), server.Client(), "opencode-go", server.URL, "k"); err != nil {
		t.Fatalf("DiscoverModelsFresh: %v", err)
	}
	entries, stale := SnapshotAll()
	// Other providers still serve bundled rows, so the merged roster as a
	// whole is stale: overall liveness requires every shown row fresh.
	if !stale {
		t.Error("bundled fallback rows alongside one live fetch report fully live")
	}
	origins := map[string]string{}
	for _, entry := range entries {
		if entry.Model.Provider == "opencode-go" {
			origins[entry.Model.ID] = entry.Origin
			if entry.Model.ID == "longcat-2.5-preview-free" && entry.Model.ContextWindow != 999000 {
				t.Errorf("window = %d, want the live value to win over bundled", entry.Model.ContextWindow)
			}
		}
	}
	if origins["longcat-2.5-preview-free"] != "live" {
		t.Errorf("origins = %v, want the refreshed row marked live", origins)
	}
}

func TestSnapshotKeepsProvidersDistinct(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	entries, _ := SnapshotAll()
	seen := map[string]int{}
	for _, entry := range entries {
		seen[entry.Model.Provider+"/"+entry.Model.ID]++
	}
	for ref, count := range seen {
		if count > 1 {
			t.Errorf("%s appears %d times: providers must never merge by name", ref, count)
		}
	}
	// The same underlying model under two gateways stays two rows.
	zen, goRows := 0, 0
	for _, entry := range entries {
		switch entry.Model.Provider {
		case "opencode-zen":
			zen++
		case "opencode-go":
			goRows++
		}
	}
	if zen == 0 || goRows == 0 {
		t.Errorf("zen = %d go = %d, want both bundled gateways present as fallbacks", zen, goRows)
	}
}

func TestSnapshotFallsBackToStaleCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	server := servePayload(t, openRouterPayload, http.StatusOK, nil)
	if _, err := DiscoverModelsFresh(context.Background(), server.Client(), "openrouter", server.URL, "k"); err != nil {
		t.Fatalf("DiscoverModelsFresh: %v", err)
	}
	server.Close()
	// Age the cache past the TTL, then kill the network.
	cacheFile := filepath.Join(home, ".ohgo", "cache", "openrouter-models.json")
	raw, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatalf("no cache file: %v", err)
	}
	aged := `{"fetched_at":"2000-01-01T00:00:00Z","models":` + extractModels(raw) + `}`
	if err := os.WriteFile(cacheFile, []byte(aged), 0o644); err != nil {
		t.Fatal(err)
	}
	dead := "http://127.0.0.1:9"
	if _, err := DiscoverModelsFresh(context.Background(), nil, "openrouter", dead, "k"); err == nil {
		t.Error("a dead network with only stale data reported success")
	}
	entries, stale := SnapshotAll()
	if !stale {
		t.Error("an aged cache reports fresh")
	}
	found := false
	for _, entry := range entries {
		if entry.Model.Provider == "openrouter" && entry.Origin == "cached" {
			found = true
		}
	}
	if !found {
		t.Error("no cached openrouter rows survived the outage")
	}
}

func TestSnapshotShowsNewModelsWithoutRebuild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := servePayload(t, `{"data":[{"id":"future/model-v9","name":"Future V9",
		"context_length":200000,"pricing":{"prompt":"0","completion":"0"},
		"supported_parameters":["tools"]}]}`, http.StatusOK, nil)
	defer server.Close()
	if _, err := DiscoverModelsFresh(context.Background(), server.Client(), "openrouter", server.URL, "k"); err != nil {
		t.Fatalf("DiscoverModelsFresh: %v", err)
	}
	entries, _ := SnapshotAll()
	for _, entry := range entries {
		if entry.Model.Provider == "openrouter" && entry.Model.ID == "future/model-v9" {
			return
		}
	}
	t.Error("a newly listed id is invisible: discovery must surface post-release models")
}

func TestCacheStatusFreshness(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if count, _, fresh := CacheStatus("openrouter"); fresh || count != 0 {
		t.Errorf("empty cache = %d rows fresh=%v, want absent", count, fresh)
	}
	server := servePayload(t, openRouterPayload, http.StatusOK, nil)
	defer server.Close()
	if _, err := DiscoverModelsFresh(context.Background(), server.Client(), "openrouter", server.URL, "k"); err != nil {
		t.Fatalf("DiscoverModelsFresh: %v", err)
	}
	count, _, fresh := CacheStatus("openrouter")
	if !fresh || count == 0 {
		t.Errorf("fresh cache = %d rows fresh=%v, want rows and fresh", count, fresh)
	}
}

func TestCorruptCacheReadsAsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ohgo", "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openrouter-models.json"), []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, fresh := CacheStatus("openrouter"); fresh {
		t.Error("a corrupt cache reads as fresh")
	}
	// Bundled rows still answer: corruption degrades to the bundle.
	entries, _ := SnapshotAll()
	if len(entries) == 0 {
		t.Error("a corrupt cache emptied the whole snapshot")
	}
}

// TestOllamaDiscoveryWithNilClient is the /model panic regression: every
// production caller passes a nil HTTP client, and getJSON already treats
// that as the default client. When /api/tags is reachable, discovery used
// to proceed into showOllamaModel and dereference the nil client, killing
// the whole program inside the Bubble Tea command goroutine (surfaced only
// as "program was killed: program experienced a panic").
func TestOllamaDiscoveryWithNilClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_, _ = fmt.Fprint(w, `{"models":[{"name":"llama3.1:8b","model":"llama3.1:8b"}]}`)
		case "/api/show":
			_, _ = fmt.Fprint(w, `{"capabilities":["vision","thinking"],
				"model_info":{"llama.context_length":64000}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	models, err := DiscoverModelsFresh(context.Background(), nil, "ollama", server.URL, "")
	if err != nil {
		t.Fatalf("DiscoverModelsFresh with nil client: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %d, want the tagged model, not a panic", len(models))
	}
	got := models[0]
	if got.ID != "llama3.1:8b" || got.ContextWindow != 64000 {
		t.Errorf("model = %+v, want the shown caps, not the fallback", got)
	}
	if !got.SupportsImages() || !got.Reasoning {
		t.Errorf("model = %+v, want vision and thinking from /api/show", got)
	}
}

// TestOllamaShowFailureWithNilClient covers the adjacent edge: a reachable
// tags endpoint whose /api/show fails must fall back per model — without a
// client and without failing the roster.
func TestOllamaShowFailureWithNilClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			_, _ = fmt.Fprint(w, `{"models":[{"name":"m","model":"m"}]}`)
			return
		}
		http.Error(w, "broken", http.StatusInternalServerError)
	}))
	defer server.Close()

	models, err := DiscoverModelsFresh(context.Background(), nil, "ollama", server.URL, "")
	if err != nil {
		t.Fatalf("DiscoverModelsFresh with nil client: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 128_000 {
		t.Errorf("models = %+v, want the documented fallback window", models)
	}
}

// TestOllamaTagsDownWithNilClient covers the daemon-down edge: /api/tags
// unreachable with no cache is an error, never a panic and never rows.
func TestOllamaTagsDownWithNilClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	models, err := DiscoverModelsFresh(context.Background(), nil, "ollama", "http://127.0.0.1:9", "")
	if err == nil {
		t.Fatalf("models = %v, want the refused dial surfaced", models)
	}
	if len(models) != 0 {
		t.Errorf("models = %v, want no rows without tags", models)
	}
}
