// Package catalog resolves model facts from bundled data.
//
// The original puts every model- and provider-conditional policy in a KDL rule
// tree compiled to rules.json, and forbids TypeScript code from deriving any of
// it by string matching on a model id. This package is the Go equivalent: the
// facts live in catalog.json, are resolved here, and no call site may re-derive
// them.
//
// # The accuracy problem this fixes
//
// Before this package, the models the CLI offered were hand-written structs.
// They had drifted: claude-sonnet-4-5 was listed with a 200,000-token context
// window when the real one is 1,000,000, and claude-opus-4-1 was priced at
// $15/$75 when it is $5/$25. A wrong context window makes compaction fire at
// the wrong time, and a wrong price makes the cost display a lie. Both are
// silent wrong answers, which is the worst class of bug in a coding agent.
//
// Adding a model is now a catalog.json edit, and a wrong fact there is caught
// by TestCatalogIsWellFormed rather than by a user noticing a wrong number.
//
// # Dependency direction
//
// ai owns the Model type and the transports; this package owns the data and
// produces ai.Model values. Nothing in ai imports catalog, so the arrow points
// exactly where the original's does.
package catalog

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/PizenLabs/ohgo/ai"
)

//go:embed catalog.json
var files embed.FS

// ErrUnknownModel is returned for a reference not present in the catalog.
//
// It is a distinct error rather than a fallback to a default: silently
// substituting a model the user did not ask for would send their conversation
// to a different provider than they configured.
var ErrUnknownModel = errors.New("catalog: unknown model")

// Provider is one upstream endpoint family.
type Provider struct {
	// Name is the human-readable provider name.
	Name string
	// BaseURL is the default endpoint, used when a model has no override.
	BaseURL string
}

// ProviderBaseURL returns a provider's default endpoint, if known.
func ProviderBaseURL(name string) (string, bool) {
	doc, err := load()
	if err != nil {
		return "", false
	}
	meta, ok := doc.Providers[name]
	if !ok || meta.BaseURL == "" {
		return "", false
	}
	return meta.BaseURL, true
}

// Describe returns the display name for a provider key, if known.
func Describe(name string) (string, bool) {
	doc, err := load()
	if err != nil {
		return "", false
	}
	meta, ok := doc.Providers[name]
	if !ok || meta.Name == "" {
		return "", false
	}
	return meta.Name, true
}

// Thinking describes how a model accepts an effort level.
//
// Mode is the wire shape: "budget" is a token budget, "effort" is a named
// level, and "anthropic-adaptive" lets the model pick. Efforts lists the levels
// the model accepts, in ascending order of cost.
type Thinking struct {
	Mode    string
	Efforts []string
}

// Supports reports whether an effort level is accepted by this model.
//
// An unknown level is refused rather than clamped: silently downgrading
// "xhigh" to "high" would spend less reasoning than the caller asked for and
// return an answer they believe cost more.
func (t *Thinking) Supports(effort string) bool {
	if t == nil {
		return false
	}
	for _, candidate := range t.Efforts {
		if candidate == effort {
			return true
		}
	}
	return false
}

// Highest returns the most expensive effort the model accepts.
func (t *Thinking) Highest() string {
	if t == nil || len(t.Efforts) == 0 {
		return ""
	}
	return t.Efforts[len(t.Efforts)-1]
}

// record is the on-disk shape of one model.
type record struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	API      string `json:"api"`
	Provider string `json:"provider"`
	// Route is the wire protocol this build uses, when it differs from API.
	//
	// It exists because accuracy matters in both directions: the catalog must
	// not claim a model speaks a protocol it does not, and must not pretend a
	// model is unusable when a working protocol exists. Every OpenAI chat model
	// upstream is routed via the Responses API; this port implements Chat
	// Completions, which those models also accept, so Route records that.
	// Models that genuinely only speak Responses — the codex line and internal
	// codenames — are excluded from the catalog rather than routed to a
	// protocol they do not speak.
	Route     string    `json:"route"`
	BaseURL   string    `json:"baseUrl"`
	Context   int       `json:"contextWindow"`
	MaxTokens int       `json:"maxTokens"`
	Reasoning bool      `json:"reasoning"`
	Input     []string  `json:"input"`
	Cost      costJSON  `json:"cost"`
	Thinking  *Thinking `json:"thinking"`
	// Priced reports explicit pricing metadata backs Cost. Bundled rows are
	// curated, so load() marks them priced; discovered rows set it when the
	// source supplied a pricing object. A zero Cost with Priced=false is
	// "price unknown", never "free".
	Priced bool `json:"priced,omitempty"`
}

