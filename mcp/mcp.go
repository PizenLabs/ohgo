// Package mcp implements a bounded MCP client boundary.
//
// An MCP server is an external actor. Discovery returns capability
// descriptions, never authority: every mutating or executing call must be
// classified in configuration, admitted by the security gate, and approved by a
// human. Every capability has a stable "<server>:<capability>" identity, and
// server provenance is preserved rather than flattened into a global namespace.
//
// The package supports stdio and the streamable-HTTP request/response core,
// normalizes tools, resources, and prompts, caches discovery snapshots with an
// explicit refresh, bounds responses and argument trees, and maps progress
// notifications into the existing tool progress callback. A cancelled exchange
// kills a stdio child rather than leaving an orphan, and every result is
// untrusted data with provenance.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/jsonschema"
	"github.com/PizenLabs/ohgo/knowledge"
)

// Hard limits for MCP configuration and responses.
const (
	// MaxServers bounds configured MCP servers.
	MaxServers = 8
	// DefaultTimeout bounds one JSON-RPC exchange.
	DefaultTimeout = 20 * time.Second
	// MaxTimeout is the absolute exchange ceiling.
	MaxTimeout = 120 * time.Second
	// DefaultMaxBytes bounds one response line and the cumulative bytes of one
	// exchange, including ignored notifications.
	DefaultMaxBytes = 256 * 1024
	// MaxResponseBytes is the absolute response ceiling.
	MaxResponseBytes = 1024 * 1024
	// DefaultMaxItems bounds content items and discovered capabilities.
	DefaultMaxItems = 32
	// MaxItemsLimit is the absolute item ceiling.
	MaxItemsLimit = 256
	// DefaultMaxTextChars bounds one text item.
	DefaultMaxTextChars = 12_000
	// MaxTextCharsLimit is the absolute per-item text ceiling.
	MaxTextCharsLimit = 64_000
	// DefaultMaxDepth bounds parsed argument and schema nesting.
	DefaultMaxDepth = 16
	// DefaultMaxArgumentBytes bounds encoded tool arguments.
	DefaultMaxArgumentBytes = 256 * 1024
	// DefaultIdleTimeout kills a server process that has been idle.
	DefaultIdleTimeout = 2 * time.Minute
	// DefaultCacheTTL bounds how long a discovery snapshot is reused. It is
	// short enough that a capability change is observed without an explicit
	// refresh, and long enough that a turn does not re-handshake per call.
	DefaultCacheTTL = 5 * time.Minute
	// MaxCacheTTL is the absolute cache lifetime.
	MaxCacheTTL = time.Hour
)

// Transport names how the client reaches an MCP server.
//
// The transport is an implementation detail below the capability layer: the
// rest of the runtime sees normalized capabilities, never an HTTP or pipe
// handle.
type Transport string

// Supported transports.
const (
	// TransportStdio speaks newline-delimited JSON-RPC to a child process.
	TransportStdio Transport = "stdio"
	// TransportStreamableHTTP speaks JSON-RPC over HTTP POST, accepting either
	// a JSON response or a server-sent-event stream.
	TransportStreamableHTTP Transport = "streamable-http"
)

// ParseTransport validates a configured transport. An empty value is stdio,
// the historical default; an unknown value is refused rather than guessed.
func ParseTransport(value string) (Transport, bool) {
	switch Transport(strings.ToLower(strings.TrimSpace(value))) {
	case "", TransportStdio:
		return TransportStdio, true
	case TransportStreamableHTTP:
		return TransportStreamableHTTP, true
	}
	return "", false
}

// Trust is human-provided provenance metadata for a server.
//
// It is deliberately not authority: a local server does not gain mutation
// permission from its label, and an external server is not refused merely for
// being external. Admission is decided by ohgo's security boundary alone.
type Trust string

// Trust classes.
const (
	// TrustLocal marks a server the human runs on the same machine.
	TrustLocal Trust = "local"
	// TrustExternal marks a configured remote or third-party server.
	TrustExternal Trust = "external"
	// TrustUntrusted is the conservative default for an unspecified server.
	TrustUntrusted Trust = "untrusted"
)

// ParseTrust normalizes trust metadata. Anything unrecognised is untrusted, so
// a typo never upgrades a server's label.
func ParseTrust(value string) Trust {
	switch Trust(strings.ToLower(strings.TrimSpace(value))) {
	case TrustLocal:
		return TrustLocal
	case TrustExternal:
		return TrustExternal
	}
	return TrustUntrusted
}

// AuthorityClass is the normalized admission class of a capability.
//
// It is metadata for admission and UX. It never grants authority: the
// security boundary still decides whether the class is permitted under the
// active workspace and whether a human must approve.
type AuthorityClass string

// Normalized authority classes.
const (
	// AuthorityRead observes without changing state.
	AuthorityRead AuthorityClass = "READ"
	// AuthorityExecute starts a process or runs a remote operation that is
	// neither a read nor a durable mutation.
	AuthorityExecute AuthorityClass = "EXECUTE"
	// AuthorityMutate changes repository state.
	AuthorityMutate AuthorityClass = "MUTATE"
	// AuthorityExternalSideEffect changes or acts on state outside the
	// workspace.
	AuthorityExternalSideEffect AuthorityClass = "EXTERNAL_SIDE_EFFECT"
	// AuthorityUnknown is the fail-closed default.
	AuthorityUnknown AuthorityClass = "UNKNOWN"
)

// Classification is the explicit safety class of one MCP tool.
//
// The zero value is Unknown and fails closed. A tool name that merely sounds
// read-only is never evidence; only configuration can classify it.
type Classification string

