package task

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// A handoff preserves task identity and both roles' contexts. The target gets
// a concise transition summary; the source keeps everything it recorded.
func TestHandoffPreservesTaskAndRoleContext(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	role := workspace.Fuzz
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	intent := testIntent("fix the flaky test")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceReproduction, Role: workspace.Fuzz,
		Source: "go test ./... --count=10", Summary: "the failure reproduces twice",
		Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	fact, err := controller.RecordFact(FactInput{
		Text: "the failure is timing dependent", EvidenceIDs: []string{evidence.ID},
	})
	if err != nil {
		t.Fatalf("RecordFact: %v", err)
	}
	hypothesis, err := controller.RecordHypothesis(HypothesisInput{
		Text: "the cache is stale", Role: workspace.Fuzz,
	})
	if err != nil {
		t.Fatalf("RecordHypothesis: %v", err)
	}
	question, err := controller.AddQuestion("does a cold cache change the result?")
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Fuzz, RoleContextUpdate{
		Strategy:  "run the suite under a cold cache",
		Progress:  []string{"reproduced the failure"},
		Artifacts: []string{"artifact:repro"},
	}); err != nil {
		t.Fatalf("FUZZ context: %v", err)
	}
	before, _ := controller.Current()

	handoff, err := controller.ProposeHandoff(HandoffInput{
		To:        workspace.Build,
		Reason:    "the cause is identified; the fix needs mutation authority",
		Summary:   "shared context and the reproduction are carried",
		Workspace: "main; 2 modified",
	})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if handoff.From != workspace.Fuzz || handoff.To != workspace.Build {
		t.Fatalf("handoff roles = %s -> %s", handoff.From, handoff.To)
	}
	if handoff.Status != HandoffProposed {
		t.Fatalf("status = %s, want PROPOSED", handoff.Status)
	}
	if handoff.TaskID != before.ID || handoff.Objective != before.Objective {
		t.Fatalf("handoff lost task identity: %+v", handoff)
	}
	if !contains(handoff.FactIDs, fact.ID) || !contains(handoff.HypothesisIDs, hypothesis.ID) ||
		!contains(handoff.QuestionIDs, question.ID) || !contains(handoff.EvidenceIDs, evidence.ID) {
		t.Fatalf("handoff did not carry the shared references: %+v", handoff)
	}
	if !contains(handoff.Artifacts, "artifact:repro") {
		t.Fatalf("handoff lost the source artifact reference: %v", handoff.Artifacts)
	}
	current, _ := controller.Current()
	if current.ID != before.ID {
		t.Fatalf("proposing changed the task identity: %q -> %q", before.ID, current.ID)
	}
	if current.LastRole() != workspace.Fuzz {
		t.Fatalf("proposing moved the recorded role to %s", current.LastRole())
	}

	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	applied, err := controller.ApplyHandoff(handoff.ID)
	if err != nil {
		t.Fatalf("ApplyHandoff: %v", err)
	}
	if applied.ID != before.ID {
		t.Fatalf("applying changed the task identity: %q -> %q", before.ID, applied.ID)
	}
	if applied.ActiveRole != workspace.Build {
		t.Fatalf("active role = %s, want BUILD", applied.ActiveRole)
	}
	if applied.Handoff == nil || applied.Handoff.Status != HandoffApplied {
		t.Fatalf("handoff = %+v, want APPLIED", applied.Handoff)
	}
	if !hasFact(applied, "the failure is timing dependent") {
		t.Fatal("the shared fact did not survive the handoff")
	}
	fuzz, ok := applied.Context.Role(workspace.Fuzz)
	if !ok || fuzz.Strategy == "" || !contains(fuzz.Progress, "reproduced the failure") {
		t.Fatalf("FUZZ context was lost: %+v", fuzz)
	}
	build, ok := applied.Context.Role(workspace.Build)
	if !ok || !containsSubstring(build.Progress, "resumed via handoff from FUZZ") {
		t.Fatalf("BUILD did not receive a transition summary: %+v", build)
	}
	if containsSubstring(build.Progress, "cache is stale") {
		t.Fatal("BUILD inherited FUZZ's private hypothesis text instead of a reference")
	}
	brief := strings.Join(ContextFor(applied, workspace.Build, ""), "\n")
	for _, want := range []string{"Handoff: FUZZ → BUILD [APPLIED]", "carries", before.Objective} {
		if !strings.Contains(brief, want) {
			t.Fatalf("BUILD briefing is missing %q:\n%s", want, brief)
		}
	}

	// Restart: the same task, the applied transition, and both role contexts
	// are restored from disk.
	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.ID != before.ID {
		t.Fatalf("restart changed the task identity: %q -> %q", before.ID, restored.ID)
	}
	if restored.ActiveRole != workspace.Build {
		t.Fatalf("restored active role = %s, want BUILD", restored.ActiveRole)
	}
	if restored.Handoff == nil || restored.Handoff.Status != HandoffApplied {
		t.Fatalf("restored handoff = %+v", restored.Handoff)
	}
	if _, ok := restored.Context.Role(workspace.Fuzz); !ok {
		t.Fatal("restart lost the FUZZ context")
	}
	if _, ok := restored.Context.Role(workspace.Build); !ok {
		t.Fatal("restart lost the BUILD context")
	}
}

