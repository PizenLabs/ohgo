package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/update"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// newUpdateProgram builds a program that can drive /update without a loop.
// closeDone releases the context the command derives its cancellation from.
func newUpdateProgram(t *testing.T) *Program {
	t.Helper()
	p := NewProgram(nil, "test-model", 0)
	t.Cleanup(p.closeDone)
	return p
}

func updateHeader(t *testing.T, state State) string {
	t.Helper()
	return ansi.Strip(strings.Split(Render(state), "\n")[0])
}

func updateNotices(state State) string {
	var text strings.Builder
	for _, entry := range state.Entries {
		if entry.Kind == EntryNotice {
			text.WriteString(entry.Notice)
		}
	}
	return text.String()
}

// Only /update may install: a discovered release is an observation, and
// applying it repeatedly must never cross the authority boundary.
func TestUpdateFoundIsHeaderChromeNotTranscript(t *testing.T) {
	calls := 0
	p := newUpdateProgram(t)
	p.SetUpdateFunc(func(context.Context) (update.Result, error) {
		calls++
		return update.Result{InstalledVersion: "v9.9.9"}, nil
	})

	before := len(p.state.Entries)
	for i := 0; i < 32; i++ {
		p.state.Apply(UpdateFound{Version: "1.2.3"})
	}
	if calls != 0 {
		t.Fatalf("an observation ran the update authority %d times", calls)
	}
	if len(p.state.Entries) != before {
		t.Fatalf("update notice added %d transcript entries", len(p.state.Entries)-before)
	}
	if p.state.Status.UpdateAvailable != "v1.2.3" {
		t.Fatalf("UpdateAvailable = %q, want v1.2.3", p.state.Status.UpdateAvailable)
	}
	header := updateHeader(t, p.state)
	if !strings.Contains(header, "update available: v1.2.3 · /update") {
		t.Fatalf("header = %q, want the update hint", header)
	}
	footer := ansi.Strip(renderStatus(p.state, DarkPalette(), ComputeLayout(p.state)))
	if strings.Contains(footer, "v1.2.3") {
		t.Fatalf("footer = %q, the release has a second home", footer)
	}
}

// Ignoring a hint is valid for as long as the user likes: unrelated chrome
// changes must not expire, retract, or start the observation.
func TestUpdateHintSurvivesUnrelatedChromeChanges(t *testing.T) {
	p := newUpdateProgram(t)
	p.SetUpdateFunc(func(context.Context) (update.Result, error) {
		t.Fatal("an ignored hint ran the authority")
		return update.Result{}, nil
	})
	p.state.Apply(UpdateFound{Version: "v1.2.3"})
	for i := 0; i < 50; i++ {
		p.state.Apply(TerminalResized{Width: 80 + i, Height: 24})
		p.state.Apply(ContextUpdated{Used: i, Window: 1000})
	}
	if got := p.state.Status.UpdateAvailable; got != "v1.2.3" {
		t.Fatalf("UpdateAvailable = %q, want the hint to persist", got)
	}
	if header := updateHeader(t, p.state); !strings.Contains(header, "v1.2.3") {
		t.Fatalf("header = %q, want the persisted hint", header)
	}
}

func TestUpdateFoundIgnoresEmptyVersion(t *testing.T) {
	state := New()
	state.Apply(UpdateFound{Version: "v1.2.3"})
	state.Apply(UpdateFound{Version: "   "})
	if state.Status.UpdateAvailable != "v1.2.3" {
		t.Fatalf("empty refresh erased a real observation: %q", state.Status.UpdateAvailable)
	}
}

