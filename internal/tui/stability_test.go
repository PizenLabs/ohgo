package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The composer is a fixed anchor. The activity surface hand-off (line → tool
// entry → line → reasoning entry → line → idle) must never change the row the
// prompt occupies. This is the layout half of "fast tasks complete silently":
// no chrome grows or shrinks for a phase, so nothing flashes or snaps while the
// output is still printing.
func TestComposerRowStaysFixedAcrossActivityTransitions(t *testing.T) {
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(WorkspaceUpdated{Workspace: "BUILD", HasWorkspace: true})
	s.Apply(UserSubmitted{Text: "inspect", At: at, Seq: 1})

	composerRow := func(label string) int {
		t.Helper()
		lines := strings.Split(ansi.Strip(Render(s)), "\n")
		if len(lines) != 30 {
			t.Fatalf("%s: frame is %d rows, want exactly 30", label, len(lines))
		}
		for i, line := range lines {
			if strings.Contains(line, "build › ") {
				return i
			}
		}
		t.Fatalf("%s: composer row missing:\n%s", label, strings.Join(lines, "\n"))
		return -1
	}
	wantComposer := composerRow("activity line")
	check := func(label string, want ActivitySurface) {
		t.Helper()
		if got := ProjectActivity(s).Surface; got != want {
			t.Fatalf("%s: activity surface = %v, want %v", label, got, want)
		}
		if got := composerRow(label); got != wantComposer {
			t.Fatalf("%s: composer row = %d, want the fixed row %d", label, got, wantComposer)
		}
	}

	check("activity line", ActivityLine)

	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: at, Seq: 1})
	check("running tool entry", ActivityEntry)

	s.Apply(ToolFinished{ID: "c1", Text: "package a\n", Summary: "1 line", At: at.Add(time.Millisecond), Seq: 1})
	check("back to activity line", ActivityLine)

	s.Apply(ReasoningDelta{Text: "plan the read", At: at.Add(2 * time.Millisecond), Seq: 1})
	check("open reasoning entry", ActivityEntry)

	s.Apply(AssistantDelta{Text: "the answer", At: at.Add(3 * time.Millisecond), Seq: 1})
	check("streaming activity line", ActivityLine)

	// Expanding a Thought block reflows the transcript, never the composer:
	// the badge is the only thing that grows.
	s.Apply(ReasoningToggled{ID: s.Entries[2].ID})
	check("expanded reasoning", ActivityLine)

	s.Apply(AssistantDone{At: at.Add(4 * time.Millisecond), Seq: 1})
	check("settled turn", ActivityNone)
}

// A reasoning trace is collapsed to one minimal, non-duplicated header. The
// body and the expansion key are not repeated as prose: the disclosure marker
// is the affordance, and the shared Ctrl+O chord expands it.
func TestThoughtHeaderIsMinimalAndExpandable(t *testing.T) {
	start := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: start, Seq: 1})
	s.Apply(ReasoningDelta{Text: "inspect the cache", At: start, Seq: 1})
	s.Apply(AssistantDelta{Text: "done", At: start.Add(4 * time.Second), Seq: 1})

	out := ansi.Strip(Render(s))
	if !strings.Contains(out, "+ Thought · 4s") {
		t.Fatalf("collapsed header = %q, want the minimal measured Thought row:\n%s", out, out)
	}
	if strings.Contains(out, "reasoning") {
		t.Fatalf("collapsed header still uses the old duplicate vocabulary:\n%s", out)
	}
	if strings.Contains(out, "ctrl+o to expand") {
		t.Fatalf("reasoning repeated the expansion key the marker already conveys:\n%s", out)
	}
	if strings.Contains(out, "inspect the cache") {
		t.Fatalf("collapsed header leaked the trace:\n%s", out)
	}

	s.Apply(ReasoningToggled{ID: s.Entries[1].ID})
	expanded := ansi.Strip(Render(s))
	if !strings.Contains(expanded, "- Thought") || !strings.Contains(expanded, "inspect the cache") {
		t.Fatalf("expanded header lost the marker or body:\n%s", expanded)
	}
}

