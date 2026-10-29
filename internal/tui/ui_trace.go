package tui

// Lightweight UI instrumentation.
//
// Tracing is opt-in and never writes to the rendered frame. OHGO_UI_DEBUG=1
// writes one line per event to stderr; tests can install an observer instead.
// The normal path is a nil check, so instrumentation cannot affect layout or
// frame timing materially.

import (
	"fmt"
	"os"
)

// UI event names. They are stable strings so a debug log can be parsed.
const (
	traceScroll      = "viewport.scroll"
	traceFollow      = "viewport.follow"
	traceDetach      = "viewport.detach"
	traceAttach      = "viewport.attach"
	traceExpand      = "block.expand"
	traceCollapse    = "block.collapse"
	traceLayout      = "layout.reflow"
	traceResize      = "terminal.resize"
	traceSelection   = "selection.change"
	traceCopy        = "clipboard.copy"
	traceUnsupported = "clipboard.unsupported"
)

// trace emits one instrumented event when tracing is enabled.
func (p *Program) trace(event string) {
	if p.traceFn != nil {
		p.traceFn(event)
	}
}

// WithUIObserver installs a UI event observer. It is intended for tests and
// local diagnostics; the production default is nil (disabled).
func WithUIObserver(observer func(string)) ProgramOption {
	return func(p *Program) { p.traceFn = observer }
}

// traceWriter enables stderr tracing when OHGO_UI_DEBUG is set.
func traceWriterFromEnv() func(string) {
	if os.Getenv("OHGO_UI_DEBUG") == "" {
		return nil
	}
	return func(event string) { _, _ = fmt.Fprintln(os.Stderr, event) }
}
