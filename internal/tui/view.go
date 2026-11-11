package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/ohgo/internal/tui/design"
)

// Palette holds every named style the view uses.
//
// Colours are referenced through the palette rather than inline escape codes
// so a theme change lands in one place. The two constructors below are the
// only literals.
type Palette struct {
	// Theme is the effective appearance these styles were resolved for.
	// Components holding only a Palette can still resolve authority and modal
	// roles without threading a second theme argument.
	Theme string
	User  lipgloss.Style
	// UserBG is the one-row surface behind a selected list entry. It is not
	// painted behind a transcript prompt: prompt attribution is the name and
	// glyph, and the terminal background stays untouched.
	UserBG    lipgloss.Style
	Assistant lipgloss.Style
	ToolName  lipgloss.Style
	ToolDone  lipgloss.Style
	ToolError lipgloss.Style
	ToolRun   lipgloss.Style
	// Warning renders a non-fatal operational warning: a review that needs a
	// human decision, a budget nearing its limit, or an approval that requires
	// attention. It never replaces an authority accent.
	Warning lipgloss.Style
	Notice  lipgloss.Style
	Status  lipgloss.Style
	// Title is the High Focus chrome tier: application brand, greetings, and
	// dialog titles. Nothing that is merely content uses it, so a title always
	// outranks the body it introduces.
	Title lipgloss.Style
	// Heading is the document-structure accent: markdown headings and table
	// headers. It is High Focus as well, distinguished by hue rather than by a
	// separate focus tier, so a report reads as a structured document.
	Heading lipgloss.Style
	// Secondary is the Medium Focus support tier: descriptions, labels, and
	// explanatory text that accompanies a primary keyword.
	Secondary lipgloss.Style
	// Faint is the Low Focus tier: timings, shortcuts, and other supporting
	// metadata. It is dimmer than Muted by design and never carries an action
	// the user must be able to read.
	Faint lipgloss.Style
	// Thought renders a reasoning block's disclosure header. Amber marks the
	// internal thinking channel as inspectable telemetry: it is distinct from
	// both the answer and the operational warning state, which belongs to
	// actual degradation.
	Thought lipgloss.Style
	// ThoughtBody renders the expanded reasoning prose: the muted half of the
	// same amber family, italicised so a cognitive stop reads as an aside
	// rather than a second answer.
	ThoughtBody lipgloss.Style
	Prompt      lipgloss.Style
	Choice      lipgloss.Style
	Selected    lipgloss.Style
	Truncated   lipgloss.Style
	Cursor      lipgloss.Style
	Queued      lipgloss.Style
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
	Code lipgloss.Style
	// Code token styles highlight fenced code lexically. Every token
	// carries the same background as Code so a highlighted line keeps one
	// uniform block; the foreground alone signals the token kind.
	CodeKeyword lipgloss.Style
	CodeString  lipgloss.Style
	CodeComment lipgloss.Style
	CodeNumber  lipgloss.Style
	Bold        lipgloss.Style
	// Strong is the inline-emphasis accent: a lead-in of a paragraph or a
	// "Section:" prefix reads warmer and heavier than the explanation it
	// introduces, without becoming another heading hue.
	Strong lipgloss.Style
	// InlineCode is a technical artifact inside prose (a path, filename, or
	// config value): the subtle pill separates reference data from the
	// sentence without turning it into a fenced block.
	InlineCode lipgloss.Style
	Italic     lipgloss.Style
	Quote      lipgloss.Style
	Bullet     lipgloss.Style
	Muted      lipgloss.Style
	// DiffAdd styles lines an edit introduced, in diff previews (approval
	// cards and inline diffs). The soft tint is part of the style so an
	// approval row reads as an added line even when the terminal profile
	// drops bold or italics.
	DiffAdd lipgloss.Style
	// DiffDelete styles lines an edit removed, in diff previews.
	DiffDelete lipgloss.Style
	// DiffAddBG and DiffDeleteBG are the row tints used when syntax colour
	// must be layered over an added or deleted line.
	DiffAddBG    lipgloss.Color
	DiffDeleteBG lipgloss.Color
	// DiffContext dims unchanged context rows so changes read first.
	DiffContext lipgloss.Style
	// Cyan is the reference/context blue: hunk headers, diff paths, and the
	// structural phase span on a tool-run row.
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
	// reference blue for concepts/technologies, gold for dates/specs.
	// Main-chat markdown never uses them, so the overlay hierarchy stays
	// distinct without changing the transcript.
	SupportHi    lipgloss.Style
	SupportHiAlt lipgloss.Style
}

// DarkPalette is the Midnight Slate appearance: a cool near-black canvas, soft
// slate body text, and restrained accent families — violet for structure, gold
// for emphasis, blue for reference data, amber for reasoning. The neutral base
// carries no green cast, so an accent is always intentional.
func DarkPalette() *Palette {
	darkPaletteOnce.Do(func() {
		t := DarkTokens()
		darkPaletteCache = &Palette{
			Theme:        ThemeDark,
			User:         fg(t.User).Bold(true),
			UserBG:       fg(t.TextPrimary).Background(lipgloss.Color(t.SurfaceSelection)),
			Assistant:    fg(t.TextPrimary),
			ToolName:     fg(t.ToolName).Bold(true),
			ToolDone:     fg(t.Success),
			ToolError:    fg(t.Error),
			ToolRun:      fg(t.Running),
			Warning:      fg(t.Warning),
			Notice:       fg(t.TextMuted).Italic(true),
			Status:       fg(t.TextMuted),
			Title:        fg(t.TextTitle).Bold(true),
			Heading:      fg(t.Heading).Bold(true),
			Secondary:    fg(t.TextSecondary),
			Faint:        fg(t.TextFaint),
			Thought:      fg(t.ThoughtHeader).Bold(true),
			ThoughtBody:  fg(t.ThoughtBody).Italic(true),
			Prompt:       fg(t.Prompt),
			Choice:       fg(t.TextPrimary),
			Selected:     fg(t.User).Bold(true),
			Truncated:    fg(t.TextMuted),
			Cursor:       fg(t.Prompt).Reverse(true),
			Queued:       fg(t.TextMuted).Italic(true),
			Chip:         fg(t.Active).Bold(true),
			Mention:      fg(t.Brand).Bold(true),
			Ghost:        fg(t.BrandMute),
			ShellPrompt:  fg(t.Brand).Bold(true),
			ShellCommand: fg(t.TextPrimary),
			Code:         fg(t.CodeForeground).Background(lipgloss.Color(t.CodeBackground)),
			CodeKeyword:  fg(t.CodeKeyword).Background(lipgloss.Color(t.CodeBackground)).Bold(true),
			CodeString:   fg(t.CodeString).Background(lipgloss.Color(t.CodeBackground)),
			CodeComment:  fg(t.TextMuted).Background(lipgloss.Color(t.CodeBackground)).Italic(true),
			CodeNumber:   fg(t.ToolName).Background(lipgloss.Color(t.CodeBackground)),
			Bold:         lipgloss.NewStyle().Bold(true),
			Strong:       fg(t.Emphasis).Bold(true),
			InlineCode:   fg(t.InlineCodeForeground).Background(lipgloss.Color(t.InlineCodeBackground)),
			Italic:       fg(t.TextPrimary).Italic(true),
			Quote:        fg(t.Quote).Italic(true),
			Bullet:       fg(t.ListMarker),
			Muted:        fg(t.TextMuted),
			DiffAdd:      fg(t.DiffAddForeground).Background(lipgloss.Color(t.DiffAddBackground)),
			DiffDelete:   fg(t.DiffDeleteForeground).Background(lipgloss.Color(t.DiffDeleteBackground)),
			DiffAddBG:    lipgloss.Color(t.DiffAddBackground),
			DiffDeleteBG: lipgloss.Color(t.DiffDeleteBackground),
			DiffContext:  fg(t.DiffContext),
			Cyan:         fg(t.DiffHeader),
			Comment:      fg(t.TextMuted).Italic(true),
			Separator:    fg(t.BorderDefault),
			Recap:        fg(t.Recap).Italic(true),
			SupportHi:    fg(t.SupportPrimary).Bold(true),
			SupportHiAlt: fg(t.SupportSecondary).Bold(true),
		}
	})
	return darkPaletteCache
}

// LightPalette is the Warm Porcelain appearance: independent bright-surface
// values with the same semantic roles. It is not an inversion of Dark: the
// canvas is barely warm, the ink is cool, and every accent is deepened for a
// bright terminal.
func LightPalette() *Palette {
	lightPaletteOnce.Do(func() {
		t := LightTokens()
		lightPaletteCache = &Palette{
			Theme:        ThemeLight,
			User:         fg(t.User).Bold(true),
			UserBG:       fg(t.TextPrimary).Background(lipgloss.Color(t.SurfaceSelection)),
			Assistant:    fg(t.TextPrimary),
			ToolName:     fg(t.ToolName).Bold(true),
			ToolDone:     fg(t.Success),
			ToolError:    fg(t.Error),
			ToolRun:      fg(t.Running),
			Warning:      fg(t.Warning),
			Notice:       fg(t.TextMuted).Italic(true),
			Status:       fg(t.TextMuted),
			Title:        fg(t.TextTitle).Bold(true),
			Heading:      fg(t.Heading).Bold(true),
			Secondary:    fg(t.TextSecondary),
			Faint:        fg(t.TextFaint),
			Thought:      fg(t.ThoughtHeader).Bold(true),
			ThoughtBody:  fg(t.ThoughtBody).Italic(true),
			Prompt:       fg(t.Prompt),
			Choice:       fg(t.TextPrimary),
			Selected:     fg(t.User).Bold(true),
			Truncated:    fg(t.TextMuted),
			Cursor:       fg(t.Prompt).Reverse(true),
			Queued:       fg(t.TextMuted).Italic(true),
			Chip:         fg(t.Active).Bold(true),
			Mention:      fg(t.Brand).Bold(true),
			Ghost:        fg(t.BrandMute),
			ShellPrompt:  fg(t.Brand).Bold(true),
			ShellCommand: fg(t.TextPrimary),
			Code:         fg(t.CodeForeground).Background(lipgloss.Color(t.CodeBackground)),
			CodeKeyword:  fg(t.CodeKeyword).Background(lipgloss.Color(t.CodeBackground)).Bold(true),
			CodeString:   fg(t.CodeString).Background(lipgloss.Color(t.CodeBackground)),
			CodeComment:  fg(t.TextMuted).Background(lipgloss.Color(t.CodeBackground)).Italic(true),
			CodeNumber:   fg(t.ToolName).Background(lipgloss.Color(t.CodeBackground)),
			Bold:         lipgloss.NewStyle().Bold(true),
			Strong:       fg(t.Emphasis).Bold(true),
			InlineCode:   fg(t.InlineCodeForeground).Background(lipgloss.Color(t.InlineCodeBackground)),
			Italic:       fg(t.TextPrimary).Italic(true),
			Quote:        fg(t.Quote).Italic(true),
			Bullet:       fg(t.ListMarker),
			Muted:        fg(t.TextMuted),
			DiffAdd:      fg(t.DiffAddForeground).Background(lipgloss.Color(t.DiffAddBackground)),
			DiffDelete:   fg(t.DiffDeleteForeground).Background(lipgloss.Color(t.DiffDeleteBackground)),
			DiffAddBG:    lipgloss.Color(t.DiffAddBackground),
			DiffDeleteBG: lipgloss.Color(t.DiffDeleteBackground),
			DiffContext:  fg(t.DiffContext),
			Cyan:         fg(t.DiffHeader),
			Comment:      fg(t.TextMuted).Italic(true),
			Separator:    fg(t.BorderDefault),
			Recap:        fg(t.Recap).Italic(true),
			SupportHi:    fg(t.SupportPrimary).Bold(true),
			SupportHiAlt: fg(t.SupportSecondary).Bold(true),
		}
	})
	return lightPaletteCache
}

