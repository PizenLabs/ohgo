package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/prompt"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

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
	// One holder per tool set: the top-level loop and every spawner built here
	// must resolve the same current grant.
	opts.budget = opts.ensureBudget()
	// The wait tool observes the bash tool's job table, so both are built
	// from the same instance: two tables would let bash start jobs no wait
	// call could ever see.
	bash := tools.NewBashTool(opts.root)
	// One structural source serves both coordinate and relationship queries.
	// Constructing it does not parse the repository; the first structural
	// query does, and every later graph query reads that same snapshot.
	structural := tools.NewStructuralSource(opts.root)
	toolSet := []agent.Tool{
		tools.NewReadTool(opts.root),
		tools.NewWriteTool(opts.root),
		tools.NewEditTool(opts.root),
		tools.NewDeleteTool(opts.root),
		bash,
		tools.NewWaitTool(bash.Jobs()),
		tools.NewGlobTool(opts.root),
		tools.NewGrepTool(opts.root),
		tools.NewLsTool(opts.root),
		tools.NewSymbolsToolWithSource(structural),
		tools.NewGraphTool(structural),
		tools.NewRefsTool(structural),
		tools.NewWebFetchTool(),
		tools.NewTodoTool(),
	}
	toolSet = append(toolSet, externalTools(opts.settings, opts.root, opts.mcpRuntime)...)

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
		// A child must never run with a nil authorizer. Production installs one
		// before building tools; this fallback keeps a direct caller (a test or
		// an embedding host) fail-closed instead of silently admitting every
		// call through the legacy path.
		gate := opts.security
		if gate == nil {
			gate = opts.newSecurityGate(nil)
		}
		// A writable child gets the same bounded contract and pre-run target
		// evidence as the top-level turn. A read-only child is not asked to
		// prove a mutation it was forbidden to make.
		intent := workspace.BuildIntentContract(spec.Task)
		if spec.Contract != nil {
			intent = *spec.Contract
		}
		if !spec.ReadOnly {
			resolver := structuralResolver(parentTools)
			spec.Contract = &intent
			spec.Resolver = resolver
			if intent.Kind == workspace.ObjectiveMutation {
				spec.Snapshot = agent.CaptureTargetsContext(ctx, opts.root, intent, resolver)
			}
		}
		// A child gets its own shape and evidence, derived from the assignment
		// rather than inherited from the parent conversation. The projection
		// narrows the child's context and action surface; the inherited
		// security gate remains the only authority.
		authority := "ask"
		if !spec.ReadOnly {
			authority = opts.authority().String()
		}
		facts := cognition.ProbeFacts(opts.root)
		shape := cognition.BuildShape(intent, cognition.ShapeParams{
			Authority: authority,
			Intent:    "execute",
			Facts:     facts,
			Snapshot:  spec.Snapshot,
		})
		childProjector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)
		spec.Project = childProjector.Project
		var lastReport agent.ProjectionReport
		haveReport := false
		spec.OnProjection = func(report agent.ProjectionReport) {
			lastReport = report
			haveReport = true
			if contextAuditEnabled() {
				_, _ = fmt.Fprintln(os.Stderr, "[ohgo context/subagent]\n"+cognition.RenderAudit(report))
			}
		}
		spec.CompletionReview = func() string {
			if !haveReport {
				return ""
			}
			return lastReport.EvidenceReview()
		}
		childTools := childToolSet(opts, registry, parentTools, spec.Depth)
		if spec.ReadOnly {
			// A read-only request must actually restrict the child. Filtering
			// here, before RunSubagent's fail-closed validation, is what turns
			// the caller's intent into a child that cannot write. Refusing the
			// whole delegation instead would make the documented `readOnly`
			// option unusable.
			childTools = readOnlyToolSet(childTools)
		}
		parent := agent.Config{
			Model:      opts.model,
			Stream:     registry.StreamFn(),
			Tools:      childTools,
			MaxTokens:  opts.maxTokens,
			Budget:     opts.ensureBudget().Get(),
			Reasoning:  opts.reasoning,
			Admit:      opts.admit(),
			Authorize:  gate,
			Compaction: compactionFromSettings(opts.settings),
			Summarize:  newSummarizer(registry.StreamFn(), opts.model, opts.budget),
			// The spawner is a separate parent config from the top-level loop,
			// so every bound the user set must be copied here too. Omitting the
			// deadline would make the D-2 inheritance fix vacuous in production.
			Deadline: opts.deadline,
			MaxTurns: opts.maxTurns,
		}
		return agent.RunSubagent(ctx, parent, spec)
	})
}

