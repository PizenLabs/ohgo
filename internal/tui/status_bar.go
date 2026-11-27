package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The status bar is a projection of structured runtime state, not a sentence
// assembled at the call site. Every slot declares what it is worth, how it may
// degrade, and the runtime state it reports; the layout pass then decides what
// survives a given width. Priority is therefore a property of the information,
// not of where a string happened to be concatenated.

// statusPriority ranks a status item. A lower value survives a narrower frame,
// so lower-priority information always yields before higher-priority
// information.
type statusPriority int

const (
	// statusPriorityRuntime is P0: the runtime constraints and telemetry the
	// user acts on (duration, input/output tokens, context consumption, and
	// the measured cost the same accounting produced).
	statusPriorityRuntime statusPriority = iota
	// statusPriorityWorkspace is P1: repository identity and active branch.
	statusPriorityWorkspace
	// statusPrioritySession is P2: the turn/session marker.
	statusPrioritySession
	// statusPriorityAction is P3: contextual keys that work right now.
	statusPriorityAction
)

// statusTone is the semantic runtime state an item reports. It is independent
// of the item's category: duration and token counts are always neutral, and
// only real runtime pressure takes a signal role. The text always carries the
// meaning; a tone never communicates state on its own.
type statusTone int

const (
	statusToneNeutral statusTone = iota
	statusToneWarning
	statusToneDanger
)

// Context-pressure thresholds. The percentage is the meaning; the tone only
// adds redundant urgency, so a monochrome terminal still reads the number.
const (
	contextWarningPercent = 80
	contextDangerPercent  = 95
)

// Workspace item geometry. The preferred path prefix is bounded so an
// absolute project path cannot crowd runtime telemetry on an ordinary
// terminal; the fitter still spends whatever the allocation awards it.
const (
	workspacePreferredCells = 32
	workspaceMinCells       = 10
)

// statusKind is the stable identity of one footer slot. Selection,
// truncation, and rendering address fields; display text is never identity.
type statusKind string

const (
	statusKindDuration  statusKind = "duration"
	statusKindReview    statusKind = "review"
	statusKindInput     statusKind = "input"
	statusKindOutput    statusKind = "output"
	statusKindContext   statusKind = "context"
	statusKindCost      statusKind = "cost"
	statusKindTurn      statusKind = "turn"
	statusKindWorkspace statusKind = "workspace"
	statusKindAction    statusKind = "action"
	statusKindNotice    statusKind = "notice"
)

// statusItem is one measured cell of the status bar.
//
//	priority       what it is worth when width is scarce
//	visible        whether current runtime/UI state makes it relevant at all
//	tone           the semantic runtime role, never the item's category
//	truncatable    whether a partial rendering remains truthful
//	minWidth       the smallest truncation that still identifies something
//	preferredWidth the cells the item wants when nothing is constrained
//	fit            the truncation function; nil means atomic
type statusItem struct {
	field    statusKind
	text     string
	priority statusPriority
	tone     statusTone
	visible  bool

	truncatable    bool
	minWidth       int
	preferredWidth int
	fit            func(budget int) (string, bool)
}

// newStatusItem builds an atomic item: it renders whole or not at all. An
// empty text is invisible, so callers never fabricate a placeholder.
func newStatusItem(field statusKind, text string, priority statusPriority, tone statusTone) statusItem {
	text = strings.TrimSpace(text)
	width := CellWidth(text)
	return statusItem{
		field:          field,
		text:           text,
		priority:       priority,
		tone:           tone,
		visible:        text != "",
		minWidth:       width,
		preferredWidth: width,
	}
}

// statusSurface names the surface a projection feeds. The framed canvas owns
// transcript scrolling; output-first live mode hands scrolling to the terminal,
// so a scroll offset there is not a user-facing control and must not be
// advertised.
type statusSurface int

const (
	statusSurfaceCanvas statusSurface = iota
	statusSurfaceLive
)

// statusBarProjection is the state-derived footer: the telemetry slots, the
// contextual action rail, or a notice that owns the whole row.
type statusBarProjection struct {
	items   []statusItem
	actions []statusItem
	notice  *statusItem
}

