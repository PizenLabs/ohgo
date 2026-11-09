package tui

// Progressive telemetry projection.
//
// This file owns the two time-based presentation rules that make streaming
// execution legible: a minimum dwell for a fast call's intent frame, and a
// short three-phase fade for a block that has just appeared. Neither rule
// changes runtime state, measured durations, or ordering. They only decide
// which already-known frame a renderer may show at a given instant.

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

const (
	// toolMinDwell is the shortest time an executing frame stays visible.
	// A 2ms read must not flash from nothing to a checkmark: the user gets
	// one readable intent frame before the settled outcome replaces it.
	toolMinDwell = 150 * time.Millisecond
	// fadeVaporEnd is the end of the dim neutral entrance phase.
	fadeVaporEnd = 30 * time.Millisecond
	// fadeSolidifyEnd is the end of the muted accent phase. The colour ramps
	// from here to the target over the next 10ms.
	fadeSolidifyEnd = 90 * time.Millisecond
	// fadeFinalAt is when the full palette target is reached. The short final
	// window keeps the target stable until the cached projection returns.
	fadeFinalAt = 100 * time.Millisecond
	// fadeDuration is the complete entrance window. Settled history never
	// carries a fade.
	fadeDuration = 120 * time.Millisecond
	// statusSettleDuration is the completion tween. A terminal frame keeps its
	// semantic glyph immediately but morphs its status colour from the running
	// amber into the final success/error role over this window.
	statusSettleDuration = 120 * time.Millisecond
	// statusSlotCells is the ghost box reserved for a status glyph. Every
	// state paints one cell in this slot, so a spinner can become a check
	// without shifting the invocation to the left or right.
	statusSlotCells = 1
)

// Fade palette: a deep neutral first, a muted sage/teal midpoint, then the
// theme's body colour. The midpoint is the same sage family as the chrome
// accent so the entrance reads as Ohgo, not as a generic terminal effect.
const (
	fadeVaporHex = "#333333"
	fadeSolidHex = "#5FB3A1"
)

// The completion tween starts and ends on the same palette roles the settled
// row uses. The hex forms are the standard xterm RGB values for those 256-col
// indexes; keeping them here means the tween cannot land on a different hue
// from the frame that replaces it.
const (
	statusRunDarkHex    = "#D7AF5F" // xterm 214, dark ToolRun
	statusRunLightHex   = "#D75F00" // xterm 166, light ToolRun
	statusDoneDarkHex   = "#00D700" // xterm 42, dark ToolDone
	statusDoneLightHex  = "#008700" // xterm 28, light ToolDone
	statusErrorDarkHex  = "#FF0000" // xterm 196, dark ToolError
	statusErrorLightHex = "#AF0000" // xterm 160, light ToolError
)

// fadePhase is one discrete step in the entrance sequence. Tests and
// diagnostics use it; the renderer uses the age directly so the colour can
// interpolate within a phase.
type fadePhase int

const (
	fadeSettled fadePhase = iota
	fadeVapor
	fadeSolidify
	fadeFinal
)

// fadePhaseAt maps a block's age to its discrete phase. The three visible
// phases exist so a 30ms presentation tick can step a new block through
// vapour, solidifying, and target instead of popping in at full brightness.
func fadePhaseAt(age time.Duration) fadePhase {
	switch {
	case age < 0:
		return fadeVapor
	case age < fadeVaporEnd:
		return fadeVapor
	case age < fadeFinalAt:
		return fadeSolidify
	case age < fadeDuration:
		return fadeFinal
	default:
		return fadeSettled
	}
}

// entryFading reports whether a block is inside its entrance window at now.
func entryFading(entry Entry, now time.Time) bool {
	if entry.AppearedAt.IsZero() {
		return false
	}
	// A frame timestamp can be a few milliseconds older than a block created
	// between ticks. Treat that as the first phase rather than skipping the
	// entrance entirely.
	return now.Sub(entry.AppearedAt) < fadeDuration
}

// fadeSupported reports whether RGB interpolation can be emitted. Reduced
// motion, NO_COLOR, dumb terminals, and colorless profiles keep the plain
// palette: the app must never inject ANSI into an environment that opted out.
func fadeSupported() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if term := os.Getenv("TERM"); term == "dumb" {
		return false
	}
	if os.Getenv("REDUCED_MOTION") != "" || os.Getenv("NO_ANIMATION") != "" {
		return false
	}
	return lipgloss.ColorProfile() != termenv.Ascii
}

