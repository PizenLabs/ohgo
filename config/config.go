// Package config loads and saves ohgo settings.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/state"
)

// ErrNotFound means no configuration file exists at the expected location.
var ErrNotFound = errors.New("config: not found")

// Role names a model profile: which model, and optionally which thinking
// effort, serves a particular purpose.
//
// ASK, FUZZ and BUILD correspond to the three execution authorities
// (workspace.Workspace); SUP and COMMIT are model roles with no authority of
// their own. The role vocabulary lives here because a role is a configuration
// concept — "which model serves this purpose" — not a runtime boundary.
type Role string

// The five roles. The first three mirror the workspaces; the last two are
// deliberately not workspaces (a role must never be mistaken for an authority).
const (
	// RoleAsk is the read-only reasoning role.
	RoleAsk Role = "ask"
	// RoleFuzz is the exploratory role.
	RoleFuzz Role = "fuzz"
	// RoleBuild is the mutation-authority role.
	RoleBuild Role = "build"
	// RoleSup is the ephemeral support role (not a workspace).
	RoleSup Role = "sup"
	// RoleCommit is the commit-authoring role (not a workspace).
	RoleCommit Role = "commit"
)

// RoleNames returns every role in canonical display order.
func RoleNames() []Role { return []Role{RoleAsk, RoleFuzz, RoleBuild, RoleSup, RoleCommit} }

// NormalizeRole lowercases and trims a role name for lookup, so "BUILD" and
// " build " address the same profile.
func NormalizeRole(name string) Role {
	return Role(strings.ToLower(strings.TrimSpace(name)))
}

// ValidRole reports whether name is a known role.
func ValidRole(name Role) bool {
	switch name {
	case RoleAsk, RoleFuzz, RoleBuild, RoleSup, RoleCommit:
		return true
	}
	return false
}

// Profile is one role's durable model selection.
//
// A profile is deliberately a value, not a pointer: an absent role is the zero
// Profile and falls through to the next precedence layer, which is the same
// meaning as an empty field.
type Profile struct {
	// Model is the "provider/id" reference bound to the role. Empty means the
	// role inherits the next layer (legacy workspace override, then global).
	Model string `json:"model,omitempty"`
	// Effort is the thinking effort level for the role. Empty means the
	// global ReasoningEffort applies. It is only meaningful when the bound
	// model actually exposes that effort; it is stored verbatim and validated
	// against catalog metadata at the seam that owns that knowledge.
	Effort string `json:"effort,omitempty"`
}

// UserDefaults is the machine-global human-defaults layer.
//
// It is not a project worker identity: a worker belongs to a project, and the
// authoritative value lives in `<project>/.ohgo/project.yml`. Its Name is a
// convenience default the initialization gate may offer when it asks for the
// project worker name; the confirmed value is persisted project-locally.
// Nothing in the initialization flow writes this field. It is never an
// authorization credential and is never sent to providers.
type UserDefaults struct {
	// Name is the machine-wide default display name. Empty means unset.
	Name string `json:"name,omitempty"`
}