// projectStatusBar derives the framed canvas footer from runtime/UI state.
func projectStatusBar(state State) statusBarProjection {
	return projectStatusBarFor(state, statusSurfaceCanvas)
}

// projectStatusBarFor derives the footer for one surface.
//
// Only state-relevant items are projected: a settled turn keeps the parked
// session marker, a running turn parks it; a fresh session invents no zero
// counters; an approval owns its own key line, so the footer has nothing to
// teach. The order of items is display order; the allocator may select them
// in a different order and still render them in this order.
func projectStatusBarFor(state State, surface statusSurface) statusBarProjection {
	switch {
	case state.Pending != nil || state.QuitConfirm != nil || state.Transition != nil ||
		state.UpdateRestart != nil || state.PostUpdate != nil:
		// The open dialog prints its own keys; advertising composer chords
		// here would contradict it.
		return statusBarProjection{}
	case interruptConfirmVisible(state):
		// The one key the user needs this instant replaces every metric.
		notice := newStatusItem(statusKindNotice, interruptConfirmText, statusPriorityAction, statusToneWarning)
		notice.truncatable = true
		notice.minWidth = 1
		return statusBarProjection{notice: &notice}
	case paletteOwnsKeys(state) && state.ConnectKey == nil:
		// The palette owns navigation; the row names only keys it handles.
		notice := newStatusItem(statusKindNotice, "↑↓ navigate · enter/tab accept · esc dismiss", statusPriorityAction, statusToneNeutral)
		notice.truncatable = true
		notice.minWidth = 1
		return statusBarProjection{notice: &notice}
	}

	p := statusBarProjection{}
	add := func(item statusItem) {
		if item.visible {
			p.items = append(p.items, item)
		}
	}

	if label := turnElapsedLabel(state); label != "" {
		add(newStatusItem(statusKindDuration, label, statusPriorityRuntime, statusToneNeutral))
	}
	if hasProvisionalAssistant(state) {
		// The candidate's prose is buffered until the publication decision.
		// The footer states what the runtime is doing instead of painting text
		// that may be withdrawn.
		add(newStatusItem(statusKindReview, "verifying evidence…", statusPriorityRuntime, statusToneNeutral))
	}
	if state.SessionIn > 0 {
		add(newStatusItem(statusKindInput, "↑"+formatTokens(state.SessionIn), statusPriorityRuntime, statusToneNeutral))
	}
	liveOut := liveSessionOut(state)
	if liveOut > 0 {
		add(newStatusItem(statusKindOutput, "↓"+formatTokens(liveOut), statusPriorityRuntime, statusToneNeutral))
	}
	// The context percentage is a runtime constraint, not decoration: it is
	// projected whenever a window is known and the session has reported
	// something, but a fresh session never invents a 0% budget.
	if pct := livePercent(state); pct >= 0 && (state.SessionActive || state.SessionIn > 0 || liveOut > 0) {
		item := newStatusItem(statusKindContext, fmt.Sprintf("%d%%", pct), statusPriorityRuntime, statusToneNeutral)
		switch {
		case pct >= contextDangerPercent:
			item.tone = statusToneDanger
		case pct >= contextWarningPercent:
			item.tone = statusToneWarning
		}
		add(item)
	}
	// Measured cost comes from the same accounting as the token counters, so
	// it is runtime telemetry; it still ranks after context consumption.
	if cost := sanitizeInline(strings.TrimSpace(state.Status.UsageText)); cost != "" {
		add(newStatusItem(statusKindCost, cost, statusPriorityRuntime, statusToneNeutral))
	}
	// The marker is session metadata: it parks while a turn runs, when the
	// row belongs to live telemetry and the action rail.
	if !state.Status.Busy {
		if session := sanitizeInline(strings.TrimSpace(state.Status.Session)); session != "" {
			add(newStatusItem(statusKindTurn, session, statusPrioritySession, statusToneNeutral))
		}
	}
	add(workspaceStatusItem(state))

	p.actions = statusActionItems(state, surface)
	return p
}

