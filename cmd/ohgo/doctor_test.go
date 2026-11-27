package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
)

// Only ERROR blocks a run. FALLBACK, OPTIONAL, and WARN are honest states, not
// failures that should turn a healthy optional configuration into a non-zero
// exit.
func TestDoctorExitStatusTaxonomy(t *testing.T) {
	nonFatal := []doctorReport{
		{Name: "ok", Status: doctorStatusOK},
		{Name: "fallback", Status: doctorStatusFallback},
		{Name: "optional", Status: doctorStatusOptional},
		{Name: "warn", Status: doctorStatusWarn},
	}
	if err := doctorFailure(nonFatal); err != nil {
		t.Fatalf("non-fatal statuses returned %v", err)
	}
	if got := countFailed(nonFatal); got != 0 {
		t.Fatalf("countFailed = %d, want 0", got)
	}

	fatal := append(append([]doctorReport(nil), nonFatal...), doctorReport{Name: "error", Status: doctorStatusError})
	err := doctorFailure(fatal)
	if err == nil || !strings.Contains(err.Error(), "1 of 5 checks failed") {
		t.Fatalf("doctorFailure = %v, want one failed check", err)
	}
}

// The default table must be grouped, aligned, and compact; --verbose carries
// the full lists without making the normal view wrap.
func TestDoctorRendersGroupedSectionsAndVerboseDetail(t *testing.T) {
	reports := []doctorReport{
		{Section: doctorSectionProvider, Name: "model", Status: doctorStatusOK, Detail: "anthropic/model", Verbose: "anthropic/model (200k ctx)"},
		{Section: doctorSectionWorkspace, Name: "config", Status: doctorStatusWarn, Detail: "malformed config; defaults in effect"},
		{Section: doctorSectionIntelligence, Name: "grep", Status: doctorStatusFallback,
			Detail:       "native scanner (ripgrep not found)",
			Remediations: []doctorRemediation{{Name: "ripgrep", Detail: "brew install ripgrep"}}},
	}

	var compact strings.Builder
	renderDoctorText(&compact, reports, false)
	text := compact.String()
	order := []string{
		string(doctorSectionWorkspace),
		string(doctorSectionProvider),
		string(doctorSectionIntelligence),
		string(doctorSectionRecommendations),
	}
	previous := -1
	for _, section := range order {
		index := strings.Index(text, section)
		if index < 0 {
			t.Fatalf("output omitted section %q:\n%s", section, text)
		}
		if index <= previous {
			t.Fatalf("section %q is out of order:\n%s", section, text)
		}
		previous = index
	}
	for _, status := range []string{"WARN", "OK", "FALLBACK", "OPTIONAL"} {
		if !strings.Contains(text, status) {
			t.Errorf("output omitted status %q:\n%s", status, text)
		}
	}
	if strings.Contains(text, "200k ctx") {
		t.Errorf("default output printed verbose detail:\n%s", text)
	}
	if !strings.Contains(text, "brew install ripgrep") {
		t.Errorf("output omitted the recommendation:\n%s", text)
	}

	var verbose strings.Builder
	renderDoctorText(&verbose, reports, true)
	if !strings.Contains(verbose.String(), "200k ctx") {
		t.Errorf("verbose output omitted full detail:\n%s", verbose.String())
	}
}

// --json is the deterministic machine contract: ordered sections, explicit
// statuses, full detail, and a non-failing ok flag for warnings/fallbacks.
func TestDoctorJSONIsGroupedAndDeterministic(t *testing.T) {
	reports := []doctorReport{
		{Section: doctorSectionWorkspace, Name: "workdir", Status: doctorStatusOK, Detail: "/work"},
		{Section: doctorSectionProvider, Name: "endpoint", Status: doctorStatusWarn,
			Detail: "probe delayed", Verbose: "probe delayed after 15s"},
		{Section: doctorSectionIntelligence, Name: "semantic", Status: doctorStatusFallback,
			Detail:       "0/1 workspace language(s) served; missing: go",
			Remediations: []doctorRemediation{{Name: "gopls", Detail: "go install golang.org/x/tools/gopls@latest"}}},
	}

	var first bytes.Buffer
	if err := renderDoctorJSON(&first, reports); err != nil {
		t.Fatalf("renderDoctorJSON: %v", err)
	}
	var second bytes.Buffer
	if err := renderDoctorJSON(&second, reports); err != nil {
		t.Fatalf("renderDoctorJSON again: %v", err)
	}
	if first.String() != second.String() {
		t.Fatalf("JSON is not deterministic:\n%s\n---\n%s", first.String(), second.String())
	}

	var document doctorJSON
	if err := json.Unmarshal(first.Bytes(), &document); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, first.String())
	}
	if !document.OK || document.Failed != 0 || document.Warnings != 1 {
		t.Fatalf("document header = %+v", document)
	}
	if len(document.Sections) != 4 {
		t.Fatalf("sections = %d, want 4", len(document.Sections))
	}
	if document.Sections[0].Name != string(doctorSectionWorkspace) ||
		document.Sections[3].Name != string(doctorSectionRecommendations) {
		t.Fatalf("section order = %+v", document.Sections)
	}
	if got := document.Sections[1].Checks[0].Verbose; got != "probe delayed after 15s" {
		t.Fatalf("verbose detail = %q", got)
	}
	if got := document.Sections[3].Checks[0]; got.Name != "gopls" || got.Status != doctorStatusOptional {
		t.Fatalf("recommendation = %+v", got)
	}
}

