package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/control/cognition"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/execution/tools"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/PizenLabs/ohgo/internal/provider/catalog"
)

// regression1183Request is the recorded task that was closed as "no meaningful
// task progress ... the action boundary was available for 9 turns without a
// change" before the model had edited anything.
const regression1183Request = "I want the project section to be displayed in a single horizontal row and add an infinite loop scrolling effect without any lag"

// regression1183Styles is the implementation the request asks for: the stack
// becomes one row and a transform-only marquee loops without layout work.
const regression1183Styles = `body{margin:0}
.projects__stack{display:flex;gap:24px;overflow:hidden}
.projects__track{display:flex;gap:24px;width:max-content;animation:projects-marquee 28s linear infinite;will-change:transform}
@keyframes projects-marquee{from{transform:translate3d(0,0,0)}to{transform:translate3d(-50%,0,0)}}
@media (prefers-reduced-motion: reduce){.projects__track{animation:none}}
`

func siteFixture1183(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"index.html":  "<!doctype html><html><body><nav></nav><section class=\"projects\"><div class=\"projects__stack\"></div></section></body></html>",
		"styles.css":  strings.Repeat(".project-card{display:grid;padding:24px}\n", 600),
		"script.js":   "document.querySelectorAll('.project-card').forEach(card => card.dataset.ready = '1')\n",
		"favicon.svg": "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestRegression1183FirstPassIsNotBlockedBeforeTheChange is the recorded
// failure from the other direction: the run was stopped as "no meaningful task
// progress" while it was reading the page, the stylesheet in windows and the
// script, before it could apply the requested change. A bounded task must be
// able to reach its change; the observation window is a share of the turn
// grant, so a legitimate first pass over a multi-file project is not mistaken
// for a survey that never ends.
func TestRegression1183FirstPassIsNotBlockedBeforeTheChange(t *testing.T) {
	root := siteFixture1183(t)
	intent := workspace.BuildIntentContract(regression1183Request)
	if intent.Kind != workspace.ObjectiveMutation {
		t.Fatalf("fixture intent = %s, want mutation", intent.Kind)
	}
	facts := cognition.ProbeFacts(root)
	source := tools.NewStructuralSource(root)
	read := &countingTool{inner: tools.NewReadTool(root)}
	write := &countingTool{inner: tools.NewWriteTool(root)}
	grep := &countingTool{inner: tools.NewGrepTool(root)}
	check := &countingTool{inner: tools.NewCheckTool(source)}

	// The observed first pass: an overview read, the project section search, the
	// stylesheet in windows, the script, and a second search. Eleven
	// observations before the first mutation is exactly the shape the fixed
	// eight-turn act window rejected.
	script := []ai.AssistantMessage{
		callTurn("read-1", "read", map[string]any{"path": "index.html"}),
		callTurn("grep-1", "grep", map[string]any{"pattern": "projects__stack", "path": "."}),
		callTurn("read-2", "read", map[string]any{"path": "styles.css", "offset": 1, "limit": 120}),
		callTurn("grep-2", "grep", map[string]any{"pattern": "project-card", "path": "."}),
		callTurn("read-3", "read", map[string]any{"path": "styles.css", "offset": 121, "limit": 120}),
		callTurn("read-4", "read", map[string]any{"path": "styles.css", "offset": 241, "limit": 120}),
		callTurn("read-5", "read", map[string]any{"path": "styles.css", "offset": 361, "limit": 120}),
		callTurn("read-6", "read", map[string]any{"path": "script.js"}),
		callTurn("grep-3", "grep", map[string]any{"pattern": "reveal", "path": "."}),
		callTurn("read-7", "read", map[string]any{"path": "styles.css", "offset": 481, "limit": 120}),
		callTurn("write-1", "write", map[string]any{"path": "styles.css", "content": regression1183Styles}),
		callTurn("check-1", "check", map[string]any{"mode": "run"}),
	}
	runaway := callTurn("read-runaway", "read", map[string]any{"path": "styles.css", "offset": 601, "limit": 120})
	stream, requests := countingStream(script, runaway)

	opts := options{root: root, settings: config.Default(), autoApprove: true}
	opts.setAuthority(workspace.Build)
	loop, err := agent.New(agent.Config{
		Model:     catalog.MustResolve("anthropic/claude-sonnet-4-5"),
		Stream:    stream,
		Tools:     []agent.Tool{read, write, grep, check, tools.NewBashTool(root)},
		Authorize: opts.newSecurityGate(nil),
		MaxTurns:  60,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	shape := cognition.BuildShape(intent, cognition.ShapeParams{
		Authority: "build",
		Facts:     facts,
		Budget:    cognition.BudgetShapeFrom(loop.Budget().Snapshot()),
	})
	projector := cognition.NewProjector(shape, cognition.NewEvidenceState(), facts)
	var (
		lastReport agent.ProjectionReport
		blockedAt  string
	)
	loop.SetGrounding(intent.Grounding)
	loop.SetProjectionHook(projector.Project)
	loop.SetProjectionReportHook(func(report agent.ProjectionReport) {
		lastReport = report
		if report.Terminal && blockedAt == "" {
			blockedAt = report.StopReason
		}
	})

	turns, err := loop.Run(context.Background(), []agent.Message{agent.UserMessage{Text: regression1183Request}})
	if err != nil && !errors.Is(err, agent.ErrTaskBlocked) {
		t.Fatalf("Run: %v", err)
	}
	if blockedAt != "" {
		t.Fatalf("the first pass was closed before the change: %s", blockedAt)
	}
	// The change must actually run: the recorded failure never reached it.
	if got := len(write.invoked()); got != 1 {
		t.Fatalf("write ran %d time(s), want 1; the run never applied the change (requests=%d)", got, requests())
	}
	if got := len(read.invoked()); got < 7 {
		t.Fatalf("read ran %d time(s), want the recorded first pass; the fixture did not exercise it", got)
	}
	if got := len(grep.invoked()); got != 3 {
		t.Fatalf("grep ran %d time(s), want the three searches in the recorded first pass", got)
	}

	body, readErr := os.ReadFile(filepath.Join(root, "styles.css"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, want := range []string{
		".projects__stack{display:flex",
		"animation:projects-marquee",
		"transform:translate3d(-50%,0,0)",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("implementation is missing %q:\n%s", want, body)
		}
	}

	if lastReport.Stage != string(cognition.StageComplete) {
		t.Fatalf("final stage = %q, want complete (missing=%v): %+v",
			lastReport.Stage, lastReport.Missing, lastReport)
	}
	if lastReport.VerificationLimited == "" {
		t.Fatalf("no verification limitation was recorded: %+v", lastReport)
	}
	outcome := projectRun(nil, turns, intent, nil, nil, false, lastReport.EvidenceReview(),
		lastReport.Provenance, loop.LastMutation(), lastReport.Unresolved, lastReport.VerificationLimited)
	if outcome.State != runCompleted {
		t.Fatalf("outcome = %s (%s), want completed", outcome.State, outcome.Reason)
	}
	if !strings.Contains(outcome.line(), "COMPLETED") {
		t.Fatalf("terminal line = %q", outcome.line())
	}
}