// workspaceStatusItem projects repository identity: shortened path, active
// branch, and measured worktree marks. It is the one truncatable telemetry
// neighbor, because a path may shed leading segments without lying.
func workspaceStatusItem(state State) statusItem {
	cwd := sanitizeInline(strings.TrimSpace(state.Status.Cwd))
	branch := sanitizeInline(strings.TrimSpace(state.Status.GitBranch))
	git := gitCompactStatus(state.Status)
	preferred := assembleWorkspace(shortenCwd(cwd, workspacePreferredCells), branch, git)

	item := newStatusItem(statusKindWorkspace, preferred, statusPriorityWorkspace, statusToneNeutral)
	if !item.visible {
		return item
	}
	item.truncatable = true
	item.minWidth = workspaceMinCells
	if item.preferredWidth < item.minWidth {
		item.minWidth = item.preferredWidth
	}
	item.fit = func(budget int) (string, bool) {
		if budget >= item.preferredWidth {
			return preferred, true
		}
		return fitWorkspacePath(cwd, branch, git, budget)
	}
	return item
}

// statusActionItems projects the contextual action rail.
//
// Only actions that work in the state they are shown in appear: a fresh
// composer teaches discovery, a draft teaches its submit key, a scrolled
// viewport teaches the way back, and an active turn leads with the cancel
// chord. The row may legitimately be empty.
func statusActionItems(state State, surface statusSurface) []statusItem {
	var actions []statusItem
	add := func(text string) {
		actions = append(actions, newStatusItem(statusKindAction, text, statusPriorityAction, statusToneNeutral))
	}
	typing := strings.TrimSpace(state.Input) != ""
	// A scroll offset is a control only where ohgo owns the viewport. In live
	// mode the terminal's scrollback is the viewport, so the hint would be a
	// lie there.
	scrolled := surface == statusSurfaceCanvas && state.Scroll > 0 && !state.Follow

	if state.Status.Busy {
		// Cancel outranks every other key while a turn owns the runtime.
		add("esc cancel")
		if typing {
			// Enter queues instead of sending, and a newline needs its chord:
			// both only mean something once there is a draft to act on.
			add("alt+enter break")
			add("enter queue")
		}
		if scrolled {
			add("end bottom")
		}
		return actions
	}

	if scrolled {
		add("end bottom")
	}
	if typing {
		add("enter send")
		return actions
	}
	// "?" and "^p" are composer bindings: they work on an empty composer, not
	// in the middle of a question. A fresh session's ^p opens the settings
	// hub; once history exists it recalls history, so the hint would lie.
	add("? help")
	if !state.SessionActive && len(state.Submitted) == 0 {
		add("^p settings")
	}
	return actions
}

// statusBarLayout is the measured selection for one width.
type statusBarLayout struct {
	left   []statusItem
	right  []statusItem
	notice *statusItem
}

// layoutStatusBar budgets width before rendering.
//
// Items are allocated one priority tier at a time, in display order inside a
// tier. An atomic item that does not fit is omitted whole; the only
// truncatable item (the workspace path) is fitted to the exact remainder and
// dropped if it cannot stay identifiable. Contextual actions are allocated
// last from what the telemetry did not claim, and are never clipped.
func layoutStatusBar(p statusBarProjection, width int) statusBarLayout {
	if p.notice != nil {
		return statusBarLayout{notice: p.notice}
	}
	// Copy: fitting rewrites an item's text, and the projection may be reused.
	items := append([]statusItem(nil), p.items...)
	chosen := make([]bool, len(items))
	if width <= 0 {
		for i, item := range items {
			chosen[i] = item.visible
		}
	} else {
		for _, priority := range []statusPriority{statusPriorityRuntime, statusPriorityWorkspace, statusPrioritySession} {
			for i := range items {
				item := items[i]
				if !item.visible || item.priority != priority {
					continue
				}
				chosen[i] = true
				if statusItemsWidth(items, chosen, -1) <= width {
					continue
				}
				base := statusItemsWidth(items, chosen, i)
				withItem := statusItemsWidth(items, chosen, -1)
				separator := withItem - base - CellWidth(item.text)
				budget := width - base - separator
				fitted := false
				if item.truncatable && item.fit != nil && budget >= item.minWidth {
					if text, ok := item.fit(budget); ok && CellWidth(text) <= budget {
						items[i].text = text
						if statusItemsWidth(items, chosen, -1) <= width {
							fitted = true
						}
					}
				}
				if !fitted {
					chosen[i] = false
				}
			}
		}
	}

	layout := statusBarLayout{}
	for i, item := range items {
		if chosen[i] {
			layout.left = append(layout.left, item)
		}
	}
	leftWidth := statusItemsWidth(items, chosen, -1)
	layout.right = selectStatusActions(p.actions, leftWidth, width)
	return layout
}

