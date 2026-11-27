package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"golang.org/x/term"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/recovery"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/PizenLabs/ohgo/internal/provider/creds"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// harness is the shared backend behind slash commands in both interfaces.
//
// The full-screen UI and the plain REPL must agree on what a command means:
// a command recognised in one and ignored by the other is a bug by
// construction. Anything that needs the registry, the store, or settings
// lives here, so both paths call the same code. Presentation differs (the UI
// wraps answers in notices and pickers; the REPL prints), but decisions do
// not.
type harness struct {
	registry *ai.Registry
	loop     *agent.Loop
	store    *session.Store
	sess     *session.Session
	usage    *agent.UsageLog
	settings *config.Settings
	root     string
	model    ai.Model
	// projectID is the stable project identity the session belongs to, empty
	// when unknown. It scopes the session browser to this project without
	// changing physical storage, which stays one machine-global database.
	projectID string
	// workspace is the execution authority label, e.g. "BUILD". Empty when the
	// caller did not supply one, in which case /status omits it rather than
	// inventing an authority.
	workspace string
	// contract is the session's persistent execution authority for hosts with no
	// durable task controller. It is captured from the role the session started
	// under and only widens through a human-authorized switch, so an automatic
	// BUILD -> FUZZ phase change does not lose the mutation grant the session
	// was created with. In the full-screen host an open task's persisted
	// contract takes precedence; this field is the fallback when no task owns
	// the turn.
	contract workspace.ExecutionContract
	// gate is the live authority shared with the loop and any subagent. Nil
	// for a harness built without one (a bare test), in which case authority
	// is applied by replacing the loop's admission hook directly.
	gate *authorityGate
	// security is the loop's authorization boundary. A workspace switch moves
	// its fallback authority when there is no live gate, so the switch reaches
	// the policy even in a bare harness.
	security *securityGate
	// support is the ephemeral $sup surface. Nil means $sup is unavailable.
	support *supportSurface
	// newBudget grants a fresh root budget for one REPL turn. Nil means the
	// loop's current grant is used as-is (one-shot runs, tests).
	newBudget func() *budget.Budget
	// rec is the workspace recovery authority. Nil means the REPL was built
	// without one (tests), in which case /undo and /redo say so.
	rec *recoveryService
	// mcp is the session's one MCP manager, shared with the tool set. Nil
	// means MCP is not configured.
	mcp *mcpRuntime
	// updateNotice prints a cached update hint before the next prompt. Nil
	// disables it: tests, non-terminals, and headless runs never install or
	// announce anything. The function reads only the local cache; it never
	// performs network I/O in the prompt loop.
	updateNotice func(io.Writer)
	// reasoningFlag and effortFlag are the invocation-level overrides. They
	// are separate from settings because a flag belongs to this run, not to
	// durable role configuration; /effort auto can still re-enable demand for
	// the active role during the session.
	reasoningFlag bool
	effortFlag    string
	// autoForced records roles whose Auto mode the human selected at runtime.
	// It lets that explicit action win over an invocation flag for the rest of
	// the session while leaving the persisted role mode unchanged.
	autoForced map[string]bool
	// reasoningMu guards lastReasoningEffort and autoForced. The per-request
	// policy runs on the request goroutine while the UI reads the badge.
	reasoningMu sync.RWMutex
	// lastReasoningEffort is the level the demand selector last applied. It
	// survives a role handoff, because reasoning is independent of role, and
	// feeds the header badge without inventing a level for a disabled or
	// mandatory-reasoning model.
	lastReasoningEffort string
}

// newHarness assembles the backend. usage and settings may be nil only where
// the caller guarantees those commands are unreachable; every method guards
// its own dependencies and says so rather than panicking.
func newHarness(registry *ai.Registry, loop *agent.Loop, store *session.Store, sess *session.Session, usage *agent.UsageLog, settings *config.Settings, root string, model ai.Model) *harness {
	h := &harness{
		registry: registry, loop: loop, store: store, sess: sess,
		usage: usage, settings: settings, root: root, model: model,
	}
	if settings != nil {
		_ = registerEndpoints(registry, *settings)
	}
	if loop != nil {
		// The loop reports one cache sample per provider request. The turn
		// totals alone cannot say whether a request repeated the previous
		// prefix, which is the only part of prompt-cache behaviour Ohgo owns.
		loop.SetUsageLog(usage)
	}
	h.bindSession(sess)
	return h
}

// bindSession moves the active session identity into the runtime together
// with the harness pointer. Providers that route by conversation (OpenCode Go,
// for example) need the new identity before the next request, not at the next
// process start.
func (h *harness) bindSession(sess *session.Session) {
	h.sess = sess
	if h.loop != nil {
		h.loop.SetSessionID(sessionProviderID(sess))
	}
}

// ProviderRows lists every catalog provider with its credential status. No
// network is touched: reachability is a probe, and probes run on demand
// (see ProbeCurrent), never as a side effect of listing.
//
// Count is a fact about the merged roster (bundled plus cached live rows), and
// Configured reports whether a credential resolves. Neither is a health claim.
func (h *harness) ProviderRows() []tui.ProviderRow {
	counts := map[string]int{}
	if entries, _ := catalog.SnapshotAll(); len(entries) > 0 {
		for _, entry := range entries {
			counts[entry.Model.Provider]++
		}
	}
	var rows []tui.ProviderRow
	seen := map[string]bool{}
	appendRow := func(name string, display, driver string) {
		if seen[name] {
			return
		}
		seen[name] = true
		row := tui.ProviderRow{
			ID:     name,
			Name:   display,
			Driver: driver,
			Count:  counts[name],
		}
		_, source, err := h.registry.ResolveAPIKeySource(name)
		switch {
		case err == nil:
			row.KeyDetail = source
			row.Configured = true
		case name == "ollama":
			row.KeyDetail = "none required"
			row.Configured = true
		default:
			row.KeyDetail = "missing: " + keyHint(err)
		}
		rows = append(rows, row)
	}
	for _, name := range catalog.ProviderNames() {
		appendRow(name, providerDisplayName(name), providerDriver(name))
	}
	return rows
}

// providerDriver labels the wire protocol a provider is reached through, the
// same protocol providerAPI routes requests to. It is display-only: the roster
// shows the transport so a user can tell an Anthropic endpoint from an
// OpenAI-compatible one at a glance.
func providerDriver(provider string) string {
	switch providerAPI(provider) {
	case ai.APIAnthropicMessages:
		return "Anthropic Messages"
	case ai.APIBedrockConverse:
		return "Bedrock Converse"
	case ai.APIOllamaChat:
		return "Ollama Chat"
	default:
		return "OpenAI Chat"
	}
}

// providerAPI resolves the wire protocol for a provider: the same selection
// validateProvider probes with, kept in one place so the roster's TYPE column
// and the connect probe can never disagree about how a provider is reached.
func providerAPI(provider string) ai.API {
	switch provider {
	case "ollama", "ollama-cloud":
		return ai.APIOllamaChat
	case "anthropic":
		return ai.APIAnthropicMessages
	case "bedrock":
		return ai.APIBedrockConverse
	default:
		return ai.APIOpenAIChatCompletions
	}
}

