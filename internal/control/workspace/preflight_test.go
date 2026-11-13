package workspace

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

// A BUILD turn whose objective needs a repository change must not start when
// the runtime has no capability that can make one. This is the whole point of
// the preflight: the model must never spend a turn discovering it.
func TestPreflightBlocksMutationWithoutMutationCapability(t *testing.T) {
	intent := BuildIntentContract("convert this project to Next.js and verify the build")
	preflight := PreflightTurn(Build, intent, []agent.Tool{
		fakeTool{name: "read", tier: agent.TierRead},
	})
	if got := preflight.State(); got != PreflightCapabilityUnavailable {
		t.Fatalf("state = %s, want %s (missing=%+v)", got, PreflightCapabilityUnavailable, preflight.Missing)
	}
	if preflight.Ready() {
		t.Fatal("a read-only runtime reported ready for a mutation objective")
	}
	reason := preflight.Reason()
	if !strings.Contains(reason, "repository mutation") {
		t.Errorf("reason = %q, want it to name the missing capability", reason)
	}
	// Execution is required too: the request asks for a successful check.
	if !strings.Contains(reason, "command execution") {
		t.Errorf("reason = %q, want it to name the missing execution capability", reason)
	}
}

// An execution objective needs a capability that runs a process. Reading files
// is not execution, and reporting ready would let the model propose a command
// the task surface refuses.
func TestPreflightBlocksExecutionWithoutExecutionCapability(t *testing.T) {
	intent := BuildIntentContract("run the full test suite")
	preflight := PreflightTurn(Build, intent, []agent.Tool{
		fakeTool{name: "read", tier: agent.TierRead},
		fakeTool{name: "write", tier: agent.TierWrite},
	})
	if preflight.State() != PreflightCapabilityUnavailable {
		t.Fatalf("state = %s, want %s", preflight.State(), PreflightCapabilityUnavailable)
	}
	if len(preflight.Missing) != 1 || preflight.Missing[0].Capability != CapabilityExecute {
		t.Fatalf("missing = %+v, want exactly execute", preflight.Missing)
	}
}

// An authority that forbids the operation is a different failure from an absent
// capability: the runtime can do it and the policy refuses it.
func TestPreflightDistinguishesAuthorityFromCapability(t *testing.T) {
	intent := BuildIntentContract("convert this project to Next.js")
	tools := []agent.Tool{
		fakeTool{name: "read", tier: agent.TierRead},
		fakeTool{name: "write", tier: agent.TierWrite},
		fakeTool{name: "bash", tier: agent.TierExec},
	}
	preflight := PreflightTurn(Ask, intent, tools)
	if preflight.State() != PreflightAuthorityDenied {
		t.Fatalf("state = %s, want %s", preflight.State(), PreflightAuthorityDenied)
	}
	if len(preflight.Missing) != 0 {
		t.Fatalf("missing = %+v, want none: the capability exists", preflight.Missing)
	}
	if !strings.Contains(preflight.Reason(), "ASK") {
		t.Errorf("reason = %q, want it to name the refusing authority", preflight.Reason())
	}
}

// A complete surface is ready. The preflight must not block the ordinary case.
func TestPreflightReadyWhenTheSurfaceCanPerformTheObjective(t *testing.T) {
	intent := BuildIntentContract("convert this project to Next.js and verify the build")
	preflight := PreflightTurn(Build, intent, []agent.Tool{
		fakeTool{name: "read", tier: agent.TierRead},
		fakeTool{name: "write", tier: agent.TierWrite},
		fakeTool{name: "bash", tier: agent.TierExec},
	})
	if !preflight.Ready() {
		t.Fatalf("state = %s (%s), want ready", preflight.State(), preflight.Reason())
	}
	if len(preflight.Available) == 0 {
		t.Fatal("no available operations were reported")
	}
}

// A deferred objective runs as its informational phase under the lower
// authority. Judging it against the mutation it cannot perform yet would block
// a legitimate investigation, which is what PhaseContract exists to prevent.
func TestPreflightAcceptsDeferredPhaseUnderLowerAuthority(t *testing.T) {
	intent := BuildIntentContract("convert this project to Next.js")
	phase, deferred := PhaseContract(intent, Ask)
	if !deferred {
		t.Fatal("the mutation objective was not deferred under ASK")
	}
	preflight := PreflightTurn(Ask, phase, []agent.Tool{
		fakeTool{name: "read", tier: agent.TierRead},
	})
	if !preflight.Ready() {
		t.Fatalf("deferred phase state = %s (%s), want ready", preflight.State(), preflight.Reason())
	}
}
