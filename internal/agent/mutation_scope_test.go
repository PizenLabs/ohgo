package agent

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/security"
)

func workspaceTechnologyContract() contract.IntentContract {
	technology := contract.Target{Kind: contract.TargetTechnology, Value: "Next.js"}
	repository := contract.Target{Kind: contract.TargetRepository, Value: "current workspace"}
	return contract.IntentContract{
		Objective:       "convert this project to Next.js",
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{technology, repository},
		ExpectedChanges: []contract.ExpectedChange{{Target: technology, Operation: contract.ChangeAny}},
		MutationScope:   contract.MutationScope{Kind: contract.MutationScopeWorkspace},
		Verification:    []contract.VerificationRequirement{{Kind: contract.VerifyAny, Check: contract.CheckBuild}},
	}
}

func directoryPathContract(directory string) contract.IntentContract {
	target := contract.Target{Kind: contract.TargetPath, Value: directory, Directory: true}
	return contract.IntentContract{
		Objective:       "add files under " + directory,
		Kind:            contract.ObjectiveMutation,
		Targets:         []contract.Target{target},
		ExpectedChanges: []contract.ExpectedChange{{Target: target, Operation: contract.ChangeAny}},
		MutationScope: contract.MutationScope{
			Kind:        contract.MutationScopePaths,
			Directories: []string{directory},
		},
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
}

func shellMutationOutcome(root, command string) ToolOutcome {
	return ToolOutcome{
		ToolName: "bash",
		Result: Result{
			Text:     "$ " + command,
			Mutates:  true,
			Executes: true,
			Finished: true,
			Details:  map[string]any{"command": command},
		},
		Authorization: &security.Decision{
			Capability: security.CapabilityExec,
			Mutates:    true,
			Scope:      security.NormalizeScope(root, "."),
		},
	}
}

func classOf(mutations []ClassifiedMutation, path string) MutationClass {
	for _, mutation := range mutations {
		if mutation.Path == path {
			return mutation.Class
		}
	}
	return ""
}

// A technology objective is workspace-scoped. A changed source file is expected
// even though its path does not contain the technology name.
func TestTechnologyTargetDoesNotBecomeAPathScope(t *testing.T) {
	root := t.TempDir()
	intent := workspaceTechnologyContract()
	provenance := MutationProvenance{
		Captured: true,
		Source:   "recovery.edit",
		Changes:  []MutationChange{{Path: "app/page.tsx", Status: "M"}},
	}
	turns := []Turn{
		{Results: []ToolOutcome{mutationOutcome(root, "app/page.tsx")}},
		{Results: []ToolOutcome{shellMutationOutcome(root, "npx create-next-app .")}},
	}
	mutations := ClassifyMutations(root, turns, intent, nil, provenance)
	if got := classOf(mutations, "app/page.tsx"); got != MutationExpected {
		t.Fatalf("app/page.tsx class = %s, want EXPECTED (%+v)", got, mutations)
	}
	for _, mutation := range mutations {
		if mutation.Class == MutationUnattributed {
			t.Fatalf("a shell mutation covered by journal evidence was unattributed: %+v", mutations)
		}
	}
}

// A directory target is a prefix scope: a file under it is expected.
func TestDirectoryTargetAllowsContainedPath(t *testing.T) {
	root := t.TempDir()
	intent := directoryPathContract("app")
	turns := []Turn{{Results: []ToolOutcome{mutationOutcome(root, "app/page.tsx")}}}
	mutations := ClassifyMutations(root, turns, intent, nil, MutationProvenance{})
	if got := classOf(mutations, "app/page.tsx"); got != MutationExpected {
		t.Fatalf("contained path class = %s, want EXPECTED (%+v)", got, mutations)
	}
}

// A directory target does not authorize an unrelated file.
func TestDirectoryTargetRejectsUnrelatedPath(t *testing.T) {
	root := t.TempDir()
	intent := directoryPathContract("app")
	turns := []Turn{{Results: []ToolOutcome{mutationOutcome(root, "unrelated.txt")}}}
	mutations := ClassifyMutations(root, turns, intent, nil, MutationProvenance{})
	if got := classOf(mutations, "unrelated.txt"); got != MutationUnexpected {
		t.Fatalf("unrelated path class = %s, want UNEXPECTED (%+v)", got, mutations)
	}
}

// A shell mutation with no path in the command is attributed by the run's
// journal diff, not by the final model statement.
func TestShellMutationIsAttributedByJournalDiff(t *testing.T) {
	root := t.TempDir()
	intent := workspaceTechnologyContract()
	turns := []Turn{{Results: []ToolOutcome{shellMutationOutcome(root, "npx create-next-app .")}}}
	mutations := ClassifyMutations(root, turns, intent, nil, MutationProvenance{
		Captured: true,
		Source:   "recovery.edit",
		Changes:  []MutationChange{{Path: "app/page.tsx", Status: "A"}},
	})
	if got := classOf(mutations, "app/page.tsx"); got != MutationExpected {
		t.Fatalf("journal-attributed path class = %s, want EXPECTED (%+v)", got, mutations)
	}
	for _, mutation := range mutations {
		if mutation.Class == MutationUnattributed {
			t.Fatalf("journal-attributed shell mutation was still unattributed: %+v", mutations)
		}
	}
}

// The journal only captures the workspace. A temporary staging path is not a
// repository mutation, while the files copied into the workspace are.
func TestTemporaryStagingIsNotARepositoryMutation(t *testing.T) {
	root := t.TempDir()
	intent := workspaceTechnologyContract()
	staging := []ToolOutcome{
		shellMutationOutcome(root, "npx create-next-app /tmp/nextapp"),
		shellMutationOutcome(root, "cp -r /tmp/nextapp/. ."),
	}
	mutations := ClassifyMutations(root, []Turn{{Results: staging}}, intent, nil, MutationProvenance{
		Captured: true,
		Source:   "recovery.edit",
		Changes:  []MutationChange{{Path: "app/page.tsx", Status: "A"}},
	})
	for _, mutation := range mutations {
		if strings.Contains(mutation.Path, "/tmp") {
			t.Fatalf("temporary staging was classified as a repository mutation: %+v", mutations)
		}
	}
	if got := classOf(mutations, "app/page.tsx"); got != MutationExpected {
		t.Fatalf("workspace mutation class = %s, want EXPECTED (%+v)", got, mutations)
	}
}

// A path that was already changed before the run is absent from the run's
// journal diff and must not be attributed to the current task.
func TestPreExistingDiffIsNotAttributed(t *testing.T) {
	root := t.TempDir()
	intent := workspaceTechnologyContract()
	turns := []Turn{{Results: []ToolOutcome{{
		ToolName: "read",
		Result:   Result{Text: "contents"},
	}}}}
	mutations := ClassifyMutations(root, turns, intent, nil, MutationProvenance{
		Captured: true,
		Source:   "recovery.edit",
	})
	if len(mutations) != 0 {
		t.Fatalf("a pre-existing change was attributed to the run: %+v", mutations)
	}
}

// A successful Next.js migration is proven by actual mutation, journal
// attribution, and a build that followed the change.
func TestSuccessfulTechnologyMigrationIsProven(t *testing.T) {
	root := t.TempDir()
	intent := workspaceTechnologyContract()
	turns := []Turn{
		{Results: []ToolOutcome{
			mutationOutcome(root, "package.json"),
			mutationOutcome(root, "app/page.tsx"),
		}},
		{Results: []ToolOutcome{shellMutationOutcome(root, "npx create-next-app .")}},
		{Results: []ToolOutcome{{
			ToolName: "bash",
			Result: Result{
				Text:     "$ npm run build",
				Executes: true,
				Finished: true,
				Verifies: true,
				Verification: &VerificationEvidence{
					Check:  contract.CheckBuild,
					Scopes: []VerificationScope{{All: true}},
				},
				Details: map[string]any{"command": "npm run build"},
			},
		}}},
		cleanStopWithText("Converted the project to Next.js; the build passed."),
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: intent,
		Root:     root,
		Mutations: MutationProvenance{Captured: true, Source: "recovery.edit", Turn: 1, Changes: []MutationChange{
			{Path: "package.json", Status: "M"},
			{Path: "app/page.tsx", Status: "A"},
			{Path: "next.config.ts", Status: "A"},
		}},
	})
	if !verdict.Complete() {
		t.Fatalf("successful migration was not proven: %s", verdict.Reason)
	}
}

