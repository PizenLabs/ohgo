package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Palette holds every named style the view uses.
//
// Colours are referenced through the palette rather than inline escape codes
// so a theme change lands in one place. The two constructors below are the
// only literals.
type Palette struct {
	User      lipgloss.Style
	UserBG    lipgloss.Style
	Assistant lipgloss.Style
	ToolName  lipgloss.Style
	ToolDone  lipgloss.Style
	ToolError lipgloss.Style
	ToolRun   lipgloss.Style
	Notice    lipgloss.Style
	Status    lipgloss.Style
	Prompt    lipgloss.Style
	Choice    lipgloss.Style
	Selected  lipgloss.Style
	Truncated lipgloss.Style
	Cursor    lipgloss.Style
	Queued    lipgloss.Style
	// Chip renders a collapsed paste pill in the composer, distinct from the
	// editable text around it without shouting.
	Chip lipgloss.Style
	// Mention renders a valid "@path" reference in the composer, so a file
	// the prompt will actually attach stands out from surrounding prose.
	Mention lipgloss.Style
	// Ghost renders the inline history completion after the cursor. It is
	// deliberately dimmer than muted text: a suggestion is never content and
	// must recede the moment the user keeps typing.
	Ghost lipgloss.Style
	// ShellPrompt renders the "!" escape marker, and ShellCommand the escaped
	// command text, so a direct shell block reads as distinct from a tool
	// (whose badge carries the tool's identity colour) and from the user's
	// own words.
	ShellPrompt  lipgloss.Style
	ShellCommand lipgloss.Style
	// Markdown styles.
	Code    lipgloss.Style
	Heading lipgloss.Style
	// Heading2 and Heading3 stratify the header hierarchy below Heading
	// (H1): H2 is sage, H3 muted sage, so depth reads without parsing.
	Heading2 lipgloss.Style
	Heading3 lipgloss.Style
	// Code token styles highlight fenced code lexically. Every token
	// carries the same background as Code so a highlighted line keeps one
	// uniform block; the foreground alone signals the token kind.
	CodeKeyword lipgloss.Style
	CodeString  lipgloss.Style
	CodeComment lipgloss.Style
	CodeNumber  lipgloss.Style
	Bold        lipgloss.Style
	Italic      lipgloss.Style
	Quote       lipgloss.Style
	Bullet      lipgloss.Style
	Muted       lipgloss.Style
	// DiffAdd styles lines an edit introduced, in diff previews (approval
	// cards and inline diffs).
	DiffAdd lipgloss.Style
	// DiffDelete styles lines an edit removed, in diff previews.
	DiffDelete lipgloss.Style
	// Cyan is a context colour for hunk headers and metadata.
	Cyan lipgloss.Style
	// Comment styles secondary/explanatory text.
	Comment lipgloss.Style
	// Separator draws full-width rules (header divider, composer frames) in
	// the dim slate chrome style, distinct from the muted body text.
	Separator lipgloss.Style
	// Recap renders the single completion line under a finished task block.
	// It is the quietest text in the transcript: italic, dimmer than metadata,
	// never a second answer, and never part of the block's copy payload.
	Recap lipgloss.Style
	// SupportHi highlights key entities inside the $sup answer viewport:
	// bright cyan for concepts/technologies, amber for dates/specs.
	// Main-chat markdown never uses them, so the overlay hierarchy stays
	// distinct without changing the transcript.
	SupportHi    lipgloss.Style
	SupportHiAlt lipgloss.Style
}

// DarkPalette is the default: muted surfaces with a single mint accent for
// the user's voice and focus, primary text for the assistant. Headers step
// down a deep-forest scale — bright emerald H1, sage H2, muted sage H3 —
// and code tokens reuse the same scale so prose and code read as one theme.
// The question keeps a faint background block instead of a label prefix so
// it reads apart from the answer.
//
// The chrome roles resolve through the semantic tokens in style.go: the
// prompt cursor and focus accent share ColorPrimary with the header brand,
// secondary context (status line, hints, labels) reads in ColorMuted, and
// full-width rules draw in the dim slate.
func DarkPalette() *Palette {
	return &Palette{
		User:         fg(darkAccent).Bold(true),
		UserBG:       fg(darkText).Background(lipgloss.Color(darkUserBg)),
		Assistant:    fg(ColorText),
		ToolName:     fg(darkTool).Bold(true),
		ToolDone:     fg(darkSuccess),
		ToolError:    fg(darkDanger),
		ToolRun:      fg(darkWarning),
		Notice:       fg(darkMuted).Italic(true),
		Status:       fg(ColorMuted),
		Prompt:       fg(ColorPrimary),
		Choice:       fg(darkText),
		Selected:     fg(darkAccent).Bold(true),
		Truncated:    fg(darkMuted),
		Cursor:       fg(ColorPrimary).Reverse(true),
		Queued:       fg(darkMuted).Italic(true),
		Chip:         fg(ColorAccent).Bold(true),
		Mention:      fg(ColorPrimary).Bold(true),
		Ghost:        fg(ColorRecap),
		ShellPrompt:  fg(ColorPrimary).Bold(true),
		ShellCommand: fg(darkText),
		Code:         fg(darkCodeFg).Background(lipgloss.Color(darkCodeBg)),
		Heading:      fg(darkH1).Bold(true),
		Heading2:     fg(darkH2).Bold(true),
		Heading3:     fg(darkH3).Bold(true),
		CodeKeyword:  fg(darkH1).Background(lipgloss.Color(darkCodeBg)).Bold(true),
		CodeString:   fg(darkCodeStr).Background(lipgloss.Color(darkCodeBg)),
		CodeComment:  fg(darkMuted).Background(lipgloss.Color(darkCodeBg)).Italic(true),
		CodeNumber:   fg(darkTool).Background(lipgloss.Color(darkCodeBg)),
		Bold:         lipgloss.NewStyle().Bold(true),
		Italic:       lipgloss.NewStyle().Italic(true),
		Quote:        fg(darkMuted).Italic(true),
		Bullet:       fg(darkAccent),
		Muted:        fg(ColorMuted),
		DiffAdd:      fg("#a6e3a1"),
		DiffDelete:   fg("#f38ba8"),
		Cyan:         fg("#89dceb"),
		Comment:      fg(ColorMuted).Italic(true),
		Separator:    fg(ColorDim),
		Recap:        fg(ColorRecap).Italic(true),
		SupportHi:    fg("87").Bold(true),
		SupportHiAlt: fg("220").Bold(true),
	}
}

// LightPalette reads on bright terminals: dark foregrounds, no heavy
// backgrounds that would clash with a light screen, the same semantics.
// The question keeps its faint background block here as well.
func LightPalette() *Palette {
	return &Palette{
		User:         fg(lightAccent).Bold(true),
		UserBG:       fg(lightText).Background(lipgloss.Color(lightUserBg)),
		Assistant:    fg(lightText),
		ToolName:     fg(lightTool).Bold(true),
		ToolDone:     fg(lightSuccess),
		ToolError:    fg(lightDanger),
		ToolRun:      fg(lightWarning),
		Notice:       fg(lightMuted).Italic(true),
		Status:       fg(lightMuted),
		Prompt:       fg(lightAccent),
		Choice:       fg(lightText),
		Selected:     fg(lightAccent).Bold(true),
		Truncated:    fg(lightMuted),
		Cursor:       fg(lightAccent).Reverse(true),
		Queued:       fg(lightMuted).Italic(true),
		Chip:         fg(chromeLightAccent).Bold(true),
		Mention:      fg(chromeLightPrimary).Bold(true),
		Ghost:        fg(chromeLightRecap),
		ShellPrompt:  fg(chromeLightPrimary).Bold(true),
		ShellCommand: fg(lightText),
		Code:         fg(lightCodeFg).Background(lipgloss.Color(lightCodeBg)),
		Heading:      fg(lightH1).Bold(true),
		Heading2:     fg(lightH2).Bold(true),
		Heading3:     fg(lightH3).Bold(true),
		CodeKeyword:  fg(lightH1).Background(lipgloss.Color(lightCodeBg)).Bold(true),
		CodeString:   fg(lightCodeStr).Background(lipgloss.Color(lightCodeBg)),
		CodeComment:  fg(lightMuted).Background(lipgloss.Color(lightCodeBg)).Italic(true),
		CodeNumber:   fg(lightTool).Background(lipgloss.Color(lightCodeBg)),
		Bold:         lipgloss.NewStyle().Bold(true),
		Italic:       lipgloss.NewStyle().Italic(true),
		Quote:        fg(lightMuted).Italic(true),
		Bullet:       fg(lightAccent),
		Muted:        fg(lightMuted),
		DiffAdd:      fg("#a6e3a1"),
		DiffDelete:   fg("#f38ba8"),
		Cyan:         fg("#89dceb"),
		Comment:      fg(lightMuted).Italic(true),
		Separator:    fg(lightMuted),
		Recap:        fg(chromeLightRecap).Italic(true),
		SupportHi:    fg("30").Bold(true),
		SupportHiAlt: fg("166").Bold(true),
	}
}

// PaletteFor resolves a theme name to its palette. Unknown names degrade to
// dark, matching NormalizeTheme: the view never fails to render.
func PaletteFor(theme string) *Palette {
	if NormalizeTheme(theme) == ThemeLight {
		return LightPalette()
	}
	return DarkPalette()
}

// spinnerFrames is the animation used for running tools.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Render produces the full view for a state.
//
// It is pure: the same state always renders the same string, which is what
// makes it testable even though the output is not asserted on by a test — the
// point is that nothing here touches a terminal, performs I/O, or mutates
// runtime state. Layout derives from WindowSizeMsg dimensions recorded in
// state; a resize re-renders without issuing requests.
func Render(state State) string {
	return renderState(state, nil)
}

