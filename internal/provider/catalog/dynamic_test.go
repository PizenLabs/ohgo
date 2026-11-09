package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// Dynamic discovery is the only source for some providers, so these tests
// prove the mapping against scripted servers rather than the live internet.

// TestDiscoverOpenRouterMapsRichEntries proves the full mapping: native ids,
// per-million pricing, tool filtering, and the thinking ladder.
func TestDiscoverOpenRouterMapsRichEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want the discovery endpoint", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth = %q, want the bearer key", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[
			{"id":"anthropic/claude-sonnet-4-5","name":"Sonnet","context_length":1000000,
			 "pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003","input_cache_write":"0.00000375"},
			 "architecture":{"input_modalities":["text","image"]},
			 "top_provider":{"max_completion_tokens":64000},
			 "supported_parameters":["tools","tool_choice","reasoning"],
			 "reasoning":{"supported_efforts":["low","medium","high"]}},
			{"id":"text-only/model","context_length":8000,
			 "pricing":{"prompt":"0","completion":"0"},
			 "supported_parameters":["temperature"]},
			{"id":"","context_length":100}
		]}`)
	}))
	defer server.Close()

	models, err := DiscoverModels(context.Background(), server.Client(), "openrouter", server.URL, "k")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1: the tool-less and empty ids must be skipped", len(models))
	}
	model := models[0]
	if model.ID != "anthropic/claude-sonnet-4-5" || model.Provider != "openrouter" {
		t.Errorf("model = %s/%s, want the native namespaced id", model.Provider, model.ID)
	}
	if model.API != ai.APIOpenAIChatCompletions {
		t.Errorf("api = %q, want the chat route, never the pseudo-API", model.API)
	}
	if model.ContextWindow != 1000000 || model.MaxOutputTokens != 64000 {
		t.Errorf("window = %d/%d, want 1000000/64000", model.ContextWindow, model.MaxOutputTokens)
	}
	if model.Cost.Input != 3 || model.Cost.Output != 15 {
		t.Errorf("cost = %+v, want per-token prices scaled to millions", model.Cost)
	}
	if len(model.InputModalities) != 2 {
		t.Errorf("modalities = %v, want text+image", model.InputModalities)
	}
}

// TestDiscoverGatewayMergesMinimalEntries proves minimal OpenAI ids merge
// with bundled facts, while unknown minimal ids are skipped rather than
// fabricated: without a context window there is nothing true to serve.
func TestDiscoverGatewayMergesMinimalEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"deepseek-v4-pro"},{"id":"brand-new-model"}]}`)
	}))
	defer server.Close()

	models, err := DiscoverModels(context.Background(), server.Client(), "opencode-go", server.URL, "k")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	foundBundled, foundNew := false, false
	for _, model := range models {
		if model.ID == "deepseek-v4-pro" {
			foundBundled = true
			if model.ContextWindow != 1000000 {
				t.Errorf("bundled merge lost the facts: %+v", model)
			}
		}
		if model.ID == "brand-new-model" {
			foundNew = true
		}
	}
	if !foundBundled {
		t.Error("the bundled id vanished from discovery")
	}
	if foundNew {
		t.Error("a minimal id with no bundled facts was fabricated into a model")
	}
}