// PaletteFor resolves a theme name to its palette. Unknown names degrade to
// dark, matching NormalizeTheme: the view never fails to render.
func PaletteFor(theme string) *Palette {
	if NormalizeTheme(theme) == ThemeLight {
		return LightPalette()
	}
	return DarkPalette()
}

// spinnerFrames is the Braille rhythm shared by the activity line and a live
// reasoning trace. The shared tick advances one frame per spinnerInterval, so
// a burst of stream deltas cannot spin faster than the throttled frame clock.
// Settled transcript rows never draw these glyphs.
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
	layout := ComputeWorkspaceLayout(state)
	if layout.Mode == LayoutMini && state.Width > 0 && state.Height > 0 {
		return renderMini(state, pal, layout)
	}

	var body strings.Builder
	renderCanvasBody(&body, state, pal, layout, frame)

	var sb strings.Builder
	sb.WriteString(renderHeader(state, pal, layout) + "\n")
	sb.WriteString(SeparatorLine(separatorWidth(state, layout), pal) + "\n")

	if layout.Sidebar {
		// The split canvas composes the conversation column and the sidebar
		// at the row level. The sidebar is not a second viewport: it paints a
		// fixed rail beside the one transcript owner, so scrolling the
		// conversation can never move or repaint it.
		sidebar := renderWorkspaceSidebar(state, pal, layout)
		composeWorkspaceFrame(&sb, body.String(), sidebar, layout, pal)
		return sb.String()
	}
	if layout.TaskLines > 0 {
		// The pinned task area owns exactly the rows the layout claimed: the
		// projection block, its spacer, and blank padding if the projection
		// shrank between the layout pass and this render. The transcript below
		// it is the only scrolling surface.
		area := pinnedTaskArea(state, pal, layout, layout.TaskLines)
		for _, line := range area {
			sb.WriteString(line + "\n")
		}
		for i := len(area); i < layout.TaskLines; i++ {
			sb.WriteString("\n")
		}
	}
	sb.WriteString(body.String())
	return sb.String()
}

