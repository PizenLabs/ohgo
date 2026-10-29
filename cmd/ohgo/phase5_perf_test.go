package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// p5PerfTool is a no-op tool. It isolates loop and accounting overhead from
// filesystem, shell, and provider work.
type p5PerfTool struct{}

func (p5PerfTool) Name() string        { return "noop" }
func (p5PerfTool) Description() string { return "no-op benchmark tool" }
func (p5PerfTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"value":{"type":"string"}}}`))
}
func (p5PerfTool) Approval() agent.Approval      { return agent.Approval{Tier: agent.TierRead} }
func (p5PerfTool) Exclusive(map[string]any) bool { return false }
func (p5PerfTool) Execute(agent.Context, map[string]any) (r agent.Result) {
	return agent.Result{Text: "ok"}
}

type p5PerfLine struct {
	Name  string  `json:"name"`
	Ops   int     `json:"ops"`
	Total string  `json:"total"`
	PerOp string  `json:"per_op_ns"`
	Note  string  `json:"note,omitempty"`
	Value float64 `json:"value,omitempty"`
}

// TestPhase5Performance writes a synthetic overhead report. It is opt-in and
// makes no production-scale claim.
func TestPhase5Performance(t *testing.T) {
	if os.Getenv("OHGO_PHASE5_PERF") != "1" {
		t.Skip("set OHGO_PHASE5_PERF=1 to run the Phase 5 performance probe")
	}
	const n = 1_000_000
	lines := []p5PerfLine{}
	measure := func(name string, ops int, fn func()) {
		start := time.Now()
		fn()
		elapsed := time.Since(start)
		lines = append(lines, p5PerfLine{
			Name: name, Ops: ops, Total: elapsed.Round(time.Microsecond).String(),
			PerOp: fmt.Sprintf("%.2f", float64(elapsed.Nanoseconds())/float64(ops)),
		})
	}

	// Baseline: a bare atomic counter with no limit, dimension lookup, or
	// error path. It is the cheapest possible accounting, not a full agent
	// loop; the ratio bounds the admission cost this phase added.
	var bare atomic.Int64
	measure("bare atomic increment", n, func() {
		for i := 0; i < n; i++ {
			bare.Add(1)
		}
	})

	grant := budget.New(budget.Limits{Turns: n + 1})
	measure("budget admission, serial", n, func() {
		for i := 0; i < n; i++ {
			if err := grant.Admit(budget.DimensionTurns); err != nil {
				t.Fatal(err)
			}
		}
	})

	const workers = 8
	parallel := budget.New(budget.Limits{ToolCalls: n + workers})
	measure("budget admission, 8 goroutines", n, func() {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < n/workers; i++ {
					if err := parallel.Admit(budget.DimensionToolCalls); err != nil {
						return
					}
				}
			}()
		}
		wg.Wait()
	})

	snapshotGrant := budget.New(budget.Limits{Turns: n + 1})
	for i := 0; i < 1000; i++ {
		_ = snapshotGrant.Admit(budget.DimensionTurns)
	}
	measure("budget snapshot", 100_000, func() {
		for i := 0; i < 100_000; i++ {
			_ = snapshotGrant.Snapshot()
		}
	})

	// End-to-end loop: N tool turns, no provider latency (completed streams),
	// no tool latency. The measured cost is the loop plus admission.
	const turns = 500
	var calls atomic.Int64
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		if calls.Add(1) <= turns {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.ToolCall{ID: "call", Name: "noop", Arguments: map[string]any{"value": "x"}}},
				StopReason: ai.StopToolUse,
			}), nil
		}
		return ai.NewCompletedStream(ai.AssistantMessage{StopReason: ai.StopEndTurn}), nil
	}
	loopGrant := budget.New(budget.Limits{Turns: turns + 1, ModelCalls: turns + 1, ToolCalls: turns + 1})
	loop, err := agent.New(agent.Config{
		Model: phase5Model(), Stream: stream, Tools: []agent.Tool{p5PerfTool{}},
		Budget: loopGrant, MaxTurns: turns + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	measure(fmt.Sprintf("agent loop, %d tool turns", turns), turns+1, func() {
		if _, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "go"}}); err != nil {
			t.Fatal(err)
		}
	})
	lines = append(lines, p5PerfLine{
		Name: "loop budget snapshot", Ops: 1,
		Total: "n/a", PerOp: "n/a",
		Note: fmt.Sprintf("used=%+v", loopGrant.Snapshot().Used),
	})

	// Delegation propagation: build and run 100 child agents with one turn
	// each over the same root grant. The baseline is the same loop shape
	// without creating a child.
	childStream := func(context.Context, ai.Request) (*ai.Stream, error) {
		return ai.NewCompletedStream(ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: "ok"}}, StopReason: ai.StopEndTurn,
		}), nil
	}
	parentConfig := agent.Config{
		Model: phase5Model(), Stream: childStream, Tools: []agent.Tool{p5PerfTool{}},
		MaxTurns: 10, Budget: budget.New(budget.Limits{Turns: 500, Children: 200}),
	}
	measure("100 delegated children, one turn each", 100, func() {
		for i := 0; i < 100; i++ {
			if _, err := agent.RunSubagent(context.Background(), parentConfig, agent.SubagentSpec{
				Task: "ping", System: []ai.SystemPromptBlock{{Text: "child"}},
			}); err != nil {
				t.Fatal(err)
			}
		}
	})

	payload, _ := json.MarshalIndent(lines, "", "  ")
	path := os.Getenv("OHGO_PHASE5_PERF_OUT")
	if path == "" {
		path = filepath.Join("..", "..", "docs", "benchmarks", "results", "phase5-performance.txt")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var text string
	for _, line := range lines {
		text += fmt.Sprintf("%-40s ops=%-8d total=%-12s ns/op=%-10s %s\n",
			line.Name, line.Ops, line.Total, line.PerOp, line.Note)
	}
	text += "\n" + string(payload) + "\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}
