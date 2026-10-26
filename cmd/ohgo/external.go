package main

import (
	"errors"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/mcp"
	"github.com/PizenLabs/ohgo/search"
	"github.com/PizenLabs/ohgo/tools"
)

// mcpRuntime holds the one MCP manager a session's tool set built. It exists
// so the UI can inspect the same manager the tools use instead of opening a
// second set of server connections, and so session shutdown closes it once.
type mcpRuntime struct {
	mu       sync.Mutex
	manager  *mcp.Manager
	settings config.MCPSettings
	has      bool
	failures map[string]string
}

// set records the manager. The first non-nil manager wins: a session builds
// one tool set, and replacing it would strand the first manager's processes.
func (r *mcpRuntime) set(manager *mcp.Manager) {
	if r == nil || manager == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.manager == nil {
		r.manager = manager
	}
}

// getManager returns the shared manager, or nil when MCP is not configured.
func (r *mcpRuntime) getManager() *mcp.Manager {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager
}

// configure records the current MCP configuration so the manager UI keeps
// showing the same state the tools were built from.
func (r *mcpRuntime) configure(settings config.MCPSettings) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings = cloneMCPSettings(settings)
	r.has = true
}

// settingsSnapshot returns the current MCP configuration and whether one has
// been observed in this session.
func (r *mcpRuntime) settingsSnapshot() (config.MCPSettings, bool) {
	if r == nil {
		return config.MCPSettings{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.has {
		return config.MCPSettings{}, false
	}
	return cloneMCPSettings(r.settings), true
}

// setFailure records a bounded failure for one server. It is display metadata,
// never authority.
func (r *mcpRuntime) setFailure(name, text string) {
	if r == nil || name == "" || text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failures == nil {
		r.failures = map[string]string{}
	}
	r.failures[name] = text
}

// clearFailure removes a stale error after a successful observation.
func (r *mcpRuntime) clearFailure(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.failures, name)
}

// failure returns the last bounded failure for a server.
func (r *mcpRuntime) failure(name string) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures[name]
}

// cloneMCPSettings copies the slice so a caller cannot mutate shared state.
func cloneMCPSettings(settings config.MCPSettings) config.MCPSettings {
	settings.Servers = append([]config.MCPServer(nil), settings.Servers...)
	return settings
}

// close stops every MCP server the session started.
func (r *mcpRuntime) close() error {
	manager := r.getManager()
	if manager == nil {
		return nil
	}
	return manager.Close()
}

// externalTools builds the Phase 8 external knowledge capabilities from
// configuration.
//
// A missing block means the capability is not offered. A present but malformed
// block keeps the tool visible with a configuration failure, so the model and
// the user see the problem instead of a capability silently disappearing.
func externalTools(settings config.Settings, root string, runtime *mcpRuntime) []agent.Tool {
	var out []agent.Tool
	if settings.Search != nil {
		out = append(out, searchToolFromSettings(*settings.Search))
	}
	if settings.MCP != nil {
		out = append(out, mcpToolsFromSettings(*settings.MCP, root, runtime)...)
	}
	if settings.Browser != nil {
		out = append(out, tools.NewBrowserTool(browserConfigFromSettings(*settings.Browser)))
	}
	return out
}

// searchToolFromSettings converts explicit provider configuration.
func searchToolFromSettings(settings config.SearchSettings) *tools.SearchTool {
	providers := make([]search.ProviderConfig, 0, len(settings.Providers))
	for _, provider := range settings.Providers {
		providers = append(providers, search.ProviderConfig{
			Name:       provider.Name,
			Kind:       provider.Kind,
			Endpoint:   provider.Endpoint,
			APIKeyEnv:  provider.APIKeyEnv,
			Timeout:    seconds(provider.TimeoutSeconds),
			MaxResults: provider.MaxResults,
			MaxBytes:   provider.MaxBytes,
			Retries:    provider.Retries,
		})
	}
	client, err := search.New(providers)
	if err == nil {
		if defaultErr := client.SetDefault(settings.DefaultProvider); defaultErr != nil {
			err = defaultErr
		}
	}
	if err != nil {
		tool := tools.NewSearchTool(nil)
		tool.ConfigErr = err.Error()
		return tool
	}
	return tools.NewSearchTool(client)
}

