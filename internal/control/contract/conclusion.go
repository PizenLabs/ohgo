package contract

import (
	"strings"
	"unicode"
)

// IsProvisionalConclusion reports whether a conclusion payload explicitly says
// the investigation is not settled yet.
//
// This is a control-plane guard, not a semantic understanding of prose. It
// recognizes the small set of phrases a model uses when it is reporting
// progress, such as "placeholder, not yet sufficient", "preliminary", or
// "still investigating". A provisional payload must be recorded as an
// observation, never admitted as a terminal conclusion.
//
// The comparison is normalized so punctuation, case, and code formatting do
// not hide a marker. The markers are deliberately phrase-based: words such as
// "insufficient" and "incomplete" occur in legitimate findings about stock or
// implementation, so they do not trigger on their own.
func IsProvisionalConclusion(parts ...string) bool {
	text := normalizeConclusionText(strings.Join(parts, " "))
	for _, marker := range []string{
		" placeholder not yet sufficient ",
		" placeholder conclusion ",
		" not yet sufficient ",
		" not yet concluded ",
		" investigation remains open ",
		" investigation is still in progress ",
		" still investigating ",
		" still in progress ",
		" continuing investigation ",
		" preliminary ",
		" insufficient evidence ",
		" insufficient information ",
		" not enough evidence ",
		" not enough information ",
		" need more evidence ",
		" needs more evidence ",
		" more evidence is needed ",
		" more evidence needed ",
		" evidence is incomplete ",
		" incomplete evidence ",
		" investigation is incomplete ",
		" not yet established ",
		" further investigation ",
		" requires investigation ",
		" pending investigation ",
		" to be determined ",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// normalizeConclusionText lowercases and reduces punctuation and whitespace to
// single separators, so "Placeholder, not-yet sufficient." still matches.
func normalizeConclusionText(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 2)
	b.WriteByte(' ')
	space := true
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}
