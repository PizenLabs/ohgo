package cognition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
)

// project1180 is the recorded task shape from the runaway run: a change request
// whose content list contains a capitalised product name the extractor reads as
// a code symbol.
func project1180(t *testing.T) (TaskShape, string) {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"index.html": "<html><body></body></html>",
		"styles.css": "body{}",
		"script.js":  "console.log(1)",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	intent := contract.IntentContract{
		Kind:         contract.ObjectiveMutation,
		Objective:    "Add a few projects in a separate stack with the projects: OpenCode, Claude Code, Duckduck Go",
		Targets:      []contract.Target{{Kind: contract.TargetSymbol, Value: "OpenCode"}},
		Ambiguous:    true,
		Verification: []contract.VerificationRequirement{{Kind: contract.VerifyAny}},
	}
	shape := BuildShape(intent, ShapeParams{Authority: "build", Facts: ProbeFacts(root)})
	if !hasNeed(shape, "target_coordinates:OpenCode") {
		t.Fatalf("fixture did not reproduce the heuristic target: %+v", shape.RequiredKnowledge)
	}
	if !hasNeed(shape, "verification") {
		t.Fatalf("fixture did not require verification: %+v", shape.RequiredKnowledge)
	}
	return shape, root
}

func mutationTurn(id, path string) agent.Turn {
	return agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   id,
		ToolName: "edit",
		Args:     map[string]any{"path": path},
		Result:   agent.Result{Text: "edited", Mutates: true, AffectedPaths: []string{path}},
	}}}
}

func symbolMiss(id, name string) agent.Turn {
	return agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   id,
		ToolName: "symbols",
		Args:     map[string]any{"symbol": name},
		Result: agent.Result{
			Text:    "status: not_found\nquery: " + name,
			Details: map[string]any{"status": "not_found", "symbol": name},
			Useless: true,
		},
	}}}
}

func checkNotRun(id string) agent.Turn {
	return agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   id,
		ToolName: "check",
		Args:     map[string]any{"mode": "run"},
		Result: agent.Result{
			Text:    "checks: NOT_RUN (no supported command was discovered)",
			Details: map[string]any{"status": "NOT_RUN", "mode": "run"},
			Useless: true,
		},
	}}}
}

func workingTools() []agent.Tool {
	return []agent.Tool{
		testTool{name: "read", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionObserve}},
		testTool{name: "edit", tier: agent.TierWrite, classes: []agent.ActionClass{agent.ActionMutate}},
		testTool{name: "symbols", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}},
		testTool{name: "grep", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionObserve}},
		testTool{name: "check", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute}},
	}
}

// languageTool is a testTool that declares the source languages it can read, so
// the candidate and requirement layers see a language-gated capability.
type languageTool struct {
	testTool
	languages []string
}

func (t languageTool) SupportedLanguages() []string { return append([]string(nil), t.languages...) }

