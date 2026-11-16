package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/tokens"
)

// liveSessionOut reports the output count the footer must show right now:
// measured session total plus the in-flight estimate while a turn runs.
//
// The in-flight part is the same estimate the activity line already shows
// (State.OutputTokens, recomputed from streamed text on every delta), so the
// footer and the activity line agree on one number without a second counting
// path. Idle returns the measured total unchanged.
func liveSessionOut(state State) int {
	if state.Status.Busy && state.OutputTokens > 0 {
		return state.SessionOut + state.OutputTokens
	}
	return state.SessionOut
}

// liveContextUsed reports the context size the footer must show right now:
// the last measured budget plus the in-flight output estimate while a turn
// runs. The provider only reports usage at turn completion, so without this
// the percentage would sit stale at the previous turn's value for the whole
// streaming phase and jump only when the turn ends. Idle returns the measured
// value unchanged.
func liveContextUsed(state State) int {
	if state.Status.Busy && state.OutputTokens > 0 {
		return state.Status.ContextUsed + state.OutputTokens
	}
	return state.Status.ContextUsed
}

// livePercent renders the context budget including in-flight output while a
// turn runs, or -1 when there is no window. It mirrors Status.Percent but on
// the live context size, so the footer and the live status line track the
// streaming estimate instead of the stale completed-turn value.
func livePercent(state State) int {
	if state.Status.ContextWindow <= 0 {
		return -1
	}
	return tokens.Percent(liveContextUsed(state), state.Status.ContextWindow)
}

// turnElapsedLabel renders the measured turn duration that leads the bottom
// status line. It appears once the session has run a prompt and keeps the
// locked final value after settlement, so idle observation still shows the
// exact execution time. A fresh session keeps the row clean.
func turnElapsedLabel(state State) string {
	if !state.SessionActive && state.TurnElapsed <= 0 {
		return ""
	}
	return "⏱ " + formatTurnElapsed(state.TurnElapsed)
}

// formatTurnElapsed renders the bottom timer: a fresh turn reads "0s" rather
// than "0ms", then delegates to the shared precise formatter for measured
// spans (420ms, 4.5s, 1m 12s).
func formatTurnElapsed(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return formatDurationPrecise(d)
}

// withTurnElapsed prefixes the timer to a bottom status line without inventing
// a separator when the line is otherwise empty. The two-space gap matches the
// status line's metric grouping rather than the " · " used between metrics.
func withTurnElapsed(state State, text string) string {
	timer := turnElapsedLabel(state)
	if timer == "" {
		return text
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return timer
	}
	return timer + "  " + text
}

// measuredStatusText renders the measured telemetry zone as one unbounded
// string. The footer itself renders the structured projection, so this helper
// exists for callers that need a single string (the live overview and the
// incremental-growth tests); it never re-derives an order of its own.
//
// The timer is a turn-level anchor the footer owns, and the session marker
// parks while a turn runs, so both are explicit choices rather than accidents
// of concatenation.
func measuredStatusText(state State, includeTurn bool) string {
	if !state.SessionActive {
		return ""
	}
	projection := projectStatusBar(state)
	items := make([]statusItem, 0, len(projection.items))
	for _, item := range projection.items {
		if !item.visible {
			continue
		}
		switch item.field {
		case statusKindDuration:
			continue
		case statusKindTurn:
			if !includeTurn {
				continue
			}
		}
		items = append(items, item)
	}
	return joinStatusItems(items)
}

// busyTelemetry renders the live token/count zone for the busy footer: the
// incrementally updating metrics plus the worktree orientation, but never the
// session marker the chrome contract parks during a turn.
func busyTelemetry(state State) string {
	return measuredStatusText(state, false)
}

// sessionTelemetry renders the measured idle telemetry zone, or "" when the
// session has measured nothing yet. Unknown counters stay absent rather than
// rendering as fabricated zeros.
func sessionTelemetry(state State) string {
	return measuredStatusText(state, true)
}

// gitCompactStatus renders the measured worktree state compactly for the
// one-row footer: "+<staged> !<modified> ?<untracked>", skipping zero
// buckets. Unknown stays absent rather than a fabricated "clean", and a
// measured clean tree renders "" so the footer keeps only cwd:branch without
// noise. The markers read without colour: "+" names staged index changes,
// "!" the unstaged modifications, "?" the untracked files.
func gitCompactStatus(status Status) string {
	if !status.GitKnown {
		return ""
	}
	var parts []string
	if status.GitStaged > 0 {
		parts = append(parts, "+"+strconv.Itoa(status.GitStaged))
	}
	if status.GitModified > 0 {
		parts = append(parts, "!"+strconv.Itoa(status.GitModified))
	}
	if status.GitUntracked > 0 {
		parts = append(parts, "?"+strconv.Itoa(status.GitUntracked))
	}
	return strings.Join(parts, " ")
}

