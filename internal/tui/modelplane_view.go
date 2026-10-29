package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ModalRect is the centered dialog rectangle for the model control plane,
// in terminal cells with the origin at the top-left of the terminal.
type ModalRect struct {
	// X is the left column of the modal, including its border.
	X int
	// Y is the top row of the modal, including its border.
	Y int
	// Width is the full modal width, including borders.
	Width int
	// Height is the full modal height, including borders.
	Height int
}

// Modal geometry constants. The dialog is responsive: it grows with the
// terminal instead of holding a fixed preferred size, so the model list and
// the two-column metadata keep reading space on roomy terminals and still
// draw on small ones. The formulas are the contract:
//
//	modalWidth = clamp(76, viewportWidth-8, 108)
//	modalHeight = clamp(22, viewportHeight-4, 44)
const (
	// planeModalLoWidth and planeModalHiWidth bound the horizontal clamp.
	planeModalLoWidth = 76
	// planeModalMaxWidth caps the dialog so ultrawide terminals keep
	// conversation context on both sides.
	planeModalMaxWidth = 108
	// planeModalLoHeight and planeModalMaxHeight bound the vertical clamp.
	planeModalLoHeight = 22
	// planeModalMaxHeight caps the dialog so short context stays visible
	// above and below.
	planeModalMaxHeight = 44
	// planeModalMarginX is the total horizontal margin (both sides).
	planeModalMarginX = 8
	// planeModalMarginY is the total vertical margin (header/status plus
	// air around the dialog).
	planeModalMarginY = 4
	// planeModalMinWidth is the narrowest dialog that still draws.
	planeModalMinWidth = 24
	// planeModalMinHeight is the shortest dialog: borders, title, one
	// content row, footer.
	planeModalMinHeight = 6
)

// PlaneModalRect returns the centered modal rectangle for a terminal size.
//
// It follows the responsive clamp, never touches the terminal edges while
// margins fit, and always stays inside the terminal. All model-plane layout
// derives from it; no pane decides its own terminal coordinates.
func PlaneModalRect(termWidth, termHeight int) ModalRect {
	if termWidth <= 0 || termHeight <= 0 {
		return ModalRect{}
	}
	width := clampInt(termWidth-planeModalMarginX, planeModalLoWidth, planeModalMaxWidth)
	height := clampInt(termHeight-planeModalMarginY, planeModalLoHeight, planeModalMaxHeight)
	// The dialog shares the screen with the header and status chrome, so
	// it is further bounded by the conversation area it overlays.
	if conv := termHeight - 3; conv < height {
		height = conv
	}
	if width < planeModalMinWidth {
		width = planeModalMinWidth
		if width > termWidth {
			width = termWidth
		}
		if width < 10 {
			width = 10
		}
	}
	if height < planeModalMinHeight {
		height = planeModalMinHeight
		if height > termHeight {
			height = termHeight
		}
		if height < 4 {
			height = 4
		}
	}
	if width > termWidth {
		width = termWidth
	}
	if height > termHeight {
		height = termHeight
	}
	x := (termWidth - width) / 2
	if x < 0 {
		x = 0
	}
	// Center in the conversation band below the two-row header so the
	// header stays visible above the dialog.
	y := 2 + (termHeight-3-height)/2
	if y < 0 {
		y = 0
	}
	if y+height > termHeight {
		y = termHeight - height
	}
	if y < 0 {
		y = 0
	}
	return ModalRect{X: x, Y: y, Width: width, Height: height}
}

// ModelPickerRect is the picker-geometry entry point behind MODEL CONTROL.
// It shares the plane's responsive clamp so the checklist's expanded
// dimensions (76x22 min to 108x44 max) hold on every caller.
func ModelPickerRect(termWidth, termHeight int) ModalRect {
	return PlaneModalRect(termWidth, termHeight)
}

// planeModalTitle names the active mode for the border title. The title is
// derived, never stored: browsing shows search or providers by focus, the
// roles mode and its selection steps show roles, and the thinking step shows
// effort, so the border always says where the next key operates.
func planeModalTitle(plane *ModelPlaneState) string {
	if plane == nil {
		return "MODEL CONTROL · search"
	}
	if plane.Auth != nil {
		return "MODEL CONTROL · providers"
	}
	if plane.Variant != nil {
		return "MODEL CONTROL · search"
	}
	switch plane.Stage {
	case PlaneStageEffort, PlaneStageApply:
		return "MODEL CONTROL · effort"
	case PlaneStageRole, PlaneStageModel:
		return "MODEL CONTROL · roles"
	}
	if plane.Mode == PlaneRoles {
		return "MODEL CONTROL · roles"
	}
	switch plane.Focus {
	case PlaneFocusProviders:
		return "MODEL CONTROL · providers"
	case PlaneFocusEffort:
		return "MODEL CONTROL · effort"
	case PlaneFocusRoles:
		return "MODEL CONTROL · roles"
	default:
		return "MODEL CONTROL · search"
	}
}

// modalTitleBorder draws a modal's top border with a centered title in the
// ASK cyan accent. The border itself is the title row, so it returns one
// vertical cell to the body. The model control plane and the /status card
// share it so their frames read as one family.
func modalTitleBorder(interior int, title string) string {
	title = sanitizeInline(title)
	const prefix = " "
	const suffix = " "
	full := prefix + title + suffix
	fullW := CellWidth(full)
	if interior <= 0 {
		return "┌┐"
	}
	if interior <= fullW {
		t := TruncateToWidth(title, interior)
		if gap := interior - CellWidth(t); gap > 0 {
			t += strings.Repeat(" ", gap)
		}
		return "┌" + WorkspaceBadge("37", t) + "┐"
	}
	left := (interior - fullW) / 2
	right := interior - fullW - left
	styled := WorkspaceBadge("37", full)
	return "┌" + strings.Repeat("─", left) + styled + strings.Repeat("─", right) + "┐"
}

// planeModalColumns splits the modal interior (excluding borders) into left
// and right panes. Narrow interiors stack into a single focused pane instead
// of drawing impossible nested rectangles.
func planeModalColumns(interiorWidth int) (left, right int, stacked bool) {
	if interiorWidth < 28 {
		return interiorWidth, 0, true
	}
	left = interiorWidth / 4
	if left < 12 {
		left = 12
	}
	if left > 28 {
		left = 28
	}
	if left > interiorWidth-12 {
		left = interiorWidth - 12
	}
	if left < 10 {
		left = 10
	}
	right = interiorWidth - left - 1
	if right < 10 {
		// The detail pane must stay usable: steal back rather than draw a
		// one-column pane beside the provider list.
		right = 10
		left = interiorWidth - right - 1
	}
	return left, right, false
}

