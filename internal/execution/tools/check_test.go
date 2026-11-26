package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/control/contract"
)

func checkDetails(t *testing.T, result agent.Result) map[string]any {
	t.Helper()
	details, ok := result.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %T, want map[string]any", result.Details)
	}
	return details
}

func TestCheckToolDiscoversGoChecksWithoutExecuting(t *testing.T) {
	// Discovery must be read-only: no runner call, exact commands, and the
	// limits that follow from one module.
	root := structureFixture(t, map[string]string{
		"pkg/a.go":      "package pkg\n\nfunc A() {}\n",
		"pkg/a_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { A() }\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	called := false
	tool.run = func(context.Context, checkCommand, time.Duration) checkExecution {
		called = true
		return checkExecution{}
	}
	result := tool.Execute(testContext(), map[string]any{"mode": "discover"})
	if result.IsError {
		t.Fatalf("discover: %s", result.Text)
	}
	if called {
		t.Fatal("discover executed a command")
	}
	for _, want := range []string{"go test ./...", "go vet ./...", "go build ./..."} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("plan = %s\nmissing %q", result.Text, want)
		}
	}
	if result.Verifies {
		t.Fatal("discovery claimed verification evidence")
	}
	details := checkDetails(t, result)
	if details["status"] != "DISCOVERED" {
		t.Fatalf("status = %v, want DISCOVERED", details["status"])
	}
	if tool.Capability(map[string]any{"mode": "discover"}) != agent.TierRead {
		t.Fatal("discovery capability is not read-only")
	}
	if tool.Capability(map[string]any{"mode": "run"}) != agent.TierExec {
		t.Fatal("run capability is not execution")
	}
}

func TestCheckToolRunsScopedGoTestWithVerificationEvidence(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"pkg/a.go":      "package pkg\n\nfunc A() {}\n",
		"pkg/a_test.go": "package pkg\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { A() }\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	var ran checkCommand
	tool.run = func(_ context.Context, command checkCommand, _ time.Duration) checkExecution {
		ran = command
		return checkExecution{exitCode: 0, output: "=== RUN   TestA\n--- PASS: TestA (0.00s)\nPASS\nok  \texample.com/app/pkg\t0.01s\n"}
	}
	result := tool.Execute(testContext(), map[string]any{
		"mode": "run", "checks": []any{"test"}, "path": "pkg",
	})
	if result.IsError {
		t.Fatalf("run: %s", result.Text)
	}
	if got := strings.Join(ran.argv, " "); got != "go test ./pkg/..." {
		t.Fatalf("command = %q, want scoped go test", got)
	}
	if !result.Verifies || result.Verification == nil {
		t.Fatal("passing scoped test did not produce verification evidence")
	}
	if result.Verification.EffectiveCheck() != contract.CheckTest {
		t.Fatalf("check = %s, want test", result.Verification.EffectiveCheck())
	}
	if result.Verification.EffectiveClass() != agent.VerificationTest {
		t.Fatalf("class = %s, want TEST", result.Verification.EffectiveClass())
	}
	if len(result.Verification.Scopes) != 1 || result.Verification.Scopes[0].Path != "pkg" ||
		!result.Verification.Scopes[0].Recursive {
		t.Fatalf("scopes = %+v, want recursive pkg", result.Verification.Scopes)
	}
	details := checkDetails(t, result)
	checks, ok := details["checks"].([]map[string]any)
	if !ok || len(checks) != 1 {
		t.Fatalf("checks = %+v, want one result", details["checks"])
	}
	counts, ok := checks[0]["test_counts"].(map[string]any)
	if !ok || counts["passed"] != 1 || counts["failed"] != 0 {
		t.Fatalf("counts = %+v, want one passing test", checks[0]["test_counts"])
	}
}

func TestCheckToolFailingTestIsAnObservedFailure(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"pkg/a.go": "package pkg\n\nfunc A() {}\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	tool.run = func(context.Context, checkCommand, time.Duration) checkExecution {
		return checkExecution{
			exitCode: 1,
			output:   "--- FAIL: TestA (0.00s)\n    a_test.go:9: got 1, want 2\nFAIL\nexit status 1\n",
		}
	}
	result := tool.Execute(testContext(), map[string]any{
		"mode": "run", "checks": []any{"test"},
	})
	if !result.IsError {
		t.Fatal("failing test was not an error result")
	}
	if result.Verifies || result.Verification != nil {
		t.Fatal("failing test produced verification evidence")
	}
	if !strings.Contains(result.Text, "got 1, want 2") {
		t.Fatalf("failure output was dropped:\n%s", result.Text)
	}
	details := checkDetails(t, result)
	checks := details["checks"].([]map[string]any)
	counts := checks[0]["test_counts"].(map[string]any)
	if counts["failed"] != 1 || checks[0]["exit_code"] != 1 {
		t.Fatalf("result = %+v, want one failed test and exit 1", checks[0])
	}
}

