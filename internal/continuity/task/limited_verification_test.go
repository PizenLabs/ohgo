package task

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// A change observed in a workspace with no supported check is a completion
// with a stated limitation. The durable proof must record what could not be
// observed rather than refusing the completion or claiming a check happened.
func TestCompleteWithEvidenceRecordsVerificationLimitation(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	intent := workspace.BuildIntentContract("Update index.html")
	if intent.Kind != workspace.ObjectiveMutation {
		t.Fatalf("intent = %s, want mutation", intent.Kind)
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "write-1", ToolName: "write", Args: map[string]any{"path": "index.html"},
		Result: agent.Result{Text: "wrote", Mutates: true, Details: map[string]any{
			"path": "index.html", "bytes": 10, "diff": "changed",
		}},
	}}}})

	completed, err := controller.CompleteWithEvidence(CompletionEvidence{
		Complete:               true,
		VerificationLimitation: "no supported verification command was discovered for this workspace",
	})
	if err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}
	if completed.Status != StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", completed.Status)
	}
	if state := completed.ObjectiveState(); state != ObjectiveCompletedLimited {
		t.Fatalf("objective state = %s, want %s", state, ObjectiveCompletedLimited)
	}
	if summary := completed.CompletionSummary(); !strings.Contains(summary, "limited verification") {
		t.Fatalf("completion summary = %q, want the limitation", summary)
	}
	proof, ok := completed.CompletionProof()
	if !ok || proof.Limitation == "" {
		t.Fatalf("proof = %+v (present=%v), want a recorded limitation", proof, ok)
	}
	if len(proof.Verified) != 0 {
		t.Fatalf("proof claimed verification it did not observe: %v", proof.Verified)
	}
}

// The limitation is not a bypass for a missing change: a host that reports it
// without an observed change still fails the durable completion boundary.
func TestVerificationLimitationDoesNotCompleteWithoutChange(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	intent := workspace.BuildIntentContract("Update index.html")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err := controller.CompleteWithEvidence(CompletionEvidence{
		Complete:               true,
		VerificationLimitation: "no supported verification command was discovered for this workspace",
	})
	if !errors.Is(err, ErrUnprovenCompletion) {
		t.Fatalf("completion error = %v, want ErrUnprovenCompletion", err)
	}
	current, _ := controller.Current()
	if current.ObjectiveState() == ObjectiveCompletedLimited || current.Status.IsTerminal() {
		t.Fatalf("an unobserved change was completed: status=%s objective=%s",
			current.Status, current.ObjectiveState())
	}
}

// The limitation must also survive persistence: a resumed projection reports
// the same truthful state instead of reverting to "no recorded proof".
func TestVerificationLimitationSurvivesCheckpoint(t *testing.T) {
	store := NewStore(t.TempDir())
	controller := NewController(store, nil)
	intent := workspace.BuildIntentContract("Update index.html")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "write-1", ToolName: "write", Args: map[string]any{"path": "index.html"},
		Result: agent.Result{Text: "wrote", Mutates: true, Details: map[string]any{
			"path": "index.html", "bytes": 10, "diff": "changed",
		}},
	}}}})
	if _, err := controller.CompleteWithEvidence(CompletionEvidence{
		Complete:               true,
		VerificationLimitation: "no supported verification command was discovered for this workspace",
	}); err != nil {
		t.Fatalf("CompleteWithEvidence: %v", err)
	}

	restored, found, err := NewController(store, nil).Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if state := restored.ObjectiveState(); state != ObjectiveCompletedLimited {
		t.Fatalf("restored objective = %s, want %s", state, ObjectiveCompletedLimited)
	}
}
