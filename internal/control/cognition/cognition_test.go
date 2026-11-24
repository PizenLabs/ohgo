package cognition

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/jsonschema"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
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

// BUILD + a mutation objective must describe mutation as allowed. This is the
// runtime-state guard that kept "redesign it" from becoming a design-only turn:
// the contract is a change objective, so the shape cannot report
// "mutation=forbidden" while BUILD is the active authority.
func TestBuildShapeAllowsMutationForAReworkObjectiveInBuild(t *testing.T) {
	intent := workspace.BuildIntentContract("redesign it")
	if intent.Kind != contract.ObjectiveMutation {
		t.Fatalf("contract kind = %s, want mutation", intent.Kind)
	}
	shape := BuildShape(intent, ShapeParams{Authority: "build"})
	if !shape.Mutation.Allowed || !shape.Authority.MutationAllowed || shape.Authority.ReadOnly {
		t.Fatalf("shape = %+v, want mutation allowed under BUILD", shape)
	}
	if strings.Contains(shape.Summary(), "mutation=forbidden") {
		t.Fatalf("summary = %q, want the allowed mutation scope", shape.Summary())
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

// "The README" is a documentation reference, not an exact extensionless
// filename. The read observation records README.md, so the requirement must
// resolve by document name; otherwise target_content:README can never be
// satisfied and the run keeps re-reading the same document.
func TestBareDocumentTargetResolvesToTheObservedREADME(t *testing.T) {
	const objective = `Investigate this project and analyze what this project is about.

Distinguish:
- Facts documented by the README.
- Architecture verified from source code.
- Runtime behavior verified from implementation.
- Features that are documented but not independently verified.`
	shape := BuildShape(workspace.BuildIntentContract(objective), ShapeParams{Authority: "fuzz", Intent: "investigate"})
	var stateKey, contentKey bool
	for _, need := range shape.RequiredKnowledge {
		switch need.Key {
		case "target_state:README":
			stateKey = true
		case "target_content:README":
			contentKey = true
		}
	}
	if !stateKey || !contentKey {
		t.Fatalf("required knowledge = %#v, want target_state:README and target_content:README", shape.RequiredKnowledge)
	}

	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{
			Text:    "1\t# go-ecommerce\n2\t\n3\tGoCommerce is an e-commerce application.",
			Details: map[string]any{"path": "README.md", "totalLines": 3, "offset": 1, "limit": 2000},
		},
	})
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-2", ToolName: "read", Args: map[string]any{"path": "docs/Readme.md"},
		Result: agent.Result{
			Text:    "1\tNested documentation.",
			Details: map[string]any{"path": "docs/Readme.md", "totalLines": 1, "offset": 1, "limit": 2000},
		},
	})
	for _, key := range []string{"target_state:README", "target_content:README"} {
		if !state.Satisfied(key, ExecutionState{}) {
			t.Fatalf("%s was not satisfied by reading README.md", key)
		}
	}

	other := NewEvidenceState()
	other.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-3", ToolName: "read", Args: map[string]any{"path": "config.md"},
		Result: agent.Result{
			Text:    "1\tA different document.",
			Details: map[string]any{"path": "config.md", "totalLines": 1, "offset": 1, "limit": 2000},
		},
	})
	if other.Satisfied("target_content:README", ExecutionState{}) {
		t.Fatal("an unrelated documentation file satisfied the README target")
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
	// A missing file is target-state evidence, not content. Reading a path that
	// does not exist must not make an unobserved repository question answerable.
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-missing", ToolName: "read", Args: map[string]any{"path": "missing.html"},
		Result: agent.Result{
			Text: `stat "missing.html": no such file or directory`, IsError: true,
			ObservedAbsence: true, Details: map[string]any{"path": "missing.html"},
		},
	})
	if state.Satisfied("repository_overview", ExecutionState{}) {
		t.Fatal("a missing-file observation satisfied a repository overview")
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

// A tool result's wire form is a function of the result alone. This is the
// cache contract the projection exists to serve: the provider cached the bytes
// of a message when it was first sent, so a later turn must not re-render it
// because the conversation grew.
func TestProjectionBoundsEachResultIndependentOfHistory(t *testing.T) {
	structural := NewEvidenceState()
	structural.ObserveOutcome(agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
		Result: agent.Result{Text: "a\nb\nc", Details: map[string]any{"path": ".", "entries": 3}},
	})
	head := []agent.Message{
		agent.UserMessage{Text: "check"},
		agent.ToolResultMessage{ToolCallID: "ls-1", ToolName: "ls", Content: []ai.ContentBlock{ai.TextContent{Text: "a\nb\nc"}}},
	}
	exact := strings.Repeat("x", 2048)
	state := structural
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "file.go"},
		Result: agent.Result{Text: exact, Details: map[string]any{"path": "file.go", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	read := agent.ToolResultMessage{ToolCallID: "read-1", ToolName: "read", Content: []ai.ContentBlock{ai.TextContent{Text: exact}}}

	early, earlyReport := projectMessages(append(append([]agent.Message(nil), head...), read), state, ProjectorOptions{})
	later := append(append([]agent.Message(nil), head...), read)
	for i := range 12 {
		filler := agent.ToolResultMessage{
			ToolCallID: "filler-" + itoa(i), ToolName: "grep",
			Content: []ai.ContentBlock{ai.TextContent{Text: strings.Repeat("y", 512)}},
		}
		later = append(later, filler)
	}
	late, lateReport := projectMessages(later, state, ProjectorOptions{})

	earlyRead, _ := early[2].(agent.ToolResultMessage)
	lateRead, _ := late[2].(agent.ToolResultMessage)
	if earlyRead.Text() != lateRead.Text() {
		t.Fatalf("an already-sent result was re-rendered as the history grew:\n early %d bytes\n late  %d bytes",
			len(earlyRead.Text()), len(lateRead.Text()))
	}
	first, _ := early[1].(agent.ToolResultMessage)
	if first.Text() != "a\nb\nc" {
		t.Fatalf("small structural result lost its coordinates: %q", first.Text())
	}
	if earlyRead.Text() != exact {
		t.Fatalf("a result inside the exact bound was rewritten: %d bytes", len(earlyRead.Text()))
	}
	if len(earlyReport.Elisions) != 0 {
		t.Fatalf("elisions = %d, want both bounded results kept exact", len(earlyReport.Elisions))
	}
	if lateRead.Text() != earlyRead.Text() {
		t.Fatalf("the exact result changed once more history arrived")
	}
	if len(lateReport.Elisions) < len(earlyReport.Elisions) {
		t.Fatalf("appending history reduced elisions: %d < %d", len(lateReport.Elisions), len(earlyReport.Elisions))
	}
}

// A structural result larger than the per-result bound is excerpted, not
// reduced to a summary that discards every coordinate. The cut is stable across
// later history, so the append-only cache contract still holds.
func TestLargeStructuralResultIsExcerptedWithCoordinates(t *testing.T) {
	state := NewEvidenceState()
	large := "backend/productcatalog/app/product.go:586: func (ps ProductService) Add(...)\n" +
		strings.Repeat("match line\n", 20)
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "grep-1", ToolName: "grep", Args: map[string]any{"pattern": "Add"},
		Result: agent.Result{Text: large, Details: map[string]any{"pattern": "Add", "files": 1}},
	})
	messages := []agent.Message{
		agent.UserMessage{Text: "trace"},
		agent.ToolResultMessage{ToolCallID: "grep-1", ToolName: "grep", Content: []ai.ContentBlock{ai.TextContent{Text: large}}},
	}
	early, _ := projectMessages(messages, state, ProjectorOptions{StructuralKeepBytes: 64})
	result, _ := early[1].(agent.ToolResultMessage)
	if !strings.Contains(result.Text(), "structural observation excerpted") {
		t.Fatalf("large structural result was not excerpted: %q", result.Text())
	}
	if !strings.Contains(result.Text(), "product.go:586") {
		t.Fatalf("excerpt discarded the coordinate prefix: %q", result.Text())
	}
	later := append(append([]agent.Message(nil), messages...), agent.ToolResultMessage{
		ToolCallID: "filler", ToolName: "grep", Content: []ai.ContentBlock{ai.TextContent{Text: strings.Repeat("y", 512)}},
	})
	late, _ := projectMessages(later, state, ProjectorOptions{StructuralKeepBytes: 64})
	lateResult, _ := late[1].(agent.ToolResultMessage)
	if early[1].(agent.ToolResultMessage).Text() != lateResult.Text() {
		t.Fatal("the excerpt changed when more history arrived")
	}
}

