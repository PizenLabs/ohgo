package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/ohgo/internal/agent"
)

func retrieveFixture(t *testing.T) (*RetrieveTool, string) {
	t.Helper()
	root := structureFixture(t, map[string]string{
		"auth/service.go":      "package auth\n\n// Authentication validates a session token.\nfunc Authenticate(token string) error { return nil }\n",
		"auth/service_test.go": "package auth\n\nimport \"testing\"\n\nfunc TestAuthenticate(t *testing.T) { Authenticate(\"t\") }\n",
		"billing/checkout.go":  "package billing\n\nfunc Checkout(cart Cart) error { return nil }\n",
		"README.md":            "# Authentication and checkout\n",
	})
	return NewRetrieveTool(NewStructuralSource(root), t.TempDir()), root
}

func TestRetrieveToolRanksFilesAndReportsEvidence(t *testing.T) {
	tool, root := retrieveFixture(t)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"query": "authentication"})
	if result.IsError {
		t.Fatalf("retrieve failed: %s", result.Text)
	}
	if !strings.Contains(result.Text, "auth/service.go") {
		t.Fatalf("text missing ranked path:\n%s", result.Text)
	}
	if result.Mutates || result.Executes || result.Verifies {
		t.Fatalf("retrieval claimed an execution effect: %+v", result)
	}
	details := structureDetails(t, result)
	hits, ok := details["hits"].([]map[string]any)
	if !ok || len(hits) == 0 || hits[0]["path"] != "auth/service.go" {
		t.Fatalf("details hits = %#v", details["hits"])
	}
	evidence, ok := details["evidence"].(map[string]any)
	if !ok || evidence["state"] != "OBSERVED" || evidence["provider"] != "retrieval" {
		t.Fatalf("evidence = %#v", details["evidence"])
	}
	if details["documents"].(int) < 3 {
		t.Fatalf("documents = %v", details["documents"])
	}
	if !strings.Contains(root, "") {
		t.Fatal("unreachable")
	}
}

func TestRetrieveToolClassificationIsOptIn(t *testing.T) {
	tool, _ := retrieveFixture(t)
	ctx := agent.Context{Ctx: context.Background()}

	defaultResult := tool.Execute(ctx, map[string]any{"query": "authenticate"})
	if strings.Contains(defaultResult.Text, "service_test.go") {
		t.Fatalf("default page included a test file:\n%s", defaultResult.Text)
	}
	withTests := tool.Execute(ctx, map[string]any{"query": "authenticate", "include_tests": true})
	if !strings.Contains(withTests.Text, "service_test.go") {
		t.Fatalf("include_tests page omitted the test:\n%s", withTests.Text)
	}
	withDocs := tool.Execute(ctx, map[string]any{"query": "authentication", "include_docs": true})
	if !strings.Contains(withDocs.Text, "README.md") {
		t.Fatalf("include_docs page omitted the documentation:\n%s", withDocs.Text)
	}
}

func TestRetrieveToolSecondCallReusesLiveIndex(t *testing.T) {
	tool, _ := retrieveFixture(t)
	ctx := agent.Context{Ctx: context.Background()}
	first := tool.Execute(ctx, map[string]any{"query": "checkout"})
	second := tool.Execute(ctx, map[string]any{"query": "checkout"})
	if second.IsError {
		t.Fatalf("second call failed: %s", second.Text)
	}
	details := structureDetails(t, second)
	if details["cache"] != "active" {
		t.Fatalf("second call cache = %v, want active", details["cache"])
	}
	if details["bytes_read"].(int64) != 0 {
		t.Fatalf("second call read %v bytes", details["bytes_read"])
	}
	if !strings.Contains(first.Text, "billing/checkout.go") || !strings.Contains(second.Text, "billing/checkout.go") {
		t.Fatalf("unchanged index lost the ranked path:\n%s\n%s", first.Text, second.Text)
	}
}

func TestRetrieveToolRejectsInvalidRequests(t *testing.T) {
	tool, _ := retrieveFixture(t)
	ctx := agent.Context{Ctx: context.Background()}
	cases := []map[string]any{
		{},
		{"query": "   "},
		{"query": "auth", "limit": 0},
		{"query": "auth", "limit": 999},
		{"query": "auth", "path": "../outside"},
		{"query": "auth", "path": "/absolute"},
		{"query": "auth", "include_tests": "yes"},
	}
	for _, args := range cases {
		result := tool.Execute(ctx, args)
		if !result.IsError || result.FailureClass != agent.ToolFailureInvalidArgument {
			t.Fatalf("args %v → %+v, want an invalid-argument failure", args, result)
		}
	}
}

func TestRetrieveToolEmptyPageIsNotAFailure(t *testing.T) {
	tool, _ := retrieveFixture(t)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{"query": "nonexistentterm"})
	if result.IsError {
		t.Fatalf("empty page reported an error: %s", result.Text)
	}
	if !result.Useless {
		t.Fatal("empty page was not marked useless for compaction")
	}
	if !strings.Contains(result.Text, "not proof") {
		t.Fatalf("empty page did not disclose the evidence limit:\n%s", result.Text)
	}
}

func TestRetrieveToolScopeRestrictsCandidates(t *testing.T) {
	tool, _ := retrieveFixture(t)
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"query": "checkout authentication",
		"path":  "billing",
	})
	if result.IsError {
		t.Fatalf("scoped retrieve failed: %s", result.Text)
	}
	if strings.Contains(result.Text, "auth/service.go") {
		t.Fatalf("scope leaked an out-of-scope path:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "billing/checkout.go") {
		t.Fatalf("scope dropped the in-scope path:\n%s", result.Text)
	}
}
