package investigate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/ohgo/internal/intelligence/evidence"
	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
	"github.com/PizenLabs/ohgo/internal/intelligence/semantic"
)

// SemanticProvider adapts a language-server manager. The manager is created on
// first use and closed before Observe returns, so a semantic step cannot leave
// a server process behind when the investigation ends.
type SemanticProvider struct {
	// Start returns a manager scoped to the workspace. Nil makes the provider
	// unavailable, which is the correct state when no server is installed.
	Start func(ctx context.Context) (*semantic.Manager, error)
	// Root bounds paths resolved from file URIs.
	Root string
	// MaxSymbols bounds declarations and follow-up requests.
	MaxSymbols int
}

// Name implements Provider.
func (p *SemanticProvider) Name() planner.Provider { return planner.ProviderSemantic }

// Available implements Provider.
func (p *SemanticProvider) Available(context.Context) bool { return p != nil && p.Start != nil }

// Observe implements Provider.
func (p *SemanticProvider) Observe(ctx context.Context, req Request, _ planner.Step, meter *Meter) (Observation, error) {
	if err := meter.ChargeLSP(); err != nil {
		return Observation{Issues: []string{err.Error()}}, nil
	}
	manager, err := p.Start(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("start language server: %w", err)
	}
	defer func() { _ = manager.Close() }()
	servers := manager.Servers()
	if len(servers) == 0 {
		return Observation{Issues: []string{"semantic: no language server is configured for this workspace"}}, nil
	}
	client, err := manager.Client(ctx, servers[0])
	if err != nil {
		return Observation{}, fmt.Errorf("language server %s: %w", servers[0], err)
	}
	if !client.Supports("workspace/symbol") {
		return Observation{Issues: []string{
			"semantic: the language server does not support workspace/symbol; declaration resolution is unavailable, not absent",
		}}, nil
	}
	symbols, err := client.WorkspaceSymbols(ctx, req.Question)
	if err != nil {
		if errors.Is(err, semantic.ErrUnsupportedCapability) {
			return Observation{Issues: []string{"semantic: workspace/symbol is unsupported: " + err.Error()}}, nil
		}
		return Observation{}, fmt.Errorf("workspace/symbol: %w", err)
	}
	limit := p.MaxSymbols
	if limit <= 0 {
		limit = 8
	}
	if len(symbols) > limit {
		symbols = symbols[:limit]
	}
	observation := Observation{
		State:      evidence.Observed,
		Confidence: 0.6,
		Coverage:   0.3,
		Summary:    fmt.Sprintf("%d declarations resolved by %s", len(symbols), servers[0]),
	}
	if len(symbols) == 0 {
		observation.State = evidence.Unknown
		observation.Confidence = 0.3
		observation.Coverage = 0.1
		observation.Issues = append(observation.Issues,
			"semantic: the server returned no declaration for the query; this is not proof the symbol is absent")
	}
	for index, symbol := range symbols {
		rel, ok := p.relative(symbol.URI)
		line := symbol.Range.Start.Line + 1
		if !ok {
			observation.Issues = append(observation.Issues,
				"semantic: a declaration had no workspace path and was not recorded")
			continue
		}
		observation.Records = append(observation.Records, evidence.Record{
			Kind: evidence.KindObservation, Provider: evidence.ProviderSemantic,
			Source: evidence.SourceLSP, Subject: "symbol:" + symbol.Name,
			Relation: "declared_at", Object: fmt.Sprintf("%s:%d", rel, line),
			State: evidence.Observed, Confidence: 0.9, Freshness: evidence.FreshnessCurrent,
			Scope:      evidence.Scope{Workspace: p.Root, Path: rel},
			Refs:       []evidence.Ref{{Path: rel, URI: symbol.URI, Line: line, Column: symbol.Range.Start.Character + 1}},
			SnapshotID: "lsp:" + servers[0],
			Note:       strings.TrimSpace(symbol.Container + " " + symbol.Detail),
		})
		observation.Facts = append(observation.Facts, Fact{Provider: string(p.Name()),
			Subject: symbol.Name, Detail: fmt.Sprintf("declared at %s:%d (%s)", rel, line, symbol.Container)})
		if index < 2 {
			p.enrich(ctx, client, &observation, symbol, rel, servers[0], meter)
		}
	}
	return observation, nil
}

