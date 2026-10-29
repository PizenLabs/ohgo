package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrCorruptTask means task.md could not be parsed into a task.
var ErrCorruptTask = errors.New("task: corrupt task projection")

// RenderMarkdown renders the human- and model-readable projection of a task.
//
// It is a projection, never an authority: editing this file can change what the
// agent is told and what a human reads, but it can never grant a capability or
// authorize an action. The format is semantic, not cosmetic — the headings are
// the contract the parser reads back.
func RenderMarkdown(t Task) string {
	var sb strings.Builder
	sb.WriteString("# Task\n\n")

	status := t.Status
	if !status.Valid() {
		status = StatusPending
	}
	fmt.Fprintf(&sb, "## Status\n\n%s\n\n", status)

	sb.WriteString("## Objective\n\n")
	if obj := strings.TrimSpace(t.Objective); obj != "" {
		sb.WriteString(obj + "\n\n")
	} else {
		sb.WriteString("_Not established yet._\n\n")
	}

	sb.WriteString("## Constraints\n\n")
	writeBullets(&sb, t.Constraints)

	sb.WriteString("## Todo\n\n")
	if len(t.Todos) == 0 {
		sb.WriteString("_No plan recorded yet._\n\n")
	} else {
		for _, todo := range t.Todos {
			fmt.Fprintf(&sb, "- %s %s <!--id:%s-->\n", todoCheckbox(todo.Status), sanitizeLine(todo.Label), todo.ID)
			if evidence := strings.TrimSpace(todo.Evidence); evidence != "" {
				fmt.Fprintf(&sb, "      evidence: %s\n", sanitizeLine(evidence))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Current Step\n\n")
	if step := strings.TrimSpace(t.ActiveStep); step != "" {
		sb.WriteString(step + "\n\n")
	} else {
		sb.WriteString("_None._\n\n")
	}

	sb.WriteString("## Findings\n\n")
	writeBullets(&sb, t.Findings)

	sb.WriteString("## Next Action\n\n")
	if next := strings.TrimSpace(t.NextAction); next != "" {
		sb.WriteString(next + "\n\n")
	} else {
		sb.WriteString("_None._\n\n")
	}

	sb.WriteString("## Meta\n\n")
	fmt.Fprintf(&sb, "- id: %s\n", sanitizeLine(t.ID))
	if t.Phase != "" {
		fmt.Fprintf(&sb, "- phase: %s\n", sanitizeLine(t.Phase))
	}
	if !t.UpdatedAt.IsZero() {
		fmt.Fprintf(&sb, "- updated: %s\n", t.UpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	return sb.String()
}

func writeBullets(sb *strings.Builder, values []string) {
	wrote := false
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		fmt.Fprintf(sb, "- %s\n", sanitizeLine(value))
		wrote = true
	}
	if !wrote {
		sb.WriteString("_None._\n")
	}
	sb.WriteString("\n")
}

// todoCheckbox maps a status to its markdown marker.
func todoCheckbox(status TodoStatus) string {
	switch status {
	case TodoDone:
		return "[x]"
	case TodoActive:
		return "[>]"
	default:
		return "[ ]"
	}
}

// sanitizeLine collapses newlines so one field cannot break the document
// structure when it is read back.
func sanitizeLine(text string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(text, "\u00a0", " ")), " ")
}

// ParseMarkdown rebuilds a task from its projection.
//
// It is deliberately tolerant of prose a human added, because task.md is a
// human artifact: unknown sections are ignored and missing ones stay empty. It
// is strict about structure: a checkbox without a status marker, or an
// unparseable Meta id, is reported rather than silently dropped.
func ParseMarkdown(text string) (Task, error) {
	if strings.TrimSpace(text) == "" {
		return Task{}, fmt.Errorf("%w: empty document", ErrCorruptTask)
	}
	var (
		t       Task
		section string
	)
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## ") {
			section = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			continue
		}
		if trimmed == "" || trimmed == "_None._" || trimmed == "_Not established yet._" || trimmed == "_No plan recorded yet._" {
			continue
		}

		switch section {
		case "objective":
			t.Objective = appendLine(t.Objective, trimmed)
		case "constraints":
			if bullet, ok := bulletValue(trimmed); ok {
				t.Constraints = append(t.Constraints, bullet)
			}
		case "todo":
			todo, ok := parseTodoLine(trimmed)
			if !ok {
				return Task{}, fmt.Errorf("%w: malformed todo line %q", ErrCorruptTask, trimmed)
			}
			// An indented "evidence:" continuation belongs to the item just
			// read; the parser is line-based, so it is consumed here.
			if i+1 < len(lines) {
				next := strings.TrimSpace(lines[i+1])
				if strings.HasPrefix(next, "evidence:") {
					todo.Evidence = strings.TrimSpace(strings.TrimPrefix(next, "evidence:"))
					i++
				}
			}
			t.Todos = append(t.Todos, todo)
		case "current step":
			t.ActiveStep = appendLine(t.ActiveStep, trimmed)
		case "findings":
			if bullet, ok := bulletValue(trimmed); ok {
				t.Findings = append(t.Findings, bullet)
			}
		case "next action":
			t.NextAction = appendLine(t.NextAction, trimmed)
		case "status":
			if t.Status == "" {
				t.Status = Status(trimmed)
			}
		case "meta":
			if bullet, ok := bulletValue(trimmed); ok {
				applyMeta(&t, bullet)
			}
		}
	}

	if t.Status == "" {
		t.Status = StatusPending
	}
	if !t.Status.Valid() {
		return Task{}, fmt.Errorf("%w: unknown status %q", ErrCorruptTask, t.Status)
	}
	if strings.TrimSpace(t.ID) == "" {
		return Task{}, fmt.Errorf("%w: missing id", ErrCorruptTask)
	}
	return t, nil
}

func appendLine(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + " " + addition
}

// bulletValue strips a leading markdown bullet, reporting whether one was there.
func bulletValue(line string) (string, bool) {
	if !strings.HasPrefix(line, "- ") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "- ")), true
}

// parseTodoLine reads one checkbox line and its inline id marker.
func parseTodoLine(line string) (Todo, bool) {
	if !strings.HasPrefix(line, "- [") {
		return Todo{}, false
	}
	rest := line[len("- ["):]
	if len(rest) < 3 || rest[1] != ']' {
		return Todo{}, false
	}
	var status TodoStatus
	switch rest[0] {
	case ' ':
		status = TodoTodo
	case 'x', 'X':
		status = TodoDone
	case '>':
		status = TodoActive
	default:
		return Todo{}, false
	}
	label := strings.TrimSpace(rest[2:])

	todo := Todo{Status: status}
	if start := strings.Index(label, "<!--id:"); start >= 0 {
		marker := label[start:]
		if end := strings.Index(marker, "-->"); end >= 0 {
			todo.ID = strings.TrimSpace(strings.TrimPrefix(marker[:end], "<!--id:"))
			label = strings.TrimSpace(label[:start])
		}
	}
	todo.Label = label
	if todo.Label == "" && todo.ID == "" {
		return Todo{}, false
	}
	return todo, true
}

// applyMeta reads one `key: value` meta bullet into the task.
func applyMeta(t *Task, bullet string) {
	key, value, ok := strings.Cut(bullet, ":")
	if !ok {
		return
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.TrimSpace(value)
	switch key {
	case "id":
		t.ID = value
	case "phase":
		t.Phase = value
	case "updated":
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			t.UpdatedAt = parsed
		}
	}
}
