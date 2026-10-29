package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/PizenLabs/ohgo/internal/agent"
	"github.com/PizenLabs/ohgo/internal/continuity/recovery"
	"github.com/PizenLabs/ohgo/internal/tui"
)

// recoveryService is the CLI's adapter between the runtime and the recovery
// store.
//
// It owns two things the rest of the program must not own: the store handle and
// the controller cache. Everything it exposes is either capture-only (Journal)
// or human-command-only (Undo, Redo), so no model-reachable path can reach a
// restore.
type recoveryService struct {
	store *recovery.Store
	root  string
	// err records why recovery is unavailable. It is kept rather than
	// discarding the service: a mutating turn must be refused when its recovery
	// point cannot be captured, not silently recorded nowhere.
	err error
}

// openRecoveryService opens the machine-level recovery store for a workspace.
//
// A failure is not fatal to the process — read-only sessions and diagnostics
// still work — but it is fatal to mutation: Journal returns a seam that refuses
// every edit boundary.
func openRecoveryService(root string) *recoveryService {
	service := &recoveryService{root: root}
	path, err := recovery.StorePath(root)
	if err != nil {
		service.err = err
		return service
	}
	store, err := recovery.Open(path)
	if err != nil {
		service.err = err
		return service
	}
	service.store = store
	return service
}

// Close releases the store handle.
func (s *recoveryService) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

// Err returns the reason recovery is unavailable, or nil.
func (s *recoveryService) Err() error {
	if s == nil {
		return errors.New("workspace recovery is unavailable in this session")
	}
	return s.err
}

// controller builds the scope-bound recovery façade for a session.
func (s *recoveryService) controller(sessionID int64) (*recovery.Controller, error) {
	if s == nil {
		return nil, errors.New("workspace recovery is unavailable in this session")
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.store == nil {
		return nil, errors.New("workspace recovery is unavailable in this session")
	}
	scope, err := recovery.NewScope(sessionID, s.root)
	if err != nil {
		return nil, err
	}
	return s.store.Controller(scope)
}

// Journal implements tui.Recoverer.
func (s *recoveryService) Journal(sessionID int64) agent.EditJournal {
	controller, err := s.controller(sessionID)
	if err != nil {
		return refusingJournal{err: err}
	}
	return controller
}

// Undo implements tui.Recoverer.
func (s *recoveryService) Undo(ctx context.Context, sessionID int64) recovery.Result {
	controller, err := s.controller(sessionID)
	if err != nil {
		return recovery.Result{Op: recovery.OpUndo, State: recovery.StateFailed, Reason: err.Error()}
	}
	return controller.Undo(ctx)
}

// Redo implements tui.Recoverer.
func (s *recoveryService) Redo(ctx context.Context, sessionID int64) recovery.Result {
	controller, err := s.controller(sessionID)
	if err != nil {
		return recovery.Result{Op: recovery.OpRedo, State: recovery.StateFailed, Reason: err.Error()}
	}
	return controller.Redo(ctx)
}

// Inspection implements tui.Recoverer.
func (s *recoveryService) Inspection(ctx context.Context, sessionID int64) string {
	controller, err := s.controller(sessionID)
	if err != nil {
		return "workspace recovery is unavailable: " + err.Error()
	}
	message, needsInspection := controller.Inspection(ctx)
	if !needsInspection {
		return ""
	}
	return message
}

// refusingJournal turns a missing recovery store into a refusal.
//
// It is the difference between "no recovery" and "unrecoverable mutation": the
// loop asks for a checkpoint before it authorizes a mutating batch, and an
// error there stops the batch. Silently proceeding would mean the one class of
// change the user most needs to undo is the one that cannot be.
type refusingJournal struct {
	err error
}

// BeginEdit implements agent.EditJournal.
func (j refusingJournal) BeginEdit(context.Context, agent.EditBoundary) (agent.EditTransaction, error) {
	return nil, fmt.Errorf("workspace recovery is unavailable: %w", j.err)
}

// formatRecoveryResult renders an outcome for the plain REPL.
//
// The wording comes from the same formatter the full-screen UI uses: a command
// must not mean one thing in one interface and something else in the other.
func formatRecoveryResult(result recovery.Result) string {
	return tui.FormatRecoveryResult(result)
}
