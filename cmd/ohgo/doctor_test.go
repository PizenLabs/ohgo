package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
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

	diagnosis := doctorDiagnosis{Reports: reports}
	view := doctorView{JSON: true}

	var first bytes.Buffer
	if err := renderDoctorJSON(&first, diagnosis, view); err != nil {
		t.Fatalf("renderDoctorJSON: %v", err)
	}
	var second bytes.Buffer
	if err := renderDoctorJSON(&second, diagnosis, view); err != nil {
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

// The tools drill-down must be a projection of the live registry, not a
// hand-maintained list: every registered tool appears, and its group, safety,
// backend, and purpose come from the tool's own metadata.
func TestDoctorToolDetailReflectsLiveRegistry(t *testing.T) {
	root := t.TempDir()
	source := tools.NewStructuralSource(root)
	live := []agent.Tool{
		tools.NewReadTool(root),
		tools.NewWriteTool(root),
		tools.NewGrepTool(root),
		tools.NewBashTool(root),
		tools.NewSymbolsToolWithSource(source),
		tools.NewSearchTool(nil),
		tools.NewMCPListTool(nil),
		tools.NewTodoTool(),
	}
	details := collectToolDetails(live)
	if len(details) != len(live) {
		t.Fatalf("details = %d, want %d live tools", len(details), len(live))
	}
	byName := make(map[string]doctorTool, len(details))
	for _, detail := range details {
		byName[detail.Name] = detail
		if strings.TrimSpace(detail.Purpose) == "" || strings.Contains(detail.Purpose, "\n") {
			t.Errorf("tool %q purpose = %q, want one non-empty line", detail.Name, detail.Purpose)
		}
		if len([]rune(detail.Purpose)) > 113 {
			t.Errorf("tool %q purpose is not compact: %q", detail.Name, detail.Purpose)
		}
	}
	for _, tool := range live {
		if _, ok := byName[tool.Name()]; !ok {
			t.Errorf("registered tool %q missing from --tools detail", tool.Name())
		}
	}

	checks := []struct {
		name, group, safety, source, backend string
	}{
		{"read", string(tools.ToolGroupFileOperations), "read-only", "built-in", "built-in"},
		{"write", string(tools.ToolGroupFileOperations), "mutating", "built-in", "built-in"},
		{"grep", string(tools.ToolGroupSearchDiscovery), "read-only", "built-in", ""},
		{"bash", string(tools.ToolGroupExecutionVerification), "shell, mutating", "built-in", "built-in"},
		{"symbols", string(tools.ToolGroupCodeIntelligence), "read-only", "built-in", "built-in"},
		{"search", string(tools.ToolGroupWorkflowNetwork), "read-only", "external", "external.search"},
		{"mcp_list", string(tools.ToolGroupWorkflowNetwork), "read-only", "mcp", "external.mcp.read"},
		{"todo", string(tools.ToolGroupWorkflowNetwork), "read-only", "built-in", "built-in"},
	}
	for _, check := range checks {
		got, ok := byName[check.name]
		if !ok {
			t.Errorf("missing detail for %q", check.name)
			continue
		}
		if got.Group != check.group {
			t.Errorf("%s group = %q, want %q", check.name, got.Group, check.group)
		}
		if got.Safety != check.safety {
			t.Errorf("%s safety = %q, want %q", check.name, got.Safety, check.safety)
		}
		if got.Source != check.source {
			t.Errorf("%s source = %q, want %q", check.name, got.Source, check.source)
		}
		if check.backend != "" && got.Backend != check.backend {
			t.Errorf("%s backend = %q, want %q", check.name, got.Backend, check.backend)
		}
	}
	grep := byName["grep"]
	if grep.Backend != "native" && !strings.HasPrefix(grep.Backend, "rg (") {
		t.Errorf("grep backend = %q, want native or the resolved rg path", grep.Backend)
	}
}

// --json exposes the same structured detail arrays only when requested.
func TestDoctorJSONIncludesRequestedDrillDowns(t *testing.T) {
	diagnosis := doctorDiagnosis{
		Reports: []doctorReport{{
			Section: doctorSectionIntelligence, Name: "tools",
			Status: doctorStatusOK, Detail: "1 tools registered",
		}},
		Tools: []doctorTool{{
			Name: "grep", Group: string(tools.ToolGroupSearchDiscovery),
			Safety: "read-only", Backend: "native", Source: "built-in",
			Purpose: "Search file contents.",
		}},
		Intelligence: doctorIntelligence{
			Adapters: []doctorAdapterDetail{{
				Name: "go", Languages: []string{"go"},
				Extensions: []string{".go"}, Capabilities: []string{"symbols"},
			}},
			Languages: []doctorLanguageMapping{{
				Language: "go", Files: 2, Structural: "adapter",
				Server: "gopls", Path: "/bin/gopls", Status: "ready",
			}},
			Unsupported: []doctorUnsupportedExtension{{Extension: ".kt", Files: 1}},
		},
	}

	for _, view := range []doctorView{
		{JSON: true, Tools: true},
		{JSON: true, Intelligence: true},
		{JSON: true, Tools: true, Intelligence: true},
	} {
		var buffer bytes.Buffer
		if err := renderDoctorJSON(&buffer, diagnosis, view); err != nil {
			t.Fatalf("renderDoctorJSON(%+v): %v", view, err)
		}
		var document doctorJSON
		if err := json.Unmarshal(buffer.Bytes(), &document); err != nil {
			t.Fatalf("decode JSON: %v", err)
		}
		if view.Tools {
			if document.Tools == nil || len(*document.Tools) != 1 {
				t.Fatalf("tools = %+v, want the requested array", document.Tools)
			}
			if got := (*document.Tools)[0]; got.Group == "" || got.Safety == "" || got.Purpose == "" {
				t.Fatalf("tool detail = %+v, want the structured fields", got)
			}
		} else if document.Tools != nil {
			t.Fatalf("tools appeared without --tools: %+v", document.Tools)
		}
		if view.Intelligence {
			if document.Intelligence == nil || len(document.Intelligence.Adapters) != 1 ||
				len(document.Intelligence.Languages) != 1 || len(document.Intelligence.Unsupported) != 1 {
				t.Fatalf("intelligence = %+v, want the requested detail", document.Intelligence)
			}
		} else if document.Intelligence != nil {
			t.Fatalf("intelligence appeared without --intelligence: %+v", document.Intelligence)
		}
		if document.Tools == nil && document.Intelligence == nil {
			t.Fatal("neither requested drill-down appeared")
		}
	}
}

// Intelligence maps detected languages to the binary that will serve them and
// reports unsupported extensions without inventing a server for a language the
// workspace does not contain.
func TestDoctorIntelligenceMapsWorkspaceLanguages(t *testing.T) {
	inventory := doctorWorkspaceInventory{
		Languages: []ast.LanguageCoverage{
			{Language: ast.LanguageGo, Files: 3, Supported: true},
			{Language: ast.LanguageTypeScript, Files: 2},
			{Language: ast.LanguageTSX, Files: 1},
		},
		Unsupported: []ast.UnsupportedLanguage{
			{Extension: ".tsx", Files: 1},
			{Extension: ".ts", Files: 2},
		},
	}
	host := doctorHost{
		GOOS: "linux",
		Lookup: fakeLookPath(map[string]string{
			"gopls": "/bin/gopls", "jdtls": "/bin/jdtls",
		}),
	}
	detail := collectIntelligence(host, inventory, nil)
	if len(detail.Adapters) == 0 {
		t.Fatal("intelligence omitted every structural adapter")
	}
	var goAdapter *doctorAdapterDetail
	for index := range detail.Adapters {
		if detail.Adapters[index].Name == "go" {
			goAdapter = &detail.Adapters[index]
		}
	}
	if goAdapter == nil || !contains(goAdapter.Extensions, ".go") {
		t.Fatalf("go adapter = %+v, want the .go extension", goAdapter)
	}

	byLanguage := map[string]doctorLanguageMapping{}
	for _, mapping := range detail.Languages {
		byLanguage[mapping.Language] = mapping
	}
	if got := byLanguage["go"]; got.Status != "ready" || got.Server != "gopls" || got.Path != "/bin/gopls" {
		t.Fatalf("go mapping = %+v, want gopls ready at /bin/gopls", got)
	}
	if got := byLanguage["typescript"]; got.Status != "missing" ||
		got.Server != "typescript-language-server" || got.Files != 3 {
		t.Fatalf("typescript mapping = %+v, want the missing preferred server", got)
	}
	for _, mapping := range detail.Languages {
		if mapping.Server == "jdtls" {
			t.Fatalf("unrelated jdtls mapped to workspace language: %+v", mapping)
		}
	}
	if len(detail.Unsupported) != 2 ||
		detail.Unsupported[0].Extension != ".ts" || detail.Unsupported[1].Extension != ".tsx" {
		t.Fatalf("unsupported = %+v, want stable extension order", detail.Unsupported)
	}
}

// The text drill-downs are readable without --verbose and the footer points at
// the targeted selectors only in the compact default view.
func TestDoctorDrillDownTextAndHint(t *testing.T) {
	var toolsOut strings.Builder
	renderDoctorToolDetails(&toolsOut, []doctorTool{{
		Name: "grep", Group: string(tools.ToolGroupSearchDiscovery),
		Safety: "read-only", Source: "built-in",
		Backend: "rg (/opt/homebrew/bin/rg)", Purpose: "Search file contents.",
	}})
	for _, want := range []string{"Registered Tools (1)", "Search & Discovery", "grep", "read-only", "rg (/opt/homebrew/bin/rg)", "Search file contents."} {
		if !strings.Contains(toolsOut.String(), want) {
			t.Errorf("tools detail omitted %q:\n%s", want, toolsOut.String())
		}
	}

	var intelligenceOut strings.Builder
	renderDoctorIntelligenceDetails(&intelligenceOut, doctorIntelligence{
		Adapters: []doctorAdapterDetail{{
			Name: "go", Languages: []string{"go"},
			Extensions: []string{".go"}, Capabilities: []string{"symbols"},
		}},
		Languages: []doctorLanguageMapping{{
			Language: "go", Files: 2, Structural: "adapter",
			Server: "gopls", Path: "/bin/gopls", Status: "ready",
		}},
		Unsupported: []doctorUnsupportedExtension{{Extension: ".kt", Files: 1}},
	})
	for _, want := range []string{"Code Intelligence Detail", "Structural Adapters (1)", "go", "Workspace Languages (1)", "gopls", "ready", "Unsupported Extensions (1)", ".kt"} {
		if !strings.Contains(intelligenceOut.String(), want) {
			t.Errorf("intelligence detail omitted %q:\n%s", want, intelligenceOut.String())
		}
	}

	var hint strings.Builder
	renderDoctorHint(&hint)
	for _, want := range []string{"hint:", "--tools", "--intelligence", "--verbose"} {
		if !strings.Contains(hint.String(), want) {
			t.Errorf("hint omitted %q:\n%s", want, hint.String())
		}
	}
	if !(doctorView{}).compact() {
		t.Fatal("the default view is not compact")
	}
	for _, view := range []doctorView{{Tools: true}, {Intelligence: true}, {Verbose: true}, {JSON: true}} {
		if view.compact() {
			t.Fatalf("view %+v reported compact", view)
		}
	}
}

func TestParseDoctorViewArgs(t *testing.T) {
	view, err := parseDoctorViewArgs([]string{"--tools", "--lsp", "--verbose", "--json"})
	if err != nil {
		t.Fatalf("parseDoctorViewArgs: %v", err)
	}
	if !view.Tools || !view.Intelligence || !view.Verbose || !view.JSON {
		t.Fatalf("view = %+v, want every selector", view)
	}
	if _, err := parseDoctorViewArgs([]string{"--nope"}); err == nil {
		t.Fatal("unknown option parsed successfully")
	}

	cmd := doctorCmd()
	for _, flag := range []string{"tools", "intelligence", "lsp", "verbose", "json"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("doctor is missing --%s", flag)
		}
	}
	for flag, value := range map[string]string{"tools": "true", "lsp": "true", "verbose": "true", "json": "true"} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	cliView := doctorViewFromCmd(cmd)
	if !cliView.Tools || !cliView.Intelligence || !cliView.Verbose || !cliView.JSON {
		t.Fatalf("CLI view = %+v, want every selector", cliView)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