// renderCanvasBody writes the rows between the title bar and the frame edge:
// the transcript or overlay, the composer, and the stable hint/status row.
// Activity is never a separate canvas band: a tool block or a collapsed
// Thought row carries it inside the transcript.
func renderCanvasBody(sb *strings.Builder, state State, pal *Palette, layout Layout, frame *transcriptFrame) {
	if state.UpdateRestart != nil {
		renderUpdateRestartArea(sb, state, pal, layout)
	} else if state.PostUpdate != nil {
		renderPostUpdateArea(sb, state, pal, layout)
	} else if state.Transition != nil {
		renderTransitionArea(sb, state, pal, layout)
	} else if state.QuitConfirm != nil {
		renderQuitConfirmArea(sb, state, pal, layout)
	} else if state.Diff != nil {
		renderDiffArea(sb, state, pal, layout)
	} else if state.Permissions != nil {
		renderPermissionsArea(sb, state, pal, layout)
	} else if state.Plane != nil {
		renderPlaneArea(sb, state, pal, layout)
	} else if state.SupportPanel != nil {
		renderSupportArea(sb, state, pal, layout)
	} else if state.StatusModal != nil {
		renderStatusArea(sb, state, pal, layout)
	} else if state.UsageModal != nil {
		renderUsageArea(sb, state, pal, layout)
	} else if state.InfoModal != nil || state.InformationModal != nil {
		renderInfoArea(sb, state, pal, layout)
	} else if state.SessionModal != nil {
		renderSessionArea(sb, state, pal, layout)
	} else if state.HelpModal != nil {
		renderHelpArea(sb, state, pal, layout)
	} else if state.SettingsModal != nil {
		renderSettingsArea(sb, state, pal, layout)
	} else if state.ConnectModal != nil {
		renderConnectArea(sb, state, pal, layout)
	} else if state.ConnectKey != nil {
		renderConnectKeyArea(sb, state, pal, layout)
	} else if state.Picker != nil {
		renderPickerArea(sb, state, pal, layout)
	} else {
		renderTranscriptArea(sb, state, pal, layout, frame)
	}

	// A completed update or pending acknowledgement owns the surface until
	// the user answers it. A tool that asks for approval underneath is left
	// blocked: quitting denies it through the existing conservative path, and
	// the lifecycle card never renders a second, conflicting dialog.
	if state.Pending != nil && state.UpdateRestart == nil && state.PostUpdate == nil {
		renderApproval(sb, state.Pending, state.Theme, layout)
	}

	if !overlayHidesComposer(state) {
		// Main canvas. The composer is the last input surface. The telemetry
		// row above it exists only while a turn runs: when the turn settles
		// the row disappears, so the transcript sits directly above the
		// composer rather than paying for a blank or duplicate summary line.
		if bar := renderLiveTelemetryBar(state, pal, layout); bar != "" {
			sb.WriteString(bar)
			sb.WriteString("\n")
		}
		sb.WriteString(renderComposer(state, pal, layout))
		if layout.ShowHints {
			sb.WriteString("\n" + composerHintRow(state, pal, columnWidth(state, layout)))
		} else {
			sb.WriteString("\n" + renderStatus(state, pal, layout))
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
}

// composeWorkspaceFrame joins the conversation body and the sidebar row by
// row, separated by one vertical rule. Both columns are measured in cells, so
// an ANSI-styled row never shifts the divider. The body already claimed the
// exact height, which keeps the frame anchored to the viewport instead of
// floating when sidebar content is shorter.
func composeWorkspaceFrame(sb *strings.Builder, body, sidebar string, layout Layout, pal *Palette) {
	if layout.Height <= layout.HeaderLines {
		return
	}
	height := layout.Height - layout.HeaderLines
	left := splitLines(body)
	right := splitLines(sidebar)
	divider := pal.Separator.Render("│")
	for i := 0; i < height; i++ {
		leftLine := ""
		if i < len(left) {
			leftLine = left[i]
		}
		rightLine := ""
		if i < len(right) {
			rightLine = right[i]
		}
		sb.WriteString(padCells(TruncateToWidth(leftLine, layout.MainWidth), layout.MainWidth))
		sb.WriteString(divider)
		sb.WriteString(padCells(TruncateToWidth(rightLine, layout.SidebarWidth), layout.SidebarWidth))
		if i < height-1 {
			sb.WriteString("\n")
		}
	}
}

// composerHintZones reports the footer's two zones for the current
// interaction: the measured left zone and the action-key right zone.
//
// An open approval owns the interaction, so neither zone is produced: the
// dialog has captured enter, esc, and the scroll keys, and advertising them
// here would contradict the key line it prints instead. ComputeLayout drops
// the row in the same case, so the frame keeps its budget either way.
func composerHintZones(state State) (left, right string) {
	if state.Pending != nil || state.QuitConfirm != nil || state.Transition != nil || state.UpdateRestart != nil || state.PostUpdate != nil {
		return "", ""
	}
	if interruptConfirmVisible(state) {
		// The armed warning replaces the ordinary busy hints: the one key the
		// user needs this instant is the second Esc, not the queue chord.
		return interruptConfirmText, ""
	}
	if paletteOwnsKeys(state) && state.ConnectKey == nil {
		return "↑↓ navigate · enter/tab accept · esc dismiss", ""
	}
	if state.Status.Busy {
		// The busy footer keeps the turn timer and measured session telemetry
		// at the left and pins the queue/cancel keys to the right edge. Joined
		// into one sentence, the keys trailed the metrics and read as more
		// telemetry, so the row competed with the transcript instead of
		// reporting. The live phase lives in the telemetry bar above the
		// composer, so repeating it here would put one fact on two surfaces,
		// and nothing here changes the row count, so a hand-off can never move
		// the composer.
		return withTurnElapsed(state, busyTelemetry(state)), ComposerHints(true)
	}
	left, right = idleHintZones(state)
	return withTurnElapsed(state, left), right
}

// composerHintsFor joins the zones into the single row the live region and
// narrow fallbacks render. Every hint shown must work.
//
// Progressive disclosure: a fresh session shows minimal path-only left with
// the full shortcut right ("? help · ^p settings"); the first executed prompt
// swaps the left for measured telemetry (↑in · ↓out (ctx%) · cost · path:branch)
// and narrows the right to "? help" so ^p never promises settings when it
// recalls history. The measured turn timer leads that line once a prompt has
// run. The busy and slash-navigation rows stay authoritative while they apply.
func composerHintsFor(state State) string {
	left, right := composerHintZones(state)
	switch {
	case left == "":
		return right
	case right == "":
		return left
	}
	return left + " · " + right
}

// composerHintBar renders the zones padded to the frame edges when width
// allows. A row too narrow to split truncates instead of wrapping: the left
// zone's metric-first head survives over the shortcuts, and the key the user
// needs this instant stays intact while a warning or the command palette owns
// the row, because those own the whole line rather than a zone.
func composerHintBar(state State, width int) string {
	if width <= 0 {
		return composerHintsFor(state)
	}
	left, right := composerHintZones(state)
	return sessionBarRow(left, right, width)
}

// composerHintRow renders the footer in the two focus tiers it owns: the
// measured left zone in the readable muted role, and the action-key right zone
// in faint, where it stays available without competing with telemetry or the
// transcript. Position carries the de-emphasis as much as colour does — the
// right zone is pinned to the frame edge across the full row of padding — so
// the eye reaches the metrics first even on a terminal that renders only the
// colour distinction.
//
// A hazard warning is not metadata: it takes the warning role for the whole
// row so it cannot be mistaken for an ordinary hint.
func composerHintRow(state State, pal *Palette, width int) string {
	if interruptConfirmVisible(state) {
		return ExtrasFor(state.Theme).Warning.Render(composerHintBar(state, width))
	}
	left, right := composerHintZones(state)
	return sessionBarRow(pal.Muted.Render(left), pal.Faint.Render(right), width)
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
	sb.WriteString(pal.Title.Render(fit("ohgo — terminal too small")) + "\n")
	sb.WriteString(pal.Muted.Render(fit(fmt.Sprintf("%dx%d: enlarge to at least %dx%d for the workbench.",
		state.Width, state.Height, MiniMinWidth, MiniMinHeight))) + "\n")
	if state.Status.Model != "" {
		sb.WriteString(pal.Status.Render(fit(sanitizeInline(state.Status.Model))) + "\n")
	}
	if strings.TrimSpace(state.Input) != "" {
		prefix := composerPrefix(state)
		budget := state.Width - CellWidth(prefix)
		if budget < 1 {
			budget = 1
		}
		sb.WriteString(composerPrefixText(state, pal) + TruncateToWidth(sanitizeInline(strings.TrimSpace(state.Input)), budget) + "\n")
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

// renderHeader draws product identity, the current model, the resting runtime
// phase, and the connection state. It is the only place identity appears.
//
// Active work is deliberately absent here: a running tool entry, an open
// reasoning entry, the live footer's activity fallback, or the dialog that owns
// the screen already names it exactly once. The header keeps only idle and
// shutting down, the phases no other surface owns. The line reads `ohgo v1.0.0 ·
// model · phase · check` at rest and drops the phase segment while work is
// represented below.
// Each segment resolves through its chrome role: the brand in primary, the
// release label muted beside it, the blue-slate model, a resting phase in muted,
// the check status in amber, and every separator in dim slate. A known newer
// release appends an update segment last: availability, or the stage of an
// explicit /update. Phase and connection stay words, never colour alone. An
// unknown version hides rather than rendering as a placeholder, and the model
// budget shrinks to pay for the label so the header stays one row.
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
	runtime, showPhase := HeaderPhase(state)
	marker := ""
	phase := ""
	if showPhase {
		marker = RuntimeMarker(runtime)
		phase = string(runtime)
	}
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
	tier := headerTier(state.Width)
	conn, showConn := headerConn(state, tier)
	updateHint := headerUpdateHint(state, tier)
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
	modelShown, effortShown := FitHeaderModelWithEffort(baseModel, effectiveEffort, width-headerChromeOverhead(marker, phase, conn, showConn, version, updateHint)-reserved)

	// The header only ever names a resting phase (see HeaderPhase), so it
	// always uses the idle chrome role; activity surfaces carry the live accent.
	phaseStyle := chrome.Idle
	var sb strings.Builder
	sb.WriteString(chrome.Brand.Render("ohgo"))
	if version != "" {
		sb.WriteString(chrome.Version.Render(" " + version))
	}
	sb.WriteString(" ")
	if marker != "" {
		sb.WriteString(phaseStyle.Render(marker))
		sb.WriteString(" ")
	}
	sb.WriteString(chrome.Model.Render(modelShown))
	if effortShown != "" {
		// The badge reads in the check amber for the glyph and muted
		// context for the level, so it stays distinct from the blue-slate model
		// without competing with the active phase.
		sb.WriteString(" ")
		sb.WriteString(chrome.Check.Render("◈"))
		sb.WriteString(" ")
		sb.WriteString(chrome.Context.Render(effortShown))
	}
	if phase != "" {
		// The header names only a resting phase. While work is active, the one
		// activity surface below owns the word, so this segment stays absent.
		sb.WriteString(chrome.Separator.Render(" · "))
		sb.WriteString(phaseStyle.Render(phase))
	}
	if showConn {
		connStyle := chrome.Check
		if state.ConnDetail != "" {
			if state.ConnOK {
				connStyle = ExtrasFor(state.Theme).Success
			} else {
				connStyle = ExtrasFor(state.Theme).Danger
			}
		}
		sb.WriteString(chrome.Separator.Render(" · "))
		sb.WriteString(connStyle.Render(conn))
	}
	if updateHint != "" {
		// Update availability is chrome, not a second header row. It sits
		// last so the model budget pays for it and the permanent identity
		// keeps its place when the terminal narrows.
		sb.WriteString(chrome.Separator.Render(" · "))
		sb.WriteString(chrome.Context.Render(updateHint))
	}
	left := sb.String()
	if notice == "" {
		if state.Width > 0 && CellWidth(left) > width {
			// Last-resort guard: a pathological release tag must not wrap the
			// header into a second row, which would desynchronise the frame.
			return TruncateToWidth(stripANSI(left), width)
		}
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

// headerUpdateHint renders the update segment for the current header tier.
//
// It is plain text: the caller styles it, so the hint stays subordinate to
// the brand and the model. Availability and progress are mutually exclusive:
// an explicit /update replaces the invitation with the phase it is running.
// Compact terminals shed the invitation verb but keep the release identity,
// so availability is never communicated by colour or animation alone.
func headerUpdateHint(state State, tier string) string {
	if state.Status.UpdateRunning {
		stage := sanitizeInline(state.Status.UpdateStage)
		if tier == "compact" {
			return "updating"
		}
		if stage == "" {
			return "updating ohgo"
		}
		return "updating · " + stage
	}
	version := sanitizeInline(state.Status.UpdateAvailable)
	if version == "" {
		return ""
	}
	switch tier {
	case "compact":
		return "update " + version
	case "medium":
		return "update " + version + " · /update"
	default:
		return "update available: " + version + " · /update"
	}
}

// headerChromeOverhead counts every header cell except the model itself, so
// the model receives exactly the remaining budget.
func headerChromeOverhead(marker, phase, conn string, showConn bool, version, updateHint string) int {
	n := CellWidth("ohgo")
	if version != "" {
		n += CellWidth(" " + version)
	}
	if marker != "" {
		n += CellWidth(" " + marker)
	}
	if phase != "" {
		n += CellWidth(" · " + phase)
	}
	if showConn {
		n += CellWidth(" · " + conn)
	}
	if updateHint != "" {
		n += CellWidth(" · " + updateHint)
	}
	return n
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
	return !state.SessionActive && len(state.Entries) == 0 && state.Picker == nil && state.ConnectKey == nil && state.Pending == nil && state.QuitConfirm == nil && state.Transition == nil && state.Permissions == nil && state.StatusModal == nil && state.UsageModal == nil && state.InfoModal == nil && state.InformationModal == nil && state.SessionModal == nil && state.HelpModal == nil && state.SettingsModal == nil && state.ConnectModal == nil && state.Diff == nil
}

// renderTranscriptBody renders every entry wrapped to the content width.
//
// The interactive path does not call it: it slices TranscriptLayout.Rows. This
// full projection is the reference for the layout-equivalence tests and the
// full-render comparison benchmark, which is exactly the old ownership model
// the retained viewport replaces.
func renderTranscriptBody(state State, pal *Palette, layout Layout) string {
	var body strings.Builder
	current := latestRunningToolIndex(state)
	now := state.presentationAt()
	for i, entry := range state.Entries {
		groupActive := entry.Kind == EntryTool && entry.Tool != nil &&
			toolRunGroupActive(state, entry.Tool.run, now)
		renderEntryGrouped(&body, entry, pal, layout, entryRenderContext{
			current:         current == i,
			toolGroupEnd:    entryGroupEnd(state.Entries, i),
			leadingGap:      entryLeadingGap(state.Entries, i),
			toolGroupActive: groupActive,
			tick:            state.Status.Spinner,
			now:             now,
			theme:           state.Theme,
			user:            state.Status.User,
		})
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
// content lands and never persists. The panel spans the conversation column and
// folds its copy on narrow terminals; it drops the frame only below the
// minimum width that can draw one without breaking.
func renderWelcome(state State, pal *Palette, layout Layout) string {
	facts := FactsFromStatus(state)
	width := columnWidth(state, layout)
	surface := RenderIdentitySurface(facts, width)
	if state.Height > 0 {
		// A short transcript area shows the bottom of an overlong card, which
		// would hide the panel's own title. The compact composition keeps the
		// full grid (greeting, subtitle, modes) visible by dropping only
		// decorative blank rows; use it whenever it fits and the full form
		// does not.
		if budget := transcriptBudget(state, layout); budget > 0 && len(splitLines(surface)) > budget {
			if compact := renderIdentitySurface(facts, width, true); len(splitLines(compact)) <= budget {
				surface = compact
			}
		}
	}
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
// the header, so the authority reads as one family across the surface. Titles
// take the High Focus role, descriptions the Medium Focus role, and trigger
// hints the Low Focus role. Styling wraps measured cells, so widths never
// change.
func styleIdentityLine(line string, pal *Palette, chrome *Chrome) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return line
	}
	if strings.HasPrefix(trimmed, "┌") {
		return pal.Separator.Render(line)
	}
	if strings.HasPrefix(trimmed, "└") {
		return pal.Separator.Render(line)
	}
	if strings.Contains(line, "│") {
		return styleIdentityBoxRow(line, pal, chrome)
	}
	return styleIdentityCell(line, pal, chrome)
}

// styleIdentityBoxRow styles each boxed interior cell independently. The
// panel owns one outer frame; splitting on its borders keeps them in the
// separator role while each row's content resolves to its own focus tier.
func styleIdentityBoxRow(line string, pal *Palette, chrome *Chrome) string {
	segs := strings.Split(line, "│")
	var sb strings.Builder
	for i, seg := range segs {
		if i > 0 {
			sb.WriteString(pal.Separator.Render("│"))
		}
		if i == 0 || i == len(segs)-1 {
			sb.WriteString(pal.Separator.Render(seg))
			continue
		}
		sb.WriteString(styleIdentityCell(seg, pal, chrome))
	}
	return sb.String()
}

// styleIdentityCell resolves one semantic cell to its focus role. Leading and
// trailing padding is carried by the styled style itself, so stripping the
// result always returns the measured cells.
func styleIdentityCell(cell string, pal *Palette, chrome *Chrome) string {
	trimmed := strings.TrimSpace(cell)
	if trimmed == "" {
		return cell
	}
	if identityRuleCell(trimmed) {
		return pal.Separator.Render(cell)
	}
	if greetingBrandIndex(cell) >= 0 {
		return styleTitleText(cell, pal)
	}
	if strings.Contains(cell, identitySubtitle) {
		return pal.Muted.Render(cell)
	}
	if mode, ok := leadingMode(cell); ok {
		idx := strings.Index(cell, mode)
		if idx < 0 {
			return pal.Secondary.Render(cell)
		}
		return pal.Faint.Render(cell[:idx]) +
			modeStyle(chrome.Theme, mode, chrome.Active).Render(mode) +
			pal.Secondary.Render(cell[idx+len(mode):])
	}
	return pal.Secondary.Render(cell)
}

// identityRuleCell reports a cell that is only a horizontal stroke. The
// section divider keeps the low-focus separator role instead of falling
// through to the medium-focus body role.
func identityRuleCell(cell string) bool {
	if cell == "" {
		return false
	}
	for _, r := range cell {
		if r != '─' {
			return false
		}
	}
	return true
}

// greetingBrandIndex locates the identity title brand when it leads the row:
// every cell before it is blank space or a box border, and the brand stands
// alone (followed by space or end of line). It reports -1 when OHGO appears
// mid-row — a workspace path, a model id — so those rows keep their own
// styling instead of borrowing the title's.
func greetingBrandIndex(line string) int {
	idx := strings.Index(line, identityBrandText)
	if idx < 0 {
		return -1
	}
	for _, r := range line[:idx] {
		if r != ' ' && r != '│' {
			return -1
		}
	}
	if tail := line[idx+len(identityBrandText):]; tail != "" && !strings.HasPrefix(tail, " ") && !strings.HasPrefix(tail, "│") {
		return -1
	}
	return idx
}

// styleTitleText applies the title role to one bare cell. The brand and the
// address both take it; padding stays faint so the row does not glow.
func styleTitleText(cell string, pal *Palette) string {
	idx := greetingBrandIndex(cell)
	if idx < 0 {
		return pal.Title.Render(cell)
	}
	tail := cell[idx+len(identityBrandText):]
	if dot := strings.Index(tail, "·"); dot >= 0 {
		// The separator between brand and address is punctuation, not part
		// of either phrase: keep it low-focus so the greeting still reads as
		// one title rather than two competing labels.
		return pal.Faint.Render(cell[:idx]) +
			pal.Title.Render(identityBrandText) +
			pal.Faint.Render(tail[:dot]) +
			pal.Faint.Render("·") +
			pal.Title.Render(tail[dot+len("·"):])
	}
	return pal.Faint.Render(cell[:idx]) + pal.Title.Render(identityBrandText) + pal.Title.Render(tail)
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

// splitLines splits rendered output into lines without a trailing empty.
func splitLines(body string) []string {
	trimmed := strings.TrimRight(body, "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// mcpPickerWidth clamps the MCP manager card to a compact reading width.
func mcpPickerWidth(terminal int) int {
	width := terminal - 10
	if width > 64 {
		width = 64
	}
	if width > terminal-2 && terminal > 2 {
		width = terminal - 2
	}
	if width < 24 {
		width = 24
	}
	return width
}

// renderMCPPickerCard draws the MCP manager as a centered modal card.
//
// It reuses the shared card frame rather than a bespoke box: the border-embedded
// cyan title, interior padding, footer rule, and footer bar match the /usage and
// /status cards, so managing a short server list reads as one compact decision.
func renderMCPPickerCard(picker *PickerState, pal *Palette, layout Layout, width, height int) string {
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}

	var rows []string
	rows = append(rows, "")
	filtered := picker.Filtered()
	window, start := picker.windowOf(filtered)
	if len(window) == 0 {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+pal.Muted.Render("(no servers)"))
	} else {
		for i, item := range window {
			marker := "  "
			if start+i == picker.Cursor {
				marker = pal.Selected.Render("> ")
			}
			row := renderMCPPickerRow(marker, item, pal, interior-statusFieldIndent)
			rows = append(rows, strings.Repeat(" ", statusFieldIndent)+row)
		}
	}
	// The blank row above the footer rule gives the short list the same
	// breathing room as the inspector cards without adding a section header.
	rows = append(rows, "")
	// The title border, the footer rule, the footer bar, and the bottom
	// border claim four rows; everything else is body.
	if height > 0 {
		rows = fitStatusRows(rows, maxInt(height-4, 0))
	}

	footer := mcpPickerFooter(pal, picker, layout, interior)
	return renderModalFrameWithFooter(mcpManagerTitle(picker), rows, footer, pal, interior)
}

// mcpManagerTitle heads the manager with its status: "MCP · 2 servers". The
// empty state reads "MCP · no servers configured", matching the actionable
// picker contract.
func mcpManagerTitle(picker *PickerState) string {
	servers := mcpServerCount(picker)
	if servers == 0 {
		return "MCP · no servers configured"
	}
	status := fmt.Sprintf("%d servers", servers)
	if servers == 1 {
		status = "1 server"
	}
	if pending := picker.PendingCount(); pending > 0 {
		status += fmt.Sprintf(" · %d pending", pending)
	}
	return "MCP · " + status
}

// mcpServerCount counts real servers, excluding the empty state's add row.
func mcpServerCount(picker *PickerState) int {
	if picker == nil {
		return 0
	}
	count := 0
	for _, item := range picker.Items {
		if item.Value != MCPAddValue {
			count++
		}
	}
	return count
}

// mcpPickerFooter names the keys the manager actually handles. Keys are bold
// and their meanings muted, matching the shared modal footer bar; the plain
// text under the styles is the contract shown in the help and tests.
func mcpPickerFooter(pal *Palette, picker *PickerState, layout Layout, interior int) string {
	type binding struct{ key, label string }
	var bindings []binding
	narrow := layout.Mode == LayoutNarrow
	switch {
	case mcpServerCount(picker) == 0:
		// The empty state's footer is the documented actionable-picker
		// contract: one add row, navigate, select, close.
		bindings = []binding{{"↑↓", "navigate"}, {"Enter", "select"}, {"Esc", "close"}}
	case narrow:
		bindings = []binding{{"Space", "toggle"}, {"Enter", "apply"}, {"Esc", "cancel"}}
	default:
		bindings = []binding{
			{"↑↓", "navigate"},
			{"Space", "toggle"},
			{"Enter", "apply"},
			{"Esc", "cancel"},
		}
	}
	line := strings.Repeat(" ", statusFieldIndent)
	for i, binding := range bindings {
		if i > 0 {
			line += pal.Muted.Render(" · ")
		}
		line += pal.Bold.Render(binding.key) + pal.Muted.Render(" "+binding.label)
	}
	return TruncateToWidth(line, interior)
}

// newSessionPickerWidth clamps the /new creator to the same compact reading
// width as the MCP manager: two choices and one input line do not need a wide
// frame, and ultrawide terminals keep context on both sides.
func newSessionPickerWidth(terminal int) int {
	return mcpPickerWidth(terminal)
}

// renderNewSessionPickerCard draws the /new creator as a centered modal card.
//
// It deliberately shares the MCP manager's frame — border-embedded title,
// interior padding, footer rule, and structured key bar — so every small
// decision surface reads as one system. The list holds exactly the two title
// modes; the line below is the custom name, not a search query.
func renderNewSessionPickerCard(picker *PickerState, pal *Palette, layout Layout, width, height int) string {
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}

	rows := []string{""}
	filtered := picker.Filtered()
	for i, item := range filtered {
		marker := "  "
		if i == picker.Cursor {
			marker = pal.Selected.Render("> ")
		}
		title := sanitizeInline(item.Title)
		line := marker + title
		if detail := sanitizeInline(item.Detail); detail != "" && layout.ShowDetail {
			if room := interior - statusFieldIndent - CellWidth(line) - 2; room >= 8 {
				line += "  " + pal.Muted.Render(TruncateToWidth(detail, room))
			}
		}
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+TruncateToWidth(line, interior-statusFieldIndent))
	}
	rows = append(rows, "")

	// The input line is always present so the custom path is discoverable
	// without a second helper row. Its cursor follows the same rune offset the
	// picker uses for its filter text.
	buffer := []rune(sanitizeInline(picker.Filter))
	cur := picker.FilterCursor
	if cur < 0 {
		cur = 0
	}
	if cur > len(buffer) {
		cur = len(buffer)
	}
	typed := string(buffer[:cur]) + pal.Cursor.Render(" ") + string(buffer[cur:])
	name := strings.Repeat(" ", statusFieldIndent) +
		pal.Muted.Render(padModalLabel("Name", detailFieldLabelWidth)) +
		pal.Choice.Render(typed)
	rows = append(rows, TruncateToWidth(name, interior))

	// The title border, footer rule, footer bar, and bottom border claim four
	// rows; everything else is body.
	if height > 0 {
		rows = fitStatusRows(rows, maxInt(height-4, 0))
	}
	footer := newSessionPickerFooter(pal, layout, interior)
	return renderModalFrameWithFooter("NEW SESSION", rows, footer, pal, interior)
}

// newSessionPickerFooter names the keys the creator actually handles: the
// standard navigation and submit contract, with Esc cancelling the creation.
func newSessionPickerFooter(pal *Palette, layout Layout, interior int) string {
	type binding struct{ key, label string }
	bindings := []binding{{"Enter", "create"}, {"Esc", "cancel"}}
	if layout.Mode != LayoutNarrow {
		bindings = append([]binding{{"↑↓", "navigate"}}, bindings...)
	}
	line := strings.Repeat(" ", statusFieldIndent)
	for i, binding := range bindings {
		if i > 0 {
			line += pal.Muted.Render(" · ")
		}
		line += pal.Bold.Render(binding.key) + pal.Muted.Render(" "+binding.label)
	}
	return TruncateToWidth(line, interior)
}

// themePickerWidth keeps the appearance chooser compact: three choices and one
// sentence read best in a narrow decision card, not a full-width catalog.
func themePickerWidth(terminal int) int {
	return mcpPickerWidth(terminal)
}

// renderThemePickerCard draws the /theme chooser as a centered modal card.
//
// It deliberately shares the small-decision frame used by /new and the MCP
// manager: border-embedded title, a short list, a footer rule, and a key bar.
// The active palette carries both the selection marker and a text "current"
// badge, so the state never depends on color alone.
func renderThemePickerCard(picker *PickerState, pal *Palette, layout Layout, width, height int, summary string) string {
	interior := width - 2
	if interior < statusMinContent {
		interior = statusMinContent
	}

	rows := []string{""}
	const themeTitleWidth = 13
	for i, item := range picker.Filtered() {
		marker := "  "
		if i == picker.Cursor {
			marker = pal.Selected.Render("> ")
		}
		title := sanitizeInline(item.Title)
		line := marker + title
		// A fixed title column keeps the three details on one axis, so the
		// card scans as a comparison rather than three ragged sentences.
		if pad := themeTitleWidth - CellWidth(title); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		current := ""
		if item.Active {
			current = "  " + pal.ToolDone.Render("●")
		}
		if detail := sanitizeInline(item.Detail); detail != "" && layout.ShowDetail {
			room := interior - statusFieldIndent - CellWidth(line) - CellWidth(current) - 2
			if room >= 8 {
				line += pal.Muted.Render(TruncateToWidth(detail, room))
			}
		}
		line += current
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+TruncateToWidth(line, interior-statusFieldIndent))
	}
	rows = append(rows, "")
	if strings.TrimSpace(summary) != "" {
		rows = append(rows, strings.Repeat(" ", statusFieldIndent)+
			pal.Choice.Render(TruncateToWidth(summary, interior-statusFieldIndent)))
	}
	note := "Theme applies immediately and is saved for the next launch."
	rows = append(rows, strings.Repeat(" ", statusFieldIndent)+
		pal.Muted.Render(TruncateToWidth(note, interior-statusFieldIndent)))
	rows = append(rows, "")

	// The title border, footer rule, footer bar, and bottom border claim four
	// rows; everything else is body.
	if height > 0 {
		rows = fitStatusRows(rows, maxInt(height-4, 0))
	}
	footer := themePickerFooter(pal, layout, interior)
	return renderModalFrameWithFooter("THEME", rows, footer, pal, interior)
}

// themePickerFooter names the keys the chooser actually handles.
func themePickerFooter(pal *Palette, layout Layout, interior int) string {
	type binding struct{ key, label string }
	bindings := []binding{{"Enter", "apply"}, {"Esc", "close"}}
	if layout.Mode != LayoutNarrow {
		bindings = append([]binding{{"↑↓", "navigate"}}, bindings...)
	}
	line := strings.Repeat(" ", statusFieldIndent)
	for i, binding := range bindings {
		if i > 0 {
			line += pal.Muted.Render(" · ")
		}
		line += pal.Bold.Render(binding.key) + pal.Muted.Render(" "+binding.label)
	}
	return TruncateToWidth(line, interior)
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
	if picker.Kind == PickerThemes {
		renderCardAreaWidth(sb, state, layout, themePickerWidth, func(width, height int) string {
			return renderThemePickerCard(picker, pal, layout, width, height, themeSummaryFor(state))
		})
		return
	}
	if picker.Kind == PickerMCP {
		renderCardAreaWidth(sb, state, layout, mcpPickerWidth, func(width, height int) string {
			return renderMCPPickerCard(picker, pal, layout, width, height)
		})
		return
	}
	if picker.Kind == PickerNewSession {
		renderCardAreaWidth(sb, state, layout, newSessionPickerWidth, func(width, height int) string {
			return renderNewSessionPickerCard(picker, pal, layout, width, height)
		})
		return
	}
	width := displayWidth(state, layout)
	header := pal.Title.Render(TruncateToWidth(pickerHeader(picker), width))
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
	if picker.Kind == PickerMCP {
		servers := 0
		for _, item := range picker.Items {
			if item.Value != MCPAddValue {
				servers++
			}
		}
		if servers == 0 {
			return "MCP · no servers configured"
		}
		label := "servers"
		if servers == 1 {
			label = "server"
		}
		header := fmt.Sprintf("%s · %d %s", title, servers, label)
		if pending := picker.PendingCount(); pending > 0 {
			header += fmt.Sprintf(" · %d pending", pending)
		}
		return header
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
	if picker.Kind == PickerMCP {
		return renderMCPPickerRow(marker, item, pal, width)
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

// renderMCPPickerRow draws one MCP manager row: state, name, provenance, a
// known capability count, and a live-connection claim only when one exists.
// It never renders a command, URL, header, or secret because those are not in
// PickerItem's management fields at all. A bounded failure keeps the row marked
// with "!" even while the human stages a different desired state.
func renderMCPPickerRow(marker string, item PickerItem, pal *Palette, width int) string {
	if item.Value == MCPAddValue {
		title := TruncateToWidth(sanitizeInline(item.Title), maxInt(width-3, 1))
		return marker + pal.Muted.Render(title)
	}
	state := item.State
	if item.Pending {
		if item.Active {
			state = "enabled"
		} else {
			state = "disabled"
		}
	}
	glyph := "○"
	switch {
	case state == "error" || item.Error != "":
		glyph = "!"
	case state == "enabled":
		glyph = "●"
	}

	var detail []string
	if item.Trust != "" {
		detail = append(detail, sanitizeInline(item.Trust))
	}
	if item.Cached {
		detail = append(detail, mcpCapabilityLabel(item.Count))
	}
	if item.Connected && state != "error" && item.Error == "" {
		detail = append(detail, "connected")
	}
	if item.Error != "" {
		detail = append(detail, sanitizeInline(item.Error))
	} else if state == "error" {
		detail = append(detail, "connection failed")
	}
	if item.Pending {
		detail = append(detail, "pending")
	}

	nameW := maxInt(width-4, 1) // marker, glyph, and one separating cell
	text := marker + glyph + " " + TruncateToWidth(sanitizeInline(item.Title), nameW)
	if joined := strings.Join(detail, " · "); joined != "" {
		// Detail yields before the name and state marker do. A narrow card
		// still shows the row identity; the facts appear whenever they fit.
		if avail := width - CellWidth(text) - 2; avail >= 8 {
			text += "  " + pal.Muted.Render(TruncateToWidth(joined, avail))
		}
	}
	return text
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
	if picker.Kind == PickerMCP {
		empty := true
		for _, item := range picker.Items {
			if item.Value != MCPAddValue {
				empty = false
				break
			}
		}
		if empty {
			if layout.Mode == LayoutNarrow {
				return "Enter select · Esc close"
			}
			return "↑↓ navigate · Enter select · Esc close"
		}
		if layout.Mode == LayoutNarrow {
			return "Space toggle · Enter apply · Esc cancel"
		}
		return "↑↓ navigate · Space toggle · Enter apply · Esc cancel"
	}
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

// composerPointer is the refined input marker shared by the foreground composer
// and the compact surfaces. It replaces the ASCII ">" so the prompt reads as a
// quiet affordance rather than a shell redirection.
const composerPointer = "›"

// composerPrefix reports the live input affordance for the current authority.
//
// It is the lowercase workspace label plus " › " ("ask › ", "build › ",
// "fuzz › "), falling back to "› " when no authority is set. Lowercase keeps
// the composer quiet next to the uppercase badge in the footer: the footer
// names the boundary, the composer shows where typing lands. The workspace
// label is producer-owned and already constrained to the three authorities,
// so lowercasing cannot invent a new mode.
func composerPrefix(state State) string {
	active := strings.TrimSpace(state.Status.Workspace)
	switch strings.ToUpper(active) {
	case "ASK", "BUILD", "FUZZ":
	default:
		return composerPointer + " "
	}
	label := strings.ToLower(active)
	return label + " " + composerPointer + " "
}

// composerPrefixText renders the first-row prefix with hue identity confined to
// the authority word. The pointer and its surrounding space stay muted, so the
// mode is legible without turning the whole prompt into a colored run.
func composerPrefixText(state State, pal *Palette) string {
	active := strings.TrimSpace(state.Status.Workspace)
	switch strings.ToUpper(active) {
	case "ASK", "BUILD", "FUZZ":
	default:
		return pal.Muted.Render(composerPointer + " ")
	}
	word := strings.ToLower(active)
	return composerPrefixStyle(state, pal).Render(word) + pal.Muted.Render(" "+composerPointer+" ")
}

// composerPrefixStyle resolves the affordance colour per authority: ASK teal,
// BUILD blue, FUZZ violet. The hue is semantic identity; only its depth
// follows the theme, so the prompt stays soft on a dark screen and readable on
// a bright one without ever blurring which authority owns the next word.
// Unknown authorities degrade to the prompt style rather than borrowing an
// authority's colour.
func composerPrefixStyle(state State, pal *Palette) lipgloss.Style {
	return modeStyle(state.Theme, state.Status.Workspace, pal.Prompt)
}

// composerCursorStyle paints the caret in the live authority's accent. The
// caret is the strongest mode signal in the composer, so it follows the same
// semantic binding as the prefix. It stays a normal foreground instead of
// reverse video: an inverted block can disappear on a bright terminal.
// Unknown authorities keep the theme-aware cursor.
func composerCursorStyle(state State, pal *Palette) lipgloss.Style {
	if accent := ModeAccentFor(state.Theme, state.Status.Workspace); accent != "" {
		return fg(accent).Bold(true)
	}
	return pal.Cursor
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
// reserved rows agree when the prefix widens from "› " to "build › ".
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
// only when space permits. The framing rules stay on the neutral border token
// in every state; mode identity lives only in the prefix word, so the box
// never glows with a saturated hue while the user is typing.
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
			cursor := composerCursorStyle(state, pal).Render("▌")
			if hint := state.composerHint(); hint != "" {
				text = cursor + composerHintText(hint, width, pal.Faint)
			} else {
				text = insertAt(text, rowCursor, cursor)
				text = appendGhost(text, ghost, width, pal.Ghost)
			}
		}
		prefix := composerContPrefix(state)
		if i == 0 {
			prefix = composerPrefixText(state, pal)
		}
		sb.WriteString("\n" + prefix + text)
	}
	if status := composerStateLine(state, pal, layout); status != "" {
		sb.WriteString("\n" + status)
	}
	sb.WriteString("\n" + composerBottomRule(state, pal, layout))
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
// prompt. It is deliberately mode-independent: the frame is a neutral
// low-contrast boundary on every authority and in every modal state. Keeping
// the hue off the box prevents the composer from glowing while the literal
// mode word already names where typing lands.
func composerRuleStyle(_ State, pal *Palette) lipgloss.Style {
	return pal.Separator
}

// composerRule draws the upper rule framing the composer in the neutral
// border token, with the current session embedded as a border block header.
// The label names the sanitized title alone (the footer owns the slot), and a
// protected session carries the discrete ★ marker. The frame never takes the
// mode hue: only the prefix word carries authority identity.
func composerRule(state State, pal *Palette, layout Layout) string {
	width := composerRuleWidth(state, layout)
	return composerRuleStyle(state, pal).Render(composerBorderTitle(state, width))
}

// composerBottomRule draws the closing rule framing the composer. It stays
// plain: the header fact lives on the upper border, and repeating it below the
// input would duplicate the session identity inside one small region.
func composerBottomRule(state State, pal *Palette, layout Layout) string {
	width := composerRuleWidth(state, layout)
	return composerRuleStyle(state, pal).Render(strings.Repeat("─", width))
}

// composerRuleWidth is the cell budget both framing rules share. It follows
// the conversation column when the sidebar owns the right edge, so neither
// rule ever crosses the divider.
func composerRuleWidth(state State, layout Layout) int {
	width := separatorWidth(state, layout)
	if layout.Sidebar {
		width = layout.MainWidth - 1
	}
	if width < 1 {
		width = 1
	}
	return width
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
		width:      columnWidth(state, layout),
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
	if state.Pending != nil || state.QuitConfirm != nil || state.Transition != nil || state.UpdateRestart != nil || state.PostUpdate != nil {
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
	first := TruncateToWidth(strings.TrimSpace(sanitizeInline(state.Queued[0])), columnWidth(state, layout)-CellWidth(label)-4)
	if first == "" {
		return pal.Queued.Render(label)
	}
	return pal.Queued.Render(label + " · " + first)
}

// entryRenderContext carries the presentation state that is not part of the
// entry payload: geometry grouping, the one live activity, and the frame clock.
// Keeping it in one value means the retained layout and the reference
// projection cannot disagree about which phase to paint.
type entryRenderContext struct {
	current         bool
	toolGroupEnd    bool
	leadingGap      bool
	toolGroupActive bool
	tick            int
	now             time.Time
	theme           string
	// user attributes a prompt to the project-local worker. It is read from
	// runtime state, never from the entry, so a resumed transcript keeps the
	// worker name the current project resolved.
	user string
	// toolRows records which rendered line belongs to a tree node. The
	// layout copies it into LayoutRow so pointer inspection can target one
	// member without duplicating the renderer's branch-counting rules.
	toolRows *[]toolRunRow
	// currentToolID is the one running call that owns the live activity.
	// Concurrent rows stay truthful but muted so they do not compete.
	currentToolID string
}

// renderEntryGrouped writes one transcript entry, wrapped to the content width.
// current is true when this block is the one live activity the runtime owns.
// Only that block may show a live projection; every other row is stable.
//
// toolGroupEnd is true when this block closes the adjacent run of tool calls.
// A completed batch owns one telemetry footer instead of one row per call.
//
// leadingGap is true when this block follows a tool run. The gap belongs to
// the block after the run, never to the last tool: appending another tool must
// not rewrite the previous block's height. This is the same ownership rule the
// output-first live painter uses, so both surfaces converge.
//
// The question renders as a worker name and dim glyph followed by plain body
// text. No row is padded or painted, so the terminal background runs through
// the transcript and the reading flow stays uninterrupted.
func renderEntryGrouped(sb *strings.Builder, entry Entry, pal *Palette, layout Layout, ctx entryRenderContext) {
	if entryFading(entry, ctx.now) {
		age := ctx.now.Sub(entry.AppearedAt)
		if age < fadeFinalAt {
			var inner strings.Builder
			renderEntryContent(&inner, entry, pal, layout, ctx)
			sb.WriteString(fadeRenderedBlock(inner.String(), age, ctx.theme))
			return
		}
		// The final phase is the full semantic palette. It is still inside
		// the entrance window only so the block keeps ticking until it is
		// indistinguishable from settled history.
	}
	renderEntryContent(sb, entry, pal, layout, ctx)
}

func renderEntryContent(sb *strings.Builder, entry Entry, pal *Palette, layout Layout, ctx entryRenderContext) {
	writeGap := func() {
		if ctx.leadingGap {
			sb.WriteString("\n")
		}
	}
	switch entry.Kind {
	case EntryUser:
		writeGap()
		renderUserBlock(sb, entry.UserText, ctx.user, pal, layout)
		sb.WriteString("\n")

	case EntryAssistant:
		// A whitespace-only block renders no rows, so it must not claim the
		// gap that follows a tool run: the gap belongs to visible content.
		if strings.TrimSpace(entry.AssistantText) == "" {
			return
		}
		writeGap()
		// The markdown layer owns wrapping: it measures plain cells first
		// and styles after, so one width calculation governs and ANSI
		// never inflates the budget. Never re-wrap styled output here.
		rendered := RenderMarkdownWrapped(entry.AssistantText, pal, textWidth(layout))
		if !entryFading(entry, ctx.now) {
			if age, ok := assistantFinalLineAge(entry, ctx.now); ok && age < fadeFinalAt {
				rendered = fadeRenderedTail(rendered, age, ctx.theme)
			}
		}
		for _, wl := range strings.Split(rendered, "\n") {
			sb.WriteString(wl + "\n")
		}
		sb.WriteString("\n")

	case EntryReasoning:
		if entry.Reasoning != nil {
			writeGap()
			renderReasoningAt(sb, entry.Reasoning, pal, layout, ctx.tick, ctx.now)
		}

	case EntryTool:
		if entry.Tool != nil {
			if entry.Tool.run != nil && len(entry.Tool.run.members) > 1 && ctx.toolGroupActive {
				// One footer represents the batch; its members are not
				// repeated as independent rows. Only the anchor paints.
				if entry.Tool.run.anchor() == entry.Tool {
					renderToolRun(sb, entry.Tool.run, pal, layout, ctx.tick, ctx.currentToolID, ctx.theme, ctx.now, ctx.toolRows)
				}
				return
			}
			// A tool never owns a trailing separator: the block after the run
			// owns the gap, so appending another tool cannot move this one.
			renderToolAt(sb, entry.Tool, pal, layout, ctx.current, false, ctx.toolGroupEnd, ctx.tick, ctx.theme, ctx.now)
		}

	case EntryShell:
		if entry.Shell != nil {
			writeGap()
			renderShell(sb, entry.Shell, pal, layout)
		}

	case EntryNotice:
		writeGap()
		for _, wl := range wrapPlain(entry.Notice, layout) {
			sb.WriteString(pal.Notice.Render(wl) + "\n")
		}
		sb.WriteString("\n")

	case EntryTask:
		// The persistent tracker owns the task when one is on screen: the
		// sidebar carries the full list, and the sticky row carries the
		// compact header. Rendering the in-transcript projection as well
		// would print the same objective and todos twice.
		if layout.TaskTracker || layout.TaskLines > 0 {
			return
		}
		writeGap()
		for _, line := range taskLines(entry.Task, pal, layout) {
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")

	case EntryRecap:
		// No separator row: this is the terminal footer of the task, so the
		// next prompt follows it directly. The footer owns at most two rows.
		writeGap()
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

// Collapse markers for transcript blocks. A "+" invites the body open and a
// "-" shows it is already open; both are plain ASCII so a terminal without
// extended glyph coverage still reads the affordance.
const (
	blockCollapsedMarker = "+"
	blockExpandedMarker  = "-"
)

// renderReasoning projects one reasoning trace.
//
// The default is collapsed to the single subordinate row
// "+ Thought · 1.1s". The body is a real block payload, not text mined out of
// the answer: expansion reveals the provider's own thinking channel, bounded so
// one long trace cannot flood the transcript. The header carries no separate
// "ctrl+o" hint — the
// disclosure marker is the affordance, and repeating the key on every trace
// would duplicate both the marker's meaning and the help surface.
//
// The +/- markers are collapse markers, not liveness markers. A collapsed trace
// that is still growing uses the shared spinner so the row visibly moves; an
// expanded trace keeps the "-" marker and updates its measured duration in
// place. Reasoning is secondary information and never takes the status slot
// reserved for a tool action.
func renderReasoning(sb *strings.Builder, reasoning *ReasoningEntry, pal *Palette, layout Layout, tick int) {
	renderReasoningAt(sb, reasoning, pal, layout, tick, time.Now())
}

func renderReasoningAt(sb *strings.Builder, reasoning *ReasoningEntry, pal *Palette, layout Layout, tick int, now time.Time) {
	if reasoning == nil {
		return
	}
	marker := blockCollapsedMarker
	if reasoning.Expanded {
		marker = blockExpandedMarker
	}
	head := marker + " Thought"
	if !reasoning.Expanded && reasoning.reasoningPresentationOpen(now) && strings.TrimSpace(reasoning.Text) != "" {
		head = spinnerGlyph(tick) + " Thought"
	}
	if elapsed := reasoning.Duration(now); !reasoning.StartedAt.IsZero() &&
		(elapsed > 0 || reasoning.Closed || !reasoning.EndedAt.IsZero()) {
		head += " · " + formatDurationPrecise(elapsed)
	}
	sb.WriteString(pal.Thought.Render(head) + "\n")
	if !reasoning.Expanded {
		sb.WriteString("\n")
		return
	}
	width := textWidth(layout) - 2
	if width < 10 {
		width = 10
	}
	lines := wrapReasoningBody(reasoning.Text, width)
	limit := maxReasoningLines(layout)
	shown := 0
	// The guide is two cells wide, exactly the body indent budget, so the
	// expanded trace reads as a child of the header without changing the
	// wrapped width. It is a dim rule role, not prose styling.
	guide := pal.Separator.Render("│ ")
	if !reasoningBodyHasText(lines) {
		// The disclosure state is real even when the provider sent no prose:
		// duration-only or whitespace-only traces must stay inspectable rather
		// than silently collapsing to a row that cannot be opened.
		sb.WriteString(guide + pal.ThoughtBody.Render("(no detailed reasoning text provided)") + "\n")
		sb.WriteString("\n")
		return
	}
	for _, line := range lines {
		if shown >= limit {
			break
		}
		// Thinking prose is the muted half of the cognitive amber: available
		// on request, orphaned from the answer by italics, and never a second
		// answer competing with the transcript.
		sb.WriteString(guide + pal.ThoughtBody.Render(line) + "\n")
		shown++
	}
	if len(lines) > shown {
		sb.WriteString(guide + pal.Truncated.Render("… [truncated]") + "\n")
	}
	sb.WriteString("\n")
}

// wrapReasoningBody sanitizes and hard-wraps the thinking channel to the
// indented body width. It stays plain text on purpose: the body is a quiet
// annotation, and reusing the markdown layer would make it read like an answer.
func wrapReasoningBody(text string, width int) []string {
	text = sanitizeBlock(text)
	var out []string
	for _, line := range strings.Split(text, "\n") {
		out = append(out, WrapToWidth(line, width)...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// reasoningBodyHasText reports whether a sanitized trace has visible prose.
//
// Whitespace-only thinking is a real provider outcome, not a malformed block:
// the disclosure state still exists, so the renderer must show a deliberate
// placeholder rather than an empty body.
func reasoningBodyHasText(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

// maxReasoningLines bounds the expanded trace so one long reasoning run cannot
// displace the answer it produced. It matches the tool-body budget.
func maxReasoningLines(layout Layout) int {
	if layout.Mode == LayoutNarrow {
		return 12
	}
	return 24
}

// toolRunRow records which visual row belongs to a tree node. The transcript
// layout carries it so a click can target a member body without inventing a
// second geometry model.
type toolRunRow struct {
	line int
	id   string
}

// toolCountLabel keeps the footer grammatical without a second formatter.
func toolCountLabel(n int) string {
	if n == 1 {
		return "1 tool"
	}
	return fmt.Sprintf("%d tools", n)
}

// renderToolRun renders one completed adjacent batch as a compact telemetry
// tree.
//
// Collapsed is the default: one row carries the phase span, tool count, and
// measured wall span. Expansion keeps every call reachable as an indented
// branch with its own status glyph, invocation, measured duration, and compact
// outcome. A node body is a third layer, opened independently.
func renderToolRun(sb *strings.Builder, run *toolRun, pal *Palette, layout Layout, tick int, currentID, theme string, now time.Time, rows *[]toolRunRow) {
	if run == nil || len(run.members) == 0 {
		return
	}
	width := dialogWidth(layout)
	expanded := run.expanded()
	marker := blockCollapsedMarker
	if expanded {
		marker = blockExpandedMarker
	}
	parts := make([]string, 0, 3)
	phaseLabel := ""
	if phase := sanitizeInline(run.phaseLabel()); phase != "" {
		phaseLabel = "[Phase: " + phase + "]"
		parts = append(parts, phaseLabel)
	}
	parts = append(parts, toolCountLabel(len(run.members)))
	if span, ok := run.duration(); ok {
		parts = append(parts, formatDurationPrecise(span))
	}
	head := marker + " " + strings.Join(parts, " · ")
	if expanded {
		head += " (ctrl+o to collapse)"
	} else {
		head += " (ctrl+o to expand)"
	}
	if width > 0 {
		head = TruncateToWidth(head, width)
	}
	// The phase span is the only structural fact on an otherwise quiet
	// telemetry row: give it the context-blue accent while tool counts and
	// durations stay muted. Style after truncation so ANSI never affects the
	// width budget, and keep the whole row muted when the label was cut.
	styledHead := pal.Status.Render(head)
	if phaseLabel != "" {
		if idx := strings.Index(head, phaseLabel); idx >= 0 {
			styledHead = pal.Status.Render(head[:idx]) +
				pal.Cyan.Render(phaseLabel) +
				pal.Status.Render(head[idx+len(phaseLabel):])
		}
	}
	sb.WriteString(styledHead + "\n")
	if !expanded {
		return
	}

	lineIndex := strings.Count(sb.String(), "\n")
	for i, member := range run.members {
		branch := "├─"
		if i == len(run.members)-1 {
			branch = "└─"
		}
		status := member.presentationStatus(now)
		glyph := toolStatusGlyph(status, tick)
		style := toolStatusStyle(member, status, pal, theme, now)
		if status == ToolRunning && member.ID != currentID && !member.dwelling(now) {
			style = pal.Muted
		}
		slot := statusSlot(glyph)
		prefix := "  " + branch + " " + slot
		invocation := sanitizeInline(toolInvocation(member))
		invocationStyle := pal.Choice
		if status == ToolRunning && member.ID != currentID && !member.dwelling(now) {
			invocationStyle = pal.Muted
		}
		elapsed := ""
		if status != ToolPending {
			elapsed = member.elapsedAt(now)
		}
		outcome := ""
		switch status {
		case ToolRunning:
			if member.Streaming {
				outcome = "receiving…"
			}
		case ToolPending:
		default:
			outcome = collapsedOutcome(member)
		}

		fixed := CellWidth(prefix)
		suffix := ""
		if elapsed != "" {
			suffix += " (" + elapsed + ")"
		}
		if outcome != "" {
			suffix += " · " + outcome
		}
		budget := width - fixed - CellWidth(suffix)
		if budget < 6 && outcome != "" {
			// The invocation is the identity; drop the optional outcome
			// before cutting the call name beyond recognition.
			outcome = ""
			suffix = ""
			if elapsed != "" {
				suffix += " (" + elapsed + ")"
			}
			budget = width - fixed - CellWidth(suffix)
		}
		if budget < 1 {
			budget = 1
		}
		invocation = TruncateMiddleToWidth(invocation, budget)
		line := pal.Muted.Render("  "+branch+" ") + style.Render(slot) + invocationStyle.Render(invocation)
		if suffix != "" {
			line += pal.Muted.Render(suffix)
		}
		if width > 0 && CellWidth(line) > width {
			line = TruncateToWidth(line, width)
		}
		sb.WriteString(line + "\n")
		if rows != nil {
			*rows = append(*rows, toolRunRow{line: lineIndex, id: member.ID})
		}
		lineIndex++

		if !member.Expanded || !toolExpandable(member) {
			continue
		}
		bodyIndent := "     "
		if i < len(run.members)-1 {
			bodyIndent = "  │  "
		}
		var body strings.Builder
		renderToolBody(&body, member, pal, layout, bodyIndent)
		bodyLines := entryLines(body.String())
		for _, bodyLine := range bodyLines {
			sb.WriteString(bodyLine + "\n")
			if rows != nil {
				*rows = append(*rows, toolRunRow{line: lineIndex, id: member.ID})
			}
			lineIndex++
		}
	}
}

// renderTool writes a tool call and, once finished, its outcome.
//
// The default projection is collapsed and compact: one row naming the
// invocation with the duration right-aligned, plus one outcome row. The
// bounded body appears only when the block is expanded via Ctrl+O, so a long
// agent run stays scannable without losing the deeper content.
//
// Current is true only for the one running call that owns the activity. A
// non-current running call is still running in the same animated status slot,
// but it is muted and carries no live clock: concurrent calls never compete
// for attention. A completed call keeps its semantic glyph and only tweens the
// status colour for the short completion window.
//
// States are text plus color: ⠋ running, ✔ success, ✗ failure, ⊘ cancelled,
// · pending, so nothing depends on hue alone. Output is bounded and wrapped;
// large results collapse to the limit with a truncation marker rather than
// flooding the conversation.
func renderTool(sb *strings.Builder, tool *ToolEntry, pal *Palette, layout Layout, current bool, separate, showHint bool) {
	renderToolAt(sb, tool, pal, layout, current, separate, showHint, 0, "", time.Now())
}

// toolStatusStyle resolves the status role at one instant. During the short
// completion window the role is an RGB interpolation rather than the settled
// palette style, so a terminal frame never snaps from amber to green in a
// single tick.
func toolStatusStyle(tool *ToolEntry, status ToolStatus, pal *Palette, theme string, now time.Time) lipgloss.Style {
	if age, ok := tool.statusSettleAge(now); ok && fadeSupported() {
		if hex, tweening := statusTweenColor(status, age, theme); tweening {
			style := lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
			if age < fadeVaporEnd {
				style = style.Faint(true)
			}
			return style
		}
	}
	switch status {
	case ToolRunning:
		return pal.ToolRun
	case ToolDone:
		return pal.ToolDone
	case ToolError:
		return pal.ToolError
	default:
		return pal.Muted
	}
}

func renderToolAt(sb *strings.Builder, tool *ToolEntry, pal *Palette, layout Layout, current bool, separate, showHint bool, tick int, theme string, now time.Time) {
	width := dialogWidth(layout)
	status := tool.presentationStatus(now)
	glyph := toolStatusGlyph(status, tick)
	style := toolStatusStyle(tool, status, pal, theme, now)
	invocationStyle := pal.Choice
	if status == ToolRunning && !current && !tool.dwelling(now) {
		// A concurrent running call is still live, but it does not compete
		// with the one activity that owns the clock. Its glyph and command
		// content both recede; the status never turns the command green.
		style = pal.Muted
		invocationStyle = pal.Muted
	}
	slot := statusSlot(glyph)

	// Duration is right-aligned so a stream of calls reads down one axis. A
	// running call exposes its clock only after it has lasted long enough to
	// be meaningful, and only while it owns the activity; a completed call
	// always shows its measured duration.
	rightText := ""
	if status != ToolPending && (status != ToolRunning || current) {
		if status != ToolRunning || tool.durationAt(now) >= 100*time.Millisecond {
			rightText = tool.elapsedAt(now)
		}
	}
	leftBudget := width - CellWidth(slot) - 2
	if rightText != "" {
		leftBudget -= CellWidth(rightText)
	}
	if leftBudget < 6 && rightText != "" {
		rightText = ""
		leftBudget = width - CellWidth(slot) - 2
	}
	if leftBudget < 1 {
		leftBudget = 1
	}
	invocationText := sanitizeInline(toolInvocation(tool))
	if tool.Expanded && CellWidth(invocationText) > leftBudget {
		// Expansion is the user asking to see the whole target. Never
		// middle-cut it then: wrap the complete invocation across rows so a
		// long path stays fully readable instead of collapsing to "…".
		writeExpandedInvocation(sb, slot, invocationText, rightText, style, invocationStyle, pal, width)
	} else {
		invocation := TruncateMiddleToWidth(invocationText, leftBudget)
		left := style.Render(slot) + invocationStyle.Render(invocation)
		if rightText != "" {
			right := pal.Faint.Render(rightText)
			if gap := width - CellWidth(left) - CellWidth(right); gap >= 2 {
				sb.WriteString(left + strings.Repeat(" ", gap) + right + "\n")
			} else {
				sb.WriteString(left + "  " + right + "\n")
			}
		} else {
			sb.WriteString(left + "\n")
		}
	}

	// A running call that has produced partial output states that explicitly.
	// The final outcome replaces this row rather than leaving both visible.
	if status == ToolRunning && tool.Streaming {
		progress := sanitizeInline(tool.Summary)
		switch {
		case progress == "" || progress == sanitizeInline(tool.Invocation):
			progress = "receiving…"
		default:
			progress = "receiving · " + progress
		}
		budget := width - 4
		if budget < 1 {
			budget = 1
		}
		line := "  " + pal.Status.Render("└─ "+TruncateToWidth(progress, budget))
		if width > 0 && CellWidth(line) > width {
			line = TruncateToWidth(line, width)
		}
		sb.WriteString(line + "\n")
	}

	// The compact outcome lands on one └─ row: the finished summary when
	// the executor provided one, else a bounded first-line fallback so a
	// single-line result stays visible even with its body collapsed.
	// The hint width is reserved up front so the whole row stays bounded.
	if status != ToolRunning && status != ToolPending {
		outcome := collapsedOutcome(tool)
		hint := ""
		// Ctrl+O targets the latest expandable block, so the affordance
		// belongs on the last call of the run rather than repeated under
		// every earlier one. Earlier blocks remain click-expandable.
		if toolExpandable(tool) && !tool.Expanded && showHint {
			hint = "  (ctrl+o to expand)"
		}
		if outcome != "" {
			budget := width - 4 - CellWidth(hint)
			if budget < 1 {
				budget = 1
			}
			// The branch marker carries the error role and the message keeps
			// neutral body text. A failed command is recognisable at a glance
			// without turning the whole outcome into a red block.
			branchStyle := pal.Status
			outcomeStyle := pal.Status
			if status == ToolError {
				branchStyle = pal.ToolError
				outcomeStyle = pal.Choice
			}
			line := "  " + branchStyle.Render("└─ ") + outcomeStyle.Render(TruncateToWidth(outcome, budget))
			if hint != "" {
				line += pal.Faint.Render(hint)
			}
			if width > 0 && CellWidth(line) > width {
				line = TruncateToWidth(line, width)
			}
			sb.WriteString(line + "\n")
		} else if hint != "" {
			// A mutation result may carry only a diff. The compact outcome is
			// empty then, but the expansion affordance must still be visible
			// or the diff would be unreachable.
			sb.WriteString(pal.Muted.Render(TruncateToWidth(hint, width)) + "\n")
		}
	}

	// Collapsed is the default: the body below only renders expanded.
	// Expansion is presentation-only — the same semantic Text, bounded by
	// the same truncation, wrapping, and sanitization as before.
	if status == ToolRunning || !tool.Expanded || (tool.Text == "" && strings.TrimSpace(tool.Diff) == "") {
		// separate is only for a one-off projection (an expanded block printed
		// into scrollback) that needs a trailing gap after it. Inline
		// transcripts pass false and let the following block own the gap, so a
		// stream of calls never reflows an earlier block.
		if separate && (status == ToolDone || status == ToolError || status == ToolCancelled) {
			sb.WriteString("\n")
		}
		return
	}
	renderToolBody(sb, tool, pal, layout, "    ")
	sb.WriteString("\n")
}

// renderToolBody writes the bounded detail below an expanded tool. indent is
// the caller's row prefix: a standalone block uses four spaces, while a tree
// node uses its branch continuation so the body stays visibly nested.
func renderToolBody(sb *strings.Builder, tool *ToolEntry, pal *Palette, layout Layout, indent string) {
	if tool == nil {
		return
	}
	if strings.TrimSpace(tool.Diff) != "" {
		var diff strings.Builder
		if renderExpandedCodeDiff(&diff, tool, pal, layout) {
			width := dialogWidth(layout)
			for _, line := range entryLines(diff.String()) {
				if indent != "    " {
					// The diff renderer owns its base four-space gutter;
					// replace it so a nested body does not drift right of
					// its text siblings.
					line = indent + strings.TrimPrefix(line, "    ")
					if width > 0 && CellWidth(line) > width {
						line = TruncateToWidth(line, width)
					}
				}
				sb.WriteString(line + "\n")
			}
			return
		}
	}
	body, truncated := TruncateForDisplay(tool.Text, toolOutputLimit)
	// Tool results carry progress bars, colors, and carriage returns from
	// the child process: keep the colors, neutralize the cursor control,
	// then wrap what the terminal will actually lay out.
	body = sanitizeBlock(body)
	bodyWidth := textWidth(layout) - CellWidth(indent)
	if bodyWidth < 1 {
		bodyWidth = 1
	}
	shown := 0
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		for _, wrapped := range WrapToWidth(line, bodyWidth) {
			if shown >= maxToolLines(layout) {
				break
			}
			sb.WriteString(indent + wrapped + "\n")
			shown++
		}
		if shown >= maxToolLines(layout) {
			break
		}
	}
	if truncated || shown >= maxToolLines(layout) {
		sb.WriteString(pal.Truncated.Render(indent+"… [truncated]") + "\n")
	}
}

// writeExpandedInvocation writes an expanded badge whose full target does not
// fit on one row. Continuations align under the first text cell and no row is
// truncated, so every path segment survives; the duration is kept only when
// the first row has room for it.
func writeExpandedInvocation(sb *strings.Builder, slot, invocation, rightText string, statusStyle, invocationStyle lipgloss.Style, pal *Palette, width int) {
	const maxLines = 64
	prefixWidth := CellWidth(slot)
	budget := width - prefixWidth
	if budget < 1 {
		budget = 1
	}
	chunks := WrapToWidth(invocation, budget)
	right := ""
	if rightText != "" {
		right = pal.Status.Render(rightText)
	}
	for i, chunk := range chunks {
		if i >= maxLines {
			// A pathological argument still cannot flood the transcript: the
			// cut is marked, never silent, and every ordinary path fits well
			// inside this bound.
			sb.WriteString(strings.Repeat(" ", prefixWidth) + pal.Truncated.Render("… [truncated]") + "\n")
			break
		}
		var line string
		if i == 0 {
			line = statusStyle.Render(slot) + invocationStyle.Render(chunk)
			if right != "" {
				if gap := width - CellWidth(line) - CellWidth(right); gap >= 2 {
					line += strings.Repeat(" ", gap) + right
				}
			}
		} else {
			line = strings.Repeat(" ", prefixWidth) + invocationStyle.Render(chunk)
		}
		sb.WriteString(line + "\n")
	}
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
	if strings.TrimSpace(tool.Diff) != "" {
		return true
	}
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

// elapsedAt renders a measured duration compactly, omitting it entirely when
// the call has not started so a pending line is not padded with a zero. The
// presentation clock supplies now, so one frame cannot measure two instants.
func (t *ToolEntry) elapsedAt(now time.Time) string {
	if t == nil || t.StartedAt.IsZero() {
		return ""
	}
	return formatDurationPrecise(t.durationAt(now))
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
		return pal.ToolError.Render("✗ ") + pal.Choice.Render(sanitizeInline(shell.Err))
	}
	style := pal.ToolDone
	mark := "✓"
	if shell.ExitCode != 0 {
		style = pal.ToolError
		mark = "✗"
	}
	line := style.Render(mark) + pal.Status.Render(fmt.Sprintf(" exit %d", shell.ExitCode))
	if elapsed := shellElapsed(shell); elapsed != "" {
		line += pal.Faint.Render(" · " + elapsed)
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

// centeredCardWidth bounds a centered dialog: wide enough for its content,
// narrow enough to read as a popup with margins on roomy terminals. floor is
// the minimum width used when the terminal is too narrow to preserve margins.
func centeredCardWidth(termWidth, maxCard, minCard, floor int) int {
	if termWidth <= 0 {
		return maxCard
	}
	w := termWidth - 8
	if w > maxCard {
		w = maxCard
	}
	if w < minCard {
		w = termWidth - 4
		if w < floor {
			w = floor
		}
		if w > termWidth {
			w = termWidth
		}
	}
	return w
}

// quitCardWidth bounds the centered quit dialog: wide enough for the hint,
// narrow enough to read as a popup with margins on roomy terminals.
func quitCardWidth(termWidth int) int {
	return centeredCardWidth(termWidth, 52, 30, 10)
}

// quitCard builds the centered bordered confirmation: title, horizontal
// Yes/No buttons, and a hint naming the left/right keys. The selected button
// is flanked by ◀ ▶ so the choice never depends on hue alone.
func quitCard(confirm *PendingApproval, cardW int) string {
	return quitCardForTheme(confirm, cardW, ThemeDark)
}

// quitCardForTheme resolves the dialog from the current appearance.
func quitCardForTheme(confirm *PendingApproval, cardW int, theme string) string {
	if confirm == nil {
		return ""
	}
	t := design.For(theme)
	if cardW < 10 {
		cardW = 10
	}
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Brand)).Bold(true).
		Render("Really quit?")
	question := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.TextSecondary)).
		Render("Exit the session?")
	yesActive := confirm.Cursor == 0
	noActive := !yesActive
	arrow := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Brand)).Bold(true)
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
		leftYes, gitButtonForTheme("Yes", yesActive, theme), rightYes,
		"   ",
		leftNo, gitButtonForTheme("No", noActive, theme), rightNo,
	)
	hints := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.TextSecondary)).
		Render("←→ move · enter confirm · y quit · n/esc stay")
	inner := lipgloss.JoinVertical(lipgloss.Left, title, question, "", buttons, "", hints)
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Brand)).
		Padding(1, 2).
		Width(cardW).
		Render(inner)
	return paintSurface(card, t.SurfaceElevated)
}

// transitionCardWidth bounds the centered transition dialog: wide enough for
// the hint, narrow enough to read as a popup with margins on roomy terminals.
func transitionCardWidth(termWidth int) int {
	return centeredCardWidth(termWidth, 56, 32, 12)
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
	return transitionCardForTheme(prompt, cardW, ThemeDark)
}

// transitionCardForTheme resolves the dialog from the current appearance.
func transitionCardForTheme(prompt *TransitionPrompt, cardW int, theme string) string {
	if prompt == nil {
		return ""
	}
	t := design.For(theme)
	if cardW < 12 {
		cardW = 12
	}
	// Producer accents are the dark-screen identity forms. Resolve the
	// light-screen depth from the same authority label when possible, so a
	// transition prompt stays legible without turning the mode into a
	// different hue.
	currentAccent := prompt.CurrentAccent
	if accent := ModeAccentFor(theme, prompt.Current); accent != "" {
		currentAccent = accent
	}
	proposedAccent := prompt.ProposedAccent
	if accent := ModeAccentFor(theme, prompt.Proposed); accent != "" {
		proposedAccent = accent
	}
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color(proposedAccent)).Bold(true).
		Render(prompt.Question)
	reason := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.TextSecondary)).
		Render(prompt.Reason)
	arrow := lipgloss.NewStyle().
		Foreground(lipgloss.Color(proposedAccent)).Bold(true)
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
		leftKeep, transitionButtonForTheme("Keep "+prompt.Current, currentAccent, keepActive, theme), rightKeep,
		"   ",
		leftSwitch, transitionButtonForTheme(prompt.Proposed, proposedAccent, switchActive, theme), rightSwitch,
	)
	hints := lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.TextSecondary)).
		Render("Enter to confirm · Esc keeps " + prompt.Current)
	rows := []string{title, reason}
	if prompt.Handoff != nil {
		detail := lipgloss.NewStyle().
			Foreground(lipgloss.Color(t.TextSecondary))
		if prompt.Handoff.Objective != "" {
			rows = append(rows, detail.Render("Task: "+TruncateToWidth(prompt.Handoff.Objective, cardW)))
		}
		if carried := handoffCarriedLine(*prompt.Handoff); carried != "" {
			rows = append(rows, detail.Render("Carries: "+carried))
		}
		if prompt.Handoff.Workspace != "" {
			rows = append(rows, detail.Render("Workspace: "+TruncateToWidth(prompt.Handoff.Workspace, cardW)))
		}
		rows = append(rows, detail.Render("Authority change requires your approval"))
	}
	rows = append(rows, "", buttons, "", hints)
	inner := lipgloss.JoinVertical(lipgloss.Left, rows...)
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(proposedAccent)).
		Padding(1, 2).
		Width(cardW).
		Render(inner)
	return paintSurface(card, t.SurfaceElevated)
}

