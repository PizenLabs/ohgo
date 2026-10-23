package tui

// Test fixtures shared by viewport, layout, selection and benchmark tests.
// They build state through State.Apply so identities and revisions are the
// same ones production uses.

import (
	"fmt"
	"strings"
	"time"
)

// syntheticDocument builds n mixed blocks: short prompts, wrapped answers,
// Unicode, ANSI-styled notices, collapsed and expanded tool results, and shell
// output. It is deliberately heterogeneous so a layout benchmark cannot win by
// assuming one cheap block shape.
func syntheticDocument(n, width int) State {
	now := time.Unix(1700000000, 0)
	entries := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		switch i % 8 {
		case 0:
			entries = append(entries, Entry{
				Kind:     EntryUser,
				UserText: fmt.Sprintf("request %d: inspect the renderer at width %d", i, width),
			})
		case 1:
			entries = append(entries, Entry{
				Kind: EntryAssistant,
				AssistantText: fmt.Sprintf(
					"answer %d with a long paragraph. %s", i,
					strings.Repeat("wrapped body text ", 12)),
			})
		case 2:
			entries = append(entries, Entry{
				Kind:   EntryNotice,
				Notice: fmt.Sprintf("note %d: 你好, мир, مرحبا — styled \x1b[31mred\x1b[0m tail", i),
			})
		case 3:
			entries = append(entries, Entry{
				Kind: EntryTool,
				Tool: &ToolEntry{
					ID:         fmt.Sprintf("tool-%d", i),
					Name:       "read",
					Status:     ToolDone,
					Invocation: fmt.Sprintf("read file-%d.go", i),
					Summary:    fmt.Sprintf("read file-%d.go", i),
					Text:       fmt.Sprintf("line one %d\nline two\nline three", i),
					StartedAt:  now,
					EndedAt:    now.Add(25 * time.Millisecond),
				},
			})
		case 4:
			entries = append(entries, Entry{
				Kind: EntryTool,
				Tool: &ToolEntry{
					ID:         fmt.Sprintf("tool-%d", i),
					Name:       "grep",
					Status:     ToolError,
					Invocation: fmt.Sprintf("grep pattern-%d", i),
					Summary:    "no matches",
					Text:       "no matches\n",
					Expanded:   true,
					StartedAt:  now,
					EndedAt:    now.Add(time.Second),
				},
			})
		case 5:
			entries = append(entries, Entry{
				Kind: EntryShell,
				Shell: &ShellEntry{
					Command:   fmt.Sprintf("printf 'shell %d\\n'", i),
					Output:    fmt.Sprintf("shell %d\n", i),
					Status:    ShellDone,
					ExitCode:  0,
					StartedAt: now,
					Duration:  time.Millisecond,
					Seq:       i,
				},
			})
		case 6:
			entries = append(entries, Entry{
				Kind: EntryTask,
				Task: &TaskProjection{
					ID:        fmt.Sprintf("task-%d", i),
					Status:    "RUNNING",
					Objective: fmt.Sprintf("objective %d", i),
					Items: []TaskItem{
						{State: "done", Label: fmt.Sprintf("step %d", i)},
						{State: "active", Label: "layout"},
					},
				},
			})
		case 7:
			entries = append(entries, Entry{
				Kind:          EntryAssistant,
				AssistantText: "short answer",
			})
		}
	}
	s := New()
	s.Apply(TerminalResized{Width: width, Height: 40})
	s.Apply(HistoryReplaced{Entries: entries})
	return s
}

// layoutFor builds a one-shot layout, the same way Render does.
func layoutFor(state State) *TranscriptLayout {
	l := &TranscriptLayout{}
	l.Refresh(&state, ComputeLayout(state))
	return l
}

// viewportFor resolves a one-shot frame.
func viewportFor(state State) *transcriptFrame {
	return newTranscriptFrame(state, ComputeLayout(state))
}
