package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
	"github.com/PizenLabs/ohgo/internal/tokens"
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

// DefaultCorrectionToolCalls bounds the evidence the answer lifecycle's repairs
// may gather. Four calls is enough to read a cited file, inspect a declaration,
// or run one check after the model has spent a previous request correcting the
// claim. A larger number turns the bounded repair back into the investigation
// that already failed to support the claim.
const DefaultCorrectionToolCalls = 4

// AnswerDisposition is the runtime's classification of a turn's assistant
// prose. It is the single authority for whether a host may present that prose
// as an answer.
//
// One candidate has exactly one terminal disposition:
//
//	candidate → AnswerSuperseded   (a bounded correction replaces it)
//	candidate → AnswerRejected     (the publication review refuses it)
//	candidate → AnswerCommitted    (the response transaction accepts it)
//
// AnswerNarration is prose that was never a candidate: a tool-round preamble
// or an abandoned plan; it is model context only. Only AnswerCommitted may be
// presented as the task's answer, and only AnswerRejected may be presented as
// a terminal refusal.
type AnswerDisposition uint8

const (
	// AnswerUnclassified is a turn no publication decision touched. Hosts
	// treat its text as ordinary assistant content. The runtime classifies
	// every turn that can end a run; a host must not infer a disposition.
	AnswerUnclassified AnswerDisposition = iota
	// AnswerNarration is prose that announced or accompanied tool work. The
	// calls and their results remain visible; the prose does not.
	AnswerNarration
	// AnswerSuperseded is a candidate a bounded correction replaced. It is
	// internal repair context: no host may show it as an answer or as a
	// withdrawal. Its lineage remains in the turn's draft audit, but its prose
	// is withheld from the transcript, persistence, and replay.
	AnswerSuperseded
	// AnswerRejected is the terminal candidate the publication review
	// refused. It is not conversation and not an answer. A host may project
	// exactly one accountable artifact from RejectedAnswer.
	AnswerRejected
	// AnswerCommitted is the one answer the response transaction accepted.
	AnswerCommitted
)

// String names the disposition for logs and diagnostics.
func (d AnswerDisposition) String() string {
	switch d {
	case AnswerNarration:
		return "narration"
	case AnswerSuperseded:
		return "superseded"
	case AnswerRejected:
		return "rejected"
	case AnswerCommitted:
		return "committed"
	default:
		return "unclassified"
	}
}

// Turn is one request/response cycle.
type Turn struct {
	// Assistant is the message the model produced.
	Assistant ai.AssistantMessage
	// Results holds one entry per tool call, in the order the model emitted
	// them. It is empty when the model made no tool calls.
	Results []ToolOutcome
	// Err is set when the turn failed before any tool call was dispatched.
	Err error
	// Answer is the runtime's classification of the assistant prose. The
	// publication gate sets it before the turn reaches a host, so every
	// transcript, persistence, and plain-output path consumes the same
	// decision instead of re-deriving one from ordered events.
	Answer AnswerDisposition
	// AnswerReview is the control-plane boundary that refused a terminal
	// candidate. It is set only with AnswerRejected and is safe to show: it
	// names the review finding, never a scheduler counter, candidate number,
	// or internal state id. A host shows it verbatim.
	AnswerReview string
	// Draft is the internal claim-preserving version of this turn's prose. It
	// is audit and repair state, never a host projection: only the turn's
	// Answer disposition decides what may be shown. A superseded draft stays
	// on its turn so lineage records which valid claims a revision had to
	// preserve.
	Draft *AnswerDraft
}

// AnswerText returns the assistant prose this turn contributes to the run's
// answer.
//
// The AnswerDisposition is the publication authority, so a consumer that needs
// "the answer" must read it here rather than Assistant.Text(). A narrated,
// superseded, or rejected candidate keeps its raw prose for audit and repair,
// but it is not an answer even when the run stops before another candidate is
// committed.
//
// AnswerUnclassified is the zero value: a host that constructs turns directly
// (tests, subagent fixtures, a resumed legacy checkpoint) did not run the
// publication gate, so there is no decision to override. A loop-produced turn
// that can end a run is always classified before the run returns.
func (t Turn) AnswerText() string {
	switch t.Answer {
	case AnswerUnclassified, AnswerCommitted:
		return t.Assistant.Text()
	default:
		return ""
	}
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
	// Retry bounds transparent retries when the transport is temporarily
	// unavailable. It applies only before any model output exists, so a retry
	// cannot repeat a tool effect or spend an extra turn. The zero value uses
	// DefaultRetryPolicy.
	Retry RetryPolicy
	// System is the system prompt, in order.
	System []ai.SystemPromptBlock
	// SystemFn resolves the system prompt per request. When set it overrides
	// System, so a caller whose base prompt depends on live runtime state
	// (the active workspace, for example) does not have to rebuild the loop
	// to change it. The returned slice is used as-is; the loop never mutates
	// it.
	SystemFn func() []ai.SystemPromptBlock
	// RuntimeContext is runtime-authored state delivered as the request's
	// final message on every request: files the user referenced, the task
	// continuity briefing, a standing proposal decision.
	//
	// It is deliberately not a system block. Provider prompt caching keys on
	// the request prefix, and the system prompt precedes the whole
	// conversation, so a briefing that changes per request placed there
	// invalidates every cached token of history. The same briefing carried as
	// the last message keeps system, tools, and history byte-identical between
	// requests and confines the volatile bytes to the tail. Set it with
	// SetRuntimeContext before Run.
	RuntimeContext []string
	// AnswerRequest is the directive carried on the next request when an
	// answer-only request returned no visible text.
	//
	// It rides the request's runtime suffix, not the conversation: a directive
	// that says "no tool is available" must not become history that every later
	// request replays. Empty disables it while keeping the one retry.
	AnswerRequest string
	// ExecutionRecoveryRequest is the directive carried once when the model
	// announces an action but emits no tool call. It asks for the actual
	// canonical call; the runtime never executes the prose. Empty keeps the
	// classification but does not spend the recovery request.
	ExecutionRecoveryRequest string
	// ProtocolRecoveryRequest is the directive carried once when a serialized
	// tool-call envelope could not be normalized. It asks for a native or
	// correctly serialized call without inventing arguments. Empty keeps the
	// classification but does not spend the recovery request.
	ProtocolRecoveryRequest string
	// ToolArgumentRecoveryRequest is the directive carried once when a streamed
	// tool call closed with incomplete or malformed argument JSON. Nothing from
	// that call executed. The directive must ask for complete arguments and,
	// when the payload was truncated by the output limit, for a smaller or
	// split action; repeating the same oversized payload would truncate again.
	// Empty keeps the classification but does not spend a recovery request.
	ToolArgumentRecoveryRequest string
	// OutputRecoveryRequest is the directive carried once when a mid-run turn
	// contains only reasoning and produces neither a tool call nor visible
	// answer text. That turn cannot change the runtime state or answer the
	// user, so the run must not treat it as a clean stop. The directive asks
	// for a concrete call or answer without repeating the planning. Empty
	// returns the recoverable lifecycle stop immediately instead of spending
	// a request that would be identical.
	OutputRecoveryRequest string
	// StreamIdleTimeout bounds how long one provider stream may go without an
	// event before the run classifies it as a stalled transport. Zero uses
	// ai.DefaultStreamIdleTimeout. It is an inactivity bound, not a total
	// generation bound, so a long stream that keeps producing deltas is
	// unaffected.
	StreamIdleTimeout time.Duration
	// CompletionReviewRequest is the directive carried once when a candidate
	// answer fails the answer-level completion review. It must contain
	// {{reason}}; the loop replaces it with the runtime's evidence reason. The
	// correction may use an available tool to acquire the missing evidence or
	// qualify the claim. Empty disables correction even when AnswerReview is
	// installed, so a host cannot spend a turn on an absent instruction.
	CompletionReviewRequest string
	// RepairRequest is the claim-level correction directive. It may contain
	// {{count}}, {{budget}}, {{repairs}}, and {{reason}}. When non-empty and
	// AnswerRepairs returns a repairable claim, it replaces the prose
	// CompletionReviewRequest. Empty falls back to the prose directive.
	RepairRequest string
	// CorrectionAnswerRequest is the directive carried after a correction's
	// tool allowance is spent. The earlier candidate was withheld, not
	// delivered, so the model must write the complete answer instead of a
	// confirmation of an analysis the user never saw. Empty omits the
	// directive; the correction review still bounds the answer.
	CorrectionAnswerRequest string
	// AnswerRepairs returns the claim-level repairs for a rejected answer. It
	// must be a projection of the same audit AnswerReview runs; the loop only
	// renders it into the correction request. nil keeps the prose correction.
	AnswerRepairs func(turns []Turn, answer string) []contract.GroundingIssue
	// AnswerReview is the optional answer-level completion gate. It receives
	// the run so far and the candidate answer and returns a non-empty reason
	// when the answer cannot be published as complete. It can only request a
	// correction; authority, completion, and publication remain the runtime's.
	// A nil hook accepts every answer with no open plan.
	AnswerReview func(turns []Turn, answer string) string
	// AnswerPlanReview audits the claims a model declares before writing its
	// final answer. It is installed with AnswerReview for informational
	// contracts. A non-nil hook makes the control plane's answer boundary
	// require a passing plan before prose: the loop offers the plan tool alone,
	// reopens the evidence surface without the plan tool when a plan names
	// missing observations, and lets the ordinary publication review take over
	// after the bounded plan attempts are spent. It cannot publish an answer or
	// complete a task.
	AnswerPlanReview func(turns []Turn, plan AnswerPlan) []contract.GroundingIssue
	// AnswerPlanRequest is the directive carried on the answer-boundary
	// request that asks for the plan. Empty still requires a plan when
	// AnswerPlanReview is installed, but gives the model no instruction.
	AnswerPlanRequest string
	// AnswerPlanEvidenceRequest is the directive carried on the evidence
	// request that follows a refused plan. The plan tool is withheld on that
	// request; the directive asks for the observations the refusal named.
	AnswerPlanEvidenceRequest string
	// EvidenceGap returns the control plane's current evidence-frontier gap.
	// It is read when an answer is reviewed and again when it is finalized.
	// A non-empty value means the model answered before the task's required
	// evidence existed, so the answer is a premature candidate, not a result.
	EvidenceGap func() string
	// FinalizeAnswer is the last gate before a candidate answer is published.
	// It receives the accepted answer and the current evidence gap, and may
	// remove or qualify claims the evidence cannot support. The returned text
	// is the only text the host may commit for this run. It cannot authorize
	// an action or complete a task; completion still evaluates the finalized
	// text and the durable evidence.
	FinalizeAnswer func(turns []Turn, answer, evidenceGap string) FinalizedAnswer
	// CorrectionToolCalls bounds the tool execution the answer lifecycle may
	// spend on repairs. Zero uses DefaultCorrectionToolCalls. The bound is per
	// answer lifecycle, not per correction request: a second repair pass may
	// still answer, but it cannot reopen the investigation the tree budget
	// already paid for.
	CorrectionToolCalls int
	// Tools are offered to the model.
	Tools []Tool
	// MaxTokens caps each completion. Zero means "use the model's own output
	// ceiling": the loop must not substitute a flat budget, because a
	// reasoning model can spend a small one on hidden reasoning and stop
	// before it emits a single action. The provider resolves the ceiling.
	MaxTokens int
	// Reasoning enables extended thinking.
	Reasoning bool
	// ReasoningEffort is the effort level forwarded to the provider.
	ReasoningEffort string
	// ReasoningPolicy, when non-nil, selects reasoning for every provider
	// request. It overrides Reasoning and ReasoningEffort for that request
	// without changing the configured defaults. Nil uses them unchanged.
	ReasoningPolicy ReasoningPolicy
	// ToolChoice constrains tool use.
	ToolChoice ai.ToolChoice
	// SessionID is forwarded to providers that support session caching.
	SessionID string
	// Usage receives one cache sample per request. Nil disables the sample; the
	// request shape is still computed, because it is what enforces the
	// append-only conversation.
	Usage *UsageLog
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
	// OnProviderActivity reports provider attempts, retries, failures, and
	// recovery. It runs on the request goroutine and must not block.
	OnProviderActivity func(ProviderActivity)
	// OnTextDelta receives visible model text while a stream is still open,
	// in order. A UI publishes each delta so the answer reveals as it is
	// generated instead of landing as one block when the turn completes.
	// Nil disables it; transports that never token-stream never fire it.
	OnTextDelta func(delta string)
	// OnAssistantMessage receives each completed assistant message before
	// any tool call is dispatched. It is the truthful boundary at which a
	// host that streamed the deltas learns whether the text is prose around
	// a tool call (not an answer) or a candidate answer. A nil hook changes
	// nothing; the message is the same value the turn carries.
	OnAssistantMessage func(message ai.AssistantMessage)
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
	// ConclusionMode is the terminal-outcome vocabulary of the active
	// informational contract. It is handed to control tools through Context so
	// a general answer cannot be recorded as a defect verdict. Empty means the
	// host did not install a contract.
	ConclusionMode contract.ConclusionMode
	// Compaction configures context reduction. The zero value disables it.
	Compaction Compaction
	// Summarize replaces the compacted prefix during compaction. Required when
	// Compaction is enabled.
	Summarize SummarizeFunc
	// OnCompact reports each compaction, for logging and UI.
	OnCompact func(CompactResult)
	// OnCompactionMetrics reports structured compaction diagnostics. It is
	// separate from OnCompact so a host can publish metadata without parsing
	// the human-facing result.
	OnCompactionMetrics func(CompactionMetrics)
	// Recall is the bounded archive that lets a compacted context retrieve a
	// trimmed observation by id. Nil disables id references.
	Recall *RecallStore
	// CompactionProjection returns the authoritative durable state a
	// state-first compaction reconstructs from. It must be control-plane
	// data, never model output. Nil uses the semantic fallback.
	CompactionProjection func() (ContextProjection, bool)
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
	// correctionActive marks the one answer-correction window. Tool calls
	// dispatched while it is set consume correctionCalls instead of reopening
	// the whole investigation. Tool calls dispatch concurrently, so both are
	// atomic.
	correctionActive atomic.Bool
	correctionCalls  atomic.Int64
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
	// lastVerifiedTree is the workspace identity the journal captured at the
	// run's last successful check. It is compared with the run-end identity so
	// completion can prove nothing changed after the check.
	lastVerifiedTree string
	// runSeq issues a local run identity for mutation provenance when the host
	// has none. It is process-local and never a durable task identity.
	runSeq int
	// compactionAttempts bounds context rewrites in one run. It is reset at
	// Run start so a pathological loop cannot compact forever.
	compactionAttempts int
	// compactionCooldown skips the next trigger after a rewrite. The rebuilt
	// context is the smallest truthful one available; re-running compaction
	// before the provider sees it would summarise the summary.
	compactionCooldown int
	// frozen and prefix carry the previous request's message list and shape.
	// They enforce the append-only conversation and make cache locality
	// observable; both survive across Run calls, because the provider cache
	// spans a session, not a turn.
	frozen []ai.Message
	prefix prefixState
	// responseMu guards the one-response transaction audit. The loop is
	// reused across user turns; Run resets it so a previous commit cannot make
	// the next turn look already published.
	responseMu sync.Mutex
	response   ResponseAudit
}

