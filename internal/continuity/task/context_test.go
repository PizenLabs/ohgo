package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/tokens"
)

// A task keeps one identity across role changes, and each role keeps its own
// execution context. Changing roles must neither replace the task nor
// overwrite another role's context.
func TestTaskIdentitySurvivesRoleChangeAndRoleContextsStayIsolated(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	role := workspace.Fuzz
	controller := NewController(store, nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	intent := testIntent("diagnose the flaky test")
	begun, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin as FUZZ: %v", err)
	}
	if begun.CreatedAt.IsZero() {
		t.Fatal("a task was created without a creation timestamp")
	}

	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind:    EvidenceCommand,
		Source:  "go test ./internal/...",
		Summary: "the suite failed in internal/continuity/task",
		Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	fact, err := controller.RecordFact(FactInput{
		Text:        "the failing test is TestFlaky",
		EvidenceIDs: []string{evidence.ID},
	})
	if err != nil {
		t.Fatalf("RecordFact: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Fuzz, RoleContextUpdate{
		Strategy: "fuzz-strategy-marker",
		Progress: []string{"reproduced the failure twice"},
	}); err != nil {
		t.Fatalf("FUZZ context: %v", err)
	}

	role = workspace.Ask
	resumedAsAsk, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin as ASK: %v", err)
	}
	if resumedAsAsk.ID != begun.ID {
		t.Fatalf("a role change replaced the task: %q -> %q", begun.ID, resumedAsAsk.ID)
	}
	if _, err := controller.UpdateRoleContext(workspace.Ask, RoleContextUpdate{
		Reasoning: []string{"ask-reasoning-marker"},
		FactIDs:   []string{fact.ID},
	}); err != nil {
		t.Fatalf("ASK context: %v", err)
	}

	role = workspace.Build
	resumedAsBuild, err := controller.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin as BUILD: %v", err)
	}
	if resumedAsBuild.ID != begun.ID {
		t.Fatalf("BUILD got a different task: %q -> %q", begun.ID, resumedAsBuild.ID)
	}
	if _, err := controller.UpdateRoleContext(workspace.Build, RoleContextUpdate{
		Plan:  []string{"build-plan-marker"},
		Scope: []string{"build-scope-marker"},
	}); err != nil {
		t.Fatalf("BUILD context: %v", err)
	}

	// The same state and role always assemble the same context.
	current, ok := controller.Current()
	if !ok {
		t.Fatal("no active task")
	}
	if got, want := len(current.Context.Facts), 1; got != want {
		t.Fatalf("facts = %d, want %d", got, want)
	}
	if got, want := len(current.Context.Evidence), 1; got != want {
		t.Fatalf("evidence = %d, want %d", got, want)
	}
	fuzzCtx, ok := current.Context.Role(workspace.Fuzz)
	if !ok || fuzzCtx.Strategy != "fuzz-strategy-marker" {
		t.Fatalf("FUZZ context did not survive role changes: %+v", fuzzCtx)
	}
	askCtx, ok := current.Context.Role(workspace.Ask)
	if !ok || len(askCtx.Reasoning) != 1 || askCtx.Reasoning[0] != "ask-reasoning-marker" {
		t.Fatalf("ASK context did not survive role changes: %+v", askCtx)
	}
	buildCtx, ok := current.Context.Role(workspace.Build)
	if !ok || len(buildCtx.Plan) != 1 || buildCtx.Plan[0] != "build-plan-marker" {
		t.Fatalf("BUILD context did not survive role changes: %+v", buildCtx)
	}
	if len(fuzzCtx.Plan) != 0 || len(fuzzCtx.Scope) != 0 || len(askCtx.Strategy) != 0 {
		t.Fatalf("role contexts bled into each other: fuzz=%+v ask=%+v", fuzzCtx, askCtx)
	}

	// Restart recovery reloads the shared context and every role context.
	reopened := NewController(store, nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	restored, found, err := reopened.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !found {
		t.Fatal("Restore did not find the checkpoint")
	}
	if restored.ID != begun.ID {
		t.Fatalf("restored id = %q, want %q", restored.ID, begun.ID)
	}
	if restored.CreatedAt.IsZero() {
		t.Fatal("creation time did not survive recovery")
	}
	if len(restored.Context.Facts) != 1 || len(restored.Context.Evidence) != 1 {
		t.Fatalf("shared context did not survive recovery: %+v", restored.Context)
	}
	for role, marker := range map[workspace.Workspace]string{
		workspace.Fuzz:  "fuzz-strategy-marker",
		workspace.Ask:   "ask-reasoning-marker",
		workspace.Build: "build-plan-marker",
	} {
		roleCtx, ok := restored.Context.Role(role)
		if !ok {
			t.Fatalf("%s context missing after recovery", role.Label())
		}
		switch role {
		case workspace.Fuzz:
			if roleCtx.Strategy != marker {
				t.Fatalf("FUZZ strategy = %q, want %q", roleCtx.Strategy, marker)
			}
		case workspace.Ask:
			if len(roleCtx.Reasoning) == 0 || roleCtx.Reasoning[0] != marker {
				t.Fatalf("ASK reasoning = %v, want %q", roleCtx.Reasoning, marker)
			}
		case workspace.Build:
			joined := strings.Join(roleCtx.Plan, " ")
			if !strings.Contains(joined, marker) {
				t.Fatalf("BUILD plan = %v, want %q", roleCtx.Plan, marker)
			}
		}
	}
}

