package cognition

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/contract"
	"github.com/PizenLabs/ohgo/jsonschema"
	"github.com/PizenLabs/ohgo/security"
)

func readContract(objective string) contract.IntentContract {
	return contract.IntentContract{
		Objective:       objective,
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{{Kind: contract.TargetPath, Value: "README.md"}},
		ExpectedChanges: []contract.ExpectedChange{{Target: contract.Target{Kind: contract.TargetPath, Value: "README.md"}, Operation: contract.ChangeAny}},
		Verification:    []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
}

func TestBuildShapeDerivesMutationScopeAndCompletion(t *testing.T) {
	shape := BuildShape(readContract("Check this project and write README.md."), ShapeParams{Authority: "build"})
	if !shape.Mutation.Allowed {
		t.Fatal("mutation should be allowed in BUILD")
	}
	if !shape.Mutation.Explicit || len(shape.Mutation.Paths) != 1 || shape.Mutation.Paths[0] != "README.md" {
		t.Fatalf("mutation scope = %#v", shape.Mutation)
	}
	if !shape.Mutation.UnrelatedForbidden {
		t.Fatal("an explicit path target must forbid unrelated mutation")
	}
	if !shape.RepositoryScope {
		t.Fatal("a request about this project must record repository scope")
	}
	keys := map[string]bool{}
	for _, need := range shape.RequiredKnowledge {
		keys[need.Key] = true
	}
	if !keys["repository_overview"] || !keys["target_state:README.md"] || !keys["verification"] {
		t.Fatalf("required knowledge = %#v", shape.RequiredKnowledge)
	}
	conditions := map[string]bool{}
	for _, condition := range shape.Completion {
		conditions[condition.Key] = true
	}
	if !conditions["target_mutated:README.md"] || !conditions["verification_after_change"] || !conditions["no_unrelated_mutation"] {
		t.Fatalf("completion = %#v", shape.Completion)
	}
}

func TestInformationalShapeRequiresTheNamedTarget(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "What does index.html do?",
		Kind:      contract.ObjectiveInformational,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "index.html"}},
	}, ShapeParams{Authority: "ask"})
	var found bool
	for _, need := range shape.RequiredKnowledge {
		if need.Key == "target_state:index.html" {
			found = true
		}
	}
	if !found {
		t.Fatalf("required knowledge = %#v, want target_state:index.html", shape.RequiredKnowledge)
	}
}

func TestBuildShapeNarrowsMutationWhenAuthorityForbids(t *testing.T) {
	shape := BuildShape(readContract("Check this project and write README.md."), ShapeParams{Authority: "ask"})
	if shape.Mutation.Allowed {
		t.Fatal("ASK authority must not record mutation as allowed")
	}
	if !shape.Authority.ReadOnly {
		t.Fatal("ASK must be read-only")
	}
}

func TestBuildShapeHandlesCommaSeparatedMutationClause(t *testing.T) {
	// normalizeRequest preserves the comma as a clause boundary; without that
	// the mutation clause was hidden inside the read-headed first clause.
	c := contract.IntentContract{Kind: contract.ObjectiveMutation, Objective: "Check the project, write README.md."}
	shape := BuildShape(c, ShapeParams{Authority: "build"})
	if !shape.Mutation.Allowed {
		t.Fatal("a comma-separated mutation clause must be recognized")
	}
}

func TestBuildShapeKeepsExternalKnowledgeExplicit(t *testing.T) {
	c := contract.IntentContract{Kind: contract.ObjectiveMutation, Objective: "Write README.md using the official documentation at https://example.test/docs."}
	shape := BuildShape(c, ShapeParams{Authority: "build"})
	if !shape.External.Required {
		t.Fatal("a URL must make external knowledge required")
	}
	if !shape.NeedsExecution {
		t.Fatal("a mutation with verification must record that execution is needed")
	}
}

func TestBuildShapeHonorsExplicitNoMutationAndNoExternal(t *testing.T) {
	c := contract.IntentContract{
		Objective: "Write README.md using the docs at https://example.test/docs. Do not modify anything and do not use the web.",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "README.md"}},
	}
	shape := BuildShape(c, ShapeParams{Authority: "build"})
	if shape.Mutation.Allowed {
		t.Fatal("an explicit no-mutation constraint must not leave mutation allowed")
	}
	if shape.Authority.MutationAllowed {
		t.Fatal("an explicit no-mutation constraint must not leave mutation permitted")
	}
	if shape.External.Required {
		t.Fatal("an explicit no-external constraint must leave external knowledge disabled")
	}
}

