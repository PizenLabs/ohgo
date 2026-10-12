package main

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/agent"
	"github.com/PizenLabs/ohgo/ai"
	"github.com/PizenLabs/ohgo/catalog"
	"github.com/PizenLabs/ohgo/config"
	"github.com/PizenLabs/ohgo/jsonschema"
	"github.com/PizenLabs/ohgo/workspace"
)

func testSwitchHarness(t *testing.T) *harness {
	t.Helper()
	loop, err := agent.New(agent.Config{
		Model: catalog.Default(),
		Stream: func(ctx context.Context, req ai.Request) (*ai.Stream, error) {
			return ai.NewCompletedStream(ai.AssistantMessage{}), nil
		},
		Tools: []agent.Tool{testIdleTool{}},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	settings := config.Default()
	return newHarness(ai.NewRegistry(), loop, nil, nil, &agent.UsageLog{}, &settings, t.TempDir(), catalog.Default())
}

type testIdleTool struct{}

func (testIdleTool) Name() string                   { return "idle" }
func (testIdleTool) Description() string            { return "idle" }
func (testIdleTool) Parameters() *jsonschema.Schema { return nil }

func (testIdleTool) Approval() agent.Approval      { return agent.Approval{} }
func (testIdleTool) Exclusive(map[string]any) bool { return false }
func (testIdleTool) Execute(agent.Context, map[string]any) agent.Result {
	return agent.Result{}
}

func TestSwitchWorkspaceCyclesAuthority(t *testing.T) {
	h := testSwitchHarness(t)
	for _, tc := range []struct {
		name string
		want workspace.Workspace
	}{
		{"ask", workspace.Ask},
		{"fuzz", workspace.Fuzz},
		{"build", workspace.Build},
	} {
		got, err := h.SwitchWorkspace(tc.name)
		if err != nil {
			t.Fatalf("SwitchWorkspace(%s): %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("workspace = %q, want %q", got, tc.want)
		}
		if h.workspace != tc.want.Label() {
			t.Errorf("harness label = %q, want %q", h.workspace, tc.want.Label())
		}
		// The runtime gate must follow the label: ASK denies mutation.
		if err := workspace.Admit(tc.want, workspace.OpWrite); (tc.want == workspace.Build) == (err != nil) {
			t.Errorf("Admit(%s, write) err = %v, want build-only", tc.want, err)
		}
	}
}

func TestSwitchWorkspaceRejectsUnknown(t *testing.T) {
	h := testSwitchHarness(t)
	if _, err := h.SwitchWorkspace("nope"); err == nil {
		t.Error("unknown workspace accepted")
	}
}

func TestReplWorkspaceCommandsReachOwner(t *testing.T) {
	t.Setenv("OHGO_HISTORY_FILE", "")
	h := testSwitchHarness(t)
	in := strings.NewReader("/ask\n/fuzz\n/build\n/quit\n")
	var out strings.Builder
	if err := repl(h, &out, in, nil); err != nil {
		t.Fatalf("repl: %v", err)
	}
	text := out.String()
	for _, want := range []string{"switched to ASK", "switched to FUZZ", "switched to BUILD"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "unknown command /ask") || strings.Contains(text, "unknown command /fuzz") || strings.Contains(text, "unknown command /build") {
		t.Errorf("workspace command reported unknown:\n%s", text)
	}
}
