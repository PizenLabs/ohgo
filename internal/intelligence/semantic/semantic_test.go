package semantic

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func executable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return path
}

func fakeSpec(t *testing.T, mode string, timeout time.Duration) Spec {
	return Spec{
		Name:         "fake",
		Languages:    []string{"go"},
		Command:      []string{executable(t), "-test.run=^TestLSPHelperServer$", "-test.timeout=60s"},
		Env:          []string{"OHGO_LSP_HELPER=1", "OHGO_LSP_MODE=" + mode},
		StartTimeout: timeout,
	}
}

func fakeManager(t *testing.T, mode string, maxServers int) *Manager {
	t.Helper()
	manager := NewManager(t.TempDir(), []Spec{fakeSpec(t, mode, 20*time.Second)}, maxServers)
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestTransportRoundTripAndValidation(t *testing.T) {
	var buffer bytes.Buffer
	first, err := encodeMessage(message{JSONRPC: "2.0", Method: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := encodeMessage(message{JSONRPC: "2.0", Method: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(&buffer, first); err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(&buffer, second); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(&buffer)
	for _, want := range []string{"one", "two"} {
		payload, err := readMessage(reader)
		if err != nil {
			t.Fatalf("readMessage: %v", err)
		}
		if !strings.Contains(string(payload), `"`+want+`"`) {
			t.Fatalf("payload = %s", payload)
		}
	}

	for name, framed := range map[string]string{
		"missing":   "Content-Type: application/vscode-jsonrpc\r\n\r\n{}",
		"invalid":   "Content-Length: nope\r\n\r\n{}",
		"negative":  "Content-Length: -3\r\n\r\n{}",
		"oversized": "Content-Length: 999999999\r\n\r\n{}",
	} {
		if _, err := readMessage(bufio.NewReader(strings.NewReader(framed))); !errors.Is(err, ErrTransport) {
			t.Errorf("%s frame error = %v, want ErrTransport", name, err)
		}
	}
}

func TestClientNegotiatesAndResolvesSymbols(t *testing.T) {
	manager := fakeManager(t, "normal", 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := manager.Client(ctx, "fake")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	caps := client.Capabilities()
	if !caps.Definition || !caps.References || !caps.CallHierarchy || !caps.DocumentSymbols {
		t.Fatalf("capabilities = %+v", caps)
	}
	if caps.Hover {
		t.Fatal("hover was advertised but the fake server does not support it")
	}
	if caps.SyncKind != 1 {
		t.Fatalf("sync kind = %d, want full", caps.SyncKind)
	}

	uri := FileURI("/tmp/source.go")
	locations, err := client.Definition(ctx, uri, Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locations) != 1 || locations[0].URI != "file:///def.go" {
		t.Fatalf("definition = %+v", locations)
	}
	if _, err := client.Hover(ctx, uri, Position{}); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("unsupported hover error = %v", err)
	}
	symbols, err := client.DocumentSymbols(ctx, uri)
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if len(symbols) != 2 || symbols[0].Name != "Handler" || symbols[1].Container != "Handler" {
		t.Fatalf("symbols = %+v", symbols)
	}
	items, err := client.PrepareCallHierarchy(ctx, uri, Position{Line: 1})
	if err != nil || len(items) != 1 {
		t.Fatalf("prepare call hierarchy = %+v, %v", items, err)
	}
	calls, err := client.IncomingCalls(ctx, items[0])
	if err != nil || len(calls) != 1 || calls[0].From.Name != "main" {
		t.Fatalf("incoming calls = %+v, %v", calls, err)
	}
}

func TestClientReceivesDiagnosticsForOpenedDocument(t *testing.T) {
	manager := fakeManager(t, "normal", 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := manager.Client(ctx, "fake")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	uri := FileURI("/tmp/diagnosed.go")
	if err := client.OpenDocument(uri, "go", "package diagnosed\n"); err != nil {
		t.Fatalf("OpenDocument: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if diagnostics := client.Diagnostics(uri); len(diagnostics) == 1 && diagnostics[0].Message == "fake diagnostic" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no diagnostics arrived: %+v", client.Diagnostics(uri))
}

func TestClientTimesOutAndCancels(t *testing.T) {
	manager := fakeManager(t, "slow", 2)
	start, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := manager.Client(start, "fake")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	requestCtx, requestCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer requestCancel()
	begin := time.Now()
	_, err = client.Definition(requestCtx, FileURI("/tmp/slow.go"), Position{})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("timeout took %s, cancellation did not take effect", elapsed)
	}
}

func TestClientRefusesToApplyWorkspaceEdits(t *testing.T) {
	manager := fakeManager(t, "applyEdit", 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := manager.Client(ctx, "fake")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	// The fake exits 0 only when it receives an error response. Any other
	// outcome means the edit was accepted or the client did not answer.
	done := make(chan error, 1)
	go func() { done <- client.WaitError() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server observed an accepted edit or malformed reply: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not terminate after the applyEdit request")
	}
}

func TestClientCrashFailsRequestsAndManagerBoundsRestarts(t *testing.T) {
	manager := fakeManager(t, "crash", 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := manager.Client(ctx, "fake")
	if err != nil {
		// A crash during the handshake is also a valid surfaced failure.
		return
	}
	if _, err := client.Definition(ctx, FileURI("/tmp/x.go"), Position{}); err == nil {
		t.Fatal("request against a crashed server succeeded")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && client.Alive() {
		time.Sleep(10 * time.Millisecond)
	}
	if client.Alive() {
		t.Fatal("crashed client still reports alive")
	}
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		next, err := manager.Client(ctx, "fake")
		if err != nil {
			lastErr = err
			if strings.Contains(err.Error(), "crashed") {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		_, _ = next.Definition(ctx, FileURI("/tmp/x.go"), Position{})
		waitDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(waitDeadline) && next.Alive() {
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatalf("manager did not surface the crash bound: %v", lastErr)
}

func TestManagerIsLazyDeduplicatedAndBounded(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root, []Spec{
		fakeSpec(t, "normal", 20*time.Second),
		{Name: "other", Languages: []string{"typescript"}, Command: []string{executable(t), "-test.run=^TestLSPHelperServer$"}},
	}, 1)
	t.Cleanup(func() { _ = manager.Close() })

	for _, status := range manager.Statuses() {
		if status.Status != "stopped" {
			t.Fatalf("server started eagerly: %+v", status)
		}
	}
	if _, err := manager.Client(context.Background(), "missing"); !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("unknown server error = %v", err)
	}
	if _, err := manager.ForLanguage(context.Background(), "rust"); !errors.Is(err, ErrNoServer) {
		t.Fatalf("unknown language error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := manager.Client(ctx, "fake")
	if err != nil {
		t.Fatalf("first client: %v", err)
	}
	second, err := manager.Client(ctx, "fake")
	if err != nil || first != second {
		t.Fatalf("deduplication failed: %v", err)
	}
	if _, err := manager.Client(ctx, "other"); !errors.Is(err, ErrServerLimit) {
		t.Fatalf("server limit error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if first.Alive() {
		t.Fatal("client survived manager close")
	}
	for _, status := range manager.Statuses() {
		if status.Status != "stopped" {
			t.Fatalf("closed manager status = %+v", status)
		}
	}
}