// ProviderPaneRows is the model control plane's provider pane: the real
// providers preceded by a synthetic All row that disables provider filtering,
// so a single search can span the whole roster.
func (h *harness) ProviderPaneRows() []tui.ProviderRow {
	rows := h.ProviderRows()
	total := 0
	for _, row := range rows {
		total += row.Count
	}
	customModels := 0
	customConfigured := false
	for _, endpoint := range h.settingsOrDefault().Endpoints {
		customModels += len(endpoint.Models)
		customConfigured = true
	}
	total += customModels
	all := tui.ProviderRow{ID: tui.AllProviders, Name: "All providers", Count: total}
	custom := tui.ProviderRow{
		ID:         tui.CustomProvider,
		Name:       tui.CustomProviderName,
		Driver:     "OpenAI Chat",
		Count:      customModels,
		Configured: customConfigured,
		Custom:     true,
		KeyDetail:  "not configured",
	}
	if customConfigured {
		custom.KeyDetail = "configured"
	}
	return append(append([]tui.ProviderRow{all}, rows...), custom)
}

// EndpointOptions projects the global configuration's endpoints for the model
// control plane. It reads the authority, not a derived cache, so a hand edit
// and an interactive save produce the same view.
func (h *harness) EndpointOptions() []tui.EndpointOption {
	settings := h.settingsOrDefault()
	keys := make([]string, 0, len(settings.Endpoints))
	for key := range settings.Endpoints {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]tui.EndpointOption, 0, len(keys))
	for _, key := range keys {
		endpoint := settings.Endpoints[key]
		automatic := false
		if len(endpoint.Models) > 0 {
			automatic = endpoint.FactsAutomatic || endpoint.ContextWindow <= 0 || endpoint.MaxTokens <= 0
			endpoint.EnsureModelFacts()
		}
		out = append(out, tui.EndpointOption{
			Key:            key,
			Name:           endpoint.DisplayName(key),
			BaseURL:        endpoint.BaseURL,
			APIKeyEnv:      endpoint.APIKeyEnv,
			Models:         append([]string(nil), endpoint.Models...),
			ContextWindow:  endpoint.ContextWindow,
			MaxTokens:      endpoint.MaxTokens,
			FactsAutomatic: automatic,
		})
	}
	return out
}

