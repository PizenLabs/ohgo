package tui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
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
	want := "Updated tui/view.go, then verified the change with go test ./... — Moved the renderer behind the layout seam."
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
	want := "Inspected docs/concurrency.md — Go's concurrency primitives are goroutines and channels; included a worked example."
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
	want := "Ran go test ./... — the suite is green."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
}

// A live runtime observation is the result the recap must narrate. The
// environment probes that ran while the model oriented itself are real
// commands, but naming them attributes the proof to the wrong action: the
// runtime came from "npm run dev", not from "node -v".
func TestRecapNarrativeAttributesRuntimeObservation(t *testing.T) {
	turns := []agent.Turn{recapTurn("The project is running.",
		agent.ToolOutcome{
			ToolName: "bash",
			Args:     map[string]any{"command": "node -v; npm -v; ls node_modules"},
			Result:   agent.Result{Executes: true, Finished: true},
		},
		agent.ToolOutcome{
			ToolName: "bash",
			Args:     map[string]any{"command": "ls app"},
			Result:   agent.Result{},
		},
		agent.ToolOutcome{
			ToolName: "verify",
			Args:     map[string]any{"service": map[string]any{"command": "npm run dev"}},
			Result: agent.Result{
				Executes: true,
				Finished: true,
				Verifies: true,
				Runtime: &agent.RuntimeEvidence{
					Command: "npm run dev",
					URL:     "http://localhost:3000",
					PID:     11260,
					Checks:  2,
				},
			},
		},
	)}

	got := recapNarrative("run this project help me", turns)
	want := "Ran npm run dev and verified http://localhost:3000 answered 2 checks — the project is running."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
	if strings.Contains(got, "node -v") || strings.Contains(got, "ls node_modules") {
		t.Fatalf("recap attributed the runtime to an environment probe: %q", got)
	}
}

// Every footer carries the stable marker exactly once, even when the stored
// narrative already names it.
func TestRecapLineCarriesMarkerOnce(t *testing.T) {
	if got := recapLine("Updated the parser."); got != "※ recap · Updated the parser." {
		t.Fatalf("recapLine = %q", got)
	}
	if got := recapLine(recapPrefix + " Updated the parser."); strings.Count(got, recapPrefix) != 1 {
		t.Fatalf("recapLine duplicated the marker: %q", got)
	}
	if got := recapLine("   "); got != "" {
		t.Fatalf("empty recap rendered %q", got)
	}
}

