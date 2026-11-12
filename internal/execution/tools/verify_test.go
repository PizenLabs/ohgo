package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/workspace"
	"github.com/PizenLabs/ohgo/internal/provider/ai"
)

// advertisingService is a long-running shell command that advertises an
// already-running test HTTP server. The service process owns no listener, so
// the test can exercise readiness, checks, and process cleanup independently.
func advertisingService(url string) string {
	return fmt.Sprintf("printf 'serving at %s\\n'; sleep 30", url)
}

func verifyDetails(t *testing.T, result agent.Result) map[string]any {
	t.Helper()
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T, want map[string]any", result.Details)
	}
	verification, ok := details["verification"].(map[string]any)
	if !ok {
		t.Fatalf("verification details = %#v", details["verification"])
	}
	return verification
}

func TestVerifyToolStartsReadyHTTPAndCleansUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html>portfolio works</html>")
	}))
	defer server.Close()

	root := t.TempDir()
	pidFile := filepath.Join(root, "service.pid")
	command := fmt.Sprintf("echo $$ > %s; printf 'serving at %s\\n'; sleep 30", pidFile, server.URL)
	result := NewVerifyTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{"command": command},
		"checks": []any{map[string]any{
			"kind": "http", "contains": []any{"portfolio works"},
		}},
	})
	if result.IsError {
		t.Fatalf("verification failed: %s", result.Text)
	}
	if !result.Verifies || !result.Executes || !result.Finished {
		t.Fatalf("result evidence = %+v", result)
	}
	details := verifyDetails(t, result)
	if details["status"] != "PASS" {
		t.Fatalf("status = %v, want PASS", details["status"])
	}
	service, _ := details["service"].(map[string]any)
	pid, _ := service["pid"].(int)
	if pid <= 0 {
		t.Fatalf("service pid = %v", service["pid"])
	}
	if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err == nil {
		t.Fatalf("service pid %d is still alive after verification", pid)
	}
	if !strings.Contains(result.Text, "cleanup: stopped") {
		t.Fatalf("result does not report cleanup: %s", result.Text)
	}
	intent := workspace.BuildIntentContract("use npx live-server test this project")
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{
			{Results: []agent.ToolOutcome{{ToolName: "verify", Result: result}}},
			{Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "verified"}}}},
		},
		Contract: intent,
		Root:     root,
	})
	if !completion.Complete() {
		t.Fatalf("passing service + HTTP verification did not prove the objective: %s", completion.Reason)
	}
}

func TestVerifyToolReportsStartFailureWithCapturedStderr(t *testing.T) {
	root := t.TempDir()
	result := NewVerifyTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{
			"command": "echo 'boom: missing runtime dependency' >&2; exit 3",
			"readiness": map[string]any{
				"kind": "http", "url": "http://127.0.0.1:1/", "timeout_ms": float64(500),
			},
		},
		"checks": []any{map[string]any{
			"kind": "http", "url": "http://127.0.0.1:1/",
		}},
	})
	if !result.IsError {
		t.Fatalf("a service that exits before readiness was reported as success: %s", result.Text)
	}
	if result.FailureClass != agent.ToolFailureStart {
		t.Fatalf("failure class = %q, want %q", result.FailureClass, agent.ToolFailureStart)
	}
	if !strings.Contains(result.Text, "boom: missing runtime dependency") {
		t.Fatalf("captured stderr missing from result: %s", result.Text)
	}
	if result.Verifies {
		t.Fatal("a failed start was recorded as verification evidence")
	}
	intent := workspace.BuildIntentContract("use npx live-server test this project")
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{
			{Results: []agent.ToolOutcome{{ToolName: "verify", Result: result}}},
			{Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "verified"}}}},
		},
		Contract: intent,
		Root:     root,
	})
	if completion.Complete() {
		t.Fatal("a failed service start was reported complete")
	}
}

func TestVerifyToolDetectsServiceCrashDuringChecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	root := t.TempDir()
	// The service exits after 100 ms. Readiness succeeds first; the command
	// check outlives the service, so a naive pass would miss the crash.
	command := fmt.Sprintf("printf 'serving at %s\\n'; sleep 0.1; exit 7", server.URL)
	result := NewVerifyTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{"command": command},
		"checks": []any{map[string]any{
			"kind": "command", "command": "sleep 0.4",
		}},
	})
	if !result.IsError {
		t.Fatalf("a service crash during verification was reported as success: %s", result.Text)
	}
	if result.FailureClass != agent.ToolFailureRuntime {
		t.Fatalf("failure class = %q, want %q", result.FailureClass, agent.ToolFailureRuntime)
	}
	if result.Verifies {
		t.Fatal("a crashed service produced verification evidence")
	}
}