// MCP tool classifications.
const (
	// ClassRead observes only.
	ClassRead Classification = "read"
	// ClassExecute runs a bounded remote operation without a durable state
	// change. It is stricter than read and weaker than interaction: ASK may
	// not run it, FUZZ and BUILD may after the normal approval.
	ClassExecute Classification = "execute"
	// ClassInteract changes transient external state.
	ClassInteract Classification = "interact"
	// ClassMutate durably changes external state.
	ClassMutate Classification = "mutate"
	// ClassUnknown is the fail-closed default.
	ClassUnknown Classification = "unknown"
)

// Valid reports whether the classification is explicit.
func (c Classification) Valid() bool {
	switch c {
	case ClassRead, ClassExecute, ClassInteract, ClassMutate, ClassUnknown:
		return true
	}
	return false
}

// Mutating reports whether the class can change external state.
func (c Classification) Mutating() bool {
	return c == ClassInteract || c == ClassMutate
}

// RequiresMutationBoundary reports whether the class may only use ModeMutate.
//
// Execution is included: running a remote operation is not a read, and ASK
// must not reach it. The mutation boundary is the admitted, approved path.
func (c Classification) RequiresMutationBoundary() bool {
	return c == ClassExecute || c == ClassInteract || c == ClassMutate
}

// Authority returns the normalized admission class.
//
// Every MCP tool acts outside the workspace, so a state-changing tool is an
// external side effect, never a repository mutation. The distinction matters
// to completion: an external call is not evidence that a repository objective
// was satisfied.
func (c Classification) Authority() AuthorityClass {
	switch c {
	case ClassRead:
		return AuthorityRead
	case ClassExecute:
		return AuthorityExecute
	case ClassInteract, ClassMutate:
		return AuthorityExternalSideEffect
	}
	return AuthorityUnknown
}

// ParseClassification maps configuration text to a classification. Anything
// unrecognised is Unknown, so a typo fails closed rather than granting read.
//
// "write" is accepted as a durable-mutation alias, and "exec" as an execution
// alias, because both spellings are common in human configuration.
func ParseClassification(value string) Classification {
	switch Classification(strings.ToLower(strings.TrimSpace(value))) {
	case ClassRead:
		return ClassRead
	case ClassExecute, "exec":
		return ClassExecute
	case ClassInteract:
		return ClassInteract
	case ClassMutate, "write":
		return ClassMutate
	}
	return ClassUnknown
}

// CapabilityKind distinguishes tools, resources, and prompt templates.
type CapabilityKind string

// Capability kinds.
const (
	// KindTool is an MCP tool.
	KindTool CapabilityKind = "tool"
	// KindResource is an MCP resource.
	KindResource CapabilityKind = "resource"
	// KindPrompt is an MCP prompt template.
	KindPrompt CapabilityKind = "prompt"
)

// ServerConfig configures one MCP server.
//
// Command and Args are human configuration. The model never supplies either,
// and the environment is filtered to the named variables so unrelated
// credentials are not inherited by an external server.
type ServerConfig struct {
	// Name is the model-facing server name.
	Name string
	// Transport selects stdio or streamable HTTP. Empty means stdio.
	Transport Transport
	// Command is the executable to start. It is never model-controlled.
	Command string
	// Args are the process arguments.
	Args []string
	// Dir is the child working directory. Empty inherits the agent's current
	// directory; the CLI sets it to the workspace root so a server that reads
	// its project from the working directory sees the same repository the
	// agent does.
	Dir string
	// URL is the streamable-HTTP endpoint. It is human configuration and must
	// be absolute http/https.
	URL string
	// HeaderEnv maps an HTTP header name to the environment variable that
	// holds its value. Configuration never contains the secret itself, and
	// the value is never logged, displayed, or published as evidence.
	HeaderEnv map[string]string
	// Env names environment variables to pass through. Values live in the
	// environment, never in configuration, logs, or model context.
	Env []string
	// Trust is human-provided provenance metadata. It is not authority.
	Trust Trust
	// Tools maps a tool name to read/execute/interact/mutate/unknown. A
	// missing tool is Unknown and fails closed.
	Tools map[string]string
	// ResourcePrefixes optionally bounds readable resource URIs. Empty means
	// the server's resources may be read.
	ResourcePrefixes []string
	// AllowMutation permits interaction/mutation/execute calls at all. It is
	// false by default; even when true, the authority and approval gates still
	// apply.
	AllowMutation bool
	// Timeout bounds one exchange.
	Timeout time.Duration
	// CacheTTL bounds discovery-snapshot reuse.
	CacheTTL time.Duration
	// MaxBytes bounds one response and the cumulative bytes of one exchange.
	MaxBytes int
	// MaxItems bounds content items and discovered capabilities.
	MaxItems int
	// MaxTextChars bounds one text item.
	MaxTextChars int
	// MaxDepth bounds parsed argument and schema nesting.
	MaxDepth int
	// MaxArgumentBytes bounds encoded tool arguments.
	MaxArgumentBytes int
	// IdleTimeout kills an idle server process.
	IdleTimeout time.Duration
}

