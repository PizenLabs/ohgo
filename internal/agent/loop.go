package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
)

// ErrNoTools means the loop was started with an empty tool set.
var ErrNoTools = errors.New("agent: no tools registered")

// ErrTurnLimit means the run reached its model-turn budget before the model
// stopped calling tools. It is a recoverable stop, not a failure of the work:
// the turns completed so far are returned intact and the task can resume.
var ErrTurnLimit = errors.New("agent: turn limit reached")

// ErrTaskBlocked means the runtime closed the task before the model stopped on
// its own: a required capability failed non-retryably, or the model kept
// proposing actions against an answer-only surface after the bounded recovery
// turn. It is a terminal semantic verdict, not a resource stop, and the caller
// must report it as review rather than success.
var ErrTaskBlocked = errors.New("agent: task blocked")

// ErrCapabilityUnavailable means the objective requires an operation the
// runtime cannot perform, discovered before the model was asked to act.
//
// It is deliberately distinct from ErrTaskBlocked and from a failed execution:
// nothing ran, nothing failed, and no model turn was spent. The caller must
// report it as a blocked capability rather than a review or a model failure, so
// the human can decide whether to enable the capability or change the objective.
var ErrCapabilityUnavailable = errors.New("agent: capability unavailable")

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
	// Args are the arguments the call ran with, after any BeforeToolCall
	// override. They are the runtime's observation of what was asked for, not
	// model narration, and are retained so evidence can be attributed to a
	// concrete path or command.
	Args map[string]any
	// Result is the tool's return value.
	Result Result
	// Authorization is the security decision that admitted this call, when an
	// authorizer was installed. It is kept separate from Result so an
	// authorization is never mistaken for an execution outcome: a denied call
	// carries a decision and no execution, while an authorized call that then
	// failed carries both.
	Authorization *security.Decision
}

// Refused reports whether the runtime or the tool refused the call on
// authority or contract grounds.
//
// A refusal is a control decision, not a failed action: no evidence was
// produced. Completion and sufficiency must not treat it as a pending
// execution failure, or a model that adapts after a denied call would be
// punished for the runtime's refusal.
func (o ToolOutcome) Refused() bool {
	if o.Result.Refused || o.Result.CandidateRejected || o.Result.FailureRejected {
		return true
	}
	return o.Authorization != nil && o.Authorization.Outcome == security.OutcomeDeny
}

// ReadOnly reports whether the call ran behind a read-only workspace boundary.
//
// A successful process under that decision cannot have changed the repository,
// so a mutation objective does not owe a later verification for it.
func (o ToolOutcome) ReadOnly() bool {
	return o.Authorization != nil && o.Authorization.ReadOnly
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
	// AnswerRequest is the user-role message appended when an answer-only
	// request returns no visible text. It makes the closed action surface
	// explicit in the conversation rather than leaving the model to infer it
	// from a system block after a tool result. It is a runtime view, never
	// persisted, and empty disables it while keeping the one retry.
	AnswerRequest string
	// ExecutionRecoveryRequest is the user-role message appended once when the
	// model announces an action but emits no tool call. It asks for the actual
	// canonical call; the runtime never executes the prose. Empty keeps the
	// classification but does not spend the recovery request.
	ExecutionRecoveryRequest string
	// ProtocolRecoveryRequest is the user-role message appended once when a
	// serialized tool-call envelope could not be normalized. It asks for a
	// native or correctly serialized call without inventing arguments. Empty
	// keeps the classification but does not spend the recovery request.
	ProtocolRecoveryRequest string
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
	// WorkspaceHandoff is consulted before authorization for every tool call.
	// The host may move the session to a workspace that holds the capability
	// the call needs, and reports whether authority moved; the loop then
	// authorizes the same call under the new authority. The host owns the
	// policy and the live gate, so this is a request for a move, never a
	// capability grant: a nil hook performs no handoff.
	//
	// It runs after semantic grounding, so a call the user's request did not
	// ground cannot use a handoff to reach a stronger authority.
	WorkspaceHandoff func(call ai.ToolCall, tool Tool, args map[string]any) (moved bool)
	// OnTurn is called after each turn, for streaming progress to a UI.
	OnTurn func(Turn)
	// OnTextDelta receives visible model text while a stream is still open,
	// in order. A UI publishes each delta so the answer reveals as it is
	// generated instead of landing as one block when the turn completes.
	// Nil disables it; transports that never token-stream never fire it.
	OnTextDelta func(delta string)
	// OnThinkingDelta receives reasoning deltas while a stream is still open,
	// in order. It is separate from OnTextDelta because reasoning is a
	// presentation block of its own, never assistant prose.
	OnThinkingDelta func(delta string)
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
	// Grounding is the request's deterministic semantic contract. When set,
	// the loop refuses a mutating call the request did not ground before the
	// security boundary is consulted. It narrows the action space; it never
	// grants authority.
	Grounding *contract.IntentGrounding
	// Compaction configures context reduction. The zero value disables it.
	Compaction Compaction
	// Summarize replaces the compacted prefix during compaction. Required when
	// Compaction is enabled.
	Summarize SummarizeFunc
	// OnCompact reports each compaction, for logging and UI.
	OnCompact func(CompactResult)
	// Project builds the next request view from authoritative runtime state.
	//
	// It may replace the messages sent on this turn, append system blocks, and
	// narrow the offered tool set. It cannot widen the tool set beyond Tools,
	// change authority, or alter the live conversation the loop retains. A nil
	// Project preserves the original full-context behaviour exactly.
	Project func(ProjectionRequest) ProjectionResult
	// OnProjection reports each projection for audit. It is called after the
	// projection and before the provider request.
	OnProjection func(ProjectionReport)
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
	// Journal captures a recovery point around the run's mutations.
	//
	// It is installed by the host and consulted immediately before the first
	// batch that can change the workspace. A journal that cannot capture the
	// pre-mutation state refuses the batch: an edit without a recovery point is
	// not permitted. Nil disables checkpointing, which is the correct default
	// for embedders that do not own a workspace.
	Journal EditJournal
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
	// evidenceMu guards the observed-change sequence below. Tool calls dispatch
	// concurrently, so one mutating and one verifying call in the same batch
	// must not race when they update the evidence order.
	evidenceMu sync.Mutex
	// evidenceSeq counts finished tool outcomes in the order they complete. The
	// last definite change and the last successful check are compared as
	// positions, so a verification artifact cannot reference a check that
	// happened before the change it describes.
	evidenceSeq     int
	lastChangeSeq   int
	lastVerifiedSeq int
	// observationMu guards the current-observation index below. A repeated
	// read-only observation of the same coordinate is answered from the
	// existing result instead of spending another call and another context
	// window; a change or execution clears the index because the workspace may
	// no longer be the one that was observed.
	observationMu sync.Mutex
	observations  map[string]observationRecord
	// mutationMu guards lastMutation. The journal publishes its evidence when
	// the run ends, and the host reads it immediately after Run returns.
	mutationMu   sync.Mutex
	lastMutation MutationProvenance
	// runSeq issues a local run identity for mutation provenance when the host
	// has none. It is process-local and never a durable task identity.
	runSeq int
}

