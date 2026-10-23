package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/contract"
)

// ReadOnlyToolNames is the set of built-in tools whose approval tier is "read".
//
// It mirrors the original's READ_ONLY_TOOL_NAMES. An agent is read-only iff its
// declared tools are a non-empty subset of this set. The fail-safe direction
// matters: any unknown tool makes the agent *not* read-only, because assuming
// write access is safe would let an unrecognised tool mutate the repository.
var ReadOnlyToolNames = map[string]bool{
	"read":    true,
	"wait":    true,
	"grep":    true,
	"glob":    true,
	"find":    true,
	"todo":    true,
	"yield":   true,
	"ask":     true,
	"recall":  true,
	"reflect": true,
	"retain":  true,
	"symbols": true,
	"graph":   true,
	"refs":    true,
}

// IsReadOnly reports whether a tool set can only observe.
//
// An empty set is not read-only: it declares nothing, so nothing is known about
// what the agent may do, and the safe answer to "unknown" is "not read-only".
func IsReadOnly(tools []Tool) bool {
	if len(tools) == 0 {
		return false
	}
	for _, tool := range tools {
		if !ReadOnlyToolNames[tool.Name()] {
			return false
		}
	}
	return true
}

// ErrNoSubagentResult is returned when a subagent finished without producing
// any assistant text.
//
// An empty result would read as "the subagent answered nothing", which is
// indistinguishable from a silent failure. Returning the parent an error
// instead keeps the two apart.
var ErrNoSubagentResult = errors.New("agent: subagent produced no output")

// ErrNoSystemPrompt is returned when a subagent was given no system prompt.
//
// The absence is an error rather than a fallback to the parent's prompt, because
// handing a child an identity it does not have is exactly what isolation exists
// to prevent.
var ErrNoSystemPrompt = errors.New("agent: subagent requires its own system prompt")

// SubagentSpec describes one child agent.
type SubagentSpec struct {
	// Task is the assignment, sent as the child's only user message.
	Task string
	// System is the child's system prompt, in order. It is not the parent's:
	// a subagent that inherited the parent's identity would behave as though it
	// were the main agent, which is the failure mode this field exists to
	// prevent.
	System []ai.SystemPromptBlock
	// Tools are offered to the child. When empty the parent's set is used.
	Tools []Tool
	// MaxTokens caps each completion. Zero inherits the parent's budget.
	MaxTokens int
	// Reasoning and ReasoningEffort are inherited when zero.
	Reasoning       bool
	ReasoningEffort string
	// Depth is this subagent's nesting level, used with MaxDepth to bound
	// recursion.
	Depth int
	// ReadOnly requests that the child be restricted to observing. When set the
	// tool set must be read-only; a set containing a write-tier tool is refused
	// rather than silently filtered, because silently dropping a tool the task
	// needs would make the child fail for a reason it cannot see.
	ReadOnly bool
	// MutationIntent reports that the assignment clearly asks for a repository
	// change. The child's completion verdict uses it to require an observed
	// change; the parent supplies it because intent classification belongs to
	// the workspace layer, not the agent loop.
	MutationIntent bool
	// Contract is the bounded completion contract for this assignment, when
	// the spawner captured one. It replaces MutationIntent for the verdict.
	Contract *contract.IntentContract
	// Snapshot is the child's pre-run target evidence.
	Snapshot *TargetSnapshot
	// Resolver locates the contract's symbols and packages after the child
	// runs. It is the same structural index the parent already owns.
	Resolver StructuralResolver
	// Project installs a task-shaped projection on the child loop. The parent
	// builds it from the assignment's own contract and evidence, never from
	// the parent's conversation: a child must not inherit the parent's state.
	Project func(ProjectionRequest) ProjectionResult
	// OnProjection reports the child's projection decisions, for a host that
	// is auditing a delegated tree.
	OnProjection func(ProjectionReport)
}

