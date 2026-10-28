package tui

import (
	"strings"
	"testing"
)

func TestUpdateFoundIsFooterChromeNotTranscript(t *testing.T) {
	state := New()
	before := len(state.Entries)
	state.Apply(UpdateFound{Version: "1.2.3"})
	if len(state.Entries) != before {
		t.Fatalf("update notice added %d transcript entries", len(state.Entries)-before)
	}
	if state.Status.UpdateAvailable != "v1.2.3" {
		t.Fatalf("UpdateAvailable = %q, want v1.2.3", state.Status.UpdateAvailable)
	}
	rendered := renderStatus(state, DarkPalette(), Layout{})
	if !strings.Contains(rendered, "update v1.2.3") {
		t.Fatalf("footer = %q, want update hint", rendered)
	}
}

func TestUpdateFoundIgnoresEmptyVersion(t *testing.T) {
	state := New()
	state.Apply(UpdateFound{Version: "v1.2.3"})
	state.Apply(UpdateFound{Version: "   "})
	if state.Status.UpdateAvailable != "v1.2.3" {
		t.Fatalf("empty refresh erased a real observation: %q", state.Status.UpdateAvailable)
	}
}

func TestPublishUpdateAvailableUsesTheEventChannel(t *testing.T) {
	program := NewProgram(nil, "provider/model", 1000)
	if program.state.Status.UpdateAvailable != "" {
		t.Fatalf("new program already has an update hint: %q", program.state.Status.UpdateAvailable)
	}
	program.PublishUpdateAvailable("1.4.0")
	select {
	case event := <-program.events:
		found, ok := event.(UpdateFound)
		if !ok || found.Version != "1.4.0" {
			t.Fatalf("event = %#v, want UpdateFound{1.4.0}", event)
		}
	default:
		t.Fatal("PublishUpdateAvailable did not publish an event")
	}
	program.closeDone()
}
