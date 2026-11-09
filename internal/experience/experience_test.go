package experience

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

var testNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func testShape() cognition.TaskShape {
	return cognition.TaskShape{
		Version:   1,
		Objective: "inspect the parser",
		Kind:      contract.ObjectiveInformational,
		Intent:    "read",
		Authority: cognition.Authority{Workspace: "ask", ReadOnly: true},
	}
}

func testObs(shape cognition.TaskShape, turns []agent.Turn, outcome Outcome) Observation {
	return Observation{
		ProjectID:  "project-a",
		TaskID:     "task-a",
		Workspace:  "ask",
		Intent:     contract.IntentContract{Objective: "inspect the parser", Kind: contract.ObjectiveInformational},
		Shape:      shape,
		Turns:      turns,
		Outcome:    outcome,
		StartedAt:  testNow.Add(-time.Second),
		FinishedAt: testNow,
	}
}

func readCall(id, path string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID:   id,
		ToolName: "read",
		Args:     map[string]any{"path": path},
		Result: agent.Result{
			Text:    "package parser\n",
			Details: map[string]any{"path": path},
		},
	}
}

func readFailure(id, path, text string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID:   id,
		ToolName: "read",
		Args:     map[string]any{"path": path},
		Result:   agent.Result{Text: text, IsError: true, Details: map[string]any{"path": path}},
	}
}

func listCall(id, path string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID:   id,
		ToolName: "ls",
		Args:     map[string]any{"path": path},
		Result:   agent.Result{Text: "parser.go\nparser_test.go\n", Details: map[string]any{"path": path, "entries": 2}},
	}
}

func turn(results ...agent.ToolOutcome) agent.Turn {
	return agent.Turn{Results: results}
}

func measuredTurn(input, output int, results ...agent.ToolOutcome) agent.Turn {
	return agent.Turn{
		Assistant: ai.AssistantMessage{Usage: ai.Usage{Input: input, Output: output}},
		Results:   results,
	}
}

func newEngine(cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return testNow }
	}
	return New(cfg)
}

func TestRepeatedProbeIncidentIsDeterministic(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go")),
		turn(readCall("2", "src/main.go")),
	}, OutcomeCompleted)
	events := Extract(obs)
	if !hasEvent(events, EventRepeatedProbe) {
		t.Fatalf("events = %+v, want repeated probe", events)
	}
	repeat := eventOfKind(events, EventRepeatedProbe)
	if repeat.Tool != "read" {
		t.Errorf("tool = %q, want read", repeat.Tool)
	}
	// Repeating the same observation must not create duplicate lessons.
	engine := newEngine(Config{ProjectID: "project-a"})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	second, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	if len(second.RecordIDs) != len(first.RecordIDs) {
		t.Fatalf("record ids = %v then %v, want the same bounded records", first.RecordIDs, second.RecordIDs)
	}
	if len(engine.List("")) > 3 {
		t.Errorf("records = %d, want a bounded set for one repeated probe", len(engine.List("")))
	}
}

func TestIrrelevantObservationCreatesNoIncident(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{turn(readCall("1", "src/main.go"))}, OutcomeCompleted)
	if events := Extract(obs); len(events) != 0 {
		t.Fatalf("events = %+v, want none for one ordinary observation", events)
	}
}

func TestRecoverableFailureIsNonMaterialWhenEquivalentEvidenceExists(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readFailure("1", "src", "read src: is a directory")),
		turn(listCall("2", "src")),
	}, OutcomeNeedsReview)
	events := Extract(obs)
	event, ok := findEvent(events, EventRecoverableFailure)
	if !ok {
		t.Fatalf("events = %+v, want recoverable failure", events)
	}
	if !event.NonMaterial {
		t.Errorf("failure = %+v, want non-material", event)
	}
	engine := newEngine(Config{ProjectID: "project-a"})
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	explanation, ok := engine.Explain(engine.List("")[0].ID)
	if !ok {
		t.Fatal("Explain did not find the lesson")
	}
	if !strings.Contains(explanation.Record.Lesson.Statement, "not material") {
		t.Errorf("lesson = %q, want the non-material lesson", explanation.Record.Lesson.Statement)
	}
}

