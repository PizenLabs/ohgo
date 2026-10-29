package workspace

import (
	"errors"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
)

// fakeTool is a minimal agent.Tool whose declared tier is controlled by the
// test. It does not implement agent.CapabilityReporter.
type fakeTool struct {
	name string
	tier agent.Tier
}

func (t fakeTool) Name() string                   { return t.name }
func (t fakeTool) Description() string            { return t.name }
func (t fakeTool) Parameters() *jsonschema.Schema { return nil }
func (t fakeTool) Approval() agent.Approval       { return agent.Approval{Tier: t.tier} }
func (t fakeTool) Exclusive(map[string]any) bool  { return false }
func (t fakeTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{}
}

// reportingTool additionally implements agent.CapabilityReporter, so the test
// can distinguish a per-call capability from the declared tier.
type reportingTool struct {
	fakeTool
	cap agent.Tier
}

func (t reportingTool) Capability(map[string]any) agent.Tier { return t.cap }

func TestOperationForTierMapsEachTier(t *testing.T) {
	cases := map[agent.Tier]Operation{
		agent.TierRead:  OpRead,
		agent.TierExec:  OpExec,
		agent.TierWrite: OpWrite,
	}
	for tier, want := range cases {
		if got := OperationForTier(tier); got != want {
			t.Errorf("OperationForTier(%s) = %s, want %s", tier, got, want)
		}
	}
	// An unknown tier must map to something the policy denies, not to a read.
	if got := OperationForTier(agent.Tier("future")); Build.Permits(got) {
		t.Errorf("unknown tier mapped to a permitted operation %q", got)
	}
}

func TestOperationForToolPrefersPerCallCapability(t *testing.T) {
	// A tool that declares write but reports exec for this call is classified
	// by the call: the invocation is what the authority boundary sees.
	tool := reportingTool{fakeTool: fakeTool{name: "bash", tier: agent.TierWrite}, cap: agent.TierExec}
	if got := OperationForTool(tool, nil); got != OpExec {
		t.Errorf("operation = %s, want %s", got, OpExec)
	}
	// A plain tool falls back to its declared tier.
	plain := fakeTool{name: "read", tier: agent.TierRead}
	if got := OperationForTool(plain, nil); got != OpRead {
		t.Errorf("operation = %s, want %s", got, OpRead)
	}
}

func TestAuthorityAdmitsByWorkspace(t *testing.T) {
	write := fakeTool{name: "write", tier: agent.TierWrite}
	read := fakeTool{name: "read", tier: agent.TierRead}

	if err := (Authority{Workspace: Fuzz}).Admit(write, nil); err == nil {
		t.Fatal("FUZZ admitted a write tool")
	}
	if err := (Authority{Workspace: Fuzz}).Admit(read, nil); err != nil {
		t.Errorf("FUZZ refused a read tool: %v", err)
	}
	if err := (Authority{Workspace: Build}).Admit(write, nil); err != nil {
		t.Errorf("BUILD refused a write tool: %v", err)
	}
}

func TestAuthorityNamesTheRefusedTool(t *testing.T) {
	write := fakeTool{name: "edit", tier: agent.TierWrite}
	err := (Authority{Workspace: Fuzz}).Admit(write, nil)
	var denied *Denied
	if !errors.As(err, &denied) {
		t.Fatalf("error = %v, want *Denied", err)
	}
	if denied.Tool != "edit" {
		t.Errorf("tool = %q, want edit", denied.Tool)
	}
}
