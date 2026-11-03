package task

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// The runtime's material observations become bounded task evidence, a
// successful check becomes an evidence-backed fact, and BUILD receives the
// progress, scope, and artifact references it needs to resume.
func TestObserveTurnsPopulatesSharedAndRoleContext(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Build
	controller.SetRoleSource(func() workspace.Workspace { return role })
	if _, err := controller.Begin(testIntent("implement the change"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{
		writeOutcome("note.txt"),
		readOutcome("note.txt"),
		verificationOutcome("go test ./..."),
	}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	current, _ := controller.Current()
	if got := len(current.Context.Evidence); got < 3 {
		t.Fatalf("evidence records = %d, want the mutation, read, and verification observations", got)
	}
	var mutation, read, verification *EvidenceRecord
	for i := range current.Context.Evidence {
		record := &current.Context.Evidence[i]
		switch {
		case record.Artifact == "cognition:mutation:note.txt":
			mutation = record
		case record.Artifact == "cognition:file:note.txt":
			read = record
		case strings.HasPrefix(record.Artifact, "cognition:verification:"):
			verification = record
		}
		if len(record.Summary) > maxEvidenceSummaryBytes {
			t.Fatalf("evidence summary was not bounded: %d bytes", len(record.Summary))
		}
		if record.TaskID != current.ID {
			t.Fatalf("evidence %q is not task-linked", record.ID)
		}
	}
	if mutation == nil || mutation.Kind != EvidenceArtifact || mutation.Outcome != EvidenceObserved {
		t.Fatalf("mutation evidence = %+v", mutation)
	}
	if read == nil || read.Kind != EvidenceSource || read.Digest == "" {
		t.Fatalf("read evidence = %+v", read)
	}
	if verification == nil || verification.Kind != EvidenceTest || verification.Outcome != EvidenceObserved {
		t.Fatalf("verification evidence = %+v", verification)
	}

	if got := len(current.Context.Facts); got != 1 {
		t.Fatalf("facts = %d, want exactly the successful verification fact", got)
	}
	if !strings.Contains(current.Context.Facts[0].Text, "verification") {
		t.Fatalf("fact text = %q, want the recorded check", current.Context.Facts[0].Text)
	}
	if len(current.Context.Facts[0].EvidenceIDs) != 1 ||
		current.Context.Facts[0].EvidenceIDs[0] != verification.ID {
		t.Fatalf("fact support = %v, want %q", current.Context.Facts[0].EvidenceIDs, verification.ID)
	}

	build, ok := current.Context.Role(workspace.Build)
	if !ok {
		t.Fatal("BUILD context was not recorded")
	}
	if !containsSubstring(build.Progress, "changed note.txt") {
		t.Fatalf("BUILD progress = %v, want the mutation observation", build.Progress)
	}
	if !containsString(build.Scope, "note.txt") {
		t.Fatalf("BUILD scope = %v, want note.txt", build.Scope)
	}
	if !containsString(build.Artifacts, "cognition:mutation:note.txt") {
		t.Fatalf("BUILD artifacts = %v, want the mutation reference", build.Artifacts)
	}

	// The projection is idempotent: a second checkpoint must not duplicate
	// evidence, facts, or role notes.
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	again, _ := controller.Current()
	if len(again.Context.Evidence) != len(current.Context.Evidence) {
		t.Fatalf("evidence grew on re-projection: %d -> %d", len(current.Context.Evidence), len(again.Context.Evidence))
	}
	if len(again.Context.Facts) != len(current.Context.Facts) {
		t.Fatalf("facts grew on re-projection: %d -> %d", len(current.Context.Facts), len(again.Context.Facts))
	}
}

// Each role receives only the observations its execution produced. ASK gets
// read progress, FUZZ gets investigation progress, and neither acquires a
// BUILD plan or scope.
func TestObservationProjectionAttributesOnlyTheExecutingRole(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	role := workspace.Ask
	controller.SetRoleSource(func() workspace.Workspace { return role })
	intent := testIntent("understand the failure")
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin ASK: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{readOutcome("note.txt")}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync ASK: %v", err)
	}
	current, _ := controller.Current()
	ask, ok := current.Context.Role(workspace.Ask)
	if !ok || !containsSubstring(ask.Progress, "inspected") {
		t.Fatalf("ASK context = %+v, want read progress", ask)
	}
	if _, ok := current.Context.Role(workspace.Fuzz); ok {
		t.Fatal("an ASK observation created a FUZZ context")
	}
	if len(current.Context.Facts) != 0 {
		t.Fatalf("a read became a verified fact: %+v", current.Context.Facts)
	}

	role = workspace.Fuzz
	if _, err := controller.Begin(intent, nil); err != nil {
		t.Fatalf("Begin FUZZ: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{commandOutcome("go test ./...")}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync FUZZ: %v", err)
	}
	current, _ = controller.Current()
	fuzz, ok := current.Context.Role(workspace.Fuzz)
	if !ok || !containsSubstring(fuzz.Progress, "go test ./...") {
		t.Fatalf("FUZZ context = %+v, want command progress", fuzz)
	}
	if len(fuzz.Scope) != 0 || len(fuzz.Plan) != 0 {
		t.Fatalf("FUZZ acquired BUILD fields: %+v", fuzz)
	}
	if len(current.Context.Facts) != 0 {
		t.Fatalf("an unverified command became a fact: %+v", current.Context.Facts)
	}
}

// Projecting observations is context, never authority: the workspace gate is
// unchanged.
func TestObservationProjectionDoesNotGrantAuthority(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("implement the change"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{
		writeOutcome("note.txt"),
		verificationOutcome("go test ./..."),
	}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := workspace.Admit(workspace.Fuzz, workspace.OpWrite); err == nil {
		t.Fatal("task context granted FUZZ mutation authority")
	}
	if err := workspace.Admit(workspace.Ask, workspace.OpExec); err == nil {
		t.Fatal("task context granted ASK execution authority")
	}
}

// Transient output is not material, and every projected observation keeps a
// provenance reference rather than copying the payload.
func TestProjectionSkipsTransientAndKeepsProvenance(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("inspect the change"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{
		{
			CallID: "bg", ToolName: "bash", Args: map[string]any{"command": "sleep 10"},
			Result: agent.Result{Text: "started", Executes: true, Details: map[string]any{"command": "sleep 10"}},
		},
		readOutcome("note.txt"),
	}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	current, _ := controller.Current()
	for _, record := range current.Context.Evidence {
		if strings.Contains(record.Summary, "background job started") {
			t.Fatalf("transient evidence was projected: %+v", record)
		}
		if !strings.HasPrefix(record.Artifact, "cognition:") {
			t.Fatalf("evidence %q has no durable artifact reference", record.ID)
		}
	}
	found := false
	for _, record := range current.Context.Evidence {
		if record.Artifact == "cognition:file:note.txt" {
			found = true
			if record.Source == "" || record.Digest == "" {
				t.Fatalf("read provenance was lost: %+v", record)
			}
		}
	}
	if !found {
		t.Fatal("the read observation was not projected")
	}
}

func writeOutcome(path string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: "write-1", ToolName: "write", Args: map[string]any{"path": path},
		Result: agent.Result{
			Text: "wrote " + path, Mutates: true,
			Details: map[string]any{"path": path, "bytes": 5},
		},
	}
}

func readOutcome(path string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: "read-1", ToolName: "read", Args: map[string]any{"path": path},
		Result: agent.Result{Text: "hello", Details: map[string]any{"path": path}},
	}
}

func commandOutcome(command string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: "bash-1", ToolName: "bash", Args: map[string]any{"command": command},
		Result: agent.Result{
			Text: "ok", Executes: true, Finished: true,
			Details: map[string]any{"command": command, "exitCode": 0},
		},
	}
}

