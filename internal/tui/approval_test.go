package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// approvalState parks a dark-theme state on an approval at the given size.
func approvalState(w, h int, mutating bool) State {
	s := New()
	s.Apply(TerminalResized{Width: w, Height: h})
	s.Apply(ApprovalAsked{
		ToolName: "bash",
		Summary:  "bash rm -rf build",
		Reason:   "deletes build output",
		Mutating: mutating,
		Choices:  approvalChoices,
	})
	return s
}

// approvalLineWith returns the first plain line containing sub, failing when
// none does.
func approvalLineWith(t *testing.T, out, sub string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	t.Fatalf("no line contains %q:\n%s", sub, out)
	return ""
}

// The dialog is a bounded rounded box: the legend rides the top border, the
// exact call and the ask sit in the body, the three answers are numbered, and
// the captured keys are named in the footer.
func TestApprovalBoxDrawsBorderedLegendAndChoices(t *testing.T) {
	out := ansi.Strip(Render(approvalState(80, 24, true)))

	title := strings.TrimSpace(approvalLineWith(t, out, approvalTitle))
	if !strings.HasPrefix(title, "╭") || !strings.HasSuffix(title, "╮") {
		t.Errorf("legend is not inline on the top border: %q", title)
	}
	bottom := strings.TrimSpace(approvalLineWith(t, out, "╰"))
	if !strings.HasSuffix(bottom, "╯") {
		t.Errorf("no rounded bottom border: %q", bottom)
	}
	for _, want := range []string{
		approvalQuestion,
		"bash rm -rf build",
		"1. Yes",
		"2. Allow for entire session",
		"3. No (default)",
		approvalFooterHint,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("approval box missing %q:\n%s", want, out)
		}
	}
	// The frame draws two side columns around the interior.
	if !strings.Contains(out, "│  ") {
		t.Errorf("frame side columns are missing:\n%s", out)
	}
}

// The frame accent reads the call's blast radius: amber for a read/inspect
// call, rose for one that mutates. The literal hex is the contract.
func TestApprovalBorderAccentFollowsBlastRadius(t *testing.T) {
	read := approvalState(80, 24, false)
	if got := approvalBorderColor(read.Pending); got != "#C9A86A" {
		t.Errorf("read approval accent = %q, want the muted amber #C9A86A", got)
	}
	write := approvalState(80, 24, true)
	if got := approvalBorderColor(write.Pending); got != "#CD694A" {
		t.Errorf("mutating approval accent = %q, want the rose #CD694A", got)
	}
}

// Exactly one answer carries the cursor, and it is the conservative default;
// every other answer is indented past the two-cell cursor gutter.
func TestApprovalCursorMarksOnlyTheDefaultRow(t *testing.T) {
	out := ansi.Strip(Render(approvalState(80, 24, false)))
	var marked, indented int
	for _, line := range strings.Split(out, "\n") {
		content := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "│"), "│")
		if strings.HasPrefix(content, "❯ ") {
			marked++
			if !strings.Contains(content, "3. No (default)") {
				t.Errorf("cursor marked %q, want the conservative default", strings.TrimSpace(content))
			}
		}
		if strings.HasPrefix(content, "  1. ") || strings.HasPrefix(content, "  2. ") {
			indented++
		}
	}
	if marked != 1 {
		t.Errorf("cursor rows = %d, want exactly 1:\n%s", marked, out)
	}
	if indented != 2 {
		t.Errorf("inactive answer rows indented = %d, want 2:\n%s", indented, out)
	}
}

// Accounting and rendering are the same plan: approvalLines must equal the
// rows renderApproval actually emits, or the transcript budget drifts and the
// chrome floats.
func TestApprovalLinesMatchRenderedRows(t *testing.T) {
	for _, size := range [][2]int{{40, 24}, {80, 24}, {60, 12}, {80, 10}, {120, 40}} {
		w, h := size[0], size[1]
		s := approvalState(w, h, true)
		layout := ComputeLayout(s)
		var b strings.Builder
		renderApproval(&b, s.Pending, s.Theme, layout)
		rendered := strings.Count(b.String(), "\n")
		if got := approvalLines(s.Pending, layout); got != rendered {
			t.Errorf("%dx%d: approvalLines = %d, rendered = %d", w, h, got, rendered)
		}
	}
}

