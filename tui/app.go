package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/session"
	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
)

// Approval choices offered to the user. The last is the safest so the default
// cursor lands on it.
var approvalChoices = []string{"Yes", "No"}

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

	// cancelTurn aborts the in-flight turn, if any. Set when a turn starts,
	// cleared when it ends, guarded by mu for the same reason as history.
	cancelTurn context.CancelFunc
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
	listModels   func() []string
	// listModelOptions is the enriched roster behind the /model picker:
	// native ids plus preformatted context/price/capability detail built by
	// the caller, which owns the catalog. Preferred over listModels when set.
	listModelOptions func() []ModelOption
	listJobs         func() []JobItem
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
	// providers lists credential status for /providers. Nil means unavailable.
	providers ProvidersFunc
	// probe checks the current endpoint for /providers. Nil skips probing.
	probe ProbeFunc
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
	// modelControl feeds and persists the /model control plane. Its zero
	// value means the plane is unavailable and /model falls back to the
	// picker; the program never owns durable configuration itself.
	modelControl ModelControlHooks
	// modelSeq is the latest catalog refresh generation. Older, slower
	// responses carry a trailing Seq and are dropped, never applied.
	modelSeq int
	// pickerRestore holds composer text to put back when a picker opened
	// over typing (slash-tab discovery) closes without choosing.
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

	// wizard is the onboarding dialog for uninitialized projects, set by
	// SetInitWizard. While non-nil it owns the keyboard and the middle of
	// the screen; header and footer keep rendering from state. It is
	// update-goroutine-owned, like pending and flow.
	wizard *InitWizardModel
	// wizardHooks commits or abandons the wizard's decisions. See
	// InitWizardHooks for the goroutine contract.
	wizardHooks InitWizardHooks
}

// WorkspaceHooks route workspace commands through their owner.
//
// Current reports the workspace in force (e.g. "build"); Switch moves the
// session to the named workspace at a turn boundary. Both are nil-safe: a
// program built without them reports the feature unavailable.
type WorkspaceHooks struct {
	// Current names the workspace in force.
	Current func() string
	// Switch moves the session to the named workspace.
	Switch func(name string) error
}

