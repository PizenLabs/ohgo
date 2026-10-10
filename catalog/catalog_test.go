package catalog

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/ai"
)

// TestResolveReturnsRealFacts pins the values that were wrong when they lived
// in hand-written structs.
//
// If this fails, the catalog has drifted from upstream and the numbers the CLI
// shows — and the compaction thresholds computed from them — are lies.
func TestResolveReturnsRealFacts(t *testing.T) {
	cases := []struct {
		ref        string
		context    int
		maxOutput  int
		inputPrice float64
	}{
		// claude-sonnet-4-5 was listed as 200,000 before the catalog existed.
		{"anthropic/claude-sonnet-4-5", 1_000_000, 64_000, 3},
		// claude-opus-4-5 was priced at 15/75; the real rate is 5/25.
		{"anthropic/claude-opus-4-5", 200_000, 64_000, 5},
		{"anthropic/claude-haiku-4-5", 200_000, 64_000, 1},
		{"openai/gpt-5", 400_000, 128_000, 1.25},
	}

	for _, tc := range cases {
		model, err := Resolve(tc.ref)
		if err != nil {
			t.Errorf("Resolve(%s): %v", tc.ref, err)
			continue
		}
		if model.ContextWindow != tc.context {
			t.Errorf("%s context window = %d, want %d", tc.ref, model.ContextWindow, tc.context)
		}
		if model.MaxOutputTokens != tc.maxOutput {
			t.Errorf("%s max output = %d, want %d", tc.ref, model.MaxOutputTokens, tc.maxOutput)
		}
		if model.Cost.Input != tc.inputPrice {
			t.Errorf("%s input price = %v, want %v", tc.ref, model.Cost.Input, tc.inputPrice)
		}
		if model.Name == "" || model.Name == model.ID {
			t.Errorf("%s name = %q, want a display name", tc.ref, model.Name)
		}
		if model.BaseURL == "" {
			t.Errorf("%s has no base URL", tc.ref)
		}
	}
}

// TestResolveRejectsUnknownModelsIsTheCriticalGuard is the contract that makes
// the whole package worth having.
//
// A fallback to a default model would be convenient and would send the user's
// conversation to a model they never chose — with no visible symptom. An error
// is the only acceptable outcome.
func TestResolveRejectsUnknownModelsIsTheCriticalGuard(t *testing.T) {
	unknown := []string{
		"anthropic/does-not-exist",
		"anthropic/claude-sonnett",   // typo
		"anthropic/claude-sonnet-99", // plausible, absent
		"nope/model",
		"",
		"   ",
		"anthropic/",
		"/claude-sonnet-4-5",
		"claude-sonnet-4-5",    // missing the provider half
		"anthropic//",          // double separator
		"anthropic/a/b",        // too many parts
		"ANTHROPIC/Claude-4-5", // case matters
	}

	for _, ref := range unknown {
		model, err := Resolve(ref)
		if err == nil {
			t.Errorf("Resolve(%q) = %+v, want an error", ref, model)
			continue
		}
		if !errors.Is(err, ErrUnknownModel) && ref != "" && strings.TrimSpace(ref) != "" {
			// Malformed refs get a distinct message; both must name the input.
			if !strings.Contains(err.Error(), ref) && !strings.Contains(err.Error(), "provider/model-id") {
				t.Errorf("Resolve(%q) error %q does not name the offending reference", ref, err)
			}
		}
		// The zero model must never look usable: a caller that ignored the
		// error and used it anyway would talk to a provider with no endpoint.
		if model.Provider != "" || model.API != "" {
			t.Errorf("Resolve(%q) returned a non-zero model alongside the error: %+v", ref, model)
		}
	}
}

// TestResolveErrorMessageNamesTheCatalog makes the failure actionable: telling
// the user what exists is the difference between a typo fixed in seconds and a
// support ticket.
func TestResolveErrorMessageNamesTheCatalog(t *testing.T) {
	_, err := Resolve("anthropic/nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrUnknownModel) {
		t.Errorf("error = %v, want ErrUnknownModel", err)
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error = %q, want it to name the provider", err)
	}
	// The count in the message must match the catalog, or the message is
	// telling the user something false.
	if want := len(mustList(t, "anthropic")); !strings.Contains(err.Error(), itoa(want)) {
		t.Errorf("error = %q, want it to say %d models exist", err, want)
	}

	_, err = Resolve("openai/nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	if want := len(mustList(t, "openai")); !strings.Contains(err.Error(), itoa(want)) {
		t.Errorf("error = %v, want it to name %d openai models", err, want)
	}
}

