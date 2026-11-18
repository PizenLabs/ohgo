package tui

import (
	"fmt"
	"time"
)

// Live telemetry bar.
//
// The bar is a projection, never a second state machine: it reads the same
// runtime state as the transcript and the header, and every value it prints is
// either measured or derived from a measured value. It owns one row immediately
// above the composer, and only while a turn is actually in flight: when no
// background work runs, the row disappears entirely so the transcript sits
// directly above the composer.
//
// The bar is intent-only. Duration, token counts, context, session, and path
// belong to the bottom status line; repeating them here would duplicate the
// same facts on two rows.
//
// The existing ProjectThinking seam decides whether a turn is active and which
// phase it is in. This file only turns that phase into the bar's user-facing
// intent phrase and animated marker; it does not re-derive runtime state from
// strings or start a second timer.

// liveTelemetry is the presentation contract for the bar.
//
// Active is false whenever the runtime has nothing in flight; the row is then
// released entirely. Animated reports that the phase is actively producing
// work, so the spinner and the intent label may animate. Parked states
// (waiting for approval) keep a still marker: motion would claim progress the
// runtime does not have.
type liveTelemetry struct {
	Active   bool
	Label    string
	Animated bool
}

// liveTelemetryVisible reports whether the bar owns a row right now.
//
// Layout and rendering both consume this predicate so the reserved row count
// and the painted rows can never disagree: an idle frame renders nothing and
// reserves nothing, and an active frame does both.
func liveTelemetryVisible(s State) bool {
	return projectLiveTelemetry(s).Active
}

// projectLiveTelemetry maps the runtime to the bar's contextual phrase and
// animated marker.
func projectLiveTelemetry(s State) liveTelemetry {
	thinking := ProjectThinking(s)
	if !thinking.Active {
		return liveTelemetry{}
	}
	label := telemetryLabel(s, thinking)
	if label == "" {
		return liveTelemetry{}
	}
	return liveTelemetry{
		Active:   true,
		Label:    label,
		Animated: thinking.Phase != ThinkingWaiting,
	}
}

// telemetryIntentLabel is the coarse phase fallback for the live bar.
//
// The concrete action phrase is derived earlier from runtime evidence
// (telemetry_status.go): a running tool's argument payload or an open thought
// channel. This function names the phase only when that evidence is too thin,
// so the bar never goes blank mid-turn:
// "preparing workspace" is a busy turn that has not produced reasoning or text
// yet; "reasoning internals" requires an open reasoning entry; "executing
// tools" requires a running tool; "formatting response" requires visible
// answer text. The fallback is never inferred from model prose.
func telemetryIntentLabel(s State, thinking ThinkingState) string {
	if s.ProviderWait != nil {
		if label := providerWaitLabel(*s.ProviderWait, s.frameAt); label != "" {
			return label
		}
	}
	switch thinking.Phase {
	case ThinkingWaiting:
		return "waiting for approval"
	case ThinkingTool:
		return "executing tools"
	case ThinkingRespond:
		return "formatting response"
	case ThinkingCompaction:
		return "compacting context"
	case ThinkingCommit:
		if thinking.Label == "execute" {
			return "executing commit"
		}
		return "preparing commit"
	case ThinkingModel:
		if s.openReasoning() >= 0 {
			return "reasoning internals"
		}
		if telemetryReasoningSettled(s) {
			// The trace just became a collapsible Thought block. The bar's
			// phase would otherwise fall back to "preparing workspace" and
			// claim work the transcript now represents. Clearing the content
			// here is the dismissal contract; the reserved row stays put.
			return ""
		}
		return "preparing workspace"
	default:
		return ""
	}
}

// providerWaitLabel names the transport state the loop reported. It never
// guesses from the model or from elapsed wall time: a retry label carries the
// loop's own attempt count and measured backoff.
func providerWaitLabel(w ProviderWait, now time.Time) string {
	kind := providerFailureKind(w.Status)
	switch w.Phase {
	case "requested":
		return "waiting for provider"
	case "retrying":
		delay := w.Delay
		// The event carries the measured backoff, but a static "in 30s"
		// goes stale while the user watches. frameAt is the latest
		// presentation tick; absent a tick, the event time is the best
		// measured anchor.
		if delay > 0 && !w.At.IsZero() && !now.IsZero() {
			if remaining := w.At.Add(delay).Sub(now); remaining > 0 {
				delay = remaining
			} else {
				delay = 0
			}
		}
		if delay > 0 {
			return fmt.Sprintf("%s · retry %d/%d in %s", kind, w.Attempt+1, w.Attempts, formatDurationPrecise(delay))
		}
		return fmt.Sprintf("%s · retry %d/%d", kind, w.Attempt+1, w.Attempts)
	case "failed":
		return kind + " · stopped"
	default:
		return ""
	}
}

// providerFailureKind classifies a provider transport failure by the status the
// provider actually returned. A zero status is a transport failure without a
// response; it is not guessed to be a rate limit.
func providerFailureKind(status int) string {
	switch status {
	case 401, 403:
		return "credential rejected"
	case 408, 504:
		return "provider timeout"
	case 429:
		return "rate limited"
	default:
		if status >= 500 {
			return "provider unavailable"
		}
		return "provider error"
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

// renderLiveTelemetryBar renders the bar at the state's latest presentation
// frame. Duration and token metrics are deliberately absent: they live in the
// bottom status line, and a render never fabricates a duration from wall time.
func renderLiveTelemetryBar(s State, pal *Palette, layout Layout) string {
	return renderLiveTelemetryBarAt(s, pal, layout, int(s.frame))
}

// renderLiveTelemetryBarAt is the deterministic seam: frame is supplied so
// tests can pin the shimmer coordinate. The spinner keeps its own slower
// cadence from the measured tick.
func renderLiveTelemetryBarAt(s State, pal *Palette, layout Layout, frame int) string {
	bar := projectLiveTelemetry(s)
	if !bar.Active {
		return ""
	}
	width := columnWidth(s, layout)
	if width <= 0 {
		return ""
	}
	return composeTelemetryIntent(bar.Label, width, pal, bar.Animated, s.Status.Spinner, frame)
}

// composeTelemetryIntent paints the only content the live bar owns: the marker
// and the contextual intent. The label is measured and cut before styling so
// shimmer escapes cannot overflow the row.
func composeTelemetryIntent(label string, width int, pal *Palette, animated bool, spinnerTick, frame int) string {
	if width <= 0 {
		return ""
	}
	label = sanitizeInline(label)
	labelBudget := width - 2 // spinner plus its separating space
	if labelBudget < 0 {
		labelBudget = 0
	}
	// Cut without an ellipsis: the status contract forbids trailing dots, and
	// the live row itself already signals that work is ongoing.
	label = CellPrefix(label, labelBudget)

	glyph := spinnerGlyph(spinnerTick)
	if !animated {
		glyph = thinkingGlyph(ThinkingWaiting)
	}
	styledGlyph := pal.Prompt.Render(glyph)
	if label == "" {
		return styledGlyph
	}
	styledLabel := pal.Status.Render(label)
	if animated && shimmerSupported() {
		styledLabel = ShimmerTextForTheme(label, frame, themeOf(pal))
	}
	return styledGlyph + " " + styledLabel
}
