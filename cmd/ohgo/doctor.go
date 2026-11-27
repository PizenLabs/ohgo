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
	"unicode"

	"github.com/PizenLabs/ohgo/internal/agent"
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
non-zero exit status.

Use --tools for a grouped breakdown of every registered tool (functional
group, approval safety, active backend, and purpose), and --intelligence (or
--lsp) for structural adapters, workspace language-to-LSP mappings, and
unsupported extensions. Both compose with --json.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd)
		},
	}
	cmd.Flags().Bool("json", false, "emit deterministic machine-readable diagnostics")
	cmd.Flags().BoolP("verbose", "v", false, "show full adapter, server, and tool lists")
	cmd.Flags().Bool("tools", false, "show every registered tool with group, safety, backend, and purpose")
	cmd.Flags().Bool("intelligence", false, "show structural adapters, workspace language mappings, and unsupported extensions")
	cmd.Flags().Bool("lsp", false, "alias for --intelligence")
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

// doctorView selects which diagnostic detail to render.
type doctorView struct {
	// Tools adds the live registered-tool breakdown.
	Tools bool
	// Intelligence adds structural adapter and language-server mappings.
	Intelligence bool
	// Verbose expands check details.
	Verbose bool
	// JSON selects the machine-readable document.
	JSON bool
}

// compact reports whether the default summary is being rendered without a
// drill-down or verbose expansion. Only that form earns the footer hint.
func (v doctorView) compact() bool {
	return !v.Tools && !v.Intelligence && !v.Verbose && !v.JSON
}

// doctorDiagnosis is the complete evidence a view may render. Reports are the
// always-present checks; Tools and Intelligence are collected from the same
// live composition, so a drill-down cannot describe a different tool set.
type doctorDiagnosis struct {
	Reports      []doctorReport
	Tools        []doctorTool
	Intelligence doctorIntelligence
}

// doctorTool is one live tool registration.
type doctorTool struct {
	Name      string   `json:"name"`
	Group     string   `json:"group"`
	Safety    string   `json:"safety"`
	Backend   string   `json:"backend"`
	Source    string   `json:"source"`
	Purpose   string   `json:"purpose"`
	Classes   []string `json:"classes,omitempty"`
	Languages []string `json:"languages,omitempty"`
}

// doctorAdapterDetail describes one structural adapter compiled into this
// binary.
type doctorAdapterDetail struct {
	Name         string   `json:"name"`
	Languages    []string `json:"languages"`
	Extensions   []string `json:"extensions"`
	Capabilities []string `json:"capabilities"`
}

// doctorLanguageMapping connects one workspace language to its structural
// adapter and language server. Status is ready, missing, or unsupported.
type doctorLanguageMapping struct {
	Language   string `json:"language"`
	Files      int    `json:"files"`
	Structural string `json:"structural"`
	Server     string `json:"server,omitempty"`
	Path       string `json:"path,omitempty"`
	Status     string `json:"status"`
}

// doctorUnsupportedExtension is a source-bearing extension the active build
// cannot parse.
type doctorUnsupportedExtension struct {
	Extension string `json:"extension"`
	Files     int    `json:"files"`
}

// doctorIntelligence is the expanded code-intelligence view. Error is set when
// the workspace inventory itself could not be read.
type doctorIntelligence struct {
	Error       string                       `json:"error,omitempty"`
	Adapters    []doctorAdapterDetail        `json:"adapters"`
	Languages   []doctorLanguageMapping      `json:"languages"`
	Unsupported []doctorUnsupportedExtension `json:"unsupported"`
}

// doctorToolGroupOrder is the display order for tool groups. It names groups,
// never tools: the members come from the live registry.
var doctorToolGroupOrder = []tools.ToolGroup{
	tools.ToolGroupFileOperations,
	tools.ToolGroupSearchDiscovery,
	tools.ToolGroupCodeIntelligence,
	tools.ToolGroupExecutionVerification,
	tools.ToolGroupWorkflowNetwork,
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
	diagnosis := collectDoctorDiagnosis(cmd)
	view := doctorViewFromCmd(cmd)

	if view.JSON {
		if err := renderDoctorJSON(out, diagnosis, view); err != nil {
			return err
		}
	} else {
		renderDoctorText(out, diagnosis.Reports, view.Verbose)
		if view.Tools {
			renderDoctorToolDetails(out, diagnosis.Tools)
		}
		if view.Intelligence {
			renderDoctorIntelligenceDetails(out, diagnosis.Intelligence)
		}
		if view.compact() {
			renderDoctorHint(out)
		}
	}
	return doctorFailure(diagnosis.Reports)
}