// SetWorkspaceSwitcher installs the owner behind /ask, /fuzz, /build.
func (p *Program) SetWorkspaceSwitcher(hooks WorkspaceHooks) {
	p.workspace = hooks
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

// SetDoctorFunc installs diagnostics behind /doctor.
func (p *Program) SetDoctorFunc(doctor DoctorFunc) {
	p.doctor = doctor
}

// SetProvidersFunc installs the credential table behind /providers.
func (p *Program) SetProvidersFunc(providers ProvidersFunc) {
	p.providers = providers
}

// SetProbeFunc installs the endpoint check behind /providers.
func (p *Program) SetProbeFunc(probe ProbeFunc) {
	p.probe = probe
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
	p := &Program{
		loop:      loop,
		events:    make(chan Event, 256),
		approvals: make(chan approvalRequest, 8),
		done:      make(chan struct{}),
		window:    window,
		termOut:   os.Stdout,
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
			ToolName: request.toolName,
			Reason:   request.reason,
			Summary:  request.summary,
			Choices:  approvalChoices,
		})
		return p, p.waitForApproval()

	case tickMsg:
		if p.state.Status.Busy || p.state.SupportBusy() {
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

	case tea.WindowSizeMsg:
		p.state.Apply(TerminalResized{Width: m.Width, Height: m.Height})
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
	if s.Picker != nil || s.Plane != nil || s.Secret != nil || s.Pending != nil || s.SupportPanel != nil {
		return false
	}
	return s.Input == ""
}

// handleMouse routes wheel ticks to the scrollable region in focus: the
// picker selection while one is open, the transcript otherwise. Press,
// release, and motion are deliberately ignored and never move state.
//
// Mouse reporting is off (see Run): the terminal owns selection and the
// application never receives mouse bytes in normal operation, so there is
// no protocol stream that can split across input reads and leak into the
// composer. The handler stays as a defensive contract: if reporting is ever
// re-enabled, wheel ticks scroll without touching prompt text, and every
// other mouse message remains a no-op.
func (p *Program) handleMouse(m tea.MouseMsg) (tea.Model, tea.Cmd) {
	if p.wizard != nil {
		// Onboarding owns the keyboard and the canvas: a wheel tick must
		// not scroll a transcript that is not on screen.
		return p, nil
	}
	if m.Button != tea.MouseButtonWheelUp && m.Button != tea.MouseButtonWheelDown {
		return p, nil
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
	relevant := (p.state.Picker != nil && p.state.Picker.Kind == PickerModels) || p.state.Plane != nil
	if !relevant {
		return p, nil
	}
	if m.seq != p.modelSeq {
		return p, nil
	}
	p.apply(ModelsRefreshed{Seq: m.seq, Snapshot: m.snapshot, Err: m.errMsg})
	return p, nil
}

// apply folds an event into the state and reacts to the ones the program owns.
func (p *Program) apply(event Event) {
	p.state.Apply(event)
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
	if p.state.Pending != nil {
		switch msg.String() {
		case "up", "k", "shift+tab", "left", "h":
			p.state.Apply(ApprovalMoved{Delta: -1})
			return p, nil
		case "down", "j", "tab", "right", "l":
			p.state.Apply(ApprovalMoved{Delta: 1})
			return p, nil
		case "enter":
			return p.answer(isAffirmative(p.state.Pending.Selected()), "denied by user")
		case "y", "Y":
			return p.answer(true, "")
		case "n", "N", "esc":
			return p.answer(false, "denied by user")
		case "ctrl+c":
			// The reflex abort key must not be swallowed by this switch: the
			// turn would stay parked on a question nobody can reach. Reuse the
			// turn-cancel path, which denies the blocked tool before cancelling,
			// so aborting can never approve the work it abandons.
			p.cancelTurnNow("cancelled")
			return p, nil
		}
		return p, nil
	}

	if p.state.Plane != nil {
		// The support overlay never stacks with the model control plane:
		// /model is unavailable until support closes.
		if p.state.SupportPanel != nil {
			return p, nil
		}
		return p.handlePlaneKey(msg)
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

	// Inline slash discovery owns its keys while visible: navigating,
	// accepting, or dismissing suggestions must not leak into history,
	// line movement, or submission. Tab is deliberately untouched: it
	// keeps opening the full command palette, matching its contract.
	if p.state.Secret == nil && p.state.Pending == nil &&
		!p.state.SlashHide && len(SlashSuggestions(p.state.Input)) > 0 {
		switch msg.String() {
		case "up", "ctrl+p":
			p.state.Apply(SlashMoved{Delta: -1})
			return p, nil
		case "down", "ctrl+n":
			p.state.Apply(SlashMoved{Delta: 1})
			return p, nil
		case "esc":
			// Dismiss keeps the draft: the text stays, only the list hides
			// until the next keystroke.
			p.state.SlashHide = true
			return p, nil
		case "enter":
			return p.acceptSlash()
		}
	}
	// A secret prompt captures the composer: typing masks, enter submits to
	// the setup flow, escape abandons it. Nothing typed here may become a
	// prompt — that path would persist the key into history.
	if p.state.Secret != nil {
		switch msg.String() {
		case "enter":
			text := p.state.Input
			p.state.Apply(SecretClosed{})
			p.submitSecret(text)
			return p, nil
		case "esc", "ctrl+c":
			p.state.Apply(SecretClosed{})
			p.flow = nil
			return p, nil
		case "backspace", "ctrl+h":
			if runes := []rune(p.state.Input); len(runes) > 0 {
				p.state.Apply(ComposerChanged{Text: string(runes[:len(runes)-1]), Cursor: len(runes) - 1})
			}
			return p, nil
		default:
			switch msg.Type {
			case tea.KeyRunes, tea.KeySpace:
				chunk := string(msg.Runes)
				if msg.Type == tea.KeySpace {
					chunk = " "
				}
				text := p.state.Input + chunk
				p.state.Apply(ComposerChanged{Text: text, Cursor: len([]rune(text))})
			}
			return p, nil
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

	case "ctrl+c":
		return p.cancelOrQuit()

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
		text := strings.TrimSpace(p.state.Input)
		if text == "" {
			// An empty line still advances queued work: the queue waits for a
			// turn boundary, and this is one.
			return p.drainQueue()
		}
		if command, ok := ParseCommand(text); ok {
			return p, p.runCommand(command)
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
		text, cursor := deleteBefore(p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "delete", "ctrl+d":
		// Ctrl+d on an empty composer quits, matching terminal convention;
		// anywhere else it deletes the character ahead.
		if msg.String() == "ctrl+d" && strings.TrimSpace(p.state.Input) == "" && !p.state.Status.Busy {
			return p, p.quitNow()
		}
		text, cursor := deleteAt(p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "left", "ctrl+b":
		p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: p.state.Cursor - 1})
		return p, nil

	case "right", "ctrl+f":
		p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: p.state.Cursor + 1})
		return p, nil

	case "home", "ctrl+a":
		p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: lineStart(p.state.Input, p.state.Cursor)})
		return p, nil

	case "end", "ctrl+e":
		// The transcript names "end" in its scrolled-up marker, so while
		// scrolled up it re-engages the live edge; otherwise (and always
		// for ctrl+e) it moves the composer cursor to end of line.
		if msg.String() == "end" && !p.state.Follow {
			p.state.Apply(ScrollToBottom{})
			return p, nil
		}
		p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: lineEnd(p.state.Input, p.state.Cursor)})
		return p, nil

	case "ctrl+u":
		text, cursor := killToStart(p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "ctrl+k":
		text, _ := killToEnd(p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: p.state.Cursor})
		return p, nil

	case "ctrl+w":
		text, cursor := killWordBack(p.state.Input, p.state.Cursor)
		p.state.Apply(ComposerChanged{Text: text, Cursor: cursor})
		return p, nil

	case "up", "ctrl+p":
		// On a fresh session there is nothing to recall, so ^p is the
		// settings/discovery hub the shortcut bar advertises. The moment a
		// prompt exists (or history does) the bar stops promising it and the
		// key returns to history recall, so the hint can never lie.
		if msg.String() == "ctrl+p" && !p.state.SessionActive && len(p.state.Submitted) == 0 {
			p.openSettingsPalette()
			return p, nil
		}
		// The terminal cannot say which arrows it synthesised for the wheel,
		// so the policy is state-based: the arrows belong to the transcript
		// exactly when the composer has nothing to move a cursor within and
		// nothing to recall. Any text, or an overlay owning the keyboard, and
		// the arrows go back to editing. History recall never moves: ctrl+p
		// and ctrl+n reach it whether or not the wheel does.
		if msg.String() == "up" && wheelScrolls(p.state) {
			p.state.Apply(ScrollMoved{Delta: mouseScrollLines})
			return p, nil
		}
		if line, ok := lineAbove(p.state.Input, p.state.Cursor); ok {
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
		if line, ok := lineBelow(p.state.Input, p.state.Cursor); ok {
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
		// Tab on a leading slash discovers commands instead of inserting a
		// literal tab: a slash line is either a command or a typo, and the
		// palette shows which without spending a turn. The typed text is
		// saved and restored if the palette closes without choosing.
		if strings.HasPrefix(strings.TrimSpace(p.state.Input), "/") {
			filter := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.state.Input), "/"))
			p.pickerRestore = p.state.Input
			p.pickerRestoreSet = true
			p.state.Apply(InputChanged{Text: ""})
			p.openCommandPalette(filter)
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
			if text == "?" && strings.TrimSpace(p.state.Input) == "" && p.state.Secret == nil {
				p.openCommandPalette("")
				return p, nil
			}
			// KeyRunes (typed or pasted, including bracketed pastes) is user
			// text: it inserts verbatim at the cursor. The application must
			// not second-guess prose that resembles protocol bytes.
			composed := insertAt(p.state.Input, p.state.Cursor, text)
			p.state.Apply(ComposerChanged{Text: composed, Cursor: p.state.Cursor + len([]rune(text))})
		}
		return p, nil
	}
}

