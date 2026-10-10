// Package tui renders the interactive agent session.
//
// The state machine in model.go is deliberately free of any terminal
// dependency: every transition is exercised by a test, and only the rendering
// and the Bubble Tea wiring touch a real terminal. That split exists because
// the original is 172k lines of differential rendering, and the part worth
// preserving here is the *behaviour* — streaming previews, approval memory,
// tool lifecycle — not the drawing code.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/tokens"
)

// ToolStatus is where a tool call is in its lifecycle.
type ToolStatus string

// Tool lifecycle states. The order matters: it is the order the view renders.
const (
	// ToolPending means the call has not started executing.
	ToolPending ToolStatus = "pending"
	// ToolRunning means the tool is executing.
	ToolRunning ToolStatus = "running"
	// ToolDone means the tool completed without error.
	ToolDone ToolStatus = "done"
	// ToolError means the tool completed with an error.
	ToolError ToolStatus = "error"
)

// Entry is one line in the transcript.
//
// Exactly one of the embedded kinds is meaningful, discriminated by Kind.
type Entry struct {
	// Kind tells which payload is set.
	Kind EntryKind

	// UserText is set when Kind is EntryUser.
	UserText string
	// AssistantText is set when Kind is EntryAssistant.
	AssistantText string
	// Tool is set when Kind is EntryTool.
	Tool *ToolEntry
	// Notice is set when Kind is EntryNotice.
	Notice string
}

// EntryKind discriminates an Entry.
type EntryKind string

// Entry kinds.
const (
	// EntryUser is something the user typed.
	EntryUser EntryKind = "user"
	// EntryAssistant is text the model produced.
	EntryAssistant EntryKind = "assistant"
	// EntryTool is a tool call and its outcome.
	EntryTool EntryKind = "tool"
	// EntryNotice is a system notice, e.g. a compaction.
	EntryNotice EntryKind = "notice"
)

// ToolEntry is the live state of one tool call.
type ToolEntry struct {
	// ID is the provider's tool call id.
	ID string
	// Name is the tool being called.
	Name string
	// Status is the lifecycle position.
	Status ToolStatus
	// Summary is a short human-readable description of what the call is doing.
	// It is filled from streamed arguments while the call runs.
	Summary string
	// Text is the outcome, set when the call finishes.
	Text string
	// StartedAt and EndedAt bound execution, for the duration display.
	StartedAt time.Time
	EndedAt   time.Time
	// Truncated marks that the outcome text was cut for display.
	Truncated bool
}

// Duration returns how long the call took, or zero if it has not started.
func (t *ToolEntry) Duration() time.Duration {
	if t.StartedAt.IsZero() {
		return 0
	}
	end := t.EndedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(t.StartedAt)
}

// PendingApproval is a question the user must answer before a tool runs.
type PendingApproval struct {
	// ToolName is the tool that wants to run.
	ToolName string
	// Reason is why the prompt appears, when the tool declared one.
	Reason string
	// Summary describes what this particular call will do.
	Summary string
	// Choices are the offered answers.
	Choices []string
	// Cursor is the highlighted choice index.
	Cursor int
}

// Selected reports the currently highlighted choice.
func (p *PendingApproval) Selected() string {
	if p.Cursor < 0 || p.Cursor >= len(p.Choices) {
		return ""
	}
	return p.Choices[p.Cursor]
}

// Move shifts the cursor, wrapping at both ends.
func (p *PendingApproval) Move(delta int) {
	if len(p.Choices) == 0 {
		p.Cursor = 0
		return
	}
	p.Cursor = (p.Cursor + delta) % len(p.Choices)
	if p.Cursor < 0 {
		p.Cursor += len(p.Choices)
	}
}

// Status is the persistent footer.
type Status struct {
	// Model is the model in use.
	Model string
	// ContextUsed and ContextWindow size the budget display.
	ContextUsed   int
	ContextWindow int
	// Busy marks that a turn is in flight.
	Busy bool
	// Spinner is the animation frame index; the caller owns advancing it.
	Spinner int
	// Cwd is the working directory shown in the status line. Empty hides it.
	Cwd string
	// Session names the session, e.g. "#12". Empty hides it.
	Session string
	// UsageText is preformatted token/cost detail, e.g. "12.4k tok · $0.02".
	// Empty hides it. The producer labels estimates; the view never invents
	// numbers, and unknown values stay absent rather than rendering as zero.
	UsageText string
}

// Percent renders the context budget, or -1 when there is no window.
//
// A model with no published window must not render "0%", which would look like
// an empty context rather than an unknown one.
func (s Status) Percent() int {
	if s.ContextWindow <= 0 {
		return -1
	}
	return tokens.Percent(s.ContextUsed, s.ContextWindow)
}

