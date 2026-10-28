package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/budget"
	"github.com/PizenLabs/ohgo/cognition"
	"github.com/PizenLabs/ohgo/commit"
	"github.com/PizenLabs/ohgo/exec"
	"github.com/PizenLabs/ohgo/prompt"
	"github.com/PizenLabs/ohgo/recovery"
	"github.com/PizenLabs/ohgo/session"
	"github.com/PizenLabs/ohgo/tokens"
	"github.com/PizenLabs/ohgo/workspace"
	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
)

// Terminal-transition states for one turn. They are deliberately three-valued:
// a completed claim and a cancellation claim are both terminal, and neither can
// be rewritten by the other.
const (
	turnTerminalOpen int32 = iota
	turnTerminalCompleted
	turnTerminalCancelled
)

// Approval choices offered to the user.
//
// The conservative answer is listed last so the resting cursor lands on it:
// Move clamps at the ends, so from the default no navigation key can reach an
// affirmative answer without a deliberate move onto one. "Allow for entire
// session" is a remembered affirmative, but it still demands that deliberate
// move — it is never the resting place.
var approvalChoices = []string{"Yes", sessionApprovalChoice, "No"}

// approvalAnswer is one user decision, delivered back to a blocked tool.
type approvalAnswer struct {
	approved bool
	reason   string
}

// approvalRequest is a question the loop is blocked on.
//
// The request carries its own reply channel, which is what routes an answer to
// the specific tool that asked rather than to whichever one reads next.
type approvalRequest struct {
	toolName string
	reason   string
	summary  string
	preview  agent.MutationPreview
	// mutating marks a state-changing call, so the dialog can accent a
	// destructive boundary differently from a read one.
	mutating bool
	answer   chan approvalAnswer
}

// teaEvent wraps a state-machine event for the Bubble Tea runtime.
type teaEvent struct{ event Event }

// approvalPrompt carries a pending approval question into Update.
type approvalPrompt struct{ request approvalRequest }

// turnFinishedMsg ends a turn command. It exists so queued prompts drain on
// the update goroutine: the turn runs off-goroutine, but only the update
// goroutine may mutate state. Draining from the turn goroutine would race the
// renderer; draining here serialises it.
type turnFinishedMsg struct{}

// tickMsg advances the spinner and the thinking shimmer.
type tickMsg time.Time

// spinnerInterval bounds animation frame rate. 100ms keeps the shimmer wave
// smooth without burning a core; the spinner shares the same tick.
const spinnerInterval = 100 * time.Millisecond

// Program drives the interactive session.
//
// It bridges two concurrency models: the agent loop calls back synchronously
// from a tool goroutine, while Bubble Tea wants message-passing. State events
// flow out through a buffered channel, and each approval carries its own reply
// channel so an answer always reaches the tool that blocked on it.
type Program struct {
	// loop runs the agent turns.
	loop *agent.Loop

	// state is the view state. It is owned by the Bubble Tea update
	// goroutine and is never touched from the loop goroutines.
	state State

	// events carries state-machine events from the loop into the UI.
	events chan Event
	// approvals carries approval questions from blocked tools.
	approvals chan approvalRequest

	// pending is the approval currently being shown. It is nil when no tool is
	// blocked, and it is only ever written from the update goroutine.
	pending *approvalRequest

	// sessionAllowed records tools the user approved for the whole session
	// ("Allow for entire session"). It is written from the update goroutine
	// and read from blocked tool goroutines in ApprovalFunc, so every access
	// holds mu.
	sessionAllowed map[string]struct{}

	// done closes when Run returns. It bounds the approval bridge past
	// shutdown: a tool reaching approval after the UI is gone gets a denial
	// instead of waiting on an answer nobody will send. Never closed by hand;
	// closeDone owns it.
	done chan struct{}
	// doneOnce makes the shutdown release idempotent: Run defers it, and
	// tests drive shutdown without Run.
	doneOnce sync.Once

	// history is the running conversation. The Program owns it because
	// Loop.Run is a turn, not a session — see AGENTS.md invariant I18.
	// It is touched from turn goroutines and the update goroutine alike,
	// so every access goes through mu.
	history []agent.Message

	// termOut is where terminal mode changes are written. It is a field so a
	// test can observe the sequences without a terminal; production writes to
	// the process stdout.
	termOut io.Writer

	// mouseReporting records whether the terminal is asked for mouse events.
	// Off by default: see WithMouseReporting for the trade it avoids.
	mouseReporting bool

	// live is the output-first presenter, set by RunLive and nil otherwise.
	// When it is set, View returns only the live region and transcript blocks
	// leave through the renderer as print lines — see live.go.
	live *livePainter

	// promptHistory is the durable prompt store, if one was supplied. It is
	// read when the program starts and appended to for every turn that
	// actually reaches the agent, never for a draft or a ghost suggestion.
	promptHistory PromptHistory

	// transcriptIndex, viewport, and transcriptFrame are the retained
	// full-screen projection: document geometry, scroll position, and the
	// rows of the frame about to paint. They are UI-goroutine owned.
	transcriptIndex *TranscriptLayout
	viewport        ViewportTracker
	transcriptFrame *transcriptFrame

	// drag is the in-progress application-owned selection. It lives only
	// between a press inside the transcript and the matching release.
	drag *dragState
	// clipboard encodes logical text for the terminal. Nil disables copying
	// (dumb terminal or OHGO_CLIPBOARD=off); no external process is ever used.
	// Emission writes the encoded sequence synchronously to termOut, so
	// Copied means the bytes reached the terminal transport, not a queue.
	clipboard func(string) string
	// traceFn receives lightweight UI events when diagnostics are enabled.
	traceFn func(string)
	// lastFollowing and lastScrollSeq detect transitions for instrumentation.
	lastFollowing bool
	lastScrollSeq int

	// cancelTurn aborts the in-flight turn, if any. Set when a turn starts,
	// cleared when it ends, guarded by mu for the same reason as history.
	cancelTurn context.CancelFunc
	// turnTerminal is the turn's terminal-transition linearization point.
	// Cancellation stores CANCELLED; the post-run transition claims OPEN with
	// a compare-and-swap before it persists COMPLETED. Whichever operation
	// wins, the other observes it: a cancel that lands before the claim turns
	// the action into PAUSE, and a claim that lands first is a completion the
	// cancellation arrived after.
	turnTerminal atomic.Int32
	// beforeTaskTransition is a deterministic test seam for the exact
	// window between "not cancelled" and the terminal claim. Production never
	// sets it.
	beforeTaskTransition func()
	// mu guards history and cancelTurn across the update and turn goroutines.
	mu sync.Mutex

	// sessionStart, sessionIn, and sessionOut are the live session's
	// progressive-disclosure telemetry: when the first turn began and the
	// measured input/output tokens accumulated since. They are guarded by mu
	// because the turn goroutine samples them while the update goroutine
	// resets them at a session boundary (/clear, resume). Zero sessionStart
	// means no turn has run; /usage remains the lifetime ledger and is
	// deliberately not consulted here.
	sessionStart time.Time
	sessionIn    int
	sessionOut   int

	// window is the context window used for the budget display.
	window int

	// store and session persist the transcript. Both nil means the program is
	// running without persistence, which is a valid mode for a throwaway or
	// embedded session.
	store   *session.Store
	session *session.Session

	// setModel switches the loop's model. currentModel reads it back for the
	// status line. Both nil-safe: a program built without them reports
	// "model switching is unavailable" rather than panicking on /model.
	setModel     func(id string) error
	currentModel func() string
	// currentEffort reports the active thinking effort for the header badge.
	// Nil means unknown: the badge reflects Status.Effort and any variant
	// suffix in Status.Model rather than inventing a level.
	currentEffort func() string

	// listSessions, openSession, listModels and listJobs feed the selection
	// overlays. Each nil-safe: without a provider the command reports the
	// feature unavailable instead of failing. They are functions because the
	// program owns neither the session store, the catalog, nor the tool set.
	listSessions func() ([]SessionItem, error)
	openSession  func(id int64) (*session.Session, OpenedSession, error)
	// commitHooks own the dedicated /commit workflow. commitRequest is the
	// proposal awaiting a human answer; commitRunning is the one execution in
	// flight; commitProposing marks a read-only proposal generation; and
	// commitEditing is the composer-backed message editor. All are touched
	// only by the update goroutine.
	commitHooks     CommitHooks
	commitRequest   *commit.Request
	commitRunning   bool
	commitProposing bool
	commitEditing   bool
	// commitCancel aborts an in-flight proposal request with a cause, so the
	// failure can distinguish user cancellation from session shutdown and
	// supersession. Execution is never cancelled here: a Git commit that
	// already started must report its real outcome rather than pretend it was
	// abandoned.
	commitCancel context.CancelCauseFunc
	// commitSeq invalidates a proposal that was generated against a session
	// the user has since left, or a proposal the user cancelled.
	commitSeq int
	// sessionActions are the Session Manager's write operations. Every field
	// is nil-safe: a missing hook reports the action unavailable rather than
	// pretending it succeeded.
	sessionActions SessionActions
	listModels     func() []string
	// listModelOptions is the enriched roster behind the /model picker:
	// native ids plus preformatted context/price/capability detail built by
	// the caller, which owns the catalog. Preferred over listModels when set.
	listModelOptions func() []ModelOption
	listJobs         func() []JobItem
	// listMentions lists workspace paths for the "@" trigger. Nil disables
	// file completion, leaving "@" a literal character. It is called once per
	// session, when the first "@" token appears.
	listMentions func() []string
	// readMention reads one workspace-relative file for "@" context
	// injection. Nil disables context resolution, leaving "@" a pure
	// completion affordance. It is called on the turn goroutine, once per
	// reference per submitted prompt.
	readMention func(path string) (string, error)
	// mentionsLoaded records that the "@" roster was installed, so a session
	// that references files walks the workspace at most once.
	mentionsLoaded bool
	// completionRoot and completionResolver provide the pre-run target
	// evidence for a mutation request. Nil means the program was built without
	// semantic completion wiring; a targeted mutation then becomes reviewable
	// rather than being trusted. They are installed by the host, which owns
	// the workspace root and the existing symbol index.
	completionRoot     string
	completionResolver agent.StructuralResolver
	// lastJobs remembers the listed jobs so choosing one can show its
	// output. It is only touched from the update goroutine.
	lastJobs []JobItem
	// onThemeChanged persists a theme switch. Nil means session-only.
	onThemeChanged func(theme string)

	// usage accumulates token totals for /usage. Nil means unavailable.
	usage *agent.UsageLog
	// compactor summarises history for /compact. Nil means unavailable.
	compactor Compactor
	// doctor runs diagnostics for /doctor. Nil means unavailable.
	doctor DoctorFunc
	// providers lists credential status for the /usage inspector. Nil means
	// the provider roster renders as unknown.
	providers ProvidersFunc
	// probe checks the current endpoint when /usage opens. Nil skips probing.
	probe ProbeFunc
	// symbolStatus reports the optional code-index status for the /status
	// card's SYMBOL ENGINE section. Nil (the default) hides the section so
	// the card never invents an engine that is not wired.
	symbolStatus func() StatusEngine
	// mcp inspects the session's configured MCP servers. Zero value reports
	// MCP unavailable rather than opening a second registry.
	mcp MCPHooks
	// mcpBefore records each server's actual enabled state when the /mcp
	// manager opened, so Enter applies only rows the user changed and Esc can
	// discard the rest. Update-goroutine-owned.
	mcpBefore map[string]bool
	// deleter removes saved credentials for /disconnect. Nil means unavailable.
	deleter CredentialDeleter
	// connect drives the /connect setup flow. Zero value reports each step
	// unavailable rather than panicking.
	connect ConnectHooks
	// flow tracks an in-progress setup. Update-goroutine-owned, like pending.
	flow *connectFlow

	// catalog feeds the /model picker with live data. Snapshot answers
	// instantly from bundled plus cached rows; Refresh fetches in the
	// background and reports through a Bubble Tea command, never blocking
	// the event loop. Zero value degrades to the legacy listers below.
	catalog CatalogHooks
	// catalogCtx bounds an in-flight catalog refresh to the program's
	// lifetime. It is cancelled by closeDone, so quitting does not leave a
	// provider fetch running behind a dead UI.
	catalogCtx context.Context
	// catalogCancel releases catalogCtx. Nil-safe for a zero Program.
	catalogCancel context.CancelFunc
	// modelRefreshing records that a catalog refresh is in flight. It
	// survives the picker closing, so reopening /model reuses the running
	// refresh instead of fanning out another one.
	modelRefreshing bool
	// modelControl feeds and persists the /model control plane. Its zero
	// value means the plane is unavailable and /model falls back to the
	// picker; the program never owns durable configuration itself.
	modelControl ModelControlHooks
	// modelSeq is the latest catalog refresh generation. Older, slower
	// responses carry a trailing Seq and are dropped, never applied.
	modelSeq int
	// pickerRestore holds composer text to put back when a modal opened over
	// typing (slash-tab discovery: the picker or the Command & Help Center)
	// closes without choosing.
	pickerRestore string
	// pickerRestoreSet marks pickerRestore as deliberate: an empty restore
	// is still a restore, distinct from "no restore".
	pickerRestoreSet bool

	// support runs an ephemeral $sup query on the SUP role's model. Nil
	// means the surface is unavailable, in which case $sup is treated as an
	// ordinary prompt. Its conversation never reaches the store or history.
	// The onDelta callback streams answer chunks; it may be nil.
	support func(ctx context.Context, question string, onDelta func(string)) (string, error)

	// supportInfo reports the effective SUP model reference and effort for
	// display. Nil means unknown: the overlay says so rather than
	// fabricating it.
	supportInfo func() (model, effort string)

	// supportCancel aborts the in-flight support turn, if any. Guarded by
	// mu like cancelTurn.
	supportCancel context.CancelFunc

	// turnSeq issues turn identities. Guarded by mu: runTurn increments it
	// off the update goroutine while cancelTurnNow reads state on it.
	turnSeq int

	// workspace hooks route /ask, /fuzz, /build through the CLI-owned
	// authority seam. The TUI never changes authority itself: it calls
	// Switch, which owns validation, the runtime, persistence, and chrome.
	workspace WorkspaceHooks

	// recovery is the host's workspace recovery authority. Nil means recovery
	// is unavailable, in which case /undo and /redo report that rather than
	// silently doing nothing. The program never builds a checkpoint itself:
	// the journal is handed to the loop per turn, and Undo/Redo are invoked
	// only from the explicit slash commands.
	recovery Recoverer

	// shell wires the "!" escape: the process runner and the mutation
	// classifier the ASK gate consults. Its zero value reports the feature
	// unavailable rather than shelling out to an unwired process.
	shell ShellHooks

	// shellCancel aborts the in-flight escape, guarded by mu.
	shellCancel context.CancelFunc
	// shellCancelSeq is the run identity shellCancel belongs to, so a
	// finishing run cannot clear a newer run's handle.
	shellCancelSeq int
	// shellSeq issues escape run identities. Guarded by mu.
	shellSeq int
	// pendingEscape is the command awaiting confirmation. It is owned by the
	// update goroutine, like pending.
	pendingEscape string

	// wizard is the onboarding dialog for uninitialized projects, set by
	// SetInitWizard. While non-nil it owns the keyboard and the middle of
	// the screen; header and footer keep rendering from state. It is
	// update-goroutine-owned, like pending and flow.
	wizard *InitWizardModel
	// wizardHooks commits or abandons the wizard's decisions. See
	// InitWizardHooks for the goroutine contract.
	wizardHooks InitWizardHooks

	// updateFn is the explicit update authority behind /update. Nil reports
	// the command unavailable: the TUI never owns release resolution,
	// download, verification, or installation itself.
	updateFn UpdateFunc
	// updateRunning guards against a second explicit update racing the first.
	// It is update-goroutine-owned, like pending and flow.
	updateRunning bool

	// task is the durable task continuity layer, nil-safe. When set, runTurn
	// records lifecycle boundaries (turn start, interruption, completion,
	// failure) and the transcript projects the task state. It is bookkeeping
	// only: the loop remains the execution owner and the task layer grants no
	// authority (see TaskHooks).
	task TaskHooks
	// lastTaskDecision records why the last turn attached, detached, or
	// started a task: NEW_SESSION, EXPLICIT_RESUME, NEW_INTENT, TASK_DETACH,
	// TASK_COMPLETE, or NO_TASK. It is diagnostics for /audit and tests, never
	// shown as a blocking prompt. Guarded by mu because runTurn writes it off
	// the update goroutine.
	lastTaskDecision string
	// proposal is the human's decided proposal awaiting or serving its turn.
	// It is set when a proposal is resolved and consumed by the released turn
	// (plus explicit continuations of the same execution). A new intent clears
	// it; a session boundary drops it. Nil means no decision stands. Guarded
	// by mu: resolveProposal writes it on the update goroutine while runTurn
	// and submit read it off-goroutine.
	proposal *ProposalResolution
	// newSessionCreating guards picker Enter against double creation: the
	// picker closes before the session is made, so a second Enter finds no
	// picker and cannot create a second session. Update-goroutine-owned.
	newSessionCreating bool
}

// WorkspaceHooks route workspace commands through their owner.
//
// Current reports the workspace in force (e.g. "build"); Switch moves the
// session to the named workspace at a turn boundary. PrepareTurn derives the
// authority for one turn from its request text. All are nil-safe: a program
// built without them reports the feature unavailable.
type WorkspaceHooks struct {
	// Current names the workspace in force.
	Current func() string
	// Switch moves the session to the named workspace.
	Switch func(name string) error
	// PrepareTurn validates a turn's request against the active authority and
	// reports a refusal before the model is called. It never changes authority.
	PrepareTurn func(text string) TurnAuthority
	// Predict classifies a turn's request and reports the safe workspace
	// transition its owner would propose, if any. It must not change
	// authority: the program calls it before deciding a turn, so a proposal
	// can never move the runtime by being computed. Nil disables transition
	// proposals entirely.
	Predict func(text string) (TurnTransition, bool)
}

// Proposal kinds carried by TurnTransition.
const (
	// TransitionKindWorkspace moves the session between authorities.
	TransitionKindWorkspace = "workspace"
	// TransitionKindAmbiguous requires an explicit workspace choice before the
	// request is admitted.
	TransitionKindAmbiguous = "ambiguous"
	// TransitionKindProposal is a request-derived decision about the execution
	// path. Authority does not move; the human resolves the decision through
	// the same permission surface a tool approval uses.
	TransitionKindProposal = "proposal"
)

// TurnTransition is the UI projection of one pending decision: a workspace
// transition or a request-derived proposal.
//
// It is a request, not a decision: the program stores it for presentation and
// acts only after the user answers. The workspace fields project
// workspace.TransitionProposal; Proposal projects workspace.Proposal. Both are
// projections, never second policies, and Strength stays a level rather than a
// score (I12).
type TurnTransition struct {
	// Kind names the decision. Empty is treated as a workspace transition so
	// a producer that predates proposals keeps its meaning.
	Kind string
	// Current and Proposed are workspace labels, e.g. "BUILD" and "ASK".
	// They are meaningful for the workspace kind only.
	Current  string
	Proposed string
	// CurrentAccent and ProposedAccent are the semantic workspace accents from
	// the owner, so the dialog never hardcodes a colour.
	CurrentAccent  string
	ProposedAccent string
	// Proposal is the request-derived decision for the proposal kind.
	Proposal *workspace.Proposal
	// Intent is the classified request, e.g. "READ".
	Intent string
	// Strength is the classifier's evidence level, e.g. "strong".
	Strength string
	// Question is the heading, e.g. "Switch to ASK?".
	Question string
	// Reason is the one-line explanation under the heading.
	Reason string
}

// ProposalResolution is a human's decided proposal: trusted control-plane
// state, not conversational text.
//
// It is recorded when the human resolves a proposal and consumed by the turn
// that resolution releases (plus explicit continuations of the same
// execution). Because it lives outside the transcript and the model input, the
// model can neither invent it nor reinterpret its provenance.
type ProposalResolution struct {
	// Option is the chosen option identity.
	Option string
	// Label is the human-facing choice.
	Label string
	// Directive is the execution constraint the choice establishes. Empty
	// means the human delegated the decision.
	Directive string
	// Dimension is the unresolved axis the human resolved.
	Dimension workspace.ProposalDimension
	// Subject is the requested work the question named.
	Subject string
	// Question is the exact question the human answered.
	Question string
	// Request is the exact user text this decision answers.
	Request string
}

// TurnAuthority is the workspace owner's validation result for one turn.
type TurnAuthority struct {
	// Active is the session's persistent workspace label, e.g. "BUILD".
	Active string
	// Intent is the classified request, e.g. "READ".
	Intent string
	// Blocked reports the turn must not run.
	Blocked bool
	// Reason explains a block in user-facing terms.
	Reason string
}

// SetWorkspaceSwitcher installs the owner behind /ask, /fuzz, /build.
func (p *Program) SetWorkspaceSwitcher(hooks WorkspaceHooks) {
	p.workspace = hooks
}

// ShellHooks wire the "!" escape to the local shell and to the authority
// classifier.
//
// Both are functions because the TUI owns neither the process runner nor the
// capability policy: the CLI does. A zero value disables the escape, which the
// program reports rather than silently sending the line to the model.
type ShellHooks struct {
	// Run executes a command in the workspace and streams output chunks.
	Run func(ctx context.Context, command string, onChunk func(string)) (exec.EscapeResult, error)
	// Mutates reports whether a command provably changes the workspace. It
	// gates the ASK confirmation; nil means "assume mutating", so ASK always
	// asks rather than running a possible mutation unconfirmed.
	Mutates func(command string) bool
	// Authorize is the security decision for a human-initiated command. When
	// set it replaces the Mutates/Decide path: allowed=false refuses with the
	// reason, confirm=true asks the human, and allowed=true/confirm=false runs
	// directly. The escape therefore crosses the same boundary as a tool call
	// instead of executing beside it.
	Authorize func(command string) (allowed bool, confirm bool, reason string)
}

// SetShellEscape installs the shell escape behind the "!" prefix.
func (p *Program) SetShellEscape(hooks ShellHooks) {
	p.shell = hooks
}

// modelsRefreshedMsg carries an async catalog refresh into Update.
type modelsRefreshedMsg struct {
	// seq pairs the result with its refresh generation.
	seq int
	// snapshot is the merged roster to show.
	snapshot CatalogSnapshot
	// errMsg carries failure as text; empty means success.
	errMsg string
}