// renderPlaneArea draws the model control plane as a centered modal dialog
// over the ohgo TUI. The header and status chrome keep rendering around it,
// and the underlying conversation state is untouched: closing the modal
// returns to the previous surface without losing anything.
func renderPlaneArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	plane := state.Plane
	if state.Width <= 0 || state.Height <= 0 {
		renderFullPlaneArea(sb, state, pal, layout)
		return
	}
	rect := PlaneModalRect(state.Width, state.Height)
	if rect.Width <= 0 || rect.Height <= 0 {
		renderFullPlaneArea(sb, state, pal, layout)
		return
	}
	modal := renderPlaneModal(plane, pal, state, layout, rect)

	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	// The modal is centered in the conversation band; rows above and below
	// stay blank so the header and status keep their pinned positions.
	topPad := (avail - len(modal)) / 2
	if topPad < 0 {
		topPad = 0
	}
	lines := make([]string, 0, avail)
	for i := 0; i < topPad && len(lines) < avail; i++ {
		lines = append(lines, "")
	}
	for _, line := range modal {
		if len(lines) >= avail {
			break
		}
		lines = append(lines, line)
	}
	for len(lines) < avail {
		lines = append(lines, "")
	}
	// Truncate overflow from the middle-out: the modal borders and footer
	// survive while blank padding yields first.
	if len(lines) > avail {
		lines = lines[:avail]
	}
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
}

// renderFullPlaneArea is the unknown-size fallback: without terminal
// dimensions there is nothing to center in, so the panes render full-width.
// Tests without a size exercise content here; real terminals use the modal.
func renderFullPlaneArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	plane := state.Plane
	leftWidth, rightWidth := planeColumns(state, layout)

	var left, right []string
	if plane.Mode == PlaneRoles {
		left = renderPlaneRolesPane(plane, pal, leftWidth)
		right = renderPlaneAssignmentPane(plane, pal, rightWidth)
	} else {
		left = renderPlaneProvidersPane(plane, pal, state, leftWidth)
		right = renderPlaneModelsPane(plane, pal, state, layout, rightWidth)
	}

	lines := zipColumns(left, right, leftWidth, pal.Muted.Render("│"))
	if plane.Auth != nil {
		// The unknown-size fallback has no modal to replace, so the
		// prompt lands below the panes where tests and narrow renders
		// can still see its masked input.
		width := displayWidth(state, layout)
		if width < 20 {
			width = 20
		}
		lines = append(lines, renderPlaneAuthPane(plane, pal, width, 4)...)
	}

	// The overlay fills its budget exactly: a footer summarises the key
	// contract and the last outcome, short content pads below so the status
	// line stays pinned, and overflow yields rows from the bottom so the
	// panes' headings and lists survive.
	footer := planeFooter(plane, pal, displayWidth(state, layout))
	if state.Height > 0 {
		avail := layout.ConversationHeight
		if state.Pending != nil {
			avail -= approvalLines(state.Pending, layout)
		}
		if avail < 0 {
			avail = 0
		}
		if len(lines) > avail-1 {
			lines = lines[:maxInt(avail-1, 0)]
		}
		lines = append(lines, footer)
		for len(lines) < avail {
			lines = append(lines, "")
		}
	} else {
		lines = append(lines, footer)
	}
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
}

// renderPlaneModal builds the dialog rows: a border-embedded title naming
// the active mode, the split panes fitted to the interior, a divider, and a
// footer with the keys that actually work. Every row is exactly rect.Width
// cells.
func renderPlaneModal(plane *ModelPlaneState, pal *Palette, state State, layout Layout, rect ModalRect) []string {
	interior := rect.Width - 2
	if interior < 1 {
		interior = 1
	}
	// Top border carries the centered title, so the body gains its row:
	// borders (2) + footer (1).
	contentH := rect.Height - 3
	if contentH < 1 {
		contentH = 1
	}
	// The divider above the footer claims one body row so the footer reads
	// apart from the panes without costing the list its detail.
	bodyH := contentH - 1
	if bodyH < 1 {
		bodyH = 1
	}
	leftW, rightW, stacked := planeModalColumns(interior)

	var body []string
	switch {
	case plane.Auth != nil:
		// The key prompt replaces the body wherever it was opened from:
		// entry must stay visible and masked while nothing behind moves.
		body = fitPane(renderPlaneAuthPane(plane, pal, interior, bodyH), bodyH)
	case plane.Variant != nil:
		// The variant prompt replaces the body wherever it was opened
		// from: choosing must stay visible in both flows.
		if stacked {
			body = fitPane(renderPlaneVariantPane(plane, pal, interior, bodyH), bodyH)
		} else {
			left := fitPane(renderPlaneProvidersPane(plane, pal, state, leftW), bodyH)
			right := fitPane(renderPlaneVariantPane(plane, pal, rightW, bodyH), bodyH)
			body = zipColumns(left, right, leftW, pal.Muted.Render("│"))
		}
	case plane.Stage != PlaneStageBrowse:
		// The assignment flow is a single bounded body: progressive
		// disclosure of one step at a time, inside the same dialog.
		body = fitPane(renderPlaneFlow(plane, pal, interior, bodyH), bodyH)
	case stacked:
		body = renderPlaneStacked(plane, pal, state, layout, interior, bodyH)
	case plane.Mode == PlaneRoles:
		left := fitPane(renderPlaneRolesPane(plane, pal, leftW), bodyH)
		right := fitPane(renderPlaneAssignmentPane(plane, pal, rightW), bodyH)
		body = zipColumns(left, right, leftW, pal.Muted.Render("│"))
	default:
		left := fitPane(renderPlaneProvidersPane(plane, pal, state, leftW), bodyH)
		right := fitPane(renderPlaneModelsPaneBudget(plane, pal, state, layout, rightW, planeModalDetailBudget(plane, bodyH)), bodyH)
		body = zipColumns(left, right, leftW, pal.Muted.Render("│"))
	}

	footer := planeFooter(plane, pal, interior)
	if gap := interior - CellWidth(footer); gap > 0 {
		footer += strings.Repeat(" ", gap)
	}
	divider := pal.Muted.Render(TruncateToWidth(" "+strings.Repeat("─", maxInt(interior-2, 1))+" ", interior))

	out := make([]string, 0, rect.Height)
	out = append(out, modalTitleBorder(interior, planeModalTitle(plane)))
	for _, line := range body {
		// Body rows carry their own styling; pad with plain spaces so the
		// outer borders land in the same column on every row.
		if gap := interior - CellWidth(line); gap > 0 {
			line += strings.Repeat(" ", gap)
		}
		out = append(out, "│"+TruncateToWidth(line, interior)+"│")
	}
	out = append(out, "│"+TruncateToWidth(divider, interior)+"│")
	out = append(out, "│"+TruncateToWidth(footer, interior)+"│")
	out = append(out, "└"+strings.Repeat("─", interior)+"┘")
	// Center horizontally within the terminal: margins on both sides keep
	// the dialog off the edges and the background chrome conceptually
	// visible around it.
	padLeft := rect.X
	termW := state.Width
	if termW <= 0 {
		termW = rect.Width
	}
	for i, line := range out {
		if padLeft > 0 {
			room := termW - CellWidth(line) - padLeft
			if room < 0 {
				room = 0
			}
			out[i] = strings.Repeat(" ", padLeft) + line + strings.Repeat(" ", room)
		} else if CellWidth(line) < termW {
			out[i] = line + strings.Repeat(" ", termW-CellWidth(line))
		}
	}
	return out
}