// TestDiscoverOllamaEnrichesViaShow proves tags plus per-model caps: the
// tags list carries no windows, so /api/show supplies them best-effort.
func TestDiscoverOllamaEnrichesViaShow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/tags" && r.Method == "GET":
			if auth := r.Header.Get("Authorization"); auth != "" {
				t.Errorf("auth = %q, want no header for local discovery", auth)
			}
			_, _ = fmt.Fprint(w, `{"models":[{"name":"llama3.1:latest","model":"llama3.1:latest"},{"name":"qwen","model":"qwen"}]}`)
		case r.URL.Path == "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Model == "llama3.1:latest" {
				_, _ = fmt.Fprint(w, `{"capabilities":["completion","vision"],"model_info":{"llama.context_length":8192}}`)
			} else {
				w.WriteHeader(404)
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	models, err := DiscoverModels(context.Background(), server.Client(), "ollama", server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %d, want both tags", len(models))
	}
	var llama, qwen *ai.Model
	for i, model := range models {
		switch model.ID {
		case "llama3.1:latest":
			llama = &models[i]
		case "qwen":
			qwen = &models[i]
		}
	}
	if llama == nil || qwen == nil {
		t.Fatalf("models = %v, want both ids", models)
	}
	if llama.ContextWindow != 8192 || len(llama.InputModalities) != 2 {
		t.Errorf("llama = %+v, want the shown caps", llama)
	}
	if llama.API != ai.APIOllamaChat {
		t.Errorf("api = %q, want the native protocol", llama.API)
	}
	if qwen.ContextWindow != 128000 || qwen.MaxOutputTokens != 8192 {
		t.Errorf("qwen = %+v, want the documented fallbacks on show failure", qwen)
	}
	if qwen.MaxOutputTokens > qwen.ContextWindow {
		t.Errorf("qwen output %d exceeds context %d: validation would refuse it", qwen.MaxOutputTokens, qwen.ContextWindow)
	}
}

// TestDiscoverOllamaCloudCapsKnownIds pins the upstream output ceiling for
// the two DeepSeek deployments that reject wider num_predict with HTTP 400.
func TestDiscoverOllamaCloudCapsKnownIds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			if r.Header.Get("Authorization") != "Bearer k" {
				t.Errorf("auth missing the cloud key")
			}
			_, _ = fmt.Fprint(w, `{"models":[{"name":"deepseek-v4-pro","model":"deepseek-v4-pro"}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"capabilities":["completion"],"model_info":{"x.context_length":1000000}}`)
	}))
	defer server.Close()

	models, err := DiscoverModels(context.Background(), server.Client(), "ollama-cloud", server.URL, "k")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	if len(models) != 1 || models[0].MaxOutputTokens != 65_536 {
		t.Errorf("models = %+v, want the 65536 ceiling", models)
	}
}

// TestDiscoverCachesAndFallsBack proves the offline story: a fresh roster
// caches to disk, and a dead network serves the stale file rather than an
// error.
func TestDiscoverCachesAndFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"m/one","context_length":1000,
			"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}]}`)
	}))
	defer server.Close()

	first, err := DiscoverModels(context.Background(), server.Client(), "openrouter", server.URL, "k")
	if err != nil || len(first) != 1 {
		t.Fatalf("first = %v, %v", first, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ohgo", "cache", "openrouter-models.json")); err != nil {
		t.Errorf("no cache file written: %v", err)
	}

	// A second call within the TTL must not hit the network again.
	second, err := DiscoverModels(context.Background(), server.Client(), "openrouter", server.URL, "k")
	if err != nil || len(second) != 1 || hits != 1 {
		t.Errorf("second = %v, hits = %d, want the cache served", second, hits)
	}

	// A dead network with a stale cache still resolves: yesterday's roster
	// answers better than an error. Age the file past the TTL first.
	cacheFile := filepath.Join(home, ".ohgo", "cache", "openrouter-models.json")
	raw, _ := os.ReadFile(cacheFile)
	aged := `{"fetched_at":"2000-01-01T00:00:00Z","models":` + extractModels(raw) + `}`
	if err := os.WriteFile(cacheFile, []byte(aged), 0o644); err != nil {
		t.Fatal(err)
	}
	dead := "http://127.0.0.1:9"
	third, err := DiscoverModels(context.Background(), server.Client(), "openrouter", dead, "k")
	if err != nil || len(third) != 1 {
		t.Errorf("offline = %v, %v: want the stale roster, not an error", third, err)
	}
}

func extractModels(raw []byte) string {
	var cached struct {
		Models any `json:"models"`
	}
	_ = json.Unmarshal(raw, &cached)
	out, _ := json.Marshal(cached.Models)
	return string(out)
}

