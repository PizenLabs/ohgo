package task

import (
	"fmt"
	"strings"
)

// RecoveryState tells a user how much of the task survived a restart.
//
// It is derived at load and never persisted: it describes the quality of the
// recovery that just happened, not a property of the task that should outlive
// the next successful checkpoint.
type RecoveryState string

// Recovery states.
const (
	// RecoveryIntact means the checkpoint and projection were both readable and
	// no record needed repair.
	RecoveryIntact RecoveryState = "INTACT"
	// RecoveryReconstructed means the task was rebuilt with missing
	// information, and the report names what was absent or repaired.
	RecoveryReconstructed RecoveryState = "RECONSTRUCTED"
	// RecoveryNeedsHuman means the recovered state is valid but cannot make
	// progress without an explicit human decision (for example, a task with no
	// completion contract).
	RecoveryNeedsHuman RecoveryState = "NEEDS_HUMAN"
	// RecoveryBlocked means no consistent task could be recovered. The task is
	// not silently replaced or started over.
	RecoveryBlocked RecoveryState = "BLOCKED"
)

// Valid reports whether the state is defined.
func (s RecoveryState) Valid() bool {
	switch s {
	case RecoveryIntact, RecoveryReconstructed, RecoveryNeedsHuman, RecoveryBlocked:
		return true
	default:
		return false
	}
}

// RecoveryReport is the truthful outcome of one load.
type RecoveryReport struct {
	// State is the recovery classification.
	State RecoveryState
	// Detail explains the classification in observed terms.
	Detail string
	// Missing names the artifacts or records that were absent or repaired.
	Missing []string
	// Outstanding is the number of todos still open after recovery. It is a
	// fact about the restored work, not a completion claim.
	Outstanding int
}

// Empty reports whether no recovery happened.
func (r RecoveryReport) Empty() bool { return r.State == "" }

// Label renders a short user-facing classification.
func (r RecoveryReport) Label() string {
	if r.Empty() {
		return ""
	}
	label := string(r.State)
	if r.Detail != "" {
		label += ": " + r.Detail
	}
	if r.Outstanding > 0 {
		label += fmt.Sprintf(" (%d open todo item(s))", r.Outstanding)
	}
	return label
}

// recoveryReport classifies a load from the artifacts it actually read.
func recoveryReport(progressFound, mdFound bool, progressErr, mdErr error, progress Progress, t Task) RecoveryReport {
	if !progressFound && !mdFound {
		return RecoveryReport{}
	}
	report := RecoveryReport{State: RecoveryIntact}
	reconstruct := func(detail string, missing ...string) {
		if report.State == RecoveryBlocked || report.State == RecoveryNeedsHuman {
			return
		}
		report.State = RecoveryReconstructed
		if report.Detail == "" {
			report.Detail = detail
		}
		report.Missing = append(report.Missing, missing...)
	}
	needsHuman := func(detail string) {
		if report.State == RecoveryBlocked {
			return
		}
		if report.Detail != "" {
			detail = report.Detail + "; " + detail
		}
		report.State = RecoveryNeedsHuman
		report.Detail = detail
	}
	blocked := func(detail string) {
		if report.Detail != "" {
			detail = report.Detail + "; " + detail
		}
		report.State = RecoveryBlocked
		report.Detail = detail
	}

	switch {
	case progressErr != nil && mdErr != nil:
		blocked("neither the checkpoint nor the projection could be read")
	case progressErr != nil:
		reconstruct("recovered from the projection; the checkpoint was unreadable", ProgressFileName)
	case mdErr != nil:
		reconstruct("recovered from the checkpoint; the projection was unreadable", TaskFileName)
	}

	if report.State != RecoveryBlocked {
		switch {
		case !progressFound:
			reconstruct("recovered from the projection; no checkpoint was present", ProgressFileName)
		case !mdFound:
			reconstruct("recovered from the checkpoint; no projection was present", TaskFileName)
		}
	}
	if report.State != RecoveryBlocked && progress.ContextProblem != "" {
		reconstruct("task context could not be restored: "+progress.ContextProblem, "structured context")
	}
	if report.State != RecoveryBlocked && progress.HandoffProblem != "" {
		reconstruct("the handoff record could not be restored: "+progress.HandoffProblem, "handoff record")
	}
	if report.State != RecoveryBlocked && progress.CompletionProblem != "" {
		reconstruct("the completion proof could not be restored: "+progress.CompletionProblem, "completion proof")
	}
	if report.State != RecoveryBlocked && progress.ContextDropped > 0 {
		reconstruct(fmt.Sprintf("%d records were invalid and were repaired or dropped", progress.ContextDropped), "damaged records")
	}
	if report.State != RecoveryBlocked && !t.Status.IsTerminal() && t.Contract == nil {
		needsHuman("the task has no completion contract; it can be cancelled or replaced, not completed")
	}
	if report.State == RecoveryIntact && (t.Status == StatusPaused || t.Status == StatusRunning || t.Status == StatusPending) {
		report.Detail = "interrupted work restored from a valid checkpoint"
	}
	for _, todo := range t.Todos {
		if todo.Status != TodoDone {
			report.Outstanding++
		}
	}
	if len(report.Missing) > 0 {
		report.Detail += " (missing: " + strings.Join(uniqueStrings(report.Missing), ", ") + ")"
	}
	return report
}
