package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/security"
)

// ErrNoTools means the loop was started with an empty tool set.
var ErrNoTools = errors.New("agent: no tools registered")

// ErrTurnLimit means the run reached its model-turn budget before the model
// stopped calling tools. It is a recoverable stop, not a failure of the work:
// the turns completed so far are returned intact and the task can resume.
var ErrTurnLimit = errors.New("agent: turn limit reached")

// DefaultMaxTurns bounds a run when the caller does not choose a budget.
//
// The deadline bounds money spent on wall-clock time; this bounds the number
// of model turns independently, so a model that keeps proposing tool calls
// cannot run forever when no deadline was set. One hundred turns is well above
// the 5/10/20/50-turn workloads the runtime is expected to serve, and still
// small enough that a runaway loop is visible rather than open-ended.
const DefaultMaxTurns = 100

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
	// Authorization is the security decision that admitted this call, when an
	// authorizer was installed. It is kept separate from Result so an
	// authorization is never mistaken for an execution outcome: a denied call
	// carries a decision and no execution, while an authorized call that then
	// failed carries both.
	Authorization *security.Decision
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
	// SystemFn resolves the system prompt per request. When set it overrides
	// System, so a caller whose base prompt depends on live runtime state
	// (the active workspace, for example) does not have to rebuild the loop
	// to change it. The returned slice is used as-is; the loop never mutates
	// it.
	SystemFn func() []ai.SystemPromptBlock
	// SystemContext appends extra system-prompt blocks after System on every
	// request. It is how a caller injects per-turn context — files the user
	// referenced, for example — without rebuilding the loop. Set it with
	// SetSystemContext before Run.
	SystemContext []ai.SystemPromptBlock
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
	// Admit decides whether a call is permitted at all, before approval.
	//
	// It is the legacy runtime authority seam, superseded by Authorize. When
	// Authorize is set the loop uses it exclusively; Admit and Approve are
	// consulted only when Authorize is nil, so a caller can migrate one seam at
	// a time without changing behavior. A nil Admit admits every call.
	Admit func(call ai.ToolCall, tool Tool) (bool, string)
	// Approve decides whether a call may run without prompting.
	Approve func(call ai.ToolCall, approval Approval) (bool, string)
	// Authorize is the security boundary. When set it is the only admission
	// path: the loop evaluates every call, executes only on AUTO, obtains a
	// grant for APPROVE through the same authorizer, and refuses DENY. Because
	// the loop owns that sequence, no tool, UI, or model output can reach
	// execution without a decision.
	Authorize Authorizer
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
	// MaxTurns bounds the number of model turns in one run. Zero uses
	// DefaultMaxTurns. Reaching the cap returns ErrTurnLimit with the turns
	// completed so far, so a model that never stops proposing tools cannot run
	// unbounded even when no deadline was configured.
	//
	// It is the legacy per-run cap. When Budget is set, the tree-wide turn
	// grant is authoritative for aggregate consumption; MaxTurns still bounds
	// one run, which is the Run Budget half of the contract.
	MaxTurns int
	// Budget is the root-owned resource grant shared by every descendant of
	// this run. When nil the loop creates a run-local budget from MaxTurns and
	// the deadline, preserving the pre-Phase-5 behaviour for embedders.
	//
	// A caller that delegates must pass the same pointer to RunSubagent (the
	// production spawner does). A child never receives a fresh budget: that
	// would reset the aggregate the root is enforcing.
	Budget *budget.Budget
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
	// Args are the arguments the call runs with. They let a UI describe
	// what the call does while it is still running, before any output
	// exists. The map is the call's own arguments, never fabricated text.
	Args map[string]any
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
//
// The pre-Execute announcement carries no outcome yet — an empty, non-final
// report — so it stages as started. A non-final report carrying a partial
// outcome stages as an update. Consumers must switch on this rather than
// re-deriving the stage from emptiness: the inference lives here, once, next
// to the code that emits the announcement.
func (p ToolProgress) Stage() ToolProgressStage {
	if p.Final {
		return ProgressFinished
	}
	if p.Result.Text == "" && p.Result.Details == nil {
		return ProgressStarted
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
	// budget is the normalised root grant for this loop. cfg.Budget points at
	// the same instance when the caller supplied one; otherwise it is the
	// run-local grant New derived from MaxTurns.
	budget *budget.Budget
	// explicitBudget records that the caller supplied or installed the grant.
	// It preserves the frozen ErrTurnLimit contract for embedders that only
	// set MaxTurns, while a host with a real tree budget receives a resource
	// stop the completion layer can inspect.
	explicitBudget bool
	// admissionMu guards admissionErr. Tool calls dispatch concurrently, so
	// the first failed admission is recorded under the mutex and surfaced to
	// Run after the batch joins.
	admissionMu  sync.Mutex
	admissionErr error
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
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = DefaultMaxTurns
	}
	explicitBudget := cfg.Budget != nil
	if cfg.Budget == nil {
		cfg.Budget = budget.New(budget.Limits{
			Turns:         cfg.MaxTurns,
			Deadline:      cfg.Deadline,
			ModelCalls:    cfg.MaxTurns,
			ExternalCalls: cfg.MaxTurns,
			ExternalBytes: 16 * 1024 * 1024,
		})
	}
	// Compaction without a summarizer would leave context growing until the
	// provider rejects the request, so refuse the misconfiguration here rather
	// than discovering it mid-conversation.
	if cfg.Compaction.Enabled && cfg.Compaction.Strategy != StrategyOff && cfg.Summarize == nil {
		return nil, ErrNoSummarizer
	}
	return &Loop{cfg: cfg, budget: cfg.Budget, explicitBudget: explicitBudget}, nil
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

// SetAdmitHook installs the runtime authority gate after construction.
//
// It exists for the same construction-order reason as SetApprovalHook: the
// workspace that owns the policy may be resolved (from a resumed session)
// after the loop is built. It must be called before Run, never during one.
func (l *Loop) SetAdmitHook(admit func(call ai.ToolCall, tool Tool) (bool, string)) {
	l.cfg.Admit = admit
}

// SetAuthorizeHook installs the security boundary after construction.
//
// It exists for the same construction-order reason as SetApprovalHook: the
// host that owns the policy (and the human interaction behind APPROVE) may be
// built after the loop, but it must be installed before Run. It must never be
// called during a run.
func (l *Loop) SetAuthorizeHook(authorizer Authorizer) {
	l.cfg.Authorize = authorizer
}

// Authorizer returns the installed security boundary, or nil.
//
// The host that installed it also needs to reach it later (a UI sets the
// approval callback once it exists), so the loop exposes the seam rather than
// forcing the host to keep a second reference that could drift.
func (l *Loop) Authorizer() Authorizer { return l.cfg.Authorize }

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
		if len(turns) >= l.cfg.MaxTurns {
			if l.explicitBudget {
				return turns, turnBudgetError(&budget.ExhaustedError{
					Dimension: budget.DimensionTurns,
					Limit:     l.cfg.MaxTurns,
					Used:      len(turns),
				})
			}
			return turns, ErrTurnLimit
		}

		// The deadline bounds wall-clock time, not a turn count.
		//
		// Each iteration makes a provider call, so time is what actually costs
		// money; a turn cap would only approximate it. Expiry returns the turns
		// so far with a nil error, because a deadline is a budget being spent
		// rather than a failure — the run is resumable from where it stopped.
		deadline := l.deadline()
		if !deadline.IsZero() && !time.Now().Before(deadline) {
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

		if err := ctx.Err(); err != nil {
			return turns, err
		}
		// A model call is admitted only after every local preparation step has
		// succeeded. Turn and model-call are charged together, atomically: a
		// refused model call must not silently spend a turn, and a child that
		// shares this grant cannot observe either counter mid-admission.
		if err := l.budget.AdmitAll(budget.DimensionTurns, budget.DimensionModelCalls); err != nil {
			return turns, turnBudgetError(err)
		}

		stream, err := l.cfg.Stream(ctx, ai.Request{
			Model:           l.model(),
			Messages:        converted,
			System:          l.system(),
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

		outcomes, admissionErr := l.dispatch(ctx, byName, assistant, calls, turn, len(turns))
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
		// The turn is complete and its evidence is intact. Return the budget
		// stop now rather than asking the model for another turn it cannot
		// afford: the work so far is resumable, and completion evaluation can
		// still inspect exactly what happened.
		if admissionErr != nil {
			return turns, admissionErr
		}
	}
}

// SetBudget installs the root grant for the next Run.
//
// A host that spans several runs (the TUI turn loop, the REPL) replaces the
// grant at a run boundary; descendants share the pointer they were given, so
// replacing it never mutates a tree that is still executing. It must be called
// before Run, never during one.
func (l *Loop) SetBudget(b *budget.Budget) {
	if b == nil {
		// A nil grant would silently disable admission. Rebuild the run-local
		// grant from the loop's existing bounds instead.
		b = budget.New(budget.Limits{
			Turns:         l.cfg.MaxTurns,
			ModelCalls:    l.cfg.MaxTurns,
			Deadline:      l.cfg.Deadline,
			ExternalCalls: l.cfg.MaxTurns,
			ExternalBytes: 16 * 1024 * 1024,
		})
	}
	l.cfg.Budget = b
	l.budget = b
	l.explicitBudget = true
}

// Budget returns the grant this loop is currently enforcing.
func (l *Loop) Budget() *budget.Budget { return l.budget }

// Deadline returns the effective wall-clock bound for the run.
//
// A headless caller uses it to distinguish a run that stopped because the
// deadline expired from one whose model simply stopped: the loop returns the
// same nil error for both, and only the caller can turn the former into an
// honest PAUSED verdict.
func (l *Loop) Deadline() time.Time { return l.deadline() }

// deadline returns the effective wall-clock bound: the stricter of the legacy
// per-run deadline and the root budget's deadline. One absolute instant is
// shared down the tree, so a child cannot extend it.
func (l *Loop) deadline() time.Time {
	deadline := l.cfg.Deadline
	if root := l.budget.Deadline(); !root.IsZero() && (deadline.IsZero() || root.Before(deadline)) {
		deadline = root
	}
	return deadline
}

// recordAdmissionFailure keeps the first budget refusal in a tool batch. The
// batch continues so every other admitted call still produces its evidence;
// Run surfaces the stop after the turn is recorded.
func (l *Loop) recordAdmissionFailure(err error) {
	l.admissionMu.Lock()
	if l.admissionErr == nil {
		l.admissionErr = err
	}
	l.admissionMu.Unlock()
}

// takeAdmissionFailure returns and clears the recorded refusal.
func (l *Loop) takeAdmissionFailure() error {
	l.admissionMu.Lock()
	defer l.admissionMu.Unlock()
	err := l.admissionErr
	l.admissionErr = nil
	return err
}

// turnBudgetError keeps the frozen ErrTurnLimit sentinel matchable while also
// exposing the explicit resource dimension to callers that classify budget
// stops. A model-call or tool-call exhaustion is not a turn limit and is
// returned unchanged.
func turnBudgetError(err error) error {
	var exhausted *budget.ExhaustedError
	if errors.As(err, &exhausted) && exhausted.Dimension == budget.DimensionTurns {
		return fmt.Errorf("%w: %w", ErrTurnLimit, err)
	}
	return err
}

// model resolves the model for the next request.
func (l *Loop) model() ai.Model {
	if l.cfg.ModelFn != nil {
		return l.cfg.ModelFn()
	}
	return l.cfg.Model
}

// system returns the system prompt for the next request: the base blocks with
// any per-turn context appended. It copies rather than appending in place, so a
// caller's System slice is never mutated and two requests cannot share backing
// storage that one of them later overwrites.
func (l *Loop) system() []ai.SystemPromptBlock {
	base := l.cfg.System
	if l.cfg.SystemFn != nil {
		base = l.cfg.SystemFn()
	}
	if len(l.cfg.SystemContext) == 0 {
		return base
	}
	out := make([]ai.SystemPromptBlock, 0, len(base)+len(l.cfg.SystemContext))
	out = append(out, base...)
	return append(out, l.cfg.SystemContext...)
}

// SetSystemContext installs extra system-prompt blocks appended after the
// loop's base System on every subsequent request. It is how a caller injects
// per-turn context — files the user referenced, for example — without rebuilding
// the loop. Pass nil to clear it.
//
// It must be called before Run, never during one.
func (l *Loop) SetSystemContext(blocks []ai.SystemPromptBlock) {
	l.cfg.SystemContext = blocks
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

// SetReasoning changes the extended-thinking setting for subsequent requests.
//
// Like SetModel it must be called before Run, never during one: the effort is
// read when the request is built, so changing it mid-turn would make the turn
// inconsistent with itself. A role's thinking effort flows through here.
func (l *Loop) SetReasoning(enabled bool, effort string) {
	l.cfg.Reasoning = enabled
	l.cfg.ReasoningEffort = effort
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
// It forwards visible text to the loop's OnTextDelta hook through the shared
// ai.DrainStream, so the loop and any other consumer (the $sup answer surface)
// cannot drift on what draining a stream means: the loop's contract is still
// the completed message.
func (l *Loop) drain(ctx context.Context, stream *ai.Stream) (ai.AssistantMessage, error) {
	return ai.DrainStream(ctx, stream, l.cfg.OnTextDelta)
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
) ([]ToolOutcome, error) {
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
	return out, l.takeAdmissionFailure()
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

	// A cancellation that landed while arguments were being validated or a
	// human was being asked must not start the tool. The check is repeated
	// after authorization because approval can block indefinitely.
	if err := ctx.Err(); err != nil {
		return fail(err.Error())
	}

	// The security boundary runs immediately before execution. When an
	// authorizer is installed it is the only admission path: DENY never
	// executes, APPROVE must produce a grant that a fresh evaluation confirms,
	// and AUTO executes directly. The loop owns that sequence, so nothing
	// downstream can skip it.
	var decision *security.Decision
	if l.cfg.Authorize != nil {
		resolved, failure, ok := l.authorize(call, tool)
		if !ok {
			return fail(failure)
		}
		decision = &resolved
	} else {
		// Legacy seams, used only when no authorizer is installed. Authority is
		// still consulted before approval: a call the workspace forbids is
		// refused outright, never offered to the user as something they could
		// approve (§27, §37).
		if l.cfg.Admit != nil {
			if admitted, reason := l.cfg.Admit(call, tool); !admitted {
				if reason == "" {
					reason = "capability denied"
				}
				return fail(reason)
			}
		}
		if l.cfg.Approve != nil {
			approval := tool.Approval()
			// Generate mutation visibility before prompting, when the tool can
			// describe its mutation. A mutation that cannot be previewed fails
			// closed rather than being approved on metadata alone.
			if previewer, ok := tool.(MutationPreviewer); ok {
				preview, applicable, err := previewer.MutationPreview(args)
				if applicable {
					if err != nil {
						return fail(fmt.Sprintf("mutation preview unavailable: %v", err))
					}
					approval.Preview = preview.Diff
					approval.Diff = preview.Diff
					approval.MutationPreview = preview
					approval.PreviewMutation = preview
				}
			}
			approved, reason := l.cfg.Approve(call, approval)
			if !approved {
				if reason == "" {
					reason = "denied by user"
				}
				return fail(reason)
			}
		}
	}

	// Approval can block indefinitely, so re-check cancellation after
	// authorization and before charging anything. A cancelled call must not
	// consume a grant it will never use.
	if err := ctx.Err(); err != nil {
		return fail(err.Error())
	}

	// Authority has admitted the operation; budget now decides whether the
	// tree can still afford it. The two are deliberately separate: a grant is
	// not permission, and remaining budget cannot widen authority. Admission
	// is atomic, so an exhausted grant produces no execution.
	if err := l.budget.Admit(budget.DimensionToolCalls); err != nil {
		l.recordAdmissionFailure(err)
		return fail(err.Error())
	}

	started := time.Now()
	toolCtx := Context{
		Ctx:       ctx,
		CallID:    call.ID,
		ToolName:  call.Name,
		SessionID: l.cfg.SessionID,
		ReadOnly:  decision != nil && decision.ReadOnly,
		Budget:    l.budget,
		Deadline:  l.deadline(),
	}
	if err := ctx.Err(); err != nil {
		return fail(err.Error())
	}

	if l.cfg.OnToolProgress != nil {
		// Announce the call before it runs, so a UI can show it immediately
		// with no output yet.
		l.cfg.OnToolProgress(ToolProgress{
			CallID:   call.ID,
			ToolName: call.Name,
			Args:     args,
			Elapsed:  time.Since(started),
		})
		// Forward the tool's own partial reports. The closure captures only
		// immutable values, so concurrent calls in one batch stay independent.
		toolCtx.OnProgress = func(partial Result) {
			l.cfg.OnToolProgress(ToolProgress{
				CallID:   call.ID,
				ToolName: call.Name,
				Args:     args,
				Result:   partial,
				Elapsed:  time.Since(started),
			})
		}
	}

	result := tool.Execute(toolCtx, args)
	if result.Exhausted != nil {
		// The tool enforced a dimension the loop cannot see directly (a
		// background job slot, a child execution). Record the same stop the
		// loop's own admission would have produced.
		l.recordAdmissionFailure(result.Exhausted)
	}

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
			Args:     args,
			Result:   result,
			Final:    true,
			Elapsed:  time.Since(started),
		})
	}

	return ToolOutcome{CallID: call.ID, ToolName: call.Name, Result: result, Authorization: decision}
}

// authorize runs the security sequence for one call and reports the failure
// text when the call may not execute.
//
// The order enforces the invariants directly: a DENY is terminal and returns
// without any execution; an APPROVE must yield a grant and then a fresh
// evaluation must confirm AUTO, so an approval that no longer matches the
// operation cannot execute. When no approver is installed an APPROVE fails
// closed rather than executing unapproved.
func (l *Loop) authorize(call ai.ToolCall, tool Tool) (security.Decision, string, bool) {
	decision := l.cfg.Authorize.Evaluate(call, tool)
	switch decision.Outcome {
	case security.OutcomeAuto:
		return decision, "", true
	case security.OutcomeDeny:
		return decision, refusalText("authorization denied", decision), false
	case security.OutcomeApprove:
		if _, ok := l.cfg.Authorize.Approve(call, decision); !ok {
			return decision, refusalText("approval rejected", decision), false
		}
		confirmed := l.cfg.Authorize.Evaluate(call, tool)
		if confirmed.Outcome != security.OutcomeAuto {
			return confirmed, refusalText("authorization changed after approval", confirmed), false
		}
		return confirmed, "", true
	default:
		return decision, "authorization denied: unknown security outcome", false
	}
}

// refusalText renders a security refusal.
//
// The wording keeps a security outcome distinct from an execution failure: the
// model is told the call was not authorized, and which rule refused it, rather
// than being handed a generic tool error it might retry blindly.
func refusalText(prefix string, decision security.Decision) string {
	if decision.Rule == "" && decision.Reason == "" {
		return prefix
	}
	if decision.Rule == "" {
		return fmt.Sprintf("%s: %s", prefix, decision.Reason)
	}
	if decision.Reason == "" {
		return fmt.Sprintf("%s (%s)", prefix, decision.Rule)
	}
	return fmt.Sprintf("%s (%s): %s", prefix, decision.Rule, decision.Reason)
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
