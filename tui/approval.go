package tui

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/charmbracelet/lipgloss"
)

// The tool approval dialog.
//
// It is the one surface where a border earns its place: a human decision
// boundary (UI_DESIGN_SYSTEM §6). The frame accent reads the blast radius
// before the words do — amber for a read or inspect call, rose for one that
// mutates — the body names the exact call, and the resting cursor stays on the
// conservative answer. The conservative answer is always the last choice, and
// PendingApproval.Move clamps at the ends, so no navigation key can reach an
// affirmative answer without a deliberate move onto one.
//
// The whole block is bounded to the viewport. Optional rows (padding, the
// reason, the key footer, even the question) yield before the command or the
// answers do, so a short terminal still shows what is being approved and the
// choices it is being asked for. Render and accounting share approvalBoxRows,
// so the reserved rows and the painted rows can never disagree.

// Approval dialog geometry and copy.
const (
	// approvalTitle is the legend embedded in the top border.
	approvalTitle = "Tool Execution Approval"
	// approvalQuestion is the ask that introduces the answers.
	approvalQuestion = "Do you want to proceed?"
	// approvalFooterHint names the keys the dialog actually captures, so the
	// user is never taught a binding the open dialog has taken over.
	approvalFooterHint = "↑↓ move · enter confirm · y approve · n/esc deny"
	// proposalTitle is the legend for a request-derived proposal. It stays in
	// the same permission boundary; only the decision data differs.
	proposalTitle = "Proposal Permission"
	// commitProposalTitle is the legend for the /commit proposal.
	commitProposalTitle = "Commit Proposal"
	// commitProposalQuestion is the ask for the one Git mutation.
	commitProposalQuestion = "Create this commit?"
	// commitProposalFooterHint names the commit-only actions in addition to
	// the shared navigation keys.
	commitProposalFooterHint = "↑↓ move · enter confirm · e edit · r retry · esc cancel"
	// proposalFooterHint names the keys a proposal captures. It deliberately
	// omits y/n: those decide a tool grant, not a proposal.
	proposalFooterHint = "↑↓ move · enter confirm · esc cancel"
	// approvalIndent insets every body row from the frame.
	approvalIndent = "  "
	// approvalBorderRows is the fixed top and bottom frame.
	approvalBorderRows = 2
)

// approvalRowKind classifies one interior row so the same plan can be counted
// plain and rendered styled.
type approvalRowKind int

const (
	approvalRowPrompt approvalRowKind = iota
	approvalRowCommand
	approvalRowReason
	approvalRowPreview
	approvalRowQuestion
	approvalRowSpacer
	approvalRowChoice
	approvalRowFooter
	approvalRowCommitSubject
	approvalRowCommitBody
	approvalRowCommitLabel
	approvalRowCommitFile
	approvalRowCommitNote
)

// Drop priorities: a row with a lower value is shed first when the box must
// shrink. Choices are protected, and the command outranks the question, so the
// text describing what is being approved survives longest.
const (
	dropPadding = iota + 1
	dropSpacerTop
	dropSpacerBottom
	dropShellPrompt
	dropReason
	dropPreview
	dropFooter
	dropQuestion
	dropCommand
	protectedRow = 1 << 7
)

// approvalRow is one interior box row: its kind, its (plain, sanitized) text,
// and the priority at which it may be shed.
type approvalRow struct {
	kind  approvalRowKind
	index int
	text  string
	drop  int
}

// approvalBorderColor is the frame accent, keyed to the call's blast radius:
// warm amber for a read or inspect call, burnt rose for one that mutates.
func approvalBorderColor(pending *PendingApproval) string {
	if pending.Mutating {
		return ColorRose
	}
	return ColorMutedAmber
}

// approvalActiveBG is the subtle surface tint behind the highlighted answer.
// It reuses the code surface per theme rather than inventing a colour, so the
// band reads as a focus tint and not a second signal.
func approvalActiveBG(theme string) lipgloss.Color {
	if NormalizeTheme(theme) == ThemeLight {
		return lipgloss.Color(lightCodeBg)
	}
	return lipgloss.Color(darkCodeBg)
}

