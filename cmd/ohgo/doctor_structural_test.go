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