// A crash between approval and application leaves an interrupted record, not a
// false role change. The source role remains the last valid state.
func TestInterruptedHandoffKeepsSourceRoleAndStaysWritable(t *testing.T) {
	root := t.TempDir()
	role := workspace.Fuzz
	controller := NewController(NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("fix the flaky test"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	handoff, err := controller.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "needs mutation"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}

	// Simulate a restart before the turn's Begin applied the transition.
	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Fuzz })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.ActiveRole != workspace.Fuzz {
		t.Fatalf("interrupted handoff moved the role to %s", restored.ActiveRole)
	}
	if restored.Handoff == nil || restored.Handoff.Status != HandoffInterrupted {
		t.Fatalf("handoff = %+v, want INTERRUPTED", restored.Handoff)
	}
	if restored.Handoff.Failure == "" {
		t.Fatal("the interruption was not diagnosed")
	}
	brief := strings.Join(ContextFor(restored, workspace.Fuzz, ""), "\n")
	if !strings.Contains(brief, "[INTERRUPTED]") || !strings.Contains(brief, "authority did not move") {
		t.Fatalf("interrupted handoff was not surfaced:\n%s", brief)
	}
	if _, err := reopened.RecordHypothesis(HypothesisInput{Text: "the task remains writable"}); err != nil {
		t.Fatalf("an interrupted handoff made the task unwritable: %v", err)
	}
}

// Rejection and failure are terminal dispositions that never move authority.
func TestHandoffRejectionAndFailureDoNotMoveAuthority(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("investigate then decide"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	rejected, err := controller.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "maybe"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.RejectHandoff(rejected.ID, "not yet"); err != nil {
		t.Fatalf("RejectHandoff: %v", err)
	}
	current, _ := controller.Current()
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("rejection moved the role to %s", current.ActiveRole)
	}
	if current.Handoff == nil || current.Handoff.Status != HandoffRejected {
		t.Fatalf("rejected handoff = %+v", current.Handoff)
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("a rejected handoff granted FUZZ mutation authority")
	}

	failed, err := controller.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "now"})
	if err != nil {
		t.Fatalf("second ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(failed.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	if _, err := controller.FailHandoff(failed.ID, "the switch was refused"); err != nil {
		t.Fatalf("FailHandoff: %v", err)
	}
	current, _ = controller.Current()
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("failure moved the role to %s", current.ActiveRole)
	}
	if current.Handoff == nil || current.Handoff.Status != HandoffFailed || current.Handoff.Failure == "" {
		t.Fatalf("failed handoff = %+v", current.Handoff)
	}
}

