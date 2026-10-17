package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A fresh session shows the shortcut bar and no token counters: there is
// nothing measured to report, so reporting "↑0 / ↓0" would be a lie.
func TestSessionBarHiddenOnFreshSession(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})

	out := Render(s)
	if strings.Contains(out, "in: ") || strings.Contains(out, "out: ") {
		t.Fatalf("fresh session showed token counters:\n%s", out)
	}
	if strings.Contains(out, "↑") || strings.Contains(out, "↓") {
		t.Fatalf("fresh session showed measured telemetry:\n%s", out)
	}
	if !strings.Contains(out, "? help") || !strings.Contains(out, "^p settings") {
		t.Fatalf("fresh session lost the shortcut bar:\n%s", out)
	}
	if got := composerHintsFor(s); got != SessionShortcutBar {
		t.Errorf("fresh helper row = %q, want the shortcut bar", got)
	}
}

// The first executed prompt flips the helper row to measured telemetry, and
// every segment reflects exactly what the runtime reported. Single-canvas:
// left metrics (↑in · ↓out) plus right shortcuts (? help) in two zones.
func TestSessionTelemetryVisibleAfterFirstRun(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(AssistantDone{})
	s.Apply(SessionMetricsUpdated{Input: 1200, Output: 340, Elapsed: 3 * time.Second})

	row := composerHintsFor(s)
	for _, want := range []string{"↑1.2k", "↓340", "? help"} {
		if !strings.Contains(row, want) {
			t.Errorf("telemetry row = %q, want %q", row, want)
		}
	}
	for _, stale := range []string{"in: ", "out: ", "enter send", "^p settings"} {
		if strings.Contains(row, stale) {
			t.Errorf("telemetry row = %q, want no stale %q (left/right split)", row, stale)
		}
	}
	if out := Render(s); !strings.Contains(out, "↑1.2k") {
		t.Fatalf("rendered frame lost the token counter:\n%s", out)
	}
}

// /clear resets progressive disclosure: the counters disappear and the clean
// shortcut state returns.
func TestSessionBarClearedWithTranscript(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(AssistantDone{})
	s.Apply(SessionMetricsUpdated{Input: 1200, Output: 340, Elapsed: 8 * time.Second})

	s.Apply(Cleared{})
	if s.SessionActive || s.SessionIn != 0 || s.SessionOut != 0 || s.SessionElapsed != 0 {
		t.Fatalf("clear left telemetry behind: active=%v in=%d out=%d elapsed=%s",
			s.SessionActive, s.SessionIn, s.SessionOut, s.SessionElapsed)
	}
	if row := composerHintsFor(s); strings.Contains(row, "in: ") || strings.Contains(row, "out: ") {
		t.Errorf("cleared session still shows counters: %q", row)
	}
}

// An active session with no measured usage stays quiet rather than inventing
// a zero: unknown is absent, not "0 in / 0 out".
func TestSessionTelemetryOmitsUnmeasuredCounters(t *testing.T) {
	s := New()
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(AssistantDone{})
	if got := sessionTelemetry(s); got != "" {
		t.Errorf("unmeasured telemetry = %q, want empty", got)
	}
}

// The fresh shortcut bar's ^p must be true. On a session with nothing to
// recall it opens the settings palette; once a prompt exists the left shows
// telemetry, the right narrows to "? help", and ^p goes back to history
// recall.
func TestCtrlPOpensSettingsOnlyOnAFreshSession(t *testing.T) {
	fresh := NewProgram(nil, "test", 0)
	keyPress(fresh, tea.KeyMsg{Type: tea.KeyCtrlP})
	if fresh.state.HelpModal == nil {
		t.Fatalf("^p on a fresh session opened %+v, want the command & help center", fresh.state.HelpModal)
	}

	used := NewProgram(nil, "test", 0)
	used.state.Apply(UserSubmitted{Text: "earlier"})
	used.state.Apply(AssistantDone{})
	used.state.Apply(InputChanged{Text: ""})
	keyPress(used, tea.KeyMsg{Type: tea.KeyCtrlP})
	if used.state.HelpModal != nil {
		t.Error("^p hijacked history recall on an active session")
	}
	if used.state.Input != "earlier" {
		t.Errorf("input = %q, want the recalled history", used.state.Input)
	}
}