func TestInformationalContentQuestionNeedsAFileRead(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "Read index.html and explain what it does.",
		Kind:      contract.ObjectiveInformational,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "index.html"}},
	}, ShapeParams{Authority: "ask"})
	var contentKey bool
	for _, need := range shape.RequiredKnowledge {
		if need.Key == "target_content:index.html" {
			contentKey = true
		}
	}
	if !contentKey {
		t.Fatalf("required knowledge = %#v, want target_content:index.html", shape.RequiredKnowledge)
	}
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
		Result: agent.Result{Text: "index.html\nstyle.css", Details: map[string]any{"path": ".", "entries": 2}},
	})
	if state.Satisfied("target_content:index.html", ExecutionState{}) {
		t.Fatal("a directory listing satisfied a target's content requirement")
	}
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "index.html"},
		Result: agent.Result{Text: "1\t<!doctype html>", Details: map[string]any{"path": "index.html", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	if !state.Satisfied("target_content:index.html", ExecutionState{}) {
		t.Fatal("a file read did not satisfy a target's content requirement")
	}
}

func TestStructuralQuerySatisfiesCoordinatesForANamedPath(t *testing.T) {
	// A symbols query that names the exact path is coordinate evidence for a
	// package/declaration question; it is not a file read, and it must not
	// satisfy a content question about an unrelated path.
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "symbols-1", ToolName: "symbols",
		Args: map[string]any{"symbol": "tui"},
		Result: agent.Result{
			Text: "status: ok",
			Details: map[string]any{
				"coordinates": []map[string]any{{
					"path": "tui/approval.go", "symbol": "tui", "kind": "package",
					"start_line": 1, "end_line": 1,
				}},
			},
		},
	})
	if !state.Satisfied("target_state:tui/approval.go", ExecutionState{}) {
		t.Fatal("a symbols result did not establish target state for the named path")
	}
	if !state.Satisfied("target_content:tui/approval.go", ExecutionState{}) {
		t.Fatal("a symbols result did not satisfy coordinate content for the named path")
	}
	if state.Satisfied("target_content:index.html", ExecutionState{}) {
		t.Fatal("a symbols result for one path satisfied content for another")
	}
}

func TestRepositoryOverviewRequiresContentEvidence(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
		Result: agent.Result{Text: "index.html\nstyle.css", Details: map[string]any{"path": ".", "entries": 2}},
	})
	if state.Satisfied("repository_overview", ExecutionState{}) {
		t.Fatal("a directory listing alone satisfied a repository overview")
	}
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "index.html"},
		Result: agent.Result{Text: "1\t<!doctype html>", Details: map[string]any{"path": "index.html", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	if !state.Satisfied("repository_overview", ExecutionState{}) {
		t.Fatal("a file read did not satisfy a repository overview")
	}
}

func TestEvidenceRecordsMissingTargetAndDiagnostic(t *testing.T) {
	state := NewEvidenceState()
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	state.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{
		{
			CallID:   "call-1",
			ToolName: "read",
			Args:     map[string]any{"path": "README.md"},
			Result: agent.Result{
				Text:    `stat "README.md": no such file or directory`,
				IsError: true,
				Details: map[string]any{"path": "README.md"},
			},
		},
	}}})
	item, ok := state.Find("file:README.md")
	if !ok || item.Summary != "README.md: absent (observed by read)" {
		t.Fatalf("missing file evidence = %#v, %v", item, ok)
	}
	if _, ok := state.Find("error:read:call-1"); !ok {
		t.Fatal("the exact diagnostic must be retained")
	}
}