// SubagentResult is what a child agent produced.
type SubagentResult struct {
	// Text is the child's final assistant text, which is what the parent's tool
	// result carries.
	Text string
	// Turns is the child's turn list, for callers that want more than the final
	// answer.
	Turns []Turn
	// ToolCalls counts the tool calls the child made.
	ToolCalls int
	// ReadOnly reports whether the child could only observe.
	ReadOnly bool
	// Completion is the child's evidence-based completion verdict. A parent
	// must not inherit a clean-looking answer when the child's own objective
	// was not met.
	Completion Completion
	// Mutated reports that the child changed the workspace.
	Mutated bool
	// Executed reports that the child ran a process to a successful terminal
	// state. It lets a parent's execution objective inherit observed child
	// execution instead of treating the child's answer as proof it ran.
	Executed bool
	// Verified reports that the child successfully verified its last change.
	Verified bool
	// Verification records the scopes the child's successful checks covered,
	// so the parent's scoped verification requirement can be satisfied by the
	// child's real command rather than by an unscoped boolean.
	Verification *VerificationEvidence
	// Budget is the shared root grant after the child ran. It is a snapshot,
	// never a fresh budget: the parent and child consumed the same instance.
	Budget budget.Snapshot
}

// RunSubagent runs a child loop derived from a parent configuration.
//
// The isolation is the point. A subagent that shared the parent's message
// history would inherit a conversation about a different problem, and its
// answers would be coloured by context the parent never intended it to see.
// It therefore starts from exactly one user message — the task — and its own
// system prompt.
//
// Everything that is not context is inherited: the stream, the model, the
// approval policy, and compaction. A child that could not write should be
// stopped by its tool set, not by a flag the caller might forget to set.
func RunSubagent(ctx context.Context, parent Config, spec SubagentSpec) (SubagentResult, error) {
	if strings.TrimSpace(spec.Task) == "" {
		return SubagentResult{}, errors.New("agent: subagent task is empty")
	}
	if len(spec.System) == 0 {
		// Deliberately not inherited: falling back to the parent's prompt would
		// hand the child an identity it does not have.
		return SubagentResult{}, ErrNoSystemPrompt
	}

	tools := spec.Tools
	if len(tools) == 0 {
		tools = parent.Tools
	}
	if len(tools) == 0 {
		return SubagentResult{}, ErrNoTools
	}

	// A read-only request against a tool set that can write is refused, not
	// filtered. Dropping the offending tool would leave the child unable to do
	// part of its task with no way to report why.
	if spec.ReadOnly && !IsReadOnly(tools) {
		return SubagentResult{}, fmt.Errorf(
			"agent: subagent requested read-only, but its tool set can write (%s)",
			firstWriteTool(tools))
	}

	maxTokens := spec.MaxTokens
	if maxTokens <= 0 {
		maxTokens = parent.MaxTokens
	}

	// A direct RunSubagent caller may not have built a root grant. Normalise
	// one here from the parent's own bounds so the child still shares a root
	// rather than receiving an independent counter it could reset. Production
	// always supplies an explicit holder shared with the parent loop.
	if parent.Budget == nil {
		turns := parent.MaxTurns
		if turns <= 0 {
			turns = DefaultMaxTurns
		}
		parent.Budget = budget.New(budget.Limits{
			Turns:         turns,
			ModelCalls:    turns,
			Deadline:      parent.Deadline,
			ExternalCalls: turns,
			ExternalBytes: 16 * 1024 * 1024,
		})
	}
	// Delegation is a resource of the same tree, not a free escape from it.
	// Admit before constructing or running the child; a refused admission
	// produces no child at all.
	if err := parent.Budget.Admit(budget.DimensionChildren); err != nil {
		return SubagentResult{}, fmt.Errorf("agent: subagent: %w", err)
	}

	child := Config{
		Model:           parent.Model,
		ModelFn:         parent.ModelFn,
		Stream:          parent.Stream,
		System:          spec.System,
		Tools:           tools,
		MaxTokens:       maxTokens,
		Reasoning:       spec.Reasoning || parent.Reasoning,
		ReasoningEffort: firstNonEmpty(spec.ReasoningEffort, parent.ReasoningEffort),
		ToolChoice:      parent.ToolChoice,
		SessionID:       parent.SessionID,
		// A child may not rewrite the parent's context, and its own compaction
		// would need its own summarizer to be correct.
		TransformContext:    nil,
		GetToolChoice:       nil,
		GetSteeringMessages: nil,
		BeforeToolCall:      parent.BeforeToolCall,
		AfterToolCall:       parent.AfterToolCall,
		// Admission and approval are inherited, never defaulted: a child that
		// lost the parent's authority seam would be a way around the security
		// boundary. A delegated turn narrows what the child may do, never
		// widens it.
		Admit:        parent.Admit,
		Approve:      parent.Approve,
		Authorize:    parent.Authorize,
		Compaction:   parent.Compaction,
		Summarize:    parent.Summarize,
		OnCompact:    parent.OnCompact,
		Project:      spec.Project,
		OnProjection: spec.OnProjection,
		// The parent's wall-clock budget bounds the whole delegated tree. A
		// child that dropped it would keep spending provider calls after the
		// user's --deadline had already expired.
		Deadline: parent.Deadline,
		// The turn budget is inherited for the same reason: delegation must
		// not be a way around the bound that keeps a single run finite.
		MaxTurns: parent.MaxTurns,
		// The same root grant, never a copy with reset counters. Every child
		// and grandchild is therefore visible in the root's aggregate.
		Budget: parent.Budget,
		// Progress and turns are the parent's concern; a child that emitted
		// them would double-count into a UI that is already showing the
		// parent's turn.
		OnTurn:         nil,
		OnToolProgress: nil,
	}

	loop, err := New(child)
	if err != nil {
		return SubagentResult{}, fmt.Errorf("agent: build subagent loop: %w", err)
	}

	turns, err := loop.Run(ctx, []Message{UserMessage{Text: spec.Task}})
	if err != nil {
		return SubagentResult{}, err
	}

	text := ""
	toolCalls := 0
	for _, turn := range turns {
		toolCalls += len(turn.Results)
		if t := strings.TrimSpace(turn.Assistant.Text()); t != "" {
			// A later turn is more considered than an earlier one: between them
			// the child read tool output it did not have before.
			text = t
		}
	}
	var completion Completion
	if spec.Contract != nil {
		completion = EvaluateTaskCompletion(CompletionInput{
			Turns:    turns,
			Contract: *spec.Contract,
			Snapshot: spec.Snapshot,
			Resolver: spec.Resolver,
		})
	} else {
		completion = EvaluateCompletion(turns, spec.MutationIntent)
	}
	evidence := scanCompletionEvidence(turns, spec.MutationIntent)
	mutated := evidence.mutated
	verified := evidence.mutated && evidence.lastVerification >= evidence.lastPotentialMutation
	executed := evidence.executed
	verification := verificationFromTurns(turns)
	if text == "" {
		return SubagentResult{
			Turns:        turns,
			ToolCalls:    toolCalls,
			Completion:   completion,
			Mutated:      mutated,
			Executed:     executed,
			Verified:     verified,
			Verification: verification,
			Budget:       parent.Budget.Snapshot(),
		}, ErrNoSubagentResult
	}

	return SubagentResult{
		Text:         text,
		Turns:        turns,
		ToolCalls:    toolCalls,
		ReadOnly:     IsReadOnly(tools),
		Completion:   completion,
		Mutated:      mutated,
		Executed:     executed,
		Verified:     verified,
		Verification: verification,
		Budget:       parent.Budget.Snapshot(),
	}, nil
}