// A handoff cannot be applied before approval, and a second pending handoff is
// refused rather than making recovery ambiguous.
func TestHandoffRequiresApprovalAndRefusesACompetingProposal(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("transition safely"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	handoff, err := controller.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "needs mutation"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApplyHandoff(handoff.ID); !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("ApplyHandoff before approval = %v, want ErrInvalidHandoff", err)
	}
	if _, err := controller.ProposeHandoff(HandoffInput{To: workspace.Ask, Reason: "competing"}); !errors.Is(err, ErrHandoffPending) {
		t.Fatalf("second pending handoff = %v, want ErrHandoffPending", err)
	}
	current, _ := controller.Current()
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("an unapproved handoff moved the role to %s", current.ActiveRole)
	}
}

// Begin is the application boundary: the live role is known and the task is
// attached there. It must keep identity and record the transition exactly once.
func TestBeginAppliesAnApprovedHandoffAndKeepsIdentity(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	intent := testIntent("fix the flaky test")
	started, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	handoff, err := controller.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "needs mutation"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}

	role = workspace.Build
	resumed, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin after approval: %v", err)
	}
	if resumed.ID != started.ID {
		t.Fatalf("the handoff recreated the task: %q -> %q", started.ID, resumed.ID)
	}
	if resumed.ActiveRole != workspace.Build {
		t.Fatalf("active role = %s, want BUILD", resumed.ActiveRole)
	}
	if resumed.Handoff == nil || resumed.Handoff.Status != HandoffApplied {
		t.Fatalf("handoff = %+v, want APPLIED", resumed.Handoff)
	}
	if _, ok := resumed.Context.Role(workspace.Build); !ok {
		t.Fatal("Begin did not create the target role context")
	}
}

// An approved handoff whose target no longer matches the live role is recorded
// as failed rather than falsely applied.
func TestBeginFailsAnApprovedHandoffForADifferentLiveRole(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	intent := testIntent("fix the flaky test")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	handoff, err := controller.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "needs mutation"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	// The user switched to ASK before the turn began.
	role = workspace.Ask
	resumed, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if resumed.ActiveRole != workspace.Ask {
		t.Fatalf("active role = %s, want the live ASK", resumed.ActiveRole)
	}
	if resumed.Handoff == nil || resumed.Handoff.Status != HandoffFailed {
		t.Fatalf("handoff = %+v, want FAILED", resumed.Handoff)
	}
	if !strings.Contains(resumed.Handoff.Failure, "does not match") {
		t.Fatalf("failure = %q", resumed.Handoff.Failure)
	}
}

// An invalid optional handoff must not make the checkpoint unreadable or the
// task unwritable.
func TestInvalidHandoffIsDroppedWithoutHidingTheTask(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("survive a bad handoff"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	progress, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress: found=%v err=%v", found, err)
	}
	progress.Handoff = &Handoff{
		ID:        "broken",
		TaskID:    progress.TaskID,
		From:      "not-a-role",
		To:        workspace.Build,
		Objective: "survive a bad handoff",
		Status:    HandoffProposed,
	}
	// SaveCheckpoint derives the handoff from the task, so write the damaged
	// checkpoint directly to model a file edited or corrupted on disk.
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write damaged checkpoint: %v", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Task.ID == "" {
		t.Fatal("a damaged optional handoff hid the task")
	}
	if snap.Task.Handoff != nil {
		t.Fatalf("damaged handoff survived: %+v", snap.Task.Handoff)
	}
	if !strings.Contains(snap.Task.ContextWarning, "handoff") {
		t.Fatalf("the dropped handoff was not reported: %q", snap.Task.ContextWarning)
	}
	if err := snap.Task.Validate(); err != nil {
		t.Fatalf("task is not writable after the repair: %v", err)
	}
}

