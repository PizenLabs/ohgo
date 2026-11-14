package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/intelligence/ast"
	"github.com/PizenLabs/ohgo/internal/intelligence/structural"
)

// analysisView runs the adaptive structural engine and renders a bounded
// summary.
//
// The view is the capability boundary, not the engine: the analyzer chooses the
// strategy portfolio, and this function only decides how much of the result is
// useful to a model. Every claim it prints carries the evidence state the
// engine assigned.
func (t *StructureTool) analysisView(
	ctx agent.Context,
	idx *ast.Index,
	intent structural.Intent,
	focus string,
	level structural.Level,
	limit int,
) agent.Result {
	var cache structural.Cache
	if dir := t.source.AnalysisCacheDir(); dir != "" {
		cache = structural.NewFileCache(dir)
	}
	analysis, err := structural.NewAnalyzer(idx, cache).Analyze(ctx.Ctx, structural.Request{
		Intent:   intent,
		Focus:    focus,
		Level:    level,
		MaxFiles: structural.DefaultMaxFiles,
	})
	if err != nil {
		return agent.ErrorResult("structural analysis: %v", err)
	}

	rows := limit
	if rows > 20 {
		rows = 20
	}
	status := "ok"
	if analysis.Coverage.Truncated {
		status = "truncated"
	} else if analysis.Coverage.State != structural.StateObserved || len(analysis.Issues) > 0 {
		status = "partial"
	}

	var text strings.Builder
	writeStructureHeader(&text, status, "analysis", analysis.Snapshot.Source,
		analysis.Coverage.Truncated, analysis.Coverage.Reason)
	fmt.Fprintf(&text, "intent: %s\n", analysis.Request.Intent)
	writePlanText(&text, analysis.Plan)
	writeArchitectureText(&text, analysis.Architecture)
	modules := structural.Summaries(analysis.Snapshot, hierarchyDisplayLevel(analysis.Snapshot), rows)
	writeHierarchyText(&text, analysis.Snapshot, modules, rows)
	writeFocusText(&text, analysis.Focus)
	writeBoundaryText(&text, analysis.Boundaries)
	writeCommunityText(&text, analysis.Communities)
	writeSampleText(&text, analysis.Samples)
	writeCoverageText(&text, analysis.Coverage)
	writeIssueText(&text, analysis.Issues)
	fmt.Fprintf(&text, "cache: %s%s\n", analysis.Cache, cacheReasonSuffix(analysis.CacheReason))

	details := map[string]any{
		"status":              status,
		"view":                "analysis",
		"intent":              string(analysis.Request.Intent),
		"focus":               analysis.Request.Focus,
		"level":               analysis.Snapshot.Level.String(),
		"plan":                planDetail(analysis.Plan),
		"architecture":        architectureDetail(analysis.Architecture),
		"boundaries":          boundaryDetails(analysis.Boundaries),
		"communities":         communityDetails(analysis.Communities),
		"samples":             sampleDetails(analysis.Samples),
		"coverage":            coverageDetail(analysis.Coverage),
		"issues":              issueDetails(analysis.Issues),
		"cache":               string(analysis.Cache),
		"cache_reason":        analysis.CacheReason,
		"nodes":               nodeDetails(modules),
		"node_total":          len(analysis.Snapshot.Nodes),
		"analysis_state":      string(analysis.Coverage.State),
		"analysis_confidence": analysis.Coverage.Confidence,
		"truncated":           analysis.Coverage.Truncated,
		"reason":              analysis.Coverage.Reason,
	}
	if analysis.Focus != nil {
		details["focus_report"] = focusDetail(*analysis.Focus)
	}
	if len(analysis.Manifests.Manifests) > 0 || analysis.Manifests.Module != "" {
		details["manifests"] = manifestDetail(analysis.Manifests)
	}
	addStructuralProvenance(details, idx, analysis.Coverage.Complete())
	details["complete"] = analysis.Coverage.Complete() && status == "ok"
	details["coordinates"] = nodeCoordinates(modules)
	return agent.Result{Text: strings.TrimRight(text.String(), "\n"), Details: details}
}

