package tools

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/PizenLabs/ohgo/internal/execution/sandbox"
)

// Background job tuning, shared with tests.
const (
	// MaxRunningBackgroundJobs caps how many background jobs may run at once.
	// A model that starts unbounded parallel builds would exhaust processes
	// and file descriptors; the refusal names the limit so it can wait and
	// retry instead of guessing.
	MaxRunningBackgroundJobs = 8
	// MaxRetainedBackgroundJobs caps the table itself. Completed jobs stay
	// until evicted so a late wait still finds them, but the table must not
	// grow without bound across a long session.
	MaxRetainedBackgroundJobs = 32
	// WaitDefaultTimeout bounds one wait call when the model picks none.
	WaitDefaultTimeout = 2 * time.Minute
	// WaitMaxTimeout is the largest bound a wait call may request.
	WaitMaxTimeout = 10 * time.Minute
	// waitPollInterval is how often a blocked wait re-checks for output.
	// Output wakes on this granularity rather than instantly; 50 ms is
	// invisible next to model latencies and avoids a busy loop.
	waitPollInterval = 50 * time.Millisecond
	// exitSettleWindow bounds how long a wait that just received output gives
	// the reaper to record the exit before reporting the job as still running.
	//
	// os/exec copies a command's final bytes before Wait returns, and the
	// reaper records the exit only after Wait, so output and completion are
	// observed in either order. Without this settle, a fast command is
	// intermittently reported with its output but no exit code — the exact
	// failure a loaded machine widens. The window is paid only when output
	// arrives while the job is not yet marked finished.
	exitSettleWindow = 50 * time.Millisecond
	// exitSettleStep is how often the settle window re-checks the exit, so a
	// completed job is reported as soon as the reaper runs rather than at the
	// end of the window.
	exitSettleStep = 5 * time.Millisecond
)

// BackgroundJobs is the table of detached shell jobs for one tool set.
//
// It exists because a foreground-only shell forces the agent to sit idle
// through every build and test run. A background job returns immediately with
// an id, and the wait tool collects its output incrementally.
//
// Scope notes, stated plainly because they differ from the original: jobs are
// shared by every holder of the tool set (a subagent waits on the same table
// as its parent — there is no per-agent ownership), and there is no PTY or
// interactive session support. What is here is the bounded core: spawn,
// incremental collect, timeout kill.
type BackgroundJobs struct {
	// mu guards everything. Writers are process-copy goroutines, the killer
	// is a timer, and waiters poll — all concurrent with each other.
	mu sync.Mutex
	// next numbers jobs in spawn order. Ids are never reused within a
	// session, so "job-3" cannot silently mean two different commands.
	next int
	// jobs holds every retained job by id.
	jobs map[string]*backgroundJob
	// order records insertion order for eviction.
	order []string
}

// backgroundJob is one detached process.
type backgroundJob struct {
	// id is the stable handle handed to the model.
	id string
	// command is the shell text, for result headers and verification evidence.
	command string
	// root is the workspace the command ran in, so wait can classify the
	// command's verification scope against the same root bash used.
	root string
	// mutates is the admission classification of the command. It describes
	// what the command may change; it is not evidence that a change happened.
	mutates bool
	// startedAt bounds the elapsed display.
	startedAt time.Time
	// cmd is the running process, nil once reaped.
	cmd *exec.Cmd
	// combined holds stdout and stderr interleaved in arrival order. Two
	// copy goroutines append under the table lock, so the order is the
	// order the lock saw — close enough to chronological for a transcript.
	combined bytes.Buffer
	// truncated marks that old bytes were dropped at the cap. A wait that
	// has not yet reported it must say so, or output silently begins
	// mid-stream.
	truncated bool
	// truncationNoticed tracks whether the truncation was reported.
	truncationNoticed bool
	// readOffset is how many combined bytes a waiter has already received.
	readOffset int
	// done marks process exit. finishedAt, exitCode and timedOut are valid
	// only once done is true.
	done       bool
	finishedAt time.Time
	exitCode   int
	timedOut   bool
}

// NewBackgroundJobs returns an empty job table.
func NewBackgroundJobs() *BackgroundJobs {
	return &BackgroundJobs{jobs: map[string]*backgroundJob{}}
}

// runningLocked counts unfinished jobs. The caller must hold mu.
func (t *BackgroundJobs) runningLocked() int {
	n := 0
	for _, job := range t.jobs {
		if !job.done {
			n++
		}
	}
	return n
}

