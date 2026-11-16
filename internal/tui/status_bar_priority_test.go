package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// measuredFooterState builds one session with every footer tier present:
// runtime telemetry, a long repository path with a branch, measured cost, a
// turn marker, and a settled turn whose duration is locked.
func measuredFooterState(t *testing.T) State {
	t.Helper()
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(WorkspaceUpdated{
		Cwd: "/Users/dev/Documents/Project/Golang/go-ecommerce", HasCwd: true,
		Session: "#1234", HasSession: true,
		GitBranch: "feature/checkout-flow", HasGit: true,
		UsageText: "$0.02", HasUsage: true,
	})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(SessionMetricsUpdated{Input: 109_500, Output: 4_500, Elapsed: 58 * time.Second})
	s.Apply(ContextUpdated{Used: 76_000, Window: 100_000})
	s.Apply(FrameTick{At: at.Add(58 * time.Second)})
	s.Apply(AssistantDone{At: at.Add(58 * time.Second), Seq: 1})
	return s
}

func plainFooter(s State, width int) string {
	return ansi.Strip(composerHintBar(s, width))
}

// Runtime telemetry is P0. A path, a turn marker, or a key hint may never
// displace the duration, the token counters, or the context percentage: they
// are what the user decides with. The measured cost is telemetry too, but it
// ranks after context consumption, so it is the first to go.
func TestFooterPreservesRuntimeTelemetryAtEveryWidth(t *testing.T) {
	core := []string{"⏱ 58s", "↑109.5k", "↓4.5k", "76%"}
	for _, width := range []int{160, 120, 90, 70, 60, 50, 40, 30} {
		s := measuredFooterState(t)
		row := plainFooter(s, width)
		if got := CellWidth(row); got > width {
			t.Errorf("width %d: footer is %d cells: %q", width, got, row)
		}
		for _, want := range core {
			if !strings.Contains(row, want) {
				t.Errorf("width %d: footer lost %q: %q", width, want, row)
			}
		}
	}
}

// Lower-priority information yields before higher-priority information. The
// same state degrades in one deliberate order as the frame narrows: leading
// path segments, then the turn marker, then the action rail, then the measured
// cost. Context consumption survives all of them.
func TestFooterDegradesInPriorityOrder(t *testing.T) {
	tests := []struct {
		width int
		want  []string
		not   []string
	}{
		{
			width: 120,
			want:  []string{"76%", "…/Golang/go-ecommerce:feature/checkout-flow", "#1234", "? help"},
		},
		{
			width: 95,
			want:  []string{"76%", "…/Golang/go-ecommerce:feature/checkout-flow", "#1234"},
			// The action rail is P3 and cannot buy cells from workspace
			// identity; at this width there is no room left over.
			not: []string{"? help"},
		},
		{
			width: 85,
			want:  []string{"76%", "…/Golang/go-ecommerce:feature/checkout-flow"},
			// The turn marker is P2: it goes before the repository identity.
			not: []string{"#1234", "? help"},
		},
		{
			width: 80,
			want:  []string{"76%", "…/go-ecommerce:feature/checkout-flow"},
			not:   []string{"#1234", "? help"},
		},
		{
			width: 70,
			want:  []string{"76%", "$0.02", "feature/checkout-flow"},
			// A long branch pushes the path leaf into its own truncation: the
			// identity survives, the directory prefix does not.
			not: []string{"#1234", "go-ecommerce", "? help"},
		},
		{
			width: 40,
			want:  []string{"76%", "$0.02"},
			not:   []string{"go-ecommerce", "feature/checkout-flow", "#1234", "? help"},
		},
		{
			width: 30,
			want:  []string{"76%"},
			not:   []string{"$0.02", "go-ecommerce", "#1234", "? help"},
		},
		{
			// Below the four P0 slots the footer still degrades in priority
			// order rather than clipping an unrelated hint.
			width: 24,
			want:  []string{"⏱ 58s", "↑109.5k", "↓4.5k"},
			not:   []string{"76%", "$0.02", "#1234", "? help"},
		},
	}
	for _, tt := range tests {
		s := measuredFooterState(t)
		row := plainFooter(s, tt.width)
		if got := CellWidth(row); got > tt.width {
			t.Errorf("width %d: footer is %d cells: %q", tt.width, got, row)
		}
		for _, want := range tt.want {
			if !strings.Contains(row, want) {
				t.Errorf("width %d: footer = %q, want %q", tt.width, row, want)
			}
		}
		for _, banned := range tt.not {
			if strings.Contains(row, banned) {
				t.Errorf("width %d: footer = %q, want %q gone first", tt.width, row, banned)
			}
		}
	}
}

