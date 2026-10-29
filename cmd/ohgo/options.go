package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/project"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/tui"
	"github.com/PizenLabs/ohgo/workspace"
	"github.com/spf13/cobra"
)

// options holds the resolved CLI settings for one invocation.
type options struct {
	// root is the working directory for every tool and command.
	root string
	// workspace is the execution authority the session runs under. It decides
	// capability policy, not presentation (§8).
	workspace workspace.Workspace
	// gate is the live authority the loop and any subagent read at call time.
	// It is non-nil once resolveOptions has run; tests that build options
	// directly leave it nil and fall back to workspace.
	gate *authorityGate
	// security is the runtime's authorization boundary. It is installed on the
	// loop and inherited by subagents, so every executable capability passes
	// through one decision. Nil until buildLoop (or a TUI caller) creates it.
	security *securityGate
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
	// maxTurns bounds the model turns in one run. Zero means the agent
	// package's default.
	maxTurns int
	// Tree-wide grants derived from the controlling task. Zero means "derive
	// from maxTurns/maxDepth" in budgetLimits.
	maxModelCalls     int
	maxToolCalls      int
	maxChildren       int
	maxBackgroundJobs int
	// budget is the stable holder shared by the loop and every spawner, so a
	// run boundary can install a fresh root grant without rebuilding tools.
	budget *budget.Holder
	// deadline bounds the whole run in wall-clock time. The zero value is no
	// limit.
	deadline time.Time
	// settings is the loaded configuration.
	settings config.Settings
	// mcpRuntime holds the session's one MCP manager. It is shared by the
	// tools and the UI inspector, and closed at session shutdown.
	mcpRuntime *mcpRuntime
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
// The live gate wins when one is installed: it moves only on an explicit
// workspace switch. An unset workspace defaults to BUILD. The CLI always
// resolves one through workspace.Parse (where a typo is fatal), but a caller
// that predates workspaces — or a test that builds options directly — keeps the
// authority it always had rather than being silently denied everything.
func (o options) authority() workspace.Workspace {
	if o.gate != nil {
		if ws := o.gate.Workspace(); ws.Valid() {
			return ws
		}
	}
	if o.workspace.Valid() {
		return o.workspace
	}
	return workspace.Build
}

// setAuthority records the session's active workspace on both the value and
// the live gate, so option resolution, session restore, and workspace switches
// agree on one authority (§30).
func (o *options) setAuthority(ws workspace.Workspace) {
	o.workspace = ws
	if o.gate == nil {
		o.gate = newAuthorityGate(ws)
		return
	}
	o.gate.Set(ws)
}

// resolveOptions builds options from the flags and environment, plus the
// registry the run will use. One registry serves resolution and the loop so
// both agree on credentials and overrides.
func resolveOptions(cmd *cobra.Command) (options, *ai.Registry, error) {
	opts := options{settings: config.Default(), mcpRuntime: &mcpRuntime{}}

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
	opts.setAuthority(workspace.Build)
	if name := flagValue(cmd, "workspace"); name != "" {
		parsed, err := workspace.Parse(name)
		if err != nil {
			return opts, nil, err
		}
		opts.setAuthority(parsed)
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

	// The turn cap is independent of the wall-clock deadline: a model that
	// keeps proposing tools is bounded even when the user set no deadline.
	opts.maxTurns = agent.DefaultMaxTurns
	if turns, _ := cmd.Flags().GetInt("max-turns"); turns > 0 {
		opts.maxTurns = turns
	}
	opts.maxModelCalls, _ = cmd.Flags().GetInt("max-model-calls")
	opts.maxToolCalls, _ = cmd.Flags().GetInt("max-tool-calls")
	opts.maxChildren, _ = cmd.Flags().GetInt("max-children")
	opts.maxBackgroundJobs, _ = cmd.Flags().GetInt("max-background-jobs")

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
	opts.budget = budget.NewHolder(opts.newBudget())
	return opts, registry, nil
}

// closeMCP stops every MCP connection the session opened. It is safe on a
// session that never enabled MCP.
func (o options) closeMCP() {
	if o.mcpRuntime != nil {
		_ = o.mcpRuntime.close()
	}
}

// budgetLimits derives the tree-wide grant from the user's explicit knobs.
//
// A derived value is not a hidden preference: it is a deterministic function
// of a flag the user can see, and every dimension has a finite default. The
// multipliers leave room for the real shape of a turn (several tools per model
// call) without letting one dimension grow without bound.
func (o options) budgetLimits() budget.Limits {
	turns := o.maxTurns
	if turns <= 0 {
		turns = agent.DefaultMaxTurns
	}
	modelCalls := o.maxModelCalls
	if modelCalls <= 0 {
		// Compaction issues a provider request of its own. Give the derived
		// grant a small allowance so routine summarisation does not masquerade
		// as model runaway; an explicit flag replaces this entirely.
		modelCalls = turns + turns/10
		if modelCalls < turns+4 {
			modelCalls = turns + 4
		}
	}
	toolCalls := o.maxToolCalls
	if toolCalls <= 0 {
		toolCalls = turns * 8
	}
	children := o.maxChildren
	if children <= 0 {
		children = o.maxDepth * 4
	}
	backgroundJobs := o.maxBackgroundJobs
	if backgroundJobs <= 0 {
		backgroundJobs = turns
	}
	// External knowledge is bounded by the same root grant. One external call
	// is at most one tool call's worth of egress, and the aggregate byte cap is
	// a fixed finite allowance that a response cannot exceed (the per-request
	// reader is additionally capped at the remaining grant).
	//
	// A floor keeps one discovery usable: initialize, every tools/list page,
	// and the optional resources/list are separate metered exchanges. Without
	// it, a small --max-tool-calls value makes a paginated MCP server fail
	// before it has advertised a single capability.
	externalCalls := toolCalls
	if externalCalls < 16 {
		externalCalls = 16
	}
	externalBytes := 16 * 1024 * 1024
	return budget.Limits{
		Turns:          turns,
		ModelCalls:     modelCalls,
		ToolCalls:      toolCalls,
		Children:       children,
		BackgroundJobs: backgroundJobs,
		ExternalCalls:  externalCalls,
		ExternalBytes:  externalBytes,
		Deadline:       o.deadline,
	}
}

// newBudget returns a fresh root grant for one run.
func (o options) newBudget() *budget.Budget {
	return budget.New(o.budgetLimits())
}

// ensureBudget returns the shared holder, creating one for a caller that built
// options directly (tests, embedding hosts) rather than through resolveOptions.
func (o options) ensureBudget() *budget.Holder {
	if o.budget != nil {
		return o.budget
	}
	return budget.NewHolder(o.newBudget())
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
		resolved, err := resolve(ai.Ref(provider, id))
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
