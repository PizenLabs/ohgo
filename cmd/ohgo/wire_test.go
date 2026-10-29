package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// recordingServer captures each request body and replies with a fixed stream.
type recordingServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newRecordingServer(t *testing.T, stream string) *recordingServer {
	t.Helper()
	srv := &recordingServer{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		srv.mu.Lock()
		srv.bodies = append(srv.bodies, string(buf))
		srv.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *recordingServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *recordingServer) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		return ""
	}
	return s.bodies[len(s.bodies)-1]
}

// ackStream is the minimum valid Anthropic SSE conversation.
const ackStreamWire = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ack"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`

// TestWireRequestCarriesTheModelCeiling proves the budget fix survives the whole
// path to the provider.
//
// resolveMaxTokens returning the ceiling is not enough on its own: agent.Loop
// substitutes its own 8192 for a non-positive budget. If that zero ever comes
// back, the truncation bug returns with no unit test failing to catch it.
func TestWireRequestCarriesTheModelCeiling(t *testing.T) {
	server := newRecordingServer(t, ackStreamWire)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	opts := options{
		root:        t.TempDir(),
		model:       model,
		maxTokens:   resolveMaxTokens(config.Settings{}, model),
		settings:    config.Default(),
		autoApprove: true,
	}

	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if _, err := loop.Run(t.Context(), []agent.Message{agent.UserMessage{Text: "write a big file"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if server.count() != 1 {
		t.Fatalf("requests = %d, want 1", server.count())
	}
	body := server.last()
	want := `"max_tokens":` + itoaWire(model.MaxOutputTokens)
	if !strings.Contains(body, want) {
		t.Errorf("request body carries %s, want %s (the model's real ceiling)",
			extractMaxTokens(body), want)
	}
	if strings.Contains(body, `"max_tokens":8192`) {
		t.Error("the flat 8192 cap is back")
	}
}

// TestWireRequestRespectsTheUserBudget proves a deliberately small setting is
// what actually reaches the provider, rather than the model's ceiling.
func TestWireRequestRespectsTheUserBudget(t *testing.T) {
	server := newRecordingServer(t, ackStreamWire)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	opts := options{
		root:        t.TempDir(),
		model:       model,
		maxTokens:   resolveMaxTokens(config.Settings{MaxTokens: 4096}, model),
		settings:    config.Default(),
		autoApprove: true,
	}

	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if _, err := loop.Run(t.Context(), []agent.Message{agent.UserMessage{Text: "hi"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(server.last(), `"max_tokens":4096`) {
		t.Errorf("request body carries %s, want max_tokens:4096", extractMaxTokens(server.last()))
	}
}

// TestWireRequestClampsAnOversizedBudget stops an over-large setting from
// reaching the provider, which would reject it with a 400 that says nothing
// about the user's configuration.
func TestWireRequestClampsAnOversizedBudget(t *testing.T) {
	server := newRecordingServer(t, ackStreamWire)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	opts := options{
		root:        t.TempDir(),
		model:       model,
		maxTokens:   resolveMaxTokens(config.Settings{MaxTokens: 5_000_000}, model),
		settings:    config.Default(),
		autoApprove: true,
	}

	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if _, err := loop.Run(t.Context(), []agent.Message{agent.UserMessage{Text: "hi"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(server.last(), `"max_tokens":64000`) {
		t.Errorf("request body carries %s, want the clamped 64000", extractMaxTokens(server.last()))
	}
}

// TestWireRequestTargetsTheResolvedModel proves the model on the wire is the one
// the catalog resolved, so a stale hand-written list cannot reappear quietly.
func TestWireRequestTargetsTheResolvedModel(t *testing.T) {
	server := newRecordingServer(t, ackStreamWire)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)

	model := catalog.MustResolve("anthropic/claude-opus-4-6")
	opts := options{
		root:        t.TempDir(),
		model:       model,
		maxTokens:   resolveMaxTokens(config.Settings{}, model),
		settings:    config.Default(),
		autoApprove: true,
	}

	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if _, err := loop.Run(t.Context(), []agent.Message{agent.UserMessage{Text: "hi"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(server.last(), `"model":"claude-opus-4-6"`) {
		t.Errorf("request body = %s, want the model the catalog resolved", server.last())
	}
}

// extractMaxTokens pulls the max_tokens value out of a request body, purely so
// a failure message names what was actually sent.
func extractMaxTokens(body string) string {
	_, after, found := strings.Cut(body, `"max_tokens":`)
	if !found {
		return "(no max_tokens field)"
	}
	end := strings.IndexAny(after, ",}")
	if end < 0 {
		return after
	}
	return `"max_tokens":` + after[:end]
}

func itoaWire(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// openAIStream is the minimum valid Chat Completions SSE conversation.
const openAIStream = `data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ack"},"finish_reason":null}]}

data: {"id":"1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}

data: [DONE]

`

// TestWireDrivesANonOpenAIProviderOnItsOwnEndpoint is the end-to-end proof that
// six dead credential paths now work.
//
// Groq, Together, Fireworks, DeepSeek, Cerebras and Mistral all had credential
// discovery in ai.Registry but no catalog data, so a user with a GROQ_API_KEY
// could not use Groq at all. The transport is generic; only the data was
// missing. This drives a Groq model through the OpenAI transport against a
// scripted server on a non-OpenAI base URL and checks the request that arrives.
func TestWireDrivesANonOpenAIProviderOnItsOwnEndpoint(t *testing.T) {
	server := newRecordingServer(t, openAIStream)
	t.Setenv("GROQ_API_KEY", "test-key")

	registry := ai.NewRegistry()
	registry.SetBaseURL("groq", server.URL)

	model := catalog.MustResolve("groq/llama-3.3-70b-versatile")
	if model.BaseURL == "https://api.openai.com/v1" {
		t.Fatal("the catalog gave groq the OpenAI base URL")
	}

	opts := options{
		root:        t.TempDir(),
		model:       model,
		maxTokens:   resolveMaxTokens(config.Settings{}, model),
		settings:    config.Default(),
		autoApprove: true,
	}

	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if _, err := loop.Run(t.Context(), []agent.Message{agent.UserMessage{Text: "hi"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if server.count() != 1 {
		t.Fatalf("requests = %d, want 1", server.count())
	}
	body := server.last()
	if !strings.Contains(body, `"model":"llama-3.3-70b-versatile"`) {
		t.Errorf("request body = %s, want the groq model id", body)
	}
	if !strings.Contains(body, `"max_tokens":32768`) {
		t.Errorf("request body carries %s, want the model's own 32768 ceiling", extractMaxTokens(body))
	}
	if strings.Contains(body, "api.openai.com") {
		t.Error("the request leaked the OpenAI base URL")
	}
}

// TestWireSendsTheProviderCredential proves the right key is sent on the wire,
// because a shared default would silently authenticate as the wrong account.
func TestWireSendsTheProviderCredential(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, openAIStream)
	}))
	defer server.Close()

	t.Setenv("MISTRAL_API_KEY", "mistral-secret")
	registry := ai.NewRegistry()
	registry.SetBaseURL("mistral", server.URL)

	model := catalog.MustResolve("mistral/mistral-large-latest")
	opts := options{
		root:        t.TempDir(),
		model:       model,
		maxTokens:   resolveMaxTokens(config.Settings{}, model),
		settings:    config.Default(),
		autoApprove: true,
	}

	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if _, err := loop.Run(t.Context(), []agent.Message{agent.UserMessage{Text: "hi"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotAuth != "Bearer mistral-secret" {
		t.Errorf("Authorization = %q, want the MISTRAL_API_KEY credential", gotAuth)
	}
	// The override replaces the whole base URL, version prefix included, so the
	// path is the bare "/chat/completions". That is a latent trap: a user who
	// points a base URL at a local gateway rooted at /v1 would get a 404, and
	// nothing here would hint at why. Documented on Registry.SetBaseURL.
	if gotPath != "/chat/completions" {
		t.Errorf("request path = %q, want /chat/completions (the override replaces the whole base)", gotPath)
	}
}

// TestWireHonoursABaseURLRootedAtAVersion pins the half of the contract that
// makes the trap above survivable: because the entire base is replaced, a base
// URL that includes /v1 produces the full path a gateway expects.
func TestWireHonoursABaseURLRootedAtAVersion(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, openAIStream)
	}))
	defer server.Close()

	t.Setenv("GROQ_API_KEY", "test-key")
	registry := ai.NewRegistry()
	registry.SetBaseURL("groq", server.URL+"/v1")

	model := catalog.MustResolve("groq/llama-3.3-70b-versatile")
	provider, err := registry.ProviderFor(model)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(t.Context(), ai.Request{
		Model:    model,
		Messages: []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Events {
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", gotPath)
	}
}

// TestRegistryPrefersASetKeyOverTheEnvironment keeps an explicitly configured
// key from being shadowed by whatever is in the environment, which would send
// the request to the wrong account with no visible difference.
func TestRegistryPrefersASetKeyOverTheEnvironment(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, openAIStream)
	}))
	defer server.Close()

	t.Setenv("GROQ_API_KEY", "from-environment")
	registry := ai.NewRegistry()
	registry.SetBaseURL("groq", server.URL)
	registry.SetAPIKey("groq", "explicitly-set")

	model := catalog.MustResolve("groq/llama-3.3-70b-versatile")
	provider, err := registry.ProviderFor(model)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(t.Context(), ai.Request{Model: model, Messages: []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Events {
	}
	if gotAuth != "Bearer explicitly-set" {
		t.Errorf("Authorization = %q, want the explicitly set key", gotAuth)
	}
}