// approvalInterior is the cells between the two frame columns.
func approvalInterior(layout Layout) int {
	interior := dialogWidth(layout) - 2
	if interior < 1 {
		interior = 1
	}
	return interior
}

// approvalTarget is the line naming the exact call. A shell escape shows its
// command alone (its prompt already says what it is); a tool call shows the
// tool name followed by its summary, without repeating a name the summary
// already carries.
func approvalTarget(pending *PendingApproval) string {
	if pending.Commit != nil {
		return "Commit proposal"
	}
	summary := strings.TrimSpace(sanitizeInline(pending.Summary))
	name := strings.TrimSpace(sanitizeInline(pending.ToolName))
	if pending.Prompt != "" || name == "" {
		if summary == "" {
			return name
		}
		return summary
	}
	if summary == "" {
		return name
	}
	if strings.HasPrefix(summary, name) {
		return summary
	}
	return name + " " + summary
}

// approvalPlan builds the untrimmed interior rows for a dialog.
func approvalPlan(pending *PendingApproval, layout Layout) []approvalRow {
	body := approvalInterior(layout) - len(approvalIndent)
	if body < 1 {
		body = 1
	}
	rows := make([]approvalRow, 0, len(pending.Choices)+8)
	rows = append(rows, approvalRow{kind: approvalRowSpacer, drop: dropPadding})
	if prompt := strings.TrimSpace(sanitizeInline(pending.Prompt)); prompt != "" {
		for _, line := range WrapToWidth(prompt, body) {
			rows = append(rows, approvalRow{kind: approvalRowPrompt, text: line, drop: dropShellPrompt})
		}
	}
	for _, line := range WrapToWidth(approvalTarget(pending), body) {
		rows = append(rows, approvalRow{kind: approvalRowCommand, text: line, drop: dropCommand})
	}
	if reason := strings.TrimSpace(sanitizeInline(pending.Reason)); reason != "" {
		for _, line := range WrapToWidth(reason, body) {
			rows = append(rows, approvalRow{kind: approvalRowReason, text: line, drop: dropReason})
		}
	}
	// Dedicated mutation preview block, separate from Reason. Bounded so a
	// large file cannot make the dialog unbounded.
	preview := pending.MutationPreview
	op := preview.Operation
	if op == "" {
		op = agent.MutationOp("MUTATION")
	}
	if preview.Diff != "" || preview.Unsupported || preview.Path != "" {
		header := fmt.Sprintf("Preview · %s", op)
		rows = append(rows, approvalRow{kind: approvalRowPreview, text: header, drop: protectedRow})
		text := preview.Diff
		if text == "" {
			if preview.Unsupported {
				text = "(no textual preview available)"
			} else {
				text = "(no changes)"
			}
		}
		text = sanitizeBlock(text)
		lines := strings.Split(text, "\n")
		const maxPreviewLines = 16
		if len(lines) > maxPreviewLines {
			lines = append(lines[:maxPreviewLines], "... (truncated)")
		}
		// Byte bound as a backstop.
		var bounded []string
		bytes := 0
		const maxPreviewBytes = 4000
		for _, line := range lines {
			if bytes+len(line) > maxPreviewBytes && len(bounded) > 0 {
				bounded = append(bounded, "... (truncated)")
				break
			}
			bounded = append(bounded, line)
			bytes += len(line) + 1
		}
		for _, line := range bounded {
			for _, wrapped := range WrapToWidth(line, body) {
				rows = append(rows, approvalRow{kind: approvalRowPreview, text: wrapped, drop: dropPreview})
			}
		}
	}
	for _, line := range WrapToWidth(pending.question(), body) {
		rows = append(rows, approvalRow{kind: approvalRowQuestion, text: line, drop: dropQuestion})
	}
	if pending.Commit != nil {
		commitRows := commitApprovalRows(pending.Commit, body)
		rows = append(rows, commitRows...)
	}
	rows = append(rows, approvalRow{kind: approvalRowSpacer, drop: dropSpacerTop})
	for i, choice := range pending.Choices {
		rows = append(rows, approvalRow{kind: approvalRowChoice, index: i, text: sanitizeInline(choice), drop: protectedRow})
	}
	rows = append(rows, approvalRow{kind: approvalRowSpacer, drop: dropSpacerBottom})
	rows = append(rows, approvalRow{kind: approvalRowFooter, text: pending.footerHint(), drop: dropFooter})
	return rows
}

