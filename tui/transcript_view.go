package tui

import (
	"strconv"
	"strings"
)

// Retained transcript projection.
//
// TranscriptLayout owns geometry; the viewport owns scroll position; this file
// binds them into the rows a single frame paints. The interactive program keeps
// one transcriptFrame between updates so scrolling only slices cached rows and
// streaming only re-renders the block that changed.

// transcriptFrame is one resolved viewport plus the layout it indexes.
type transcriptFrame struct {
	index *TranscriptLayout
	vp    Viewport
}

// transcriptBudget is the number of rows the transcript area may paint:
// the conversation allocation minus any approval dialog sharing that budget.
func transcriptBudget(state State, layout Layout) int {
	if state.Height <= 0 {
		return 0
	}
	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	return avail
}

// newTranscriptFrame builds a one-shot layout and resolves the viewport from
// the state's scroll fields.
func newTranscriptFrame(state State, layout Layout) *transcriptFrame {
	index := &TranscriptLayout{}
	index.Refresh(&state, layout)
	tracker := &ViewportTracker{}
	scroll := state.scrollIntent
	if scroll == 0 && state.Scroll > 0 {
		// Direct callers (tests, fixtures) set the mirror without going through
		// a ScrollMoved event. Honour it rather than silently following.
		scroll = state.Scroll
	}
	vp := tracker.Update(state.Follow, scroll, state.ScrollSeq, transcriptBudget(state, layout), index)
	return &transcriptFrame{index: index, vp: vp}
}

// renderTranscriptArea writes exactly the transcript budget as rows.
func renderTranscriptArea(sb *strings.Builder, state State, pal *Palette, layout Layout, frame *transcriptFrame) {
	if state.Height <= 0 || (layout.Mode == LayoutWide && state.Width <= 0) {
		renderTranscriptUnbounded(sb, state, pal, layout, frame)
		return
	}
	if len(state.Entries) == 0 && showWelcome(state) {
		renderWelcomeArea(sb, state, pal, layout)
		return
	}
	if frame == nil {
		frame = newTranscriptFrame(state, layout)
	}
	budget := transcriptBudget(state, layout)
	if budget <= 0 {
		return
	}
	if frame.vp.Marker && budget > 0 {
		above := frame.vp.ScrollAbove
		if above < 1 {
			above = 1
		}
		marker := "↑ " + strconv.Itoa(above) + " lines above — end to follow"
		sb.WriteString(pal.Muted.Render(TruncateToWidth(marker, displayWidth(state, layout))) + "\n")
		budget--
	}
	rows := frame.index.Rows(frame.vp.Offset, budget)
	for _, row := range rows {
		styled := ApplySelectionHighlight(frame.index, state.Selection, row)
		sb.WriteString(styled + "\n")
	}
	for i := len(rows); i < budget; i++ {
		sb.WriteString("\n")
	}
}

// renderTranscriptUnbounded is the unknown-size path: no viewport, no slicing.
// It uses the full projection so its output is byte-for-byte the historical
// no-size frame; there is no screen height against which to anchor a viewport.
func renderTranscriptUnbounded(sb *strings.Builder, state State, pal *Palette, layout Layout, _ *transcriptFrame) {
	if len(state.Entries) == 0 && showWelcome(state) {
		body := renderWelcome(state, pal, layout)
		sb.WriteString(body)
		if body != "" && !strings.HasSuffix(body, "\n") {
			sb.WriteString("\n")
		}
		return
	}
	body := renderTranscriptBody(state, pal, layout)
	sb.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		sb.WriteString("\n")
	}
}

// renderWelcomeArea keeps the historical slice/pad contract for the identity
// surface: it is chrome, not a transcript block, and has no scroll state.
func renderWelcomeArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	lines := splitLines(renderWelcome(state, pal, layout))
	budget := transcriptBudget(state, layout)
	if budget <= 0 {
		return
	}
	if len(lines) <= budget {
		for _, line := range lines {
			sb.WriteString(line + "\n")
		}
		for i := len(lines); i < budget; i++ {
			sb.WriteString("\n")
		}
		return
	}
	// The welcome card can be taller than a short transcript budget. It is
	// fixed chrome, never history, so show its bottom: the identity and the
	// key hints live there, and there is no stale scroll offset to honour.
	start := len(lines) - budget
	if start < 0 {
		start = 0
	}
	for _, line := range lines[start:] {
		sb.WriteString(line + "\n")
	}
}
