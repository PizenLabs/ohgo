package design

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// relativeLuminance implements the WCAG 2.x sRGB definition.
func relativeLuminance(hex string) (float64, bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, false
	}
	channel := func(part string) (float64, bool) {
		v, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			return 0, false
		}
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92, true
		}
		return math.Pow((c+0.055)/1.055, 2.4), true
	}
	r, okR := channel(hex[0:2])
	g, okG := channel(hex[2:4])
	b, okB := channel(hex[4:6])
	if !okR || !okG || !okB {
		return 0, false
	}
	return 0.2126*r + 0.7152*g + 0.0722*b, true
}

func contrast(a, b string) float64 {
	la, okA := relativeLuminance(a)
	lb, okB := relativeLuminance(b)
	if !okA || !okB {
		return 0
	}
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// The three focus tiers must remain ordered: a title outranks body text,
// body text outranks faint metadata, and all three stay visible on their
// calibrated canvas. This is the contract that keeps a flat surface from
// returning.
func TestFocusTiersStayOrdered(t *testing.T) {
	for name, tok := range map[string]Tokens{"dark": Dark(), "light": Light()} {
		t.Run(name, func(t *testing.T) {
			title := contrast(tok.TextTitle, tok.SurfaceBackground)
			body := contrast(tok.TextPrimary, tok.SurfaceBackground)
			faint := contrast(tok.TextFaint, tok.SurfaceBackground)
			if !(title > body && body > faint) {
				t.Errorf("%s tiers = title %.2f, body %.2f, faint %.2f; want title > body > faint",
					name, title, body, faint)
			}
		})
	}
}

// Inline code must read as reference data, not as body text with a tinted
// background. Its foreground therefore has to be a chromatic accent (not a
// near-neutral lift) and must not collapse onto body or title text. This is
// the regression that made paths look muted in a report.
func TestInlineCodeTextIsAnAccent(t *testing.T) {
	for name, tok := range map[string]Tokens{"dark": Dark(), "light": Light()} {
		t.Run(name, func(t *testing.T) {
			if tok.InlineCodeForeground == tok.TextPrimary || tok.InlineCodeForeground == tok.TextTitle {
				t.Fatalf("inline code shares body/title text: %q", tok.InlineCodeForeground)
			}
			r, g, b, ok := rgbChannels(tok.InlineCodeForeground)
			if !ok {
				t.Fatalf("inline code foreground %q is not hex", tok.InlineCodeForeground)
			}
			chroma := max(r, g, b) - min(r, g, b)
			if chroma < 80 {
				t.Errorf("inline code foreground %q chroma = %d, want a visible accent (>= 80)",
					tok.InlineCodeForeground, chroma)
			}
		})
	}
}

// rgbChannels parses a #rrggbb value into its 8-bit channels.
func rgbChannels(hex string) (r, g, b int, ok bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	part := func(s string) (int, bool) {
		v, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			return 0, false
		}
		return int(v), true
	}
	r, okR := part(hex[0:2])
	g, okG := part(hex[2:4])
	b, okB := part(hex[4:6])
	return r, g, b, okR && okG && okB
}

// Report accents must stay distinct in both appearances. A heading, a bold
// lead-in, a list marker, body copy, and reasoning are different jobs; if two
// collapse onto one hue the document loses its navigable rhythm.
func TestReportAccentsStayDistinct(t *testing.T) {
	for name, tok := range map[string]Tokens{"dark": Dark(), "light": Light()} {
		t.Run(name, func(t *testing.T) {
			roles := map[string]string{
				"heading":     tok.Heading,
				"emphasis":    tok.Emphasis,
				"list marker": tok.ListMarker,
				"body":        tok.TextPrimary,
			}
			for a, ca := range roles {
				for b, cb := range roles {
					if a < b && ca == cb {
						t.Errorf("%s and %s share %q", a, b, ca)
					}
				}
			}
			// Reasoning and an operational warning are different stops even
			// though both are warm; they must not resolve to one value.
			if tok.ThoughtHeader == tok.Warning {
				t.Errorf("thought header and warning share %q", tok.ThoughtHeader)
			}
		})
	}
}

// The structure accent is the strongest signal in a report, so it must read
// as purple rather than a near-white lift of body text. Chroma rejects the
// pale tint that washed the headings out; the hue band keeps the accent in
// the violet family instead of drifting into an unrelated colour.
func TestHeadingReadsAsPurpleStructure(t *testing.T) {
	for name, tok := range map[string]Tokens{"dark": Dark(), "light": Light()} {
		t.Run(name, func(t *testing.T) {
			r, g, b, ok := rgbChannels(tok.Heading)
			if !ok {
				t.Fatalf("heading %q is not a six-digit hex colour", tok.Heading)
			}
			if chroma := max(r, g, b) - min(r, g, b); chroma < 70 {
				t.Errorf("heading %q chroma = %d, want a saturated purple (>= 70)", tok.Heading, chroma)
			}
			if hue := rgbHue(r, g, b); hue < 245 || hue > 290 {
				t.Errorf("heading %q hue = %.0f°, want the purple band [245, 290]", tok.Heading, hue)
			}
		})
	}
}