// State is everything the view renders. It owns no terminal handles.
type State struct {
	// Entries are the transcript, oldest first.
	Entries []Entry
	// Status is the footer.
	Status Status
	// Input is the text being composed.
	Input string
	// Cursor is the rune offset in Input where typing lands.
	Cursor int
	// Submitted remembers past prompts for history recall, oldest first.
	Submitted []string
	// HistCursor selects a Submitted entry, or -1 for the live input.
	HistCursor int
	// HistDraft stashes the live input while history is being browsed.
	HistDraft string
	// Queued holds prompts typed while a turn runs, in order. They submit
	// automatically when the turn completes.
	Queued []string
	// Scroll is how many lines above the bottom edge the viewport shows.
	// Zero follows the live edge.
	Scroll int
	// Follow reports the viewport tracks new output. Scrolling up clears it;
	// scrolling back to the bottom restores it.
	Follow bool
	// Width and Height are the terminal size in cells. Zero means unknown,
	// in which case the view renders everything without slicing.
	Width  int
	Height int
	// Theme names the palette: "dark" (default) or "light".
	Theme string
	// Picker is the open selection overlay, or nil.
	Picker *PickerState
	// Secret is an open credential prompt, or nil. While set, the composer
	// collects a masked secret for its handler instead of a prompt.
	Secret *SecretState
	// ConnOK and ConnDetail report the last connection probe. Unknown until
	// the first probe: the UI never blocks startup on network.
	ConnOK     bool
	ConnDetail string
	// Pending is a non-nil approval prompt.
	Pending *PendingApproval
	// Quit is set once the session has been asked to exit.
	Quit bool
	// closedAssistant marks that the trailing assistant entry has ended, so
	// the next turn opens a new one.
	closedAssistant bool
}

// New returns an empty state.
func New() State {
	return State{HistCursor: -1, Follow: true, Theme: ThemeDark}
}

