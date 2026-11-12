package cognition

import (
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// ClaimSummary is what the evidence state can truthfully support on the answer
// side. It is derived from the same observations the completion gate uses, so
// the model's pre-answer briefing and the final review cannot disagree.
//
// The classes are evidence classes, not confidence levels: a configured port
// and an implemented listener are different observations, and an absence claim
// needs a complete filtered inventory, not a zero in a metadata overview.
type ClaimSummary struct {
	// HasImplementation reports source content (a source read or source-scoped
	// grep), not a manifest, configuration, documentation, or inventory.
	HasImplementation bool
	// HasConfiguration reports a configuration or manifest observation. It
	// declares topology or dependencies; it does not prove execution.
	HasConfiguration bool
	// HasDocumentation reports a prose observation.
	HasDocumentation bool
	// HasRuntime reports a live runtime observation.
	HasRuntime bool
	// HasInventory reports a file/directory/structural inventory. It proves
	// names exist; it does not prove what their content does.
	HasInventory bool
	// HasSourcePath reports source coordinates observed without source
	// content, such as a glob result or a structural file row.
	HasSourcePath bool
	// StructuralScope is COMPLETE, PARTIAL, or "" when no structural
	// observation exists.
	StructuralScope string
	// StructuralCompleteness is COMPLETE, PARTIAL, UNPARSED, or "".
	StructuralCompleteness string
	// InventoryCompleteness is COMPLETE, PARTIAL, or "".
	InventoryCompleteness string
	// Truncated reports that the latest structural result was bounded by its
	// own page/window limit.
	Truncated bool
	// ExcludedRoots, ExcludedSourceFiles, and UnsupportedSources are the
	// disclosed scope gaps of the latest structural observation.
	ExcludedRoots       int
	ExcludedSourceFiles int
	UnsupportedSources  int
	// DependencyLevel is the strongest dependency signal observed:
	// NOT_OBSERVED, DECLARED, IMPORTED, USED_IN_SOURCE, or RUNTIME. It is a
	// projection hint; a specific dependency claim is audited against its own
	// component and declaration.
	DependencyLevel string
	// TestInventory is COMPLETE, PARTIAL, or NOT_QUERIED. Only COMPLETE with
	// zero test files can support a test-file absence claim.
	TestInventory string
	// TestFiles is the snapshot test-file count when the inventory reported it.
	TestFiles int
	// TestFilesKnown reports that TestFiles is an observation rather than a
	// zero-value default.
	TestFilesKnown bool
	// TestTotal is the test-symbol count the filtered inventory returned.
	TestTotal int
	// TestTotalKnown reports that TestTotal is an observation.
	TestTotalKnown bool
	// ImplementationPaths, ConfigurationPaths, and DocumentationPaths are
	// bounded coordinates the model can cite. They are evidence, not authority.
	ImplementationPaths []string
	ConfigurationPaths  []string
	DocumentationPaths  []string
	// Components is a bounded, deterministic projection of the components the
	// inventory/source observations name and whether source content was
	// observed for each. It is how the state block derives an actionable
	// evidence frontier without hardcoding project names.
	Components []ComponentEvidence
}

// ComponentEvidence is one component identity observed in the evidence.
type ComponentEvidence struct {
	// Name is the derived component label.
	Name string
	// SourceContent reports observed source content under this component.
	SourceContent bool
	// Structural reports an inventory or structural coordinate naming it.
	Structural bool
	// HintPath is a bounded coordinate the model can inspect to acquire
	// source content. It is inventory metadata, not source evidence.
	HintPath string
}

// claimPathLimit bounds each model-facing path list. The evidence block already
// carries the coordinates; this list is a reminder of the observed class.
const claimPathLimit = 3

// claimComponentLimit bounds the projected component frontier.
const claimComponentLimit = 8

// ClaimSummary derives the answer-side claim classes from the evidence state.
func (s *EvidenceState) ClaimSummary() ClaimSummary {
	summary := ClaimSummary{TestInventory: "NOT_QUERIED", DependencyLevel: "NOT_OBSERVED"}
	if s == nil {
		return summary
	}
	scopeSeq := -1
	components := map[string]*ComponentEvidence{}
	addComponent := func(name string, source bool, structural bool, hint string) {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			return
		}
		entry := components[name]
		if entry == nil {
			entry = &ComponentEvidence{Name: name}
			components[name] = entry
		}
		if source {
			entry.SourceContent = true
		}
		if structural {
			entry.Structural = true
		}
		if entry.HintPath == "" {
			entry.HintPath = strings.TrimSpace(hint)
		}
	}
	for _, item := range s.Items {
		if item.Status == StatusConflicting || item.Status == StatusUnknown || item.Status == StatusRequired {
			continue
		}
		if item.Runtime {
			summary.HasRuntime = true
			if runtimeDependencySignal(item) {
				summary.DependencyLevel = laterDependencyLevel(summary.DependencyLevel, "RUNTIME")
			}
		}
		// A later current structural query describes the snapshot. A stale
		// observation is evidence of what was true, not proof of what is true
		// now, so it cannot support an absence claim.
		current := item.Source.GraphState == "CURRENT"
		if current && item.Source.ScopeCompleteness != "" && item.Seq >= scopeSeq {
			scopeSeq = item.Seq
			summary.StructuralScope = item.Source.ScopeCompleteness
			summary.StructuralCompleteness = item.Source.Completeness
			summary.InventoryCompleteness = item.Source.InventoryCompleteness
			summary.Truncated = item.Source.Truncated
			summary.ExcludedRoots = item.Source.ExcludedRoots
			summary.ExcludedSourceFiles = item.Source.ExcludedSourceFiles
			summary.UnsupportedSources = item.Source.UnsupportedSources
		}
		if item.ID == "structure:tests" {
			// A stale inventory was queried, but it is not current truth. It
			// remains PARTIAL so the model cannot read it as absence support.
			summary.TestInventory = testInventoryState(item)
			if value, ok := item.Counts["test_files"]; ok {
				summary.TestFiles = value
				summary.TestFilesKnown = true
			}
			if value, ok := item.Counts["total"]; ok {
				summary.TestTotal = value
				summary.TestTotalKnown = true
			}
		}
		switch item.EvidenceClass() {
		case EvidenceDocumentation:
			if !claimFileContent(item) {
				continue
			}
			summary.HasDocumentation = true
			summary.DocumentationPaths = appendClaimPath(summary.DocumentationPaths, item.Source.Path)
		case EvidenceConfiguration, EvidenceManifest:
			if !claimFileContent(item) {
				continue
			}
			summary.HasConfiguration = true
			summary.ConfigurationPaths = appendClaimPath(summary.ConfigurationPaths, item.Source.Path)
			summary.DependencyLevel = laterDependencyLevel(summary.DependencyLevel, "DECLARED")
		case EvidenceInventory:
			summary.HasInventory = true
			for _, entry := range listingComponents(item) {
				addComponent(entry.Name, false, true, entry.HintPath)
			}
		case EvidenceStructural:
			summary.HasInventory = true
			if isComponentSeedPath(item.Source.Path) {
				if name := agent.ComponentFromPath(item.Source.Path); name != "" {
					addComponent(name, false, true, item.Source.Path)
				}
			}
		case EvidenceSourcePath:
			summary.HasSourcePath = true
			if isComponentSeedPath(item.Source.Path) {
				if name := agent.ComponentFromPath(item.Source.Path); name != "" {
					addComponent(name, false, true, item.Source.Path)
				}
			}
			if strings.HasPrefix(item.ID, "search:glob:") {
				for _, path := range globPaths(item.Value) {
					if isComponentSeedPath(path) {
						if name := agent.ComponentFromPath(path); name != "" {
							addComponent(name, false, true, path)
						}
					}
				}
			}
		case EvidenceSourceImport:
			summary.HasImplementation = true
			summary.DependencyLevel = laterDependencyLevel(summary.DependencyLevel, "IMPORTED")
			summary.ImplementationPaths = appendClaimPath(summary.ImplementationPaths, item.Source.Path)
			if isComponentSeedPath(item.Source.Path) {
				if name := agent.ComponentFromPath(item.Source.Path); name != "" {
					addComponent(name, true, false, item.Source.Path)
				}
			}
		case EvidenceSourceUse:
			summary.HasImplementation = true
			summary.DependencyLevel = laterDependencyLevel(summary.DependencyLevel, "USED_IN_SOURCE")
			summary.ImplementationPaths = appendClaimPath(summary.ImplementationPaths, item.Source.Path)
			if isComponentSeedPath(item.Source.Path) {
				if name := agent.ComponentFromPath(item.Source.Path); name != "" {
					addComponent(name, true, false, item.Source.Path)
				}
			}
		case EvidenceSourceContent:
			summary.HasImplementation = true
			summary.ImplementationPaths = appendClaimPath(summary.ImplementationPaths, item.Source.Path)
			if isComponentSeedPath(item.Source.Path) {
				if name := agent.ComponentFromPath(item.Source.Path); name != "" {
					addComponent(name, true, false, item.Source.Path)
				}
			}
		}
	}
	summary.Components = sortedComponentEvidence(components)
	return summary
}

