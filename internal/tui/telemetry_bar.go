package tui

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
		Active:   true,
		Label:    label,
		Animated: thinking.Phase != ThinkingWaiting,
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
	case ThinkingCompaction:
		return "compacting context…"
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

// renderLiveTelemetryBar renders the bar at the state's latest presentation
// tick. Duration and token metrics are deliberately absent: they live in the
// bottom status line, and a render never fabricates a duration from wall time.
func renderLiveTelemetryBar(s State, pal *Palette, layout Layout) string {
	return renderLiveTelemetryBarAt(s, pal, layout, s.Status.Spinner)
}

// renderLiveTelemetryBarAt is the deterministic seam: tick is supplied so tests
// can pin the shimmer frame.
func renderLiveTelemetryBarAt(s State, pal *Palette, layout Layout, tick int) string {
	bar := projectLiveTelemetry(s)
	if !bar.Active {
		return ""
	}
	width := columnWidth(s, layout)
	if width <= 0 {
		return ""
	}
	return composeTelemetryIntent(bar.Label, width, pal, bar.Animated, tick)
}

// composeTelemetryIntent paints the only content the live bar owns: the marker
// and the semantic intent. The label is measured and truncated before styling
// so ShimmerText escapes cannot overflow the row.
func composeTelemetryIntent(label string, width int, pal *Palette, animated bool, tick int) string {
	if width <= 0 {
		return ""
	}
	label = sanitizeInline(label)
	labelBudget := width - 2 // spinner plus its separating space
	if labelBudget < 0 {
		labelBudget = 0
	}
	label = TruncateToWidth(label, labelBudget)

	glyph := spinnerGlyph(tick)
	if !animated {
		glyph = thinkingGlyph(ThinkingWaiting)
	}
	styledGlyph := pal.Prompt.Render(glyph)
	if label == "" {
		return styledGlyph
	}
	styledLabel := pal.Status.Render(label)
	if animated && shimmerSupported() {
		styledLabel = ShimmerTextForTheme(label, tick, themeOf(pal))
	}
	return styledGlyph + " " + styledLabel
}
