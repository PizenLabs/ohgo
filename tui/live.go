package tui

// Output-first interaction mode (the prototype).
//
// The full-screen UI in view.go owns a virtual transcript viewport: it keeps
// every rendered line in memory, slices it to the conversation height, and
// repaints the whole frame on every state change. That ownership is what makes
// the interaction boundary unstable — the wheel, drag selection and copy all
// belong to the terminal, and an application-owned scrollback wants the bytes.
//
// This mode inverts the boundary. Transcript blocks are handed to Bubble Tea
// as print lines, which the standard renderer writes above its own frame and
// the terminal scrolls into its scrollback: from then on the terminal owns
// them, so wheel scrolling, drag selection and copy stay its job. ohgo keeps a
// small live region at the bottom — the in-flight answer, the status line and
// the composer — and rewrites only that.
//
//	state → transcript blocks → terminal output   (history, append-only)
//	state → live block + status + composer        (rewritten in place)
//
// Everything that can be sealed is written exactly once. Everything still
// moving is bounded to one region the renderer owns, so no scrollback line is
// ever painted twice.
//
// The terminal lifecycle stays exactly where it was. Raw mode, hidden cursor,
// bracketed paste and their restoration on every exit path are Bubble Tea's,
// as in the alt-screen path: this mode only declines the alternate screen and
// never asks for a mouse mode, and that is the whole mechanism. Notably, it
// does not use tea.WithoutRenderer, which makes Bubble Tea skip terminal
// initialisation altogether — including raw mode, which would leave the
// composer unable to read a single keypress.

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Row budgets for the live region.
const (
	// liveSpareRows keeps the painted region off the terminal's last row: a
	// write on the last row scrolls, which would move the rows the renderer
	// is still counting on.
	liveSpareRows = 2
	// liveDefaultRows is the row budget when the terminal has not reported a
	// size — a headless run, or a test that never sent WindowSizeMsg.
	liveDefaultRows = 24
	// liveBlockCap bounds the in-flight answer so one long turn cannot grow
	// the live region past what a single repaint can reach.
	liveBlockCap = 20
	// liveCommitWalk bounds how many paragraph boundaries a cut considers.
	// A block with more of them than this is far past the point where
	// committing matters.
	liveCommitWalk = 200
)

// livePainter resolves state into history and live rows, tracking its own
// progress so each scrollback line is produced exactly once.
//
// It writes nothing. History leaves through tea.Println, which Bubble Tea's
// renderer handles; the live region is returned to the same renderer by View.
// Keeping one writer is what makes the renderer's own row accounting sound.
type livePainter struct {
	// painted is how many entries have already left for the scrollback. It
	// only moves forward.
	painted int

	// streamIndex is the entry index of the in-flight assistant answer, and
	// streamRunes is how many of its runes have already left for the
	// scrollback. Rune offsets keep every seal on a character boundary, so a
	// CJK or emoji turn can never be cut mid-rune.
	streamIndex int
	streamRunes int

	// rows is the height of the live region the renderer last painted, live
	// is the region itself, and block is the live region's non-chrome part —
	// the in-flight answer. block is what a reader of the scrollback would
	// see once the turn ends, so tests use it to reconstruct the transcript.
	rows  int
	live  []string
	block []string
}

// newLivePainter returns a painter with no progress.
func newLivePainter() *livePainter {
	return &livePainter{streamIndex: -1}
}

// render folds one state change into output.
//
// It returns the rows that have just become permanent history, and caches the
// new live region for View. The order is fixed: the caller emits the history
// before the renderer paints the next frame, which is what puts the new
// content above the live region instead of under the composer.
func (v *livePainter) render(s State) []string {
	f := v.frame(s)
	v.live = f.live()
	v.block = f.block
	v.rows = len(v.live)
	return f.history
}

// view is the live region as the renderer expects it: rows joined by newlines.
func (v *livePainter) view() string {
	return strings.Join(v.live, "\n")
}

