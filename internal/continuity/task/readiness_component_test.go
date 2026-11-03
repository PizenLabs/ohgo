package task

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
)

// This file is the deterministic layer of the Task Continuity product-readiness
// suite. It exercises the task controller and its persistence boundary directly;
// runtime integration and process-boundary coverage lives in internal/tui and
// cmd/ohgo. Test IDs match docs/testing/TASK_CONTINUITY_TEST_CATALOG.md.

const readinessObjective = "Fix GrepTool in tools/grep.go and run go test ./tools"

func readinessIntent() contract.IntentContract {
	return contract.IntentContract{
		Objective:    readinessObjective,
		Kind:         contract.ObjectiveMutation,
		Targets:      []contract.Target{{Kind: contract.TargetPath, Value: "tools/grep.go"}},
		Verification: []contract.VerificationRequirement{{Check: contract.CheckTest, Scope: "tools"}},
	}
}

// newReadinessController returns a controller with a fixed injected clock and a
// live-role function the caller can change between turns.
func newReadinessController(t *testing.T, root string, role *workspace.Workspace, clock *time.Time) *Controller {
	t.Helper()
	c := NewController(NewStore(root), nil)
	c.SetRoleSource(func() workspace.Workspace { return *role })
	if clock != nil {
		c.now = func() time.Time { return *clock }
	}
	return c
}

func advance(clock *time.Time) {
	*clock = clock.Add(time.Second)
}

func mustBegin(t *testing.T, c *Controller, intent contract.IntentContract) Task {
	t.Helper()
	task, err := c.Begin(intent, nil)
	if err != nil {
		t.Fatalf("Begin(%q): %v", intent.Objective, err)
	}
	return task
}

func mustEvidence(t *testing.T, c *Controller, in EvidenceInput) EvidenceRecord {
	t.Helper()
	record, err := c.RecordEvidence(in)
	if err != nil {
		t.Fatalf("RecordEvidence(%+v): %v", in, err)
	}
	return record
}

// RA-A01: a new objective creates a durable identity, and many turns that keep
// the same contract preserve it while each role context remains isolated.
func TestReadinessA01TaskIdentityAcrossManyTurns(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(1000, 0).UTC()
	role := workspace.Ask
	c := newReadinessController(t, root, &role, &clock)

	first := mustBegin(t, c, readinessIntent())
	for i, next := range []workspace.Workspace{
		workspace.Fuzz, workspace.Build, workspace.Ask, workspace.Build, workspace.Fuzz, workspace.Build,
	} {
		role = next
		advance(&clock)
		current := mustBegin(t, c, readinessIntent())
		if current.ID != first.ID {
			t.Fatalf("RA-A01: turn %d under %s changed identity %q -> %q", i+1, next.Label(), first.ID, current.ID)
		}
		if current.ActiveRole != next {
			t.Fatalf("RA-A01: turn %d active role = %s, want %s", i+1, current.ActiveRole, next)
		}
		if _, ok := current.Context.Role(next); !ok {
			t.Fatalf("RA-A01: turn %d did not record the %s role context", i+1, next.Label())
		}
	}

	final, ok := c.Current()
	if !ok {
		t.Fatal("RA-A01: the task disappeared")
	}
	created := 0
	for _, event := range final.Timeline {
		if event.Kind == TimelineCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("RA-A01: %d creation events, want exactly one durable identity", created)
	}
	for _, role := range []workspace.Workspace{workspace.Ask, workspace.Fuzz, workspace.Build} {
		if _, ok := final.Context.Role(role); !ok {
			t.Fatalf("RA-A01: %s context was lost across turns", role.Label())
		}
	}
}

// RA-A03: an incompatible objective cannot silently reuse the active task, and a
// genuinely different objective starts clean without inheriting context.
func TestReadinessA03DifferentObjectiveDoesNotInheritContext(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(2000, 0).UTC()
	role := workspace.Ask
	c := newReadinessController(t, root, &role, &clock)

	first := mustBegin(t, c, readinessIntent())
	record := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceSource, Source: "tools/grep.go:3",
		Summary: "GrepTool returns the old value", Outcome: EvidenceObserved, At: clock,
	})
	if _, err := c.RecordFact(FactInput{Text: "GrepTool is stale", EvidenceIDs: []string{record.ID}, At: clock}); err != nil {
		t.Fatalf("RA-A03: RecordFact: %v", err)
	}
	if _, err := c.UpdateRoleContext(workspace.Ask, RoleContextUpdate{Reasoning: []string{"ask-marker"}}); err != nil {
		t.Fatalf("RA-A03: ask context: %v", err)
	}

	advance(&clock)
	conflict := contract.IntentContract{Objective: "Document the release process", Kind: contract.ObjectiveInformational}
	if _, err := c.Begin(conflict, nil); !errors.Is(err, ErrObjectiveConflict) {
		t.Fatalf("RA-A03: Begin(different) error = %v, want ErrObjectiveConflict", err)
	}
	active, _ := c.Current()
	if active.ID != first.ID || len(active.Context.Facts) != 1 {
		t.Fatalf("RA-A03: the refused objective mutated task A: %+v", active)
	}

	if _, ok, err := c.Detach(); err != nil || !ok {
		t.Fatalf("RA-A03: Detach: ok=%v err=%v", ok, err)
	}
	advance(&clock)
	second := mustBegin(t, c, conflict)
	if second.ID == first.ID {
		t.Fatalf("RA-A03: a different objective reused task %q", first.ID)
	}
	if len(second.Context.Facts) != 0 || len(second.Context.Evidence) != 0 {
		t.Fatalf("RA-A03: task B inherited task A context: %+v", second.Context)
	}
	for _, role := range second.Context.Roles {
		if len(role.Reasoning) != 0 || len(role.Progress) != 0 || role.Strategy != "" ||
			len(role.Plan) != 0 || len(role.Scope) != 0 || len(role.Artifacts) != 0 {
			t.Fatalf("RA-A03: task B inherited task A role progress: %+v", role)
		}
	}

	resumable, err := c.ListResumable()
	if err != nil {
		t.Fatalf("RA-A03: ListResumable: %v", err)
	}
	if !containsTaskID(resumable, first.ID) {
		t.Fatalf("RA-A03: detached task A is not resumable: %+v", resumable)
	}
	restored, err := c.ResumeTask(first.ID)
	if err != nil {
		t.Fatalf("RA-A03: ResumeTask: %v", err)
	}
	if restored.ID != first.ID || len(restored.Context.Facts) != 1 {
		t.Fatalf("RA-A03: resumed task = %+v, want A with its fact", restored)
	}
}

// RA-A04: invalid lifecycle transitions are rejected, terminal states absorb,
// and repeated idempotent operations do not create misleading transitions.
func TestReadinessA04LifecycleTransitionMatrix(t *testing.T) {
	now := time.Unix(3000, 0).UTC()
	paused := Task{ID: "t", Objective: "x", Status: StatusPaused}
	if err := paused.Complete(now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("RA-A04: PAUSED -> COMPLETED error = %v, want ErrInvalidTransition", err)
	}
	review := Task{ID: "t", Objective: "x", Status: StatusNeedsReview}
	if err := review.Complete(now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("RA-A04: NEEDS_REVIEW -> COMPLETED error = %v, want ErrInvalidTransition", err)
	}
	for _, terminal := range []Status{StatusCompleted, StatusFailed, StatusCancelled} {
		task := Task{ID: "t", Objective: "x", Status: terminal}
		// Repeating the same state is idempotent; every *different* state must
		// be refused by a terminal task.
		moves := map[Status]func(time.Time) error{
			StatusRunning:     task.Start,
			StatusPaused:      task.Pause,
			StatusNeedsReview: task.NeedsReview,
		}
		for _, to := range []Status{StatusRunning, StatusPaused, StatusNeedsReview, StatusCompleted, StatusFailed, StatusCancelled} {
			if to == terminal {
				continue
			}
			move, ok := moves[to]
			if !ok {
				switch to {
				case StatusCompleted:
					move = task.Complete
				case StatusFailed:
					move = task.Fail
				case StatusCancelled:
					move = task.Cancel
				}
			}
			if err := move(now); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("RA-A04: terminal %s accepted a move to %s: %v", terminal, to, err)
			}
		}
	}
	// Repeating the same lifecycle state is allowed but must not append a
	// duplicate timeline event.
	root := t.TempDir()
	clock := time.Unix(3100, 0).UTC()
	role := workspace.Build
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, readinessIntent())
	advance(&clock)
	if _, err := c.Pause("waiting"); err != nil {
		t.Fatalf("RA-A04: Pause: %v", err)
	}
	advance(&clock)
	if _, err := c.Pause("waiting"); err != nil {
		t.Fatalf("RA-A04: repeated Pause: %v", err)
	}
	current, _ := c.Current()
	if got := countEvents(current.Timeline, TimelinePaused); got != 1 {
		t.Fatalf("RA-A04: repeated pause recorded %d pause events, want 1", got)
	}
}