// EvidenceFrontier derives the actionable next-observation list from the
// current claim summary. It names what is missing, not what the model should
// conclude: an implementation frontier is resolved only by source content for
// that component, and a test frontier only by a complete current inventory.
//
// It is deterministic and bounded. It never hardcodes a project's component
// names; the components come from the observations themselves.
func (s ClaimSummary) EvidenceFrontier() []string {
	var out []string
	add := func(line string) {
		if len(out) >= 4 {
			return
		}
		for _, existing := range out {
			if existing == line {
				return
			}
		}
		out = append(out, line)
	}
	componentLines := 0
	for _, component := range s.Components {
		if component.SourceContent || !component.Structural || componentLines >= 2 {
			continue
		}
		componentLines++
		if component.HintPath != "" {
			add("component " + component.Name + " -> SOURCE_CONTENT missing; next: read " + component.HintPath)
		} else {
			add("component " + component.Name + " -> SOURCE_CONTENT missing; next: inspect its entrypoint or implementation files")
		}
	}
	if !s.HasImplementation && s.HasConfiguration {
		add("implementation -> SOURCE_CONTENT missing; documentation, manifests, configuration, and inventory are not implementation")
	}
	if s.TestInventory != "COMPLETE" && (s.HasInventory || s.StructuralScope != "") {
		add("test absence -> COMPLETE current unscoped test inventory missing; next: query structure view=tests and inspect completeness")
	}
	if s.StructuralScope == "PARTIAL" || s.ExcludedRoots > 0 || s.ExcludedSourceFiles > 0 {
		detail := "PARTIAL"
		var gaps []string
		if s.ExcludedRoots > 0 {
			gaps = append(gaps, itoa(s.ExcludedRoots)+" excluded root(s)")
		}
		if s.ExcludedSourceFiles > 0 {
			gaps = append(gaps, itoa(s.ExcludedSourceFiles)+" excluded source file(s)")
		}
		if len(gaps) > 0 {
			detail += " (" + strings.Join(gaps, ", ") + ")"
		}
		add("scope -> " + detail + "; repository-wide absence is unsupported")
	}
	if s.Truncated {
		add("structural result -> TRUNCATED; complete inventory is unsupported")
	}
	if s.UnsupportedSources > 0 {
		add("unsupported source -> " + itoa(s.UnsupportedSources) + " file(s) have no structural adapter; their content is not observed")
	}
	if s.HasImplementation && s.DependencyLevel == "DECLARED" {
		add("dependency usage -> DECLARED only; next: observe an import or a concrete operation in the component's source")
	} else if s.HasImplementation && s.DependencyLevel == "IMPORTED" {
		add("dependency usage -> IMPORTED only; next: observe a concrete source operation before saying the project uses it")
	}
	return out
}