// The budgeted row is the row the frame actually paints: a split-pane footer
// keeps the context percentage and never overflows the terminal, so the
// terminal's own clipping never decides what survives.
func TestFramedFooterKeepsContextAtSplitPaneWidths(t *testing.T) {
	for _, width := range []int{60, 50, 40} {
		s := measuredFooterState(t)
		s.Apply(TerminalResized{Width: width, Height: 24})

		out := Render(s)
		for _, line := range strings.Split(out, "\n") {
			if got := CellWidth(line); got > width {
				t.Fatalf("width %d: frame line is %d cells: %q", width, got, line)
			}
		}
		footer := lastLine(out)
		if !strings.Contains(footer, "76%") {
			t.Errorf("width %d: footer = %q, want the context percentage", width, footer)
		}
	}
}

// A long branch name survives the leading path segments and never displaces
// the context percentage: identity is truncatable, runtime constraint data is
// not.
func TestFooterKeepsLongBranchBeforePathPrefix(t *testing.T) {
	const branch = "feature/very-long-checkout-reconciliation-branch"
	s := New()
	s.Apply(WorkspaceUpdated{
		Cwd: "/Users/dev/Documents/Project/Golang/go-ecommerce", HasCwd: true,
		Session: "#7", HasSession: true,
		GitBranch: branch, HasGit: true,
	})
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(SessionMetricsUpdated{Input: 109_500, Output: 4_500})
	s.Apply(ContextUpdated{Used: 76_000, Window: 100_000})

	row := plainFooter(s, 100)
	if got := CellWidth(row); got > 100 {
		t.Fatalf("footer is %d cells: %q", got, row)
	}
	for _, want := range []string{"76%", "↑109.5k", "↓4.5k", branch} {
		if !strings.Contains(row, want) {
			t.Errorf("footer = %q, want %q", row, want)
		}
	}
	if strings.Contains(row, "#7") {
		t.Errorf("footer = %q, want the turn marker to yield before the branch", row)
	}
}

// The action rail is atomic. A clipped "alt+enter bre…" or "enter que…" is a
// broken instruction, so an action either fits whole or disappears.
func TestFooterNeverPartiallyClipsActionHints(t *testing.T) {
	s := measuredFooterState(t)
	s.Apply(UserSubmitted{Text: "go"})
	s.Apply(InputChanged{Text: "next prompt"})
	s.Apply(AssistantDelta{Text: "streaming"})

	partials := []string{"alt+enter bre…", "enter que…", "esc cance…", "? hel…", "end botto…"}
	for width := 20; width <= 140; width++ {
		row := plainFooter(s, width)
		if got := CellWidth(row); got > width {
			t.Fatalf("width %d: footer overflows at %d cells: %q", width, got, row)
		}
		for _, partial := range partials {
			if strings.Contains(row, partial) {
				t.Fatalf("width %d: footer clipped an action hint: %q", width, row)
			}
		}
		// The rail is selected as a prefix: a lower-priority hint never
		// appears while the higher-priority one was dropped.
		if strings.Contains(row, "alt+enter break") && !strings.Contains(row, "esc cancel") {
			t.Fatalf("width %d: break hint appeared without cancel: %q", width, row)
		}
	}
}

// Contextual actions are derived from the state that shows them, not from a
// fixed footer. The rail may legitimately be empty.
func TestFooterActionsFollowRuntimeState(t *testing.T) {
	typing := func(s State, text string) State {
		s.Apply(InputChanged{Text: text})
		return s
	}
	busy := func(t *testing.T) State {
		s := New()
		s.Apply(TerminalResized{Width: 120, Height: 30})
		s.Apply(UserSubmitted{Text: "go"})
		return s
	}

	tests := []struct {
		name  string
		state func(t *testing.T) State
		want  []string
		not   []string
	}{
		{
			name:  "fresh composer teaches discovery",
			state: func(*testing.T) State { return New() },
			want:  []string{"? help", "^p settings"},
			not:   []string{"esc cancel", "enter queue", "enter send", "end bottom"},
		},
		{
			name:  "a draft teaches its submit key",
			state: func(*testing.T) State { return typing(New(), "question") },
			want:  []string{"enter send"},
			// "?" is literal while composing, so help must not be advertised.
			not: []string{"? help", "^p settings", "esc cancel"},
		},
		{
			name:  "an active turn leads with cancel",
			state: busy,
			want:  []string{"esc cancel"},
			not:   []string{"enter queue", "alt+enter break", "? help", "^p settings"},
		},
		{
			name: "a draft during a turn names queue and the newline chord",
			state: func(t *testing.T) State {
				return typing(busy(t), "next prompt")
			},
			want: []string{"esc cancel", "alt+enter break", "enter queue"},
			not:  []string{"? help", "^p settings"},
		},
		{
			name: "a scrolled viewport teaches the way back",
			state: func(t *testing.T) State {
				s := New()
				s.Apply(WorkspaceUpdated{Cwd: "/work/proj", HasCwd: true})
				s.Apply(ScrollMoved{Delta: 5})
				return s
			},
			want: []string{"end bottom", "? help"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.state(t)
			rail := plainFooter(s, 160)
			for _, want := range tt.want {
				if !strings.Contains(rail, want) {
					t.Errorf("footer = %q, want %q", rail, want)
				}
			}
			for _, banned := range tt.not {
				if strings.Contains(rail, banned) {
					t.Errorf("footer = %q, want no %q", rail, banned)
				}
			}
		})
	}
}