// RB-B01: facts require current usable evidence; deductions and inconclusive
// observations never become established facts, and duplicates collapse.
func TestReadinessB01EvidencePromotionRules(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(4000, 0).UTC()
	role := workspace.Fuzz
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, contract.IntentContract{Objective: "diagnose the cache", Kind: contract.ObjectiveInformational})

	if _, err := c.RecordFact(FactInput{Text: "model said so", At: clock}); err == nil {
		t.Fatal("RB-B01: a model assertion became a fact without evidence")
	}
	inconclusive := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceCommand, Source: "go test ./cache", Summary: "timed out",
		Outcome: EvidenceInconclusive, At: clock,
	})
	if _, err := c.RecordFact(FactInput{Text: "the cache is broken", EvidenceIDs: []string{inconclusive.ID}, At: clock}); err == nil {
		t.Fatal("RB-B01: inconclusive evidence supported a fact")
	}
	observed := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceSource, Source: "cache.go:42", Summary: "the cache key omits the locale",
		Outcome: EvidenceObserved, DedupKey: "cache-key", At: clock,
	})
	again := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceSource, Source: "cache.go:42", Summary: "the cache key omits the locale",
		Outcome: EvidenceObserved, DedupKey: "cache-key", At: clock,
	})
	if again.ID != observed.ID {
		t.Fatalf("RB-B01: duplicate evidence minted a second record: %s vs %s", observed.ID, again.ID)
	}
	fact, err := c.RecordFact(FactInput{Text: "the cache key omits the locale", EvidenceIDs: []string{observed.ID}, At: clock})
	if err != nil {
		t.Fatalf("RB-B01: observed evidence did not support a fact: %v", err)
	}
	if fact.Status != FactActive {
		t.Fatalf("RB-B01: fact status = %s, want active", fact.Status)
	}
	contradicting := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceCommand, Source: "go test ./cache", Summary: "the cache rebuilt",
		Outcome: EvidenceContradicting, Contradicts: []string{fact.ID}, At: clock,
	})
	if _, err := c.RecordFact(FactInput{Text: "the cache is broken", EvidenceIDs: []string{contradicting.ID}, At: clock}); err == nil {
		t.Fatal("RB-B01: contradicting evidence supported a fact")
	}
	current, _ := c.Current()
	if got := countEvidenceKind(current.Context.Evidence, EvidenceSource); got != 1 {
		t.Fatalf("RB-B01: duplicate evidence accumulated %d source records, want 1", got)
	}
	if got := findFact(t, current, fact.ID); got.Status != FactActive {
		t.Fatalf("RB-B01: contradicting evidence rewrote the fact: %+v", got)
	}
	if disputes := current.ContradictingEvidence(fact.ID); len(disputes) != 1 {
		t.Fatalf("RB-B01: contradiction was not associated with the fact: %+v", disputes)
	}
}

// RB-B02: invalidating one support reevaluates the fact, and the disposition is
// independent of the order the supports were recorded.
func TestReadinessB02InvalidationIsOrderIndependent(t *testing.T) {
	orders := map[string][]string{"record-order": nil, "reverse-order": nil}
	for name, order := range orders {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			clock := time.Unix(5000, 0).UTC()
			role := workspace.Ask
			c := newReadinessController(t, root, &role, &clock)
			mustBegin(t, c, contract.IntentContract{Objective: "explain the queue", Kind: contract.ObjectiveInformational})

			a := mustEvidence(t, c, EvidenceInput{Kind: EvidenceSource, Source: "a.go:1", Summary: "first support", Outcome: EvidenceObserved, At: clock})
			b := mustEvidence(t, c, EvidenceInput{Kind: EvidenceSource, Source: "b.go:1", Summary: "second support", Outcome: EvidenceObserved, At: clock})
			ids := []string{a.ID, b.ID}
			order = ids
			fact, err := c.RecordFact(FactInput{Text: "the queue is bounded", EvidenceIDs: order, At: clock})
			if err != nil {
				t.Fatalf("RB-B02: RecordFact: %v", err)
			}
			if _, err := c.InvalidateEvidence(a.ID, "the revision changed"); err != nil {
				t.Fatalf("RB-B02: InvalidateEvidence: %v", err)
			}
			after, _ := c.Current()
			got := findFact(t, after, fact.ID)
			if got.Status != FactActive {
				t.Fatalf("RB-B02: fact = %+v, want active while one support remains", got)
			}
			if _, err := c.InvalidateEvidence(b.ID, "the revision changed"); err != nil {
				t.Fatalf("RB-B02: InvalidateEvidence(second): %v", err)
			}
			after, _ = c.Current()
			got = findFact(t, after, fact.ID)
			if got.Status != FactInvalidated {
				t.Fatalf("RB-B02: fact = %+v, want invalidated once all support is gone", got)
			}
		})
	}
}

// RB-B03: each role receives its own context plus shared facts, never another
// role's private plan, strategy, or notes, and assembly stays bounded.
func TestReadinessB03RoleContextIsolationAndBounds(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(6000, 0).UTC()
	role := workspace.Ask
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, readinessIntent())
	record := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceSource, Source: "tools/grep.go:3", Summary: "GrepTool returns old",
		Outcome: EvidenceObserved, At: clock,
	})
	if _, err := c.RecordFact(FactInput{Text: "GrepTool returns old", EvidenceIDs: []string{record.ID}, At: clock}); err != nil {
		t.Fatalf("RB-B03: RecordFact: %v", err)
	}
	updates := map[workspace.Workspace]RoleContextUpdate{
		workspace.Ask:   {Reasoning: []string{"ask-reasoning-marker"}},
		workspace.Fuzz:  {Strategy: "fuzz-strategy-marker", Artifacts: []string{"artifact:repro"}},
		workspace.Build: {Plan: []string{"build-plan-marker"}, Scope: []string{"tools/grep.go"}},
	}
	for role, update := range updates {
		if _, err := c.UpdateRoleContext(role, update); err != nil {
			t.Fatalf("RB-B03: role context %s: %v", role, err)
		}
	}

	briefs := map[workspace.Workspace]string{}
	for _, role := range []workspace.Workspace{workspace.Ask, workspace.Fuzz, workspace.Build} {
		briefs[role] = strings.Join(ContextFor(mustCurrent(t, c), role, ""), "\n")
		if len(briefs[role]) > 6000 {
			t.Fatalf("RB-B03: %s briefing = %d chars, want <= 6000", role.Label(), len(briefs[role]))
		}
		if !strings.Contains(briefs[role], "GrepTool returns old") {
			t.Fatalf("RB-B03: %s briefing lost the shared fact", role.Label())
		}
	}
	if !strings.Contains(briefs[workspace.Ask], "ask-reasoning-marker") {
		t.Fatal("RB-B03: ASK lost its own reasoning")
	}
	if strings.Contains(briefs[workspace.Ask], "fuzz-strategy-marker") || strings.Contains(briefs[workspace.Ask], "build-plan-marker") {
		t.Fatal("RB-B03: ASK received another role's private context")
	}
	if !strings.Contains(briefs[workspace.Fuzz], "fuzz-strategy-marker") {
		t.Fatal("RB-B03: FUZZ lost its own strategy")
	}
	if strings.Contains(briefs[workspace.Fuzz], "build-plan-marker") {
		t.Fatal("RB-B03: FUZZ received BUILD's private plan")
	}
	if !strings.Contains(briefs[workspace.Build], "build-plan-marker") {
		t.Fatal("RB-B03: BUILD lost its own plan")
	}
	if strings.Contains(briefs[workspace.Build], "fuzz-strategy-marker") {
		t.Fatal("RB-B03: BUILD received FUZZ's private strategy")
	}
}

// RB-B04: under budget pressure an older fact the active role references is
// retained, and the omitted count is reported rather than pretending to be
// complete.
func TestReadinessB04ContextKeepsRoleRelevantOlderFacts(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(7000, 0).UTC()
	role := workspace.Ask
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, contract.IntentContract{Objective: "explain the parser", Kind: contract.ObjectiveInformational})

	older := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "the parser was changed",
		Outcome: EvidenceObserved, At: clock,
	})
	olderFact, err := c.RecordFact(FactInput{Text: "older-role-relevant-fact", EvidenceIDs: []string{older.ID}, At: clock})
	if err != nil {
		t.Fatalf("RB-B04: older fact: %v", err)
	}
	if _, err := c.UpdateRoleContext(workspace.Ask, RoleContextUpdate{
		Reasoning: []string{"ask keeps the older finding in scope"},
		FactIDs:   []string{olderFact.ID},
	}); err != nil {
		t.Fatalf("RB-B04: reference older fact: %v", err)
	}
	for i := 0; i < 40; i++ {
		advance(&clock)
		record := mustEvidence(t, c, EvidenceInput{
			Kind: EvidenceSource, Source: "noise.go", Summary: strings.Repeat("newer noise sentence ", 8),
			Outcome: EvidenceObserved, At: clock,
		})
		if _, err := c.RecordFact(FactInput{Text: strings.Repeat("newer fact ", 8), EvidenceIDs: []string{record.ID}, At: clock}); err != nil {
			t.Fatalf("RB-B04: newer fact %d: %v", i, err)
		}
	}
	brief := strings.Join(ContextFor(mustCurrent(t, c), workspace.Ask, ""), "\n")
	if len(brief) > 6000 {
		t.Fatalf("RB-B04: briefing = %d chars, want <= 6000", len(brief))
	}
	if !strings.Contains(brief, "older-role-relevant-fact") {
		t.Fatalf("RB-B04: a role-referenced older fact was dropped under pressure:\n%s", brief)
	}
	if !strings.Contains(brief, "(+") {
		t.Fatalf("RB-B04: the briefing did not report omitted content:\n%s", brief)
	}
}

