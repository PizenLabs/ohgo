package tui

import (
	"strings"
	"time"
)

// This file is the thinking projection seam.
//
// Runtime state is authoritative; the activity line is only a projection of
// it. ProjectThinking reads the same State that drives the transcript and the
// renderer never decides for itself what the runtime is doing. There is no
// second state machine, no timer goroutine, and no clock inside the renderer:
// elapsing is derived from the activity's real start time, which the runtime
// stamped.
//
// ProjectThinking is the semantic projection. telemetry_bar.go is the
// production phase owner for the workspace canvas, and ProjectActivity
// (activity.go) decides which concrete entry owns action detail. The workspace
// role is never part of this projection; the prompt boundary owns it.

// ThinkingPhase is the authoritative execution category the activity line
// projects.
//
// Only categories the runtime can actually establish are defined. There is
// deliberately no Inspect or Verify phase: ohgo does not tag a tool by intent,
// and reading "verifying" out of a tool name or out of model prose would be a
// claim the runtime cannot prove. Every executing tool is Execute; the
// argument-derived summary carries the specific detail.
type ThinkingPhase string

// Thinking phases.
const (
	// ThinkingNone means no activity line exists.
	ThinkingNone ThinkingPhase = ""
	// ThinkingModel means the model is working without visible text yet. The
	// user-facing label is "reason": it names the runtime phase rather than
	// the model's private process.
	ThinkingModel ThinkingPhase = "model"
	// ThinkingRespond means visible answer text is streaming, so the truthful
	// phase is responding rather than reasoning.
	ThinkingRespond ThinkingPhase = "respond"
	// ThinkingTool means a tool call is executing. The user-facing label is
	// "execute": the runtime is running an action, not merely "working".
	ThinkingTool ThinkingPhase = "tool"
	// ThinkingWaiting means the turn is blocked on human approval.
	ThinkingWaiting ThinkingPhase = "waiting"
	// ThinkingCommit means the dedicated /commit workflow is working. It is a
	// distinct phase because no agent turn is running, so the ordinary
	// Busy-derived label would be false.
	ThinkingCommit ThinkingPhase = "commit"
)

// ThinkingState is the presentation contract for the activity line.
//
// It is derived from State on every render and never stored: a stored copy
// would drift from the runtime state that owns the truth. Every field is
// backed by runtime state; an unknown value stays empty rather than being
// guessed.
type ThinkingState struct {
	// Active reports whether an activity line should exist at all.
	Active bool
	// Phase is the authoritative execution category.
	Phase ThinkingPhase
	// Label is the deterministic runtime phase word ("reason", "respond",
	// "execute", "prepare", "waiting for approval"). It carries no execution
	// authority and is never model-generated prose.
	Label string
	// Detail is truthful runtime detail — currently a running tool's own,
	// argument-derived summary. It is never inferred from model prose.
	Detail string
	// StartedAt is when the represented activity actually began. Zero means
	// unknown, in which case no elapsed time is shown: substituting render
	// time would fabricate a duration.
	StartedAt time.Time
	// TurnStartedAt is when the whole turn began. It is the long-running
	// context: during a tool it differs from StartedAt, so the line can show
	// both the action's duration and the task's elapsed time. Zero means
	// unknown and stays absent.
	TurnStartedAt time.Time
	// Interruptible reports that the runtime can actually interrupt the
	// operation, so the line may advertise esc. Waiting for approval is not
	// interruptible from the line: the approval dialog owns the keyboard.
	Interruptible bool
	// OutputTokens is the live estimated output count while streaming. Zero
	// means unknown or nothing yet, in which case no token segment renders:
	// the line never fabricates telemetry it did not count.
	OutputTokens int
}