// expandView lists the direct children of one structural node.
//
// Expansion is explicit and bounded: nothing is expanded because it exists. The
// caller must name the node (or path) it wants to inspect.
func (t *StructureTool) expandView(
	ctx agent.Context,
	idx *ast.Index,
	nodeArg, pathArg string,
	limit int,
) agent.Result {
	inventory, err := idx.Inventory(ctx.Ctx, ast.InventoryQuery{
		MaxFiles: structural.DefaultMaxFiles,
	})
	if err != nil {
		return agent.ErrorResult("expand: %v", err)
	}
	snapshot := structural.Build(inventory, structural.BuildOptions{
		Level:    structural.LevelFile,
		MaxFiles: structural.DefaultMaxFiles,
	})
	target, ok := expandTarget(snapshot, nodeArg, pathArg)
	if !ok {
		return absenceResult("no structural node %q in the current snapshot; call view analysis first and use a details.nodes id, or pass an existing path",
			firstNonEmpty(nodeArg, pathArg))
	}
	children := snapshot.Children(target.ID)
	sort.SliceStable(children, func(i, j int) bool {
		if children[i].Level != children[j].Level {
			return children[i].Level < children[j].Level
		}
		if children[i].Files != children[j].Files {
			return children[i].Files > children[j].Files
		}
		return children[i].ID < children[j].ID
	})
	truncated := false
	reason := ""
	if len(children) > limit {
		children = children[:limit]
		truncated = true
		reason = "child_limit"
	}

	status := "ok"
	if truncated {
		status = "truncated"
	}
	var text strings.Builder
	writeStructureHeader(&text, status, "expand", snapshot.Source, truncated, reason,
		structureSliceCount{Label: "children", Shown: len(children), Total: len(snapshot.Children(target.ID))})
	fmt.Fprintf(&text, "node: %s (%s, %s) path=%s\n", target.ID, target.Kind, target.Level, displayPath(target.Path))
	fmt.Fprintf(&text, "files: %d tests: %d symbols: %d evidence: %s %s\n",
		target.Files, target.TestFiles, target.Symbols, target.Evidence.State,
		confidenceLabel(target.Evidence.Confidence))
	if summary := classSummary(target.Classes); summary != "" {
		fmt.Fprintf(&text, "classes: %s\n", summary)
	}
	if len(children) == 0 {
		text.WriteString("children: none (a file node has no structural children; use symbols or refs for declarations)\n")
	}
	for _, child := range children {
		fmt.Fprintf(&text, "  - %s %s (%s) files=%d symbols=%d evidence=%s",
			child.ID, displayPath(child.Path), child.Kind, child.Files, child.Symbols, child.Evidence.State)
		if child.Class != "" {
			fmt.Fprintf(&text, " class=%s/%s", child.Class, child.Classification.State)
		}
		text.WriteString("\n")
	}

	details := map[string]any{
		"status":         status,
		"view":           "expand",
		"node":           nodeDetail(target),
		"children":       nodeDetails(children),
		"truncated":      truncated,
		"reason":         reason,
		"level":          target.Level.String(),
		"snapshot_id":    snapshot.Source.SnapshotID,
		"scope_complete": snapshot.Source.ScopeComplete,
	}
	addStructuralProvenance(details, idx, !truncated && snapshot.Coverage.Complete())
	return agent.Result{Text: strings.TrimRight(text.String(), "\n"), Details: details}
}

// expandTarget resolves the node to expand. An explicit node id must exist; a
// path resolves to its deepest node, preferring a container over the project
// node that shares the root path.
func expandTarget(snapshot *structural.Snapshot, nodeArg, pathArg string) (structural.Node, bool) {
	if nodeArg != "" {
		return snapshot.Node(nodeArg)
	}
	best := structural.Node{}
	found := false
	for _, node := range snapshot.Nodes {
		if node.Path != pathArg {
			continue
		}
		if !found || node.Level > best.Level ||
			(node.Level == best.Level && node.Kind == structural.NodeWorkspace) {
			best = node
			found = true
		}
	}
	return best, found
}

