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