// SaveEndpoint validates one endpoint, persists it atomically in the global
// settings file, and refreshes the registry the catalog is derived from.
//
// The map key is identity: a new endpoint derives it from the display name,
// and an edit keeps it, so a rename never changes model references. Duplicate
// keys or display names are refused rather than silently overwriting.
func (h *harness) SaveEndpoint(draft tui.EndpointDraft) ([]tui.EndpointOption, error) {
	if h.settings == nil {
		return nil, fmt.Errorf("configuration is unavailable")
	}
	name := strings.TrimSpace(draft.Name)
	if name == "" {
		return nil, fmt.Errorf("endpoint name is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(draft.BaseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	apiKeyEnv := strings.TrimSpace(draft.APIKeyEnv)
	key := strings.TrimSpace(draft.Key)
	next := cloneSettings(*h.settings)
	if next.Endpoints == nil {
		next.Endpoints = map[string]config.Endpoint{}
	}
	existing, editing := next.Endpoints[key]
	if key == "" {
		key = config.EndpointKey(name)
		if key == "" {
			return nil, fmt.Errorf("endpoint name needs at least one letter or digit")
		}
	}
	if key == tui.CustomProvider {
		return nil, fmt.Errorf("%q is reserved for the Custom row; choose another name", tui.CustomProvider)
	}
	for _, builtin := range catalog.ProviderNames() {
		if key == builtin {
			return nil, fmt.Errorf("%q is a built-in provider; choose another name", key)
		}
	}
	if !editing {
		if _, clash := next.Endpoints[key]; clash {
			return nil, fmt.Errorf("an endpoint named %q already exists", key)
		}
	}
	for otherKey, other := range next.Endpoints {
		if otherKey == key {
			continue
		}
		if strings.EqualFold(other.DisplayName(otherKey), name) {
			return nil, fmt.Errorf("an endpoint named %q already exists", name)
		}
	}

	models := make([]string, 0, len(draft.Models))
	seen := map[string]bool{}
	for _, id := range draft.Models {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if strings.ContainsAny(id, " \t") {
			return nil, fmt.Errorf("model id %q contains whitespace", id)
		}
		seen[id] = true
		models = append(models, id)
	}
	sort.Strings(models)

	endpoint := existing
	endpoint.Name = name
	endpoint.BaseURL = baseURL
	endpoint.APIKeyEnv = apiKeyEnv
	endpoint.Models = models
	if len(models) > 0 {
		endpoint.EnsureModelFacts()
	}
	if err := endpoint.Validate(key); err != nil {
		return nil, err
	}
	next.Endpoints[key] = endpoint
	if editing {
		keep := map[string]bool{}
		for _, id := range models {
			keep[id] = true
		}
		pruneEndpointReferences(&next, key, keep)
	}
	if err := config.Save(next); err != nil {
		return nil, fmt.Errorf("save endpoint: %w", err)
	}

	previous := h.settings.Endpoints
	*h.settings = next
	h.syncEndpointRegistry(previous, next)
	return h.EndpointOptions(), nil
}

// DeleteEndpoint removes one endpoint from the global configuration and
// clears role and workspace references that would otherwise dangle.
func (h *harness) DeleteEndpoint(key string) ([]tui.EndpointOption, error) {
	if h.settings == nil {
		return nil, fmt.Errorf("configuration is unavailable")
	}
	key = strings.TrimSpace(key)
	next := cloneSettings(*h.settings)
	if _, ok := next.Endpoints[key]; !ok {
		return nil, fmt.Errorf("no endpoint %q", key)
	}
	delete(next.Endpoints, key)
	pruneEndpointReferences(&next, key, nil)
	if err := config.Save(next); err != nil {
		return nil, fmt.Errorf("delete endpoint: %w", err)
	}
	previous := h.settings.Endpoints
	*h.settings = next
	h.syncEndpointRegistry(previous, next)
	return h.EndpointOptions(), nil
}

// ProbeEndpoint checks one draft endpoint without persisting anything. The
// credential comes from the named environment variable; an empty name is a
// keyless local server.
func (h *harness) ProbeEndpoint(ctx context.Context, draft tui.EndpointDraft) tui.EndpointProbeResult {
	baseURL := strings.TrimRight(strings.TrimSpace(draft.BaseURL), "/")
	env := strings.TrimSpace(draft.APIKeyEnv)
	key := ""
	if env != "" {
		key = os.Getenv(env)
	}
	result := ai.ProbeProvider(ctx, nil, ai.APIOpenAIChatCompletions, baseURL, key)
	out := tui.EndpointProbeResult{
		Reachable:     result.Reachable,
		Authenticated: result.Authenticated,
		Protocol:      result.Protocol,
		Models:        result.Models,
		ModelIDs:      append([]string(nil), result.ModelIDs...),
		Detail:        result.Detail,
	}
	if env != "" && key == "" {
		detail := strings.Replace(result.Detail, "credential accepted, ", "", 1)
		out.Detail = env + " is not set; probed without a credential; " + detail
	}
	return out
}

// syncEndpointRegistry removes every endpoint the previous configuration
// declared and registers the new set, so a deleted model or changed base URL
// cannot survive in a cached provider instance.
func (h *harness) syncEndpointRegistry(previous map[string]config.Endpoint, next config.Settings) {
	if h.registry == nil {
		return
	}
	for key := range previous {
		h.registry.RemoveEndpoint(key)
	}
	for key := range next.Endpoints {
		h.registry.RemoveEndpoint(key)
	}
	_ = registerEndpoints(h.registry, next)
}

// cloneSettings deep-copies the maps and slices an endpoint edit mutates.
func cloneSettings(settings config.Settings) config.Settings {
	out := settings
	out.Models = map[string]string{}
	for key, value := range settings.Models {
		out.Models[key] = value
	}
	out.Roles = map[string]config.Profile{}
	for key, value := range settings.Roles {
		out.Roles[key] = value
	}
	out.Endpoints = map[string]config.Endpoint{}
	for key, value := range settings.Endpoints {
		value.Models = append([]string(nil), value.Models...)
		value.Input = append([]string(nil), value.Input...)
		value.ReasoningEfforts = append([]string(nil), value.ReasoningEfforts...)
		out.Endpoints[key] = value
	}
	return out
}

// pruneEndpointReferences clears role/workspace/global references to models
// that the endpoint no longer serves. A nil keep means the endpoint is gone.
// Leaving these strings behind would make a deleted endpoint look configured
// while resolution silently fell through to a different model.
func pruneEndpointReferences(settings *config.Settings, key string, keep map[string]bool) {
	prefix := key + "/"
	valid := func(ref string) bool {
		if !strings.HasPrefix(ref, prefix) {
			return true
		}
		if keep == nil {
			return false
		}
		return keep[strings.TrimPrefix(ref, prefix)]
	}
	for role, profile := range settings.Roles {
		if valid(profile.Model) {
			continue
		}
		profile.Model = ""
		profile.Effort = ""
		settings.Roles[role] = profile
	}
	for workspace, ref := range settings.Models {
		if !valid(ref) {
			delete(settings.Models, workspace)
		}
	}
	if !valid(settings.Model) {
		settings.Model = ""
	}
}

// RoleAssignments reports the effective model profile for every role, with the
// authoritative capability facts of each bound model. It is read-only: the
// control plane renders it and hands edits back through AssignRole.
func (h *harness) RoleAssignments() []tui.RoleAssignment {
	facts := map[string]catalog.SnapshotEntry{}
	for _, entry := range h.snapshotEntries() {
		facts[entry.Model.Ref()] = entry
	}
	for _, model := range endpointModels(h.registry) {
		facts[model.Ref()] = catalog.SnapshotEntry{
			Model:          model,
			Origin:         "bundled",
			Thinking:       &catalog.Thinking{Mode: "effort", Efforts: append([]string(nil), model.ReasoningEfforts...)},
			ReasoningKnown: model.Reasoning || len(model.ReasoningEfforts) > 0,
		}
	}
	settings := h.settingsOrDefault()
	var out []tui.RoleAssignment
	for _, role := range config.RoleNames() {
		ref, effort, source := settings.RoleModel(string(role))
		assignment := h.roleAssignment(string(role), ref, effort, source, facts)
		assignment.Auto = h.demandActive(string(role))
		assignment.EffortMode = h.effortMode(string(role))
		out = append(out, assignment)
	}
	return out
}

// snapshotEntries returns the merged roster, tolerating an empty snapshot.
func (h *harness) snapshotEntries() []catalog.SnapshotEntry {
	entries, _ := catalog.SnapshotAll()
	return entries
}

// EffortAssignment reports the role target for /effort. Unlike the broader
// model-control roster, the session's own role uses the live model the next
// request will call, so a --model/resume override cannot leave the card
// offering levels the runtime will refuse.
func (h *harness) EffortAssignment(role string) tui.RoleAssignment {
	name := config.NormalizeRole(role)
	settings := h.settingsOrDefault()
	ref, effort, source := settings.RoleModel(string(name))
	model, err := h.effortModel(name)
	if err != nil {
		// Keep the configured reference visible. Substituting the catalog
		// default here would name a model the role does not call and could
		// make the card look valid for the wrong ladder.
		return tui.RoleAssignment{
			Role: string(name), Model: ref, Effort: effort, Source: source,
			Auto: h.demandActive(string(name)), EffortMode: h.effortMode(string(name)),
		}
	}
	if h.activeRole() == name && h.model.Ref() != "" {
		model = h.model
		if strings.TrimSpace(ref) == "" || model.Ref() != ref {
			source = "session"
		}
	}
	assignment := tui.RoleAssignment{
		Role:       string(name),
		Model:      model.Ref(),
		Provider:   model.Provider,
		Name:       model.Name,
		Effort:     effort,
		Source:     source,
		Auto:       h.demandActive(string(name)),
		EffortMode: h.effortMode(string(name)),
		Reasoning:  model.Reasoning,
		Efforts:    append([]string(nil), model.ReasoningEfforts...),
	}
	assignment.ReasoningKnown = model.Reasoning || len(assignment.Efforts) > 0
	// Bundled rows merge discovered facts, which is the same ladder
	// effortSupported validates against. A configured endpoint's model facts
	// are already authoritative and must not be replaced by a bundled row.
	if !h.customModel(model.Ref()) {
		if thinking := catalog.ThinkingFor(model.Ref()); thinking != nil {
			assignment.Efforts = append([]string(nil), thinking.Efforts...)
			assignment.ReasoningKnown = true
		}
	}
	if h.sessionRole() == string(name) && h.sess != nil {
		assignment.SessionModel = h.sess.Model
	}
	return assignment
}

// customModel reports whether the reference belongs to a configured endpoint.
func (h *harness) customModel(ref string) bool {
	if h.registry == nil {
		return false
	}
	_, handled, err := h.registry.CustomModel(ref)
	return handled && err == nil
}

// roleAssignment builds one role's display row from its effective reference.
func (h *harness) roleAssignment(role, ref, effort, source string, facts map[string]catalog.SnapshotEntry) tui.RoleAssignment {
	assignment := tui.RoleAssignment{Role: role, Model: ref, Effort: effort, Source: source}
	if strings.TrimSpace(ref) == "" {
		assignment.Source = "builtin"
	} else if provider, _, ok := strings.Cut(ref, "/"); ok {
		assignment.Provider = provider
		if entry, found := facts[ref]; found {
			assignment.Name = entry.Model.Name
			assignment.Efforts = thinkingEfforts(entry.Thinking)
			assignment.Reasoning = entry.Model.Reasoning
			assignment.ReasoningKnown = entry.ReasoningKnown
		} else if model, handled, err := h.registry.CustomModel(ref); handled && err == nil {
			assignment.Name = model.Name
			assignment.Reasoning = model.Reasoning
			assignment.ReasoningKnown = model.Reasoning || len(model.ReasoningEfforts) > 0
			assignment.Efforts = append([]string(nil), model.ReasoningEfforts...)
		} else if model, err := catalog.Resolve(ref); err == nil {
			assignment.Name = model.Name
			assignment.Reasoning = model.Reasoning
			assignment.ReasoningKnown = true
			assignment.Efforts = thinkingEfforts(catalog.ThinkingFor(ref))
		}
	}
	if h.sessionRole() == role && h.sess != nil {
		assignment.SessionModel = h.sess.Model
	}
	return assignment
}

// sessionRole names the role the running session resolved, derived from the
// workspace label so a resumed session's snapshot is attributed to the right
// role.
func (h *harness) sessionRole() string {
	return strings.ToLower(strings.TrimSpace(h.workspace))
}

// activeRole names the role whose model the session is actually calling. An
// unset workspace label falls back to the parsed authority, so a bare harness
// still targets the workspace it runs under instead of an empty role.
func (h *harness) activeRole() config.Role {
	if role := config.NormalizeRole(h.sessionRole()); config.ValidRole(role) {
		return role
	}
	return config.NormalizeRole(h.activeWorkspace().String())
}

// AssignRole validates, persists, and (for the session's own role) applies a
// role assignment. It is the single seam /model writes through: the plane
// never touches configuration or the loop itself.
//
// Validation is against authoritative metadata: an effort the bound model does
// not expose is refused rather than stored, so a persisted profile can never
// ask a provider for something it rejects.
func (h *harness) AssignRole(update tui.RoleAssignment) (tui.RoleAssignment, []tui.RoleAssignment, error) {
	role := config.NormalizeRole(update.Role)
	if !config.ValidRole(role) {
		return tui.RoleAssignment{}, h.RoleAssignments(), fmt.Errorf("unknown role %q", update.Role)
	}
	if h.settings == nil {
		return tui.RoleAssignment{}, h.RoleAssignments(), fmt.Errorf("configuration is unavailable")
	}
	ref := strings.TrimSpace(update.Model)
	if ref == "" {
		return tui.RoleAssignment{}, h.RoleAssignments(), fmt.Errorf("no model selected for %s", role)
	}
	model, err := dynamicResolver(context.Background(), h.registry)(ref)
	if err != nil {
		return tui.RoleAssignment{}, h.RoleAssignments(), err
	}
	canonical := model.Ref()
	effort := strings.TrimSpace(update.Effort)
	if effort != "" && effort != tui.EffortOff && !h.effortSupported(canonical, model, effort) {
		return tui.RoleAssignment{}, h.RoleAssignments(),
			fmt.Errorf("%s does not expose thinking effort %q", canonical, effort)
	}
	h.settings.AssignRole(string(role), canonical, effort)
	if err := config.Save(*h.settings); err != nil {
		return tui.RoleAssignment{}, h.RoleAssignments(), fmt.Errorf("save configuration: %w", err)
	}
	h.reasoningMu.Lock()
	delete(h.autoForced, string(role))
	h.reasoningMu.Unlock()
	if h.sessionRole() == string(role) {
		h.applyModel(model)
		// An empty effort with demand active means the per-request policy
		// owns the next request; do not replace it with the global default.
		// An explicit effort, or a non-demand policy, applies immediately.
		if effort != "" || !h.demandActive(string(role)) {
			enabled, level := h.nonDemandReasoning()
			if effort != "" {
				enabled, level = h.reasoningFor(effort)
			}
			if h.loop != nil {
				h.loop.SetReasoning(enabled, level)
			}
		}
		h.recordSessionAuthority(model)
	}
	for _, assignment := range h.RoleAssignments() {
		if assignment.Role == string(role) {
			return assignment, h.RoleAssignments(), nil
		}
	}
	return tui.RoleAssignment{Role: string(role), Model: canonical, Effort: effort}, h.RoleAssignments(), nil
}

// SetEffort validates, persists, and applies a thinking level to one role
// without rebinding its model. It is the single seam /effort writes through.
//
// The level is validated against the model the role will actually call: the
// live model for the session's own role, otherwise the role's resolved
// profile. "off" disables thinking. "auto" persists a demand-driven role mode
// and records a runtime choice that wins over an invocation flag for this
// session; "default"/"unset" clear the role to the global policy.
func (h *harness) SetEffort(role, effort string) (tui.RoleAssignment, []tui.RoleAssignment, error) {
	name := config.NormalizeRole(role)
	if !config.ValidRole(name) {
		return tui.RoleAssignment{}, h.RoleAssignments(), fmt.Errorf("unknown role %q", role)
	}
	if h.settings == nil {
		return tui.RoleAssignment{}, h.RoleAssignments(), fmt.Errorf("configuration is unavailable")
	}
	requested := strings.ToLower(strings.TrimSpace(effort))
	autoMode := requested == tui.EffortAuto
	level := normalizeEffortSetting(effort)
	model, err := h.effortModel(name)
	if err != nil {
		return tui.RoleAssignment{}, h.RoleAssignments(), err
	}
	canonical := model.Ref()
	if !autoMode && level != "" && level != tui.EffortOff && !h.effortSupported(canonical, model, level) {
		return tui.RoleAssignment{}, h.RoleAssignments(),
			fmt.Errorf("%s does not expose thinking effort %q", canonical, level)
	}
	if autoMode {
		h.settings.AssignAuto(string(name), "")
	} else {
		h.settings.AssignRole(string(name), "", level)
	}
	if err := config.Save(*h.settings); err != nil {
		return tui.RoleAssignment{}, h.RoleAssignments(), fmt.Errorf("save configuration: %w", err)
	}
	h.reasoningMu.Lock()
	if autoMode {
		if h.autoForced == nil {
			h.autoForced = map[string]bool{}
		}
		h.autoForced[string(name)] = true
		if h.activeRole() == name {
			h.lastReasoningEffort = ""
		}
	} else {
		delete(h.autoForced, string(name))
		if h.activeRole() == name {
			h.lastReasoningEffort = ""
		}
	}
	h.reasoningMu.Unlock()
	if h.activeRole() == name && !autoMode {
		// An explicit level, including "off", is a human override and takes
		// effect now. Auto has no level to apply: the per-request policy owns
		// the next request and selects from the work.
		enabled, setting := h.reasoningFor(level)
		if h.loop != nil {
			h.loop.SetReasoning(enabled, setting)
		}
	}
	return h.EffortAssignment(string(name)), h.RoleAssignments(), nil
}

// normalizeEffortSetting canonicalizes a /effort argument to a fixed level.
// "auto", "default", and "unset" have no level; SetEffort distinguishes Auto
// as a persisted mode before calling this, while the other spellings clear the
// role back to the global policy.
func normalizeEffortSetting(effort string) string {
	level := strings.ToLower(strings.TrimSpace(effort))
	switch level {
	case tui.EffortAuto, "default", "unset":
		return ""
	}
	return level
}

// recordSessionAuthority is the single writer for the session's authority
// snapshot: the workspace and model that will actually execute.
//
// Every path that changes either — an explicit role reassignment, a model
// switch, a workspace switch — records it here, so a later --resume restores
// what ran rather than what configuration happened to say. It mirrors
// applySessionAuthority: a storage failure is reported but never fails the
// transition, because the runtime state is still correct in memory.
func (h *harness) recordSessionAuthority(model ai.Model) {
	effective := model.Ref()
	if h.sess != nil {
		h.sess.Model = effective
	}
	if h.store != nil && h.sess != nil {
		if err := h.store.SetAuthority(h.sess.ID, strings.ToLower(h.workspace), effective); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not record session authority: %v\n", err)
		}
	}
}

// reasoningFor maps a persisted effort to the loop's thinking settings. An
// empty effort defers to the global default; "off" disables thinking outright.
func (h *harness) reasoningFor(effort string) (bool, string) {
	switch effort {
	case tui.EffortOff:
		return false, ""
	case "":
		settings := h.settingsOrDefault()
		if strings.EqualFold(strings.TrimSpace(settings.ReasoningEffort), "off") {
			return false, ""
		}
		return settings.Reasoning, settings.ReasoningEffort
	default:
		return true, effort
	}
}

// thinkingEfforts copies a catalog thinking ladder into the UI-shaped slice.
func thinkingEfforts(thinking *catalog.Thinking) []string {
	if thinking == nil {
		return nil
	}
	return append([]string(nil), thinking.Efforts...)
}

// supportsEffort reports whether the resolved model exposes an effort. It
// reads the model's own ladder, so a configured endpoint and a catalog model
// are validated by the same contract.
func supportsEffort(model ai.Model, effort string) bool {
	for _, candidate := range model.ReasoningEfforts {
		if candidate == effort {
			return true
		}
	}
	return false
}

// effortSupported preserves the catalog's merged-roster ladder for bundled
// providers while reading the configured facts for an endpoint provider.
func (h *harness) effortSupported(ref string, model ai.Model, effort string) bool {
	if h.registry != nil {
		if _, handled, err := h.registry.CustomModel(ref); handled && err == nil {
			return supportsEffort(model, effort)
		}
	}
	return catalog.ThinkingFor(ref).Supports(effort)
}

// effortModel resolves the model a role's thinking level must be valid for.
// The session's own role uses the live model, because that is what the next
// request calls even when a flag overrode configuration. Other roles resolve
// through the canonical role chain so an edit validates against the model that
// role would start with.
func (h *harness) effortModel(role config.Role) (ai.Model, error) {
	if h.activeRole() == role && h.model.Ref() != "" {
		return h.model, nil
	}
	ref, _, _ := h.settingsOrDefault().RoleModel(string(role))
	if strings.TrimSpace(ref) == "" {
		return catalog.Default(), nil
	}
	resolve := catalog.Resolve
	if h.registry != nil {
		resolve = dynamicResolver(context.Background(), h.registry)
	}
	return resolve(ref)
}

// applyModel moves the loop, the ledger pricing, and the harness model
// together. Splitting those updates would leave the status line naming a model
// the loop is not calling, or value new tokens at old prices.
func (h *harness) applyModel(model ai.Model) {
	if h.loop != nil {
		h.loop.SetModel(model)
	}
	h.model = model
	if h.usage != nil {
		h.usage.SetCost(model.Cost)
	}
	// A level selected for the previous model or work is not a claim about the
	// next request, even when the new model happens to publish the same name.
	// Drop it; the per-request policy will record the real selection before a
	// request is sent, and until then the badge is honestly unknown.
	if h.lastReasoningLevel() != "" {
		h.clearLastReasoning()
	}
}

// providerDisplayName names a provider for humans from the catalog's own
// metadata. An undescribed key shows as itself rather than a guess.
func providerDisplayName(name string) string {
	if display, ok := catalog.Describe(name); ok {
		return display
	}
	return name
}

// keyHint extracts the actionable tail of a missing-credential error ("set
// X or Y") so listings stay one line.
func keyHint(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "(set "); i >= 0 {
		return strings.TrimSuffix(msg[i+len("(set "):], ")")
	}
	return "no credential"
}