// handoffCarriedLine summarises the shared records a handoff references. It
// names only counts: the dialog never copies evidence text or a tool payload.
func handoffCarriedLine(handoff HandoffView) string {
	var parts []string
	if handoff.Facts > 0 {
		parts = append(parts, fmt.Sprintf("%d facts", handoff.Facts))
	}
	if handoff.Hypotheses > 0 {
		parts = append(parts, fmt.Sprintf("%d hypotheses", handoff.Hypotheses))
	}
	if handoff.Questions > 0 {
		parts = append(parts, fmt.Sprintf("%d questions", handoff.Questions))
	}
	if handoff.Evidence > 0 {
		parts = append(parts, fmt.Sprintf("%d evidence", handoff.Evidence))
	}
	if handoff.Artifacts > 0 {
		parts = append(parts, fmt.Sprintf("%d artifacts", handoff.Artifacts))
	}
	if handoff.Constraints > 0 {
		parts = append(parts, fmt.Sprintf("%d constraints", handoff.Constraints))
	}
	return strings.Join(parts, " · ")
}

// transitionButton renders one workspace choice: its own accent when
// highlighted, a quiet grey label otherwise.
func transitionButtonForTheme(label, accent string, active bool, theme string) string {
	text := "[ " + label + " ]"
	if active {
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(accent)).Bold(true).
			Render(text)
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(design.For(theme).TextSecondary)).
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
		card := transitionCardForTheme(prompt, transitionCardWidth(0), state.Theme)
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
	card := transitionCardForTheme(prompt, transitionCardWidth(width), state.Theme)
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
		card := quitCardForTheme(confirm, quitCardWidth(0), state.Theme)
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
	card := quitCardForTheme(confirm, quitCardWidth(width), state.Theme)
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
	if interruptConfirmVisible(state) {
		// When the hint band is unavailable this row is the bottom surface, so
		// the double-tap warning takes it rather than being silently dropped.
		text := interruptConfirmText
		if state.Width > 0 {
			text = TruncateToWidth(text, columnWidth(state, layout))
		}
		return ExtrasFor(state.Theme).Warning.Render(text)
	}
	status := state.Status
	parts := []string{}
	// The turn timer leads the context detail on every bottom status surface,
	// including approval rows where the hint bar is unavailable.
	if timer := turnElapsedLabel(state); timer != "" {
		parts = append(parts, timer)
	}
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
	// A newer release is chrome, never a transcript line. It has exactly one
	// home: the header, where the release identity already lives. The footer
	// stays context-only so the same fact cannot appear twice.
	// The runtime phase is named once, by the transcript entry that owns it
	// (or by the live footer when no entry exists); this status row stays
	// context-only so a third "working" copy never competes. SUP is a model
	// role, not a workspace: the footer keeps naming the workspace authority
	// while support is open. Support runtime state lives inside the overlay.
	out := strings.Join(parts, "  ·  ")
	width := columnWidth(state, layout)
	if state.Width > 0 {
		out = TruncateToWidth(out, width)
	}
	if status.Workspace == "" {
		return pal.Status.Render(out)
	}
	// The authority badge leads the footer, in its own semantic accent. Plain
	// cells are measured and truncated before styling, so ANSI never inflates
	// the one-row budget.
	badge := sanitizeInline(status.Workspace)
	accent := status.WorkspaceAccent
	if modeAccent := ModeAccentFor(state.Theme, status.Workspace); modeAccent != "" {
		accent = modeAccent
	}
	const sep = "  ·  "
	if state.Width > 0 {
		restBudget := width - CellWidth(badge) - CellWidth(sep)
		if restBudget < 0 {
			// Too narrow for both: the authority identity is the part that
			// must survive, so the context detail yields.
			return WorkspaceBadge(accent, TruncateToWidth(badge, width))
		}
		out = TruncateToWidth(out, restBudget)
	}
	styled := WorkspaceBadge(accent, badge)
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

