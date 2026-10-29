package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The header/footer chrome resolves through one token set: the brand, model,
// phase, and check segments must each wire to their semantic colour, and the
// active phase must never share the idle style.
func TestChromeTokensResolveToPalette(t *testing.T) {
	chrome := ChromeFor(ThemeDark)
	got := map[string]string{
		"brand": colorName(chrome.Brand.GetForeground()),
		"model": colorName(chrome.Model.GetForeground()),
		"check": colorName(chrome.Check.GetForeground()),
		"modes": colorName(chrome.Active.GetForeground()),
		"dim":   colorName(chrome.Separator.GetForeground()),
		"rel":   colorName(chrome.Version.GetForeground()),
	}
	// Literal hex: the palette is the contract, not the constant names.
	want := map[string]string{
		"brand": "#5FB3A1",
		"model": "#8FB08D",
		"check": "#C9A86A",
		"modes": "#7AA8C9",
		"dim":   "#4C566A",
		"rel":   "#6C7A89",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s foreground = %q, want token %q", name, got[name], w)
		}
	}
	if colorName(chrome.Active.GetForeground()) == colorName(chrome.Idle.GetForeground()) {
		t.Error("active and idle phases share a style: state reads by colour alone")
	}
}

// The prompt cursor, the input marker, and the focused composer border share
// the primary accent with the header brand; secondary context reads muted
// and rules draw dim.
func TestPromptAndRulesShareTheChrome(t *testing.T) {
	pal := PaletteFor(ThemeDark)
	if got := colorName(pal.Prompt.GetForeground()); got != "#5FB3A1" {
		t.Errorf("prompt = %q, want the primary %q", got, "#5FB3A1")
	}
	if got := colorName(pal.Cursor.GetForeground()); got != "#5FB3A1" {
		t.Errorf("cursor = %q, want the primary %q", got, "#5FB3A1")
	}
	if got := colorName(ExtrasFor(ThemeDark).Focus.GetForeground()); got != "#5FB3A1" {
		t.Errorf("focus = %q, want the primary %q", got, "#5FB3A1")
	}
	if got := colorName(pal.Status.GetForeground()); got != "#6C7A89" {
		t.Errorf("status = %q, want the muted %q", got, "#6C7A89")
	}
	if got := colorName(pal.Separator.GetForeground()); got != "#4C566A" {
		t.Errorf("separator = %q, want the dim %q", got, "#4C566A")
	}
}

