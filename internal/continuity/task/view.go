package task

import (
	"fmt"
	"strings"
)

// ItemState is the display state of one projected todo.
type ItemState string

// Projection states, matching the semantic todo vocabulary.
const (
	// ItemPending is work not started.
	ItemPending ItemState = "pending"
	// ItemActive is the work being done now.
	ItemActive ItemState = "active"
	// ItemDone is finished work.
	ItemDone ItemState = "done"
)

// Item is one projected todo line: a state and a label, with no presentation
// glyph. The renderer chooses the glyph, so the vocabulary lives in one place.
type Item struct {
	// State is the display state.
	State ItemState
	// Label is the todo text.
	Label string
}

// Items projects the task's todos for a UI.
//
// The vocabulary is intentionally three-valued, so a consumer never has to
// infer a display state from a workflow status it does not know.
func Items(t Task) []Item {
	items := make([]Item, 0, len(t.Todos))
	for _, todo := range t.Todos {
		state := ItemPending
		switch todo.Status {
		case TodoDone:
			state = ItemDone
		case TodoActive:
			state = ItemActive
		}
		items = append(items, Item{State: state, Label: todo.Label})
	}
	return items
}

// contextBudget bounds the reconstructed context. Semantic continuity needs
// orientation, not a transcript dump: the point is to keep the model oriented
// without letting reconstruction become the unbounded history it replaced.
const contextBudget = 6000

// contextFieldCap bounds one field. A single giant finding or constraint list
// must not crowd out the orientation that follows it.
const contextFieldCap = 1200

// contextFindings bounds how many findings reconstruction carries: recent
// observations matter, and an append-only log is not orientation.
const contextFindings = 8

// Context returns deterministic orientation blocks for a task.
//
// Every field is drawn from recorded state; nothing is inferred and nothing is
// invented. The same task and last action always produce the same blocks, which
// is what makes context reconstruction testable without a model.
//
// Ordering is deliberate: the fields a resumed model needs first (objective,
// status, remaining work, next action) precede findings, so a long finding log
// cannot push the orientation off the budget.
func Context(t Task, lastAction string) []string {
	var blocks []string
	add := func(label, value string) {
		if value = strings.TrimSpace(value); value != "" {
			blocks = append(blocks, label+": "+truncate(value, contextFieldCap))
		}
	}

	add("Objective", t.Objective)
	if t.Status != "" {
		add("Status", string(t.Status))
	}
	add("Phase", t.Phase)
	add("Constraints", strings.Join(t.Constraints, "; "))
	add("Todo", renderTodoContext(t))

	if active, ok := t.ActiveTodo(); ok {
		add("Current step", active.Label)
	}
	add("Next action", t.NextAction)
	add("Last action", lastAction)
	add("Findings", recentFindings(t.Findings))

	return boundBlocks(blocks, contextBudget)
}

// recentFindings renders the tail of the finding log, newest last.
func recentFindings(findings []string) string {
	if len(findings) == 0 {
		return ""
	}
	start := 0
	if len(findings) > contextFindings {
		start = len(findings) - contextFindings
	}
	recent := make([]string, 0, len(findings)-start)
	for _, finding := range findings[start:] {
		if finding = strings.TrimSpace(finding); finding != "" {
			recent = append(recent, truncate(finding, 240))
		}
	}
	return strings.Join(recent, "; ")
}

// truncate bounds a value, marking the cut so a reader knows it is incomplete.
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// renderTodoContext renders the todo list on one line, marking each item.
func renderTodoContext(t Task) string {
	if len(t.Todos) == 0 {
		return ""
	}
	parts := make([]string, 0, len(t.Todos))
	for _, todo := range t.Todos {
		marker := "TODO"
		switch todo.Status {
		case TodoDone:
			marker = "DONE"
		case TodoActive:
			marker = "ACTIVE"
		}
		entry := fmt.Sprintf("%s %s", marker, todo.Label)
		if todo.Evidence != "" {
			entry += " (evidence: " + todo.Evidence + ")"
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, "; ")
}

// boundBlocks trims the block list to a total character budget, dropping from
// the end so the most important orientation (objective, status, todos) survives
// a long findings or evidence list.
func boundBlocks(blocks []string, budget int) []string {
	total := 0
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if total+len(block) > budget {
			break
		}
		total += len(block)
		out = append(out, block)
	}
	return out
}