// ProbeCurrent checks the active model's endpoint without spending a
// completion. Reachability and credential acceptance are all it claims;
// tool-call health needs a real turn, which it says rather than implies.
func (h *harness) ProbeCurrent(ctx context.Context) tui.ProbeFinished {
	baseURL := h.registry.EndpointFor(h.model)
	key, _ := h.registry.ResolveAPIKey(h.model.Provider)
	result := ai.ProbeProvider(ctx, nil, h.model.API, baseURL, key)
	return tui.ProbeFinished{OK: result.Reachable && result.Authenticated, Detail: result.Detail}
}

// StatusReport renders /status from local state only.
func (h *harness) StatusReport(history []agent.Message, queued int, theme string, busy bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "model: %s/%s\n", h.model.Provider, h.model.ID)
	if h.model.ContextWindow > 0 {
		fmt.Fprintf(&sb, "context window: %s tokens\n", formatInt(h.model.ContextWindow))
	}
	fmt.Fprintf(&sb, "turns: %d", len(history))
	if h.sess != nil {
		fmt.Fprintf(&sb, " · session #%d", h.sess.ID)
	}
	if h.workspace != "" {
		fmt.Fprintf(&sb, " · workspace %s", h.workspace)
	}
	if queued > 0 {
		fmt.Fprintf(&sb, " · %d queued", queued)
	}
	if busy {
		sb.WriteString(" · working")
	}
	fmt.Fprintf(&sb, "\ntheme: %s", theme)
	return sb.String()
}

