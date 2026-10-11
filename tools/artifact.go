package tools

import (
	"fmt"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/workspace"
)

// ArtifactSink records a structured handoff artifact.
type ArtifactSink func(artifact workspace.Artifact) error

// ArtifactTool records a structured artifact for cross-workspace handoff.
//
// It is the mechanism behind "ASK → Plan, FUZZ → Finding, BUILD → Change /
// Verification" (§23, I12). It does not mutate the repository — it records
// metadata — so it is admitted in every workspace; authority is enforced
// through the artifact kind, which must match the workspace that produced it.
// That is what stops FUZZ from authoring a Change, or ASK from asserting a
// Verification it never ran.
type ArtifactTool struct {
	Base
	// origin is the workspace this tool instance serves.
	origin workspace.Workspace
	// sink persists the artifact. A nil sink refuses the call rather than
	// silently dropping the record.
	sink ArtifactSink
}

// NewArtifactTool returns an artifact tool for a workspace and sink.
func NewArtifactTool(sink ArtifactSink, origin workspace.Workspace) *ArtifactTool {
	tool := &ArtifactTool{origin: origin, sink: sink}
	tool.Base = NewBase("artifact",
		"Record a structured artifact for handoff between workspaces: a plan "+
			"(ask), a finding (fuzz), or a change/verification (build). A finding "+
			"requires a reproduction and evidence.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"plan", "finding", "change", "verification"},
					"description": "The artifact kind, which must match the workspace.",
				},
				"target": map[string]any{
					"type":        "string",
					"description": "What the artifact is about, e.g. a file or a task.",
				},
				"status": map[string]any{
					"type":        "string",
					"description": "The artifact's own state, e.g. \"reproduced\".",
				},
				"expected": map[string]any{
					"type":        "string",
					"description": "What should have happened.",
				},
				"observed": map[string]any{
					"type":        "string",
					"description": "What actually happened.",
				},
				"reproduction": map[string]any{
					"type":        "string",
					"description": "How to trigger it, e.g. \"go test -race ./...\". Required for a finding.",
				},
				"evidence": map[string]any{
					"type":        "string",
					"description": "The captured output backing the claim. Required for a finding.",
				},
			},
			"required": []string{"kind", "target"},
		},
		agent.Approval{Tier: agent.TierRead},
	)
	return tool
}

// Execute implements agent.Tool.
func (t *ArtifactTool) Execute(ctx agent.Context, args map[string]any) agent.Result {
	if err := checkCancelled(ctx.Ctx); err != nil {
		return agent.ErrorResult("%v", err)
	}
	kind, err := strArg(args, "kind", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	target, err := strArg(args, "target", "")
	if err != nil {
		return agent.ErrorResult("%v", err)
	}
	status, _ := strArg(args, "status", "")
	expected, _ := strArg(args, "expected", "")
	observed, _ := strArg(args, "observed", "")
	reproduction, _ := strArg(args, "reproduction", "")
	evidence, _ := strArg(args, "evidence", "")

	artifactKind := workspace.ArtifactKind(kind)
	if !artifactKind.Valid() {
		return agent.ErrorResult("unknown artifact kind %q", kind)
	}
	if producer := producerOf(artifactKind); producer != t.origin {
		return agent.ErrorResult("workspace %s cannot produce a %s artifact", t.origin, kind)
	}

	artifact := workspace.Artifact{
		ID:           workspace.NewArtifactID(artifactKind),
		Kind:         artifactKind,
		Origin:       t.origin,
		Target:       target,
		Status:       status,
		Expected:     expected,
		Observed:     observed,
		Reproduction: reproduction,
		Evidence:     evidence,
	}
	if err := artifact.Validate(); err != nil {
		return agent.ErrorResult("%v", err)
	}
	if t.sink == nil {
		return agent.ErrorResult("artifact recording is unavailable")
	}
	if err := t.sink(artifact); err != nil {
		return agent.ErrorResult("record artifact: %v", err)
	}
	return agent.Result{Text: fmt.Sprintf("recorded %s %s (%s)", artifact.Kind, artifact.ID, target)}
}

// producerOf names the workspace that produces an artifact kind.
func producerOf(kind workspace.ArtifactKind) workspace.Workspace {
	switch kind {
	case workspace.KindPlan:
		return workspace.Ask
	case workspace.KindFinding:
		return workspace.Fuzz
	case workspace.KindChange, workspace.KindVerification:
		return workspace.Build
	}
	return ""
}