// Aggregate growth is compaction's job. Per-request re-elision used to shrink
// the request by rewriting messages the provider had already cached, which
// traded a bounded token saving for a full prefix invalidation.
func TestProjectionDoesNotRewriteOversizedResultsPerRequest(t *testing.T) {
	state := NewEvidenceState()
	messages := []agent.Message{agent.UserMessage{Text: "read several files"}}
	options := ProjectorOptions{}
	for i := range 5 {
		path := "file" + itoa(i) + ".go"
		text := strings.Repeat("x", options.normalized().ExactKeepBytes*2)
		state.ObserveOutcome(agent.ToolOutcome{
			CallID: "read-" + itoa(i), ToolName: "read", Args: map[string]any{"path": path},
			Result: agent.Result{Text: text, Details: map[string]any{"path": path, "totalLines": 1, "offset": 1, "limit": 2000}},
		})
		messages = append(messages, agent.ToolResultMessage{
			ToolCallID: "read-" + itoa(i), ToolName: "read",
			Content: []ai.ContentBlock{ai.TextContent{Text: text}},
		})
	}
	first, _ := projectMessages(messages, state, options)
	second, _ := projectMessages(messages, state, options)
	for i := range first {
		left, leftOK := first[i].(agent.ToolResultMessage)
		right, rightOK := second[i].(agent.ToolResultMessage)
		if leftOK != rightOK {
			t.Fatalf("message %d changed kind between identical projections", i)
		}
		if leftOK && left.Text() != right.Text() {
			t.Fatalf("message %d is not a pure function of its evidence", i)
		}
	}
	for _, message := range first {
		result, ok := message.(agent.ToolResultMessage)
		if !ok {
			continue
		}
		if len(result.Text()) > options.normalized().ExactKeepBytes+256 {
			t.Fatalf("result = %d bytes, want it bounded by the per-result cap", len(result.Text()))
		}
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
	projected, report := projectMessages(messages, state, ProjectorOptions{ExactKeepBytes: 4096})
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

// planTestTool is a plan owner. It exists so the candidate and render tests can
// exercise agent.PlanReporter without importing the tool package.
type planTestTool struct {
	testTool
	open []string
}

func (t planTestTool) OpenPlanItems() []string { return t.open }

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

// A read-only authority cannot execute. Offering a shell in an informational
// phase makes the model propose a call the gate must deny, which then looks
// like a failed investigation. The action surface must describe the authority.
func TestCandidatesWithholdExecutionWhenAuthorityCannotExecute(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Run the tests.",
	}, ShapeParams{Authority: "ask"})
	if shape.Authority.ExecutionAllowed {
		t.Fatal("an ASK shape must not permit execution")
	}
	if !shape.NeedsExecution {
		t.Fatal("the objective still names execution; the test must exercise the authority gate")
	}
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "bash", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}},
	}
	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	set := Candidates(shape, NewEvidenceState(), ExecutionState{}, tools, RepositoryFacts{}, suff)
	var names []string
	for _, tool := range set.Tools {
		names = append(names, tool.Name())
	}
	if strings.Join(names, ",") != "read" {
		t.Fatalf("candidates = %v, want only read under ASK", names)
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

// Once a repository overview question has the evidence its contract requires,
// the next request is for the answer: every observation tool is withheld.
func TestRepositoryOverviewAtStageAnswerWithholdsAllTools(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}, ShapeParams{Authority: "ask"})
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "ls", tier: agent.TierRead},
	}

	evidence := NewEvidenceState()
	for _, path := range []string{"README.md", "go.mod"} {
		evidence.ObserveOutcome(agent.ToolOutcome{
			CallID: "read-" + path, ToolName: "read", Args: map[string]any{"path": path},
			Result: agent.Result{
				Text: "1\tcontent", Details: map[string]any{
					"path": path, "totalLines": 1, "offset": 1, "limit": 2000,
				},
			},
		})
	}
	suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false)
	if suff.Stage != StageAnswer {
		t.Fatalf("sufficiency stage = %s, want answer", suff.Stage)
	}
	set := Candidates(shape, evidence, ExecutionState{}, tools, RepositoryFacts{}, suff)
	if len(set.Tools) != 0 {
		t.Fatalf("offered %d tool(s) after sufficient evidence: %+v", len(set.Tools), set.Tools)
	}
	if len(set.Excluded) == 0 || !strings.Contains(set.Excluded[0].Reason, "answer without further tools") {
		t.Fatalf("the answer boundary was not explained: %+v", set.Excluded)
	}
}

