package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// discoveryScript serves one discovery payload for resolve tests.
func discoveryScript(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestUnknownModelIsNotSilentlySubstituted is the guard for the bug class the
// catalog replaced.
//
// Before it, an unrecognised model in settings or in an env var fell back to
// the default with no message. The user would then watch their conversation
// happen against a model they never picked. Both must now fail loudly.
func TestUnknownModelIsNotSilentlySubstituted(t *testing.T) {
	cases := []struct {
		name      string
		settings  config.Settings
		flagModel string
		envModel  string
		wantInErr string
	}{
		{
			name:      "settings",
			settings:  config.Settings{Model: "anthropic/claude-sonnet-99"},
			wantInErr: "claude-sonnet-99",
		},
		{
			name:      "environment",
			settings:  config.Settings{},
			envModel:  "anthropic/nope",
			wantInErr: "nope",
		},
		{
			name:      "flag",
			settings:  config.Settings{},
			flagModel: "anthropic/nope",
			wantInErr: "nope",
		},
		{
			name:      "unknown provider",
			settings:  config.Settings{Model: "notaprovider/model"},
			wantInErr: "notaprovider",
		},
		{
			name:      "malformed",
			settings:  config.Settings{},
			flagModel: "claude-sonnet-4-5",
			wantInErr: "provider/model-id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveModel(tc.settings, tc.flagModel, tc.envModel, catalog.Resolve)
			if err == nil {
				t.Fatal("an unknown model was silently substituted")
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantInErr)
			}
		})
	}
}

// TestResolveModelErrorNamesTheSource tells the user which file to fix. An
// error that just says "unknown model" leaves them guessing between the
// settings file, the environment and the flag.
func TestResolveModelErrorNamesTheSource(t *testing.T) {
	_, err := resolveModel(config.Settings{Model: "anthropic/nope"}, "", "", catalog.Resolve)
	if err == nil || !strings.Contains(err.Error(), "settings") {
		t.Errorf("settings error = %v, want it to name the settings source", err)
	}

	_, err = resolveModel(config.Settings{}, "", "anthropic/nope", catalog.Resolve)
	if err == nil || !strings.Contains(err.Error(), "OHGO_MODEL") {
		t.Errorf("env error = %v, want it to name OHGO_MODEL", err)
	}

	_, err = resolveModel(config.Settings{}, "anthropic/nope", "", catalog.Resolve)
	if err == nil || !strings.Contains(err.Error(), "ohgo models") {
		t.Errorf("flag error = %v, want it to suggest listing the models", err)
	}
}

// TestResolveModelPrecedence locks the order: flag beats settings, settings
// beats the environment, the environment beats the default. Without this a
// user who set --model on the command line would silently get the configured
// model instead.
func TestResolveModelPrecedence(t *testing.T) {
	a := catalog.MustResolve("anthropic/claude-haiku-4-5")
	b := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	c := catalog.MustResolve("openai/gpt-5")

	settings := config.Settings{Model: "anthropic/claude-sonnet-4-5"}

	got, err := resolveModel(settings, "openai/gpt-5", "anthropic/claude-haiku-4-5", catalog.Resolve)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != c.ID {
		t.Errorf("flag lost to settings or env: got %s, want %s", got.ID, c.ID)
	}

	// With no flag, the settings model wins over the environment: a project
	// configuration is a deliberate choice, while an env var is an ambient one.
	got, err = resolveModel(settings, "", "anthropic/claude-haiku-4-5", catalog.Resolve)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != b.ID {
		t.Errorf("the env var beat the settings model: got %s, want %s", got.ID, b.ID)
	}

	// With no settings model either, the environment wins over the default.
	got, err = resolveModel(config.Settings{}, "", "anthropic/claude-haiku-4-5", catalog.Resolve)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID {
		t.Errorf("the default beat the env var: got %s, want %s", got.ID, a.ID)
	}

	got, err = resolveModel(settings, "", "", catalog.Resolve)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != b.ID {
		t.Errorf("settings did not win: got %s, want %s", got.ID, b.ID)
	}
}

// TestResolveModelDefaultsToTheCatalog proves an empty configuration still
// yields a real, complete model rather than a zero value the caller would have
// to special-case.
func TestResolveModelDefaultsToTheCatalog(t *testing.T) {
	got, err := resolveModel(config.Settings{}, "", "", catalog.Resolve)
	if err != nil {
		t.Fatal(err)
	}
	want := catalog.Default()
	if got.ID != want.ID {
		t.Fatalf("default = %s, want %s", got.ID, want.ID)
	}
	if got.Provider == "" || got.API == "" || got.BaseURL == "" {
		t.Errorf("default model is incomplete: %+v", got)
	}
	if got.ContextWindow < 200_000 {
		t.Errorf("default context window = %d, want at least 200,000", got.ContextWindow)
	}
	// The stale hand-written list had claude-sonnet-4-5 at 200,000 tokens.
	if got.ContextWindow != 1_000_000 {
		t.Errorf("default context window = %d, want the catalog's 1,000,000", got.ContextWindow)
	}
}