func hierarchyDisplayLevel(snapshot *structural.Snapshot) structural.Level {
	if snapshot == nil {
		return structural.LevelModule
	}
	if len(snapshot.AtLevel(structural.LevelModule)) > 0 {
		return structural.LevelModule
	}
	if len(snapshot.AtLevel(structural.LevelWorkspace)) > 0 {
		return structural.LevelWorkspace
	}
	return structural.LevelProject
}

func writePlanText(text *strings.Builder, plan structural.Plan) {
	fmt.Fprintf(text, "plan: level=%s stop=%s confidence=%.2f threshold=%.2f cost=%s\n",
		plan.Level, plan.StopReason, plan.EstimatedConfidence, plan.Threshold, plan.Estimated.Band)
	if plan.Reason != "" {
		fmt.Fprintf(text, "plan_reason: %s\n", plan.Reason)
	}
	fmt.Fprintf(text, "steps: %d\n", len(plan.Selected))
	for i, step := range plan.Selected {
		if i >= 8 {
			fmt.Fprintf(text, "  - ... %d more steps\n", len(plan.Selected)-8)
			break
		}
		fmt.Fprintf(text, "  - %s v%s (%s, utility=%.2f): %s\n",
			step.Strategy, step.Version, step.Cost.Band, step.Utility, step.Purpose)
	}
	if len(plan.Skipped) > 0 {
		fmt.Fprintf(text, "skipped: %d\n", len(plan.Skipped))
		for i, skip := range plan.Skipped {
			if i >= 6 {
				fmt.Fprintf(text, "  - ... %d more\n", len(plan.Skipped)-6)
				break
			}
			fmt.Fprintf(text, "  - %s: %s\n", skip.Strategy, skip.Reason)
		}
	}
}

func writeArchitectureText(text *strings.Builder, architecture structural.Architecture) {
	switch architecture.Pattern {
	case "", "not-analyzed":
		text.WriteString("architecture: not analyzed (strategy not selected for this intent)\n")
		return
	}
	fmt.Fprintf(text, "architecture: %s (%s, %s, confidence %.2f)\n",
		architecture.Pattern, architecture.Basis, architecture.State, architecture.Confidence)
	for _, line := range architecture.Evidence {
		fmt.Fprintf(text, "  evidence: %s\n", line)
	}
	if len(architecture.Hints) > 0 {
		fmt.Fprintf(text, "  name_hints (not evidence): %s\n", strings.Join(architecture.Hints, ", "))
	}
}

func writeHierarchyText(text *strings.Builder, snapshot *structural.Snapshot, modules []structural.Node, limit int) {
	if snapshot == nil {
		return
	}
	total := 0
	for _, node := range snapshot.Nodes {
		if node.Level == hierarchyDisplayLevel(snapshot) {
			total++
		}
	}
	label := hierarchyDisplayLevel(snapshot).String()
	fmt.Fprintf(text, "hierarchy: %d %s nodes", total, label)
	if total > len(modules) {
		fmt.Fprintf(text, " (showing %d)", len(modules))
	}
	text.WriteString("\n")
	for _, node := range modules {
		fmt.Fprintf(text, "  - %s %s files=%d tests=%d symbols=%d",
			node.ID, displayPath(node.Path), node.Files, node.TestFiles, node.Symbols)
		if len(node.Languages) > 0 {
			fmt.Fprintf(text, " languages=%s", joinLanguages(node.Languages))
		}
		fmt.Fprintf(text, " evidence=%s", node.Evidence.State)
		text.WriteString("\n")
	}
	if total > len(modules) && len(modules) > 0 {
		fmt.Fprintf(text, "  expand: view=expand node=%s\n", modules[0].ID)
	}
}