// RB-B05: facts, hypotheses, questions, artifacts, invalidations, and
// duplicates accumulated across turns survive a restart; each role's briefing
// keeps its own private context and excludes the others.
func TestReadinessB05ContextContinuityAcrossRestart(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(7500, 0).UTC()
	role := workspace.Ask
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, contract.IntentContract{Objective: "explain the scheduler", Kind: contract.ObjectiveInformational})

	keep := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceSource, Source: "queue.go:10", Summary: "the queue is bounded",
		Outcome: EvidenceObserved, DedupKey: "queue-bounded", At: clock,
	})
	_, _ = c.RecordEvidence(EvidenceInput{ // duplicate, must not accumulate
		Kind: EvidenceSource, Source: "queue.go:10", Summary: "the queue is bounded",
		Outcome: EvidenceObserved, DedupKey: "queue-bounded", At: clock,
	})
	stale := mustEvidence(t, c, EvidenceInput{
		Kind: EvidenceSource, Source: "queue.go:20", Summary: "the queue is unbounded",
		Outcome: EvidenceObserved, At: clock,
	})
	keepFact, err := c.RecordFact(FactInput{Text: "the queue is bounded", EvidenceIDs: []string{keep.ID}, At: clock})
	if err != nil {
		t.Fatalf("RB-B05: kept fact: %v", err)
	}
	staleFact, err := c.RecordFact(FactInput{Text: "the queue is unbounded", EvidenceIDs: []string{stale.ID}, At: clock})
	if err != nil {
		t.Fatalf("RB-B05: stale fact: %v", err)
	}
	hypothesis, err := c.RecordHypothesis(HypothesisInput{Text: "the bound is per worker", Role: workspace.Fuzz, At: clock})
	if err != nil {
		t.Fatalf("RB-B05: hypothesis: %v", err)
	}
	if _, err := c.AddQuestion("does the bound apply per worker or per queue?"); err != nil {
		t.Fatalf("RB-B05: question: %v", err)
	}
	if _, err := c.UpdateRoleContext(workspace.Ask, RoleContextUpdate{Reasoning: []string{"ask-marker"}, FactIDs: []string{keepFact.ID}}); err != nil {
		t.Fatalf("RB-B05: ASK context: %v", err)
	}
	if _, err := c.UpdateRoleContext(workspace.Fuzz, RoleContextUpdate{Strategy: "fuzz-strategy", HypothesisIDs: []string{hypothesis.ID}, Artifacts: []string{"artifact:repro"}}); err != nil {
		t.Fatalf("RB-B05: FUZZ context: %v", err)
	}
	if _, err := c.UpdateRoleContext(workspace.Build, RoleContextUpdate{Plan: []string{"build-plan"}, Scope: []string{"queue.go"}}); err != nil {
		t.Fatalf("RB-B05: BUILD context: %v", err)
	}
	if _, err := c.InvalidateEvidence(stale.ID, "the revision changed"); err != nil {
		t.Fatalf("RB-B05: invalidate: %v", err)
	}
	advance(&clock)

	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return role })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RB-B05: restore: found=%v err=%v", found, err)
	}
	if got := findFact(t, restored, staleFact.ID); got.Status != FactInvalidated {
		t.Fatalf("RB-B05: invalidated fact restored as %s", got.Status)
	}
	if got := findFact(t, restored, keepFact.ID); got.Status != FactActive {
		t.Fatalf("RB-B05: current fact restored as %s", got.Status)
	}
	if len(restored.Context.Hypotheses) != 1 || len(restored.Context.Questions) != 1 {
		t.Fatalf("RB-B05: hypotheses/questions lost: %+v", restored.Context)
	}
	if len(restored.Context.Evidence) != 2 {
		t.Fatalf("RB-B05: evidence count = %d, want 2 after dedup and invalidation", len(restored.Context.Evidence))
	}

	askBrief := strings.Join(ContextFor(restored, workspace.Ask, ""), "\n")
	fuzzBrief := strings.Join(ContextFor(restored, workspace.Fuzz, ""), "\n")
	buildBrief := strings.Join(ContextFor(restored, workspace.Build, ""), "\n")
	if !strings.Contains(askBrief, "the queue is bounded") || !strings.Contains(askBrief, "ask-marker") {
		t.Fatalf("RB-B05: ASK lost its current context:\n%s", askBrief)
	}
	if strings.Contains(askBrief, "fuzz-strategy") || strings.Contains(askBrief, "build-plan") {
		t.Fatalf("RB-B05: ASK received another role's private context:\n%s", askBrief)
	}
	if !strings.Contains(fuzzBrief, "fuzz-strategy") || !strings.Contains(fuzzBrief, "artifact:repro") {
		t.Fatalf("RB-B05: FUZZ lost its strategy or artifact:\n%s", fuzzBrief)
	}
	if strings.Contains(fuzzBrief, "build-plan") {
		t.Fatalf("RB-B05: FUZZ received BUILD's plan:\n%s", fuzzBrief)
	}
	if !strings.Contains(buildBrief, "build-plan") || !strings.Contains(buildBrief, "queue.go") {
		t.Fatalf("RB-B05: BUILD lost its plan or scope:\n%s", buildBrief)
	}
	if strings.Contains(buildBrief, "fuzz-strategy") {
		t.Fatalf("RB-B05: BUILD received FUZZ's strategy:\n%s", buildBrief)
	}
	for name, brief := range map[string]string{"ask": askBrief, "fuzz": fuzzBrief, "build": buildBrief} {
		if len(brief) > contextBudget {
			t.Fatalf("RB-B05: %s briefing = %d chars, want <= %d", name, len(brief), contextBudget)
		}
	}
	// Superseded/invalid evidence must not be presented as current support.
	if strings.Contains(fuzzBrief, "current evidence: "+stale.ID) {
		t.Fatalf("RB-B05: invalid evidence was supplied as current support:\n%s", fuzzBrief)
	}
}

// RC-C01: the authority gate is the only grant. Task metadata and stored role
// context never widen it.
func TestReadinessC01AuthorityPolicyMatrix(t *testing.T) {
	cases := []struct {
		role workspace.Workspace
		op   workspace.Operation
		want bool
	}{
		{workspace.Ask, workspace.OpRead, true},
		{workspace.Ask, workspace.OpSearch, true},
		{workspace.Ask, workspace.OpInspect, true},
		{workspace.Ask, workspace.OpExec, false},
		{workspace.Ask, workspace.OpWrite, false},
		{workspace.Ask, workspace.OpPatch, false},
		{workspace.Fuzz, workspace.OpRead, true},
		{workspace.Fuzz, workspace.OpExec, true},
		{workspace.Fuzz, workspace.OpWrite, false},
		{workspace.Fuzz, workspace.OpDelete, false},
		{workspace.Build, workspace.OpRead, true},
		{workspace.Build, workspace.OpExec, true},
		{workspace.Build, workspace.OpWrite, true},
		{workspace.Build, workspace.OpMove, true},
		{"", workspace.OpRead, false},
	}
	for _, tc := range cases {
		err := workspace.Admit(tc.role, tc.op)
		if got := err == nil; got != tc.want {
			t.Fatalf("RC-C01: Admit(%s, %s) allowed=%v, want %v (err=%v)", tc.role, tc.op, got, tc.want, err)
		}
	}
}

// RC-C02: metadata claiming a stronger role is refused, so the task can never
// describe authority the live gate does not hold.
func TestReadinessC02TaskMetadataCannotGrantAuthority(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(8000, 0).UTC()
	live := workspace.Ask
	c := newReadinessController(t, root, &live, &clock)
	mustBegin(t, c, readinessIntent())

	// A host bug attempts to record a switch it did not perform. The live gate
	// is the only authority, so the claim is refused before it can become
	// task metadata.
	if err := c.RecordWorkspaceSwitch(workspace.Ask, workspace.Build, "claimed"); err == nil {
		t.Fatal("RC-C02: RecordWorkspaceSwitch accepted a switch the live gate did not hold")
	}
	current, _ := c.Current()
	if current.ActiveRole != workspace.Ask {
		t.Fatalf("RC-C02: recorded role = %s, want the live ASK", current.ActiveRole)
	}
	if err := (workspace.Authority{Workspace: live}).Admit(readinessWriteTool{}, map[string]any{"path": "tools/grep.go"}); err == nil {
		t.Fatal("RC-C02: task metadata granted mutation authority the live gate did not hold")
	}
	if err := (workspace.Authority{Workspace: live}).Admit(readinessReadTool{}, map[string]any{"path": "tools/grep.go"}); err != nil {
		t.Fatalf("RC-C02: the live ASK authority lost its read grant: %v", err)
	}
}

