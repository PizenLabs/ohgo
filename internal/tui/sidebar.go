package tui

import (
	"fmt"
	"strings"
)

// Contextual workspace chrome.
//
// The task tracker and integrations are projections of state the runtime
// already owns. They are rendered beside the transcript only while an open
// task has at least one open todo, so an idle, question-only, or completed
// session keeps the full-width conversation. The rail sits outside the
// transcript, so scrolling the conversation cannot move it and a tick never
// repaints the transcript just to keep a panel current. The sidebar never
// duplicates the global status bar: token usage, context percentage, path,
// branch, session, and workspace authority stay in the footer and composer
// prefix.

// Sidebar content bounds. They keep a pathological todo list or a large MCP
// roster from turning the rail into an unbounded per-frame walk.
const (
	sidebarMaxTaskItems       = 6
	sidebarMaxIntegrations    = 4
	sidebarMaxInsightRows     = 4
	stickyTaskMaxObjectiveLen = 72

	// stickyTaskMaxRows bounds the pinned task area on tiers without the rail.
	// The block includes its trailing spacer, so at most eight projection rows
	// may compete with the conversation.
	stickyTaskMaxRows = 9
	// stickyTaskMinTranscriptRows is the floor the pinned area must leave the
	// conversation. A task never turns the transcript into a status-only pane.
	stickyTaskMinTranscriptRows = 3
)

// pinnedTaskArea is the medium/narrow task surface: the projection block,
// bounded to the layout's claimed rows, plus the blank spacer that separates
// it from the scrolling transcript.
//
// It is chrome, not a transcript block: the caller paints it between the title
// separator and the conversation, so the viewport can only move the rows below
// it. When the budget is a single row it falls back to the compact header, which
// keeps the task anchored even on a viewport too short for the block.
func pinnedTaskArea(state State, pal *Palette, layout Layout, maxRows int) []string {
	task := activeTaskProjection(&state)
	if task == nil || maxRows <= 0 {
		return nil
	}
	if maxRows == 1 {
		if line := renderStickyTaskRow(state, pal, layout); line != "" {
			return []string{line}
		}
		return nil
	}
	lines := taskLines(task, pal, layout)
	// The final claimed row is the spacer. Reserve one content row for the
	// overflow marker when the projection is longer than the screen allows.
	content := maxRows - 1
	if len(lines) > content {
		overflow := len(lines) - (content - 1)
		lines = lines[:content-1]
		marker := "  … +" + formatTaskCount(overflow) + " more"
		if focus := taskFocusLabel(task); focus != "" {
			marker += " · ◼ " + sanitizeInline(focus)
		}
		if width := textWidth(layout); width > 0 {
			marker = TruncateToWidth(marker, width)
		}
		lines = append(lines, pal.Status.Render(marker))
	}
	return append(lines, "")
}

// pinnedTaskAreaLimit is the row budget the pinned task area may claim while
// leaving the conversation enough rows to stay a conversation.
func pinnedTaskAreaLimit(layout Layout) int {
	limit := layout.ConversationHeight - stickyTaskMinTranscriptRows
	if limit < 1 {
		limit = 1
	}
	if limit > stickyTaskMaxRows {
		limit = stickyTaskMaxRows
	}
	return limit
}

// renderStickyTaskRow is the narrow-terminal fallback: exactly one row under
// the title bar, carrying only the facts needed to keep the current task
// anchored while the transcript scrolls. It exists only while an open task has
// at least one open todo; the transcript's own task block returns once the task
// ends.
func renderStickyTaskRow(state State, pal *Palette, layout Layout) string {
	task := activeTaskProjection(&state)
	if task == nil {
		return ""
	}
	parts := []string{"task"}
	if objective := strings.TrimSpace(sanitizeInline(task.Objective)); objective != "" {
		if CellWidth(objective) > stickyTaskMaxObjectiveLen {
			objective = TruncateToWidth(objective, stickyTaskMaxObjectiveLen)
		}
		parts = append(parts, objective)
	}
	if status := taskStatusWord(task.Status); status != "" {
		parts = append(parts, status)
	}
	if progress := taskProgressLabel(task); progress != "" {
		parts = append(parts, progress)
	}
	if focus := taskFocusLabel(task); focus != "" {
		parts = append(parts, focus)
	}
	line := strings.Join(parts, " · ")
	if width := columnWidth(state, layout); width > 0 {
		line = TruncateToWidth(line, width)
	}
	if strings.EqualFold(strings.TrimSpace(task.Status), "NEEDS_REVIEW") {
		return pal.Warning.Render(line)
	}
	return pal.Notice.Render(line)
}