// observationRecord is one current read-only observation. Turn keeps a
// same-batch duplicate from being answered from a sibling's result: only an
// observation from an earlier turn is reusable.
type observationRecord struct {
	turn    int
	summary string
}

// VerificationObserved reports whether a successful check has been observed
// since the last definite repository change.
//
// It is the evidence predicate behind a verification artifact: the artifact is
// a reference to a check the runtime saw, not a claim the model can author on
// its own. A run with no observed change has nothing to verify until a check
// runs, and a change after the last check reopens the obligation.
func (l *Loop) VerificationObserved() bool {
	if l == nil {
		return false
	}
	l.evidenceMu.Lock()
	defer l.evidenceMu.Unlock()
	return l.lastVerifiedSeq > 0 && l.lastVerifiedSeq >= l.lastChangeSeq
}

// noteEvidence records one finished tool outcome for the artifact gate. It is
// intentionally narrower than completion: it tracks definite changes only, so
// a read-only probe cannot invalidate a check the runtime already observed.
func (l *Loop) noteEvidence(result Result) {
	l.evidenceMu.Lock()
	defer l.evidenceMu.Unlock()
	l.evidenceSeq++
	if result.Mutates && !result.IsError {
		l.lastChangeSeq = l.evidenceSeq
	}
	if result.Verifies && !result.IsError {
		l.lastVerifiedSeq = l.evidenceSeq
	}
}

// recordMutation stores the run's published mutation evidence.
func (l *Loop) recordMutation(provenance MutationProvenance) {
	l.mutationMu.Lock()
	defer l.mutationMu.Unlock()
	l.lastMutation = provenance
}

// LastMutation returns the mutation evidence published by the most recent run.
//
// It is empty when no journal was installed or the journal published nothing.
// The caller can distinguish that from a captured boundary with no changes via
// Captured, so completion never invents attribution.
func (l *Loop) LastMutation() MutationProvenance {
	if l == nil {
		return MutationProvenance{}
	}
	l.mutationMu.Lock()
	defer l.mutationMu.Unlock()
	out := l.lastMutation
	out.Changes = append([]MutationChange(nil), l.lastMutation.Changes...)
	return out
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
	return &Loop{
		cfg: cfg, budget: cfg.Budget, explicitBudget: explicitBudget,
		observations: map[string]observationRecord{},
	}, nil
}

// observationTool reports a read-only capability whose exact invocation is an
// observation. Verification is excluded: its result is a state check, not a
// repository coordinate.
func observationTool(name string) bool {
	switch name {
	case "read", "ls", "glob", "grep", "structure", "symbols", "graph", "refs":
		return true
	default:
		return false
	}
}

// priorObservation returns the short result already observed for an identical
// read-only invocation. It is a context-efficiency guard, not authority: the
// tool is not run again, and the model is told to use the evidence it already
// has or to change the coordinate.
func (l *Loop) priorObservation(call ai.ToolCall, turnIndex int) (string, bool) {
	if l == nil || !observationTool(call.Name) {
		return "", false
	}
	signature := CallSignature(call.Name, call.Arguments)
	l.observationMu.Lock()
	defer l.observationMu.Unlock()
	prior, ok := l.observations[signature]
	if !ok || prior.summary == "" || prior.turn >= turnIndex {
		return "", false
	}
	return "This exact observation is already current in this run: " + prior.summary +
		". Use the recorded evidence instead of repeating it; re-inspect only after a change changes the workspace.", true
}

// noteObservation adds a successful read-only observation to the current
// index. A definite change or any process execution invalidates the index,
// because the workspace state the observations described may be gone.
func (l *Loop) noteObservation(call ai.ToolCall, result Result, turnIndex int) {
	if l == nil {
		return
	}
	if result.IsError || result.Refused || result.Reused {
		return
	}
	l.observationMu.Lock()
	defer l.observationMu.Unlock()
	if result.Mutates || result.Executes || result.Verifies {
		l.observations = map[string]observationRecord{}
		return
	}
	if l.observations == nil {
		l.observations = map[string]observationRecord{}
	}
	if !observationTool(call.Name) {
		return
	}
	summary := strings.TrimSpace(firstLineText(result.Text))
	if summary == "" {
		summary = call.Name + " returned an empty observation"
	}
	l.observations[CallSignature(call.Name, call.Arguments)] = observationRecord{
		turn: turnIndex, summary: truncateRunes(summary, 120),
	}
}

// firstLineText returns the first non-empty content line of tool output.
func firstLineText(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if tab := strings.IndexByte(line, '\t'); tab >= 0 {
			line = strings.TrimSpace(line[tab+1:])
		}
		if line == "" || strings.HasPrefix(line, "[showing ") {
			continue
		}
		return line
	}
	return ""
}

// truncateRunes bounds a display summary without splitting a UTF-8 rune.
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
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

