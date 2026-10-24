package tui

// Permission selection: the modal surface behind /permissions and /mode.
//
// The selector is presentation only. It never changes execution authority
// itself: confirming routes the chosen name through the CLI-owned
// WorkspaceHooks.Switch seam, exactly like /ask, /fuzz, and /build. That is
// what keeps the authority check below the UI and above execution: the modal
// may display the three authorities and ask for one, but it cannot grant a
// capability (ARCHITECTURE_CONTRACT_V1 §7, I5).

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/workspace"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// AuthorityMode names one execution authority offered by the selector. The
// values are the uppercase display labels the chrome already uses, so a mode
// derived from Status.Workspace compares equal without a second mapping.
type AuthorityMode string

// The three authorities. There are exactly three: a fourth name is a bug, not
// a mode.
const (
	// ModeAsk is the read-only reasoning boundary.
	ModeAsk AuthorityMode = "ASK"
	// ModeBuild is the sole mutation authority.
	ModeBuild AuthorityMode = "BUILD"
	// ModeFuzz is the exploratory boundary: execute without mutating.
	ModeFuzz AuthorityMode = "FUZZ"
)

// AuthorityOption is one row of the authority registry.
type AuthorityOption struct {
	// Mode is the authority this row selects.
	Mode AuthorityMode
	// Label is the one-line display name, e.g. "ASK (Read-Only)".
	Label string
	// Description is the multi-line explanation rendered under the label.
	Description string
}

// AuthorityOptions is the fixed registry, in the order the modal numbers.
// It is the single source the renderer and the key handler both read, so a
// shown row can never select an authority dispatch rejects.
var AuthorityOptions = []AuthorityOption{
	{
		Mode:  ModeAsk,
		Label: "ASK (Read-Only)",
		Description: "ohgo can inspect, search, and reason over the codebase. " +
			"No files will be modified and no mutating shell commands will be executed.",
	},
	{
		Mode:  ModeBuild,
		Label: "BUILD (Workspace Authority)",
		Description: "ohgo is authorized to modify files and run commands within the current workspace. " +
			"Network access or out-of-bounds file edits require explicit human approval.",
	},
	{
		Mode:  ModeFuzz,
		Label: "FUZZ (Autonomous Assistance)",
		Description: "ohgo executes autonomous fuzzing, fast inspection, and deep test cycles within workspace guardrails. " +
			"Use with active oversight.",
	},
}

// Permissions geometry.
const (
	// permissionsDefaultWidth is the fallback box width when no terminal size
	// is known (a test, or a headless render).
	permissionsDefaultWidth = 72
	// permissionsMinInterior is the narrowest usable content area. Below it
	// the box stops being readable and grows to this floor instead.
	permissionsMinInterior = 20
	// permissionsIndent is the four-space axis every description wraps onto.
	permissionsIndent = "    "
	// permissionsFooterHint names the keys that actually work in the modal.
	permissionsFooterHint = "Press enter to confirm · esc to cancel"
)

// PermissionsState is the open authority selector.
type PermissionsState struct {
	// Cursor is the highlighted option index into AuthorityOptions.
	Cursor int
	// Title and Reason customize the selector when it is the ambiguity boundary.
	// Empty Title means the normal /permissions heading.
	Title  string
	Reason string
	// Request is held until the user chooses a workspace; an empty value means
	// this is an ordinary authority-management action.
	Request string
}

// Move shifts the highlight, wrapping at both ends: the three authorities form
// a cycle, so overshooting the last lands on the first rather than stranding
// the cursor.
func (p *PermissionsState) Move(delta int) {
	if p == nil {
		return
	}
	n := len(AuthorityOptions)
	if n == 0 {
		p.Cursor = 0
		return
	}
	p.Cursor = (p.Cursor + delta) % n
	if p.Cursor < 0 {
		p.Cursor += n
	}
}