// Apply folds one event into the state.
//
// This is the only place a transition happens, which is what makes the
// behaviour testable without a terminal.
func (s *State) Apply(event Event) {
	switch e := event.(type) {
	case UserSubmitted:
		s.Entries = append(s.Entries, Entry{Kind: EntryUser, UserText: e.Text})
		s.Input = ""
		s.Cursor = 0
		s.Status.Busy = true
		// The prompt joins recallable history. Consecutive duplicates are
		// collapsed so holding enter does not flood the history.
		if len(s.Submitted) == 0 || s.Submitted[len(s.Submitted)-1] != e.Text {
			s.Submitted = append(s.Submitted, e.Text)
		}
		s.HistCursor = -1
		s.HistDraft = ""
		s.growScroll(countLines(e.Text))

	case AssistantDelta:
		s.appendAssistant(e.Text)
		s.growScroll(strings.Count(e.Text, "\n"))

	case AssistantDone:
		// Close the trailing assistant entry: without this the next turn would
		// append to it and the transcript would read as one continuous answer.
		if open := s.openAssistant(); open >= 0 {
			s.closedAssistant = true
		}
		s.Status.Busy = false

	case ToolStarted:
		s.Entries = append(s.Entries, Entry{
			Kind: EntryTool,
			Tool: &ToolEntry{
				ID:        e.ID,
				Name:      e.Name,
				Status:    ToolRunning,
				StartedAt: e.At,
			},
		})
		s.growScroll(1)

	case ToolPreview:
		// A preview updates the summary of a *running* call only. Once a call
		// has finished, a late preview would resurrect stale progress on top
		// of the real outcome.
		if tool := s.tool(e.ID); tool != nil && tool.Status == ToolRunning {
			tool.Summary = e.Summary
		}

	case ToolFinished:
		tool := s.tool(e.ID)
		if tool == nil {
			return
		}
		tool.EndedAt = e.At
		tool.Summary = ""
		tool.Text = e.Text
		tool.Truncated = e.Truncated
		if e.IsError {
			tool.Status = ToolError
		} else {
			tool.Status = ToolDone
		}
		s.growScroll(countLines(e.Text) + 1)

	case Notice:
		s.Entries = append(s.Entries, Entry{Kind: EntryNotice, Notice: e.Text})
		s.growScroll(countLines(e.Text))

	case HistoryReplaced:
		// A resumed session continues rather than restarts: the replayed
		// transcript replaces the view outright.
		s.Entries = append([]Entry(nil), e.Entries...)
		s.closedAssistant = true

	case ContextUpdated:
		s.Status.ContextUsed = e.Used
		s.Status.ContextWindow = e.Window

	case Compacted:
		// Compaction is announced as a notice, never silently: the user must
		// be able to tell that history was summarised rather than lost.
		s.Entries = append(s.Entries, Entry{
			Kind:   EntryNotice,
			Notice: fmt.Sprintf("context compacted: %d messages summarised, %d retained", e.Messages, e.Retained),
		})
		s.Status.ContextUsed = e.AfterTokens
		s.Status.ContextWindow = e.Window
		s.growScroll(1)

	case ApprovalAsked:
		s.Pending = &PendingApproval{
			ToolName: e.ToolName,
			Reason:   e.Reason,
			Summary:  e.Summary,
			Choices:  e.Choices,
			Cursor:   defaultApprovalSelection(e.Choices),
		}

	case ApprovalMoved:
		if s.Pending != nil {
			s.Pending.Move(e.Delta)
		}

	case ApprovalAnswered:
		s.Pending = nil

	case InputChanged:
		s.Input = e.Text
		s.Cursor = len([]rune(e.Text))

	case ComposerChanged:
		s.Input = e.Text
		s.Cursor = clampCursor(e.Text, e.Cursor)
		// Typing over a recalled entry leaves history: the draft stays saved
		// until the prompt submits or history is re-entered.
		if s.HistCursor >= 0 {
			s.HistCursor = -1
		}

	case HistoryMoved:
		s.moveHistory(e.Delta)

	case MessageQueued:
		s.Queued = append(s.Queued, e.Text)
		s.growScroll(1)

	case QueueShifted:
		if len(s.Queued) > 0 {
			s.Queued = append([]string(nil), s.Queued[1:]...)
		}

	case QueueCleared:
		s.Queued = nil

	case ScrollMoved:
		s.Scroll += e.Delta
		if s.Scroll <= 0 {
			s.Scroll = 0
			s.Follow = true
		} else {
			s.Follow = false
		}

	case ScrollToBottom:
		s.Scroll = 0
		s.Follow = true

	case TerminalResized:
		s.Width = e.Width
		s.Height = e.Height
		if s.Picker != nil {
			s.Picker.SetVisibleRows(pickerRowsFor(*s))
		}

	case ThemeChanged:
		s.Theme = NormalizeTheme(e.Name)

	case PickerOpened:
		s.Picker = NewPicker(e.Kind, e.Title, e.Items)
		s.Picker.Loading = e.Loading
		s.Picker.Origin = e.Origin
		s.Picker.Refreshing = e.Stale
		s.Picker.SetVisibleRows(pickerRowsFor(*s))

	case PickerFilterChanged:
		if s.Picker != nil {
			s.Picker.SetFilter(e.Text)
		}

	case PickerTyped:
		if s.Picker != nil {
			s.Picker.InsertFilterText(e.Text)
		}

	case PickerFilterBackspace:
		if s.Picker != nil {
			s.Picker.DeleteFilterBefore()
		}

	case PickerFilterDelete:
		if s.Picker != nil {
			s.Picker.DeleteFilterAt()
		}

	case PickerFilterCursorMoved:
		if s.Picker != nil {
			s.Picker.MoveFilterCursor(e.Delta)
		}

	case PickerFilterCleared:
		if s.Picker != nil {
			s.Picker.ClearFilter()
		}

	case PickerMoved:
		if s.Picker != nil {
			s.Picker.Move(e.Delta)
		}

	case PickerPaged:
		if s.Picker != nil {
			s.Picker.Page(e.Delta)
		}

	case PickerTop:
		if s.Picker != nil {
			s.Picker.Top()
		}

	case PickerBottom:
		if s.Picker != nil {
			s.Picker.Bottom()
		}

	case PickerRowsChanged:
		if s.Picker != nil {
			s.Picker.SetVisibleRows(e.Rows)
		}

	case ModelsRefreshed:
		if s.Picker != nil && s.Picker.Kind == PickerModels {
			s.Picker.Loading = false
			s.Picker.Refreshing = false
			// Fresh rows apply even alongside a partial failure: four
			// live providers beat one dead daemon, and the error names
			// which source stayed cached. Only an empty snapshot keeps
			// the current rows untouched.
			if len(e.Snapshot.Options) > 0 {
				s.Picker.SetItems(optionsToItems(e.Snapshot.Options))
				if e.Snapshot.Origin != "" {
					s.Picker.Origin = e.Snapshot.Origin
				}
			}
			s.Picker.ErrMsg = e.Err
		}

	case PickerClosed:
		s.Picker = nil

	case SecretOpened:
		s.Secret = &SecretState{Label: e.Label}
		s.Input = ""
		s.Cursor = 0

	case SecretClosed:
		s.Secret = nil
		s.Input = ""
		s.Cursor = 0

	case ProbeFinished:
		s.ConnOK = e.OK
		s.ConnDetail = e.Detail

	case WorkspaceUpdated:
		if e.HasCwd {
			s.Status.Cwd = e.Cwd
		}
		if e.HasSession {
			s.Status.Session = e.Session
		}
		if e.HasUsage {
			s.Status.UsageText = e.UsageText
		}

	case QuitRequested:
		s.Quit = true

	case Cleared:
		// /clear drops the transcript so the next turn starts from nothing.
		// The status line is preserved: the model and budget still apply.
		// Queued prompts go too — they were typed against the context that
		// was just dropped. Recallable history stays: it is memory, not
		// context.
		s.Entries = nil
		s.Pending = nil
		s.Queued = nil
		s.closedAssistant = false
	}
}