func TestVerifyToolReportsHTTPVerificationFailureAfterReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusInternalServerError)
	}))
	defer server.Close()
	root := t.TempDir()
	result := NewVerifyTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{"command": advertisingService(server.URL)},
		"checks": []any{map[string]any{
			"kind": "http", "status": float64(200),
		}},
	})
	if !result.IsError {
		t.Fatalf("a 500 response was reported as success: %s", result.Text)
	}
	if result.FailureClass != agent.ToolFailureVerification {
		t.Fatalf("failure class = %q, want %q", result.FailureClass, agent.ToolFailureVerification)
	}
	if !result.Executes || !result.Finished {
		t.Fatalf("service execution evidence = %+v", result)
	}
	if result.Verifies {
		t.Fatal("a failed check was recorded as verification evidence")
	}
	intent := workspace.BuildIntentContract("use npx live-server test this project")
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{
			{Results: []agent.ToolOutcome{{ToolName: "verify", Result: result}}},
			{Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "failed"}}}},
		},
		Contract: intent,
		Root:     root,
	})
	if completion.Complete() {
		t.Fatal("a failed HTTP check was reported complete")
	}
	if !strings.Contains(completion.Reason, string(agent.ToolFailureVerification)) {
		t.Fatalf("completion reason = %q, want the verification failure class", completion.Reason)
	}
}

func TestVerifyToolRunsBrowserCheckWhenAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html>ignored raw body</html>")
	}))
	defer server.Close()
	root := t.TempDir()
	browser := filepath.Join(root, "fake-browser")
	if err := os.WriteFile(browser, []byte("#!/bin/sh\necho '<html><body>rendered portfolio</body></html>'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := NewVerifyTool(root)
	tool.BrowserPath = browser
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{"command": advertisingService(server.URL)},
		"checks": []any{map[string]any{
			"kind": "browser", "contains": []any{"rendered portfolio"},
		}},
	})
	if result.IsError {
		t.Fatalf("browser verification failed: %s", result.Text)
	}
	if !result.Verifies {
		t.Fatal("a passing browser check did not count as verification")
	}
	intent := workspace.BuildIntentContract("use npx live-server test this project")
	completion := agent.EvaluateTaskCompletion(agent.CompletionInput{
		Turns: []agent.Turn{
			{Results: []agent.ToolOutcome{{ToolName: "verify", Result: result}}},
			{Assistant: ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "verified"}}}},
		},
		Contract: intent,
		Root:     root,
	})
	if !completion.Complete() {
		t.Fatalf("passing service + browser verification did not prove the objective: %s", completion.Reason)
	}
}

func TestVerifyToolReportsOptionalBrowserAsInsufficient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html>ok</html>")
	}))
	defer server.Close()
	root := t.TempDir()
	tool := NewVerifyTool(root)
	tool.BrowserPath = filepath.Join(root, "missing-browser")
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{"command": advertisingService(server.URL)},
		"checks": []any{map[string]any{
			"kind": "browser", "contains": []any{"never"},
		}},
	})
	if !result.IsError {
		t.Fatalf("an unavailable optional browser was reported as proven: %s", result.Text)
	}
	if result.FailureClass != agent.ToolFailureInsufficient {
		t.Fatalf("failure class = %q, want %q", result.FailureClass, agent.ToolFailureInsufficient)
	}
	if result.Verifies {
		t.Fatal("an unrun browser check was recorded as verification")
	}
}

func TestVerifyToolWaitsForReadinessBeforeDependentChecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html>ready page</html>")
	}))
	defer server.Close()
	root := t.TempDir()
	marker := filepath.Join(root, "browser-ran")
	browser := filepath.Join(root, "fake-browser")
	script := fmt.Sprintf("#!/bin/sh\ntest -f %q || exit 9\necho '<html>ready page</html>'\n", marker)
	if err := os.WriteFile(browser, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The service advertises the URL only after a delay. If the browser check
	// runs before readiness, the marker file cannot exist and the check fails.
	command := fmt.Sprintf("sleep 0.25; touch %s; printf 'serving at %s\\n'; sleep 30", marker, server.URL)
	tool := NewVerifyTool(root)
	tool.BrowserPath = browser
	result := tool.Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{"command": command},
		"checks": []any{map[string]any{
			"kind": "browser", "contains": []any{"ready page"},
		}},
	})
	if result.IsError {
		t.Fatalf("dependent browser check ran before readiness: %s", result.Text)
	}
}