// planeModalDetailBudget sizes the model detail block from the modal
// interior: whatever the list chrome does not claim goes to detail, so the
// role line and the effort radio survive at normal sizes. Tiny dialogs get
// at least one row; fitPane truncates any remainder.
func planeModalDetailBudget(plane *ModelPlaneState, contentH int) int {
	rows := plane.VisibleRows
	if rows <= 0 {
		rows = len(plane.VisibleModels())
	}
	budget := contentH - 1 - rows - 1
	if len(plane.VisibleModels()) > rows {
		budget-- // the overflow marker claims a row
	}
	if budget < 1 {
		budget = 1
	}
	return budget
}

// renderPlaneStacked renders the single focused pane full-width when the
// modal is too narrow for a split. The focused control decides what shows,
// so navigation stays explicit instead of drawing clipped panes. An open
// variant prompt takes the whole interior: choosing must stay visible.
func renderPlaneStacked(plane *ModelPlaneState, pal *Palette, state State, layout Layout, width, contentH int) []string {
	if plane.Auth != nil {
		return fitPane(renderPlaneAuthPane(plane, pal, width, contentH), contentH)
	}
	if plane.Variant != nil {
		return fitPane(renderPlaneVariantPane(plane, pal, width, contentH), contentH)
	}
	var lines []string
	switch plane.Focus {
	case PlaneFocusProviders, PlaneFocusSearch:
		lines = renderPlaneProvidersPane(plane, pal, state, width)
	case PlaneFocusRoles:
		lines = append(lines, renderPlaneRolesPane(plane, pal, width)...)
		lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
		lines = append(lines, renderPlaneAssignmentPane(plane, pal, width)...)
	default:
		lines = renderPlaneModelsPane(plane, pal, state, layout, width)
	}
	return fitPane(lines, contentH)
}

// renderPlaneFlow draws the linear assignment flow: the choices already made,
// then the control for the current step. The step's control is guaranteed a
// row even in the smallest dialog, and its cursor row is never scrolled off, so
// the highlighted control can never be invisible.
func renderPlaneFlow(plane *ModelPlaneState, pal *Palette, width, contentH int) []string {
	var header []string
	if plane.PendingModel.ID != "" {
		header = append(header, flowField(pal, width, "Model", planeModelName(plane.PendingModel)))
	}
	if plane.ActiveRole != "" && plane.Stage != PlaneStageRole {
		header = append(header, flowField(pal, width, "Role", strings.ToUpper(plane.ActiveRole)))
	}
	// The control comes first: shed summary rows before the step can be lost.
	if max := contentH - 1; max < 0 {
		header = nil
	} else if len(header) > max {
		header = header[:max]
	}
	budget := contentH - len(header)
	if budget < 1 {
		budget = 1
	}

	var step []string
	switch plane.Stage {
	case PlaneStageRole:
		rows := roleFlowRows(plane, pal, width)
		step = headedRows(pal, width, "Assign to role", rows, plane.RoleCursor, budget)
	case PlaneStageModel:
		rows := modelFlowRows(plane, pal, width)
		rows = append([]string{renderPlaneSearch(plane, pal, width)}, rows...)
		step = headedRows(pal, width, "Select model for "+strings.ToUpper(plane.ActiveRole), rows, plane.ModelCursor+1, budget)
	case PlaneStageEffort:
		rows := effortFlowRows(plane, pal, width)
		step = headedRows(pal, width, "Thinking effort", rows, plane.EffortCursor, budget)
	case PlaneStageApply:
		step = []string{TruncateToWidth("  "+pal.Bold.Render("Enter")+pal.Muted.Render(" applies the assignment"), width)}
	}
	return append(header, step...)
}

// headedRows renders a step heading and its control rows, windowed to budget so
// the cursor row is always visible. With a single row of budget the heading
// yields: the control is the thing that must never be lost.
func headedRows(pal *Palette, width int, heading string, rows []string, cursor, budget int) []string {
	if budget <= 0 {
		return nil
	}
	if budget == 1 {
		return cursorRows(rows, cursor, 1)
	}
	out := make([]string, 0, budget)
	out = append(out, pal.Muted.Render(TruncateToWidth(heading, width)))
	out = append(out, cursorRows(rows, cursor, budget-1)...)
	return out
}

