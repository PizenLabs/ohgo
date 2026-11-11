package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// The wide canvas is a real split: the transcript gets a narrower column and
// the rail stays inside the requested 25–30% band. Narrow/medium terminals do
// not crush the conversation; they reserve one sticky task row instead.
func TestWorkspaceLayoutSplitsWideAndFallsBackToStickyTask(t *testing.T) {
	task := TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "ship the parser",
		Items: []TaskItem{{State: taskStateActive, Label: "wire the lexer"}},
	}
	wide := New()
	wide.Apply(TerminalResized{Width: 140, Height: 40})
	wide.Apply(TaskProjected{Projection: task})
	layout := ComputeWorkspaceLayout(wide)
	if !layout.Sidebar || layout.TaskLines != 0 {
		t.Fatalf("wide layout = sidebar:%v taskRows:%d, want the split rail", layout.Sidebar, layout.TaskLines)
	}
	if pct := layout.SidebarWidth * 100 / layout.Width; pct < 25 || pct > 30 {
		t.Fatalf("sidebar = %d/%d = %d%%, want the 25–30%% band", layout.SidebarWidth, layout.Width, pct)
	}
	if layout.ContentWidth >= ComputeLayout(wide).ContentWidth {
		t.Fatalf("split content width = %d, want narrower than the full canvas", layout.ContentWidth)
	}

	narrow := New()
	narrow.Apply(TerminalResized{Width: 80, Height: 30})
	narrow.Apply(TaskProjected{Projection: task})
	narrowLayout := ComputeWorkspaceLayout(narrow)
	if narrowLayout.Sidebar || narrowLayout.TaskLines != 1 || !narrowLayout.TaskTracker {
		t.Fatalf("medium layout = sidebar:%v taskRows:%d tracker:%v, want the sticky header",
			narrowLayout.Sidebar, narrowLayout.TaskLines, narrowLayout.TaskTracker)
	}
}

// The rail is transient chrome, not the default canvas. Idle sessions and
// terminal tasks keep the full-width transcript, even when integrations are
// available; a completed task settles back into history instead of pinning a
// panel beside every later turn.
func TestWorkspaceRailOnlyExistsWhileTaskIsOpen(t *testing.T) {
	integrations := IntegrationsUpdated{Items: []IntegrationStatus{
		{Kind: IntegrationMCP, Name: "github", State: "connected", Active: true},
	}}
	assertNoRail := func(t *testing.T, state State) {
		t.Helper()
		layout := ComputeWorkspaceLayout(state)
		if layout.Sidebar || layout.TaskLines != 0 || layout.TaskTracker {
			t.Fatalf("layout = sidebar:%v taskRows:%d tracker:%v, want no rail",
				layout.Sidebar, layout.TaskLines, layout.TaskTracker)
		}
		if layout.MainWidth != state.Width || layout.ContentWidth != ComputeLayout(state).ContentWidth {
			t.Fatalf("main = %d/%d, want the full-width transcript", layout.MainWidth, state.Width)
		}
		out := ansi.Strip(Render(state))
		for _, heading := range []string{"TASK", "ACTIVE INTEGRATIONS", "INSIGHTS"} {
			if strings.Contains(out, heading) {
				t.Fatalf("hidden rail still rendered %q:\n%s", heading, out)
			}
		}
	}

	idle := New()
	idle.Apply(TerminalResized{Width: 140, Height: 40})
	idle.Apply(integrations)
	assertNoRail(t, idle)

	done := New()
	done.Apply(TerminalResized{Width: 140, Height: 40})
	done.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "prove the parser",
		Items: []TaskItem{{State: taskStateActive, Label: "run the suite"}},
	}})
	done.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "COMPLETED", Objective: "prove the parser",
		Items: []TaskItem{{State: taskStateDone, Label: "run the suite"}},
	}})
	done.Apply(integrations)
	assertNoRail(t, done)

	// Narrow tiers follow the same lifecycle: the sticky fallback is not a
	// permanent row under the title bar either.
	narrowDone := done
	narrowDone.Apply(TerminalResized{Width: 80, Height: 24})
	assertNoRail(t, narrowDone)

	// The completed projection is no longer pinned, but it remains history:
	// the transcript owns it again after the rail releases the fact.
	if out := ansi.Strip(Render(done)); !strings.Contains(out, "prove the parser") {
		t.Fatalf("completed task vanished from history:\n%s", out)
	}
}

