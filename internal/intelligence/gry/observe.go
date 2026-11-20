package gry

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/investigate"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
)

// observationBuilder accumulates one bounded observation. It is a parser
// helper, not a second evidence store: every fact and record it produces is
// published into the shared investigate/evidence model.
type observationBuilder struct {
	obs       investigate.Observation
	project   string
	snapshot  string
	freshness evidence.Freshness
	scope     evidence.Scope
}

// internalPackage is the bounded projection of one architecture package row.
// External packages are excluded before this type is populated.
type internalPackage struct {
	Name         string
	QN           string
	Ecosystem    string
	Dependents   int
	Dependencies int
}

func (p *Provider) observation(project, generation, focus string) *observationBuilder {
	snapshot := "gry:" + project
	if generation != "" {
		snapshot += ":" + generation
	}
	return &observationBuilder{
		obs: investigate.Observation{
			Outcome:    investigate.OutcomeSuccess,
			Source:     "gry",
			State:      evidence.Observed,
			Confidence: 0.55,
			Coverage:   0.45,
		},
		project:   project,
		snapshot:  snapshot,
		freshness: evidence.FreshnessUnknown,
		scope:     evidence.Scope{Workspace: p.cfg.Root, Path: focus},
	}
}

func (b *observationBuilder) fact(subject, detail string) {
	b.obs.Facts = append(b.obs.Facts, investigate.Fact{
		Provider: string(planner.ProviderStructural),
		Subject:  subject,
		Detail:   detail,
	})
}

func (b *observationBuilder) record(subject, relation, object string, state evidence.State,
	confidence float64, refs []evidence.Ref, note string) {
	b.obs.Records = append(b.obs.Records, evidence.Record{
		Kind:       evidence.KindRelationship,
		Provider:   evidence.ProviderStructural,
		Source:     evidence.SourceGRY,
		Subject:    subject,
		Relation:   relation,
		Object:     object,
		State:      state,
		Confidence: confidence,
		Freshness:  b.freshness,
		Scope:      b.scope,
		Refs:       refs,
		SnapshotID: b.snapshot,
		Note:       note,
	})
}

func (b *observationBuilder) issue(format string, args ...any) {
	b.obs.Issues = append(b.obs.Issues, fmt.Sprintf(format, args...))
}

// edgeSubject identifies one graph edge as a functional claim.
//
// Correlation groups by (subject, relation) and treats multiple objects as
// disagreement. A symbol can legitimately have many callees, so the edge — not
// the source symbol — is the subject. Two mechanisms observing the same edge
// still agree; two different edges cannot be mistaken for a contradiction.
func edgeSubject(source, target string) string {
	return "edge:" + source + "->" + target
}

func (b *observationBuilder) finish() investigate.Observation {
	return b.obs
}

func (p *Provider) query(ctx context.Context, name string, args []string) ([]byte, *failure) {
	stdout, stderr, err := p.run(ctx, p.cfg, args)
	if err != nil {
		return nil, failureFromCommand(name, err, stderr)
	}
	return stdout, nil
}

func decodeResponse[T any](raw []byte) (T, *failure) {
	var payload T
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, &failure{
			investigate.OutcomeFailed,
			"invalid_output",
			"gry returned invalid JSON",
		}
	}
	return payload, nil
}

