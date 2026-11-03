package tui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/session"
)

// A session written before stream normalization stored provider protocol inside
// ordinary assistant text. Resuming must canonicalize at the history boundary,
// so scrolling the resumed transcript cannot reveal syntax the live path never
// showed.
func TestResumedLegacyRowCanonicalizesProviderProtocol(t *testing.T) {
	raw := `<dotsfunctioncall><invoke name="ls"><parameter name="path">cmd</parameter></invoke></dotsfunctioncall>`
	stored := session.StoredMessage{Role: "assistant", Text: raw}

	message, err := session.ToAgentMessage(stored)
	if err != nil {
		t.Fatalf("ToAgentMessage: %v", err)
	}
	entries := EntriesFromMessages([]agent.Message{message})
	if len(entries) != 1 || entries[0].Kind != EntryTool || entries[0].Tool == nil {
		t.Fatalf("entries = %+v, want one canonical tool block", entries)
	}
	if entries[0].Tool.Name != "ls" {
		t.Fatalf("tool = %+v, want ls", entries[0].Tool)
	}
	rendered := Render(State{Entries: entries, Theme: ThemeDark})
	for _, marker := range []string{
		"<dotsfunctioncall>", "</dotsfunctioncall>", "<invoke", "<parameter",
	} {
		if strings.Contains(rendered, marker) {
			t.Fatalf("resumed transcript leaked %q:\n%s", marker, rendered)
		}
	}
}
