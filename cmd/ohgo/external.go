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
	once    sync.Once
	manager *mcp.Manager
}

// set records the manager. The first non-nil manager wins: a session builds
// one tool set, and replacing it would strand the first manager's processes.
func (r *mcpRuntime) set(manager *mcp.Manager) {
	if r == nil || manager == nil {
		return
	}
	r.once.Do(func() { r.manager = manager })
}

// close stops every MCP server the session started.
func (r *mcpRuntime) close() error {
	if r == nil || r.manager == nil {
		return nil
	}
	return r.manager.Close()
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
	servers := make([]mcp.ServerConfig, 0, len(settings.Servers))
	for _, server := range settings.Servers {
		if !server.IsEnabled() {
			continue
		}
		dir := server.Dir
		if dir == "" {
			dir = root
		}
		servers = append(servers, mcp.ServerConfig{
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
		})
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
