package tui

// Automatic copy + transient "Copied" status.
//
// Every test defends one externally observable contract: a mouse drag that
// ends in a non-empty logical selection reaches the clipboard path exactly
// once and surfaces a transient far-right header confirmation, while empty,
// disabled, unsupported, or failed copies never claim success and never
// disturb the selection, the transcript, or runtime state.

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func noticeDecode(t *testing.T, seq string) string {
	t.Helper()
	payload := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("clipboard payload is not base64: %v", err)
	}
	return string(decoded)
}

func noticeHeaderPlain(state State) string {
	return ansi.Strip(strings.Split(Render(state), "\n")[0])
}

func noticeDragProgram(t *testing.T, clipboard func(string) string) (*Program, *bytes.Buffer) {
	t.Helper()
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var out bytes.Buffer
	p.termOut = &out
	if clipboard != nil {
		p.clipboard = clipboard
	}
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	return p, &out
}

func noticeDragTop(p *Program) (marker, top int) {
	_, marker, top, _ = p.visibleRows()
	return marker, top
}

// Test 1 — a finished non-empty drag copies the exact logical text and shows
// "Copied" while keeping the selection visible.
func TestCopyNoticeAutoCopyShowsCopied(t *testing.T) {
	calls := 0
	var copied string
	p, out := noticeDragProgram(t, func(s string) string {
		calls++
		copied = s
		return OSC52Sequence(s)
	})
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if calls != 1 {
		t.Fatalf("clipboard calls = %d, want exactly 1", calls)
	}
	if copied != "world" {
		t.Fatalf("copied = %q, want logical selection", copied)
	}
	if p.state.CopyNotice != "Copied" {
		t.Fatalf("status = %q, want Copied", p.state.CopyNotice)
	}
	if p.state.Selection == nil {
		t.Fatal("selection cleared by MouseUp")
	}
	if out.Len() == 0 {
		t.Fatal("no OSC 52 bytes emitted")
	}
	if got := noticeDecode(t, out.String()); got != "world" {
		t.Fatalf("OSC 52 payload = %q, want logical text", got)
	}
	header := noticeHeaderPlain(p.state)
	if !strings.HasSuffix(header, "Copied") {
		t.Fatalf("header %q does not end with Copied", header)
	}
}

// Test 2 — a partial drag copies exactly its range.
func TestCopyNoticePartialWorld(t *testing.T) {
	var copied string
	p, out := noticeDragProgram(t, func(s string) string {
		copied = s
		return OSC52Sequence(s)
	})
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if copied != "world" {
		t.Fatalf("copied = %q, want world", copied)
	}
	if out.Len() == 0 {
		t.Fatal("partial drag wrote no bytes")
	}
	if got := noticeDecode(t, out.String()); got != "world" {
		t.Fatalf("emitted payload = %q, want world", got)
	}
	if p.state.CopyNotice != "Copied" {
		t.Fatalf("status = %q, want Copied", p.state.CopyNotice)
	}
}

// Test 3 — a right-to-left drag copies the same text as left-to-right.
func TestCopyNoticeReversedDrag(t *testing.T) {
	var copied string
	p, out := noticeDragProgram(t, func(s string) string {
		copied = s
		return OSC52Sequence(s)
	})
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 6, Y: y})
	if copied != "world" {
		t.Fatalf("reversed copied = %q, want world", copied)
	}
	if out.Len() == 0 {
		t.Fatal("reversed drag wrote no bytes")
	}
	if p.state.CopyNotice != "Copied" {
		t.Fatalf("status = %q, want Copied", p.state.CopyNotice)
	}
	if p.state.Selection == nil {
		t.Fatal("reversed drag lost the selection")
	}
}

