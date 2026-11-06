package tui

import (
	"strings"
	"testing"
)

// The lexical pass repairs complete prose words and preserves sentence case.
// It runs on the rendered projection only, never on stored text.
func TestStreamingTextCorrectsCommonTypos(t *testing.T) {
	cases := map[string]string{
		"teh recieve seperate":        "the receive separate",
		"Teh RECIEVE":                 "The RECEIVE",
		"I definately occured":        "I definitely occurred",
		"a **teh** word":              "a **the** word",
		"the dependency dependancies": "the dependency dependencies",
		"word .":                      "word.",
	}
	for in, want := range cases {
		if got := correctStreamingText(in); got != want {
			t.Errorf("correctStreamingText(%q) = %q, want %q", in, got, want)
		}
	}
}

// Code is exact content: fenced blocks, inline spans, paths, and URLs must pass
// through the correction pass byte-for-byte.
func TestStreamingTextPreservesCodeAndExactStrings(t *testing.T) {
	inputs := []string{
		"```\nteh recieve\n```",
		"`teh recieve`",
		"read /Users/teh/project/recieve.go",
		"see https://teh.example/recieve?teh=1",
		"a **bold** and `teh` mix",
	}
	for _, in := range inputs {
		got := correctStreamingText(in)
		if strings.Contains(in, "**") {
			// Prose emphasis is markdown syntax, but the code span inside it
			// must still be untouched.
			if !strings.Contains(got, "`teh`") {
				t.Errorf("inline code changed inside emphasis: %q -> %q", in, got)
			}
			continue
		}
		if got != in {
			t.Errorf("exact content changed:\n%q ->\n%q", in, got)
		}
	}
}

// A partially streamed word is not corrected until the whole token arrives, so
// text never flickers between a correction and its prefix.
func TestStreamingTextLeavesPartialWordsAlone(t *testing.T) {
	for _, partial := range []string{"reciev", "seperat", "tehx"} {
		if got := correctStreamingText(partial); got != partial {
			t.Errorf("partial %q became %q", partial, got)
		}
	}
}

// Double-encoded UTF-8 and invisible formatting characters are repaired; a
// legitimate ZWJ emoji sequence is not split.
func TestStreamingTextRepairsArtifactsWithoutSplittingGlyphs(t *testing.T) {
	if got := correctStreamingText("donâ€™t"); got != "don't" {
		t.Errorf("mojibake repair = %q, want don't", got)
	}
	if got := correctStreamingText("zero\u200bwidth"); got != "zerowidth" {
		t.Errorf("zero-width repair = %q, want zerowidth", got)
	}
	const family = "👨\u200d👩\u200d👧"
	if got := correctStreamingText(family); got != family {
		t.Errorf("ZWJ sequence changed: %q -> %q", family, got)
	}
}

// The pass is idempotent: rendering the same accumulated text twice never
// keeps changing it.
func TestStreamingTextCorrectionIsIdempotent(t *testing.T) {
	in := "teh recieve github `teh` and a ."
	once := correctStreamingText(in)
	if twice := correctStreamingText(once); twice != once {
		t.Fatalf("not idempotent:\n%q\n%q", once, twice)
	}
}

// Markdown rendering applies the correction without mutating the stored text.
func TestMarkdownRenderCorrectsDisplayedProseOnly(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(AssistantDelta{Text: "teh recieve github"})
	s.Apply(AssistantDone{})

	out := stripForTest(t, Render(s))
	for _, want := range []string{"the receive GitHub"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered prose lost %q:\n%s", want, out)
		}
	}
	if got := s.Entries[0].AssistantText; got != "teh recieve github" {
		t.Fatalf("stored text was mutated: %q", got)
	}
}