// searchSymbols asks gry for an exact symbol first, then falls back to its
// ranked search. The ranked result for "NewOrder" is dominated by
// NewOrderView/NewOrderSummary and can omit the exact declaration; a locate
// question that names a symbol must not be answered by lexical neighbours.
func (p *Provider) searchSymbols(ctx context.Context, project, target string) (searchResponse, *failure) {
	exactPattern := "^" + regexp.QuoteMeta(target) + "$"
	raw, queryFailure := p.query(ctx, "search", []string{
		"search", project,
		"--name-pattern", exactPattern,
		"--limit", strconv.Itoa(p.cfg.MaxRows),
		"--json",
	})
	if queryFailure == nil {
		if payload, decodeFailure := decodeResponse[searchResponse](raw); decodeFailure == nil && len(payload.Results) > 0 {
			return payload, nil
		}
	} else if queryFailure.outcome != investigate.OutcomeUnresolved {
		return searchResponse{}, queryFailure
	}
	raw, queryFailure = p.query(ctx, "search", []string{
		"search", project, target,
		"--limit", strconv.Itoa(p.cfg.MaxRows),
		"--json",
	})
	if queryFailure != nil {
		return searchResponse{}, queryFailure
	}
	payload, decodeFailure := decodeResponse[searchResponse](raw)
	if decodeFailure != nil {
		return searchResponse{}, decodeFailure
	}
	return payload, nil
}

// observeResolve answers a target-shaped locate question. Search is ranked, so
// multiple candidates are preserved; the adapter never chooses one silently.
func (p *Provider) observeResolve(ctx context.Context, project string, req investigate.Request) investigate.Observation {
	target := strings.TrimSpace(req.Focus)
	builder := p.observation(project, "", target)
	if target == "" {
		builder.obs.Outcome = investigate.OutcomeUnavailable
		builder.issue("gry resolve requires a target; no query was sent")
		return builder.finish()
	}
	payload, queryFailure := p.searchSymbols(ctx, project, target)
	if queryFailure != nil {
		return failureObservation(queryFailure)
	}
	if len(payload.Results) == 0 {
		builder.obs.Outcome = investigate.OutcomeUnresolved
		builder.obs.State = evidence.Unknown
		builder.obs.Confidence = 0.15
		builder.issue("gry resolved no symbol for %q; this is not proof that the symbol does not exist", target)
		return builder.finish()
	}
	if payload.Total < len(payload.Results) {
		payload.Total = len(payload.Results)
	}
	builder.obs.Summary = fmt.Sprintf(
		"gry resolve: %d of %d candidates for %s", payload.Returned, payload.Total, target)
	for _, result := range payload.Results {
		subject := result.QN
		if subject == "" {
			subject = result.Name
		}
		builder.fact(subject, fmt.Sprintf("%s %s (in %d / out %d)",
			result.Label, location(result.File, result.Lines), result.In, result.Out))
		builder.record("symbol:"+subject, "declared_at", location(result.File, result.Lines),
			evidence.Observed, 0.8, refs(result.File, result.Lines), result.Label)
	}
	if len(payload.Results) > 1 {
		builder.obs.Outcome = investigate.OutcomePartial
		builder.issue("%d candidates matched %q; no single target was selected", payload.Total, target)
	}
	if payload.Total > len(payload.Results) {
		builder.obs.Outcome = investigate.OutcomePartial
		builder.issue("bounded result: showing %d of %d candidates", len(payload.Results), payload.Total)
	}
	builder.obs.Confidence = 0.65
	return builder.finish()
}