// NewModelsRefreshedMsg packages an async catalog result for the Bubble Tea
// runtime. Refresh hooks live outside tui (which owns no network), so they
// report through this constructor rather than the unexported message.
func NewModelsRefreshedMsg(seq int, snapshot CatalogSnapshot, errMsg string) tea.Msg {
	return modelsRefreshedMsg{seq: seq, snapshot: snapshot, errMsg: errMsg}
}

// CatalogHooks feeds the /model picker with live catalog data.
//
// Snapshot must be cheap and synchronous (bundled plus cached rows);
// Refresh runs off the update goroutine and delivers through the returned
// command. Both are nil-safe: without them the picker serves the legacy
// listers with no freshness claims.
type CatalogHooks struct {
	// Snapshot returns the instant roster with its origin and staleness.
	Snapshot func() CatalogSnapshot
	// Refresh fetches the live catalog for seq and reports back a
	// modelsRefreshedMsg. It must not touch Program state: the message
	// path owns that.
	Refresh func(ctx context.Context, seq int) tea.Cmd
}

// SetCatalogHooks installs live catalog sourcing behind the /model picker.
func (p *Program) SetCatalogHooks(hooks CatalogHooks) {
	p.catalog = hooks
}

// ModelControlHooks feed and persist the /model control plane.
//
// They are functions because the program owns neither the catalog, the
// configuration authority, nor the runtime: the CLI does. Passing them in
// keeps the TUI free of durable configuration, which is the whole point of the
// split — an interactive change and a direct settings edit converge on the
// same authority because both go through it.
type ModelControlHooks struct {
	// Providers lists the provider pane, including the synthetic All row.
	Providers func() []ProviderRow
	// Roles returns the effective role assignments to display.
	Roles func() []RoleAssignment
	// ActiveRole names the role an Enter applies to by default (normally the
	// session's own role).
	ActiveRole func() string
	// Assign validates, persists through the configuration authority, and
	// applies an assignment to the runtime when it targets the active role.
	// It returns the applied assignment and the refreshed role roster, so the
	// UI never guesses what was accepted.
	Assign func(update RoleAssignment) (RoleAssignment, []RoleAssignment, error)
	// EffectiveWindow reports the runtime context window after an apply, so
	// the status line does not keep a stale budget. Optional.
	EffectiveWindow func() int
	// SaveKey persists an API key for a provider through the credential
	// authority (the file store). Nil means saving is unavailable; the
	// prompt reports it rather than losing the typed key silently.
	SaveKey func(provider, key string) error
}

// SetModelControl installs the model control plane behind /model. Without it,
// /model falls back to the legacy picker, so a caller that has not wired the
// plane keeps yesterday's behaviour rather than a broken surface.
func (p *Program) SetModelControl(hooks ModelControlHooks) {
	p.modelControl = hooks
}

// SetUsageLog installs the ledger behind /usage.
func (p *Program) SetUsageLog(log *agent.UsageLog) {
	p.usage = log
}

// SetCompactor installs the summarizer behind /compact.
func (p *Program) SetCompactor(compact Compactor) {
	p.compactor = compact
}

// SetRecovery installs the workspace recovery authority behind /undo and
// /redo, and the journal the loop uses to checkpoint agent edits.
//
// It is one seam because the two halves must agree: the journal records into
// the same session timeline that the commands move through.
func (p *Program) SetRecovery(recoverer Recoverer) {
	p.recovery = recoverer
}

// SetCommitHooks installs the dedicated /commit workflow. Both hooks are
// required for the command to run; a nil pair reports it unavailable rather
// than inventing a fallback through the normal agent path.
func (p *Program) SetCommitHooks(hooks CommitHooks) {
	p.commitHooks = hooks
}

// SetDoctorFunc installs diagnostics behind /doctor.
func (p *Program) SetDoctorFunc(doctor DoctorFunc) {
	p.doctor = doctor
}

// SetProvidersFunc installs the credential roster behind the /usage
// inspector's provider status section.
func (p *Program) SetProvidersFunc(providers ProvidersFunc) {
	p.providers = providers
}

// SetProbeFunc installs the endpoint check that /usage runs when it opens.
func (p *Program) SetProbeFunc(probe ProbeFunc) {
	p.probe = probe
}

// SetSymbolStatusFunc installs the optional code-index status reporter behind
// the /status card's SYMBOL ENGINE section. Nil (the default) hides the
// section: an engine the runtime does not have is never claimed.
func (p *Program) SetSymbolStatusFunc(status func() StatusEngine) {
	p.symbolStatus = status
}

// SetMCPHooks installs the MCP inspector behind /mcp.
func (p *Program) SetMCPHooks(hooks MCPHooks) {
	p.mcp = hooks
}

// SetCredentialDeleter installs removal behind /disconnect.
func (p *Program) SetCredentialDeleter(deleter CredentialDeleter) {
	p.deleter = deleter
}

// SetConnectHooks installs the guided setup behind /connect.
func (p *Program) SetConnectHooks(hooks ConnectHooks) {
	p.connect = hooks
}

// ProgramOption configures a Program at construction.
type ProgramOption func(*Program)

// PromptHistory is the durable store behind up-arrow recall and inline
// suggestions.
//
// The program only needs the loaded entries and an append hook, so a test can
// substitute a fake and a failed file never has to be simulated.
type PromptHistory interface {
	// Entries returns the persisted prompts, oldest first.
	Entries() []string
	// Add records a submitted prompt. It must not block the update
	// goroutine on file I/O.
	Add(prompt string)
}

// promptHistoryFlusher is implemented by stores that buffer appends. Shutdown
// flushes them so a prompt submitted immediately before quit is not lost.
type promptHistoryFlusher interface {
	Flush() error
}

// WithPromptHistory seeds recallable history from a durable store and records
// every submitted prompt back into it.
//
// Loading happens here, before the first frame, so the very first up arrow in
// a new session already reaches the previous session's prompts.
func WithPromptHistory(store PromptHistory) ProgramOption {
	return func(p *Program) {
		if store == nil {
			return
		}
		p.promptHistory = store
		p.state.Submitted = append(p.state.Submitted, store.Entries()...)
	}
}

// WithMouseReporting makes the program request mouse reporting from the
// terminal, so wheel ticks arrive as real mouse events instead of as arrow
// keys.
//
// This is opt-in because it costs something real: once the terminal sends mouse
// events it stops doing its own text selection, so drag-select and copy stop
// working without a modifier (usually Shift, or Option on macOS). The default
// keeps the terminal in charge of selection and reaches the wheel through
// alternate scroll mode instead — see Run for why that is not always enough.
func WithMouseReporting() ProgramOption {
	return func(p *Program) { p.mouseReporting = true }
}

// WithVersion records the binary release for the chrome: the header brands
// as "ohgo v1.0.0" and the welcome frame titles itself the same way. Empty
// hides the label rather than rendering a placeholder.
func WithVersion(v string) ProgramOption {
	return func(p *Program) { p.state.Status.Version = strings.TrimSpace(v) }
}

// NewProgram returns a program driving the given loop.
func NewProgram(loop *agent.Loop, model string, window int, opts ...ProgramOption) *Program {
	catalogCtx, catalogCancel := context.WithCancel(context.Background())
	p := &Program{
		loop:           loop,
		events:         make(chan Event, 256),
		approvals:      make(chan approvalRequest, 8),
		sessionAllowed: make(map[string]struct{}),
		done:           make(chan struct{}),
		window:         window,
		termOut:        os.Stdout,
		traceFn:        traceWriterFromEnv(),
		catalogCtx:     catalogCtx,
		catalogCancel:  catalogCancel,
	}
	// State first, options after: an option records into the state the
	// program actually runs (model, version), rather than a zero value
	// the constructor then overwrites.
	p.state = New()
	for _, opt := range opts {
		opt(p)
	}
	p.state.Status.Model = model
	p.state.Status.ContextWindow = window
	// OSC 52 is the only clipboard transport. A dumb terminal or an explicit
	// opt-out disables it; the copy is then a no-op rather than a spawned
	// process or a false success.
	term := os.Getenv("TERM")
	if ClipboardAvailable(term) && !strings.EqualFold(os.Getenv("OHGO_CLIPBOARD"), "off") {
		tmux := os.Getenv("TMUX") != ""
		p.clipboard = func(text string) string { return OSC52SequenceFor(text, tmux, term) }
	}
	return p
}

// Init implements tea.Model.
func (p *Program) Init() tea.Cmd {
	return tea.Batch(p.waitForEvent(), p.waitForApproval(), tick())
}

// Update implements tea.Model.
//
// It is a thin shell around update so live mode folds one state change into
// one output decision: the rows that have just become history leave as print
// commands, and the live region is left for the renderer to paint. Folding
// inside the message handlers would emit several frames per keystroke.
func (p *Program) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := p.update(msg)
	if p.live != nil {
		if history := p.live.render(p.state); len(history) > 0 {
			cmd = tea.Batch(cmd, printTranscript(history))
		}
	}
	return model, cmd
}

func (p *Program) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		return p.handleKey(m)

	case teaEvent:
		p.apply(m.event)
		return p, p.waitForEvent()

	case approvalPrompt:
		// A tool is now blocked. Record which one so the answer can be routed.
		request := m.request
		p.pending = &request
		p.state.Apply(ApprovalAsked{
			ToolName:        request.toolName,
			Reason:          request.reason,
			Summary:         request.summary,
			MutationPreview: request.preview,
			Mutating:        request.mutating,
			Choices:         approvalChoices,
		})
		return p, p.waitForApproval()

	case tickMsg:
		// The commit spinner shares the one spinner index and the one tick.
		// Advancing it here keeps the activity line animating without a second
		// timer, and the tick itself never blocks the update goroutine.
		if p.state.Status.Busy || p.state.SupportBusy() || p.state.CommitActive() {
			p.state.Status.Spinner = AdvanceSpinner(p.state.Status.Spinner, len(spinnerFrames))
		}
		return p, tick()

	case turnFinishedMsg:
		return p.drainQueue()

	case cursor.BlinkMsg:
		// The wizard's inputs blink through the same message the
		// textinput runtime produces; anything else has no caret.
		if p.wizard != nil && !p.wizard.Done() && !p.wizard.Canceled() {
			updated, cmd := p.wizard.Update(msg)
			p.wizard = updated
			return p, cmd
		}
		return p, nil

	case modelsRefreshedMsg:
		return p.onModelsRefreshed(m)

	case updateFinishedMsg:
		return p.onUpdateFinished(m)

	case commitProposedMsg:
		return p.onCommitProposed(m)

	case commitFinishedMsg:
		return p.onCommitFinished(m)

	case copyNoticeExpiredMsg:
		// A stale expiry never clears a newer confirmation: the generation
		// pins the visible status to the latest successful copy.
		p.state.clearCopyNotice(m.gen)
		return p, nil

	case tea.WindowSizeMsg:
		p.state.Apply(TerminalResized{Width: m.Width, Height: m.Height})
		p.trace(traceResize)
		p.syncDiffSize()
		return p, nil

	case tea.MouseMsg:
		if p.live != nil {
			// Output-first mode never requests a mouse mode, so the terminal
			// never sends these and the handler below is unreachable here.
			// Isolating it explicitly keeps the alt-screen path untouched
			// while making sure it cannot quietly become part of a boundary
			// that says the terminal owns the wheel.
			return p, nil
		}
		return p.handleMouse(m)
	}
	// Anything else may belong to the inspector's asynchronous fetches. It is
	// forwarded only while the inspector is open, so a stray message for a
	// closed model is dropped.
	if p.state.Diff != nil {
		return p.handleDiffMsg(msg)
	}
	return p, nil
}

// mouseScrollLines is one wheel tick in transcript rows: enough to feel
// like scrolling, small enough to keep context. Follow semantics come
// from ScrollMoved itself — scrolling up clears follow, scrolling back to
// the bottom restores it — so the wheel cannot break sticky-follow.
const mouseScrollLines = 3

// wheelScrolls reports whether the arrow keys must act as wheel ticks.
//
// Run serves the wheel through alternate scroll mode, which has the terminal
// send plain arrow keys the program cannot tell apart from pressed ones. The
// arrows are therefore given to the transcript only while the composer has
// nothing to do with them — no text to move a cursor within, and no overlay
// owning the keyboard. That is the state a reader is in, and the one where
// scrolling is what the user means. Typing keeps the arrows for editing.
func wheelScrolls(s State) bool {
	if s.Picker != nil || s.Plane != nil || s.ConnectKey != nil || s.Pending != nil || s.QuitConfirm != nil || s.Transition != nil || s.SupportPanel != nil || s.Permissions != nil || s.Diff != nil || s.HelpModal != nil || s.StatusModal != nil || s.UsageModal != nil || s.InfoModal != nil || s.InformationModal != nil {
		return false
	}
	return s.Input == ""
}

// composerMoveWidth is the wrap width the active composer renderer folds to.
// The full-screen composer subtracts the live mode prefix ("ask > " is wider
// than "> "), while live mode draws a fixed two-cell prompt. Movement must use
// the same width the visible rows used, or up/down hop rows that are not there.
func (p *Program) composerMoveWidth() int {
	layout := ComputeLayout(p.state)
	if p.live != nil {
		return liveComposerWidth(layout)
	}
	return composerTextWidthFor(p.state, layout)
}

// composerLineAbove moves the cursor one display row up, wrapping to the width
// the active renderer uses. It reports false on the top row so the key handler
// can fall back to history recall. A landing inside a chip label snaps to its
// edge so a pill is never split.
func (p *Program) composerLineAbove() (int, bool) {
	pos, ok := lineAboveWrapped(p.state.Input, p.state.Cursor, p.composerMoveWidth())
	if !ok {
		return pos, false
	}
	return snapPasteBoundary(p.state.Pastes, p.state.Input, pos), true
}

// composerLineBelow moves the cursor one display row down, reporting false on
// the bottom row.
func (p *Program) composerLineBelow() (int, bool) {
	pos, ok := lineBelowWrapped(p.state.Input, p.state.Cursor, p.composerMoveWidth())
	if !ok {
		return pos, false
	}
	return snapPasteBoundary(p.state.Pastes, p.state.Input, pos), true
}

// handleMouse routes pointer input. Wheel ticks scroll the region in focus
// (the picker selection while one is open, the transcript otherwise). When
// mouse reporting is enabled, press/motion/release belong to the application
// selection and hit-test paths; in the default selection mode the terminal
// keeps drag-copy and these events never arrive.
func (p *Program) handleMouse(m tea.MouseMsg) (tea.Model, tea.Cmd) {
	if p.wizard != nil {
		// Onboarding owns the keyboard and the canvas: a wheel tick must
		// not scroll a transcript that is not on screen.
		return p, nil
	}
	// Application-owned pointer interaction. Selection mode never receives
	// these events; reporting mode maps them through the current layout.
	if m.Button != tea.MouseButtonWheelUp && m.Button != tea.MouseButtonWheelDown {
		_, cmd := p.handleTranscriptMouse(m)
		return p, cmd
	}
	// The inspector owns the wheel while it is open: a tick moves the tree
	// selection or scrolls the hunk viewport, never the hidden transcript.
	if p.state.Diff != nil {
		keyType := tea.KeyDown
		if m.Button == tea.MouseButtonWheelUp {
			keyType = tea.KeyUp
		}
		return p.handleDiffKey(tea.KeyMsg{Type: keyType})
	}
	switch m.Button {
	case tea.MouseButtonWheelUp:
		if p.state.SupportPanel != nil {
			p.state.Apply(SupportScrolled{Delta: mouseScrollLines})
			return p, nil
		}
		if p.state.Plane != nil {
			p.state.Apply(PlaneMoved{Delta: -1})
			return p, nil
		}
		if p.state.HelpModal != nil {
			p.state.Apply(HelpCenterMoved{Delta: -1})
			return p, nil
		}
		if p.state.Picker != nil {
			p.state.Apply(PickerMoved{Delta: -1})
			return p, nil
		}
		p.state.Apply(ScrollMoved{Delta: mouseScrollLines})
		return p, nil
	case tea.MouseButtonWheelDown:
		if p.state.SupportPanel != nil {
			p.state.Apply(SupportScrolled{Delta: -mouseScrollLines})
			return p, nil
		}
		if p.state.Plane != nil {
			p.state.Apply(PlaneMoved{Delta: 1})
			return p, nil
		}
		if p.state.HelpModal != nil {
			p.state.Apply(HelpCenterMoved{Delta: 1})
			return p, nil
		}
		if p.state.Picker != nil {
			p.state.Apply(PickerMoved{Delta: 1})
			return p, nil
		}
		p.state.Apply(ScrollMoved{Delta: -mouseScrollLines})
		return p, nil
	}
	return p, nil
}

// onModelsRefreshed folds an async catalog result into the open picker.
//
// A result for a closed (or non-model) picker is dropped, as is any result
// whose generation trails the latest issued refresh: an older, slower
// response must never overwrite newer rows. A failure keeps the cached rows
// and records the reason on the picker.
func (p *Program) onModelsRefreshed(m modelsRefreshedMsg) (tea.Model, tea.Cmd) {
	if m.seq != p.modelSeq {
		return p, nil
	}
	// The refresh belongs to the program, not to one open picker. Clear the
	// in-flight marker even when the user closed the picker while it ran, or
	// the next /model would wait behind a refresh nobody is showing.
	p.modelRefreshing = false
	relevant := (p.state.Picker != nil && p.state.Picker.Kind == PickerModels) || p.state.Plane != nil
	if !relevant {
		return p, nil
	}
	// A refresh supplies catalog facts, not runtime authority: recompute the
	// active marker from what the loop is actually calling so replacing the
	// roster cannot erase the current-model highlight.
	if p.currentModel != nil && len(m.snapshot.Options) > 0 {
		active := p.currentModel()
		options := append([]ModelOption(nil), m.snapshot.Options...)
		for i := range options {
			options[i].Active = options[i].ID == active
		}
		m.snapshot.Options = options
	}
	p.apply(ModelsRefreshed{Seq: m.seq, Snapshot: m.snapshot, Err: m.errMsg})
	return p, nil
}

// apply folds an event into the state and reacts to the ones the program owns.
func (p *Program) apply(event Event) {
	p.state.Apply(event)
	// A prompt joins durable history only when it actually becomes a turn.
	// Drafts, ghost suggestions, queued lines that never run, and slash or
	// shell commands are not prompts and must not pollute recall.
	if submitted, ok := event.(UserSubmitted); ok && p.promptHistory != nil {
		p.promptHistory.Add(submitted.Text)
	}
	// Validation results pair with the pending setup flow here, on the update
	// goroutine, where the flow lives.
	if result, ok := event.(ValidationFinished); ok {
		p.onValidationFinished(result)
	}
}