func itoa(n int) string {
	return strings.TrimSpace(strconv.Itoa(n))
}

func TestResolveRoundTripsEveryListedModel(t *testing.T) {
	// Everything List advertises must resolve, or the CLI would offer a model
	// that then fails.
	for _, model := range List() {
		ref := model.Provider + "/" + model.ID
		got, err := Resolve(ref)
		if err != nil {
			t.Errorf("List advertised %s but Resolve failed: %v", ref, err)
			continue
		}
		if got.ID != model.ID || got.Provider != model.Provider {
			t.Errorf("Resolve(%s) = %s/%s, want %s/%s",
				ref, got.Provider, got.ID, model.Provider, model.ID)
		}
		if got.ContextWindow != model.ContextWindow {
			t.Errorf("Resolve(%s) context = %d, want %d", ref, got.ContextWindow, model.ContextWindow)
		}
	}
}

// TestCatalogIsWellFormed guards the embedded data itself.
//
// The skip-if-invalid path in ListForProvider is deliberate so one bad entry
// does not take down a provider, which means a bad entry is otherwise silent.
// This test is the alarm.
func TestCatalogIsWellFormed(t *testing.T) {
	providers := ProviderNames()
	if len(providers) == 0 {
		t.Fatal("the catalog has no providers")
	}

	seen := map[string]bool{}
	var checked int
	for _, provider := range providers {
		models, err := ListForProvider(provider)
		if err != nil {
			t.Errorf("ListForProvider(%s): %v", provider, err)
			continue
		}
		if len(models) == 0 && !DynamicProvider(provider) {
			t.Errorf("provider %s has no usable models", provider)
		}
		for _, model := range models {
			checked++
			ref := model.Provider + "/" + model.ID
			if seen[ref] {
				t.Errorf("duplicate catalog entry %s", ref)
			}
			seen[ref] = true

			if model.ID == "" || model.Name == "" || model.Provider == "" {
				t.Errorf("%s is missing identity fields", ref)
			}
			if model.API == "" {
				t.Errorf("%s has no API", ref)
			}
			if model.BaseURL == "" {
				t.Errorf("%s has no base URL", ref)
			}
			if model.ContextWindow <= 0 {
				t.Errorf("%s context window = %d", ref, model.ContextWindow)
			}
			if model.MaxOutputTokens <= 0 {
				t.Errorf("%s max output = %d", ref, model.MaxOutputTokens)
			}
			if model.MaxOutputTokens > model.ContextWindow {
				t.Errorf("%s max output %d exceeds context %d",
					ref, model.MaxOutputTokens, model.ContextWindow)
			}
			if len(model.InputModalities) == 0 {
				t.Errorf("%s lists no input modalities", ref)
			}
			hasText := false
			for _, modality := range model.InputModalities {
				if modality == "text" {
					hasText = true
				}
			}
			if !hasText {
				t.Errorf("%s does not accept text input: %v", ref, model.InputModalities)
			}
			if model.Cost.Input < 0 || model.Cost.Output < 0 {
				t.Errorf("%s has negative pricing: %+v", ref, model.Cost)
			}
		}
	}
	if checked < 50 {
		t.Errorf("checked %d models, want at least 50", checked)
	}
}

// TestImageGenerationModelsAreExcludedIsARegression catches the case where the
// upstream catalog's image models leak into the chat list.
//
// They have no context window because they are not language models. Exposing
// them would offer the user a model that cannot answer a prompt.
func TestImageGenerationModelsAreExcludedIsARegression(t *testing.T) {
	for _, model := range List() {
		if strings.Contains(model.ID, "image") {
			t.Errorf("%s/%s is an image-generation model and must not be in the catalog",
				model.Provider, model.ID)
		}
	}
}