// verificationFromTurns collects the successful check scopes a child produced.
// It is the child's evidence passed to the parent, not a claim the parent makes
// up from the child's answer.
func verificationFromTurns(turns []Turn) *VerificationEvidence {
	var (
		command string
		check   contract.VerificationCheck
		mixed   bool
		scopes  []VerificationScope
		seen    = map[VerificationScope]bool{}
	)
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if !outcome.Result.Verifies || outcome.Result.Verification == nil {
				continue
			}
			if command == "" {
				command = outcome.Result.Verification.Command
			}
			recordCheck := outcome.Result.Verification.EffectiveCheck()
			if recordCheck != contract.CheckAny {
				switch {
				case check == "" || check == contract.CheckAny:
					check = recordCheck
				case check != recordCheck:
					mixed = true
				}
			}
			for _, scope := range outcome.Result.Verification.Scopes {
				if !seen[scope] {
					seen[scope] = true
					scopes = append(scopes, scope)
				}
			}
		}
	}
	if command == "" && len(scopes) == 0 {
		return nil
	}
	if mixed {
		// Mixed check kinds cannot be represented as one evidence kind; the
		// conservative value is Any, which does not satisfy a specific check
		// requirement.
		check = contract.CheckAny
	}
	if check == "" {
		check = contract.CheckAny
	}
	return &VerificationEvidence{Command: command, Check: check, Scopes: scopes}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// firstWriteTool names a tool that can mutate state, for an error message.
func firstWriteTool(tools []Tool) string {
	for _, tool := range tools {
		if !ReadOnlyToolNames[tool.Name()] {
			return tool.Name()
		}
	}
	return "(none)"
}
