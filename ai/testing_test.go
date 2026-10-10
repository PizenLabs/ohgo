package ai

// Test fixtures for models.
//
// These live in ai rather than in the catalog package because catalog imports
// ai: a test in ai that wanted catalog's data would create an import cycle. The
// values are the real ones for claude-sonnet-4-5 as of the catalog's data, but
// they are asserted by TestResolveReturnsRealFacts in the catalog package, so a
// drift there is caught where the data actually lives. Duplicating the numbers
// in two test files would mean updating both when the price changes, which is
// how the original hand-written list drifted in the first place.

// testModel returns a model with a realistic shape for transport tests.
//
// The specifics matter only where a test asserts on them: a 200,000-token
// window and a modest output cap are enough to exercise truncation and window
// logic without pretending to be a real deployment.
func testModel() Model {
	return Model{
		ID:       "claude-sonnet-4-5",
		Provider: "anthropic",
		API:      APIAnthropicMessages,
		// A base URL is deliberately present: it mimics the catalog default and
		// is what lets the registry tests prove that an override beats it.
		BaseURL:         "https://api.anthropic.com",
		ContextWindow:   200_000,
		MaxOutputTokens: 64_000,
		Cost:            Cost{Input: 3, Output: 15, CacheRead: 0.30, CacheWrite: 3.75},
		Reasoning:       true,
		InputModalities: []string{"text", "image"},
	}
}

// testOpenAIModel returns an OpenAI-shaped model for the chat-completions
// transport tests.
func testOpenAIModel() Model {
	return Model{
		ID:              "gpt-5",
		Provider:        "openai",
		API:             APIOpenAIChatCompletions,
		ContextWindow:   400_000,
		MaxOutputTokens: 128_000,
		Cost:            Cost{Input: 1.25, Output: 10, CacheRead: 0.125},
		Reasoning:       true,
		InputModalities: []string{"text", "image"},
	}
}