// observeTrace answers a flow or reverse-dependency question. The target must
// resolve uniquely; an ambiguous short name is preserved as ambiguity, never
// guessed.
func (p *Provider) observeTrace(ctx context.Context, project string, req investigate.Request, direction string) investigate.Observation {
	target := strings.TrimSpace(req.Focus)
	builder := p.observation(project, "", target)
	if target == "" {
		builder.obs.Outcome = investigate.OutcomeUnavailable
		builder.issue("gry trace requires a target; no query was sent")
		return builder.finish()
	}
	raw, queryFailure := p.query(ctx, "trace", []string{
		"trace", project, target,
		"--direction", direction,
		"--depth", strconv.Itoa(p.cfg.MaxDepth),
		"--limit", strconv.Itoa(p.cfg.MaxRows),
		// Test call sites are evidence for impact and test-impact questions.
		// Omitting them made a symbol with thirteen test callers look unused.
		"--include-tests",
		"--json",
	})
	if queryFailure != nil {
		return failureObservation(queryFailure)
	}
	payload, decodeFailure := decodeResponse[traceResponse](raw)
	if decodeFailure != nil {
		return failureObservation(decodeFailure)
	}
	if payload.Ambiguous {
		return ambiguousTrace(builder, target, payload)
	}
	if !payload.Found {
		builder.obs.Outcome = investigate.OutcomeUnresolved
		builder.obs.State = evidence.Unknown
		builder.obs.Confidence = 0.2
		message := strings.TrimSpace(payload.Message)
		if message == "" {
			message = "the target was not resolved"
		}
		builder.issue("gry trace did not resolve %q: %s; this is not proof that the target does not exist", target, message)
		return builder.finish()
	}
	start := payload.Start.QN
	if start == "" {
		start = target
	}
	builder.obs.Summary = fmt.Sprintf(
		"gry trace: %d of %d %s relationships for %s (depth %d)",
		payload.Returned, payload.Total, direction, start, p.cfg.MaxDepth)
	if len(payload.Rows) == 0 {
		builder.obs.Outcome = investigate.OutcomeUnresolved
		builder.obs.State = evidence.Unknown
		builder.obs.Confidence = 0.25
		builder.issue("gry observed no %s relationship for %q; this is not proof that no relationship exists", direction, target)
		return builder.finish()
	}
	for _, row := range payload.Rows {
		state := evidence.Inferred
		confidence := 0.5
		if row.Observed {
			state = evidence.Observed
			confidence = 0.8
		}
		relation := row.Edge
		if relation == "" {
			relation = "related_to"
		}
		edgeSource, edgeTarget := start, row.QN
		if row.Direction == "inbound" {
			edgeSource, edgeTarget = row.QN, start
		}
		builder.fact(row.QN, fmt.Sprintf("%s %s %s hop %d via %s",
			relation, row.Direction, location(row.File, row.Lines), row.Hop, start))
		builder.record(edgeSubject("symbol:"+edgeSource, "symbol:"+edgeTarget), relation, "observed",
			state, confidence, refs(row.File, row.Lines),
			fmt.Sprintf("hop=%d direction=%s via=%s", row.Hop, row.Direction, row.ViaQN))
	}
	if payload.Truncated || payload.Total > payload.Returned {
		builder.obs.Outcome = investigate.OutcomePartial
		if payload.Total > payload.Returned {
			builder.issue("bounded traversal: showing %d of %d relationship rows; the rest were not retrieved",
				payload.Returned, payload.Total)
		} else {
			builder.issue("bounded traversal: gry truncated the result at %d rows", payload.Returned)
		}
	}
	builder.obs.Confidence = 0.65
	return builder.finish()
}

func ambiguousTrace(builder *observationBuilder, target string, payload traceResponse) investigate.Observation {
	builder.obs.Outcome = investigate.OutcomeAmbiguous
	builder.obs.State = evidence.PartiallyObserved
	builder.obs.Confidence = 0.3
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = "the target is ambiguous"
	}
	builder.issue("gry trace reported an ambiguous target for %q: %s; no candidate was selected", target, message)
	for _, candidate := range payload.Candidates {
		builder.fact("candidate:"+candidate.QN, fmt.Sprintf("%s %s", candidate.Label, location(candidate.File, candidate.Lines)))
	}
	builder.obs.Summary = fmt.Sprintf("gry trace: %d ambiguous candidates for %s", len(payload.Candidates), target)
	return builder.finish()
}

// observeContext answers an open structural question with gry's bounded task
// context: related nodes, relationships, tests, and explicit unknowns.
func (p *Provider) observeContext(ctx context.Context, project string, req investigate.Request) investigate.Observation {
	builder := p.observation(project, "", req.Focus)
	args := []string{
		"context", project,
		"--task", truncate(strings.TrimSpace(req.Question), 512),
		"--profile", "context",
		"--depth", strconv.Itoa(p.cfg.MaxDepth),
		"--max-nodes", strconv.Itoa(p.cfg.MaxNodes),
		"--max-tokens", strconv.Itoa(p.cfg.MaxTokens),
		"--timeout-ms", strconv.Itoa(int(p.cfg.Timeout / time.Millisecond)),
		"--json",
	}
	if focus := strings.TrimSpace(req.Focus); focus != "" {
		args = append(args, "--target", focus)
	}
	raw, queryFailure := p.query(ctx, "context", args)
	if queryFailure != nil {
		return failureObservation(queryFailure)
	}
	payload, decodeFailure := decodeResponse[contextResponse](raw)
	if decodeFailure != nil {
		return failureObservation(decodeFailure)
	}
	return contextObservation(builder, payload)
}

