package main

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// The active binary's adapter set is a build fact. Doctor must report it so a
// portable install and a multi-language install no longer diagnose
// identically.
func TestDoctorReportsStructuralAdapters(t *testing.T) {
	report := structuralReport()
	if report.Name != "structural" || !report.OK {
		t.Fatalf("report = %+v", report)
	}
	detail := strings.ToLower(report.Detail)
	if !strings.Contains(detail, "adapter") {
		t.Fatalf("detail = %q, want the adapter count", report.Detail)
	}
	if len(ast.DefaultAdapters()) > 1 {
		if !strings.Contains(detail, "typescript") {
			t.Fatalf("multi-language detail = %q, want TypeScript named", report.Detail)
		}
	} else {
		if !strings.Contains(detail, "go") {
			t.Fatalf("portable detail = %q, want the Go-only limitation", report.Detail)
		}
		// A limitation without the remedy leaves the user unable to change
		// the build that produced it.
		if !strings.Contains(report.Detail, "make build") {
			t.Fatalf("portable detail = %q, want the rebuild command", report.Detail)
		}
	}
}

// Ripgrep is optional. Doctor must report which scanner is active and must not
// mark a supported native fallback as a failed check.
func TestDoctorReportsGrepBackend(t *testing.T) {
	found := searchReport(fakeLookPath(map[string]string{"rg": "/usr/local/bin/rg"}))
	if found.Name != "grep" || !found.OK {
		t.Fatalf("found report = %+v", found)
	}
	if !strings.Contains(found.Detail, "/usr/local/bin/rg") {
		t.Errorf("detail = %q, want the resolved path", found.Detail)
	}
	if !strings.Contains(strings.ToLower(found.Detail), "fallback") {
		t.Errorf("detail = %q, want the fallback named", found.Detail)
	}

	missing := searchReport(fakeLookPath(nil))
	if missing.Name != "grep" || !missing.OK {
		t.Fatalf("missing report = %+v, want an optional check that stays OK", missing)
	}
	for _, want := range []string{"native scanner", "install ripgrep", "CGO"} {
		if !strings.Contains(missing.Detail, want) {
			t.Errorf("missing detail = %q, want %q", missing.Detail, want)
		}
	}
}