// fadeTarget is the full-palette body colour a block settles to.
func fadeTarget(theme string) string {
	if NormalizeTheme(theme) == ThemeLight {
		return lightText
	}
	return darkText
}

// fadeColorAt interpolates the entrance colour for one age. It is a pure
// function: the same age and target always produce the same RGB triple.
func fadeColorAt(age time.Duration, target string) string {
	switch {
	case age < 0:
		age = 0
	case age >= fadeDuration:
		return target
	}
	if age < fadeVaporEnd {
		return fadeVaporHex
	}
	if age < fadeSolidifyEnd {
		t := float64(age-fadeVaporEnd) / float64(fadeSolidifyEnd-fadeVaporEnd)
		return blendHex(fadeVaporHex, fadeSolidHex, t)
	}
	if age < fadeFinalAt {
		t := float64(age-fadeSolidifyEnd) / float64(fadeFinalAt-fadeSolidifyEnd)
		return blendHex(fadeSolidHex, target, t)
	}
	return target
}

// blendHex linearly interpolates two #rrggbb colours. A malformed token falls
// back to the first colour rather than inventing a channel value.
func blendHex(from, to string, t float64) string {
	if t <= 0 {
		return from
	}
	if t >= 1 {
		return to
	}
	fr, fg, fb := parseHexColor(from)
	tr, tg, tb := parseHexColor(to)
	lerp := func(a, b int) int {
		return a + int(float64(b-a)*t)
	}
	return hexRGB(lerp(fr, tr), lerp(fg, tg), lerp(fb, tb))
}

func hexRGB(r, g, b int) string {
	const digits = "0123456789abcdef"
	buf := [7]byte{'#'}
	buf[1] = digits[(r>>4)&0xf]
	buf[2] = digits[r&0xf]
	buf[3] = digits[(g>>4)&0xf]
	buf[4] = digits[g&0xf]
	buf[5] = digits[(b>>4)&0xf]
	buf[6] = digits[b&0xf]
	return string(buf[:])
}

// fadeRenderedBlock applies the entrance colour to one rendered block.
//
// Terminal cells have no alpha, so the whole projection is flattened to its
// plain text for the short window. Inner syntax colours would otherwise win
// over the interpolation; the block settles back to the full palette at
// fadeDuration, when the cached projection is re-rendered normally.
func fadeRenderedBlock(rendered string, age time.Duration, theme string) string {
	if rendered == "" || !fadeSupported() {
		return rendered
	}
	if age < 0 {
		age = 0
	}
	color := fadeColorAt(age, fadeTarget(theme))
	r, g, b := parseHexColor(color)
	prefix := truecolorPrefix(r, g, b)
	switch fadePhaseAt(age) {
	case fadeVapor, fadeSolidify:
		prefix += "\x1b[2m"
	}
	var sb strings.Builder
	sb.Grow(len(rendered) + 24)
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		plain := ansi.Strip(line)
		if plain == "" {
			continue
		}
		sb.WriteString(prefix)
		sb.WriteString(plain)
		sb.WriteString("\x1b[0m")
	}
	return sb.String()
}

// fadeRenderedTail applies the entrance colour to the last non-empty visual
// line only. It is the final-answer counterpart to fadeRenderedBlock: the rest
// of a long streamed answer is settled, so re-fading it would look like a
// repaint rather than the arrival of the closing line.
func fadeRenderedTail(rendered string, age time.Duration, theme string) string {
	lines := strings.Split(rendered, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(ansi.Strip(lines[i])) == "" {
			continue
		}
		lines[i] = fadeRenderedBlock(lines[i], age, theme)
		break
	}
	return strings.Join(lines, "\n")
}

// assistantFinalLineAge returns the age of a settled trailing answer line when
// it is still inside its entrance window.
func assistantFinalLineAge(entry Entry, now time.Time) (time.Duration, bool) {
	if entry.FinalLineAt.IsZero() {
		return 0, false
	}
	age := now.Sub(entry.FinalLineAt)
	if age >= fadeDuration {
		return 0, false
	}
	return age, true
}

func truecolorPrefix(r, g, b int) string {
	// Keep the escape construction in one place; shimmer.go already owns the
	// foreground wrapper for inline text and this is the block-level form.
	return "\x1b[38;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
}

// toolRunGroupEligible reports whether a batch has the structural shape of a
// group: at least two adjacent calls, and a run that is closed by a following
// block or a settled turn. It deliberately ignores the dwell floor because an
// explicit Ctrl+O is a user request to open the tree immediately.
func toolRunGroupEligible(s State, run *toolRun) bool {
	if run == nil || len(run.members) < 2 {
		return false
	}
	if run.start < 0 || run.end < run.start || run.end >= len(s.Entries) {
		return false
	}
	if run.end+1 >= len(s.Entries) && (s.TurnSeq == 0 || s.Status.Busy) {
		return false
	}
	return true
}