// forget drops painting progress after the transcript is replaced (resume) or
// wiped (/clear): the scrollback already holds real lines and the painter must
// not try to reconcile against them.
func (v *livePainter) forget() {
	v.painted = 0
	v.streamIndex = -1
	v.streamRunes = 0
	v.rows = 0
	v.live = nil
	v.block = nil
}

// sealToTerminal writes whatever never became history while the session was
// alive, then retires the live region.
//
// It runs after Bubble Tea has stopped and restored the terminal, so the
// remaining rows are plain output scrolling into the terminal's scrollback.
// Without it, an answer whose turn was interrupted — or one still hidden
// behind a closing overlay — would leave the terminal with the session.
func (v *livePainter) sealToTerminal(w io.Writer, s State) {
	if w == nil {
		return
	}
	f := v.frameSealed(s)
	var b strings.Builder
	if v.rows > 0 {
		b.WriteString("\r")
		b.WriteString("\x1b[" + strconv.Itoa(v.rows) + "A")
		b.WriteString("\x1b[J")
	}
	for _, line := range f.history {
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	_, _ = fmt.Fprint(w, b.String())
	v.rows = 0
	v.live = nil
	v.block = nil
}

// liveFrame is one painted frame.
//
// history becomes permanent scrollback — written once, never touched again.
// block is the in-flight answer: the rows the renderer repaints. chrome is
// the status line and the composer (or a modal overlay in their place). Only
// block and chrome are ever repainted, and block is capped by the row budget
// so the two together always fit.
type liveFrame struct {
	history []string
	block   []string
	chrome  []string
}

// live is the bottom region: what the terminal shows below the scrollback cut.
func (f liveFrame) live() []string {
	out := make([]string, 0, len(f.block)+len(f.chrome))
	out = append(out, f.block...)
	return append(out, f.chrome...)
}

// frame resolves a state into history and live rows, advancing the painter's
// own progress as it goes.
func (v *livePainter) frame(s State) liveFrame {
	pal := PaletteFor(s.Theme)
	layout := ComputeLayout(s)

	overlay := liveOverlay(s, pal, layout)
	if overlay != nil {
		// A modal owns the keyboard, so the in-flight answer is not shown.
		// It is sealed only when the overlay closes or the session ends,
		// which is why nothing is committed here.
		first := firstUnstable(s)
		f := v.sealUpTo(s, first, pal, layout)
		f.chrome = overlay
		return f
	}

	budget := liveBudget(s, layout)
	first := firstUnstable(s)
	f := v.sealUpTo(s, first, pal, layout)

	if first < len(s.Entries) {
		committed, live := v.streamLines(s, first, pal, layout, budget)
		f.history = append(f.history, committed...)
		f.block = live
	}
	if status := liveStatusLine(s, pal, layout); status != "" {
		f.chrome = append(f.chrome, status)
	}
	f.chrome = append(f.chrome, liveComposerLines(s, pal, layout)...)
	return f
}

// sealUpTo writes the entries below first that have not left yet.
//
// An assistant entry that has been streaming is finished from where the
// stream stopped, not from its beginning: the prefix is already in the
// scrollback, and re-rendering it would put the answer on screen twice.
func (v *livePainter) sealUpTo(s State, first int, pal *Palette, layout Layout) liveFrame {
	var f liveFrame
	for i := v.painted; i < first && i < len(s.Entries); i++ {
		e := s.Entries[i]
		if e.Kind == EntryAssistant && v.streamIndex == i {
			f.history = append(f.history, v.streamTail(e, pal, layout)...)
			continue
		}
		f.history = append(f.history, liveEntryLines(s, e, pal, layout)...)
	}
	v.painted = first
	return f
}

// streamTail renders the part of a streaming assistant entry that has not
// been sealed yet, and closes the stream.
func (v *livePainter) streamTail(e Entry, pal *Palette, layout Layout) []string {
	rest := []rune(e.AssistantText)
	if v.streamRunes > 0 && v.streamRunes <= len(rest) {
		rest = rest[v.streamRunes:]
	}
	v.streamIndex, v.streamRunes = -1, 0
	return liveMarkdownLines(string(rest), pal, layout)
}

// frameSealed resolves a state with no live region at all: everything is
// history. It is what shutdown paints.
func (v *livePainter) frameSealed(s State) liveFrame {
	pal := PaletteFor(s.Theme)
	layout := ComputeLayout(s)
	var f liveFrame
	for i := v.painted; i < len(s.Entries); i++ {
		f.history = append(f.history, liveEntryLines(s, s.Entries[i], pal, layout)...)
	}
	if open := s.openAssistant(); open >= 0 && open >= v.painted {
		// An interrupted answer: render its tail and give up streaming it.
		rest := []rune(s.Entries[open].AssistantText)
		if v.streamIndex == open && v.streamRunes <= len(rest) {
			rest = rest[v.streamRunes:]
		}
		f.history = append(f.history, liveMarkdownLines(string(rest), pal, layout)...)
	}
	v.painted = len(s.Entries)
	v.streamIndex, v.streamRunes = -1, 0
	return f
}

// streamLines renders the live region below the sealed history: every entry
// from the first unstable one, with the in-flight assistant answer committing
// a prefix to the scrollback when it no longer fits.
func (v *livePainter) streamLines(s State, first int, pal *Palette, layout Layout, budget int) (history, live []string) {
	for i := first; i < len(s.Entries); i++ {
		e := s.Entries[i]
		if e.Kind == EntryAssistant && s.openAssistant() == i {
			committed, kept := v.assistantBlock(s, i, pal, layout, budget)
			history = append(history, committed...)
			live = append(live, kept...)
			continue
		}
		live = append(live, liveEntryLines(s, e, pal, layout)...)
	}
	return history, live
}

// assistantBlock renders one in-flight assistant answer at index i,
// committing a prefix to the scrollback when the block no longer fits.
//
// The commit point is a blank line wherever one exists. A paragraph break is
// the only place a later wrap cannot have moved: committing mid-paragraph
// would put a render in the scrollback that the very next delta contradicts,
// and the scrollback is the thing this mode promises never to rewrite. A
// single paragraph longer than the budget has no such point, so it seals
// whole and the seam lands inside it.
func (v *livePainter) assistantBlock(s State, i int, pal *Palette, layout Layout, budget int) (history, live []string) {
	text := s.Entries[i].AssistantText
	if v.streamIndex != i || v.streamRunes > len([]rune(text)) {
		v.streamIndex, v.streamRunes = i, 0
	}
	rest := []rune(text)[v.streamRunes:]
	if len(rest) == 0 {
		return nil, nil
	}
	lines := liveMarkdownLines(string(rest), pal, layout)
	if budget <= 0 || len(lines) <= budget {
		return nil, lines
	}
	cut := commitCut(rest, budget, pal, layout)
	if cut <= 0 || cut >= len(rest) {
		// No paragraph boundary fits: seal what there is and start fresh
		// with the next delta.
		v.streamRunes = len([]rune(text))
		return liveMarkdownLines(string(rest), pal, layout), nil
	}
	committed := liveMarkdownLines(string(rest[:cut]), pal, layout)
	v.streamRunes += cut
	return committed, liveMarkdownLines(string(rest[cut:]), pal, layout)
}

// liveMarkdownLines renders markdown to display lines plus the one blank row
// that separates it from what follows.
func liveMarkdownLines(text string, pal *Palette, layout Layout) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	rendered := RenderMarkdownWrapped(text, pal, textWidth(layout))
	if rendered == "" {
		return nil
	}
	return splitLines(rendered + "\n")
}