// The answer boundary closes observation, not plan bookkeeping. Withdrawing the
// plan tool left an open plan frozen after the model had answered, and the
// durable task then refused completion for work the model could no longer mark.
func TestAnswerBoundaryKeepsThePlanTool(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}, ShapeParams{Authority: "ask"})
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		planTestTool{
			testTool: testTool{name: "todo", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionPlan}},
			open:     []string{"trace the pipeline"},
		},
	}

	evidence := NewEvidenceState()
	for _, path := range []string{"README.md", "go.mod"} {
		evidence.ObserveOutcome(agent.ToolOutcome{
			CallID: "read-" + path, ToolName: "read", Args: map[string]any{"path": path},
			Result: agent.Result{
				Text: "1\tcontent", Details: map[string]any{
					"path": path, "totalLines": 1, "offset": 1, "limit": 2000,
				},
			},
		})
	}
	suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false)
	if suff.Stage != StageAnswer {
		t.Fatalf("sufficiency stage = %s, want answer", suff.Stage)
	}
	set := Candidates(shape, evidence, ExecutionState{}, tools, RepositoryFacts{}, suff)
	if len(set.Tools) != 1 || set.Tools[0].Name() != "todo" {
		t.Fatalf("offered tools = %+v, want only the plan tool", set.Tools)
	}
}

// The runtime state names the open plan and the one action left at the answer
// boundary. A model that only reads "answer from the evidence above" cannot
// know the durable task will refuse completion for the plan it left open.
func TestAnswerStateReportsOpenPlanAndReconciliation(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "check this project and analyze what this project is about?",
	}, ShapeParams{Authority: "ask"})
	evidence := NewEvidenceState()
	evidence.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-README.md", ToolName: "read", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{
			Text: "1\tcontent", Details: map[string]any{
				"path": "README.md", "totalLines": 1, "offset": 1, "limit": 2000,
			},
		},
	})
	suff := Sufficiency{State: Sufficient, Stage: StageAnswer}
	plan := planTestTool{
		testTool: testTool{name: "todo", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionPlan}},
		open:     []string{"trace the pipeline", "check the tests"},
	}
	rendered := RenderState(shape, evidence, ExecutionState{}, suff,
		CandidateSet{Tools: []agent.Tool{plan}}, 0, 0)
	if !strings.Contains(rendered, "Plan: 2 open item(s)") ||
		!strings.Contains(rendered, "trace the pipeline") ||
		!strings.Contains(rendered, "Mark each done, or remove it if you will not do it") {
		t.Fatalf("state = %q, want the open plan and the reconciliation rule", rendered)
	}
	if !strings.Contains(rendered, "Next action: reconcile the plan, then answer") {
		t.Fatalf("state = %q, want reconciliation named as the next action", rendered)
	}

	reconciled := plan
	reconciled.open = nil
	rendered = RenderState(shape, evidence, ExecutionState{}, suff,
		CandidateSet{Tools: []agent.Tool{reconciled}}, 0, 0)
	if !strings.Contains(rendered, "The plan is reconciled and no evidence tool is available") {
		t.Fatalf("state = %q, want the answer boundary after a reconciled plan", rendered)
	}
}

