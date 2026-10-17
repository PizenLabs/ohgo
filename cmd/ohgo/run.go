package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/exec"
	"github.com/PizenLabs/ohgo/project"
	"github.com/PizenLabs/ohgo/prompt"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/startup"
	"github.com/PizenLabs/ohgo/state"
	"github.com/PizenLabs/ohgo/tools"
	"github.com/PizenLabs/ohgo/tui"
	"github.com/PizenLabs/ohgo/workspace"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// options holds the resolved CLI settings for one invocation.
type options struct {
	// root is the working directory for every tool and command.
	root string
	// workspace is the execution authority the session runs under. It decides
	// capability policy, not presentation (§8).
	workspace workspace.Workspace
	// projectID is the stable project identity, empty when it could not be
	// established.
	projectID string
	// model is the model to call.
	model ai.Model
	// maxTokens is the completion budget, already clamped to the model's
	// ceiling. Zero means "use the model's own default".
	maxTokens int
	// maxDepth is how many levels of subagent may nest, counting this agent.
	maxDepth int
	// deadline bounds the whole run in wall-clock time. The zero value is no
	// limit.
	deadline time.Time
	// settings is the loaded configuration.
	settings config.Settings
	// noTools disables the tool set entirely.
	noTools bool
	// autoApprove skips every approval prompt.
	autoApprove bool
	// reasoning enables extended thinking.
	reasoning bool
	// effort is the reasoning effort level.
	effort string
}

// authority returns the workspace in force.
//
// An unset workspace defaults to BUILD. The CLI always resolves one through
// workspace.Parse (where a typo is fatal), but a caller that predates
// workspaces — or a test that builds options directly — keeps the authority it
// always had rather than being silently denied everything.
func (o options) authority() workspace.Workspace {
	if o.workspace.Valid() {
		return o.workspace
	}
	return workspace.Build
}

// resolveOptions builds options from the flags and environment, plus the
// registry the run will use. One registry serves resolution and the loop so
// both agree on credentials and overrides.
func resolveOptions(cmd *cobra.Command) (options, *ai.Registry, error) {
	opts := options{settings: config.Default()}

	flagDir, _ := cmd.Flags().GetString("dir")
	opts.root = flagDir
	if opts.root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return opts, nil, fmt.Errorf("locate working directory: %w", err)
		}
		opts.root = cwd
	}
	abs, err := filepath.Abs(opts.root)
	if err != nil {
		return opts, nil, fmt.Errorf("resolve working directory: %w", err)
	}
	opts.root = abs

	if settings, err := config.Load(opts.root); err != nil && err != config.ErrNotFound {
		// A malformed config is reported but not fatal: the defaults still
		// produce a working session.
		fmt.Fprintf(os.Stderr, "ohgo: %v\n", err)
	} else {
		opts.settings = settings
	}

	opts.noTools, _ = cmd.Flags().GetBool("no-tools")
	opts.autoApprove, _ = cmd.Flags().GetBool("yes")
	opts.reasoning, _ = cmd.Flags().GetBool("reasoning")
	opts.effort, _ = cmd.Flags().GetString("effort")

	// The workspace is a runtime authority, resolved before the model because
	// it participates in model selection (§11, §14). An unknown name is fatal,
	// never a silent fallback to an authority the user did not choose.
	opts.workspace = workspace.Build
	if name := flagValue(cmd, "workspace"); name != "" {
		parsed, err := workspace.Parse(name)
		if err != nil {
			return opts, nil, err
		}
		opts.workspace = parsed
	}

	// Project identity is resolved here but never created: creation belongs
	// to the startup flow (runSession/runOnce via startup.Ensure), which
	// owns the Git and identity prompts. Auto-creating here would silently
	// skip those prompts, so a missing identity simply leaves projectID
	// empty until startup establishes it.
	if identity, found, err := project.Load(opts.root); err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: %v\n", err)
	} else if found {
		opts.projectID = identity.ID
	}

	// A model the user names is resolved through the catalog, and an unknown
	// one is an error rather than a silent fallback: substituting a model would
	// send their conversation to a provider they never chose.
	//
	// The registry is built here (rather than with the loop) because dynamic
	// providers may need it during resolution. Both registries read the same
	// sources, so the one the loop later builds agrees with this one.
	registry := ai.NewRegistry()
	model, modelErr := resolveWorkspaceModel(opts.settings, opts.workspace, flagValue(cmd, "model"), os.Getenv("OHGO_MODEL"),
		dynamicResolver(context.Background(), registry))
	if modelErr != nil {
		return opts, nil, modelErr
	}
	opts.model = model
	opts.maxTokens = resolveMaxTokens(opts.settings, model)

	// Delegation is opt-in. A default of 1 means the top-level agent may
	// delegate but a subagent may not, which caps the token cost of a runaway
	// tree at one level.
	opts.maxDepth = 1
	if depth, _ := cmd.Flags().GetInt("max-depth"); depth > 0 {
		opts.maxDepth = depth
	}

	// A wall-clock deadline bounds the whole run, because every loop iteration
	// makes a provider call and that is what costs money. It is a budget, not a
	// failure mode: on expiry the completed turns are returned intact so the
	// caller can resume rather than discard the work.
	opts.deadline = time.Time{}
	if timeout, _ := cmd.Flags().GetDuration("deadline"); timeout > 0 {
		opts.deadline = time.Now().Add(timeout)
	}

	// Effort precedence: --effort, then the role's configured effort, then the
	// global default. An explicit effort turns thinking on; "off" turns it off
	// outright. The global default alone does not enable thinking — that stays
	// governed by the reasoning setting, so an untouched install behaves as
	// before.
	roleEffort := ""
	if _, level, _ := opts.settings.RoleModel(opts.workspace.String()); level != "" {
		roleEffort = level
	}
	if opts.effort == "" {
		opts.effort = roleEffort
	}
	switch opts.effort {
	case tui.EffortOff:
		opts.reasoning = false
		opts.effort = ""
	case "":
		opts.effort = opts.settings.ReasoningEffort
		if !opts.reasoning {
			opts.reasoning = opts.settings.Reasoning
		}
	default:
		opts.reasoning = true
	}
	return opts, registry, nil
}

// resolveModel picks the model for a run from the highest-precedence source,
// using the global default model from settings.
//
// Precedence is flag, then settings, then environment, then the catalog default.
// Every source but the default must name a model the catalog knows: a silent
// fallback would send the user's conversation to a provider they never chose,
// with nothing on screen to say so. An unknown name is therefore an error, and
// the message names the offending source so the user knows which file to fix.
//
// resolve looks references up; production passes a wrapper that falls back to
// live discovery for dynamic providers, while tests pass catalog.Resolve to
// stay offline and deterministic.
func resolveModel(settings config.Settings, flagModel, envModel string, resolve func(string) (ai.Model, error)) (ai.Model, error) {
	return resolveModelRef(settings.ModelID, flagModel, envModel, resolve)
}

// resolveWorkspaceModel is resolveModel with workspace-specific settings.
//
// The settings source is the workspace's effective model, which falls back to
// the global default when the workspace has no override (§12, I7). Everything
// else — flag and environment precedence, unknown-name refusal — is shared
// with resolveModel through resolveModelRef.
func resolveWorkspaceModel(settings config.Settings, ws workspace.Workspace, flagModel, envModel string, resolve func(string) (ai.Model, error)) (ai.Model, error) {
	return resolveModelRef(func() (string, string, bool) {
		ref, _, _ := settings.RoleModel(ws.String())
		if ref == "" {
			return "", "", false
		}
		provider, id, ok := strings.Cut(ref, "/")
		if !ok || provider == "" || id == "" {
			return "", "", false
		}
		return provider, id, true
	}, flagModel, envModel, resolve)
}

// resolveModelRef resolves the model from the highest-precedence source.
//
// settingsModel is the configuration layer's contribution: the global default
// for resolveModel, or a workspace override for resolveWorkspaceModel.
func resolveModelRef(settingsModel func() (provider, id string, ok bool), flagModel, envModel string, resolve func(string) (ai.Model, error)) (ai.Model, error) {
	model := catalog.Default()

	if override := strings.TrimSpace(envModel); override != "" {
		resolved, err := resolve(override)
		if err != nil {
			return ai.Model{}, fmt.Errorf("OHGO_MODEL: %w", err)
		}
		model = resolved
	}

	if provider, id, ok := settingsModel(); ok {
		resolved, err := resolve(provider + "/" + id)
		if err != nil {
			return ai.Model{}, fmt.Errorf("settings model: %w", err)
		}
		model = resolved
	}

	if override := strings.TrimSpace(flagModel); override != "" {
		resolved, err := resolve(override)
		if err != nil {
			return ai.Model{}, fmt.Errorf("%w\nrun `ohgo models` to list them", err)
		}
		model = resolved
	}

	return model, nil
}

// dynamicResolver wraps static resolution with live discovery for dynamic
// providers (OpenRouter, Ollama, OpenCode gateways). Bundled rows answer
// offline and fast; ids the bundle never saw resolve over the network with a
// bounded timeout, so a dead endpoint fails the run in seconds rather than
// hanging startup.
func dynamicResolver(ctx context.Context, registry *ai.Registry) func(string) (ai.Model, error) {
	return func(ref string) (ai.Model, error) {
		if model, err := catalog.Resolve(ref); err == nil {
			return model, nil
		} else {
			provider, _, ok := strings.Cut(strings.TrimSpace(ref), "/")
			if !ok || !catalog.DynamicProvider(provider) {
				return ai.Model{}, err
			}
			baseURL, _ := catalog.ProviderBaseURL(provider)
			if override, ok := registry.BaseURLOverride(provider); ok {
				baseURL = override
			}
			key, keyErr := registry.ResolveAPIKey(provider)
			if keyErr != nil {
				return ai.Model{}, keyErr
			}
			dctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			return catalog.ResolveDynamic(dctx, http.DefaultClient, ref, baseURL, key)
		}
	}
}