// commitCut returns how many runes of rest can leave for the scrollback so
// what remains live fits the budget, or -1 when no paragraph boundary does.
//
// Boundaries are walked newest first and the first one that fits wins, so the
// live block keeps as little of the answer as it safely can. An earlier
// boundary is a longer remainder and therefore never shorter, so the walk is
// monotone and the first hit is the latest that fits.
//
// The cut lands before the blank separator, not after it, so the remainder
// owns the separating blank row: the committed prefix renders without a
// trailing blank (rendering trims it) and the remainder renders with its
// leading blank intact, and joining the two reproduces the whole-document
// render exactly.
func commitCut(rest []rune, budget int, pal *Palette, layout Layout) int {
	for scanned, i := 0, len(rest)-1; i > 0 && scanned < liveCommitWalk; i-- {
		if !isParagraphEnd(rest, i) {
			continue
		}
		scanned++
		if len(liveMarkdownLines(string(rest[i:]), pal, layout)) <= budget {
			return i
		}
	}
	return -1
}

// isParagraphEnd reports whether the newline at i ends a blank line.
func isParagraphEnd(rest []rune, i int) bool {
	if rest[i] != '\n' {
		return false
	}
	j := i - 1
	for j >= 0 && (rest[j] == ' ' || rest[j] == '\t' || rest[j] == '\r') {
		j--
	}
	return j >= 0 && rest[j] == '\n'
}

