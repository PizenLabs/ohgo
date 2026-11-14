package investigate

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
	"github.com/PizenLabs/ohgo/internal/intelligence/retrieval"
	"github.com/PizenLabs/ohgo/internal/intelligence/structural"
)

// StructuralProvider adapts the adaptive structural analyzer to the provider
// contract. It observes; it never runs a strategy the planner did not select.
type StructuralProvider struct {
	Source structural.Source
	Cache  structural.Cache
	Root   string
}

// Name implements Provider.
func (p *StructuralProvider) Name() planner.Provider { return planner.ProviderStructural }

// Available implements Provider.
func (p *StructuralProvider) Available(context.Context) bool { return p != nil && p.Source != nil }

// Observe implements Provider.
func (p *StructuralProvider) Observe(ctx context.Context, req Request, step planner.Step, _ *Meter) (Observation, error) {
	intent, focus, ok := structuralRequest(req)
	if !ok {
		return Observation{Issues: []string{"semantic request has no structural intent"}}, nil
	}
	analysis, err := structural.NewAnalyzer(p.Source, p.Cache).Analyze(ctx, structural.Request{
		Intent: intent, Focus: focus, MaxFiles: 4000, MaxSteps: 5,
	})
	if err != nil {
		return Observation{}, fmt.Errorf("structural analysis: %w", err)
	}
	snapshot := fmt.Sprintf("structural:%s:%d", analysis.AnalyzerVersion, analysis.Coverage.Files)
	scope := evidence.Scope{Workspace: p.Root, Path: focus}
	observation := Observation{
		State:      analysis.Coverage.State,
		Confidence: analysis.Coverage.Confidence,
		Coverage:   structuralCoverage(intent, analysis),
		Summary: fmt.Sprintf("%d files, %d modules, architecture %s (%s), coverage %s",
			analysis.Coverage.Files, len(analysis.Manifests.Manifests),
			analysis.Architecture.Pattern, analysis.Architecture.State, analysis.Coverage.State),
	}
	if observation.Confidence <= 0 {
		observation.Confidence = 0.4
	}
	record := func(subject, relation, object string, state evidence.State, confidence float64, note string) {
		observation.Records = append(observation.Records, evidence.Record{
			Kind: evidence.KindRelationship, Provider: evidence.ProviderStructural,
			Source: evidence.SourceAST, Subject: subject, Relation: relation, Object: object,
			State: state, Confidence: confidence, Freshness: evidence.FreshnessCurrent,
			Scope: scope, SnapshotID: snapshot, Note: capText(note, maxSummaryChars),
		})
	}
	for _, manifest := range analysis.Manifests.Manifests {
		record("manifest:"+manifest.Path, "declares", manifest.Kind, evidence.Observed, 0.9,
			strings.TrimSpace(manifest.Language+" "+manifest.Framework))
		observation.Facts = append(observation.Facts, Fact{Provider: string(p.Name()),
			Subject: manifest.Path, Detail: strings.TrimSpace(manifest.Kind + " " + manifest.Language + " " + manifest.Framework)})
	}
	for _, workspace := range analysis.Manifests.Workspaces {
		record("workspace:"+workspace, "member_of", "module:"+analysis.Manifests.Module, evidence.Observed, 0.9, "")
	}
	architecture := analysis.Architecture
	if architecture.Pattern != "" && architecture.Pattern != "not-established" {
		record("repository", "architecture", architecture.Pattern, architecture.State, architecture.Confidence,
			architecture.Basis+" "+strings.Join(architecture.Evidence, "; "))
		observation.Facts = append(observation.Facts, Fact{Provider: string(p.Name()), Subject: "architecture",
			Detail: fmt.Sprintf("%s (%s, confidence %.2f)", architecture.Pattern, architecture.State, architecture.Confidence)})
	}
	for _, boundary := range analysis.Boundaries {
		record("boundary:"+boundary.From, "crosses_to", boundary.To, boundary.Evidence.State, boundary.Evidence.Confidence,
			fmt.Sprintf("%d crossings", boundary.Crossings))
		observation.Facts = append(observation.Facts, Fact{Provider: string(p.Name()),
			Subject: "boundary", Detail: fmt.Sprintf("%s -> %s (%d crossings, %s)", boundary.From, boundary.To, boundary.Crossings, boundary.Evidence.State)})
	}
	for _, sample := range analysis.Samples {
		record("file:"+sample.Path, "representative_of", sample.NodeID, sample.Evidence.State, sample.Evidence.Confidence, sample.Reason)
	}
	if analysis.Focus != nil {
		switch {
		case !analysis.Focus.Found:
			observation.Issues = append(observation.Issues,
				"structural focus was not found; absence is unresolved, not proof that the target does not exist")
		case analysis.Focus.Ambiguous:
			observation.Issues = append(observation.Issues,
				"structural focus is ambiguous: "+strings.Join(analysis.Focus.Candidates, ", "))
		default:
			observation.Facts = append(observation.Facts, Fact{Provider: string(p.Name()), Subject: analysis.Focus.Selector,
				Detail: fmt.Sprintf("%d references, %d callers, %d importers, %d tests",
					analysis.Focus.References, analysis.Focus.Callers, analysis.Focus.Importers, analysis.Focus.Tests)})
		}
	}
	for _, issue := range analysis.Issues {
		observation.Issues = append(observation.Issues, issue.Code+": "+issue.Detail)
	}
	return observation, nil
}