func TestCheckToolExecutesOnlyRequestedKinds(t *testing.T) {
	root := structureFixture(t, map[string]string{
		"pkg/a.go": "package pkg\n\nfunc A() {}\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	var ran []checkKind
	tool.run = func(_ context.Context, command checkCommand, _ time.Duration) checkExecution {
		ran = append(ran, command.kind)
		return checkExecution{exitCode: 0}
	}
	result := tool.Execute(testContext(), map[string]any{
		"mode": "run", "checks": []any{"build"},
	})
	if result.IsError {
		t.Fatalf("run: %s", result.Text)
	}
	if len(ran) != 1 || ran[0] != checkBuild {
		t.Fatalf("ran = %v, want only build", ran)
	}
}

func TestCheckToolZeroTestsIsNotRunNotPass(t *testing.T) {
	// A scheduled check that executed but reported no test is not evidence
	// that the repository passes anything. It must stay NOT_RUN.
	root := structureFixture(t, map[string]string{
		"pkg/a.go": "package pkg\n\nfunc A() {}\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	tool.run = func(context.Context, checkCommand, time.Duration) checkExecution {
		return checkExecution{exitCode: 0, output: "?   \texample.com/app/pkg\t[no test files]\n"}
	}
	result := tool.Execute(testContext(), map[string]any{
		"mode": "run", "checks": []any{"test"},
	})
	if result.IsError {
		t.Fatalf("run: %s", result.Text)
	}
	if result.Verifies || result.Verification != nil {
		t.Fatalf("verifies=%v verification=%+v text=%s details=%+v", result.Verifies, result.Verification, result.Text, result.Details)
	}
	if !strings.Contains(result.Text, "checks: NOT_RUN") {
		t.Fatalf("result = %s, want NOT_RUN", result.Text)
	}
}

// A runner discovered from dependencies must be planned in its non-watch
// form; a bare vitest would start a watcher that never exits.
func TestCheckToolPlansNonWatchRunnerFallback(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"package.json": `{"devDependencies": {"vitest": "^1.0.0"}}`,
		"src/app.ts":   "export const app = 1;\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{"mode": "discover", "checks": []any{"test"}})
	if result.IsError {
		t.Fatalf("discover: %s", result.Text)
	}
	if !strings.Contains(result.Text, "npm exec -- vitest run") {
		t.Fatalf("plan = %s\nwant the non-watch vitest command", result.Text)
	}
}

func TestCheckToolRefusesUnsafePackageScript(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"package.json": `{"scripts": {"test": "rm -rf / && vitest run"}}`,
		"src/app.ts":   "export const app = 1;\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{"mode": "discover", "checks": []any{"test"}})
	if result.IsError {
		t.Fatalf("discover: %s", result.Text)
	}
	if strings.Contains(result.Text, "npm run test") {
		t.Fatalf("unsafe test script was planned:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "not planned") || !strings.Contains(result.Text, "recursive delete") {
		t.Fatalf("refusal reason missing:\n%s", result.Text)
	}
}

func TestCheckToolDisclosesSafePackageScript(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"package.json": `{"scripts": {"test": "vitest run"}}`,
		"src/app.ts":   "export const app = 1;\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{"mode": "discover", "checks": []any{"test"}})
	if result.IsError {
		t.Fatalf("discover: %s", result.Text)
	}
	if !strings.Contains(result.Text, "script: vitest run") {
		t.Fatalf("script body was not disclosed:\n%s", result.Text)
	}
	if !strings.Contains(result.Text, "lifecycle scripts") {
		t.Fatalf("lifecycle boundary was not disclosed:\n%s", result.Text)
	}
}

func TestCheckEnvironmentRedactsCredentialVariables(t *testing.T) {
	env := checkEnvironment([]string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"OPENAI_API_KEY=secret",
		"AWS_SECRET_ACCESS_KEY=secret",
		"AWS_REGION=us-east-1",
		"NPM_CONFIG_//registry:_authToken=secret",
		"SSH_AUTH_SOCK=/tmp/socket",
	})
	joined := strings.Join(env, "\n")
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/user", "AWS_REGION=us-east-1"} {
		if !strings.Contains(joined, keep) {
			t.Fatalf("env lost non-secret %q: %v", keep, env)
		}
	}
	for _, secret := range []string{"OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY", "NPM_CONFIG", "SSH_AUTH_SOCK"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("env kept credential %q: %v", secret, env)
		}
	}
}

func TestCheckRunRefusesDirectoryOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	tool := NewCheckTool(NewStructuralSource(root))
	called := false
	tool.run = func(context.Context, checkCommand, time.Duration) checkExecution {
		called = true
		return checkExecution{}
	}
	result := tool.runPlan(testContext(), checkPlan{
		scopeRoot: root,
		Commands: []checkCommand{{
			kind: checkTest, name: "outside", argv: []string{"true"}, dir: outside,
		}},
	}, time.Second)
	if !result.IsError || !strings.Contains(result.Text, "outside the workspace") {
		t.Fatalf("result = %s (error=%v), want a refusal", result.Text, result.IsError)
	}
	if called {
		t.Fatal("a command outside the workspace was executed")
	}
	if result.Executes || result.Finished {
		t.Fatalf("refused run claimed execution evidence: %+v", result)
	}
}

func TestCheckToolDiscoversNodeScripts(t *testing.T) {
	root := symbolsFixture(t, map[string]string{
		"package.json": `{
			"scripts": {"test": "vitest run", "build": "vite build", "lint": "eslint ."},
			"devDependencies": {"vitest": "^1.0.0", "typescript": "^5.0.0"}
		}`,
		"tsconfig.json": `{"compilerOptions": {}}`,
		"src/app.ts":    "export const app = 1;\n",
	})
	tool := NewCheckTool(NewStructuralSource(root))
	result := tool.Execute(testContext(), map[string]any{"mode": "discover"})
	if result.IsError {
		t.Fatalf("discover: %s", result.Text)
	}
	for _, want := range []string{"npm run test", "npm run build", "npm run lint"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("plan = %s\nmissing %q", result.Text, want)
		}
	}
	if !strings.Contains(result.Text, "ecosystems: node") {
		t.Fatalf("plan did not name the Node ecosystem:\n%s", result.Text)
	}
}