// liveBudget is the row budget for the in-flight answer: what the screen can
// hold, minus the status line and the composer, capped so one long turn can
// never outgrow a single repaint. Zero means append-only: the answer is
// sealed whole when its turn ends and nothing streams.
func liveBudget(s State, layout Layout) int {
	rows := liveRows(s)
	if rows <= 0 {
		return 0
	}
	budget := rows - liveChrome(s, layout)
	if budget > liveBlockCap {
		budget = liveBlockCap
	}
	if budget < 4 {
		return 0
	}
	return budget
}

// liveRows is how many rows of the screen the live region may occupy.
func liveRows(s State) int {
	if s.Height <= 0 {
		return liveDefaultRows
	}
	return s.Height - liveSpareRows
}

// liveChrome counts the rows the live region spends outside the answer.
func liveChrome(s State, layout Layout) int {
	pal := PaletteFor(s.Theme)
	n := len(liveComposerLines(s, pal, layout))
	if liveStatusLine(s, pal, layout) != "" {
		n++
	}
	return n
}

// firstUnstable is the index of the first entry that can still change: the
// in-flight assistant answer (per the state's own openAssistant), or a tool
// still executing.
func firstUnstable(s State) int {
	first := len(s.Entries)
	if open := s.openAssistant(); open >= 0 {
		first = open
	}
	for i, e := range s.Entries {
		if i >= first {
			break
		}
		if e.Kind == EntryTool && e.Tool != nil && e.Tool.Status == ToolRunning {
			first = i
		}
		if e.Kind == EntryShell && e.Shell != nil && e.Shell.Status == ShellRunning {
			first = i
		}
	}
	return first
}

// liveEntryLines renders one sealed entry as permanent scrollback.
// The question uses the same faint background block as the fullscreen
// view, with no label prefix, so both modes distinguish it identically.
func liveEntryLines(s State, e Entry, pal *Palette, layout Layout) []string {
	switch e.Kind {
	case EntryUser:
		return liveUserLines(e.UserText, pal, layout)

	case EntryAssistant:
		return liveMarkdownLines(e.AssistantText, pal, layout)

	case EntryTool:
		if e.Tool == nil {
			return nil
		}
		var b strings.Builder
		renderTool(&b, e.Tool, pal, layout, spinnerFrame(s.Status.Spinner))
		return splitLines(b.String())

	case EntryShell:
		if e.Shell == nil {
			return nil
		}
		var b strings.Builder
		renderShell(&b, e.Shell, pal, layout)
		return splitLines(b.String())

	case EntryNotice:
		wrapped := wrapPlain(e.Notice, layout)
		out := make([]string, 0, len(wrapped)+1)
		for _, l := range wrapped {
			out = append(out, pal.Notice.Render(l))
		}
		return append(out, "")

	case EntryTask:
		if e.Task == nil {
			return nil
		}
		return append(taskLines(e.Task, pal, layout), "")

	case EntryRecap:
		// No separator row: the footer sits directly above the next prompt.
		lines := recapLines(e.Recap, textWidth(layout))
		for i, line := range lines {
			lines[i] = pal.Recap.Render(line)
		}
		return lines
	}
	return nil
}