// resolveMaxTokens picks the completion budget for a run.
//
// It defaults to the model's own ceiling. That used to be a flat 8192, which is
// a real functional limit for a coding agent: a 20,000-token file write was
// truncated mid-output and had to be redone in pieces.
//
// The zero case matters as much as the clamp. agent.Loop substitutes its own
// 8192 for a non-positive budget, so returning zero here would reintroduce the
// flat cap one layer down, where the model's ceiling is not in scope.
//
// A user-requested budget above the model's ceiling is clamped rather than
// rejected, because the provider refuses the request outright — and a cap is a
// preference, not a behaviour a model can be asked for beyond what it supports.
func resolveMaxTokens(settings config.Settings, model ai.Model) int {
	ceiling := model.MaxOutputTokens

	budget := settings.MaxTokens
	if budget <= 0 {
		if ceiling > 0 {
			return ceiling
		}
		return 0
	}
	if ceiling > 0 && budget > ceiling {
		return ceiling
	}
	return budget
}

// sessionLabel names a session for the status line. Empty when none.
func sessionLabel(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	return fmt.Sprintf("#%d", sess.ID)
}

// flagValue reads a string flag, which the caller has already declared.
//
// Persistent flags are only merged into Flags() during execution, so a command
// built but not executed (as in tests) still carries the value on
// PersistentFlags(). Checking both keeps resolution working in either case.
func flagValue(cmd *cobra.Command, name string) string {
	if value, err := cmd.Flags().GetString(name); err == nil && value != "" {
		return value
	}
	value, _ := cmd.PersistentFlags().GetString(name)
	return value
}

// buildTools assembles the tool set for a session.
//
// registry is needed because the task tool spawns sibling agents that must reach
// the same provider and credentials. Passing it in rather than reading it from a
// package-level value is what keeps a test able to point a subagent at a scripted
// server.
//
// maxDepth is how many levels of subagent may nest, counting this one. A value of
// 1 means the top-level agent may delegate but a subagent may not, which is the
// safe shape: delegation is useful, and an unbounded tree of agents each spending
// tokens is the failure mode a cap exists to prevent.
func buildTools(opts options, registry *ai.Registry) []agent.Tool {
	if opts.noTools {
		return nil
	}
	// The wait tool observes the bash tool's job table, so both are built
	// from the same instance: two tables would let bash start jobs no wait
	// call could ever see.
	bash := tools.NewBashTool(opts.root)
	toolSet := []agent.Tool{
		tools.NewReadTool(opts.root),
		tools.NewWriteTool(opts.root),
		tools.NewEditTool(opts.root),
		bash,
		tools.NewWaitTool(bash.Jobs()),
		tools.NewGlobTool(opts.root),
		tools.NewGrepTool(opts.root),
		tools.NewLsTool(opts.root),
		tools.NewWebFetchTool(),
		tools.NewTodoTool(),
	}

	// The task tool is added last and gets a spawner that stands up a sibling
	// with the same capabilities. It is omitted entirely for a read-only
	// session, where there would be nothing worth delegating.
	if opts.maxDepth > 1 {
		spawner := newSubagentSpawner(opts, registry, toolSet)
		toolSet = append(toolSet, tools.NewTaskTool(spawner, 0, opts.maxDepth, buildSubagentPrompt(opts)))
	}
	return toolSet
}

// buildSubagentPrompt renders the prompt a subagent is given.
//
// It lives here rather than in the engine because it describes what *this*
// deployment tells a subagent: a child in a coding agent is a researcher with
// the same repository, not a separate product.
func buildSubagentPrompt(opts options) tools.SystemBuilder {
	return func(task string) []ai.SystemPromptBlock {
		return []ai.SystemPromptBlock{
			{
				Text: "You are a subagent working on one piece of a larger task " +
					"assigned to you by the main agent. You cannot see that " +
					"conversation, so answer from what you find in the repository " +
					"and from the assignment alone. Be specific: name the files and " +
					"lines you based your answer on. If you could not determine " +
					"something, say so rather than guessing.",
				Cache: true,
			},
			{Text: "Your assignment:\n" + task},
		}
	}
}

// catalogSnapshot builds the instant /model picker roster from the merged
// catalog snapshot: bundled rows plus cached live rosters, no network.
//
// Detail shows context size and verified pricing only when known; local
// (ollama) rows are marked; confirmed capability flags (reasoning, images)
// appear when set. Unpriced rows carry no price claim: the picker matches a
// "free" query against their names without ever rendering them as verified
// free. Ids stay native "provider/id" so selection resolves through the
// existing switcher without aliasing or fabrication.
func catalogSnapshot() tui.CatalogSnapshot {
	entries, stale := catalog.SnapshotAll()
	options := make([]tui.ModelOption, 0, len(entries))
	for _, entry := range entries {
		model := entry.Model
		id := model.Provider + "/" + model.ID
		title := model.Name
		if title == "" {
			title = id
		}
		var bits []string
		if model.ContextWindow > 0 {
			bits = append(bits, formatTokens(model.ContextWindow)+" ctx")
		}
		switch {
		case entry.PriceKnown && entry.Free:
			bits = append(bits, "free")
		case entry.PriceKnown && (model.Cost.Input > 0 || model.Cost.Output > 0):
			bits = append(bits, fmt.Sprintf("$%.2f/$%.2f per 1M", model.Cost.Input, model.Cost.Output))
		}
		if model.Provider == "ollama" || model.Provider == "ollama-cloud" {
			bits = append(bits, "local")
		} else {
			bits = append(bits, "remote")
		}
		var caps []string
		if model.Reasoning {
			caps = append(caps, "reasoning")
		}
		for _, m := range model.InputModalities {
			if m == "image" {
				caps = append(caps, "images")
				break
			}
		}
		if len(caps) > 0 {
			bits = append(bits, strings.Join(caps, "+"))
		}
		options = append(options, tui.ModelOption{
			ID:              id,
			Provider:        model.Provider,
			Title:           title,
			Detail:          strings.Join(bits, " · "),
			Name:            model.Name,
			Context:         model.ContextWindow,
			PriceKnown:      entry.PriceKnown,
			Free:            entry.Free,
			Origin:          entry.Origin,
			Capabilities:    caps,
			MaxOutput:       model.MaxOutputTokens,
			CostInput:       model.Cost.Input,
			CostOutput:      model.Cost.Output,
			CostCacheRead:   model.Cost.CacheRead,
			CostCacheWrite:  model.Cost.CacheWrite,
			InputModalities: append([]string(nil), model.InputModalities...),
			ThinkingEfforts: thinkingEfforts(entry.Thinking),
			Reasoning:       model.Reasoning,
			ReasoningKnown:  entry.ReasoningKnown,
		})
	}
	origin := "live"
	if stale {
		origin = "cached"
	}
	if len(options) == 0 {
		origin = "loading"
	}
	return tui.CatalogSnapshot{Options: options, Origin: origin, Stale: stale}
}

// refreshModelCatalog fetches every dynamic provider's live roster off the
// update goroutine and reports back through the Bubble Tea command path.
//
// Public metadata needs no credential: OpenRouter's /models answers
// unauthenticated, so a missing key browses with "" rather than failing.
// Authenticated providers use their configured key when one resolves.
// Per-provider failures keep that provider's cached rows and surface one
// summary error; victories merge through the same snapshot the picker
// opened with, so newly added models appear without a binary rebuild.
func refreshModelCatalog(held *harness, seq int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		var failures []string
		for _, provider := range catalog.DynamicProviders() {
			baseURL, _ := catalog.ProviderBaseURL(provider)
			if override, ok := held.registry.BaseURLOverride(provider); ok {
				baseURL = override
			}
			// A missing credential browses public metadata anonymously;
			// only inference requires authentication.
			key, _ := held.registry.ResolveAPIKey(provider)
			pctx, pcancel := context.WithTimeout(ctx, 20*time.Second)
			_, err := catalog.DiscoverModelsFresh(pctx, nil, provider, baseURL, key)
			pcancel()
			if err != nil {
				failures = append(failures, provider+": "+shortCatalogError(err))
			}
		}
		snapshot := catalogSnapshot()
		snapshot.Origin = "live"
		snapshot.Stale = false
		errMsg := ""
		if len(failures) > 0 {
			errMsg = strings.Join(failures, "; ")
			// Partial failure: refreshed providers are live, the rest keep
			// cached rows. Claim "live" only when everything refreshed.
			if len(failures) == len(catalog.DynamicProviders()) {
				snapshot.Origin = "cached"
				snapshot.Stale = true
			}
		}
		return tui.NewModelsRefreshedMsg(seq, snapshot, errMsg)
	}
}

// shortCatalogError compresses a fetch failure to one picker-status line.
func shortCatalogError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "HTTP 429") {
		return "rate-limited"
	}
	if strings.Contains(msg, "rejected the credential") {
		return "credential rejected"
	}
	if strings.Contains(msg, "no live discovery") {
		return "no live catalog"
	}
	if strings.Contains(msg, "context deadline") || strings.Contains(msg, "timeout") {
		return "timed out"
	}
	if strings.Contains(msg, "decode") {
		return "bad response"
	}
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "network") || strings.Contains(msg, "connection refused") {
		return "offline"
	}
	return "unreachable"
}

// formatTokens renders a context window compactly for picker detail.
func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%dk", n/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// connectHooks builds the guided-setup providers from the harness.
func connectHooks(held *harness) tui.ConnectHooks {
	return tui.ConnectHooks{
		Providers: func() []tui.ProviderRow {
			return held.ProviderRows()
		},
		Validate: func(ctx context.Context, provider, key string) ([]string, string, error) {
			return held.validateProvider(ctx, provider, key)
		},
		Save: func(provider, key, model string) error {
			return held.SaveConnect(provider, key, provider+"/"+model)
		},
	}
}

