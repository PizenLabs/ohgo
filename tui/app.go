package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/session"
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

// tickMsg advances the spinner.
type tickMsg time.Time

// spinnerInterval bounds animation frame rate. Fast enough to look alive,
// slow enough not to burn a core.
const spinnerInterval = 250 * time.Millisecond

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

	// history is the running conversation. The Program owns it because
	// Loop.Run is a turn, not a session — see AGENTS.md invariant I18.
	// It is touched from turn goroutines and the update goroutine alike,
	// so every access goes through mu.
	history []agent.Message

	// cancelTurn aborts the in-flight turn, if any. Set when a turn starts,
	// cleared when it ends, guarded by mu for the same reason as history.
	cancelTurn context.CancelFunc
	// mu guards history and cancelTurn across the update and turn goroutines.
	mu sync.Mutex

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
	// modelSeq is the latest catalog refresh generation. Older, slower
	// responses carry a trailing Seq and are dropped, never applied.
	modelSeq int
	// pickerRestore holds composer text to put back when a picker opened
	// over typing (slash-tab discovery) closes without choosing.
	pickerRestore string
	// pickerRestoreSet marks pickerRestore as deliberate: an empty restore
	// is still a restore, distinct from "no restore".
	pickerRestoreSet bool
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

// NewProgram returns a program driving the given loop.
func NewProgram(loop *agent.Loop, model string, window int) *Program {
	p := &Program{
		loop:      loop,
		events:    make(chan Event, 256),
		approvals: make(chan approvalRequest, 8),
		window:    window,
	}
	p.state = New()
	p.state.Status.Model = model
	p.state.Status.ContextWindow = window
	return p
}

// Init implements tea.Model.
func (p *Program) Init() tea.Cmd {
	return tea.Batch(p.waitForEvent(), p.waitForApproval(), tick())
}

// Update implements tea.Model.
func (p *Program) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if p.state.Status.Busy {
			p.state.Status.Spinner = AdvanceSpinner(p.state.Status.Spinner, len(spinnerFrames))
		}
		return p, tick()

	case turnFinishedMsg:
		return p.drainQueue()

	case modelsRefreshedMsg:
		return p.onModelsRefreshed(m)

	case tea.WindowSizeMsg:
		p.state.Apply(TerminalResized{Width: m.Width, Height: m.Height})
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
	if p.state.Picker == nil || p.state.Picker.Kind != PickerModels {
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
// While an approval prompt is open, keys answer it rather than compose a line:
// a tool is blocked on that answer, so the prompt has to be modal.
func (p *Program) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		}
		return p, nil
	}

	if p.state.Picker != nil {
		return p.handlePickerKey(msg)
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
				text := p.state.Input + string(msg.Runes)
				if msg.Type == tea.KeySpace {
					text = p.state.Input + " "
				}
				p.state.Apply(ComposerChanged{Text: text, Cursor: len([]rune(text))})
			}
			return p, nil
		}
	}

	switch msg.String() {
	case "ctrl+c":
		return p.cancelOrQuit()

	case "esc":
		// Escape backs out of a running turn the way ctrl+c does, but never
		// quits: a stray keypress must not end the session.
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
			p.state.Apply(QuitRequested{})
			return p, tea.Quit
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
		if line, ok := lineAbove(p.state.Input, p.state.Cursor); ok {
			p.state.Apply(ComposerChanged{Text: p.state.Input, Cursor: line})
		} else {
			p.state.Apply(HistoryMoved{Delta: 1})
		}
		return p, nil

	case "down", "ctrl+n":
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
		p.state.Apply(QuitRequested{})
		return tea.Quit

	case "help":
		p.state.Apply(Notice{Text: HelpText()})
		return nil

	case "clear":
		p.state.Apply(Cleared{})
		p.mu.Lock()
		p.history = nil
		p.mu.Unlock()
		return nil

	case "model":
		if len(command.Args) == 0 {
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
	if p.state.Picker != nil {
		p.closePicker()
		return p, nil
	}
	if p.state.Status.Busy {
		p.cancelTurnNow("cancelled")
		return p, nil
	}
	p.state.Apply(QuitRequested{})
	return p, tea.Quit
}

// cancelTurnNow aborts the in-flight turn, if any.
//
// A tool blocked in its approval prompt is denied first: without that the
// turn would stay parked on a question nobody will answer, and the
// cancellation would take effect never. Queued prompts are dropped with it:
// they were typed against a turn the user just abandoned, and silently
// starting one afterwards would undo the cancellation.
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
	if len(p.state.Queued) > 0 {
		p.state.Apply(QueueCleared{})
		note += " (queue cleared)"
	}
	p.publish(Notice{Text: note})
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
	// footer in step with what the loop will actually call.
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
	if p.state.Picker == nil || p.state.Picker.Kind != PickerModels {
		return nil
	}
	p.modelSeq++
	p.state.Picker.Refreshing = true
	return p.catalog.Refresh(context.Background(), p.modelSeq)
}