// liveUserLines renders the question as a faint background block, padded
// to the content width like the fullscreen view so the surface reads solid.
func liveUserLines(text string, pal *Palette, layout Layout) []string {
	width := textWidth(layout)
	lines := wrapPlain(text, layout)
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		padded := l
		if width > 0 {
			if gap := width - CellWidth(l); gap > 0 {
				padded += strings.Repeat(" ", gap)
			}
		}
		out = append(out, pal.UserBG.Render(padded))
	}
	return out
}

// expandedToolLines renders one tool block's expanded projection as new
// scrollback lines for live mode.
//
// A sealed block cannot be rewritten — the terminal owns those rows now —
// so expanding one prints the same bounded projection fullscreen renders
// inline (badge, compact outcome, bounded body), correlated by the
// identical badge. Collapsing flips state only: sealed lines stay where
// the terminal put them.
func expandedToolLines(s State, id string) []string {
	for _, e := range s.Entries {
		if e.Kind != EntryTool || e.Tool == nil || e.Tool.ID != id {
			continue
		}
		cpy := *e.Tool
		cpy.Expanded = true
		pal := PaletteFor(s.Theme)
		layout := ComputeLayout(s)
		var b strings.Builder
		renderTool(&b, &cpy, pal, layout, spinnerFrame(s.Status.Spinner))
		return splitLines(b.String())
	}
	return nil
}

// liveOverlay renders a modal overlay in place of the answer. Precedence
// matches the full-screen UI: one thing at a time owns the keyboard.
func liveOverlay(s State, pal *Palette, layout Layout) []string {
	switch {
	case s.Transition != nil:
		return liveTransitionLines(s)
	case s.QuitConfirm != nil:
		return liveQuitConfirmLines(s, pal, layout)
	case s.Diff != nil:
		return liveDiffLines(s)
	case s.StatusModal != nil:
		return liveStatusModalLines(s, pal, layout)
	case s.UsageModal != nil:
		return liveUsageModalLines(s, pal, layout)
	case s.InfoModal != nil || s.InformationModal != nil:
		return liveInfoModalLines(s, pal, layout)
	case s.SessionModal != nil:
		return liveSessionManagerLines(s, pal, layout)
	case s.HelpModal != nil:
		return liveHelpCenterLines(s, pal, layout)
	case s.ConnectModal != nil:
		return liveConnectModalLines(s, pal, layout)
	case s.ConnectKey != nil:
		return liveConnectKeyLines(s, pal, layout)
	case s.Picker != nil:
		return livePickerLines(s, pal, layout)
	case s.Pending != nil:
		return liveApprovalLines(s, layout)
	}
	return nil
}

// liveDiffLines renders the inspector in output-first mode. The model already
// bounds its own frame to the budget the program set (see syncDiffSize), so the
// rows are taken as-is.
func liveDiffLines(s State) []string {
	if s.Diff == nil {
		return nil
	}
	return splitLines(s.Diff.View())
}

// liveStatusModalLines renders the /status card in output-first mode. It is
// centered horizontally and bounded to the live region so the card never
// overflows the screen.
func liveStatusModalLines(s State, pal *Palette, layout Layout) []string {
	if s.StatusModal == nil {
		return nil
	}
	return liveModalCardLines(s, pal, layout, func(width, height int) string {
		return RenderStatusModal(*s.StatusModal, pal, width, height)
	})
}

// liveUsageModalLines renders the /usage inspector in output-first mode with
// the same centering and bound as the status card.
func liveUsageModalLines(s State, pal *Palette, layout Layout) []string {
	if s.UsageModal == nil {
		return nil
	}
	return liveModalCardLinesWidth(s, pal, layout, statusCardWidth, func(width, height int) string {
		return RenderUsageModal(*s.UsageModal, pal, width, height)
	})
}

func liveInfoModalLines(s State, pal *Palette, layout Layout) []string {
	view := s.InfoModal
	if view == nil {
		view = s.InformationModal
	}
	if view == nil {
		return nil
	}
	return liveModalCardLinesWidth(s, pal, layout, statusCardWidth, func(width, height int) string {
		return RenderInfoModal(*view, pal, width, height)
	})
}