// readOnlyToolSet keeps only tools that cannot change the workspace.
//
// The test names come from agent.ReadOnlyToolNames, the same set the executor
// uses to assert a child is read-only; duplicating that knowledge here would
// let the two drift.
func readOnlyToolSet(tools []agent.Tool) []agent.Tool {
	out := make([]agent.Tool, 0, len(tools))
	for _, tool := range tools {
		if agent.ReadOnlyToolNames[tool.Name()] {
			out = append(out, tool)
		}
	}
	return out
}

// childToolSet builds the tools a subagent at depth receives.
//
// It is a pure function so the depth bound is testable directly, without having
// to drive a whole model exchange to observe what a child was given.
//
// The task tool is omitted when the child sits at the cap. A bound checked at
// call time instead would be a rule someone can bypass; one baked into the tool
// set cannot be. Below the cap the task tool must carry a real spawner: a
// nil spawner would offer the child a tool that can only fail.
func childToolSet(opts options, registry *ai.Registry, parentTools []agent.Tool, depth int) []agent.Tool {
	if depth+1 >= opts.maxDepth {
		return parentTools
	}
	out := make([]agent.Tool, 0, len(parentTools)+1)
	out = append(out, parentTools...)
	return append(out, tools.NewTaskTool(
		newSubagentSpawner(opts, registry, parentTools), depth, opts.maxDepth, buildSubagentPrompt(opts)))
}

// buildLoop constructs the agent loop for a session.
func buildLoop(opts options, registry *ai.Registry, out io.Writer, store *session.Store, sess *session.Session, approve func(ai.ToolCall, agent.Approval) (bool, string)) (*agent.Loop, error) {
	// Pin one holder before building the tools, so the loop and the spawner
	// it creates cannot each derive a different root grant.
	opts.budget = opts.ensureBudget()
	// The security boundary is built before the tools so the subagent spawner
	// can inherit the same gate: a delegated child must cross the same
	// boundary as its parent, not a fresh one.
	if approve == nil && !opts.autoApprove {
		approve = func(call ai.ToolCall, approval agent.Approval) (bool, string) {
			return approveToolCall(out, opts, call, approval)
		}
	}
	if opts.security == nil {
		opts.security = opts.newSecurityGate(approve)
	} else if approve != nil {
		opts.security.SetAsk(approve)
	}
	if sess != nil {
		opts.security.SetSession(fmt.Sprintf("%d", sess.ID))
	}

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

	// The workspace contract is composed into the prompt on every request
	// rather than at construction, so a workspace switch is reflected
	// immediately. It explains the mode; it is not the enforcement boundary.
	// The project context is read once because it changes on disk, not on a
	// request.
	projectContext := prompt.ProjectContext(opts.root)
	systemPrompt := func() []ai.SystemPromptBlock {
		var parts []string
		if contract := prompt.WorkspaceContract(opts.authority().String()); contract != "" {
			parts = append(parts, contract)
		}
		if projectContext != "" {
			parts = append(parts, projectContext)
		}
		if opts.settings.SystemPrompt != "" {
			parts = append(parts, opts.settings.SystemPrompt)
		}
		return promptBlocks(opts, strings.Join(parts, "\n\n"))
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
		SystemFn:        systemPrompt,
		Tools:           toolSet,
		MaxTokens:       opts.maxTokens,
		Budget:          opts.budget.Get(),
		Deadline:        opts.deadline,
		MaxTurns:        opts.maxTurns,
		Reasoning:       opts.reasoning,
		ReasoningEffort: opts.effort,
		OnTurn:          onTurn,
		// The security boundary replaces the legacy admit/approve pair. The
		// gate performs the authority check, the policy decision, and (for an
		// APPROVE) the human approval, so there is exactly one path to
		// execution.
		Authorize:  opts.security,
		Compaction: compactionFromSettings(opts.settings),
		Summarize:  newSummarizer(registry.StreamFn(), opts.model, opts.budget),
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

// admit returns the loop's runtime gate, resolving the authority at call time.
//
// It is admitFor against the live options rather than a captured workspace, so
// an explicit workspace switch moves authority without rebuilding the loop. A
// test that builds options without a gate falls back to the resolved workspace.
func (o options) admit() func(ai.ToolCall, agent.Tool) (bool, string) {
	if o.gate != nil {
		return o.gate.Admit
	}
	return admitFor(o.authority())
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
func newSummarizer(stream ai.StreamFn, model ai.Model, holders ...*budget.Holder) agent.SummarizeFunc {
	return func(ctx context.Context, messages []agent.Message) (string, error) {
		// Compaction is a provider request. Charge it to the same root grant
		// when the caller supplied one, so summarisation cannot become an
		// unmetered side channel around the model-call budget.
		if len(holders) > 0 {
			if err := holders[0].Get().Admit(budget.DimensionModelCalls); err != nil {
				return "", err
			}
		}
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
