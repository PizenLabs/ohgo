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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/tokens"
	diffview "github.com/PizenLabs/ohgo/tui/diff"
	"github.com/PizenLabs/ohgo/workspace"
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
//
// Entry is the canonical block representation: it holds logical content, never
// terminal coordinates or rendered bytes. ID identifies the block across
// scrolling, expansion and resize; rev is a cache token the retained layout
// uses to decide which projection is stale.
type Entry struct {
	// ID is the block's stable identity for the lifetime of this view. It is
	// assigned when the entry enters the transcript and is never derived from
	// a screen row.
	ID BlockID
	// rev advances whenever the entry's rendered projection can change. It is
	// a dirty token for the layout cache, not semantic state.
	rev uint64
	// Kind tells which payload is set.
	Kind EntryKind

	// UserText is set when Kind is EntryUser.
	UserText string
	// AssistantText is set when Kind is EntryAssistant.
	AssistantText string
	// Tool is set when Kind is EntryTool.
	Tool *ToolEntry
	// Shell is set when Kind is EntryShell: a "!" escape command and its
	// streamed output.
	Shell *ShellEntry
	// Notice is set when Kind is EntryNotice.
	Notice string
	// Task is set when Kind is EntryTask: the compact projection of the
	// durable task state.
	Task *TaskProjection
	// Recap is set when Kind is EntryRecap: the semantic completion footer,
	// appended after every response and tool block for the task has settled.
	// It is display metadata, never copyable block content.
	Recap string
	// SupportText is set when Kind is EntrySupport: the question when
	// SupportIsQuestion, else the answer.
	SupportText string
	// SupportIsQuestion marks a support question entry rather than an answer.
	SupportIsQuestion bool
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
	// EntryShell is a "!" escape command and its streamed output. It is an
	// interaction affordance, not a model turn, so it never enters history
	// or the session store.
	EntryShell EntryKind = "shell"
	// EntryNotice is a system notice, e.g. a compaction.
	EntryNotice EntryKind = "notice"
	// EntryTask is the compact projection of durable task state. It is a
	// projection, never an owner: it carries no execution authority and the
	// task domain remains the single source of truth.
	EntryTask EntryKind = "task"
	// EntryRecap is the trailing completion footer for a task. It is
	// appended only after the task is COMPLETED and the stream has settled,
	// so it always lands below the response it describes rather than in the
	// task block above it.
	EntryRecap EntryKind = "recap"
	// EntrySupport is one turn of the ephemeral $sup surface. It is rendered
	// but never persisted and never enters the model's context (§10.2, I17).
	EntrySupport EntryKind = "support"
)