// mcpToolsFromSettings converts configured MCP servers.
//
// Disabled servers contribute nothing: disabling MCP removes capabilities
// without changing native execution. An MCP block with no enabled server
// contributes no tools at all.
func mcpToolsFromSettings(settings config.MCPSettings, root string, runtime *mcpRuntime) []agent.Tool {
	if runtime != nil {
		runtime.configure(settings)
	}
	servers := make([]mcp.ServerConfig, 0, len(settings.Servers))
	for _, server := range settings.Servers {
		if !server.IsEnabled() {
			continue
		}
		servers = append(servers, mcpServerConfig(server, root))
	}
	if len(servers) == 0 {
		if len(settings.Servers) == 0 {
			// A present but empty block is a configuration error, not a
			// silent no-op: the human asked for MCP and named nothing.
			return failedMCPTools(errors.New("mcp: no servers configured"))
		}
		// Every server is disabled. Disabling MCP removes its capabilities
		// without changing native execution.
		return nil
	}
	manager, err := mcp.NewManager(servers)
	list := tools.NewMCPListTool(manager)
	read := tools.NewMCPReadTool(manager)
	call := tools.NewMCPCallTool(manager)
	if err != nil {
		return failedMCPTools(err)
	}
	runtime.set(manager)
	return []agent.Tool{list, read, call}
}

// mcpServerConfig converts one persisted server into the manager's config.
func mcpServerConfig(server config.MCPServer, root string) mcp.ServerConfig {
	dir := server.Dir
	if dir == "" {
		dir = root
	}
	return mcp.ServerConfig{
		Name:             server.Name,
		Transport:        mcp.Transport(server.Transport),
		Command:          server.Command,
		Args:             server.Args,
		Dir:              dir,
		URL:              server.URL,
		HeaderEnv:        server.HeaderEnv,
		Env:              server.Env,
		Trust:            mcp.Trust(server.Trust),
		Tools:            server.Tools,
		ResourcePrefixes: server.Resources,
		AllowMutation:    server.AllowMutation,
		Timeout:          seconds(server.TimeoutSeconds),
		CacheTTL:         seconds(server.CacheTTLSeconds),
		MaxBytes:         server.MaxBytes,
		MaxItems:         server.MaxItems,
		MaxTextChars:     server.MaxTextChars,
		MaxDepth:         server.MaxDepth,
		MaxArgumentBytes: server.MaxArgumentBytes,
		IdleTimeout:      seconds(server.IdleTimeoutSeconds),
	}
}

// failedMCPTools keeps a configuration problem visible while failing closed.
func failedMCPTools(err error) []agent.Tool {
	list := tools.NewMCPListTool(nil)
	read := tools.NewMCPReadTool(nil)
	call := tools.NewMCPCallTool(nil)
	message := err.Error()
	list.ConfigErr = message
	read.ConfigErr = message
	call.ConfigErr = message
	return []agent.Tool{list, read, call}
}

// browserConfigFromSettings converts browser bounds.
func browserConfigFromSettings(settings config.BrowserSettings) tools.BrowserConfig {
	return tools.BrowserConfig{
		AllowedHosts:   settings.AllowedHosts,
		MaxNavigations: settings.MaxNavigations,
		MaxRedirects:   settings.MaxRedirects,
		MaxBytes:       settings.MaxBytes,
		MaxChars:       settings.MaxChars,
		MaxLinks:       settings.MaxLinks,
		SessionTTL:     seconds(settings.SessionTTLSeconds),
		MaxSessions:    settings.MaxSessions,
		RequestTimeout: seconds(settings.TimeoutSeconds),
	}
}

// seconds converts a configured second count into a duration. Zero means the
// tool's own default; negative values are treated the same way.
func seconds(value int) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value) * time.Second
}