// A shell mutation with no journal evidence remains unattributed and requires
// review, even when a check passed.
func TestTrulyUnattributedMutationIsReview(t *testing.T) {
	root := t.TempDir()
	intent := workspaceTechnologyContract()
	turns := []Turn{
		{Results: []ToolOutcome{shellMutationOutcome(root, "some-opaque-tool --write")}},
		{Results: []ToolOutcome{{
			ToolName: "bash",
			Result: Result{
				Text:     "$ npm run build",
				Executes: true,
				Finished: true,
				Verifies: true,
				Verification: &VerificationEvidence{
					Check:  contract.CheckBuild,
					Scopes: []VerificationScope{{All: true}},
				},
			},
		}}},
		cleanStop(),
	}
	verdict := EvaluateTaskCompletion(CompletionInput{
		Turns:    turns,
		Contract: intent,
		Root:     root,
	})
	if verdict.Complete() {
		t.Fatal("an unattributed external mutation was reported complete")
	}
	if !strings.Contains(verdict.Reason, "could not be attributed") {
		t.Fatalf("review reason = %q, want the attribution gap", verdict.Reason)
	}
}

func cleanStopWithText(text string) Turn {
	turn := cleanStop()
	turn.Assistant.Content = append(turn.Assistant.Content, ai.TextContent{Text: text})
	return turn
}