// api returns the wire protocol this build uses for the record.
func (r record) api() string {
	if r.Route != "" {
		return r.Route
	}
	return r.API
}

// costJSON is the per-million-token price schedule in USD.
type costJSON struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// document is the on-disk catalog.
type document struct {
	Providers map[string]Provider          `json:"providers"`
	Models    map[string]map[string]record `json:"models"`
}

var (
	loadOnce sync.Once
	loaded   document
	loadErr  error
)

// load reads and parses the embedded catalog exactly once.
func load() (document, error) {
	loadOnce.Do(func() {
		data, err := files.ReadFile("catalog.json")
		if err != nil {
			loadErr = fmt.Errorf("catalog: read embedded catalog: %w", err)
			return
		}
		var doc document
		if err := json.Unmarshal(data, &doc); err != nil {
			loadErr = fmt.Errorf("catalog: parse embedded catalog: %w", err)
			return
		}
		if len(doc.Models) == 0 {
			loadErr = errors.New("catalog: embedded catalog has no models")
			return
		}
		for provider, models := range doc.Models {
			if len(models) == 0 {
				loadErr = fmt.Errorf("catalog: provider %q has no models", provider)
				return
			}
		}
		loaded = doc
		// Bundled rows are curated facts: their pricing (including zero
		// for free models) is explicit metadata, not a missing value.
		for _, models := range loaded.Models {
			for id, rec := range models {
				rec.Priced = true
				models[id] = rec
			}
		}
	})
	return loaded, loadErr
}

// Resolve turns a "provider/model-id" reference into a model.
//
// The error names what was wrong. There is deliberately no fallback: if the
// reference is unusable the caller must decide what to do, because substituting
// a model would send the conversation somewhere the user never chose.
func Resolve(ref string) (ai.Model, error) {
	doc, err := load()
	if err != nil {
		return ai.Model{}, err
	}

	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ai.Model{}, errors.New("catalog: empty model reference, want provider/model-id")
	}
	provider, id, ok := strings.Cut(ref, "/")
	if !ok || provider == "" || id == "" {
		return ai.Model{}, fmt.Errorf(
			"catalog: %q is not a provider/model-id reference (e.g. anthropic/claude-sonnet-4-5)", ref)
	}

	models, ok := doc.Models[provider]
	if !ok {
		return ai.Model{}, fmt.Errorf("%w: no provider %q (have %s)",
			ErrUnknownModel, provider, strings.Join(ProviderNames(), ", "))
	}
	rec, ok := models[id]
	if !ok {
		return ai.Model{}, fmt.Errorf("%w: %s (have %d %s models)",
			ErrUnknownModel, id, len(models), provider)
	}
	return rec.toModel(doc.Providers[provider])
}

// MustResolve resolves a reference or panics.
//
// It exists for tests and for package-level defaults, where a bad reference is
// a programming error rather than a runtime condition. It is never used on a
// path a user's input can reach.
func MustResolve(ref string) ai.Model {
	model, err := Resolve(ref)
	if err != nil {
		panic(err)
	}
	return model
}

