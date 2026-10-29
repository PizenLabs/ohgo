package session

import (
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

func TestSaveArtifactRoundTrips(t *testing.T) {
	store := openTempStore(t)
	sess, err := store.CreateWith(CreateOptions{Cwd: "/w", Model: "m", Workspace: "fuzz"})
	if err != nil {
		t.Fatal(err)
	}

	finding := workspace.Artifact{
		ID: "finding-1", Kind: workspace.KindFinding, Origin: workspace.Fuzz,
		Target: "internal/runtime.go", Status: "reproduced",
		Expected: "no race", Observed: "DATA RACE",
		Reproduction: "go test -race ./...", Evidence: "runtime.go:42",
	}
	if err := store.SaveArtifact(sess.ID, finding); err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}

	got, err := store.Artifacts(sess.ID)
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("artifacts = %d, want 1", len(got))
	}
	if got[0].ID != finding.ID || got[0].Evidence != finding.Evidence || got[0].Origin != workspace.Fuzz {
		t.Errorf("round-tripped artifact = %+v, want the stored finding", got[0])
	}
}

func TestSaveArtifactRefusesAnIncompleteFinding(t *testing.T) {
	// A finding without evidence is a claim, not a handoff; it must not be
	// persisted as though it were one.
	store := openTempStore(t)
	sess, err := store.CreateWith(CreateOptions{Cwd: "/w", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	incomplete := workspace.Artifact{ID: "finding-2", Kind: workspace.KindFinding, Origin: workspace.Fuzz, Target: "x"}
	if err := store.SaveArtifact(sess.ID, incomplete); err == nil {
		t.Fatal("an incomplete finding was persisted")
	}
}

func TestSaveArtifactIsIdempotentByIdentity(t *testing.T) {
	store := openTempStore(t)
	sess, err := store.CreateWith(CreateOptions{Cwd: "/w", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	plan := workspace.Artifact{ID: "plan-1", Kind: workspace.KindPlan, Origin: workspace.Ask, Target: "step one"}
	if err := store.SaveArtifact(sess.ID, plan); err != nil {
		t.Fatal(err)
	}
	plan.Status = "approved"
	if err := store.SaveArtifact(sess.ID, plan); err != nil {
		t.Fatal(err)
	}
	got, err := store.Artifacts(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("artifacts = %d, want 1 after an update by identity", len(got))
	}
	if got[0].Status != "approved" {
		t.Errorf("status = %q, want the updated value", got[0].Status)
	}
}

func TestDeleteRemovesArtifacts(t *testing.T) {
	store := openTempStore(t)
	sess, err := store.CreateWith(CreateOptions{Cwd: "/w", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveArtifact(sess.ID, workspace.Artifact{ID: "plan-1", Kind: workspace.KindPlan, Origin: workspace.Ask, Target: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Artifacts(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("delete left %d artifacts behind", len(got))
	}
}