// Test 4 — a wrapped drag copies both visual rows with their line break.
func TestCopyNoticeWrappedSelection(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 50, Height: 30})
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryUser, UserText: strings.Repeat("wrap this line many times over ", 4)}}})
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var discard bytes.Buffer
	p.termOut = &discard
	var copied string
	p.clipboard = func(text string) string {
		copied = text
		return OSC52Sequence(text)
	}
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	rows, marker, top, ok := p.visibleRows()
	if !ok || len(rows) < 2 {
		t.Fatal("fixture did not wrap")
	}
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: top + marker})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 3, Y: top + marker + 1})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 3, Y: top + marker + 1})
	if copied == "" {
		t.Fatal("wrapped drag copied nothing")
	}
	if !strings.Contains(copied, "\n") {
		t.Fatalf("wrapped copy lost its line break: %q", copied)
	}
	want, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
	if !ok || want != copied {
		t.Fatalf("retained copy %q != released payload %q", want, copied)
	}
	if p.state.CopyNotice != "Copied" {
		t.Fatalf("status = %q, want Copied", p.state.CopyNotice)
	}
}

// Test 5 — Unicode drags copy exact cells and still confirm.
func TestCopyNoticeUnicodeSelection(t *testing.T) {
	s := New()
	s.Apply(TerminalResized{Width: 40, Height: 20})
	const text = "Aé中日本語✅éB"
	s.Apply(HistoryReplaced{Entries: []Entry{{Kind: EntryNotice, Notice: text}}})
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var discard bytes.Buffer
	p.termOut = &discard
	var copied string
	p.clipboard = func(v string) string {
		copied = v
		return OSC52Sequence(v)
	}
	p.state = s
	p.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	p.syncTranscript()
	rows, marker, top, ok := p.visibleRows()
	if !ok || len(rows) == 0 {
		t.Fatal("no visible rows")
	}
	y := top + marker
	// Select the CJK span: cells [2,10) cover 中日本語.
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 10, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 10, Y: y})
	if copied != "中日本語" {
		t.Fatalf("unicode copied = %q, want 中日本語", copied)
	}
	if p.state.CopyNotice != "Copied" {
		t.Fatalf("status = %q, want Copied", p.state.CopyNotice)
	}
}

// Test 6 — a press without movement copies nothing and shows no status.
func TestCopyNoticeEmptyDragNoStatus(t *testing.T) {
	calls := 0
	p, out := noticeDragProgram(t, func(s string) string {
		calls++
		return OSC52Sequence(s)
	})
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 2, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 2, Y: y})
	if calls != 0 {
		t.Fatalf("empty drag copied %d times", calls)
	}
	if out.Len() != 0 {
		t.Fatalf("empty drag wrote %q", out.String())
	}
	if p.state.CopyNotice == "Copied" {
		t.Fatal("empty drag reported success")
	}
}

// Test 7 — a disabled clipboard keeps the selection but never claims success.
func TestCopyNoticeDisabledClipboardNoFalseSuccess(t *testing.T) {
	p, out := noticeDragProgram(t, nil)
	p.clipboard = nil
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if p.state.Selection == nil {
		t.Fatal("disabled clipboard cleared the selection")
	}
	if p.state.CopyNotice == "Copied" {
		t.Fatal("disabled clipboard reported false success")
	}
	if out.Len() != 0 {
		t.Fatalf("disabled clipboard wrote %q", out.String())
	}
}

// Test 8 — an unsupported terminal never reports success.
func TestCopyNoticeUnsupportedTerminalNoFalseSuccess(t *testing.T) {
	if ClipboardAvailable("dumb") {
		t.Fatal("dumb terminal reports clipboard support")
	}
	if ClipboardAvailable("") {
		t.Fatal("empty TERM reports clipboard support")
	}
	p, out := noticeDragProgram(t, nil)
	p.clipboard = nil
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 0, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 5, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 5, Y: y})
	if p.state.CopyNotice == "Copied" {
		t.Fatal("unsupported terminal reported false success")
	}
	if out.Len() != 0 {
		t.Fatalf("unsupported terminal wrote %q", out.String())
	}
}

// A clipboard path that refuses the payload (empty sequence) must not show
// success, and the selection must survive.
func TestCopyNoticeEmissionFailureNoFalseSuccess(t *testing.T) {
	p, out := noticeDragProgram(t, func(string) string { return "" })
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if p.state.CopyNotice == "Copied" {
		t.Fatal("failed emission reported success")
	}
	if out.Len() != 0 {
		t.Fatalf("failed emission wrote %q", out.String())
	}
	if p.state.Selection == nil {
		t.Fatal("failed emission cleared the selection")
	}
}