// A task without a todo plan is still a normal question even though the task
// layer records an objective. The rail must not take over the canvas until the
// execution request has produced an actual todo plan.
func TestWorkspaceRailRequiresTodoPlan(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 140, Height: 40})
	s.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "explain the module",
	}})
	layout := ComputeWorkspaceLayout(s)
	if layout.Sidebar || layout.TaskLines != 0 || layout.TaskTracker {
		t.Fatalf("question layout = sidebar:%v taskRows:%d tracker:%v, want the normal transcript",
			layout.Sidebar, layout.TaskLines, layout.TaskTracker)
	}
	out := ansi.Strip(Render(s))
	if strings.Contains(out, "ACTIVE INTEGRATIONS") || strings.Contains(out, "INSIGHTS") {
		t.Fatalf("a question opened the task rail:\n%s", out)
	}
	if !strings.Contains(out, "explain the module") {
		t.Fatalf("the task projection vanished from the transcript:\n%s", out)
	}

	// A stale finished plan is not work to track either: the rail waits for an
	// open todo, not merely a todo list.
	s.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "explain the module",
		Items: []TaskItem{{State: taskStateDone, Label: "read go.mod"}},
	}})
	if layout := ComputeWorkspaceLayout(s); layout.Sidebar || layout.TaskTracker {
		t.Fatalf("layout = sidebar:%v tracker:%v, want no rail for a finished plan", layout.Sidebar, layout.TaskTracker)
	}

	// Once the request creates open todos, the rail has something to track.
	s.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "explain the module",
		Items: []TaskItem{{State: taskStateActive, Label: "read go.mod"}},
	}})
	if layout := ComputeWorkspaceLayout(s); !layout.Sidebar {
		t.Fatalf("layout = %+v, want the rail after the todo plan appeared", layout)
	}
}

// The sidebar owns the task projection on wide terminals. The transcript must
// not print the same objective or todo rows a second time, and the right rail
// must not repeat the global footer's tokens, path, branch, or session.
func TestSidebarOwnsTaskAndExcludesFooterFacts(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 140, Height: 40})
	s.Status.UsageText = "1.2k tok · $0.02"
	s.Status.Cwd = "/work/root"
	s.Status.GitBranch = "feature/rail"
	s.Status.Session = "#9"
	s.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "NEEDS_REVIEW", Objective: "ship the parser",
		ReviewReason: "targeted tests did not run",
		Items: []TaskItem{
			{State: taskStateDone, Label: "wire the lexer"},
			{State: taskStateActive, Label: "run the parser suite"},
		},
	}})
	s.Apply(IntegrationsUpdated{Items: []IntegrationStatus{
		{Kind: IntegrationMCP, Name: "github", State: "connected", Active: true, Detail: "local · 3 capabilities"},
		{Kind: IntegrationLSP, Name: "gopls", State: "ready", Active: true, Detail: "go"},
	}})

	out := ansi.Strip(Render(s))
	for _, heading := range []string{"TASK", "INTEGRATIONS", "INSIGHTS"} {
		if !strings.Contains(out, heading) {
			t.Errorf("sidebar lost %q:\n%s", heading, out)
		}
	}
	if got := strings.Count(out, "ship the parser"); got != 1 {
		t.Fatalf("task objective appeared %d times, want only the sidebar copy:\n%s", got, out)
	}
	if got := strings.Count(out, "wire the lexer"); got != 1 {
		t.Fatalf("todo appeared %d times, want only the sidebar copy:\n%s", got, out)
	}
	for _, want := range []string{"● MCP · github · connected", "● LSP · gopls · ready", "review: targeted tests did not run"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q:\n%s", want, out)
		}
	}

	// The right column is everything after the last divider on each body row.
	for _, line := range strings.Split(out, "\n") {
		idx := strings.LastIndex(line, "│")
		if idx < 0 {
			continue
		}
		rail := line[idx+1:]
		for _, forbidden := range []string{"1.2k tok", "feature/rail", "#9", "/work/root"} {
			if strings.Contains(rail, forbidden) {
				t.Errorf("sidebar repeated footer fact %q: %q", forbidden, rail)
			}
		}
	}
}