// mentionLister builds the workspace roster offered by the "@" trigger. It
// walks the project with the search tools' own ignore rules, so the inline
// palette cannot suggest a path the read tool would refuse, and caps the roster
// so a large tree cannot stall discovery. A trailing slash marks a folder.
func mentionLister(root string) func() []string {
	return func() []string {
		paths := tools.WorkspaceMentions(context.Background(), root, 0)
		out := make([]string, 0, len(paths))
		for _, p := range paths {
			if p.Dir {
				out = append(out, p.Path+"/")
				continue
			}
			out = append(out, p.Path)
		}
		return out
	}
}

// mentionReader reads a workspace-relative file for "@" context injection,
// with the same root containment the read tool enforces: a reference outside
// the workspace, a directory, or an oversized file fails rather than attaching
// the wrong bytes to the model request.
func mentionReader(root string) func(path string) (string, error) {
	return func(path string) (string, error) {
		return tools.ReadWorkspaceFile(root, path)
	}
}

// validateProvider checks a credential and lists models: probe first (cheap,
// precise errors), discovery second. An empty key resolves the detected
// credential, so choosing "use detected" never pastes anything anywhere.
func (h *harness) validateProvider(ctx context.Context, provider, key string) ([]string, string, error) {
	if strings.TrimSpace(key) == "" {
		resolved, _, err := h.registry.ResolveAPIKeySource(provider)
		if err != nil {
			return nil, "", err
		}
		key = resolved
	}
	baseURL, _ := catalog.ProviderBaseURL(provider)
	if override, ok := h.registry.BaseURLOverride(provider); ok {
		baseURL = override
	}
	api := providerAPI(provider)
	probe := ai.ProbeProvider(ctx, nil, api, baseURL, key)
	if !probe.Reachable || !probe.Authenticated {
		return nil, "", fmt.Errorf("%s", probe.Detail)
	}
	models, err := catalog.DiscoverModels(ctx, nil, provider, baseURL, key)
	if err != nil {
		// Discovery failing after a good probe still validates the
		// credential: the connection works, only the roster fetch did not.
		// Fall back to bundled rows so setup can proceed.
		bundled, berr := catalog.ListForProvider(provider)
		if berr != nil || len(bundled) == 0 {
			return nil, "", err
		}
		var ids []string
		for _, model := range bundled {
			ids = append(ids, model.ID)
		}
		return ids, probe.Detail + " (bundled roster)", nil
	}
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids, probe.Detail, nil
}

type spawnFunc func(ctx context.Context, spec agent.SubagentSpec) (agent.SubagentResult, error)

func (f spawnFunc) Spawn(ctx context.Context, spec agent.SubagentSpec) (agent.SubagentResult, error) {
	return f(ctx, spec)
}

// newSubagentSpawner builds the Spawner the task tool uses.
//
// It closes over the registry and the parent's tool set so a subagent reaches
// the same provider and has the same capabilities. The depth bound is enforced
// structurally: a subagent at the cap is given no task tool at all, so it cannot
// delegate however it is prompted. That is a property of the construction rather
// than a rule checked at call time, which is the difference between a bound and
// a request.
func newSubagentSpawner(opts options, registry *ai.Registry, parentTools []agent.Tool) tools.Spawner {
	return spawnFunc(func(ctx context.Context, spec agent.SubagentSpec) (agent.SubagentResult, error) {
		parent := agent.Config{
			Model:      opts.model,
			Stream:     registry.StreamFn(),
			Tools:      childToolSet(opts, parentTools, spec.Depth),
			MaxTokens:  opts.maxTokens,
			Reasoning:  opts.reasoning,
			Admit:      admitFor(opts.authority()),
			Compaction: compactionFromSettings(opts.settings),
			Summarize:  newSummarizer(registry.StreamFn(), opts.model),
		}
		return agent.RunSubagent(ctx, parent, spec)
	})
}

// childToolSet builds the tools a subagent at depth receives.
//
// It is a pure function so the depth bound is testable directly, without having
// to drive a whole model exchange to observe what a child was given.
//
// The task tool is omitted when the child sits at the cap. A bound checked at
// call time instead would be a rule someone can bypass; one baked into the tool
// set cannot be.
func childToolSet(opts options, parentTools []agent.Tool, depth int) []agent.Tool {
	if depth+1 >= opts.maxDepth {
		return parentTools
	}
	out := make([]agent.Tool, 0, len(parentTools)+1)
	out = append(out, parentTools...)
	return append(out, tools.NewTaskTool(
		nil, depth, opts.maxDepth, buildSubagentPrompt(opts)))
}

// buildLoop constructs the agent loop for a session.
func buildLoop(opts options, registry *ai.Registry, out io.Writer, store *session.Store, sess *session.Session, approve func(ai.ToolCall, agent.Approval) (bool, string)) (*agent.Loop, error) {
	toolSet := buildTools(opts, registry)
	if len(toolSet) == 0 {
		return nil, agent.ErrNoTools
	}

	// The artifact tool is added only when there is somewhere to record to.
	// Its authority is the workspace's: a FUZZ session can file a Finding but
	// not author a Change, which is what makes the handoff contract real
	// rather than a convention (§23, §27).
	if store != nil && sess != nil {
		toolSet = append(toolSet, tools.NewArtifactTool(func(a workspace.Artifact) error {
			return store.SaveArtifact(sess.ID, a)
		}, opts.authority()))
	}

	var extra string
	if project := prompt.ProjectContext(opts.root); project != "" {
		extra = project
	}
	if opts.settings.SystemPrompt != "" {
		if extra != "" {
			extra += "\n\n"
		}
		extra += opts.settings.SystemPrompt
	}

	if approve == nil && !opts.autoApprove {
		approve = func(call ai.ToolCall, approval agent.Approval) (bool, string) {
			return approveToolCall(out, opts, call, approval)
		}
	}

	var onTurn func(agent.Turn)
	if store != nil && sess != nil {
		onTurn = func(turn agent.Turn) {
			printTurn(out, turn)
			persistTurn(store, *sess, turn)
		}
	} else {
		onTurn = func(turn agent.Turn) { printTurn(out, turn) }
	}

	loop, err := agent.New(agent.Config{
		Model:           opts.model,
		Stream:          registry.StreamFn(),
		System:          promptBlocks(opts, extra),
		Tools:           toolSet,
		MaxTokens:       opts.maxTokens,
		Deadline:        opts.deadline,
		Reasoning:       opts.reasoning,
		ReasoningEffort: opts.effort,
		OnTurn:          onTurn,
		Admit:           admitFor(opts.authority()),
		Approve:         approve,
		Compaction:      compactionFromSettings(opts.settings),
		Summarize:       newSummarizer(registry.StreamFn(), opts.model),
		OnCompact: func(result agent.CompactResult) {
			_, _ = fmt.Fprintf(out, "[compacted %d messages into %d tokens; %d retained]\n",
				result.Compacted, result.AfterTokens, result.Retained)
		},
	})
	if err != nil {
		return nil, err
	}
	return loop, nil
}

// compactionFromSettings translates configured values into loop settings.
//
// The percentage default lives here rather than in config so that a config
// file that omits the field entirely still gets a sane trigger.
func compactionFromSettings(settings config.Settings) agent.Compaction {
	c := agent.DefaultCompaction()
	if settings.Compaction == nil {
		return c
	}
	if settings.Compaction.Enabled != nil {
		c.Enabled = *settings.Compaction.Enabled
	}
	if settings.Compaction.Strategy != "" {
		c.Strategy = agent.CompactionStrategy(settings.Compaction.Strategy)
	}
	if settings.Compaction.KeepRecent != nil {
		c.KeepRecent = *settings.Compaction.KeepRecent
	}
	if settings.Compaction.ThresholdPercent != nil {
		c.ThresholdPercent = *settings.Compaction.ThresholdPercent
	}
	return c
}

// admitFor builds the loop's runtime authority gate for a workspace.
//
// It is the one place the workspace policy is bound to the loop, shared by the
// top level and by subagents: a delegated child must inherit the parent's
// authority, or delegation would be a way around the boundary (§7, §37).
func admitFor(ws workspace.Workspace) func(ai.ToolCall, agent.Tool) (bool, string) {
	authority := workspace.Authority{Workspace: ws}
	return func(call ai.ToolCall, tool agent.Tool) (bool, string) {
		if err := authority.Admit(tool, call.Arguments); err != nil {
			return false, err.Error()
		}
		return true, ""
	}
}

// attachShellEscape wires the "!" escape to the local shell and to the same
// mutation classifier the bash tool's admission uses.
//
// The runner is rooted at the workspace directory and runs through the
// platform's shell; the classifier is the exported form of the admission rule,
// so the ASK confirmation gate and the runtime authority cannot drift.
func attachShellEscape(program *tui.Program, root string) {
	program.SetShellEscape(tui.ShellHooks{
		Run: func(ctx context.Context, command string, onChunk func(string)) (exec.EscapeResult, error) {
			return exec.ShellRunner{Dir: root}.Run(ctx, command, onChunk)
		},
		Mutates: tools.CommandMutates,
	})
}

// supportLoopBuilder returns a builder for the ephemeral $sup runtime.
//
// The model and effort resolve through the SUP role on every invocation,
// so a /model reassignment applies to the next $sup with no restart. The
// loop runs under ASK authority (read-only), so support can reason and
// read but cannot mutate. It is built lazily on first use: resolving a
// second model eagerly would cost a round trip for a capability most
// sessions never invoke.
func supportLoopBuilder(opts options, registry *ai.Registry, out io.Writer) func() (supportBundle, error) {
	return func() (supportBundle, error) {
		supModel, effort, err := resolveSupModel(opts.settings, dynamicResolver(context.Background(), registry))
		if err != nil {
			return supportBundle{}, err
		}
		supOpts := opts
		supOpts.workspace = workspace.Ask
		supOpts.model = supModel
		supOpts.maxTokens = resolveMaxTokens(opts.settings, supModel)
		enabled, level := supReasoning(effort)
		supOpts.reasoning = enabled
		supOpts.effort = level
		// Support is read-only by construction (ASK authority), so read tools
		// are auto-approved rather than parking a modal prompt on a query the
		// user expects to be instant.
		loop, err := buildLoop(supOpts, registry, out, nil, nil,
			func(ai.ToolCall, agent.Approval) (bool, string) { return true, "" })
		if err != nil {
			return supportBundle{}, err
		}
		// The caller renders the exchange itself; the loop's turn hook would
		// otherwise print raw text.
		loop.SetTurnHook(nil)
		return supportBundle{
			loop:   loop,
			ref:    supModel.Provider + "/" + supModel.ID,
			effort: effort,
		}, nil
	}
}