// AuthorityModeFromLabel resolves an uppercase chrome label ("BUILD") to its
// mode. An unknown or empty label reports false: the selector then shows no
// (current) badge rather than guessing.
func AuthorityModeFromLabel(label string) (AuthorityMode, bool) {
	switch AuthorityMode(strings.ToUpper(strings.TrimSpace(label))) {
	case ModeAsk:
		return ModeAsk, true
	case ModeBuild:
		return ModeBuild, true
	case ModeFuzz:
		return ModeFuzz, true
	}
	return "", false
}

// Valid reports whether m is one of the three authorities.
func (m AuthorityMode) Valid() bool {
	switch m {
	case ModeAsk, ModeBuild, ModeFuzz:
		return true
	}
	return false
}

// WorkspaceName is the lowercase wire name the workspace owner accepts
// ("ask", "build", "fuzz"). It mirrors workspace.Parse's vocabulary so the
// switch seam resolves the same authority the modal displayed.
func (m AuthorityMode) WorkspaceName() string {
	return strings.ToLower(string(m))
}

// authorityIndex returns the registry position of a mode, or 0 when unknown.
func authorityIndex(mode AuthorityMode) int {
	for i, opt := range AuthorityOptions {
		if opt.Mode == mode {
			return i
		}
	}
	return 0
}

// authorityIndexForLabel parks the cursor on the authority a chrome label
// names, defaulting to the top row when the label is unknown.
func authorityIndexForLabel(label string) int {
	if mode, ok := AuthorityModeFromLabel(label); ok {
		return authorityIndex(mode)
	}
	return 0
}

// permissionSelection resolves the option at an index, reporting false outside
// the registry so a stale cursor can never select a row that does not exist.
func permissionSelection(index int) (AuthorityOption, bool) {
	if len(AuthorityOptions) == 0 {
		return AuthorityOption{}, false
	}
	if index < 0 || index >= len(AuthorityOptions) {
		return AuthorityOption{}, false
	}
	return AuthorityOptions[index], true
}

// authorityAccent is the mode's semantic identity colour: ASK teal, BUILD
// steel blue, FUZZ magenta. An invalid mode degrades to the dim slate rather
// than borrowing an authority's colour.
func authorityAccent(mode AuthorityMode) string {
	switch mode {
	case ModeAsk:
		return ColorPrimary
	case ModeBuild:
		return ColorAccent
	case ModeFuzz:
		return ColorFuzz
	}
	return ColorDim
}

// RenderPermissionsModal renders the bordered authority selector.
//
// width is the total rendered width of the box, border included. The box grows
// to permissionsMinInterior when a caller passes a width too small to read, so
// a narrow terminal still gets a legible dialog instead of a degenerate one.
// Every emitted line is padded to the interior width, which keeps the rounded
// border aligned and guarantees no row exceeds width.
func RenderPermissionsModal(selectedIdx int, currentMode AuthorityMode, width int) string {
	return renderPermissionsModal(selectedIdx, currentMode, width, "", "", "")
}

func renderPermissionsModal(selectedIdx int, currentMode AuthorityMode, width int, title, reason, request string) string {
	if width <= 0 {
		width = permissionsDefaultWidth
	}
	interior := width - 2 // the two border columns
	if interior < permissionsMinInterior {
		interior = permissionsMinInterior
		width = interior + 2
	}
	// Descriptions wrap at modalWidth-6, then indent four cells: the two
	// together fit exactly in the interior (width-2). Clamp to the interior
	// so an unusually narrow box still fits.
	descWidth := width - 6
	if max := interior - len(permissionsIndent); descWidth > max {
		descWidth = max
	}
	if descWidth < 8 {
		descWidth = 8
	}

	selected := selectedIdx
	if selected < 0 || selected >= len(AuthorityOptions) {
		selected = 0
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(ColorText))
	descStyle := fg(ColorDim)
	footerStyle := fg(ColorDim)

	var rows []string
	if strings.TrimSpace(title) == "" {
		title = "Update Model Execution Authority"
	}
	rows = append(rows, permissionsCenter(titleStyle, title, interior))
	if strings.TrimSpace(reason) != "" {
		rows = append(rows, "")
		for _, line := range WrapToWidth(reason, descWidth) {
			rows = append(rows, descStyle.Render(permissionsIndent+line))
		}
	}
	if strings.TrimSpace(request) != "" {
		rows = append(rows, "")
		for _, line := range WrapToWidth("Request: "+request, descWidth) {
			rows = append(rows, descStyle.Render(permissionsIndent+line))
		}
	}
	rows = append(rows, "")
	for i, opt := range AuthorityOptions {
		rows = append(rows, permissionsOptionRow(i, i == selected, opt, currentMode, interior))
		for _, line := range WrapToWidth(opt.Description, descWidth) {
			rows = append(rows, descStyle.Render(permissionsIndent+line))
		}
		if i != len(AuthorityOptions)-1 {
			rows = append(rows, "")
		}
	}
	rows = append(rows, "")
	rows = append(rows, permissionsCenter(footerStyle, permissionsFooterHint, interior))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(ColorDim)).
		Render(strings.Join(rows, "\n"))
}

