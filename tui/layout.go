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
	// ThinkingLines is the rows the projected activity line claims (0 or 1).
	// Render emits exactly this many rows, so the transcript budget and the
	// painted frame agree and the chrome never floats.
	ThinkingLines int
	// PaletteLines is the inline-palette suggestion rows the composer will
	// render. It equals what composerHeight reserves: the renderer truncates
	// to it, so reserved and rendered rows always agree even on viewports
	// shorter than the full suggestion window. Negative means uncapped
	// (unknown size).
	PaletteLines int
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
// stays visible; header and hint bar stay compact. Dialogs are bounded by the
// caller against ConversationHeight so they never exceed the viewport.
func ComputeLayout(s State) Layout {
	mode := LayoutForMode(s.Width, s.Height)
	if s.Width <= 0 || s.Height <= 0 {
		return Layout{
			Mode:          LayoutWide,
			Width:         s.Width,
			Height:        s.Height,
			ContentWidth:  0, // zero means "do not wrap"
			ShowDetail:    true,
			ShowHints:     true,
			PaletteLines:  -1, // uncapped: render everything
			ThinkingLines: thinkingRows(s),
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
	// No middle status row on the main canvas: the header owns identity,
	// the composer prefix owns the authority, and the hint bar owns metrics
	// and path. Reserving a row for a line that never renders would short
	// the viewport by one.
	status := 0
	hints := 0
	// An open approval captures enter, esc, alt+enter, and the scroll keys, so
	// the composer hint row would advertise bindings the dialog has taken over.
	// Drop it here rather than at render time: reserved and rendered rows have
	// to agree or the frame stops filling the viewport. The dialog prints its
	// own key line, so nothing is left untaught.
	//
	// The bottom telemetry footer renders wherever the composer does, on
	// every tier: like the old status row it is the one context row narrow
	// and medium screens get, so gating it on width would drop session and
	// path exactly where space is scarcest.
	if s.Picker == nil && s.Plane == nil && s.SupportPanel == nil && s.StatusModal == nil && s.UsageModal == nil && s.Pending == nil && s.QuitConfirm == nil && s.Permissions == nil && s.Diff == nil {
		hints = 1
	}
	if hints == 0 {
		// Modal or approval: the hint bar is gone (the dialog or overlay
		// owns the keys), so the status row holds the single context row
		// and keeps reserved == rendered.
		status = 1
	}
	composer, sugg := composerHeight(s, content, mode, hints)
	// An open overlay hides the composer (and its hints row belongs to the
	// transcript chrome, not the overlay): reserving rows for regions that
	// never render would short the viewport and unpin the footer.
	// The overlay area fills the released rows instead. The quit popup is a
	// centered modal like the plane and support surfaces, so it hides the
	// composer the same way.
	if s.Picker != nil || s.Plane != nil || s.SupportPanel != nil || s.StatusModal != nil || s.UsageModal != nil || s.QuitConfirm != nil || s.Permissions != nil || s.Diff != nil {
		composer = 0
		sugg = 0
	}
	thinking := thinkingRows(s)
	chrome := header + status + hints + composer + thinking
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
		ThinkingLines:      thinking,
		ShowDetail:         mode == LayoutWide || mode == LayoutMedium,
		ShowHints:          hints > 0,
		PaletteLines:       sugg,
	}
}

// thinkingMinHeight is the shortest viewport that still paints the activity
// line. Below it the frame is at the tiny-viewport floor where regions already
// concede rows, and the header still names the phase, so nothing is
// misreported. Unknown sizes (zero) are unbudgeted and always paint it.
const thinkingMinHeight = 10

// thinkingRows is the rows the activity line claims: one while an activity is
// active on the normal surface, none otherwise. A modal overlay owns the
// screen, so the line stays out of that surface's budget rather than shrinking
// a region it does not paint on; the header still carries the phase there.
func thinkingRows(s State) int {
	if s.Picker != nil || s.Plane != nil || s.SupportPanel != nil || s.Permissions != nil || s.StatusModal != nil || s.UsageModal != nil || s.Diff != nil {
		return 0
	}
	if s.Height > 0 && s.Height < thinkingMinHeight {
		return 0
	}
	if ProjectThinking(s).Active {
		return 1
	}
	return 0
}

// composerHeight sizes the composer in rows: the two framing rules plus
// hard-wrapped input lines, inline palette suggestions, and the interaction
// state line. Bounded so a pasted novel cannot evict the conversation. It
// uses the same wrap as renderComposer so the reserved rows and the rendered
// rows agree. It also returns the suggestion rows, capped so the chrome
// leaves the transcript at least one row: suggestions yield first — rules,
// input, and state never shrink for them. The renderer truncates to the
// same count via Layout.PaletteLines.
func composerHeight(s State, contentWidth int, mode LayoutMode, hintRows int) (lines, sugg int) {
	lines = 2 // the two framing rules
	if s.Secret != nil {
		// contentWidth is clamped to at least 10 wherever this runs, so
		// contentWidth-2 is exactly what composerWrapWidth yields there.
		masked := len(secretMaskedRows(s.Secret.Label, s.Input, contentWidth-2))
		if masked > maxComposerRows {
			masked = maxComposerRows
		}
		lines += masked + 1 // masked block + hint
		return lines, 0
	}
	inputRows := 1
	text := s.Input
	if text != "" {
		// Same sanitized text the renderer folds: pasted escapes must not
		// change the reserved row count behind the view's back. The wrap
		// budget subtracts the live mode prefix ("ask > " is wider than
		// "> "), matching composerTextWidthFor so reserved and rendered rows
		// agree.
		clean, _ := sanitizeWithCursor(text, 0)
		wrapWidth := contentWidth - CellWidth(composerPrefix(s))
		if wrapWidth < 1 {
			wrapWidth = 1
		}
		rows, _, _, _ := layoutComposer(clean, -1, wrapWidth)
		inputRows = len(rows)
		if inputRows < 1 {
			inputRows = 1
		}
		if inputRows > maxComposerRows {
			inputRows = maxComposerRows
		}
	}
	lines += inputRows
	stateRows := 0
	if composerStateRows(s) > 0 {
		stateRows = 1
		lines++
	}
	sugg = paletteRows(s)
	if s.Height > 0 {
		maxSugg := s.Height - 2 - 1 - hintRows - 2 - inputRows - stateRows - 1
		if maxSugg < 0 {
			maxSugg = 0
		}
		if sugg > maxSugg {
			sugg = maxSugg
		}
	}
	// A palette needs both borders plus one content row. Below that the
	// renderer draws nothing, so reserving rows would short the frame.
	if sugg > 0 && sugg < paletteBorderRows+1 {
		sugg = 0
	}
	lines += sugg
	if mode == LayoutMini {
		// Mini never renders; keep the count sane for callers.
		if lines > 2 {
			lines = 2
		}
		return lines, 0
	}
	return lines, sugg
}

// composerStateRows reports whether the queued-prompts line shows. Busy and
// approval states live in the header, status, and modal (see
// composerStateLine); only the queue needs this row.
func composerStateRows(s State) int {
	if s.Pending != nil || s.QuitConfirm != nil {
		return 0
	}
	if len(s.Queued) > 0 {
		return 1
	}
	return 0
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

// planeDetailRows is the number of rows the model control plane reserves below
// its list for the highlighted model's detail. It shrinks with the tier so a
// narrow terminal still shows a usable list.
func planeDetailRows(mode LayoutMode) int {
	switch mode {
	case LayoutWide:
		return 9
	case LayoutMedium:
		return 6
	default:
		return 3
	}
}

// planeRowsFor sizes the control plane's list window so the panes fill the
// modal interior: the list gets whatever the taller pane's chrome does not
// claim. The right pane's real detail length (not a tier constant) sizes it,
// so a tall dialog shows long lists instead of empty padding, and the detail
// block — role line and effort radio included — always fits alongside them.
// Zero means unknown size, in which case everything renders.
func planeRowsFor(s State) int {
	if s.Height <= 0 {
		return 0
	}
	rect := PlaneModalRect(s.Width, s.Height)
	// Borders (2) + footer (1); the top border carries the title and the
	// divider above the footer claims one body row.
	contentH := rect.Height - 3
	if contentH < 1 {
		contentH = 1
	}
	bodyH := contentH - 1
	if bodyH < 1 {
		bodyH = 1
	}
	if p := s.Plane; p != nil && p.Stage != PlaneStageBrowse {
		// The assignment flow is a single bounded body: a title, the summary
		// fields, a separator, then the step. The list gets whatever remains.
		rows := bodyH - 6
		if rows < 1 {
			rows = 1
		}
		return rows
	}
	// Left chrome: heading, separator, search heading, search box, count,
	// plus room for the overflow marker.
	need := 6
	if p := s.Plane; p != nil && p.Mode != PlaneRoles {
		detailLen := len(planeModelDetail(p, PaletteFor(s.Theme), 80, 99))
		// Right chrome: heading, separator, detail, plus the overflow
		// marker when the list does not fit (one fewer list row keeps the
		// detail — role line and effort radio — on screen).
		need = 2 + detailLen
		rows := bodyH - need
		if len(p.VisibleModels()) > rows && rows > 1 {
			rows--
		}
		if rows < 1 {
			rows = 1
		}
		return rows
	}
	rows := bodyH - need
	if rows < 1 {
		rows = 1
	}
	return rows
}