// UsageReport renders /usage from the shared ledger plus the provider roster,
// mirroring the full-screen inspector's two sections so both interfaces agree
// on what the command reports.
func (h *harness) UsageReport() string {
	var sb strings.Builder
	if h.usage == nil {
		sb.WriteString("usage tracking is unavailable")
	} else {
		sb.WriteString(h.usage.Report(false))
	}
	rows := h.ProviderRows()
	if len(rows) > 0 {
		sb.WriteString("\nproviders:")
		for _, row := range rows {
			marker, status := "[×]", "missing API key"
			if row.Configured {
				marker, status = "[✓]", "configured"
			}
			fmt.Fprintf(&sb, "\n  %s %-14s %s", marker, row.ID, status)
		}
	}
	return sb.String()
}

// DoctorLines runs the same diagnostics as `ohgo doctor`, minus the workdir
// row the CLI adds around them. Args select the same drill-downs as the CLI
// flags. One implementation serves both: a check that passes in one interface
// and fails in the other would be a lie in one of them.
func (h *harness) DoctorLines(args []string) []tui.DoctorLine {
	view, err := parseDoctorViewArgs(args)
	if err != nil {
		return []tui.DoctorLine{{
			Section: "Diagnostics", Name: "doctor", Status: "ERROR", Detail: err.Error(),
		}}
	}
	registry := h.registry
	if registry == nil {
		registry = ai.NewRegistry()
	}
	diagnosis := collectDiagnosis(h.model.Ref(), registry)

	if view.JSON {
		var document strings.Builder
		if err := renderDoctorJSON(&document, diagnosis, view); err != nil {
			return []tui.DoctorLine{{
				Section: "Diagnostics", Name: "json", Status: "ERROR", Detail: err.Error(),
			}}
		}
		return []tui.DoctorLine{{Raw: true, Detail: document.String()}}
	}

	out := make([]tui.DoctorLine, 0, len(diagnosis.Reports))
	for _, rep := range diagnosis.Reports {
		out = append(out, tui.DoctorLine{
			Section: string(rep.Section),
			Name:    rep.Name,
			Status:  string(rep.Status),
			Detail:  rep.detail(view.Verbose),
		})
	}
	for _, remediation := range collectRecommendations(diagnosis.Reports) {
		out = append(out, tui.DoctorLine{
			Section:       string(doctorSectionRecommendations),
			Name:          remediation.Name,
			Status:        string(doctorStatusOptional),
			Detail:        remediation.Detail,
			Informational: true,
		})
	}
	if view.Tools {
		out = append(out, doctorToolLines(diagnosis.Tools)...)
	}
	if view.Intelligence {
		out = append(out, doctorIntelligenceLines(diagnosis.Intelligence)...)
	}
	if view.compact() {
		out = append(out, tui.DoctorLine{Hint: true, Informational: true, Detail: doctorHintText})
	}
	return out
}

// doctorToolLines projects the live tool detail into TUI rows.
func doctorToolLines(tools []doctorTool) []tui.DoctorLine {
	if len(tools) == 0 {
		return []tui.DoctorLine{{
			Section: "Registered Tools", Name: "tools", Status: "OPTIONAL",
			Detail: "no tools registered", Informational: true,
		}}
	}
	out := make([]tui.DoctorLine, 0, len(tools)+len(doctorToolGroupOrder))
	for _, group := range groupToolDetails(tools) {
		section := "Tools · " + group.Name
		for _, tool := range group.Tools {
			out = append(out, tui.DoctorLine{
				Section: section, Name: tool.Name, Status: tool.Safety,
				Detail: tool.Backend + " · " + tool.Purpose, Informational: true,
			})
		}
	}
	return out
}