// SetUsageLog installs the ledger that receives per-request cache samples.
//
// It is separate from the turn ledger the host folds after Run, because cache
// locality is a property of each request inside a run, not of its totals. Pass
// nil to stop reporting.
func (l *Loop) SetUsageLog(log *UsageLog) {
	l.cfg.Usage = log
}

// observationRecord is one current read-only observation. Turn keeps a
// same-batch duplicate from being answered from a sibling's result: only an
// observation from an earlier turn is reusable.
type observationRecord struct {
	turn int
	// text is the recorded payload, not a summary of it. A reuse must be a
	// replay of the evidence: a notice that the observation is already current
	// without the data it reported is unusable, and a model that receives one
	// has to repeat the call to get the payload back.
	text string
	// cut reports that text was bounded at observationReplayBytes. The replay
	// states the cut instead of presenting a partial payload as the whole.
	cut bool
}

// observationReplayBytes bounds the payload retained for reuse. Tool results
// are already bounded per call; this cap only stops one unusually large
// observation (a long read window) from being held for the rest of the run.
const observationReplayBytes = 32 << 10

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

// markVerification captures the workspace identity after a successful check.
//
// It runs after the whole batch, so a later executing outcome in the same batch
// would make the capture ambiguous; in that case the identity is left unset and
// completion keeps the conservative rule. The capture is best-effort: a journal
// without VerificationMarker simply provides none.
func (l *Loop) markVerification(ctx context.Context, edit *runEdit, outcomes []ToolOutcome) {
	if edit == nil {
		return
	}
	if l.cfg.Grounding == nil || !l.cfg.Grounding.MutationIntent {
		// Only a repository-change objective needs the identity; a read-only
		// run must not pay a workspace capture for a check it will never owe.
		return
	}
	marker, ok := edit.journal.(VerificationMarker)
	if !ok {
		return
	}
	lastCheck := -1
	for index, outcome := range outcomes {
		if outcome.Result.Verifies && !outcome.Result.IsError {
			lastCheck = index
		}
	}
	if lastCheck < 0 {
		return
	}
	for _, outcome := range outcomes[lastCheck+1:] {
		if outcome.Result.Executes && !outcome.ReadOnly() {
			// A process ran after the check in the same batch. The capture
			// cannot tell what it changed, so it cannot stand for the verified
			// state.
			return
		}
	}
	identity, err := marker.MarkVerification(ctx)
	if err != nil || identity == "" {
		return
	}
	l.mutationMu.Lock()
	l.lastVerifiedTree = identity
	l.mutationMu.Unlock()
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

// VerifiedTree returns the workspace identity captured at the run's last
// successful check, or "" when the journal could not provide one.
func (l *Loop) VerifiedTree() string {
	if l == nil {
		return ""
	}
	l.mutationMu.Lock()
	defer l.mutationMu.Unlock()
	return l.lastVerifiedTree
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
	if cfg.MaxTokens < 0 {
		cfg.MaxTokens = 0
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = DefaultMaxTurns
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = ai.DefaultStreamIdleTimeout
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
	if cfg.Compaction.Enabled && cfg.Compaction.Strategy != StrategyOff &&
		cfg.Summarize == nil && cfg.CompactionProjection == nil {
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
	if !ok || prior.text == "" || prior.turn >= turnIndex {
		return "", false
	}
	var replay strings.Builder
	replay.WriteString("This observation is unchanged since your earlier call in this run. Its payload is replayed below; do not call the tool again for it.\n\n")
	replay.WriteString(prior.text)
	if prior.cut {
		replay.WriteString("\n\n[replay bounded at 32 KiB; the omitted tail is the only part a repeat would add]")
	}
	return replay.String(), true
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
	text, cut := boundObservationText(result.Text)
	if text == "" {
		text = call.Name + " returned an empty observation"
	}
	l.observations[CallSignature(call.Name, call.Arguments)] = observationRecord{
		turn: turnIndex, text: text, cut: cut,
	}
}

// boundObservationText keeps a recorded payload consumable on replay while
// bounding what one observation may retain for the run.
func boundObservationText(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if len(text) <= observationReplayBytes {
		return text, false
	}
	cut := observationReplayBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut]), true
}

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

// SetAssistantMessageHook installs the completed-stream reporter after
// construction.
//
// It must be called before Run, never during one. The hook runs on the
// request goroutine after the stream has been drained and before the turn is
// classified, so a host can discard live text that belonged to a tool round
// without waiting for the tool's result.
func (l *Loop) SetAssistantMessageHook(hook func(message ai.AssistantMessage)) {
	l.cfg.OnAssistantMessage = hook
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

// SetConclusionMode installs the terminal-outcome vocabulary for this turn.
// The zero value clears it, which preserves unrestricted legacy behaviour.
func (l *Loop) SetConclusionMode(mode contract.ConclusionMode) {
	if l != nil {
		l.cfg.ConclusionMode = mode
	}
}

// SetAnswerReview installs the bounded answer-level completion gate for the
// next run, or nil to accept every answer with no open plan. The gate can ask
// for bounded,
// progress-gated corrected answers; it cannot authorize an action, widen the
// tool set beyond the loop's constructed capabilities, or mark the run
// complete.
func (l *Loop) SetAnswerReview(review func(turns []Turn, answer string) string) {
	l.cfg.AnswerReview = review
}

// SetEvidenceGap installs the current evidence-frontier gap for the next run.
//
// The host projects this from the same evidence state the completion evaluator
// uses; it is not model output. Passing nil clears it.
func (l *Loop) SetEvidenceGap(gap func() string) {
	if l != nil {
		l.cfg.EvidenceGap = gap
	}
}

// SetAnswerFinalizer installs the last gate before a candidate answer is
// published. Passing nil accepts the sanitized answer unchanged.
func (l *Loop) SetAnswerFinalizer(finalize func(turns []Turn, answer, evidenceGap string) FinalizedAnswer) {
	if l != nil {
		l.cfg.FinalizeAnswer = finalize
	}
}

// SetAnswerRepairs installs the claim-level repair planner for the next run,
// or nil to keep the prose correction. The planner is a projection of the
// review's own audit; it cannot authorize an action or accept a claim.
func (l *Loop) SetAnswerRepairs(repairs func(turns []Turn, answer string) []contract.GroundingIssue) {
	l.cfg.AnswerRepairs = repairs
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

// SetProviderActivityHook installs the transport-state reporter after
// construction, or nil to silence it. It must be set before Run.
func (l *Loop) SetProviderActivityHook(hook func(ProviderActivity)) {
	l.cfg.OnProviderActivity = hook
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

// SetCompactionProjection installs the control plane's durable-state source
// after construction, or nil to use the semantic fallback. It is a view of
// authoritative state: the loop validates it and retains its previous context
// when the projection is malformed.
func (l *Loop) SetCompactionProjection(source func() (ContextProjection, bool)) {
	l.cfg.CompactionProjection = source
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
	l.lastVerifiedTree = ""
	l.mutationMu.Unlock()
	l.compactionAttempts = 0
	l.compactionCooldown = 0
	l.beginResponse()
	defer func() { l.abortResponse(runErr) }()

	edit := &runEdit{journal: l.cfg.Journal}
	defer func() {
		if edit.txn == nil {
			return
		}
		commitErr := edit.txn.Commit(context.WithoutCancel(ctx))
		if provenance, ok := edit.txn.Mutation(); ok {
			provenance.RunID = runID
			provenance.VerifiedTree = l.VerifiedTree()
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
	// reasoningRetried bounds the recovery for a mid-run turn that produced
	// only hidden reasoning. The turn cannot act or answer, so one directive
	// asks for a concrete next output; a second reasoning-only turn is a
	// recoverable lifecycle stop, not a deliberate finish.
	reasoningRetried := false
	// outputLimitRetried bounds the recovery for a turn the provider cut off
	// at the completion output ceiling before any call or visible text. A
	// retry at the same budget truncates identically, so the recovery raises
	// the budget to the model ceiling once; a second truncation is reported.
	outputLimitRetried := false
	// completionBudget is the per-request output ceiling. It starts at the
	// configured cap and is raised once when evidence proves that cap cannot
	// produce an action for this model. It stays raised for the run because
	// the same reasoning scale governs every later turn; the run's turn and
	// wall-clock budgets remain the outer bound.
	completionBudget := l.cfg.MaxTokens
	// answerCorrections and correctionPending implement the bounded
	// completion-review correction. The next request reopens the constructed
	// tool surface so the model can acquire the missing evidence instead of
	// rewriting prose. A correction is only spent on a new claim set: the same
	// rejected claims terminate at review, so a model that repeats itself
	// cannot turn the correction into a retry loop.
	answerCorrections := 0
	lastRepairKey := ""
	correctionPending := false
	// planPassed records that the pre-prose claim audit accepted a plan for
	// this run. planAttempts bounds refused plans so a model that cannot
	// converge returns to the ordinary publication review instead of looping.
	// planGaps is the last refusal; it stays set until a successful observation
	// so a prose-only turn cannot return to the plan surface with the same
	// claims. The evidence request withholds the plan tool.
	planPassed := false
	planAttempts := 0
	var planGaps []contract.GroundingIssue
	// planProseAttempts bounds how many text-only turns may be withheld for
	// lacking the required plan. After the bound, the publication review
	// decides; the plan gate is a preflight, not an unconditional trap.
	planProseAttempts := 0
	// correctionDirective is the claim-level repair instruction for the active
	// correction window. It is re-appended to every request while the window
	// is open: the window can span several model turns, and a later turn that
	// no longer sees the named claims follows an unrelated frontier instead.
	correctionDirective := ""
	// executionRecoveryAttempted and normalizationRecoveryAttempted are the
	// bounded protocol recoveries. Each is spent at most once per run and the
	// run/turn budget still bounds the whole loop, so neither can become an
	// infinite retry. argumentRecoveryAttempted is the same bound for a
	// streamed tool call whose argument JSON never completed: the call was not
	// executed, so one reduced re-emission is the only safe recovery.
	executionRecoveryAttempted := false
	normalizationRecoveryAttempted := false
	argumentRecoveryAttempted := false
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
	// lastEvidenceReview is the projection's frontier verdict for the request
	// that produced the current candidate. It is the control plane's
	// statement that the answer may be premature, not a model judgement.
	lastEvidenceReview := ""
	turns = nil
	// ledger is the per-run claim-preserving draft history. A review failure
	// scopes to claims, and a revision that drops an independently supported
	// claim cannot displace the stronger draft.
	ledger := answerLedger{}
	// nudges are the one-shot runtime directives for the next request only:
	// the answer request, the execution-intent recovery, and the protocol
	// recovery. They are not conversation content — a stale "no tool is
	// available" replayed on a later request is both wrong and a duplicate —
	// so they ride the runtime suffix and are cleared once that request is
	// sent.
	var nudges []string

	// reportTurn publishes the completed turn after the answer gate has
	// classified it. A host sees AnswerNarration, AnswerSuperseded,
	// AnswerRejected, or AnswerCommitted and never has to infer the decision
	// from event order.
	reportedTurn := -1
	reportTurn := func() {
		if l.cfg.OnTurn != nil && len(turns) > 0 {
			l.cfg.OnTurn(turns[len(turns)-1])
		}
		if len(turns) > 0 {
			reportedTurn = len(turns) - 1
		}
	}
	// acceptAnswer is the one publication gate for an assistant candidate. It
	// returns true when the run must end and false when it issued a correction
	// request.
	//
	// Every path that can end a run with a publishable answer goes through
	// here. A text turn that skipped this gate used to be published by the host
	// even though no response transaction committed it and no claim audit
	// approved it, which is how a candidate could be displayed and then
	// rejected by the completion verdict. Prose that accompanied tool calls is
	// narration, not a candidate: it stays in the turn for completion's veto
	// but never opens a transaction.
	acceptAnswer := func() bool {
		if len(turns) == 0 {
			return true
		}
		last := &turns[len(turns)-1]
		text := last.Assistant.Text()
		if last.Answer != AnswerNarration || l.cfg.AnswerReview != nil {
			l.noteDraft()
		}
		// withhold keeps the candidate out of every user-visible answer
		// projection. Tool results in the same turn are real work: hiding
		// them would erase calls the user watched run, so only the prose is
		// withdrawn.
		withhold := func(review string) {
			if len(last.Results) > 0 {
				last.Answer = AnswerNarration
				last.AnswerReview = ""
				return
			}
			last.Answer = AnswerRejected
			last.AnswerReview = review
		}
		// supersede marks an intermediate candidate that a correction
		// replaces. It must not become a user-visible answer artifact. The
		// model still sees the text in the live conversation, so it can repair
		// the named claims. Tool outcomes remain visible.
		supersede := func() {
			last.Answer = AnswerSuperseded
			last.AnswerReview = ""
		}
		finish := func() {
			l.recordDraftLineage(ledger.summaries(), ledger.preservedCount(), ledger.removedCount())
			// A correction can reclassify the same turn after a continuation
			// path already reported it; the final disposition is published
			// exactly once.
			if len(turns)-1 != reportedTurn {
				reportTurn()
			}
		}
		// commitSalvage publishes the strongest draft with only the refused
		// claims removed. It still crosses the finalizer, so a fallback repair
		// cannot publish text the same publication gate would refuse.
		commitSalvage := func(salvagedText string, salvaged AnswerDraft, reviewFailed bool) bool {
			adoptAnswerText(turns, &live, salvagedText)
			if !l.finalizeResponse(turns, &live, lastEvidenceReview) {
				return false
			}
			if reviewFailed {
				l.noteRejected()
			}
			last = &turns[len(turns)-1]
			// The fallback composes a new semantic version from the strongest
			// draft: it supersedes the latest revision, not the draft it was
			// recovered from.
			finalText := last.Assistant.Text()
			composed := newAnswerDraft(ledger.newVersion(), ledger.last.Version, finalText, nil)
			// Preserve the removed-claim states: the audit must be able to say
			// that the committed answer dropped an unsupported assertion rather
			// than silently looking as if it never existed.
			composed.Claims = append([]AnswerClaim(nil), salvaged.Claims...)
			committed := ledger.commit(composed)
			last.Draft = &committed
			last.Answer = AnswerCommitted
			last.AnswerReview = ""
			finish()
			return true
		}
		if strings.TrimSpace(text) == "" {
			finish()
			return true
		}

		// The control plane reached its answer boundary and the claim
		// preflight has not passed. Withhold this prose as narration and ask
		// for the plan; nothing user-visible was streamed, so the turn costs a
		// request rather than a published draft and a correction. The bound
		// keeps a model that never plans from trapping the run: after one
		// withheld prose turn the ordinary publication review decides.
		if l.cfg.AnswerPlanReview != nil && !planPassed &&
			planAttempts < MaxAnswerPlanAttempts && answerCorrections == 0 &&
			toolNamed(l.cfg.Tools, AnswerPlanToolName) != nil && planProseAttempts < 1 {
			last.Answer = AnswerNarration
			last.AnswerReview = ""
			planProseAttempts++
			if directive := strings.TrimSpace(l.cfg.AnswerPlanRequest); directive != "" {
				nudges = append(nudges, directive)
			}
			finish()
			return false
		}

		reason := l.answerReviewReason(turns)
		repairs := l.answerRepairs(turns)
		if strings.Contains(reason, withheldDraftReviewReason) {
			// The refusal is about the answer's publication boundary, not its
			// claims. Reusing the first review's claim key would suppress the
			// correction as a repeated failure and leave the withheld draft
			// unpublished; the answer must be regenerated, not re-audited.
			repairs = nil
		}

		// Track only candidates that can carry claims. A prose-only rejection
		// (a plan or a missing evidence boundary) has no claim list and cannot
		// be repaired sentence by sentence.
		var draft AnswerDraft
		haveDraft := false
		if reason == "" || len(repairs) > 0 {
			draft = ledger.observe(text, repairs)
			draft.State = AnswerDraftUnderReview
			last.Draft = &draft
			haveDraft = true
		}

		if reason != "" {
			key := answerRepairKey(repairs, reason)
			// One correction cannot repair a claim the repair itself
			// introduced. A second pass is admitted only when the review
			// names a different claim set; repeating the same rejection
			// terminates at review instead of spinning. Claim-level repairs
			// are the only ones that preserve valid work; a prose-only
			// rejection keeps the generic bounded correction.
			if answerCorrections < MaxAnswerCorrections && key != lastRepairKey &&
				len(turns) < l.cfg.MaxTurns {
				if directive := l.correctionDirective(turns, reason, ledger.preserved()); directive != "" {
					if haveDraft {
						draft.State = AnswerDraftSuperseded
						ledger.supersede(draft.Version)
						last.Draft = &draft
					}
					supersede()
					l.noteRejected()
					if answerCorrections == 0 {
						// The tool allowance belongs to the answer
						// lifecycle, not to each repair: a second pass must
						// not double the investigation budget.
						l.correctionCalls.Store(0)
					}
					answerCorrections++
					lastRepairKey = key
					correctionPending = true
					correctionDirective = directive
					l.correctionActive.Store(true)
					finish()
					return false
				}
			}
			// The correction allowance is spent, the claim set did not
			// change, or the host authorized no correction. Before refusing
			// the whole answer, remove only the claims the review refused.
			// Independently supported claims and their structure survive.
			if haveDraft {
				if salvagedText, salvaged, ok := ledger.salvage(); ok &&
					l.answerPassesReview(turns, salvagedText) &&
					commitSalvage(salvagedText, salvaged, true) {
					return true
				}
			}
			// A factual claim the same review refuses is not a final answer:
			// keep it for completion's claim list, but never publish it. A
			// facet gap is different: the answer may honestly state the
			// missing deliverable, and the finalizer's Approved gate decides
			// whether the text is publishable while completion carries the
			// gap.
			if blocksPublication(repairs) {
				if haveDraft {
					ledger.reject(draft.Version)
					draft.State = AnswerDraftRejectedTerminal
					last.Draft = &draft
				}
				withhold(answerRejectionReason(reason, repairs))
				l.noteRejected()
				l.markResponseReview(repairs)
				finish()
				return true
			}
		}
		if last.Answer == AnswerNarration {
			// Prose that accompanied tool calls or only announced a future
			// action is narration. It stays in the completion input for its
			// veto, but it is not a publishable answer and must not open a
			// response transaction.
			finish()
			return true
		}
		// A revision may pass review but be materially poorer than the
		// strongest draft it replaced. Prefer the strong draft with only its
		// unsupported claims removed over a rewrite that discarded valid
		// cognitive work.
		if haveDraft && draft.PreservationLost {
			if salvagedText, salvaged, ok := ledger.salvage(); ok &&
				l.answerPassesReview(turns, salvagedText) &&
				commitSalvage(salvagedText, salvaged, false) {
				return true
			}
		}
		if !l.finalizeResponse(turns, &live, lastEvidenceReview) {
			// The finalizer's own audit is authoritative. It refused the
			// candidate, so the host must not publish it. The raw turn stays
			// available to completion, which reports the exact claims.
			if haveDraft {
				ledger.reject(draft.Version)
				draft.State = AnswerDraftRejectedTerminal
				last.Draft = &draft
			}
			audit := l.LastResponseAudit().Answer
			withhold(answerRejectionReason("", audit.Issues))
			l.noteRejected()
			if l.responseState() != ResponseReview {
				l.markResponseReview(l.answerRepairs(turns))
			}
			finish()
			return true
		}
		last.Answer = AnswerCommitted
		last.AnswerReview = ""
		if haveDraft {
			if finalText := last.Assistant.Text(); finalText != draft.Text {
				draft = newAnswerDraft(draft.Version, draft.Supersedes, finalText, nil)
			}
			committed := ledger.commit(draft)
			last.Draft = &committed
		}
		finish()
		return true
	}

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

		compacted, compactErr := l.compactIfNeeded(ctx, &live)
		if compactErr != nil {
			return turns, compactErr
		}
		if compacted {
			// Compaction rewrites the conversation, so the per-message history
			// provenance no longer describes it. The reconstructed context is
			// history by definition. The append-only baseline is dropped with
			// it: compaction is the one deliberate prefix break, and keeping
			// the old bytes would undo it.
			origins = historyOrigins(len(live))
			l.forgetFrozenHistory()
			continue
		}

		// The projection is a view of this request, never a mutation of the
		// live conversation. The loop keeps the exact transcript and the full
		// tool set; only what reaches the provider is narrowed. This is what
		// makes an elided tool result recoverable: the evidence state explains
		// it and a later turn can still re-read the source.
		requestMessages := live
		requestSystem := l.system()
		lastEvidenceReview = ""
		// runtimeBlocks is the volatile tail of this request: caller-supplied
		// per-turn state first, then the projection's briefing, then any
		// one-shot directive, so the directive is the last thing read. It is
		// rebuilt per request and never stored, so the conversation stays
		// append-only.
		runtimeBlocks := make([]string, 0, len(l.cfg.RuntimeContext)+len(nudges)+1)
		runtimeBlocks = append(runtimeBlocks, l.cfg.RuntimeContext...)
		runtimeBlocks = append(runtimeBlocks, nudges...)
		activeTools := tools
		// planEvidenceSurface is the pre-closure action surface for a plan
		// refusal. It is captured before the control plane withdraws tools at
		// its answer boundary, so a refused plan can reopen exactly the reads
		// the projection considered available rather than the full built set.
		var planEvidenceSurface []Tool
		surface := actionSurface{}
		answerEligible := false
		objectiveComplete := false
		terminal := false
		closed := false
		refusals := map[string]string(nil)
		var projectedToolChoice ai.ToolChoice
		// forcePlanChoice pins the next request to the plan tool. It is set
		// only when the control plane has reached its answer boundary and a
		// plan has not passed yet. forceEvidenceChoice requires an evidence
		// call on the request that follows a refused plan; a projector that
		// forbade tools at the answer boundary must not turn the re-opened
		// surface back into prose.
		forcePlanChoice := false
		forceEvidenceChoice := false
		// redundant names offered capabilities the control plane judged unable
		// to close the current gap. They stay offered so the request's tool
		// block, and therefore its cached prefix, survives; the loop still
		// refuses to treat a call to one as evidence.
		var redundant map[string]bool
		if l.cfg.Project != nil {
			// The correction window spans every request until the model
			// answers, not just the first one: a repair may need a read, a
			// second read, and then the corrected text.
			correcting := correctionPending || l.correctionActive.Load()
			projected := l.cfg.Project(ProjectionRequest{
				Messages:         copyMessages(live),
				Turns:            append([]Turn(nil), turns...),
				Turn:             len(turns),
				Tools:            tools,
				System:           requestSystem,
				ForceAnswer:      forceAnswer && !correcting,
				Correcting:       correcting,
				CorrectionBudget: l.remainingCorrectionCalls(),
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
			runtimeBlocks = append(runtimeBlocks, projected.Runtime...)
			report := projected.Report
			for _, entry := range report.Redundant {
				if redundant == nil {
					redundant = make(map[string]bool, len(report.Redundant))
				}
				redundant[entry.Tool] = true
			}
			answerEligible = report.AnswerEligible
			objectiveComplete = report.ObjectiveComplete
			terminal = report.Terminal
			stopReason = report.StopReason
			refusals = projected.Refusals
			lastEvidenceReview = report.EvidenceReview()
			if projected.ToolChoice.Type != "" || projected.ToolChoice.ToolName != "" {
				projectedToolChoice = projected.ToolChoice
			}
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
			// The projection's correction surface, before the answer-boundary
			// closure below clears it. A correction request may use these
			// tools to gather the evidence the review named.
			correctionSurface := append([]Tool(nil), activeTools...)
			// A refused plan reopens the evidence surface without plan
			// bookkeeping. Offering the plan tool again lets the model repeat
			// the same unsupported claims; offering task-plan bookkeeping lets
			// it satisfy a forced tool call without observing the subject. The
			// plan tools return after an observation.
			planEvidenceSurface = withoutPlanTools(activeTools)
			// AnswerEligible and Terminal are the control plane's semantic
			// closure, not hints. The loop owns the request shape, so it must
			// withdraw every tool rather than trusting a projector to also
			// return an empty slice. Otherwise a model could execute a call on
			// a surface the runtime already declared closed.
			//
			// A plan tool is the exception at an answer boundary: it records
			// the model's own guidance rather than evidence, and withdrawing it
			// left an open plan frozen after the answer. Keeping only a plan
			// tool with open items lets the model reconcile it; once the plan
			// is closed, the surface is empty again. A terminal closure still
			// withdraws everything: the task cannot progress.
			if answerEligible || terminal {
				if terminal {
					activeTools = []Tool{}
				} else {
					activeTools = OpenPlanTools(activeTools)
				}
			}
			closed = terminal || len(activeTools) == 0
			// The previous answer failed the answer-level review. Reopen the
			// surface the projection described for this correction request:
			// the missing evidence may be one read away, while the projection
			// closed the surface from its own evidence model. Without a
			// projector, the constructed set is the only available surface.
			// This never widens authority; every call still crosses grounding
			// and the security gate, and a terminal capability/stall stop is
			// not overridden.
			// A closed window is not an empty projected surface to recover from:
			// when the allowance is spent, the projector has deliberately
			// withdrawn every tool. Restoring the constructed set here would
			// turn the "repair window is closed" notice into a lie and let a
			// stale tool call execute after the lifecycle refused its budget.
			if correcting && !terminal && l.remainingCorrectionCalls() > 0 {
				if len(correctionSurface) > 0 {
					activeTools = correctionSurface
				} else {
					activeTools = tools
				}
				closed = false
				answerEligible = false
				forceAnswer = false
			}
			// ForceAnswer is loop policy, not a projector hint. Withdraw the
			// surface even when the projector does not: the model already
			// spent two turns on proposals the runtime refused, so another
			// call can only be refused again. This runs before the audit
			// fields are filled so the report matches the request.
			if forceAnswer {
				activeTools = []Tool{}
				closed = true
			}
			// Pre-prose evidence plan. The control plane has reached its
			// answer boundary; require the model to declare the claims it
			// intends to make before any answer prose can stream. A refused
			// plan reopens the evidence surface for exactly one request so the
			// missing observation can be read. The ordinary publication review
			// still decides what may be published.
			if l.cfg.AnswerPlanReview != nil && !correcting && !terminal {
				planTool := toolNamed(tools, AnswerPlanToolName)
				switch {
				case len(planGaps) > 0 && planTool != nil:
					activeTools = planEvidenceSurface
					if len(activeTools) == 0 {
						activeTools = withoutPlanTools(tools)
					}
					closed = false
					answerEligible = false
					forceAnswer = false
					forceEvidenceChoice = true
					forcePlanChoice = false
					report.AnswerEligible = false
					report.Reason = joinReason(report.Reason,
						"answer plan refused; evidence surface reopened for the missing observations")
					if directive := strings.TrimSpace(l.cfg.AnswerPlanEvidenceRequest); directive != "" {
						runtimeBlocks = append(runtimeBlocks, directive)
					}
					// planGaps stays set until a successful observation. A
					// prose-only turn or a repeated plan must not turn this
					// back into the plan-only surface with the same claims.
				case answerEligible && planTool != nil && !planPassed && planAttempts < MaxAnswerPlanAttempts:
					// A reconciled task plan stays available beside the
					// preflight: forcing the plan tool while a todo is open
					// would make the answer review refuse an open item the
					// model was not allowed to close.
					openPlans := OpenPlanTools(tools)
					activeTools = append([]Tool{planTool}, openPlans...)
					closed = false
					forceAnswer = false
					forcePlanChoice = len(openPlans) == 0
					report.AnswerEligible = false
					report.Reason = joinReason(report.Reason, "answer plan required before publication")
					if directive := strings.TrimSpace(l.cfg.AnswerPlanRequest); directive != "" {
						runtimeBlocks = append(runtimeBlocks, directive)
					}
				}
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
			// The rejection path needs the same surface the projection just
			// described. Passing it forward is what turns "tool not offered"
			// into a recoverable instruction naming the stage, the control
			// reason, and the actions that are still available.
			surface.stage = report.Stage
			if len(report.Excluded) > 0 {
				surface.withheld = make(map[string]string, len(report.Excluded))
				for _, entry := range report.Excluded {
					if entry.Tool != "" && entry.Reason != "" {
						surface.withheld[entry.Tool] = entry.Reason
					}
				}
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
		// The repair claims are the correction window's objective, not a
		// one-request hint. A correction can need several model turns (read,
		// read, answer), and a later turn that no longer sees the named claims
		// follows whatever unrelated coordinate the state block surfaces. While
		// the allowance can still buy evidence, the directive names the
		// claims. Once it is spent, the answer itself is the only repair: state
		// that the earlier candidate was withheld so the model writes the
		// complete answer instead of confirming a draft the user never saw.
		if l.correctionActive.Load() {
			if l.remainingCorrectionCalls() > 0 {
				if correctionDirective != "" {
					runtimeBlocks = append(runtimeBlocks, correctionDirective)
				}
			} else if directive := strings.TrimSpace(l.cfg.CorrectionAnswerRequest); directive != "" {
				runtimeBlocks = append(runtimeBlocks, directive)
			}
		}
		// The reopened surface applies to this correction request only. A
		// second rejected answer is returned to the host for completion
		// evaluation rather than spending another turn.
		correctionPending = false

		activeByName := make(map[string]Tool, len(activeTools))
		for _, tool := range activeTools {
			activeByName[tool.Name()] = tool
		}
		specs := Specs(activeTools)

		converted, requestOrigins, err := l.convert(requestMessages, origins)
		if err != nil {
			return turns, fmt.Errorf("agent: convert context: %w", err)
		}
		// The conversation is append-only from here on. A projection may elide
		// a tool result, but the form a result was first sent in is the form
		// the provider cached; rewriting it later would invalidate every cached
		// token after it to save a few hundred bytes.
		shape := l.freezePrefix(converted, requestSystem, specs)
		// Per-turn state rides at the very end, after every cached byte. The
		// slice is copied because append would otherwise write into the live
		// conversation's spare capacity.
		if text := joinRuntime(runtimeBlocks); text != "" {
			converted = append(append([]ai.Message(nil), converted...), ai.RuntimeStateMessage{Text: text})
			requestOrigins = append(requestOrigins, ai.ProvenanceRuntimeState)
		}
		// The canonical contract is checked before any transport sees the
		// conversation. A malformed message is an internal construction
		// failure: sending it would ask a provider to diagnose runtime state.
		if err := ai.ValidateMessages(converted, requestOrigins); err != nil {
			return turns, fmt.Errorf("%w: %w", ErrInvalidProviderContext, err)
		}

		toolChoice := l.cfg.ToolChoice
		if l.cfg.GetToolChoice != nil {
			toolChoice = l.cfg.GetToolChoice()
		}
		if projectedToolChoice.Type != "" || projectedToolChoice.ToolName != "" {
			toolChoice = projectedToolChoice
		}
		forcedPlanTool := forcePlanChoice && toolNamed(activeTools, AnswerPlanToolName) != nil
		if forcedPlanTool {
			// The evidence phase is complete; the next action is the claim
			// preflight, not another probe and not prose. The plan tool is the
			// only offered capability, so a provider that ignores the choice
			// still cannot run an unrelated call.
			toolChoice = ai.ToolChoice{Type: "tool", ToolName: AnswerPlanToolName}
		} else if forceEvidenceChoice && len(activeTools) > 0 {
			// A refused plan owes evidence. The projection may have closed the
			// answer boundary with a prose choice; the reopened surface must
			// produce an observation before the plan returns.
			toolChoice = ai.ToolChoiceAny
		}
		if toolChoice.Type == "tool" && toolChoice.ToolName != "" &&
			toolNamed(activeTools, toolChoice.ToolName) == nil {
			// A projector may pin a capability the loop's final surface
			// withdrew. Never force a tool that is not offered; the request
			// would ask the provider to call an unavailable capability.
			toolChoice = ai.ToolChoice{}
		}

		if err := ctx.Err(); err != nil {
			return turns, err
		}
		// The policy sees the budget before this request is charged, so a
		// remaining count of one means this is the last call the run can make.
		// It also sees the measured request shape, including the tool and task
		// surface the provider will bill, not just the conversation.
		reasoningEnabled, reasoningEffort := l.cfg.Reasoning, l.cfg.ReasoningEffort
		if l.cfg.ReasoningPolicy != nil {
			reasoningEnabled, reasoningEffort = l.cfg.ReasoningPolicy(l.RequestFacts(requestMessages))
		}
		// A model call is admitted only after every local preparation step has
		// succeeded. Turn and model-call are charged together, atomically: a
		// refused model call must not silently spend a turn, and a child that
		// shares this grant cannot observe either counter mid-admission.
		if err := l.budget.AdmitAll(budget.DimensionTurns, budget.DimensionModelCalls); err != nil {
			return turns, turnBudgetError(err)
		}

		// The request owns a cancellable context so the idle watchdog can abort
		// a transport that has gone silent. Without the cancel, a stalled HTTP
		// response would keep its goroutine alive after the run stopped waiting.
		requestCtx, requestCancel := context.WithCancel(ctx)
		stream, err := l.openStream(requestCtx, ai.Request{
			Model:           l.model(),
			Messages:        converted,
			System:          requestSystem,
			Tools:           specs,
			ToolChoice:      toolChoice,
			MaxTokens:       completionBudget,
			Reasoning:       reasoningEnabled,
			ReasoningEffort: reasoningEffort,
			SessionID:       l.cfg.SessionID,
		})
		if err != nil {
			requestCancel()
			return turns, fmt.Errorf("%w: %w", ErrProviderRequest, err)
		}

		assistant, err := l.drain(requestCtx, stream, requestCancel)
		requestCancel()
		if err != nil {
			// A tool call whose arguments never completed is not an executed
			// action and not a failed task. Nothing from that turn was
			// dispatched, so one bounded re-emission with complete arguments
			// is safe; a truncated payload is asked to be split rather than
			// repeated. If the recovery is unavailable or already spent, the
			// run stops as a recoverable protocol failure so the task remains
			// resumable and earlier mutations stay authoritative.
			if argumentErr := asToolArgumentError(err); argumentErr != nil {
				if !argumentRecoveryAttempted && len(activeTools) > 0 &&
					strings.TrimSpace(l.cfg.ToolArgumentRecoveryRequest) != "" &&
					len(turns) < l.cfg.MaxTurns {
					argumentRecoveryAttempted = true
					// A payload cut off by the output ceiling will truncate
					// again at the same budget. Raise the ceiling once so the
					// re-emission has room; a malformed (not truncated)
					// payload keeps the configured budget.
					if argumentErr.OutputLimit {
						completionBudget = l.escalateCompletionBudget(completionBudget)
					}
					nudges = append(nudges, l.cfg.ToolArgumentRecoveryRequest)
					continue
				}
				return turns, fmt.Errorf("%w: %w", ErrToolCallIncomplete, argumentErr)
			}
			if errors.Is(err, ErrProviderStalled) {
				return turns, err
			}
			return turns, fmt.Errorf("%w: %w", ErrProviderStream, err)
		}
		// The provider's own context count is the floor for the next
		// compaction decision.
		l.lastProviderTokens = assistant.Usage.Total
		l.observeRequest(len(turns), shape, assistant.Usage)
		// The directives belonged to the request that was just sent.
		nudges = nil
		// The stream is closed, so a host that published live deltas can now
		// decide whether they were prose around a tool call or a candidate
		// answer. This runs before dispatch: a tool's start must not be the
		// event that first reveals the turn was not an answer.
		if l.cfg.OnAssistantMessage != nil {
			l.cfg.OnAssistantMessage(assistant)
		}

		turn := Turn{Assistant: assistant}
		live = append(live, AssistantMessage{Message: assistant})
		origins = append(origins, ai.ProvenanceAssistant)

		calls := assistant.ToolCalls()
		if len(calls) > 0 || strings.TrimSpace(assistant.Text()) != "" {
			activityObserved = true
		}
		// Prose that is followed by tool calls is not a conclusion. The call
		// remains visible and its result is evidence; the prose would read as
		// an answer the run later replaces.
		if len(calls) > 0 && strings.TrimSpace(assistant.Text()) != "" {
			turn.Answer = AnswerNarration
		}
		if len(calls) > 0 {
			// A canonical call is the correction both protocol recoveries ask
			// for. It may still be refused by authority or schema, but it is
			// no longer a missing or malformed representation.
			normalizationPending = false
			executionIntentPending = false
		}
		if len(calls) == 0 {
			turn.Assistant = SanitizeAssistantMessage(turn.Assistant)
			// A message made only of "let me check…" clauses is a plan, not a
			// result. It stays in the turn so completion can veto it, but it is
			// never published or persisted as the answer: exposing internal
			// exploration narration as final prose is the failure this
			// suppression closes.
			if answerIsDeferredPlan(turn.Assistant.Text()) {
				turn.Answer = AnswerNarration
			}
			turns = append(turns, turn)
			// A terminal surface with neither a proposal nor an answer is a
			// blocked task, not a clean model stop. Preserve the terminal reason
			// for the outcome layer instead of letting an empty turn look like a
			// plausible completion attempt.
			if terminal && strings.TrimSpace(assistant.Text()) == "" {
				reason := strings.TrimSpace(stopReason)
				if reason == "" {
					reason = "the runtime closed the task because no further action can make progress"
				}
				reportTurn()
				return turns, fmt.Errorf("%w: %s", ErrTaskBlocked, reason)
			}
			// Serialized syntax that could not be normalized is a provider
			// protocol failure, not an answer. Spend one bounded recovery
			// request asking for the canonical call; never execute or invent
			// the partial envelope. A second failure is terminal for this run.
			//
			// The answer boundary can also produce this shape: the surface is
			// closed, so there is no canonical call left to re-emit. The one
			// recovery then asks for the prose answer from the evidence already
			// observed, and the diagnostic is still never published as the
			// answer. Without this branch a closed surface accepted the
			// diagnostic as a committed answer.
			if len(assistant.NormalizationFailures) > 0 {
				normalizationPending = true
				if !normalizationRecoveryAttempted && len(turns) < l.cfg.MaxTurns {
					directive := ""
					switch {
					case closed:
						directive = l.cfg.AnswerRequest
					case len(activeTools) > 0:
						directive = l.cfg.ProtocolRecoveryRequest
					}
					if strings.TrimSpace(directive) != "" {
						normalizationRecoveryAttempted = true
						nudges = append(nudges, directive)
						reportTurn()
						continue
					}
				}
				reportTurn()
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
						nudges = append(nudges, l.cfg.ExecutionRecoveryRequest)
					}
					reportTurn()
					continue
				}
				reportTurn()
				return turns, fmt.Errorf("%w: the model announced an execution it never emitted as a tool call",
					ErrExecutionIntentWithoutTool)
			}
			// A turn the provider cut off at the output ceiling produced no
			// complete action and no visible text: the model was still writing
			// when the budget ran out. That is not a deliberate stop, so
			// repeating the request at the same budget truncates identically.
			// The one recovery raises the completion ceiling to the model's
			// own ceiling and asks for the concrete next output. A second
			// truncation is reported as a budget stop, not as a silent model.
			if strings.TrimSpace(assistant.Text()) == "" &&
				assistant.StopReason == ai.StopMaxTokens &&
				len(turns) < l.cfg.MaxTurns {
				if !outputLimitRetried && strings.TrimSpace(l.cfg.OutputRecoveryRequest) != "" {
					outputLimitRetried = true
					completionBudget = l.escalateCompletionBudget(completionBudget)
					// A truncated turn has no replayable form: the partial
					// reasoning is dropped and the request restarts from the
					// last conversational turn plus the directive.
					live = live[:len(live)-1]
					origins = origins[:len(origins)-1]
					if closed || forceAnswer {
						if strings.TrimSpace(l.cfg.AnswerRequest) != "" {
							nudges = append(nudges, l.cfg.AnswerRequest)
						}
					} else {
						nudges = append(nudges, l.cfg.OutputRecoveryRequest)
					}
					reportTurn()
					continue
				}
				reportTurn()
				return turns, fmt.Errorf("%w: the response reached the %d-token completion ceiling before emitting a tool call or answer",
					ErrOutputTruncated, completionBudget)
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
				reportTurn()
				continue
			}
			// A mid-run turn that contains only hidden reasoning produced no
			// action and no answer. It cannot change the runtime state or give
			// the user a result, so treating it as a clean stop silently ends
			// the run with neither. This is the shape a reasoning model takes
			// when it exhausts the output budget on planning. Ask once for a
			// concrete call or answer; a second reasoning-only turn is a
			// recoverable lifecycle stop rather than a deliberate finish.
			if strings.TrimSpace(assistant.Text()) == "" && !closed && !forceAnswer &&
				!normalizationPending && !executionIntentPending &&
				strings.TrimSpace(assistant.ThinkingText()) != "" &&
				len(turns) < l.cfg.MaxTurns {
				if directive := strings.TrimSpace(l.cfg.OutputRecoveryRequest); directive != "" && !reasoningRetried {
					reasoningRetried = true
					// Reasoning-only content has no replayable form, so the
					// retry starts from the last conversational turn and the
					// directive, not from the exhausted reasoning.
					live = live[:len(live)-1]
					origins = origins[:len(origins)-1]
					nudges = append(nudges, directive)
					reportTurn()
					continue
				}
				reportTurn()
				return turns, fmt.Errorf("%w: the model produced only reasoning and no tool call or answer",
					ErrNoModelOutput)
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
					nudges = append(nudges, l.cfg.AnswerRequest)
				}
				// Keep the retry answer-only even if a projector would
				// otherwise re-offer the probe surface. The model already had
				// an answer boundary and produced no answer; reopening tools
				// would turn the lifecycle failure into another action loop.
				forceAnswer = true
				answerRetried = true
				reportTurn()
				continue
			}
			if strings.TrimSpace(assistant.Text()) == "" {
				// The previous turn was already a protocol recovery request.
				// Silence after it is the outcome of that stage, not a generic
				// empty model stop.
				if executionIntentPending {
					reportTurn()
					return turns, fmt.Errorf("%w: the model announced an execution and then produced no tool call",
						ErrExecutionIntentWithoutTool)
				}
				if normalizationPending {
					reportTurn()
					return turns, fmt.Errorf("%w: provider tool-call syntax did not resolve after recovery",
						ErrToolNormalizationFailed)
				}
				if activityObserved {
					// Work already ran; an empty closing turn is a completion
					// question. Returning the turns lets the evaluator judge
					// the evidence instead of replacing it with a protocol
					// error.
					reportTurn()
					return turns, nil
				}
				reportTurn()
				return turns, fmt.Errorf("%w: the provider produced no visible text and no tool call",
					ErrNoModelOutput)
			}
			// A visible text turn ends any correction tool window. The
			// publication gate reviews it, spends a correction pass when the
			// host authorized one and the claim set changed, and otherwise
			// commits or rejects it.
			l.correctionActive.Store(false)
			if !acceptAnswer() {
				continue
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
				l.noEvidenceInBatch(activeByName, redundant, calls, len(turns)))) {
			turn.Assistant = SanitizeAssistantMessage(answerWithoutToolCalls(assistant))
			turn.Answer = AnswerUnclassified
			turns = append(turns, turn)
			// The stored turn dropped the call blocks, so the live conversation
			// must drop them too. A correction request otherwise replays a call
			// with no result; a committed answer leaves a call the host would
			// persist without its outcome.
			live[len(live)-1] = AssistantMessage{Message: turn.Assistant}
			// A visible text turn ends any correction tool window.
			l.correctionActive.Store(false)
			if !acceptAnswer() {
				continue
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

		outcomes, admissionErr := l.dispatch(ctx, activeByName, surface, refusals, assistant, calls, turn, len(turns), turns, edit)
		for _, outcome := range outcomes {
			if outcome.ToolName != AnswerPlanToolName {
				continue
			}
			result, ok := outcome.Result.Details.(AnswerPlanResult)
			if !ok || !result.Reviewed {
				continue
			}
			if planAttempts >= MaxAnswerPlanAttempts {
				// The preflight is bounded. A later plan call is still a
				// control observation for the model, but it cannot reopen the
				// evidence surface again; the ordinary publication review owns
				// the answer.
				continue
			}
			planAttempts++
			if result.Accepted {
				planPassed = true
				planGaps = nil
				continue
			}
			planPassed = false
			planGaps = result.Issues
		}
		if len(planGaps) > 0 {
			for _, outcome := range outcomes {
				if planEvidenceOutcome(outcome) {
					// The model paid the evidence debt; the next request may
					// offer the plan again. A prose-only turn or a control
					// update leaves the debt in place and reopens evidence.
					planGaps = nil
					break
				}
			}
		}
		turn.Results = outcomes
		l.markVerification(ctx, edit, outcomes)
		for _, outcome := range outcomes {
			live = append(live, ToolResultMessage{
				ToolCallID: outcome.CallID,
				ToolName:   outcome.ToolName,
				Content:    ai.TextBlocks(outcome.Result.Text),
				IsError:    outcome.Result.IsError,
			})
			origins = append(origins, ai.ProvenanceToolResult)
		}

		turn.Assistant = SanitizeAssistantMessage(turn.Assistant)
		turns = append(turns, turn)
		// The turn is complete and its evidence is intact. Return the budget
		// stop now rather than asking the model for another turn it cannot
		// afford: the work so far is resumable, and completion evaluation can
		// still inspect exactly what happened.
		if admissionErr != nil {
			reportTurn()
			return turns, admissionErr
		}
		// A terminal projection has closed the task. If the model used the
		// request to answer, keep that answer for the completion evaluator;
		// otherwise the run ends here rather than spending another generation
		// rediscovering that no action is available.
		if terminal {
			if strings.TrimSpace(assistant.Text()) != "" {
				// Prose produced on a terminal surface is still an answer
				// candidate, not a published result. It crosses the same
				// claim-preserving publication gate as any other turn.
				if !acceptAnswer() {
					continue
				}
				return turns, nil
			}
			reason := strings.TrimSpace(stopReason)
			if reason == "" {
				reason = "the runtime closed the task because no further action can make progress"
			}
			reportTurn()
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
			reportTurn()
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
			// The calls were refused only because the surface was closed, so
			// the accompanying prose is the candidate answer, not narration.
			turns[len(turns)-1].Answer = AnswerUnclassified
			if !acceptAnswer() {
				continue
			}
			return turns, nil
		}
		// A closed surface can still receive a call with no accompanying prose.
		// The model has now ignored the answer boundary once; spend the single
		// bounded answer retry rather than falling straight into a hard block.
		// The turn and its refusal stay in the live conversation so the model
		// sees the consequence, and the retry is answer-only.
		if answerRequired(outcomes) && !answerRetried &&
			strings.TrimSpace(l.cfg.AnswerRequest) != "" && len(turns) < l.cfg.MaxTurns {
			answerRetried = true
			forceAnswer = true
			nudges = append(nudges, l.cfg.AnswerRequest)
			reportTurn()
			continue
		}
		// The objective's evidence is complete and the model still proposed a
		// call the runtime refused. No action is left, so the turns that
		// satisfied the objective are the authoritative result: end the run
		// and let completion evaluate them instead of reporting a block that
		// would erase a satisfied objective.
		if answerRequired(outcomes) && objectiveComplete {
			reportTurn()
			return turns, nil
		}
		unproductiveTurns++
		if unproductiveTurns < maxUnproductiveTurns {
			reportTurn()
			continue
		}
		// The model received the refusal and still produced nothing actionable.
		// If it also produced text, that text is the best candidate available;
		// the publication gate and completion decide whether it is sufficient.
		if strings.TrimSpace(assistant.Text()) != "" {
			// No call executed, so the prose is the best candidate available,
			// not a preamble to work that happened.
			turns[len(turns)-1].Answer = AnswerUnclassified
			if !acceptAnswer() {
				continue
			}
			return turns, nil
		}
		// Nothing can execute, so a further tool turn cannot make progress.
		// Close the action surface and ask for the answer instead of ending the
		// run with a hard block; the answer remains a completion candidate.
		// This applies only when an offered capability was refused: a model
		// that keeps calling a withheld tool has already ignored the surface,
		// and a model with no offered tools already had its answer-only turn.
		if !forceAnswer && !closed && offeredRefusals > 0 {
			// A refusal of an identical request-level rejection is correction
			// feedback, not a hostile repeat of a withheld capability. Give the
			// model one bounded corrected attempt before asking for prose; the
			// modified invocation is a different proposal and may execute.
			if allFailureRejected(outcomes) && unproductiveTurns <= maxUnproductiveTurns {
				reportTurn()
				continue
			}
			forceAnswer = true
			reportTurn()
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
		reportTurn()
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

// CompactionPolicy returns the policy automatic compaction uses. Manual
// compaction reads it so the two triggers cannot diverge.
func (l *Loop) CompactionPolicy() Compaction { return l.cfg.Compaction }

// Recall returns the run's archive so a manual compaction can write the same
// ids the recall tool resolves.
func (l *Loop) Recall() *RecallStore { return l.cfg.Recall }

// MeasureCompaction measures a supplied history against the loop's live policy
// and tool/system overhead. It is the manual-command counterpart of the
// automatic trigger: both use the same MeasureRequest call.
func (l *Loop) MeasureCompaction(messages []Message, projection ContextProjection, hasProjection bool) RequestMeasurement {
	window := l.model().ContextWindow
	if window <= 0 {
		return RequestMeasurement{}
	}
	return l.measureMessages(messages, window, l.cfg.Compaction, projection, hasProjection)
}

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

// escalateCompletionBudget raises a proven-insufficient completion cap to the
// model's own output ceiling.
//
// A turn that stopped at the cap before emitting any call or text cannot be
// recovered by repeating the identical request: the model's reasoning alone
// consumed the budget. The model ceiling is a bound, not a target — the model
// still stops when it is done and the run's turn budget remains the outer
// limit — so using it for the recovery is the same budget the provider would
// apply to a caller that never set a cap. When the model publishes no ceiling,
// doubling the current budget is the only evidence-based step; a zero budget
// already means "provider ceiling" and cannot be raised.
func (l *Loop) escalateCompletionBudget(current int) int {
	ceiling := l.model().MaxOutputTokens
	if ceiling > current {
		return ceiling
	}
	if current > 0 {
		return current * 2
	}
	return current
}

// system returns the system prompt for the next request: exactly the stable
// prefix the host supplied. Nothing per-turn is appended here.
//
// The distinction is the whole cache contract. A system block sits ahead of the
// conversation on every wire protocol, so anything volatile in it makes the
// provider re-read the entire request each turn; per-turn state travels as the
// request's runtime message instead (see RuntimeContext).
func (l *Loop) system() []ai.SystemPromptBlock {
	if l.cfg.SystemFn != nil {
		return l.cfg.SystemFn()
	}
	return l.cfg.System
}

// SetRuntimeContext installs the runtime state carried as the request's final
// message on every subsequent request. It is how a caller injects per-turn
// context — files the user referenced, the task continuity briefing — without
// rebuilding the loop or touching the cached prefix. Pass nil to clear it.
//
// It must be called before Run, never during one.
func (l *Loop) SetRuntimeContext(blocks []string) {
	l.cfg.RuntimeContext = blocks
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
//
// The delta hook is an output boundary, not the provider wire. A model can copy
// the request-side runtime state into its prose, so the live text is projected
// through the same sanitizer the completed answer uses. A consumer never sees a
// runtime directive that the final answer would withhold, and the live text
// stays a prefix of the completed text even when a trigger is split across
// deltas.
func (l *Loop) drain(ctx context.Context, stream *ai.Stream, cancel context.CancelFunc) (ai.AssistantMessage, error) {
	var textSanitizer, thinkingSanitizer *answerStreamSanitizer
	hooks := ai.DrainHooks{}
	if l.cfg.OnTextDelta != nil {
		textSanitizer = newAnswerStreamSanitizer()
		hooks.OnText = func(delta string) {
			if public := textSanitizer.Write(delta); public != "" {
				l.cfg.OnTextDelta(public)
			}
		}
	}
	// Reasoning is a public block in the UI, not a private scratchpad. It is a
	// separate channel, but the runtime-state boundary still applies: a model
	// that copies the request briefing into its reasoning must not publish the
	// orchestration state through the expanded reasoning view.
	if l.cfg.OnThinkingDelta != nil {
		thinkingSanitizer = newAnswerStreamSanitizer()
		hooks.OnThinking = func(delta string) {
			if public := thinkingSanitizer.Write(delta); public != "" {
				l.cfg.OnThinkingDelta(public)
			}
		}
	}
	message, err := ai.DrainStreamHooksIdle(ctx, stream, hooks, l.cfg.StreamIdleTimeout)
	// An idle transport is aborted before returning: the provider goroutine
	// must not keep reading a response the run has stopped waiting for. A
	// parent cancellation has its own cause and is not reclassified.
	if errors.Is(err, ai.ErrStreamStalled) {
		if cancel != nil {
			cancel()
		}
		err = fmt.Errorf("%w: %v", ErrProviderStalled, err)
	}
	if textSanitizer != nil {
		if tail := textSanitizer.Flush(); tail != "" {
			l.cfg.OnTextDelta(tail)
		}
	}
	if thinkingSanitizer != nil {
		if tail := thinkingSanitizer.Flush(); tail != "" {
			l.cfg.OnThinkingDelta(tail)
		}
	}
	return message, err
}

// runEdit tracks one run's optional edit transaction.
type runEdit struct {
	journal EditJournal
	txn     EditTransaction
}

// actionSurface is the projection's view of what the runtime offered for one
// request. The loop passes it to dispatch so a withheld call can be refused
// with the same stage and reason the projection already sent, instead of a bare
// "not offered" that leaves the model to rediscover the surface by trial and
// error. It is control data, never authority: the security gate still decides
// every call that is offered.
type actionSurface struct {
	stage    string
	withheld map[string]string
}

// asToolArgumentError extracts the provider-boundary classification for a
// streamed tool call whose arguments could not be decoded.
func asToolArgumentError(err error) *ai.ToolCallArgumentError {
	var argumentErr *ai.ToolCallArgumentError
	if errors.As(err, &argumentErr) {
		return argumentErr
	}
	return nil
}

// dispatch runs a batch of tool calls.
//
// Calls marked exclusive run alone; shared calls run concurrently, except in
// the answer-correction window, whose small allowance is admitted in model
// order. Results are always returned in the order the model emitted the calls,
// so the transcript matches the assistant message regardless of completion
// order.
func (l *Loop) dispatch(
	ctx context.Context,
	byName map[string]Tool,
	surface actionSurface,
	refusals map[string]string,
	assistant ai.AssistantMessage,
	calls []ai.ToolCall,
	turn Turn,
	turnIndex int,
	history []Turn,
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

	// The answer-correction allowance is per call, but a batch is one model
	// action. A concurrent correction batch lets whichever goroutine reaches
	// the atomic counter first win the last slot, so the runtime could execute
	// a later call while refusing an earlier one in the same batch. The
	// allowance is at most a handful of calls, so serializing only the
	// correction window costs little and makes both the admitted set and the
	// refusals follow model order. Investigation batches keep their
	// concurrency.
	serial := l.correctionActive.Load()
	run := func(position int, call ai.ToolCall) {
		outcome := l.execute(ctx, byName, surface, refusals, assistant, call, turn, turnIndex, history)
		l.noteEvidence(outcome.Result)
		l.noteObservation(call, outcome.Result, turnIndex)
		record(position, outcome)
	}
	for i, call := range calls {
		exclusive := isExclusive(byName, call)
		if exclusive {
			// An exclusive call must run alone: drain anything already in
			// flight before starting it.
			wg.Wait()
		}

		if serial {
			run(i, call)
		} else {
			wg.Add(1)
			go func(position int, call ai.ToolCall) {
				defer wg.Done()
				run(position, call)
			}(i, call)
		}

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
func (l *Loop) noEvidenceInBatch(active map[string]Tool, redundant map[string]bool, calls []ai.ToolCall, turnIndex int) bool {
	if l.cfg.Project == nil || len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		tool, built := l.builtTool(call.Name)
		if !built || policySensitive(tool) {
			return false
		}
		// A capability the control plane judged unable to close the current
		// gap cannot add evidence, whether or not it is still offered: it stays
		// in the tool block only to keep the cached prefix intact.
		if redundant[call.Name] {
			continue
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
	surface actionSurface,
	refusals map[string]string,
	assistant ai.AssistantMessage,
	call ai.ToolCall,
	turn Turn,
	turnIndex int,
	history []Turn,
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
					Text:              closedSurfaceRefusal(call.Name, surface),
					IsError:           true,
					FailureClass:      ToolFailureCapabilityUnavailable,
					CandidateRejected: true,
					AnswerRequired:    !policyRefusal,
					Details:           refusalDetails(call.Name, surface, nil),
				},
			}
		}
		// A tool the loop was built with but the task-shaped projection
		// withheld is a runtime action-space refusal, not an execution
		// failure. The model is told the stage, the control-plane reason, and
		// which tools are still available, so recovery is a choice rather than
		// another blind probe; completion does not count the refusal as a
		// failed call.
		if l.cfg.Project != nil && l.hasTool(call.Name) {
			available := sorted(names)
			return ToolOutcome{
				CallID:   call.ID,
				ToolName: call.Name,
				Args:     call.Arguments,
				Result: Result{
					Text:              withheldSurfaceRefusal(call.Name, surface, available),
					IsError:           true,
					FailureClass:      ToolFailureCapabilityUnavailable,
					CandidateRejected: true,
					Details:           refusalDetails(call.Name, surface, available),
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

	// A correction has its own small bound. The initial investigation already
	// spent the tree budget; reopening it would turn one rejected claim into a
	// second investigation, which is the cost this window exists to prevent.
	// Authority still runs first: a denied call never consumes the correction
	// allowance.
	// Conclude records a control observation, not evidence. It must remain
	// available after the evidence allowance is spent; refusing it forces
	// another full answer and turns the terminal control action into a failure.
	// A plan tool is the same kind of control action: it records the model's own
	// guidance and acquires no evidence, so spending the investigation allowance
	// must not make an open plan impossible to reconcile before the answer.
	if l.correctionActive.Load() && call.Name != "conclude" && !isPlanTool(tool) {
		limit := int64(l.correctionToolBudget())
		if used := l.correctionCalls.Add(1); used > limit {
			return failClass(ToolFailureCorrectionBudget,
				"the repair window is closed. Answer with the evidence already observed; qualify or remove any claim that remains unsupported")
		}
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
		Ctx:            ctx,
		CallID:         call.ID,
		ToolName:       call.Name,
		SessionID:      l.cfg.SessionID,
		ReadOnly:       decision != nil && decision.ReadOnly,
		ConclusionMode: l.cfg.ConclusionMode,
		Budget:         l.budget,
		Deadline:       l.deadline(),
	}
	if call.Name == AnswerPlanToolName && l.cfg.AnswerPlanReview != nil {
		// The plan is a control observation: the audit sees the evidence from
		// completed turns and never the plan's own claims. The history slice
		// is immutable for the duration of dispatch; Run appends to the turn
		// list only after every outcome is collected.
		reviewTurns := history
		toolCtx.AnswerPlanReview = func(plan AnswerPlan) []contract.GroundingIssue {
			return l.cfg.AnswerPlanReview(reviewTurns, plan)
		}
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

// refusalStage names the evidence-lifecycle stage for a refusal message. An
// empty stage is a projection that did not report one; the message then omits
// the parenthetical rather than inventing a value.
func refusalStage(surface actionSurface) string {
	if strings.TrimSpace(surface.stage) == "" {
		return ""
	}
	return " (stage: " + surface.stage + ")"
}

// withheldSurfaceRefusal renders a recoverable action-space refusal.
//
// It answers the three questions a model needs to pick a different action:
// what was requested, why the runtime withheld it, and what is still offered.
// It never widens the surface and never executes the withheld call.
func withheldSurfaceRefusal(name string, surface actionSurface, available []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Tool %q is not offered in the current task stage%s. Nothing ran; no state changed.",
		name, refusalStage(surface))
	if reason := strings.TrimSpace(surface.withheld[name]); reason != "" {
		sb.WriteString(" Runtime reason: " + reason + ".")
	}
	if len(available) > 0 {
		sb.WriteString(" Available actions: " + strings.Join(available, ", ") + ".")
		sb.WriteString(" Choose an available action, or answer from the evidence already observed.")
	} else {
		sb.WriteString(" No action is available; answer from the evidence already observed.")
	}
	return sb.String()
}

// closedSurfaceRefusal renders the answer-boundary refusal. The closed surface
// has no action to choose, so the only valid recovery is prose.
func closedSurfaceRefusal(name string, surface actionSurface) string {
	text := fmt.Sprintf("No tool is available in the current task stage%s; this call is not offered for the objective.",
		refusalStage(surface))
	if reason := strings.TrimSpace(surface.withheld[name]); reason != "" {
		text += " Runtime reason: " + reason + "."
	}
	text += fmt.Sprintf(" Answer from the evidence already established. Ignored call: %q.", name)
	return text
}

// refusalDetails is the structured audit counterpart of a refusal message. It
// lets a host or test inspect the stage, reason, state-change fact, and offered
// actions without parsing prose.
func refusalDetails(name string, surface actionSurface, available []string) map[string]any {
	details := map[string]any{
		"tool":          name,
		"reason":        "tool_not_offered",
		"state_changed": false,
	}
	if surface.stage != "" {
		details["stage"] = surface.stage
	}
	if reason := surface.withheld[name]; reason != "" {
		details["runtime_reason"] = reason
	}
	if available != nil {
		details["available_tools"] = append([]string(nil), available...)
	}
	return details
}

// compactIfNeeded reduces live context when the projected next request no
// longer fits the model window.
//
// It reports whether a compaction happened. When it did, the caller should
// restart the turn rather than send a request built from the pre-compaction
// context.
//
// The decision uses the full projected request — stored conversation, system
// blocks, tool definitions, the durable task projection, the expected output,
// and a safety margin — floored by the provider-reported size from the
// previous turn. Measuring the conversation alone let a large tool surface or
// output budget overflow a window that looked half empty.
func (l *Loop) compactIfNeeded(ctx context.Context, live *[]Message) (bool, error) {
	c := l.cfg.Compaction
	if !c.Enabled || c.Strategy == StrategyOff {
		return false, nil
	}
	if l.compactionCooldown > 0 {
		l.compactionCooldown--
		return false, nil
	}
	window := l.model().ContextWindow
	if window <= 0 {
		return false, nil
	}
	if l.cfg.Summarize == nil && l.cfg.CompactionProjection == nil {
		return false, nil
	}

	measure := l.measureContext(*live, window, c)
	if !NeedsCompaction(measure) {
		return false, nil
	}
	if l.compactionAttempts >= c.maxAttempts() {
		// Another compaction would repeat the same rewrite. Report the truth
		// rather than sending a request that cannot fit.
		return false, fmt.Errorf("%w: compaction ran %d times and the projected request is still %d of a %d-token window",
			ErrContextOverflow, l.compactionAttempts, measure.Projected, window)
	}
	l.compactionAttempts++

	result, err := CompactWith(ctx, *live, CompactOptions{
		Compaction:           c,
		Window:               window,
		Summarize:            l.cfg.Summarize,
		Projection:           l.cfg.CompactionProjection,
		Recall:               l.cfg.Recall,
		ExpectedOutputTokens: l.expectedOutput(window, c),
		ContextBudgetTokens:  measure.ContextBudget(),
		Trigger:              measure.Pressure,
	})
	if err != nil {
		// A failed compaction must not abort the run while the context still
		// fits. Deterministic trimming is the safe fallback; if it cannot make
		// the request safe either, report a truthful overflow instead of
		// silently sending it.
		emergency, report := TrimMessages(*live, c, l.cfg.Recall)
		// Deduplication may have nothing to remove while a single oversized
		// payload still overflows. Bound the remaining payloads as the second
		// deterministic step before giving up.
		if next := l.measureContext(emergency, window, c); report.ToolResultsTrimmed == 0 || next.Pressure == PressureCritical {
			if bounded, boundReport := BoundToolResults(emergency, l.cfg.Recall, minBoundToolBytes); boundReport.ToolResultsTrimmed > 0 {
				emergency = bounded
				report.ToolResultsTrimmed += boundReport.ToolResultsTrimmed
				report.BytesRemoved += boundReport.BytesRemoved
			}
		}
		if report.ToolResultsTrimmed > 0 {
			next := l.measureContext(emergency, window, c)
			if next.Projected < measure.Projected {
				*live = emergency
				l.lastProviderTokens = 0
				errResult := CompactResult{
					Messages:     emergency,
					BeforeTokens: measure.ContextTokens,
					AfterTokens:  EstimateMessages(emergency),
					Mode:         "trim",
					Trimmed:      report.ToolResultsTrimmed,
					Methods:      []string{"trim"},
					Metrics: CompactionMetrics{
						ContextBefore:      measure.ContextTokens,
						ContextAfter:       EstimateMessages(emergency),
						ContextLimit:       window,
						ProjectedBefore:    measure.Projected,
						Trigger:            measure.Pressure,
						Mode:               "trim",
						ToolOutputRemoved:  report.BytesRemoved,
						CompactionFailures: 1,
					},
				}
				if l.cfg.OnCompact != nil {
					l.cfg.OnCompact(errResult)
				}
				l.reportCompactionMetrics(errResult.Metrics)
				return true, nil
			}
		}
		return false, fmt.Errorf("%w: %w", ErrContextOverflow, err)
	}

	*live = result.Messages
	l.lastProviderTokens = 0
	result.Metrics.ProjectedBefore = measure.Projected
	post := l.measureContext(*live, window, c)
	result.Metrics.ContextAfter = post.ContextTokens
	if l.cfg.OnCompact != nil {
		l.cfg.OnCompact(result)
	}
	l.reportCompactionMetrics(result.Metrics)
	if post.Pressure == PressureCritical {
		if l.compactionAttempts >= c.maxAttempts() {
			return false, fmt.Errorf("%w: rebuilt context is still projected at %d of a %d-token window",
				ErrContextOverflow, post.Projected, window)
		}
		// Another rewrite may still fit. Do not cool down, or the loop would
		// send the request it just found unsafe.
		l.compactionCooldown = 0
		return true, nil
	}
	// The rebuilt context is the smallest truthful projection available.
	// Further compaction here would summarise the summary, so let the provider
	// see it; the next turn starts a fresh decision.
	l.compactionCooldown = 1
	return true, nil
}

// expectedOutput is the completion budget used for prediction. The per-request
// MaxTokens is a ceiling, not a promise: predicting it in full would report a
// large window as overflowing before any conversation accumulated.
func (l *Loop) expectedOutput(window int, c Compaction) int {
	expected := c.expectedOutput(window)
	if l.cfg.MaxTokens > 0 && l.cfg.MaxTokens < expected {
		expected = l.cfg.MaxTokens
	}
	return expected
}

// measureContext accounts for every billed component of the next request.
func (l *Loop) measureContext(messages []Message, window int, c Compaction) RequestMeasurement {
	var projection ContextProjection
	has := false
	if l.cfg.CompactionProjection != nil {
		projection, has = l.cfg.CompactionProjection()
	}
	return l.measureMessages(messages, window, c, projection, has)
}

// measureMessages is the one measurement path. Automatic compaction and the
// manual command both reach it, so pressure cannot be calculated twice.
func (l *Loop) measureMessages(messages []Message, window int, c Compaction, projection ContextProjection, hasProjection bool) RequestMeasurement {
	system := l.system()
	taskTokens := 0
	if hasProjection {
		taskTokens = tokens.Count(projection.Render(), tokens.ModeEstimate)
	}
	return MeasureRequest(MeasureInput{
		ContextTokens:        EstimateMessages(messages),
		ProviderTokens:       l.lastProviderTokens,
		SystemTokens:         EstimateSystemTokens(system),
		ToolTokens:           EstimateToolTokens(l.cfg.Tools),
		TaskStateTokens:      taskTokens,
		ExpectedOutputTokens: l.expectedOutput(window, c),
		Window:               window,
	}, c)
}

// reportCompactionMetrics publishes structured diagnostics without ever
// including source content.
func (l *Loop) reportCompactionMetrics(metrics CompactionMetrics) {
	if l.cfg.OnCompactionMetrics != nil {
		l.cfg.OnCompactionMetrics(metrics)
	}
}
