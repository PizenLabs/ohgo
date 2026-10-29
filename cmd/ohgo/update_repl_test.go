package main

import (
	"bytes"
	"strings"
	"testing"
)

// A piped session still treats /update as the explicit command rather than a
// prompt: the plain REPL crosses the same authority as `ohgo update` and
// reports its refusal without a terminal or a model turn.
func TestREPLUpdateIsExplicitAndUsesTheSharedAuthority(t *testing.T) {
	// Pin the build identity so the authority refuses before any network
	// access, regardless of how the test binary was linked.
	original := version
	version = "0.0.0-dev"
	t.Cleanup(func() { version = original })

	var out bytes.Buffer
	if err := repl(&harness{}, &out, strings.NewReader("/update\n/quit\n"), nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "Current version:") {
		t.Fatalf("output = %q, want the update operation's own output", text)
	}
	if !strings.Contains(text, "development version") {
		t.Fatalf("output = %q, want the authority's refusal", text)
	}
	if strings.Contains(text, "unknown command") {
		t.Fatalf("output = %q, /update was not recognised", text)
	}
}