func TestFormatTokensCompacts(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{1200, "1.2k"},
		{1_500_000, "1.5M"},
	}
	for _, c := range cases {
		if got := formatTokens(c.in); got != c.want {
			t.Errorf("formatTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The footer is two zones: left metrics/context, right shortcuts pinned to
// the far edge. A wide frame pads between them; a narrow frame keeps the
// measured telemetry over shortcuts rather than wrapping to a second row.
func TestSessionBarSplitsTelemetryAndShortcuts(t *testing.T) {
	left, right := "↑1.2k · ↓340", "? help · ^p settings"
	row := sessionBarRow(left, right, 60)
	if !strings.HasPrefix(row, left) || !strings.HasSuffix(row, right) {
		t.Fatalf("split row = %q, want left leading and right trailing", row)
	}
	if got := CellWidth(row); got != 60 {
		t.Errorf("split row is %d cells, want exactly the 60-cell frame", got)
	}
	narrow := sessionBarRow(left, right, 20)
	if got := CellWidth(narrow); got > 20 {
		t.Fatalf("narrow row is %d cells, want no overflow: %q", got, narrow)
	}
	if !strings.Contains(narrow, "↑1.2k") {
		t.Errorf("narrow row = %q, want measured telemetry to survive the trim", narrow)
	}
	if got := sessionBarRow(left, right, 0); got != left+" · "+right {
		t.Errorf("unbounded row = %q, want the joined zones", got)
	}
}

// Fresh shows minimal path-only left with full shortcuts; active reveals full
// telemetry and narrows the right so ^p never promises settings when it
// recalls history. Either way the left never invents zeros.
func TestIdleHintZonesKeepCtrlPTruthful(t *testing.T) {
	fresh := New()
	fresh.Apply(WorkspaceUpdated{Cwd: "/work/proj", HasCwd: true, GitBranch: "main", HasGit: true})
	left, right := idleHintZones(fresh)
	if right != SessionShortcutBar || !strings.Contains(right, "^p settings") {
		t.Errorf("fresh right = %q, want the full shortcut bar", right)
	}
	if left == "" || !strings.Contains(left, "proj") {
		t.Errorf("fresh left = %q, want minimal path info", left)
	}

	active := New()
	active.Apply(UserSubmitted{Text: "go"})
	active.Apply(AssistantDone{})
	active.Apply(SessionMetricsUpdated{Input: 500, Output: 100})
	active.Apply(WorkspaceUpdated{Cwd: "/work/proj", HasCwd: true, HasGit: false})
	left, right = idleHintZones(active)
	if !strings.Contains(left, "↑500") || !strings.Contains(left, "↓100") {
		t.Errorf("active left = %q, want measured ↑/↓ telemetry", left)
	}
	if strings.Contains(right, "^p settings") {
		t.Errorf("active right = %q, want no ^p promise once history exists", right)
	}
	if !strings.Contains(right, "? help") {
		t.Errorf("active right = %q, want the help shortcut kept", right)
	}
}

// A split-pane minimum width keeps measured tokens and cost over the help
// shortcut: at 40 columns the footer must still name what the session spent,
// not just how to ask for help.
func TestNarrowFooterKeepsTokensOverHelp(t *testing.T) {
	left := "↑1.6k · ↓127 (0%) · $0.02 · #44 · …/test-web1:main"
	right := "? help"
	row := sessionBarRow(left, right, 39)
	if got := CellWidth(row); got > 39 {
		t.Fatalf("narrow row is %d cells, want no overflow: %q", got, row)
	}
	if !strings.Contains(row, "↑1.6k") || !strings.Contains(row, "↓127") {
		t.Errorf("narrow row = %q, want the token counts kept", row)
	}
	if !strings.Contains(row, "$0.02") {
		t.Errorf("narrow row = %q, want the cost kept over the shortcut", row)
	}

	s := New()
	s.Status.Model = "p/m"
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(AssistantDone{})
	s.Apply(SessionMetricsUpdated{Input: 1600, Output: 127})
	s.Apply(WorkspaceUpdated{
		Cwd: "/tmp/web-test4/test-web1", HasCwd: true,
		Session: "#44", HasSession: true,
		UsageText: "$0.02", HasUsage: true,
		GitBranch: "main", HasGit: true,
	})
	s.Apply(TerminalResized{Width: 40, Height: 24})
	out := Render(s)
	footer := lastLine(out)
	if !strings.Contains(footer, "↑1.6k") || !strings.Contains(footer, "↓127") {
		t.Errorf("40-col footer = %q, want token counts visible at minimum size", footer)
	}
	if !strings.Contains(footer, "$0.02") {
		t.Errorf("40-col footer = %q, want cost visible at minimum size", footer)
	}
	if got := CellWidth(footer); got > 40 {
		t.Errorf("40-col footer is %d cells, want one row: %q", got, footer)
	}
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