// liveSessionManagerLines renders the Session Manager in output-first mode
// with the same centering and bound as the read-only cards.
func liveSessionManagerLines(s State, pal *Palette, layout Layout) []string {
	if s.SessionModal == nil {
		return nil
	}
	return liveModalCardLinesWidth(s, pal, layout, sessionCardWidth, func(width, height int) string {
		return RenderSessionManager(*s.SessionModal, pal, width, height)
	})
}

// liveHelpCenterLines renders the Command & Help Center in output-first mode
// with the same centering and bound as the other working modals.
func liveHelpCenterLines(s State, pal *Palette, layout Layout) []string {
	if s.HelpModal == nil {
		return nil
	}
	return liveModalCardLinesWidth(s, pal, layout, helpCardWidth, func(width, height int) string {
		return RenderHelpCenter(*s.HelpModal, pal, width, height)
	})
}

// liveConnectModalLines renders the Provider Manager in output-first mode with
// the same centering and bound as the other working modals.
func liveConnectModalLines(s State, pal *Palette, layout Layout) []string {
	if s.ConnectModal == nil {
		return nil
	}
	return liveModalCardLinesWidth(s, pal, layout, connectCardWidth, func(width, height int) string {
		return RenderConnectModal(*s.ConnectModal, pal, width, height)
	})
}

// liveModalCardLines centers a card horizontally in the live region and trims
// it to the region's height, so a card taller than the viewport never pushes
// the scrollback.
func liveModalCardLines(s State, pal *Palette, layout Layout, build func(width, height int) string) []string {
	return liveModalCardLinesWidth(s, pal, layout, statusCardWidth, build)
}

// liveModalCardLinesWidth is liveModalCardLines with an explicit width
// resolver, so a card with a wider preferred frame keeps its own clamp.
func liveModalCardLinesWidth(s State, pal *Palette, layout Layout, widthFn func(int) int, build func(width, height int) string) []string {
	width := displayWidth(s, layout)
	limit := liveRows(s)
	card := build(widthFn(width), limit)
	cardLines := splitLines(card)
	placed := lipgloss.Place(width, len(cardLines), lipgloss.Center, lipgloss.Top, card)
	lines := splitLines(placed)
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	return lines
}

// livePickerLines renders the selection overlay. The question is the one row
// that must never be lost, so overflow is trimmed from the item list.
func livePickerLines(s State, pal *Palette, layout Layout) []string {
	picker := s.Picker
	if picker == nil {
		return nil
	}
	width := displayWidth(s, layout)
	head := pal.Heading.Render(TruncateToWidth(pickerHeader(picker), width))
	search := renderPickerSearch(picker, pal, width)
	foot := pal.Muted.Render(TruncateToWidth(pickerFooter(picker, layout), width))

	filtered := picker.Filtered()
	window, start := picker.windowOf(filtered)
	rows := make([]string, 0, len(window)+2)
	for i, item := range window {
		rows = append(rows, renderPickerRow(start+i, picker, item, pal, layout, width))
	}
	if hidden := len(filtered) - start - len(window); hidden > 0 {
		rows = append(rows, pal.Muted.Render(TruncateToWidth(fmt.Sprintf("  … %d more", hidden), width)))
	}
	if picker.Kind == PickerModels {
		if status := pickerCatalogStatus(picker); status != "" {
			rows = append(rows, pal.Muted.Render(TruncateToWidth(status, width)))
		}
	}
	room := liveRows(s) - 3 // header, search, footer
	if room < 0 {
		room = 0
	}
	if len(rows) > room {
		rows = rows[:room]
	}
	out := []string{head, search}
	out = append(out, rows...)
	return append(out, foot)
}