// sessionPathBranchWidth is sessionPathBranch with an explicit cwd budget,
// so the narrow modal chrome (16 cells) and the hint bar (24 cells) share
// one branch-plus-status rule instead of each re-deriving it.
func sessionPathBranchWidth(state State, maxCwd int) string {
	if maxCwd < 1 {
		maxCwd = 1
	}
	cwd := shortenCwd(sanitizeInline(strings.TrimSpace(state.Status.Cwd)), maxCwd)
	branch := sanitizeInline(strings.TrimSpace(state.Status.GitBranch))
	var base string
	switch {
	case cwd == "" && branch == "":
		base = ""
	case cwd == "":
		base = branch
	case branch == "":
		base = cwd
	default:
		base = cwd + ":" + branch
	}
	if git := gitCompactStatus(state.Status); git != "" {
		if base == "" {
			return git
		}
		return base + " " + git
	}
	return base
}

// sessionPathBranch reports the essential workspace orientation for the
// footer: shortened cwd plus ":branch" when a branch is known, with the
// compact worktree state appended when measured and dirty. Empty when
// neither is known, so the footer stays clean rather than rendering a
// placeholder. Both arrive from the environment and are sanitized inline so
// the one-row footer never wraps or splits.
func sessionPathBranch(state State) string {
	return sessionPathBranchWidth(state, 24)
}

// sessionBarRow joins the telemetry zone and the contextual action rail into
// one footer row. When width is positive and both fit with a clean gap, the
// rail pins to the far edge with blank padding.
//
// The rail is atomic: a clipped "alt+enter bre…" teaches nothing, so when the
// gap is too small the rail yields entirely and the telemetry keeps the row.
// Non-positive widths join with " · " and never pad, for tests and unknown
// viewports.
func sessionBarRow(left, right string, width int) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if width <= 0 {
		switch {
		case left == "":
			return right
		case right == "":
			return left
		default:
			return left + " · " + right
		}
	}
	if left == "" {
		// A rail that does not fit is removed whole rather than cut short.
		if CellWidth(right) <= width {
			return right
		}
		return ""
	}
	if right == "" {
		return TruncateToWidth(left, width)
	}
	if gap := width - CellWidth(left) - CellWidth(right); gap >= 2 {
		return left + strings.Repeat(" ", gap) + right
	}
	return TruncateToWidth(left, width)
}

// formatTokens renders a token count compactly with one decimal under a
// million, trimming a zero fraction so 1000 reads "1k" while 1200 reads
// "1.2k". It is presentation only: the stored count is never rounded.
func formatTokens(n int) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n >= 1_000_000:
		return trimZeroFraction(fmt.Sprintf("%.1f", float64(n)/1_000_000)) + "M"
	case n >= 1_000:
		return trimZeroFraction(fmt.Sprintf("%.1f", float64(n)/1_000)) + "k"
	default:
		return strconv.Itoa(n)
	}
}

// trimZeroFraction drops a trailing ".0" so "1.0" reads "1" without changing a
// non-zero fraction.
func trimZeroFraction(s string) string {
	return strings.TrimSuffix(s, ".0")
}

// formatDurationText renders a non-negative duration compactly: seconds under
// a minute, then minutes and seconds, then hours and minutes. It is the coarse
// formatter for activity timers and the session bar; transcript badges use
// formatDurationPrecise, which adds milliseconds and tenths.
func formatDurationText(d time.Duration) string {
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

// formatDurationPrecise renders a measured span for transcript badges. Fast
// work stays in milliseconds, ordinary work keeps one decimal so "1.1s" and
// "6.5s" are visible, and long work collapses to the compact vocabulary. It is
// the one formatter for thought and tool durations: two implementations of the
// same badge would drift.
func formatDurationPrecise(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Round(time.Millisecond).Milliseconds())
	}
	if d < 10*time.Second {
		if d%time.Second == 0 {
			return fmt.Sprintf("%ds", int(d/time.Second))
		}
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return formatDurationText(d)
}
