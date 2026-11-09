package tui

import (
	"strings"
	"time"
)

// Live telemetry bar.
//
// The bar is a projection, never a second state machine: it reads the same
// runtime state as the transcript and the header, and every value it prints is
// either measured or derived from a measured value. It owns exactly one row,
// reserved immediately above the composer for the life of the view, so a turn
// starting, changing phase, or finishing can never move the composer or the
// transcript. Idle frames render the slot empty rather than removing it.
//
// The existing ProjectThinking seam decides whether a turn is active and which
// phase it is in. This file only turns that phase into the bar's user-facing
// intent phrase and its right-aligned metrics; it does not re-derive runtime
// state from strings or start a second timer.

// liveTelemetry is the presentation contract for the bar.
//
// Active is false whenever the runtime has nothing in flight; the row is still
// reserved by the layout, but renders blank. StartedAt is the measured origin
// of the displayed duration, and is zero when the runtime never stamped one.
type liveTelemetry struct {
	Active    bool
	Phase     ThinkingPhase
	Label     string
	StartedAt time.Time
	// OutputTokens is the live streamed-output estimate carried by the
	// activity projection. Zero means the volume is unknown and stays absent.
	OutputTokens int
	// Animated reports that the phase is actively producing work, so the
	// spinner and the intent label may animate. Parked states (waiting for
	// approval) keep a still marker: motion would claim progress the runtime
	// does not have.
	Animated bool
}

// projectLiveTelemetry maps the runtime to the bar's semantic phrase and
// measured anchors.
func projectLiveTelemetry(s State) liveTelemetry {
	thinking := ProjectThinking(s)
	if !thinking.Active {
		return liveTelemetry{}
	}
	label := telemetryIntentLabel(s, thinking)
	if label == "" {
		return liveTelemetry{}
	}
	return liveTelemetry{
		Active:       true,
		Phase:        thinking.Phase,
		Label:        label,
		StartedAt:    telemetryStart(thinking),
		OutputTokens: thinking.OutputTokens,
		Animated:     thinking.Phase != ThinkingWaiting,
	}
}

// telemetryIntentLabel names what the runtime is doing in user-facing terms.
//
// The labels describe the phase the runtime can actually prove: "preparing
// workspace" is a busy turn that has not produced reasoning or text yet;
// "reasoning internals" requires an open reasoning entry; "executing tools"
// requires a running tool; "formatting response" requires visible answer text.
// No label is inferred from model prose or a tool name.
func telemetryIntentLabel(s State, thinking ThinkingState) string {
	switch thinking.Phase {
	case ThinkingWaiting:
		return "waiting for approval…"
	case ThinkingTool:
		return "executing tools…"
	case ThinkingRespond:
		return "formatting response…"
	case ThinkingCommit:
		if thinking.Label == "execute" {
			return "executing commit…"
		}
		return "preparing commit…"
	case ThinkingModel:
		if s.openReasoning() >= 0 {
			return "reasoning internals…"
		}
		if telemetryReasoningSettled(s) {
			// The trace just became a collapsible Thought block. The bar's
			// phase would otherwise fall back to "preparing workspace…" and
			// claim work the transcript now represents. Clearing the content
			// here is the dismissal contract; the reserved row stays put.
			return ""
		}
		return "preparing workspace…"
	default:
		return ""
	}
}

// telemetryReasoningSettled reports that the trailing entry is a reasoning
// trace which has closed into its collapsible presentation and no later entry
// has taken over the phase.
func telemetryReasoningSettled(s State) bool {
	if len(s.Entries) == 0 {
		return false
	}
	last := s.Entries[len(s.Entries)-1]
	return last.Kind == EntryReasoning && last.Reasoning != nil && last.Reasoning.Closed
}

// telemetryStart selects the measured origin of the bar's elapsed clock.
//
// A turn-anchored phase (reasoning, tools, response, approval) shows the whole
// turn's age, so the bar does not reset at every hand-off. The commit workflow
// runs while the agent turn is idle, so it uses its own start; using the
// previous turn's stamp there would print a false duration.
func telemetryStart(thinking ThinkingState) time.Time {
	if thinking.Phase == ThinkingCommit || thinking.TurnStartedAt.IsZero() {
		return thinking.StartedAt
	}
	return thinking.TurnStartedAt
}

