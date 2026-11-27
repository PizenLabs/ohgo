package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/semantic"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
	"github.com/spf13/cobra"
)

// doctorCmd checks the local setup and reports what would stop a run.
//
// Every check names its own fix: a diagnostic that says "broken" without saying
// what to do about it is a second error message, not a diagnosis.
func doctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the local setup",
		Long: `Check the working directory, configuration, model catalog, credentials,
session store, tool set, and local accelerators, and report what would stop a run.

Checks are grouped by domain and carry one status:

  OK        healthy, with the preferred backend
  FALLBACK  working through a built-in or optional fallback
  OPTIONAL  an optional capability is intentionally unconfigured
  WARN      actionable, but it does not block a run
  ERROR     a run would fail; the detail names the fix

A missing API key for the default provider is the common failure: the fix is
printed with the check, e.g. "export ANTHROPIC_API_KEY=...". Ripgrep and
language servers are optional accelerators; Recommendations names the install
command for this operating system when one is missing. Only ERROR sets a
non-zero exit status.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd)
		},
	}
	cmd.Flags().Bool("json", false, "emit deterministic machine-readable diagnostics")
	cmd.Flags().BoolP("verbose", "v", false, "show full adapter, server, and tool lists")
	return cmd
}

// doctorStatus is the severity taxonomy of one check.
type doctorStatus string

const (
	// doctorStatusOK is healthy, with the preferred backend.
	doctorStatusOK doctorStatus = "OK"
	// doctorStatusFallback is functional through a built-in or optional
	// fallback, such as the native scanner without ripgrep.
	doctorStatusFallback doctorStatus = "FALLBACK"
	// doctorStatusOptional is an optional capability left unconfigured.
	doctorStatusOptional doctorStatus = "OPTIONAL"
	// doctorStatusWarn is actionable but non-fatal.
	doctorStatusWarn doctorStatus = "WARN"
	// doctorStatusError blocks a run and sets a non-zero exit status.
	doctorStatusError doctorStatus = "ERROR"
)

// doctorSection groups checks by operational domain.
type doctorSection string

const (
	doctorSectionWorkspace       doctorSection = "Workspace & Storage"
	doctorSectionProvider        doctorSection = "Model & Provider"
	doctorSectionIntelligence    doctorSection = "Code Intelligence & Tools"
	doctorSectionRecommendations doctorSection = "Recommendations"
)

// doctorSectionOrder is the stable display order. Adding a section means
// deciding where it belongs, not appending it wherever the caller happens to
// run.
var doctorSectionOrder = []doctorSection{
	doctorSectionWorkspace,
	doctorSectionProvider,
	doctorSectionIntelligence,
	doctorSectionRecommendations,
}

// doctorRemediation is one OS-aware install instruction. It is emitted under
// Recommendations only when the accelerator it names is missing.
type doctorRemediation struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// doctorReport is one check result, kept so tests assert on structure rather
// than on formatted text.
type doctorReport struct {
	// Section groups the check into an operational domain.
	Section doctorSection
	// Name is the check, e.g. "config".
	Name string
	// Status is the severity taxonomy.
	Status doctorStatus
	// Detail is the concise human-readable outcome or fix.
	Detail string
	// Verbose is the full list or explanation, shown with --verbose.
	Verbose string
	// Remediations are optional install instructions attached to this check.
	Remediations []doctorRemediation
}

// detail returns the concise line, or the full line when verbose is set.
func (r doctorReport) detail(verbose bool) string {
	if verbose && strings.TrimSpace(r.Verbose) != "" {
		return r.Verbose
	}
	return r.Detail
}

// doctorHost is the platform and binary-resolution seam for install guidance.
type doctorHost struct {
	GOOS   string
	Lookup lookPathFunc
}

// defaultDoctorHost uses the real OS and the shared binary resolver.
func defaultDoctorHost() doctorHost {
	return doctorHost{GOOS: runtime.GOOS, Lookup: tools.LookPath}
}

func (h doctorHost) goos() string {
	if strings.TrimSpace(h.GOOS) == "" {
		return runtime.GOOS
	}
	return h.GOOS
}

func (h doctorHost) lookPath() lookPathFunc {
	if h.Lookup != nil {
		return h.Lookup
	}
	return tools.LookPath
}

// has reports whether command is installed. It uses the same resolver as the
// accelerators, so a GUI process sees a package manager outside its PATH too.
func (h doctorHost) has(command string) bool {
	_, err := h.lookPath()(command)
	return err == nil
}

func runDoctor(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	reports := collectDoctorReports(cmd)

	jsonOut, _ := cmd.Flags().GetBool("json")
	verbose, _ := cmd.Flags().GetBool("verbose")
	if jsonOut {
		if err := renderDoctorJSON(out, reports); err != nil {
			return err
		}
	} else {
		renderDoctorText(out, reports, verbose)
	}
	return doctorFailure(reports)
}

// doctorFailure is the exit contract: OK, FALLBACK, OPTIONAL, and WARN are
// non-failing; only ERROR sets a non-zero status.
func doctorFailure(reports []doctorReport) error {
	failed := countFailed(reports)
	if failed == 0 {
		return nil
	}
	return fmt.Errorf("ohgo doctor: %d of %d checks failed", failed, len(reports))
}

func countFailed(reports []doctorReport) int {
	n := 0
	for _, rep := range reports {
		if rep.Status == doctorStatusError {
			n++
		}
	}
	return n
}

func countWarnings(reports []doctorReport) int {
	n := 0
	for _, rep := range reports {
		if rep.Status == doctorStatusWarn {
			n++
		}
	}
	return n
}

// collectDoctorReports runs every check. It is split from runDoctor so tests
// assert on the reports without parsing formatted output.
func collectDoctorReports(cmd *cobra.Command) []doctorReport {
	return collectReports(flagValue(cmd, "model"), ai.NewRegistry())
}

// collectReports runs every check from explicit inputs so the harness — and
// therefore /doctor in both interfaces — uses the same implementation as the
// CLI. One diagnostic path, not two that can disagree.
func collectReports(modelFlag string, registry *ai.Registry) []doctorReport {
	var reports []doctorReport
	add := func(section doctorSection, name string, status doctorStatus, detail, verbose string) {
		reports = append(reports, doctorReport{
			Section: section, Name: name, Status: status, Detail: detail, Verbose: verbose,
		})
	}

	cwd, err := os.Getwd()
	if err != nil {
		add(doctorSectionWorkspace, "workdir", doctorStatusError,
			fmt.Sprintf("cannot locate working directory: %v", err), "")
		cwd = "."
	} else {
		add(doctorSectionWorkspace, "workdir", doctorStatusOK, cwd, "")
	}

	settings, err := config.Load(cwd)
	switch {
	case errors.Is(err, config.ErrNotFound):
		settings = config.Default()
		add(doctorSectionWorkspace, "config", doctorStatusOK,
			"defaults in effect (no settings file)", "")
	case err != nil:
		settings = config.Default()
		add(doctorSectionWorkspace, "config", doctorStatusWarn,
			"malformed config; defaults in effect", err.Error()+"; fix ~/.ohgo/settings.json")
	default:
		add(doctorSectionWorkspace, "config", doctorStatusOK, "settings loaded", "")
	}

	path, err := session.DefaultPath()
	if err != nil {
		add(doctorSectionWorkspace, "sessions", doctorStatusError,
			fmt.Sprintf("cannot locate session store: %v", err), "")
	} else if store, err := session.Open(path); err != nil {
		add(doctorSectionWorkspace, "sessions", doctorStatusError,
			fmt.Sprintf("cannot open %s: %v", path, err), "")
	} else {
		list, listErr := store.List(1)
		_ = store.Close()
		if listErr != nil {
			add(doctorSectionWorkspace, "sessions", doctorStatusError,
				fmt.Sprintf("store unreadable: %v", listErr), "")
		} else {
			add(doctorSectionWorkspace, "sessions", doctorStatusOK,
				fmt.Sprintf("%s (%d+ stored sessions visible)", path, len(list)), "")
		}
	}

	providers := catalog.ProviderNames()
	models := catalog.List()
	if len(providers) == 0 || len(models) == 0 {
		add(doctorSectionProvider, "catalog", doctorStatusError,
			"embedded catalog is empty; reinstall ohgo", "")
	} else {
		add(doctorSectionProvider, "catalog", doctorStatusOK,
			fmt.Sprintf("%d models across %d providers", len(models), len(providers)), "")
	}

	// Endpoint configuration is optional; a malformed entry is reported but
	// does not hide healthy providers behind it.
	if warnings := registerEndpoints(registry, settings); len(warnings) > 0 {
		add(doctorSectionProvider, "endpoints", doctorStatusWarn,
			fmt.Sprintf("%d endpoint config warning(s)", len(warnings)),
			strings.Join(warnings, "; ")+"; fix ~/.ohgo/settings.json")
	} else if len(settings.Endpoints) > 0 {
		add(doctorSectionProvider, "endpoints", doctorStatusOK,
			fmt.Sprintf("%d configured endpoint(s)", len(settings.Endpoints)), "")
	} else {
		add(doctorSectionProvider, "endpoints", doctorStatusOptional,
			"no custom endpoints configured", "")
	}

	model, modelErr := resolveModel(settings, modelFlag, os.Getenv("OHGO_MODEL"), dynamicResolver(context.Background(), registry))
	if modelErr != nil {
		add(doctorSectionProvider, "model", doctorStatusError, modelErr.Error(), "")
	} else {
		add(doctorSectionProvider, "model", doctorStatusOK,
			fmt.Sprintf("%s/%s (%s ctx)", model.Provider, model.ID, formatInt(model.ContextWindow)), "")
	}

	if modelErr == nil {
		if _, keyErr := registry.ResolveAPIKey(model.Provider); keyErr != nil {
			add(doctorSectionProvider, "credentials", doctorStatusError,
				fmt.Sprintf("%s/%s: %v", model.Provider, model.ID, keyErr), "")
		} else {
			add(doctorSectionProvider, "credentials", doctorStatusOK,
				fmt.Sprintf("credential present for %s", model.Provider), "")
		}
	} else {
		add(doctorSectionProvider, "credentials", doctorStatusError, "skipped: no model resolved", "")
	}

	// The endpoint probe distinguishes configured from connected: a present
	// credential against a dead endpoint is a different problem with a
	// different fix. Bounded, so a dead network fails the check in seconds.
	if modelErr == nil {
		key, _ := registry.ResolveAPIKey(model.Provider)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		probe := ai.ProbeProvider(ctx, nil, model.API, registry.EndpointFor(model), key)
		cancel()
		status := doctorStatusOK
		if !probe.Reachable || !probe.Authenticated {
			status = doctorStatusError
		}
		add(doctorSectionProvider, "endpoint", status, probe.Detail, "")
	} else {
		add(doctorSectionProvider, "endpoint", doctorStatusError, "skipped: no model resolved", "")
	}

	// One inventory scan serves both the build-capability check and the
	// workspace-scoped LSP recommendations. It is the same structural
	// inventory production tools use, so an ignored directory is not counted
	// here either.
	languages, inventoryErr := workspaceLanguages(cwd)
	reports = append(reports, structuralReport(languages))

	host := defaultDoctorHost()
	reports = append(reports, searchReport(host), semanticReport(host, languages, inventoryErr), runtimeReport())

	opts := options{root: cwd, maxDepth: 1, settings: settings}
	toolSet := buildTools(opts, registry)
	if len(toolSet) == 0 {
		add(doctorSectionIntelligence, "tools", doctorStatusError,
			"no tools built; check --no-tools is not set", "")
	} else {
		names := make([]string, 0, len(toolSet))
		var externalErrors []string
		external := 0
		for _, tool := range toolSet {
			names = append(names, tool.Name())
			switch typed := tool.(type) {
			case *tools.SearchTool:
				external++
				if typed.ConfigErr != "" {
					externalErrors = append(externalErrors, "search: "+typed.ConfigErr)
				}
			case *tools.MCPListTool:
				external++
				if typed.ConfigErr != "" {
					externalErrors = append(externalErrors, "mcp: "+typed.ConfigErr)
				}
			case *tools.MCPReadTool:
				external++
			case *tools.MCPCallTool:
				external++
			}
		}
		sort.Strings(names)
		add(doctorSectionIntelligence, "tools", doctorStatusOK,
			fmt.Sprintf("%d tools registered", len(toolSet)),
			fmt.Sprintf("%d tools: %s", len(toolSet), strings.Join(names, ", ")))
		if len(externalErrors) > 0 {
			add(doctorSectionIntelligence, "external", doctorStatusWarn,
				fmt.Sprintf("%d external capability issue(s)", len(externalErrors)),
				strings.Join(externalErrors, "; ")+"; fix ~/.ohgo/settings.json")
		} else if external > 0 {
			add(doctorSectionIntelligence, "external", doctorStatusOK,
				fmt.Sprintf("%d external capability tool(s) configured", external), "")
		} else {
			add(doctorSectionIntelligence, "external", doctorStatusOptional,
				"no external capability tools configured", "")
		}
	}

	return reports
}

// workspaceLanguages returns the source-language inventory of root. It uses
// the same adaptive structural source production tools use, so the doctor
// recommends servers for the workspace it would actually analyze rather than
// for every language this binary knows.
func workspaceLanguages(root string) ([]ast.LanguageCoverage, error) {
	source := tools.NewStructuralSource(root)
	idx, err := source.Index()
	if err != nil {
		return nil, err
	}
	info := idx.Info()
	out := make([]ast.LanguageCoverage, len(info.Languages))
	copy(out, info.Languages)
	return out, nil
}

// structuralReport names the syntax adapters compiled into this binary.
//
// The portable and multi-language builds are otherwise indistinguishable from
// the inside: both run, both report the same version, and only one can parse
// TypeScript. The doctor must answer "what can this binary see?" directly
// rather than leaving the user to infer it from a failed symbol query.
func structuralReport(languages []ast.LanguageCoverage) doctorReport {
	adapters := ast.DefaultAdapters()
	names := make([]string, 0, len(adapters))
	seen := map[string]bool{}
	for _, adapter := range adapters {
		if seen[adapter.Name()] {
			continue
		}
		seen[adapter.Name()] = true
		names = append(names, adapter.Name())
	}
	sort.Strings(names)

	if len(names) <= 1 {
		var unsupported []string
		for _, coverage := range languages {
			if !coverage.Supported {
				unsupported = append(unsupported, string(coverage.Language))
			}
		}
		sort.Strings(unsupported)
		detail := "Go only (portable build)"
		status := doctorStatusOK
		if len(unsupported) > 0 {
			status = doctorStatusFallback
			detail = fmt.Sprintf("Go only (portable build); %d workspace language(s) unsupported", len(unsupported))
		}
		return doctorReport{
			Section: doctorSectionIntelligence, Name: "structural", Status: status, Detail: detail,
			Verbose: fmt.Sprintf("1 adapter: go; workspace languages without an adapter: %s; rebuild with `make build` (needs a C toolchain) or install the multi-language release",
				compactList(unsupported, 6)),
		}
	}
	return doctorReport{
		Section: doctorSectionIntelligence, Name: "structural", Status: doctorStatusOK,
		Detail:  fmt.Sprintf("%d adapters", len(names)),
		Verbose: fmt.Sprintf("%d adapters: %s", len(names), strings.Join(names, ", ")),
	}
}

// searchReport names the content-search backend. Ripgrep is an optional
// accelerator: the native scanner always works, so absence is a fallback
// rather than a failed check. The install command lives under Recommendations,
// keeping the check line compact.
func searchReport(host doctorHost) doctorReport {
	path, err := host.lookPath()("rg")
	if err != nil || strings.TrimSpace(path) == "" {
		return doctorReport{
			Section: doctorSectionIntelligence, Name: "grep", Status: doctorStatusFallback,
			Detail:  "native scanner (ripgrep not found)",
			Verbose: "ripgrep not found on PATH or the standard install directories; the native scanner remains available and CGO is not required",
			Remediations: []doctorRemediation{
				ripgrepRemediation(host),
			},
		}
	}
	return doctorReport{
		Section: doctorSectionIntelligence, Name: "grep", Status: doctorStatusOK,
		Detail:  fmt.Sprintf("ripgrep (%s)", compactPath(path, 48)),
		Verbose: fmt.Sprintf("ripgrep at %s; content search uses it with the native scanner as fallback", path),
	}
}

// semanticReport names the language servers this machine can launch and
// scopes the recommendation to languages present in the workspace. A missing
// server for an active language is a fallback, not an error: AST evidence
// remains available.
func semanticReport(host doctorHost, languages []ast.LanguageCoverage, inventoryErr error) doctorReport {
	if inventoryErr != nil {
		return doctorReport{
			Section: doctorSectionIntelligence, Name: "semantic", Status: doctorStatusWarn,
			Detail:  "workspace language inventory unavailable",
			Verbose: "workspace language inventory unavailable: " + inventoryErr.Error() + "; language-server recommendations skipped",
		}
	}

	detected := map[string]int{}
	for _, coverage := range languages {
		if coverage.Files <= 0 {
			continue
		}
		language := lspLanguageID(string(coverage.Language))
		if language == "" {
			continue
		}
		detected[language] += coverage.Files
	}
	if len(detected) == 0 {
		return doctorReport{
			Section: doctorSectionIntelligence, Name: "semantic", Status: doctorStatusOptional,
			Detail:  "no workspace source languages detected",
			Verbose: "no supported workspace source languages detected; semantic assistance is optional and AST evidence remains available",
		}
	}

	installed := languageServerCommands(host.lookPath())
	covered := map[string]string{}
	for _, spec := range installed {
		for _, language := range spec.Languages {
			if _, ok := covered[language]; !ok {
				covered[language] = spec.Name
			}
		}
	}

	var served, missing []string
	var remediations []doctorRemediation
	for language := range detected {
		if _, ok := covered[language]; ok {
			served = append(served, language)
			continue
		}
		missing = append(missing, language)
		if definition, ok := languageServerDefinitionFor(language); ok {
			remediations = append(remediations, languageServerRemediation(definition, host))
		}
	}
	sort.Strings(served)
	sort.Strings(missing)

	full := fmt.Sprintf("workspace: %s", languageCounts(detected))
	if len(installed) > 0 {
		full += "; installed: " + strings.Join(languageServerNames(installed), ", ")
	}
	if len(missing) > 0 {
		full += "; missing: " + strings.Join(missing, ", ")
	}

	if len(missing) == 0 {
		return doctorReport{
			Section: doctorSectionIntelligence, Name: "semantic", Status: doctorStatusOK,
			Detail:  fmt.Sprintf("%d workspace language(s) served", len(detected)),
			Verbose: full + "; started lazily on first request",
		}
	}
	return doctorReport{
		Section: doctorSectionIntelligence, Name: "semantic", Status: doctorStatusFallback,
		Detail: fmt.Sprintf("%d/%d workspace language(s) served; missing: %s",
			len(served), len(detected), compactList(missing, 4)),
		Verbose:      full + "; AST evidence remains available",
		Remediations: remediations,
	}
}

// runtimeReport states the DAP boundary explicitly. No debug adapter is
// launched without an authorized session, so absence here is an optional
// capability, never a failed check.
func runtimeReport() doctorReport {
	return doctorReport{
		Section: doctorSectionIntelligence, Name: "runtime", Status: doctorStatusOptional,
		Detail:  "DAP not composed; requires an authorized debug session",
		Verbose: "DAP is composed only for an authorized debug session; runtime evidence is optional and absence is not a structural failure",
	}
}

// languageServerDefinitionFor returns the preferred server serving language.
func languageServerDefinitionFor(language string) (languageServerDefinition, bool) {
	for _, definition := range languageServerDefinitions {
		for _, served := range definition.languages {
			if served == language {
				return definition, true
			}
		}
	}
	return languageServerDefinition{}, false
}

func languageServerNames(specs []semantic.Spec) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		if seen[spec.Name] {
			continue
		}
		seen[spec.Name] = true
		out = append(out, spec.Name)
	}
	sort.Strings(out)
	return out
}

// languageCounts renders "go (12 files), typescript (4 files)" in stable
// order for the verbose diagnostic.
func languageCounts(detected map[string]int) string {
	languages := make([]string, 0, len(detected))
	for language := range detected {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	parts := make([]string, 0, len(languages))
	for _, language := range languages {
		parts = append(parts, fmt.Sprintf("%s (%d files)", language, detected[language]))
	}
	return strings.Join(parts, ", ")
}

// compactPath keeps a resolved path readable in the default table while the
// verbose row still names it in full.
func compactPath(path string, max int) string {
	runes := []rune(path)
	if len(runes) <= max || max < 4 {
		return path
	}
	half := (max - 3) / 2
	return string(runes[:half]) + "..." + string(runes[len(runes)-(max-3-half):])
}

// compactList bounds a recommendation line. The full list remains available
// through --verbose and --json.
func compactList(values []string, max int) string {
	if len(values) <= max {
		return strings.Join(values, ", ")
	}
	return strings.Join(values[:max], ", ") + fmt.Sprintf(", +%d more", len(values)-max)
}

// ripgrepRemediation names the install command for the host.
func ripgrepRemediation(host doctorHost) doctorRemediation {
	return doctorRemediation{Name: "ripgrep", Detail: ripgrepInstallHint(host)}
}

func ripgrepInstallHint(host doctorHost) string {
	switch host.goos() {
	case "darwin":
		if host.has("brew") {
			return "brew install ripgrep"
		}
		if host.has("port") {
			return "sudo port install ripgrep"
		}
	case "windows":
		if host.has("winget") {
			return "winget install BurntSushi.ripgrep.MSVC"
		}
		if host.has("scoop") {
			return "scoop install ripgrep"
		}
		if host.has("choco") {
			return "choco install ripgrep"
		}
	default:
		switch {
		case host.has("apt"):
			return "sudo apt install ripgrep"
		case host.has("dnf"):
			return "sudo dnf install ripgrep"
		case host.has("pacman"):
			return "sudo pacman -S ripgrep"
		case host.has("apk"):
			return "sudo apk add ripgrep"
		case host.has("nix"):
			return "nix profile install nixpkgs#ripgrep"
		}
	}
	return "install ripgrep: https://github.com/BurntSushi/ripgrep#installation"
}

// languageServerRemediation names the install command for one server. The
// recommendation name stays short so the table does not wrap; the command
// carries the full binary name.
func languageServerRemediation(definition languageServerDefinition, host doctorHost) doctorRemediation {
	return doctorRemediation{
		Name:   remediationName(definition.name),
		Detail: languageServerInstallHint(definition.name, host),
	}
}

func remediationName(server string) string {
	switch server {
	case "typescript-language-server":
		return "typescript"
	case "kotlin-language-server":
		return "kotlin-ls"
	default:
		return server
	}
}

func languageServerInstallHint(server string, host doctorHost) string {
	switch server {
	case "gopls":
		if host.goos() == "darwin" && host.has("brew") {
			return "brew install gopls"
		}
		return "go install golang.org/x/tools/gopls@latest"
	case "typescript-language-server":
		return "npm install -g typescript-language-server typescript"
	case "pyright":
		return "npm install -g pyright"
	case "pylsp":
		return "pip install python-lsp-server"
	case "rust-analyzer":
		return "rustup component add rust-analyzer"
	case "ruby-lsp":
		return "gem install ruby-lsp"
	case "csharp-ls":
		return "dotnet tool install --global csharp-ls"
	case "jdtls":
		switch host.goos() {
		case "darwin":
			if host.has("brew") {
				return "brew install jdtls"
			}
		case "windows":
			if host.has("scoop") {
				return "scoop install main/jdtls"
			}
		case "linux":
			if host.has("apk") {
				return "sudo apk add jdtls"
			}
			if host.has("brew") {
				return "brew install jdtls"
			}
		}
		return "install jdtls: https://github.com/eclipse-jdtls/eclipse.jdt.ls#installation"
	case "clangd":
		switch host.goos() {
		case "darwin":
			if host.has("brew") {
				return "brew install llvm"
			}
		case "windows":
			if host.has("winget") {
				return "winget install --id LLVM.clangd -e"
			}
			if host.has("scoop") {
				return "scoop install llvm"
			}
		case "linux":
			switch {
			case host.has("apt"):
				return "sudo apt install clangd"
			case host.has("dnf"):
				return "sudo dnf install clang-tools-extra"
			case host.has("pacman"):
				return "sudo pacman -S clang"
			case host.has("apk"):
				return "sudo apk add clang-extra-tools"
			}
		}
		return "install clangd: https://clangd.llvm.org/installation"
	case "kotlin-language-server":
		switch host.goos() {
		case "darwin":
			if host.has("brew") {
				return "brew install kotlin-language-server"
			}
		case "windows":
			if host.has("scoop") {
				return "scoop install kotlin-language-server"
			}
		}
		return "install kotlin-language-server: https://github.com/kotlin-lsp/kotlin-language-server/releases"
	}
	return "install " + server
}

// collectRecommendations flattens and deduplicates the remediations attached
// to checks. Duplicate names keep the first command, which is the preferred
// source.
func collectRecommendations(reports []doctorReport) []doctorRemediation {
	seen := map[string]bool{}
	var out []doctorRemediation
	for _, report := range reports {
		for _, remediation := range report.Remediations {
			if remediation.Name == "" || remediation.Detail == "" || seen[remediation.Name] {
				continue
			}
			seen[remediation.Name] = true
			out = append(out, remediation)
		}
	}
	return out
}

// doctorReportGroup is one rendered section.
type doctorReportGroup struct {
	Section doctorSection
	Reports []doctorReport
}

// groupReports orders checks by section and then by their emitted order.
func groupReports(reports []doctorReport) []doctorReportGroup {
	bySection := map[doctorSection][]doctorReport{}
	for _, report := range reports {
		bySection[report.Section] = append(bySection[report.Section], report)
	}
	var groups []doctorReportGroup
	for _, section := range doctorSectionOrder {
		if len(bySection[section]) == 0 {
			continue
		}
		groups = append(groups, doctorReportGroup{Section: section, Reports: bySection[section]})
		delete(bySection, section)
	}
	// A caller that adds a section without updating the order must still see
	// its checks. Sort the unknown sections so output stays deterministic.
	unknown := make([]string, 0, len(bySection))
	for section := range bySection {
		unknown = append(unknown, string(section))
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		section := doctorSection(name)
		groups = append(groups, doctorReportGroup{Section: section, Reports: bySection[section]})
	}
	return groups
}

// reportsWithRecommendations appends the Recommendations section. It returns a
// copy so rendering never mutates the diagnosis.
func reportsWithRecommendations(reports []doctorReport) []doctorReport {
	out := append([]doctorReport(nil), reports...)
	for _, remediation := range collectRecommendations(reports) {
		out = append(out, doctorReport{
			Section: doctorSectionRecommendations,
			Name:    remediation.Name,
			Status:  doctorStatusOptional,
			Detail:  remediation.Detail,
			Verbose: remediation.Detail,
		})
	}
	return out
}

// renderDoctorText prints the grouped, aligned human table.
func renderDoctorText(out io.Writer, reports []doctorReport, verbose bool) {
	for index, group := range groupReports(reportsWithRecommendations(reports)) {
		if index > 0 {
			_, _ = fmt.Fprintln(out)
		}
		_, _ = fmt.Fprintln(out, group.Section)
		for _, report := range group.Reports {
			_, _ = fmt.Fprintf(out, "  %-8s %-14s %s\n",
				report.Status, report.Name, report.detail(verbose))
		}
	}
}

// doctorJSONCheck is one machine-readable check.
type doctorJSONCheck struct {
	Name    string       `json:"name"`
	Status  doctorStatus `json:"status"`
	Detail  string       `json:"detail"`
	Verbose string       `json:"verbose,omitempty"`
}

// doctorJSONSection is one grouped section.
type doctorJSONSection struct {
	Name   string            `json:"name"`
	Checks []doctorJSONCheck `json:"checks"`
}

// doctorJSON is the deterministic machine-readable diagnosis. Sections are in
// display order, checks keep their diagnostic order, and no field depends on
// map iteration.
type doctorJSON struct {
	OK       bool                `json:"ok"`
	Failed   int                 `json:"failed"`
	Warnings int                 `json:"warnings"`
	Sections []doctorJSONSection `json:"sections"`
}

func renderDoctorJSON(out io.Writer, reports []doctorReport) error {
	failed := countFailed(reports)
	document := doctorJSON{
		OK:       failed == 0,
		Failed:   failed,
		Warnings: countWarnings(reports),
	}
	for _, group := range groupReports(reportsWithRecommendations(reports)) {
		section := doctorJSONSection{Name: string(group.Section)}
		for _, report := range group.Reports {
			section.Checks = append(section.Checks, doctorJSONCheck{
				Name: report.Name, Status: report.Status,
				Detail: report.Detail, Verbose: report.Verbose,
			})
		}
		document.Sections = append(document.Sections, section)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}
