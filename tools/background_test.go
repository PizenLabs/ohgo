package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/agent"
)

// bgCtx is a context with no deadline, for jobs that must outlive the call.
func bgCtx() agent.Context {
	return agent.Context{Ctx: context.Background()}
}

// spawnBackground starts a command as a background job and returns the tool
// pair sharing one table, so tests exercise the production wiring.
func spawnPair(root string) (*BashTool, *WaitTool) {
	bash := NewBashTool(root)
	return bash, NewWaitTool(bash.Jobs())
}

func bgArgs(command string) map[string]any {
	return map[string]any{"command": command, "background": true}
}

// TestBashBackgroundReturnsImmediately is the capability this increment buys:
// a slow command must not occupy the turn that started it.
func TestBashBackgroundReturnsImmediately(t *testing.T) {
	bash, _ := spawnPair(t.TempDir())

	start := time.Now()
	result := bash.Execute(bgCtx(), bgArgs("sleep 30"))
	elapsed := time.Since(start)

	if result.IsError {
		t.Fatalf("spawn: %s", result.Text)
	}
	if elapsed > 10*time.Second {
		t.Errorf("spawn took %v, want it to return at once", elapsed)
	}
	if !strings.Contains(result.Text, "job-1") {
		t.Errorf("result = %q, want the job id", result.Text)
	}
	if !strings.Contains(result.Text, "wait") {
		t.Errorf("result = %q, want it to point at wait", result.Text)
	}
}

// TestBackgroundOutputCollectedViaWait proves the round trip: spawn, wait,
// and the output arrives with an explicit completion.
func TestBackgroundOutputCollectedViaWait(t *testing.T) {
	bash, wait := spawnPair(t.TempDir())

	spawned := bash.Execute(bgCtx(), bgArgs("echo hello-background"))
	if spawned.IsError {
		t.Fatalf("spawn: %s", spawned.Text)
	}

	result := wait.Execute(bgCtx(), map[string]any{"jobId": "job-1", "timeout": float64(10000)})
	if result.IsError {
		t.Fatalf("wait: %s", result.Text)
	}
	if !strings.Contains(result.Text, "hello-background") {
		t.Errorf("result = %q, want the job's output", result.Text)
	}
	if !strings.Contains(result.Text, "finished") || !strings.Contains(result.Text, "exit code 0") {
		t.Errorf("result = %q, want an explicit completion", result.Text)
	}
}

// TestWaitIsIncremental keeps repeated waits from refilling the context with
// the same build log: the second wait must not repeat the first.
func TestWaitIsIncremental(t *testing.T) {
	bash, wait := spawnPair(t.TempDir())

	if spawned := bash.Execute(bgCtx(), bgArgs("echo one && echo two")); spawned.IsError {
		t.Fatalf("spawn: %s", spawned.Text)
	}
	first := wait.Execute(bgCtx(), map[string]any{"jobId": "job-1", "timeout": float64(10000)})
	if first.IsError {
		t.Fatalf("first wait: %s", first.Text)
	}

	second := wait.Execute(bgCtx(), map[string]any{"jobId": "job-1", "timeout": float64(2000)})
	if second.IsError {
		t.Fatalf("second wait: %s", second.Text)
	}
	if strings.Contains(second.Text, "one") || strings.Contains(second.Text, "two") {
		t.Errorf("second wait = %q, want no repeated output", second.Text)
	}
	if !second.Useless {
		t.Errorf("second wait = %q, want it marked elidable: it adds nothing to context", second.Text)
	}
}

// TestWaitDeliversOutputWhileRunning proves output does not wait for exit:
// a job that prints early and sleeps on must have its first line collectable
// while still running.
func TestWaitDeliversOutputWhileRunning(t *testing.T) {
	bash, wait := spawnPair(t.TempDir())

	if spawned := bash.Execute(bgCtx(), bgArgs("echo first-line; sleep 5; echo second-line")); spawned.IsError {
		t.Fatalf("spawn: %s", spawned.Text)
	}

	first := wait.Execute(bgCtx(), map[string]any{"jobId": "job-1", "timeout": float64(10000)})
	if first.IsError {
		t.Fatalf("first wait: %s", first.Text)
	}
	if !strings.Contains(first.Text, "first-line") {
		t.Errorf("first wait = %q, want the early line", first.Text)
	}
	if strings.Contains(first.Text, "second-line") {
		t.Errorf("first wait = %q, want only what had arrived", first.Text)
	}
	if !strings.Contains(first.Text, "still running") {
		t.Errorf("first wait = %q, want the running state stated", first.Text)
	}

	second := wait.Execute(bgCtx(), map[string]any{"jobId": "job-1", "timeout": float64(15000)})
	if second.IsError {
		t.Fatalf("second wait: %s", second.Text)
	}
	if !strings.Contains(second.Text, "second-line") || !strings.Contains(second.Text, "finished") {
		t.Errorf("second wait = %q, want the late line plus completion", second.Text)
	}
}

