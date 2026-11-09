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
	// HeaderLines, TaskLines, TelemetryLines, ComposerLines, StatusLines size
	// the chrome.
	HeaderLines    int
	TelemetryLines int
	ComposerLines  int
	StatusLines    int
	// TaskLines is the sticky active-task row below the title bar on tiers
	// that do not render the sidebar. It is zero whenever no open task has an
	// open todo: wide terminals keep the tracker in the sidebar instead, so
	// exactly one task surface is ever visible and idle or question-only
	// sessions render none.
	TaskLines int
	// TaskTracker reports that the sticky/sidebar task surface owns the
	// projection, so the in-transcript task block must not repeat it.
	TaskTracker bool
	// Sidebar reports that the full-screen canvas is split into the
	// conversation column and the contextual right sidebar. It is only true
	// while an open task has at least one open todo; without one the
	// transcript keeps the full width.
	Sidebar bool
	// SidebarWidth is the sidebar's cell budget, excluding the one-cell
	// divider that separates it from the conversation.
	SidebarWidth int
	// MainWidth is the conversation column's cell budget, excluding the
	// divider. It is the terminal width when Sidebar is false.
	MainWidth int
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

// ComputeLayout derives the base geometry from state and terminal size.
//
// The conversation receives whatever the chrome does not claim; the composer
// stays visible; header and hint bar stay compact. Dialogs are bounded by the
// caller against ConversationHeight so they never exceed the viewport. The
// full-screen canvas applies the responsive workspace split on top of this
// base through ComputeWorkspaceLayout; output-first mode deliberately keeps
// the full width because the terminal owns its scrollback and no sidebar is
// painted there.
func ComputeLayout(s State) Layout {
	if s.Width <= 0 || s.Height <= 0 {
		return Layout{
			Mode:         LayoutWide,
			Width:        s.Width,
			Height:       s.Height,
			ContentWidth: 0, // zero means "do not wrap"
			MainWidth:    s.Width,
			ShowDetail:   true,
			ShowHints:    true,
			PaletteLines: -1, // uncapped: render everything
		}
	}
	mode := LayoutForMode(s.Width, s.Height)
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
	return computeLayoutWith(s, content, 0)
}

// Workspace split geometry. The right rail is a proportional column with a
// floor and ceiling so it stays readable on a laptop and never becomes a
// second conversation on an ultrawide terminal. A minimum conversation width
// keeps the split from crushing the transcript on the first wide tier.
const (
	sidebarPercent      = 28
	sidebarMinWidth     = 26
	sidebarMaxWidth     = 56
	sidebarMinMain      = 48
	sidebarMinHeight    = 12
	sidebarDividerWidth = 1
)

// ComputeWorkspaceLayout derives the full-screen canvas.
//
// The task rail is transient chrome, not the default canvas. It exists only
// while an open task has at least one open todo: wide terminals narrow the
// conversation and give it a fixed proportional rail, while narrower tiers
// reserve one sticky task row immediately below the title bar. An idle session,
// a question with no todos, and every terminal task keep the full-width
// transcript, so a completed projection settles into history. The task row and
// the sidebar are mutually exclusive: one fact has one home.
func ComputeWorkspaceLayout(s State) Layout {
	base := ComputeLayout(s)
	if s.Width <= 0 || s.Height <= 0 || base.Mode == LayoutMini {
		return base
	}
	task := activeTaskProjection(&s)
	if task == nil || !mainCanvasAvailable(s) {
		return base
	}
	if base.Mode == LayoutWide && s.Height >= sidebarMinHeight {
		sidebar := s.Width * sidebarPercent / 100
		if sidebar < sidebarMinWidth {
			sidebar = sidebarMinWidth
		}
		if sidebar > sidebarMaxWidth {
			sidebar = sidebarMaxWidth
		}
		main := s.Width - sidebar - sidebarDividerWidth
		if main >= sidebarMinMain {
			content := main - 2
			if content < 10 {
				content = 10
			}
			layout := computeLayoutWith(s, content, 0)
			layout.Sidebar = true
			layout.SidebarWidth = sidebar
			layout.MainWidth = main
			layout.TaskTracker = true
			return layout
		}
	}
	layout := ComputeLayout(s)
	layout.TaskLines = 1
	layout.TaskTracker = true
	layout.ConversationHeight--
	if layout.ConversationHeight < 1 {
		layout.ConversationHeight = 1
	}
	return layout
}