// renderState produces the full frame. frame is an optional retained
// transcript projection; nil builds a one-shot layout, which is what Render
// does for tests and standalone callers.
func renderState(state State, frame *transcriptFrame) string {
	pal := PaletteFor(state.Theme)
	layout := ComputeLayout(state)
	if layout.Mode == LayoutMini && state.Width > 0 && state.Height > 0 {
		return renderMini(state, pal, layout)
	}

	var sb strings.Builder
	sb.WriteString(renderHeader(state, pal, layout) + "\n")
	sb.WriteString(SeparatorLine(separatorWidth(state, layout), pal) + "\n")

	if state.Transition != nil {
		renderTransitionArea(&sb, state, pal, layout)
	} else if state.QuitConfirm != nil {
		renderQuitConfirmArea(&sb, state, pal, layout)
	} else if state.Diff != nil {
		renderDiffArea(&sb, state, pal, layout)
	} else if state.Permissions != nil {
		renderPermissionsArea(&sb, state, pal, layout)
	} else if state.Plane != nil {
		renderPlaneArea(&sb, state, pal, layout)
	} else if state.SupportPanel != nil {
		renderSupportArea(&sb, state, pal, layout)
	} else if state.StatusModal != nil {
		renderStatusArea(&sb, state, pal, layout)
	} else if state.UsageModal != nil {
		renderUsageArea(&sb, state, pal, layout)
	} else if state.InfoModal != nil || state.InformationModal != nil {
		renderInfoArea(&sb, state, pal, layout)
	} else if state.SessionModal != nil {
		renderSessionArea(&sb, state, pal, layout)
	} else if state.HelpModal != nil {
		renderHelpArea(&sb, state, pal, layout)
	} else if state.ConnectModal != nil {
		renderConnectArea(&sb, state, pal, layout)
	} else if state.ConnectKey != nil {
		renderConnectKeyArea(&sb, state, pal, layout)
	} else if state.Picker != nil {
		renderPickerArea(&sb, state, pal, layout)
	} else {
		renderTranscriptArea(&sb, state, pal, layout, frame)
	}

	if state.Pending != nil {
		renderApproval(&sb, state.Pending, state.Theme, layout)
	}

	renderThinkingArea(&sb, state, pal, layout)

	if state.Picker == nil && state.Plane == nil && state.SupportPanel == nil && state.StatusModal == nil && state.UsageModal == nil && state.InfoModal == nil && state.InformationModal == nil && state.SessionModal == nil && state.HelpModal == nil && state.ConnectModal == nil && state.ConnectKey == nil && state.QuitConfirm == nil && state.Transition == nil && state.Permissions == nil && state.Diff == nil {
		// Main canvas. No middle status line while the hint bar owns the
		// row (ShowHints): the header, composer prefix, and hint bar
		// already own every fact it would repeat. A pending approval drops
		// the hint row (the dialog owns the keys), so the status row holds
		// its place there instead.
		if !layout.ShowHints {
			sb.WriteString(renderStatus(state, pal, layout))
			sb.WriteString("\n")
		}
		sb.WriteString(renderComposer(state, pal, layout))
		if layout.ShowHints {
			sb.WriteString("\n" + pal.Muted.Render(composerHintBar(state, displayWidth(state, layout))))
		}
	} else {
		// Modal overlays hide the composer (and the hint bar with it): the
		// status row stays as the single context row and the frame
		// terminator, and the overlay area fills the released composer rows.
		sb.WriteString(renderStatus(state, pal, layout))
		if state.SupportPanel != nil && state.Plane == nil && state.Picker == nil {
			// The support overlay owns interaction while open: the main
			// composer hides so a normal workspace turn cannot be submitted
			// underneath it.
			if layout.ShowHints {
				sb.WriteString("\n" + pal.Muted.Render("support owns input · Enter submit · Esc close"))
			}
		}
	}
	return sb.String()
}

// composerHintsFor picks the hint row for the current interaction: slash
// navigation while discovering commands, the busy set while a turn runs, and
// progressive disclosure while idle. Every hint shown must work.
//
// An open approval owns the interaction, so no composer hint is produced: the
// dialog has captured enter, esc, and the scroll keys, and advertising them
// here would contradict the key line it prints instead. ComputeLayout drops
// the row in the same case, so the frame keeps its budget either way.
//
// Progressive disclosure: a fresh session shows minimal path-only left with
// the full shortcut right ("? help · ^p settings"); the first executed prompt
// swaps the left for measured telemetry (↑in · ↓out (ctx%) · cost · path:branch)
// and narrows the right to "? help" so ^p never promises settings when it
// recalls history. The busy and slash-navigation rows stay authoritative while
// they apply — they name keys the user needs this instant, so telemetry never
// displaces them. This joined form is for tests and narrow fallbacks; the
// frame renders the padded split via composerHintBar.
func composerHintsFor(state State) string {
	if state.Pending != nil || state.QuitConfirm != nil || state.Transition != nil {
		return ""
	}
	if paletteOwnsKeys(state) && state.ConnectKey == nil {
		return "↑↓ navigate · enter/tab accept · esc dismiss"
	}
	if state.Status.Busy {
		// Busy keeps the live telemetry visible alongside the queue/cancel
		// keys: hiding ↑/↓/% until the turn ends is what left the
		// statusline stale for the whole streaming phase. The telemetry
		// already includes the in-flight output estimate (see
		// liveSessionOut/livePercent), so it updates on every AssistantDelta
		// without an extra event per chunk. The session marker still yields
		// while busy (see busyTelemetry), so the row never duplicates it.
		if telemetry := busyTelemetry(state); telemetry != "" {
			return telemetry + " · " + ComposerHints(true)
		}
		return ComposerHints(true)
	}
	left, right := idleHintZones(state)
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + " · " + right
}

// composerHintBar renders the same hint row as composerHintsFor but split
// into left/right zones pinned to the frame edges when width allows. Slash
// navigation and busy rows are transient and authoritative: they truncate to
// width but never split, so the key the user needs this instant stays intact.
// Idle telemetry splits via sessionBarRow: left metrics survive, right
// shortcuts yield first on narrowing, and the row never wraps.
func composerHintBar(state State, width int) string {
	if state.Pending != nil || state.QuitConfirm != nil || state.Transition != nil {
		return ""
	}
	if paletteOwnsKeys(state) && state.ConnectKey == nil {
		const hint = "↑↓ navigate · enter/tab accept · esc dismiss"
		if width > 0 {
			return TruncateToWidth(hint, width)
		}
		return hint
	}
	if state.Status.Busy {
		// Same live-telemetry contract as composerHintsFor, split into zones
		// when a width is known so the busy keys pin right and the measured
		// tokens survive narrowing. Truncation keeps the token-first head.
		// The session marker still yields while busy (busyTelemetry).
		if telemetry := busyTelemetry(state); telemetry != "" {
			busy := ComposerHints(true)
			if width <= 0 {
				return telemetry + " · " + busy
			}
			return sessionBarRow(telemetry, busy, width)
		}
		if width > 0 {
			return TruncateToWidth(ComposerHints(true), width)
		}
		return ComposerHints(true)
	}
	left, right := idleHintZones(state)
	return sessionBarRow(left, right, width)
}

// renderMini concedes a viewport too small for the workbench. It keeps the
// composer text and the model visible in prose so nothing is silently lost,
// and tells the user the useful action: enlarge the terminal.
func renderMini(state State, pal *Palette, layout Layout) string {
	// Every line fits the viewport: mini screens are where an overlong row
	// hurts most, so each line is truncated plain and styled after.
	fit := func(s string) string {
		if state.Width > 0 && CellWidth(s) > state.Width {
			return TruncateToWidth(s, state.Width)
		}
		return s
	}
	var sb strings.Builder
	sb.WriteString(pal.Heading.Render(fit("ohgo — terminal too small")) + "\n")
	sb.WriteString(pal.Muted.Render(fit(fmt.Sprintf("%dx%d: enlarge to at least %dx%d for the workbench.",
		state.Width, state.Height, MiniMinWidth, MiniMinHeight))) + "\n")
	if state.Status.Model != "" {
		sb.WriteString(pal.Status.Render(fit(sanitizeInline(state.Status.Model))) + "\n")
	}
	if strings.TrimSpace(state.Input) != "" {
		prefix := composerPrefix(state)
		prefixStyle := composerPrefixStyle(state, pal)
		budget := state.Width - CellWidth(prefix)
		if budget < 1 {
			budget = 1
		}
		sb.WriteString(prefixStyle.Render(prefix) + TruncateToWidth(sanitizeInline(strings.TrimSpace(state.Input)), budget) + "\n")
	}
	sb.WriteString(pal.Muted.Render(fit("your input and history are preserved.")) + "\n")
	return sb.String()
}

// Header width tiers. These key off the terminal width, not the layout tier:
// the header sheds detail earlier than the workbench drops columns, so a
// mid-size terminal keeps full conversation chrome with a compact header.
const (
	// headerFullMin is the first terminal width that shows the full model
	// id, the runtime phase, and the full connection detail.
	headerFullMin = 81
	// headerCompactMax is the last terminal width that shows connection
	// state. Below it the header is brand, model, and phase only.
	headerCompactMax = 54
)

