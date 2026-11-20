package main

import (
	"strings"
	"testing"
)

// Self-update installs the portable release. On a multi-language binary that
// silently removes every non-Go adapter, so it must refuse rather than report
// a successful version update.
func TestPortableUpdateRefusalProtectsMultiLanguageBuilds(t *testing.T) {
	refusal := portableUpdateRefusal(true, false)
	if refusal == "" {
		t.Fatal("multi-language update was not refused")
	}
	for _, want := range []string{"multi-language", "portable", "make build-multilang"} {
		if !strings.Contains(refusal, want) {
			t.Fatalf("refusal = %q, missing %q", refusal, want)
		}
	}
	if portableUpdateRefusal(true, true) != "" {
		t.Fatal("--check should remain available on a multi-language build")
	}
	if portableUpdateRefusal(false, false) != "" {
		t.Fatal("a portable build must still self-update")
	}
}
