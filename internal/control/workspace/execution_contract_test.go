package workspace

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// The execution contract is the master task's persistent authority, not the
// live role. These cases pin the two properties the runtime relies on: the
// origin never moves, and a human authorization only adds capability.
func TestExecutionContractDerivesFromOrigin(t *testing.T) {
	cases := []struct {
		origin  Workspace
		execute bool
		mutate  bool
	}{
		{Ask, false, false},
		{Fuzz, true, false},
		{Build, true, true},
	}
	for _, tc := range cases {
		contract := ContractFor(tc.origin)
		if contract.Origin != tc.origin || contract.Execute != tc.execute || contract.Mutate != tc.mutate {
			t.Errorf("ContractFor(%s) = %s, want origin=%s execute=%v mutate=%v",
				tc.origin, contract, tc.origin, tc.execute, tc.mutate)
		}
	}
	if ContractFor("").Valid() {
		t.Fatal("an unresolved origin produced a valid contract")
	}
}

func TestExecutionContractAuthorizeOnlyWidens(t *testing.T) {
	ask := ContractFor(Ask)
	build := ask.Authorize(Build)
	if build.Origin != Ask {
		t.Fatalf("origin = %s, want the immutable ASK origin", build.Origin)
	}
	if !build.Execute || !build.Mutate {
		t.Fatalf("authorized BUILD contract = %s, want execution and mutation", build)
	}

	// Returning to ASK changes the live role, not the capabilities a human
	// already granted. A later BUILD phase must not require a second grant
	// merely because the task answered a question in between.
	narrowed := build.Authorize(Ask)
	if narrowed.Origin != Ask || !narrowed.Execute || !narrowed.Mutate {
		t.Fatalf("narrowed contract = %s, want the BUILD grant preserved", narrowed)
	}

	fuzz := ContractFor(Fuzz).Authorize(Build)
	if fuzz.Origin != Fuzz || !fuzz.Mutate {
		t.Fatalf("FUZZ-origin widened contract = %s, want mutation with the FUZZ origin", fuzz)
	}
}

func TestExecutionContractNormalizeEnforcesExecutionForMutation(t *testing.T) {
	contract := ExecutionContract{Origin: Build, Mutate: true}
	normalized := contract.Normalize()
	if !normalized.Execute {
		t.Fatal("a mutating contract was allowed to omit execution authority")
	}
	if !normalized.Permits(CapabilityMutate) || !normalized.Permits(CapabilityExecute) {
		t.Fatalf("normalized contract = %s, want execute and mutate", normalized)
	}
	if ContractFor(Ask).Permits(CapabilityExecute) {
		t.Fatal("an ASK contract authorized execution")
	}
	if ContractFor(Fuzz).Permits(CapabilityMutate) {
		t.Fatal("a FUZZ contract authorized mutation")
	}
	if ContractFor(Ask).Permits(Capability("invented")) {
		t.Fatal("an unknown capability was authorized")
	}
}

// A widened ASK-origin task still crosses the ASK boundary through the human
// confirmation path. The contract records what the human granted; it never
// turns the protected boundary into an automatic move.
func TestWidenedAskTaskStillConfirmsLeavingAsk(t *testing.T) {
	contract := ContractFor(Ask).Authorize(Build)
	decision := DecideTransition(Ask, contract, AnalyzeRequest("fix the checkout bug"))
	if !decision.NeedsConfirmation() || decision.Target != Build {
		t.Fatalf("decision = %+v, want a confirmed ASK -> BUILD move", decision)
	}
}

// A standalone FUZZ task has no mutation authorization. The same request under
// a BUILD-authorized task is the internal execution loop, and no human prompt
// is required for it.
func TestFuzzMutationRequiresTaskMutationAuthority(t *testing.T) {
	requirement := AnalyzeRequest("fix the checkout bug")

	standalone := DecideTransition(Fuzz, ContractFor(Fuzz), requirement)
	if !standalone.NeedsConfirmation() || standalone.Target != Build {
		t.Fatalf("standalone FUZZ mutation = %+v, want a BUILD confirmation", standalone)
	}

	authorized := DecideTransition(Fuzz, ContractFor(Build), requirement)
	if !authorized.Automatic() || authorized.Target != Build {
		t.Fatalf("BUILD-authorized FUZZ mutation = %+v, want an automatic BUILD handoff", authorized)
	}

	// The tool boundary follows the same contract: a write cannot acquire
	// mutation authority from the call itself.
	write := fakeTool{name: "write", tier: agent.TierWrite}
	decision, ok := ToolCallTransition(Fuzz, ContractFor(Fuzz), write, map[string]any{"path": "x.go"})
	if ok || !decision.NeedsConfirmation() || decision.Target != Build {
		t.Fatalf("tool transition = %+v ok=%v, want a BUILD confirmation only", decision, ok)
	}
	decision, ok = ToolCallTransition(Fuzz, ContractFor(Build), write, map[string]any{"path": "x.go"})
	if !ok || !decision.Automatic() || decision.Target != Build {
		t.Fatalf("authorized tool transition = %+v ok=%v, want automatic BUILD", decision, ok)
	}
}
