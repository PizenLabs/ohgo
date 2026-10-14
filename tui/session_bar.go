package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SessionShortcutBar is the fresh-session helper row: the two discovery paths
// that cost nothing and always work. "? help" opens the command palette on an
// empty composer and "^p settings" opens it too, so the hints never advertise
// a key that does something else.
const SessionShortcutBar = "enter send  ·  ? help  ·  ^p settings"

// sessionTelemetry renders the active-session helper row: the measured token
// totals and elapsed time, or "" when nothing has been measured yet.
//
// "enter send" leads so the row keeps teaching the one affordance the composer
// always owns, matching the fresh bar's shape and the contract that the
// composer's hint row is the frame's last line. Unknown counters stay absent
// rather than rendering as "0 in / 0 out": the point of progressive disclosure
// is that a fresh, unmeasured session shows nothing it did not measure.
func sessionTelemetry(state State) string {
	if !state.SessionActive {
		return ""
	}
	var parts []string
	if state.SessionIn > 0 {
		parts = append(parts, "in: "+formatTokens(state.SessionIn))
	}
	if state.SessionOut > 0 {
		parts = append(parts, "out: "+formatTokens(state.SessionOut))
	}
	if state.SessionElapsed > 0 {
		parts = append(parts, formatDurationText(state.SessionElapsed))
	}
	if len(parts) == 0 {
		return ""
	}
	return "enter send · " + strings.Join(parts, " · ")
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
