package tui

import (
	"strings"

	"github.com/muesli/termenv"
)

// rgbSeq renders a #rrggbb token in the SGR parameter form a truecolor
// sequence carries. It lets style tests assert the token without duplicating
// the literal the token already owns.
//
// The conversion goes through the same termenv path the renderer uses: its
// colour library stores channels as floats, so naively formatting the parsed
// hex can differ from the emitted sequence by one in the last place. Using the
// production conversion keeps the assertion tied to what the terminal sees.
func rgbSeq(hex string) string {
	if len(hex) != 7 || !strings.HasPrefix(hex, "#") {
		return ""
	}
	seq := termenv.RGBColor(hex).Sequence(false)
	return strings.TrimPrefix(seq, "38;2;")
}