// computeLayoutWith is the shared geometry engine. content is the message wrap
// budget; taskLines is the sticky task row claimed above the transcript.
func computeLayoutWith(s State, content int, taskLines int) Layout {
	mode := LayoutForMode(s.Width, s.Height)
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
	if !canvasOwnedByModal(s) {
		hints = 1
	}
	if hints == 0 {
		// Modal or approval: the hint bar is gone (the dialog or overlay
		// owns the keys), so the status row holds the single context row
		// and keeps reserved == rendered.
		status = 1
	}
	// The live telemetry bar owns a row immediately above the composer only
	// while a turn is actually in flight. An idle frame releases the row
	// entirely: reserving it would leave a blank gap between the transcript
	// and the prompt, and the global footer already carries session telemetry.
	hidden := overlayHidesComposer(s)
	telemetry := 0
	if !hidden && liveTelemetryVisible(s) {
		telemetry = 1
	}
	composer, sugg := composerHeight(s, content, mode, hints, telemetry)
	// An open overlay hides the composer (and its hints row belongs to the
	// transcript chrome, not the overlay): reserving rows for regions that
	// never render would short the viewport and unpin the footer.
	// The overlay area fills the released rows instead. The quit popup is a
	// centered modal like the plane and support surfaces, so it hides the
	// composer the same way.
	if hidden {
		composer = 0
		sugg = 0
		telemetry = 0
	}
	chrome := header + taskLines + status + hints + telemetry + composer
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
		TaskLines:          taskLines,
		TelemetryLines:     telemetry,
		MainWidth:          s.Width,
		ComposerLines:      composer,
		StatusLines:        status,
		ShowDetail:         mode == LayoutWide || mode == LayoutMedium,
		ShowHints:          hints > 0,
		PaletteLines:       sugg,
	}
}

// canvasOwnedByModal reports whether a modal owns the main canvas. The
// workspace split and sticky task row are hidden then: the dialog needs the
// width and its own keys, and the sidebar would compete with the decision.
func canvasOwnedByModal(s State) bool {
	return s.Picker != nil || s.Plane != nil || s.SupportPanel != nil ||
		s.StatusModal != nil || s.UsageModal != nil || s.InfoModal != nil ||
		s.InformationModal != nil || s.SessionModal != nil || s.HelpModal != nil ||
		s.SettingsModal != nil ||
		s.ConnectModal != nil || s.ConnectKey != nil || s.Pending != nil ||
		s.QuitConfirm != nil || s.Transition != nil || s.UpdateRestart != nil ||
		s.PostUpdate != nil || s.Permissions != nil || s.Diff != nil
}

// mainCanvasAvailable reports whether the ordinary conversation canvas — not
// a modal — currently owns the screen.
func mainCanvasAvailable(s State) bool { return !canvasOwnedByModal(s) }

// overlayHidesComposer reports whether an overlay replaces the composer. The
// quit popup is a centered modal, so it hides the composer like the others;
// a tool approval keeps the composer echo underneath its own dialog, so it is
// deliberately absent here.
func overlayHidesComposer(s State) bool {
	return s.Picker != nil || s.Plane != nil || s.SupportPanel != nil ||
		s.StatusModal != nil || s.UsageModal != nil || s.InfoModal != nil ||
		s.InformationModal != nil || s.SessionModal != nil || s.HelpModal != nil ||
		s.SettingsModal != nil ||
		s.ConnectModal != nil || s.ConnectKey != nil || s.QuitConfirm != nil ||
		s.Transition != nil || s.UpdateRestart != nil || s.PostUpdate != nil ||
		s.Permissions != nil || s.Diff != nil
}

// composerHeight sizes the composer in rows: the two framing rules plus
// hard-wrapped input lines, inline palette suggestions, and the interaction
// state line. Bounded so a pasted novel cannot evict the conversation. It
// uses the same wrap as renderComposer so the reserved rows and the rendered
// rows agree. It also returns the suggestion rows, capped so the chrome
// leaves the transcript at least one row: suggestions yield first — rules,
// input, and state never shrink for them. The renderer truncates to the
// same count via Layout.PaletteLines.
func composerHeight(s State, contentWidth int, mode LayoutMode, hintRows, telemetryRows int) (lines, sugg int) {
	lines = 2 // the two framing rules
	inputRows := 1
	text := s.Input
	if text != "" {
		// Same sanitized text the renderer folds: pasted escapes must not
		// change the reserved row count behind the view's back. The wrap
		// budget subtracts the live mode prefix ("ask › " is wider than
		// "› "), matching composerTextWidthFor so reserved and rendered rows
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
		// The live telemetry row above the composer claims a cell only while
		// a turn runs before suggestions may; suggestions yield first so the
		// transcript and the composer keep their rows on a short screen.
		maxSugg := s.Height - 2 - 1 - hintRows - 2 - inputRows - stateRows - telemetryRows - 1
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
	if s.Pending != nil || s.QuitConfirm != nil || s.Transition != nil || s.UpdateRestart != nil || s.PostUpdate != nil {
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