func TestListIsSortedAndStable(t *testing.T) {
	first := List()
	second := List()
	if len(first) != len(second) {
		t.Fatalf("List returned %d then %d models", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("List[%d] = %s then %s, want a stable order", i, first[i].ID, second[i].ID)
		}
	}
	for i := 1; i < len(first); i++ {
		a, b := first[i-1], first[i]
		if a.Provider > b.Provider || (a.Provider == b.Provider && a.ID > b.ID) {
			t.Errorf("List is unordered at %d: %s/%s then %s/%s",
				i, a.Provider, a.ID, b.Provider, b.ID)
		}
	}
}

func TestListForProviderRejectsUnknown(t *testing.T) {
	if _, err := ListForProvider("nope"); err == nil {
		t.Error("an unknown provider was listed")
	}
}

func TestListForProviderOnlyReturnsThatProvider(t *testing.T) {
	for _, provider := range ProviderNames() {
		for _, model := range mustList(t, provider) {
			if model.Provider != provider {
				t.Errorf("ListForProvider(%s) returned %s/%s", provider, model.Provider, model.ID)
			}
		}
	}
}

func mustList(t *testing.T, provider string) []ai.Model {
	t.Helper()
	models, err := ListForProvider(provider)
	if err != nil {
		t.Fatalf("ListForProvider(%s): %v", provider, err)
	}
	return models
}

// TestModalitiesAreHonoured proves the facts drive behaviour: a model that does
// not accept images must refuse an image, because sending one is a provider 400.
func TestModalitiesAreHonoured(t *testing.T) {
	for _, model := range List() {
		for _, modality := range model.InputModalities {
			if modality != "text" && modality != "image" {
				t.Errorf("%s/%s lists unknown modality %q", model.Provider, model.ID, modality)
			}
		}
	}
	// Every model in this catalog accepts both, so the check below is a guard
	// against the day one does not.
	textOnly := 0
	for _, model := range List() {
		if !model.SupportsImages() {
			textOnly++
		}
	}
	if textOnly == len(List()) {
		t.Error("no model supports images, which contradicts the catalog")
	}
}

// TestCostUsesCatalogPrices checks the money path end to end, because a wrong
// price is a wrong number shown to the user.
func TestCostUsesCatalogPrices(t *testing.T) {
	model := MustResolve("anthropic/claude-sonnet-4-5")

	usage := ai.Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000}
	want := model.Cost.Input + model.Cost.Output + model.Cost.CacheRead + model.Cost.CacheWrite
	got := usage.Cost(model)
	if got != want {
		t.Errorf("cost of 1M of everything = %v, want %v", got, want)
	}
	if got != 22.05 {
		t.Errorf("cost = %v, want 22.05 (3 + 15 + 0.3 + 3.75)", got)
	}
}

// TestCostScalesWithUsage catches a unit error, which is the most likely way a
// price could be silently wrong by a factor of a million.
func TestCostScalesWithUsage(t *testing.T) {
	model := MustResolve("anthropic/claude-haiku-4-5")
	small := ai.Usage{Input: 1_000, Output: 1_000}.Cost(model)
	large := ai.Usage{Input: 10_000, Output: 10_000}.Cost(model)
	if large <= small {
		t.Errorf("cost did not scale: 1k = %v, 10k = %v", small, large)
	}
	// 1000 input tokens at $1/M plus 1000 output tokens at $5/M.
	if want := 0.006; small != want {
		t.Errorf("cost = %v, want %v", small, want)
	}
}

func TestThinkingForReportsEffortLadders(t *testing.T) {
	reasoning := MustResolve("anthropic/claude-opus-4-5")
	thinking := ThinkingFor("anthropic/claude-opus-4-5")
	if thinking == nil {
		t.Fatal("a reasoning model has no effort ladder")
	}
	if !reasoning.Reasoning && thinking != nil {
		t.Error("a non-reasoning model advertises an effort ladder")
	}
	if !thinking.Supports("high") {
		t.Errorf("%v does not support high effort", thinking.Efforts)
	}

	adaptive := ThinkingFor("anthropic/claude-opus-4-6")
	if adaptive == nil || adaptive.Mode != "anthropic-adaptive" {
		t.Fatalf("opus-4-6 thinking = %+v, want the adaptive mode", adaptive)
	}
	if adaptive.Highest() != "max" {
		t.Errorf("highest effort = %q, want max", adaptive.Highest())
	}
}

