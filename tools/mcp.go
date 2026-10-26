package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/knowledge"
	"github.com/PizenLabs/ohgo/mcp"
	"github.com/PizenLabs/ohgo/security"
)

// MaxMCPDiscoveryChars bounds one discovery answer published to the model.
const MaxMCPDiscoveryChars = 32_000

// MaxMCPInputSchemaChars bounds one advertised input schema. The model needs
// property names, required fields, and enums; a pathological schema must not
// crowd out the rest of the discovery answer.
const MaxMCPInputSchemaChars = 4096

// MCPManager is the bounded MCP boundary the tools consume. It is an interface
// so tests can assert the exact mode a tool requests without starting a
// subprocess; production supplies *mcp.Manager.
type MCPManager interface {
	Names() []string
	Configured(name string) bool
	Discover(ctx context.Context, name string, meter mcp.Meter) (mcp.Discovery, error)
	ReadResource(ctx context.Context, name, uri string, meter mcp.Meter) (mcp.ResourceResult, error)
	CallTool(ctx context.Context, name, tool string, args map[string]any, mode mcp.Mode, meter mcp.Meter) (mcp.ToolResult, error)
	Close() error
}

// mcpMeter charges every MCP exchange to the root grant.
//
// Initialization, discovery, schema lookup, and the actual call are separate
// exchanges. Each one is charged and byte-bounded, so a server cannot multiply
// work or flood context through the protocol.
func mcpMeter(ctx agent.Context) mcp.Meter {
	return mcp.Meter{
		Before: func(context.Context, string) (int, error) {
			if err := checkCancelled(ctx.Ctx); err != nil {
				return 0, knowledge.Fail(knowledge.FailureCancelled, false, "mcp call cancelled")
			}
			if err := admitExternal(ctx); err != nil {
				return 0, err
			}
			return externalByteAllowance(ctx, mcp.DefaultMaxBytes)
		},
		After: func(_ context.Context, bytes int) error {
			return chargeExternalBytes(ctx, bytes)
		},
	}
}

// MCPListTool discovers configured MCP capabilities.
//
// Discovery is description only. It never grants authority, never changes a
// classification, and never makes a mutating tool callable.
type MCPListTool struct {
	Base
	Manager   MCPManager
	ConfigErr string
}

// NewMCPListTool returns the discovery tool.
func NewMCPListTool(manager MCPManager) *MCPListTool {
	tool := &MCPListTool{Manager: manager}
	tool.Base = NewBase("mcp_list",
		"List the tools and resources a configured MCP server advertises, with "+
			"the runtime's explicit read/interact/mutate/unknown classification, "+
			"each tool's bounded input schema, and the server's own usage "+
			"instructions. Use the schema to build mcp_read arguments. "+
			"Discovery is not authorization: an advertised capability is not a "+
			"permitted one, and an unknown classification fails closed. This "+
			"tool starts the configured server process; the command is human "+
			"configuration, never model input.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"server": map[string]any{
					"type":      "string",
					"minLength": 1,
					"description": "Configured server name; omitted lists every " +
						"configured server.",
				},
				"schema": map[string]any{
					"type": "string",
					"enum": []string{"summary", "full", "none"},
					"description": "Input-schema detail: summary (default) keeps " +
						"required fields, types, enums and bounds; full adds the " +
						"server's property descriptions; none omits schemas.",
				},
			},
		},
		agent.Approval{Tier: agent.TierRead, Reason: "lists a configured MCP server's advertised capabilities"},
	)
	// Configured keeps the tool in the candidate surface for ordinary tasks.
	// Without it, task-shape narrowing hides every MCP capability unless the
	// prompt happens to ask for web knowledge, which makes a repository
	// intelligence server unusable for normal code questions.
	tool.SetActionClasses(agent.ActionExternal, agent.ActionConfigured)
	return tool
}