// handleKey processes a keypress.
//
// The wizard owns the keyboard while open: onboarding must finish before
// any turn, command, or overlay can start underneath it. While an approval
// prompt is open, keys answer it rather than compose a line: a tool is
// blocked on that answer, so the prompt has to be modal.
func (p *Program) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if p.wizard != nil {
		return p.handleWizardKey(msg)
	}
	// A transition proposal owns the keyboard until it is decided: no turn may
	// start while it is open, so no provider request, tool, or mutation can run
	// before the human answers (I4-I6). Left/right select; Esc and Ctrl+C are
	// the conservative exits and never move authority.
	if p.state.Transition != nil {
		switch msg.String() {
		case "left", "h", "shift+tab":
			p.state.Apply(TransitionMoved{Delta: -1})
			return p, nil
		case "right", "l", "tab":
			p.state.Apply(TransitionMoved{Delta: 1})
			return p, nil
		case "enter":
			return p.resolveTransition(p.state.Transition.Selected())
		case "esc":
			// Esc resolves the proposal the way an approval's esc resolves a
			// tool: it declines the proposed action (the switch) and lets the
			// turn continue under the current workspace. Authority is never
			// gained by declining (I3).
			return p.resolveTransition(false)
		case "ctrl+c":
			// The reflex abort abandons the pending turn instead of running
			// it. The typed prompt stays in the composer, so nothing is lost.
			p.state.Apply(TransitionResolved{})
			return p, nil
		}
		return p, nil
	}
	// Quit confirmation wins over a blocked tool: confirming /q must be able
	// to deny the tool and exit, while dismissing leaves the tool waiting.
	if p.state.QuitConfirm != nil {
		switch msg.String() {
		case "up", "k", "shift+tab", "left", "h":
			p.state.Apply(QuitConfirmMoved{Delta: -1})
			return p, nil
		case "down", "j", "tab", "right", "l":
			p.state.Apply(QuitConfirmMoved{Delta: 1})
			return p, nil
		case "enter":
			if isAffirmative(p.state.QuitConfirm.Selected()) {
				return p, p.quitNow()
			}
			p.state.Apply(QuitConfirmClosed{})
			return p, nil
		case "y", "Y":
			return p, p.quitNow()
		case "n", "N", "esc":
			p.state.Apply(QuitConfirmClosed{})
			return p, nil
		case "ctrl+c":
			// The reflex abort key must never quit: it dismisses the
			// confirmation and, when a turn is running, cancels the work
			// the same way an idle esc would.
			p.state.Apply(QuitConfirmClosed{})
			if p.state.Status.Busy {
				p.cancelTurnNow("cancelled")
			}
			return p, nil
		}
		return p, nil
	}

	// The commit editor owns Enter and the conservative exits. Every other
	// key falls through to the ordinary composer, so editing feels like
	// typing a prompt rather than using a second text surface.
	if p.commitEditing && !p.otherOverlayOpen() {
		switch msg.String() {
		case "enter":
			return p.submitCommitEdit()
		case "esc", "ctrl+c":
			p.cancelCommitEdit()
			return p, nil
		}
	}

	// A proposal generation is an interruptible read-only operation. Esc and
	// Ctrl+C cancel it immediately; the sequence guard makes the later answer
	// harmless. An open overlay keeps its own keys.
	if p.commitProposing && !p.otherOverlayOpen() && (msg.String() == "esc" || msg.String() == "ctrl+c") {
		p.cancelCommitProposal()
		return p, nil
	}

	if p.state.Pending != nil {
		if p.state.Pending.Commit != nil {
			return p.handleCommitKey(msg)
		}
		if p.state.Pending.IsProposal() {
			return p.handleProposalKey(msg)
		}
		switch msg.String() {
		case "up", "k", "shift+tab", "left", "h":
			p.state.Apply(ApprovalMoved{Delta: -1})
			return p, nil
		case "down", "j", "tab", "right", "l":
			p.state.Apply(ApprovalMoved{Delta: 1})
			return p, nil
		case "enter":
			return p.answerSelection(p.state.Pending.Selected())
		case "y", "Y":
			return p.answer(true, "")
		case "n", "N", "esc":
			return p.answer(false, "denied by user")
		case "ctrl+c":
			// The reflex abort key must not be swallowed by this switch: the
			// turn would stay parked on a question nobody can reach. A shell
			// confirmation has no turn to cancel, so it is denied instead.
			if p.pendingEscape != "" {
				return p.answer(false, "cancelled")
			}
			// Reuse the turn-cancel path, which denies the blocked tool
			// before cancelling, so aborting can never approve the work it
			// abandons.
			p.cancelTurnNow("cancelled")
			return p, nil
		}
		return p, nil
	}

	if p.state.Diff != nil {
		return p.handleDiffKey(msg)
	}

	if p.state.Plane != nil {
		// The support overlay never stacks with the model control plane:
		// /model is unavailable until support closes.
		if p.state.SupportPanel != nil {
			return p, nil
		}
		return p.handlePlaneKey(msg)
	}

	if p.state.Permissions != nil {
		return p.handlePermissionsKey(msg)
	}

	if p.state.StatusModal != nil {
		return p.handleStatusKey(msg)
	}

	if p.state.UsageModal != nil {
		return p.handleUsageKey(msg)
	}

	if p.state.InfoModal != nil || p.state.InformationModal != nil {
		return p.handleInfoKey(msg)
	}
	if p.state.SessionModal != nil {
		return p.handleSessionManagerKey(msg)
	}

	if p.state.ConnectModal != nil {
		return p.handleConnectKey(msg)
	}

	if p.state.ConnectKey != nil {
		return p.handleConnectKeyEntry(msg)
	}

	if p.state.HelpModal != nil {
		return p.handleHelpCenterKey(msg)
	}

	if p.state.Picker != nil {
		return p.handlePickerKey(msg)
	}

	// The support overlay owns interaction while open: printable input
	// goes to the support composer, and no workspace turn, slash command,
	// or overlay can start underneath it.
	if p.state.SupportPanel != nil {
		return p.handleSupportKey(msg)
	}

	// The "@" trigger needs its workspace roster before the palette can show
	// file rows. Install it once, lazily, so a session that never references
	// a file never walks the tree.
	p.ensureMentionRoster()

	// The inline palette owns its keys while visible: navigating, accepting,
	// or dismissing suggestions must not leak into history, line movement, or
	// submission. Enter and Tab both accept the highlighted row; the text is
	// spliced into the composer, so the same keys mean the same thing for
	// every trigger.
	if p.state.ConnectKey == nil && p.state.Pending == nil && p.state.QuitConfirm == nil &&
		paletteOwnsKeys(p.state) {
		switch msg.String() {
		case "up", "ctrl+p":
			p.state.Apply(PaletteMoved{Delta: -1})
			return p, nil
		case "down", "ctrl+n":
			p.state.Apply(PaletteMoved{Delta: 1})
			return p, nil
		case "esc":
			// Dismiss keeps the draft: the text stays, only the list hides
			// until the next keystroke.
			p.state.PaletteHide = true
			return p, nil
		case "enter", "tab":
			return p.acceptPalette()
		}
	}

	switch msg.String() {
	case "ctrl+o":
		// Expand or collapse the latest activity block. Presentation-only:
		// the toggle flips the block's Expanded flag and never re-runs
		// the tool, mutates its result, or touches approval state. A
		// modal overlay owns the keyboard while open, so this is a no-op
		// until it closes.
		id := p.state.LatestToolID()
		if id == "" {
			return p, nil
		}
		expanding := true
		if tool := p.state.tool(id); tool != nil {
			expanding = !tool.Expanded
		}
		p.state.Apply(ToolToggled{ID: id})
		if p.live != nil && expanding {
			// Output-first owns sealed lines: a still-live block
			// re-renders in place on the next frame, but an
			// already-sealed one cannot be rewritten, so its bounded
			// expanded projection prints once below as new scrollback.
			// Collapsing flips state only — sealed lines stay where the
			// terminal put them.
			if idx := p.state.toolIndex(id); idx >= 0 && idx < p.live.painted {
				if lines := expandedToolLines(p.state, id); len(lines) > 0 {
					return p, printTranscript(lines)
				}
			}
		}
		return p, nil

	case "ctrl+s":
		// Ctrl+S opens $sup without going through the composer, so a prompt
		// already being written is never repurposed as a command. The draft
		// survives: the chord never clears the main input (see
		// openSupportChord). Raw mode disables IXON, so the chord arrives as
		// a key event rather than a terminal XOFF; a terminal that still
		// traps flow control needs `stty -ixon` to deliver it.
		p.openSupportChord()
		return p, nil

	case "ctrl+c":
		return p.interruptOrClear()

	case "esc":
		// Escape backs out of a running turn the way ctrl+c does, but never
		// quits: a stray keypress must not end the session. (An open
		// support overlay routes to its own handler before reaching here.)
		if p.state.Status.Busy {
			p.cancelTurnNow("cancelled")
			return p, nil
		}
		return p, nil

	case "enter":
		text := strings.TrimSpace(expandPastes(p.state.Pastes, p.state.Input))
		if text == "" {
			// An empty line still advances queued work: the queue waits for a
			// turn boundary, and this is one.
			return p.drainQueue()
		}
		if command, ok := ParseCommand(text); ok {
			return p, p.runCommand(command)
		}
		// A "!" line is a direct shell escape, not a model prompt: it runs
		// in the workspace under the active authority.
		if command, ok := exec.ParseEscape(text); ok {
			return p, p.submitEscape(command)
		}
		if p.state.Status.Busy {
			p.queue(text)
			return p, nil
		}
		return p, p.submit(text)

	case "alt+enter", "ctrl+j":
		// The one way to write a newline: enter always submits, so multiline
		// prompts need an explicit chord.
		p.state.Apply(ComposerChanged{Text: insertAt(p.state.Input, p.state.Cursor, "\n"), Cursor: p.state.Cursor + 1})
		return p, nil

	case "backspace", "ctrl+h":
		if text, cursor, _, ok := pasteDeleteBefore(p.state.Pastes, p.state.Input, p.state.Cursor); ok {
			// A chip is one token: backspace removes the whole pill, and the
			// payload is pruned with it.
			p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
			return p, nil
		}
		text, cursor := deleteBefore(p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "delete", "ctrl+d":
		// Ctrl+d is EOF: with an empty composer and an idle runtime it ends
		// the session the way a REPL does. With any draft present it stays
		// delete-forward, so the chord can never silently discard typed text.
		if msg.String() == "ctrl+d" && p.state.Input == "" && !p.state.Status.Busy {
			return p, p.quitNow()
		}
		if text, cursor, _, ok := pasteDeleteAt(p.state.Pastes, p.state.Input, p.state.Cursor); ok {
			p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
			return p, nil
		}
		text, cursor := deleteAt(p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "left", "ctrl+b":
		p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: cursorLeftAtomic(p.state.Pastes, p.state.Input, p.state.Cursor)})
		return p, nil

	case "right", "ctrl+f":
		// Right arrow at the end of the input accepts a visible history
		// completion; ctrl+f stays a pure cursor movement so the emacs-style
		// binding never rewrites the line.
		if msg.String() == "right" && p.acceptInlineSuggestion() {
			return p, nil
		}
		p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: cursorRightAtomic(p.state.Pastes, p.state.Input, p.state.Cursor)})
		return p, nil

	case "home", "ctrl+a":
		// With an idle composer (the reader state), Home scrolls the
		// transcript to the top. Typing keeps it as the line-start chord.
		if msg.String() == "home" && wheelScrolls(p.state) {
			p.syncTranscript()
			scroll := p.state.scrollIntent
			if scroll == 0 {
				scroll = p.state.Scroll
			}
			if top := p.transcriptFrame.vp.MaxOffset; top > 0 && top > scroll {
				p.state.Apply(ScrollMoved{Delta: top - scroll})
				return p, nil
			}
		}
		p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: lineStartAtomic(p.state.Pastes, p.state.Input, p.state.Cursor)})
		return p, nil

	case "end", "ctrl+e":
		// The transcript names "end" in its scrolled-up marker, so while
		// scrolled up it re-engages the live edge; otherwise (and always
		// for ctrl+e) it moves the composer cursor to end of line.
		if msg.String() == "end" && !p.state.Follow {
			p.state.Apply(ScrollToBottom{})
			return p, nil
		}
		p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: lineEndAtomic(p.state.Pastes, p.state.Input, p.state.Cursor)})
		return p, nil

	case "ctrl+u":
		text, cursor := killToStartAtomic(p.state.Pastes, p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "ctrl+k":
		text, cursor := killToEndAtomic(p.state.Pastes, p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "ctrl+w":
		text, cursor := killWordBackAtomic(p.state.Pastes, p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "up", "ctrl+p":
		// On a fresh session there is nothing to recall, so ^p is the
		// settings/discovery hub the shortcut bar advertises. The moment a
		// prompt exists (or history does) the bar stops promising it and the
		// key returns to history recall, so the hint can never lie.
		if msg.String() == "ctrl+p" && !p.state.SessionActive && len(p.state.Submitted) == 0 {
			p.openHelpCenter("")
			return p, nil
		}
		// The terminal cannot say which arrows it synthesised for the wheel,
		// so the policy is state-based: an empty composer hands the arrows to
		// the transcript — the state a reader is in — but only while there is
		// nothing to recall. Once history exists the arrow recalls it, which
		// is the reason most people reach for up in the first place. ctrl+p
		// and ctrl+n reach history whether or not the wheel does.
		if msg.String() == "up" && wheelScrolls(p.state) {
			if len(p.state.Submitted) > 0 {
				p.state.Apply(HistoryMoved{Delta: 1})
				return p, nil
			}
			p.state.Apply(ScrollMoved{Delta: mouseScrollLines})
			return p, nil
		}
		// Inside the text, up moves a display row; on the top row it falls
		// back to history, so a multi-line draft never traps the cursor.
		if line, ok := p.composerLineAbove(); ok {
			p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: line})
		} else {
			p.state.Apply(HistoryMoved{Delta: 1})
		}
		return p, nil

	case "down", "ctrl+n":
		if msg.String() == "down" && wheelScrolls(p.state) {
			p.state.Apply(ScrollMoved{Delta: -mouseScrollLines})
			return p, nil
		}
		if line, ok := p.composerLineBelow(); ok {
			p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: line})
		} else {
			p.state.Apply(HistoryMoved{Delta: -1})
		}
		return p, nil

	case "pgup":
		p.state.Apply(ScrollMoved{Delta: p.pageStep()})
		return p, nil

	case "pgdown":
		p.state.Apply(ScrollMoved{Delta: -p.pageStep()})
		return p, nil

	case "tab":
		// Tab accepts an inline history completion before it considers any
		// other meaning. A visible ghost is the most specific available
		// action, so it wins over command discovery; the palette path below
		// only runs when no suggestion is offered.
		if p.acceptInlineSuggestion() {
			return p, nil
		}
		// Tab on a leading slash discovers commands instead of inserting a
		// literal tab: a slash line is either a command or a typo, and the
		// palette shows which without spending a turn. The typed text is
		// saved and restored if the palette closes without choosing.
		if strings.HasPrefix(strings.TrimSpace(p.state.Input), "/") {
			filter := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.state.Input), "/"))
			p.pickerRestore = p.state.Input
			p.pickerRestoreSet = true
			p.state.Apply(InputChanged{Text: ""})
			p.openHelpCenter(filter)
			return p, nil
		}
		return p, nil

	default:
		// Space arrives as KeySpace, not KeyRunes: without this branch no
		// space can ever be typed into the composer.
		switch msg.Type {
		case tea.KeyRunes, tea.KeySpace:
			text := string(msg.Runes)
			if msg.Type == tea.KeySpace {
				text = " "
			}
			// "?" on an empty composer opens contextual help from the
			// authoritative registry. While composing it stays literal:
			// help must never steal a question mark being typed.
			if text == "?" && strings.TrimSpace(p.state.Input) == "" && p.state.ConnectKey == nil {
				p.openHelpCenter("")
				return p, nil
			}
			// A large bracketed paste folds into one inline chip: the full
			// payload is retained for submission while the composer stays a
			// single row. Short pastes, and keystroke streams from terminals
			// without bracketed paste, stay literal.
			if shouldCollapsePaste(msg.Paste, text) {
				p.state.Apply(PasteCollapsed{Text: text, Lines: pasteLineCount(text)})
				return p, nil
			}
			// KeyRunes (typed or pasted, including bracketed pastes) is user
			// text: it inserts verbatim at the cursor. The application must
			// not second-guess prose that resembles protocol bytes.
			composed := insertAt(p.state.Input, p.state.Cursor, text)
			cursor := p.state.Cursor + len([]rune(text))
			p.state.Apply(ComposerChanged{Text: composed, Cursor: cursor})
			// The insertion may have just opened a trigger word (a typed or
			// pasted "@"), so install the roster before the next render.
			p.ensureMentionRoster()
		}
		return p, nil
	}
}

// ensureMentionRoster installs the "@" workspace roster the first time the
// cursor enters an "@" token. It is idempotent: the walk runs at most once per
// session, and a nil lister leaves "@" literal.
func (p *Program) ensureMentionRoster() {
	if p.mentionsLoaded || p.listMentions == nil {
		return
	}
	trigger, _, _, _, ok := paletteToken(p.state.Input, p.state.Cursor)
	if !ok || trigger != paletteFile {
		return
	}
	p.mentionsLoaded = true
	p.state.Apply(PaletteFilesLoaded{Files: p.listMentions()})
}

// acceptInlineSuggestion replaces the live input with the inline history
// suggestion and reports whether one was accepted.
//
// The whole prompt is written, not just the visible tail: the tail is derived
// from the input at paint time, so a user who accepted before the next frame
// would otherwise keep only the prefix.
func (p *Program) acceptInlineSuggestion() bool {
	full := p.state.inlineSuggestion()
	if full == "" {
		return false
	}
	p.state.Apply(ComposerChanged{Text: full, Cursor: len([]rune(full))})
	return true
}

// acceptPalette resolves Enter or Tab on the inline palette: the highlighted
// row's token is spliced into the composer over the trigger word.
//
// A "/" word that is already an exact known command dispatches exactly like
// submitting it, so a second Enter still runs what the user has now seen. Every
// other case completes the highlighted row and hides the palette until the next
// keystroke, which re-opens it if the cursor is still inside a trigger word.
func (p *Program) acceptPalette() (tea.Model, tea.Cmd) {
	entries := paletteEntriesFor(p.state)
	if len(entries) == 0 {
		return p, nil
	}
	trigger, _, start, end, ok := paletteToken(p.state.Input, p.state.Cursor)
	if !ok {
		return p, nil
	}
	if trigger == paletteCommand {
		if command, ok := ParseCommand(strings.TrimSpace(p.state.Input)); ok {
			if _, known := LookupCommand(command.Name); known && len(command.Args) == 0 {
				return p, p.runCommand(command)
			}
		}
	}
	cur := ClampPaletteCursor(p.state.PaletteCursor, len(entries))
	text, cursor := SpliceToken(p.state.Input, start, end, entries[cur].Insert)
	p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
	// Hide until the next keystroke: the spliced token still holds the cursor,
	// so without this the palette would immediately re-open over the row just
	// accepted.
	p.state.PaletteHide = true
	return p, nil
}

// pageStep scrolls by half the viewport, or a fixed step when the size is
// unknown (a test, or a terminal that never reported it).
func (p *Program) pageStep() int {
	if p.state.Height > 4 {
		return p.state.Height / 2
	}
	return 10
}

// supportPageStep scrolls the $sup response by half the dialog's content
// height, so one page keeps context. Unknown sizes fall back to the
// transcript step.
func (p *Program) supportPageStep() int {
	if p.state.Width > 0 && p.state.Height > 0 {
		rect := SupportModalRect(p.state.Width, p.state.Height)
		if contentH := rect.Height - 3; contentH > 4 {
			return contentH / 2
		}
	}
	return p.pageStep()
}

// runCommand dispatches a slash command.
//
// A command that ends the session returns tea.Quit; one that only changes state
// returns nil so the input stays editable. An unknown command is reported
// rather than silently forwarded to the model, which would burn tokens on a
// typo.
func (p *Program) runCommand(command Command) tea.Cmd {
	p.state.Apply(InputChanged{Text: ""})

	switch command.Name {
	case "quit", "exit":
		return p.quitNow()

	case "q":
		p.openQuitConfirm()
		return nil

	case "ask", "fuzz", "build":
		p.switchWorkspace(command.Name)
		if len(command.Args) > 0 {
			objective := strings.Join(command.Args, " ")
			if p.state.Status.Busy {
				p.queue(objective)
				return nil
			}
			return p.submit(objective)
		}
		return nil

	case "permissions", "mode":
		p.openPermissions()
		return nil

	case "help":
		p.openHelpCenter("")
		return nil

	case "clear":
		p.state.Apply(Cleared{})
		if p.live != nil {
			// The scrollback keeps the lines that were there; the painter
			// must not try to reconcile its progress against them.
			p.live.forget()
		}
		p.mu.Lock()
		p.history = nil
		// A cleared session starts a fresh telemetry window: the helper bar
		// returns to shortcuts and the next turn's counts begin at zero.
		p.resetSessionTelemetryLocked()
		p.mu.Unlock()
		return nil

	case "model":
		// No nested modals: /model stays unavailable until the support
		// panel closes, so one overlay never stacks on another.
		if p.state.SupportPanel != nil {
			p.state.Apply(Notice{Text: "close support first (Esc) before /model"})
			return nil
		}
		if len(command.Args) == 0 {
			if p.modelControl.Assign != nil {
				return p.openModelControl()
			}
			return p.openModelPicker()
		}
		p.applyModelSwitch(strings.Join(command.Args, ""))
		return nil

	case "sessions", "resume":
		p.openSessionManager()
		return nil

	case "new":
		p.openNewSessionPicker(command.Args)
		return nil

	case "jobs":
		p.openJobsView()
		return nil

	case "task":
		p.taskCommand(command.Args)
		return nil

	case "audit":
		if p.task.Audit == nil {
			p.state.Apply(Notice{Text: "context audit is unavailable in this session"})
			return nil
		}
		p.state.Apply(Notice{Text: p.task.Audit()})
		return nil

	case "theme":
		p.applyThemeCommand(command.Args)
		return nil

	case "models":
		return p.openModelPicker()

	case "commands":
		p.openHelpCenter("")
		return nil

	case "status":
		p.openStatus()
		return nil

	case "update":
		// /update is the human's explicit authorization. Observation events
		// never reach this branch; they only change header chrome.
		return p.startUpdate()

	case "usage":
		p.openUsage()
		return nil

	case "info", "about", "documentation", "docs", "changelog":
		p.openInfo()
		return nil

	case "doctor":
		p.runDoctor()
		return nil

	case "mcp":
		return p.mcpCommand(command.Args)

	case "diff":
		return p.openDiff(command.Args)

	case "commit":
		return p.startCommit(command.Args)

	case "retry":
		return p.retryLast()

	case "compact":
		return p.compactNow()

	case "undo":
		p.recoverWorkspace(recovery.OpUndo)
		return nil

	case "redo":
		p.recoverWorkspace(recovery.OpRedo)
		return nil

	case "connect":
		p.openConnectManager()
		return nil

	case "disconnect":
		p.disconnect(command.Args)
		return nil

	default:
		text := fmt.Sprintf("unknown command /%s — try /help", command.Name)
		if near, ok := SuggestCommand(command.Name); ok {
			text = fmt.Sprintf("unknown command /%s — did you mean /%s?", command.Name, near)
		}
		p.state.Apply(Notice{Text: text})
		return nil
	}
}

// handlePickerKey routes keys to the open selection overlay.
//
// Precedence is fixed (see the contract in picker.go): esc clears the query
// first and closes only when already empty; ctrl+c always closes; enter
// selects; arrows/pgup/pgdn/home/end navigate; left/right/ctrl+b/ctrl+f move
// the query cursor with backspace/delete editing; ctrl+r refreshes a stale
// model catalog; printable input — including space — inserts into the query.
// Nothing else reaches the composer while the overlay is open.
func (p *Program) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	picker := p.state.Picker
	switch msg.String() {
	case "esc":
		// The MCP manager has no search box: Esc always closes it and
		// discards staged changes immediately.
		if picker != nil && picker.Kind == PickerMCP {
			p.closePicker()
			return p, nil
		}
		if picker.Filter != "" {
			p.state.Apply(PickerFilterCleared{})
			return p, nil
		}
		p.closePicker()
		return p, nil
	case "ctrl+c":
		p.closePicker()
		return p, nil
	case "enter":
		return p.choosePicker()
	case "up", "ctrl+p", "shift+tab":
		p.state.Apply(PickerMoved{Delta: -1})
		return p, nil
	case "down", "ctrl+n", "tab":
		p.state.Apply(PickerMoved{Delta: 1})
		return p, nil
	case "pgup":
		p.state.Apply(PickerPaged{Delta: -1})
		return p, nil
	case "pgdown":
		p.state.Apply(PickerPaged{Delta: 1})
		return p, nil
	case "home", "ctrl+home":
		p.state.Apply(PickerTop{})
		return p, nil
	case "end", "ctrl+end":
		p.state.Apply(PickerBottom{})
		return p, nil
	case "left", "ctrl+b":
		p.state.Apply(PickerFilterCursorMoved{Delta: -1})
		return p, nil
	case "right", "ctrl+f":
		p.state.Apply(PickerFilterCursorMoved{Delta: 1})
		return p, nil
	case "ctrl+a":
		if picker != nil {
			p.state.Apply(PickerFilterCursorMoved{Delta: -len([]rune(picker.Filter))})
		}
		return p, nil
	case "ctrl+e":
		if picker != nil {
			p.state.Apply(PickerFilterCursorMoved{Delta: len([]rune(picker.Filter))})
		}
		return p, nil
	case " ", "space":
		// Space stages an MCP toggle; it is not a filter character here. Every
		// other picker keeps space in the query.
		if picker != nil && picker.Kind == PickerMCP {
			p.state.Apply(PickerToggled{})
			return p, nil
		}
		p.state.Apply(PickerTyped{Text: " "})
		return p, nil
	case "backspace", "ctrl+h":
		p.state.Apply(PickerFilterBackspace{})
		return p, nil
	case "delete", "ctrl+d":
		p.state.Apply(PickerFilterDelete{})
		return p, nil
	case "ctrl+r":
		// The MCP manager has no catalog to refresh; Ctrl+R stays inert so
		// opening or browsing it never starts unrelated I/O.
		if picker != nil && picker.Kind == PickerMCP {
			return p, nil
		}
		if cmd := p.refreshModelCatalog(); cmd != nil {
			return p, cmd
		}
		return p, nil
	default:
		// The MCP manager is keyboard-first and has no filter: printable input
		// must not accumulate an invisible query that Esc would clear instead
		// of closing.
		if picker != nil && picker.Kind == PickerMCP {
			return p, nil
		}
		// Space arrives as KeySpace, not KeyRunes: accept both, or
		// multiword search ("deepseek v4 free") silently loses its spaces.
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			text := string(msg.Runes)
			if msg.Type == tea.KeySpace {
				text = " "
			}
			if text != "" {
				p.state.Apply(PickerTyped{Text: text})
			}
		}
		return p, nil
	}
}

