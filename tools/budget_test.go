package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/internal/control/budget"
)

func bashContext(b *budget.Budget) agent.Context {
	return agent.Context{Ctx: context.Background(), Budget: b}
}

// Background work is charged to the root at admission. A refused job must not
// appear in the table: there is no process to wait on or clean up.
func TestBackgroundBudgetRefusesWithoutStartingAJob(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	b := budget.New(budget.Limits{BackgroundJobs: 1})
	ctx := bashContext(b)

	first := tool.Execute(ctx, map[string]any{"command": "echo one", "background": true})
	if first.IsError {
		t.Fatalf("first background job: %s", first.Text)
	}
	second := tool.Execute(ctx, map[string]any{"command": "echo two", "background": true})
	if !second.IsError {
		t.Fatalf("second background job was admitted: %s", second.Text)
	}
	if !strings.Contains(second.Text, string(budget.DimensionBackgroundJobs)) {
		t.Fatalf("refusal does not name the dimension: %q", second.Text)
	}
	if got := len(tool.Jobs().Snapshot()); got != 1 {
		t.Fatalf("retained jobs = %d, want exactly the admitted job", got)
	}
	if got := b.Snapshot().Used.BackgroundJobs; got != 1 {
		t.Fatalf("charged jobs = %d, want 1", got)
	}
}

// A background job is detached from the turn but not from the root deadline.
// The effective timeout is the remaining root time, so the process is killed
// even when the model asked for a longer one.
func TestBackgroundJobIsCappedByTheRootDeadline(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	b := budget.New(budget.Limits{
		BackgroundJobs: 1,
		Deadline:       time.Now().Add(150 * time.Millisecond),
	})
	result := tool.Execute(bashContext(b), map[string]any{
		"command":    "sleep 5",
		"background": true,
		"timeout":    float64(60_000),
	})
	if result.IsError {
		t.Fatalf("spawn: %s", result.Text)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		jobs := tool.Jobs().Snapshot()
		if len(jobs) == 1 && !jobs[0].Running {
			if jobs[0].Elapsed > 2*time.Second {
				t.Fatalf("job ran %s, want it capped near the root deadline", jobs[0].Elapsed)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still running after the root deadline: %+v", jobs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An already-expired root deadline refuses the job before any table slot or
// process is created.
func TestExpiredRootDeadlineRefusesBackgroundWork(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	b := budget.New(budget.Limits{
		BackgroundJobs: 4,
		Deadline:       time.Now().Add(-time.Second),
	})
	result := tool.Execute(bashContext(b), map[string]any{
		"command":    "echo never",
		"background": true,
	})
	if !result.IsError || !strings.Contains(result.Text, "deadline") {
		t.Fatalf("result = %q, want a deadline refusal", result.Text)
	}
	if got := len(tool.Jobs().Snapshot()); got != 0 {
		t.Fatalf("refused call created %d jobs", got)
	}
	if got := b.Snapshot().Used.BackgroundJobs; got != 0 {
		t.Fatalf("refused call consumed %d background grants", got)
	}
}

// Foreground work is capped by the root deadline even when the model asked for
// a longer per-command timeout.
func TestForegroundCommandIsCappedByTheRootDeadline(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	b := budget.New(budget.Limits{Deadline: time.Now().Add(150 * time.Millisecond)})
	start := time.Now()
	result := tool.Execute(bashContext(b), map[string]any{
		"command": "sleep 5",
		"timeout": float64(60_000),
	})
	elapsed := time.Since(start)
	if !result.IsError || !strings.Contains(result.Text, "timed out") {
		t.Fatalf("result = %q, want a timeout", result.Text)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("foreground command ran %s, want the root deadline to cap it", elapsed)
	}
}

// Foreground shell work is metered as a tool call by the loop, not as a
// background job. A background-only grant must not block it.
func TestBackgroundGrantDoesNotMeterForegroundCommands(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	b := budget.New(budget.Limits{BackgroundJobs: 0, ToolCalls: 1})
	if err := b.Admit(budget.DimensionToolCalls); err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(bashContext(b), map[string]any{"command": "echo ok"})
	if result.IsError {
		t.Fatalf("foreground command was blocked by the background grant: %s", result.Text)
	}
}