// rgbHue returns the HSL hue of an already-decoded colour in degrees.
func rgbHue(r, g, b int) float64 {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	hi := math.Max(rf, math.Max(gf, bf))
	lo := math.Min(rf, math.Min(gf, bf))
	delta := hi - lo
	if delta == 0 {
		return 0
	}
	var hue float64
	switch hi {
	case rf:
		hue = math.Mod((gf-bf)/delta, 6)
	case gf:
		hue = (bf-rf)/delta + 2
	default:
		hue = (rf-gf)/delta + 4
	}
	hue *= 60
	if hue < 0 {
		hue += 360
	}
	return hue
}

// The measured surface pairs back the appearance calibration. Normal text
// must clear 4.5:1; quiet-but-readable disabled text and meaningful non-text
// indicators must clear 3:1. These are contract checks, not a claim of full
// WCAG conformance for terminal themes we do not control.
func TestTokenContrast(t *testing.T) {
	for name, tok := range map[string]Tokens{"dark": Dark(), "light": Light()} {
		t.Run(name, func(t *testing.T) {
			bg := tok.SurfaceBackground
			text := map[string]string{
				"title":          tok.TextTitle,
				"primary":        tok.TextPrimary,
				"secondary":      tok.TextSecondary,
				"muted":          tok.TextMuted,
				"brand":          tok.Brand,
				"model":          tok.Model,
				"check":          tok.Check,
				"active":         tok.Active,
				"success":        tok.Success,
				"warning":        tok.Warning,
				"error":          tok.Error,
				"running":        tok.Running,
				"thought header": tok.ThoughtHeader,
				"thought body":   tok.ThoughtBody,
				"heading":        tok.Heading,
				"emphasis":       tok.Emphasis,
				"list marker":    tok.ListMarker,
				"recap":          tok.Recap,
				"code string":    tok.CodeString,
				"diff header":    tok.DiffHeader,
				"diff context":   tok.DiffContext,
				"support":        tok.SupportPrimary,
				"support accent": tok.SupportSecondary,
			}
			for role, fg := range text {
				if got := contrast(fg, bg); got < 4.5 {
					t.Errorf("%s %s contrast = %.2f:1, want >= 4.5:1", name, role, got)
				}
			}
			// TextFaint is the Low Focus tier: intentionally below the
			// normal-text floor so metadata recedes. It still has a visibility
			// floor, and it must stay dimmer than the readable muted role so
			// the hierarchy cannot silently invert.
			faint := contrast(tok.TextFaint, bg)
			if faint < 2 {
				t.Errorf("%s faint contrast = %.2f:1, want >= 2:1", name, faint)
			}
			if faint >= contrast(tok.TextMuted, bg) {
				t.Errorf("%s faint contrast = %.2f:1, want dimmer than muted", name, faint)
			}
			if got := contrast(tok.BorderFocus, bg); got < 3 {
				t.Errorf("%s focus contrast = %.2f:1, want >= 3:1", name, got)
			}
			if got := contrast(tok.TextDisabled, bg); got < 3 {
				t.Errorf("%s disabled contrast = %.2f:1, want >= 3:1", name, got)
			}
			if got := contrast(tok.CodeForeground, tok.CodeBackground); got < 4.5 {
				t.Errorf("%s code contrast = %.2f:1, want >= 4.5:1", name, got)
			}
			for role, fg := range map[string]string{
				"keyword": tok.CodeKeyword,
				"string":  tok.CodeString,
				"comment": tok.TextMuted,
				"number":  tok.ToolName,
			} {
				if got := contrast(fg, tok.CodeBackground); got < 4.5 {
					t.Errorf("%s code %s contrast = %.2f:1, want >= 4.5:1", name, role, got)
				}
			}
			if got := contrast(tok.InlineCodeForeground, tok.InlineCodeBackground); got < 4.5 {
				t.Errorf("%s inline code contrast = %.2f:1, want >= 4.5:1", name, got)
			}
			if got := contrast(tok.TextPrimary, tok.SurfaceSelection); got < 4.5 {
				t.Errorf("%s selection contrast = %.2f:1, want >= 4.5:1", name, got)
			}
			if got := contrast(tok.DiffAddForeground, tok.DiffAddBackground); got < 4.5 {
				t.Errorf("%s diff add contrast = %.2f:1, want >= 4.5:1", name, got)
			}
			if got := contrast(tok.DiffDeleteForeground, tok.DiffDeleteBackground); got < 4.5 {
				t.Errorf("%s diff delete contrast = %.2f:1, want >= 4.5:1", name, got)
			}
		})
	}
}
