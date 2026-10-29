package catalog

import (
	"context"
	"net/http"
	"testing"
)

// TestBundledReasoningIsAuthoritativeWithAModelSpecificLadder proves the two
// facts the control plane depends on: a bundled row's reasoning flag is a
// determination in both directions, and a thinking ladder is model-specific
// rather than a single global list.
func TestBundledReasoningIsAuthoritativeWithAModelSpecificLadder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	entries, _ := SnapshotAll()
	byID := map[string]SnapshotEntry{}
	for _, entry := range entries {
		byID[entry.Model.Provider+"/"+entry.Model.ID] = entry
	}

	ladder, ok := byID["anthropic/claude-haiku-4-5"]
	if !ok {
		t.Fatal("anthropic/claude-haiku-4-5 missing from the bundled snapshot")
	}
	if ladder.Thinking == nil || len(ladder.Thinking.Efforts) == 0 {
		t.Fatalf("haiku thinking = %+v, want an effort ladder", ladder.Thinking)
	}
	if !ladder.ReasoningKnown || !ladder.Model.Reasoning {
		t.Errorf("haiku reasoning known=%v value=%v, want an authoritative supported flag",
			ladder.ReasoningKnown, ladder.Model.Reasoning)
	}
	if !ladder.Thinking.Supports("minimal") {
		t.Errorf("haiku ladder = %v, want the model's own levels", ladder.Thinking.Efforts)
	}

	// A different model exposes a different ladder: the levels are data, not a
	// universal constant.
	other, ok := byID["anthropic/claude-fable-5"]
	if !ok {
		t.Fatal("anthropic/claude-fable-5 missing from the bundled snapshot")
	}
	if other.Thinking != nil && other.Thinking.Supports("minimal") {
		t.Errorf("fable ladder = %v, want a different ladder from haiku", other.Thinking.Efforts)
	}

	// A curated non-reasoning model is unsupported, authoritatively: not
	// unknown.
	plain, ok := byID["anthropic/claude-3-5-sonnet-20240620"]
	if !ok {
		t.Fatal("anthropic/claude-3-5-sonnet-20240620 missing from the bundled snapshot")
	}
	if !plain.ReasoningKnown {
		t.Error("a bundled non-reasoning row reports reasoning as unknown, want a determination")
	}
	if plain.Model.Reasoning {
		t.Error("a bundled non-reasoning row reports support")
	}
}

// TestOpenRouterReasoningWithoutALadderRecordsTheExplicitDisable proves the
// discovered capability needed by a lightweight role: a model that reasons by
// default and publishes no ladder still has a provider-native "none" value.
func TestOpenRouterReasoningWithoutALadderRecordsTheExplicitDisable(t *testing.T) {
	entry := map[string]any{
		"context_length":       float64(100000),
		"top_provider":         map[string]any{"max_completion_tokens": float64(4096)},
		"pricing":              map[string]any{"prompt": "0", "completion": "0"},
		"architecture":         map[string]any{"input_modalities": []any{"text"}},
		"supported_parameters": []any{"tools", "reasoning"},
		"reasoning":            map[string]any{"mandatory": false},
	}
	rec := recordFromOpenRouterEntry("vendor/default-reasoner", "openrouter", entry)
	if !rec.Reasoning {
		t.Fatal("a model that advertises the reasoning parameter was recorded as non-reasoning")
	}
	if rec.Thinking == nil || rec.Thinking.Disable != "none" {
		t.Fatalf("thinking = %+v, want the explicit none disable", rec.Thinking)
	}
	model, err := rec.toModel(Provider{})
	if err != nil {
		t.Fatalf("toModel: %v", err)
	}
	if model.ReasoningDisable != "none" {
		t.Errorf("model disable = %q, want the capability carried on ai.Model", model.ReasoningDisable)
	}

	// A mandatory reasoner must not claim it can be switched off.
	entry["reasoning"] = map[string]any{"mandatory": true}
	mandatory := recordFromOpenRouterEntry("vendor/always-reasons", "openrouter", entry)
	if mandatory.Thinking == nil || !mandatory.Thinking.Mandatory || mandatory.Thinking.Disable != "" {
		t.Errorf("mandatory thinking = %+v, want mandatory with no disable", mandatory.Thinking)
	}

	// Gateways do not necessarily accept OpenRouter's "none" value.
	entry["reasoning"] = map[string]any{"mandatory": false}
	gateway := recordFromOpenRouterEntry("vendor/default-reasoner", "opencode-zen", entry)
	if gateway.Thinking != nil && gateway.Thinking.Disable != "" {
		t.Errorf("gateway thinking = %+v, want no foreign disable value", gateway.Thinking)
	}
}

// TestDiscoveredReasoningAbsenceIsUnknownNotUnsupported is the unknown-vs-
// unsupported contract for discovered rows: a live roster that says nothing
// about reasoning must not be presented as "does not support thinking".
func TestDiscoveredReasoningAbsenceIsUnknownNotUnsupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := servePayload(t, `{"data":[
		{"id":"vendor/with-ladder","name":"With Ladder","context_length":100000,
		 "top_provider":{"max_completion_tokens":4096},
		 "pricing":{"prompt":"0.000001","completion":"0.000002"},
		 "supported_parameters":["tools"],
		 "reasoning":{"supported_efforts":["low","high"]}},
		{"id":"vendor/silent","name":"Silent","context_length":100000,
		 "top_provider":{"max_completion_tokens":4096},
		 "pricing":{"prompt":"0.000001","completion":"0.000002"},
		 "supported_parameters":["tools"]}]}`, http.StatusOK, nil)
	defer server.Close()
	if _, err := DiscoverModelsFresh(context.Background(), server.Client(), "openrouter", server.URL, "k"); err != nil {
		t.Fatalf("DiscoverModelsFresh: %v", err)
	}

	entries, _ := SnapshotAll()
	byID := map[string]SnapshotEntry{}
	for _, entry := range entries {
		byID[entry.Model.Provider+"/"+entry.Model.ID] = entry
	}

	known, ok := byID["openrouter/vendor/with-ladder"]
	if !ok {
		t.Fatal("the laddered discovered row is missing")
	}
	if !known.ReasoningKnown || known.Thinking == nil || !known.Thinking.Supports("high") {
		t.Errorf("with-ladder = %+v, want known support and a ladder", known)
	}

	silent, ok := byID["openrouter/vendor/silent"]
	if !ok {
		t.Fatal("the silent discovered row is missing")
	}
	if silent.ReasoningKnown {
		t.Error("a discovered row that said nothing about reasoning reported it as known")
	}
	if silent.Model.Reasoning {
		t.Error("a discovered row that said nothing about reasoning claimed support")
	}
	if silent.Thinking != nil {
		t.Errorf("silent thinking = %+v, want no ladder", silent.Thinking)
	}
}