func TestEvidenceMarksConflictingReadsWithoutMutation(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "index.html"},
		Result: agent.Result{Text: "1\t<!doctype html>", Details: map[string]any{"path": "index.html", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-2", ToolName: "read", Args: map[string]any{"path": "index.html"},
		Result: agent.Result{Text: "1\t<html>", Details: map[string]any{"path": "index.html", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	item, _ := state.Find("file:index.html")
	if item.Status != StatusConflicting {
		t.Fatalf("status = %s, want CONFLICTING", item.Status)
	}
	if !strings.Contains(item.Conflict, "without a recorded runtime mutation") {
		t.Fatalf("conflict = %q", item.Conflict)
	}
}

func TestEvidenceDifferentReadWindowsAreNotAConflict(t *testing.T) {
	// Paging through a large file is one investigation, not two disagreeing
	// observations. Only the same source range may conflict.
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "large.go"},
		Result: agent.Result{Text: "1\tpackage large", Details: map[string]any{"path": "large.go", "totalLines": 100, "offset": 1, "limit": 10}},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-2", ToolName: "read", Args: map[string]any{"path": "large.go"},
		Result: agent.Result{Text: "51\tfunc later() {}", Details: map[string]any{"path": "large.go", "totalLines": 100, "offset": 51, "limit": 10}},
	})
	item, ok := state.Find("file:large.go")
	if !ok {
		t.Fatal("file evidence missing")
	}
	if item.Status == StatusConflicting {
		t.Fatalf("different windows were marked conflicting: %#v", item)
	}
	if len(state.Conflicts()) != 0 {
		t.Fatalf("conflicts = %#v", state.Conflicts())
	}
}

func TestEvidenceAcceptsDifferentReadAfterRecordedMutation(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{Text: "1\told", Details: map[string]any{"path": "README.md", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "write-1", ToolName: "write", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{Text: "Wrote 7 bytes to README.md.", Mutates: true, Details: map[string]any{"path": "README.md", "bytes": 7}},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-2", ToolName: "read", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{Text: "1\tnew", Details: map[string]any{"path": "README.md", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	item, _ := state.Find("file:README.md")
	if item.Status != StatusKnown {
		t.Fatalf("status = %s, want KNOWN after a recorded mutation", item.Status)
	}
}

func TestEvidenceIgnoresRefusedCalls(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "write-1", ToolName: "write",
		Args:   map[string]any{"path": "README.md"},
		Result: agent.Result{Text: "authorization denied", IsError: true},
		Authorization: &security.Decision{
			Outcome: security.OutcomeDeny,
			Rule:    "capability_not_permitted",
		},
	})
	if len(state.Items) != 0 {
		t.Fatalf("a denied call produced evidence: %#v", state.Items)
	}
}

func TestEvidenceExternalRetainsProvenanceAndUntrustedMark(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "fetch-1", ToolName: "web_fetch", Args: map[string]any{"url": "https://example.test/docs"},
		Result: agent.Result{
			Text: "[EXTERNAL KNOWLEDGE — UNTRUSTED DATA]\n| docs",
			Details: map[string]any{
				"url": "https://example.test/docs",
				"evidence": []any{map[string]any{
					"kind":  "WEB_FETCH",
					"trust": "untrusted_external",
					"provenance": map[string]any{
						"capability": "web.fetch",
						"source":     "example.test",
						"locator":    "https://example.test/docs",
						"provider":   "web_fetch",
					},
				}},
			},
		},
	})
	var found bool
	for _, item := range state.Items {
		if item.Retention != RetentionExternal {
			continue
		}
		found = true
		if item.Source.Trust != "untrusted_external" || item.Source.URL != "https://example.test/docs" {
			t.Fatalf("external provenance = %#v", item.Source)
		}
		if !strings.HasPrefix(item.Summary, "UNTRUSTED") {
			t.Fatalf("external summary must be marked untrusted: %q", item.Summary)
		}
	}
	if !found {
		t.Fatal("external evidence was not recorded")
	}
}

func TestProjectionElidesStructuralResultsAndKeepsRecentExact(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
		Result: agent.Result{Text: "a\nb\nc", Details: map[string]any{"path": ".", "entries": 3}},
	})
	messages := []agent.Message{
		agent.UserMessage{Text: "check"},
		agent.ToolResultMessage{ToolCallID: "ls-1", ToolName: "ls", Content: []ai.ContentBlock{ai.TextContent{Text: "a\nb\nc"}}},
		agent.ToolResultMessage{ToolCallID: "read-1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "exact recent"}}},
	}
	projected, report := projectMessages(messages, state, ProjectorOptions{KeepRecentToolResults: 1, ExactKeepBytes: 4096})
	if len(projected) != len(messages) {
		t.Fatalf("projection removed messages: %d -> %d", len(messages), len(projected))
	}
	first, _ := projected[1].(agent.ToolResultMessage)
	if !strings.Contains(first.Text(), "summarized from runtime evidence") {
		t.Fatalf("old structural result was not elided: %q", first.Text())
	}
	second, _ := projected[2].(agent.ToolResultMessage)
	if second.Text() != "exact recent" {
		t.Fatalf("recent result was rewritten: %q", second.Text())
	}
	if len(report.Elisions) != 1 {
		t.Fatalf("elisions = %d, want 1", len(report.Elisions))
	}
}