// enrich adds references and incoming calls for one resolved declaration.
// A failure here is an explicit capability issue, never a zero count.
func (p *SemanticProvider) enrich(ctx context.Context, client *semantic.Client, observation *Observation,
	symbol semantic.Symbol, rel, server string, meter *Meter) {
	if symbol.URI == "" {
		return
	}
	text, err := readBounded(filepath.Join(p.Root, filepath.FromSlash(rel)), 512*1024)
	if err != nil {
		observation.Issues = append(observation.Issues, "semantic: cannot read declaration source: "+err.Error())
		return
	}
	language := languageID(rel)
	if err := client.OpenDocument(symbol.URI, language, text); err != nil {
		observation.Issues = append(observation.Issues, "semantic: open document failed: "+err.Error())
		return
	}
	defer func() { _ = client.CloseDocument(symbol.URI) }()
	if client.Supports("textDocument/references") && meter.ChargeLSP() == nil {
		references, err := client.References(ctx, symbol.URI, symbol.Range.Start, true)
		if err != nil {
			observation.Issues = append(observation.Issues, "semantic: references failed: "+err.Error())
		} else {
			observation.Records = append(observation.Records, evidence.Record{
				Kind: evidence.KindRelationship, Provider: evidence.ProviderSemantic,
				Source: evidence.SourceLSP, Subject: "symbol:" + symbol.Name,
				Relation: "references", Object: fmt.Sprintf("%d", len(references)),
				State: evidence.Observed, Confidence: 0.9, Freshness: evidence.FreshnessCurrent,
				Scope:      evidence.Scope{Workspace: p.Root, Path: rel},
				SnapshotID: "lsp:" + server,
				Note:       referenceNote(references),
			})
			observation.Facts = append(observation.Facts, Fact{Provider: string(p.Name()),
				Subject: symbol.Name, Detail: fmt.Sprintf("%d references: %s", len(references), referenceNote(references))})
		}
	}
	if client.Supports("textDocument/prepareCallHierarchy") && meter.ChargeLSP() == nil {
		items, err := client.PrepareCallHierarchy(ctx, symbol.URI, symbol.Range.Start)
		if err == nil && len(items) > 0 && meter.ChargeLSP() == nil {
			calls, callErr := client.IncomingCalls(ctx, items[0])
			if callErr == nil {
				observation.Records = append(observation.Records, evidence.Record{
					Kind: evidence.KindRelationship, Provider: evidence.ProviderSemantic,
					Source: evidence.SourceLSP, Subject: "symbol:" + symbol.Name,
					Relation: "incoming_calls", Object: fmt.Sprintf("%d", len(calls)),
					State: evidence.Observed, Confidence: 0.85, Freshness: evidence.FreshnessCurrent,
					Scope:      evidence.Scope{Workspace: p.Root, Path: rel},
					SnapshotID: "lsp:" + server,
				})
			} else if !errors.Is(callErr, semantic.ErrUnsupportedCapability) {
				observation.Issues = append(observation.Issues, "semantic: call hierarchy failed: "+callErr.Error())
			}
		} else if err != nil && !errors.Is(err, semantic.ErrUnsupportedCapability) {
			observation.Issues = append(observation.Issues, "semantic: call hierarchy failed: "+err.Error())
		}
	}
}

func (p *SemanticProvider) relative(uri string) (string, bool) {
	if uri == "" || p.Root == "" {
		return "", false
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	root, err := filepath.Abs(p.Root)
	if err != nil {
		return "", false
	}
	full, err := filepath.Abs(filepath.FromSlash(parsed.Path))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func referenceNote(references []semantic.Location) string {
	if len(references) == 0 {
		return "none returned"
	}
	parts := make([]string, 0, 3)
	for index, location := range references {
		if index == 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%d", filepath.Base(location.URI), location.Range.Start.Line+1))
	}
	return strings.Join(parts, ", ")
}

func languageID(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	default:
		return ""
	}
}

func readBounded(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return "", err
	}
	return string(data), nil
}