// SetThinkingDeltaHook installs the live-reasoning reporter after
// construction.
//
// It has the same construction-order and boundary contracts as
// SetTextDeltaHook: the UI is built after the loop, and the hook must be
// installed before Run. Reasoning stays a separate channel so no consumer has
// to infer it from model prose.
func (l *Loop) SetThinkingDeltaHook(hook func(delta string)) {
	l.cfg.OnThinkingDelta = hook
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

// SetBeforeToolCall installs the pre-execution hook. It must be called before
// Run, never during one. Pass nil to remove it.
func (l *Loop) SetBeforeToolCall(hook func(BeforeToolCallContext) BeforeToolCallResult) {
	l.cfg.BeforeToolCall = hook
}

// SetAfterToolCall installs the post-execution hook. It must be called before
// Run, never during one. Pass nil to remove it.
func (l *Loop) SetAfterToolCall(hook func(AfterToolCallContext) AfterToolCallResult) {
	l.cfg.AfterToolCall = hook
}

// SetWorkspaceHandoff installs the tool-boundary authority handoff.
//
// It must be called before Run, never during one. Pass nil to disable it. The
// hook may move the host's live authority when the call needs a capability the
// current workspace lacks; the loop only asks, and always re-evaluates the
// call under the authority that is in force after the hook returns.
func (l *Loop) SetWorkspaceHandoff(hook func(call ai.ToolCall, tool Tool, args map[string]any) (moved bool)) {
	l.cfg.WorkspaceHandoff = hook
}

// SetProjectionHook installs the context projection seam after construction.
//
// A host installs a fresh projector at each turn boundary because the task
// shape belongs to the turn, not to the loop. It must be called before Run,
// never during one. Pass nil to restore full-context behaviour.
func (l *Loop) SetProjectionHook(project func(ProjectionRequest) ProjectionResult) {
	l.cfg.Project = project
}

// SetGrounding installs the request's deterministic semantic contract for the
// next run. It is set at a turn boundary by the control plane and cleared
// afterwards, so one turn's grounding never authorizes the next.
func (l *Loop) SetGrounding(grounding *contract.IntentGrounding) {
	l.cfg.Grounding = grounding
}

// SetProjectionReportHook installs the projection audit reporter.
//
// It must be called before Run, never during one. Passing nil silences audit
// reports without disabling projection.
func (l *Loop) SetProjectionReportHook(hook func(ProjectionReport)) {
	l.cfg.OnProjection = hook
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

// SetJournal installs the edit-recovery seam after construction.
//
// The host rebuilds it when the active session changes, so a checkpoint can
// never be attributed to the wrong session's timeline. It must be called before
// Run, never during one. Passing nil disables checkpointing.
func (l *Loop) SetJournal(journal EditJournal) {
	l.cfg.Journal = journal
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
//
// When a Journal is installed, the run's mutation boundary is captured before
// the first mutating tool batch and published when the run ends. The commit is
// attempted even after cancellation: the mutation already happened, so its
// recovery point must not be dropped merely because the turn stopped early.
func (l *Loop) Run(ctx context.Context, messages []Message) (turns []Turn, runErr error) {
	l.mutationMu.Lock()
	l.runSeq++
	runID := fmt.Sprintf("run-%d", l.runSeq)
	l.lastMutation = MutationProvenance{}
	l.mutationMu.Unlock()

	edit := &runEdit{journal: l.cfg.Journal}
	defer func() {
		if edit.txn == nil {
			return
		}
		commitErr := edit.txn.Commit(context.WithoutCancel(ctx))
		if provenance, ok := edit.txn.Mutation(); ok {
			provenance.RunID = runID
			l.recordMutation(provenance)
		}
		if commitErr == nil {
			return
		}
		published := fmt.Errorf("agent: publish edit checkpoint: %w", commitErr)
		if runErr == nil {
			runErr = published
			return
		}
		runErr = fmt.Errorf("%w; %v", runErr, published)
	}()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	live := make([]Message, len(messages))
	copy(live, messages)
	// origins is aligned with live so a canonical-contract failure can name
	// the constructing stage. The input is the replayed transcript plus the
	// current request; anything this run appends is attributed as it is added.
	origins := historyOrigins(len(live))

	// maxUnproductiveTurns is a semantic bound, not a resource bound. A turn
	// that executed nothing and produced no answer cannot change the runtime's
	// state, so one recovery request is enough; a second such turn is terminal.
	// Counting this from outcomes rather than from the offered tool slice is
	// load-bearing: a projector may withhold the tool a model keeps proposing
	// while still offering others, and the refusal must still influence the
	// continuation decision.
	const maxUnproductiveTurns = 2

	tools := l.cfg.Tools

	unproductiveTurns := 0
	// forceAnswer closes the action surface for one request after the model
	// has twice responded only with refused proposals. The run then ends with
	// the model's answer instead of a hard block; completion still decides
	// whether that answer is sufficient.
	forceAnswer := false
	// answerRetried records that an answer-only request already came back
	// without visible text. The action surface is closed, so there is nothing
	// else the model can do; one further request is the bounded recovery. It
	// covers both a forced answer after refusals and a natural evidence
	// boundary where the first answer request produced only reasoning.
	answerRetried := false
	// executionRecoveryAttempted and normalizationRecoveryAttempted are the
	// bounded protocol recoveries. Each is spent at most once per run and the
	// run/turn budget still bounds the whole loop, so neither can become an
	// infinite retry.
	executionRecoveryAttempted := false
	normalizationRecoveryAttempted := false
	// executionIntentPending means the previous turn announced an action and
	// the runtime asked for the actual call. If the next turn still has no
	// call, the failure is classified as a protocol failure, never executed.
	executionIntentPending := false
	// normalizationPending means a failed normalization is awaiting the one
	// recovery request. Valid calls in the next turn clear it; another failed
	// turn is terminal for the run's protocol stage.
	normalizationPending := false
	// activityObserved records that any turn produced a call or visible text.
	// An empty model stop after activity is a completion question, not a
	// missing-output protocol failure; NO_MODEL_OUTPUT is reserved for a run
	// that never produced text or a call at all.
	activityObserved := false
	stopReason := ""
	turns = nil
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
			// Compaction rewrites the conversation, so the per-message history
			// provenance no longer describes it. The reconstructed context is
			// history by definition.
			origins = historyOrigins(len(live))
			continue
		}

		// The projection is a view of this request, never a mutation of the
		// live conversation. The loop keeps the exact transcript and the full
		// tool set; only what reaches the provider is narrowed. This is what
		// makes an elided tool result recoverable: the evidence state explains
		// it and a later turn can still re-read the source.
		requestMessages := live
		requestSystem := l.system()
		activeTools := tools
		answerEligible := false
		terminal := false
		closed := false
		refusals := map[string]string(nil)
		if l.cfg.Project != nil {
			projected := l.cfg.Project(ProjectionRequest{
				Messages:    copyMessages(live),
				Turns:       append([]Turn(nil), turns...),
				Turn:        len(turns),
				Tools:       tools,
				System:      requestSystem,
				ForceAnswer: forceAnswer,
			})
			emptyProjection := false
			if projected.Messages != nil {
				if len(projected.Messages) == 0 {
					// An empty conversation cannot be sent. Treat it as a
					// projector bug and keep the authoritative live context
					// rather than silently asking the provider to answer
					// nothing.
					emptyProjection = true
				} else {
					requestMessages = projected.Messages
				}
			}
			if projected.Tools != nil {
				activeTools = projected.Tools
			}
			if len(projected.System) > 0 {
				requestSystem = append(append([]ai.SystemPromptBlock(nil), requestSystem...), projected.System...)
			}
			report := projected.Report
			answerEligible = report.AnswerEligible
			terminal = report.Terminal
			stopReason = report.StopReason
			refusals = projected.Refusals
			report.Applied = true
			report.Turn = len(turns)
			if emptyProjection {
				report.Reason = joinReason(report.Reason, "empty message projection ignored")
			}
			// A projection may narrow the tool surface, never widen it. A
			// projector cannot create a capability the loop was not built
			// with, even if it returns a fabricated Tool value.
			filtered, removed := intersectTools(tools, activeTools)
			// Always use the resolved set, even when nothing was removed: a
			// projector may have supplied a different value under a known
			// name, and the loop must execute its own capability instance.
			activeTools = filtered
			// AnswerEligible and Terminal are the control plane's semantic
			// closure, not hints. The loop owns the request shape, so it must
			// withdraw every tool rather than trusting a projector to also
			// return an empty slice. Otherwise a model could execute a call on
			// a surface the runtime already declared closed.
			if answerEligible || terminal {
				activeTools = []Tool{}
			}
			closed = answerEligible || terminal || len(activeTools) == 0
			// ForceAnswer is loop policy, not a projector hint. Withdraw the
			// surface even when the projector does not: the model already
			// spent two turns on proposals the runtime refused, so another
			// call can only be refused again. This runs before the audit
			// fields are filled so the report matches the request.
			if forceAnswer {
				activeTools = []Tool{}
				closed = true
			}
			if removed > 0 {
				report.Reason = joinReason(report.Reason,
					fmt.Sprintf("projection proposed %d tool(s) outside the loop's tool set; ignored", removed))
			}
			// The loop owns the final request shape, so it fills the audit
			// fields from the request it will actually send. A projector may
			// describe its intent, but the offered names and message count must
			// reflect the post-intersection reality or the audit would lie.
			report.ToolNames = report.ToolNames[:0]
			for _, tool := range activeTools {
				report.ToolNames = append(report.ToolNames, tool.Name())
			}
			report.Messages = len(requestMessages)
			if l.cfg.OnProjection != nil {
				l.cfg.OnProjection(report)
			}
			// The control plane resolved the objective's required capabilities
			// and the runtime cannot perform one of them. No later turn can
			// either, so the request is never sent: the model is not asked to
			// discover a limit by proposing an action that cannot run, and the
			// turn is not spent on prose that explains it.
			if len(report.MissingCapabilities) > 0 {
				reason := strings.TrimSpace(report.StopReason)
				if reason == "" {
					reason = "the runtime cannot perform a required capability: " +
						strings.Join(report.MissingCapabilities, ", ")
				}
				return turns, fmt.Errorf("%w: %s", ErrCapabilityUnavailable, reason)
			}
		} else if forceAnswer {
			activeTools = []Tool{}
			closed = true
		}

		converted, requestOrigins, err := l.convert(requestMessages, origins)
		if err != nil {
			return turns, fmt.Errorf("agent: convert context: %w", err)
		}
		// The canonical contract is checked before any transport sees the
		// conversation. A malformed message is an internal construction
		// failure: sending it would ask a provider to diagnose runtime state.
		if err := ai.ValidateMessages(converted, requestOrigins); err != nil {
			return turns, fmt.Errorf("%w: %w", ErrInvalidProviderContext, err)
		}

		activeByName := make(map[string]Tool, len(activeTools))
		for _, tool := range activeTools {
			activeByName[tool.Name()] = tool
		}
		specs := Specs(activeTools)

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
			System:          requestSystem,
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
		origins = append(origins, ai.ProvenanceAssistant)

		calls := assistant.ToolCalls()
		if len(calls) > 0 || strings.TrimSpace(assistant.Text()) != "" {
			activityObserved = true
		}
		if len(calls) > 0 {
			// A canonical call is the correction both protocol recoveries ask
			// for. It may still be refused by authority or schema, but it is
			// no longer a missing or malformed representation.
			normalizationPending = false
			executionIntentPending = false
		}
		if len(calls) == 0 {
			turns = append(turns, turn)
			if l.cfg.OnTurn != nil {
				l.cfg.OnTurn(turn)
			}
			// A terminal surface with neither a proposal nor an answer is a
			// blocked task, not a clean model stop. Preserve the terminal reason
			// for the outcome layer instead of letting an empty turn look like a
			// plausible completion attempt.
			if terminal && strings.TrimSpace(assistant.Text()) == "" {
				reason := strings.TrimSpace(stopReason)
				if reason == "" {
					reason = "the runtime closed the task because no further action can make progress"
				}
				return turns, fmt.Errorf("%w: %s", ErrTaskBlocked, reason)
			}
			// Serialized syntax that could not be normalized is a provider
			// protocol failure, not an answer. Spend one bounded recovery
			// request asking for the canonical call; never execute or invent
			// the partial envelope. A second failure is terminal for this run.
			if len(assistant.NormalizationFailures) > 0 && !closed {
				normalizationPending = true
				if !normalizationRecoveryAttempted && len(activeTools) > 0 && len(turns) < l.cfg.MaxTurns {
					normalizationRecoveryAttempted = true
					if strings.TrimSpace(l.cfg.ProtocolRecoveryRequest) != "" {
						live = append(live, UserMessage{Text: l.cfg.ProtocolRecoveryRequest})
						origins = append(origins, ai.ProvenanceRecovery)
					}
					continue
				}
				return turns, fmt.Errorf("%w: %s", ErrToolNormalizationFailed,
					normalizationFailureReason(assistant.NormalizationFailures))
			}
			// Execution intent without a tool call is its own lifecycle
			// failure. The prose is not an action: one bounded request asks for
			// the canonical call, and a second narration is reported rather
			// than executed or promoted to an answer.
			if text := strings.TrimSpace(assistant.Text()); text != "" && !closed &&
				len(activeTools) > 0 && executionIntentWithoutTool(text) {
				executionIntentPending = true
				if !executionRecoveryAttempted && len(turns) < l.cfg.MaxTurns {
					executionRecoveryAttempted = true
					if strings.TrimSpace(l.cfg.ExecutionRecoveryRequest) != "" {
						live = append(live, UserMessage{Text: l.cfg.ExecutionRecoveryRequest})
						origins = append(origins, ai.ProvenanceRecovery)
					}
					continue
				}
				return turns, fmt.Errorf("%w: the model announced an execution it never emitted as a tool call",
					ErrExecutionIntentWithoutTool)
			}
			// A model can stop without any visible text immediately after a
			// refusal-only turn. The refusal is feedback it has not answered
			// yet, and silence is not an answer. Close the surface once and ask
			// for the answer from the evidence already observed, exactly as the
			// repeated-refusal path does. The empty assistant message is
			// dropped from the provider context because an empty turn is not
			// useful to the model and some providers reject it.
			if strings.TrimSpace(assistant.Text()) == "" && !closed && !forceAnswer &&
				unproductiveTurns > 0 && len(turns) < l.cfg.MaxTurns {
				// The turn stays in turns as a run observation, but it is not
				// conversation content: a reasoning-only assistant message
				// cannot be replayed. Drop it from the next request.
				live = live[:len(live)-1]
				origins = origins[:len(origins)-1]
				forceAnswer = true
				continue
			}
			// The surface was already answer-only and the model returned only
			// reasoning. That is the same lifecycle failure one request later:
			// the runtime asked for the answer, nothing executed, and the run
			// still has no visible text to hand the user. Drop the empty turn
			// and ask once more from the same closed surface. Without this, a
			// model that reaches the forced-answer request and stays silent
			// bypasses response generation entirely and the task ends with an
			// empty final message. The bound is one retry, not a loop.
			if strings.TrimSpace(assistant.Text()) == "" && (closed || forceAnswer) &&
				!answerRetried && len(turns) < l.cfg.MaxTurns {
				live = live[:len(live)-1]
				origins = origins[:len(origins)-1]
				if strings.TrimSpace(l.cfg.AnswerRequest) != "" {
					live = append(live, UserMessage{Text: l.cfg.AnswerRequest})
					origins = append(origins, ai.ProvenanceRecovery)
				}
				// Keep the retry answer-only even if a projector would
				// otherwise re-offer the probe surface. The model already had
				// an answer boundary and produced no answer; reopening tools
				// would turn the lifecycle failure into another action loop.
				forceAnswer = true
				answerRetried = true
				continue
			}
			if strings.TrimSpace(assistant.Text()) == "" {
				// The previous turn was already a protocol recovery request.
				// Silence after it is the outcome of that stage, not a generic
				// empty model stop.
				if executionIntentPending {
					return turns, fmt.Errorf("%w: the model announced an execution and then produced no tool call",
						ErrExecutionIntentWithoutTool)
				}
				if normalizationPending {
					return turns, fmt.Errorf("%w: provider tool-call syntax did not resolve after recovery",
						ErrToolNormalizationFailed)
				}
				if activityObserved {
					// Work already ran; an empty closing turn is a completion
					// question. Returning the turns lets the evaluator judge
					// the evidence instead of replacing it with a protocol
					// error.
					return turns, nil
				}
				return turns, fmt.Errorf("%w: the provider produced no visible text and no tool call",
					ErrNoModelOutput)
			}
			return turns, nil
		}

		// A substantive answer is the terminal transition when the rest of the
		// batch is continuation: every call is either a capability the task
		// stage withheld or an exact repetition of an observation already in
		// hand, so no call can add evidence. Dispatching them would only append
		// refusal or reuse cards after a finished answer. A batch with a new
		// offered capability keeps the normal path; narration before real work
		// is not an answer. The call blocks are dropped from the stored
		// assistant message too, because a persisted call with no result is an
		// invalid conversation for the next provider request. A closed surface
		// has no recovery, so its text is the only output available.
		if text := strings.TrimSpace(assistant.Text()); text != "" &&
			(closed || (!answerIsDeferredPlan(text) &&
				l.noEvidenceInBatch(activeByName, calls, len(turns)))) {
			turn.Assistant = answerWithoutToolCalls(assistant)
			turns = append(turns, turn)
			if l.cfg.OnTurn != nil {
				l.cfg.OnTurn(turn)
			}
			return turns, nil
		}
		if closed {
			// Defence in depth: a closed surface must never execute a stale
			// call even when the model emitted no answer text.
			activeByName = map[string]Tool{}
		}

		// Steering arrives only at a boundary: mid-batch injection would
		// orphan results for calls that already ran.
		if l.cfg.GetSteeringMessages != nil {
			if steered := l.cfg.GetSteeringMessages(ctx); len(steered) > 0 {
				live = append(live, steered...)
				for range steered {
					origins = append(origins, ai.ProvenanceSynthetic)
				}
			}
		}

		outcomes, admissionErr := l.dispatch(ctx, activeByName, refusals, assistant, calls, turn, len(turns), edit)
		turn.Results = outcomes
		for _, outcome := range outcomes {
			live = append(live, ToolResultMessage{
				ToolCallID: outcome.CallID,
				ToolName:   outcome.ToolName,
				Content:    ai.TextBlocks(outcome.Result.Text),
				IsError:    outcome.Result.IsError,
			})
			origins = append(origins, ai.ProvenanceToolResult)
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
		// A terminal projection has closed the task. If the model used the
		// request to answer, keep that answer for the completion evaluator;
		// otherwise the run ends here rather than spending another generation
		// rediscovering that no action is available.
		if terminal {
			if strings.TrimSpace(assistant.Text()) != "" {
				return turns, nil
			}
			reason := strings.TrimSpace(stopReason)
			if reason == "" {
				reason = "the runtime closed the task because no further action can make progress"
			}
			return turns, fmt.Errorf("%w: %s", ErrTaskBlocked, reason)
		}

		// The continuation decision belongs here, once, and it is derived from
		// what the turn actually did. A non-refused outcome is work: it may add
		// evidence, run a check, or fail in a way the failure ledger records.
		// A refused outcome is a control decision, not work. A turn made only
		// of refusals cannot change the runtime's state, so it is unproductive
		// regardless of how many other tools the projection still offered.
		executed := 0
		offeredRefusals := 0
		rejected := 0
		for _, outcome := range outcomes {
			// A rejected invocation never reached the capability with a valid
			// request. It is feedback for a correction, not work: treating it
			// as executed would let a model reset the unproductive-turn bound
			// by submitting an endless stream of malformed proposals.
			if !outcome.Refused() && !outcome.Result.Rejected() {
				executed++
				continue
			}
			if outcome.Result.Rejected() {
				rejected++
			}
			// A refused call to a tool this projection offered was withheld
			// by policy (grounding, authority, approval) at execution time.
			// A call to a tool the projection already withheld is the model
			// ignoring the offered surface; forcing an answer there would
			// reward the same mistake.
			if _, offered := activeByName[outcome.ToolName]; offered {
				offeredRefusals++
			}
		}
		if executed > 0 {
			unproductiveTurns = 0
			continue
		}
		// Nothing ran. A generic refusal is control feedback the model has not
		// seen yet: CandidateRejected means the runtime withheld the action and
		// asked the model to choose another. The narration that accompanied the
		// refused call is therefore not a final answer; treating it as one ends
		// the turn before the model can recover from its own rejected proposal.
		//
		// AnswerRequired is the exception. The runtime offered no tool at all,
		// so there is no other action to choose and the text is the candidate
		// answer. Every other refusal gets one bounded recovery request.
		if answerRequired(outcomes) && strings.TrimSpace(assistant.Text()) != "" {
			return turns, nil
		}
		unproductiveTurns++
		if unproductiveTurns < maxUnproductiveTurns {
			continue
		}
		// The model received the refusal and still produced nothing actionable.
		// If it also produced text, that text is the best answer available;
		// completion decides whether it is sufficient.
		if strings.TrimSpace(assistant.Text()) != "" {
			return turns, nil
		}
		// Nothing can execute, so a further tool turn cannot make progress.
		// Close the action surface and ask for the answer instead of ending
		// the run with a hard block; the answer remains a completion candidate.
		// This applies only when an offered capability was refused: a model
		// that keeps calling a withheld tool has already ignored the surface,
		// and a model with no offered tools already had its answer-only turn.
		if !forceAnswer && !closed && offeredRefusals > 0 {
			// A refusal of an identical request-level rejection is correction
			// feedback, not a hostile repeat of a withheld capability. Give the
			// model one bounded corrected attempt before asking for prose; the
			// modified invocation is a different proposal and may execute.
			if allFailureRejected(outcomes) && unproductiveTurns <= maxUnproductiveTurns {
				continue
			}
			forceAnswer = true
			continue
		}
		reason := strings.TrimSpace(stopReason)
		if reason == "" {
			switch {
			case rejected > 0 && offeredRefusals == 0:
				reason = "the runtime rejected every proposed invocation and the model did not submit a corrected action"
			case closed:
				reason = "the runtime offered no tool and the model did not answer"
			default:
				reason = "the runtime refused every proposed action and the model did not answer"
			}
		}
		return turns, fmt.Errorf("%w: %s", ErrTaskBlocked, reason)
	}
}

// answerRequired reports whether every outcome is a withheld call made after the
// runtime closed the action surface. In that case there is no other tool to
// choose, so the assistant text accompanying the refusal is the candidate
// answer. A single generic CandidateRejected outcome means the model still has
// an action to revise, so the run must continue instead.
func answerRequired(outcomes []ToolOutcome) bool {
	if len(outcomes) == 0 {
		return false
	}
	for _, outcome := range outcomes {
		if !outcome.Result.AnswerRequired {
			return false
		}
	}
	return true
}

// allFailureRejected reports whether every outcome in the batch was refused
// because it repeated an invocation the failure ledger already rejected. That
// is the one refusal shape where the model has an actionable correction: change
// the arguments, which produces a different signature, rather than choose a
// different capability because the surface closed.
func allFailureRejected(outcomes []ToolOutcome) bool {
	if len(outcomes) == 0 {
		return false
	}
	for _, outcome := range outcomes {
		if !outcome.Result.FailureRejected {
			return false
		}
	}
	return true
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

// SetSessionID changes the conversation identity forwarded to providers.
//
// It is called when the host adopts, resumes, or replaces a session. The
// provider uses it for routing and prompt-cache affinity; changing it between
// turns is safe, but changing it during a turn would split one conversation.
func (l *Loop) SetSessionID(id string) {
	l.cfg.SessionID = id
}

// historyOrigins labels a restored or reconstructed conversation: every entry
// is history except the final user request that triggers the run.
func historyOrigins(count int) []ai.Provenance {
	origins := make([]ai.Provenance, count)
	for i := range origins {
		origins[i] = ai.ProvenanceHistory
	}
	if count > 0 {
		origins[count-1] = ai.ProvenanceUser
	}
	return origins
}

// convert turns agent messages into provider messages.
//
// origins is aligned with messages and is carried through so the canonical
// validation can name the constructing subsystem. A host TransformContext hook
// may rewrite the list arbitrarily, so it is treated as history: the runtime
// no longer knows which stage produced those entries.
func (l *Loop) convert(messages []Message, origins []ai.Provenance) ([]ai.Message, []ai.Provenance, error) {
	if l.cfg.TransformContext != nil {
		messages = l.cfg.TransformContext(messages)
		origins = nil
	}
	out := make([]ai.Message, 0, len(messages))
	outOrigins := make([]ai.Provenance, 0, len(messages))
	for i, msg := range messages {
		provenance := ai.ProvenanceUnknown
		if i < len(origins) && origins[i] != "" {
			provenance = origins[i]
		}
		switch m := msg.(type) {
		case ai.Message:
			out = append(out, m)
			outOrigins = append(outOrigins, provenance)
		case Message:
			converted, ok := m.toLLM()
			if !ok {
				// UI-only messages are dropped rather than sent.
				continue
			}
			out = append(out, converted)
			outOrigins = append(outOrigins, provenance)
		default:
			return nil, nil, fmt.Errorf("unsupported message type %T", msg)
		}
	}
	return out, outOrigins, nil
}

// drain consumes a stream to completion and returns the assembled message.
//
// It forwards visible text to the loop's OnTextDelta hook through the shared
// ai.DrainStream, so the loop and any other consumer (the $sup answer surface)
// cannot drift on what draining a stream means: the loop's contract is still
// the completed message.
func (l *Loop) drain(ctx context.Context, stream *ai.Stream) (ai.AssistantMessage, error) {
	return ai.DrainStreamHooks(ctx, stream, ai.DrainHooks{
		OnText:     l.cfg.OnTextDelta,
		OnThinking: l.cfg.OnThinkingDelta,
	})
}

// runEdit tracks one run's optional edit transaction.
type runEdit struct {
	journal EditJournal
	txn     EditTransaction
}

// dispatch runs a batch of tool calls.
//
// Calls marked exclusive run alone; shared calls run concurrently. Results are
// always returned in the order the model emitted the calls, so the transcript
// matches the assistant message regardless of completion order.
func (l *Loop) dispatch(
	ctx context.Context,
	byName map[string]Tool,
	refusals map[string]string,
	assistant ai.AssistantMessage,
	calls []ai.ToolCall,
	turn Turn,
	turnIndex int,
	edit *runEdit,
) ([]ToolOutcome, error) {
	// The recovery point is captured before the first batch that can change the
	// workspace, and before any call in it is authorized. A journal failure
	// refuses the batch outright: executing a mutation we cannot undo would
	// trade a recoverable mistake for an unrecoverable one.
	if edit != nil && edit.txn == nil && edit.journal != nil && batchMayMutate(byName, calls) {
		txn, err := edit.journal.BeginEdit(ctx, EditBoundary{
			Turn:  turnIndex,
			Tools: batchToolNames(calls),
		})
		if err != nil {
			out := make([]ToolOutcome, len(calls))
			for i, call := range calls {
				out[i] = ToolOutcome{
					CallID:   call.ID,
					ToolName: call.Name,
					Args:     call.Arguments,
					Result: Result{
						Text: fmt.Sprintf(
							"recovery checkpoint unavailable: %v; no files were modified", err),
						IsError:      true,
						FailureClass: ToolFailureDispatch,
					},
				}
			}
			return out, nil
		}
		edit.txn = txn
	}

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
			outcome := l.execute(ctx, byName, refusals, assistant, call, turn, turnIndex)
			l.noteEvidence(outcome.Result)
			l.noteObservation(call, outcome.Result, turnIndex)
			record(position, outcome)
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

// hasTool reports whether the loop was constructed with a tool of this name.
func (l *Loop) hasTool(name string) bool {
	_, ok := l.builtTool(name)
	return ok
}

// builtTool returns the loop's own capability under name.
func (l *Loop) builtTool(name string) (Tool, bool) {
	for _, tool := range l.cfg.Tools {
		if tool.Name() == name {
			return tool, true
		}
	}
	return nil, false
}

// batchMayMutate reports whether any call in a batch can change the workspace.
//
// It is deliberately conservative in the safe direction: a tool declares its
// action surface, and a delegated call is treated as mutating because the child
// shares this workspace. An unrecognised tool falls back to its approval tier,
// so the only way to be skipped is a tool that is provably read-only.
func batchMayMutate(byName map[string]Tool, calls []ai.ToolCall) bool {
	for _, call := range calls {
		tool, ok := byName[call.Name]
		if !ok {
			continue
		}
		// A capability may refine its tier from this invocation's arguments
		// (verify runs a service and checks that can include a mutating
		// command). The journal must cover that per-call possibility even when
		// the static action surface is execution-only.
		if reporter, ok := tool.(CapabilityReporter); ok {
			if reporter.Capability(call.Arguments) == TierWrite {
				return true
			}
		}
		for _, class := range ToolActionClasses(tool) {
			if class == ActionMutate || class == ActionDelegate {
				return true
			}
		}
	}
	return false
}

// answerWithoutToolCalls returns the answer-shaped projection of a final
// assistant message.
//
// A model can emit visible prose and tool calls in one message. When the prose
// is a substantive answer and the batch cannot add evidence (or the surface is
// closed), the calls are continuation after the answer, not actions the runtime
// performed. Keeping them would execute work after a finished answer or leave
// an invalid tool call without a result in the stored conversation. The text,
// reasoning, and usage are preserved; only call blocks and the tool-use stop
// reason are removed.
func answerWithoutToolCalls(message ai.AssistantMessage) ai.AssistantMessage {
	content := make([]ai.ContentBlock, 0, len(message.Content))
	for _, block := range message.Content {
		if _, isCall := block.(ai.ToolCall); isCall {
			continue
		}
		content = append(content, block)
	}
	message.Content = content
	if message.StopReason == ai.StopToolUse {
		message.StopReason = ai.StopEndTurn
	}
	return message
}

// noEvidenceInBatch reports whether every call is a non-policy capability that
// cannot add evidence: the task projection withheld it, or it repeats an
// observation already current in this run.
//
// A new offered capability, an unknown tool, or a policy-sensitive proposal
// returns false. Policy-sensitive calls stay visible because they are attempted
// actions, and a new offered observation may be real work rather than
// continuation after an answer.
func (l *Loop) noEvidenceInBatch(active map[string]Tool, calls []ai.ToolCall, turnIndex int) bool {
	if l.cfg.Project == nil || len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		tool, built := l.builtTool(call.Name)
		if !built || policySensitive(tool) {
			return false
		}
		if _, offered := active[call.Name]; offered {
			if _, reused := l.priorObservation(call, turnIndex); !reused {
				return false
			}
		}
	}
	return true
}

// batchToolNames lists the tools in a batch, in call order.
func batchToolNames(calls []ai.ToolCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, call.Name)
	}
	return names
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
	refusals map[string]string,
	assistant ai.AssistantMessage,
	call ai.ToolCall,
	turn Turn,
	turnIndex int,
) (out ToolOutcome) {
	fail := func(text string) ToolOutcome {
		return ToolOutcome{CallID: call.ID, ToolName: call.Name, Result: Result{Text: text, IsError: true}}
	}
	failClass := func(class, text string) ToolOutcome {
		return ToolOutcome{CallID: call.ID, ToolName: call.Name, Result: Result{
			Text: text, IsError: true, FailureClass: class,
		}}
	}

	// Every outcome the transcript keeps needs a matching start and finish
	// report. A reused or refused call returns before the execution seam, so
	// deferring the report to the host's post-run pass would replay an early
	// outcome after a later answer. AnswerRequired refusals are deliberately
	// excluded: the transcript projection removes them, so publishing a card
	// would leave a live block the completed turn does not own.
	callStarted := time.Now()
	started := time.Time{}
	announced := false
	announce := func(args map[string]any) {
		if announced || l.cfg.OnToolProgress == nil {
			return
		}
		announced = true
		if args == nil {
			args = call.Arguments
		}
		elapsed := time.Since(callStarted)
		if !started.IsZero() {
			elapsed = time.Since(started)
		}
		l.cfg.OnToolProgress(ToolProgress{
			CallID:   call.ID,
			ToolName: call.Name,
			Args:     args,
			Elapsed:  elapsed,
		})
	}
	defer func() {
		if out.CallID == "" || out.Result.AnswerRequired {
			return
		}
		announce(out.Args)
		if l.cfg.OnToolProgress == nil {
			return
		}
		args := out.Args
		if args == nil {
			args = call.Arguments
		}
		elapsed := time.Since(callStarted)
		if !started.IsZero() {
			elapsed = time.Since(started)
		}
		l.cfg.OnToolProgress(ToolProgress{
			CallID:   call.ID,
			ToolName: call.Name,
			Args:     args,
			Result:   out.Result,
			Final:    true,
			Elapsed:  elapsed,
		})
	}()

	// A repeated failed proposal is decided before the capability boundary.
	// The control plane recorded the failure class and retry budget; this
	// refusal is how that memory becomes effective without executing the call
	// again. It is a control decision, not an execution failure, so completion
	// does not punish the model for a refusal the runtime chose.
	if reason, refused := refusals[CallSignature(call.Name, call.Arguments)]; refused && reason != "" {
		return ToolOutcome{
			CallID:   call.ID,
			ToolName: call.Name,
			Args:     call.Arguments,
			Result: Result{
				Text:              reason,
				IsError:           true,
				CandidateRejected: true,
				FailureRejected:   true,
				AnswerRequired:    len(byName) == 0,
			},
		}
	}

	tool, ok := byName[call.Name]
	if !ok {
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		// When the runtime offers no tools at all, any call is an
		// answer-required refusal, whether or not the loop was constructed with
		// that name. This is the closed-surface case: there is no other tool to
		// choose, and a call that is visibly refused must not remain
		// outstanding work after the answer. It is checked before the
		// partial-surface branch so the model is never told tools are
		// "available" when the surface is closed.
		if l.cfg.Project != nil && len(byName) == 0 {
			// A mutating, executing, delegating, or external proposal on a
			// closed surface is a policy refusal, not merely continuation
			// after an answer: it is an attempted action the user may need to
			// see. Observation calls after the answer are continuation and are
			// projected out of the transcript (see TranscriptTurn).
			policyRefusal := false
			if tool, ok := l.builtTool(call.Name); ok {
				policyRefusal = policySensitive(tool)
			}
			return ToolOutcome{
				CallID:   call.ID,
				ToolName: call.Name,
				Args:     call.Arguments,
				Result: Result{
					Text:              fmt.Sprintf("No tool is available in the current task stage; this call is not offered for the objective. Answer from the evidence already established. Ignored call: %q.", call.Name),
					IsError:           true,
					FailureClass:      ToolFailureCapabilityUnavailable,
					CandidateRejected: true,
					AnswerRequired:    !policyRefusal,
				},
			}
		}
		// A tool the loop was built with but the task-shaped projection
		// withheld is a runtime action-space refusal, not an execution
		// failure. The model is told which tools are still available;
		// completion does not count the refusal as a failed call.
		if l.cfg.Project != nil && l.hasTool(call.Name) {
			return ToolOutcome{
				CallID:   call.ID,
				ToolName: call.Name,
				Args:     call.Arguments,
				Result: Result{
					Text: fmt.Sprintf("Tool %q is not offered in the current task stage. Available tools: %s.",
						call.Name, strings.Join(sorted(names), ", ")),
					IsError:           true,
					FailureClass:      ToolFailureCapabilityUnavailable,
					CandidateRejected: true,
				},
			}
		}
		return ToolOutcome{
			CallID: call.ID, ToolName: call.Name, Args: call.Arguments,
			Result: Result{
				Text:         fmt.Sprintf("Tool %q not found. Available tools: %s.", call.Name, strings.Join(sorted(names), ", ")),
				IsError:      true,
				FailureClass: ToolFailureUnknownTool,
			},
		}
	}

	// An identical read-only observation from an earlier turn that is still
	// current adds no evidence. Answer from the recorded result rather than
	// spending another tool call and context window; the model can still
	// change the coordinate or re-inspect after a change. A same-batch
	// duplicate is not reused: concurrency semantics and each call's own
	// result remain intact.
	if text, reused := l.priorObservation(call, turnIndex); reused {
		return ToolOutcome{
			CallID:   call.ID,
			ToolName: call.Name,
			Args:     call.Arguments,
			Result: Result{
				Text:    text,
				Useless: true,
				Reused:  true,
				Details: map[string]any{"reused": true},
			},
		}
	}

	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	if err := ValidateArgs(tool, args); err != nil {
		// A schema violation is reported back to the model rather than
		// aborting the turn, so it can fix its own arguments.
		return ToolOutcome{
			CallID: call.ID, ToolName: call.Name, Args: args,
			Result: Result{
				Text:         fmt.Sprintf("Invalid arguments for %q: %v", call.Name, err),
				IsError:      true,
				FailureClass: ToolFailureInvalidArgument,
			},
		}
	}

	if l.cfg.BeforeToolCall != nil {
		before := l.cfg.BeforeToolCall(BeforeToolCallContext{
			Tool: tool, Call: call, Args: args, Turn: turn, Turns: turnIndex,
		})
		if before.Block {
			reason := before.Reason
			if reason == "" {
				reason = "blocked by policy"
			}
			return ToolOutcome{
				CallID: call.ID, ToolName: call.Name, Args: args,
				Result: Result{Text: reason, IsError: true, FailureClass: ToolFailureAuthorization},
			}
		}
		if before.Args != nil {
			args = before.Args
			if err := ValidateArgs(tool, args); err != nil {
				return failClass(ToolFailureInvalidArgument,
					fmt.Sprintf("Invalid arguments for %q: %v", call.Name, err))
			}
		}
	}

	// Semantic admission runs before the security boundary. It is not an
	// authorization decision: it refuses an action the user's own request did
	// not ground, so a mutating proposal can never reach the approval prompt
	// or execution under a read-only or actionless objective. The refusal is
	// a control decision (refused, not a failed execution), so completion does
	// not punish the model for adapting.
	if l.cfg.Grounding != nil {
		// Delegation may only narrow authority. A request that did not ground
		// a mutation cannot authorize a writable child, whatever the model's
		// assignment says; the child is forced read-only here. This is the
		// same bounded-observation rule as a direct read: the child may gather
		// evidence, but it cannot mutate under an ungrounded objective.
		if HasActionClass(tool, ActionDelegate) && !l.cfg.Grounding.MutationIntent {
			args["readOnly"] = true
		}
		assessment := ValidateProposal(l.cfg.Grounding, ProposalForCall(tool, args))
		if assessment.Verdict == contract.GroundingReject {
			return ToolOutcome{
				CallID:   call.ID,
				ToolName: call.Name,
				Args:     args,
				Result: Result{
					Text:              "Action not grounded in the request: " + assessment.Reason(),
					IsError:           true,
					CandidateRejected: true,
				},
			}
		}
	}

	// A call that needs a capability the live workspace lacks asks the host
	// for an execution-workspace handoff before the security boundary sees it.
	// The control plane owns the decision and moves the one live gate; this
	// loop re-reads that gate during authorization below. Running after
	// grounding is what keeps a handoff from strengthening an action the
	// user's request never authorized.
	if l.cfg.WorkspaceHandoff != nil {
		l.cfg.WorkspaceHandoff(call, tool, args)
	}

	// A cancellation that landed while arguments were being validated or a
	// human was being asked must not start the tool. The check is repeated
	// after authorization because approval can block indefinitely.
	if err := ctx.Err(); err != nil {
		return ToolOutcome{
			CallID: call.ID, ToolName: call.Name, Args: args,
			Result: Result{Text: err.Error(), IsError: true, FailureClass: ToolFailureCancelled},
		}
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
			// A denied call carries the decision that refused it, so an audit
			// can tell "not authorized" from "authorized but failed" without
			// parsing error text. Nothing executed.
			return ToolOutcome{
				CallID:        call.ID,
				ToolName:      call.Name,
				Args:          args,
				Result:        Result{Text: failure, IsError: true, FailureClass: ToolFailureAuthorization},
				Authorization: &resolved,
			}
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

	started = time.Now()
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
		announce(args)
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

	return ToolOutcome{
		CallID:        call.ID,
		ToolName:      call.Name,
		Args:          args,
		Result:        result,
		Authorization: decision,
	}
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
			// A human refusal is a denial, not a failed execution. Recording
			// it as DENY keeps completion from treating the refusal as an
			// outstanding tool failure while leaving the audit truthful.
			denied := decision
			denied.Outcome = security.OutcomeDeny
			denied.Rule = "approval_rejected"
			denied.Reason = "the operation was not approved"
			return denied, refusalText("approval rejected", decision), false
		}
		confirmed := l.cfg.Authorize.Evaluate(call, tool)
		if confirmed.Outcome != security.OutcomeAuto {
			// The operation no longer matches what was approved. It is still
			// a refusal rather than an execution failure, so mark it DENY for
			// completion's benefit while preserving the policy rule.
			denied := confirmed
			if denied.Outcome == security.OutcomeApprove {
				denied.Outcome = security.OutcomeDeny
				denied.Rule = "approval_not_confirmed"
				denied.Reason = "the approved operation could not be confirmed"
			}
			return denied, refusalText("authorization changed after approval", confirmed), false
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

// copyMessages returns a shallow copy of the message slice. A projector gets a
// private slice so appending or reordering cannot alter the loop's live
// conversation. Messages themselves are treated as immutable.
func copyMessages(messages []Message) []Message {
	out := make([]Message, len(messages))
	copy(out, messages)
	return out
}

// intersectTools keeps only tools present in the loop's constructed set. A
// projection may remove a tool; returning a tool the loop was not built with is
// a projector bug and must not become a capability.
func intersectTools(constructed, proposed []Tool) ([]Tool, int) {
	// Resolve by name to the loop's own instance. A projector is trusted
	// runtime code, but the loop still owns the capability boundary: a
	// projected value that reuses a known name must not replace the tool the
	// authorizer and executor were built around.
	byName := make(map[string]Tool, len(constructed))
	for _, tool := range constructed {
		byName[tool.Name()] = tool
	}
	out := make([]Tool, 0, len(proposed))
	seen := make(map[string]bool, len(proposed))
	removed := 0
	for _, tool := range proposed {
		built, known := byName[tool.Name()]
		if !known || seen[tool.Name()] {
			removed++
			continue
		}
		seen[tool.Name()] = true
		out = append(out, built)
	}
	return out, removed
}

// joinReason appends one audit reason without losing an earlier one.
func joinReason(existing, addition string) string {
	if existing == "" {
		return addition
	}
	if addition == "" {
		return existing
	}
	return existing + "; " + addition
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