// Test 9 — the confirmation expires without a sleep: the expiry message
// clears the current generation.
func TestCopyNoticeExpiryClears(t *testing.T) {
	p, _ := noticeDragProgram(t, OSC52Sequence)
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if p.state.CopyNotice != "Copied" {
		t.Fatalf("status = %q, want Copied", p.state.CopyNotice)
	}
	gen := p.state.CopyGen
	p.Update(copyNoticeExpiredMsg{gen: gen})
	if p.state.CopyNotice != "" {
		t.Fatalf("status = %q, want cleared after expiry", p.state.CopyNotice)
	}
	header := noticeHeaderPlain(p.state)
	if strings.Contains(header, "Copied") {
		t.Fatalf("header still shows confirmation after expiry: %q", header)
	}
}

// Test 10 — a second copy before the first expiry owns the lifetime: the
// stale expiry is ignored and only the newest one clears.
func TestCopyNoticeRepeatedCopyStaleExpiryIgnored(t *testing.T) {
	p, out := noticeDragProgram(t, OSC52Sequence)
	marker, top := noticeDragTop(p)
	y := top + marker
	drag := func(x0, x1 int) {
		p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: x0, Y: y})
		p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: x1, Y: y})
		p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: x1, Y: y})
	}
	drag(6, 11)
	if out.Len() == 0 {
		t.Fatal("first copy wrote no bytes")
	}
	firstLen := out.Len()
	genA := p.state.CopyGen
	if p.state.CopyNotice != "Copied" {
		t.Fatal("first copy shows no confirmation")
	}
	drag(0, 5)
	if out.Len() <= firstLen {
		t.Fatal("second copy wrote no additional bytes")
	}
	genB := p.state.CopyGen
	if genB == genA {
		t.Fatal("second copy did not advance the generation")
	}
	if p.state.CopyNotice != "Copied" {
		t.Fatal("second copy lost the confirmation")
	}
	p.Update(copyNoticeExpiredMsg{gen: genA})
	if p.state.CopyNotice != "Copied" {
		t.Fatal("stale expiry cleared the newer confirmation")
	}
	p.Update(copyNoticeExpiredMsg{gen: genB})
	if p.state.CopyNotice != "" {
		t.Fatal("current expiry did not clear the confirmation")
	}
}

// Test 11 — the confirmation stays far-right, one row, and width-valid
// across resizes without disturbing the viewport.
func TestCopyNoticeResizeKeepsPosition(t *testing.T) {
	p, _ := noticeDragProgram(t, OSC52Sequence)
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if p.state.CopyNotice != "Copied" {
		t.Fatal("no confirmation to resize with")
	}
	for _, width := range []int{100, 60, 50, 80, 45} {
		p.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		p.syncTranscript()
		header := noticeHeaderPlain(p.state)
		if got := CellWidth(header); got > width {
			t.Fatalf("width %d: header is %d cells, want one row: %q", width, got, header)
		}
		if !strings.HasSuffix(header, "Copied") {
			t.Fatalf("width %d: header lost far-right confirmation: %q", width, header)
		}
		if !strings.Contains(header, "ohgo") || !strings.Contains(header, "idle") {
			t.Fatalf("width %d: header lost permanent chrome: %q", width, header)
		}
		if p.state.Selection == nil {
			t.Fatalf("width %d lost the selection", width)
		}
		text, ok := CopySelection(p.transcriptFrame.index, p.state.Selection)
		if !ok || text != "world" {
			t.Fatalf("width %d copy = %q (%v)", width, text, ok)
		}
		if p.transcriptFrame == nil || p.transcriptFrame.index == nil {
			t.Fatalf("width %d lost the viewport", width)
		}
	}
}