// cursorRows returns at most size rows of a list with the cursor row kept in
// view. Short lists pass through untouched.
func cursorRows(rows []string, cursor, size int) []string {
	if size <= 0 || len(rows) <= size {
		return rows
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	start := cursor - size + 1
	if start < 0 {
		start = 0
	}
	if start+size > len(rows) {
		start = len(rows) - size
	}
	return rows[start : start+size]
}

// flowField renders one "label value" row of the flow summary.
func flowField(pal *Palette, width int, label, value string) string {
	return TruncateToWidth("  "+pal.Muted.Render(padLabel(label))+pal.Bold.Render(sanitizeInline(value)), width)
}

// planeModelName names a model row for humans, falling back through its fields.
func planeModelName(model ModelOption) string {
	if model.Name != "" {
		return model.Name
	}
	if model.Title != "" {
		return model.Title
	}
	return model.ID
}

// roleFlowRows renders the role choices of the role step.
func roleFlowRows(plane *ModelPlaneState, pal *Palette, width int) []string {
	out := make([]string, 0, len(plane.Roles))
	for i, role := range plane.Roles {
		marker := "  "
		if i == plane.RoleCursor {
			marker = pal.Selected.Render("> ")
		}
		name := strings.ToUpper(sanitizeInline(role.Role))
		row := marker + pal.Bold.Render(name)
		model := sanitizeInline(planeModelNameFromRole(role))
		if model != "" {
			if room := width - CellWidth(row) - CellWidth(model) - 3; room >= 1 {
				row += strings.Repeat(" ", room) + pal.Muted.Render(model)
			}
		}
		out = append(out, TruncateToWidth(row, width))
	}
	return out
}

// planeModelNameFromRole names the model a role currently resolves to.
func planeModelNameFromRole(role RoleAssignment) string {
	if role.Name != "" {
		return role.Name
	}
	if role.Model != "" {
		return role.Model
	}
	return "unset"
}

// modelFlowRows renders the model choices of the roles-flow model step, windowed
// around the highlight so the selected row is always visible.
func modelFlowRows(plane *ModelPlaneState, pal *Palette, width int) []string {
	visible := plane.VisibleModels()
	if len(visible) == 0 {
		return []string{pal.Muted.Render(TruncateToWidth("  (no matches — esc backs out)", width))}
	}
	rows := plane.VisibleRows
	if rows <= 0 || rows > len(visible) {
		rows = len(visible)
	}
	window, start := window(visible, plane.ModelOffset, rows)
	out := make([]string, 0, len(window)+1)
	for i, model := range window {
		out = append(out, renderPlaneModelRow(start+i, plane, model, pal, width))
	}
	if hidden := len(visible) - start - len(window); hidden > 0 {
		out = append(out, pal.Muted.Render(TruncateToWidth(fmt.Sprintf("  … %d more", hidden), width)))
	}
	return out
}

// effortFlowRows renders the effort ladder of the effort step.
func effortFlowRows(plane *ModelPlaneState, pal *Palette, width int) []string {
	options := plane.EffortOptions()
	if len(options) == 0 {
		return []string{pal.Muted.Render(TruncateToWidth("  this model exposes no thinking effort", width))}
	}
	out := make([]string, 0, len(options))
	for i, option := range options {
		marker := "  "
		choice := pal.Muted.Render("○ " + option)
		if i == plane.EffortCursor {
			marker = pal.Selected.Render("> ")
			choice = pal.Selected.Render("● " + option)
		}
		out = append(out, TruncateToWidth(marker+choice, width))
	}
	return out
}

// fitPane pins a pane to exactly height rows: short content pads below,
// overflow yields from the bottom so headings survive.
func fitPane(lines []string, height int) []string {
	if height < 0 {
		height = 0
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// planeFooter names the keys that actually work and the last outcome, so the
// user always knows where they are and what an action did. Printable
// shortcuts are deliberately absent: with a search input on screen, only
// non-printable navigation keys are advertised.
func planeFooter(plane *ModelPlaneState, pal *Palette, width int) string {
	hints := "←→ pane  ·  ↑↓ select  ·  Enter choose  ·  Tab roles  ·  Esc exit"
	if plane.Auth != nil {
		hints = "Enter save  ·  Esc cancel"
	} else if plane.Variant != nil {
		hints = "↑↓ choose variant · Enter confirm · Esc cancel"
	} else if plane.Stage == PlaneStageRole {
		hints = "↑↓ role · Enter choose · Esc back"
	} else if plane.Stage == PlaneStageModel {
		hints = "type to filter · ↑↓ model · Enter choose · Esc back"
	} else if plane.Stage == PlaneStageEffort {
		hints = "↑↓ effort · Enter apply · Esc back"
	} else if plane.Stage == PlaneStageApply {
		hints = "Enter apply · Esc back"
	} else if plane.Mode == PlaneRoles {
		hints = "↑↓ role · Enter assign model · Tab models · Esc back"
	} else if plane.Stage == PlaneStageBrowse && plane.Mode == PlaneModels && plane.Focus == PlaneFocusProviders {
		hints = "←→ pane  ·  Ctrl+E configure key  ·  ↑↓ select  ·  Esc exit"
	}
	if width < 50 {
		// Narrow dialogs cannot fit the full contract: abbreviate rather
		// than truncate the close key away.
		hints = "↑↓ · Enter · Esc"
	}
	status := plane.Notice
	if status == "" {
		status = planeCatalogStatus(plane)
	}
	line := pal.Muted.Render(hints)
	if status != "" {
		gap := width - CellWidth(hints) - CellWidth(status) - 2
		if gap >= 1 {
			line += strings.Repeat(" ", gap) + pal.Muted.Render(status)
		}
	}
	return TruncateToWidth(line, width)
}

// planeCatalogStatus reports roster freshness in words, matching the picker.
func planeCatalogStatus(plane *ModelPlaneState) string {
	if plane.Loading {
		return "loading catalog…"
	}
	var parts []string
	switch plane.Origin {
	case "live":
		parts = append(parts, "live catalog")
	case "cached":
		parts = append(parts, "cached catalog")
	case "bundled":
		parts = append(parts, "bundled catalog")
	}
	if plane.Refreshing {
		parts = append(parts, "refreshing…")
	}
	if plane.ErrMsg != "" {
		parts = append(parts, "refresh failed ("+sanitizeInline(plane.ErrMsg)+")")
	}
	return strings.Join(parts, " · ")
}

// planeColumns splits the plane width into left and right panes. The split is
// width-aware: the provider pane grows on wide terminals and shrinks with the
// tier, but the model pane always keeps a usable minimum.
func planeColumns(state State, layout Layout) (int, int) {
	total := displayWidth(state, layout)
	if total < 20 {
		total = 20
	}
	var left int
	switch layout.Mode {
	case LayoutWide:
		left = total / 4
		if left < 24 {
			left = 24
		}
		if left > 34 {
			left = 34
		}
	case LayoutMedium:
		left = 20
	default:
		left = 14
	}
	if left > total-12 {
		left = total - 12
	}
	if left < 10 {
		left = 10
	}
	right := total - left - 1
	if right < 1 {
		right = 1
	}
	return left, right
}

// zipColumns lays two line slices side by side, padding the left column to its
// width so the separator lands in the same column on every row.
func zipColumns(left, right []string, leftWidth int, separator string) []string {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, padRight(l, leftWidth)+separator+r)
	}
	return out
}

// padRight pads a (possibly styled) line with spaces to width cells.
func padRight(line string, width int) string {
	gap := width - CellWidth(line)
	if gap <= 0 {
		return line
	}
	return line + strings.Repeat(" ", gap)
}

// renderPlaneProvidersPane draws the provider navigation pane: a heading, one
// row per provider with its model count and credential state, a separator, and
// the search box. The state glyph is truthful — configured, not configured, or
// unknown — and never claims a health check the runtime did not run.
func renderPlaneProvidersPane(plane *ModelPlaneState, pal *Palette, state State, width int) []string {
	heading := "PROVIDERS"
	if plane.Focus == PlaneFocusProviders {
		heading = "▶ PROVIDERS"
	}
	lines := []string{pal.Heading.Render(TruncateToWidth(heading, width))}

	rows := plane.VisibleRows
	if rows <= 0 {
		rows = len(plane.Providers)
	}
	window, start := window(plane.Providers, plane.ProviderOffset, rows)
	for i, row := range window {
		lines = append(lines, renderProviderRow(start+i, plane, row, pal, width))
	}
	if hidden := len(plane.Providers) - start - len(window); hidden > 0 {
		lines = append(lines, pal.Muted.Render(TruncateToWidth(fmt.Sprintf("  … %d more", hidden), width)))
	}

	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	searchHeading := "Search / Filter"
	if plane.Focus == PlaneFocusSearch {
		searchHeading = "▶ Search / Filter"
	}
	lines = append(lines, pal.Muted.Render(TruncateToWidth(searchHeading, width)))
	lines = append(lines, renderPlaneSearch(plane, pal, width))

	total := len(plane.Models)
	visible := len(plane.VisibleModels())
	lines = append(lines, pal.Muted.Render(TruncateToWidth(
		fmt.Sprintf("%d of %d models", visible, total), width)))
	return lines
}

// renderProviderRow draws one provider row: marker, state glyph, name, count.
func renderProviderRow(index int, plane *ModelPlaneState, row ProviderRow, pal *Palette, width int) string {
	name := sanitizeInline(row.Name)
	if name == "" {
		name = sanitizeInline(row.ID)
	}
	count := ""
	if row.ID == AllProviders {
		name = "All providers"
	} else {
		count = formatCount(row.Count)
	}
	glyph, glyphStyle := providerGlyph(row, pal)
	marker := "  "
	if index == plane.ProviderCursor {
		marker = pal.Selected.Render("> ")
	}
	label := glyphStyle.Render(glyph) + " " + name
	// Right-align the count with at least one cell of air before the pane
	// edge: a count flush against the separator reads as border damage.
	if count != "" {
		pad := width - CellWidth(marker) - CellWidth(label) - CellWidth(count) - 1
		if pad >= 1 {
			return marker + label + strings.Repeat(" ", pad) + pal.Muted.Render(count)
		}
	}
	return marker + TruncateToWidth(label, width-CellWidth(marker))
}

// providerGlyph is the truthful credential-state marker for a provider row.
func providerGlyph(row ProviderRow, pal *Palette) (string, lipgloss.Style) {
	if row.ID == AllProviders {
		return "≡", pal.Muted
	}
	if row.Configured {
		return "✓", pal.ToolDone
	}
	if row.KeyDetail == "none required" {
		return "✓", pal.ToolDone
	}
	if strings.HasPrefix(row.KeyDetail, "missing") {
		return "!", pal.ToolRun
	}
	return "?", pal.Muted
}

// renderPlaneSearch draws the query with a text cursor, matching the picker's
// convention so the two surfaces feel the same. The cursor block renders
// only while search owns the keyboard; otherwise the query shows without a
// cursor so focus is unambiguous.
func renderPlaneSearch(plane *ModelPlaneState, pal *Palette, width int) string {
	runes := []rune(sanitizeInline(plane.Filter))
	cur := plane.FilterCursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(runes) {
		cur = len(runes)
	}
	var query string
	if plane.Focus == PlaneFocusSearch {
		query = string(runes[:cur]) + pal.Cursor.Render(" ") + string(runes[cur:])
	} else {
		query = string(runes)
		if query == "" {
			query = pal.Muted.Render("—")
		}
	}
	return TruncateToWidth(pal.Muted.Render("/ ")+query, width)
}

// renderPlaneModelsPane draws the model pane: the compact model list followed
// by the highlighted model's structured detail and capability controls.
func renderPlaneModelsPane(plane *ModelPlaneState, pal *Palette, state State, layout Layout, width int) []string {
	return renderPlaneModelsPaneBudget(plane, pal, state, layout, width, planeDetailRows(layout.Mode))
}

// renderPlaneModelsPaneBudget draws the model pane with an explicit detail
// budget. The modal path derives the budget from its interior height so the
// effort radio — the visible thinking control — survives at normal sizes
// instead of being truncated by a tier constant.
func renderPlaneModelsPaneBudget(plane *ModelPlaneState, pal *Palette, state State, layout Layout, width, detailBudget int) []string {
	heading := "MODEL"
	if scope := plane.ProviderScope(); scope != "" {
		heading = "MODEL · " + sanitizeInline(scope)
	}
	if plane.Focus == PlaneFocusModels {
		heading = "▶ " + heading
	}
	lines := []string{pal.Heading.Render(TruncateToWidth(heading, width))}

	rows := plane.VisibleRows
	visible := plane.VisibleModels()
	if rows <= 0 {
		rows = len(visible)
	}
	window, start := window(visible, plane.ModelOffset, rows)
	if plane.Loading && len(window) == 0 {
		lines = append(lines, pal.Muted.Render(TruncateToWidth("  loading catalog…", width)))
	} else if len(window) == 0 {
		lines = append(lines, pal.Muted.Render(TruncateToWidth("  (no matches — esc clears the search)", width)))
	} else {
		for i, model := range window {
			lines = append(lines, renderPlaneModelRow(start+i, plane, model, pal, width))
		}
		if hidden := len(visible) - start - len(window); hidden > 0 {
			lines = append(lines, pal.Muted.Render(TruncateToWidth(fmt.Sprintf("  … %d more", hidden), width)))
		}
	}

	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	detail := planeModelDetail(plane, pal, width, detailBudget)
	lines = append(lines, detail...)
	return lines
}

// renderPlaneModelRow draws one model row with strict two-ended column
// docking: the name is left-aligned (truncated with an ellipsis when long)
// and the metadata group is rigidly right-docked in fixed-width,
// right-aligned columns (price 11, context 6, capabilities 10). Missing
// price, context, or capability metadata renders as a muted "-" placeholder
// inside its fixed-width column, so the group width never varies and price,
// context, and tags land on one straight vertical axis across rows instead
// of floating behind each name. Narrow panes shed capabilities first, then context,
// keeping the name and price visible longest. The in-use dot stays muted
// while the highlight stays accented, so the cursor — where the next key
// operates — always wins the eye over where the session runs.
func renderPlaneModelRow(index int, plane *ModelPlaneState, model ModelOption, pal *Palette, width int) string {
	name := sanitizeInline(model.Name)
	if name == "" {
		name = sanitizeInline(model.Title)
	}
	if name == "" {
		name = sanitizeInline(model.ID)
	}
	marker := "  "
	if index == plane.ModelCursor {
		marker = pal.Selected.Render("> ")
	}
	active := ""
	activeW := 0
	if model.Active {
		active = pal.Muted.Render("● ")
		activeW = 2
	}

	// Fixed right-docked columns: exact widths, right-aligned, so every
	// row's metadata shares one vertical axis.
	const rowPriceW = 11 // "$0.80/$1.60"
	const rowCtxW = 6    // "262k"
	const rowCapW = 10   // "vis think"
	const rowGap = "  "
	price := sanitizeInline(planePrice(model))
	if price == "" {
		// Missing pricing stays a fixed-width muted placeholder so the
		// price column never collapses and the shared axis holds.
		price = "-"
	}
	ctx := "-"
	if model.Context > 0 {
		if formatted := sanitizeInline(formatCount(model.Context)); formatted != "" {
			ctx = formatted
		}
	}
	capStr := sanitizeInline(planeCapShort(model))
	if capStr == "" {
		// No confirmed capabilities still occupies the 10-cell column
		// so context and price dock on the same axis every row.
		capStr = "-"
	}
	type col struct {
		text string
		w    int
	}
	cols := []col{
		{price, rowPriceW},
		{ctx, rowCtxW},
		{capStr, rowCapW},
	}
	groupWidth := func(cs []col) int {
		if len(cs) == 0 {
			return 0
		}
		total := 0
		for _, c := range cs {
			total += c.w
		}
		return total + (len(cs)-1)*CellWidth(rowGap)
	}
	// The name keeps a fair share of the row: metadata yields before the
	// name does, and price outranks context outranks capabilities.
	nameMin := width / 3
	if nameMin < 12 {
		nameMin = 12
	}
	if nameMin > width-4 {
		nameMin = width - 4
	}
	if nameMin < 1 {
		nameMin = 1
	}
	maxGroup := width - CellWidth(marker) - activeW - nameMin - CellWidth(rowGap)
	if maxGroup < 0 {
		maxGroup = 0
	}
	for len(cols) > 1 && groupWidth(cols) > maxGroup {
		cols = cols[:len(cols)-1]
	}
	group := ""
	for i, c := range cols {
		if i > 0 {
			group += rowGap
		}
		group += padLeftCells(c.text, c.w)
	}
	if CellWidth(group) > maxGroup && maxGroup > 0 {
		group = TruncateToWidth(group, maxGroup)
	}

	nameWidth := width - CellWidth(marker) - activeW
	if group != "" {
		nameWidth -= CellWidth(group) + CellWidth(rowGap)
	}
	if nameWidth < 6 {
		nameWidth = 6
	}
	// Pad the name cell so the right-docked group always starts from the
	// same column: short names get filler, long names truncate, and the
	// group never floats.
	nameCell := padRight(TruncateToWidth(name, nameWidth), nameWidth)
	line := marker + active + nameCell
	if group != "" {
		line += rowGap + pal.Muted.Render(group)
	}
	return TruncateToWidth(line, width)
}

// padLeftCells right-aligns text in a fixed-width column.
func padLeftCells(text string, width int) string {
	if gap := width - CellWidth(text); gap > 0 {
		return strings.Repeat(" ", gap) + text
	}
	return text
}

// planeCapShort renders the fixed-width capability tag for a model row:
// "vis" for image input, "think" for reasoning, joined as "vis think".
// Only confirmed capabilities appear; absence is silent rather than claiming
// "unsupported". The abbreviated form keeps the pair inside the 10-cell
// right-docked column on every row.
func planeCapShort(model ModelOption) string {
	var out []string
	for _, modality := range model.InputModalities {
		if modality == "image" {
			out = append(out, "vis")
			break
		}
	}
	if model.Reasoning && model.ReasoningKnown {
		out = append(out, "think")
	}
	return strings.Join(out, " ")
}

// planePrice renders the row's price when authoritative: "free" for verified
// zero pricing, "$in/$out" otherwise. A row without pricing metadata says
// nothing.
func planePrice(model ModelOption) string {
	if !model.PriceKnown {
		return ""
	}
	if model.Free {
		return "free"
	}
	if model.CostInput == 0 && model.CostOutput == 0 {
		return ""
	}
	return fmt.Sprintf("$%s/$%s", trimPrice(model.CostInput), trimPrice(model.CostOutput))
}

// trimPrice formats a per-1M price compactly, dropping a trailing ".0".
func trimPrice(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%.2f", value)
}

// planeModelDetail renders the structured detail and capability controls for
// the highlighted model. It shows only fields backed by authoritative
// metadata: unknown never renders as a value, and unsupported never renders as
// a missing one.
func planeModelDetail(plane *ModelPlaneState, pal *Palette, width, budget int) []string {
	model, ok := plane.ChosenModel()
	if !ok {
		return []string{pal.Muted.Render(TruncateToWidth("  select a model to see its detail", width))}
	}
	return planeModelDetailFor(plane, model, pal, width, budget)
}

// planeModelDetailFor renders the structured detail for an explicit model
// row. The variant prompt uses it to preview the highlighted sibling; the
// role line and pending effort still come from the plane, since choosing a
// variant never changes the targeted role.
func planeModelDetailFor(plane *ModelPlaneState, model ModelOption, pal *Palette, width, budget int) []string {
	if budget <= 0 {
		budget = 1
	}
	name := sanitizeInline(model.Name)
	if name == "" {
		name = sanitizeInline(model.Title)
	}
	if name == "" {
		name = sanitizeInline(model.ID)
	}
	// Narrow panes keep the single-column list so values never crush.
	if width < 50 {
		var lines []string
		add := func(label, value string) {
			if value == "" {
				return
			}
			lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(padLabel(label))+value, width))
		}
		lines = append(lines, pal.Bold.Render(TruncateToWidth("  "+name, width)))
		add("ID", sanitizeInline(model.ID))
		add("Context", planeContext(model))
		add("Output", planeOutput(model))
		add("Input", planeModalities(model))
		add("Thinking", planeThinking(model))
		add("Price", planePriceDetail(model))
		add("Role", planeRoleLine(plane, pal))
		if radio := planeEffortRadioFor(plane, model, pal, width); len(radio) > 0 {
			lines = append(lines, radio...)
		}
		if len(lines) > budget {
			lines = lines[:budget]
		}
		return lines
	}
	var lines []string
	lines = append(lines, pal.Bold.Render(TruncateToWidth("  "+name, width)))
	lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(padDetailLabel("ID"))+sanitizeInline(model.ID), width))
	// Borderless metadata grid: two invisible parallel axes split at half
	// the pane width. Left cells fill their half so the right column always
	// opens at the same offset; right cells carry consistent padding from
	// that offset. Whitespace alone separates the columns.
	half := width / 2
	if half < 10 {
		half = 10
	}
	if half > width-11 {
		half = width - 11
	}
	grid := [][2]string{
		{"Context", planeContext(model)},
		{"Output", planeOutput(model)},
		{"Thinking", planeThinkingCompact(model)},
	}
	right := []string{planeInputCell(model), planeVisionCell(model), planePriceCompact(model)}
	rightLabels := []string{"Input", "Vision", "Price"}
	for i := range grid {
		left := padRight("  "+pal.Muted.Render(padDetailLabel(grid[i][0]))+sanitizeInline(grid[i][1]), half)
		left = TruncateToWidth(left, half)
		rightCell := "  " + pal.Muted.Render(padDetailLabel(rightLabels[i])) + sanitizeInline(right[i])
		lines = append(lines, TruncateToWidth(left+strings.Repeat(" ", gridGapWidth)+rightCell, width))
	}
	lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(padDetailLabel("Role"))+planeRoleLineShort(plane, pal), width))

	if radio := planeEffortRadioFor(plane, model, pal, width); len(radio) > 0 {
		lines = append(lines, radio...)
	}
	if len(lines) > budget {
		lines = lines[:budget]
	}
	return lines
}