// The recorded failure: after the change was made, the runtime kept demanding a
// code symbol the repository never contained and a check the workspace could
// not run. Both requirements must be settled by evidence, and the projection
// must then close the action surface instead of inviting another survey.
func TestProjectionCompletesWhenHeuristicTargetIsAbsentAndVerificationUnavailable(t *testing.T) {
	shape, root := project1180(t)
	projector := NewProjector(shape, NewEvidenceState(), ProbeFacts(root))
	tools := workingTools()
	turns := []agent.Turn{
		mutationTurn("edit-1", "index.html"),
		symbolMiss("symbols-1", "OpenCode"),
		symbolMiss("symbols-2", "OpenCode"),
		checkNotRun("check-1"),
	}

	// The target lookup is settled by one definitive structural miss, but the
	// check has not run yet, so the objective is still at the verification
	// boundary. No limitation is claimed before the check observes one.
	first := projector.Project(agent.ProjectionRequest{Turns: turns[:2], Tools: tools})
	if first.Report.VerificationLimited != "" {
		t.Fatalf("verification was called limited before any check: %q", first.Report.VerificationLimited)
	}
	if first.Report.Stage == string(StageComplete) {
		t.Fatalf("stage completed before the required check: %+v", first.Report)
	}
	if first.Report.Stage != string(StageVerify) {
		t.Fatalf("stage = %q, want verify after the target lookup settled", first.Report.Stage)
	}

	final := projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools})
	if final.Report.Stage != string(StageComplete) {
		t.Fatalf("stage = %q, want %q (missing=%v unresolved=%v)",
			final.Report.Stage, StageComplete, final.Report.Missing, final.Report.Unresolved)
	}
	if !final.Report.AnswerEligible {
		t.Fatal("the completed objective was not answer-eligible")
	}
	if len(final.Tools) != 0 {
		t.Fatalf("completed objective still offered %d tool(s): %v", len(final.Tools), names(final.Tools))
	}
	if final.Report.VerificationLimited == "" {
		t.Fatal("the unavailable check was not recorded as a verification limitation")
	}
	if len(final.Report.Missing) != 0 {
		t.Fatalf("completed objective still reported missing evidence: %v", final.Report.Missing)
	}
	unresolved := strings.Join(final.Report.Unresolved, "\n")
	if !strings.Contains(unresolved, "OpenCode") || !strings.Contains(unresolved, "not applicable") {
		t.Fatalf("absent target was not reported as not applicable: %q", unresolved)
	}
}

// A portable build cannot observe a web-only repository's sources, so the
// inferred target has no available lookup. The requirement must be reported as
// not applicable rather than block a change the runtime itself observed; the
// run still owes the check, and its absence remains a verification limitation.
func TestUnavailableStructuralAdapterResolvesHeuristicTarget(t *testing.T) {
	shape, root := project1180(t)
	projector := NewProjector(shape, NewEvidenceState(), ProbeFacts(root))
	tools := []agent.Tool{
		testTool{name: "read", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionObserve}},
		testTool{name: "edit", tier: agent.TierWrite, classes: []agent.ActionClass{agent.ActionMutate}},
		languageTool{
			testTool:  testTool{name: "symbols", tier: agent.TierRead, classes: []agent.ActionClass{agent.ActionObserve, agent.ActionStructure}},
			languages: []string{"go"},
		},
		testTool{name: "check", tier: agent.TierExec, classes: []agent.ActionClass{agent.ActionExecute}},
	}
	turns := []agent.Turn{
		mutationTurn("edit-1", "index.html"),
		readTurn("read-1", "index.html", "<html><body></body></html>"),
		checkNotRun("check-1"),
	}
	projected := projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools})
	if projected.Report.Stage != string(StageComplete) {
		t.Fatalf("stage = %q, want %q (missing=%v unresolved=%v)",
			projected.Report.Stage, StageComplete, projected.Report.Missing, projected.Report.Unresolved)
	}
	if projected.Report.VerificationLimited == "" {
		t.Fatal("the unavailable check was not recorded as a verification limitation")
	}
	for _, missing := range projected.Report.Missing {
		if strings.HasPrefix(missing, "target_coordinates:") {
			t.Fatalf("an unacquirable coordinate stayed blocking: %v", projected.Report.Missing)
		}
	}
	unresolved := strings.Join(projected.Report.Unresolved, "\n")
	if !strings.Contains(unresolved, "OpenCode") || !strings.Contains(unresolved, "no available structural capability") {
		t.Fatalf("unacquirable target was not reported as not applicable: %q", unresolved)
	}
}