// liveApprovalLines renders the approval prompt, bounded to the screen.
func liveApprovalLines(s State, layout Layout) []string {
	var b strings.Builder
	bounded := layout
	bounded.Height = liveRows(s)
	renderApproval(&b, s.Pending, s.Theme, bounded)
	lines := splitLines(b.String())
	if limit := liveRows(s); len(lines) > limit {
		// Keep the question and the choices; drop the middle of the preview.
		kept := []string{lines[0]}
		return append(kept, lines[len(lines)-(limit-1):]...)
	}
	return lines
}

// liveQuitConfirmLines renders the /q quit-confirmation popup, bounded to
// the screen. It reuses the centered card so live and full-screen agree on
// the border, the horizontal buttons, and the left/right hint.
func liveQuitConfirmLines(s State, _ *Palette, layout Layout) []string {
	if s.QuitConfirm == nil {
		return nil
	}
	card := quitCard(s.QuitConfirm, quitCardWidth(displayWidth(s, layout)))
	lines := splitLines(card)
	if limit := liveRows(s); len(lines) > limit {
		lines = lines[:limit]
	}
	return lines
}

// liveTransitionLines renders the workspace-transition decision in
// output-first mode, bounded to the screen. It reuses the centered card so
// live and full-screen agree on the border, the buttons, and the key line.
func liveTransitionLines(s State) []string {
	if s.Transition == nil {
		return nil
	}
	card := transitionCard(s.Transition, transitionCardWidth(s.Width))
	lines := splitLines(card)
	if limit := liveRows(s); len(lines) > limit {
		lines = lines[:limit]
	}
	return lines
}

// liveConnectKeyLines renders the /connect API-key modal in output-first mode
// with the same centering and bound as the other working modals.
func liveConnectKeyLines(s State, pal *Palette, layout Layout) []string {
	if s.ConnectKey == nil {
		return nil
	}
	return liveModalCardLinesWidth(s, pal, layout, connectCardWidth, func(width, height int) string {
		return RenderConnectKeyModal(*s.ConnectKey, pal, width, height)
	})
}

// liveStatusLine is the one-line runtime footer. It names the phase in words
// so nothing depends on colour, and stays quiet at idle rather than repeating
// what the composer already shows.
func liveStatusLine(s State, pal *Palette, layout Layout) string {
	var parts []string
	// The activity line is the same pure projection the full-screen surface
	// paints, so both modes name the runtime state identically. The phase
	// word remains the fallback for the one non-idle state the activity line
	// deliberately does not claim: shutting down.
	if ts := ProjectThinking(s); ts.Active {
		now := time.Now()
		lines := RenderThinking(ts, now, 0)
		if shimmerSupported() {
			lines = RenderThinkingTick(ts, now, s.Status.Spinner, 0)
		}
		if len(lines) > 0 {
			parts = append(parts, lines[0])
		}
	} else if runtime := DeriveRuntime(s); runtime != RuntimeIdle {
		parts = append(parts, RuntimeMarker(runtime)+" "+string(runtime))
	}
	if s.Status.Model != "" {
		parts = append(parts, s.Status.Model)
	}
	// Token telemetry stays visible while streaming: the provider only
	// reports measured usage at turn completion, so the footer would sit
	// stale for the whole Responding phase without the in-flight estimate.
	// liveSessionOut/livePercent already fold OutputTokens in while busy and
	// pass through measured totals when idle.
	if s.SessionIn > 0 {
		parts = append(parts, "↑"+formatTokens(s.SessionIn))
	}
	if liveOut := liveSessionOut(s); liveOut > 0 {
		parts = append(parts, "↓"+formatTokens(liveOut))
	}
	if pct := livePercent(s); pct >= 0 {
		parts = append(parts, "ctx "+strconv.Itoa(pct)+"%")
	}
	// Path carries branch plus the compact worktree state ("proj:main !6
	// ?1") so live mode orients exactly like the full-screen hint bar;
	// unknown or clean stays quiet rather than inventing zeros.
	if path := sessionPathBranch(s); path != "" {
		parts = append(parts, path)
	}
	if len(parts) == 0 {
		return ""
	}
	return pal.Muted.Render(TruncateToWidth(strings.Join(parts, " · "), displayWidth(s, layout)))
}

