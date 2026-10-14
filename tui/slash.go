package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Slash palette geometry. The name column is fixed so a description always
// starts at the same cell: filtering, cycling the cursor, or switching the
// active row never shifts the column, which is what makes the overlay read as
// a table instead of jittering.
const (
	// PaletteNameCols is the fixed width of the command-name column, including
	// the leading slash. Names are padded with spaces to this width so the
	// description column cannot move between rows.
	PaletteNameCols = 18
	// PaletteMaxRows bounds the selectable rows. Discovery shows the top
	// matches only, so the palette never evicts the conversation behind it.
	PaletteMaxRows = 6
	// paletteBorderRows is the rounded box's top and bottom border. The layout
	// reservation and the renderer share it so reserved and painted rows agree.
	paletteBorderRows = 2
	// paletteMarkerCols is the fixed width of the selection gutter. It keeps
	// the cursor visible without colour (nothing depends on hue alone) and
	// keeps the name column aligned across active and inactive rows.
	paletteMarkerCols = 2
	// paletteMinInner is the smallest usable content width before the box
	// itself stops being readable.
	paletteMinInner = 8
)

// paletteStyles names the semantic styles the palette uses, resolved once per
// theme so the row builder never reaches for a raw colour.
type paletteStyles struct {
	border       lipgloss.Style
	header       lipgloss.Style
	marker       lipgloss.Style
	activeName   lipgloss.Style
	activeDesc   lipgloss.Style
	inactiveName lipgloss.Style
	inactiveDesc lipgloss.Style
}

// paletteStylesFor resolves the palette styles from the shared chrome tokens.
//
// Dark follows the design tokens exactly: active name in ColorPrimary and
// bold, active description in ColorText, inactive name in ColorMuted, inactive
// description in ColorDim, border in ColorDim. Light keeps the same roles with
// the darkened counterparts so a bright screen stays legible.
func paletteStylesFor(theme string) paletteStyles {
	if NormalizeTheme(theme) == ThemeLight {
		return paletteStyles{
			border:       fg(chromeLightDim),
			header:       fg(lightMuted).Bold(true),
			marker:       fg(chromeLightPrimary),
			activeName:   fg(chromeLightPrimary).Bold(true),
			activeDesc:   fg(lightText),
			inactiveName: fg(lightMuted),
			inactiveDesc: fg(chromeLightDim),
		}
	}
	return paletteStyles{
		border:       fg(ColorDim),
		header:       fg(ColorMuted).Bold(true),
		marker:       fg(ColorPrimary),
		activeName:   fg(ColorPrimary).Bold(true),
		activeDesc:   fg(ColorText),
		inactiveName: fg(ColorMuted),
		inactiveDesc: fg(ColorDim),
	}
}

// filterCommands returns the visible registry entries whose name starts with
// query, case-insensitively. Hidden aliases stay out, matching /help, so the
// palette can never show a command the listing hides. The registry is the
// single source of truth, so a shown command is always one dispatch accepts.
func filterCommands(query string) []CommandSpec {
	// The registry names never carry the leading slash, and both the stateful
	// caller (which strips it) and the exported palette (which may not) feed
	// this, so trim one defensively rather than forcing every caller to agree.
	filter := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(query)), "/")
	var out []CommandSpec
	for _, spec := range Commands {
		if spec.Hidden {
			continue
		}
		if filter == "" || strings.HasPrefix(strings.ToLower(spec.Name), filter) {
			out = append(out, spec)
		}
	}
	return out
}

// ClampPaletteCursor keeps a selection inside the filtered match list. An
// empty list parks at zero; an overshooting index lands on the last match
// rather than rendering no highlight at all.
func ClampPaletteCursor(cursor, matches int) int {
	if matches <= 0 || cursor < 0 {
		return 0
	}
	if cursor >= matches {
		return matches - 1
	}
	return cursor
}

// paletteRequest is the renderer's input. It carries an already-windowed slice
// plus the absolute indices needed for the overflow markers, so the stateful
// renderer and the exported RenderSlashPalette share one row builder and
// cannot drift.
type paletteRequest struct {
	// shown is the visible match slice, in registry order.
	shown []CommandSpec
	// cursor is the absolute index into the full match list.
	cursor int
	// startIndex is the absolute index of shown[0].
	startIndex int
	// total is the full match count, for the "more" markers.
	total int
	// above and below report rows hidden on either side of the window.
	above, below bool
	// current names the workspace to mark "· current" on its command.
	current string
	// headers renders the WORKSPACE/CONTROL group titles.
	headers bool
	// width is the cells the whole box may occupy.
	width int
	// exactRows, when positive, is the total number of lines the box must
	// emit (borders included). The stateful caller reserves rows before it
	// trims the match list, so it pads the box to the reservation: a frame
	// that emits fewer rows than it reserved would float the composer.
	exactRows int
	// styles resolves the semantic palette colours.
	styles paletteStyles
}