// displayWidth is the budget for full-width chrome lines (header, picker).
func displayWidth(state State, layout Layout) int {
	if state.Width > 0 {
		return state.Width - 1
	}
	return 80
}

// columnWidth is the budget for chrome that lives inside the conversation
// column. Without the split it is the full frame width; with the sidebar it
// stops one cell before the divider so the rail never shifts.
func columnWidth(state State, layout Layout) int {
	if layout.Sidebar && layout.MainWidth > 0 {
		if w := layout.MainWidth - 1; w > 0 {
			return w
		}
		return 1
	}
	return displayWidth(state, layout)
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

// Prompt attribution. A prompt is a line of transcript, not a selection
// strip: the project-local worker name and a dim glyph introduce the words on
// the terminal's own background. Continuation rows hang under the content so
// wrapped prompts keep one visual column.
const (
	// promptUserFallback keeps an unset project worker attributable without
	// inventing a name. It is a label, never an identity.
	promptUserFallback = "user"
	// promptUserMaxCells bounds the name so a hostile or pathological
	// identity cannot consume the reading width.
	promptUserMaxCells = 24
	// promptGlyph separates attribution from the prompt body.
	promptGlyph = "›"
)

// renderUserBlock writes one prompt with its worker attribution. Each row is
// styled at the measured width; no row is padded, so the transcript keeps the
// terminal background and only the glyph, name, and words carry colour.
func renderUserBlock(sb *strings.Builder, text, user string, pal *Palette, layout Layout) {
	for _, line := range userPromptLines(text, user, pal, layout) {
		sb.WriteString(line + "\n")
	}
}

// userPromptLines is the single prompt projection shared by the fullscreen
// view and live scrollback. Keeping it shared is what stops the two surfaces
// from drifting into different attribution or wrap geometry.
func userPromptLines(text, user string, pal *Palette, layout Layout) []string {
	width := textWidth(layout)
	name := promptDisplayName(user, width)
	prefixStyled := pal.User.Render(name) + pal.Muted.Render(" "+promptGlyph+" ")
	prefixWidth := CellWidth(name + " " + promptGlyph + " ")

	// A viewport too narrow for the name keeps the glyph, then drops even that
	// if one content cell is impossible. Degrading is better than overflowing
	// a row the layout counted.
	if width > 0 && prefixWidth >= width {
		prefixStyled = pal.Muted.Render(promptGlyph + " ")
		prefixWidth = CellWidth(promptGlyph + " ")
	}
	bodyWidth := width
	if width > 0 {
		bodyWidth = width - prefixWidth
		if bodyWidth < 1 {
			prefixStyled, prefixWidth, bodyWidth = "", 0, width
		}
	}

	lines := wrapPlainWidth(text, bodyWidth)
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		styled := pal.Assistant.Render(line)
		switch {
		case i == 0 && prefixWidth > 0:
			out = append(out, prefixStyled+styled)
		case prefixWidth > 0:
			out = append(out, strings.Repeat(" ", prefixWidth)+styled)
		default:
			out = append(out, styled)
		}
	}
	return out
}