// Settings is the user-facing configuration.
//
// Every field is optional: a zero Settings is a valid configuration that uses
// the built-in defaults.
type Settings struct {
	// UserDefaults holds machine-wide human defaults. The JSON key stays
	// "identity" so settings written by an earlier build remain readable; the
	// value is only ever a suggested default, never a project's worker
	// identity.
	UserDefaults UserDefaults `json:"identity,omitempty"`
	// Model is the default model as "provider/id", e.g. "anthropic/claude-sonnet-4-5".
	Model string `json:"model,omitempty"`
	// Models maps a workspace name ("ask", "fuzz", "build") to a
	// "provider/id" override for that authority. A workspace with no entry
	// falls back to Model, so configuring one workspace does not require
	// configuring all three (§11, §12, I7).
	//
	// It predates Roles and remains the legacy read path: a file that only
	// sets Models still resolves exactly as before. New writes go to Roles.
	Models map[string]string `json:"models,omitempty"`
	// Roles maps a role name to its model profile. It is the model-control
	// plane's durable store and the canonical place interactive /model
	// changes land. A role with no entry falls back to Models (legacy
	// workspace overrides), then Model.
	Roles map[string]Profile `json:"roles,omitempty"`
	// MaxTokens caps each completion.
	MaxTokens int `json:"maxTokens,omitempty"`
	// Reasoning enables extended thinking by default.
	Reasoning bool `json:"reasoning,omitempty"`
	// ReasoningEffort is the default effort level.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// Approvals maps a tool name to its approval policy.
	Approvals map[string]string `json:"approvals,omitempty"`
	// AutoApprove lists the tools that never prompt.
	AutoApprove []string `json:"autoApprove,omitempty"`
	// SystemPrompt is appended to the built-in system prompt.
	SystemPrompt string `json:"systemPrompt,omitempty"`
	// Theme selects the UI palette: "dark" (default) or "light". Any other
	// value behaves as dark, so a typo degrades to the default rather than
	// an error.
	Theme string `json:"theme,omitempty"`
	// Mouse selects how the full-screen UI receives the wheel.
	//
	// "select" (the default) never requests mouse reporting, so the terminal
	// keeps text selection and drag-copy; the wheel reaches the UI through
	// alternate scroll mode, which terminals that do not implement it — and
	// multiplexers such as tmux, which consume the wheel for their own
	// copy-mode — never provide.
	//
	// "report" asks the terminal for real mouse events, so the wheel works
	// everywhere including inside tmux, at the cost of the terminal's own
	// selection: drag-select needs a modifier (Shift, or Option on macOS).
	//
	// Any other value behaves as "select", so a typo keeps copy working.
	Mouse string `json:"mouse,omitempty"`
	// Search configures external search providers. Absence disables search;
	// presence with no providers is a configuration error surfaced by the
	// tool builder rather than a silent no-op.
	Search *SearchSettings `json:"search,omitempty"`
	// MCP configures external MCP servers. Absence disables the MCP tools.
	MCP *MCPSettings `json:"mcp,omitempty"`
	// Browser configures the bounded read/navigation browser. Absence
	// disables the browser tool.
	Browser *BrowserSettings `json:"browser,omitempty"`
	// Compaction configures context reduction. A nil pointer means "use the
	// built-in defaults", which is distinct from "disabled".
	Compaction *Compaction `json:"compaction,omitempty"`
}

// SearchSettings is the external search configuration.
//
// Credentials are named environment variables, never literal values: a
// settings file is readable by other local processes and must never contain a
// secret.
type SearchSettings struct {
	// DefaultProvider is used when the model names none. Empty uses the first
	// configured provider.
	DefaultProvider string `json:"defaultProvider,omitempty"`
	// Providers are the configured search providers.
	Providers []SearchProvider `json:"providers,omitempty"`
}

// SearchProvider configures one external search provider.
type SearchProvider struct {
	// Name is the model-facing provider name.
	Name string `json:"name"`
	// Kind is the wire shape: "brave" or "tavily".
	Kind string `json:"kind"`
	// Endpoint is the provider URL. Production requires https.
	Endpoint string `json:"endpoint"`
	// APIKeyEnv names the environment variable holding the credential.
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
	// TimeoutSeconds bounds one request.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// MaxResults bounds the page size.
	MaxResults int `json:"maxResults,omitempty"`
	// MaxBytes bounds one response body.
	MaxBytes int `json:"maxBytes,omitempty"`
	// Retries is the number of extra bounded attempts.
	Retries int `json:"retries,omitempty"`
}

// MCPSettings is the external MCP configuration.
type MCPSettings struct {
	// Servers are the configured MCP servers.
	Servers []MCPServer `json:"servers,omitempty"`
}

// MCPServer configures one MCP server.
//
// Command and Args are human configuration, never model input. Env passes only
// the named environment variables; the server does not inherit the rest of the
// process environment.
type MCPServer struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
	// Tools is the explicit safety classification: read, interact, mutate, or
	// unknown. A tool not listed is unknown and fails closed.
	Tools map[string]string `json:"tools,omitempty"`
	// Resources optionally bounds readable resource URI prefixes.
	Resources []string `json:"resources,omitempty"`
	// AllowMutation permits interaction/mutation calls at all. It is false by
	// default; authority and human approval still apply when true.
	AllowMutation bool `json:"allowMutation,omitempty"`
	// TimeoutSeconds bounds one JSON-RPC exchange.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// MaxBytes bounds one response and one exchange's cumulative bytes.
	MaxBytes int `json:"maxBytes,omitempty"`
	// MaxItems bounds content items and discovered capabilities.
	MaxItems int `json:"maxItems,omitempty"`
	// MaxTextChars bounds one tool/resource text result.
	MaxTextChars int `json:"maxTextChars,omitempty"`
	// MaxDepth bounds parsed argument and schema nesting.
	MaxDepth int `json:"maxDepth,omitempty"`
	// MaxArgumentBytes bounds encoded tool arguments.
	MaxArgumentBytes int `json:"maxArgumentBytes,omitempty"`
	// IdleTimeoutSeconds kills an idle server process.
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds,omitempty"`
}