func TestMaterialFailureRemainsBlocking(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readFailure("1", "src/required.go", "read src/required.go: no such file")),
	}, OutcomeNeedsReview)
	events := Extract(obs)
	if !hasEvent(events, EventMaterialFailure) {
		t.Fatalf("events = %+v, want material failure", events)
	}
	if hasEvent(events, EventRecoverableFailure) {
		t.Fatalf("events = %+v, a material failure must not be reported recoverable", events)
	}
}

func TestSuccessfulShortcutCandidateIsPromotedAfterValidation(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a", MinObservations: 2})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(first.RecordIDs) != 1 {
		t.Fatalf("record ids = %v, want one shortcut candidate", first.RecordIDs)
	}
	record, ok := engine.Explain(first.RecordIDs[0])
	if !ok {
		t.Fatal("candidate not found")
	}
	if record.Record.Candidate.Status != StatusCandidate {
		t.Fatalf("status = %s, want candidate after one observation", record.Record.Candidate.Status)
	}
	cold := engine.Advise(cognition.AdviceRequest{Shape: testShape()})
	if !cold.Empty() {
		t.Fatalf("advice = %+v, want none before validation", cold)
	}
	if _, err := engine.Promote(first.RecordIDs[0]); !errors.Is(err, ErrNotValidated) {
		t.Fatalf("Promote error = %v, want ErrNotValidated", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	record, _ = engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Status != StatusValidated {
		t.Fatalf("status = %s, want validated after two observations", record.Record.Candidate.Status)
	}
	if !engine.Advise(cognition.AdviceRequest{Shape: testShape()}).Empty() {
		t.Fatal("validated but unpromoted experience must not be applied")
	}
	if _, err := engine.Promote(first.RecordIDs[0]); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	advice := engine.Advise(cognition.AdviceRequest{
		Shape: testShape(),
		Tools: []agent.Tool{fakeTool{"read"}, fakeTool{"grep"}},
	})
	if len(advice.PreferredTools) != 1 || advice.PreferredTools[0] != "read" {
		t.Fatalf("preferred tools = %v, want [read]", advice.PreferredTools)
	}
}

func TestContradictoryObservationPreventsPromotion(t *testing.T) {
	success := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a", AutoPromote: true})
	first, err := engine.Observe(success)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	adverse := testObs(testShape(), nil, OutcomeNeedsReview)
	adverse.Projection = agent.ProjectionReport{Sufficiency: "INSUFFICIENT"}
	if _, err := engine.Observe(adverse); err != nil {
		t.Fatalf("Observe adverse: %v", err)
	}
	record, ok := engine.Explain(first.RecordIDs[0])
	if !ok {
		t.Fatal("shortcut candidate disappeared")
	}
	if record.Record.Candidate.Contradictions == 0 {
		t.Fatalf("record = %+v, want a contradiction", record.Record.Candidate)
	}
	if record.Record.Candidate.Status.Active() {
		t.Fatalf("status = %s, contradictory evidence must not be active", record.Record.Candidate.Status)
	}
	if _, err := engine.Promote(first.RecordIDs[0]); !errors.Is(err, ErrContradicted) {
		t.Fatalf("Promote error = %v, want ErrContradicted", err)
	}
}

func TestContradictoryObservationRetiresPromotedExperience(t *testing.T) {
	success := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a", AutoPromote: true})
	first, err := engine.Observe(success)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(success); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	record, _ := engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Status != StatusPromoted {
		t.Fatalf("status = %s, want promoted", record.Record.Candidate.Status)
	}
	adverse := testObs(testShape(), nil, OutcomeNeedsReview)
	adverse.Projection = agent.ProjectionReport{Sufficiency: "INSUFFICIENT"}
	for i := 0; i < 2; i++ {
		if _, err := engine.Observe(adverse); err != nil {
			t.Fatalf("Observe adverse %d: %v", i, err)
		}
	}
	record, _ = engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Status != StatusRetired {
		t.Fatalf("status = %s, want retired after repeated contradictions", record.Record.Candidate.Status)
	}
	retiredAdvice := engine.Advise(cognition.AdviceRequest{
		Shape:       testShape(),
		Tools:       []agent.Tool{fakeTool{"read"}},
		Sufficiency: cognition.Sufficiency{State: cognition.Sufficient},
	})
	if len(retiredAdvice.PreferredTools) != 0 {
		t.Fatalf("retired experience still ordered tools: %+v", retiredAdvice)
	}
}

func TestEvidenceGapCannotBecomeEvidenceOrPreferredTools(t *testing.T) {
	obs := testObs(testShape(), nil, OutcomeNeedsReview)
	obs.Projection = agent.ProjectionReport{Sufficiency: "INSUFFICIENT"}
	engine := newEngine(Config{ProjectID: "project-a", AutoPromote: true})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	record, _ := engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Kind != CandidateEvidencePriority {
		t.Fatalf("kind = %s, want evidence priority", record.Record.Candidate.Kind)
	}
	advice := engine.Advise(cognition.AdviceRequest{Shape: testShape()})
	if len(advice.PreferredTools) != 0 {
		t.Fatalf("preferred tools = %v, an evidence gap must not become a tool grant", advice.PreferredTools)
	}
	if len(advice.Hints) == 0 {
		t.Fatal("want an evidence-priority hint")
	}
	if !strings.Contains(strings.Join(advice.Hints, " "), "evidence") {
		t.Errorf("hints = %v, want evidence guidance", advice.Hints)
	}
}

func TestHandoffRedundancyIsRecorded(t *testing.T) {
	shape := testShape()
	shape.Authority.Workspace = "fuzz"
	prior := cognition.NewEvidenceState()
	prior.Items = []cognition.EvidenceItem{{
		ID:     "file:src/main.go",
		Status: cognition.StatusKnown,
		Source: cognition.Source{Path: "src/main.go"},
	}}
	obs := testObs(shape, []agent.Turn{turn(readCall("1", "src/main.go"))}, OutcomeCompleted)
	obs.PriorWorkspace = "build"
	obs.Workspace = "fuzz"
	obs.PriorEvidence = prior
	if !hasEvent(Extract(obs), EventHandoffRedundancy) {
		t.Fatalf("events = %+v, want handoff redundancy", Extract(obs))
	}
}

func TestAuthorityRefusalCannotBecomePolicy(t *testing.T) {
	shape := testShape()
	shape.Authority.Workspace = "ask"
	refusal := agent.ToolOutcome{
		CallID:   "1",
		ToolName: "write",
		Args:     map[string]any{"path": "src/main.go"},
		Result:   agent.Result{Text: "denied by authority", IsError: true, Refused: true},
	}
	obs := testObs(shape, []agent.Turn{turn(refusal)}, OutcomeNeedsReview)
	engine := newEngine(Config{ProjectID: "project-a", AutoPromote: true})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	record, _ := engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Kind != CandidateRecoveryHint {
		t.Fatalf("kind = %s, want a recovery hint", record.Record.Candidate.Kind)
	}
	advice := engine.Advise(cognition.AdviceRequest{
		Shape: shape,
		Tools: []agent.Tool{fakeTool{"read"}},
		Turns: []agent.Turn{turn(refusal)},
	})
	if len(advice.PreferredTools) != 0 {
		t.Fatalf("preferred tools = %v, an authority refusal cannot promote a tool", advice.PreferredTools)
	}
	if len(advice.Hints) == 0 {
		t.Fatal("want a recovery hint when the same refused call recurs")
	}
	if strings.Contains(strings.ToLower(strings.Join(advice.Hints, " ")), "grant") {
		t.Fatalf("hints = %v, authority guidance must not imply a grant", advice.Hints)
	}
}

func TestContextGrowthIsBounded(t *testing.T) {
	obs := testObs(testShape(), nil, OutcomeCompleted)
	obs.Projection = agent.ProjectionReport{BytesBefore: contextGrowthBytes + 1}
	event, ok := findEvent(Extract(obs), EventContextGrowth)
	if !ok {
		t.Fatalf("events = %+v, want context growth", Extract(obs))
	}
	if event.Cost.ToolCalls != 0 {
		t.Errorf("cost = %+v, want a zero-call observation", event.Cost)
	}
}

func TestCostIsCaptured(t *testing.T) {
	turns := []agent.Turn{measuredTurn(120, 30,
		readCall("1", "src/main.go"), readCall("2", "src/parser.go"))}
	obs := testObs(testShape(), turns, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a"})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	record, _ := engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Cost.InputTokens != 120 || record.Record.Candidate.Cost.OutputTokens != 30 {
		t.Fatalf("cost = %+v, want measured tokens", record.Record.Candidate.Cost)
	}
	if record.Record.Candidate.Cost.ToolCalls != 2 {
		t.Fatalf("tool calls = %d, want 2", record.Record.Candidate.Cost.ToolCalls)
	}
}

func TestExplicitRetirementStopsAdvice(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a", AutoPromote: true})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	if _, err := engine.Retire(first.RecordIDs[0], "human review"); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	record, _ := engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Status != StatusRetired {
		t.Fatalf("status = %s, want retired", record.Record.Candidate.Status)
	}
	advice := engine.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}})
	if len(advice.PreferredTools) != 0 {
		t.Fatalf("retired advice = %+v, want none", advice)
	}
}