func TestProjectionEnforcesExactWorkingSetBudget(t *testing.T) {
	// The budget is a contract, not a hint. Previously an old exact result
	// smaller than ExactKeepBytes bypassed the working-set budget and every
	// page of a large file stayed verbatim.
	state := NewEvidenceState()
	messages := []agent.Message{agent.UserMessage{Text: "read several files"}}
	for i := 0; i < 5; i++ {
		path := "file" + itoa(i) + ".go"
		text := strings.Repeat("x", 2048)
		state.ObserveOutcome(agent.ToolOutcome{
			CallID: "read-" + itoa(i), ToolName: "read", Args: map[string]any{"path": path},
			Result: agent.Result{Text: text, Details: map[string]any{"path": path, "totalLines": 1, "offset": 1, "limit": 2000}},
		})
		messages = append(messages, agent.ToolResultMessage{
			ToolCallID: "read-" + itoa(i), ToolName: "read",
			Content: []ai.ContentBlock{ai.TextContent{Text: text}},
		})
	}
	options := ProjectorOptions{
		KeepRecentToolResults: 1,
		ExactKeepBytes:        4096,
		ToolResultBudgetBytes: 4096,
	}
	projected, report := projectMessages(messages, state, options)
	exactBytes := 0
	for _, message := range projected {
		result, ok := message.(agent.ToolResultMessage)
		if !ok {
			continue
		}
		if !strings.Contains(result.Text(), "summarized from runtime evidence") {
			exactBytes += len(result.Text())
		}
	}
	if exactBytes > options.ToolResultBudgetBytes {
		t.Fatalf("exact tool-result bytes = %d, over budget %d", exactBytes, options.ToolResultBudgetBytes)
	}
	if len(report.Elisions) == 0 {
		t.Fatal("old exact results were not summarized")
	}
}

func TestProjectionKeepsConflictingEvidenceExact(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "x.go"},
		Result: agent.Result{Text: "1\told", Details: map[string]any{"path": "x.go", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-2", ToolName: "read", Args: map[string]any{"path": "x.go"},
		Result: agent.Result{Text: "1\tnew", Details: map[string]any{"path": "x.go", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	messages := []agent.Message{
		agent.ToolResultMessage{ToolCallID: "read-1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "1\told"}}},
		agent.ToolResultMessage{ToolCallID: "read-2", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "1\tnew"}}},
		agent.ToolResultMessage{ToolCallID: "tail", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "tail"}}},
	}
	projected, report := projectMessages(messages, state, ProjectorOptions{KeepRecentToolResults: 1, ExactKeepBytes: 4096})
	if len(report.Elisions) != 0 {
		t.Fatalf("conflicting evidence must not be summarized, elisions = %#v", report.Elisions)
	}
	_ = projected
}

type testTool struct {
	name    string
	tier    agent.Tier
	classes []agent.ActionClass
	ops     []contract.ChangeOp
}

func (t testTool) Name() string        { return t.name }
func (t testTool) Description() string { return t.name }
func (t testTool) Parameters() *jsonschema.Schema {
	return jsonschema.MustCompile([]byte(`{"type":"object"}`))
}
func (t testTool) Approval() agent.Approval                           { return agent.Approval{Tier: t.tier} }
func (t testTool) Exclusive(map[string]any) bool                      { return false }
func (t testTool) Execute(agent.Context, map[string]any) agent.Result { return agent.Result{} }
func (t testTool) ActionClasses() []agent.ActionClass                 { return t.classes }
func (t testTool) MutationOperations() []contract.ChangeOp            { return t.ops }