// ToolEntry is the live state of one tool call.
type ToolEntry struct {
	// ID is the provider's tool call id.
	ID string
	// Name is the tool being called.
	Name string
	// Status is the lifecycle position.
	Status ToolStatus
	// Invocation describes what the call does, captured at start from its
	// arguments and never overwritten by the outcome. It keeps the badge
	// line truthful after the finish replaces Summary with the result.
	Invocation string
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
	// Expanded is presentation-only: whether the transcript shows the
	// bounded body below the compact summary. It never affects execution,
	// approval, or the stored outcome.
	Expanded bool
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

// ShellStatus is where a "!" escape command is in its lifecycle.
type ShellStatus string

// Shell lifecycle states. They mirror the tool lifecycle so the two blocks
// read the same way.
const (
	// ShellRunning means the command is executing.
	ShellRunning ShellStatus = "running"
	// ShellDone means the command exited with status 0.
	ShellDone ShellStatus = "done"
	// ShellError means the command failed, was cancelled, or exited non-zero.
	ShellError ShellStatus = "error"
)

// ShellEntry is the live state of one "!" escape command.
//
// It is deliberately not a ToolEntry: the escape is typed by the human, runs
// outside the agent loop, and is never persisted or sent to the model, so it
// shares the lifecycle vocabulary without sharing the tool's semantics.
type ShellEntry struct {
	// Command is the raw command as typed, without the leading "!".
	Command string
	// Output is the combined stdout and stderr streamed so far.
	Output string
	// Status is the lifecycle position.
	Status ShellStatus
	// ExitCode is the process exit status; -1 when unknown or aborted.
	ExitCode int
	// Err carries a start/cancel failure, empty on a normal exit.
	Err string
	// StartedAt and Duration bound execution. Duration is the engine's own
	// measurement, so the rendered elapsed time is the process's, not the
	// UI's render cadence.
	StartedAt time.Time
	Duration  time.Duration
	// Truncated marks that Output hit the runner's cap.
	Truncated bool
	// Seq identifies the run, so a late chunk from an aborted command is
	// dropped instead of appending to a newer block.
	Seq int
}

// PendingApproval is a question the user must answer before a tool runs, or a
// proposal decision rendered through the same permission boundary.
type PendingApproval struct {
	// ToolName is the tool that wants to run. It is empty for a proposal,
	// whose Proposal and Reason carry the context instead.
	ToolName string
	// Prompt, when non-empty, is a shell-specific label shown above the call
	// in the dialog. It exists for the "!" shell escape, whose confirmation
	// is not a tool invocation.
	Prompt string
	// Reason is why the prompt appears, when the tool declared one.
	Reason string
	// Summary describes what this particular call will do.
	Summary string
	// MutationPreview is the dedicated mutation preview, rendered separately
	// from Reason.
	MutationPreview agent.MutationPreview
	// Mutating marks a call that changes state (write, exec, destructive)
	// rather than reading. It is presentation only: it selects the frame
	// accent (rose vs amber), never the answer. The producer derives it from
	// the tool's declared tier, so the renderer never guesses from a name.
	Mutating bool
	// Choices are the offered answers.
	Choices []string
	// Cursor is the highlighted choice index.
	Cursor int
	// Proposal, when non-nil, marks this as a request-derived proposal rather
	// than a tool approval. It supplies the contextual question and the
	// resolutions; Choices mirrors the option labels so the existing
	// navigation and layout stay authoritative.
	Proposal *workspace.Proposal
	// Commit, when non-nil, marks this as the /commit proposal dialog. The
	// exact message and scope are rendered as dedicated rows in the same
	// permission surface.
	Commit *CommitProposalView
}

// IsProposal reports whether the pending question is a proposal decision.
func (p *PendingApproval) IsProposal() bool { return p != nil && p.Proposal != nil }

// Selected reports the currently highlighted choice.
func (p *PendingApproval) Selected() string {
	if p.Cursor < 0 || p.Cursor >= len(p.Choices) {
		return ""
	}
	return p.Choices[p.Cursor]
}

// Move shifts the cursor, stopping at either end.
//
// It deliberately does not wrap. Wrapping made every navigation key a way onto
// the affirmative choice: the cursor opens on the conservative option, and with
// two choices a forward move landed on the other index, so pressing Tab or an
// arrow to look around and then Enter reflexively ran the tool. Stopping at the
// ends keeps the conservative default the resting place, so the affirmative
// option is only ever reached by a deliberate move, never by wrap-around.
func (p *PendingApproval) Move(delta int) {
	if len(p.Choices) == 0 {
		p.Cursor = 0
		return
	}
	next := p.Cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(p.Choices) {
		next = len(p.Choices) - 1
	}
	p.Cursor = next
}

// TransitionPrompt is a pending workspace-transition proposal: a request the
// classifier placed in a lower-authority workspace, awaiting the user's
// decision.
//
// It owns the keyboard while set, so no turn can start until it is resolved
// and nothing the model could do is attributed to the proposed workspace
// before the user accepts (I2, I4-I6). It is ephemeral: it never enters the
// transcript, history, or session, and it carries no probability — Strength
// is the classifier's evidence level (I12).
type TransitionPrompt struct {
	// Text is the turn awaiting the decision. It is replayed under whichever
	// workspace the decision leaves in force.
	Text string
	// Current and Proposed are workspace labels, e.g. "BUILD" and "ASK".
	Current  string
	Proposed string
	// CurrentAccent and ProposedAccent are the semantic accents for the two
	// workspaces, from the producer. They are presentation only.
	CurrentAccent  string
	ProposedAccent string
	// Intent is the classified request, e.g. "READ".
	Intent string
	// Strength is the classifier's evidence level, retained for diagnostics
	// and tests. It is deliberately not rendered: the dialog explains the
	// request in words, because a level is not a calibrated score and a
	// percentage would be fabricated (I12).
	Strength string
	// Question is the heading, e.g. "Switch to ASK?".
	Question string
	// Reason is the one-line explanation under the heading.
	Reason string
	// Selection is the highlighted choice: 0 keeps the current workspace
	// (the conservative default), 1 accepts the proposed one.
	Selection int
}

// Selected reports whether the proposed transition is highlighted. The
// default is false: Enter alone never changes authority (I2).
func (t *TransitionPrompt) Selected() bool { return t.Selection == 1 }

// Move shifts the highlight, stopping at either end. Like PendingApproval it
// deliberately does not wrap, so an extra keypress cannot land on the
// authority-changing choice by accident.
func (t *TransitionPrompt) Move(delta int) {
	next := t.Selection + delta
	if next < 0 {
		next = 0
	}
	if next > 1 {
		next = 1
	}
	t.Selection = next
}

// Status is the persistent footer.
type Status struct {
	// Model is the model in use.
	Model string
	// Effort is the active thinking effort for the header badge, e.g. "max".
	// Empty hides the badge: the view never invents a level, and "off"
	// normalizes to empty since disabled thinking has no level to show.
	Effort string
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
	// Workspace is the execution authority label, e.g. "BUILD". Empty hides
	// it. It is authority identity, not decoration: it tells the user which
	// boundary the session is running under.
	Workspace string
	// WorkspaceAccent is the semantic 256-color index for the workspace, from
	// workspace.Style(). Empty falls back to a neutral accent.
	WorkspaceAccent string
	// User is the project worker display name from the project-local metadata.
	// Empty hides it: the view never invents a name.
	User string
	// Greeting is the time-of-day greeting, e.g. "Good evening, Jack."
	Greeting string
	// GitBranch is the short branch name (or short SHA when detached).
	GitBranch string
	// GitStatus is the cleanliness summary, e.g. "clean" or "3 changed".
	GitStatus string
	// GitStaged, GitModified, and GitUntracked are the repository status
	// counts when GitKnown is set. They let /status name which kind of change
	// is present; unknown counts stay zero and GitKnown false, so the card
	// never fabricates a clean tree from a failed status read.
	GitStaged    int
	GitModified  int
	GitUntracked int
	GitKnown     bool
	// Ready is runtime readiness, e.g. "ready". Empty renders "ready".
	Ready string
	// Version is the binary version, e.g. "0.1.0-dev" or "v1.0.0". Empty
	// hides it: the header and the welcome frame show no version rather
	// than an invented one.
	Version string
	// UpdateAvailable is a newer release found by the background check,
	// e.g. "v1.2.3". Empty hides the hint. It is chrome, not transcript:
	// the notification never enters the conversation or the model context.
	UpdateAvailable string
}

// VersionLabel normalizes a raw version for display alongside the brand.
//
// It trims surrounding space, reports "" for unknown, and ensures the
// leading "v" ("1.0.0" becomes "v1.0.0", "v1.0.0" passes through), so every
// surface spells the release the same way without each call site repeating
// the rule.
func VersionLabel(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
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
	// ComposerScroll is the first display row of the composer's visible
	// window. It tracks the cursor when the input folds past maxComposerRows,
	// so the block cursor is always painted; zero shows the top rows.
	ComposerScroll int
	// Submitted remembers past prompts for history recall, oldest first.
	Submitted []string
	// HistCursor selects a Submitted entry, or -1 for the live input.
	HistCursor int
	// HistDraft stashes the live input while history is being browsed.
	HistDraft string
	// Pastes holds the payloads behind collapsed paste chips in Input. A chip
	// renders inline as "[Paste #N - K lines]" while its full text waits here
	// for submission; the payload is released once its label leaves Input.
	Pastes []PasteChip
	// NextPasteID numbers the next collapsed paste. It never reuses an id, so
	// a stale label can never resurrect a deleted payload.
	NextPasteID int
	// Queued holds prompts typed while a turn runs, in order. They submit
	// automatically when the turn completes.
	Queued []string
	// Scroll is how many lines above the bottom edge the viewport shows.
	// Zero follows the live edge. It is retained as the event-level mirror of
	// the user's scroll intent; TranscriptLayout is the geometry authority,
	// so this number is never interpreted as a terminal row.
	Scroll int
	// scrollIntent is the user's accumulated scroll distance, untouched by
	// output growth. The viewport tracker uses it to distinguish "the user
	// scrolled" from "content arrived while detached"; the Scroll mirror keeps
	// both effects for compatibility with callers that observe it.
	scrollIntent int
	// ScrollSeq advances only on an explicit scroll transition. The viewport
	// tracker treats it as the boundary between a user action and reflow.
	ScrollSeq int
	// Follow reports the viewport tracks new output. Scrolling up clears it;
	// scrolling back to the bottom restores it.
	Follow bool
	// Selection is the application-owned logical selection, or nil. Positions
	// name a block and a row inside it, never a terminal coordinate.
	Selection *Selection
	// CopyNotice is transient clipboard feedback, e.g. "Copied", shown in
	// the existing top header at the far right. It is ephemeral UI state
	// only: it never enters the document, the transcript, the session, the
	// runtime authority, or any execution state. It is set only after the
	// clipboard path reports success and clears automatically after
	// copyNoticeTTL. Empty means no confirmation is visible.
	CopyNotice string
	// CopyGen identifies the current clipboard confirmation generation. Every
	// successful copy increments it and schedules an expiry carrying that
	// generation; a stale expiry whose generation trails the current one is
	// ignored, so the latest copy always owns the visible lifetime.
	CopyGen uint64
	// Width and Height are the terminal size in cells. Zero means unknown,
	// in which case the view renders everything without slicing.
	Width  int
	Height int
	// Theme names the palette: "dark" (default) or "light".
	Theme string
	// Picker is the open selection overlay, or nil.
	Picker *PickerState
	// Plane is the open model control plane, or nil. It is the persistent
	// split-pane surface behind /model; the single-overlay Picker remains for
	// sessions, jobs, and the connect flow.
	Plane *ModelPlaneState
	// ConnectKey is the open /connect API-key modal, or nil. It is a working
	// modal like the Provider Manager: it owns the keyboard, edits a masked
	// buffer, and reports validation in place. The buffer never enters the
	// transcript, history, or a session.
	ConnectKey *ConnectKeyView
	// ConnOK and ConnDetail report the last connection probe. Unknown until
	// the first probe: the UI never blocks startup on network.
	ConnOK     bool
	ConnDetail string
	// Pending is a non-nil approval prompt.
	Pending *PendingApproval
	// Commit is the /commit lifecycle, or nil when idle. It is the single
	// source the activity line, header, and tests read; the proposal payload
	// itself lives in the Program because it contains non-renderable state.
	Commit *CommitActivity
	// QuitConfirm is a non-nil quit-confirmation prompt opened by /q. It
	// reuses PendingApproval for its Yes/No choices so the conservative
	// default and the no-wrap navigation match the tool approval dialog.
	QuitConfirm *PendingApproval
	// Transition is a pending workspace-transition proposal, or nil. While
	// set it owns the keyboard and the decision surface: no turn may start
	// until the user accepts or rejects it, so no provider request, tool, or
	// mutation runs while it is unresolved (I4-I6). It is ephemeral and never
	// enters the transcript, history, or session.
	Transition *TransitionPrompt
	// Permissions is the open authority selector, or nil. It is a modal
	// overlay like the picker: while set it owns the keyboard and the middle
	// of the screen, and confirming routes the chosen name through the
	// workspace owner rather than changing authority here.
	Permissions *PermissionsState
	// StatusModal is the open /status card, or nil. It is a read-only modal:
	// while set it owns the keyboard and the middle of the screen, and any
	// key dismisses it back to the composer. It is a snapshot taken when the
	// card opened, so it never enters the transcript, history, or session.
	StatusModal *StatusView
	// UsageModal is the open /usage inspector, or nil. It is the same
	// read-only modal family as StatusModal: any key dismisses it, and it
	// never enters the transcript, history, or session.
	UsageModal *UsageView
	// SessionModal is the open /sessions Session Manager, or nil. Unlike the
	// read-only cards it owns a working roster: while set it captures every
	// key, moves its own cursor, and dispatches write actions through the
	// program. It never enters the transcript, history, or session itself.
	SessionModal *SessionManagerState
	// HelpModal is the open Command & Help Center, or nil. It is the single
	// surface behind /help, /commands, the "?" shortcut, and ^p on a fresh
	// session. It is a working modal like the Session Manager: it owns the
	// keyboard, filters its roster live, and runs the highlighted command. It
	// never enters the transcript, history, or session itself.
	HelpModal *HelpCenterState
	// InfoModal is the open /info page, or nil. It is a read-only modal like
	// the /status card: any key dismisses it. It never enters the transcript,
	// history, or session.
	InfoModal *InfoView
	// InformationModal is an alias field for InfoModal, kept in sync.
	InformationModal *InformationView // ConnectModal is the open /connect Provider Manager, or nil. It is a
	// working modal like the Session Manager: it owns the keyboard, moves its
	// own cursor, and starts the guided setup for the highlighted provider. It
	// never enters the transcript, history, or session itself.
	ConnectModal *ConnectView
	// Diff is the open interactive diff inspector, or nil. It is a full-screen
	// modal overlay: while set it owns the keyboard and the pane area, and it
	// reads the repository through its own on-demand git driver. It never
	// enters the transcript, history, or session.
	Diff *diffview.Model
	// Quit is set once the session has been asked to exit.
	Quit bool
	// PaletteCursor selects within the inline palette (the "/" command,
	// "@" file, and "$" support suggestions share one overlay). It is derived
	// UI cursor state, reset whenever the input text itself changes.
	PaletteCursor int
	// PaletteHide dismisses the inline palette while keeping the typed text
	// (esc). Any input change shows it again.
	PaletteHide bool
	// PaletteFiles is the workspace roster the "@" trigger completes against,
	// installed by the owner. Paths are slash-relative; a trailing slash marks
	// a directory. Empty disables file completion.
	PaletteFiles []string
	// closedAssistant marks that the trailing assistant entry has ended, so
	// the next turn opens a new one.
	closedAssistant bool
	// recapEmitted records the task IDs whose completion footer is already in
	// the transcript. The footer is a separate trailing entry, so this map is
	// the dedup memory that keeps a repeated projection from stacking a second
	// line. A new task identity is a new footer.
	recapEmitted map[string]bool
	// Support reports that the ephemeral $sup surface is open. It is UI state
	// only: the workspace and session authority are unchanged while it is set
	// (§34). It is derived from SupportPanel: non-nil means open.
	Support bool
	// SupportPanel is the ephemeral support overlay. Non-nil while the
	// surface is open; nil after close discards it. It never enters the
	// normal transcript, history, or session.
	SupportPanel *SupportPanel
	// SupportSeq identifies the latest support turn. Support lifecycle
	// events carry it so a late event from an old support turn cannot
	// mutate the new one.
	SupportSeq int
	// TurnSeq identifies the latest turn. Lifecycle events carry it so a late
	// event from an old turn cannot mutate the new one.
	TurnSeq int
	// TurnStartedAt is when the current turn began, zero when unknown. It is
	// the authoritative start for the model-phase activity line: elapsed time
	// derives from it, never from render time, so the UI cannot invent a
	// duration the runtime did not measure.
	TurnStartedAt time.Time
	// Cancelled records the current turn reached its terminal cancellation
	// state. It makes repeated cancels idempotent and lets late progress
	// events be dropped instead of resurrecting Running.
	Cancelled bool
	// OutputTokens counts estimated output tokens streamed this turn. It is
	// reset on every new turn and recomputed from the trailing assistant
	// text on each delta, so the activity line can report live progress
	// without inventing telemetry the runtime did not measure.
	OutputTokens int
	// SessionActive reports that a prompt has run in this session. It gates
	// progressive disclosure: while false the helper bar teaches the
	// shortcuts; once true it shows the measured session telemetry instead.
	SessionActive bool
	// SessionIn and SessionOut are cumulative per-session token counts from
	// the runtime's own ledger. Zero means unknown: the helper bar hides an
	// unmeasured counter rather than rendering a fabricated "0 in / 0 out".
	SessionIn  int
	SessionOut int
	// SessionElapsed is the measured session duration at the last telemetry
	// sample. It is stored rather than read at render time so Render stays
	// pure and deterministic.
	SessionElapsed time.Duration
	// nextBlockID and nextEntryRev allocate stable identities and dirty
	// tokens for transcript blocks. They never leave the UI state.
	nextBlockID  int
	nextEntryRev uint64
	// dirty and dirtyReset are the layout cache's invalidation hints. They
	// record which blocks Apply changed, so a refresh does not have to scan
	// the whole document to find a single streamed block.
	dirty      []int
	dirtyReset bool
	// runningTools counts ToolRunning blocks. DeriveRuntime asks whether any
	// tool runs on every header render; without the counter an idle 10k-block
	// transcript would be scanned per frame. runtimeTracked distinguishes a
	// counter maintained by Apply from a hand-built fixture, which still falls
	// back to a scan.
	runningTools   int
	runtimeTracked bool
	// assistantText reports the most recent assistant entry has streamed
	// text. DeriveRuntime asks this on every busy frame; the flag keeps an
	// idle/tool-only history from being scanned for it.
	assistantText bool
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
	if !s.runtimeTracked {
		// A fixture may have installed entries directly. Count once before
		// the incremental bookkeeping takes over.
		s.runningTools = 0
		s.assistantText = false
		for i := range s.Entries {
			if s.Entries[i].Kind == EntryTool && s.Entries[i].Tool != nil && s.Entries[i].Tool.Status == ToolRunning {
				s.runningTools++
			}
		}
		for i := len(s.Entries) - 1; i >= 0; i-- {
			if s.Entries[i].Kind == EntryAssistant {
				s.assistantText = s.Entries[i].AssistantText != ""
				break
			}
		}
		s.runtimeTracked = true
	}
	switch e := event.(type) {
	case UserSubmitted:
		seq := e.Seq
		if seq == 0 {
			seq = s.TurnSeq + 1
		}
		s.TurnSeq = seq
		s.Cancelled = false
		s.TurnStartedAt = e.At
		// A resolved decision never outlives the turn it was made for: a new
		// turn supersedes any proposal still held (I: stale state cannot
		// survive into another turn).
		s.Transition = nil
		s.pushEntry(Entry{Kind: EntryUser, UserText: e.Text})
		s.Input = ""
		s.Cursor = 0
		s.Pastes = nil
		s.Status.Busy = true
		// The prompt joins recallable history. Consecutive duplicates are
		// collapsed so holding enter does not flood the history.
		if len(s.Submitted) == 0 || s.Submitted[len(s.Submitted)-1] != e.Text {
			s.Submitted = append(s.Submitted, e.Text)
		}
		s.HistCursor = -1
		s.HistDraft = ""
		s.OutputTokens = 0
		// The first prompt flips the helper bar from shortcuts to telemetry;
		// a run that later clears returns it to the clean state.
		s.SessionActive = true
		s.Selection = nil
		s.growScroll(countLines(e.Text))

	case AssistantDelta:
		if e.Seq != 0 && e.Seq != s.TurnSeq {
			return
		}
		if s.Cancelled {
			return
		}
		s.appendAssistant(e.Text)
		s.OutputTokens = s.currentOutputTokens()
		s.growScroll(strings.Count(e.Text, "\n"))

	case AssistantDone:
		// A turn boundary supersedes any pending proposal. A proposal can
		// never be open while a turn runs, so this only ever drops state a
		// resolved decision should already have cleared.
		s.Transition = nil
		if e.Seq != 0 && e.Seq != s.TurnSeq {
			return
		}
		// Close the trailing assistant entry: without this the next turn would
		// append to it and the transcript would read as one continuous answer.
		if open := s.openAssistant(); open >= 0 {
			s.closedAssistant = true
		}
		s.Status.Busy = false
		// The stream is settled now, so a task that completed during the turn
		// can close with its footer at the true tail of the output.
		s.appendTaskRecap()

	case TurnCancelled:
		// The same turn-boundary rule as AssistantDone: a cancellation can
		// never race an open proposal, because no turn runs while one is open.
		s.Transition = nil
		if e.Seq != 0 && e.Seq != s.TurnSeq {
			return
		}
		if s.Cancelled {
			return
		}
		s.Cancelled = true
		s.Status.Busy = false
		s.Queued = nil
		if open := s.openAssistant(); open >= 0 {
			s.closedAssistant = true
		}
		for i := range s.Entries {
			entry := &s.Entries[i]
			if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.Status == ToolRunning {
				entry.Tool.Status = ToolError
				entry.Tool.Summary = "cancelled"
				entry.Tool.Text = "cancelled"
				s.bumpEntry(i)
				if s.runningTools > 0 {
					s.runningTools--
				}
			}
		}
		text := e.Text
		if text == "" {
			text = "cancelled"
		}
		s.pushEntry(Entry{Kind: EntryNotice, Notice: text})
		s.growScroll(countLines(text))

	case ToolStarted:
		if e.Seq != 0 && e.Seq != s.TurnSeq {
			return
		}
		if s.Cancelled {
			return
		}
		// A second announcement for the same call updates nothing: an
		// empty partial report must never open a duplicate block next to
		// the running one.
		if existing := s.tool(e.ID); existing != nil {
			return
		}
		s.Entries = append(s.Entries, Entry{
			Kind: EntryTool,
			Tool: &ToolEntry{
				ID:         e.ID,
				Name:       e.Name,
				Status:     ToolRunning,
				Invocation: e.Summary,
				Summary:    e.Summary,
				StartedAt:  e.At,
			},
		})
		s.assignIdentity(len(s.Entries) - 1)
		s.runningTools++
		s.growScroll(1)

	case ToolPreview:
		if e.Seq != 0 && e.Seq != s.TurnSeq {
			return
		}
		if s.Cancelled {
			return
		}
		// A preview updates the summary of a *running* call only. Once a call
		// has finished, a late preview would resurrect stale progress on top
		// of the real outcome.
		if idx := s.toolIndex(e.ID); idx >= 0 && s.Entries[idx].Tool != nil && s.Entries[idx].Tool.Status == ToolRunning {
			s.Entries[idx].Tool.Summary = e.Summary
			s.bumpEntry(idx)
		}

	case ToolFinished:
		if e.Seq != 0 && e.Seq != s.TurnSeq {
			return
		}
		if s.Cancelled {
			return
		}
		idx := s.toolIndex(e.ID)
		if idx < 0 {
			return
		}
		tool := s.Entries[idx].Tool
		if tool.Status == ToolRunning && s.runningTools > 0 {
			s.runningTools--
		}
		tool.EndedAt = e.At
		// The outcome replaces the running summary, but the invocation
		// stays: the badge keeps saying what ran while the summary line
		// says what it produced.
		tool.Summary = e.Summary
		tool.Text = e.Text
		tool.Truncated = e.Truncated
		if e.IsError {
			tool.Status = ToolError
		} else {
			tool.Status = ToolDone
		}
		s.bumpEntry(idx)
		s.growScroll(countLines(e.Text) + 1)

	case ToolToggled:
		// Expansion is presentation-only: flipping it never re-runs the
		// tool, mutates its result, or touches approval state.
		if idx := s.toolIndex(e.ID); idx >= 0 {
			s.Entries[idx].Tool.Expanded = !s.Entries[idx].Tool.Expanded
			s.bumpEntry(idx)
		}

	case ShellStarted:
		s.Entries = append(s.Entries, Entry{Kind: EntryShell, Shell: &ShellEntry{
			Command:   e.Command,
			Status:    ShellRunning,
			Seq:       e.Seq,
			StartedAt: e.At,
		}})
		s.assignIdentity(len(s.Entries) - 1)
		s.growScroll(1)

	case ShellDelta:
		shell := s.shell(e.Seq)
		if shell == nil || shell.Status != ShellRunning {
			return
		}
		shell.Output += e.Text
		if idx := s.shellIndex(e.Seq); idx >= 0 {
			s.bumpEntry(idx)
		}
		s.growScroll(strings.Count(e.Text, "\n"))

	case ShellFinished:
		idx := s.shellIndex(e.Seq)
		if idx < 0 {
			return
		}
		shell := s.Entries[idx].Shell
		shell.Duration = e.Duration
		shell.ExitCode = e.ExitCode
		shell.Truncated = e.Truncated
		shell.Err = e.Err
		switch {
		case e.Err != "", e.ExitCode != 0:
			shell.Status = ShellError
		default:
			shell.Status = ShellDone
		}
		s.bumpEntry(idx)
		s.growScroll(1)

	case Notice:
		s.pushEntry(Entry{Kind: EntryNotice, Notice: e.Text})
		s.growScroll(countLines(e.Text))

	case UpdateFound:
		// Availability is chrome, not conversation: it must never enter the
		// transcript, the session store, or the model's context. An empty
		// version is ignored rather than clearing an existing hint, so a
		// failed or empty refresh cannot erase a real observation.
		if version := strings.TrimSpace(e.Version); version != "" {
			s.Status.UpdateAvailable = VersionLabel(version)
		}

	case TaskProjected:
		// The projection replaces the current task block in place, so a long
		// turn does not stack a fresh panel on every todo change. A dropped
		// empty projection keeps the previous one rather than blanking it.
		if e.Projection.ID == "" {
			return
		}
		projection := e.Projection
		index := -1
		for i := len(s.Entries) - 1; i >= 0; i-- {
			if s.Entries[i].Kind == EntryTask {
				index = i
				break
			}
		}
		if index >= 0 {
			s.Entries[index].Task = &projection
			s.bumpEntry(index)
		} else {
			s.Entries = append(s.Entries, Entry{Kind: EntryTask, Task: &projection})
			s.assignIdentity(len(s.Entries) - 1)
			s.growScroll(len(projection.Items) + 2)
		}
		// A completed projection that arrives after the stream already
		// settled (or outside a turn) still gets its footer; one arriving
		// mid-stream waits for AssistantDone.
		s.appendTaskRecap()

	case HistoryReplaced:
		// A resumed session continues rather than restarts: the replayed
		// transcript replaces the view outright. It is a fresh session
		// boundary, so the progressive-disclosure telemetry restarts clean:
		// the prior session's token counts and clock do not belong to it.
		s.replaceEntries(e.Entries)
		s.closedAssistant = true
		s.recapEmitted = nil
		s.OutputTokens = 0
		s.Transition = nil
		s.Selection = nil
		// A transient clipboard confirmation never survives a session
		// boundary: the selection it confirmed no longer exists.
		s.CopyNotice = ""
		s.Scroll = 0
		s.scrollIntent = 0
		s.ScrollSeq++
		s.Follow = true
		s.resetSessionMetrics()
		// A waiting or terminal commit phase belongs to the session that
		// produced it. An execution already in flight stays visible so its
		// real result is reported rather than silently swallowed.
		if !s.CommitActive() {
			s.Commit = nil
		}

	case ContextUpdated:
		s.Status.ContextUsed = e.Used
		s.Status.ContextWindow = e.Window

	case ModelEffortUpdated:
		s.Status.Effort = NormalizeEffort(e.Effort)

	case SessionMetricsUpdated:
		s.SessionIn = e.Input
		s.SessionOut = e.Output
		s.SessionElapsed = e.Elapsed

	case Compacted:
		// Compaction is announced as a notice, never silently: the user must
		// be able to tell that history was summarised rather than lost.
		s.Entries = append(s.Entries, Entry{
			Kind:   EntryNotice,
			Notice: fmt.Sprintf("context compacted: %d messages summarised, %d retained", e.Messages, e.Retained),
		})
		s.assignIdentity(len(s.Entries) - 1)
		s.Status.ContextUsed = e.AfterTokens
		s.Status.ContextWindow = e.Window
		s.growScroll(1)

	case CommitChanged:
		if e.Phase == CommitIdle {
			s.Commit = nil
			break
		}
		started := e.At
		if started.IsZero() && s.Commit != nil {
			// A transition that names no new start keeps the phase's real
			// start time; substituting render time would fabricate elapsed.
			started = s.Commit.StartedAt
		}
		s.Commit = &CommitActivity{Phase: e.Phase, StartedAt: started, Text: e.Text}

	case ApprovalAsked:
		s.Pending = &PendingApproval{
			ToolName:        e.ToolName,
			Prompt:          e.Prompt,
			Reason:          e.Reason,
			Summary:         e.Summary,
			MutationPreview: e.MutationPreview,
			Mutating:        e.Mutating,
			Choices:         e.Choices,
			Cursor:          defaultApprovalSelection(e.Choices),
			Proposal:        e.Proposal,
			Commit:          e.Commit,
		}

	case ApprovalMoved:
		if s.Pending != nil {
			s.Pending.Move(e.Delta)
		}

	case ApprovalAnswered:
		s.Pending = nil

	case QuitConfirmOpened:
		s.QuitConfirm = &PendingApproval{
			ToolName: "quit",
			Summary:  "Really quit?",
			Choices:  append([]string(nil), quitConfirmChoices...),
			Cursor:   defaultApprovalSelection(quitConfirmChoices),
		}

	case QuitConfirmMoved:
		if s.QuitConfirm != nil {
			s.QuitConfirm.Move(e.Delta)
		}

	case QuitConfirmClosed:
		s.QuitConfirm = nil

	case TransitionProposed:
		// The highlight opens on "keep current", so a stray Enter reaffirms
		// the workspace instead of moving authority (I2).
		s.Transition = &TransitionPrompt{
			Text:           e.Text,
			Current:        e.Current,
			Proposed:       e.Proposed,
			CurrentAccent:  e.CurrentAccent,
			ProposedAccent: e.ProposedAccent,
			Intent:         e.Intent,
			Strength:       e.Strength,
			Question:       e.Question,
			Reason:         e.Reason,
			Selection:      0,
		}

	case TransitionMoved:
		if s.Transition != nil {
			s.Transition.Move(e.Delta)
		}

	case TransitionResolved:
		s.Transition = nil

	case PermissionsOpened:
		// The cursor parks on the authority in force, so Enter on an
		// unchanged selection is a deliberate reaffirmation rather than a
		// silent jump to another mode.
		s.Permissions = &PermissionsState{
			Cursor:  authorityIndexForLabel(s.Status.Workspace),
			Title:   e.Title,
			Reason:  e.Reason,
			Request: e.Request,
		}

	case PermissionsMoved:
		if s.Permissions != nil {
			s.Permissions.Move(e.Delta)
		}

	case PermissionsClosed:
		s.Permissions = nil

	case StatusModalOpened:
		// The card renders a snapshot, so a later state change cannot make a
		// fact it already printed stale underneath it.
		view := e.View
		s.StatusModal = &view

	case StatusModalClosed:
		s.StatusModal = nil

	case UsageModalOpened:
		// Same snapshot contract as the status card: the facts are frozen at
		// open, so nothing already printed can go stale underneath it.
		view := e.View
		s.UsageModal = &view

	case UsageModalClosed:
		s.UsageModal = nil

	case InfoModalOpened:
		view := e.View
		s.InfoModal = &view
		s.InformationModal = &view

	case InfoModalClosed:
		s.InfoModal = nil
		s.InformationModal = nil

	case InformationModalOpened:
		view := e.View
		s.InformationModal = &view
		s.InfoModal = &view

	case InformationModalClosed:
		s.InformationModal = nil
		s.InfoModal = nil
	case SessionManagerOpened:
		// The roster is a snapshot taken when the modal opened, so a later
		// store change cannot make a printed row stale underneath it.
		view := e.View
		s.SessionModal = &view

	case SessionManagerClosed:
		s.SessionModal = nil

	case SessionManagerMoved:
		if s.SessionModal != nil {
			s.SessionModal.Move(e.Delta)
		}

	case SessionManagerPaged:
		if s.SessionModal != nil {
			s.SessionModal.Page(e.Delta)
		}

	case SessionManagerTop:
		if s.SessionModal != nil {
			s.SessionModal.Top()
		}

	case SessionManagerBottom:
		if s.SessionModal != nil {
			s.SessionModal.Bottom()
		}

	case SessionManagerReloaded:
		if s.SessionModal != nil {
			s.SessionModal.SetItems(e.Items)
			s.SessionModal.Mode = SessionBrowse
			s.SessionModal.Rename = ""
			s.SessionModal.ConfirmID = 0
			s.SessionModal.Confirm = ""
			s.SessionModal.Notice = e.Notice
		}

	case SessionManagerRenameStarted:
		if s.SessionModal != nil {
			if item, ok := s.SessionModal.Selected(); ok {
				s.SessionModal.Mode = SessionRename
				s.SessionModal.Rename = item.Title
				s.SessionModal.Notice = ""
			}
		}

	case SessionManagerRenameChanged:
		if s.SessionModal != nil {
			s.SessionModal.Rename = e.Text
		}

	case SessionManagerConfirmAsked:
		if s.SessionModal != nil && e.ID != 0 {
			s.SessionModal.Mode = SessionConfirm
			s.SessionModal.ConfirmID = e.ID
			s.SessionModal.Confirm = e.Action
			s.SessionModal.Notice = ""
		}

	case SessionManagerModeCancelled:
		if s.SessionModal != nil {
			s.SessionModal.Mode = SessionBrowse
			s.SessionModal.Rename = ""
			s.SessionModal.ConfirmID = 0
			s.SessionModal.Confirm = ""
		}

	case SessionManagerNoticed:
		if s.SessionModal != nil {
			s.SessionModal.Notice = e.Text
		}

	case HelpCenterOpened:
		// The roster is a snapshot taken when the modal opened, so a later
		// registry change cannot make a printed row stale underneath it.
		view := e.View
		s.HelpModal = &view
		// Size the roster window from the modal interior immediately: a zero
		// VisibleRows means "render everything", which would let the table eat
		// the action hint below it.
		s.HelpModal.VisibleRows = helpCenterVisibleRows(*s)

	case HelpCenterClosed:
		s.HelpModal = nil

	case HelpCenterMoved:
		if s.HelpModal != nil {
			s.HelpModal.Move(e.Delta)
		}

	case HelpCenterViewSwitched:
		if s.HelpModal != nil {
			s.HelpModal.SwitchView(e.Delta)
		}

	case HelpCenterPaged:
		if s.HelpModal != nil {
			s.HelpModal.Page(e.Delta)
		}

	case HelpCenterTop:
		if s.HelpModal != nil {
			s.HelpModal.Top()
		}

	case HelpCenterBottom:
		if s.HelpModal != nil {
			s.HelpModal.Bottom()
		}

	case HelpCenterTyped:
		if s.HelpModal != nil {
			s.HelpModal.InsertFilterText(e.Text)
		}

	case HelpCenterBackspaced:
		if s.HelpModal != nil {
			s.HelpModal.DeleteFilterBefore()
		}

	case HelpCenterDeleted:
		if s.HelpModal != nil {
			s.HelpModal.DeleteFilterAt()
		}

	case HelpCenterFilterCursorMoved:
		if s.HelpModal != nil {
			s.HelpModal.MoveFilterCursor(e.Delta)
		}

	case HelpCenterFilterCleared:
		if s.HelpModal != nil {
			s.HelpModal.ClearFilter()
		}

	case ConnectModalOpened:
		// The roster is a snapshot taken when the modal opened, so a later
		// catalog change cannot make a printed row stale underneath it.
		view := e.View
		s.ConnectModal = &view

	case ConnectModalClosed:
		s.ConnectModal = nil

	case ConnectModalMoved:
		if s.ConnectModal != nil {
			s.ConnectModal.Move(e.Delta)
		}

	case ConnectModalPaged:
		if s.ConnectModal != nil {
			s.ConnectModal.Page(e.Delta)
		}

	case ConnectModalTop:
		if s.ConnectModal != nil {
			s.ConnectModal.Top()
		}

	case ConnectModalBottom:
		if s.ConnectModal != nil {
			s.ConnectModal.Bottom()
		}

	case InputChanged:
		s.Input = e.Text
		s.Cursor = len([]rune(e.Text))
		s.PaletteCursor = 0
		s.PaletteHide = false
		s.prunePastes()
		s.syncComposerScroll()

	case ComposerChanged:
		s.setComposer(e.Text, e.Cursor)

	case PasteCollapsed:
		s.NextPasteID++
		chip := PasteChip{ID: s.NextPasteID, Payload: e.Text, Lines: e.Lines}
		s.Pastes = append(s.Pastes, chip)
		label := pasteChipLabel(chip)
		s.setComposer(insertAt(s.Input, s.Cursor, label), s.Cursor+len([]rune(label)))

	case PaletteMoved:
		s.PaletteHide = false
		s.movePalette(e.Delta)

	case PaletteFilesLoaded:
		s.PaletteFiles = append([]string(nil), e.Files...)

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
		s.scrollIntent += e.Delta
		// Attachment follows the user's intent, not the compatibility mirror:
		// while detached, growScroll also advances Scroll for output that has
		// arrived. Using Scroll here would make the wheel need one tick per
		// streamed line before it could reattach.
		if s.scrollIntent <= 0 {
			s.Scroll = 0
			s.scrollIntent = 0
			s.Follow = true
		} else {
			s.Follow = false
		}
		s.ScrollSeq++

	case ScrollToBottom:
		s.Scroll = 0
		s.scrollIntent = 0
		s.Follow = true
		s.ScrollSeq++

	case TerminalResized:
		s.Width = e.Width
		s.Height = e.Height
		if s.Picker != nil {
			s.Picker.SetVisibleRows(pickerRowsFor(*s))
		}
		if s.Plane != nil {
			s.Plane.SetVisibleRows(planeRowsFor(*s))
		}
		if s.SessionModal != nil {
			s.SessionModal.SetVisibleRows(sessionManagerVisibleRows(*s))
		}
		if s.HelpModal != nil {
			s.HelpModal.VisibleRows = helpCenterVisibleRows(*s)
		}
		if s.ConnectModal != nil {
			s.ConnectModal.SetVisibleRows(connectVisibleRows(*s))
		}
		// The fold width changes with the terminal, so the visible window must
		// be recomputed or a resize could scroll the caret out of view.
		s.syncComposerScroll()

	case ThemeChanged:
		s.Theme = NormalizeTheme(e.Name)

	case DiffOpened:
		s.Diff = e.Model

	case DiffClosed:
		s.Diff = nil

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

	case PickerToggled:
		if s.Picker != nil {
			s.Picker.TogglePending()
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
				items := optionsToItems(e.Snapshot.Options)
				if !samePickerItems(s.Picker.Items, items) {
					s.Picker.SetItems(items)
				}
				if e.Snapshot.Origin != "" {
					s.Picker.Origin = e.Snapshot.Origin
				}
			}
			s.Picker.ErrMsg = e.Err
		}
		if s.Plane != nil {
			s.Plane.Loading = false
			s.Plane.Refreshing = false
			s.Plane.Stale = false
			if len(e.Snapshot.Options) > 0 {
				if !sameModelOptions(s.Plane.Models, e.Snapshot.Options) {
					s.Plane.SetModels(e.Snapshot.Options)
				}
				if e.Snapshot.Origin != "" {
					s.Plane.Origin = e.Snapshot.Origin
				}
			}
			s.Plane.ErrMsg = e.Err
			// A refresh replaces the rows the variant options point at, so
			// an open prompt cannot survive it: cancel and say why rather
			// than confirming a stale row.
			if s.Plane.Variant != nil {
				s.Plane.CancelVariant()
				s.Plane.Notice = "catalog refreshed — choose the variant again"
			}
		}

	case PickerClosed:
		s.Picker = nil

	case ModelPlaneOpened:
		s.Plane = NewModelPlane(e)
		// Size the list window from the modal interior immediately: a
		// zero VisibleRows means "render everything", which would let the
		// lists eat the detail block. The program's open path recomputes
		// this too; doing it here keeps every opener correct.
		s.Plane.SetVisibleRows(planeRowsFor(*s))

	case ModelPlaneClosed:
		s.Plane = nil

	case PlaneMoved:
		if s.Plane != nil {
			if s.Plane.Variant != nil {
				s.Plane.MoveVariant(e.Delta)
			} else {
				s.Plane.Move(e.Delta)
			}
		}

	case PlanePaged:
		if s.Plane != nil {
			if s.Plane.Variant != nil {
				s.Plane.EndVariant(e.Delta > 0)
			} else {
				s.Plane.Page(e.Delta)
			}
		}

	case PlaneTop:
		if s.Plane != nil {
			if s.Plane.Variant != nil {
				s.Plane.EndVariant(false)
			} else {
				s.Plane.Top()
			}
		}

	case PlaneBottom:
		if s.Plane != nil {
			if s.Plane.Variant != nil {
				s.Plane.EndVariant(true)
			} else {
				s.Plane.Bottom()
			}
		}

	case PlaneFocusChanged:
		if s.Plane != nil {
			if e.Right {
				s.Plane.FocusModels()
			} else {
				s.Plane.FocusProviders()
			}
		}

	case PlaneFocusSet:
		if s.Plane != nil {
			s.Plane.setFocus(e.Focus)
		}

	case PlaneFocusMoved:
		if s.Plane != nil {
			if e.Delta < 0 {
				for i := 0; i < -e.Delta; i++ {
					s.Plane.PrevFocus()
				}
			} else {
				for i := 0; i < e.Delta; i++ {
					s.Plane.NextFocus()
				}
			}
		}

	case PlaneModeToggled:
		if s.Plane != nil {
			s.Plane.ToggleMode()
		}

	case PlaneFilterChanged:
		if s.Plane != nil {
			s.Plane.SetFilter(e.Text)
		}

	case PlaneTyped:
		if s.Plane != nil {
			s.Plane.InsertFilterText(e.Text)
		}

	case PlaneFilterBackspace:
		if s.Plane != nil {
			s.Plane.DeleteFilterBefore()
		}

	case PlaneFilterDelete:
		if s.Plane != nil {
			s.Plane.DeleteFilterAt()
		}

	case PlaneFilterCursorMoved:
		if s.Plane != nil {
			s.Plane.MoveFilterCursor(e.Delta)
		}

	case PlaneFilterCleared:
		if s.Plane != nil {
			s.Plane.ClearFilter()
		}

	case PlaneVariantOpened:
		if s.Plane != nil {
			if model, ok := s.Plane.ChosenModel(); ok {
				s.Plane.OpenVariant(model)
			}
		}

	case PlaneVariantMoved:
		if s.Plane != nil {
			s.Plane.MoveVariant(e.Delta)
		}

	case PlaneVariantClosed:
		if s.Plane != nil {
			s.Plane.CancelVariant()
		}

	case PlaneEffortCycled:
		if s.Plane != nil {
			s.Plane.MoveEffort(e.Delta)
		}

	case PlaneRoleCycled:
		if s.Plane != nil {
			s.Plane.NextRole(e.Delta)
		}

	case PlaneRoleStaged:
		if s.Plane != nil {
			s.Plane.StageRole()
		}

	case PlaneModelStaged:
		if s.Plane != nil {
			s.Plane.beginModelFirst(e.Model)
		}

	case PlaneRoleChosen:
		if s.Plane != nil {
			s.Plane.chooseRole()
		}

	case PlaneModelChosen:
		if s.Plane != nil {
			s.Plane.chooseModel()
		}

	case PlaneStageCancelled:
		if s.Plane != nil {
			s.Plane.CancelStage()
		}

	case PlaneVariantConfirmed:
		if s.Plane != nil {
			s.Plane.ConfirmVariant(e.Model)
		}

	case PlaneAssigned:
		if s.Plane != nil {
			s.Plane.ApplyAssignment(e.Result, e.Roles, e.Notice)
		}

	case PlaneAuthOpened:
		if s.Plane != nil {
			s.Plane.OpenAuth()
		}

	case PlaneAuthClosed:
		if s.Plane != nil {
			s.Plane.CloseAuth()
		}

	case PlaneAuthTyped:
		if s.Plane != nil {
			s.Plane.InsertAuthText(e.Text)
		}

	case PlaneAuthBackspace:
		if s.Plane != nil {
			s.Plane.DeleteAuthBefore()
		}

	case PlaneAuthDelete:
		if s.Plane != nil {
			s.Plane.DeleteAuthAt()
		}

	case PlaneAuthCursorMoved:
		if s.Plane != nil {
			s.Plane.MoveAuthCursor(e.Delta)
		}

	case PlaneAuthClearedToStart:
		if s.Plane != nil {
			s.Plane.ClearAuthToStart()
		}

	case PlaneAuthClearedToEnd:
		if s.Plane != nil {
			s.Plane.ClearAuthToEnd()
		}

	case PlaneAuthWordBack:
		if s.Plane != nil {
			s.Plane.DeleteAuthWordBack()
		}

	case PlaneAuthSaved:
		if s.Plane != nil {
			s.Plane.ApplyAuthSaved(e.Providers, e.Notice)
		}

	case PlaneAuthFailed:
		if s.Plane != nil {
			s.Plane.Notice = e.Notice
		}

	case PlaneRowsChanged:
		if s.Plane != nil {
			s.Plane.SetVisibleRows(e.Rows)
		}

	case ConnectKeyOpened:
		// A fresh buffer: the previous provider's key is never carried over.
		view := e.View
		s.ConnectKey = &view

	case ConnectKeyClosed:
		s.ConnectKey = nil

	case ConnectKeyChanged:
		if s.ConnectKey != nil {
			s.ConnectKey.Key = e.Key
		}

	case ConnectKeyValidating:
		if s.ConnectKey != nil {
			s.ConnectKey.Validating = true
			s.ConnectKey.Error = ""
		}

	case ConnectKeyFailed:
		if s.ConnectKey != nil {
			s.ConnectKey.Validating = false
			s.ConnectKey.Error = e.Error
		}

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
		if e.HasWorkspace {
			s.Status.Workspace = e.Workspace
			s.Status.WorkspaceAccent = e.WorkspaceAccent
			// The workspace moved, so a proposal naming the old one is stale.
			s.Transition = nil
		}
		if e.HasUser {
			s.Status.User = e.User
		}
		if e.HasGreeting {
			s.Status.Greeting = e.Greeting
		}
		if e.HasGit {
			s.Status.GitBranch = e.GitBranch
			s.Status.GitStatus = e.GitStatus
			s.Status.GitStaged = e.GitStaged
			s.Status.GitModified = e.GitModified
			s.Status.GitUntracked = e.GitUntracked
			s.Status.GitKnown = e.GitKnown
		}
		if e.HasReady {
			s.Status.Ready = e.Ready
		}

	case IdentityUpdated:
		s.Status.User = e.User
		s.Status.Greeting = e.Greeting
		s.Status.GitBranch = e.GitBranch
		s.Status.GitStatus = e.GitStatus
		s.Status.GitStaged = e.GitStaged
		s.Status.GitModified = e.GitModified
		s.Status.GitUntracked = e.GitUntracked
		s.Status.GitKnown = e.GitKnown
		if e.Ready != "" {
			s.Status.Ready = e.Ready
		}

	case QuitRequested:
		s.Quit = true
		s.QuitConfirm = nil
		s.Transition = nil

	case SupportTurn:
		// Legacy single-event form: the exchange lands in the overlay as a
		// finished turn. It never touches the normal transcript, history, or
		// session (I17). EntrySupport entries are no longer produced: the
		// panel is the surface, not the main viewport.
		if s.SupportPanel == nil {
			s.SupportSeq++
			s.SupportPanel = &SupportPanel{Status: SupportIdle, Seq: s.SupportSeq}
		}
		s.SupportPanel.Request = e.Question
		s.SupportPanel.Response = e.Answer
		s.SupportPanel.Status = SupportCompleted
		s.SupportPanel.Input = ""
		s.SupportPanel.Cursor = 0
		s.SupportPanel.Scroll = 0
		s.Support = true
		s.growScroll(countLines(e.Question) + countLines(e.Answer) + 2)

	case SupportOpened:
		if s.SupportPanel == nil {
			s.SupportPanel = &SupportPanel{Status: SupportIdle}
		}
		if s.SupportPanel.Status == SupportRunning {
			break
		}
		if e.Model != "" {
			s.SupportPanel.Model = e.Model
			s.SupportPanel.Effort = e.Effort
		}
		if s.SupportPanel.Status != SupportCompleted &&
			s.SupportPanel.Status != SupportCancelled &&
			s.SupportPanel.Status != SupportFailed {
			s.SupportPanel.Status = SupportIdle
		}
		s.SupportPanel.Scroll = 0
		s.Support = true

	case SupportInputChanged:
		if s.SupportPanel == nil || s.SupportPanel.Status == SupportRunning {
			break
		}
		s.SupportPanel.Input = e.Text
		s.SupportPanel.Cursor = clampCursor(e.Text, e.Cursor)

	case SupportStarted:
		seq := e.Seq
		if seq == 0 {
			s.SupportSeq++
			seq = s.SupportSeq
		} else if seq > s.SupportSeq {
			s.SupportSeq = seq
		}
		if s.SupportPanel == nil {
			s.SupportPanel = &SupportPanel{}
		}
		s.SupportPanel.Request = e.Request
		s.SupportPanel.Response = ""
		s.SupportPanel.Status = SupportRunning
		s.SupportPanel.Seq = seq
		s.SupportPanel.Model = e.Model
		s.SupportPanel.Effort = e.Effort
		s.SupportPanel.Error = ""
		s.SupportPanel.Input = ""
		s.SupportPanel.Cursor = 0
		s.SupportPanel.Scroll = 0
		s.Support = true
		s.growScroll(countLines(e.Request) + 1)

	case SupportDelta:
		panel := s.SupportPanel
		if panel == nil || e.Seq != panel.Seq || panel.Status != SupportRunning {
			break
		}
		panel.Response += e.Text
		s.growScroll(strings.Count(e.Text, "\n"))

	case SupportDone:
		panel := s.SupportPanel
		if panel == nil || e.Seq != panel.Seq || panel.Status != SupportRunning {
			break
		}
		if e.Text != "" {
			panel.Response += e.Text
		}
		panel.Status = SupportCompleted

	case SupportFailedEvent:
		panel := s.SupportPanel
		if panel == nil || e.Seq != panel.Seq || panel.Status != SupportRunning {
			break
		}
		panel.Status = SupportFailed
		panel.Error = e.Error

	case SupportCancelledEvent:
		panel := s.SupportPanel
		if panel == nil || e.Seq != panel.Seq {
			break
		}
		// Idempotent: the first cancellation wins; a terminal panel
		// stays terminal without a second line.
		if panel.Status != SupportRunning {
			break
		}
		panel.Status = SupportCancelled
		panel.Error = ""
		if e.Text != "" && e.Text != "cancelled" {
			panel.Response = e.Text
		}

	case SupportClosed:
		// Closing discards the ephemeral panel outright: the surface must
		// return the user to the previous interaction, not leave a transcript
		// of a conversation that never happened.
		kept := s.Entries[:0]
		for _, entry := range s.Entries {
			if entry.Kind != EntrySupport {
				kept = append(kept, entry)
			}
		}
		s.Entries = kept
		s.SupportPanel = nil
		s.Support = false

	case SupportScrolled:
		// The response viewport follows the transcript convention: an
		// offset above the live edge, floored at zero. Overshoot parks
		// at the oldest line in the view, which clamps to the content
		// without mutating the stored offset.
		if s.SupportPanel == nil {
			break
		}
		s.SupportPanel.Scroll += e.Delta
		if s.SupportPanel.Scroll < 0 {
			s.SupportPanel.Scroll = 0
		}

	case SupportScrollToBottom:
		if s.SupportPanel == nil {
			break
		}
		s.SupportPanel.Scroll = 0

	case Cleared:
		// /clear drops the transcript so the next turn starts from nothing.
		// The status line is preserved: the model and budget still apply.
		// Queued prompts go too — they were typed against the context that
		// was just dropped. Recallable history stays: it is memory, not
		// context. The viewport rejoins the live edge: a stale scroll offset
		// would pin the next turn's output to the top of an empty
		// transcript instead of following it.
		s.Entries = nil
		s.dirtyReset = true
		s.dirty = s.dirty[:0]
		s.runningTools = 0
		s.assistantText = false
		s.Pending = nil
		s.QuitConfirm = nil
		s.Transition = nil
		s.Permissions = nil
		s.StatusModal = nil
		s.UsageModal = nil
		s.SessionModal = nil
		s.HelpModal = nil
		s.ConnectModal = nil
		s.ConnectKey = nil
		s.Queued = nil
		s.Scroll = 0
		s.scrollIntent = 0
		s.ScrollSeq++
		s.Follow = true
		s.Selection = nil
		// Clearing drops the transient confirmation with the transcript it
		// confirmed; a stale expiry after this is a no-op via the generation.
		s.CopyNotice = ""
		s.closedAssistant = false
		s.recapEmitted = nil
		s.OutputTokens = 0
		s.resetSessionMetrics()
	}
}