// /update must execute exactly once, off the update path, and report the
// installed release while the running process is still the old build.
func TestExplicitUpdateInstallsOnCommand(t *testing.T) {
	p := newUpdateProgram(t)
	p.state.Status.Version = "1.0.0"
	p.state.Apply(UpdateFound{Version: "v1.2.3"})
	before := len(p.state.Entries)

	var calls int
	p.SetUpdateFunc(func(ctx context.Context) (update.Result, error) {
		calls++
		if ctx == nil {
			t.Error("update authority received a nil context")
		}
		if !p.state.Status.UpdateRunning {
			t.Error("authority ran without the running state set")
		}
		return update.Result{InstalledVersion: "v1.2.3"}, nil
	})

	cmd := p.runCommand(Command{Name: "update"})
	if cmd == nil {
		t.Fatal("/update produced no command")
	}
	if !p.state.Status.UpdateRunning {
		t.Fatal("/update did not mark the update running")
	}
	if got := len(p.state.Entries); got != before {
		t.Fatalf("starting /update added %d transcript entries", got-before)
	}
	if header := updateHeader(t, p.state); !strings.Contains(header, "updating") {
		t.Fatalf("header = %q, want update progress while running", header)
	}

	p.Update(cmd())

	if calls != 1 {
		t.Fatalf("authority ran %d times, want 1", calls)
	}
	if p.state.Status.UpdateRunning || p.state.Status.UpdateStage != "" {
		t.Fatalf("update state not retired: running=%v stage=%q",
			p.state.Status.UpdateRunning, p.state.Status.UpdateStage)
	}
	if p.state.Status.UpdateAvailable != "" {
		t.Fatalf("successful update kept the hint %q", p.state.Status.UpdateAvailable)
	}
	if got := len(p.state.Entries); got != before+1 {
		t.Fatalf("transcript = %d entries, want exactly one outcome notice", got-before)
	}
	text := updateNotices(p.state)
	if !strings.Contains(text, "Updated ohgo to v1.2.3.") {
		t.Fatalf("notice = %q, want the installed release", text)
	}
	if !strings.Contains(text, "Restart ohgo to use the new version.") {
		t.Fatalf("notice = %q, must not claim the running process changed", text)
	}
}

// Already current is a resolved observation: the hint goes away and the
// notice reports the truth instead of pretending an install happened.
func TestExplicitUpdateAlreadyCurrentClearsTheHint(t *testing.T) {
	p := newUpdateProgram(t)
	p.state.Apply(UpdateFound{Version: "v1.2.3"})
	p.SetUpdateFunc(func(context.Context) (update.Result, error) {
		return update.Result{
			AlreadyCurrent: true,
			Status:         update.Status{CurrentVersion: "v1.2.3"},
		}, nil
	})

	p.Update(p.runCommand(Command{Name: "update"})())

	if p.state.Status.UpdateAvailable != "" {
		t.Fatalf("already-current kept the hint %q", p.state.Status.UpdateAvailable)
	}
	if text := updateNotices(p.state); !strings.Contains(text, "already up to date (v1.2.3)") {
		t.Fatalf("notice = %q, want the current release named", text)
	}
}

// A failure keeps the availability hint: the newer release still exists, so
// hiding it would misreport the machine. The authority's cause survives.
func TestExplicitUpdateFailureKeepsTheHintAndCause(t *testing.T) {
	p := newUpdateProgram(t)
	p.state.Apply(UpdateFound{Version: "v1.2.3"})
	p.SetUpdateFunc(func(context.Context) (update.Result, error) {
		return update.Result{}, errors.New("download failed")
	})

	p.Update(p.runCommand(Command{Name: "update"})())

	if p.state.Status.UpdateRunning || p.state.Status.UpdateStage != "" {
		t.Fatalf("failed update left running state: %v %q",
			p.state.Status.UpdateRunning, p.state.Status.UpdateStage)
	}
	if p.state.Status.UpdateAvailable != "v1.2.3" {
		t.Fatalf("failure erased the known release: %q", p.state.Status.UpdateAvailable)
	}
	text := updateNotices(p.state)
	if !strings.Contains(text, "update: download failed") {
		t.Fatalf("notice = %q, want the authority cause", text)
	}
	if header := updateHeader(t, p.state); strings.Contains(header, "updating") {
		t.Fatalf("header = %q, failed update still reports progress", header)
	}
}