// compactModel drops exactly the provider prefix: the leaf id survives, and
// prefix-less ids pass through untouched.
func TestCompactModelDropsProviderPrefix(t *testing.T) {
	cases := map[string]string{
		"groq/deepseek-r1-distill-llama-70b": "deepseek-r1-distill-llama-70b",
		"openrouter/cohere/north-mini":       "cohere/north-mini",
		"no-prefix-model":                    "no-prefix-model",
		"":                                   "",
	}
	for in, want := range cases {
		if got := compactModel(in); got != want {
			t.Errorf("compactModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// The header is one row at every width: identity, model leaf, and phase
// survive, connection detail sheds on compact terminals instead of wrapping.
func TestHeaderFitsAcrossWidths(t *testing.T) {
	models := []string{
		"groq/deepseek-r1-distill-llama-70b",
		"openrouter/cohere/north-mini-code:free",
		"m",
		"",
	}
	for _, width := range []int{140, 100, 81, 80, 70, 60, 55, 54, 50, 40} {
		for _, model := range models {
			for _, busy := range []bool{false, true} {
				s := New()
				s.Status.Model = model
				s.Apply(TerminalResized{Width: width, Height: 24})
				if busy {
					s.Apply(UserSubmitted{Text: "go"})
				}
				if ComputeLayout(s).Mode == LayoutMini {
					continue
				}
				out := Render(s)
				header := ansi.Strip(strings.Split(out, "\n")[0])
				if got := CellWidth(header); got > width {
					t.Errorf("width %d model %q busy=%v: header is %d cells, want one row: %q",
						width, model, busy, got, header)
				}
				if !strings.Contains(header, "ohgo") {
					t.Errorf("width %d: header lost identity: %q", width, header)
				}
				if busy && !strings.Contains(header, "working") {
					t.Errorf("width %d: busy header lost the phase: %q", width, header)
				}
				if !busy && !strings.Contains(header, "idle") {
					t.Errorf("width %d: idle header lost the phase: %q", width, header)
				}
				if width < 55 && strings.Contains(header, "not checked") {
					t.Errorf("width %d: compact header kept the probe tag: %q", width, header)
				}
				// A leaf that cannot fit hard-truncates, but its head must
				// survive: the header orients, never invents.
				if width >= 55 && model != "" && !strings.Contains(header, leafHead(model)) {
					t.Errorf("width %d: header lost the model head %q: %q", width, leafHead(model), header)
				}
			}
		}
	}
}

// leafHead is the model fragment the header must always keep: the start of
// the leaf id, which survives even a last-resort truncation.
func leafHead(model string) string {
	leaf := leafOf(model)
	runes := []rune(leaf)
	if len(runes) > 10 {
		return string(runes[:10])
	}
	return leaf
}

// leafOf is the shortest model fragment the header must always keep.
func leafOf(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}

// The provider prefix sheds only to fit: a wide terminal shows the full id,
// a medium one keeps the leaf, and a compact one that already fits keeps the
// full id rather than stripping for show.
func TestHeaderShortensPrefixOnlyToFit(t *testing.T) {
	const model = "groq/deepseek-r1-distill-llama-70b"
	const leaf = "deepseek-r1-distill-llama-70b"

	wide := New()
	wide.Status.Model = model
	wide.Apply(TerminalResized{Width: 120, Height: 24})
	if header := ansi.Strip(strings.Split(Render(wide), "\n")[0]); !strings.Contains(header, model) {
		t.Errorf("wide header = %q, want the full model id", header)
	}

	medium := New()
	medium.Status.Model = model
	medium.Apply(TerminalResized{Width: 60, Height: 24})
	mid := ansi.Strip(strings.Split(Render(medium), "\n")[0])
	if !strings.Contains(mid, leaf) {
		t.Errorf("medium header = %q, want the leaf id kept", mid)
	}
	if strings.Contains(mid, "groq/") {
		t.Errorf("medium header = %q, want the prefix shed to fit", mid)
	}

	fitting := New()
	fitting.Status.Model = "groq/some-long-model-name"
	fitting.Apply(TerminalResized{Width: 40, Height: 24})
	if header := ansi.Strip(strings.Split(Render(fitting), "\n")[0]); !strings.Contains(header, "groq/some-long-model-name") {
		t.Errorf("compact header = %q, want the fitting id kept whole", header)
	}
}

// Accenting a mode label must not move cells: stripping the styled row
// returns the input, so the viewport budget measured on plain text still
// holds.
func TestModeBadgeStylingKeepsCells(t *testing.T) {
	pal := DarkPalette()
	chrome := ChromeFor(ThemeDark)
	for _, line := range []string{
		"ASK      Understand, inspect, reason",
		"│ ASK      Understand   │ BUILD      Authorized    │",
		"│ model      groq/m                 │",
	} {
		styled := styleIdentityLine(line, pal, chrome)
		if got := ansi.Strip(styled); got != line {
			t.Errorf("stripped = %q, want the input cells unchanged %q", got, line)
		}
	}
}

// The header brands the release beside the app name, and an unknown release
// leaves the legacy line untouched: no placeholder, no shifted segments.
func TestHeaderShowsVersionBesideBrand(t *testing.T) {
	versioned := New()
	versioned.Status.Model = "groq/m"
	versioned.Status.Version = "1.0.0"
	versioned.Apply(TerminalResized{Width: 120, Height: 24})
	header := ansi.Strip(strings.Split(Render(versioned), "\n")[0])
	if !strings.Contains(header, "ohgo v1.0.0 ○") {
		t.Errorf("header = %q, want the release beside the brand", header)
	}

	bare := New()
	bare.Status.Model = "groq/m"
	bare.Apply(TerminalResized{Width: 120, Height: 24})
	legacy := ansi.Strip(strings.Split(Render(bare), "\n")[0])
	if !strings.Contains(legacy, "ohgo ○") {
		t.Errorf("header = %q, want the legacy brand-to-marker order with no release", legacy)
	}
	if strings.Contains(legacy, " v") {
		t.Errorf("header = %q, want no release label for an unknown version", legacy)
	}
}

// The release label is chrome, not model text: it survives every width while
// the header stays one row, with the model paying the cell budget.
func TestHeaderWithVersionFitsAcrossWidths(t *testing.T) {
	const model = "groq/deepseek-r1-distill-llama-70b"
	for _, width := range []int{140, 100, 81, 80, 70, 60, 55, 54, 50, 40} {
		s := New()
		s.Status.Model = model
		s.Status.Version = "1.0.0"
		s.Apply(TerminalResized{Width: width, Height: 24})
		if ComputeLayout(s).Mode == LayoutMini {
			continue
		}
		header := ansi.Strip(strings.Split(Render(s), "\n")[0])
		if got := CellWidth(header); got > width {
			t.Errorf("width %d: header is %d cells, want one row: %q", width, got, header)
		}
		if !strings.Contains(header, "v1.0.0") {
			t.Errorf("width %d: header shed the release label: %q", width, header)
		}
	}
}

// The greeting address reads bold while the brand keeps the heading style,
// boxed or bare alike: emphasis must not move cells either.
func TestGreetingAddressRendersBold(t *testing.T) {
	fp := forcedPalette()
	chrome := ChromeFor(ThemeDark)
	bare := "OHGO   Good evening, Jack."
	if styled := styleIdentityLine(bare, fp, chrome); !strings.Contains(styled, fp.Bold.Render("   Good evening, Jack.")) {
		t.Errorf("bare title = %q, want the address in bold", ansi.Strip(styled))
	}
	boxed := "│ OHGO   Good evening, Jack. │"
	styled := styleIdentityLine(boxed, fp, chrome)
	if !strings.Contains(styled, fp.Heading.Render("OHGO")) {
		t.Errorf("boxed title = %q, want the brand in the heading style", ansi.Strip(styled))
	}
	if !strings.Contains(styled, fp.Bold.Render("   Good evening, Jack. │")) {
		t.Errorf("boxed title = %q, want the address in bold", ansi.Strip(styled))
	}
	for _, line := range []string{bare, boxed} {
		if got := ansi.Strip(styleIdentityLine(line, fp, chrome)); got != line {
			t.Errorf("stripped = %q, want the input cells unchanged %q", got, line)
		}
	}
}

// OHGO mid-row is content, not the title: a path or id containing the brand
// must keep its own styling instead of borrowing the title's.
func TestGreetingTitleIgnoresMidRowBrand(t *testing.T) {
	fp := forcedPalette()
	chrome := ChromeFor(ThemeDark)
	for _, line := range []string{
		"│ workspace  ~/OHGOlabs │",
		"│ model      OHGOx      │",
	} {
		if greetingBrandIndex(line) >= 0 {
			t.Errorf("brand index matched mid-row content: %q", line)
		}
		if styled := styleIdentityLine(line, fp, chrome); strings.Contains(styled, fp.Heading.Render("OHGO")) {
			t.Errorf("row = %q, want no title styling on brand-like content", ansi.Strip(styled))
		}
	}
}