// statusItemsWidth measures the chosen items in display order, skipping one
// index so an allocation can price a candidate against its own base.
func statusItemsWidth(items []statusItem, chosen []bool, skip int) int {
	width := 0
	previous := statusKind("")
	for i, item := range items {
		if i == skip || !chosen[i] {
			continue
		}
		if previous != "" {
			width += CellWidth(statusSeparator(previous))
		}
		width += CellWidth(item.text)
		previous = item.field
	}
	return width
}

// selectStatusActions awards the action rail whatever the telemetry left,
// whole hint by whole hint. A partial key hint ("alt+enter bre…") teaches
// nothing, so an action that does not fit is omitted entirely; a shorter one
// later in the rail may still be shown, because the hints are independent
// bindings rather than fragments of one sentence.
func selectStatusActions(actions []statusItem, leftWidth, width int) []statusItem {
	visible := make([]statusItem, 0, len(actions))
	for _, action := range actions {
		if action.visible {
			visible = append(visible, action)
		}
	}
	if width <= 0 {
		return visible
	}
	remaining := width - leftWidth
	var chosen []statusItem
	used := 0
	for _, action := range visible {
		need := CellWidth(action.text)
		if len(chosen) == 0 {
			if leftWidth > 0 {
				need += 2 // one clean gap before the rail
			}
		} else {
			need += CellWidth(statusSeparator(statusKindAction))
		}
		if used+need > remaining {
			continue
		}
		used += need
		chosen = append(chosen, action)
	}
	return chosen
}

// statusSeparator is the join between one item and the next. The timer is a
// turn-level anchor and keeps its own wider gap; every other pair uses the
// compact middle dot.
func statusSeparator(previous statusKind) string {
	if previous == statusKindDuration {
		return "  "
	}
	return " · "
}

// joinStatusItems renders items in display order.
func joinStatusItems(items []statusItem) string {
	var sb strings.Builder
	for i, item := range items {
		if i > 0 {
			sb.WriteString(statusSeparator(items[i-1].field))
		}
		sb.WriteString(item.text)
	}
	return sb.String()
}

// statusZones reports the unbounded zones for a state. Narrow callers use the
// measured form; this is the full projection, used by tests that assert which
// facts exist rather than which fit.
func statusZones(state State) (left, right string) {
	return statusZonesFor(projectStatusBar(state))
}

// statusZonesFor reports the unbounded zones of one projection.
func statusZonesFor(p statusBarProjection) (left, right string) {
	if p.notice != nil {
		return p.notice.text, ""
	}
	layout := layoutStatusBar(p, 0)
	return joinStatusItems(layout.left), joinStatusItems(layout.right)
}

// statusLineFor joins the two zones into one unmeasured row, which the live
// region needs when it cannot spend the full frame width on a split.
func statusLineFor(p statusBarProjection) string {
	left, right := statusZonesFor(p)
	switch {
	case left == "":
		return right
	case right == "":
		return left
	default:
		return left + " · " + right
	}
}

// renderStatusBar is the single footer renderer: project, budget, style, and
// compose. A nil palette renders plain text, which is what the live region
// and tests need; a styled row keeps telemetry in the readable muted role,
// action hints in faint, and only genuine context pressure in a signal role.
func renderStatusBar(state State, pal *Palette, width int) string {
	p := projectStatusBar(state)
	layout := layoutStatusBar(p, width)

	if layout.notice != nil {
		text := layout.notice.text
		if width > 0 {
			text = TruncateToWidth(text, width)
		}
		if pal == nil {
			return text
		}
		return statusToneStyle(pal, layout.notice.tone).Render(text)
	}

	left := joinStatusItems(layout.left)
	right := joinStatusItems(layout.right)
	if pal != nil {
		left = styleStatusItems(pal, layout.left)
		// The rail is one focus tier below the metrics: available without
		// competing with the telemetry the user must act on.
		if right != "" {
			right = pal.Faint.Render(right)
		}
	}
	return sessionBarRow(left, right, width)
}