func TestLowerCostPathOrdersFirst(t *testing.T) {
	engine := newEngine(Config{ProjectID: "project-a"})
	engine.Advise(cognition.AdviceRequest{Shape: testShape()}) // load empty docs
	ctx := ContextForShape(testShape(), "ask")
	expensive := newRecord(
		Event{Kind: EventSuccessfulShortcut, Key: "shortcut|expensive", Summary: "expensive"},
		ctx, ScopeProject, "project-a", testNow,
	)
	expensive.Candidate.Status = StatusPromoted
	expensive.Candidate.Kind = CandidatePreferredToolOrder
	expensive.Candidate.PreferredTools = []string{"read"}
	expensive.Candidate.Cost = Cost{ToolCalls: 20, RepeatedToolCalls: 5}
	cheap := newRecord(
		Event{Kind: EventSuccessfulShortcut, Key: "shortcut|cheap", Summary: "cheap"},
		ctx, ScopeProject, "project-a", testNow,
	)
	cheap.Candidate.Status = StatusPromoted
	cheap.Candidate.Kind = CandidatePreferredToolOrder
	cheap.Candidate.PreferredTools = []string{"grep"}
	cheap.Candidate.Cost = Cost{ToolCalls: 7, RepeatedToolCalls: 0}
	engine.mu.Lock()
	engine.projectDoc.Records = []Record{expensive, cheap}
	engine.mu.Unlock()
	advice := engine.Advise(cognition.AdviceRequest{
		Shape: testShape(),
		Tools: []agent.Tool{fakeTool{"read"}, fakeTool{"grep"}},
	})
	if len(advice.PreferredTools) < 2 || advice.PreferredTools[0] != "grep" {
		t.Fatalf("preferred tools = %v, want the lower-cost path first", advice.PreferredTools)
	}
}