// SecurityCapability implements agent.CapabilityNamer.
func (t *MCPListTool) SecurityCapability() security.Capability {
	return security.CapabilityExternalMCPRead
}

// Execute implements agent.Tool.
func (t *MCPListTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	started := time.Now()
	return withAudit(t.execute(ctx, args), started, ctx)
}

// execute performs discovery and returns an unaudited result.
func (t *MCPListTool) execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return externalCancelledResult(ctx, map[string]any{"capability": string(knowledge.CapabilityMCPDiscovery)})
	}
	if err := validateClosedArgs(args, "server", "schema"); err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	if t.Manager == nil {
		return externalFailureResult(mcpNotConfigured(t.ConfigErr), nil)
	}
	server, err := strArg(args, "server", "")
	if err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	schemaMode, err := strArg(args, "schema", "summary")
	if err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	switch schemaMode {
	case "", "summary":
		schemaMode = "summary"
	case "full", "none":
	default:
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"schema must be summary, full, or none"), nil)
	}
	names := t.Manager.Names()
	if strings.TrimSpace(server) != "" {
		if !t.Manager.Configured(server) {
			return externalFailureResult(knowledge.Fail(knowledge.FailureNotConfigured, false,
				"mcp server %q is not configured", server), nil)
		}
		names = []string{server}
	}
	details := map[string]any{
		"capability":         string(knowledge.CapabilityMCPDiscovery),
		"operation":          string(knowledge.OpRead),
		"discovery_only":     true,
		"grants_authority":   false,
		"configured_servers": len(names),
	}
	meter := mcpMeter(ctx)
	body := &strings.Builder{}
	total := 0
	failures := 0
	truncated := false
	truncationReason := ""
	for _, name := range names {
		if err := checkCancelled(ctx.Ctx); err != nil {
			return externalCancelledResult(ctx, details)
		}
		discovery, err := t.Manager.Discover(ctx.Ctx, name, meter)
		if err != nil {
			failures++
			if ctx.Ctx.Err() != nil {
				return externalErrorResult(ctx, err, details)
			}
			var failure knowledge.Failure
			if !errors.As(err, &failure) {
				failure = knowledge.Fail(knowledge.FailureUnavailable, true, "%v", err)
			}
			message, _ := knowledge.BoundText(failure.Message, 500)
			fmt.Fprintf(body, "server: %s\ndiscovery: failed (%s)\n",
				name, failure.Class)
			if strings.TrimSpace(message) != "" {
				fmt.Fprintf(body, "reason: %s\n", singleLine(message))
			}
			body.WriteString("\n")
			continue
		}
		capabilities := discovery.Capabilities
		truncated = truncated || discovery.Truncated
		if discovery.Truncated && truncationReason == "" {
			truncationReason = discovery.Reason
		}
		total += len(capabilities)
		fmt.Fprintf(body, "server: %s\ndisclosed: %d\n", name, len(capabilities))
		if discovery.Truncated {
			fmt.Fprintf(body, "truncated: true (%s)\n", discovery.Reason)
		}
		if discovery.Instructions != "" {
			instructions, _ := knowledge.BoundText(discovery.Instructions, 8000)
			fmt.Fprintf(body, "instructions:\n%s\n", strings.TrimRight(instructions, "\n"))
		}
		for _, capability := range capabilities {
			fmt.Fprintf(body, "  - kind: %s\n", capability.Kind)
			fmt.Fprintf(body, "    name: %s\n", capability.Name)
			fmt.Fprintf(body, "    classification: %s\n", capability.Classification)
			if capability.URI != "" {
				fmt.Fprintf(body, "    uri: %s\n", capability.URI)
			}
			if capability.MIMEType != "" {
				fmt.Fprintf(body, "    mime_type: %s\n", capability.MIMEType)
			}
			if capability.Description != "" {
				description, _ := knowledge.BoundText(capability.Description, 300)
				fmt.Fprintf(body, "    description: %s\n", singleLine(description))
			}
			if schemaMode != "none" && len(capability.SchemaRaw) > 0 {
				if schema, ok := boundedSchema(capability.SchemaRaw, schemaMode == "full"); ok {
					fmt.Fprintf(body, "    input_schema: %s\n", schema)
				}
			}
		}
		body.WriteString("\n")
	}
	status := "ok"
	if failures > 0 {
		status = "partial"
	}
	if total == 0 && failures == 0 {
		status = "empty"
	}
	bodyText, bodyCut := knowledge.BoundText(body.String(), MaxMCPDiscoveryChars)
	if bodyCut {
		truncated = true
		truncationReason = "output_limit"
	}
	header := &strings.Builder{}
	fmt.Fprintf(header, "capability: mcp.discovery\nstatus: %s\nservers: %d\ndisclosed: %d\n",
		status, len(names), total)
	header.WriteString("discovery_grants_authority: false\n")
	if truncated {
		fmt.Fprintf(header, "truncated: true (%s)\n", truncationReason)
	}
	details["status"] = status
	details["discovered"] = total
	details["failures"] = failures
	details["truncated"] = truncated
	return agent.Result{
		Text:    knowledge.Envelope(header.String(), bodyText),
		Details: details,
		Useless: total == 0,
		IsError: failures > 0 && total == 0,
	}
}