// ProjectThinking maps the authoritative runtime state to the activity line.
//
// It is a pure function: it reads State and returns a value, with no I/O, no
// clock read, no provider call, no mutation, and no Bubble Tea command. Phase
// selection defers to DeriveRuntime, the existing single owner of "which phase
// is this", so the activity line can never drift from the header's phase word.
func ProjectThinking(s State) ThinkingState {
	// /commit owns its own lifecycle and runs while the agent turn is idle.
	// Read it first so the activity line names the work that is actually in
	// flight rather than reporting an idle session.
	if s.CommitActive() && !s.Quit {
		label := "prepare"
		if s.Commit.Phase == CommitExecuting {
			label = "execute"
		}
		return ThinkingState{
			Active:        true,
			Phase:         ThinkingCommit,
			Label:         label,
			Detail:        "commit",
			StartedAt:     s.Commit.StartedAt,
			TurnStartedAt: s.TurnStartedAt,
			Interruptible: s.Commit.Phase == CommitProposing,
		}
	}
	switch DeriveRuntime(s) {
	case RuntimeAwaitingApproval:
		// A blocked tool is not running, so waiting gets no elapsed timer
		// and no interrupt hint: the approval dialog owns the keyboard, and
		// advertising esc here would name a key the dialog has taken over.
		return ThinkingState{Active: true, Phase: ThinkingWaiting, Label: "waiting for approval", TurnStartedAt: s.TurnStartedAt}

	case RuntimeRunningTool:
		ts := ThinkingState{Active: true, Phase: ThinkingTool, Label: "execute", TurnStartedAt: s.TurnStartedAt, Interruptible: true}
		if tool := latestRunningTool(s); tool != nil {
			ts.Detail = tool.Summary
			if ts.Detail == "" {
				ts.Detail = tool.Name
			}
			ts.StartedAt = tool.StartedAt
		}
		return ts

	case RuntimeStreaming:
		// Text is already visible, so the truthful phase is responding, not
		// reasoning. The distinction already exists in DeriveRuntime.
		// OutputTokens is the running estimate counted from streamed deltas
		// in State.Apply, so the line reports what arrived, never a guess.
		return ThinkingState{
			Active:        true,
			Phase:         ThinkingRespond,
			Label:         "respond",
			StartedAt:     s.TurnStartedAt,
			TurnStartedAt: s.TurnStartedAt,
			Interruptible: true,
			OutputTokens:  s.OutputTokens,
		}

	case RuntimeWorking:
		return ThinkingState{
			Active:        true,
			Phase:         ThinkingModel,
			Label:         "reason",
			StartedAt:     s.TurnStartedAt,
			TurnStartedAt: s.TurnStartedAt,
			Interruptible: true,
		}

	default:
		// Idle and shutting down have no activity to claim. Completion,
		// failure, and cancellation all leave nothing behind, so a stale
		// activity line can never outlive the turn.
		return ThinkingState{}
	}
}

// latestRunningTool returns the most recent tool still executing, or nil.
//
// A concurrent tool batch can have several running at once; the latest is the
// one the user just watched start, matching LatestToolID's rule. The choice is
// deterministic and selects among tools the runtime actually holds — it never
// invents one.
func latestRunningTool(s State) *ToolEntry {
	if index := latestRunningToolIndex(s); index >= 0 {
		return s.Entries[index].Tool
	}
	return nil
}

// latestRunningToolIndex is latestRunningTool as a transcript position. The
// activity projection needs the identity so it can hand presentation to that
// exact entry instead of rendering a second copy on the activity line.
func latestRunningToolIndex(s State) int {
	if s.runtimeTracked && s.runningTools == 0 {
		return -1
	}
	for i := len(s.Entries) - 1; i >= 0; i-- {
		entry := &s.Entries[i]
		if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.Status == ToolRunning {
			return i
		}
	}
	return -1
}

// RenderThinking renders the activity line for a projected state.
//
// It is pure and deterministic: now is supplied rather than read, so the same
// inputs always produce the same output and tests need no wall clock. An
// inactive state yields no lines; an active state yields exactly one. When
// width is positive the line never exceeds it, and the head shortens before
// the timer does, so the duration survives a narrow terminal. The token
// segment renders only when OutputTokens is positive, so unknown stays absent
// rather than fabricated. This entry point stays shimmer-free so plain-text
// assertions keep working; animated callers use RenderThinkingTick.
func RenderThinking(state ThinkingState, now time.Time, width int) []string {
	return renderThinking(state, now, 0, false, width)
}

// RenderThinkingTick renders the activity line with a frame index for the
// spinner and the shimmer wave. It is pure like RenderThinking: tick is
// supplied rather than read, so output is deterministic. Only the phase word
// shimmers, so the elapsed time and the tool detail keep stable text. Width
// handling matches RenderThinking; a truncated shimmer degrades to plain text
// rather than overflowing.
func RenderThinkingTick(state ThinkingState, now time.Time, tick int, width int) []string {
	return renderThinking(state, now, tick, true, width)
}