func contextObservation(builder *observationBuilder, payload contextResponse) investigate.Observation {
	builder.snapshot = "gry:" + builder.project
	if payload.Generation != "" {
		builder.snapshot = "gry:" + builder.project + ":" + payload.Generation
	}
	builder.freshness = freshness(payload.Freshness.Freshness, payload.Freshness.Stale)
	if builder.freshness == evidence.FreshnessUnknown {
		builder.freshness = freshness(payload.Evidence.Freshness, false)
	}
	if payload.Status == "stale" || payload.Freshness.Stale {
		builder.obs.Outcome = investigate.OutcomeStale
		builder.obs.Confidence = 0.4
		if builder.freshness == evidence.FreshnessUnknown {
			builder.freshness = evidence.FreshnessStale
		}
	}

	nodes := payload.Nodes
	relationships := payload.Relationships
	tests := payload.Tests
	if len(nodes) == 0 && len(relationships) == 0 && len(tests) == 0 {
		builder.obs.Outcome = investigate.OutcomeUnresolved
		builder.obs.State = evidence.Unknown
		builder.obs.Confidence = 0.2
		builder.issue("gry assembled no structurally related node for this task; this is not proof that the concept is absent")
		builder.obs.Summary = "gry context: no related node"
		return builder.finish()
	}

	const maxContextRelationships = 6
	const maxContextNodes = 6
	const maxContextTests = 4
	groupOmitted := 0
	for index, relationship := range relationships {
		if index >= maxContextRelationships {
			groupOmitted += len(relationships) - maxContextRelationships
			break
		}
		state := evidence.Inferred
		confidence := 0.5
		if relationship.Observed {
			state = evidence.Observed
			confidence = 0.8
		}
		relation := relationship.Type
		if relation == "" {
			relation = "related_to"
		}
		edgeSource, edgeTarget := relationship.Source, relationship.Target
		if relationship.Direction == "inbound" {
			edgeSource, edgeTarget = edgeTarget, edgeSource
		}
		builder.fact(relationship.Source, fmt.Sprintf("%s %s", relation, relationship.Target))
		builder.record(edgeSubject("symbol:"+edgeSource, "symbol:"+edgeTarget),
			relation, "observed", state, confidence, nil, relationship.Provenance)
	}
	for index, node := range nodes {
		if index >= maxContextNodes {
			groupOmitted += len(nodes) - maxContextNodes
			break
		}
		builder.fact(node.QN, fmt.Sprintf("%s role=%s distance=%d %s",
			node.Label, node.Role, node.Distance, location(node.File, node.Lines)))
		builder.record("symbol:"+node.QN, "appears_in_context", "task",
			provenanceState(node.Provenance), provenanceConfidence(node.Provenance),
			refs(node.File, node.Lines), node.Role)
	}
	for index, test := range tests {
		if index >= maxContextTests {
			groupOmitted += len(tests) - maxContextTests
			break
		}
		builder.fact("test:"+test.Name, location(test.File, test.Lines))
		builder.record("test:"+test.QN, "related_to", "task",
			provenanceState(test.Provenance), provenanceConfidence(test.Provenance), refs(test.File, test.Lines), "")
	}
	if groupOmitted > 0 {
		builder.issue("bounded context: %d additional node, relationship, or test rows were omitted", groupOmitted)
		if builder.obs.Outcome == investigate.OutcomeSuccess {
			builder.obs.Outcome = investigate.OutcomePartial
		}
	}

	builder.obs.Summary = fmt.Sprintf(
		"gry context: %d nodes, %d relationships, %d tests (%s)",
		len(nodes), len(relationships), len(tests), builder.freshness)
	if payload.Truncated || len(payload.Unknowns) > 0 {
		if builder.obs.Outcome == investigate.OutcomeSuccess {
			builder.obs.Outcome = investigate.OutcomePartial
		}
		for _, unknown := range payload.Unknowns {
			detail := unknown.Message
			if detail == "" {
				detail = unknown.Kind
			}
			if unknown.Omitted > 0 {
				detail += fmt.Sprintf(" (%d omitted)", unknown.Omitted)
			}
			builder.issue("gry context bound: %s", detail)
		}
	}
	if payload.Evidence.Limitation != "" {
		builder.issue("gry context scope: %s", payload.Evidence.Limitation)
	}
	for _, reason := range payload.Evidence.Reasons {
		builder.issue("gry context limitation: %s", reason)
	}
	for _, warning := range payload.Warnings {
		builder.issue("gry warning: %s", warning)
	}
	if !payload.Evidence.CompleteForScope {
		builder.issue("gry did not claim repository-wide completeness for this context")
	}
	if len(payload.Coverage.Gaps) > 0 {
		builder.issue("gry reported %d coverage gap(s) for the paths it checked", len(payload.Coverage.Gaps))
	}
	builder.obs.Confidence = 0.6
	return builder.finish()
}

