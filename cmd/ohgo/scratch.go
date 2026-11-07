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
// into a silent success. The returned stop function unregisters the handler
// when Program.Run returns normally.
func installScratchSignalCleanup(cleanup func()) (stop func()) {
	if cleanup == nil {
		return func() {}
	}
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
	received := make(chan os.Signal, 1)
	signal.Notify(received, signals...)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case sig := <-received:
			handleScratchSignal(cleanup, sig, reRaiseProcessSignal)
		case <-done:
		}
	}()
	return func() {
		once.Do(func() {
			signal.Stop(received)
			close(done)
		})
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
func reRaiseProcessSignal(sig os.Signal) {
	if system, ok := sig.(syscall.Signal); ok {
		signal.Reset(system)
		_ = syscall.Kill(os.Getpid(), system)
		return
	}
	os.Exit(1)
}