// commitApprovalRows renders the exact message and scope inside the existing
// permission dialog. The subject and files are protected: the reviewer must be
// able to see what text and what paths the approval would commit, even on a
// short terminal. The body and the explanatory note shed first.
func commitApprovalRows(view *CommitProposalView, body int) []approvalRow {
	var rows []approvalRow
	rows = append(rows, approvalRow{kind: approvalRowSpacer, drop: dropSpacerTop})
	for _, line := range WrapToWidth(sanitizeInline(view.Subject), body) {
		rows = append(rows, approvalRow{kind: approvalRowCommitSubject, text: line, drop: protectedRow})
	}
	for _, line := range view.Body {
		for _, wrapped := range WrapToWidth(sanitizeInline(line), body) {
			rows = append(rows, approvalRow{kind: approvalRowCommitBody, text: wrapped, drop: dropReason})
		}
	}
	if len(view.Files) > 0 {
		rows = append(rows, approvalRow{kind: approvalRowCommitLabel, text: "Files:", drop: protectedRow})
		for _, file := range view.Files {
			for _, wrapped := range WrapToWidth(sanitizeInline(file), body) {
				rows = append(rows, approvalRow{kind: approvalRowCommitFile, text: wrapped, drop: protectedRow})
			}
		}
	}
	if strings.TrimSpace(view.Note) != "" {
		for _, line := range WrapToWidth(sanitizeInline(view.Note), body) {
			rows = append(rows, approvalRow{kind: approvalRowCommitNote, text: line, drop: dropPreview})
		}
	}
	return rows
}

// title is the frame legend: the tool boundary or the proposal boundary.
func (p *PendingApproval) title() string {
	if p.Commit != nil {
		return commitProposalTitle
	}
	if p.IsProposal() {
		return proposalTitle
	}
	return approvalTitle
}

// question is the ask that introduces the answers. A proposal supplies its
// own request-derived question; a tool approval keeps the fixed ask.
func (p *PendingApproval) question() string {
	if p.Commit != nil {
		return commitProposalQuestion
	}
	if p.IsProposal() && strings.TrimSpace(p.Proposal.Question) != "" {
		return p.Proposal.Question
	}
	return approvalQuestion
}

// footerHint names the keys this prompt actually captures.
func (p *PendingApproval) footerHint() string {
	if p.Commit != nil {
		return commitProposalFooterHint
	}
	if p.IsProposal() {
		return proposalFooterHint
	}
	return approvalFooterHint
}

// approvalBudget is the rows the whole approval block may occupy. The
// full-screen surface reserves the transcript region; the live surface passes
// its own cap through Height. A negative result means the size is unknown and
// the full box renders.
func approvalBudget(layout Layout) int {
	if layout.ConversationHeight > 0 {
		return layout.ConversationHeight
	}
	if layout.Height > 0 {
		return layout.Height
	}
	return -1
}

// approvalBoxRows is the interior rows the dialog actually paints: the full
// plan trimmed to the viewport, so optional rows yield before the command or
// the answers. Render and accounting share it.
func approvalBoxRows(pending *PendingApproval, layout Layout) []approvalRow {
	rows := approvalPlan(pending, layout)
	if budget := approvalBudget(layout); budget >= 0 {
		budget -= approvalBlankRows(pending, layout)
		if budget < 0 {
			budget = 0
		}
		rows = fitApprovalRows(rows, budget)
	}
	return rows
}