// A question that is not about this repository must keep its tools even though
// it has no repository evidence requirement to satisfy.
func TestNonRepositoryInformationalQuestionKeepsToolsAtStageAnswer(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "who calls parse",
	}, ShapeParams{Authority: "ask"})
	if shape.RepositoryScope {
		t.Fatal("the test objective must not be repository-scoped")
	}
	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	set := Candidates(shape, NewEvidenceState(), ExecutionState{},
		[]agent.Tool{testTool{name: "read", tier: agent.TierRead}}, RepositoryFacts{}, suff)
	if len(set.Tools) != 1 {
		t.Fatalf("offered %d tool(s), want the read tool", len(set.Tools))
	}
}

func TestExplorationObjectiveNeedsStructureAndExternalContext(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Help me explore this project using Gry MCP",
	}, ShapeParams{Authority: "ask"})
	if !shape.NeedsCodeNavigation {
		t.Error("an explicit Gry/MCP exploration request must enable structural navigation")
	}
	if !shape.NeedsRelationshipNavigation {
		t.Error("an exploration request must enable relationship navigation")
	}
	if !shape.External.Required {
		t.Error("an explicit Gry/MCP request must require external context")
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

// A mutation scoped to a concrete path already has the coordinate it needs.
// Asking for a separate symbol lookup adds a tool call without changing the
// edit boundary; completion still re-resolves the symbol after the change.
func TestPathScopedMutationDoesNotRequireRedundantCoordinateLookup(t *testing.T) {
	intent := contract.IntentContract{
		Kind:      contract.ObjectiveMutation,
		Objective: "Fix GrepTool in tools/grep.go and run go test ./tools",
		Targets: []contract.Target{
			{Kind: contract.TargetPath, Value: "tools/grep.go"},
			{Kind: contract.TargetSymbol, Value: "GrepTool"},
		},
	}
	scoped := BuildShape(intent, ShapeParams{Authority: "build"})
	if hasNeed(scoped, "target_coordinates:GrepTool") {
		t.Fatalf("a path-scoped mutation still demanded a coordinate lookup: %+v", scoped.RequiredKnowledge)
	}
	if !hasNeed(scoped, "target_state:tools/grep.go") {
		t.Fatalf("the edit boundary was lost: %+v", scoped.RequiredKnowledge)
	}

	targetless := intent
	targetless.Targets = []contract.Target{{Kind: contract.TargetSymbol, Value: "GrepTool"}}
	lookup := BuildShape(targetless, ShapeParams{Authority: "build"})
	if !hasNeed(lookup, "target_coordinates:GrepTool") {
		t.Fatalf("a targetless mutation lost the coordinate lookup: %+v", lookup.RequiredKnowledge)
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

// A runtime objective is not complete because a command finished. The
// environment probe is Executed evidence, but only the live service that
// reached readiness and answered a check is Runtime evidence. Reproduces the
// "run this project" trace where "node -v && npm -v" was accepted as execution
// and the tool surface closed before the project ever ran.
func TestSufficiencyRequiresLiveRuntimeObservation(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective:     "run this project help me",
		Kind:          contract.ObjectiveExecution,
		ExecutionMode: contract.ExecutionRuntime,
	}, ShapeParams{Authority: "build"})
	if !hasCompletionCondition(shape, "runtime_observed") {
		t.Fatalf("runtime completion condition missing: %+v", shape.Completion)
	}

	evidence := NewEvidenceState()
	evidence.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "probe-1", ToolName: "bash",
		Result: agent.Result{Text: "v24.11.0\n11.6.1", Executes: true, Finished: true,
			Details: map[string]any{"command": "node -v && npm -v", "exitCode": 0}},
	}}}})
	execution := ExecutionFromEvidence(evidence)
	if !execution.Executed {
		t.Fatal("the probe was not observed as execution")
	}
	if execution.RuntimeObserved {
		t.Fatal("a finished command was treated as a live runtime")
	}
	suff := EvaluateSufficiency(shape, evidence, execution, false)
	if suff.State != Insufficient || suff.Stage != StageAct {
		t.Fatalf("sufficiency = %s/%s, want INSUFFICIENT/act", suff.State, suff.Stage)
	}
	if !containsString(suff.Missing, "runtime_observation") {
		t.Fatalf("missing = %v, want runtime_observation", suff.Missing)
	}

	evidence.ObserveTurns(shape, []agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "verify-1", ToolName: "verify",
		Result: agent.Result{
			Text: "verification: PASS", Executes: true, Finished: true, Verifies: true,
			Verification: &agent.VerificationEvidence{Command: "npm run dev"},
			Runtime:      &agent.RuntimeEvidence{Command: "npm run dev", URL: "http://127.0.0.1:3000", PID: 99, Checks: 1},
			Details:      map[string]any{"verification": map[string]any{"status": "PASS"}},
		},
	}}}})
	execution = ExecutionFromEvidence(evidence)
	if !execution.RuntimeObserved {
		t.Fatal("the live runtime observation was not projected")
	}
	suff = EvaluateSufficiency(shape, evidence, execution, false)
	if suff.State != Sufficient || suff.Stage != StageComplete {
		t.Fatalf("sufficiency after runtime observation = %s/%s, want SUFFICIENT/complete", suff.State, suff.Stage)
	}

	// A complete lifecycle is an answer-only surface. The candidate layer
	// normally withdraws every tool, and the projection must also say so
	// independently: a continuation call can never be dispatched once the
	// objective's evidence is complete.
	report := NewProjector(shape, evidence, RepositoryFacts{}).Project(agent.ProjectionRequest{
		Turn:     3,
		Tools:    []agent.Tool{testTool{name: "verify", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute}}},
		Messages: []agent.Message{agent.UserMessage{Text: "run this project help me"}},
	})
	if !report.Report.AnswerEligible {
		t.Fatalf("a complete runtime objective was not answer-eligible: %+v", report.Report)
	}
	if len(report.Tools) != 0 {
		t.Fatalf("a complete runtime objective still offered tools: %v", toolNames(report.Tools))
	}

	// The zero mode keeps the weaker one-shot rule: a completed command is
	// still execution evidence for "run the tests" or "run the build".
	command := BuildShape(contract.IntentContract{
		Objective: "run go build ./...", Kind: contract.ObjectiveExecution,
	}, ShapeParams{Authority: "build"})
	commandSuff := EvaluateSufficiency(command, evidence, execution, false)
	if commandSuff.Stage != StageComplete {
		t.Fatalf("command objective stage = %s, want complete", commandSuff.Stage)
	}
}

