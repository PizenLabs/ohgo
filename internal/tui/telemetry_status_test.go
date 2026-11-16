package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// TestReasoningProgressPhraseNormalisesToParticiple: the thought channel is
// prose, but the bar must claim an action in present-participle form. Lead-ins
// and infinitives are stripped so the verb is the first visible word.
func TestReasoningProgressPhraseNormalisesToParticiple(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"Inspect the retry helper.", "inspecting the retry helper"},
		{"Let me read the main file.", "reading the main file"},
		{"I need to run the tests.", "running the tests"},
		{"The user wants me to check the project structure.", "checking the project structure"},
		{"I will update the handler and rerun the suite.", "updating the handler and rerun the suite"},
		{"I'm reviewing the repository structure.", "reviewing the repository structure"},
		// No recognised action: the caller must fall back to the phase label
		// rather than presenting prose as a progress claim.
		{"The repository is a Go monorepo.", ""},
		{"There is something odd in the tests.", ""},
	}
	for _, tt := range cases {
		if got := reasoningProgressPhrase(tt.text); got != tt.want {
			t.Errorf("reasoningProgressPhrase(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

// TestToolProgressPhraseMapsArguments: the tool payload becomes a human
// progress description deterministically, including shell phases the user
// recognises without reading the command.
func TestToolProgressPhraseMapsArguments(t *testing.T) {
	cases := []struct {
		name       string
		invocation string
		want       string
	}{
		{"read", "read internal/tui/shimmer.go", "reading shimmer.go"},
		{"write", "write internal/tui/new.go", "writing new.go"},
		{"edit", "edit internal/tui/telemetry_bar.go", "editing telemetry_bar.go"},
		{"delete", "delete internal/tui/old.go", "deleting old.go"},
		{"bash", "go test ./...", "running tests"},
		{"bash", "go vet ./...", "running tests"},
		{"bash", "go build ./...", "building the project"},
		{"bash", "go mod tidy", "resolving dependencies"},
		{"bash", "golangci-lint run ./...", "linting the code"},
		{"bash", "git status --short", "inspecting git state"},
		{"bash", "make deploy", "building the project"},
		{"bash", "df -h", "running df -h"},
		{"grep", "grep TODO", "searching for TODO"},
		{"glob", "glob **/*.go", "finding files matching **/*.go"},
		{"ls", "ls internal/tui", "listing internal/tui"},
		{"ls", "ls .", "listing files"},
		{"web_fetch", "web_fetch https://example.com/a", "fetching https://example.com/a"},
		{"search", "external search widgets", "searching for widgets"},
		{"todo", "todo", "updating the task list"},
		{"some_tool", "some_tool", "running some tool"},
	}
	for _, tt := range cases {
		tool := &ToolEntry{Name: tt.name, Invocation: tt.invocation}
		if got := toolProgressPhrase(tool); got != tt.want {
			t.Errorf("%s %q = %q, want %q", tt.name, tt.invocation, got, tt.want)
		}
	}
}

// TestTelemetryStatusHonoursDwellFloor: a replacement phrase waits for the
// 500ms floor, even when the runtime has already moved on, so fast calls do not
// flicker the row.
func TestTelemetryStatusHonoursDwellFloor(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(ReasoningDelta{Text: "Let me read the main file.", At: at, Seq: 1})

	const first = "reading the main file"
	if got := projectLiveTelemetry(s).Label; got != first {
		t.Fatalf("initial phrase = %q, want %q", got, first)
	}

	s.Apply(ToolStarted{ID: "c1", Name: "bash", Invocation: "go test ./...", At: at.Add(100 * time.Millisecond), Seq: 1})
	if got := projectLiveTelemetry(s).Label; got != first {
		t.Fatalf("phrase changed inside the floor: %q", got)
	}
	s.Apply(FrameTick{At: at.Add(499 * time.Millisecond)})
	if got := projectLiveTelemetry(s).Label; got != first {
		t.Fatalf("phrase changed at 499ms: %q", got)
	}
	s.Apply(FrameTick{At: at.Add(500 * time.Millisecond)})
	if got := projectLiveTelemetry(s).Label; got != "running tests" {
		t.Fatalf("phrase after the floor = %q, want the new action", got)
	}
}

// TestTelemetryStatusCoalescesRapidUpdates: several actions inside one floor
// collapse to the newest live one; the bar never replays an action that has
// already finished by the time the floor expires.
func TestTelemetryStatusCoalescesRapidUpdates(t *testing.T) {
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 100, Height: 30})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(ToolStarted{ID: "c1", Name: "bash", Invocation: "go test ./...", At: at, Seq: 1})
	s.Apply(ToolFinished{ID: "c1", Text: "ok", Summary: "ok", At: at.Add(80 * time.Millisecond), Seq: 1})
	s.Apply(ToolStarted{ID: "c2", Name: "edit", Invocation: "edit internal/tui/shimmer.go", At: at.Add(120 * time.Millisecond), Seq: 1})

	s.Apply(FrameTick{At: at.Add(telemetryStatusDwell)})
	if got := projectLiveTelemetry(s).Label; got != "editing shimmer.go" {
		t.Fatalf("coalesced phrase = %q, want the newest live action", got)
	}
}

// TestTelemetryBarTruncatesLongAction: the phrase is measured and cut before
// styling, so shimmer escapes can never push the row past the terminal width.
func TestTelemetryBarTruncatesLongAction(t *testing.T) {
	forceTrueColor(t)
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(TerminalResized{Width: 28, Height: 24})
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	s.Apply(ReasoningDelta{
		Text: "Inspecting the very long retry implementation with every caller in the repository",
		At:   at, Seq: 1,
	})
	bar := renderLiveTelemetryBarAt(s, PaletteFor(s.Theme), ComputeLayout(s), 7)
	if got := CellWidth(bar); got > 28 {
		t.Fatalf("bar is %d cells, want <= 28: %q", got, ansi.Strip(bar))
	}
	if strings.HasSuffix(strings.TrimSpace(ansi.Strip(bar)), "…") {
		t.Fatalf("long action kept a trailing ellipsis: %q", ansi.Strip(bar))
	}
}

// TestTelemetryPhraseTypography pins the formatting contract: status phrases
// are lowercase, carry no trailing dots, and keep path/code spelling intact.
func TestTelemetryPhraseTypography(t *testing.T) {
	cases := []struct {
		phrase string
		want   string
	}{
		{"Preparing workspace...", "preparing workspace"},
		{"Formatting response..", "formatting response"},
		{"Listing ...", "listing"},
		{"Inspecting the Retry Helper.", "inspecting the retry helper"},
		{"Reading internal/TUI/Shimmer.go.", "reading internal/TUI/Shimmer.go"},
		{"Calling HTTPClient", "calling HTTPClient"},
		{"Searching for TODO", "searching for TODO"},
		{"Reading main.go.", "reading main.go"},
		{"Running go test ./...", "running go test ./..."},
	}
	for _, tt := range cases {
		got := normalizeTelemetryPhrase(tt.phrase)
		if got != tt.want {
			t.Errorf("normalizeTelemetryPhrase(%q) = %q, want %q", tt.phrase, got, tt.want)
		}
		if (strings.HasSuffix(got, ".") || strings.HasSuffix(got, "…")) && !strings.HasSuffix(got, "./...") {
			t.Errorf("normalized phrase %q kept trailing dots", got)
		}
	}

	// Every fallback phase obeys the same typography.
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	s := New()
	s.Apply(UserSubmitted{Text: "go", At: at, Seq: 1})
	if got := projectLiveTelemetry(s).Label; got != "preparing workspace" {
		t.Fatalf("fallback phrase = %q, want lower-case without dots", got)
	}
}
