package tui

import (
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	diffview "github.com/PizenLabs/ohgo/tui/diff"
)

// Interactive diff inspector wiring.
//
// The inspector is a self-contained bubbletea model (tui/diff) that reads the
// repository through its own git driver. The program's job is narrow: open it
// for /diff, route every message to it while it is open, and paint its View in
// the pane area. It is a modal overlay in the same family as the picker and the
// model control plane, so the composer hides while it is up and no workspace
// turn can be submitted underneath it.

// openDiff starts the inspector for the parsed /diff arguments.
//
// A modal already on screen owns the keyboard, so /diff declines rather than
// stacking: two overlays would fight over the same keys. The size is resolved
// after the model is installed so ComputeLayout counts the inspector's own
// overlay budget (the composer rows it releases) rather than the transcript's.
func (p *Program) openDiff(args []string) tea.Cmd {
	if p.state.Diff != nil {
		return nil
	}
	if p.otherOverlayOpen() {
		p.state.Apply(Notice{Text: "close the open panel before /diff"})
		return nil
	}

	dir := strings.TrimSpace(p.state.Status.Cwd)
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	if dir == "" {
		p.state.Apply(Notice{Text: "diff: no working directory"})
		return nil
	}

	spec, err := diffview.ParseArgs(args)
	if err != nil {
		p.state.Apply(Notice{Text: err.Error()})
		return nil
	}

	model := diffview.New(diffview.NewGit(dir), spec, spec.Label)
	model.SetTheme(p.state.Theme)
	p.state.Apply(DiffOpened{Model: model})
	p.syncDiffSize()
	return model.Init()
}

// otherOverlayOpen reports whether a modal that owns the keyboard is up.
func (p *Program) otherOverlayOpen() bool {
	return p.state.Picker != nil || p.state.Plane != nil || p.state.SupportPanel != nil ||
		p.state.Permissions != nil || p.state.StatusModal != nil || p.state.QuitConfirm != nil ||
		p.state.Pending != nil || p.state.Secret != nil
}

// closeDiff dismisses the inspector.
func (p *Program) closeDiff() {
	p.state.Apply(DiffClosed{})
}

// syncDiffSize re-budgets the inspector for the current terminal. The model
// slices its viewport to this height, so a resize must reach it or the frame
// would be drawn against a stale budget.
func (p *Program) syncDiffSize() {
	if p.state.Diff == nil {
		return
	}
	width, height := p.diffSize()
	p.state.Diff.SetSize(width, height)
}

// diffSize is the width and row budget the inspector's frame may occupy: the
// conversation height in the alt-screen path, or the live region's bound in
// output-first mode.
func (p *Program) diffSize() (width, height int) {
	layout := ComputeLayout(p.state)
	width = displayWidth(p.state, layout)
	height = layout.ConversationHeight
	if p.live != nil {
		height = liveRows(p.state)
	}
	return width, height
}

// handleDiffKey routes a keypress to the inspector.
//
// Esc clears an active filter before it closes, so a typed query is never lost
// to a reflexive dismiss; a second Esc (or q on the tree with no filter) closes.
// Every other key — including a printable rune that is really a filter
// character — goes to the model, which owns the interaction inside the panes.
func (p *Program) handleDiffKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	model := p.state.Diff
	if model == nil {
		return p, nil
	}
	switch msg.String() {
	case "ctrl+c":
		p.closeDiff()
		return p, nil
	case "esc":
		if model.FilterActive() {
			model.ClearFilter()
			return p, nil
		}
		p.closeDiff()
		return p, nil
	case "q":
		if !model.FilterActive() {
			p.closeDiff()
			return p, nil
		}
	}
	return p.forwardDiff(msg)
}

// handleDiffMsg forwards any non-key message to the inspector. The async
// file-list and per-file diff results arrive this way.
func (p *Program) handleDiffMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	if p.state.Diff == nil {
		return p, nil
	}
	return p.forwardDiff(msg)
}

// forwardDiff hands a message to the model and writes the returned model back.
// Update returns the tea.Model interface, so the concrete type is asserted
// here, in one place.
func (p *Program) forwardDiff(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := p.state.Diff.Update(msg)
	if model, ok := updated.(*diffview.Model); ok {
		p.state.Diff = model
	}
	return p, cmd
}

// renderDiffArea paints the inspector in place of the transcript. The model
// already bounds its own frame to the size the program handed it, so no further
// slicing happens here.
func renderDiffArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	if state.Diff == nil {
		renderTranscriptArea(sb, state, pal, layout)
		return
	}
	sb.WriteString(state.Diff.View())
	sb.WriteByte('\n')
}