// renderWorkspaceSidebar paints the fixed right rail: task tracker first,
// then active integrations, then non-redundant task insights. It returns at
// most the body height; the frame composer pads the remainder so the divider
// stays aligned.
func renderWorkspaceSidebar(state State, pal *Palette, layout Layout) string {
	height := layout.Height - layout.HeaderLines
	if height <= 0 || layout.SidebarWidth <= 0 {
		return ""
	}
	task := activeTaskProjection(&state)
	if task == nil {
		return ""
	}
	inner := layout.SidebarWidth - 2
	if inner < 1 {
		inner = 1
	}
	lines := make([]string, 0, height+4)
	add := func(text string, style interface{ Render(...string) string }) {
		text = oneLine(text)
		if CellWidth(text) > inner {
			text = TruncateToWidth(text, inner)
		}
		lines = append(lines, " "+style.Render(text))
	}
	blank := func() { lines = append(lines, "") }

	add("TASK", pal.Notice.Bold(true))
	if objective := strings.TrimSpace(sanitizeInline(task.Objective)); objective != "" {
		add(objective, pal.Status)
	}
	summaryStyle := pal.Status
	if strings.EqualFold(strings.TrimSpace(task.Status), "NEEDS_REVIEW") {
		summaryStyle = pal.Warning
	}
	add(sidebarTaskSummary(task), summaryStyle)
	itemBudget := sidebarTaskItemBudget(height, len(task.Items))
	for i, item := range task.Items {
		if i >= itemBudget {
			remaining := len(task.Items) - itemBudget
			if remaining > 0 {
				add(fmt.Sprintf("… +%d more", remaining), pal.Muted)
			}
			break
		}
		style := pal.Status
		switch item.State {
		case taskStateDone:
			style = pal.ToolDone
		case taskStateActive:
			style = pal.ToolRun
		}
		add(taskGlyph(item.State)+" "+sanitizeInline(item.Label), style)
	}
	currentReview := task.ReviewReason != "" &&
		strings.EqualFold(strings.TrimSpace(task.Status), "NEEDS_REVIEW")
	if len(task.Items) == 0 && !currentReview {
		add("no todos", pal.Muted)
	}

	blank()
	add("ACTIVE INTEGRATIONS", pal.Notice.Bold(true))
	if len(state.Integrations) == 0 {
		add("none active", pal.Muted)
	} else {
		shown := len(state.Integrations)
		if shown > sidebarMaxIntegrations {
			shown = sidebarMaxIntegrations
		}
		for _, item := range state.Integrations[:shown] {
			add(sidebarIntegrationLine(item), sidebarIntegrationStyle(item, pal))
		}
		if hidden := len(state.Integrations) - shown; hidden > 0 {
			add(fmt.Sprintf("… +%d more", hidden), pal.Muted)
		}
	}

	insights := sidebarInsights(task)
	blank()
	add("INSIGHTS", pal.Notice.Bold(true))
	if len(insights) == 0 {
		add("no additional context", pal.Muted)
	} else {
		if len(insights) > sidebarMaxInsightRows {
			insights = insights[:sidebarMaxInsightRows]
		}
		for _, insight := range insights {
			add(insight, pal.Status)
		}
	}
	return fitSidebarLines(lines, height)
}

// sidebarTaskSummary names lifecycle and progress once. The objective is the
// previous row, so it is deliberately not repeated here.
func sidebarTaskSummary(task *TaskProjection) string {
	parts := []string{}
	if task == nil {
		return ""
	}
	if status := taskStatusWord(task.Status); status != "" {
		parts = append(parts, status)
	}
	if progress := taskProgressLabel(task); progress != "" {
		parts = append(parts, progress)
	}
	if state := strings.ToUpper(sanitizeInline(strings.TrimSpace(task.ObjectiveState))); state != "" && state != "ACTIVE" {
		parts = append(parts, strings.ToLower(state))
	}
	return strings.Join(parts, " · ")
}