// observeArchitecture answers a repository-shape question. The raw response can
// contain every package; the adapter keeps the largest internal packages and
// reports the bound.
func (p *Provider) observeArchitecture(ctx context.Context, project string, req investigate.Request) investigate.Observation {
	builder := p.observation(project, "", req.Focus)
	args := []string{"architecture", project, "--json"}
	if focus := strings.TrimSpace(req.Focus); focus != "" {
		args = append(args, "--path", focus)
	}
	raw, queryFailure := p.query(ctx, "architecture", args)
	if queryFailure != nil {
		return failureObservation(queryFailure)
	}
	payload, decodeFailure := decodeResponse[architectureResponse](raw)
	if decodeFailure != nil {
		return failureObservation(decodeFailure)
	}
	builder.snapshot = "gry:" + builder.project
	if payload.Generation != "" {
		builder.snapshot = "gry:" + builder.project + ":" + payload.Generation
	}
	if len(payload.Languages) == 0 && len(payload.Packages) == 0 && len(payload.EntryPoints) == 0 {
		builder.obs.Outcome = investigate.OutcomeUnresolved
		builder.obs.State = evidence.Unknown
		builder.obs.Confidence = 0.2
		builder.issue("gry returned no architecture observation for this workspace; this is not proof that the repository has no structure")
		return builder.finish()
	}

	const maxLanguages = 4
	const maxPackages = 8
	const maxEntryPoints = 4
	bounded := 0
	for index, language := range payload.Languages {
		if index >= maxLanguages {
			bounded += len(payload.Languages) - maxLanguages
			break
		}
		builder.fact("language:"+language.Language,
			fmt.Sprintf("%d files, %d lines", language.Files, language.Lines))
		builder.record("language:"+language.Language, "implemented_in", "repository",
			evidence.Observed, 0.85, nil, "")
	}

	internal := make([]internalPackage, 0, len(payload.Packages))
	for _, pkg := range payload.Packages {
		if pkg.External {
			continue
		}
		internal = append(internal, internalPackage{
			Name: pkg.Name, QN: pkg.QN, Ecosystem: pkg.Ecosystem,
			Dependents: pkg.Dependents, Dependencies: pkg.Dependencies,
		})
	}
	sort.SliceStable(internal, func(i, j int) bool {
		if internal[i].Dependents != internal[j].Dependents {
			return internal[i].Dependents > internal[j].Dependents
		}
		return internal[i].Name < internal[j].Name
	})
	for index, pkg := range internal {
		if index >= maxPackages {
			bounded += len(internal) - maxPackages
			break
		}
		builder.fact("package:"+pkg.QN, fmt.Sprintf("%d dependents, %d dependencies, %s",
			pkg.Dependents, pkg.Dependencies, pkg.Ecosystem))
		builder.record("package:"+pkg.QN, "has_dependents", strconv.Itoa(pkg.Dependents),
			evidence.Observed, 0.75, nil, pkg.Name)
	}

	entryPoints := 0
	for _, entry := range payload.EntryPoints {
		if strings.HasSuffix(entry.File, "_test.go") {
			continue
		}
		if entryPoints >= maxEntryPoints {
			bounded++
			continue
		}
		builder.fact("entrypoint:"+entry.QN, location(entry.File, entry.Lines))
		entryPoints++
	}

	builder.obs.Summary = fmt.Sprintf(
		"gry architecture: %d languages, %d internal packages, %d entry points, %d nodes / %d edges",
		len(payload.Languages), len(internal), entryPoints, payload.Nodes, payload.Edges)
	if payload.Caveat != "" {
		builder.issue("gry architecture caveat: %s", payload.Caveat)
	}
	if bounded > 0 {
		builder.obs.Outcome = investigate.OutcomePartial
		builder.issue("bounded architecture: %d additional rows were omitted by the adapter", bounded)
	}
	builder.obs.Confidence = 0.7
	return builder.finish()
}