// closePicker dismisses the overlay, restoring composer text saved when the
// picker opened over typing. Closing never selects, and never loses the
// saved draft.
func (p *Program) closePicker() {
	if picker := p.state.Picker; picker != nil && picker.Kind == PickerMCP {
		// Esc discards staged changes: dropping the snapshot is what makes
		// "nothing was applied" true, even if a later picker reopens.
		p.mcpBefore = nil
	}
	p.state.Apply(PickerClosed{})
	if p.pickerRestoreSet {
		p.state.Apply(InputChanged{Text: p.pickerRestore})
		p.pickerRestore = ""
		p.pickerRestoreSet = false
	}
}

// choosePicker acts on the highlighted item, then closes the overlay.
func (p *Program) choosePicker() (tea.Model, tea.Cmd) {
	picker := p.state.Picker
	if picker == nil {
		return p, nil
	}
	item, ok := picker.Chosen()
	kind := picker.Kind
	if kind == PickerMCP {
		return p.applyMCPPicker(picker, item, ok)
	}
	p.state.Apply(PickerClosed{})
	p.pickerRestore = ""
	p.pickerRestoreSet = false
	if !ok {
		return p, nil
	}

	switch kind {
	case PickerSessions:
		return p, p.resumeSession(item)
	case PickerModels:
		p.applyModelSwitch(item.Value)
		return p, nil
	case PickerJobs:
		p.state.Apply(Notice{Text: p.jobDetail(item.Value)})
		return p, nil
	case PickerNewSession:
		return p.chooseNewSession(item)
	case PickerTasks:
		return p.chooseTask(item)
	case "connect-models":
		p.finishConnect(item)
		return p, nil
	default:
		return p, nil
	}
}

// applyMCPPicker commits the staged MCP changes and closes the manager.
//
// Space only stages state, so this is the one place a server change becomes
// real. Esc never reaches it, which is what makes cancel side-effect free.
func (p *Program) applyMCPPicker(picker *PickerState, item PickerItem, ok bool) (tea.Model, tea.Cmd) {
	if !ok {
		p.closePicker()
		return p, nil
	}
	if item.Value == MCPAddValue {
		p.closePicker()
		hint := MCPAddHintText("")
		if p.mcp.AddHint != nil {
			hint = p.mcp.AddHint()
		}
		p.state.Apply(Notice{Text: hint})
		return p, nil
	}
	changes := mcpPickerChanges(picker, p.mcpBefore)
	p.closePicker()
	if len(changes) == 0 {
		p.state.Apply(Notice{Text: "no MCP changes"})
		return p, nil
	}
	if p.mcp.Apply == nil {
		p.state.Apply(Notice{Text: "applying MCP changes is unavailable in this session"})
		return p, nil
	}
	return p, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		p.publish(Notice{Text: RenderMCPApplyResults(p.mcp.Apply(ctx, changes))})
		return nil
	}
}

// openNewSessionPicker opens the explicit /new selector.
//
// It uses the same generic picker as every other selector — no second picker
// implementation. Esc cancels without touching session or task state; Enter on
// the start row creates exactly one new session. A name argument creates
// directly with that title instead of opening the picker.
func (p *Program) openNewSessionPicker(args []string) {
	if name := strings.TrimSpace(strings.Join(args, " ")); name != "" {
		p.createNewSessionWithTitle(name)
		return
	}
	p.state.Apply(PickerOpened{
		Kind:  PickerNewSession,
		Title: "New Session",
		Items: []PickerItem{
			{Title: "Start new session", Detail: "fresh conversation", Value: "new"},
			{Title: "Cancel", Detail: "stay in this session", Value: "cancel"},
		},
	})
}

// chooseNewSession acts on the /new picker selection.
func (p *Program) chooseNewSession(item PickerItem) (tea.Model, tea.Cmd) {
	if item.Value == "cancel" {
		return p, nil
	}
	if p.newSessionCreating {
		return p, nil
	}
	p.newSessionCreating = true
	defer func() { p.newSessionCreating = false }()
	if p.sessionActions.New == nil {
		p.state.Apply(Notice{Text: "new session is unavailable"})
		return p, nil
	}
	p.cancelTurnForNewSession()
	sess, opened, err := p.sessionActions.New()
	if err != nil {
		p.state.Apply(Notice{Text: "new session: " + err.Error()})
		return p, nil
	}
	p.detachTaskForNewSession()
	p.adoptSession(sess, opened)
	p.recordTaskDecision("NEW_SESSION")
	return p, nil
}

// createNewSessionWithTitle creates a session directly for "/new <name>".
func (p *Program) createNewSessionWithTitle(name string) {
	if p.newSessionCreating {
		return
	}
	p.newSessionCreating = true
	defer func() { p.newSessionCreating = false }()
	if p.sessionActions.New == nil {
		p.state.Apply(Notice{Text: "new session is unavailable"})
		return
	}
	p.cancelTurnForNewSession()
	sess, opened, err := p.sessionActions.New()
	if err != nil {
		p.state.Apply(Notice{Text: "new session: " + err.Error()})
		return
	}
	p.detachTaskForNewSession()
	p.adoptSession(sess, opened)
	if p.sessionActions.Rename != nil && sess != nil {
		_ = p.sessionActions.Rename(sess.ID, name)
	}
	p.recordTaskDecision("NEW_SESSION")
}

// cancelTurnForNewSession aborts the in-flight turn so its history and
// transcript cannot leak into the new session. It checks the cancellation
// handle directly rather than only the view's Busy flag: tests drive runTurn
// without the Update loop, so Busy may be stale while a turn is running.
func (p *Program) cancelTurnForNewSession() {
	p.mu.Lock()
	hasTurn := p.cancelTurn != nil
	p.mu.Unlock()
	if hasTurn || p.state.Status.Busy {
		p.cancelTurnNow("new session")
	}
}

// detachTaskForNewSession preserves the active task as resumable without
// blocking the new session. It never deletes durable work: the controller
// archives the task and clears only the session binding.
func (p *Program) detachTaskForNewSession() {
	if p.task.Detach == nil {
		return
	}
	projection := p.task.Detach()
	if projection.ID != "" && projection.Err == "" {
		p.PublishTask(projection)
		p.recordTaskDecision("TASK_DETACH")
	}
}

// chooseTask resumes the task chosen from the /task resume picker.
func (p *Program) chooseTask(item PickerItem) (tea.Model, tea.Cmd) {
	if p.task.ResumeTask == nil {
		p.state.Apply(Notice{Text: "task resume is unavailable"})
		return p, nil
	}
	projection := p.task.ResumeTask(item.Value)
	if projection.Err != "" {
		p.state.Apply(Notice{Text: "task resume: " + projection.Err})
		return p, nil
	}
	p.PublishTask(projection)
	p.state.Apply(Notice{Text: "resumed task: " + projection.Objective})
	p.recordTaskDecision("EXPLICIT_RESUME")
	return p, nil
}

// recordTaskDecision stores the internal classification for observability. It
// answers why a task was (or was not) resumed without exposing internals to
// the transcript.
func (p *Program) recordTaskDecision(decision string) {
	p.mu.Lock()
	p.lastTaskDecision = decision
	p.mu.Unlock()
}

// TaskDecision returns the last internal task classification.
func (p *Program) TaskDecision() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastTaskDecision
}

// jobDetail renders one background job for the transcript, preferring the
// listed snapshot's output tail over the bare picker line.
func (p *Program) jobDetail(id string) string {
	for _, job := range p.lastJobs {
		if job.ID != id {
			continue
		}
		status := fmt.Sprintf("exit %d", job.ExitCode)
		if job.Running {
			status = "running, " + job.Elapsed + " elapsed"
		}
		if strings.TrimSpace(job.Tail) == "" {
			return fmt.Sprintf("%s %s (%s, no output yet)", job.ID, job.Command, status)
		}
		return fmt.Sprintf("%s %s (%s)\n%s", job.ID, job.Command, status, job.Tail)
	}
	return id
}

// ctrlCExitHint is the deliberate-exit affordance shown when Ctrl+C has
// nothing left to abort. It names the two chords that actually quit so the
// user discovers them instead of losing a session to a reflex.
const ctrlCExitHint = "Press Ctrl+D or type /exit to quit"

// interruptOrClear handles ctrl+c at the composer.
//
// The reflex abort key is layered from safest to most destructive, so a
// single stray press can never discard a session:
//
//   - an open overlay closes and focus returns to the composer;
//   - a running turn (or tool) is interrupted and the runtime returns to idle;
//   - a non-empty composer is cleared to an empty prompt line;
//   - only an already-empty, idle composer explains the deliberate exit
//     chords instead of quitting.
func (p *Program) interruptOrClear() (tea.Model, tea.Cmd) {
	if p.state.QuitConfirm != nil {
		// An open quit confirmation owns the abort key: dismiss it rather
		// than quitting, cancelling a running turn the same way esc would.
		p.state.Apply(QuitConfirmClosed{})
		if p.state.Status.Busy {
			p.cancelTurnNow("cancelled")
		}
		return p, nil
	}
	if p.state.Plane != nil {
		p.closePlane()
		return p, nil
	}
	if p.state.HelpModal != nil {
		p.closeHelpCenter()
		return p, nil
	}
	if p.state.InfoModal != nil || p.state.InformationModal != nil {
		p.state.Apply(InfoModalClosed{})
		p.state.Apply(InformationModalClosed{})
		return p, nil
	}
	if p.state.Picker != nil {
		p.closePicker()
		return p, nil
	}
	// While the support overlay is open, ctrl+c cancels a running
	// support turn or closes an idle panel. It never quits the session
	// from behind the overlay and never touches the main turn.
	if p.state.SupportPanel != nil {
		if p.state.SupportBusy() {
			p.cancelSupportNow("cancelled")
			return p, nil
		}
		p.state.Apply(SupportClosed{})
		return p, nil
	}
	// A running "!" escape is foreground work: the abort key stops it before
	// reaching for typed text or the deliberate-exit hint. It is checked
	// before the turn so the most recent foreground action stops first.
	if p.state.runningShell() != nil {
		p.cancelShellNow()
		return p, nil
	}
	// A running turn wins over typed text: the interrupt must reach the
	// stream or tool, not just the buffer, so work can actually be stopped.
	if p.state.Status.Busy {
		p.cancelTurnNow("cancelled")
		return p, nil
	}
	// The inline palette is chrome over the composer: ctrl+c dismisses it
	// the way esc does and keeps the draft rather than clearing it. A hidden
	// palette already reports no rows, so an empty result falls through.
	if paletteOwnsKeys(p.state) {
		p.state.PaletteHide = true
		return p, nil
	}
	if p.state.Input != "" {
		// Clear the prompt buffer — paste pills included, since setComposer
		// prunes chips whose labels vanish — leaving the cursor on an empty
		// prompt line. Ctrl+C means "abandon what I'm typing", not "quit".
		p.state.Apply(ComposerChanged{Text: "", Cursor: 0})
		return p, nil
	}
	// Nothing left to abort. Teach the deliberate exit chords instead of
	// quitting on a reflex; a repeated press must not stack the same hint.
	if !p.lastEntryIsNotice(ctrlCExitHint) {
		p.state.Apply(Notice{Text: ctrlCExitHint})
	}
	return p, nil
}

// lastEntryIsNotice reports whether the newest transcript entry is exactly
// the given notice, so a repeated keypress cannot stack identical hints.
func (p *Program) lastEntryIsNotice(text string) bool {
	n := len(p.state.Entries)
	if n == 0 {
		return false
	}
	last := p.state.Entries[n-1]
	return last.Kind == EntryNotice && last.Notice == text
}

// cancelSupportNow aborts the in-flight support turn, if any.
//
// Cancellation is a single semantic transition: the reducer makes repeated
// requests idempotent, so one Ctrl+C produces one terminal Cancelled state
// and never a stream of duplicates. The spinner stops with the transition
// itself, because the overlay derives it from the panel status rather than
// from the loop unwinding.
func (p *Program) cancelSupportNow(note string) {
	p.mu.Lock()
	cancel := p.supportCancel
	p.mu.Unlock()

	// Runs on the update goroutine, which owns the state: the read is
	// direct, like every other state read here.
	seq := 0
	if p.state.SupportPanel != nil {
		seq = p.state.SupportPanel.Seq
	}
	if seq == 0 {
		seq = p.state.SupportSeq
	}

	if cancel != nil {
		cancel()
	}
	text := note
	if text == "" {
		text = "cancelled"
	}
	p.state.Apply(SupportCancelledEvent{Seq: seq, Text: text})
}

// cancelTurnNow aborts the in-flight turn, if any.
//
// A tool blocked in its approval prompt is denied first: without that the
// turn would stay parked on a question nobody will answer, and the
// cancellation would take effect never. Queued prompts are dropped with it:
// they were typed against a turn the user just abandoned, and silently
// starting one afterwards would undo the cancellation.
//
// Cancellation is a single semantic transition: the reducer makes repeated
// requests idempotent, so one Ctrl+C produces one terminal Cancelled state
// and one user-visible line, never a stream of duplicates. The spinner stops
// with the transition itself, not when the loop unwinds, so the footer can
// never report working after the turn has terminated.
func (p *Program) cancelTurnNow(note string) {
	// A commit proposal has its own lifetime. /commit is an independent
	// interactive command, so cancelling an unrelated turn must not abort it;
	// user cancellation, session changes, and quitting cancel it explicitly.
	// Claim the terminal transition before the context is cancelled. The
	// post-run path claims the same state with a CAS; a cancel that gets here
	// first therefore cannot be lost between its ctx check and Complete.
	p.turnTerminal.Store(turnTerminalCancelled)

	p.mu.Lock()
	cancel := p.cancelTurn
	p.mu.Unlock()

	if p.pending != nil {
		p.answer(false, "cancelled")
	}
	if cancel != nil {
		cancel()
	}
	text := note
	if len(p.state.Queued) > 0 {
		text += " (queue cleared)"
	}
	if text == "" {
		text = "cancelled"
	}
	p.state.Apply(TurnCancelled{Seq: p.state.TurnSeq, Text: text})
}

// queue holds a prompt typed while a turn runs. Past the cap it refuses with
// a notice rather than silently dropping words the user typed.
func (p *Program) queue(text string) tea.Cmd {
	if len(p.state.Queued) >= maxQueued {
		p.state.Apply(Notice{Text: "queue is full: wait for the turn to finish"})
		return nil
	}
	p.state.Apply(MessageQueued{Text: text})
	p.state.Apply(InputChanged{Text: ""})
	return nil
}

// switchWorkspace routes /ask, /fuzz, /build through the owner seam.
//
// The transition happens at a turn boundary: a running turn keeps the
// authority it started under, so one execution never spans two workspaces.
func (p *Program) switchWorkspace(name string) bool {
	if p.workspace.Switch == nil {
		p.state.Apply(Notice{Text: "workspace switching is unavailable"})
		return false
	}
	// A workspace switch underneath an open support panel would move the
	// authority a running SUP turn executes under. Refused until the
	// panel closes (Esc); the turn boundary rule stays intact.
	if p.state.SupportPanel != nil {
		p.state.Apply(Notice{Text: "close support first (Esc) before switching workspace"})
		return false
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: "wait for the turn to finish before switching workspace"})
		return false
	}
	if p.workspace.Current != nil && strings.EqualFold(p.workspace.Current(), name) {
		return true
	}
	if err := p.workspace.Switch(name); err != nil {
		p.state.Apply(Notice{Text: "workspace: " + err.Error()})
		return false
	}
	return true
}

// It exists so a test can exercise the dispatch table without a Bubble Tea
// runtime; the returned command would be a no-op there anyway.
// applyModelSwitch resolves a model id through the loop and refreshes the
// status line from what the loop will actually call.
func (p *Program) applyModelSwitch(id string) {
	if p.setModel == nil {
		p.state.Apply(Notice{Text: "model switching is unavailable"})
		return
	}
	if err := p.setModel(id); err != nil {
		p.state.Apply(Notice{Text: "model: " + err.Error()})
		return
	}
	// The status line is read from state, so refreshing it here keeps the
	// footer in step with what the loop will actually call. Effort stays as
	// set: a bare id switch carries no level, so the existing badge (or a
	// variant suffix in the new id) stands.
	p.state.Status.Model = p.currentModel()
}

// openModelPicker browses the available models instead of requiring an exact
// id. The snapshot answers instantly from bundled plus cached rows; a stale
// snapshot refreshes asynchronously without blocking the event loop, and the
// picker stays usable (scrollable, searchable) while loading. Without any
// lister it degrades to reporting the current model, which is what the
// command did before the picker existed.
func (p *Program) openModelPicker() tea.Cmd {
	snapshot, ok := p.modelSnapshot()
	if !ok {
		p.state.Apply(Notice{Text: "model: " + p.state.Status.Model})
		return nil
	}
	// An empty snapshot with a refresh hook is a cache miss, not a dead end:
	// open the picker in its existing loading state and hydrate it in the
	// background. Without a hook there is nothing to hydrate, so say so.
	loading := snapshot.Origin == "loading" || (len(snapshot.Options) == 0 && p.catalog.Refresh != nil)
	if len(snapshot.Options) == 0 && !loading {
		p.state.Apply(Notice{Text: "no models known"})
		return nil
	}
	active := ""
	if p.currentModel != nil {
		active = p.currentModel()
	}
	for i := range snapshot.Options {
		snapshot.Options[i].Active = snapshot.Options[i].ID == active
	}
	p.state.Apply(PickerOpened{
		Kind:    PickerModels,
		Title:   "models",
		Items:   optionsToItems(snapshot.Options),
		Loading: loading,
		Origin:  snapshot.Origin,
		Stale:   snapshot.Stale,
	})
	// The snapshot is the boot roster. Revalidate it in the background, after
	// the picker is already visible: refresh time must never determine
	// time-to-first-picker.
	return p.issueRefresh()
}

// openModelControl opens the persistent split-pane control plane behind
// /model. It builds the roster and role list from the caller's hooks and
// never persists anything itself: choosing a role routes through Assign,
// which owns validation, the configuration authority, and the runtime.
func (p *Program) openModelControl() tea.Cmd {
	snapshot, ok := p.modelSnapshot()
	if !ok {
		p.state.Apply(Notice{Text: "model: " + p.state.Status.Model})
		return nil
	}
	active := ""
	if p.currentModel != nil {
		active = p.currentModel()
	}
	for i := range snapshot.Options {
		snapshot.Options[i].Active = snapshot.Options[i].ID == active
	}
	var providers []ProviderRow
	if p.modelControl.Providers != nil {
		providers = p.modelControl.Providers()
	}
	var roles []RoleAssignment
	if p.modelControl.Roles != nil {
		roles = p.modelControl.Roles()
	}
	activeRole := ""
	if p.modelControl.ActiveRole != nil {
		activeRole = p.modelControl.ActiveRole()
	}
	loading := snapshot.Origin == "loading" || (len(snapshot.Options) == 0 && p.catalog.Refresh != nil)
	p.state.Apply(ModelPlaneOpened{
		Providers:  providers,
		Models:     snapshot.Options,
		Roles:      roles,
		ActiveRole: activeRole,
		Loading:    loading,
		Origin:     snapshot.Origin,
		Stale:      snapshot.Stale,
	})
	if p.state.Plane != nil {
		p.state.Plane.SetVisibleRows(planeRowsFor(p.state))
	}
	// Render from the cached snapshot first; revalidate it in the background.
	// The providers and roles above are local reads, so nothing on this path
	// waits for provider discovery or a remote catalog.
	return p.issueRefresh()
}

// closePlane dismisses the control plane without applying anything.
func (p *Program) closePlane() {
	p.state.Apply(ModelPlaneClosed{})
}

// handlePlaneKey routes keys to the open control plane.
//
// The interaction contract is a linear, transactional flow. MODEL mode runs
// model -> role -> effort -> apply; ROLES mode runs role -> model -> effort ->
// apply. Every step owns only transient state: nothing is persisted until the
// explicit Enter on the effort (or apply) step, and Esc backs out a step
// without touching configuration. Tab switches between the MODEL and ROLES
// modes. Search owns printable input, so no letter ever executes a command.
func (p *Program) handlePlaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	plane := p.state.Plane
	// The inline key prompt owns the keyboard while open: typing masks,
	// Enter saves through the credential seam, Esc abandons the input.
	if plane.Auth != nil {
		return p.handlePlaneAuthKey(msg)
	}
	// An open variant prompt owns the keyboard: nothing behind it moves
	// until a variant is confirmed or the prompt is abandoned.
	if plane.Variant != nil {
		return p.handleVariantKey(msg)
	}
	switch plane.Stage {
	case PlaneStageBrowse:
		if plane.Mode == PlaneRoles {
			return p.handleRoleBrowseKey(msg)
		}
		return p.handleModelBrowseKey(msg)
	case PlaneStageModel:
		return p.handleModelBrowseKey(msg)
	default:
		return p.handlePlaneStageKey(msg)
	}
}