// acceptSlash resolves Enter on a slash line: an exact known command (with
// or without arguments) dispatches exactly like submitting it; a partial
// line completes to the highlighted suggestion and stays in the composer
// so a second Enter runs what the user has now seen.
func (p *Program) acceptSlash() (tea.Model, tea.Cmd) {
	matches := SlashSuggestions(p.state.Input)
	if len(matches) == 0 {
		return p, nil
	}
	cur := p.state.SlashCursor
	if cur < 0 || cur >= len(matches) {
		cur = 0
	}
	typed := strings.TrimSpace(p.state.Input)
	if command, ok := ParseCommand(typed); ok {
		if _, known := LookupCommand(command.Name); known &&
			(typed == "/"+command.Name || len(command.Args) > 0) {
			return p, p.runCommand(command)
		}
	}
	completed := "/" + matches[cur].Name
	p.state.Apply(ComposerChanged{Text: completed, Cursor: len([]rune(completed))})
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

	case "ask", "fuzz", "build":
		p.switchWorkspace(command.Name)
		return nil

	case "help":
		p.state.Apply(Notice{Text: HelpText()})
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
		p.openSessionPicker()
		return nil

	case "jobs":
		p.openJobsView()
		return nil

	case "theme":
		p.applyThemeCommand(command.Args)
		return nil

	case "models":
		return p.openModelPicker()

	case "commands":
		p.openCommandPalette("")
		return nil

	case "providers":
		p.showProviders()
		return nil

	case "status":
		p.state.Apply(Notice{Text: p.statusReport()})
		return nil

	case "usage":
		p.showUsage()
		return nil

	case "doctor":
		p.runDoctor()
		return nil

	case "retry":
		return p.retryLast()

	case "compact":
		return p.compactNow()

	case "connect":
		p.openConnectPicker()
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
	case "backspace", "ctrl+h":
		p.state.Apply(PickerFilterBackspace{})
		return p, nil
	case "delete", "ctrl+d":
		p.state.Apply(PickerFilterDelete{})
		return p, nil
	case "ctrl+r":
		if cmd := p.refreshModelCatalog(); cmd != nil {
			return p, cmd
		}
		return p, nil
	default:
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
	case PickerCommands:
		p.state.Apply(InputChanged{Text: ""})
		return p, p.runCommand(Command{Name: item.Value, Raw: "/" + item.Value})
	case PickerJobs:
		p.state.Apply(Notice{Text: p.jobDetail(item.Value)})
		return p, nil
	case "connect":
		p.continueConnect(item)
		return p, nil
	case "connect-src":
		p.continueConnectSource(item)
		return p, nil
	case "connect-models":
		p.finishConnect(item)
		return p, nil
	default:
		return p, nil
	}
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