// doctorViewFromCmd projects the CLI flags onto the shared view.
func doctorViewFromCmd(cmd *cobra.Command) doctorView {
	toolDetail, _ := cmd.Flags().GetBool("tools")
	intelligence, _ := cmd.Flags().GetBool("intelligence")
	lsp, _ := cmd.Flags().GetBool("lsp")
	verbose, _ := cmd.Flags().GetBool("verbose")
	jsonOut, _ := cmd.Flags().GetBool("json")
	return doctorView{
		Tools:        toolDetail,
		Intelligence: intelligence || lsp,
		Verbose:      verbose,
		JSON:         jsonOut,
	}
}

// parseDoctorViewArgs is the TUI/REPL form of the CLI flags. The same option
// words select the same detail in both interfaces.
func parseDoctorViewArgs(args []string) (doctorView, error) {
	var view doctorView
	for _, arg := range args {
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "tools", "--tools", "tool":
			view.Tools = true
		case "intelligence", "--intelligence", "intel", "lsp", "--lsp":
			view.Intelligence = true
		case "verbose", "--verbose", "-v":
			view.Verbose = true
		case "json", "--json":
			view.JSON = true
		case "":
			// An extra space is not an option.
		default:
			return view, fmt.Errorf("unknown doctor option %q; use --tools, --intelligence, --verbose, or --json", arg)
		}
	}
	return view, nil
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
	return collectDoctorDiagnosis(cmd).Reports
}

// collectDoctorDiagnosis runs every check and collects the drill-down detail
// from the same live composition.
func collectDoctorDiagnosis(cmd *cobra.Command) doctorDiagnosis {
	return collectDiagnosis(flagValue(cmd, "model"), ai.NewRegistry())
}

