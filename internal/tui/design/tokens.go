// Package design is the canonical visual-token source for OHGO's terminal UI.
//
// It is deliberately data-only: no renderer, no terminal I/O, and no theme
// preference policy. The tui package resolves a stored preference to one of
// these palettes; components then consume semantic roles rather than literals.
// The diff inspector shares the same values, so one product language survives
// across presentation packages.
//
// Every role names a purpose, never a hue. The three authority accents are not
// here: ASK, FUZZ, and BUILD are owned by control/workspace because they are
// product identity, not theme chrome. Chrome roles and authority roles must
// remain independent, which is why success never borrows BUILD blue and
// warning never becomes FUZZ violet.
package design

// Tokens is one complete appearance. Values are ANSI-independent colour
// strings: truecolor hex where a calibrated role needs exact control, or a
// 256-colour index where the terminal fallback is already the intended value.
//
// Surfaces exist only where OHGO paints one. The terminal owns the canvas
// background, so SurfaceBackground documents the pair the text was calibrated
// against rather than a painted rectangle.
type Tokens struct {
	// Surfaces.
	SurfaceBackground string
	SurfacePrimary    string
	SurfaceSecondary  string
	SurfaceElevated   string
	SurfaceInput      string
	SurfaceSelection  string

	// Text, in the three focus tiers the interface renders.
	//
	// TextTitle is the High Focus chrome tier: application brand and dialog
	// titles. It is deliberately brighter than body text on a dark screen and
	// deeper than body text on a light one, so a title is the first thing the
	// eye lands on without painting a background.
	//
	// TextPrimary is the Medium Focus tier: commands, tool invocations, answer
	// prose, and the content the user actually reads.
	//
	// TextFaint is the Low Focus tier: timings, key hints, path exclusions, and
	// other supporting metadata. It is intentionally below the WCAG 4.5:1
	// normal-text floor (measured in tokens_test.go), so it must never carry
	// information the user needs to act on; TextMuted is the quiet readable
	// role for that.
	TextTitle     string
	TextPrimary   string
	TextSecondary string
	TextMuted     string
	TextDisabled  string
	TextFaint     string
	TextInverse   string

	// Borders.
	BorderSubtle  string
	BorderDefault string
	BorderStrong  string
	BorderFocus   string

	// Product chrome.
	Brand     string
	BrandMute string
	Model     string
	Check     string
	Active    string
	User      string
	ToolName  string
	Prompt    string
	Recap     string
	Idle      string
	Info      string

	// Document structure accents. They are not additional focus tiers: a
	// heading is still High Focus, but its hue says "this is a boundary in
	// the document" rather than "this is chrome". The three families are
	// deliberately distinct so a report never collapses into one hue:
	//
	//   Heading   — violet: headings and table headers.
	//   Emphasis  — warm gold: bold lead-ins and key inline claims.
	//   ListMarker — muted violet: list glyphs, a quieter relative of Heading.
	Heading    string
	Emphasis   string
	ListMarker string

	// Thought is the cognitive stop: amber marks internal reasoning, kept
	// apart from both the neutral answer and the operational warning colour.
	// The header is the strong form; the body is the muted, italic form.
	ThoughtHeader string
	ThoughtBody   string

	// Operational status. These are independent of authority identity: a
	// successful FUZZ call keeps FUZZ violet and gains a success glyph.
	Success string
	Warning string
	Error   string
	Running string

	// Code and prose hierarchy.
	CodeForeground string
	CodeBackground string
	CodeString     string
	CodeKeyword    string
	// Inline code is a pill inside prose, not a fence: it needs its own pair
	// so a path or filename reads as reference data without inheriting the
	// block's full-width surface. The foreground is a chromatic reference
	// accent rather than a near-body text colour; the hue, not just the tint,
	// is what makes the artifact pop.
	InlineCodeForeground string
	InlineCodeBackground string
	Quote                string

	// Diff roles.
	DiffAddForeground    string
	DiffAddBackground    string
	DiffDeleteForeground string
	DiffDeleteBackground string
	DiffContext          string
	DiffHeader           string
	DiffGutter           string

	// Support overlay accents.
	SupportPrimary   string
	SupportSecondary string
}

