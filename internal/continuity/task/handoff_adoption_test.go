package task

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// A widening approval is a decision about the request that triggered it. When
// the approving turn states a new objective, the task adopts that objective and
// its completion contract instead of running the previous objective under the
// new authority.
//
// The failure this defends against: an ASK investigation of a static site, then
// "ok, convert this project to Next.js" approved into BUILD. Running the old
// informational contract under BUILD produced a read-only tool surface, so the
// model could not edit or run anything and answered an execution request with a
// manual implementation plan.
func TestApprovedHandoffAdoptsTheApprovedObjective(t *testing.T) {
	root := t.TempDir()
	role := workspace.Ask
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	askIntent := testIntent("understand this project")
	if _, err := controller.Begin(askIntent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	before, ok := controller.Current()
	if !ok {
		t.Fatal("no task after Begin")
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceSource, Role: workspace.Ask,
		Source: "index.html", Summary: "a static page with a stylesheet",
		Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}

	const approved = "convert this project to Next.js"
	handoff, err := controller.ProposeHandoff(HandoffInput{
		To:                workspace.Build,
		Reason:            "the conversion needs mutation authority",
		ApprovedObjective: approved,
	})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if handoff.ApprovedObjective != approved {
		t.Fatalf("handoff approved objective = %q, want %q", handoff.ApprovedObjective, approved)
	}
	if handoff.Objective != before.Objective {
		t.Fatalf("handoff lost the task objective: %q", handoff.Objective)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	role = workspace.Build

	mutation := contract.IntentContract{
		Objective:    approved,
		Kind:         contract.ObjectiveMutation,
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyAny, Scope: "*"}},
	}
	after, err := controller.Begin(mutation, nil)
	if err != nil {
		t.Fatalf("Begin after approval: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("the transition recreated the task: %q -> %q", before.ID, after.ID)
	}
	if after.Objective != approved {
		t.Fatalf("objective = %q, want the approved %q", after.Objective, approved)
	}
	if after.Contract == nil || after.Contract.Kind != contract.ObjectiveMutation {
		t.Fatalf("contract = %+v, want the approved mutation contract", after.Contract)
	}
	if after.Handoff == nil || after.Handoff.Status != HandoffApplied {
		t.Fatalf("handoff = %+v, want APPLIED", after.Handoff)
	}
	// The observations that established repository facts still describe this
	// repository, so the approved objective keeps them.
	if _, ok := after.Context.evidenceByID(evidence.ID); !ok {
		t.Fatalf("the approved objective lost the ASK observation %q", evidence.ID)
	}
	// The previous objective's needs no longer describe this task. Keeping
	// them would report a permanent evidence gap for work nobody asked for.
	taskEvidence := controller.Evidence()
	if taskEvidence == nil || len(taskEvidence.Requirements) != 0 {
		t.Fatalf("requirements carried across the re-scope: %+v", taskEvidence)
	}

	// The re-scope must be durable: a fresh controller loading the same store
	// sees the approved objective and contract.
	reloaded := NewController(NewStore(root), nil)
	restored, ok, err := reloaded.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !ok {
		t.Fatal("the re-scoped task was not durable")
	}
	if restored.Objective != approved || restored.Contract == nil ||
		restored.Contract.Kind != contract.ObjectiveMutation {
		t.Fatalf("restored task = objective %q contract %+v", restored.Objective, restored.Contract)
	}
}

// A transition that recorded no new objective preserves the task objective, so
// a different objective still conflicts with the persisted task instead of
// silently inheriting the transition.
func TestHandoffWithoutAnApprovedObjectiveDoesNotAdoptAnotherObjective(t *testing.T) {
	root := t.TempDir()
	role := workspace.Ask
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	if _, err := controller.Begin(testIntent("understand this project"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	handoff, err := controller.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "a narrower reading"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if handoff.ApprovedObjective != "" {
		t.Fatalf("a preserving transition recorded an approved objective: %q", handoff.ApprovedObjective)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	role = workspace.Build

	intruder := contract.IntentContract{Objective: "delete the vendored theme", Kind: contract.ObjectiveMutation}
	if _, err := controller.Begin(intruder, nil); err == nil {
		t.Fatal("an objective the approval never named was attached to the task")
	}
	current, ok := controller.Current()
	if !ok {
		t.Fatal("the refusal destroyed the task")
	}
	if current.Objective != "understand this project" {
		t.Fatalf("objective = %q after a refused adoption", current.Objective)
	}
	if current.Contract == nil || current.Contract.Kind != contract.ObjectiveInformational {
		t.Fatalf("contract = %+v after a refused adoption", current.Contract)
	}
}

// An approval that named a new objective covers the turn that follows it, and
// that turn's contract replaces the persisted one — even when the turn's text
// is a prepared form of the approving request.
func TestApprovedHandoffAdoptsTheContractOfTheTurnThatFollowsIt(t *testing.T) {
	root := t.TempDir()
	role := workspace.Ask
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("understand this project"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	handoff, err := controller.ProposeHandoff(HandoffInput{
		To:                workspace.Build,
		ApprovedObjective: "convert this project to Next.js and verify the build",
	})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	role = workspace.Build

	// The prompt owner compresses a long request before the model sees it, so
	// the objective the turn runs is not byte-identical to the approved text.
	prepared := contract.IntentContract{
		Objective:    "convert this project to Next.js",
		Kind:         contract.ObjectiveMutation,
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyAny, Scope: "*"}},
	}
	after, err := controller.Begin(prepared, nil)
	if err != nil {
		t.Fatalf("Begin with the approved turn's contract: %v", err)
	}
	if after.Objective != prepared.Objective {
		t.Fatalf("objective = %q, want the turn's %q", after.Objective, prepared.Objective)
	}
	if after.Contract == nil || after.Contract.Kind != contract.ObjectiveMutation {
		t.Fatalf("contract = %+v, want the turn's mutation contract", after.Contract)
	}
	if after.Handoff == nil || after.Handoff.Status != HandoffApplied {
		t.Fatalf("handoff = %+v, want APPLIED", after.Handoff)
	}
	// The record keeps what was approved and describes the objective the task
	// now carries, so a later validation cannot fail on the re-scope.
	if after.Handoff.ApprovedObjective != handoff.ApprovedObjective {
		t.Fatalf("handoff approved objective = %q, want the recorded %q",
			after.Handoff.ApprovedObjective, handoff.ApprovedObjective)
	}
	if after.Handoff.Objective != prepared.Objective {
		t.Fatalf("handoff objective = %q, want the adopted %q", after.Handoff.Objective, prepared.Objective)
	}
	if err := after.Validate(); err != nil {
		t.Fatalf("the re-scoped task is not valid: %v", err)
	}
}

// An objective that cannot be recorded faithfully is refused at the approval
// boundary. Truncating it would make the approving turn's objective disagree
// with the record, and the transition would fail later for a reason nobody
// could see.
func TestApprovedObjectiveIsNeverSilentlyTruncated(t *testing.T) {
	root := t.TempDir()
	role := workspace.Ask
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("understand this project"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	huge := strings.Repeat("convert ", maxApprovedObjective)
	if _, err := controller.ProposeHandoff(HandoffInput{
		To:                workspace.Build,
		ApprovedObjective: huge,
	}); err == nil {
		t.Fatal("an unrecordable approved objective was accepted")
	}
	if _, pending := controller.PendingHandoff(); pending {
		t.Fatal("the refused proposal left a pending transition")
	}
}
