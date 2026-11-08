package workspace

// Style is the semantic identity of a workspace: a stable accent and label.
//
// These are semantic identity, not arbitrary user-theme colors (§9). A user
// must be able to learn that teal means ASK, blue means BUILD, and magenta
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
// ASK → teal, BUILD → blue, FUZZ → magenta (§9). An invalid workspace returns
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
// The dark forms are muted so the prompt stays quiet during a long session;
// the light forms deepen the same hue so it survives a bright screen. The
// neutral is deliberately outside all three hue families so an unresolved
// workspace is visibly not an authority.
const (
	// AccentAsk is ASK's muted teal identity on a dark screen.
	AccentAsk = "#2dd4bf"
	// AccentAskLight is ASK's deep teal identity on a bright screen.
	AccentAskLight = "#0f766e"
	// AccentBuild is BUILD's muted steel-blue identity on a dark screen.
	AccentBuild = "#60a5fa"
	// AccentBuildLight is BUILD's deep blue identity on a bright screen.
	AccentBuildLight = "#1d4ed8"
	// AccentFuzz is FUZZ's muted mauve identity on a dark screen.
	AccentFuzz = "#e879f9"
	// AccentFuzzLight is FUZZ's deep purple identity on a bright screen.
	AccentFuzzLight = "#a21caf"

	accentUnknown = "244"
)