// gridGapWidth is the whitespace separation between the metadata grid
// columns: no line character, just empty cells, so the two key-value axes
// read as parallel without a boundary.
const gridGapWidth = 4

// padDetailLabel pads a grid label to a stable column with its · separator.
func padDetailLabel(label string) string {
	return padModalLabel(label, detailFieldLabelWidth)
}

// padModalLabel pads a modal field label to a stable column with its " · "
// separator. A label wider than the column keeps its own width so it is never
// truncated; the shared callers agree on the column so values line up.
func padModalLabel(label string, width int) string {
	if CellWidth(label) >= width {
		return label + " · "
	}
	return label + strings.Repeat(" ", width-CellWidth(label)) + "· "
}

// planeInputCell renders the text input modality, or unknown when unlisted.
func planeInputCell(model ModelOption) string {
	if len(model.InputModalities) == 0 {
		return "unknown"
	}
	for _, m := range model.InputModalities {
		if m == "text" {
			return "text"
		}
	}
	return model.InputModalities[0]
}

// planeVisionCell renders image support distinctly from an unknown source.
func planeVisionCell(model ModelOption) string {
	if len(model.InputModalities) == 0 {
		return "unknown"
	}
	for _, m := range model.InputModalities {
		if m == "image" {
			return "supported"
		}
	}
	return "unsupported"
}