// resetSessionMetrics returns progressive disclosure to its clean state: no
// active session and no token or duration telemetry. The helper bar falls
// back to the shortcut hints rather than reporting zeros.
func (s *State) resetSessionMetrics() {
	s.SessionActive = false
	s.SessionIn = 0
	s.SessionOut = 0
	s.SessionElapsed = 0
}

// currentOutputTokens estimates tokens for the trailing open assistant
// entry. It recounts the whole entry rather than summing per-chunk estimates
// so chunk-boundary rounding cannot inflate the total. Zero when no entry is
// open: unknown stays absent rather than fabricated.
func (s *State) currentOutputTokens() int {
	idx := s.openAssistant()
	if idx < 0 {
		return s.OutputTokens
	}
	return tokens.Count(s.Entries[idx].AssistantText, tokens.ModeEstimate)
}

// growScroll keeps a scrolled-up viewport pinned while new output lands.
//
// In follow mode the view tracks the live edge, so there is nothing to do.
// Scrolled up, the offset from the bottom must grow by the lines just added,
// or the viewport would drift downward with every event. This is only the
// compatibility mirror: TranscriptLayout re-resolves geometry from the visual
// rows, and the viewport tracker anchors on a block, so wrapped output cannot
// make the rendered position drift even when this estimate is coarse.
func (s *State) growScroll(lines int) {
	if !s.Follow {
		s.Scroll += lines
	}
}