// cancelOrQuit cancels a running turn, or quits when idle.
//
// The first ctrl+c never quits a working session: it stops the work. Only an
// idle ctrl+c exits, so a reflex keypress cannot discard a session.
func (p *Program) cancelOrQuit() (tea.Model, tea.Cmd) {
	if p.state.Plane != nil {
		p.closePlane()
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
	if p.state.Status.Busy {
		p.cancelTurnNow("cancelled")
		return p, nil
	}
	return p, p.quitNow()
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
func (p *Program) switchWorkspace(name string) {
	if p.workspace.Switch == nil {
		p.state.Apply(Notice{Text: "workspace switching is unavailable"})
		return
	}
	// A workspace switch underneath an open support panel would move the
	// authority a running SUP turn executes under. Refused until the
	// panel closes (Esc); the turn boundary rule stays intact.
	if p.state.SupportPanel != nil {
		p.state.Apply(Notice{Text: "close support first (Esc) before switching workspace"})
		return
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: "wait for the turn to finish before switching workspace"})
		return
	}
	if p.workspace.Current != nil && strings.EqualFold(p.workspace.Current(), name) {
		p.state.Apply(Notice{Text: "already in " + strings.ToUpper(name)})
		return
	}
	if err := p.workspace.Switch(name); err != nil {
		p.state.Apply(Notice{Text: "workspace: " + err.Error()})
		return
	}
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
	if len(snapshot.Options) == 0 && snapshot.Origin != "loading" {
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
	loading := snapshot.Origin == "loading"
	p.state.Apply(PickerOpened{
		Kind:    PickerModels,
		Title:   "models",
		Items:   optionsToItems(snapshot.Options),
		Loading: loading,
		Origin:  snapshot.Origin,
		Stale:   snapshot.Stale,
	})
	// A stale, bundled-only, or still-loading snapshot refreshes in the
	// background; the picker is already open and usable meanwhile.
	if snapshot.Stale || loading || snapshot.Origin == "bundled" || snapshot.Origin == "" {
		return p.issueRefresh()
	}
	return nil
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
	loading := snapshot.Origin == "loading"
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
	if snapshot.Stale || loading || snapshot.Origin == "bundled" || snapshot.Origin == "" {
		return p.issueRefresh()
	}
	return nil
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

// issueRefresh starts a refresh unconditionally (the caller already decided
// one is due, e.g. a stale snapshot at open). It bumps the generation so a
// slower older response can never overwrite newer rows.
func (p *Program) issueRefresh() tea.Cmd {
	if p.catalog.Refresh == nil {
		return nil
	}
	if p.state.Picker == nil && p.state.Plane == nil {
		return nil
	}
	p.modelSeq++
	if p.state.Picker != nil {
		p.state.Picker.Refreshing = true
	}
	if p.state.Plane != nil {
		p.state.Plane.Refreshing = true
	}
	return p.catalog.Refresh(context.Background(), p.modelSeq)
}

// openCommandPalette lists every slash command for discovery and keyboard
// navigation. Choosing one runs it; arguments are not collected here —
// commands needing them report usage, matching their direct invocation.
func (p *Program) openCommandPalette(filter string) {
	p.openPalette("commands", filter)
}

// openSettingsPalette opens the same registry under the "settings" title,
// reached from ^p on a fresh session. It is the configuration/discovery hub —
// theme, model, connect, and providers all live here — so the shortcut bar's
// "^p settings" hint points at a surface that actually exists.
func (p *Program) openSettingsPalette() {
	p.openPalette("settings", "")
}

// openPalette builds and opens the command list. Choosing an item runs it;
// arguments are not collected here — commands needing them report usage,
// matching their direct invocation.
func (p *Program) openPalette(title, filter string) {
	var items []PickerItem
	for _, spec := range Commands {
		if spec.Hidden {
			continue
		}
		items = append(items, PickerItem{
			Title:  "/" + spec.Name,
			Detail: groupOf(spec) + " · " + spec.Description,
			Value:  spec.Name,
		})
	}
	p.state.Apply(PickerOpened{Kind: PickerCommands, Title: title, Items: items})
	if filter != "" {
		p.state.Apply(PickerFilterChanged{Text: filter})
	}
}

// openSessionPicker lists stored sessions for resume. The program owns
// neither the store nor the replay, so both arrive as functions.
func (p *Program) openSessionPicker() {
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
	var items []PickerItem
	for _, sess := range list {
		title := fmt.Sprintf("#%d %s", sess.ID, sess.Title)
		if sess.Title == "" {
			title = fmt.Sprintf("#%d %s", sess.ID, sess.Model)
		}
		items = append(items, PickerItem{
			Title:  title,
			Detail: sess.Updated + " · " + sess.Cwd,
			Value:  fmt.Sprintf("%d", sess.ID),
		})
	}
	p.state.Apply(PickerOpened{Kind: PickerSessions, Title: "sessions", Items: items})
}

// resumeSession switches the program to a stored session.
//
// History is replaced, never merged: the resumed conversation continues from
// where it left off, and mixing in the current one would send a transcript
// the model never saw.
func (p *Program) resumeSession(item PickerItem) tea.Cmd {
	var id int64
	if _, err := fmt.Sscanf(item.Value, "%d", &id); err != nil {
		p.state.Apply(Notice{Text: "sessions: bad session id " + item.Value})
		return nil
	}
	sess, opened, err := p.openSession(id)
	if err != nil {
		p.state.Apply(Notice{Text: "sessions: " + err.Error()})
		return nil
	}
	p.mu.Lock()
	p.history = append([]agent.Message(nil), opened.History...)
	// A resumed session is a new telemetry window: the prior session's token
	// counts and clock never carry over.
	p.resetSessionTelemetryLocked()
	p.mu.Unlock()
	// The store is untouched: every session lives in the one database it
	// already points at. Only the session pointer moves.
	p.session = sess
	p.state.Apply(HistoryReplaced{Entries: opened.Entries})
	if p.live != nil {
		p.live.forget()
	}
	if opened.Notice != "" {
		p.state.Apply(Notice{Text: opened.Notice})
	}
	return nil
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

// quitNow ends the session from any idle-state quit path (/quit, /exit,
// ctrl+d, idle ctrl+c).
//
// A tool blocked in its approval prompt is denied first: without that the
// tool waits on an answer channel no UI reads after shutdown, stranding its
// goroutine (and the turn holding it) until the process exits. Denial is the
// same conservative answer n/esc gives, so quitting can never approve work.
func (p *Program) quitNow() tea.Cmd {
	if p.pending != nil {
		p.answer(false, "denied by user")
	}
	p.state.Apply(QuitRequested{})
	return tea.Quit
}

// answer unblocks the tool that is waiting.
func (p *Program) answer(approved bool, reason string) (tea.Model, tea.Cmd) {
	if p.pending != nil {
		// A buffered reply channel means this never blocks the UI even if the
		// tool has already given up.
		p.pending.answer <- approvalAnswer{approved: approved, reason: reason}
		p.pending = nil
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
	return Render(p.state)
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
	p.doneOnce.Do(func() { close(p.done) })
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
			return func() tea.Msg {
				p.runTurn(text)
				return turnFinishedMsg{}
			}
		}
		p.openSupport(question)
		if strings.TrimSpace(question) == "" || p.state.SupportBusy() {
			return nil
		}
		return p.startSupportRun(strings.TrimSpace(question))
	}
	return func() tea.Msg {
		p.runTurn(text)
		return turnFinishedMsg{}
	}
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

// runTurn executes a single turn.
//
// It never touches the queue: shifting state off the update goroutine would
// race the renderer, so the shift waits for turnFinishedMsg.
func (p *Program) runTurn(text string) {
	p.mu.Lock()
	p.turnSeq++
	seq := p.turnSeq
	p.mu.Unlock()
	p.publish(UserSubmitted{Text: text, Seq: seq, At: time.Now()})

	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancelTurn = cancel
	history := append([]agent.Message(nil), p.history...)
	p.mu.Unlock()

	user := agent.UserMessage{Text: text}
	p.persist(user)

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

	turns, err := p.loop.Run(ctx, request)

	p.mu.Lock()
	p.cancelTurn = nil
	p.mu.Unlock()

	if p.usage != nil {
		p.usage.Add(turns)
	}
	// Progressive disclosure reads the live session's own accumulation, not
	// the lifetime /usage ledger: a cleared or resumed session starts clean.
	p.accumulateSessionMetrics(turns)

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

	// Rebase from the request actually sent: appending to the old history
	// would reintroduce anything a compaction just removed.
	p.mu.Lock()
	p.history = rebase(request, turns)
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
	// here (the loop's turn hook stays silent in UI mode — see SetTurnHook).
	for _, turn := range turns {
		p.persist(agent.AssistantMessage{Message: turn.Assistant})
		for _, outcome := range turn.Results {
			p.persist(agent.ToolResultMessage{
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
	p.store = store
	p.session = sess
	p.history = append([]agent.Message(nil), history...)
	if len(history) == 0 {
		return
	}
	p.state.Apply(HistoryReplaced{Entries: EntriesFromMessages(history)})
	if sess != nil {
		p.state.Apply(Notice{Text: fmt.Sprintf("resumed session %d (%d messages)", sess.ID, len(history))})
	}
}

// persist appends one message to the transcript, if a session is attached.
//
// A storage failure is surfaced as a notice rather than aborting the turn: the
// conversation is still correct in memory, and losing the log is better than
// losing the answer.
func (p *Program) persist(msg agent.Message) {
	if p.store == nil || p.session == nil {
		return
	}
	stored, err := session.FromAgentMessage(msg)
	if err != nil || stored.Role == "" {
		// A UI-only or unconvertible message has no stored form.
		return
	}
	if err := p.store.Append(p.session.ID, stored); err != nil {
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
		answer := make(chan approvalAnswer, 1)
		request := approvalRequest{
			toolName: call.Name,
			reason:   approval.Reason,
			summary:  ToolSummaryForCall(call.Name, call.Arguments),
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
