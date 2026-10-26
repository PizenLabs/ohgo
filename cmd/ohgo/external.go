package main

import (
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/mcp"
	"github.com/PizenLabs/ohgo/search"
	"github.com/PizenLabs/ohgo/tools"
)

// externalTools builds the Phase 8 external knowledge capabilities from
// configuration.
//
// A missing block means the capability is not offered. A present but malformed
// block keeps the tool visible with a configuration failure, so the model and
// the user see the problem instead of a capability silently disappearing.
func externalTools(settings config.Settings, root string) []agent.Tool {
	var out []agent.Tool
	if settings.Search != nil {
		out = append(out, searchToolFromSettings(*settings.Search))
	}
	if settings.MCP != nil {
		out = append(out, mcpToolsFromSettings(*settings.MCP, root)...)
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
func mcpToolsFromSettings(settings config.MCPSettings, root string) []agent.Tool {
	servers := make([]mcp.ServerConfig, 0, len(settings.Servers))
	for _, server := range settings.Servers {
		dir := server.Dir
		if dir == "" {
			dir = root
		}
		servers = append(servers, mcp.ServerConfig{
			Name:             server.Name,
			Command:          server.Command,
			Args:             server.Args,
			Dir:              dir,
			Env:              server.Env,
			Tools:            server.Tools,
			ResourcePrefixes: server.Resources,
			AllowMutation:    server.AllowMutation,
			Timeout:          seconds(server.TimeoutSeconds),
			MaxBytes:         server.MaxBytes,
			MaxItems:         server.MaxItems,
			MaxTextChars:     server.MaxTextChars,
			MaxDepth:         server.MaxDepth,
			MaxArgumentBytes: server.MaxArgumentBytes,
			IdleTimeout:      seconds(server.IdleTimeoutSeconds),
		})
	}
	manager, err := mcp.NewManager(servers)
	list := tools.NewMCPListTool(manager)
	read := tools.NewMCPReadTool(manager)
	call := tools.NewMCPCallTool(manager)
	if err != nil {
		list.ConfigErr = err.Error()
		read.ConfigErr = err.Error()
		call.ConfigErr = err.Error()
		list.Manager = nil
		read.Manager = nil
		call.Manager = nil
	}
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