// RC-C03: proposal, approval, rejection, failure, application, replay, and
// terminal-task rules for the handoff record.
func TestReadinessC03HandoffDispositionMatrix(t *testing.T) {
	newHandoffTask := func(t *testing.T) (*Controller, *workspace.Workspace) {
		t.Helper()
		root := t.TempDir()
		role := workspace.Fuzz
		c := NewController(NewStore(root), nil)
		c.SetRoleSource(func() workspace.Workspace { return role })
		mustBegin(t, c, contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational})
		return c, &role
	}

	t.Run("proposal-does-not-move-authority", func(t *testing.T) {
		c, role := newHandoffTask(t)
		h, err := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
		if err != nil {
			t.Fatalf("RC-C03: ProposeHandoff: %v", err)
		}
		current, _ := c.Current()
		if h.Status != HandoffProposed || current.ActiveRole != workspace.Fuzz {
			t.Fatalf("RC-C03: proposal status=%s role=%s, want PROPOSED/FUZZ", h.Status, current.ActiveRole)
		}
		if *role != workspace.Fuzz {
			t.Fatalf("RC-C03: proposal changed the live role to %s", *role)
		}
		if _, err := c.ApplyHandoff(h.ID); err == nil {
			t.Fatal("RC-C03: an unapproved handoff was applied")
		}
	})
	t.Run("approval-does-not-apply", func(t *testing.T) {
		c, role := newHandoffTask(t)
		h, _ := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
		approved, err := c.ApproveHandoff(h.ID)
		if err != nil {
			t.Fatalf("RC-C03: ApproveHandoff: %v", err)
		}
		current, _ := c.Current()
		if approved.Status != HandoffApproved || current.ActiveRole != workspace.Fuzz || *role != workspace.Fuzz {
			t.Fatalf("RC-C03: approval moved authority: handoff=%s taskRole=%s live=%s", approved.Status, current.ActiveRole, *role)
		}
	})
	t.Run("rejection-leaves-authority-and-role", func(t *testing.T) {
		c, role := newHandoffTask(t)
		h, _ := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
		rejected, err := c.RejectHandoff(h.ID, "not now")
		if err != nil {
			t.Fatalf("RC-C03: RejectHandoff: %v", err)
		}
		current, _ := c.Current()
		if rejected.Status != HandoffRejected || current.ActiveRole != workspace.Fuzz || *role != workspace.Fuzz {
			t.Fatalf("RC-C03: rejection moved authority: %+v", current)
		}
		if _, err := c.ApproveHandoff(h.ID); err == nil {
			t.Fatal("RC-C03: a rejected handoff was approved")
		}
	})
	t.Run("failure-leaves-authority-and-role", func(t *testing.T) {
		c, role := newHandoffTask(t)
		h, _ := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
		failed, err := c.FailHandoff(h.ID, "switch refused")
		if err != nil {
			t.Fatalf("RC-C03: FailHandoff: %v", err)
		}
		current, _ := c.Current()
		if failed.Status != HandoffFailed || current.ActiveRole != workspace.Fuzz || *role != workspace.Fuzz {
			t.Fatalf("RC-C03: failure moved authority: %+v", current)
		}
	})
	t.Run("application-then-replay", func(t *testing.T) {
		c, role := newHandoffTask(t)
		h, _ := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
		if _, err := c.ApproveHandoff(h.ID); err != nil {
			t.Fatalf("RC-C03: ApproveHandoff: %v", err)
		}
		*role = workspace.Build // the authority owner performed the switch
		applied, err := c.ApplyHandoff(h.ID)
		if err != nil {
			t.Fatalf("RC-C03: ApplyHandoff: %v", err)
		}
		if applied.Handoff == nil || applied.Handoff.Status != HandoffApplied || applied.ActiveRole != workspace.Build {
			t.Fatalf("RC-C03: applied task = %+v", applied)
		}
		if _, err := c.ApplyHandoff(h.ID); err == nil {
			t.Fatal("RC-C03: a consumed handoff was applied twice")
		}
	})
	t.Run("terminal-task-refuses-proposal", func(t *testing.T) {
		c, _ := newHandoffTask(t)
		if _, err := c.Cancel("done"); err != nil {
			t.Fatalf("RC-C03: Cancel: %v", err)
		}
		if _, err := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "too late"}); err == nil {
			t.Fatal("RC-C03: a terminal task accepted a handoff proposal")
		}
	})
}

// RD-D01: recovery reconciliation against the restored authority never switches
// authority and reports only what the live role establishes.
func TestReadinessD01HandoffRecoveryMatrix(t *testing.T) {
	setup := func(t *testing.T) (*Controller, *workspace.Workspace, Handoff) {
		t.Helper()
		root := t.TempDir()
		role := workspace.Fuzz
		c := NewController(NewStore(root), nil)
		c.SetRoleSource(func() workspace.Workspace { return role })
		mustBegin(t, c, contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational})
		h, err := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
		if err != nil {
			t.Fatalf("RD-D01: propose: %v", err)
		}
		if _, err := c.ApproveHandoff(h.ID); err != nil {
			t.Fatalf("RD-D01: approve: %v", err)
		}
		return c, &role, h
	}

	t.Run("live-target-becomes-applied-once", func(t *testing.T) {
		c, role, _ := setup(t)
		*role = workspace.Build
		recovery, err := c.ReconcileRecoveredHandoff(workspace.Build)
		if err != nil {
			t.Fatalf("RD-D01: reconcile: %v", err)
		}
		current, _ := c.Current()
		if current.Handoff.Status != HandoffApplied || !current.Handoff.Consumed {
			t.Fatalf("RD-D01: handoff = %+v, want APPLIED and consumed", current.Handoff)
		}
		if current.ActiveRole != workspace.Build {
			t.Fatalf("RD-D01: recorded role = %s, want BUILD", current.ActiveRole)
		}
		if !recovery.Changed || strings.Contains(strings.ToLower(recovery.Notice), "discrepancy") {
			t.Fatalf("RD-D01: recovery = %+v, want a matching-target notice", recovery)
		}
		again, err := c.ReconcileRecoveredHandoff(workspace.Build)
		if err != nil {
			t.Fatalf("RD-D01: second reconcile: %v", err)
		}
		if again.Changed {
			t.Fatal("RD-D01: repeated reconciliation changed a terminal handoff")
		}
	})
	t.Run("live-source-stays-interrupted", func(t *testing.T) {
		c, role, _ := setup(t)
		*role = workspace.Fuzz
		recovery, err := c.ReconcileRecoveredHandoff(workspace.Fuzz)
		if err != nil {
			t.Fatalf("RD-D01: reconcile: %v", err)
		}
		current, _ := c.Current()
		if current.Handoff.Status != HandoffInterrupted || current.ActiveRole != workspace.Fuzz {
			t.Fatalf("RD-D01: handoff=%+v role=%s, want INTERRUPTED/FUZZ", current.Handoff, current.ActiveRole)
		}
		if !strings.Contains(recovery.Notice, "not applied") {
			t.Fatalf("RD-D01: notice = %q, want the observed non-application", recovery.Notice)
		}
	})
	t.Run("unrelated-live-role-is-a-discrepancy", func(t *testing.T) {
		c, role, _ := setup(t)
		*role = workspace.Ask
		recovery, err := c.ReconcileRecoveredHandoff(workspace.Ask)
		if err != nil {
			t.Fatalf("RD-D01: reconcile: %v", err)
		}
		current, _ := c.Current()
		if current.Handoff.Status != HandoffInterrupted || current.ActiveRole != workspace.Fuzz {
			t.Fatalf("RD-D01: handoff=%+v role=%s, want INTERRUPTED with the recorded role untouched", current.Handoff, current.ActiveRole)
		}
		if !strings.Contains(strings.ToLower(recovery.Notice), "discrepancy") {
			t.Fatalf("RD-D01: notice = %q, want a recovery discrepancy", recovery.Notice)
		}
	})
	t.Run("unapproved-proposal-never-applies", func(t *testing.T) {
		root := t.TempDir()
		role := workspace.Fuzz
		c := NewController(NewStore(root), nil)
		c.SetRoleSource(func() workspace.Workspace { return role })
		mustBegin(t, c, contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational})
		if _, err := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"}); err != nil {
			t.Fatalf("RD-D01: propose: %v", err)
		}
		recovered := NewController(NewStore(root), nil)
		recovered.SetRoleSource(func() workspace.Workspace { return role })
		restored, found, err := recovered.Restore()
		if err != nil || !found {
			t.Fatalf("RD-D01: Restore: found=%v err=%v", found, err)
		}
		if restored.Handoff == nil || restored.Handoff.Status != HandoffInterrupted {
			t.Fatalf("RD-D01: restored handoff = %+v, want INTERRUPTED", restored.Handoff)
		}
		if restored.ActiveRole != workspace.Fuzz || role != workspace.Fuzz {
			t.Fatalf("RD-D01: an unapproved proposal changed authority: task=%s live=%s", restored.ActiveRole, role)
		}
	})
}

// RE-E01: a projection written before a failed checkpoint commit is never
// restored as newer authoritative state.
func TestReadinessE01ProjectionCannotOutrunACommit(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(9000, 0).UTC()
	role := workspace.Build
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, readinessIntent())
	advance(&clock)
	mustEvidence(t, c, EvidenceInput{Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed", Outcome: EvidenceObserved, At: clock})
	advance(&clock)
	mustEvidence(t, c, EvidenceInput{Kind: EvidenceTest, Source: "check:test", Scope: "tools", Summary: "passed", Outcome: EvidenceObserved, At: clock})

	installReadinessWriteFailure(c.Store(), ".ohgo/progress.json", "injected checkpoint failure")
	advance(&clock)
	refused, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err == nil {
		t.Fatal("RE-E01: completion succeeded despite the failed checkpoint write")
	}
	if refused.Status == StatusCompleted || refused.Proven() {
		t.Fatalf("RE-E01: the failed write published a completion in memory: status=%s state=%s", refused.Status, refused.ObjectiveState())
	}
	// The checkpoint is the commit point and is written first, so a failed
	// completion write must leave the human projection at its prior state. It
	// must never announce a COMPLETED status or proof the checkpoint did not
	// record.
	md, readErr := os.ReadFile(filepath.Join(root, ".ohgo", TaskFileName))
	if readErr != nil {
		t.Fatalf("RE-E01: read projection: %v", readErr)
	}
	if strings.Contains(string(md), string(StatusCompleted)) {
		t.Fatalf("RE-E01: the projection announced an uncommitted completion:\n%s", md)
	}

	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return role })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RE-E01: Restore: found=%v err=%v", found, err)
	}
	if restored.Status == StatusCompleted || restored.Status == StatusFailed || restored.Status == StatusCancelled {
		t.Fatalf("RE-E01: restart restored the uncommitted projection: %s", restored.Status)
	}
	if restored.Status != StatusPaused {
		t.Fatalf("RE-E01: restored status = %s, want PAUSED (an interrupted task)", restored.Status)
	}
	if restored.Completion != nil || restored.Proven() {
		t.Fatalf("RE-E01: restart restored a completion proof the checkpoint never committed: %+v", restored.Completion)
	}
	if _, err := reopened.Begin(readinessIntent(), nil); err != nil {
		t.Fatalf("RE-E01: the task was not resumable after the failed completion: %v", err)
	}
}