func writeFocusText(text *strings.Builder, focus *structural.FocusReport) {
	if focus == nil {
		return
	}
	fmt.Fprintf(text, "focus: %s (%s) found=%v targets=%d references=%d callers=%d importers=%d tests=%d evidence=%s",
		focus.Selector, focus.SelectorKind, focus.Found, focus.Targets,
		focus.References, focus.Callers, focus.Importers, focus.Tests, focus.Evidence.State)
	if focus.Evidence.Reason != "" {
		fmt.Fprintf(text, " (%s)", focus.Evidence.Reason)
	}
	text.WriteString("\n")
	if len(focus.Candidates) > 0 {
		fmt.Fprintf(text, "  candidates: %s\n", strings.Join(focus.Candidates, ", "))
	}
	if focus.Truncated {
		fmt.Fprintf(text, "  truncated: %s\n", focus.Reason)
	}
}

func writeBoundaryText(text *strings.Builder, boundaries []structural.Boundary) {
	if len(boundaries) == 0 {
		return
	}
	fmt.Fprintf(text, "boundaries: %d\n", len(boundaries))
	for i, boundary := range boundaries {
		if i >= 8 {
			fmt.Fprintf(text, "  - ... %d more\n", len(boundaries)-8)
			break
		}
		fmt.Fprintf(text, "  - %s -> %s (%s) crossings=%d evidence=%s\n",
			boundary.From, boundary.To, boundary.Kind, boundary.Crossings, boundary.Evidence.State)
	}
}

func writeCommunityText(text *strings.Builder, communities []structural.Community) {
	if len(communities) == 0 {
		return
	}
	fmt.Fprintf(text, "communities: %d (strategy %s, evidence %s)\n",
		len(communities), structural.ModularityStrategyName, communities[0].Evidence.State)
	for i, community := range communities {
		if i >= 4 {
			fmt.Fprintf(text, "  - ... %d more\n", len(communities)-4)
			break
		}
		members := community.Members
		if len(members) > 5 {
			members = members[:5]
		}
		fmt.Fprintf(text, "  - %s members=%d internal=%d external=%d [%s]\n",
			community.ID, len(community.Members), community.Internal, community.External,
			strings.Join(members, ", "))
	}
}

func writeSampleText(text *strings.Builder, samples []structural.Sample) {
	if len(samples) == 0 {
		return
	}
	text.WriteString("samples:\n")
	for i, sample := range samples {
		if i >= 6 {
			fmt.Fprintf(text, "  - ... %d more\n", len(samples)-6)
			break
		}
		fmt.Fprintf(text, "  - %s (%s): %s\n", displayPath(sample.Path), sample.NodeID, sample.Reason)
	}
}

func writeCoverageText(text *strings.Builder, coverage structural.Coverage) {
	fmt.Fprintf(text, "coverage: state=%s confidence=%.2f level=%s\n",
		coverage.State, coverage.Confidence, coverage.Level)
	fmt.Fprintf(text, "  files=%d materialized=%d parsed=%d pending=%d failed=%d\n",
		coverage.Files, coverage.Materialized, coverage.Parsed, coverage.Pending, coverage.Failed)
	if coverage.DependenciesSeen {
		fmt.Fprintf(text, "  dependencies: nodes=%d edges=%d unresolved=%d\n",
			coverage.GraphNodes, coverage.GraphEdges, coverage.Unresolved)
	} else {
		text.WriteString("  dependencies: not analyzed\n")
	}
}

func writeIssueText(text *strings.Builder, issues []structural.Issue) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintf(text, "issues: %d\n", len(issues))
	for i, issue := range issues {
		if i >= 8 {
			fmt.Fprintf(text, "  - ... %d more\n", len(issues)-8)
			break
		}
		fmt.Fprintf(text, "  - %s: %s\n", issue.Code, issue.Detail)
	}
}

func cacheReasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

func classSummary(classes map[structural.SourceClass]int) string {
	if len(classes) == 0 {
		return ""
	}
	names := make([]string, 0, len(classes))
	for class := range classes {
		names = append(names, string(class))
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, classes[structural.SourceClass(name)]))
	}
	return strings.Join(parts, " ")
}