// The sticky fallback sits immediately below the title separator and survives
// a scroll without turning into a second transcript row.
func TestStickyTaskRowStaysBelowTitleBar(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "keep the task pinned",
		Items: []TaskItem{{State: taskStateActive, Label: "follow the cursor"}},
	}})
	s.Apply(ScrollMoved{Delta: 5})
	lines := strings.Split(ansi.Strip(Render(s)), "\n")
	if len(lines) < 4 {
		t.Fatalf("render too short for chrome:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "ohgo") {
		t.Fatalf("row 0 = %q, want the title bar", lines[0])
	}
	if strings.Trim(lines[1], "─") != "" {
		t.Fatalf("row 1 = %q, want the title separator", lines[1])
	}
	if !strings.Contains(lines[2], "task") || !strings.Contains(lines[2], "keep the task pinned") {
		t.Fatalf("row 2 = %q, want the sticky task tracker immediately below the title bar", lines[2])
	}
	if got := strings.Count(strings.Join(lines, "\n"), "keep the task pinned"); got != 1 {
		t.Fatalf("sticky objective appeared %d times, want one:\n%s", got, strings.Join(lines, "\n"))
	}
}

// A mutation result exposes its canonical diff when expanded: path, change
// metrics, syntax highlighting layered over the add/delete tint, and the exact
// collapse key.
func TestExpandedToolRendersCodeDiff(t *testing.T) {
	prev := termenv.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	diff := "diff --git a/internal/parser.go b/internal/parser.go\n" +
		"--- a/internal/parser.go\n" +
		"+++ b/internal/parser.go\n" +
		"@@ -1,2 +1,3 @@\n" +
		" package parser\n" +
		"+func Added() {\n" +
		"+}\n" +
		"-var old = 1\n"
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 40})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "edit", Summary: "edit internal/parser.go", At: now})
	s.Apply(ToolFinished{ID: "c1", Text: "", Summary: "", Diff: diff, Path: "internal/parser.go", Operation: "MODIFY", At: now})
	s.Apply(FrameTick{At: now.Add(toolMinDwell)})
	s.Apply(ToolToggled{ID: "c1"})

	out := Render(s)
	plain := ansi.Strip(out)
	for _, want := range []string{"internal/parser.go", "[+2, -1]", "ctrl+o to collapse", "+ func Added() {"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expanded diff missing %q:\n%s", want, plain)
		}
	}
	// Dark add/delete tints are part of the row style, not just the mark.
	addTint := "48;2;" + rgbSeq(DarkTokens().DiffAddBackground)
	delTint := "48;2;" + rgbSeq(DarkTokens().DiffDeleteBackground)
	for _, tint := range []string{addTint, delTint} {
		if !strings.Contains(out, tint) {
			t.Errorf("expanded diff missing row tint %s", tint)
		}
	}
}