// fitApprovalRows sheds droppable rows, lowest priority first, until the plan
// fits budget. Choices are protected: the answers to the question always stay
// on screen, which is the safety-critical part of the dialog.
func fitApprovalRows(rows []approvalRow, budget int) []approvalRow {
	if len(rows) <= budget {
		return rows
	}
	out := append([]approvalRow(nil), rows...)
	for len(out) > budget {
		idx := -1
		for i := range out {
			if out[i].drop >= protectedRow {
				continue
			}
			if idx == -1 || out[i].drop < out[idx].drop {
				idx = i
			}
		}
		if idx == -1 {
			break
		}
		out = append(out[:idx], out[idx+1:]...)
	}
	return out
}

// approvalLines counts the rendered approval block, bounded to the viewport.
// Every row the dialog paints is claimed here, plus the two frame rows and the
// trailing separator blank, so the reserved and painted rows always agree.
func approvalLines(pending *PendingApproval, layout Layout) int {
	if pending == nil {
		return 0
	}
	return len(approvalBoxRows(pending, layout)) + approvalBorderRows + approvalBlankRows(pending, layout)
}

// approvalBlankRows is 1 when the separator blank after the dialog fits and
// 0 when yielding it keeps the box on screen. Render and accounting share it
// so the reserved rows and the rendered rows always agree.
func approvalBlankRows(pending *PendingApproval, layout Layout) int {
	if approvalBlank(pending, layout) {
		return 1
	}
	return 0
}

// approvalBlank reports whether the separator blank after the dialog fits.
// On viewports too short for the chrome plus the dialog minimum it yields,
// so the box stays on screen instead of the spacing.
func approvalBlank(pending *PendingApproval, layout Layout) bool {
	if layout.Height <= 0 {
		return true
	}
	hints := 0
	if layout.ShowHints {
		hints = 1
	}
	// header + the box minimum (command, question, choices, hint) + blank +
	// composer + layout hints + one transcript row.
	return layout.Height >= len(pending.Choices)+layout.ComposerLines+hints+9
}

// renderApproval draws the approval dialog: a bounded rounded box with the
// legend on the top border, the exact call, and the numbered answers. theme
// selects the active-row tint; the frame accent comes from the call's blast
// radius, so a destructive boundary never looks like a read.
func renderApproval(sb *strings.Builder, pending *PendingApproval, theme string, layout Layout) {
	if pending == nil {
		return
	}
	interior := approvalInterior(layout)
	border := approvalBorderColor(pending)
	sb.WriteString(approvalTitleBorder(pending.title(), interior, border))
	sb.WriteString("\n")
	for _, row := range approvalBoxRows(pending, layout) {
		content := renderApprovalRow(row, pending, border, theme, interior)
		sb.WriteString(approvalFrame(border, content))
		sb.WriteString("\n")
	}
	sb.WriteString(approvalBottomBorder(interior, border))
	sb.WriteString("\n")
	if approvalBlank(pending, layout) {
		sb.WriteString("\n")
	}
}

// approvalFrame wraps one interior row in the rounded side borders. The runes
// come from lipgloss.RoundedBorder(); the legend on the top border is the only
// drawn difference from that border's plain form.
func approvalFrame(border string, content string) string {
	b := lipgloss.RoundedBorder()
	edge := fg(border)
	return edge.Render(b.Left) + content + edge.Render(b.Right)
}

// approvalTitleBorder draws the top frame with the legend centered on it, in
// the frame accent so the title and the boundary read as one object.
func approvalTitleBorder(title string, interior int, border string) string {
	b := lipgloss.RoundedBorder()
	edge := fg(border)
	titleStyle := fg(border).Bold(true)
	if interior <= 0 {
		return edge.Render(b.TopLeft + b.TopRight)
	}
	label := " " + title + " "
	if interior <= CellWidth(label) {
		t := TruncateToWidth(title, interior)
		if gap := interior - CellWidth(t); gap > 0 {
			t += strings.Repeat(" ", gap)
		}
		return edge.Render(b.TopLeft) + titleStyle.Render(t) + edge.Render(b.TopRight)
	}
	left := (interior - CellWidth(label)) / 2
	right := interior - CellWidth(label) - left
	return edge.Render(b.TopLeft+strings.Repeat(b.Top, left)) +
		titleStyle.Render(label) +
		edge.Render(strings.Repeat(b.Top, right)+b.TopRight)
}

