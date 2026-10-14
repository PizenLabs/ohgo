package tui

import (
	"fmt"
	"strings"
	"time"
)

// This file is the thinking projection seam.
//
// Runtime state is authoritative; the activity line is only a projection of
// it. ProjectThinking reads the same State that drives the transcript and the
// header, RenderThinking turns that projection into one bounded row, and the
// renderer never decides for itself what the runtime is doing. There is no
// second state machine, no timer goroutine, and no clock inside the renderer:
// elapsing is derived from the activity's real start time, which the runtime
// stamped.

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
	// ThinkingModel means the model is generating or awaiting its response.
	ThinkingModel ThinkingPhase = "model"
	// ThinkingTool means a tool call is executing.
	ThinkingTool ThinkingPhase = "tool"
	// ThinkingWaiting means the turn is blocked on human approval.
	ThinkingWaiting ThinkingPhase = "waiting"
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
	// Label is presentation vocabulary for the phase ("Thinking…",
	// "Running", "Waiting for approval"). It carries no execution authority.
	Label string
	// Detail is truthful runtime detail — currently a running tool's own,
	// argument-derived summary. It is never inferred from model prose.
	Detail string
	// StartedAt is when the represented activity actually began. Zero means
	// unknown, in which case no elapsed time is shown: substituting render
	// time would fabricate a duration.
	StartedAt time.Time
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
	switch DeriveRuntime(s) {
	case RuntimeAwaitingApproval:
		// A blocked tool is not running, so waiting gets no elapsed timer
		// and no interrupt hint: the approval dialog owns the keyboard, and
		// advertising esc here would name a key the dialog has taken over.
		return ThinkingState{Active: true, Phase: ThinkingWaiting, Label: "Waiting for approval"}

	case RuntimeRunningTool:
		ts := ThinkingState{Active: true, Phase: ThinkingTool, Label: "Running", Interruptible: true}
		if tool := latestRunningTool(s); tool != nil {
			ts.Detail = tool.Summary
			if ts.Detail == "" {
				ts.Detail = tool.Name
			}
			ts.StartedAt = tool.StartedAt
		}
		return ts

	case RuntimeStreaming:
		// Text is already visible, so the truthful word is "Responding…",
		// not "Thinking…". The distinction already exists in DeriveRuntime.
		// OutputTokens is the running estimate counted from streamed deltas
		// in State.Apply, so the line reports what arrived, never a guess.
		return ThinkingState{
			Active:        true,
			Phase:         ThinkingModel,
			Label:         "Responding…",
			StartedAt:     s.TurnStartedAt,
			Interruptible: true,
			OutputTokens:  s.OutputTokens,
		}

	case RuntimeWorking:
		return ThinkingState{
			Active:        true,
			Phase:         ThinkingModel,
			Label:         "Thinking…",
			StartedAt:     s.TurnStartedAt,
			Interruptible: true,
		}

	default:
		// Idle and shutting down have no activity to claim. Completion,
		// failure, and cancellation all leave nothing behind, so a stale
		// "Thinking…" can never outlive the turn.
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
	for i := len(s.Entries) - 1; i >= 0; i-- {
		entry := &s.Entries[i]
		if entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.Status == ToolRunning {
			return entry.Tool
		}
	}
	return nil
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
// shimmer wave. It is pure like RenderThinking: tick is supplied rather than
// read, so output is deterministic. Only the Responding label shimmers, so
// Thinking, Running, and Waiting lines keep their stable text. Width handling
// matches RenderThinking; a truncated shimmer degrades to plain text rather
// than overflowing.
func RenderThinkingTick(state ThinkingState, now time.Time, tick int, width int) []string {
	return renderThinking(state, now, tick, true, width)
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
		return []string{formatThinkingLine(z.waiting(thinkingGlyph(state.Phase)), z.waiting(state.Label), "", width, z)}
	}
	// The shimmer wraps only label runes and its stripped width always
	// equals the plain width, so the trailing metadata never shifts across
	// ticks. Styles apply after the text blocks are built, so color codes
	// never enter length math: CellWidth and TruncateToWidth strip them.
	label := state.Label
	if shimmer && state.Phase == ThinkingModel && label == "Responding…" {
		label = z.wave(label, tick)
	} else {
		label = z.active(label)
	}
	content := label
	if state.Detail != "" {
		content += " " + z.active(oneLine(state.Detail))
	}
	// Metadata is one parenthesised block directly above the composer
	// divider: (↓tokens · elapsed · esc). Tokens carry the ↓ prefix so the
	// count reads without colour; elapsed derives from the real start;
	// esc appears only when the runtime proved interruptibility. Unknown
	// stays absent rather than fabricated.
	var metaParts []string
	if state.OutputTokens > 0 {
		metaParts = append(metaParts, z.meta(formatOutputTokens(state.OutputTokens)))
	}
	// Tail is elapsed time plus the interrupt hint. It yields last under
	// truncation because it carries the live affordances.
	if elapsed := elapsedLabel(state.StartedAt, now); elapsed != "" {
		metaParts = append(metaParts, z.tail(elapsed))
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
	return []string{formatThinkingLine(z.active(thinkingGlyph(state.Phase)), content, tail, width, z)}
}

// formatThinkingLine joins glyph, content, and parenthesised tail into one
// compact row: "✳ Responding… · (↓248 tokens · 5s · esc)".
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
// as zero.
func formatOutputTokens(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("↓%d tokens", n)
}

// thinkingGlyph is a static phase marker. It is presentation only: the phase
// is the authority and the glyph merely distinguishes active work (✳) from
// waiting (◇). Animation is deliberately not owned here — the existing
// spinner remains the only animated indicator, so the activity line adds no
// second clock and no independent lifecycle.
func thinkingGlyph(phase ThinkingPhase) string {
	if phase == ThinkingWaiting {
		return "◇"
	}
	return "✳"
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
	return formatDurationText(d)
}