// renderHeader draws product identity, the current model, the runtime phase,
// and the connection state. It is the only place identity and phase appear:
// the hint bar below carries session metrics and path, never a second copy.
//
// The line reads `ohgo v1.0.0 · model · phase · check`, each segment in its
// chrome role: the brand in primary, the release label muted beside it, the
// model in sage, an active phase in bold accent (idle in muted), the check
// status in amber, and every separator in dim slate. Phase and connection
// stay words, never colour alone. An unknown version hides rather than
// rendering as a placeholder, and the model budget shrinks to pay for the
// label so the header stays one row.
//
// Width sheds detail, never rows: full terminals show the whole model id and
// probe detail; medium terminals keep the state word and shorten the model;
// compact terminals drop the connection tag. The model shortens
// provider-prefix-first (openrouter/cohere/x becomes cohere/x) so the leaf
// id — the part that orients — survives longest; a hard cell truncation is
// the last resort. The composed plain line is budgeted before styling, so
// ANSI never inflates the measurement and the header is always one row.
func renderHeader(state State, pal *Palette, layout Layout) string {
	chrome := ChromeFor(state.Theme)
	runtime := DeriveRuntime(state)
	marker := RuntimeMarker(runtime)
	// Model ids and probe details arrive from catalogs and networks:
	// untrusted, sanitized inline. Effort arrives from configuration with
	// the same trust level, so it is sanitized the same way.
	modelRaw := sanitizeInline(state.Status.Model)
	effortRaw := sanitizeInline(state.Status.Effort)
	model := modelRaw
	if model == "" {
		model = "no model"
	}
	baseModel, effectiveEffort := EffectiveModelEffort(model, effortRaw)
	if modelRaw == "" {
		// No model means no badge: an effort without an engine orients
		// nowhere and would read as a claim about "no model".
		effectiveEffort = ""
		baseModel = model
	}
	// The release label arrives from build metadata: trusted but
	// normalized and sanitized like any other chrome text.
	version := sanitizeInline(VersionLabel(state.Status.Version))
	phase := string(runtime)
	tier := headerTier(state.Width)
	conn, showConn := headerConn(state, tier)
	width := displayWidth(state, layout)

	// A transient clipboard confirmation layers over the permanent header on
	// the far right. It reserves its own budget before the model truncates,
	// so the permanent segments keep their established shedding order and
	// the confirmation can never overlap them.
	notice := sanitizeInline(state.CopyNotice)
	reserved := 0
	if notice != "" {
		reserved = CellWidth(notice) + 2
	}

	// The model segment shares one budget with its effort badge: the badge
	// drops before the core id truncates, so narrowing sheds "◈ max" first
	// and only then shortens the leaf.
	modelShown, effortShown := FitHeaderModelWithEffort(baseModel, effectiveEffort, width-headerChromeOverhead(marker, phase, conn, showConn, version)-reserved)

	phaseStyle := chrome.Idle
	if headerRuntimeActive(runtime) {
		phaseStyle = chrome.Active
	}
	var sb strings.Builder
	sb.WriteString(chrome.Brand.Render("ohgo"))
	if version != "" {
		sb.WriteString(chrome.Version.Render(" " + version))
	}
	sb.WriteString(" ")
	sb.WriteString(phaseStyle.Render(marker))
	sb.WriteString(" ")
	sb.WriteString(chrome.Model.Render(modelShown))
	if effortShown != "" {
		// The badge reads in the check amber for the glyph and muted
		// context for the level, so it stays distinct from the sage model
		// without competing with the active phase.
		sb.WriteString(" ")
		sb.WriteString(chrome.Check.Render("◈"))
		sb.WriteString(" ")
		sb.WriteString(chrome.Context.Render(effortShown))
	}
	sb.WriteString(chrome.Separator.Render(" · "))
	sb.WriteString(phaseStyle.Render(phase))
	if showConn {
		sb.WriteString(chrome.Separator.Render(" · "))
		sb.WriteString(chrome.Check.Render(conn))
	}
	left := sb.String()
	if notice == "" {
		return left
	}
	noticeStyled := ExtrasFor(state.Theme).Success.Bold(true).Render(notice)
	if state.Width <= 0 {
		// Unknown size renders everything without slicing: join without
		// padding so no arbitrary width is invented.
		return left + "  " + noticeStyled
	}
	leftW := CellWidth(left)
	noticeW := CellWidth(notice)
	gap := width - leftW - noticeW
	if gap < 2 {
		// Unreachable on every supported width (the model budget above
		// guarantees at least the minimum gap, and Mini owns narrower
		// terminals), but never overlap or overflow if it ever happens:
		// truncate the plain left to make room rather than covering it.
		maxLeft := width - noticeW - 2
		if maxLeft < 0 {
			maxLeft = 0
		}
		left = TruncateToWidth(stripANSI(left), maxLeft)
		leftW = CellWidth(left)
		gap = width - leftW - noticeW
		if gap < 2 {
			gap = 2
		}
	}
	return left + strings.Repeat(" ", gap) + noticeStyled
}

// headerTier resolves the header density from the terminal width. Unknown
// (non-positive) widths render everything, matching the historical no-size
// behaviour.
func headerTier(width int) string {
	switch {
	case width <= 0 || width >= headerFullMin:
		return "full"
	case width <= headerCompactMax:
		return "compact"
	default:
		return "medium"
	}
}

// headerConn reports the connection text for a tier. Compact hides it: on a
// narrow terminal the phase and the leaf model id matter, the probe words
// do not. Full shows the probe detail; medium keeps the state word only.
func headerConn(state State, tier string) (string, bool) {
	if tier == "compact" {
		return "", false
	}
	if tier == "medium" {
		conn := "not checked"
		if state.ConnDetail != "" {
			if state.ConnOK {
				conn = "connected"
			} else {
				conn = "not connected"
			}
		}
		return conn, true
	}
	conn := "○ not checked"
	if state.ConnDetail != "" {
		detail := sanitizeInline(state.ConnDetail)
		if state.ConnOK {
			conn = "● " + detail
		} else {
			conn = "○ " + detail
		}
	}
	return conn, true
}

// headerChromeOverhead counts every header cell except the model itself, so
// the model receives exactly the remaining budget.
func headerChromeOverhead(marker, phase, conn string, showConn bool, version string) int {
	n := CellWidth("ohgo " + marker + " " + " · " + phase)
	if version != "" {
		n += CellWidth(" " + version)
	}
	if showConn {
		n += CellWidth(" · " + conn)
	}
	return n
}

// headerRuntimeActive reports the phase renders in the active style: anything
// busier than idle. Shutting down reads as idle chrome; the word itself
// carries the state.
func headerRuntimeActive(runtime RuntimeState) bool {
	switch runtime {
	case RuntimeIdle, RuntimeShuttingDown:
		return false
	default:
		return true
	}
}

// fitHeaderModel shortens a model id to a cell budget, provider prefix first:
// "openrouter/cohere/north" shortens to "cohere/north" before any character
// is cut, so the leaf id survives longest. A non-positive budget yields the
// empty string rather than an overflowing row.
func fitHeaderModel(model string, budget int) string {
	if budget <= 0 {
		return ""
	}
	if CellWidth(model) <= budget {
		return model
	}
	if short := compactModel(model); CellWidth(short) <= budget {
		return short
	}
	return TruncateToWidth(compactModel(model), budget)
}

// compactModel drops the provider prefix from a model id:
// "groq/deepseek-r1-distill" becomes "deepseek-r1-distill". Ids without a
// prefix return unchanged.
func compactModel(model string) string {
	if i := strings.Index(model, "/"); i >= 0 && i+1 < len(model) {
		return model[i+1:]
	}
	return model
}

// showWelcome reports whether the empty-conversation introduction belongs.
//
// It is derived, never stored: the welcome is chrome for "nothing has happened
// yet", so it appears exactly when no prompt has run and the transcript is
// empty, and vanishes the moment real content lands or an overlay owns the
// screen. SessionActive is the lifecycle gate: the first submitted prompt
// flips it, so the surface is gone even before the transcript renders. Because
// it is only ever rendered — never applied as an Entry — it cannot leak into
// the persisted session or reappear over a resumed transcript.
func showWelcome(state State) bool {
	return !state.SessionActive && len(state.Entries) == 0 && state.Picker == nil && state.ConnectKey == nil && state.Pending == nil && state.QuitConfirm == nil && state.Transition == nil && state.Permissions == nil && state.StatusModal == nil && state.UsageModal == nil && state.InfoModal == nil && state.InformationModal == nil && state.SessionModal == nil && state.HelpModal == nil && state.ConnectModal == nil && state.Diff == nil
}

// renderTranscriptBody renders every entry wrapped to the content width.
//
// The interactive path does not call it: it slices TranscriptLayout.Rows. This
// full projection is the reference for the layout-equivalence tests and the
// full-render comparison benchmark, which is exactly the old ownership model
// the retained viewport replaces.
func renderTranscriptBody(state State, pal *Palette, layout Layout) string {
	var body strings.Builder
	frame := spinnerFrame(state.Status.Spinner)
	for _, entry := range state.Entries {
		renderEntry(&body, entry, pal, layout, frame)
	}
	return body.String()
}

// renderWelcome draws the runtime identity surface for an empty
// conversation.
//
// It derives only the ephemeral facts from state: the local identity and the
// greeting. The model, workspace, working directory, git state, and readiness
// live in the top bar; repeating them here would double a fact the user can
// already see. It is chrome, never an Entry, so it vanishes the moment real
// content lands and never persists. Layout follows the responsive tier: wide
// and medium box the composition, narrow renders it borderless.
func renderWelcome(state State, pal *Palette, layout Layout) string {
	facts := FactsFromStatus(state)
	surface := RenderIdentitySurface(facts, state.Width)
	if state.Width > 0 {
		// The transcript area slices by lines; style after measuring so ANSI
		// never inflates the viewport budget. Keep it quiet: identity first,
		// decoration last.
		chrome := ChromeFor(state.Theme)
		var styled []string
		for _, line := range strings.Split(surface, "\n") {
			styled = append(styled, styleIdentityLine(line, pal, chrome))
		}
		return strings.Join(styled, "\n") + "\n"
	}
	return surface + "\n"
}

// styleIdentityLine keeps box borders dim and lets identity content read.
// Mode labels (ASK/BUILD/FUZZ) render in the accent badge style shared with
// the header and footer, so the authority reads as one family across the
// surface. Styling wraps measured cells, so widths never change.
func styleIdentityLine(line string, pal *Palette, chrome *Chrome) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return line
	}
	if strings.HasPrefix(trimmed, "┌") || strings.HasPrefix(trimmed, "└") ||
		strings.HasPrefix(trimmed, "─") {
		return pal.Separator.Render(line)
	}
	if greetingBrandIndex(line) >= 0 {
		return styleGreetingTitle(line, pal)
	}
	if strings.Contains(line, "│") {
		if styled, ok := styleBoxedModes(line, pal, chrome); ok {
			return styled
		}
		return pal.Muted.Render(line)
	}
	return styleModeBadges(line, pal, chrome)
}