func hasCompletionCondition(shape TaskShape, key string) bool {
	for _, condition := range shape.Completion {
		if condition.Key == key {
			return true
		}
	}
	return false
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

// A long investigation can produce more diagnostic command output than the
// evidence state can retain. Trimming must not evict the file reads and
// directory listing that satisfy the repository needs while keeping dozens of
// transient command logs. The observed failure did exactly that: after 88 shell
// probes the state still had 42 diagnostic items but no content observation, so
// sufficiency reported the investigation as incomplete and the answer boundary
// never opened.
func TestEvidenceTrimKeepsContentThatSatisfiesRepositoryNeeds(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "investigate the bottleneck and evaluate in this project",
	}, ShapeParams{Authority: "fuzz"})
	if !shape.RepositoryScope || !shape.BroadRepositoryAnalysis {
		t.Fatalf("test shape = repository %t, broad %t; want both",
			shape.RepositoryScope, shape.BroadRepositoryAnalysis)
	}

	state := NewEvidenceState()
	state.ensureRequirements(shape)
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "."},
		Result: agent.Result{
			Text:    "index.html\nstyles.css\nscript.js",
			Details: map[string]any{"path": ".", "entries": 3},
		},
	})
	for _, path := range []string{"index.html", "styles.css", "script.js"} {
		state.ObserveOutcome(agent.ToolOutcome{
			CallID: "read-" + path, ToolName: "read", Args: map[string]any{"path": path},
			Result: agent.Result{
				Text: path + " content",
				Details: map[string]any{
					"path": path, "totalLines": 1, "offset": 1, "limit": 2000,
				},
			},
		})
	}
	for i := 0; i < 120; i++ {
		command := "python3 -c 'audit " + itoa(i) + "'"
		state.ObserveOutcome(agent.ToolOutcome{
			CallID: "bash-" + itoa(i), ToolName: "bash", Args: map[string]any{"command": command},
			Result: agent.Result{
				Text:     strings.Repeat("diagnostic ", 800),
				Details:  map[string]any{"command": command, "exitCode": 0},
				Executes: true, Finished: true,
			},
		})
	}

	if !state.Satisfied("repository_overview", ExecutionState{}) {
		t.Fatal("repository_overview was lost when diagnostic output filled the evidence bound")
	}
	if !state.Satisfied("repository_survey", ExecutionState{}) {
		t.Fatal("repository_survey was lost when diagnostic output filled the evidence bound")
	}
	// The command observations are durable evidence; reconstruct the execution
	// state they imply rather than testing sufficiency against an empty run.
	suff := EvaluateSufficiency(shape, state, ExecutionFromEvidence(state), false)
	if suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("sufficiency = %s/%s, want SUFFICIENT/answer after a long diagnostic run",
			suff.State, suff.Stage)
	}
	if got := state.Bytes(); got > MaxEvidenceBytes {
		t.Fatalf("evidence bytes = %d, over cap %d", got, MaxEvidenceBytes)
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

// A command that ran and exited non-zero is an observation, not a capability
// failure. It must not enter the durable failure ledger (which would tell the
// model "do not repeat an identical call") and must not count as a failed
// action. A failed check keeps the opposite behaviour.
func TestCommandExitStatusIsNotACapabilityFailure(t *testing.T) {
	observation := agent.ToolOutcome{
		CallID: "bash-1", ToolName: "bash",
		Args: map[string]any{"command": "git status --short && git log --oneline -5"},
		Result: agent.Result{
			Text:    "fatal: your current branch 'main' does not have any commits yet\n[exit code 128]",
			IsError: true, Executes: true, Finished: true,
			FailureClass: agent.ToolFailureCommand,
		},
	}
	state := NewEvidenceState()
	state.ObserveOutcome(observation)
	if records := state.FailureState().Records; len(records) != 0 {
		t.Fatalf("a completed command entered the failure ledger: %+v", records)
	}
	execution := SummarizeExecution([]agent.Turn{{Results: []agent.ToolOutcome{observation}}})
	if execution.FailedActions != 0 || len(execution.PendingFailures) != 0 {
		t.Fatalf("execution = %+v, want no failed action for a command result", execution)
	}

	failedCheck := observation
	failedCheck.Result.FailureClass = agent.ToolFailureVerification
	failedCheck.Result.Text = "--- FAIL: TestX\n[exit code 1]"
	failed := NewEvidenceState()
	failed.ObserveOutcome(failedCheck)
	if records := failed.FailureState().Records; len(records) != 1 {
		t.Fatalf("a failed check did not enter the failure ledger: %+v", records)
	}
	checkExecution := SummarizeExecution([]agent.Turn{{Results: []agent.ToolOutcome{failedCheck}}})
	if checkExecution.FailedActions != 1 || checkExecution.PendingFailures["bash"] != 1 {
		t.Fatalf("execution = %+v, want the failed check counted", checkExecution)
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
	if len(result.Runtime) != 1 || !strings.Contains(result.Runtime[0], "Runtime state") {
		t.Fatalf("runtime block = %#v", result.Runtime)
	}
}

// The workspace probe is already measured. Surface it in the runtime state so
// the model does not spend a tool call rediscovering language or manifests.
func TestRenderStateCarriesWorkspaceProbe(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "explain this project",
	}, ShapeParams{
		Authority: "ask",
		Facts: RepositoryFacts{
			Go: true, Make: true, RootFiles: 7, Markers: []string{"Makefile", "go.mod"},
		},
	})
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": "README.md"},
		Result: agent.Result{Text: "project", Details: map[string]any{"path": "README.md", "totalLines": 1, "offset": 1, "limit": 2000}},
	})
	rendered := RenderState(shape, state, SummarizeExecution(nil),
		Sufficiency{State: Sufficient, Stage: StageAnswer}, CandidateSet{}, 0, 0)
	for _, want := range []string{"Workspace probe", "7 root entries", "go.mod", "Go"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("state block missing workspace fact %q:\n%s", want, rendered)
		}
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