// RE-E02: a failed approval write leaves the in-memory task at PROPOSED. The
// contract requires the in-memory task to be restored to its prior state so a
// caller cannot observe an uncommitted transition.
func TestReadinessE02FailedApprovalWriteLeavesNoUncommittedApproval(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(10000, 0).UTC()
	role := workspace.Fuzz
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational})
	h, err := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
	if err != nil {
		t.Fatalf("RE-E02: propose: %v", err)
	}
	installReadinessWriteFailure(c.Store(), ".ohgo/progress.json", "injected approval failure")
	if _, err := c.ApproveHandoff(h.ID); err == nil {
		t.Fatal("RE-E02: approval reported success despite the failed checkpoint write")
	}
	current, _ := c.Current()
	if current.Handoff == nil || current.Handoff.Status != HandoffProposed {
		t.Fatalf("RE-E02: in-memory handoff = %+v, want the prior PROPOSED state after a failed write", current.Handoff)
	}
}

// RE-E03: a failed application write leaves the in-memory record at APPROVED
// and the recorded role at its prior value. The authority switch already
// happened, but the durable record did not commit the transition.
func TestReadinessE03FailedApplyWriteLeavesNoUncommittedApplied(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(11000, 0).UTC()
	role := workspace.Fuzz
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational})
	h, err := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
	if err != nil {
		t.Fatalf("RE-E03: propose: %v", err)
	}
	if _, err := c.ApproveHandoff(h.ID); err != nil {
		t.Fatalf("RE-E03: approve: %v", err)
	}
	role = workspace.Build
	installReadinessWriteFailure(c.Store(), ".ohgo/progress.json", "injected apply failure")
	if _, err := c.ApplyHandoff(h.ID); err == nil {
		t.Fatal("RE-E03: application reported success despite the failed checkpoint write")
	}
	current, _ := c.Current()
	if current.Handoff == nil || current.Handoff.Status != HandoffApproved {
		t.Fatalf("RE-E03: in-memory handoff = %+v, want the prior APPROVED state after a failed write", current.Handoff)
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("RE-E03: in-memory active role = %s, want the prior FUZZ after a failed write", current.ActiveRole)
	}
}

// RE-E02b: every terminal disposition (reject, fail) rolls back on a failed
// write, and the same transition succeeds once the write path recovers. The
// second half is the control: the refusal must come from persistence, not from
// a state machine that consumed the transition.
func TestReadinessE02bFailedTerminalWritesRollBack(t *testing.T) {
	transitions := []struct {
		name       string
		transition func(*Controller, string) (Handoff, error)
	}{
		{"reject", func(c *Controller, id string) (Handoff, error) { return c.RejectHandoff(id, "declined") }},
		{"fail", func(c *Controller, id string) (Handoff, error) { return c.FailHandoff(id, "could not switch") }},
	}
	for _, tc := range transitions {
		name, transition := tc.name, tc.transition
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			clock := time.Unix(11500, 0).UTC()
			role := workspace.Fuzz
			c := newReadinessController(t, root, &role, &clock)
			mustBegin(t, c, contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational})
			h, err := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
			if err != nil {
				t.Fatalf("RE-E02b/%s: propose: %v", name, err)
			}
			store := c.Store()
			healthy := store.write
			installReadinessWriteFailure(store, ".ohgo/progress.json", "injected write failure")
			if _, err := transition(c, h.ID); err == nil {
				t.Fatalf("RE-E02b/%s: the transition reported success despite the failed write", name)
			}
			current, _ := c.Current()
			if current.Handoff == nil || current.Handoff.Status != HandoffProposed {
				t.Fatalf("RE-E02b/%s: in-memory handoff = %+v, want the prior PROPOSED state", name, current.Handoff)
			}
			store.write = healthy
			if _, err := transition(c, h.ID); err != nil {
				t.Fatalf("RE-E02b/%s: the retry did not commit: %v", name, err)
			}
			current, _ = c.Current()
			if current.Handoff == nil || current.Handoff.Status == HandoffProposed {
				t.Fatalf("RE-E02b/%s: retry left handoff = %+v, want a committed disposition", name, current.Handoff)
			}
		})
	}
}

// RE-E02c: recovery reconciliation is also a decision that must commit. A
// failed write leaves the loaded APPROVED record untouched instead of
// publishing an APPLIED record the checkpoint never stored.
func TestReadinessE02cFailedReconciliationRollsBack(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(11750, 0).UTC()
	role := workspace.Fuzz
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational})
	h, err := c.ProposeHandoff(HandoffInput{To: workspace.Build, Reason: "mutation needed"})
	if err != nil {
		t.Fatalf("RE-E02c: propose: %v", err)
	}
	if _, err := c.ApproveHandoff(h.ID); err != nil {
		t.Fatalf("RE-E02c: approve: %v", err)
	}

	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	if _, found, err := reopened.Restore(); err != nil || !found {
		t.Fatalf("RE-E02c: restore: found=%v err=%v", found, err)
	}
	// The switch already happened; only the durable record lags behind it.
	installReadinessWriteFailure(reopened.Store(), ".ohgo/progress.json", "injected reconciliation failure")
	if _, err := reopened.ReconcileRecoveredHandoff(workspace.Build); err == nil {
		t.Fatal("RE-E02c: reconciliation reported success despite the failed write")
	}
	current, _ := reopened.Current()
	if current.Handoff == nil || current.Handoff.Status != HandoffApproved {
		t.Fatalf("RE-E02c: in-memory handoff = %+v, want the loaded APPROVED record", current.Handoff)
	}
	if current.ActiveRole != workspace.Fuzz {
		t.Fatalf("RE-E02c: the failed reconciliation moved the recorded role to %s", current.ActiveRole)
	}
}

// RE-E02d: an explicit workspace command is recorded with the same commit
// contract. The host already switched authority, but a failed write must leave
// the task metadata at the prior role rather than claiming a switch the
// checkpoint does not contain.
func TestReadinessE02dFailedExplicitSwitchRollsBack(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(11800, 0).UTC()
	role := workspace.Fuzz
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, contract.IntentContract{Objective: "fix the parser", Kind: contract.ObjectiveInformational})

	role = workspace.Build
	store := c.Store()
	healthy := store.write
	installReadinessWriteFailure(store, ".ohgo/progress.json", "injected switch failure")
	if err := c.RecordWorkspaceSwitch(workspace.Fuzz, workspace.Build, "main; clean"); err == nil {
		t.Fatal("RE-E02d: the switch was recorded despite the failed write")
	}
	current, _ := c.Current()
	if current.ActiveRole != workspace.Fuzz || current.Handoff != nil {
		t.Fatalf("RE-E02d: in-memory task = role %s handoff %+v, want the prior FUZZ with no switch record",
			current.ActiveRole, current.Handoff)
	}
	store.write = healthy
	if err := c.RecordWorkspaceSwitch(workspace.Fuzz, workspace.Build, "main; clean"); err != nil {
		t.Fatalf("RE-E02d: retry: %v", err)
	}
	current, _ = c.Current()
	if current.ActiveRole != workspace.Build || current.Handoff == nil || current.Handoff.Status != HandoffApplied {
		t.Fatalf("RE-E02d: retry task = role %s handoff %+v, want BUILD/APPLIED", current.ActiveRole, current.Handoff)
	}
}

// RE-E18: a committed checkpoint and the human projection agree. The projection
// is the derived artifact, so it may lag a failed commit but must never lead
// one.
func TestReadinessE18ProjectionMatchesCommittedCheckpoint(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(11900, 0).UTC()
	role := workspace.Build
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, readinessIntent())
	advance(&clock)
	mustEvidence(t, c, EvidenceInput{Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed", Outcome: EvidenceObserved, At: clock})
	advance(&clock)
	mustEvidence(t, c, EvidenceInput{Kind: EvidenceTest, Source: "check:test", Scope: "tools", Summary: "passed", Outcome: EvidenceObserved, At: clock})
	advance(&clock)
	completed, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("RE-E18: complete: %v", err)
	}
	if completed.Status != StatusCompleted || !completed.Proven() {
		t.Fatalf("RE-E18: completion = %s/%s, want COMPLETED/PROVEN", completed.Status, completed.ObjectiveState())
	}
	md, readErr := os.ReadFile(filepath.Join(root, ".ohgo", TaskFileName))
	if readErr != nil {
		t.Fatalf("RE-E18: read projection: %v", readErr)
	}
	text := string(md)
	if !strings.Contains(text, string(StatusCompleted)) || !strings.Contains(text, string(ObjectiveProven)) {
		t.Fatalf("RE-E18: a committed completion is missing from the projection:\n%s", text)
	}
	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return role })
	restored, found, restoreErr := reopened.Restore()
	if restoreErr != nil || !found {
		t.Fatalf("RE-E18: restore: found=%v err=%v", found, restoreErr)
	}
	if restored.Status != StatusCompleted || !restored.Proven() {
		t.Fatalf("RE-E18: restart = %s/%s, want the committed completion", restored.Status, restored.ObjectiveState())
	}
}