func structuralRequest(req Request) (structural.Intent, string, bool) {
	switch req.Intent {
	case planner.IntentProject:
		return structural.IntentProject, "", true
	case planner.IntentArchitecture:
		return structural.IntentArchitecture, "", true
	case planner.IntentLocate:
		return structural.IntentLocate, req.Focus, true
	case planner.IntentFlow:
		return structural.IntentFlow, req.Focus, true
	case planner.IntentImpact:
		return structural.IntentImpact, req.Focus, true
	case planner.IntentDebug:
		return structural.IntentLocate, req.Focus, true
	case planner.IntentVerify:
		return structural.IntentImpact, req.Focus, true
	default:
		return "", "", false
	}
}

func structuralCoverage(intent structural.Intent, analysis structural.Analysis) float64 {
	switch intent {
	case structural.IntentProject:
		return 0.85
	case structural.IntentArchitecture:
		if analysis.Coverage.GraphEdges > 0 {
			return 0.6
		}
		return 0.45
	case structural.IntentImpact, structural.IntentIsolation:
		return 0.5
	default:
		if analysis.Focus != nil && analysis.Focus.Found {
			return 0.45
		}
		return 0.2
	}
}

// RetrievalProvider adapts the shared BM25F workspace.
type RetrievalProvider struct {
	Workspace *retrieval.Workspace
	Source    retrieval.Source
	Root      string
	Limit     int
}

// Name implements Provider.
func (p *RetrievalProvider) Name() planner.Provider { return planner.ProviderRetrieval }

// Available implements Provider.
func (p *RetrievalProvider) Available(context.Context) bool {
	return p != nil && p.Workspace != nil && p.Source != nil
}

// Observe implements Provider.
func (p *RetrievalProvider) Observe(ctx context.Context, req Request, _ planner.Step, _ *Meter) (Observation, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 8
	}
	result, stats, err := p.Workspace.Search(ctx, p.Source, retrieval.Query{Text: req.Question, Limit: limit})
	if err != nil {
		return Observation{}, fmt.Errorf("BM25 retrieval: %w", err)
	}
	observation := Observation{
		State:      evidence.Observed,
		Confidence: 0.5,
		Coverage:   0.35,
		Summary:    fmt.Sprintf("%d hits from %d documents (%s index)", len(result.Hits), result.Documents, stats.Cache),
	}
	if len(result.Hits) == 0 {
		observation.State = evidence.Unknown
		observation.Confidence = 0.2
		observation.Coverage = 0.1
		observation.Issues = append(observation.Issues,
			"no lexical match in the current index; this is not evidence that the concept is absent")
	}
	for _, hit := range result.Hits {
		observation.Records = append(observation.Records, evidence.Record{
			Kind: evidence.KindRetrieval, Provider: evidence.ProviderRetrieval,
			Source: evidence.SourceBM25, Subject: "file:" + hit.Path,
			Relation: "ranked_for", Object: capText(req.Question, 120),
			State: evidence.Observed, Confidence: clamp01(0.4 + hit.Score/20),
			Freshness:  evidence.FreshnessCurrent,
			Scope:      evidence.Scope{Workspace: p.Root, Path: hit.Path},
			Refs:       []evidence.Ref{{Path: hit.Path}},
			SnapshotID: fmt.Sprintf("retrieval:%d", result.Documents),
			Note:       "bm25 " + strings.Join(hit.Matches, ","),
		})
		observation.Facts = append(observation.Facts, Fact{Provider: string(p.Name()), Subject: hit.Path,
			Detail: fmt.Sprintf("score %.3f; terms: %s", hit.Score, strings.Join(hit.Matches, ", "))})
	}
	return observation, nil
}