// reserve creates a job entry and returns its id.
//
// It fails when too many jobs are already running, or when the table is full
// of completed jobs with nothing evictable — a table of only running jobs
// cannot make room without killing something it was told to keep.
func (t *BackgroundJobs) reserve(command string, mutates bool, root string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.runningLocked() >= MaxRunningBackgroundJobs {
		return "", fmt.Errorf("too many running background jobs (max %d): wait for one to finish and retry",
			MaxRunningBackgroundJobs)
	}
	if len(t.jobs) >= MaxRetainedBackgroundJobs {
		evicted := false
		kept := t.order[:0]
		for _, id := range t.order {
			job, ok := t.jobs[id]
			if !ok {
				// A discarded reservation: the map entry is gone but the
				// order entry stayed. Dropping it here is what keeps the
				// order slice from growing without bound — and from
				// dereferencing a nil job below.
				continue
			}
			if job.done && !evicted {
				delete(t.jobs, id)
				evicted = true
				continue
			}
			kept = append(kept, id)
		}
		t.order = kept
		if !evicted {
			return "", fmt.Errorf("too many background jobs (max %d retained)", MaxRetainedBackgroundJobs)
		}
	}

	t.next++
	id := fmt.Sprintf("job-%d", t.next)
	t.jobs[id] = &backgroundJob{
		id: id, command: command, root: root,
		mutates: mutates, startedAt: time.Now(),
	}
	t.order = append(t.order, id)
	return id, nil
}

// attach records the process for a reserved job.
func (t *BackgroundJobs) attach(id string, cmd *exec.Cmd) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if job, ok := t.jobs[id]; ok {
		job.cmd = cmd
	}
}

// discard removes a reservation that never started, so a failed spawn does
// not occupy a table slot or consume an id the model saw succeed.
func (t *BackgroundJobs) discard(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.jobs, id)
}

// append adds output to a job, dropping the oldest bytes at the cap.
//
// Dropped bytes are silent data loss unless reported, so the first wait after
// a drop carries a truncation notice instead of starting mid-stream.
func (t *BackgroundJobs) append(id string, p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	job, ok := t.jobs[id]
	if !ok {
		return
	}
	if job.combined.Len()+len(p) > BashMaxOutput {
		overflow := job.combined.Len() + len(p) - BashMaxOutput
		// Drop from the front, but never drop unread bytes without noticing:
		// anything already handed out stays valid because readOffset only
		// moves forward through retained bytes.
		kept := job.combined.Bytes()
		if overflow > len(kept) {
			overflow = len(kept)
		}
		rest := append([]byte(nil), kept[overflow:]...)
		if job.readOffset > overflow {
			job.readOffset -= overflow
		} else {
			job.readOffset = 0
		}
		job.combined.Reset()
		job.combined.Write(rest)
		job.truncated = true
	}
	job.combined.Write(p)
}

// finish records a job's exit. It is safe to call twice; the first call wins,
// because a timeout kill and a natural exit can race.
func (t *BackgroundJobs) finish(id string, exitCode int, timedOut bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	job, ok := t.jobs[id]
	if !ok || job.done {
		return
	}
	job.done = true
	job.finishedAt = time.Now()
	job.exitCode = exitCode
	job.timedOut = timedOut
	job.cmd = nil
}

// kill stops a running job. It reports whether anything was running: a kill
// against an already-finished job is a no-op, not an error, because the
// timeout timer and natural exit race by construction.
func (t *BackgroundJobs) kill(id string) bool {
	t.mu.Lock()
	job, ok := t.jobs[id]
	if !ok || job.done {
		t.mu.Unlock()
		return false
	}
	cmd := job.cmd
	t.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return false
	}
	// Kill the process group: the shell's children may still be writing when
	// only the shell is killed, and a background job that outlives its timeout
	// is exactly the leak the timeout exists to prevent.
	return sandbox.Kill(cmd) == nil
}

// jobSnapshot is what one locked inspection of a job yields.
//
// It is a copy, never a pointer into the table: the process-copy goroutines
// keep appending while a waiter formats its answer, so anything shared would
// be a data race.
type jobSnapshot struct {
	// ok reports the job existed.
	ok bool
	// done reports the process exited.
	done bool
	// exitCode and timedOut describe the exit, valid only when done.
	exitCode int
	timedOut bool
	// command, root, and mutates carry the job's own execution classification
	// so wait can report evidence without guessing from text.
	command string
	root    string
	mutates bool
	// elapsed is how long the job has (or did) run.
	elapsed time.Duration
	// text is the output accumulated since the last consume; the cursor is
	// already advanced past it.
	text string
	// truncationNotice must prefix the text: old bytes were dropped at the
	// cap and this is the first report since.
	truncationNotice bool
}