// permissionsOptionRow renders one option: the selection arrow, its number, the
// mode label in its authority accent, and a (current) badge when it is the
// authority in force. The selection is carried by the arrow glyph and the
// label emphasis together, so a monochrome terminal still shows where Enter
// would land.
func permissionsOptionRow(index int, selected bool, opt AuthorityOption, current AuthorityMode, interior int) string {
	indicator := "  "
	if selected {
		indicator = "› "
	}
	number := fmt.Sprintf("%d. ", index+1)
	tag := ""
	if opt.Mode == current {
		tag = " (current)"
	}
	// The label yields to the fixed gutter and badge: a long label truncates
	// rather than pushing the row past the box.
	fixed := CellWidth(indicator) + CellWidth(number) + CellWidth(tag)
	budget := interior - fixed
	if budget < 1 {
		budget = 1
	}
	label := TruncateToWidth(opt.Label, budget)

	indicatorStyle := lipgloss.NewStyle()
	if selected {
		indicatorStyle = fg(ColorPrimary)
	}
	labelStyle := fg(authorityAccent(opt.Mode))
	if selected {
		labelStyle = labelStyle.Bold(true)
	}
	row := indicatorStyle.Render(indicator) +
		fg(ColorMuted).Render(number) +
		labelStyle.Render(label)
	if tag != "" {
		row += fg(ColorMuted).Render(tag)
	}
	if gap := interior - CellWidth(indicator) - CellWidth(number) - CellWidth(label) - CellWidth(tag); gap > 0 {
		row += strings.Repeat(" ", gap)
	}
	return row
}

// permissionsCenter places plain text on the interior axis, centering its
// styled form. When the text is wider than the axis it truncates instead,
// so a centered row can never overflow the box.
func permissionsCenter(style lipgloss.Style, text string, width int) string {
	plainWidth := CellWidth(text)
	if plainWidth >= width {
		return style.Render(TruncateToWidth(text, width))
	}
	left := (width - plainWidth) / 2
	right := width - plainWidth - left
	return strings.Repeat(" ", left) + style.Render(text) + strings.Repeat(" ", right)
}

// permissionsCardWidth bounds the centered dialog: wide enough for the longest
// description to wrap over a couple of rows, narrow enough to read as a popup
// with margins on roomy terminals.
func permissionsCardWidth(termWidth int) int {
	const maxCard = 76
	const minCard = 44
	if termWidth <= 0 {
		return maxCard
	}
	w := termWidth - 8
	if w > maxCard {
		w = maxCard
	}
	if w < minCard {
		w = termWidth - 4
		if w < permissionsMinInterior+2 {
			w = permissionsMinInterior + 2
		}
		if w > termWidth {
			w = termWidth
		}
	}
	return w
}