func TestCandidatesHideMutationWhenShapeForbidsIt(t *testing.T) {
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "write", tier: agent.TierWrite, ops: []contract.ChangeOp{contract.ChangeCreate}},
		testTool{name: "bash", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}},
	}
	shape := BuildShape(contract.IntentContract{Kind: contract.ObjectiveInformational, Objective: "explain this project"}, ShapeParams{Authority: "ask"})
	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	set := Candidates(shape, NewEvidenceState(), ExecutionState{}, tools, RepositoryFacts{}, suff)
	var names []string
	for _, tool := range set.Tools {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "read" {
		t.Fatalf("candidates = %v, want only read", names)
	}
}

// A human-configured MCP capability stays in the action space for an ordinary
// code question. It is still external and still passes the security boundary;
// only the task-shape narrowing exempts it.
func TestCandidatesKeepConfiguredExternalTools(t *testing.T) {
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "web_fetch", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionExternal}},
		testTool{name: "mcp_read", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionExternal, agent.ActionConfigured}},
	}
	shape := BuildShape(contract.IntentContract{Kind: contract.ObjectiveInformational, Objective: "who calls parse"}, ShapeParams{Authority: "ask"})
	if shape.External.Required {
		t.Fatal("the test objective must not require web knowledge")
	}
	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	set := Candidates(shape, NewEvidenceState(), ExecutionState{}, tools, RepositoryFacts{}, suff)
	var names []string
	for _, tool := range set.Tools {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "read,mcp_read" {
		t.Fatalf("candidates = %v, want read,mcp_read (web_fetch stays hidden)", names)
	}
}

func TestCandidatesExcludeExistingOnlyToolsForCreateTarget(t *testing.T) {
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "write", tier: agent.TierWrite, ops: []contract.ChangeOp{contract.ChangeCreate, contract.ChangeModify}},
		testTool{name: "edit", tier: agent.TierWrite, classes: []agent.ActionClass{agent.ActionMutate, agent.ActionRequiresExisting}, ops: []contract.ChangeOp{contract.ChangeModify}},
		testTool{name: "delete", tier: agent.TierWrite, classes: []agent.ActionClass{agent.ActionMutate, agent.ActionRequiresExisting}, ops: []contract.ChangeOp{contract.ChangeDelete}},
	}
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	shape.Mutation.AbsentPaths = []string{"README.md"}
	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	set := Candidates(shape, NewEvidenceState(), ExecutionState{}, tools, RepositoryFacts{}, suff)
	var names []string
	for _, tool := range set.Tools {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "read,write" {
		t.Fatalf("candidates = %v, want read,write", names)
	}
}

func TestSufficiencyRequiresVerificationAfterMutation(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	evidence := NewEvidenceState()
	evidence.ensureRequirements(shape)
	evidence.ObserveOutcome(agent.ToolOutcome{
		CallID: "write-1", ToolName: "write", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{Text: "wrote", Mutates: true, Details: map[string]any{"path": "README.md", "bytes": 5}},
	})
	turns := []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "write-1", ToolName: "write", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{Text: "wrote", Mutates: true, Details: map[string]any{"path": "README.md", "bytes": 5}},
	}}}}
	execution := SummarizeExecution(turns)
	suff := EvaluateSufficiency(shape, evidence, execution, false)
	if suff.State != Insufficient || suff.Stage != StageVerify {
		t.Fatalf("sufficiency = %s/%s, want INSUFFICIENT/verify", suff.State, suff.Stage)
	}
	verifiedTurn := []agent.Turn{{Results: []agent.ToolOutcome{
		{CallID: "write-1", ToolName: "write", Result: agent.Result{Text: "wrote", Mutates: true, Details: map[string]any{"path": "README.md", "bytes": 5}}},
		{CallID: "bash-1", ToolName: "bash", Result: agent.Result{Text: "ok", Executes: true, Finished: true, Verifies: true, Verification: &agent.VerificationEvidence{Command: "go test ./...", Check: contract.CheckTest}}},
	}}}
	execution = SummarizeExecution(verifiedTurn)
	suff = EvaluateSufficiency(shape, evidence, execution, false)
	if suff.State != Sufficient || suff.Stage != StageComplete {
		t.Fatalf("sufficiency = %s/%s, want SUFFICIENT/complete", suff.State, suff.Stage)
	}
}

