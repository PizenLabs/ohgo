package tui

// LayoutMode names the responsive tier the workspace renders in.
//
// Thresholds are starting points, not hardware constants: Wide keeps full
// metadata, Medium drops secondary detail, Narrow keeps the conversation
// readable above all, and Mini concedes the terminal is too small for the
// workbench and says so instead of overlapping regions.
type LayoutMode string

// Layout modes.
const (
	// LayoutWide is 100 columns or more: full header, metadata, detail.
	LayoutWide LayoutMode = "wide"
	// LayoutMedium is 70-99 columns: reduced metadata, full conversation.
	LayoutMedium LayoutMode = "medium"
	// LayoutNarrow is under 70 columns: conversation first, chrome minimal.
	LayoutNarrow LayoutMode = "narrow"
	// LayoutMini is a viewport too small for the workbench.
	LayoutMini LayoutMode = "mini"
)

// Breakpoints. Kept as named constants so tests pin the contract.
const (
	// WideMinWidth is the first width that renders the full workspace.
	WideMinWidth = 100
	// MediumMinWidth is the first width that renders reduced chrome.
	MediumMinWidth = 70
	// MiniMinWidth is the first width the workbench attempts at all.
	MiniMinWidth = 40
	// MiniMinHeight is the first height the workbench attempts at all.
	MiniMinHeight = 8
)

// Layout is the computed geometry for one render.
//
// It is derived from State plus the terminal size, never stored: storing it
// would let a resize render stale chrome. Compute once per View call.
type Layout struct {
	// Mode is the responsive tier.
	Mode LayoutMode
	// Width and Height are the terminal size in cells.
	Width  int
	Height int
	// ContentWidth is the cells available for message text after chrome
	// padding. Wrapping targets this, never the raw terminal width.
	ContentWidth int
	// ConversationHeight is the rows reserved for the transcript.
	ConversationHeight int
	// HeaderLines, ComposerLines, StatusLines size the chrome.
	HeaderLines   int
	ComposerLines int
	StatusLines   int
	// ShowDetail reports secondary metadata survives (context %, session,
	// cwd). Narrow tiers drop it before touching conversation width.
	ShowDetail bool
	// ShowHints reports keyboard hints fit without stealing rows.
	ShowHints bool
}

// LayoutForMode resolves a width to its tier. Unknown (zero) widths render
// everything without slicing, matching the historical no-size behaviour.
func LayoutForMode(width, height int) LayoutMode {
	if width <= 0 || height <= 0 {
		return LayoutWide
	}
	if width < MiniMinWidth || height < MiniMinHeight {
		return LayoutMini
	}
	switch {
	case width >= WideMinWidth:
		return LayoutWide
	case width >= MediumMinWidth:
		return LayoutMedium
	default:
		return LayoutNarrow
	}
}

// ComputeLayout derives geometry from state and terminal size.
//
// The conversation receives whatever the chrome does not claim; the composer
// stays visible; header and status stay compact. Dialogs are bounded by the
// caller against ConversationHeight so they never exceed the viewport.
func ComputeLayout(s State) Layout {
	mode := LayoutForMode(s.Width, s.Height)
	if s.Width <= 0 || s.Height <= 0 {
		return Layout{
			Mode:         LayoutWide,
			Width:        s.Width,
			Height:       s.Height,
			ContentWidth: 0, // zero means "do not wrap"
			ShowDetail:   true,
			ShowHints:    true,
		}
	}
	content := s.Width - 2
	if content < 10 {
		content = 10
	}
	if mode == LayoutNarrow {
		content = s.Width - 1
		if content < 10 {
			content = 10
		}
	}
	header := 2 // header line + separator
	status := 1
	composer := composerHeight(s, content, mode)
	hints := 0
	if mode == LayoutWide && s.Height >= 20 {
		hints = 1
	}
	chrome := header + status + hints + composer
	conv := s.Height - chrome
	if conv < 1 {
		conv = 1
	}
	return Layout{
		Mode:               mode,
		Width:              s.Width,
		Height:             s.Height,
		ContentWidth:       content,
		ConversationHeight: conv,
		HeaderLines:        header,
		ComposerLines:      composer,
		StatusLines:        status,
		ShowDetail:         mode == LayoutWide || mode == LayoutMedium,
		ShowHints:          hints > 0,
	}
}

// composerHeight sizes the composer in rows: wrapped input lines plus one hint
// row on wide terminals. Bounded so a pasted novel cannot evict the
// conversation entirely.
func composerHeight(s State, contentWidth int, mode LayoutMode) int {
	lines := 1
	if s.Secret != nil {
		lines = 2 // masked line + hint
	} else {
		text := s.Input
		if text == "" {
			lines = 1
		} else {
			n := 0
			for _, part := range splitInputLines(text) {
				wrapped := WrapToWidth(part, contentWidth-2)
				n += len(wrapped)
			}
			if n < 1 {
				n = 1
			}
			lines = n
		}
		if mode == LayoutMini {
			// Mini never renders; keep the count sane for callers.
			if lines > 2 {
				lines = 2
			}
			return lines
		}
		if lines > 6 {
			lines = 6
		}
	}
	return lines
}

// pickerRowsFor sizes the picker's result window from the terminal size:
// the conversation height minus the picker's own chrome (header, search,
// and footer lines). Zero or negative means unknown size: render everything.
func pickerRowsFor(s State) int {
	if s.Height <= 0 {
		return 0
	}
	layout := ComputeLayout(s)
	rows := layout.ConversationHeight - 3 // header + search + footer
	if rows < 1 {
		rows = 1
	}
	return rows
}

// splitInputLines splits composer text preserving empty lines.
func splitInputLines(text string) []string {
	if text == "" {
		return []string{""}
	}
	out := []string{}
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			out = append(out, text[start:i])
			start = i + 1
		}
	}
	return append(out, text[start:])
}
