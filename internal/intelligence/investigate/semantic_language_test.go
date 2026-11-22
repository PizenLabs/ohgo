package investigate

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/intelligence/planner"
	"github.com/PizenLabs/ohgo/internal/intelligence/semantic"
)

// TestSemanticProviderSelectsTheRequestLanguage proves a Python focus never
// starts the Go server and reports an explicit unavailable state instead of a
// false absence. This is the cross-language dispatch contract.
func TestSemanticProviderSelectsTheRequestLanguage(t *testing.T) {
	manager := semantic.NewManager(t.TempDir(), []semantic.Spec{{
		Name: "gopls", Languages: []string{"go"}, Command: []string{"gopls", "serve"},
	}}, 1)
	provider := &SemanticProvider{
		Root:  t.TempDir(),
		Start: func(context.Context) (*semantic.Manager, error) { return manager, nil },
	}
	meter := NewMeter(Budget{})
	observation, err := provider.Observe(context.Background(),
		Request{Question: "where is app.py defined?", Focus: "app.py"}, planner.Step{}, meter)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observation.Outcome != OutcomeUnavailable {
		t.Fatalf("outcome = %s, want UNAVAILABLE", observation.Outcome)
	}
	if len(observation.Issues) == 0 || !strings.Contains(observation.Issues[0], "python") {
		t.Fatalf("issues = %+v, want the missing Python server named", observation.Issues)
	}
	if meter.LSP() != 1 {
		t.Fatalf("LSP requests = %d, want one bounded attempt", meter.LSP())
	}
}

// TestSemanticProviderUsesWorkspaceDefault proves a symbol-only request uses
// the workspace's dominant language, not the first configured server.
func TestSemanticProviderUsesWorkspaceDefault(t *testing.T) {
	manager := semantic.NewManager(t.TempDir(), []semantic.Spec{{
		Name: "gopls", Languages: []string{"go"}, Command: []string{"gopls", "serve"},
	}}, 1)
	provider := &SemanticProvider{
		Root:            t.TempDir(),
		DefaultLanguage: func() string { return "python" },
		Start:           func(context.Context) (*semantic.Manager, error) { return manager, nil },
	}
	observation, err := provider.Observe(context.Background(),
		Request{Question: "where is Thing defined?", Focus: "Thing"}, planner.Step{}, NewMeter(Budget{}))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observation.Outcome != OutcomeUnavailable {
		t.Fatalf("outcome = %s, want UNAVAILABLE", observation.Outcome)
	}
	if len(observation.Issues) == 0 || !strings.Contains(observation.Issues[0], "python") {
		t.Fatalf("issues = %+v, want the missing Python server named", observation.Issues)
	}
}

func TestSemanticSnapshotIDCorrelatesWithStructuralSnapshot(t *testing.T) {
	provider := &SemanticProvider{SnapshotID: func() string { return "snap123" }}
	if got := provider.semanticSnapshotID("gopls"); got != "lsp:gopls:snap123" {
		t.Fatalf("snapshot id = %q, want lsp:gopls:snap123", got)
	}
	plain := (&SemanticProvider{}).semanticSnapshotID("pyright")
	if plain != "lsp:pyright" {
		t.Fatalf("snapshot id without a structural snapshot = %q", plain)
	}
}

func TestRequestLanguage(t *testing.T) {
	cases := []struct {
		focus string
		want  string
	}{
		{"app.tsx", "typescriptreact"},
		{"src/main.py", "python"},
		{"service/main.go", "go"},
		{"Thing", ""},
		{"where is `lib/posts.ts` used?", "typescript"},
	}
	for _, tc := range cases {
		got := requestLanguage(Request{Focus: tc.focus})
		if tc.focus == "" {
			got = requestLanguage(Request{Question: tc.focus})
		}
		if got != tc.want {
			t.Errorf("requestLanguage(%q) = %q, want %q", tc.focus, got, tc.want)
		}
	}
}