func hasFact(recorded Task, text string) bool {
	for _, fact := range recorded.Context.Facts {
		if fact.Text == text {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// An explicit workspace command is a completed human decision. It records an
// applied handoff so the next turn continues the task, and a later Begin marks
// it consumed so the approval does not attach every future prompt.
func TestExplicitWorkspaceSwitchRecordsAndContinuesTheTask(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	intent := testIntent("fix the flaky test")
	started, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := controller.RecordWorkspaceSwitch(workspace.Fuzz, workspace.Build, "main; clean"); err != nil {
		t.Fatalf("RecordWorkspaceSwitch: %v", err)
	}
	current, _ := controller.Current()
	if current.ActiveRole != workspace.Build {
		t.Fatalf("active role = %s, want BUILD", current.ActiveRole)
	}
	if current.Handoff == nil || current.Handoff.Status != HandoffApplied || current.Handoff.Consumed {
		t.Fatalf("explicit switch handoff = %+v", current.Handoff)
	}
	if current.Handoff.Workspace != "main; clean" {
		t.Fatalf("workspace = %q", current.Handoff.Workspace)
	}
	if _, ok := current.Context.Role(workspace.Build); !ok {
		t.Fatal("explicit switch did not create the target role context")
	}

	role = workspace.Build
	resumed, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin after switch: %v", err)
	}
	if resumed.ID != started.ID {
		t.Fatalf("explicit switch recreated the task: %q -> %q", started.ID, resumed.ID)
	}
	if resumed.Handoff == nil || !resumed.Handoff.Consumed {
		t.Fatalf("handoff was not consumed by the next turn: %+v", resumed.Handoff)
	}
}

// The explicit-switch recorder must not replace a proposal the human already
// approved: the turn boundary applies that one with its own reason and summary.
func TestExplicitSwitchDoesNotReplaceAnApprovedProposal(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("fix the flaky test"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	handoff, err := controller.ProposeHandoff(HandoffInput{
		To: workspace.Build, Reason: "the cause is known", Summary: "carry the reproduction",
	})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if _, err := controller.ApproveHandoff(handoff.ID); err != nil {
		t.Fatalf("ApproveHandoff: %v", err)
	}
	if err := controller.RecordWorkspaceSwitch(workspace.Fuzz, workspace.Build, ""); err != nil {
		t.Fatalf("RecordWorkspaceSwitch: %v", err)
	}
	current, _ := controller.Current()
	if current.Handoff == nil || current.Handoff.Status != HandoffApproved {
		t.Fatalf("approved proposal was replaced: %+v", current.Handoff)
	}
	if current.Handoff.Reason != "the cause is known" || current.Handoff.Summary != "carry the reproduction" {
		t.Fatalf("approved proposal lost its reason/summary: %+v", current.Handoff)
	}
}

// The transition snapshot is relevance-ordered: an older evidence record cited
// by an active fact outranks newer uncited records under the reference cap.
func TestHandoffCarriesCitedEvidenceBeforeNewerRecords(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Fuzz
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("carry relevant evidence"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	var oldest string
	for i := 0; i < maxHandoffRefs+4; i++ {
		record, err := controller.RecordEvidence(EvidenceInput{
			Kind: EvidenceCommand, Source: "run-" + string(rune('a'+i)),
			Summary: "observation", Outcome: EvidenceObserved,
		})
		if err != nil {
			t.Fatalf("RecordEvidence %d: %v", i, err)
		}
		if i == 0 {
			oldest = record.ID
		}
	}
	if _, err := controller.RecordFact(FactInput{
		Text: "the first observation still matters", EvidenceIDs: []string{oldest},
	}); err != nil {
		t.Fatalf("RecordFact: %v", err)
	}
	handoff, err := controller.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "needs mutation"})
	if err != nil {
		t.Fatalf("ProposeHandoff: %v", err)
	}
	if !contains(handoff.EvidenceIDs, oldest) {
		t.Fatalf("cited evidence %q was lost under the cap: %v", oldest, handoff.EvidenceIDs)
	}
	if len(handoff.EvidenceIDs) > maxHandoffRefs {
		t.Fatalf("evidence refs = %d, want <= %d", len(handoff.EvidenceIDs), maxHandoffRefs)
	}
}