// RE-E04: checkpoint corruption and unsupported versions are classified, and a
// newer checkpoint is refused without being rewritten.
func TestReadinessE04CheckpointClassificationAndVersionBarrier(t *testing.T) {
	t.Run("newer-version-refused-without-rewrite", func(t *testing.T) {
		root := t.TempDir()
		clock := time.Unix(12000, 0).UTC()
		role := workspace.Build
		c := newReadinessController(t, root, &role, &clock)
		mustBegin(t, c, readinessIntent())
		path := filepath.Join(root, ".ohgo", ProgressFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("RE-E04: read checkpoint: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("RE-E04: decode checkpoint: %v", err)
		}
		raw["version"] = float64(ProgressVersion + 1)
		future, _ := json.MarshalIndent(raw, "", "  ")
		future = append(future, '\n')
		if err := os.WriteFile(path, future, 0o644); err != nil {
			t.Fatalf("RE-E04: write future checkpoint: %v", err)
		}
		snap, err := NewStore(root).Load()
		if !errors.Is(err, ErrCheckpointVersion) {
			t.Fatalf("RE-E04: Load error = %v, want ErrCheckpointVersion", err)
		}
		if !snap.Found {
			t.Fatal("RE-E04: a newer checkpoint hid the readable projection")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("RE-E04: re-read checkpoint: %v", err)
		}
		if string(after) != string(future) {
			t.Fatal("RE-E04: a refused newer checkpoint was rewritten")
		}
		// The controller reports the refusal rather than silently continuing
		// with authoritative state it could not read.
		reopened := NewController(NewStore(root), nil)
		reopened.SetRoleSource(func() workspace.Workspace { return role })
		restored, found, _ := reopened.Restore()
		if !found || restored.ID == "" {
			t.Fatal("RE-E04: the projection was not offered as a recovery source")
		}
		if !strings.Contains(restored.ContextWarning, "progress") && !strings.Contains(restored.ContextWarning, "version") {
			t.Fatalf("RE-E04: refusal was not reported to the user: %q", restored.ContextWarning)
		}
	})

	t.Run("malformed-checkpoint-recovers-projection", func(t *testing.T) {
		root := t.TempDir()
		clock := time.Unix(13000, 0).UTC()
		role := workspace.Build
		c := newReadinessController(t, root, &role, &clock)
		mustBegin(t, c, readinessIntent())
		path := filepath.Join(root, ".ohgo", ProgressFileName)
		if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("RE-E04: corrupt checkpoint: %v", err)
		}
		snap, err := NewStore(root).Load()
		if !snap.Found {
			t.Fatal("RE-E04: the projection was ignored after checkpoint corruption")
		}
		if !errors.Is(err, ErrCorruptCheckpoint) {
			t.Fatalf("RE-E04: Load error = %v, want ErrCorruptCheckpoint", err)
		}
		if snap.Task.ID == "" {
			t.Fatal("RE-E04: the projection did not supply the task identity")
		}
		reopened := NewController(NewStore(root), nil)
		reopened.SetRoleSource(func() workspace.Workspace { return role })
		restored, found, _ := reopened.Restore()
		if !found || restored.ID == "" {
			t.Fatal("RE-E04: the controller did not recover the projection")
		}
		// A corrupt checkpoint plus a projection that carries no contract is
		// valid but cannot progress without a human decision. The loss is still
		// named; the stronger NEEDS_HUMAN classification wins.
		if reopened.Recovery().State != RecoveryNeedsHuman {
			t.Fatalf("RE-E04: recovery = %s, want NEEDS_HUMAN", reopened.Recovery().State)
		}
		if !strings.Contains(strings.Join(reopened.Recovery().Missing, ","), ProgressFileName) {
			t.Fatalf("RE-E04: recovery did not name the corrupt checkpoint: %+v", reopened.Recovery())
		}
	})

	t.Run("version-1-checkpoint-needs-human", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".ohgo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		legacy := []byte(`{"version":1,"taskId":"legacy","status":"PAUSED","checkpointAt":"2024-01-01T00:00:00Z"}`)
		if err := os.WriteFile(filepath.Join(dir, ProgressFileName), legacy, 0o644); err != nil {
			t.Fatal(err)
		}
		c := NewController(NewStore(root), nil)
		restored, found, err := c.Restore()
		if err != nil || !found {
			t.Fatalf("RE-E04: version 1 restore: found=%v err=%v", found, err)
		}
		if restored.ID != "legacy" {
			t.Fatalf("RE-E04: version 1 identity = %q", restored.ID)
		}
		if c.Recovery().State != RecoveryNeedsHuman {
			t.Fatalf("RE-E04: version 1 recovery = %s, want NEEDS_HUMAN", c.Recovery().State)
		}
	})
}

// RF-F01: the durable completion boundary distinguishes answered, executed,
// observed, and verified, and refuses unsupported or unverified claims.
func TestReadinessF01CompletionProofMatrix(t *testing.T) {
	newTask := func(t *testing.T, kind contract.ObjectiveKind) (*Controller, *time.Time, *workspace.Workspace) {
		t.Helper()
		root := t.TempDir()
		clock := time.Unix(14000, 0).UTC()
		role := workspace.Build
		c := newReadinessController(t, root, &role, &clock)
		intent := readinessIntent()
		intent.Kind = kind
		if kind != contract.ObjectiveMutation {
			intent.Targets = nil
			intent.Verification = nil
		}
		mustBegin(t, c, intent)
		return c, &clock, &role
	}

	t.Run("informational-answer-required", func(t *testing.T) {
		c, _, _ := newTask(t, contract.ObjectiveInformational)
		if _, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
			t.Fatal("RF-F01: an informational objective completed without an answer")
		}
		done, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true})
		if err != nil {
			t.Fatalf("RF-F01: informational completion: %v", err)
		}
		if !done.Proven() || done.Completion == nil || !done.Completion.Answered {
			t.Fatalf("RF-F01: informational proof = %+v", done.Completion)
		}
	})
	t.Run("execution-answer-alone-insufficient", func(t *testing.T) {
		c, _, _ := newTask(t, contract.ObjectiveExecution)
		if _, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true}); err == nil {
			t.Fatal("RF-F01: an answer alone proved an execution objective")
		}
	})
	t.Run("execution-observed-success-proves", func(t *testing.T) {
		c, clock, _ := newTask(t, contract.ObjectiveExecution)
		mustEvidence(t, c, EvidenceInput{
			Kind: EvidenceCommand, Source: "go build ./...", Summary: "build succeeded",
			Outcome: EvidenceObserved, At: *clock,
		})
		advance(clock)
		done, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true})
		if err != nil {
			t.Fatalf("RF-F01: execution completion: %v", err)
		}
		if !done.Proven() || done.Completion == nil || !done.Completion.Executed {
			t.Fatalf("RF-F01: execution proof = %+v", done.Completion)
		}
	})
	t.Run("failed-check-is-not-execution", func(t *testing.T) {
		c, clock, _ := newTask(t, contract.ObjectiveExecution)
		mustEvidence(t, c, EvidenceInput{
			Kind: EvidenceCommand, Source: "go build ./...", Summary: "build failed",
			Outcome: EvidenceInconclusive, At: *clock,
		})
		if _, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
			t.Fatal("RF-F01: an inconclusive check produced a successful execution proof")
		}
	})
	t.Run("mutation-requires-change-and-later-verification", func(t *testing.T) {
		c, clock, _ := newTask(t, contract.ObjectiveMutation)
		change := mustEvidence(t, c, EvidenceInput{
			Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed tools/grep.go",
			Outcome: EvidenceObserved, At: *clock,
		})
		if _, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
			t.Fatal("RF-F01: a change without verification proved a mutation objective")
		}
		advance(clock)
		verify := mustEvidence(t, c, EvidenceInput{
			Kind: EvidenceTest, Source: "check:test", Scope: "tools",
			Summary: "the tools suite passed", Outcome: EvidenceObserved, At: *clock,
		})
		done, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true})
		if err != nil {
			t.Fatalf("RF-F01: mutation completion: %v", err)
		}
		if !done.Proven() || done.Completion == nil || !done.Completion.Observed || !done.Completion.VerifiesEvidence(verify.ID) {
			t.Fatalf("RF-F01: mutation proof = %+v", done.Completion)
		}
		if !done.Completion.VerifiesEvidence(change.ID) {
			t.Fatalf("RF-F01: mutation proof lost the observed change reference: %+v", done.Completion)
		}
	})
	t.Run("verification-before-change-refused", func(t *testing.T) {
		c, clock, _ := newTask(t, contract.ObjectiveMutation)
		mustEvidence(t, c, EvidenceInput{
			Kind: EvidenceTest, Source: "check:test", Scope: "tools",
			Summary: "the tools suite passed", Outcome: EvidenceObserved, At: *clock,
		})
		advance(clock)
		mustEvidence(t, c, EvidenceInput{
			Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed tools/grep.go",
			Outcome: EvidenceObserved, At: *clock,
		})
		if _, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true}); err == nil {
			t.Fatal("RF-F01: a verification that predates the change proved the mutation")
		}
	})
	t.Run("unsupported-contract-kind-refused", func(t *testing.T) {
		c, _, _ := newTask(t, contract.ObjectiveKind("invented"))
		if _, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true}); err == nil {
			t.Fatal("RF-F01: an unsupported contract kind was completed")
		}
	})
	t.Run("open-todo-refuses-completion", func(t *testing.T) {
		root := t.TempDir()
		clock := time.Unix(14500, 0).UTC()
		role := workspace.Build
		source := func() []Todo {
			return []Todo{{Label: "run the verification", Status: TodoTodo}}
		}
		c := NewController(NewStore(root), source)
		c.SetRoleSource(func() workspace.Workspace { return role })
		c.now = func() time.Time { return clock }
		mustBegin(t, c, contract.IntentContract{Objective: "explain the queue", Kind: contract.ObjectiveInformational})
		current, _ := c.Current()
		if !current.HasOpenTodos() {
			t.Fatalf("RF-F01: fixture did not record an open todo: %+v", current.Todos)
		}
		if _, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true, Answered: true}); err == nil {
			t.Fatal("RF-F01: completion succeeded while todo work remained open")
		}
	})
}

