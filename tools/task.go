package tools

import (
	"context"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
)

// Spawner runs a subagent.
//
// It is an interface so the task tool never needs to know how an agent.Loop is
// constructed, and so tests can substitute a scripted child without a provider.
// A test that drove the real loop would assert on model behaviour rather than on
// the tool's contract.
type Spawner interface {
	Spawn(ctx context.Context, spec agent.SubagentSpec) (agent.SubagentResult, error)
}

// spawnFunc adapts a function to Spawner.
type spawnFunc func(ctx context.Context, spec agent.SubagentSpec) (agent.SubagentResult, error)

func (f spawnFunc) Spawn(ctx context.Context, spec agent.SubagentSpec) (agent.SubagentResult, error) {
	return f(ctx, spec)
}

// SystemBuilder renders the prompt a subagent is given for a task.
//
// It lives in this package rather than in agent because it describes what *this
// deployment* tells a subagent, which is a prompt concern, not an engine one.
type SystemBuilder func(task string) []ai.SystemPromptBlock

// TaskTool delegates a piece of work to a subagent with an isolated context.
//
// This is the only tool that creates another agent. It exists because a single
// context cannot hold both a plan and the detail of every step: the parent hands
// off a self-contained assignment and gets back a short answer, instead of
// filling its own context with the intermediate reads and greps.
//
// The isolation is enforced in agent.RunSubagent, which starts the child from a
// single user message and its own system prompt. This tool validates the
// arguments, bounds the recursion, and shapes the result.
type TaskTool struct {
	Base
	// Spawn runs the child. Required.
	Spawn Spawner
	// MaxDepth caps subagent nesting, counting the top-level agent. Zero means no
	// nesting at all beyond the top level, which is the safe default: an
	// unbounded tree of agents each spending tokens is the failure mode a depth
	// cap exists to prevent.
	MaxDepth int
	// Depth is this tool's nesting level. Zero at the top.
	Depth int
	// System renders the child's prompt. Required, so a child can never inherit
	// the parent's identity by default.
	System SystemBuilder
}

// NewTaskTool builds the task tool.
//
// maxDepth is the total nesting allowed, including the top-level agent. A value
// of 1 means only the top-level agent may delegate; 2 allows the subagent it
// spawns to delegate one level further.
func NewTaskTool(spawn Spawner, depth, maxDepth int, system SystemBuilder) *TaskTool {
	tool := &TaskTool{
		Spawn:    spawn,
		MaxDepth: maxDepth,
		Depth:    depth,
		System:   system,
	}
	tool.Base = NewBase("task",
		"Delegate a self-contained piece of work to a subagent. The subagent runs "+
			"with its own context and returns only its final answer, so the "+
			"intermediate reads and searches it performs do not consume your "+
			"context. Give it a task specific enough to complete without the "+
			"surrounding conversation.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "What the subagent should do. Must be self-contained: it cannot see this conversation.",
					"minLength":   1.0,
				},
				"context": map[string]any{
					"type":        "string",
					"description": "Background the subagent needs but could not discover itself.",
				},
				"readOnly": map[string]any{
					"type":        "boolean",
					"description": "Restrict the subagent to read-only tools. Defaults to false.",
				},
			},
			"required": []string{"prompt"},
		},
		// Delegation itself only costs tokens, so it is a read-tier decision:
		// the subagent's own tools carry their own approval tiers.
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
}

// canNest reports whether a subagent spawned from here may itself delegate.
//
// It exists so the depth cap is checked in one place rather than re-derived at
// each construction site, where it would drift.
func (t *TaskTool) canNest() bool {
	return t.MaxDepth > 0 && t.Depth+1 < t.MaxDepth
}

// Execute implements agent.Tool.
func (t *TaskTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	if t.Spawn == nil {
		return agent.ErrorResult("task: no subagent spawner configured")
	}
	if t.System == nil {
		// A child with no system prompt would run with no role at all. Refusing
		// is better than producing an answer nobody can attribute.
		return agent.ErrorResult("task: no subagent system prompt configured")
	}

	prompt, err := strArg(args, "prompt", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	if strings.TrimSpace(prompt) == "" {
		return agent.ErrorResult("task: prompt must not be empty")
	}

	// Background is optional. A subagent that needs it and does not get it will
	// produce a confident answer about the wrong thing, which is worse than a
	// failure the parent can retry.
	background, err := strArg(args, "context", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}

	readOnly, err := boolArg(args, "readOnly", false)
	if err != nil {
		return agent.ErrorResult("%v", err)
	}

	system := t.System(prompt)
	if len(system) == 0 {
		return agent.ErrorResult("task: the system prompt builder returned no blocks")
	}
	if strings.TrimSpace(background) != "" {
		// Background goes last, so it survives any truncation of the shared
		// prefix, and is marked so the child does not mistake it for its
		// assignment.
		system = append(system, ai.SystemPromptBlock{
			Text:  "Background supplied by the main agent:\n" + background,
			Cache: true,
		})
	}

	spec := agent.SubagentSpec{
		Task:     prompt,
		System:   system,
		Depth:    t.Depth + 1,
		ReadOnly: readOnly,
	}

	result, err := t.Spawn.Spawn(ctx.Ctx, spec)
	if err != nil {
		// The child's error is passed through rather than wrapped into a
		// generic failure: the parent needs to know whether to retry, narrow
		// the task, or give up, and those need the original reason.
		return agent.ErrorResult("task: subagent failed: %v", err)
	}
	if strings.TrimSpace(result.Text) == "" {
		// Whitespace is not an answer. Accepting it would hand the parent a
		// result that looks successful and carries nothing, which is the same
		// silent-wrong-answer shape as an empty file listing.
		return agent.ErrorResult("task: subagent produced no output")
	}

	// The parent's answer is the child's answer plus what it cost. Without the
	// cost a parent cannot tell a cheap success from one that spent its whole
	// budget reading the repository.
	body := result.Text
	if meta := describeRun(result); meta != "" {
		body += "\n\n" + meta
	}
	return agent.Result{Text: body, Details: result}
}

// describeRun summarises what the child did.
//
// The read-only marker is reported even when it was not requested, because it
// tells the parent that the answer was derived by observation alone — useful
// when the child reported "the file does not exist" and the parent needs to
// know whether that was checked or assumed.
func describeRun(result agent.SubagentResult) string {
	var parts []string
	if result.ReadOnly {
		parts = append(parts, "read-only")
	}
	if result.ToolCalls > 0 {
		parts = append(parts, itoa(result.ToolCalls)+" tool calls")
	}
	if len(parts) == 0 {
		return ""
	}
	return "[subagent " + strings.Join(parts, ", ") + "]"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