// doctorIntelligenceLines projects the intelligence detail into TUI rows.
func doctorIntelligenceLines(detail doctorIntelligence) []tui.DoctorLine {
	if detail.Error != "" {
		return []tui.DoctorLine{{
			Section: "Code Intelligence Detail", Name: "inventory", Status: "WARN",
			Detail: detail.Error, Informational: true,
		}}
	}
	out := make([]tui.DoctorLine, 0, len(detail.Adapters)+len(detail.Languages)+len(detail.Unsupported)+3)
	for _, adapter := range detail.Adapters {
		out = append(out, tui.DoctorLine{
			Section: "Structural Adapters", Name: adapter.Name, Status: "adapter",
			Detail: fmt.Sprintf("languages: %s; extensions: %s; facts: %s",
				compactList(adapter.Languages, 3),
				compactList(adapter.Extensions, 3),
				compactList(adapter.Capabilities, 8)),
			Informational: true,
		})
	}
	for _, mapping := range detail.Languages {
		server := mapping.Server
		if server == "" {
			server = "-"
		} else if mapping.Path != "" {
			server += " (" + compactPath(mapping.Path, 36) + ")"
		}
		out = append(out, tui.DoctorLine{
			Section: "Workspace Languages", Name: mapping.Language, Status: mapping.Status,
			Detail:        fmt.Sprintf("%d file(s); %s; structural: %s", mapping.Files, server, mapping.Structural),
			Informational: true,
		})
	}
	for _, unsupported := range detail.Unsupported {
		out = append(out, tui.DoctorLine{
			Section: "Unsupported Extensions", Name: unsupported.Extension, Status: "unsupported",
			Detail: fmt.Sprintf("%d file(s)", unsupported.Files), Informational: true,
		})
	}
	return out
}

func (h *harness) settingsOrDefault() config.Settings {
	if h.settings == nil {
		return config.Default()
	}
	return *h.settings
}

// SwitchModel resolves a reference (static, then live discovery) and moves
// the loop, the ledger pricing, and the harness model together. Splitting
// those updates would leave the status line naming a model the loop is not
// calling, or value new tokens at old prices.
func (h *harness) SwitchModel(ctx context.Context, ref string) (ai.Model, error) {
	resolve := dynamicResolver(ctx, h.registry)
	model, err := resolve(ref)
	if err != nil {
		return ai.Model{}, err
	}
	h.applyModel(model)
	return model, nil
}

// SetModel switches models and records the choice in the session's role.
//
// It is the plain REPL's /model: it converges on the same configuration
// authority the control plane writes through, so an interactive switch and a
// hand-edited file agree. A harness without settings (a bare test harness)
// still switches, it just has nowhere durable to record it.
func (h *harness) SetModel(ref string) (ai.Model, error) {
	resolve := catalog.Resolve
	if h.registry != nil {
		resolve = dynamicResolver(context.Background(), h.registry)
	}
	model, err := resolve(ref)
	if err != nil {
		return ai.Model{}, err
	}
	h.applyModel(model)
	if h.settings == nil {
		return model, nil
	}
	role := h.sessionRole()
	if !config.ValidRole(config.NormalizeRole(role)) {
		role = string(config.RoleBuild)
	}
	canonical := model.Ref()
	// A model switch must not carry an effort the new model does not expose:
	// the incompatible claim is dropped instead of persisted to fail at
	// invocation time. A still-valid effort survives the switch, and Auto
	// survives because it is a mode rather than a level.
	auto := h.settings.RoleAuto(role)
	effort := h.roleEffortFor(role)
	if effort != "" && effort != tui.EffortOff && !h.effortSupported(canonical, model, effort) {
		effort = ""
	}
	if auto && effort == "" {
		h.settings.SetRoleModel(role, canonical)
	} else {
		h.settings.AssignRole(role, canonical, effort)
	}
	if err := config.Save(*h.settings); err != nil {
		return model, fmt.Errorf("save configuration: %w", err)
	}
	if effort != "" || !h.demandActive(role) {
		enabled, level := h.nonDemandReasoning()
		if effort != "" {
			enabled, level = h.reasoningFor(effort)
		}
		if h.loop != nil {
			h.loop.SetReasoning(enabled, level)
		}
	}
	if role == h.sessionRole() {
		h.recordSessionAuthority(model)
	}
	return model, nil
}

// roleEffortFor returns a role's currently persisted explicit effort.
func (h *harness) roleEffortFor(role string) string {
	if h.settings == nil {
		return ""
	}
	_, effort, _ := h.settings.RoleModel(role)
	return strings.TrimSpace(effort)
}

// currentEffort reports the level the next request on the active role would
// use, with the same precedence the per-request policy applies. It is the
// single projection of role effort, role Auto, invocation flags, and global
// policy, so the header badge, /effort, and the plain REPL cannot disagree.
func (h *harness) currentEffort() string {
	role := string(h.activeRole())
	if explicit := h.roleEffortFor(role); explicit != "" {
		enabled, level := h.reasoningFor(explicit)
		if !enabled {
			return ""
		}
		return level
	}
	if h.demandActive(role) {
		level := h.lastReasoningLevel()
		if level == "" {
			return ""
		}
		// A handoff or model switch can outrun the next policy call. Never
		// report a level the live model does not publish. The model facts are
		// authoritative; the catalog fallback covers merged bundled rosters.
		for _, candidate := range h.model.ReasoningEfforts {
			if candidate == level {
				return level
			}
		}
		if h.effortSupported(h.model.Ref(), h.model, level) {
			return level
		}
		return ""
	}
	enabled, level := h.nonDemandReasoning()
	if !enabled {
		return ""
	}
	return level
}

// CompactHistory runs one manual /compact through the same agent.CompactWith
// pipeline automatic compaction uses.
//
// The request carries the Program's authoritative projection. When it is
// present, CompactWith performs a state-first reconstruction and never calls
// the summarizer. The semantic fallback is built here only because a task
// without durable state still needs a path; it charges the loop's existing
// model-call grant before opening a stream.
func (h *harness) CompactHistory(ctx context.Context, req tui.CompactionRequest) (agent.CompactResult, error) {
	if len(req.History) == 0 {
		return agent.CompactResult{}, fmt.Errorf("nothing to compact: the transcript is empty")
	}
	window := h.model.ContextWindow
	if window <= 0 {
		return agent.CompactResult{}, fmt.Errorf("compact: the model has no context window to compact into")
	}
	settings := h.settingsOrDefault()
	policy := compactionFromSettings(settings)
	if h.loop != nil {
		policy = h.loop.CompactionPolicy()
	}
	opts := agent.CompactOptions{
		Compaction: policy,
		Window:     window,
	}

	var measure agent.RequestMeasurement
	if h.loop != nil {
		measure = h.loop.MeasureCompaction(req.History, req.Projection, req.HasProjection)
		opts.Recall = h.loop.Recall()
	} else {
		taskTokens := 0
		if req.HasProjection {
			taskTokens = agent.EstimateMessages([]agent.Message{
				agent.UserMessage{Text: req.Projection.Render()},
			})
		}
		measure = agent.MeasureRequest(agent.MeasureInput{
			ContextTokens:        agent.EstimateMessages(req.History),
			TaskStateTokens:      taskTokens,
			ExpectedOutputTokens: settings.MaxTokens,
			Window:               window,
		}, policy)
	}
	opts.Trigger = measure.Pressure
	if budgetTokens := measure.ContextBudget(); budgetTokens > 0 {
		opts.ContextBudgetTokens = budgetTokens
	}
	if req.HasProjection {
		projection := req.Projection
		opts.Projection = func() (agent.ContextProjection, bool) { return projection, true }
	}
	if h.registry != nil {
		opts.Summarize = h.chargeSummarizer(newSummarizer(h.registry.StreamFn(), h.model))
	}
	return agent.CompactWith(ctx, req.History, opts)
}

// chargeSummarizer wraps the semantic fallback in the loop's existing
// model-call admission. It never creates a second budget and never bypasses an
// exhausted grant.
func (h *harness) chargeSummarizer(inner agent.SummarizeFunc) agent.SummarizeFunc {
	return func(ctx context.Context, messages []agent.Message) (string, error) {
		if h.loop != nil {
			if b := h.loop.Budget(); b != nil {
				if err := b.Admit(budget.DimensionModelCalls); err != nil {
					return "", err
				}
			}
		}
		return inner(ctx, messages)
	}
}