// TestResolveModelReturnsNoModelOnError is the other half of the contract: an
// error must not be accompanied by a usable-looking model, or a caller that
// ignored the error would proceed with a half-resolved model.
func TestResolveModelReturnsNoModelOnError(t *testing.T) {
	got, err := resolveModel(config.Settings{Model: "anthropic/nope"}, "", "", catalog.Resolve)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got.Provider != "" || got.ID != "" || got.API != "" {
		t.Errorf("error came with a non-zero model: %+v", got)
	}
}

// TestCompletionBudgetDefaultsToTheModelCeiling is a regression test for a flat
// cap that truncated real work.
//
// The budget was hardcoded to 8192 for every model. A 20,000-token file write
// was cut off mid-output and had to be redone in pieces, with nothing to
// indicate why.
func TestCompletionBudgetDefaultsToTheModelCeiling(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")
	if model.MaxOutputTokens < 8192 {
		t.Skip("the default model caps output below the old flat value")
	}

	got := resolveMaxTokens(config.Settings{}, model)
	if got != model.MaxOutputTokens {
		t.Errorf("budget = %d, want the model ceiling %d", got, model.MaxOutputTokens)
	}
}

// TestCompletionBudgetIsClampedToTheModelCeiling keeps an over-large setting
// from producing a provider rejection, which the user would see as an opaque
// 400 with no connection to their configuration.
func TestCompletionBudgetIsClampedToTheModelCeiling(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")

	got := resolveMaxTokens(config.Settings{MaxTokens: 999_999}, model)
	if got != model.MaxOutputTokens {
		t.Errorf("budget = %d, want it clamped to %d", got, model.MaxOutputTokens)
	}
}

// TestCompletionBudgetRespectsAModestSetting proves clamping does not swallow a
// deliberately small budget, which a user may set to control spend.
func TestCompletionBudgetRespectsAModestSetting(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")

	got := resolveMaxTokens(config.Settings{MaxTokens: 4096}, model)
	if got != 4096 {
		t.Errorf("budget = %d, want the user's 4096", got)
	}
}

// TestNegativeCompletionBudgetFallsBack stops a nonsensical setting from
// becoming a negative request field.
func TestNegativeCompletionBudgetFallsBack(t *testing.T) {
	model := catalog.MustResolve("anthropic/claude-sonnet-4-5")

	got := resolveMaxTokens(config.Settings{MaxTokens: -7}, model)
	if got != model.MaxOutputTokens {
		t.Errorf("budget = %d, want the model ceiling %d", got, model.MaxOutputTokens)
	}
	if got < 0 {
		t.Error("a negative budget was produced")
	}
}

// TestCompletionBudgetSurvivesAnUnknownCeiling covers a catalog entry with no
// max output: the budget must pass through rather than being clamped to zero.
func TestCompletionBudgetSurvivesAnUnknownCeiling(t *testing.T) {
	model := ai.Model{ID: "mystery", Provider: "p", MaxOutputTokens: 0}

	if got := resolveMaxTokens(config.Settings{MaxTokens: 4096}, model); got != 4096 {
		t.Errorf("budget = %d, want 4096 preserved when the model has no ceiling", got)
	}
	if got := resolveMaxTokens(config.Settings{}, model); got != 0 {
		t.Errorf("budget = %d, want 0 so the transport applies its own default", got)
	}
}

// TestBudgetTracksTheModel proves the budget is derived per run, so switching
// models mid-session does not keep the previous model's ceiling.
func TestBudgetTracksTheModel(t *testing.T) {
	// haiku-4-5 and sonnet-4-5 share a 64,000 ceiling, so they cannot show
	// this. claude-3-5-sonnet-20240620 caps at 8,192 and sonnet-4-5 at 64,000.
	small := catalog.MustResolve("anthropic/claude-3-5-sonnet-20240620")
	large := catalog.MustResolve("anthropic/claude-sonnet-4-5")

	fromSmall := resolveMaxTokens(config.Settings{}, small)
	fromLarge := resolveMaxTokens(config.Settings{}, large)
	if fromSmall == fromLarge {
		t.Errorf("both models produced a budget of %d, want per-model ceilings", fromSmall)
	}
	if fromSmall != small.MaxOutputTokens || fromLarge != large.MaxOutputTokens {
		t.Errorf("budgets = %d, %d, want %d, %d",
			fromSmall, fromLarge, small.MaxOutputTokens, large.MaxOutputTokens)
	}
}

// TestEffortLadderIsValidatedAgainstTheCatalog catches an effort the chosen
// model cannot honour. Sending one anyway means the provider ignores it, and
// the user believes they paid for reasoning they never got.
func TestEffortLadderIsValidatedAgainstTheCatalog(t *testing.T) {
	// The merged roster prefers a discovered row over a bundled one, so the
	// test must run without the machine's live cache to compare like with like.
	t.Setenv("HOME", t.TempDir())

	// opus-4-6 uses the adaptive ladder: it tops out at "max" and has no
	// "xhigh", which the older budget-based Anthropic models accept.
	ladder := catalog.ThinkingFor("anthropic/claude-opus-4-6")
	if ladder == nil {
		t.Fatal("opus-4-6 has no effort ladder")
	}
	if ladder.Supports("xhigh") {
		t.Error("the adaptive ladder wrongly accepts xhigh")
	}
	if !ladder.Supports("max") {
		t.Error("the adaptive ladder rejects max")
	}

	// A model that is not a reasoning model must not advertise a ladder.
	for _, model := range catalog.List() {
		if !model.Reasoning {
			if got := catalog.ThinkingFor(model.Provider + "/" + model.ID); got != nil {
				t.Errorf("%s is not a reasoning model but advertises efforts %v",
					model.ID, got.Efforts)
			}
		}
	}
}