// normalized returns a config with every bound filled and clamped.
func (c ServerConfig) normalized() ServerConfig {
	c.Name = strings.TrimSpace(c.Name)
	c.Command = strings.TrimSpace(c.Command)
	c.URL = strings.TrimSpace(c.URL)
	c.Trust = ParseTrust(string(c.Trust))
	if strings.TrimSpace(string(c.Transport)) == "" {
		c.Transport = TransportStdio
	} else if transport, ok := ParseTransport(string(c.Transport)); ok {
		c.Transport = transport
	}
	c.Timeout = clampDuration(c.Timeout, DefaultTimeout, MaxTimeout)
	c.CacheTTL = clampDuration(c.CacheTTL, DefaultCacheTTL, MaxCacheTTL)
	c.MaxBytes = clampInt(c.MaxBytes, DefaultMaxBytes, MaxResponseBytes)
	c.MaxItems = clampInt(c.MaxItems, DefaultMaxItems, MaxItemsLimit)
	c.MaxTextChars = clampInt(c.MaxTextChars, DefaultMaxTextChars, MaxTextCharsLimit)
	c.MaxDepth = clampInt(c.MaxDepth, DefaultMaxDepth, 64)
	c.MaxArgumentBytes = clampInt(c.MaxArgumentBytes, DefaultMaxArgumentBytes, MaxResponseBytes)
	c.IdleTimeout = clampDuration(c.IdleTimeout, DefaultIdleTimeout, 10*time.Minute)
	return c
}

// validate checks a normalized config.
func (c ServerConfig) validate() error {
	if c.Name == "" {
		return errors.New("mcp: server name is required")
	}
	if !validServerName(c.Name) {
		return fmt.Errorf("mcp: server name %q must be non-empty and must not contain ':' or control characters", c.Name)
	}
	switch c.Transport {
	case TransportStdio:
		if c.Command == "" {
			return fmt.Errorf("mcp: server %q has no command", c.Name)
		}
		if c.URL != "" {
			return fmt.Errorf("mcp: stdio server %q must not set a URL", c.Name)
		}
	case TransportStreamableHTTP:
		if c.Command != "" {
			return fmt.Errorf("mcp: http server %q must not set a command", c.Name)
		}
		parsed, err := url.Parse(c.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("mcp: server %q has an invalid URL", c.Name)
		}
		if parsed.User != nil {
			// Credentials belong in HeaderEnv, never in the URL: the URL is
			// configuration and may be shown in diagnostics.
			return fmt.Errorf("mcp: server %q URL must not contain userinfo; use headerEnv", c.Name)
		}
	default:
		return fmt.Errorf("mcp: server %q has an unsupported transport %q", c.Name, c.Transport)
	}
	for name := range c.Tools {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("mcp: server %q has an empty tool name", c.Name)
		}
	}
	for _, name := range c.Env {
		if !validEnvName(name) {
			return fmt.Errorf("mcp: server %q has invalid environment name %q", c.Name, name)
		}
	}
	for header, name := range c.HeaderEnv {
		if !validHeaderName(header) {
			return fmt.Errorf("mcp: server %q has invalid header name %q", c.Name, header)
		}
		if !validEnvName(name) {
			return fmt.Errorf("mcp: server %q header %q has invalid environment name %q", c.Name, header, name)
		}
	}
	return nil
}

// Classify returns the configured class of a tool.
func (c ServerConfig) Classify(tool string) Classification {
	if c.Tools == nil {
		return ClassUnknown
	}
	value, ok := c.Tools[tool]
	if !ok {
		return ClassUnknown
	}
	class := ParseClassification(value)
	if !class.Valid() {
		return ClassUnknown
	}
	return class
}

// allowsResource reports whether a resource URI is permitted by configuration.
func (c ServerConfig) allowsResource(uri string) bool {
	if len(c.ResourcePrefixes) == 0 {
		return true
	}
	for _, prefix := range c.ResourcePrefixes {
		if strings.HasPrefix(uri, prefix) {
			return true
		}
	}
	return false
}