// An armed interrupt owns the row: the second Esc is the only useful action,
// so telemetry yields rather than competing for attention. An open approval
// prints its own key line, so the footer is empty.
func TestFooterBlockedStatesOwnOrYieldTheRow(t *testing.T) {
	s := measuredFooterState(t)
	s.Apply(UserSubmitted{Text: "go"})
	s.InterruptConfirm = true // the first Esc armed the double-tap warning
	if row := plainFooter(s, 120); row != interruptConfirmText {
		t.Fatalf("armed footer = %q, want only %q", row, interruptConfirmText)
	}

	blocked := measuredFooterState(t)
	blocked.Pending = &PendingApproval{}
	if row := plainFooter(blocked, 120); row != "" {
		t.Fatalf("blocked footer = %q, want the dialog to own the keys", row)
	}
}

// Context pressure is the only semantic tone in the row: below the threshold
// ordinary telemetry stays neutral, at 80% it warns, and at 95% it is an
// error. The number itself always states the meaning, so colour is never the
// only signal.
func TestContextPressureTakesSemanticTone(t *testing.T) {
	pal := forcedPalette()

	stylized := func(used int) string {
		s := New()
		s.Apply(WorkspaceUpdated{Cwd: "/work/proj", HasCwd: true})
		s.Apply(UserSubmitted{Text: "go"})
		s.Apply(ContextUpdated{Used: used, Window: 100_000})
		return composerHintRow(s, pal, 160)
	}

	normal := stylized(50_000)
	if !strings.Contains(ansi.Strip(normal), "50%") {
		t.Fatalf("normal row lost the percentage: %q", ansi.Strip(normal))
	}
	if strings.Contains(normal, pal.Warning.Render("  50%")) {
		t.Errorf("normal context took the warning tone: %q", normal)
	}

	warning := stylized(85_000)
	if !strings.Contains(warning, pal.Warning.Render("  85%")) {
		t.Errorf("85%% context is not in the warning role: %q", warning)
	}
	if !strings.Contains(ansi.Strip(warning), "85%") {
		t.Errorf("warning context lost its numeric meaning: %q", ansi.Strip(warning))
	}

	danger := stylized(97_000)
	if !strings.Contains(danger, pal.ToolError.Render("  97%")) {
		t.Errorf("97%% context is not in the error role: %q", danger)
	}
	if !strings.Contains(ansi.Strip(danger), "97%") {
		t.Errorf("danger context lost its numeric meaning: %q", ansi.Strip(danger))
	}
}

// The projection, not string concatenation, carries the priority contract:
// P0 telemetry is atomic, and only workspace identity may be truncated.
func TestStatusProjectionCarriesPriorityAndTruncatability(t *testing.T) {
	projection := projectStatusBar(measuredFooterState(t))
	byKind := make(map[statusKind]statusItem, len(projection.items))
	for _, item := range projection.items {
		byKind[item.field] = item
		if item.preferredWidth != CellWidth(item.text) {
			t.Errorf("%s preferred width = %d, want its measured %d cells",
				item.field, item.preferredWidth, CellWidth(item.text))
		}
	}

	wantPriority := map[statusKind]statusPriority{
		statusKindDuration:  statusPriorityRuntime,
		statusKindInput:     statusPriorityRuntime,
		statusKindOutput:    statusPriorityRuntime,
		statusKindContext:   statusPriorityRuntime,
		statusKindCost:      statusPriorityRuntime,
		statusKindTurn:      statusPrioritySession,
		statusKindWorkspace: statusPriorityWorkspace,
	}
	for kind, priority := range wantPriority {
		item, ok := byKind[kind]
		if !ok {
			t.Fatalf("projection lost %q", kind)
		}
		if item.priority != priority {
			t.Errorf("%s priority = %d, want %d", kind, item.priority, priority)
		}
		if kind != statusKindWorkspace && item.truncatable {
			t.Errorf("%s is truncatable; runtime and session slots must be atomic", kind)
		}
	}
	workspace := byKind[statusKindWorkspace]
	if !workspace.truncatable || workspace.minWidth <= 0 {
		t.Errorf("workspace item = %+v, want a truncatable item with a minimum", workspace)
	}
}
