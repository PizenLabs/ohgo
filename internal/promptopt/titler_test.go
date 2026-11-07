package promptopt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A local title must read as the task, not as the sentence that introduced it,
// and must never consume the column with a dangling article.
func TestSanitizeTitleStripsNoiseAndBoundsAtWordBoundaries(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "request framing and articles",
			raw:  "Use ls to inspect the project root",
			want: "ls project root",
		},
		{
			name: "make sure framing",
			raw:  "Please make sure to fix the parser",
			want: "fix parser",
		},
		{
			name: "thanks framing",
			raw:  "Thank you in advance, fix the parser",
			want: "fix parser",
		},
		{
			name: "make command is not framing",
			raw:  "make install",
			want: "make install",
		},
		{
			name: "paths survive and trailing slash is decoration",
			raw:  "Use ls to inspect the project root and backend/",
			want: "ls project root backend",
		},
		{
			name: "politeness and pronouns",
			raw:  "Please explain the auth flow to me",
			want: "auth flow",
		},
		{
			name: "trailing article is dropped",
			raw:  "Use ls to inspect the",
			want: "ls",
		},
		{
			name: "punctuation around tokens",
			raw:  "Fix the parser, please!",
			want: "Fix parser",
		},
		{
			name: "repeated words add no navigation value",
			raw:  "check this project and analyze what this project is about",
			want: "check project analyze",
		},
		{
			name: "identifiers and paths stay whole",
			raw:  "internal/promptopt/titler.go in the patch",
			want: "internal/promptopt/titler.go patch",
		},
		{
			name: "noise only",
			raw:  "please explain the",
			want: "",
		},
		{
			name: "markdown heading and bold markers",
			raw:  "### **Fix** the parser",
			want: "Fix parser",
		},
		{
			name: "issue identifiers keep their marker",
			raw:  "#42 is broken",
			want: "#42 broken",
		},
		{
			name: "glob is not emphasis",
			raw:  "check **/*.go",
			want: "check **/*.go",
		},
		{
			name: "five words",
			raw:  "one two three four five six seven",
			want: "one two three four five",
		},
		{
			name: "rune bound never splits a word",
			raw:  "authentication configuration serialization deserialization initialization",
			want: "authentication configuration",
		},
		{
			name: "single pathological token stays bounded",
			raw:  strings.Repeat("a", 60),
			want: strings.Repeat("a", TitleMaxRunes),
		},
		{
			name: "empty",
			raw:  "   ",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeTitle(tc.raw)
			if got != tc.want {
				t.Fatalf("SanitizeTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if utf8.RuneCountInString(got) > TitleMaxRunes {
				t.Fatalf("SanitizeTitle(%q) = %q, longer than %d runes", tc.raw, got, TitleMaxRunes)
			}
			if len(strings.Fields(got)) > TitleMaxWords {
				t.Fatalf("SanitizeTitle(%q) = %q, more than %d words", tc.raw, got, TitleMaxWords)
			}
		})
	}
}

// TruncateTitle is the shared bound used by both the local sanitizer and the
// model-answer normalizer: it may only cut between words, and it must remove a
// stop word even when the rune bound created the dangling edge.
func TestTruncateTitleCutsAtWordsAndDropsDanglingStopWords(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		maxWords int
		maxRunes int
		want     string
	}{
		{
			name:     "word bound",
			raw:      "one two three four five six seven",
			maxWords: 5,
			maxRunes: 0,
			want:     "one two three four five",
		},
		{
			name:     "rune bound keeps whole words",
			raw:      "alpha beta gamma",
			maxWords: 5,
			maxRunes: 6,
			want:     "alpha",
		},
		{
			name:     "dangling stop word after word cut",
			raw:      "one two three four the six",
			maxWords: 5,
			maxRunes: 0,
			want:     "one two three four",
		},
		{
			name:     "dangling stop word after rune cut",
			raw:      "alpha the extraordinarilylong",
			maxWords: 5,
			maxRunes: 9,
			want:     "alpha",
		},
		{
			name:     "trailing preposition without truncation",
			raw:      "Fix the parser in",
			maxWords: 6,
			maxRunes: 80,
			want:     "Fix the parser",
		},
		{
			name:     "rune bound keeps multibyte words whole",
			raw:      "日本語 テスト",
			maxWords: 5,
			maxRunes: 3,
			want:     "日本語",
		},
		{
			name:     "single overlong token is capped",
			raw:      strings.Repeat("a", 50),
			maxWords: 5,
			maxRunes: 10,
			want:     strings.Repeat("a", 10),
		},
		{
			name:     "single dangling token is dropped rather than split",
			raw:      "the",
			maxWords: 5,
			maxRunes: 2,
			want:     "",
		},
		{
			name:     "non-positive word bound is disabled",
			raw:      "one two",
			maxWords: 0,
			maxRunes: 40,
			want:     "one two",
		},
		{
			name:     "empty",
			raw:      "   ",
			maxWords: 5,
			maxRunes: 40,
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateTitle(tc.raw, tc.maxWords, tc.maxRunes); got != tc.want {
				t.Fatalf("TruncateTitle(%q, %d, %d) = %q, want %q",
					tc.raw, tc.maxWords, tc.maxRunes, got, tc.want)
			}
		})
	}
}