// newSummarizer builds the compaction summarizer over a transport.
//
// The stream is a parameter rather than a registry so a test can substitute a
// stub and assert on the request the summarizer actually sends. A copy of this
// body in a test would let the test pass while the production path rots.
//
// It runs a second request against the same provider, which costs tokens but
// is the only honest way to summarise without a local model. The small output
// budget keeps that cost bounded.
func newSummarizer(stream ai.StreamFn, model ai.Model) agent.SummarizeFunc {
	return func(ctx context.Context, messages []agent.Message) (string, error) {
		var text []string
		for _, msg := range messages {
			switch m := msg.(type) {
			case agent.UserMessage:
				text = append(text, "user: "+m.Text)
			case agent.AssistantMessage:
				text = append(text, "assistant: "+m.Message.Text())
			case agent.ToolResultMessage:
				text = append(text, "tool result: "+m.Text())
			case agent.Notification:
				// Never sent to the provider, so never summarised.
			}
		}
		s, err := stream(ctx, ai.Request{
			Model: model,
			System: []ai.SystemPromptBlock{
				{Text: "You summarise coding-agent conversations for later resumption."},
				{Text: prompt.SummarizeInstruction, Cache: true},
			},
			Messages:  []ai.Message{ai.UserMessage{Content: []ai.ContentBlock{ai.TextContent{Text: strings.Join(text, "\n")}}}},
			MaxTokens: 2048,
		})
		if err != nil {
			return "", err
		}
		msg, err := s.Result()
		if err != nil {
			return "", err
		}
		return msg.Text(), nil
	}
}

// persistTurn appends a completed turn to the session transcript.
//
// A write failure is reported but never fails the run: the conversation is
// still correct in memory, and aborting a working turn because a log insert
// failed would be the worse trade.
func persistTurn(store *session.Store, sess session.Session, turn agent.Turn) {
	assistant, err := session.FromAgentMessage(agent.AssistantMessage{Message: turn.Assistant})
	if err == nil && assistant.Role != "" {
		if err := store.Append(sess.ID, assistant); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not save turn: %v\n", err)
		}
	}
	for _, outcome := range turn.Results {
		stored, err := session.FromAgentMessage(agent.ToolResultMessage{
			ToolCallID: outcome.CallID,
			ToolName:   outcome.ToolName,
			Content:    []ai.ContentBlock{ai.TextContent{Text: outcome.Result.Text}},
			IsError:    outcome.Result.IsError,
		})
		if err != nil || stored.Role == "" {
			continue
		}
		if err := store.Append(sess.ID, stored); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not save tool result: %v\n", err)
		}
	}
}

// promptBlocks renders the system prompt into provider blocks.
func promptBlocks(opts options, extra string) []ai.SystemPromptBlock {
	blocks := prompt.Build(opts.root, extra)
	out := make([]ai.SystemPromptBlock, 0, len(blocks))
	for i, text := range blocks {
		out = append(out, ai.SystemPromptBlock{
			Text:  text,
			Cache: i == len(blocks)-1,
		})
	}
	return out
}

// printTurn streams a completed turn to the output.
func printTurn(out io.Writer, turn agent.Turn) {
	for _, block := range turn.Assistant.Content {
		switch b := block.(type) {
		case ai.TextContent:
			if b.Text != "" {
				_, _ = fmt.Fprintln(out, b.Text)
			}
		}
	}
	for _, outcome := range turn.Results {
		status := "ok"
		if outcome.Result.IsError {
			status = "error"
		}
		_, _ = fmt.Fprintf(out, "  %s %s [%s]\n", status, outcome.ToolName, outcome.CallID)
	}
}

// approveToolCall asks the user to permit a tool call.
func approveToolCall(out io.Writer, opts options, call ai.ToolCall, approval agent.Approval) (bool, string) {
	// Policy from settings wins, so a user who denied a tool is never asked
	// again for it.
	if policy, ok := opts.settings.ApprovalFor(call.Name); ok {
		switch policy {
		case "allow":
			return true, ""
		case "deny":
			return false, "blocked by settings"
		}
	}
	if approval.Reason != "" {
		_, _ = fmt.Fprintf(out, "%s (%s)\n", call.Name, approval.Reason)
	} else {
		_, _ = fmt.Fprintln(out, call.Name)
	}
	_, _ = fmt.Fprint(out, "Run this tool? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, "could not read approval"
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "y" || answer == "yes" {
		return true, ""
	}
	return false, "denied by user"
}

// runCmd handles `ohgo run` for one-shot execution.
func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run [prompt]",
		Short: "Run one prompt and exit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnce(cmd, args[0])
		},
	}
}

// bootProject resolves the project identity for a one-shot run without ever
// creating project state.
//
// It is the read-only counterpart to startup.Ensure: a command that cannot
// show the initialization gate must not initialize silently, so an
// uninitialized directory is a fact to report, not a directory to create.
func bootProject(root string) (string, error) {
	d := startup.Discover(root)
	if !d.ProjectFound {
		return "", fmt.Errorf(
			"project not initialized in %s: run `ohgo` there to complete initialization", root)
	}
	return d.Project.ID, nil
}

// runOnce executes a single prompt.
func runOnce(cmd *cobra.Command, text string) error {
	opts, registry, err := resolveOptions(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	// A one-shot run cannot satisfy the initialization gate: it never
	// prompts. Discovery is therefore read-only and an uninitialized project
	// is reported rather than created — creating .ohgo/ here would initialize
	// as a side effect of running, exactly the bypass startup exists to
	// prevent.
	projectID, err := bootProject(opts.root)
	if err != nil {
		return err
	}
	opts.projectID = projectID

	resumeID, _ := cmd.Flags().GetInt64("resume")
	store, sess, closeStore, err := openSession(opts, resumeID)
	if err != nil {
		return err
	}
	defer closeStore()

	if resumeID > 0 {
		if err := applySessionAuthority(cmd, &opts, dynamicResolver(context.Background(), registry), store, sess); err != nil {
			return err
		}
	}

	var history []agent.Message
	if sess != nil && resumeID > 0 {
		history, err = loadHistory(store, sess)
		if err != nil {
			return err
		}
	}

	loop, err := buildLoop(opts, registry, out, store, sess, nil)
	if err != nil {
		return err
	}
	request := append(append([]agent.Message(nil), history...), agent.UserMessage{Text: text})
	_, err = loop.Run(context.Background(), request)
	return err
}

// openStore opens the session database without creating a session. A store
// that cannot be opened is not fatal: running without persistence is better
// than refusing to run at all, so the failure is reported and a nil store
// is returned. It is the shared seam behind openSession and the onboarding
// wizard, which must open the store before the project identity (and hence
// the session) exists.
func openStore() (*session.Store, func(), error) {
	path, err := session.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: sessions disabled: %v\n", err)
		return nil, func() {}, nil
	}
	store, err := session.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: sessions disabled: %v\n", err)
		return nil, func() {}, nil
	}
	return store, func() { _ = store.Close() }, nil
}

// openSession opens the store and either resumes or creates a session.
//
// A resume id that does not exist *is* fatal, because silently
// starting an empty session would look like the history was lost.
func openSession(opts options, resumeID int64) (*session.Store, *session.Session, func(), error) {
	store, closeStore, err := openStore()
	if err != nil {
		return nil, nil, nil, err
	}
	if store == nil {
		return nil, nil, closeStore, nil
	}

	if resumeID > 0 {
		sess, err := store.Get(resumeID)
		if err != nil {
			_ = store.Close()
			if errors.Is(err, session.ErrSessionNotFound) {
				return nil, nil, nil, fmt.Errorf("%w: %d", session.ErrSessionNotFound, resumeID)
			}
			return nil, nil, nil, err
		}
		return store, &sess, func() { _ = store.Close() }, nil
	}

	sess, err := store.CreateWith(session.CreateOptions{
		Cwd:       opts.root,
		Model:     opts.model.Provider + "/" + opts.model.ID,
		Workspace: opts.authority().String(),
		Project:   opts.projectID,
	})
	if err != nil {
		_ = store.Close()
		fmt.Fprintf(os.Stderr, "ohgo: sessions disabled: %v\n", err)
		return nil, nil, func() {}, nil
	}
	return store, &sess, func() { _ = store.Close() }, nil
}