func joinLanguages(languages []ast.Language) string {
	parts := make([]string, 0, len(languages))
	for _, language := range languages {
		parts = append(parts, string(language))
	}
	return strings.Join(parts, ",")
}

func displayPath(path string) string {
	if path == "" || path == "." {
		return "."
	}
	return path
}

func confidenceLabel(value float64) string {
	return fmt.Sprintf("%.2f", value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func planDetail(plan structural.Plan) map[string]any {
	steps := make([]map[string]any, 0, len(plan.Selected))
	for _, step := range plan.Selected {
		steps = append(steps, map[string]any{
			"strategy": step.Strategy,
			"version":  step.Version,
			"purpose":  step.Purpose,
			"level":    step.Level.String(),
			"utility":  step.Utility,
			"cost":     costDetail(step.Cost),
			"reason":   step.Reason,
		})
	}
	skipped := make([]map[string]any, 0, len(plan.Skipped))
	for _, skip := range plan.Skipped {
		skipped = append(skipped, map[string]any{
			"strategy": skip.Strategy,
			"reason":   skip.Reason,
		})
	}
	return map[string]any{
		"intent":               string(plan.Intent),
		"focus":                plan.Focus,
		"level":                plan.Level.String(),
		"selected":             steps,
		"skipped":              skipped,
		"estimated":            costDetail(plan.Estimated),
		"estimated_confidence": plan.EstimatedConfidence,
		"threshold":            plan.Threshold,
		"stop_reason":          plan.StopReason,
		"reason":               plan.Reason,
	}
}

func costDetail(cost structural.Cost) map[string]any {
	return map[string]any{
		"files":       cost.Files,
		"graph_nodes": cost.GraphNodes,
		"graph_edges": cost.GraphEdges,
		"parse_work":  cost.ParseWork,
		"units":       cost.Units,
		"band":        cost.Band,
	}
}

func evidenceDetail(evidence structural.Evidence) map[string]any {
	refs := make([]map[string]any, 0, len(evidence.Refs))
	for _, ref := range evidence.Refs {
		refs = append(refs, map[string]any{
			"kind":    string(ref.Kind),
			"path":    ref.Path,
			"line":    ref.Line,
			"adapter": ref.Adapter,
			"rule":    ref.Rule,
		})
	}
	return map[string]any{
		"state":      string(evidence.State),
		"confidence": evidence.Confidence,
		"band":       evidence.Band(),
		"reason":     evidence.Reason,
		"refs":       refs,
	}
}

func architectureDetail(architecture structural.Architecture) map[string]any {
	return map[string]any{
		"pattern":      architecture.Pattern,
		"confidence":   architecture.Confidence,
		"state":        string(architecture.State),
		"basis":        architecture.Basis,
		"evidence":     append([]string(nil), architecture.Evidence...),
		"name_hints":   append([]string(nil), architecture.Hints...),
		"alternatives": append([]string(nil), architecture.Alternatives...),
	}
}

func nodeDetail(node structural.Node) map[string]any {
	classes := map[string]int{}
	for class, count := range node.Classes {
		classes[string(class)] = count
	}
	languages := make([]string, 0, len(node.Languages))
	for _, language := range node.Languages {
		languages = append(languages, string(language))
	}
	return map[string]any{
		"id":             node.ID,
		"kind":           string(node.Kind),
		"level":          node.Level.String(),
		"name":           node.Name,
		"path":           node.Path,
		"parent":         node.Parent,
		"class":          string(node.Class),
		"classification": evidenceDetail(node.Classification),
		"languages":      languages,
		"files":          node.Files,
		"test_files":     node.TestFiles,
		"symbols":        node.Symbols,
		"packages":       append([]string(nil), node.Packages...),
		"classes":        classes,
		"evidence":       evidenceDetail(node.Evidence),
	}
}

func nodeDetails(nodes []structural.Node) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, nodeDetail(node))
	}
	return out
}