// A failed check is not an unavailable one. The refusal to invent success is
// the same rule in both directions: evidence decides the state, not the wish
// to finish.
func TestProjectionKeepsVerificationPendingWhenCheckExists(t *testing.T) {
	shape, root := project1180(t)
	projector := NewProjector(shape, NewEvidenceState(), ProbeFacts(root))
	turns := []agent.Turn{
		mutationTurn("edit-1", "index.html"),
		symbolMiss("symbols-1", "OpenCode"),
		symbolMiss("symbols-2", "OpenCode"),
	}
	projected := projector.Project(agent.ProjectionRequest{Turns: turns, Tools: workingTools()})
	if projected.Report.Stage != string(StageVerify) {
		t.Fatalf("stage = %q, want verify: %+v", projected.Report.Stage, projected.Report)
	}
	if projected.Report.VerificationLimited != "" {
		t.Fatalf("verification was claimed unavailable without a check result: %q", projected.Report.VerificationLimited)
	}
}

// Novelty is not progress toward a change. A model that keeps finding new files
// must still be stopped by the observation budget, or a filesystem walk is an
// unbounded task.
func TestExplorationForAChangeIsBoundedEvenWhenEveryTurnFindsSomething(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := contract.IntentContract{Kind: contract.ObjectiveMutation, Objective: "Refactor the code."}
	shape := BuildShape(intent, ShapeParams{Authority: "build", Facts: ProbeFacts(root)})
	if !hasNeed(shape, "implementation_observation") {
		t.Fatalf("fixture is not a targetless code change: %+v", shape.RequiredKnowledge)
	}
	projector := NewProjector(shape, NewEvidenceState(), ProbeFacts(root))
	tools := workingTools()

	var turns []agent.Turn
	terminal := false
	var report agent.ProjectionReport
	for turn := 0; turn < PreActionObservationAllowance(shape)+3; turn++ {
		turns = append(turns, agent.Turn{Results: []agent.ToolOutcome{{
			CallID:   "grep-" + string(rune('a'+turn)),
			ToolName: "grep",
			Args:     map[string]any{"pattern": "x", "path": "dir" + string(rune('a'+turn))},
			Result: agent.Result{
				Text:    "dir" + string(rune('a'+turn)) + "/file.go:1: new fact " + string(rune('a'+turn)),
				Details: map[string]any{"matches": 1},
			},
		}}})
		projected := projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools, Turn: turn})
		report = projected.Report
		if projected.Report.Terminal {
			terminal = true
			break
		}
	}
	if !terminal {
		t.Fatalf("exploration never terminated: stage=%q stalled=%d", report.Stage, report.NoProgressTurns)
	}
	if report.Stage != string(StageBlocked) {
		t.Fatalf("terminal stage = %q, want blocked: %+v", report.Stage, report)
	}
	if report.StopReason == "" {
		t.Fatalf("terminal state had no authoritative reason: %+v", report)
	}
}

// The observation window is proportional to the grant, because how much reading
// a change needs is a property of the project. A tight grant stays its own
// bound; an unmetered run still gets the finite floor.
func TestPreActionObservationAllowanceFollowsTheGrant(t *testing.T) {
	shape := TaskShape{Kind: contract.ObjectiveMutation}
	if got := PreActionObservationAllowance(shape); got != PreActionObservationFloor {
		t.Fatalf("unmetered allowance = %d, want the floor %d", got, PreActionObservationFloor)
	}
	shape.Budget = BudgetShape{MaxTurns: 100}
	if got := PreActionObservationAllowance(shape); got <= PreActionObservationFloor {
		t.Fatalf("allowance for a 100-turn grant = %d, want more than the floor %d", got, PreActionObservationFloor)
	}
	shape.Budget = BudgetShape{MaxTurns: 10, TurnsUsed: 2}
	if got := PreActionObservationAllowance(shape); got > 8 {
		t.Fatalf("allowance for a 10-turn grant with 2 used = %d, want at most the 8 remaining turns", got)
	}
}

// frontEnd1183 is the recorded failed request. It is a real, bounded change to
// a four-file static site; the first pass needs a page read, a stylesheet read
// in windows, a script read and two searches before the first edit.
const frontEnd1183 = "I want the project section to be displayed in a single horizontal row and add an infinite loop scrolling effect without any lag"