// pushEntry appends a block and gives it a stable identity.
func (s *State) pushEntry(entry Entry) {
	s.Entries = append(s.Entries, entry)
	s.assignIdentity(len(s.Entries) - 1)
}

// assignIdentity allocates the block ID and dirty token for an entry that just
// entered the transcript.
func (s *State) assignIdentity(index int) {
	if index < 0 || index >= len(s.Entries) {
		return
	}
	s.nextBlockID++
	s.nextEntryRev++
	s.Entries[index].ID = BlockID(fmt.Sprintf("block-%d", s.nextBlockID))
	s.Entries[index].rev = s.nextEntryRev
	s.dirty = append(s.dirty, index)
}

// bumpEntry marks one block's projection stale after an in-place update.
func (s *State) bumpEntry(index int) {
	if index < 0 || index >= len(s.Entries) {
		return
	}
	s.nextEntryRev++
	s.Entries[index].rev = s.nextEntryRev
	s.dirty = append(s.dirty, index)
}

// clearDirty consumes the layout invalidation hints. The UI goroutine calls it
// after a successful Refresh; a second cache (a one-shot Render) rebuilds from
// scratch and never relies on these hints.
func (s *State) clearDirty() {
	s.dirty = s.dirty[:0]
	s.dirtyReset = false
}

