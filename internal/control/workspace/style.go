package workspace

// Style is the semantic identity of a workspace: a stable accent and label.
//
// These are semantic identity, not arbitrary user-theme colors (§9). A user
// must be able to learn that teal means ASK, blue means BUILD, and violet
// means FUZZ without relearning it when they switch palettes. Each authority
// therefore has a dark-screen and a light-screen form of the same hue: the
// light forms are deepened so they read on a bright terminal, the dark forms
// are softened so a long session is not neon. Every surface also carries the
// uppercase label, so no meaning depends on hue alone.
//
// The color system never participates in authority decisions: Style is
// presentation only. Authority is decided by Permits/Admit, which know nothing
// about color.
type Style struct {
	// Accent is the dark-screen semantic color used for the workspace's chrome.
	Accent string
	// LightAccent is the same authority's deepened color for bright terminals.
	LightAccent string
	// Label is the uppercase display name.
	Label string
}

// Style returns the fixed semantic style for a workspace.
//
// ASK → teal, BUILD → blue, FUZZ → violet (§9). An invalid workspace returns
// a muted neutral so a rendering bug is visible rather than disguised as one of
// the three authorities.
func (w Workspace) Style() Style {
	switch w {
	case Ask:
		return Style{Accent: AccentAsk, LightAccent: AccentAskLight, Label: w.Label()}
	case Fuzz:
		return Style{Accent: AccentFuzz, LightAccent: AccentFuzzLight, Label: w.Label()}
	case Build:
		return Style{Accent: AccentBuild, LightAccent: AccentBuildLight, Label: w.Label()}
	}
	return Style{Accent: accentUnknown, LightAccent: accentUnknown, Label: "?"}
}

// Fixed semantic accents. Named so a change lands here and nowhere else.
//
// These are OHGO's signature identity colours, not theme chrome. The exact
// hexes are part of the product contract: ASK is teal, BUILD is blue, FUZZ is
// a soft violet. The light forms are the same hue deepened for a bright
// terminal; they are not a different colour family. The FUZZ pair stays
// desaturated on purpose: lavender reads as an authority badge, while a
// high-chroma magenta or neon purple glares during a long session. A change
// here is an identity change, not a palette tweak.
const (
	// AccentAsk is ASK's teal identity on a dark screen.
	AccentAsk = "#5FB3A1"
	// AccentAskLight is ASK's deep teal identity on a bright screen.
	AccentAskLight = "#1F6F61"
	// AccentBuild is BUILD's blue identity on a dark screen.
	AccentBuild = "#61AFEF"
	// AccentBuildLight is BUILD's deep blue identity on a bright screen.
	AccentBuildLight = "#2B6CB0"
	// AccentFuzz is FUZZ's soft lavender-violet identity on a dark screen.
	AccentFuzz = "#C678DD"
	// AccentFuzzLight is FUZZ's deep violet identity on a bright screen.
	AccentFuzzLight = "#8C4398"

	accentUnknown = "244"
)