// installSessionManager wires the /sessions Session Manager: the roster
// listing, the resume opener, and the write actions (new, rename, archive,
// delete). A nil store installs nothing, so the command reports itself
// unavailable rather than panicking.
func installSessionManager(program *tui.Program, store *session.Store, held *harness, opts options) {
	if store == nil {
		return
	}
	program.SetSessionBrowser(
		func() ([]tui.SessionItem, error) {
			// The browser reads the harness's project, not the startup
			// snapshot: onboarding commits the identity after the browser is
			// installed, and the listing must follow it.
			list, err := store.ListByProject(held.projectID, 50)
			if err != nil {
				return nil, err
			}
			ids := make([]int64, 0, len(list))
			for _, sess := range list {
				ids = append(ids, sess.ID)
			}
			stats, err := store.Stats(ids)
			if err != nil {
				return nil, err
			}
			items := make([]tui.SessionItem, 0, len(list))
			for _, sess := range list {
				stat := stats[sess.ID]
				items = append(items, tui.SessionItem{
					ID:      sess.ID,
					Title:   sess.Title,
					Model:   sess.Model,
					Updated: sess.UpdatedAt.Format("2006-01-02 15:04"),
					Cwd:     sess.Cwd,
					Tokens:  stat.Tokens,
					Prompt:  stat.Prompt,
				})
			}
			return items, nil
		},
		func(id int64) (*session.Session, tui.OpenedSession, error) {
			sess, err := store.Get(id)
			if err != nil {
				return nil, tui.OpenedSession{}, err
			}
			stored, err := store.Messages(id, 0)
			if err != nil {
				return nil, tui.OpenedSession{}, fmt.Errorf("load session %d: %w", id, err)
			}
			history, err := session.Replay(stored)
			if err != nil {
				return nil, tui.OpenedSession{}, fmt.Errorf("load session %d: %w", id, err)
			}
			return &sess, tui.OpenedSession{
				History: history,
				Entries: tui.EntriesFromMessages(history),
				Notice:  fmt.Sprintf("resumed session %d (%d messages)", id, len(history)),
			}, nil
		},
	)
	program.SetSessionActions(tui.SessionActions{
		New: func() (*session.Session, tui.OpenedSession, error) {
			sess, err := store.CreateWith(session.CreateOptions{
				Cwd:       opts.root,
				Model:     held.model.Provider + "/" + held.model.ID,
				Workspace: opts.authority().String(),
				Project:   held.projectID,
			})
			if err != nil {
				return nil, tui.OpenedSession{}, err
			}
			return &sess, tui.OpenedSession{
				Notice: fmt.Sprintf("started new session %d", sess.ID),
			}, nil
		},
		Rename:  func(id int64, title string) error { return store.SetTitle(id, title) },
		Archive: func(id int64) error { return store.SetArchived(id, true) },
		Delete:  func(id int64) error { return store.Delete(id) },
	})
}

// applySessionAuthority restores a resumed session's effective workspace and
// model, then records the result as the session's snapshot.
//
// A running/resumable session owns its resolved model identity (§13): if the
// global configuration changed since the session was created, the session must
// not silently switch models on restart (I8). An explicit flag or environment
// override still wins, and is then written back so the snapshot stays truthful
// (§31).
//
// resolve is a parameter rather than a registry so a test can drive it with
// catalog.Resolve and stay offline.
func applySessionAuthority(cmd *cobra.Command, opts *options, resolve func(string) (ai.Model, error), store *session.Store, sess *session.Session) error {
	if sess == nil {
		return nil
	}
	explicitWorkspace := flagValue(cmd, "workspace") != ""
	explicitModel := flagValue(cmd, "model") != "" || strings.TrimSpace(os.Getenv("OHGO_MODEL")) != ""

	if explicitWorkspace {
		// The CLI already resolved and validated this; re-read it so the
		// function is self-consistent rather than depending on the caller.
		if ws, err := workspace.Parse(flagValue(cmd, "workspace")); err == nil {
			opts.workspace = ws
		}
	} else if ws, err := workspace.Parse(sess.Workspace); err == nil {
		opts.workspace = ws
	}
	if !explicitModel && strings.TrimSpace(sess.Model) != "" {
		resolved, err := resolve(sess.Model)
		if err != nil {
			return fmt.Errorf("resume: session model %q: %w", sess.Model, err)
		}
		opts.model = resolved
		opts.maxTokens = resolveMaxTokens(opts.settings, resolved)
	}

	// Snapshot the effective authority so a later run restores what actually
	// executed rather than what the configuration happened to say this time.
	effective := opts.model.Provider + "/" + opts.model.ID
	if store != nil && (sess.Workspace != opts.authority().String() || sess.Model != effective) {
		if err := store.SetAuthority(sess.ID, opts.authority().String(), effective); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not record session authority: %v\n", err)
		}
	}
	return nil
}

// loadHistory replays a stored transcript so a resumed session continues from
// where it left off.
//
// A replay failure is fatal: starting from an empty history would look
// identical to the session having been wiped.
func loadHistory(store *session.Store, sess *session.Session) ([]agent.Message, error) {
	if store == nil || sess == nil {
		return nil, nil
	}
	stored, err := store.Messages(sess.ID, 0)
	if err != nil {
		return nil, fmt.Errorf("load session %d: %w", sess.ID, err)
	}
	return session.Replay(stored)
}

