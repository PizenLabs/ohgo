package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/ai"
)

// ErrNoTools means the loop was started with an empty tool set.
var ErrNoTools = errors.New("agent: no tools registered")

// Turn is one request/response cycle.
type Turn struct {
	// Assistant is the message the model produced.
	Assistant ai.AssistantMessage
	// Results holds one entry per tool call, in the order the model emitted
	// them. It is empty when the model made no tool calls.
	Results []ToolOutcome
	// Err is set when the turn failed before any tool call was dispatched.
	Err error
}

// ToolOutcome records what happened to one tool call.
type ToolOutcome struct {
	// CallID is the id of the tool call this outcome answers.
	CallID string
	// ToolName is the tool that ran.
	ToolName string
	// Result is the tool's return value.
	Result Result
}

// Config configures a Loop.
type Config struct {
	// Model is the model to call. Use ModelFn for a dynamically resolved model.
	Model ai.Model
	// ModelFn resolves the model per request. When set it overrides Model.
	ModelFn func() ai.Model
	// Stream opens a completion. Required.
	Stream ai.StreamFn
	// System is the system prompt, in order.
	System []ai.SystemPromptBlock
	// Tools are offered to the model.
	Tools []Tool
	// MaxTokens caps each completion.
	MaxTokens int
	// Reasoning enables extended thinking.
	Reasoning bool
	// ReasoningEffort is the effort level forwarded to the provider.
	ReasoningEffort string
	// ToolChoice constrains tool use.
	ToolChoice ai.ToolChoice
	// SessionID is forwarded to providers that support session caching.
	SessionID string
	// TransformContext rewrites the agent messages before conversion.
	TransformContext func(messages []Message) []Message
	// GetToolChoice resolves the tool choice per turn.
	GetToolChoice func() ai.ToolChoice
	// GetSteeringMessages returns messages to inject before the next request.
	GetSteeringMessages func(ctx context.Context) []Message
	// BeforeToolCall can block or rewrite a call before it runs.
	BeforeToolCall func(call BeforeToolCallContext) BeforeToolCallResult
	// AfterToolCall can override fields of a completed result.
	AfterToolCall func(call AfterToolCallContext) AfterToolCallResult
	// OnTurn is called after each turn, for streaming progress to a UI.
	OnTurn func(Turn)
	// OnTextDelta receives visible model text while a stream is still open,
	// in order. A UI publishes each delta so the answer reveals as it is
	// generated instead of landing as one block when the turn completes.
	// Nil disables it; transports that never token-stream never fire it.
	OnTextDelta func(delta string)
	// Approve decides whether a call may run without prompting.
	Approve func(call ai.ToolCall, approval Approval) (bool, string)
	// Compaction configures context reduction. The zero value disables it.
	Compaction Compaction
	// Summarize replaces the compacted prefix during compaction. Required when
	// Compaction is enabled.
	Summarize SummarizeFunc
	// OnCompact reports each compaction, for logging and UI.
	OnCompact func(CompactResult)
	// Deadline bounds the whole run in wall-clock time. The zero value means no
	// limit, and every loop iteration makes a provider call, so time is what
	// actually costs money. Expiry returns the turns completed so far with a
	// nil error, because a deadline is a budget being spent rather than a
	// failure.
	Deadline time.Time
	// OnToolProgress reports partial and final outcomes for a tool call.
	//
	// It is how a long-running command streams output into a UI. Without it a
	// slow tool shows nothing until it finishes, which is exactly when the
	// user most wants to see what it is doing.
	OnToolProgress func(progress ToolProgress)
}

// ToolProgress is one report about a running tool call.
type ToolProgress struct {
	// CallID identifies the call.
	CallID string
	// ToolName is the tool running.
	ToolName string
	// Result holds the partial outcome, or the final one when Final is true.
	Result Result
	// Final marks the last report for this call.
	Final bool
	// Elapsed is how long the call has been running.
	Elapsed time.Duration
}

// ToolProgressStage is where a call is, for consumers that prefer a tag.
type ToolProgressStage string

// Progress stages.
const (
	// ProgressStarted fires before Execute, so a UI can show the call
	// immediately with no output yet.
	ProgressStarted ToolProgressStage = "started"
	// ProgressUpdate fires while the tool runs.
	ProgressUpdate ToolProgressStage = "update"
	// ProgressFinished fires once the outcome is final.
	ProgressFinished ToolProgressStage = "finished"
)