// BrowserSettings configures bounded browser read/navigation.
//
// The browser has no interaction or mutation surface in Phase 8. A page is
// fetched only with GET; forms, clicks, JavaScript, and non-GET methods are
// unsupported and fail closed.
type BrowserSettings struct {
	// AllowedHosts is the exact host allowlist. Empty means the host opened
	// first; a session never widens automatically.
	AllowedHosts []string `json:"allowedHosts,omitempty"`
	// MaxNavigations bounds network navigations per session.
	MaxNavigations int `json:"maxNavigations,omitempty"`
	// MaxRedirects bounds redirects per navigation.
	MaxRedirects int `json:"maxRedirects,omitempty"`
	// MaxBytes bounds one response body.
	MaxBytes int `json:"maxBytes,omitempty"`
	// MaxChars bounds extracted page text.
	MaxChars int `json:"maxChars,omitempty"`
	// MaxLinks bounds extracted links.
	MaxLinks int `json:"maxLinks,omitempty"`
	// MaxSessions bounds live sessions.
	MaxSessions int `json:"maxSessions,omitempty"`
	// TimeoutSeconds bounds one request.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// SessionTTLSeconds bounds a session's lifetime.
	SessionTTLSeconds int `json:"sessionTtlSeconds,omitempty"`
}

// Compaction settings as configured by the user.
//
// A nil field means "use the built-in default", which is how a sparse JSON
// document can override one knob without restating the others.
type Compaction struct {
	// Enabled turns compaction on. The built-in default is on.
	Enabled *bool `json:"enabled,omitempty"`
	// Strategy selects how context is reduced.
	Strategy string `json:"strategy,omitempty"`
	// KeepRecent is the number of trailing messages preserved verbatim.
	KeepRecent *int `json:"keepRecent,omitempty"`
	// ThresholdPercent triggers compaction at this fraction of the window.
	ThresholdPercent *float64 `json:"thresholdPercent,omitempty"`
}

// Default returns the built-in settings.
func Default() Settings {
	return Settings{
		MaxTokens:       8192,
		Reasoning:       false,
		ReasoningEffort: "medium",
		Approvals:       map[string]string{},
		Roles:           map[string]Profile{},
		Compaction:      defaultCompaction(),
	}
}

// defaultCompaction returns the built-in compaction settings.
func defaultCompaction() *Compaction {
	enabled := true
	keepRecent := 8
	threshold := 80.0
	return &Compaction{
		Enabled:          &enabled,
		KeepRecent:       &keepRecent,
		ThresholdPercent: &threshold,
	}
}

// Load reads the configuration file for root.
//
// A missing file is not an error: Default is returned with ErrNotFound so
// callers can distinguish "no config" from "malformed config".
func Load(root string) (Settings, error) {
	path, err := Path(root)
	if err != nil {
		return Default(), err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), ErrNotFound
		}
		return Default(), fmt.Errorf("config: read %s: %w", path, err)
	}

	settings := Default()
	if err := json.Unmarshal(data, &settings); err != nil {
		return settings, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if settings.Approvals == nil {
		settings.Approvals = map[string]string{}
	}
	if settings.Roles == nil {
		settings.Roles = map[string]Profile{}
	}
	return settings, nil
}

// Path returns the configuration file path for root.
//
// The root parameter is vestigial: settings are machine-scoped, so the file is
// always ~/.ohgo/settings.json regardless of which project loaded it. It is
// kept so callers that pass a root keep compiling, but no project-local
// settings file exists.
func Path(root string) (string, error) {
	return state.Path("settings.json")
}

// Dir returns the directory that holds ohgo state.
func Dir() (string, error) {
	return state.Dir()
}