// handleModelBrowseKey drives the model list, shared by MODEL browsing and the
// roles-flow model step. Enter confirms the highlighted model and advances the
// flow; in roles mode it continues to effort, in model mode to role selection.
func (p *Program) handleModelBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	plane := p.state.Plane
	inFlow := plane.Stage == PlaneStageModel
	switch msg.String() {
	case "esc", "ctrl+c":
		if inFlow {
			p.state.Apply(PlaneStageCancelled{})
			return p, nil
		}
		// Leaving search keeps the query; a second Esc clears it; a third
		// closes. Closing never applies anything.
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
			return p, nil
		}
		if plane.Filter != "" {
			p.state.Apply(PlaneFilterCleared{})
			return p, nil
		}
		p.closePlane()
		return p, nil
	case "tab", "shift+tab":
		if inFlow {
			// Mid-flow, Tab is not a step; keep the user where they are.
			return p, nil
		}
		p.state.Apply(PlaneModeToggled{})
		return p, nil
	case "left":
		// The left arrow docks focus on the PROVIDERS pane; inside the
		// search box it moves the query cursor instead.
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFilterCursorMoved{Delta: -1})
			return p, nil
		}
		if !inFlow {
			p.state.Apply(PlaneFocusSet{Focus: PlaneFocusProviders})
		}
		return p, nil
	case "ctrl+b":
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFilterCursorMoved{Delta: -1})
			return p, nil
		}
		if !inFlow {
			p.state.Apply(PlaneFocusMoved{Delta: -1})
		}
		return p, nil
	case "right":
		// The right arrow docks focus on the MODELS list; inside the
		// search box it moves the query cursor instead.
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFilterCursorMoved{Delta: 1})
			return p, nil
		}
		if !inFlow {
			p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
		}
		return p, nil
	case "ctrl+f":
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFilterCursorMoved{Delta: 1})
			return p, nil
		}
		if !inFlow {
			p.state.Apply(PlaneFocusMoved{Delta: 1})
		}
		return p, nil
	case "up", "ctrl+p":
		p.state.Apply(PlaneMoved{Delta: -1})
		return p, nil
	case "down", "ctrl+n":
		p.state.Apply(PlaneMoved{Delta: 1})
		return p, nil
	case "pgup":
		p.state.Apply(PlanePaged{Delta: -1})
		return p, nil
	case "pgdown":
		p.state.Apply(PlanePaged{Delta: 1})
		return p, nil
	case "home", "ctrl+home":
		p.state.Apply(PlaneTop{})
		return p, nil
	case "end", "ctrl+end":
		p.state.Apply(PlaneBottom{})
		return p, nil
	case "ctrl+a":
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFilterCursorMoved{Delta: -len([]rune(plane.Filter))})
		}
		return p, nil
	case "ctrl+e":
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFilterCursorMoved{Delta: len([]rune(plane.Filter))})
			return p, nil
		}
		// Inline credential entry is a providers-pane action in browse
		// mode only. On the models list or the search box it stays a
		// no-op (search keeps its end-of-line behaviour above), so a
		// stray chord never opens a prompt over the wrong target.
		if !inFlow && plane.Focus == PlaneFocusProviders {
			p.state.Apply(PlaneAuthOpened{})
		}
		return p, nil
	case "backspace", "ctrl+h":
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFilterBackspace{})
		}
		return p, nil
	case "delete", "ctrl+d":
		if plane.Focus == PlaneFocusSearch {
			p.state.Apply(PlaneFilterDelete{})
		}
		return p, nil
	case "enter":
		switch {
		case inFlow:
			p.choosePlaneModel()
		case plane.Focus == PlaneFocusProviders, plane.Focus == PlaneFocusSearch:
			// Start of the flow: move to the model list.
			p.state.Apply(PlaneFocusSet{Focus: PlaneFocusModels})
		default:
			p.stagePlaneModel()
		}
		return p, nil
	case "ctrl+r":
		if cmd := p.refreshPlaneCatalog(); cmd != nil {
			return p, cmd
		}
		return p, nil
	default:
		if msg.Type != tea.KeyRunes && msg.Type != tea.KeySpace {
			return p, nil
		}
		text := string(msg.Runes)
		if msg.Type == tea.KeySpace {
			text = " "
		}
		if text == "" {
			return p, nil
		}
		// No shortcut is needed to search — typing is enough. A printable
		// outside the input focuses search first and lands in the query, so
		// it can never execute a command.
		if plane.Focus != PlaneFocusSearch {
			p.state.Apply(PlaneFocusSet{Focus: PlaneFocusSearch})
		}
		p.state.Apply(PlaneTyped{Text: text})
		return p, nil
	}
}

// handleRoleBrowseKey drives the ROLES list. Enter begins the roles-first
// transaction for the highlighted role; Esc backs out to MODEL mode (a second
// Esc then closes), preserving the top-level navigation.
func (p *Program) handleRoleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		p.state.Apply(PlaneModeToggled{})
		return p, nil
	case "tab", "shift+tab":
		p.state.Apply(PlaneModeToggled{})
		return p, nil
	case "up", "ctrl+p":
		p.state.Apply(PlaneMoved{Delta: -1})
		return p, nil
	case "down", "ctrl+n":
		p.state.Apply(PlaneMoved{Delta: 1})
		return p, nil
	case "pgup":
		p.state.Apply(PlanePaged{Delta: -1})
		return p, nil
	case "pgdown":
		p.state.Apply(PlanePaged{Delta: 1})
		return p, nil
	case "home", "ctrl+home":
		p.state.Apply(PlaneTop{})
		return p, nil
	case "end", "ctrl+end":
		p.state.Apply(PlaneBottom{})
		return p, nil
	case "enter":
		p.state.Apply(PlaneRoleStaged{})
		return p, nil
	default:
		// Roles mode has no search box; typing stays a no-op rather than
		// yanking the mode away.
		return p, nil
	}
}

// handlePlaneStageKey drives the role, effort, and apply steps. Enter advances
// or applies; Esc backs out one step.
func (p *Program) handlePlaneStageKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	plane := p.state.Plane
	switch msg.String() {
	case "esc", "ctrl+c":
		p.state.Apply(PlaneStageCancelled{})
		return p, nil
	case "up", "ctrl+p", "left", "ctrl+b":
		p.state.Apply(PlaneMoved{Delta: -1})
		return p, nil
	case "down", "ctrl+n", "right", "ctrl+f":
		p.state.Apply(PlaneMoved{Delta: 1})
		return p, nil
	case "pgup":
		p.state.Apply(PlanePaged{Delta: -1})
		return p, nil
	case "pgdown":
		p.state.Apply(PlanePaged{Delta: 1})
		return p, nil
	case "home", "ctrl+home":
		p.state.Apply(PlaneTop{})
		return p, nil
	case "end", "ctrl+end":
		p.state.Apply(PlaneBottom{})
		return p, nil
	case "enter":
		if plane.Stage == PlaneStageRole {
			p.state.Apply(PlaneRoleChosen{})
			return p, nil
		}
		p.applyPlane()
		return p, nil
	default:
		return p, nil
	}
}

// stagePlaneModel begins the model-first transaction for the highlighted model.
// A model that needs a variant choice opens that prompt first; nothing is
// persisted either way.
func (p *Program) stagePlaneModel() {
	plane := p.state.Plane
	model, ok := plane.ChosenModel()
	if !ok {
		p.state.Apply(PlaneAssigned{Notice: "no model selected"})
		return
	}
	if plane.NeedsVariantChoice(model) {
		p.state.Apply(PlaneVariantOpened{})
		return
	}
	p.state.Apply(PlaneModelStaged{Model: model})
}

// choosePlaneModel confirms the highlighted model in the roles flow.
func (p *Program) choosePlaneModel() {
	plane := p.state.Plane
	model, ok := plane.ChosenModel()
	if !ok {
		p.state.Apply(PlaneAssigned{Notice: "no model selected"})
		return
	}
	if plane.NeedsVariantChoice(model) {
		p.state.Apply(PlaneVariantOpened{})
		return
	}
	p.state.Apply(PlaneModelChosen{})
}

// refreshPlaneCatalog issues a background refresh for an open plane, at most
// one at a time.
func (p *Program) refreshPlaneCatalog() tea.Cmd {
	if p.state.Plane == nil || p.state.Plane.Refreshing {
		return nil
	}
	return p.issueRefresh()
}

// handleVariantKey routes keys while the choose-a-variant prompt is open.
//
// The prompt is strictly modal: navigation moves the variant highlight,
// Enter confirms it through the normal assignment seam, Esc abandons it.
// Everything else — printable input, Tab, refresh — is ignored, so a choice
// can never half-apply and search text can never leak into it.
func (p *Program) handleVariantKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		p.state.Apply(PlaneVariantClosed{})
		return p, nil
	case "enter":
		p.confirmPlaneVariant()
		return p, nil
	case "up", "ctrl+p":
		p.state.Apply(PlaneVariantMoved{Delta: -1})
		return p, nil
	case "down", "ctrl+n":
		p.state.Apply(PlaneVariantMoved{Delta: 1})
		return p, nil
	case "pgup":
		p.state.Apply(PlanePaged{Delta: -1})
		return p, nil
	case "pgdown":
		p.state.Apply(PlanePaged{Delta: 1})
		return p, nil
	case "home", "ctrl+home":
		p.state.Apply(PlaneTop{})
		return p, nil
	case "end", "ctrl+end":
		p.state.Apply(PlaneBottom{})
		return p, nil
	default:
		return p, nil
	}
}

// handlePlaneAuthKey routes keys while the inline API-key prompt is open.
//
// The prompt is strictly modal: printable input edits the masked key, Enter
// saves through the credential seam, Esc abandons the input. Nothing else
// moves behind it, so a keystroke can never half-assign a model while a key
// is being typed.
func (p *Program) handlePlaneAuthKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		p.state.Apply(PlaneAuthClosed{})
		return p, nil
	case "enter":
		p.savePlaneKey()
		return p, nil
	case "backspace", "ctrl+h":
		p.state.Apply(PlaneAuthBackspace{})
		return p, nil
	case "delete", "ctrl+d":
		p.state.Apply(PlaneAuthDelete{})
		return p, nil
	case "left", "ctrl+b":
		p.state.Apply(PlaneAuthCursorMoved{Delta: -1})
		return p, nil
	case "right", "ctrl+f":
		p.state.Apply(PlaneAuthCursorMoved{Delta: 1})
		return p, nil
	case "home", "ctrl+a":
		if p.state.Plane != nil && p.state.Plane.Auth != nil {
			p.state.Apply(PlaneAuthCursorMoved{Delta: -len([]rune(p.state.Plane.Auth.Input))})
		}
		return p, nil
	case "end", "ctrl+e":
		if p.state.Plane != nil && p.state.Plane.Auth != nil {
			p.state.Apply(PlaneAuthCursorMoved{Delta: len([]rune(p.state.Plane.Auth.Input))})
		}
		return p, nil
	case "ctrl+u":
		p.state.Apply(PlaneAuthClearedToStart{})
		return p, nil
	case "ctrl+k":
		p.state.Apply(PlaneAuthClearedToEnd{})
		return p, nil
	case "ctrl+w":
		p.state.Apply(PlaneAuthWordBack{})
		return p, nil
	default:
		// Space arrives as KeySpace, not KeyRunes: accept both, or a pasted
		// key with spaces loses them. Only the masked bullets ever render.
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			text := string(msg.Runes)
			if msg.Type == tea.KeySpace {
				text = " "
			}
			if text != "" {
				p.state.Apply(PlaneAuthTyped{Text: text})
			}
		}
		return p, nil
	}
}

// savePlaneKey persists the typed key through the caller's credential seam,
// then refreshes the provider pane so the credential glyph flips from
// unauthenticated to active immediately. An empty key or a missing seam is
// refused with the prompt left open, so the typed input is never lost to a
// no-op submit.
func (p *Program) savePlaneKey() {
	plane := p.state.Plane
	if plane == nil || plane.Auth == nil {
		return
	}
	provider := plane.Auth.Provider
	key := strings.TrimSpace(plane.Auth.Input)
	if key == "" {
		p.state.Apply(PlaneAuthFailed{Notice: "empty key: paste a key or press esc to cancel"})
		return
	}
	if p.modelControl.SaveKey == nil {
		p.state.Apply(PlaneAuthFailed{Notice: "credential saving is unavailable"})
		return
	}
	if err := p.modelControl.SaveKey(provider, key); err != nil {
		p.state.Apply(PlaneAuthFailed{Notice: sanitizeInline(err.Error())})
		return
	}
	var providers []ProviderRow
	if p.modelControl.Providers != nil {
		providers = p.modelControl.Providers()
	}
	p.state.Apply(PlaneAuthSaved{
		Providers: providers,
		Notice:    "✓ API key saved for " + provider,
	})
}

// applyPlane applies the pending transaction through the caller's Assign hook,
// then records the outcome. It only runs at the effort or apply step, so the
// model and role are already resolved and nothing half-built reaches the
// authority seam.
func (p *Program) applyPlane() {
	plane := p.state.Plane
	if plane == nil {
		return
	}
	// A running turn must stay consistent with itself: the loop reads its
	// model and effort when it builds each request, so switching mid-turn
	// could make one turn span two models. The change is refused at the busy
	// boundary and the user is told why, rather than silently applied.
	if p.state.Status.Busy {
		p.state.Apply(PlaneAssigned{Notice: "wait for the turn to finish before changing models"})
		return
	}
	update, ok := plane.PendingAssignment()
	if !ok {
		p.state.Apply(PlaneAssigned{Notice: "no model selected"})
		return
	}
	p.assignPlane(update)
}

// confirmPlaneVariant commits the chosen variant row and advances the flow
// through the same transaction: the variant id carries its suffix, its own
// ladder is resolved fresh, and nothing is persisted until Apply.
func (p *Program) confirmPlaneVariant() {
	plane := p.state.Plane
	if plane == nil || plane.Variant == nil {
		return
	}
	if p.state.Status.Busy {
		p.state.Apply(PlaneAssigned{Notice: "wait for the turn to finish before changing models"})
		return
	}
	model, ok := plane.ChosenVariant()
	if !ok {
		p.state.Apply(PlaneVariantClosed{})
		p.state.Apply(PlaneAssigned{Notice: "no variant selected"})
		return
	}
	p.state.Apply(PlaneVariantConfirmed{Model: model})
}

// assignPlane sends one staged assignment through the caller's Assign hook
// and records the outcome. Both the direct path and the variant-confirm path
// share it, so validation, persistence, and the status refresh cannot drift
// apart.
func (p *Program) assignPlane(update RoleAssignment) {
	if p.modelControl.Assign == nil {
		p.state.Apply(PlaneAssigned{Notice: "model configuration is unavailable"})
		return
	}
	result, roles, err := p.modelControl.Assign(update)
	if err != nil {
		p.state.Apply(PlaneAssigned{Roles: roles, Notice: sanitizeInline(err.Error())})
		return
	}
	notice := fmt.Sprintf("assigned %s to %s", describeAssignment(result), strings.ToUpper(result.Role))
	p.state.Apply(PlaneAssigned{Result: result, Roles: roles, Notice: notice})

	// Keep the status line in step with the runtime when the active role's
	// model changed. The window is refreshed from the runtime when the caller
	// supplies it, so the budget display is not left stale. The effort badge
	// follows the applied assignment; an empty level falls back to the live
	// reporter (e.g. the effective default) rather than keeping a level the
	// new model no longer carries.
	if p.modelControl.ActiveRole != nil && strings.EqualFold(result.Role, p.modelControl.ActiveRole()) {
		if p.currentModel != nil {
			p.state.Status.Model = p.currentModel()
		}
		p.state.Status.Effort = NormalizeEffort(result.Effort)
		if p.state.Status.Effort == "" && p.currentEffort != nil {
			p.state.Status.Effort = NormalizeEffort(p.currentEffort())
		}
		if p.modelControl.EffectiveWindow != nil {
			p.state.Status.ContextWindow = p.modelControl.EffectiveWindow()
		}
	}
}

// describeAssignment names an applied model for a confirmation line.
func describeAssignment(result RoleAssignment) string {
	name := result.Name
	if name == "" {
		name = result.Model
	}
	if name == "" {
		name = "model"
	}
	if result.Effort != "" {
		return name + " @" + result.Effort
	}
	return name
}

// modelSnapshot resolves the instant roster: live hooks first, the enriched
// lister second, the legacy id list last.
func (p *Program) modelSnapshot() (CatalogSnapshot, bool) {
	if p.catalog.Snapshot != nil {
		return p.catalog.Snapshot(), true
	}
	if p.listModelOptions != nil {
		options := p.listModelOptions()
		active := ""
		if p.currentModel != nil {
			active = p.currentModel()
		}
		for i := range options {
			if options[i].Active || (active != "" && options[i].ID == active) {
				options[i].Active = true
			}
		}
		return CatalogSnapshot{Options: options}, true
	}
	if p.listModels == nil {
		return CatalogSnapshot{}, false
	}
	var options []ModelOption
	for _, id := range p.listModels() {
		options = append(options, ModelOption{ID: id, Title: id})
	}
	return CatalogSnapshot{Options: options}, true
}

// refreshModelCatalog issues a background catalog refresh for the open model
// picker. It returns the Bubble Tea command to run, or nil when a refresh
// is unavailable, inapplicable, or already in flight: repeated requests for
// every keystroke would be a request flood, not freshness.
func (p *Program) refreshModelCatalog() tea.Cmd {
	if p.state.Picker == nil || p.state.Picker.Kind != PickerModels {
		return nil
	}
	if p.state.Picker.Refreshing {
		return nil
	}
	return p.issueRefresh()
}

// issueRefresh starts a refresh unless one is already in flight. It bumps the
// generation so a slower older response can never overwrite newer rows.
//
// The marker lives on the program rather than the open surface: closing and
// reopening /model must reuse the running refresh, not spawn a second one. The
// returned command is nil in that case, while the newly opened picker is told
// a refresh is already on its way.
func (p *Program) issueRefresh() tea.Cmd {
	if p.catalog.Refresh == nil {
		return nil
	}
	if p.state.Picker == nil && p.state.Plane == nil {
		return nil
	}
	if p.modelRefreshing {
		p.markRefreshing()
		return nil
	}
	p.modelSeq++
	p.modelRefreshing = true
	p.markRefreshing()
	ctx := p.catalogCtx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := p.catalog.Refresh(ctx, p.modelSeq)
	if cmd == nil {
		// No command means no result will ever arrive; do not leave the
		// in-flight marker stuck and block every later refresh.
		p.modelRefreshing = false
		p.markRefreshed()
	}
	return cmd
}

// markRefreshing shows the subtle in-flight status on whichever surface is
// open.
func (p *Program) markRefreshing() {
	if p.state.Picker != nil {
		p.state.Picker.Refreshing = true
	}
	if p.state.Plane != nil {
		p.state.Plane.Refreshing = true
	}
}

// markRefreshed clears the in-flight status without applying rows.
func (p *Program) markRefreshed() {
	if p.state.Picker != nil {
		p.state.Picker.Refreshing = false
	}
	if p.state.Plane != nil {
		p.state.Plane.Refreshing = false
	}
}

// openSessionManager opens the Session Manager over the stored sessions. The
// program owns neither the store nor the replay, so both arrive as functions.
func (p *Program) openSessionManager() {
	if p.listSessions == nil || p.openSession == nil {
		p.state.Apply(Notice{Text: "session browsing is unavailable"})
		return
	}
	list, err := p.listSessions()
	if err != nil {
		p.state.Apply(Notice{Text: "sessions: " + err.Error()})
		return
	}
	if len(list) == 0 {
		p.state.Apply(Notice{Text: "no saved sessions yet"})
		return
	}
	list = p.markActiveSession(list)
	view := SessionManagerState{
		Items:    list,
		ActiveID: p.activeSessionID(),
	}
	// Size the roster window against the modal layout (composer and hints
	// hidden) rather than the surface being replaced, so the highlight stays
	// on screen without a resize.
	probe := p.state
	probe.SessionModal = &view
	view.VisibleRows = sessionManagerVisibleRows(probe)
	// Park the highlight on the loaded session so the manager opens oriented
	// on where the user already is; fall back to the top when it is absent.
	for i, item := range list {
		if item.ID == view.ActiveID {
			view.Cursor = i
			break
		}
	}
	view.EnsureVisible()
	p.state.Apply(SessionManagerOpened{View: view})
}

// activeSessionID returns the loaded session's id, or zero when none.
func (p *Program) activeSessionID() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		return 0
	}
	return p.session.ID
}

// resumeSession switches the program to a stored session chosen from the
// picker.
func (p *Program) resumeSession(item PickerItem) tea.Cmd {
	var id int64
	if _, err := fmt.Sscanf(item.Value, "%d", &id); err != nil {
		p.state.Apply(Notice{Text: "sessions: bad session id " + item.Value})
		return nil
	}
	return p.resumeSessionByID(id)
}

// resumeSessionByID switches the program to a stored session.
//
// History is replaced, never merged: the resumed conversation continues from
// where it left off, and mixing in the current one would send a transcript
// the model never saw.
func (p *Program) resumeSessionByID(id int64) tea.Cmd {
	sess, opened, err := p.openSession(id)
	if err != nil {
		p.state.Apply(Notice{Text: "sessions: " + err.Error()})
		return nil
	}
	p.adoptSession(sess, opened)
	return nil
}

// adoptSession installs a session as the live conversation: its history, its
// replayed transcript, and a fresh telemetry window. The store is untouched —
// every session lives in the one database the program already points at, so
// only the session pointer moves.
//
// A new session never inherits session-scoped execution state: pending tool
// approvals are denied (an old proposal must never execute in the new
// session), whole-session tool approvals are cleared (authority never leaks),
// and the transcript is replaced, never merged.
func (p *Program) adoptSession(sess *session.Session, opened OpenedSession) {
	if p.pending != nil {
		p.answer(false, "superseded by a new session")
	}
	p.pendingEscape = ""
	p.mu.Lock()
	p.history = append([]agent.Message(nil), opened.History...)
	// A resumed or new session is a fresh telemetry window: the prior
	// session's token counts and clock never carry over.
	p.resetSessionTelemetryLocked()
	p.sessionAllowed = nil
	// A session boundary drops any standing proposal decision with the rest
	// of the session state: the decision answered one session's execution,
	// and a new session must determine its own.
	p.proposal = nil
	p.session = sess
	p.mu.Unlock()
	// A commit proposal belongs to the old session's repository decision and
	// must not be answerable into the new one. The sequence bump and the
	// cancelled context make an in-flight answer harmless.
	p.commitSeq++
	p.commitRequest = nil
	p.commitProposing = false
	p.commitEditing = false
	p.stopCommitRequest(commit.ErrSessionClosed)
	// A cancelled generation must not leave the new session's activity line
	// spinning. An execution already in flight stays visible so its real
	// result is reported rather than silently swallowed.
	if p.state.CommitPhase() != CommitExecuting {
		p.state.Apply(CommitChanged{Phase: CommitIdle})
	}
	p.state.Apply(HistoryReplaced{Entries: opened.Entries})
	if sess != nil {
		p.reportRecoveryInspection(sess.ID)
	}
	// A proposal still on screen belongs to the old session; close it so it
	// cannot be answered into the new one. Tool approvals were already denied
	// above through the blocked tool's reply channel.
	if p.state.Pending != nil {
		p.state.Apply(ApprovalAnswered{})
	}
	if p.live != nil {
		p.live.forget()
	}
	if opened.Notice != "" {
		p.state.Apply(Notice{Text: opened.Notice})
	}
}