// renderLiveTelemetryBar renders the bar at the state's latest presentation
// tick. It deliberately does not fall back to wall time: a one-shot Render
// without a frame clock must stay deterministic and omit the elapsed value
// rather than fabricate a duration from the moment the test happened to run.
func renderLiveTelemetryBar(s State, pal *Palette, layout Layout) string {
	return renderLiveTelemetryBarAt(s, pal, layout, s.frameAt, s.Status.Spinner)
}

// renderLiveTelemetryBarAt is the deterministic seam: now and tick are supplied
// so tests can pin both the elapsed value and the shimmer frame.
func renderLiveTelemetryBarAt(s State, pal *Palette, layout Layout, now time.Time, tick int) string {
	bar := projectLiveTelemetry(s)
	if !bar.Active {
		return ""
	}
	width := columnWidth(s, layout)
	if width <= 0 {
		return ""
	}
	metrics := telemetryMetrics(bar, now)
	return composeTelemetryRow(bar.Label, metrics, width, pal, bar.Animated, tick)
}

// telemetryMetrics renders the right-pinned performance zone: total turn
// elapsed time and, once text is streaming, the compact output volume with its
// downward arrow. Unknown values stay absent rather than rendering as invented
// zeroes. The separator is part of the right group so the middle remains pure
// whitespace.
func telemetryMetrics(bar liveTelemetry, now time.Time) string {
	var parts []string
	if !now.IsZero() && !bar.StartedAt.IsZero() {
		elapsed := now.Sub(bar.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		parts = append(parts, formatDurationPrecise(elapsed))
	}
	if tokens := formatOutputTokens(bar.OutputTokens); tokens != "" {
		parts = append(parts, tokens)
	}
	return strings.Join(parts, " · ")
}

// composeTelemetryRow lays out the left group, a pure-whitespace spacer, and
// the right-pinned metrics in exactly width cells.
//
// Width math runs on plain text before any styling: the spinner is one cell,
// the intent label keeps its stripped width even while ShimmerText adds colour
// escapes, and the metrics are measured before they are painted. That is what
// keeps the right block rigidly pinned across shimmer frames.
//
// The metrics survive truncation. On a narrow terminal the intent label yields
// first; if even the spinner cannot fit beside the metrics, the left group is
// dropped rather than letting the right block wrap or detach from the edge.
func composeTelemetryRow(label, metrics string, width int, pal *Palette, animated bool, tick int) string {
	if width <= 0 {
		return ""
	}
	label = sanitizeInline(label)
	metrics = sanitizeInline(metrics)

	// The metrics are the fixed anchor: never let the left group push them
	// off the edge. Truncating here is a last resort for a terminal narrower
	// than the metrics themselves.
	metrics = TruncateToWidth(metrics, width)
	metricsW := CellWidth(metrics)
	remaining := width - metricsW
	if remaining <= 0 {
		return pal.Muted.Render(metrics)
	}

	// Left group budget: marker, one separating space, then as much of the
	// intent label as fits while leaving at least one pure-whitespace spacer
	// cell before the metrics whenever space permits.
	labelBudget := remaining - 3
	if labelBudget < 0 {
		labelBudget = 0
	}
	label = TruncateToWidth(label, labelBudget)
	labelW := CellWidth(label)

	glyph := spinnerGlyph(tick)
	if !animated {
		glyph = thinkingGlyph(ThinkingWaiting)
	}
	styledGlyph := pal.Prompt.Render(glyph)
	styledMetrics := ""
	if metrics != "" {
		styledMetrics = pal.Muted.Render(metrics)
	}

	if label == "" {
		// No sentence fits, but the spinner still names liveness when it can.
		if remaining >= 1 {
			gap := remaining - 1
			return styledGlyph + strings.Repeat(" ", gap) + styledMetrics
		}
		return styledMetrics
	}

	styledLabel := pal.Status.Render(label)
	if animated && shimmerSupported() {
		styledLabel = ShimmerText(label, tick)
	}
	leftW := 1 + 1 + labelW // glyph + separator + label
	gap := width - leftW - metricsW
	if gap < 0 {
		gap = 0
	}
	return styledGlyph + " " + styledLabel + strings.Repeat(" ", gap) + styledMetrics
}