// redact removes configured credential values from text that may become model
// context, an error, or evidence.
//
// The configuration stores variable names, never values, so this is the only
// place that reads them for redaction. A short value is left alone: replacing
// every "1" or "on" would corrupt normal content without protecting a secret.
func (c ServerConfig) redact(text string) string {
	if text == "" {
		return text
	}
	names := append([]string(nil), c.Env...)
	for _, name := range c.HeaderEnv {
		names = append(names, name)
	}
	for _, name := range names {
		value := os.Getenv(name)
		if len(value) < 4 {
			continue
		}
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	return text
}

// Capability is one discovered MCP tool, resource, or prompt.
//
// Discovery is description, not permission. Classification is copied from
// configuration, and Schema is compiled and bounded before use.
type Capability struct {
	Server         string             `json:"server"`
	Kind           CapabilityKind     `json:"kind"`
	Name           string             `json:"name"`
	Description    string             `json:"description,omitempty"`
	URI            string             `json:"uri,omitempty"`
	MIMEType       string             `json:"mime_type,omitempty"`
	Classification Classification     `json:"classification"`
	Authority      AuthorityClass     `json:"authority"`
	Trust          Trust              `json:"trust"`
	Schema         *jsonschema.Schema `json:"-"`
	SchemaRaw      json.RawMessage    `json:"-"`
	DiscoveredAt   time.Time          `json:"discovered_at"`
}

// ID returns the stable, provenance-preserving capability identity.
//
// A tool is "<server>:<tool>". Prompts and resources are namespace-qualified
// because an MCP server may advertise a tool and a prompt with the same name;
// collapsing them would make two distinct capabilities share one identity.
// The server prefix is part of the identity, never decoration: two servers may
// advertise the same name, and a capability from one must never be represented
// as the other's.
func (c Capability) ID() string {
	switch c.Kind {
	case KindPrompt:
		return c.Server + ":prompt/" + c.Name
	case KindResource:
		value := c.URI
		if strings.TrimSpace(value) == "" {
			value = c.Name
		}
		return c.Server + ":resource/" + value
	default:
		return c.Server + ":" + c.Name
	}
}

// Mode is the safety boundary a caller asks the manager to enforce.
type Mode string

// Call modes.
const (
	// ModeRead accepts only read-classified tools and resources.
	ModeRead Mode = "read"
	// ModeMutate accepts only interact/mutate-classified tools that the server
	// configuration explicitly allows.
	ModeMutate Mode = "mutate"
)

// Meter charges and bounds one exchange. Before may clamp the response bytes;
// After is called once per exchange with the bytes actually read. Progress is
// called for a server progress notification that arrives while the exchange is
// outstanding.
//
// A nil Meter performs no accounting. The capability tool always supplies one,
// so an MCP exchange cannot bypass the root grant.
type Meter struct {
	Before   func(ctx context.Context, method string) (maxBytes int, err error)
	After    func(ctx context.Context, bytes int) error
	Progress func(Progress)
}

// Progress is one bounded progress observation from a server.
//
// It is presentation, not authority and not completion evidence: a server that
// reports progress has not proven that anything finished.
type Progress struct {
	// Message is the bounded human-readable update.
	Message string
	// Progress and Total are the server's own counters. Total is zero when the
	// server did not report one.
	Progress float64
	Total    float64
}

// ToolResult is one bounded MCP tool result.
type ToolResult struct {
	// Text is the bounded content published to the model.
	Text string
	// Items is the number of content items observed.
	Items int
	// Bytes is the response bytes observed.
	Bytes int
	// Truncated reports that a bound cut the content.
	Truncated bool
	// Reason names the bound.
	Reason string
	// IsError reports that the remote tool itself returned an error result.
	// It is a failed external call, not a transport success.
	IsError bool
}

// ResourceResult is one bounded MCP resource read.
type ResourceResult struct {
	// URI is the resource locator.
	URI string
	// MIMEType is the reported type.
	MIMEType string
	// Text is the bounded content.
	Text string
	// Items is the number of content entries observed.
	Items int
	// Bytes is the response bytes observed.
	Bytes int
	// Truncated reports that a bound cut the content.
	Truncated bool
	// Reason names the bound.
	Reason string
}

// PromptResult is one bounded MCP prompt rendering.
//
// It is context, not authority: the runtime treats every message as untrusted
// external data, exactly like a tool result.
type PromptResult struct {
	// Name is the prompt template identity.
	Name string
	// Description is the server's bounded description.
	Description string
	// Text is the bounded rendered messages.
	Text string
	// Items is the number of messages observed.
	Items int
	// Bytes is the response bytes observed.
	Bytes int
	// Truncated reports that a bound cut the content.
	Truncated bool
	// Reason names the bound.
	Reason string
}

// Discovery is one bounded discovery result.
//
// Truncated and Reason are explicit: a server that advertises more
// capabilities than the bound is never presented as if the list were complete.
type Discovery struct {
	// Capabilities are the advertised tools, resources, and prompts, capped
	// and sorted.
	Capabilities []Capability
	// Instructions is the bounded usage guidance the server returned from
	// initialize. It is data from the server, not authority.
	Instructions string
	// Truncated reports that the bound cut the advertised list.
	Truncated bool
	// Reason names the bound that cut it.
	Reason string
	// FetchedAt is when the snapshot was observed.
	FetchedAt time.Time
	// CacheHit reports that this answer came from the capability cache rather
	// than a fresh exchange.
	CacheHit bool
}

// ServerStatus is the cheap, non-connecting view of one configured server.
//
// It is what /mcp lists: configuration, trust metadata, connection state, and
// the last cached capability count. It never starts a server.
type ServerStatus struct {
	Name      string
	Transport Transport
	Trust     Trust
	// Enabled reports whether the server contributes capabilities. A disabled
	// server is listed so the manager can show it, but it is never admitted.
	Enabled        bool
	Connected      bool
	Cached         bool
	Capabilities   int
	DiscoveredAt   time.Time
	CacheExpiresAt time.Time
}

// Manager owns the configured MCP servers.
type Manager struct {
	mu      sync.Mutex
	servers map[string]*serverEntry
	order   []string
	start   func(ServerConfig) (serverProcess, error)
	closed  bool
}

// serverEntry holds one configured server, its lazily started client, and the
// cached capability snapshot.
type serverEntry struct {
	cfg    ServerConfig
	client *client
	// disabled is runtime enablement, separate from configuration. A disabled
	// entry stays in the manager so the UI can show it and re-enable it
	// without inventing a second registry solely for the manager.
	disabled bool

	// stateMu guards the cache. It is held across a discovery exchange so two
	// callers cannot race two handshakes for one server.
	stateMu  sync.Mutex
	cache    *Discovery
	cachedAt time.Time
}

// NewManager validates and builds a manager.
func NewManager(configs []ServerConfig) (*Manager, error) {
	return newManager(configs, startStdio)
}

// newManager is the test seam for process construction.
func newManager(configs []ServerConfig, start func(ServerConfig) (serverProcess, error)) (*Manager, error) {
	if len(configs) > MaxServers {
		return nil, fmt.Errorf("mcp: %d servers configured, maximum is %d", len(configs), MaxServers)
	}
	if len(configs) == 0 {
		return nil, errors.New("mcp: no servers configured")
	}
	manager := &Manager{servers: map[string]*serverEntry{}, start: start}
	for _, raw := range configs {
		cfg := raw.normalized()
		if err := cfg.validate(); err != nil {
			return nil, err
		}
		if _, exists := manager.servers[cfg.Name]; exists {
			return nil, fmt.Errorf("mcp: duplicate server %q", cfg.Name)
		}
		manager.servers[cfg.Name] = &serverEntry{cfg: cfg}
		manager.order = append(manager.order, cfg.Name)
	}
	return manager, nil
}

// Names returns the enabled server names in order.
//
// A disabled server stays visible through Status but contributes no
// capability, so the model-facing roster never includes one.
func (m *Manager) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.order))
	for _, name := range m.order {
		if entry := m.servers[name]; entry != nil && !entry.disabled {
			out = append(out, name)
		}
	}
	return out
}