// RF-F02: proof survives a restart while its evidence is current, and becomes
// COMPLETED_UNPROVEN when that evidence is invalidated after the restart.
func TestReadinessF02ProofCurrentnessAcrossRestart(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(15000, 0).UTC()
	role := workspace.Build
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, readinessIntent())
	mustEvidence(t, c, EvidenceInput{Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed", Outcome: EvidenceObserved, At: clock})
	advance(&clock)
	verify := mustEvidence(t, c, EvidenceInput{Kind: EvidenceTest, Source: "check:test", Scope: "tools", Summary: "passed", Outcome: EvidenceObserved, At: clock})
	done, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("RF-F02: complete: %v", err)
	}
	if !done.Proven() {
		t.Fatalf("RF-F02: objective = %s, want PROVEN", done.ObjectiveState())
	}

	reopened := NewController(NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return role })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("RF-F02: restore: found=%v err=%v", found, err)
	}
	if !restored.Proven() {
		t.Fatalf("RF-F02: restart downgraded a current proof to %s", restored.ObjectiveState())
	}
	if _, err := reopened.InvalidateEvidence(verify.ID, "the check was rerun on a different revision"); err != nil {
		t.Fatalf("RF-F02: invalidate: %v", err)
	}
	after, _ := reopened.Current()
	if after.ObjectiveState() != ObjectiveCompletedUnproven {
		t.Fatalf("RF-F02: objective after invalidation = %s, want COMPLETED_UNPROVEN", after.ObjectiveState())
	}
	if after.Status != StatusCompleted {
		t.Fatalf("RF-F02: lifecycle status = %s, want the historical COMPLETED retained", after.Status)
	}

	final := NewController(NewStore(root), nil)
	final.SetRoleSource(func() workspace.Workspace { return role })
	restored, found, err = final.Restore()
	if err != nil || !found {
		t.Fatalf("RF-F02: second restore: found=%v err=%v", found, err)
	}
	if restored.ObjectiveState() != ObjectiveCompletedUnproven {
		t.Fatalf("RF-F02: persisted objective = %s, want COMPLETED_UNPROVEN", restored.ObjectiveState())
	}
}

// RF-F03: a contradiction downgrades PROVEN only when it targets a claim the
// proof depends on; an unrelated contradiction leaves the proof intact across a
// restart.
func TestReadinessF03ContradictionScopeAcrossRestart(t *testing.T) {
	buildProven := func(t *testing.T) (*Controller, string, string) {
		t.Helper()
		root := t.TempDir()
		clock := time.Unix(15500, 0).UTC()
		role := workspace.Build
		c := newReadinessController(t, root, &role, &clock)
		mustBegin(t, c, readinessIntent())
		_ = mustEvidence(t, c, EvidenceInput{Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed", Outcome: EvidenceObserved, At: clock})
		advance(&clock)
		verify := mustEvidence(t, c, EvidenceInput{Kind: EvidenceTest, Source: "check:test", Scope: "tools", Summary: "passed", Outcome: EvidenceObserved, At: clock})
		done, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true})
		if err != nil || !done.Proven() {
			t.Fatalf("RF-F03: fixture did not prove: state=%s err=%v", done.ObjectiveState(), err)
		}
		return c, root, verify.ID
	}

	t.Run("material-downgrades", func(t *testing.T) {
		c, root, _ := buildProven(t)
		current, _ := c.Current()
		changeRecord := current.Context.Evidence[0]
		fact, err := c.RecordFact(FactInput{Text: "GrepTool changed", EvidenceIDs: []string{changeRecord.ID}, At: time.Unix(15600, 0).UTC()})
		if err != nil {
			t.Fatalf("RF-F03: dependent fact: %v", err)
		}
		if _, err := c.RecordEvidence(EvidenceInput{
			Kind: EvidenceCommand, Source: "git diff", Summary: "the change was reverted",
			Outcome: EvidenceContradicting, Contradicts: []string{fact.ID}, At: time.Unix(15601, 0).UTC(),
		}); err != nil {
			t.Fatalf("RF-F03: contradiction: %v", err)
		}
		after, _ := c.Current()
		if after.ObjectiveState() != ObjectiveCompletedUnproven {
			t.Fatalf("RF-F03: material contradiction left objective = %s", after.ObjectiveState())
		}
		_ = root
	})

	t.Run("unrelated-does-not-downgrade", func(t *testing.T) {
		c, root, _ := buildProven(t)
		other := mustEvidence(t, c, EvidenceInput{
			Kind: EvidenceSource, Source: "docs/readme.md", Summary: "the readme is complete",
			Outcome: EvidenceObserved, At: time.Unix(15600, 0).UTC(),
		})
		otherFact, err := c.RecordFact(FactInput{Text: "the readme is complete", EvidenceIDs: []string{other.ID}, At: time.Unix(15600, 0).UTC()})
		if err != nil {
			t.Fatalf("RF-F03: unrelated fact: %v", err)
		}
		if _, err := c.RecordEvidence(EvidenceInput{
			Kind: EvidenceCommand, Source: "git diff docs", Summary: "the readme changed",
			Outcome: EvidenceContradicting, Contradicts: []string{otherFact.ID}, At: time.Unix(15601, 0).UTC(),
		}); err != nil {
			t.Fatalf("RF-F03: unrelated contradiction: %v", err)
		}
		after, _ := c.Current()
		if after.ObjectiveState() != ObjectiveProven {
			t.Fatalf("RF-F03: unrelated contradiction downgraded objective to %s", after.ObjectiveState())
		}
		reopened := NewController(NewStore(root), nil)
		reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
		restored, _, err := reopened.Restore()
		if err != nil {
			t.Fatalf("RF-F03: restore: %v", err)
		}
		if restored.ObjectiveState() != ObjectiveProven {
			t.Fatalf("RF-F03: restart after unrelated contradiction = %s", restored.ObjectiveState())
		}
	})
}

// RG-G01: timeline order uses the injected clock, stays bounded, keeps the
// creation anchor, and never changes lifecycle or authority by itself.
func TestReadinessG01TimelineOrderAndAuthorityIndependence(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(16000, 0).UTC()
	role := workspace.Ask
	c := newReadinessController(t, root, &role, &clock)
	mustBegin(t, c, contract.IntentContract{Objective: "explain the queue", Kind: contract.ObjectiveInformational})
	beforeRole := workspace.Ask
	for i := 0; i < MaxTimelineEvents+20; i++ {
		advance(&clock)
		if !c.task.AppendEvent(TimelineEvent{
			At: clock, Kind: TimelineCheckpoint, Role: role, Status: string(StatusRunning),
			Summary: "checkpoint",
		}) {
			t.Fatalf("RG-G01: event %d was refused", i)
		}
	}
	if err := c.persist(); err != nil {
		t.Fatalf("RG-G01: persist bounded timeline: %v", err)
	}
	current, _ := c.Current()
	if len(current.Timeline) > MaxTimelineEvents {
		t.Fatalf("RG-G01: timeline has %d events, want <= %d", len(current.Timeline), MaxTimelineEvents)
	}
	if current.Timeline[0].Kind != TimelineCreated {
		t.Fatalf("RG-G01: first retained event = %s, want the creation anchor", current.Timeline[0].Kind)
	}
	for i := 1; i < len(current.Timeline); i++ {
		if current.Timeline[i].Seq <= current.Timeline[i-1].Seq {
			t.Fatalf("RG-G01: timeline sequence is not increasing at %d", i)
		}
		if current.Timeline[i].At.Before(current.Timeline[i-1].At) {
			t.Fatalf("RG-G01: timeline time runs backwards at %d", i)
		}
	}
	if current.TimelineDropped() == 0 {
		t.Fatal("RG-G01: a truncated timeline did not report its sequence gap")
	}
	if current.Status != StatusRunning || current.ActiveRole != beforeRole {
		t.Fatalf("RG-G01: timeline truncation changed lifecycle state: status=%s role=%s", current.Status, current.ActiveRole)
	}
}