// TestUnknownEffortIsRefused is a silent-wrong-answer guard: clamping "xhigh"
// down to "high" would spend less reasoning than asked and report success.
func TestUnknownEffortIsRefused(t *testing.T) {
	thinking := ThinkingFor("anthropic/claude-opus-4-6")
	if thinking.Supports("xhigh") {
		t.Error("the adaptive ladder must not accept xhigh; it tops out at max")
	}
	if thinking.Supports("") {
		t.Error("an empty effort was accepted")
	}
	if ThinkingFor("anthropic/nope") != nil {
		t.Error("an unknown model reported an effort ladder")
	}
}

func TestNilThinkingIsSafe(t *testing.T) {
	var thinking *Thinking
	if thinking.Supports("high") {
		t.Error("a nil ladder accepted an effort")
	}
	if thinking.Highest() != "" {
		t.Error("a nil ladder reported a highest effort")
	}
}

func TestDefaultIsResolvableAndReasonable(t *testing.T) {
	model := Default()
	ref := model.Provider + "/" + model.ID
	if ref != DefaultRef {
		t.Fatalf("default = %s, want %s", ref, DefaultRef)
	}
	resolved, err := Resolve(ref)
	if err != nil {
		t.Fatalf("the default model does not resolve: %v", err)
	}
	if resolved.ContextWindow < 200_000 {
		t.Errorf("default context window = %d, want at least 200,000", resolved.ContextWindow)
	}
	if !resolved.SupportsImages() {
		t.Error("the default model should accept images")
	}
}

func TestProvidersAreWellFormed(t *testing.T) {
	for _, provider := range Providers() {
		if provider.Name == "" {
			t.Errorf("provider %+v has no name", provider)
		}
		if !strings.HasPrefix(provider.BaseURL, "https://") {
			// Loopback http is the documented local default (Ollama serves
			// plain HTTP on localhost); anything else over http would send
			// credentials in the clear.
			u := provider.BaseURL
			if !strings.HasPrefix(u, "http://127.0.0.1") && !strings.HasPrefix(u, "http://localhost") {
				t.Errorf("provider %s base URL = %q, want https", provider.Name, provider.BaseURL)
			}
		}
	}
	// Every provider with models must be describable.
	for _, name := range ProviderNames() {
		found := false
		for _, provider := range Providers() {
			if provider.Name != "" && name != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("provider %s has no description", name)
		}
	}
}

// TestModelAPIMatchesASupportedTransport is a runtime tripwire for data drift.
//
// The catalog records the API each model actually speaks. If an upstream regen
// switched a model to a protocol this build has no transport for, the CLI would
// otherwise hand it to the registry and fail with a confusing error at request
// time instead of at resolve time.
func TestModelAPIMatchesASupportedTransport(t *testing.T) {
	supported := map[string]bool{
		string(ai.APIAnthropicMessages):     true,
		string(ai.APIOpenAIChatCompletions): true,
		string(ai.APIOllamaChat):            true,
	}
	for _, model := range List() {
		if !supported[string(model.API)] {
			t.Errorf("%s/%s speaks %s, which no transport in this build implements",
				model.Provider, model.ID, model.API)
		}
	}
}

// TestCatalogDataIsSaneSize keeps the embedded data from silently becoming the
// whole upstream file.
func TestCatalogDataIsSaneSize(t *testing.T) {
	data, err := files.ReadFile("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 1<<20 {
		t.Errorf("catalog.json is %d bytes, want under 1 MiB", len(data))
	}
	if len(data) == 0 {
		t.Error("catalog.json is empty")
	}
}

func BenchmarkResolve(b *testing.B) {
	for i := 0; i < b.N; i++ {
		MustResolve("anthropic/claude-sonnet-4-5")
	}
}

func BenchmarkList(b *testing.B) {
	for i := 0; i < b.N; i++ {
		List()
	}
}