// approvalBottomBorder draws the closing frame.
func approvalBottomBorder(interior int, border string) string {
	b := lipgloss.RoundedBorder()
	if interior < 1 {
		interior = 1
	}
	return fg(border).Render(b.BottomLeft + strings.Repeat(b.Bottom, interior) + b.BottomRight)
}

// renderApprovalRow renders one interior row padded to the frame's interior.
// Styling wraps the padded plain text so an active row's background spans the
// full width, and the padding is measured after ANSI stripping.
func renderApprovalRow(row approvalRow, pending *PendingApproval, border string, theme string, interior int) string {
	plain, style := approvalRowStyle(row, pending, border, theme)
	return style.Render(padApprovalRow(plain, interior))
}

// approvalRowStyle resolves one row's plain text and style. The highlighted
// answer carries the frame accent, bold text, and a subtle tint; every other
// answer is indented in the muted body colour.
func approvalRowStyle(row approvalRow, pending *PendingApproval, border string, theme string) (string, lipgloss.Style) {
	switch row.kind {
	case approvalRowPrompt:
		return approvalIndent + row.text, fg(ColorMuted)
	case approvalRowCommand:
		return approvalIndent + row.text, fg(ColorText).Bold(true)
	case approvalRowReason:
		return approvalIndent + row.text, fg(ColorMuted)
	case approvalRowPreview:
		if strings.HasPrefix(row.text, "Preview ·") {
			return approvalIndent + row.text, fg(border).Bold(true)
		}
		pal := PaletteFor(theme)
		switch {
		case strings.HasPrefix(row.text, "+"):
			return approvalIndent + row.text, pal.DiffAdd
		case strings.HasPrefix(row.text, "-"):
			return approvalIndent + row.text, pal.DiffDelete
		case strings.HasPrefix(row.text, "@@"):
			return approvalIndent + row.text, pal.Cyan
		}
		return approvalIndent + row.text, fg(ColorText)
	case approvalRowQuestion:
		return approvalIndent + row.text, fg(ColorText)
	case approvalRowCommitSubject:
		return approvalIndent + row.text, fg(ColorText).Bold(true)
	case approvalRowCommitBody:
		return approvalIndent + row.text, fg(ColorMuted)
	case approvalRowCommitLabel:
		return approvalIndent + row.text, fg(ColorMuted).Bold(true)
	case approvalRowCommitFile:
		return approvalIndent + row.text, fg(ColorText)
	case approvalRowCommitNote:
		return approvalIndent + row.text, fg(ColorDim)
	case approvalRowFooter:
		return approvalIndent + row.text, fg(ColorDim)
	case approvalRowChoice:
		if row.index == pending.Cursor {
			return "❯ " + approvalChoiceLabel(row, pending),
				fg(border).Bold(true).Background(approvalActiveBG(theme))
		}
		return approvalIndent + approvalChoiceLabel(row, pending), fg(ColorMuted)
	default:
		return "", lipgloss.NewStyle()
	}
}

// approvalChoiceLabel numbers a choice and marks the conservative default, the
// same choice the cursor opens on.
func approvalChoiceLabel(row approvalRow, pending *PendingApproval) string {
	label := fmt.Sprintf("%d. %s", row.index+1, row.text)
	if row.index == defaultApprovalSelection(pending.Choices) {
		label += " (default)"
	}
	return label
}

// padApprovalRow pads plain text to the interior width, truncating the rare
// overlong row so a choice can never overflow the frame.
func padApprovalRow(plain string, interior int) string {
	if CellWidth(plain) > interior {
		plain = TruncateToWidth(plain, interior)
	}
	if gap := interior - CellWidth(plain); gap > 0 {
		plain += strings.Repeat(" ", gap)
	}
	return plain
}
