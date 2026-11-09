package tui

import "time"

// Runtime activity ownership.
//
// The runtime has exactly one current activity, but it has two legitimate
// presentations: an entry that is already in the transcript (a running tool or
// an open reasoning trace), or the fixed telemetry bar above the composer for
// phases with no concrete entry of their own. ProjectActivity answers which
// concrete action surface owns the detail; the bar always owns the semantic
// phase and measured metrics (telemetry_bar.go). Surfaces ask the projections;
// none of them re-derives the choice from strings or from its own copy of the
// runtime state.
//
// The bar's slot is reserved even while idle. Its content appears and clears
// with the turn, but its row never does, so a phase hand-off cannot move the
// composer or the transcript. Concrete action detail stays in its entry: a
// running tool keeps its summary, and an open Thought trace keeps its prose.
//
// The prompt boundary owns the workspace role. Neither projection carries it,
// so "BUILD" cannot appear once as a prompt and again as a telemetry line for
// the same turn.

// ActivitySurface names the one surface that presents the current activity.
type ActivitySurface int

const (
	// ActivityNone means no surface presents the activity. The runtime is idle,
	// an overlay owns the screen, or a dialog already represents the activity.
	ActivityNone ActivitySurface = iota
	// ActivityLine means no transcript entry owns the concrete action, so the
	// telemetry bar's semantic phase is the only live presentation.
	ActivityLine
	// ActivityEntry means an existing transcript/live entry owns the activity.
	ActivityEntry
)

// String implements fmt.Stringer for diagnostics and test failures.
func (s ActivitySurface) String() string {
	switch s {
	case ActivityLine:
		return "line"
	case ActivityEntry:
		return "entry"
	default:
		return "none"
	}
}

// ActivityProjection is the authoritative answer to "what is happening, and
// which single element shows it?".
//
// It is derived from State on every call and never stored: a stored projection
// would drift from the entries that actually own the activity. EntryIndex is
// the owning entry position only when Surface is ActivityEntry.
type ActivityProjection struct {
	// Thinking is the semantic projection, whether or not a line renders it.
	// Tests and non-visual consumers can inspect it without presenting it.
	Thinking ThinkingState
	// Surface is the one element that presents the activity.
	Surface ActivitySurface
	// EntryIndex is the owning entry, or -1 when no entry owns it.
	EntryIndex int
}

// ProjectActivity decides which surface owns the current activity.
//
// Precedence follows the runtime's own precedence:
//
//  1. A running tool entry owns the activity. Its badge already shows the
//     spinner, the argument-derived action, and the lifecycle word.
//  2. An open reasoning trace owns the activity. Its collapsed head already
//     shows that the model is thinking and for how long.
//  3. Otherwise the activity line owns it.
//
// Overlays and dialogs suppress the activity line because they own the
// keyboard and the screen; their own body is the representation for that
// moment. They never create a second activity surface.
func ProjectActivity(s State) ActivityProjection {
	thinking := ProjectThinking(s)
	if !thinking.Active {
		return ActivityProjection{Thinking: thinking, EntryIndex: -1}
	}
	if index := s.activityEntryIndex(thinking); index >= 0 {
		return ActivityProjection{
			Thinking:   thinking,
			Surface:    ActivityEntry,
			EntryIndex: index,
		}
	}
	if activityDialogOwns(s) {
		return ActivityProjection{Thinking: thinking, EntryIndex: -1}
	}
	return ActivityProjection{Thinking: thinking, Surface: ActivityLine, EntryIndex: -1}
}

// activityEntryIndex returns the entry that already represents the activity,
// or -1. A tool call outranks a reasoning trace because the runtime itself
// outranks execution over generation: a running tool means the model already
// finished thinking.
func (s State) activityEntryIndex(thinking ThinkingState) int {
	if thinking.Phase == ThinkingTool {
		if index := latestRunningToolIndex(s); index >= 0 {
			return index
		}
	}
	if index := s.openReasoning(); index >= 0 {
		return index
	}
	return -1
}

// activityDialogOwns reports whether a modal or dialog already presents the
// runtime state. These surfaces capture the keyboard, so a second activity
// line underneath them would both duplicate the fact and advertise keys the
// dialog has taken over.
func activityDialogOwns(s State) bool {
	return s.Pending != nil ||
		s.QuitConfirm != nil ||
		s.Transition != nil ||
		s.UpdateRestart != nil ||
		s.PostUpdate != nil ||
		s.Picker != nil ||
		s.Plane != nil ||
		s.SupportPanel != nil ||
		s.Permissions != nil ||
		s.StatusModal != nil ||
		s.UsageModal != nil ||
		s.InfoModal != nil ||
		s.InformationModal != nil ||
		s.SessionModal != nil ||
		s.HelpModal != nil ||
		s.SettingsModal != nil ||
		s.ConnectModal != nil ||
		s.ConnectKey != nil ||
		s.Diff != nil
}

// HeaderPhase reports the runtime phase the header may name.
//
// An active phase is already named by exactly one activity surface: the
// activity line, a running tool entry, an open reasoning entry, or the dialog
// that owns the screen. Printing it again in the header would make the same
// fact appear twice. The header therefore keeps only the resting phases it
// alone can name: idle and shutting down.
func HeaderPhase(s State) (RuntimeState, bool) {
	switch runtime := DeriveRuntime(s); runtime {
	case RuntimeIdle, RuntimeShuttingDown:
		return runtime, true
	default:
		return runtime, false
	}
}

// ActivityLineText renders the generic one-row activity projection when no
// entry owns the concrete action. The workspace bar uses projectLiveTelemetry
// for its semantic phrase; this helper remains the single semantic line
// projection for callers that own a status row and need the detailed phase.
func ActivityLineText(s State, now time.Time, tick int, width int) string {
	activity := ProjectActivity(s)
	if activity.Surface != ActivityLine {
		return ""
	}
	lines := RenderThinkingFrame(activity.Thinking, now, tick, width)
	if shimmerSupported() {
		lines = RenderThinkingTick(activity.Thinking, now, tick, width)
	}
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}