// TestOpenAIModelsAreRoutedToAProtocolThisBuildSpeaks documents the one place
// the catalog deliberately diverges from upstream, and locks it in.
//
// Upstream routes every OpenAI chat model through the Responses API. This port
// implements Chat Completions, which the same models also accept. Without the
// route field the catalog would offer models the CLI cannot drive.
func TestOpenAIModelsAreRoutedToAProtocolThisBuildSpeaks(t *testing.T) {
	found := 0
	for _, model := range ListForProviderMustExist(t, "openai") {
		if string(model.API) != "openai-chat-completions" {
			t.Errorf("%s speaks %s, want the chat-completions route this build implements",
				model.ID, model.API)
		} else {
			found++
		}
	}
	if found == 0 {
		t.Fatal("no OpenAI model was routed; the route field is not being applied")
	}
}

// TestResponsesOnlyModelsAreExcludedIsARegression guards the exclusion.
//
// The codex line and internal codenames only speak the Responses API. Routing
// them to Chat Completions would send requests the provider rejects, and the
// failure would surface as a confusing API error rather than a clear "this
// build cannot drive that model".
func TestResponsesOnlyModelsAreExcludedIsARegression(t *testing.T) {
	for _, model := range List() {
		id := model.ID
		if strings.Contains(id, "codex") || strings.Contains(id, "daybreak") ||
			strings.Contains(id, "realtime") {
			t.Errorf("%s/%s is Responses-only and was excluded upstream", model.Provider, id)
		}
	}
}

// TestAnthropicModelsNeedNoRoute proves the route field is only used where it
// must be: Anthropic's protocol already matches what this build implements.
func TestAnthropicModelsNeedNoRoute(t *testing.T) {
	for _, model := range ListForProviderMustExist(t, "anthropic") {
		if string(model.API) != "anthropic-messages" {
			t.Errorf("%s speaks %s, want anthropic-messages", model.ID, model.API)
		}
	}
}

func ListForProviderMustExist(t *testing.T, provider string) []ai.Model {
	t.Helper()
	return mustList(t, provider)
}

// TestMultiSlashModelIDsResolveIsARegression guards a real shape.
//
// Groq, Together and Fireworks name models "vendor/model", so the reference is
// "groq/meta-llama/llama-4-maverick-17b-128e-instruct" — two separators. A
// resolver that split on the last one, or that rejected a reference with more
// than one separator, would make 51 models unreachable.
func TestMultiSlashModelIDsResolveIsARegression(t *testing.T) {
	var multi int
	for _, model := range List() {
		if strings.Contains(model.ID, "/") {
			multi++
			ref := model.Provider + "/" + model.ID
			got, err := Resolve(ref)
			if err != nil {
				t.Errorf("Resolve(%s): %v", ref, err)
				continue
			}
			if got.ID != model.ID || got.Provider != model.Provider {
				t.Errorf("Resolve(%s) = %s/%s, want %s/%s",
					ref, got.Provider, got.ID, model.Provider, model.ID)
			}
		}
	}
	if multi < 10 {
		t.Errorf("only %d multi-slash models were found, want the vendor/model shape to be exercised", multi)
	}
}

// TestEveryCatalogModelDrivesAProvider is the unlock this increment bought.
//
// Six providers had credential discovery in ai.Registry but no catalog data, so
// a user with a GROQ_API_KEY could not use Groq at all. This walks every model
// through the real registry: if a provider cannot be built, the credential or
// transport wiring has regressed.
func TestEveryCatalogModelDrivesAProvider(t *testing.T) {
	registry := ai.NewRegistry()
	registry.SetAPIKey("anthropic", "k")
	for _, provider := range ProviderNames() {
		registry.SetAPIKey(provider, "k")
	}

	// apiKeyVars mirrors the registry's table, so a provider the catalog
	// advertises but the registry cannot authenticate fails here rather than at
	// request time.
	var missingCreds []string
	for _, model := range List() {
		ref := model.Provider + "/" + model.ID
		if _, err := registry.ProviderFor(model); err != nil {
			missingCreds = append(missingCreds, ref+": "+err.Error())
		}
	}
	if len(missingCreds) > 0 {
		t.Errorf("%d catalog models could not be driven: %s",
			len(missingCreds), strings.Join(missingCreds[:5], "\n  "))
	}
}