// TestWaitWithNoJobsReturnsImmediately is the hang guard: blocking until
// timeout when no job can ever produce output would be a hang with a delay.
func TestWaitWithNoJobsReturnsImmediately(t *testing.T) {
	_, wait := spawnPair(t.TempDir())

	start := time.Now()
	result := wait.Execute(bgCtx(), map[string]any{"timeout": float64(60000)})
	elapsed := time.Since(start)

	if !result.IsError {
		t.Fatalf("wait with no jobs succeeded: %q", result.Text)
	}
	if elapsed > 5*time.Second {
		t.Errorf("wait took %v with nothing to wait for, want an immediate return", elapsed)
	}
	if !strings.Contains(result.Text, "no background jobs") {
		t.Errorf("result = %q, want it to state the situation", result.Text)
	}
}

// TestWaitOnAnUnknownJob names the id, so a model that mistyped it can fix
// the call rather than retry blindly.
func TestWaitOnAnUnknownJob(t *testing.T) {
	bash, wait := spawnPair(t.TempDir())

	if spawned := bash.Execute(bgCtx(), bgArgs("echo hi")); spawned.IsError {
		t.Fatalf("spawn: %s", spawned.Text)
	}
	result := wait.Execute(bgCtx(), map[string]any{"jobId": "job-99"})
	if !result.IsError {
		t.Fatalf("wait on job-99 succeeded: %q", result.Text)
	}
	if !strings.Contains(result.Text, "job-99") {
		t.Errorf("result = %q, want it to name the unknown id", result.Text)
	}
}

// TestBackgroundTimeoutKillsARunaway bounds what a forgotten job can cost:
// the timeout is a kill-after, not a wait bound.
func TestBackgroundTimeoutKillsARunaway(t *testing.T) {
	bash, wait := spawnPair(t.TempDir())

	spawned := bash.Execute(bgCtx(), map[string]any{
		"command": "sleep 60", "background": true, "timeout": float64(300),
	})
	if spawned.IsError {
		t.Fatalf("spawn: %s", spawned.Text)
	}

	result := wait.Execute(bgCtx(), map[string]any{"jobId": "job-1", "timeout": float64(10000)})
	if result.IsError {
		t.Fatalf("wait: %s", result.Text)
	}
	if !strings.Contains(result.Text, "killed after its timeout") {
		t.Errorf("result = %q, want the kill stated", result.Text)
	}
}

// TestBackgroundSpawnIsShared keeps parallel launches possible: only the
// foreground call serialises the batch.
func TestBackgroundSpawnIsShared(t *testing.T) {
	bash := NewBashTool(t.TempDir())

	if !bash.Exclusive(map[string]any{"command": "make -j"}) {
		t.Error("a foreground call must stay exclusive: concurrent shells interleave output")
	}
	if bash.Exclusive(bgArgs("sleep 60")) {
		t.Error("a background spawn must be shared: it produces no output to interleave")
	}
	// Malformed input stays exclusive: Execute will refuse it, and until then
	// a call that might run in the foreground must not overlap one that does.
	if !bash.Exclusive(map[string]any{"command": "x", "background": "yes"}) {
		t.Error("a malformed background flag must fall back to exclusive")
	}
}