func nodeCoordinates(nodes []structural.Node) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		coordinate := map[string]any{
			"node_id": node.ID,
			"kind":    string(node.Kind),
			"path":    node.Path,
			"files":   node.Files,
			"symbols": node.Symbols,
		}
		if len(node.Evidence.Refs) > 0 {
			coordinate["path"] = node.Evidence.Refs[0].Path
			coordinate["line"] = node.Evidence.Refs[0].Line
		}
		out = append(out, coordinate)
	}
	return out
}

func boundaryDetails(boundaries []structural.Boundary) []map[string]any {
	out := make([]map[string]any, 0, len(boundaries))
	for _, boundary := range boundaries {
		out = append(out, map[string]any{
			"from":      boundary.From,
			"to":        boundary.To,
			"kind":      string(boundary.Kind),
			"crossings": boundary.Crossings,
			"evidence":  evidenceDetail(boundary.Evidence),
		})
	}
	return out
}

func communityDetails(communities []structural.Community) []map[string]any {
	out := make([]map[string]any, 0, len(communities))
	for _, community := range communities {
		members := community.Members
		if len(members) > 12 {
			members = members[:12]
		}
		out = append(out, map[string]any{
			"id":           community.ID,
			"members":      append([]string(nil), members...),
			"member_total": len(community.Members),
			"internal":     community.Internal,
			"external":     community.External,
			"evidence":     evidenceDetail(community.Evidence),
		})
	}
	return out
}

func sampleDetails(samples []structural.Sample) []map[string]any {
	out := make([]map[string]any, 0, len(samples))
	for _, sample := range samples {
		out = append(out, map[string]any{
			"path":     sample.Path,
			"node_id":  sample.NodeID,
			"reason":   sample.Reason,
			"score":    sample.Score,
			"evidence": evidenceDetail(sample.Evidence),
		})
	}
	return out
}

func coverageDetail(coverage structural.Coverage) map[string]any {
	return map[string]any{
		"level":              coverage.Level.String(),
		"files":              coverage.Files,
		"materialized_files": coverage.Materialized,
		"parsed_files":       coverage.Parsed,
		"pending_files":      coverage.Pending,
		"failed_files":       coverage.Failed,
		"graph_nodes":        coverage.GraphNodes,
		"graph_edges":        coverage.GraphEdges,
		"unresolved":         coverage.Unresolved,
		"dependencies_seen":  coverage.DependenciesSeen,
		"truncated":          coverage.Truncated,
		"reason":             coverage.Reason,
		"state":              string(coverage.State),
		"confidence":         coverage.Confidence,
		"complete":           coverage.Complete(),
	}
}

func issueDetails(issues []structural.Issue) []map[string]any {
	out := make([]map[string]any, 0, len(issues))
	for _, issue := range issues {
		out = append(out, map[string]any{
			"code":   issue.Code,
			"detail": issue.Detail,
		})
	}
	return out
}

func focusDetail(focus structural.FocusReport) map[string]any {
	return map[string]any{
		"selector":      focus.Selector,
		"selector_kind": focus.SelectorKind,
		"found":         focus.Found,
		"ambiguous":     focus.Ambiguous,
		"candidates":    append([]string(nil), focus.Candidates...),
		"targets":       focus.Targets,
		"references":    focus.References,
		"callers":       focus.Callers,
		"importers":     focus.Importers,
		"tests":         focus.Tests,
		"nodes":         focus.Nodes,
		"edges":         focus.Edges,
		"truncated":     focus.Truncated,
		"reason":        focus.Reason,
		"evidence":      evidenceDetail(focus.Evidence),
	}
}

func manifestDetail(topology structural.ManifestTopology) map[string]any {
	facts := make([]map[string]any, 0, len(topology.Manifests))
	for _, fact := range topology.Manifests {
		facts = append(facts, map[string]any{
			"path":      fact.Path,
			"kind":      fact.Kind,
			"framework": fact.Framework,
			"language":  fact.Language,
		})
	}
	return map[string]any{
		"module":       topology.Module,
		"manifests":    facts,
		"workspaces":   append([]string(nil), topology.Workspaces...),
		"multi_module": topology.MultiModule,
		"evidence":     evidenceDetail(topology.Evidence),
	}
}