// replaceEntries installs a whole transcript, as a resume does. IDs are
// reassigned because a persisted transcript has no view identity to preserve:
// the old selection and viewport refer to blocks that no longer exist.
func (s *State) replaceEntries(entries []Entry) {
	s.Entries = append([]Entry(nil), entries...)
	s.dirty = s.dirty[:0]
	s.dirtyReset = true
	s.runningTools = 0
	s.assistantText = false
	for i := range s.Entries {
		s.nextBlockID++
		s.nextEntryRev++
		s.Entries[i].ID = BlockID(fmt.Sprintf("block-%d", s.nextBlockID))
		s.Entries[i].rev = s.nextEntryRev
		if s.Entries[i].Kind == EntryTool && s.Entries[i].Tool != nil && s.Entries[i].Tool.Status == ToolRunning {
			s.runningTools++
		}
	}
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == EntryAssistant {
			s.assistantText = s.Entries[i].AssistantText != ""
			break
		}
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

// setComposer is the single write path for the composed text and cursor.
// Editing over a recalled entry leaves history and abandons the stashed draft
// (there is no way back to it once it is edited), and any change prunes paste
// chips whose labels no longer appear so payloads never outlive their pills.
func (s *State) setComposer(text string, cursor int) {
	if s.Input != text {
		// New text re-opens discovery and parks on the top match.
		s.PaletteCursor = 0
		s.PaletteHide = false
	}
	s.Input = text
	s.Cursor = clampCursor(text, cursor)
	if s.HistCursor >= 0 {
		// Editing a recalled entry returns to live input: the stashed draft is
		// abandoned and can never be restored, so it must not keep a paste
		// payload alive.
		s.HistCursor = -1
		s.HistDraft = ""
	}
	s.prunePastes()
	s.syncComposerScroll()
}

// syncComposerScroll advances the composer's visible window to the cursor,
// folding the input the way the full-screen renderer does. The live renderer
// clamps to its own width at paint time, so a width mismatch can only fail to
// advance the stored window — it can never hide the cursor.
func (s *State) syncComposerScroll() {
	layout := ComputeLayout(*s)
	clean, cleanCursor := sanitizeWithCursor(s.Input, s.Cursor)
	rows, cursorRow, _, have := layoutComposer(clean, cleanCursor, composerTextWidthFor(*s, layout))
	if !have {
		cursorRow = 0
	}
	s.ComposerScroll = composerScrollOffset(s.ComposerScroll, cursorRow, len(rows), maxComposerRows)
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
			s.syncComposerScroll()
			return
		}
	}
	s.Input = s.Submitted[s.HistCursor]
	s.Cursor = len([]rune(s.Input))
	s.syncComposerScroll()
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

// appendTaskRecap adds the completion footer at the tail of the transcript.
//
// The footer is a lifecycle event, not part of the task block: it is emitted
// only when the task is COMPLETED and nothing is still streaming. That is what
// puts it below the answer the user just read instead of above it in the task
// header. One entry is emitted per task identity; repeated projections or a
// second AssistantDone cannot duplicate it.
func (s *State) appendTaskRecap() {
	if !s.streamSettled() {
		return
	}
	task := s.tailTask()
	if task == nil {
		return
	}
	text := taskRecapText(task)
	if text == "" {
		return
	}
	key := task.ID
	if key == "" {
		key = text
	}
	if s.recapEmitted[key] {
		return
	}
	if s.recapEmitted == nil {
		s.recapEmitted = make(map[string]bool)
	}
	s.recapEmitted[key] = true
	s.pushEntry(Entry{Kind: EntryRecap, Recap: text})
	s.growScroll(1)
}

// streamSettled reports whether the active turn has finished emitting: no
// busy turn, no open assistant block, and no tool still running.
func (s *State) streamSettled() bool {
	return !s.Status.Busy && s.openAssistant() < 0 && !hasRunningTool(*s)
}

// tailTask returns the most recent task projection, or nil when the transcript
// has none.
func (s *State) tailTask() *TaskProjection {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == EntryTask && s.Entries[i].Task != nil {
			return s.Entries[i].Task
		}
	}
	return nil
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
		s.assistantText = true
		s.bumpEntry(index)
		return
	}
	s.Entries = append(s.Entries, Entry{Kind: EntryAssistant, AssistantText: text})
	s.assistantText = true
	s.assignIdentity(len(s.Entries) - 1)
	s.closedAssistant = false
}

// tool finds a tool entry by call id.
func (s *State) tool(id string) *ToolEntry {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		entry := &s.Entries[i]
		if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.ID == id {
			return entry.Tool
		}
	}
	return nil
}

// shell finds a "!" escape block by run sequence. A zero seq matches the most
// recent block, for callers that do not track identity.
func (s *State) shell(seq int) *ShellEntry {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		entry := &s.Entries[i]
		if entry.Kind != EntryShell || entry.Shell == nil {
			continue
		}
		if seq == 0 || entry.Shell.Seq == seq {
			return entry.Shell
		}
	}
	return nil
}

// runningShell returns the in-flight escape block, or nil. There is never
// more than one: the program refuses a second while the first runs.
func (s *State) runningShell() *ShellEntry {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		entry := &s.Entries[i]
		if entry.Kind == EntryShell && entry.Shell != nil && entry.Shell.Status == ShellRunning {
			return entry.Shell
		}
	}
	return nil
}

// LatestToolID names the most recent tool block, or "" when there is none.
//
// Ctrl+O operates on this block: with no cursor model in the transcript,
// the latest activity is the smallest deterministic rule consistent with
// the interaction model — the user just watched it run.
func (s *State) LatestToolID() string {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if entry := &s.Entries[i]; entry.Kind == EntryTool && entry.Tool != nil {
			return entry.Tool.ID
		}
	}
	return ""
}

// toolIndex returns the transcript index of a tool block, or -1.
func (s *State) toolIndex(id string) int {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		entry := &s.Entries[i]
		if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.ID == id {
			return i
		}
	}
	return -1
}

// shellIndex returns the transcript index of an escape block, or -1. A zero
// seq matches the most recent block, mirroring shell().
func (s *State) shellIndex(seq int) int {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		entry := &s.Entries[i]
		if entry.Kind != EntryShell || entry.Shell == nil {
			continue
		}
		if seq == 0 || entry.Shell.Seq == seq {
			return i
		}
	}
	return -1
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
type UserSubmitted struct {
	Text string
	// Seq identifies the turn. Zero means legacy callers without an id.
	Seq int
	// At is when the turn actually began, the authoritative start for the
	// model-phase activity line. Zero means unknown, and unknown shows no
	// elapsed time rather than a fabricated one.
	At time.Time
}

func (UserSubmitted) eventKind() string { return "user_submitted" }

// AssistantDelta is streamed model text.
type AssistantDelta struct {
	Text string
	// Seq identifies the turn. Zero accepts any turn for legacy callers.
	Seq int
}

func (AssistantDelta) eventKind() string { return "assistant_delta" }

// AssistantDone marks the end of a turn.
type AssistantDone struct {
	// Seq identifies the turn. Zero accepts any turn for legacy callers.
	Seq int
}

func (AssistantDone) eventKind() string { return "assistant_done" }

// TurnCancelled is the single semantic cancellation transition for a turn.
// The reducer makes it idempotent: the first one wins, later duplicates and
// stale turns are dropped, so the renderer naturally receives one terminal
// cancellation state and the spinner projection converges to idle.
type TurnCancelled struct {
	// Seq identifies the turn. Zero means the current turn.
	Seq int
	// Text is the user-visible cancellation line. Empty renders "cancelled".
	Text string
}

func (TurnCancelled) eventKind() string { return "turn_cancelled" }

// ToolStarted announces a call beginning.
//
// Summary describes what this particular call does, derived from its
// arguments at publication time — never inferred from assistant prose.
type ToolStarted struct {
	ID      string
	Name    string
	Summary string
	At      time.Time
	// Seq identifies the turn. Zero accepts any turn for legacy callers.
	Seq int
}

func (ToolStarted) eventKind() string { return "tool_started" }

// ToolPreview updates a running call's summary.
type ToolPreview struct {
	ID      string
	Summary string
	// Seq identifies the turn. Zero accepts any turn for legacy callers.
	Seq int
}

func (ToolPreview) eventKind() string { return "tool_preview" }

// ToolFinished records a call's outcome.
//
// Summary is the compact, deterministic description of the outcome (line
// count, first error line), shown even when the full text below is bounded
// or truncated. IsError keeps success and failure distinct without
// re-deriving it from the text.
type ToolFinished struct {
	ID        string
	Text      string
	Summary   string
	IsError   bool
	Truncated bool
	At        time.Time
	// Seq identifies the turn. Zero accepts any turn for legacy callers.
	Seq int
}

func (ToolFinished) eventKind() string { return "tool_finished" }

// ToolToggled flips one tool block between its collapsed and expanded
// projections. It carries only the call id: the target is the same
// semantic block, and the toggle never touches execution state.
type ToolToggled struct {
	// ID is the provider's tool call id to expand or collapse.
	ID string
}

func (ToolToggled) eventKind() string { return "tool_toggled" }

// ShellStarted announces a "!" escape command beginning.
type ShellStarted struct {
	// Command is the raw command as typed, without the leading "!".
	Command string
	// At is when execution began.
	At time.Time
	// Seq identifies the run so a late chunk from an aborted command is
	// dropped rather than appended to a newer block.
	Seq int
}

func (ShellStarted) eventKind() string { return "shell_started" }

// ShellDelta streams a chunk of combined command output.
type ShellDelta struct {
	// Text is the chunk appended to the running block.
	Text string
	// Seq identifies the run.
	Seq int
}

func (ShellDelta) eventKind() string { return "shell_delta" }

// ShellFinished records an escape command's outcome.
type ShellFinished struct {
	// ExitCode is the process exit status; -1 when unknown or aborted.
	ExitCode int
	// Duration is the engine's measured execution time.
	Duration time.Duration
	// Err carries a start or cancellation failure; empty on a normal exit.
	Err string
	// Truncated reports that the output hit the runner's cap.
	Truncated bool
	// At is when the command ended.
	At time.Time
	// Seq identifies the run.
	Seq int
}

func (ShellFinished) eventKind() string { return "shell_finished" }

// Notice adds a system line.
type Notice struct{ Text string }

func (Notice) eventKind() string { return "notice" }

// UpdateFound reports a newer release discovered in the background. It is
// chrome-only: it updates the footer hint and never enters the transcript.
type UpdateFound struct{ Version string }

func (UpdateFound) eventKind() string { return "update_found" }

// ContextUpdated sizes the budget display.
type ContextUpdated struct {
	Used   int
	Window int
}

func (ContextUpdated) eventKind() string { return "context_updated" }

// SessionMetricsUpdated reports the cumulative per-session token telemetry and
// elapsed duration from the runtime's ledger. The runtime owns the clock and
// the usage accounting; the state stores what it measured rather than reading
// the wall clock at render time, so Render stays pure. A zero field means
// unknown, and the helper bar hides an unknown counter instead of showing 0.
type SessionMetricsUpdated struct {
	// Input and Output are cumulative tokens across the session.
	Input  int
	Output int
	// Elapsed is the measured session duration.
	Elapsed time.Duration
}

func (SessionMetricsUpdated) eventKind() string { return "session_metrics_updated" }

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
	// Prompt, when non-empty, is a shell-specific label shown above the call.
	// It is used by the "!" shell escape, whose confirmation is not a tool
	// call.
	Prompt          string
	Reason          string
	Summary         string
	MutationPreview agent.MutationPreview
	// Mutating marks a call that changes state; the dialog accents a
	// destructive boundary differently from a read one.
	Mutating bool
	Choices  []string
	// Proposal, when non-nil, turns the prompt into a proposal decision. The
	// question, reason, and choices come from the request-derived proposal;
	// the dialog shell remains the one permission surface.
	Proposal *workspace.Proposal
	// Commit carries the /commit proposal payload when the prompt is a commit
	// decision.
	Commit *CommitProposalView
}

func (ApprovalAsked) eventKind() string { return "approval_asked" }

// ApprovalMoved shifts the highlighted choice.
type ApprovalMoved struct{ Delta int }

func (ApprovalMoved) eventKind() string { return "approval_moved" }

// ApprovalAnswered closes the prompt.
type ApprovalAnswered struct{}

func (ApprovalAnswered) eventKind() string { return "approval_answered" }

// quitConfirmChoices are the answers offered by the /q quit-confirmation
// dialog. The last is the safest so the default cursor lands on it: quitting
// must be a deliberate move off the resting place, never the resting place.
var quitConfirmChoices = []string{"Yes", "No"}

// QuitConfirmOpened asks whether the session should really exit. It is opened
// by /q; confirming quits, dismissing stays.
type QuitConfirmOpened struct{}

func (QuitConfirmOpened) eventKind() string { return "quit_confirm_opened" }

// QuitConfirmMoved shifts the quit-confirmation highlight. Positive goes down.
type QuitConfirmMoved struct{ Delta int }

func (QuitConfirmMoved) eventKind() string { return "quit_confirm_moved" }

// QuitConfirmClosed dismisses the quit-confirmation prompt without exiting.
type QuitConfirmClosed struct{}

func (QuitConfirmClosed) eventKind() string { return "quit_confirm_closed" }

// TransitionProposed opens the workspace-transition decision surface for a
// request the classifier placed in a lower-authority workspace. Opening it
// changes no authority: that waits for the user's answer (I2, I4-I6).
type TransitionProposed struct {
	// Text is the turn awaiting the decision.
	Text string
	// Current and Proposed are workspace labels, e.g. "BUILD" and "ASK".
	Current  string
	Proposed string
	// CurrentAccent and ProposedAccent are the semantics accents from the
	// producer (workspace.Style()), never invented here.
	CurrentAccent  string
	ProposedAccent string
	// Intent is the classified request, e.g. "READ".
	Intent string
	// Strength is the classifier's evidence level, e.g. "strong". It is a
	// level, never a percentage (I12).
	Strength string
	// Question is the heading, e.g. "Switch to ASK?".
	Question string
	// Reason is the one-line explanation under the heading.
	Reason string
}

func (TransitionProposed) eventKind() string { return "transition_proposed" }

// TransitionMoved shifts the transition highlight. Positive moves toward the
// proposed workspace.
type TransitionMoved struct{ Delta int }

func (TransitionMoved) eventKind() string { return "transition_moved" }

// TransitionResolved clears the transition prompt. It is published exactly
// once per proposal, on accept, reject, or cancel, so the surface can never
// outlive the decision that consumed it.
type TransitionResolved struct{}

func (TransitionResolved) eventKind() string { return "transition_resolved" }

// PermissionsOpened opens the authority selector with the cursor parked on the
// authority in force. Request is non-empty only when the selector is resolving
// an ambiguous user intent; that request runs only after an explicit choice.
type PermissionsOpened struct {
	Title   string
	Reason  string
	Request string
}

func (PermissionsOpened) eventKind() string { return "permissions_opened" }

// PermissionsMoved shifts the authority highlight. Positive goes down; the
// registry wraps at both ends.
type PermissionsMoved struct{ Delta int }

