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
// A legacy row containing an unknown dots-family spelling is repaired at the
// same history boundary. The viewport may show the truthful diagnostic, never
// the provider's bytes.
func TestResumedLegacyRowCanonicalizesUnknownDotsSpelling(t *testing.T) {
	raw := `<dots_protocol_call><invoke name="ls"><parameter name="path">cmd</parameter></invoke></dots_protocol_call>`
	stored := session.StoredMessage{Role: "assistant", Text: raw}

	message, err := session.ToAgentMessage(stored)
	if err != nil {
		t.Fatalf("ToAgentMessage: %v", err)
	}
	entries := EntriesFromMessages([]agent.Message{message})
	if len(entries) != 1 || entries[0].Kind != EntryAssistant {
		t.Fatalf("entries = %+v, want one assistant diagnostic", entries)
	}
	if !strings.Contains(entries[0].AssistantText, "could not be normalized") {
		t.Fatalf("assistant text = %q, want the truthful diagnostic", entries[0].AssistantText)
	}
	rendered := Render(State{Entries: entries, Theme: ThemeDark})
	for _, marker := range []string{"<dots", "</dots", "<invoke", "<parameter"} {
		if strings.Contains(strings.ToLower(rendered), marker) {
			t.Fatalf("resumed transcript leaked %q:\n%s", marker, rendered)
		}
	}
}

// Quoted protocol documentation in a legacy row is content. Repairing history
// must not turn a code example back into residue.
func TestResumedLegacyQuotedProtocolIsContent(t *testing.T) {
	raw := "Example:\n```text\n<dotsfunctioncall><invoke name=\"ls\"><parameter name=\"path\">cmd</parameter></invoke></dotsfunctioncall>\n```"
	stored := session.StoredMessage{Role: "assistant", Text: raw}

	message, err := session.ToAgentMessage(stored)
	if err != nil {
		t.Fatalf("ToAgentMessage: %v", err)
	}
	entries := EntriesFromMessages([]agent.Message{message})
	if len(entries) != 1 || entries[0].Kind != EntryAssistant {
		t.Fatalf("entries = %+v, want the quoted example preserved", entries)
	}
	if entries[0].AssistantText != raw {
		t.Fatalf("quoted content changed:\n got %q\nwant %q", entries[0].AssistantText, raw)
	}
}

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