// TestResolveDynamicFindsDiscoveredIds proves end-to-end resolution of an id
// the bundle never saw, and a clear error for one discovery never listed.
func TestResolveDynamicFindsDiscoveredIds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"new/model","context_length":5000,
			"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}]}`)
	}))
	defer server.Close()

	model, err := ResolveDynamic(context.Background(), server.Client(), "openrouter/new/model", server.URL, "k")
	if err != nil {
		t.Fatalf("ResolveDynamic: %v", err)
	}
	if model.ID != "new/model" || model.Provider != "openrouter" {
		t.Errorf("model = %s/%s, want the discovered id", model.Provider, model.ID)
	}

	_, err = ResolveDynamic(context.Background(), server.Client(), "openrouter/nope/nothing", server.URL, "k")
	if err == nil || !strings.Contains(err.Error(), "nope/nothing") {
		t.Errorf("err = %v, want it to name the missing id", err)
	}
}

// TestResolveDynamicRejectsStaticProviders keeps the two resolvers apart: a
// bundled provider resolves through the bundle, never the network.
func TestResolveDynamicRejectsStaticProviders(t *testing.T) {
	_, err := ResolveDynamic(context.Background(), nil, "groq/llama-3.1-8b-instant", "", "")
	if err == nil {
		t.Error("a static provider resolved through discovery")
	}
}

func TestDynamicProviderSetCoversDiscovery(t *testing.T) {
	for _, provider := range []string{"ollama", "ollama-cloud", "openrouter", "opencode-zen", "opencode-go"} {
		if !DynamicProvider(provider) {
			t.Errorf("%s is not marked dynamic", provider)
		}
	}
	if DynamicProvider("groq") {
		t.Error("a static provider claims discovery")
	}
}