// A directory-scoped target's content is its complete listing. This is the
// counterpart of TestInformationalContentQuestionNeedsAFileRead: a file still
// needs its bytes, but "inspect backend/" is answered by the directory itself.
func TestDirectoryContentIsItsCompleteListing(t *testing.T) {
	state := NewEvidenceState()
	state.ObserveOutcome(agent.ToolOutcome{
		CallID: "ls-1", ToolName: "ls", Args: map[string]any{"path": "backend"},
		Result: agent.Result{
			Text:    "cmd\ninternal\npkg\ngo.mod",
			Details: map[string]any{"path": "backend", "entries": 4},
		},
	})
	if !state.Satisfied("target_content:backend", ExecutionState{}) {
		t.Fatal("a complete directory listing did not satisfy directory content")
	}

	truncated := NewEvidenceState()
	truncated.ObserveOutcome(agent.ToolOutcome{
		CallID: "ls-2", ToolName: "ls", Args: map[string]any{"path": "backend"},
		Result: agent.Result{
			Text:    "cmd\ninternal\n[showing 2 of 10 entries]",
			Details: map[string]any{"path": "backend", "entries": 10},
		},
	})
	if truncated.Satisfied("target_content:backend", ExecutionState{}) {
		t.Fatal("a truncated directory listing satisfied directory content")
	}
}

// A whole-change prohibition is a contraction in ordinary speech. The shape
// must still withhold mutation; otherwise a contradictory request reaches the
// write surface because the router saw a mutation verb first.
func TestBuildShapeHonorsAContractedNoMutationLimit(t *testing.T) {
	c := contract.IntentContract{
		Objective: "Fix the parser. Actually don't change anything; explain the current behavior.",
		Kind:      contract.ObjectiveMutation,
		Targets:   []contract.Target{{Kind: contract.TargetPath, Value: "parser.go"}},
	}
	shape := BuildShape(c, ShapeParams{Authority: "build"})
	if shape.Mutation.Allowed {
		t.Fatal("a contracted no-mutation limit left mutation allowed")
	}
	if shape.Authority.MutationAllowed {
		t.Fatal("a contracted no-mutation limit left mutation permitted")
	}
}