// A model-style assertion is recorded as a hypothesis. It does not become a
// fact because it was written down, repeated, or confirmed without evidence.
func TestAssertionsStayHypothesesUntilEvidenceConfirmsThem(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("fix the crash"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	inconclusive, err := controller.RecordEvidence(EvidenceInput{
		Kind:    EvidenceTest,
		Source:  "go test ./...",
		Summary: "the test run timed out before producing a result",
		Outcome: EvidenceInconclusive,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	proposal, err := controller.RecordHypothesis(HypothesisInput{
		Text: "the crash comes from the lifecycle transition",
	})
	if err != nil {
		t.Fatalf("RecordHypothesis: %v", err)
	}
	current, _ := controller.Current()
	if len(current.Context.Facts) != 0 {
		t.Fatalf("a hypothesis became a fact without evidence: %+v", current.Context.Facts)
	}
	if _, err := controller.RecordFact(FactInput{Text: "unsupported claim", EvidenceIDs: nil}); !errors.Is(err, ErrUnverifiedClaim) {
		t.Fatalf("RecordFact without evidence = %v, want ErrUnverifiedClaim", err)
	}
	if _, err := controller.RecordFact(FactInput{
		Text:        "unsupported claim",
		EvidenceIDs: []string{inconclusive.ID},
	}); !errors.Is(err, ErrUnverifiedClaim) {
		t.Fatalf("RecordFact with inconclusive evidence = %v, want ErrUnverifiedClaim", err)
	}
	if _, _, err := controller.ConfirmHypothesis(proposal.ID, []string{inconclusive.ID}, workspace.Ask); !errors.Is(err, ErrUnverifiedClaim) {
		t.Fatalf("ConfirmHypothesis with inconclusive evidence = %v, want ErrUnverifiedClaim", err)
	}

	observed, err := controller.RecordEvidence(EvidenceInput{
		Kind:    EvidenceSource,
		Source:  "internal/continuity/task/controller.go:200",
		Summary: "Begin compares the persisted contract before starting",
		Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	if err := controller.AttachEvidence(proposal.ID, []string{observed.ID}); err != nil {
		t.Fatalf("AttachEvidence: %v", err)
	}
	current, _ = controller.Current()
	attached := findHypothesis(t, current, proposal.ID)
	if attached.Status != HypothesisActive {
		t.Fatalf("attaching evidence resolved a hypothesis without confirmation: %s", attached.Status)
	}
	if len(current.Context.Facts) != 0 {
		t.Fatal("attaching evidence created a fact")
	}
	confirmed, fact, err := controller.ConfirmHypothesis(proposal.ID, []string{observed.ID}, workspace.Ask)
	if err != nil {
		t.Fatalf("ConfirmHypothesis: %v", err)
	}
	if confirmed.Status != HypothesisConfirmed {
		t.Fatalf("hypothesis status = %s, want confirmed", confirmed.Status)
	}
	if confirmed.ResolvedBy != workspace.Ask || confirmed.ResolvedAt.IsZero() {
		t.Fatalf("confirmation provenance was lost: %+v", confirmed)
	}
	if fact.FromHypothesis != proposal.ID {
		t.Fatalf("fact does not name its source hypothesis: %+v", fact)
	}
	joined := strings.Join(ContextFor(taskFromController(t, controller), workspace.Ask, ""), "\n")
	if !strings.Contains(joined, "Verified facts") {
		t.Fatalf("confirmed hypothesis did not appear as a verified fact:\n%s", joined)
	}
	if got := strings.Count(joined, proposal.Text); got != 1 {
		t.Fatalf("confirmed statement appeared %d times, want once (no hypothesis/fact duplication):\n%s", got, joined)
	}

	rejected, err := controller.RecordHypothesis(HypothesisInput{Text: "the crash comes from the renderer"})
	if err != nil {
		t.Fatalf("RecordHypothesis: %v", err)
	}
	if _, err := controller.RejectHypothesis(rejected.ID, "the reproduction disproved it"); err != nil {
		t.Fatalf("RejectHypothesis: %v", err)
	}
	current, _ = controller.Current()
	rejectedAfter := findHypothesis(t, current, rejected.ID)
	if rejectedAfter.Status != HypothesisRejected || !strings.Contains(rejectedAfter.Resolution, "reproduction") {
		t.Fatalf("rejected hypothesis lost its provenance: %+v", rejectedAfter)
	}
	if len(current.Context.Facts) != 1 {
		t.Fatalf("rejection created a fact: %+v", current.Context.Facts)
	}
}

// Contradicting evidence is represented without deleting or rewriting the
// fact, and invalidation keeps the historical record inspectable.
func TestContradictoryEvidenceDoesNotOverwriteFacts(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	if _, err := controller.Begin(testIntent("explain the failure"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	support, err := controller.RecordEvidence(EvidenceInput{
		Kind:    EvidenceTest,
		Source:  "go test ./task -run TestX",
		Summary: "TestX passed",
		Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	fact, err := controller.RecordFact(FactInput{Text: "TestX passes on this revision", EvidenceIDs: []string{support.ID}})
	if err != nil {
		t.Fatalf("RecordFact: %v", err)
	}
	contradiction, err := controller.RecordEvidence(EvidenceInput{
		Kind:        EvidenceTest,
		Source:      "go test ./task -run TestX -count=1",
		Summary:     "TestX failed under a clean cache",
		Outcome:     EvidenceContradicting,
		Contradicts: []string{fact.ID},
	})
	if err != nil {
		t.Fatalf("RecordEvidence contradiction: %v", err)
	}

	current, _ := controller.Current()
	stored := findFact(t, current, fact.ID)
	if stored.Status != FactActive {
		t.Fatalf("a contradicted fact was silently rewritten to %s", stored.Status)
	}
	if got := current.ContradictingEvidence(fact.ID); len(got) != 1 || got[0].ID != contradiction.ID {
		t.Fatalf("contradiction was not retained: %+v", got)
	}
	joined := strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	if !strings.Contains(joined, "DISPUTED") {
		t.Fatalf("contradicted fact was assembled without its dispute:\n%s", joined)
	}

	if _, err := controller.InvalidateEvidence(support.ID, "the result was produced against another revision"); err != nil {
		t.Fatalf("InvalidateEvidence: %v", err)
	}
	current, _ = controller.Current()
	stored = findFact(t, current, fact.ID)
	if stored.Status != FactInvalidated {
		t.Fatalf("fact status = %s after its only support was invalidated, want invalidated", stored.Status)
	}
	if len(current.Context.Facts) != 1 {
		t.Fatal("invalidation deleted the fact instead of retaining history")
	}

	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	recoveredEvidence, ok := restored.Context.evidenceByID(contradiction.ID)
	if !ok {
		t.Fatal("contradicting evidence did not survive recovery")
	}
	if len(recoveredEvidence.Contradicts) != 1 || recoveredEvidence.Contradicts[0] != fact.ID {
		t.Fatalf("contradiction association was lost: %+v", recoveredEvidence)
	}
}

// Evidence is task-linked, deduplicated, and bounded to a reference rather
// than a copy of the tool output.
func TestEvidenceIsTaskLinkedAndDeduplicated(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	begun, err := controller.Begin(testIntent("describe the repository"), nil)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	first, err := controller.RecordEvidence(EvidenceInput{
		Kind:     EvidenceTool,
		Source:   "read:call-1",
		Artifact: "session:1#call-1",
		Summary:  "README.md starts with the project name",
		Outcome:  EvidenceObserved,
		DedupKey: "read:README.md:1",
		Digest:   "sha256:abc",
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	if first.TaskID != begun.ID {
		t.Fatalf("evidence task = %q, want %q", first.TaskID, begun.ID)
	}
	second, err := controller.RecordEvidence(EvidenceInput{
		Kind:     EvidenceTool,
		Source:   "read:call-2",
		Summary:  "duplicate observation",
		Outcome:  EvidenceObserved,
		DedupKey: "read:README.md:1",
	})
	if err != nil {
		t.Fatalf("duplicate RecordEvidence: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("dedup created a second record: %q -> %q", first.ID, second.ID)
	}
	current, _ := controller.Current()
	if len(current.Context.Evidence) != 1 {
		t.Fatalf("evidence records = %d, want 1", len(current.Context.Evidence))
	}
	if len(current.Context.Evidence[0].Summary) > maxEvidenceSummaryBytes {
		t.Fatalf("evidence summary was not bounded: %d bytes", len(current.Context.Evidence[0].Summary))
	}
}

// Context assembly is deterministic, bounded, role-scoped, and descriptive
// only: recording a BUILD scope cannot make FUZZ able to mutate.
func TestContextAssemblyIsDeterministicBoundedAndRoleScoped(t *testing.T) {
	role := workspace.Ask
	controller := NewController(NewStore(t.TempDir()), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("understand the failure"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind:    EvidenceCommand,
		Source:  "go test ./...",
		Summary: "one package failed",
		Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	fact, err := controller.RecordFact(FactInput{Text: "one package fails", EvidenceIDs: []string{evidence.ID}})
	if err != nil {
		t.Fatalf("RecordFact: %v", err)
	}
	hypothesis, err := controller.RecordHypothesis(HypothesisInput{Text: "the cache is stale"})
	if err != nil {
		t.Fatalf("RecordHypothesis: %v", err)
	}
	question, err := controller.AddQuestion("is the failure reproducible on a clean cache?")
	if err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Ask, RoleContextUpdate{
		Reasoning: []string{"ask-reasoning-marker"},
		Progress:  []string{"ask-progress-marker"},
	}); err != nil {
		t.Fatalf("ASK context: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Fuzz, RoleContextUpdate{
		Strategy:      "fuzz-strategy-marker",
		HypothesisIDs: []string{hypothesis.ID},
		Progress:      []string{"fuzz-progress-marker"},
	}); err != nil {
		t.Fatalf("FUZZ context: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Build, RoleContextUpdate{
		Plan:  []string{"build-plan-marker"},
		Scope: []string{"build-scope-marker"},
	}); err != nil {
		t.Fatalf("BUILD context: %v", err)
	}

	task := taskFromController(t, controller)
	askOne := ContextFor(task, workspace.Ask, "last action")
	askTwo := ContextFor(task, workspace.Ask, "last action")
	if strings.Join(askOne, "\n") != strings.Join(askTwo, "\n") {
		t.Fatal("the same task and role produced different context")
	}
	ask := strings.Join(askOne, "\n")
	for _, want := range []string{"Verified facts", fact.Text, "Hypotheses", hypothesis.Text,
		"Open questions", question.Text, "Evidence", "Authority: ASK", "ask-reasoning-marker"} {
		if !strings.Contains(ask, want) {
			t.Errorf("ASK context is missing %q:\n%s", want, ask)
		}
	}
	for _, forbidden := range []string{"fuzz-strategy-marker", "build-plan-marker", "build-scope-marker"} {
		if strings.Contains(ask, forbidden) {
			t.Errorf("ASK context leaked another role's %q:\n%s", forbidden, ask)
		}
	}

	fuzz := strings.Join(ContextFor(task, workspace.Fuzz, "last action"), "\n")
	if !strings.Contains(fuzz, "fuzz-strategy-marker") || !strings.Contains(fuzz, "FUZZ context") {
		t.Fatalf("FUZZ context did not assemble its own execution context:\n%s", fuzz)
	}
	if strings.Contains(fuzz, "ask-reasoning-marker") || strings.Contains(fuzz, "build-plan-marker") {
		t.Fatalf("FUZZ context leaked another role's context:\n%s", fuzz)
	}

	build := strings.Join(ContextFor(task, workspace.Build, "last action"), "\n")
	if !strings.Contains(build, "build-plan-marker") || !strings.Contains(build, "build-scope-marker") {
		t.Fatalf("BUILD context did not assemble its plan and scope:\n%s", build)
	}
	if strings.Contains(build, "fuzz-strategy-marker") || strings.Contains(build, "ask-reasoning-marker") {
		t.Fatalf("BUILD context leaked another role's context:\n%s", build)
	}

	unresolved := strings.Join(ContextFor(task, "", "last action"), "\n")
	if !strings.Contains(unresolved, "Authority: unresolved") {
		t.Fatalf("an unresolved role was not exposed:\n%s", unresolved)
	}
	if strings.Contains(unresolved, "fuzz-strategy-marker") || strings.Contains(unresolved, "build-plan-marker") {
		t.Fatalf("unresolved context guessed a role:\n%s", unresolved)
	}

	if len(ask) > contextBudget {
		t.Fatalf("context = %d chars, want <= %d", len(ask), contextBudget)
	}
	if got := tokens.Count(ask, tokens.ModeEstimate); got > contextTokenBudget {
		t.Fatalf("context = %d estimated tokens, want <= %d", got, contextTokenBudget)
	}

	// Recording BUILD's scope is context, not a grant. The workspace gate is
	// unchanged: FUZZ still cannot write.
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("task context granted mutation authority to FUZZ")
	}
}

// Detached tasks keep separate context and resuming one restores only its own
// facts.
func TestArchivedTasksKeepIndependentContext(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)

	first, err := controller.Begin(testIntent("first objective"), nil)
	if err != nil {
		t.Fatalf("Begin first: %v", err)
	}
	firstEvidence, _ := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "first-command", Summary: "first observation", Outcome: EvidenceObserved,
	})
	if _, err := controller.RecordFact(FactInput{Text: "first-fact-marker", EvidenceIDs: []string{firstEvidence.ID}}); err != nil {
		t.Fatalf("first fact: %v", err)
	}
	if _, err := controller.Pause("switch"); err != nil {
		t.Fatalf("Pause first: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach first: %v", err)
	}

	second, err := controller.Begin(testIntent("second objective"), nil)
	if err != nil {
		t.Fatalf("Begin second: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("the second objective reused the first task identity")
	}
	secondEvidence, _ := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "second-command", Summary: "second observation", Outcome: EvidenceObserved,
	})
	if _, err := controller.RecordFact(FactInput{Text: "second-fact-marker", EvidenceIDs: []string{secondEvidence.ID}}); err != nil {
		t.Fatalf("second fact: %v", err)
	}
	if _, err := controller.Pause("switch"); err != nil {
		t.Fatalf("Pause second: %v", err)
	}
	if _, _, err := controller.Detach(); err != nil {
		t.Fatalf("Detach second: %v", err)
	}

	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 2 {
		t.Fatalf("resumable = %d, want 2", len(resumable))
	}
	if _, err := controller.ResumeTask(first.ID); err != nil {
		t.Fatalf("ResumeTask first: %v", err)
	}
	current, _ := controller.Current()
	joined := strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	if !strings.Contains(joined, "first-fact-marker") || strings.Contains(joined, "second-fact-marker") {
		t.Fatalf("first task resumed with the wrong context:\n%s", joined)
	}
	if _, err := controller.ResumeTask(second.ID); err != nil {
		t.Fatalf("ResumeTask second: %v", err)
	}
	current, _ = controller.Current()
	joined = strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	if !strings.Contains(joined, "second-fact-marker") || strings.Contains(joined, "first-fact-marker") {
		t.Fatalf("second task resumed with the wrong context:\n%s", joined)
	}
}

// A malformed optional context is repaired on read rather than making the task
// unrecoverable, and the repair is surfaced.
func TestMalformedOptionalContextIsRepairedAndExposed(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	if _, err := controller.Begin(testIntent("recover the context"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, _ := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "go test ./...", Summary: "tests passed", Outcome: EvidenceObserved,
	})
	if _, err := controller.RecordFact(FactInput{Text: "tests pass", EvidenceIDs: []string{evidence.ID}}); err != nil {
		t.Fatalf("RecordFact: %v", err)
	}
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	progress, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress: found=%v err=%v", found, err)
	}
	progress.Context.Facts = append(progress.Context.Facts, Fact{
		ID: "bad-fact", TaskID: progress.TaskID, Text: "unsupported", Role: workspace.Build, Status: FactActive,
	})
	progress.Context.Roles = append(progress.Context.Roles, RoleContext{Role: "invalid-role"})
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		t.Fatalf("marshal progress: %v", err)
	}
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write malformed progress: %v", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load repaired context: %v", err)
	}
	if !snap.Found || snap.Task.ID == "" {
		t.Fatal("the task was lost with the malformed optional context")
	}
	if snap.Task.ContextWarning == "" {
		t.Fatal("a repaired context was not exposed")
	}
	if got := len(snap.Task.Context.Facts); got != 1 {
		t.Fatalf("facts = %d, want the one valid fact retained", got)
	}
	if err := snap.Task.Context.Validate(snap.Task.ID); err != nil {
		t.Fatalf("repaired context is not internally consistent: %v", err)
	}

	// A context version this build does not understand is refused without
	// making the task unrecoverable.
	progress.Context.Version = ContextVersion + 1
	data, _ = json.MarshalIndent(progress, "", "  ")
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write future context: %v", err)
	}
	snap, err = store.Load()
	if err != nil {
		t.Fatalf("Load future context: %v", err)
	}
	if snap.Task.ContextWarning == "" || !strings.Contains(snap.Task.ContextWarning, "newer") {
		t.Fatalf("future context was not reported: %q", snap.Task.ContextWarning)
	}
	if !snap.Task.Context.Empty() {
		t.Fatal("a future context version was decoded as if understood")
	}
}

// A checkpoint written before structured context existed loads exactly as
// before and does not report a spurious warning.
func TestLegacyCheckpointWithoutContextLoads(t *testing.T) {
	store := NewStore(t.TempDir())
	legacy := Task{
		ID:         "legacy-task",
		Objective:  "legacy objective",
		Status:     StatusPaused,
		UpdatedAt:  time.Now().UTC(),
		CreatedAt:  time.Now().UTC(),
		NextAction: "continue",
	}
	if err := store.Save(legacy); err != nil {
		t.Fatalf("Save legacy: %v", err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if !snap.Found || snap.Task.ID != legacy.ID {
		t.Fatalf("legacy task not recovered: %+v", snap.Task)
	}
	if snap.Task.ContextWarning != "" {
		t.Fatalf("legacy checkpoint reported a context problem: %q", snap.Task.ContextWarning)
	}
}

// A readable projection without a checkpoint recovers the task but reports the
// missing structured context instead of inventing continuity.
func TestMissingCheckpointExposesMissingContext(t *testing.T) {
	store := NewStore(t.TempDir())
	task, err := New("recover from the projection")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	task.Status = StatusPaused
	if err := store.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Remove(store.ProgressPath()); err != nil {
		t.Fatalf("remove progress: %v", err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !snap.Found || snap.Task.ID != task.ID {
		t.Fatalf("projection was not recovered: %+v", snap.Task)
	}
	if !strings.Contains(snap.Task.ContextWarning, "no progress checkpoint") {
		t.Fatalf("missing context was not exposed: %q", snap.Task.ContextWarning)
	}
	joined := strings.Join(ContextFor(snap.Task, workspace.Build, ""), "\n")
	if !strings.Contains(joined, "Continuity:") {
		t.Fatalf("assembly did not expose missing continuity:\n%s", joined)
	}
}

// An invalid context is refused before any file is written, and a write
// failure restores the in-memory task instead of looking applied.
func TestContextWritesAreValidatedAndAtomic(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	invalid := Task{
		ID:        "task-invalid",
		Objective: "invalid context",
		Status:    StatusRunning,
		UpdatedAt: time.Now().UTC(),
		Context: SharedContext{
			Version: ContextVersion,
			Facts: []Fact{{
				ID: "fact-1", TaskID: "task-invalid", Text: "no support",
				Role: workspace.Build, Status: FactActive,
			}},
		},
	}
	if err := store.Save(invalid); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Save invalid context = %v, want ErrInvalidTask", err)
	}
	if _, err := os.Stat(store.ProgressPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an invalid context was written to disk: %v", err)
	}

	failing := NewController(store, nil)
	if _, err := failing.Begin(testIntent("write should fail"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Make the next checkpoint impossible: .ohgo becomes a regular file.
	if err := os.RemoveAll(filepath.Join(root, ".ohgo")); err != nil {
		t.Fatalf("remove .ohgo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ohgo"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("block .ohgo: %v", err)
	}
	if _, err := failing.RecordHypothesis(HypothesisInput{Text: "should not appear applied"}); err == nil {
		t.Fatal("a failed context write reported success")
	}
	current, _ := failing.Current()
	if len(current.Context.Hypotheses) != 0 {
		t.Fatalf("a failed write left a phantom hypothesis: %+v", current.Context.Hypotheses)
	}
}

// Supersession keeps earlier conclusions and evidence in the record while
// assembly presents only the currently valid conclusion.
func TestSupersessionRetainsHistory(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	if _, err := controller.Begin(testIntent("replace an earlier conclusion"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	firstEvidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run-1", Summary: "first observation", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence first: %v", err)
	}
	firstFact, err := controller.RecordFact(FactInput{Text: "old-conclusion-marker", EvidenceIDs: []string{firstEvidence.ID}})
	if err != nil {
		t.Fatalf("RecordFact first: %v", err)
	}
	secondEvidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run-2", Summary: "second observation", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence second: %v", err)
	}
	secondFact, err := controller.RecordFact(FactInput{Text: "new-conclusion-marker", EvidenceIDs: []string{secondEvidence.ID}})
	if err != nil {
		t.Fatalf("RecordFact second: %v", err)
	}
	superseded, err := controller.SupersedeFact(firstFact.ID, secondFact.ID)
	if err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	if superseded.Status != FactSuperseded || superseded.SupersededBy != secondFact.ID {
		t.Fatalf("fact supersession provenance lost: %+v", superseded)
	}
	oldEvidence, err := controller.SupersedeEvidence(firstEvidence.ID, secondEvidence.ID)
	if err != nil {
		t.Fatalf("SupersedeEvidence: %v", err)
	}
	if oldEvidence.Validity != EvidenceSuperseded {
		t.Fatalf("evidence validity = %s, want superseded", oldEvidence.Validity)
	}

	firstHypothesis, err := controller.RecordHypothesis(HypothesisInput{Text: "old-hypothesis-marker"})
	if err != nil {
		t.Fatalf("RecordHypothesis first: %v", err)
	}
	secondHypothesis, err := controller.RecordHypothesis(HypothesisInput{Text: "new-hypothesis-marker"})
	if err != nil {
		t.Fatalf("RecordHypothesis second: %v", err)
	}
	supersededHypothesis, err := controller.SupersedeHypothesis(firstHypothesis.ID, secondHypothesis.ID)
	if err != nil {
		t.Fatalf("SupersedeHypothesis: %v", err)
	}
	if supersededHypothesis.Status != HypothesisSuperseded || supersededHypothesis.SupersededBy != secondHypothesis.ID {
		t.Fatalf("hypothesis supersession provenance lost: %+v", supersededHypothesis)
	}

	current, _ := controller.Current()
	joined := strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	if !strings.Contains(joined, "new-conclusion-marker") || strings.Contains(joined, "old-conclusion-marker") {
		t.Fatalf("assembly did not present the superseding fact instead of the old one:\n%s", joined)
	}
	if !strings.Contains(joined, "new-hypothesis-marker") || !strings.Contains(joined, "old-hypothesis-marker") {
		t.Fatalf("assembly did not retain the superseded hypothesis with its replacement:\n%s", joined)
	}
	if !strings.Contains(joined, "[superseded/BUILD] old-hypothesis-marker") {
		t.Fatalf("superseded hypothesis was not labelled with its disposition:\n%s", joined)
	}
	if len(current.Context.Facts) != 2 || len(current.Context.Evidence) != 2 || len(current.Context.Hypotheses) != 2 {
		t.Fatalf("supersession deleted history: facts=%d evidence=%d hypotheses=%d",
			len(current.Context.Facts), len(current.Context.Evidence), len(current.Context.Hypotheses))
	}

	reopened := NewController(store, nil)
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if findFact(t, restored, firstFact.ID).Status != FactSuperseded {
		t.Fatal("superseded fact status did not survive recovery")
	}
	if findHypothesis(t, restored, firstHypothesis.ID).Status != HypothesisSuperseded {
		t.Fatal("superseded hypothesis status did not survive recovery")
	}
}

func taskFromController(t *testing.T, controller *Controller) Task {
	t.Helper()
	current, ok := controller.Current()
	if !ok {
		t.Fatal("no active task")
	}
	return current
}

func findFact(t *testing.T, task Task, id string) Fact {
	t.Helper()
	for _, fact := range task.Context.Facts {
		if fact.ID == id {
			return fact
		}
	}
	t.Fatalf("fact %q not found", id)
	return Fact{}
}

func findHypothesis(t *testing.T, task Task, id string) Hypothesis {
	t.Helper()
	for _, hypothesis := range task.Context.Hypotheses {
		if hypothesis.ID == id {
			return hypothesis
		}
	}
	t.Fatalf("hypothesis %q not found", id)
	return Hypothesis{}
}

// A corrupted role list with non-adjacent duplicates must be repaired: the
// task stays readable, the repair is reported, and later writes remain possible.
func TestMalformedNonAdjacentDuplicateRolesAreRepairedAndWritable(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	if _, err := controller.Begin(testIntent("repair duplicate roles"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Ask, RoleContextUpdate{Reasoning: []string{"ask-reasoning"}}); err != nil {
		t.Fatalf("ASK context: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Fuzz, RoleContextUpdate{Strategy: "fuzz-strategy"}); err != nil {
		t.Fatalf("FUZZ context: %v", err)
	}
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	progress, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress: found=%v err=%v", found, err)
	}
	progress.Context.Roles = append(progress.Context.Roles, RoleContext{
		Role: workspace.Ask, Reasoning: []string{"duplicate-ask"}, UpdatedAt: time.Now().UTC(),
	})
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write malformed progress: %v", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Task.ContextWarning == "" {
		t.Fatal("the role repair was not reported")
	}
	if got := len(snap.Task.Context.Roles); got != 3 {
		t.Fatalf("roles = %d, want BUILD, ASK, FUZZ after the duplicate is dropped", got)
	}
	if err := snap.Task.Context.Validate(snap.Task.ID); err != nil {
		t.Fatalf("repaired context is not valid: %v", err)
	}
	seen := map[workspace.Workspace]bool{}
	for _, role := range snap.Task.Context.Roles {
		if seen[role.Role] {
			t.Fatalf("duplicate role %q survived repair", role.Role)
		}
		seen[role.Role] = true
	}

	reopened := NewController(store, nil)
	if _, _, err := reopened.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := reopened.RecordHypothesis(HypothesisInput{Text: "the task remains writable"}); err != nil {
		t.Fatalf("a repaired task refused a later context write: %v", err)
	}
	saved, found, err := store.LoadProgress()
	if err != nil || !found {
		t.Fatalf("LoadProgress after write: found=%v err=%v", found, err)
	}
	if got := len(saved.Context.Hypotheses); got != 1 {
		t.Fatalf("hypotheses after the write = %d, want 1", got)
	}
}

// Verified facts are selected by relevance under the budget, not by a fixed
// recency window: older facts survive, and an overfull section reports what it
// omitted instead of silently pretending to be complete.
func TestContextSelectionKeepsOlderFactsAndReportsOmissions(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("select facts by relevance"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceSource, Source: "src", Summary: "support", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := controller.RecordFact(FactInput{
			Text:        fmt.Sprintf("fact-marker-%02d", i),
			EvidenceIDs: []string{evidence.ID},
		}); err != nil {
			t.Fatalf("RecordFact %d: %v", i, err)
		}
	}
	current, _ := controller.Current()
	joined := strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	for i := 0; i < 12; i++ {
		if marker := fmt.Sprintf("fact-marker-%02d", i); !strings.Contains(joined, marker) {
			t.Errorf("older fact %q was discarded below the budget:\n%s", marker, joined)
		}
	}
	if len(joined) > contextBudget {
		t.Fatalf("context = %d chars, want <= %d", len(joined), contextBudget)
	}

	for i := 12; i < 40; i++ {
		if _, err := controller.RecordFact(FactInput{
			Text:        fmt.Sprintf("fact-marker-%02d %s", i, strings.Repeat("x", 40)),
			EvidenceIDs: []string{evidence.ID},
		}); err != nil {
			t.Fatalf("RecordFact %d: %v", i, err)
		}
	}
	current, _ = controller.Current()
	joined = strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	if !strings.Contains(joined, "fact-marker-00") {
		t.Fatalf("the oldest fact was discarded only because newer facts exist:\n%s", joined)
	}
	if !strings.Contains(joined, "(+") {
		t.Fatalf("an overfull fact section did not report its omissions:\n%s", joined)
	}
	if len(joined) > contextBudget {
		t.Fatalf("context = %d chars, want <= %d", len(joined), contextBudget)
	}
	if again := strings.Join(ContextFor(current, workspace.Build, ""), "\n"); again != joined {
		t.Fatal("the same task state produced different context")
	}
}

// The active role's execution context is reserved, so a full shared-knowledge
// briefing cannot push it out of the model's view.
func TestContextReservesActiveRoleContextUnderPressure(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("role context under pressure"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "go test ./...", Summary: "tests pass", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	for i := 0; i < 40; i++ {
		if _, err := controller.RecordEvidence(EvidenceInput{
			Kind: EvidenceCommand, Source: "run",
			Summary: fmt.Sprintf("evidence-%02d %s", i, strings.Repeat("e", 80)),
			Outcome: EvidenceObserved,
		}); err != nil {
			t.Fatalf("evidence %d: %v", i, err)
		}
		if _, err := controller.RecordFact(FactInput{
			Text: fmt.Sprintf("fact-%02d %s", i, strings.Repeat("f", 40)), EvidenceIDs: []string{evidence.ID},
		}); err != nil {
			t.Fatalf("fact %d: %v", i, err)
		}
		if _, err := controller.RecordHypothesis(HypothesisInput{
			Text: fmt.Sprintf("hypothesis-%02d %s", i, strings.Repeat("h", 40)),
		}); err != nil {
			t.Fatalf("hypothesis %d: %v", i, err)
		}
		if _, err := controller.AddQuestion(fmt.Sprintf("question-%02d %s", i, strings.Repeat("q", 40))); err != nil {
			t.Fatalf("question %d: %v", i, err)
		}
	}
	if _, err := controller.UpdateRoleContext(workspace.Build, RoleContextUpdate{
		Plan:  []string{"role-plan-marker"},
		Scope: []string{"role-scope-marker"},
	}); err != nil {
		t.Fatalf("BUILD context: %v", err)
	}
	current, _ := controller.Current()
	// Fill the pre-role sections to the section caps. Without a reserved share,
	// the objective, constraints, todo list, facts, and hypotheses consume the
	// whole briefing before role context is reached.
	current.Objective = strings.Repeat("objective ", 140)
	current.Constraints = []string{strings.Repeat("constraint ", 120)}
	for i := 0; i < 12; i++ {
		current.Todos = append(current.Todos, Todo{
			ID: fmt.Sprintf("todo-%d", i), Label: strings.Repeat("todo ", 30), Status: TodoTodo,
		})
	}
	joined := strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	for _, want := range []string{"role-plan-marker", "role-scope-marker"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the active role context lost %q under budget pressure:\n%s", want, joined)
		}
	}
	if len(joined) > contextBudget {
		t.Fatalf("context = %d chars, want <= %d", len(joined), contextBudget)
	}
	if got := tokens.Count(joined, tokens.ModeEstimate); got > contextTokenBudget {
		t.Fatalf("context = %d estimated tokens, want <= %d", got, contextTokenBudget)
	}
}

// Invalid and superseded evidence is history; it must not appear as current
// evidence or crowd out the records that still apply.
func TestCurrentEvidenceAssemblyExcludesStaleRecords(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("evidence validity"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	currentRecord, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run-1", Summary: "current-marker", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("current evidence: %v", err)
	}
	inconclusive, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run-2", Summary: "inconclusive-marker", Outcome: EvidenceInconclusive,
	})
	if err != nil {
		t.Fatalf("inconclusive evidence: %v", err)
	}
	stale, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run-3", Summary: "invalid-marker", Outcome: EvidenceInconclusive,
	})
	if err != nil {
		t.Fatalf("stale evidence: %v", err)
	}
	if _, err := controller.InvalidateEvidence(stale.ID, "different revision"); err != nil {
		t.Fatalf("InvalidateEvidence: %v", err)
	}
	old, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run-4", Summary: "superseded-marker", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("old evidence: %v", err)
	}
	replacement, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run-5", Summary: "replacement-marker", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("replacement evidence: %v", err)
	}
	if _, err := controller.SupersedeEvidence(old.ID, replacement.ID); err != nil {
		t.Fatalf("SupersedeEvidence: %v", err)
	}

	current, _ := controller.Current()
	joined := strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	for _, want := range []string{"current-marker", "inconclusive-marker", "replacement-marker"} {
		if !strings.Contains(joined, want) {
			t.Errorf("current evidence is missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"invalid-marker", "superseded-marker"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("stale evidence %q was presented as current:\n%s", forbidden, joined)
		}
	}
	_ = currentRecord
	_ = inconclusive
}

// A fact copied from a confirmations must name the role that confirmed it,
// while the hypothesis keeps the role that proposed it.
func TestConfirmedHypothesisRecordsConfirmingRole(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("confirm across roles"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	hypothesis, err := controller.RecordHypothesis(HypothesisInput{
		Text: "the cache is stale", Role: workspace.Fuzz,
	})
	if err != nil {
		t.Fatalf("RecordHypothesis: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceReproduction, Role: workspace.Fuzz, Source: "run", Summary: "reproduced", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	confirmed, fact, err := controller.ConfirmHypothesis(hypothesis.ID, []string{evidence.ID}, workspace.Build)
	if err != nil {
		t.Fatalf("ConfirmHypothesis: %v", err)
	}
	if confirmed.Role != workspace.Fuzz || confirmed.ResolvedBy != workspace.Build {
		t.Fatalf("hypothesis provenance = proposer %q resolver %q", confirmed.Role, confirmed.ResolvedBy)
	}
	if fact.Role != workspace.Build {
		t.Fatalf("fact role = %q, want the confirming role BUILD", fact.Role)
	}
	if fact.FromHypothesis != hypothesis.ID {
		t.Fatalf("fact lost its source hypothesis: %+v", fact)
	}
}

// A dedup key identifies one observation. Reusing it for a different outcome
// or contradiction target is refused rather than silently swallowing it.
func TestDedupKeyCannotReuseADifferentObservation(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("dedup integrity"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	first, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run", Summary: "first", Outcome: EvidenceObserved, DedupKey: "same-key",
	})
	if err != nil {
		t.Fatalf("first evidence: %v", err)
	}
	replay, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run", Summary: "replay", Outcome: EvidenceObserved, DedupKey: "same-key",
	})
	if err != nil {
		t.Fatalf("replay evidence: %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay created a second record: %q -> %q", first.ID, replay.ID)
	}
	if _, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run", Summary: "different", Outcome: EvidenceInconclusive, DedupKey: "same-key",
	}); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("a different outcome under the same key = %v, want ErrInvalidContext", err)
	}
}

