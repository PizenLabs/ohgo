package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/tokens"
)

// SessionShortcutBar is the right zone of the telemetry footer: the two
// discovery paths that cost nothing and always work. "? help" opens the
// Command & Help Center on an empty composer and "^p settings" opens it too, so
// the hints never advertise a key that does something else.
const SessionShortcutBar = "? help · ^p settings"

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

// busyTelemetry renders the live token/count zone for the busy footer: the
// incrementally updating metrics (↑/↓ with live % and measured cost) plus
// the path:branch worktree orientation with its compact dirty state, but
// never the session marker.
//
// The existing chrome contract parks the session marker while a turn runs —
// the hint row belongs to the queue/cancel keys until the turn ends — so the
// busy row must not reintroduce "#N". Path and git status stay because
// orientation must not disappear mid-run. Hiding only "#N" keeps that
// contract while still fixing the stale-statusline defect: ↑/↓/% now grow on
// every AssistantDelta via liveSessionOut/livePercent instead of waiting for
// the terminal completion hook.
func busyTelemetry(state State) string {
	if !state.SessionActive {
		return ""
	}
	var parts []string
	if state.SessionIn > 0 {
		parts = append(parts, "↑"+formatTokens(state.SessionIn))
	}
	if liveOut := liveSessionOut(state); liveOut > 0 {
		out := "↓" + formatTokens(liveOut)
		if pct := livePercent(state); pct >= 0 {
			out += fmt.Sprintf(" (%d%%)", pct)
		}
		parts = append(parts, out)
	} else if pct := livePercent(state); pct >= 0 {
		parts = append(parts, fmt.Sprintf("(%d%%)", pct))
	}
	if usage := sanitizeInline(strings.TrimSpace(state.Status.UsageText)); usage != "" {
		parts = append(parts, usage)
	}
	if path := sessionPathBranch(state); path != "" {
		parts = append(parts, path)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// sessionTelemetry renders the left zone of the telemetry footer: measured
// session metrics and context, or "" when nothing has been measured yet.
//
// Format is "↑<in> · ↓<out> (<ctx%>) · <cost> · <session> ·
// <path>:<branch> [+<staged> !<modified> ?<untracked>]",
// with unknown counters absent rather than "0". The arrows read without
// colour, the parenthesised context budget ties to the output count it
// scopes, and the trailing session plus path:branch plus compact worktree
// state orients. A fresh,
// unmeasured session shows nothing it did not measure here; the minimal
// path-only row is owned by the hint bar, not here.
func sessionTelemetry(state State) string {
	if !state.SessionActive {
		return ""
	}
	var parts []string
	if state.SessionIn > 0 {
		parts = append(parts, "↑"+formatTokens(state.SessionIn))
	}
	if liveOut := liveSessionOut(state); liveOut > 0 {
		out := "↓" + formatTokens(liveOut)
		if pct := livePercent(state); pct >= 0 {
			out += fmt.Sprintf(" (%d%%)", pct)
		}
		parts = append(parts, out)
	} else if pct := livePercent(state); pct >= 0 {
		parts = append(parts, fmt.Sprintf("(%d%%)", pct))
	}
	if strings.TrimSpace(state.Status.UsageText) != "" {
		parts = append(parts, sanitizeInline(strings.TrimSpace(state.Status.UsageText)))
	}
	if session := sanitizeInline(strings.TrimSpace(state.Status.Session)); session != "" {
		parts = append(parts, session)
	}
	if path := sessionPathBranch(state); path != "" {
		parts = append(parts, path)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
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

// sessionBarRow joins the left telemetry zone and the right shortcut zone
// into one footer row. When width is positive the right zone pins to the far
// edge with blank padding; when the row cannot fit, the right yields first so
// measured telemetry (token counts, cost) survives over shortcuts, and the
// whole row truncates to width rather than wrapping. Non-positive widths join
// with " · " and never pad, for tests and unknown viewports.
func sessionBarRow(left, right string, width int) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" {
		if width > 0 {
			return TruncateToWidth(right, width)
		}
		return right
	}
	if right == "" {
		if width > 0 {
			return TruncateToWidth(left, width)
		}
		return left
	}
	if width <= 0 {
		return left + " · " + right
	}
	gap := width - CellWidth(left) - CellWidth(right)
	if gap < 2 {
		// Too narrow for a split: the left wins so token/cost telemetry
		// stays visible in split-pane minimum sizes. Keep the left whole
		// plus a truncated right when the "? help" head still fits;
		// otherwise the left alone wins over a sliver, and truncation of
		// the left (which keeps its token-first head) is the last resort.
		sep := " · "
		if CellWidth(left)+CellWidth(sep) < width && width-CellWidth(left)-CellWidth(sep) >= CellWidth("? help") {
			return left + sep + TruncateToWidth(right, width-CellWidth(left)-CellWidth(sep))
		}
		return TruncateToWidth(left, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// idleHintZones reports the two footer zones for an idle composer: left
// session metrics and context, right shortcuts.
//
// Fresh sessions (no prompt, no recallable history) show minimal path-only
// left with the full "? help · ^p settings" right, because ^p genuinely opens
// settings there. Once a prompt exists the left reveals full telemetry and the
// right narrows to "? help": ^p returns to history recall, so advertising
// settings would lie (see app.go ^p handling). Unknown counters stay absent;
// an empty left keeps the footer clean rather than inventing zeros.
func idleHintZones(state State) (left, right string) {
	if telemetry := sessionTelemetry(state); telemetry != "" {
		return telemetry, "? help"
	}
	if state.SessionActive || len(state.Submitted) > 0 {
		return "", "? help"
	}
	// Fresh and unmeasured: only what is present and essential — session,
	// measured cost, path — never invented zeros and never a 0% budget.
	var parts []string
	if usage := sanitizeInline(strings.TrimSpace(state.Status.UsageText)); usage != "" {
		parts = append(parts, usage)
	}
	if session := sanitizeInline(strings.TrimSpace(state.Status.Session)); session != "" {
		parts = append(parts, session)
	}
	if path := sessionPathBranch(state); path != "" {
		parts = append(parts, path)
	}
	return strings.Join(parts, " · "), SessionShortcutBar
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
// a minute, then minutes and seconds, then hours and minutes. It is the single
// duration formatter shared by the activity line's timer and the session bar.
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