func verificationOutcome(command string) agent.ToolOutcome {
	return agent.ToolOutcome{
		CallID: "verify-1", ToolName: "bash", Args: map[string]any{"command": command},
		Result: agent.Result{
			Text: "ok", Executes: true, Finished: true, Verifies: true,
			Verification: &agent.VerificationEvidence{Command: command, Check: contract.CheckTest},
			Details:      map[string]any{"command": command, "exitCode": 0},
		},
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

// A host disposition is authoritative: re-projecting the same verification
// observation must not recreate a fact the host superseded.
func TestProjectionDoesNotResurrectASupersededVerificationFact(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("respect host disposition"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{
		verificationOutcome("go test ./..."),
	}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	current, _ := controller.Current()
	if len(current.Context.Facts) != 1 {
		t.Fatalf("facts = %d, want the verification fact", len(current.Context.Facts))
	}
	old := current.Context.Facts[0]

	replacementEvidence, err := controller.RecordEvidence(EvidenceInput{
		Kind: EvidenceTest, Source: "replacement", Summary: "a newer conclusion", Outcome: EvidenceObserved,
	})
	if err != nil {
		t.Fatalf("replacement evidence: %v", err)
	}
	replacement, err := controller.RecordFact(FactInput{
		Text: "a newer conclusion", EvidenceIDs: []string{replacementEvidence.ID},
	})
	if err != nil {
		t.Fatalf("replacement fact: %v", err)
	}
	if _, err := controller.SupersedeFact(old.ID, replacement.ID); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	current, _ = controller.Current()
	for _, fact := range current.Context.Facts {
		if fact.Text == old.Text && fact.Status == FactActive {
			t.Fatalf("projection resurrected a superseded verification fact: %+v", fact)
		}
	}
	if got := findFact(t, current, old.ID).Status; got != FactSuperseded {
		t.Fatalf("superseded verification fact status = %s", got)
	}
}

// A full bounded log is reported in the briefing instead of silently dropping
// new observations.
func TestProjectionReportsAFullEvidenceLog(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("bounded log"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := 0; i < MaxEvidenceRecords; i++ {
		controller.task.Context.Evidence = append(controller.task.Context.Evidence, EvidenceRecord{
			ID:       fmt.Sprintf("existing-%d", i),
			TaskID:   controller.task.ID,
			Kind:     EvidenceCommand,
			Role:     workspace.Build,
			Summary:  "existing observation",
			Outcome:  EvidenceObserved,
			Validity: EvidenceCurrent,
		})
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{readOutcome("note.txt")}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	current, _ := controller.Current()
	if !strings.Contains(current.ContextWarning, "limit reached") {
		t.Fatalf("a full evidence log was not reported: %q", current.ContextWarning)
	}
}

// A command that may have changed the workspace has no trustworthy path, so it
// must not turn its digest into BUILD's recorded scope.
func TestBashMutationDoesNotBecomeRecordedScope(t *testing.T) {
	controller := NewController(NewStore(t.TempDir()), nil)
	if _, err := controller.Begin(testIntent("command mutation"), nil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ObserveTurns([]agent.Turn{{Results: []agent.ToolOutcome{{
		CallID: "m1", ToolName: "bash", Args: map[string]any{"command": "sed -i s/a/b/ note.txt"},
		Result: agent.Result{
			Text: "ok", Mutates: true, Executes: true, Finished: true,
			Details: map[string]any{"command": "sed -i s/a/b/ note.txt", "exitCode": 0},
		},
	}}}})
	if _, err := controller.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	current, _ := controller.Current()
	build, ok := current.Context.Role(workspace.Build)
	if !ok {
		t.Fatal("BUILD context was not recorded")
	}
	if len(build.Scope) != 0 {
		t.Fatalf("a bash digest became BUILD scope: %v", build.Scope)
	}
	if !containsSubstring(build.Progress, "command may have changed") {
		t.Fatalf("BUILD progress = %v, want the command observation", build.Progress)
	}
}