// A second explicit command must be refused while the first is running: two
// installers would race over the same executable.
func TestExplicitUpdateRefusesConcurrentRun(t *testing.T) {
	p := newUpdateProgram(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	p.SetUpdateFunc(func(context.Context) (update.Result, error) {
		calls++
		close(started)
		<-release
		return update.Result{InstalledVersion: "v1.2.3"}, nil
	})

	first := p.runCommand(Command{Name: "update"})
	if first == nil {
		t.Fatal("first /update produced no command")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- first() }()
	<-started

	if second := p.runCommand(Command{Name: "update"}); second != nil {
		t.Fatal("second /update produced a command while one was running")
	}
	if text := updateNotices(p.state); !strings.Contains(text, "already running") {
		t.Fatalf("notice = %q, want the concurrent refusal", text)
	}

	close(release)
	p.Update(<-done)
	if calls != 1 {
		t.Fatalf("authority ran %d times, want 1", calls)
	}
}

// Without an authority the command reports itself unavailable rather than
// inventing a second updater or panicking.
func TestUpdateWithoutAuthorityReportsUnavailable(t *testing.T) {
	p := newUpdateProgram(t)
	if cmd := p.runCommand(Command{Name: "update"}); cmd != nil {
		t.Fatal("a nil authority still produced a command")
	}
	if p.state.Status.UpdateRunning {
		t.Fatal("a nil authority marked the update running")
	}
	if text := updateNotices(p.state); !strings.Contains(text, "unavailable") {
		t.Fatalf("notice = %q, want the unavailable report", text)
	}
}

// Progress is chrome only, and only while an update is actually running.
func TestUpdateProgressIsHeaderOnlyWhileRunning(t *testing.T) {
	p := newUpdateProgram(t)
	before := len(p.state.Entries)

	p.state.Apply(UpdateProgress{Stage: "downloading"})
	if p.state.Status.UpdateStage != "" {
		t.Fatalf("progress applied without a running update: %q", p.state.Status.UpdateStage)
	}
	p.state.Apply(UpdateStarted{})
	p.state.Apply(UpdateProgress{Stage: "downloading"})
	if p.state.Status.UpdateStage != "downloading" {
		t.Fatalf("UpdateStage = %q, want downloading", p.state.Status.UpdateStage)
	}
	if got := len(p.state.Entries); got != before {
		t.Fatalf("progress added %d transcript entries", got-before)
	}
	if header := updateHeader(t, p.state); !strings.Contains(header, "updating · downloading") {
		t.Fatalf("header = %q, want the running stage", header)
	}
	if footer := ansi.Strip(renderStatus(p.state, DarkPalette(), ComputeLayout(p.state))); strings.Contains(footer, "downloading") {
		t.Fatalf("footer = %q, progress belongs in the header", footer)
	}
}

func TestPublishUpdateAvailableUsesTheEventChannel(t *testing.T) {
	program := NewProgram(nil, "provider/model", 1000)
	if program.state.Status.UpdateAvailable != "" {
		t.Fatalf("new program already has an update hint: %q", program.state.Status.UpdateAvailable)
	}
	program.PublishUpdateAvailable("1.4.0")
	select {
	case event := <-program.events:
		found, ok := event.(UpdateFound)
		if !ok || found.Version != "1.4.0" {
			t.Fatalf("event = %#v, want UpdateFound{1.4.0}", event)
		}
	default:
		t.Fatal("PublishUpdateAvailable did not publish an event")
	}
	program.closeDone()
}

func TestPublishUpdateProgressUsesTheEventChannel(t *testing.T) {
	program := NewProgram(nil, "provider/model", 1000)
	program.PublishUpdateProgress("downloading")
	select {
	case event := <-program.events:
		progress, ok := event.(UpdateProgress)
		if !ok || progress.Stage != "downloading" {
			t.Fatalf("event = %#v, want UpdateProgress{downloading}", event)
		}
	default:
		t.Fatal("PublishUpdateProgress did not publish an event")
	}
	program.closeDone()
}

// Availability has one home, and it sheds detail with the header tier rather
// than wrapping or moving to the footer.
func TestUpdateHintRendersInHeaderByTier(t *testing.T) {
	cases := []struct {
		width int
		want  string
	}{
		{120, "update available: v1.2.3 · /update"},
		{70, "update v1.2.3 · /update"},
		{50, "update v1.2.3"},
	}
	for _, tc := range cases {
		state := New()
		state.Status.Model = "groq/m"
		state.Status.UpdateAvailable = "v1.2.3"
		state.Width = tc.width
		state.Height = 24
		header := updateHeader(t, state)
		if !strings.Contains(header, tc.want) {
			t.Errorf("width %d header = %q, want %q", tc.width, header, tc.want)
		}
		footer := ansi.Strip(renderStatus(state, DarkPalette(), ComputeLayout(state)))
		if strings.Contains(footer, "v1.2.3") {
			t.Errorf("width %d footer = %q, the release must not repeat", tc.width, footer)
		}
	}
}

// The hint must never cost a second header row: the model pays for it, and
// identity survives at every supported width.
func TestUpdateHintKeepsHeaderOneRow(t *testing.T) {
	const model = "groq/deepseek-r1-distill-llama-70b"
	for _, width := range []int{160, 140, 120, 100, 81, 80, 70, 60, 55, 54, 50, 40} {
		state := New()
		state.Status.Model = model
		state.Status.Version = "1.0.0"
		state.Status.UpdateAvailable = "v1.2.3"
		state.Width = width
		state.Height = 24
		if ComputeLayout(state).Mode == LayoutMini {
			continue
		}
		header := updateHeader(t, state)
		if got := CellWidth(header); got > width {
			t.Errorf("width %d: header is %d cells, want one row: %q", width, got, header)
		}
		if !strings.Contains(header, "ohgo") {
			t.Errorf("width %d: header lost identity: %q", width, header)
		}
		if !strings.Contains(header, "v1.2.3") {
			t.Errorf("width %d: header shed the available release: %q", width, header)
		}
	}
}