func TestSufficiencyBlocksMutationTheShapeDidNotPermit(t *testing.T) {
	// A read-only task whose run changed the repository is blocked even when
	// the workspace authority allowed the call. The shape describes the
	// objective, and the projection must not keep exploring as if nothing
	// happened.
	shape := BuildShape(contract.IntentContract{
		Objective: "Explain the project.",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask"})
	evidence := NewEvidenceState()
	execution := ExecutionState{Mutated: true, MutatedPaths: []string{"extra.txt"}}
	suff := EvaluateSufficiency(shape, evidence, execution, false)
	if suff.State != Blocked || suff.Stage != StageBlocked {
		t.Fatalf("sufficiency = %s/%s, want BLOCKED/blocked", suff.State, suff.Stage)
	}
	if len(suff.Reasons) == 0 {
		t.Fatal("a blocked verdict must explain the observed mutation")
	}
}

func TestRenderStateKeepsContractWhenEvidenceIsTruncated(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	evidence := NewEvidenceState()
	evidence.ensureRequirements(shape)
	for i := 0; i < 40; i++ {
		evidence.Items = append(evidence.Items, EvidenceItem{
			ID:        "file:noise" + itoa(i) + ".go",
			Status:    StatusKnown,
			Retention: RetentionExact,
			Summary:   strings.Repeat("noise ", 40),
			Seq:       i + 1,
		})
	}
	execution := ExecutionState{ToolCalls: 5}
	suff := EvaluateSufficiency(shape, evidence, execution, false)
	text := RenderState(shape, evidence, execution, suff, CandidateSet{}, 700, 40)
	for _, want := range []string{"Mutation scope", "Verification", "Completion", "Sufficiency", "Available actions"} {
		if !strings.Contains(text, want) {
			t.Fatalf("state block lost %q under truncation:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "runtime state truncated") {
		t.Fatalf("expected evidence truncation marker:\n%s", text)
	}
}

func TestRenderStateBoundsAndMarksExternalEvidence(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	evidence := NewEvidenceState()
	evidence.ensureRequirements(shape)
	evidence.Items = append(evidence.Items,
		EvidenceItem{ID: "file:README.md", Status: StatusKnown, Retention: RetentionExact, Summary: "README.md: absent", Seq: 1},
		EvidenceItem{ID: "external:x", Status: StatusKnown, Retention: RetentionExternal, Summary: "UNTRUSTED web.fetch from example.test: docs", Seq: 2},
		EvidenceItem{ID: "dir:.", Status: StatusConflicting, Retention: RetentionStructural, Summary: "directory . conflicting", Conflict: "changed twice", Seq: 3},
	)
	suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false)
	text := RenderState(shape, evidence, ExecutionState{}, suff, CandidateSet{}, 900, 5)
	if len(text) > 1200 {
		t.Fatalf("state block exceeds bound: %d", len(text))
	}
	for _, want := range []string{"UNTRUSTED", "Conflicting evidence", "Evidence missing"} {
		if !strings.Contains(text, want) {
			t.Fatalf("state block missing %q:\n%s", want, text)
		}
	}
}

func TestEvidenceTrimBoundsItemsAndBytes(t *testing.T) {
	state := NewEvidenceState()
	large := strings.Repeat("x", MaxEvidenceValueBytes)
	for i := 0; i < 20; i++ {
		state.ObserveOutcome(agent.ToolOutcome{
			CallID:   "read-" + itoa(i),
			ToolName: "read",
			Args:     map[string]any{"path": "file" + itoa(i) + ".txt"},
			Result: agent.Result{
				Text:    large,
				Details: map[string]any{"path": "file" + itoa(i) + ".txt", "totalLines": 1, "offset": 1, "limit": 2000},
			},
		})
	}
	if len(state.Items) > MaxEvidenceItems {
		t.Fatalf("items = %d, over cap %d", len(state.Items), MaxEvidenceItems)
	}
	if state.Bytes() > MaxEvidenceBytes {
		t.Fatalf("bytes = %d, over cap %d", state.Bytes(), MaxEvidenceBytes)
	}
}

func TestEvidenceObservationIsIdempotentPerCall(t *testing.T) {
	state := NewEvidenceState()
	turn := agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   "read-1",
		ToolName: "read",
		Args:     map[string]any{"path": "a.go"},
		Result:   agent.Result{Text: "1\tpackage a", Details: map[string]any{"path": "a.go", "totalLines": 1, "offset": 1, "limit": 2000}},
	}}}
	state.ObserveTurns(TaskShape{}, []agent.Turn{turn})
	before := len(state.Items)
	state.ObserveTurns(TaskShape{}, []agent.Turn{turn})
	if len(state.Items) != before {
		t.Fatalf("replayed turn added evidence: %d -> %d", before, len(state.Items))
	}
}

