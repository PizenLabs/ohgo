package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/task"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// The turn entrypoint is the real continuity path: a role change on the same
// objective must not detach the task, and a genuinely new objective still must.
func TestRoleChangeKeepsTaskIdentityAndContext(t *testing.T) {
	root := t.TempDir()
	role := workspace.Build
	controller := task.NewController(task.NewStore(root), nil)
	controller.SetRoleSource(func() workspace.Workspace { return role })

	var (
		mu       sync.Mutex
		requests int
	)
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		mu.Lock()
		requests++
		request := requests
		mu.Unlock()
		switch request {
		case 1:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.ToolCall{
					ID: "c1", Name: "write", Arguments: map[string]any{"path": "note.txt"},
				}},
				StopReason: ai.StopToolUse,
			}), nil
		case 2:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "wrote note.txt"}},
				StopReason: ai.StopEndTurn,
			}), nil
		default:
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content:    []ai.ContentBlock{ai.TextContent{Text: "done"}},
				StopReason: ai.StopEndTurn,
			}), nil
		}
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools:  runtimeCaps(&runtimeWriteTool{path: "note.txt"}),
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	program := NewProgram(loop, "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{Current: func() string { return string(role) }})
	program.SetTaskHooks(runtimeHooks(controller))

	program.runTurn("fix the flaky test in note.txt")
	first, ok := controller.Current()
	if !ok {
		t.Fatal("the first turn recorded no task")
	}
	if first.Status.IsTerminal() {
		t.Fatalf("the first turn completed the task unexpectedly: %s", first.Status)
	}
	if len(first.Context.Evidence) == 0 {
		t.Fatal("the runtime observation was not projected into task evidence")
	}
	build, ok := first.Context.Role(workspace.Build)
	if !ok || len(build.Progress) == 0 {
		t.Fatalf("BUILD context was not populated by the turn: %+v", build)
	}

	// A host/control-plane fact, recorded from the observation the run made.
	var evidenceID string
	for _, record := range first.Context.Evidence {
		if record.Kind == task.EvidenceArtifact {
			evidenceID = record.ID
			break
		}
	}
	if evidenceID == "" {
		t.Fatalf("no mutation evidence was recorded: %+v", first.Context.Evidence)
	}
	if _, err := controller.RecordFact(task.FactInput{
		Text: "note.txt was changed", EvidenceIDs: []string{evidenceID},
	}); err != nil {
		t.Fatalf("RecordFact: %v", err)
	}
	if _, err := controller.UpdateRoleContext(workspace.Build, task.RoleContextUpdate{
		Plan: []string{"build-plan-marker"},
	}); err != nil {
		t.Fatalf("BUILD plan: %v", err)
	}

	// The user switches authority and continues the same objective. The
	// prompt is not in the continuation phrase list; only the role changed.
	role = workspace.Fuzz
	program.runTurn("fix the flaky test in note.txt")
	second, ok := controller.Current()
	if !ok {
		t.Fatal("no task after the role change")
	}
	if second.ID != first.ID {
		t.Fatalf("a role change recreated the task: %q -> %q", first.ID, second.ID)
	}
	if !taskHasFact(second, "note.txt was changed") {
		t.Fatal("the shared fact did not survive the role change")
	}
	buildAfter, ok := second.Context.Role(workspace.Build)
	if !ok || !taskContains(buildAfter.Plan, "build-plan-marker") {
		t.Fatalf("BUILD role context did not survive the role change: %+v", buildAfter)
	}
	if _, ok := second.Context.Role(workspace.Fuzz); !ok {
		t.Fatal("the FUZZ role context was not created on the role change")
	}
	fuzzBrief := strings.Join(task.ContextFor(second, workspace.Fuzz, ""), "\n")
	if strings.Contains(fuzzBrief, "build-plan-marker") {
		t.Fatalf("FUZZ received BUILD's private plan:\n%s", fuzzBrief)
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("the role change granted FUZZ mutation authority")
	}

	// A genuinely new objective still starts a new task and preserves the old
	// one as resumable.
	program.runTurn("summarize the README")
	third, ok := controller.Current()
	if !ok {
		t.Fatal("no task after the new objective")
	}
	if third.ID == first.ID {
		t.Fatal("a new objective reused the old task")
	}
	resumable, err := controller.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	found := false
	for _, candidate := range resumable {
		if candidate.ID == first.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("the old task was not preserved as resumable")
	}
}