// Test 12 — copying never mutates the transcript: no new block, no revision,
// no viewport theft, and no transcript row containing the confirmation.
func TestCopyNoticeNoTranscriptMutation(t *testing.T) {
	p, _ := noticeDragProgram(t, OSC52Sequence)
	entriesBefore := len(p.state.Entries)
	idsBefore := make([]BlockID, entriesBefore)
	for i := range p.state.Entries {
		idsBefore[i] = p.state.Entries[i].ID
	}
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if len(p.state.Entries) != entriesBefore {
		t.Fatalf("copy changed block count %d -> %d", entriesBefore, len(p.state.Entries))
	}
	for i, id := range idsBefore {
		if p.state.Entries[i].ID != id {
			t.Fatalf("copy moved block %d %s -> %s", i, id, p.state.Entries[i].ID)
		}
	}
	for _, e := range p.state.Entries {
		for _, field := range []string{e.UserText, e.AssistantText, e.Notice} {
			if field == "Copied" || strings.Contains(field, "Copied") && field != "Copied" && strings.TrimSpace(field) == "Copied" {
				t.Fatalf("transcript absorbed the confirmation: %+v", e)
			}
		}
		if e.Kind == EntryNotice && strings.TrimSpace(e.Notice) == "Copied" {
			t.Fatalf("transcript gained a Copied block: %+v", e)
		}
	}
	full := Render(p.state)
	if strings.Contains(full, "\nCopied\n") {
		t.Fatal("rendered transcript contains a Copied row")
	}
	// The header owns the confirmation; the transcript area must not.
	lines := strings.Split(full, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(ansi.Strip(lines[i])) == "Copied" {
			t.Fatalf("transcript row %d is the confirmation", i)
		}
	}
}

// Test 13 — copying touches only ephemeral UI state.
func TestCopyNoticeRuntimeIsolation(t *testing.T) {
	p, _ := noticeDragProgram(t, OSC52Sequence)
	p.state.Status.Workspace = "BUILD"
	p.state.Status.Session = "#7"
	p.state.Status.Busy = false
	entriesBefore := len(p.state.Entries)
	workspaceBefore := p.state.Status.Workspace
	sessionBefore := p.state.Status.Session
	busyBefore := p.state.Status.Busy
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if p.state.Status.Workspace != workspaceBefore {
		t.Fatalf("copy moved workspace %q", p.state.Status.Workspace)
	}
	if p.state.Status.Session != sessionBefore {
		t.Fatalf("copy moved session %q", p.state.Status.Session)
	}
	if p.state.Status.Busy != busyBefore {
		t.Fatalf("copy changed busy state")
	}
	if len(p.state.Entries) != entriesBefore {
		t.Fatal("copy changed execution history")
	}
	if p.pending != nil {
		t.Fatal("copy opened an approval")
	}
}

// The header keeps its one-row contract with the confirmation present, and
// the confirmation never covers permanent chrome.
func TestCopyNoticeHeaderComposesWithChrome(t *testing.T) {
	p, _ := noticeDragProgram(t, OSC52Sequence)
	p.state.Status.Model = "groq/deepseek-r1-distill-llama-70b"
	p.state.Status.Version = "1.0.0"
	p.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	p.syncTranscript()
	marker, top := noticeDragTop(p)
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	header := noticeHeaderPlain(p.state)
	if got := CellWidth(header); got > 100 {
		t.Fatalf("header is %d cells, want one row: %q", got, header)
	}
	for _, want := range []string{"ohgo", "v1.0.0", "idle", "Copied"} {
		if !strings.Contains(header, want) {
			t.Fatalf("header %q lost %q", header, want)
		}
	}
	idxCopied := strings.LastIndex(header, "Copied")
	idxIdle := strings.Index(header, "idle")
	if idxCopied < idxIdle {
		t.Fatalf("confirmation overlaps chrome: %q", header)
	}
	middle := header[idxIdle+len("idle") : idxCopied]
	if strings.TrimSpace(middle) != "" && !strings.Contains(middle, "·") && len(strings.TrimSpace(middle)) < 2 {
		t.Fatalf("confirmation crowds chrome without a gap: %q", header)
	}
}

// The configured lifetime sits inside the required window.
func TestCopyNoticeTTLInRange(t *testing.T) {
	if copyNoticeText != "Copied" {
		t.Fatalf("confirmation text = %q, want Copied", copyNoticeText)
	}
	if copyNoticeTTL < 1500*1000000 || copyNoticeTTL > 2000*1000000 {
		t.Fatalf("TTL = %v, want 1.5-2.0s", copyNoticeTTL)
	}
}