// TestModelsCommandFiltersByProvider covers the usability half of a large
// catalog: 215 entries is not a listing a person can read, so the command takes
// a provider name.
func TestModelsCommandFiltersByProvider(t *testing.T) {
	out := &strings.Builder{}
	cmd := modelsCmd()
	cmd.SetOut(out)
	cmd.SetArgs([]string{"groq"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("models groq: %v", err)
	}
	body := out.String()
	lines := strings.Split(strings.TrimSpace(body), "\n")

	if len(lines) < 5 {
		t.Errorf("groq listed %d lines, want its 20 models", len(lines))
	}
	if len(lines) > 25 {
		t.Errorf("groq listed %d lines, want only groq's models", len(lines))
	}
	// Compare against the real set rather than pattern-matching the text: groq
	// serves models under an "openai/" namespace, so a substring check for
	// other providers produces false alarms.
	want, err := catalog.ListForProvider("groq")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(want) {
		t.Errorf("listed %d lines, want groq's %d models", len(lines), len(want))
	}
	for _, model := range want {
		if !strings.Contains(body, model.ID) {
			t.Errorf("groq model %q is missing from the listing", model.ID)
		}
	}
	for _, provider := range catalog.ProviderNames() {
		if provider == "groq" {
			continue
		}
		models, err := catalog.ListForProvider(provider)
		if err != nil {
			t.Fatal(err)
		}
		for _, model := range models {
			if strings.Contains(model.ID, "/") {
				continue // ambiguous across providers
			}
			if strings.Contains(body, model.ID) && !strings.Contains(anyGroqID(want), model.ID) {
				t.Errorf("%s's model %q leaked into groq's listing", provider, model.ID)
			}
		}
	}
}

// anyGroqID reports whether a model id is one groq also serves.
func anyGroqID(models []ai.Model) string {
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, "|")
}

// TestModelsCommandRejectsAnUnknownProvider keeps an unknown provider from
// looking like "this provider has no models", which is a different statement
// from "I have never heard of this provider".
func TestModelsCommandRejectsAnUnknownProvider(t *testing.T) {
	out := &strings.Builder{}
	cmd := modelsCmd()
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"nope"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("an unknown provider produced an empty listing")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v, want it to name the provider", err)
	}
	if !strings.Contains(err.Error(), "anthropic") {
		t.Errorf("error = %v, want it to list the available providers", err)
	}
}

// TestModelsCommandListEverythingWithoutAFilter proves the unfiltered path
// still works, since the filter is opt-in.
func TestModelsCommandListEverythingWithoutAFilter(t *testing.T) {
	out := &strings.Builder{}
	cmd := modelsCmd()
	cmd.SetOut(out)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("models: %v", err)
	}
	want := 0
	for _, provider := range catalog.ProviderNames() {
		models, err := catalog.ListForProvider(provider)
		if err != nil {
			t.Fatal(err)
		}
		want += len(models)
	}
	if got := len(strings.Split(strings.TrimSpace(out.String()), "\n")); got != want {
		t.Errorf("listed %d models, want %d", got, want)
	}
}

// TestResolveModelFallsBackToDiscovery proves an id the bundle never saw
// still resolves when the provider's live roster lists it. Static misses for
// static providers never touch the network.
func TestResolveModelFallsBackToDiscovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := discoveryScript(t, `{"data":[{"id":"future/model","context_length":32000,
		"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["tools"]}]}`)

	registry := ai.NewRegistry()
	registry.SetBaseURL("openrouter", server.URL)
	registry.SetAPIKey("openrouter", "k")

	got, err := resolveModel(config.Settings{}, "openrouter/future/model", "",
		dynamicResolver(context.Background(), registry))
	if err != nil {
		t.Fatalf("resolveModel: %v", err)
	}
	if got.ID != "future/model" || got.Provider != "openrouter" {
		t.Errorf("model = %s/%s, want the discovered id", got.Provider, got.ID)
	}
}

// TestResolveModelKeepsStaticErrorsOffline proves a typo against a static
// provider fails without any network: discovery is never consulted where it
// cannot help.
func TestResolveModelKeepsStaticErrorsOffline(t *testing.T) {
	_, err := resolveModel(config.Settings{}, "groq/no-such-model", "",
		dynamicResolver(context.Background(), ai.NewRegistry()))
	if err == nil || !strings.Contains(err.Error(), "no-such-model") {
		t.Errorf("err = %v, want it to name the unknown id", err)
	}
}