func TestSummarizeExecutionIgnoresRefusedCalls(t *testing.T) {
	// A denied action is a control decision, not evidence that the run failed.
	// Otherwise a read-only task that tried one forbidden call would look
	// permanently unrecoverable even after it answered correctly.
	turns := []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID:   "write-1",
		ToolName: "write",
		Result:   agent.Result{Text: "authorization denied", IsError: true},
		Authorization: &security.Decision{
			Outcome: security.OutcomeDeny,
			Rule:    "capability_not_permitted",
		},
	}}}}
	execution := SummarizeExecution(turns)
	if execution.FailedActions != 0 {
		t.Fatalf("failed actions = %d, want 0 for a refused call", execution.FailedActions)
	}
	if len(execution.PendingFailures) != 0 {
		t.Fatalf("pending failures = %#v, want none", execution.PendingFailures)
	}
	if execution.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want the refused attempt counted", execution.ToolCalls)
	}
}

func TestSummarizeExecutionRequiresVerificationAfterExecution(t *testing.T) {
	turns := []agent.Turn{{Results: []agent.ToolOutcome{
		{CallID: "bash-1", ToolName: "bash", Result: agent.Result{Text: "ok", Executes: true, Finished: true}},
		{CallID: "bash-2", ToolName: "bash", Result: agent.Result{Text: "ok", Executes: true, Finished: true, Verifies: true, Verification: &agent.VerificationEvidence{Command: "go test ./..."}}},
	}}}
	execution := SummarizeExecution(turns)
	if !execution.VerifiedAfterExecution {
		t.Fatalf("execution summary = %#v, want verified after execution", execution)
	}
	reversed := SummarizeExecution([]agent.Turn{{Results: []agent.ToolOutcome{
		{CallID: "bash-2", ToolName: "bash", Result: agent.Result{Text: "ok", Executes: true, Finished: true, Verifies: true, Verification: &agent.VerificationEvidence{Command: "go test ./..."}}},
		{CallID: "bash-1", ToolName: "bash", Result: agent.Result{Text: "ok", Executes: true, Finished: true}},
	}}})
	if reversed.VerifiedAfterExecution {
		t.Fatalf("a check before the execution must not verify it: %#v", reversed)
	}
}

func TestCandidatesWithholdEverythingWhenProofComplete(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	tools := []agent.Tool{testTool{name: "read", tier: agent.TierRead}}
	suff := Sufficiency{State: Sufficient, Stage: StageComplete}
	set := Candidates(shape, NewEvidenceState(), ExecutionState{Mutated: true}, tools, RepositoryFacts{}, suff)
	if len(set.Tools) != 0 {
		t.Fatalf("complete stage offered tools: %#v", set.Tools)
	}
	if len(set.Excluded) == 0 {
		t.Fatal("complete stage must explain why the tool was withheld")
	}
}

func TestCandidatesBlockMutationButKeepVerification(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "write", tier: agent.TierWrite, ops: []contract.ChangeOp{contract.ChangeCreate}},
		testTool{name: "bash", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}},
		testTool{name: "mcp_call", tier: agent.TierWrite, classes: []agent.ActionClass{
			agent.ActionExternal, agent.ActionConfigured, agent.ActionMutate, agent.ActionExecute,
		}},
	}
	suff := Sufficiency{State: Blocked, Stage: StageBlocked}
	set := Candidates(shape, NewEvidenceState(), ExecutionState{}, tools, RepositoryFacts{}, suff)
	var names []string
	for _, tool := range set.Tools {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "read,bash" {
		t.Fatalf("blocked candidates = %v, want read,bash", names)
	}
}

