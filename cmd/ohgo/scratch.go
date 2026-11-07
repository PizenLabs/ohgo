package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/PizenLabs/ohgo/internal/history"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// installPromptScratch wires ephemeral oversized-prompt storage into the live
// program and starts the asynchronous stale sweep.
//
// The sweep runs at boot so files orphaned by a crash or a killed terminal do
// not accumulate forever. Anything younger than the stale age is left alone:
// another ohgo process in the same project may still own it.
func installPromptScratch(program *tui.Program, root string) *history.Scratch {
	scratch := history.NewScratch(root)
	program.SetPromptScratch(scratch)
	go func() {
		_, _ = scratch.SweepStale(history.ScratchStaleAge)
	}()
	return scratch
}

// installScratchSignalCleanup removes active scratch files when the process is
// terminated by a signal rather than through the UI's normal shutdown.
//
// The handler restores and re-raises the signal after cleanup, so it preserves
// the process's ordinary exit status instead of turning a forced termination
// into a silent success. Callers must defer the returned stop immediately: it
// is the only path that releases the handler goroutine on a normal shutdown.
func installScratchSignalCleanup(cleanup func()) (stop func()) {
	return startScratchSignalCleanup(cleanup, reRaiseProcessSignal)
}

// startScratchSignalCleanup installs the handler and returns an idempotent stop
// function. stop unregisters the signal, then joins the handler goroutine:
// Program.Run's deferred teardown must not race a cleanup callback that is
// still touching scratch files. reexit is a seam for tests; production passes
// reRaiseProcessSignal.
func startScratchSignalCleanup(cleanup func(), reexit func(os.Signal)) (stop func()) {
	if cleanup == nil {
		return func() {}
	}
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
	received := make(chan os.Signal, 1)
	signal.Notify(received, signals...)
	done := make(chan struct{})
	var once sync.Once
	var handler sync.WaitGroup
	handler.Add(1)
	go func() {
		defer handler.Done()
		select {
		case sig := <-received:
			handleScratchSignal(cleanup, sig, reexit)
		case <-done:
		}
	}()
	return func() {
		once.Do(func() {
			signal.Stop(received)
			close(done)
		})
		handler.Wait()
	}
}

// handleScratchSignal is the testable seam between signal observation and
// process termination. cleanup runs first; reexit preserves the original
// signal semantics.
func handleScratchSignal(cleanup func(), sig os.Signal, reexit func(os.Signal)) {
	if cleanup != nil {
		cleanup()
	}
	if reexit != nil {
		reexit(sig)
	}
}

// reRaiseProcessSignal restores the default action and delivers the same
// signal to this process.
//
// Reset happens before delivery: the restored default action terminates the
// process, so this handler cannot observe its own signal and run cleanup twice.
func reRaiseProcessSignal(sig os.Signal) {
	system, ok := sig.(syscall.Signal)
	if !ok {
		os.Exit(1)
	}
	signal.Reset(system)
	if err := syscall.Kill(os.Getpid(), system); err != nil {
		// The process can no longer safely continue: the handler is gone and
		// the original termination status cannot be reproduced. Fall back to a
		// deterministic failure rather than returning to normal execution.
		os.Exit(1)
	}
}