// greetingBrandIndex locates the identity title brand when it leads the row:
// every cell before it is blank space or a box border, and the brand stands
// alone (followed by space or end of line). It reports -1 when OHGO appears
// mid-row — a workspace path, a model id — so those rows keep their own
// styling instead of borrowing the title's.
func greetingBrandIndex(line string) int {
	const brand = "OHGO"
	idx := strings.Index(line, brand)
	if idx < 0 {
		return -1
	}
	for _, r := range line[:idx] {
		if r != ' ' && r != '│' {
			return -1
		}
	}
	if tail := line[idx+len(brand):]; tail != "" && !strings.HasPrefix(tail, " ") && !strings.HasPrefix(tail, "│") {
		return -1
	}
	return idx
}

// styleGreetingTitle renders the identity title row: the OHGO brand keeps
// the heading style while the personal address ("Good evening, Toma.") reads
// in bold body text, so the greeting carries emphasis instead of blending
// into the brand — or, on the boxed surface, into the muted row. Borders
// stay muted, bare or boxed alike. Styling wraps measured cells, so the
// stripped row still equals the input.
func styleGreetingTitle(line string, pal *Palette) string {
	const brand = "OHGO"
	idx := greetingBrandIndex(line)
	if idx < 0 {
		return pal.Heading.Render(line)
	}
	return pal.Muted.Render(line[:idx]) + pal.Heading.Render(brand) + pal.Bold.Render(line[idx+len(brand):])
}

// modeLabels are the authority names that render as accent badges.
var modeLabels = map[string]bool{"ASK": true, "BUILD": true, "FUZZ": true}

// leadingMode reports the authority label heading a cell, if any. The label
// must be the whole first token: a description that merely starts with those
// letters never matches.
func leadingMode(cell string) (string, bool) {
	rest := strings.TrimLeft(cell, " ")
	for mode := range modeLabels {
		if rest == mode || strings.HasPrefix(rest, mode+" ") {
			return mode, true
		}
	}
	return "", false
}

// styleBoxedModes accents the mode labels inside a boxed row, leaving every
// other cell muted. It reports false when the row holds no label, so the
// caller falls back to the plain muted row.
func styleBoxedModes(line string, pal *Palette, chrome *Chrome) (string, bool) {
	segs := strings.Split(line, "│")
	if len(segs) < 3 {
		return "", false
	}
	holds := false
	for i := 1; i < len(segs)-1; i++ {
		if _, ok := leadingMode(segs[i]); ok {
			holds = true
		}
	}
	if !holds {
		return "", false
	}
	var sb strings.Builder
	sb.WriteString(pal.Muted.Render(segs[0] + "│"))
	for i := 1; i < len(segs)-1; i++ {
		sb.WriteString(styleModeCell(segs[i], pal, chrome))
		sb.WriteString(pal.Muted.Render("│"))
	}
	sb.WriteString(pal.Muted.Render(segs[len(segs)-1]))
	return sb.String(), true
}

// styleModeCell accents a leading authority label and mutes the rest of the
// cell. Cells without a label render muted wholesale.
func styleModeCell(cell string, pal *Palette, chrome *Chrome) string {
	mode, ok := leadingMode(cell)
	if !ok {
		return pal.Muted.Render(cell)
	}
	lead := cell[:len(cell)-len(strings.TrimLeft(cell, " "))]
	rest := strings.TrimLeft(cell, " ")[len(mode):]
	return pal.Muted.Render(lead) + chrome.Active.Render(mode) + pal.Muted.Render(rest)
}

// styleModeBadges accents a leading authority label on a borderless line.
func styleModeBadges(line string, pal *Palette, chrome *Chrome) string {
	mode, ok := leadingMode(line)
	if !ok {
		return line
	}
	rest := strings.TrimLeft(line, " ")[len(mode):]
	return chrome.Active.Render(mode) + pal.Muted.Render(rest)
}