// Equivalent evidence must serialize identically: the runtime state block is
// part of the request, and two builds of the same semantic state that differ
// would invalidate the provider's cache without any state having changed.
func TestRenderStateIsDeterministicForEquivalentEvidence(t *testing.T) {
	build := func() (*EvidenceState, ExecutionState) {
		state := NewEvidenceState()
		for i := range 6 {
			path := "pkg/file" + itoa(i) + ".go"
			state.ObserveOutcome(agent.ToolOutcome{
				CallID:   "call-" + itoa(i),
				ToolName: "read",
				Args:     map[string]any{"path": path},
				Result: agent.Result{
					Text:    "package pkg\nfunc f" + itoa(i) + "() {}\n",
					Details: map[string]any{"path": path, "totalLines": 2, "offset": 1, "limit": 100},
				},
			})
		}
		return state, ExecutionFromEvidence(state)
	}
	shape := BuildShape(contract.IntentContract{
		Objective: "Explain the package.",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: RepositoryFacts{Go: true, Markers: []string{"go.mod"}}})
	render := func(state *EvidenceState, execution ExecutionState) string {
		suff := EvaluateSufficiency(shape, state, execution, false)
		candidates := Candidates(shape, state, execution, nil, RepositoryFacts{Go: true}, suff)
		return RenderState(shape, state, execution, suff, candidates, DefaultStateBlockBytes, DefaultStateBlockItems)
	}
	firstState, firstExecution := build()
	secondState, secondExecution := build()
	first := render(firstState, firstExecution)
	second := render(secondState, secondExecution)
	if first != second {
		t.Fatalf("equivalent evidence rendered differently:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if first == "" {
		t.Fatal("the state block is empty; the comparison proved nothing")
	}
}

// The withheld-tool notices are derived from a map of reasons. Equivalent tool
// surfaces must produce the same text regardless of the input order, because
// the notice text is part of the request.
func TestCandidateNoticesAreOrderIndependent(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Objective: "Explain the build.",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask"})
	suff := EvaluateSufficiency(shape, NewEvidenceState(), ExecutionState{}, false)
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "write", tier: agent.TierWrite, ops: []contract.ChangeOp{contract.ChangeCreate}},
		testTool{name: "edit", tier: agent.TierWrite, ops: []contract.ChangeOp{contract.ChangeModify}},
		testTool{name: "bash", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute}},
	}
	first := Candidates(shape, NewEvidenceState(), ExecutionState{}, tools, RepositoryFacts{}, suff)
	reversed := []agent.Tool{tools[3], tools[2], tools[1], tools[0]}
	second := Candidates(shape, NewEvidenceState(), ExecutionState{}, reversed, RepositoryFacts{}, suff)
	if strings.Join(first.Notices, "|") != strings.Join(second.Notices, "|") {
		t.Fatalf("notices depend on input order:\n%v\n%v", first.Notices, second.Notices)
	}
	if first.CandidateSummary(2000) != second.CandidateSummary(2000) {
		t.Fatalf("candidate summary depends on input order")
	}
	if len(first.Notices) == 0 {
		t.Fatal("no notices rendered; the comparison proved nothing")
	}
}

// Gry is an optional structural-intelligence provider, not external knowledge.
// A mention of Gry must not create an external_observation requirement that the
// investigate tool cannot satisfy.
func TestGryMentionDoesNotRequireExternalKnowledge(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Use Gry or targeted execution when it can reduce uncertainty.",
	}, ShapeParams{Authority: "fuzz", Intent: "investigate"})
	if shape.External.Required {
		t.Fatal("Gry was treated as required external knowledge")
	}
	if !shape.NeedsCodeNavigation || !shape.NeedsRelationshipNavigation {
		t.Fatal("an explicit Gry request did not enable structural navigation")
	}
}

// A mutation at the act boundary may still read a few coordinates, but it
// cannot survey forever. After the bounded act window the run reports; the
// resource grant is not spent on another repository tour.
func TestActBoundaryStopsUnboundedObservation(t *testing.T) {
	shape := BuildShape(workspace.BuildIntentContract("Add validation to the product creation endpoint."), ShapeParams{Authority: "build"})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "write", tier: agent.TierWrite},
		testTool{name: "bash", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute, agent.ActionMutate}},
	}
	request := func(turns []agent.Turn) agent.ProjectionResult {
		return projector.Project(agent.ProjectionRequest{
			Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
			Turns:    turns,
			Tools:    tools,
			System:   []ai.SystemPromptBlock{{Text: "base"}},
		})
	}
	var turns []agent.Turn
	request(turns)
	var projected agent.ProjectionResult
	for i := 0; i <= MaxActObservationTurns; i++ {
		turns = append(turns, readTurn("read-"+itoa(i), "product"+itoa(i)+".go", "package product"))
		projected = request(turns)
	}
	if !projected.Report.Terminal || len(projected.Tools) != 0 {
		t.Fatalf("unbounded act-stage observation was not stopped: %+v", projected.Report)
	}
	if !strings.Contains(projected.Report.StopReason, "action boundary") {
		t.Fatalf("stop reason = %q", projected.Report.StopReason)
	}
}

// An investigate result is provider-backed evidence. Without recording it the
// ledger sees no observation and the projector cannot tell that a bounded
// investigation actually ran.
func TestInvestigationResultBecomesEvidence(t *testing.T) {
	state := NewEvidenceState()
	turn := agent.Turn{Results: []agent.ToolOutcome{{
		CallID: "inv-1", ToolName: "investigate",
		Args: map[string]any{"question": "where is checkout?"},
		Result: agent.Result{
			Text: "investigation: sufficient · intent locate\n- [structural/gry] Checkout — calls order",
			Details: map[string]any{
				"status": "sufficient", "stop_reason": "confidence target reached",
				"facts": []map[string]any{{"provider": "structural", "source": "gry", "subject": "Checkout", "detail": "calls order"}},
			},
		},
	}}}
	observed, novel := state.ObserveTurns(TaskShape{}, []agent.Turn{turn})
	if observed == 0 || novel == 0 {
		t.Fatalf("investigation outcome was not evidence: observed=%d novel=%d", observed, novel)
	}
	item, ok := state.Find("investigation:where is checkout?")
	if !ok || !item.Complete || item.View != "investigate" {
		t.Fatalf("investigation evidence = %+v, found=%v", item, ok)
	}
}