func TestProjectorSeparatesBudgetFromEvidenceSufficiency(t *testing.T) {
	// A spent grant withholds tools, but it is not an evidence conflict that
	// should veto a completion already proven by the run.
	shape := BuildShape(contract.IntentContract{
		Objective: "Explain this.",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask"})
	shape.Budget = BudgetShape{MaxTurns: 1, TurnsUsed: 1}
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{})
	result := projector.Project(agent.ProjectionRequest{
		Tools: []agent.Tool{testTool{name: "read", tier: agent.TierRead}},
	})
	if result.Report.Sufficiency == string(Blocked) {
		t.Fatalf("budget exhaustion was reported as evidence BLOCKED: %#v", result.Report)
	}
	if len(result.Tools) != 0 {
		t.Fatalf("an exhausted grant offered tools: %#v", result.Tools)
	}
	if len(result.Report.Excluded) == 0 || !strings.Contains(result.Report.Excluded[0].Reason, "grant is exhausted") {
		t.Fatalf("exhausted grant was not explained: %#v", result.Report.Excluded)
	}
}

func TestProjectorReportsEvidenceAndElidesOldResults(t *testing.T) {
	shape := BuildShape(readContract("Write README.md."), ShapeParams{Authority: "build"})
	evidence := NewEvidenceState()
	projector := NewProjector(shape, evidence, RepositoryFacts{})
	messages := []agent.Message{
		agent.UserMessage{Text: "write README.md"},
		agent.ToolResultMessage{ToolCallID: "ls-1", ToolName: "ls", Content: []ai.ContentBlock{ai.TextContent{Text: "a\nb"}}},
		agent.ToolResultMessage{ToolCallID: "read-1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: "exact"}}},
	}
	result := projector.Project(agent.ProjectionRequest{
		Messages: messages,
		Tools:    []agent.Tool{testTool{name: "read", tier: agent.TierRead}},
		Turns: []agent.Turn{{Results: []agent.ToolOutcome{{
			CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
			Result: agent.Result{Text: "a\nb", Details: map[string]any{"path": ".", "entries": 2}},
		}}}},
	})
	if !result.Report.Applied {
		t.Fatal("projector did not report application")
	}
	if result.Report.Shape == "" || result.Report.Evidence == "" {
		t.Fatalf("report is missing shape/evidence: %#v", result.Report)
	}
	if len(result.System) != 1 || !strings.Contains(result.System[0].Text, "Runtime state") {
		t.Fatalf("system block = %#v", result.System)
	}
}

func TestEvidenceRoundTripsThroughJSON(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "a.go"},
		Result: agent.Result{Text: "1\tpackage a", Details: map[string]any{"path": "a.go", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EvidenceState
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.Find("file:a.go"); !ok {
		t.Fatal("file evidence did not round-trip")
	}
}

// A configured MCP call tool is dual-use: the same wire tool may run an
// execute-class remote operation or an interaction/mutation. Candidate
// selection must keep it for FUZZ execution and BUILD mutation, while ASK
// read-only still withholds it.
func TestCandidatesKeepConfiguredMCPForExecutionAndMutation(t *testing.T) {
	mcpCall := testTool{name: "mcp_call", tier: agent.TierWrite, classes: []agent.ActionClass{
		agent.ActionExternal, agent.ActionConfigured, agent.ActionMutate, agent.ActionExecute,
	}}
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		mcpCall,
	}
	names := func(shape TaskShape) string {
		suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
		set := Candidates(shape, NewEvidenceState(), ExecutionState{}, tools, RepositoryFacts{}, suff)
		var out []string
		for _, tool := range set.Tools {
			out = append(out, tool.Name())
		}
		return strings.Join(out, ",")
	}

	fuzz := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveExecution,
		Objective: "run the tests to reproduce the failure",
	}, ShapeParams{Authority: "fuzz"})
	if got := names(fuzz); !strings.Contains(got, "mcp_call") {
		t.Fatalf("FUZZ execution candidates = %q, want mcp_call", got)
	}

	build := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveMutation,
		Objective: "write README.md",
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "README.md"}},
	}, ShapeParams{Authority: "build"})
	if got := names(build); !strings.Contains(got, "mcp_call") {
		t.Fatalf("BUILD mutation candidates = %q, want mcp_call", got)
	}

	ask := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "explain this project",
	}, ShapeParams{Authority: "ask"})
	if got := names(ask); strings.Contains(got, "mcp_call") {
		t.Fatalf("ASK informational candidates = %q, want mcp_call withheld", got)
	}
}