// journal returns the loop's checkpoint seam for the current session, or nil
// when no session or no recovery store is attached.
func (h *harness) journal() agent.EditJournal {
	if h == nil || h.rec == nil || h.sess == nil {
		return nil
	}
	return h.rec.Journal(h.sess.ID)
}

// Undo reverts the last agent edit in the current session's workspace.
//
// It is reachable only from the explicit REPL command, exactly like the
// full-screen UI's /undo. No tool calls it, and the harness exposes no other
// entry point.
func (h *harness) Undo() recovery.Result {
	if h.rec == nil {
		return recovery.Result{Op: recovery.OpUndo, State: recovery.StateFailed,
			Reason: "workspace recovery is unavailable in this session"}
	}
	var sessionID int64
	if h.sess != nil {
		sessionID = h.sess.ID
	}
	return h.rec.Undo(context.Background(), sessionID)
}

// Redo restores the next undone agent edit in the current session.
func (h *harness) Redo() recovery.Result {
	if h.rec == nil {
		return recovery.Result{Op: recovery.OpRedo, State: recovery.StateFailed,
			Reason: "workspace recovery is unavailable in this session"}
	}
	var sessionID int64
	if h.sess != nil {
		sessionID = h.sess.ID
	}
	return h.rec.Redo(context.Background(), sessionID)
}

// Disconnect removes a saved credential. Only file-store and explicit keys
// are removable here: environment and shell credentials are read-only by
// design, and the answer says where to unset them instead of pretending.
func (h *harness) Disconnect(provider string) (string, error) {
	if strings.TrimSpace(provider) == "" {
		return "", fmt.Errorf("usage: /disconnect [provider]")
	}
	_, source, err := h.registry.ResolveAPIKeySource(provider)
	if err != nil {
		return "", err
	}
	switch source {
	case "explicit":
		for _, name := range creds.CredentialProviders(provider) {
			h.registry.SetAPIKey(name, "")
		}
		return fmt.Sprintf("removed the in-session credential for %s", provider), nil
	case "credential file":
		path, pathErr := creds.DefaultPath()
		if pathErr != nil {
			return "", pathErr
		}
		store, openErr := creds.Open(path)
		if openErr != nil {
			return "", openErr
		}
		for _, name := range creds.CredentialProviders(provider) {
			if err := store.Delete(name); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("removed the saved credential for %s", provider), nil
	default:
		return "", fmt.Errorf("the %s credential comes from %s: unset it there rather than here", provider, source)
	}
}

// ResumeSession loads a stored session for /sessions resume in the REPL: the
// history continues the conversation and the session pointer moves with it.
func (h *harness) ResumeSession(id int64) (history []agent.Message, notice string, err error) {
	if h.store == nil {
		return nil, "", fmt.Errorf("no session store attached")
	}
	sess, err := h.store.Get(id)
	if err != nil {
		return nil, "", err
	}
	stored, err := h.store.Messages(id, 0)
	if err != nil {
		return nil, "", fmt.Errorf("load session %d: %w", id, err)
	}
	history, err = session.Replay(stored)
	if err != nil {
		return nil, "", fmt.Errorf("load session %d: %w", id, err)
	}
	h.bindSession(&sess)
	return history, fmt.Sprintf("resumed session %d (%d messages)", id, len(history)), nil
}

// ListSessions prints stored sessions newest-first for the REPL, scoped to the
// current project so sessions from another project do not masquerade as this
// one's history.
func (h *harness) ListSessions(out io.Writer) {
	if h.store == nil {
		_, _ = fmt.Fprintln(out, "no session store attached")
		return
	}
	list, err := h.store.ListByProject(h.projectID, 20)
	if err != nil {
		_, _ = fmt.Fprintf(out, "sessions: %v\n", err)
		return
	}
	if len(list) == 0 {
		_, _ = fmt.Fprintln(out, "No sessions yet.")
		return
	}
	for _, sess := range list {
		title := sess.Title
		if title == "" {
			title = sess.Model
		}
		_, _ = fmt.Fprintf(out, "#%-5d %-16s %-28s %s\n",
			sess.ID, sess.UpdatedAt.Format("2006-01-02 15:04"), title, sess.Cwd)
	}
	_, _ = fmt.Fprintln(out, "resume with /resume <id>")
}

// JobsReport prints background jobs for the REPL. The table is shared with
// the UI's /jobs through the same bash tool instance the loop drives.
func (h *harness) JobsReport() string {
	for _, tool := range h.loop.Tools() {
		bash, ok := tool.(*tools.BashTool)
		if !ok {
			continue
		}
		return formatJobs(bash.Jobs().Snapshot())
	}
	return "job listing is unavailable"
}

// formatJobs renders snapshots for humans. It lives here (not in tools) so
// the UI picker and the REPL print agree without sharing a renderer they
// would then both have to import.
func formatJobs(snapshots []tools.JobInfo) string {
	if len(snapshots) == 0 {
		return "no background jobs"
	}
	var sb strings.Builder
	for _, job := range snapshots {
		status := fmt.Sprintf("exit %d", job.ExitCode)
		if job.Running {
			status = "running, " + job.Elapsed.Round(time.Millisecond).String()
		}
		fmt.Fprintf(&sb, "%s %s (%s)\n", job.ID, job.Command, status)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ModelList returns static catalog ids for the /models browser seed.
func (h *harness) ModelList() []string {
	var ids []string
	for _, model := range catalog.List() {
		ids = append(ids, model.Ref())
	}
	sort.Strings(ids)
	return ids
}

// SavePlaneKey persists an API key for the model control plane's inline
// prompt. The key goes to the credential file (0600, never the shell) so a
// later resolve sees it; the registry's explicit map is untouched so
// /disconnect semantics stay intact. An empty key or a keyless provider is
// refused rather than stored.
func (h *harness) SavePlaneKey(provider, key string) error {
	provider = strings.TrimSpace(provider)
	key = strings.TrimSpace(key)
	if provider == "" || provider == tui.AllProviders {
		return fmt.Errorf("select a provider first")
	}
	if vars, ok := creds.ProviderEnvVars(provider); !ok {
		return fmt.Errorf("unknown provider %q", provider)
	} else if len(vars) == 0 {
		return fmt.Errorf("%s needs no API key", provider)
	}
	if key == "" {
		return fmt.Errorf("empty key: paste a key or press esc to cancel")
	}
	path, err := creds.DefaultPath()
	if err != nil {
		return err
	}
	store, err := creds.Open(path)
	if err != nil {
		return err
	}
	return store.Set(provider, key)
}

// SaveConnect persists a /connect outcome: the pasted key goes to the
// credential file (0600, never the shell), the model goes to settings and
// the loop together. An empty key keeps whatever credential resolved.
func (h *harness) SaveConnect(provider, key, modelRef string) error {
	if strings.TrimSpace(key) != "" {
		path, err := creds.DefaultPath()
		if err != nil {
			return err
		}
		store, err := creds.Open(path)
		if err != nil {
			return err
		}
		if err := store.Set(provider, strings.TrimSpace(key)); err != nil {
			return err
		}
	}
	model, err := h.SwitchModel(context.Background(), modelRef)
	if err != nil {
		return err
	}
	if h.settings != nil {
		h.settings.Model = model.Ref()
		if err := config.Save(*h.settings); err != nil {
			return fmt.Errorf("connect: save settings: %w", err)
		}
	}
	return nil
}

// ConnectREPL runs guided setup on a line reader. Prompts are numbered
// choices rather than free text wherever the answer comes from a list: free
// text where a number belongs is how typos become configuration.
func (h *harness) ConnectREPL(out io.Writer, in io.Reader) error {
	reader := bufio.NewReader(in)
	rows := h.ProviderRows()
	if len(rows) == 0 {
		return fmt.Errorf("no providers known")
	}
	_, _ = fmt.Fprintln(out, "Choose a provider:")
	for i, row := range rows {
		_, _ = fmt.Fprintf(out, "  %2d  %-14s %s\n", i+1, row.ID, row.KeyDetail)
	}
	choice, err := readChoice(reader, out, len(rows))
	if err != nil {
		return err
	}
	provider := rows[choice-1].ID

	_, source, keyErr := h.registry.ResolveAPIKeySource(provider)
	options := []string{"paste a new key"}
	if keyErr == nil {
		options = append(options, "use detected credential ("+source+")")
	}
	_, _ = fmt.Fprintln(out, "Credential:")
	for i, option := range options {
		_, _ = fmt.Fprintf(out, "  %2d  %s\n", i+1, option)
	}
	useDetected := false
	if len(options) > 1 {
		pick, err := readChoice(reader, out, len(options))
		if err != nil {
			return err
		}
		useDetected = pick == 2
	}
	key := ""
	if !useDetected {
		key, err = readSecret(out, reader, in, "API key: ")
		if err != nil {
			return err
		}
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("empty key: paste a key or redo /connect to cancel")
		}
	}

	_, _ = fmt.Fprintln(out, "validating…")
	validateKey := ""
	if !useDetected {
		validateKey = key
	}
	models, _, err := h.validateProvider(context.Background(), provider, validateKey)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return fmt.Errorf("no models listed for %s", provider)
	}
	_, _ = fmt.Fprintln(out, "Models (Enter for the first):")
	shown := models
	if len(shown) > 30 {
		shown = shown[:30]
	}
	for i, id := range shown {
		_, _ = fmt.Fprintf(out, "  %2d  %s\n", i+1, id)
	}
	_, _ = fmt.Fprint(out, "model [1]: ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	model := models[0]
	if pick := strings.TrimSpace(line); pick != "" {
		if n, nerr := atoi(pick); nerr == nil && n >= 1 && n <= len(shown) {
			model = shown[n-1]
		} else {
			model = pick
		}
	}
	saveKey := ""
	if !useDetected {
		saveKey = key
	}
	if err := h.SaveConnect(provider, saveKey, provider+"/"+model); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "connected to %s as %s\n", provider, model)
	return nil
}

// readChoice reads a 1-based menu choice.
func readChoice(reader *bufio.Reader, out io.Writer, max int) (int, error) {
	_, _ = fmt.Fprintf(out, "choice [1-%d]: ", max)
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("read input: %w", err)
	}
	n, nerr := atoi(strings.TrimSpace(line))
	if nerr != nil || n < 1 || n > max {
		return 0, fmt.Errorf("enter a number between 1 and %d", max)
	}
	return n, nil
}

