package tui

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/cognition"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/jsonschema"
)

type projectionTool struct {
	name string
	tier agent.Tier
}

func (t projectionTool) Name() string        { return t.name }
func (t projectionTool) Description() string { return t.name }
func (t projectionTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (t projectionTool) Approval() agent.Approval      { return agent.Approval{Tier: t.tier} }
func (t projectionTool) Exclusive(map[string]any) bool { return false }
func (t projectionTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "ok"}
}

func TestRunTurnUsesTaskShapeAndAudit(t *testing.T) {
	var (
		mu       sync.Mutex
		captured ai.Request
	)
	loop, err := agent.New(agent.Config{
		Model: ai.Model{ID: "m", Provider: "p"},
		Stream: func(_ context.Context, req ai.Request) (*ai.Stream, error) {
			mu.Lock()
			captured = req
			mu.Unlock()
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "It is a test project."}},
				StopReason: ai.StopEndTurn,
			}), nil
		},
		Tools: []agent.Tool{
			projectionTool{name: "read", tier: agent.TierRead},
			projectionTool{name: "write", tier: agent.TierWrite},
			projectionTool{name: "bash", tier: agent.TierExec},
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	p := NewProgram(loop, "test", 0)
	evidence := cognition.NewEvidenceState()
	var audit string
	p.SetTaskHooks(TaskHooks{
		Begin: func(contract.IntentContract, *agent.TargetSnapshot) TaskProjection {
			return TaskProjection{ID: "t1", Status: "RUNNING"}
		},
		Evidence: func() *cognition.EvidenceState { return evidence },
		RecordProjection: func(report agent.ProjectionReport) {
			audit = cognition.RenderAudit(report)
		},
		Audit: func() string { return audit },
	})

	p.runTurn("Explain this project. Do not modify anything.")

	mu.Lock()
	offered := append([]ai.ToolSpec(nil), captured.Tools...)
	mu.Unlock()
	names := make([]string, 0, len(offered))
	for _, tool := range offered {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "read" {
		t.Fatalf("offered tools = %v, want only read", names)
	}
	if audit == "" || !strings.Contains(audit, "withheld") {
		t.Fatalf("projection audit = %q", audit)
	}

	p.runCommand(Command{Name: "audit"})
	var notice string
	for i := len(p.state.Entries) - 1; i >= 0; i-- {
		if p.state.Entries[i].Notice != "" {
			notice = p.state.Entries[i].Notice
			break
		}
	}
	if !strings.Contains(notice, "projection:") {
		t.Fatalf("/audit notice = %q", notice)
	}
}
