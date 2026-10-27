package gryhook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	log := filepath.Join(dir, "args.log")
	body = strings.ReplaceAll(body, "@LOG@", log)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func enabledConfig(t *testing.T, bin, project, log string) Config {
	t.Helper()
	t.Setenv("FAKE_GRY_LOG", log)
	return Config{
		Enabled:   true,
		Binary:    bin,
		Project:   project,
		Root:      t.TempDir(),
		Timeout:   2 * time.Second,
		MaxTokens: 1200,
	}
}

const okPayload = `{"hook":{"stage":"before_edit","status":"ok","cached":false},"context":{"status":"ok","evidence":{"freshness":"fresh","complete_for_scope":true,"repository_scan":false},"files":[{"path":"src/a.go"}],"nodes":[{"qn":"pkg.A","file":"src/a.go"}],"unknowns":[],"evidence":{"reasons":[]}},"failure":null}`

func TestRunInvokesStageAndMemoizesIdenticalRequests(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	bin := writeScript(t, dir, "gry", `printf '%s\n' "$@" >> "@LOG@"
cat <<'EOF'
`+okPayload+`
EOF`)
	runner, err := New(enabledConfig(t, bin, "proj", log))
	if err != nil {
		t.Fatal(err)
	}
	req := Request{TaskID: "run-1", ChangedPaths: []string{"src/a.go"}}
	first := runner.Run(context.Background(), "before_edit", req)
	if !first.OK() || first.Cached {
		t.Fatalf("first = %+v", first)
	}
	second := runner.Run(context.Background(), "before_edit", req)
	if !second.Cached {
		t.Fatalf("identical request was not memoised: %+v", second)
	}
	raw, _ := os.ReadFile(log)
	if strings.Count(string(raw), "before_edit") != 1 {
		t.Fatalf("expected one process invocation, log:\n%s", raw)
	}

	// A different file is a different lifecycle event and must not be
	// suppressed by the previous edit.
	other := req
	other.ChangedPaths = []string{"src/b.go"}
	third := runner.Run(context.Background(), "before_edit", other)
	if third.Cached {
		t.Fatal("a different changed path was incorrectly deduplicated")
	}
	raw, _ = os.ReadFile(log)
	if strings.Count(string(raw), "before_edit") != 2 {
		t.Fatalf("expected two process invocations, log:\n%s", raw)
	}
}

func TestDifferentTaskIdentityIsNotSuppressed(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	bin := writeScript(t, dir, "gry", `printf '%s\n' "$@" >> "@LOG@"
echo '`+okPayload+`'`)
	runner, err := New(enabledConfig(t, bin, "proj", log))
	if err != nil {
		t.Fatal(err)
	}
	_ = runner.Run(context.Background(), "before_edit", Request{TaskID: "a", ChangedPaths: []string{"x"}})
	second := runner.Run(context.Background(), "before_edit", Request{TaskID: "b", ChangedPaths: []string{"x"}})
	if second.Cached {
		t.Fatal("independent sessions must not share duplicate suppression")
	}
}

func TestTimeoutIsExplicitAndNotPermanentlyCached(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	bin := writeScript(t, dir, "gry", `sleep 0.3
echo '`+okPayload+`'`)
	cfg := enabledConfig(t, bin, "proj", log)
	cfg.Timeout = 50 * time.Millisecond
	runner, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{TaskID: "run-1", ChangedPaths: []string{"src/a.go"}}
	first := runner.Run(context.Background(), "before_edit", req)
	if first.Failure == nil || first.Failure.Code != "timeout" {
		t.Fatalf("timeout result = %+v", first)
	}
	cfg.Timeout = 2 * time.Second
	runner, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second := runner.Run(context.Background(), "before_edit", req)
	if !second.OK() {
		t.Fatalf("retry after timeout did not recover: %+v", second)
	}
}

func TestMalformedOutputIsNeverAnEmptySuccess(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "gry", `echo 'not json'`)
	runner, err := New(enabledConfig(t, bin, "proj", filepath.Join(dir, "log")))
	if err != nil {
		t.Fatal(err)
	}
	result := runner.Run(context.Background(), "before_edit", Request{TaskID: "r", ChangedPaths: []string{"x"}})
	if result.OK() {
		t.Fatalf("malformed output was treated as success: %+v", result)
	}
	if result.Failure == nil || result.Failure.Code != "invalid_output" {
		t.Fatalf("failure = %+v", result.Failure)
	}
	if len(result.Context) != 0 {
		t.Fatalf("malformed output produced context: %s", result.Context)
	}
}