// toolRunGroupActive reports whether a batch may collapse into its telemetry
// footer. A collapsed footer waits for every member's terminal frame
// (including any dwell); an already-expanded tree is an explicit disclosure
// and may render immediately. Until then the individual rows remain visible so
// a live call never disappears behind a summary.
func toolRunGroupActive(s State, run *toolRun, now time.Time) bool {
	if !toolRunGroupEligible(s, run) {
		return false
	}
	if run.expanded() {
		return true
	}
	return run.settledAt(now)
}

// toolStatusGlyph is the one-cell status marker for a tool row. The running
// frame is the shared Braille spinner; every terminal state uses a distinct
// text glyph, so the column never widens or narrows as the call settles.
func toolStatusGlyph(status ToolStatus, tick int) string {
	switch status {
	case ToolRunning:
		return spinnerGlyph(tick)
	case ToolDone:
		return "✔"
	case ToolError:
		return "✗"
	case ToolCancelled:
		return "⊘"
	default:
		return "·"
	}
}

// statusSlot renders a glyph in the invariant status box followed by the
// separator space. A caller budgets with this exact string: the glyph may
// animate, but the number of cells consumed is fixed.
func statusSlot(glyph string) string {
	if width := CellWidth(glyph); width < statusSlotCells {
		glyph += strings.Repeat(" ", statusSlotCells-width)
	}
	return glyph + " "
}

// settleAt is the instant the terminal frame may replace the executing frame.
//
// A fast call waits for the dwell floor; a call that already ran longer than
// the floor settles at its measured end. Timestamps the runtime never measured
// stay unknown, so the renderer never invents a tween start.
func (t *ToolEntry) settleAt() (time.Time, bool) {
	if t == nil || (t.Status != ToolDone && t.Status != ToolError) {
		return time.Time{}, false
	}
	settle := t.EndedAt
	if settle.IsZero() {
		settle = t.DwellUntil
	}
	if !t.DwellUntil.IsZero() && t.DwellUntil.After(settle) {
		settle = t.DwellUntil
	}
	if settle.IsZero() {
		return time.Time{}, false
	}
	return settle, true
}

// statusSettleAge reports how long ago the terminal frame became presentable.
// It returns false while the dwell floor still owns the row and after the
// completion tween has finished, so settled history stays cacheable.
func (t *ToolEntry) statusSettleAge(now time.Time) (time.Duration, bool) {
	if t == nil || t.presentationStatus(now) != t.Status {
		return 0, false
	}
	settle, ok := t.settleAt()
	if !ok {
		return 0, false
	}
	age := now.Sub(settle)
	if age < 0 || age >= statusSettleDuration {
		return 0, false
	}
	return age, true
}

// statusTweenColor interpolates the running amber into the terminal role.
// The terminal has no alpha, so a short colour ramp is the honest way to
// morph a status in place without changing the glyph's cell width. The
// endpoints match the palette roles, so the tween hands off without a final
// hue jump.
func statusTweenColor(status ToolStatus, age time.Duration, theme string) (string, bool) {
	light := NormalizeTheme(theme) == ThemeLight
	var start, target string
	switch status {
	case ToolDone:
		if light {
			start, target = statusRunLightHex, statusDoneLightHex
		} else {
			start, target = statusRunDarkHex, statusDoneDarkHex
		}
	case ToolError:
		if light {
			start, target = statusRunLightHex, statusErrorLightHex
		} else {
			start, target = statusRunDarkHex, statusErrorDarkHex
		}
	default:
		return "", false
	}
	if age < 0 {
		age = 0
	}
	if age >= statusSettleDuration {
		return target, false
	}
	t := float64(age) / float64(statusSettleDuration)
	return blendHex(start, target, t), true
}

// phaseLabel is the batch's measured authority span. Empty means the producer
// did not report a workspace; the formatter then omits the segment rather than
// inventing a phase. A single phase is named once instead of drawing a
// meaningless arrow to itself.
func (r *toolRun) phaseLabel() string {
	if r == nil {
		return ""
	}
	start := strings.TrimSpace(r.phaseStart)
	end := strings.TrimSpace(r.phaseEnd)
	switch {
	case start == "":
		return end
	case end == "" || end == start:
		return start
	default:
		return start + " → " + end
	}
}
