// Package mcp implements a bounded MCP client boundary.
//
// An MCP server is an external actor. Discovery returns capability
// descriptions, never authority: every mutating call must be configured as
// such, admitted by the security gate, and approved by a human. Responses are
// bounded before they become model context, and a cancelled exchange kills the
// process rather than leaving an unbounded read behind.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	case ClassRead, ClassInteract, ClassMutate, ClassUnknown:
		return true
	}
	return false
}

// Mutating reports whether the class requires the mutation boundary.
func (c Classification) Mutating() bool {
	return c == ClassInteract || c == ClassMutate
}

// ParseClassification maps configuration text to a classification. Anything
// unrecognised is Unknown, so a typo fails closed rather than granting read.
func ParseClassification(value string) Classification {
	switch Classification(strings.ToLower(strings.TrimSpace(value))) {
	case ClassRead:
		return ClassRead
	case ClassInteract:
		return ClassInteract
	case ClassMutate:
		return ClassMutate
	}
	return ClassUnknown
}

// CapabilityKind distinguishes tools from resources.
type CapabilityKind string

// Capability kinds.
const (
	// KindTool is an MCP tool.
	KindTool CapabilityKind = "tool"
	// KindResource is an MCP resource.
	KindResource CapabilityKind = "resource"
)

// ServerConfig configures one MCP server.
//
// Command and Args are human configuration. The model never supplies either,
// and the environment is filtered to the named variables so unrelated
// credentials are not inherited by an external server.
type ServerConfig struct {
	// Name is the model-facing server name.
	Name string
	// Command is the executable to start. It is never model-controlled.
	Command string
	// Args are the process arguments.
	Args []string
	// Env names environment variables to pass through. Values live in the
	// environment, never in configuration, logs, or model context.
	Env []string
	// Tools maps a tool name to read/interact/mutate/unknown. A missing tool is
	// Unknown and fails closed.
	Tools map[string]string
	// ResourcePrefixes optionally bounds readable resource URIs. Empty means
	// the server's resources may be read.
	ResourcePrefixes []string
	// AllowMutation permits interaction/mutation calls at all. It is false by
	// default; even when true, the authority and approval gates still apply.
	AllowMutation bool
	// Timeout bounds one exchange.
	Timeout time.Duration
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
	c.Timeout = clampDuration(c.Timeout, DefaultTimeout, MaxTimeout)
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
	if c.Command == "" {
		return fmt.Errorf("mcp: server %q has no command", c.Name)
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

// Capability is one discovered MCP tool or resource.
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
	Schema         *jsonschema.Schema `json:"-"`
	SchemaRaw      json.RawMessage    `json:"-"`
	DiscoveredAt   time.Time          `json:"discovered_at"`
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
// After is called once per exchange with the bytes actually read.
//
// A nil Meter performs no accounting. The capability tool always supplies one,
// so an MCP exchange cannot bypass the root grant.
type Meter struct {
	Before func(ctx context.Context, method string) (maxBytes int, err error)
	After  func(ctx context.Context, bytes int) error
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

// Discovery is one bounded discovery result.
//
// Truncated and Reason are explicit: a server that advertises more
// capabilities than the bound is never presented as if the list were complete.
type Discovery struct {
	// Capabilities are the advertised tools and resources, capped and sorted.
	Capabilities []Capability
	// Truncated reports that the bound cut the advertised list.
	Truncated bool
	// Reason names the bound that cut it.
	Reason string
}

// Manager owns the configured MCP servers.
type Manager struct {
	mu      sync.Mutex
	servers map[string]*serverEntry
	order   []string
	start   func(ServerConfig) (serverProcess, error)
	closed  bool
}

// serverEntry holds one configured server and its lazily started client.
type serverEntry struct {
	cfg    ServerConfig
	client *client
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

// Names returns the configured server names in order.
func (m *Manager) Names() []string { return append([]string(nil), m.order...) }

// Configured reports whether a server is configured.
func (m *Manager) Configured(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.servers[name]
	return ok
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
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// clientFor returns the lazily started client for a configured server.
func (m *Manager) clientFor(name string) (*client, ServerConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ServerConfig{}, knowledge.Fail(knowledge.FailureNotConfigured, false,
			"mcp manager is closed")
	}
	entry, ok := m.servers[name]
	if !ok {
		return nil, ServerConfig{}, knowledge.Fail(knowledge.FailureNotConfigured, false,
			"mcp server %q is not configured", name)
	}
	if entry.client == nil {
		entry.client = newClient(entry.cfg, m.start)
	}
	return entry.client, entry.cfg, nil
}

// Discover lists the bounded capabilities a server advertises.
//
// It starts and initializes the server, which is configuration-trusted: the
// model never chooses the command. A failed discovery grants nothing.
func (m *Manager) Discover(ctx context.Context, name string, meter Meter) (Discovery, error) {
	client, cfg, err := m.clientFor(name)
	if err != nil {
		return Discovery{}, err
	}
	if err := client.initialize(ctx, meter); err != nil {
		return Discovery{}, err
	}
	now := time.Now().UTC()
	var capabilities []Capability
	truncated := false

	tools, toolsTruncated, err := client.listTools(ctx, meter)
	if err != nil && !isUnsupported(err) {
		return Discovery{}, err
	}
	truncated = truncated || toolsTruncated
	for _, tool := range tools {
		capability := Capability{
			Server:         name,
			Kind:           KindTool,
			Name:           tool.Name,
			Description:    tool.Description,
			Classification: cfg.Classify(tool.Name),
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
			Description:    resource.Description,
			URI:            resource.URI,
			MIMEType:       resource.MIMEType,
			Classification: ClassRead,
			DiscoveredAt:   now,
		})
	}

	sort.Slice(capabilities, func(i, j int) bool {
		if capabilities[i].Kind != capabilities[j].Kind {
			return capabilities[i].Kind < capabilities[j].Kind
		}
		return capabilities[i].Name < capabilities[j].Name
	})
	if len(capabilities) > cfg.MaxItems {
		capabilities = capabilities[:cfg.MaxItems]
		truncated = true
	}
	result := Discovery{Capabilities: capabilities, Truncated: truncated}
	if truncated {
		result.Reason = "item_limit"
	}
	return result, nil
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
// interact/mutate tools, requires AllowMutation, and leaves the final authority
// and approval decision to the security gate. Unknown always fails closed.
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
	switch mode {
	case ModeRead:
		if class != ClassRead {
			return ToolResult{}, knowledge.Fail(knowledge.FailureMutationDenied, false,
				"mcp tool %q is classified %s, not read", tool, class)
		}
	case ModeMutate:
		if class == ClassUnknown || class == ClassRead {
			return ToolResult{}, knowledge.Fail(knowledge.FailureMutationDenied, false,
				"mcp tool %q is classified %s; only interact or mutate tools may use this boundary", tool, class)
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
	if err := client.validateToolArgs(ctx, tool, args, cfg, meter); err != nil {
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

// validateToolArgs fetches discovery and validates the tool's input schema.
func (c *client) validateToolArgs(ctx context.Context, tool string, args map[string]any, cfg ServerConfig, meter Meter) error {
	if err := c.initialize(ctx, meter); err != nil {
		return err
	}
	tools, _, err := c.listTools(ctx, meter)
	if err != nil {
		return err
	}
	for _, item := range tools {
		if item.Name != tool {
			continue
		}
		schemaRaw := item.SchemaRaw()
		if len(schemaRaw) == 0 {
			return nil
		}
		schema, err := compileSchema(schemaRaw, cfg.MaxDepth)
		if err != nil {
			return knowledge.Fail(knowledge.FailureUnknownCapability, false,
				"mcp tool %q has an invalid input schema: %v", tool, err)
		}
		if err := schema.Validate(args); err != nil {
			return knowledge.Fail(knowledge.FailureInvalidRequest, false,
				"mcp tool %q arguments: %v", tool, err)
		}
		return nil
	}
	return knowledge.Fail(knowledge.FailureInvalidRequest, false, "mcp tool %q was not discovered", tool)
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