// liveComposerWidth is the text budget after the "> " prompt.
func liveComposerWidth(layout Layout) int {
	w := layout.ContentWidth
	if w <= 0 {
		w = 80
	}
	if w < 3 {
		w = 3
	}
	return w - 2
}

// liveComposerLines renders the input block: the prompt, wrapped rows with the
// block cursor, inline command discovery, the queue line, and one hint row.
//
// It is the full-screen composer's geometry without its framing rules. Those
// rules existed to pin the composer to a virtual viewport; here the composer
// is simply the last thing on screen, which is where the terminal already
// puts it.
func liveComposerLines(s State, pal *Palette, layout Layout) []string {
	if s.ConnectKey != nil || s.Picker != nil || s.HelpModal != nil || s.StatusModal != nil || s.UsageModal != nil || s.InfoModal != nil || s.InformationModal != nil || s.Pending != nil || s.QuitConfirm != nil || s.Transition != nil {
		return nil
	}
	clean, cleanCursor := sanitizeWithCursor(s.Input, s.Cursor)
	rows, cursorRow, cursorRune, haveCursor := layoutComposer(clean, cleanCursor, liveComposerWidth(layout))
	// Same cursor-following window as the framed composer: the caret must stay
	// painted however far the input folds past the row cap.
	shown, start := composerWindow(rows, cursorRow, s.ComposerScroll, maxComposerRows)

	var out []string
	// The palette sits strictly above the input line it completes. It is
	// expanded into one element per row so liveChrome's row count matches
	// what the painter erases and rewrites.
	if suggest := renderPaletteSuggestions(s, layout); suggest != "" {
		out = append(out, strings.Split(suggest, "\n")...)
	}
	for i, row := range shown {
		text := row
		isCursorRow := haveCursor && start+i == cursorRow
		rowCursor := -1
		if isCursorRow {
			rowCursor = cursorRune
		}
		text, rowCursor = highlightMentions(pal.Mention, text, rowCursor)
		text, rowCursor = renderPasteChips(s.Pastes, pal.Chip, text, rowCursor)
		if isCursorRow {
			text = insertAt(text, rowCursor, pal.Cursor.Render("▌"))
		}
		if i == 0 {
			out = append(out, pal.Prompt.Render("> ")+text)
			continue
		}
		out = append(out, "  "+text)
	}
	if queued := composerStateLine(s, pal, layout); queued != "" {
		out = append(out, queued)
	}
	return append(out, pal.Muted.Render(liveHintsFor(s, layout)))
}

// liveHintsFor is the composer's hint row. It names only bindings that work
// in this mode, and no scrolling key is advertised: the terminal owns the
// wheel here and ohgo never asks it for it.
//
// The row is chosen by width, longest first, because a hint that overflows
// soft-wraps and breaks the live region's row count: the region is erased and
// rewritten from that count, so a row that silently becomes two would leave
// the composer a row below where the renderer looks for it.
func liveHintsFor(s State, layout Layout) string {
	var choices []string
	switch {
	case len(paletteEntriesFor(s)) > 0 && s.ConnectKey == nil:
		choices = []string{"↑↓ navigate · enter/tab accept · esc dismiss", "↑↓ enter esc"}
	case s.Status.Busy:
		choices = []string{"enter queue · esc cancel · alt+enter break", "enter queue · esc cancel"}
	default:
		// Idle hints share composerHintsFor, so the live surface gets the
		// same progressive disclosure: shortcuts fresh, telemetry once a
		// prompt has run. The short fallbacks keep a narrow terminal in one
		// row.
		choices = []string{composerHintsFor(s), "enter send"}
	}
	width := displayWidth(s, layout)
	for _, text := range choices {
		if CellWidth(text) <= width {
			return text
		}
	}
	return choices[len(choices)-1]
}

// printTranscript hands sealed rows to the renderer, which writes them above
// the live region so the terminal scrolls them into its scrollback.
func printTranscript(lines []string) tea.Cmd {
	return tea.Println(strings.Join(lines, "\n"))
}