// readSecret reads a credential, masked when the input is a terminal. Piped
// input echoes — unavoidable without a TTY — and the prompt says so rather
// than pretending otherwise.
func readSecret(out io.Writer, reader *bufio.Reader, in io.Reader, prompt string) (string, error) {
	if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		_, _ = fmt.Fprint(out, prompt)
		secret, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		return string(secret), nil
	}
	_, _ = fmt.Fprintf(out, "%s (input will echo: not a terminal)\n> ", prompt)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// SwitchWorkspace moves the session to the named workspace at a turn boundary.
//
// It is the single owner of workspace transitions for both interfaces: the
// plain REPL calls it directly and the full-screen UI reaches it through the
// TUI workspace hook, so neither interface can drift. The transition updates
// the runtime authority gate, the effective role model, and the session
// snapshot together; the caller publishes chrome afterwards.
func (h *harness) SwitchWorkspace(name string) (workspace.Workspace, error) {
	ws, err := workspace.Parse(name)
	if err != nil {
		return "", err
	}
	// Resolve the role's effective model. Offline first: a configured role
	// that the bundle knows switches instantly. Unknown ids keep the current
	// model so a workspace switch never fails for a catalog reason — the
	// authority still changes, which is the transition's contract.
	role := strings.ToLower(ws.String())
	// A role move invalidates the previous demand selection even when the
	// model is unchanged; the next request maps the new role's work onto the
	// ladder.
	h.clearLastReasoning()
	if h.settings != nil {
		if ref, _, _ := h.settings.RoleModel(role); strings.TrimSpace(ref) != "" {
			resolve := catalog.Resolve
			if h.registry != nil {
				resolve = dynamicResolver(context.Background(), h.registry)
			}
			if model, rerr := resolve(ref); rerr == nil {
				h.applyModel(model)
			}
		}
		// Explicit levels and non-demand policies apply immediately. When
		// demand owns the role, the per-request policy will map it onto the
		// newly active model before the next call.
		if !h.demandActive(role) {
			effort := h.roleEffortFor(role)
			enabled, level := h.nonDemandReasoning()
			if effort != "" {
				enabled, level = h.reasoningFor(effort)
			}
			if h.loop != nil {
				h.loop.SetReasoning(enabled, level)
			}
		}
	}
	// A human or control-plane move is applied through this one owner. It may
	// widen the session contract (a switch to a stronger role), but it never
	// removes a grant: narrowing changes only the live role. The policy already
	// decided the move was permitted, so this cannot grant unapproved authority.
	h.contract = h.activeContract().Authorize(ws)
	h.applyAuthority(ws)
	h.workspace = ws.Label()
	if h.sess != nil {
		h.sess.Workspace = ws.String()
	}
	// The session's authority snapshot has one writer. AssignRole, SetModel and
	// a workspace switch all describe the same persisted fact, so they must not
	// each assemble it.
	h.recordSessionAuthority(h.model)
	return ws, nil
}

// activeContract is the session authority evidence for a stateless host.
//
// It is the persistent contract when one has been captured, otherwise the
// authority the session currently holds. It is read-only evidence: the live
// gate still admits every call.
func (h *harness) activeContract() workspace.ExecutionContract {
	if h.contract.Valid() {
		return h.contract
	}
	return workspace.ContractFor(h.activeWorkspace())
}

// activeWorkspace parses the harness's workspace label.
//
// An unset or unknown label resolves to BUILD, mirroring options.authority():
// a harness that predates workspaces keeps the authority it always had rather
// than being silently denied everything. Production always sets the label.
func (h *harness) activeWorkspace() workspace.Workspace {
	if ws, err := workspace.Parse(h.workspace); err == nil {
		return ws
	}
	return workspace.Build
}

// applyAuthority installs ws as the runtime authority.
//
// With a gate it moves the single live authority the loop and any subagent
// read. Without one (a bare test harness) it replaces the loop's admission
// hook, which is the closest equivalent.
func (h *harness) applyAuthority(ws workspace.Workspace) {
	if h.gate != nil {
		h.gate.Set(ws)
		return
	}
	if h.security != nil {
		h.security.SetFallback(ws)
	}
	if h.loop != nil {
		h.loop.SetAdmitHook(admitFor(ws))
	}
}

// RouteTurn classifies a request against the active workspace.
//
// It is the runtime's intent guard: it refuses work outside ASK/FUZZ authority,
// while permitted work stays under the workspace the user selected. Intent
// classification never changes the live gate (§9, §10).
//
// A refused route installs nothing: an ASK request to mutate leaves ASK in
// force and returns the refusal for the caller to show.
func (h *harness) RouteTurn(text string) workspace.Route {
	return workspace.RouteTurn(h.activeWorkspace(), text)
}

// atoi parses a menu number.
func atoi(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