// Stage returns the stage this report represents.
func (p ToolProgress) Stage() ToolProgressStage {
	if p.Final {
		return ProgressFinished
	}
	return ProgressUpdate
}

// BeforeToolCallContext is passed to BeforeToolCall.
type BeforeToolCallContext struct {
	Tool  Tool
	Call  ai.ToolCall
	Args  map[string]any
	Turn  Turn
	Turns int
}

// BeforeToolCallResult can block a call or replace its arguments.
type BeforeToolCallResult struct {
	// Block prevents execution; the loop emits an error result instead.
	Block bool
	// Reason is the error text used when Block is true.
	Reason string
	// Args replaces the arguments the call runs with.
	Args map[string]any
}

// AfterToolCallContext is passed to AfterToolCall.
type AfterToolCallContext struct {
	Tool Tool
	Call ai.ToolCall
	Args map[string]any
	// Result is the outcome of execution, before any override.
	Result Result
}

// AfterToolCallResult overrides individual fields of a completed result.
//
// Omitted fields keep their executed values; there is no deep merge.
type AfterToolCallResult struct {
	Text    *string
	IsErr   *bool
	Useless *bool
	Details any
}

// Loop runs the model/tool cycle until the model stops calling tools.
type Loop struct {
	cfg Config
	// lastProviderTokens is the context size the provider reported for the
	// previous request. It floors the local estimate so on-wire compression
	// cannot hide real growth.
	lastProviderTokens int
}