// useInteractiveUI reports whether the full-screen UI should be used.
//
// It requires a TTY on both stdin and stdout, plus the absence of --no-tui or
// --plain. Detection reads the process's own descriptors, so it is a function
// of the environment rather than a flag a test could leave set.
func useInteractiveUI(cmd *cobra.Command) bool {
	if disabled, _ := cmd.Flags().GetBool("no-tui"); disabled {
		return false
	}
	if plain, _ := cmd.Flags().GetBool("plain"); plain {
		return false
	}
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

// isTerminal reports whether a file is a real terminal.
//
// The check is an ioctl, not a file-mode test: /dev/null is a character device
// and would otherwise count, which would start the full-screen UI with its
// output discarded.
func isTerminal(file *os.File) bool {
	return term.IsTerminal(int(file.Fd()))
}

// runSession starts the interactive REPL.
func runSession(cmd *cobra.Command, args []string) error {
	opts, registry, err := resolveOptions(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	// The full-screen UI owns onboarding for an uninitialized project: raw
	// stdin prompts would scroll the terminal and then jump into the
	// alt-screen. The wizard collects the same three decisions in-canvas
	// and commits through the same seams. Every other path keeps the
	// stdin gate below, which fast-paths initialized projects either way.
	if useInteractiveUI(cmd) && startup.NeedsInit(startup.Discover(opts.root)) {
		return runSessionWizard(cmd, opts, registry, out)
	}

	// Startup owns project initialization, Git detection, and local
	// identity. It runs before the runtime so the identity surface below is
	// truthful, and it fast-paths when .ohgo/project.yml already exists.
	//
	// A cancel at the initialization gate is a clean exit, not an error: the
	// user chose not to initialize, and nothing was created.
	boot, err := startup.Ensure(opts.root, startup.Deps{
		Stdin:  os.Stdin,
		Stdout: out,
	})
	if errors.Is(err, startup.ErrCanceled) {
		_, _ = fmt.Fprintln(out, "initialization canceled; nothing was created.")
		return nil
	}
	if err != nil {
		return err
	}
	opts.settings = boot.Settings
	opts.projectID = boot.Project.ID

	resumeID, _ := cmd.Flags().GetInt64("resume")
	store, sess, closeStore, err := openSession(opts, resumeID)
	if err != nil {
		return err
	}
	defer closeStore()

	if resumeID > 0 {
		if err := applySessionAuthority(cmd, &opts, dynamicResolver(context.Background(), registry), store, sess); err != nil {
			return err
		}
	}

	loop, err := buildLoop(opts, registry, out, store, sess, nil)
	if err != nil {
		return err
	}

	// One ephemeral surface serves both interfaces. It is created here, not in
	// buildLoop, because it owns its own ASK-model loop and is deliberately
	// absent from the session's persistence path.
	surface := newSupportSurface(supportLoopBuilder(opts, registry, out))

	// The full-screen UI needs the loop before it can supply its approval
	// hook, so the routing happens here rather than inside buildLoop. A pipe or
	// a test never reaches this branch and keeps plain line output.
	if useInteractiveUI(cmd) {
		// The header effort badge shows the active reasoning level, or hides
		// when thinking is disabled: a default install (reasoning off) keeps
		// the legacy header rather than inventing a level.
		initialEffort := ""
		if opts.reasoning {
			initialEffort = opts.effort
		}
		programOpts := []tui.ProgramOption{tui.WithVersion(version), tui.WithEffort(initialEffort)}
		// Precedence: flag, then settings, then the environment default. The
		// environment matters because the wheel reaches the program by
		// different routes in different terminals, and a multiplexer that does
		// not forward alternate scroll needs mouse reporting to deliver it at
		// all — see tui.MouseDefaultFor for the measurement behind that.
		// An unrecognised value degrades to select so a typo never silently
		// costs the user drag-copy.
		mouse := opts.settings.Mouse
		if flag := flagValue(cmd, "mouse"); flag != "" {
			mouse = flag
		}
		if mouse == "" {
			mouse = tui.MouseDefaultFor(os.Getenv("TMUX"))
		}
		if tui.NormalizeMouse(mouse) == tui.MouseReport {
			programOpts = append(programOpts, tui.WithMouseReporting())
		}
		program := tui.NewProgram(loop, opts.model.Provider+"/"+opts.model.ID, opts.model.ContextWindow, programOpts...)
		attachShellEscape(program, opts.root)

		// Output-first mode is a different interaction boundary, not a
		// different program: the same loop, the same state machine, the same
		// approval and command paths. The terminal scrolls, selects and
		// copies; ohgo writes the transcript and keeps the composer. It
		// rejects mouse reporting outright, so the mouse policy above is
		// ignored there.
		if live, _ := cmd.Flags().GetBool("live"); live {
			return program.RunLive()
		}

		// One harness serves the UI's commands; the REPL builds its own
		// further down from the same pieces, so both agree.
		held := newHarness(registry, loop, store, sess, &agent.UsageLog{}, &opts.settings, opts.root, opts.model)
		held.usage.SetCost(opts.model.Cost)
		held.workspace = opts.authority().Label()
		held.projectID = opts.projectID
		held.support = surface
		program.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
			return surface.ask(ctx, question, onDelta)
		})
		program.SetSupportInfo(held.SupportInfo)
		program.SetMentionLister(mentionLister(opts.root))
		program.SetMentionReader(mentionReader(opts.root))

		// The header effort badge follows the session role's effective level.
		// The reporter reads the configuration authority (role effort, then
		// the global default) through the same reasoning mapping the loop
		// uses, so the badge and the request agree. Disabled thinking
		// reports "" and the badge hides.
		program.SetEffortProvider(func() string {
			role := held.sessionRole()
			if role == "" {
				role = strings.ToLower(opts.authority().String())
			}
			enabled, level := held.reasoningFor(held.roleEffortFor(role))
			if !enabled {
				return ""
			}
			return level
		})

		// A resumed session replays its transcript, so the UI continues rather
		// than restarts. Loading it here means a failure is fatal rather than a
		// silently empty session.
		var history []agent.Message
		if sess != nil && resumeID > 0 {
			history, err = loadHistory(store, sess)
			if err != nil {
				return err
			}
		}
		if store != nil && sess != nil {
			program.AttachSession(store, sess, history)
		}

		program.SetModelSwitcher(
			func(id string) error {
				// The mid-session switcher shares the catalog's resolver, so a
				// model offered by `ohgo models` is guaranteed to be switchable
				// and a typo gets the same message either way.
				_, err := held.SwitchModel(context.Background(), id)
				return err
			},
			func() string {
				return held.model.Provider + "/" + held.model.ID
			},
		)
		program.SetUsageLog(held.usage)
		program.SetCompactor(func(ctx context.Context, history []agent.Message) ([]agent.Message, string, error) {
			return held.CompactHistory(ctx, history)
		})
		program.SetDoctorFunc(held.DoctorLines)
		program.SetProvidersFunc(held.ProviderRows)
		program.SetProbeFunc(func(ctx context.Context) tui.ProbeFinished {
			return held.ProbeCurrent(ctx)
		})
		program.SetCredentialDeleter(func(provider string) (string, error) {
			return held.Disconnect(provider)
		})
		program.SetConnectHooks(connectHooks(held))
		program.SetModelLister(func() []string {
			var ids []string
			for _, model := range catalog.List() {
				ids = append(ids, model.Provider+"/"+model.ID)
			}
			return ids
		})
		program.SetCatalogHooks(tui.CatalogHooks{
			Snapshot: func() tui.CatalogSnapshot { return catalogSnapshot() },
			Refresh: func(ctx context.Context, seq int) tea.Cmd {
				return refreshModelCatalog(held, seq)
			},
		})
		// The model control plane: the provider pane, the role assignments,
		// and the one seam /model writes through. The program owns none of
		// this — it renders what the harness reports and hands edits back.
		program.SetModelControl(tui.ModelControlHooks{
			Providers: func() []tui.ProviderRow { return held.ProviderPaneRows() },
			Roles:     func() []tui.RoleAssignment { return held.RoleAssignments() },
			ActiveRole: func() string {
				return strings.ToLower(opts.authority().String())
			},
			Assign: func(update tui.RoleAssignment) (tui.RoleAssignment, []tui.RoleAssignment, error) {
				return held.AssignRole(update)
			},
			EffectiveWindow: func() int { return held.model.ContextWindow },
			SaveKey: func(provider, key string) error {
				return held.SavePlaneKey(provider, key)
			},
		})
		// Workspace commands route through the harness seam: the program never
		// owns authority, it asks the owner to move it at a turn boundary.
		program.SetWorkspaceSwitcher(tui.WorkspaceHooks{
			Current: func() string { return held.workspace },
			Switch: func(name string) error {
				ws, err := held.SwitchWorkspace(name)
				if err != nil {
					return err
				}
				opts.workspace = ws
				style := ws.Style()
				program.PublishWorkspace(tui.WorkspaceUpdated{
					Workspace:       style.Label,
					WorkspaceAccent: style.Accent,
					HasWorkspace:    true,
				})
				// A workspace move rebinds the session's model and effort:
				// the header follows the newly effective role rather than
				// naming a model the loop no longer calls.
				program.PublishModel(held.model.Provider + "/" + held.model.ID)
				program.PublishEffort(func() string {
					role := held.sessionRole()
					if role == "" {
						role = strings.ToLower(ws.String())
					}
					enabled, level := held.reasoningFor(held.roleEffortFor(role))
					if !enabled {
						return ""
					}
					return level
				}())
				program.PublishNotice("switched to " + style.Label)
				return nil
			},
		})
		program.SetJobsLister(func() []tui.JobItem {
			for _, tool := range loop.Tools() {
				bash, ok := tool.(*tools.BashTool)
				if !ok {
					continue
				}
				var items []tui.JobItem
				for _, job := range bash.Jobs().Snapshot() {
					items = append(items, tui.JobItem{
						ID:       job.ID,
						Command:  job.Command,
						Running:  job.Running,
						ExitCode: job.ExitCode,
						Elapsed:  job.Elapsed.Round(time.Millisecond).String(),
						Tail:     job.OutputTail,
					})
				}
				return items
			}
			return nil
		})
		if store != nil {
			installSessionManager(program, store, held, opts)
		}
		program.SetTheme(opts.settings.Theme)
		program.SetThemeChangedHandler(func(theme string) {
			opts.settings.Theme = theme
			if err := config.Save(opts.settings); err != nil {
				fmt.Fprintf(os.Stderr, "ohgo: could not save theme: %v\n", err)
			}
		})
		style := opts.authority().Style()
		program.PublishWorkspace(tui.WorkspaceUpdated{
			Cwd:             opts.root,
			HasCwd:          true,
			HasSession:      sess != nil,
			Session:         sessionLabel(sess),
			Workspace:       style.Label,
			WorkspaceAccent: style.Accent,
			HasWorkspace:    true,
		})
		// The runtime identity surface shows who, where, and what state.
		// All values come from startup's observed state, never invented.
		branch, gitStatus := boot.Git.BranchStatus()
		program.PublishIdentity(tui.IdentityUpdated{
			User:      boot.Project.WorkerName(),
			Greeting:  boot.Greeting,
			GitBranch: branch,
			GitStatus: gitStatus,
			// The measured counts let /status name which kind of change is
			// present; Known marks them real so an unknown status never
			// renders as a clean tree.
			GitStaged:    boot.Git.Summary.Staged,
			GitModified:  boot.Git.Summary.Unstaged,
			GitUntracked: boot.Git.Summary.Untracked,
			GitKnown:     boot.Git.Known,
			Ready:        "ready",
		})

		if opts.autoApprove {
			loop.SetApprovalHook(func(ai.ToolCall, agent.Approval) (bool, string) { return true, "" })
		} else {
			// The hook belongs to the program that is actually running. Wiring
			// it anywhere else deadlocks: the tool would block on an answer
			// channel no UI is reading.
			loop.SetApprovalHook(program.ApprovalFunc())
		}
		// The program owns turn output and persistence here (transcript via
		// events, store via runTurn): the loop's hooks must stay silent, or
		// every turn prints raw text over the alt-screen and persists twice.
		loop.SetTurnHook(nil)
		loop.SetCompactHook(func(result agent.CompactResult) {
			program.PublishNotice(fmt.Sprintf("context compacted: %d messages summarised, %d retained",
				result.Compacted, result.Retained))
		})
		return program.Run()
	}

	_, _ = fmt.Fprintf(out, "ohgo %s · %s/%s · %s · %s\n", version, opts.model.Provider, opts.model.ID, opts.authority().Label(), opts.root)
	if boot.Greeting != "" {
		_, _ = fmt.Fprintln(out, boot.Greeting)
	}
	_, _ = fmt.Fprintln(out, "Type your request, or /quit to exit.")

	var history []agent.Message
	if sess != nil && resumeID > 0 {
		history, err = loadHistory(store, sess)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "resumed session %d (%d messages)\n", sess.ID, len(history))
	}

	plain := newHarness(registry, loop, store, sess, &agent.UsageLog{}, &opts.settings, opts.root, opts.model)
	plain.usage.SetCost(opts.model.Cost)
	plain.workspace = opts.authority().Label()
	plain.projectID = opts.projectID
	plain.support = surface
	return repl(plain, out, os.Stdin, history)
}