// Save writes settings to the configuration file for root.
//
// The write is atomic: data lands in a temporary file in the same directory and
// is renamed over the destination. A crash or a concurrent reader therefore sees
// either the old file or the new one, never a half-written document — which
// matters because /model rewrites this file while a human may be editing it.
func Save(settings Settings) error {
	path, err := Path("")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return fmt.Errorf("config: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Remove the temp file if anything below fails, so a failed save never
	// leaves droppings in ~/.ohgo.
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: write temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: set mode on temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("config: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("config: replace %s: %w", path, err)
	}
	return nil
}

// GlobalName returns the trimmed machine-wide default display name, or "" when
// unset.
//
// It is a suggested default for the initialization gate's worker prompt, never
// authoritative project worker identity.
func (s Settings) GlobalName() string {
	return strings.TrimSpace(s.UserDefaults.Name)
}

// ApprovalFor reports the approval policy for a tool.
//
// The explicit AutoApprove list wins over Approvals, and AutoApprove takes
// precedence over a deny so a user can force a tool on without editing the
// per-tool policy map.
func (s Settings) ApprovalFor(tool string) (string, bool) {
	for _, name := range s.AutoApprove {
		if strings.EqualFold(strings.TrimSpace(name), tool) {
			return "allow", true
		}
	}
	policy, ok := s.Approvals[tool]
	return policy, ok
}

// ModelID splits the configured model into provider and id.
//
// It returns ok=false when the value is not in "provider/id" form, so the
// caller can fall back to the default model rather than guessing a provider.
func (s Settings) ModelID() (provider, id string, ok bool) {
	return splitModel(s.Model)
}

// ModelFor returns the effective model reference for a workspace.
//
// Precedence within configuration is workspace-specific, then global default
// (§12). An unknown or unconfigured workspace falls back to the global model,
// so the presence of a workspace-specific model never has to be complete.
func (s Settings) ModelFor(workspace string) (provider, id string, ok bool) {
	if workspace != "" {
		if value, found := s.Models[normalizeWorkspaceKey(workspace)]; found {
			if provider, id, ok := splitModel(value); ok {
				return provider, id, true
			}
		}
	}
	return splitModel(s.Model)
}

// RoleModel returns the effective model reference and thinking effort for a
// role, plus a source label naming which layer won.
//
// Precedence is role profile, then legacy workspace override, then the global
// default. The source label is what lets the UI show the user *why* a role
// resolves the way it does instead of hiding the layering:
//
//	"role"      the roles map carries this role
//	"workspace" the legacy models map carries this role's workspace name
//	"default"   the global model applies
//	"builtin"   nothing configured; the caller falls back to the catalog default
//
// A role entry that carries only an effort contributes that effort while the
// model still resolves through the lower layers, so setting effort alone never
// silently unbinds a model.
func (s Settings) RoleModel(role string) (model, effort, source string) {
	key := strings.ToLower(strings.TrimSpace(role))
	if key != "" {
		if profile, found := s.Roles[key]; found {
			profileEffort := strings.TrimSpace(profile.Effort)
			if model := strings.TrimSpace(profile.Model); model != "" {
				return model, profileEffort, "role"
			}
			// No model on the profile: an effort-only entry keeps its effort
			// while the model resolves through the layers below.
			if m, _, _, ok := s.lowerLayerModel(key); ok {
				return m, profileEffort, "role"
			}
		}
	}
	if model, _, _, ok := s.lowerLayerModel(key); ok {
		if _, found := s.Models[key]; found {
			return model, "", "workspace"
		}
		return model, "", "default"
	}
	return "", "", "builtin"
}

// lowerLayerModel resolves the model below the role layer: the legacy
// workspace override, then the global default.
func (s Settings) lowerLayerModel(key string) (model, provider, id string, ok bool) {
	if key != "" {
		if value, found := s.Models[key]; found {
			if p, i, ok := splitModel(value); ok {
				return value, p, i, true
			}
		}
	}
	if p, i, ok := splitModel(s.Model); ok {
		return s.Model, p, i, true
	}
	return "", "", "", false
}

// AssignRole records a role's model reference and thinking effort.
//
// An empty model leaves the existing model binding untouched, so changing only
// the effort never unbinds a role; an empty effort clears it, returning the
// role to the global default.
func (s *Settings) AssignRole(role, model, effort string) {
	key := strings.ToLower(strings.TrimSpace(role))
	if key == "" {
		return
	}
	if s.Roles == nil {
		s.Roles = map[string]Profile{}
	}
	profile := s.Roles[key]
	if trimmed := strings.TrimSpace(model); trimmed != "" {
		profile.Model = trimmed
	}
	profile.Effort = strings.TrimSpace(effort)
	s.Roles[key] = profile
}

// SetRoleModel binds a role to a model while preserving its configured effort.
//
// It is the switch-a-model path (the plain REPL's /model): changing which model
// answers must not silently clear a thinking effort the user chose.
func (s *Settings) SetRoleModel(role, model string) {
	key := strings.ToLower(strings.TrimSpace(role))
	trimmed := strings.TrimSpace(model)
	if key == "" || trimmed == "" {
		return
	}
	if s.Roles == nil {
		s.Roles = map[string]Profile{}
	}
	profile := s.Roles[key]
	profile.Model = trimmed
	s.Roles[key] = profile
}

// normalizeWorkspaceKey lowercases and trims a workspace name for lookup, so
// "ASK" and "ask" address the same model.
func normalizeWorkspaceKey(workspace string) string {
	return strings.ToLower(strings.TrimSpace(workspace))
}

// splitModel parses a "provider/id" reference.
func splitModel(value string) (provider, id string, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	provider, id, ok = strings.Cut(value, "/")
	if !ok || provider == "" || id == "" {
		return "", "", false
	}
	return provider, id, true
}
