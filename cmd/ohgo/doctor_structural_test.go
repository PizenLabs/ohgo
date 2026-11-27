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
	report := structuralReport(nil)
	if report.Name != "structural" {
		t.Fatalf("report = %+v", report)
	}
	if len(ast.DefaultAdapters()) > 1 {
		if report.Status != doctorStatusOK {
			t.Fatalf("multi-language status = %q, want OK", report.Status)
		}
		if !strings.Contains(report.Verbose, "typescript") {
			t.Fatalf("verbose detail = %q, want TypeScript named", report.Verbose)
		}
		return
	}

	// The portable build is only a fallback when the workspace actually
	// contains a language this binary cannot parse.
	report = structuralReport([]ast.LanguageCoverage{
		{Language: ast.LanguageGo, Files: 10, Supported: true},
	})
	if report.Status != doctorStatusOK {
		t.Fatalf("Go-only workspace status = %q, want OK", report.Status)
	}
	report = structuralReport([]ast.LanguageCoverage{
		{Language: ast.LanguageGo, Files: 10, Supported: true},
		{Language: ast.LanguageTypeScript, Files: 2},
	})
	if report.Status != doctorStatusFallback {
		t.Fatalf("mixed workspace status = %q, want FALLBACK", report.Status)
	}
	if !strings.Contains(report.Verbose, "make build") {
		t.Fatalf("verbose detail = %q, want the rebuild command", report.Verbose)
	}
}

// Ripgrep is optional. Doctor must report which scanner is active and must not
// mark a supported native fallback as a failed check.
func TestDoctorReportsGrepBackend(t *testing.T) {
	host := doctorHost{
		GOOS:   "darwin",
		Lookup: fakeLookPath(map[string]string{"rg": "/usr/local/bin/rg"}),
	}
	found := searchReport(host)
	if found.Name != "grep" || found.Status != doctorStatusOK {
		t.Fatalf("found report = %+v", found)
	}
	if !strings.Contains(found.Detail, "/usr/local/bin/rg") {
		t.Errorf("detail = %q, want the resolved path", found.Detail)
	}
	if len(found.Remediations) != 0 {
		t.Errorf("installed ripgrep attached a remediation: %+v", found.Remediations)
	}

	missing := searchReport(doctorHost{
		GOOS:   "linux",
		Lookup: fakeLookPath(map[string]string{"apt": "/usr/bin/apt"}),
	})
	if missing.Name != "grep" || missing.Status != doctorStatusFallback {
		t.Fatalf("missing report = %+v, want a fallback check", missing)
	}
	for _, want := range []string{"native scanner", "ripgrep"} {
		if !strings.Contains(missing.Detail, want) {
			t.Errorf("missing detail = %q, want %q", missing.Detail, want)
		}
	}
	for _, want := range []string{"native scanner", "CGO"} {
		if !strings.Contains(missing.Verbose, want) {
			t.Errorf("missing verbose = %q, want %q", missing.Verbose, want)
		}
	}
	if len(missing.Remediations) != 1 || missing.Remediations[0].Detail != "sudo apt install ripgrep" {
		t.Fatalf("remediations = %+v, want the host command", missing.Remediations)
	}
}