// planeThinkingCompact renders the grid thinking state without the ladder
// list: the effort radio below already names the levels, so repeating them
// here only crushes the symmetric columns.
func planeThinkingCompact(model ModelOption) string {
	if len(model.ThinkingEfforts) > 0 {
		return "supported"
	}
	return planeThinking(model)
}

// planePriceCompact renders the grid price: free, unknown, or "$in / $out".
func planePriceCompact(model ModelOption) string {
	if !model.PriceKnown {
		return "unknown"
	}
	if model.Free {
		return "free"
	}
	if model.CostInput == 0 && model.CostOutput == 0 {
		return "unknown"
	}
	return fmt.Sprintf("$%s / $%s", trimPrice(model.CostInput), trimPrice(model.CostOutput))
}

// planeRoleLineShort names the role target for the grid's full-width row.
func planeRoleLineShort(plane *ModelPlaneState, pal *Palette) string {
	role := strings.ToUpper(sanitizeInline(plane.ActiveRole))
	if role == "" {
		role = "unset"
	}
	return pal.Bold.Render(role) + pal.Muted.Render(" (Enter assigns · Tab roles)")
}

// planeContext renders the context window, or "unknown".
func planeContext(model ModelOption) string {
	if model.Context <= 0 {
		return "unknown"
	}
	return formatCount(model.Context) + " tokens"
}