// observeChanges answers a change-impact or test-impact question from the
// current Git diff. An empty diff is unresolved, not proof of no impact.
func (p *Provider) observeChanges(ctx context.Context, project, scope string) investigate.Observation {
	builder := p.observation(project, "", "")
	raw, queryFailure := p.query(ctx, "changes", []string{
		"changes", project,
		"--scope", scope,
		"--direction", "inbound",
		"--depth", strconv.Itoa(p.cfg.MaxDepth),
		"--json",
	})
	if queryFailure != nil {
		return failureObservation(queryFailure)
	}
	payload, decodeFailure := decodeResponse[changesResponse](raw)
	if decodeFailure != nil {
		return failureObservation(decodeFailure)
	}
	builder.snapshot = "gry:" + builder.project
	if payload.Generation != "" {
		builder.snapshot = "gry:" + builder.project + ":" + payload.Generation
	}
	if !payload.GitAvailable {
		builder.obs.Outcome = investigate.OutcomeUnavailable
		builder.obs.State = evidence.Unknown
		builder.obs.Confidence = 0.1
		builder.issue("gry reports no Git repository for this workspace; change evidence is unavailable, not empty")
		return builder.finish()
	}
	if payload.ChangedFiles.Total == 0 {
		builder.obs.Outcome = investigate.OutcomeUnresolved
		builder.obs.State = evidence.Unknown
		builder.obs.Confidence = 0.15
		builder.issue("gry observed no changed file in this workspace; this is not proof that no change is required or that nothing is impacted")
		builder.obs.Summary = "gry changes: no changed file"
		return builder.finish()
	}

	const maxChanged = 4
	const maxImpact = 6
	const maxModules = 2
	const maxTests = 4
	bounded := 0
	if payload.ChangedFiles.Total > len(payload.ChangedFiles.Rows) {
		bounded += payload.ChangedFiles.Total - len(payload.ChangedFiles.Rows)
	}
	for index, file := range payload.ChangedFiles.Rows {
		if index >= maxChanged {
			bounded += len(payload.ChangedFiles.Rows) - maxChanged
			break
		}
		builder.fact("file:"+file.Path, fmt.Sprintf("changed status=%s", file.Status))
		builder.record("file:"+file.Path, "changed_in_worktree", file.Status,
			evidence.Observed, 0.8, []evidence.Ref{{Path: file.Path}}, "")
	}
	for index, row := range payload.Impact.Rows {
		if index >= maxImpact {
			bounded += len(payload.Impact.Rows) - maxImpact
			break
		}
		relation := row.Edge
		if relation == "" {
			relation = "impacted_by_change"
		}
		builder.fact(row.QN, fmt.Sprintf("%s %s hop %d %s",
			row.Direction, location(row.File, row.Lines), row.Hop, row.Risk))
		builder.record("symbol:"+row.QN, relation, "change",
			provenanceState(row.Provenance), provenanceConfidence(row.Provenance),
			refs(row.File, row.Lines), fmt.Sprintf("hop=%d risk=%s", row.Hop, row.Risk))
	}
	for index, module := range payload.Modules.Rows {
		if index >= maxModules {
			bounded += len(payload.Modules.Rows) - maxModules
			break
		}
		builder.fact("module:"+module.Module, fmt.Sprintf("%d changed files, %d impacted symbols",
			module.ChangedFiles, module.ImpactedSymbols))
	}
	for index, test := range payload.Tests {
		if index >= maxTests {
			bounded += len(payload.Tests) - maxTests
			break
		}
		builder.fact("test:"+test.Name, location(test.File, test.Lines))
		builder.record("test:"+test.QN, "covers_change", "change",
			evidence.Observed, 0.75, refs(test.File, test.Lines), "")
	}

	builder.obs.Summary = fmt.Sprintf(
		"gry changes(%s): %d changed files, %d seeds, %d impacted symbols, %d tests",
		scope, payload.ChangedFiles.Total, payload.Seeds.Total, payload.Impact.Total, len(payload.Tests))
	if scope == "tests" {
		builder.obs.Coverage = 0.4
	}
	if payload.Note != "" {
		builder.issue("gry changes note: %s", payload.Note)
	}
	if bounded > 0 || payload.Impact.Total > payload.Impact.Returned {
		builder.obs.Outcome = investigate.OutcomePartial
		if bounded > 0 {
			builder.issue("bounded change evidence: %d additional rows were omitted by the adapter", bounded)
		}
		if payload.Impact.Total > payload.Impact.Returned {
			builder.issue("bounded impact: showing %d of %d impacted symbols",
				payload.Impact.Returned, payload.Impact.Total)
		}
	}
	builder.obs.Confidence = 0.65
	return builder.finish()
}

