package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func dapSpec(t *testing.T, mode string) Spec {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Spec{
		Name:    "fake",
		Command: []string{path, "-test.run=^TestDAPHelperAdapter$", "-test.timeout=60s"},
		Env:     []string{"OHGO_DAP_HELPER=1", "OHGO_DAP_MODE=" + mode},
	}
}

func dapManager(t *testing.T, mode string, maxSessions int, authorize Authorizer) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	manager := NewManager(root, Config{
		Adapters:    map[string]Spec{"fake": dapSpec(t, mode)},
		MaxSessions: maxSessions,
		Authorize:   authorize,
		Grace:       500 * time.Millisecond,
	})
	t.Cleanup(func() { _ = manager.Close() })
	return manager, root
}

func allowAll(context.Context, Action, string) error { return nil }

func launchFixture(t *testing.T, manager *Manager, root string) *Session {
	t.Helper()
	breakpointFile := filepath.Join(root, "main.go")
	if err := os.WriteFile(breakpointFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := manager.Launch(ctx, LaunchRequest{
		Adapter:   "fake",
		Arguments: json.RawMessage("{}"),
		Breakpoints: []SourceBreakpoints{{
			Path:        breakpointFile,
			Breakpoints: []SourceBreakpoint{{Line: 3}},
		}},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return session
}

func TestManagerLaunchObservesRuntimeState(t *testing.T) {
	manager, root := dapManager(t, "normal", 2, allowAll)
	session := launchFixture(t, manager, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := session.WaitEvent(ctx, "stopped"); err != nil {
		t.Fatalf("waiting for stopped: %v", err)
	}
	if state := session.State(); state != StateStopped {
		t.Fatalf("state = %s, want STOPPED", state)
	}
	threads, err := session.Threads(ctx)
	if err != nil || len(threads) != 1 || threads[0].Name != "main" {
		t.Fatalf("threads = %+v, %v", threads, err)
	}
	frames, err := session.StackTrace(ctx, threads[0].ID, 10)
	if err != nil || len(frames) != 1 || frames[0].Line != 3 {
		t.Fatalf("frames = %+v, %v", frames, err)
	}
	scopes, err := session.Scopes(ctx, frames[0].ID)
	if err != nil || len(scopes) != 1 || scopes[0].Name != "Locals" {
		t.Fatalf("scopes = %+v, %v", scopes, err)
	}
	variables, err := session.Variables(ctx, scopes[0].VariablesReference)
	if err != nil || len(variables) != 1 || variables[0].Value != "42" {
		t.Fatalf("variables = %+v, %v", variables, err)
	}
	value, err := session.Evaluate(ctx, "answer", frames[0].ID)
	if err != nil || value.Result != "42" {
		t.Fatalf("evaluate = %+v, %v", value, err)
	}
	exception, err := session.ExceptionInfo(ctx, threads[0].ID)
	if err != nil {
		t.Fatalf("exceptionInfo: %v", err)
	}
	if string(exception) == "" {
		t.Fatal("exceptionInfo returned an empty body")
	}
	if err := session.Continue(ctx, threads[0].ID); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if state := session.State(); state != StateTerminated {
		t.Fatalf("state after close = %s, want TERMINATED", state)
	}
	if session.Alive() {
		t.Fatal("session still alive after close")
	}
}

func TestManagerDeniesLaunchAndEvaluate(t *testing.T) {
	denyLaunch := func(context.Context, Action, string) error { return errors.New("not authorized") }
	manager, _ := dapManager(t, "normal", 2, denyLaunch)
	if _, err := manager.Launch(context.Background(), LaunchRequest{Adapter: "fake"}); err == nil {
		t.Fatal("unauthorized launch succeeded")
	}
	if len(manager.Sessions()) != 0 {
		t.Fatal("denied launch registered a session")
	}

	// The adapter exits 9 if it ever receives an evaluate request; a denial
	// must therefore leave it untouched.
	denyEvaluate := func(_ context.Context, action Action, _ string) error {
		if action == ActionEvaluate {
			return errors.New("evaluation not authorized")
		}
		return nil
	}
	manager, root := dapManager(t, "evaluateGuard", 2, denyEvaluate)
	session := launchFixture(t, manager, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := session.Evaluate(ctx, "dangerous()", 1); err == nil {
		t.Fatal("unauthorized evaluation succeeded")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session.client.mu.Lock()
	waitErr := session.client.waitErr
	session.client.mu.Unlock()
	if waitErr != nil {
		t.Fatalf("adapter observed an evaluation it should never have received: %v", waitErr)
	}
}

func TestManagerBoundsSessions(t *testing.T) {
	manager, root := dapManager(t, "normal", 1, allowAll)
	_ = launchFixture(t, manager, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := manager.Launch(ctx, LaunchRequest{Adapter: "fake"}); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("second launch error = %v, want ErrSessionLimit", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if len(manager.Sessions()) != 0 {
		t.Fatal("manager retained sessions after close")
	}
}

func TestSessionCloseKillsUnresponsiveAdapter(t *testing.T) {
	manager, root := dapManager(t, "ignoreDisconnect", 1, allowAll)
	session := launchFixture(t, manager, root)
	begin := time.Now()
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("close took %s; an unresponsive adapter was not reaped", elapsed)
	}
	if session.Alive() {
		t.Fatal("unresponsive adapter still alive")
	}
	session.client.mu.Lock()
	waitErr := session.client.waitErr
	session.client.mu.Unlock()
	if waitErr == nil {
		t.Fatal("adapter was not killed")
	}
}

func TestLaunchTimesOutWhenAdapterNeverInitializes(t *testing.T) {
	manager, _ := dapManager(t, "noInitialized", 1, allowAll)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := manager.Launch(ctx, LaunchRequest{Adapter: "fake", Timeout: 400 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("launch error = %v, want ErrTimeout", err)
	}
	if len(manager.Sessions()) != 0 {
		t.Fatal("timed-out launch registered a session")
	}
}

func TestBreakpointOutsideWorkspaceIsRejected(t *testing.T) {
	manager, root := dapManager(t, "normal", 1, allowAll)
	session := launchFixture(t, manager, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := session.SetBreakpoints(ctx, SourceBreakpoints{
		Path:        "/etc/passwd",
		Breakpoints: []SourceBreakpoint{{Line: 1}},
	})
	if err == nil {
		t.Fatal("out-of-workspace breakpoint was accepted")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateRejectsEmptyExpression(t *testing.T) {
	manager, root := dapManager(t, "normal", 1, allowAll)
	session := launchFixture(t, manager, root)
	if _, err := session.Evaluate(context.Background(), "   ", 1); err == nil {
		t.Fatal("empty expression was accepted")
	}
	_ = session.Close()
}

// TestDialTransportLifecycle proves the dial-client transport with a real
// subprocess and a loopback socket: the adapter connects back, the handshake is
// the same as stdio, and cleanup ends the process.
func TestDialTransportLifecycle(t *testing.T) {
	manager := NewManager(t.TempDir(), Config{
		Adapters: map[string]Spec{"fake": {
			Name:       "fake",
			Command:    []string{os.Args[0], "-test.run=TestDAPHelperAdapter"},
			Env:        []string{"OHGO_DAP_HELPER=1", "OHGO_DAP_DIAL={addr}"},
			DialClient: true, ConnectTimeout: 5 * time.Second,
		}},
		MaxSessions:   1,
		Authorize:     func(context.Context, Action, string) error { return nil },
		LaunchTimeout: 5 * time.Second,
		Grace:         time.Second,
	})
	t.Cleanup(func() { _ = manager.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := manager.Launch(ctx, LaunchRequest{Adapter: "fake"})
	if err != nil {
		t.Fatalf("launch over dial transport: %v", err)
	}
	if _, err := session.WaitEvent(ctx, "stopped"); err != nil {
		t.Fatalf("wait stopped: %v", err)
	}
	threads, err := session.Threads(ctx)
	if err != nil || len(threads) != 1 {
		t.Fatalf("threads = %v, %v", threads, err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if state := session.State(); state != StateTerminated {
		t.Fatalf("state = %s, want TERMINATED", state)
	}
}