// openJobsView lists background jobs. Choosing one shows its latest output;
// the wait tool remains the way to follow a running job.
func (p *Program) openJobsView() {
	if p.listJobs == nil {
		p.state.Apply(Notice{Text: "job listing is unavailable"})
		return
	}
	list := p.listJobs()
	if len(list) == 0 {
		p.state.Apply(Notice{Text: "no background jobs"})
		return
	}
	p.lastJobs = list
	var items []PickerItem
	for _, job := range list {
		title := fmt.Sprintf("%s %s", job.ID, job.Command)
		detail := fmt.Sprintf("exit %d", job.ExitCode)
		if job.Running {
			title += " (running)"
			detail = job.Elapsed
		}
		items = append(items, PickerItem{Title: title, Detail: detail, Value: job.ID})
	}
	p.state.Apply(PickerOpened{Kind: PickerJobs, Title: "background jobs", Items: items})
}

// applyThemeCommand switches the palette, with no args reporting the current
// one. The change persists through the handler when one is installed, so a
// restart keeps what the user picked.
func (p *Program) applyThemeCommand(args []string) {
	if len(args) == 0 {
		p.state.Apply(Notice{Text: "theme: " + NormalizeTheme(p.state.Theme)})
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case ThemeDark, ThemeLight:
		// Valid: fall through.
	default:
		p.state.Apply(Notice{Text: fmt.Sprintf("theme: unknown %q — using dark (choose dark or light)", args[0])})
	}
	name := NormalizeTheme(args[0])
	p.state.Apply(ThemeChanged{Name: name})
	if p.onThemeChanged != nil {
		p.onThemeChanged(name)
	}
}

// SetSessionBrowser installs the session lister and opener behind /sessions.
func (p *Program) SetSessionBrowser(list func() ([]SessionItem, error), open func(id int64) (*session.Session, OpenedSession, error)) {
	p.listSessions = list
	p.openSession = open
}

// SessionActions are the Session Manager's write operations. Every field is
// nil-safe: a missing hook reports the action unavailable rather than
// pretending it succeeded.
type SessionActions struct {
	// New creates and opens a fresh session, returning what resuming it would
	// hand the program. Nil disables the "n" key.
	New func() (*session.Session, OpenedSession, error)
	// Rename relabels a session. Nil disables the "r" key.
	Rename func(id int64, title string) error
	// Archive hides a session from the default roster without deleting it.
	// Nil disables the "a" key.
	Archive func(id int64) error
	// Delete removes a session and its transcript. Nil disables the "d" key.
	Delete func(id int64) error
}

// SetSessionActions installs the Session Manager's write operations. It is
// separate from SetSessionBrowser so a caller can offer browsing without
// exposing destructive actions.
func (p *Program) SetSessionActions(actions SessionActions) {
	p.sessionActions = actions
}

// SetModelLister installs the model list behind the /model picker.
func (p *Program) SetModelLister(list func() []string) {
	p.listModels = list
}

// SetModelOptionLister installs the enriched roster behind the /model picker.
//
// Options carry native ids plus caller-built detail (context, price, local
// vs remote, capabilities); the picker renders detail verbatim and resolves
// the native id, so no entry is fabricated and no alias silently substitutes.
func (p *Program) SetModelOptionLister(list func() []ModelOption) {
	p.listModelOptions = list
}

// SetJobsLister installs the background-job list behind /jobs.
func (p *Program) SetJobsLister(list func() []JobItem) {
	p.listJobs = list
}

// SetThemeChangedHandler installs the persistence behind /theme.
func (p *Program) SetThemeChangedHandler(onChange func(theme string)) {
	p.onThemeChanged = onChange
}

// SetTheme applies the initial palette, e.g. from saved settings.
func (p *Program) SetTheme(theme string) {
	p.state.Apply(ThemeChanged{Name: theme})
}

// PublishWorkspace records chrome metadata (cwd, session, usage) without
// touching the transcript. Safe before Run (applies directly) and usable as
// documentation for the event path used live.
func (p *Program) PublishWorkspace(meta WorkspaceUpdated) {
	p.state.Apply(meta)
}

// PublishIdentity records the runtime identity facts (user, greeting, git,
// readiness) without touching the transcript. Safe before Run.
func (p *Program) PublishIdentity(facts IdentityUpdated) {
	p.state.Apply(facts)
}

// runOn invokes the command against a program.
//
// It exists so a test can exercise the dispatch table without a Bubble Tea
// runtime; the returned command would be a no-op there anyway.
func (c Command) runOn(p *Program) { p.runCommand(c) }

// openQuitConfirm asks whether the session should really exit. It is the /q
// path: unlike /quit and /exit it never exits on its own, so a stray command
// cannot discard the session. Confirming quits through quitNow, which denies
// any blocked tool first.
func (p *Program) openQuitConfirm() {
	p.state.Apply(QuitConfirmOpened{})
}

// quitNow ends the session from any idle-state quit path (/quit, /exit,
// confirmed /q, ctrl+d on an empty composer).
//
// A tool blocked in its approval prompt is denied first: without that the
// tool waits on an answer channel no UI reads after shutdown, stranding its
// goroutine (and the turn holding it) until the process exits. Denial is the
// same conservative answer n/esc gives, so quitting can never approve work.
func (p *Program) quitNow() tea.Cmd {
	p.commitSeq++
	p.commitRequest = nil
	p.commitProposing = false
	p.commitEditing = false
	p.stopCommitRequest(commit.ErrSessionClosed)
	if p.state.Pending != nil {
		p.answer(false, "denied by user")
	}
	// A running escape must not outlive the UI: cancelling the context kills
	// the child process instead of leaving it detached.
	if p.state.runningShell() != nil {
		p.cancelShellNow()
	}
	p.state.Apply(QuitRequested{})
	return tea.Quit
}

// answerSelection resolves the highlighted approval answer.
//
// The session-wide choice is the only one that carries memory: it records the
// tool (or the shell escape) as approved for the rest of the session and then
// answers affirmative. Every other choice maps through the usual affirmative
// test, so "No" still denies and a custom label still defaults to denial.
func (p *Program) answerSelection(choice string) (tea.Model, tea.Cmd) {
	if isSessionApprovalChoice(choice) {
		switch {
		case p.pending != nil:
			p.allowForSession(p.pending.toolName)
		case p.pendingEscape != "":
			p.allowForSession(shellApprovalTool)
		}
		return p.answer(true, "")
	}
	return p.answer(isAffirmative(choice), "denied by user")
}

// sessionAllowedFor reports whether a tool was approved for the whole session.
// ApprovalFunc reads it from a blocked tool's goroutine, so the map is guarded
// by mu; a nil map (a Program built outside NewProgram) reads as empty.
func (p *Program) sessionAllowedFor(tool string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.sessionAllowed[tool]
	return ok
}

// allowForSession remembers a tool as approved for the rest of the session.
// It is written from the update goroutine and read from tool goroutines.
func (p *Program) allowForSession(tool string) {
	if tool == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionAllowed == nil {
		p.sessionAllowed = make(map[string]struct{})
	}
	p.sessionAllowed[tool] = struct{}{}
}

// answer unblocks the tool that is waiting, or starts the confirmed shell
// escape that asked.
func (p *Program) answer(approved bool, reason string) (tea.Model, tea.Cmd) {
	if p.state.Pending != nil && p.state.Pending.Commit != nil {
		return p.answerCommit(approved, reason)
	}
	if p.pending != nil {
		// A buffered reply channel means this never blocks the UI even if the
		// tool has already given up.
		p.pending.answer <- approvalAnswer{approved: approved, reason: reason}
		p.pending = nil
		p.state.Apply(ApprovalAnswered{})
		return p, nil
	}
	// A shell escape awaiting confirmation carries no tool: on approval it
	// starts, on denial it simply closes.
	if p.pendingEscape != "" {
		command := p.pendingEscape
		p.pendingEscape = ""
		p.state.Apply(ApprovalAnswered{})
		if approved {
			return p, p.startEscape(command)
		}
		return p, nil
	}
	p.state.Apply(ApprovalAnswered{})
	return p, nil
}

// View implements tea.Model.
//
// In live mode it returns the live region only, unless onboarding is open:
// the wizard owns the screen until it commits, in every presentation mode.
// The transcript itself is not here: it has already left through print
// commands, and re-rendering it as a frame would make the renderer own a
// second copy of the scrollback.
func (p *Program) View() string {
	if p.wizard != nil {
		return p.renderWizardFrame()
	}
	if p.live != nil {
		return p.live.view()
	}
	p.syncTranscript()
	return renderState(p.state, p.transcriptFrame)
}

// syncTranscript folds the current state into the retained layout and resolves
// the viewport. Only blocks whose revision changed are re-rendered, so a scroll
// or a mouse hit-test never walks the rendered history.
func (p *Program) syncTranscript() {
	if p.transcriptIndex == nil {
		p.transcriptIndex = &TranscriptLayout{}
	}
	layout := ComputeLayout(p.state)
	p.transcriptIndex.Refresh(&p.state, layout)
	if p.transcriptIndex.Reflowed {
		p.trace(traceLayout)
	}
	scroll := p.state.scrollIntent
	if scroll == 0 && p.state.Scroll > 0 && !p.state.Follow {
		// A caller that set the mirror directly still gets honest geometry.
		scroll = p.state.Scroll
	}
	vp := p.viewport.Update(p.state.Follow, scroll, p.state.ScrollSeq, transcriptBudget(p.state, layout), p.transcriptIndex)
	p.transcriptFrame = &transcriptFrame{index: p.transcriptIndex, vp: vp}
	if p.state.ScrollSeq != p.lastScrollSeq {
		p.lastScrollSeq = p.state.ScrollSeq
		p.trace(traceScroll)
	}
	if vp.Following != p.lastFollowing {
		p.lastFollowing = vp.Following
		if vp.Following {
			p.trace(traceFollow)
		} else {
			p.trace(traceDetach)
		}
	}
}

// enterAlternateScroll turns on alternate scroll mode and returns the function
// that turns it back off. The caller must always call it: the mode outlives the
// process state that set it, and a terminal left in alternate scroll keeps
// translating the wheel into arrows long after the UI is gone.
func (p *Program) enterAlternateScroll() func() {
	setAlternateScroll(p.termOut, true)
	return func() { setAlternateScroll(p.termOut, false) }
}

// Run starts the UI and blocks until it exits.
//
// Terminal ownership stays with Bubble Tea throughout: alt-screen entry,
// raw mode, and their restoration on every exit path (normal, error,
// signal, panic) are its shutdown sequence's job. The app's share is
// releasing the approval bridge below so no tool outlives the UI.
//
// Mouse reporting is off unless explicitly requested (WithMouseReporting).
// Bubble Tea decodes SGR mouse reports only when whole in one input read; a
// report split across reads degrades to a lone esc plus a KeyRunes tail
// (verified against bubbletea v1.3.10: ESC then "[<64;67;30M" arrives as esc
// + typed text), and that tail lands in the composer verbatim. Requesting
// cell-motion reporting also maximises the split rate (a motion event per cell
// of every drag) while hijacking drag-select from the terminal.
//
// By default the wheel is therefore served without a mouse protocol at all:
// alternate scroll mode (1007) leaves selection and copy untouched and has the
// terminal send wheel ticks as plain arrow keys, which this program already
// parses as whole sequences. handleKey decides what an arrow means.
//
// That path has limits the application cannot fix. Terminals that do not
// implement 1007 never forward the wheel, and a multiplexer such as tmux
// consumes wheel events for its own copy-mode before the pane's terminal sees
// them at all. For those, WithMouseReporting asks for real mouse events —
// which tmux does forward to a pane that requests them — at the cost of the
// terminal's own selection.
func (p *Program) Run() error {
	defer p.closeDone()
	release := p.enterAlternateScroll()
	defer release()

	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if p.mouseReporting {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	_, err := tea.NewProgram(p, opts...).Run()
	return err
}

// RunLive starts the output-first UI and blocks until it exits.
//
// It differs from Run in exactly one terminal fact, and that fact is the
// whole prototype: no alternate screen. Output goes to the terminal's normal
// buffer, so everything ohgo writes scrolls into the terminal's own
// scrollback and stays there.
//
// What is *not* different is the ownership boundary. Bubble Tea still owns
// raw mode, the hidden cursor, bracketed paste, and their restoration on
// every exit path (normal, error, signal, panic); this method is still the
// lifecycle construction boundary. No mouse mode of any kind is requested —
// not 1000, 1002, 1003, 1006, not alternate scroll (1007), not cell motion —
// which is what leaves the wheel, drag selection and copy with the terminal.
// Note that tea.WithoutRenderer is deliberately not used here: it makes
// Bubble Tea skip terminal initialisation entirely, including raw mode, which
// would leave the composer unable to read a single keypress.
//
// The transcript is streamed rather than repainted: sealed rows are handed to
// the renderer as print lines and the renderer writes them above its frame,
// so the terminal scrolls them into the scrollback. Only the live region —
// the in-flight answer, the status line and the composer — is ever repainted,
// and it is bounded to a known row count, so nothing already in the scrollback
// is rewritten.
func (p *Program) RunLive() error {
	defer p.closeDone()

	painter := newLivePainter()
	p.live = painter
	_, err := tea.NewProgram(p).Run()

	// Bubble Tea has stopped and restored the terminal, so what follows is
	// plain output: an answer whose turn was interrupted, or one still hidden
	// behind an overlay, still reaches the scrollback.
	painter.sealToTerminal(p.termOut, p.state)
	return err
}

// closeDone releases tools blocked past shutdown. Idempotent: Run defers it,
// and headless tests drive shutdown without Run.
func (p *Program) closeDone() {
	p.doneOnce.Do(func() {
		if p.catalogCancel != nil {
			p.catalogCancel()
		}
		if closer, ok := p.promptHistory.(interface{ Close() error }); ok {
			_ = closer.Close()
		} else if flusher, ok := p.promptHistory.(promptHistoryFlusher); ok {
			_ = flusher.Flush()
		}
		close(p.done)
	})
}

// waitForEvent returns a command that blocks for the next state event.
func (p *Program) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-p.events
		if !ok {
			return nil
		}
		return teaEvent{event: event}
	}
}

// waitForApproval returns a command that blocks for the next question.
func (p *Program) waitForApproval() tea.Cmd {
	return func() tea.Msg {
		request, ok := <-p.approvals
		if !ok {
			return nil
		}
		return approvalPrompt{request: request}
	}
}

func tick() tea.Cmd {
	return tea.Tick(spinnerInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// handleProposalKey answers a request-derived proposal. The interaction is
// the permission dialog's own: arrows move, Enter confirms, Esc cancels. The
// pending turn has not started, so cancelling simply closes the prompt and
// leaves the typed request in the composer.
func (p *Program) handleProposalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k", "shift+tab", "left", "h":
		p.state.Apply(ApprovalMoved{Delta: -1})
		return p, nil
	case "down", "j", "tab", "right", "l":
		p.state.Apply(ApprovalMoved{Delta: 1})
		return p, nil
	case "enter":
		return p.resolveProposal()
	case "esc", "ctrl+c":
		p.state.Apply(ApprovalAnswered{})
		return p, nil
	}
	return p, nil
}

// resolveProposal records the human's choice as trusted structured state and
// releases the turn it was blocking.
//
// The choice is stored outside the transcript and the model input, then
// rendered into the turn's system context with control-plane provenance. It
// never becomes conversational text the model could reinterpret.
func (p *Program) resolveProposal() (tea.Model, tea.Cmd) {
	pending := p.state.Pending
	if pending == nil || pending.Proposal == nil {
		return p, nil
	}
	choice, ok := pending.Proposal.Choice(pending.Cursor)
	if !ok {
		return p, nil
	}
	resolution := &ProposalResolution{
		Option:    choice.ID,
		Label:     choice.Label,
		Directive: choice.Directive,
		Dimension: pending.Proposal.Dimension,
		Subject:   pending.Proposal.Subject,
		Question:  pending.Proposal.Question,
		Request:   pending.Proposal.Request,
	}
	p.mu.Lock()
	p.proposal = resolution
	p.mu.Unlock()
	p.state.Apply(ApprovalAnswered{})
	p.state.Apply(Notice{Text: "proposal: " + choice.Label + " (your choice)"})
	return p, p.startTurn(pending.Proposal.Request)
}

// proposalFor returns the standing human proposal decision for a turn's
// request, or nil when none applies.
//
// A decision serves the exact request it answered and explicit continuations
// of the same execution ("continue", ...). Any other request clears it, so a
// recorded choice can never constrain an unrelated turn. It is called from
// submit (to skip re-proposing a resolved choice) and from runTurn (to consume
// the choice), both under mu.
func (p *Program) proposalFor(text string) *ProposalResolution {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proposal == nil {
		return nil
	}
	if text == p.proposal.Request || continuationPrompt(text) {
		return p.proposal
	}
	p.proposal = nil
	return nil
}

// proposalSystemBlock renders a recorded human proposal decision as a
// control-plane-authored system block for the turn that decision releases.
// The copy itself lives in the prompt package; this only maps trusted state
// onto it. A delegated choice yields no block: the human explicitly granted
// the authority, and there is nothing to convey.
func proposalSystemBlock(res *ProposalResolution) *ai.SystemPromptBlock {
	if res == nil {
		return nil
	}
	text, ok := prompt.ProposalDecision(res.Label, res.Directive, res.Question, res.Subject)
	if !ok {
		return nil
	}
	return &ai.SystemPromptBlock{Text: text}
}

// submit runs one turn off the UI goroutine.
//
// Prompts typed while a turn runs wait their turn instead of racing it:
// concurrent Loop.Run calls would interleave history writes and tool batches,
// which is the race this queue exists to prevent. When the turn ends, the
// returned message drains the queue on the update goroutine.
func (p *Program) submit(text string) tea.Cmd {
	// $sup is routed to the ephemeral overlay before it can become a turn:
	// it must never persist, never touch history, and never reach the store
	// (§10.2). A bare "$sup" opens the panel with input focus and starts
	// no provider call; "$sup <request>" opens it and submits directly.
	if question, ok := ParseSupport(text); ok {
		if p.support == nil {
			return p.startTurn(text)
		}
		p.openSupport(question)
		if strings.TrimSpace(question) == "" || p.state.SupportBusy() {
			return nil
		}
		return p.startSupportRun(strings.TrimSpace(question))
	}
	// A standing human proposal decision answers the turn before prediction
	// runs: re-proposing a resolved choice would interrogate the user about a
	// decision they already made. proposalFor also drops a decision its request
	// no longer matches, so a stale choice can never constrain an unrelated
	// turn.
	if p.proposalFor(text) != nil {
		return p.startTurn(text)
	}
	// Ask the workspace owner what it would propose for this request before
	// the turn begins. Predicting changes nothing: it only opens the decision
	// surface, and the turn starts when that decision is resolved, so no
	// provider request, tool, or mutation can run while it is pending
	// (I2, I4-I6). A program without a Predict hook keeps the pre-proposal
	// behavior and starts the turn immediately.
	if p.workspace.Predict != nil && p.state.Transition == nil && p.state.Pending == nil {
		if proposed, ok := p.workspace.Predict(text); ok {
			if proposed.Kind == TransitionKindAmbiguous {
				p.openAuthorityChoice(text, proposed.Reason)
				return nil
			}
			if proposed.Kind == TransitionKindProposal && proposed.Proposal != nil {
				labels := make([]string, len(proposed.Proposal.Options))
				for i, option := range proposed.Proposal.Options {
					labels[i] = option.Label
				}
				// The proposal uses the existing permission boundary: the
				// same dialog, the same frame, the same arrow/enter/esc
				// interaction. Only its question and choices differ.
				p.state.Apply(ApprovalAsked{
					Prompt:  "Request",
					Summary: text,
					Reason:  proposed.Proposal.Reason,
					// A proposal is a decision, not a destructive call: the
					// amber read accent keeps it in the permission family.
					Mutating: false,
					Choices:  labels,
					Proposal: proposed.Proposal,
				})
				return nil
			}
			p.state.Apply(TransitionProposed{
				Text:           text,
				Current:        proposed.Current,
				Proposed:       proposed.Proposed,
				CurrentAccent:  proposed.CurrentAccent,
				ProposedAccent: proposed.ProposedAccent,
				Intent:         proposed.Intent,
				Strength:       proposed.Strength,
				Question:       proposed.Question,
				Reason:         proposed.Reason,
			})
			return nil
		}
	}
	return p.startTurn(text)
}

// startTurn runs one turn off the UI goroutine. It is submit's tail without
// the prediction step, so a resolved proposal replays its prompt without
// being re-proposed.
func (p *Program) startTurn(text string) tea.Cmd {
	return p.startTurnWithChoice(text, "")
}

// startTurnWithChoice runs a request under the workspace explicitly chosen in
// the centered selector. The choice is captured by the command, not shared
// state, so another input cannot consume or overwrite it before execution.
func (p *Program) startTurnWithChoice(text string, choice workspace.Workspace) tea.Cmd {
	return func() tea.Msg {
		p.runTurnWithChoice(text, choice)
		return turnFinishedMsg{}
	}
}

// resolveTransition consumes the pending proposal and starts the turn.
//
// Accepting routes the workspace move through the owner's Switch — the one
// authoritative seam — and only then runs the turn. Rejecting cancels the
// request; running it under a different workspace would make the visual and
// runtime contract disagree (I2, I3, I7).
//
// The prompt is cleared before the switch so the chrome the switch publishes
// cannot race a stale proposal back onto the screen.
func (p *Program) resolveTransition(accept bool) (tea.Model, tea.Cmd) {
	pending := p.state.Transition
	if pending == nil {
		return p, nil
	}
	text, proposed := pending.Text, pending.Proposed
	p.state.Apply(TransitionResolved{})
	if !accept {
		p.restoreAuthorityRequest(text)
		p.state.Apply(Notice{Text: "request cancelled; choose its workspace explicitly, then resend"})
		return p, nil
	}
	if p.workspace.Switch == nil {
		p.state.Apply(Notice{Text: "workspace switching is unavailable"})
		p.restoreAuthorityRequest(text)
		return p, nil
	}
	if p.workspace.Current != nil && strings.EqualFold(p.workspace.Current(), proposed) {
		// Already there (an external switch landed between prediction and
		// decision): run the turn without a redundant transition.
		return p, p.startTurn(text)
	}
	if err := p.workspace.Switch(proposed); err != nil {
		// The accepted authority is not in force, so running the turn would
		// silently execute under the workspace the user declined. Report and
		// drop the prompt; the user can resend.
		p.state.Apply(Notice{Text: "workspace: " + err.Error()})
		p.restoreAuthorityRequest(text)
		return p, nil
	}
	return p, p.startTurn(text)
}

// supportIdentity reads the effective SUP model and effort for display.
// Unknown stays unknown: the overlay says so rather than fabricating it.
func (p *Program) supportIdentity() (model, effort string) {
	if p.supportInfo == nil {
		return "", ""
	}
	return p.supportInfo()
}