// Configured reports whether a server is configured and enabled.
func (m *Manager) Configured(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.servers[name]
	return ok && !entry.disabled
}

// SetServerEnabled enables or disables one server at runtime.
//
// Enabling does not connect: the first discovery or call still starts the
// server lazily. Enabling from disabled also drops any cached snapshot, so the
// reconnect observes the server as it is now rather than as it was in an
// earlier enabled period. Disabling closes the live client and drops the cached
// snapshot, because a disabled server must not keep answering from a snapshot
// taken while it was enabled. Configuration persistence is the caller's job.
func (m *Manager) SetServerEnabled(cfg ServerConfig, enabled bool) error {
	cfg = cfg.normalized()
	if err := cfg.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return knowledge.Fail(knowledge.FailureNotConfigured, false, "mcp manager is closed")
	}
	entry, ok := m.servers[cfg.Name]
	if !ok {
		entry = &serverEntry{cfg: cfg}
		m.servers[cfg.Name] = entry
		m.order = append(m.order, cfg.Name)
	}
	// An existing entry keeps the configuration it was built with. Runtime
	// enablement is not a config rewrite; a changed command, URL, or bound
	// belongs to the next session so a live client is never mutated mid-flight.
	wasDisabled := entry.disabled
	entry.disabled = !enabled
	var client *client
	if !enabled {
		client = entry.client
		entry.client = nil
	}
	m.mu.Unlock()

	if client != nil {
		if err := client.Close(); err != nil {
			return err
		}
	}
	// A disabled server must forget what it knew, and a server coming back
	// from disabled must observe its current capabilities rather than reuse a
	// snapshot from a previous enabled period. Both paths drop the cache; a
	// set-enabled on an already-enabled server changes nothing.
	if !enabled || wasDisabled {
		entry.stateMu.Lock()
		entry.cache = nil
		entry.cachedAt = time.Time{}
		entry.stateMu.Unlock()
	}
	return nil
}

// Status returns the cheap cached view of every configured server. It never
// connects, so calling it is safe at startup and from a UI key handler.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	entries := make([]*serverEntry, 0, len(m.order))
	for _, name := range m.order {
		entries = append(entries, m.servers[name])
	}
	m.mu.Unlock()

	out := make([]ServerStatus, 0, len(entries))
	for _, entry := range entries {
		entry.stateMu.Lock()
		m.mu.Lock()
		disabled := entry.disabled
		m.mu.Unlock()
		status := ServerStatus{
			Name:      entry.cfg.Name,
			Transport: entry.cfg.Transport,
			Trust:     entry.cfg.Trust,
			Enabled:   !disabled,
			Cached:    entry.cache != nil,
		}
		if entry.cache != nil {
			status.Capabilities = len(entry.cache.Capabilities)
			status.DiscoveredAt = entry.cachedAt
			status.CacheExpiresAt = entry.cachedAt.Add(entry.cfg.CacheTTL)
		}
		entry.stateMu.Unlock()
		m.mu.Lock()
		client := entry.client
		m.mu.Unlock()
		if client != nil {
			status.Connected = client.connected()
		}
		out = append(out, status)
	}
	return out
}