// splitLines splits rendered output into lines without a trailing empty.
func splitLines(body string) []string {
	trimmed := strings.TrimRight(body, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// renderPickerArea shows the selection overlay in place of the transcript.
//
// The transcript hides while picking: an overlay that shared the screen with
// a streaming transcript would jump under the cursor with every event. The
// search row stays fixed while only the result window scrolls; the highlight
// is clamped into the window by EnsureVisible, so the marker is always
// rendered on a visible row, never outside the viewport.
//
// Like the transcript, the picker fills its budget exactly: short result
// lists pad below so the status and composer stay pinned, and overflow
// trims item rows while keeping the header, search, and footer visible.
func renderPickerArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	picker := state.Picker
	width := displayWidth(state, layout)
	header := pal.Heading.Render(TruncateToWidth(pickerHeader(picker), width))
	search := renderPickerSearch(picker, pal, width)
	footer := pal.Muted.Render(TruncateToWidth(pickerFooter(picker, layout), width))

	var middle []string
	filtered := picker.Filtered()
	window, start := picker.windowOf(filtered)
	if picker.Loading && len(window) == 0 {
		middle = append(middle, pal.Muted.Render("  loading catalog…"))
	} else if len(window) == 0 {
		middle = append(middle, pal.Muted.Render("  (no matches — esc clears the search)"))
	} else {
		for i, item := range window {
			row := renderPickerRow(start+i, picker, item, pal, layout, width)
			middle = append(middle, WrapToWidth(row, width)...)
		}
		if hidden := len(filtered) - start - len(window); hidden > 0 {
			middle = append(middle, pal.Muted.Render(fmt.Sprintf("  … %d more", hidden)))
		}
	}
	if picker.Kind == PickerModels {
		if status := pickerCatalogStatus(picker); status != "" {
			middle = append(middle, pal.Muted.Render(TruncateToWidth(status, width)))
		}
	}

	// Unknown size renders everything: only a known viewport may slice.
	if state.Height <= 0 || layout.Mode == LayoutWide && state.Width <= 0 {
		sb.WriteString(header + "\n")
		sb.WriteString(search + "\n")
		for _, line := range middle {
			sb.WriteString(line + "\n")
		}
		sb.WriteString(footer + "\n")
		return
	}
	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	// Header, search, and footer are never trimmed: overflow yields item
	// rows from the bottom, keeping navigation context on screen. Below a
	// zero budget even those overflow, but that corner needs a modal pileup
	// on a sub-10-row screen to exist at all.
	budget := avail - 3
	if budget < 0 {
		budget = 0
	}
	if len(middle) > budget {
		middle = middle[:budget]
	}
	sb.WriteString(header + "\n")
	sb.WriteString(search + "\n")
	for _, line := range middle {
		sb.WriteString(line + "\n")
	}
	for i := 3 + len(middle); i < avail; i++ {
		sb.WriteString("\n")
	}
	sb.WriteString(footer + "\n")
}

// pickerHeader names the overlay, the provider scope, the result count, and
// any loading state: "Models · All providers · 215 results".
func pickerHeader(picker *PickerState) string {
	scope := "All providers"
	if provider := picker.ProviderScope(); provider != "" {
		scope = "provider:" + sanitizeInline(provider)
	}
	count := len(picker.Filtered())
	results := "results"
	if count == 1 {
		results = "result"
	}
	title := picker.Title
	if title == "" {
		title = "select"
	}
	if picker.Kind == PickerModels {
		return fmt.Sprintf("%s · %s · %d %s", title, scope, count, results)
	}
	return fmt.Sprintf("%s · %d %s", title, count, results)
}

// renderPickerSearch draws the query with a text cursor. The cursor renders
// inside the query so spaces and mid-text edits stay visible; active scope
// filters show after the query without covering it.
func renderPickerSearch(picker *PickerState, pal *Palette, width int) string {
	label := "Search models… "
	if picker.Kind != PickerModels {
		label = "> "
	}
	runes := []rune(sanitizeInline(picker.Filter))
	cur := picker.FilterCursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(runes) {
		cur = len(runes)
	}
	query := string(runes[:cur]) + pal.Cursor.Render(" ") + string(runes[cur:])
	line := label + query
	if scope := picker.ProviderScope(); scope != "" && picker.Kind == PickerModels {
		line += "  " + pal.Muted.Render("[provider:"+TruncateToWidth(scope, 24)+"]")
	}
	return TruncateToWidth(line, width)
}

// renderPickerRow draws one result row: marker first so long ids truncate
// the text, never the selection indicator. The active model carries a ●
// alongside the highlight; pricing appears only when metadata backs it.
// index is the row's position in the filtered set: the marker lands exactly
// on the highlight, never on a scrolled-off row.
func renderPickerRow(index int, picker *PickerState, item PickerItem, pal *Palette, layout Layout, width int) string {
	// Picker rows replay catalogs, session titles, and job commands: all
	// external text, sanitized inline before measuring so a control
	// sequence can never split a row or move the cursor.
	item.Title = sanitizeInline(item.Title)
	item.Detail = sanitizeInline(item.Detail)
	item.Value = sanitizeInline(item.Value)
	item.Name = sanitizeInline(item.Name)
	item.Provider = sanitizeInline(item.Provider)
	marker := "  "
	if index == picker.Cursor {
		marker = pal.Selected.Render("> ")
	}
	active := ""
	if item.Active {
		active = "● "
	}
	if picker.Kind != PickerModels {
		title := TruncateToWidth(item.Title, width-4-len(active))
		line := marker + active + title
		if item.Detail != "" && layout.ShowDetail {
			if detail := TruncateToWidth(item.Detail, width-CellWidth(title)-8); detail != "" {
				line += "  " + pal.Muted.Render(detail)
			}
		}
		return line
	}
	name := item.Title
	if name == "" {
		name = item.Value
	}
	switch layout.Mode {
	case LayoutNarrow:
		// Name and selection state only; ids truncate hard.
		return marker + active + TruncateToWidth(name, width-4-len(active))
	case LayoutMedium:
		row := marker + active + TruncateToWidth(name, width-4-len(active)-20)
		if item.Provider != "" {
			row += "  " + pal.Muted.Render(TruncateToWidth(item.Provider, 16))
		}
		if item.Context > 0 {
			row += "  " + pal.Muted.Render(formatCount(item.Context))
		}
		return row
	default:
		row := marker + active + TruncateToWidth(name, width-4-len(active)-40)
		if item.Provider != "" {
			row += "  " + pal.Muted.Render(TruncateToWidth(item.Provider, 16))
		}
		if item.Context > 0 {
			row += "  " + pal.Muted.Render(formatCount(item.Context)+" ctx")
		}
		if item.PriceKnown && item.Free {
			row += "  " + pal.Muted.Render("free")
		}
		if item.Value != "" && item.Value != name {
			if id := TruncateToWidth(item.Value, 42); id != "" {
				row += "  " + pal.Muted.Render(id)
			}
		}
		return row
	}
}

// pickerCatalogStatus reports catalog freshness in words: loading, live,
// cached with staleness, or a failed refresh that kept cached rows.
func pickerCatalogStatus(picker *PickerState) string {
	if picker.Loading {
		return "loading catalog…"
	}
	var parts []string
	switch picker.Origin {
	case "live":
		parts = append(parts, "live catalog")
	case "cached":
		parts = append(parts, "cached catalog")
	case "bundled":
		parts = append(parts, "bundled catalog")
	}
	if picker.Refreshing {
		parts = append(parts, "refreshing…")
	}
	if picker.ErrMsg != "" {
		parts = append(parts, "refresh failed — showing cached ("+sanitizeInline(picker.ErrMsg)+")")
	}
	return strings.Join(parts, " · ")
}

// pickerFooter lists the shortcuts that actually work in the picker,
// shortened per tier so the footer never overflows a narrow viewport.
func pickerFooter(picker *PickerState, layout Layout) string {
	refresh := ""
	if picker.Kind == PickerModels {
		refresh = " · Ctrl+R refresh"
	}
	switch layout.Mode {
	case LayoutNarrow:
		return "↑↓ nav · Enter select · Esc close"
	case LayoutMedium:
		return "↑↓ navigate · PgUp/PgDn · Enter select · Esc close" + refresh
	default:
		if picker.Kind == PickerModels {
			return "↑↓ navigate · PgUp/PgDn scroll · Home/End jump · Enter select · Esc close · Ctrl+R refresh"
		}
		return "↑↓ navigate · PgUp/PgDn scroll · Home/End jump · Enter select · Esc close · type to filter"
	}
}

// composerPrefix reports the live input affordance for the current authority.
//
// It is the lowercase workspace label plus " > " ("ask > ", "build > ",
// "fuzz > "), falling back to "> " when no authority is set. Lowercase keeps
// the composer quiet next to the uppercase badge in the footer: the footer
// names the boundary, the composer shows where typing lands. The workspace
// label is producer-owned and already constrained to the three authorities,
// so lowercasing cannot invent a new mode.
func composerPrefix(state State) string {
	active := strings.TrimSpace(state.Status.Workspace)
	switch strings.ToUpper(active) {
	case "ASK", "BUILD", "FUZZ":
	default:
		return "> "
	}
	label := strings.ToLower(active)
	return label + " > "
}

// composerPrefixStyle resolves the affordance colour per authority: ASK in
// teal/primary, BUILD in blue/accent, FUZZ in magenta. ASK and BUILD resolve
// through the theme-aware chrome so bright terminals keep the same hues in
// their darkened forms; FUZZ uses the workspace magenta identity shared with
// the footer badge. Unknown authorities degrade to the prompt style rather
// than borrowing an authority's colour.
func composerPrefixStyle(state State, pal *Palette) lipgloss.Style {
	chrome := ChromeFor(state.Theme)
	switch strings.ToUpper(strings.TrimSpace(state.Status.Workspace)) {
	case "ASK":
		return chrome.Prompt
	case "BUILD":
		return chrome.Active
	case "FUZZ":
		if NormalizeTheme(state.Theme) == ThemeLight {
			return fg("164")
		}
		return fg("200")
	default:
		return pal.Prompt
	}
}

// composerContPrefix aligns wrapped composer rows under the input text: spaces
// exactly as wide as the live prefix, so a long line that folds keeps one
// left edge no matter which authority owns it.
func composerContPrefix(state State) string {
	w := CellWidth(composerPrefix(state))
	if w < 1 {
		w = 2
	}
	return strings.Repeat(" ", w)
}

// composerTextWidthFor is the cells available for composer text after the live
// prefix. It matches the budget the layout reserves so rendered rows and
// reserved rows agree when the prefix widens from "> " to "build > ".
func composerTextWidthFor(state State, layout Layout) int {
	width := layout.ContentWidth
	if width <= 0 {
		width = 80
	}
	if width < 3 {
		width = 3
	}
	pw := CellWidth(composerPrefix(state))
	if pw < 1 {
		pw = 2
	}
	if w := width - pw; w >= 1 {
		return w
	}
	return 1
}

// renderComposer draws the multiline input framed by two restrained
// horizontal rules, or the masked secret entry while one is open. Masking
// renders bullets and never the key: shoulder-surfing a setup screen must
// not leak the credential.
//
// The visual hierarchy is deliberate: editable text and cursor first, the
// interaction state second, contextual suggestions when relevant, hints
// only when space permits. The rules stay quiet when idle (muted) and turn
// to the focus accent while the composer owns the keyboard, so focus is
// unmistakable without permanent decoration.
//
// Input is hard-wrapped to the content budget cell-by-cell (never
// byte-sliced, and never folded on whitespace) so a long line stays within
// the viewport and the cursor lands exactly where typing goes. It is capped
// at the layout's own row budget from the tail, so a paste cannot evict the
// conversation.
func renderComposer(state State, pal *Palette, layout Layout) string {
	rule := composerRule(state, pal, layout)

	// Pasted input can carry terminal escapes: sanitize for display (cursor
	// mapping preserved) while the stored input stays raw for editing.
	// Layout reserves from the same sanitized text — see composerHeight.
	clean, cleanCursor := sanitizeWithCursor(state.Input, state.Cursor)
	rows, cursorRow, cursorRune, haveCursor := layoutComposer(clean, cleanCursor, composerTextWidthFor(state, layout))

	// The window follows the cursor, not the tail: with more display rows than
	// the cap, a fixed tail slice would scroll the caret off-screen the moment
	// Alt+Enter (or a fold) pushed it above the last maxComposerRows rows.
	shown, start := composerWindow(rows, cursorRow, state.ComposerScroll, maxComposerRows)
	ghost := state.suggestionTail()
	width := composerTextWidthFor(state, layout)

	var sb strings.Builder
	sb.WriteString(rule)
	// The palette sits strictly above the input line it completes: the rule
	// frames it, the input stays the live row below, and the closing rule
	// still bounds the composer.
	if suggest := renderPaletteSuggestions(state, layout); suggest != "" {
		sb.WriteString("\n" + suggest)
	}
	for i, row := range shown {
		text := row
		isCursorRow := haveCursor && start+i == cursorRow
		rowCursor := -1
		if isCursorRow {
			rowCursor = cursorRune
		}
		// "@" references are highlighted first, on the plain row, so the
		// scanner never has to step over another style's escapes; collapsed
		// paste pills are then styled in place. Each styling step remaps the
		// cursor for the escape runes it adds.
		text, rowCursor = highlightMentions(pal.Mention, text, rowCursor)
		text, rowCursor = renderPasteChips(state.Pastes, pal.Chip, text, rowCursor)
		if isCursorRow {
			text = insertAt(text, rowCursor, pal.Cursor.Render("▌"))
			text = appendGhost(text, ghost, width, pal.Ghost)
		}
		prefix := composerContPrefix(state)
		prefixStyle := composerPrefixStyle(state, pal)
		if i == 0 {
			prefix = composerPrefix(state)
		}
		sb.WriteString("\n" + prefixStyle.Render(prefix) + text)
	}
	if status := composerStateLine(state, pal, layout); status != "" {
		sb.WriteString("\n" + status)
	}
	sb.WriteString("\n" + rule)
	return sb.String()
}

// appendGhost paints the inline history completion immediately after the
// cursor. The suggestion is purely visual: it never enters the input buffer,
// the layout, or the submission text.
//
// The tail is truncated to the cells left on the cursor row instead of
// wrapping. Wrapping would add a display row the layout never reserved, and a
// ghost that shifts the composer on every keystroke is worse than one that
// disappears near the right edge.
func appendGhost(text, tail string, width int, style lipgloss.Style) string {
	if tail == "" {
		return text
	}
	avail := width - CellWidth(text)
	if avail < 1 {
		return text
	}
	tail = TruncateToWidth(tail, avail)
	if tail == "" {
		return text
	}
	return text + style.Render(tail)
}

// composerRuleStyle resolves the frame colour for the two lines around the
// prompt: the live workspace accent while composing, the dim slate while a
// modal state owns the keyboard. It shares its foreground with
// composerPrefixStyle so the frame and the "ask > "/"build > "/"fuzz > "
// text read as one boundary.
func composerRuleStyle(state State, pal *Palette) lipgloss.Style {
	if state.Picker != nil || state.ConnectKey != nil || state.Pending != nil || state.QuitConfirm != nil || state.Transition != nil || state.Permissions != nil || state.StatusModal != nil || state.UsageModal != nil || state.InfoModal != nil || state.InformationModal != nil || state.SessionModal != nil || state.HelpModal != nil || state.ConnectModal != nil || state.Diff != nil {
		return pal.Separator
	}
	return composerPrefixStyle(state, pal).Bold(true)
}

// composerRule draws one horizontal rule framing the composer in the live
// workspace accent, so the two lines around the prompt read as the same
// boundary as the "ask > "/"build > "/"fuzz > " prefix. Any overlay,
// approval, or secret entry quiets it to the dim slate so attention goes to
// the modal state.
func composerRule(state State, pal *Palette, layout Layout) string {
	width := separatorWidth(state, layout)
	if width < 1 {
		width = 1
	}
	line := strings.Repeat("─", width)
	return composerRuleStyle(state, pal).Render(line)
}

// renderPaletteSuggestions draws the inline palette for the trigger word at
// the cursor ("/" commands, "@" files, "$" support).
//
// It is built from the trigger's registry on every render, filters as the user
// types, and caps its rows so discovery never evicts the conversation. Rows
// render through the shared palette builder with a fixed name column and a
// rounded frame, so cycling the cursor or typing filter letters never shifts
// the layout. The window is trimmed to the reserved budget, and the count
// markers stay truthful about what is hidden; a negative budget means the size
// is unknown (a test, or a terminal that never reported it) and everything
// renders.
func renderPaletteSuggestions(state State, layout Layout) string {
	shown, start, cursor, above, below := paletteWindow(state)
	if len(shown) == 0 {
		return ""
	}
	matches := paletteEntriesFor(state)
	items := append([]paletteEntry(nil), shown...)
	hideAbove := above
	hideBelow := below
	if layout.PaletteLines >= 0 {
		budget := layout.PaletteLines
		// The box needs both borders plus at least one content row; below
		// that there is nothing to draw and the composer keeps its inputs.
		if budget < paletteBorderRows+1 {
			return ""
		}
		for {
			hideBelow = len(matches)-start-len(items) > 0
			if len(items)+paletteGroupHeaders(items)+one(hideAbove)+one(hideBelow)+paletteBorderRows <= budget {
				break
			}
			if len(items) > 0 {
				items = items[:len(items)-1]
				continue
			}
			// Markers alone overflow the budget: keep the counted
			// below-marker over the uncounted above-marker.
			hideAbove = false
		}
	}
	return renderPaletteBox(paletteRequest{
		shown:      items,
		cursor:     cursor,
		startIndex: start,
		total:      len(matches),
		above:      hideAbove,
		below:      hideBelow,
		headers:    true,
		width:      displayWidth(state, layout),
		exactRows:  layout.PaletteLines,
		styles:     paletteStylesFor(state.Theme),
	})
}

// one counts a marker row when its section shows.
func one(show bool) int {
	if show {
		return 1
	}
	return 0
}

// composerStateLine names queued prompts under the input: the count plus the
// head of the queue, truncated to one row. This is the queue's only live
// home — the transcript holds conversation history, never interaction
// state — so nothing appears twice and nothing floats mid-screen.
// Idleness stays quiet, and an approval modal owns its dialog, so both
// yield no line at all.
func composerStateLine(state State, pal *Palette, layout Layout) string {
	if state.Pending != nil || state.QuitConfirm != nil || state.Transition != nil {
		return ""
	}
	n := len(state.Queued)
	if n == 0 {
		return ""
	}
	label := "1 queued"
	if n > 1 {
		label = fmt.Sprintf("%d queued", n)
	}
	first := TruncateToWidth(strings.TrimSpace(sanitizeInline(state.Queued[0])), displayWidth(state, layout)-CellWidth(label)-4)
	if first == "" {
		return pal.Queued.Render(label)
	}
	return pal.Queued.Render(label + " · " + first)
}

// renderEntry writes one transcript entry, wrapped to the content width.
// frame is the current spinner frame, shared with the status line so a running
// tool and the footer animate in step.
//
// The question renders as a faint background block across the content width,
// with no label prefix: the surface alone separates it from the answer.
func renderEntry(sb *strings.Builder, entry Entry, pal *Palette, layout Layout, frame string) {
	switch entry.Kind {
	case EntryUser:
		renderUserBlock(sb, entry.UserText, pal, layout)
		sb.WriteString("\n")

	case EntryAssistant:
		if entry.AssistantText == "" {
			return
		}
		// The markdown layer owns wrapping: it measures plain cells first
		// and styles after, so one width calculation governs and ANSI
		// never inflates the budget. Never re-wrap styled output here.
		rendered := RenderMarkdownWrapped(entry.AssistantText, pal, textWidth(layout))
		for _, wl := range strings.Split(rendered, "\n") {
			sb.WriteString(wl + "\n")
		}
		sb.WriteString("\n")

	case EntryTool:
		if entry.Tool != nil {
			renderTool(sb, entry.Tool, pal, layout, frame)
		}

	case EntryShell:
		if entry.Shell != nil {
			renderShell(sb, entry.Shell, pal, layout)
		}

	case EntryNotice:
		for _, wl := range wrapPlain(entry.Notice, layout) {
			sb.WriteString(pal.Notice.Render(wl) + "\n")
		}
		sb.WriteString("\n")

	case EntryTask:
		for _, line := range taskLines(entry.Task, pal, layout) {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")

	case EntryRecap:
		// No separator row: this is the terminal footer of the task, so the
		// next prompt follows it directly. The footer owns at most two rows.
		for _, line := range recapLines(entry.Recap, textWidth(layout)) {
			sb.WriteString(pal.Recap.Render(line) + "\n")
		}

	case EntrySupport:
		renderSupportEntry(sb, entry, pal, layout)
	}
}

// renderSupportEntry renders one $sup exchange.
//
// The question carries a "$sup" marker and the answer renders through the
// markdown layer, so the ephemeral surface reads as distinct from the main
// conversation without depending on colour alone.
func renderSupportEntry(sb *strings.Builder, entry Entry, pal *Palette, layout Layout) {
	if entry.SupportIsQuestion {
		for _, line := range wrapPlain("$sup "+entry.SupportText, layout) {
			sb.WriteString(pal.Notice.Render(line) + "\n")
		}
		sb.WriteString("\n")
		return
	}
	rendered := RenderMarkdownWrapped(entry.SupportText, pal, textWidth(layout))
	for _, wl := range strings.Split(rendered, "\n") {
		sb.WriteString(wl + "\n")
	}
	sb.WriteString("\n")
}

// renderTool writes a tool call and, once finished, its outcome.
//
// The default projection is collapsed and compact: one badge line naming
// the invocation plus one ⎿ outcome line. The bounded body appears only
// when the block is expanded via Ctrl+O, so a long agent run stays
// scannable without losing the deeper content.
//
// States are text plus color: running spinner, ✓ success, ✗ failure, ·
// pending, so nothing depends on hue alone. Output is bounded and wrapped;
// large results collapse to the limit with a truncation marker rather than
// flooding the conversation.
func renderTool(sb *strings.Builder, tool *ToolEntry, pal *Palette, layout Layout, frame string) {
	// The badge names the invocation (tool plus its bounded arguments),
	// falling back to the bare tool name for blocks that predate it.
	// The invocation is truncated against the viewport minus the badge
	// chrome around it, so a long path can never soft-wrap the terminal.
	elapsed := tool.elapsed()
	invBudget := dialogWidth(layout)
	switch tool.Status {
	case ToolRunning:
		invBudget -= CellWidth("⠋  running")
	case ToolDone, ToolError:
		invBudget -= 4 + CellWidth(elapsed)
	default:
		invBudget -= 2
	}
	if invBudget < 8 {
		invBudget = 8
	}
	invocation := TruncateToWidth(sanitizeInline(toolInvocation(tool)), invBudget)
	var badge string
	switch tool.Status {
	case ToolRunning:
		badge = pal.ToolRun.Render(frame+" "+invocation) + "  " + pal.Status.Render("running")
	case ToolDone:
		badge = pal.ToolDone.Render("✓ "+invocation) + "  " + pal.Status.Render(elapsed)
	case ToolError:
		badge = pal.ToolError.Render("✗ "+invocation) + "  " + pal.Status.Render(elapsed)
	case ToolPending:
		badge = pal.ToolRun.Render("· " + invocation)
	}
	sb.WriteString(badge + "\n")

	// The compact outcome lands on one ⎿ line: the finished summary when
	// the executor provided one, else a bounded first-line fallback so a
	// single-line result stays visible even with its body collapsed.
	// The hint width is reserved up front so the whole row stays bounded.
	if tool.Status != ToolRunning {
		if outcome := collapsedOutcome(tool); outcome != "" {
			hint := ""
			if toolExpandable(tool) && !tool.Expanded {
				hint = "  (ctrl+o to expand)"
			}
			budget := dialogWidth(layout) - 4 - CellWidth(hint)
			if budget < 8 {
				budget = 8
			}
			sb.WriteString("  " + pal.Status.Render("⎿ "+TruncateToWidth(outcome, budget)))
			if hint != "" {
				sb.WriteString(pal.Muted.Render(hint))
			}
			sb.WriteString("\n")
		}
	}

	// Collapsed is the default: the body below only renders expanded.
	// Expansion is presentation-only — the same semantic Text, bounded by
	// the same truncation, wrapping, and sanitization as before.
	if !tool.Expanded || tool.Text == "" {
		// A finished block keeps its blank separator so sequential calls
		// stay distinct; a running one stays tight while it streams.
		if tool.Status == ToolDone || tool.Status == ToolError {
			sb.WriteString("\n")
		}
		return
	}
	body, truncated := TruncateForDisplay(tool.Text, toolOutputLimit)
	// Tool results carry progress bars, colors, and carriage returns from
	// the child process: keep the colors, neutralize the cursor control,
	// then wrap what the terminal will actually lay out.
	body = sanitizeBlock(body)
	width := textWidth(layout) - 4
	if width < 10 {
		width = 10
	}
	shown := 0
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		for _, wl := range WrapToWidth(line, width) {
			if shown >= maxToolLines(layout) {
				break
			}
			sb.WriteString("    " + wl + "\n")
			shown++
		}
		if shown >= maxToolLines(layout) {
			break
		}
	}
	if truncated || shown >= maxToolLines(layout) {
		sb.WriteString(pal.Truncated.Render("    … [truncated]") + "\n")
	}
	sb.WriteString("\n")
}

// toolInvocation names what the call does: the captured invocation when the
// block opened with one, else the bare tool name.
func toolInvocation(tool *ToolEntry) string {
	if tool.Invocation != "" {
		return tool.Invocation
	}
	if tool.Name != "" {
		return tool.Name
	}
	return "tool"
}

// collapsedOutcome is the one-line result shown with or without expansion.
// The executor's compact summary wins; without one, the first line of the
// outcome stands in so trivial results stay visible.
func collapsedOutcome(tool *ToolEntry) string {
	if tool.Summary != "" {
		return sanitizeInline(tool.Summary)
	}
	if strings.TrimSpace(tool.Text) == "" {
		return ""
	}
	first := tool.Text
	if i := strings.Index(first, "\n"); i >= 0 {
		first = first[:i]
	}
	return sanitizeInline(oneLine(first))
}

// toolExpandable reports whether expanding reveals more than the collapsed
// line already says: multi-line or truncated outcomes only. Trivial
// single-line results stay hint-free so sequential runs stay scannable.
func toolExpandable(tool *ToolEntry) bool {
	if tool.Text == "" {
		return false
	}
	if tool.Truncated {
		return true
	}
	return strings.Contains(strings.TrimRight(tool.Text, "\n"), "\n")
}

// toolOutputLimit bounds one tool's displayed outcome.
const toolOutputLimit = 4000

// shellMaxLines bounds the wrapped rows one "!" escape renders, so a long
// stream never rebuilds thousands of lines on every frame.
const shellMaxLines = 40

// maxToolLines bounds rendered tool rows per call so one result cannot flood
// the conversation. Wide shows more; narrow keeps the transcript scannable.
func maxToolLines(layout Layout) int {
	if layout.Mode == LayoutNarrow {
		return 12
	}
	return 24
}

// elapsed renders a duration compactly, omitting it entirely when the call has
// not started so a pending line is not padded with a zero.
func (t *ToolEntry) elapsed() string {
	if t.StartedAt.IsZero() {
		return ""
	}
	d := t.Duration().Round(time.Millisecond)
	if d <= 0 {
		return ""
	}
	return d.String()
}

// renderShell writes one "!" escape block: the echoed command, its streamed
// output, and a truthful exit line.
//
// The leading "!" carries ColorPrimary so the escape reads as a deliberate
// direct action, distinct from a tool badge and from the user's own words. The
// exit status is text plus colour (✓/✗), so a monochrome terminal still shows
// success or failure.
func renderShell(sb *strings.Builder, shell *ShellEntry, pal *Palette, layout Layout) {
	width := textWidth(layout)
	if width <= 0 {
		width = 80
	}
	// Truncate the plain command before styling so a cut keeps the marker and
	// command colours instead of stripping the whole row to fit.
	command := sanitizeInline(shell.Command)
	budget := width - CellWidth("! ")
	if budget < 1 {
		budget = 1
	}
	header := pal.ShellPrompt.Render("! ") + pal.ShellCommand.Render(TruncateToWidth(command, budget))
	sb.WriteString(header + "\n")

	if body := strings.TrimRight(shell.Output, "\n"); strings.TrimSpace(body) != "" {
		// Command output carries terminal colours and control bytes from the
		// child process: keep the colours, neutralise the cursor control,
		// then wrap what the terminal will actually lay out. Rendering is
		// bounded like a tool result, so a long stream can never rebuild
		// thousands of wrapped rows on every frame.
		shown := 0
		for _, line := range strings.Split(sanitizeBlock(body), "\n") {
			for _, wl := range WrapToWidth(line, width) {
				if shown >= shellMaxLines {
					break
				}
				sb.WriteString(wl + "\n")
				shown++
			}
			if shown >= shellMaxLines {
				break
			}
		}
		if shell.Truncated || shown >= shellMaxLines {
			sb.WriteString(pal.Truncated.Render("… [output truncated]") + "\n")
		}
	}

	switch shell.Status {
	case ShellRunning:
		sb.WriteString(pal.ToolRun.Render("● running…") + "\n")
	default:
		sb.WriteString(shellExitLine(shell, pal) + "\n")
	}
	sb.WriteString("\n")
}

// shellExitLine renders the terminal status row: a ✓/✗ marker, the exit code,
// and the measured elapsed time. A cancellation or start failure names its
// reason in place of a code.
func shellExitLine(shell *ShellEntry, pal *Palette) string {
	if shell.Err != "" {
		return pal.ToolError.Render("✗ " + sanitizeInline(shell.Err))
	}
	style := pal.ToolDone
	mark := "✓"
	if shell.ExitCode != 0 {
		style = pal.ToolError
		mark = "✗"
	}
	line := style.Render(fmt.Sprintf("%s exit %d", mark, shell.ExitCode))
	if elapsed := shellElapsed(shell); elapsed != "" {
		line += pal.Status.Render(" · " + elapsed)
	}
	return line
}

// shellElapsed renders the engine's measured duration compactly, omitting an
// unmeasured one rather than showing a zero.
func shellElapsed(shell *ShellEntry) string {
	if shell.Duration <= 0 {
		return ""
	}
	return shell.Duration.Round(time.Millisecond).String()
}

// quitCardWidth bounds the centered quit dialog: wide enough for the hint,
// narrow enough to read as a popup with margins on roomy terminals.
func quitCardWidth(termWidth int) int {
	const maxCard = 52
	const minCard = 30
	if termWidth <= 0 {
		return maxCard
	}
	w := termWidth - 8
	if w > maxCard {
		w = maxCard
	}
	if w < minCard {
		w = termWidth - 4
		if w < 10 {
			w = 10
		}
		if w > termWidth {
			w = termWidth
		}
	}
	return w
}

// quitCard builds the centered bordered confirmation: title, horizontal
// Yes/No buttons reusing the wizard badges, and a hint naming the left/right
// keys. The selected button is flanked by ◀ ▶ so the choice never depends on
// hue alone — a monochrome terminal still shows which option Enter confirms.
func quitCard(confirm *PendingApproval, cardW int) string {
	if confirm == nil {
		return ""
	}
	if cardW < 10 {
		cardW = 10
	}
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardTeal)).Bold(true).
		Render("Really quit?")
	question := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("Exit the session?")
	yesActive := confirm.Cursor == 0
	noActive := !yesActive
	arrow := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardTeal)).Bold(true)
	leftYes, rightYes := "  ", "  "
	leftNo, rightNo := "  ", "  "
	if yesActive {
		leftYes = arrow.Render("◀ ")
		rightYes = arrow.Render(" ▶")
	}
	if noActive {
		leftNo = arrow.Render("◀ ")
		rightNo = arrow.Render(" ▶")
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Top,
		leftYes, gitButton("Yes", yesActive), rightYes,
		"   ",
		leftNo, gitButton("No", noActive), rightNo,
	)
	hints := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("←→ move · enter confirm · y quit · n/esc stay")
	inner := lipgloss.JoinVertical(lipgloss.Left, title, question, "", buttons, "", hints)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(wizardTeal)).
		Background(lipgloss.Color(wizardBG)).
		Padding(1, 2).
		Width(cardW).
		Render(inner)
}