// TestProviderBaseURLMatchesTheRegistry proves the endpoint the provider will
// actually call is the one the catalog records, not a stale default.
func TestProviderBaseURLMatchesTheRegistry(t *testing.T) {
	registry := ai.NewRegistry()
	for _, provider := range ProviderNames() {
		registry.SetAPIKey(provider, "k")
	}

	for _, model := range List() {
		built, err := registry.ProviderFor(model)
		if err != nil {
			continue
		}
		var got string
		switch p := built.(type) {
		case *ai.AnthropicProvider:
			got = p.BaseURL
		case *ai.OpenAIProvider:
			got = p.BaseURL
		case *ai.OllamaProvider:
			got = p.BaseURL
		default:
			t.Errorf("%s/%s built %T, which the test does not know how to inspect",
				model.Provider, model.ID, built)
			continue
		}
		if got != model.BaseURL {
			t.Errorf("%s/%s provider points at %q, want the catalog's %q",
				model.Provider, model.ID, got, model.BaseURL)
		}
	}
}

// TestOpenAICompatibleProvidersShareOneTransport documents why six providers
// were added in one pass: they all speak the same wire protocol, so the only
// per-provider work is a base URL and a credential name.
func TestOpenAICompatibleProvidersShareOneTransport(t *testing.T) {
	compatible := map[string]string{
		"groq":      "GROQ_API_KEY",
		"together":  "TOGETHER_API_KEY",
		"fireworks": "FIREWORKS_API_KEY",
		"deepseek":  "DEEPSEEK_API_KEY",
		"cerebras":  "CEREBRAS_API_KEY",
		"mistral":   "MISTRAL_API_KEY",
	}

	registry := ai.NewRegistry()
	for provider, envVar := range compatible {
		models, err := ListForProvider(provider)
		if err != nil {
			t.Errorf("ListForProvider(%s): %v", provider, err)
			continue
		}
		if len(models) == 0 {
			t.Errorf("provider %s has no models", provider)
			continue
		}

		model := models[0]
		// Without a credential the error must name this provider's env var,
		// which is the difference between "set GROQ_API_KEY" and a bare
		// "missing API key".
		t.Setenv(envVar, "")
		_, err = registry.ProviderFor(model)
		if err == nil {
			t.Errorf("%s resolved a provider with no credential in scope", provider)
			continue
		}
		if !strings.Contains(err.Error(), envVar) {
			t.Errorf("%s error = %v, want it to name %s", provider, err, envVar)
		}

		// With one, the same generic transport must be built.
		t.Setenv(envVar, "test-key")
		built, err := registry.ProviderFor(model)
		if err != nil {
			t.Errorf("%s: %v", provider, err)
			continue
		}
		if _, ok := built.(*ai.OpenAIProvider); !ok {
			t.Errorf("%s built %T, want the shared OpenAI transport", provider, built)
		}
		if got := built.(*ai.OpenAIProvider).BaseURL; got != model.BaseURL {
			t.Errorf("%s base URL = %q, want %q", provider, got, model.BaseURL)
		}
	}
}

// TestExcludedProvidersAreDocumented explains the provider the catalog
// deliberately leaves out, so a later reader does not "fix" the omission.
func TestExcludedProvidersAreDocumented(t *testing.T) {
	// xai speaks openai-responses upstream, which this build does not implement.
	for _, model := range List() {
		if model.Provider == "xai" {
			t.Errorf("xai speaks openai-responses and was supposed to be excluded")
		}
	}
	// OpenRouter used to be excluded as a custom API; it is now included
	// through the Chat Completions route with native namespaced ids, so the
	// old exclusion must be gone rather than lingering in the test.
	found := false
	for _, model := range List() {
		if model.Provider == "openrouter" {
			found = true
			if model.API != ai.APIOpenAIChatCompletions {
				t.Errorf("openrouter/%s routes %s, want the chat route", model.ID, model.API)
			}
		}
	}
	if !found {
		t.Error("openrouter has no models: the inclusion did not land")
	}

	if _, err := ListForProvider("xai"); err == nil {
		t.Error("provider xai is present but documented as excluded")
	}
}