// growScroll keeps a scrolled-up viewport pinned while new output lands.
//
// In follow mode the view tracks the live edge, so there is nothing to do.
// Scrolled up, the offset from the bottom must grow by the lines just added,
// or the viewport would drift downward with every event.
func (s *State) growScroll(lines int) {
	if !s.Follow {
		s.Scroll += lines
	}
}

// countLines counts the logical lines of a text block.
func countLines(text string) int {
	if text == "" {
		return 1
	}
	return strings.Count(text, "\n") + 1
}

// clampCursor bounds a rune offset to its text.
func clampCursor(text string, pos int) int {
	n := len([]rune(text))
	if pos < 0 {
		return 0
	}
	if pos > n {
		return n
	}
	return pos
}

// moveHistory browses recallable prompts. Positive delta goes older.
//
// The live input is stashed as a draft on entry and restored when browsing
// back past the newest entry, so recall never eats what was being typed.
func (s *State) moveHistory(delta int) {
	if len(s.Submitted) == 0 {
		return
	}
	if s.HistCursor < 0 {
		// Entering from live input stashes the draft; newer from live is a
		// no-op because there is nothing newer than what is being typed.
		if delta <= 0 {
			return
		}
		s.HistDraft = s.Input
		s.HistCursor = len(s.Submitted) - 1
	} else if delta > 0 {
		// Older clamps at the first prompt: overshooting must not wrap
		// around to live input, which would read as history vanishing.
		s.HistCursor--
		if s.HistCursor < 0 {
			s.HistCursor = 0
		}
	} else {
		// Newer past the newest entry returns to live input and restores
		// the stashed draft, so recall never eats what was being typed.
		s.HistCursor++
		if s.HistCursor >= len(s.Submitted) {
			s.HistCursor = -1
			s.Input = s.HistDraft
			s.Cursor = len([]rune(s.Input))
			return
		}
	}
	s.Input = s.Submitted[s.HistCursor]
	s.Cursor = len([]rune(s.Input))
}

// defaultApprovalSelection picks the safest choice, which is the last one.
//
// "Deny" is listed last so the cursor lands on it: a tool that forgets to say
// what its choices mean still defaults to the conservative answer.
func defaultApprovalSelection(choices []string) int {
	if len(choices) == 0 {
		return 0
	}
	return len(choices) - 1
}

// openAssistant returns the index of the trailing assistant entry that can
// still be extended, or -1 when a new one must be started.
func (s *State) openAssistant() int {
	if len(s.Entries) == 0 || s.closedAssistant {
		return -1
	}
	last := len(s.Entries) - 1
	if s.Entries[last].Kind != EntryAssistant {
		return -1
	}
	return last
}

// appendAssistant extends the open assistant entry, or starts one.
func (s *State) appendAssistant(text string) {
	if text == "" {
		return
	}
	if index := s.openAssistant(); index >= 0 {
		s.Entries[index].AssistantText += text
		return
	}
	s.Entries = append(s.Entries, Entry{Kind: EntryAssistant, AssistantText: text})
	s.closedAssistant = false
}

// tool finds a tool entry by call id.
func (s *State) tool(id string) *ToolEntry {
	for i := range s.Entries {
		entry := &s.Entries[i]
		if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.ID == id {
			return entry.Tool
		}
	}
	return nil
}

// AssistantText returns the concatenated assistant text, for tests and for the
// non-interactive renderer.
func (s *State) AssistantText() string {
	var sb strings.Builder
	for _, entry := range s.Entries {
		if entry.Kind == EntryAssistant {
			sb.WriteString(entry.AssistantText)
		}
	}
	return sb.String()
}

// Events are the only way State changes. They are plain values so a test can
// build a script of them.

// Event is one state transition.
type Event interface{ eventKind() string }