// openSupport opens the overlay, optionally seeding it for a direct submit.
// Opening never invokes the provider: the call starts only when a
// non-empty request is submitted. The invoking line leaves the main
// composer: it belongs to the support input now, never to the next
// workspace turn.
func (p *Program) openSupport(question string) {
	model, effort := p.supportIdentity()
	p.state.Apply(SupportOpened{Model: model, Effort: effort})
	p.state.Apply(InputChanged{Text: ""})
	if trimmed := strings.TrimSpace(question); trimmed != "" && !p.state.SupportBusy() {
		p.state.Apply(SupportInputChanged{Text: trimmed, Cursor: len([]rune(trimmed))})
	}
}

// openSupportChord opens the ephemeral $sup surface from the Ctrl+S chord.
//
// It differs from the "$sup" line in one deliberate way: the chord fires while
// a prompt is still being composed, so it must not consume or clear the main
// composer — the user keeps the draft they were writing. The overlay opens
// with input focus and starts no provider call until a request is submitted.
// A missing handler reports the surface unavailable rather than opening an
// overlay that could never answer.
func (p *Program) openSupportChord() {
	if p.support == nil {
		p.state.Apply(Notice{Text: "$sup is unavailable"})
		return
	}
	model, effort := p.supportIdentity()
	p.state.Apply(SupportOpened{Model: model, Effort: effort})
}

// startSupportRun begins one support turn for a non-empty request. It runs
// on the update goroutine: the Started event (with its sequence identity)
// applies synchronously, and the provider call runs off-goroutine.
func (p *Program) startSupportRun(request string) tea.Cmd {
	if p.support == nil || p.state.SupportPanel == nil || p.state.SupportBusy() {
		return nil
	}
	p.state.SupportSeq++
	seq := p.state.SupportSeq
	model, effort := p.supportIdentity()
	p.state.Apply(SupportStarted{Seq: seq, Request: request, Model: model, Effort: effort})

	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.supportCancel = cancel
	p.mu.Unlock()

	support := p.support
	return func() tea.Msg {
		p.runSupport(ctx, seq, request, support)
		return turnFinishedMsg{}
	}
}

// runSupport executes one ephemeral $sup exchange off the update goroutine.
//
// Nothing here touches p.history, the store, or the session: that isolation
// is the whole contract of the surface (I6, I17). Answer chunks stream
// into the overlay through SupportDelta; the terminal event carries the
// turn's sequence so stale turns cannot mutate the panel.
func (p *Program) runSupport(ctx context.Context, seq int, question string, support func(ctx context.Context, question string, onDelta func(string)) (string, error)) {
	defer func() {
		p.mu.Lock()
		p.supportCancel = nil
		p.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Chunks stream into the overlay as they arrive; the completed answer
	// then publishes only its unstreamed tail, so visible text is never
	// duplicated. Transports that never token-stream publish everything
	// in the terminal event.
	var streamed string
	answer, err := support(ctx, question, func(delta string) {
		if delta == "" {
			return
		}
		for _, chunk := range splitAssistantChunks(delta) {
			if p.publishOK(SupportDelta{Seq: seq, Text: chunk}) {
				streamed += chunk
			}
		}
	})
	if err != nil {
		// A cancelled context is an interruption, not a failure: the
		// single semantic cancellation already reached the panel through
		// SupportCancelledEvent, so only real failures publish here. The
		// reducer makes a duplicate cancellation a no-op either way.
		if errors.Is(err, context.Canceled) {
			p.publish(SupportCancelledEvent{Seq: seq, Text: "cancelled"})
		} else {
			p.publish(SupportFailedEvent{Seq: seq, Error: err.Error()})
		}
		return
	}
	rest := answer
	if streamed != "" {
		if strings.HasPrefix(answer, streamed) {
			rest = answer[len(streamed):]
		} else if n := commonPrefixLen(answer, streamed); n > 0 {
			rest = answer[n:]
		}
	}
	p.publish(SupportDone{Seq: seq, Text: rest})
}

// handleSupportKey routes keys while the support overlay is open.
//
// The contract is focus-explicit with no printable shortcuts: while a turn
// runs, only Ctrl+C (cancel) and Esc (cancel) own the keyboard; otherwise
// every printable key — including s, c, r, e — is composer text. Enter
// submits, Esc closes (or cancels when running), Ctrl+C cancels a running
// turn, and Tab stays a no-op until another focusable control exists.
func (p *Program) handleSupportKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	panel := p.state.SupportPanel
	if panel == nil {
		return p, nil
	}
	// The response viewport scrolls in every state, including mid-turn: a
	// long streaming answer stays inspectable while it lands.
	switch msg.String() {
	case "pgup", "shift+up":
		p.state.Apply(SupportScrolled{Delta: p.supportPageStep()})
		return p, nil
	case "pgdown", "shift+down":
		p.state.Apply(SupportScrolled{Delta: -p.supportPageStep()})
		return p, nil
	case "ctrl+end":
		p.state.Apply(SupportScrollToBottom{})
		return p, nil
	}
	if panel.Status == SupportRunning {
		switch msg.String() {
		case "ctrl+c", "esc":
			p.cancelSupportNow("cancelled")
			return p, nil
		}
		return p, nil
	}
	switch msg.String() {
	case "enter":
		text := strings.TrimSpace(panel.Input)
		if text == "" {
			return p, nil
		}
		return p, p.startSupportRun(text)
	case "esc", "ctrl+c":
		p.state.Apply(SupportClosed{})
		return p, nil
	case "alt+enter", "ctrl+j":
		p.state.Apply(SupportInputChanged{
			Text:   insertAt(panel.Input, panel.Cursor, "\n"),
			Cursor: panel.Cursor + 1,
		})
		return p, nil
	case "backspace", "ctrl+h":
		text, cursor := deleteBefore(panel.Input, panel.Cursor)
		p.state.Apply(SupportInputChanged{Text: text, Cursor: cursor})
		return p, nil
	case "delete", "ctrl+d":
		text, cursor := deleteAt(panel.Input, panel.Cursor)
		p.state.Apply(SupportInputChanged{Text: text, Cursor: cursor})
		return p, nil
	case "left", "ctrl+b":
		p.state.Apply(SupportInputChanged{Text: panel.Input, Cursor: panel.Cursor - 1})
		return p, nil
	case "right", "ctrl+f":
		p.state.Apply(SupportInputChanged{Text: panel.Input, Cursor: panel.Cursor + 1})
		return p, nil
	case "home", "ctrl+a":
		p.state.Apply(SupportInputChanged{Text: panel.Input, Cursor: lineStart(panel.Input, panel.Cursor)})
		return p, nil
	case "end", "ctrl+e":
		p.state.Apply(SupportInputChanged{Text: panel.Input, Cursor: lineEnd(panel.Input, panel.Cursor)})
		return p, nil
	case "ctrl+u":
		text, cursor := killToStart(panel.Input, panel.Cursor)
		p.state.Apply(SupportInputChanged{Text: text, Cursor: cursor})
		return p, nil
	case "ctrl+k":
		text, _ := killToEnd(panel.Input, panel.Cursor)
		p.state.Apply(SupportInputChanged{Text: text, Cursor: panel.Cursor})
		return p, nil
	case "ctrl+w":
		text, cursor := killWordBack(panel.Input, panel.Cursor)
		p.state.Apply(SupportInputChanged{Text: text, Cursor: cursor})
		return p, nil
	case "up", "ctrl+p":
		if line, ok := lineAbove(panel.Input, panel.Cursor); ok {
			p.state.Apply(SupportInputChanged{Text: panel.Input, Cursor: line})
		}
		return p, nil
	case "down", "ctrl+n":
		if line, ok := lineBelow(panel.Input, panel.Cursor); ok {
			p.state.Apply(SupportInputChanged{Text: panel.Input, Cursor: line})
		}
		return p, nil
	case "tab", "shift+tab":
		// One focusable control exists (the input): Tab stays a no-op
		// rather than yielding focus nowhere.
		return p, nil
	default:
		// Every printable key is composer text. Space arrives as
		// KeySpace, not KeyRunes: without this branch no space can ever
		// be typed into the support input.
		switch msg.Type {
		case tea.KeyRunes, tea.KeySpace:
			text := string(msg.Runes)
			if msg.Type == tea.KeySpace {
				text = " "
			}
			if text == "" {
				return p, nil
			}
			composed := insertAt(panel.Input, panel.Cursor, text)
			p.state.Apply(SupportInputChanged{Text: composed, Cursor: panel.Cursor + len([]rune(text))})
		}
		return p, nil
	}
}

// drainQueue starts the oldest queued prompt. It runs on the update
// goroutine, so the shift applies directly.
func (p *Program) drainQueue() (tea.Model, tea.Cmd) {
	if len(p.state.Queued) == 0 {
		return p, nil
	}
	next := p.state.Queued[0]
	p.state.Apply(QueueShifted{})
	return p, p.submit(next)
}

// shellSyntaxHint is shown when the composer submits a bare "!" with no
// command: it teaches the syntax without running an empty shell.
const shellSyntaxHint = "! <command> — run a shell command in the workspace"

// submitEscape handles a "!<command>" line: it gates on the active authority
// and either runs the command or opens the confirmation it needs.
func (p *Program) submitEscape(command string) tea.Cmd {
	if strings.TrimSpace(command) == "" {
		p.state.Apply(Notice{Text: shellSyntaxHint})
		return nil
	}
	if p.shell.Run == nil {
		p.state.Apply(Notice{Text: "shell escape is unavailable"})
		return nil
	}
	if p.state.runningShell() != nil {
		p.state.Apply(Notice{Text: "a shell command is already running"})
		return nil
	}
	// The security boundary decides first when one is installed: a denied
	// command never runs, and an approved one uses the same confirmation
	// dialog a tool approval does.
	if p.shell.Authorize != nil {
		allowed, confirm, reason := p.shell.Authorize(command)
		if !allowed {
			if strings.TrimSpace(reason) == "" {
				reason = "command denied"
			}
			p.state.Apply(Notice{Text: reason})
			return nil
		}
		if confirm && !p.sessionAllowedFor(shellApprovalTool) {
			return p.confirmEscape(command)
		}
		return p.startEscape(command)
	}
	// Fallback for a caller that installs no boundary (tests): an unclassified
	// command is treated as mutating, so ASK always asks rather than running a
	// possible mutation unconfirmed.
	mutating := true
	if p.shell.Mutates != nil {
		mutating = p.shell.Mutates(command)
	}
	if exec.Decide(p.state.Status.Workspace, mutating) == exec.Confirm && !p.sessionAllowedFor(shellApprovalTool) {
		return p.confirmEscape(command)
	}
	return p.startEscape(command)
}

// confirmEscape asks the user to approve a mutating command in ASK. It reuses
// the approval modal, with a shell-specific header, so the keyboard contract
// is identical to a tool approval.
func (p *Program) confirmEscape(command string) tea.Cmd {
	p.pendingEscape = command
	p.state.Apply(ApprovalAsked{
		ToolName: "shell",
		Prompt:   "Run shell command",
		Summary:  command,
		Mutating: true,
		Choices:  approvalChoices,
	})
	return nil
}

// startEscape launches an escape command off the update goroutine. The
// command's output streams back as ShellDelta events and closes with a single
// ShellFinished.
func (p *Program) startEscape(command string) tea.Cmd {
	run := p.shell.Run
	if run == nil {
		p.state.Apply(Notice{Text: "shell escape is unavailable"})
		return nil
	}
	p.mu.Lock()
	p.shellSeq++
	seq := p.shellSeq
	ctx, cancel := context.WithCancel(context.Background())
	p.shellCancel = cancel
	p.shellCancelSeq = seq
	p.mu.Unlock()

	p.state.Apply(InputChanged{Text: ""})
	p.state.Apply(ShellStarted{Command: command, At: time.Now(), Seq: seq})
	return func() tea.Msg {
		p.runEscape(ctx, seq, command, run)
		return nil
	}
}

// runEscape executes one command and publishes its lifecycle. It runs off the
// update goroutine; every state change leaves through the event channel.
func (p *Program) runEscape(ctx context.Context, seq int, command string, run func(context.Context, string, func(string)) (exec.EscapeResult, error)) {
	defer func() {
		p.mu.Lock()
		if p.shellCancelSeq == seq {
			p.shellCancel = nil
		}
		p.mu.Unlock()
	}()

	result, err := run(ctx, command, func(chunk string) {
		if chunk == "" {
			return
		}
		p.publish(ShellDelta{Text: chunk, Seq: seq})
	})
	if err != nil {
		text := err.Error()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			text = "cancelled"
		}
		p.publish(ShellFinished{ExitCode: -1, Err: text, At: time.Now(), Seq: seq})
		return
	}
	p.publish(ShellFinished{
		ExitCode:  result.ExitCode,
		Duration:  result.Duration,
		Truncated: result.Truncated,
		At:        time.Now(),
		Seq:       seq,
	})
}