// promptDisplayName resolves the attribution name. The project-local worker is
// the authoritative source; an empty value falls back to the neutral label.
func promptDisplayName(user string, width int) string {
	// The name is metadata, not authored prose: strip any embedded styling so
	// a crafted identity cannot override the semantic accent.
	name := strings.TrimSpace(ansi.Strip(sanitizeInline(user)))
	if name == "" {
		name = promptUserFallback
	}
	limit := promptUserMaxCells
	if width > 0 {
		// Reserve the glyph separators plus at least one content cell.
		if room := width - CellWidth(" "+promptGlyph+" ") - 1; room < limit {
			limit = room
		}
	}
	if limit < 1 {
		limit = 1
	}
	return TruncateToWidth(name, limit)
}

// wrapPlain wraps unstyled transcript prose to the content width. Input is
// untrusted (prompts, notices, tool echoes), so it is sanitized first:
// control sequences that could move the terminal cursor must never reach
// measurement or output. Styled spans (markdown) are wrapped per line by
// the caller splitting on newlines; ANSI is stripped for measurement so
// styles never inflate the budget.
func wrapPlain(text string, layout Layout) []string {
	return wrapPlainWidth(text, textWidth(layout))
}

// wrapPlainWidth is wrapPlain at an explicit budget, so a prompt can wrap
// inside the width its attribution prefix leaves behind.
func wrapPlainWidth(text string, width int) []string {
	text = sanitizeBlock(text)
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

// AdvanceSpinner returns the next frame index.
func AdvanceSpinner(current, frames int) int {
	if frames <= 0 {
		return 0
	}
	return (current + 1) % frames
}