func TestMismatchedStageIsInvalidOutput(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "gry", `echo '{"hook":{"stage":"before_plan","status":"ok"},"context":{},"failure":null}'`)
	runner, err := New(enabledConfig(t, bin, "proj", filepath.Join(dir, "log")))
	if err != nil {
		t.Fatal(err)
	}
	result := runner.Run(context.Background(), "after_edit", Request{TaskID: "r", ChangedPaths: []string{"x"}})
	if result.Failure == nil || result.Failure.Code != "invalid_output" {
		t.Fatalf("mismatched stage = %+v", result)
	}
}

func TestNotIndexedIsAPreservedFailure(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "gry", `echo '{"hook":{"stage":"before_plan","status":"not_indexed"},"context":null,"failure":{"code":"not_indexed","message":"project not found or not indexed: proj","retryable":true}}'`)
	runner, err := New(enabledConfig(t, bin, "proj", filepath.Join(dir, "log")))
	if err != nil {
		t.Fatal(err)
	}
	result := runner.Run(context.Background(), "before_plan", Request{TaskID: "r", Task: "x"})
	if result.OK() {
		t.Fatalf("not_indexed was treated as success: %+v", result)
	}
	if result.Failure == nil || result.Failure.Code != "not_indexed" {
		t.Fatalf("failure = %+v", result.Failure)
	}
	if len(result.Context) != 0 {
		t.Fatalf("not_indexed produced context: %s", result.Context)
	}
}

func TestSecretEnvironmentValuesAreRedacted(t *testing.T) {
	const secret = "sk-test-super-secret-value"
	t.Setenv("OPENAI_API_KEY", secret)
	dir := t.TempDir()
	bin := writeScript(t, dir, "gry", `echo '{"hook":{"stage":"before_edit","status":"unavailable"},"context":null,"failure":{"code":"unavailable","message":"upstream said `+secret+`","retryable":true}}'`)
	runner, err := New(enabledConfig(t, bin, "proj", filepath.Join(dir, "log")))
	if err != nil {
		t.Fatal(err)
	}
	result := runner.Run(context.Background(), "before_edit", Request{TaskID: "r", ChangedPaths: []string{"x"}})
	if result.Failure == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(result.Failure.Message, secret) {
		t.Fatalf("secret leaked: %q", result.Failure.Message)
	}
	if !strings.Contains(result.Failure.Message, "<redacted-env>") {
		t.Fatalf("secret was not replaced: %q", result.Failure.Message)
	}
}

func TestInvalidProjectIsRejected(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "gry", "true")
	if _, err := New(Config{Enabled: true, Binary: bin, Project: "bad name; rm -rf /"}); err == nil {
		t.Fatal("invalid project name was accepted")
	}
}

func TestOversizedOutputIsRejected(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "gry", `i=0; while [ $i -lt 20000 ]; do printf 'aaaaaaaaaaaaaaaa'; i=$((i+1)); done`)
	runner, err := New(enabledConfig(t, bin, "proj", filepath.Join(dir, "log")))
	if err != nil {
		t.Fatal(err)
	}
	result := runner.Run(context.Background(), "before_edit", Request{TaskID: "r", ChangedPaths: []string{"x"}})
	if result.Failure == nil || result.Failure.Code != "output_too_large" {
		t.Fatalf("failure = %+v", result.Failure)
	}
}

func TestEvidenceTextIsBoundedAndCarriesUncertainty(t *testing.T) {
	ctx := []byte(`{"status":"partial","evidence":{"freshness":"stale","complete_for_scope":false,"repository_scan":false,"reasons":["added_files_not_indexed"]},"files":[{"path":"a.go"}],"nodes":[{"qn":"pkg.A","file":"a.go"}],"unknowns":[]}`)
	text := EvidenceText(Result{Stage: "before_edit", Status: "partial", Freshness: "stale", Context: ctx})
	if !strings.Contains(text, "before_edit") || !strings.Contains(text, "added_files_not_indexed") {
		t.Fatalf("evidence text lost status/uncertainty:\n%s", text)
	}
	if !strings.Contains(text, "not as permission") {
		t.Fatalf("evidence text lacks the authority boundary:\n%s", text)
	}
	if len(text) > 6100 {
		t.Fatalf("evidence text is unbounded: %d", len(text))
	}
}

func TestDisabledRunnerStartsNoProcess(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	log := filepath.Join(dir, "args.log")
	bin := writeScript(t, dir, "gry", `echo x >> "@LOG@"`)
	cfg := enabledConfig(t, bin, "proj", log)
	cfg.Enabled = false
	runner, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result := runner.Run(context.Background(), "before_plan", Request{TaskID: "r"})
	if result.Status != "disabled" {
		t.Fatalf("status = %q", result.Status)
	}
	if calls.Load() != 0 {
		t.Fatal("disabled runner started a process")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("disabled runner wrote a log: %v", err)
	}
}