func TestTaskScopedExperienceDoesNotLeak(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	obs.Scope = ScopeTask
	engine := newEngine(Config{ProjectID: "project-a", TaskID: "task-a", AutoPromote: true})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	if advice := engine.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}}); advice.Empty() {
		t.Fatal("task-scoped advice was not applied in its own task")
	}
	other := newEngine(Config{ProjectID: "project-a", TaskID: "task-b"})
	if !other.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}}).Empty() {
		t.Fatal("task-scoped experience leaked into another task")
	}
	_ = first
}

func TestProjectExperienceDoesNotBecomeGlobalAutomatically(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global.json")
	projectPath := filepath.Join(dir, "project-a.json")
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	p1 := newEngine(Config{ProjectID: "project-a", ProjectPath: projectPath, GlobalPath: globalPath, AutoPromote: true})
	first, err := p1.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p1.Observe(obs); err != nil {
			t.Fatalf("Observe %d: %v", i, err)
		}
	}
	record, _ := p1.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Observations < 3 {
		t.Fatalf("observations = %d, want 3", record.Record.Candidate.Observations)
	}
	if _, err := p1.Validate(first.RecordIDs[0], Validation{Kind: ValidationRegression, Ref: "TestSuccessfulShortcutCandidateIsPromotedAfterValidation", Passed: true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	p2 := newEngine(Config{ProjectID: "project-b", ProjectPath: filepath.Join(dir, "project-b.json"), GlobalPath: globalPath})
	if !p2.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}}).Empty() {
		t.Fatal("project experience became global without explicit promotion")
	}
	if _, err := p1.PromoteToGlobal(first.RecordIDs[0]); err != nil {
		t.Fatalf("PromoteToGlobal: %v", err)
	}
	p3 := newEngine(Config{ProjectID: "project-c", ProjectPath: filepath.Join(dir, "project-c.json"), GlobalPath: globalPath})
	advice := p3.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}})
	if len(advice.PreferredTools) != 1 || advice.PreferredTools[0] != "read" {
		t.Fatalf("global advice = %+v, want the explicitly promoted preference", advice)
	}
}

