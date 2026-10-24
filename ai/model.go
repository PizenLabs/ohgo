package ai

// Cost is the per-million-token price schedule for one model, in USD.
//
// A zero value means "price unknown" and yields a zero computed cost rather
// than a division or a fabricated number.
type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Total returns the dollar cost of the given usage under this schedule.
func (c Cost) Total(u Usage) float64 {
	const m = 1_000_000.0
	return float64(u.Input)*c.Input/m +
		float64(u.Output)*c.Output/m +
		float64(u.CacheRead)*c.CacheRead/m +
		float64(u.CacheWrite)*c.CacheWrite/m
}

// API identifies the wire protocol a model speaks.
//
// The agent loop never branches on this; only providers do. A model whose API
// has no registered provider fails at resolve time with ErrNoProvider rather
// than at call time.
type API string

// Supported wire protocols.
const (
	// APIAnthropicMessages is the Anthropic Messages API.
	APIAnthropicMessages API = "anthropic-messages"
	// APIOpenAIChatCompletions is the OpenAI Chat Completions API.
	APIOpenAIChatCompletions API = "openai-chat-completions"
	// APIOpenAIResponses is the OpenAI Responses API.
	APIOpenAIResponses API = "openai-responses"
)

// Model is one addressable model on one provider.
//
// Capability fields are policy, not identity: they are resolved from the
// catalog's rule tree and must never be re-derived by string matching on the
// model id at a call site.
type Model struct {
	// ID is the provider-scoped model identifier, e.g. "claude-sonnet-4-5".
	ID string
	// Name is the human-readable display name, e.g. "Claude Sonnet 4.5".
	Name string
	// Provider is the provider key, e.g. "anthropic".
	Provider string
	// API selects the wire protocol.
	API API
	// BaseURL overrides the provider default when non-empty.
	BaseURL string
	// ContextWindow is the maximum total tokens the model accepts.
	ContextWindow int
	// MaxOutputTokens is the largest completion the model will emit.
	MaxOutputTokens int
	// Cost is the price schedule.
	Cost Cost
	// Reasoning reports whether the model accepts a thinking budget.
	Reasoning bool
	// ReasoningEfforts is the model's effort ladder, lowest first. Empty means
	// the provider publishes no ladder, not that reasoning is unsupported.
	ReasoningEfforts []string
	// ReasoningDisable is the provider-native effort value that explicitly
	// turns reasoning off. Empty means the provider exposes no such value, so
	// omitting the effort is the only disable.
	ReasoningDisable string
	// ReasoningMandatory reports a model that reasons whether or not an effort
	// is sent. A caller must not claim it disabled reasoning for this model.
	ReasoningMandatory bool
	// InputModalities lists the content kinds the model accepts, e.g. "text", "image".
	InputModalities []string
}

// SupportsImages reports whether the model accepts image content blocks.
func (m Model) SupportsImages() bool {
	for _, modality := range m.InputModalities {
		if modality == "image" {
			return true
		}
	}
	return false
}

// Usage is the token accounting for one completed turn.
type Usage struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	// Reasoning is the subset of Output the provider reports as hidden
	// reasoning tokens. It stays zero when the provider does not distinguish
	// them, rather than being inferred from Output.
	Reasoning int
	// Total is Input + Output + CacheRead, excluding cache-write tokens which
	// are billed separately by most providers.
	Total int
}

// Cost returns the dollar cost of this usage under the model's price schedule.
func (u Usage) Cost(m Model) float64 {
	return m.Cost.Total(u)
}