// Expanding a tool block reveals the complete target even when the collapsed
// badge had to middle-truncate it. Summary stays bounded; Invocation is the
// recoverable form the expansion renders.
func TestExpandedToolShowsFullPath(t *testing.T) {
	full := "/Users/anvndev/Documents/Project/Golang/go-ecommerce/src/components/checkout/validators/payment.ts"
	if got := ToolSummaryForCall("read", map[string]any{"path": full}); got != "read "+full {
		t.Fatalf("summary = %q, want the complete target", got)
	}
	if got := ToolInvocationForCall("read", map[string]any{"path": full}); got != "read "+full {
		t.Fatalf("invocation = %q, want the complete target", got)
	}

	s := New()
	s.Apply(TerminalResized{Width: 200, Height: 30})
	now := time.Now()
	s.Apply(ToolStarted{
		ID:         "c1",
		Name:       "read",
		Summary:    ToolSummaryForCall("read", map[string]any{"path": full}),
		Invocation: ToolInvocationForCall("read", map[string]any{"path": full}),
		At:         now,
	})
	s.Apply(ToolFinished{ID: "c1", Text: "one\ntwo\n", Summary: "2 lines", At: now})
	s.Apply(ToolToggled{ID: "c1"})

	out := ansi.Strip(Render(s))
	if !strings.Contains(out, full) {
		t.Fatalf("expanded block lost the full path:\n%s", out)
	}
}

// The expanded diff header is also content the user asked to see: a path that
// does not fit wraps instead of being middle-cut, while a short path keeps the
// existing single-row identity.
func TestExpandedDiffShowsFullPath(t *testing.T) {
	long := "/Users/anvndev/Documents/Project/Golang/go-ecommerce/backend/services/checkout/internal/domain/order/aggregate/order_aggregate.go"
	s := New()
	s.Apply(TerminalResized{Width: 60, Height: 30})
	now := time.Now()
	s.Apply(ToolStarted{ID: "c1", Name: "edit", Summary: "edit checkout", At: now})
	s.Apply(ToolFinished{
		ID:        "c1",
		Diff:      "@@ -1,2 +1,2 @@\n-old\n+new\n",
		Path:      long,
		Operation: "MODIFY",
		Summary:   "1 file changed",
		At:        now,
	})
	s.Apply(ToolToggled{ID: "c1"})

	out := ansi.Strip(Render(s))
	if strings.Contains(out, "…") {
		t.Fatalf("expanded diff path was abbreviated:\n%s", out)
	}
	if !strings.Contains(out, "/Users/anvndev/Documents/Project/Golang") {
		t.Fatalf("expanded diff lost the path head:\n%s", out)
	}
	if !strings.Contains(out, "order_aggregate.go") {
		t.Fatalf("expanded diff lost the basename:\n%s", out)
	}
}

// The live footer never repeats a phase its transcript entry already presents,
// but its telemetry row stays painted. The chrome count matches the rows the
// footer actually emits, so handing the spinner to a tool entry cannot grow the
// region behind the composer's back.
func TestLiveStatusLineDoesNotDuplicateEntryActivity(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Status.Model = "p/m"
	s.Apply(UserSubmitted{Text: "go", At: time.Now(), Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "read", Summary: "read a.go", At: time.Now(), Seq: 1})

	if ProjectActivity(s).Surface != ActivityEntry {
		t.Fatal("fixture did not put the running call on the entry surface")
	}
	layout := ComputeLayout(s)
	pal := PaletteFor(s.Theme)
	status := ansi.Strip(liveStatusLine(s, pal, layout))
	if strings.Contains(status, "read a.go") {
		t.Fatalf("entry-owned activity rendered a second status copy: %q", status)
	}
	if !strings.Contains(status, "p/m") {
		t.Fatalf("live footer lost its telemetry: %q", status)
	}
	want := len(liveComposerLines(s, pal, layout)) + 1
	if got := liveChrome(s, layout); got != want {
		t.Fatalf("live chrome = %d rows, want telemetry plus %d composer rows", got, want-1)
	}

	// Once no entry owns the phase, the footer is the one place the phase can
	// appear. It still occupies the same telemetry row.
	s.Apply(ToolFinished{ID: "c1", Text: "package a\n", Summary: "1 line", At: time.Now(), Seq: 1})
	status = ansi.Strip(liveStatusLine(s, pal, ComputeLayout(s)))
	if !strings.Contains(status, "reason") {
		t.Fatalf("activity line did not reach the live footer: %q", status)
	}
}