// collectDiagnosis returns the checks plus the tool and intelligence detail a
// drill-down may render. The harness and the CLI both call it, so the summary
// and the drill-down describe the same live composition.
func collectDiagnosis(modelFlag string, registry *ai.Registry) doctorDiagnosis {
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

	// One inventory scan serves the build-capability check, the
	// workspace-scoped LSP recommendations, and the intelligence drill-down.
	// It is the same structural inventory production tools use, so an ignored
	// directory is not counted here either.
	inventory, inventoryErr := workspaceInventoryFor(cwd)
	languages := inventory.Languages
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
		seenErrors := map[string]bool{}
		external := 0
		for _, tool := range toolSet {
			names = append(names, tool.Name())
			reporter, ok := tool.(tools.ExternalCapabilityReporter)
			if !ok || !reporter.ExternalCapability() {
				continue
			}
			external++
			message := reporter.ConfigError()
			if message == "" || seenErrors[message] {
				continue
			}
			seenErrors[message] = true
			externalErrors = append(externalErrors, tool.Name()+": "+message)
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

	return doctorDiagnosis{
		Reports:      reports,
		Tools:        collectToolDetails(toolSet),
		Intelligence: collectIntelligence(host, inventory, inventoryErr),
	}
}

// doctorWorkspaceInventory is the structural inventory the intelligence view
// and the semantic recommendation share.
type doctorWorkspaceInventory struct {
	Languages   []ast.LanguageCoverage
	Unsupported []ast.UnsupportedLanguage
}

// workspaceInventoryFor reads the workspace language and unsupported-extension
// inventory from the same adaptive structural source production tools use.
func workspaceInventoryFor(root string) (doctorWorkspaceInventory, error) {
	source := tools.NewStructuralSource(root)
	idx, err := source.Index()
	if err != nil {
		return doctorWorkspaceInventory{}, err
	}
	info := idx.Info()
	inventory := doctorWorkspaceInventory{
		Languages:   make([]ast.LanguageCoverage, len(info.Languages)),
		Unsupported: make([]ast.UnsupportedLanguage, len(info.Unsupported)),
	}
	copy(inventory.Languages, info.Languages)
	copy(inventory.Unsupported, info.Unsupported)
	return inventory, nil
}

// workspaceLanguages returns the source-language inventory of root. It uses
// the same adaptive structural source production tools use, so the doctor
// recommends servers for the workspace it would actually analyze rather than
// for every language this binary knows.
func workspaceLanguages(root string) ([]ast.LanguageCoverage, error) {
	inventory, err := workspaceInventoryFor(root)
	return inventory.Languages, err
}

// collectToolDetails projects every registered tool from its live metadata. No
// names, descriptions, or group membership are maintained here: the tool owns
// its description and group, and the approval/action classes own safety.
func collectToolDetails(toolSet []agent.Tool) []doctorTool {
	out := make([]doctorTool, 0, len(toolSet))
	for _, tool := range toolSet {
		group := string(tools.ToolGroupSearchDiscovery)
		if reporter, ok := tool.(interface{ Group() tools.ToolGroup }); ok {
			if reported := reporter.Group(); reported != "" {
				group = string(reported)
			}
		}
		classes := agent.ToolActionClasses(tool)
		classNames := make([]string, 0, len(classes))
		for _, class := range classes {
			classNames = append(classNames, string(class))
		}
		sort.Strings(classNames)
		source := "built-in"
		switch {
		case agent.HasActionClass(tool, agent.ActionConfigured):
			source = "mcp"
		case agent.HasActionClass(tool, agent.ActionExternal):
			source = "external"
		}
		languages := agent.ToolRequiredLanguages(tool)
		sort.Strings(languages)
		backend := doctorToolBackend(tool)
		if reporter, ok := tool.(tools.ExternalCapabilityReporter); ok && reporter.ConfigError() != "" {
			backend += " (config error)"
		}
		out = append(out, doctorTool{
			Name:      tool.Name(),
			Group:     group,
			Safety:    doctorToolSafety(tool),
			Backend:   backend,
			Source:    source,
			Purpose:   doctorToolPurpose(tool.Description()),
			Classes:   classNames,
			Languages: languages,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := toolGroupRank(out[i].Group), toolGroupRank(out[j].Group)
		if left != right {
			return left < right
		}
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func toolGroupRank(group string) int {
	for index, candidate := range doctorToolGroupOrder {
		if string(candidate) == group {
			return index
		}
	}
	return len(doctorToolGroupOrder)
}

// doctorToolSafety reports the approval class and any additional mutation or
// execution surface. The tier is the authoritative approval boundary; action
// classes add the second capability when a tool exercises one.
func doctorToolSafety(tool agent.Tool) string {
	tier := tool.Approval().Tier
	safety := "read-only"
	switch tier {
	case agent.TierWrite:
		safety = "mutating"
	case agent.TierExec:
		safety = "shell"
	}
	mutates := agent.HasActionClass(tool, agent.ActionMutate)
	executes := agent.HasActionClass(tool, agent.ActionExecute)
	switch {
	case tier == agent.TierExec && mutates:
		safety += ", mutating"
	case tier == agent.TierWrite && executes:
		safety += ", shell"
	}
	return safety
}

// doctorToolBackend reports the runtime binding a tool will actually use.
func doctorToolBackend(tool agent.Tool) string {
	if reporter, ok := tool.(interface{ SearchBackend() (string, string) }); ok {
		backend, path := reporter.SearchBackend()
		if strings.TrimSpace(path) == "" {
			return backend
		}
		return fmt.Sprintf("%s (%s)", backend, compactPath(path, 40))
	}
	if namer, ok := tool.(agent.CapabilityNamer); ok {
		return string(namer.SecurityCapability())
	}
	return "built-in"
}

// doctorToolPurpose condenses the tool's own model-facing description to its
// first sentence. The description remains the single source; the doctor does
// not maintain a second purpose table that can drift.
func doctorToolPurpose(description string) string {
	text := strings.Join(strings.Fields(description), " ")
	if index := strings.Index(text, ". "); index >= 0 {
		text = text[:index+1]
	}
	return compactText(text, 110)
}

// compactText bounds one line at a word boundary without splitting a rune.
func compactText(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max || max < 2 {
		return text
	}
	cut := max
	for cut > 0 && !unicode.IsSpace(runes[cut]) {
		cut--
	}
	if cut == 0 {
		cut = max
	}
	return strings.TrimSpace(string(runes[:cut])) + "..."
}

// collectIntelligence maps each workspace language to its structural adapter
// and resolved language server. Adapter and server identities come from the
// live registries, never from a doctor-local table.
func collectIntelligence(host doctorHost, inventory doctorWorkspaceInventory, inventoryErr error) doctorIntelligence {
	detail := doctorIntelligence{
		Adapters:    adapterDetails(),
		Languages:   []doctorLanguageMapping{},
		Unsupported: []doctorUnsupportedExtension{},
	}
	if inventoryErr != nil {
		detail.Error = inventoryErr.Error()
		return detail
	}

	installed := languageServerCommands(host.lookPath())
	byLanguage := map[string]semantic.Spec{}
	for _, spec := range installed {
		for _, language := range spec.Languages {
			if _, ok := byLanguage[language]; !ok {
				byLanguage[language] = spec
			}
		}
	}

	aggregated := map[string]*doctorLanguageMapping{}
	for _, coverage := range inventory.Languages {
		language := lspLanguageID(string(coverage.Language))
		if language == "" {
			continue
		}
		mapping := aggregated[language]
		if mapping == nil {
			mapping = &doctorLanguageMapping{
				Language: language, Structural: "adapter", Status: "missing",
			}
			aggregated[language] = mapping
		}
		mapping.Files += coverage.Files
		if !coverage.Supported {
			mapping.Structural = "unsupported"
		}
	}
	languages := make([]string, 0, len(aggregated))
	for language := range aggregated {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	for _, language := range languages {
		mapping := *aggregated[language]
		if spec, ok := byLanguage[language]; ok {
			mapping.Server = spec.Name
			if len(spec.Command) > 0 {
				mapping.Path = spec.Command[0]
			}
			mapping.Status = "ready"
		} else if definition, ok := languageServerDefinitionFor(language); ok {
			mapping.Server = definition.name
		}
		detail.Languages = append(detail.Languages, mapping)
	}
	for _, unsupported := range inventory.Unsupported {
		detail.Unsupported = append(detail.Unsupported, doctorUnsupportedExtension{
			Extension: unsupported.Extension, Files: unsupported.Files,
		})
	}
	sort.SliceStable(detail.Unsupported, func(i, j int) bool {
		return detail.Unsupported[i].Extension < detail.Unsupported[j].Extension
	})
	return detail
}

// adapterDetails projects the adapters compiled into this binary.
func adapterDetails() []doctorAdapterDetail {
	adapters := ast.DefaultAdapters()
	out := make([]doctorAdapterDetail, 0, len(adapters))
	for _, adapter := range adapters {
		languages := make([]string, 0, len(adapter.Languages()))
		for _, language := range adapter.Languages() {
			languages = append(languages, string(language))
		}
		sort.Strings(languages)
		extensions := append([]string(nil), adapter.Extensions()...)
		sort.Strings(extensions)
		out = append(out, doctorAdapterDetail{
			Name:         adapter.Name(),
			Languages:    languages,
			Extensions:   extensions,
			Capabilities: adapterCapabilities(adapter.Capabilities()),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func adapterCapabilities(capabilities ast.Capabilities) []string {
	values := []struct {
		name    string
		enabled bool
	}{
		{"symbols", capabilities.Symbols},
		{"imports", capabilities.Imports},
		{"exports", capabilities.Exports},
		{"references", capabilities.References},
		{"calls", capabilities.Calls},
		{"inheritance", capabilities.Inheritance},
		{"tests", capabilities.Tests},
		{"entrypoints", capabilities.Entrypoints},
		{"semantic", capabilities.Semantic},
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value.enabled {
			out = append(out, value.name)
		}
	}
	return out
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

// doctorToolGroup is one rendered tool group.
type doctorToolGroup struct {
	Name  string
	Tools []doctorTool
}

// groupToolDetails preserves the registry order established by
// collectToolDetails, which is already group-major and name-sorted.
func groupToolDetails(tools []doctorTool) []doctorToolGroup {
	var groups []doctorToolGroup
	for _, tool := range tools {
		if len(groups) == 0 || groups[len(groups)-1].Name != tool.Group {
			groups = append(groups, doctorToolGroup{Name: tool.Group})
		}
		groups[len(groups)-1].Tools = append(groups[len(groups)-1].Tools, tool)
	}
	return groups
}

// renderDoctorToolDetails prints the live tool breakdown requested by --tools.
func renderDoctorToolDetails(out io.Writer, tools []doctorTool) {
	_, _ = fmt.Fprintf(out, "\nRegistered Tools (%d)\n", len(tools))
	if len(tools) == 0 {
		_, _ = fmt.Fprintln(out, "  no tools registered")
		return
	}
	_, _ = fmt.Fprintf(out, "\n  %-16s %-16s %-9s %-30s %s\n",
		"TOOL", "SAFETY", "SOURCE", "BACKEND", "PURPOSE")
	for _, group := range groupToolDetails(tools) {
		_, _ = fmt.Fprintf(out, "\n  %s\n", group.Name)
		for _, tool := range group.Tools {
			_, _ = fmt.Fprintf(out, "  %-16s %-16s %-9s %-30s %s\n",
				tool.Name, tool.Safety, tool.Source, tool.Backend, tool.Purpose)
		}
	}
}

// renderDoctorIntelligenceDetails prints the --intelligence expansion.
func renderDoctorIntelligenceDetails(out io.Writer, detail doctorIntelligence) {
	_, _ = fmt.Fprintln(out, "\nCode Intelligence Detail")
	if detail.Error != "" {
		_, _ = fmt.Fprintf(out, "  ERROR workspace inventory unavailable: %s\n", detail.Error)
		return
	}

	_, _ = fmt.Fprintf(out, "\nStructural Adapters (%d)\n", len(detail.Adapters))
	_, _ = fmt.Fprintf(out, "  %-24s %-18s %-26s %s\n",
		"ADAPTER", "LANGUAGES", "EXTENSIONS", "FACTS")
	for _, adapter := range detail.Adapters {
		_, _ = fmt.Fprintf(out, "  %-24s %-18s %-26s %s\n",
			adapter.Name,
			compactList(adapter.Languages, 3),
			compactList(adapter.Extensions, 3),
			compactList(adapter.Capabilities, 8))
	}

	_, _ = fmt.Fprintf(out, "\nWorkspace Languages (%d)\n", len(detail.Languages))
	_, _ = fmt.Fprintf(out, "  %-14s %-6s %-8s %-40s %s\n",
		"LANGUAGE", "FILES", "STATUS", "SERVER", "STRUCTURAL")
	for _, mapping := range detail.Languages {
		server := mapping.Server
		if server == "" {
			server = "-"
		} else if mapping.Path != "" {
			server += " (" + compactPath(mapping.Path, 36) + ")"
		}
		_, _ = fmt.Fprintf(out, "  %-14s %-6d %-8s %-40s %s\n",
			mapping.Language, mapping.Files, mapping.Status, server, mapping.Structural)
	}

	_, _ = fmt.Fprintf(out, "\nUnsupported Extensions (%d)\n", len(detail.Unsupported))
	if len(detail.Unsupported) == 0 {
		_, _ = fmt.Fprintln(out, "  none")
		return
	}
	for _, unsupported := range detail.Unsupported {
		_, _ = fmt.Fprintf(out, "  %-10s %d file(s)\n", unsupported.Extension, unsupported.Files)
	}
}

// doctorHintText is the compact-view footer shown by every interface.
const doctorHintText = "--tools shows every tool's group, safety, and backend; --intelligence shows language mappings; --verbose expands checks"

// renderDoctorHint points at the targeted views without forcing --verbose.
func renderDoctorHint(out io.Writer) {
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "hint: "+doctorHintText)
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
// map iteration. Tools and Intelligence are present only when requested.
type doctorJSON struct {
	OK           bool                `json:"ok"`
	Failed       int                 `json:"failed"`
	Warnings     int                 `json:"warnings"`
	Sections     []doctorJSONSection `json:"sections"`
	Tools        *[]doctorTool       `json:"tools,omitempty"`
	Intelligence *doctorIntelligence `json:"intelligence,omitempty"`
}

func renderDoctorJSON(out io.Writer, diagnosis doctorDiagnosis, view doctorView) error {
	failed := countFailed(diagnosis.Reports)
	document := doctorJSON{
		OK:       failed == 0,
		Failed:   failed,
		Warnings: countWarnings(diagnosis.Reports),
	}
	for _, group := range groupReports(reportsWithRecommendations(diagnosis.Reports)) {
		section := doctorJSONSection{Name: string(group.Section)}
		for _, report := range group.Reports {
			section.Checks = append(section.Checks, doctorJSONCheck{
				Name: report.Name, Status: report.Status,
				Detail: report.Detail, Verbose: report.Verbose,
			})
		}
		document.Sections = append(document.Sections, section)
	}
	if view.Tools {
		tools := diagnosis.Tools
		if tools == nil {
			tools = []doctorTool{}
		}
		document.Tools = &tools
	}
	if view.Intelligence {
		intelligence := diagnosis.Intelligence
		document.Intelligence = &intelligence
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}