// MCPReadTool reads a resource or calls a read-classified MCP tool.
type MCPReadTool struct {
	Base
	Manager   MCPManager
	ConfigErr string
}

// NewMCPReadTool returns the read-only MCP tool.
func NewMCPReadTool(manager MCPManager) *MCPReadTool {
	tool := &MCPReadTool{Manager: manager}
	tool.Base = NewBase("mcp_read",
		"Read a configured MCP resource, or call an MCP tool explicitly "+
			"classified as read. Mutating, interactive, and unknown tools are "+
			"refused by this boundary; use mcp_call for an explicitly admitted "+
			"mutation. Results are bounded untrusted data with provenance.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"server"},
			"properties": map[string]any{
				"server": map[string]any{
					"type": "string", "minLength": 1,
					"description": "Configured server name.",
				},
				"resource": map[string]any{
					"type": "string", "minLength": 1,
					"description": "Resource URI to read.",
				},
				"tool": map[string]any{
					"type": "string", "minLength": 1,
					"description": "MCP tool classified as read.",
				},
				"arguments": map[string]any{
					"type":        "object",
					"description": "Arguments for a read tool; validated against the server schema and bounded.",
				},
			},
			"anyOf": []any{
				map[string]any{"required": []string{"resource"}},
				map[string]any{"required": []string{"tool"}},
			},
		},
		agent.Approval{Tier: agent.TierRead, Reason: "reads an external MCP resource or read-classified tool"},
	)
	tool.SetActionClasses(agent.ActionExternal, agent.ActionConfigured)
	return tool
}

// SecurityCapability implements agent.CapabilityNamer.
func (t *MCPReadTool) SecurityCapability() security.Capability {
	return security.CapabilityExternalMCPRead
}

// Execute implements agent.Tool.
func (t *MCPReadTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	started := time.Now()
	return withAudit(t.execute(ctx, args), started, ctx)
}