// A verdict-shaped investigation has one requirement no read can satisfy. The
// conclusion tool is the transition; after it, the surface closes and the model
// answers rather than surveying forever.
func TestInvestigationConclusionClosesTheVerdictBoundary(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Investigate the order flow and conclude one of SAFE, BUG, or UNRESOLVED. Do not create a fix.",
	}, ShapeParams{Authority: "fuzz", Intent: "investigate", Facts: RepositoryFacts{Go: true}})
	requireNeed(t, shape, "investigation_conclusion", true)

	evidence := NewEvidenceState()
	read := readTurn("read-1", "checkout.go", "package checkout\n\nfunc Place() {}\n")
	evidence.ObserveTurns(shape, []agent.Turn{read})
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.Stage != StageExplore {
		t.Fatalf("before conclusion = %s/%s, want explore", suff.State, suff.Stage)
	}
	conclusion := agent.Turn{Results: []agent.ToolOutcome{{
		CallID: "conclude-1", ToolName: "conclude",
		Args: map[string]any{"verdict": "SAFE", "evidence": "checkout.Place calls Reserve before Save"},
		Result: agent.Result{
			Text: "conclusion: SAFE",
			Details: map[string]any{
				"verdict": "SAFE", "evidence": "checkout.Place calls Reserve before Save",
			},
		},
	}}}
	evidence.ObserveTurns(shape, []agent.Turn{conclusion})
	if suff := EvaluateSufficiency(shape, evidence, ExecutionState{}, false); suff.State != Sufficient || suff.Stage != StageAnswer {
		t.Fatalf("after conclusion = %s/%s, want SUFFICIENT/answer", suff.State, suff.Stage)
	}
}

// After the only missing requirement is the conclusion, repeated reads are not
// progress. The semantic circuit breaker stops before the model-turn grant.
func TestConclusionRequirementStopsRepeatedReads(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Investigate the order flow and conclude one of SAFE, BUG, or UNRESOLVED.",
	}, ShapeParams{Authority: "fuzz", Intent: "investigate", Facts: RepositoryFacts{Go: true}})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "conclude", tier: agent.TierRead},
	}
	request := func(turns []agent.Turn) agent.ProjectionResult {
		return projector.Project(agent.ProjectionRequest{
			Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
			Turns:    turns,
			Tools:    tools,
			System:   []ai.SystemPromptBlock{{Text: "base"}},
		})
	}
	var turns []agent.Turn
	request(turns)
	var projected agent.ProjectionResult
	// The first read establishes the repository requirement; the conclusion
	// window allows a bounded number of further reads, then closes.
	for i := 0; i <= MaxConclusionObservationTurns; i++ {
		turns = append(turns, readTurn("read-"+itoa(i), "checkout.go", "package checkout\n\nfunc Place() {}"))
		projected = request(turns)
	}
	if !projected.Report.Terminal || len(projected.Tools) != 0 {
		t.Fatalf("repeated reads did not stop at the conclusion boundary: %+v", projected.Report)
	}
	if !strings.Contains(projected.Report.StopReason, "no meaningful task progress") {
		t.Fatalf("stop reason = %q", projected.Report.StopReason)
	}
}

// After the bounded conclusion window, the projection forces the terminal
// tool. The model cannot turn "conclude" into another repository survey.
func TestConclusionWindowForcesTheConcludeTool(t *testing.T) {
	shape := BuildShape(contract.IntentContract{
		Kind:      contract.ObjectiveInformational,
		Objective: "Investigate CheckoutService.Place and conclude SAFE, BUG, or UNRESOLVED.",
	}, ShapeParams{Authority: "fuzz", Intent: "investigate", Facts: RepositoryFacts{Go: true}})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "conclude", tier: agent.TierRead},
	}
	request := func(turns []agent.Turn) agent.ProjectionResult {
		return projector.Project(agent.ProjectionRequest{
			Messages: []agent.Message{agent.UserMessage{Text: shape.Objective}},
			Turns:    turns,
			Tools:    tools,
			System:   []ai.SystemPromptBlock{{Text: "base"}},
		})
	}
	var turns []agent.Turn
	turns = append(turns, readTurn("read-0", "checkout.go", "package checkout\n\nfunc Place() {}"))
	projected := request(turns)
	for i := 1; i < MaxConclusionObservationTurns; i++ {
		turns = append(turns, readTurn("read-"+itoa(i), "checkout"+itoa(i)+".go", "package checkout"))
		projected = request(turns)
	}
	if projected.ToolChoice.ToolName != "conclude" {
		t.Fatalf("tool choice = %+v, want the conclude tool", projected.ToolChoice)
	}
	if len(projected.Tools) != 1 || projected.Tools[0].Name() != "conclude" {
		t.Fatalf("tools = %v, want only the conclude tool at the terminal window", toolNames(projected.Tools))
	}
	if projected.Report.Terminal {
		t.Fatalf("the conclusion window became terminal before the forced call: %+v", projected.Report)
	}
}