// UserSubmitted is a line the user entered.
type UserSubmitted struct{ Text string }

func (UserSubmitted) eventKind() string { return "user_submitted" }

// AssistantDelta is streamed model text.
type AssistantDelta struct{ Text string }

func (AssistantDelta) eventKind() string { return "assistant_delta" }

// AssistantDone marks the end of a turn.
type AssistantDone struct{}

func (AssistantDone) eventKind() string { return "assistant_done" }

// ToolStarted announces a call beginning.
type ToolStarted struct {
	ID   string
	Name string
	At   time.Time
}

func (ToolStarted) eventKind() string { return "tool_started" }

// ToolPreview updates a running call's summary.
type ToolPreview struct {
	ID      string
	Summary string
}

func (ToolPreview) eventKind() string { return "tool_preview" }

// ToolFinished records a call's outcome.
type ToolFinished struct {
	ID        string
	Text      string
	IsError   bool
	Truncated bool
	At        time.Time
}

func (ToolFinished) eventKind() string { return "tool_finished" }

// Notice adds a system line.
type Notice struct{ Text string }

func (Notice) eventKind() string { return "notice" }

// ContextUpdated sizes the budget display.
type ContextUpdated struct {
	Used   int
	Window int
}

func (ContextUpdated) eventKind() string { return "context_updated" }

// Compacted announces that history was summarised.
type Compacted struct {
	Messages    int
	Retained    int
	AfterTokens int
	Window      int
}

func (Compacted) eventKind() string { return "compacted" }

// ApprovalAsked opens a prompt.
type ApprovalAsked struct {
	ToolName string
	Reason   string
	Summary  string
	Choices  []string
}

func (ApprovalAsked) eventKind() string { return "approval_asked" }

// ApprovalMoved shifts the highlighted choice.
type ApprovalMoved struct{ Delta int }

func (ApprovalMoved) eventKind() string { return "approval_moved" }

// ApprovalAnswered closes the prompt.
type ApprovalAnswered struct{}

func (ApprovalAnswered) eventKind() string { return "approval_answered" }

// InputChanged sets the composed line.
type InputChanged struct{ Text string }

func (InputChanged) eventKind() string { return "input_changed" }

// QuitRequested ends the session.
type QuitRequested struct{}

func (QuitRequested) eventKind() string { return "quit_requested" }

// ToolSummaryForCall renders a one-line description of a call from its
// arguments.
//
// It exists so a long-running tool shows progress rather than a name, and it
// reads only the common keys: a tool that stores its target elsewhere still
// gets a name, which is better than a blank line.
func ToolSummaryForCall(name string, args map[string]any) string {
	for _, key := range []string{"path", "command", "pattern", "url", "file"} {
		if value, ok := args[key]; ok {
			if text, ok := value.(string); ok && text != "" {
				return fmt.Sprintf("%s %s", name, oneLine(text))
			}
		}
	}
	return name
}

// oneLine collapses whitespace and truncates a value for a summary line.
func oneLine(text string) string {
	const max = 60
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) > max {
		return collapsed[:max] + "…"
	}
	return collapsed
}

// TruncateForDisplay cuts a long outcome so one tool cannot flood the view.
func TruncateForDisplay(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	return text[:limit] + "\n… [truncated]", true
}

// ModelID renders a model as "provider/id", matching the CLI's own format.
func ModelID(model ai.Model) string {
	return model.Provider + "/" + model.ID
}

// Command is a parsed slash command.
type Command struct {
	// Name is the command name without the leading slash.
	Name string
	// Args are the whitespace-separated arguments.
	Args []string
	// Raw is the line as typed, for echoing and error messages.
	Raw string
}

// ParseCommand parses a line that starts with "/".
//
// It reports ok=false for anything else, so a caller can treat a line as a
// command or a prompt without inspecting it itself. Arguments are split on
// whitespace only: quoting would need escaping rules that a typed command line
// rarely needs.
func ParseCommand(line string) (Command, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") {
		return Command{}, false
	}

	body := strings.TrimPrefix(trimmed, "/")
	fields := strings.Fields(body)
	if len(fields) == 0 {
		// A bare "/" is more likely a path or a typo than a command.
		return Command{}, false
	}
	return Command{
		Name: strings.ToLower(fields[0]),
		Args: fields[1:],
		Raw:  trimmed,
	}, true
}

// IsCommand reports whether a line is a slash command.
func IsCommand(line string) bool {
	_, ok := ParseCommand(line)
	return ok
}

