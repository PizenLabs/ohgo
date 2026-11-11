package contract

import "strings"

// NoMutationRequested reports whether the request states an explicit limit on
// the whole repository change, rather than a narrower instruction such as
// "do not change the API".
//
// It is finite vocabulary, not interpretation. Only phrases whose object is the
// change itself ("do not change anything", "do not modify anything", "leave the
// code alone") qualify; a prohibition on one file or symbol is a constraint on
// the change, not a statement that the task is informational. The helper exists
// so the completion contract and the task-continuity decision cannot disagree
// about the request's own words.
func NoMutationRequested(text string) bool {
	normalized := " " + normalizeConstraintText(text) + " "
	for _, phrase := range noMutationPhrases {
		if phraseIsWholeChange(normalized, phrase) {
			return true
		}
	}
	return false
}

// noMutationPhrases are whole-change prohibitions, normalized without
// punctuation. Apostrophes become spaces, so "don't" is matched as "don t"
// and the contraction cannot silently bypass the limit.
var noMutationPhrases = []string{
	"do not change anything", "do not modify anything", "do not touch anything",
	"do not alter anything", "do not edit anything",
	"don t change anything", "don t modify anything", "don t touch anything",
	"don t alter anything", "don t edit anything",
	"dont change anything", "dont modify anything", "dont touch anything",
	"dont alter anything", "dont edit anything",
	"never change anything", "never modify anything", "never touch anything",
	"without changing anything", "without modifying anything",
	"leave it alone", "leave the code alone", "leave the file alone",
	"leave the repository alone",
}

// scopeExtensionWords mark a phrase that narrows the prohibition to something
// other than the whole change. "Don't change anything else" is a scope limit,
// not a request to leave the repository untouched.
var scopeExtensionWords = map[string]bool{
	"else": true, "other": true, "more": true, "besides": true,
	"outside": true, "apart": true, "beyond": true,
}

// phraseIsWholeChange reports whether phrase occurs in padded without a scope
// extension immediately after it.
func phraseIsWholeChange(padded, phrase string) bool {
	needle := " " + phrase + " "
	for offset := 0; offset < len(padded); {
		index := strings.Index(padded[offset:], needle)
		if index < 0 {
			return false
		}
		after := offset + index + len(needle)
		fields := strings.Fields(padded[after:])
		next := ""
		if len(fields) > 0 {
			next = fields[0]
		}
		if !scopeExtensionWords[next] {
			return true
		}
		offset = after
	}
	return false
}

// normalizeConstraintText lowercases text and collapses every run of
// non-alphanumeric characters to one space.
func normalizeConstraintText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := true
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
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
