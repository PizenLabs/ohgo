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
		return ThinkingState{
			Active:        true,
			Phase:         ThinkingModel,
			Label:         "Responding…",
			StartedAt:     s.TurnStartedAt,
			Interruptible: true,
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
// the timer does, so the duration survives a narrow terminal.
func RenderThinking(state ThinkingState, now time.Time, width int) []string {
	if !state.Active {
		return nil
	}
	head := thinkingGlyph(state.Phase) + " " + state.Label
	if state.Detail != "" {
		head += " " + oneLine(state.Detail)
	}
	tail := ""
	if elapsed := elapsedLabel(state.StartedAt, now); elapsed != "" {
		tail = elapsed
	}
	// The interrupt hint is only ever emitted when the runtime proved the
	// operation is interruptible, and it names the key the existing turn
	// cancellation already uses.
	if state.Interruptible {
		if tail == "" {
			tail = "esc"
		} else {
			tail += " · esc"
		}
	}
	return []string{composeThinkingLine(head, tail, width)}
}

// composeThinkingLine right-aligns the tail (elapsed, interrupt hint) and
// trims the head to whatever the tail leaves, so a long tool summary yields
// before the timer or the hint does.
func composeThinkingLine(head, tail string, width int) string {
	if tail == "" {
		if width > 0 && CellWidth(head) > width {
			return TruncateToWidth(head, width)
		}
		return head
	}
	if width <= 0 {
		return head + "  " + tail
	}
	budget := width - CellWidth(tail)
	if budget < 0 {
		budget = 0
	}
	if CellWidth(head) > budget {
		head = TruncateToWidth(head, budget)
	}
	if gap := budget - CellWidth(head); gap > 0 {
		head += strings.Repeat(" ", gap)
	}
	line := head + tail
	if CellWidth(line) > width {
		line = TruncateToWidth(line, width)
	}
	return line
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
	secs := int(d / time.Second)
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	default:
		return fmt.Sprintf("%dh %dm", secs/3600, (secs%3600)/60)
	}
}
