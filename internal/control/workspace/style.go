package workspace

// Style is the semantic identity of a workspace: a stable accent and label.
//
// These are semantic identity, not user-theme colors (§9). They must stay the
// same under any terminal palette, so a user can learn that magenta means FUZZ
// without relearning it when they switch from Dracula to Solarized. The accent
// is a 256-color index, chosen to resolve on every color-capable terminal and
// degrade predictably elsewhere.
//
// The color system never participates in authority decisions: Style is
// presentation only. Authority is decided by Permits/Admit, which know nothing
// about color.
type Style struct {
	// Accent is the 256-color index used for the workspace's chrome.
	Accent string
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
		return Style{Accent: accentAsk, Label: w.Label()}
	case Fuzz:
		return Style{Accent: accentFuzz, Label: w.Label()}
	case Build:
		return Style{Accent: accentBuild, Label: w.Label()}
	}
	return Style{Accent: accentUnknown, Label: "?"}
}

// Fixed semantic accents. Named so a change lands here and nowhere else.
const (
	accentAsk     = "37"  // teal
	accentBuild   = "33"  // blue
	accentFuzz    = "200" // magenta
	accentUnknown = "244" // muted grey, deliberately not one of the three
)