// RenderThinkingFrame is the no-colour/no-shimmer animation path. The spinner
// frame still advances with tick, so liveness survives a terminal without
// TrueColor or with reduced motion.
func RenderThinkingFrame(state ThinkingState, now time.Time, tick int, width int) []string {
	return renderThinking(state, now, tick, false, width)
}

func renderThinking(state ThinkingState, now time.Time, tick int, shimmer bool, width int) []string {
	return renderThinkingZoned(state, now, tick, shimmer, width, liveThinkingZones)
}

// thinkingZones maps each activity-line zone to its style, keeping color
// decisions separate from layout math. Every field is a pure string
// function, so tests can substitute markers and assert the exact zoning
// while production resolves through the semantic chrome tokens in style.go.
// No zone carries meaning by hue alone: the glyph and the words already name
// the state, color only quiets the secondary segments.
type thinkingZones struct {
	// active styles the glyph and the head content while executing.
	active func(string) string
	// waiting styles the blocked-on-approval line, which must read as
	// parked rather than live.
	waiting func(string) string
	// meta styles the token count: information that recedes instead of
	// competing with the label.
	meta func(string) string
	// dim styles the · separators as clean punctuation.
	dim func(string) string
	// tail styles elapsed time and the esc hint as secondary system status.
	tail func(string) string
	// wave renders the animated label for the shimmer frame.
	wave func(label string, tick int) string
}

// Thinking zone styles, resolved once from the chrome tokens: primary for
// the live head, muted for parked text and token telemetry, dim slate for
// punctuation and tail status.
var (
	thinkingActiveStyle  = fg(ColorPrimary)
	thinkingWaitingStyle = fg(ColorMuted)
	thinkingMetaStyle    = fg(ColorMuted)
	thinkingDimStyle     = fg(ColorDim)
	thinkingTailStyle    = fg(ColorDim)
)

// liveThinkingZones is the production zoning. Lipgloss resolves the profile
// from the terminal, so colorless environments render plain text and every
// stripped assertion keeps holding.
var liveThinkingZones = thinkingZones{
	active: func(s string) string { return thinkingActiveStyle.Render(s) },
	waiting: func(s string) string {
		return thinkingWaitingStyle.Render(s)
	},
	meta: func(s string) string { return thinkingMetaStyle.Render(s) },
	dim:  func(s string) string { return thinkingDimStyle.Render(s) },
	tail: func(s string) string { return thinkingTailStyle.Render(s) },
	wave: ShimmerText,
}

func renderThinkingZoned(state ThinkingState, now time.Time, tick int, shimmer bool, width int, z thinkingZones) []string {
	if !state.Active {
		return nil
	}
	if state.Phase == ThinkingWaiting {
		// Blocked on the user, not executing: the whole row parks in the
		// muted zone with no timer and no interrupt hint.
		return []string{formatThinkingLine(z.waiting(thinkingGlyphAt(state.Phase, tick)), z.waiting(state.Label), "", width, z)}
	}
	// The shimmer wraps only phase-label runes and its stripped width always
	// equals the plain width, so the trailing metadata never shifts across
	// ticks. Styles apply after the text blocks are built, so color codes
	// never enter length math: CellWidth and TruncateToWidth strip them.
	label := state.Label
	if shimmer && state.Phase != ThinkingWaiting {
		label = z.wave(label, tick)
	} else {
		label = z.active(label)
	}
	content := label
	if state.Detail != "" {
		content += " " + z.active(oneLine(state.Detail))
	}
	// Metadata is one parenthesised block on the activity line:
	// (↓tokens · elapsed · esc). Tokens carry the ↓ prefix so the count reads
	// without colour; elapsed derives from the real start; esc appears only
	// when the runtime proved interruptibility. Unknown stays absent rather
	// than fabricated. The full-screen canvas does not reserve a line for
	// this: activity there is a transcript entry, and the stable hint row
	// carries telemetry.
	var metaParts []string
	if state.OutputTokens > 0 {
		metaParts = append(metaParts, z.meta(formatOutputTokens(state.OutputTokens)))
	}
	// Tail is elapsed time plus the interrupt hint. It yields last under
	// truncation because it carries the live affordances.
	if elapsed := elapsedLabel(state.StartedAt, now); elapsed != "" {
		metaParts = append(metaParts, z.tail(elapsed))
	}
	// The whole-turn elapsed is the long-running context. It only appears
	// when it differs from the action's own start, so "reason" does not
	// print the same duration twice; a running tool shows both its own time
	// and how long the task has been going.
	if !state.TurnStartedAt.IsZero() && !state.TurnStartedAt.Equal(state.StartedAt) {
		if total := elapsedLabel(state.TurnStartedAt, now); total != "" {
			metaParts = append(metaParts, z.tail("turn "+total))
		}
	}
	// The interrupt hint is only ever emitted when the runtime proved the
	// operation is interruptible, and it names the key the existing turn
	// cancellation already uses.
	if state.Interruptible {
		metaParts = append(metaParts, z.tail("esc"))
	}
	tail := ""
	if len(metaParts) > 0 {
		tail = z.dim("(") + strings.Join(metaParts, " "+z.dim("·")+" ") + z.dim(")")
	}
	return []string{formatThinkingLine(z.active(thinkingGlyphAt(state.Phase, tick)), content, tail, width, z)}
}