func TestGlobalPromotionRequiresRegressionAndRecurrence(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global.json")
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a", ProjectPath: filepath.Join(dir, "project.json"), GlobalPath: globalPath, AutoPromote: true})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	if _, err := engine.PromoteToGlobal(first.RecordIDs[0]); !errors.Is(err, ErrInsufficientRecurrence) {
		t.Fatalf("PromoteToGlobal error = %v, want ErrInsufficientRecurrence", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe third: %v", err)
	}
	if _, err := engine.PromoteToGlobal(first.RecordIDs[0]); !errors.Is(err, ErrNoRegression) {
		t.Fatalf("PromoteToGlobal error = %v, want ErrNoRegression", err)
	}
}

func TestMinimumObservationsIsEnforced(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a", MinObservations: 3})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	record, _ := engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Status != StatusCandidate {
		t.Fatalf("status = %s, want candidate before three observations", record.Record.Candidate.Status)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe third: %v", err)
	}
	record, _ = engine.Explain(first.RecordIDs[0])
	if record.Record.Candidate.Status != StatusValidated {
		t.Fatalf("status = %s, want validated after three observations", record.Record.Candidate.Status)
	}
}

func TestExplicitRegressionCanPromoteSingleObservation(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a"})
	first, err := engine.Observe(obs)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Validate(first.RecordIDs[0], Validation{
		Kind: ValidationRegression, Ref: "TestSuccessfulShortcutCandidateIsPromotedAfterValidation", Passed: true,
	}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, err := engine.Promote(first.RecordIDs[0]); err != nil {
		t.Fatalf("Promote with regression: %v", err)
	}
	if advice := engine.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}}); len(advice.PreferredTools) != 1 {
		t.Fatalf("advice = %+v, want the regression-validated preference", advice)
	}
}

func TestObserveCannotWriteGlobalScopeDirectly(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	obs.Scope = ScopeGlobal
	engine := newEngine(Config{ProjectID: "project-a", ProjectPath: filepath.Join(t.TempDir(), "project.json"), GlobalPath: filepath.Join(t.TempDir(), "global.json")})
	if _, err := engine.Observe(obs); !errors.Is(err, ErrGlobalPromotionOnly) {
		t.Fatalf("Observe error = %v, want ErrGlobalPromotionOnly", err)
	}
}

func TestProjectStoreRoundTripKeepsPromotedAdvice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "experience.json")
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	first := newEngine(Config{ProjectID: "project-a", ProjectPath: path, AutoPromote: true})
	for i := 0; i < 2; i++ {
		if _, err := first.Observe(obs); err != nil {
			t.Fatalf("Observe %d: %v", i, err)
		}
	}
	reopened := newEngine(Config{ProjectID: "project-a", ProjectPath: path})
	advice := reopened.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}})
	if len(advice.PreferredTools) != 1 || advice.PreferredTools[0] != "read" {
		t.Fatalf("reopened advice = %+v, want the persisted preference", advice)
	}
}

func TestStoreRecordFromAnotherProjectIsIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "experience.json")
	foreign := newRecord(
		Event{Kind: EventSuccessfulShortcut, Key: "shortcut|foreign", Summary: "shortcut"},
		ContextForShape(testShape(), "ask"),
		ScopeProject, "project-b", testNow,
	)
	foreign.Candidate.Status = StatusPromoted
	foreign.Candidate.PreferredTools = []string{"read"}
	doc := Document{Version: StoreVersion, Records: []Record{foreign}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	engine := newEngine(Config{ProjectID: "project-a", ProjectPath: path})
	if advice := engine.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}}); !advice.Empty() {
		t.Fatalf("advice = %+v, another project's record must not apply", advice)
	}
}

func TestFailedPersistenceRollsBackInMemoryRecords(t *testing.T) {
	dir := t.TempDir()
	engine := newEngine(Config{ProjectID: "project-a", ProjectPath: filepath.Join(dir, "experience.json")})
	engine.project.write = func(string, []byte) error { return errors.New("disk full") }
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	if _, err := engine.Observe(obs); err == nil {
		t.Fatal("Observe succeeded despite a failed store write")
	}
	if records := engine.List(""); len(records) != 0 {
		t.Fatalf("records = %d, want the failed observation rolled back", len(records))
	}
}

func TestMalformedExperienceIsIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "experience.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := newEngine(Config{ProjectID: "project-a", ProjectPath: path})
	if advice := engine.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}}); !advice.Empty() {
		t.Fatalf("advice = %+v, malformed state must be ignored", advice)
	}
	if warnings := engine.Warnings(); len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "malformed") {
		t.Fatalf("warnings = %v, want a malformed-state diagnostic", warnings)
	}
}

func TestUnsupportedExperienceVersionIsIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "experience.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"records":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := newEngine(Config{ProjectID: "project-a", ProjectPath: path})
	if advice := engine.Advise(cognition.AdviceRequest{Shape: testShape(), Tools: []agent.Tool{fakeTool{"read"}}}); !advice.Empty() {
		t.Fatalf("advice = %+v, newer state must be ignored", advice)
	}
	if warnings := engine.Warnings(); len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "version 99") {
		t.Fatalf("warnings = %v, want an unsupported-version diagnostic", warnings)
	}
}

func TestStoreDeduplicatesSameRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "experience.json")
	event := Event{Kind: EventSuccessfulShortcut, Key: "shortcut|dedup", Summary: "shortcut"}
	older := newRecord(event, Context{Workspace: "ask"}, ScopeProject, "project-a", testNow)
	newer := older
	newer.Candidate.Observations = 3
	newer.UpdatedAt = testNow.Add(time.Minute)
	doc := Document{Version: StoreVersion, Records: []Record{older, newer}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Records) != 1 {
		t.Fatalf("records = %d, want one deduplicated record", len(loaded.Records))
	}
	if loaded.Records[0].Candidate.Observations != 3 {
		t.Fatalf("observations = %d, want the newest higher count", loaded.Records[0].Candidate.Observations)
	}
	if len(warnings) == 0 {
		t.Fatal("want a duplicate diagnostic")
	}
}

