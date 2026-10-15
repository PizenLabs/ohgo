package tui

import (
	"fmt"
	"strings"

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
	approvalRowQuestion
	approvalRowSpacer
	approvalRowChoice
	approvalRowFooter
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
	rows = append(rows, approvalRow{kind: approvalRowQuestion, text: approvalQuestion, drop: dropQuestion})
	rows = append(rows, approvalRow{kind: approvalRowSpacer, drop: dropSpacerTop})
	for i, choice := range pending.Choices {
		rows = append(rows, approvalRow{kind: approvalRowChoice, index: i, text: sanitizeInline(choice), drop: protectedRow})
	}
	rows = append(rows, approvalRow{kind: approvalRowSpacer, drop: dropSpacerBottom})
	rows = append(rows, approvalRow{kind: approvalRowFooter, text: approvalFooterHint, drop: dropFooter})
	return rows
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
	sb.WriteString(approvalTitleBorder(interior, border))
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
func approvalTitleBorder(interior int, border string) string {
	b := lipgloss.RoundedBorder()
	edge := fg(border)
	titleStyle := fg(border).Bold(true)
	if interior <= 0 {
		return edge.Render(b.TopLeft + b.TopRight)
	}
	label := " " + approvalTitle + " "
	if interior <= CellWidth(label) {
		t := TruncateToWidth(approvalTitle, interior)
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
	case approvalRowQuestion:
		return approvalIndent + row.text, fg(ColorText)
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