// New returns a Loop. It returns ErrNoTools when the config has no tools,
// because such a loop can never do anything the user asked for.
func New(cfg Config) (*Loop, error) {
	if len(cfg.Tools) == 0 {
		return nil, ErrNoTools
	}
	if cfg.Stream == nil {
		return nil, errors.New("agent: Stream is required")
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	// Compaction without a summarizer would leave context growing until the
	// provider rejects the request, so refuse the misconfiguration here rather
	// than discovering it mid-conversation.
	if cfg.Compaction.Enabled && cfg.Compaction.Strategy != StrategyOff && cfg.Summarize == nil {
		return nil, ErrNoSummarizer
	}
	return &Loop{cfg: cfg}, nil
}

// defaultMaxTokens caps a completion when the caller did not pick a budget.
const defaultMaxTokens = 8192

// SetToolProgressHook installs the progress reporter after construction.
//
// Same construction-order reason as SetApprovalHook: a UI is built after the
// loop but is the only thing that can turn progress into display events.
//
// It must be called before Run, never during one.
func (l *Loop) SetToolProgressHook(hook func(progress ToolProgress)) {
	l.cfg.OnToolProgress = hook
}

// SetTextDeltaHook installs the live-text reporter after construction.
//
// Same construction-order reason as SetToolProgressHook: a UI is built after
// the loop but is the only thing that can turn streamed text into display
// events.
//
// It must be called before Run, never during one.
func (l *Loop) SetTextDeltaHook(hook func(delta string)) {
	l.cfg.OnTextDelta = hook
}

// SetApprovalHook installs the approval gate after construction.
//
// A UI is the case that needs this: the loop must be built before the UI
// exists, but the UI's approval function needs the loop. SetApprovalHook
// breaks that cycle without making the UI a build-time dependency of the loop.
//
// It must be called before Run, never during one.
func (l *Loop) SetApprovalHook(approve func(call ai.ToolCall, approval Approval) (bool, string)) {
	l.cfg.Approve = approve
}

// SetTurnHook installs the per-turn reporter after construction, or nil to
// silence turn reports.
//
// The plain REPL prints and persists through this hook; the full-screen UI
// must clear it, because the program owns both jobs there (transcript via
// events, persistence via runTurn) and a hook would print raw turns over the
// alt-screen and persist every turn twice.
//
// It must be called before Run, never during one.
func (l *Loop) SetTurnHook(hook func(Turn)) {
	l.cfg.OnTurn = hook
}

// SetCompactHook installs the compaction reporter after construction, or nil
// to silence compaction reports.
//
// Same ownership reason as SetTurnHook: the plain path prints to stdout,
// which would scribble over the alt-screen, so the UI replaces it with a
// transcript notice.
//
// It must be called before Run, never during one.
func (l *Loop) SetCompactHook(hook func(CompactResult)) {
	l.cfg.OnCompact = hook
}

// Tools returns the loop's tool set.
//
// The UI needs it to build views over live tool state (the background-job
// list reads the bash tool's table). The returned slice shares the tool
// instances rather than copying them, which is exactly what such a view
// requires.
func (l *Loop) Tools() []Tool {
	return l.cfg.Tools
}

// Run executes the loop until the model stops calling tools.
//
// messages are the agent-level messages to send, in order. The returned turns
// cover every cycle, including the final one that made no tool calls. A
// non-nil error means the run did not complete: the turns returned so far are
// still valid and the caller may resume from them.
func (l *Loop) Run(ctx context.Context, messages []Message) ([]Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	live := make([]Message, len(messages))
	copy(live, messages)

	tools := l.cfg.Tools
	specs := Specs(tools)
	byName := make(map[string]Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}

	var turns []Turn
	for {
		if err := ctx.Err(); err != nil {
			return turns, err
		}

		// The deadline bounds wall-clock time, not a turn count.
		//
		// Each iteration makes a provider call, so time is what actually costs
		// money; a turn cap would only approximate it. Expiry returns the turns
		// so far with a nil error, because a deadline is a budget being spent
		// rather than a failure — the run is resumable from where it stopped.
		if !l.cfg.Deadline.IsZero() && !time.Now().Before(l.cfg.Deadline) {
			return turns, nil
		}

		if l.compactIfNeeded(ctx, &live) {
			continue
		}

		converted, err := l.convert(live)
		if err != nil {
			return turns, fmt.Errorf("agent: convert context: %w", err)
		}

		toolChoice := l.cfg.ToolChoice
		if l.cfg.GetToolChoice != nil {
			toolChoice = l.cfg.GetToolChoice()
		}

		stream, err := l.cfg.Stream(ctx, ai.Request{
			Model:           l.model(),
			Messages:        converted,
			System:          l.cfg.System,
			Tools:           specs,
			ToolChoice:      toolChoice,
			MaxTokens:       l.cfg.MaxTokens,
			Reasoning:       l.cfg.Reasoning,
			ReasoningEffort: l.cfg.ReasoningEffort,
			SessionID:       l.cfg.SessionID,
		})
		if err != nil {
			return turns, fmt.Errorf("agent: open stream: %w", err)
		}

		assistant, err := l.drain(ctx, stream)
		if err != nil {
			return turns, fmt.Errorf("agent: stream: %w", err)
		}
		// The provider's own context count is the floor for the next
		// compaction decision.
		l.lastProviderTokens = assistant.Usage.Total

		turn := Turn{Assistant: assistant}
		live = append(live, AssistantMessage{Message: assistant})

		calls := assistant.ToolCalls()
		if len(calls) == 0 {
			turns = append(turns, turn)
			if l.cfg.OnTurn != nil {
				l.cfg.OnTurn(turn)
			}
			return turns, nil
		}

		// Steering arrives only at a boundary: mid-batch injection would
		// orphan results for calls that already ran.
		if l.cfg.GetSteeringMessages != nil {
			if steered := l.cfg.GetSteeringMessages(ctx); len(steered) > 0 {
				live = append(live, steered...)
			}
		}

		outcomes := l.dispatch(ctx, byName, assistant, calls, turn, len(turns))
		turn.Results = outcomes
		for _, outcome := range outcomes {
			live = append(live, ToolResultMessage{
				ToolCallID: outcome.CallID,
				ToolName:   outcome.ToolName,
				Content:    []ai.ContentBlock{ai.TextContent{Text: outcome.Result.Text}},
				IsError:    outcome.Result.IsError,
			})
		}

		turns = append(turns, turn)
		if l.cfg.OnTurn != nil {
			l.cfg.OnTurn(turn)
		}
	}
}

// model resolves the model for the next request.
func (l *Loop) model() ai.Model {
	if l.cfg.ModelFn != nil {
		return l.cfg.ModelFn()
	}
	return l.cfg.Model
}

// SetModel switches the model for subsequent requests.
//
// It clears a configured ModelFn: an explicit override from /model must win
// over a resolver that would otherwise keep returning the old model. It must
// be called before Run, never during one.
func (l *Loop) SetModel(model ai.Model) {
	l.cfg.Model = model
	l.cfg.ModelFn = nil
}

// convert turns agent messages into provider messages.
func (l *Loop) convert(messages []Message) ([]ai.Message, error) {
	if l.cfg.TransformContext != nil {
		messages = l.cfg.TransformContext(messages)
	}
	out := make([]ai.Message, 0, len(messages))
	for _, msg := range messages {
		switch m := msg.(type) {
		case ai.Message:
			out = append(out, m)
		case Message:
			converted, ok := m.toLLM()
			if !ok {
				// UI-only messages are dropped rather than sent.
				continue
			}
			out = append(out, converted)
		default:
			return nil, fmt.Errorf("unsupported message type %T", msg)
		}
	}
	return out, nil
}

// drain consumes a stream to completion and returns the assembled message.
//
// Visible text deltas are forwarded to OnTextDelta while the stream is still
// open, in order, so a UI can reveal the answer as it is generated. The
// loop's contract is still the completed message: a nil hook only skips the
// forwarding, and every other event is discarded on purpose.
func (l *Loop) drain(ctx context.Context, stream *ai.Stream) (ai.AssistantMessage, error) {
	if stream == nil {
		return ai.AssistantMessage{}, errors.New("agent: nil stream")
	}
	hook := l.cfg.OnTextDelta
	for event := range stream.Events {
		// Draining is required: the producer blocks on an unbuffered send
		// otherwise, and the stream would never finish.
		if delta, ok := event.(ai.EventTextDelta); ok && hook != nil && delta.Delta != "" {
			hook(delta.Delta)
		}
		select {
		case <-ctx.Done():
			return ai.AssistantMessage{}, ctx.Err()
		default:
		}
	}
	return stream.Result()
}

// dispatch runs a batch of tool calls.
//
// Calls marked exclusive run alone; shared calls run concurrently. Results are
// always returned in the order the model emitted the calls, so the transcript
// matches the assistant message regardless of completion order.
func (l *Loop) dispatch(
	ctx context.Context,
	byName map[string]Tool,
	assistant ai.AssistantMessage,
	calls []ai.ToolCall,
	turn Turn,
	turnIndex int,
) []ToolOutcome {
	type indexed struct {
		position int
		outcome  ToolOutcome
	}

	var (
		mu        sync.Mutex
		collected []indexed
		wg        sync.WaitGroup
	)

	record := func(position int, outcome ToolOutcome) {
		mu.Lock()
		defer mu.Unlock()
		collected = append(collected, indexed{position: position, outcome: outcome})
	}

	for i, call := range calls {
		exclusive := isExclusive(byName, call)
		if exclusive {
			// An exclusive call must run alone: drain anything already in
			// flight before starting it.
			wg.Wait()
		}

		wg.Add(1)
		go func(position int, call ai.ToolCall) {
			defer wg.Done()
			record(position, l.execute(ctx, byName, assistant, call, turn, turnIndex))
		}(i, call)

		if exclusive {
			// And drain again before anything else starts, otherwise a shared
			// call launched afterwards would overlap it and the call would not
			// in fact be running alone.
			wg.Wait()
		}
	}
	wg.Wait()

	out := make([]ToolOutcome, len(calls))
	for _, entry := range collected {
		out[entry.position] = entry.outcome
	}
	return out
}

// isExclusive reports whether a call must run alone.
func isExclusive(byName map[string]Tool, call ai.ToolCall) bool {
	tool, ok := byName[call.Name]
	if !ok {
		return false
	}
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	return tool.Exclusive(args)
}

// execute runs one tool call and normalises its outcome.
func (l *Loop) execute(
	ctx context.Context,
	byName map[string]Tool,
	assistant ai.AssistantMessage,
	call ai.ToolCall,
	turn Turn,
	turnIndex int,
) ToolOutcome {
	fail := func(text string) ToolOutcome {
		return ToolOutcome{CallID: call.ID, ToolName: call.Name, Result: Result{Text: text, IsError: true}}
	}

	tool, ok := byName[call.Name]
	if !ok {
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		return fail(fmt.Sprintf("Tool %q not found. Available tools: %s.", call.Name, strings.Join(sorted(names), ", ")))
	}

	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	if err := ValidateArgs(tool, args); err != nil {
		// A schema violation is reported back to the model rather than
		// aborting the turn, so it can fix its own arguments.
		return fail(fmt.Sprintf("Invalid arguments for %q: %v", call.Name, err))
	}

	if l.cfg.BeforeToolCall != nil {
		outcome := l.cfg.BeforeToolCall(BeforeToolCallContext{
			Tool: tool, Call: call, Args: args, Turn: turn, Turns: turnIndex,
		})
		if outcome.Block {
			reason := outcome.Reason
			if reason == "" {
				reason = "blocked by policy"
			}
			return fail(reason)
		}
		if outcome.Args != nil {
			args = outcome.Args
			if err := ValidateArgs(tool, args); err != nil {
				return fail(fmt.Sprintf("Invalid arguments for %q: %v", call.Name, err))
			}
		}
	}

	if l.cfg.Approve != nil {
		approved, reason := l.cfg.Approve(call, tool.Approval())
		if !approved {
			if reason == "" {
				reason = "denied by user"
			}
			return fail(reason)
		}
	}

	started := time.Now()
	toolCtx := Context{
		Ctx:       ctx,
		CallID:    call.ID,
		ToolName:  call.Name,
		SessionID: l.cfg.SessionID,
	}

	if l.cfg.OnToolProgress != nil {
		// Announce the call before it runs, so a UI can show it immediately
		// with no output yet.
		l.cfg.OnToolProgress(ToolProgress{
			CallID:   call.ID,
			ToolName: call.Name,
			Elapsed:  time.Since(started),
		})
		// Forward the tool's own partial reports. The closure captures only
		// immutable values, so concurrent calls in one batch stay independent.
		toolCtx.OnProgress = func(partial Result) {
			l.cfg.OnToolProgress(ToolProgress{
				CallID:   call.ID,
				ToolName: call.Name,
				Result:   partial,
				Elapsed:  time.Since(started),
			})
		}
	}

	result := tool.Execute(toolCtx, args)

	// Apply the host override exactly once, here. The progress report below
	// then sees the final values, so a UI never shows a result the model will
	// not receive.
	if l.cfg.AfterToolCall != nil {
		override := l.cfg.AfterToolCall(AfterToolCallContext{
			Tool: tool, Call: call, Args: args, Result: result,
		})
		if override.Text != nil {
			result.Text = *override.Text
		}
		if override.IsErr != nil {
			result.IsError = *override.IsErr
		}
		if override.Useless != nil {
			result.Useless = *override.Useless
		}
		if override.Details != nil {
			result.Details = override.Details
		}
	}

	if l.cfg.OnToolProgress != nil {
		l.cfg.OnToolProgress(ToolProgress{
			CallID:   call.ID,
			ToolName: call.Name,
			Result:   result,
			Final:    true,
			Elapsed:  time.Since(started),
		})
	}

	return ToolOutcome{CallID: call.ID, ToolName: call.Name, Result: result}
}

func sorted(values []string) []string {
	out := append([]string(nil), values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// compactIfNeeded reduces live context when it has grown past the trigger.
//
// It reports whether a compaction happened. When it did, the caller should
// restart the turn rather than send a request built from the pre-compaction
// context.
//
// The decision uses the local estimate, floored by the provider-reported size
// from the previous turn: a request transform can compress the wire below the
// real stored conversation, so trusting the provider alone would defer
// compaction until the request is rejected.
func (l *Loop) compactIfNeeded(ctx context.Context, live *[]Message) bool {
	c := l.cfg.Compaction
	if !c.Enabled || c.Strategy == StrategyOff || l.cfg.Summarize == nil {
		return false
	}
	window := l.model().ContextWindow
	if window <= 0 {
		return false
	}

	estimate := EstimateMessages(*live)
	contextTokens := CompactionContextTokens(l.lastProviderTokens, estimate)
	if !ShouldCompact(contextTokens, window, c) {
		return false
	}

	result, err := Compact(ctx, *live, c, window, l.cfg.Summarize)
	if err != nil {
		// A failed compaction must not abort the run: the context still fits
		// the window, so sending it is better than failing. The next trigger
		// will retry.
		return false
	}
	*live = result.Messages
	l.lastProviderTokens = 0
	if l.cfg.OnCompact != nil {
		l.cfg.OnCompact(result)
	}
	return true
}