// execute performs a read and returns an unaudited result.
func (t *MCPReadTool) execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return externalCancelledResult(ctx, map[string]any{"capability": string(knowledge.CapabilityMCPReadTool)})
	}
	if err := validateClosedArgs(args, "server", "resource", "tool", "arguments"); err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	if t.Manager == nil {
		return externalFailureResult(mcpNotConfigured(t.ConfigErr), nil)
	}
	server, err := strArg(args, "server", "")
	if err != nil || strings.TrimSpace(server) == "" {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "server is required"), nil)
	}
	resource, err := strArg(args, "resource", "")
	if err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	toolName, err := strArg(args, "tool", "")
	if err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	if (strings.TrimSpace(resource) == "") == (strings.TrimSpace(toolName) == "") {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"exactly one of resource or tool is required"), nil)
	}
	arguments, err := objectArg(args, "arguments")
	if err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	if strings.TrimSpace(resource) != "" && len(arguments) > 0 {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false,
			"arguments apply only to a tool call"), nil)
	}
	meter := mcpMeter(ctx)
	if strings.TrimSpace(resource) != "" {
		requestID := knowledge.Hash("mcp-resource\x00" + server + "\x00" + resource)
		details := mcpDetails(server, knowledge.CapabilityMCPResource, resource, requestID)
		result, err := t.Manager.ReadResource(ctx.Ctx, server, resource, meter)
		if err != nil {
			return externalErrorResult(ctx, err, details)
		}
		return publishMCPResource(ctx, details, requestID, server, result)
	}
	requestID := knowledge.Hash("mcp-tool\x00" + server + "\x00" + toolName)
	details := mcpDetails(server, knowledge.CapabilityMCPReadTool, toolName, requestID)
	result, err := t.Manager.CallTool(ctx.Ctx, server, toolName, arguments, mcp.ModeRead, meter)
	if err != nil {
		return externalErrorResult(ctx, err, details)
	}
	return publishMCPTool(ctx, details, requestID, server, toolName, knowledge.CapabilityMCPReadTool, result)
}

// MCPCallTool performs an explicitly admitted MCP interaction or mutation.
type MCPCallTool struct {
	Base
	Manager   MCPManager
	ConfigErr string
}

// NewMCPCallTool returns the mutation-boundary MCP tool.
func NewMCPCallTool(manager MCPManager) *MCPCallTool {
	tool := &MCPCallTool{Manager: manager}
	tool.Base = NewBase("mcp_call",
		"Call a configured MCP tool explicitly classified as interact or mutate. "+
			"This boundary is unavailable in a read-only authority, requires an "+
			"explicit server configuration, and prompts for approval. A tool "+
			"classified read, unknown, or unclassified is refused. The call may "+
			"change external state; it is not a repository change and cannot "+
			"complete a repository objective by itself.",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"server", "tool"},
			"properties": map[string]any{
				"server": map[string]any{
					"type": "string", "minLength": 1,
					"description": "Configured server name.",
				},
				"tool": map[string]any{
					"type": "string", "minLength": 1,
					"description": "MCP tool explicitly classified interact or mutate.",
				},
				"arguments": map[string]any{
					"type":        "object",
					"description": "Arguments validated against the server schema and bounded.",
				},
			},
		},
		agent.Approval{
			Tier:   agent.TierWrite,
			Reason: "calls an MCP interaction/mutation tool and may change external state",
		},
	)
	tool.SetExclusive(true)
	tool.SetActionClasses(agent.ActionExternal, agent.ActionConfigured, agent.ActionMutate)
	tool.SetMutationOperations(contract.ChangeAny)
	return tool
}

// SecurityCapability implements agent.CapabilityNamer.
func (t *MCPCallTool) SecurityCapability() security.Capability {
	return security.CapabilityExternalMCPMutate
}

// Capability implements agent.CapabilityReporter. The declaration is already
// write, but reporting it per call keeps the authority classification explicit
// even if the declared tier is later relaxed.
func (t *MCPCallTool) Capability(map[string]any) agent.Tier { return agent.TierWrite }

// Execute implements agent.Tool.
func (t *MCPCallTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	started := time.Now()
	return withAudit(t.execute(ctx, args), started, ctx)
}