// CommandSpec describes one command for the help listing.
type CommandSpec struct {
	// Name is the command word.
	Name string
	// Usage shows the arguments, e.g. "model <provider/id>".
	Usage string
	// Description is one line of help.
	Description string
	// Hidden keeps a command out of the listing, for aliases.
	Hidden bool
}

// Commands is the registry.
//
// It lives beside the state machine rather than in the UI so the help text and
// the dispatch table cannot drift apart.
var Commands = []CommandSpec{
	{Name: "help", Usage: "help", Description: "list the available commands"},
	{Name: "commands", Usage: "commands", Description: "search every command"},
	{Name: "clear", Usage: "clear", Description: "clear the transcript and start a fresh context"},
	{Name: "model", Usage: "model [provider/id]", Description: "browse or switch the model"},
	{Name: "models", Usage: "models", Description: "browse every known model", Hidden: true},
	{Name: "sessions", Usage: "sessions", Description: "browse and resume saved sessions"},
	{Name: "resume", Usage: "resume", Description: "browse and resume saved sessions", Hidden: true},
	{Name: "jobs", Usage: "jobs", Description: "inspect background jobs"},
	{Name: "theme", Usage: "theme [dark|light]", Description: "show or switch the palette"},
	{Name: "connect", Usage: "connect", Description: "set up a provider connection"},
	{Name: "disconnect", Usage: "disconnect [provider]", Description: "remove a saved credential"},
	{Name: "providers", Usage: "providers", Description: "list providers and credential status"},
	{Name: "status", Usage: "status", Description: "show provider, model, context, and session"},
	{Name: "usage", Usage: "usage", Description: "show token usage and cost"},
	{Name: "doctor", Usage: "doctor", Description: "diagnose setup, credentials, and tools"},
	{Name: "retry", Usage: "retry", Description: "re-send the last prompt"},
	{Name: "compact", Usage: "compact", Description: "summarise context now"},
	{Name: "quit", Usage: "quit", Description: "exit the session", Hidden: true},
	{Name: "exit", Usage: "exit", Description: "exit the session", Hidden: true},
}

// SuggestCommand names the closest known command to a typo, for the "did you
// mean" hint. Distance 2 covers transpositions and one wrong letter without
// suggesting something unrelated for a genuinely unknown word.
func SuggestCommand(name string) (string, bool) {
	best, bestDist := "", 3
	for _, spec := range Commands {
		if d := editDistance(name, spec.Name); d < bestDist {
			best, bestDist = spec.Name, d
		}
	}
	return best, best != ""
}

// editDistance is the Levenshtein distance over command names. Local because
// the vocabulary is a dozen short words: importing a diff engine for this
// would be the tail wagging the dog.
func editDistance(a, b string) int {
	la, lb := len(a), len(b)
	prev := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur := make([]int, lb+1)
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if a > b {
		a = b
	}
	if a > c {
		a = c
	}
	return a
}

// LookupCommand finds a command by name, case-insensitively.
func LookupCommand(name string) (CommandSpec, bool) {
	for _, spec := range Commands {
		if strings.EqualFold(spec.Name, name) {
			return spec, true
		}
	}
	return CommandSpec{}, false
}

// HelpText renders the command listing for /help.
func HelpText() string {
	var sb strings.Builder
	sb.WriteString("Commands:\n")
	for _, spec := range Commands {
		if spec.Hidden {
			continue
		}
		fmt.Fprintf(&sb, "  /%-22s %s\n", spec.Usage, spec.Description)
	}
	sb.WriteString("\nAnything else is sent to the model.")
	return strings.TrimRight(sb.String(), "\n")
}

// Cleared is published when /clear wipes the transcript.
type Cleared struct{}

func (Cleared) eventKind() string { return "cleared" }

// ComposerChanged sets the composed text and cursor together.
//
// InputChanged stays for callers that replace the whole line (clearing after
// a command): it pins the cursor to the end, which is what "replace" means.
type ComposerChanged struct {
	// Text is the new composed text.
	Text string
	// Cursor is the rune offset where typing lands.
	Cursor int
}

func (ComposerChanged) eventKind() string { return "composer_changed" }

// HistoryMoved browses recallable prompts. Positive delta goes older.
type HistoryMoved struct{ Delta int }

func (HistoryMoved) eventKind() string { return "history_moved" }

// MessageQueued holds a prompt typed while a turn runs.
type MessageQueued struct{ Text string }

func (MessageQueued) eventKind() string { return "message_queued" }

// QueueShifted drops the head of the queue once its turn starts.
type QueueShifted struct{}

func (QueueShifted) eventKind() string { return "queue_shifted" }

// QueueCleared drops every queued prompt.
type QueueCleared struct{}

func (QueueCleared) eventKind() string { return "queue_cleared" }