// A supersession pointer whose target was dropped in repair is cleared, not
// left claiming a replacement that does not exist.
func TestDanglingSupersessionPointersAreRepaired(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	if _, err := controller.Begin(testIntent("repair supersession"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceCommand, Source: "run", Summary: "support", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	fact, err := controller.RecordFact(FactInput{Text: "a fact", EvidenceIDs: []string{evidence.ID}})
	if err != nil {
		t.Fatalf("fact: %v", err)
	}
	hypothesis, err := controller.RecordHypothesis(HypothesisInput{Text: "a hypothesis"})
	if err != nil {
		t.Fatalf("hypothesis: %v", err)
	}
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	progress, _, _ := store.LoadProgress()
	for i := range progress.Context.Facts {
		if progress.Context.Facts[i].ID == fact.ID {
			progress.Context.Facts[i].Status = FactSuperseded
			progress.Context.Facts[i].SupersededBy = "ghost-fact"
		}
	}
	for i := range progress.Context.Hypotheses {
		if progress.Context.Hypotheses[i].ID == hypothesis.ID {
			progress.Context.Hypotheses[i].Status = HypothesisSuperseded
			progress.Context.Hypotheses[i].SupersededBy = "ghost-hypothesis"
		}
	}
	data, _ := json.MarshalIndent(progress, "", "  ")
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Task.ContextWarning == "" {
		t.Fatal("dangling supersession was not reported")
	}
	if err := snap.Task.Context.Validate(snap.Task.ID); err != nil {
		t.Fatalf("repaired context is not valid: %v", err)
	}
	for _, stored := range snap.Task.Context.Facts {
		if stored.ID == fact.ID && stored.SupersededBy != "" {
			t.Fatalf("dangling fact replacement survived: %+v", stored)
		}
	}
	for _, stored := range snap.Task.Context.Hypotheses {
		if stored.ID == hypothesis.ID && stored.SupersededBy != "" {
			t.Fatalf("dangling hypothesis replacement survived: %+v", stored)
		}
	}
}

// Repairing a confirmed hypothesis whose support disappeared must not leave a
// "confirmed" resolution next to an invalidated status.
func TestRepairedInvalidatedHypothesisRewritesConfirmationResolution(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	controller := NewController(store, nil)
	if _, err := controller.Begin(testIntent("repair invalidation"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	evidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "run", Summary: "passed", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	hypothesis, err := controller.RecordHypothesis(HypothesisInput{Text: "the fix is correct"})
	if err != nil {
		t.Fatalf("hypothesis: %v", err)
	}
	if _, _, err := controller.ConfirmHypothesis(hypothesis.ID, []string{evidence.ID}, workspace.Build); err != nil {
		t.Fatalf("ConfirmHypothesis: %v", err)
	}
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	progress, _, _ := store.LoadProgress()
	progress.Context.Evidence = nil
	progress.Context.Facts = nil
	data, _ := json.MarshalIndent(progress, "", "  ")
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var repaired []Hypothesis
	for _, stored := range snap.Task.Context.Hypotheses {
		if stored.ID == hypothesis.ID {
			repaired = append(repaired, stored)
		}
	}
	if len(repaired) != 1 {
		t.Fatalf("hypothesis was not retained: %+v", repaired)
	}
	if repaired[0].Status != HypothesisInvalidated {
		t.Fatalf("status = %s, want invalidated", repaired[0].Status)
	}
	if strings.Contains(repaired[0].Resolution, "confirmed by supporting evidence") {
		t.Fatalf("invalidated hypothesis still claims confirmation: %q", repaired[0].Resolution)
	}
}

// The explicit active role is additive: a checkpoint written before it existed
// derives the value from its role contexts, and the next save persists it.
func TestActiveRoleIsPersistedAndMigratesFromLegacyContext(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := map[string]any{
		"version": 2,
		"taskId":  "legacy-role-task",
		"status":  "PAUSED",
		"context": map[string]any{
			"version": 1,
			"roles": []map[string]any{
				{"role": "ask", "updatedAt": time.Now().Add(-time.Minute).UTC()},
				{"role": "fuzz", "updatedAt": time.Now().UTC()},
			},
		},
		"checkpointAt": time.Now().UTC(),
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Task.ActiveRole != workspace.Fuzz {
		t.Fatalf("migrated active role = %q, want %q", snap.Task.ActiveRole, workspace.Fuzz)
	}
	if snap.Task.LastRole() != workspace.Fuzz {
		t.Fatalf("LastRole = %q, want %q", snap.Task.LastRole(), workspace.Fuzz)
	}
	if err := store.Save(snap.Task); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, err := os.ReadFile(store.ProgressPath())
	if err != nil {
		t.Fatalf("read saved progress: %v", err)
	}
	if !strings.Contains(string(saved), `"activeRole": "fuzz"`) {
		t.Fatalf("saved checkpoint does not carry the explicit role:\n%s", saved)
	}
	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if reloaded.Task.ActiveRole != workspace.Fuzz {
		t.Fatalf("reloaded active role = %q, want %q", reloaded.Task.ActiveRole, workspace.Fuzz)
	}
}

// Explicit constraints are user-derived contract content, so the task records
// them and the briefing preserves them.
func TestBeginPopulatesConstraintsFromContractGrounding(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	intent := contract.IntentContract{
		Objective: "change the parser without changing the public API",
		Kind:      contract.ObjectiveMutation,
		Grounding: &contract.IntentGrounding{
			Constraints: []string{"do not change the public API", "keep the change internal"},
		},
	}
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	current, _ := controller.Current()
	if len(current.Constraints) != 2 {
		t.Fatalf("constraints = %v, want the contract's two limits", current.Constraints)
	}
	brief := strings.Join(ContextFor(current, workspace.Build, ""), "\n")
	for _, want := range current.Constraints {
		if !strings.Contains(brief, want) {
			t.Errorf("briefing is missing constraint %q:\n%s", want, brief)
		}
	}
}

// A damaged optional active role is dropped, not treated as a corrupt
// checkpoint: the task and its structured context remain recoverable.
func TestInvalidActiveRoleDoesNotHideTheCheckpoint(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	checkpoint := map[string]any{
		"version":    2,
		"taskId":     "damaged-role-task",
		"status":     "PAUSED",
		"activeRole": "not-a-role",
		"context": map[string]any{
			"version": 1,
			"roles": []map[string]any{
				{"role": "build", "updatedAt": time.Now().UTC()},
			},
		},
		"checkpointAt": time.Now().UTC(),
	}
	data, _ := json.MarshalIndent(checkpoint, "", "  ")
	if err := os.WriteFile(store.ProgressPath(), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if snap.Task.ID != "damaged-role-task" {
		t.Fatalf("task identity was lost: %+v", snap.Task)
	}
	if len(snap.Task.Context.Roles) != 1 {
		t.Fatalf("structured context was lost with the optional role: %+v", snap.Task.Context)
	}
	if snap.Task.ActiveRole != workspace.Build {
		t.Fatalf("active role = %q, want the role-context fallback BUILD", snap.Task.ActiveRole)
	}
}

// A damaged optional role in an archived task is dropped, not allowed to make
// the task unwritable after resume.
func TestArchivedInvalidActiveRoleIsDropped(t *testing.T) {
	store := NewStore(t.TempDir())
	recorded := Task{
		ID:         "archived-role",
		Status:     StatusPaused,
		Objective:  "resume the archived task",
		ActiveRole: "not-a-role",
		Context: SharedContext{
			Version: ContextVersion,
			Roles: []RoleContext{{
				Role: workspace.Fuzz, UpdatedAt: time.Now().UTC(),
			}},
		},
		UpdatedAt: time.Now().UTC(),
	}
	file := archivedFile{Task: recorded, Progress: Progress{Version: ProgressVersion, TaskID: recorded.ID, Status: recorded.Status}}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(store.ArchiveDir(), 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if err := os.WriteFile(store.archivePath(recorded.ID), data, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	snap, err := store.LoadArchived(recorded.ID)
	if err != nil {
		t.Fatalf("LoadArchived: %v", err)
	}
	if !snap.Found {
		t.Fatal("archive not found")
	}
	if snap.Task.ActiveRole != "" {
		t.Fatalf("invalid archived role survived: %q", snap.Task.ActiveRole)
	}
	if snap.Task.LastRole() != workspace.Fuzz {
		t.Fatalf("role-context fallback = %q, want FUZZ", snap.Task.LastRole())
	}
	if err := snap.Task.Validate(); err != nil {
		t.Fatalf("repaired archive is not writable: %v", err)
	}
}