// Install guidance follows the host and the package managers actually
// available, and never invents a package manager.
func TestDoctorInstallHintsFollowHost(t *testing.T) {
	host := func(goos string, commands ...string) doctorHost {
		found := make(map[string]string, len(commands))
		for _, command := range commands {
			found[command] = "/bin/" + command
		}
		return doctorHost{GOOS: goos, Lookup: fakeLookPath(found)}
	}
	cases := []struct {
		name string
		host doctorHost
		want string
	}{
		{name: "macOS Homebrew", host: host("darwin", "brew"), want: "brew install ripgrep"},
		{name: "macOS MacPorts", host: host("darwin", "port"), want: "sudo port install ripgrep"},
		{name: "Debian", host: host("linux", "apt"), want: "sudo apt install ripgrep"},
		{name: "Fedora", host: host("linux", "dnf"), want: "sudo dnf install ripgrep"},
		{name: "Arch", host: host("linux", "pacman"), want: "sudo pacman -S ripgrep"},
		{name: "Alpine", host: host("linux", "apk"), want: "sudo apk add ripgrep"},
		{name: "Nix", host: host("linux", "nix"), want: "nix profile install nixpkgs#ripgrep"},
		{name: "Windows winget", host: host("windows", "winget"), want: "winget install BurntSushi.ripgrep.MSVC"},
		{name: "Windows scoop", host: host("windows", "scoop"), want: "scoop install ripgrep"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := ripgrepInstallHint(test.host); got != test.want {
				t.Fatalf("hint = %q, want %q", got, test.want)
			}
		})
	}

	unknown := ripgrepInstallHint(host("plan9"))
	if !strings.Contains(unknown, "ripgrep") || !strings.Contains(unknown, "https://") {
		t.Fatalf("unknown-platform hint = %q, want the upstream install reference", unknown)
	}
}

// Recommendations are scoped to languages the workspace actually contains: a
// Go repository must not be told to install jdtls or pyright, and a Go-only
// repository with gopls must produce no recommendation at all.
func TestSemanticReportScopesRecommendationsToWorkspace(t *testing.T) {
	goOnly := []ast.LanguageCoverage{{Language: ast.LanguageGo, Files: 12, Supported: true}}
	mixed := []ast.LanguageCoverage{
		{Language: ast.LanguageGo, Files: 12, Supported: true},
		{Language: ast.LanguageTypeScript, Files: 4, Supported: true},
	}

	withGopls := doctorHost{GOOS: "linux", Lookup: fakeLookPath(map[string]string{"gopls": "/bin/gopls"})}
	served := semanticReport(withGopls, goOnly, nil)
	if served.Status != doctorStatusOK || len(served.Remediations) != 0 {
		t.Fatalf("served report = %+v, want OK with no remediation", served)
	}

	missingGo := semanticReport(doctorHost{GOOS: "linux", Lookup: fakeLookPath(nil)}, goOnly, nil)
	if missingGo.Status != doctorStatusFallback || len(missingGo.Remediations) != 1 ||
		missingGo.Remediations[0].Name != "gopls" {
		t.Fatalf("missing Go report = %+v, want one gopls fallback", missingGo)
	}

	partial := semanticReport(withGopls, mixed, nil)
	if partial.Status != doctorStatusFallback {
		t.Fatalf("mixed report status = %q, want FALLBACK", partial.Status)
	}
	if len(partial.Remediations) != 1 || partial.Remediations[0].Name != "typescript" {
		t.Fatalf("mixed remediations = %+v, want TypeScript only", partial.Remediations)
	}
	if !strings.Contains(partial.Detail, "missing: typescript") {
		t.Fatalf("mixed detail = %q, want the missing language named", partial.Detail)
	}

	// A server for an unrelated language is not a reason to recommend that
	// language, and it does not satisfy a Go workspace.
	jdtlsOnly := semanticReport(doctorHost{
		GOOS: "linux", Lookup: fakeLookPath(map[string]string{"jdtls": "/bin/jdtls"}),
	}, goOnly, nil)
	for _, remediation := range jdtlsOnly.Remediations {
		if remediation.Name == "jdtls" {
			t.Fatalf("pure Go workspace recommended jdtls: %+v", jdtlsOnly)
		}
	}

	empty := semanticReport(doctorHost{
		GOOS: "linux", Lookup: fakeLookPath(map[string]string{"jdtls": "/bin/jdtls"}),
	}, nil, nil)
	if empty.Status != doctorStatusOptional || len(empty.Remediations) != 0 {
		t.Fatalf("empty workspace report = %+v, want OPTIONAL with no remediation", empty)
	}
}

// The workspace inventory uses the same structural scope production tools use,
// so dependency output cannot trigger a recommendation.
func TestWorkspaceLanguagesUseStructuralScope(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                       "module example.com/test\n\ngo 1.27\n",
		"main.go":                      "package main\n\nfunc main() {}\n",
		"web/app.ts":                   "export const value = 1;\n",
		"node_modules/pkg/index.js":    "export const ignored = true;\n",
		"vendor/example/dependency.go": "package example\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	languages, err := workspaceLanguages(root)
	if err != nil {
		t.Fatalf("workspaceLanguages: %v", err)
	}
	byLanguage := map[ast.Language]ast.LanguageCoverage{}
	for _, coverage := range languages {
		byLanguage[coverage.Language] = coverage
	}
	if byLanguage[ast.LanguageGo].Files < 1 {
		t.Fatalf("languages = %+v, want Go inventory", languages)
	}
	if byLanguage[ast.LanguageTypeScript].Files != 1 {
		t.Fatalf("languages = %+v, want one TypeScript file", languages)
	}
	if _, ok := byLanguage[ast.LanguageJavaScript]; ok {
		t.Fatalf("node_modules leaked into the inventory: %+v", languages)
	}
}