// renderPermissionsArea draws the authority selector as a centered modal. The
// transcript hides behind it and returns untouched on dismiss, and the overlay
// fills its budget exactly so the chrome below stays pinned.
func renderPermissionsArea(sb *strings.Builder, state State, _ *Palette, layout Layout) {
	modal := state.Permissions
	if modal == nil {
		return
	}
	current, _ := AuthorityModeFromLabel(state.Status.Workspace)
	card := renderPermissionsModal(modal.Cursor, current, permissionsCardWidth(state.Width), modal.Title, modal.Reason, modal.Request)
	if state.Width <= 0 || state.Height <= 0 {
		sb.WriteString(card)
		if !strings.HasSuffix(card, "\n") {
			sb.WriteString("\n")
		}
		return
	}
	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	width := state.Width
	if width <= 0 {
		width = 80
	}
	placed := lipgloss.Place(width, avail, lipgloss.Center, lipgloss.Center, card)
	lines := strings.Split(placed, "\n")
	if len(lines) > avail {
		lines = lines[:avail]
	}
	for len(lines) < avail {
		lines = append(lines, strings.Repeat(" ", width))
	}
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
}

// openPermissions opens the authority selector. It refuses while a turn is
// running or the support surface is open: authority moves at a turn boundary,
// and one turn must never span two authorities (§7).
func (p *Program) openPermissions() {
	if p.state.SupportPanel != nil {
		p.state.Apply(Notice{Text: "close support first (Esc) before choosing authority"})
		return
	}
	if p.state.Status.Busy {
		p.state.Apply(Notice{Text: "wait for the turn to finish before choosing authority"})
		return
	}
	p.state.Apply(PermissionsOpened{})
}

// openAuthorityChoice makes the centered authority selector the human
// decision boundary for a request the classifier cannot safely place.
func (p *Program) openAuthorityChoice(request, reason string) {
	if p.state.Status.Busy || p.state.SupportPanel != nil {
		p.restoreAuthorityRequest(request)
		p.state.Apply(Notice{Text: "wait until the current surface closes before choosing a workspace"})
		return
	}
	p.state.Apply(PermissionsOpened{
		Title:   "Choose Workspace For This Request",
		Reason:  reason,
		Request: request,
	})
}

// restoreAuthorityRequest leaves a cancelled or failed choice available for
// the user to edit and resubmit; it is never silently executed.
func (p *Program) restoreAuthorityRequest(request string) {
	if strings.TrimSpace(request) == "" {
		return
	}
	p.state.Apply(InputChanged{Text: request})
}

// handlePermissionsKey routes keys to the open selector. It owns the keyboard
// while open: navigation wraps, Enter confirms, and Esc or Ctrl+C cancels
// without touching authority.
func (p *Program) handlePermissionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		p.state.Apply(PermissionsMoved{Delta: -1})
	case "down", "j":
		p.state.Apply(PermissionsMoved{Delta: 1})
	case "enter", " ":
		return p.confirmPermissions()
	case "esc", "ctrl+c":
		if p.state.Permissions != nil {
			p.restoreAuthorityRequest(p.state.Permissions.Request)
		}
		p.state.Apply(PermissionsClosed{})
	}
	return p, nil
}

// confirmPermissions applies the highlighted authority through the workspace
// owner and closes the modal.
//
// The TUI never grants capability itself: it hands the lowercase name to
// Switch, which owns validation, the runtime, and persistence, and publishes
// the chrome update the composer prefix reads. A stale cursor selects nothing
// and simply closes.
func (p *Program) confirmPermissions() (tea.Model, tea.Cmd) {
	modal := p.state.Permissions
	if modal == nil {
		return p, nil
	}
	option, ok := permissionSelection(modal.Cursor)
	p.state.Apply(PermissionsClosed{})
	if !ok {
		p.restoreAuthorityRequest(modal.Request)
		return p, nil
	}
	name := option.Mode.WorkspaceName()
	current := ""
	if p.workspace.Current != nil {
		current = p.workspace.Current()
	}
	if !strings.EqualFold(current, name) && !p.switchWorkspace(name) {
		p.restoreAuthorityRequest(modal.Request)
		return p, nil
	}
	if modal.Request != "" {
		ws, err := workspace.Parse(name)
		if err != nil {
			p.restoreAuthorityRequest(modal.Request)
			p.state.Apply(Notice{Text: "workspace: " + err.Error()})
			return p, nil
		}
		return p, p.startTurnWithChoice(modal.Request, ws)
	}
	return p, nil
}
