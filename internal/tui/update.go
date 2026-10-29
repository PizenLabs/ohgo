package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// updateCommandTimeout bounds one explicit update. It matches the CLI's own
// command budget: a stalled endpoint fails instead of holding the operation
// open forever. The running turn, composer, and renderer are never blocked
// because the whole operation lives off the update goroutine.
const updateCommandTimeout = 10 * time.Minute

// UpdateFunc is the explicit-update authority behind /update.
//
// The TUI presents this seam; it does not own release resolution, download,
// verification, installation, or rollback. Production supplies
// update.Service.Update, so both `ohgo update` and /update cross the same
// boundary. A nil func reports the command unavailable rather than inventing
// a second updater.
type UpdateFunc func(ctx context.Context) (update.Result, error)

// updateFinishedMsg carries the authority's outcome into Update.
//
// It is a Bubble Tea message, not a state event: progress may be best-effort
// chrome, but the final outcome must always reach the update goroutine even
// when the event buffer is full. A lost completion would leave the header
// reporting a run that already ended.
type updateFinishedMsg struct {
	result update.Result
	err    error
}

// SetUpdateFunc installs the update authority behind /update.
func (p *Program) SetUpdateFunc(fn UpdateFunc) {
	p.updateFn = fn
}

// PublishUpdateProgress records one stage of the explicit update from the
// authority's progress hook. It is safe before or during Run and only touches
// header chrome: no transcript entry, no dialog, no focus change.
func (p *Program) PublishUpdateProgress(stage string) {
	p.publish(UpdateProgress{Stage: stage})
}

// startUpdate launches the explicit update off the update goroutine.
//
// This is the only transition from user intent to installation:
//
//	/update → startUpdate → update authority
//
// UpdateFound never calls it. That separation is the feature: an available
// release is information, and only a human invoking /update grants authority.
func (p *Program) startUpdate() tea.Cmd {
	if p.updateFn == nil {
		p.state.Apply(Notice{Text: "self-update is unavailable in this session"})
		return nil
	}
	if p.updateRunning {
		p.state.Apply(Notice{Text: "an update is already running"})
		return nil
	}
	p.updateRunning = true
	p.state.Apply(UpdateStarted{})
	// Derive cancellation from the program lifetime so quitting stops a
	// stalled network operation instead of leaving it behind a dead UI.
	base := p.catalogCtx
	if base == nil {
		base = context.Background()
	}
	fn := p.updateFn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(base, updateCommandTimeout)
		defer cancel()
		result, err := fn(ctx)
		return updateFinishedMsg{result: result, err: err}
	}
}

// onUpdateFinished renders the authority's outcome and retires the hint only
// when the observation is actually resolved.
func (p *Program) onUpdateFinished(m updateFinishedMsg) (tea.Model, tea.Cmd) {
	p.updateRunning = false
	if m.err != nil {
		p.state.Apply(UpdateFinished{Err: m.err.Error()})
		p.state.Apply(Notice{Text: updateFailureText(m.err)})
		return p, nil
	}
	current := m.result.Status.CurrentVersion
	if strings.TrimSpace(current) == "" {
		current = p.state.Status.Version
	}
	switch {
	case m.result.AlreadyCurrent:
		p.state.Apply(UpdateFinished{AlreadyCurrent: true})
		if m.result.Status.Reason == update.ReasonPrerelease {
			p.state.Apply(Notice{Text: fmt.Sprintf(
				"ohgo is already up to date (%s); the newest release is a prerelease.",
				updateDisplayVersion(current))})
			return p, nil
		}
		p.state.Apply(Notice{Text: fmt.Sprintf("ohgo is already up to date (%s).", updateDisplayVersion(current))})
	case m.result.InstalledVersion != "":
		p.state.Apply(UpdateFinished{InstalledVersion: m.result.InstalledVersion})
		// The running image is still the old release. Saying only "updated"
		// would invite the user to believe the current process changed.
		p.state.Apply(Notice{Text: fmt.Sprintf(
			"Updated ohgo to %s.\nRestart ohgo to use the new version.",
			updateDisplayVersion(m.result.InstalledVersion))})
	default:
		// No install and no error is still a resolved observation: report the
		// current release truthfully rather than claiming a change.
		p.state.Apply(UpdateFinished{AlreadyCurrent: true})
		p.state.Apply(Notice{Text: fmt.Sprintf("ohgo is already up to date (%s).", updateDisplayVersion(current))})
	}
	return p, nil
}

// updateFailureText keeps the authority's cause text. The update package
// already names the failing operation (resolve, download, checksum,
// permissions, package-manager ownership, rollback), so the TUI must not
// replace that with a generic "update failed".
func updateFailureText(err error) string {
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "update failed"
	}
	if strings.HasPrefix(strings.ToLower(text), "update:") {
		return text
	}
	return "update: " + text
}

// updateDisplayVersion spells a release the way every ohgo surface does: a
// leading "v" for a release, the raw spelling for anything unorderable.
func updateDisplayVersion(raw string) string {
	if parsed, ok := update.ParseVersion(raw); ok {
		return parsed.Display()
	}
	return strings.TrimSpace(raw)
}
