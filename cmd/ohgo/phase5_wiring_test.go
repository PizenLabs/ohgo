package main

import (
	"context"
	"io"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The production loop and every spawner built beside it must resolve the same
// root budget. A child that received a fresh instance would leave the root's
// counters untouched, which is the exact escape Phase 5 forbids.
func TestProductionLoopAndDelegationShareOneRootBudget(t *testing.T) {
	server := newNestedServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	opts := testOptions(t, t.TempDir(), 3)
	registry := ai.NewRegistry()
	registry.SetBaseURL("anthropic", server.URL)
	loop, err := buildLoop(opts, registry, io.Discard, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildLoop: %v", err)
	}
	if _, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: "delegate deeply"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	root := loop.Budget()
	if root == nil {
		t.Fatal("the production loop has no root budget")
	}
	snap := root.Snapshot()
	if snap.Used.Children != 2 {
		t.Fatalf("root children = %d, want the two delegated grandchildren", snap.Used.Children)
	}
	if snap.Used.Turns < 5 {
		t.Fatalf("root turns = %d, want parent + two child levels + grandchild", snap.Used.Turns)
	}
	if snap.Used.Turns > snap.Limits.Turns {
		t.Fatalf("root turns %d exceeded grant %d", snap.Used.Turns, snap.Limits.Turns)
	}
}

// A caller that constructs options directly still gets one holder, not a new
// grant per build helper.
func TestDirectOptionsResolveOneBudgetHolder(t *testing.T) {
	opts := testOptions(t, t.TempDir(), 2)
	opts.maxTurns = 7
	opts.maxToolCalls = 11
	holder := opts.ensureBudget()
	opts.budget = holder
	if got := holder.Get().Limits().Turns; got != 7 {
		t.Fatalf("turn grant = %d, want 7", got)
	}
	if got := holder.Get().Limits().ToolCalls; got != 11 {
		t.Fatalf("tool grant = %d, want 11", got)
	}
	if opts.ensureBudget() != holder {
		t.Fatal("ensureBudget created a second holder")
	}
}

// The derived defaults are finite and deterministic: an unbounded dimension
// cannot slip in through a zero flag.
func TestDerivedBudgetLimitsAreFinite(t *testing.T) {
	opts := options{maxTurns: 100, maxDepth: 2}
	limits := opts.budgetLimits()
	if limits.Turns != 100 || limits.ModelCalls != 110 || limits.ToolCalls != 800 ||
		limits.Children != 8 || limits.BackgroundJobs != 100 ||
		limits.ExternalCalls != 800 || limits.ExternalBytes != 16*1024*1024 {
		t.Fatalf("derived limits = %+v", limits)
	}
	if limits.Turns <= 0 || limits.ModelCalls <= 0 || limits.ToolCalls <= 0 ||
		limits.Children <= 0 || limits.BackgroundJobs <= 0 ||
		limits.ExternalCalls <= 0 || limits.ExternalBytes <= 0 {
		t.Fatalf("derived budget has an unmetered dimension: %+v", limits)
	}
}

// A tiny explicit tool grant must not make paginated MCP discovery fail before
// it advertises anything: discovery is several metered exchanges.
func TestTinyToolGrantKeepsDiscoveryUsable(t *testing.T) {
	opts := options{maxTurns: 1, maxToolCalls: 2}
	limits := opts.budgetLimits()
	if limits.ToolCalls != 2 {
		t.Fatalf("tool grant = %d, want the explicit 2", limits.ToolCalls)
	}
	if limits.ExternalCalls < 16 {
		t.Fatalf("external grant = %d, want at least one discovery floor", limits.ExternalCalls)
	}
}