// Close stops every server process.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	entries := make([]*serverEntry, 0, len(m.servers))
	for _, entry := range m.servers {
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	var first error
	for _, entry := range entries {
		m.mu.Lock()
		client := entry.client
		m.mu.Unlock()
		if client == nil {
			continue
		}
		entry.stateMu.Lock()
		entry.cache = nil
		entry.cachedAt = time.Time{}
		entry.stateMu.Unlock()
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// entryFor returns a configured server entry.
func (m *Manager) entryFor(name string) (*serverEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, knowledge.Fail(knowledge.FailureNotConfigured, false,
			"mcp manager is closed")
	}
	entry, ok := m.servers[name]
	if !ok {
		return nil, knowledge.Fail(knowledge.FailureNotConfigured, false,
			"mcp server %q is not configured", name)
	}
	if entry.disabled {
		return nil, knowledge.Fail(knowledge.FailureNotConfigured, false,
			"mcp server %q is disabled", name)
	}
	return entry, nil
}

// clientFor returns the lazily started client for a configured server.
func (m *Manager) clientFor(name string) (*client, ServerConfig, error) {
	entry, err := m.entryFor(name)
	if err != nil {
		return nil, ServerConfig{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ServerConfig{}, knowledge.Fail(knowledge.FailureNotConfigured, false,
			"mcp manager is closed")
	}
	if entry.client == nil {
		entry.client = newClient(entry.cfg, m.start)
	}
	return entry.client, entry.cfg, nil
}

// Classify returns the configured classification of one tool.
//
// The answer is configuration metadata, never authority. A server name and
// tool name that are not configured fail closed as Unknown.
func (m *Manager) Classify(name, tool string) (Classification, bool) {
	entry, err := m.entryFor(name)
	if err != nil {
		return ClassUnknown, false
	}
	return entry.cfg.Classify(tool), true
}

// Discover lists the bounded capabilities a server advertises, reusing a
// fresh cached snapshot.
//
// It starts and initializes the server on a cache miss, which is
// configuration-trusted: the model never chooses the command or URL. A failed
// discovery grants nothing and is never cached as success.
func (m *Manager) Discover(ctx context.Context, name string, meter Meter) (Discovery, error) {
	if err := ctx.Err(); err != nil {
		return Discovery{}, contextFailure(ctx)
	}
	entry, err := m.entryFor(name)
	if err != nil {
		return Discovery{}, err
	}
	return m.discover(ctx, entry, meter, false)
}

// Refresh bypasses the capability cache and observes the server again.
func (m *Manager) Refresh(ctx context.Context, name string, meter Meter) (Discovery, error) {
	if err := ctx.Err(); err != nil {
		return Discovery{}, contextFailure(ctx)
	}
	entry, err := m.entryFor(name)
	if err != nil {
		return Discovery{}, err
	}
	return m.discover(ctx, entry, meter, true)
}

// discover performs one cached or fresh discovery.
func (m *Manager) discover(ctx context.Context, entry *serverEntry, meter Meter, force bool) (Discovery, error) {
	entry.stateMu.Lock()
	defer entry.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Discovery{}, contextFailure(ctx)
	}
	if !force && entry.cache != nil {
		age := time.Since(entry.cachedAt)
		if age >= 0 && age < entry.cfg.CacheTTL {
			cached := cloneDiscovery(*entry.cache)
			cached.CacheHit = true
			return cached, nil
		}
	}
	result, err := m.discoverFresh(ctx, entry, meter)
	if err != nil {
		// A failed refresh must not silently keep serving a stale snapshot as
		// if it were current: drop it, so the next caller observes the failure
		// rather than inventing a capability list.
		entry.cache = nil
		entry.cachedAt = time.Time{}
		return Discovery{}, err
	}
	entry.cache = &result
	entry.cachedAt = result.FetchedAt
	cached := cloneDiscovery(result)
	return cached, nil
}

// discoverFresh performs the actual handshake and capability listing.
func (m *Manager) discoverFresh(ctx context.Context, entry *serverEntry, meter Meter) (Discovery, error) {
	name := entry.cfg.Name
	cfg := entry.cfg
	client, _, err := m.clientFor(name)
	if err != nil {
		return Discovery{}, err
	}
	if err := client.initialize(ctx, meter); err != nil {
		return Discovery{}, err
	}
	instructions := client.serverInstructions()
	instructions = cfg.redact(instructions)
	instructions, _ = knowledge.BoundText(instructions, cfg.MaxTextChars)
	now := time.Now().UTC()
	var capabilities []Capability
	truncated := false

	tools, toolsTruncated, err := client.listTools(ctx, meter)
	if err != nil && !isUnsupported(err) {
		return Discovery{}, err
	}
	truncated = truncated || toolsTruncated
	for _, tool := range tools {
		class := cfg.Classify(tool.Name)
		capability := Capability{
			Server:         name,
			Kind:           KindTool,
			Name:           tool.Name,
			Description:    cfg.redact(tool.Description),
			Classification: class,
			Authority:      class.Authority(),
			Trust:          cfg.Trust,
			SchemaRaw:      tool.SchemaRaw(),
			DiscoveredAt:   now,
		}
		if schemaRaw := tool.SchemaRaw(); len(schemaRaw) > 0 {
			schema, compileErr := compileSchema(schemaRaw, cfg.MaxDepth)
			if compileErr != nil {
				// An unusable schema is not a usable capability. Keep the
				// discovery row so the user can see why it was refused, but
				// leave Schema nil so validation fails closed.
				capability.Description = strings.TrimSpace(capability.Description +
					" [schema rejected: " + compileErr.Error() + "]")
			} else {
				capability.Schema = schema
			}
		}
		capabilities = append(capabilities, capability)
	}

	resources, resourcesTruncated, err := client.listResources(ctx, meter)
	if err != nil && !isUnsupported(err) {
		return Discovery{}, err
	}
	truncated = truncated || resourcesTruncated
	for _, resource := range resources {
		capabilities = append(capabilities, Capability{
			Server:         name,
			Kind:           KindResource,
			Name:           resource.Name,
			Description:    cfg.redact(resource.Description),
			URI:            cfg.redact(resource.URI),
			MIMEType:       cfg.redact(resource.MIMEType),
			Classification: ClassRead,
			Authority:      AuthorityRead,
			Trust:          cfg.Trust,
			DiscoveredAt:   now,
		})
	}

	prompts, promptsTruncated, err := client.listPrompts(ctx, meter)
	if err != nil && !isUnsupported(err) {
		return Discovery{}, err
	}
	truncated = truncated || promptsTruncated
	for _, prompt := range prompts {
		capability := Capability{
			Server:         name,
			Kind:           KindPrompt,
			Name:           prompt.Name,
			Description:    cfg.redact(prompt.Description),
			Classification: ClassRead,
			Authority:      AuthorityRead,
			Trust:          cfg.Trust,
			SchemaRaw:      prompt.schemaRaw(),
			DiscoveredAt:   now,
		}
		if len(capability.SchemaRaw) > 0 {
			if schema, compileErr := compileSchema(capability.SchemaRaw, cfg.MaxDepth); compileErr == nil {
				capability.Schema = schema
			}
		}
		capabilities = append(capabilities, capability)
	}

	// Tools first. A truncated discovery excerpt must contain the callable
	// tool contracts before prompt templates and resources.
	kindOrder := func(kind CapabilityKind) int {
		switch kind {
		case KindTool:
			return 0
		case KindResource:
			return 1
		case KindPrompt:
			return 2
		default:
			return 3
		}
	}
	sort.Slice(capabilities, func(i, j int) bool {
		left, right := kindOrder(capabilities[i].Kind), kindOrder(capabilities[j].Kind)
		if left != right {
			return left < right
		}
		return capabilities[i].Name < capabilities[j].Name
	})
	if len(capabilities) > cfg.MaxItems {
		capabilities = capabilities[:cfg.MaxItems]
		truncated = true
	}
	result := Discovery{
		Capabilities: capabilities,
		Instructions: instructions,
		FetchedAt:    now,
		Truncated:    truncated,
	}
	if truncated {
		result.Reason = "item_limit"
	}
	return result, nil
}

// cloneDiscovery copies a discovery result so a caller cannot mutate the
// cached slice through the returned value.
func cloneDiscovery(source Discovery) Discovery {
	source.Capabilities = append([]Capability(nil), source.Capabilities...)
	return source
}

// ReadResource reads one bounded resource.
func (m *Manager) ReadResource(ctx context.Context, name, uri string, meter Meter) (ResourceResult, error) {
	if strings.TrimSpace(uri) == "" {
		return ResourceResult{}, knowledge.Fail(knowledge.FailureInvalidRequest, false, "resource URI is required")
	}
	client, cfg, err := m.clientFor(name)
	if err != nil {
		return ResourceResult{}, err
	}
	if !cfg.allowsResource(uri) {
		return ResourceResult{}, knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"resource %q is not permitted by server %q configuration", uri, name)
	}
	if err := client.initialize(ctx, meter); err != nil {
		return ResourceResult{}, err
	}
	raw, bytes, err := client.exchange(ctx, "resources/read", map[string]any{"uri": uri}, meter)
	if err != nil {
		return ResourceResult{}, err
	}
	return parseResource(raw, bytes, cfg)
}