// transitionCardWidth bounds the centered transition dialog: wide enough for
// the hint, narrow enough to read as a popup with margins on roomy terminals.
func transitionCardWidth(termWidth int) int {
	const maxCard = 56
	const minCard = 32
	if termWidth <= 0 {
		return maxCard
	}
	w := termWidth - 8
	if w > maxCard {
		w = maxCard
	}
	if w < minCard {
		w = termWidth - 4
		if w < 12 {
			w = 12
		}
		if w > termWidth {
			w = termWidth
		}
	}
	return w
}

// transitionCard builds the centered workspace-transition dialog: the
// question, the reason, the two workspace choices, and the key line.
//
// Each choice carries its own workspace's semantic accent, so ASK, BUILD, and
// FUZZ keep the identity they have everywhere else in the UI. The selected
// choice is flanked by ◀ ▶, so the decision never depends on hue alone. The
// dialog contains no percentage: Strength is an evidence level, and the copy
// names it in words rather than implying a calibrated score (I12).
func transitionCard(prompt *TransitionPrompt, cardW int) string {
	if prompt == nil {
		return ""
	}
	if cardW < 12 {
		cardW = 12
	}
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(prompt.ProposedAccent)).Bold(true).
		Render(prompt.Question)
	reason := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render(prompt.Reason)
	arrow := lipgloss.NewStyle().
		Foreground(lipgloss.Color(prompt.ProposedAccent)).Bold(true)
	keepActive := prompt.Selection == 0
	switchActive := !keepActive
	leftKeep, rightKeep := "  ", "  "
	leftSwitch, rightSwitch := "  ", "  "
	if keepActive {
		leftKeep, rightKeep = arrow.Render("◀ "), arrow.Render(" ▶")
	}
	if switchActive {
		leftSwitch, rightSwitch = arrow.Render("◀ "), arrow.Render(" ▶")
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Top,
		leftKeep, transitionButton("Keep "+prompt.Current, prompt.CurrentAccent, keepActive), rightKeep,
		"   ",
		leftSwitch, transitionButton(prompt.Proposed, prompt.ProposedAccent, switchActive), rightSwitch,
	)
	hints := lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render("Enter to confirm · Esc keeps " + prompt.Current)
	inner := lipgloss.JoinVertical(lipgloss.Left, title, reason, "", buttons, "", hints)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(prompt.ProposedAccent)).
		Background(lipgloss.Color(wizardBG)).
		Padding(1, 2).
		Width(cardW).
		Render(inner)
}

