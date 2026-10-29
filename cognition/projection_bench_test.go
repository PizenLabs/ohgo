package cognition

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// BenchmarkProjectorProject measures the runtime-only cost of building one
// projected request from a bounded evidence set. It excludes the provider and
// tool work, which the Phase 10 matrix measures separately.
func BenchmarkProjectorProject(b *testing.B) {
	shape := BuildShape(contract.IntentContract{
		Objective: "Write README.md.",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "README.md"}},
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyAny,
		}},
	}, ShapeParams{Authority: "build"})
	evidence := NewEvidenceState()
	evidence.ensureRequirements(shape)
	for i := 0; i < 200; i++ {
		path := fmt.Sprintf("pkg/file%d.go", i)
		text := strings.Repeat("line of source\n", 40)
		evidence.ObserveOutcome(agent.ToolOutcome{
			CallID:   fmt.Sprintf("call-%d", i),
			ToolName: "read",
			Args:     map[string]any{"path": path},
			Result: agent.Result{
				Text:    text,
				Details: map[string]any{"path": path, "totalLines": 40, "offset": 1, "limit": 2000},
			},
		})
	}
	messages := make([]agent.Message, 0, 220)
	messages = append(messages, agent.UserMessage{Text: "Write README.md."})
	for i := 0; i < 200; i++ {
		messages = append(messages, agent.ToolResultMessage{
			ToolCallID: fmt.Sprintf("call-%d", i),
			ToolName:   "read",
			Content:    []ai.ContentBlock{ai.TextContent{Text: strings.Repeat("line of source\n", 40)}},
		})
	}
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "write", tier: agent.TierWrite, ops: []contract.ChangeOp{contract.ChangeCreate}},
	}
	projector := NewProjector(shape, evidence, RepositoryFacts{Go: true, Markers: []string{"go.mod"}})
	req := agent.ProjectionRequest{Messages: messages, Tools: tools, Turn: 200}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = projector.Project(req)
	}
}