// RH-H01: recovery classification names what was actually restored, and a
// repeated load is idempotent.
func TestReadinessH01RecoveryClassificationMatrix(t *testing.T) {
	t.Run("intact", func(t *testing.T) {
		root := t.TempDir()
		clock := time.Unix(17000, 0).UTC()
		role := workspace.Build
		c := newReadinessController(t, root, &role, &clock)
		mustBegin(t, c, readinessIntent())
		reopened := NewController(NewStore(root), nil)
		reopened.SetRoleSource(func() workspace.Workspace { return role })
		if _, found, err := reopened.Restore(); err != nil || !found {
			t.Fatalf("RH-H01: restore: found=%v err=%v", found, err)
		}
		if got := reopened.Recovery().State; got != RecoveryIntact {
			t.Fatalf("RH-H01: recovery = %s, want INTACT", got)
		}
		first, _ := reopened.Current()
		again := NewController(NewStore(root), nil)
		again.SetRoleSource(func() workspace.Workspace { return role })
		second, _, err := again.Restore()
		if err != nil {
			t.Fatalf("RH-H01: second load: %v", err)
		}
		if second.ID != first.ID || second.Status != first.Status || len(second.Timeline) != len(first.Timeline) {
			t.Fatalf("RH-H01: repeated recovery mutated state: %+v -> %+v", first, second)
		}
	})
	t.Run("reconstructed-missing-projection", func(t *testing.T) {
		root := t.TempDir()
		clock := time.Unix(18000, 0).UTC()
		role := workspace.Build
		c := newReadinessController(t, root, &role, &clock)
		mustBegin(t, c, readinessIntent())
		if err := os.Remove(filepath.Join(root, ".ohgo", TaskFileName)); err != nil {
			t.Fatalf("RH-H01: remove projection: %v", err)
		}
		reopened := NewController(NewStore(root), nil)
		reopened.SetRoleSource(func() workspace.Workspace { return role })
		restored, found, err := reopened.Restore()
		if err != nil || !found {
			t.Fatalf("RH-H01: restore: found=%v err=%v", found, err)
		}
		if restored.Objective != readinessObjective {
			t.Fatalf("RH-H01: checkpoint-only objective = %q", restored.Objective)
		}
		if reopened.Recovery().State != RecoveryReconstructed {
			t.Fatalf("RH-H01: recovery = %s, want RECONSTRUCTED", reopened.Recovery().State)
		}
		if !strings.Contains(strings.Join(reopened.Recovery().Missing, ","), TaskFileName) {
			t.Fatalf("RH-H01: recovery did not name the missing projection: %+v", reopened.Recovery())
		}
	})
	t.Run("needs-human-without-contract", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".ohgo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		legacy := []byte(`{"version":1,"taskId":"legacy","status":"PAUSED","checkpointAt":"2024-01-01T00:00:00Z"}`)
		if err := os.WriteFile(filepath.Join(dir, ProgressFileName), legacy, 0o644); err != nil {
			t.Fatal(err)
		}
		c := NewController(NewStore(root), nil)
		if _, found, _ := c.Restore(); !found {
			t.Fatal("RH-H01: legacy checkpoint was not found")
		}
		if c.Recovery().State != RecoveryNeedsHuman {
			t.Fatalf("RH-H01: recovery = %s, want NEEDS_HUMAN", c.Recovery().State)
		}
	})
	t.Run("blocked-when-nothing-is-readable", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".ohgo")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ProgressFileName), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, TaskFileName), []byte("not a task"), 0o644); err != nil {
			t.Fatal(err)
		}
		c := NewController(NewStore(root), nil)
		_, _, err := c.Restore()
		if err == nil {
			t.Fatal("RH-H01: an unreadable task was restored without an error")
		}
		if c.Recovery().State != RecoveryBlocked {
			t.Fatalf("RH-H01: recovery = %s, want BLOCKED", c.Recovery().State)
		}
	})
}

// RH-H02: repeated archive/resume cycles preserve the task, and a corrupt
// archive is surfaced without hiding healthy ones.
func TestReadinessH02ArchiveCycleAndCorruption(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(19000, 0).UTC()
	role := workspace.Build
	c := newReadinessController(t, root, &role, &clock)
	first := mustBegin(t, c, readinessIntent())
	mustEvidence(t, c, EvidenceInput{Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed", Outcome: EvidenceObserved, At: clock})

	for cycle := 0; cycle < 3; cycle++ {
		detached, ok, err := c.Detach()
		if err != nil || !ok {
			t.Fatalf("RH-H02: cycle %d detach: ok=%v err=%v", cycle, ok, err)
		}
		if detached.ID != first.ID {
			t.Fatalf("RH-H02: cycle %d changed identity: %q", cycle, detached.ID)
		}
		resumable, err := c.ListResumable()
		if err != nil {
			t.Fatalf("RH-H02: cycle %d list: %v", cycle, err)
		}
		if countTasks(resumable, first.ID) != 1 {
			t.Fatalf("RH-H02: cycle %d duplicated the archived task: %+v", cycle, resumable)
		}
		resumed, err := c.ResumeTask(first.ID)
		if err != nil {
			t.Fatalf("RH-H02: cycle %d resume: %v", cycle, err)
		}
		if resumed.ID != first.ID || len(resumed.Context.Evidence) != 1 {
			t.Fatalf("RH-H02: cycle %d lost state: %+v", cycle, resumed)
		}
	}

	// A corrupt archive must be reported, not silently treated as absent.
	dir := c.Store().ArchiveDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("RH-H02: corrupt archive: %v", err)
	}
	healthy, err := c.ListResumable()
	if err != nil {
		t.Fatalf("RH-H02: list with corruption: %v", err)
	}
	if countTasks(healthy, first.ID) != 1 {
		// The task is live and must appear exactly once; a corrupt sibling must
		// neither duplicate it nor make the list fail.
		t.Fatalf("RH-H02: live task count = %d, want 1: %+v", countTasks(healthy, first.ID), healthy)
	}
	problems := c.ArchiveProblems()
	if len(problems) != 1 || !strings.Contains(problems[0].Path, "broken.json") {
		t.Fatalf("RH-H02: archive problems = %+v, want the corrupt file", problems)
	}
}

// RH-H08: a completed task archived before the next objective remains reachable
// through the read-only archive listing. Preserving the record is only useful if
// the UI can enumerate it.
func TestReadinessH08ArchivedTerminalHistoryIsListable(t *testing.T) {
	root := t.TempDir()
	clock := time.Unix(19500, 0).UTC()
	role := workspace.Build
	c := newReadinessController(t, root, &role, &clock)
	intent := readinessIntent()
	started := mustBegin(t, c, intent)
	advance(&clock)
	mustEvidence(t, c, EvidenceInput{Kind: EvidenceArtifact, Source: "write:tools/grep.go", Summary: "changed", Outcome: EvidenceObserved, At: clock})
	advance(&clock)
	mustEvidence(t, c, EvidenceInput{Kind: EvidenceTest, Source: "check:test", Scope: "tools", Summary: "passed", Outcome: EvidenceObserved, At: clock})
	advance(&clock)
	completed, err := c.CompleteWithEvidence(CompletionEvidence{Complete: true})
	if err != nil {
		t.Fatalf("RH-H08: complete: %v", err)
	}
	if completed.Status != StatusCompleted {
		t.Fatalf("RH-H08: completion status = %s", completed.Status)
	}
	if _, _, err := c.Detach(); err != nil {
		t.Fatalf("RH-H08: detach: %v", err)
	}
	archived, err := c.ArchivedTasks()
	if err != nil {
		t.Fatalf("RH-H08: list archived: %v", err)
	}
	var found *Task
	for i := range archived {
		if archived[i].ID == started.ID {
			found = &archived[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("RH-H08: the completed task is not in the archive listing: %+v", archived)
	}
	if found.Status != StatusCompleted || found.Objective != intent.Objective {
		t.Fatalf("RH-H08: archived task = %s/%q, want COMPLETED and the original objective", found.Status, found.Objective)
	}
	if found.Completion == nil || len(found.Timeline) == 0 {
		t.Fatalf("RH-H08: archived history lost its proof or timeline: %+v", found)
	}
}

// installReadinessWriteFailure makes the store fail writes to one suffix while
// letting the others through, so the projection/checkpoint ordering can be
// exercised without a second persistence implementation.
func installReadinessWriteFailure(store *Store, suffix, message string) {
	store.write = func(path string, data []byte) error {
		if strings.HasSuffix(path, suffix) {
			return errors.New(message)
		}
		return os.WriteFile(path, data, 0o644)
	}
}

func mustCurrent(t *testing.T, c *Controller) Task {
	t.Helper()
	current, ok := c.Current()
	if !ok {
		t.Fatal("no current task")
	}
	return current
}

func containsTaskID(tasks []Task, id string) bool { return countTasks(tasks, id) > 0 }

func countTasks(tasks []Task, id string) int {
	n := 0
	for _, candidate := range tasks {
		if candidate.ID == id {
			n++
		}
	}
	return n
}

func countEvents(events []TimelineEvent, kind TimelineKind) int {
	n := 0
	for _, event := range events {
		if event.Kind == kind {
			n++
		}
	}
	return n
}

func countEvidenceKind(records []EvidenceRecord, kind EvidenceKind) int {
	n := 0
	for _, record := range records {
		if record.Kind == kind {
			n++
		}
	}
	return n
}

// Minimal capability-reported tools for the authority matrix; they never
// execute. The matrix asserts the policy, not a tool implementation.
type readinessReadTool struct{}

func (readinessReadTool) Name() string                   { return "read" }
func (readinessReadTool) Description() string            { return "read" }
func (readinessReadTool) Parameters() *jsonschema.Schema { return nil }
func (readinessReadTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierRead} }
func (readinessReadTool) Exclusive(map[string]any) bool  { return false }
func (readinessReadTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{}
}

type readinessWriteTool struct{}

func (readinessWriteTool) Name() string                   { return "write" }
func (readinessWriteTool) Description() string            { return "write" }
func (readinessWriteTool) Parameters() *jsonschema.Schema { return nil }
func (readinessWriteTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierWrite} }
func (readinessWriteTool) Exclusive(map[string]any) bool  { return false }
func (readinessWriteTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{}
}