// execute performs a mutation-boundary call and returns an unaudited result.
func (t *MCPCallTool) execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return externalCancelledResult(ctx, map[string]any{"capability": string(knowledge.CapabilityMCPMutationTool)})
	}
	if err := validateClosedArgs(args, "server", "tool", "arguments"); err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	if t.Manager == nil {
		return externalFailureResult(mcpNotConfigured(t.ConfigErr), nil)
	}
	server, err := strArg(args, "server", "")
	if err != nil || strings.TrimSpace(server) == "" {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "server is required"), nil)
	}
	toolName, err := strArg(args, "tool", "")
	if err != nil || strings.TrimSpace(toolName) == "" {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "tool is required"), nil)
	}
	arguments, err := objectArg(args, "arguments")
	if err != nil {
		return externalFailureResult(knowledge.Fail(knowledge.FailureInvalidRequest, false, "%v", err), nil)
	}
	requestID := knowledge.Hash("mcp-mutate\x00" + server + "\x00" + toolName)
	details := mcpDetails(server, knowledge.CapabilityMCPMutationTool, toolName, requestID)
	result, err := t.Manager.CallTool(ctx.Ctx, server, toolName, arguments, mcp.ModeMutate, mcpMeter(ctx))
	if err != nil {
		return externalErrorResult(ctx, err, details)
	}
	return publishMCPTool(ctx, details, requestID, server, toolName, knowledge.CapabilityMCPMutationTool, result)
}

// mcpNotConfigured renders a fail-closed configuration failure.
func mcpNotConfigured(configErr string) knowledge.Failure {
	if configErr != "" {
		return knowledge.Fail(knowledge.FailureNotConfigured, false,
			"mcp configuration is invalid: %s", configErr)
	}
	return knowledge.Fail(knowledge.FailureNotConfigured, false, "no MCP servers are configured")
}

// boundedSchema renders one advertised input schema as compact, bounded JSON.
// An unparsable schema is omitted: discovery must never publish malformed JSON
// as if it were a callable contract.
//
// Summary mode drops prose descriptions but keeps every field that changes a
// call: required names, types, enums, defaults, bounds, array items, and
// additionalProperties. The model needs the contract, not the marketing copy,
// and a full schema for every tool crowds an otherwise small discovery answer.
func boundedSchema(raw json.RawMessage, full bool) (string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	if !full {
		value = summarizeSchema(value)
	}
	var compact bytes.Buffer
	encoder := json.NewEncoder(&compact)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", false
	}
	text, _ := knowledge.BoundText(strings.TrimSpace(compact.String()), MaxMCPInputSchemaChars)
	return text, true
}

// summarizeSchema removes prose from a JSON Schema while preserving every
// structural keyword, including composition ($ref, anyOf, oneOf, allOf) and
// nested definitions. A summary must never validate a different contract than
// the full schema; it only costs fewer tokens.
func summarizeSchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			switch key {
			case "description", "title", "examples", "$comment", "deprecated":
				continue
			}
			out[key] = summarizeSchema(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = summarizeSchema(item)
		}
		return out
	default:
		return value
	}
}

// mcpDetails builds the bounded audit projection for one MCP call.
func mcpDetails(server string, capability knowledge.Capability, target, requestID string) map[string]any {
	return map[string]any{
		"capability": string(capability),
		"operation":  string(knowledge.OpRead),
		"server":     server,
		"target":     target,
		"request_id": requestID,
	}
}