// styleStatusItems renders items with their semantic tone, grouping adjacent
// neutral items so an ordinary row stays one quiet span rather than a run of
// resets.
func styleStatusItems(pal *Palette, items []statusItem) string {
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && items[j].tone == items[i].tone {
			j++
		}
		var run strings.Builder
		if i > 0 {
			// The separator before a run belongs to that run's tone, so a
			// styled warning keeps its leading divider.
			run.WriteString(statusSeparator(items[i-1].field))
		}
		run.WriteString(joinStatusItems(items[i:j]))
		sb.WriteString(statusToneStyle(pal, items[i].tone).Render(run.String()))
		i = j
	}
	return sb.String()
}

// statusToneStyle resolves the semantic role for one tone from the active
// palette, not from a category. Neutral telemetry is the readable muted role;
// warning and danger are the only semantic signals the footer uses, and they
// always accompany a number or word.
func statusToneStyle(pal *Palette, tone statusTone) lipgloss.Style {
	switch tone {
	case statusToneWarning:
		return pal.Warning
	case statusToneDanger:
		return pal.ToolError
	default:
		return pal.Muted
	}
}

// assembleWorkspace joins the path, branch, and measured worktree marks.
func assembleWorkspace(cwd, branch, git string) string {
	base := cwd
	if branch != "" {
		if base != "" {
			base += ":"
		}
		base += branch
	}
	if git != "" {
		if base != "" {
			base += " "
		}
		base += git
	}
	return base
}

// fitWorkspacePath renders the workspace item inside budget cells.
//
// Degradation is deliberate. Worktree marks are detail and go first, then
// leading path segments ("…/Golang/go-ecommerce" before "…/go-ecommerce"),
// then the branch is shortened around a surviving repository leaf. The path is
// never allowed to outlive the runtime telemetry it would displace.
func fitWorkspacePath(cwd, branch, git string, budget int) (string, bool) {
	if budget < workspaceMinCells {
		return "", false
	}
	if full := assembleWorkspace(cwd, branch, git); CellWidth(full) <= budget {
		return full, true
	}
	if git != "" {
		if identity := assembleWorkspace(cwd, branch, ""); CellWidth(identity) <= budget {
			return identity, true
		}
	}
	if branch != "" {
		headBudget := budget - CellWidth(branch) - 1 // ":" before the branch
		if headBudget >= 4 {
			if head := pathHead(cwd, headBudget); head != "" {
				if out := head + ":" + branch; CellWidth(out) <= budget {
					return out, true
				}
			}
		}
		// The leaf plus a shortened branch still names the workspace.
		if leaf := pathLeaf(cwd); leaf != "" {
			reserved := 2 + CellWidth(leaf) + 1 // "…/" + leaf + ":"
			if branchBudget := budget - reserved; branchBudget >= 2 {
				if shortened := TruncateToWidth(branch, branchBudget); shortened != "" {
					return "…/" + leaf + ":" + shortened, true
				}
			}
		}
		// The branch outlives the path when nothing else fits.
		if shortened := TruncateToWidth(branch, budget); CellWidth(shortened) <= budget {
			return shortened, true
		}
		return "", false
	}
	if head := pathHead(cwd, budget); head != "" {
		return head, true
	}
	return "", false
}

// pathHead shortens a path from the left: the tail is what orients, so the
// deepest two segments survive before the leaf alone, and the leaf is
// ellipsised only as the last resort.
func pathHead(path string, budget int) string {
	if budget < 4 || path == "" {
		return ""
	}
	if CellWidth(path) <= budget {
		return path
	}
	if two := "…/" + lastTwo(path); CellWidth(two) <= budget {
		return two
	}
	leaf := pathLeaf(path)
	if leaf == "" {
		return ""
	}
	if short := "…/" + leaf; CellWidth(short) <= budget {
		return short
	}
	return "…/" + TruncateToWidth(leaf, budget-2)
}

// pathLeaf reports the deepest path segment.
func pathLeaf(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