// transitionButton renders one workspace choice: its own accent when
// highlighted, a quiet grey label otherwise.
func transitionButton(label, accent string, active bool) string {
	text := "[ " + label + " ]"
	if active {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(accent)).Bold(true).
			Render(text)
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(wizardGrey)).
		Render(text)
}

// renderTransitionArea draws the workspace-transition decision as a centered
// popup. Like the quit confirmation it owns the conversation area while open
// and returns it untouched on resolution, so the prompt reads as a decision
// surface rather than another transcript row. It fills its budget exactly, so
// reserved and rendered rows agree.
func renderTransitionArea(sb *strings.Builder, state State, _ *Palette, layout Layout) {
	prompt := state.Transition
	if prompt == nil {
		return
	}
	if state.Width <= 0 || state.Height <= 0 {
		card := transitionCard(prompt, transitionCardWidth(0))
		sb.WriteString(card)
		if !strings.HasSuffix(card, "\n") {
			sb.WriteString("\n")
		}
		return
	}
	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	width := state.Width
	if width <= 0 {
		width = 80
	}
	card := transitionCard(prompt, transitionCardWidth(width))
	placed := lipgloss.Place(width, avail, lipgloss.Center, lipgloss.Center, card)
	lines := strings.Split(placed, "\n")
	if len(lines) > avail {
		lines = lines[:avail]
	}
	for len(lines) < avail {
		lines = append(lines, strings.Repeat(" ", width))
	}
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
}