// openCommandPalette lists every slash command for discovery and keyboard
// navigation. Choosing one runs it; arguments are not collected here —
// commands needing them report usage, matching their direct invocation.
func (p *Program) openCommandPalette(filter string) {
	var items []PickerItem
	for _, spec := range Commands {
		if spec.Hidden {
			continue
		}
		items = append(items, PickerItem{
			Title:  "/" + spec.Name,
			Detail: spec.Description,
			Value:  spec.Name,
		})
	}
	p.state.Apply(PickerOpened{Kind: PickerCommands, Title: "commands", Items: items})
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
	p.mu.Unlock()
	// The store is untouched: every session lives in the one database it
	// already points at. Only the session pointer moves.
	p.session = sess
	p.state.Apply(HistoryReplaced{Entries: opened.Entries})
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

// runOn invokes the command against a program.
//
// It exists so a test can exercise the dispatch table without a Bubble Tea
// runtime; the returned command would be a no-op there anyway.
func (c Command) runOn(p *Program) { p.runCommand(c) }

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
func (p *Program) View() string { return Render(p.state) }

// Run starts the UI and blocks until it exits.
func (p *Program) Run() error {
	_, err := tea.NewProgram(p, tea.WithAltScreen()).Run()
	return err
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
	return func() tea.Msg {
		p.runTurn(text)
		return turnFinishedMsg{}
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
	p.publish(UserSubmitted{Text: text})

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
	p.loop.SetToolProgressHook(func(progress agent.ToolProgress) {
		switch progress.Stage() {
		case agent.ProgressStarted:
			p.publish(ToolStarted{
				ID:   progress.CallID,
				Name: progress.ToolName,
				At:   time.Now(),
			})
		case agent.ProgressUpdate:
			p.publish(ToolPreview{
				ID:      progress.CallID,
				Summary: oneLine(progress.Result.Text),
			})
		case agent.ProgressFinished:
			text, truncated := TruncateForDisplay(progress.Result.Text, toolOutputLimit)
			p.publish(ToolFinished{
				ID:        progress.CallID,
				Text:      text,
				IsError:   progress.Result.IsError,
				Truncated: truncated,
				At:        time.Now(),
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

	if err != nil {
		// A cancelled turn is an interruption, not a failure: reporting
		// it as an error would read as though something broke.
		if errors.Is(err, context.Canceled) {
			p.publish(Notice{Text: "turn cancelled"})
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
	for _, turn := range turns {
		if text := turn.Assistant.Text(); text != "" {
			p.publish(AssistantDelta{Text: text})
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
	p.publish(AssistantDone{})
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

// publish sends a state event, dropping it if the UI has gone away.
func (p *Program) publish(event Event) {
	select {
	case p.events <- event:
	default:
		// A full buffer must never block a tool: losing a preview line is
		// acceptable, a stalled shell command is not.
	}
}

// ApprovalFunc returns the loop's approval hook.
//
// It blocks the calling tool until the user answers, which is what makes the
// prompt modal without the loop knowing a UI exists.
func (p *Program) ApprovalFunc() func(call ai.ToolCall, approval agent.Approval) (bool, string) {
	return func(call ai.ToolCall, approval agent.Approval) (bool, string) {
		answer := make(chan approvalAnswer, 1)
		p.approvals <- approvalRequest{
			toolName: call.Name,
			reason:   approval.Reason,
			summary:  ToolSummaryForCall(call.Name, call.Arguments),
			answer:   answer,
		}
		reply := <-answer
		return reply.approved, reply.reason
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
