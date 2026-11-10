package tui

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// The three authority accents are product identity, not theme-chrome colours.
// Their hue is fixed; only depth follows the appearance. Each form must stay
// legible against the surface it is rendered on.
func TestModeAccentsRemainLegibleAndDistinct(t *testing.T) {
	dark := DarkTokens()
	light := LightTokens()
	lum := func(hex string) float64 {
		hex = strings.TrimPrefix(hex, "#")
		if len(hex) != 6 {
			t.Fatalf("%q is not a six-digit hex colour", hex)
		}
		total := 0.0
		for i, weight := range []float64{0.2126, 0.7152, 0.0722} {
			v, err := strconv.ParseUint(hex[i*2:i*2+2], 16, 8)
			if err != nil {
				t.Fatalf("%q is not hex: %v", hex, err)
			}
			c := float64(v) / 255
			if c <= 0.04045 {
				c /= 12.92
			} else {
				c = math.Pow((c+0.055)/1.055, 2.4)
			}
			total += weight * c
		}
		return total
	}
	ratio := func(a, b string) float64 {
		hi, lo := math.Max(lum(a), lum(b)), math.Min(lum(a), lum(b))
		return (hi + 0.05) / (lo + 0.05)
	}
	cases := []struct {
		name string
		mode workspace.Workspace
		bg   string
	}{
		{"ASK dark", workspace.Ask, dark.SurfaceBackground},
		{"FUZZ dark", workspace.Fuzz, dark.SurfaceBackground},
		{"BUILD dark", workspace.Build, dark.SurfaceBackground},
		{"ASK light", workspace.Ask, light.SurfaceBackground},
		{"FUZZ light", workspace.Fuzz, light.SurfaceBackground},
		{"BUILD light", workspace.Build, light.SurfaceBackground},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		theme := ThemeDark
		if strings.HasSuffix(tc.name, "light") {
			theme = ThemeLight
		}
		accent := ModeAccentFor(theme, tc.mode.Label())
		if accent == "" {
			t.Errorf("%s has no accent", tc.name)
			continue
		}
		if seen[accent] {
			t.Errorf("%s reuses another authority's accent %q", tc.name, accent)
		}
		seen[accent] = true
		if got := ratio(accent, tc.bg); got < 3 {
			t.Errorf("%s contrast = %.2f:1, want >= 3:1", tc.name, got)
		}
	}
}

// FUZZ is an authority badge, not an alarm. Its pair must stay in the violet
// family and remain desaturated enough for a long session: the coral it used
// to carry read as an error state, and a neon purple glares on both canvases.
func TestFuzzAccentReadsAsSoftViolet(t *testing.T) {
	for _, tc := range []struct{ name, accent string }{
		{"dark", ModeAccentFuzz},
		{"light", ModeAccentFuzzLight},
	} {
		r, g, b, ok := accentChannels(tc.accent)
		if !ok {
			t.Fatalf("%s FUZZ accent %q is not a six-digit hex colour", tc.name, tc.accent)
		}
		hue := accentHue(r, g, b)
		if hue < 250 || hue > 315 {
			t.Errorf("%s FUZZ hue = %.0f°, want the violet band [250, 315]", tc.name, hue)
		}
		chroma := max(r, g, b) - min(r, g, b)
		if chroma > 120 {
			t.Errorf("%s FUZZ chroma = %d, want a desaturated badge (<= 120)", tc.name, chroma)
		}
	}
}

// accentChannels parses a #rrggbb value into 8-bit red, green, and blue.
func accentChannels(hex string) (r, g, b int, ok bool) {
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

// accentHue returns the HSL hue in degrees. The caller has already decoded the
// channels, so only the hue formula belongs here.
func accentHue(r, g, b int) float64 {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	hi, lo := math.Max(rf, math.Max(gf, bf)), math.Min(rf, math.Min(gf, bf))
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