// publishMCPResource renders a bounded resource result.
func publishMCPResource(ctx agent.Context, details map[string]any, requestID, server string, result mcp.ResourceResult) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return externalCancelledResult(ctx, details)
	}
	if result.Text == "" {
		details["status"] = "empty"
		return agent.Result{
			Text: knowledge.Envelope("capability: mcp.resource\nserver: "+singleLine(server)+
				"\nresource: "+singleLine(result.URI)+"\nstatus: empty", "(empty resource)"),
			Details: details,
			Useless: true,
		}
	}
	uri := result.URI
	if strings.TrimSpace(uri) == "" {
		if target, ok := details["target"].(string); ok {
			uri = target
		}
	}
	retrievedAt := time.Now().UTC()
	evidence := knowledge.Evidence{
		Kind:  knowledge.EvidenceMCPResource,
		Trust: knowledge.TrustUntrusted,
		Provenance: knowledge.Provenance{
			Capability:  knowledge.CapabilityMCPResource,
			Source:      "mcp:" + server,
			Locator:     uri,
			Provider:    server,
			RetrievedAt: retrievedAt,
			RequestID:   requestID,
		},
		Bound: knowledge.Bound{
			Bytes: result.Bytes, Chars: len([]rune(result.Text)),
			Items: result.Items, Truncated: result.Truncated, Reason: result.Reason,
		},
		ContentHash: knowledge.Hash(result.Text),
	}.Normalize()
	status := "ok"
	if result.Truncated {
		status = "truncated"
	}
	header := fmt.Sprintf("capability: mcp.resource\nserver: %s\nresource: %s\nmime_type: %s\nretrieved_at: %s\nstatus: %s\n",
		singleLine(server), singleLine(uri), singleLine(result.MIMEType), retrievedAt.Format(time.RFC3339), status)
	if result.Truncated {
		header += fmt.Sprintf("truncated: true (%s)\n", result.Reason)
	}
	details = mergeDetails(details, externalEvidenceDetails(
		knowledge.CapabilityMCPResource, knowledge.OpRead, requestID, []knowledge.Evidence{evidence}))
	details["status"] = status
	details["uri"] = uri
	details["bytes"] = result.Bytes
	details["truncated"] = result.Truncated
	details["retrieved_at"] = retrievedAt
	return agent.Result{Text: knowledge.Envelope(header, result.Text), Details: details}
}

// publishMCPTool renders a bounded tool result.
func publishMCPTool(ctx agent.Context, details map[string]any, requestID, server, tool string, capability knowledge.Capability, result mcp.ToolResult) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return externalCancelledResult(ctx, details)
	}
	retrievedAt := time.Now().UTC()
	operation := knowledge.OpRead
	if capability == knowledge.CapabilityMCPMutationTool {
		operation = knowledge.OpMutate
	}
	status := "ok"
	if result.IsError {
		status = "remote_tool_error"
	} else if result.Truncated {
		status = "truncated"
	}
	evidence := knowledge.Evidence{
		Kind:  knowledge.EvidenceMCPToolResult,
		Trust: knowledge.TrustUntrusted,
		Provenance: knowledge.Provenance{
			Capability:  capability,
			Source:      "mcp:" + server,
			Locator:     "tool:" + tool,
			Provider:    server,
			RetrievedAt: retrievedAt,
			RequestID:   requestID,
		},
		Bound: knowledge.Bound{
			Bytes: result.Bytes, Chars: len([]rune(result.Text)),
			Items: result.Items, Truncated: result.Truncated, Reason: result.Reason,
		},
		ContentHash: knowledge.Hash(result.Text),
	}.Normalize()
	header := fmt.Sprintf("capability: %s\nserver: %s\ntool: %s\noperation: %s\nretrieved_at: %s\nstatus: %s\n",
		capability, singleLine(server), singleLine(tool), operation, retrievedAt.Format(time.RFC3339), status)
	if result.Truncated {
		header += fmt.Sprintf("truncated: true (%s)\n", result.Reason)
	}
	details = mergeDetails(details, externalEvidenceDetails(
		capability, operation, requestID, []knowledge.Evidence{evidence}))
	details["status"] = status
	details["bytes"] = result.Bytes
	details["truncated"] = result.Truncated
	details["retrieved_at"] = retrievedAt
	if result.IsError {
		details["failure"] = knowledge.Failure{
			Class:   knowledge.FailureToolError,
			Message: "the remote MCP tool reported an error result",
		}
		details["classification"] = string(knowledge.FailureToolError)
	}
	return agent.Result{
		Text:    knowledge.Envelope(header, result.Text),
		Details: details,
		IsError: result.IsError,
	}
}

// objectArg extracts an optional object argument.
func objectArg(args map[string]any, key string) (map[string]any, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return map[string]any{}, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("argument %q must be an object", key)
	}
	return value, nil
}
