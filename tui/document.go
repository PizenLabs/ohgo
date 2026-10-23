package tui

// Canonical transcript document.
//
// The application's truth is State.Entries: structured blocks with stable
// identities. Nothing here consults a terminal row, a cursor position, or a
// rendered ANSI string. Geometry is derived later by TranscriptLayout, and
// the renderer only projects the visible slice of that layout.
//
// Entry already is the domain block for this codebase (user text, assistant
// text, tool lifecycle, shell output, notices, task projection). Rather than
// introduce a parallel hierarchy, this file gives those blocks the identity
// and logical-copy contract the viewport layer needs.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// BlockID is the stable identity of one transcript block.
//
// It is allocated when the block enters the document and is never derived from
// a row number, screen coordinate, or scrollback position.
type BlockID string

// Block is the read-only projection of one document entry.
type Block struct {
	ID         BlockID
	Kind       EntryKind
	Index      int
	Expanded   bool
	Expandable bool
	Entry      Entry
}

// BlockState names the lifecycle state a block projects.
type BlockState string

// Block states. They are derived from the entry payload, never stored as a
// second source of truth.
const (
	BlockStatic  BlockState = "static"
	BlockPending BlockState = "pending"
	BlockRunning BlockState = "running"
	BlockDone    BlockState = "done"
	BlockError   BlockState = "error"
)

// State reports the block's lifecycle position.
func (b Block) State() BlockState {
	switch b.Kind {
	case EntryTool:
		if b.Entry.Tool == nil {
			return BlockStatic
		}
		switch b.Entry.Tool.Status {
		case ToolPending:
			return BlockPending
		case ToolRunning:
			return BlockRunning
		case ToolError:
			return BlockError
		default:
			return BlockDone
		}
	case EntryShell:
		if b.Entry.Shell == nil {
			return BlockStatic
		}
		switch b.Entry.Shell.Status {
		case ShellRunning:
			return BlockRunning
		case ShellError:
			return BlockError
		default:
			return BlockDone
		}
	default:
		return BlockStatic
	}
}

// Document is the ordered view of State.Entries.
type Document struct {
	entries []Entry
}

// Document returns the canonical block sequence.
func (s State) Document() Document {
	return Document{entries: s.Entries}
}

// Len reports the number of blocks.
func (d Document) Len() int { return len(d.entries) }

// At returns the block at a document index.
func (d Document) At(index int) (Block, bool) {
	if index < 0 || index >= len(d.entries) {
		return Block{}, false
	}
	entry := d.entries[index]
	block := Block{
		ID:       blockIDAt(d.entries, index),
		Kind:     entry.Kind,
		Index:    index,
		Entry:    entry,
		Expanded: entryExpanded(entry),
	}
	block.Expandable = block.Kind == EntryTool && entry.Tool != nil && toolExpandable(entry.Tool)
	return block, true
}

// ByID resolves a block without depending on its current position.
func (d Document) ByID(id BlockID) (Block, bool) {
	index := blockIndex(d.entries, id)
	if index < 0 {
		return Block{}, false
	}
	return d.At(index)
}

// Entries exposes the ordered entries for layout refreshes. The slice is
// read-only: callers must mutate through State.Apply.
func (d Document) Entries() []Entry { return d.entries }

// blockIDAt returns the entry's allocated ID, or a deterministic fallback for
// entries constructed directly by a caller (tests, fixtures) that bypassed
// State.Apply. The fallback is stable for a given slice position.
func blockIDAt(entries []Entry, index int) BlockID {
	if index >= 0 && index < len(entries) && entries[index].ID != "" {
		return entries[index].ID
	}
	return BlockID(fmt.Sprintf("entry-%d", index))
}

// blockIndex resolves an ID to its current position, or -1.
func blockIndex(entries []Entry, id BlockID) int {
	for i := range entries {
		if blockIDAt(entries, i) == id {
			return i
		}
	}
	return -1
}

// entryExpanded reports whether the block projects an expanded body.
func entryExpanded(entry Entry) bool {
	return entry.Kind == EntryTool && entry.Tool != nil && entry.Tool.Expanded
}

// BlockIndex resolves a block ID to its current document position.
func (s *State) BlockIndex(id BlockID) int {
	return blockIndex(s.Entries, id)
}

// ToggleBlock flips one block's presentation state.
//
// The transition goes through Apply, so expansion remains the same
// presentation-only event Ctrl+O uses: no tool runs, no repository is read,
// no task or authority state changes.
func (s *State) ToggleBlock(id BlockID) bool {
	index := s.BlockIndex(id)
	if index < 0 {
		return false
	}
	entry := &s.Entries[index]
	if entry.Kind != EntryTool || entry.Tool == nil || !toolExpandable(entry.Tool) {
		return false
	}
	toolID := entry.Tool.ID
	s.Apply(ToolToggled{ID: toolID})
	return true
}

// BlockCopyText returns the canonical logical text of one block.
//
// It is derived from structured content, never from the rendered projection:
// ANSI styling, cursor controls, padding, and renderer artifacts can never
// reach the clipboard.
func BlockCopyText(entry Entry) string {
	var text string
	switch entry.Kind {
	case EntryUser:
		text = entry.UserText
	case EntryAssistant:
		text = entry.AssistantText
	case EntrySupport:
		text = entry.SupportText
	case EntryNotice:
		text = entry.Notice
	case EntryTool:
		if entry.Tool != nil {
			text = entry.Tool.Text
			if strings.TrimSpace(text) == "" {
				text = entry.Tool.Summary
			}
		}
	case EntryShell:
		if entry.Shell != nil {
			text = entry.Shell.Command
			if body := strings.TrimRight(entry.Shell.Output, "\n"); body != "" {
				text += "\n" + body
			}
		}
	case EntryTask:
		if entry.Task != nil {
			var b strings.Builder
			if entry.Task.Objective != "" {
				b.WriteString(entry.Task.Objective)
			}
			for _, item := range entry.Task.Items {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString("[" + item.State + "] " + item.Label)
			}
			text = b.String()
		}
	case EntryRecap:
		// The completion footer is lifecycle metadata, not block content:
		// copying the block must never carry it into a clipboard payload.
		return ""
	}
	return logicalText(text)
}

// logicalText strips terminal control sequences and normalises line endings.
//
// Clipboard content is logical text. It keeps ordinary Unicode and newlines;
// SGR, OSC, CR, and other controls are removed rather than copied as bytes the
// receiving application cannot interpret.
func logicalText(text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = ansi.Strip(text)
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			// Drop residual controls; they have no logical meaning in a
			// clipboard payload.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