func (PermissionsMoved) eventKind() string { return "permissions_moved" }

// PermissionsClosed dismisses the selector without changing authority.
type PermissionsClosed struct{}

func (PermissionsClosed) eventKind() string { return "permissions_closed" }

// StatusModalOpened shows the /status card with a snapshot of the facts to
// report. The view is copied, so the card is a stable read of the moment it
// opened.
type StatusModalOpened struct {
	// View is the snapshot the card renders.
	View StatusView
}

func (StatusModalOpened) eventKind() string { return "status_modal_opened" }

// StatusModalClosed dismisses the status card.
type StatusModalClosed struct{}

func (StatusModalClosed) eventKind() string { return "status_modal_closed" }

// UsageModalOpened shows the /usage inspector with a snapshot of the ledger
// and provider roster. The view is copied, so the card is a stable read of the
// moment it opened.
type UsageModalOpened struct {
	// View is the snapshot the card renders.
	View UsageView
}

func (UsageModalOpened) eventKind() string { return "usage_modal_opened" }

// UsageModalClosed dismisses the usage inspector.
type UsageModalClosed struct{}

func (UsageModalClosed) eventKind() string { return "usage_modal_closed" }

// SessionManagerOpened shows the Session Manager with a roster snapshot. The
// view is copied, so the roster is a stable read of the moment it opened.
type SessionManagerOpened struct {
	// View is the roster snapshot the manager renders.
	View SessionManagerState
}

func (SessionManagerOpened) eventKind() string { return "session_manager_opened" }

// SessionManagerClosed dismisses the Session Manager without acting.
type SessionManagerClosed struct{}

func (SessionManagerClosed) eventKind() string { return "session_manager_closed" }

// SessionManagerMoved shifts the roster highlight. Positive goes down; the
// list wraps at both ends.
type SessionManagerMoved struct{ Delta int }

func (SessionManagerMoved) eventKind() string { return "session_manager_moved" }

// SessionManagerPaged shifts the roster highlight by approximately one
// viewport without wrapping.
type SessionManagerPaged struct{ Delta int }

func (SessionManagerPaged) eventKind() string { return "session_manager_paged" }

// SessionManagerTop parks the highlight on the first session.
type SessionManagerTop struct{}

func (SessionManagerTop) eventKind() string { return "session_manager_top" }

// SessionManagerBottom parks the highlight on the last session.
type SessionManagerBottom struct{}

func (SessionManagerBottom) eventKind() string { return "session_manager_bottom" }

// SessionManagerReloaded replaces the roster after a write action, preserving
// the highlight by id when the selected session survives.
type SessionManagerReloaded struct {
	// Items is the fresh roster.
	Items []SessionItem
	// Notice reports the action that caused the reload.
	Notice string
}

func (SessionManagerReloaded) eventKind() string { return "session_manager_reloaded" }

// SessionManagerRenameStarted enters the inline rename editor, seeded with the
// highlighted session's current title.
type SessionManagerRenameStarted struct{}

func (SessionManagerRenameStarted) eventKind() string { return "session_manager_rename_started" }

// SessionManagerRenameChanged replaces the rename buffer.
type SessionManagerRenameChanged struct{ Text string }

func (SessionManagerRenameChanged) eventKind() string { return "session_manager_rename_changed" }

// SessionManagerConfirmAsked opens a destructive-action confirmation for one
// session.
type SessionManagerConfirmAsked struct {
	// ID is the session the action targets.
	ID int64
	// Action names the action and its consequence in words.
	Action string
}

func (SessionManagerConfirmAsked) eventKind() string { return "session_manager_confirm_asked" }

// SessionManagerModeCancelled returns the modal to browsing, discarding any
// in-progress rename or confirmation.
type SessionManagerModeCancelled struct{}

func (SessionManagerModeCancelled) eventKind() string { return "session_manager_mode_cancelled" }

// SessionManagerNoticed records an action outcome without changing the roster.
type SessionManagerNoticed struct{ Text string }

func (SessionManagerNoticed) eventKind() string { return "session_manager_noticed" }

// HelpCenterOpened shows the Command & Help Center with a registry snapshot.
// The view is copied, so the roster is a stable read of the moment it opened.
type HelpCenterOpened struct {
	// View is the command snapshot the modal renders.
	View HelpCenterState
}

func (HelpCenterOpened) eventKind() string { return "help_center_opened" }

// HelpCenterClosed dismisses the Command & Help Center without acting.
type HelpCenterClosed struct{}

func (HelpCenterClosed) eventKind() string { return "help_center_closed" }

// HelpCenterMoved shifts the command highlight. Positive goes down; the list
// wraps at both ends.
type HelpCenterMoved struct{ Delta int }

func (HelpCenterMoved) eventKind() string { return "help_center_moved" }

// HelpCenterViewSwitched cycles the active tab by delta, wrapping both ways.
// Each tab keeps its own query and highlight, so the switch is non-destructive.
type HelpCenterViewSwitched struct{ Delta int }

func (HelpCenterViewSwitched) eventKind() string { return "help_center_view_switched" }

// HelpCenterPaged shifts the command highlight by approximately one viewport
// without wrapping.
type HelpCenterPaged struct{ Delta int }

func (HelpCenterPaged) eventKind() string { return "help_center_paged" }

// HelpCenterTop parks the highlight on the first command.
type HelpCenterTop struct{}

func (HelpCenterTop) eventKind() string { return "help_center_top" }

// HelpCenterBottom parks the highlight on the last command.
type HelpCenterBottom struct{}

func (HelpCenterBottom) eventKind() string { return "help_center_bottom" }

// HelpCenterTyped inserts text into the search box at the cursor.
type HelpCenterTyped struct{ Text string }

func (HelpCenterTyped) eventKind() string { return "help_center_typed" }

// HelpCenterBackspaced deletes the rune before the search cursor.
type HelpCenterBackspaced struct{}

func (HelpCenterBackspaced) eventKind() string { return "help_center_backspaced" }

// HelpCenterDeleted deletes the rune under the search cursor.
type HelpCenterDeleted struct{}

func (HelpCenterDeleted) eventKind() string { return "help_center_deleted" }

// HelpCenterFilterCursorMoved shifts the search cursor. It never changes the
// highlight.
type HelpCenterFilterCursorMoved struct{ Delta int }

func (HelpCenterFilterCursorMoved) eventKind() string { return "help_center_filter_cursor_moved" }

// HelpCenterFilterCleared empties the search box and restores the highlight to
// the top of the full roster.
type HelpCenterFilterCleared struct{}

func (HelpCenterFilterCleared) eventKind() string { return "help_center_filter_cleared" }

// ConnectModalOpened shows the /connect Provider Manager with a roster
// snapshot. The view is copied, so the roster is a stable read of the moment it
// opened.
type ConnectModalOpened struct {
	// View is the roster snapshot the manager renders.
	View ConnectView
}

func (ConnectModalOpened) eventKind() string { return "connect_modal_opened" }

// ConnectModalClosed dismisses the Provider Manager without acting.
type ConnectModalClosed struct{}

func (ConnectModalClosed) eventKind() string { return "connect_modal_closed" }

// ConnectModalMoved shifts the roster highlight. Positive goes down; the list
// wraps at both ends.
type ConnectModalMoved struct{ Delta int }

func (ConnectModalMoved) eventKind() string { return "connect_modal_moved" }

// ConnectModalPaged shifts the roster highlight by approximately one viewport
// without wrapping.
type ConnectModalPaged struct{ Delta int }

func (ConnectModalPaged) eventKind() string { return "connect_modal_paged" }

// ConnectModalTop parks the highlight on the first provider.
type ConnectModalTop struct{}

func (ConnectModalTop) eventKind() string { return "connect_modal_top" }

// ConnectModalBottom parks the highlight on the last provider.
type ConnectModalBottom struct{}

func (ConnectModalBottom) eventKind() string { return "connect_modal_bottom" }

// DiffOpened installs the interactive diff inspector as the active overlay.
// The model owns its own repository reads; the program only routes keys and
// paints its View while it is set.
type DiffOpened struct {
	// Model is the inspector to show. A nil model is ignored by the overlay
	// render, so a caller must always pass a constructed one.
	Model *diffview.Model
}

func (DiffOpened) eventKind() string { return "diff_opened" }

// DiffClosed dismisses the inspector and returns the screen to the transcript.
type DiffClosed struct{}

func (DiffClosed) eventKind() string { return "diff_closed" }

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
// gets a name, which is better than a blank line. External capabilities carry
// an explicit "external" marker so network activity never reads as local
// execution.
func ToolSummaryForCall(name string, args map[string]any) string {
	summary := toolSummaryBody(name, args)
	if IsExternalTool(name) {
		return "external " + summary
	}
	return summary
}

// IsExternalTool reports whether a tool talks to an external actor.
//
// It is presentation only: authority is still decided by the security gate,
// never by this classification.
func IsExternalTool(name string) bool {
	switch name {
	case "search", "mcp_list", "mcp_read", "mcp_call", "browser", "web_fetch":
		return true
	}
	return false
}

// toolSummaryBody renders the tool-specific part of a call summary.
func toolSummaryBody(name string, args map[string]any) string {
	switch name {
	case "search":
		if query, ok := args["query"].(string); ok && query != "" {
			return fmt.Sprintf("search %s", oneLine(query))
		}
	case "mcp_list":
		if server, ok := args["server"].(string); ok && server != "" {
			return fmt.Sprintf("mcp_list %s", oneLine(server))
		}
		return "mcp_list"
	case "mcp_read", "mcp_call":
		server, _ := args["server"].(string)
		target, _ := args["tool"].(string)
		if target == "" {
			target, _ = args["resource"].(string)
		}
		if server != "" || target != "" {
			return fmt.Sprintf("%s %s %s", name, oneLine(server), oneLine(target))
		}
	case "browser":
		operation, _ := args["operation"].(string)
		if operation == "" {
			operation = "browser"
		}
		if rawURL, ok := args["url"].(string); ok && rawURL != "" {
			return fmt.Sprintf("browser %s %s", operation, oneLine(rawURL))
		}
		if index, ok := args["index"].(float64); ok {
			return fmt.Sprintf("browser %s #%d", operation, int(index))
		}
		return "browser " + operation
	}
	for _, key := range []string{"path", "command", "pattern", "url", "file"} {
		if value, ok := args[key]; ok {
			if text, ok := value.(string); ok && text != "" {
				return fmt.Sprintf("%s %s", name, oneLine(text))
			}
		}
	}
	return name
}

// CompactResultSummary derives the one-line outcome description for a
// finished tool call from its real output.
//
// It is deliberately generic: per-tool phrasing ("PASS — 27 tests") would
// require the renderer to understand every tool, which is how renderers
// become execution detectors. Line counts and the first error line are
// always honest. Empty and single-line successes need no summary — the
// badge plus the bounded body already says it.
func CompactResultSummary(text string, isError bool) string {
	trimmed := strings.TrimRight(text, "\n")
	if isError {
		first := trimmed
		if i := strings.Index(first, "\n"); i >= 0 {
			first = first[:i]
		}
		first = oneLine(first)
		if first == "" {
			return "error"
		}
		return "error: " + first
	}
	if trimmed == "" || !strings.Contains(trimmed, "\n") {
		return ""
	}
	lines := strings.Count(trimmed, "\n") + 1
	return fmt.Sprintf("%d lines", lines)
}

// oneLine collapses whitespace and truncates a value for a summary line.
//
// The cut lands on a rune boundary: a byte cut inside a multi-byte rune
// would show as � in the approval dialog this feeds.
func oneLine(text string) string {
	const max = 60
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(collapsed[cut]) {
			cut--
		}
		return collapsed[:cut] + "…"
	}
	return collapsed
}

// assistantChunkRunes bounds one AssistantDelta event so a long turn
// reveals chunk-by-chunk across successive renders instead of landing as
// a single block. Small turns stay a single event: chunking is
// progressive disclosure, never pacing — no timers, no blocked loop.
const assistantChunkRunes = 1024