func frontEnd1183Shape(t *testing.T) (TaskShape, string) {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"index.html":  "<html><body><nav></nav><section class=\"projects\"></section></body></html>",
		"styles.css":  strings.Repeat(".project-card{display:grid}\n", 400),
		"script.js":   "console.log('projects')",
		"favicon.svg": "<svg></svg>",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	intent := workspace.BuildIntentContract(frontEnd1183)
	shape := BuildShape(intent, ShapeParams{
		Authority: "build",
		Facts:     ProbeFacts(root),
		Budget:    BudgetShape{MaxTurns: 100},
	})
	if shape.Kind != contract.ObjectiveMutation {
		t.Fatalf("fixture intent = %s, want mutation", shape.Kind)
	}
	for _, need := range []string{"repository_overview", "implementation_observation", "verification"} {
		if !hasNeed(shape, need) {
			t.Fatalf("fixture is not the recorded shape: %q missing from %+v", need, shape.RequiredKnowledge)
		}
	}
	return shape, root
}

// firstPass1183 is the observed pre-edit sequence. Every turn observes new
// content: the page establishes the overview and the implementation fact, then
// the stylesheet is read in windows and the script plus two searches close the
// picture.
func firstPass1183() []agent.Turn {
	return []agent.Turn{
		readTurn("read-index", "index.html", "<html><body><section class=\"projects\"></section></body></html>"),
		grepTurn("grep-projects", "index.html:243:<div class=\"projects__stack\">"),
		readTurn("read-script", "script.js", "console.log('projects')"),
		readTurnWindow("read-css-1", "styles.css", ".projects__stack{display:grid}", 1, 202, 1600),
		grepTurn("grep-marquee", "styles.css:900:.marquee{overflow:hidden}"),
		readTurnWindow("read-css-2", "styles.css", ".marquee__track{animation:marquee}", 880, 62, 1600),
		readTurnWindow("read-css-3", "styles.css", "@media(max-width:900px){.projects__stack{grid}}", 1380, 122, 1600),
		readTurnWindow("read-index-2", "index.html", "<article class=\"project-card\">…</article>", 235, 67, 302),
		readTurnWindow("read-script-2", "script.js", "function initRipple(){}", 470, 24, 493),
		readTurnWindow("read-css-4", "styles.css", ".project-card{padding:24px}", 240, 202, 1600),
		readTurnWindow("read-css-5", "styles.css", ".project-card:hover{transform:none}", 440, 202, 1600),
	}
}

func grepTurn(callID, text string) agent.Turn {
	return agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   callID,
		ToolName: "grep",
		Args:     map[string]any{"pattern": "projects", "path": "."},
		Result:   agent.Result{Text: text, Details: map[string]any{"matches": 1}},
	}}}
}

// A legitimate first pass over a multi-file front end is not an unbounded
// survey. The recorded #1183 run was stopped after nine observations at the
// action boundary, before it could edit anything, because the window was a
// constant unrelated to the project's size. The window must cover a real first
// pass and still close on a model that never acts.
func TestFirstPassOverAMultiFileFrontEndIsNotBlocked(t *testing.T) {
	shape, root := frontEnd1183Shape(t)
	projector := NewProjector(shape, NewEvidenceState(), ProbeFacts(root))
	tools := workingTools()

	turns := firstPass1183()
	for i := range turns {
		projected := projector.Project(agent.ProjectionRequest{Turns: turns[:i+1], Tools: tools, Turn: i})
		if projected.Report.Terminal {
			t.Fatalf("the first pass was closed after %d turns: %s", i+1, projected.Report.StopReason)
		}
		if projected.Report.Stage == string(StageBlocked) {
			t.Fatalf("stage %q after %d turns: %+v", projected.Report.Stage, i+1, projected.Report)
		}
	}
	// The lifecycle still ends the way the request asks: the change is applied
	// and the missing check is recorded as a verification limitation.
	turns = append(turns, mutationTurn("edit-1", "styles.css"), checkNotRun("check-1"))
	final := projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools})
	if final.Report.Stage != string(StageComplete) {
		t.Fatalf("stage = %q, want complete (missing=%v): %s",
			final.Report.Stage, final.Report.Missing, final.Report.StopReason)
	}
	if final.Report.VerificationLimited == "" {
		t.Fatal("the unavailable check was not recorded as a limitation")
	}
}

