package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A fresh session shows the shortcut bar and no token counters: there is
// nothing measured to report, so reporting "0 in / 0 out" would be a lie.
func TestSessionBarHiddenOnFreshSession(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})

	out := Render(s)
	if strings.Contains(out, "in: ") || strings.Contains(out, "out: ") {
		t.Fatalf("fresh session showed token counters:\n%s", out)
	}
	if !strings.Contains(out, "? help") || !strings.Contains(out, "^p settings") {
		t.Fatalf("fresh session lost the shortcut bar:\n%s", out)
	}
	if got := composerHintsFor(s); got != SessionShortcutBar {
		t.Errorf("fresh helper row = %q, want the shortcut bar", got)
	}
}

// The first executed prompt flips the helper row to measured telemetry, and
// every segment reflects exactly what the runtime reported.
func TestSessionTelemetryVisibleAfterFirstRun(t *testing.T) {
	s := New()
	s.Status.Model = "p/m"
	s.Apply(TerminalResized{Width: 100, Height: 24})
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(AssistantDone{})
	s.Apply(SessionMetricsUpdated{Input: 1200, Output: 340, Elapsed: 3 * time.Second})

	row := composerHintsFor(s)
	for _, want := range []string{"in: 1.2k", "out: 340", "3s", "enter send"} {
		if !strings.Contains(row, want) {
			t.Errorf("telemetry row = %q, want %q", row, want)
		}
	}
	if strings.Contains(row, "? help") {
		t.Errorf("telemetry did not replace the shortcut hints: %q", row)
	}
	if out := Render(s); !strings.Contains(out, "in: 1.2k") {
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
// recall it opens the settings palette; once a prompt exists the bar shows
// telemetry and ^p goes back to history recall.
func TestCtrlPOpensSettingsOnlyOnAFreshSession(t *testing.T) {
	fresh := NewProgram(nil, "test", 0)
	keyPress(fresh, tea.KeyMsg{Type: tea.KeyCtrlP})
	if fresh.state.Picker == nil || fresh.state.Picker.Kind != PickerCommands {
		t.Fatalf("^p on a fresh session opened %+v, want the settings palette", fresh.state.Picker)
	}
	if fresh.state.Picker.Title != "settings" {
		t.Errorf("palette title = %q, want settings", fresh.state.Picker.Title)
	}

	used := NewProgram(nil, "test", 0)
	used.state.Apply(UserSubmitted{Text: "earlier"})
	used.state.Apply(AssistantDone{})
	used.state.Apply(InputChanged{Text: ""})
	keyPress(used, tea.KeyMsg{Type: tea.KeyCtrlP})
	if used.state.Picker != nil {
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