// The permission surface separates metadata from the payload and uses the same
// tinted add/delete semantics as the expanded transcript diff.
func TestApprovalMetadataAndTintedPreview(t *testing.T) {
	prev := termenv.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	diff := "@@ -1,2 +1,3 @@\n" +
		" package parser\n" +
		"+func Added() {\n" +
		"+}\n" +
		"-var old = 1\n"
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 30})
	s.Apply(ApprovalAsked{
		ToolName: "edit", Summary: "edit internal/parser.go", Mutating: true,
		Choices: approvalChoices,
		MutationPreview: agent.MutationPreview{
			Path: "internal/parser.go", Operation: agent.MutationModify, Diff: diff,
		},
	})
	out := Render(s)
	plain := ansi.Strip(out)
	for _, want := range []string{
		"Capability · workspace mutation",
		"Risk · elevated",
		"Preview · MODIFY · internal/parser.go",
		"[+2, -1]",
		"+ func Added() {",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("approval missing %q:\n%s", want, plain)
		}
	}
	addTint := "48;2;" + rgbSeq(DarkTokens().DiffAddBackground)
	delTint := "48;2;" + rgbSeq(DarkTokens().DiffDeleteBackground)
	for _, tint := range []string{addTint, delTint} {
		if !strings.Contains(out, tint) {
			t.Errorf("approval diff missing row tint %s", tint)
		}
	}
}

// Reasoning and responding share the throttled Braille rhythm. The frame
// changes the glyph only; the label and metadata stay fixed.
func TestSpinnerFramesAreThrottledAndReasoningAnimated(t *testing.T) {
	if spinnerInterval < 80*time.Millisecond || spinnerInterval > 100*time.Millisecond {
		t.Fatalf("spinner interval = %v, want the 80–100ms band", spinnerInterval)
	}
	for tick, want := range spinnerFrames {
		if got := spinnerGlyph(tick); got != want {
			t.Fatalf("spinnerGlyph(%d) = %q, want %q", tick, got, want)
		}
	}
	state := ThinkingState{Active: true, Phase: ThinkingRespond, Label: "respond"}
	first := ansi.Strip(RenderThinkingFrame(state, time.Now(), 0, 80)[0])
	second := ansi.Strip(RenderThinkingFrame(state, time.Now(), 1, 80)[0])
	if !strings.HasPrefix(first, spinnerFrames[0]+" respond") || !strings.HasPrefix(second, spinnerFrames[1]+" respond") {
		t.Fatalf("frames = %q / %q, want the first two spinner frames", first, second)
	}

	at := time.Now()
	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(UserSubmitted{Text: "go", At: at})
	s.Apply(ReasoningDelta{Text: "inspect the parser", At: at})
	live := ansi.Strip(Render(s))
	if !strings.Contains(live, spinnerFrames[0]+" Thought") {
		t.Fatalf("open reasoning lost its spinner head:\n%s", live)
	}
	s.Apply(AssistantDelta{Text: "answer", At: at})
	s.Apply(FrameTick{At: at.Add(toolMinDwell)})
	settled := ansi.Strip(Render(s))
	for _, line := range strings.Split(settled, "\n") {
		if strings.Contains(line, "Thought") && strings.ContainsAny(line, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
			t.Fatalf("settled reasoning kept a spinner: %q", line)
		}
	}
}

// The MCP adapter keeps the sidebar truthful: disabled servers are omitted,
// connected is a live state, and enabled-but-not-connected is not promoted to
// connected.
func TestProjectMCPIntegrationsKeepsTruthfulState(t *testing.T) {
	rows := []MCPServerRow{
		{Name: "github", Enabled: true, Connected: true, Cached: true, Capabilities: 3, Trust: "local"},
		{Name: "configured", Enabled: true},
		{Name: "disabled", Enabled: false},
		{Name: "broken", Enabled: true, Error: "connection failed"},
	}
	items := ProjectMCPIntegrations(rows)
	if len(items) != 3 {
		t.Fatalf("items = %+v, want enabled/error rows only", items)
	}
	if !items[0].Active || items[0].State != "connected" {
		t.Errorf("connected row = %+v, want active connected", items[0])
	}
	if items[1].Active || items[1].State != "enabled" {
		t.Errorf("configured row = %+v, want enabled but not active", items[1])
	}
	if items[2].State != "error" {
		t.Errorf("failed row = %+v, want error", items[2])
	}
}