// Dark is the Midnight Slate appearance: a cool near-black canvas, soft slate
// body text, and three restrained accent families. The neutral base is
// deliberately not green-tinted; colour is reserved for structure (violet),
// emphasis (gold), reference data (blue), and reasoning (amber). The three
// authority accents are supplied by workspace.Style at the call site and are
// not redefined here.
func Dark() Tokens {
	return Tokens{
		SurfaceBackground: "#0E1117",
		SurfacePrimary:    "#141821",
		SurfaceSecondary:  "#191E28",
		SurfaceElevated:   "#161B25",
		SurfaceInput:      "#141922",
		SurfaceSelection:  "#1E2432",

		TextTitle:     "#E9EDF7",
		TextPrimary:   "#C2CBDB",
		TextSecondary: "#9DA9BD",
		TextMuted:     "#7E8BA0",
		TextDisabled:  "#616D81",
		TextFaint:     "#4C5769",
		TextInverse:   "#0B0E13",

		BorderSubtle:  "#1A202A",
		BorderDefault: "#252C38",
		BorderStrong:  "#333D4C",
		BorderFocus:   "#5FB3A1",

		Brand:     "#5FB3A1",
		BrandMute: "#6E9C93",
		Model:     "#9FB2CE",
		Check:     "#C9A86A",
		Active:    "#8FB4D9",
		User:      "#5FB3A1",
		ToolName:  "#8FB3C9",
		Prompt:    "#5FB3A1",
		Recap:     "#6E9C93",
		Idle:      "#7E8BA0",
		Info:      "#8FB4D9",

		Heading:    "#B79BFF",
		Emphasis:   "#E8CE9B",
		ListMarker: "#8F8FC9",

		ThoughtHeader: "#DFB878",
		ThoughtBody:   "#C2A468",

		Success: "#7FC29B",
		Warning: "#E0A465",
		Error:   "#E49A9A",
		Running: "#8FB4D9",

		CodeForeground:       "#C9D5E6",
		CodeBackground:       "#141A24",
		CodeString:           "#DCC08C",
		CodeKeyword:          "#C5B8F2",
		InlineCodeForeground: "#9ED8FF",
		InlineCodeBackground: "#162333",
		Quote:                "#9AA5B8",

		DiffAddForeground:    "#A9DBBB",
		DiffAddBackground:    "#13241C",
		DiffDeleteForeground: "#D9A6A9",
		DiffDeleteBackground: "#2A1A1F",
		DiffContext:          "#7E8BA0",
		DiffHeader:           "#8FB4D9",
		DiffGutter:           "#7C8A9C",

		SupportPrimary:   "#8FB4D9",
		SupportSecondary: "#DCC08C",
	}
}

// Light is the Warm Porcelain appearance. It is independently calibrated for
// a bright terminal: a barely-warm near-white canvas, cool ink text, and the
// same accent families deepened rather than inverted. It is never a numerical
// inversion of Dark.
func Light() Tokens {
	return Tokens{
		SurfaceBackground: "#F7F7F4",
		SurfacePrimary:    "#FCFCFA",
		SurfaceSecondary:  "#F1F1ED",
		SurfaceElevated:   "#FCFCFA",
		SurfaceInput:      "#FFFFFF",
		SurfaceSelection:  "#E8E9F2",

		TextTitle:     "#12161D",
		TextPrimary:   "#262C36",
		TextSecondary: "#4A5361",
		TextMuted:     "#626B7A",
		TextDisabled:  "#838C99",
		TextFaint:     "#9AA2AE",
		TextInverse:   "#FCFCFA",

		BorderSubtle:  "#E8E8E4",
		BorderDefault: "#D9DAD6",
		BorderStrong:  "#C2C5C0",
		BorderFocus:   "#1F6F61",

		Brand:     "#1F6F61",
		BrandMute: "#4E7069",
		Model:     "#4C6280",
		Check:     "#8A6A2F",
		Active:    "#3C6E96",
		User:      "#1F6F61",
		ToolName:  "#2F6B7D",
		Prompt:    "#1F6F61",
		Recap:     "#4E7069",
		Idle:      "#626B7A",
		Info:      "#3C6E96",

		Heading:    "#513C9E",
		Emphasis:   "#7A5719",
		ListMarker: "#6B62A8",

		ThoughtHeader: "#8A6420",
		ThoughtBody:   "#6E5520",

		Success: "#2C7A55",
		Warning: "#96551E",
		Error:   "#A94545",
		Running: "#3C6E96",

		CodeForeground:       "#2A3442",
		CodeBackground:       "#EDEFF2",
		CodeString:           "#7A5A1E",
		CodeKeyword:          "#4B3E8C",
		InlineCodeForeground: "#0B5C8A",
		InlineCodeBackground: "#E2EEF6",
		Quote:                "#4A5361",

		DiffAddForeground:    "#1D5A39",
		DiffAddBackground:    "#E8F3EC",
		DiffDeleteForeground: "#8E2F2F",
		DiffDeleteBackground: "#F9E9E8",
		DiffContext:          "#4A5361",
		DiffHeader:           "#2F628F",
		DiffGutter:           "#7C8794",

		SupportPrimary:   "#2F628F",
		SupportSecondary: "#7A5A1E",
	}
}

// For resolves an effective appearance name to its tokens. An unknown name
// degrades to Dark, matching the renderer's historical safety behaviour.
func For(name string) Tokens {
	if name == "light" {
		return Light()
	}
	return Dark()
}