// The same window still closes: a model that only ever reads new content and
// never applies the change is stopped, and the reason names the observation
// budget rather than a generic failure.
func TestFirstPassWindowClosesWhenTheChangeIsNeverApplied(t *testing.T) {
	shape, root := frontEnd1183Shape(t)
	projector := NewProjector(shape, NewEvidenceState(), ProbeFacts(root))
	tools := workingTools()

	allowance := PreActionObservationAllowance(shape)
	turns := firstPass1183()
	var report agent.ProjectionReport
	terminal := false
	for i := 0; i <= allowance+2; i++ {
		if i >= len(turns) {
			turns = append(turns, readTurnWindow(
				"read-late-"+itoa(i), "styles.css", ".late-rule-"+itoa(i)+"{display:block}", i*10+1, 10, 1600))
		}
		projected := projector.Project(agent.ProjectionRequest{Turns: turns[:i+1], Tools: tools, Turn: i})
		report = projected.Report
		if projected.Report.Terminal {
			terminal = true
			break
		}
	}
	if !terminal {
		t.Fatalf("the observation window never closed: %+v", report)
	}
	if !strings.Contains(report.StopReason, "observation turns") {
		t.Fatalf("stop reason = %q", report.StopReason)
	}
}

func failingCheck(id string) agent.Turn {
	return agent.Turn{Results: []agent.ToolOutcome{{
		CallID:   id,
		ToolName: "check",
		Args:     map[string]any{"mode": "run"},
		Result: agent.Result{
			Text:     "checks: FAIL (1 failing)",
			Executes: true, Finished: true,
			Details: map[string]any{"status": "FAIL"},
		},
	}}}
}

// A failing check is an observation, and the fix that follows it is new work.
// The verification window must restart on the change, or a model iterating
// fix → check is closed as "waiting for its check" while it is working. The
// window still bounds waiting: reading without changing or checking closes it.
func TestVerificationWindowRestartsOnANewChange(t *testing.T) {
	shape := BuildShape(readContract("Fix the failing check in README.md."), ShapeParams{Authority: "build"})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	tools := workingTools()

	var turns []agent.Turn
	project := func() agent.ProjectionReport {
		projected := projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools})
		return projected.Report
	}
	// Seed the tracker, then iterate change → failing check → two reads. A real
	// run projects once per model turn, so each cycle is four projections at the
	// verification boundary, and the cycle makes progress because the change is
	// real.
	project()
	for cycle := 0; cycle < 3; cycle++ {
		cycleTurns := []agent.Turn{
			mutationTurn("edit-"+itoa(cycle), "README.md"),
			failingCheck("check-" + itoa(cycle)),
		}
		for read := 0; read < 2; read++ {
			cycleTurns = append(cycleTurns, readTurnWindow(
				"read-"+itoa(cycle)+"-"+itoa(read), "README.md",
				"# change "+itoa(cycle)+" read "+itoa(read)+"\n", read*10+1, 10, 400))
		}
		for _, turn := range cycleTurns {
			turns = append(turns, turn)
			report := project()
			if report.Terminal {
				t.Fatalf("fix/check cycle %d was closed as waiting: %s", cycle, report.StopReason)
			}
			// The window restarted on the change: at most the four projections
			// of one cycle, not four more accumulated on the previous cycle.
			if report.VerificationTurns > 4 {
				t.Fatalf("cycle %d accumulated %d verification turns; the window did not restart on the change",
					cycle, report.VerificationTurns)
			}
		}
	}

	// Waiting without changing or checking still closes: the window is finite.
	terminal := false
	var report agent.ProjectionReport
	for read := 0; read <= MaxVerifyObservationTurns+2; read++ {
		turns = append(turns, readTurnWindow(
			"wait-"+itoa(read), "README.md", "# wait "+itoa(read)+"\n", read*10+1, 10, 400))
		report = project()
		if report.Terminal {
			terminal = true
			break
		}
	}
	if !terminal {
		t.Fatalf("waiting at the verification boundary never closed: %+v", report)
	}
}