// cancelShellNow aborts the in-flight escape, if any. The worker's own
// ShellFinished event marks the block, so nothing is synthesised here.
func (p *Program) cancelShellNow() {
	p.mu.Lock()
	cancel := p.shellCancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// runTurn executes a single turn.
//
// It never touches the queue: shifting state off the update goroutine would
// race the renderer, so the shift waits for turnFinishedMsg.
func (p *Program) runTurn(text string) {
	p.runTurnWithChoice(text, "")
}

func (p *Program) runTurnWithChoice(text string, choice workspace.Workspace) {
	p.mu.Lock()
	p.turnSeq++
	seq := p.turnSeq
	p.mu.Unlock()

	// The control plane validates the active authority before the model is asked.
	// A request the selected workspace cannot answer is refused unless this exact
	// turn carries the user's explicit choice from the authority selector.
	turn := TurnAuthority{}
	if choice.Valid() {
		// The selector is the authority decision. Classification remains useful
		// task context, but it cannot veto or replace the user's chosen workspace;
		// tool admission still enforces that workspace's contract.
		turn = TurnAuthority{
			Active: choice.Label(),
			Intent: strings.ToUpper(string(workspace.Classify(text))),
		}
	} else if p.workspace.PrepareTurn != nil {
		turn = p.workspace.PrepareTurn(text)
	}
	if turn.Blocked {
		p.publish(UserSubmitted{Text: text, Seq: seq, At: time.Now()})
		reason := turn.Reason
		if reason == "" {
			reason = "request refused by workspace authority"
		}
		p.publish(Notice{Text: reason})
		p.publish(AssistantDone{Seq: seq})
		return
	}
	p.publish(UserSubmitted{Text: text, Seq: seq, At: time.Now()})

	// The completion contract is extracted from the user's own text before any
	// execution. It is the task's objective and the evidence boundary; the
	// model cannot add to or reinterpret it.
	intent := workspace.BuildIntentContract(text)

	ctx, cancel := context.WithCancel(context.Background())
	p.turnTerminal.Store(turnTerminalOpen)
	p.mu.Lock()
	p.cancelTurn = cancel
	history := append([]agent.Message(nil), p.history...)
	// Capture the session this turn belongs to: a /new during the turn must
	// not let this turn's history or transcript leak into the new session.
	turnStore := p.store
	var turnSessID int64
	if p.session != nil {
		turnSessID = p.session.ID
	}
	p.mu.Unlock()

	// persistTo appends to the turn's own session, even if the live session
	// moved on while the turn was running.
	persistTo := func(msg agent.Message) {
		if turnStore == nil || turnSessID == 0 {
			return
		}
		stored, err := session.FromAgentMessage(msg)
		if err != nil || stored.Role == "" {
			return
		}
		if err := turnStore.Append(turnSessID, stored); err != nil {
			p.publish(Notice{Text: "could not save turn: " + err.Error()})
		}
	}

	// abortTurn ends a turn that never started execution: it reports the
	// reason and clears the cancellation handle. The durable task is left
	// exactly as it was, and the unused context is left to be collected; the
	// only cancellation path that can store the terminal claim is
	// cancelTurnNow, which is scoped to the active turn.
	abortTurn := func(notice string) {
		p.publish(Notice{Text: notice})
		p.publish(AssistantDone{Seq: seq})
		p.mu.Lock()
		p.cancelTurn = nil
		p.mu.Unlock()
	}

	// A persisted task is resumable, never mandatory. An explicit continuation
	// reuses the persisted contract and its pre-run target baseline; any other
	// prompt is a new intent and starts a new current-session task. The previous
	// task remains durable (detached, not deleted) and stays reachable through
	// /task list and /task resume. Relatedness — same repository, same files —
	// never implies continuation.
	var targetSnapshot *agent.TargetSnapshot
	if p.task.Current != nil {
		if current, ok := p.task.Current(); ok && !taskStatusTerminal(current.Status) {
			if continuationPrompt(text) {
				if !current.HasContract {
					abortTurn("an open task has no persisted completion contract; run /task cancel to abandon it, or start a new task with /task list to preserve it")
					return
				}
				intent = current.Contract
				if current.Snapshot != nil {
					cloned := *current.Snapshot
					if p.completionRoot != "" {
						cloned.Root = p.completionRoot
					}
					targetSnapshot = &cloned
				}
				p.recordTaskDecision("EXPLICIT_RESUME")
			} else {
				// New intent: detach the durable task so it survives the
				// session boundary without owning the new intent, then
				// proceed with the fresh contract. COMPLETED/FAILED/CANCELLED
				// never reach here (terminal), so no continuation prompt is
				// ever shown for them.
				if p.task.Detach != nil {
					detached := p.task.Detach()
					if detached.Err != "" {
						abortTurn("task state was not recorded: " + detached.Err)
						return
					}
					if detached.ID != "" {
						p.PublishTask(detached)
					}
				}
				p.recordTaskDecision("NEW_INTENT")
			}
		} else {
			p.recordTaskDecision("NEW_INTENT")
		}
	} else {
		p.recordTaskDecision("NEW_INTENT")
	}

	// The completion contract's targets are captured before execution; the
	// snapshot is the pre-run state the evaluator compares against. A resumed
	// task keeps the baseline captured before its first run: recapturing an
	// already changed file would erase the change the contract must see. No
	// second repository index is built: the resolver is the symbols tool's own
	// index.
	if targetSnapshot == nil && intent.Kind == workspace.ObjectiveMutation && p.completionRoot != "" {
		targetSnapshot = agent.CaptureTargetsContext(ctx, p.completionRoot, intent, p.completionResolver)
	}

	// A turn is a task boundary: record (or resume) the durable task before
	// any provider call, so an interruption during this turn has something to
	// checkpoint. Bookkeeping only — it neither gates nor authorizes the turn.
	if p.task.Begin != nil {
		projection := p.task.Begin(intent, targetSnapshot)
		p.PublishTask(projection)
		if projection.Err != "" {
			abortTurn("task state was not recorded: " + projection.Err)
			return
		}
	}

	// The budget is granted at the turn boundary, before any execution. The
	// spawner shares this instance through the host's holder, so the same
	// grant covers the root and every descendant. A new user turn is a new
	// grant; the task ledger still carries the cumulative consumption.
	var runBudget *budget.Budget
	if p.task.RunBudget != nil {
		runBudget = p.task.RunBudget()
		if runBudget != nil {
			p.loop.SetBudget(runBudget)
		}
	}

	// The task shape and evidence state are the runtime's own model of what
	// this turn needs. They narrow the action surface and build a smaller
	// context view; they never authorize a call. The security boundary still
	// evaluates every tool that remains, and completion still evaluates
	// evidence the runtime observed. The shape is built after the fresh run
	// grant is installed so an exhausted prior turn cannot poison this one.
	authorityLabel := strings.ToLower(strings.TrimSpace(turn.Active))
	if authorityLabel == "" && p.workspace.Current != nil {
		authorityLabel = strings.ToLower(strings.TrimSpace(p.workspace.Current()))
	}
	facts := cognition.ProbeFacts(p.completionRoot)
	shape := cognition.BuildShape(intent, cognition.ShapeParams{
		Authority: authorityLabel,
		Intent:    turn.Intent,
		Facts:     facts,
		Snapshot:  targetSnapshot,
		Budget:    cognition.BudgetShapeFrom(p.loop.Budget().Snapshot()),
	})
	if p.task.Evidence != nil {
		projector := cognition.NewProjector(shape, p.task.Evidence(), facts)
		p.loop.SetProjectionHook(projector.Project)
		p.loop.SetProjectionReportHook(p.task.RecordProjection)
	} else {
		// A program built without durable evidence keeps the pre-Phase-9
		// behaviour exactly: full context and the full tool set.
		p.loop.SetProjectionHook(nil)
		p.loop.SetProjectionReportHook(nil)
	}

	user := agent.UserMessage{Text: text}
	persistTo(user)

	// The grounding belongs to this turn. It is cleared when runTurn returns,
	// so a later turn never inherits an objective it did not state.
	p.loop.SetGrounding(intent.Grounding)
	defer p.loop.SetGrounding(nil)

	request := append(append([]agent.Message(nil), history...), user)

	// Progress is forwarded live so a slow tool is visible while it runs.
	// The loop's hook is installed for the duration of the turn rather than
	// at construction, because the program is the only thing that can turn
	// it into UI events.
	//
	// Visible model text is forwarded the same way: each delta publishes as
	// it arrives, so the answer reveals sequentially while the stream is
	// open instead of dumping as one block when the turn completes. Only
	// deltas that reach the event channel count as revealed — a dropped
	// delta stays the completed turn's job below, never silently lost.
	var live strings.Builder
	p.loop.SetTextDeltaHook(func(delta string) {
		if delta == "" {
			return
		}
		for _, chunk := range splitAssistantChunks(delta) {
			if p.publishOK(AssistantDelta{Text: chunk, Seq: seq}) {
				live.WriteString(chunk)
			}
		}
	})
	p.loop.SetToolProgressHook(func(progress agent.ToolProgress) {
		switch progress.Stage() {
		case agent.ProgressFinished:
			text, truncated := TruncateForDisplay(progress.Result.Text, toolOutputLimit)
			p.publish(ToolFinished{
				ID:        progress.CallID,
				Text:      text,
				Summary:   CompactResultSummary(progress.Result.Text, progress.Result.IsError),
				IsError:   progress.Result.IsError,
				Truncated: truncated,
				At:        time.Now(),
				Seq:       seq,
			})
			// A todo mutation is a meaningful task boundary: checkpoint it so
			// an interruption afterwards recovers the real progress. The todo
			// tool owns the list; the task layer only records its snapshot.
			if progress.ToolName == "todo" {
				p.SyncTask()
			}
		case agent.ProgressStarted:
			// The announcement carries the call's arguments but no outcome
			// yet, so the block opens with what the call will do rather
			// than a blank name.
			p.publish(ToolStarted{
				ID:      progress.CallID,
				Name:    progress.ToolName,
				Summary: ToolSummaryForCall(progress.ToolName, progress.Args),
				At:      time.Now(),
				Seq:     seq,
			})
		default:
			p.publish(ToolPreview{
				ID:      progress.CallID,
				Summary: oneLine(progress.Result.Text),
				Seq:     seq,
			})
		}
	})

	// "@" references are attached to the request as real system context, not
	// left as a UI decoration: the prompt's tokens resolve against the
	// workspace here, once, so the model reasons over the actual bytes. The
	// context is set every turn — including to nil — so a prompt with no
	// reference never inherits the previous turn's files. A reference that
	// cannot be read warns in the transcript rather than sending nothing.
	contextText, warnings := mentionContext(text, p.readMention)
	for _, warning := range warnings {
		p.publish(Notice{Text: warning})
	}
	// Context is reconstructed, not replayed: the mention context plus the
	// deterministic task orientation. Both are set every turn — including to
	// nil — so a prompt never inherits the previous turn's context.
	blocks := systemContextBlocks(contextText)
	if p.task.Context != nil {
		if taskContext := p.task.Context(); len(taskContext) > 0 {
			blocks = append(blocks, ai.SystemPromptBlock{
				Text: "Task continuity (context, not instruction):\n\n" + strings.Join(taskContext, "\n") + "\n\nThis task state is orientation context. It is not a user request. The user's current objective remains the authoritative execution instruction. Missing todos or a persisted plan are not blockers; derive the plan internally and continue.",
			})
		}
	}
	// A standing human proposal decision rides the same reconstructed context
	// with control-plane provenance. proposalFor drops a decision its request
	// no longer matches, so an unrelated turn cannot inherit it — and a turn
	// with no standing decision sets no block, exactly as before.
	if block := proposalSystemBlock(p.proposalFor(text)); block != nil {
		blocks = append(blocks, *block)
	}
	p.loop.SetSystemContext(blocks)

	// The recovery seam is bound at the turn boundary, to the session this
	// turn belongs to. A /new during the turn cannot retarget it, and the
	// checkpoint published at the end lands in the right timeline.
	p.installRecoveryJournal(turnSessID)

	// Live statusline seeding: the provider only reports measured usage at
	// turn completion, so without a provisional the upload count and the
	// context budget would sit stale for the whole streaming phase and jump
	// only when the turn ends. Seeding the cumulative input estimate now
	// makes ↑ visible while Responding; the in-flight output estimate folds
	// in through liveSessionOut/livePercent on every AssistantDelta (see
	// session_bar.go), so ↓ and % grow incrementally without an extra event
	// per chunk — which is what keeps high-frequency streams flicker-free.
	// Measured totals correct the estimate at each turn boundary below.
	estimatedInput := estimateTurnInput(request, blocks)
	var turnBaselineIn, turnBaselineOut int
	var turnSessionStart time.Time
	p.mu.Lock()
	if p.sessionStart.IsZero() {
		p.sessionStart = time.Now()
	}
	turnBaselineIn, turnBaselineOut = p.sessionIn, p.sessionOut
	turnSessionStart = p.sessionStart
	p.mu.Unlock()
	if estimatedInput > 0 {
		p.publish(SessionMetricsUpdated{
			Input:   turnBaselineIn + estimatedInput,
			Output:  turnBaselineOut,
			Elapsed: time.Since(turnSessionStart),
		})
	}
	// Intermediate measured totals: a tool loop runs several provider calls
	// inside one Run, and waiting for the final accumulate would leave the
	// statusline stale between tool executions. Reporting each turn's
	// measured usage as it completes keeps ↑/↓/% moving while tools run.
	// The closure only publishes absolute cumulative totals for State; the
	// Program counters still accumulate once at the end, so nothing is
	// double-counted.
	var seenIn, seenOut int
	p.loop.SetTurnHook(func(turn agent.Turn) {
		seenIn += turn.Assistant.Usage.Input
		seenOut += turn.Assistant.Usage.Output
		if seenIn == 0 && seenOut == 0 && turn.Assistant.Usage.Total == 0 {
			return
		}
		p.publish(SessionMetricsUpdated{
			Input:   turnBaselineIn + seenIn,
			Output:  turnBaselineOut + seenOut,
			Elapsed: time.Since(turnSessionStart),
		})
		if turn.Assistant.Usage.Total > 0 {
			p.publish(ContextUpdated{Used: turn.Assistant.Usage.Total, Window: p.window})
		}
	})

	turns, err := p.loop.Run(ctx, request)
	// The run published its edit checkpoint, if any. Clearing the journal
	// keeps a later code path from capturing under this turn's scope.
	p.loop.SetJournal(nil)

	// Record what this run consumed before any lifecycle transition persists
	// it. The ledger is cumulative across resumes; the run grant itself is
	// never reused for another turn.
	if runBudget != nil && p.task.RecordBudget != nil {
		p.task.RecordBudget(runBudget.Snapshot())
	}

	if p.usage != nil {
		p.usage.Add(turns)
	}
	// Progressive disclosure reads the live session's own accumulation, not
	// the lifetime /usage ledger: a cleared or resumed session starts clean.
	p.accumulateSessionMetrics(turns)
	// A provisional estimate must never survive as measured when the
	// provider reported nothing: unknown stays absent rather than rendering
	// a fabricated upload. When the measured totals did not move past the
	// baseline, revert the State to the baseline so the estimate does not
	// linger after the turn.
	if estimatedInput > 0 {
		p.mu.Lock()
		finalIn, finalOut := p.sessionIn, p.sessionOut
		p.mu.Unlock()
		if finalIn == turnBaselineIn && finalOut == turnBaselineOut {
			p.publish(SessionMetricsUpdated{
				Input:   turnBaselineIn,
				Output:  turnBaselineOut,
				Elapsed: time.Since(turnSessionStart),
			})
		}
	}

	if err != nil {
		// A cancelled turn is an interruption, not a failure: reporting
		// it as an error would read as though something broke. The single
		// semantic cancellation already reached the transcript through
		// TurnCancelled, so only real failures publish here.
		if errors.Is(err, context.Canceled) {
			p.publish(TurnCancelled{Seq: seq, Text: "cancelled"})
		} else {
			p.publish(Notice{Text: "error: " + err.Error()})
		}
	}

	// A loop that stops at the wall-clock bound returns nil; the deadline is
	// still authoritative. Represent it as the resource stop it is, so the
	// task is paused unless already-complete evidence can justify completion.
	if err == nil {
		if deadline := p.loop.Deadline(); !deadline.IsZero() && !time.Now().Before(deadline) {
			err = context.DeadlineExceeded
		}
	}
	// Record the task outcome at the turn boundary from runtime evidence, not
	// from the model's stop alone. A clean finish is not proof that a requested
	// change happened, that it happened to the named target, or that the
	// verification covered it; a failure is not erased by a plausible closing
	// sentence.
	evidenceReview := ""
	if p.task.ProjectionReview != nil {
		evidenceReview = p.task.ProjectionReview()
	}
	action, reason := turnTaskOutcomeWithReview(err, turns, intent, targetSnapshot, p.completionResolver, evidenceReview)
	if p.beforeTaskTransition != nil {
		p.beforeTaskTransition()
	}
	// Cancellation can land after Run returned but while post-run evidence is
	// still being read. The handle stays installed until the transition, so
	// ctx.Err is observable here. The atomic claim below is the linearization
	// point: the ctx check is only a fast path for a cancel whose callback has
	// not run yet.
	if ctx.Err() != nil {
		p.turnTerminal.CompareAndSwap(turnTerminalOpen, turnTerminalCancelled)
	}
	if action == taskActionReview && p.turnTerminal.Load() == turnTerminalCancelled {
		// Cancellation is an interruption, not a review verdict. Both are
		// non-terminal, but the human's stop is the more truthful transition.
		action, reason = taskActionPause, "interrupted"
	}
	if action == taskActionComplete {
		if !p.turnTerminal.CompareAndSwap(turnTerminalOpen, turnTerminalCompleted) {
			action, reason = taskActionPause, "interrupted"
		}
	}
	switch action {
	case taskActionComplete:
		if p.task.Complete != nil {
			projection := p.task.Complete()
			// The recap is derived here, at the only place that owns both
			// the completion decision and the run's evidence. It narrates
			// what the run accomplished from observed tool outcomes and the
			// assistant's own closing answer; the durable task layer never
			// stores it.
			objective := projection.Objective
			if strings.TrimSpace(objective) == "" {
				objective = intent.Objective
			}
			projection.Recap = recapNarrative(objective, turns)
			p.PublishTask(projection)
		}
	case taskActionReview:
		if p.task.NeedsReview != nil {
			p.PublishTask(p.task.NeedsReview(reason))
		} else if p.task.Pause != nil {
			p.PublishTask(p.task.Pause(reason))
		}
	case taskActionPause:
		if p.task.Pause != nil {
			p.PublishTask(p.task.Pause(reason))
		}
	case taskActionFail:
		if p.task.Fail != nil {
			p.PublishTask(p.task.Fail(reason))
		}
	}

	// The turn has made its terminal transition. Keeping the handle installed
	// until now is what lets a cancel during post-run evidence reach ctx.Err.
	// A session switch during the turn leaves the new session's history alone:
	// this turn belongs to the session it started in, and its transcript was
	// already persisted to that session above.
	p.mu.Lock()
	p.cancelTurn = nil
	var liveSessID int64
	if p.session != nil {
		liveSessID = p.session.ID
	}
	if liveSessID == turnSessID {
		// Rebase from the request actually sent: appending to the old history
		// would reintroduce anything a compaction just removed.
		p.history = rebase(request, turns)
	}
	p.mu.Unlock()

	// The budget display is fed from the provider's own accounting, not an
	// estimate: a wrong percentage here misleads more than it helps.
	for _, turn := range turns {
		if used := turn.Assistant.Usage.Total; used > 0 {
			p.publish(ContextUpdated{Used: used, Window: p.window})
		}
	}

	// Assistant text must reach the transcript: tool progress travels through
	// the hook above, but the model's own words only exist on the completed
	// turns. Without this the transcript shows the user's prompts with empty
	// space where the answers belong, which reads as "nothing appears".
	// Text already revealed live while the stream was open is skipped by
	// common prefix, so the tail publishes without duplicating: without live
	// deltas (replayed or non-streaming transports) nothing was revealed and
	// the whole turn publishes in bounded chunks as before.
	streamed := live.String()
	consumed := 0
	for _, turn := range turns {
		if text := turn.Assistant.Text(); text != "" {
			rest := text
			if consumed < len(streamed) {
				skip := commonPrefixLen(text, streamed[consumed:])
				rest = text[skip:]
				consumed += skip
			}
			for _, chunk := range splitAssistantChunks(rest) {
				p.publish(AssistantDelta{Text: chunk, Seq: seq})
			}
		}
	}

	// Every turn persists its assistant plus all of its results, in the same
	// order as the plain path: dropping non-final results would lose the
	// evidence the transcript replays, and persisting is the program's job
	// here (the loop's turn hook only publishes live metrics in UI mode —
	// transcript text still travels through AssistantDelta events).
	// They land in the turn's own session, never in a session created mid-turn.
	for _, turn := range turns {
		persistTo(agent.AssistantMessage{Message: turn.Assistant})
		for _, outcome := range turn.Results {
			persistTo(agent.ToolResultMessage{
				ToolCallID: outcome.CallID,
				ToolName:   outcome.ToolName,
				Content:    []ai.ContentBlock{ai.TextContent{Text: outcome.Result.Text}},
				IsError:    outcome.Result.IsError,
			})
		}
	}
	p.publish(AssistantDone{Seq: seq})
}

// resetSessionTelemetryLocked zeroes the live session's progressive-disclosure
// telemetry: no active clock and no token counts. The caller holds mu.
func (p *Program) resetSessionTelemetryLocked() {
	p.sessionStart = time.Time{}
	p.sessionIn = 0
	p.sessionOut = 0
}

// estimateTurnInput approximates the input tokens for one turn's request so
// the statusline can seed the upload count while the stream is still open.
//
// It counts what the loop will actually send — the agent messages via the
// loop's own estimator plus any per-turn system blocks — in estimate mode,
// matching the compaction trigger's own counting rather than inventing a
// second tokenizer. The provider's measured usage corrects it at each turn
// boundary, so a heuristic miss never survives the turn.
func estimateTurnInput(request []agent.Message, blocks []ai.SystemPromptBlock) int {
	estimate := agent.EstimateMessages(request)
	if len(blocks) > 0 {
		texts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		}
		estimate += tokens.CountBlocks(texts, tokens.ModeEstimate)
	}
	if estimate < 0 {
		return 0
	}
	return estimate
}

// accumulateSessionMetrics folds one request's measured usage into the live
// session and publishes the running totals. It runs on the turn goroutine, so
// every read and write of the counters goes through mu. A provider that does
// not report usage contributes nothing, and an all-zero session publishes no
// event: unknown stays absent rather than rendering a fabricated "0 in / 0
// out".
func (p *Program) accumulateSessionMetrics(turns []agent.Turn) {
	p.mu.Lock()
	if p.sessionStart.IsZero() {
		p.sessionStart = time.Now()
	}
	for _, turn := range turns {
		p.sessionIn += turn.Assistant.Usage.Input
		p.sessionOut += turn.Assistant.Usage.Output
	}
	in, out := p.sessionIn, p.sessionOut
	elapsed := time.Since(p.sessionStart)
	p.mu.Unlock()
	if in == 0 && out == 0 {
		return
	}
	p.publish(SessionMetricsUpdated{Input: in, Output: out, Elapsed: elapsed})
}

// persist appends one message to the transcript, if a session is attached.

// SetModelSwitcher installs the handlers behind /model.
//
// They are functions because the program does not own the model registry: the
// CLI does. Passing them in keeps tui independent of ai's provider table.
func (p *Program) SetModelSwitcher(set func(id string) error, current func() string) {
	p.setModel = set
	p.currentModel = current
}

// SetSupportFunc installs the ephemeral $sup handler.
//
// The handler runs on the SUP role's model and must not persist anything:
// the program routes its exchange through the overlay, never the normal
// transcript. A nil handler leaves $sup unavailable, in which case it is
// treated as an ordinary prompt.
func (p *Program) SetSupportFunc(support func(ctx context.Context, question string) (string, error)) {
	if support == nil {
		p.support = nil
		return
	}
	p.support = func(ctx context.Context, question string, onDelta func(string)) (string, error) {
		return support(ctx, question)
	}
}

// SetSupportStreamFunc installs the streaming $sup handler. Chunks stream
// into the overlay as they arrive; the returned string completes the turn
// for transports that never token-stream. Same persistence contract as
// SetSupportFunc.
func (p *Program) SetSupportStreamFunc(support func(ctx context.Context, question string, onDelta func(string)) (string, error)) {
	p.support = support
}

// SetSupportInfo installs the reporter behind the overlay's SUP identity
// line. It must return the effective SUP model reference and effort from
// the canonical configuration authority, so the display never goes stale.
func (p *Program) SetSupportInfo(info func() (model, effort string)) {
	p.supportInfo = info
}

// SetMentionLister installs the workspace path source behind the "@" trigger.
//
// The lister returns the candidate roster once, when the cursor first enters an
// "@" token; the inline palette filters it purely, so the lister must not
// pre-filter. Paths are slash-relative and a trailing slash marks a folder.
// A nil lister leaves "@" a literal character, which keeps the feature off for
// embedders and tests that do not own a workspace.
func (p *Program) SetMentionLister(lister func() []string) {
	p.listMentions = lister
}

// SetMentionReader installs the workspace file reader behind "@" context
// injection.
//
// It is the other half of the "@" contract: the lister offers paths to
// complete, and the reader turns an accepted path into the bytes the model
// receives. It must resolve within the workspace and return an error for a
// missing, unreadable, or out-of-root path, which becomes an explicit warning
// rather than silent empty context. A nil reader keeps "@" a pure UI
// affordance for embedders and tests that do not own a workspace.
func (p *Program) SetMentionReader(reader func(path string) (string, error)) {
	p.readMention = reader
}

// SetCompletionContext installs the workspace root and structural resolver
// used to capture pre-run evidence for a mutation request.
//
// It must be called before any turn runs. A nil resolver leaves symbol and
// package targets unverifiable; a path-only contract can still be checked.
// The resolver is the existing symbols tool's index, never a second index.
func (p *Program) SetCompletionContext(root string, resolver agent.StructuralResolver) {
	p.completionRoot = root
	p.completionResolver = resolver
}

// AttachSession wires the program to a persistent transcript.
//
// history is the replayed conversation from a previous run, so a resumed
// session continues rather than restarting. The program keeps its own copy: a
// caller mutating the slice afterwards must not change what the session sends.
// The replayed entries land in the transcript too, so a CLI resume shows
// what the model will continue instead of an empty welcome screen while the
// loop secretly holds full history. This mirrors the /sessions picker path,
// which applies HistoryReplaced on choosing.
func (p *Program) AttachSession(store *session.Store, sess *session.Session, history []agent.Message) {
	p.mu.Lock()
	p.store = store
	p.session = sess
	p.history = append([]agent.Message(nil), history...)
	// An attached session — new or resumed — starts with no standing proposal
	// decision: the choice belongs to the execution it released.
	p.proposal = nil
	p.mu.Unlock()
	if p.state.Pending != nil {
		p.state.Apply(ApprovalAnswered{})
	}
	if len(history) == 0 {
		if sess != nil {
			p.reportRecoveryInspection(sess.ID)
		}
		return
	}
	p.state.Apply(HistoryReplaced{Entries: EntriesFromMessages(history)})
	if sess != nil {
		p.state.Apply(Notice{Text: fmt.Sprintf("resumed session %d (%d messages)", sess.ID, len(history))})
		// A leftover interrupted recovery belongs to this session's workspace,
		// so it is surfaced when the session opens rather than waiting for an
		// undo that would be refused.
		p.reportRecoveryInspection(sess.ID)
	}
}

// persist appends one message to the transcript, if a session is attached.
//
// A storage failure is surfaced as a notice rather than aborting the turn: the
// conversation is still correct in memory, and losing the log is better than
// losing the answer.
func (p *Program) persist(msg agent.Message) {
	p.mu.Lock()
	store := p.store
	sess := p.session
	p.mu.Unlock()
	if store == nil || sess == nil {
		return
	}
	stored, err := session.FromAgentMessage(msg)
	if err != nil || stored.Role == "" {
		// A UI-only or unconvertible message has no stored form.
		return
	}
	if err := store.Append(sess.ID, stored); err != nil {
		p.publish(Notice{Text: "could not save turn: " + err.Error()})
	}
}

// PublishNotice records a transcript notice from outside the update
// goroutine. Like PublishWorkspace it is safe before Run and travels the
// event channel live, so background work (compaction reports, validation
// outcomes) never writes to the terminal directly.
func (p *Program) PublishNotice(text string) {
	p.publish(Notice{Text: text})
}

// PublishUpdateAvailable records a newer release in the header chrome from
// outside the update goroutine.
//
// It is deliberately not a Notice: an update hint is not part of the
// conversation, must not be persisted, and must not become model context.
func (p *Program) PublishUpdateAvailable(version string) {
	p.publish(UpdateFound{Version: version})
}

// rebase rebuilds history from the request that was sent plus the new turns.
func rebase(request []agent.Message, turns []agent.Turn) []agent.Message {
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

// commonPrefixLen reports the shared byte prefix of two strings, so text
// already revealed while streaming is published once, never twice. The
// result always lands on a rune boundary: backing off a split continuation
// byte keeps the tail valid UTF-8 for CJK/emoji turns.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	for i > 0 && i < len(a) && i < len(b) && !utf8.RuneStart(a[i]) {
		i--
	}
	return i
}

// publish sends a state event, dropping it if the UI has gone away.
func (p *Program) publish(event Event) {
	_ = p.publishOK(event)
}

// publishOK sends a state event and reports whether it landed. A full
// buffer must never block a tool: losing a preview line is acceptable, a
// stalled shell command is not.
func (p *Program) publishOK(event Event) bool {
	select {
	case p.events <- event:
		return true
	default:
		// A full buffer must never block a tool: losing a preview line is
		// acceptable, a stalled shell command is not.
		return false
	}
}

// ApprovalFunc returns the loop's approval hook.
//
// It blocks the calling tool until the user answers, which is what makes the
// prompt modal without the loop knowing a UI exists. Past shutdown there is
// no UI left to answer, so the tool is denied instead of parked forever: a
// denial the turn can complete beats a goroutine that never returns.
func (p *Program) ApprovalFunc() func(call ai.ToolCall, approval agent.Approval) (bool, string) {
	return func(call ai.ToolCall, approval agent.Approval) (bool, string) {
		// A tool approved "for entire session" never re-enters the dialog:
		// the remembered answer is the whole point of that choice.
		if p.sessionAllowedFor(call.Name) {
			return true, ""
		}
		answer := make(chan approvalAnswer, 1)
		request := approvalRequest{
			toolName: call.Name,
			reason:   approval.Reason,
			summary:  ToolSummaryForCall(call.Name, call.Arguments),
			preview:  approval.MutationPreview,
			// Anything but an explicit read is treated as state-changing:
			// an undeclared tier must never look harmless in the dialog.
			mutating: approval.Tier != agent.TierRead,
			answer:   answer,
		}
		select {
		case p.approvals <- request:
		case <-p.done:
			return false, "session ended"
		}
		select {
		case reply := <-answer:
			return reply.approved, reply.reason
		case <-p.done:
			return false, "session ended"
		}
	}
}

// isAffirmative reports whether a choice means "yes".
func isAffirmative(choice string) bool {
	switch strings.ToLower(choice) {
	case "yes", "y", "allow", "always":
		return true
	}
	return false
}

// shellApprovalTool is the name the "!" escape's confirmation is remembered
// under when the user allows it for the whole session. It matches the ToolName
// confirmEscape publishes so one choice covers the escape either way.
const shellApprovalTool = "shell"

// sessionApprovalChoice is the remembered affirmative offered by the approval
// dialog. It is matched exactly: a tool that offers a differently worded
// choice must not accidentally grant session-wide approval.
const sessionApprovalChoice = "Allow for entire session"

// isSessionApprovalChoice reports whether a choice grants session-wide
// approval. It is whitespace- and case-insensitive so the key handler and the
// dialog agree on the label they render.
func isSessionApprovalChoice(choice string) bool {
	return strings.EqualFold(strings.TrimSpace(choice), sessionApprovalChoice)
}