// taskStatusWord normalizes the producer's lifecycle word for compact chrome.
func taskStatusWord(status string) string {
	return strings.ToLower(sanitizeInline(strings.TrimSpace(status)))
}

// taskProgressLabel reports completed and active counts without inventing a
// denominator when the producer supplied no todos.
func taskProgressLabel(task *TaskProjection) string {
	if task == nil || len(task.Items) == 0 {
		return ""
	}
	done, active := 0, 0
	for _, item := range task.Items {
		switch item.State {
		case taskStateDone:
			done++
		case taskStateActive:
			active++
		}
	}
	label := fmt.Sprintf("%d/%d done", done, len(task.Items))
	if active > 0 {
		label += fmt.Sprintf(" · %d active", active)
	}
	return label
}

// taskFocusLabel names the one in-progress todo. The list below already shows
// it; this is only for the single-row sticky header, where there is no list.
func taskFocusLabel(task *TaskProjection) string {
	if task == nil {
		return ""
	}
	for _, item := range task.Items {
		if item.State == taskStateActive {
			return sanitizeInline(item.Label)
		}
	}
	return ""
}

// sidebarTaskItemBudget keeps the tracker visible when the rail is short:
// the object and lifecycle always win, and the todo list yields first.
func sidebarTaskItemBudget(height, total int) int {
	budget := (height - 8) / 2
	if budget < 1 {
		budget = 1
	}
	if budget > sidebarMaxTaskItems {
		budget = sidebarMaxTaskItems
	}
	if budget > total {
		budget = total
	}
	return budget
}

// sidebarIntegrationLine keeps kind, name, and truthful state on one row.
func sidebarIntegrationLine(item IntegrationStatus) string {
	marker := "○"
	if item.Active {
		marker = "●"
	}
	if item.Error != "" || strings.EqualFold(integrationState(item), "error") {
		marker = "!"
	}
	kind := strings.ToUpper(string(item.Kind))
	if kind == "" {
		kind = "INTEGRATION"
	}
	parts := []string{kind}
	if name := strings.TrimSpace(item.Name); name != "" {
		parts = append(parts, name)
	}
	state := integrationState(item)
	if state == "" {
		if item.Active {
			state = "active"
		} else {
			state = "configured"
		}
	}
	parts = append(parts, state)
	line := marker + " " + strings.Join(parts, " · ")
	if item.Error != "" {
		line += " · " + item.Error
	} else if item.Detail != "" {
		line += " · " + item.Detail
	}
	return line
}

// integrationState resolves the canonical lifecycle word, accepting the
// Status alias used by direct callers.
func integrationState(item IntegrationStatus) string {
	if state := strings.TrimSpace(item.State); state != "" {
		return state
	}
	return strings.TrimSpace(item.Status)
}

func sidebarIntegrationStyle(item IntegrationStatus, pal *Palette) interface{ Render(...string) string } {
	if item.Error != "" || strings.EqualFold(integrationState(item), "error") {
		return pal.ToolError
	}
	if item.Active {
		return pal.ToolDone
	}
	return pal.Status
}

// sidebarInsights returns the task facts the tracker does not already show.
// Tokens, context, path, branch, session, and authority are intentionally
// absent: the global footer and composer prefix own them.
func sidebarInsights(task *TaskProjection) []string {
	if task == nil {
		return nil
	}
	var out []string
	if review := strings.TrimSpace(task.ReviewReason); review != "" &&
		strings.EqualFold(strings.TrimSpace(task.Status), "NEEDS_REVIEW") {
		out = append(out, "review: "+sanitizeInline(review))
	}
	if recovery := strings.TrimSpace(task.Recovery); recovery != "" {
		out = append(out, "recovery: "+sanitizeInline(recovery))
	}
	if warning := strings.TrimSpace(task.Warning); warning != "" {
		out = append(out, "continuity: "+sanitizeInline(warning))
	}
	if errText := strings.TrimSpace(task.Err); errText != "" {
		out = append(out, "checkpoint: "+sanitizeInline(errText))
	}
	if id := shortTaskID(strings.TrimSpace(task.ID)); id != "" {
		out = append(out, "task: "+id)
	}
	return out
}

// fitSidebarLines bounds the rail to exactly the body height. It pads blank
// rows so the divider column keeps its length and the frame stays anchored.
func fitSidebarLines(lines []string, height int) string {
	if height <= 0 {
		return ""
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