// TestGatewayDiscoveryReadsCurrentConsoleModels is the regression for the
// live OpenCode model shape: windows live under limit, prices under
// cost.text, modalities under modalities, and protocol under api. The previous
// parser only knew OpenRouter's fields, so every live row fell back to stale
// bundled facts or vanished.
func TestGatewayDiscoveryReadsCurrentConsoleModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[
			{"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash",
			 "limit":{"context":1000000,"output":384000},
			 "modalities":{"input":["text","image/jpeg"]},
			 "capabilities":{"reasoning":{"supported":true,"effort":["low","high","max"]}},
			 "cost":{"text":{"input":{"price":0.3,"cache":{"read":0.006}},"output":{"price":1.2}}},
			 "api":{"chat-completions":{"base_url":"https://opencode.ai/inference/openai/v1"}}},
			{"id":"claude-sonnet-4-5","name":"Claude Sonnet 4.5",
			 "limit":{"context":1000000,"output":64000},
			 "modalities":{"input":["text","image/png","application/pdf"]},
			 "capabilities":{"reasoning":{"supported":true}},
			 "cost":{"text":{"input":{"price":3,"cache":{"read":0.3,"write":3.75}},"output":{"price":15}}},
			 "api":{"messages":{"base_url":"https://opencode.ai/inference/anthropic/v1"}}},
			{"id":"big-pickle","name":"Big Pickle",
			 "limit":{"context":200000,"output":32000},
			 "modalities":{"input":["text"]},
			 "capabilities":{"reasoning":{"supported":true}},
			 "cost":{"text":{"input":{"price":0},"output":{"price":0}}},
			 "api":{"chat-completions":{"base_url":"https://opencode.ai/inference/openai/v1"}}},
			{"id":"responses-only","name":"Responses Only",
			 "limit":{"context":128000,"output":128000},
			 "modalities":{"input":["text"]},
			 "cost":{"text":{"input":{"price":1},"output":{"price":1}}},
			 "api":{"responses":{"base_url":"https://opencode.ai/inference/openai/v1"}}}
		]}`)
	}))
	defer server.Close()

	models, err := DiscoverModels(context.Background(), server.Client(), "opencode-zen", server.URL, "k")
	if err != nil {
		t.Fatalf("DiscoverModels: %v", err)
	}
	byID := map[string]ai.Model{}
	for _, model := range models {
		byID[model.ID] = model
	}
	if _, ok := byID["responses-only"]; ok {
		t.Error("a Responses-only model was mis-routed instead of excluded")
	}
	if _, ok := byID["big-pickle"]; ok {
		t.Error("a free-tier Zen model was offered even though only the OpenCode client can call it")
	}
	deepseek, ok := byID["deepseek-v4.1-flash"]
	if !ok {
		t.Fatal("the paid chat model is missing")
	}
	if deepseek.API != ai.APIOpenAIChatCompletions {
		t.Errorf("api = %q, want chat completions", deepseek.API)
	}
	if deepseek.ContextWindow != 1000000 || deepseek.MaxOutputTokens != 384000 {
		t.Errorf("window = %d/%d, want 1000000/384000", deepseek.ContextWindow, deepseek.MaxOutputTokens)
	}
	if deepseek.Cost.Input != 0.3 || deepseek.Cost.Output != 1.2 || deepseek.Cost.CacheRead != 0.006 {
		t.Errorf("cost = %+v, want Console's per-million prices", deepseek.Cost)
	}
	if !deepseek.SupportsImages() {
		t.Error("image/jpeg input was lost in modality normalization")
	}
	if deepseek.SessionHeader != "x-opencode-session" {
		t.Errorf("session header = %q, want the gateway's routing header", deepseek.SessionHeader)
	}
	claude, ok := byID["claude-sonnet-4-5"]
	if !ok {
		t.Fatal("the paid Messages model is missing")
	}
	if claude.API != ai.APIAnthropicMessages {
		t.Errorf("api = %q, want native Messages", claude.API)
	}
	if claude.BaseURL != "https://opencode.ai/inference/anthropic" {
		t.Errorf("base URL = %q, want the transport root without a duplicated /v1", claude.BaseURL)
	}
}

// TestBundledOpenCodeZenOmitsClientRestrictedModels guards the shipped
// fallback, which is what a first run sees before discovery: free Zen rows are
// unreachable outside OpenCode, and retired ids would produce a 400 instead of
// a model choice.
func TestBundledOpenCodeZenOmitsClientRestrictedModels(t *testing.T) {
	models := mustList(t, "opencode-zen")
	if len(models) == 0 {
		t.Fatal("the bundled Zen roster is empty")
	}
	for _, model := range models {
		if model.Cost.Input == 0 && model.Cost.Output == 0 && model.Cost.CacheRead == 0 && model.Cost.CacheWrite == 0 {
			t.Errorf("%s is a free-tier Zen model, which only the OpenCode client can call", model.ID)
		}
		if model.ID == "deepseek-v4-flash" || model.ID == "big-pickle" {
			t.Errorf("%s is retired or client-restricted and must not be offered", model.ID)
		}
	}
}

// TestBundledOpenCodeGoKeepsItsFreeModels is the counter-case: Go serves free
// models to outside harnesses, so filtering by price alone would remove a
// working choice.
func TestBundledOpenCodeGoKeepsItsFreeModels(t *testing.T) {
	models := mustList(t, "opencode-go")
	for _, model := range models {
		if model.ID == "beam-free" || model.ID == "longcat-2.5-free" {
			return
		}
	}
	t.Error("Go free models disappeared from the bundled roster")
}

// TestBundledOpenCodeGatewaysUseTheConsoleEndpoints pins the advertised
// endpoints and session header. The old /zen paths still redirect for some
// models, which is exactly why a silent drift would only fail at inference
// time.
func TestBundledOpenCodeGatewaysUseTheConsoleEndpoints(t *testing.T) {
	cases := []struct {
		provider string
		baseURL  string
	}{
		{"opencode-zen", "https://opencode.ai/inference/openai/v1"},
		{"opencode-go", "https://opencode.ai/inference/go/openai/v1"},
	}
	for _, tc := range cases {
		baseURL, ok := ProviderBaseURL(tc.provider)
		if !ok || baseURL != tc.baseURL {
			t.Errorf("%s base URL = %q, %v; want %q", tc.provider, baseURL, ok, tc.baseURL)
		}
		models := mustList(t, tc.provider)
		for _, model := range models {
			if model.SessionHeader != "x-opencode-session" {
				t.Errorf("%s/%s session header = %q, want x-opencode-session",
					tc.provider, model.ID, model.SessionHeader)
				break
			}
		}
	}
}