// On a known-size viewport the approval block fills the frame exactly: every
// row the dialog claims is painted, and no more.
func TestApprovalFrameFillsTheViewportExactly(t *testing.T) {
	for _, size := range [][2]int{{40, 24}, {80, 24}, {100, 40}, {160, 50}} {
		w, h := size[0], size[1]
		out := Render(approvalState(w, h, false))
		if got := len(strings.Split(out, "\n")); got != h {
			t.Errorf("%dx%d rendered %d lines, want exactly %d", w, h, got, h)
		}
	}
}

// A short viewport sheds optional rows (padding, the reason, the spacers)
// before it touches the command or the answers, and it never floods the frame.
func TestApprovalBoxShedsOptionalRowsBeforeItsChoices(t *testing.T) {
	s := approvalState(60, 12, true)
	out := ansi.Strip(Render(s))
	for _, want := range []string{"bash rm -rf build", "1. Yes", "2. Allow for entire session", "3. No (default)"} {
		if !strings.Contains(out, want) {
			t.Errorf("short viewport dropped %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "deletes build output") {
		t.Errorf("short viewport kept the optional reason before the choices:\n%s", out)
	}
	if got := len(strings.Split(Render(s), "\n")); got > 12+2 {
		t.Errorf("short viewport rendered %d lines, want at most %d", got, 14)
	}
}

// "Allow for entire session" answers affirmative and then answers that tool
// without a prompt for the rest of the session; the default still denies.
func TestApprovalSessionChoiceRemembersTheTool(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	approve := p.ApprovalFunc()

	answers := make(chan bool, 1)
	go func() {
		ok, _ := approve(ai.ToolCall{ID: "c1", Name: "bash"}, agent.Approval{Tier: agent.TierWrite, Reason: "writes"})
		answers <- ok
	}()
	var request approvalRequest
	select {
	case request = <-p.approvals:
	case <-time.After(2 * time.Second):
		t.Fatal("the approval never reached the UI")
	}
	p.pending = &request
	p.state.Apply(ApprovalAsked{ToolName: "bash", Mutating: true, Choices: approvalChoices})

	if got := p.state.Pending.Selected(); got != "No" {
		t.Fatalf("default = %q, want the conservative choice", got)
	}
	// The resting cursor is the last (conservative) choice; one deliberate
	// move up reaches the remembered affirmative.
	p.handleKey(navKey("up"))
	if got := p.state.Pending.Selected(); !isSessionApprovalChoice(got) {
		t.Fatalf("up moved to %q, want the session choice", got)
	}
	p.handleKey(navKey("enter"))
	select {
	case ok := <-answers:
		if !ok {
			t.Fatal("the session choice denied the tool")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the blocked tool never received the answer")
	}

	// The next call of the same tool is answered without opening a prompt.
	second := make(chan bool, 1)
	go func() {
		ok, _ := approve(ai.ToolCall{ID: "c2", Name: "bash"}, agent.Approval{Tier: agent.TierWrite})
		second <- ok
	}()
	select {
	case ok := <-second:
		if !ok {
			t.Fatal("a session-approved tool was denied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a session-approved tool still blocked on a prompt")
	}
}

// Diff approval previews carry green/red ANSI styling on +/- lines.
func TestApprovalDiffPreviewStylesAdditionsAndDeletions(t *testing.T) {
	prev := termenv.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	s := New()
	s.Apply(TerminalResized{Width: 80, Height: 24})
	s.Apply(ApprovalAsked{
		ToolName: "write",
		Summary:  "write /tmp/x",
		Mutating: true,
		Choices:  approvalChoices,
		MutationPreview: agent.MutationPreview{
			Operation: agent.MutationModify,
			Diff:      "+ added line\n- removed line\n@@ -1,1 +1,1 @@",
		},
	})
	out := Render(s)
	if !strings.Contains(out, "166;227;161") {
		t.Errorf("approval preview lacks green ANSI styling for + lines:\n%s", ansi.Strip(out))
	}
	if !strings.Contains(out, "243;139;168") {
		t.Errorf("approval preview lacks red ANSI styling for - lines:\n%s", ansi.Strip(out))
	}
	if !strings.Contains(out, "137;220;235") {
		t.Errorf("approval preview lacks cyan ANSI styling for @@ lines:\n%s", ansi.Strip(out))
	}
}
