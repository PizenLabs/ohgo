package task

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// TestCompactedTaskTruthSurvivesRestartWithoutTranscript is the restart
// contract: a task that was compacted in one process restores its objective,
// authority, completion state, facts, hypotheses, evidence categories,
// questions, and next action from the checkpoint alone.
//
// No transcript is replayed. The restored context blocks must be byte-equal to
// the blocks that existed before the restart.
func TestCompactedTaskTruthSurvivesRestartWithoutTranscript(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)

	task, err := New("fix the sequence collision in internal/store")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	task.Constraints = []string{"do not change the public API", "keep the allocator deterministic"}
	task.Contract = &contract.IntentContract{
		Objective: "fix the sequence collision in internal/store",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "internal/store/store.go"}},
		MutationScope: contract.MutationScope{
			Kind: contract.MutationScopePaths, Files: []string{"internal/store/store.go"},
		},
		Verification: []contract.VerificationRequirement{{
			Kind: contract.VerifyScope, Scope: "./internal/store", Check: contract.CheckTest,
		}},
	}
	task.Phase = "verify"
	task.NextAction = "run go test ./internal/store"

	source, err := task.RecordEvidence(EvidenceInput{
		Kind: EvidenceSource, Role: workspace.Build, Source: "read internal/store/store.go",
		Summary: "the allocator resets seq to zero on every open", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("record source evidence: %v", err)
	}
	hypothesis, err := task.RecordHypothesis(HypothesisInput{
		Text: "SQLite is causing the sequence collision", Role: workspace.Build,
	})
	if err != nil {
		t.Fatalf("record hypothesis: %v", err)
	}
	if _, _, err := task.ConfirmHypothesis(hypothesis.ID, []string{source.ID}, workspace.Build, time.Now()); err != nil {
		t.Fatalf("confirm hypothesis: %v", err)
	}
	contradiction, err := task.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Role: workspace.Build, Source: "go test ./internal/store -run TestAllocator",
		Summary: "FAIL TestAllocator: duplicate sequence 7", Outcome: EvidenceContradicting,
		Contradicts: []string{hypothesis.ID},
	})
	if err != nil {
		t.Fatalf("record contradiction: %v", err)
	}
	if _, err := task.RecordHypothesis(HypothesisInput{
		Text: "the allocator reset, not SQLite, causes the duplicate", Role: workspace.Build,
	}); err != nil {
		t.Fatalf("record correction hypothesis: %v", err)
	}
	failure, err := task.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Role: workspace.Build, Source: "go test ./internal/store",
		Scope: "./internal/store", Summary: "FAIL: TestAllocator duplicate sequence",
		Outcome: EvidenceInconclusive,
	})
	if err != nil {
		t.Fatalf("record failed verification: %v", err)
	}
	success, err := task.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Role: workspace.Build, Source: "go test ./internal/store",
		Scope: "./internal/store", Summary: "PASS: TestAllocator",
		Outcome: EvidenceSupporting,
	})
	if err != nil {
		t.Fatalf("record successful verification: %v", err)
	}
	mutation, err := task.RecordEvidence(EvidenceInput{
		Kind: EvidenceTool, Role: workspace.Build, Source: "edit internal/store/store.go",
		Summary: "changed the allocator to derive seq from the persisted maximum", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("record mutation evidence: %v", err)
	}
	question, err := task.AddQuestion("does the allocator reset on reopen?", workspace.Build, time.Now())
	if err != nil {
		t.Fatalf("add question: %v", err)
	}
	task.Status = StatusNeedsReview
	task.ReviewReason = "the first verification failed; the correction was applied and verified"

	// A realistic compacted conversation: exploration, reads, searches, edits,
	// failed and successful checks, all paired so the rebuilt transcript is
	// structurally valid.
	var messages []agent.Message
	for i := range 80 {
		id := fmt.Sprintf("c%d", i)
		messages = append(messages, agent.UserMessage{Text: fmt.Sprintf("explore step %d", i)})
		messages = append(messages, agent.AssistantMessage{Message: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.ToolCall{
				ID: id, Name: "read", Arguments: map[string]any{"path": "internal/store/store.go"},
			}},
		}})
		messages = append(messages, agent.ToolResultMessage{
			ToolCallID: id, ToolName: "read",
			Content: ai.TextBlocks(strings.Repeat("source line\n", 80)),
		})
	}
	// The recovery classification is diagnostic runtime state, not task truth.
	// The restored process always reports INTACT after a clean load, so pin the
	// pre-restart value to make the block comparison meaningful.
	task.Recovery = RecoveryReport{State: RecoveryIntact}
	blocksBefore := ContextFor(task, workspace.Build, "turn 80")
	projection := agent.ContextProjection{
		Authority:  "BUILD · mutation permitted · execution permitted",
		Objective:  task.Objective,
		Completion: string(task.ObjectiveState()),
		Blocks:     blocksBefore,
	}
	compacted, err := agent.CompactWith(context.Background(), messages, agent.CompactOptions{
		Compaction: agent.Compaction{Enabled: true, Strategy: agent.StrategySummarize, KeepRecent: 4},
		Window:     4000,
		Projection: func() (agent.ContextProjection, bool) { return projection, true },
	})
	if err != nil {
		t.Fatalf("CompactWith: %v", err)
	}
	if compacted.AfterTokens >= compacted.BeforeTokens {
		t.Fatalf("compaction did not reduce: %d -> %d", compacted.BeforeTokens, compacted.AfterTokens)
	}
	if err := agent.ValidateCompactedMessages(compacted.Messages); err != nil {
		t.Fatalf("compacted transcript is invalid: %v", err)
	}

	if err := store.Save(task); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	// Restart: a new store and controller over the same root, with no
	// transcript and no in-memory recall archive.
	restartedStore := NewStore(root)
	restarted := NewController(restartedStore, nil)
	restored, ok, err := restarted.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !ok {
		t.Fatal("the compacted task was not restored")
	}

	if restored.ID != task.ID {
		t.Fatalf("task identity = %q, want %q", restored.ID, task.ID)
	}
	if restored.Objective != task.Objective {
		t.Fatalf("objective = %q, want %q", restored.Objective, task.Objective)
	}
	if restored.Status != task.Status || restored.ObjectiveState() != task.ObjectiveState() {
		t.Fatalf("completion = %s/%s, want %s/%s", restored.Status, restored.ObjectiveState(), task.Status, task.ObjectiveState())
	}
	if strings.Join(restored.Constraints, "|") != strings.Join(task.Constraints, "|") {
		t.Fatalf("constraints = %v, want %v", restored.Constraints, task.Constraints)
	}
	if restored.NextAction != task.NextAction {
		t.Fatalf("next action = %q, want %q", restored.NextAction, task.NextAction)
	}
	if restored.Contract == nil || restored.Contract.Objective != task.Contract.Objective {
		t.Fatal("the persisted contract did not restore")
	}

	if len(restored.Context.Facts) != len(task.Context.Facts) ||
		len(restored.Context.Hypotheses) != len(task.Context.Hypotheses) ||
		len(restored.Context.Evidence) != len(task.Context.Evidence) ||
		len(restored.Context.Questions) != len(task.Context.Questions) {
		t.Fatalf("semantic categories changed across restart: %+v -> %+v",
			task.Context, restored.Context)
	}
	if len(restored.Context.Facts) != 1 || len(restored.Context.Hypotheses) != 2 ||
		len(restored.Context.Questions) != 1 {
		t.Fatalf("semantic categories lost: %+v", restored.Context)
	}
	if !hasEvidence(restored, failure.ID, EvidenceInconclusive, "FAIL: TestAllocator") {
		t.Fatal("the failed verification did not restore as failed")
	}
	if !hasEvidence(restored, success.ID, EvidenceSupporting, "PASS: TestAllocator") {
		t.Fatal("the successful verification did not restore as successful")
	}
	if !hasEvidence(restored, contradiction.ID, EvidenceContradicting, "duplicate sequence 7") {
		t.Fatal("the contradicting observation did not restore as contradicting")
	}
	if !hasEvidence(restored, mutation.ID, EvidenceObserved, "derive seq from the persisted maximum") {
		t.Fatal("the changed-file observation did not restore")
	}
	if len(restored.Context.Questions) != 1 || restored.Context.Questions[0].ID != question.ID ||
		restored.Context.Questions[0].Status != QuestionOpen {
		t.Fatalf("open question = %+v, want the recorded open question", restored.Context.Questions)
	}

	blocksAfter := ContextFor(restored, workspace.Build, "turn 80")
	if strings.Join(blocksAfter, "\n") != strings.Join(blocksBefore, "\n") {
		t.Fatalf("restored context differs from the pre-restart context:\n--- before ---\n%s\n--- after ---\n%s",
			strings.Join(blocksBefore, "\n"), strings.Join(blocksAfter, "\n"))
	}
}

func hasEvidence(task Task, id string, outcome EvidenceOutcome, summaryFragment string) bool {
	for _, record := range task.Context.Evidence {
		if record.ID != id {
			continue
		}
		return record.Outcome == outcome && strings.Contains(record.Summary, summaryFragment) &&
			record.Validity == EvidenceCurrent
	}
	return false
}