// planeOutput renders the maximum completion, or "unknown".
func planeOutput(model ModelOption) string {
	if model.MaxOutput <= 0 {
		return "unknown"
	}
	return formatCount(model.MaxOutput) + " tokens"
}

// planeModalities renders the input modalities, or "unknown" when the source
// listed none.
func planeModalities(model ModelOption) string {
	if len(model.InputModalities) == 0 {
		return "unknown"
	}
	return strings.Join(model.InputModalities, " · ")
}

// planeThinking renders the thinking capability, keeping unsupported distinct
// from unknown.
func planeThinking(model ModelOption) string {
	if len(model.ThinkingEfforts) > 0 {
		return "supported (" + strings.Join(model.ThinkingEfforts, ", ") + ")"
	}
	if !model.ReasoningKnown {
		return "unknown"
	}
	if model.Reasoning {
		return "supported"
	}
	return "unsupported"
}

// planePriceDetail renders the economics, or "unknown" when unbacked.
func planePriceDetail(model ModelOption) string {
	if !model.PriceKnown {
		return "unknown"
	}
	if model.Free {
		return "free"
	}
	out := fmt.Sprintf("$%s in / $%s out per 1M", trimPrice(model.CostInput), trimPrice(model.CostOutput))
	if model.CostCacheRead > 0 || model.CostCacheWrite > 0 {
		out += fmt.Sprintf(" · cache $%s/$%s", trimPrice(model.CostCacheRead), trimPrice(model.CostCacheWrite))
	}
	return out
}

// planeRoleLine names the role an Enter applies to and how to change it.
func planeRoleLine(plane *ModelPlaneState, pal *Palette) string {
	role := strings.ToUpper(sanitizeInline(plane.ActiveRole))
	if role == "" {
		role = "unset"
	}
	return pal.Bold.Render(role) + pal.Muted.Render("  (Enter assigns · Tab roles)")
}

// planeEffortRadio renders the thinking effort radio for a model that exposes
// a ladder, marking the pending choice. It renders nothing for a model without
// a ladder, so no control appears that the model does not expose. The heading
// carries the focus marker so the control is discoverable without a hidden key.
func planeEffortRadio(plane *ModelPlaneState, pal *Palette, width int) []string {
	model, ok := plane.ChosenModel()
	if !ok {
		return nil
	}
	return planeEffortRadioFor(plane, model, pal, width)
}

// planeEffortRadioFor renders the effort radio for an explicit model row,
// marking the plane's pending choice. The variant prompt previews a sibling
// through it without moving the highlight.
func planeEffortRadioFor(plane *ModelPlaneState, model ModelOption, pal *Palette, width int) []string {
	options := effortOptions(model)
	if len(options) == 0 {
		return nil
	}
	parts := make([]string, 0, len(options))
	for _, option := range options {
		if option == plane.Effort {
			parts = append(parts, pal.Selected.Render("● "+option))
		} else {
			parts = append(parts, pal.Muted.Render("○ "+option))
		}
	}
	label := "Effort"
	if plane.Focus == PlaneFocusEffort {
		label = "▶ Effort"
	}
	line := "  " + pal.Muted.Render(padLabel(label)) + strings.Join(parts, "  ")
	return []string{TruncateToWidth(line, width)}
}