// runSessionWizard starts the full-screen UI with in-canvas onboarding.
//
// It is the TUI counterpart to startup.Ensure: the same three decisions in
// the same order (Git, worker, project), collected by an InitWizardModel
// instead of stdin prompts, and committed through the same seams
// (startup.InitGit, project.EnsureWith). Nothing on disk changes before
// Complete runs, so quitting the wizard leaves nothing created behind,
// exactly like cancelling the stdin gate.
//
// The terminal is entered once: the wizard renders between the header and
// footer of the same Program that serves the session, and completion drops
// the modal onto the idle canvas without clearing or re-initializing the
// screen. No os.Stdin read happens on this path.
func runSessionWizard(cmd *cobra.Command, opts options, registry *ai.Registry, out io.Writer) error {
	d := startup.Discover(opts.root)

	// The store opens before the project identity exists; the session is
	// created in Complete, once the identity it belongs to is committed.
	store, closeStore, err := openStore()
	if err != nil {
		return err
	}
	defer closeStore()

	loop, err := buildLoop(opts, registry, out, store, nil, nil)
	if err != nil {
		return err
	}

	surface := newSupportSurface(supportLoopBuilder(opts, registry, out))

	initialEffort := ""
	if opts.reasoning {
		initialEffort = opts.effort
	}
	programOpts := []tui.ProgramOption{tui.WithVersion(version), tui.WithEffort(initialEffort)}
	mouse := opts.settings.Mouse
	if flag := flagValue(cmd, "mouse"); flag != "" {
		mouse = flag
	}
	if mouse == "" {
		mouse = tui.MouseDefaultFor(os.Getenv("TMUX"))
	}
	if tui.NormalizeMouse(mouse) == tui.MouseReport {
		programOpts = append(programOpts, tui.WithMouseReporting())
	}
	program := tui.NewProgram(loop, opts.model.Provider+"/"+opts.model.ID, opts.model.ContextWindow, programOpts...)
	attachShellEscape(program, opts.root)

	if live, _ := cmd.Flags().GetBool("live"); live {
		return program.RunLive()
	}

	held := newHarness(registry, loop, store, nil, &agent.UsageLog{}, &opts.settings, opts.root, opts.model)
	held.usage.SetCost(opts.model.Cost)
	held.workspace = opts.authority().Label()
	held.support = surface
	program.SetSupportStreamFunc(func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return surface.ask(ctx, question, onDelta)
	})
	program.SetSupportInfo(held.SupportInfo)
	program.SetMentionLister(mentionLister(opts.root))
	program.SetMentionReader(mentionReader(opts.root))

	program.SetEffortProvider(func() string {
		role := held.sessionRole()
		if role == "" {
			role = strings.ToLower(opts.authority().String())
		}
		enabled, level := held.reasoningFor(held.roleEffortFor(role))
		if !enabled {
			return ""
		}
		return level
	})

	program.SetModelSwitcher(
		func(id string) error {
			_, err := held.SwitchModel(context.Background(), id)
			return err
		},
		func() string {
			return held.model.Provider + "/" + held.model.ID
		},
	)
	program.SetUsageLog(held.usage)
	program.SetCompactor(func(ctx context.Context, history []agent.Message) ([]agent.Message, string, error) {
		return held.CompactHistory(ctx, history)
	})
	program.SetDoctorFunc(held.DoctorLines)
	program.SetProvidersFunc(held.ProviderRows)
	program.SetProbeFunc(func(ctx context.Context) tui.ProbeFinished {
		return held.ProbeCurrent(ctx)
	})
	program.SetCredentialDeleter(func(provider string) (string, error) {
		return held.Disconnect(provider)
	})
	program.SetConnectHooks(connectHooks(held))
	program.SetModelLister(func() []string {
		var ids []string
		for _, model := range catalog.List() {
			ids = append(ids, model.Provider+"/"+model.ID)
		}
		return ids
	})
	program.SetCatalogHooks(tui.CatalogHooks{
		Snapshot: func() tui.CatalogSnapshot { return catalogSnapshot() },
		Refresh: func(ctx context.Context, seq int) tea.Cmd {
			return refreshModelCatalog(held, seq)
		},
	})
	program.SetModelControl(tui.ModelControlHooks{
		Providers: func() []tui.ProviderRow { return held.ProviderPaneRows() },
		Roles:     func() []tui.RoleAssignment { return held.RoleAssignments() },
		ActiveRole: func() string {
			return strings.ToLower(opts.authority().String())
		},
		Assign: func(update tui.RoleAssignment) (tui.RoleAssignment, []tui.RoleAssignment, error) {
			return held.AssignRole(update)
		},
		EffectiveWindow: func() int { return held.model.ContextWindow },
		SaveKey: func(provider, key string) error {
			return held.SavePlaneKey(provider, key)
		},
	})
	program.SetWorkspaceSwitcher(tui.WorkspaceHooks{
		Current: func() string { return held.workspace },
		Switch: func(name string) error {
			ws, err := held.SwitchWorkspace(name)
			if err != nil {
				return err
			}
			opts.workspace = ws
			style := ws.Style()
			program.PublishWorkspace(tui.WorkspaceUpdated{
				Workspace:       style.Label,
				WorkspaceAccent: style.Accent,
				HasWorkspace:    true,
			})
			program.PublishModel(held.model.Provider + "/" + held.model.ID)
			program.PublishEffort(func() string {
				role := held.sessionRole()
				if role == "" {
					role = strings.ToLower(ws.String())
				}
				enabled, level := held.reasoningFor(held.roleEffortFor(role))
				if !enabled {
					return ""
				}
				return level
			}())
			program.PublishNotice("switched to " + style.Label)
			return nil
		},
	})
	program.SetJobsLister(func() []tui.JobItem {
		for _, tool := range loop.Tools() {
			bash, ok := tool.(*tools.BashTool)
			if !ok {
				continue
			}
			var items []tui.JobItem
			for _, job := range bash.Jobs().Snapshot() {
				items = append(items, tui.JobItem{
					ID:       job.ID,
					Command:  job.Command,
					Running:  job.Running,
					ExitCode: job.ExitCode,
					Elapsed:  job.Elapsed.Round(time.Millisecond).String(),
					Tail:     job.OutputTail,
				})
			}
			return items
		}
		return nil
	})
	if store != nil {
		installSessionManager(program, store, held, opts)
	}
	program.SetTheme(opts.settings.Theme)
	program.SetThemeChangedHandler(func(theme string) {
		opts.settings.Theme = theme
		if err := config.Save(opts.settings); err != nil {
			fmt.Fprintf(os.Stderr, "ohgo: could not save theme: %v\n", err)
		}
	})
	style := opts.authority().Style()
	program.PublishWorkspace(tui.WorkspaceUpdated{
		Cwd:             opts.root,
		HasCwd:          true,
		Workspace:       style.Label,
		WorkspaceAccent: style.Accent,
		HasWorkspace:    true,
	})

	resumeID, _ := cmd.Flags().GetInt64("resume")
	program.SetInitWizard(
		tui.NewInitWizard(!d.HasGit, startup.WorkerSuggestion(d, opts.settings), startup.ProjectSuggestion(d)),
		tui.InitWizardHooks{Complete: wizardComplete(cmd, &opts, registry, d, store, held, program, resumeID)},
	)

	if opts.autoApprove {
		loop.SetApprovalHook(func(ai.ToolCall, agent.Approval) (bool, string) { return true, "" })
	} else {
		loop.SetApprovalHook(program.ApprovalFunc())
	}
	loop.SetTurnHook(nil)
	loop.SetCompactHook(func(result agent.CompactResult) {
		program.PublishNotice(fmt.Sprintf("context compacted: %d messages summarised, %d retained",
			result.Compacted, result.Retained))
	})
	return program.Run()
}

// wizardComplete commits one finished onboarding run. It collects the Git,
// worker, and project decisions first and mutates disk only afterwards,
// mirroring startup.Ensure: git init, then project.yml, then the session.
// It runs on the Bubble Tea update goroutine (see InitWizardHooks), so it
// attaches the session and publishes chrome directly. A failed commit
// returns an error and the wizard reopens its final step for retry.
func wizardComplete(cmd *cobra.Command, opts *options, registry *ai.Registry, d startup.Discovery, store *session.Store, held *harness, program *tui.Program, resumeID int64) func(tui.InitWizardDecision) (tui.IdentityUpdated, string, error) {
	return func(dec tui.InitWizardDecision) (tui.IdentityUpdated, string, error) {
		var notes []string
		if dec.WantGit {
			if !startup.GitAvailable() {
				notes = append(notes, "git is unavailable; continuing without a repository.")
			} else if err := startup.InitGit(d.Dir); err != nil {
				notes = append(notes, fmt.Sprintf("git init failed: %v; continuing without a repository.", err))
			}
		}

		identity, err := project.EnsureWith(d.Dir, project.Metadata{Name: dec.Project, Worker: dec.Worker})
		if err != nil {
			return tui.IdentityUpdated{}, "", fmt.Errorf("commit project: %w", err)
		}
		held.projectID = identity.ID
		opts.projectID = identity.ID

		git := startup.DescribeGit(d.Dir)
		branch, gitStatus := git.BranchStatus()
		facts := tui.IdentityUpdated{
			User:         identity.WorkerName(),
			Greeting:     startup.Greeting(identity.WorkerName(), time.Now()),
			GitBranch:    branch,
			GitStatus:    gitStatus,
			GitStaged:    git.Summary.Staged,
			GitModified:  git.Summary.Unstaged,
			GitUntracked: git.Summary.Untracked,
			GitKnown:     git.Known,
			Ready:        "ready",
		}

		if store != nil && held.sess == nil {
			if serr := attachWizardSession(cmd, opts, registry, store, held, program, identity.ID, resumeID); serr != nil {
				return tui.IdentityUpdated{}, "", serr
			}
		}

		notice := "runtime ready"
		if name := identity.WorkerName(); name != "" {
			notice = "Welcome, " + name + " — runtime ready"
		}
		if len(notes) > 0 {
			notice = strings.Join(notes, "\n") + "\n" + notice
		}
		return facts, notice, nil
	}
}

