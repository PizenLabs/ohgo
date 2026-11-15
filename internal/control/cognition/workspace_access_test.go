package cognition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

// An unlistable workspace root is an environment blocker, not a progress stall.
// The transcript that motivated this test ran nine failing calls and reported
// "no meaningful task progress" while never saying that the process cannot list
// the repository at all.
func TestWorkspaceUnreadableBlocksOnlyEnumerationRequirements(t *testing.T) {
	facts := RepositoryFacts{RootError: "open /w: operation not permitted"}
	broad := BuildShape(contract.IntentContract{
		Objective: "check this project and analyze what this project is about?",
		Kind:      contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: facts})
	broadSuff := EvaluateSufficiency(broad, NewEvidenceState(), ExecutionState{}, false)
	// Precondition: the probe cannot make these requirements sufficient on its
	// own, so the environment verdict has something to close.
	if broadSuff.State == Sufficient {
		t.Fatalf("a broad account was judged sufficient before any observation: %+v", broadSuff)
	}

	blocked := workspaceUnreadable(facts, broadSuff)
	if blocked.State != Blocked || blocked.Kind != BlockCapabilityFailure {
		t.Fatalf("state/kind = %v/%v, want the environment block", blocked.State, blocked.Kind)
	}
	reason := strings.Join(blocked.Reasons, " ")
	if !strings.Contains(reason, "cannot be enumerated") ||
		!strings.Contains(reason, "operation not permitted") {
		t.Fatalf("reason = %q, want the restriction and its cause", reason)
	}
	if !strings.Contains(strings.Join(blocked.Next, " "), "do not retry discovery") {
		t.Fatalf("next = %v, want an explicit instruction not to retry", blocked.Next)
	}

	// A targeted read of a path the model already knows may still succeed when
	// enumeration does not, so the blocker must not fire for it.
	targeted := BuildShape(contract.IntentContract{
		Objective: "explain what main.go does", Kind: contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: facts})
	targetedSuff := EvaluateSufficiency(targeted, NewEvidenceState(), ExecutionState{}, false)
	if got := workspaceUnreadable(facts, targetedSuff); got.State == Blocked && got.Kind == BlockCapabilityFailure {
		t.Fatalf("a targeted read was blocked: %v", got.Reasons)
	}

	// A readable root never blocks.
	if got := workspaceUnreadable(RepositoryFacts{Go: true}, broadSuff); got.State == Blocked {
		t.Fatalf("a readable root was blocked: %v", got.Reasons)
	}
}

// The probe must report the enumeration failure it measured, and keep the
// markers it could still stat: a manifest is real even when the listing is not.
func TestProbeRecordsUnreadableRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o111); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if _, err := os.ReadDir(root); err == nil {
		t.Skip("the root is still listable; the permission bit was not enforced")
	}
	facts := ProbeFacts(root)
	if facts.RootError == "" {
		t.Fatal("the probe reported a listable root")
	}
	if facts.RootFiles != 0 {
		t.Fatalf("root entries = %d, want 0 for an unlistable root", facts.RootFiles)
	}
	if len(facts.Markers) != 1 || facts.Markers[0] != "package.json" {
		t.Fatalf("markers = %v, want the statable manifest", facts.Markers)
	}

	readable := t.TempDir()
	if err := os.WriteFile(filepath.Join(readable, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if clean := ProbeFacts(readable); clean.RootError != "" {
		t.Fatalf("a readable root reported %q", clean.RootError)
	}
}

// The state block must lead with the restriction: a language list without it
// reads as a complete (if tiny) project.
func TestStateBlockNamesUnreadableRoot(t *testing.T) {
	if got := workspaceProbeSummary(RepositoryFacts{}); got != "" {
		t.Fatalf("a clean probe rendered %q", got)
	}
	summary := workspaceProbeSummary(RepositoryFacts{JavaScript: true, Markers: []string{"package.json"},
		RootError: "open /w: operation not permitted"})
	if !strings.HasPrefix(summary, "root not enumerable (open /w: operation not permitted)") {
		t.Fatalf("summary = %q, want the restriction first", summary)
	}
	if !strings.Contains(summary, "detected JavaScript") {
		t.Fatalf("summary = %q, want the statable facts preserved", summary)
	}
}

// The projector is where the environment fact meets the offered surface: an
// unenumerable root must close the repository-wide task even though read, ls,
// glob, and structure are all still offered, because none of them can list the
// workspace. Without the ordering (after ReconcileAcquisition) the remaining
// providers would resurrect a task nothing can satisfy.
func TestProjectedLaunchReportsTheUnreadableWorkspace(t *testing.T) {
	const rootCause = "open /w: operation not permitted"
	facts := RepositoryFacts{Markers: []string{"package.json"}, JavaScript: true, RootError: rootCause}
	const prompt = "check this project and analyze what this project is about?"
	shape := BuildShape(contract.IntentContract{
		Objective: prompt, Kind: contract.ObjectiveInformational,
	}, ShapeParams{Authority: "ask", Facts: facts})
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead},
		testTool{name: "ls", tier: agent.TierRead},
		testTool{name: "glob", tier: agent.TierRead},
		testTool{name: "structure", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionStructure}},
	}
	result := NewProjector(shape, NewEvidenceState(), facts).Project(agent.ProjectionRequest{
		Turn: 1, Tools: tools,
	})
	report := result.Report
	if report.StopReason == "" || strings.Contains(report.StopReason, "no meaningful task progress") {
		t.Fatalf("stop reason = %q, want the environment restriction", report.StopReason)
	}
	if !strings.Contains(report.StopReason, rootCause) {
		t.Fatalf("stop reason = %q, want the measured cause", report.StopReason)
	}
	if !report.Terminal || !report.AnswerEligible {
		t.Fatalf("terminal/answerEligible = %v/%v, want the report boundary", report.Terminal, report.AnswerEligible)
	}
	if len(result.Tools) != 0 {
		t.Fatalf("offered %d tools for an unenumerable workspace", len(result.Tools))
	}
	state := strings.Join(result.Runtime, "\n")
	if !strings.Contains(state, "root not enumerable") {
		t.Fatalf("runtime block does not name the restriction:\n%s", state)
	}
}