// maxQueued bounds prompts typed while a turn runs. Typing past it is
// reported rather than silently dropped, so the user knows where their words
// went.
const maxQueued = 5

// ScrollMoved shifts the viewport by lines. Positive goes up (older).
type ScrollMoved struct{ Delta int }

func (ScrollMoved) eventKind() string { return "scroll_moved" }

// ScrollToBottom re-engages the live edge.
type ScrollToBottom struct{}

func (ScrollToBottom) eventKind() string { return "scroll_to_bottom" }

// TerminalResized records the terminal size for viewport slicing.
type TerminalResized struct {
	Width  int
	Height int
}

func (TerminalResized) eventKind() string { return "terminal_resized" }

// Theme names.
const (
	// ThemeDark is the default palette.
	ThemeDark = "dark"
	// ThemeLight is the bright palette.
	ThemeLight = "light"
)

// NormalizeTheme maps a name to a known theme. Anything unrecognised becomes
// dark: a typo degrades to the default rather than an error.
func NormalizeTheme(name string) string {
	if strings.EqualFold(strings.TrimSpace(name), ThemeLight) {
		return ThemeLight
	}
	return ThemeDark
}

// ThemeChanged switches the palette.
type ThemeChanged struct{ Name string }

func (ThemeChanged) eventKind() string { return "theme_changed" }

// PickerOpened shows a selection overlay.
type PickerOpened struct {
	// Kind selects what choosing means: "sessions", "models" or "jobs".
	Kind string
	// Title heads the overlay.
	Title string
	// Items are the choices.
	Items []PickerItem
	// Loading reports the first catalog snapshot has not arrived yet.
	Loading bool
	// Origin names the snapshot: "live", "cached", "bundled", or "".
	Origin string
	// Stale reports cached rows shown while a refresh runs.
	Stale bool
}

func (PickerOpened) eventKind() string { return "picker_opened" }

// PickerFilterChanged re-filters the visible items.
type PickerFilterChanged struct{ Text string }

func (PickerFilterChanged) eventKind() string { return "picker_filter_changed" }

// PickerMoved shifts the highlight. Positive goes down.
type PickerMoved struct{ Delta int }

func (PickerMoved) eventKind() string { return "picker_moved" }

// PickerPaged shifts the highlight by approximately one viewport.
// Positive goes down; paging parks at the ends rather than wrapping.
type PickerPaged struct{ Delta int }

func (PickerPaged) eventKind() string { return "picker_paged" }

// PickerTop parks the highlight on the first result.
type PickerTop struct{}

func (PickerTop) eventKind() string { return "picker_top" }

// PickerBottom parks the highlight on the last result.
type PickerBottom struct{}

func (PickerBottom) eventKind() string { return "picker_bottom" }

// PickerTyped inserts text into the query at the query cursor.
type PickerTyped struct{ Text string }

func (PickerTyped) eventKind() string { return "picker_typed" }

// PickerFilterBackspace deletes the rune before the query cursor.
type PickerFilterBackspace struct{}

func (PickerFilterBackspace) eventKind() string { return "picker_filter_backspace" }

// PickerFilterDelete deletes the rune under the query cursor.
type PickerFilterDelete struct{}

func (PickerFilterDelete) eventKind() string { return "picker_filter_delete" }

// PickerFilterCursorMoved shifts the query cursor. Positive goes right.
type PickerFilterCursorMoved struct{ Delta int }

func (PickerFilterCursorMoved) eventKind() string { return "picker_filter_cursor_moved" }

// PickerFilterCleared empties the query.
type PickerFilterCleared struct{}

func (PickerFilterCleared) eventKind() string { return "picker_filter_cleared" }

// PickerRowsChanged resizes the result window, e.g. after a resize.
type PickerRowsChanged struct{ Rows int }

func (PickerRowsChanged) eventKind() string { return "picker_rows_changed" }

// PickerClosed dismisses the overlay without choosing.
type PickerClosed struct{}

func (PickerClosed) eventKind() string { return "picker_closed" }

// HistoryReplaced swaps the transcript for replayed entries on resume.
type HistoryReplaced struct {
	// Entries is the replayed transcript, oldest first.
	Entries []Entry
}

func (HistoryReplaced) eventKind() string { return "history_replaced" }

// SecretState is an open credential prompt. While set, the composer collects
// a secret instead of a prompt: input renders masked, and submit delivers to
// the secret handler rather than the model. A secret must never become a
// UserSubmitted entry, or it would persist into history and transcripts.
type SecretState struct {
	// Label tells what the secret is for, e.g. "API key for OpenRouter".
	Label string
}