// attachWizardSession creates (or resumes) the session for a newly
// onboarded project and wires it into the running program. The store
// predates the project identity, so this runs at commit time rather than
// before the UI starts.
func attachWizardSession(cmd *cobra.Command, opts *options, registry *ai.Registry, store *session.Store, held *harness, program *tui.Program, projectID string, resumeID int64) error {
	if resumeID > 0 {
		sess, err := store.Get(resumeID)
		if err != nil {
			if errors.Is(err, session.ErrSessionNotFound) {
				return fmt.Errorf("%w: %d", session.ErrSessionNotFound, resumeID)
			}
			return err
		}
		history, err := loadHistory(store, &sess)
		if err != nil {
			return err
		}
		if err := applySessionAuthority(cmd, opts, dynamicResolver(context.Background(), registry), store, &sess); err != nil {
			return err
		}
		// The loop predates the authority restore, so it moves when the
		// session names a different model than the resolved one.
		if current := held.model.Provider + "/" + held.model.ID; current != opts.model.Provider+"/"+opts.model.ID {
			if _, err := held.SwitchModel(context.Background(), opts.model.Provider+"/"+opts.model.ID); err != nil {
				return fmt.Errorf("resume: session model: %w", err)
			}
		}
		held.workspace = opts.authority().Label()
		// The loop predates the authority restore, so its gate follows
		// the restored workspace.
		if held.loop != nil {
			held.loop.SetAdmitHook(admitFor(opts.authority()))
		}
		sessCopy := sess
		held.sess = &sessCopy
		program.AttachSession(store, held.sess, history)
		program.PublishWorkspace(tui.WorkspaceUpdated{
			HasSession: true,
			Session:    sessionLabel(held.sess),
		})
		return nil
	}

	sess, err := store.CreateWith(session.CreateOptions{
		Cwd:       opts.root,
		Model:     opts.model.Provider + "/" + opts.model.ID,
		Workspace: opts.authority().String(),
		Project:   projectID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: sessions disabled: %v\n", err)
		return nil
	}
	sessCopy := sess
	held.sess = &sessCopy
	program.AttachSession(store, held.sess, nil)
	program.PublishWorkspace(tui.WorkspaceUpdated{
		HasSession: true,
		Session:    sessionLabel(held.sess),
	})
	return nil
}

// History is a parameter so a resumed session can be seeded from a stored
// transcript. It is kept in the caller-owned slice rather than in the loop
// because each Run is a turn, not a conversation: handing Run a single message
// would make every turn start from nothing, which is precisely the bug this
// shape is designed to prevent.
//
// Commands dispatch through the same harness the full-screen UI uses, so both
// interfaces agree on what each command means.
func repl(h *harness, out io.Writer, in io.Reader, history []agent.Message) error {
	reader := bufio.NewReader(in)
	pending := ""
	for {
		var text string
		if pending != "" {
			text, pending = pending, ""
		} else {
			_, _ = fmt.Fprint(out, "\n> ")
			line, err := reader.ReadString('\n')
			if err == io.EOF {
				_, _ = fmt.Fprintln(out)
				return nil
			}
			if err != nil {
				return fmt.Errorf("read input: %w", err)
			}
			text = strings.TrimSpace(line)
			if text == "" {
				continue
			}
		}

		// Commands are recognised before anything is sent to the model: a typo
		// like "/modle" would otherwise cost a turn and produce a confused
		// answer.
		if command, ok := tui.ParseCommand(text); ok {
			switch command.Name {
			case "quit", "exit", "q":
				return nil
			case "help":
				_, _ = fmt.Fprintln(out, tui.HelpText())
				continue
			case "clear":
				history = nil
				_, _ = fmt.Fprintln(out, "Transcript cleared.")
				continue
			case "model":
				// The plain REPL switches models the same way the full-screen
				// UI does, including live discovery for ids the bundle never
				// saw — one resolver serves both.
				if len(command.Args) == 0 {
					_, _ = fmt.Fprintln(out, "usage: /model provider/id — e.g. /model groq/llama-3.1-8b-instant")
					continue
				}
				model, err := h.SetModel(command.Args[0])
				if err != nil {
					_, _ = fmt.Fprintf(out, "unknown model %q: %v\nrun `ohgo models` to list them\n", command.Args[0], err)
					continue
				}
				_, _ = fmt.Fprintf(out, "Switched to %s/%s.\n", model.Provider, model.ID)
				continue
			case "models":
				for _, id := range h.ModelList() {
					_, _ = fmt.Fprintln(out, "  "+id)
				}
				continue
			case "status":
				_, _ = fmt.Fprintln(out, h.StatusReport(history, 0, "full-screen UI only", false))
				continue
			case "usage":
				_, _ = fmt.Fprintln(out, h.UsageReport())
				continue
			case "doctor":
				for _, line := range h.DoctorLines() {
					status := "ok"
					if !line.OK {
						status = "FAIL"
					}
					_, _ = fmt.Fprintf(out, "%-10s %-12s %s\n", status, line.Name, line.Detail)
				}
				continue
			case "retry":
				last, ok := agent.LastUserText(history)
				if !ok {
					_, _ = fmt.Fprintln(out, "nothing to retry: no prompt yet")
					continue
				}
				pending = last
				continue
			case "compact":
				compacted, summary, err := h.CompactHistory(context.Background(), history)
				if err != nil {
					_, _ = fmt.Fprintf(out, "compact: %v\n", err)
					continue
				}
				history = compacted
				_, _ = fmt.Fprintf(out, "Compacted: %s.\n", summary)
				continue
			case "connect":
				if err := h.ConnectREPL(out, reader); err != nil {
					_, _ = fmt.Fprintf(out, "connect: %v\n", err)
				}
				continue
			case "disconnect":
				provider := h.model.Provider
				if len(command.Args) > 0 {
					provider = command.Args[0]
				}
				outcome, err := h.Disconnect(provider)
				if err != nil {
					_, _ = fmt.Fprintf(out, "disconnect: %v\n", err)
					continue
				}
				_, _ = fmt.Fprintln(out, outcome)
				continue
			case "sessions":
				h.ListSessions(out)
				continue
			case "resume":
				if len(command.Args) == 0 {
					_, _ = fmt.Fprintln(out, "usage: /resume <id>")
					continue
				}
				var id int64
				if _, err := fmt.Sscanf(command.Args[0], "%d", &id); err != nil {
					_, _ = fmt.Fprintf(out, "invalid session id %q\n", command.Args[0])
					continue
				}
				relayed, notice, err := h.ResumeSession(id)
				if err != nil {
					_, _ = fmt.Fprintf(out, "resume: %v\n", err)
					continue
				}
				history = relayed
				_, _ = fmt.Fprintln(out, notice)
				continue
			case "jobs":
				_, _ = fmt.Fprintln(out, h.JobsReport())
				continue
			case "ask", "fuzz", "build":
				ws, err := h.SwitchWorkspace(command.Name)
				if err != nil {
					_, _ = fmt.Fprintf(out, "workspace: %v\n", err)
					continue
				}
				_, _ = fmt.Fprintf(out, "switched to %s.\n", ws.Label())
				continue
			case "theme":
				_, _ = fmt.Fprintln(out, "theme applies to the full-screen UI (dark or light via /theme there)")
				continue
			case "commands":
				_, _ = fmt.Fprintln(out, tui.HelpText())
				continue
			default:
				text := fmt.Sprintf("unknown command /%s — try /help", command.Name)
				if near, ok := tui.SuggestCommand(command.Name); ok {
					text = fmt.Sprintf("unknown command /%s — did you mean /%s?", command.Name, near)
				}
				_, _ = fmt.Fprintln(out, text)
				continue
			}
		}

		// $sup opens the ephemeral support surface: an isolated conversation
		// that is never appended to the main history and never persisted
		// (§10.2, I17). A bare "$sup" closes it.
		if question, ok := tui.ParseSupport(text); ok {
			if strings.TrimSpace(question) == "" {
				h.CloseSupport()
				_, _ = fmt.Fprintln(out, "support closed.")
				continue
			}
			answer, err := h.Support(context.Background(), question)
			if err != nil {
				_, _ = fmt.Fprintf(out, "$sup: %v\n", err)
				continue
			}
			_, _ = fmt.Fprintln(out, answer)
			continue
		}

		// The prompt is appended to a history file best-effort, so a plain-REPL
		// user keeps an audit trail across runs. A failure here never fails the
		// turn: the conversation in memory is what matters.
		recordReplHistory(text)

		// The user's own turn is persisted alongside the assistant's, or a
		// replay would start with the answer to a question it never saw.
		if h.store != nil && h.sess != nil {
			if stored, err := session.FromAgentMessage(agent.UserMessage{Text: text}); err == nil && stored.Role != "" {
				if err := h.store.Append(h.sess.ID, stored); err != nil {
					fmt.Fprintf(os.Stderr, "ohgo: could not save turn: %v\n", err)
				}
			}
		}

		user := agent.UserMessage{Text: text}
		request := append(append([]agent.Message(nil), history...), user)

		turns, err := h.loop.Run(context.Background(), request)
		if err != nil {
			_, _ = fmt.Fprintf(out, "error: %v\n", err)
			continue
		}
		if h.usage != nil {
			h.usage.Add(turns)
		}
		// Whatever the loop actually sent replaces the accumulator, including
		// any compaction it applied, so the next turn starts from the real
		// context rather than a stale copy.
		history = rebaseHistory(request, turns)
	}
}

// replHistoryMaxLines caps the prompt history file, so it stays an audit
// trail rather than growing without bound.
const replHistoryMaxLines = 1000

// replHistoryPath resolves where plain-REPL prompts are recorded.
//
// OHGO_HISTORY_FILE overrides the default; setting it to the empty string
// disables recording. The default is ~/.ohgo/history, next to the session
// store. Tests set the variable to a temp file (or empty) so a scripted REPL
// never touches the real home directory.
func replHistoryPath() string {
	if value, ok := os.LookupEnv("OHGO_HISTORY_FILE"); ok {
		return strings.TrimSpace(value)
	}
	path, err := state.Path("history")
	if err != nil {
		return ""
	}
	return path
}

// recordReplHistory appends one user line to the history file, best-effort.
//
// It never returns an error to the caller: losing an audit line is acceptable,
// interrupting a working turn over it is not. Failures are reported to stderr
// once, at the point they happen, so a broken path does not fail silently on
// every turn without anyone noticing.
func recordReplHistory(text string) {
	path := replHistoryPath()
	if path == "" || strings.TrimSpace(text) == "" {
		return
	}
	if err := appendReplHistoryLine(path, text); err != nil {
		fmt.Fprintf(os.Stderr, "ohgo: could not record history: %v\n", err)
	}
}

// appendReplHistoryLine appends a line and rotates the file to the tail.
func appendReplHistoryLine(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintln(f, text)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return rotateReplHistory(path)
}

// rotateReplHistory keeps only the last replHistoryMaxLines lines.
func rotateReplHistory(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	// A trailing newline produces one empty final element; it is not a line.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= replHistoryMaxLines {
		return nil
	}
	tail := strings.Join(lines[len(lines)-replHistoryMaxLines:], "\n") + "\n"
	return os.WriteFile(path, []byte(tail), 0o644)
}

// rebaseHistory rebuilds the running history after one turn.
//
// Compaction rewrites the context, so appending the new messages to the old
// list would silently reintroduce everything the summary just removed. Only
// the user turn plus whatever the assistant produced is kept.
func rebaseHistory(request []agent.Message, turns []agent.Turn) []agent.Message {
	out := append([]agent.Message(nil), request...)
	for _, turn := range turns {
		out = append(out, agent.AssistantMessage{Message: turn.Assistant})
		for _, outcome := range turn.Results {
			out = append(out, agent.ToolResultMessage{
				ToolCallID: outcome.CallID,
				ToolName:   outcome.ToolName,
				Content:    []ai.ContentBlock{ai.TextContent{Text: outcome.Result.Text}},
				IsError:    outcome.Result.IsError,
			})
		}
	}
	return out
}
