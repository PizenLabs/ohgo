package promptopt

import (
	"strings"
	"testing"
)

// Filler must disappear, but the instruction it framed must survive verbatim.
func TestCompressPromptStripsConversationalFluff(t *testing.T) {
	raw := "Hello,\n\nPlease make sure to review the parser. Thank you in advance!\n\nI would like you to run the tests."
	want := "review the parser.\n\nrun the tests."
	if got := CompressPrompt(raw); got != want {
		t.Fatalf("CompressPrompt() = %q, want %q", got, want)
	}
}

// Code is evidence, not prose: fenced blocks, inline spans, paths, and globs
// must survive byte-for-byte while surrounding filler is removed.
func TestCompressPromptPreservesCodeAndTechnicalText(t *testing.T) {
	codeBlock := "```go\nfunc main() {\n\tfmt.Println(\"please  keep  this\")\n}\n```"
	raw := "Please review:\n\n" + codeBlock + "\n\nDo **not** change `internal/promptopt/compressor.go` or */*. Thanks!\n"
	got := CompressPrompt(raw)

	if !strings.Contains(got, codeBlock) {
		t.Fatalf("code block was altered:\n%s", got)
	}
	for _, want := range []string{
		"review:",
		"`internal/promptopt/compressor.go`",
		"*/*",
		"Do not change",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("compressed prompt lost %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "**not**") {
		t.Fatalf("bold markdown survived:\n%s", got)
	}
	if strings.Contains(got, "Thanks") {
		t.Fatalf("thanks survived:\n%s", got)
	}
}

// Compression is a formatter, so a second pass must not keep deleting content.
func TestCompressPromptIsIdempotent(t *testing.T) {
	raw := "Hello,\n\nPlease make sure to **review** `a  b` or */*.\n\n```go\nplease  keep  this\n```\n"
	once := CompressPrompt(raw)
	twice := CompressPrompt(once)
	if once != twice {
		t.Fatalf("second pass changed the prompt:\nonce  %q\ntwice %q", once, twice)
	}
}

// Greeting variants are removed only when they actually frame a request; a
// hyphenated word that merely starts with "hi" is technical text.
func TestCompressPromptRemovesGreetingVariants(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "hi there", raw: "Hi there, please run the tests.", want: "run the tests."},
		{name: "good morning", raw: "Good morning! Can you review the parser?", want: "review the parser?"},
		{name: "would you mind", raw: "Would you mind reviewing the parser?", want: "reviewing the parser?"},
		{name: "hyphenated word", raw: "hi-fi audio is noisy", want: "hi-fi audio is noisy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompressPrompt(tc.raw); got != tc.want {
				t.Fatalf("CompressPrompt(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// Glob syntax and bold syntax both use asterisks. A span with a path separator
// stays untouched because it is technical text, not decoration.
func TestCompressPromptDoesNotTreatGlobAsBold(t *testing.T) {
	raw := "Check **/foo/** and **/*.go please."
	got := CompressPrompt(raw)
	for _, want := range []string{"**/foo/**", "**/*.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("compressed prompt lost glob %q:\n%s", want, got)
		}
	}
}

// A prompt with no filler is already optimal; technical instructions must pass
// through unchanged rather than being "improved".
func TestCompressPromptLeavesTechnicalInstructionsAlone(t *testing.T) {
	raw := "Run go test ./...\nThen commit the change."
	if got := CompressPrompt(raw); got != raw {
		t.Fatalf("CompressPrompt() = %q, want the original %q", got, raw)
	}
}

// An all-filler prompt compresses to nothing. The scratch caller, not this
// formatter, decides whether an empty payload is acceptable.
func TestCompressPromptOnlyFluffReturnsEmpty(t *testing.T) {
	raw := "Hello,\nPlease make sure to\nThank you in advance."
	if got := CompressPrompt(raw); got != "" {
		t.Fatalf("CompressPrompt(%q) = %q, want empty", raw, got)
	}
}

// An unclosed fence still marks the rest of the prompt as code; guessing where
// it ended would rewrite the user's evidence.
func TestCompressPromptPreservesUnclosedCodeFence(t *testing.T) {
	raw := "Please keep this:\n\n```go\nplease  keep  this\n"
	got := CompressPrompt(raw)
	if !strings.Contains(got, "```go\nplease  keep  this") {
		t.Fatalf("unclosed code block was rewritten:\n%s", got)
	}
}

// Markdown's four-space rule has no fence, but it is still code. Blank lines
// inside the block are code too, even though they carry no indentation.
func TestCompressPromptPreservesIndentedCode(t *testing.T) {
	raw := "Please run:\n\n    first\n\n\n    second\n\nAfter."
	got := CompressPrompt(raw)
	if !strings.Contains(got, "    first\n\n\n    second") {
		t.Fatalf("indented code was rewritten:\n%s", got)
	}
	if !strings.Contains(got, "After.") {
		t.Fatalf("prose after indented code was lost:\n%s", got)
	}
}

// Rules and blank-line runs are decoration; the prose around them survives.
func TestCompressPromptRemovesRulesAndExtraBlankLines(t *testing.T) {
	raw := "Intro\n\n---\n\n\n\nOutro"
	want := "Intro\n\nOutro"
	if got := CompressPrompt(raw); got != want {
		t.Fatalf("CompressPrompt() = %q, want %q", got, want)
	}
}

// Whitespace in prose is compacted, including spaces before punctuation, while
// inline code keeps its internal spacing.
func TestCompressPromptCompactsProseWhitespace(t *testing.T) {
	raw := "Run   this\tnow , with `a  b` ."
	want := "Run this now, with `a  b`."
	if got := CompressPrompt(raw); got != want {
		t.Fatalf("CompressPrompt() = %q, want %q", got, want)
	}
}