// renderPlaneRolesPane draws the role list. Each row names the role and the
// model currently assigned to it, so the pane is a display of effective
// assignments: choosing a role stages it for replacement via model search,
// never assigns a hidden highlight.
func renderPlaneRolesPane(plane *ModelPlaneState, pal *Palette, width int) []string {
	heading := "ROLES"
	if plane.Focus == PlaneFocusRoles {
		heading = "▶ ROLES"
	}
	lines := []string{pal.Heading.Render(TruncateToWidth(heading, width))}
	for i, role := range plane.Roles {
		marker := "  "
		if i == plane.RoleCursor {
			marker = pal.Selected.Render("> ")
		}
		name := strings.ToUpper(sanitizeInline(role.Role))
		model := sanitizeInline(role.Name)
		if model == "" {
			model = sanitizeInline(role.Model)
		}
		if model == "" {
			model = "unset"
		}
		row := marker + name
		// The model keeps the pane honest: a role without a model says so
		// rather than hiding the gap. Narrow panes drop the model first.
		if tail := pal.Muted.Render(model); tail != "" {
			if room := width - CellWidth(row) - CellWidth(tail) - 3; room >= 1 {
				row += strings.Repeat(" ", room) + tail
			}
		}
		lines = append(lines, TruncateToWidth(row, width))
	}
	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	lines = append(lines, pal.Muted.Render(TruncateToWidth("Tab moves focus · Enter chooses model", width)))
	return lines
}

// renderPlaneAssignmentPane draws the highlighted role's effective
// configuration, naming the source so the precedence layering is visible.
// It never previews a hidden model highlight: Enter on a role stages it and
// returns to model search, so the pane ends with that contract instead of a
// misleading "assigns <hidden model>" line.
func renderPlaneAssignmentPane(plane *ModelPlaneState, pal *Palette, width int) []string {
	role, ok := plane.ChosenRole()
	if !ok {
		return []string{pal.Muted.Render(TruncateToWidth("  no roles configured", width))}
	}
	lines := []string{pal.Heading.Render(TruncateToWidth("ASSIGNMENT", width))}
	add := func(label, value string) {
		if value == "" {
			value = pal.Muted.Render("unset")
		}
		lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(padLabel(label))+value, width))
	}
	lines = append(lines, pal.Bold.Render(TruncateToWidth("  "+strings.ToUpper(sanitizeInline(role.Role)), width)))
	add("Model", sanitizeInline(role.Name))
	add("Provider", sanitizeInline(role.Provider))
	add("ID", sanitizeInline(role.Model))
	add("Thinking", planeRoleThinking(role))
	add("Source", sanitizeInline(role.Source))
	if role.SessionModel != "" && role.SessionModel != role.Model {
		add("Session", sanitizeInline(role.SessionModel)+" (snapshot override)")
	}
	if role.Err != "" {
		lines = append(lines, pal.ToolError.Render(TruncateToWidth("  "+sanitizeInline(role.Err), width)))
	}
	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	lines = append(lines, TruncateToWidth(
		"  "+pal.Muted.Render("Enter chooses model for ")+pal.Bold.Render(strings.ToUpper(sanitizeInline(role.Role))), width))
	return lines
}

// renderPlaneVariantPane draws the choose-a-variant prompt: one radio row
// per callable sibling of the base model, then the highlighted sibling's
// detail. Every option is a real roster row — its full id, price, and ladder
// — so confirming assigns exactly what was previewed.
func renderPlaneVariantPane(plane *ModelPlaneState, pal *Palette, width, contentH int) []string {
	variant := plane.Variant
	if variant == nil || len(variant.Options) == 0 {
		return []string{pal.Muted.Render(TruncateToWidth("  no variants", width))}
	}
	heading := "▶ VARIANT · " + sanitizeInline(variant.Base)
	lines := []string{pal.Heading.Render(TruncateToWidth(heading, width))}
	for i, option := range variant.Options {
		lines = append(lines, renderVariantOptionRow(i, variant.Cursor, option, pal, width))
	}
	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	budget := contentH - len(lines) - 1
	if budget < 1 {
		budget = 1
	}
	if chosen, ok := plane.ChosenVariant(); ok {
		lines = append(lines, planeModelDetailFor(plane, chosen, pal, width, budget)...)
	}
	return lines
}

// renderPlaneAuthPane draws the inline API-key prompt: the target provider,
// a masked input row with a text cursor, and the save/cancel contract.
// Only bullets ever render: the raw key never reaches the screen on any row.
func renderPlaneAuthPane(plane *ModelPlaneState, pal *Palette, width, contentH int) []string {
	auth := plane.Auth
	if auth == nil {
		return nil
	}
	_ = contentH
	title := "Configure API Key for " + sanitizeInline(auth.Display)
	if title == "Configure API Key for " {
		title = "Configure API Key for " + sanitizeInline(auth.Provider)
	}
	lines := []string{pal.Heading.Render(TruncateToWidth(title, width))}
	masked := strings.Repeat("•", len([]rune(auth.Input)))
	runes := []rune(masked)
	cur := auth.Cursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(runes) {
		cur = len(runes)
	}
	query := string(runes[:cur]) + pal.Cursor.Render(" ") + string(runes[cur:])
	lines = append(lines, TruncateToWidth(pal.Muted.Render("> ")+query, width))
	lines = append(lines, pal.Muted.Render(TruncateToWidth("[Enter save  ·  Esc cancel]", width)))
	return lines
}

// renderVariantOptionRow draws one variant choice: a radio marker, the
// variant suffix (or "default" for the bare base), and the row's own price
// and context when authoritative.
func renderVariantOptionRow(index, cursor int, option ModelOption, pal *Palette, width int) string {
	_, suffix := SplitModelVariant(option.ID)
	label := suffix
	if label == "" {
		label = "(default)"
	}
	marker := "  "
	choice := pal.Muted.Render("○ " + label)
	if index == cursor {
		marker = pal.Selected.Render("> ")
		choice = pal.Selected.Render("● " + label)
	}
	var tail []string
	if price := planePrice(option); price != "" {
		tail = append(tail, price)
	}
	if option.Context > 0 {
		tail = append(tail, formatCount(option.Context))
	}
	line := marker + choice
	if len(tail) > 0 {
		meta := strings.Join(tail, "  ")
		if room := width - CellWidth(line) - CellWidth(meta) - 3; room >= 1 {
			line += strings.Repeat(" ", room) + pal.Muted.Render(meta)
		}
	}
	return TruncateToWidth(line, width)
}

// planeRoleThinking renders a role's thinking state, keeping unsupported and
// unknown distinct.
func planeRoleThinking(role RoleAssignment) string {
	if role.Effort != "" {
		return role.Effort
	}
	if len(role.Efforts) > 0 {
		return "supported (" + strings.Join(role.Efforts, ", ") + ")"
	}
	if !role.ReasoningKnown {
		return "unknown"
	}
	if role.Reasoning {
		return "supported"
	}
	return "unsupported"
}

// padLabel pads a detail label to a stable column so values align.
func padLabel(label string) string {
	const width = 10
	if len(label) >= width {
		return label + " "
	}
	return label + strings.Repeat(" ", width-len(label))
}

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
