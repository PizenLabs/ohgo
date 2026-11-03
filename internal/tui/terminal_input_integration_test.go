package tui

import (
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// scriptedReader returns one terminal chunk per Read, then blocks until the
// test releases it. It is the raw byte stream a terminal would hand to Bubble
// Tea, not a synthesised tea.KeyMsg.
type scriptedReader struct {
	chunks []string
	i      int
	done   chan struct{}
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		<-r.done
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}

type inputHarness struct {
	program *Program
}

type harnessQuit struct{}

func (m *inputHarness) Init() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return harnessQuit{} })
}

func (m *inputHarness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(harnessQuit); ok {
		return m, tea.Quit
	}
	model, cmd := m.program.Update(msg)
	m.program = model.(*Program)
	return m, cmd
}

func (m *inputHarness) View() string { return "" }

// The exact real-world failure, driven through Bubble Tea's own input decoder:
// the terminal writes ESC in one read and the three concatenated reports in the
// next. The composer must remain empty and all three reports must become
// semantic wheel events.
func TestRealInputDecoderDeliversConcatenatedReportsWithoutLeak(t *testing.T) {
	report := "\x1b[<65;96;26M"
	reader := &scriptedReader{
		chunks: []string{"\x1b", report[1:] + report + report},
		done:   make(chan struct{}),
	}
	defer close(reader.done)

	p := NewProgram(nil, "test", 0, WithMouseReporting())
	p.state = newViewportState(40, 80, 24)
	harness := &inputHarness{program: p}

	if _, err := tea.NewProgram(harness, tea.WithInput(reader), tea.WithoutRenderer()).Run(); err != nil {
		t.Fatalf("input harness: %v", err)
	}
	if p.state.Input != "" {
		t.Fatalf("real decoder path leaked SGR protocol into the composer: %q", p.state.Input)
	}
	if p.mouseReports != 3 {
		t.Fatalf("mouseReports = %d, want all three reports applied", p.mouseReports)
	}
}