func TestAdviceRequiresMatchingContext(t *testing.T) {
	shape := testShape()
	shape.NeedsCodeNavigation = true
	obs := testObs(shape, []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a", AutoPromote: true})
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	other := testShape()
	other.NeedsCodeNavigation = false
	advice := engine.Advise(cognition.AdviceRequest{Shape: other, Tools: []agent.Tool{fakeTool{"read"}}})
	if !advice.Empty() {
		t.Fatalf("advice = %+v, a different context must not reuse the path", advice)
	}
}

func TestCorruptRecordIsDroppedAndValidRecordsRetained(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "experience.json")
	valid := newRecord(
		Event{Kind: EventSuccessfulShortcut, Key: "shortcut|project-a", Summary: "shortcut"},
		Context{Workspace: "ask", TaskKind: "informational"},
		ScopeProject, "project-a", testNow,
	)
	doc := Document{Version: StoreVersion, Records: []Record{valid, {ID: "broken"}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Records) != 1 {
		t.Fatalf("records = %d, want the valid record retained", len(loaded.Records))
	}
	if len(warnings) == 0 {
		t.Fatal("want a warning for the dropped record")
	}
}

func TestRegressionCatalogCoversEveryLessonKind(t *testing.T) {
	kinds := []string{
		EventRepeatedProbe, EventRecoverableFailure, EventMaterialFailure,
		EventSuccessfulShortcut, EventExpensivePath, EventEvidenceGap,
		EventStaleReview, EventHandoffRedundancy, EventAuthorityRefusal,
		EventContextGrowth,
	}
	for _, kind := range kinds {
		if RegressionFor(kind).Name == "" {
			t.Errorf("lesson kind %q has no regression case", kind)
		}
	}
}

func TestBoundedRecordStore(t *testing.T) {
	records := make([]Record, 0, MaxRecords+10)
	for i := 0; i < MaxRecords+10; i++ {
		event := Event{
			Kind:    EventSuccessfulShortcut,
			Key:     "shortcut|" + string(rune('a'+i%26)) + "|" + string(rune('a'+i/26)),
			Summary: "shortcut",
		}
		records = append(records, newRecord(event, Context{Workspace: "ask"}, ScopeProject, "project-a", testNow))
	}
	trimmed := trimRecords(records)
	if len(trimmed) != MaxRecords {
		t.Fatalf("records = %d, want %d", len(trimmed), MaxRecords)
	}
}

func TestObserveWithNoPersistencePathStillWorks(t *testing.T) {
	obs := testObs(testShape(), []agent.Turn{
		turn(readCall("1", "src/main.go"), readCall("2", "src/parser.go")),
	}, OutcomeCompleted)
	engine := newEngine(Config{ProjectID: "project-a"})
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(engine.List("")) == 0 {
		t.Fatal("want an in-memory record")
	}
}

func TestRepeatedReviewDoesNotProduceAppliedAdvice(t *testing.T) {
	obs := testObs(testShape(), nil, OutcomeNeedsReview)
	obs.PriorStatus = "NEEDS_REVIEW"
	obs.PriorReviewReason = "required evidence was not observed"
	if !hasEvent(Extract(obs), EventStaleReview) {
		t.Fatalf("events = %+v, want stale review", Extract(obs))
	}
	engine := newEngine(Config{ProjectID: "project-a", AutoPromote: true})
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if _, err := engine.Observe(obs); err != nil {
		t.Fatalf("Observe second: %v", err)
	}
	if !engine.Advise(cognition.AdviceRequest{Shape: testShape()}).Empty() {
		t.Fatal("a stale review must not become an applied path preference")
	}
}

// fakeTool is the minimal agent.Tool an advice test needs.
type fakeTool struct{ name string }

func (f fakeTool) Name() string                   { return f.name }
func (f fakeTool) Description() string            { return "test tool" }
func (f fakeTool) Parameters() *jsonschema.Schema { return nil }
func (f fakeTool) Approval() agent.Approval       { return agent.Approval{Tier: agent.TierRead} }
func (f fakeTool) Exclusive(map[string]any) bool  { return false }
func (f fakeTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{}
}

// The helpers below keep assertions about the event set readable.
func hasEvent(events []Event, kind string) bool {
	_, ok := findEvent(events, kind)
	return ok
}

func findEvent(events []Event, kind string) (Event, bool) {
	for _, event := range events {
		if event.Kind == kind {
			return event, true
		}
	}
	return Event{}, false
}

func eventOfKind(events []Event, kind string) Event {
	event, _ := findEvent(events, kind)
	return event
}
