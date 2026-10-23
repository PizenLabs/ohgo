package tui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/charmbracelet/x/ansi"
)

// A greeting produced no evidence to summarise, so the recap must say exactly
// that instead of pretending a task ran.
func TestRecapNarrativeGreetingIsStatic(t *testing.T) {
	got := recapNarrative("  Hello!  ", nil)
	if got != recapGreeting {
		t.Fatalf("greeting recap = %q, want %q", got, recapGreeting)
	}
	if strings.Contains(got, "hi") || strings.Contains(got, "Hello") {
		t.Fatalf("greeting recap echoed the input: %q", got)
	}
}

// A changed file is narrated as a change, and the successful verification is
// named. A failed write cannot be claimed as accomplished work.
func TestRecapNarrativeDescribesChangeAndVerification(t *testing.T) {
	turns := []agent.Turn{recapTurn("Moved the renderer behind the layout seam.",
		agent.ToolOutcome{
			ToolName: "write",
			Args:     map[string]any{"path": "/repo/tui/view.go"},
			Result:   agent.Result{Mutates: true},
		},
		agent.ToolOutcome{
			ToolName: "write",
			Args:     map[string]any{"path": "/repo/tui/abandoned.go"},
			Result:   agent.Result{IsError: true},
		},
		agent.ToolOutcome{
			ToolName: "bash",
			Args:     map[string]any{"command": "go test ./..."},
			Result:   agent.Result{Executes: true, Finished: true, Verifies: true},
		},
	)}

	got := recapNarrative("refactor the rendering engine", turns)
	want := "Updated tui/view.go, then verified the change with go test ./..."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
	if strings.Contains(got, "refactor the rendering engine") {
		t.Fatalf("recap echoed the objective: %q", got)
	}
	if strings.Contains(got, "abandoned.go") {
		t.Fatalf("recap claimed a failed write succeeded: %q", got)
	}
}

// An informational turn is narrated from the assistant's own prose, not from
// the prompt, and carries no duration or token telemetry.
func TestRecapNarrativeDescribesAnswerWithoutEchoingPrompt(t *testing.T) {
	turns := []agent.Turn{recapTurn(
		"# Concurrency\n\nSure, Go's concurrency primitives are goroutines and channels.\n\n```go\nvar wg sync.WaitGroup\n```\n",
		agent.ToolOutcome{
			ToolName: "read",
			Args:     map[string]any{"path": "docs/concurrency.md"},
			Result:   agent.Result{},
		},
	)}

	got := recapNarrative("what are goroutines?", turns)
	want := "Read docs/concurrency.md and explained the core idea: Go's concurrency primitives are goroutines and channels. Included a worked example."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
	if strings.Contains(got, "what are goroutines") {
		t.Fatalf("recap echoed the prompt: %q", got)
	}
	if strings.Contains(got, "tokens") || strings.Contains(got, "0.8s") || strings.Contains(got, "•") {
		t.Fatalf("recap leaked telemetry: %q", got)
	}
}

// A turn that ran a process without changing a file is narrated as execution.
func TestRecapNarrativeDescribesExecution(t *testing.T) {
	turns := []agent.Turn{recapTurn("The suite is green.",
		agent.ToolOutcome{
			ToolName: "bash",
			Args:     map[string]any{"command": "go test ./..."},
			Result:   agent.Result{Executes: true, Finished: true, Verifies: true},
		},
	)}
	got := recapNarrative("run the tests", turns)
	want := "Ran go test ./... and reported the result."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
}

// Every footer carries the stable marker exactly once, even when the stored
// narrative already names it.
func TestRecapLineCarriesMarkerOnce(t *testing.T) {
	if got := recapLine("Updated the parser."); got != "※ recap: Updated the parser." {
		t.Fatalf("recapLine = %q", got)
	}
	if got := recapLine(recapPrefix + " Updated the parser."); strings.Count(got, recapPrefix) != 1 {
		t.Fatalf("recapLine duplicated the marker: %q", got)
	}
	if got := recapLine("   "); got != "" {
		t.Fatalf("empty recap rendered %q", got)
	}
}

// A narrow terminal may fold the narrative onto a second row, but never more:
// the footer must not push the prompt down or lose its marker.
func TestRecapWrapsToAtMostTwoRows(t *testing.T) {
	long := strings.Repeat("The change moved the scheduler behind the layout seam. ", 6)
	rows := recapLines(long, 80)
	if len(rows) != 2 {
		t.Fatalf("recap rows = %d, want 2:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[0], recapPrefix+" ") {
		t.Fatalf("first row = %q, want the marker", rows[0])
	}
	if !strings.HasSuffix(rows[1], "…") {
		t.Fatalf("second row = %q, want a bounded ellipsis", rows[1])
	}
	for _, row := range rows {
		if got := CellWidth(row); got > 80 {
			t.Fatalf("row is %d cells wide:\n%s", got, row)
		}
	}
	if got := recapLines("Updated the parser.", 80); len(got) != 1 {
		t.Fatalf("short recap rows = %d, want 1", len(got))
	}
}

// The recap is the quietest element: italic and the dedicated dim token, so
// closure never reads as a second answer.
func TestRecapStyleIsQuietItalic(t *testing.T) {
	for name, pal := range map[string]*Palette{
		"dark":  DarkPalette(),
		"light": LightPalette(),
	} {
		if !pal.Recap.GetItalic() {
			t.Errorf("%s recap style is not italic", name)
		}
	}
	if got := colorName(DarkPalette().Recap.GetForeground()); got != "#5C7E70" {
		t.Errorf("dark recap foreground = %q, want #5C7E70", got)
	}
}

// The footer must sit on the row directly above the composer prefix, not
// inside the task block or separated by a blank transcript row.
func TestRecapSitsDirectlyAboveComposer(t *testing.T) {
	state := New()
	state.Status.Workspace = "ASK"
	state.Apply(TerminalResized{Width: 80, Height: 40})
	state.Apply(UserSubmitted{Text: "explain go"})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "RUNNING", Objective: "explain go",
	}})
	state.Apply(AssistantDelta{Text: "Goroutines are lightweight threads."})
	state.Apply(TaskProjected{Projection: TaskProjection{
		ID: "t1", Status: "COMPLETED", Objective: "explain go",
		Recap: "Explained the core idea: goroutines are lightweight threads.",
	}})
	state.Apply(AssistantDone{})

	lines := strings.Split(ansi.Strip(Render(state)), "\n")
	composer := -1
	for i, line := range lines {
		if strings.Contains(line, "ask >") {
			composer = i
			break
		}
	}
	if composer < 1 {
		t.Fatalf("the composer row is missing:\n%s", strings.Join(lines, "\n"))
	}
	// The composer frame draws its own rule; the nearest transcript content
	// above it must be the recap, with no answer or task row in between.
	nearest := ""
	for i := composer - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.Trim(line, "─") == "" {
			continue
		}
		nearest = line
		break
	}
	if !strings.HasPrefix(nearest, recapPrefix+" ") {
		t.Fatalf("nearest transcript row above the composer = %q, want the recap:\n%s", nearest, strings.Join(lines, "\n"))
	}
}

// recapTurn builds one completed turn with an assistant answer and outcomes.
func recapTurn(answer string, outcomes ...agent.ToolOutcome) agent.Turn {
	return agent.Turn{
		Assistant: ai.AssistantMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: answer}},
		},
		Results: outcomes,
	}
}