// toModel converts a catalog record into the transport-facing model.
func (r record) toModel(provider Provider) (ai.Model, error) {
	if r.ID == "" || r.Provider == "" {
		return ai.Model{}, fmt.Errorf("catalog: model %q has no id or provider", r.Name)
	}
	if r.Context <= 0 {
		return ai.Model{}, fmt.Errorf("catalog: %s/%s has no context window", r.Provider, r.ID)
	}
	if r.MaxTokens <= 0 {
		return ai.Model{}, fmt.Errorf("catalog: %s/%s has no max output", r.Provider, r.ID)
	}
	if r.MaxTokens > r.Context {
		// A model that can emit more than it can accept would be unusable, and
		// the request would be rejected by the provider.
		return ai.Model{}, fmt.Errorf("catalog: %s/%s max output %d exceeds context %d",
			r.Provider, r.ID, r.MaxTokens, r.Context)
	}
	if len(r.Input) == 0 {
		return ai.Model{}, fmt.Errorf("catalog: %s/%s lists no input modalities", r.Provider, r.ID)
	}

	baseURL := r.BaseURL
	if baseURL == "" {
		baseURL = provider.BaseURL
	}

	return ai.Model{
		ID:              r.ID,
		Provider:        r.Provider,
		Name:            r.Name,
		API:             ai.API(r.api()),
		BaseURL:         baseURL,
		ContextWindow:   r.Context,
		MaxOutputTokens: r.MaxTokens,
		Cost: ai.Cost{
			Input:      r.Cost.Input,
			Output:     r.Cost.Output,
			CacheRead:  r.Cost.CacheRead,
			CacheWrite: r.Cost.CacheWrite,
		},
		Reasoning:       r.Reasoning,
		InputModalities: append([]string(nil), r.Input...),
	}, nil
}

// List returns every model in the catalog, sorted by provider then id.
func List() []ai.Model {
	all, err := listAll()
	if err != nil {
		return nil
	}
	return all
}

func listAll() ([]ai.Model, error) {
	doc, err := load()
	if err != nil {
		return nil, err
	}
	out := make([]ai.Model, 0, len(doc.Models)*8)
	for provider := range doc.Models {
		models, err := ListForProvider(provider)
		if err != nil {
			return nil, err
		}
		out = append(out, models...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// DynamicProviders names providers whose models come from live discovery
// rather than (or in addition to) bundled rows. A dynamic provider with an
// empty bundled set is legitimate: local Ollama's models exist only on the
// user's machine, so there is nothing true to bundle.
func DynamicProviders() []string {
	return []string{"ollama", "ollama-cloud", "openrouter", "opencode-zen", "opencode-go"}
}

// DynamicProvider reports whether a provider resolves models through live
// discovery.
func DynamicProvider(provider string) bool {
	for _, name := range DynamicProviders() {
		if name == provider {
			return true
		}
	}
	return false
}

// ListForProvider returns one provider's bundled models, sorted by id.
//
// A model that fails validation is skipped rather than returned broken: one bad
// entry should not make the whole provider unreachable. TestCatalogIsWellFormed
// catches any entry that lands in that bucket.
//
// A known provider with no bundled rows returns an empty list, not an error:
// dynamic providers (local Ollama above all) have nothing true to bundle,
// and discovery is their listing.
func ListForProvider(provider string) ([]ai.Model, error) {
	doc, err := load()
	if err != nil {
		return nil, err
	}
	records, ok := doc.Models[provider]
	if !ok {
		if _, known := doc.Providers[provider]; known {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: no provider %q", ErrUnknownModel, provider)
	}
	meta := doc.Providers[provider]

	out := make([]ai.Model, 0, len(records))
	for _, rec := range records {
		model, err := rec.toModel(meta)
		if err != nil {
			continue
		}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ProviderNames lists the providers in the catalog, sorted.
func ProviderNames() []string {
	doc, err := load()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(doc.Providers))
	for name := range doc.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Providers returns the catalog's providers, sorted by key.
func Providers() []Provider {
	doc, err := load()
	if err != nil {
		return nil
	}
	names := ProviderNames()
	out := make([]Provider, 0, len(names))
	for _, name := range names {
		out = append(out, doc.Providers[name])
	}
	return out
}

// ThinkingFor returns the effort configuration for a reference, or nil.
//
// It consults the merged roster — discovered rows override bundled ones, id
// by id — so it reports the same ladder SnapshotAll exposes to the model
// picker. The assignment seam and the runtime resolver both read this, which
// is what keeps them from disagreeing about what a dynamic model exposes.
func ThinkingFor(ref string) *Thinking {
	provider, id, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || provider == "" || id == "" {
		return nil
	}
	if rec, found := findCachedRecord(provider, id); found {
		return rec.Thinking
	}
	doc, err := load()
	if err != nil {
		return nil
	}
	if rec, ok := doc.Models[provider][id]; ok {
		return rec.Thinking
	}
	return nil
}

// Default returns the model used when the user names none.
func Default() ai.Model {
	return MustResolve(DefaultRef)
}

// DefaultRef is the reference served by Default.
const DefaultRef = "anthropic/claude-sonnet-4-5"
