package gryhook

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestGryHookLive is an opt-in compatibility test against a real GRY index.
// It does not mutate the repository: every stage is a read-only hook.
//
//	GRY_BIN=/path/to/gry GRY_CACHE=/path/to/cache GRY_PROJECT=goca \
//	  go test ./internal/gryhook -run TestGryHookLive -v
func TestGryHookLive(t *testing.T) {
	bin := os.Getenv("GRY_BIN")
	project := os.Getenv("GRY_PROJECT")
	cache := os.Getenv("GRY_CACHE")
	if bin == "" || project == "" || cache == "" {
		t.Skip("set GRY_BIN, GRY_CACHE and GRY_PROJECT to run the live hook test")
	}
	root := os.Getenv("GRY_ROOT")
	if root == "" {
		root = "."
	}
	cfg := Config{
		Enabled:   true,
		Binary:    bin,
		Project:   project,
		Root:      root,
		CacheDir:  cache,
		Timeout:   5 * time.Second,
		MaxTokens: 6000,
	}
	runner, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session := NewSession(runner, "live-test", "Trace the POST /task request to the repository")
	plan := session.BeforePlan(context.Background())
	if !plan.OK() {
		t.Fatalf("before_plan = %+v", plan)
	}
	if len(plan.Context) == 0 {
		t.Fatal("before_plan returned no context")
	}
	if plan.RepositoryScan {
		t.Fatal("a bounded hook claimed a repository-wide scan")
	}
	finish := session.BeforeFinish(context.Background())
	if finish.Status == "" || finish.Status == "disabled" {
		t.Fatalf("before_finish = %+v", finish)
	}
}