type runtimeWriteTool struct{ path string }

func (t *runtimeWriteTool) Name() string        { return "write" }
func (t *runtimeWriteTool) Description() string { return "write a file" }
func (t *runtimeWriteTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"}}}`))
}
func (t *runtimeWriteTool) Approval() agent.Approval {
	return agent.Approval{Tier: agent.TierWrite, Reason: "writes a file", Policy: agent.PolicyAllow}
}
func (t *runtimeWriteTool) Exclusive(map[string]any) bool { return false }
func (t *runtimeWriteTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{
		Text: "wrote " + t.path, Mutates: true,
		Details: map[string]any{"path": t.path, "bytes": 5},
	}
}

func runtimeProjection(recorded task.Task, err error) TaskProjection {
	projection := TaskProjection{
		ID:             recorded.ID,
		Status:         string(recorded.Status),
		Objective:      recorded.Objective,
		ObjectiveState: string(recorded.ObjectiveState()),
		Warning:        recorded.ContextWarning,
	}
	if !recorded.Recovery.Empty() && recorded.Recovery.State != task.RecoveryIntact {
		projection.Recovery = recorded.Recovery.Label()
	}
	for _, item := range task.Items(recorded) {
		projection.Items = append(projection.Items, TaskItem{
			State: string(item.State),
			Label: item.Label,
		})
	}
	if err != nil {
		projection.Err = err.Error()
	}
	return projection
}

func taskHasFact(recorded task.Task, text string) bool {
	for _, fact := range recorded.Context.Facts {
		if fact.Text == text {
			return true
		}
	}
	return false
}

func taskContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// The complete Phase 1 scenario through the real turn entrypoint: ASK reads,
// FUZZ investigates, BUILD implements and verifies, FUZZ and BUILD resume, the
// application restarts, and the same task is restored with every context.
func TestPhase1ContinuityScenarioThroughRuntime(t *testing.T) {
	root := t.TempDir()
	role := workspace.Ask
	controller := task.NewController(task.NewStore(root), func() []task.Todo {
		return []task.Todo{{Label: "verify the flaky test", Status: task.TodoActive}}
	})
	controller.SetRoleSource(func() workspace.Workspace { return role })

	objective := "fix the flaky test in note.txt"
	script := []ai.AssistantMessage{
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "a1", Name: "read", Arguments: map[string]any{"path": "note.txt"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "understood the failure"}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "f1", Name: "bash", Arguments: map[string]any{"command": "go test ./... --repro"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "reproduced the failure"}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "b1", Name: "write", Arguments: map[string]any{"path": "note.txt"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "implemented the change"}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "f2", Name: "bash", Arguments: map[string]any{"command": "inspect the latest note.txt"}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "inspected the implementation"}}, StopReason: ai.StopEndTurn},
		{Content: []ai.ContentBlock{ai.ToolCall{ID: "b2", Name: "bash", Arguments: map[string]any{"command": "go test ./..."}}}, StopReason: ai.StopToolUse},
		{Content: []ai.ContentBlock{ai.TextContent{Text: "verified the change"}}, StopReason: ai.StopEndTurn},
	}
	var (
		mu       sync.Mutex
		requests int
	)
	stream := func(context.Context, ai.Request) (*ai.Stream, error) {
		mu.Lock()
		defer mu.Unlock()
		if requests >= len(script) {
			return ai.NewCompletedStream(ai.AssistantMessage{
				Content: []ai.ContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopEndTurn,
			}), nil
		}
		message := script[requests]
		requests++
		return ai.NewCompletedStream(message), nil
	}
	loop, err := agent.New(agent.Config{
		Model:  ai.Model{ID: "m", Provider: "p"},
		Stream: stream,
		Tools: []agent.Tool{
			&runtimeReadTool{path: "note.txt"},
			&runtimeWriteTool{path: "note.txt"},
			&runtimeBashTool{},
		},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	program := NewProgram(loop, "test", 0)
	program.SetWorkspaceSwitcher(WorkspaceHooks{Current: func() string { return string(role) }})
	program.SetTaskHooks(runtimeHooks(controller))

	// 1. ASK establishes understanding from a read-only observation.
	program.runTurn(objective)
	askTask, ok := controller.Current()
	if !ok || askTask.Status.IsTerminal() {
		t.Fatalf("ASK task = %+v, want a non-terminal task", askTask)
	}
	firstID := askTask.ID
	askEvidence := evidenceFor(askTask, "cognition:file:note.txt")
	if askEvidence == nil || askEvidence.Kind != task.EvidenceSource {
		t.Fatalf("ASK read evidence = %+v", askEvidence)
	}
	askCtx, ok := askTask.Context.Role(workspace.Ask)
	if !ok || !taskContainsSubstring(askCtx.Progress, "inspected") {
		t.Fatalf("ASK context = %+v, want read progress", askCtx)
	}
	if _, err := controller.AddQuestion("is the failure reproducible on a clean cache?"); err != nil {
		t.Fatalf("AddQuestion: %v", err)
	}

	// 2. FUZZ investigates, records a reproduction, and resolves a hypothesis.
	role = workspace.Fuzz
	program.runTurn(objective)
	fuzzTask, ok := controller.Current()
	if !ok || fuzzTask.ID != firstID {
		t.Fatalf("FUZZ replaced the task: %+v", fuzzTask)
	}
	if evidenceFor(fuzzTask, "cognition:command:"+commandDigest("go test ./... --repro")) == nil {
		t.Fatalf("FUZZ command evidence was not recorded: %+v", fuzzTask.Context.Evidence)
	}
	fuzzCtx, ok := fuzzTask.Context.Role(workspace.Fuzz)
	if !ok || !taskContainsSubstring(fuzzCtx.Progress, "go test ./... --repro") {
		t.Fatalf("FUZZ context = %+v, want investigation progress", fuzzCtx)
	}
	reproduction, err := controller.RecordEvidence(task.EvidenceInput{
		Kind: task.EvidenceReproduction, Role: workspace.Fuzz,
		Source: "go test ./... --repro", Artifact: "artifact:repro",
		Summary: "the failure reproduces twice", Outcome: task.EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("reproduction evidence: %v", err)
	}
	hypothesis, err := controller.RecordHypothesis(task.HypothesisInput{
		Text: "the cache is stale", Role: workspace.Fuzz,
	})
	if err != nil {
		t.Fatalf("RecordHypothesis: %v", err)
	}
	if _, _, err := controller.ConfirmHypothesis(hypothesis.ID, []string{reproduction.ID}, workspace.Fuzz); err != nil {
		t.Fatalf("ConfirmHypothesis: %v", err)
	}

	// 3. BUILD consumes the shared context, implements, and records progress.
	role = workspace.Build
	program.runTurn(objective)
	buildTask, ok := controller.Current()
	if !ok || buildTask.ID != firstID {
		t.Fatalf("BUILD replaced the task: %+v", buildTask)
	}
	if evidenceFor(buildTask, "cognition:mutation:note.txt") == nil {
		t.Fatalf("BUILD mutation evidence was not recorded: %+v", buildTask.Context.Evidence)
	}
	buildCtx, ok := buildTask.Context.Role(workspace.Build)
	if !ok || !taskContains(buildCtx.Scope, "note.txt") || len(buildCtx.Progress) == 0 {
		t.Fatalf("BUILD context = %+v, want implementation progress and scope", buildCtx)
	}
	if !taskContains(buildCtx.Plan, "verify the flaky test") {
		t.Fatalf("BUILD plan did not include the open todo: %+v", buildCtx)
	}
	if _, err := controller.UpdateRoleContext(workspace.Build, task.RoleContextUpdate{
		Plan: []string{"build-plan-marker"},
	}); err != nil {
		t.Fatalf("BUILD plan marker: %v", err)
	}

	// 4. FUZZ resumes against the latest implementation without mutation.
	role = workspace.Fuzz
	program.runTurn(objective)
	fuzzAgain, ok := controller.Current()
	if !ok || fuzzAgain.ID != firstID {
		t.Fatalf("FUZZ resume replaced the task: %+v", fuzzAgain)
	}
	if evidenceFor(fuzzAgain, "cognition:command:"+commandDigest("inspect the latest note.txt")) == nil {
		t.Fatal("the FUZZ resume observation was not recorded")
	}
	fuzzBrief := strings.Join(task.ContextFor(fuzzAgain, workspace.Fuzz, ""), "\n")
	if strings.Contains(fuzzBrief, "build-plan-marker") {
		t.Fatalf("FUZZ received BUILD's private plan:\n%s", fuzzBrief)
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("FUZZ acquired source mutation authority")
	}

	// 5. BUILD resumes with its own context and the new shared findings.
	role = workspace.Build
	program.runTurn(objective)
	buildAgain, ok := controller.Current()
	if !ok || buildAgain.ID != firstID {
		t.Fatalf("BUILD resume replaced the task: %+v", buildAgain)
	}
	if !hasVerificationFact(buildAgain) {
		t.Fatalf("BUILD verification was not recorded as a fact: %+v", buildAgain.Context.Facts)
	}
	buildCtx, ok = buildAgain.Context.Role(workspace.Build)
	if !ok || !taskContains(buildCtx.Plan, "build-plan-marker") {
		t.Fatalf("BUILD lost its previous plan: %+v", buildCtx)
	}
	if buildAgain.Status.IsTerminal() {
		t.Fatalf("the open todo did not keep the task resumable: %s", buildAgain.Status)
	}

	// 6/7. Restart and restore the same task from disk.
	reopened := task.NewController(task.NewStore(root), nil)
	reopened.SetRoleSource(func() workspace.Workspace { return workspace.Build })
	restored, found, err := reopened.Restore()
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	if restored.ID != firstID {
		t.Fatalf("restart changed the task identity: %q -> %q", firstID, restored.ID)
	}
	if len(restored.Context.Facts) == 0 || len(restored.Context.Hypotheses) != 1 ||
		len(restored.Context.Evidence) == 0 || len(restored.Context.Questions) != 1 {
		t.Fatalf("restart lost shared context: facts=%d hypotheses=%d evidence=%d questions=%d",
			len(restored.Context.Facts), len(restored.Context.Hypotheses),
			len(restored.Context.Evidence), len(restored.Context.Questions))
	}
	for _, expected := range []workspace.Workspace{workspace.Ask, workspace.Fuzz, workspace.Build} {
		if _, ok := restored.Context.Role(expected); !ok {
			t.Fatalf("%s context was lost across restart", expected.Label())
		}
	}
	restoredBrief := strings.Join(task.ContextFor(restored, workspace.Build, ""), "\n")
	for _, want := range []string{objective, "build-plan-marker", "the cache is stale"} {
		if !strings.Contains(restoredBrief, want) {
			t.Fatalf("restored briefing is missing %q:\n%s", want, restoredBrief)
		}
	}
	if len(restoredBrief) > 6000 {
		t.Fatalf("restored briefing = %d chars, want the bounded budget", len(restoredBrief))
	}
}

func runtimeHooks(controller *task.Controller) TaskHooks {
	return TaskHooks{
		Begin: func(intent contract.IntentContract, snapshot *agent.TargetSnapshot) TaskProjection {
			recorded, err := controller.Begin(intent, snapshot)
			return runtimeProjection(recorded, err)
		},
		Current: func() (TaskResume, bool) {
			current, ok := controller.Current()
			if !ok {
				return TaskResume{}, false
			}
			return runtimeResume(current), true
		},
		Sync: func() TaskProjection {
			recorded, err := controller.Sync()
			return runtimeProjection(recorded, err)
		},
		ListResumable: func() []TaskResume {
			tasks, err := controller.ListResumable()
			if err != nil {
				return nil
			}
			out := make([]TaskResume, 0, len(tasks))
			for _, t := range tasks {
				out = append(out, runtimeResume(t))
			}
			return out
		},
		ListArchived: func() []TaskResume {
			tasks, err := controller.ArchivedTasks()
			if err != nil {
				return nil
			}
			out := make([]TaskResume, 0, len(tasks))
			for _, t := range tasks {
				out = append(out, runtimeResume(t))
			}
			return out
		},
		Detach: func() TaskProjection {
			detached, _, err := controller.Detach()
			if detached.ID == "" && err == nil {
				return TaskProjection{}
			}
			return runtimeProjection(detached, err)
		},
		Pause: func(reason string) TaskProjection {
			recorded, err := controller.Pause(reason)
			return runtimeProjection(recorded, err)
		},
		NeedsReview: func(reason string) TaskProjection {
			recorded, err := controller.NeedsReview(reason)
			return runtimeProjection(recorded, err)
		},
		Complete: func() TaskProjection {
			recorded, err := controller.Complete()
			return runtimeProjection(recorded, err)
		},
		CompleteWithEvidence: func(evidence CompletionEvidence) TaskProjection {
			recorded, err := controller.CompleteWithEvidence(task.CompletionEvidence{
				Complete: evidence.Complete,
				Reason:   evidence.Reason,
				Answered: evidence.Answered,
			})
			return runtimeProjection(recorded, err)
		},
		Fail: func(reason string) TaskProjection {
			recorded, err := controller.Fail(reason)
			return runtimeProjection(recorded, err)
		},
		PreviewHandoff: func(target workspace.Workspace) (HandoffView, error) {
			recorded, err := controller.PreviewHandoff(task.HandoffInput{To: target})
			return runtimeHandoffView(recorded), err
		},
		ProposeHandoff: func(target workspace.Workspace, reason, summary, workspaceState, approvedObjective string) (HandoffView, error) {
			recorded, err := controller.ProposeHandoff(task.HandoffInput{
				To: target, Reason: reason, Summary: summary, Workspace: workspaceState,
				ApprovedObjective: approvedObjective,
			})
			return runtimeHandoffView(recorded), err
		},
		ApproveHandoff: func(id string) (HandoffView, error) {
			recorded, err := controller.ApproveHandoff(id)
			return runtimeHandoffView(recorded), err
		},
		FailHandoff: func(id, reason string) error {
			_, err := controller.FailHandoff(id, reason)
			return err
		},
		Context:          controller.Context,
		ObserveTurns:     controller.ObserveTurns,
		HasCurrentChange: controller.HasCurrentChange,
		Evidence:         controller.Evidence,
		RecordProjection: controller.RecordProjection,
		ProjectionReview: func() string {
			report, ok := controller.LastProjection()
			if !ok {
				return ""
			}
			return report.EvidenceReview()
		},
		ProjectionProvenance: func() agent.EvidenceProvenance {
			report, ok := controller.LastProjection()
			if !ok {
				return agent.EvidenceProvenance{}
			}
			return report.Provenance
		},
		Timeline: func(limit int) []TimelineView {
			current, ok := controller.Current()
			if !ok {
				return nil
			}
			events := current.Timeline
			if limit > 0 && limit < len(events) {
				events = events[len(events)-limit:]
			}
			out := make([]TimelineView, 0, len(events))
			for _, event := range events {
				view := TimelineView{
					Seq: event.Seq, At: event.At, Kind: string(event.Kind),
					Status: event.Status, Summary: event.Summary, Refs: event.Refs,
				}
				if event.Role.Valid() {
					view.Role = event.Role.Label()
				}
				out = append(out, view)
			}
			return out
		},
		Lineage: func(id string) (LineageView, []LineageView, bool) {
			if _, ok := controller.Current(); !ok {
				return LineageView{}, nil, false
			}
			if id == "" {
				return LineageView{}, runtimeLineageViews(controller.Lineage()), true
			}
			focus, related, found := controller.TraceLineage(id)
			return runtimeLineageView(focus), runtimeLineageViews(related), found
		},
	}
}

func runtimeResume(current task.Task) TaskResume {
	resume := TaskResume{
		ID: current.ID, Status: string(current.Status), Objective: current.Objective,
		ReviewReason: current.ReviewReason, Role: string(current.LastRole()),
	}
	resume.ObjectiveState = string(current.ObjectiveState())
	resume.CompletionSummary = current.CompletionSummary()
	resume.Warning = current.ContextWarning
	if !current.Recovery.Empty() {
		resume.RecoveryState = string(current.Recovery.State)
		resume.RecoveryDetail = current.Recovery.Detail
	}
	if current.Handoff != nil {
		resume.HandoffStatus = string(current.Handoff.Status)
		resume.HandoffTo = current.Handoff.To.String()
		resume.HandoffBy = current.Handoff.ApprovedBy
		resume.HandoffConsumed = current.Handoff.Consumed
		resume.HandoffApprovedObjective = current.Handoff.ApprovedObjective
	}
	if current.Contract != nil {
		resume.Contract = *current.Contract
		resume.HasContract = true
	}
	resume.Snapshot = current.Snapshot
	return resume
}

func runtimeLineageViews(entries []task.LineageEntry) []LineageView {
	out := make([]LineageView, 0, len(entries))
	for _, entry := range entries {
		out = append(out, runtimeLineageView(entry))
	}
	return out
}

func runtimeLineageView(entry task.LineageEntry) LineageView {
	view := LineageView{
		Kind: entry.Kind, ID: entry.ID, Stage: string(entry.Stage), Status: entry.Status,
		Text: entry.Text, Note: entry.Note, Artifact: entry.Artifact,
		EvidenceIDs: entry.EvidenceIDs, Contradictions: entry.Contradicts, Missing: entry.Missing,
	}
	if entry.Role.Valid() {
		view.Role = entry.Role.Label()
	}
	return view
}

func runtimeHandoffView(recorded task.Handoff) HandoffView {
	carried := recorded.Carried()
	return HandoffView{
		ID: recorded.ID, From: recorded.From.Label(), To: recorded.To.Label(),
		Status: string(recorded.Status), Objective: recorded.Objective,
		Reason: recorded.Reason, Summary: recorded.Summary, Workspace: recorded.Workspace,
		Facts: carried.Facts, Hypotheses: carried.Hypotheses, Questions: carried.Questions,
		Evidence: carried.Evidence, Artifacts: carried.Artifacts, Constraints: carried.Constraints,
	}
}

type runtimeReadTool struct{ path string }

func (t *runtimeReadTool) Name() string        { return "read" }
func (t *runtimeReadTool) Description() string { return "read a file" }
func (t *runtimeReadTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"path":{"type":"string"}}}`))
}
func (t *runtimeReadTool) Approval() agent.Approval {
	return agent.Approval{Tier: agent.TierRead, Policy: agent.PolicyAllow}
}
func (t *runtimeReadTool) Exclusive(map[string]any) bool { return false }
func (t *runtimeReadTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{Text: "hello", Details: map[string]any{"path": t.path, "totalLines": 1}}
}

type runtimeBashTool struct{}

func (t *runtimeBashTool) Name() string        { return "bash" }
func (t *runtimeBashTool) Description() string { return "run a command" }
func (t *runtimeBashTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object","properties":{"command":{"type":"string"}}}`))
}
func (t *runtimeBashTool) Approval() agent.Approval {
	return agent.Approval{Tier: agent.TierExec, Policy: agent.PolicyAllow}
}
func (t *runtimeBashTool) Exclusive(map[string]any) bool { return false }
func (t *runtimeBashTool) Execute(_ agent.Context, args map[string]any) agent.Result {
	command, _ := args["command"].(string)
	switch {
	case strings.Contains(command, "--repro"):
		return agent.Result{
			Text: "FAIL", Executes: true, Finished: true,
			Details: map[string]any{"command": command, "exitCode": 1},
		}
	case strings.Contains(command, "inspect"):
		return agent.Result{
			Text: "ok", Executes: true, Finished: true,
			Details: map[string]any{"command": command, "exitCode": 0},
		}
	default:
		return agent.Result{
			Text: "ok", Executes: true, Finished: true, Verifies: true,
			Verification: &agent.VerificationEvidence{Command: command, Check: contract.CheckTest},
			Details:      map[string]any{"command": command, "exitCode": 0},
		}
	}
}

func evidenceFor(recorded task.Task, artifact string) *task.EvidenceRecord {
	for i := range recorded.Context.Evidence {
		if recorded.Context.Evidence[i].Artifact == artifact {
			return &recorded.Context.Evidence[i]
		}
	}
	return nil
}

func hasVerificationFact(recorded task.Task) bool {
	for _, fact := range recorded.Context.Facts {
		if strings.Contains(fact.Text, "verification") {
			return true
		}
	}
	return false
}

func taskContainsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

// commandDigest mirrors cognition's command evidence identity. It is only used
// to find the runtime's own record for an assertion.
func commandDigest(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:8])
}