// splitAssistantChunks slices text into rune-bounded chunks for
// sequential publishing. It returns nil for empty text, and the
// concatenation of the chunks always equals the input.
func splitAssistantChunks(text string) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	var out []string
	for len(runes) > 0 {
		n := assistantChunkRunes
		if n > len(runes) {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

// TruncateForDisplay cuts a long outcome so one tool cannot flood the view.
//
// The cut lands on a rune boundary for the same reason: the outcome is
// displayed (after sanitizeBlock repairs it to �), and keeping a whole
// character beats displaying a replacement glyph for half of one.
func TruncateForDisplay(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n… [truncated]", true
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

// ParseSupport reports whether a line invokes the ephemeral support surface
// and returns its question.
//
// $sup is a capability of ASK, not a workspace and not a slash command (§10.1,
// I6): it is recognised separately so it can be routed to an isolated
// conversation that never touches the main context. A bare "$sup" carries an
// empty question, which the caller treats as "close the surface".
func ParseSupport(line string) (question string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed != "$sup" && !strings.HasPrefix(trimmed, "$sup ") && !strings.HasPrefix(trimmed, "$sup\t") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, "$sup")), true
}

// CommandSubcommand documents one argument form of a command.
//
// It is help text only: the parent command's handler owns dispatch, so a
// subcommand row cannot invent a second routing path.
type CommandSubcommand struct {
	// Usage is the argument shape without the command word, e.g.
	// "inspect <server>".
	Usage string
	// Description is one line of help.
	Description string
}

// CommandSpec describes one command for the help listing.
type CommandSpec struct {
	// Name is the command word.
	Name string
	// Usage shows the arguments, e.g. "model <provider/id>".
	Usage string
	// Description is one line of help.
	Description string
	// Category groups the slash menu: WORKSPACE or CONTROL.
	Category string
	// Hidden keeps a command out of the listing, for aliases.
	Hidden bool
	// Subcommands documents argument forms that deserve their own help row.
	// The registry remains the single source for runtime help and generated
	// reference output.
	Subcommands []CommandSubcommand
}

// Slash command categories. Workspace commands change execution authority;
// control commands drive the UI. The menu renders them as separate groups,
// never one undifferentiated list.
const (
	// CommandGroupWorkspace holds /ask, /fuzz, /build.
	CommandGroupWorkspace = "WORKSPACE"
	// CommandGroupControl holds /model, /help and the rest.
	CommandGroupControl = "CONTROL"
)

// Commands is the registry.
//
// It lives beside the state machine rather than in the UI so the help text and
// the dispatch table cannot drift apart.
var Commands = []CommandSpec{
	{Name: "ask", Usage: "ask", Description: "Ask / read-only", Category: CommandGroupWorkspace},
	{Name: "fuzz", Usage: "fuzz", Description: "Explore / investigate", Category: CommandGroupWorkspace},
	{Name: "build", Usage: "build", Description: "Build / modify", Category: CommandGroupWorkspace},
	{Name: "new", Usage: "new [name]", Description: "start a new session", Category: CommandGroupControl},
	{Name: "help", Usage: "help", Description: "open the command & help center", Category: CommandGroupControl},
	{Name: "commands", Usage: "commands", Description: "open the command & help center", Category: CommandGroupControl},
	{Name: "clear", Usage: "clear", Description: "clear the transcript and start a fresh context", Category: CommandGroupControl},
	{Name: "model", Usage: "model [provider/id]", Description: "browse or switch the model", Category: CommandGroupControl},
	{Name: "models", Usage: "models", Description: "browse every known model", Hidden: true, Category: CommandGroupControl},
	{Name: "permissions", Usage: "permissions", Description: "choose the execution authority", Category: CommandGroupControl},
	{Name: "mode", Usage: "mode", Description: "choose the execution authority", Hidden: true, Category: CommandGroupControl},
	{Name: "sessions", Usage: "sessions", Description: "manage saved sessions: resume, new, rename, archive, delete", Category: CommandGroupControl},
	{Name: "resume", Usage: "resume", Description: "browse and resume saved sessions", Hidden: true, Category: CommandGroupControl},
	{Name: "jobs", Usage: "jobs", Description: "inspect background jobs", Category: CommandGroupControl},
	{Name: "task", Usage: "task [status|list|show|resume|cancel|accept]", Description: "manage durable tasks: inspect, list, resume, or abandon", Category: CommandGroupControl},
	{Name: "audit", Usage: "audit", Description: "explain the last context projection and action surface", Category: CommandGroupControl},
	{Name: "theme", Usage: "theme [dark|light]", Description: "show or switch the palette", Category: CommandGroupControl},
	{Name: "connect", Usage: "connect", Description: "set up a provider connection", Category: CommandGroupControl},
	{Name: "disconnect", Usage: "disconnect [provider]", Description: "remove a saved credential", Category: CommandGroupControl},
	{Name: "status", Usage: "status", Description: "show workspace, session, and authority status", Category: CommandGroupControl},
	{Name: "usage", Usage: "usage", Description: "inspect token usage, cost, and providers", Category: CommandGroupControl},
	{Name: "info", Usage: "info", Description: "show documentation and changelog", Category: CommandGroupControl},
	{Name: "about", Usage: "about", Description: "show documentation and changelog", Hidden: true, Category: CommandGroupControl},
	{Name: "docs", Usage: "docs", Description: "show documentation and changelog", Hidden: true, Category: CommandGroupControl},
	{Name: "changelog", Usage: "changelog", Description: "show documentation and changelog", Hidden: true, Category: CommandGroupControl}, {Name: "doctor", Usage: "doctor", Description: "diagnose setup, credentials, and tools", Category: CommandGroupControl},
	{Name: "mcp", Usage: "mcp", Description: "manage MCP servers and capabilities", Category: CommandGroupControl, Subcommands: []CommandSubcommand{
		{Usage: "list", Description: "show configured MCP servers and cached status"},
		{Usage: "inspect <server>", Description: "inspect one MCP server"},
		{Usage: "refresh [server]", Description: "refresh MCP discovery"},
		{Usage: "enable <server>", Description: "enable one MCP server"},
		{Usage: "disable <server>", Description: "disable one MCP server"},
	}},
	{Name: "diff", Usage: "diff [target]", Description: "inspect the working-tree diff", Category: CommandGroupControl},
	{Name: "commit", Usage: "commit [--retry|--edit]", Description: "propose and create one Git commit", Category: CommandGroupControl},
	{Name: "undo", Usage: "undo", Description: "revert the last agent edit", Category: CommandGroupControl},
	{Name: "redo", Usage: "redo", Description: "restore an undone agent edit", Category: CommandGroupControl},
	{Name: "retry", Usage: "retry", Description: "re-send the last prompt", Category: CommandGroupControl},
	{Name: "compact", Usage: "compact", Description: "summarise context now", Category: CommandGroupControl},
	{Name: "quit", Usage: "quit", Description: "exit the session", Hidden: true, Category: CommandGroupControl},
	{Name: "exit", Usage: "exit", Description: "exit the session", Hidden: true, Category: CommandGroupControl},
	{Name: "q", Usage: "q", Description: "exit the session", Hidden: true, Category: CommandGroupControl},
}

// SuggestCommand names the closest known command to a typo, for the "did you
// mean" hint. Distance 2 covers transpositions and one wrong letter without
// suggesting something unrelated for a genuinely unknown word.
func SuggestCommand(name string) (string, bool) {
	best, bestDist := "", 3
	for _, spec := range Commands {
		// Hidden aliases stay out of discovery (see filterCommands), so they
		// stay out of the suggestion too: hinting /mode for a typo would
		// surface a name the palette deliberately hides, and a hidden alias
		// can easily beat the command it aliases on distance.
		if spec.Hidden {
			continue
		}
		// Single-letter aliases match every single-letter typo at distance
		// 1 and would hijack suggestions like /a (meant /ask) into /q, so
		// they stay out of the hint while remaining runnable.
		if len(spec.Name) <= 1 {
			continue
		}
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

// HelpText renders the command listing for the plain-text REPL's /help and
// /commands. The full-screen UI no longer prints this into the transcript: it
// opens the Command & Help Center instead, so the same registry is presented
// as a navigable, filterable modal.
func HelpText() string {
	var sb strings.Builder
	sb.WriteString("Commands:\n")
	for _, group := range []string{CommandGroupWorkspace, CommandGroupControl} {
		sb.WriteString(group + "\n")
		for _, spec := range Commands {
			if spec.Hidden {
				continue
			}
			if groupOf(spec) != group {
				continue
			}
			fmt.Fprintf(&sb, "  /%-22s %s\n", spec.Usage, spec.Description)
			for _, sub := range spec.Subcommands {
				fmt.Fprintf(&sb, "    /%-20s %s\n", spec.Name+" "+sub.Usage, sub.Description)
			}
		}
	}
	sb.WriteString("\nAnything else is sent to the model.")
	return strings.TrimRight(sb.String(), "\n")
}

// groupOf returns the menu group for a command. Empty categories from older
// callers fall back to CONTROL so help never loses a row.
func groupOf(spec CommandSpec) string {
	if spec.Category == CommandGroupWorkspace {
		return CommandGroupWorkspace
	}
	return CommandGroupControl
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

// PasteCollapsed folds a large multi-line paste into an inline chip. The
// payload is retained for submission and the label is inserted at the cursor.
type PasteCollapsed struct {
	// Text is the paste's full payload.
	Text string
	// Lines is the logical line count shown in the label.
	Lines int
}

func (PasteCollapsed) eventKind() string { return "paste_collapsed" }

// HistoryMoved browses recallable prompts. Positive delta goes older.
type HistoryMoved struct{ Delta int }

func (HistoryMoved) eventKind() string { return "history_moved" }

// PaletteMoved shifts the inline palette highlight. Positive goes down.
type PaletteMoved struct{ Delta int }

func (PaletteMoved) eventKind() string { return "palette_moved" }

// PaletteFilesLoaded installs the workspace roster the "@" trigger completes
// against. It is applied once per session, when the first "@" token appears,
// so a session that never references a file never pays for the walk.
type PaletteFilesLoaded struct{ Files []string }

func (PaletteFilesLoaded) eventKind() string { return "palette_files_loaded" }

// SlashActive reports whether the composer holds a completable slash line:
// single-line input starting with "/". Multiline input stays prose.
func SlashActive(input string) bool {
	if !strings.HasPrefix(input, "/") {
		return false
	}
	return !strings.Contains(input, "\n")
}

// SlashSuggestions filters the authoritative command registry by the text
// after the leading "/". Hidden aliases stay out, matching /help, so the
// list can never drift from what dispatch accepts. It is the "/" trigger's
// view of the unified palette, kept for callers that want commands only.
func SlashSuggestions(input string) []CommandSpec {
	if !SlashActive(input) {
		return nil
	}
	return filterCommands(strings.TrimPrefix(input, "/"))
}

// movePalette shifts the inline palette highlight, wrapping at both ends.
func (s *State) movePalette(delta int) {
	n := len(paletteEntriesFor(*s))
	if n == 0 {
		s.PaletteCursor = 0
		return
	}
	s.PaletteCursor = (s.PaletteCursor + delta) % n
	if s.PaletteCursor < 0 {
		s.PaletteCursor += n
	}
}

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

// Mouse modes: how the full-screen UI receives the wheel.
const (
	// MouseSelect never requests mouse reporting, so the terminal keeps text
	// selection and drag-copy. The wheel arrives as arrow keys via alternate
	// scroll mode, which terminals without it — and multiplexers such as tmux,
	// which consume the wheel for their own copy-mode — do not provide.
	MouseSelect = "select"
	// MouseReport requests real mouse events, so the wheel works everywhere
	// including inside tmux, at the cost of the terminal's own selection.
	MouseReport = "report"
)

// NormalizeMouse maps a configured mode to a known one. Anything unrecognised
// becomes MouseSelect: a typo must not silently cost the user drag-copy, which
// is the capability they are more likely to notice losing.
func NormalizeMouse(name string) string {
	if strings.EqualFold(strings.TrimSpace(name), MouseReport) {
		return MouseReport
	}
	return MouseSelect
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

// PickerToggled stages an MCP enable/disable change on the highlighted row.
// It never runs a hook or performs I/O.
type PickerToggled struct{}

func (PickerToggled) eventKind() string { return "picker_toggled" }

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

// ModelPlaneOpened opens the persistent split-pane model control surface.
//
// The caller owns the roster and the role list: it builds both from
// authoritative metadata and the configuration authority, so the plane never
// fabricates a model fact or a persistence decision.
type ModelPlaneOpened struct {
	// Providers is the provider pane, including the synthetic All row.
	Providers []ProviderRow
	// Models is the merged model roster.
	Models []ModelOption
	// Roles is the effective role configuration.
	Roles []RoleAssignment
	// ActiveRole is the role an Enter applies to in model mode.
	ActiveRole string
	// Loading reports the first roster has not arrived yet.
	Loading bool
	// Origin names the snapshot: "live", "cached", "bundled", "loading".
	Origin string
	// Stale reports cached rows shown while a refresh runs.
	Stale bool
}

func (ModelPlaneOpened) eventKind() string { return "model_plane_opened" }

// ModelPlaneClosed dismisses the control plane without applying anything.
type ModelPlaneClosed struct{}

func (ModelPlaneClosed) eventKind() string { return "model_plane_closed" }

// PlaneMoved shifts the highlight in the focused pane. Positive goes down.
type PlaneMoved struct{ Delta int }

func (PlaneMoved) eventKind() string { return "plane_moved" }

// PlanePaged shifts the highlight by approximately one viewport.
type PlanePaged struct{ Delta int }

func (PlanePaged) eventKind() string { return "plane_paged" }

// PlaneTop parks the highlight on the first row of the focused pane.
type PlaneTop struct{}

func (PlaneTop) eventKind() string { return "plane_top" }

// PlaneBottom parks the highlight on the last row of the focused pane.
type PlaneBottom struct{}

func (PlaneBottom) eventKind() string { return "plane_bottom" }

// PlaneFocusChanged moves focus between the two panes. Right=true focuses the
// right pane (models in model mode), false the left.
type PlaneFocusChanged struct{ Right bool }

func (PlaneFocusChanged) eventKind() string { return "plane_focus_changed" }

// PlaneFocusSet moves the keyboard to an explicit control. Tab cycling and
// programmatic focus both go through it so the mode stays in step.
type PlaneFocusSet struct{ Focus PlaneFocus }

func (PlaneFocusSet) eventKind() string { return "plane_focus_set" }

// PlaneFocusMoved steps the keyboard through the Tab order. Positive goes
// forward, negative goes back.
type PlaneFocusMoved struct{ Delta int }

func (PlaneFocusMoved) eventKind() string { return "plane_focus_moved" }

// PlaneModeToggled swaps model browsing and role assignment.
type PlaneModeToggled struct{}

func (PlaneModeToggled) eventKind() string { return "plane_mode_toggled" }

// PlaneFilterChanged replaces the query.
type PlaneFilterChanged struct{ Text string }

func (PlaneFilterChanged) eventKind() string { return "plane_filter_changed" }

// PlaneTyped inserts text into the query at the cursor.
type PlaneTyped struct{ Text string }

func (PlaneTyped) eventKind() string { return "plane_typed" }

// PlaneFilterBackspace deletes the rune before the query cursor.
type PlaneFilterBackspace struct{}

func (PlaneFilterBackspace) eventKind() string { return "plane_filter_backspace" }

// PlaneFilterDelete deletes the rune under the query cursor.
type PlaneFilterDelete struct{}

func (PlaneFilterDelete) eventKind() string { return "plane_filter_delete" }

// PlaneFilterCursorMoved shifts the query cursor.
type PlaneFilterCursorMoved struct{ Delta int }

func (PlaneFilterCursorMoved) eventKind() string { return "plane_filter_cursor_moved" }

// PlaneFilterCleared empties the query.
type PlaneFilterCleared struct{}

func (PlaneFilterCleared) eventKind() string { return "plane_filter_cleared" }

// PlaneVariantOpened starts the choose-a-variant prompt for the highlighted
// model. It is a no-op when the model needs no choice.
type PlaneVariantOpened struct{}

func (PlaneVariantOpened) eventKind() string { return "plane_variant_opened" }

// PlaneVariantMoved shifts the variant highlight. Positive goes down.
type PlaneVariantMoved struct{ Delta int }

func (PlaneVariantMoved) eventKind() string { return "plane_variant_moved" }

// PlaneVariantClosed dismisses the variant prompt without choosing.
type PlaneVariantClosed struct{}

func (PlaneVariantClosed) eventKind() string { return "plane_variant_closed" }

// PlaneEffortCycled moves the pending thinking effort among the highlighted
// model's authoritative options.
type PlaneEffortCycled struct{ Delta int }

func (PlaneEffortCycled) eventKind() string { return "plane_effort_cycled" }

// PlaneRoleCycled moves the active role in model mode.
type PlaneRoleCycled struct{ Delta int }

func (PlaneRoleCycled) eventKind() string { return "plane_role_cycled" }

// PlaneRoleStaged begins the roles-first transaction for the highlighted role:
// the role is staged and the flow moves to model selection. It never assigns.
type PlaneRoleStaged struct{}

func (PlaneRoleStaged) eventKind() string { return "plane_role_staged" }

// PlaneModelStaged begins the model-first transaction for a picked model: the
// model is staged and the flow moves to role selection. It never assigns.
type PlaneModelStaged struct{ Model ModelOption }

func (PlaneModelStaged) eventKind() string { return "plane_model_staged" }

// PlaneRoleChosen confirms the highlighted role in the role stage and advances
// to the effort (or apply) step.
type PlaneRoleChosen struct{}

func (PlaneRoleChosen) eventKind() string { return "plane_role_chosen" }

// PlaneModelChosen confirms the highlighted model in the roles-flow model step
// and advances to the effort (or apply) step.
type PlaneModelChosen struct{}

func (PlaneModelChosen) eventKind() string { return "plane_model_chosen" }

// PlaneStageCancelled backs out one step of the assignment transaction,
// discarding transient state without persisting or closing.
type PlaneStageCancelled struct{}

func (PlaneStageCancelled) eventKind() string { return "plane_stage_cancelled" }

// PlaneVariantConfirmed commits a chosen variant row and advances the flow.
type PlaneVariantConfirmed struct{ Model ModelOption }

func (PlaneVariantConfirmed) eventKind() string { return "plane_variant_confirmed" }

// PlaneAssigned records the outcome of an assignment attempt: the applied
// role, the refreshed role roster, and a one-line notice (confirmation or
// refusal).
type PlaneAssigned struct {
	// Result is the assignment that was applied.
	Result RoleAssignment
	// Roles is the refreshed effective role roster, or nil to leave it.
	Roles []RoleAssignment
	// Notice is the confirmation or refusal shown in the footer.
	Notice string
}

func (PlaneAssigned) eventKind() string { return "plane_assigned" }

// PlaneAuthOpened starts inline API-key entry for the highlighted provider.
type PlaneAuthOpened struct{}

func (PlaneAuthOpened) eventKind() string { return "plane_auth_opened" }

// PlaneAuthClosed abandons key entry without saving.
type PlaneAuthClosed struct{}

func (PlaneAuthClosed) eventKind() string { return "plane_auth_closed" }

// PlaneAuthTyped inserts text into the key entry at the cursor.
type PlaneAuthTyped struct{ Text string }

func (PlaneAuthTyped) eventKind() string { return "plane_auth_typed" }

// PlaneAuthBackspace deletes the rune before the entry cursor.
type PlaneAuthBackspace struct{}

func (PlaneAuthBackspace) eventKind() string { return "plane_auth_backspace" }

// PlaneAuthDelete deletes the rune under the entry cursor.
type PlaneAuthDelete struct{}

func (PlaneAuthDelete) eventKind() string { return "plane_auth_delete" }

// PlaneAuthCursorMoved shifts the entry cursor. Positive goes right.
type PlaneAuthCursorMoved struct{ Delta int }

func (PlaneAuthCursorMoved) eventKind() string { return "plane_auth_cursor_moved" }

// PlaneAuthClearedToStart deletes everything before the entry cursor.
type PlaneAuthClearedToStart struct{}

func (PlaneAuthClearedToStart) eventKind() string { return "plane_auth_cleared_to_start" }

// PlaneAuthClearedToEnd deletes everything from the entry cursor onward.
type PlaneAuthClearedToEnd struct{}

func (PlaneAuthClearedToEnd) eventKind() string { return "plane_auth_cleared_to_end" }

// PlaneAuthWordBack deletes the word before the entry cursor.
type PlaneAuthWordBack struct{}

func (PlaneAuthWordBack) eventKind() string { return "plane_auth_word_back" }

// PlaneAuthSaved records a successful key save: the prompt closes, the
// provider pane refreshes, and the footer confirms the provider.
type PlaneAuthSaved struct {
	// Providers is the refreshed provider pane, or nil to leave it.
	Providers []ProviderRow
	// Notice is the confirmation shown in the footer.
	Notice string
}

func (PlaneAuthSaved) eventKind() string { return "plane_auth_saved" }

// PlaneAuthFailed records a refused save. The prompt stays open so the key
// can be corrected; only the footer notice changes.
type PlaneAuthFailed struct {
	// Notice is the refusal shown in the footer.
	Notice string
}

func (PlaneAuthFailed) eventKind() string { return "plane_auth_failed" }

// PlaneRowsChanged resizes the list window, e.g. after a resize.
type PlaneRowsChanged struct{ Rows int }

func (PlaneRowsChanged) eventKind() string { return "plane_rows_changed" }

// HistoryReplaced swaps the transcript for replayed entries on resume.
type HistoryReplaced struct {
	// Entries is the replayed transcript, oldest first.
	Entries []Entry
}

func (HistoryReplaced) eventKind() string { return "history_replaced" }

// ConnectKeyOpened opens the /connect API-key modal over a provider snapshot.
// The view is copied, so the modal works on a stable read of the provider.
type ConnectKeyOpened struct {
	// View is the credential modal state to show.
	View ConnectKeyView
}

func (ConnectKeyOpened) eventKind() string { return "connect_key_opened" }

// ConnectKeyClosed dismisses the API-key modal without submitting.
type ConnectKeyClosed struct{}

func (ConnectKeyClosed) eventKind() string { return "connect_key_closed" }

// ConnectKeyChanged replaces the masked credential buffer.
type ConnectKeyChanged struct{ Key string }

func (ConnectKeyChanged) eventKind() string { return "connect_key_changed" }

// ConnectKeyValidating marks the credential check in flight, locking the
// field and clearing any prior failure.
type ConnectKeyValidating struct{}

func (ConnectKeyValidating) eventKind() string { return "connect_key_validating" }

// ConnectKeyFailed reports a rejected credential in place, so the modal stays
// open and the key can be fixed without redoing the flow.
type ConnectKeyFailed struct{ Error string }

func (ConnectKeyFailed) eventKind() string { return "connect_key_failed" }

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
// session label, measured usage text, and the execution authority. It never
// touches the transcript, so a resize or a usage tick cannot duplicate
// requests or mutate history.
type WorkspaceUpdated struct {
	// Cwd is the working directory for the status line. Empty leaves it.
	Cwd string
	// Session names the session, e.g. "#12". Empty leaves it.
	Session string
	// UsageText is preformatted usage detail. Empty leaves it.
	UsageText string
	// Workspace is the execution authority label, e.g. "BUILD".
	Workspace string
	// WorkspaceAccent is the semantic accent for the authority. The producer
	// reads it from workspace.Style(), so the TUI never hardcodes a
	// workspace colour (§9).
	WorkspaceAccent string
	// HasCwd, HasSession, HasUsage, HasWorkspace mark which fields apply: an
	// empty string can be a deliberate clear, not just an omission.
	HasCwd       bool
	HasSession   bool
	HasUsage     bool
	HasWorkspace bool
	// User is the local display name. HasUser marks it applies.
	User    string
	HasUser bool
	// Greeting is the time-of-day greeting. HasGreeting marks it applies.
	Greeting    string
	HasGreeting bool
	// GitBranch and GitStatus carry truthful repository state. HasGit marks
	// they apply. GitStaged, GitModified, and GitUntracked carry the counts
	// when GitKnown is set; an unknown status leaves GitKnown false so the
	// card reports the summary line alone rather than inventing zeros.
	GitBranch    string
	GitStatus    string
	GitStaged    int
	GitModified  int
	GitUntracked int
	GitKnown     bool
	HasGit       bool
	// Ready names runtime readiness. HasReady marks it applies.
	Ready    string
	HasReady bool
}

func (WorkspaceUpdated) eventKind() string { return "workspace_updated" }

// IdentityUpdated carries the runtime identity facts: local display name,
// greeting, git state, and readiness. It never touches the transcript.
type IdentityUpdated struct {
	// User is the local display name. Empty clears it.
	User string
	// Greeting is the time-of-day greeting.
	Greeting string
	// GitBranch is the short branch name.
	GitBranch string
	// GitStatus is the cleanliness summary.
	GitStatus string
	// GitStaged, GitModified, and GitUntracked are the status counts;
	// GitKnown marks them measured. Unknown counts stay zero so /status never
	// reports a fabricated clean tree.
	GitStaged    int
	GitModified  int
	GitUntracked int
	GitKnown     bool
	// Ready names runtime readiness.
	Ready string
}

func (IdentityUpdated) eventKind() string { return "identity_updated" }

// SupportStatus is where a support turn is in its lifecycle.
type SupportStatus string

// Support lifecycle states.
const (
	// SupportIdle means the panel is open for input with no turn in flight.
	SupportIdle SupportStatus = "idle"
	// SupportRunning means a support request is executing.
	SupportRunning SupportStatus = "running"
	// SupportCompleted means the support response finished.
	SupportCompleted SupportStatus = "completed"
	// SupportCancelled means the support turn was cancelled.
	SupportCancelled SupportStatus = "cancelled"
	// SupportFailed means the support turn ended in an error.
	SupportFailed SupportStatus = "failed"
)

// SupportPanel is the ephemeral $sup overlay state.
//
// It owns the support interaction while open: its input, the in-flight or
// finished response, and the effective SUP model identity the runtime
// resolved. It is discarded on close and never enters the normal
// transcript, history, or session.
type SupportPanel struct {
	// Input is the support request being composed.
	Input string
	// Cursor is the rune offset in Input where typing lands.
	Cursor int
	// Request is the submitted support question for the current turn.
	Request string
	// Response is the streamed support answer for the current turn.
	Response string
	// Status is the lifecycle position of the current turn.
	Status SupportStatus
	// Seq identifies the current support turn. Stale events are dropped.
	Seq int
	// Model is the effective SUP model reference, e.g. "provider/id".
	Model string
	// Effort is the effective thinking effort, or "" when unset.
	Effort string
	// Error carries a failure message when Status is SupportFailed.
	Error string
	// Scroll is the response viewport offset in lines above the live edge.
	// Zero follows new output; positive looks back at older lines while
	// streaming continues underneath. It is presentation-only and never
	// enters the transcript, history, or session.
	Scroll int
}

// SupportInputFocused reports whether the panel owns printable input: the
// panel is open and no support turn is running.
func (s *State) SupportInputFocused() bool {
	if s.SupportPanel == nil {
		return false
	}
	return s.SupportPanel.Status != SupportRunning
}

// SupportBusy reports whether a support turn is in flight.
func (s *State) SupportBusy() bool {
	return s.SupportPanel != nil && s.SupportPanel.Status == SupportRunning
}

// SupportOpened opens the ephemeral surface without starting a turn. A
// bare "$sup" carries no request, so no provider call occurs yet.
type SupportOpened struct {
	// Model is the effective SUP model reference for display.
	Model string
	// Effort is the effective thinking effort for display.
	Effort string
}

func (SupportOpened) eventKind() string { return "support_opened" }

// SupportInputChanged sets the support composer's text and cursor.
type SupportInputChanged struct {
	// Text is the new support input.
	Text string
	// Cursor is the rune offset where typing lands.
	Cursor int
}

func (SupportInputChanged) eventKind() string { return "support_input_changed" }

// SupportStarted begins one support turn with its own sequence identity.
type SupportStarted struct {
	// Seq identifies the support turn.
	Seq int
	// Request is the submitted support question.
	Request string
	// Model is the effective SUP model reference.
	Model string
	// Effort is the effective thinking effort.
	Effort string
}

func (SupportStarted) eventKind() string { return "support_started" }

// SupportDelta streams one chunk of the support answer.
type SupportDelta struct {
	// Seq identifies the support turn.
	Seq int
	// Text is the chunk to append.
	Text string
}

func (SupportDelta) eventKind() string { return "support_delta" }

// SupportDone marks a support turn successfully completed.
type SupportDone struct {
	// Seq identifies the support turn.
	Seq int
	// Text is appended when the transport did not stream (empty otherwise).
	Text string
}

func (SupportDone) eventKind() string { return "support_done" }

// SupportFailedEvent marks a support turn failed with an error.
type SupportFailedEvent struct {
	// Seq identifies the support turn.
	Seq int
	// Error is the user-visible failure.
	Error string
}

func (SupportFailedEvent) eventKind() string { return "support_failed" }

// SupportCancelledEvent is the single semantic cancellation transition
// for a support turn: the first one wins, later duplicates and stale
// turns are dropped.
type SupportCancelledEvent struct {
	// Seq identifies the support turn.
	Seq int
	// Text is the user-visible cancellation line. Empty renders "cancelled".
	Text string
}

func (SupportCancelledEvent) eventKind() string { return "support_cancelled" }

// SupportTurn records one ephemeral $sup exchange. It never persists and never
// enters the model's context (I17).
//
// It is the legacy single-event form: it opens the overlay (when needed)
// with the finished exchange, so older callers keep working while the
// interactive path streams through SupportStarted/SupportDelta/SupportDone.
type SupportTurn struct {
	// Question is what the user asked.
	Question string
	// Answer is the support model's reply.
	Answer string
}

func (SupportTurn) eventKind() string { return "support_turn" }

// SupportClosed dismisses the ephemeral surface, removing its entries.
type SupportClosed struct{}

func (SupportClosed) eventKind() string { return "support_closed" }

// SupportScrolled moves the support response viewport. Positive Delta looks
// back at older lines; negative returns toward the live edge. The view
// clamps to the content, so overshoot simply parks at the oldest line.
type SupportScrolled struct {
	// Delta is the lines to move. Positive goes up (older).
	Delta int
}

func (SupportScrolled) eventKind() string { return "support_scrolled" }

// SupportScrollToBottom re-engages the support live edge.
type SupportScrollToBottom struct{}

func (SupportScrollToBottom) eventKind() string { return "support_scroll_to_bottom" }

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
	// MaxOutput is the model's maximum completion in tokens, or zero when
	// unknown.
	MaxOutput int
	// CostInput, CostOutput, CostCacheRead and CostCacheWrite are the
	// per-million-token USD schedule. They are meaningful only when
	// PriceKnown is true; a zero under PriceKnown is a real zero (free),
	// not a missing value.
	CostInput      float64
	CostOutput     float64
	CostCacheRead  float64
	CostCacheWrite float64
	// InputModalities lists the content kinds the model accepts, e.g.
	// "text", "image". Empty means unknown, never unsupported.
	InputModalities []string
	// ThinkingEfforts lists the effort levels the model exposes, in
	// ascending cost order. Empty means no explicit ladder; Reasoning and
	// ReasoningKnown then decide support.
	ThinkingEfforts []string
	// Reasoning reports the model accepts a thinking budget.
	Reasoning bool
	// ReasoningKnown reports Reasoning is authoritative. False means the
	// source said nothing, so the UI renders "unknown", never
	// "unsupported".
	ReasoningKnown bool
}

// sameModelOptions reports whether two rosters are indistinguishable to the
// control plane. A refresh that changes nothing must not swap the rows out
// from under the user's cursor.
func sameModelOptions(a, b []ModelOption) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// Origin is per-row provenance, not a displayed fact: the plane shows
		// freshness once for the whole roster, so cached->live alone must not
		// rebuild the rows. Changed facts still differ through the fields below.
		x, y := a[i], b[i]
		if x.ID != y.ID || x.Provider != y.Provider || x.Title != y.Title ||
			x.Detail != y.Detail || x.Active != y.Active || x.Name != y.Name ||
			x.Context != y.Context || x.PriceKnown != y.PriceKnown ||
			x.Free != y.Free || x.MaxOutput != y.MaxOutput || x.CostInput != y.CostInput ||
			x.CostOutput != y.CostOutput || x.CostCacheRead != y.CostCacheRead ||
			x.CostCacheWrite != y.CostCacheWrite || x.Reasoning != y.Reasoning ||
			x.ReasoningKnown != y.ReasoningKnown ||
			!slices.Equal(x.Capabilities, y.Capabilities) ||
			!slices.Equal(x.InputModalities, y.InputModalities) ||
			!slices.Equal(x.ThinkingEfforts, y.ThinkingEfforts) {
			return false
		}
	}
	return true
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