// formatThinkingLine joins glyph, content, and parenthesised tail into one
// compact row: "◌ respond · (↓248 tokens · 5s · esc)".
//
// The head yields before the tail under truncation, so the timer and the esc
// hint survive a narrow terminal while a long tool summary trims first. The
// separator " · " reads in the dim zone like every other punctuation, and the
// line is never padded to fill width: its cells are exactly the content plus
// the separator and tail, keeping shimmer frames layout-stable.
func formatThinkingLine(glyph, content, tail string, width int, z thinkingZones) string {
	head := glyph
	if content != "" {
		head += " " + content
	}
	if tail == "" {
		if width > 0 && CellWidth(head) > width {
			return TruncateToWidth(head, width)
		}
		return head
	}
	sep := " " + z.dim("·") + " "
	if width <= 0 {
		return head + sep + tail
	}
	if CellWidth(tail) > width {
		// Even the metadata alone overflows: the head yields entirely
		// rather than squeezing both sides into ellipses.
		return TruncateToWidth(tail, width)
	}
	budget := width - CellWidth(tail) - CellWidth(sep)
	if budget <= 0 {
		return TruncateToWidth(tail, width)
	}
	if CellWidth(head) > budget {
		head = TruncateToWidth(head, budget)
	}
	return head + sep + tail
}

// formatOutputTokens renders the live token segment with its ↓ prefix.
// Non-positive counts yield "", so unknown stays absent rather than rendering
// as zero. The count uses the shared compact formatter, so a four-digit volume
// reads "↓1.1k tokens" on the one-row telemetry bar.
func formatOutputTokens(n int) string {
	if n <= 0 {
		return ""
	}
	return "↓" + formatTokens(n) + " tokens"
}

// thinkingGlyph is the static fallback marker for phases that do not animate
// (waiting and commit preparation). The phase is the authority; the marker
// only makes the row readable without colour.
func thinkingGlyph(phase ThinkingPhase) string {
	if phase == ThinkingWaiting {
		return "◇"
	}
	return "◌"
}

// spinnerGlyph returns one Braille animation frame. The caller supplies the
// shared tick, which the program advances at spinnerInterval (90ms), so a
// burst of stream deltas cannot spin the glyph faster than the throttled frame
// clock.
func spinnerGlyph(tick int) string {
	if len(spinnerFrames) == 0 {
		return "◌"
	}
	frame := tick % len(spinnerFrames)
	if frame < 0 {
		frame += len(spinnerFrames)
	}
	return spinnerFrames[frame]
}

// thinkingGlyphAt selects the marker for the phase. Reasoning and responding
// are live model work, so they get the smooth Braille spinner. Waiting is
// parked on a human, and commit preparation is a workflow phase rather than
// model generation, so both keep a stable marker; the tick never invents
// motion where the runtime is not generating.
func thinkingGlyphAt(phase ThinkingPhase, tick int) string {
	switch phase {
	case ThinkingModel, ThinkingRespond:
		return spinnerGlyph(tick)
	default:
		return thinkingGlyph(phase)
	}
}

// elapsedLabel formats a truthful elapsed time from the activity's real start.
//
// A zero start means unknown, and unknown yields "" rather than a fabricated
// duration. A future start (a clock that went backwards) clamps to zero rather
// than rendering a negative age.
func elapsedLabel(started, now time.Time) string {
	if started.IsZero() {
		return ""
	}
	d := now.Sub(started)
	if d < 0 {
		d = 0
	}
	// Sub-second ages are not information a transcript reader can use, and
	// showing "0s" makes every line look laggy at the moment it starts.
	if d < time.Second {
		return ""
	}
	return formatDurationText(d)
}