// SecretOpened starts credential entry.
type SecretOpened struct{ Label string }

func (SecretOpened) eventKind() string { return "secret_opened" }

// SecretClosed abandons credential entry without submitting.
type SecretClosed struct{}

func (SecretClosed) eventKind() string { return "secret_closed" }

// DoctorLine is one diagnostic row for the /doctor overlay output.
type DoctorLine struct {
	// Name is the check, e.g. "credentials".
	Name string
	// OK reports the check passed.
	OK bool
	// Detail is the outcome or fix. It never carries secret bytes: sources
	// are named ("environment (X)"), values never printed.
	Detail string
}

// ProbeFinished records a background connection probe. Probes run off the
// update goroutine so a slow endpoint never freezes the UI; the result lands
// here and the header reflects it.
type ProbeFinished struct {
	// OK reports the endpoint answered and accepted the credential.
	OK bool
	// Detail is the one-line outcome.
	Detail string
}

func (ProbeFinished) eventKind() string { return "probe_finished" }

// WorkspaceUpdated carries out-of-band chrome metadata: working directory,
// session label, and measured usage text. It never touches the transcript,
// so a resize or a usage tick cannot duplicate requests or mutate history.
type WorkspaceUpdated struct {
	// Cwd is the working directory for the status line. Empty leaves it.
	Cwd string
	// Session names the session, e.g. "#12". Empty leaves it.
	Session string
	// UsageText is preformatted usage detail. Empty leaves it.
	UsageText string
	// HasCwd, HasSession, HasUsage mark which fields apply: an empty string
	// can be a deliberate clear, not just an omission.
	HasCwd     bool
	HasSession bool
	HasUsage   bool
}

func (WorkspaceUpdated) eventKind() string { return "workspace_updated" }

// ModelOption is one enriched model row for the picker. Detail lines are
// built by the caller (which owns the catalog); the picker only renders
// them, so the TUI never fabricates model facts.
type ModelOption struct {
	// ID is the native "provider/id" reference the switcher resolves.
	ID string
	// Provider groups the picker and stays distinct per source catalog:
	// the same underlying model under two providers is two rows.
	Provider string
	// Title is the display name, defaulting to ID.
	Title string
	// Detail is preformatted context/price/capability text. Empty hides it.
	Detail string
	// Active marks the current model.
	Active bool
	// Name is the display name from the source catalog, distinct from the
	// canonical ID. Empty falls back to Title for matching.
	Name string
	// Context is the context window in tokens, or zero when unknown.
	Context int
	// PriceKnown reports explicit pricing metadata backs this row. Free is
	// meaningless without it, and the row must not claim a price.
	PriceKnown bool
	// Free reports verified zero pricing. Only meaningful when PriceKnown.
	Free bool
	// Origin names the snapshot behind the row: "live", "cached", or
	// "bundled". It tells the UI what freshness to claim, never a fact
	// about the model itself.
	Origin string
	// Capabilities lists confirmed capabilities, e.g. "reasoning",
	// "images". Absent means unconfirmed, never unsupported-by-default.
	Capabilities []string
}

// CatalogSnapshot is one model roster with freshness metadata.
type CatalogSnapshot struct {
	// Options is the merged roster, sorted by provider then id.
	Options []ModelOption
	// Origin names the snapshot: "live", "cached", "bundled", or
	// "loading" before the first rows arrive.
	Origin string
	// Stale reports the rows predate a refresh in flight: show them, but
	// say they are stale.
	Stale bool
}

// ModelsRefreshed swaps the model picker's roster for fresh catalog data.
//
// Seq pairs the result with its refresh: a slower older response must never
// overwrite a newer one, so the Program drops any result whose Seq trails
// the latest issued refresh.
type ModelsRefreshed struct {
	// Seq is the refresh generation this result answers.
	Seq int
	// Snapshot is the merged roster to show.
	Snapshot CatalogSnapshot
	// Err carries failure as text; empty means success. A failure keeps
	// the cached rows and records the reason on the picker.
	Err string
}

func (ModelsRefreshed) eventKind() string { return "models_refreshed" }

// ValidationFinished ends an async /connect credential check with the
// discovered models, or the reason it failed.
type ValidationFinished struct {
	// Provider and KeyIsNew pair the result with the pending flow: a stale
	// validation from an abandoned attempt must not open a picker.
	Provider string
	Key      string
	KeyIsNew bool
	// Models are the discovered ids to choose from.
	Models []string
	// Detail confirms success for the transcript.
	Detail string
	// Err carries failure as text; empty means success.
	Err string
}

func (ValidationFinished) eventKind() string { return "validation_finished" }