// CallTool calls one MCP tool under an explicit mode.
//
// Read mode accepts only tools classified read. Mutate mode accepts only
// execute/interact/mutate tools, requires AllowMutation, and leaves the final
// authority and approval decision to the security gate. Unknown always fails
// closed.
func (m *Manager) CallTool(ctx context.Context, name, tool string, args map[string]any, mode Mode, meter Meter) (ToolResult, error) {
	if strings.TrimSpace(tool) == "" {
		return ToolResult{}, knowledge.Fail(knowledge.FailureInvalidRequest, false, "tool name is required")
	}
	if args == nil {
		args = map[string]any{}
	}
	client, cfg, err := m.clientFor(name)
	if err != nil {
		return ToolResult{}, err
	}
	class := cfg.Classify(tool)
	if class == ClassUnknown {
		return ToolResult{}, knowledge.Fail(knowledge.FailureUnknownCapability, false,
			"unknown mcp capability %q; call mcp_list for tools, and use mcp_read prompt=<name> for prompts", tool)
	}
	switch mode {
	case ModeRead:
		if class != ClassRead {
			return ToolResult{}, knowledge.Fail(knowledge.FailureMutationDenied, false,
				"mcp tool %q is classified %s, not read", tool, class)
		}
	case ModeMutate:
		if !class.RequiresMutationBoundary() {
			return ToolResult{}, knowledge.Fail(knowledge.FailureMutationDenied, false,
				"mcp tool %q is classified %s; only execute, interact, or mutate tools may use this boundary", tool, class)
		}
		if !cfg.AllowMutation {
			return ToolResult{}, knowledge.Fail(knowledge.FailureMutationDenied, false,
				"mcp server %q does not allow mutation", name)
		}
	default:
		return ToolResult{}, knowledge.Fail(knowledge.FailureInvalidRequest, false, "unknown mcp call mode %q", mode)
	}

	maxValues := cfg.MaxItems * 8
	if maxValues < 64 {
		maxValues = 64
	}
	if err := knowledge.ValidateStructure(args, cfg.MaxArgumentBytes, cfg.MaxDepth, maxValues); err != nil {
		return ToolResult{}, knowledge.Fail(knowledge.FailureInvalidRequest, false, "mcp arguments: %v", err)
	}
	if err := m.validateToolArgs(ctx, name, tool, args, cfg, meter); err != nil {
		return ToolResult{}, err
	}
	if err := client.initialize(ctx, meter); err != nil {
		return ToolResult{}, err
	}
	raw, bytes, err := client.exchange(ctx, "tools/call", map[string]any{
		"name": tool, "arguments": args,
	}, meter)
	if err != nil {
		return ToolResult{}, err
	}
	return parseToolResult(raw, bytes, cfg)
}