// consume returns the unread output of a job and advances its cursor, all
// under one lock.
func (t *BackgroundJobs) consume(id string) jobSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	job, ok := t.jobs[id]
	if !ok {
		return jobSnapshot{}
	}
	return t.consumeLocked(job)
}

// consumeLocked snapshots a job the caller already holds the lock for.
func (t *BackgroundJobs) consumeLocked(job *backgroundJob) jobSnapshot {
	snap := jobSnapshot{
		ok:       true,
		done:     job.done,
		exitCode: job.exitCode,
		timedOut: job.timedOut,
		command:  job.command,
		root:     job.root,
		mutates:  job.mutates,
		elapsed:  time.Since(job.startedAt),
	}
	if job.done {
		snap.elapsed = job.finishedAt.Sub(job.startedAt)
	}
	kept := job.combined.Bytes()
	if job.readOffset < len(kept) {
		snap.text = string(kept[job.readOffset:])
		job.readOffset = len(kept)
	}
	if job.truncated && !job.truncationNoticed {
		snap.truncationNotice = true
		job.truncationNoticed = true
	}
	return snap
}

// completion reports a job's exit fields without consuming its output.
//
// It lets a waiter upgrade a snapshot taken moments before the reaper recorded
// the exit, so output and completion can be reported in the same result. The
// second return is false when the job is unknown or still running.
func (t *BackgroundJobs) completion(id string) (jobSnapshot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	job, ok := t.jobs[id]
	if !ok || !job.done {
		return jobSnapshot{}, false
	}
	return jobSnapshot{
		ok:       true,
		done:     true,
		exitCode: job.exitCode,
		timedOut: job.timedOut,
		command:  job.command,
		root:     job.root,
		mutates:  job.mutates,
		elapsed:  job.finishedAt.Sub(job.startedAt),
	}, true
}

// anySnapshot describes the oldest job worth reporting on: the oldest with
// unread output first, else the oldest still running, else nothing.
type anySnapshot struct {
	// id is the job the snapshot is about, empty when there is none.
	id string
	// empty reports the table holds no jobs at all.
	empty bool
	// drained reports every retained job finished and was fully read: nothing
	// will ever arrive, so waiting would hang until timeout.
	drained bool
	jobSnapshot
}

// consumeAny inspects the whole table under one lock.
func (t *BackgroundJobs) consumeAny() anySnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.jobs) == 0 {
		return anySnapshot{empty: true}
	}
	var oldestRunning *backgroundJob
	for _, id := range t.order {
		job, ok := t.jobs[id]
		if !ok {
			continue
		}
		kept := job.combined.Bytes()
		if job.readOffset < len(kept) {
			return anySnapshot{id: id, jobSnapshot: t.consumeLocked(job)}
		}
		if !job.done && oldestRunning == nil {
			oldestRunning = job
		}
	}
	if oldestRunning != nil {
		return anySnapshot{id: oldestRunning.id, jobSnapshot: t.consumeLocked(oldestRunning)}
	}
	return anySnapshot{drained: true}
}

// JobInfo is a display snapshot of one background job.
type JobInfo struct {
	// ID is the stable handle, e.g. "job-1".
	ID string
	// Command is the shell text.
	Command string
	// Running reports the process has not exited.
	Running bool
	// ExitCode is valid only when Running is false.
	ExitCode int
	// Elapsed bounds the run so far (or its total, once finished).
	Elapsed time.Duration
	// OutputTail is the last bytes of combined output, for detail views.
	OutputTail string
}

// jobOutputTailBytes bounds one snapshot's tail.
const jobOutputTailBytes = 500

// Snapshot lists every retained job in spawn order.
//
// The output tails are copies: the process-copy goroutines keep appending
// while a view formats its answer.
func (t *BackgroundJobs) Snapshot() []JobInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]JobInfo, 0, len(t.jobs))
	for _, id := range t.order {
		job, ok := t.jobs[id]
		if !ok {
			continue
		}
		info := JobInfo{
			ID:      job.id,
			Command: job.command,
			Running: !job.done,
		}
		if job.done {
			info.ExitCode = job.exitCode
			info.Elapsed = job.finishedAt.Sub(job.startedAt)
		} else {
			info.Elapsed = time.Since(job.startedAt)
		}
		if tail := job.combined.Bytes(); len(tail) > jobOutputTailBytes {
			info.OutputTail = string(tail[len(tail)-jobOutputTailBytes:])
		} else {
			info.OutputTail = string(tail)
		}
		out = append(out, info)
	}
	return out
}