// TestTooManyRunningJobsAreRefused bounds process exhaustion: the refusal
// names the limit so the model waits and retries instead of guessing.
func TestTooManyRunningJobsAreRefused(t *testing.T) {
	bash, _ := spawnPair(t.TempDir())

	for i := 0; i < MaxRunningBackgroundJobs; i++ {
		if result := bash.Execute(bgCtx(), bgArgs("sleep 60")); result.IsError {
			t.Fatalf("spawn %d: %s", i, result.Text)
		}
	}
	result := bash.Execute(bgCtx(), bgArgs("sleep 60"))
	if !result.IsError {
		t.Fatal("a ninth concurrent job was accepted")
	}
	if !strings.Contains(result.Text, "too many running background jobs") {
		t.Errorf("result = %q, want the refusal to name the limit", result.Text)
	}
}

// TestOldestCompletedJobsAreEvicted bounds the table: completed jobs stay for
// late waits, but the table must not grow without bound across a session.
func TestOldestCompletedJobsAreEvicted(t *testing.T) {
	bash, wait := spawnPair(t.TempDir())

	for wave := 0; wave < 5; wave++ {
		for i := 0; i < MaxRunningBackgroundJobs; i++ {
			if result := bash.Execute(bgCtx(), bgArgs("true")); result.IsError {
				t.Fatalf("wave %d spawn %d: %s", wave, i, result.Text)
			}
		}
		// Instant commands still occupy a running slot between Start and
		// reap, so each wave drains before the next: spawning all 40 at
		// once would correctly trip the running-jobs cap instead.
		deadline := time.Now().Add(10 * time.Second)
		for {
			any := bash.Jobs().consumeAny()
			if any.drained || any.empty || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Forty completed jobs against a cap of 32: the oldest are gone, and the
	// table still accepts new work.
	if result := bash.Execute(bgCtx(), bgArgs("true")); result.IsError {
		t.Fatalf("final spawn: %s", result.Text)
	}

	result := wait.Execute(bgCtx(), map[string]any{"jobId": "job-1"})
	if !result.IsError || !strings.Contains(result.Text, "unknown background job") {
		t.Errorf("result = %q, want the evicted job reported unknown", result.Text)
	}
}

// TestWaitHonoursCancellation stops a blocked wait at once: a cancelled run
// must not linger until the wait timeout.
func TestWaitHonoursCancellation(t *testing.T) {
	bash, wait := spawnPair(t.TempDir())

	if spawned := bash.Execute(bgCtx(), bgArgs("sleep 60")); spawned.IsError {
		t.Fatalf("spawn: %s", spawned.Text)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	result := wait.Execute(agent.Context{Ctx: ctx}, map[string]any{"jobId": "job-1", "timeout": float64(60000)})
	elapsed := time.Since(start)

	if !result.IsError {
		t.Fatalf("a cancelled wait succeeded: %q", result.Text)
	}
	if elapsed > 5*time.Second {
		t.Errorf("a cancelled wait took %v", elapsed)
	}
}

// TestWaitIsReadTier proves observation is classified as observation: a wait
// never starts, stops, or alters a job, so it must not trigger an exec
// approval.
func TestWaitIsReadTier(t *testing.T) {
	tool := NewWaitTool(nil)
	if got := tool.Approval().Tier; got != agent.TierRead {
		t.Errorf("tier = %q, want read", got)
	}
	// The read-only policy already listed "wait" before the tool existed;
	// now the name resolves to something real.
	if !agent.ReadOnlyToolNames[tool.Name()] {
		t.Errorf("%q is read-tier but missing from the read-only set", tool.Name())
	}
}

// TestWaitOnAnyJobPicksUnreadOutput proves the default (no id) waits on the
// oldest job with something to say, not the oldest job outright.
func TestWaitOnAnyJobPicksUnreadOutput(t *testing.T) {
	bash, wait := spawnPair(t.TempDir())

	if spawned := bash.Execute(bgCtx(), bgArgs("echo from-first")); spawned.IsError {
		t.Fatalf("spawn first: %s", spawned.Text)
	}
	if spawned := bash.Execute(bgCtx(), bgArgs("sleep 30")); spawned.IsError {
		t.Fatalf("spawn second: %s", spawned.Text)
	}

	result := wait.Execute(bgCtx(), map[string]any{"timeout": float64(10000)})
	if result.IsError {
		t.Fatalf("wait: %s", result.Text)
	}
	if !strings.Contains(result.Text, "from-first") {
		t.Errorf("result = %q, want the finished job's output, not the sleeper", result.Text)
	}
}