// A long recap wraps onto as many rows as it needs, indented under the
// narrative, and never ends in an ellipsis: completion must not look unfinished.
func TestRecapWrapsLongNarrativeWithoutEllipsis(t *testing.T) {
	long := strings.Repeat("The change moved the scheduler behind the layout seam. ", 6)
	rows := recapLines(long, 80)
	if len(rows) <= 2 {
		t.Fatalf("recap rows = %d, want a natural multiline wrap:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	if !strings.HasPrefix(rows[0], recapPrefix+" ") {
		t.Fatalf("first row = %q, want the marker", rows[0])
	}
	indent := strings.Repeat(" ", CellWidth(recapPrefix)+1)
	for i, row := range rows {
		if got := CellWidth(row); got > 80 {
			t.Fatalf("row is %d cells wide:\n%s", got, row)
		}
		if strings.Contains(row, "…") {
			t.Fatalf("row %d is ellipsized: %q", i, row)
		}
		if i > 0 && !strings.HasPrefix(row, indent) {
			t.Fatalf("continuation row %d is not indented under the narrative: %q", i, row)
		}
	}
	// Wrapping reflows whitespace but must not drop any word.
	var wrapped []string
	for i, row := range rows {
		if i == 0 {
			wrapped = append(wrapped, strings.TrimPrefix(row, recapPrefix+" "))
			continue
		}
		wrapped = append(wrapped, strings.TrimSpace(row))
	}
	if got, want := strings.Join(strings.Fields(strings.Join(wrapped, " ")), " "),
		strings.Join(strings.Fields(strings.TrimPrefix(recapLine(long), recapPrefix+" ")), " "); got != want {
		t.Fatalf("wrapped narrative lost content:\ngot  %q\nwant %q", got, want)
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
	if got := colorName(DarkPalette().Recap.GetForeground()); got != DarkTokens().Recap {
		t.Errorf("dark recap foreground = %q, want the dim token %s", got, DarkTokens().Recap)
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
		if strings.Contains(line, "ask ›") {
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
		if line == "" || strings.Trim(line, "─") == "" || isComposerBorderHeader(line) {
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

// A listing is not a read. The recap must not claim the run read a directory,
// and it must not restate a one-line answer as "the core idea".
func TestRecapNamesOnlyFilesActuallyRead(t *testing.T) {
	turns := []agent.Turn{recapTurn("The module name is ecommerce.",
		agent.ToolOutcome{
			ToolName: "ls",
			Args:     map[string]any{"path": "/Users/x/Golang/go-ecommerce/backend"},
			Result:   agent.Result{},
		},
		agent.ToolOutcome{
			ToolName: "read",
			Args:     map[string]any{"path": "backend/go.mod"},
			Result:   agent.Result{},
		},
	)}
	got := recapNarrative("Use ls to inspect backend/, read backend/go.mod, then tell me the module name.", turns)
	want := "Inspected backend/go.mod — the module name is ecommerce."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
	if strings.Contains(got, "Answered:") {
		t.Fatalf("recap fell back to an answer label: %q", got)
	}
	if strings.Count(got, "backend/go.mod") != 1 {
		t.Fatalf("recap repeated the inspected path: %q", got)
	}
}

// A completed turn with no answer and no inspected file must not claim an
// answer it cannot show.
func TestRecapWithoutAnAnswerIsHonest(t *testing.T) {
	got := recapNarrative("do the work", []agent.Turn{{}})
	if got != "" {
		t.Fatalf("recap = %q, want no fabricated closure", got)
	}
	if strings.Contains(got, "Closed the task") || strings.Contains(got, "returned to the prompt") {
		t.Fatalf("recap fabricated boilerplate: %q", got)
	}
}

// A long finding is carried whole. The recap is prose, not a badge: display
// wrapping owns its width, so generating an ellipsis would discard the answer's
// own conclusion.
func TestRecapKeepsLongFindingWhole(t *testing.T) {
	long := strings.Repeat("word ", 40)
	got := recapNarrative("explain", []agent.Turn{recapTurn(long)})
	if strings.Contains(got, "…") {
		t.Fatalf("recap = %q, want no generated ellipsis", got)
	}
	if n := strings.Count(got, "word"); n != 40 {
		t.Fatalf("recap kept %d of 40 words: %q", n, got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Fatalf("recap = %q, want one terminal period", got)
	}
}

// A meta sentence that announces the analysis is not the analysis. The recap
// must skip it and carry the first substantive finding.
func TestRecapSkipsAnAnalysisLeadIn(t *testing.T) {
	turns := []agent.Turn{recapTurn(
		"Based on the evidence gathered, here's my analysis of the project.\n\nThe services are health-check skeletons.",
		agent.ToolOutcome{
			ToolName: "read",
			Args:     map[string]any{"path": "backend/go.mod"},
			Result:   agent.Result{},
		},
	)}
	got := recapNarrative("check this project", turns)
	want := "Inspected backend/go.mod — the services are health-check skeletons."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
}

// A sentence that announces an observation the turn is about to make is not a
// finding: the turn is over, and repeating it in the footer promises work that
// will never happen after the completion line.
func TestRecapDropsDeferredAnnouncement(t *testing.T) {
	turns := []agent.Turn{recapTurn(
		"The project launched and passed verification. Let me check the project metadata to give you a complete picture.")}
	got := recapNarrative("run this project help me", turns)
	if strings.Contains(got, "Let me check") {
		t.Fatalf("recap promised work after the turn: %q", got)
	}
	if !strings.Contains(got, "launched and passed verification") {
		t.Fatalf("recap lost the substantive finding: %q", got)
	}
}

// recapReadOutcome builds a file read with the range the read tool publishes.
func recapReadOutcome(path, text string, totalLines, limit int) agent.ToolOutcome {
	details := map[string]any{"path": path}
	if totalLines > 0 && limit > 0 {
		details["offset"] = 1
		details["limit"] = limit
		details["totalLines"] = totalLines
	}
	return agent.ToolOutcome{
		ToolName: "read",
		Args:     map[string]any{"path": path},
		Result:   agent.Result{Text: text, Details: details},
	}
}

// A survey reads README and settings to orient itself, then the source that
// answers the question. The footer must cite the file the answer is about, not
// the first file the run opened, and it must disclose that it was not the only
// one.
func TestRecapNamesTheFileTheAnswerCites(t *testing.T) {
	turns := []agent.Turn{recapTurn(
		"This is a Next.js 16 portfolio site (see app/portfolio.tsx).",
		recapReadOutcome("README.md", strings.Repeat("readme ", 800), 0, 0),
		recapReadOutcome("package.json", strings.Repeat("dep ", 200), 0, 0),
		recapReadOutcome("app/portfolio.tsx", strings.Repeat("jsx ", 2000), 0, 0),
	)}
	got := recapNarrative("check this project", turns)
	want := "Inspected 3 files, including app/portfolio.tsx and README.md — this is a Next.js 16 portfolio site."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
}

// More reads than the footer can name are counted, not silently dropped.
func TestRecapReportsEveryFileItRead(t *testing.T) {
	turns := []agent.Turn{recapTurn("The services are health-check skeletons.",
		recapReadOutcome("services/a/main.go", strings.Repeat("a", 900), 0, 0),
		recapReadOutcome("services/b/main.go", strings.Repeat("b", 800), 0, 0),
		recapReadOutcome("services/c/main.go", strings.Repeat("c", 700), 0, 0),
		recapReadOutcome("services/d/main.go", strings.Repeat("d", 600), 0, 0),
	)}
	got := recapNarrative("check this project", turns)
	if !strings.HasPrefix(got, "Inspected 4 files, including ") {
		t.Fatalf("recap hid the read count: %q", got)
	}
	if !strings.Contains(got, "a/main.go and b/main.go") {
		t.Fatalf("recap did not name the largest reads: %q", got)
	}
}

// A read that returned a window is disclosed: the footer must not imply a
// whole-file reading the run never performed.
func TestRecapDisclosesATruncatedRead(t *testing.T) {
	turns := []agent.Turn{recapTurn("A single-screen animated portfolio.",
		recapReadOutcome("app/portfolio.tsx", strings.Repeat("jsx ", 400), 512, 400),
	)}
	got := recapNarrative("what is this project", turns)
	want := "Inspected app/portfolio.tsx (truncated at 400 of 512 lines) — a single-screen animated portfolio."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
}

// With several files read, the truncation clause names which one was cut.
func TestRecapNamesTheTruncatedFile(t *testing.T) {
	turns := []agent.Turn{recapTurn("A single-screen animated portfolio.",
		recapReadOutcome("app/portfolio.tsx", strings.Repeat("jsx ", 400), 512, 400),
		recapReadOutcome("app/page.tsx", "export default function Page() {}", 0, 0),
	)}
	got := recapNarrative("what is this project", turns)
	want := "Inspected app/portfolio.tsx and app/page.tsx (app/portfolio.tsx was truncated at 400 of 512 lines) — a single-screen animated portfolio."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
}

// Citation outranks volume: the file the answer names is the evidence the
// reader needs, even when another read returned far more text.
func TestRecapCitesTheFileEvenWhenItIsSmall(t *testing.T) {
	turns := []agent.Turn{recapTurn(
		"The route in app/page.tsx renders the portfolio.",
		recapReadOutcome("app/portfolio.tsx", strings.Repeat("jsx ", 3000), 0, 0),
		recapReadOutcome("app/page.tsx", "export default function Page() {}", 0, 0),
	)}
	got := recapNarrative("what does the app render", turns)
	// The action already names the file, so the finding drops its own citation.
	want := "Inspected app/page.tsx and app/portfolio.tsx — the route renders the portfolio."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
}

// A paged read is described as a window, not as "truncated at": the run read
// lines 200-400, so "truncated at 400 of 512" would imply it saw the head.
func TestRecapDescribesAPagedReadAsAWindow(t *testing.T) {
	turns := []agent.Turn{recapTurn("The contact section lists an email address.",
		agent.ToolOutcome{
			ToolName: "read",
			Args:     map[string]any{"path": "app/portfolio.tsx"},
			Result: agent.Result{
				Text:    strings.Repeat("jsx ", 200),
				Details: map[string]any{"path": "app/portfolio.tsx", "offset": 200, "limit": 200, "totalLines": 512},
			},
		},
	)}
	got := recapNarrative("find the contact section", turns)
	want := "Inspected app/portfolio.tsx (lines 200-399 of 512 read) — the contact section lists an email address."
	if got != want {
		t.Fatalf("recap = %q, want %q", got, want)
	}
}
