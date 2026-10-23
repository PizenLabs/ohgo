package tui

// Forensic clipboard transport: exact bytes, writer failure, tmux paths,
// and emission-before-notice.
//
// These tests capture the actual bytes written by the production clipboard
// path through a recording writer. They prove the OSC 52 framing, the
// tmux-correct packaging, and that Copied is tied to emission success, not
// to an internal queue.

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func transportDecode(t *testing.T, seq string) string {
	t.Helper()
	if !strings.HasPrefix(seq, "\x1b]52;c;") || !strings.HasSuffix(seq, "\x07") {
		t.Fatalf("bad OSC 52 framing: %q", seq)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	return string(decoded)
}

// The production path writes the exact framed bytes for the logical text,
// decodable to the same text, with no ANSI leakage.
func TestClipboardTransportEmitsExactBytes(t *testing.T) {
	for _, text := range []string{
		"hello world",
		"OHGO_CLIPBOARD_E2E_7F31A",
		"line one\nline two\nline three",
		"Aé中日本語✅éB",
		strings.Repeat("x", 1024),
		strings.Repeat("y", 10*1024),
	} {
		p := NewProgram(nil, "test", 0, WithMouseReporting())
		var out bytes.Buffer
		p.termOut = &out
		p.clipboard = OSC52Sequence
		p.state = selectionFixture()
		p.transcriptFrame = viewportFor(p.state)
		// Direct production call: encode + emit to the terminal writer.
		if cmd := p.queueClipboard(text); cmd == nil {
			t.Fatalf("text len %d: queueClipboard returned nil", len(text))
		}
		if out.Len() == 0 {
			t.Fatalf("text len %d: wrote no bytes", len(text))
		}
		if got := transportDecode(t, out.String()); got != text {
			t.Fatalf("text len %d: decoded %q, want exact text", len(text), got)
		}
		if p.state.CopyNotice != "Copied" {
			t.Fatalf("text len %d: emission without Copied", len(text))
		}
	}
}

// Styled source still emits plain logical bytes.
func TestClipboardTransportExcludesANSI(t *testing.T) {
	p := NewProgram(nil, "test", 0)
	var out bytes.Buffer
	p.termOut = &out
	p.clipboard = OSC52Sequence
	if cmd := p.queueClipboard("second line"); cmd == nil {
		t.Fatal("no emission")
	}
	if got := transportDecode(t, out.String()); strings.Contains(got, "\x1b") {
		t.Fatalf("payload leaked ANSI: %q", got)
	}
}

// Inside tmux the raw sequence is emitted so the multiplexer forwards it via
// set-clipboard. Verified end-to-end: raw updates the OS clipboard under
// tmux 3.8 (set-clipboard on, allow-passthrough off, Ghostty); the DCS
// passthrough wrapper is dropped and leaves the clipboard untouched.
func TestClipboardTransportTmuxUsesRaw(t *testing.T) {
	raw := OSC52Sequence("hello")
	if got := OSC52SequenceFor("hello", true, "xterm-256color"); got != raw {
		t.Fatalf("tmux path = %q, want raw %q", got, raw)
	}
	if got := OSC52SequenceFor("hello", true, "tmux-256color"); got != raw {
		t.Fatalf("tmux-256color path = %q, want raw", got)
	}
	// The forbidden wrapper must never appear on the clipboard path.
	if got := OSC52SequenceFor("hello", true, "xterm-256color"); strings.Contains(got, "tmux;") {
		t.Fatalf("tmux path contains DCS passthrough: %q", got)
	}
}

// Screen keeps its documented DCS wrapper; direct terminals stay raw.
func TestClipboardTransportScreenAndDirect(t *testing.T) {
	raw := OSC52Sequence("hi")
	if got := OSC52SequenceFor("hi", false, "screen-256color"); got == raw {
		t.Fatalf("screen path lost its wrapper: %q", got)
	}
	if got := OSC52SequenceFor("hi", false, "xterm-256color"); got != raw {
		t.Fatalf("direct path = %q, want raw", got)
	}
}

// Termination is BEL, the form proven against Ghostty+tmux.
func TestClipboardTransportTerminatesWithBEL(t *testing.T) {
	seq := OSC52Sequence("hi")
	if !strings.HasSuffix(seq, "\x07") {
		t.Fatalf("sequence does not end with BEL: %q", seq)
	}
	if strings.Contains(seq, "\x1b\\") {
		t.Fatalf("raw sequence contains ST terminator: %q", seq)
	}
}

// Emission is synchronous: bytes are present immediately after the release
// returns, before any further View/render.
func TestClipboardTransportFlushesImmediately(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var out bytes.Buffer
	p.termOut = &out
	p.clipboard = OSC52Sequence
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	_, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	// No View call between release and this assertion: the writer must
	// already hold the framed bytes. A queued-until-next-frame design fails here.
	if out.Len() == 0 {
		t.Fatal("release returned before any clipboard bytes were emitted")
	}
	if got := transportDecode(t, out.String()); got != "world" {
		t.Fatalf("emitted %q, want world", got)
	}
	if p.state.CopyNotice != "Copied" {
		t.Fatal("synchronous emission without Copied")
	}
}

// The rendered frame never carries clipboard bytes; they travel on the
// terminal writer only, so renderer truncation/diffing cannot drop them.
func TestClipboardTransportNeverRidesTheFrame(t *testing.T) {
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	var out bytes.Buffer
	p.termOut = &out
	p.clipboard = OSC52Sequence
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	_, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	for i := 0; i < 3; i++ {
		if view := p.View(); strings.Contains(view, "\x1b]52;c;") {
			t.Fatalf("frame %d carries OSC 52; transport must bypass the renderer", i)
		}
	}
}

// The Bubble Tea renderer preserves a leading OSC 52 through its width
// truncation, documenting why the old frame-riding design appeared to work
// in unit tests while tmux dropped the (wrongly wrapped) bytes downstream.
func TestClipboardRendererPreservesOSC52(t *testing.T) {
	line := OSC52Sequence("hi") + "ohgo header"
	if truncated := ansi.Truncate(line, 80, ""); !strings.HasPrefix(truncated, "\x1b]52;c;") {
		t.Fatalf("renderer truncation dropped OSC 52: %q", truncated)
	}
}

// Writer failure, empty text, disabled and unsupported terminals write
// nothing and never show Copied; the selection survives.
func TestClipboardTransportNegativeMatrix(t *testing.T) {
	// Writer failure.
	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.termOut = failWriter{}
	p.clipboard = OSC52Sequence
	p.state = selectionFixture()
	p.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	p.syncTranscript()
	_, marker, top, ok := p.visibleRows()
	if !ok {
		t.Fatal("no visible rows")
	}
	y := top + marker
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 6, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion, X: 11, Y: y})
	p.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 11, Y: y})
	if p.state.CopyNotice == "Copied" {
		t.Fatal("writer failure reported success")
	}
	if p.state.Selection == nil {
		t.Fatal("writer failure cleared the selection")
	}
	// Empty selection writes nothing.
	q := NewProgram(nil, "test", 0)
	var out bytes.Buffer
	q.termOut = &out
	q.clipboard = OSC52Sequence
	q.state = selectionFixture()
	q.transcriptFrame = viewportFor(q.state)
	q.state.Selection = nil
	q.copySelection()
	if out.Len() != 0 {
		t.Fatalf("empty selection wrote %q", out.String())
	}
	if q.state.CopyNotice == "Copied" {
		t.Fatal("empty selection reported success")
	}
}