// RenderSlashPalette renders the slash-command overlay for the text typed
// after the leading "/".
//
// It is the palette's pure entry point: query, cursor, and width in, a string
// out. The filter is a case-insensitive prefix match on the command name, the
// cursor is clamped into range, and only the top PaletteMaxRows matches show.
// Every row is padded to one content width, so cycling the cursor or typing
// filter letters can never change the layout. It returns "" when nothing
// matches, which the caller treats as "no overlay".
func RenderSlashPalette(query string, cursor, width int) string {
	matches := filterCommands(query)
	if len(matches) == 0 {
		return ""
	}
	cursor = ClampPaletteCursor(cursor, len(matches))
	shown, start, above, below := windowMatches(matches, cursor, PaletteMaxRows)
	return renderPaletteBox(paletteRequest{
		shown:      shown,
		cursor:     cursor,
		startIndex: start,
		total:      len(matches),
		above:      above,
		below:      below,
		headers:    true,
		width:      width,
		styles:     paletteStylesFor(ThemeDark),
	})
}

// renderPaletteBox builds the rounded, fixed-width overlay.
//
// Rows are laid out plain first and padded to one content width, then each
// field is styled: measuring after styling would let ANSI codes inflate the
// column. The box owns the only decoration, so a group title, a match, and an
// overflow marker all share the same width and the overlay never shifts.
func renderPaletteBox(req paletteRequest) string {
	if len(req.shown) == 0 && !req.above && !req.below {
		return ""
	}
	inner := paletteInnerWidth(req.width)
	nameW := PaletteNameCols
	if avail := inner - paletteMarkerCols - 1; nameW > avail {
		nameW = avail
	}
	if nameW < 4 {
		nameW = 4
	}
	// Description budget: the fixed gutter, name column, and one separator.
	descW := inner - paletteMarkerCols - nameW - 1
	if descW < 0 {
		descW = 0
	}

	var lines []string
	if req.above {
		lines = append(lines, req.styles.inactiveDesc.Render(padPaletteRow(fmt.Sprintf("… %d above", req.startIndex), inner)))
	}
	lastGroup := ""
	for i, spec := range req.shown {
		if req.headers {
			if group := groupOf(spec); group != lastGroup {
				lines = append(lines, req.styles.header.Render(padPaletteRow(group, inner)))
				lastGroup = group
			}
		}
		lines = append(lines, paletteItemRow(spec, req.startIndex+i == req.cursor, req.current, nameW, descW, inner, req.styles))
	}
	if req.below {
		hidden := req.total - req.startIndex - len(req.shown)
		lines = append(lines, req.styles.inactiveDesc.Render(padPaletteRow(fmt.Sprintf("… %d more", hidden), inner)))
	}
	// Fill the reserved height exactly. Trimming the match list can shed a
	// group header along with the last row of that group, so the content may
	// come in under the reservation; padding the interior keeps the frame
	// exactly as tall as the layout promised.
	if req.exactRows >= paletteBorderRows+1 {
		for len(lines)+paletteBorderRows < req.exactRows {
			lines = append(lines, strings.Repeat(" ", inner))
		}
		for len(lines)+paletteBorderRows > req.exactRows && len(lines) > 0 {
			lines = lines[:len(lines)-1]
		}
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(req.styles.border.GetForeground()).
		Padding(0, 1)
	return box.Render(strings.Join(lines, "\n"))
}

// paletteItemRow renders one command row. The selection is carried by the
// marker glyph and by colour together: a monochrome terminal still shows which
// row Enter would accept.
func paletteItemRow(spec CommandSpec, selected bool, current string, nameW, descW, inner int, st paletteStyles) string {
	marker := "  "
	nameStyle := st.inactiveName
	descStyle := st.inactiveDesc
	if selected {
		marker = st.marker.Render("▸ ")
		nameStyle = st.activeName
		descStyle = st.activeDesc
	}
	name := padCells("/"+spec.Name, nameW)
	if CellWidth(name) > nameW {
		name = TruncateToWidth(name, nameW)
	}
	desc := spec.Description
	if current != "" && strings.EqualFold(spec.Name, current) {
		if desc != "" {
			desc += " · current"
		} else {
			desc = "current"
		}
	}
	desc = TruncateToWidth(desc, descW)
	// Pad the description so the styled row is exactly `inner` cells wide.
	gap := inner - paletteMarkerCols - nameW - 1 - CellWidth(desc)
	if gap > 0 {
		desc += strings.Repeat(" ", gap)
	}
	return marker + nameStyle.Render(name) + " " + descStyle.Render(desc)
}

// paletteInnerWidth is the content width inside the border and padding. It
// never collapses below paletteMinInner: a caller that passed an unusable
// width gets a readable box rather than a degenerate one.
func paletteInnerWidth(width int) int {
	if width <= 0 {
		width = 64
	}
	inner := width - paletteBorderRows - 2 // box sides + one cell of horizontal padding each
	if inner < paletteMinInner {
		inner = paletteMinInner
	}
	return inner
}

// padPaletteRow pads a plain row to exactly width cells. It is used for rows
// without a styled interior (headers and overflow markers), whose trailing
// space is safe to style as one span.
func padPaletteRow(text string, width int) string {
	text = TruncateToWidth(text, width)
	if gap := width - CellWidth(text); gap > 0 {
		text += strings.Repeat(" ", gap)
	}
	return text
}