// validateToolArgs validates one tool call against the advertised schema.
//
// It reads the capability cache first. A tool that is missing from a fresh
// snapshot forces one refresh, so a capability added on the server is not
// permanently invisible, and a schema change is observed at the cache
// boundary rather than silently trusted forever. A validation failure against
// a cached snapshot also refreshes once: a rejected argument may be valid under
// a schema the cache has not yet observed.
func (m *Manager) validateToolArgs(ctx context.Context, name, tool string, args map[string]any, cfg ServerConfig, meter Meter) error {
	discovery, err := m.Discover(ctx, name, meter)
	if err != nil {
		return err
	}
	capability, ok := findCapability(discovery, KindTool, tool)
	if !ok {
		discovery, err = m.Refresh(ctx, name, meter)
		if err != nil {
			return err
		}
		capability, ok = findCapability(discovery, KindTool, tool)
	}
	if !ok {
		return knowledge.Fail(knowledge.FailureUnknownCapability, false,
			"mcp tool %q was not discovered on server %q", tool, name)
	}
	validationErr := validateCapabilityArgs(capability, args, cfg.MaxDepth)
	if validationErr == nil || !discovery.CacheHit {
		return validationErr
	}
	refreshed, refreshErr := m.Refresh(ctx, name, meter)
	if refreshErr != nil {
		return validationErr
	}
	capability, ok = findCapability(refreshed, KindTool, tool)
	if !ok {
		return knowledge.Fail(knowledge.FailureUnknownCapability, false,
			"mcp tool %q was not discovered on server %q after refresh", tool, name)
	}
	return validateCapabilityArgs(capability, args, cfg.MaxDepth)
}

// validateCapabilityArgs validates arguments against a discovered schema. A
// capability with no schema accepts any structurally bounded object; a
// capability whose schema was rejected at discovery fails closed.
func validateCapabilityArgs(capability Capability, args map[string]any, maxDepth int) error {
	if len(capability.SchemaRaw) == 0 {
		return nil
	}
	if capability.Schema == nil {
		return knowledge.Fail(knowledge.FailureUnknownCapability, false,
			"mcp capability %q has an unusable input schema", capability.ID())
	}
	if err := capability.Schema.Validate(args); err != nil {
		return knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"mcp tool %q arguments: %v", capability.Name, err)
	}
	return nil
}

// findCapability returns one capability of a given kind by name.
func findCapability(discovery Discovery, kind CapabilityKind, name string) (Capability, bool) {
	for _, capability := range discovery.Capabilities {
		if capability.Kind == kind && capability.Name == name {
			return capability, true
		}
	}
	return Capability{}, false
}

// GetPrompt renders one MCP prompt template.
//
// A prompt is input/context, never authority and never an instruction from the
// runtime: its messages are untrusted external data and still pass through the
// normal ohgo pipeline.
func (m *Manager) GetPrompt(ctx context.Context, name, prompt string, args map[string]any, meter Meter) (PromptResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return PromptResult{}, knowledge.Fail(knowledge.FailureInvalidRequest, false, "prompt name is required")
	}
	if args == nil {
		args = map[string]any{}
	}
	client, cfg, err := m.clientFor(name)
	if err != nil {
		return PromptResult{}, err
	}
	maxValues := cfg.MaxItems * 8
	if maxValues < 64 {
		maxValues = 64
	}
	if err := knowledge.ValidateStructure(args, cfg.MaxArgumentBytes, cfg.MaxDepth, maxValues); err != nil {
		return PromptResult{}, knowledge.Fail(knowledge.FailureInvalidRequest, false, "mcp prompt arguments: %v", err)
	}
	discovery, err := m.Discover(ctx, name, meter)
	if err != nil {
		return PromptResult{}, err
	}
	capability, ok := findCapability(discovery, KindPrompt, prompt)
	if !ok {
		discovery, err = m.Refresh(ctx, name, meter)
		if err != nil {
			return PromptResult{}, err
		}
		capability, ok = findCapability(discovery, KindPrompt, prompt)
	}
	if !ok {
		return PromptResult{}, knowledge.Fail(knowledge.FailureUnknownCapability, false,
			"mcp prompt %q was not discovered on server %q", prompt, name)
	}
	if err := validateCapabilityArgs(capability, args, cfg.MaxDepth); err != nil {
		return PromptResult{}, err
	}
	if err := client.initialize(ctx, meter); err != nil {
		return PromptResult{}, err
	}
	raw, bytes, err := client.exchange(ctx, "prompts/get", map[string]any{
		"name": prompt, "arguments": args,
	}, meter)
	if err != nil {
		return PromptResult{}, err
	}
	return parsePrompt(raw, bytes, cfg)
}

// compileSchema compiles a bounded JSON Schema.
func compileSchema(raw json.RawMessage, maxDepth int) (*jsonschema.Schema, error) {
	object, err := knowledge.DecodeObject(raw, MaxResponseBytes, maxDepth)
	if err != nil {
		return nil, err
	}
	schema, err := jsonschema.CompileMap(object)
	if err != nil {
		return nil, err
	}
	return schema, nil
}

// isUnsupported reports a method the server does not implement.
func isUnsupported(err error) bool {
	var failure knowledge.Failure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Class == knowledge.FailureUnsupported
}

// clampInt returns fallback when value is non-positive, and clamps upward.
func clampInt(value, fallback, max int) int {
	if value <= 0 {
		value = fallback
	}
	if value > max {
		value = max
	}
	return value
}

// clampDuration returns fallback when value is non-positive, and clamps upward.
func clampDuration(value, fallback, max time.Duration) time.Duration {
	if value <= 0 {
		value = fallback
	}
	if value > max {
		value = max
	}
	return value
}

// validServerName reports whether a server name is usable as an identity
// prefix. A colon is refused because the stable capability identity is
// "<server>:<capability>"; control characters are refused because a name is
// displayed and may reach a terminal. Spaces and punctuation are allowed so an
// existing human-configured name keeps working.
func validServerName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	for _, r := range name {
		if r == ':' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// validEnvName reports whether name is a portable environment variable name.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