func freshness(value string, stale bool) evidence.Freshness {
	if stale {
		return evidence.FreshnessStale
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fresh", "current":
		return evidence.FreshnessCurrent
	case "stale":
		return evidence.FreshnessStale
	default:
		return evidence.FreshnessUnknown
	}
}

// provenanceState maps gry's provenance onto Ohgo's evidence vocabulary.
//
// A verified structural relationship is still an observation: it proves what
// gry indexed, not that the user's objective is established. VERIFIED is
// reserved for Ohgo's explicit proof constructor and is never produced here.
func provenanceState(provenance string) evidence.State {
	switch strings.ToLower(strings.TrimSpace(provenance)) {
	case "verified", "declared", "observed":
		return evidence.Observed
	case "inferred":
		return evidence.Inferred
	default:
		return evidence.Unknown
	}
}

func provenanceConfidence(provenance string) float64 {
	switch strings.ToLower(strings.TrimSpace(provenance)) {
	case "verified":
		return 0.8
	case "declared", "observed":
		return 0.75
	case "inferred":
		return 0.5
	default:
		return 0.35
	}
}

func location(file string, lines []int) string {
	if file == "" {
		return "unknown"
	}
	if len(lines) >= 2 && lines[0] > 0 {
		return fmt.Sprintf("%s:%d-%d", file, lines[0], lines[1])
	}
	if len(lines) >= 1 && lines[0] > 0 {
		return fmt.Sprintf("%s:%d", file, lines[0])
	}
	return file
}

func refs(file string, lines []int) []evidence.Ref {
	if file == "" {
		return nil
	}
	ref := evidence.Ref{Path: file}
	if len(lines) >= 1 {
		ref.Line = lines[0]
	}
	return []evidence.Ref{ref}
}