func TestVerifyToolRunsIndependentChecksConcurrently(t *testing.T) {
	root := t.TempDir()
	check := func(name, other string) map[string]any {
		script := fmt.Sprintf(
			"touch %s; for i in $(seq 1 100); do test -f %s && exit 0; sleep 0.02; done; exit 1",
			name, other)
		return map[string]any{"kind": "command", "command": script}
	}
	start := time.Now()
	result := NewVerifyTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"parallel": true,
		"checks":   []any{check("a.marker", "b.marker"), check("b.marker", "a.marker")},
	})
	if result.IsError {
		t.Fatalf("independent checks did not run concurrently: %s", result.Text)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("checks took %s; the barrier only succeeds when they overlap", elapsed)
	}
}

func TestVerifyToolCancellationCleansOwnedProcesses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	root := t.TempDir()
	pidFile := filepath.Join(root, "service.pid")
	command := fmt.Sprintf("echo $$ > %s; printf 'serving at %s\\n'; sleep 30", pidFile, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	tool := NewVerifyTool(root)
	tool.Now = time.Now
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	result := tool.Execute(agent.Context{Ctx: ctx}, map[string]any{
		"service": map[string]any{"command": command, "readiness": map[string]any{"kind": "http"}},
		"checks": []any{
			map[string]any{"kind": "command", "command": "sleep 5", "timeout_ms": float64(10000)},
		},
	})
	if !result.IsError {
		t.Fatalf("cancelled verification reported success: %s", result.Text)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("service pid was not recorded: %v", err)
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		t.Fatal("empty service pid")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("kill", "-0", pid).Run(); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("owned service pid %s survived cancellation", pid)
}

func TestVerifyToolCancellationKillsBrowserProcessGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	root := t.TempDir()
	pidFile := filepath.Join(root, "browser.pid")
	browser := filepath.Join(root, "fake-browser")
	script := fmt.Sprintf("#!/bin/sh\necho $$ > %s\nsleep 30\n", pidFile)
	if err := os.WriteFile(browser, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := NewVerifyTool(root)
	tool.BrowserPath = browser
	go func() {
		// Cancel only after the browser has actually started, so the test
		// observes cleanup of a live process rather than a race with startup.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(data)) != "" {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	result := tool.Execute(agent.Context{Ctx: ctx}, map[string]any{
		"service": map[string]any{"command": advertisingService(server.URL)},
		"checks": []any{map[string]any{
			"kind": "browser", "contains": []any{"never"},
		}},
	})
	if !result.IsError {
		t.Fatalf("cancelled browser verification reported success: %s", result.Text)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("browser never started: %v\n%s", err, result.Text)
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		t.Fatal("empty browser pid")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("kill", "-0", pid).Run(); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	out, _ := exec.Command("ps", "-o", "pid,ppid,pgid,command", "-p", pid).CombinedOutput()
	t.Fatalf("browser process survived verification cancellation: %s", out)
}

func TestVerifyToolReadinessTimeoutIsOwnedAndCleaned(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "service.pid")
	command := fmt.Sprintf("echo $$ > %s; sleep 30", pidFile)
	result := NewVerifyTool(root).Execute(agent.Context{Ctx: context.Background()}, map[string]any{
		"service": map[string]any{
			"command": command,
			"readiness": map[string]any{
				"kind": "http", "url": "http://127.0.0.1:1/", "timeout_ms": float64(250),
			},
		},
		"checks": []any{map[string]any{"kind": "http", "url": "http://127.0.0.1:1/"}},
	})
	if !result.IsError {
		t.Fatalf("a readiness timeout was reported as success: %s", result.Text)
	}
	if result.FailureClass != agent.ToolFailureStart {
		t.Fatalf("failure class = %q, want %q", result.FailureClass, agent.ToolFailureStart)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("service pid was not recorded: %v", err)
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		t.Fatal("empty service pid")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("kill", "-0", pid).Run(); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed-out service pid %s survived cleanup", pid)
}