// isComponentSeedPath reports a path whose derived identity is a project
// component rather than a shared library package. A package under backend/pkg
// is not a service, and naming it in the evidence frontier would send the
// model to inspect the wrong scope.
func isComponentSeedPath(path string) bool {
	normalized := strings.Trim(strings.ToLower(strings.ReplaceAll(path, "\\", "/")), "/")
	if normalized == "" {
		return false
	}
	for _, shared := range []string{"/pkg/", "/utils/", "/util/", "/shared/", "/common/", "/lib/"} {
		if strings.Contains("/"+normalized+"/", shared) {
			return false
		}
	}
	for _, root := range []string{
		"cmd/", "internal/", "services/", "apps/", "packages/",
		"src/", "frontend/", "backend/", "server/", "web/",
	} {
		if normalized == strings.TrimSuffix(root, "/") ||
			strings.HasPrefix(normalized, root) ||
			strings.Contains(normalized, "/"+root) {
			return true
		}
	}
	return false
}

// sortedComponentEvidence orders the component projection deterministically.
// Components without source content come first: they are the actionable gap.
func sortedComponentEvidence(values map[string]*ComponentEvidence) []ComponentEvidence {
	out := make([]ComponentEvidence, 0, len(values))
	for _, entry := range values {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceContent != out[j].SourceContent {
			return !out[i].SourceContent
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > claimComponentLimit {
		out = out[:claimComponentLimit]
	}
	return out
}

// runtimeDependencySignal reports a runtime observation whose output names a
// dependency and a readiness/operation signal. A live service alone does not
// prove that it uses a particular dependency.
func runtimeDependencySignal(item EvidenceItem) bool {
	text := strings.ToLower(item.Value + " " + item.Summary)
	for _, token := range []string{
		"redis", "rabbitmq", "kafka", "postgres", "mysql", "mongodb",
		"jwt", "grpc", "elasticsearch",
	} {
		if strings.Contains(text, token) {
			for _, marker := range []string{"ping", "ready", "connected", "operation", "cache"} {
				if strings.Contains(text, marker) {
					return true
				}
			}
		}
	}
	return false
}

// laterDependencyLevel keeps the strongest dependency signal. The ranks are
// evidence strength, not confidence.
func laterDependencyLevel(current, next string) string {
	rank := func(value string) int {
		switch value {
		case "DECLARED":
			return 1
		case "IMPORTED":
			return 2
		case "USED_IN_SOURCE":
			return 3
		case "RUNTIME":
			return 4
		default:
			return 0
		}
	}
	if rank(next) > rank(current) {
		return next
	}
	return current
}

// componentSeed is one component coordinate derived from inventory metadata.
type componentSeed struct {
	Name     string
	HintPath string
}

// listingComponents returns the component-shaped directory entries a listing
// disclosed. It is metadata, not source evidence: the frontier uses it only to
// say which source content is still missing and where to look.
func listingComponents(item EvidenceItem) []componentSeed {
	if !strings.HasPrefix(item.ID, "dir:") {
		return nil
	}
	base := strings.ToLower(strings.Trim(strings.TrimPrefix(item.ID, "dir:"), "/"))
	last := base
	if index := strings.LastIndex(base, "/"); index >= 0 {
		last = base[index+1:]
	}
	switch last {
	case "cmd", "services", "service", "apps", "internal", "packages",
		"src", "components", "handlers", "handlers-go", "backend", "frontend":
	default:
		return nil
	}
	parent := strings.Trim(item.Source.Path, "/")
	if parent == "" || parent == "." {
		parent = ""
	}
	var out []componentSeed
	for _, raw := range strings.Split(item.Value, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line == item.Source.Path || strings.HasPrefix(line, "[") {
			continue
		}
		line = strings.TrimSuffix(line, "/")
		if strings.ContainsAny(line, " \t:") {
			continue
		}
		name := agent.ComponentFromPath(line)
		if name == "" {
			name = strings.ToLower(line)
		}
		if !listingComponentName(name) {
			continue
		}
		hint := line
		if parent != "" {
			hint = parent + "/" + line
		}
		out = append(out, componentSeed{Name: name, HintPath: hint})
	}
	return out
}

// listingComponentName filters generic structural folders out of a listing so
// the frontier names candidate components, not every directory.
func listingComponentName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || len(name) > 40 {
		return false
	}
	switch name {
	case "components", "contexts", "pages", "hooks", "store", "stores",
		"utils", "util", "lib", "shared", "common", "types", "styles",
		"assets", "public", "static", "tests", "test", "fixtures", "docs",
		"doc", "build", "dist", "bin", "target", "vendor", "node_modules",
		"docker", "k8s", "deploy", "monitoring", "scripts", "proto",
		"migrations", "config", "configs", "api", "handler", "handlers",
		"domain", "repository", "service", "backend", "frontend", "server",
		"cmd", "internal", "pkg", "packages", "apps", "src":
		return false
	}
	return true
}

// globPaths extracts the source coordinates a glob result returned. The
// trailing exclusion/truncation notes are not paths.
func globPaths(text string) []string {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "No files") {
			continue
		}
		if strings.ContainsAny(line, " \t") {
			continue
		}
		out = append(out, line)
		if len(out) >= 24 {
			break
		}
	}
	return out
}

// claimFileContent reports a direct file read that carried content. A search
// hit, structural coordinate, or missing-file observation does not establish
// what the file says.
func claimFileContent(item EvidenceItem) bool {
	return strings.HasPrefix(item.ID, "file:") && strings.TrimSpace(item.Value) != ""
}

// testInventoryState reports whether one structure tests observation proves
// absence. It requires scope, inventory, and structural completeness plus an
// untruncated result; the completion layer applies the same axes.
func testInventoryState(item EvidenceItem) string {
	if item.Complete &&
		item.Source.GraphState == "CURRENT" &&
		item.Source.ScopeCompleteness == "COMPLETE" &&
		item.Source.InventoryCompleteness == "COMPLETE" &&
		item.Source.Completeness == "COMPLETE" &&
		!item.Source.Truncated {
		return "COMPLETE"
	}
	return "PARTIAL"
}

// appendClaimPath retains a bounded, duplicate-free coordinate list. A
// directory listing has no source coordinate and is skipped; it is structure,
// not implementation.
func appendClaimPath(paths []string, path string) []string {
	path = strings.TrimSpace(path)
	if path == "" || path == "." || path == "./" {
		return paths
	}
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	if len(paths) >= claimPathLimit {
		return paths
	}
	return append(paths, path)
}