// Waiting at the verification boundary belongs to MaxVerifyObservationTurns.
// The generic no-progress counter used to trip after four reads while the
// model was still implementing the change, closing the surface before the
// dedicated window could end. The recorded #1190 run lost its final edit this
// way: four reads after a stylesheet change, then "No tool is available".
func TestVerificationWaitIsBoundedByItsOwnWindow(t *testing.T) {
	shape := BuildShape(readContract("Update README.md."), ShapeParams{Authority: "build"})
	projector := NewProjector(shape, NewEvidenceState(), RepositoryFacts{Go: true})
	tools := workingTools()

	turns := []agent.Turn{readTurnWindow("read-0", "README.md", "# original\n", 1, 1, 2)}
	projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools})

	turns = append(turns, mutationTurn("edit-1", "README.md"))
	projected := projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools})
	if projected.Report.Stage != string(StageVerify) {
		t.Fatalf("stage after the change = %q, want %q: %s",
			projected.Report.Stage, StageVerify, projected.Report.StopReason)
	}

	// The generic bound is four turns; the change is still being implemented,
	// so the verification window must keep the surface open past it.
	for read := range MaxConsecutiveNoProgress + 1 {
		turns = append(turns, readTurnWindow(
			"wait-"+itoa(read), "README.md", "# wait "+itoa(read)+"\n", read+1, 1, 100))
		projected = projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools})
		if projected.Report.Terminal {
			t.Fatalf("verification wait %d closed the surface early: %s",
				read+1, projected.Report.StopReason)
		}
		if projected.Report.Conclusion != nil {
			t.Fatalf("verification wait %d claimed a terminal conclusion: %+v",
				read+1, projected.Report.Conclusion)
		}
		offered := false
		for _, tool := range projected.Tools {
			if tool.Name() == "edit" {
				offered = true
				break
			}
		}
		if !offered {
			t.Fatalf("verification wait %d withdrew the change capability: %v",
				read+1, names(projected.Tools))
		}
	}

	terminal := false
	for read := range MaxVerifyObservationTurns + 1 {
		turns = append(turns, readTurnWindow(
			"close-"+itoa(read), "README.md", "# close "+itoa(read)+"\n", read+1, 1, 100))
		projected = projector.Project(agent.ProjectionRequest{Turns: turns, Tools: tools})
		if projected.Report.Terminal {
			terminal = true
			break
		}
	}
	if !terminal {
		t.Fatalf("the verification window never closed: %+v", projected.Report)
	}
	if !strings.Contains(projected.Report.StopReason, "verification turns") {
		t.Fatalf("stop reason = %q, want the verification window", projected.Report.StopReason)
	}
	if strings.Contains(projected.Report.StopReason, "consecutive turns") {
		t.Fatalf("the generic counter closed the verification wait: %q", projected.Report.StopReason)
	}
	conclusion := projected.Report.Conclusion
	if conclusion == nil {
		t.Fatalf("terminal verification wait has no conclusion: %+v", projected.Report)
	}
	if conclusion.Status != agent.ConclusionUnresolved {
		t.Fatalf("conclusion status = %q, want UNRESOLVED", conclusion.Status)
	}
	if conclusion.Reason != agent.ConclusionVerificationNotEstablished {
		t.Fatalf("conclusion reason = %q, want verification_not_established", conclusion.Reason)
	}
	if !strings.Contains(strings.ToLower(conclusion.Summary), "verification") {
		t.Fatalf("conclusion summary does not explain the missing verification: %q", conclusion.Summary)
	}
}

func names(tools []agent.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}