// renderQuitConfirmArea draws the /q exit-confirmation as a centered popup.
// The dialog owns the conversation area while open (like the model plane and
// support modals): the transcript hides behind it and returns untouched on
// dismiss, so the popup reads as a notification rather than another
// transcript row. It fills its budget exactly with blank padding around the
// card, so reserved and rendered rows always agree.
func renderQuitConfirmArea(sb *strings.Builder, state State, _ *Palette, layout Layout) {
	confirm := state.QuitConfirm
	if confirm == nil {
		return
	}
	if state.Width <= 0 || state.Height <= 0 {
		card := quitCard(confirm, quitCardWidth(0))
		sb.WriteString(card)
		if !strings.HasSuffix(card, "\n") {
			sb.WriteString("\n")
		}
		return
	}
	avail := layout.ConversationHeight
	if state.Pending != nil {
		avail -= approvalLines(state.Pending, layout)
	}
	if avail < 0 {
		avail = 0
	}
	width := state.Width
	if width <= 0 {
		width = 80
	}
	card := quitCard(confirm, quitCardWidth(width))
	placed := lipgloss.Place(width, avail, lipgloss.Center, lipgloss.Center, card)
	lines := strings.Split(placed, "\n")
	if len(lines) > avail {
		lines = lines[:avail]
	}
	for len(lines) < avail {
		lines = append(lines, strings.Repeat(" ", width))
	}
	for _, line := range lines {
		sb.WriteString(line + "\n")
	}
}

// renderThinkingArea writes the projected activity line, when one exists.
//
// The row is reserved by Layout.ThinkingLines, so the transcript budget already
// excludes it and this must emit exactly that many rows. It renders the pure
// projection and nothing else: it never mutates state, issues a command, or
// decides for itself whether the runtime is working. Waiting reads muted so the
// blocked-on-human state is visually distinct from active execution.
func renderThinkingArea(sb *strings.Builder, state State, pal *Palette, layout Layout) {
	if layout.ThinkingLines == 0 {
		return
	}
	ts := ProjectThinking(state)
	style := pal.ToolRun
	if ts.Phase == ThinkingWaiting {
		style = pal.Muted
	}
	// now is read once here, at the single render seam, and handed to the
	// otherwise clock-free renderer: elapsed derives from the activity's real
	// start time, never from a counter. The spinner index is the shimmer
	// frame; on terminals without TrueColor (or with reduced motion) the
	// plain projection renders instead, so the line degrades to the standard
	// palette without animation.
	now := time.Now()
	tick := state.Status.Spinner
	lines := RenderThinking(ts, now, displayWidth(state, layout))
	// The commit glyph is the shared spinner, so it needs the tick even on a
	// terminal that declines the TrueColor shimmer. The tick is pure and the
	// commit label never shimmers, so this cannot enable reduced motion.
	if shimmerSupported() || ts.Phase == ThinkingCommit {
		lines = RenderThinkingTick(ts, now, tick, displayWidth(state, layout))
	}
	for _, line := range lines {
		sb.WriteString(style.Render(line) + "\n")
	}
}

// renderStatus draws the contextual footer: working directory, session,
// branch plus compact worktree state, context budget, and measured usage.
// It serves modal chrome (overlays, approvals) and the setup wizard; the
// main idle canvas leaves this row out
// (header, composer prefix, and hint bar already own every fact it would
// repeat) and gives the row to the transcript instead.
//
// It deliberately does not repeat the identity, model, or runtime phase shown
// in the header — one fact, one home. Wide and medium tiers show the full
// context/session/cwd/usage set; narrow keeps cwd and session so the composer
// and conversation keep their rows, dropping the budget prefix and usage.
// Unknown values stay absent rather than rendering as zero, and usage text is
// shown verbatim from measured accounting — the view never labels an estimate
// as measured. Nothing wraps: the terminal owns wrapping, and a footer that
// wraps reads as two footers.
func renderStatus(state State, pal *Palette, layout Layout) string {
	status := state.Status
	parts := []string{}
	// Cwd and usage text arrive from the environment and measured
	// accounting: untrusted for layout purposes, sanitized inline so the
	// footer stays exactly one row (it is truncated, never wrapped).
	if layout.ShowDetail {
		if pct := livePercent(state); pct >= 0 {
			parts = append(parts, fmt.Sprintf("context %d%%", pct))
		}
		if status.Session != "" {
			parts = append(parts, status.Session)
		}
		if path := sessionPathBranchWidth(state, 24); path != "" {
			parts = append(parts, path)
		}
		if status.UsageText != "" {
			parts = append(parts, sanitizeInline(status.UsageText))
		}
	} else {
		if path := sessionPathBranchWidth(state, 16); path != "" {
			parts = append(parts, path)
		}
		if status.Session != "" {
			parts = append(parts, status.Session)
		}
	}
	// The runtime phase is named once, in the header, and the live detail
	// lives in the activity line; the footer stays context-only so a third
	// "working" copy never competes with them. SUP is a model role, not a
	// workspace: the footer keeps naming the workspace authority while
	// support is open. Support runtime state lives inside the overlay.
	out := strings.Join(parts, "  ·  ")
	if state.Width > 0 {
		out = TruncateToWidth(out, state.Width-1)
	}
	if status.Workspace == "" {
		return pal.Status.Render(out)
	}
	// The authority badge leads the footer, in its own semantic accent. Plain
	// cells are measured and truncated before styling, so ANSI never inflates
	// the one-row budget.
	badge := sanitizeInline(status.Workspace)
	const sep = "  ·  "
	if state.Width > 0 {
		restBudget := state.Width - 1 - CellWidth(badge) - CellWidth(sep)
		if restBudget < 0 {
			// Too narrow for both: the authority identity is the part that
			// must survive, so the context detail yields.
			return WorkspaceBadge(status.WorkspaceAccent, TruncateToWidth(badge, state.Width-1))
		}
		out = TruncateToWidth(out, restBudget)
	}
	styled := WorkspaceBadge(status.WorkspaceAccent, badge)
	if out == "" {
		return styled
	}
	return styled + pal.Status.Render(sep+out)
}

// shortenCwd keeps the tail of a long path: the leaf directory is what
// orients, not the $HOME prefix.
func shortenCwd(cwd string, max int) string {
	if CellWidth(cwd) <= max {
		return cwd
	}
	return "…/" + TruncateToWidth(lastTwo(cwd), max-3)
}

// lastTwo keeps the last two path segments.
func lastTwo(p string) string {
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return p
}

// textWidth is the wrap budget for message text.
func textWidth(layout Layout) int {
	if layout.ContentWidth > 0 {
		return layout.ContentWidth
	}
	return 80
}

// displayWidth is the budget for chrome lines (header, picker, composer).
func displayWidth(state State, layout Layout) int {
	if state.Width > 0 {
		return state.Width - 1
	}
	return 80
}

// dialogWidth bounds approval and tool preview lines inside the viewport.
func dialogWidth(layout Layout) int {
	if layout.ContentWidth > 0 {
		return layout.ContentWidth
	}
	return 80
}

// separatorWidth sizes the rule under the header without overflowing.
func separatorWidth(state State, layout Layout) int {
	if state.Width > 0 {
		w := state.Width - 1
		if w < 1 {
			w = 1
		}
		return w
	}
	return 40
}

// renderUserBlock writes the question as a faint background layer across
// the content width. Each wrapped line is padded to the full budget before
// styling so the background reads as one solid block rather than patchy
// per-word highlights. The stored text stays untouched; only this render
// carries the background.
func renderUserBlock(sb *strings.Builder, text string, pal *Palette, layout Layout) {
	width := textWidth(layout)
	for _, wl := range wrapPlain(text, layout) {
		padded := wl
		if width > 0 {
			if gap := width - CellWidth(wl); gap > 0 {
				padded += strings.Repeat(" ", gap)
			}
		}
		sb.WriteString(pal.UserBG.Render(padded) + "\n")
	}
}

// wrapPlain wraps unstyled transcript prose to the content width. Input is
// untrusted (prompts, notices, tool echoes), so it is sanitized first:
// control sequences that could move the terminal cursor must never reach
// measurement or output. Styled spans (markdown) are wrapped per line by
// the caller splitting on newlines; ANSI is stripped for measurement so
// styles never inflate the budget.
func wrapPlain(text string, layout Layout) []string {
	text = sanitizeBlock(text)
	width := textWidth(layout)
	if width <= 0 {
		return strings.Split(text, "\n")
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		out = append(out, WrapToWidth(line, width)...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// spinnerFrame returns the animation glyph for an index, normalised so a
// negative or oversized value can never index out of range: Render must not
// panic on any state a caller can build.
func spinnerFrame(index int) string {
	n := len(spinnerFrames)
	if n == 0 {
		return ""
	}
	index %= n
	if index < 0 {
		index += n
	}
	return spinnerFrames[index]
}

// AdvanceSpinner returns the next frame index.
func AdvanceSpinner(current, frames int) int {
	if frames <= 0 {
		return 0
	}
	return (current + 1) % frames
}
