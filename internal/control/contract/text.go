package contract

import "strings"

// NormalizeWords lowercases text and reduces every run of non-word characters
// to one space. The contract's phrase recognizers compare against a padded
// form of this value, so punctuation and formatting cannot hide a term.
//
// It is exported because cognition's shape extraction uses the same
// normalisation for its own phrase lists. Two normalisers would eventually
// disagree about the same objective.
func NormalizeWords(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := false
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// joinAlternatives renders a short enum as prose: "A", "A or B", or
// "A, B, or C".
func joinAlternatives(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	case 2:
		return values[0] + " or " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", or " + values[len(values)-1]
	}
}
